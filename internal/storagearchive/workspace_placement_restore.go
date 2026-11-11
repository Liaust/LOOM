package storagearchive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"reflect"
	"strings"

	"golang.org/x/sys/unix"
)

func restoreReceiptName(plan WorkspaceArchivePlan) string {
	return "restore-" + plan.OperationID + ".json"
}

func (s WorkspaceMoveService) placementRestoreHistory(ctx context.Context, plan WorkspaceArchivePlan) (*WorkspaceArchiveManifest, error) {
	active, err := openHeldDirectoryChain(s.Roots.BoxRoot, path.Dir(plan.Destination.Path.RelativePath), plan.DestinationAncestors)
	if err != nil {
		return nil, err
	}
	defer active.Close()
	present, err := namePresentNoFollow(active.ParentFD(), path.Base(plan.Destination.Path.RelativePath))
	if err != nil || present {
		return nil, err
	}
	g, err := s.openPlacementRestoreGuard(plan)
	if err != nil {
		return nil, err
	}
	defer g.Close()
	archivePlan, archiveOperation, archived, err := s.loadCompletedArchiveEvidence(ctx, plan.ArchiveOperationID)
	if err != nil {
		return nil, err
	}
	record, _, err := s.loadOperationPlan(ctx, plan.OperationID)
	if err != nil {
		return nil, err
	}
	operation, err := operationFromJournal(record, plan)
	if err != nil || operation.Phase != PhaseRestoreComplete {
		return nil, fmt.Errorf("restore history is not complete")
	}
	manifest, err := s.buildRestoredManifest(ctx, archived, archiveOperation, operation, plan)
	if err != nil {
		return nil, err
	}
	original, present, err := readFileAtNoFollow(g.containerFD(), "archive.json", workspaceManifestMaxBytes)
	if err != nil || !present || s.verifyCanonicalManifestPayload(ctx, archivePlan, archived, original) != nil {
		return nil, fmt.Errorf("original archive history missing or changed")
	}
	raw, present, err := readFileAtNoFollow(g.containerFD(), restoreReceiptName(plan), workspaceManifestMaxBytes)
	want, _ := json.Marshal(manifest)
	if err != nil || !present || !bytes.Equal(raw, want) {
		return nil, fmt.Errorf("completed restore history receipt missing or changed")
	}
	if err = validateDurableWorkspaceRestoreProjection(record, archiveOperation, operation, manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func (s WorkspaceMoveService) planPlacementRestore(ctx context.Context, in WorkspaceRestorePlanInput, archivedPlan WorkspaceArchivePlan, operation WorkspaceArchiveOperation, manifest WorkspaceArchiveManifest, clean bool) (WorkspaceArchivePlan, error) {
	paths, err := ResolveWorkspacePlanPaths(s.Roots, archivedPlan)
	if err != nil {
		return WorkspaceArchivePlan{}, err
	}
	payload := paths.ArchivePayload
	revision := ""
	if s.CurrentLocation != nil {
		current, rev, e := s.CurrentLocation(ctx, "workspace:"+archivedPlan.OperationID, payload.AbsolutePath)
		if e != nil {
			return WorkspaceArchivePlan{}, e
		}
		rel, e := filepath.Rel(s.Roots.StorageRoot, current)
		if e != nil {
			return WorkspaceArchivePlan{}, e
		}
		payload, e = retainedRef(s.Roots, filepath.ToSlash(rel))
		if e != nil {
			return WorkspaceArchivePlan{}, e
		}
		revision = rev
	}
	source, err := openHeldDirectoryChain(s.Roots.StorageRoot, path.Dir(payload.RelativePath), nil)
	if err != nil {
		return WorkspaceArchivePlan{}, err
	}
	defer source.Close()
	destination, err := openHeldDirectoryChain(s.Roots.BoxRoot, path.Dir(paths.Active.RelativePath), nil)
	if err != nil {
		return WorkspaceArchivePlan{}, err
	}
	defer destination.Close()
	meta, err := openHeldDirectoryChain(s.Roots.StorageRoot, paths.ArchiveContainer.RelativePath, nil)
	if err != nil {
		return WorkspaceArchivePlan{}, err
	}
	defer meta.Close()
	allowed := map[string]bool{"archive.json": true}
	if archivedPlan.Placement == nil && payload.AbsolutePath == paths.ArchivePayload.AbsolutePath {
		allowed[path.Base(paths.ArchivePayload.RelativePath)] = true
	}
	if !clean {
		allowed[workspaceRestoreIntentMarkerName] = true
	}
	if err = verifyContainerEntries(meta.ParentFD(), allowed); err != nil {
		return WorkspaceArchivePlan{}, err
	}
	raw, present, err := readFileAtNoFollow(meta.ParentFD(), "archive.json", workspaceManifestMaxBytes)
	if err != nil || !present {
		return WorkspaceArchivePlan{}, fmt.Errorf("archive manifest missing: %v", err)
	}
	if err = s.verifyCanonicalManifestPayload(ctx, archivedPlan, manifest, raw); err != nil {
		return WorkspaceArchivePlan{}, err
	}
	inv, err := inventoryRetainedSchema(ctx, s.Roots, payload, NoFollowInventorySchemaVersion)
	if err != nil {
		return WorkspaceArchivePlan{}, err
	}
	if !reflect.DeepEqual(inv, archivedPlan.Inventory) || !SamePathIdentity(inv.RootIdentity, operation.Source.Identity) {
		return WorkspaceArchivePlan{}, fmt.Errorf("archived payload no longer matches completed archive")
	}
	if exists, e := namePresentNoFollow(destination.ParentFD(), path.Base(paths.Active.RelativePath)); e != nil || exists {
		return WorkspaceArchivePlan{}, fmt.Errorf("restore destination must be absent: %v", e)
	}
	srcParent, _ := identityForFD(source.ParentFD())
	dstParent, _ := identityForFD(destination.ParentFD())
	metaID, _ := identityForFD(meta.ParentFD())
	metaAncestors := append([]WorkspaceAncestorBinding(nil), meta.evidence[:len(meta.evidence)-1]...)
	metaParent := metaAncestors[len(metaAncestors)-1].Identity
	plan := WorkspaceArchivePlan{BoxID: archivedPlan.BoxID, SchemaVersion: WorkspaceArchivePlanSchemaVersionV2, EvidenceKind: PhysicalWorkspaceMoveEvidence,
		OperationID: in.OperationID, OperationKind: WorkspaceOperationRestore, ArchiveOperationID: archivedPlan.OperationID, ArchiveManifestDigest: sha256Digest(raw),
		Kind: archivedPlan.Kind, ObjectID: archivedPlan.ObjectID, Slug: archivedPlan.Slug,
		Source: WorkspacePathBinding{Path: payload, Identity: inv.RootIdentity, ParentIdentity: srcParent}, Destination: WorkspacePathBinding{Path: paths.Active, Identity: PathIdentity{Presence: PathAbsent}, ParentIdentity: dstParent},
		SourceAncestors: source.evidence, DestinationAncestors: destination.evidence, Inventory: inv,
		Placement: &WorkspaceArchivePlacement{Metadata: WorkspacePathBinding{Path: paths.ArchiveContainer, Identity: metaID, ParentIdentity: metaParent}, MetadataAncestors: metaAncestors, RelocationID: revision},
		ActorID:   strings.TrimSpace(in.ActorID), Reason: strings.TrimSpace(in.Reason), PlannedAt: normalizedPlannedAt(in.PlannedAt, s.now())}
	if archivedPlan.SchemaVersion == WorkspaceArchivePlanSchemaVersionV3 {
		plan.SchemaVersion = WorkspaceArchivePlanSchemaVersionV3
		plan.ActiveLocation = cloneWorkspaceActiveLocation(archivedPlan.ActiveLocation)
	}
	plan.CatalogRebinds, err = s.planPlacementCatalog(ctx, plan.Source.Path, plan.Destination.Path)
	if err != nil {
		return WorkspaceArchivePlan{}, err
	}
	plan.CatalogObservations, err = s.planCatalogObservations(ctx, plan.Source.Path, plan.Destination.Path)
	if err != nil {
		return WorkspaceArchivePlan{}, err
	}
	if err = SealWorkspaceArchivePlan(&plan); err == nil {
		err = s.validatePlacementExecution(plan)
	}
	return plan, err
}

func (s WorkspaceMoveService) openPlacementRestoreGuard(plan WorkspaceArchivePlan) (*workspaceRestoreGuard, error) {
	source, err := openHeldDirectoryChain(s.Roots.StorageRoot, path.Dir(plan.Source.Path.RelativePath), plan.SourceAncestors)
	if err != nil {
		return nil, err
	}
	g := &workspaceRestoreGuard{sourceChain: source}
	g.destinationChain, err = openHeldDirectoryChain(s.Roots.BoxRoot, path.Dir(plan.Destination.Path.RelativePath), plan.DestinationAncestors)
	if err != nil {
		g.Close()
		return nil, err
	}
	ancestors := append(append([]WorkspaceAncestorBinding(nil), plan.Placement.MetadataAncestors...), WorkspaceAncestorBinding{RelativePath: plan.Placement.Metadata.Path.RelativePath, Identity: plan.Placement.Metadata.Identity})
	g.metadataChain, err = openHeldDirectoryChain(s.Roots.StorageRoot, plan.Placement.Metadata.Path.RelativePath, ancestors)
	if err != nil {
		g.Close()
		return nil, err
	}
	return g, nil
}

func (s WorkspaceMoveService) acquirePlacementRestoreGuard(ctx context.Context, plan, archivedPlan WorkspaceArchivePlan, manifest WorkspaceArchiveManifest, existing bool) (*workspaceRestoreGuard, error) {
	g, err := s.openPlacementRestoreGuard(plan)
	if err != nil {
		return nil, err
	}
	if !existing {
		marker, e := s.sealRestoreIntentMarker(ctx, plan)
		if e != nil {
			g.Close()
			return nil, e
		}
		raw, e := json.Marshal(marker)
		if e != nil {
			g.Close()
			return nil, e
		}
		err = writeExclusiveFileAt(g.containerFD(), workspaceRestoreIntentMarkerName, raw, 0600)
		if err != nil && !errors.Is(err, unix.EEXIST) {
			g.Close()
			return nil, err
		}
	}
	held, present, err := openBoundedFileAtNoFollow(g.containerFD(), workspaceRestoreIntentMarkerName, workspaceRestoreIntentMaxBytes)
	if err != nil || !present {
		g.Close()
		return nil, fmt.Errorf("restore intent missing: %v", err)
	}
	g.marker = held
	if err = s.lockRestoreClaim(ctx, held.file); err == nil {
		err = s.verifyRestoreIntentMarkerPayload(ctx, held.payload, plan)
	}
	if err == nil {
		err = g.verify()
	}
	if err == nil && !existing {
		raw, present, e := readFileAtNoFollow(g.containerFD(), "archive.json", workspaceManifestMaxBytes)
		if e != nil || !present || s.verifyCanonicalManifestPayload(ctx, archivedPlan, manifest, raw) != nil || sha256Digest(raw) != plan.ArchiveManifestDigest {
			err = fmt.Errorf("archive evidence changed before restore intent")
		}
	}
	if err != nil {
		g.Close()
		return nil, err
	}
	return g, nil
}

func (s WorkspaceMoveService) inspectPlacementRestore(ctx context.Context, plan, archivedPlan WorkspaceArchivePlan, archiveOperation WorkspaceArchiveOperation, archived WorkspaceArchiveManifest, restoreOperation WorkspaceArchiveOperation) (WorkspaceCustodyState, *WorkspaceArchiveManifest, *WorkspaceArchiveFinding, error) {
	g, err := s.openPlacementRestoreGuard(plan)
	if err != nil {
		return CustodyAmbiguous, nil, nil, err
	}
	defer g.Close()
	allowed := map[string]bool{"archive.json": true, workspaceRestoreIntentMarkerName: true, restoreReceiptName(plan): true, ".restore.json." + plan.OperationID + ".tmp": true}
	if archivedPlan.Placement == nil && path.Dir(plan.Source.Path.RelativePath) == plan.Placement.Metadata.Path.RelativePath {
		allowed[path.Base(plan.Source.Path.RelativePath)] = true
	}
	if err = verifyContainerEntries(g.containerFD(), allowed); err != nil {
		return CustodyAmbiguous, nil, nil, err
	}
	raw, present, err := readFileAtNoFollow(g.containerFD(), "archive.json", workspaceManifestMaxBytes)
	if err != nil || !present || s.verifyCanonicalManifestPayload(ctx, archivedPlan, archived, raw) != nil || sha256Digest(raw) != plan.ArchiveManifestDigest {
		return CustodyAmbiguous, nil, nil, fmt.Errorf("original archive evidence changed")
	}
	manifest := archived
	if receipt, exists, e := readFileAtNoFollow(g.containerFD(), restoreReceiptName(plan), workspaceManifestMaxBytes); e != nil {
		return CustodyAmbiguous, nil, nil, e
	} else if exists {
		canonical, e := s.buildRestoredManifest(ctx, archived, archiveOperation, restoreOperation, plan)
		if e != nil {
			return CustodyAmbiguous, nil, nil, e
		}
		want, _ := json.Marshal(canonical)
		if !bytes.Equal(receipt, want) {
			return CustodyAmbiguous, nil, nil, fmt.Errorf("restore receipt differs from completed operation")
		}
		manifest = canonical
	}
	src, srcMatches, err := inspectNamedDirectory(g.sourceChain.ParentFD(), path.Base(plan.Source.Path.RelativePath), plan.Source.Identity)
	if err != nil {
		return CustodyAmbiguous, nil, nil, err
	}
	dst, dstMatches, err := inspectNamedDirectory(g.destinationChain.ParentFD(), path.Base(plan.Destination.Path.RelativePath), plan.Source.Identity)
	if err != nil {
		return CustodyAmbiguous, nil, nil, err
	}
	if src == dst || (src && !srcMatches) || (dst && !dstMatches) {
		return CustodyAmbiguous, &manifest, nil, fmt.Errorf("restore custody is ambiguous or substituted")
	}
	ref := plan.Source.Path
	if dst {
		ref = plan.Destination.Path
	}
	inv, err := inventoryRetainedSchema(ctx, s.Roots, ref, NoFollowInventorySchemaVersion)
	if err != nil || !reflect.DeepEqual(inv, plan.Inventory) {
		return CustodyAmbiguous, &manifest, nil, fmt.Errorf("restore payload differs from review: %v", err)
	}
	if src {
		return CustodyArchived, &manifest, nil, nil
	}
	return CustodyActive, &manifest, nil, nil
}

func (s WorkspaceMoveService) publishPlacementRestore(ctx context.Context, plan, archivedPlan WorkspaceArchivePlan, archived, restored WorkspaceArchiveManifest, g *workspaceRestoreGuard) error {
	if err := g.verify(); err != nil {
		return err
	}
	original, present, err := openBoundedFileAtNoFollow(g.containerFD(), "archive.json", workspaceManifestMaxBytes)
	if err != nil || !present {
		return fmt.Errorf("original archive manifest missing")
	}
	defer original.Close()
	if err = s.verifyCanonicalManifestPayload(ctx, archivedPlan, archived, original.payload); err != nil || sha256Digest(original.payload) != plan.ArchiveManifestDigest {
		return fmt.Errorf("original archive manifest changed")
	}
	raw, err := json.Marshal(restored)
	if err != nil {
		return err
	}
	name := restoreReceiptName(plan)
	if current, exists, e := readFileAtNoFollow(g.containerFD(), name, workspaceManifestMaxBytes); e != nil {
		return e
	} else if exists {
		if bytes.Equal(current, raw) {
			return nil
		}
		return fmt.Errorf("restore receipt replay conflict")
	}
	tempName := ".restore.json." + plan.OperationID + ".tmp"
	if err = writeExclusiveFileAt(g.containerFD(), tempName, raw, 0600); err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
	if err = s.fail(BoundaryAfterRestoredManifestTemp); err != nil {
		return err
	}
	temp, present, err := openBoundedFileAtNoFollow(g.containerFD(), tempName, workspaceManifestMaxBytes)
	if err != nil || !present {
		return fmt.Errorf("restore receipt temporary file missing")
	}
	defer temp.Close()
	if !bytes.Equal(temp.payload, raw) {
		return fmt.Errorf("restore receipt temporary replay conflict")
	}
	if err = s.fail(BoundaryBeforeRestoredManifestCommit); err != nil {
		return err
	}
	if err = g.verify(); err != nil {
		return err
	}
	if err = verifyNamedHeldFile(g.containerFD(), "archive.json", original); err != nil {
		return err
	}
	if err = verifyNamedHeldFile(g.containerFD(), tempName, temp); err != nil {
		return err
	}
	if err = renameNoReplaceAt(g.containerFD(), tempName, g.containerFD(), name); err != nil {
		return err
	}
	return unix.Fsync(g.containerFD())
}
