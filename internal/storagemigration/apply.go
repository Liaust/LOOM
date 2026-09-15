package storagemigration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/storagecatalog"
)

type CatalogRebinder interface {
	RebindPaths(context.Context, storagecatalog.RebindPathsInput) (storagecatalog.RebindPathsResult, error)
}

type CatalogBatchRebinder interface {
	RebindPathBatches(context.Context, int, storagecatalog.RebindPathsBatchLoader) (storagecatalog.RebindPathsResult, error)
}

type ApplyInput struct {
	Manifest                  Manifest
	NodeID, LayoutFingerprint string
	LoadedFromFile            bool
}

type PreparedApply struct {
	Catalog              storagecatalog.RebindPathsInput
	VerifiedDestinations int
}

type ApplyResult struct {
	Catalog              storagecatalog.RebindPathsResult `json:"catalog"`
	VerifiedDestinations int                              `json:"verified_destinations"`
	BatchesApplied       int                              `json:"batches_applied,omitempty"`
}

func Apply(ctx context.Context, catalog CatalogRebinder, input ApplyInput) (ApplyResult, error) {
	prepared, err := PreflightApply(ctx, input)
	if err != nil {
		return ApplyResult{}, err
	}
	if len(prepared.Catalog.PhysicalRefs) == 0 && len(prepared.Catalog.Entries) == 0 {
		return ApplyResult{VerifiedDestinations: prepared.VerifiedDestinations}, nil
	}
	if catalog == nil {
		return ApplyResult{}, fmt.Errorf("storage catalog is required")
	}
	if _, err := RecheckApplyEvidence(ctx, input); err != nil {
		return ApplyResult{}, fmt.Errorf("repeat evidence check: %w", err)
	}
	result, err := catalog.RebindPaths(ctx, prepared.Catalog)
	if err != nil {
		return ApplyResult{}, err
	}
	return ApplyResult{Catalog: result, VerifiedDestinations: prepared.VerifiedDestinations}, nil
}

// PrepareCatalogRebind validates the reviewed manifest contract and builds its
// bounded catalog input without reading payload bytes. Manifest-set callers use
// this while streaming children through the one catalog transaction, after the
// complete set has passed filesystem preflight.
func PrepareCatalogRebind(input ApplyInput) (storagecatalog.RebindPathsInput, error) {
	m, _, err := validateApplyInput(input)
	if err != nil {
		return storagecatalog.RebindPathsInput{}, err
	}
	return catalogRebindInput(m)
}

// PreflightApply validates one bounded manifest child, proves destination
// evidence, and returns the catalog rebind without mutating the database.
func PreflightApply(ctx context.Context, input ApplyInput) (PreparedApply, error) {
	m, rootByName, err := validateApplyInput(input)
	if err != nil {
		return PreparedApply{}, err
	}
	if err := VerifyReviewedRootMove(ctx, m.Roots, m.RootInventory); err != nil {
		return PreparedApply{}, err
	}
	return preflightApplyActions(ctx, m, rootByName)
}

// PreflightApplyActions repeats the bounded per-action evidence without
// walking every mapped root. Manifest-set callers must bracket all child calls
// with VerifyReviewedRootMove so a production-sized tree is scanned once per
// complete-set pass rather than once per child.
func PreflightApplyActions(ctx context.Context, input ApplyInput) (PreparedApply, error) {
	m, rootByName, err := validateApplyInput(input)
	if err != nil {
		return PreparedApply{}, err
	}
	return preflightApplyActions(ctx, m, rootByName)
}

func preflightApplyActions(ctx context.Context, m Manifest, rootByName map[string]RootMapping) (PreparedApply, error) {
	if err := verifyDestinationRootFilesystems(m); err != nil {
		return PreparedApply{}, err
	}
	catalogInput, err := catalogRebindInput(m)
	if err != nil {
		return PreparedApply{}, err
	}
	verified, err := verifyManifestFilesystem(ctx, m.Actions, rootByName)
	if err != nil {
		return PreparedApply{}, err
	}
	return PreparedApply{Catalog: catalogInput, VerifiedDestinations: verified}, nil
}

