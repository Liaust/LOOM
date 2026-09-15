package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"loom.local/loom/internal/capabilities"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/idempotency"
	"loom.local/loom/internal/policy"
	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/response"
)

const maximumProvenanceIdempotencyKeyBytes = 256

type ProvenanceRequestResolver func(context.Context, string) (requestctx.Context, error)

type ProvenanceAuthorizer interface {
	Authorize(context.Context, requestctx.Context, string) (provenance.ExecutionAuthority, error)
}

type provenancePolicyService interface {
	Explain(context.Context, requestctx.Context, policy.DecisionInput) (policy.PolicyExplanation, error)
}

type PolicyProvenanceAuthorizer struct{ service provenancePolicyService }

func NewPolicyProvenanceAuthorizer(service policy.Service) PolicyProvenanceAuthorizer {
	return PolicyProvenanceAuthorizer{service: service}
}

type ProvenanceAuthorizationError struct {
	Decision   string
	ReasonCode string
}

func (err *ProvenanceAuthorizationError) Error() string {
	return fmt.Sprintf("provenance authorization denied: %s", err.ReasonCode)
}

func (authorizer PolicyProvenanceAuthorizer) Authorize(ctx context.Context, req requestctx.Context, capability string) (provenance.ExecutionAuthority, error) {
	explanation, err := authorizer.service.Explain(ctx, req, policy.DecisionInput{
		Operation: "capability:" + capability,
		Metadata:  json.RawMessage(`{"surface":"loomd_http","semantic_identity_authority":"separate"}`),
	})
	if err != nil {
		return provenance.ExecutionAuthority{}, err
	}
	if explanation.Decision.Decision != policy.DecisionAllow {
		return provenance.ExecutionAuthority{}, &ProvenanceAuthorizationError{
			Decision: explanation.Decision.Decision, ReasonCode: explanation.Decision.ReasonCode,
		}
	}
	if explanation.Context.ActorID != req.ActorID || explanation.Context.OriginNodeID != req.OriginNodeID || explanation.Target.CapabilityAddress != capability {
		return provenance.ExecutionAuthority{}, errors.New("provenance policy context does not match authenticated request")
	}
	return provenance.ExecutionAuthority{
		ActorID: req.ActorID, OriginNodeID: req.OriginNodeID,
		PolicyDecisionID: explanation.Decision.PolicyDecisionID, Capability: capability,
	}, nil
}

func (s Server) handleProvenanceHealth(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx, cancel := provenanceRequestMeta(r)
	defer cancel()
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "provenance", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	if _, ok := s.authorizeProvenance(w, ctx, correlationID, capabilities.ProvenanceHealthReadCapability); !ok {
		return
	}
	if s.services.Provenance == nil {
		s.writeError(w, correlationID, http.StatusServiceUnavailable, "provenance.not_ready", "provenance", "runtime", "Provenance runtime is not ready.", nil)
		return
	}
	readiness := s.services.Provenance.Readiness()
	status := http.StatusOK
	if readiness.State != provenance.ReadinessReady {
		status = http.StatusServiceUnavailable
	}
	s.writeBoundedProvenanceJSON(w, correlationID, status, response.Success(correlationID, readiness))
}

