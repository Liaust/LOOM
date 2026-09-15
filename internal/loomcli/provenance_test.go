package loomcli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/idempotency"
	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/response"
)

func TestProvenanceCLIUsesTypedSearchAndFoundationExactGet(t *testing.T) {
	recordID := provenance.SemanticID("11111111-1111-4111-8111-111111111111")
	candidateID := provenance.SemanticID("22222222-2222-4222-8222-222222222222")
	caseID := provenance.SemanticID("33333333-3333-4333-8333-333333333333")
	repositoryID := "repo_01K41SEARCH000000000000002"
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	searchResult := provenance.SearchResponse{
		SchemaVersion: provenance.SearchSchemaVersion, Query: "embeddings provenance", Ordering: "stable",
		AcceptedRecords: provenance.SearchCollectionPage[provenance.AcceptedRecordSearchResult]{Items: []provenance.AcceptedRecordSearchResult{{
			RecordID: recordID,
			Match: provenance.SearchCompactMatch{
				Summary:          provenance.SearchText{Text: "Use lexical retrieval; embeddings remain deferred."},
				MatchExplanation: provenance.SearchText{Text: "Matched claim on: embedding, provenance."},
				Source:           provenance.SearchSourceSummary{Kind: "accepted_record", SourceReferenceIDs: []provenance.SemanticID{}},
				Freshness:        provenance.SearchFreshness{RecordedAt: now, Currentness: "current"},
				AssertionPosture: "accepted", ProjectIDs: []string{}, RepositoryIDs: []string{},
			},
			ExactGet: provenance.SearchExactGet{Resource: "record", ID: string(recordID), Path: "/v1/provenance/records/" + string(recordID)},
		}}},
		PendingCandidates: provenance.SearchCollectionPage[provenance.PendingCandidateSearchResult]{Items: []provenance.PendingCandidateSearchResult{{
			CandidateID: candidateID,
			Match: provenance.SearchCompactMatch{
				Summary:          provenance.SearchText{Text: "Enable embeddings now."},
				MatchExplanation: provenance.SearchText{Text: "Matched claim on: embedding."},
				Source:           provenance.SearchSourceSummary{Kind: "pending_candidate", SourceReferenceIDs: []provenance.SemanticID{}},
				Freshness:        provenance.SearchFreshness{RecordedAt: now, Currentness: "pending_unaccepted"},
				AssertionPosture: "unaccepted_candidate", ProjectIDs: []string{}, RepositoryIDs: []string{},
			},
			ExactGet: provenance.SearchExactGet{Resource: "candidate", ID: string(candidateID), Path: "/v1/provenance/candidates/" + string(candidateID)},
		}}},
		UnresolvedCases: provenance.SearchCollectionPage[provenance.UnresolvedCaseSearchResult]{Items: []provenance.UnresolvedCaseSearchResult{}},
		RepositoryState: provenance.SearchCollectionPage[provenance.RepositoryStateSearchResult]{Items: []provenance.RepositoryStateSearchResult{{
			RepositoryID: repositoryID,
			Match: provenance.SearchCompactMatch{
				Summary:          provenance.SearchText{Text: "Atlas Literature Graph Personal Research"},
				MatchExplanation: provenance.SearchText{Text: "Matched aliases on: atlas."},
				Source:           provenance.SearchSourceSummary{Kind: "repository_state_projection", SourceReferenceIDs: []provenance.SemanticID{}},
				Freshness:        provenance.SearchFreshness{RecordedAt: now, Currentness: "not_observed"},
				AssertionPosture: "deterministic_rebuildable_projection", ProjectIDs: []string{"project_01K41SEARCH000000000000002"}, RepositoryIDs: []string{repositoryID},
			},
			ExactGet: provenance.SearchExactGet{Resource: "repo", ID: repositoryID, Path: "/v1/provenance/repos/" + repositoryID},
		}}},
		Returned: 3,
	}

	requests := map[string]int{}
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests[r.URL.Path]++
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/provenance/search":
			var input provenance.SearchRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input.Query != "embeddings provenance" || !input.IncludePending || input.Project != "project_test" || input.Repository != "repo_test" || input.Limit != 4 || len(input.Collections) != 3 || input.Collections[2] != provenance.SearchCollectionRepositoryState {
				t.Fatalf("search request=%#v", input)
			}
			response.WriteJSON(w, http.StatusOK, response.Success("corr_search", searchResult))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/provenance/records/"+string(recordID):
			if r.URL.Query().Get("limit") != "16" {
				t.Fatalf("record exact limit=%q", r.URL.Query().Get("limit"))
			}
			response.WriteJSON(w, http.StatusOK, response.Success("corr_record", provenance.RecordLifecycleProjection{
				Record:             provenance.Record{ID: recordID, AssertionPosture: "accepted", Claim: "accepted claim", Temporal: provenance.TemporalInterpretation{Interpretation: "current"}},
				SourceReferenceIDs: []provenance.SemanticID{}, Events: []provenance.LedgerEvent{},
			}))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/provenance/candidates/"+string(candidateID):
			response.WriteJSON(w, http.StatusOK, response.Success("corr_candidate", provenance.CandidateLifecycleProjection{
				Candidate: provenance.Candidate{ID: candidateID, AssertionPosture: "unaccepted_candidate", Claim: "candidate claim"}, EffectiveState: "pending",
				SourceReferenceIDs: []provenance.SemanticID{}, Events: []provenance.LedgerEvent{},
			}))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/provenance/cases/"+string(caseID):
			response.WriteJSON(w, http.StatusOK, response.Success("corr_case", provenance.ResolutionCaseLifecycleProjection{
				Case: provenance.ResolutionCase{ID: caseID, Issue: "unresolved issue"}, EffectiveState: "open",
				Members: []provenance.CaseMember{}, Events: []provenance.CaseEvent{},
			}))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "provenance", "search", "embeddings provenance", "--project", "project_test", "--repo", "repo_test", "--collection", "accepted_records", "--collection", "pending_candidates", "--collection", "repo_state", "--include-pending", "--limit", "4")
	if err != nil {
		t.Fatalf("search returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{"Accepted records: 1", string(recordID), "Pending candidates (unaccepted): 1", string(candidateID), "pending_unaccepted", "Repository state: 1", repositoryID, "deterministic_rebuildable_projection"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("search output missing %q:\n%s", want, stdout)
		}
	}

	for _, args := range [][]string{
		{"provenance", "record", "get", string(recordID)},
		{"provenance", "candidate", "get", string(candidateID)},
		{"provenance", "case", "get", string(caseID)},
	} {
		stdout, stderr, err = executeRootCommand(append([]string{"--socket", socketPath}, args...)...)
		if err != nil {
			t.Fatalf("%v returned error: %v stderr=%s", args, err, stderr)
		}
		if !strings.Contains(stdout, args[3]) {
			t.Fatalf("%v output missing exact id:\n%s", args, stdout)
		}
	}
	if requests["/v1/provenance/search"] != 1 || requests["/v1/provenance/records/"+string(recordID)] != 1 || requests["/v1/provenance/candidates/"+string(candidateID)] != 1 || requests["/v1/provenance/cases/"+string(caseID)] != 1 {
		t.Fatalf("unexpected request counts: %#v", requests)
	}
}

