package storagecleanup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	ScopeAcceptanceOnly     = "acceptance_only"
	ScopeBroadProduction    = "broad_production"
	quarantineRootRelPath   = ".loom-acceptance/.cleanup-quarantine"
	acceptanceComponentName = ".loom-acceptance"
)

var acceptanceComponentNames = map[string]struct{}{
	acceptanceComponentName: {},
	"loom-acceptance":       {},
	"loom acceptance":       {},
}

var cleanupRules = []struct {
	id          string
	description string
	re          *regexp.Regexp
}{
	{
		id:          "loom_versioned_smoke_component",
		description: "Path component looks like a LOOM versioned smoke/slice artifact, for example v0-6-2-slice-01.",
		re:          regexp.MustCompile(`(?i)^v[0-9]+[-_][0-9]+.*(slice|smoke|test)`),
	},
	{
		id:          "slice_smoke_component",
		description: "Path component looks like a slice smoke artifact.",
		re:          regexp.MustCompile(`(?i)^slice[-_][0-9]+.*(smoke|test)`),
	},
	{
		id:          "explicit_smoke_component",
		description: "Path component explicitly combines LOOM/test/slice wording with smoke.",
		re:          regexp.MustCompile(`(?i)(loom|slice|test|v[0-9]+).*(smoke)|smoke.*(loom|slice|test|v[0-9]+)`),
	},
	{
		id:          "temporary_loom_component",
		description: "Path component is an explicit LOOM temporary artifact.",
		re:          regexp.MustCompile(`(?i)^(tmp|temp)[-_].*(loom|smoke|slice|test)`),
	},
}

func Inventory(input InventoryInput) (InventoryResult, error) {
	now := resolveNow(input.Now)
	root, err := normalizeRoot(input.Root)
	if err != nil {
		return InventoryResult{}, err
	}
	result := InventoryResult{
		SchemaVersion: SchemaVersion,
		Root:          root,
		Production:    input.Production,
		GeneratedAt:   now,
		RuleSet:       ruleDescriptions(),
	}
	info, err := os.Stat(root)
	if err != nil {
		return InventoryResult{}, fmt.Errorf("stat storage root: %w", err)
	}
	if !info.IsDir() {
		return InventoryResult{}, fmt.Errorf("storage root must be a directory: %s", root)
	}
	err = filepath.WalkDir(root, func(pathValue string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("skip %s: %v", pathValue, walkErr))
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if pathValue == root {
			return nil
		}
		rel, err := filepath.Rel(root, pathValue)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("skip %s: %v", pathValue, err))
			return nil
		}
		rel = filepath.ToSlash(rel)
		if isQuarantinePath(rel) {
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := os.Lstat(pathValue)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("skip %s: %v", rel, err))
			return nil
		}
		result.Summary.Scanned++
		if !info.IsDir() {
			result.Summary.BytesScanned += info.Size()
		}
		matches := matchingRuleIDs(rel)
		if len(matches) == 0 {
			return nil
		}
		if input.Production && !input.AllowBroadProductionScan && !isAcceptancePath(rel) {
			return nil
		}
		candidate := classifyCandidate(root, rel, pathValue, info, matches)
		result.Items = append(result.Items, candidate)
		result.Summary.Candidates++
		result.Summary.CandidateBytes += candidate.SizeBytes
		if candidate.AutoApply {
			result.Summary.AutoApply++
		} else {
			result.Summary.ManualReview++
		}
		return nil
	})
	if err != nil {
		return InventoryResult{}, err
	}
	sortCandidates(result.Items)
	return result, nil
}

func Plan(input PlanInput) (CleanupPlan, error) {
	inventory, err := Inventory(InventoryInput{Root: input.Root, Production: input.Production, AllowBroadProductionScan: input.AllowBroadProductionScan, Now: input.Now})
	if err != nil {
		return CleanupPlan{}, err
	}
	scope := ""
	if input.Production && input.AllowBroadProductionScan {
		scope = ScopeBroadProduction
	} else if input.Production {
		scope = ScopeAcceptanceOnly
	}
	return CleanupPlan{
		SchemaVersion: inventory.SchemaVersion,
		Root:          inventory.Root,
		Production:    inventory.Production,
		Scope:         scope,
		GeneratedAt:   inventory.GeneratedAt,
		Summary:       inventory.Summary,
		Items:         inventory.Items,
		Warnings:      inventory.Warnings,
		RuleSet:       inventory.RuleSet,
	}, nil
}

