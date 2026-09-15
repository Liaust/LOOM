package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/policy"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/storagearchive"
)

type WorkspaceArchiveLifecycleService interface {
	ReviewArchivePlan(context.Context, storagearchive.WorkspaceArchivePlanInput) (storagearchive.WorkspaceMovePlanReview, error)
	ReviewRestorePlan(context.Context, storagearchive.WorkspaceRestorePlanInput) (storagearchive.WorkspaceMovePlanReview, error)
	ReplanReviewed(context.Context, storagearchive.WorkspaceMovePlanReview) (storagearchive.WorkspaceArchivePlan, error)
	ApplyArchive(context.Context, storagearchive.WorkspaceArchivePlan, string) (storagearchive.WorkspaceArchiveInspection, error)
	ApplyRestore(context.Context, storagearchive.WorkspaceArchivePlan, string) (storagearchive.WorkspaceArchiveInspection, error)
	InspectOperation(context.Context, string) (storagearchive.WorkspaceArchiveInspection, error)
	RecoverOperation(context.Context, string) (storagearchive.WorkspaceArchiveInspection, error)
}

type WorkspaceArchiveRequestResolver func(context.Context, string) (requestctx.Context, error)

type WorkspaceArchiveAuthorizer interface {
	Authorize(context.Context, requestctx.Context, string) error
}

type workspaceArchivePolicyService interface {
	Explain(context.Context, requestctx.Context, policy.DecisionInput) (policy.PolicyExplanation, error)
}

type PolicyWorkspaceArchiveAuthorizer struct{ service workspaceArchivePolicyService }

func NewPolicyWorkspaceArchiveAuthorizer(service policy.Service) PolicyWorkspaceArchiveAuthorizer {
	return PolicyWorkspaceArchiveAuthorizer{service: service}
}

type WorkspaceArchiveAuthorizationError struct{ ReasonCode string }

func (err *WorkspaceArchiveAuthorizationError) Error() string {
	return "workspace archive authorization denied: " + err.ReasonCode
}

func (authorizer PolicyWorkspaceArchiveAuthorizer) Authorize(ctx context.Context, req requestctx.Context, capability string) error {
	explanation, err := authorizer.service.Explain(ctx, req, policy.DecisionInput{
		Operation: "capability:" + capability,
		Metadata:  json.RawMessage(`{"surface":"loomd_http","filesystem_authority":"none"}`),
	})
	if err != nil {
		return err
	}
	if explanation.Decision.Decision != policy.DecisionAllow {
		return &WorkspaceArchiveAuthorizationError{ReasonCode: explanation.Decision.ReasonCode}
	}
	if explanation.Context.ActorID != req.ActorID || explanation.Context.OriginNodeID != req.OriginNodeID || explanation.Context.ScopeID != req.ScopeID || explanation.Target.CapabilityAddress != capability {
		return errors.New("workspace archive policy context does not match authenticated request")
	}
	return nil
}

func (s Server) handleWorkspaceArchivePlan(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx, cancel := workspaceArchiveRequestMeta(r)
	defer cancel()
	if !s.requireWorkspaceArchiveMethod(w, r, correlationID, http.MethodPost) {
		return
	}
	req, ok := s.authorizeWorkspaceArchive(w, ctx, correlationID, capabilities.WorkspaceArchivePlanCapability)
	if !ok {
		return
	}
	var input storagearchive.WorkspaceArchivePlanRequest
	if !s.decodeWorkspaceArchiveRequest(w, r, correlationID, &input) {
		return
	}
	service, ok := s.workspaceArchiveService(w, correlationID)
	if !ok {
		return
	}
	review, err := service.ReviewArchivePlan(ctx, storagearchive.WorkspaceArchivePlanInput{
		OperationID: input.OperationID, Kind: input.Kind, ObjectID: input.ObjectID,
		Slug: input.Slug, ActorID: req.ActorID, Reason: input.Reason,
	})
	if err != nil {
		s.writeWorkspaceArchiveError(w, correlationID, "plan", err)
		return
	}
	s.writeBoundedWorkspaceArchiveJSON(w, correlationID, http.StatusOK, response.Success(correlationID, review))
}

