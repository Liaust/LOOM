package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const ReleaseRetentionSchemaVersion = "loom.update.release_retention.v1"

type ReleaseRetentionInput struct {
	ReleasesDir        string
	ActivePath         string
	StateDir           string
	KeepSuccessful     int
	ProtectYoungerThan time.Duration
	Pins               []string
	Now                func() time.Time
}

type ReleaseRetentionSpec struct {
	ReleasesDir          string   `json:"releases_dir"`
	ActivePath           string   `json:"active_path"`
	StateDir             string   `json:"state_dir"`
	KeepSuccessful       int      `json:"keep_successful"`
	ProtectYoungerSecond int64    `json:"protect_younger_seconds"`
	Pins                 []string `json:"pins,omitempty"`
}

type ReleaseRetentionEntry struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	ModifiedAt time.Time `json:"modified_at"`
	Bytes      int64     `json:"bytes"`
	Reasons    []string  `json:"reasons,omitempty"`
}

type ReleaseRetentionPlan struct {
	SchemaVersion string                  `json:"schema_version"`
	Status        string                  `json:"status"`
	CreatedAt     time.Time               `json:"created_at"`
	PlanHash      string                  `json:"plan_hash"`
	Spec          ReleaseRetentionSpec    `json:"spec"`
	Protected     []ReleaseRetentionEntry `json:"protected"`
	Delete        []ReleaseRetentionEntry `json:"delete"`
	ProtectedByte int64                   `json:"protected_bytes"`
	DeleteBytes   int64                   `json:"delete_bytes"`
	Diagnostics   []UpdateDiagnostic      `json:"diagnostics,omitempty"`
}

type ReleaseRetentionApplyInput struct {
	PlanInput        ReleaseRetentionInput
	ExpectedPlanHash string
	Yes              bool
}

type ReleaseRetentionApplyResult struct {
	Status     string                  `json:"status"`
	Plan       ReleaseRetentionPlan    `json:"plan"`
	Deleted    []ReleaseRetentionEntry `json:"deleted"`
	FreedBytes int64                   `json:"freed_bytes"`
	Refused    bool                    `json:"refused"`
	Refusal    string                  `json:"refusal,omitempty"`
}

func PlanReleaseRetention(input ReleaseRetentionInput) (ReleaseRetentionPlan, error) {
	now := currentTime(input.Now)
	spec, err := normalizeReleaseRetentionSpec(input)
	if err != nil {
		return ReleaseRetentionPlan{}, err
	}
	plan := ReleaseRetentionPlan{
		SchemaVersion: ReleaseRetentionSchemaVersion,
		Status:        PlanStatusReady,
		CreatedAt:     now,
		Spec:          spec,
		Protected:     []ReleaseRetentionEntry{},
		Delete:        []ReleaseRetentionEntry{},
		Diagnostics:   []UpdateDiagnostic{},
	}

	entries, err := readReleaseRetentionEntries(spec.ReleasesDir)
	if err != nil {
		return ReleaseRetentionPlan{}, err
	}
	entryByPath := make(map[string]ReleaseRetentionEntry, len(entries))
	for _, entry := range entries {
		entryByPath[entry.Path] = entry
	}
	protected := map[string]map[string]struct{}{}
	block := func(code, message, path string) {
		plan.Status = PlanStatusBlocked
		plan.Diagnostics = append(plan.Diagnostics, UpdateDiagnostic{
			Severity: DiagnosticBlocking,
			Code:     code,
			Message:  message,
			Path:     path,
		})
	}
	protect := func(path, reason string, required bool) bool {
		resolved, pathErr := directReleaseChild(spec.ReleasesDir, path)
		if pathErr != nil {
			if required {
				block("update.release_retention_protected_path_invalid", pathErr.Error(), path)
			}
			return false
		}
		if _, ok := entryByPath[resolved]; !ok {
			if required {
				block("update.release_retention_protected_release_missing", "Required protected release is missing.", resolved)
			}
			return false
		}
		if protected[resolved] == nil {
			protected[resolved] = map[string]struct{}{}
		}
		protected[resolved][reason] = struct{}{}
		return true
	}

	activeTarget, err := resolveActiveRelease(spec.ActivePath)
	if err != nil {
		block("update.release_retention_active_invalid", err.Error(), spec.ActivePath)
	} else {
		protect(activeTarget, "active_symlink", true)
	}

	activeManifestPath := ActiveManifestPath(spec.StateDir)
	activeManifest, err := ReadUpdateManifest(activeManifestPath)
	if err != nil {
		block("update.release_retention_active_manifest_invalid", err.Error(), activeManifestPath)
	} else {
		protect(activeManifest.Active.Path, "immediate_rollback", true)
		protect(activeManifest.Target.Path, "active_update_target", true)
	}

	history, historyDiagnostics := ListHistory(spec.StateDir, math.MaxInt)
	for _, diagnostic := range historyDiagnostics {
		block("update.release_retention_history_invalid", diagnostic.Message, diagnostic.Path)
	}
	successfulSeen := map[string]struct{}{}
	successfulKept := 0
	for _, manifest := range history {
		if manifest.Status != UpdateStatusSucceeded || successfulKept >= spec.KeepSuccessful {
			continue
		}
		path, pathErr := directReleaseChild(spec.ReleasesDir, manifest.Target.Path)
		if pathErr != nil {
			block("update.release_retention_history_target_invalid", pathErr.Error(), manifest.Target.Path)
			continue
		}
		if _, seen := successfulSeen[path]; seen {
			continue
		}
		successfulSeen[path] = struct{}{}
		if protect(path, "recent_successful_update", false) {
			successfulKept++
		}
	}
	if successfulKept < spec.KeepSuccessful && len(entries) > successfulKept {
		plan.Diagnostics = append(plan.Diagnostics, UpdateDiagnostic{
			Severity: DiagnosticWarning,
			Code:     "update.release_retention_successful_history_short",
			Message:  fmt.Sprintf("Only %d existing successful release targets were available to retain; requested %d.", successfulKept, spec.KeepSuccessful),
			Path:     HistoryDir(spec.StateDir),
		})
	}

	for _, pin := range spec.Pins {
		protect(pin, "operator_pin", true)
	}
	cutoff := now.Add(-time.Duration(spec.ProtectYoungerSecond) * time.Second)
	for _, entry := range entries {
		if entry.ModifiedAt.After(cutoff) {
			protect(entry.Path, "young_release", true)
		}
	}

	for _, entry := range entries {
		if reasons, ok := protected[entry.Path]; ok {
			for reason := range reasons {
				entry.Reasons = append(entry.Reasons, reason)
			}
			sort.Strings(entry.Reasons)
			plan.Protected = append(plan.Protected, entry)
			plan.ProtectedByte += entry.Bytes
			continue
		}
		plan.Delete = append(plan.Delete, entry)
		plan.DeleteBytes += entry.Bytes
	}
	sortReleaseRetentionEntries(plan.Protected)
	sortReleaseRetentionEntries(plan.Delete)
	plan.PlanHash = releaseRetentionPlanHash(plan)
	return plan, nil
}

