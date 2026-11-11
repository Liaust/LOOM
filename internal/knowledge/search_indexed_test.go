package knowledge

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"loom.local/loom/internal/boxregistry"
)

func TestIndexedNotesAdmissionReusesPositiveAndNegativeSnapshotResults(t *testing.T) {
	cache := &notesIndexedAdmission{checked: map[string]bool{}}
	var lookedUp [][]string
	lookup := func(ids []string) ([]string, error) {
		lookedUp = append(lookedUp, append([]string(nil), ids...))
		visible := []string{}
		for _, id := range ids {
			if id != "withdrawn" {
				visible = append(visible, id)
			}
		}
		return visible, nil
	}
	for _, ids := range [][]string{{"one", "withdrawn"}, {"one", "two", "withdrawn"}, {"one", "two", "withdrawn"}} {
		if _, err := cache.admit(ids, lookup); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(lookedUp, [][]string{{"one", "withdrawn"}, {"two"}}) {
		t.Fatalf("refill repeated admission: %v", lookedUp)
	}
	// A new request owns a new cache and must check both bindings again.
	cache = &notesIndexedAdmission{checked: map[string]bool{}}
	if _, err := cache.admit([]string{"one", "withdrawn"}, lookup); err != nil || len(lookedUp) != 3 {
		t.Fatalf("new request reused admission: %v %v", lookedUp, err)
	}
}

func TestIndexedNotesAdmissionFailureDoesNotCacheDenial(t *testing.T) {
	cache := &notesIndexedAdmission{checked: map[string]bool{}}
	if _, err := cache.admit([]string{"one"}, func([]string) ([]string, error) { return nil, errors.New("unavailable") }); err == nil || len(cache.checked) != 0 {
		t.Fatal("failed lookup must not become a permission decision")
	}
}

func TestIndexedNotesHydratesOnlyBoundedAdmittedPassages(t *testing.T) {
	key := notesLexicalVariant{SourceLifecycleFilterActive, false, 1}
	state := &notesIndexedSearch{lexical: map[notesLexicalVariant][]string{}, documentObjects: map[string]string{}, visible: map[string]bool{"allowed": true}}
	for i := 0; i < 24; i++ {
		id := fmt.Sprintf("document_%02d", i)
		state.lexical[key] = append(state.lexical[key], id)
		state.documentObjects[id] = "allowed"
		if i < 3 {
			state.documentObjects[id] = "denied"
		}
	}
	raw, err := notesIndexedWantedJSON(NotesSearchInput{indexed: state}, []notesLexicalVariant{key}, false)
	if err != nil {
		t.Fatal(err)
	}
	var selected []notesIndexedWanted
	if err := json.Unmarshal([]byte(raw), &selected); err != nil {
		t.Fatal(err)
	}
	if len(selected) != 8 || selected[0].ID != "document_03" || selected[7].ID != "document_10" {
		t.Fatalf("hydration did not retain the bounded admitted ranking: %v", selected)
	}
}

func TestIndexedNotesRetrievalQueryShape(t *testing.T) {
	input := NotesSearchInput{Query: "quantum mechanics", Mode: NotesSearchModeHybrid, SourceLifecycle: SourceLifecycleFilterActive, RequireCurrent: true, Limit: 10}
	variants := notesLexicalVariants(input)
	lexical, err := buildIndexedNotesLexicalCandidates(input, variants)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"sd.tsv @@ nq.query", "sd.notes_fields LIKE", "sd.metadata->'tags'", "source_lifecycle=request.lifecycle", "NOT pool.require_current OR", "LIMIT request.candidate_limit", "candidate_pool AS MATERIALIZED", "status.truncated"} {
		if !strings.Contains(lexical.SQL, fragment) {
			t.Fatalf("missing %s", fragment)
		}
	}
	for _, forbidden := range []string{"corpus_stats", "lt.term LIKE", "SELECT sd.*", "visible_objects AS MATERIALIZED"} {
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

func TestIndexedNotesBoxOnlyANNFiltersMembershipBeforeTopKWithoutCountProbe(t *testing.T) {
	for _, origin := range []string{"default", "explicit"} {
		for _, boxID := range []string{"box_personal", "box_company"} {
			for _, large := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/large_%t", origin, boxID, large), func(t *testing.T) {
					input := NotesSearchInput{Query: "quantum mechanics", Mode: NotesSearchModeHybrid, SourceLifecycle: SourceLifecycleFilterActive, Limit: 10,
						boxSelection: &boxregistry.ResolvedSelection{BoxIDs: []string{boxID}, Origin: origin}}
					if notesIndexedExactScope(input) || !notesIndexedHasScope(input) || !notesIndexedUsesANN(input) {
						t.Fatal("Box-only request entered the 201-object count probe/exact lane")
					}
					if large {
						exact := false
						input.indexedExactScope = &exact
					}
					query, err := buildIndexedNotesSemanticCandidates(input, EmbeddingSettings{Dimensions: 3}, []float32{1, 2, 3}, notesLexicalVariants(input))
					if err != nil {
						t.Fatal(err)
					}
					end := strings.Index(query.SQL, "), archived_objects AS MATERIALIZED")
					if end < 0 {
						t.Fatal("missing ANN pool boundary")
					}
					ann := query.SQL[:end]
					for _, fragment := range []string{"ann_object.knowledge_object_id=ce.knowledge_object_id", "ann_root.project_id", "membership_project.metadata->>'home_box_id'", "ann_root.metadata->>'box_id'", "OFFSET 0", "ORDER BY embedding <=> $1::vector LIMIT 160"} {
						if !strings.Contains(ann, fragment) {
							t.Fatalf("missing pre-Top-K point predicate: %s", fragment)
						}
					}
					if strings.Index(ann, "home_box_id") > strings.Index(ann, "ORDER BY embedding") || !strings.Contains(query.SQL, "FROM ann ce") {
						t.Fatal("Box membership moved after ANN limit or disabled ANN")
					}
					if len(query.Args) < 5 || query.Args[len(query.Args)-1] != `["`+boxID+`"]` {
						t.Fatal("ANN pool did not bind selected Box IDs")
					}
				})
			}
		}
	}
}

