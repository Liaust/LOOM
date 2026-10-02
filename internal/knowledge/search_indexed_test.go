package knowledge

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
)

func TestIndexedNotesRetrievalQueryShape(t *testing.T) {
	input := NotesSearchInput{Query: "quantum mechanics", Mode: NotesSearchModeHybrid, SourceLifecycle: SourceLifecycleFilterActive, RequireCurrent: true, Limit: 10}
	variants := notesLexicalVariants(input)
	lexical, err := buildIndexedNotesLexicalCandidates(input, variants)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"sd.tsv @@ nq.query", "sd.notes_fields LIKE", "lower(sd.body) LIKE", "sd.metadata->'tags'", "source_lifecycle=request.lifecycle", "NOT request.require_current OR publication_current", "LIMIT request.candidate_limit"} {
		if !strings.Contains(lexical.SQL, fragment) {
			t.Fatalf("missing %s", fragment)
		}
	}
	for _, forbidden := range []string{"corpus_stats", "average_document_length", "visible_objects AS MATERIALIZED", "bm25_scores"} {
		if strings.Contains(lexical.SQL, forbidden) {
			t.Fatalf("unbounded preparation: %s", forbidden)
		}
	}
	semantic, err := buildIndexedNotesSemanticCandidates(input, EmbeddingSettings{Dimensions: 3}, []float32{1, 2, 3}, variants)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(semantic.SQL, "ORDER BY embedding <=> $1::vector LIMIT 160") || strings.Contains(semantic.SQL, "semantic_candidates AS MATERIALIZED") {
		t.Fatal("global retrieval must expose the distance/LIMIT directly to HNSW")
	}
	if !strings.Contains(semantic.SQL, "ORDER BY (ce.embedding <=> $1::vector) + 0.0") {
		t.Fatal("archive omission must use exact scoped retrieval")
	}
	input.ProjectID = "project_test"
	semantic, err = buildIndexedNotesSemanticCandidates(input, EmbeddingSettings{Dimensions: 3}, []float32{1, 2, 3}, variants)
	if err != nil || strings.Contains(semantic.SQL, "FROM ann ce") {
		t.Fatal("explicit scopes need exact retrieval rather than post-filtered global ANN")
	}
}

func TestIndexedNotesRefillUsesMergedVisibleObjects(t *testing.T) {
	key := notesLexicalVariant{SourceLifecycleFilterActive, true, 2}
	state := &notesIndexedSearch{
		lexical:   map[notesLexicalVariant][]string{key: {"a", "duplicate"}},
		semantic:  map[notesLexicalVariant][]string{key: {"b", "denied"}},
		exhausted: map[notesLexicalVariant]bool{key: true},
	}
	objects := map[string]string{"a": "one", "duplicate": "one", "b": "two", "denied": "private"}
	visible := map[string]bool{"one": true, "two": true}
	if notesIndexedNeedsRefill(state, key, objects, visible, 2) {
		t.Fatal("full hybrid page must not refill a smaller lexical lane")
	}
	delete(visible, "two")
	if !notesIndexedNeedsRefill(state, key, objects, visible, 2) {
		t.Fatal("duplicate chunks and denied objects must not fill the page")
	}
	state.exhausted[key] = false
	if notesIndexedNeedsRefill(state, key, objects, visible, 2) {
		t.Fatal("exact exhausted result set must not refill")
	}
}

func TestIndexedNotesLifecycleAndAdmissionPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	search := func(query string, lifecycle SourceLifecycleFilter) NotesSearchResultSet {
		t.Helper()
		result, err := f.s.SearchNotes(t.Context(), NotesSearchInput{Query: query, Mode: NotesSearchModeLexical, SourceLifecycle: lifecycle, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := search("cobalt observatory", SourceLifecycleFilterAll)
	if before.ResultCount != 5 {
		t.Fatalf("initial match count: %+v", before)
	}
	if search("obalt", SourceLifecycleFilterAll).ResultCount != 5 || search("co", SourceLifecycleFilterAll).ResultCount != 5 || search("totally_nonexistent_notes_identifier", SourceLifecycleFilterAll).ResultCount != 0 {
		t.Fatal("substring/short-term/no-match compatibility")
	}
	f.archive(t)
	if got := search("cobalt observatory", SourceLifecycleFilterAll).ResultCount; got != 3 {
		t.Fatalf("unprojected archive leaked %d results", got)
	}
	if _, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID); err != nil {
		t.Fatal(err)
	}
	archived := search("cobalt observatory", SourceLifecycleFilterArchived)
	if archived.ResultCount != 2 {
		t.Fatalf("archive results: %+v", archived)
	}
	for _, result := range archived.Results {
		if result.SourceLifecycle != SourceLifecycleArchived || !result.ValidPassageFollowup() {
			t.Fatalf("invalid archived result: %+v", result)
		}
		if _, err := f.s.GetNotesPassage(t.Context(), *result.PassageFollowup); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.s.store.db.Exec(`UPDATE files.file_metadata SET index_policy='private_no_index' WHERE object_id=(SELECT metadata->'synced_object'->>'object_id' FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1)`, archived.Results[0].KnowledgeObjectID); err != nil {
		t.Fatal(err)
	}
	if search("cobalt observatory", SourceLifecycleFilterArchived).ResultCount != 1 {
		t.Fatal("private source remained searchable without reindexing")
	}
}

func TestIndexedNotesSemanticPublicationPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	seedNotesArchiveSearchEmbeddings(t, f)
	settings, err := f.s.store.GetEmbeddingSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	vector := make([]float32, settings.Dimensions)
	for i := range vector {
		vector[i] = 1
	}
	input := NotesSearchInput{Query: "cobalt observatory", Mode: NotesSearchModeHybrid, SourceLifecycle: SourceLifecycleFilterAll, RequireCurrent: true, Limit: 10, queryEmbedding: &notesQueryEmbedding{loaded: true, vector: vector}}
	tx, err := f.s.store.db.BeginTx(t.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	input.readTx = tx
	if err := f.s.prepareIndexedNotesSearch(t.Context(), &input, input.Mode, "", settings); err != nil {
		t.Fatal(err)
	}
	if input.indexed == nil || input.candidateObjectIDsJSON == "" {
		t.Fatal("indexed reader was not selected")
	}
	batch, err := f.s.loadNotesSemanticBatch(t.Context(), input, settings, vector)
	if err != nil {
		t.Fatal(err)
	}
	for key, got := range batch {
		legacy := input
		legacy.indexed, legacy.semanticBatch = nil, nil
		legacy.visibleObjectIDsJSON, legacy.candidateObjectIDsJSON = "", ""
		legacy.SourceLifecycle, legacy.RequireCurrent, legacy.Limit = key.Lifecycle, key.Current, key.Limit
		want, err := f.s.searchSemanticNotesVector(t.Context(), legacy, settings, vector)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("publication/followup parity: key=%+v got=%+v want=%+v err=%v", key, got, want, err)
		}
	}
}