func (s Server) handleProvenanceSearch(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx, cancel := provenanceRequestMeta(r)
	defer cancel()
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "provenance", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	if _, ok := s.authorizeProvenance(w, ctx, correlationID, capabilities.ProvenanceFoundationReadCapability); !ok {
		return
	}
	var input provenance.SearchRequest
	if err := decodeProvenanceRequest(w, r, &input); err != nil {
		s.writeProvenanceError(w, correlationID, "search", err)
		return
	}
	search, ok := s.provenanceService().(provenance.SearchTransport)
	if !ok {
		s.writeError(w, correlationID, http.StatusServiceUnavailable, "provenance.search_not_ready", "provenance", "search", "Provenance search is not ready.", nil)
		return
	}
	result, err := search.Search(ctx, input)
	if err != nil {
		s.writeProvenanceError(w, correlationID, "search", err)
		return
	}
	s.writeBoundedProvenanceJSON(w, correlationID, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleProvenanceRepositories(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx, cancel := provenanceRequestMeta(r)
	defer cancel()
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "provenance", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	if _, ok := s.authorizeProvenance(w, ctx, correlationID, capabilities.ProvenanceFoundationReadCapability); !ok {
		return
	}
	projection, ok := s.provenanceService().(provenance.ProjectionTransport)
	if !ok {
		s.writeError(w, correlationID, http.StatusServiceUnavailable, "provenance.projection_not_ready", "provenance", "repo", "Provenance repository projection is not ready.", nil)
		return
	}
	limit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			s.writeProvenanceError(w, correlationID, "repo", &provenance.FoundationValidationError{Err: errors.New("repository limit must be an integer")})
			return
		}
		limit = parsed
	}
	result, err := projection.ListRepositoryProjections(ctx, provenance.RepositoryProjectionListRequest{
		Query: r.URL.Query().Get("query"), Project: r.URL.Query().Get("project"),
		Topic: r.URL.Query().Get("topic"), Role: r.URL.Query().Get("role"),
		TrackingStatus: provenance.RepositoryTrackingStatus(r.URL.Query().Get("tracking_status")), Limit: limit,
	})
	if err != nil {
		s.writeProvenanceError(w, correlationID, "repo", err)
		return
	}
	s.writeBoundedProvenanceJSON(w, correlationID, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleProvenanceRepository(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx, cancel := provenanceRequestMeta(r)
	defer cancel()
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "provenance", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	if _, ok := s.authorizeProvenance(w, ctx, correlationID, capabilities.ProvenanceFoundationReadCapability); !ok {
		return
	}
	raw := strings.Trim(strings.TrimPrefix(r.URL.EscapedPath(), "/v1/provenance/repos/"), "/")
	if raw == "" || strings.Contains(raw, "/") {
		s.writeProvenanceError(w, correlationID, "repo", &provenance.FoundationValidationError{Err: errors.New("exact repository id is required")})
		return
	}
	repositoryID, err := url.PathUnescape(raw)
	if err != nil {
		s.writeProvenanceError(w, correlationID, "repo", &provenance.FoundationValidationError{Err: errors.New("repository id is invalid")})
		return
	}
	projection, ok := s.provenanceService().(provenance.ProjectionTransport)
	if !ok {
		s.writeError(w, correlationID, http.StatusServiceUnavailable, "provenance.projection_not_ready", "provenance", "repo", "Provenance repository projection is not ready.", nil)
		return
	}
	result, err := projection.GetRepositoryProjection(ctx, repositoryID)
	if err != nil {
		s.writeProvenanceError(w, correlationID, "repo", err)
		return
	}
	s.writeBoundedProvenanceJSON(w, correlationID, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleProvenanceProjectProjection(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.handleProvenanceProjectContext(w, r)
		return
	}
	correlationID, ctx, cancel := provenanceRequestMeta(r)
	defer cancel()
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "provenance", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	const prefix = "/v1/provenance/projects/"
	raw := strings.TrimPrefix(r.URL.EscapedPath(), prefix)
	if !strings.HasSuffix(raw, "/sync") {
		s.writeProvenanceError(w, correlationID, "project_sync", &provenance.FoundationValidationError{Err: errors.New("project sync path is invalid")})
		return
	}
	raw = strings.TrimSuffix(raw, "/sync")
	if raw == "" || strings.Contains(raw, "/") {
		s.writeProvenanceError(w, correlationID, "project_sync", &provenance.FoundationValidationError{Err: errors.New("one project reference is required")})
		return
	}
	projectRef, err := url.PathUnescape(raw)
	if err != nil || strings.TrimSpace(projectRef) == "" {
		s.writeProvenanceError(w, correlationID, "project_sync", &provenance.FoundationValidationError{Err: errors.New("project reference is invalid")})
		return
	}
	key, ok := s.requireProvenanceIdempotencyKey(w, r, correlationID)
	if !ok {
		return
	}
	execution, req, ok := s.authorizeProvenanceMutation(w, ctx, correlationID, capabilities.ProvenanceLifecycleCapability)
	if !ok {
		return
	}
	selectedProjectID, authorized := s.authorizeProjectRepositoryRead(w, ctx, correlationID, projectRef)
	if !authorized {
		return
	}
	input := map[string]string{"project_id": selectedProjectID}
	idemRecord, proceed := s.beginRecoverableIdempotency(w, r, ctx, correlationID, req, "provenance.project_projection.sync", input)
	if !proceed {
		return
	}
	if s.services.ProjectRepos.Provenance == nil || s.services.ProjectRepos.RepositoryState == nil {
		err := errors.New("bounded project repository projection source is unavailable")
		s.failProvenanceIdempotency(w, ctx, correlationID, key, idemRecord, "project_sync", err)
		return
	}
	projection, ok := s.provenanceService().(provenance.ProjectionTransport)
	if !ok {
		err := errors.New("provenance projection runtime is not ready")
		s.failProvenanceIdempotency(w, ctx, correlationID, key, idemRecord, "project_sync", err)
		return
	}
	refresh := provenance.ProjectRefreshService{Source: s.services.ProjectRepos.Provenance, Repositories: s.services.ProjectRepos.RepositoryState, Projection: projection}
	receipt, err := refresh.RefreshProject(ctx, selectedProjectID, false)
	if err != nil {
		s.failProvenanceIdempotency(w, ctx, correlationID, key, idemRecord, "project_sync", err)
		return
	}
	result := provenance.AuthorizedProjectProjectionSync{Execution: execution, Receipt: receipt}
	envelope := response.SuccessWithIdempotency(correlationID, key, result)
	s.completeIdempotency(ctx, idemRecord, "provenance_project_projection", string(receipt.ProjectSnapshotID), envelope)
	s.writeBoundedProvenanceJSON(w, correlationID, http.StatusOK, envelope)
}

func (s Server) handleProvenanceProjectContext(w http.ResponseWriter, r *http.Request) {
	cid, ctx, cancel := provenanceRequestMeta(r)
	defer cancel()
	if _, ok := s.authorizeProvenance(w, ctx, cid, capabilities.ProvenanceFoundationReadCapability); !ok {
		return
	}
	ref := strings.TrimPrefix(r.URL.Path, "/v1/provenance/projects/")
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || ref == "" || strings.Contains(ref, "/") || len(query) > 1 || (len(query) == 1 && (!query.Has("snapshot_id") || len(query["snapshot_id"]) != 1 || query.Get("snapshot_id") == "")) {
		s.writeProvenanceError(w, cid, "project", &provenance.FoundationValidationError{Err: errors.New("exact project id and optional snapshot_id required")})
		return
	}
	reader, ok := s.provenanceService().(provenance.ProjectProjectionReader)
	if !ok {
		s.writeError(w, cid, http.StatusServiceUnavailable, "provenance.projection_not_ready", "provenance", "project", "Project context is not ready.", nil)
		return
	}
	result, err := reader.GetProjectProjection(ctx, ref, provenance.SemanticID(query.Get("snapshot_id")))
	if err != nil {
		s.writeProvenanceError(w, cid, "project", err)
		return
	}
	s.writeBoundedProvenanceJSON(w, cid, http.StatusOK, response.Success(cid, result))
}

func (s Server) handleProvenanceCandidates(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx, cancel := provenanceRequestMeta(r)
	defer cancel()
	switch r.Method {
	case http.MethodGet:
		if _, ok := s.authorizeProvenance(w, ctx, correlationID, capabilities.ProvenanceFoundationReadCapability); !ok {
			return
		}
		pageRequest, err := provenancePageRequest(r)
		if err != nil {
			s.writeProvenanceError(w, correlationID, "candidates", err)
			return
		}
		items, err := s.provenanceService().ListCandidates(ctx, pageRequest)
		if err != nil {
			s.writeProvenanceError(w, correlationID, "candidates", err)
			return
		}
		s.writeBoundedProvenanceJSON(w, correlationID, http.StatusOK, response.Success(correlationID, items))
	case http.MethodPost:
		key, ok := s.requireProvenanceIdempotencyKey(w, r, correlationID)
		if !ok {
			return
		}
		var input provenance.CandidateRegistrationRequest
		if err := decodeProvenanceRequest(w, r, &input); err != nil {
			s.writeProvenanceError(w, correlationID, "candidates", err)
			return
		}
		execution, req, ok := s.authorizeProvenanceMutation(w, ctx, correlationID, capabilities.ProvenanceRegisterCapability)
		if !ok {
			return
		}
		idemRecord, proceed := s.beginRecoverableIdempotency(w, r, ctx, correlationID, req, "provenance.candidate.register", input)
		if !proceed {
			return
		}
		receipt, err := s.provenanceService().RegisterCandidates(ctx, key, input)
		if err != nil {
			s.failProvenanceIdempotency(w, ctx, correlationID, key, idemRecord, "candidates", err)
			return
		}
		result := provenance.AuthorizedCandidateRegistration{
			Execution: execution,
			Receipt:   provenance.ProjectCandidateRegistrationReceipt(receipt),
		}
		envelope := response.SuccessWithIdempotency(correlationID, key, result)
		resultRef := "candidate_batch"
		if len(receipt.Receipts) > 0 {
			resultRef = string(receipt.Receipts[0].CandidateID)
		}
		s.completeIdempotency(ctx, idemRecord, "provenance_candidate_registration", resultRef, envelope)
		s.writeBoundedProvenanceJSON(w, correlationID, http.StatusOK, envelope)
	default:
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "provenance", r.URL.Path, "Method is not allowed.", nil)
	}
}

func (s Server) handleProvenanceCandidate(w http.ResponseWriter, r *http.Request) {
	s.handleProvenanceExactCandidate(w, r)
}

func (s Server) handleProvenanceExactCandidate(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx, cancel := provenanceRequestMeta(r)
	defer cancel()
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "provenance", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	if _, ok := s.authorizeProvenance(w, ctx, correlationID, capabilities.ProvenanceFoundationReadCapability); !ok {
		return
	}
	id, limit, err := provenanceExactRequest(r, "/v1/provenance/candidates/")
	if err != nil {
		s.writeProvenanceError(w, correlationID, "candidate", err)
		return
	}
	if s.handleProvenanceSources(w, r, ctx, correlationID, "candidate", id, limit) {
		return
	}
	item, err := s.provenanceService().GetCandidate(ctx, id, limit)
	if err != nil {
		s.writeProvenanceError(w, correlationID, "candidate", err)
		return
	}
	s.writeBoundedProvenanceJSON(w, correlationID, http.StatusOK, response.Success(correlationID, item))
}

func (s Server) handleProvenanceRecords(w http.ResponseWriter, r *http.Request) {
	s.handleProvenanceList(w, r, "records", func(ctx context.Context, request provenance.PageRequest) (any, error) {
		return s.provenanceService().ListRecords(ctx, request)
	})
}

func (s Server) handleProvenanceRecord(w http.ResponseWriter, r *http.Request) {
	s.handleProvenanceExact(w, r, "record", "/v1/provenance/records/", func(ctx context.Context, id provenance.SemanticID, limit int) (any, error) {
		return s.provenanceService().GetRecord(ctx, id, limit)
	})
}

func (s Server) handleProvenanceRelationships(w http.ResponseWriter, r *http.Request) {
	s.handleProvenanceList(w, r, "relationships", func(ctx context.Context, request provenance.PageRequest) (any, error) {
		return s.provenanceService().ListRelationships(ctx, request)
	})
}

func (s Server) handleProvenanceRelationship(w http.ResponseWriter, r *http.Request) {
	s.handleProvenanceExact(w, r, "relationship", "/v1/provenance/relationships/", func(ctx context.Context, id provenance.SemanticID, limit int) (any, error) {
		return s.provenanceService().GetRelationship(ctx, id, limit)
	})
}

func (s Server) handleProvenanceCases(w http.ResponseWriter, r *http.Request) {
	s.handleProvenanceList(w, r, "cases", func(ctx context.Context, request provenance.PageRequest) (any, error) {
		return s.provenanceService().ListResolutionCases(ctx, request)
	})
}

func (s Server) handleProvenanceCase(w http.ResponseWriter, r *http.Request) {
	s.handleProvenanceExact(w, r, "case", "/v1/provenance/cases/", func(ctx context.Context, id provenance.SemanticID, limit int) (any, error) {
		return s.provenanceService().GetResolutionCase(ctx, id, limit)
	})
}

func (s Server) handleProvenanceOperations(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx, cancel := provenanceRequestMeta(r)
	defer cancel()
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "provenance", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	key, ok := s.requireProvenanceIdempotencyKey(w, r, correlationID)
	if !ok {
		return
	}
	var input provenance.ManualOperationsRequest
	if err := decodeProvenanceRequest(w, r, &input); err != nil {
		s.writeProvenanceError(w, correlationID, "operations", err)
		return
	}
	execution, req, ok := s.authorizeProvenanceMutation(w, ctx, correlationID, capabilities.ProvenanceLifecycleCapability)
	if !ok {
		return
	}
	idemRecord, proceed := s.beginRecoverableIdempotency(w, r, ctx, correlationID, req, "provenance.lifecycle.apply", input)
	if !proceed {
		return
	}
	receipt, err := s.provenanceService().ApplyManualOperations(ctx, input)
	if err != nil {
		s.failProvenanceIdempotency(w, ctx, correlationID, key, idemRecord, "operations", err)
		return
	}
	result := provenance.AuthorizedManualOperations{Execution: execution, Receipt: receipt}
	envelope := response.SuccessWithIdempotency(correlationID, key, result)
	resultRef := "operation_batch"
	if len(receipt.Receipts) > 0 {
		resultRef = string(receipt.Receipts[0].OperationID)
	}
	s.completeIdempotency(ctx, idemRecord, "provenance_manual_operations", resultRef, envelope)
	s.writeBoundedProvenanceJSON(w, correlationID, http.StatusOK, envelope)
}

func (s Server) handleProvenanceList(w http.ResponseWriter, r *http.Request, target string, list func(context.Context, provenance.PageRequest) (any, error)) {
	correlationID, ctx, cancel := provenanceRequestMeta(r)
	defer cancel()
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "provenance", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	if _, ok := s.authorizeProvenance(w, ctx, correlationID, capabilities.ProvenanceFoundationReadCapability); !ok {
		return
	}
	request, err := provenancePageRequest(r)
	if err != nil {
		s.writeProvenanceError(w, correlationID, target, err)
		return
	}
	items, err := list(ctx, request)
	if err != nil {
		s.writeProvenanceError(w, correlationID, target, err)
		return
	}
	s.writeBoundedProvenanceJSON(w, correlationID, http.StatusOK, response.Success(correlationID, items))
}

func (s Server) handleProvenanceExact(w http.ResponseWriter, r *http.Request, target, prefix string, get func(context.Context, provenance.SemanticID, int) (any, error)) {
	correlationID, ctx, cancel := provenanceRequestMeta(r)
	defer cancel()
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "provenance", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	if _, ok := s.authorizeProvenance(w, ctx, correlationID, capabilities.ProvenanceFoundationReadCapability); !ok {
		return
	}
	id, limit, err := provenanceExactRequest(r, prefix)
	if err != nil {
		s.writeProvenanceError(w, correlationID, target, err)
		return
	}
	if target == "record" && s.handleProvenanceSources(w, r, ctx, correlationID, target, id, limit) {
		return
	}
	item, err := get(ctx, id, limit)
	if err != nil {
		s.writeProvenanceError(w, correlationID, target, err)
		return
	}
	s.writeBoundedProvenanceJSON(w, correlationID, http.StatusOK, response.Success(correlationID, item))
}

func (s Server) provenanceService() provenance.FoundationTransport {
	return s.services.Provenance
}

func provenanceRequestMeta(r *http.Request) (string, context.Context, context.CancelFunc) {
	correlationID, ctx := requestMeta(r)
	ctx, cancel := context.WithTimeout(ctx, provenance.FoundationRequestTimeout)
	return correlationID, ctx, cancel
}

func (s Server) authorizeProvenance(w http.ResponseWriter, ctx context.Context, correlationID, capability string) (provenance.ExecutionAuthority, bool) {
	_, execution, ok := s.resolveAndAuthorizeProvenance(w, ctx, correlationID, capability)
	return execution, ok
}

func (s Server) authorizeProvenanceMutation(w http.ResponseWriter, ctx context.Context, correlationID, capability string) (provenance.ExecutionAuthority, requestctx.Context, bool) {
	req, execution, ok := s.resolveAndAuthorizeProvenance(w, ctx, correlationID, capability)
	return execution, req, ok
}

func (s Server) resolveAndAuthorizeProvenance(w http.ResponseWriter, ctx context.Context, correlationID, capability string) (requestctx.Context, provenance.ExecutionAuthority, bool) {
	resolver := s.services.ProvenanceRequestResolver
	if resolver == nil {
		resolver = func(ctx context.Context, correlationID string) (requestctx.Context, error) {
			return requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		}
	}
	req, err := resolver(ctx, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusServiceUnavailable, "provenance.request_context_unavailable", "provenance", "authorization", "Authenticated provenance request context is unavailable.", err)
		return requestctx.Context{}, provenance.ExecutionAuthority{}, false
	}
	if strings.TrimSpace(req.ActorID) == "" || strings.TrimSpace(req.OriginNodeID) == "" {
		s.writeError(w, correlationID, http.StatusForbidden, "provenance.forbidden", "provenance", "authorization", "Provenance request actor and node are not authorized.", nil)
		return requestctx.Context{}, provenance.ExecutionAuthority{}, false
	}
	if s.services.ProvenanceAuthorizer == nil {
		s.writeError(w, correlationID, http.StatusServiceUnavailable, "provenance.authorization_unavailable", "provenance", "authorization", "Provenance authorization is unavailable.", nil)
		return requestctx.Context{}, provenance.ExecutionAuthority{}, false
	}
	execution, err := s.services.ProvenanceAuthorizer.Authorize(ctx, req, capability)
	if err != nil {
		var denied *ProvenanceAuthorizationError
		if errors.As(err, &denied) {
			s.writeError(w, correlationID, http.StatusForbidden, "provenance.forbidden", "provenance", "authorization", "Provenance request actor and node are not authorized.", err)
			return requestctx.Context{}, provenance.ExecutionAuthority{}, false
		}
		s.writeError(w, correlationID, http.StatusServiceUnavailable, "provenance.authorization_failed", "provenance", "authorization", "Provenance authorization could not be evaluated.", err)
		return requestctx.Context{}, provenance.ExecutionAuthority{}, false
	}
	if s.services.Provenance == nil {
		s.writeError(w, correlationID, http.StatusServiceUnavailable, "provenance.not_ready", "provenance", "runtime", "Provenance runtime is not ready.", nil)
		return requestctx.Context{}, provenance.ExecutionAuthority{}, false
	}
	return req, execution, true
}

func provenancePageRequest(r *http.Request) (provenance.PageRequest, error) {
	query := r.URL.Query()
	limit, err := provenanceLimit(query.Get("limit"))
	if err != nil {
		return provenance.PageRequest{}, &provenance.FoundationValidationError{Err: err}
	}
	request := provenance.PageRequest{
		Limit: limit, Domain: strings.TrimSpace(query.Get("domain")), Visibility: strings.TrimSpace(query.Get("visibility")),
	}
	afterTime, afterID := strings.TrimSpace(query.Get("after_time")), strings.TrimSpace(query.Get("after_id"))
	if (afterTime == "") != (afterID == "") {
		return provenance.PageRequest{}, &provenance.FoundationValidationError{Err: errors.New("pagination cursor requires both after_time and after_id")}
	}
	if afterTime != "" {
		parsedTime, err := time.Parse(time.RFC3339Nano, afterTime)
		if err != nil {
			return provenance.PageRequest{}, &provenance.FoundationValidationError{Err: errors.New("after_time must use RFC3339Nano")}
		}
		parsedID, err := provenance.ParseSemanticID(afterID)
		if err != nil {
			return provenance.PageRequest{}, &provenance.FoundationValidationError{Err: err}
		}
		parsedTime = parsedTime.UTC()
		request.AfterTime, request.AfterID = &parsedTime, &parsedID
	}
	if err := provenance.ValidateFoundationPageRequest(request); err != nil {
		return provenance.PageRequest{}, &provenance.FoundationValidationError{Err: err}
	}
	return request, nil
}

func provenanceExactRequest(r *http.Request, prefix string) (provenance.SemanticID, int, error) {
	ref := strings.Trim(pathRef(r.URL.Path, prefix), "/")
	if ref == "" || strings.Contains(ref, "/") {
		return "", 0, &provenance.FoundationValidationError{Err: errors.New("exact semantic UUID is required")}
	}
	id, err := provenance.ParseSemanticID(ref)
	if err != nil {
		return "", 0, &provenance.FoundationValidationError{Err: err}
	}
	limit, err := provenanceLimit(r.URL.Query().Get("limit"))
	if err != nil {
		return "", 0, &provenance.FoundationValidationError{Err: err}
	}
	return id, limit, nil
}

func provenanceLimit(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return provenance.DefaultPageLimit, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > provenance.MaximumPageLimit {
		return 0, fmt.Errorf("limit must be between 1 and %d", provenance.MaximumPageLimit)
	}
	return limit, nil
}

func decodeProvenanceRequest(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, provenance.MaximumFoundationRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return &provenance.FoundationValidationError{Err: fmt.Errorf("request exceeds %d bytes", provenance.MaximumFoundationRequestBytes)}
		}
		return &provenance.FoundationValidationError{Err: err}
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return &provenance.FoundationValidationError{Err: errors.New("request body must contain one JSON object")}
	} else if !errors.Is(err, io.EOF) {
		return &provenance.FoundationValidationError{Err: err}
	}
	return nil
}

