package knowledge

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	lexical "loom.local/loom/internal/search"
)

func TestIndexedNotesFileSelectionPreservesFullPoolRanking(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	for _, mode := range []string{NotesSearchModeLexical, NotesSearchModeSemantic, NotesSearchModeHybrid} {
		for _, order := range []string{NotesSearchSortRelevance, NotesSearchSortOldest, NotesSearchSortNewest} {
			t.Run(mode+"/"+order, func(t *testing.T) {
				input := NotesSearchInput{Query: "thermal question", fileView: true, discoveryLimit: 2, Limit: 12,
					RequireCurrent: true, SourceLifecycle: SourceLifecycleFilterAll, Mode: mode, Sort: order}
				state := &notesIndexedSearch{lexical: map[notesLexicalVariant][]string{}, semantic: map[notesLexicalVariant][]string{},
					lexicalScores: map[string]float64{}, lexicalFallback: map[string]bool{}, semanticDistances: map[string]float64{},
					semanticCoverage: map[string]notesSemanticCoverage{}, documentObjects: map[string]string{}, visible: map[string]bool{},
					exhausted: map[notesLexicalVariant]bool{}}
				input.indexed = state
				metadata := map[string]NotesSearchResult{}
				want := map[notesLexicalVariant][]NotesSearchResult{}
				for _, key := range notesLexicalVariants(input) {
					var lexicalResults, semanticResults []NotesSearchResult
					for i := 0; i < 20; i++ {
						id := fmt.Sprintf("%s-%02d", key.Lifecycle, i)
						file := fmt.Sprintf("file-%02d", i)
						if i < 3 {
							file = "shared-canonical-file"
						}
						r := NotesSearchResult{SearchDocumentID: id, KnowledgeObjectID: id, SourceKind: KnowledgeSearchSourceKind,
							FileID: file, RelativePath: id, RecencyAt: now.Add(-time.Duration(20-i) * 24 * time.Hour)}
						if i == 4 {
							r.MetadataOnly = true
						}
						metadata[id] = r
						state.documentObjects[id], state.visible[id] = id, i != 1
						state.semanticCoverage[id] = notesSemanticCoverageUnavailable
						if i == 6 {
							state.semanticCoverage[id] = notesSemanticCoverageUnknown
						}
						if mode != NotesSearchModeSemantic && i < 16 {
							state.lexical[key] = append(state.lexical[key], id)
							state.lexicalScores[id] = float64(20 - i/2)
							state.lexicalFallback[id] = i >= 12
							if state.visible[id] {
								l := r
								l.RankScore, l.FinalScore = state.lexicalScores[id], state.lexicalScores[id]
								l.LexicalRank = len(lexicalResults) + 1
								annotateIndexedNotesLexicalResult(input, &l)
								lexicalResults = append(lexicalResults, l)
							}
						}
						if mode != NotesSearchModeLexical && i >= 8 {
							state.semantic[key] = append(state.semantic[key], id)
							state.semanticDistances[id] = float64(i/2) / 20
							if state.visible[id] {
								s := r
								s.SemanticRank = len(semanticResults) + 1
								s.SemanticDistance = state.semanticDistances[id]
								s.SemanticScore = lexical.VectorDistanceScore(s.SemanticDistance)
								s.RankScore, s.FinalScore, s.SemanticCoverage = s.SemanticScore, s.SemanticScore, notesSemanticCoverageAvailable
								semanticResults = append(semanticResults, s)
							}
						}
					}
					// Use the existing eager ranking path as the oracle, with enough
					// space for every object before canonical file deduplication.
					old := input
					old.fileView, old.Limit = false, 50
					var ranked []NotesSearchResult
					switch mode {
					case NotesSearchModeHybrid:
						ranked = fuseNotesSearchResults(lexicalResults, semanticResults, old, now)
					case NotesSearchModeSemantic:
						ranked = rerankNotesSearchResults(semanticResults, old, now, mode)
					default:
						ranked = rerankNotesSearchResults(lexicalResults, old, now, mode)
					}
					want[key] = groupNotesSearchResultsByFile(ranked, input.discoveryLimit+4, true)
					state.exhausted[key] = true
				}
				state.filePartitions = selectNotesFileCandidates(input, mode, now, metadata)
				for key, part := range state.filePartitions {
					if !reflect.DeepEqual(part.results, want[key]) {
						t.Fatalf("ranking changed for %+v:\ngot=%+v\nwant=%+v", key, part.results, want[key])
					}
					if !part.truncated || part.matches <= len(part.results) {
						t.Fatal("hydration reduction erased candidate qualification or match count")
					}
				}
				seen := map[string]bool{}
				for _, semantic := range []bool{false, true} {
					raw, err := notesIndexedWantedJSON(input, notesLexicalVariants(input), semantic)
					if err != nil {
						t.Fatal(err)
					}
					var wanted []notesIndexedWanted
					if err := json.Unmarshal([]byte(raw), &wanted); err != nil {
						t.Fatal(err)
					}
					for _, w := range wanted {
						if seen[w.ID] {
							t.Fatal("shared hit hydrated twice")
						}
						seen[w.ID] = true
					}
				}
				if len(seen) != 2*(input.discoveryLimit+4) {
					t.Fatalf("hydration was not bounded by selected files: %d", len(seen))
				}
			})
		}
	}
}

