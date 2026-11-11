package knowledge

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

func TestNotesSearchCoverageLookupIsBoundedAndVersionQualified(t *testing.T) {
	key := notesLexicalVariant{SourceLifecycleFilterActive, false, 1}
	input := NotesSearchInput{SourceLifecycle: SourceLifecycleFilterActive, Limit: 1, indexed: &notesIndexedSearch{
		lexical:         map[notesLexicalVariant][]string{key: {"allowed", "private"}},
		documentObjects: map[string]string{"allowed": "one", "private": "two"}, visible: map[string]bool{"one": true},
	}}
	query, err := buildNotesSemanticCoverageQuery(input, EmbeddingSettings{RuntimeKey: "runtime", ModelKey: "model", Dimensions: 3})
	if err != nil {
		t.Fatal(err)
	}
	var wanted []notesIndexedWanted
	if err := json.Unmarshal([]byte(query.Args[0].(string)), &wanted); err != nil || len(wanted) != 1 || wanted[0].ID != "allowed" {
		t.Fatalf("coverage escaped admitted hydration candidates: %v %v", wanted, err)
	}
	for _, required := range []string{"FROM wanted w JOIN", "ce.knowledge_chunk_id=kc.knowledge_chunk_id", "ce.knowledge_object_version_id=kc.knowledge_object_version_id", "ce.chunk_hash=kc.chunk_hash", "ce.chunker_version=kc.chunker_version", "ce.runtime_key=$2 AND ce.model_key=$3 AND ce.dimensions=$4", "ko.semantic_version_id", "ko.semantic_runtime_key", "THEN 'unknown'", "THEN 'available'", "ELSE 'unavailable'"} {
		if !strings.Contains(query.SQL, required) {
			t.Fatalf("coverage lacks %q", required)
		}
	}
	if strings.Contains(query.SQL, "count(*)") || strings.Contains(query.SQL, "embedding <=>") || strings.Contains(query.SQL, "sd.body") {
		t.Fatal("coverage became corpus/ANN/body work")
	}
}

func TestNotesSearchCoveragePublicationAndVectorPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	seedNotesArchiveSearchEmbeddings(t, f)
	settings, err := f.s.store.GetEmbeddingSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var document, object, chunk string
	if err := f.s.store.db.QueryRow(`SELECT sd.search_document_id,sd.notes_object_id,sd.source_id
 FROM search.search_documents sd WHERE sd.source_kind='knowledge_chunk' ORDER BY sd.search_document_id LIMIT 1`).Scan(&document, &object, &chunk); err != nil {
		t.Fatal(err)
	}
	key := notesLexicalVariant{SourceLifecycleFilterActive, false, 1}
	input := NotesSearchInput{SourceLifecycle: SourceLifecycleFilterActive, Limit: 1, indexed: &notesIndexedSearch{
		lexical:         map[notesLexicalVariant][]string{key: {document}},
		documentObjects: map[string]string{document: object}, visible: map[string]bool{object: true},
	}}
	check := func(want notesSemanticCoverage, settings EmbeddingSettings) {
		t.Helper()
		tx, err := f.s.store.db.BeginTx(t.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		input.readTx = tx
		got, err := f.s.loadNotesSemanticCoverage(t.Context(), input, settings)
		if err != nil || got[document] != want {
			t.Fatalf("coverage = %v, want %s: %v", got, want, err)
		}
	}
	// No semantic Top-K was retrieved: compatible publication alone establishes it.
	check(notesSemanticCoverageAvailable, settings)
	incompatible := settings
	incompatible.ModelKey = "different-model"
	check(notesSemanticCoverageUnavailable, incompatible)
	if _, err := f.s.store.db.Exec(`UPDATE knowledge.chunk_embeddings SET active=false WHERE knowledge_chunk_id=$1`, chunk); err != nil {
		t.Fatal(err)
	}
	check(notesSemanticCoverageUnavailable, settings)
	if _, err := f.s.store.db.Exec(`UPDATE knowledge.chunk_embeddings SET active=true,chunk_hash='sha256:' || repeat('0',64) WHERE knowledge_chunk_id=$1`, chunk); err != nil {
		t.Fatal(err)
	}
	check(notesSemanticCoverageUnavailable, settings)
	if _, err := f.s.store.db.Exec(`UPDATE knowledge.chunk_embeddings ce SET chunk_hash=kc.chunk_hash
 FROM knowledge.knowledge_chunks kc WHERE ce.knowledge_chunk_id=kc.knowledge_chunk_id AND kc.knowledge_chunk_id=$1`, chunk); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.store.db.Exec(`UPDATE knowledge.knowledge_objects SET semantic_version_id=NULL WHERE knowledge_object_id=$1`, object); err != nil {
		t.Fatal(err)
	}
	check(notesSemanticCoverageUnavailable, settings)
	if _, err := f.s.store.db.Exec(`UPDATE search.search_documents SET metadata=metadata || '{"metadata_only":true}'::jsonb WHERE search_document_id=$1`, document); err != nil {
		t.Fatal(err)
	}
	check(notesSemanticCoverageUnknown, settings)
}
