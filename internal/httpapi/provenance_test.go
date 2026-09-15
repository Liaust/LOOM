package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/idempotency"
	"loom.local/loom/internal/policy"
	"loom.local/loom/internal/projectstate"
	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/repostate"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/response"
)

type provenancePolicyStub struct {
	explanation policy.PolicyExplanation
	input       policy.DecisionInput
}

func (stub *provenancePolicyStub) Explain(_ context.Context, _ requestctx.Context, input policy.DecisionInput) (policy.PolicyExplanation, error) {
	stub.input = input
	return stub.explanation, nil
}

func TestPolicyProvenanceAuthorizerBindsDecisionToAuthenticatedContext(t *testing.T) {
	req := requestctx.Context{ActorID: "actor_authenticated", OriginNodeID: "node_authenticated"}
	capability := capabilities.ProvenanceRegisterCapability
	stub := &provenancePolicyStub{explanation: policy.PolicyExplanation{
		Decision: policy.Decision{PolicyDecisionID: "policy_decision_test", Decision: policy.DecisionAllow},
		Context:  policy.PolicyContext{ActorID: req.ActorID, OriginNodeID: req.OriginNodeID},
		Target:   policy.PolicyTarget{CapabilityAddress: capability},
	}}
	authorizer := PolicyProvenanceAuthorizer{service: stub}
	execution, err := authorizer.Authorize(context.Background(), req, capability)
	if err != nil {
		t.Fatal(err)
	}
	if execution.ActorID != req.ActorID || execution.OriginNodeID != req.OriginNodeID || execution.PolicyDecisionID != "policy_decision_test" || execution.Capability != capability {
		t.Fatalf("execution = %#v", execution)
	}
	if stub.input.Operation != "capability:"+capability || !strings.Contains(string(stub.input.Metadata), `"semantic_identity_authority":"separate"`) {
		t.Fatalf("policy input = %#v", stub.input)
	}

	stub.explanation.Context.ActorID = "source_claim_actor"
	if _, err := authorizer.Authorize(context.Background(), req, capability); err == nil || !strings.Contains(err.Error(), "does not match authenticated request") {
		t.Fatalf("mismatched policy context error = %v", err)
	}
	stub.explanation.Context.ActorID = req.ActorID
	stub.explanation.Decision.Decision = policy.DecisionDeny
	stub.explanation.Decision.ReasonCode = "actor_not_authorized_on_target_node"
	if _, err := authorizer.Authorize(context.Background(), req, capability); err == nil {
		t.Fatal("denied policy decision was accepted")
	} else if _, ok := err.(*ProvenanceAuthorizationError); !ok {
		t.Fatalf("denial error = %T %v", err, err)
	}
}

type provenanceTransportStub struct {
	readiness      provenance.RuntimeReadiness
	registerCalls  int
	registerKey    string
	registerInput  provenance.CandidateRegistrationRequest
	registerResult provenance.CandidateRegistrationBatchReceipt
	listCalls      int
	listCandidates func(context.Context, provenance.PageRequest) (provenance.Page[provenance.CandidateSummary], error)
	searchCalls    int
	searchInput    provenance.SearchRequest
	searchResult   provenance.SearchResponse
	searchErr      error
	repoListCalls  int
	repoListInput  provenance.RepositoryProjectionListRequest
	repoListResult provenance.RepositoryProjectionList
	repoGetCalls   int
	repoGetID      string
	repoGetResult  provenance.RepositoryCard
	repoSyncInput  provenance.ProjectProjectionSyncInput
	repoSyncResult provenance.ProjectProjectionSyncReceipt
}