// RecheckApplyEvidence repeats destination identity and payload evidence after
// the full artifact has been preflighted and immediately before catalog apply.
func RecheckApplyEvidence(ctx context.Context, input ApplyInput) (int, error) {
	verified, err := RecheckApplyActionEvidence(ctx, input)
	if err != nil {
		return 0, err
	}
	if err := VerifyReviewedRootMove(ctx, input.Manifest.Roots, input.Manifest.RootInventory); err != nil {
		return 0, err
	}
	return verified, nil
}

// RecheckApplyActionEvidence repeats only the bounded action evidence. It is
// paired with one whole-set VerifyReviewedRootMove immediately before the
// manifest-set catalog transaction.
func RecheckApplyActionEvidence(ctx context.Context, input ApplyInput) (int, error) {
	m, rootByName, err := validateApplyInput(input)
	if err != nil {
		return 0, err
	}
	if err := verifyDestinationRootFilesystems(m); err != nil {
		return 0, err
	}
	return verifyManifestFilesystem(ctx, m.Actions, rootByName)
}

// VerifyReviewedRootMove proves the complete reviewed physical-root move,
// including roots with no catalog actions. It never follows payload symlinks.
// The old root must be absent and the new real directory must remain on the
// reviewed filesystem with the exact physical inventory counts.
func VerifyReviewedRootMove(ctx context.Context, roots []RootMapping, inventory []RootInventoryEvidence) error {
	normalizedRoots, err := ValidateRootMappings(roots)
	if err != nil {
		return fmt.Errorf("reviewed roots: %w", err)
	}
	if err := ValidateRootInventory(normalizedRoots, inventory, true); err != nil {
		return err
	}
	inventoryByName := make(map[string]RootInventoryEvidence, len(inventory))
	for _, evidence := range inventory {
		inventoryByName[evidence.RootName] = evidence
	}
	for _, root := range normalizedRoots {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := os.Lstat(root.OldRoot); err == nil {
			return fmt.Errorf("reviewed source root %s still exists; filesystem move is incomplete", root.Name)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect reviewed source root %s: %w", root.Name, err)
		}
		info, err := os.Lstat(root.NewRoot)
		if err != nil {
			return fmt.Errorf("inspect reviewed destination root %s: %w", root.Name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("reviewed destination root %s is not a real directory", root.Name)
		}
		if got, err := filesystemID(root.NewRoot); err != nil || got != root.NewFilesystemID {
			return fmt.Errorf("destination root %s filesystem changed or is missing", root.Name)
		}
		actual := RootInventoryEvidence{RootName: root.Name, OldRoot: root.OldRoot}
		structureHash := sha256.New()
		walkErr := filepath.WalkDir(root.NewRoot, func(pathValue string, entry os.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				actual.InventoryErrorCount++
				if entry != nil && entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if pathValue == root.NewRoot {
				return nil
			}
			entryInfo, err := entry.Info()
			if err != nil {
				actual.InventoryErrorCount++
				return nil
			}
			markerRoot := RootMapping{Name: root.Name, OldRoot: root.NewRoot}
			internalMarker := entryInfo.Mode().IsRegular() && allowedMigrationInternalMarker(markerRoot, pathValue)
			if err := appendRootStructureEvidence(ctx, structureHash, root.NewRoot, pathValue, entryInfo, internalMarker); err != nil {
				actual.InventoryErrorCount++
				return nil
			}
			switch {
			case entryInfo.IsDir():
				actual.DirectoryCount++
			case entryInfo.Mode().IsRegular():
				if internalMarker {
					actual.InternalMarkerCount++
				} else {
					actual.RegularFileCount++
				}
			case entryInfo.Mode()&os.ModeSymlink != 0:
				actual.SymlinkCount++
			default:
				actual.SpecialFileCount++
			}
			return nil
		})
		if walkErr != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
			actual.InventoryErrorCount++
		}
		actual.StructureDigest = "sha256:" + hex.EncodeToString(structureHash.Sum(nil))
		reviewed := inventoryByName[root.Name]
		if actual.DirectoryCount != reviewed.DirectoryCount ||
			actual.RegularFileCount != reviewed.RegularFileCount ||
			actual.InternalMarkerCount != reviewed.InternalMarkerCount ||
			actual.SymlinkCount != reviewed.SymlinkCount ||
			actual.SpecialFileCount != reviewed.SpecialFileCount ||
			actual.InventoryErrorCount != reviewed.InventoryErrorCount ||
			actual.StructureDigest != reviewed.StructureDigest {
			return fmt.Errorf("destination root %s physical inventory changed: directories=%d/%d regular=%d/%d internal=%d/%d symlink=%d/%d special=%d/%d errors=%d/%d structure=%s/%s",
				root.Name,
				actual.DirectoryCount, reviewed.DirectoryCount,
				actual.RegularFileCount, reviewed.RegularFileCount,
				actual.InternalMarkerCount, reviewed.InternalMarkerCount,
				actual.SymlinkCount, reviewed.SymlinkCount,
				actual.SpecialFileCount, reviewed.SpecialFileCount,
				actual.InventoryErrorCount, reviewed.InventoryErrorCount,
				actual.StructureDigest, reviewed.StructureDigest)
		}
	}
	return nil
}

