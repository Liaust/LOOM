package workers

import (
	"context"
	"database/sql"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
)

func createRunOnceControl(ctx context.Context, tx *sql.Tx, req requestctx.Context, instance WorkerInstance, input RunOnceInput) (WorkerControl, error) {
	inputJSON, err := marshalJSONObject(map[string]any{
		"schema_version":   "worker_control.run_once.v0.2",
		"reason":           input.Reason,
		"idempotency_key":  input.IdempotencyKey,
		"request_metadata": rawJSONObjectOrDefault(input.Metadata),
	}, "input_json")
	if err != nil {
		return WorkerControl{}, err
	}
	return scanWorkerControl(tx.QueryRowContext(ctx, `
		INSERT INTO workers.worker_controls (
			worker_control_id, worker_instance_id, control_kind, control_status,
			requested_by_actor_id, input_json, metadata
		)
		VALUES ($1, $2, 'run_once', 'requested', $3, $4,
		        '{"schema_version":"worker_control.metadata.v0.2"}'::jsonb)
		RETURNING worker_control_id, worker_instance_id, control_kind, control_status,
		          requested_by_actor_id, requested_at, applied_by_run_id, applied_at,
		          expires_at, input_json, result_json, error_json, metadata
	`, ids.NewWorkerControlID(), instance.WorkerInstanceID, req.ActorID, []byte(inputJSON)))
}

func markControlApplied(ctx context.Context, tx *sql.Tx, control WorkerControl, run WorkerRun) (*WorkerControl, error) {
	resultJSON, err := marshalJSONObject(map[string]any{
		"schema_version": "worker_control.result.v0.2",
		"worker_run_id":  run.WorkerRunID,
		"run_status":     run.RunStatus,
	}, "result_json")
	if err != nil {
		return nil, err
	}
	updated, err := scanWorkerControl(tx.QueryRowContext(ctx, `
		UPDATE workers.worker_controls
		SET control_status = 'applied',
		    applied_by_run_id = $2,
		    applied_at = now(),
		    result_json = $3
		WHERE worker_control_id = $1
		RETURNING worker_control_id, worker_instance_id, control_kind, control_status,
		          requested_by_actor_id, requested_at, applied_by_run_id, applied_at,
		          expires_at, input_json, result_json, error_json, metadata
	`, control.WorkerControlID, run.WorkerRunID, []byte(resultJSON)))
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

func markControlFailed(ctx context.Context, tx *sql.Tx, control WorkerControl, run WorkerRun, runErr error) (*WorkerControl, error) {
	updated, err := scanWorkerControl(tx.QueryRowContext(ctx, `
		UPDATE workers.worker_controls
		SET control_status = 'failed',
		    applied_by_run_id = $2,
		    applied_at = now(),
		    error_json = $3
		WHERE worker_control_id = $1
		RETURNING worker_control_id, worker_instance_id, control_kind, control_status,
		          requested_by_actor_id, requested_at, applied_by_run_id, applied_at,
		          expires_at, input_json, result_json, error_json, metadata
	`, control.WorkerControlID, run.WorkerRunID, []byte(errorJSON(runErr))))
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

func workerControlSelectSQL() string {
	return `
		SELECT worker_control_id, worker_instance_id, control_kind, control_status,
		       requested_by_actor_id, requested_at, applied_by_run_id, applied_at,
		       expires_at, input_json, result_json, error_json, metadata
		FROM workers.worker_controls
	`
}

func scanWorkerControl(scanner scanner) (WorkerControl, error) {
	var control WorkerControl
	var appliedByRunID sql.NullString
	var appliedAt, expiresAt sql.NullTime
	var inputJSON, resultJSON, errorRaw, metadata []byte
	if err := scanner.Scan(
		&control.WorkerControlID,
		&control.WorkerInstanceID,
		&control.ControlKind,
		&control.ControlStatus,
		&control.RequestedByActorID,
		&control.RequestedAt,
		&appliedByRunID,
		&appliedAt,
		&expiresAt,
		&inputJSON,
		&resultJSON,
		&errorRaw,
		&metadata,
	); err != nil {
		return WorkerControl{}, err
	}
	control.AppliedByRunID = stringPtr(appliedByRunID)
	control.AppliedAt = timePtr(appliedAt)
	control.ExpiresAt = timePtr(expiresAt)
	control.InputJSON = rawMessage(inputJSON)
	control.ResultJSON = rawMessage(resultJSON)
	control.ErrorJSON = rawMessage(errorRaw)
	control.Metadata = rawMessage(metadata)
	return control, nil
}
