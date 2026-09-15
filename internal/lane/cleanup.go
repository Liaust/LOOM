package lane

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/filesystemmeta"
)

type localCleanupOutcome struct {
	QuarantinePath  string
	QuarantineState string
	Quarantined     []string
	Restored        []string
	Removed         []string
}

type stagedCleanupEntry struct {
	index  int
	entry  TransferEntry
	target string
	staged string
}

func applyLocalCleanupOutcome(record *BatchRecord, result *SendResult, outcome localCleanupOutcome) {
	if record != nil {
		record.LocalCleanupQuarantinePath = outcome.QuarantinePath
		record.LocalCleanupQuarantineState = outcome.QuarantineState
		record.QuarantinedLocalItems = append([]string{}, outcome.Quarantined...)
		record.RestoredLocalItems = append([]string{}, outcome.Restored...)
		record.RemovedLocalItems = append([]string{}, outcome.Removed...)
	}
	if result != nil {
		result.LocalCleanupQuarantinePath = outcome.QuarantinePath
		result.LocalCleanupQuarantineState = outcome.QuarantineState
		result.QuarantinedLocalItems = append([]string{}, outcome.Quarantined...)
		result.RestoredLocalItems = append([]string{}, outcome.Restored...)
		result.RemovedLocalItems = append([]string{}, outcome.Removed...)
	}
}

func cleanupTransferredEntries(rootPath, laneRelPath, statePath, batchID string, plan TransferPlan, beforeEntry func(int, TransferEntry) error, afterStage func(int, TransferEntry, string) error) (localCleanupOutcome, error) {
	laneRel, err := safeCleanupRelativeRoot(laneRelPath, "Lane")
	if err != nil {
		return localCleanupOutcome{}, err
	}
	lanePath := filepath.Join(rootPath, laneRel)
	statePath, err = normalizeLaneStatePath(rootPath, DefaultStateRelPath, statePath)
	if err != nil {
		return localCleanupOutcome{}, err
	}
	commonRoot := commonAncestorPath(lanePath, statePath)
	if commonRoot == "" || commonRoot == string(filepath.Separator) {
		return localCleanupOutcome{}, fmt.Errorf("Lane source and runtime state do not share a bounded filesystem root")
	}
	laneRoot, err := filepath.Rel(commonRoot, lanePath)
	if err != nil {
		return localCleanupOutcome{}, fmt.Errorf("resolve Lane cleanup root: %w", err)
	}
	stateRoot, err := filepath.Rel(commonRoot, statePath)
	if err != nil {
		return localCleanupOutcome{}, fmt.Errorf("resolve Lane state root: %w", err)
	}
	stateFromLane, err := filepath.Rel(laneRoot, stateRoot)
	if err != nil {
		return localCleanupOutcome{}, fmt.Errorf("compare Lane cleanup roots: %w", err)
	}
	if stateFromLane == "." || (stateFromLane != ".." && !strings.HasPrefix(filepath.ToSlash(stateFromLane), "../")) {
		return localCleanupOutcome{}, fmt.Errorf("Lane cleanup quarantine must stay outside the visible Lane source root")
	}
	for _, entry := range plan.Entries {
		if err := validateBundleRelativePath(entry.RelativePath); err != nil {
			return localCleanupOutcome{}, fmt.Errorf("validate Lane cleanup entry %q: %w", entry.RelativePath, err)
		}
	}
	root, err := os.OpenRoot(commonRoot)
	if err != nil {
		return localCleanupOutcome{}, err
	}
	defer root.Close()

	quarantineRel := filepath.Join(stateRoot, "cleanup", safeToken(batchID, "batch"))
	quarantinePath := filepath.Join(commonRoot, quarantineRel)
	if err := root.MkdirAll(filepath.Dir(quarantineRel), 0o700); err != nil {
		return localCleanupOutcome{}, fmt.Errorf("create Lane cleanup quarantine parent: %w", err)
	}
	if err := root.Mkdir(quarantineRel, 0o700); err != nil {
		return localCleanupOutcome{}, fmt.Errorf("create Lane cleanup quarantine for batch %s: %w", batchID, err)
	}
	outcome := localCleanupOutcome{
		QuarantinePath:  quarantinePath,
		QuarantineState: CleanupQuarantineRetainedForRecovery,
	}

	retainUserPolicies := cleanupRetainsUserPolicies(plan)
	var staged []stagedCleanupEntry
	for index, entry := range plan.Entries {
		if entry.Kind == filesystemmeta.ObjectKindDirectory || cleanupRetainsPolicyFile(retainUserPolicies, entry.RelativePath) {
			continue
		}
		if beforeEntry != nil {
			if err := beforeEntry(index, entry); err != nil {
				return rollbackCleanup(root, quarantineRel, outcome, staged, fmt.Errorf("before Lane cleanup entry %s: %w", entry.RelativePath, err))
			}
		}
		target := filepath.Join(laneRoot, filepath.FromSlash(entry.RelativePath))
		stagedPath := filepath.Join(quarantineRel, filepath.FromSlash(entry.RelativePath))
		if err := root.MkdirAll(filepath.Dir(stagedPath), 0o700); err != nil {
			return rollbackCleanup(root, quarantineRel, outcome, staged, fmt.Errorf("prepare Lane cleanup quarantine for %s: %w", entry.RelativePath, err))
		}
		moved, err := stageMatchingCleanupEntry(root, target, stagedPath, entry)
		if moved {
			staged = append(staged, stagedCleanupEntry{index: index, entry: entry, target: target, staged: stagedPath})
		}
		if err != nil {
			return rollbackCleanup(root, quarantineRel, outcome, staged, err)
		}
		if afterStage != nil {
			if err := afterStage(index, entry, filepath.Join(commonRoot, stagedPath)); err != nil {
				return rollbackCleanup(root, quarantineRel, outcome, staged, fmt.Errorf("after Lane cleanup stage %s: %w", entry.RelativePath, err))
			}
		}
	}

	for _, item := range staged {
		outcome.Quarantined = append(outcome.Quarantined, item.entry.RelativePath)
		outcome.Removed = append(outcome.Removed, item.entry.RelativePath)
	}
	var removedDirectories []stagedCleanupEntry
	for index := len(plan.Entries) - 1; index >= 0; index-- {
		entry := plan.Entries[index]
		if entry.Kind != filesystemmeta.ObjectKindDirectory {
			continue
		}
		target := filepath.Join(laneRoot, filepath.FromSlash(entry.RelativePath))
		info, infoErr := root.Lstat(target)
		if errors.Is(infoErr, os.ErrNotExist) {
			continue
		}
		if infoErr != nil {
			return rollbackFinalizedCleanup(root, quarantineRel, outcome, staged, removedDirectories, fmt.Errorf("inspect Lane directory %s during cleanup: %w", entry.RelativePath, infoErr))
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return rollbackFinalizedCleanup(root, quarantineRel, outcome, staged, removedDirectories, fmt.Errorf("Lane source %s changed type during local cleanup", entry.RelativePath))
		}
		if info.Mode().Perm() != os.FileMode(entry.Mode).Perm() {
			return rollbackFinalizedCleanup(root, quarantineRel, outcome, staged, removedDirectories, fmt.Errorf("Lane source %s changed metadata during local cleanup", entry.RelativePath))
		}
		err := root.Remove(target)
		if isDirectoryNotEmpty(err) {
			continue
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return rollbackFinalizedCleanup(root, quarantineRel, outcome, staged, removedDirectories, fmt.Errorf("remove empty Lane directory %s: %w", entry.RelativePath, err))
		}
		if err == nil {
			removedDirectories = append(removedDirectories, stagedCleanupEntry{index: index, entry: entry, target: target})
			outcome.Removed = append(outcome.Removed, entry.RelativePath)
		}
	}
	if len(outcome.Quarantined) == 0 {
		_ = root.RemoveAll(quarantineRel)
		outcome.QuarantinePath = ""
		outcome.QuarantineState = CleanupQuarantineRestored
	} else if err := syncCleanupQuarantine(quarantinePath, staged); err != nil {
		return finishCleanupOutcome(outcome), fmt.Errorf("sync Lane cleanup quarantine: %w", err)
	}
	return finishCleanupOutcome(outcome), nil
}