func (stub *provenanceTransportStub) Readiness() provenance.RuntimeReadiness { return stub.readiness }
func (stub *provenanceTransportStub) ListCandidates(ctx context.Context, request provenance.PageRequest) (provenance.Page[provenance.CandidateSummary], error) {
	stub.listCalls++
	if stub.listCandidates != nil {
		return stub.listCandidates(ctx, request)
	}
	return provenance.Page[provenance.CandidateSummary]{}, nil
}
func (stub *provenanceTransportStub) GetCandidate(context.Context, provenance.SemanticID, int) (provenance.CandidateLifecycleProjection, error) {
	return provenance.CandidateLifecycleProjection{}, nil
}
func (stub *provenanceTransportStub) ListRecords(context.Context, provenance.PageRequest) (provenance.Page[provenance.RecordSummary], error) {
	return provenance.Page[provenance.RecordSummary]{}, nil
}
func (stub *provenanceTransportStub) GetRecord(context.Context, provenance.SemanticID, int) (provenance.RecordLifecycleProjection, error) {
	return provenance.RecordLifecycleProjection{}, nil
}
func (stub *provenanceTransportStub) ListRelationships(context.Context, provenance.PageRequest) (provenance.Page[provenance.RelationshipSummary], error) {
	return provenance.Page[provenance.RelationshipSummary]{}, nil
}
func (stub *provenanceTransportStub) GetRelationship(context.Context, provenance.SemanticID, int) (provenance.RelationshipLifecycleProjection, error) {
	return provenance.RelationshipLifecycleProjection{}, nil
}
func (stub *provenanceTransportStub) ListResolutionCases(context.Context, provenance.PageRequest) (provenance.Page[provenance.ResolutionCaseSummary], error) {
	return provenance.Page[provenance.ResolutionCaseSummary]{}, nil
}
func (stub *provenanceTransportStub) GetResolutionCase(context.Context, provenance.SemanticID, int) (provenance.ResolutionCaseLifecycleProjection, error) {
	return provenance.ResolutionCaseLifecycleProjection{}, nil
}
func (stub *provenanceTransportStub) RegisterCandidates(_ context.Context, key string, input provenance.CandidateRegistrationRequest) (provenance.CandidateRegistrationBatchReceipt, error) {
	stub.registerCalls++
	stub.registerKey, stub.registerInput = key, input
	return stub.registerResult, nil
}
func (stub *provenanceTransportStub) ApplyManualOperations(context.Context, provenance.ManualOperationsRequest) (provenance.ManualOperationBatchReceipt, error) {
	return provenance.ManualOperationBatchReceipt{}, nil
}
func (stub *provenanceTransportStub) Search(_ context.Context, input provenance.SearchRequest) (provenance.SearchResponse, error) {
	stub.searchCalls++
	stub.searchInput = input
	return stub.searchResult, stub.searchErr
}
func (stub *provenanceTransportStub) ListRepositoryProjections(_ context.Context, input provenance.RepositoryProjectionListRequest) (provenance.RepositoryProjectionList, error) {
	stub.repoListCalls++
	stub.repoListInput = input
	return stub.repoListResult, nil
}
func (stub *provenanceTransportStub) GetRepositoryProjection(_ context.Context, repositoryID string) (provenance.RepositoryCard, error) {
	stub.repoGetCalls++
	stub.repoGetID = repositoryID
	return stub.repoGetResult, nil
}
func (stub *provenanceTransportStub) SyncProjectProjection(_ context.Context, input provenance.ProjectProjectionSyncInput) (provenance.ProjectProjectionSyncReceipt, error) {
	stub.repoSyncInput = input
	return stub.repoSyncResult, nil
}

type provenanceAuthorizerStub struct {
	execution provenance.ExecutionAuthority
	err       error
	seen      []string
}

func (stub *provenanceAuthorizerStub) Authorize(_ context.Context, _ requestctx.Context, capability string) (provenance.ExecutionAuthority, error) {
	stub.seen = append(stub.seen, capability)
	if stub.err != nil {
		return provenance.ExecutionAuthority{}, stub.err
	}
	execution := stub.execution
	execution.Capability = capability
	return execution, nil
}

type provenanceIdempotencyStub struct {
	mu         sync.Mutex
	decision   string
	beginInput idempotency.BeginInput
	completed  bool
	failed     bool
	response   json.RawMessage
}

func (stub *provenanceIdempotencyStub) Begin(_ context.Context, input idempotency.BeginInput) (idempotency.BeginResult, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.beginInput = input
	decision := stub.decision
	if decision == "" {
		decision = idempotency.DecisionNew
	}
	return idempotency.BeginResult{Decision: decision, Record: idempotency.Record{
		IdempotencyID: "idem_test", Key: input.Key, ActorID: input.ActorID,
		NodeID: input.NodeID, ResponseSnapshot: append(json.RawMessage(nil), stub.response...),
	}}, nil
}

func (stub *provenanceIdempotencyStub) Complete(_ context.Context, _ string, _, _ string, snapshot any) error {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.completed = true
	stub.response, _ = json.Marshal(snapshot)
	return nil
}

func (stub *provenanceIdempotencyStub) Fail(_ context.Context, _ string, _ string, snapshot any) error {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.failed = true
	stub.response, _ = json.Marshal(snapshot)
	return nil
}