func validateApplyInput(input ApplyInput) (Manifest, map[string]RootMapping, error) {
	m := input.Manifest
	if !input.LoadedFromFile {
		return Manifest{}, nil, fmt.Errorf("generated-at-runtime manifests cannot be applied")
	}
	if m.SchemaVersion != ManifestSchemaVersion {
		return Manifest{}, nil, fmt.Errorf("unsupported manifest schema %q", m.SchemaVersion)
	}
	if err := ValidateManifestHash(m); err != nil {
		return Manifest{}, nil, err
	}
	if err := ValidateManifestSemantics(m); err != nil {
		return Manifest{}, nil, err
	}
	if err := ValidateRootInventory(m.Roots, m.RootInventory, true); err != nil {
		return Manifest{}, nil, err
	}
	if strings.TrimSpace(m.Review.ReviewedBy) == "" || m.Review.ReviewedAt == nil || m.Review.ReviewedAt.IsZero() {
		return Manifest{}, nil, fmt.Errorf("manifest is not reviewed")
	}
	if len(m.Conflicts) > 0 {
		return Manifest{}, nil, fmt.Errorf("manifest has %d open conflicts", len(m.Conflicts))
	}
	if strings.TrimSpace(input.NodeID) != m.NodeID || strings.TrimSpace(input.LayoutFingerprint) != m.LayoutFingerprint {
		return Manifest{}, nil, fmt.Errorf("manifest node/layout does not match this runtime")
	}
	rootByName := make(map[string]RootMapping, len(m.Roots))
	for _, root := range m.Roots {
		rootByName[root.Name] = root
	}
	return m, rootByName, nil
}

func verifyDestinationRootFilesystems(manifest Manifest) error {
	for _, root := range manifest.Roots {
		if got, err := filesystemID(root.NewRoot); err != nil || got != root.NewFilesystemID {
			return fmt.Errorf("destination root %s filesystem changed or is missing", root.Name)
		}
	}
	return nil
}

func catalogRebindInput(m Manifest) (storagecatalog.RebindPathsInput, error) {
	refs := []storagecatalog.PhysicalRefPathRebind{}
	entries := map[string]storagecatalog.EntryPathRebind{}
	for _, action := range m.Actions {
		if action.NewURI != "" {
			refs = append(refs, storagecatalog.PhysicalRefPathRebind{StoragePhysicalRefID: action.PhysicalRefID, StorageEntryID: action.StorageEntryID, ExpectedURI: action.OldURI, NewURI: action.NewURI})
		}
		if action.OldOriginal != action.NewOriginal || action.OldView != action.NewView {
			candidate := storagecatalog.EntryPathRebind{StorageEntryID: action.StorageEntryID, ExpectedOriginalSourcePath: action.OldOriginal, NewOriginalSourcePath: action.NewOriginal, ExpectedCurrentViewPath: action.OldView, NewCurrentViewPath: action.NewView}
			if prior, ok := entries[action.StorageEntryID]; ok && prior != candidate {
				return storagecatalog.RebindPathsInput{}, fmt.Errorf("conflicting entry path actions for %s", action.StorageEntryID)
			}
			entries[action.StorageEntryID] = candidate
		}
	}
	entryList := make([]storagecatalog.EntryPathRebind, 0, len(entries))
	for _, entry := range entries {
		entryList = append(entryList, entry)
	}
	sort.Slice(entryList, func(i, j int) bool { return entryList[i].StorageEntryID < entryList[j].StorageEntryID })
	return storagecatalog.RebindPathsInput{PhysicalRefs: refs, Entries: entryList}, nil
}