func (s Server) handleWorkspaceArchiveRestorePlan(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx, cancel := workspaceArchiveRequestMeta(r)
	defer cancel()
	if !s.requireWorkspaceArchiveMethod(w, r, correlationID, http.MethodPost) {
		return
	}
	req, ok := s.authorizeWorkspaceArchive(w, ctx, correlationID, capabilities.WorkspaceArchiveRestorePlanCapability)
	if !ok {
		return
	}
	var input storagearchive.WorkspaceRestorePlanRequest
	if !s.decodeWorkspaceArchiveRequest(w, r, correlationID, &input) {
		return
	}
	service, ok := s.workspaceArchiveService(w, correlationID)
	if !ok {
		return
	}
	review, err := service.ReviewRestorePlan(ctx, storagearchive.WorkspaceRestorePlanInput{
		OperationID: input.OperationID, ArchiveOperationID: input.ArchiveOperationID,
		ActorID: req.ActorID, Reason: input.Reason,
	})
	if err != nil {
		s.writeWorkspaceArchiveError(w, correlationID, "restore-plan", err)
		return
	}
	s.writeBoundedWorkspaceArchiveJSON(w, correlationID, http.StatusOK, response.Success(correlationID, review))
}

func (s Server) handleWorkspaceArchiveApply(w http.ResponseWriter, r *http.Request) {
	s.handleWorkspaceArchiveApplyKind(w, r, storagearchive.WorkspaceOperationArchive, capabilities.WorkspaceArchiveApplyCapability)
}

func (s Server) handleWorkspaceArchiveRestoreApply(w http.ResponseWriter, r *http.Request) {
	s.handleWorkspaceArchiveApplyKind(w, r, storagearchive.WorkspaceOperationRestore, capabilities.WorkspaceArchiveRestoreApplyCapability)
}

func (s Server) handleWorkspaceArchiveApplyKind(w http.ResponseWriter, r *http.Request, kind storagearchive.WorkspaceOperationKind, capability string) {
	correlationID, ctx, cancel := workspaceArchiveRequestMeta(r)
	defer cancel()
	if !s.requireWorkspaceArchiveMethod(w, r, correlationID, http.MethodPost) {
		return
	}
	req, ok := s.authorizeWorkspaceArchive(w, ctx, correlationID, capability)
	if !ok {
		return
	}
	var input storagearchive.WorkspaceMoveApplyRequest
	if !s.decodeWorkspaceArchiveRequest(w, r, correlationID, &input) {
		return
	}
	if !input.Confirm || input.PlanDigest == "" || input.PlanDigest != input.Plan.PlanDigest || input.Plan.OperationKind != kind || input.Plan.ActorID != req.ActorID {
		s.writeError(w, correlationID, http.StatusBadRequest, "workspace_archive.confirmation_required", "workspace_archive", "apply", "Exact plan digest and explicit confirmation are required.", nil)
		return
	}
	service, ok := s.workspaceArchiveService(w, correlationID)
	if !ok {
		return
	}
	plan, err := service.ReplanReviewed(ctx, input.Plan)
	if err != nil {
		s.writeWorkspaceArchiveError(w, correlationID, "apply", err)
		return
	}
	var inspection storagearchive.WorkspaceArchiveInspection
	if kind == storagearchive.WorkspaceOperationArchive {
		inspection, err = service.ApplyArchive(ctx, plan, input.PlanDigest)
	} else {
		inspection, err = service.ApplyRestore(ctx, plan, input.PlanDigest)
	}
	if err != nil {
		s.writeWorkspaceArchiveError(w, correlationID, "apply", err)
		return
	}
	s.writeBoundedWorkspaceArchiveJSON(w, correlationID, http.StatusOK, response.Success(correlationID, storagearchive.NewWorkspaceMoveInspectionSummary(inspection)))
}

