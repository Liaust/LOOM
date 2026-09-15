package storagemigration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"loom.local/loom/internal/storagecatalog"
)

type PlanInput struct {
	Context           context.Context
	NodeID            string
	LayoutFingerprint string
	Roots             []RootMapping
	Details           []storagecatalog.EntryDetail
	Now               time.Time
}

func Plan(input PlanInput) (Manifest, error) {
	ctx := input.Context
	if ctx == nil {
		ctx = context.Background()
	}
	manifest := Manifest{SchemaVersion: ManifestSchemaVersion, GeneratedAt: input.Now.UTC(), NodeID: strings.TrimSpace(input.NodeID), LayoutFingerprint: strings.TrimSpace(input.LayoutFingerprint)}
	if manifest.GeneratedAt.IsZero() {
		manifest.GeneratedAt = time.Now().UTC()
	}
	if manifest.NodeID == "" || manifest.LayoutFingerprint == "" {
		return Manifest{}, fmt.Errorf("node id and layout fingerprint are required")
	}
	validatedRoots, err := ValidateRootMappings(input.Roots)
	if err != nil {
		return Manifest{}, err
	}
	manifest.Roots = validatedRoots
	for index := range manifest.Roots {
		root := &manifest.Roots[index]
		var err error
		root.OldFilesystemID, err = filesystemID(root.OldRoot)
		if err != nil && !os.IsNotExist(err) {
			return Manifest{}, fmt.Errorf("inspect old root %s: %w", root.Name, err)
		}
		root.NewFilesystemID, err = filesystemID(root.NewRoot)
		if err != nil && !os.IsNotExist(err) {
			return Manifest{}, fmt.Errorf("inspect new root %s: %w", root.Name, err)
		}
		if root.OldFilesystemID == "" {
			manifest.Conflicts = append(manifest.Conflicts, Conflict{Code: "old_root_missing", Path: root.OldRoot, Detail: "old root is unavailable for inventory"})
		}
		if root.NewFilesystemID == "" {
			manifest.Conflicts = append(manifest.Conflicts, Conflict{Code: "new_root_missing", Path: root.NewRoot, Detail: "destination root must exist before review"})
		}
		if root.OldFilesystemID != "" && root.NewFilesystemID != "" && root.OldFilesystemID != root.NewFilesystemID {
			manifest.Conflicts = append(manifest.Conflicts, Conflict{Code: "cross_filesystem_move_unplanned", Path: root.NewRoot, Detail: "old and new roots are on different filesystems; this manifest has no space/copy/rollback proof and cannot be applied"})
		}
	}
	details := append([]storagecatalog.EntryDetail(nil), input.Details...)
	sort.Slice(details, func(i, j int) bool {
		return details[i].Entry.StorageEntryID < details[j].Entry.StorageEntryID
	})
	manifest.RootInventory = inventoryMigrationRoots(ctx, &manifest, details)
	for _, detail := range details {
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}
		planEntry(ctx, &manifest, detail)
	}
	sort.Slice(manifest.Actions, func(i, j int) bool { return manifest.Actions[i].ActionID < manifest.Actions[j].ActionID })
	sort.Slice(manifest.Conflicts, func(i, j int) bool {
		if manifest.Conflicts[i].Code != manifest.Conflicts[j].Code {
			return manifest.Conflicts[i].Code < manifest.Conflicts[j].Code
		}
		return manifest.Conflicts[i].Path < manifest.Conflicts[j].Path
	})
	if err := RefreshManifestHash(&manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

type inventoryCatalogRef struct {
	entryID string
	refID   string
}

func inventoryMigrationRoots(ctx context.Context, manifest *Manifest, details []storagecatalog.EntryDetail) []RootInventoryEvidence {
	result := make([]RootInventoryEvidence, 0, len(manifest.Roots))
	for _, root := range manifest.Roots {
		evidence := RootInventoryEvidence{RootName: root.Name, OldRoot: root.OldRoot}
		refsByPath := map[string][]inventoryCatalogRef{}
		for _, detail := range details {
			for _, ref := range detail.PhysicalRefs {
				if !storagecatalog.PhysicalRefAvailable(ref) {
					continue
				}
				pathValue, ok := storagecatalog.PhysicalRefLocalPath(ref)
				if !ok {
					continue
				}
				if _, within := relativeWithin(pathValue, root.OldRoot); !within {
					continue
				}
				evidence.CatalogRefCount++
				refsByPath[pathValue] = append(refsByPath[pathValue], inventoryCatalogRef{entryID: detail.Entry.StorageEntryID, refID: ref.StoragePhysicalRefID})
			}
		}
		catalogPaths := make([]string, 0, len(refsByPath))
		for pathValue := range refsByPath {
			catalogPaths = append(catalogPaths, pathValue)
		}
		sort.Strings(catalogPaths)
		evidence.CatalogPathCount = len(catalogPaths)
		catalogPathValid := make(map[string]bool, len(catalogPaths))
		catalogPathObserved := make(map[string]bool, len(catalogPaths))
		for _, pathValue := range catalogPaths {
			refs := refsByPath[pathValue]
			sort.Slice(refs, func(i, j int) bool {
				if refs[i].entryID != refs[j].entryID {
					return refs[i].entryID < refs[j].entryID
				}
				return refs[i].refID < refs[j].refID
			})
			if len(refs) > 1 {
				evidence.DuplicateCatalogCount += len(refs) - 1
				for index := 1; index < len(refs); index++ {
					manifest.Conflicts = append(manifest.Conflicts, Conflict{
						Code:    "duplicate_catalog_path",
						Path:    pathValue,
						EntryID: refs[index].entryID,
						RefID:   refs[index].refID,
						Detail:  fmt.Sprintf("available catalog ref duplicates %s for entry %s", refs[0].refID, refs[0].entryID),
					})
				}
			}
			if err := validateManagedPath(root.OldRoot, pathValue, true); err != nil {
				evidence.MissingCatalogCount++
				manifest.Conflicts = append(manifest.Conflicts, Conflict{Code: "catalog_path_unusable", Path: pathValue, EntryID: refs[0].entryID, RefID: refs[0].refID, Detail: err.Error()})
				continue
			}
			info, err := os.Lstat(pathValue)
			if os.IsNotExist(err) {
				evidence.MissingCatalogCount++
				manifest.Conflicts = append(manifest.Conflicts, Conflict{Code: "catalog_path_missing", Path: pathValue, EntryID: refs[0].entryID, RefID: refs[0].refID, Detail: "available catalog ref has no physical object under the mapped old root"})
				continue
			} else if err != nil {
				evidence.MissingCatalogCount++
				manifest.Conflicts = append(manifest.Conflicts, Conflict{Code: "catalog_path_unreadable", Path: pathValue, EntryID: refs[0].entryID, RefID: refs[0].refID, Detail: err.Error()})
				continue
			} else if !info.Mode().IsRegular() {
				evidence.MissingCatalogCount++
				manifest.Conflicts = append(manifest.Conflicts, Conflict{Code: "catalog_path_not_regular", Path: pathValue, EntryID: refs[0].entryID, RefID: refs[0].refID, Detail: fmt.Sprintf("available catalog ref resolves to %s instead of a regular file", info.Mode().Type())})
				continue
			}
			catalogPathValid[pathValue] = true
		}
		if root.OldFilesystemID == "" {
			evidence.InventoryErrorCount++
			result = append(result, evidence)
			continue
		}
		structureHash := sha256.New()
		walkErr := filepath.WalkDir(root.OldRoot, func(pathValue string, entry os.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				evidence.InventoryErrorCount++
				manifest.Conflicts = append(manifest.Conflicts, Conflict{Code: "physical_inventory_unavailable", Path: pathValue, Detail: walkErr.Error()})
				if entry != nil && entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if pathValue == root.OldRoot {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				evidence.InventoryErrorCount++
				manifest.Conflicts = append(manifest.Conflicts, Conflict{Code: "physical_inventory_unavailable", Path: pathValue, Detail: err.Error()})
				return nil
			}
			internalMarker := info.Mode().IsRegular() && allowedMigrationInternalMarker(root, pathValue)
			if err := appendRootStructureEvidence(ctx, structureHash, root.OldRoot, pathValue, info, internalMarker); err != nil {
				evidence.InventoryErrorCount++
				manifest.Conflicts = append(manifest.Conflicts, Conflict{Code: "physical_inventory_unavailable", Path: pathValue, Detail: err.Error()})
				return nil
			}
			switch {
			case info.IsDir():
				evidence.DirectoryCount++
			case info.Mode().IsRegular():
				if internalMarker {
					evidence.InternalMarkerCount++
					return nil
				}
				evidence.RegularFileCount++
				if len(refsByPath[pathValue]) == 0 {
					evidence.UntrackedFileCount++
					manifest.Conflicts = append(manifest.Conflicts, Conflict{Code: "untracked_regular_file", Path: pathValue, Detail: "physical custody file has no available catalog ref; inspect and explicitly register or quarantine before cutover"})
				} else if catalogPathValid[pathValue] {
					evidence.MatchedFileCount++
					catalogPathObserved[pathValue] = true
				}
			case info.Mode()&os.ModeSymlink != 0:
				evidence.SymlinkCount++
				manifest.Conflicts = append(manifest.Conflicts, Conflict{Code: "unexpected_symlink", Path: pathValue, Detail: "mapped physical custody contains a symlink; reconcile it explicitly before cutover"})
			default:
				evidence.SpecialFileCount++
				manifest.Conflicts = append(manifest.Conflicts, Conflict{Code: "unexpected_special_file", Path: pathValue, Detail: fmt.Sprintf("mapped physical custody contains unsupported object type %s", info.Mode().Type())})
			}
			return nil
		})
		if walkErr != nil && ctx.Err() != nil {
			evidence.InventoryErrorCount++
			manifest.Conflicts = append(manifest.Conflicts, Conflict{Code: "physical_inventory_cancelled", Path: root.OldRoot, Detail: ctx.Err().Error()})
		}
		evidence.StructureDigest = "sha256:" + hex.EncodeToString(structureHash.Sum(nil))
		for _, pathValue := range catalogPaths {
			if !catalogPathValid[pathValue] || catalogPathObserved[pathValue] {
				continue
			}
			refs := refsByPath[pathValue]
			evidence.MissingCatalogCount++
			manifest.Conflicts = append(manifest.Conflicts, Conflict{Code: "catalog_path_not_observed", Path: pathValue, EntryID: refs[0].entryID, RefID: refs[0].refID, Detail: "available catalog ref was not observed by the no-follow physical inventory"})
		}
		result = append(result, evidence)
	}
	return result
}

type rootStructureEvidence struct {
	RelativePath string    `json:"relative_path"`
	ObjectType   string    `json:"object_type"`
	Mode         uint32    `json:"mode"`
	ModifiedAt   time.Time `json:"modified_at"`
	SizeBytes    int64     `json:"size_bytes,omitempty"`
	Checksum     string    `json:"checksum,omitempty"`
	LinkTarget   string    `json:"link_target,omitempty"`
}

func appendRootStructureEvidence(ctx context.Context, writer io.Writer, rootPath, pathValue string, info os.FileInfo, internalMarker bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	relative, ok := relativeWithin(pathValue, rootPath)
	if !ok || relative == "." {
		return fmt.Errorf("inventory path %q is outside root %q", pathValue, rootPath)
	}
	record := rootStructureEvidence{
		RelativePath: filepath.ToSlash(relative),
		Mode:         uint32(info.Mode()),
		ModifiedAt:   info.ModTime().UTC(),
	}
	switch {
	case info.IsDir():
		record.ObjectType = "directory"
	case info.Mode().IsRegular() && internalMarker:
		record.ObjectType = "internal_marker"
		record.SizeBytes = info.Size()
		evidence, err := inspectEvidenceContext(ctx, pathValue)
		if err != nil {
			return err
		}
		if evidence.ObjectType != "file" || evidence.SizeBytes != info.Size() || evidence.Mode != uint32(info.Mode()) || !evidence.ModifiedAt.Equal(info.ModTime().UTC()) {
			return fmt.Errorf("internal marker changed while inventorying")
		}
		record.Checksum = evidence.ChecksumType + ":" + evidence.ChecksumHex
	case info.Mode().IsRegular():
		record.ObjectType = "regular_file"
		record.SizeBytes = info.Size()
	case info.Mode()&os.ModeSymlink != 0:
		record.ObjectType = "symlink"
		target, err := os.Readlink(pathValue)
		if err != nil {
			return err
		}
		record.LinkTarget = target
	default:
		record.ObjectType = "special"
	}
	return json.NewEncoder(writer).Encode(record)
}

func allowedMigrationInternalMarker(root RootMapping, pathValue string) bool {
	if root.Name != "lane_accepted" || filepath.Base(pathValue) != ".loom-lane-custody.json" {
		return false
	}
	relative, ok := relativeWithin(pathValue, root.OldRoot)
	if !ok {
		return false
	}
	return len(strings.Split(filepath.ToSlash(relative), "/")) == 4
}

// ValidateRootMappings rejects ambiguous and unsafe rebases using lexical,
// cleaned paths. It deliberately performs no filesystem access so callers can
// fail before existence checks, directory walks, or payload hashing.
func ValidateRootMappings(roots []RootMapping) ([]RootMapping, error) {
	if len(roots) == 0 {
		return nil, fmt.Errorf("at least one migration root is required")
	}
	normalized := append([]RootMapping(nil), roots...)
	seenNames := map[string]struct{}{}
	for index := range normalized {
		root := &normalized[index]
		root.Name = strings.TrimSpace(root.Name)
		if root.Name == "" {
			return nil, fmt.Errorf("invalid root mapping %d: name is required", index)
		}
		if _, exists := seenNames[root.Name]; exists {
			return nil, fmt.Errorf("duplicate migration root name %q", root.Name)
		}
		seenNames[root.Name] = struct{}{}
		for label, value := range map[string]string{"old": root.OldRoot, "new": root.NewRoot} {
			if !filepath.IsAbs(value) || filepath.Clean(value) != value {
				return nil, fmt.Errorf("invalid root mapping %q: %s root must be absolute and clean", root.Name, label)
			}
		}
		if root.OldRoot == root.NewRoot {
			return nil, fmt.Errorf("invalid root mapping %q: old and new roots are identical", root.Name)
		}
	}
	for left := range normalized {
		for right := left + 1; right < len(normalized); right++ {
			if pathsOverlap(normalized[left].OldRoot, normalized[right].OldRoot) {
				return nil, fmt.Errorf("old migration roots %q and %q overlap", normalized[left].Name, normalized[right].Name)
			}
			if pathsOverlap(normalized[left].NewRoot, normalized[right].NewRoot) {
				return nil, fmt.Errorf("new migration roots %q and %q overlap", normalized[left].Name, normalized[right].Name)
			}
		}
	}
	for oldIndex := range normalized {
		for newIndex := range normalized {
			if pathsOverlap(normalized[oldIndex].OldRoot, normalized[newIndex].NewRoot) {
				return nil, fmt.Errorf("unsafe migration nesting: old root %q overlaps new root %q", normalized[oldIndex].Name, normalized[newIndex].Name)
			}
		}
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].Name < normalized[j].Name })
	return normalized, nil
}