// ValidateManifestSemantics rejects duplicate, cross-root, or incomplete
// actions before any filesystem evidence read or catalog mutation.
func ValidateManifestSemantics(manifest Manifest) error {
	roots, err := ValidateRootMappings(manifest.Roots)
	if err != nil {
		return fmt.Errorf("manifest roots: %w", err)
	}
	rootByName := make(map[string]RootMapping, len(roots))
	for _, root := range roots {
		rootByName[root.Name] = root
	}
	if err := ValidateRootInventory(roots, manifest.RootInventory, false); err != nil {
		return err
	}
	actionIDs := map[string]struct{}{}
	refIDs := map[string]struct{}{}
	entryPaths := map[string]storagecatalog.EntryPathRebind{}
	for index, action := range manifest.Actions {
		if strings.TrimSpace(action.ActionID) == "" || action.ActionID != actionID(action) {
			return fmt.Errorf("manifest action %d has an invalid action id", index)
		}
		if _, exists := actionIDs[action.ActionID]; exists {
			return fmt.Errorf("manifest has duplicate action id %s", action.ActionID)
		}
		actionIDs[action.ActionID] = struct{}{}
		if strings.TrimSpace(action.StorageEntryID) == "" {
			return fmt.Errorf("manifest action %s has no storage entry id", action.ActionID)
		}
		if action.NewURI != "" {
			root, ok := rootByName[action.RootName]
			if !ok {
				return fmt.Errorf("manifest action %s references unknown root %q", action.ActionID, action.RootName)
			}
			if strings.TrimSpace(action.PhysicalRefID) == "" {
				return fmt.Errorf("manifest action %s has no physical ref id", action.ActionID)
			}
			if _, exists := refIDs[action.PhysicalRefID]; exists {
				return fmt.Errorf("manifest has duplicate physical ref action %s", action.PhysicalRefID)
			}
			refIDs[action.PhysicalRefID] = struct{}{}
			oldPath, ok := storagecatalog.PhysicalRefLocalPath(storagecatalog.PhysicalRef{RefKind: action.RefKind, URI: action.OldURI})
			if !ok || !rebaseMatches(oldPath, action.NewURI, root) {
				return fmt.Errorf("manifest action %s URI rebase escapes root %q", action.ActionID, action.RootName)
			}
			class, ok := storagecatalog.ClassifyPhysicalRef(storagecatalog.PhysicalRef{RefKind: action.RefKind})
			if !ok || class != action.RefClass {
				return fmt.Errorf("manifest action %s ref class does not match kind %q", action.ActionID, action.RefKind)
			}
			if err := validateFileEvidence(action.Expected, oldPath); err != nil {
				return fmt.Errorf("manifest action %s: %w", action.ActionID, err)
			}
		} else if action.PhysicalRefID != "" || action.OldURI != "" || action.RefKind != "" || action.RefClass != "" {
			return fmt.Errorf("manifest action %s has an incomplete physical ref rebind", action.ActionID)
		}
		entryChanged := action.OldOriginal != action.NewOriginal || action.OldView != action.NewView
		if entryChanged {
			if len(action.EntryEvidence) == 0 {
				return fmt.Errorf("manifest action %s has unverified entry path changes", action.ActionID)
			}
			fields := map[string]bool{}
			for _, evidence := range action.EntryEvidence {
				root, ok := rootByName[evidence.RootName]
				if !ok || !rebaseMatches(evidence.OldPath, evidence.NewPath, root) {
					return fmt.Errorf("manifest action %s entry evidence escapes root %q", action.ActionID, evidence.RootName)
				}
				if fields[evidence.Field] {
					return fmt.Errorf("manifest action %s duplicates %s evidence", action.ActionID, evidence.Field)
				}
				fields[evidence.Field] = true
				switch evidence.Field {
				case "original_source_path":
					if action.OldOriginal != evidence.OldPath || action.NewOriginal != evidence.NewPath {
						return fmt.Errorf("manifest action %s original path evidence does not match action", action.ActionID)
					}
				case "current_view_path":
					if action.OldView != evidence.OldPath || action.NewView != evidence.NewPath {
						return fmt.Errorf("manifest action %s view path evidence does not match action", action.ActionID)
					}
				default:
					return fmt.Errorf("manifest action %s has unknown entry evidence field %q", action.ActionID, evidence.Field)
				}
				if err := validateFileEvidence(evidence.Expected, evidence.OldPath); err != nil {
					return fmt.Errorf("manifest action %s: %w", action.ActionID, err)
				}
			}
			if action.OldOriginal != action.NewOriginal && !fields["original_source_path"] {
				return fmt.Errorf("manifest action %s lacks original path evidence", action.ActionID)
			}
			if action.OldView != action.NewView && !fields["current_view_path"] {
				return fmt.Errorf("manifest action %s lacks view path evidence", action.ActionID)
			}
			candidate := storagecatalog.EntryPathRebind{StorageEntryID: action.StorageEntryID, ExpectedOriginalSourcePath: action.OldOriginal, NewOriginalSourcePath: action.NewOriginal, ExpectedCurrentViewPath: action.OldView, NewCurrentViewPath: action.NewView}
			if prior, exists := entryPaths[action.StorageEntryID]; exists && prior != candidate {
				return fmt.Errorf("manifest has conflicting entry path actions for %s", action.StorageEntryID)
			}
			entryPaths[action.StorageEntryID] = candidate
		} else if len(action.EntryEvidence) > 0 {
			return fmt.Errorf("manifest action %s has entry evidence without a path change", action.ActionID)
		}
		if action.NewURI == "" && !entryChanged {
			return fmt.Errorf("manifest action %s has no change", action.ActionID)
		}
		if action.Rollback != expectedRollback(action) {
			return fmt.Errorf("manifest action %s rollback does not exactly reverse the action", action.ActionID)
		}
	}
	return nil
}

