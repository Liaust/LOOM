package knowledge

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/objects"
	"loom.local/loom/internal/objectstore"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/search"
)

type currentTextReader struct{ text string }

func TestFileReadProcessingIsExactAndOptional(t *testing.T) {
	if fileReadProcessing(fileReadTarget{fileID: "metadata-only"}) != nil {
		t.Fatal("invented processing identity for a metadata-only target")
	}
	for _, tc := range []struct {
		version string
		current bool
		want    string
	}{
		{"", false, "unpublished"}, {"retained", false, "not_current"}, {"published", true, "current"},
	} {
		got := fileReadProcessing(fileReadTarget{objectID: "knowledge_object_exact", version: tc.version, current: tc.current})
		if got.KnowledgeObjectID != "knowledge_object_exact" || got.PublishedVersionID != tc.version || got.Publication != tc.want || got.InspectCommand != `loom notes pipelines inspect "knowledge_object_exact" --summary --json` {
			t.Fatalf("incorrect follow-up: %+v", got)
		}
	}
}

func (r currentTextReader) ReadNotesFile(_ context.Context, _, _, _ string, offset int64, limit int) (NativeFileRead, error) {
	return FileTextRange([]byte(r.text)[offset:], offset, limit)
}

func TestNotesObservedSourcePresenceUsesIndexedIdentityChecks(t *testing.T) {
	query := notesObservedSourcePresentSQL("ko")
	for _, fragment := range []string{
		"observed_file.object_id=COALESCE(ko.canonical_file_id,ko.metadata->'synced_object'->>'object_id')",
		"observed_file.managed_source_path=ko.source_path",
		"observed_file.source_relative_path=ko.relative_path AND observed_file.source_root_key<>''",
		"observed_file.source_relative_path=ko.relative_path AND observed_file.source_root_key=''",
		"WHERE identity_root.notes_source_root_id=ko.notes_source_root_id)=''",
		"e.to_state='archived'",
		"unavailable_root.metadata->>'root_reachable'='false'",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("presence fence lost %s", fragment)
		}
	}
	if strings.Contains(query, "OR observed_file.") {
		t.Fatal("identity OR forces per-object metadata scans")
	}
	for _, fence := range []string{"FROM files.file_metadata observed_file", "observed_file.source_node_id=ko.source_node_id", "observed_file.source_presence IN ('missing','unavailable')"} {
		if strings.Count(query, fence) != 4 {
			t.Fatalf("each identity must retain %s", fence)
		}
	}
}