func provenanceHTTPTestHandler(transport provenance.FoundationTransport, authorizer ProvenanceAuthorizer, idem idempotencyService) http.Handler {
	return NewServer(Services{
		Provenance: transport, ProvenanceAuthorizer: authorizer, Idempotency: idem,
		ProvenanceRequestResolver: func(_ context.Context, correlationID string) (requestctx.Context, error) {
			return requestctx.Context{
				ActorID: "actor_authenticated", ActorKey: "owner",
				OriginNodeID: "node_authenticated", OriginNodeKey: "main",
				ScopeID: "scope_system", ScopeKey: "system", CorrelationID: correlationID,
			}, nil
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
}

type provenanceProjectSourceStub struct {
	source projectstate.ProvenanceProjectSource
	ref    string
	calls  int
}

func (stub *provenanceProjectSourceStub) ObserveProjectForProvenance(_ context.Context, ref string) (projectstate.ProvenanceProjectSource, error) {
	stub.calls++
	stub.ref = ref
	return stub.source, nil
}

type provenanceRepositoryProjectorStub struct {
	projection repostate.ProvenanceProjection
	inputs     []projectstate.ProvenanceRepositorySource
}

func (stub *provenanceRepositoryProjectorStub) ProjectForProvenance(_ context.Context, input projectstate.ProvenanceRepositorySource) repostate.ProvenanceProjection {
	stub.inputs = append(stub.inputs, input)
	result := stub.projection
	result.Source = input
	return result
}

type provenanceProjectReadAuthorizerStub struct {
	projectID string
	ref       string
}

func (stub *provenanceProjectReadAuthorizerStub) AuthorizeProjectRepositoryRead(_ context.Context, _ requestctx.Context, ref string) (string, error) {
	stub.ref = ref
	return stub.projectID, nil
}

func provenanceProjectionSyncHTTPTestHandler(transport provenance.FoundationTransport, authorizer ProvenanceAuthorizer, idem idempotencyService, source projectstate.ProvenanceProjectObserver, projector repostate.ProvenanceProjector, projectAuthorizer ProjectRepositoryReadAuthorizer) http.Handler {
	return NewServer(Services{
		Provenance: transport, ProvenanceAuthorizer: authorizer, Idempotency: idem,
		ProjectRepos: ProjectRepositoryServices{Provenance: source, RepositoryState: projector, Authorizer: projectAuthorizer,
			RequestResolver: func(_ context.Context, correlationID string) (requestctx.Context, error) {
				return requestctx.Context{ActorID: "actor_authenticated", OriginNodeID: "node_authenticated", CorrelationID: correlationID}, nil
			},
		},
		ProvenanceRequestResolver: func(_ context.Context, correlationID string) (requestctx.Context, error) {
			return requestctx.Context{ActorID: "actor_authenticated", ActorKey: "owner", OriginNodeID: "node_authenticated", OriginNodeKey: "main", ScopeID: "scope_system", ScopeKey: "system", CorrelationID: correlationID}, nil
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
}

func TestProvenanceMutationDeniesUnauthorizedActorNodeBeforeStoreAccess(t *testing.T) {
	transport := &provenanceTransportStub{}
	authorizer := &provenanceAuthorizerStub{err: &ProvenanceAuthorizationError{Decision: "deny", ReasonCode: "actor_not_authorized_on_target_node"}}
	handler := provenanceHTTPTestHandler(transport, authorizer, &provenanceIdempotencyStub{})
	req := httptest.NewRequest(http.MethodPost, "/v1/provenance/candidates", strings.NewReader(`{"candidates":[{"producer":{"producer_id":"owner","producer_kind":"source_claim"}}]}`))
	req.Header.Set(idempotency.Header, "unauthorized-attempt")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "provenance.forbidden") {
		t.Fatalf("response=%d %s", rec.Code, rec.Body.String())
	}
	if transport.registerCalls != 0 {
		t.Fatalf("provenance store accessed %d times", transport.registerCalls)
	}
}

func TestProvenanceRegistrationSeparatesExecutionAuthorityAndSemanticProducer(t *testing.T) {
	candidateID := provenance.SemanticID("11111111-1111-4111-8111-111111111111")
	sourceContext := strings.Repeat("semantic source body", 100)
	transport := &provenanceTransportStub{registerResult: provenance.CandidateRegistrationBatchReceipt{
		SchemaVersion: provenance.SchemaVersion,
		Receipts: []provenance.CandidateRegistrationReceipt{{
			SchemaVersion: provenance.SchemaVersion, CandidateID: candidateID, State: "pending",
			SourceResults: []provenance.SourceReference{{
				ID: "22222222-2222-4222-8222-222222222222", SchemaVersion: provenance.SchemaVersion,
				SourceKind: "codex_current_thread", Status: "unresolved", VerificationPosture: "unverified",
				SourceContext: &sourceContext, Submitted: json.RawMessage(`{"private":"source body"}`), Payload: json.RawMessage(`{"private":"source body"}`),
			}},
		}},
	}}
	authorizer := &provenanceAuthorizerStub{execution: provenance.ExecutionAuthority{
		ActorID: "actor_authenticated", OriginNodeID: "node_authenticated", PolicyDecisionID: "policy_decision_test",
	}}
	idem := &provenanceIdempotencyStub{}
	handler := provenanceHTTPTestHandler(transport, authorizer, idem)
	body := `{"candidates":[{"schema_version":"1.0","claim":"source says owner","record_kind":"decision","record_context":"evidence only","domain":"loom-development","visibility":"private","assertion_posture":"source_claim","temporal_interpretation":{"interpretation":"point in time"},"producer":{"producer_id":"actor_authenticated","producer_kind":"untrusted_source_name"},"sources":[{"kind":"codex_current_thread","status":"unresolved","verification_posture":"unverified","resolver_name":"test","resolver_version":"1","gap_reason":"not resolved","submitted":{}}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/provenance/candidates", strings.NewReader(body))
	req.Header.Set(idempotency.Header, "register-separate-identities")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("response=%d %s", rec.Code, rec.Body.String())
	}
	if transport.registerCalls != 1 || transport.registerKey != "register-separate-identities" {
		t.Fatalf("registration calls=%d key=%q", transport.registerCalls, transport.registerKey)
	}
	if got := transport.registerInput.Candidates[0].Producer.ProducerKind; got != "untrusted_source_name" {
		t.Fatalf("semantic producer changed to %q", got)
	}
	if idem.beginInput.ActorID != "actor_authenticated" || idem.beginInput.NodeID != "node_authenticated" {
		t.Fatalf("idempotency execution identity=%q/%q", idem.beginInput.ActorID, idem.beginInput.NodeID)
	}
	var envelope response.Envelope[provenance.AuthorizedCandidateRegistration]
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Execution.ActorID != "actor_authenticated" || envelope.Data.Execution.OriginNodeID != "node_authenticated" || envelope.Data.Execution.PolicyDecisionID != "policy_decision_test" {
		t.Fatalf("execution authority=%#v", envelope.Data.Execution)
	}
	for _, forbidden := range []string{"source_context", "submitted", "payload", "source body"} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Fatalf("registration receipt leaked %q: %s", forbidden, rec.Body.String())
		}
	}
	if envelope.Meta.IdempotencyKey != "register-separate-identities" || !idem.completed {
		t.Fatalf("idempotency meta=%#v complete=%v", envelope.Meta, idem.completed)
	}
}

func TestProvenanceRequestIdempotencyConflictIsStableBeforeStoreAccess(t *testing.T) {
	transport := &provenanceTransportStub{}
	authorizer := &provenanceAuthorizerStub{execution: provenance.ExecutionAuthority{ActorID: "actor_authenticated", OriginNodeID: "node_authenticated"}}
	idem := &provenanceIdempotencyStub{decision: idempotency.DecisionConflict}
	handler := provenanceHTTPTestHandler(transport, authorizer, idem)
	req := httptest.NewRequest(http.MethodPost, "/v1/provenance/candidates", strings.NewReader(`{"candidates":[]}`))
	req.Header.Set(idempotency.Header, "conflicting-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "idempotency.conflict") {
		t.Fatalf("response=%d %s", rec.Code, rec.Body.String())
	}
	if transport.registerCalls != 0 {
		t.Fatalf("provenance store accessed %d times", transport.registerCalls)
	}
}

func TestProvenancePaginationAndDeadlineErrorsAreBoundedAndStable(t *testing.T) {
	transport := &provenanceTransportStub{}
	authorizer := &provenanceAuthorizerStub{execution: provenance.ExecutionAuthority{ActorID: "actor_authenticated", OriginNodeID: "node_authenticated"}}
	handler := provenanceHTTPTestHandler(transport, authorizer, &provenanceIdempotencyStub{})

	partial := httptest.NewRequest(http.MethodGet, "/v1/provenance/candidates?after_time=2026-08-29T12:00:00Z", nil)
	partialRec := httptest.NewRecorder()
	handler.ServeHTTP(partialRec, partial)
	if partialRec.Code != http.StatusBadRequest || !strings.Contains(partialRec.Body.String(), "provenance.invalid_request") || transport.listCalls != 0 {
		t.Fatalf("partial cursor=%d %s calls=%d", partialRec.Code, partialRec.Body.String(), transport.listCalls)
	}

	transport.listCandidates = func(ctx context.Context, _ provenance.PageRequest) (provenance.Page[provenance.CandidateSummary], error) {
		<-ctx.Done()
		return provenance.Page[provenance.CandidateSummary]{}, ctx.Err()
	}
	parent, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	deadline := httptest.NewRequest(http.MethodGet, "/v1/provenance/candidates", nil).WithContext(parent)
	deadlineRec := httptest.NewRecorder()
	handler.ServeHTTP(deadlineRec, deadline)
	if deadlineRec.Code != http.StatusGatewayTimeout || !strings.Contains(deadlineRec.Body.String(), "provenance.deadline_exceeded") {
		t.Fatalf("deadline=%d %s", deadlineRec.Code, deadlineRec.Body.String())
	}
}

func TestProvenanceHealthIsMetadataOnlyAndSearchIsTypedReadOnly(t *testing.T) {
	repositoryID := "repo_01K41SEARCH000000000000002"
	transport := &provenanceTransportStub{readiness: provenance.RuntimeReadiness{
		State: provenance.ReadinessReady, Code: provenance.ReadinessCodeReady,
		Database: provenance.DatabaseName, Role: provenance.DatabaseRole, AppliedHead: provenance.SchemaHead, PackagedHead: provenance.SchemaHead,
	}}
	transport.searchResult = provenance.SearchResponse{
		SchemaVersion: provenance.SearchSchemaVersion, Query: "embeddings provenance",
		AcceptedRecords: provenance.SearchCollectionPage[provenance.AcceptedRecordSearchResult]{Items: []provenance.AcceptedRecordSearchResult{{
			RecordID: "11111111-1111-4111-8111-111111111111",
		}}},
		PendingCandidates: provenance.SearchCollectionPage[provenance.PendingCandidateSearchResult]{Items: []provenance.PendingCandidateSearchResult{{
			CandidateID: "22222222-2222-4222-8222-222222222222",
		}}},
		UnresolvedCases: provenance.SearchCollectionPage[provenance.UnresolvedCaseSearchResult]{Items: []provenance.UnresolvedCaseSearchResult{}},
		RepositoryState: provenance.SearchCollectionPage[provenance.RepositoryStateSearchResult]{Items: []provenance.RepositoryStateSearchResult{{
			RepositoryID: repositoryID,
			ExactGet: provenance.SearchExactGet{
				Resource: "repo", ID: repositoryID, Path: "/v1/provenance/repos/" + repositoryID,
			},
		}}},
		Returned: 3,
	}
	authorizer := &provenanceAuthorizerStub{execution: provenance.ExecutionAuthority{ActorID: "actor_authenticated", OriginNodeID: "node_authenticated"}}
	handler := provenanceHTTPTestHandler(transport, authorizer, &provenanceIdempotencyStub{})
	health := httptest.NewRequest(http.MethodGet, "/v1/provenance/health", nil)
	healthRec := httptest.NewRecorder()
	handler.ServeHTTP(healthRec, health)
	if healthRec.Code != http.StatusOK {
		t.Fatalf("health=%d %s", healthRec.Code, healthRec.Body.String())
	}
	for _, forbidden := range []string{"claim", "source_context", "submitted", "credential", "password"} {
		if strings.Contains(healthRec.Body.String(), forbidden) {
			t.Fatalf("health leaked %q: %s", forbidden, healthRec.Body.String())
		}
	}
	if len(authorizer.seen) != 1 || authorizer.seen[0] != capabilities.ProvenanceHealthReadCapability {
		t.Fatalf("health authorization=%v", authorizer.seen)
	}

	search := httptest.NewRequest(http.MethodPost, "/v1/provenance/search", bytes.NewBufferString(`{"query":"embeddings provenance","include_pending":true,"project":"project_test","repository":"repo_test","limit":4}`))
	searchRec := httptest.NewRecorder()
	handler.ServeHTTP(searchRec, search)
	if searchRec.Code != http.StatusOK {
		t.Fatalf("search=%d %s", searchRec.Code, searchRec.Body.String())
	}
	if transport.searchCalls != 1 || !transport.searchInput.IncludePending || transport.searchInput.Project != "project_test" || transport.searchInput.Repository != "repo_test" || transport.searchInput.Limit != 4 {
		t.Fatalf("typed search input=%#v calls=%d", transport.searchInput, transport.searchCalls)
	}
	var envelope response.Envelope[provenance.SearchResponse]
	if err := json.Unmarshal(searchRec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data.AcceptedRecords.Items) != 1 || len(envelope.Data.PendingCandidates.Items) != 1 || len(envelope.Data.RepositoryState.Items) != 1 || envelope.Data.RepositoryState.Items[0].RepositoryID != repositoryID || envelope.Data.RepositoryState.Items[0].ExactGet.Path != "/v1/provenance/repos/"+repositoryID || envelope.Data.AcceptedRecords.Items[0].RecordID == envelope.Data.PendingCandidates.Items[0].CandidateID {
		t.Fatalf("typed search collections were mixed: %#v", envelope.Data)
	}
	if len(authorizer.seen) != 2 || authorizer.seen[1] != capabilities.ProvenanceFoundationReadCapability {
		t.Fatalf("search authorization=%v", authorizer.seen)
	}
}

func TestProvenanceRepositoryRoutesUseTypedReadCapabilityAndExactID(t *testing.T) {
	repositoryID := "repo_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	transport := &provenanceTransportStub{repoListResult: provenance.RepositoryProjectionList{
		SchemaVersion: provenance.RepositoryCardSchemaVersion, Ordering: "repository_id_asc",
		Items: []provenance.RepositoryCard{{RepositoryID: repositoryID, Name: "Atlas Search", AcceptedContext: []provenance.RepositoryAcceptedContext{}, Diagnostics: []string{}, FieldSources: map[string]provenance.RepositoryFieldSource{}}},
	}, repoGetResult: provenance.RepositoryCard{RepositoryID: repositoryID, AcceptedContext: []provenance.RepositoryAcceptedContext{}, Diagnostics: []string{}, FieldSources: map[string]provenance.RepositoryFieldSource{}}}
	authorizer := &provenanceAuthorizerStub{execution: provenance.ExecutionAuthority{ActorID: "actor_authenticated", OriginNodeID: "node_authenticated"}}
	handler := provenanceHTTPTestHandler(transport, authorizer, &provenanceIdempotencyStub{})

	list := httptest.NewRequest(http.MethodGet, "/v1/provenance/repos?query=atlas&project=atlas&topic=provenance&role=component&tracking_status=valid&limit=3", nil)
	listRec := httptest.NewRecorder()
	handler.ServeHTTP(listRec, list)
	if listRec.Code != http.StatusOK || transport.repoListCalls != 1 {
		t.Fatalf("list response=%d %s calls=%d", listRec.Code, listRec.Body.String(), transport.repoListCalls)
	}
	if input := transport.repoListInput; input.Query != "atlas" || input.Project != "atlas" || input.Topic != "provenance" || input.Role != "component" || input.TrackingStatus != provenance.RepositoryTrackingValid || input.Limit != 3 {
		t.Fatalf("repository list input=%#v", input)
	}

	get := httptest.NewRequest(http.MethodGet, "/v1/provenance/repos/"+repositoryID, nil)
	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, get)
	if getRec.Code != http.StatusOK || transport.repoGetCalls != 1 || transport.repoGetID != repositoryID {
		t.Fatalf("get response=%d %s calls=%d id=%q", getRec.Code, getRec.Body.String(), transport.repoGetCalls, transport.repoGetID)
	}
	if len(authorizer.seen) != 2 || authorizer.seen[0] != capabilities.ProvenanceFoundationReadCapability || authorizer.seen[1] != capabilities.ProvenanceFoundationReadCapability {
		t.Fatalf("repository route authorization=%v", authorizer.seen)
	}
}

func TestProvenanceSearchHTTPKeepsAtlasRepositoriesAndCaseTyped(t *testing.T) {
	caseID := provenance.SemanticID("33333333-3333-4333-8333-333333333002")
	repositoryIDs := []string{"repo_01K41SEARCH000000000000002", "repo_01K41SEARCH000000000000003"}
	transport := &provenanceTransportStub{searchResult: provenance.SearchResponse{
		SchemaVersion: provenance.SearchSchemaVersion, Query: "Open Atlas.", Ordering: "stable",
		AcceptedRecords:   provenance.SearchCollectionPage[provenance.AcceptedRecordSearchResult]{Items: []provenance.AcceptedRecordSearchResult{}},
		PendingCandidates: provenance.SearchCollectionPage[provenance.PendingCandidateSearchResult]{Items: []provenance.PendingCandidateSearchResult{}},
		UnresolvedCases: provenance.SearchCollectionPage[provenance.UnresolvedCaseSearchResult]{Items: []provenance.UnresolvedCaseSearchResult{{
			CaseID: caseID, ExactGet: provenance.SearchExactGet{Resource: "case", ID: string(caseID), Path: "/v1/provenance/cases/" + string(caseID)},
		}}},
		RepositoryState: provenance.SearchCollectionPage[provenance.RepositoryStateSearchResult]{Items: []provenance.RepositoryStateSearchResult{
			{RepositoryID: repositoryIDs[0], ExactGet: provenance.SearchExactGet{Resource: "repo", ID: repositoryIDs[0], Path: "/v1/provenance/repos/" + repositoryIDs[0]}},
			{RepositoryID: repositoryIDs[1], ExactGet: provenance.SearchExactGet{Resource: "repo", ID: repositoryIDs[1], Path: "/v1/provenance/repos/" + repositoryIDs[1]}},
		}},
		Returned: 3,
	}}
	authorizer := &provenanceAuthorizerStub{execution: provenance.ExecutionAuthority{ActorID: "actor_authenticated", OriginNodeID: "node_authenticated"}}
	handler := provenanceHTTPTestHandler(transport, authorizer, &provenanceIdempotencyStub{})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/provenance/search", bytes.NewBufferString(`{"query":"Open Atlas."}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("search=%d %s", rec.Code, rec.Body.String())
	}
	var envelope response.Envelope[provenance.SearchResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data.RepositoryState.Items) != 2 || len(envelope.Data.UnresolvedCases.Items) != 1 || envelope.Data.RepositoryState.Items[0].RepositoryID != repositoryIDs[0] || envelope.Data.RepositoryState.Items[1].RepositoryID != repositoryIDs[1] || envelope.Data.UnresolvedCases.Items[0].CaseID != caseID {
		t.Fatalf("combined typed Atlas response=%#v", envelope.Data)
	}
	if strings.Contains(rec.Body.String(), "repo_01K41SEARCH000000000000001") {
		t.Fatalf("combined Atlas response contained unrelated LOOM repository: %s", rec.Body.String())
	}
}

func TestProvenanceProjectSyncIsManualOneProjectAndIdempotent(t *testing.T) {
	projectID := "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	repositoryID := "repo_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	source := &provenanceProjectSourceStub{source: projectstate.ProvenanceProjectSource{
		Projection: projectstate.ProjectProjection{Project: projectstate.ProjectIdentityProjection{ProjectID: projectID}},
		Repositories: []projectstate.ProvenanceRepositorySource{
			{Projection: projectstate.RepositoryProjection{RepositoryID: repositoryID, RepositoryOwnerProjectID: projectID}, Owned: true, Available: true, SourceVersion: 4},
			{Projection: projectstate.RepositoryProjection{RepositoryID: "repo_01ARZ3NDEKTSV4RRFFQ69G5FB0", RepositoryOwnerProjectID: projectID}, Owned: false},
		},
	}}
	projector := &provenanceRepositoryProjectorStub{}
	transport := &provenanceTransportStub{repoSyncResult: provenance.ProjectProjectionSyncReceipt{
		SchemaVersion: provenance.ProjectProjectionSchemaVersion, ProjectID: projectID, ProjectSnapshotID: "11111111-1111-4111-8111-111111111111", ProjectSourceVersion: 12, Repositories: []provenance.ProjectProjectionSyncRepositoryReceipt{},
	}}
	authorizer := &provenanceAuthorizerStub{execution: provenance.ExecutionAuthority{ActorID: "actor_authenticated", OriginNodeID: "node_authenticated", PolicyDecisionID: "policy_test"}}
	projectAuthorizer := &provenanceProjectReadAuthorizerStub{projectID: projectID}
	idem := &provenanceIdempotencyStub{}
	handler := provenanceProjectionSyncHTTPTestHandler(transport, authorizer, idem, source, projector, projectAuthorizer)
	req := httptest.NewRequest(http.MethodPost, "/v1/provenance/projects/atlas/sync", strings.NewReader(`{}`))
	req.Header.Set(idempotency.Header, "sync-one-project")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || source.calls != 1 || source.ref != projectID || projectAuthorizer.ref != "atlas" || len(projector.inputs) != 1 || projector.inputs[0].Projection.RepositoryID != repositoryID {
		t.Fatalf("sync response=%d %s source=%#v auth=%#v projected=%#v", rec.Code, rec.Body.String(), source, projectAuthorizer, projector.inputs)
	}
	if got := transport.repoSyncInput; got.Project.Project.ProjectID != projectID || len(got.Repositories) != 1 || got.Repositories[0].Source.Projection.RepositoryID != repositoryID {
		t.Fatalf("bounded project sync input=%#v", got)
	}
	if !idem.completed || len(authorizer.seen) != 1 || authorizer.seen[0] != capabilities.ProvenanceLifecycleCapability {
		t.Fatalf("sync idempotency=%#v authorization=%v", idem, authorizer.seen)
	}
}

func TestProvenanceMutationRequiresIdempotencyKey(t *testing.T) {
	transport := &provenanceTransportStub{}
	handler := provenanceHTTPTestHandler(transport, &provenanceAuthorizerStub{}, &provenanceIdempotencyStub{})
	req := httptest.NewRequest(http.MethodPost, "/v1/provenance/candidates", strings.NewReader(`{"candidates":[]}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "provenance.idempotency_key_required") || transport.registerCalls != 0 {
		t.Fatalf("response=%d %s calls=%d", rec.Code, rec.Body.String(), transport.registerCalls)
	}
}

func TestProvenanceOutputCapFailsClosed(t *testing.T) {
	large := strings.Repeat("x", provenance.MaximumFoundationResponseBytes+1)
	transport := &provenanceTransportStub{}
	transportGet := &provenanceExactTransportStub{provenanceTransportStub: transport, candidate: provenance.CandidateLifecycleProjection{
		Candidate: provenance.Candidate{ID: "11111111-1111-4111-8111-111111111111", Payload: json.RawMessage(`"` + large + `"`)},
	}}
	handler := provenanceHTTPTestHandler(transportGet, &provenanceAuthorizerStub{execution: provenance.ExecutionAuthority{ActorID: "actor_authenticated", OriginNodeID: "node_authenticated"}}, &provenanceIdempotencyStub{})
	req := httptest.NewRequest(http.MethodGet, "/v1/provenance/candidates/11111111-1111-4111-8111-111111111111", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "provenance.output_too_large") {
		t.Fatalf("response=%d %s", rec.Code, rec.Body.String())
	}
}

type provenanceExactTransportStub struct {
	*provenanceTransportStub
	candidate provenance.CandidateLifecycleProjection
}

func (stub *provenanceExactTransportStub) GetCandidate(context.Context, provenance.SemanticID, int) (provenance.CandidateLifecycleProjection, error) {
	return stub.candidate, nil
}

var _ provenance.FoundationTransport = (*provenanceTransportStub)(nil)
var _ provenance.FoundationTransport = (*provenanceExactTransportStub)(nil)
var _ provenance.SearchTransport = (*provenanceTransportStub)(nil)

func TestProvenanceExactSourcesMalformedQuery(t *testing.T) {
	for _, kind := range []string{"candidate", "record"} {
		for _, query := range []string{"sources=garbage", "sources=", "sources=true&sources=false", "sources=%zz"} {
			t.Run(kind+"/"+query, func(t *testing.T) {
				transport := &provenanceTransportStub{}
				handler := provenanceHTTPTestHandler(transport, &provenanceAuthorizerStub{execution: provenance.ExecutionAuthority{ActorID: "actor_authenticated", OriginNodeID: "node_authenticated"}}, &provenanceIdempotencyStub{})
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/provenance/"+kind+"s/22222222-2222-4222-8222-222222222222?"+query, nil))
				if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "provenance.invalid_request") {
					t.Fatalf("malformed sources query: %d %s", rec.Code, rec.Body.String())
				}
			})
		}
	}
}

type exactSourcesTransportStub struct {
	*provenanceTransportStub
	calls int
	kind  string
	limit int
	err   error
	claim string
}

func (s *exactSourcesTransportStub) expansion(kind string, id provenance.SemanticID, limit int) provenance.LinkedSources {
	s.calls++
	s.kind = kind
	s.limit = limit
	return provenance.LinkedSources{SchemaVersion: provenance.LinkedSourcesSchemaVersion, ParentKind: kind, ParentID: id, Posture: "stored_resolution", Items: []provenance.LinkedSourceItem{}}
}
func (s *exactSourcesTransportStub) GetCandidateWithSources(_ context.Context, id provenance.SemanticID, limit int) (provenance.CandidateWithSources, error) {
	return provenance.CandidateWithSources{CandidateLifecycleProjection: provenance.CandidateLifecycleProjection{Candidate: provenance.Candidate{ID: id, Claim: s.claim}, EffectiveState: "pending", Truncated: true}, Sources: s.expansion("candidate", id, limit)}, s.err
}
func (s *exactSourcesTransportStub) GetRecordWithSources(_ context.Context, id provenance.SemanticID, limit int) (provenance.RecordWithSources, error) {
	return provenance.RecordWithSources{RecordLifecycleProjection: provenance.RecordLifecycleProjection{Record: provenance.Record{ID: id, Claim: s.claim}, Truncated: true}, Sources: s.expansion("record", id, limit)}, s.err
}

func TestProvenanceExactSourcesHTTPContract(t *testing.T) {
	const id = "22222222-2222-4222-8222-222222222222"
	for _, kind := range []string{"candidate", "record"} {
		t.Run(kind, func(t *testing.T) {
			backend := &exactSourcesTransportStub{provenanceTransportStub: &provenanceTransportStub{}, claim: "original parent claim"}
			auth := &provenanceAuthorizerStub{execution: provenance.ExecutionAuthority{ActorID: "actor_authenticated", OriginNodeID: "node_authenticated"}}
			idem := &provenanceIdempotencyStub{}
			handler := provenanceHTTPTestHandler(backend, auth, idem)
			request := func(query string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodGet, "/v1/provenance/"+kind+"s/"+id+query, nil)
				r.Header.Set("X-Correlation-ID", "corr_exact_sources")
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			for _, tc := range []struct {
				query string
				limit int
			}{{"?sources=true", 50}, {"?sources=true&limit=16", 16}, {"?sources=true&limit=100", 100}} {
				w := request(tc.query)
				var result struct {
					Data struct {
						Sources   provenance.LinkedSources
						Truncated bool
					}
				}
				err := json.Unmarshal(w.Body.Bytes(), &result)
				if w.Code != 200 || err != nil || backend.kind != kind || backend.limit != tc.limit || result.Data.Sources.ParentID != id || result.Data.Sources.SourcesTruncated || !result.Data.Truncated || !strings.Contains(w.Body.String(), backend.claim) {
					t.Fatalf("expanded response %d %s error=%v", w.Code, w.Body.String(), err)
				}
			}
			before := backend.calls
			regular := request("")
			explicitFalse := request("?sources=false")
			// Compare the data projection; generated response metadata is independent.
			var a, b map[string]json.RawMessage
			_ = json.Unmarshal(regular.Body.Bytes(), &a)
			_ = json.Unmarshal(explicitFalse.Body.Bytes(), &b)
			if !bytes.Equal(a["data"], b["data"]) || backend.calls != before || strings.Contains(string(a["data"]), `"sources"`) {
				t.Fatalf("ordinary contract changed: %s %s", regular.Body.String(), explicitFalse.Body.String())
			}
			for _, query := range []string{"?sources=true&limit=101", "?sources=true&limit=-1"} {
				if w := request(query); w.Code != 400 {
					t.Fatalf("invalid limit accepted: %d", w.Code)
				}
			}
			backend.err = provenance.ErrFoundationNotFound
			if w := request("?sources=true"); w.Code != 404 {
				t.Fatalf("missing parent: %d %s", w.Code, w.Body.String())
			}
			backend.err = provenance.ErrLinkedSourcesRead
			if w := request("?sources=true"); w.Code != 500 || !strings.Contains(w.Body.String(), "provenance.sources_read_failed") {
				t.Fatalf("read failure: %d %s", w.Code, w.Body.String())
			}
			backend.err = nil
			backend.claim = strings.Repeat("x", provenance.MaximumFoundationResponseBytes)
			if w := request("?sources=true"); w.Code != 500 || !strings.Contains(w.Body.String(), "provenance.output_too_large") {
				t.Fatalf("parent silently shrunk: %d", w.Code)
			}
			auth.err = &ProvenanceAuthorizationError{ReasonCode: "denied"}
			before = backend.calls
			if w := request("?sources=true"); w.Code != 403 || backend.calls != before {
				t.Fatalf("read accessed before authorization: %d", w.Code)
			}
			for _, capability := range auth.seen {
				if capability != capabilities.ProvenanceFoundationReadCapability {
					t.Fatalf("unexpected authority: %s", capability)
				}
			}
			if idem.completed || idem.failed || idem.beginInput.Key != "" {
				t.Fatal("read touched idempotency ledger")
			}
			handler = provenanceHTTPTestHandler(&provenanceTransportStub{}, &provenanceAuthorizerStub{execution: provenance.ExecutionAuthority{ActorID: "actor_authenticated", OriginNodeID: "node_authenticated"}}, idem)
			if w := request("?sources=true"); w.Code != 501 || !strings.Contains(w.Body.String(), "provenance.sources_unsupported") {
				t.Fatalf("silent fallback: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