// ValidateRootInventory binds the physical/catalog reconciliation summary to
// every mapped old root. Apply additionally requires a clean inventory, so a
// reviewed artifact cannot hide untracked custody merely by deleting its
// conflict records.
func ValidateRootInventory(roots []RootMapping, inventory []RootInventoryEvidence, requireClean bool) error {
	if len(inventory) != len(roots) {
		return fmt.Errorf("root inventory has %d entries for %d mapped roots", len(inventory), len(roots))
	}
	rootByName := make(map[string]RootMapping, len(roots))
	for _, root := range roots {
		rootByName[root.Name] = root
	}
	seen := make(map[string]struct{}, len(inventory))
	for index, evidence := range inventory {
		root, ok := rootByName[evidence.RootName]
		if !ok {
			return fmt.Errorf("root inventory %d references unknown root %q", index, evidence.RootName)
		}
		if _, exists := seen[evidence.RootName]; exists {
			return fmt.Errorf("root inventory duplicates root %q", evidence.RootName)
		}
		seen[evidence.RootName] = struct{}{}
		if evidence.OldRoot != root.OldRoot {
			return fmt.Errorf("root inventory %q old root %q does not match mapping %q", evidence.RootName, evidence.OldRoot, root.OldRoot)
		}
		counts := []int{
			evidence.DirectoryCount,
			evidence.RegularFileCount,
			evidence.CatalogRefCount,
			evidence.CatalogPathCount,
			evidence.MatchedFileCount,
			evidence.InternalMarkerCount,
			evidence.UntrackedFileCount,
			evidence.MissingCatalogCount,
			evidence.DuplicateCatalogCount,
			evidence.SymlinkCount,
			evidence.SpecialFileCount,
			evidence.InventoryErrorCount,
		}
		for _, count := range counts {
			if count < 0 {
				return fmt.Errorf("root inventory %q has a negative count", evidence.RootName)
			}
		}
		digestValue := strings.TrimPrefix(evidence.StructureDigest, "sha256:")
		decodedDigest, decodeErr := hex.DecodeString(digestValue)
		if !strings.HasPrefix(evidence.StructureDigest, "sha256:") || decodeErr != nil || len(decodedDigest) != sha256.Size {
			return fmt.Errorf("root inventory %q has an invalid structure digest", evidence.RootName)
		}
		if evidence.RegularFileCount != evidence.MatchedFileCount+evidence.UntrackedFileCount {
			return fmt.Errorf("root inventory %q regular-file counts are inconsistent", evidence.RootName)
		}
		if evidence.CatalogRefCount != evidence.CatalogPathCount+evidence.DuplicateCatalogCount {
			return fmt.Errorf("root inventory %q catalog-ref counts are inconsistent", evidence.RootName)
		}
		if evidence.CatalogPathCount != evidence.MatchedFileCount+evidence.MissingCatalogCount {
			return fmt.Errorf("root inventory %q catalog-path counts are inconsistent", evidence.RootName)
		}
		if requireClean {
			blocking := evidence.UntrackedFileCount + evidence.MissingCatalogCount + evidence.DuplicateCatalogCount + evidence.SymlinkCount + evidence.SpecialFileCount + evidence.InventoryErrorCount
			if blocking != 0 {
				return fmt.Errorf("root inventory %q has %d blocking physical/catalog findings", evidence.RootName, blocking)
			}
		}
	}
	return nil
}

