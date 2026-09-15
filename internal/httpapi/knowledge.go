package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"loom.local/loom/internal/box"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/idempotency"
	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/notesprojection"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/workers"
)

func (s Server) handleKnowledgeNotesPassage(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "knowledge", "notes_passage", "Method is not allowed.", nil)
		return
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	valid := err == nil && (len(values) == 3 || (len(values) == 4 && len(values["source_lifecycle"]) == 1)) && len(values["source_lifecycle"]) <= 1
	for _, key := range []string{"object_id", "version_id", "source_hash"} {
		valid = valid && len(values[key]) == 1
	}
	input := knowledge.NotesPassageInput{
		SourceLifecycle:   knowledge.SourceLifecycleFilter(values.Get("source_lifecycle")),
		KnowledgeChunkID:  strings.TrimPrefix(r.URL.Path, "/v1/knowledge/notes/passages/"),
		KnowledgeObjectID: values.Get("object_id"), KnowledgeObjectVersionID: values.Get("version_id"), SourceHash: values.Get("source_hash"),
	}
	if !valid || knowledge.ValidateNotesPassageInput(input) != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "knowledge.invalid_passage", "knowledge", "notes_passage", "An exact object, version, chunk and source hash are required.", nil)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	passage, err := knowledge.NewService(s.services.DB).GetNotesPassage(ctx, input)
	if errors.Is(err, sql.ErrNoRows) {
		s.writeError(w, correlationID, http.StatusNotFound, "knowledge.notes_passage_not_found", "knowledge", "notes_passage", "The requested passage is not available.", nil)
		return
	}
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "knowledge.notes_passage_failed", "knowledge", "notes_passage", "Could not read the requested passage.", nil)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, passage))
}

func (s Server) handleKnowledgeNotesRoots(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "knowledge", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	filter, err := sourceRootFilterFromRequest(r)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "knowledge.invalid_filter", "knowledge", "notes_roots", "Knowledge notes root filter is invalid.", err)
		return
	}
	roots, err := knowledge.NewService(s.services.DB).Store().ListReadableSourceRoots(ctx, filter)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "knowledge.notes_roots_list_failed", "knowledge", "notes_roots", "Could not list knowledge notes source roots.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, roots))
}

func (s Server) handleKnowledgeNotesOverview(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "knowledge", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	input, err := notesOverviewInputFromRequest(r)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "knowledge.invalid_filter", "knowledge", "notes_overview", "Knowledge notes overview filter is invalid.", err)
		return
	}
	projection, err := s.notesProjectionService().Status(ctx)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "knowledge.notes_projection_status_failed", "knowledge", "notes_projection", "Could not read notes projection status.", err)
		return
	}
	input.Projection = &projection
	overview, err := knowledge.NewService(s.services.DB).GetNotesOverview(ctx, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "knowledge.notes_overview_failed", "knowledge", "notes_overview", "Could not build knowledge notes overview.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, overview))
}

func (s Server) handleKnowledgeNotesRootsReconcile(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "knowledge", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input knowledge.SourceRootReconcileInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "knowledge", "notes_roots", "Request body is not valid notes-root reconcile JSON.", err)
		return
	}
	var req requestctx.Context
	if !input.DryRun {
		var err error
		req, err = requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request_context.unavailable", "runtime", "bootstrap", "Could not resolve request context.", err)
			return
		}
	}
	if err := s.seedRuntimeBoxNotesRoots(ctx, req, input.DryRun, &input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "knowledge.notes_roots_box_seed_failed", "knowledge", "notes_roots", "Could not seed runtime Box notes root before reconciliation.", err)
		return
	}
	input.DiscoverFromStore = true
	result, err := knowledge.NewService(s.services.DB).ReconcileSourceRoots(ctx, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "knowledge.notes_roots_reconcile_failed", "knowledge", "notes_roots", "Could not reconcile knowledge notes source roots.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) seedRuntimeBoxNotesRoots(ctx context.Context, req requestctx.Context, dryRun bool, input *knowledge.SourceRootReconcileInput) error {
	if input == nil {
		return nil
	}
	cfg := s.services.RuntimeConfig
	boxRoot := strings.TrimSpace(cfg.BoxPath)
	if boxRoot == "" {
		return nil
	}
	profile := strings.TrimSpace(cfg.BoxProfile)
	if profile == "" {
		if strings.EqualFold(strings.TrimSpace(cfg.NodeRole), box.ProfileMain) {
			profile = box.ProfileMain
		} else {
			profile = box.ProfileWorkspace
		}
	}
	resolved := box.Resolved{
		RootPath:         boxRoot,
		Profile:          profile,
		OwnerNode:        firstNonEmptyHTTP(strings.TrimSpace(cfg.NodeID), "main"),
		NodeRole:         cfg.NodeRole,
		RuntimeStateRoot: cfg.BoxStateRoot,
	}
	plan, err := box.BuildWatchPlan(box.WatchStatusInput{Resolved: resolved})
	if err != nil {
		return nil
	}
	if dryRun {
		input.BoxRegistrations = append(input.BoxRegistrations, knowledge.BoxWatchPlanSourceRootRegistrations(plan, "")...)
		return nil
	}
	_, err = s.services.Box.ApplyWatchPolicy(ctx, req, box.WatchApplyInput{Resolved: resolved, Plan: &plan})
	return err
}

