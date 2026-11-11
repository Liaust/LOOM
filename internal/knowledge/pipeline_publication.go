package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"time"

	"loom.local/loom/internal/projectcontracts"
)

type PipelineRefreshStatus struct {
	LatestSourceRevision   string                                  `json:"latest_source_revision"`
	LatestSourceHash       string                                  `json:"latest_source_hash"`
	SelectedSourceRevision string                                  `json:"selected_source_revision,omitempty"`
	Pending                bool                                    `json:"pending"`
	PendingSince           *time.Time                              `json:"pending_since,omitempty"`
	LexicalVersionID       string                                  `json:"lexical_version_id,omitempty"`
	SemanticVersionID      string                                  `json:"semantic_version_id,omitempty"`
	LexicalPublishedAt     *time.Time                              `json:"lexical_published_at,omitempty"`
	SemanticPublishedAt    *time.Time                              `json:"semantic_published_at,omitempty"`
	RequestedPolicy        *projectcontracts.KnowledgeSourcePolicy `json:"requested_policy,omitempty"`
}

func (s *Service) pipelineRefreshStatus(ctx context.Context, run PipelineRun) (PipelineRefreshStatus, error) {
	var result PipelineRefreshStatus
	var metadata []byte
	var relativePath, fileClass string
	var lexical, semantic sql.NullTime
	err := s.store.db.QueryRowContext(ctx, `SELECT source_revision,source_hash,metadata,COALESCE(lexical_version_id,''),COALESCE(semantic_version_id,''),lexical_published_at,semantic_published_at,relative_path,file_class
	 FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1`, run.KnowledgeObjectID).Scan(&result.LatestSourceRevision, &result.LatestSourceHash, &metadata, &result.LexicalVersionID, &result.SemanticVersionID, &lexical, &semantic, &relativePath, &fileClass)
	if err != nil {
		return result, err
	}
	if len(run.SourceSnapshot) > 0 {
		result.SelectedSourceRevision = run.SourceRevision
	}
	result.Pending = result.LatestSourceRevision != run.SourceRevision || result.LatestSourceHash != run.SourceHash
	object := KnowledgeObject{Metadata: json.RawMessage(metadata), RelativePath: relativePath, FileClass: fileClass}
	result.PendingSince = objectRefreshClock(object).PendingSince
	result.LexicalPublishedAt, result.SemanticPublishedAt = nullTimePtr(lexical), nullTimePtr(semantic)
	result.RequestedPolicy, err = knowledgeSourcePolicy(object)
	return result, err
}

