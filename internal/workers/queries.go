package workers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const defaultListLimit = 50
const maxListLimit = 200

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s Service) ListWorkers(ctx context.Context, filter WorkerFilter) ([]WorkerListItem, error) {
	if s.DB == nil {
		return nil, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	filter.Limit = normalizeLimit(filter.Limit)

	query := `
		SELECT
			wi.worker_instance_id, wi.worker_key, wi.worker_kind, wi.display_name,
			wi.locality, wi.lifecycle_status, wi.enabled, wi.paused,
			COALESCE(wh.health_status, 'unknown'),
			COALESCE(wh.severity, 'info'),
			wi.last_success_at, wi.last_failure_at, wi.current_run_id,
			COALESCE(wh.attention_required, false)
		FROM workers.worker_instances wi
		LEFT JOIN workers.worker_health wh ON wh.worker_instance_id = wi.worker_instance_id
		LEFT JOIN nodes.nodes host_node ON host_node.node_id = wi.host_node_id
		LEFT JOIN nodes.nodes owner_node ON owner_node.node_id = wi.owner_node_id
		WHERE true
	`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if strings.TrimSpace(filter.Kind) != "" {
		add("wi.worker_kind =", strings.TrimSpace(filter.Kind))
	}
	if strings.TrimSpace(filter.Status) != "" {
		add("wi.lifecycle_status =", strings.TrimSpace(filter.Status))
	}
	if strings.TrimSpace(filter.Health) != "" {
		add("COALESCE(wh.health_status, 'unknown') =", strings.TrimSpace(filter.Health))
	}
	if strings.TrimSpace(filter.NodeRef) != "" {
		args = append(args, strings.TrimSpace(filter.NodeRef))
		query += fmt.Sprintf(" AND (wi.host_node_id = $%d OR host_node.node_key = $%d OR wi.owner_node_id = $%d OR owner_node.node_key = $%d)", len(args), len(args), len(args), len(args))
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY wi.worker_key ASC LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var workers []WorkerListItem
	for rows.Next() {
		item, err := scanWorkerListItem(rows)
		if err != nil {
			return nil, err
		}
		workers = append(workers, item)
	}
	return workers, rows.Err()
}

func (s Service) InspectWorker(ctx context.Context, ref string) (WorkerDetail, error) {
	if s.DB == nil {
		return WorkerDetail{}, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	instance, err := resolveWorkerInstance(ctx, s.DB, ref)
	if err != nil {
		return WorkerDetail{}, err
	}
	kind, err := getWorkerKind(ctx, s.DB, instance.WorkerKind)
	if err != nil {
		return WorkerDetail{}, err
	}
	health, err := getWorkerHealth(ctx, s.DB, instance.WorkerInstanceID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return WorkerDetail{}, err
	}
	currentRun, err := getRunByID(ctx, s.DB, instance.CurrentRunID)
	if err != nil {
		return WorkerDetail{}, err
	}
	lastRun, err := getRunByID(ctx, s.DB, instance.LastRunID)
	if err != nil {
		return WorkerDetail{}, err
	}
	latestCheckpoint, err := latestCheckpoint(ctx, s.DB, instance.WorkerInstanceID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return WorkerDetail{}, err
	}
	recentRuns, err := listRunsForInstance(ctx, s.DB, instance.WorkerInstanceID, RunFilter{Limit: 5})
	if err != nil {
		return WorkerDetail{}, err
	}

	return WorkerDetail{
		Instance:         instance,
		Kind:             kind,
		Health:           health,
		CurrentRun:       currentRun,
		LastRun:          lastRun,
		LatestCheckpoint: latestCheckpoint,
		RecentRuns:       recentRuns,
	}, nil
}

func (s Service) ListRuns(ctx context.Context, workerRef string, filter RunFilter) ([]WorkerRun, error) {
	if s.DB == nil {
		return nil, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	instance, err := resolveWorkerInstance(ctx, s.DB, workerRef)
	if err != nil {
		return nil, err
	}
	return listRunsForInstance(ctx, s.DB, instance.WorkerInstanceID, filter)
}

func (s Service) ListDueWorkers(ctx context.Context, now time.Time, limit int) ([]WorkerInstance, error) {
	if s.DB == nil {
		return nil, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	limit = normalizeLimit(limit)
	rows, err := s.DB.QueryContext(ctx, workerInstanceSelectSQL()+`
		WHERE locality = $1
		  AND lifecycle_status IN ($2, $3, $4, $5)
		  AND enabled = true
		  AND paused = false
		  AND current_run_id IS NULL
		  AND (
		      (tick_policy_json->>'mode' = $6 AND (next_run_after IS NULL OR next_run_after <= $8))
		      OR
		      (tick_policy_json->>'mode' = $7 AND next_run_after IS NOT NULL AND next_run_after <= $8)
		  )
		ORDER BY COALESCE(next_run_after, '-infinity'::timestamptz), worker_key ASC
		LIMIT $9
	`, LocalityMainOwned, LifecycleRegistered, LifecycleActive, LifecycleDegraded, LifecycleFailed, TickModeInterval, TickModeDailyLocal, now.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []WorkerInstance
	for rows.Next() {
		instance, err := scanWorkerInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, instance)
	}
	return out, rows.Err()
}

func (s Service) advanceDailyScheduleAfterFailedAttempt(ctx context.Context, selected WorkerInstance, policy TickPolicy, now time.Time) (bool, error) {
	if s.DB == nil {
		return false, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	if policy.Mode != TickModeDailyLocal || selected.NextRunAfter == nil {
		return false, nil
	}
	nextRunAfter := policy.NextAfter(now.UTC())
	if nextRunAfter == nil {
		return false, fmt.Errorf("%w: daily_local policy did not derive next_run_after", ErrInvalid)
	}
	result, err := s.DB.ExecContext(ctx, `
		UPDATE workers.worker_instances
		SET next_run_after = $5,
		    updated_at = now()
		WHERE worker_instance_id = $1
		  AND tick_policy_json = $2::jsonb
		  AND next_run_after = $3
		  AND next_run_after <= $4
		  AND current_run_id IS NULL
	`, selected.WorkerInstanceID, string(selected.TickPolicyJSON), selected.NextRunAfter.UTC(), now.UTC(), nextRunAfter.UTC())
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

func resolveWorkerInstance(ctx context.Context, q queryer, ref string) (WorkerInstance, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return WorkerInstance{}, fmt.Errorf("%w: worker ref is required", ErrInvalid)
	}

	instance, err := scanWorkerInstance(q.QueryRowContext(ctx, workerInstanceSelectSQL()+`
		WHERE worker_instance_id = $1 OR worker_key = $1
	`, ref))
	if err == nil {
		return instance, nil
	}
	if err != sql.ErrNoRows {
		return WorkerInstance{}, err
	}

	rows, err := q.QueryContext(ctx, workerInstanceSelectSQL()+`
		WHERE worker_kind = $1
		ORDER BY worker_key ASC
		LIMIT 2
	`, ref)
	if err != nil {
		return WorkerInstance{}, err
	}
	defer rows.Close()

	var matches []WorkerInstance
	for rows.Next() {
		match, err := scanWorkerInstance(rows)
		if err != nil {
			return WorkerInstance{}, err
		}
		matches = append(matches, match)
	}
	if err := rows.Err(); err != nil {
		return WorkerInstance{}, err
	}
	switch len(matches) {
	case 0:
		return WorkerInstance{}, fmt.Errorf("%w: worker %q was not found", ErrNotFound, ref)
	case 1:
		return matches[0], nil
	default:
		return WorkerInstance{}, fmt.Errorf("%w: worker ref %q matches multiple instances", ErrAmbiguous, ref)
	}
}

func getWorkerKind(ctx context.Context, q queryer, kind string) (WorkerKind, error) {
	return scanWorkerKind(q.QueryRowContext(ctx, `
		SELECT worker_kind, display_name, description, runtime_owner, runtime_package,
		       status, array_to_json(supported_localities)::jsonb,
		       may_create_jobs, may_call_capabilities, may_touch_filesystem,
		       may_store_raw_payloads, default_tick_policy_json,
		       default_concurrency_policy_json, default_retry_policy_json,
		       default_timeout_policy_json, default_resource_limits_json,
		       config_schema_json, checkpoint_schema_json, result_schema_json,
		       registered_by_actor_id, created_at, updated_at, metadata
		FROM workers.worker_kinds
		WHERE worker_kind = $1
	`, kind))
}

func getWorkerHealth(ctx context.Context, q queryer, instanceID string) (*WorkerHealth, error) {
	health, err := scanWorkerHealth(q.QueryRowContext(ctx, workerHealthSelectSQL()+`
		WHERE worker_instance_id = $1
	`, instanceID))
	if err != nil {
		return nil, err
	}
	return &health, nil
}

func getRunByID(ctx context.Context, q queryer, runID *string) (*WorkerRun, error) {
	if runID == nil || strings.TrimSpace(*runID) == "" {
		return nil, nil
	}
	run, err := scanWorkerRun(q.QueryRowContext(ctx, workerRunSelectSQL()+`
		WHERE worker_run_id = $1
	`, strings.TrimSpace(*runID)))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func listRunsForInstance(ctx context.Context, q queryer, instanceID string, filter RunFilter) ([]WorkerRun, error) {
	filter.Limit = normalizeLimit(filter.Limit)
	query := workerRunSelectSQL() + ` WHERE worker_instance_id = $1`
	args := []any{instanceID}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if strings.TrimSpace(filter.Status) != "" {
		add("run_status =", strings.TrimSpace(filter.Status))
	}
	if strings.TrimSpace(filter.Trigger) != "" {
		add("trigger_kind =", strings.TrimSpace(filter.Trigger))
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY started_at DESC LIMIT $%d", len(args))

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []WorkerRun
	for rows.Next() {
		run, err := scanWorkerRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func latestCheckpoint(ctx context.Context, q queryer, instanceID string) (*WorkerCheckpoint, error) {
	checkpoint, err := scanWorkerCheckpoint(q.QueryRowContext(ctx, workerCheckpointSelectSQL()+`
		WHERE worker_instance_id = $1
		ORDER BY updated_at DESC
		LIMIT 1
	`, instanceID))
	if err != nil {
		return nil, err
	}
	return &checkpoint, nil
}

func loadCheckpoints(ctx context.Context, q queryer, instanceID string) (map[string]WorkerCheckpoint, error) {
	rows, err := q.QueryContext(ctx, workerCheckpointSelectSQL()+`
		WHERE worker_instance_id = $1
	`, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	checkpoints := map[string]WorkerCheckpoint{}
	for rows.Next() {
		checkpoint, err := scanWorkerCheckpoint(rows)
		if err != nil {
			return nil, err
		}
		checkpoints[checkpoint.CheckpointKey] = checkpoint
	}
	return checkpoints, rows.Err()
}

func listCheckpoints(ctx context.Context, q queryer, instanceID string) ([]WorkerCheckpoint, error) {
	rows, err := q.QueryContext(ctx, workerCheckpointSelectSQL()+`
		WHERE worker_instance_id = $1
		ORDER BY checkpoint_key ASC
	`, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var checkpoints []WorkerCheckpoint
	for rows.Next() {
		checkpoint, err := scanWorkerCheckpoint(rows)
		if err != nil {
			return nil, err
		}
		checkpoints = append(checkpoints, checkpoint)
	}
	return checkpoints, rows.Err()
}

func workerInstanceSelectSQL() string {
	return `
		SELECT worker_instance_id, worker_key, worker_kind, display_name, description,
		       owner_node_id, host_node_id, scope_id, project_id, locality,
		       lifecycle_status, enabled, paused, config_json, tick_policy_json,
		       concurrency_policy_json, retry_policy_json, timeout_policy_json,
		       resource_limits_json, visibility_json, current_run_id, last_run_id,
		       last_success_at, last_failure_at, last_heartbeat_at, next_run_after,
		       backoff_until, consecutive_failures, created_at, updated_at, metadata
		FROM workers.worker_instances
	`
}

func workerRunSelectSQL() string {
	return `
		SELECT worker_run_id, worker_instance_id, worker_kind, run_status,
		       trigger_kind, trigger_ref, lease_id, lease_generation, correlation_id,
		       idempotency_key, started_at, finished_at, deadline_at,
		       result_summary_json, counters_json, resource_usage_json, error_json,
		       retryable, created_at, updated_at, metadata
		FROM workers.worker_runs
	`
}

func workerCheckpointSelectSQL() string {
	return `
		SELECT worker_checkpoint_id, worker_instance_id, checkpoint_key,
		       checkpoint_json, schema_version, updated_by_run_id, updated_at, metadata
		FROM workers.worker_checkpoints
	`
}

func workerHealthSelectSQL() string {
	return `
		SELECT worker_health_id, worker_instance_id, health_status, severity, summary,
		       attention_required, last_success_at, last_failure_at, current_run_id,
		       queue_depth, consecutive_failures, computed_at, details_json, metadata
		FROM workers.worker_health
	`
}

type scanner interface {
	Scan(dest ...any) error
}

func scanWorkerListItem(scanner scanner) (WorkerListItem, error) {
	var item WorkerListItem
	var lastSuccessAt sql.NullTime
	var lastFailureAt sql.NullTime
	var currentRunID sql.NullString
	if err := scanner.Scan(
		&item.WorkerInstanceID,
		&item.WorkerKey,
		&item.WorkerKind,
		&item.DisplayName,
		&item.Locality,
		&item.LifecycleStatus,
		&item.Enabled,
		&item.Paused,
		&item.HealthStatus,
		&item.Severity,
		&lastSuccessAt,
		&lastFailureAt,
		&currentRunID,
		&item.AttentionRequired,
	); err != nil {
		return WorkerListItem{}, err
	}
	item.LastSuccessAt = timePtr(lastSuccessAt)
	item.LastFailureAt = timePtr(lastFailureAt)
	item.CurrentRunID = stringPtr(currentRunID)
	return item, nil
}

func scanWorkerKind(scanner scanner) (WorkerKind, error) {
	var kind WorkerKind
	var localitiesRaw []byte
	var registeredBy sql.NullString
	var defaultTick, defaultConcurrency, defaultRetry, defaultTimeout, defaultResource []byte
	var configSchema, checkpointSchema, resultSchema, metadata []byte
	if err := scanner.Scan(
		&kind.WorkerKind,
		&kind.DisplayName,
		&kind.Description,
		&kind.RuntimeOwner,
		&kind.RuntimePackage,
		&kind.Status,
		&localitiesRaw,
		&kind.MayCreateJobs,
		&kind.MayCallCapabilities,
		&kind.MayTouchFilesystem,
		&kind.MayStoreRawPayloads,
		&defaultTick,
		&defaultConcurrency,
		&defaultRetry,
		&defaultTimeout,
		&defaultResource,
		&configSchema,
		&checkpointSchema,
		&resultSchema,
		&registeredBy,
		&kind.CreatedAt,
		&kind.UpdatedAt,
		&metadata,
	); err != nil {
		return WorkerKind{}, err
	}
	if len(localitiesRaw) > 0 {
		if err := json.Unmarshal(localitiesRaw, &kind.SupportedLocalities); err != nil {
			return WorkerKind{}, err
		}
	}
	kind.RegisteredByActorID = stringPtr(registeredBy)
	kind.DefaultTickPolicyJSON = rawMessage(defaultTick)
	kind.DefaultConcurrencyPolicyJSON = rawMessage(defaultConcurrency)
	kind.DefaultRetryPolicyJSON = rawMessage(defaultRetry)
	kind.DefaultTimeoutPolicyJSON = rawMessage(defaultTimeout)
	kind.DefaultResourceLimitsJSON = rawMessage(defaultResource)
	kind.ConfigSchemaJSON = rawMessage(configSchema)
	kind.CheckpointSchemaJSON = rawMessage(checkpointSchema)
	kind.ResultSchemaJSON = rawMessage(resultSchema)
	kind.Metadata = rawMessage(metadata)
	return kind, nil
}

func scanWorkerInstance(scanner scanner) (WorkerInstance, error) {
	var instance WorkerInstance
	var scopeID, projectID, currentRunID, lastRunID sql.NullString
	var lastSuccessAt, lastFailureAt, lastHeartbeatAt, nextRunAfter, backoffUntil sql.NullTime
	var config, tick, concurrency, retry, timeout, resource, visibility, metadata []byte
	if err := scanner.Scan(
		&instance.WorkerInstanceID,
		&instance.WorkerKey,
		&instance.WorkerKind,
		&instance.DisplayName,
		&instance.Description,
		&instance.OwnerNodeID,
		&instance.HostNodeID,
		&scopeID,
		&projectID,
		&instance.Locality,
		&instance.LifecycleStatus,
		&instance.Enabled,
		&instance.Paused,
		&config,
		&tick,
		&concurrency,
		&retry,
		&timeout,
		&resource,
		&visibility,
		&currentRunID,
		&lastRunID,
		&lastSuccessAt,
		&lastFailureAt,
		&lastHeartbeatAt,
		&nextRunAfter,
		&backoffUntil,
		&instance.ConsecutiveFails,
		&instance.CreatedAt,
		&instance.UpdatedAt,
		&metadata,
	); err != nil {
		return WorkerInstance{}, err
	}
	instance.ScopeID = stringPtr(scopeID)
	instance.ProjectID = stringPtr(projectID)
	instance.CurrentRunID = stringPtr(currentRunID)
	instance.LastRunID = stringPtr(lastRunID)
	instance.LastSuccessAt = timePtr(lastSuccessAt)
	instance.LastFailureAt = timePtr(lastFailureAt)
	instance.LastHeartbeatAt = timePtr(lastHeartbeatAt)
	instance.NextRunAfter = timePtr(nextRunAfter)
	instance.BackoffUntil = timePtr(backoffUntil)
	instance.ConfigJSON = rawMessage(config)
	instance.TickPolicyJSON = rawMessage(tick)
	instance.ConcurrencyJSON = rawMessage(concurrency)
	instance.RetryPolicyJSON = rawMessage(retry)
	instance.TimeoutPolicyJSON = rawMessage(timeout)
	instance.ResourceLimitsJSON = rawMessage(resource)
	instance.VisibilityJSON = rawMessage(visibility)
	instance.Metadata = rawMessage(metadata)
	return instance, nil
}

func scanWorkerRun(scanner scanner) (WorkerRun, error) {
	var run WorkerRun
	var leaseID sql.NullString
	var leaseGeneration sql.NullInt64
	var finishedAt, deadlineAt sql.NullTime
	var resultSummary, counters, resourceUsage, errorRaw, metadata []byte
	if err := scanner.Scan(
		&run.WorkerRunID,
		&run.WorkerInstanceID,
		&run.WorkerKind,
		&run.RunStatus,
		&run.TriggerKind,
		&run.TriggerRef,
		&leaseID,
		&leaseGeneration,
		&run.CorrelationID,
		&run.IdempotencyKey,
		&run.StartedAt,
		&finishedAt,
		&deadlineAt,
		&resultSummary,
		&counters,
		&resourceUsage,
		&errorRaw,
		&run.Retryable,
		&run.CreatedAt,
		&run.UpdatedAt,
		&metadata,
	); err != nil {
		return WorkerRun{}, err
	}
	run.LeaseID = stringPtr(leaseID)
	run.LeaseGeneration = int64Ptr(leaseGeneration)
	run.FinishedAt = timePtr(finishedAt)
	run.DeadlineAt = timePtr(deadlineAt)
	run.ResultSummaryJSON = rawMessage(resultSummary)
	run.CountersJSON = rawMessage(counters)
	run.ResourceUsageJSON = rawMessage(resourceUsage)
	run.ErrorJSON = rawMessage(errorRaw)
	run.Metadata = rawMessage(metadata)
	return run, nil
}

func scanWorkerCheckpoint(scanner scanner) (WorkerCheckpoint, error) {
	var checkpoint WorkerCheckpoint
	var updatedByRunID sql.NullString
	var checkpointJSON, metadata []byte
	if err := scanner.Scan(
		&checkpoint.WorkerCheckpointID,
		&checkpoint.WorkerInstanceID,
		&checkpoint.CheckpointKey,
		&checkpointJSON,
		&checkpoint.SchemaVersion,
		&updatedByRunID,
		&checkpoint.UpdatedAt,
		&metadata,
	); err != nil {
		return WorkerCheckpoint{}, err
	}
	checkpoint.UpdatedByRunID = stringPtr(updatedByRunID)
	checkpoint.CheckpointJSON = rawMessage(checkpointJSON)
	checkpoint.Metadata = rawMessage(metadata)
	return checkpoint, nil
}

func scanWorkerHealth(scanner scanner) (WorkerHealth, error) {
	var health WorkerHealth
	var lastSuccessAt, lastFailureAt sql.NullTime
	var currentRunID sql.NullString
	var details, metadata []byte
	if err := scanner.Scan(
		&health.WorkerHealthID,
		&health.WorkerInstanceID,
		&health.HealthStatus,
		&health.Severity,
		&health.Summary,
		&health.AttentionRequired,
		&lastSuccessAt,
		&lastFailureAt,
		&currentRunID,
		&health.QueueDepth,
		&health.ConsecutiveFails,
		&health.ComputedAt,
		&details,
		&metadata,
	); err != nil {
		return WorkerHealth{}, err
	}
	health.LastSuccessAt = timePtr(lastSuccessAt)
	health.LastFailureAt = timePtr(lastFailureAt)
	health.CurrentRunID = stringPtr(currentRunID)
	health.DetailsJSON = rawMessage(details)
	health.Metadata = rawMessage(metadata)
	return health, nil
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return defaultListLimit
	}
	if limit > maxListLimit {
		return maxListLimit
	}
	return limit
}

func rawMessage(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(raw)
}

func stringPtr(value sql.NullString) *string {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	out := value.String
	return &out
}

func timePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	out := value.Time
	return &out
}

func int64Ptr(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	out := value.Int64
	return &out
}
