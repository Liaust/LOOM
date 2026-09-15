package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const boxStateMigrationSchemaVersion = "loom.box_state_migration.v1"

// PlanBoxStateMigration is intentionally read-only. Large safety artifacts are
// marked for adoption, never copied, and the plan has no apply operation.
func PlanBoxStateMigration(sourceRoot, targetRoot string) (BoxStateMigrationPlan, error) {
	sourceRoot, err := normalizeBoxStateMigrationRoot("source", sourceRoot)
	if err != nil {
		return BoxStateMigrationPlan{}, err
	}
	targetRoot, err = normalizeBoxStateMigrationRoot("target", targetRoot)
	if err != nil {
		return BoxStateMigrationPlan{}, err
	}
	plan := BoxStateMigrationPlan{
		SchemaVersion: boxStateMigrationSchemaVersion,
		SourceRoot:    sourceRoot,
		TargetRoot:    targetRoot,
		State:         "not_needed",
		DryRun:        true,
	}
	if plan.SourceRoot == plan.TargetRoot {
		// A pre-cutover profile can expose the same tree under both configuration
		// names. Path identity means there is nothing to migrate; return before
		// inventory or hashing so a large live tree is never scanned twice.
		return plan, nil
	}
	source, sourceExists, err := inventoryBoxState(plan.SourceRoot)
	if err != nil {
		return BoxStateMigrationPlan{}, fmt.Errorf("inventory legacy Box state: %w", err)
	}
	target, targetExists, err := inventoryBoxState(plan.TargetRoot)
	if err != nil {
		return BoxStateMigrationPlan{}, fmt.Errorf("inventory canonical Box state: %w", err)
	}
	if !sourceExists {
		return plan, nil
	}
	plan.State = "ready"
	for _, entry := range source {
		entry.Action = "adopt"
		plan.Entries = append(plan.Entries, entry)
		switch entry.Type {
		case "file":
			plan.FileCount++
			plan.TotalBytes += entry.Size
			if entry.SHA256 != "" {
				plan.HashedFiles++
			}
		case "directory":
			plan.DirectoryCount++
		case "symlink":
			plan.SymlinkCount++
		}
	}
	if !targetExists {
		return plan, nil
	}
	targetByPath := map[string]BoxStateMigrationEntry{}
	for _, entry := range target {
		targetByPath[entry.RelativePath] = entry
	}
	sourceByPath := map[string]BoxStateMigrationEntry{}
	for _, sourceEntry := range source {
		sourceByPath[sourceEntry.RelativePath] = sourceEntry
		targetEntry, ok := targetByPath[sourceEntry.RelativePath]
		if !ok {
			plan.Conflicts = append(plan.Conflicts, BoxStateMigrationConflict{
				RelativePath: sourceEntry.RelativePath,
				SourceType:   sourceEntry.Type,
				TargetType:   "missing",
				Message:      "legacy and canonical runtime-state entries diverge; explicit operator reconciliation is required",
			})
			continue
		}
		if migrationEntriesEqual(sourceEntry, targetEntry) {
			continue
		}
		plan.Conflicts = append(plan.Conflicts, BoxStateMigrationConflict{
			RelativePath: sourceEntry.RelativePath,
			SourceType:   sourceEntry.Type,
			TargetType:   targetEntry.Type,
			Message:      "legacy and canonical runtime-state entries diverge; explicit operator reconciliation is required",
		})
	}
	for _, targetEntry := range target {
		if _, ok := sourceByPath[targetEntry.RelativePath]; ok {
			continue
		}
		plan.Conflicts = append(plan.Conflicts, BoxStateMigrationConflict{
			RelativePath: targetEntry.RelativePath,
			SourceType:   "missing",
			TargetType:   targetEntry.Type,
			Message:      "legacy and canonical runtime-state entries diverge; explicit operator reconciliation is required",
		})
	}
	sort.Slice(plan.Conflicts, func(i, j int) bool { return plan.Conflicts[i].RelativePath < plan.Conflicts[j].RelativePath })
	if len(plan.Conflicts) > 0 {
		plan.State = "conflict"
	} else {
		plan.State = "equivalent"
	}
	return plan, nil
}

func normalizeBoxStateMigrationRoot(label, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !filepath.IsAbs(value) {
		return "", fmt.Errorf("Box state migration %s root must be an absolute path", label)
	}
	value = filepath.Clean(value)
	if value == string(filepath.Separator) {
		return "", fmt.Errorf("Box state migration %s root must be below the filesystem root", label)
	}
	return value, nil
}

func inventoryBoxState(root string) ([]BoxStateMigrationEntry, bool, error) {
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, true, fmt.Errorf("%s is not a directory", root)
	}
	entries := []BoxStateMigrationEntry{}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		item := BoxStateMigrationEntry{RelativePath: filepath.ToSlash(rel)}
		switch {
		case info.IsDir():
			item.Type = "directory"
		case info.Mode().IsRegular():
			item.Type = "file"
			item.Size = info.Size()
			item.SHA256, err = hashMigrationFile(path)
			if err != nil {
				return err
			}
		case info.Mode()&os.ModeSymlink != 0:
			item.Type = "symlink"
			item.LinkTarget, err = os.Readlink(path)
			if err != nil {
				return err
			}
		default:
			item.Type = "other"
		}
		entries = append(entries, item)
		return nil
	})
	if err != nil {
		return nil, true, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].RelativePath < entries[j].RelativePath })
	return entries, true, nil
}

func hashMigrationFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func migrationEntriesEqual(a, b BoxStateMigrationEntry) bool {
	return a.Type == b.Type && a.Size == b.Size && strings.EqualFold(a.SHA256, b.SHA256) && a.LinkTarget == b.LinkTarget
}