func (s Server) handleKnowledgeNotesObjects(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "knowledge", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	filter, err := knowledgeObjectFilterFromRequest(r)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "knowledge.invalid_filter", "knowledge", "notes_objects", "Knowledge notes object filter is invalid.", err)
		return
	}
	objects, err := knowledge.NewService(s.services.DB).Store().ListKnowledgeObjects(ctx, filter)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "knowledge.notes_objects_list_failed", "knowledge", "notes_objects", "Could not list knowledge notes objects.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, objects))
}

func (s Server) handleKnowledgeNotesObject(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "knowledge", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	ref := strings.TrimPrefix(r.URL.Path, "/v1/knowledge/notes/objects/")
	ref, err := url.PathUnescape(ref)
	if err != nil || strings.TrimSpace(ref) == "" {
		s.writeError(w, correlationID, http.StatusBadRequest, "knowledge.invalid_object_ref", "knowledge", "notes_objects", "Knowledge object ref is invalid.", err)
		return
	}
	lifecycle, err := notesLifecycleFromRequest(r)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "knowledge.invalid_filter", "knowledge", "notes_objects", "Notes source lifecycle filter is invalid.", err)
		return
	}
	object, err := knowledge.NewService(s.services.DB).Store().GetKnowledgeObjectWithLifecycle(ctx, ref, lifecycle)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.writeError(w, correlationID, http.StatusNotFound, "knowledge.notes_object_not_found", "knowledge", "notes_objects", "Knowledge notes object was not found.", err)
			return
		}
		s.writeError(w, correlationID, http.StatusInternalServerError, "knowledge.notes_object_get_failed", "knowledge", "notes_objects", "Could not get knowledge notes object.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, object))
}

func (s Server) handleKnowledgeNotesObjectsReconcile(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "knowledge", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input knowledge.KnowledgeObjectReconcileInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "knowledge", "notes_objects", "Request body is not valid notes-object reconcile JSON.", err)
		return
	}
	input.DiscoverFromStore = true
	result, err := knowledge.NewService(s.services.DB).ReconcileKnowledgeObjects(ctx, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "knowledge.notes_objects_reconcile_failed", "knowledge", "notes_objects", "Could not reconcile knowledge notes objects.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleKnowledgeNotesSearch(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "knowledge", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input knowledge.NotesSearchInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "knowledge", "notes_search", "Request body is not valid notes search JSON.", err)
		return
	}
	results, err := s.knowledgeService().SearchNotes(ctx, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "knowledge.notes_search_failed", "knowledge", input.Query, notesSearchFailureSummary(err), err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, results))
}

func notesSearchFailureSummary(err error) string {
	message := ""
	if err != nil {
		message = err.Error()
	}
	switch {
	case strings.Contains(message, "invalid notes search after timestamp"):
		return "The notes search after value must use RFC3339 with an explicit timezone or YYYY-MM-DD."
	case strings.Contains(message, "invalid notes search before timestamp"):
		return "The notes search before value must use RFC3339 with an explicit timezone or YYYY-MM-DD."
	case strings.Contains(message, "after timestamp must be earlier than before timestamp"):
		return "The notes search after value must be earlier than the before value."
	case strings.Contains(message, "notes search query is required"):
		return "Pass a non-empty notes search query."
	case strings.Contains(message, "notes search sort"):
		return "Notes search sort must be relevance, newest, or oldest."
	default:
		return "Could not search notes knowledge."
	}
}