func TestIndexedNotesEmptyBoxANNDoesNotUsePersonalOrBroaden(t *testing.T) {
	input := NotesSearchInput{Query: "empty", Mode: NotesSearchModeSemantic, Limit: 10, SourceLifecycle: SourceLifecycleFilterActive, boxSelection: &boxregistry.ResolvedSelection{}}
	query, err := buildIndexedNotesSemanticCandidates(input, EmbeddingSettings{Dimensions: 3}, []float32{1, 2, 3}, notesLexicalVariants(input))
	if err != nil {
		t.Fatal(err)
	}
	end := strings.Index(query.SQL, "), archived_objects AS MATERIALIZED")
	if end < 0 || !strings.Contains(query.SQL[:end], "AND false OFFSET 0") {
		t.Fatal("empty Box selection became a global ANN pool")
	}
}

func TestIndexedNotesNativeRelevanceSurvivesHydration(t *testing.T) {
	input := NotesSearchInput{Query: "silver orchard refresh", Mode: NotesSearchModeLexical, SourceLifecycle: SourceLifecycleFilterActive, Limit: 5}
	input.indexed = &notesIndexedSearch{}
	variants := notesLexicalVariants(input)
	candidate, err := buildIndexedNotesLexicalCandidates(input, variants)
	if err != nil || !strings.Contains(candidate.SQL, "ts_rank_cd(sd.tsv,nq.query,32)") {
		t.Fatalf("native relevance missing: %v", err)
	}
	for _, forbidden := range []string{"exact_postings", "term_hits", "term_stats", "matching_lengths", "pool_stats", "term_frequency", "document_length"} {
		if strings.Contains(candidate.SQL, forbidden) {
			t.Fatalf("retired corpus scorer: %s", forbidden)
		}
	}
	pool := strings.Index(candidate.SQL, "candidate_pool AS MATERIALIZED")
	if pool < 0 || strings.Contains(candidate.SQL[:pool], "latest_version") || strings.Contains(candidate.SQL[:pool], "observed_file") {
		t.Fatal("body/publication/presence work escaped the bounded pool")
	}
	hydration, err := buildIndexedNotesLexicalHydration(input, variants)
	if err != nil || !strings.Contains(hydration.SQL, "w.score AS final_score") || strings.Contains(hydration.SQL, "THEN 3.0") {
		t.Fatalf("hydration discarded indexed relevance: %v", err)
	}
}

