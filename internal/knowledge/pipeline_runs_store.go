package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

func insertPipelineRunTx(ctx context.Context, tx *sql.Tx, run PipelineRun) (PipelineRun, error) {
	return scanPipelineRun(tx.QueryRowContext(ctx, `INSERT INTO knowledge.pipeline_runs (
		knowledge_pipeline_run_id, knowledge_object_id, knowledge_object_version_id,
		pipeline_definition_key, pipeline_definition_version, generation, status,
		current_stage_key, current_execution_class, priority, source_revision, source_hash,
		quiet_window_eligible_at, claimed_by_worker_run_id, claim_generation, claim_expires_at,
		last_error_code, last_error_message, warning_count, plan_snapshot, resource_totals,
		metadata, created_at, started_at, updated_at, completed_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26)
	RETURNING `+pipelineRunColumns(), run.KnowledgePipelineRunID, run.KnowledgeObjectID,
		run.KnowledgeObjectVersionID, run.PipelineDefinitionKey, run.PipelineDefinitionVersion,
		run.Generation, run.Status, run.CurrentStageKey, run.CurrentExecutionClass, run.Priority,
		run.SourceRevision, run.SourceHash, nullableTime(run.QuietWindowEligibleAt), run.ClaimedByWorkerRunID,
		run.ClaimGeneration, nullableTime(run.ClaimExpiresAt), run.LastErrorCode, run.LastErrorMessage,
		run.WarningCount, run.PlanSnapshot, run.ResourceTotals, run.Metadata, run.CreatedAt,
		nullableTime(run.StartedAt), run.UpdatedAt, nullableTime(run.CompletedAt)))
}

func insertPipelineStageRunTx(ctx context.Context, tx *sql.Tx, stage PipelineStageRun) (PipelineStageRun, error) {
	return scanPipelineStageRun(tx.QueryRowContext(ctx, `INSERT INTO knowledge.pipeline_stage_runs (
		knowledge_pipeline_stage_run_id, knowledge_pipeline_run_id, stage_key,
		stage_contract_version, ordinal, dependency_snapshot, execution_class, status,
		attempt_count, next_attempt_at, claimed_by_worker_run_id, claim_generation,
		input_hash, output_artifact_count, progress_completed, progress_total,
		resource_request, resource_usage, warning_json, error_json, metadata,
		created_at, started_at, updated_at, completed_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25)
	RETURNING `+pipelineStageRunColumns(), stage.KnowledgePipelineStageRunID, stage.KnowledgePipelineRunID,
		stage.StageKey, stage.StageContractVersion, stage.Ordinal, stage.DependencySnapshot,
		stage.ExecutionClass, stage.Status, stage.AttemptCount, nullableTime(stage.NextAttemptAt),
		stage.ClaimedByWorkerRunID, stage.ClaimGeneration, stage.InputHash, stage.OutputArtifactCount,
		stage.ProgressCompleted, stage.ProgressTotal, stage.ResourceRequest, stage.ResourceUsage,
		stage.Warnings, stage.Error, stage.Metadata, stage.CreatedAt, nullableTime(stage.StartedAt),
		stage.UpdatedAt, nullableTime(stage.CompletedAt)))
}

func findReusablePipelineRunTx(ctx context.Context, tx *sql.Tx, versionID, key, version string) (PipelineRun, bool, error) {
	run, err := scanPipelineRun(tx.QueryRowContext(ctx, `SELECT `+pipelineRunColumns()+`
		FROM knowledge.pipeline_runs WHERE knowledge_object_version_id=$1
		  AND pipeline_definition_key=$2 AND pipeline_definition_version=$3
		  AND status<>'stale'
		ORDER BY generation DESC LIMIT 1 FOR UPDATE`, versionID, key, version))
	if err == sql.ErrNoRows {
		return PipelineRun{}, false, nil
	}
	return run, err == nil, err
}

func (s Store) GetPipelineRun(ctx context.Context, ref string) (PipelineRun, error) {
	return scanPipelineRun(s.db.QueryRowContext(ctx, `SELECT `+pipelineRunColumns()+` FROM knowledge.pipeline_runs
		WHERE knowledge_pipeline_run_id=$1 OR knowledge_object_id=$1 ORDER BY generation DESC LIMIT 1`, ref))
}

func (s Store) ListPipelineStageRuns(ctx context.Context, runID string) ([]PipelineStageRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+pipelineStageRunColumns()+` FROM knowledge.pipeline_stage_runs
		WHERE knowledge_pipeline_run_id=$1 ORDER BY ordinal`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []PipelineStageRun
	for rows.Next() {
		stage, err := scanPipelineStageRun(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, stage)
	}
	return result, rows.Err()
}