func (s Server) handleKnowledgeNotesPipelinesStatus(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "knowledge", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	status, err := s.knowledgeService().GetPipelineOverallStatus(ctx)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "knowledge.notes_pipelines_status_failed", "knowledge", "notes_pipelines", "Could not read Notes pipeline status.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, status))
}

func (s Server) handleKnowledgeNotesPipelines(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "knowledge", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := s.knowledgeService().ListPipelineRuns(ctx, knowledge.PipelineListInput{Status: strings.TrimSpace(r.URL.Query().Get("status")), ObjectID: strings.TrimSpace(r.URL.Query().Get("object_id")), Limit: limit})
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "knowledge.notes_pipelines_list_failed", "knowledge", "notes_pipelines", "Could not list Notes pipelines.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, runs))
}

func (s Server) handleKnowledgeNotesPipelineFailures(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "knowledge", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	runs, err := s.knowledgeService().ListPipelineRuns(ctx, knowledge.PipelineListInput{FailuresOnly: true, Limit: 200})
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "knowledge.notes_pipeline_failures_failed", "knowledge", "notes_pipelines", "Could not list Notes pipeline failures.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, runs))
}

func (s Server) handleKnowledgeNotesPipelineRef(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	ref := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/knowledge/notes/pipelines/"), "/")
	retry := strings.HasSuffix(ref, "/retry")
	if retry {
		ref = strings.TrimSuffix(ref, "/retry")
	}
	decoded, err := url.PathUnescape(ref)
	if err != nil || strings.TrimSpace(decoded) == "" {
		s.writeError(w, correlationID, http.StatusBadRequest, "knowledge.invalid_pipeline_ref", "knowledge", ref, "Pipeline ref is invalid.", err)
		return
	}
	if !retry && r.Method == http.MethodGet {
		inspection, err := s.knowledgeService().InspectPipeline(ctx, decoded)
		if err != nil {
			s.writeError(w, correlationID, http.StatusNotFound, "knowledge.notes_pipeline_not_found", "knowledge", decoded, "Notes pipeline was not found.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, inspection))
		return
	}
	if retry && r.Method == http.MethodPost {
		var input knowledge.PipelineRetryInput
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "knowledge", decoded, "Pipeline retry JSON is invalid.", err)
			return
		}
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "knowledge", "bootstrap", "Could not resolve request context.", err)
			return
		}
		idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "knowledge.notes.pipeline.retry", map[string]any{"ref": decoded, "input": input})
		if !proceed {
			return
		}
		run, err := s.knowledgeService().RetryPipeline(ctx, decoded, input)
		if err != nil {
			idemErr := loomerrors.Wrap("knowledge.notes_pipeline_retry_failed", "knowledge", decoded, "Could not retry Notes pipeline.", err)
			s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
			s.writeError(w, correlationID, http.StatusBadRequest, "knowledge.notes_pipeline_retry_failed", "knowledge", decoded, "Could not retry Notes pipeline.", err)
			return
		}
		envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, run)
		s.completeIdempotency(ctx, idemRecord, "knowledge_pipeline_run", run.KnowledgePipelineRunID, envelope)
		response.WriteJSON(w, http.StatusOK, envelope)
		return
	}
	s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "knowledge", r.URL.Path, "Method is not allowed.", nil)
}

func (s Server) handleKnowledgeNotesPipelinePolicy(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	service := s.knowledgeService()
	if r.Method == http.MethodGet {
		policy, err := service.GetPipelinePolicy(ctx)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "knowledge.notes_pipeline_policy_failed", "knowledge", "notes_pipeline_policy", "Could not read Notes pipeline policy.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, policy))
		return
	}
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		var input knowledge.PipelinePolicyUpdate
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "knowledge", "notes_pipeline_policy", "Pipeline policy JSON is invalid.", err)
			return
		}
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "knowledge", "bootstrap", "Could not resolve request context.", err)
			return
		}
		idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "knowledge.notes.pipeline.policy", input)
		if !proceed {
			return
		}
		policy, err := service.UpdatePipelinePolicy(ctx, input)
		if err != nil {
			idemErr := loomerrors.Wrap("knowledge.notes_pipeline_policy_update_failed", "knowledge", "notes_pipeline_policy", "Could not update Notes pipeline policy.", err)
			s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
			s.writeError(w, correlationID, http.StatusBadRequest, "knowledge.notes_pipeline_policy_update_failed", "knowledge", "notes_pipeline_policy", "Could not update Notes pipeline policy.", err)
			return
		}
		envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, policy)
		s.completeIdempotency(ctx, idemRecord, "knowledge_pipeline_policy", knowledge.EmbeddingSettingsID, envelope)
		response.WriteJSON(w, http.StatusOK, envelope)
		return
	}
	s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "knowledge", r.URL.Path, "Method is not allowed.", nil)
}

