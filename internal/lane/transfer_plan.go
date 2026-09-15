package lane

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/filesystemmeta"
)

const laneReadmeControlPath = "README.md"

func BuildTransferPlan(lanePath, policyRoot string, profile filepolicy.Profile) (TransferPlan, error) {
	if profile == "" {
		profile = filepolicy.ProfileFaithful
	}
	if _, err := filepolicy.ParseProfile(string(profile)); err != nil {
		return TransferPlan{}, err
	}
	if strings.TrimSpace(policyRoot) == "" {
		policyRoot = lanePath
	}
	resolver, err := filepolicy.NewResolver(lanePath, profile, filepolicy.ResolverOptions{
		DiscoverUserRules: profile.DiscoversUserRules(),
		PolicyRoot:        policyRoot,
	})
	if err != nil {
		return TransferPlan{}, err
	}
	plan := TransferPlan{
		SchemaVersion:     TransferPlanSchemaVersion,
		Profile:           profile,
		PolicyVersion:     filepolicy.BuiltInPolicyVersion,
		PolicyFingerprint: resolver.Fingerprint(),
		PolicyHashes:      map[string]string{},
	}
	for _, policyFile := range resolver.PolicyFiles() {
		plan.PolicyHashes[policyFile.RelativePath] = policyFile.ContentHash
	}
	if profile == filepolicy.ProfileExact {
		plan.Warnings = append(plan.Warnings, "exact mode bypasses .loomignore user rules; mandatory LOOM transfer safety still applies")
	}
	items := map[string]*PendingItem{}
	err = filepath.WalkDir(resolver.Root(), func(pathValue string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if pathValue == resolver.Root() {
			return nil
		}
		relative, err := filepath.Rel(resolver.Root(), pathValue)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if relative == laneReadmeControlPath {
			decision := laneReadmeDecision(profile, relative)
			addIgnoredTransferEntry(&plan, decision, info)
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			decision := laneUnsupportedDecision(profile, relative, "lane:symlink_not_followed")
			addIgnoredTransferEntry(&plan, decision, info)
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("symlink %s was not followed or transferred", relative))
			return nil
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			decision := laneUnsupportedDecision(profile, relative, "lane:unsupported_special_file")
			addIgnoredTransferEntry(&plan, decision, info)
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("special file %s was not transferred", relative))
			return nil
		}
		resolution, err := resolver.Resolve(relative, info.IsDir())
		if err != nil {
			return err
		}
		if !resolution.Decision.Included {
			addIgnoredTransferEntry(&plan, resolution.Decision, info)
			return nil
		}
		kind := filesystemmeta.ObjectKindRegularFile
		if info.IsDir() {
			kind = filesystemmeta.ObjectKindDirectory
			plan.DirCount++
		} else {
			plan.FileCount++
			plan.TotalBytes += info.Size()
		}
		plan.Entries = append(plan.Entries, TransferEntry{
			RelativePath: relative,
			Kind:         kind,
			Bytes:        regularFileSize(info),
			Mode:         uint32(info.Mode().Perm()),
			ModifiedAt:   info.ModTime().UTC(),
			Decision:     resolution.Decision,
		})
		addTransferItem(items, resolver.Root(), relative, info)
		return nil
	})
	if err != nil {
		return TransferPlan{}, fmt.Errorf("plan Lane transfer under %q: %w", resolver.Root(), err)
	}
	sort.Slice(plan.Entries, func(left, right int) bool { return plan.Entries[left].RelativePath < plan.Entries[right].RelativePath })
	sort.Slice(plan.Ignored, func(left, right int) bool { return plan.Ignored[left].Path < plan.Ignored[right].Path })
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		plan.Items = append(plan.Items, *items[key])
	}
	plan.InventoryHash = transferInventoryHash(plan)
	plan.Transport, err = SelectBundleTransport(plan, TransportModeAuto)
	if err != nil {
		return TransferPlan{}, err
	}
	return plan, nil
}

func laneReadmeDecision(profile filepolicy.Profile, relative string) filepolicy.Decision {
	return filepolicy.Decision{Path: relative, Included: false, Profile: profile, RuleCategory: filepolicy.RuleCategoryContract, Pattern: "lane-control:README.md", PolicyVersion: filepolicy.BuiltInPolicyVersion}
}

func laneUnsupportedDecision(profile filepolicy.Profile, relative, pattern string) filepolicy.Decision {
	return filepolicy.Decision{Path: relative, Included: false, Profile: profile, RuleCategory: filepolicy.RuleCategoryMandatorySafety, Pattern: pattern, PolicyVersion: filepolicy.BuiltInPolicyVersion}
}

func addIgnoredTransferEntry(plan *TransferPlan, decision filepolicy.Decision, info os.FileInfo) {
	plan.Ignored = append(plan.Ignored, decision)
	if info.IsDir() {
		plan.IgnoredDirCount++
		return
	}
	if info.Mode().IsRegular() {
		plan.IgnoredFileCount++
		plan.IgnoredBytes += info.Size()
	}
}

func addTransferItem(items map[string]*PendingItem, root, relative string, info os.FileInfo) {
	top := strings.Split(relative, "/")[0]
	item := items[top]
	if item == nil {
		topPath := filepath.Join(root, filepath.FromSlash(top))
		topInfo, err := os.Lstat(topPath)
		if err != nil {
			topInfo = info
		}
		kind := filesystemmeta.ObjectKindDirectory
		if !topInfo.IsDir() {
			kind = filesystemmeta.ObjectKindRegularFile
		}
		observation, _ := filesystemmeta.DetectPath(topPath, filesystemmeta.DetectOptions{RootPath: root, IncludeXattrNames: true})
		item = &PendingItem{RelativePath: top, Kind: kind, ModifiedAt: topInfo.ModTime().UTC(), Fidelity: &observation, Warnings: laneFidelityWarnings(observation)}
		items[top] = item
	}
	if info.IsDir() {
		item.DirCount++
	} else if info.Mode().IsRegular() {
		item.FileCount++
		item.Bytes += info.Size()
	}
	if info.ModTime().After(item.ModifiedAt) {
		item.ModifiedAt = info.ModTime().UTC()
	}
}

func transferInventoryHash(plan TransferPlan) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "%s\x00%s\x00%s\x00%s\x00", plan.SchemaVersion, plan.PolicyVersion, plan.Profile, plan.PolicyFingerprint)
	for _, entry := range plan.Entries {
		fmt.Fprintf(hash, "%s\x00%s\x00%d\x00%d\x00%d\x00", entry.RelativePath, entry.Kind, entry.Bytes, entry.Mode, entry.ModifiedAt.UnixNano())
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil))
}

func regularFileSize(info os.FileInfo) int64 {
	if info.Mode().IsRegular() {
		return info.Size()
	}
	return 0
}

func writeTransferPlanFiles(manifestPath, fileListPath string, plan TransferPlan) error {
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	if err := os.WriteFile(manifestPath, payload, 0o644); err != nil {
		return err
	}
	fileList := make([]byte, 0, len(plan.Entries)*32)
	for _, entry := range plan.Entries {
		pathValue := entry.RelativePath
		fileList = append(fileList, []byte(pathValue)...)
		fileList = append(fileList, 0)
	}
	return os.WriteFile(fileListPath, fileList, 0o644)
}
