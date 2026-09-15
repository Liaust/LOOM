package localclient

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/notesprojection"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/workers"
)

func (c Client) ListKnowledgeNotesRoots(ctx context.Context, correlationID string, filter knowledge.SourceRootFilter) (response.Envelope[[]knowledge.SourceRoot], error) {
	values := url.Values{}
	if _, err := knowledge.NormalizeSourceLifecycleFilter(filter.SourceLifecycle); err != nil {
		return response.Envelope[[]knowledge.SourceRoot]{}, err
	}
	if filter.SourceLifecycle != "" {
		values.Set("source_lifecycle", string(filter.SourceLifecycle))
	}
	if filter.SourceCategory != "" {
		values.Set("source_category", filter.SourceCategory)
	}
	if filter.NotesSourceRootID != "" {
		values.Set("notes_source_root_id", filter.NotesSourceRootID)
	}
	if filter.RootKind != "" {
		values.Set("root_kind", filter.RootKind)
	}
	if filter.NodeKey != "" {
		values.Set("node_key", filter.NodeKey)
	}
	if filter.ProjectID != "" {
		values.Set("project_id", filter.ProjectID)
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.IncludeInactive {
		values.Set("include_inactive", strconv.FormatBool(filter.IncludeInactive))
	}
	path := "/v1/knowledge/notes/roots"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]knowledge.SourceRoot](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) ReconcileKnowledgeNotesRoots(ctx context.Context, correlationID string, input knowledge.SourceRootReconcileInput) (response.Envelope[knowledge.SourceRootReconcileResult], error) {
	return doJSON[knowledge.SourceRootReconcileResult](c, ctx, http.MethodPost, "/v1/knowledge/notes/roots/reconcile", correlationID, input)
}

