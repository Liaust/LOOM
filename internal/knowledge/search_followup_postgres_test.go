package knowledge

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/ids"
)

func notesFollowupExpected(t *testing.T, s *Service, object string, lifecycle SourceLifecycleFilter) NotesPassageInput {
	t.Helper()
	p := NotesPassageInput{SourceLifecycle: lifecycle}
	// Fixture-owned independent expectation; never derive the hash from a hit.
	if err := s.store.db.QueryRow(`SELECT c.knowledge_object_id,c.knowledge_object_version_id,c.knowledge_chunk_id,v.source_hash
	 FROM knowledge.knowledge_chunks c JOIN knowledge.knowledge_object_versions v USING(knowledge_object_version_id)
	 JOIN knowledge.knowledge_objects o ON o.knowledge_object_id=c.knowledge_object_id AND o.source_hash=v.source_hash AND o.source_revision=v.source_revision
	 WHERE c.knowledge_object_id=$1 AND COALESCE(c.metadata->>'text_source','')<>'metadata_text' ORDER BY c.chunk_index LIMIT 1`, object).
		Scan(&p.KnowledgeObjectID, &p.KnowledgeObjectVersionID, &p.KnowledgeChunkID, &p.SourceHash); err != nil {
		t.Fatal(err)
	}
	return p
}

func notesFollowupSnapshot(t *testing.T, s *Service) string {
	t.Helper()
	rows, err := s.store.db.Query(`SELECT schemaname,tablename FROM pg_tables WHERE schemaname IN ('knowledge','search','provenance','events') ORDER BY schemaname,tablename`)
	if err != nil {
		t.Fatal(err)
	}
	var tables [][2]string
	for rows.Next() {
		var table [2]string
		if err := rows.Scan(&table[0], &table[1]); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	var snapshot strings.Builder
	for _, table := range tables {
		var raw string
		query := fmt.Sprintf(`SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text),'[]'::jsonb)::text FROM %q.%q r`, table[0], table[1])
		if err := s.store.db.QueryRow(query).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&snapshot, "%s.%s:%s\n", table[0], table[1], raw)
	}
	return snapshot.String()
}

func notesFollowupReadOnly(t *testing.T, s *Service, exercise func()) {
	t.Helper()
	s.store.db.SetMaxOpenConns(1)
	before := notesFollowupSnapshot(t, s)
	if _, err := s.store.db.Exec(`SET default_transaction_read_only=on`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := s.store.db.Exec(`SET default_transaction_read_only=off`); err != nil {
			t.Error(err)
		}
	}()
	exercise()
	if after := notesFollowupSnapshot(t, s); after != before {
		t.Fatal("search/passage changed knowledge, search, event or provenance state")
	}
}

func TestNotesSearchExactFollowupPostgres(t *testing.T) {
	t.Run("historical_and_current_admission", testNotesFollowupHistorical)
	t.Run("archive_restore_groups", testNotesFollowupLifecycles)
	t.Run("metadata_extraction", testNotesFollowupMetadata)
}

