package knowledge

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/storagecatalog"
)

func TestNotesSearchFollowupFusionKeepsWholeCandidate(t *testing.T) {
	now := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	lex := notesFollowupResultFixture()
	lex.SearchDocumentID, lex.FinalScore, lex.LexicalRank, lex.RecencyAt = "shared", 10, 1, now
	lex.PassageFollowup = notesSearchPassageFollowup(lex, "sha256:"+strings.Repeat("a", 64), false)
	sem := lex
	sem.KnowledgeObjectVersionID, sem.KnowledgeChunkID = ids.NewKnowledgeObjectVersionID(), ids.NewKnowledgeChunkID()
	sem.SemanticRank, sem.SemanticScore = 1, 0.9
	sem.PassageFollowup = notesSearchPassageFollowup(sem, "sha256:"+strings.Repeat("b", 64), false)
	only := notesFollowupResultFixture()
	only.SearchDocumentID, only.SemanticRank, only.SemanticScore, only.RecencyAt = "semantic-only", 2, 0.8, now
	only.SourceLifecycle = SourceLifecycleArchived
	only.PassageFollowup = notesSearchPassageFollowup(only, "sha256:"+strings.Repeat("c", 64), false)
	before, _ := json.Marshal([]NotesSearchResult{lex, sem, only})
	input := NotesSearchInput{Limit: 10, Sort: NotesSearchSortRelevance}
	got := fuseNotesSearchResults([]NotesSearchResult{lex}, []NotesSearchResult{sem, only}, input, now)
	got = groupNotesSearchResults(rerankNotesSearchResults(got, input, now, NotesSearchModeHybrid), input.Limit)
	if len(got) != 2 {
		t.Fatalf("changed result selection: %+v", got)
	}
	wants := map[string]*NotesPassageInput{"shared": lex.PassageFollowup, "semantic-only": only.PassageFollowup}
	for _, r := range got {
		if !r.ValidPassageFollowup() || !reflect.DeepEqual(r.PassageFollowup, wants[r.SearchDocumentID]) {
			t.Fatalf("fusion spliced or lost tuple: %+v", r)
		}
		if r.SearchDocumentID == "shared" && (r.LexicalRank != 1 || r.SemanticRank != 1 || !strings.Contains(strings.Join(r.MatchReasons, ","), "semantic")) {
			t.Fatal("fusion metadata changed")
		}
	}
	after, _ := json.Marshal([]NotesSearchResult{lex, sem, only})
	if string(before) != string(after) {
		t.Fatal("input candidates mutated")
	}
}

func TestNotesSearchExactSemanticVersionProjection(t *testing.T) {
	query, err := buildSemanticNotesSearchQuery(NotesSearchInput{Query: "cobalt"}, EmbeddingSettings{}, []float32{1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query.SQL, "kov.source_hash, '') AS passage_source_hash") || strings.Count(query.SQL, "passage_source_hash") != 2 {
		t.Fatal("semantic result does not select the matched version hash")
	}
}

func TestBoxSourceSemanticFilterAndVisibilityParity(t *testing.T) {
	query, err := buildSemanticNotesSearchQuery(NotesSearchInput{Query: `"write contention"`, SourceCategory: "library"}, EmbeddingSettings{}, []float32{1})
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"WHEN 'box_library' THEN 'library'", "visibility_root.status = 'active'", "visibility_file.index_policy IS DISTINCT FROM 'private_no_index'", "visibility_entry.availability_state = 'available'", "source_context_root", "strpos("} {
		if !strings.Contains(query.SQL, part) {
			t.Fatalf("semantic parity missing %s", part)
		}
	}
	if _, err := buildSemanticNotesSearchQuery(NotesSearchInput{Query: "x", SourceCategory: "everything"}, EmbeddingSettings{}, []float32{1}); err == nil {
		t.Fatal("invalid semantic category accepted")
	}
}

