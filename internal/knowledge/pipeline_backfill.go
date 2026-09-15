package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

type PipelineBackfillInput struct {
	Confirm  bool `json:"confirm"`
	Priority int  `json:"priority,omitempty"`
}

type PipelineBackfillCandidate struct {
	KnowledgeObjectID          string   `json:"knowledge_object_id"`
	FileClass                  string   `json:"file_class"`
	FileFamily                 string   `json:"file_family"`
	PipelineDefinitionKey      string   `json:"pipeline_definition_key"`
	PipelineDefinitionVersion  string   `json:"pipeline_definition_version"`
	Stages                     []string `json:"stages"`
	ExistingPipeline           bool     `json:"existing_pipeline"`
	ReusableChunks             int      `json:"reusable_chunks"`
	ReusableLexicalDocuments   int      `json:"reusable_lexical_documents"`
	ReusableVectors            int      `json:"reusable_vectors"`
	RequiresSourceReprocessing bool     `json:"requires_source_reprocessing"`
	BlockedReason              string   `json:"blocked_reason,omitempty"`
	object                     KnowledgeObject
}

type LegacyEmbeddingBackfillStatus struct {
	TotalRows    int `json:"total_rows"`
	PendingRows  int `json:"pending_rows"`
	ActiveClaims int `json:"active_claims"`
}

type PipelineBackfillPlan struct {
	DryRun                     bool                          `json:"dry_run"`
	TotalObjects               int                           `json:"total_objects"`
	ObjectsToCreate            int                           `json:"objects_to_create"`
	ExistingPipelines          int                           `json:"existing_pipelines"`
	RequiresSourceReprocessing int                           `json:"requires_source_reprocessing"`
	ReusableChunks             int                           `json:"reusable_chunks"`
	ReusableLexicalDocuments   int                           `json:"reusable_lexical_documents"`
	ReusableVectors            int                           `json:"reusable_vectors"`
	ByFileClass                map[string]int                `json:"by_file_class"`
	StagePlan                  map[string]int                `json:"stage_plan"`
	BlockedReasons             map[string]int                `json:"blocked_reasons,omitempty"`
	LegacyEmbedding            LegacyEmbeddingBackfillStatus `json:"legacy_embedding"`
	Candidates                 []PipelineBackfillCandidate   `json:"candidates,omitempty"`
}

type PipelineBackfillResult struct {
	Plan                 PipelineBackfillPlan `json:"plan"`
	CreatedPipelines     int                  `json:"created_pipelines"`
	ExistingPipelines    int                  `json:"existing_pipelines"`
	BlockedObjects       int                  `json:"blocked_objects"`
	LegacyRowsHistorical int                  `json:"legacy_rows_historical"`
}

func (s *Service) PlanPipelineBackfill(ctx context.Context) (PipelineBackfillPlan, error) {
	if s == nil || s.store.db == nil {
		return PipelineBackfillPlan{}, fmt.Errorf("knowledge store is not configured")
	}
	policy, err := s.GetPipelinePolicy(ctx)
	if err != nil {
		return PipelineBackfillPlan{}, err
	}
	objects, err := s.listAllBackfillObjects(ctx)
	if err != nil {
		return PipelineBackfillPlan{}, err
	}
	candidates := make([]PipelineBackfillCandidate, 0, len(objects))
	for _, object := range objects {
		candidate, err := s.pipelineBackfillCandidate(ctx, object, policy.Policy)
		if err != nil {
			return PipelineBackfillPlan{}, err
		}
		candidates = append(candidates, candidate)
	}
	legacy, err := s.legacyEmbeddingBackfillStatus(ctx)
	if err != nil {
		return PipelineBackfillPlan{}, err
	}
	plan := summarizePipelineBackfillCandidates(candidates)
	plan.DryRun = true
	plan.LegacyEmbedding = legacy
	return plan, nil
}