func (c Client) GetKnowledgeNotesOverview(ctx context.Context, correlationID string, input knowledge.NotesOverviewInput) (response.Envelope[knowledge.NotesOverview], error) {
	values := url.Values{}
	if _, err := knowledge.NormalizeSourceLifecycleFilter(input.SourceLifecycle); err != nil {
		return response.Envelope[knowledge.NotesOverview]{}, err
	}
	if input.SourceLifecycle != "" {
		values.Set("source_lifecycle", string(input.SourceLifecycle))
	}
	if input.SourceCategory != "" {
		values.Set("source_category", input.SourceCategory)
	}
	if input.NodeKey != "" {
		values.Set("node_key", input.NodeKey)
	}
	if input.ProjectID != "" {
		values.Set("project_id", input.ProjectID)
	}
	if input.IncludeInactive {
		values.Set("include_inactive", strconv.FormatBool(input.IncludeInactive))
	}
	path := "/v1/knowledge/notes/overview"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[knowledge.NotesOverview](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) ListKnowledgeNotesObjects(ctx context.Context, correlationID string, filter knowledge.KnowledgeObjectFilter) (response.Envelope[[]knowledge.KnowledgeObject], error) {
	values := url.Values{}
	if _, err := knowledge.NormalizeSourceLifecycleFilter(filter.SourceLifecycle); err != nil {
		return response.Envelope[[]knowledge.KnowledgeObject]{}, err
	}
	if filter.SourceLifecycle != "" {
		values.Set("source_lifecycle", string(filter.SourceLifecycle))
	}
	if filter.SourceCategory != "" {
		values.Set("source_category", filter.SourceCategory)
	}
	if filter.NotesSourceRootID != "" {
		values.Set("notes_source_root_id", filter.NotesSourceRootID)
	}
	if filter.ProjectID != "" {
		values.Set("project_id", filter.ProjectID)
	}
	if filter.SourceNodeKey != "" {
		values.Set("node_key", filter.SourceNodeKey)
	}
	if filter.ProcessingState != "" {
		values.Set("processing_state", filter.ProcessingState)
	}
	if filter.IncludeDeleted {
		values.Set("include_deleted", strconv.FormatBool(filter.IncludeDeleted))
	}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Offset > 0 {
		values.Set("offset", strconv.Itoa(filter.Offset))
	}
	path := "/v1/knowledge/notes/objects"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]knowledge.KnowledgeObject](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetKnowledgeNotesObject(ctx context.Context, correlationID, ref string) (response.Envelope[knowledge.KnowledgeObject], error) {
	return c.GetKnowledgeNotesObjectWithLifecycle(ctx, correlationID, ref, "")
}

func (c Client) GetKnowledgeNotesObjectWithLifecycle(ctx context.Context, correlationID, ref string, lifecycle knowledge.SourceLifecycleFilter) (response.Envelope[knowledge.KnowledgeObject], error) {
	if _, err := knowledge.NormalizeSourceLifecycleFilter(lifecycle); err != nil {
		return response.Envelope[knowledge.KnowledgeObject]{}, err
	}
	path := "/v1/knowledge/notes/objects/" + url.PathEscape(ref)
	if lifecycle != "" {
		path += "?" + url.Values{"source_lifecycle": {string(lifecycle)}}.Encode()
	}
	return doJSON[knowledge.KnowledgeObject](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetKnowledgeNotesPassage(ctx context.Context, correlationID string, input knowledge.NotesPassageInput) (response.Envelope[knowledge.NotesPassage], error) {
	if err := knowledge.ValidateNotesPassageInput(input); err != nil {
		return response.Envelope[knowledge.NotesPassage]{}, err
	}
	query := url.Values{"object_id": {input.KnowledgeObjectID}, "version_id": {input.KnowledgeObjectVersionID}, "source_hash": {input.SourceHash}}
	if input.SourceLifecycle != "" {
		query.Set("source_lifecycle", string(input.SourceLifecycle))
	}
	return doJSON[knowledge.NotesPassage](c, ctx, http.MethodGet, "/v1/knowledge/notes/passages/"+url.PathEscape(input.KnowledgeChunkID)+"?"+query.Encode(), correlationID, nil)
}

func (c Client) ReconcileKnowledgeNotesObjects(ctx context.Context, correlationID string, input knowledge.KnowledgeObjectReconcileInput) (response.Envelope[knowledge.KnowledgeObjectReconcileResult], error) {
	return doJSON[knowledge.KnowledgeObjectReconcileResult](c, ctx, http.MethodPost, "/v1/knowledge/notes/objects/reconcile", correlationID, input)
}

func (c Client) SearchKnowledgeNotes(ctx context.Context, correlationID string, input knowledge.NotesSearchInput) (response.Envelope[knowledge.NotesSearchResultSet], error) {
	if _, err := knowledge.NormalizeSourceLifecycleFilter(input.SourceLifecycle); err != nil {
		return response.Envelope[knowledge.NotesSearchResultSet]{}, err
	}
	return doJSON[knowledge.NotesSearchResultSet](c, ctx, http.MethodPost, "/v1/knowledge/notes/search", correlationID, input)
}

func (c Client) GetKnowledgeNotesEmbeddingStatus(ctx context.Context, correlationID string) (response.Envelope[knowledge.EmbeddingStatus], error) {
	return doJSON[knowledge.EmbeddingStatus](c, ctx, http.MethodGet, "/v1/knowledge/notes/embeddings/status", correlationID, nil)
}

func (c Client) EnableKnowledgeNotesEmbeddings(ctx context.Context, correlationID string) (response.Envelope[knowledge.SetEmbeddingsEnabledResult], error) {
	return doJSON[knowledge.SetEmbeddingsEnabledResult](c, ctx, http.MethodPost, "/v1/knowledge/notes/embeddings/enable", correlationID, nil)
}

func (c Client) DisableKnowledgeNotesEmbeddings(ctx context.Context, correlationID string) (response.Envelope[knowledge.SetEmbeddingsEnabledResult], error) {
	return doJSON[knowledge.SetEmbeddingsEnabledResult](c, ctx, http.MethodPost, "/v1/knowledge/notes/embeddings/disable", correlationID, nil)
}

func (c Client) GetKnowledgeNotesPipelineStatus(ctx context.Context, correlationID string) (response.Envelope[knowledge.PipelineOverallStatus], error) {
	return doJSON[knowledge.PipelineOverallStatus](c, ctx, http.MethodGet, "/v1/knowledge/notes/pipelines/status", correlationID, nil)
}
func (c Client) ListKnowledgeNotesPipelines(ctx context.Context, correlationID string, input knowledge.PipelineListInput) (response.Envelope[[]knowledge.PipelineRun], error) {
	values := url.Values{}
	if input.Status != "" {
		values.Set("status", input.Status)
	}
	if input.ObjectID != "" {
		values.Set("object_id", input.ObjectID)
	}
	if input.Limit > 0 {
		values.Set("limit", strconv.Itoa(input.Limit))
	}
	path := "/v1/knowledge/notes/pipelines"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]knowledge.PipelineRun](c, ctx, http.MethodGet, path, correlationID, nil)
}
func (c Client) GetKnowledgeNotesPipeline(ctx context.Context, correlationID, ref string) (response.Envelope[knowledge.PipelineInspect], error) {
	return doJSON[knowledge.PipelineInspect](c, ctx, http.MethodGet, "/v1/knowledge/notes/pipelines/"+url.PathEscape(ref), correlationID, nil)
}
func (c Client) ListKnowledgeNotesPipelineFailures(ctx context.Context, correlationID string) (response.Envelope[[]knowledge.PipelineRun], error) {
	return doJSON[[]knowledge.PipelineRun](c, ctx, http.MethodGet, "/v1/knowledge/notes/pipelines/failures", correlationID, nil)
}
func (c Client) RetryKnowledgeNotesPipeline(ctx context.Context, correlationID, ref string, input knowledge.PipelineRetryInput) (response.Envelope[knowledge.PipelineRun], error) {
	return doJSON[knowledge.PipelineRun](c, ctx, http.MethodPost, "/v1/knowledge/notes/pipelines/"+url.PathEscape(ref)+"/retry", correlationID, input)
}
func (c Client) GetKnowledgeNotesPipelinePolicy(ctx context.Context, correlationID string) (response.Envelope[knowledge.PipelinePolicyStatus], error) {
	return doJSON[knowledge.PipelinePolicyStatus](c, ctx, http.MethodGet, "/v1/knowledge/notes/pipelines/policy", correlationID, nil)
}
func (c Client) UpdateKnowledgeNotesPipelinePolicy(ctx context.Context, correlationID string, input knowledge.PipelinePolicyUpdate) (response.Envelope[knowledge.PipelinePolicyStatus], error) {
	return doJSON[knowledge.PipelinePolicyStatus](c, ctx, http.MethodPost, "/v1/knowledge/notes/pipelines/policy", correlationID, input)
}
func (c Client) PlanKnowledgeNotesPipelineBackfill(ctx context.Context, correlationID string) (response.Envelope[knowledge.PipelineBackfillPlan], error) {
	return doJSON[knowledge.PipelineBackfillPlan](c, ctx, http.MethodGet, "/v1/knowledge/notes/pipelines/backfill", correlationID, nil)
}
func (c Client) ApplyKnowledgeNotesPipelineBackfill(ctx context.Context, correlationID string, input knowledge.PipelineBackfillInput) (response.Envelope[knowledge.PipelineBackfillResult], error) {
	return doJSON[knowledge.PipelineBackfillResult](c, ctx, http.MethodPost, "/v1/knowledge/notes/pipelines/backfill", correlationID, input)
}
func (c Client) RunKnowledgeNotesCoordinator(ctx context.Context, correlationID string, input workers.RunOnceInput) (response.Envelope[workers.RunOnceResult], error) {
	return doJSON[workers.RunOnceResult](c, ctx, http.MethodPost, "/v1/knowledge/notes/workers/coordinator/run", correlationID, input)
}
func (c Client) RunKnowledgeNotesHeavy(ctx context.Context, correlationID string, input workers.RunOnceInput) (response.Envelope[workers.RunOnceResult], error) {
	return doJSON[workers.RunOnceResult](c, ctx, http.MethodPost, "/v1/knowledge/notes/workers/heavy/run", correlationID, input)
}

func (c Client) ReprocessKnowledgeNotes(ctx context.Context, correlationID string, input knowledge.ReprocessInput) (response.Envelope[knowledge.ReprocessResult], error) {
	return doJSON[knowledge.ReprocessResult](c, ctx, http.MethodPost, "/v1/knowledge/notes/reprocess", correlationID, input)
}

func (c Client) GetKnowledgeNotesProjectionStatus(ctx context.Context, correlationID string) (response.Envelope[notesprojection.Status], error) {
	return doJSON[notesprojection.Status](c, ctx, http.MethodGet, "/v1/knowledge/notes/projection/status", correlationID, nil)
}

func (c Client) RebuildKnowledgeNotesProjection(ctx context.Context, correlationID string, input notesprojection.RebuildInput) (response.Envelope[notesprojection.RebuildResult], error) {
	return doJSON[notesprojection.RebuildResult](c, ctx, http.MethodPost, "/v1/knowledge/notes/projection/rebuild", correlationID, input)
}

func (c Client) RunKnowledgeNotesIndexer(ctx context.Context, correlationID string, input workers.RunOnceInput) (response.Envelope[workers.RunOnceResult], error) {
	return doJSON[workers.RunOnceResult](c, ctx, http.MethodPost, "/v1/knowledge/notes/workers/indexer/run", correlationID, input)
}

func (c Client) RunKnowledgeNotesEmbedder(ctx context.Context, correlationID string, input workers.RunOnceInput) (response.Envelope[workers.RunOnceResult], error) {
	return doJSON[workers.RunOnceResult](c, ctx, http.MethodPost, "/v1/knowledge/notes/workers/embedder/run", correlationID, input)
}