func TestBuildSemanticNotesSearchQueryUsesCurrentOnlyActiveEmbeddingsAndFilters(t *testing.T) {
	query, err := buildSemanticNotesSearchQuery(NotesSearchInput{
		Query:             "threat intel",
		ProjectID:         ids.NewProjectID(),
		SourceNodeKey:     "main",
		NotesSourceRootID: ids.NewNotesSourceRootID(),
		FileClass:         storagecatalog.FileClassMarkdown,
		Path:              "reports",
		Tags:              []string{"OSINT"},
		Limit:             5,
	}, EmbeddingSettings{
		RuntimeKey: EmbeddingRuntimeOllama,
		ModelKey:   EmbeddingModelMXBAIEmbedLarge,
		Dimensions: DefaultEmbeddingDimensions,
	}, []float32{0.1, 0.2, 0.3})
	if err != nil {
		t.Fatalf("buildSemanticNotesSearchQuery returned error: %v", err)
	}
	for _, want := range []string{
		"ce.embedding <=> $1::vector",
		"ce.active = true",
		"ce.status = 'active'",
		"ce.knowledge_chunk_id IS NOT NULL",
		"sd.source_kind = 'knowledge_chunk'",
		"sd.index_version = 'knowledge_bm25_v1'",
		"ce.knowledge_object_version_id = kc.knowledge_object_version_id",
		"loom.notes.yaml",
		"ko.project_id =",
		"ko.source_node_key =",
		"ko.notes_source_root_id =",
		"ko.file_class =",
		"ko.relative_path ILIKE",
		"(sd.metadata->'tags') ?",
		"ORDER BY semantic_distance ASC",
		"LIMIT $11",
	} {
		if !strings.Contains(query.SQL, want) {
			t.Fatalf("semantic SQL missing %q:\n%s", want, query.SQL)
		}
	}
	if len(query.Args) != 11 {
		t.Fatalf("args len = %d, want 11: %#v", len(query.Args), query.Args)
	}
	if query.Args[0] != "[0.1,0.2,0.3]" || query.Args[1] != EmbeddingRuntimeOllama || query.Args[2] != EmbeddingModelMXBAIEmbedLarge || query.Args[3] != DefaultEmbeddingDimensions {
		t.Fatalf("embedding args = %#v", query.Args[:4])
	}
	if query.Args[10] != 40 {
		t.Fatalf("candidate limit = %#v, want 40", query.Args[10])
	}
}

func TestBuildSemanticNotesSearchQueryRejectsEmptyVector(t *testing.T) {
	_, err := buildSemanticNotesSearchQuery(NotesSearchInput{Query: "loom"}, EmbeddingSettings{}, nil)
	if err == nil {
		t.Fatal("buildSemanticNotesSearchQuery returned nil error")
	}
}

func TestFuseNotesSearchResultsUsesRRFAndMergesSemanticReason(t *testing.T) {
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	lexicalResults := []NotesSearchResult{
		{
			SearchDocumentID:  "search_document_a",
			KnowledgeObjectID: "knowledge_object_a",
			KnowledgeChunkID:  "knowledge_chunk_a",
			RelativePath:      "a.md",
			FinalScore:        10,
			LexicalRank:       1,
			MatchReasons:      []string{"title"},
			IndexedAt:         now,
		},
		{
			SearchDocumentID:  "search_document_b",
			KnowledgeObjectID: "knowledge_object_b",
			KnowledgeChunkID:  "knowledge_chunk_b",
			RelativePath:      "b.md",
			FinalScore:        8,
			LexicalRank:       2,
			MatchReasons:      []string{"body"},
			IndexedAt:         now,
		},
	}
	semanticResults := []NotesSearchResult{
		{
			SearchDocumentID:  "search_document_b",
			KnowledgeObjectID: "knowledge_object_b",
			KnowledgeChunkID:  "knowledge_chunk_b",
			RelativePath:      "b.md",
			SemanticRank:      1,
			SemanticDistance:  0.2,
			SemanticScore:     0.83,
			RecencyAt:         now,
			MatchReasons:      []string{"semantic"},
			IndexedAt:         now,
		},
	}
	lexicalResults[0].RecencyAt = now.Add(-24 * time.Hour)
	lexicalResults[1].RecencyAt = now
	results := fuseNotesSearchResults(lexicalResults, semanticResults, NotesSearchInput{Limit: 10}, now)
	if len(results) != 2 {
		t.Fatalf("result count = %d, want 2: %#v", len(results), results)
	}
	if results[0].SearchDocumentID != "search_document_b" {
		t.Fatalf("top result = %#v, want semantic+lexical overlap first", results[0])
	}
	if results[0].LexicalRank != 2 || results[0].SemanticRank != 1 {
		t.Fatalf("merged ranks = lexical %d semantic %d", results[0].LexicalRank, results[0].SemanticRank)
	}
	if strings.Join(results[0].MatchReasons, ",") != "body,semantic" {
		t.Fatalf("match reasons = %#v", results[0].MatchReasons)
	}
	if results[0].FinalScore <= 0 || results[0].FinalScore == lexicalResults[1].FinalScore {
		t.Fatalf("final score should be fused, got %.6f", results[0].FinalScore)
	}
}

func TestNotesSearchModeNormalizationAndCandidateLimit(t *testing.T) {
	if got := normalizeNotesSearchMode(" HYBRID "); got != NotesSearchModeHybrid {
		t.Fatalf("mode = %q", got)
	}
	if got := notesSearchCandidateLimit(7); got != 56 {
		t.Fatalf("candidate limit = %d, want 56", got)
	}
	if got := notesSearchCandidateLimit(80); got != 80 {
		t.Fatalf("clamped candidate limit = %d, want 80", got)
	}
}