func withdrawIncompatiblePublicationsTx(ctx context.Context, tx *sql.Tx, objectID string, plan CompiledPipelinePlan) error {
	if !compiledPlanContainsSelectedStage(plan, FilePipelineStageChunk) {
		return withdrawBodyPublicationsTx(ctx, tx, objectID, compiledPlanContainsSelectedStage(plan, FilePipelineStageLexicalIndex))
	}
	var previousLexical, previousSemantic sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT lexical_version_id,semantic_version_id
	 FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1`, objectID).Scan(&previousLexical, &previousSemantic); err != nil {
		return err
	}
	for _, stage := range plan.Stages {
		if stage.Selected {
			continue
		}
		// Adding OCR/vision must not recompute source-disabled embeddings or remove
		// the exact earlier semantic publication authorized for these same bytes.
		if plan.Enrichment != nil && stage.StageKey == FilePipelineStageEmbedding && plan.Enrichment.Preserved.Embeddings {
			continue
		}
		switch stage.StageKey {
		case FilePipelineStageEmbedding, FilePipelineStagePDFOCR, FilePipelineStageImageOCR, FilePipelineStageImageDescription, FilePipelineStageNativeText, FilePipelineStageLexicalIndex:
		default:
			continue
		}
		// Withdraw disabled derivations without waiting for a replacement.
		lexical := stage.StageKey != FilePipelineStageEmbedding
		_, err := tx.ExecContext(ctx, `WITH replacement AS MATERIALIZED (
		 SELECT o.knowledge_object_id,o.semantic_version_id AS previous_semantic_version_id,
		 CASE WHEN EXISTS (SELECT 1 FROM knowledge.pipeline_runs r,jsonb_array_elements(r.plan_snapshot->'stages') s
		 WHERE r.knowledge_object_version_id=o.semantic_version_id AND s->>'stage_key'=$2 AND s->>'selected'='true') THEN NULL ELSE o.semantic_version_id END,
		 CASE WHEN $3 AND EXISTS (SELECT 1 FROM knowledge.pipeline_runs r,jsonb_array_elements(r.plan_snapshot->'stages') s
		 WHERE r.knowledge_object_version_id=o.lexical_version_id AND s->>'stage_key'=$2 AND s->>'selected'='true') THEN NULL ELSE o.lexical_version_id END
		 FROM knowledge.knowledge_objects o WHERE o.knowledge_object_id=$1
		 ), withdrawn AS (UPDATE knowledge.knowledge_objects o SET
		 semantic_version_id=p.semantic_version_id,lexical_version_id=p.lexical_version_id
		 FROM replacement AS p(knowledge_object_id,previous_semantic_version_id,semantic_version_id,lexical_version_id)
		 WHERE o.knowledge_object_id=p.knowledge_object_id
		 AND (o.semantic_version_id,o.lexical_version_id) IS DISTINCT FROM (p.semantic_version_id,p.lexical_version_id)
		 RETURNING p.previous_semantic_version_id,o.semantic_version_id)
		 UPDATE knowledge.chunk_embeddings e SET active=false,status='historical',deactivated_at=COALESCE(deactivated_at,now())
		 FROM withdrawn w WHERE w.previous_semantic_version_id IS NOT NULL AND w.semantic_version_id IS NULL
		 AND e.knowledge_object_version_id=w.previous_semantic_version_id AND e.active`, objectID, stage.StageKey, lexical)
		if err != nil {
			return err
		}
	}
	// Retirement keeps historical rows/citations, but current vector flags and
	// publication descriptors must agree with the withdrawn semantic pointer.
	result, err := tx.ExecContext(ctx, `UPDATE knowledge.knowledge_objects SET
	 lexical_published_at=CASE WHEN lexical_version_id IS NULL THEN NULL ELSE lexical_published_at END,
	 semantic_published_at=CASE WHEN semantic_version_id IS NULL THEN NULL ELSE semantic_published_at END,
	 semantic_runtime_key=CASE WHEN semantic_version_id IS NULL THEN NULL ELSE semantic_runtime_key END,
	 semantic_model_key=CASE WHEN semantic_version_id IS NULL THEN NULL ELSE semantic_model_key END,
	 semantic_dimensions=CASE WHEN semantic_version_id IS NULL THEN NULL ELSE semantic_dimensions END
	 WHERE knowledge_object_id=$1 AND ((lexical_version_id IS NULL AND lexical_published_at IS NOT NULL) OR
	 (semantic_version_id IS NULL AND (semantic_published_at IS NOT NULL OR semantic_runtime_key IS NOT NULL OR semantic_model_key IS NOT NULL OR semantic_dimensions IS NOT NULL)))`, objectID)
	if err != nil {
		return err
	}
	cleaned, err := result.RowsAffected()
	if err != nil {
		return err
	}
	var lexical, semantic sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT lexical_version_id,semantic_version_id
	 FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1`, objectID).Scan(&lexical, &semantic); err != nil {
		return err
	}
	if cleaned > 0 || lexical != previousLexical || semantic != previousSemantic {
		return MarkProjectionDirtyTx(ctx, tx, ProjectionDirtyInput{ObjectIDs: []string{objectID}})
	}
	return nil
}