func TestProvenanceCLIRejectsExactGetOutsideFrozenBound(t *testing.T) {
	_, _, err := executeRootCommand("provenance", "record", "get", "11111111-1111-4111-8111-111111111111", "--limit", "17")
	if err == nil {
		t.Fatal("exact get accepted a limit beyond the Slice 1 source bound")
	}
}

func TestProvenanceCLICombinedAtlasSearchKeepsRepositoriesAndCaseTyped(t *testing.T) {
	caseID := provenance.SemanticID("33333333-3333-4333-8333-333333333002")
	repositoryIDs := []string{"repo_01K41SEARCH000000000000002", "repo_01K41SEARCH000000000000003"}
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	match := func(summary, currentness string) provenance.SearchCompactMatch {
		return provenance.SearchCompactMatch{
			Summary: provenance.SearchText{Text: summary}, MatchExplanation: provenance.SearchText{Text: "Matched aliases on: atlas."},
			Source:           provenance.SearchSourceSummary{Kind: "repository_state_projection", SourceReferenceIDs: []provenance.SemanticID{}},
			Freshness:        provenance.SearchFreshness{RecordedAt: now, Currentness: currentness},
			AssertionPosture: "deterministic_rebuildable_projection", ProjectIDs: []string{}, RepositoryIDs: []string{},
		}
	}
	result := provenance.SearchResponse{
		SchemaVersion: provenance.SearchSchemaVersion, Query: "Open Atlas.", Ordering: "stable",
		AcceptedRecords:   provenance.SearchCollectionPage[provenance.AcceptedRecordSearchResult]{Items: []provenance.AcceptedRecordSearchResult{}},
		PendingCandidates: provenance.SearchCollectionPage[provenance.PendingCandidateSearchResult]{Items: []provenance.PendingCandidateSearchResult{}},
		UnresolvedCases: provenance.SearchCollectionPage[provenance.UnresolvedCaseSearchResult]{Items: []provenance.UnresolvedCaseSearchResult{{
			CaseID: caseID,
			Match: provenance.SearchCompactMatch{
				Summary: provenance.SearchText{Text: "Atlas repository ambiguity"}, MatchExplanation: provenance.SearchText{Text: "Matched issue on: atlas."},
				Source:    provenance.SearchSourceSummary{Kind: "resolution_case", SourceReferenceIDs: []provenance.SemanticID{}},
				Freshness: provenance.SearchFreshness{RecordedAt: now, Currentness: "open"}, AssertionPosture: "unresolved",
			},
			ExactGet: provenance.SearchExactGet{Resource: "case", ID: string(caseID), Path: "/v1/provenance/cases/" + string(caseID)},
		}}},
		RepositoryState: provenance.SearchCollectionPage[provenance.RepositoryStateSearchResult]{Items: []provenance.RepositoryStateSearchResult{
			{RepositoryID: repositoryIDs[0], Match: match("Atlas Literature Graph — Personal Research", "not_observed"), ExactGet: provenance.SearchExactGet{Resource: "repo", ID: repositoryIDs[0], Path: "/v1/provenance/repos/" + repositoryIDs[0]}},
			{RepositoryID: repositoryIDs[1], Match: match("Atlas Client Portal — Client Atlas", "not_observed"), ExactGet: provenance.SearchExactGet{Resource: "repo", ID: repositoryIDs[1], Path: "/v1/provenance/repos/" + repositoryIDs[1]}},
		}},
		Returned: 3,
	}
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/provenance/search" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
		var input provenance.SearchRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.Query != "Open Atlas." || len(input.Collections) != 0 || input.IncludePending {
			t.Fatalf("default combined search input=%#v", input)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_atlas", result))
	})
	defer shutdown()
	stdout, stderr, err := executeRootCommand("--socket", socketPath, "provenance", "search", "Open Atlas.")
	if err != nil {
		t.Fatalf("combined Atlas search returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{"Unresolved cases: 1", string(caseID), "Repository state: 2", repositoryIDs[0], repositoryIDs[1]} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("combined Atlas output missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "repo_01K41SEARCH000000000000001") {
		t.Fatalf("combined Atlas output contained unrelated LOOM repository:\n%s", stdout)
	}
}

