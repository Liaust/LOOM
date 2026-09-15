package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"loom.local/loom/internal/automation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/response"
)

func (s Server) handleAutomations(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "automation", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	items, err := s.services.Automation.ListAutomations(ctx, automation.AutomationFilter{
		Limit:      limit,
		Status:     query.Get("status"),
		SourceKind: query.Get("source_kind"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "automations.list_failed", "automation", "automations", "Could not list automations.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, items))
}

func (s Server) handleAutomation(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	ref := pathRef(r.URL.Path, "/v1/automations/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "automation.ref_required", "automation", "automation", "Automation reference is required.", nil)
		return
	}
	detail, err := s.services.Automation.GetAutomation(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "automation", ref, "Automation was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, detail))
}

func (s Server) handleIntegrations(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handleIntegrationCreate(w, r)
	case http.MethodGet:
		s.handleIntegrationList(w, r)
	default:
		correlationID, _ := requestMeta(r)
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", r.URL.Path, "Method is not allowed.", nil)
	}
}

func (s Server) handleIntegrationCreate(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	var input automation.CreateIntegrationInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "automation", "body", "Request body is not valid integration JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "automation", "bootstrap", "Could not resolve request context.", err)
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "automation.integration.create", input)
	if !proceed {
		return
	}
	detail, err := s.services.Automation.CreateIntegration(ctx, req, input)
	if err != nil {
		idemErr := loomerrors.Wrap("integration.create_failed", "automation", input.IntegrationKey, "Could not create integration.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, http.StatusBadRequest, "integration.create_failed", "automation", input.IntegrationKey, "Could not create integration.", err)
		return
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, detail)
	s.completeIdempotency(ctx, idemRecord, "integration", detail.Integration.IntegrationID, envelope)
	response.WriteJSON(w, http.StatusCreated, envelope)
}

func (s Server) handleIntegrationList(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "automation", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	items, err := s.services.Automation.ListIntegrations(ctx, automation.IntegrationFilter{
		Limit:  limit,
		Status: query.Get("status"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "integrations.list_failed", "automation", "integrations", "Could not list integrations.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, items))
}

func (s Server) handleIntegration(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	ref := pathRef(r.URL.Path, "/v1/integrations/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "integration.ref_required", "automation", "integration", "Integration reference is required.", nil)
		return
	}
	if strings.HasSuffix(ref, "/auth-profiles") {
		integrationRef := strings.Trim(strings.TrimSuffix(ref, "/auth-profiles"), "/")
		switch r.Method {
		case http.MethodPost:
			s.handleIntegrationAuthProfileCreate(w, r, integrationRef)
		case http.MethodGet:
			s.handleIntegrationAuthProfileList(w, r, integrationRef)
		default:
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", r.URL.Path, "Method is not allowed.", nil)
		}
		return
	}
	if strings.HasSuffix(ref, "/disable") || strings.HasSuffix(ref, "/revoke") {
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		action := "disable"
		integrationRef := strings.Trim(strings.TrimSuffix(ref, "/disable"), "/")
		if strings.HasSuffix(ref, "/revoke") {
			action = "revoke"
			integrationRef = strings.Trim(strings.TrimSuffix(ref, "/revoke"), "/")
		}
		var input automation.UpdateIntegrationStatusInput
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil && err != io.EOF {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "automation", "body", "Request body is not valid integration status JSON.", err)
			return
		}
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "automation", "bootstrap", "Could not resolve request context.", err)
			return
		}
		var detail automation.IntegrationDetail
		if action == "revoke" {
			detail, err = s.services.Automation.RevokeIntegration(ctx, req, integrationRef, input)
		} else {
			detail, err = s.services.Automation.DisableIntegration(ctx, req, integrationRef, input)
		}
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "integration.status_update_failed", "automation", integrationRef, "Could not update integration status.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, detail))
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	detail, err := s.services.Automation.GetIntegration(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "automation", ref, "Integration was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, detail))
}

func (s Server) handleIntegrationAuthProfileCreate(w http.ResponseWriter, r *http.Request, integrationRef string) {
	correlationID, ctx := requestMeta(r)
	var input automation.CreateIntegrationAuthProfileInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "automation", "body", "Request body is not valid integration auth profile JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "automation", "bootstrap", "Could not resolve request context.", err)
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "automation.integration.auth_profile.create", map[string]any{
		"integration_ref": integrationRef,
		"input":           input,
	})
	if !proceed {
		return
	}
	result, err := s.services.Automation.CreateIntegrationAuthProfile(ctx, req, integrationRef, input)
	if err != nil {
		idemErr := loomerrors.Wrap("integration_auth_profile.create_failed", "automation", integrationRef, "Could not create integration auth profile.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, http.StatusBadRequest, "integration_auth_profile.create_failed", "automation", integrationRef, "Could not create integration auth profile.", err)
		return
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, result)
	s.completeIdempotency(ctx, idemRecord, "integration_auth_profile", result.Profile.AuthProfileID, envelope)
	response.WriteJSON(w, http.StatusCreated, envelope)
}

func (s Server) handleIntegrationAuthProfileList(w http.ResponseWriter, r *http.Request, integrationRef string) {
	correlationID, ctx := requestMeta(r)
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "automation", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	items, err := s.services.Automation.ListIntegrationAuthProfiles(ctx, automation.IntegrationAuthProfileFilter{
		IntegrationRef: integrationRef,
		Limit:          limit,
		Status:         query.Get("status"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "integration_auth_profiles.list_failed", "automation", integrationRef, "Could not list integration auth profiles.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, items))
}

func (s Server) handleIntegrationAuthProfile(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	ref := pathRef(r.URL.Path, "/v1/integration-auth-profiles/")
	if !strings.HasSuffix(ref, "/revoke") {
		s.writeError(w, correlationID, http.StatusNotFound, "integration_auth_profile.action_required", "automation", "integration_auth_profile", "Integration auth profile action is required.", nil)
		return
	}
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	authRef := strings.Trim(strings.TrimSuffix(ref, "/revoke"), "/")
	var input automation.UpdateIntegrationAuthProfileStatusInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil && err != io.EOF {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "automation", "body", "Request body is not valid auth profile status JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "automation", "bootstrap", "Could not resolve request context.", err)
		return
	}
	profile, err := s.services.Automation.RevokeIntegrationAuthProfile(ctx, req, authRef, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "integration_auth_profile.revoke_failed", "automation", authRef, "Could not revoke integration auth profile.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, profile))
}

func (s Server) handleDirectEventIngest(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	slug := pathRef(r.URL.Path, "/v1/direct-events/ingest/")
	if slug == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "direct_event.endpoint_required", "automation", "direct_event", "Direct event endpoint slug is required.", nil)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, (10<<20)+1))
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.body_failed", "automation", slug, "Could not read direct event body.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "automation", "bootstrap", "Could not resolve request context.", err)
		return
	}
	result, err := s.services.Automation.AcceptDirectEvent(ctx, req, automation.DirectEventHTTPRequestInput{
		EndpointSlug: slug,
		Method:       r.Method,
		Path:         r.URL.Path,
		Headers:      mapStringSlice(r.Header),
		Query:        mapStringSlice(r.URL.Query()),
		Body:         body,
		RemoteAddr:   r.RemoteAddr,
	})
	if err != nil {
		status := http.StatusBadRequest
		code := "direct_event.ingest_failed"
		summary := "Could not accept direct event."
		if s.writeProjectRuntimeArchivedError(w, correlationID, "automation", slug, err) {
			return
		}
		if errors.Is(err, automation.ErrDirectEventUnauthorized) {
			status = http.StatusUnauthorized
			code = "direct_event.unauthorized"
			summary = "Direct event authentication failed."
		} else if errors.Is(err, automation.ErrDirectEventIdempotencyConflict) {
			status = http.StatusConflict
			code = "direct_event.idempotency_conflict"
			summary = "Direct event idempotency key conflicts with an existing payload."
		}
		s.writeError(w, correlationID, status, code, "automation", slug, summary, err)
		return
	}
	if result.ResponseMode == automation.DirectEventResponseSyncWait {
		syncResult, syncErr := s.services.Automation.ProcessDirectEventSyncWait(ctx, req, result.DirectEvent.DirectEventID, result.Duplicate)
		status := directEventSyncWaitHTTPStatus(syncResult, syncErr)
		if syncErr != nil && status == http.StatusInternalServerError {
			s.writeError(w, correlationID, status, "direct_event.sync_wait_failed", "automation", slug, "Could not process direct event sync-wait.", syncErr)
			return
		}
		response.WriteJSON(w, status, response.Success(correlationID, syncResult))
		return
	}
	response.WriteJSON(w, http.StatusAccepted, response.Success(correlationID, result))
}

func directEventSyncWaitHTTPStatus(result automation.DirectEventIngestResult, err error) int {
	if errors.Is(err, automation.ErrDirectEventMappingFailed) {
		return http.StatusUnprocessableEntity
	}
	if errors.Is(err, automation.ErrDirectEventSyncTimedOut) {
		return http.StatusGatewayTimeout
	}
	if errors.Is(err, automation.ErrDirectEventUnauthorized) {
		return http.StatusUnauthorized
	}
	if err != nil {
		return http.StatusInternalServerError
	}
	switch result.DirectEvent.Status {
	case automation.DirectEventStatusCompleted:
		return http.StatusOK
	case automation.DirectEventStatusAccepted, automation.DirectEventStatusInvocationCreated:
		return http.StatusAccepted
	case automation.DirectEventStatusMappingFailed:
		return http.StatusUnprocessableEntity
	case automation.DirectEventStatusTimedOut:
		return http.StatusGatewayTimeout
	case automation.DirectEventStatusFailed, automation.DirectEventStatusRejected:
		return http.StatusUnprocessableEntity
	default:
		return http.StatusAccepted
	}
}

func (s Server) handleDirectEvents(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "automation", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	items, err := s.services.Automation.ListDirectEvents(ctx, automation.DirectEventFilter{
		Limit:          limit,
		Status:         query.Get("status"),
		EndpointRef:    query.Get("endpoint"),
		IntegrationRef: query.Get("integration"),
		AutomationRef:  query.Get("automation"),
		ProjectRef:     query.Get("project"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "direct_events.list_failed", "automation", "direct_events", "Could not list direct events.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, items))
}

func (s Server) handleDirectEventStatus(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	status, err := s.services.Automation.DirectEventStatus(ctx)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "direct_events.status_failed", "automation", "direct_events", "Could not summarize direct events.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, status))
}

func (s Server) handleDirectEventFailures(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "automation", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	items, err := s.services.Automation.ListDirectEventFailures(ctx, automation.DirectEventFilter{
		Limit:          limit,
		EndpointRef:    query.Get("endpoint"),
		IntegrationRef: query.Get("integration"),
		AutomationRef:  query.Get("automation"),
		ProjectRef:     query.Get("project"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "direct_events.failures_failed", "automation", "direct_events", "Could not list failed direct events.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, items))
}

func (s Server) handleDirectEvent(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	ref := pathRef(r.URL.Path, "/v1/direct-events/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "direct_event.ref_required", "automation", "direct_event", "Direct event reference is required.", nil)
		return
	}
	if strings.HasSuffix(ref, "/raw") {
		if r.Method != http.MethodGet {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		eventRef := strings.Trim(strings.TrimSuffix(ref, "/raw"), "/")
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "automation", "bootstrap", "Could not resolve request context.", err)
			return
		}
		raw, err := s.services.Automation.GetDirectEventRawPayload(ctx, req, eventRef)
		if err != nil {
			s.writeLookupError(w, correlationID, "automation", eventRef, "Direct event raw payload was not found.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, raw))
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	detail, err := s.services.Automation.GetDirectEvent(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "automation", ref, "Direct event was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, detail))
}

func (s Server) handleDirectEventEndpoints(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handleDirectEventEndpointCreate(w, r)
	case http.MethodGet:
		s.handleDirectEventEndpointList(w, r)
	default:
		correlationID, _ := requestMeta(r)
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", r.URL.Path, "Method is not allowed.", nil)
	}
}

func (s Server) handleDirectEventEndpointCreate(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	var input automation.CreateDirectEventEndpointInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "automation", "body", "Request body is not valid direct event endpoint JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "automation", "bootstrap", "Could not resolve request context.", err)
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "automation.direct_event_endpoint.create", input)
	if !proceed {
		return
	}
	detail, err := s.services.Automation.CreateDirectEventEndpoint(ctx, req, input)
	if err != nil {
		idemErr := loomerrors.Wrap("direct_event_endpoint.create_failed", "automation", input.EndpointSlug, "Could not create direct event endpoint.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, http.StatusBadRequest, "direct_event_endpoint.create_failed", "automation", input.EndpointSlug, "Could not create direct event endpoint.", err)
		return
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, detail)
	s.completeIdempotency(ctx, idemRecord, "direct_event_endpoint", detail.Endpoint.EndpointID, envelope)
	response.WriteJSON(w, http.StatusCreated, envelope)
}

func (s Server) handleDirectEventEndpointList(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "automation", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	items, err := s.services.Automation.ListDirectEventEndpoints(ctx, automation.DirectEventEndpointFilter{
		Limit:          limit,
		Status:         query.Get("status"),
		IntegrationRef: query.Get("integration"),
		AutomationRef:  query.Get("automation"),
		ProjectRef:     query.Get("project"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "direct_event_endpoints.list_failed", "automation", "direct_event_endpoints", "Could not list direct event endpoints.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, items))
}

func (s Server) handleDirectEventEndpoint(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	ref := pathRef(r.URL.Path, "/v1/direct-event-endpoints/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "direct_event_endpoint.ref_required", "automation", "direct_event_endpoint", "Direct event endpoint reference is required.", nil)
		return
	}
	for _, action := range []string{"pause", "resume", "disable"} {
		suffix := "/" + action
		if strings.HasSuffix(ref, suffix) {
			if r.Method != http.MethodPost {
				s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", r.URL.Path, "Method is not allowed.", nil)
				return
			}
			endpointRef := strings.Trim(strings.TrimSuffix(ref, suffix), "/")
			var input automation.UpdateDirectEventEndpointStatusInput
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil && err != io.EOF {
				s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "automation", "body", "Request body is not valid direct event endpoint status JSON.", err)
				return
			}
			req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
			if err != nil {
				s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "automation", "bootstrap", "Could not resolve request context.", err)
				return
			}
			var detail automation.DirectEventEndpointDetail
			switch action {
			case "pause":
				detail, err = s.services.Automation.PauseDirectEventEndpoint(ctx, req, endpointRef, input)
			case "resume":
				detail, err = s.services.Automation.ResumeDirectEventEndpoint(ctx, req, endpointRef, input)
			default:
				detail, err = s.services.Automation.DisableDirectEventEndpoint(ctx, req, endpointRef, input)
			}
			if err != nil {
				s.writeError(w, correlationID, http.StatusBadRequest, "direct_event_endpoint.status_update_failed", "automation", endpointRef, "Could not update direct event endpoint status.", err)
				return
			}
			response.WriteJSON(w, http.StatusOK, response.Success(correlationID, detail))
			return
		}
	}
	if strings.HasSuffix(ref, "/preview") {
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		endpointRef := strings.Trim(strings.TrimSuffix(ref, "/preview"), "/")
		var input automation.MappingPreviewInput
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil && err != io.EOF {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "automation", "body", "Request body is not valid mapping preview JSON.", err)
			return
		}
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "automation", "bootstrap", "Could not resolve request context.", err)
			return
		}
		result, err := s.services.Automation.PreviewDirectEventEndpointMapping(ctx, req, endpointRef, input)
		if err != nil {
			if s.writeProjectRuntimeArchivedError(w, correlationID, "automation", endpointRef, err) {
				return
			}
			s.writeError(w, correlationID, http.StatusBadRequest, "direct_event_endpoint.preview_failed", "automation", endpointRef, "Could not preview direct event mapping.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	detail, err := s.services.Automation.GetDirectEventEndpoint(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "automation", ref, "Direct event endpoint was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, detail))
}

func (s Server) handleSchedules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handleScheduleCreate(w, r)
	case http.MethodGet:
		s.handleScheduleList(w, r)
	default:
		correlationID, _ := requestMeta(r)
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", r.URL.Path, "Method is not allowed.", nil)
	}
}

func (s Server) handleScheduleCreate(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	var input automation.CreateScheduleInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "automation", "body", "Request body is not valid schedule JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "automation", "bootstrap", "Could not resolve request context.", err)
		return
	}
	if input.DryRun {
		detail, err := s.services.Automation.CreateSchedule(ctx, req, input)
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "schedule.create_failed", "automation", input.ScheduleKey, "Could not preview schedule.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, detail))
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "automation.schedule.create", input)
	if !proceed {
		return
	}
	detail, err := s.services.Automation.CreateSchedule(ctx, req, input)
	if err != nil {
		idemErr := loomerrors.Wrap("schedule.create_failed", "automation", input.ScheduleKey, "Could not create schedule.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, http.StatusBadRequest, "schedule.create_failed", "automation", input.ScheduleKey, "Could not create schedule.", err)
		return
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, detail)
	s.completeIdempotency(ctx, idemRecord, "schedule", detail.Schedule.ScheduleID, envelope)
	response.WriteJSON(w, http.StatusCreated, envelope)
}

func (s Server) handleScheduleList(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "automation", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	items, err := s.services.Automation.ListSchedules(ctx, automation.ScheduleFilter{
		Limit:         limit,
		Status:        query.Get("status"),
		AutomationRef: query.Get("automation"),
		ProjectRef:    query.Get("project"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "schedules.list_failed", "automation", "schedules", "Could not list schedules.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, items))
}

func (s Server) handleScheduleStatus(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	status, err := s.services.Automation.ScheduleStatus(ctx)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "schedules.status_failed", "automation", "schedules", "Could not summarize schedules.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, status))
}

func (s Server) handleSchedule(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	ref := pathRef(r.URL.Path, "/v1/schedules/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "schedule.ref_required", "automation", "schedule", "Schedule reference is required.", nil)
		return
	}
	if strings.HasSuffix(ref, "/pause") || strings.HasSuffix(ref, "/resume") || strings.HasSuffix(ref, "/disable") {
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		action := "pause"
		scheduleRef := strings.Trim(strings.TrimSuffix(ref, "/pause"), "/")
		if strings.HasSuffix(ref, "/resume") {
			action = "resume"
			scheduleRef = strings.Trim(strings.TrimSuffix(ref, "/resume"), "/")
		}
		if strings.HasSuffix(ref, "/disable") {
			action = "disable"
			scheduleRef = strings.Trim(strings.TrimSuffix(ref, "/disable"), "/")
		}
		var input automation.UpdateScheduleStatusInput
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "automation", "body", "Request body is not valid schedule status JSON.", err)
			return
		}
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "automation", "bootstrap", "Could not resolve request context.", err)
			return
		}
		var detail automation.ScheduleDetail
		switch action {
		case "pause":
			detail, err = s.services.Automation.PauseSchedule(ctx, req, scheduleRef, input)
		case "resume":
			detail, err = s.services.Automation.ResumeSchedule(ctx, req, scheduleRef, input)
		default:
			detail, err = s.services.Automation.DisableSchedule(ctx, req, scheduleRef, input)
		}
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "schedule.status_update_failed", "automation", scheduleRef, "Could not update schedule status.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, detail))
		return
	}
	if strings.HasSuffix(ref, "/fire") {
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		scheduleRef := strings.Trim(strings.TrimSuffix(ref, "/fire"), "/")
		var input automation.FireScheduleInput
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil && err != io.EOF {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "automation", "body", "Request body is not valid schedule fire JSON.", err)
			return
		}
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "automation", "bootstrap", "Could not resolve request context.", err)
			return
		}
		idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "automation.schedule.fire", map[string]any{
			"schedule_ref": scheduleRef,
			"input":        input,
		})
		if !proceed {
			return
		}
		result, err := s.services.Automation.FireScheduleNow(ctx, req, scheduleRef, input)
		if err != nil {
			idemErr := loomerrors.Wrap("schedule.fire_failed", "automation", scheduleRef, "Could not fire schedule.", err)
			s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
			if s.writeProjectRuntimeArchivedError(w, correlationID, "automation", scheduleRef, err) {
				return
			}
			s.writeError(w, correlationID, http.StatusBadRequest, "schedule.fire_failed", "automation", scheduleRef, "Could not fire schedule.", err)
			return
		}
		envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, result)
		s.completeIdempotency(ctx, idemRecord, "schedule_fire", result.Fire.ScheduleFireID, envelope)
		response.WriteJSON(w, http.StatusCreated, envelope)
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	detail, err := s.services.Automation.GetSchedule(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "automation", ref, "Schedule was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, detail))
}

func (s Server) handleScheduleFires(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "automation", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	items, err := s.services.Automation.ListScheduleFires(ctx, automation.ScheduleFireFilter{
		Limit:         limit,
		Status:        query.Get("status"),
		ScheduleRef:   query.Get("schedule"),
		AutomationRef: query.Get("automation"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "schedule_fires.list_failed", "automation", "schedule_fires", "Could not list schedule fires.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, items))
}

func (s Server) handleScheduleFire(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	ref := pathRef(r.URL.Path, "/v1/schedule-fires/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "schedule_fire.ref_required", "automation", "schedule_fire", "Schedule fire reference is required.", nil)
		return
	}
	fire, err := s.services.Automation.GetScheduleFire(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "automation", ref, "Schedule fire was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, fire))
}

func (s Server) handleInvocations(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "automation", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	items, err := s.services.Automation.ListInvocations(ctx, automation.InvocationFilter{
		Limit:         limit,
		Status:        query.Get("status"),
		AutomationRef: query.Get("automation"),
		ProjectRef:    query.Get("project"),
		SourceKind:    query.Get("source_kind"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "invocations.list_failed", "automation", "invocations", "Could not list invocations.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, items))
}

func (s Server) handleInvocationFailures(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "automation", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	items, err := s.services.Automation.ListInvocationFailures(ctx, automation.InvocationFilter{
		Limit:         limit,
		AutomationRef: query.Get("automation"),
		ProjectRef:    query.Get("project"),
		SourceKind:    query.Get("source_kind"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "invocations.failures_failed", "automation", "invocations", "Could not list failed invocations.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, items))
}

func (s Server) handleInvocation(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	ref := pathRef(r.URL.Path, "/v1/invocations/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "invocation.ref_required", "automation", "invocation", "Invocation reference is required.", nil)
		return
	}
	invocation, err := s.services.Automation.GetInvocation(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "automation", ref, "Invocation was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, invocation))
}

func mapStringSlice(values map[string][]string) map[string][]string {
	out := make(map[string][]string, len(values))
	for key, value := range values {
		copied := make([]string, len(value))
		copy(copied, value)
		out[key] = copied
	}
	return out
}