func testNotesFollowupHistorical(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	objects := reconcileBoxSyncedFixture(t, s, roots)
	advanceBoxSyncedFixture(t, s)
	var object KnowledgeObject
	for _, candidate := range objects {
		if candidate.SourceCategory == "notes" {
			object = candidate
		}
	}
	if object.KnowledgeObjectID == "" {
		t.Fatal("missing fixture Notes object")
	}
	f := &notesArchivePostgresFixture{s: s}
	runtime := seedNotesArchiveSearchEmbeddings(t, f)
	want := notesFollowupExpected(t, s, object.KnowledgeObjectID, SourceLifecycleFilterActive)
	savedBytes, _ := json.Marshal(want)
	var saved NotesPassageInput
	notesFollowupReadOnly(t, s, func() {
		for _, mode := range []string{NotesSearchModeLexical, NotesSearchModeSemantic, NotesSearchModeHybrid} {
			calls := runtime.calls
			out, err := s.SearchNotes(t.Context(), NotesSearchInput{Query: "cobalt observatory", SourceCategory: "notes", Mode: mode, Limit: 10})
			if err != nil || out.Mode != mode || len(out.Results) != 1 || out.Results[0].PassageFollowup == nil || *out.Results[0].PassageFollowup != want {
				t.Fatalf("%s exact search: %+v %v", mode, out, err)
			}
			if mode == NotesSearchModeLexical && runtime.calls != calls || mode != NotesSearchModeLexical && runtime.calls != calls+1 {
				t.Fatal("unexpected source/model work")
			}
			saved = *out.Results[0].PassageFollowup
		}
	})
	before, err := s.GetNotesPassage(t.Context(), saved)
	if err != nil || before.Historical {
		t.Fatalf("initial passage: %+v %v", before, err)
	}
	reader := startNotesArchivePublicReader(t, f)
	var apiSearch NotesSearchResultSet
	reader.api(t, "search", NotesSearchInput{Query: "cobalt observatory", SourceCategory: "notes", Mode: NotesSearchModeLexical}, http.StatusOK, &apiSearch)
	if len(apiSearch.Results) != 1 || apiSearch.Results[0].PassageFollowup == nil || *apiSearch.Results[0].PassageFollowup != saved {
		t.Fatal("actual HTTP search lost tuple")
	}
	command := exec.CommandContext(t.Context(), reader.cli, "--socket", reader.socket, "notes", "search", "cobalt observatory", "--category", "notes", "--mode", "lexical")
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + reader.root, "XDG_CONFIG_HOME=" + reader.root}
	body, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("human search: %s %v", body, err)
	}
	var displayed string
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "Follow-up [1]: ") {
			displayed = strings.TrimPrefix(line, "Follow-up [1]: ")
		}
	}
	if displayed == "" || len("Follow-up [1]: "+displayed+"\n") > 512 {
		t.Fatalf("no bounded copyable command: %s", body)
	}
	setPipelinePolicyForTest(t, s.store.db, PipelinePolicy{})
	entries, err := s.store.ListSyncedObjectEntriesForNotesRoots(t.Context(), object.NotesSourceRootID)
	if err != nil || len(entries) != 1 {
		t.Fatalf("fixture sync entry: %v", err)
	}
	e := entries[0]
	revised := before.Text + "\nRevised cobalt observatory conclusion, version two.\n"
	file := filepath.Join(t.TempDir(), "revised.md")
	if err := os.WriteFile(file, []byte(revised), 0600); err != nil {
		t.Fatal(err)
	}
	blob, version, hash := ids.NewBlobID(), ids.NewObjectVersionID(), hashArtifactValue(revised)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO files.blobs(blob_id,hash_algorithm,hash_hex,hash_uri,size_bytes,storage_path,status) VALUES ($1,'sha256',$2,$3,$4,$5,'verified')`, []any{blob, strings.TrimPrefix(hash, "sha256:"), hash, len(revised), file}},
		{`INSERT INTO objects.object_versions(object_version_id,object_id,version_number,blob_id,content_hash,source_node_id,source_path,size_bytes,mime_type,status,metadata) VALUES ($1,$2,2,$3,$4,$5,$6,$7,'text/markdown','active','{"file_class":"markdown"}')`, []any{version, e.ObjectID, blob, hash, e.SourceNodeID, e.SourcePath, len(revised)}},
		{`UPDATE files.file_metadata SET latest_version_id=$1 WHERE object_id=$2`, []any{version, e.ObjectID}},
		{`UPDATE objects.object_versions SET status='superseded' WHERE object_version_id=$1`, []any{e.ObjectVersionID}},
		{`UPDATE sync.replicas SET replicated_id=$1,storage_ref=$2 WHERE replica_id=$3`, []any{version, hash, e.ReplicaID}},
	} {
		if _, err := s.store.db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.GetNotesPassage(t.Context(), saved); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unadmitted source update did not revoke saved tuple: %v", err)
	}
	reconcileBoxSyncedFixture(t, s, roots)
	advanceBoxSyncedFixture(t, s)
	current := notesFollowupExpected(t, s, object.KnowledgeObjectID, SourceLifecycleFilterActive)
	if current.SourceHash != hash || current.KnowledgeObjectVersionID == saved.KnowledgeObjectVersionID {
		t.Fatal("v2 fixture did not advance")
	}
	// No source payload is accessible during the read proof. Database-retained
	// text must suffice; there is no source or latest-body rescue path.
	for _, path := range []string{file, filepath.Join(roots[0].SourcePath, "source.md")} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	notesFollowupReadOnly(t, s, func() {
		for range 3 {
			passage, err := s.GetNotesPassage(t.Context(), saved)
			if err != nil || !passage.Historical || passage.Text != before.Text || passage.NotesPassageInput != saved {
				t.Fatalf("historical tuple changed: %+v %v", passage, err)
			}
		}
		var apiCurrent NotesSearchResultSet
		reader.api(t, "search", NotesSearchInput{Query: "cobalt observatory", SourceCategory: "notes", Mode: NotesSearchModeLexical}, 200, &apiCurrent)
		if len(apiCurrent.Results) != 1 || apiCurrent.Results[0].PassageFollowup == nil || *apiCurrent.Results[0].PassageFollowup != current {
			t.Fatal("HTTP latest search did not carry its own tuple")
		}
		var historical NotesPassage
		args := strings.Fields(strings.TrimPrefix(displayed, "loom notes "))
		reader.command(t, args, true, &historical)
		if historical.NotesPassageInput != saved || !historical.Historical || historical.Text != before.Text {
			t.Fatal("copied pre-update command substituted latest")
		}
	})
	// Direct database-observation mutation, not an ordinary rename: a current
	// eligible .md path must not revoke the original historical tuple.
	if _, err := s.store.db.Exec(`UPDATE objects.object_versions SET source_path=$1 WHERE object_version_id=$2`, "watched-root://"+roots[0].BackendRootKey+"/renamed.md", version); err != nil {
		t.Fatal(err)
	}
	eligible, err := s.GetNotesPassage(t.Context(), saved)
	if err != nil || !eligible.Historical || eligible.Text != before.Text || eligible.NotesPassageInput != saved {
		t.Fatalf("eligible current path lost historical text: %+v %v", eligible, err)
	}
	if _, err := s.store.db.Exec(`UPDATE objects.object_versions SET source_path=$1 WHERE object_version_id=$2`, e.SourcePath, version); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*NotesPassageInput){
		"object":  func(p *NotesPassageInput) { p.KnowledgeObjectID = ids.NewKnowledgeObjectID() },
		"version": func(p *NotesPassageInput) { p.KnowledgeObjectVersionID = current.KnowledgeObjectVersionID },
		"chunk":   func(p *NotesPassageInput) { p.KnowledgeChunkID = current.KnowledgeChunkID },
		"hash":    func(p *NotesPassageInput) { p.SourceHash = current.SourceHash },
	} {
		t.Run(name, func(t *testing.T) {
			bad := saved
			mutate(&bad)
			if _, err := s.GetNotesPassage(t.Context(), bad); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("wrong binding accepted: %v", err)
			}
		})
	}
	for name, statements := range map[string][2]string{
		"privacy":         {`UPDATE files.file_metadata SET index_policy='private_no_index' WHERE object_id=(SELECT metadata->'synced_object'->>'object_id' FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`, `UPDATE files.file_metadata SET index_policy='text_later' WHERE object_id=(SELECT metadata->'synced_object'->>'object_id' FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`},
		"root":            {`UPDATE knowledge.notes_source_roots SET status='blocked' WHERE notes_source_root_id=(SELECT notes_source_root_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`, `UPDATE knowledge.notes_source_roots SET status='active' WHERE notes_source_root_id=(SELECT notes_source_root_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`},
		"node":            {`UPDATE knowledge.knowledge_objects SET source_node_key='other-owner' WHERE knowledge_object_id=$1`, `UPDATE knowledge.knowledge_objects SET source_node_key='sync-owner' WHERE knowledge_object_id=$1`},
		"deleted":         {`UPDATE knowledge.knowledge_objects SET deleted_at=now() WHERE knowledge_object_id=$1`, `UPDATE knowledge.knowledge_objects SET deleted_at=NULL WHERE knowledge_object_id=$1`},
		"cross_root_move": {`UPDATE objects.object_versions SET source_path=replace(source_path,'watched-root://','watched-root://outside_') WHERE object_version_id=(SELECT metadata->'synced_object'->>'object_version_id' FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`, `UPDATE objects.object_versions SET source_path=replace(source_path,'watched-root://outside_','watched-root://') WHERE object_version_id=(SELECT metadata->'synced_object'->>'object_version_id' FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`},
		"source_move":     {`UPDATE objects.object_versions SET source_path=source_path||'.moved' WHERE object_version_id=(SELECT metadata->'synced_object'->>'object_version_id' FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`, `UPDATE objects.object_versions SET source_path=replace(source_path,'.moved','') WHERE object_version_id=(SELECT metadata->'synced_object'->>'object_version_id' FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.store.db.Exec(statements[0], object.KnowledgeObjectID); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := s.store.db.Exec(statements[1], object.KnowledgeObjectID); err != nil {
					t.Error(err)
				}
			}()
			if _, err := s.GetNotesPassage(t.Context(), saved); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("current revocation bypass: %v", err)
			}
		})
	}
	finalBytes, _ := json.Marshal(saved)
	if string(finalBytes) != string(savedBytes) {
		t.Fatal("saved tuple mutated across current admission changes")
	}
}

func testNotesFollowupLifecycles(t *testing.T) {
	f := notesArchiveTestFixture(t)
	runtime := seedNotesArchiveSearchEmbeddings(t, f)
	active := map[string]NotesPassageInput{}
	for _, object := range f.objects {
		active[object.KnowledgeObjectID] = notesFollowupExpected(t, f.s, object.KnowledgeObjectID, SourceLifecycleFilterActive)
	}
	f.archive(t)
	if _, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID); err != nil {
		t.Fatal(err)
	}
	// Only archive/restore-managed objects have a current-custody pointer.
	var pointerObject string
	for _, object := range f.objects {
		if object.RelativePath == "topic-one/source.md" {
			pointerObject = object.KnowledgeObjectID
		}
	}
	archivedPointer := active[pointerObject]
	archivedPointer.SourceLifecycle = SourceLifecycleFilterArchived
	var pointer []byte
	if err := f.s.store.db.QueryRow(`DELETE FROM knowledge.notes_current_custody WHERE knowledge_object_id=$1 RETURNING to_jsonb(notes_current_custody)`, pointerObject).Scan(&pointer); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.GetNotesPassage(t.Context(), archivedPointer); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing current custody bypass: %v", err)
	}
	if _, err := f.s.store.db.Exec(`INSERT INTO knowledge.notes_current_custody SELECT * FROM jsonb_populate_record(NULL::knowledge.notes_current_custody,$1)`, pointer); err != nil {
		t.Fatal(err)
	}
	archived := map[string]NotesPassageInput{}
	notesFollowupReadOnly(t, f.s, func() {
		for _, mode := range []string{NotesSearchModeLexical, NotesSearchModeSemantic, NotesSearchModeHybrid} {
			for _, filter := range []SourceLifecycleFilter{"", SourceLifecycleFilterArchived, SourceLifecycleFilterAll} {
				calls := runtime.calls
				out, err := f.s.SearchNotes(t.Context(), NotesSearchInput{Query: "cobalt observatory", Mode: mode, SourceLifecycle: filter, Limit: 10})
				wantCount := 3
				if filter == SourceLifecycleFilterArchived {
					wantCount = 2
				}
				if filter == SourceLifecycleFilterAll {
					wantCount = 5
				}
				if err != nil || len(out.Results) != wantCount || out.Mode != mode {
					t.Fatalf("%s/%s group search: %+v %v", mode, filter, out, err)
				}
				if mode != NotesSearchModeLexical && runtime.calls != calls+1 {
					t.Fatal("synthetic vector was not reused across groups")
				}
				for _, group := range out.LifecycleGroups {
					for _, result := range out.Results[group.Offset : group.Offset+group.ResultCount] {
						want := active[result.KnowledgeObjectID]
						want.SourceLifecycle = SourceLifecycleFilter(group.SourceLifecycle)
						if result.PassageFollowup == nil || *result.PassageFollowup != want || result.SourceLifecycle != group.SourceLifecycle {
							t.Fatalf("group tuple crossed lifecycle: %+v", result)
						}
						if _, err := f.s.GetNotesPassage(t.Context(), want); err != nil {
							t.Fatal(err)
						}
						if group.SourceLifecycle == SourceLifecycleArchived {
							archived[result.KnowledgeObjectID] = want
							if _, err := f.s.GetNotesPassage(t.Context(), active[result.KnowledgeObjectID]); !errors.Is(err, sql.ErrNoRows) {
								t.Fatalf("active saved tuple widened into archive: %v", err)
							}
						}
					}
				}
				if filter == "" && out.ArchivedMatchesOmitted != 2 {
					t.Fatal("default no longer omits archived matches")
				}
			}
		}
	})
	if len(archived) != 2 {
		t.Fatal("missing archived fixtures")
	}
	restored := f.restore(t)
	if _, err := f.p.ProjectOperation(t.Context(), restored.OperationID); err != nil {
		t.Fatal(err)
	}
	notesFollowupReadOnly(t, f.s, func() {
		for id, tuple := range archived {
			if _, err := f.s.GetNotesPassage(t.Context(), tuple); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("archived tuple widened into restored source: %v", err)
			}
			if _, err := f.s.GetNotesPassage(t.Context(), active[id]); err != nil {
				t.Fatal(err)
			}
		}
	})
}

