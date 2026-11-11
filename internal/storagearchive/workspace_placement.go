package storagearchive

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
	"loom.local/loom/internal/storagecatalog"
)

func catalogObservationInput(plan WorkspaceArchivePlan) []storagecatalog.WorkspaceCatalogObservation {
	var result []storagecatalog.WorkspaceCatalogObservation
	for _, item := range plan.CatalogObservations {
		result = append(result, storagecatalog.WorkspaceCatalogObservation{StorageEntryID: item.StorageEntryID, OriginalPath: item.OriginalPath, CurrentViewPath: item.CurrentViewPath})
	}
	return result
}

func (s WorkspaceMoveService) planPlacementCatalog(ctx context.Context, source, destination WorkspacePathRef) ([]WorkspaceCatalogRebind, error) {
	details, err := s.Catalog.ListWorkspaceArchiveCatalogEntries(ctx, source.AbsolutePath, source.RelativePath)
	if err != nil {
		return nil, err
	}
	var result []WorkspaceCatalogRebind
	for _, detail := range details {
		view, _, err := rebaseCatalogPath(detail.Entry.CurrentViewPath, source, destination)
		if err != nil {
			return nil, err
		}
		if view != detail.Entry.CurrentViewPath {
			result = append(result, WorkspaceCatalogRebind{StorageEntryID: detail.Entry.StorageEntryID, ExpectedOriginalPath: detail.Entry.OriginalSourcePath, NewOriginalPath: detail.Entry.OriginalSourcePath, ExpectedCurrentViewPath: detail.Entry.CurrentViewPath, NewCurrentViewPath: view})
		}
		for _, ref := range detail.PhysicalRefs {
			uri, _, err := rebaseCatalogPath(ref.URI, source, destination)
			if err != nil {
				return nil, err
			}
			if uri != ref.URI {
				result = append(result, WorkspaceCatalogRebind{StorageEntryID: detail.Entry.StorageEntryID, StoragePhysicalRefID: ref.StoragePhysicalRefID, ExpectedURI: ref.URI, NewURI: uri})
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].StorageEntryID != result[j].StorageEntryID {
			return result[i].StorageEntryID < result[j].StorageEntryID
		}
		return result[i].StoragePhysicalRefID < result[j].StoragePhysicalRefID
	})
	return result, validateWorkspaceCatalogRebinds(result)
}

func (s WorkspaceMoveService) qualifyArchiveLocation(ctx context.Context, inspection WorkspaceArchiveInspection) (WorkspaceArchiveInspection, error) {
	ref := inspection.Plan.Destination.Path
	if s.CurrentLocation != nil {
		current, rev, err := s.CurrentLocation(ctx, "workspace:"+inspection.Plan.OperationID, ref.AbsolutePath)
		if err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		rel, err := filepath.Rel(s.Roots.StorageRoot, current)
		if err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		ref, err = retainedRef(s.Roots, filepath.ToSlash(rel))
		if err != nil {
			return WorkspaceArchiveInspection{}, err
		}
		inspection.RelocationID = rev
	}
	if ref == inspection.Plan.Destination.Path && inspection.Plan.Placement == nil && inspection.RelocationID == "" {
		return inspection, nil
	}
	inspection.CurrentPayload = &ref
	return inspection, nil
}

