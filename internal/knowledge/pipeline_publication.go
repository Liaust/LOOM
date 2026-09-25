package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
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
	var lexical, semantic sql.NullTime
	err := s.store.db.QueryRowContext(ctx, `SELECT source_revision,source_hash,metadata,COALESCE(lexical_version_id,''),COALESCE(semantic_version_id,''),lexical_published_at,semantic_published_at
	 FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1`, run.KnowledgeObjectID).Scan(&result.LatestSourceRevision, &result.LatestSourceHash, &metadata, &result.LexicalVersionID, &result.SemanticVersionID, &lexical, &semantic)
	if err != nil {
		return result, err
	}
	if len(run.SourceSnapshot) > 0 {
		result.SelectedSourceRevision = run.SourceRevision
	}
	result.Pending = result.LatestSourceRevision != run.SourceRevision || result.LatestSourceHash != run.SourceHash
	object := KnowledgeObject{Metadata: json.RawMessage(metadata)}
	result.PendingSince = objectRefreshClock(object).PendingSince
	result.LexicalPublishedAt, result.SemanticPublishedAt = nullTimePtr(lexical), nullTimePtr(semantic)
	result.RequestedPolicy, err = knowledgeSourcePolicy(object)
	return result, err
}

func withdrawIncompatiblePublicationsTx(ctx context.Context, tx *sql.Tx, objectID string, plan CompiledPipelinePlan) error {
	for _, stage := range plan.Stages {
		if stage.Selected {
			continue
		}
		switch stage.StageKey {
		case FilePipelineStageEmbedding, FilePipelineStagePDFOCR, FilePipelineStageImageOCR, FilePipelineStageImageDescription:
		default:
			continue
		}
		// Withdraw disabled derivations without waiting for a replacement.
		lexical := stage.StageKey != FilePipelineStageEmbedding
		_, err := tx.ExecContext(ctx, `UPDATE knowledge.knowledge_objects o SET
		 semantic_version_id=CASE WHEN EXISTS (SELECT 1 FROM knowledge.pipeline_runs r,jsonb_array_elements(r.plan_snapshot->'stages') s
		 WHERE r.knowledge_object_version_id=o.semantic_version_id AND s->>'stage_key'=$2 AND s->>'selected'='true') THEN NULL ELSE semantic_version_id END,
		 lexical_version_id=CASE WHEN $3 AND EXISTS (SELECT 1 FROM knowledge.pipeline_runs r,jsonb_array_elements(r.plan_snapshot->'stages') s
		 WHERE r.knowledge_object_version_id=o.lexical_version_id AND s->>'stage_key'=$2 AND s->>'selected'='true') THEN NULL ELSE lexical_version_id END
		 WHERE o.knowledge_object_id=$1`, objectID, stage.StageKey, lexical)
		if err != nil {
			return err
		}
	}
	return nil
}

func refreshQueuedPipelineTimingTx(ctx context.Context, tx *sql.Tx, run PipelineRun, object KnowledgeObject, plan CompiledPipelinePlan, snapshot json.RawMessage, now time.Time) (PipelineRun, error) {
	run.QuietWindowEligibleAt = nil
	if deadline, ok := pipelineQuietWindowEligibleAt(object, plan, now); ok {
		run.QuietWindowEligibleAt = &deadline
	}
	quiet := false
	for _, stage := range plan.Stages {
		if stage.StageKey == run.CurrentStageKey {
			quiet = stage.QuietWindow
		}
		if _, err := tx.ExecContext(ctx, `UPDATE knowledge.pipeline_stage_runs SET metadata=jsonb_set(metadata,'{quiet_window}',to_jsonb($3::boolean))
		 WHERE knowledge_pipeline_run_id=$1 AND stage_key=$2`, run.KnowledgePipelineRunID, stage.StageKey, stage.QuietWindow); err != nil {
			return run, err
		}
	}
	run.Status = waitingPipelineStatus(run.CurrentExecutionClass, quiet && run.QuietWindowEligibleAt != nil && run.QuietWindowEligibleAt.After(now))
	run.PlanSnapshot = snapshot
	_, err := tx.ExecContext(ctx, `UPDATE knowledge.pipeline_runs SET quiet_window_eligible_at=$2,status=$3,plan_snapshot=$4 WHERE knowledge_pipeline_run_id=$1`, run.KnowledgePipelineRunID, nullableTime(run.QuietWindowEligibleAt), run.Status, snapshot)
	return run, err
}