func commonAncestorPath(left, right string) string {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	for {
		if right == left || strings.HasPrefix(right, left+string(filepath.Separator)) {
			return left
		}
		parent := filepath.Dir(left)
		if parent == left {
			return left
		}
		left = parent
	}
}

func safeCleanupRelativeRoot(value, label string) (string, error) {
	cleaned := filepath.Clean(filepath.FromSlash(strings.TrimSpace(value)))
	if cleaned == "." || filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(filepath.ToSlash(cleaned), "../") {
		return "", fmt.Errorf("%s path must be a contained relative path", label)
	}
	return cleaned, nil
}

func cleanupRetainsUserPolicies(plan TransferPlan) bool {
	for _, decision := range plan.Ignored {
		if decision.RuleCategory == filepolicy.RuleCategoryUser {
			return true
		}
	}
	return false
}

func cleanupRetainsPolicyFile(retainUserPolicies bool, relativePath string) bool {
	return retainUserPolicies && (relativePath == ".loomignore" || strings.HasSuffix(relativePath, "/.loomignore"))
}

func stageMatchingCleanupEntry(root *os.Root, target, staged string, entry TransferEntry) (bool, error) {
	before, err := root.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("Lane source %s disappeared during local cleanup", entry.RelativePath)
	}
	if err != nil {
		return false, err
	}
	if err := validateCleanupEntryInfo(entry, before); err != nil {
		return false, err
	}
	if err := root.Rename(target, staged); err != nil {
		return false, fmt.Errorf("stage Lane source %s in cleanup quarantine: %w", entry.RelativePath, err)
	}
	after, err := root.Lstat(staged)
	if err != nil {
		return true, fmt.Errorf("inspect quarantined Lane source %s: %w", entry.RelativePath, err)
	}
	if !os.SameFile(before, after) {
		return true, fmt.Errorf("Lane source %s was replaced during local cleanup", entry.RelativePath)
	}
	if err := validateCleanupEntryInfo(entry, after); err != nil {
		return true, err
	}
	return true, nil
}

