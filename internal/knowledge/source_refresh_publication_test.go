package knowledge

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"loom.local/loom/internal/migrations"
)

func TestRefreshPathInputSurvivesRestartPostgres(t *testing.T) {
	db, url := boxSourcesDatabase(t)
	if _, err := migrations.Up(t.Context(), url, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	s, object := pipelineFixture(t, db, "markdown", "text/markdown", "# Stable\nCaptured cobalt notebook.\n", time.Now().UTC())
	s.sourceStagingRoot = t.TempDir()
	if _, err := s.EnsurePipelineRun(t.Context(), object, PipelinePolicy{}, false, 100); err != nil {
		t.Fatal(err)
	}
	result, err := s.RunPipelineCoordinatorOnce(t.Context(), PipelineCoordinatorRunInput{WorkerRunID: "capture", Limit: 1})
	if err != nil || result.Completed != 1 {
		t.Fatalf("capture: %+v %v", result, err)
	}
	run, err := s.store.GetPipelineRun(t.Context(), object.KnowledgeObjectID)
	if err != nil {
		t.Fatal(err)
	}
	var selected pipelineSourceSnapshot
	if err = json.Unmarshal(run.SourceSnapshot, &selected); err != nil || !selected.Temporary {
		t.Fatal("missing durable local input", err)
	}
	if err = os.WriteFile(object.SourcePath, []byte("New source still being observed."), 0600); err != nil {
		t.Fatal(err)
	}
	s = NewService(db, WithSourceStagingRoot(s.sourceStagingRoot))
	claim := claimSinglePipeline(t, s, PipelineExecutionCoordinator, "restart", time.Now())
	var body bytes.Buffer
	if err = s.readSelectedPipelineSource(t.Context(), claim.Object, 4096, &body); err != nil || !strings.Contains(body.String(), "Captured cobalt") {
		t.Fatalf("restart read latest file instead of selected bytes: %s %v", body.String(), err)
	}
	if err = s.ReleasePipelineClaim(t.Context(), claim); err != nil {
		t.Fatal(err)
	}
	advanceBoxSyncedFixture(t, s)
	if _, err = os.Stat(selected.Input.Path); !os.IsNotExist(err) {
		t.Fatalf("completed capture retained temporary bytes: %v", err)
	}
}

func TestRefreshSemanticPublicationPostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	setPipelinePolicyForTest(t, s.store.db, PipelinePolicy{EmbeddingsEnabled: true})
	objects := reconcileBoxSyncedFixture(t, s, roots)
	object := objects[0]
	var root SourceRoot
	for _, r := range roots {
		if r.NotesSourceRootID == object.NotesSourceRootID {
			root = r
		}
	}
	retainRefreshFixtureBlob(t, s, object)
	advanceBoxSyncedFixture(t, s)
	settings, err := s.store.GetEmbeddingSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	vector := make([]float32, settings.Dimensions)
	vector[0] = 1
	claim := func() PipelineWorkItem {
		t.Helper()
		items, err := s.ClaimPipelineRuns(t.Context(), PipelineExecutionHeavy, "semantic-refresh", PipelineClaimOptions{Limit: 20, Now: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			if item.Object.KnowledgeObjectID == object.KnowledgeObjectID {
				return item
			}
		}
		t.Fatal("embedding was not claimable")
		return PipelineWorkItem{}
	}
	search := func(mode string) []NotesSearchResult {
		t.Helper()
		items, err := s.searchSemanticNotesVector(t.Context(), NotesSearchInput{Query: "observatory", Mode: mode, NotesSourceRootID: root.NotesSourceRootID, SourceLifecycle: SourceLifecycleFilterActive, Limit: 50}, settings, vector)
		if err != nil {
			t.Fatal(err)
		}
		return items
	}
	first := claim()
	chunks, err := s.store.listKnowledgeChunksForVersion(t.Context(), first.Run.KnowledgeObjectVersionID)
	if err != nil || len(chunks) == 0 {
		t.Fatal("chunks", err)
	}
	for _, chunk := range chunks {
		if err = s.activateUnifiedChunkEmbedding(t.Context(), first, chunk, settings, vector, hashArtifactValue("first")); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.publishUnifiedEmbeddingObjectState(t.Context(), first, settings); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CompletePipelineStage(t.Context(), first, nil); err != nil {
		t.Fatal(err)
	}
	advanceBoxSyncedFixture(t, s)
	if len(search(NotesSearchModeSemantic)) == 0 {
		t.Fatal("initial semantic publication missing")
	}
	// Exercise the additive migration against populated, already-published output.
	dir := filepath.Join("..", "..", "migrations")
	if err = goose.DownContext(t.Context(), s.store.db, dir); err != nil {
		t.Fatal(err)
	}
	if err = goose.UpContext(t.Context(), s.store.db, dir); err != nil {
		t.Fatal(err)
	}
	if len(search(NotesSearchModeSemantic)) == 0 {
		t.Fatal("migration did not adopt the completed current semantic publication")
	}
	var partialPromoted bool
	if err = s.store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM knowledge.knowledge_objects WHERE knowledge_object_id<>$1 AND semantic_version_id IS NOT NULL)`, object.KnowledgeObjectID).Scan(&partialPromoted); err != nil || partialPromoted {
		t.Fatal("migration promoted incomplete semantic output", err)
	}
	dxObserve(t, s, root, object, "# Next notebook\n"+strings.Repeat("An amber experiment replaces the previous notebook observation. ", 400))
	reconcileBoxSyncedFixture(t, s, roots)
	advanceBoxSyncedFixture(t, s)
	next := claim()
	chunks, err = s.store.listKnowledgeChunksForVersion(t.Context(), next.Run.KnowledgeObjectVersionID)
	if err != nil || len(chunks) < 2 {
		t.Fatal("need multiple chunks", err)
	}
	if err = s.activateUnifiedChunkEmbedding(t.Context(), next, chunks[0], settings, vector, hashArtifactValue("partial")); err != nil {
		t.Fatal(err)
	}
	if err = s.publishUnifiedEmbeddingObjectState(t.Context(), next, settings); !errors.Is(err, ErrConflict) {
		t.Fatalf("partial embedding set published: %v", err)
	}
	for _, result := range search(NotesSearchModeSemantic) {
		if result.KnowledgeObjectVersionID != first.Run.KnowledgeObjectVersionID || result.Freshness.Current {
			t.Fatal("partial or mislabeled semantic result")
		}
	}
	if len(search(NotesSearchModeSemantic)) == 0 {
		t.Fatal("old semantic publication disappeared")
	}
	if len(search(NotesSearchModeHybrid)) != 0 {
		t.Fatal("hybrid mixed lexical and semantic versions")
	}
	for _, chunk := range chunks[1:] {
		if err = s.activateUnifiedChunkEmbedding(t.Context(), next, chunk, settings, vector, hashArtifactValue("complete")); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.publishUnifiedEmbeddingObjectState(t.Context(), next, settings); err != nil {
		t.Fatal(err)
	}
	results := search(NotesSearchModeHybrid)
	if len(results) == 0 || results[0].KnowledgeObjectVersionID != next.Run.KnowledgeObjectVersionID || !results[0].Freshness.Current {
		t.Fatalf("complete semantic publication: %+v", results)
	}
	var oldActive bool
	if err = s.store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM knowledge.chunk_embeddings WHERE knowledge_object_version_id=$1 AND active)`, first.Run.KnowledgeObjectVersionID).Scan(&oldActive); err != nil || oldActive {
		t.Fatal("unreferenced old vectors escaped existing history retention", err)
	}
}