func expectedRollback(action PathAction) RollbackAction {
	return RollbackAction{PhysicalRefID: action.PhysicalRefID, StorageEntryID: action.StorageEntryID, ExpectedURI: action.NewURI, RestoreURI: action.OldURI, ExpectedOriginal: action.NewOriginal, RestoreOriginal: action.OldOriginal, ExpectedView: action.NewView, RestoreView: action.OldView}
}

func rebaseMatches(oldPath, newPath string, root RootMapping) bool {
	if !filepath.IsAbs(oldPath) || !filepath.IsAbs(newPath) || filepath.Clean(oldPath) != oldPath || filepath.Clean(newPath) != newPath {
		return false
	}
	rel, ok := relativeWithin(oldPath, root.OldRoot)
	return ok && filepath.Join(root.NewRoot, rel) == newPath
}

func validateFileEvidence(evidence FileEvidence, oldPath string) error {
	if evidence.Path != filepath.Clean(oldPath) {
		return fmt.Errorf("file evidence path %q does not match source %q", evidence.Path, oldPath)
	}
	if evidence.DeviceID == 0 || evidence.Inode == 0 {
		return fmt.Errorf("file evidence has no device/inode identity")
	}
	switch evidence.ObjectType {
	case "file":
		if evidence.ChecksumType != "sha256" || strings.TrimSpace(evidence.ChecksumHex) == "" {
			return fmt.Errorf("file evidence has no SHA-256 checksum")
		}
	case "symlink":
		if evidence.ChecksumType != "sha256-symlink-target" || strings.TrimSpace(evidence.ChecksumHex) == "" {
			return fmt.Errorf("symlink evidence has no target checksum")
		}
	case "directory", "other":
	default:
		return fmt.Errorf("unsupported evidence object type %q", evidence.ObjectType)
	}
	return nil
}

func verifyManifestFilesystem(ctx context.Context, actions []PathAction, roots map[string]RootMapping) (int, error) {
	verified := 0
	seen := map[string]struct{}{}
	verify := func(actionID string, root RootMapping, oldPath, newPath string, expected FileEvidence) error {
		key := strings.Join([]string{oldPath, newPath, expected.ObjectType, fmt.Sprint(expected.SizeBytes), fmt.Sprint(expected.Mode), fmt.Sprint(expected.DeviceID), fmt.Sprint(expected.Inode), expected.ModifiedAt.UTC().Format(time.RFC3339Nano), expected.ChecksumType, expected.ChecksumHex}, "\x00")
		if _, exists := seen[key]; exists {
			return nil
		}
		if err := validateManagedPath(root.NewRoot, newPath, false); err != nil {
			return fmt.Errorf("destination path invalid for action %s: %w", actionID, err)
		}
		destination, err := inspectEvidenceContext(ctx, newPath)
		if err != nil {
			return fmt.Errorf("destination missing for action %s: %w", actionID, err)
		}
		if !sameEvidence(expected, destination) {
			return fmt.Errorf("destination evidence changed for action %s", actionID)
		}
		if _, err := os.Lstat(oldPath); err == nil {
			if pathErr := validateManagedPath(root.OldRoot, oldPath, false); pathErr != nil {
				return fmt.Errorf("source path invalid for action %s: %w", actionID, pathErr)
			}
			return fmt.Errorf("source still exists for action %s; filesystem move is incomplete", actionID)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect source for action %s: %w", actionID, err)
		}
		seen[key] = struct{}{}
		verified++
		return nil
	}
	for _, action := range actions {
		if err := ctx.Err(); err != nil {
			return verified, err
		}
		if action.NewURI != "" {
			root := roots[action.RootName]
			oldPath, _ := storagecatalog.PhysicalRefLocalPath(storagecatalog.PhysicalRef{RefKind: action.RefKind, URI: action.OldURI})
			if err := verify(action.ActionID, root, oldPath, action.NewURI, action.Expected); err != nil {
				return verified, err
			}
		}
		for _, evidence := range action.EntryEvidence {
			if err := verify(action.ActionID, roots[evidence.RootName], evidence.OldPath, evidence.NewPath, evidence.Expected); err != nil {
				return verified, err
			}
		}
	}
	return verified, nil
}