func (s Server) handleWorkspaceArchiveInspect(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx, cancel := workspaceArchiveRequestMeta(r)
	defer cancel()
	if !s.requireWorkspaceArchiveMethod(w, r, correlationID, http.MethodGet) {
		return
	}
	req, ok := s.authorizeWorkspaceArchive(w, ctx, correlationID, capabilities.WorkspaceArchiveInspectCapability)
	if !ok {
		return
	}
	operationID := strings.TrimSpace(r.URL.Query().Get("operation_id"))
	service, ok := s.workspaceArchiveService(w, correlationID)
	if !ok {
		return
	}
	inspection, err := service.InspectOperation(ctx, operationID)
	if err != nil {
		s.writeWorkspaceArchiveError(w, correlationID, "inspect", err)
		return
	}
	if inspection.Operation.ActorID != req.ActorID {
		s.writeError(w, correlationID, http.StatusForbidden, "workspace_archive.forbidden", "workspace_archive", "inspect", "Workspace archive operation is not authorized.", nil)
		return
	}
	s.writeBoundedWorkspaceArchiveJSON(w, correlationID, http.StatusOK, response.Success(correlationID, storagearchive.NewWorkspaceMoveInspectionSummary(inspection)))
}

func (s Server) handleWorkspaceArchiveRecover(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx, cancel := workspaceArchiveRequestMeta(r)
	defer cancel()
	if !s.requireWorkspaceArchiveMethod(w, r, correlationID, http.MethodPost) {
		return
	}
	req, ok := s.authorizeWorkspaceArchive(w, ctx, correlationID, capabilities.WorkspaceArchiveRecoverCapability)
	if !ok {
		return
	}
	var input storagearchive.WorkspaceMoveRecoverRequest
	if !s.decodeWorkspaceArchiveRequest(w, r, correlationID, &input) {
		return
	}
	if !input.Confirm || input.OperationID == "" || input.PlanDigest == "" {
		s.writeError(w, correlationID, http.StatusBadRequest, "workspace_archive.confirmation_required", "workspace_archive", "recover", "Exact plan digest and explicit confirmation are required.", nil)
		return
	}
	service, ok := s.workspaceArchiveService(w, correlationID)
	if !ok {
		return
	}
	inspection, err := service.InspectOperation(ctx, input.OperationID)
	if err != nil {
		s.writeWorkspaceArchiveError(w, correlationID, "recover", err)
		return
	}
	if inspection.Operation.ActorID != req.ActorID {
		s.writeError(w, correlationID, http.StatusForbidden, "workspace_archive.forbidden", "workspace_archive", "recover", "Workspace archive operation is not authorized.", nil)
		return
	}
	if inspection.Operation.PlanDigest != input.PlanDigest {
		s.writeError(w, correlationID, http.StatusConflict, "workspace_archive.plan_conflict", "workspace_archive", "recover", "Workspace archive plan digest does not match durable state.", nil)
		return
	}
	inspection, err = service.RecoverOperation(ctx, input.OperationID)
	if err != nil {
		s.writeWorkspaceArchiveError(w, correlationID, "recover", err)
		return
	}
	s.writeBoundedWorkspaceArchiveJSON(w, correlationID, http.StatusOK, response.Success(correlationID, storagearchive.NewWorkspaceMoveInspectionSummary(inspection)))
}

func workspaceArchiveRequestMeta(r *http.Request) (string, context.Context, context.CancelFunc) {
	correlationID, ctx := requestMeta(r)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	return correlationID, ctx, cancel
}

