package localclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/idempotency"
	"loom.local/loom/internal/provenance"
)

func TestProvenanceClientSendsTypedSearchWithoutMutationAuthority(t *testing.T) {
	client := Client{BaseURL: "http://loom", client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Path != "/v1/provenance/search" {
			t.Fatalf("request=%s %s", req.Method, req.URL.Path)
		}
		if key := req.Header.Get(idempotency.Header); key != "" {
			t.Fatalf("read-only search sent mutation idempotency key %q", key)
		}
		var input provenance.SearchRequest
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.Query != "embeddings provenance" || !input.IncludePending || input.Project != "project_test" || input.Repository != "repo_test" || input.Limit != 4 || input.Cursor != "cursor_test" || len(input.Collections) != 1 || input.Collections[0] != provenance.SearchCollectionPendingCandidates {
			t.Fatalf("search input=%#v", input)
		}
		return okEnvelope(`{"schema_version":"loom.provenance.search.v1","query":"embeddings provenance","ordering":"stable","accepted_records":{"items":[],"truncated":false},"pending_candidates":{"items":[{"candidate_id":"22222222-2222-4222-8222-222222222222","match":{"summary":{"text":"candidate","truncated":false},"match_explanation":{"text":"matched","truncated":false},"matched_fields":[],"matched_terms":[],"source":{"kind":"pending_candidate","source_reference_ids":[],"truncated":false},"freshness":{"recorded_at":"2026-08-31T12:00:00Z","currentness":"pending_unaccepted"},"assertion_posture":"unaccepted_candidate","project_ids":[],"repository_ids":[],"compact_truncated":false},"exact_get":{"resource":"candidate","id":"22222222-2222-4222-8222-222222222222","path":"/v1/provenance/candidates/22222222-2222-4222-8222-222222222222"}}],"truncated":false},"unresolved_cases":{"items":[],"truncated":false},"repo_state":{"items":[],"truncated":false},"returned":1,"truncated":false}`), nil
	})}}
	result, err := client.SearchProvenance(context.Background(), "corr_search", provenance.SearchRequest{
		Query: "embeddings provenance", Project: "project_test", Repository: "repo_test",
		Collections:    []provenance.SearchCollection{provenance.SearchCollectionPendingCandidates},
		IncludePending: true, Limit: 4, Cursor: "cursor_test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Data.PendingCandidates.Items) != 1 || len(result.Data.AcceptedRecords.Items) != 0 || len(result.Data.RepositoryState.Items) != 0 {
		t.Fatalf("typed search response=%#v", result.Data)
	}
}

func TestProvenanceClientDecodesRepositoryStateSearchCollection(t *testing.T) {
	repositoryID := "repo_01K41SEARCH000000000000002"
	client := Client{BaseURL: "http://loom", client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var input provenance.SearchRequest
		if req.Method != http.MethodPost || req.URL.Path != "/v1/provenance/search" || json.NewDecoder(req.Body).Decode(&input) != nil {
			t.Fatalf("request=%s %s", req.Method, req.URL.Path)
		}
		if len(input.Collections) != 1 || input.Collections[0] != provenance.SearchCollectionRepositoryState || input.IncludePending {
			t.Fatalf("repository-state search input=%#v", input)
		}
		return okEnvelope(`{"schema_version":"loom.provenance.search.v1","query":"Atlas","ordering":"stable","accepted_records":{"items":[],"truncated":false},"pending_candidates":{"items":[],"truncated":false},"unresolved_cases":{"items":[],"truncated":false},"repo_state":{"items":[{"repository_id":"` + repositoryID + `","match":{"summary":{"text":"Atlas Literature Graph","truncated":false},"match_explanation":{"text":"Matched aliases on: atlas.","truncated":false},"matched_fields":["aliases"],"matched_terms":["atlas"],"source":{"kind":"repository_state_projection","source_reference_ids":[],"truncated":false},"freshness":{"recorded_at":"2026-08-31T12:00:00Z","currentness":"not_observed"},"assertion_posture":"deterministic_rebuildable_projection","project_ids":["project_01K41SEARCH000000000000002"],"repository_ids":["` + repositoryID + `"],"compact_truncated":false},"exact_get":{"resource":"repo","id":"` + repositoryID + `","path":"/v1/provenance/repos/` + repositoryID + `"}}],"truncated":false},"returned":1,"truncated":false}`), nil
	})}}
	result, err := client.SearchProvenance(context.Background(), "corr_repo_search", provenance.SearchRequest{
		Query: "Atlas", Collections: []provenance.SearchCollection{provenance.SearchCollectionRepositoryState},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Data.RepositoryState.Items) != 1 || result.Data.RepositoryState.Items[0].RepositoryID != repositoryID || result.Data.RepositoryState.Items[0].ExactGet.ID != repositoryID {
		t.Fatalf("repository-state response=%#v", result.Data)
	}
}

