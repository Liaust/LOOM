package knowledge

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/ids"
)

func TestBoxSyncedHistoricalPassagePolicyPostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	objects := reconcileBoxSyncedFixture(t, s, roots)
	advanceBoxSyncedFixture(t, s)
	for _, object := range objects {
		t.Run(object.SourceCategory, func(t *testing.T) {
			var citation NotesPassageInput
			if err := s.store.db.QueryRow(`SELECT c.knowledge_object_id,c.knowledge_object_version_id,c.knowledge_chunk_id,v.source_hash FROM knowledge.knowledge_chunks c JOIN knowledge.knowledge_object_versions v USING(knowledge_object_version_id) WHERE c.knowledge_object_id=$1 ORDER BY c.chunk_index LIMIT 1`, object.KnowledgeObjectID).Scan(&citation.KnowledgeObjectID, &citation.KnowledgeObjectVersionID, &citation.KnowledgeChunkID, &citation.SourceHash); err != nil {
				t.Fatal(err)
			}
			before, err := s.GetNotesPassage(t.Context(), citation)
			if err != nil || before.Historical {
				t.Fatalf("current citation: %+v %v", before, err)
			}
			entries, err := s.store.ListSyncedObjectEntriesForNotesRoots(t.Context(), object.NotesSourceRootID)
			if err != nil || len(entries) != 1 {
				t.Fatalf("sync entry: %+v %v", entries, err)
			}
			e := entries[0]
			body := before.Text + "\nA revised source with a different conclusion.\n"
			file := filepath.Join(t.TempDir(), "changed.md")
			if err := os.WriteFile(file, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			blob, version, hash := ids.NewBlobID(), ids.NewObjectVersionID(), hashArtifactValue(body)
			if _, err := s.store.db.Exec(`INSERT INTO files.blobs(blob_id,hash_algorithm,hash_hex,hash_uri,size_bytes,storage_path,status) VALUES ($1,'sha256',$2,$3,$4,$5,'verified')`, blob, strings.TrimPrefix(hash, "sha256:"), hash, len(body), file); err != nil {
				t.Fatal(err)
			}
			if _, err := s.store.db.Exec(`INSERT INTO objects.object_versions(object_version_id,object_id,version_number,blob_id,content_hash,source_node_id,source_path,size_bytes,mime_type,status,metadata) VALUES ($1,$2,2,$3,$4,$5,$6,$7,'text/markdown','active','{"file_class":"markdown"}')`, version, e.ObjectID, blob, hash, e.SourceNodeID, e.SourcePath, len(body)); err != nil {
				t.Fatal(err)
			}
			if _, err := s.store.db.Exec(`UPDATE files.file_metadata SET latest_version_id=$1 WHERE object_id=$2`, version, e.ObjectID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.store.db.Exec(`UPDATE objects.object_versions SET status='superseded' WHERE object_version_id=$1`, e.ObjectVersionID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.store.db.Exec(`UPDATE sync.replicas SET replicated_id=$1,storage_ref=$2 WHERE replica_id=$3`, version, hash, e.ReplicaID); err != nil {
				t.Fatal(err)
			}
			// Even historical content is unavailable until current admission catches up.
			if _, err := s.GetNotesPassage(t.Context(), citation); err != sql.ErrNoRows {
				t.Fatalf("stale observation accessible: %v", err)
			}
			reconcileBoxSyncedFixture(t, s, roots)
			advanceBoxSyncedFixture(t, s)
			after, err := s.GetNotesPassage(t.Context(), citation)
			if err != nil || !after.Historical || after.Text != before.Text {
				t.Fatalf("historical citation: %+v %v", after, err)
			}
			if _, err := s.store.db.Exec(`UPDATE files.file_metadata SET index_policy='private_no_index' WHERE object_id=$1`, e.ObjectID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetNotesPassage(t.Context(), citation); err != sql.ErrNoRows {
				t.Fatalf("historical privacy bypass: %v", err)
			}
			if _, err := s.store.db.Exec(`UPDATE files.file_metadata SET index_policy='text_later' WHERE object_id=$1`, e.ObjectID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.store.db.Exec(`UPDATE knowledge.notes_source_roots SET status='blocked' WHERE notes_source_root_id=$1`, object.NotesSourceRootID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetNotesPassage(t.Context(), citation); err != sql.ErrNoRows {
				t.Fatalf("historical root-policy bypass: %v", err)
			}
			if _, err := s.store.db.Exec(`UPDATE knowledge.notes_source_roots SET status='active' WHERE notes_source_root_id=$1`, object.NotesSourceRootID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNotesPassageBindingValidation(t *testing.T) {
	valid := NotesPassageInput{KnowledgeObjectID: ids.NewKnowledgeObjectID(), KnowledgeObjectVersionID: ids.NewKnowledgeObjectVersionID(), KnowledgeChunkID: ids.NewKnowledgeChunkID(), SourceHash: "sha256:" + strings.Repeat("a", 64)}
	if err := ValidateNotesPassageInput(valid); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*NotesPassageInput){
		func(v *NotesPassageInput) { v.SourceLifecycle = "unknown" },
		func(v *NotesPassageInput) { v.KnowledgeObjectID = "latest" },
		func(v *NotesPassageInput) { v.KnowledgeObjectVersionID = v.KnowledgeObjectID },
		func(v *NotesPassageInput) { v.KnowledgeChunkID = "../file" },
		func(v *NotesPassageInput) { v.SourceHash = "" },
		func(v *NotesPassageInput) { v.SourceHash = " " },
		func(v *NotesPassageInput) { v.SourceHash = "sha256:wrong" },
	} {
		input := valid
		change(&input)
		_, err := NewService(nil).GetNotesPassage(t.Context(), input)
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid binding reached DB: %#v %v", input, err)
		}
	}
}

func TestNotesPassageUntruncatedBoundsAndIntegrity(t *testing.T) {
	text := strings.Repeat("\u03bb", MaxNotesPassageTextBytes/2)
	if !validNotesPassage(text, hashChunkText(1, text), 1, "page:1") {
		t.Fatal("exact Unicode byte bound refused")
	}
	for _, item := range []struct {
		text, hash, locator string
		index               int
	}{
		{text + "x", hashChunkText(1, text+"x"), "", 1},
		{text, "sha256:" + strings.Repeat("0", 64), "", 1},
		{text, hashChunkText(1, text), "", 2},
		{" ", hashChunkText(1, " "), "", 1},
		{"\xff", hashChunkText(1, "\xff"), "", 1},
		{text, hashChunkText(1, text), strings.Repeat("x", 4097), 1},
	} {
		if validNotesPassage(item.text, item.hash, item.index, item.locator) {
			t.Fatal("invalid or oversized passage accepted")
		}
	}
}

func TestNotesPassageCurrentPrivacyAllRootKinds(t *testing.T) {
	for _, kind := range []string{RootKindBoxNotes, RootKindProjectNotes, RootKindBoxTopics, RootKindBoxLibrary, RootKindProjectMaterial} {
		root := SourceRoot{RootKind: kind}
		if !notesPassageSourceAllowed(root, "note.md", json.RawMessage(`{}`)) {
			t.Fatalf("eligible %s refused", kind)
		}
		for _, path := range []string{".env", ".env.local", "credentials/key.md", ".hermes/memory.md", "private_no_index/note.md", "loom.notes.yaml"} {
			if notesPassageSourceAllowed(root, path, json.RawMessage(`{}`)) {
				t.Fatalf("%s leaked %s", kind, path)
			}
		}
		for _, field := range []string{"storage_metadata", "file_metadata", "object_metadata", "version_metadata"} {
			for _, policy := range []string{`{"private_no_index":true}`, `{"index_policy":"private_no_index"}`, `[]`} {
				if notesPassageSourceAllowed(root, "note.md", json.RawMessage(`{"`+field+`":`+policy+`}`)) {
					t.Fatalf("privacy leaked %s %s", kind, field)
				}
			}
		}
	}
}
