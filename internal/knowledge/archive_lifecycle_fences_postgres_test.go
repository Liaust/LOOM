package knowledge

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/storagecatalog"
)

func notesFenceObject(t *testing.T, f *notesArchivePostgresFixture, relative string) KnowledgeObject {
	t.Helper()
	for _, object := range f.objects {
		if object.RelativePath == relative {
			current, err := f.s.store.GetKnowledgeObject(t.Context(), object.KnowledgeObjectID)
			if err != nil {
				t.Fatal(err)
			}
			return current
		}
	}
	t.Fatalf("missing fixture object %s", relative)
	return KnowledgeObject{}
}

func notesFenceClaim(t *testing.T, f *notesArchivePostgresFixture) PipelineWorkItem {
	t.Helper()
	policy, err := f.s.GetPipelinePolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	object := notesFenceObject(t, f, "topic-one/source.md")
	if _, err := f.s.EnsurePipelineRun(t.Context(), object, policy.Policy, true, 1); err != nil {
		t.Fatal(err)
	}
	items, err := f.s.ClaimPipelineRuns(t.Context(), PipelineExecutionCoordinator, "notes-custody-fixture", PipelineClaimOptions{Limit: 1})
	if err != nil || len(items) != 1 || items[0].Object.KnowledgeObjectID != object.KnowledgeObjectID {
		t.Fatalf("initial claim: %+v %v", items, err)
	}
	return items[0]
}

func TestNotesCustodyFencePendingPublicationPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	claim := notesFenceClaim(t, f)
	policy, err := f.s.GetPipelinePolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"topic-one/second.md", "neighbour/other.md"} {
		if _, err := f.s.EnsurePipelineRun(t.Context(), notesFenceObject(t, f, relative), policy.Policy, true, 2); err != nil {
			t.Fatal(err)
		}
	}
	before := f.processingSnapshot(t)
	f.p.Workspace.FailureHook = func(boundary storagearchive.WorkspaceMoveBoundary) error {
		if boundary == storagearchive.BoundaryAfterIntent {
			return errors.New("disposable stop after durable intent")
		}
		return nil
	}
	if _, err := f.p.Workspace.ApplyArchive(t.Context(), f.plan, f.plan.PlanDigest); err == nil {
		t.Fatal("failure boundary not exercised")
	}
	for _, relative := range []string{"topic-one/source.md", "topic-one/second.md"} {
		object := notesFenceObject(t, f, relative)
		if _, err := f.s.store.UpsertKnowledgeObject(t.Context(), object); !errors.Is(err, ErrNotesCustodyPaused) {
			t.Fatalf("pending archive admitted metadata: %v", err)
		}
		if _, err := f.s.EnsurePipelineRun(t.Context(), object, policy.Policy, true, 1); !errors.Is(err, ErrNotesCustodyPaused) {
			t.Fatalf("pending archive created trajectory: %v", err)
		}
	}
	if _, err := f.s.CompletePipelineStage(t.Context(), claim, nil); !errors.Is(err, ErrNotesCustodyPaused) {
		t.Fatalf("in-flight publication passed intent: %v", err)
	}
	if f.processingSnapshot(t) != before {
		t.Fatal("refused admission/publication modified existing processing")
	}
	items, err := f.s.ClaimPipelineRuns(t.Context(), PipelineExecutionCoordinator, "notes-custody-neighbour", PipelineClaimOptions{Limit: 20})
	if err != nil || len(items) != 1 || items[0].Object.RelativePath != "neighbour/other.md" {
		t.Fatalf("pending archive did not preserve neighbour claim: %+v %v", items, err)
	}
	if _, err := f.s.CompletePipelineStage(t.Context(), items[0], nil); err != nil {
		t.Fatalf("neighbour publication blocked: %v", err)
	}
	f.p.Workspace.FailureHook = nil
	if _, err := f.p.Workspace.RecoverArchive(t.Context(), f.plan.OperationID); err != nil {
		t.Fatal(err)
	}
	f.before = f.processingSnapshot(t)
	if _, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID); err != nil {
		t.Fatal(err)
	}
	f.assertCustody(t, "archived", 2)
	if _, err := f.s.CompletePipelineStage(t.Context(), claim, nil); !errors.Is(err, ErrNotesCustodyPaused) {
		t.Fatalf("archive completion reopened old claim: %v", err)
	}
}