func testNotesFollowupMetadata(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	entries, err := s.store.ListSyncedObjectEntriesForNotesRoots(t.Context(), roots[2].NotesSourceRootID)
	if err != nil || len(entries) != 1 {
		t.Fatalf("metadata fixture: %v", err)
	}
	e := entries[0]
	if _, err := s.store.db.Exec(`UPDATE objects.object_versions SET metadata='{"file_class":"image"}',mime_type='image/png' WHERE object_version_id=$1`, e.ObjectVersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.db.Exec(`UPDATE files.file_metadata SET mime_type='image/png' WHERE object_id=$1`, e.ObjectID); err != nil {
		t.Fatal(err)
	}
	objects := reconcileBoxSyncedFixture(t, s, roots)
	advanceBoxSyncedFixture(t, s)
	var metadataObject KnowledgeObject
	for _, object := range objects {
		if object.NotesSourceRootID == roots[2].NotesSourceRootID {
			metadataObject = object
		}
	}
	out, err := s.SearchNotes(t.Context(), NotesSearchInput{Query: "source", SourceCategory: "library", Mode: NotesSearchModeLexical})
	if err != nil || len(out.Results) != 1 || !out.Results[0].MetadataOnly || out.Results[0].PassageFollowup != nil || out.Results[0].KnowledgeObjectID != metadataObject.KnowledgeObjectID {
		t.Fatalf("metadata extraction gained passage: %+v %v", out, err)
	}
	f := &notesArchivePostgresFixture{s: s}
	seedNotesArchiveSearchEmbeddings(t, f)
	// A bound metadata_text chunk can exist in older/extraction fixture data.
	// Both actual SQL paths must retain search selection while omitting the tuple.
	if _, err := s.store.db.Exec(`UPDATE knowledge.knowledge_chunks SET metadata=jsonb_set(metadata,'{text_source}','"metadata_text"') WHERE knowledge_object_id IN (SELECT knowledge_object_id FROM knowledge.knowledge_objects WHERE notes_source_root_id=$1)`, roots[0].NotesSourceRootID); err != nil {
		t.Fatal(err)
	}
	notesFollowupReadOnly(t, s, func() {
		for _, mode := range []string{NotesSearchModeLexical, NotesSearchModeSemantic, NotesSearchModeHybrid} {
			out, err := s.SearchNotes(t.Context(), NotesSearchInput{Query: "cobalt observatory", SourceCategory: "notes", Mode: mode})
			if err != nil || len(out.Results) != 1 || out.Results[0].PassageFollowup != nil {
				t.Fatalf("%s metadata_text tuple: %+v %v", mode, out, err)
			}
		}
	})
}