func (s WorkspaceMoveService) inspectRelocatedArchive(ctx context.Context, plan WorkspaceArchivePlan, current string) (WorkspaceCustodyState, *WorkspaceArchiveManifest, *WorkspaceArchiveFinding, error) {
	paths, err := ResolveWorkspacePaths(s.Roots, plan.Kind, plan.Slug)
	if err != nil {
		return CustodyAmbiguous, nil, nil, err
	}
	rel, err := filepath.Rel(s.Roots.StorageRoot, current)
	if err != nil {
		return CustodyAmbiguous, nil, nil, err
	}
	ref, err := retainedRef(s.Roots, filepath.ToSlash(rel))
	if err != nil || ref.RelativePath != "Projects/"+plan.Slug {
		return CustodyAmbiguous, nil, nil, fmt.Errorf("archive relocation is not the named project payload")
	}
	meta, err := openHeldDirectoryChain(s.Roots.StorageRoot, paths.ArchiveContainer.RelativePath, nil)
	if err != nil {
		return CustodyAmbiguous, nil, nil, err
	}
	defer meta.Close()
	if err = verifyContainerEntries(meta.ParentFD(), map[string]bool{"archive.json": true}); err != nil {
		return CustodyAmbiguous, nil, nil, err
	}
	manifest, present, err := s.readAndVerifyManifestAt(ctx, meta.ParentFD(), plan)
	if err != nil || !present {
		return CustodyAmbiguous, nil, nil, fmt.Errorf("archive metadata unavailable: %v", err)
	}
	inv, err := inventoryRetainedSchema(ctx, s.Roots, ref, NoFollowInventorySchemaVersion)
	if err != nil || !reflect.DeepEqual(inv, plan.Inventory) {
		return CustodyAmbiguous, manifest, nil, fmt.Errorf("relocated archive inventory does not match original: %v", err)
	}
	active, err := openHeldDirectoryChain(s.Roots.BoxRoot, path.Dir(paths.Active.RelativePath), plan.SourceAncestors)
	if err != nil {
		return CustodyAmbiguous, nil, nil, err
	}
	defer active.Close()
	if exists, e := namePresentNoFollow(active.ParentFD(), plan.Slug); e != nil || exists {
		return CustodyAmbiguous, manifest, nil, fmt.Errorf("active path exists alongside retained archive")
	}
	old, err := openHeldDirectoryChain(s.Roots.StorageRoot, path.Dir(paths.ArchivePayload.RelativePath), nil)
	if err != nil {
		return CustodyAmbiguous, nil, nil, err
	}
	defer old.Close()
	if exists, e := namePresentNoFollow(old.ParentFD(), path.Base(paths.ArchivePayload.RelativePath)); e != nil || exists {
		return CustodyAmbiguous, manifest, nil, fmt.Errorf("historical archive payload path unexpectedly exists")
	}
	return CustodyArchived, manifest, nil, nil
}

func archiveMetadataAncestors(plan WorkspaceArchivePlan) []WorkspaceAncestorBinding {
	if plan.Placement != nil {
		return plan.Placement.MetadataAncestors
	}
	return plan.DestinationAncestors
}

func workspaceOperationSchema(plan WorkspaceArchivePlan) string {
	if plan.SchemaVersion == WorkspaceArchivePlanSchemaVersionV3 {
		return WorkspaceArchiveOperationSchemaVersionV3
	}
	if plan.Placement != nil {
		return WorkspaceArchiveOperationSchemaVersionV2
	}
	return WorkspaceArchiveOperationSchemaVersion
}

func workspaceEventSchema(operation WorkspaceArchiveOperation) string {
	if operation.SchemaVersion == WorkspaceArchiveOperationSchemaVersionV3 {
		return WorkspaceLifecycleEventSchemaVersionV3
	}
	if operation.Placement != nil {
		return WorkspaceLifecycleEventSchemaVersionV2
	}
	return WorkspaceLifecycleEventSchemaVersion
}