func (s *Service) ApplyPipelineBackfill(ctx context.Context, input PipelineBackfillInput) (PipelineBackfillResult, error) {
	if err := validatePipelineBackfillApply(input.Confirm, 0); err != nil {
		return PipelineBackfillResult{}, err
	}
	plan, err := s.PlanPipelineBackfill(ctx)
	if err != nil {
		return PipelineBackfillResult{}, err
	}
	if err := validatePipelineBackfillApply(true, plan.LegacyEmbedding.ActiveClaims); err != nil {
		return PipelineBackfillResult{}, err
	}
	policy, err := s.GetPipelinePolicy(ctx)
	if err != nil {
		return PipelineBackfillResult{}, err
	}
	result := PipelineBackfillResult{Plan: plan}
	for _, candidate := range plan.Candidates {
		if candidate.BlockedReason != "" {
			result.BlockedObjects++
			continue
		}
		if candidate.ExistingPipeline {
			result.ExistingPipelines++
			continue
		}
		run, err := s.EnsurePipelineRun(ctx, candidate.object, policy.Policy, false, input.Priority)
		if err != nil {
			return result, fmt.Errorf("backfill object %s: %w", candidate.KnowledgeObjectID, err)
		}
		if err := s.recordPipelineBackfillReuse(ctx, run.KnowledgePipelineRunID, candidate); err != nil {
			return result, err
		}
		result.CreatedPipelines++
	}
	result.LegacyRowsHistorical, err = s.markLegacyEmbeddingRowsHistorical(ctx)
	return result, err
}

func validatePipelineBackfillApply(confirm bool, activeLegacyClaims int) error {
	if !confirm {
		return fmt.Errorf("%w: pipeline backfill apply requires explicit confirmation", ErrInvalid)
	}
	if activeLegacyClaims > 0 {
		return fmt.Errorf("%w: %d active legacy embedding claim(s) prevent queue retirement", ErrInvalid, activeLegacyClaims)
	}
	return nil
}

func (s *Service) listAllBackfillObjects(ctx context.Context) ([]KnowledgeObject, error) {
	const pageSize = 5000
	result := []KnowledgeObject{}
	for offset := 0; ; offset += pageSize {
		page, err := s.store.ListKnowledgeObjects(ctx, KnowledgeObjectFilter{Limit: pageSize, Offset: offset})
		if err != nil {
			return nil, err
		}
		result = append(result, page...)
		if len(page) < pageSize {
			return result, nil
		}
	}
}

func (s *Service) pipelineBackfillCandidate(ctx context.Context, object KnowledgeObject, policy PipelinePolicy) (PipelineBackfillCandidate, error) {
	plan, err := CompilePipelinePlan(object, policy)
	if err != nil {
		return PipelineBackfillCandidate{}, err
	}
	candidate := PipelineBackfillCandidate{
		KnowledgeObjectID:         object.KnowledgeObjectID,
		FileClass:                 object.FileClass,
		FileFamily:                plan.FileFamily,
		PipelineDefinitionKey:     plan.DefinitionKey,
		PipelineDefinitionVersion: plan.DefinitionVersion,
		object:                    object,
	}
	for _, stage := range plan.Stages {
		if stage.Selected {
			candidate.Stages = append(candidate.Stages, stage.StageKey)
		}
	}
	if err := ValidateKnowledgeObject(object); err != nil {
		candidate.BlockedReason = "invalid_knowledge_object"
		return candidate, nil
	}
	var versionID string
	err = s.store.db.QueryRowContext(ctx, `SELECT knowledge_object_version_id FROM knowledge.knowledge_object_versions WHERE knowledge_object_id=$1 AND source_hash=$2 AND source_revision=$3 ORDER BY version_number DESC LIMIT 1`, object.KnowledgeObjectID, object.SourceHash, object.SourceRevision).Scan(&versionID)
	if err == sql.ErrNoRows {
		candidate.RequiresSourceReprocessing = true
		return candidate, nil
	}
	if err != nil {
		return PipelineBackfillCandidate{}, err
	}
	var existingSnapshot []byte
	err = s.store.db.QueryRowContext(ctx, `SELECT plan_snapshot FROM knowledge.pipeline_runs WHERE knowledge_object_version_id=$1 AND pipeline_definition_key=$2 AND pipeline_definition_version=$3 AND status<>'stale' ORDER BY generation DESC LIMIT 1`, versionID, plan.DefinitionKey, plan.DefinitionVersion).Scan(&existingSnapshot)
	if err != nil && err != sql.ErrNoRows {
		return PipelineBackfillCandidate{}, err
	}
	if err == nil {
		snapshot, snapshotErr := plan.Snapshot()
		if snapshotErr != nil {
			return PipelineBackfillCandidate{}, snapshotErr
		}
		candidate.ExistingPipeline = pipelinePlanSnapshotsEqual(existingSnapshot, snapshot)
	}
	if err := s.store.db.QueryRowContext(ctx, `SELECT count(*)::int FROM knowledge.knowledge_chunks WHERE knowledge_object_version_id=$1`, versionID).Scan(&candidate.ReusableChunks); err != nil {
		return PipelineBackfillCandidate{}, err
	}
	if err := s.store.db.QueryRowContext(ctx, `SELECT count(*)::int FROM search.search_documents WHERE source_kind=$1 AND source_version_id=$2`, KnowledgeSearchSourceKind, versionID).Scan(&candidate.ReusableLexicalDocuments); err != nil {
		return PipelineBackfillCandidate{}, err
	}
	if err := s.store.db.QueryRowContext(ctx, `SELECT count(*)::int FROM knowledge.chunk_embeddings WHERE knowledge_object_version_id=$1 AND status IN ('active','reusable')`, versionID).Scan(&candidate.ReusableVectors); err != nil {
		return PipelineBackfillCandidate{}, err
	}
	return candidate, nil
}