func (s Server) handleKnowledgeNotesPipelineBackfill(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	service := s.knowledgeService()
	if r.Method == http.MethodGet {
		plan, err := service.PlanPipelineBackfill(ctx)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "knowledge.notes_pipeline_backfill_plan_failed", "knowledge", "notes_pipeline_backfill", "Could not plan Notes pipeline backfill.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, plan))
		return
	}
	if r.Method == http.MethodPost {
		var input knowledge.PipelineBackfillInput
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "knowledge", "notes_pipeline_backfill", "Pipeline backfill JSON is invalid.", err)
			return
		}
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "knowledge", "bootstrap", "Could not resolve request context.", err)
			return
		}
		idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "knowledge.notes.pipeline.backfill", input)
		if !proceed {
			return
		}
		result, err := service.ApplyPipelineBackfill(ctx, input)
		if err != nil {
			idemErr := loomerrors.Wrap("knowledge.notes_pipeline_backfill_failed", "knowledge", "notes_pipeline_backfill", "Could not apply Notes pipeline backfill.", err)
			s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
			s.writeError(w, correlationID, http.StatusBadRequest, idemErr.Code, "knowledge", "notes_pipeline_backfill", idemErr.Summary, err)
			return
		}
		envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, result)
		s.completeIdempotency(ctx, idemRecord, "knowledge_pipeline_backfill", "unified", envelope)
		response.WriteJSON(w, http.StatusOK, envelope)
		return
	}
	s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "knowledge", r.URL.Path, "Method is not allowed.", nil)
}

func (s Server) handleKnowledgeNotesEmbeddingsStatus(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "knowledge", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	status, err := s.knowledgeService().GetEmbeddingStatus(ctx)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "knowledge.notes_embeddings_status_failed", "knowledge", "notes_embeddings", "Could not read notes embedding status.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, status))
}

func (s Server) handleKnowledgeNotesEmbeddingsEnable(w http.ResponseWriter, r *http.Request) {
	s.handleKnowledgeNotesEmbeddingsToggle(w, r, true)
}

func (s Server) handleKnowledgeNotesEmbeddingsDisable(w http.ResponseWriter, r *http.Request) {
	s.handleKnowledgeNotesEmbeddingsToggle(w, r, false)
}

func (s Server) handleKnowledgeNotesEmbeddingsToggle(w http.ResponseWriter, r *http.Request, enabled bool) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "knowledge", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "knowledge", "bootstrap", "Could not resolve request context.", err)
		return
	}
	scope := "knowledge.notes.embeddings.disable"
	resultScope := "disabled"
	if enabled {
		scope = "knowledge.notes.embeddings.enable"
		resultScope = "enabled"
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, scope, map[string]any{"enabled": enabled})
	if !proceed {
		return
	}
	result, err := s.knowledgeService().SetEmbeddingsEnabled(ctx, enabled)
	if err != nil {
		idemErr := loomerrors.Wrap("knowledge.notes_embeddings_toggle_failed", "knowledge", "notes_embeddings", "Could not update notes embedding state.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, http.StatusInternalServerError, "knowledge.notes_embeddings_toggle_failed", "knowledge", "notes_embeddings", "Could not update notes embedding state.", err)
		return
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, result)
	s.completeIdempotency(ctx, idemRecord, "knowledge_embeddings", resultScope, envelope)
	response.WriteJSON(w, http.StatusOK, envelope)
}

func (s Server) handleKnowledgeNotesReprocess(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "knowledge", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input knowledge.ReprocessInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "knowledge", "notes_reprocess", "Request body is not valid notes reprocess JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "knowledge", "bootstrap", "Could not resolve request context.", err)
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "knowledge.notes.reprocess", input)
	if !proceed {
		return
	}
	result, err := knowledge.NewService(s.services.DB).QueueReprocess(ctx, input)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, knowledge.ErrInvalid) || errors.Is(err, sql.ErrNoRows) {
			status = http.StatusBadRequest
		}
		idemErr := loomerrors.Wrap("knowledge.notes_reprocess_failed", "knowledge", "notes_reprocess", "Could not queue notes reprocessing.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, status, "knowledge.notes_reprocess_failed", "knowledge", "notes_reprocess", "Could not queue notes reprocessing.", err)
		return
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, result)
	s.completeIdempotency(ctx, idemRecord, "knowledge_reprocess", result.Scope, envelope)
	response.WriteJSON(w, http.StatusOK, envelope)
}