func (s WorkspaceMoveService) planNamedArchive(ctx context.Context, in WorkspaceArchivePlanInput) (WorkspaceArchivePlan, error) {
	if s.Catalog == nil {
		return WorkspaceArchivePlan{}, fmt.Errorf("workspace archive catalog is not configured")
	}
	if in.OperationID == "" {
		in.OperationID = newWorkspaceID("workspace_archive_operation")
	}
	paths, err := ResolveWorkspacePaths(s.Roots, in.Kind, in.Slug)
	if err != nil {
		return WorkspaceArchivePlan{}, err
	}
	location, err := s.ResolveActiveProjectLocation(ctx, strings.TrimSpace(in.ObjectID))
	if err != nil {
		return WorkspaceArchivePlan{}, err
	}
	if location != nil {
		paths.Active = location.Path
	}
	payload, err := retainedRef(s.Roots, "Projects/"+in.Slug)
	if err != nil {
		return WorkspaceArchivePlan{}, err
	}
	metadata, err := retainedRef(s.Roots, ".loom/workspace-archives/"+in.OperationID)
	if err != nil {
		return WorkspaceArchivePlan{}, err
	}
	source, err := openHeldDirectoryChain(s.Roots.BoxRoot, path.Dir(paths.Active.RelativePath), nil)
	if err != nil {
		return WorkspaceArchivePlan{}, err
	}
	defer source.Close()
	destination, err := openHeldDirectoryChain(s.Roots.StorageRoot, path.Dir(payload.RelativePath), nil)
	if err != nil {
		return WorkspaceArchivePlan{}, err
	}
	defer destination.Close()
	meta, err := openHeldDirectoryChain(s.Roots.StorageRoot, path.Dir(metadata.RelativePath), nil)
	if err != nil {
		return WorkspaceArchivePlan{}, err
	}
	defer meta.Close()
	for _, item := range []struct {
		fd   int
		name string
	}{{destination.ParentFD(), in.Slug}, {meta.ParentFD(), in.OperationID}} {
		present, e := namePresentNoFollow(item.fd, item.name)
		if e != nil || present {
			return WorkspaceArchivePlan{}, fmt.Errorf("archive destination must be absent: %v", e)
		}
	}
	inv, err := inventoryRetainedSchema(ctx, s.Roots, paths.Active, NoFollowInventorySchemaVersion)
	if err != nil {
		return WorkspaceArchivePlan{}, err
	}
	sourceParent, _ := identityForFD(source.ParentFD())
	destinationParent, _ := identityForFD(destination.ParentFD())
	metaParent, _ := identityForFD(meta.ParentFD())
	plan := WorkspaceArchivePlan{BoxID: s.Roots.BoxID, SchemaVersion: WorkspaceArchivePlanSchemaVersionV2, EvidenceKind: PhysicalWorkspaceMoveEvidence,
		OperationID: in.OperationID, OperationKind: WorkspaceOperationArchive, Kind: in.Kind, ObjectID: strings.TrimSpace(in.ObjectID), Slug: in.Slug,
		Source:          WorkspacePathBinding{Path: paths.Active, Identity: inv.RootIdentity, ParentIdentity: sourceParent},
		Destination:     WorkspacePathBinding{Path: payload, Identity: PathIdentity{Presence: PathAbsent}, ParentIdentity: destinationParent},
		SourceAncestors: source.evidence, DestinationAncestors: destination.evidence, Inventory: inv,
		Placement: &WorkspaceArchivePlacement{Metadata: WorkspacePathBinding{Path: metadata, Identity: PathIdentity{Presence: PathAbsent}, ParentIdentity: metaParent}, MetadataAncestors: meta.evidence},
		ActorID:   strings.TrimSpace(in.ActorID), Reason: strings.TrimSpace(in.Reason), PlannedAt: normalizedPlannedAt(in.PlannedAt, s.now())}
	if location != nil {
		plan.SchemaVersion = WorkspaceArchivePlanSchemaVersionV3
		plan.ActiveLocation = location
	}
	plan.CatalogRebinds, err = s.planPlacementCatalog(ctx, plan.Source.Path, plan.Destination.Path)
	if err != nil {
		return WorkspaceArchivePlan{}, err
	}
	plan.CatalogObservations, err = s.planCatalogObservations(ctx, plan.Source.Path, plan.Destination.Path)
	if err != nil {
		return WorkspaceArchivePlan{}, err
	}
	current, err := s.ResolveActiveProjectLocation(ctx, strings.TrimSpace(in.ObjectID))
	if err != nil {
		return WorkspaceArchivePlan{}, err
	}
	if !sameWorkspaceActiveLocation(location, current) {
		return WorkspaceArchivePlan{}, fmt.Errorf("accepted project location changed while planning archive")
	}
	if err = SealWorkspaceArchivePlan(&plan); err == nil {
		err = s.validatePlacementExecution(plan)
	}
	return plan, err
}