func summarizePipelineBackfillCandidates(candidates []PipelineBackfillCandidate) PipelineBackfillPlan {
	plan := PipelineBackfillPlan{ByFileClass: map[string]int{}, StagePlan: map[string]int{}, BlockedReasons: map[string]int{}, Candidates: candidates}
	plan.TotalObjects = len(candidates)
	for _, candidate := range candidates {
		plan.ByFileClass[firstNonEmpty(candidate.FileClass, "unknown")]++
		for _, stage := range candidate.Stages {
			plan.StagePlan[stage]++
		}
		plan.ReusableChunks += candidate.ReusableChunks
		plan.ReusableLexicalDocuments += candidate.ReusableLexicalDocuments
		plan.ReusableVectors += candidate.ReusableVectors
		if candidate.RequiresSourceReprocessing {
			plan.RequiresSourceReprocessing++
		}
		if candidate.BlockedReason != "" {
			plan.BlockedReasons[candidate.BlockedReason]++
			continue
		}
		if candidate.ExistingPipeline {
			plan.ExistingPipelines++
		} else {
			plan.ObjectsToCreate++
		}
	}
	return plan
}

func (s *Service) legacyEmbeddingBackfillStatus(ctx context.Context) (LegacyEmbeddingBackfillStatus, error) {
	var status LegacyEmbeddingBackfillStatus
	err := s.store.db.QueryRowContext(ctx, `SELECT count(*)::int,count(*) FILTER(WHERE status IN ('queued','processing','failed'))::int,count(*) FILTER(WHERE status='processing' AND claimed_by_worker_run_id<>'' AND claim_expires_at>now())::int FROM knowledge.embedding_work_items`).Scan(&status.TotalRows, &status.PendingRows, &status.ActiveClaims)
	return status, err
}

func (s *Service) markLegacyEmbeddingRowsHistorical(ctx context.Context) (int, error) {
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `LOCK TABLE knowledge.embedding_work_items IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return 0, err
	}
	var activeClaims int
	if err = tx.QueryRowContext(ctx, `SELECT count(*)::int FROM knowledge.embedding_work_items WHERE status='processing' AND claimed_by_worker_run_id<>'' AND claim_expires_at>now()`).Scan(&activeClaims); err != nil {
		return 0, err
	}
	if err = validatePipelineBackfillApply(true, activeClaims); err != nil {
		return 0, err
	}
	metadata, _ := json.Marshal(map[string]any{"schema_version": "knowledge.embedding_work_item.metadata.v1", "migration_state": "historical", "replacement": "knowledge.pipeline_runs"})
	result, err := tx.ExecContext(ctx, `UPDATE knowledge.embedding_work_items SET status=CASE WHEN status IN ('queued','processing','failed','disabled_by_policy','skipped_unsupported') THEN 'stale' ELSE status END,claimed_by_worker_run_id='',claim_expires_at=NULL,metadata=metadata || $1::jsonb,updated_at=now() WHERE NOT (metadata @> '{"migration_state":"historical"}'::jsonb)`, metadata)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return int(count), nil
}

func (s *Service) recordPipelineBackfillReuse(ctx context.Context, runID string, candidate PipelineBackfillCandidate) error {
	metadata, _ := json.Marshal(map[string]any{"schema_version": "knowledge.pipeline_backfill_reuse.v1", "chunks": candidate.ReusableChunks, "lexical_documents": candidate.ReusableLexicalDocuments, "vectors": candidate.ReusableVectors, "requires_source_reprocessing": candidate.RequiresSourceReprocessing})
	_, err := s.store.db.ExecContext(ctx, `UPDATE knowledge.pipeline_runs SET metadata=metadata || jsonb_build_object('backfill', $2::jsonb),updated_at=now() WHERE knowledge_pipeline_run_id=$1`, runID, metadata)
	return err
}