func Apply(input ApplyInput) (ApplyResult, error) {
	now := resolveNow(input.Now)
	plan, planPath, err := resolvePlan(input)
	if err != nil {
		return ApplyResult{}, err
	}
	result := ApplyResult{
		SchemaVersion: SchemaVersion,
		Root:          plan.Root,
		PlanPath:      planPath,
		DryRun:        input.DryRun,
		DeleteNow:     input.DeleteNow,
		GeneratedAt:   now,
		Summary:       ApplySummary{Planned: len(plan.Items)},
	}
	if !input.DryRun && !input.Yes {
		result.Refused = true
		result.Refusal = "storage cleanup apply requires --yes before moving files to quarantine or deleting them"
		return result, nil
	}
	root, err := normalizeRoot(plan.Root)
	if err != nil {
		return ApplyResult{}, err
	}
	if !input.DeleteNow {
		result.QuarantineDir = cleanupQuarantineDir(root, now)
	}
	for _, item := range sortedApplyItems(plan.Items) {
		if !item.AutoApply {
			result.Summary.ManualReview++
			result.Summary.Skipped++
			result.Changes = append(result.Changes, ApplyChange{RelativePath: item.RelativePath, FullPath: item.FullPath, Action: item.Action, Status: StatusSkipped, Message: "manual review required"})
			continue
		}
		result.Summary.Eligible++
		change := applyItem(root, item, input.DryRun, input.DeleteNow, input.AcceptMetadataLoss, result.QuarantineDir)
		result.Changes = append(result.Changes, change)
		switch change.Status {
		case StatusRemoved:
			result.Summary.Removed++
		case StatusQuarantined:
			result.Summary.Quarantined++
		case StatusWouldRemove:
			result.Summary.WouldRemove++
		case StatusWouldQuarantine:
			result.Summary.WouldQuarantine++
		case StatusBlocked:
			result.Summary.Blocked++
		default:
			result.Summary.Skipped++
		}
	}
	return result, nil
}

func WritePlan(pathValue string, plan CleanupPlan) error {
	pathValue = filepath.Clean(strings.TrimSpace(pathValue))
	if pathValue == "" || pathValue == "." {
		return fmt.Errorf("plan output path is required")
	}
	if err := os.MkdirAll(filepath.Dir(pathValue), 0o755); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return os.WriteFile(pathValue, payload, 0o600)
}

func LoadPlan(pathValue string) (CleanupPlan, error) {
	payload, err := os.ReadFile(pathValue)
	if err != nil {
		return CleanupPlan{}, err
	}
	var plan CleanupPlan
	if err := json.Unmarshal(payload, &plan); err != nil {
		return CleanupPlan{}, err
	}
	if plan.SchemaVersion != SchemaVersion {
		return CleanupPlan{}, fmt.Errorf("unsupported storage cleanup plan schema_version %q", plan.SchemaVersion)
	}
	return plan, nil
}

func classifyCandidate(root, rel, fullPath string, info fs.FileInfo, matches []string) Candidate {
	kind := fileKind(info)
	action := ActionManualReview
	autoApply := false
	cleanupScope := CleanupScopeManualReview
	catalogDisposition := CatalogDispositionManualReview
	reason := "candidate requires manual review before removal"
	acceptance := isAcceptancePath(rel)
	protected := isProtectedMainDocumentsPath(rel) && !acceptance
	fidelityFindings, requiresMetadataLossAcceptance := cleanupFidelityFindings(rel, kind, info)
	switch {
	case protected:
		reason = "canonical Documents is user data; only explicit .loom-acceptance artifacts are eligible for automatic cleanup"
		catalogDisposition = CatalogDispositionNotTouched
	case !acceptance:
		reason = "candidate is outside .loom-acceptance; canonical user data requires manual review"
		catalogDisposition = CatalogDispositionNotTouched
	case kind == KindSymlink:
		action = ActionRemoveSymlink
		autoApply = true
		cleanupScope = CleanupScopeViewOnly
		catalogDisposition = CatalogDispositionUnchanged
		reason = "explicit .loom-acceptance symlink can be quarantined without deleting its target"
	case kind == KindFile:
		action = ActionRemoveFile
		autoApply = true
		cleanupScope = CleanupScopeMaterializedFile
		catalogDisposition = CatalogDispositionUnchanged
		reason = "explicit .loom-acceptance artifact can be quarantined"
	case kind == KindDir:
		action = ActionRemoveEmptyDir
		if empty, err := isDirEmpty(fullPath); err == nil && empty {
			autoApply = true
			cleanupScope = CleanupScopeViewOnly
			catalogDisposition = CatalogDispositionUnchanged
			reason = "empty candidate directory"
		} else {
			reason = "directory is not empty; remove contained safe items first or review manually"
		}
	}
	return Candidate{
		RelativePath:                   rel,
		FullPath:                       filepath.Join(root, filepath.FromSlash(rel)),
		Kind:                           kind,
		SizeBytes:                      fileSizeForCandidate(info),
		ModTime:                        info.ModTime().UTC(),
		Action:                         action,
		AutoApply:                      autoApply,
		CleanupScope:                   cleanupScope,
		CatalogDisposition:             catalogDisposition,
		Reason:                         reason,
		RuleIDs:                        matches,
		FidelityFindings:               fidelityFindings,
		RequiresMetadataLossAcceptance: requiresMetadataLossAcceptance,
	}
}