func TestIndexedNotesFileHydrationPreservesAttributionAndFollowup(t *testing.T) {
	key := notesLexicalVariant{SourceLifecycleFilterActive, true, 4}
	followup := &NotesPassageInput{KnowledgeObjectID: "object", KnowledgeObjectVersionID: "version", KnowledgeChunkID: "chunk"}
	selected := NotesSearchResult{SearchDocumentID: "doc", KnowledgeObjectID: "object", FileID: "canonical",
		LexicalRank: 8, SemanticRank: 12, RankScore: .07, FinalScore: .07, RecencyRank: 18, RecencyScore: .3,
		SemanticScore: .8, SemanticDistance: .2, SemanticCoverage: notesSemanticCoverageAvailable}
	hydrated := NotesSearchResult{SearchDocumentID: "doc", KnowledgeObjectID: "object", Snippet: "Actual source passage.",
		LexicalMethod: "pg_search_bm25", BM25Score: 24.5, PassageFollowup: followup, MetadataOnly: true}
	input := NotesSearchInput{fileView: true, SourceLifecycle: key.Lifecycle, RequireCurrent: key.Current, Limit: key.Limit,
		indexed:      &notesIndexedSearch{filePartitions: map[notesLexicalVariant]notesSearchPartition{key: {results: []NotesSearchResult{selected}, truncated: true, matches: 20}}},
		lexicalBatch: map[notesLexicalVariant][]NotesSearchResult{key: {hydrated}}}
	part := selectedNotesFilePartition(input, "")
	if len(part.results) != 1 || !part.truncated || part.matches != 20 {
		t.Fatalf("lost partition evidence: %+v", part)
	}
	r := part.results[0]
	if r.LexicalMethod != hydrated.LexicalMethod || r.BM25Score != 24.5 || r.FTSScore != 0 || r.PassageFollowup != followup || r.Snippet != hydrated.Snippet || !r.MetadataOnly {
		t.Fatalf("selection overwrote source or backend fields: %+v", r)
	}
	if r.FinalScore != selected.FinalScore || r.LexicalRank != 8 || r.SemanticRank != 12 || r.RecencyRank != 18 || r.SemanticDistance != .2 || r.SemanticCoverage != notesSemanticCoverageAvailable {
		t.Fatalf("selection reranked the reduced pool: %+v", r)
	}
	if notesFileHydratesLane(input, true) || !notesFileHydratesLane(input, false) {
		t.Fatal("shared hits need only lexical hydration")
	}
	input.queryEmbedding = &notesQueryEmbedding{loaded: true, err: &EmbeddingRuntimeError{Kind: EmbeddingRuntimeErrorUnavailable}}
	if part := selectedNotesFilePartition(input, ""); part.fallback != notesSearchFallbackRuntimeUnavailable || !part.truncated {
		t.Fatal("selected file results hid the semantic fallback or bounded notice")
	}
	input.lexicalBatch = nil
	if part := selectedNotesFilePartition(input, ""); len(part.results) != 0 || !part.truncated {
		t.Fatal("unhydrated ranking input escaped as a result")
	}
}