func RollbackManifest(source Manifest, generatedAt time.Time) (Manifest, error) {
	if err := ValidateManifestHash(source); err != nil {
		return Manifest{}, err
	}
	if err := ValidateManifestSemantics(source); err != nil {
		return Manifest{}, err
	}
	if err := ValidateRootInventory(source.Roots, source.RootInventory, true); err != nil {
		return Manifest{}, err
	}
	if len(source.Conflicts) != 0 {
		return Manifest{}, fmt.Errorf("forward manifest has %d open conflicts", len(source.Conflicts))
	}
	if strings.TrimSpace(source.Review.ReviewedBy) == "" || source.Review.ReviewedAt == nil || source.Review.ReviewedAt.IsZero() {
		return Manifest{}, fmt.Errorf("forward manifest is not reviewed")
	}
	rollback := source
	rollback.GeneratedAt = generatedAt.UTC()
	rollback.Review = Review{}
	rollback.Conflicts = nil
	for index := range rollback.Roots {
		root := &rollback.Roots[index]
		root.OldRoot, root.NewRoot = root.NewRoot, root.OldRoot
		root.OldFilesystemID, root.NewFilesystemID = root.NewFilesystemID, root.OldFilesystemID
	}
	rollbackRootByName := make(map[string]RootMapping, len(rollback.Roots))
	for _, root := range rollback.Roots {
		rollbackRootByName[root.Name] = root
	}
	for index := range rollback.RootInventory {
		rollback.RootInventory[index].OldRoot = rollbackRootByName[rollback.RootInventory[index].RootName].OldRoot
	}
	for index := range rollback.Actions {
		action := &rollback.Actions[index]
		prior := action.Rollback
		action.OldURI, action.NewURI = prior.ExpectedURI, prior.RestoreURI
		action.OldOriginal, action.NewOriginal = prior.ExpectedOriginal, prior.RestoreOriginal
		action.OldView, action.NewView = prior.ExpectedView, prior.RestoreView
		if action.NewURI != "" {
			if oldPath, ok := storagecatalog.PhysicalRefLocalPath(storagecatalog.PhysicalRef{RefKind: action.RefKind, URI: action.OldURI}); ok {
				action.Expected.Path = oldPath
			}
		}
		for evidenceIndex := range action.EntryEvidence {
			evidence := &action.EntryEvidence[evidenceIndex]
			evidence.OldPath, evidence.NewPath = evidence.NewPath, evidence.OldPath
			evidence.Expected.Path = evidence.OldPath
		}
		if action.NewURI == "" && len(action.EntryEvidence) > 0 {
			action.Expected = action.EntryEvidence[0].Expected
		}
		action.Rollback = expectedRollback(*action)
		action.ActionID = actionID(*action)
	}
	sort.Slice(rollback.Actions, func(i, j int) bool {
		if rollback.Actions[i].StorageEntryID != rollback.Actions[j].StorageEntryID {
			return rollback.Actions[i].StorageEntryID < rollback.Actions[j].StorageEntryID
		}
		return rollback.Actions[i].ActionID < rollback.Actions[j].ActionID
	})
	if err := RefreshManifestHash(&rollback); err != nil {
		return Manifest{}, err
	}
	return rollback, nil
}