func TestProvenanceClientCarriesStableCursorAndDeadline(t *testing.T) {
	afterTime := time.Date(2026, 8, 29, 12, 30, 0, 123, time.UTC)
	afterID := provenance.SemanticID("11111111-1111-4111-8111-111111111111")
	client := Client{BaseURL: "http://loom", client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.Path != "/v1/provenance/candidates" {
			t.Fatalf("request=%s %s", req.Method, req.URL.Path)
		}
		query := req.URL.Query()
		if query.Get("limit") != "25" || query.Get("after_time") != afterTime.Format(time.RFC3339Nano) || query.Get("after_id") != string(afterID) || query.Get("domain") != "loom-development" || query.Get("visibility") != "private" {
			t.Fatalf("query=%v", query)
		}
		deadline, ok := req.Context().Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > provenance.FoundationRequestTimeout {
			t.Fatalf("deadline=%v ok=%v", deadline, ok)
		}
		return okEnvelope(`{"items":[],"truncated":false}`), nil
	})}}
	_, err := client.ListProvenanceCandidates(context.Background(), "corr_provenance", provenance.PageRequest{
		Limit: 25, AfterTime: &afterTime, AfterID: &afterID, Domain: "loom-development", Visibility: "private",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestProvenanceClientUsesBoundedRepositoryRoutes(t *testing.T) {
	repositoryID := "repo_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	requests := 0
	client := Client{BaseURL: "http://loom", client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		switch requests {
		case 1:
			if req.Method != http.MethodGet || req.URL.Path != "/v1/provenance/repos" || req.Header.Get(idempotency.Header) != "" {
				t.Fatalf("list request=%s %s idempotency=%q", req.Method, req.URL.String(), req.Header.Get(idempotency.Header))
			}
			query := req.URL.Query()
			if query.Get("query") != "atlas search" || query.Get("project") != "atlas" || query.Get("topic") != "provenance" || query.Get("role") != "component" || query.Get("tracking_status") != "valid" || query.Get("limit") != "3" {
				t.Fatalf("repository list query=%v", query)
			}
			return okEnvelope(`{"schema_version":"loom.provenance.repository_card.v1","ordering":"repository_id_asc","items":[],"returned":0,"truncated":false}`), nil
		case 2:
			if req.Method != http.MethodGet || req.URL.Path != "/v1/provenance/repos/"+repositoryID || req.Header.Get(idempotency.Header) != "" {
				t.Fatalf("get request=%s %s idempotency=%q", req.Method, req.URL.String(), req.Header.Get(idempotency.Header))
			}
			return okEnvelope(`{"schema_version":"loom.provenance.repository_card.v1","repository_id":"` + repositoryID + `","aliases":[],"accepted_context":[],"topics":[],"active_focus":[],"recent_outcomes":[],"next_priorities":[],"blockers":[],"diagnostics":[],"field_sources":{}}`), nil
		default:
			t.Fatalf("unexpected repository request %s %s", req.Method, req.URL.String())
			return nil, nil
		}
	})}}
	if _, err := client.ListProvenanceRepositories(context.Background(), "corr_repo_list", provenance.RepositoryProjectionListRequest{
		Query: "atlas search", Project: "atlas", Topic: "provenance", Role: "component", TrackingStatus: provenance.RepositoryTrackingValid, Limit: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if result, err := client.GetProvenanceRepository(context.Background(), "corr_repo_get", repositoryID); err != nil || result.Data.RepositoryID != repositoryID {
		t.Fatalf("repository get result=%#v err=%v", result.Data, err)
	}
	if requests != 2 {
		t.Fatalf("repository request count=%d", requests)
	}
}

func TestProvenanceClientMutationRequiresAndSendsIdempotencyKey(t *testing.T) {
	requests := 0
	client := Client{BaseURL: "http://loom", client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.Method != http.MethodPost || req.URL.Path != "/v1/provenance/candidates" || req.Header.Get(idempotency.Header) != "provenance-idem" {
			t.Fatalf("request=%s %s idempotency=%q", req.Method, req.URL.Path, req.Header.Get(idempotency.Header))
		}
		return okEnvelope(`{"execution_authority":{"actor_id":"actor","origin_node_id":"node","policy_decision_id":"decision","capability":"main@provenance.candidate.register"},"receipt":{"schema_version":"1.0","registration_digest":"sha256:test","replayed":false,"receipts":[]}}`), nil
	})}}
	input := provenance.CandidateRegistrationRequest{}
	if _, err := client.RegisterProvenanceCandidates(context.Background(), "corr_provenance", input); err == nil || !strings.Contains(err.Error(), "idempotency key") {
		t.Fatalf("missing-key error=%v", err)
	}
	if requests != 0 {
		t.Fatalf("request sent without key")
	}
	result, err := client.WithIdempotencyKey(" provenance-idem ").RegisterProvenanceCandidates(context.Background(), "corr_provenance", input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Data.Execution.ActorID != "actor" || result.Data.Execution.OriginNodeID != "node" || requests != 1 {
		t.Fatalf("result=%#v requests=%d", result.Data, requests)
	}
}