func pipelineRunColumns() string {
	return `knowledge_pipeline_run_id, knowledge_object_id, knowledge_object_version_id,
	        pipeline_definition_key, pipeline_definition_version, generation, status,
	        current_stage_key, current_execution_class, priority, source_revision, source_hash,
	        quiet_window_eligible_at, claimed_by_worker_run_id, claim_generation, claim_expires_at,
	        last_error_code, last_error_message, warning_count, plan_snapshot, resource_totals,
	        metadata, created_at, started_at, updated_at, completed_at`
}

func pipelineRunColumnsQualified(alias string) string {
	columns := strings.Split(pipelineRunColumns(), ",")
	for index := range columns {
		columns[index] = strings.TrimSpace(alias) + "." + strings.TrimSpace(columns[index])
	}
	return strings.Join(columns, ",")
}

func pipelineStageRunColumns() string {
	return `knowledge_pipeline_stage_run_id, knowledge_pipeline_run_id, stage_key,
	        stage_contract_version, ordinal, dependency_snapshot, execution_class, status,
	        attempt_count, next_attempt_at, claimed_by_worker_run_id, claim_generation,
	        input_hash, output_artifact_count, progress_completed, progress_total,
	        resource_request, resource_usage, warning_json, error_json, metadata,
	        created_at, started_at, updated_at, completed_at`
}

func scanPipelineRun(scanner interface{ Scan(dest ...any) error }) (PipelineRun, error) {
	var run PipelineRun
	var quiet, claimExpiry, started, completed sql.NullTime
	var plan, totals, metadata []byte
	err := scanner.Scan(&run.KnowledgePipelineRunID, &run.KnowledgeObjectID, &run.KnowledgeObjectVersionID,
		&run.PipelineDefinitionKey, &run.PipelineDefinitionVersion, &run.Generation, &run.Status,
		&run.CurrentStageKey, &run.CurrentExecutionClass, &run.Priority, &run.SourceRevision,
		&run.SourceHash, &quiet, &run.ClaimedByWorkerRunID, &run.ClaimGeneration, &claimExpiry,
		&run.LastErrorCode, &run.LastErrorMessage, &run.WarningCount, &plan, &totals, &metadata,
		&run.CreatedAt, &started, &run.UpdatedAt, &completed)
	if err != nil {
		return PipelineRun{}, err
	}
	run.QuietWindowEligibleAt, run.ClaimExpiresAt = nullTimePtr(quiet), nullTimePtr(claimExpiry)
	run.StartedAt, run.CompletedAt = nullTimePtr(started), nullTimePtr(completed)
	run.PlanSnapshot, run.ResourceTotals, run.Metadata = jsonObjectOrEmpty(plan), jsonObjectOrEmpty(totals), jsonObjectOrEmpty(metadata)
	return run, nil
}

func scanPipelineStageRun(scanner interface{ Scan(dest ...any) error }) (PipelineStageRun, error) {
	var stage PipelineStageRun
	var next, started, completed sql.NullTime
	var deps, request, usage, warnings, failure, metadata []byte
	err := scanner.Scan(&stage.KnowledgePipelineStageRunID, &stage.KnowledgePipelineRunID,
		&stage.StageKey, &stage.StageContractVersion, &stage.Ordinal, &deps, &stage.ExecutionClass,
		&stage.Status, &stage.AttemptCount, &next, &stage.ClaimedByWorkerRunID, &stage.ClaimGeneration,
		&stage.InputHash, &stage.OutputArtifactCount, &stage.ProgressCompleted, &stage.ProgressTotal,
		&request, &usage, &warnings, &failure, &metadata, &stage.CreatedAt, &started, &stage.UpdatedAt, &completed)
	if err != nil {
		return PipelineStageRun{}, err
	}
	stage.NextAttemptAt, stage.StartedAt, stage.CompletedAt = nullTimePtr(next), nullTimePtr(started), nullTimePtr(completed)
	stage.DependencySnapshot = json.RawMessage(deps)
	stage.ResourceRequest, stage.ResourceUsage, stage.Warnings, stage.Error, stage.Metadata = jsonObjectOrEmpty(request), jsonObjectOrEmpty(usage), json.RawMessage(warnings), jsonObjectOrEmpty(failure), jsonObjectOrEmpty(metadata)
	if len(stage.DependencySnapshot) == 0 {
		stage.DependencySnapshot = json.RawMessage(`[]`)
	}
	if len(stage.Warnings) == 0 {
		stage.Warnings = json.RawMessage(`[]`)
	}
	return stage, nil
}