func (s Server) authorizeWorkspaceArchive(w http.ResponseWriter, ctx context.Context, correlationID, capability string) (requestctx.Context, bool) {
	if s.services.WorkspaceArchive == nil {
		s.writeError(w, correlationID, http.StatusServiceUnavailable, "workspace_archive.not_ready", "workspace_archive", "runtime", "Workspace archive lifecycle is not ready.", nil)
		return requestctx.Context{}, false
	}
	resolver := s.services.WorkspaceArchiveRequestResolver
	if resolver == nil {
		resolver = func(ctx context.Context, correlationID string) (requestctx.Context, error) {
			return requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		}
	}
	req, err := resolver(ctx, correlationID)
	if err != nil || strings.TrimSpace(req.ActorID) == "" || strings.TrimSpace(req.OriginNodeID) == "" {
		s.writeError(w, correlationID, http.StatusForbidden, "workspace_archive.forbidden", "workspace_archive", "authorization", "Workspace archive request is not authorized.", err)
		return requestctx.Context{}, false
	}
	if s.services.WorkspaceArchiveAuthorizer == nil {
		s.writeError(w, correlationID, http.StatusServiceUnavailable, "workspace_archive.authorization_unavailable", "workspace_archive", "authorization", "Workspace archive authorization is unavailable.", nil)
		return requestctx.Context{}, false
	}
	if err := s.services.WorkspaceArchiveAuthorizer.Authorize(ctx, req, capability); err != nil {
		var denied *WorkspaceArchiveAuthorizationError
		status := http.StatusServiceUnavailable
		if errors.As(err, &denied) {
			status = http.StatusForbidden
		}
		s.writeError(w, correlationID, status, "workspace_archive.forbidden", "workspace_archive", "authorization", "Workspace archive request is not authorized.", err)
		return requestctx.Context{}, false
	}
	return req, true
}

func (s Server) workspaceArchiveService(w http.ResponseWriter, correlationID string) (WorkspaceArchiveLifecycleService, bool) {
	if s.services.WorkspaceArchive == nil {
		s.writeError(w, correlationID, http.StatusServiceUnavailable, "workspace_archive.not_ready", "workspace_archive", "runtime", "Workspace archive lifecycle is not ready.", nil)
		return nil, false
	}
	return s.services.WorkspaceArchive, true
}

func (s Server) requireWorkspaceArchiveMethod(w http.ResponseWriter, r *http.Request, correlationID, method string) bool {
	if r.Method == method {
		return true
	}
	s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "workspace_archive", r.URL.Path, "Method is not allowed.", nil)
	return false
}

func (s Server) decodeWorkspaceArchiveRequest(w http.ResponseWriter, r *http.Request, correlationID string, destination any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, storagearchive.MaximumWorkspaceSurfaceRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "workspace_archive.invalid_request", "workspace_archive", "request", "Workspace archive request is invalid.", nil)
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil || !errors.Is(err, io.EOF) {
		s.writeError(w, correlationID, http.StatusBadRequest, "workspace_archive.invalid_request", "workspace_archive", "request", "Workspace archive request must contain one JSON object.", nil)
		return false
	}
	return true
}

func (s Server) writeWorkspaceArchiveError(w http.ResponseWriter, correlationID, target string, err error) {
	status := http.StatusConflict
	if errors.Is(err, context.DeadlineExceeded) {
		status = http.StatusGatewayTimeout
	} else if errors.Is(err, context.Canceled) {
		status = http.StatusRequestTimeout
	}
	s.writeError(w, correlationID, status, "workspace_archive.conflict", "workspace_archive", target, "Workspace archive operation could not proceed safely.", err)
}

func (s Server) writeBoundedWorkspaceArchiveJSON(w http.ResponseWriter, correlationID string, status int, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "workspace_archive.response_invalid", "workspace_archive", "response", "Workspace archive response could not be encoded.", err)
		return
	}
	if len(payload) > storagearchive.MaximumWorkspaceSurfaceResponseBytes {
		s.writeError(w, correlationID, http.StatusInternalServerError, "workspace_archive.output_too_large", "workspace_archive", "response", "Workspace archive response exceeded its output bound.", nil)
		return
	}
	writeRawJSON(w, status, payload)
}