func TestProvenanceClientResponseCapAndStableError(t *testing.T) {
	t.Run("cap", func(t *testing.T) {
		client := Client{BaseURL: "http://loom", client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			body := io.MultiReader(strings.NewReader(strings.Repeat("x", provenance.MaximumFoundationResponseBytes)), bytes.NewBufferString("xx"))
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(body)}, nil
		})}}
		_, err := client.ProvenanceHealth(context.Background(), "corr_cap")
		if !errors.Is(err, ErrProvenanceResponseTooLarge) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("typed error", func(t *testing.T) {
		client := Client{BaseURL: "http://loom", client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusForbidden, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":false,"error":{"code":"provenance.forbidden","summary":"denied","domain":"provenance","target":"authorization","correlation_id":"corr_error"},"meta":{"correlation_id":"corr_error"}}`))}, nil
		})}}
		_, err := client.ProvenanceHealth(context.Background(), "corr_error")
		var requestErr *RequestError
		if !errors.As(err, &requestErr) || requestErr.StatusCode != http.StatusForbidden || requestErr.Envelope.Error.Code != "provenance.forbidden" {
			t.Fatalf("error=%T %v", err, err)
		}
	})
}

func TestProvenanceClientHonorsCallerCancellation(t *testing.T) {
	client := Client{BaseURL: "http://loom", client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.ProvenanceHealth(ctx, "corr_cancel")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

func TestProvenanceExactSourcesClientContract(t *testing.T) {
	const id provenance.SemanticID = "22222222-2222-4222-8222-222222222222"
	for _, kind := range []string{"candidate", "record"} {
		for _, limit := range []int{0, 16, 100} {
			calls := 0
			client := Client{BaseURL: "http://loom", client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != "GET" || req.URL.Path != "/v1/provenance/"+kind+"s/"+string(id) || req.URL.Query().Get("sources") != "true" || req.Header.Get(idempotency.Header) != "" {
					t.Fatalf("unexpected expansion request %s %s", req.Method, req.URL)
				}
				deadline, ok := req.Context().Deadline()
				if !ok || time.Until(deadline) > provenance.FoundationRequestTimeout {
					t.Fatal("lost bounded timeout")
				}
				body := `{"sources":{"schema_version":"loom.provenance.linked_sources.v1","parent_kind":"` + kind + `","parent_id":"` + string(id) + `","posture":"stored_resolution","items":[],"returned":0,"sources_truncated":false,"incomplete_items":0}}`
				return okEnvelope(body), nil
			})}}
			var err error
			if kind == "candidate" {
				_, err = client.GetProvenanceCandidateWithSources(context.Background(), "corr", id, limit)
			} else {
				_, err = client.GetProvenanceRecordWithSources(context.Background(), "corr", id, limit)
			}
			if err != nil || calls != 1 {
				t.Fatalf("get fan-out/error: %d %v", calls, err)
			}
		}
	}
	for _, kind := range []string{"candidate", "record"} {
		client := Client{BaseURL: "http://loom", client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) { return okEnvelope(`{}`), nil })}}
		var err error
		if kind == "candidate" {
			_, err = client.GetProvenanceCandidateWithSources(context.Background(), "corr", id, 16)
		} else {
			_, err = client.GetProvenanceRecordWithSources(context.Background(), "corr", id, 16)
		}
		if err == nil || !strings.Contains(err.Error(), "expansion is missing or invalid") {
			t.Fatalf("old server accepted as expansion: %v", err)
		}
	}
}
