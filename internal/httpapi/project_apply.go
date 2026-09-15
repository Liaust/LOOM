package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/projectapply"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/response"
)

type ProjectDeclarationService interface {
	Plan(context.Context, projectapply.Principal, pc.DeclarationPlanRequest) (pc.DeclarationPlan, error)
	Apply(context.Context, projectapply.Principal, pc.DeclarationApplyRequest) (pc.DeclarationResult, error)
	Operation(context.Context, projectapply.Principal, string) (pc.DeclarationResult, error)
	Status(context.Context, projectapply.Principal, pc.DeclarationPlanRequest) (pc.DeclarationStatus, error)
}

// Optional read interface: existing service registration remains unchanged.
type ProjectConversionAssessmentService interface {
	ConversionAssessment(context.Context, projectapply.Principal, pc.DeclarationPlanRequest) (pc.DeclarationMigrationAssessment, error)
}
type ProjectDeclarationRequestResolver func(context.Context, string) (requestctx.Context, error)

// The standard error fields are unchanged. Only declaration failures can carry
// the exact durable D0 result and its typed, redacted cause beside that envelope.
type ProjectDeclarationErrorEnvelope struct {
	response.ErrorEnvelope
	Data             *pc.DeclarationResult `json:"data,omitempty"`
	DeclarationError pc.DeclarationError   `json:"declaration_error"`
}

const declarationBodyLimit = 16 << 10

var declarationDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var declarationCause = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,79}$`)

func declarationFailure(code pc.DeclarationErrorCode, cause string) error {
	return &projectapply.Failure{Code: code, Cause: cause}
}
func declarationString(s string, limit int) bool {
	return s != "" && len(s) <= limit && strings.TrimSpace(s) == s && !strings.ContainsFunc(s, unicode.IsControl)
}

// Decode one flat D0 object, refusing duplicate/case-folded/unknown fields and
// explicit nulls. Do this before identity resolution or service dispatch.
func decodeDeclaration(r *http.Request, out any, allowed ...string) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, declarationBodyLimit+1))
	if err != nil || len(raw) > declarationBodyLimit {
		return declarationFailure(pc.DeclarationInvalid, "body_too_large_or_unreadable")
	}
	if !utf8.Valid(raw) {
		return declarationFailure(pc.DeclarationInvalid, "invalid_json_encoding")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return declarationFailure(pc.DeclarationInvalid, "invalid_json")
	}
	keys := map[string]bool{}
	for _, k := range allowed {
		keys[k] = true
	}
	seen := map[string]bool{}
	for d.More() {
		tok, err := d.Token()
		if err != nil {
			return declarationFailure(pc.DeclarationInvalid, "invalid_json")
		}
		key, ok := tok.(string)
		if !ok || !keys[key] || seen[key] {
			return declarationFailure(pc.DeclarationInvalid, "unknown_or_duplicate_field")
		}
		seen[key] = true
		var v json.RawMessage
		if d.Decode(&v) != nil || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return declarationFailure(pc.DeclarationInvalid, "invalid_json_value")
		}
	}
	if _, err = d.Token(); err != nil {
		return declarationFailure(pc.DeclarationInvalid, "invalid_json")
	}
	if _, err = d.Token(); err != io.EOF {
		return declarationFailure(pc.DeclarationInvalid, "trailing_json")
	}
	if json.Unmarshal(raw, out) != nil {
		return declarationFailure(pc.DeclarationInvalid, "invalid_json_value")
	}
	return nil
}
func validateDeclarationPlan(r pc.DeclarationPlanRequest) error {
	if r.SchemaVersion != pc.DeclarationRequestSchemaV05 {
		return declarationFailure(pc.DeclarationUnsupported, "request_schema_unsupported")
	}
	if !declarationString(r.ProjectRef, 4096) || (r.NodeRef != "" && !declarationString(r.NodeRef, 256)) {
		return declarationFailure(pc.DeclarationInvalid, "invalid_selector")
	}
	if strings.Contains(r.ProjectRef, "\\") || r.ProjectRef == "." || r.ProjectRef == ".." || (strings.Contains(r.ProjectRef, "/") && !filepath.IsAbs(r.ProjectRef)) || (filepath.IsAbs(r.ProjectRef) && (r.ProjectRef == "/" || filepath.Clean(r.ProjectRef) != r.ProjectRef)) {
		return declarationFailure(pc.DeclarationInvalid, "absolute_project_path_or_reference_required")
	}
	for prefix, ref := range map[string]string{ids.ProjectPrefix: r.ProjectRef, ids.NodePrefix: r.NodeRef} {
		if strings.HasPrefix(ref, prefix+"_") && ids.Validate(prefix, ref) != nil {
			return declarationFailure(pc.DeclarationInvalid, "invalid_selector_id")
		}
	}
	if r.Effects != nil {
		if len(r.Effects) == 0 {
			return declarationFailure(pc.DeclarationInvalid, "effects_required")
		}
		seen := map[pc.DeclarationEffect]bool{}
		for _, x := range r.Effects {
			if (x != pc.DeclarationReconcile && x != pc.DeclarationProjections) || seen[x] {
				return declarationFailure(pc.DeclarationInvalid, "invalid_effects")
			}
			seen[x] = true
		}
	}
	return nil
}
func validateDeclarationApply(r pc.DeclarationApplyRequest) error {
	if err := validateDeclarationPlan(pc.DeclarationPlanRequest{SchemaVersion: r.SchemaVersion, ProjectRef: r.ProjectRef, NodeRef: r.NodeRef, Effects: r.Effects}); err != nil {
		return err
	}
	if len(r.Effects) == 0 || !declarationDigest.MatchString(r.PlanID) || !declarationString(r.IdempotencyKey, 256) || (r.OperationID != "" && ids.Validate(ids.JobPrefix, r.OperationID) != nil) {
		return declarationFailure(pc.DeclarationInvalid, "invalid_operation_request")
	}
	seen := map[string]bool{}
	for _, ref := range r.ApprovalRefs {
		if !declarationString(ref, 256) || seen[ref] {
			return declarationFailure(pc.DeclarationInvalid, "invalid_approval")
		}
		seen[ref] = true
	}
	return nil
}
func (s Server) declarationPrincipal(r *http.Request) (projectapply.Principal, error) {
	svc := s.services.ProjectDeclaration
	if svc == nil || (reflect.ValueOf(svc).Kind() == reflect.Ptr && reflect.ValueOf(svc).IsNil()) || s.services.ProjectDeclarationRequestResolver == nil {
		return projectapply.Principal{}, declarationFailure(pc.DeclarationTargetUnavailable, "declaration_not_configured")
	}
	cid, ctx := requestMeta(r)
	req, err := s.services.ProjectDeclarationRequestResolver(ctx, cid)
	if err != nil {
		return projectapply.Principal{}, declarationFailure(pc.DeclarationUnauthorized, "request_identity_unavailable")
	}
	if ids.Validate(ids.ActorPrefix, req.ActorID) != nil || ids.Validate(ids.NodePrefix, req.OriginNodeID) != nil {
		return projectapply.Principal{}, declarationFailure(pc.DeclarationUnauthorized, "invalid_principal")
	}
	return projectapply.Principal{ActorID: req.ActorID, OriginNodeID: req.OriginNodeID}, nil
}
func (s Server) declarationAdmission(w http.ResponseWriter, r *http.Request, method string, queryAllowed bool) bool {
	cid, _ := requestMeta(r)
	if r.Method != method {
		s.writeError(w, cid, http.StatusMethodNotAllowed, "method.not_allowed", "projects", "declaration", "Method is not allowed.", nil)
		return false
	}
	if !queryAllowed && (r.URL.RawQuery != "" || r.URL.ForceQuery) {
		s.writeDeclarationError(w, r, declarationFailure(pc.DeclarationInvalid, "unexpected_query"), nil, false)
		return false
	}
	if method == http.MethodGet && (r.ContentLength != 0 || len(r.TransferEncoding) != 0) {
		s.writeDeclarationError(w, r, declarationFailure(pc.DeclarationInvalid, "unexpected_body"), nil, false)
		return false
	}
	return true
}
func (s Server) handleProjectDeclarationPlans(w http.ResponseWriter, r *http.Request) {
	if !s.declarationAdmission(w, r, http.MethodPost, false) {
		return
	}
	var input pc.DeclarationPlanRequest
	err := decodeDeclaration(r, &input, "schema_version", "project_ref", "node_ref", "effects")
	if err == nil {
		err = validateDeclarationPlan(input)
	}
	if err != nil {
		s.writeDeclarationError(w, r, err, nil, false)
		return
	}
	p, err := s.declarationPrincipal(r)
	if err != nil {
		s.writeDeclarationError(w, r, err, nil, false)
		return
	}
	cid, ctx := requestMeta(r)
	result, err := s.services.ProjectDeclaration.Plan(ctx, p, input)
	if err != nil {
		s.writeDeclarationError(w, r, err, nil, false)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(cid, result))
}
func (s Server) handleProjectDeclarationApply(w http.ResponseWriter, r *http.Request) {
	if !s.declarationAdmission(w, r, http.MethodPost, false) {
		return
	}
	var input pc.DeclarationApplyRequest
	err := decodeDeclaration(r, &input, "schema_version", "project_ref", "node_ref", "effects", "plan_id", "idempotency_key", "operation_id", "approval_refs")
	if err == nil {
		err = validateDeclarationApply(input)
	}
	if err != nil {
		s.writeDeclarationError(w, r, err, nil, false)
		return
	}
	p, err := s.declarationPrincipal(r)
	if err != nil {
		s.writeDeclarationError(w, r, err, nil, false)
		return
	}
	cid, ctx := requestMeta(r)
	result, err := s.services.ProjectDeclaration.Apply(ctx, p, input)
	var failure *projectapply.Failure
	if errors.As(err, &failure) && failure.Code == pc.DeclarationOwnerFailed && failure.Cause == "owner_pending" && result.OperationID != "" { // Accepted asynchronous work, not an effect failure.
		response.WriteJSON(w, http.StatusAccepted, response.SuccessWithIdempotency(cid, input.IdempotencyKey, result))
		return
	}
	if err != nil {
		s.writeDeclarationError(w, r, err, &result, false, input.IdempotencyKey)
		return
	}
	status := http.StatusOK
	if result.State == pc.DeclarationOperationQueued || result.State == pc.DeclarationOperationRunning || result.State == pc.DeclarationOperationPartial {
		status = http.StatusAccepted
	}
	response.WriteJSON(w, status, response.SuccessWithIdempotency(cid, input.IdempotencyKey, result))
}
func (s Server) handleProjectDeclarationOperation(w http.ResponseWriter, r *http.Request) {
	if !s.declarationAdmission(w, r, http.MethodGet, false) {
		return
	}
	ref := strings.TrimPrefix(r.URL.Path, "/v1/project-declaration-operations/")
	if ids.Validate(ids.JobPrefix, ref) != nil {
		s.writeDeclarationError(w, r, declarationFailure(pc.DeclarationInvalid, "invalid_operation_id"), nil, false)
		return
	}
	p, err := s.declarationPrincipal(r)
	if err != nil {
		s.writeDeclarationError(w, r, err, nil, true)
		return
	}
	cid, ctx := requestMeta(r)
	result, err := s.services.ProjectDeclaration.Operation(ctx, p, ref)
	if err != nil {
		s.writeDeclarationError(w, r, err, nil, true)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(cid, result))
}
func projectDeclarationStatusRoute(r *http.Request) (string, bool) {
	const prefix = "/v1/projects/"
	const suffix = "/declaration-status"
	path := r.URL.EscapedPath()
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	ref, err := url.PathUnescape(strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix))
	return ref, err == nil
}
func (s Server) handleProjectDeclarationStatus(w http.ResponseWriter, r *http.Request, ref string) {
	if !s.declarationAdmission(w, r, http.MethodGet, true) {
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	invalidQuery := err != nil
	for key, values := range query {
		if len(values) != 1 || (key != "node_ref" && key != "conversion_preview") || values[0] == "" {
			invalidQuery = true
		}
	}
	if query.Has("conversion_preview") && query.Get("conversion_preview") != "true" && query.Get("conversion_preview") != "false" {
		invalidQuery = true
	}
	if invalidQuery {
		s.writeDeclarationError(w, r, declarationFailure(pc.DeclarationInvalid, "unexpected_query"), nil, false)
		return
	}
	input := pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: ref, NodeRef: query.Get("node_ref")}
	if err = validateDeclarationPlan(input); err != nil {
		s.writeDeclarationError(w, r, err, nil, false)
		return
	}
	p, err := s.declarationPrincipal(r)
	if err != nil {
		s.writeDeclarationError(w, r, err, nil, false)
		return
	}
	cid, ctx := requestMeta(r)
	if query.Get("conversion_preview") == "true" {
		svc, ok := s.services.ProjectDeclaration.(ProjectConversionAssessmentService)
		if !ok {
			s.writeDeclarationError(w, r, declarationFailure(pc.DeclarationUnsupported, "conversion_source_owner_required"), nil, false)
			return
		}
		result, err := svc.ConversionAssessment(ctx, p, input)
		if err != nil {
			s.writeDeclarationError(w, r, err, nil, false)
			return
		}
		if !pc.ValidDeclarationMigrationAssessment(result) {
			s.writeDeclarationError(w, r, declarationFailure(pc.DeclarationTargetUnavailable, "conversion_assessment_invalid"), nil, false)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(cid, result))
		return
	}
	result, err := s.services.ProjectDeclaration.Status(ctx, p, input)
	if err != nil {
		s.writeDeclarationError(w, r, err, nil, false)
		return
	}
	s.attachProjectDevelopment(ctx, &result)
	response.WriteJSON(w, http.StatusOK, response.Success(cid, result))
}
func (s Server) writeDeclarationError(w http.ResponseWriter, r *http.Request, err error, result *pc.DeclarationResult, hideOperation bool, idempotencyKey ...string) {
	code, cause := pc.DeclarationTargetUnavailable, "declaration_service_unavailable"
	var f *projectapply.Failure
	if errors.As(err, &f) && declarationCause.MatchString(f.Cause) {
		code, cause = f.Code, f.Cause
	}
	status := http.StatusUnprocessableEntity
	switch code {
	case pc.DeclarationInvalid, pc.DeclarationUnsupported, pc.DeclarationApprovalRequired:
		status = http.StatusUnprocessableEntity
	case pc.DeclarationIdentityConflict, pc.DeclarationPlanStale, pc.DeclarationOperationConflict:
		status = http.StatusConflict
	case pc.DeclarationUnauthorized:
		status = http.StatusForbidden
	case pc.DeclarationReferenceMissing:
		if cause == "operation_missing" || cause == "project_missing" {
			status = http.StatusNotFound
		}
	case pc.DeclarationTargetUnavailable, pc.DeclarationOwnerFailed:
		status = http.StatusServiceUnavailable
	default:
		code, cause, status = pc.DeclarationTargetUnavailable, "declaration_service_unavailable", http.StatusServiceUnavailable
	}
	if cause == "core_not_configured" {
		status = http.StatusServiceUnavailable
	}
	if hideOperation && (code == pc.DeclarationUnauthorized || (code == pc.DeclarationReferenceMissing && cause == "operation_missing")) {
		code, cause, status = pc.DeclarationReferenceMissing, "operation_missing", http.StatusNotFound
		result = nil
	}
	cid, _ := requestMeta(r)
	detail := pc.DeclarationError{Code: code, CauseCode: cause, Message: string(code) + ": " + cause, Retryable: status == http.StatusServiceUnavailable, CompletedEffects: []string{}}
	if cause == "application_preflight_blocked" {
		detail.Preflight = f.PublicPreflight()
		detail.Retryable = false
		detail.Message = "Application prerequisites are incomplete; resolve them before applying."
		status = http.StatusUnprocessableEntity
	}
	if result == nil || result.OperationID == "" {
		result = nil
	} else {
		for _, action := range result.Actions {
			if action.EffectRef != "" {
				detail.CompletedEffects = append(detail.CompletedEffects, action.EffectRef)
			}
		}
	}
	hint := "Inspect the cause and resolve its prerequisite before further work."
	switch code {
	case pc.DeclarationUnauthorized:
		hint = "Resolve the permission prerequisite, then inspect the operation or current status."
	case pc.DeclarationApprovalRequired:
		hint = "Obtain the required approval, then inspect the operation or current status."
	case pc.DeclarationUnsupported:
		hint = "Resolve the unsupported prerequisite, then inspect the operation or current status."
	case pc.DeclarationPlanStale, pc.DeclarationIdentityConflict:
		hint = "Resolve source/identity drift and review a new project plan on the same owner; do not reuse a stale plan."
	default:
		canResume := detail.Retryable || (code == pc.DeclarationOwnerFailed && (cause == "owner_pending" || cause == "observation_uncertain"))
		if result != nil {
			for _, failure := range result.Errors {
				pending := failure.Code == pc.DeclarationOwnerFailed && (failure.CauseCode == "owner_pending" || failure.CauseCode == "observation_uncertain")
				if !failure.Retryable && !pending || failure.Code == pc.DeclarationUnauthorized || failure.Code == pc.DeclarationApprovalRequired || failure.Code == pc.DeclarationUnsupported || failure.Code == pc.DeclarationPlanStale || failure.Code == pc.DeclarationIdentityConflict {
					canResume = false
				}
			}
			for _, action := range result.Actions {
				if failure := action.Error; failure != nil {
					pending := failure.Code == pc.DeclarationOwnerFailed && (failure.CauseCode == "owner_pending" || failure.CauseCode == "observation_uncertain")
					if !failure.Retryable && !pending || failure.Code == pc.DeclarationUnauthorized || failure.Code == pc.DeclarationApprovalRequired || failure.Code == pc.DeclarationUnsupported || failure.Code == pc.DeclarationPlanStale || failure.Code == pc.DeclarationIdentityConflict {
						canResume = false
					}
				}
			}
			if canResume && result.State != pc.DeclarationOperationSucceeded && result.State != pc.DeclarationOperationSuperseded {
				hint = "Inspect this operation; if retry is still needed, resume with the original project, node, effects, plan ID, idempotency key and approval references."
			}
		}
	}
	meta := response.NewMeta(cid)
	if len(detail.Preflight) > 0 {
		hint = "Review the preflight requirements together. Missing setup is not an automatically provisionable resource yet; plan again after its implementation or configuration changes."
	}
	if len(idempotencyKey) == 1 {
		meta.IdempotencyKey = idempotencyKey[0]
	}
	response.WriteJSON(w, status, ProjectDeclarationErrorEnvelope{ErrorEnvelope: response.ErrorEnvelope{OK: false, Error: response.ErrorBody{Code: string(code), Summary: detail.Message, Domain: "projects", Target: "declaration", Hint: hint, CorrelationID: cid}, Meta: meta}, Data: result, DeclarationError: detail})
}
