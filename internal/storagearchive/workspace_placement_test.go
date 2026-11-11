package storagearchive

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func placementFixture(t *testing.T) (*workspaceMoveFixture, WorkspaceArchivePlanInput) {
	t.Helper()
	f := newWorkspaceMoveFixture(t)
	paths, err := ResolveWorkspacePaths(f.roots, WorkspaceKindProject, "project-one")
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Dir(paths.Active.AbsolutePath), filepath.Dir(paths.ArchiveContainer.AbsolutePath)} {
		if err = os.MkdirAll(dir, 0750); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.Rename(f.paths.Active.AbsolutePath, paths.Active.AbsolutePath); err != nil {
		t.Fatal(err)
	}
	f.paths = paths
	f.catalog.entries = nil
	f.catalog.details = nil
	if err = PrepareRetainedRoots(f.roots.StorageRoot); err != nil {
		t.Fatal(err)
	}
	return f, WorkspaceArchivePlanInput{OperationID: moveTestOperationID, Kind: WorkspaceKindProject, ObjectID: "project_one", Slug: "project-one", ActorID: "actor_archive_test", Reason: "archive completed project"}
}

func TestWorkspacePlacementMixedRestoreAndCycle(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{true: "relocated-v1", false: "named-v2"}[legacy], func(t *testing.T) {
			ctx := context.Background()
			f, input := placementFixture(t)
			f.service.NamedPlacement = !legacy
			archive, err := f.service.PlanArchive(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.service.ApplyArchive(ctx, archive, archive.PlanDigest); err != nil {
				t.Fatal(err)
			}
			paths, err := ResolveWorkspacePlanPaths(f.roots, archive)
			if err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(paths.ArchiveManifest.AbsolutePath)
			if err != nil {
				t.Fatal(err)
			}
			if legacy {
				current := filepath.Join(f.roots.StorageRoot, "Projects", input.Slug)
				if err = os.Rename(paths.ArchivePayload.AbsolutePath, current); err != nil {
					t.Fatal(err)
				}
				f.service.CurrentLocation = func(_ context.Context, owner, old string) (string, string, error) {
					if owner == "workspace:"+archive.OperationID {
						return current, "retained_relocation_01ARZ3NDEKTSV4RRFFQ69G5FAY", nil
					}
					return old, "", nil
				}
			}
			f.service.NamedPlacement = true
			if _, err = f.service.InspectArchive(ctx, archive.OperationID); err != nil {
				t.Fatal(err)
			}
			restore, err := f.service.PlanRestore(ctx, WorkspaceRestorePlanInput{OperationID: restoreTestOperationID, ArchiveOperationID: archive.OperationID, ActorID: input.ActorID, Reason: "restore inactive"})
			if err != nil {
				t.Fatal(err)
			}
			f.service.FailureHook = func(b WorkspaceMoveBoundary) error {
				if b == BoundaryAfterRestoredManifestTemp {
					return errors.New("interrupted receipt publication")
				}
				return nil
			}
			if _, err = f.service.ApplyRestore(ctx, restore, restore.PlanDigest); err == nil {
				t.Fatal("expected interruption")
			}
			f.service.FailureHook = nil
			result, err := f.service.RecoverOperation(ctx, restore.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			if result.Operation.Phase != PhaseRestoreComplete || result.ActivationState != WorkspaceActivationInactive {
				t.Fatal("restore not completed inactive")
			}
			after, err := os.ReadFile(paths.ArchiveManifest.AbsolutePath)
			if err != nil || !bytes.Equal(original, after) {
				t.Fatal("original signed manifest changed")
			}
			// Keep the fake's historical archive slot addressable before its next cycle.
			f.journal.restoreRecords[archive.OperationID] = cloneMoveRecord(f.journal.record)
			f.journal.exists = false
			input.OperationID = newWorkspaceID("workspace_archive_operation")
			next, err := f.service.PlanArchive(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.service.ApplyArchive(ctx, next, next.PlanDigest); err != nil {
				t.Fatal(err)
			}
			if _, err = f.service.InspectOperation(ctx, restore.OperationID); err != nil {
				t.Fatal(err)
			}
		})
	}
}