func TestIndexedNotesANNCustodyLookupIsPointBounded(t *testing.T) {
	for _, tc := range []struct {
		name      string
		lifecycle SourceLifecycleFilter
		project   string
		point     int
	}{
		{"active", SourceLifecycleFilterActive, "", 1},
		{"both", SourceLifecycleFilterAll, "", 1},
		{"archive", SourceLifecycleFilterArchived, "", 0},
		{"scoped", SourceLifecycleFilterActive, "project_test", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := NotesSearchInput{Query: "thermal acceleration", Mode: NotesSearchModeHybrid,
				SourceLifecycle: tc.lifecycle, ProjectID: tc.project, Limit: 4, RequireCurrent: true, fileView: true}
			query, err := buildIndexedNotesSemanticCandidates(input, EmbeddingSettings{Dimensions: 3}, []float32{1, 2, 3}, notesLexicalVariants(input))
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Count(query.SQL, "WHERE c.knowledge_object_id=ko.knowledge_object_id OFFSET 0"); got != tc.point {
				t.Fatalf("point lookup count=%d, want %d", got, tc.point)
			}
			for _, required := range []string{notesObservedSourcePresentSQL("ko"), notesPublishedVersionSQL("ko", "kc", "kov", true, false), notesPublicationCurrentSQL("ko", "kov")} {
				if !strings.Contains(query.SQL, required) {
					t.Fatal("bounded lookup lost a source or publication fence")
				}
			}
		})
	}
}

func TestIndexedNotesQueryCardinalityBound(t *testing.T) {
	for _, input := range []NotesSearchInput{
		{Query: "WebDAV"},
		{Query: "cowardice", Path: "sources/tree-of-life"},
		{Query: "silver SILVER", Phrases: []string{"orchard refresh", "orchard refresh", " "}, Tags: []string{"garden"}},
		{Query: "!!!"},
	} {
		input.SourceLifecycle, input.Limit = SourceLifecycleFilterActive, 5
		input.indexed = &notesIndexedSearch{}
		for _, build := range []func(NotesSearchInput, []notesLexicalVariant) (notesSearchQuery, error){buildIndexedNotesLexicalCandidates, buildIndexedNotesLexicalHydration} {
			query, err := build(input, notesLexicalVariants(input))
			if err != nil {
				t.Fatal(err)
			}
			for _, bound := range []string{
				fmt.Sprintf("WHERE $1::text IS NOT NULL LIMIT %d)", len(notesQueryTerms(input))),
				fmt.Sprintf("WHERE trim(value)<>'' LIMIT %d)", len(input.Phrases)),
			} {
				if !strings.Contains(query.SQL, bound) {
					t.Fatalf("missing planner bound %s for %+v", bound, input)
				}
			}
			var payload struct {
				Terms []notesQueryTerm `json:"terms"`
			}
			var phrases []string
			if err := json.Unmarshal([]byte(query.Args[1].(string)), &payload); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(query.Args[2].(string)), &phrases); err != nil {
				t.Fatal(err)
			}
			if len(payload.Terms) != len(notesQueryTerms(input)) || len(phrases) > len(input.Phrases) {
				t.Fatal("planner bound would discard input terms or phrases")
			}
		}
	}
}

func TestIndexedNotesOwnedPathRestrictsEveryMatchLane(t *testing.T) {
	for _, ownership := range []NotesSearchInput{{ProjectID: "project_test"}, {NotesSourceRootID: "root_test"}, {}} {
		input := ownership
		input.Query, input.Path, input.Limit = "cowardice", "sources/tree-of-life", 1
		input.SourceLifecycle = SourceLifecycleFilterActive
		input.Tags, input.After = []string{"history"}, "2026-10-01T00:00:00Z"
		query, err := buildIndexedNotesLexicalCandidates(input, notesLexicalVariants(input))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(query.SQL, "scoped_documents AS NOT MATERIALIZED") {
			t.Fatal("path-only scope must prefilter matching too")
		}
		if strings.Contains(query.SQL, "FROM search.search_documents sd,all_query_terms") || !strings.Contains(query.SQL, "JOIN scoped_documents sd USING(search_document_id)") || !strings.Contains(query.SQL, "FROM search.search_documents sd JOIN scoped_documents scoped USING(search_document_id)") {
			t.Fatal("an indexed match lane still expands outside the selected path")
		}
		args := []any{input.Query, query.Args[1], query.Args[2]}
		scope := notesIndexedScopeSQL(input, &args)
		if strings.Count(query.SQL, scope) != 3 || !reflect.DeepEqual(args, query.Args) {
			t.Fatal("early path filter must use the same full scope and bound parameters as eligibility")
		}
	}
}