func pathsOverlap(left, right string) bool {
	_, leftWithinRight := relativeWithin(left, right)
	_, rightWithinLeft := relativeWithin(right, left)
	return leftWithinRight || rightWithinLeft
}

func planEntry(ctx context.Context, manifest *Manifest, detail storagecatalog.EntryDetail) {
	newOriginal, originalRoot, originalChanged := rebaseAny(detail.Entry.OriginalSourcePath, manifest.Roots)
	newView, viewRoot, viewChanged := rebaseAny(detail.Entry.CurrentViewPath, manifest.Roots)
	for _, ref := range detail.PhysicalRefs {
		if !storagecatalog.PhysicalRefAvailable(ref) {
			continue
		}
		oldPath, ok := storagecatalog.PhysicalRefLocalPath(ref)
		if !ok {
			continue
		}
		newPath, root, changed := rebaseAny(oldPath, manifest.Roots)
		if !changed {
			continue
		}
		class, classified := storagecatalog.ClassifyPhysicalRef(ref)
		if !classified {
			manifest.Conflicts = append(manifest.Conflicts, Conflict{Code: "copy_class_unknown", Path: oldPath, EntryID: detail.Entry.StorageEntryID, RefID: ref.StoragePhysicalRefID, Detail: "available filesystem ref kind is not classified"})
			continue
		}
		if err := validateManagedPath(root.OldRoot, oldPath, false); err != nil {
			manifest.Conflicts = append(manifest.Conflicts, Conflict{Code: "source_path_escape", Path: oldPath, EntryID: detail.Entry.StorageEntryID, RefID: ref.StoragePhysicalRefID, Detail: err.Error()})
			continue
		}
		if err := validateManagedPath(root.NewRoot, newPath, true); err != nil {
			manifest.Conflicts = append(manifest.Conflicts, Conflict{Code: "destination_path_escape", Path: newPath, EntryID: detail.Entry.StorageEntryID, RefID: ref.StoragePhysicalRefID, Detail: err.Error()})
			continue
		}
		evidence, err := inspectEvidenceContext(ctx, oldPath)
		if err != nil {
			manifest.Conflicts = append(manifest.Conflicts, Conflict{Code: "source_evidence_unavailable", Path: oldPath, EntryID: detail.Entry.StorageEntryID, RefID: ref.StoragePhysicalRefID, Detail: err.Error()})
			continue
		}
		if destination, err := inspectEvidenceContext(ctx, newPath); err == nil {
			if !sameEvidence(evidence, destination) {
				manifest.Conflicts = append(manifest.Conflicts, Conflict{Code: "destination_collision", Path: newPath, EntryID: detail.Entry.StorageEntryID, RefID: ref.StoragePhysicalRefID, Detail: "destination exists with different type, size, or checksum"})
				continue
			}
		} else if !os.IsNotExist(err) {
			manifest.Conflicts = append(manifest.Conflicts, Conflict{Code: "destination_evidence_unavailable", Path: newPath, EntryID: detail.Entry.StorageEntryID, RefID: ref.StoragePhysicalRefID, Detail: err.Error()})
			continue
		}
		action := PathAction{RootName: root.Name, StorageEntryID: detail.Entry.StorageEntryID, PhysicalRefID: ref.StoragePhysicalRefID, RefKind: ref.RefKind, RefClass: class, OldURI: ref.URI, NewURI: newPath, Expected: evidence}
		action.Rollback = RollbackAction{PhysicalRefID: action.PhysicalRefID, StorageEntryID: action.StorageEntryID, ExpectedURI: action.NewURI, RestoreURI: action.OldURI}
		action.ActionID = actionID(action)
		manifest.Actions = append(manifest.Actions, action)
	}
	if originalChanged || viewChanged {
		action := PathAction{StorageEntryID: detail.Entry.StorageEntryID, OldOriginal: detail.Entry.OriginalSourcePath, NewOriginal: chooseChanged(detail.Entry.OriginalSourcePath, newOriginal, originalChanged), OldView: detail.Entry.CurrentViewPath, NewView: chooseChanged(detail.Entry.CurrentViewPath, newView, viewChanged)}
		planned := true
		cached := map[string]FileEvidence{}
		paths := []struct {
			field            string
			oldPath, newPath string
			root             RootMapping
			changed          bool
		}{
			{field: "original_source_path", oldPath: detail.Entry.OriginalSourcePath, newPath: newOriginal, root: originalRoot, changed: originalChanged},
			{field: "current_view_path", oldPath: detail.Entry.CurrentViewPath, newPath: newView, root: viewRoot, changed: viewChanged},
		}
		for _, item := range paths {
			if !item.changed {
				continue
			}
			if action.RootName == "" {
				action.RootName = item.root.Name
			}
			if err := validateManagedPath(item.root.OldRoot, item.oldPath, false); err != nil {
				manifest.Conflicts = append(manifest.Conflicts, Conflict{Code: "entry_path_escape", Path: item.oldPath, EntryID: detail.Entry.StorageEntryID, Detail: err.Error()})
				planned = false
				continue
			}
			if err := validateManagedPath(item.root.NewRoot, item.newPath, true); err != nil {
				manifest.Conflicts = append(manifest.Conflicts, Conflict{Code: "entry_destination_escape", Path: item.newPath, EntryID: detail.Entry.StorageEntryID, Detail: err.Error()})
				planned = false
				continue
			}
			key := item.oldPath + "\x00" + item.newPath
			evidence, ok := cached[key]
			if !ok {
				var err error
				evidence, err = inspectEvidenceContext(ctx, item.oldPath)
				if err != nil {
					manifest.Conflicts = append(manifest.Conflicts, Conflict{Code: "entry_path_evidence_unavailable", Path: item.oldPath, EntryID: detail.Entry.StorageEntryID, Detail: err.Error()})
					planned = false
					continue
				}
				if destination, err := inspectEvidenceContext(ctx, item.newPath); err == nil {
					if !sameEvidence(evidence, destination) {
						manifest.Conflicts = append(manifest.Conflicts, Conflict{Code: "entry_destination_collision", Path: item.newPath, EntryID: detail.Entry.StorageEntryID, Detail: "destination exists with different type, size, or checksum"})
						planned = false
						continue
					}
				} else if !os.IsNotExist(err) {
					manifest.Conflicts = append(manifest.Conflicts, Conflict{Code: "entry_destination_evidence_unavailable", Path: item.newPath, EntryID: detail.Entry.StorageEntryID, Detail: err.Error()})
					planned = false
					continue
				}
				cached[key] = evidence
			}
			action.EntryEvidence = append(action.EntryEvidence, EntryPathEvidence{Field: item.field, RootName: item.root.Name, OldPath: item.oldPath, NewPath: item.newPath, Expected: evidence})
		}
		if !planned {
			return
		}
		if len(action.EntryEvidence) > 0 {
			action.Expected = action.EntryEvidence[0].Expected
		}
		action.Rollback = RollbackAction{StorageEntryID: action.StorageEntryID, ExpectedOriginal: action.NewOriginal, RestoreOriginal: action.OldOriginal, ExpectedView: action.NewView, RestoreView: action.OldView}
		action.ActionID = actionID(action)
		manifest.Actions = append(manifest.Actions, action)
	}
}

