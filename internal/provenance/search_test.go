package provenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"loom.local/loom/internal/projectstate"
)

func TestSearchProjectSlugResolution(t *testing.T) {
	for _, tc := range []struct {
		name      string
		matches   []string
		wantError bool
	}{
		{"exact", []string{projectionTestProjectID}, false},
		{"unknown", nil, true},
		{"ambiguous", []string{projectionTestProjectID, "project_01K41SEARCH000000000000002"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &Store{q: queryAdapter{queryRow: func(_ context.Context, sql string, args ...any) pgx.Row {
				if len(args) != 1 || args[0] != "atlas" || !strings.Contains(sql, "projection_revision DESC") || !strings.Contains(sql, "LIMIT 2") {
					t.Fatalf("unbounded or historical slug lookup: %s %#v", sql, args)
				}
				return projectProjectionTestRow(func(out ...any) error { *out[0].(*[]string) = tc.matches; return nil })
			}}}
			got, err := store.resolveSearchProject(context.Background(), "atlas")
			if (err != nil) != tc.wantError || (!tc.wantError && got != projectionTestProjectID) {
				t.Fatalf("got %q, %v", got, err)
			}
			if tc.wantError {
				var invalid *FoundationValidationError
				if !errors.As(err, &invalid) {
					t.Fatalf("missing typed validation error: %v", err)
				}
			}
		})
	}
}

