package storagearchive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"

	"golang.org/x/sys/unix"
)

const BoundaryAfterHistoryRetained WorkspaceMoveBoundary = "after_history_retained"
const CustodyHistorical WorkspaceCustodyState = "historical"

func workspaceHistoryName(slug, restoreID string) string {
	return "." + slug + "." + restoreID + ".history"
}

// Only a completed, authenticated restore with no remaining payload may release
// the canonical container name. Old manifests and journals remain unchanged.
func (s WorkspaceMoveService) readCompletedCycle(ctx context.Context, fd int, kind WorkspaceKind, objectID, slug string) (*WorkspaceArchivePreviousCycle, WorkspaceArchiveManifest, error) {
	var zero WorkspaceArchiveManifest
	if err := verifyContainerEntries(fd, map[string]bool{"archive.json": true}); err != nil {
		return nil, zero, err
	}
	raw, present, err := readFileAtNoFollow(fd, "archive.json", workspaceManifestMaxBytes)
	if err != nil || !present {
		return nil, zero, fmt.Errorf("retained archive manifest missing or unreadable: %w", err)
	}
	m, err := DecodePhysicalWorkspaceArchiveManifest(raw)
	if err != nil {
		return nil, zero, err
	}
	if m.LifecycleState != WorkspaceLifecycleActive || m.Kind != kind || m.ObjectID != objectID || m.Slug != slug || m.RestoreOperationID == "" {
		return nil, zero, fmt.Errorf("archive container is not the completed restore of this workspace")
	}
	r, rp, err := s.loadOperationPlan(ctx, m.RestoreOperationID)
	if err != nil {
		return nil, zero, err
	}
	ro, err := operationFromJournal(r, rp)
	if err != nil || ro.Phase != PhaseRestoreComplete || ro.Status != OperationStatusComplete {
		return nil, zero, fmt.Errorf("prior restore is not complete: %w", err)
	}
	_, ao, archived, err := s.loadCompletedArchiveEvidence(ctx, rp.ArchiveOperationID)
	if err != nil {
		return nil, zero, err
	}
	canonical, err := s.buildRestoredManifest(ctx, archived, ao, ro, rp)
	if err != nil {
		return nil, zero, err
	}
	want, err := json.Marshal(canonical)
	if err != nil || !bytes.Equal(raw, want) {
		return nil, zero, fmt.Errorf("retained manifest differs from completed restore")
	}
	if err := validateDurableWorkspaceRestoreProjection(r, ao, ro, canonical); err != nil {
		return nil, zero, err
	}
	id, err := identityForFD(fd)
	if err != nil {
		return nil, zero, err
	}
	return &WorkspaceArchivePreviousCycle{RestoreOperationID: m.RestoreOperationID, ManifestDigest: sha256Digest(raw), ContainerIdentity: id}, m, nil
}

func (s WorkspaceMoveService) previousArchiveCycle(ctx context.Context, plan WorkspaceArchivePlan) (*WorkspaceArchivePreviousCycle, error) {
	paths, err := ResolveWorkspacePaths(s.Roots, plan.Kind, plan.Slug)
	if err != nil {
		return nil, err
	}
	chain, err := openHeldDirectoryChain(s.Roots.StorageRoot, path.Dir(paths.Mapping.ArchiveContainerPath), plan.DestinationAncestors)
	if err != nil {
		return nil, err
	}
	defer chain.Close()
	fd, err := unix.Openat(chain.ParentFD(), plan.Slug, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	previous, manifest, err := s.readCompletedCycle(ctx, fd, plan.Kind, plan.ObjectID, plan.Slug)
	if err != nil {
		return nil, err
	}
	if !sameStableDirectoryIdentity(manifest.ArchiveSourceIdentity, plan.Source.Identity) {
		return nil, fmt.Errorf("active workspace is not the restored payload")
	}
	if present, err := namePresentNoFollow(chain.ParentFD(), workspaceHistoryName(plan.Slug, previous.RestoreOperationID)); err != nil || present {
		return nil, fmt.Errorf("prior cycle history destination is occupied: %w", err)
	}
	return previous, nil
}

func samePreviousCycle(a, b *WorkspaceArchivePreviousCycle) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.RestoreOperationID == b.RestoreOperationID && a.ManifestDigest == b.ManifestDigest && sameStableDirectoryIdentity(a.ContainerIdentity, b.ContainerIdentity)
}

func (s WorkspaceMoveService) retainPreviousArchiveCycle(ctx context.Context, plan WorkspaceArchivePlan) error {
	if plan.PreviousCycle == nil {
		return nil
	}
	paths, err := ResolveWorkspacePaths(s.Roots, plan.Kind, plan.Slug)
	if err != nil {
		return err
	}
	chain, err := openHeldDirectoryChain(s.Roots.StorageRoot, path.Dir(paths.Mapping.ArchiveContainerPath), plan.DestinationAncestors)
	if err != nil {
		return err
	}
	defer chain.Close()
	history := workspaceHistoryName(plan.Slug, plan.PreviousCycle.RestoreOperationID)
	// A prior attempt may have retained history before creating the new container.
	name := history
	fd, err := unix.Openat(chain.ParentFD(), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		name = plan.Slug
		fd, err = unix.Openat(chain.ParentFD(), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	}
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	previous, _, err := s.readCompletedCycle(ctx, fd, plan.Kind, plan.ObjectID, plan.Slug)
	if err != nil {
		return err
	}
	if !samePreviousCycle(previous, plan.PreviousCycle) {
		return fmt.Errorf("previous archive cycle changed after review")
	}
	if err := chain.verifyNamed(); err != nil {
		return err
	}
	if err := verifyNamedDirectoryIdentity(chain.ParentFD(), name, previous.ContainerIdentity); err != nil {
		return err
	}
	if name == history {
		return unix.Fsync(chain.ParentFD())
	}
	if err := renameNoReplaceAt(chain.ParentFD(), name, chain.ParentFD(), history); err != nil {
		return fmt.Errorf("retain previous archive history: %w", err)
	}
	if err := unix.Fsync(chain.ParentFD()); err != nil {
		return err
	}
	return s.fail(BoundaryAfterHistoryRetained)
}

func (s WorkspaceMoveService) retainedRestoreHistory(ctx context.Context, plan WorkspaceArchivePlan) (*WorkspaceArchiveManifest, error) {
	if err := s.validateExecutableRestorePlan(plan); err != nil {
		return nil, err
	}
	paths, err := ResolveWorkspacePaths(s.Roots, plan.Kind, plan.Slug)
	if err != nil {
		return nil, err
	}
	chain, err := openHeldDirectoryChain(s.Roots.StorageRoot, path.Dir(paths.Mapping.ArchiveContainerPath), plan.SourceAncestors[:len(plan.SourceAncestors)-1])
	if err != nil {
		return nil, err
	}
	defer chain.Close()
	fd, err := unix.Openat(chain.ParentFD(), workspaceHistoryName(plan.Slug, plan.OperationID), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	p, m, err := s.readCompletedCycle(ctx, fd, plan.Kind, plan.ObjectID, plan.Slug)
	if err != nil {
		return nil, err
	}
	if p.RestoreOperationID != plan.OperationID || !sameStableDirectoryIdentity(p.ContainerIdentity, plan.Source.ParentIdentity) {
		return nil, fmt.Errorf("retained history does not match restored container")
	}
	return &m, nil
}