func (s Server) handleKnowledgeNotesProjectionStatus(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "knowledge", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	status, err := s.notesProjectionService().Status(ctx)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "knowledge.notes_projection_status_failed", "knowledge", "notes_projection", "Could not read notes projection status.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, status))
}

func (s Server) handleKnowledgeNotesProjectionRebuild(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "knowledge", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input notesprojection.RebuildInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "knowledge", "notes_projection", "Request body is not valid notes projection rebuild JSON.", err)
		return
	}
	result, err := s.notesProjectionService().Rebuild(ctx, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "knowledge.notes_projection_rebuild_failed", "knowledge", "notes_projection", "Could not rebuild notes projection.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) notesProjectionService() notesprojection.Service {
	root := s.services.RuntimeConfig.NotesProjectionRoot()
	return notesprojection.NewService(s.knowledgeService(), root)
}

func (s Server) knowledgeService() *knowledge.Service {
	options := []knowledge.Option{}
	if s.services.EmbeddingRuntime != nil {
		options = append(options, knowledge.WithEmbeddingRuntime(s.services.EmbeddingRuntime))
	}
	return knowledge.NewService(s.services.DB, options...)
}

func (s Server) handleKnowledgeNotesIndexerRun(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "knowledge", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input workers.RunOnceInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "knowledge", "notes_indexer", "Request body is not valid notes indexer run JSON.", err)
		return
	}
	if input.IdempotencyKey == "" {
		input.IdempotencyKey = strings.TrimSpace(r.Header.Get(idempotency.Header))
	}
	if strings.TrimSpace(input.Reason) == "" {
		input.Reason = "manual notes knowledge indexer run"
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "knowledge", "bootstrap", "Could not resolve request context.", err)
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "knowledge.notes.indexer.run", input)
	if !proceed {
		return
	}
	result, err := s.services.Workers.RunOnce(ctx, req, "main.knowledge_indexer", input)
	if err != nil && result.Run.WorkerRunID == "" {
		status := http.StatusBadRequest
		if errors.Is(err, workers.ErrConflict) {
			status = http.StatusConflict
		}
		if errors.Is(err, workers.ErrNotFound) {
			status = http.StatusNotFound
		}
		idemErr := loomerrors.Wrap("knowledge.notes_indexer_run_failed", "knowledge", "main.knowledge_indexer", "Could not run notes knowledge indexer.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, status, "knowledge.notes_indexer_run_failed", "knowledge", "main.knowledge_indexer", "Could not run notes knowledge indexer.", err)
		return
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, result)
	s.completeIdempotency(ctx, idemRecord, "worker_run", result.Run.WorkerRunID, envelope)
	response.WriteJSON(w, http.StatusOK, envelope)
}

func (s Server) handleKnowledgeNotesEmbedderRun(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "knowledge", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input workers.RunOnceInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "knowledge", "notes_embedder", "Request body is not valid notes embedder run JSON.", err)
		return
	}
	if input.IdempotencyKey == "" {
		input.IdempotencyKey = strings.TrimSpace(r.Header.Get(idempotency.Header))
	}
	if strings.TrimSpace(input.Reason) == "" {
		input.Reason = "manual notes embedding run"
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "knowledge", "bootstrap", "Could not resolve request context.", err)
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "knowledge.notes.embedder.run", input)
	if !proceed {
		return
	}
	result, err := s.services.Workers.RunOnce(ctx, req, "main.knowledge_heavy", input)
	if err != nil && result.Run.WorkerRunID == "" {
		status := http.StatusBadRequest
		if errors.Is(err, workers.ErrConflict) {
			status = http.StatusConflict
		}
		if errors.Is(err, workers.ErrNotFound) {
			status = http.StatusNotFound
		}
		idemErr := loomerrors.Wrap("knowledge.notes_embedder_run_failed", "knowledge", "main.knowledge_embedder", "Could not run notes knowledge embedder.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, status, "knowledge.notes_embedder_run_failed", "knowledge", "main.knowledge_embedder", "Could not run notes knowledge embedder.", err)
		return
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, result)
	s.completeIdempotency(ctx, idemRecord, "worker_run", result.Run.WorkerRunID, envelope)
	response.WriteJSON(w, http.StatusOK, envelope)
}

