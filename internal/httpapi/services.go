package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/idempotency"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/routing"
	"loom.local/loom/internal/serviceregistry"
)

type ServiceRegistrationRequest struct {
	Registration serviceregistry.ProjectRegistrationInput `json:"registration"`
}
type ServiceOperationRequest struct {
	Confirmed       bool   `json:"confirmed,omitempty"`
	Lines           int    `json:"lines,omitempty"`
	MaxBytes        int    `json:"max_bytes,omitempty"`
	MaxAgeSeconds   int    `json:"max_age_seconds,omitempty"`
	RequestApproval bool   `json:"request_approval,omitempty"`
	ApprovalReason  string `json:"approval_reason,omitempty"`
}

func (s Server) handleServices(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "services", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "services", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	items, err := s.services.Capabilities.ListProviders(ctx, capabilities.ProviderFilter{Limit: limit, NodeRef: query.Get("node"), ScopeRef: query.Get("scope"), ProjectRef: query.Get("project"), ProviderType: capabilities.ProviderTypeService, Status: query.Get("status"), Health: query.Get("health")})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "services.list_failed", "services", "list", "Could not list services.", err)
		return
	}
	services := make([]serviceregistry.ServiceListItem, 0, len(items))
	for _, item := range items {
		projected, err := serviceregistry.ProjectServiceListItem(item)
		if err != nil {
			continue
		}
		services = append(services, projected)
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, services))
}

func (s Server) handleService(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	ref := pathRef(r.URL.Path, "/v1/services/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "service.ref_required", "services", "service", "Service reference is required.", nil)
		return
	}
	parts := strings.Split(strings.Trim(ref, "/"), "/")
	if len(parts) > 2 {
		s.writeError(w, correlationID, http.StatusNotFound, "service.path_invalid", "services", r.URL.Path, "Service path is not supported.", nil)
		return
	}
	providerRef := parts[0]
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "services", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		inspection, err := s.services.Capabilities.InspectProvider(ctx, providerRef)
		if err != nil {
			s.writeLookupError(w, correlationID, "services", providerRef, "Service was not found.", err)
			return
		}
		projected, err := serviceregistry.ProjectServiceInspection(inspection)
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "service.invalid", "services", providerRef, "Provider is not a registered service.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, projected))
		return
	}
	operation := serviceregistry.Operation(parts[1])
	policy, err := serviceregistry.StandardOperationPolicy(operation)
	if err != nil {
		s.writeError(w, correlationID, http.StatusNotFound, "service.operation_unknown", "services", parts[1], "Service operation is not supported.", nil)
		return
	}
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "services", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input ServiceOperationRequest
	if err := decodeServiceRequest(r.Body, &input, true); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "services", "body", "Service request is invalid.", err)
		return
	}
	if policy.RequiresConfirmation && !input.Confirmed {
		s.writeError(w, correlationID, http.StatusBadRequest, "service.confirmation_required", "services", providerRef, "Lifecycle operation requires explicit confirmation.", nil)
		return
	}
	if err := validateServiceOperationRequest(operation, input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "service.selector_invalid", "services", providerRef, "Log selectors are invalid or outside global bounds.", err)
		return
	}
	inspection, err := s.services.Capabilities.InspectProvider(ctx, providerRef)
	if err != nil {
		s.writeLookupError(w, correlationID, "services", providerRef, "Service was not found.", err)
		return
	}
	projected, err := serviceregistry.ProjectServiceInspection(inspection)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "service.invalid", "services", providerRef, "Provider is not a registered service.", err)
		return
	}
	if projected.RegistryState != serviceregistry.ProviderStateActive {
		s.writeError(w, correlationID, http.StatusConflict, "service.provider_inactive", "services", providerRef, "Service provider is not active.", nil)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "services", "bootstrap", "Could not resolve request context.", err)
		return
	}
	payload, _ := json.Marshal(map[string]any{"lines": input.Lines, "max_bytes": input.MaxBytes, "max_age_seconds": input.MaxAgeSeconds})
	call, err := s.services.Routing.Call(ctx, req, routing.CapabilityCallInput{Target: projected.ProviderAddress + ".service." + string(operation), ScopeRef: projected.ScopeID, Input: payload, RequestApproval: input.RequestApproval, ApprovalReason: input.ApprovalReason, Metadata: json.RawMessage(`{"source":"services_api"}`)}, strings.TrimSpace(r.Header.Get(idempotency.Header)))
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "service.operation_failed", "services", providerRef, "Service operation could not be routed.", err)
		return
	}
	response.WriteJSON(w, http.StatusAccepted, response.Success(correlationID, call))
}

func validateServiceOperationRequest(operation serviceregistry.Operation, input ServiceOperationRequest) error {
	if operation != serviceregistry.OperationLogs {
		if input.Lines != 0 || input.MaxBytes != 0 || input.MaxAgeSeconds != 0 {
			return fmt.Errorf("log selectors are valid only for logs")
		}
		return nil
	}
	limits := serviceregistry.LogLimits{MaxLines: serviceregistry.MaximumLogLines, MaxBytes: serviceregistry.MaximumLogBytes, MaxLineBytes: serviceregistry.MaximumLogLineBytes, MaxAgeSeconds: serviceregistry.MaximumLogAgeSeconds}
	return serviceregistry.ValidateLogSelectors(input.Lines, input.MaxBytes, input.MaxAgeSeconds, limits)
}

func (s Server) handleServiceRegistration(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	action := strings.TrimPrefix(r.URL.Path, "/v1/service-registrations/")
	if action != "plan" && action != "apply" {
		s.writeError(w, correlationID, http.StatusNotFound, "service_registration.action_unknown", "services", action, "Registration action is not supported.", nil)
		return
	}
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "services", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input ServiceRegistrationRequest
	if err := decodeServiceRequest(r.Body, &input, false); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "services", "body", "Registration request is invalid.", err)
		return
	}
	detail, err := s.services.Projects.GetProjectRegistrationStatus(ctx, input.Registration.ProjectID)
	if err != nil {
		s.writeLookupError(w, correlationID, "projects", input.Registration.ProjectID, "Project was not found.", err)
		return
	}
	if input.Registration.ScopeKey != serviceregistry.ProjectRegistrationScopeKey(detail.Project.Project.Slug) {
		s.writeError(w, correlationID, http.StatusBadRequest, "service_registration.scope_mismatch", "services", input.Registration.ScopeKey, "Registration scope does not match project.", nil)
		return
	}
	planInput := serviceregistry.ProjectRegistrationPlanInput{Registration: input.Registration, ProjectSlug: detail.Project.Project.Slug, ScopeRef: detail.Project.Project.ProjectScopeID, ProviderKey: projectcontracts.ProjectServiceProviderKey(detail.Project.Project.Slug, input.Registration.Service.Key)}
	plan, err := serviceregistry.PlanProjectRegistration(planInput)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "service_registration.invalid", "services", input.Registration.Service.Key, "Registration is invalid.", err)
		return
	}
	if action == "plan" {
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, plan))
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "services", "bootstrap", "Could not resolve request context.", err)
		return
	}
	result, err := serviceregistry.RegisterProject(ctx, req, s.services.Capabilities, s.services.ServiceAllowlists, planInput)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "service_registration.apply_failed", "services", input.Registration.Service.Key, "Registration could not be applied.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func decodeServiceRequest(body io.Reader, destination any, allowEmpty bool) error {
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		if allowEmpty && errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("request body must contain one JSON object")
		}
		return fmt.Errorf("request body must contain one JSON object: %w", err)
	}
	return nil
}