func TestProvenanceCLIRepositoryProjectionCommandsAreCompactAndBounded(t *testing.T) {
	repositoryID := "repo_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	projectID := "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	requests := map[string]int{}
	card := provenance.RepositoryCard{
		SchemaVersion: provenance.RepositoryCardSchemaVersion, RepositoryID: repositoryID, Name: "Atlas Search",
		OwningProject: provenance.RepositoryOwningProject{ProjectID: projectID, Name: "Atlas", Lifecycle: "active", NavigationRef: "loom-project://" + projectID},
		Role:          "component", Purpose: "Search provenance", Aliases: []string{}, Topics: []string{"provenance"}, CurrentState: "review",
		ActiveFocus: []string{"projection"}, RecentOutcomes: []string{}, NextPriorities: []string{}, Blockers: []string{},
		AcceptedContext: []provenance.RepositoryAcceptedContext{}, Diagnostics: []string{}, FieldSources: map[string]provenance.RepositoryFieldSource{},
	}
	socketPath, shutdown := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests[r.URL.Path]++
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/provenance/repos":
			query := r.URL.Query()
			if query.Get("query") != "atlas" || query.Get("project") != "atlas" || query.Get("topic") != "provenance" || query.Get("role") != "component" || query.Get("tracking_status") != "valid" || query.Get("limit") != "3" {
				t.Fatalf("repository list query=%v", query)
			}
			response.WriteJSON(w, http.StatusOK, response.Success("corr_repo_list", provenance.RepositoryProjectionList{SchemaVersion: provenance.RepositoryCardSchemaVersion, Ordering: "repository_id_asc", Items: []provenance.RepositoryCard{card}, Returned: 1}))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/provenance/repos/"+repositoryID:
			response.WriteJSON(w, http.StatusOK, response.Success("corr_repo_get", card))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/provenance/projects/atlas/sync":
			if r.Header.Get(idempotency.Header) == "" {
				t.Fatal("manual one-project sync omitted idempotency key")
			}
			response.WriteJSON(w, http.StatusOK, response.Success("corr_repo_sync", provenance.AuthorizedProjectProjectionSync{
				Execution: provenance.ExecutionAuthority{ActorID: "actor", OriginNodeID: "node", PolicyDecisionID: "decision", Capability: "main@provenance.lifecycle.apply"},
				Receipt:   provenance.ProjectProjectionSyncReceipt{SchemaVersion: provenance.ProjectProjectionSchemaVersion, ProjectID: projectID, ProjectSnapshotID: "11111111-1111-4111-8111-111111111111", ProjectSourceVersion: 12, Repositories: []provenance.ProjectProjectionSyncRepositoryReceipt{}},
			}))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		}
	})
	defer shutdown()

	stdout, stderr, err := executeRootCommand("--socket", socketPath, "provenance", "repo", "list", "--query", "atlas", "--project", "atlas", "--topic", "provenance", "--role", "component", "--tracking-status", "valid", "--limit", "3")
	if err != nil || !strings.Contains(stdout, repositoryID) || !strings.Contains(stdout, "Returned: 1") {
		t.Fatalf("repo list stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	stdout, stderr, err = executeRootCommand("--socket", socketPath, "provenance", "repo", "get", repositoryID)
	if err != nil || !strings.Contains(stdout, "Repository "+repositoryID) || !strings.Contains(stdout, "tracking:") {
		t.Fatalf("repo get stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	stdout, stderr, err = executeRootCommand("--socket", socketPath, "provenance", "repo", "sync", "atlas")
	if err != nil || !strings.Contains(stdout, "Synced project "+projectID) {
		t.Fatalf("repo sync stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if requests["/v1/provenance/repos"] != 1 || requests["/v1/provenance/repos/"+repositoryID] != 1 || requests["/v1/provenance/projects/atlas/sync"] != 1 {
		t.Fatalf("repository projection request counts=%#v", requests)
	}
}

func TestRootCommandIncludesProvenanceFamily(t *testing.T) {
	command := NewRootCommand()
	for _, args := range [][]string{
		{"provenance", "search", "qualified state"},
		{"provenance", "record", "get", "11111111-1111-4111-8111-111111111111"},
		{"provenance", "candidate", "get", "22222222-2222-4222-8222-222222222222"},
		{"provenance", "case", "get", "33333333-3333-4333-8333-333333333333"},
		{"provenance", "repo", "list"},
		{"provenance", "repo", "get", "repo_01ARZ3NDEKTSV4RRFFQ69G5FAV"},
		{"provenance", "repo", "sync", "atlas"},
	} {
		found, _, err := command.Find(args)
		if err != nil || found == command {
			t.Fatalf("command %v is not registered: found=%v err=%v", args, found, err)
		}
	}
}

func TestProvenanceExactSourcesCLIRoundtrip(t *testing.T) {
	const id = "22222222-2222-4222-8222-222222222222"
	for _, kind := range []string{"candidate", "record"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodGet || r.URL.Path != "/v1/provenance/"+kind+"s/"+id || r.URL.RawQuery != "limit=16&sources=true" {
					t.Errorf("unexpected exact request: %s %s", r.Method, r.URL.RequestURI())
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":true,"data":{"schema_version":"1.0","` + kind + `":{"` + kind + `_id":"` + id + `","claim":"retained claim"},"effective_state":"accepted","source_reference_ids":[],"sources":{"schema_version":"loom.provenance.linked_sources.v1","parent_kind":"` + kind + `","parent_id":"` + id + `","posture":"stored_resolution","items":[],"returned":0,"sources_truncated":false,"incomplete_items":0}}}`))
			})
			defer stop()
			stdout, stderr, err := executeRootCommand("--socket", socket, "--json", "provenance", kind, "get", id, "--sources")
			if err != nil || stderr != "" || calls != 1 {
				t.Fatalf("expanded get: %v stderr=%q calls=%d stdout=%s", err, stderr, calls, stdout)
			}
			var got struct {
				Data struct {
					Sources struct {
						ParentID, Posture string
						Returned          int
					}
				}
			}
			if err := json.Unmarshal([]byte(stdout), &got); err != nil || !strings.Contains(stdout, `"parent_id":"`+id+`"`) || got.Data.Sources.Posture != "stored_resolution" {
				t.Fatalf("lost explicit expansion: %s %v", stdout, err)
			}
		})
	}
}

func TestProvenanceExactSourcesHumanPlainAndOrdinary(t *testing.T) {
	const id provenance.SemanticID = "22222222-2222-4222-8222-222222222222"
	locator := "/private/界\n\x1b[31m$(touch /must-not-run)" + strings.Repeat("x", 2048)
	for _, kind := range []string{"candidate", "record"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			expanded := false
			socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				expanded = r.URL.Query().Get("sources") == "true"
				if r.URL.Query().Get("limit") != "16" {
					t.Errorf("limit changed: %s", r.URL)
				}
				data := map[string]any{kind: map[string]any{kind + "_id": id, "claim": "unchanged parent"}, "effective_state": "pending"}
				if expanded {
					data["sources"] = provenance.LinkedSources{SchemaVersion: provenance.LinkedSourcesSchemaVersion, ParentKind: kind, ParentID: id, Posture: "stored_resolution", Items: []provenance.LinkedSourceItem{{SourceReferenceID: id, StoredSourceReceipt: &provenance.StoredSourceReceipt{Status: "unresolved", CanonicalLocator: &locator}}, {SourceReferenceID: id, ReceiptUnavailable: "item_exceeds_bound"}}, Returned: 2, SourcesTruncated: true, IncompleteItems: 1}
				}
				_ = json.NewEncoder(w).Encode(response.Success("corr", data))
			})
			defer stop()
			stdout, stderr, err := executeRootCommand("--socket", socket, "provenance", kind, "get", string(id), "--sources")
			if err != nil || stderr != "" || calls != 1 || !expanded || strings.Contains(stdout, "\x1b") || !strings.Contains(stdout, `\n\x1b[31m$(touch /must-not-run)`+strings.Repeat("x", 2048)) || !strings.Contains(stdout, "sources_truncated=true; incomplete_items=1") || !strings.Contains(stdout, "no current source access performed") {
				t.Fatalf("unsafe/lossy human output=%q err=%v", stdout, err)
			}
			stdout, _, err = executeRootCommand("--socket", socket, "--plain", "provenance", kind, "get", string(id), "--sources")
			if err != nil || stdout != string(id)+"\n" {
				t.Fatalf("plain changed: %q %v", stdout, err)
			}
			stdout, _, err = executeRootCommand("--socket", socket, "--json", "provenance", kind, "get", string(id), "--sources=false")
			if err != nil || expanded || strings.Contains(stdout, `"sources"`) {
				t.Fatalf("ordinary response changed: %s %v", stdout, err)
			}
			before := calls
			_, _, err = executeRootCommand("--socket", socket, "provenance", kind, "get", string(id), "--sources", "--limit", "17")
			if err == nil || calls != before {
				t.Fatal("CLI limit widened")
			}
		})
	}
	stdout, _, err := executeRootCommand("provenance", "candidate", "--help")
	if err != nil || strings.Contains(stdout, "pending-only") || !strings.Contains(stdout, "across lifecycle states") {
		t.Fatalf("misleading candidate help: %s", stdout)
	}
}