func sourceRootFilterFromRequest(r *http.Request) (knowledge.SourceRootFilter, error) {
	lifecycle, err := notesLifecycleFromRequest(r)
	if err != nil {
		return knowledge.SourceRootFilter{}, err
	}
	query := r.URL.Query()
	if err := knowledge.ValidateSourceCategory(query.Get("source_category")); err != nil {
		return knowledge.SourceRootFilter{}, err
	}
	filter := knowledge.SourceRootFilter{
		SourceLifecycle:   lifecycle,
		SourceCategory:    query.Get("source_category"),
		NotesSourceRootID: strings.TrimSpace(firstNonEmptyHTTP(query.Get("root"), query.Get("notes_source_root_id"))),
		RootKind:          strings.TrimSpace(query.Get("root_kind")),
		NodeKey:           strings.TrimSpace(query.Get("node_key")),
		ProjectID:         strings.TrimSpace(query.Get("project_id")),
		Status:            strings.TrimSpace(query.Get("status")),
	}
	if raw := strings.TrimSpace(query.Get("include_inactive")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return knowledge.SourceRootFilter{}, err
		}
		filter.IncludeInactive = value
	}
	return filter, nil
}

func notesOverviewInputFromRequest(r *http.Request) (knowledge.NotesOverviewInput, error) {
	lifecycle, err := notesLifecycleFromRequest(r)
	if err != nil {
		return knowledge.NotesOverviewInput{}, err
	}
	query := r.URL.Query()
	if err := knowledge.ValidateSourceCategory(query.Get("source_category")); err != nil {
		return knowledge.NotesOverviewInput{}, err
	}
	input := knowledge.NotesOverviewInput{
		SourceLifecycle: lifecycle,
		SourceCategory:  query.Get("source_category"),
		NodeKey:         strings.TrimSpace(firstNonEmptyHTTP(query.Get("node_key"), query.Get("node"))),
		ProjectID:       strings.TrimSpace(firstNonEmptyHTTP(query.Get("project_id"), query.Get("project"))),
	}
	if raw := strings.TrimSpace(query.Get("include_inactive")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return knowledge.NotesOverviewInput{}, err
		}
		input.IncludeInactive = value
	}
	return input, nil
}

func knowledgeObjectFilterFromRequest(r *http.Request) (knowledge.KnowledgeObjectFilter, error) {
	lifecycle, err := notesLifecycleFromRequest(r)
	if err != nil {
		return knowledge.KnowledgeObjectFilter{}, err
	}
	query := r.URL.Query()
	if err := knowledge.ValidateSourceCategory(query.Get("source_category")); err != nil {
		return knowledge.KnowledgeObjectFilter{}, err
	}
	filter := knowledge.KnowledgeObjectFilter{
		SourceLifecycle:   lifecycle,
		SourceCategory:    query.Get("source_category"),
		NotesSourceRootID: strings.TrimSpace(firstNonEmptyHTTP(query.Get("root"), query.Get("notes_source_root_id"))),
		ProjectID:         strings.TrimSpace(query.Get("project_id")),
		SourceNodeKey:     strings.TrimSpace(query.Get("node_key")),
		ProcessingState:   strings.TrimSpace(query.Get("processing_state")),
	}
	if raw := strings.TrimSpace(query.Get("include_deleted")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return knowledge.KnowledgeObjectFilter{}, err
		}
		filter.IncludeDeleted = value
	}
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			return knowledge.KnowledgeObjectFilter{}, err
		}
		filter.Limit = value
	}
	if raw := strings.TrimSpace(query.Get("offset")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			return knowledge.KnowledgeObjectFilter{}, err
		}
		filter.Offset = value
	}
	return filter, nil
}

func notesLifecycleFromRequest(r *http.Request) (knowledge.SourceLifecycleFilter, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", err
	}
	if len(values["source_lifecycle"]) > 1 {
		return "", knowledge.ErrInvalid
	}
	return knowledge.NormalizeSourceLifecycleFilter(knowledge.SourceLifecycleFilter(values.Get("source_lifecycle")))
}
