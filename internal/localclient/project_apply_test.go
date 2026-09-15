package localclient

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/response"
)

func TestProjectDeclarationClientRoutesAndPartialError(t *testing.T) {
	input := pc.DeclarationApplyRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: "/tmp/my project", NodeRef: "selected-node", PlanID: "reviewed", IdempotencyKey: "original", OperationID: "original-operation", Effects: []pc.DeclarationEffect{pc.DeclarationReconcile}, ApprovalRefs: []string{"approval-one"}}
	result := pc.DeclarationResult{SchemaVersion: pc.DeclarationResultSchemaV05, OperationID: input.OperationID, PlanID: input.PlanID, State: pc.DeclarationOperationPartial, Actions: []pc.DeclarationActionResult{{ActionID: "registration", EffectRef: "committed"}}}
	calls := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.RequestURI)
		switch r.URL.Path {
		case "/v1/project-declaration-plans":
			var got pc.DeclarationPlanRequest
			_ = json.NewDecoder(r.Body).Decode(&got)
			if got.ProjectRef != input.ProjectRef || got.NodeRef != input.NodeRef {
				t.Error("plan selector changed")
			}
			response.WriteJSON(w, 200, response.Success("server-correlation", pc.DeclarationPlan{SchemaVersion: pc.DeclarationPlanSchemaV05, PlanID: input.PlanID}))
		case "/v1/project-declaration-operations":
			var got pc.DeclarationApplyRequest
			_ = json.NewDecoder(r.Body).Decode(&got)
			if !reflect.DeepEqual(input, got) {
				t.Error("apply selectors changed")
			}
			response.WriteJSON(w, 409, struct {
				response.ErrorEnvelope
				Data   pc.DeclarationResult `json:"data"`
				Detail pc.DeclarationError  `json:"declaration_error"`
			}{response.ErrorEnvelope{OK: false, Error: response.ErrorBody{Code: string(pc.DeclarationPlanStale), CorrelationID: "server-correlation", Summary: "source changed", Hint: "exact resume"}, Meta: response.Meta{CorrelationID: "server-correlation", Source: "main-authoritative", Freshness: "live"}}, result, pc.DeclarationError{CauseCode: "source_changed"}})
		case "/v1/project-declaration-operations/original-operation":
			response.WriteJSON(w, 200, response.Success("read", result))
		case "/v1/projects//tmp/my project/declaration-status":
			if r.URL.Query().Get("node_ref") != input.NodeRef {
				t.Error("node selector changed")
			}
			response.WriteJSON(w, 200, response.Success("status", pc.DeclarationStatus{SchemaVersion: pc.DeclarationStatusSchemaV05}))
		default:
			t.Errorf("unexpected route %s", r.RequestURI)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	client, _ := NewHTTP(server.URL)
	if _, err := client.PlanProjectDeclaration(t.Context(), "client", pc.DeclarationPlanRequest{SchemaVersion: input.SchemaVersion, ProjectRef: input.ProjectRef, NodeRef: input.NodeRef}); err != nil {
		t.Fatal(err)
	}
	env, err := client.ApplyProjectDeclaration(t.Context(), "client", input)
	var typed *DeclarationRequestError
	var standard *RequestError
	if !errors.As(err, &typed) || !errors.As(err, &standard) || !reflect.DeepEqual(env.Data, result) || !reflect.DeepEqual(*typed.Result, result) || standard.CorrelationID("") != "server-correlation" || standard.Envelope.Meta.Source != "main-authoritative" || standard.Envelope.Error.Hint != "exact resume" || typed.Detail.CauseCode != "source_changed" {
		t.Fatalf("lost typed error/result: %+v %v", env, err)
	}
	if _, err = client.GetProjectDeclarationOperation(t.Context(), "client", input.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err = client.GetProjectDeclarationStatus(t.Context(), "client", input.ProjectRef, input.NodeRef); err != nil {
		t.Fatal(err)
	}
	want := []string{"POST /v1/project-declaration-plans", "POST /v1/project-declaration-operations", "GET /v1/project-declaration-operations/original-operation", "GET /v1/projects/%2Ftmp%2Fmy%20project/declaration-status?node_ref=selected-node"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("routes=%v", calls)
	}
}
func TestProjectDeclarationClientPendingAndLegacyError(t *testing.T) {
	for _, status := range []int{202, 403} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if status == 202 {
					response.WriteJSON(w, status, response.Success("pending", pc.DeclarationResult{SchemaVersion: pc.DeclarationResultSchemaV05, OperationID: "durable", State: pc.DeclarationOperationPartial}))
				} else {
					response.WriteJSON(w, status, response.ErrorEnvelope{Error: response.ErrorBody{Code: "declaration.unauthorized", CorrelationID: "denied"}})
				}
			}))
			defer server.Close()
			client, _ := NewHTTP(server.URL)
			env, err := client.ApplyProjectDeclaration(t.Context(), "client", pc.DeclarationApplyRequest{})
			if status == 202 {
				if err != nil || !env.OK || env.Data.OperationID != "durable" {
					t.Fatal("pending result lost")
				}
			} else {
				var typed *DeclarationRequestError
				if !errors.As(err, &typed) || typed.Result != nil || typed.CorrelationID("") != "denied" {
					t.Fatal("standard error lost")
				}
			}
		})
	}
}

func TestProjectDeclarationClientRejectsLegacySuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response.WriteJSON(w, 200, response.Success("legacy", map[string]any{"registerable": true, "actions": []string{}}))
	}))
	defer server.Close()
	client, _ := NewHTTP(server.URL)
	env, err := client.PlanProjectDeclaration(t.Context(), "client", pc.DeclarationPlanRequest{})
	var failure *DeclarationRequestError
	if !errors.As(err, &failure) || failure.Detail.CauseCode != "response_schema_unsupported" || env.Data.SchemaVersion != "" {
		t.Fatal("old wire response reinterpreted as declaration")
	}
}

func TestProjectDeclarationClientErrorDetailWithoutResult(t *testing.T) {
	detail := pc.DeclarationError{Code: pc.DeclarationApprovalRequired, CauseCode: "approval_required", Retryable: false, CompletedEffects: []string{"already-retained"}, Message: "approval prerequisite"}
	standard := response.ErrorEnvelope{Error: response.ErrorBody{Code: string(detail.Code), Summary: detail.Message, Hint: "Inspect required approval", CorrelationID: "exact-server-correlation"}, Meta: response.Meta{CorrelationID: "exact-server-correlation", IdempotencyKey: "original-key", Source: "main-authoritative", Freshness: "live"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response.WriteJSON(w, 422, struct {
			response.ErrorEnvelope
			Detail pc.DeclarationError `json:"declaration_error"`
		}{standard, detail})
	}))
	defer server.Close()
	client, _ := NewHTTP(server.URL)
	_, err := client.ApplyProjectDeclaration(t.Context(), "client", pc.DeclarationApplyRequest{})
	var typed *DeclarationRequestError
	var request *RequestError
	if !errors.As(err, &typed) || !errors.As(err, &request) || typed.Result != nil || !reflect.DeepEqual(typed.Detail, detail) || !reflect.DeepEqual(request.Envelope, standard) || request.StatusCode != 422 {
		t.Fatalf("typed/standard error changed: %+v %v", typed, err)
	}
}