func (s Server) requireProvenanceIdempotencyKey(w http.ResponseWriter, r *http.Request, correlationID string) (string, bool) {
	key := strings.TrimSpace(r.Header.Get(idempotency.Header))
	if key == "" || len(key) > maximumProvenanceIdempotencyKeyBytes {
		s.writeError(w, correlationID, http.StatusBadRequest, "provenance.idempotency_key_required", "provenance", "idempotency", "A bounded idempotency key is required for provenance mutations.", nil)
		return "", false
	}
	return key, true
}

func (s Server) failProvenanceIdempotency(w http.ResponseWriter, ctx context.Context, correlationID, key string, record idempotency.Record, target string, err error) {
	status, code, summary := provenanceErrorResponse(err)
	idemErr := loomerrors.Wrap(code, "provenance", target, summary, err)
	failure := response.FailureWithIdempotency(correlationID, key, idemErr)
	s.failIdempotency(ctx, record, code, failure)
	s.writeErrorWithIdempotency(w, correlationID, key, status, code, "provenance", target, summary, err)
}

func (s Server) writeProvenanceError(w http.ResponseWriter, correlationID, target string, err error) {
	status, code, summary := provenanceErrorResponse(err)
	s.writeError(w, correlationID, status, code, "provenance", target, summary, err)
}