func TestIndexedNotesFileCandidateQueryHasNoPassageHydration(t *testing.T) {
	key := notesLexicalVariant{SourceLifecycleFilterActive, true, 4}
	input := NotesSearchInput{fileView: true, SourceLifecycle: key.Lifecycle, RequireCurrent: key.Current, Limit: key.Limit,
		indexed: &notesIndexedSearch{lexical: map[notesLexicalVariant][]string{key: {"good", "private"}},
			documentObjects: map[string]string{"good": "one", "private": "two"}, visible: map[string]bool{"one": true}}}
	query, err := buildNotesFileCandidatesQuery(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"chunk_text", "sd.body", "ts_headline", "source_context", "embedding <=>"} {
		if strings.Contains(query.SQL, forbidden) {
			t.Fatalf("lightweight selection reads %s", forbidden)
		}
	}
	if strings.Contains(query.Args[0].(string), "private") || !strings.Contains(query.SQL, "canonical_file_id") || !strings.Contains(query.SQL, "COALESCE(sd.recency_at,ko.recency_at)") {
		t.Fatal("ranking input lost admission, identity or recency")
	}
}

func TestIndexedNotesAdmissionFactorsExactObjectsAndRootAuthority(t *testing.T) {
	var args []any
	query := notesSearchObjectsSQL(NotesSearchInput{SourceLifecycle: SourceLifecycleFilterAll, candidateObjectIDsJSON: `["one","two"]`}, &args)
	for _, required := range []string{"requested_objects AS MATERIALIZED", "ko.knowledge_object_id=requested.id",
		"SELECT notes_source_root_id FROM requested_objects", "authorized_roots AS MATERIALIZED", "FROM requested_objects ko",
		notesStatementVisibleObjectSQL("ko"), notesObservedSourcePresentSQL("ko"), visibleNotesKnowledgeRelativePathSQL("ko.relative_path")} {
		if !strings.Contains(query, required) {
			t.Fatalf("exact admission lost %s", required)
		}
	}
	if strings.Count(query, "AS statement_authority") != 1 || strings.Count(query, "projects.declaration_watch_read_sources(") != 1 || len(args) != 1 {
		t.Fatal("root proof or source lookup was duplicated")
	}
}

func TestIndexedNotesUnmovedAdmissionKeepsAllActiveFences(t *testing.T) {
	var args []any
	input := NotesSearchInput{SourceLifecycle: SourceLifecycleFilterAll,
		candidateObjectIDsJSON: `["one","two"]`, indexedNoCustody: true}
	query := notesSearchObjectsSQL(input, &args)
	for _, required := range []string{notesStatementUnmovedObjectSQL("ko"),
		notesCustodyReadCaughtUpSQL("ko"), "NOT " + notesHasCustodySQL("ko"),
		notesObservedSourcePresentSQL("ko"), visibleNotesKnowledgeRelativePathSQL("ko.relative_path"),
		"authorized_roots AS MATERIALIZED", "AS statement_authority"} {
		if !strings.Contains(query, required) {
			t.Fatalf("unmoved admission lost %s", required)
		}
	}
	input.indexedNoCustody = false
	full := notesSearchObjectsSQL(input, &args)
	if len(query) >= len(full) || !strings.Contains(full, notesStatementVisibleObjectSQL("ko")) {
		t.Fatal("custody branch not selected exactly")
	}
}
