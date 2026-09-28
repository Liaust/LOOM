package storagearchive

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func restoredCycleFixture(t *testing.T) (*workspaceMoveFixture, WorkspaceArchivePlan, WorkspaceArchivePlan) {
	t.Helper()
	f, archive, _ := archivedRestoreFixture(t)
	restore := planRestore(t, f, archive.OperationID)
	if _, err := f.service.ApplyRestore(context.Background(), restore, restore.PlanDigest); err != nil {
		t.Fatal(err)
	}
	f.journal.record.ManifestJSON = append([]byte(nil), f.journal.restoreRecords[restore.OperationID].ManifestJSON...)
	f.journal.restoreRecords[archive.OperationID] = cloneMoveRecord(f.journal.record)
	// The fake's current archive slot is reused; completed records remain addressable.
	f.journal.exists = false
	detail := f.catalog.details[moveTestEntryID]
	detail.Entry.OriginalSourcePath = filepath.Join(f.paths.Active.AbsolutePath, "payload.txt")
	detail.Entry.CurrentViewPath = filepath.ToSlash(filepath.Join(f.paths.Active.RelativePath, "payload.txt"))
	detail.PhysicalRefs[0].URI = detail.Entry.OriginalSourcePath
	f.catalog.entries[0] = detail.Entry
	f.catalog.details[moveTestEntryID] = detail
	return f, archive, restore
}

func TestWorkspaceArchiveRepeatCyclePreservesHistoryAndRecovers(t *testing.T) {
	for _, crash := range []WorkspaceMoveBoundary{"", BoundaryAfterHistoryRetained, BoundaryAfterPayloadMove} {
		t.Run(string(crash), func(t *testing.T) {
			f, archive, restore := restoredCycleFixture(t)
			ctx := context.Background()
			prior, err := os.ReadFile(filepath.Join(f.paths.ArchiveContainer.AbsolutePath, "archive.json"))
			if err != nil {
				t.Fatal(err)
			}
			// A restored project can evolve before its next archive.
			if err := os.WriteFile(filepath.Join(f.paths.Active.AbsolutePath, "new.txt"), []byte("second cycle"), 0o640); err != nil {
				t.Fatal(err)
			}
			before := snapshotWorkspacePayload(t, f.paths.Active.AbsolutePath)
			input := f.planInput()
			input.OperationID = newWorkspaceID("workspace_archive_operation")
			plan, err := f.service.PlanArchive(ctx, input)
			if err != nil || plan.PreviousCycle == nil || plan.PreviousCycle.RestoreOperationID != restore.OperationID {
				t.Fatalf("plan: %v", err)
			}
			if crash != "" {
				f.service.FailureHook = func(b WorkspaceMoveBoundary) error {
					if b == crash {
						return errors.New("interrupted")
					}
					return nil
				}
				if _, err := f.service.ApplyArchive(ctx, plan, plan.PlanDigest); err == nil {
					t.Fatal("expected interruption")
				}
				f.service.FailureHook = nil
			}
			result, err := f.service.ApplyArchive(ctx, plan, plan.PlanDigest)
			if err != nil || result.Operation.Phase != PhaseArchiveComplete {
				t.Fatalf("second archive: %v", err)
			}
			history := filepath.Join(filepath.Dir(f.paths.ArchiveContainer.AbsolutePath), workspaceHistoryName(plan.Slug, restore.OperationID))
			retained, err := os.ReadFile(filepath.Join(history, "archive.json"))
			if err != nil || !bytes.Equal(prior, retained) {
				t.Fatal("prior authenticated bytes changed")
			}
			if _, err := os.Stat(f.paths.Active.AbsolutePath); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("source still exists")
			}
			if got := snapshotWorkspacePayload(t, f.paths.ArchivePayload.AbsolutePath); !reflect.DeepEqual(before, got) {
				t.Fatal("payload changed")
			}
			for _, id := range []string{archive.OperationID, restore.OperationID} {
				i, err := f.service.InspectOperation(ctx, id)
				if err != nil || i.Custody != CustodyHistorical {
					t.Fatalf("historical inspection %s: %v, %s", id, err, i.Custody)
				}
			}
			if _, err := f.service.ApplyArchive(ctx, plan, plan.PlanDigest); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWorkspaceArchiveRepeatCycleRejectsChangedHistory(t *testing.T) {
	for _, mutation := range []string{"payload", "tamper", "substitution", "foreign-owner", "incomplete-restore"} {
		t.Run(mutation, func(t *testing.T) {
			f, _, restore := restoredCycleFixture(t)
			input := f.planInput()
			input.OperationID = newWorkspaceID("workspace_archive_operation")
			plan, err := f.service.PlanArchive(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			container := f.paths.ArchiveContainer.AbsolutePath
			switch mutation {
			case "payload":
				if err := os.Mkdir(f.paths.ArchivePayload.AbsolutePath, 0o750); err != nil {
					t.Fatal(err)
				}
			case "tamper":
				if err := os.WriteFile(filepath.Join(container, "archive.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "substitution":
				if err := os.Rename(container, container+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(container, 0o750); err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(filepath.Join(container+".old", "archive.json"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(container, "archive.json"), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			case "foreign-owner":
				plan.ObjectID = "topic_someone_else"
				if err := SealWorkspaceArchivePlan(&plan); err != nil {
					t.Fatal(err)
				}
			case "incomplete-restore":
				r := f.journal.restoreRecords[restore.OperationID]
				r.Phase, r.TerminalStatus = string(PhaseRestoreProjectionsCommitted), string(OperationStatusRunning)
				f.journal.restoreRecords[restore.OperationID] = r
			}
			if _, err := f.service.ApplyArchive(context.Background(), plan, plan.PlanDigest); err == nil {
				t.Fatal("changed history accepted")
			}
			if _, err := os.Stat(f.paths.Active.AbsolutePath); err != nil {
				t.Fatal("active payload moved")
			}
		})
	}
}