func retainRefreshFixtureBlob(t *testing.T, s *Service, object KnowledgeObject) {
	t.Helper()
	var path string
	if err := s.store.db.QueryRow(`SELECT b.storage_path FROM files.blobs b WHERE b.hash_uri=$1`, object.SourceHash).Scan(&path); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	retained := filepath.Join(t.TempDir(), "retained.md")
	if err = os.WriteFile(retained, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.db.Exec(`UPDATE files.blobs SET storage_path=$1 WHERE hash_uri=$2`, retained, object.SourceHash); err != nil {
		t.Fatal(err)
	}
}

func TestSelectedRefreshPublicationPostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	objects := reconcileBoxSyncedFixture(t, s, roots)
	object := objects[0]
	var root SourceRoot
	for _, r := range roots {
		if r.NotesSourceRootID == object.NotesSourceRootID {
			root = r
		}
	}
	// The fixture's initial blob shares the source path. Give it an immutable
	// retained location before simulating repeated writes to the original path.
	entries, err := s.store.ListSyncedObjectEntriesForNotesRoots(t.Context(), root.NotesSourceRootID)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries: %v %v", entries, err)
	}
	retainRefreshFixtureBlob(t, s, object)
	step := func() {
		t.Helper()
		result, err := s.RunPipelineCoordinatorOnce(t.Context(), PipelineCoordinatorRunInput{WorkerRunID: "refresh-test", Limit: 20, Now: time.Now().Add(time.Hour)})
		if err != nil || result.Failed != 0 || result.Claimed == 0 {
			t.Fatalf("stage: %+v %v", result, err)
		}
	}
	step() // Select the initial revision, but do not extract it yet.
	first, err := s.store.GetPipelineRun(t.Context(), object.KnowledgeObjectID)
	if err != nil || len(first.SourceSnapshot) == 0 {
		t.Fatalf("selection: %+v %v", first, err)
	}
	dxObserve(t, s, root, object, "# Notebook\nNew amber experiment, waiting for extraction.\n")
	current := reconcileBoxSyncedFixture(t, s, roots)
	for _, o := range current {
		if o.KnowledgeObjectID == object.KnowledgeObjectID {
			object = o
		}
	}
	same, err := s.store.GetPipelineRun(t.Context(), object.KnowledgeObjectID)
	if err != nil || same.KnowledgePipelineRunID != first.KnowledgePipelineRunID {
		t.Fatal("new upload superseded selected work", err)
	}
	// A new service must resume the captured blob, not the latest upload.
	s = NewService(s.store.db)
	advanceBoxSyncedFixture(t, s)
	search := func(query string, strict bool) NotesSearchResultSet {
		t.Helper()
		out, err := s.SearchNotes(t.Context(), NotesSearchInput{Query: query, Mode: NotesSearchModeLexical, NotesSourceRootID: root.NotesSourceRootID, RequireCurrent: strict})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	old := search("cobalt observatory", false)
	if len(old.Results) != 1 || old.Results[0].Freshness.Current || old.Results[0].Freshness.IndexedSourceRevision != first.SourceRevision || old.Results[0].Freshness.LatestSourceRevision != object.SourceRevision {
		t.Fatalf("last publication not truthful: %+v", old)
	}
	strict := search("cobalt observatory", true)
	if len(strict.Results) != 0 || strict.RefreshingMatchesOmitted != 1 {
		t.Fatalf("strict current: %+v", strict)
	}
	latest, err := s.store.GetKnowledgeObject(t.Context(), object.KnowledgeObjectID)
	if err != nil || latest.SourceRevision == first.SourceRevision {
		t.Fatal("old run overwrote latest observation", err)
	}
	reconcileBoxSyncedFixture(t, s, roots)
	queued, err := s.store.GetPipelineRun(t.Context(), object.KnowledgeObjectID)
	if err != nil || queued.SourceRevision != object.SourceRevision {
		t.Fatal("newest pending target was not queued", err)
	}
	if len(search("cobalt observatory", false).Results) != 1 {
		t.Fatal("queued replacement hid old publication")
	}
	advanceBoxSyncedFixture(t, s)
	newest := search("amber experiment", true)
	if len(newest.Results) != 1 || !newest.Results[0].Freshness.Current {
		t.Fatalf("new publication: %+v", newest)
	}
	if len(search("cobalt observatory", false).Results) != 0 {
		t.Fatal("old lexical publication still selected")
	}
	if _, err = s.GetNotesPassage(t.Context(), *old.Results[0].PassageFollowup); err != nil {
		t.Fatal("old exact citation was lost", err)
	}
	// Force on identical bytes gets separate output storage, so publication is
	// retained throughout the replacement, not deleted by rechunking.
	forced, err := s.EnsurePipelineRun(t.Context(), latest, PipelinePolicy{}, true, 100)
	if err != nil || forced.KnowledgeObjectVersionID == queued.KnowledgeObjectVersionID {
		t.Fatal("force reused published output storage", err)
	}
	if len(search("amber experiment", true).Results) != 1 {
		t.Fatal("forced refresh hid good output")
	}
	advanceBoxSyncedFixture(t, s)
	if len(search("amber experiment", true).Results) != 1 {
		t.Fatal("forced publication absent")
	}
	dxObserve(t, s, root, object, "")
	reconcileBoxSyncedFixture(t, s, roots)
	advanceBoxSyncedFixture(t, s)
	if len(search("amber experiment", false).Results) != 0 {
		t.Fatal("empty replacement retained the old searchable body")
	}
	dxObserve(t, s, root, object, "# Notebook\nNew amber experiment, waiting for extraction.\n")
	reconcileBoxSyncedFixture(t, s, roots)
	advanceBoxSyncedFixture(t, s)
	if len(search("amber experiment", true).Results) != 1 {
		t.Fatal("A-B-A source reversion did not publish its new identity")
	}
	if _, err = s.store.db.Exec(`UPDATE files.file_metadata SET index_policy='private_no_index'`); err != nil {
		t.Fatal(err)
	}
	if len(search("amber experiment", false).Results) != 0 {
		t.Fatal("publication resurrected withdrawn source")
	}
}