func TestProvenanceSearchProjectSlugMatchesID(t *testing.T) {
	_, store, _ := migratedStore(t, "search_project_slug")
	corpus := loadSearchContract[searchFixtureCorpus](t, "corpus.json")
	seedSliceTwoSearchCorpus(t, store, corpus)
	seedSearchRepositoryProjectionCorpus(t, store, corpus)
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	api := &FoundationAPI{store: store, service: service}
	project := corpus.Projects[0]
	request := SearchRequest{Query: project.Name, Project: project.ProjectID, IncludePending: true, Limit: 1}
	byID, err := api.Search(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Project = strings.ToUpper(project.Slug)
	bySlug, err := api.Search(context.Background(), request)
	if err != nil || !reflect.DeepEqual(byID, bySlug) || byID.Returned == 0 {
		t.Fatalf("slug and ID diverged: id=%#v slug=%#v err=%v", byID, bySlug, err)
	}
	if byID.NextCursor != "" {
		request.Cursor = byID.NextCursor
		nextSlug, err := api.Search(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		request.Project = project.ProjectID
		nextID, err := api.Search(context.Background(), request)
		if err != nil || !reflect.DeepEqual(nextID, nextSlug) {
			t.Fatalf("cursor changed: %v", err)
		}
	}
	request.Cursor, request.Project = "", "unregistered-project"
	if _, err := api.Search(context.Background(), request); err == nil {
		t.Fatal("unknown slug silently accepted")
	}
	oldSlug := project.Slug
	project.Slug = "renamed-search-project"
	insertSearchProjectProjection(t, store, project, 2, time.Now().UTC())
	request.Project = oldSlug
	if _, err := api.Search(context.Background(), request); err == nil {
		t.Fatal("historical slug resolved after rename")
	}
	other := corpus.Projects[1]
	other.Slug = project.Slug
	insertSearchProjectProjection(t, store, other, 2, time.Now().UTC())
	request.Project = project.Slug
	if _, err := api.Search(context.Background(), request); err == nil {
		t.Fatal("ambiguous slug resolved")
	}
}

func TestNormalizeSearchRequestKeepsPendingExplicitAndCursorBound(t *testing.T) {
	if searchAcceptedRecordPriority != 0 || searchUnresolvedCasePriority != 1 || searchPendingCandidatePriority != 2 || searchRepositoryStatePriority != 3 {
		t.Fatalf("additive repository collection changed lifecycle cursor priorities: %d %d %d %d", searchAcceptedRecordPriority, searchUnresolvedCasePriority, searchPendingCandidatePriority, searchRepositoryStatePriority)
	}
	defaultRequest, err := normalizeSearchRequest(SearchRequest{Query: "Should embeddings be enabled for provenance search now?"})
	if err != nil {
		t.Fatal(err)
	}
	if defaultRequest.IncludePending || !reflect.DeepEqual(defaultRequest.Collections, []SearchCollection{SearchCollectionAcceptedRecords, SearchCollectionUnresolvedCases, SearchCollectionRepositoryState, SearchCollectionProjectState}) {
		t.Fatalf("default pending boundary changed: %#v", defaultRequest)
	}
	if got, want := defaultRequest.Terms, []string{"embedding", "provenance"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized terms = %#v, want %#v", got, want)
	}

	explicit, err := normalizeSearchRequest(SearchRequest{
		Query: "embeddings provenance", Collections: []SearchCollection{SearchCollectionPendingCandidates},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !explicit.IncludePending || !reflect.DeepEqual(explicit.Collections, []SearchCollection{SearchCollectionPendingCandidates}) {
		t.Fatalf("explicit candidate search was not preserved: %#v", explicit)
	}
	repositoryOnly, err := normalizeSearchRequest(SearchRequest{
		Query: "atlas", Collections: []SearchCollection{SearchCollectionRepositoryState},
	})
	if err != nil || !reflect.DeepEqual(repositoryOnly.Collections, []SearchCollection{SearchCollectionRepositoryState}) {
		t.Fatalf("explicit repository-state selection was not exact: request=%#v err=%v", repositoryOnly, err)
	}
	row := searchQueryRow{ID: "11111111-1111-4111-8111-111111111111", Score: 2, CollectionPriority: searchPendingCandidatePriority, SortTime: time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)}
	cursor, err := encodeSearchCursor(explicit.Digest, row)
	if err != nil {
		t.Fatal(err)
	}
	continued, err := normalizeSearchRequest(SearchRequest{
		Query: "embeddings provenance", Collections: []SearchCollection{SearchCollectionPendingCandidates}, Cursor: cursor,
	})
	if err != nil || !continued.Cursor.Enabled || continued.Cursor.ID != row.ID {
		t.Fatalf("cursor did not round trip: request=%#v err=%v", continued, err)
	}
	if _, err := normalizeSearchRequest(SearchRequest{Query: "different query", Cursor: cursor}); err == nil {
		t.Fatal("cursor was reusable across a different search")
	}
	repositoryRow := searchQueryRow{ID: "repo_01K41SEARCH000000000000002", Score: 1, CollectionPriority: searchRepositoryStatePriority, SortTime: row.SortTime}
	repositoryCursor, err := encodeSearchCursor(repositoryOnly.Digest, repositoryRow)
	if err != nil {
		t.Fatal(err)
	}
	repositoryContinued, err := normalizeSearchRequest(SearchRequest{Query: "atlas", Collections: []SearchCollection{SearchCollectionRepositoryState}, Cursor: repositoryCursor})
	if err != nil || repositoryContinued.Cursor.ID != repositoryRow.ID {
		t.Fatalf("typed repository cursor did not round trip: request=%#v err=%v", repositoryContinued, err)
	}
	invalidRepositoryCursor, err := encodeSearchCursor(repositoryOnly.Digest, searchQueryRow{
		ID: row.ID, Score: 1, CollectionPriority: searchRepositoryStatePriority, SortTime: row.SortTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := normalizeSearchRequest(SearchRequest{Query: "atlas", Collections: []SearchCollection{SearchCollectionRepositoryState}, Cursor: invalidRepositoryCursor}); err == nil {
		t.Fatal("repository cursor accepted a lifecycle UUID")
	}
	for _, request := range []SearchRequest{
		{Query: "the and where"},
		{Query: "valid", Limit: MaximumSearchLimit + 1},
		{Query: "valid", Collections: []SearchCollection{"unknown_collection"}},
	} {
		if _, err := normalizeSearchRequest(request); err == nil {
			t.Fatalf("invalid request unexpectedly normalized: %#v", request)
		}
	}
}

func TestProjectSearchCursorCompatibility(t *testing.T) {
	if searchProjectStatePriority != 4 {
		t.Fatal("project state must append priority 4")
	}
	row := searchQueryRow{ID: projectionTestRepoOne, Score: 1, CollectionPriority: searchRepositoryStatePriority, SortTime: time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)}
	for _, pending := range []bool{false, true} {
		legacy, err := normalizeSearchRequest(SearchRequest{Query: "lexical", Project: projectionTestProjectID,
			Collections: []SearchCollection{SearchCollectionAcceptedRecords, SearchCollectionUnresolvedCases, SearchCollectionRepositoryState}, IncludePending: pending})
		if err != nil {
			t.Fatal(err)
		}
		cursor, err := encodeSearchCursor(legacy.Digest, row)
		if err != nil {
			t.Fatal(err)
		}
		request := SearchRequest{Query: "lexical", Project: projectionTestProjectID, IncludePending: pending, Cursor: cursor}
		for page := 0; page < 2; page++ {
			continued, err := normalizeSearchRequest(request)
			if err != nil || continued.Digest != legacy.Digest || !reflect.DeepEqual(continued.Collections, legacy.Collections) || !continued.Cursor.Enabled {
				t.Fatalf("legacy default cursor failed: %#v %v", continued, err)
			}
			request.Cursor, err = encodeSearchCursor(continued.Digest, row)
			if err != nil {
				t.Fatal(err)
			}
		}
		request.Project = "different-project"
		if _, err := normalizeSearchRequest(request); err == nil {
			t.Fatal("legacy fallback dropped project filter binding")
		}
	}
	request, err := normalizeSearchRequest(SearchRequest{Query: "lexical", Collections: []SearchCollection{SearchCollectionProjectState}})
	if err != nil {
		t.Fatal(err)
	}
	row.ID, row.CollectionPriority = projectionTestProjectID, searchProjectStatePriority
	cursor, err := encodeSearchCursor(request.Digest, row)
	if err != nil {
		t.Fatal(err)
	}
	continued, err := normalizeSearchRequest(SearchRequest{Query: "lexical", Collections: []SearchCollection{SearchCollectionProjectState}, Cursor: cursor})
	if err != nil || continued.Cursor.ID != projectionTestProjectID {
		t.Fatalf("project cursor failed: %#v %v", continued, err)
	}
	for _, id := range []string{projectionTestRepoOne, "11111111-1111-4111-8111-111111111111"} {
		row.ID = id
		invalid, err := encodeSearchCursor(request.Digest, row)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := normalizeSearchRequest(SearchRequest{Query: "lexical", Collections: []SearchCollection{SearchCollectionProjectState}, Cursor: invalid}); err == nil {
			t.Fatalf("project cursor accepted non-project identity %s", id)
		}
	}
}

func TestProjectSearchMaterializationKeepsCapturedQualifiers(t *testing.T) {
	input := projectDevelopmentTestInput("Lexical discovery")
	input.Project.Development.Features = []projectstate.ProjectDevelopmentFeature{{Slug: "search", Title: "Lexical discovery", Status: "planned"}}
	input.Project.Development.Decisions = []projectstate.ProjectDevelopmentDecision{{ID: "ADR-0008", Title: "Project context", Status: "proposed"}}
	project := ProjectContextProjection{ProjectID: projectionTestProjectID, Name: "Atlas", Lifecycle: "active", Development: &input.Project.Development}
	snapshotID := stableProjectionID("search capture")
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	for _, posture := range []projectstate.ProjectDevelopmentPosture{
		projectstate.ProjectDevelopmentReady, projectstate.ProjectDevelopmentPartial, projectstate.ProjectDevelopmentMissing,
		projectstate.ProjectDevelopmentInvalid, projectstate.ProjectDevelopmentUnavailable, projectstate.ProjectDevelopmentMismatch,
		projectstate.ProjectDevelopmentArchived, projectstate.ProjectDevelopmentNotRegistered,
	} {
		project.Development.Posture = posture
		project.Development.Complete = posture == projectstate.ProjectDevelopmentReady
		row := searchQueryRow{ID: project.ProjectID, Collection: SearchCollectionProjectState,
			CollectionPriority: searchProjectStatePriority, Score: 1, SortTime: now, ObservedAt: &now,
			ProjectContext: &project, ProjectSnapshotID: snapshotID, SearchableSummary: projectSearchableSummary(project)}
		item, err := (&FoundationAPI{}).materializeSearchResult(context.Background(), []string{"lexical"}, row)
		if err != nil {
			t.Fatal(err)
		}
		result := item.project
		if result.SnapshotID != snapshotID || result.Match.AssertionPosture != "source_declared" || result.Match.Freshness.Currentness != string(posture) || result.Match.Freshness.ObservedAt == nil || !result.Match.Freshness.ObservedAt.Equal(now) {
			t.Fatalf("project qualification changed: %#v", result)
		}
		if result.ExactGet.Path != "/v1/provenance/projects/"+project.ProjectID+"?snapshot_id="+string(snapshotID) || jsonRuneCount(result) > MaximumSearchCompactResult {
			t.Fatalf("project capture link or bound changed: %#v", result)
		}
		if posture != projectstate.ProjectDevelopmentReady && !result.Match.Source.Truncated {
			t.Fatal("partial/unavailable source claimed complete")
		}
		response := buildSearchResponse("lexical", []materializedSearchResult{item}, map[SearchCollection]bool{SearchCollectionProjectState: true})
		if response.Returned != 1 || len(response.ProjectState.Items) != 1 || !response.ProjectState.Truncated || len(response.RepositoryState.Items) != 0 || len(response.AcceptedRecords.Items) != 0 {
			t.Fatalf("zero-repo project crossed collection boundary: %#v", response)
		}
	}
	project.Lifecycle = "archived"
	project.Development.Posture = projectstate.ProjectDevelopmentReady
	if projectSearchCurrentness(project) != "archived" {
		t.Fatal("archived project was shown as ready")
	}
	if summary := projectSearchableSummary(project); !strings.Contains(summary, "planned") || !strings.Contains(summary, "proposed") {
		t.Fatalf("source statuses absent from search: %s", summary)
	}
}

func TestSearchCompactResultAndResponseBoundsUseUnicodeCodePoints(t *testing.T) {
	longText := strings.Repeat("界", 600)
	match := newSearchCompactMatch(
		[]string{"semantic", "retrieval"},
		[]searchField{{name: "claim", value: "semantic retrieval"}},
		longText, "accepted_record",
		[]SemanticID{
			"11111111-1111-4111-8111-111111111111",
			"22222222-2222-4222-8222-222222222222",
			"33333333-3333-4333-8333-333333333333",
		}, true, "accepted", "current", time.Now().UTC(), nil, nil, nil,
		[]string{"project_01K41SEARCH000000000000001"}, []string{"repo_01K41SEARCH000000000000001"},
	)
	item := AcceptedRecordSearchResult{
		RecordID: "11111111-1111-4111-8111-111111111111", Match: match,
		ExactGet: SearchExactGet{Resource: "record", ID: "11111111-1111-4111-8111-111111111111", Path: "/v1/provenance/records/11111111-1111-4111-8111-111111111111"},
	}
	if err := fitSearchCompactResult(&item.Match, func() any { return item }); err != nil {
		t.Fatal(err)
	}
	if got := jsonRuneCount(item); got > MaximumSearchCompactResult {
		t.Fatalf("compact result has %d code points, maximum %d", got, MaximumSearchCompactResult)
	}
	if utf8.RuneCountInString(item.Match.MatchExplanation.Text) > MaximumSearchMatchExplanation {
		t.Fatal("match explanation exceeds its frozen bound")
	}
	if !item.Match.CompactTruncated || !item.Match.Summary.Truncated || !item.Match.Source.Truncated {
		t.Fatalf("truncation posture is incomplete: %#v", item.Match)
	}
}

func TestSearchCompactAcceptedFixtureResultsFitWithoutChangingIdentityOrPosture(t *testing.T) {
	timestamp := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		id          SemanticID
		summary     string
		currentness string
		terms       []string
	}{
		{
			name: "compatible-cloud-cadence-update", id: "11111111-1111-4111-8111-111111111004",
			summary:     "Create the recovery package at 03:00 and the cloud archive at 03:15 Europe/Amsterdam.",
			currentness: "current_refinement", terms: []string{"cloud", "daily", "cadence"},
		},
		{
			name: "pending-default-exclusion", id: "11111111-1111-4111-8111-111111111008",
			summary:     "Use lexical and structured retrieval first; embeddings remain deferred until fixture evaluation proves a material gap.",
			currentness: "current", terms: []string{"embedding", "provenance"},
		},
		{
			name: "pending-explicit-inclusion", id: "11111111-1111-4111-8111-111111111008",
			summary:     "Use lexical and structured retrieval first; embeddings remain deferred until fixture evaluation proves a material gap.",
			currentness: "current", terms: []string{"embedding", "provenance"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			match := newSearchCompactMatch(
				test.terms,
				[]searchField{{name: "claim", value: test.summary}, {name: "record_context", value: test.name}},
				test.summary, "accepted_record", nil, false, "accepted", test.currentness,
				timestamp, &timestamp, &timestamp, nil,
				[]string{"project_01K41SEARCH000000000000001"}, nil,
			)
			item := AcceptedRecordSearchResult{
				RecordID: test.id, Match: match,
				ExactGet: SearchExactGet{Resource: "record", ID: string(test.id), Path: "/v1/provenance/records/" + string(test.id)},
			}
			beforeID, beforePosture, beforeCurrentness, beforeExact := item.RecordID, item.Match.AssertionPosture, item.Match.Freshness.Currentness, item.ExactGet
			if err := fitSearchCompactResult(&item.Match, func() any { return item }); err != nil {
				t.Fatal(err)
			}
			if got := jsonRuneCount(item); got > MaximumSearchCompactResult {
				t.Fatalf("fixture-shaped result has %d code points, maximum %d", got, MaximumSearchCompactResult)
			}
			if item.RecordID != beforeID || item.Match.AssertionPosture != beforePosture || item.Match.Freshness.Currentness != beforeCurrentness || item.ExactGet != beforeExact {
				t.Fatalf("bounded compaction changed protected identity or posture: %#v", item)
			}
		})
	}
}

func TestSearchCompactResultFailsClosedWhenProtectedFieldsCannotFit(t *testing.T) {
	id := SemanticID("11111111-1111-4111-8111-111111111111")
	protectedPosture := strings.Repeat("p", MaximumSearchCompactResult)
	match := SearchCompactMatch{
		Summary: SearchText{}, MatchExplanation: SearchText{},
		MatchedFields: []string{}, MatchedTerms: []string{},
		Source: SearchSourceSummary{Kind: "accepted_record", SourceReferenceIDs: []SemanticID{}},
		Freshness: SearchFreshness{
			RecordedAt: time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC), Currentness: "current",
		},
		AssertionPosture: protectedPosture, ProjectIDs: []string{}, RepositoryIDs: []string{},
	}
	item := AcceptedRecordSearchResult{
		RecordID: id, Match: match,
		ExactGet: SearchExactGet{Resource: "record", ID: string(id), Path: "/v1/provenance/records/" + string(id)},
	}
	if err := fitSearchCompactResult(&item.Match, func() any { return item }); err == nil {
		t.Fatal("oversized protected fields did not fail closed")
	}
	if item.RecordID != id || item.Match.AssertionPosture != protectedPosture || item.Match.Freshness.Currentness != "current" || item.ExactGet.ID != string(id) {
		t.Fatalf("fail-closed compaction changed protected fields: %#v", item)
	}
}

type searchCompactProtectedSnapshot struct {
	Collection       SearchCollection
	Identity         string
	ExactGet         SearchExactGet
	SourceKind       string
	Freshness        SearchFreshness
	AssertionPosture string
}

func TestSearchCompactTypedShapesFitAtAndBeyondBoundaryDeterministically(t *testing.T) {
	tests := []struct {
		name        string
		collection  SearchCollection
		summary     string
		explanation string
	}{
		{name: "accepted record", collection: SearchCollectionAcceptedRecords, summary: strings.Repeat("界", 80), explanation: strings.Repeat("e", 96)},
		{name: "pending candidate", collection: SearchCollectionPendingCandidates, summary: strings.Repeat("候", 80), explanation: strings.Repeat("e", 96)},
		{name: "unresolved case explanation", collection: SearchCollectionUnresolvedCases, explanation: strings.Repeat("説明", 48)},
		{name: "repository state", collection: SearchCollectionRepositoryState, summary: strings.Repeat("🧭", 80)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, target := range []int{MaximumSearchCompactResult, MaximumSearchCompactResult + 8} {
				t.Run(fmt.Sprintf("%d_code_points", target), func(t *testing.T) {
					match, value, protected := newSearchCompactBoundaryFixture(test.collection, test.summary, test.explanation)
					padSearchCompactResultToRunes(t, match, value, target)
					beforeProtected := protected()
					beforePayload, err := json.Marshal(value())
					if err != nil {
						t.Fatal(err)
					}
					beforeOptionalRunes := utf8.RuneCountInString(match.Summary.Text) + utf8.RuneCountInString(match.MatchExplanation.Text)

					if err := fitSearchCompactResult(match, value); err != nil {
						t.Fatal(err)
					}
					if got := jsonRuneCount(value()); got > MaximumSearchCompactResult {
						t.Fatalf("compact %s has %d code points", test.collection, got)
					}
					if got := protected(); !reflect.DeepEqual(got, beforeProtected) {
						t.Fatalf("compact %s changed protected fields: got %#v want %#v", test.collection, got, beforeProtected)
					}
					if !utf8.ValidString(match.Summary.Text) || !utf8.ValidString(match.MatchExplanation.Text) || strings.ContainsRune(match.Summary.Text+match.MatchExplanation.Text, utf8.RuneError) {
						t.Fatalf("compact %s damaged Unicode text: %#v", test.collection, match)
					}

					if target == MaximumSearchCompactResult {
						afterPayload, _ := json.Marshal(value())
						if match.CompactTruncated || !reflect.DeepEqual(afterPayload, beforePayload) {
							t.Fatalf("at-boundary %s was changed: %#v", test.collection, match)
						}
						return
					}
					if !match.CompactTruncated {
						t.Fatalf("beyond-boundary %s omitted compact_truncated", test.collection)
					}
					afterOptionalRunes := utf8.RuneCountInString(match.Summary.Text) + utf8.RuneCountInString(match.MatchExplanation.Text)
					if afterOptionalRunes >= beforeOptionalRunes {
						t.Fatalf("beyond-boundary %s did not consume optional text: before=%d after=%d", test.collection, beforeOptionalRunes, afterOptionalRunes)
					}
					if test.summary != "" && !match.Summary.Truncated {
						t.Fatalf("beyond-boundary %s omitted summary truncation truth", test.collection)
					}
					if test.summary == "" && test.explanation != "" && !match.MatchExplanation.Truncated {
						t.Fatalf("beyond-boundary %s omitted explanation truncation truth", test.collection)
					}

					replayMatch, replayValue, _ := newSearchCompactBoundaryFixture(test.collection, test.summary, test.explanation)
					padSearchCompactResultToRunes(t, replayMatch, replayValue, target)
					if err := fitSearchCompactResult(replayMatch, replayValue); err != nil {
						t.Fatal(err)
					}
					firstPayload, _ := json.Marshal(value())
					replayPayload, _ := json.Marshal(replayValue())
					if !reflect.DeepEqual(firstPayload, replayPayload) {
						t.Fatalf("compact %s replay was not deterministic\nfirst:  %s\nreplay: %s", test.collection, firstPayload, replayPayload)
					}
				})
			}
		})
	}
}

func TestSearchCompactSliceSixPendingCandidatePostgres(t *testing.T) {
	_, store, _ := migratedStore(t, "search_compact_slice6")
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	api := &FoundationAPI{store: store, service: service}
	sourceID := SemanticID("00000000-0000-4000-8000-000000006001")
	gapReason := "The disposable source intentionally remains unresolved."
	registration := CandidateRegistration{
		SchemaVersion: SchemaVersion,
		Claim:         "Disposable Slice 6 source-gap candidate.",
		RecordKind:    "decision",
		RecordContext: "Disposable integrated semantic evaluation only.",
		Sources: []SourceRegistration{{
			SourceReferenceID: sourceID, Kind: "codex_current_thread", Status: "unresolved",
			Verification: "unverified", ResolverName: "disposable-smoke", ResolverVersion: "1.0",
			GapReason: &gapReason,
			Submitted: json.RawMessage(`{"kind":"codex_current_thread","evidence":{"excerpt":"disposable only"}}`),
		}},
		Domain: "provenance-search-slice-6", Visibility: "private",
		Temporal:         TemporalInterpretation{Interpretation: "Applies only to this disposable smoke."},
		AssertionPosture: "source_claim",
		Producer: ProducerIdentity{
			ProducerID: "slice-6-smoke", ProducerKind: "working_agent", TaskID: "disposable-slice-6",
		},
		Anchors: &StructuralAnchors{Entities: []string{"slice6.disposable.source_gap"}, Projects: []string{"LOOM"}},
	}
	receipt, err := service.RegisterCandidate(context.Background(), "slice6-archivist-register", registration)
	if err != nil {
		t.Fatal(err)
	}
	request := SearchRequest{
		Query: boundedArchivistSearchQuery(registration.Claim), Project: "LOOM", IncludePending: true, Limit: 8,
		Collections: []SearchCollection{SearchCollectionAcceptedRecords, SearchCollectionPendingCandidates, SearchCollectionUnresolvedCases},
	}
	first, err := api.Search(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := api.Search(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	firstPayload, _ := json.Marshal(first)
	replayPayload, _ := json.Marshal(replay)
	if !reflect.DeepEqual(firstPayload, replayPayload) {
		t.Fatalf("supported pending search was not deterministic\nfirst:  %s\nreplay: %s", firstPayload, replayPayload)
	}
	if len(first.AcceptedRecords.Items) != 0 || len(first.UnresolvedCases.Items) != 0 || len(first.PendingCandidates.Items) != 1 {
		t.Fatalf("pending search changed typed collection separation: %#v", first)
	}
	item := first.PendingCandidates.Items[0]
	wantExact := SearchExactGet{Resource: "candidate", ID: string(receipt.CandidateID), Path: "/v1/provenance/candidates/" + string(receipt.CandidateID)}
	if item.CandidateID != receipt.CandidateID || item.ExactGet != wantExact || item.Match.Source.Kind != "pending_candidate" || item.Match.AssertionPosture != "unaccepted_candidate" || item.Match.Freshness.Currentness != "pending_unaccepted" {
		t.Fatalf("pending compact result changed protected identity, exact get, source, or posture: %#v", item)
	}
	compactRunes := jsonRuneCount(item)
	if compactRunes > MaximumSearchCompactResult {
		t.Fatalf("Slice 6 pending compact result has %d code points, maximum %d", compactRunes, MaximumSearchCompactResult)
	}
	t.Logf("Slice 6 pending compact result fits at %d Unicode code points", compactRunes)
	if !item.Match.CompactTruncated || !item.Match.Summary.Truncated {
		t.Fatalf("Slice 6 pending compact result omitted truncation truth: %#v", item.Match)
	}
	exact, err := api.GetCandidate(context.Background(), receipt.CandidateID, searchExactProjectionLimit)
	if err != nil {
		t.Fatal(err)
	}
	if exact.Candidate.ID != receipt.CandidateID || exact.EffectiveState != "pending" || exact.Candidate.AssertionPosture != registration.AssertionPosture {
		t.Fatalf("exact get changed pending/unaccepted source posture: %#v", exact)
	}
}

func newSearchCompactBoundaryFixture(collection SearchCollection, summary, explanation string) (*SearchCompactMatch, func() any, func() searchCompactProtectedSnapshot) {
	observedAt := time.Date(2026, 8, 31, 12, 34, 56, 0, time.UTC)
	sourceKind := map[SearchCollection]string{
		SearchCollectionAcceptedRecords:   "accepted_record",
		SearchCollectionPendingCandidates: "pending_candidate",
		SearchCollectionUnresolvedCases:   "resolution_case",
		SearchCollectionRepositoryState:   "repository_state_projection",
	}[collection]
	match := SearchCompactMatch{
		Summary: SearchText{Text: summary}, MatchExplanation: SearchText{Text: explanation},
		MatchedFields: []string{}, MatchedTerms: []string{},
		Source: SearchSourceSummary{Kind: sourceKind, SourceReferenceIDs: []SemanticID{}},
		Freshness: SearchFreshness{
			RecordedAt: observedAt, ObservedAt: &observedAt, Currentness: "current_typed_posture",
		},
		AssertionPosture: "protected_assertion_", ProjectIDs: []string{}, RepositoryIDs: []string{},
	}
	snapshot := func(current SearchCompactMatch, identity string, exact SearchExactGet) searchCompactProtectedSnapshot {
		return searchCompactProtectedSnapshot{
			Collection: collection, Identity: identity, ExactGet: exact, SourceKind: current.Source.Kind,
			Freshness: current.Freshness, AssertionPosture: current.AssertionPosture,
		}
	}
	switch collection {
	case SearchCollectionAcceptedRecords:
		id := SemanticID("11111111-1111-4111-8111-111111111111")
		item := &AcceptedRecordSearchResult{RecordID: id, Match: match, ExactGet: SearchExactGet{Resource: "record", ID: string(id), Path: "/v1/provenance/records/" + string(id)}}
		return &item.Match, func() any { return *item }, func() searchCompactProtectedSnapshot {
			return snapshot(item.Match, string(item.RecordID), item.ExactGet)
		}
	case SearchCollectionPendingCandidates:
		id := SemanticID("22222222-2222-4222-8222-222222222222")
		item := &PendingCandidateSearchResult{CandidateID: id, Match: match, ExactGet: SearchExactGet{Resource: "candidate", ID: string(id), Path: "/v1/provenance/candidates/" + string(id)}}
		return &item.Match, func() any { return *item }, func() searchCompactProtectedSnapshot {
			return snapshot(item.Match, string(item.CandidateID), item.ExactGet)
		}
	case SearchCollectionUnresolvedCases:
		id := SemanticID("33333333-3333-4333-8333-333333333333")
		item := &UnresolvedCaseSearchResult{CaseID: id, Match: match, ExactGet: SearchExactGet{Resource: "case", ID: string(id), Path: "/v1/provenance/cases/" + string(id)}}
		return &item.Match, func() any { return *item }, func() searchCompactProtectedSnapshot { return snapshot(item.Match, string(item.CaseID), item.ExactGet) }
	case SearchCollectionRepositoryState:
		id := "repo_01K41SEARCH000000000000001"
		item := &RepositoryStateSearchResult{RepositoryID: id, Match: match, ExactGet: SearchExactGet{Resource: "repo", ID: id, Path: "/v1/provenance/repos/" + id}}
		return &item.Match, func() any { return *item }, func() searchCompactProtectedSnapshot { return snapshot(item.Match, item.RepositoryID, item.ExactGet) }
	default:
		panic("unsupported compact search fixture collection " + collection)
	}
}

func padSearchCompactResultToRunes(t *testing.T, match *SearchCompactMatch, value func() any, target int) {
	t.Helper()
	current := jsonRuneCount(value())
	for current > target {
		overage := current - target
		summaryRunes := []rune(match.Summary.Text)
		if len(summaryRunes) > 0 {
			remove := min(overage, len(summaryRunes))
			match.Summary.Text = string(summaryRunes[:len(summaryRunes)-remove])
		} else {
			explanationRunes := []rune(match.MatchExplanation.Text)
			if len(explanationRunes) == 0 {
				t.Fatalf("compact boundary fixture cannot remove %d unprotected code points", overage)
			}
			remove := min(overage, len(explanationRunes))
			match.MatchExplanation.Text = string(explanationRunes[:len(explanationRunes)-remove])
		}
		current = jsonRuneCount(value())
	}
	match.AssertionPosture += strings.Repeat("p", target-current)
	if got := jsonRuneCount(value()); got != target {
		t.Fatalf("compact boundary fixture has %d code points, target %d", got, target)
	}
}

func TestSearchSelectionStopsBeforeFirstUnreturnedCollectionItem(t *testing.T) {
	base := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	rows := make([]searchQueryRow, 0, 10)
	for index := 0; index < 9; index++ {
		rows = append(rows, searchQueryRow{
			ID:         fmt.Sprintf("55555555-5555-4555-8555-%012d", index+1),
			Collection: SearchCollectionAcceptedRecords, CollectionPriority: searchAcceptedRecordPriority,
			Score: 2, SortTime: base.Add(-time.Duration(index) * time.Minute),
		})
	}
	rows = append(rows, searchQueryRow{
		ID: "33333333-3333-4333-8333-333333333333", Collection: SearchCollectionUnresolvedCases,
		CollectionPriority: searchUnresolvedCasePriority, Score: 2, SortTime: base.Add(time.Hour),
	})
	sort.Slice(rows, func(i, j int) bool { return searchRowLess(rows[i], rows[j]) })
	selected, more := selectBoundedSearchRows(rows, MaximumSearchLimit)
	if len(selected) != MaximumSearchResultsPerCollection || selected[len(selected)-1].ID != "55555555-5555-4555-8555-000000000008" {
		t.Fatalf("selection advanced past the accepted collection boundary: %#v", selected)
	}
	if !more[SearchCollectionAcceptedRecords] || !more[SearchCollectionUnresolvedCases] {
		t.Fatalf("unreturned collection posture is incomplete: %#v", more)
	}
}

func TestProvenanceSearchFrozenSliceTwoCorpus(t *testing.T) {
	_, store, _ := migratedStore(t, "search_contract")
	corpus := loadSearchContract[searchFixtureCorpus](t, "corpus.json")
	queries := loadSearchContract[searchQueryContract](t, "queries.json")
	seedSliceTwoSearchCorpus(t, store, corpus)
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	api := &FoundationAPI{store: store, service: service}

	for _, fixture := range queries.Queries {
		fixture := fixture
		if !sliceTwoSearchQuery(fixture) {
			continue
		}
		t.Run(fixture.QueryID, func(t *testing.T) {
			request := SearchRequest{Query: fixture.Query, IncludePending: fixture.Request.IncludePending}
			if value := fixture.Request.Filters["project"]; value != "" {
				request.Project = value
			}
			if value := fixture.Request.Filters["repo"]; value != "" {
				request.Repository = value
			}
			if value := fixture.Request.Filters["collection"]; value != "" {
				request.Collections = []SearchCollection{SearchCollection(value)}
			}
			result, err := api.Search(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			assertSearchFixtureCollections(t, result, fixture.Expected.Collections)
			assertSearchFixtureContamination(t, result, fixture.Expected.ProhibitedContamination)
			assertSearchBounds(t, result)
			assertFixtureExactGet(t, api, fixture.Expected.ExactGet)
		})
	}

	filtered, err := api.Search(context.Background(), SearchRequest{
		Query: "embeddings provenance", Project: "project_01K41SEARCH000000000000001",
		Repository: "repo_01K41SEARCH000000000000001", IncludePending: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.AcceptedRecords.Items) != 1 || len(filtered.PendingCandidates.Items) != 1 {
		t.Fatalf("project/repository filters did not preserve separate accepted and pending results: %#v", filtered)
	}
}

func TestProvenanceSearchCrossProjectAtlasReturnsRepositoryStateAndCase(t *testing.T) {
	_, store, _ := migratedStore(t, "search_atlas_combined")
	corpus := loadSearchContract[searchFixtureCorpus](t, "corpus.json")
	queries := loadSearchContract[searchQueryContract](t, "queries.json")
	seedSliceTwoSearchCorpus(t, store, corpus)
	seedSearchRepositoryProjectionCorpus(t, store, corpus)
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	api := &FoundationAPI{store: store, service: service}

	var fixture searchQueryExpectation
	found := false
	for _, query := range queries.Queries {
		if query.QueryID == "cross-project-atlas-ambiguity" {
			fixture, found = query, true
			break
		}
	}
	if !found {
		t.Fatal("frozen cross-project-atlas-ambiguity query is missing")
	}
	result, err := api.Search(context.Background(), SearchRequest{Query: fixture.Query})
	if err != nil {
		t.Fatal(err)
	}
	assertSearchFixtureCollections(t, result, fixture.Expected.Collections)
	assertSearchFixtureContamination(t, result, fixture.Expected.ProhibitedContamination)
	assertSearchBounds(t, result)
	if result.Returned != len(result.AcceptedRecords.Items)+len(result.PendingCandidates.Items)+len(result.UnresolvedCases.Items)+len(result.RepositoryState.Items) || result.Truncated != (result.NextCursor != "") {
		t.Fatalf("combined search response metadata is not truthful: %#v", result)
	}
	assertFixtureExactGet(t, api, fixture.Expected.ExactGet)
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"project_root", "repository_path", "facet_path", "registry_recorded_at", "git_command", "raw_observation", "/private/"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("compact repository search leaked forbidden technical field %q: %s", forbidden, encoded)
		}
	}
	for _, item := range result.RepositoryState.Items {
		if item.ExactGet.Resource != "repo" || item.ExactGet.ID != item.RepositoryID || item.ExactGet.Path != "/v1/provenance/repos/"+item.RepositoryID {
			t.Fatalf("repository exact-get identity is not exact: %#v", item)
		}
		if item.Match.Source.Kind != "repository_state_projection" || item.Match.AssertionPosture != "deterministic_rebuildable_projection" || strings.TrimSpace(item.Match.Freshness.Currentness) == "" {
			t.Fatalf("repository compact source/currentness posture is incomplete: %#v", item)
		}
		card, err := api.GetRepositoryProjection(context.Background(), item.RepositoryID)
		if err != nil || card.RepositoryID != item.RepositoryID || card.OwningProject.ProjectID == "" {
			t.Fatalf("repository exact get did not preserve identity: card=%#v err=%v", card, err)
		}
	}

	projectFiltered, err := api.Search(context.Background(), SearchRequest{Query: "Atlas", Project: "project_01K41SEARCH000000000000002"})
	if err != nil {
		t.Fatal(err)
	}
	if got := searchRepositoryIDs(projectFiltered.RepositoryState.Items); !reflect.DeepEqual(got, []string{"repo_01K41SEARCH000000000000002"}) || len(projectFiltered.UnresolvedCases.Items) != 0 {
		t.Fatalf("project filter crossed repository/case ownership: %#v", projectFiltered)
	}
	repositoryFiltered, err := api.Search(context.Background(), SearchRequest{Query: "Atlas", Repository: "repo_01K41SEARCH000000000000003"})
	if err != nil {
		t.Fatal(err)
	}
	if got := searchRepositoryIDs(repositoryFiltered.RepositoryState.Items); !reflect.DeepEqual(got, []string{"repo_01K41SEARCH000000000000003"}) || len(repositoryFiltered.UnresolvedCases.Items) != 0 {
		t.Fatalf("repository filter crossed typed collections: %#v", repositoryFiltered)
	}
	mismatchedFilters, err := api.Search(context.Background(), SearchRequest{
		Query: "Atlas", Project: "project_01K41SEARCH000000000000002", Repository: "repo_01K41SEARCH000000000000003",
	})
	if err != nil {
		t.Fatal(err)
	}
	if mismatchedFilters.Returned != 0 {
		t.Fatalf("mismatched project/repository filters returned results: %#v", mismatchedFilters)
	}
	repositoryOnly, err := api.Search(context.Background(), SearchRequest{Query: "Atlas", Collections: []SearchCollection{SearchCollectionRepositoryState}})
	if err != nil {
		t.Fatal(err)
	}
	if len(repositoryOnly.RepositoryState.Items) != 2 || len(repositoryOnly.AcceptedRecords.Items) != 0 || len(repositoryOnly.PendingCandidates.Items) != 0 || len(repositoryOnly.UnresolvedCases.Items) != 0 {
		t.Fatalf("explicit repository collection selection was not exact: %#v", repositoryOnly)
	}
}

func TestProvenanceSearchRepoCursorPaginationAcrossCollectionBoundaries(t *testing.T) {
	_, store, _ := migratedStore(t, "search_repo_cursor")
	corpus := loadSearchContract[searchFixtureCorpus](t, "corpus.json")
	seedSliceTwoSearchCorpus(t, store, corpus)
	seedSearchRepositoryProjectionCorpus(t, store, corpus)
	ctx := context.Background()
	base := mustSearchFixtureTime(t, corpus.FrozenAt)
	for index := 0; index < 9; index++ {
		id := SemanticID(fmt.Sprintf("55555555-5555-4555-8555-%012d", index+1))
		if err := store.AppendRecord(ctx, Record{
			ID: id, SchemaVersion: SchemaVersion, Claim: fmt.Sprintf("Atlas policy %02d", index),
			RecordKind: "decision", RecordContext: "Atlas pagination boundary", Domain: "loom",
			Visibility: "private", AssertionPosture: "accepted",
			Temporal: TemporalInterpretation{Interpretation: "current"}, CreatedAt: base.Add(time.Duration(index) * time.Minute), Payload: json.RawMessage(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	candidateID := SemanticID("66666666-6666-4666-8666-666666666666")
	if err := store.AppendCandidate(ctx, Candidate{
		ID: candidateID, SchemaVersion: SchemaVersion, State: "pending", Domain: "loom", Visibility: "private",
		RecordKind: "decision", Claim: "Atlas candidate", RecordContext: "Atlas pagination boundary",
		AssertionPosture: "agent_interpretation", ProducerID: "fixture-owner", RegisteredAt: base,
		Submitted: json.RawMessage(`{"anchors":{}}`), Payload: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	api := &FoundationAPI{store: store, service: service}
	request := SearchRequest{Query: "Atlas", IncludePending: true, Limit: MaximumSearchLimit}
	seen := map[string]bool{}
	var responses []SearchResponse
	for page := 0; page < 10; page++ {
		response, err := api.Search(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		responses = append(responses, response)
		if page == 0 && (len(response.AcceptedRecords.Items) == 0 || len(response.AcceptedRecords.Items) > MaximumSearchResultsPerCollection || len(response.UnresolvedCases.Items) != 0 || len(response.PendingCandidates.Items) != 0 || len(response.RepositoryState.Items) != 0 || response.NextCursor == "") {
			t.Fatalf("first page crossed the accepted collection boundary: %#v", response)
		}
		for _, id := range append(append(append(searchRecordIDs(response.AcceptedRecords.Items), searchCaseIDs(response.UnresolvedCases.Items)...), searchCandidateIDs(response.PendingCandidates.Items)...), searchRepositoryIDs(response.RepositoryState.Items)...) {
			if seen[id] {
				t.Fatalf("pagination duplicated %s", id)
			}
			seen[id] = true
		}
		if response.NextCursor == "" {
			break
		}
		request.Cursor = response.NextCursor
	}
	if len(seen) != 13 {
		t.Fatalf("pagination returned %d unique results, want 13: %#v", len(seen), seen)
	}
	last := responses[len(responses)-1]
	if last.NextCursor != "" || !seen[string(candidateID)] || !seen["33333333-3333-4333-8333-333333333002"] || !seen["repo_01K41SEARCH000000000000002"] || !seen["repo_01K41SEARCH000000000000003"] {
		t.Fatalf("pagination did not cross every typed collection boundary: pages=%d seen=%#v", len(responses), seen)
	}

	repositoryRequest := SearchRequest{Query: "Atlas", Collections: []SearchCollection{SearchCollectionRepositoryState}, Limit: 1}
	var repositoryIDs []string
	for page := 0; page < 3; page++ {
		response, err := api.Search(ctx, repositoryRequest)
		if err != nil {
			t.Fatal(err)
		}
		repositoryIDs = append(repositoryIDs, searchRepositoryIDs(response.RepositoryState.Items)...)
		if response.NextCursor == "" {
			break
		}
		repositoryRequest.Cursor = response.NextCursor
	}
	if !reflect.DeepEqual(repositoryIDs, []string{"repo_01K41SEARCH000000000000002", "repo_01K41SEARCH000000000000003"}) {
		t.Fatalf("typed repository cursor skipped or duplicated results: %#v", repositoryIDs)
	}
}

func TestProvenanceSearchRepoStateUsesLatestProjectMembership(t *testing.T) {
	_, store, _ := migratedStore(t, "search_repo_membership")
	corpus := loadSearchContract[searchFixtureCorpus](t, "corpus.json")
	seedSliceTwoSearchCorpus(t, store, corpus)
	seedSearchRepositoryProjectionCorpus(t, store, corpus)
	project := corpus.Projects[1]
	insertSearchProjectProjection(t, store, project, 2, mustSearchFixtureTime(t, corpus.FrozenAt).Add(time.Minute))
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	api := &FoundationAPI{store: store, service: service}
	result, err := api.Search(context.Background(), SearchRequest{Query: "Atlas", Collections: []SearchCollection{SearchCollectionRepositoryState}})
	if err != nil {
		t.Fatal(err)
	}
	if got := searchRepositoryIDs(result.RepositoryState.Items); !reflect.DeepEqual(got, []string{"repo_01K41SEARCH000000000000003"}) {
		t.Fatalf("search retained repository state removed from latest project membership: %#v", got)
	}
	if _, err := api.GetRepositoryProjection(context.Background(), "repo_01K41SEARCH000000000000002"); !errors.Is(err, ErrRepositoryProjectionNotFound) {
		t.Fatalf("exact get retained repository removed from latest membership: %v", err)
	}
}

func TestProvenanceSearchStableCursorPagination(t *testing.T) {
	_, store, _ := migratedStore(t, "search_pagination")
	ctx := context.Background()
	base := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	for index := 0; index < 12; index++ {
		id := SemanticID(fmt.Sprintf("55555555-5555-4555-8555-%012d", index+1))
		if err := store.AppendRecord(ctx, Record{
			ID: id, SchemaVersion: SchemaVersion, Claim: fmt.Sprintf("Fixture policy %02d", index),
			RecordKind: "decision", RecordContext: "fixture policy pagination",
			Domain: "loom", Visibility: "private", AssertionPosture: "accepted",
			Temporal:  TemporalInterpretation{Interpretation: "current"},
			Anchors:   &StructuralAnchors{Projects: []string{"project_page"}},
			CreatedAt: base.Add(time.Duration(index) * time.Minute), Payload: json.RawMessage(`{}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	service, _ := NewService(store)
	api := &FoundationAPI{store: store, service: service}
	request := SearchRequest{Query: "fixture policy", Collections: []SearchCollection{SearchCollectionAcceptedRecords}, Limit: 3}
	var ids []SemanticID
	for page := 0; page < 10; page++ {
		result, err := api.Search(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range result.AcceptedRecords.Items {
			ids = append(ids, item.RecordID)
		}
		if result.NextCursor == "" {
			break
		}
		request.Cursor = result.NextCursor
	}
	if len(ids) != 12 {
		t.Fatalf("paginated ids=%d, want 12: %#v", len(ids), ids)
	}
	seen := map[SemanticID]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("stable cursor duplicated %s", id)
		}
		seen[id] = true
	}
}

func sliceTwoSearchQuery(query searchQueryExpectation) bool {
	if query.ExpectedFirstEngine != "provenance" || query.Request.Surface != "search" {
		return false
	}
	for _, collection := range query.Expected.Collections {
		if collection.Collection == "repo_state" {
			return false
		}
	}
	return true
}

func seedSliceTwoSearchCorpus(t *testing.T, store *Store, corpus searchFixtureCorpus) {
	t.Helper()
	ctx := context.Background()
	items := map[string]searchFixtureItem{}
	for _, item := range corpus.Items {
		items[item.ID] = item
		switch item.Collection {
		case "accepted_records":
			id, _ := ParseSemanticID(item.ID)
			observed := mustSearchFixtureTime(t, item.Temporal.ObservedAt)
			anchors := &StructuralAnchors{}
			if item.ProjectID != nil {
				anchors.Projects = []string{*item.ProjectID}
			}
			if item.RepositoryID != nil {
				anchors.Entities = []string{*item.RepositoryID}
			}
			if err := store.AppendRecord(ctx, Record{
				ID: id, SchemaVersion: SchemaVersion, Claim: item.Text, RecordKind: "decision",
				RecordContext: item.Title, Domain: "loom", Visibility: "private",
				AssertionPosture: item.AssertionPosture,
				Temporal: TemporalInterpretation{
					Interpretation: item.Temporal.Currentness, ObservedAt: &observed,
					ValidFrom:  optionalSearchFixtureTime(t, item.Temporal.ValidFrom),
					ValidUntil: optionalSearchFixtureTime(t, item.Temporal.ValidUntil),
				},
				Anchors: anchors, CreatedAt: observed, Payload: json.RawMessage(`{}`),
			}); err != nil {
				t.Fatalf("append fixture record %s: %v", id, err)
			}
		case "pending_candidates":
			id, _ := ParseSemanticID(item.ID)
			registeredAt := mustSearchFixtureTime(t, item.Temporal.ObservedAt)
			anchors := &StructuralAnchors{}
			if item.ProjectID != nil {
				anchors.Projects = []string{*item.ProjectID}
			}
			if item.RepositoryID != nil {
				anchors.Entities = []string{*item.RepositoryID}
			}
			registration := CandidateRegistration{Anchors: anchors}
			submitted, _ := json.Marshal(registration)
			if err := store.AppendCandidate(ctx, Candidate{
				ID: id, SchemaVersion: SchemaVersion, State: "pending", Domain: "loom", Visibility: "private",
				RecordKind: "decision", Claim: item.Text, RecordContext: item.Title,
				AssertionPosture: item.AssertionPosture, ProducerID: "fixture-owner",
				RegisteredAt: registeredAt, Submitted: submitted, Payload: json.RawMessage(`{}`),
			}); err != nil {
				t.Fatalf("append fixture candidate %s: %v", id, err)
			}
		case "unresolved_cases":
			id, _ := ParseSemanticID(item.ID)
			createdAt := mustSearchFixtureTime(t, item.Temporal.ObservedAt)
			if err := store.AppendResolutionCase(ctx, ResolutionCase{
				ID: id, SchemaVersion: SchemaVersion, Issue: item.Text, Domain: "loom", Visibility: "private",
				InitialStatus: item.Lifecycle, CreatedAt: createdAt, Payload: json.RawMessage(`{}`),
			}); err != nil {
				t.Fatalf("append fixture case %s: %v", id, err)
			}
		}
	}

	for _, relation := range corpus.Relationships {
		fromID, fromErr := ParseSemanticID(relation.FromID)
		toID, toErr := ParseSemanticID(relation.ToID)
		if fromErr != nil || toErr != nil {
			continue
		}
		var caseID *SemanticID
		if relation.CaseID != nil {
			parsed, _ := ParseSemanticID(*relation.CaseID)
			caseID = &parsed
			for _, recordID := range []SemanticID{fromID, toID} {
				if err := store.AppendCaseMember(ctx, CaseMember{
					CaseID: parsed, MemberType: "record", RecordID: &recordID, Role: "contested_claim",
					AttachedAt: mustSearchFixtureTime(t, items[string(recordID)].Temporal.ObservedAt),
				}); err != nil {
					t.Fatalf("append fixture case member: %v", err)
				}
			}
		}
		relationshipID := SemanticID(fmt.Sprintf("44444444-4444-4444-8444-%012d", len(relation.FromID)+len(relation.ToID)+len(relation.RelationshipType)))
		// Fixture relation lengths are not unique, so make the final byte stable by
		// the loop position when a collision would occur.
		for suffix := 1; ; suffix++ {
			relationshipID = SemanticID(fmt.Sprintf("44444444-4444-4444-8444-%012d", suffix))
			if _, found, err := store.GetRelationship(ctx, relationshipID); err != nil {
				t.Fatal(err)
			} else if !found {
				break
			}
		}
		if err := store.AppendRelationship(ctx, Relationship{
			ID: relationshipID, SchemaVersion: SchemaVersion, RelationshipType: relation.RelationshipType,
			FromRecordID: fromID, ToRecordID: toID, ResolutionCaseID: caseID,
			CreatedAt: mustSearchFixtureTime(t, items[relation.FromID].Temporal.ObservedAt),
			CreatedBy: ProducerIdentity{ProducerID: "fixture-owner", ProducerKind: "acceptance_fixture"},
			Payload:   json.RawMessage(`{}`),
		}); err != nil {
			t.Fatalf("append fixture relationship %s: %v", relation.RelationshipType, err)
		}
	}
}

func seedSearchRepositoryProjectionCorpus(t *testing.T, store *Store, corpus searchFixtureCorpus) {
	t.Helper()
	recordedAt := mustSearchFixtureTime(t, corpus.FrozenAt)
	projects := make(map[string]searchFixtureProject, len(corpus.Projects))
	projectSnapshots := make(map[string]SemanticID, len(corpus.Projects))
	for _, project := range corpus.Projects {
		projects[project.ProjectID] = project
		projectSnapshots[project.ProjectID] = insertSearchProjectProjection(t, store, project, 1, recordedAt)
	}
	for _, fixture := range corpus.Repositories {
		project, ok := projects[fixture.OwningProject.ProjectID]
		if !ok {
			t.Fatalf("repository fixture %s has no owning project", fixture.RepositoryID)
		}
		fieldSources := make(map[string]RepositoryFieldSource, len(fixture.FieldSources))
		for name, source := range fixture.FieldSources {
			fieldSources[name] = RepositoryFieldSource{Source: source.Source, Posture: source.Posture}
		}
		acceptedContext := make([]RepositoryAcceptedContext, 0, len(fixture.AcceptedContext))
		for _, item := range fixture.AcceptedContext {
			id, err := ParseSemanticID(item.RecordID)
			if err != nil {
				t.Fatal(err)
			}
			acceptedContext = append(acceptedContext, RepositoryAcceptedContext{RecordID: id, Summary: item.Summary, Qualification: item.Qualification})
		}
		activeFocus := []string{}
		if strings.TrimSpace(fixture.ActiveFocus) != "" {
			activeFocus = append(activeFocus, fixture.ActiveFocus)
		}
		card := RepositoryCard{
			SchemaVersion: RepositoryCardSchemaVersion, RepositoryID: fixture.RepositoryID,
			Name: fixture.Name, Aliases: append([]string(nil), fixture.Aliases...),
			OwningProject: RepositoryOwningProject{
				ProjectID: project.ProjectID, Name: project.Name, Slug: project.Slug,
				Lifecycle: project.Lifecycle, NavigationRef: project.NavigationRef,
			},
			Role: fixture.Role, Purpose: fixture.Purpose, Topics: append([]string(nil), fixture.Topics...),
			CurrentState: fixture.CurrentState, ActiveFocus: activeFocus,
			RecentOutcomes: append([]string(nil), fixture.RecentOutcomes...),
			NextPriorities: append([]string(nil), fixture.NextPriorities...),
			Blockers:       append([]string(nil), fixture.Blockers...),
			// Accepted context is joined from current records by exact get and is
			// deliberately not stored in deterministic repository snapshots.
			AcceptedContext: []RepositoryAcceptedContext{},
			Freshness: RepositoryFreshness{
				Posture: fixture.Freshness.Posture, ObservedAt: optionalSearchFixtureTime(t, fixture.Freshness.ObservedAt),
				SourceVersion: fixture.Freshness.SourceVersion, SourceDigest: fixture.Freshness.SourceDigest,
				ObservedCommit: fixture.Freshness.ObservedCommit, SourceBranch: fixture.Freshness.SourceBranch,
				FrontierPosture: fixture.Freshness.FrontierPosture,
			},
			PortableNavigationRef: fixture.PortableNavigationRef,
			TrackingStatus:        RepositoryTrackingStatus(fixture.TrackingStatus),
			Diagnostics:           append([]string(nil), fixture.Diagnostics...),
			FieldSources:          fieldSources,
		}
		payload, err := json.Marshal(card)
		if err != nil {
			t.Fatal(err)
		}
		snapshotDigest, err := deterministicRepositoryCardDigest(card)
		if err != nil {
			t.Fatal(err)
		}
		snapshotID := stableProjectionID("search-repository-fixture", fixture.RepositoryID)
		rows, err := store.q.exec(context.Background(), `
			INSERT INTO provenance.repository_projection_snapshots(
				id, project_snapshot_id, project_id, repository_id, source_revision,
				source_identity_digest, source_version, source_digest, snapshot_digest,
				observed_commit, tracking_status, observed_at, searchable_summary, card_json, created_at
			) VALUES ($1::uuid, $2::uuid, $3, $4, 1, $5, $6, $7, $8, $9, $10, $11, $12, $13::jsonb, $14)
		`, string(snapshotID), string(projectSnapshots[project.ProjectID]), project.ProjectID, fixture.RepositoryID,
			digestProjection([]byte("search-repository-identity:"+fixture.RepositoryID)), fixture.Freshness.SourceVersion,
			fixture.Freshness.SourceDigest, snapshotDigest, fixture.Freshness.ObservedCommit,
			fixture.TrackingStatus, optionalSearchFixtureTime(t, fixture.Freshness.ObservedAt),
			repositorySearchableSummary(card), payload, recordedAt)
		if err != nil || rows != 1 {
			t.Fatalf("insert repository search fixture %s: rows=%d err=%v", fixture.RepositoryID, rows, err)
		}
		_ = acceptedContext // validates the frozen exact-context identities.
	}
}

func insertSearchProjectProjection(t *testing.T, store *Store, project searchFixtureProject, revision int64, recordedAt time.Time) SemanticID {
	t.Helper()
	snapshotID := stableProjectionID("search-project-fixture", project.ProjectID, fmt.Sprint(revision))
	payload, err := json.Marshal(ProjectContextProjection{
		SchemaVersion: ProjectProjectionSchemaVersion, ProjectID: project.ProjectID,
		Name: project.Name, Slug: project.Slug, Lifecycle: project.Lifecycle, NavigationRef: project.NavigationRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.q.exec(context.Background(), `
		INSERT INTO provenance.project_projection_snapshots(
			id, project_id, projection_revision, source_revision, source_identity_digest,
			source_digest, source_schema_version, snapshot_digest, observed_at,
			searchable_summary, projection_json, created_at
		) VALUES ($1::uuid, $2, $3, $3, $4, $5, 'fixture', $6, $7, $8, $9::jsonb, $7)
	`, string(snapshotID), project.ProjectID, revision,
		digestProjection([]byte(fmt.Sprintf("search-project-identity:%s:%d", project.ProjectID, revision))),
		digestProjection([]byte(fmt.Sprintf("search-project-source:%s:%d", project.ProjectID, revision))),
		digestProjection(payload), recordedAt,
		strings.Join([]string{project.ProjectID, project.Name, project.Slug, project.Lifecycle}, " "), payload)
	if err != nil || rows != 1 {
		t.Fatalf("insert project search fixture %s revision %d: rows=%d err=%v", project.ProjectID, revision, rows, err)
	}
	return snapshotID
}

func assertSearchFixtureCollections(t *testing.T, result SearchResponse, expected []searchFixtureCollection) {
	t.Helper()
	want := map[string][]string{}
	for _, collection := range expected {
		want[collection.Collection] = append([]string(nil), collection.IDs...)
	}
	got := map[string][]string{
		"accepted_records":   searchRecordIDs(result.AcceptedRecords.Items),
		"pending_candidates": searchCandidateIDs(result.PendingCandidates.Items),
		"unresolved_cases":   searchCaseIDs(result.UnresolvedCases.Items),
		"repo_state":         searchRepositoryIDs(result.RepositoryState.Items),
	}
	for collection, ids := range got {
		if want[collection] == nil {
			want[collection] = []string{}
		}
		if !reflect.DeepEqual(ids, want[collection]) {
			t.Fatalf("%s ids=%#v, want %#v", collection, ids, want[collection])
		}
	}
}

func assertSearchFixtureContamination(t *testing.T, result SearchResponse, prohibited []searchFixtureContamination) {
	t.Helper()
	collections := map[string]map[string]bool{
		"accepted_records":   stringBoolSet(searchRecordIDs(result.AcceptedRecords.Items)),
		"pending_candidates": stringBoolSet(searchCandidateIDs(result.PendingCandidates.Items)),
		"unresolved_cases":   stringBoolSet(searchCaseIDs(result.UnresolvedCases.Items)),
		"repo_state":         stringBoolSet(searchRepositoryIDs(result.RepositoryState.Items)),
	}
	for _, item := range prohibited {
		if item.Collection != "" {
			if collections[item.Collection][item.ID] {
				t.Fatalf("prohibited %s appeared in %s", item.ID, item.Collection)
			}
			continue
		}
		for collection, ids := range collections {
			if ids[item.ID] {
				t.Fatalf("prohibited %s appeared in %s", item.ID, collection)
			}
		}
	}
}

func assertSearchBounds(t *testing.T, result SearchResponse) {
	t.Helper()
	if result.Returned > DefaultSearchLimit || searchResponseRuneCount(result) > MaximumSearchCompactResponse {
		t.Fatalf("search response exceeds frozen bound: returned=%d runes=%d", result.Returned, searchResponseRuneCount(result))
	}
	for _, item := range result.AcceptedRecords.Items {
		if jsonRuneCount(item) > MaximumSearchCompactResult || utf8.RuneCountInString(item.Match.MatchExplanation.Text) > MaximumSearchMatchExplanation {
			t.Fatalf("accepted compact match exceeds frozen bound: %#v", item)
		}
	}
	for _, item := range result.PendingCandidates.Items {
		if jsonRuneCount(item) > MaximumSearchCompactResult || utf8.RuneCountInString(item.Match.MatchExplanation.Text) > MaximumSearchMatchExplanation {
			t.Fatalf("candidate compact match exceeds frozen bound: %#v", item)
		}
	}
	for _, item := range result.UnresolvedCases.Items {
		if jsonRuneCount(item) > MaximumSearchCompactResult || utf8.RuneCountInString(item.Match.MatchExplanation.Text) > MaximumSearchMatchExplanation {
			t.Fatalf("case compact match exceeds frozen bound: %#v", item)
		}
	}
	for _, item := range result.RepositoryState.Items {
		if jsonRuneCount(item) > MaximumSearchCompactResult || utf8.RuneCountInString(item.Match.MatchExplanation.Text) > MaximumSearchMatchExplanation {
			t.Fatalf("repository compact match exceeds frozen bound: %#v", item)
		}
	}
}

func assertFixtureExactGet(t *testing.T, api *FoundationAPI, exact searchFixtureExactGet) {
	t.Helper()
	switch exact.Collection {
	case "accepted_records":
		id, err := ParseSemanticID(exact.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := api.GetRecord(context.Background(), id, searchExactProjectionLimit); err != nil {
			t.Fatal(err)
		}
	case "pending_candidates":
		id, err := ParseSemanticID(exact.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := api.GetCandidate(context.Background(), id, searchExactProjectionLimit); err != nil {
			t.Fatal(err)
		}
	case "unresolved_cases":
		id, err := ParseSemanticID(exact.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := api.GetResolutionCase(context.Background(), id, searchExactProjectionLimit); err != nil {
			t.Fatal(err)
		}
	case "repo_state":
		card, err := api.GetRepositoryProjection(context.Background(), exact.ID)
		if err != nil || card.RepositoryID != exact.ID {
			t.Fatalf("repository exact get=%#v err=%v", card, err)
		}
	}
}

func searchRecordIDs(items []AcceptedRecordSearchResult) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, string(item.RecordID))
	}
	return result
}

func searchCandidateIDs(items []PendingCandidateSearchResult) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, string(item.CandidateID))
	}
	return result
}

func searchCaseIDs(items []UnresolvedCaseSearchResult) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, string(item.CaseID))
	}
	return result
}

func searchRepositoryIDs(items []RepositoryStateSearchResult) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, item.RepositoryID)
	}
	return result
}

func stringBoolSet(values []string) map[string]bool {
	result := map[string]bool{}
	for _, value := range values {
		result[value] = true
	}
	return result
}

func mustSearchFixtureTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.UTC()
}

func optionalSearchFixtureTime(t *testing.T, value *string) *time.Time {
	t.Helper()
	if value == nil {
		return nil
	}
	parsed := mustSearchFixtureTime(t, *value)
	return &parsed
}

func TestSearchFixtureQuerySelectionIsStable(t *testing.T) {
	queries := loadSearchContract[searchQueryContract](t, "queries.json")
	var selected []string
	for _, query := range queries.Queries {
		if sliceTwoSearchQuery(query) {
			selected = append(selected, query.QueryID)
		}
	}
	sort.Strings(selected)
	want := []string{
		"canonical-box-current", "compatible-cloud-cadence-update", "pending-default-exclusion",
		"pending-explicit-inclusion", "superseded-storage-decision", "unresolved-retention-contradiction",
	}
	if !reflect.DeepEqual(selected, want) {
		t.Fatalf("Slice 2 fixture selection changed: %#v", selected)
	}
}