func TestNotesCustodyFenceAdmissionRestorePostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	object := notesFenceObject(t, f, "topic-one/source.md")
	f.archive(t)
	for _, projected := range []bool{false, true} {
		if projected {
			if _, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := f.s.store.UpsertKnowledgeObject(t.Context(), object); !errors.Is(err, ErrNotesCustodyPaused) {
			t.Fatalf("archive admitted object (projected=%v): %v", projected, err)
		}
		roots, err := f.s.store.ListSourceRoots(t.Context(), SourceRootFilter{})
		if err != nil {
			t.Fatal(err)
		}
		synced, err := f.s.store.ListSyncedObjectEntriesForNotesRoots(t.Context(), "")
		if err != nil {
			t.Fatal(err)
		}
		result, err := f.s.ReconcileKnowledgeObjects(t.Context(), KnowledgeObjectReconcileInput{SourceRoots: roots, SyncedObjects: synced})
		if err != nil || result.Applied != 3 || len(result.Skipped) != 2 {
			t.Fatalf("archive reconciliation: %+v %v", result, err)
		}
		for _, skip := range result.Skipped {
			if skip.Reason != "source_custody_paused" {
				t.Fatalf("untyped custody skip: %+v", skip)
			}
		}
	}
	before := f.processingSnapshot(t)
	// Ordinary policy updates must not fail globally because archived Notes exist.
	disabled := false
	if _, err := f.s.UpdatePipelinePolicy(t.Context(), PipelinePolicyUpdate{PDFOCREnabled: &disabled}); err != nil {
		t.Fatalf("archived source blocked active policy update: %v", err)
	}
	restore := f.restore(t)
	if _, err := f.s.store.UpsertKnowledgeObject(t.Context(), object); !errors.Is(err, ErrNotesCustodyPaused) {
		t.Fatalf("unprojected restore reopened admission: %v", err)
	}
	if _, err := f.p.ProjectOperation(t.Context(), restore.OperationID); err != nil {
		t.Fatal(err)
	}
	if f.processingSnapshot(t) != before {
		t.Fatal("path-only restore changed processing")
	}
	object, err := f.s.store.GetKnowledgeObject(t.Context(), object.KnowledgeObjectID)
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := f.s.store.UpsertKnowledgeObject(t.Context(), object)
	if err != nil || admitted.KnowledgeObjectID != object.KnowledgeObjectID {
		t.Fatalf("exact restore did not preserve admission identity: %+v %v", admitted, err)
	}
	policy, err := f.s.GetPipelinePolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := f.s.ensurePipelineRun(t.Context(), admitted, policy.Policy, false, 1); err != nil || created {
		t.Fatalf("path-only restore generated pipeline: created=%v %v", created, err)
	}
}

func TestNotesCustodyFenceCatalogPathReusePostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	object := notesFenceObject(t, f, "topic-one/source.md")
	catalog := storagecatalog.NewService(f.s.store.db)
	entry, err := catalog.RegisterEntry(t.Context(), storagecatalog.RegisterEntryInput{
		StorageClass: storagecatalog.StorageClassObjectBlob, SourceArea: storagecatalog.SourceAreaExternalWatchedRoot,
		OriginNodeID: *f.root.NodeID, OriginNodeKey: f.root.NodeKey, WatchedRootKey: f.root.BackendRootKey,
		LogicalPath: object.RelativePath, OriginalSourcePath: object.SourcePath, FileClass: object.FileClass, MimeType: object.MimeType,
		SizeBytes: object.SizeBytes, ChecksumAlgorithm: "sha256", ChecksumHex: strings.TrimPrefix(object.SourceHash, "sha256:"), AvailabilityState: storagecatalog.AvailabilityStateAvailable})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ReconcileKnowledgeObjects(t.Context(), KnowledgeObjectReconcileInput{SourceRoots: []SourceRoot{f.root}, StorageEntries: []storagecatalog.Entry{entry}}); err != nil {
		t.Fatal(err)
	}
	object = notesFenceObject(t, f, "topic-one/source.md")
	f.plan, err = f.p.Workspace.PlanArchive(t.Context(), storagearchive.WorkspaceArchivePlanInput{
		Kind: f.plan.Kind, ObjectID: f.plan.ObjectID, Slug: f.plan.Slug, ActorID: f.actor, Reason: f.plan.Reason})
	if err != nil {
		t.Fatal(err)
	}
	f.archive(t)
	before := f.processingSnapshot(t)
	object.KnowledgeObjectID = ids.NewKnowledgeObjectID()
	object.RelativePath = "catalog-path-reused.md"
	object.SourcePath = filepath.Join(f.root.SourcePath, object.RelativePath)
	if _, err := f.s.store.UpsertKnowledgeObject(t.Context(), object); !errors.Is(err, ErrNotesCustodyPaused) {
		t.Fatalf("catalog rebind became new object: %v", err)
	}
	if f.processingSnapshot(t) != before {
		t.Fatal("catalog path reuse changed object or queue identity")
	}
}

func waitNotesCustodyLock(t *testing.T, f *notesArchivePostgresFixture, granted bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		var found bool
		if err := f.s.store.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks
		 WHERE locktype='advisory' AND granted=$1 AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))`, granted).Scan(&found); err != nil {
			t.Fatal(err)
		}
		if found {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("custody lock was not observed")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestNotesCustodyFencePublicationBeforeIntentPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	claim := notesFenceClaim(t, f)
	blocker, err := f.s.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err := blocker.Exec(`SELECT 1 FROM knowledge.pipeline_runs WHERE knowledge_pipeline_run_id=$1 FOR UPDATE`, claim.Run.KnowledgePipelineRunID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	publication := make(chan error, 1)
	go func() {
		_, err := f.s.CompletePipelineStage(ctx, claim, nil)
		publication <- err
	}()
	waitNotesCustodyLock(t, f, true)
	archive := make(chan error, 1)
	go func() {
		_, err := f.p.Workspace.ApplyArchive(ctx, f.plan, f.plan.PlanDigest)
		archive <- err
	}()
	waitNotesCustodyLock(t, f, false)
	var intentCount int
	if err := f.s.store.db.QueryRow(`SELECT count(*) FROM storage.workspace_archive_operations`).Scan(&intentCount); err != nil || intentCount != 0 {
		t.Fatalf("intent passed open publication: %d %v", intentCount, err)
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-publication; err != nil {
		t.Fatalf("earlier publication did not finish: %v", err)
	}
	if err := <-archive; err != nil {
		t.Fatalf("archive did not proceed after publication: %v", err)
	}
	if _, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID); err != nil {
		t.Fatal(err)
	}
}