func ApplyReleaseRetention(input ReleaseRetentionApplyInput) (ReleaseRetentionApplyResult, error) {
	plan, err := PlanReleaseRetention(input.PlanInput)
	if err != nil {
		return ReleaseRetentionApplyResult{}, err
	}
	result := ReleaseRetentionApplyResult{
		Status:  UpdateStatusDryRun,
		Plan:    plan,
		Deleted: []ReleaseRetentionEntry{},
	}
	if plan.Status != PlanStatusReady {
		result.Refused = true
		result.Refusal = "release retention plan is blocked"
		return result, nil
	}
	if !input.Yes {
		result.Refused = true
		result.Refusal = "release retention apply requires --yes"
		return result, nil
	}
	expected := strings.TrimSpace(input.ExpectedPlanHash)
	if expected == "" {
		result.Refused = true
		result.Refusal = "release retention apply requires --plan-hash from a reviewed plan"
		return result, nil
	}
	if expected != plan.PlanHash {
		result.Refused = true
		result.Refusal = fmt.Sprintf("release retention plan changed: expected %s, current %s", expected, plan.PlanHash)
		return result, nil
	}
	for _, entry := range plan.Delete {
		if _, err := directReleaseChild(plan.Spec.ReleasesDir, entry.Path); err != nil {
			return result, err
		}
		info, err := os.Lstat(entry.Path)
		if err != nil {
			return result, fmt.Errorf("recheck release %s: %w", entry.Path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return result, fmt.Errorf("release candidate changed type before apply: %s", entry.Path)
		}
	}
	for _, entry := range plan.Delete {
		if err := os.RemoveAll(entry.Path); err != nil {
			return result, fmt.Errorf("delete release %s: %w", entry.Path, err)
		}
		result.Deleted = append(result.Deleted, entry)
		result.FreedBytes += entry.Bytes
	}
	result.Status = UpdateStatusSucceeded
	return result, nil
}

func normalizeReleaseRetentionSpec(input ReleaseRetentionInput) (ReleaseRetentionSpec, error) {
	releasesDir, err := normalizeExistingDir(input.ReleasesDir, "releases directory")
	if err != nil {
		return ReleaseRetentionSpec{}, err
	}
	rootInfo, err := os.Lstat(releasesDir)
	if err != nil {
		return ReleaseRetentionSpec{}, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return ReleaseRetentionSpec{}, fmt.Errorf("releases directory must not be a symlink: %s", releasesDir)
	}
	releasesDir, err = filepath.EvalSymlinks(releasesDir)
	if err != nil {
		return ReleaseRetentionSpec{}, fmt.Errorf("resolve releases directory: %w", err)
	}
	releasesDir = filepath.Clean(releasesDir)
	activePath, err := normalizePath(input.ActivePath, "active release path")
	if err != nil {
		return ReleaseRetentionSpec{}, err
	}
	stateDir, err := normalizePath(input.StateDir, "update state directory")
	if err != nil {
		return ReleaseRetentionSpec{}, err
	}
	keepSuccessful := input.KeepSuccessful
	if keepSuccessful <= 0 {
		keepSuccessful = 10
	}
	protectYounger := input.ProtectYoungerThan
	if protectYounger <= 0 {
		protectYounger = 48 * time.Hour
	}
	pins := make([]string, 0, len(input.Pins))
	seen := map[string]struct{}{}
	for _, value := range input.Pins {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if !filepath.IsAbs(value) {
			value = filepath.Join(releasesDir, value)
		}
		value = filepath.Clean(value)
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		pins = append(pins, value)
	}
	sort.Strings(pins)
	return ReleaseRetentionSpec{
		ReleasesDir:          releasesDir,
		ActivePath:           activePath,
		StateDir:             stateDir,
		KeepSuccessful:       keepSuccessful,
		ProtectYoungerSecond: int64(protectYounger / time.Second),
		Pins:                 pins,
	}, nil
}

func readReleaseRetentionEntries(root string) ([]ReleaseRetentionEntry, error) {
	dirEntries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	entries := make([]ReleaseRetentionEntry, 0, len(dirEntries))
	for _, dirEntry := range dirEntries {
		path := filepath.Join(root, dirEntry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, fmt.Errorf("release root contains a non-directory entry: %s", path)
		}
		bytes, err := releaseDirectoryBytes(path)
		if err != nil {
			return nil, fmt.Errorf("measure release %s: %w", path, err)
		}
		entries = append(entries, ReleaseRetentionEntry{
			Name:       dirEntry.Name(),
			Path:       path,
			ModifiedAt: info.ModTime().UTC(),
			Bytes:      bytes,
		})
	}
	return entries, nil
}

func releaseDirectoryBytes(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func resolveActiveRelease(activePath string) (string, error) {
	info, err := os.Lstat(activePath)
	if err != nil {
		return "", fmt.Errorf("active release path is unreadable: %w", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return "", fmt.Errorf("active release path must be a symlink: %s", activePath)
	}
	target, err := filepath.EvalSymlinks(activePath)
	if err != nil {
		return "", fmt.Errorf("resolve active release path: %w", err)
	}
	return filepath.Clean(target), nil
}

func directReleaseChild(root, path string) (string, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || path == "" {
		return "", fmt.Errorf("release path is required")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path = filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		path = filepath.Clean(resolved)
	} else if os.IsNotExist(err) {
		resolvedParent, parentErr := filepath.EvalSymlinks(filepath.Dir(path))
		if parentErr != nil {
			return "", fmt.Errorf("resolve release path parent: %w", parentErr)
		}
		path = filepath.Join(filepath.Clean(resolvedParent), filepath.Base(path))
	} else {
		return "", fmt.Errorf("resolve release path: %w", err)
	}
	if filepath.Dir(path) != root {
		return "", fmt.Errorf("release path must be a direct child of %s: %s", root, path)
	}
	return path, nil
}

func sortReleaseRetentionEntries(entries []ReleaseRetentionEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].ModifiedAt.Equal(entries[j].ModifiedAt) {
			return entries[i].Name < entries[j].Name
		}
		return entries[i].ModifiedAt.Before(entries[j].ModifiedAt)
	})
}

func releaseRetentionPlanHash(plan ReleaseRetentionPlan) string {
	payload := struct {
		SchemaVersion string                  `json:"schema_version"`
		Status        string                  `json:"status"`
		Spec          ReleaseRetentionSpec    `json:"spec"`
		Protected     []ReleaseRetentionEntry `json:"protected"`
		Delete        []ReleaseRetentionEntry `json:"delete"`
		Diagnostics   []UpdateDiagnostic      `json:"diagnostics,omitempty"`
	}{
		SchemaVersion: plan.SchemaVersion,
		Status:        plan.Status,
		Spec:          plan.Spec,
		Protected:     plan.Protected,
		Delete:        plan.Delete,
		Diagnostics:   plan.Diagnostics,
	}
	raw, _ := json.Marshal(payload)
	hash := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(hash[:])
}

func IsReleaseRetentionBlocked(plan ReleaseRetentionPlan) bool {
	if plan.Status == PlanStatusBlocked {
		return true
	}
	for _, diagnostic := range plan.Diagnostics {
		if diagnostic.Severity == DiagnosticBlocking || diagnostic.Severity == DiagnosticError {
			return true
		}
	}
	return false
}