func provenanceErrorResponse(err error) (int, string, string) {
	var validation *provenance.FoundationValidationError
	var conflict *provenance.FoundationConflictError
	switch {
	case errors.As(err, &validation):
		return http.StatusBadRequest, "provenance.invalid_request", "Provenance request is invalid."
	case errors.Is(err, provenance.ErrFoundationNotFound):
		return http.StatusNotFound, "provenance.not_found", "Provenance object was not found."
	case errors.Is(err, provenance.ErrRepositoryProjectionNotFound):
		return http.StatusNotFound, "provenance.repo_not_found", "Provenance repository projection was not found."
	case errors.Is(err, provenance.ErrProjectProjectionNotFound):
		return http.StatusNotFound, "provenance.project_not_found", "Provenance project snapshot was not found."
	case errors.As(err, &conflict), errors.Is(err, provenance.ErrReplayConflict), errors.Is(err, provenance.ErrOperationReplayConflict), errors.Is(err, provenance.ErrPartialOperationReplay), errors.Is(err, provenance.ErrLifecycleConflict):
		return http.StatusConflict, "provenance.conflict", "Provenance request conflicts with durable state."
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "provenance.deadline_exceeded", "Provenance request exceeded its deadline."
	case errors.Is(err, context.Canceled):
		return http.StatusRequestTimeout, "provenance.request_canceled", "Provenance request was canceled."
	case errors.Is(err, provenance.ErrLinkedSourcesRead):
		return http.StatusInternalServerError, "provenance.sources_read_failed", "Linked source receipts could not be read."
	default:
		return http.StatusInternalServerError, "provenance.failed", "Provenance request failed."
	}
}