func TestIndexedNotesEveryExplicitScopePrecedesExpansion(t *testing.T) {
	for _, input := range []NotesSearchInput{
		{ProjectID: "project_test"}, {NotesSourceRootID: "root_test"}, {Path: "physics-lab"},
		{SourceNodeKey: "main"}, {FileClass: "pdf"}, {SourceCategory: "notes"},
		{Tags: []string{"physics"}}, {After: "2026-10-01T00:00:00Z"}, {Before: "2026-10-13T00:00:00Z"},
	} {
		input.Query, input.Limit, input.SourceLifecycle = "quantum", 5, SourceLifecycleFilterActive
		query, err := buildIndexedNotesLexicalCandidates(input, notesLexicalVariants(input))
		if err != nil || !strings.Contains(query.SQL, "scoped_documents AS NOT MATERIALIZED") {
			t.Fatalf("scope expanded globally: %+v %v", input, err)
		}
		large := false
		input.indexedExactScope = &large
		semantic, err := buildIndexedNotesSemanticCandidates(input, EmbeddingSettings{Dimensions: 3}, []float32{1, 2, 3}, notesLexicalVariants(input))
		if err != nil || strings.Contains(semantic.SQL, "FROM ann ce") {
			t.Fatalf("scoped semantic retrieval expanded global ANN: %+v %v", input, err)
		}
	}
}

func TestIndexedNotesTermCoverageRankingPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	seedNotesArchiveSibling(t, f.s, f.root, "neighbour/refresh.md", "refresh")
	seedNotesArchiveSibling(t, f.s, f.root, "neighbour/plain.md", "The silver orchard refresh is complete.")
	reconcileBoxSyncedFixture(t, f.s, []SourceRoot{f.root})
	advanceBoxSyncedFixture(t, f.s)
	result, err := f.s.SearchNotes(t.Context(), NotesSearchInput{Query: "silver orchard refresh", Mode: NotesSearchModeLexical, Limit: 1})
	if err != nil || len(result.Results) != 1 || result.Results[0].RelativePath != "neighbour/plain.md" {
		t.Fatalf("all-term body match lost to single-term fields: %+v %v", result, err)
	}
	if _, err := f.s.GetNotesPassage(t.Context(), *result.Results[0].PassageFollowup); err != nil {
		t.Fatal(err)
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

func TestIndexedNotesUnfilteredFallbackNeedsVisibleRequestedUnderfill(t *testing.T) {
	key := notesLexicalVariant{SourceLifecycleFilterActive, true, 2}
	archive := notesLexicalVariant{SourceLifecycleFilterArchived, true, 50}
	input := NotesSearchInput{Query: "how acceleration relates to temperature", SourceLifecycle: SourceLifecycleFilterActive, RequireCurrent: true, Limit: 2, indexedBudget: 800}
	state := &notesIndexedSearch{
		lexical:         map[notesLexicalVariant][]string{key: {"one", "two", "duplicate", "private"}},
		documentObjects: map[string]string{"one": "a", "two": "b", "duplicate": "a", "private": "c"},
		visible:         map[string]bool{"a": true, "b": true},
	}
	variants := []notesLexicalVariant{key, archive}
	if got := notesIndexedUnfilteredVariants(input, state, variants); len(got) != 0 {
		t.Fatalf("adequate visible page or diagnostic archive triggered grammar fallback: %v", got)
	}
	delete(state.visible, "b")
	needed := notesIndexedUnfilteredVariants(input, state, variants)
	if len(needed) != 1 || !needed[key] {
		t.Fatalf("denied or duplicate passages hid real underfill: %v", needed)
	}
	query, err := buildIndexedNotesLexicalCandidates(input, variants)
	if err != nil || !strings.Contains(query.SQL, "AND FALSE\n ORDER BY score") || strings.Contains(query.SQL, "SELECT 1 FROM primary_scores WHERE") {
		t.Fatal("initial query can activate corpus grammar fallback before admission")
	}
	input.indexedUnfiltered = needed
	query, err = buildIndexedNotesLexicalCandidates(input, variants)
	if err != nil || !strings.Contains(query.SQL, "AND request.ordinal IN (0)") {
		t.Fatal("fallback did not restrict itself to the underfilled requested partition")
	}
	input.fileView, input.discoveryLimit = true, 1
	if got := notesIndexedUnfilteredVariants(input, state, variants); len(got) != 0 {
		t.Fatal("file-view oversampling was mistaken for the user's requested count")
	}
}

func TestIndexedNotesDiversityKeepsExactScopesAndBoundedANN(t *testing.T) {
	input := NotesSearchInput{Query: "quantum mechanics", Mode: NotesSearchModeHybrid, SourceLifecycle: SourceLifecycleFilterActive, Limit: 10}
	lexical, err := buildIndexedNotesLexicalCandidates(input, notesLexicalVariants(input))
	if err != nil || !strings.Contains(lexical.SQL, "PARTITION BY ordinal,knowledge_object_id") || !strings.Contains(lexical.SQL, "WHEN request.lifecycle='archived' THEN request.candidate_limit ELSE 4 END") {
		t.Fatalf("broad lexical contribution not bounded: %v %s", err, lexical.SQL)
	}
	semantic, err := buildIndexedNotesSemanticCandidates(input, EmbeddingSettings{Dimensions: 3}, []float32{1, 2, 3}, notesLexicalVariants(input))
	if err != nil || !strings.Contains(semantic.SQL, "object_position<=4") || !strings.Contains(semantic.SQL, "ORDER BY embedding <=> $1::vector LIMIT 160") {
		t.Fatalf("ANN pool diversity or bound lost: %v %s", err, semantic.SQL)
	}
	// Exact small/filter scopes and historical reads keep all ranked passages.
	input.ProjectID = "project_test"
	lexical, err = buildIndexedNotesLexicalCandidates(input, notesLexicalVariants(input))
	if err != nil || !strings.Contains(lexical.SQL, "ELSE 160 END") {
		t.Fatal("small scoped lexical search must preserve its existing bound")
	}
	semantic, err = buildIndexedNotesSemanticCandidates(input, EmbeddingSettings{Dimensions: 3}, []float32{1, 2, 3}, notesLexicalVariants(input))
	if err != nil || strings.Contains(semantic.SQL, "object_position<=4") || strings.Contains(semantic.SQL, "FROM ann ce") {
		t.Fatal("small exact search must not use broad ANN diversity")
	}
}

func TestNotesSearchCoverageCountsOnlyReturnedEvidence(t *testing.T) {
	coverage := notesSearchReturnedCoverage([]NotesSearchResult{
		{SourceKind: KnowledgeSearchSourceKind, SemanticRank: 1},
		{SourceKind: KnowledgeMetadataSearchSourceKind, MetadataOnly: true},
		{SourceKind: KnowledgeSearchSourceKind, TextSource: TextSourceMetadataText, SemanticRank: 2},
	})
	if coverage.BodyResults != 1 || coverage.ReferenceOnlyResults != 2 || coverage.CompatibleVectorMatches != 2 || coverage.CorpusCountsKnown || coverage.CountsScope != "returned_results" || coverage.Scope != "enrolled_readable_sources" {
		t.Fatalf("coverage overclaims search scope: %+v", coverage)
	}
	if empty := notesSearchReturnedCoverage(nil); empty.CorpusCountsKnown || empty.BodyResults != 0 {
		t.Fatal("no match is not a corpus census")
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
	assertIndexedNotesAdmissionParity(t, f.s)
	before := search("cobalt observatory", SourceLifecycleFilterAll)
	if before.ResultCount != 5 {
		t.Fatalf("initial match count: %+v", before)
	}
	if search("obalt", SourceLifecycleFilterAll).ResultCount != 5 || search("co", SourceLifecycleFilterAll).ResultCount != 5 || search("totally_nonexistent_notes_identifier", SourceLifecycleFilterAll).ResultCount != 0 {
		t.Fatal("substring/short-term/no-match compatibility")
	}
	f.archive(t)
	assertIndexedNotesAdmissionParity(t, f.s)
	if got := search("cobalt observatory", SourceLifecycleFilterAll).ResultCount; got != 3 {
		t.Fatalf("unprojected archive leaked %d results", got)
	}
	if _, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID); err != nil {
		t.Fatal(err)
	}
	assertIndexedNotesAdmissionParity(t, f.s)
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
	assertIndexedNotesAdmissionParity(t, f.s)
	restored := f.restore(t)
	assertIndexedNotesAdmissionParity(t, f.s)
	if _, err := f.p.ProjectOperation(t.Context(), restored.OperationID); err != nil {
		t.Fatal(err)
	}
	assertIndexedNotesAdmissionParity(t, f.s)
	if search("cobalt observatory", SourceLifecycleFilterActive).ResultCount != 4 {
		t.Fatal("restored custody history lost admission or private-source denial")
	}
}

// The existing five-source fixture exercises active, archive delivery lag,
// projected archive and private-source denial through the new admission shape.
func assertIndexedNotesAdmissionParity(t *testing.T, s *Service) {
	t.Helper()
	tx, err := s.store.db.BeginTx(t.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var ids string
	if err := tx.QueryRowContext(t.Context(), `SELECT COALESCE(jsonb_agg(knowledge_object_id),'[]'::jsonb)::text FROM knowledge.knowledge_objects`).Scan(&ids); err != nil {
		t.Fatal(err)
	}
	input := NotesSearchInput{readTx: tx, SourceLifecycle: SourceLifecycleFilterAll}
	before, err := s.loadNotesVisibleObjectIDs(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.candidateObjectIDsJSON = ids
	after, err := s.loadNotesVisibleObjectIDs(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	var oldIDs, newIDs []string
	if err := json.Unmarshal([]byte(before), &oldIDs); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(after), &newIDs); err != nil {
		t.Fatal(err)
	}
	sort.Strings(oldIDs)
	sort.Strings(newIDs)
	if !reflect.DeepEqual(oldIDs, newIDs) {
		t.Fatalf("factored admission changed visible objects: old=%v new=%v", oldIDs, newIDs)
	}
}

func TestIndexedNotesFileSelectionHydrationPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	seedNotesArchiveSearchEmbeddings(t, f)
	settings, err := f.s.store.GetEmbeddingSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{NotesSearchModeLexical, NotesSearchModeSemantic, NotesSearchModeHybrid} {
		t.Run(mode, func(t *testing.T) {
			input := NotesSearchInput{Query: "cobalt observatory", Mode: mode, SourceLifecycle: SourceLifecycleFilterActive,
				RequireCurrent: true, fileView: true, discoveryLimit: 2, Limit: 8, queryEmbedding: &notesQueryEmbedding{}}
			tx, err := f.s.store.db.BeginTx(t.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			input.readTx = tx
			if err := f.s.prepareIndexedNotesSearch(t.Context(), &input, mode, "", settings); err != nil {
				t.Fatal(err)
			}
			load := func() {
				t.Helper()
				if mode != NotesSearchModeSemantic {
					input.lexicalBatch, err = f.s.loadNotesLexicalBatch(t.Context(), input)
					if err != nil {
						t.Fatal(err)
					}
				}
				if mode != NotesSearchModeLexical {
					input.semanticBatch, err = f.s.loadNotesSemanticBatch(t.Context(), input, settings, input.queryEmbedding.vector)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			load()
			now := f.s.currentTime()
			want, err := f.s.searchNotesPartition(t.Context(), input, mode, settings, "", now)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.s.loadNotesFileIdentities(t.Context(), input, want.results); err != nil {
				t.Fatal(err)
			}
			if err := f.s.selectIndexedNotesFiles(t.Context(), input, mode, now); err != nil {
				t.Fatal(err)
			}
			load()
			got, err := f.s.searchNotesPartition(t.Context(), input, mode, settings, "", now)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.s.loadNotesFileIdentities(t.Context(), input, got.results); err != nil {
				t.Fatal(err)
			}
			if len(got.results) == 0 || !reflect.DeepEqual(got, want) {
				t.Fatalf("selected hydration changed ranking/source/followup: got=%+v want=%+v", got, want)
			}
			for _, r := range got.results {
				if !r.ValidPassageFollowup() || r.FileID == "" || r.Snippet == "" {
					t.Fatalf("selected result lost its source: %+v", r)
				}
			}
		})
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
		if len(got) == len(want) {
			for i := range got {
				if strings.TrimSpace(got[i].Snippet) == "" {
					t.Fatal("indexed semantic result lost its source excerpt")
				}
				// Excerpt selection intentionally changes; identity, publication,
				// rank, distance and exact followup must retain legacy parity.
				want[i].Snippet = got[i].Snippet
			}
		}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("publication/followup parity: key=%+v got=%+v want=%+v err=%v", key, got, want, err)
		}
	}
}