func rebaseAny(value string, roots []RootMapping) (string, RootMapping, bool) {
	if !filepath.IsAbs(value) {
		return value, RootMapping{}, false
	}
	clean := filepath.Clean(value)
	for _, root := range roots {
		if rel, ok := relativeWithin(clean, root.OldRoot); ok {
			return filepath.Join(root.NewRoot, rel), root, true
		}
	}
	return value, RootMapping{}, false
}
func relativeWithin(pathValue, root string) (string, bool) {
	rel, err := filepath.Rel(root, pathValue)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}
func chooseChanged(old, new string, changed bool) string {
	if changed {
		return new
	}
	return old
}
func filesystemID(pathValue string) (string, error) {
	info, err := os.Lstat(pathValue)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("root must not be a symlink")
	}
	if !info.IsDir() {
		return "", fmt.Errorf("root must be a directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("filesystem id unavailable")
	}
	return fmt.Sprint(stat.Dev), nil
}

// validateManagedPath requires lexical containment and rejects symlinks in
// every existing component below the reviewed root. A symlink leaf is allowed
// because it is itself a filesystem object whose target is hashed as evidence;
// it is never followed. Missing destination components are allowed only while
// planning an external move.
func validateManagedPath(root, pathValue string, allowMissing bool) error {
	if !filepath.IsAbs(pathValue) || filepath.Clean(pathValue) != pathValue {
		return fmt.Errorf("path %q must be absolute and clean", pathValue)
	}
	if _, ok := relativeWithin(pathValue, root); !ok {
		return fmt.Errorf("path %q escapes reviewed root %q", pathValue, root)
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return fmt.Errorf("reviewed root %q is not a real directory", root)
	}
	rel, _ := filepath.Rel(root, pathValue)
	if rel == "." {
		return nil
	}
	parts := strings.Split(rel, string(filepath.Separator))
	current := root
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) && allowMissing {
			return nil
		}
		if err != nil {
			return err
		}
		leaf := index == len(parts)-1
		if info.Mode()&os.ModeSymlink != 0 && !leaf {
			return fmt.Errorf("path %q traverses symlink component %q", pathValue, current)
		}
		if !leaf && !info.IsDir() {
			return fmt.Errorf("path %q traverses non-directory component %q", pathValue, current)
		}
	}
	return nil
}
func inspectEvidence(pathValue string) (FileEvidence, error) {
	return inspectEvidenceContext(context.Background(), pathValue)
}