func (s WorkspaceMoveService) validatePlacementExecution(plan WorkspaceArchivePlan) error {
	if err := ValidateWorkspaceArchivePlan(plan, s.Roots); err != nil {
		return err
	}
	for _, item := range []struct {
		binding   WorkspacePathBinding
		ancestors []WorkspaceAncestorBinding
	}{{plan.Source, plan.SourceAncestors}, {plan.Destination, plan.DestinationAncestors}, {plan.Placement.Metadata, plan.Placement.MetadataAncestors}} {
		if len(item.ancestors) == 0 || item.ancestors[0].RelativePath != "." || item.ancestors[len(item.ancestors)-1].RelativePath != path.Dir(item.binding.Path.RelativePath) || !SamePathIdentity(item.binding.ParentIdentity, item.ancestors[len(item.ancestors)-1].Identity) {
			return fmt.Errorf("placement lacks exact independent parent evidence")
		}
	}
	for _, r := range plan.CatalogRebinds {
		for _, pair := range [][2]string{{r.ExpectedURI, r.NewURI}, {r.ExpectedOriginalPath, r.NewOriginalPath}, {r.ExpectedCurrentViewPath, r.NewCurrentViewPath}} {
			if pair[0] == pair[1] {
				continue
			}
			want, changed, err := rebaseCatalogPath(pair[0], plan.Source.Path, plan.Destination.Path)
			if err != nil || !changed || want != pair[1] {
				return fmt.Errorf("catalog rebind does not follow reviewed placement")
			}
		}
	}
	return nil
}

func (s WorkspaceMoveService) planCatalogObservations(ctx context.Context, source, destination WorkspacePathRef) ([]WorkspaceCatalogObservation, error) {
	details, err := s.Catalog.ListWorkspaceArchiveCatalogEntries(ctx, source.AbsolutePath, source.RelativePath)
	if err != nil {
		return nil, err
	}
	var result []WorkspaceCatalogObservation
	for _, detail := range details {
		original := detail.Entry.OriginalSourcePath
		view, _, err := rebaseCatalogPath(detail.Entry.CurrentViewPath, source, destination)
		if err != nil {
			return nil, err
		}
		if view == detail.Entry.CurrentViewPath && (withinRetained(original, source.AbsolutePath) || withinRetained(original, source.RelativePath) || withinRetained(view, source.RelativePath) || withinRetained(view, source.AbsolutePath)) {
			result = append(result, WorkspaceCatalogObservation{detail.Entry.StorageEntryID, original, view})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].StorageEntryID < result[j].StorageEntryID })
	return result, nil
}

