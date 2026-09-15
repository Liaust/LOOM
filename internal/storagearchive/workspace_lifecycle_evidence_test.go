package storagearchive

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"loom.local/loom/internal/storagecatalog"
)

func TestWorkspaceLifecycleEvidenceHistoricalReplay(t *testing.T) {
	fixture, archivePlan, before := archivedRestoreFixture(t)
	ctx := context.Background()
	archived, err := fixture.service.ReadLifecycleEvidence(ctx, archivePlan.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if archived.Event.ToState != WorkspaceLifecycleArchived || archived.Manifest.LifecycleState != WorkspaceLifecycleArchived || archived.ManifestDigest == "" {
		t.Fatal("wrong archive evidence")
	}
	for i := 0; i < 3; i++ {
		replay, err := fixture.service.ReadLifecycleEvidence(ctx, archivePlan.OperationID)
		if err != nil || !reflect.DeepEqual(replay, archived) {
			t.Fatal("archive replay changed", err)
		}
	}
	if !reflect.DeepEqual(snapshotWorkspacePayload(t, fixture.paths.ArchivePayload.AbsolutePath), before) || fixture.journal.eventCommits != 1 || fixture.journal.catalogCommits != 1 {
		t.Fatal("read mutated archived custody")
	}
	restorePlan := planRestore(t, fixture, archivePlan.OperationID)
	if _, err := fixture.service.ApplyRestore(ctx, restorePlan, restorePlan.PlanDigest); err != nil {
		t.Fatal(err)
	}
	// Mirror the actual catalogue's single current manifest for both operations.
	fixture.journal.record.ManifestJSON = append(json.RawMessage(nil), fixture.journal.restoreRecords[restorePlan.OperationID].ManifestJSON...)
	delayed, err := fixture.service.ReadLifecycleEvidence(ctx, archivePlan.OperationID)
	if err != nil || !reflect.DeepEqual(delayed, archived) {
		t.Fatal("delayed archive event changed after restore", err)
	}
	restored, err := fixture.service.ReadLifecycleEvidence(ctx, restorePlan.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Event.ToState != WorkspaceLifecycleActive || restored.Manifest.ArchiveOperationID != archivePlan.OperationID || restored.Manifest.RestoreOperationID != restorePlan.OperationID || restored.Plan.ArchiveManifestDigest != archived.ManifestDigest {
		t.Fatal("restore chain differs")
	}
	if !reflect.DeepEqual(snapshotWorkspacePayload(t, fixture.paths.Active.AbsolutePath), before) || fixture.journal.restoreEventCommits != 1 || fixture.journal.restoreCatalogCommits != 1 {
		t.Fatal("historical read mutated restored custody")
	}
}

type lifecycleEvidenceJournal struct {
	WorkspaceMoveJournal
	rewrite func(string, *storagecatalog.WorkspaceArchiveJournalRecord)
}

func (j lifecycleEvidenceJournal) LoadWorkspaceArchiveJournal(ctx context.Context, id string) (storagecatalog.WorkspaceArchiveJournalRecord, bool, error) {
	r, found, err := j.WorkspaceMoveJournal.LoadWorkspaceArchiveJournal(ctx, id)
	if found && err == nil {
		j.rewrite(id, &r)
	}
	return r, found, err
}

func TestWorkspaceLifecycleEvidenceRejectsTamperAndPartial(t *testing.T) {
	for name, rewrite := range map[string]func(*storagecatalog.WorkspaceArchiveJournalRecord){
		"identity": func(r *storagecatalog.WorkspaceArchiveJournalRecord) { r.OperationID = restoreTestOperationID },
		"phase": func(r *storagecatalog.WorkspaceArchiveJournalRecord) {
			r.Phase = string(PhaseArchiveProjectionsCommitted)
			r.CompletedAt = nil
		},
		"missing_event": func(r *storagecatalog.WorkspaceArchiveJournalRecord) { r.EventJSON = nil },
		"oversized_event": func(r *storagecatalog.WorkspaceArchiveJournalRecord) {
			r.EventJSON = json.RawMessage(strings.Repeat(" ", 32769))
		},
		"trailing_event": func(r *storagecatalog.WorkspaceArchiveJournalRecord) {
			r.EventJSON = append(append(json.RawMessage(nil), r.EventJSON...), []byte(` {}`)...)
		},
		"event_identity": func(r *storagecatalog.WorkspaceArchiveJournalRecord) {
			var e WorkspaceLifecycleEvent
			_ = json.Unmarshal(r.EventJSON, &e)
			e.EventID = "workspace_lifecycle_event_01ARZ3NDEKTSV4RRFFQ69G5FAY"
			r.EventJSON, _ = json.Marshal(e)
		},
		"event_owner": func(r *storagecatalog.WorkspaceArchiveJournalRecord) {
			var e WorkspaceLifecycleEvent
			_ = json.Unmarshal(r.EventJSON, &e)
			e.OperationID = restoreTestOperationID
			r.EventJSON, _ = json.Marshal(e)
		},
		"manifest": func(r *storagecatalog.WorkspaceArchiveJournalRecord) {
			var m WorkspaceArchiveManifest
			_ = json.Unmarshal(r.ManifestJSON, &m)
			m.Reason = "substitution"
			r.ManifestJSON, _ = json.Marshal(m)
		},
		"digest": func(r *storagecatalog.WorkspaceArchiveJournalRecord) {
			r.PlanDigest = "sha256:" + strings.Repeat("a", 64)
		},
		"trailing_plan": func(r *storagecatalog.WorkspaceArchiveJournalRecord) {
			r.PlanJSON = append(append(json.RawMessage(nil), r.PlanJSON...), []byte(` {}`)...)
		},
		"unknown_event": func(r *storagecatalog.WorkspaceArchiveJournalRecord) {
			var e map[string]any
			_ = json.Unmarshal(r.EventJSON, &e)
			e["unexpected"] = true
			r.EventJSON, _ = json.Marshal(e)
		},
		"missing_manifest": func(r *storagecatalog.WorkspaceArchiveJournalRecord) { r.ManifestJSON = nil },
		"oversized_manifest": func(r *storagecatalog.WorkspaceArchiveJournalRecord) {
			r.ManifestJSON = json.RawMessage(strings.Repeat(" ", workspaceManifestMaxBytes+1))
		},
	} {
		t.Run(name, func(t *testing.T) {
			f, p, before := archivedRestoreFixture(t)
			f.service.Journal = lifecycleEvidenceJournal{WorkspaceMoveJournal: f.journal, rewrite: func(_ string, r *storagecatalog.WorkspaceArchiveJournalRecord) { rewrite(r) }}
			if _, err := f.service.ReadLifecycleEvidence(context.Background(), p.OperationID); err == nil {
				t.Fatal("untrusted evidence accepted")
			}
			if !reflect.DeepEqual(snapshotWorkspacePayload(t, f.paths.ArchivePayload.AbsolutePath), before) || f.journal.eventCommits != 1 || f.journal.catalogCommits != 1 {
				t.Fatal("refused read mutated custody")
			}
		})
	}
}

func TestWorkspaceLifecycleEvidenceIsHistoryNotCurrentCustody(t *testing.T) {
	f, p, _ := archivedRestoreFixture(t)
	before, err := f.service.ReadLifecycleEvidence(context.Background(), p.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	// Removing availability in this owned fixture must not invalidate retained
	// historical truth or be silently represented as fresh filesystem proof.
	if err := os.Rename(f.paths.ArchivePayload.AbsolutePath, f.paths.ArchivePayload.AbsolutePath+"-offline"); err != nil {
		t.Fatal(err)
	}
	after, err := f.service.ReadLifecycleEvidence(context.Background(), p.OperationID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("history depends on live payload availability", err)
	}
}

func TestWorkspaceLifecycleEvidenceRejectsRestoreChainDrift(t *testing.T) {
	for _, target := range []string{"phase", "archive_digest", "archive_id"} {
		t.Run(target, func(t *testing.T) {
			f, p, _ := archivedRestoreFixture(t)
			restore := planRestore(t, f, p.OperationID)
			if _, err := f.service.ApplyRestore(context.Background(), restore, restore.PlanDigest); err != nil {
				t.Fatal(err)
			}
			f.journal.record.ManifestJSON = append(json.RawMessage(nil), f.journal.restoreRecords[restore.OperationID].ManifestJSON...)
			f.service.Journal = lifecycleEvidenceJournal{WorkspaceMoveJournal: f.journal, rewrite: func(id string, r *storagecatalog.WorkspaceArchiveJournalRecord) {
				if id != restore.OperationID {
					return
				}
				if target == "phase" {
					r.Phase = string(PhaseRestoreProjectionsCommitted)
					r.CompletedAt = nil
					return
				}
				var plan WorkspaceArchivePlan
				_ = json.Unmarshal(r.PlanJSON, &plan)
				if target == "archive_id" {
					plan.ArchiveOperationID = "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAZ"
				} else {
					plan.ArchiveManifestDigest = "sha256:" + strings.Repeat("b", 64)
				}
				plan.PlanDigest, _ = WorkspaceArchivePlanDigest(plan)
				r.PlanDigest = plan.PlanDigest
				r.PlanJSON, _ = json.Marshal(plan)
			}}
			for _, id := range []string{p.OperationID, restore.OperationID} {
				if _, err := f.service.ReadLifecycleEvidence(context.Background(), id); err == nil {
					t.Fatal("substituted restore chain accepted")
				}
			}
		})
	}
}

func TestWorkspaceLifecycleEvidenceRequiresKey(t *testing.T) {
	f, p, _ := archivedRestoreFixture(t)
	f.service.ManifestKey = nil
	if _, err := f.service.ReadLifecycleEvidence(context.Background(), p.OperationID); err == nil {
		t.Fatal("missing key accepted")
	}
}