// A metadata-only replacement withdraws body retrieval immediately, including
// legacy publications without a unified run. Only derived search rows are
// removed; source versions, chunks, artifacts and recovery history remain.
func withdrawBodyPublicationsTx(ctx context.Context, tx *sql.Tx, objectID string, metadataSearch bool) error {
	changed := false
	queries := []string{
		`UPDATE knowledge.knowledge_objects SET lexical_version_id=NULL,lexical_published_at=NULL,
		 semantic_version_id=NULL,semantic_published_at=NULL,semantic_runtime_key=NULL,semantic_model_key=NULL,semantic_dimensions=NULL
		 WHERE knowledge_object_id=$1 AND (lexical_version_id IS NOT NULL OR lexical_published_at IS NOT NULL OR
		 semantic_version_id IS NOT NULL OR semantic_published_at IS NOT NULL OR semantic_runtime_key IS NOT NULL OR
		 semantic_model_key IS NOT NULL OR semantic_dimensions IS NOT NULL)`,
		`UPDATE knowledge.chunk_embeddings SET active=false,status='historical',deactivated_at=COALESCE(deactivated_at,now())
		 WHERE knowledge_object_id=$1 AND active`,
		`DELETE FROM search.search_documents WHERE notes_object_id=$1
		 AND source_kind='knowledge_chunk' AND index_version='knowledge_bm25_v1'`,
	}
	if !metadataSearch {
		queries = append(queries, `DELETE FROM search.search_documents WHERE notes_object_id=$1
		 AND source_kind='knowledge_object_metadata' AND index_version='knowledge_bm25_v1'`)
	}
	for _, query := range queries {
		result, err := tx.ExecContext(ctx, query, objectID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		changed = changed || count > 0
	}
	if changed {
		return MarkProjectionDirtyTx(ctx, tx, ProjectionDirtyInput{ObjectIDs: []string{objectID}})
	}
	return nil
}

func refreshQueuedPipelineTimingTx(ctx context.Context, tx *sql.Tx, run PipelineRun, object KnowledgeObject, plan CompiledPipelinePlan, snapshot json.RawMessage, now time.Time) (PipelineRun, error) {
	run.QuietWindowEligibleAt = nil
	if deadline, ok := pipelineQuietWindowEligibleAt(object, plan, now); ok {
		run.QuietWindowEligibleAt = &deadline
	}
	quiet := false
	parameters := make([][]any, 0, len(plan.Stages))
	for _, stage := range plan.Stages {
		if stage.StageKey == run.CurrentStageKey {
			quiet = stage.QuietWindow
		}
		parameters = append(parameters, []any{stage.StageKey, stage.QuietWindow})
	}
	for start := 0; start < len(parameters); {
		end := pipelineWriteBatchEnd(parameters, start)
		values, args := pipelineWriteValues(parameters[start:end], []string{"text", "boolean"})
		args = append(args, run.KnowledgePipelineRunID)
		if _, err := tx.ExecContext(ctx, `UPDATE knowledge.pipeline_stage_runs s
		 SET metadata=jsonb_set(s.metadata,'{quiet_window}',to_jsonb(p.quiet_window))
		 FROM (VALUES `+values+`) AS p(stage_key,quiet_window)
		 WHERE s.knowledge_pipeline_run_id=$`+strconv.Itoa(len(args))+` AND s.stage_key=p.stage_key
		 AND s.metadata->'quiet_window' IS DISTINCT FROM to_jsonb(p.quiet_window)`, args...); err != nil {
			return run, err
		}
		start = end
	}
	run.Status = waitingPipelineStatus(run.CurrentExecutionClass, quiet && run.QuietWindowEligibleAt != nil && run.QuietWindowEligibleAt.After(now))
	run.PlanSnapshot = snapshot
	_, err := tx.ExecContext(ctx, `UPDATE knowledge.pipeline_runs SET quiet_window_eligible_at=$2,status=$3,plan_snapshot=$4 WHERE knowledge_pipeline_run_id=$1`, run.KnowledgePipelineRunID, nullableTime(run.QuietWindowEligibleAt), run.Status, snapshot)
	return run, err
}
