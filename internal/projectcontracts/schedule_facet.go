package projectcontracts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/capabilities"
)

const (
	ScheduleActivationStatusDisabled = "disabled"
	ScheduleActivationStatusPending  = "pending_later_slice"
)

var (
	scheduleKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,80}$`)
	runAsPattern       = regexp.MustCompile(`^[A-Za-z0-9:_-]+$`)
)

func validateScheduleFacet(loaded LoadedProject, contract ProjectContract, add func(Diagnostic)) []ScheduleFacetItem {
	if !contract.Facets["schedules"] {
		return nil
	}
	root := filepath.Join(loaded.RootPath, "schedules")
	entries, err := projectFacetEntries(loaded, "schedules")
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		add(Diagnostic{Severity: SeverityError, Code: "schedule.facet_read_failed", Message: "could not read schedules folder: " + err.Error(), File: root, Field: "facets.schedules"})
		return nil
	}

	items := []ScheduleFacetItem{}
	seen := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || strings.HasPrefix(name, ".") || ignoredScheduleDir(name) {
			continue
		}
		packageLoaded, packageRoot := projectFacetPackage(loaded, "schedules", name)
		item := validateSchedulePackage(packageLoaded, contract, name, packageRoot, add)
		if item.Key == "" {
			continue
		}
		if previous := seen[item.Key]; previous != "" {
			add(Diagnostic{
				Severity:   SeverityError,
				Code:       "schedule.key_duplicate",
				Message:    "schedule key is declared more than once: " + item.Key,
				File:       item.ManifestPath,
				Field:      "schedule.key",
				Suggestion: "use unique project-local schedule keys; previous declaration was in " + previous,
			})
		}
		seen[item.Key] = item.ManifestPath
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Key < items[j].Key
	})
	if len(items) == 0 {
		add(Diagnostic{
			Severity:   SeverityWarning,
			Code:       "schedule.none_found",
			Message:    "schedules facet is enabled but no schedule packages were found",
			File:       root,
			Field:      "facets.schedules",
			Suggestion: "create schedules/<schedule>/loom.schedule.yaml or disable the schedules facet",
		})
	}
	return items
}

func validateSchedulePackage(loaded LoadedProject, contract ProjectContract, folderName, scheduleRoot string, add func(Diagnostic)) ScheduleFacetItem {
	manifestPath := filepath.Join(scheduleRoot, "loom.schedule.yaml")
	folder := filepath.ToSlash(mustRelPath(loaded.RootPath, scheduleRoot))
	item := ScheduleFacetItem{
		Key:              folderName,
		Folder:           folder,
		ManifestPath:     filepath.ToSlash(manifestPath),
		ContractStatus:   ProjectStatusDraft,
		ActivationStatus: ScheduleActivationStatusDisabled,
		Timezone:         "UTC",
	}
	if !pathExists(manifestPath) {
		add(Diagnostic{
			Severity:   SeverityError,
			Code:       "schedule.manifest_missing",
			Message:    "schedule package is missing loom.schedule.yaml",
			File:       manifestPath,
			Field:      "schedules." + folderName,
			Suggestion: "add loom.schedule.yaml or remove the schedule package folder",
		})
		return item
	}

	schedule, payload, err := LoadScheduleContract(manifestPath)
	if err != nil {
		add(Diagnostic{
			Severity: SeverityError,
			Code:     "schedule.manifest_invalid",
			Message:  "schedule manifest is invalid: " + err.Error(),
			File:     manifestPath,
			Field:    "schedules." + folderName,
		})
		return item
	}
	item.ManifestHash = hashBytesURI(payload)
	item.Metadata = schedule.Metadata
	normalized := normalizeScheduleContract(schedule)
	item.Key = normalized.Schedule.Key
	item.BackendScheduleKey = ProjectScheduleKey(contract.Project.Slug, normalized.Schedule.Key)
	item.DisplayName = normalized.Schedule.DisplayName
	item.Description = normalized.Schedule.Description
	item.ContractStatus = normalized.Schedule.Status
	item.ScheduleKind = normalized.Timing.Kind
	item.ScheduleExpr = normalized.Timing.Expression
	item.Timezone = normalized.Timing.Timezone
	item.MisfirePolicy = normalized.Misfire.Policy
	item.LatenessWindowSecs = normalized.Misfire.LatenessWindowSeconds
	item.ConcurrencyPolicy = normalized.Concurrency.Policy
	item.ApprovalPolicy = normalized.Approval.Policy
	item.TimeoutSeconds = normalized.Timeout.Seconds
	item.MaxAttempts = normalized.Retry.MaxAttempts
	item.RunAs = normalized.Target.RunAs
	if item.ContractStatus != "disabled" {
		item.ActivationStatus = ScheduleActivationStatusPending
	}

	validateScheduleContractBasics(normalized, manifestPath, add)
	validateScheduleTarget(&item, scheduleRoot, manifestPath, normalized, add)
	validateScheduleProfiles(&item, manifestPath, normalized, add)
	return item
}

func LoadScheduleContract(path string) (ScheduleContract, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ScheduleContract{}, nil, err
	}
	var contract ScheduleContract
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&contract); err != nil {
		return ScheduleContract{}, raw, err
	}
	return contract, raw, nil
}

func normalizeScheduleContract(contract ScheduleContract) ScheduleContract {
	contract.Kind = strings.TrimSpace(contract.Kind)
	contract.SchemaVersion = strings.TrimSpace(contract.SchemaVersion)
	contract.Schedule.Key = strings.ToLower(strings.TrimSpace(contract.Schedule.Key))
	contract.Schedule.DisplayName = strings.TrimSpace(contract.Schedule.DisplayName)
	if contract.Schedule.DisplayName == "" {
		contract.Schedule.DisplayName = contract.Schedule.Key
	}
	contract.Schedule.Description = strings.TrimSpace(contract.Schedule.Description)
	contract.Schedule.Status = strings.ToLower(strings.TrimSpace(contract.Schedule.Status))
	if contract.Schedule.Status == "" {
		contract.Schedule.Status = ProjectStatusDraft
	}
	contract.Target.Capability = strings.TrimSpace(contract.Target.Capability)
	contract.Target.InputFile = strings.TrimSpace(contract.Target.InputFile)
	if contract.Target.InputFile == "" {
		contract.Target.InputFile = "input.example.json"
	}
	contract.Target.RunAs = strings.TrimSpace(contract.Target.RunAs)
	if contract.Target.RunAs == "" {
		contract.Target.RunAs = "owner"
	}
	contract.Timing.Kind = strings.TrimSpace(contract.Timing.Kind)
	contract.Timing.Expression = strings.TrimSpace(contract.Timing.Expression)
	contract.Timing.Timezone = strings.TrimSpace(contract.Timing.Timezone)
	if contract.Timing.Timezone == "" && contract.Timing.Kind != automation.ScheduleKindCron {
		contract.Timing.Timezone = "UTC"
	}
	contract.Misfire.Policy = strings.TrimSpace(contract.Misfire.Policy)
	if contract.Misfire.Policy == "" {
		contract.Misfire.Policy = automation.MisfireMarkMissed
	}
	contract.Concurrency.Policy = strings.TrimSpace(contract.Concurrency.Policy)
	if contract.Concurrency.Policy == "" {
		contract.Concurrency.Policy = automation.ConcurrencyAllowParallel
	}
	contract.Approval.Policy = strings.TrimSpace(contract.Approval.Policy)
	if contract.Approval.Policy == "" {
		contract.Approval.Policy = automation.ApprovalNone
	}
	return contract
}

func validateScheduleContractBasics(contract ScheduleContract, manifestPath string, add func(Diagnostic)) {
	if contract.Kind != ScheduleContractKind {
		add(Diagnostic{Severity: SeverityError, Code: "schedule.kind_invalid", Message: "schedule contract kind must be " + ScheduleContractKind, File: manifestPath, Field: "kind", Suggestion: "set kind: loom.schedule"})
	}
	if contract.SchemaVersion != ScheduleSchemaV03 {
		add(Diagnostic{Severity: SeverityError, Code: "schedule.schema_version_invalid", Message: "schedule contract schema_version must be " + ScheduleSchemaV03, File: manifestPath, Field: "schema_version", Suggestion: "set schema_version: schedule.contract.v0.3"})
	}
	if contract.Schedule.Key == "" {
		add(Diagnostic{Severity: SeverityError, Code: "schedule.key_required", Message: "schedule.key is required", File: manifestPath, Field: "schedule.key"})
	} else if !scheduleKeyPattern.MatchString(contract.Schedule.Key) {
		add(Diagnostic{Severity: SeverityError, Code: "schedule.key_invalid", Message: "schedule.key must match ^[a-z][a-z0-9_-]{0,80}$", File: manifestPath, Field: "schedule.key", Suggestion: "use lowercase letters, numbers, underscore, or hyphen"})
	}
	if !validScheduleContractStatus(contract.Schedule.Status) {
		add(Diagnostic{Severity: SeverityError, Code: "schedule.status_invalid", Message: "schedule.status is not supported", File: manifestPath, Field: "schedule.status", Suggestion: "use draft, active, paused, or disabled"})
	}
	if contract.Target.RunAs != "" && !runAsPattern.MatchString(contract.Target.RunAs) {
		add(Diagnostic{Severity: SeverityError, Code: "schedule.run_as_invalid", Message: "target.run_as must be an actor key, actor id, owner, or scheduler", File: manifestPath, Field: "target.run_as"})
	}
}

func validateScheduleTarget(item *ScheduleFacetItem, scheduleRoot, manifestPath string, contract ScheduleContract, add func(Diagnostic)) {
	if contract.Target.Capability == "" {
		add(Diagnostic{Severity: SeverityError, Code: "schedule.target_required", Message: "target.capability is required", File: manifestPath, Field: "target.capability"})
	} else if normalized, err := capabilities.NormalizeAddress(contract.Target.Capability); err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "schedule.target_invalid", Message: "target capability address is invalid: " + err.Error(), File: manifestPath, Field: "target.capability"})
	} else {
		item.TargetCapability = normalized
	}

	inputRel, err := normalizeRelativePath(contract.Target.InputFile)
	if err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "schedule.input_path_unsafe", Message: "target.input_file is unsafe: " + contract.Target.InputFile, File: manifestPath, Field: "target.input_file", Suggestion: "use a safe path relative to the schedule folder"})
		return
	}
	inputPath := filepath.Join(scheduleRoot, filepath.FromSlash(inputRel))
	item.InputPath = filepath.ToSlash(inputPath)
	raw, err := os.ReadFile(inputPath)
	if err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "schedule.input_missing", Message: "schedule input file could not be read: " + err.Error(), File: inputPath, Field: "target.input_file"})
		return
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "schedule.input_invalid_json", Message: "schedule input file must be a JSON object: " + err.Error(), File: inputPath, Field: "target.input_file"})
		return
	}
	normalized, err := json.Marshal(object)
	if err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "schedule.input_invalid_json", Message: "schedule input file could not be normalized: " + err.Error(), File: inputPath, Field: "target.input_file"})
		return
	}
	item.InputJSON = json.RawMessage(normalized)
	item.InputHash = hashBytesURI(normalized)
}

func validateScheduleProfiles(item *ScheduleFacetItem, manifestPath string, contract ScheduleContract, add func(Diagnostic)) {
	if _, err := automation.ParseScheduleExpressionInTimezone(contract.Timing.Kind, contract.Timing.Expression, contract.Timing.Timezone, time.Now().UTC()); err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "schedule.timing_invalid", Message: "schedule timing is invalid: " + err.Error(), File: manifestPath, Field: "timing.expression"})
	}
	if _, err := time.LoadLocation(contract.Timing.Timezone); err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "schedule.timezone_invalid", Message: "schedule timezone is invalid: " + err.Error(), File: manifestPath, Field: "timing.timezone"})
	}

	misfire, err := automation.NormalizeMisfireProfile(scheduleProfileJSON(automation.MisfireProfile{
		Policy:                contract.Misfire.Policy,
		LatenessWindowSeconds: contract.Misfire.LatenessWindowSeconds,
	}))
	if err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "schedule.misfire_invalid", Message: "schedule misfire profile is invalid: " + err.Error(), File: manifestPath, Field: "misfire"})
	} else {
		item.MisfirePolicy = misfire.Policy
		item.LatenessWindowSecs = misfire.LatenessWindowSeconds
	}
	concurrency, err := automation.NormalizeConcurrencyProfile(scheduleProfileJSON(automation.ConcurrencyProfile{Policy: contract.Concurrency.Policy}))
	if err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "schedule.concurrency_invalid", Message: "schedule concurrency profile is invalid: " + err.Error(), File: manifestPath, Field: "concurrency"})
	} else {
		item.ConcurrencyPolicy = concurrency.Policy
	}
	approval, err := automation.NormalizeApprovalProfile(scheduleProfileJSON(automation.ApprovalProfile{Mode: contract.Approval.Policy}))
	if err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "schedule.approval_invalid", Message: "schedule approval profile is invalid: " + err.Error(), File: manifestPath, Field: "approval"})
	} else {
		item.ApprovalPolicy = approval.Mode
	}
	timeoutProfile, err := automation.NormalizeTimeoutProfile(scheduleProfileJSON(automation.TimeoutProfile{TimeoutSeconds: contract.Timeout.Seconds}))
	if err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "schedule.timeout_invalid", Message: "schedule timeout profile is invalid: " + err.Error(), File: manifestPath, Field: "timeout.seconds"})
	} else {
		item.TimeoutSeconds = timeoutProfile.TimeoutSeconds
	}
	retryProfile, err := automation.NormalizeRetryProfile(scheduleProfileJSON(automation.RetryProfile{MaxAttempts: contract.Retry.MaxAttempts}))
	if err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "schedule.retry_invalid", Message: "schedule retry profile is invalid: " + err.Error(), File: manifestPath, Field: "retry.max_attempts"})
	} else {
		item.MaxAttempts = retryProfile.MaxAttempts
	}
}

func validScheduleContractStatus(status string) bool {
	switch status {
	case ProjectStatusDraft, "active", "paused", "disabled":
		return true
	default:
		return false
	}
}

func ignoredScheduleDir(name string) bool {
	switch name {
	case "tmp", "result", ".cache", ".git":
		return true
	default:
		return false
	}
}

func scheduleSummary(schedules []ScheduleFacetItem) (discovered int) {
	for _, schedule := range schedules {
		if schedule.ManifestPath != "" {
			discovered++
		}
	}
	return discovered
}

func scheduleTargetSummaries(schedules []ScheduleFacetItem) []string {
	out := []string{}
	for _, schedule := range schedules {
		if schedule.ManifestPath == "" {
			continue
		}
		out = append(out, fmt.Sprintf("%s -> %s %s %s %s", schedule.Key, schedule.TargetCapability, schedule.ScheduleKind, schedule.ScheduleExpr, schedule.ActivationStatus))
	}
	sort.Strings(out)
	return out
}

func ProjectScheduleKey(projectSlug, scheduleKey string) string {
	projectSlug = strings.TrimSpace(projectSlug)
	scheduleKey = strings.TrimSpace(scheduleKey)
	base := strings.ReplaceAll(projectSlug, "-", "_") + "__" + scheduleKey
	if base == "__" {
		base = "project_schedule"
	}
	base = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, strings.ToLower(base))
	if base == "" || base[0] < 'a' || base[0] > 'z' {
		base = "p_" + base
	}
	const maxScheduleKeyLength = 81
	if len(base) <= maxScheduleKeyLength {
		return base
	}
	sum := sha256.Sum256([]byte(projectSlug + "\x00" + scheduleKey))
	suffix := "_" + hex.EncodeToString(sum[:])[:12]
	return strings.TrimRight(base[:maxScheduleKeyLength-len(suffix)], "_-") + suffix
}

func scheduleProfileJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(raw)
}