func validateCleanupEntryInfo(entry TransferEntry, info os.FileInfo) error {
	if info == nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("Lane source %s changed type during local cleanup", entry.RelativePath)
	}
	if info.Size() != entry.Bytes || info.Mode().Perm() != os.FileMode(entry.Mode).Perm() || !info.ModTime().UTC().Equal(entry.ModifiedAt.UTC()) {
		return fmt.Errorf("Lane source %s changed after main accepted the transfer", entry.RelativePath)
	}
	return nil
}

func rollbackCleanup(root *os.Root, quarantineRel string, outcome localCleanupOutcome, staged []stagedCleanupEntry, cause error) (localCleanupOutcome, error) {
	var rollbackErrors []string
	for index := len(staged) - 1; index >= 0; index-- {
		item := staged[index]
		if _, err := root.Lstat(item.target); err == nil {
			outcome.Quarantined = append(outcome.Quarantined, item.entry.RelativePath)
			outcome.Removed = append(outcome.Removed, item.entry.RelativePath)
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			outcome.Quarantined = append(outcome.Quarantined, item.entry.RelativePath)
			outcome.Removed = append(outcome.Removed, item.entry.RelativePath)
			rollbackErrors = append(rollbackErrors, fmt.Sprintf("inspect restore target %s: %v", item.entry.RelativePath, err))
			continue
		}
		if err := root.MkdirAll(filepath.Dir(item.target), 0o755); err != nil {
			outcome.Quarantined = append(outcome.Quarantined, item.entry.RelativePath)
			outcome.Removed = append(outcome.Removed, item.entry.RelativePath)
			rollbackErrors = append(rollbackErrors, fmt.Sprintf("prepare restore target %s: %v", item.entry.RelativePath, err))
			continue
		}
		if err := root.Rename(item.staged, item.target); err != nil {
			outcome.Quarantined = append(outcome.Quarantined, item.entry.RelativePath)
			outcome.Removed = append(outcome.Removed, item.entry.RelativePath)
			rollbackErrors = append(rollbackErrors, fmt.Sprintf("restore %s: %v", item.entry.RelativePath, err))
			continue
		}
		outcome.Restored = append(outcome.Restored, item.entry.RelativePath)
	}
	if len(outcome.Quarantined) == 0 {
		if err := root.RemoveAll(quarantineRel); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Sprintf("remove empty cleanup quarantine: %v", err))
		}
		outcome.QuarantinePath = ""
		outcome.QuarantineState = CleanupQuarantineRestored
	} else if err := syncCleanupQuarantine(outcome.QuarantinePath, staged); err != nil {
		rollbackErrors = append(rollbackErrors, fmt.Sprintf("sync retained cleanup quarantine: %v", err))
	}
	outcome = finishCleanupOutcome(outcome)
	if len(rollbackErrors) > 0 {
		return outcome, fmt.Errorf("%w; cleanup rollback: %s", cause, strings.Join(rollbackErrors, "; "))
	}
	return outcome, cause
}

func rollbackFinalizedCleanup(root *os.Root, quarantineRel string, outcome localCleanupOutcome, staged, removedDirectories []stagedCleanupEntry, cause error) (localCleanupOutcome, error) {
	base := localCleanupOutcome{
		QuarantinePath:  outcome.QuarantinePath,
		QuarantineState: outcome.QuarantineState,
	}
	restored, err := rollbackCleanup(root, quarantineRel, base, staged, cause)
	for _, directory := range removedDirectories {
		if _, statErr := root.Lstat(directory.target); errors.Is(statErr, os.ErrNotExist) {
			restored.Removed = append(restored.Removed, directory.entry.RelativePath)
		}
	}
	return finishCleanupOutcome(restored), err
}

func syncCleanupQuarantine(quarantinePath string, staged []stagedCleanupEntry) error {
	if strings.TrimSpace(quarantinePath) == "" {
		return nil
	}
	seen := map[string]bool{}
	for _, item := range staged {
		directory := filepath.Dir(filepath.Join(quarantinePath, filepath.FromSlash(item.entry.RelativePath)))
		if !strings.HasPrefix(filepath.Clean(directory), filepath.Clean(quarantinePath)+string(os.PathSeparator)) {
			continue
		}
		if seen[directory] {
			continue
		}
		seen[directory] = true
		if err := syncDirectory(directory); err != nil {
			return err
		}
	}
	return syncDirectory(quarantinePath)
}

func finishCleanupOutcome(outcome localCleanupOutcome) localCleanupOutcome {
	outcome.Quarantined = sortedUniqueStrings(outcome.Quarantined)
	outcome.Restored = sortedUniqueStrings(outcome.Restored)
	outcome.Removed = sortedUniqueStrings(outcome.Removed)
	return outcome
}

func sortedUniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func isDirectoryNotEmpty(err error) bool {
	return err != nil && (strings.Contains(strings.ToLower(err.Error()), "directory not empty") || strings.Contains(strings.ToLower(err.Error()), "not empty"))
}