func (s Server) writeBoundedProvenanceJSON(w http.ResponseWriter, correlationID string, status int, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "provenance.response_invalid", "provenance", "response", "Provenance response could not be encoded.", err)
		return
	}
	if len(payload) > provenance.MaximumFoundationResponseBytes {
		s.writeError(w, correlationID, http.StatusInternalServerError, "provenance.output_too_large", "provenance", "response", "Provenance response exceeded its output bound.", nil)
		return
	}
	writeRawJSON(w, status, payload)
}

// handleProvenanceSources runs only after the existing foundation authorization
// and exact-parent validation. false is precisely the ordinary read path.
func (s Server) handleProvenanceSources(w http.ResponseWriter, r *http.Request, ctx context.Context, correlationID, kind string, id provenance.SemanticID, limit int) bool {
	values, err := url.ParseQuery(r.URL.RawQuery)
	requested := false
	if raw, present := values["sources"]; present && err == nil {
		if len(raw) != 1 || (raw[0] != "true" && raw[0] != "false") {
			err = errors.New("sources must appear once as true or false")
		} else {
			requested = raw[0] == "true"
		}
	}
	if err != nil {
		s.writeProvenanceError(w, correlationID, kind, &provenance.FoundationValidationError{Err: err})
		return true
	}
	if !requested {
		return false
	}
	reader, ok := s.provenanceService().(provenance.LinkedSourcesTransport)
	if !ok {
		s.writeError(w, correlationID, http.StatusNotImplemented, "provenance.sources_unsupported", "provenance", kind, "This backend does not support linked source expansion.", nil)
		return true
	}
	var item any
	if kind == "candidate" {
		item, err = reader.GetCandidateWithSources(ctx, id, limit)
	} else {
		item, err = reader.GetRecordWithSources(ctx, id, limit)
	}
	if err != nil {
		s.writeProvenanceError(w, correlationID, kind, err)
	} else {
		s.writeBoundedProvenanceJSON(w, correlationID, http.StatusOK, response.Success(correlationID, item))
	}
	return true
}