func applyItem(root string, item Candidate, dryRun bool, deleteNow bool, acceptMetadataLoss bool, quarantineDir string) ApplyChange {
	change := ApplyChange{RelativePath: item.RelativePath, FullPath: item.FullPath, Action: item.Action}
	fullPath, ok := safeItemPath(root, item)
	if !ok {
		change.Status = StatusBlocked
		change.Message = "plan item path escapes cleanup root"
		return change
	}
	if isProtectedMainDocumentsPath(item.RelativePath) {
		change.Status = StatusSkipped
		change.Message = "main/Documents requires manual review"
		return change
	}
	info, err := os.Lstat(fullPath)
	if os.IsNotExist(err) {
		change.Status = StatusAlreadySatisfied
		change.Message = "path is already absent"
		return change
	}
	if err != nil {
		change.Status = StatusBlocked
		change.Message = err.Error()
		return change
	}
	if !samePlannedFile(info, item) {
		change.Status = StatusBlocked
		change.Message = "path changed since cleanup plan was created"
		return change
	}
	if item.Action == ActionRemoveEmptyDir {
		empty, err := isDirEmpty(fullPath)
		if err != nil {
			change.Status = StatusBlocked
			change.Message = err.Error()
			return change
		}
		if !empty {
			change.Status = StatusSkipped
			change.Message = "directory is not empty"
			return change
		}
	}
	if item.RequiresMetadataLossAcceptance && !acceptMetadataLoss {
		change.Status = StatusBlocked
		change.Message = "candidate has unresolved fidelity findings; rerun with --accept-metadata-loss after review"
		return change
	}
	if dryRun {
		if deleteNow {
			change.Status = StatusWouldRemove
		} else {
			change.Status = StatusWouldQuarantine
			change.QuarantinePath = quarantinePathForItem(quarantineDir, item)
		}
		return change
	}
	if !deleteNow {
		quarantinePath := quarantinePathForItem(quarantineDir, item)
		if quarantinePath == "" {
			change.Status = StatusBlocked
			change.Message = "quarantine path is not configured"
			return change
		}
		if ok := pathWithinRoot(quarantineDir, quarantinePath); !ok {
			change.Status = StatusBlocked
			change.Message = "quarantine path escapes quarantine root"
			return change
		}
		if err := os.MkdirAll(filepath.Dir(quarantinePath), 0o755); err != nil {
			change.Status = StatusBlocked
			change.Message = err.Error()
			return change
		}
		if _, err := os.Lstat(quarantinePath); err == nil {
			change.Status = StatusBlocked
			change.Message = "quarantine target already exists"
			change.QuarantinePath = quarantinePath
			return change
		} else if err != nil && !os.IsNotExist(err) {
			change.Status = StatusBlocked
			change.Message = err.Error()
			change.QuarantinePath = quarantinePath
			return change
		}
		if err := os.Rename(fullPath, quarantinePath); err != nil {
			change.Status = StatusBlocked
			change.Message = err.Error()
			change.QuarantinePath = quarantinePath
			return change
		}
		change.Status = StatusQuarantined
		change.QuarantinePath = quarantinePath
		return change
	}
	if err := os.Remove(fullPath); err != nil {
		change.Status = StatusBlocked
		change.Message = err.Error()
		return change
	}
	change.Status = StatusRemoved
	return change
}

func cleanupFidelityFindings(rel string, kind string, info fs.FileInfo) ([]string, bool) {
	findings := []string{}
	requiresAcceptance := false
	lower := strings.ToLower(filepath.Base(filepath.ToSlash(rel)))
	if strings.HasSuffix(lower, ".loom-meta.json") {
		findings = append(findings, "metadata_sidecar")
		requiresAcceptance = true
	}
	if kind == KindOther {
		findings = append(findings, "special_file")
		requiresAcceptance = true
	}
	if kind == KindFile && info.Mode()&0o111 != 0 {
		findings = append(findings, "executable_mode")
	}
	return findings, requiresAcceptance
}

func cleanupQuarantineDir(root string, now time.Time) string {
	stamp := now.UTC().Format("20060102T150405Z")
	return filepath.Join(root, filepath.FromSlash(quarantineRootRelPath), stamp)
}

func quarantinePathForItem(quarantineDir string, item Candidate) string {
	if strings.TrimSpace(quarantineDir) == "" {
		return ""
	}
	return filepath.Join(quarantineDir, filepath.FromSlash(filepath.ToSlash(item.RelativePath)))
}