func inspectEvidenceContext(ctx context.Context, pathValue string) (FileEvidence, error) {
	if err := ctx.Err(); err != nil {
		return FileEvidence{}, err
	}
	info, err := os.Lstat(pathValue)
	if err != nil {
		return FileEvidence{}, err
	}
	e := FileEvidence{Path: filepath.Clean(pathValue), SizeBytes: info.Size(), Mode: uint32(info.Mode()), ModifiedAt: info.ModTime().UTC()}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		e.DeviceID = uint64(stat.Dev)
		e.Inode = uint64(stat.Ino)
	}
	switch {
	case info.Mode().IsRegular():
		e.ObjectType = "file"
		e.ChecksumType = "sha256"
		file, err := os.Open(pathValue)
		if err != nil {
			return FileEvidence{}, err
		}
		defer file.Close()
		hash := sha256.New()
		if _, err := io.Copy(hash, contextReader{ctx: ctx, reader: file}); err != nil {
			return FileEvidence{}, err
		}
		e.ChecksumHex = hex.EncodeToString(hash.Sum(nil))
	case info.Mode()&os.ModeSymlink != 0:
		e.ObjectType = "symlink"
		target, err := os.Readlink(pathValue)
		if err != nil {
			return FileEvidence{}, err
		}
		sum := sha256.Sum256([]byte(target))
		e.ChecksumType = "sha256-symlink-target"
		e.ChecksumHex = hex.EncodeToString(sum[:])
	case info.IsDir():
		e.ObjectType = "directory"
	default:
		e.ObjectType = "other"
	}
	return e, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}
func sameEvidence(left, right FileEvidence) bool {
	return left.ObjectType == right.ObjectType &&
		left.SizeBytes == right.SizeBytes &&
		left.Mode == right.Mode &&
		left.DeviceID == right.DeviceID &&
		left.Inode == right.Inode &&
		left.ModifiedAt.Equal(right.ModifiedAt) &&
		left.ChecksumType == right.ChecksumType &&
		left.ChecksumHex == right.ChecksumHex
}
func actionID(action PathAction) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{action.StorageEntryID, action.PhysicalRefID, action.OldURI, action.NewURI, action.OldOriginal, action.NewOriginal, action.OldView, action.NewView}, "\x00")))
	return "fsact_" + hex.EncodeToString(sum[:12])
}