func TestCurrentFileSearchReadAndSingleBodyOwnerPostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	admitted := reconcileBoxSyncedFixture(t, s, roots)
	if len(admitted) != 3 || advanceBoxSyncedFixture(t, s) == 0 {
		t.Fatal("no publication")
	}
	files, err := s.SearchFiles(t.Context(), NotesSearchInput{Query: "cobalt observatory", NotesSourceRootID: roots[0].NotesSourceRootID, Mode: NotesSearchModeLexical})
	if err != nil || len(files.Results) != 1 || !filepath.IsAbs(files.Results[0].Path) {
		t.Fatalf("%+v %v", files, err)
	}
	id := files.Results[0].FileID
	var version string
	if err = s.store.db.QueryRow(`SELECT latest_version_id FROM files.file_metadata WHERE object_id=$1`, id).Scan(&version); err != nil {
		t.Fatal(err)
	}
	technical := search.NewService(s.store.db, objectstore.Store{}, CurrentFileBodyDocumentSQL())
	enqueued, err := technical.EnqueueObjectVersion(t.Context(), requestctx.Context{}, search.IndexEnqueueInput{ObjectID: id, ObjectVersionID: version})
	if err != nil || enqueued.Status != "disabled_by_policy" {
		t.Fatalf("duplicate writer: %+v %v", enqueued, err)
	}
	indexed, err := technical.Search(t.Context(), requestctx.Context{}, search.SearchInput{Query: "cobalt observatory", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, hit := range indexed.Results {
		if hit.ObjectID == id && hit.ResultKind == KnowledgeSearchSourceKind {
			found = true
		}
	}
	if !found {
		t.Fatal("Objects search did not share the canonical Notes body")
	}
	s.fileSourceReader = currentTextReader{"new current source independent of indexed body"}
	for _, ref := range []string{id, files.Results[0].Path} {
		read, err := s.ReadFile(t.Context(), FileReadInput{Ref: ref})
		if err != nil || read.Text != "new current source independent of indexed body" || read.FileID != id {
			t.Fatalf("%+v %v", read, err)
		}
	}
	// A legacy duplicate membership keeps its receipt, but cannot create another
	// current processing trajectory for the same physical file.
	var originalID string
	if err = s.store.db.QueryRow(`SELECT knowledge_object_id FROM knowledge.knowledge_objects WHERE canonical_file_id=$1`, id).Scan(&originalID); err != nil {
		t.Fatal(err)
	}
	object, err := s.store.GetKnowledgeObject(t.Context(), originalID)
	if err != nil {
		t.Fatal(err)
	}
	var duplicate string
	if err = s.store.db.QueryRow(`SELECT knowledge_object_id FROM knowledge.knowledge_objects WHERE notes_source_root_id=$1`, roots[1].NotesSourceRootID).Scan(&duplicate); err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.db.Exec(`UPDATE knowledge.knowledge_objects SET canonical_file_id=$2 WHERE knowledge_object_id=$1`, duplicate, id); err != nil {
		t.Fatal(err)
	}
	if err = s.store.db.QueryRow(`SELECT max(knowledge_object_id) FROM knowledge.knowledge_objects WHERE canonical_file_id=$1`, id).Scan(&duplicate); err != nil {
		t.Fatal(err)
	}
	object.KnowledgeObjectID = duplicate
	if _, err = s.EnsurePipelineRun(t.Context(), object, PipelinePolicy{}, false, 100); !errors.Is(err, ErrNotesCustodyPaused) {
		t.Fatalf("duplicate processing owner: %v", err)
	}
}

func TestNotesFilePresencePostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	admitted := reconcileBoxSyncedFixture(t, s, roots)
	if len(admitted) != 3 || advanceBoxSyncedFixture(t, s) == 0 {
		t.Fatal("fixture has no current publication")
	}
	root := roots[0]
	input := NotesSearchInput{Query: "cobalt observatory", NotesSourceRootID: root.NotesSourceRootID, Mode: NotesSearchModeLexical}
	before, err := s.SearchNotes(t.Context(), input)
	if err != nil || len(before.Results) != 1 || before.Results[0].PassageFollowup == nil {
		t.Fatalf("initial search: %+v %v", before, err)
	}
	result := before.Results[0]
	var canonical string
	if err := s.store.db.QueryRow(`SELECT canonical_file_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1`, result.KnowledgeObjectID).Scan(&canonical); err != nil {
		t.Fatal(err)
	}
	for i, presence := range []string{"present", "missing", "unavailable", "present"} {
		tx, err := s.store.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		file, err := objects.ObserveFileTx(t.Context(), tx, requestctx.Context{}, objects.FileObservation{
			NodeID: *root.NodeID, RootKey: root.BackendRootKey, RelativePath: "source.md", Generation: "fixture",
			SourcePath: "watched-root://" + root.BackendRootKey + "/source.md", ManagedPath: filepath.Join(root.SourcePath, "source.md"),
			Presence: presence, Sequence: int64(i + 1), ObservedAt: time.Now().UTC(), SizeBytes: 3,
			InitialIndexPolicy: "metadata_only", InitialRawBackupPolicy: "normal"})
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if file != canonical {
			t.Fatal("observation failed to adopt exact existing identity")
		}
		current, err := s.SearchNotes(t.Context(), input)
		want := 0
		if presence == "present" {
			want = 1
		}
		if err != nil || len(current.Results) != want {
			t.Fatalf("presence %s: %+v %v", presence, current, err)
		}
		retained, err := s.GetNotesPassage(t.Context(), *result.PassageFollowup)
		if err != nil || retained.Historical != (presence != "present") || retained.Text == "" {
			t.Fatalf("exact retained passage lost its truthful state: %+v %v", retained, err)
		}
	}
	// Another admitted root describing the same physical path shares the
	// existing canonical parent; it does not merge unrelated files by hash.
	tx, err := s.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	alias, err := objects.ObserveFileTx(t.Context(), tx, requestctx.Context{}, objects.FileObservation{
		NodeID: *root.NodeID, RootKey: "overlapping-root", RelativePath: "source.md", Generation: "fixture",
		SourcePath: "watched-root://overlapping-root/source.md", ManagedPath: filepath.Join(root.SourcePath, "source.md"),
		Presence: "present", Sequence: 5, ObservedAt: time.Now().UTC(), SizeBytes: 3,
		InitialIndexPolicy: "metadata_only", InitialRawBackupPolicy: "normal"})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if alias != canonical {
		t.Fatal("overlapping path created another current file")
	}
	var count int
	if err := s.store.db.QueryRow(`SELECT count(*) FROM files.file_metadata WHERE managed_source_path=$1 AND source_node_id=$2`, filepath.Join(root.SourcePath, "source.md"), *root.NodeID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("current file identity duplicated: %d %v", count, err)
	}
}

func TestNotesFilePresencePreservesVerifiedArchivePostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	if _, err := f.p.Workspace.ApplyArchive(t.Context(), f.plan, f.plan.PlanDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := f.s.store.db.QueryRow(`SELECT knowledge_object_id FROM knowledge.notes_object_custody WHERE workspace_object_id='topic_notes_one' ORDER BY knowledge_object_id LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.store.db.Exec(`UPDATE files.file_metadata SET source_presence='missing' WHERE object_id IN (SELECT canonical_file_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`, id); err != nil {
		t.Fatal(err)
	}
	var available bool
	if err := f.s.store.db.QueryRow(`SELECT `+notesObservedSourcePresentSQL("ko")+` FROM knowledge.knowledge_objects ko WHERE knowledge_object_id=$1`, id).Scan(&available); err != nil || !available {
		t.Fatalf("verified archive was withdrawn by missing Box path: %t %v", available, err)
	}
}