func pathWithinRoot(root, pathValue string) bool {
	root = filepath.Clean(root)
	pathValue = filepath.Clean(pathValue)
	rel, err := filepath.Rel(root, pathValue)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func isQuarantinePath(rel string) bool {
	rel = strings.Trim(filepath.ToSlash(rel), "/")
	return rel == quarantineRootRelPath || strings.HasPrefix(rel, quarantineRootRelPath+"/")
}

func isAcceptancePath(rel string) bool {
	for _, component := range strings.Split(filepath.ToSlash(rel), "/") {
		normalized := strings.ToLower(strings.TrimSpace(component))
		if _, ok := acceptanceComponentNames[normalized]; ok {
			return true
		}
	}
	return false
}

func resolvePlan(input ApplyInput) (CleanupPlan, string, error) {
	if input.Plan != nil {
		if input.Plan.SchemaVersion != SchemaVersion {
			return CleanupPlan{}, "", fmt.Errorf("unsupported storage cleanup plan schema_version %q", input.Plan.SchemaVersion)
		}
		return *input.Plan, "", nil
	}
	pathValue := filepath.Clean(strings.TrimSpace(input.PlanPath))
	if pathValue == "" || pathValue == "." {
		return CleanupPlan{}, "", fmt.Errorf("cleanup plan path is required")
	}
	plan, err := LoadPlan(pathValue)
	if err != nil {
		return CleanupPlan{}, "", err
	}
	return plan, pathValue, nil
}

func safeItemPath(root string, item Candidate) (string, bool) {
	root = filepath.Clean(root)
	fullPath := filepath.Clean(strings.TrimSpace(item.FullPath))
	if fullPath == "" || fullPath == "." {
		fullPath = filepath.Join(root, filepath.FromSlash(item.RelativePath))
	}
	rel, err := filepath.Rel(root, fullPath)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", false
	}
	return fullPath, true
}

func matchingRuleIDs(rel string) []string {
	components := strings.Split(filepath.ToSlash(rel), "/")
	matches := []string{}
	seen := map[string]bool{}
	for _, component := range components {
		component = strings.TrimSpace(component)
		if component == "" {
			continue
		}
		for _, rule := range cleanupRules {
			if rule.re.MatchString(component) && !seen[rule.id] {
				matches = append(matches, rule.id)
				seen[rule.id] = true
			}
		}
	}
	return matches
}

func ruleDescriptions() []RuleDescription {
	out := make([]RuleDescription, 0, len(cleanupRules))
	for _, rule := range cleanupRules {
		out = append(out, RuleDescription{ID: rule.id, Description: rule.description})
	}
	return out
}

func normalizeRoot(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("storage root is required")
	}
	if strings.HasPrefix(value, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if value == "~" {
			value = home
		} else if strings.HasPrefix(value, "~/") {
			value = filepath.Join(home, value[2:])
		}
	}
	if !filepath.IsAbs(value) {
		abs, err := filepath.Abs(value)
		if err != nil {
			return "", err
		}
		value = abs
	}
	return filepath.Clean(value), nil
}

func fileKind(info fs.FileInfo) string {
	mode := info.Mode()
	switch {
	case mode&os.ModeSymlink != 0:
		return KindSymlink
	case info.IsDir():
		return KindDir
	case mode.IsRegular():
		return KindFile
	default:
		return KindOther
	}
}

func fileSizeForCandidate(info fs.FileInfo) int64 {
	if info.IsDir() {
		return 0
	}
	return info.Size()
}

func samePlannedFile(info fs.FileInfo, item Candidate) bool {
	if fileKind(info) != item.Kind {
		return false
	}
	if fileSizeForCandidate(info) != item.SizeBytes {
		return false
	}
	return info.ModTime().UTC().Equal(item.ModTime.UTC())
}

func isProtectedMainDocumentsPath(rel string) bool {
	rel = strings.Trim(filepath.ToSlash(rel), "/")
	return rel == "Documents" || strings.HasPrefix(rel, "Documents/") || rel == "main/Documents" || strings.HasPrefix(rel, "main/Documents/")
}

func isDirEmpty(pathValue string) (bool, error) {
	dir, err := os.Open(pathValue)
	if err != nil {
		return false, err
	}
	defer dir.Close()
	_, err = dir.Readdirnames(1)
	if errors.Is(err, io.EOF) {
		return true, nil
	}
	return false, err
}

func sortCandidates(items []Candidate) {
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].RelativePath < items[j].RelativePath
	})
}

func sortedApplyItems(items []Candidate) []Candidate {
	out := append([]Candidate{}, items...)
	sort.SliceStable(out, func(i, j int) bool {
		return strings.Count(out[i].RelativePath, "/") > strings.Count(out[j].RelativePath, "/")
	})
	return out
}

func resolveNow(now func() time.Time) time.Time {
	if now == nil {
		return time.Now().UTC()
	}
	return now().UTC()
}