func (s WorkspaceMoveService) comparePlacementArchive(ctx context.Context, plan WorkspaceArchivePlan) ([]WorkspaceArchiveFinding, error) {
	if plan.ActiveLocation != nil {
		current, err := s.ResolveActiveProjectLocation(ctx, plan.ObjectID)
		if err != nil {
			return nil, err
		}
		if !sameWorkspaceActiveLocation(plan.ActiveLocation, current) {
			return nil, fmt.Errorf("accepted project location changed before archive")
		}
	}
	for _, item := range []struct {
		root      string
		binding   WorkspacePathBinding
		ancestors []WorkspaceAncestorBinding
	}{{s.Roots.BoxRoot, plan.Source, plan.SourceAncestors}, {s.Roots.StorageRoot, plan.Destination, plan.DestinationAncestors}, {s.Roots.StorageRoot, plan.Placement.Metadata, plan.Placement.MetadataAncestors}} {
		chain, err := openHeldDirectoryChain(item.root, path.Dir(item.binding.Path.RelativePath), item.ancestors)
		if err != nil {
			return nil, err
		}
		if item.binding.Path == plan.Destination.Path {
			present, e := namePresentNoFollow(chain.ParentFD(), path.Base(item.binding.Path.RelativePath))
			if e != nil || present {
				chain.Close()
				return nil, fmt.Errorf("archive payload destination appeared: %v", e)
			}
		}
		if item.binding.Path == plan.Placement.Metadata.Path {
			fd, e := unix.Openat(chain.ParentFD(), path.Base(item.binding.Path.RelativePath), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if e == nil {
				e = s.verifyIntentMarker(ctx, fd, plan)
				if e == nil {
					e = verifyContainerEntries(fd, map[string]bool{workspaceIntentMarkerName: true})
				}
				_ = unix.Close(fd)
			} else if e == unix.ENOENT {
				e = nil
			}
			if e != nil {
				chain.Close()
				return nil, e
			}
		}
		chain.Close()
	}
	inv, err := inventoryRetainedSchema(ctx, s.Roots, plan.Source.Path, NoFollowInventorySchemaVersion)
	if err != nil {
		return nil, err
	}
	catalog, err := s.planPlacementCatalog(ctx, plan.Source.Path, plan.Destination.Path)
	if err != nil {
		return nil, err
	}
	observations, err := s.planCatalogObservations(ctx, plan.Source.Path, plan.Destination.Path)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(inv, plan.Inventory) || !reflect.DeepEqual(catalog, plan.CatalogRebinds) || !reflect.DeepEqual(observations, plan.CatalogObservations) {
		return []WorkspaceArchiveFinding{newWorkspaceFinding(plan.OperationID, FindingSourceDrift, PhaseArchivePlanned, "reviewed payload or catalog changed", true)}, nil
	}
	return nil, nil
}

func (s WorkspaceMoveService) moveNamedPayload(ctx context.Context, plan WorkspaceArchivePlan) error {
	if findings, err := s.comparePlacementArchive(ctx, plan); err != nil || len(findings) != 0 {
		return fmt.Errorf("archive placement changed before rename: %v", err)
	}
	source, err := openHeldDirectoryChain(s.Roots.BoxRoot, path.Dir(plan.Source.Path.RelativePath), plan.SourceAncestors)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := openHeldDirectoryChain(s.Roots.StorageRoot, path.Dir(plan.Destination.Path.RelativePath), plan.DestinationAncestors)
	if err != nil {
		return err
	}
	defer destination.Close()
	meta, err := openHeldDirectoryChain(s.Roots.StorageRoot, path.Dir(plan.Placement.Metadata.Path.RelativePath), plan.Placement.MetadataAncestors)
	if err != nil {
		return err
	}
	defer meta.Close()
	fd, err := s.ensureIntentContainer(ctx, meta, plan)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if err = s.fail(BoundaryAfterCatalogRecheck); err != nil {
		return err
	}
	for _, chain := range []*heldDirectoryChain{source, destination, meta} {
		if err = chain.verifyNamed(); err != nil {
			return err
		}
	}
	if err = verifyNamedDirectoryIdentity(source.ParentFD(), path.Base(plan.Source.Path.RelativePath), plan.Source.Identity); err != nil {
		return err
	}
	if err = verifyWorkspaceRenameMounts(source.ParentFD(), path.Base(plan.Source.Path.RelativePath), destination.ParentFD()); err != nil {
		return err
	}
	if err = renameNoReplaceAt(source.ParentFD(), path.Base(plan.Source.Path.RelativePath), destination.ParentFD(), path.Base(plan.Destination.Path.RelativePath)); err != nil {
		return err
	}
	if err = unix.Fsync(source.ParentFD()); err != nil {
		return err
	}
	if err = unix.Fsync(destination.ParentFD()); err != nil {
		return err
	}
	return verifyNamedDirectoryIdentity(destination.ParentFD(), path.Base(plan.Destination.Path.RelativePath), plan.Source.Identity)
}

func (s WorkspaceMoveService) inspectPlacementArchive(ctx context.Context, plan WorkspaceArchivePlan) (WorkspaceCustodyState, *WorkspaceArchiveManifest, *WorkspaceArchiveFinding, error) {
	source, err := openHeldDirectoryChain(s.Roots.BoxRoot, path.Dir(plan.Source.Path.RelativePath), plan.SourceAncestors)
	if err != nil {
		return CustodyAmbiguous, nil, nil, err
	}
	defer source.Close()
	destination, err := openHeldDirectoryChain(s.Roots.StorageRoot, path.Dir(plan.Destination.Path.RelativePath), plan.DestinationAncestors)
	if err != nil {
		return CustodyAmbiguous, nil, nil, err
	}
	defer destination.Close()
	meta, err := openHeldDirectoryChain(s.Roots.StorageRoot, path.Dir(plan.Placement.Metadata.Path.RelativePath), plan.Placement.MetadataAncestors)
	if err != nil {
		return CustodyAmbiguous, nil, nil, err
	}
	defer meta.Close()
	active, activeMatches, err := inspectNamedDirectory(source.ParentFD(), path.Base(plan.Source.Path.RelativePath), plan.Source.Identity)
	if err != nil {
		return CustodyAmbiguous, nil, nil, err
	}
	archived, archivedMatches, err := inspectNamedDirectory(destination.ParentFD(), path.Base(plan.Destination.Path.RelativePath), plan.Source.Identity)
	if err != nil {
		return CustodyAmbiguous, nil, nil, err
	}
	if active == archived || (active && !activeMatches) || (archived && !archivedMatches) {
		return CustodyAmbiguous, nil, nil, fmt.Errorf("workspace custody is not unique or reviewed identity changed")
	}
	fd, err := unix.Openat(meta.ParentFD(), path.Base(plan.Placement.Metadata.Path.RelativePath), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == unix.ENOENT && active {
		return CustodyActive, nil, nil, nil
	}
	if err != nil {
		return CustodyAmbiguous, nil, nil, err
	}
	defer unix.Close(fd)
	if err = verifyContainerEntries(fd, map[string]bool{workspaceIntentMarkerName: true, "archive.json": true, ".archive.json." + plan.OperationID + ".tmp": true}); err != nil {
		return CustodyAmbiguous, nil, nil, err
	}
	manifest, present, err := s.readAndVerifyManifestAt(ctx, fd, plan)
	if err != nil {
		return CustodyAmbiguous, nil, nil, err
	}
	if !present {
		if err = s.verifyIntentMarker(ctx, fd, plan); err != nil {
			return CustodyAmbiguous, nil, nil, err
		}
	}
	if active {
		if present {
			return CustodyAmbiguous, nil, nil, fmt.Errorf("archive manifest precedes payload move")
		}
		return CustodyActive, nil, nil, nil
	}
	inv, err := inventoryRetainedSchema(ctx, s.Roots, plan.Destination.Path, NoFollowInventorySchemaVersion)
	if err != nil {
		return CustodyAmbiguous, manifest, nil, fmt.Errorf("archived inventory differs from review: %v", err)
	}
	if !reflect.DeepEqual(inv, plan.Inventory) {
		return CustodyAmbiguous, manifest, nil, fmt.Errorf("archived inventory differs from review: %s", workspaceInventoryDifference(plan.Inventory, inv))
	}
	if raw, exists, e := readFileAtNoFollow(fd, ".archive.json."+plan.OperationID+".tmp", workspaceManifestMaxBytes); e != nil {
		return CustodyAmbiguous, nil, nil, e
	} else if exists {
		var staged WorkspaceArchiveManifest
		if e = json.Unmarshal(raw, &staged); e != nil {
			return CustodyAmbiguous, nil, nil, e
		}
		if e = s.verifyManifest(ctx, staged, plan); e != nil || (present && !sameWorkspaceManifest(staged, *manifest)) {
			return CustodyAmbiguous, nil, nil, fmt.Errorf("archive temporary manifest does not match reviewed operation")
		}
	}
	return CustodyArchived, manifest, nil, nil
}

// The HTTP boundary keeps causes private. Give operators bounded metadata
// differences, never file contents or symlink targets, without relaxing custody.
func workspaceInventoryDifference(want, got NoFollowInventory) string {
	changes := []string{}
	if want.RootIdentity != got.RootIdentity {
		changes = append(changes, fmt.Sprintf("root metadata mode %o -> %o", want.RootIdentity.Mode, got.RootIdentity.Mode))
	}
	if len(want.Entries) != len(got.Entries) {
		return fmt.Sprintf("entry count %d -> %d", len(want.Entries), len(got.Entries))
	}
	for i, expected := range want.Entries {
		actual := got.Entries[i]
		if expected != actual {
			changes = append(changes, fmt.Sprintf("entry %q mode %o -> %o identity_changed=%t payload_changed=%t mtime_changed=%t", expected.RelativePath, expected.Mode, actual.Mode,
				expected.RelativePath != actual.RelativePath || expected.Kind != actual.Kind || expected.DeviceID != actual.DeviceID || expected.Inode != actual.Inode,
				expected.SizeBytes != actual.SizeBytes || expected.ContentDigest != actual.ContentDigest || expected.TargetDigest != actual.TargetDigest || expected.SymlinkTarget != actual.SymlinkTarget,
				expected.ModifiedUnixNS != actual.ModifiedUnixNS))
			if len(changes) >= 8 {
				break
			}
		}
	}
	if len(changes) == 0 {
		return "inventory schema, digest or census changed"
	}
	return strings.Join(changes, "; ")
}
