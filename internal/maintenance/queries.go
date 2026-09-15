package maintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/ids"
)

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type queryExecer interface {
	queryer
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type scanner interface {
	Scan(...any) error
}

func listMaintenanceWorkers(ctx context.Context, q queryer) ([]WorkerStatus, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT
			wi.worker_instance_id,
			wi.worker_key,
			wi.worker_kind,
			COALESCE(wh.health_status, 'unknown') AS health_status,
			COALESCE(wh.severity, 'info') AS severity,
			COALESCE(wh.attention_required, false) AS attention_required,
			wh.last_success_at,
			wh.last_failure_at,
			COALESCE(wh.consecutive_failures, 0) AS consecutive_failures,
			COUNT(mf.maintenance_finding_id) FILTER (WHERE mf.status = 'open') AS open_findings
		FROM workers.worker_instances wi
		LEFT JOIN workers.worker_health wh ON wh.worker_instance_id = wi.worker_instance_id
		LEFT JOIN maintenance.findings mf ON mf.worker_instance_id = wi.worker_instance_id
		WHERE wi.worker_kind IN ($1, $2, $3, $4, $5)
		GROUP BY wi.worker_instance_id, wi.worker_key, wi.worker_kind, wh.health_status,
		         wh.severity, wh.attention_required, wh.last_success_at, wh.last_failure_at,
		         wh.consecutive_failures
		ORDER BY wi.worker_key
	`, WorkerKindPolicyExpiry, WorkerKindRealtimeExpiry, WorkerKindDBMaintenance, WorkerKindMainBackup, WorkerKindObjectStore)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []WorkerStatus
	for rows.Next() {
		var item WorkerStatus
		var lastSuccess sql.NullTime
		var lastFailure sql.NullTime
		if err := rows.Scan(
			&item.WorkerInstanceID,
			&item.WorkerKey,
			&item.WorkerKind,
			&item.HealthStatus,
			&item.Severity,
			&item.AttentionRequired,
			&lastSuccess,
			&lastFailure,
			&item.ConsecutiveFails,
			&item.OpenFindings,
		); err != nil {
			return nil, err
		}
		item.LastSuccessAt = nullTimePtr(lastSuccess)
		item.LastFailureAt = nullTimePtr(lastFailure)
		out = append(out, item)
	}
	return out, rows.Err()
}

func findingSummary(ctx context.Context, q queryer) (FindingSummary, error) {
	var summary FindingSummary
	err := q.QueryRowContext(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE status = 'open') AS open_count,
			COUNT(*) FILTER (WHERE status = 'open' AND severity = 'critical') AS critical_count,
			COUNT(*) FILTER (WHERE status = 'open' AND severity = 'error') AS error_count,
			COUNT(*) FILTER (WHERE status = 'open' AND severity = 'warning') AS warning_count,
			COUNT(*) FILTER (WHERE status = 'open' AND severity = 'info') AS info_count
		FROM maintenance.findings
	`).Scan(
		&summary.Open,
		&summary.Critical,
		&summary.Error,
		&summary.Warning,
		&summary.Info,
	)
	return summary, err
}

func findingSummaryForKinds(ctx context.Context, q queryer, kinds ...string) (FindingSummary, error) {
	if len(kinds) == 0 {
		return findingSummary(ctx, q)
	}

	args := make([]any, 0, len(kinds))
	placeholders := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		args = append(args, strings.TrimSpace(kind))
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
	}

	var summary FindingSummary
	err := q.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT
			COUNT(*) FILTER (WHERE mf.status = 'open') AS open_count,
			COUNT(*) FILTER (WHERE mf.status = 'open' AND mf.severity = 'critical') AS critical_count,
			COUNT(*) FILTER (WHERE mf.status = 'open' AND mf.severity = 'error') AS error_count,
			COUNT(*) FILTER (WHERE mf.status = 'open' AND mf.severity = 'warning') AS warning_count,
			COUNT(*) FILTER (WHERE mf.status = 'open' AND mf.severity = 'info') AS info_count
		FROM maintenance.findings mf
		JOIN workers.worker_instances wi ON wi.worker_instance_id = mf.worker_instance_id
		WHERE wi.worker_kind IN (%s)
	`, strings.Join(placeholders, ", ")), args...).Scan(
		&summary.Open,
		&summary.Critical,
		&summary.Error,
		&summary.Warning,
		&summary.Info,
	)
	return summary, err
}

func createOperation(ctx context.Context, q queryer, input CreateOperationInput) (Operation, error) {
	normalized, err := normalizeCreateOperationInput(input)
	if err != nil {
		return Operation{}, err
	}
	input = normalized
	return scanOperation(q.QueryRowContext(ctx, operationReturningSQL(`
		INSERT INTO maintenance.operations (
			maintenance_operation_id, operation_key, worker_instance_id, worker_run_id,
			operation_kind, status, subject_kind, subject_id,
			config_json, result_json, error_json, metadata
		)
		VALUES (
			$1, $2, $3, nullif($4, ''),
			$5, $6, $7, $8,
			$9, $10, $11, $12
		)
		RETURNING %s
	`),
		newMaintenanceOperationID(),
		input.OperationKey,
		input.WorkerInstanceID,
		input.WorkerRunID,
		input.OperationKind,
		input.Status,
		input.SubjectKind,
		input.SubjectID,
		[]byte(input.ConfigJSON),
		[]byte(input.ResultJSON),
		[]byte(input.ErrorJSON),
		[]byte(input.Metadata),
	))
}

func completeOperation(ctx context.Context, q queryer, input CompleteOperationInput) (Operation, error) {
	operation, err := scanOperation(q.QueryRowContext(ctx, operationReturningSQL(`
		UPDATE maintenance.operations
		SET status = $2,
		    result_json = $3,
		    error_json = $4,
		    finished_at = CASE
		        WHEN $2 IN ('succeeded', 'failed', 'cancelled', 'requires_manual_action', 'skipped') THEN COALESCE(finished_at, now())
		        ELSE finished_at
		    END,
		    updated_at = now()
		WHERE maintenance_operation_id = $1 OR operation_key = $1
		RETURNING %s
	`),
		input.OperationRef,
		input.Status,
		[]byte(input.ResultJSON),
		[]byte(input.ErrorJSON),
	))
	if err == sql.ErrNoRows {
		return Operation{}, fmt.Errorf("%w: operation %q", ErrNotFound, input.OperationRef)
	}
	return operation, err
}

func getOperation(ctx context.Context, q queryer, ref string) (Operation, error) {
	operation, err := scanOperation(q.QueryRowContext(ctx, operationSelectSQL(`
		SELECT %s
		FROM maintenance.operations mo
		WHERE mo.maintenance_operation_id = $1 OR mo.operation_key = $1
		LIMIT 1
	`), ref))
	if err == sql.ErrNoRows {
		return Operation{}, fmt.Errorf("%w: operation %q", ErrNotFound, ref)
	}
	return operation, err
}

func listOperations(ctx context.Context, q queryer, filter OperationFilter) ([]Operation, error) {
	if filter.Limit <= 0 || filter.Limit > maxOperationLimit {
		filter.Limit = defaultOperationLimit
	}

	query := operationSelectSQL(`SELECT %s FROM maintenance.operations mo WHERE true`)
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if strings.TrimSpace(filter.Kind) != "" {
		add("mo.operation_kind =", strings.TrimSpace(filter.Kind))
	}
	if strings.TrimSpace(filter.Status) != "" {
		add("mo.status =", strings.TrimSpace(filter.Status))
	}
	if strings.TrimSpace(filter.SubjectKind) != "" {
		add("mo.subject_kind =", strings.TrimSpace(filter.SubjectKind))
	}
	if strings.TrimSpace(filter.SubjectID) != "" {
		add("mo.subject_id =", strings.TrimSpace(filter.SubjectID))
	}

	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY mo.started_at DESC LIMIT $%d", len(args))

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	operations := []Operation{}
	for rows.Next() {
		operation, err := scanOperation(rows)
		if err != nil {
			return nil, err
		}
		operations = append(operations, operation)
	}
	return operations, rows.Err()
}

func latestOperationByKind(ctx context.Context, q queryer, kind, status string) (*Operation, error) {
	query := operationSelectSQL(`
		SELECT %s
		FROM maintenance.operations mo
		WHERE mo.operation_kind = $1
	`)
	args := []any{strings.TrimSpace(kind)}
	if strings.TrimSpace(status) != "" {
		args = append(args, strings.TrimSpace(status))
		query += fmt.Sprintf(" AND mo.status = $%d", len(args))
	}
	query += " ORDER BY mo.started_at DESC LIMIT 1"

	operation, err := scanOperation(q.QueryRowContext(ctx, query, args...))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &operation, nil
}

func insertArtifact(ctx context.Context, q queryer, input CreateArtifactInput) (Artifact, error) {
	var sha sql.NullString
	if strings.TrimSpace(input.SHA256) != "" {
		sha = sql.NullString{String: strings.TrimSpace(input.SHA256), Valid: true}
	}
	var size sql.NullInt64
	if input.SizeBytes != nil {
		size = sql.NullInt64{Int64: *input.SizeBytes, Valid: true}
	}

	return scanArtifact(q.QueryRowContext(ctx, artifactReturningSQL(`
		INSERT INTO maintenance.artifacts (
			maintenance_artifact_id, maintenance_operation_id, artifact_kind,
			uri, size_bytes, sha256, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING %s
	`),
		newMaintenanceArtifactID(),
		input.MaintenanceOperationID,
		input.ArtifactKind,
		input.URI,
		size,
		sha,
		[]byte(input.Metadata),
	))
}

func listArtifacts(ctx context.Context, q queryer, operationID string) ([]Artifact, error) {
	rows, err := q.QueryContext(ctx, artifactSelectSQL(`
		SELECT %s
		FROM maintenance.artifacts ma
		WHERE ma.maintenance_operation_id = $1
		ORDER BY ma.created_at ASC, ma.artifact_kind ASC
	`), operationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	artifacts := []Artifact{}
	for rows.Next() {
		artifact, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, rows.Err()
}

func objectStoreBlobCounts(ctx context.Context, q queryer) (ObjectStoreBlobCounts, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT status, COUNT(*)
		FROM files.blobs
		GROUP BY status
	`)
	if err != nil {
		return ObjectStoreBlobCounts{}, err
	}
	defer rows.Close()

	var counts ObjectStoreBlobCounts
	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return ObjectStoreBlobCounts{}, err
		}
		counts.Total += count
		switch status {
		case "pending":
			counts.Pending = count
		case "verified":
			counts.Verified = count
		case "missing":
			counts.Missing = count
		case "corrupt":
			counts.Corrupt = count
		}
	}
	return counts, rows.Err()
}

func latestObjectStoreScanRun(ctx context.Context, q queryer) (*ObjectStoreScanRun, error) {
	run, err := scanObjectStoreScanRun(q.QueryRowContext(ctx, `
		SELECT
			wr.worker_run_id,
			wr.run_status,
			wr.trigger_kind,
			wr.trigger_ref,
			wr.started_at,
			wr.finished_at,
			wr.result_summary_json,
			wr.counters_json
		FROM workers.worker_runs wr
		JOIN workers.worker_instances wi ON wi.worker_instance_id = wr.worker_instance_id
		WHERE wi.worker_key = 'main.object_store_integrity_sample'
		  AND wr.worker_kind = $1
		ORDER BY wr.started_at DESC
		LIMIT 1
	`, WorkerKindObjectStore))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func listFindings(ctx context.Context, q queryer, filter FindingFilter) ([]Finding, error) {
	if filter.Limit <= 0 || filter.Limit > maxFindingLimit {
		filter.Limit = defaultFindingLimit
	}

	query := findingSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}

	if strings.TrimSpace(filter.Status) != "" {
		add("mf.status =", strings.TrimSpace(filter.Status))
	}
	if strings.TrimSpace(filter.Severity) != "" {
		add("mf.severity =", strings.TrimSpace(filter.Severity))
	}
	if strings.TrimSpace(filter.WorkerRef) != "" {
		args = append(args, strings.TrimSpace(filter.WorkerRef))
		query += fmt.Sprintf(" AND (wi.worker_instance_id = $%[1]d OR wi.worker_key = $%[1]d OR wi.worker_kind = $%[1]d)", len(args))
	}

	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY mf.last_seen_at DESC LIMIT $%d", len(args))

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	findings := []Finding{}
	for rows.Next() {
		finding, err := scanFinding(rows)
		if err != nil {
			return nil, err
		}
		findings = append(findings, finding)
	}
	return findings, rows.Err()
}

func latestDBMaintenanceRun(ctx context.Context, q queryer) (*DBMaintenanceRun, error) {
	run, err := scanDBMaintenanceRun(q.QueryRowContext(ctx, `
		SELECT
			wr.worker_run_id,
			wr.run_status,
			wr.trigger_kind,
			wr.trigger_ref,
			wr.started_at,
			wr.finished_at,
			wr.result_summary_json,
			wr.counters_json
		FROM workers.worker_runs wr
		JOIN workers.worker_instances wi ON wi.worker_instance_id = wr.worker_instance_id
		WHERE wi.worker_key = 'main.db_maintenance'
		  AND wr.worker_kind = $1
		  AND wr.run_status = 'succeeded'
		ORDER BY wr.started_at DESC
		LIMIT 1
	`, WorkerKindDBMaintenance))
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func databaseTableStatus(ctx context.Context, q queryer) ([]DatabaseTable, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT
			n.nspname,
			c.relname,
			COALESCE(s.n_live_tup, 0)::bigint AS estimated_rows,
			pg_total_relation_size(c.oid)::bigint AS total_bytes
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_stat_user_tables s ON s.relid = c.oid
		WHERE (n.nspname, c.relname) IN (
			('events', 'events'),
			('workers', 'worker_runs'),
			('workers', 'worker_controls'),
			('workers', 'worker_leases'),
			('maintenance', 'operations'),
			('maintenance', 'findings'),
			('maintenance', 'database_rollups')
		)
		  AND c.relkind IN ('r', 'p')
		ORDER BY total_bytes DESC, n.nspname ASC, c.relname ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []DatabaseTable{}
	for rows.Next() {
		var item DatabaseTable
		if err := rows.Scan(&item.Schema, &item.Name, &item.Rows, &item.TotalBytes); err != nil {
			return nil, err
		}
		item.QualifiedName = item.Schema + "." + item.Name
		item.RetentionRole = databaseRetentionRole(item.QualifiedName)
		item.Pressure = databasePressure(item.TotalBytes)
		out = append(out, item)
	}
	return out, rows.Err()
}

func databaseRollupStatus(ctx context.Context, q queryer, limit int) ([]DatabaseRollup, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := q.QueryContext(ctx, `
		SELECT rollup_day::timestamptz, rollup_kind, rollup_key,
		       success_count, failure_count, total_count, total_duration_ms, metadata
		FROM maintenance.database_rollups
		ORDER BY rollup_day DESC, rollup_kind ASC, rollup_key ASC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []DatabaseRollup{}
	for rows.Next() {
		var item DatabaseRollup
		if err := rows.Scan(
			&item.RollupDay,
			&item.RollupKind,
			&item.RollupKey,
			&item.SuccessCount,
			&item.FailureCount,
			&item.TotalCount,
			&item.TotalDurationMS,
			&item.Metadata,
		); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func databaseRetentionCandidates(ctx context.Context, q queryer, cutoff time.Time) (map[string]int64, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT 'events.events' AS table_name, COUNT(*)::bigint AS candidates
		FROM events.events
		WHERE created_at < $1
		  AND event_level IN ('local_debug', 'node_activity', 'summary')
		  AND COALESCE(status, '') IN ('ok', 'success', 'succeeded', 'completed')
		UNION ALL
		SELECT 'workers.worker_runs' AS table_name, COUNT(*)::bigint AS candidates
		FROM workers.worker_runs
		WHERE started_at < $1
		  AND run_status = 'succeeded'
		  AND trigger_kind <> 'manual'
		UNION ALL
		SELECT 'workers.worker_controls' AS table_name, COUNT(*)::bigint AS candidates
		FROM workers.worker_controls
		WHERE requested_at < $1
		  AND control_status IN ('applied', 'expired', 'rejected')
		UNION ALL
		SELECT 'workers.worker_leases' AS table_name, COUNT(*)::bigint AS candidates
		FROM workers.worker_leases
		WHERE acquired_at < $1
		  AND lease_status IN ('released', 'expired', 'stolen')
	`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int64{}
	for rows.Next() {
		var table string
		var count int64
		if err := rows.Scan(&table, &count); err != nil {
			return nil, err
		}
		out[table] = count
	}
	return out, rows.Err()
}

func databaseRetentionPolicy() map[string]string {
	return map[string]string{
		"events.events":           "routine local/node/summary success events can be summarized later; audit, security, failure, and operator-significant events are kept",
		"workers.worker_runs":     "routine succeeded non-manual runs are rollup candidates; failures, manual runs, running rows, and reference-critical rows are kept",
		"workers.worker_controls": "old applied, expired, or rejected controls are low-value candidates; pending and failed controls are kept",
		"workers.worker_leases":   "old released, expired, or stolen leases are low-value candidates; active leases are kept",
		"maintenance.operations":  "maintenance operations are evidence and are kept; only aggregate rollups should be added",
		"maintenance.findings":    "maintenance findings are operator evidence and are kept until resolved/archived by policy",
	}
}

type databaseCompactLimits struct {
	AllowedTables   []string
	RowLimits       map[string]int64
	MaxRowsPerBatch int64
	MaxTotalRows    int64
}

func databaseCompactionLimits(input DatabaseCompactInput, plans []DatabaseRetentionPlan) (databaseCompactLimits, []string) {
	warnings := []string{}
	limits := databaseCompactLimits{
		AllowedTables:   []string{"events.events", "workers.worker_leases"},
		RowLimits:       map[string]int64{},
		MaxRowsPerBatch: input.MaxRowsPerBatch,
		MaxTotalRows:    input.MaxTotalRows,
	}
	if limits.MaxRowsPerBatch <= 0 {
		limits.MaxRowsPerBatch = defaultDBCompactMaxRowsPerBatch
	}
	if limits.MaxTotalRows <= 0 {
		limits.MaxTotalRows = defaultDBCompactMaxTotalRows
	}
	if limits.MaxRowsPerBatch > maxDBCompactMaxRowsPerBatch {
		warnings = append(warnings, fmt.Sprintf("max_rows_per_batch was capped at %d", maxDBCompactMaxRowsPerBatch))
		limits.MaxRowsPerBatch = maxDBCompactMaxRowsPerBatch
	}
	if limits.MaxTotalRows > maxDBCompactMaxTotalRows {
		warnings = append(warnings, fmt.Sprintf("max_total_rows was capped at %d", maxDBCompactMaxTotalRows))
		limits.MaxTotalRows = maxDBCompactMaxTotalRows
	}
	if limits.MaxRowsPerBatch > limits.MaxTotalRows {
		limits.MaxRowsPerBatch = limits.MaxTotalRows
	}
	candidates := map[string]int64{}
	for _, plan := range plans {
		candidates[plan.Table] = plan.CandidateRows
	}
	for _, table := range limits.AllowedTables {
		limit := candidates[table]
		if limit < 0 {
			limit = 0
		}
		if limit > limits.MaxTotalRows {
			limit = limits.MaxTotalRows
		}
		limits.RowLimits[table] = limit
	}
	return limits, warnings
}

func databaseCandidateDrift(expected map[string]int64, current map[string]int64) map[string]int64 {
	drift := map[string]int64{}
	for table, currentCount := range current {
		if currentCount != expected[table] {
			drift[table] = currentCount - expected[table]
		}
	}
	for table, expectedCount := range expected {
		if _, ok := current[table]; !ok && expectedCount != 0 {
			drift[table] = -expectedCount
		}
	}
	if len(drift) == 0 {
		return nil
	}
	return drift
}

func validateDatabaseRetentionApply(plans []DatabaseRetentionPlan) error {
	for _, plan := range plans {
		if plan.CandidateRows == 0 {
			continue
		}
		switch plan.Table {
		case "events.events":
			if plan.CandidateClass != "routine_success_events" {
				return fmt.Errorf("%w: events retention apply only allows routine success events", ErrInvalid)
			}
		case "workers.worker_leases":
			if plan.CandidateClass != "settled_worker_leases" {
				return fmt.Errorf("%w: worker lease retention apply only allows settled leases", ErrInvalid)
			}
		case "workers.worker_runs", "workers.worker_controls", "maintenance.operations", "maintenance.findings":
			continue
		default:
			return fmt.Errorf("%w: retention apply is not allowed for %s", ErrInvalid, plan.Table)
		}
	}
	return nil
}

func databaseRetentionApplyWarnings(plans []DatabaseRetentionPlan) []string {
	warnings := []string{
		"Apply writes aggregate rollups and deletes only old routine success events plus settled worker leases.",
		"Worker run details, worker controls, maintenance operations, maintenance findings, audit/security events, failures, manual actions, and recent rows are preserved.",
	}
	for _, plan := range plans {
		if plan.CandidateRows == 0 {
			continue
		}
		switch plan.Table {
		case "workers.worker_runs":
			warnings = append(warnings, "workers.worker_runs has rollup candidates, but detailed worker-run deletion remains disabled until reference-safety is proven.")
		case "workers.worker_controls":
			warnings = append(warnings, "workers.worker_controls has settled candidates, but control deletion remains disabled until operator-history impact is reviewed.")
		}
	}
	return warnings
}

func databaseAuditCriticalExclusions() []DatabaseRetentionExclusion {
	return []DatabaseRetentionExclusion{
		{Table: "events.events", Rule: "audit/security/failure/manual events", Reason: "needed for security review, causality, and user-visible history"},
		{Table: "workers.worker_runs", Rule: "failed/manual/running/reference-critical runs", Reason: "needed for failed-job diagnosis, worker accountability, and linked operation history"},
		{Table: "maintenance.operations", Rule: "all operation evidence", Reason: "maintenance and backup history must remain inspectable"},
		{Table: "maintenance.findings", Rule: "all open or resolved findings", Reason: "attention lifetime and operator decisions must remain auditable"},
	}
}

func databaseRetentionPlans(tables []DatabaseTable, candidates map[string]int64) []DatabaseRetentionPlan {
	byName := map[string]DatabaseTable{}
	for _, table := range tables {
		byName[table.QualifiedName] = table
	}
	order := []string{"events.events", "workers.worker_runs", "workers.worker_controls", "workers.worker_leases", "maintenance.operations", "maintenance.findings"}
	plans := make([]DatabaseRetentionPlan, 0, len(order))
	for _, tableName := range order {
		table := byName[tableName]
		candidateRows := candidates[tableName]
		plan := DatabaseRetentionPlan{
			Table:            tableName,
			RetentionRole:    firstNonEmpty(table.RetentionRole, databaseRetentionRole(tableName)),
			CandidateClass:   databaseRetentionCandidateClass(tableName),
			CandidateRows:    candidateRows,
			EstimatedBytes:   estimateRetentionCandidateBytes(table, candidateRows),
			CompactableLater: databaseRetentionCompactableLater(tableName),
			KeepRule:         databaseRetentionKeepRule(tableName),
			FutureAction:     databaseRetentionFutureAction(tableName),
			Reason:           databaseRetentionReason(tableName, candidateRows),
		}
		plans = append(plans, plan)
	}
	return plans
}

func estimateRetentionCandidateBytes(table DatabaseTable, candidateRows int64) int64 {
	if table.Rows <= 0 || table.TotalBytes <= 0 || candidateRows <= 0 {
		return 0
	}
	if candidateRows >= table.Rows {
		return table.TotalBytes
	}
	return table.TotalBytes * candidateRows / table.Rows
}

func databaseRetentionCandidateClass(table string) string {
	switch table {
	case "events.events":
		return "routine_success_events"
	case "workers.worker_runs":
		return "routine_succeeded_non_manual_runs"
	case "workers.worker_controls":
		return "settled_worker_controls"
	case "workers.worker_leases":
		return "settled_worker_leases"
	case "maintenance.operations":
		return "none_audit_evidence"
	case "maintenance.findings":
		return "none_operator_evidence"
	default:
		return "none"
	}
}

func databaseRetentionCompactableLater(table string) bool {
	switch table {
	case "events.events", "workers.worker_controls", "workers.worker_leases":
		return true
	case "workers.worker_runs":
		return false
	default:
		return false
	}
}

func databaseRetentionKeepRule(table string) string {
	switch table {
	case "events.events":
		return "keep audit, security, error, failed, manual, and recent events"
	case "workers.worker_runs":
		return "keep failed, manual, running, recent, and reference-critical worker runs"
	case "workers.worker_controls":
		return "keep pending controls and failed controls"
	case "workers.worker_leases":
		return "keep active leases"
	case "maintenance.operations":
		return "keep maintenance operation evidence"
	case "maintenance.findings":
		return "keep operator attention evidence"
	default:
		return "keep by default"
	}
}

func databaseRetentionFutureAction(table string) string {
	switch table {
	case "events.events":
		return "future rollup plus reviewed deletion of routine success events only"
	case "workers.worker_runs":
		return "future rollup only until reference safety is proven"
	case "workers.worker_controls", "workers.worker_leases":
		return "future reviewed deletion of settled low-value rows"
	case "maintenance.operations", "maintenance.findings":
		return "no destructive retention action planned"
	default:
		return "no action planned"
	}
}

func databaseRetentionReason(table string, candidates int64) string {
	if candidates == 0 {
		return "no rows match the current dry-run candidate rule"
	}
	switch table {
	case "events.events":
		return "matched rows are old routine success events, not audit-critical event classes"
	case "workers.worker_runs":
		return "matched rows are old succeeded non-manual worker runs; detailed deletion still needs reference-safety design"
	case "workers.worker_controls":
		return "matched rows are settled worker controls"
	case "workers.worker_leases":
		return "matched rows are settled worker leases"
	default:
		return "table is kept as audit/operator evidence"
	}
}

func databaseCompactionRollups(ctx context.Context, q queryExecer, cutoff time.Time, dryRun bool) ([]DatabaseRollup, error) {
	rows, err := q.QueryContext(ctx, `
		WITH rollups AS (
			SELECT date_trunc('day', started_at)::date AS rollup_day,
			       'worker_runs'::text AS rollup_kind,
			       worker_kind || ':' || run_status AS rollup_key,
			       COUNT(*) FILTER (WHERE run_status = 'succeeded')::bigint AS success_count,
			       COUNT(*) FILTER (WHERE run_status <> 'succeeded')::bigint AS failure_count,
			       COUNT(*)::bigint AS total_count,
			       COALESCE(SUM(EXTRACT(EPOCH FROM (finished_at - started_at)) * 1000)::bigint, 0) AS total_duration_ms,
			       jsonb_build_object('source_table', 'workers.worker_runs') AS metadata
			FROM workers.worker_runs
			WHERE started_at < $1
			  AND finished_at IS NOT NULL
			GROUP BY 1, 2, 3
			UNION ALL
			SELECT date_trunc('day', created_at)::date AS rollup_day,
			       'events'::text AS rollup_kind,
			       event_type || ':' || COALESCE(NULLIF(result, ''), NULLIF(status, ''), 'recorded') AS rollup_key,
			       COUNT(*) FILTER (WHERE COALESCE(status, '') IN ('', 'ok', 'success', 'succeeded', 'completed'))::bigint AS success_count,
			       COUNT(*) FILTER (WHERE COALESCE(status, '') IN ('failed', 'error'))::bigint AS failure_count,
			       COUNT(*)::bigint AS total_count,
			       0::bigint AS total_duration_ms,
			       jsonb_build_object('source_table', 'events.events') AS metadata
			FROM events.events
			WHERE created_at < $1
			GROUP BY 1, 2, 3
			UNION ALL
			SELECT date_trunc('day', started_at)::date AS rollup_day,
			       'maintenance_operations'::text AS rollup_kind,
			       operation_kind || ':' || status AS rollup_key,
			       COUNT(*) FILTER (WHERE status = 'succeeded')::bigint AS success_count,
			       COUNT(*) FILTER (WHERE status IN ('failed', 'requires_manual_action'))::bigint AS failure_count,
			       COUNT(*)::bigint AS total_count,
			       COALESCE(SUM(EXTRACT(EPOCH FROM (COALESCE(finished_at, updated_at) - started_at)) * 1000)::bigint, 0) AS total_duration_ms,
			       jsonb_build_object('source_table', 'maintenance.operations') AS metadata
			FROM maintenance.operations
			WHERE started_at < $1
			  AND status IN ('succeeded', 'failed', 'requires_manual_action', 'skipped')
			GROUP BY 1, 2, 3
		)
		SELECT rollup_day::timestamptz, rollup_kind, rollup_key,
		       success_count, failure_count, total_count, total_duration_ms, metadata
		FROM rollups
		ORDER BY rollup_day DESC, rollup_kind ASC, rollup_key ASC
	`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []DatabaseRollup{}
	for rows.Next() {
		var item DatabaseRollup
		if err := rows.Scan(
			&item.RollupDay,
			&item.RollupKind,
			&item.RollupKey,
			&item.SuccessCount,
			&item.FailureCount,
			&item.TotalCount,
			&item.TotalDurationMS,
			&item.Metadata,
		); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if dryRun {
		return out, nil
	}
	for _, item := range out {
		if _, err := q.ExecContext(ctx, `
			INSERT INTO maintenance.database_rollups (
				database_rollup_id, rollup_day, rollup_kind, rollup_key,
				success_count, failure_count, total_count, total_duration_ms, metadata
			)
			VALUES ($1, $2::date, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (rollup_day, rollup_kind, rollup_key) DO UPDATE
			SET success_count = EXCLUDED.success_count,
			    failure_count = EXCLUDED.failure_count,
			    total_count = EXCLUDED.total_count,
			    total_duration_ms = EXCLUDED.total_duration_ms,
			    metadata = EXCLUDED.metadata,
			    updated_at = now()
		`, ids.NewDatabaseRollupID(), item.RollupDay, item.RollupKind, item.RollupKey, item.SuccessCount, item.FailureCount, item.TotalCount, item.TotalDurationMS, []byte(item.Metadata)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func deleteRoutineDatabaseRows(ctx context.Context, q queryExecer, cutoff time.Time, limits databaseCompactLimits) (map[string]int64, error) {
	deleted := map[string]int64{}
	statements := []struct {
		table string
		sql   string
	}{
		{
			table: "events.events",
			sql: `
				WITH candidates AS (
					SELECT event_id
					FROM events.events
					WHERE created_at < $1
					  AND event_level IN ('local_debug', 'node_activity', 'summary')
					  AND COALESCE(status, '') IN ('ok', 'success', 'succeeded', 'completed')
					ORDER BY created_at ASC, event_id ASC
					LIMIT $2
				)
				DELETE FROM events.events e
				USING candidates c
				WHERE e.event_id = c.event_id
			`,
		},
		{
			table: "workers.worker_leases",
			sql: `
				WITH candidates AS (
					SELECT worker_lease_id
					FROM workers.worker_leases
					WHERE acquired_at < $1
					  AND lease_status IN ('released', 'expired', 'stolen')
					ORDER BY acquired_at ASC, worker_lease_id ASC
					LIMIT $2
				)
				DELETE FROM workers.worker_leases wl
				USING candidates c
				WHERE wl.worker_lease_id = c.worker_lease_id
			`,
		},
	}
	remainingTotal := limits.MaxTotalRows
	if remainingTotal <= 0 {
		return deleted, nil
	}
	batchSize := limits.MaxRowsPerBatch
	if batchSize <= 0 {
		batchSize = defaultDBCompactMaxRowsPerBatch
	}
	for _, stmt := range statements {
		remainingTable := limits.RowLimits[stmt.table]
		for remainingTotal > 0 && remainingTable > 0 {
			limit := minInt64(batchSize, remainingTotal, remainingTable)
			result, err := q.ExecContext(ctx, stmt.sql, cutoff, limit)
			if err != nil {
				return deleted, err
			}
			count, _ := result.RowsAffected()
			deleted[stmt.table] += count
			remainingTotal -= count
			remainingTable -= count
			if count == 0 || count < limit {
				break
			}
		}
	}
	return deleted, nil
}

func minInt64(first int64, rest ...int64) int64 {
	min := first
	for _, value := range rest {
		if value < min {
			min = value
		}
	}
	return min
}

func recordDatabaseCompactionEvidence(ctx context.Context, q queryExecer, result *DatabaseCompactResult) error {
	workerInstanceID, err := dbMaintenanceWorkerInstanceID(ctx, q)
	if err != nil {
		return err
	}
	operation, err := createOperation(ctx, q, CreateOperationInput{
		WorkerInstanceID: workerInstanceID,
		OperationKind:    OperationKindDatabaseCompaction,
		Status:           OperationRunning,
		SubjectKind:      "database",
		SubjectID:        "loom_main",
		ConfigJSON: mustJSON(map[string]any{
			"schema_version":         "database_compaction.config.v0.9.9",
			"plan_id":                result.PlanID,
			"plan_hash":              result.PlanHash,
			"cutoff_at":              result.CutoffAt,
			"recent_success_days":    result.RecentSuccessDays,
			"max_rows_per_batch":     result.MaxRowsPerBatch,
			"max_total_rows":         result.MaxTotalRows,
			"allowed_tables":         result.AllowedTables,
			"row_limits":             result.RowLimits,
			"reason":                 result.Reason,
			"physical_storage_note":  result.PhysicalStorageNote,
			"reviewed_plan_required": false,
		}),
		Metadata: json.RawMessage(`{"schema_version":"maintenance_operation.metadata.v0.2","source":"database_compaction"}`),
	})
	if err != nil {
		return err
	}
	result.EvidenceOperationID = operation.MaintenanceOperationID
	result.EvidenceStatus = "recorded"
	_, err = completeOperation(ctx, q, CompleteOperationInput{
		OperationRef: operation.MaintenanceOperationID,
		Status:       OperationSucceeded,
		ResultJSON:   mustJSON(result),
		ErrorJSON:    json.RawMessage(`{}`),
	})
	return err
}

func dbMaintenanceWorkerInstanceID(ctx context.Context, q queryer) (string, error) {
	var workerInstanceID string
	err := q.QueryRowContext(ctx, `
		SELECT worker_instance_id
		FROM workers.worker_instances
		WHERE worker_key = 'main.db_maintenance'
		   OR worker_kind = $1
		ORDER BY CASE WHEN worker_key = 'main.db_maintenance' THEN 0 ELSE 1 END,
		         worker_instance_id ASC
		LIMIT 1
	`, WorkerKindDBMaintenance).Scan(&workerInstanceID)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("%w: db maintenance worker instance is not registered", ErrNotFound)
	}
	return workerInstanceID, err
}

func databaseRetentionRole(qualified string) string {
	switch qualified {
	case "events.events":
		return "routine_event_history"
	case "workers.worker_runs":
		return "worker_run_history"
	case "workers.worker_controls":
		return "worker_control_history"
	case "workers.worker_leases":
		return "worker_lease_history"
	case "maintenance.operations":
		return "maintenance_evidence"
	case "maintenance.findings":
		return "manual_action_evidence"
	case "maintenance.database_rollups":
		return "retention_rollups"
	default:
		return "none"
	}
}

func databasePressure(bytes int64) string {
	switch {
	case bytes >= 5<<30:
		return OverallCritical
	case bytes >= 1<<30:
		return OverallWarning
	default:
		return OverallOK
	}
}

func databaseTableWarnings(tables []DatabaseTable) []string {
	warnings := []string{}
	for _, table := range tables {
		switch table.Pressure {
		case OverallCritical:
			warnings = append(warnings, fmt.Sprintf("%s is over 5 GiB; run `loom maintenance retention dry-run --json` and review retention candidates", table.QualifiedName))
		case OverallWarning:
			warnings = append(warnings, fmt.Sprintf("%s is over 1 GiB; run `loom maintenance retention dry-run --json` before any cleanup decision", table.QualifiedName))
		}
	}
	return warnings
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func getFindingByKey(ctx context.Context, q queryer, key string) (Finding, error) {
	finding, err := scanFinding(q.QueryRowContext(ctx, findingSelectSQL()+` WHERE mf.finding_key = $1`, key))
	if err == sql.ErrNoRows {
		return Finding{}, fmt.Errorf("%w: finding %q", ErrNotFound, key)
	}
	return finding, err
}

func findingSelectSQL() string {
	return `
		SELECT
			mf.maintenance_finding_id,
			mf.finding_key,
			mf.worker_instance_id,
			wi.worker_key,
			wi.worker_kind,
			mf.worker_run_id,
			mf.finding_kind,
			mf.severity,
			mf.status,
			mf.subject_kind,
			mf.subject_id,
			mf.first_seen_at,
			mf.last_seen_at,
			mf.resolved_at,
			mf.summary,
			mf.details_json,
			mf.resolution_json,
			mf.created_at,
			mf.updated_at,
			mf.metadata
		FROM maintenance.findings mf
		JOIN workers.worker_instances wi ON wi.worker_instance_id = mf.worker_instance_id
	`
}

func operationSelectSQL(template string) string {
	return fmt.Sprintf(template, operationColumns("mo."))
}

func operationReturningSQL(template string) string {
	return fmt.Sprintf(template, operationColumns(""))
}

func operationColumns(prefix string) string {
	columns := `
		%smaintenance_operation_id,
		%soperation_key,
		%sworker_instance_id,
		%sworker_run_id,
		%soperation_kind,
		%sstatus,
		%ssubject_kind,
		%ssubject_id,
		%sstarted_at,
		%sfinished_at,
		%sconfig_json,
		%sresult_json,
		%serror_json,
		%screated_at,
		%supdated_at,
		%smetadata
	`
	return fmt.Sprintf(columns,
		prefix, prefix, prefix, prefix,
		prefix, prefix, prefix, prefix,
		prefix, prefix, prefix, prefix,
		prefix, prefix, prefix, prefix,
	)
}

func artifactSelectSQL(template string) string {
	return fmt.Sprintf(template, artifactColumns("ma."))
}

func artifactReturningSQL(template string) string {
	return fmt.Sprintf(template, artifactColumns(""))
}

func artifactColumns(prefix string) string {
	columns := `
		%smaintenance_artifact_id,
		%smaintenance_operation_id,
		%sartifact_kind,
		%suri,
		%ssize_bytes,
		%ssha256,
		%screated_at,
		%smetadata
	`
	return fmt.Sprintf(columns,
		prefix, prefix, prefix, prefix,
		prefix, prefix, prefix, prefix,
	)
}

func scanOperation(s scanner) (Operation, error) {
	var operation Operation
	var workerRunID sql.NullString
	var finishedAt sql.NullTime
	var configJSON []byte
	var resultJSON []byte
	var errorJSON []byte
	var metadata []byte

	if err := s.Scan(
		&operation.MaintenanceOperationID,
		&operation.OperationKey,
		&operation.WorkerInstanceID,
		&workerRunID,
		&operation.OperationKind,
		&operation.Status,
		&operation.SubjectKind,
		&operation.SubjectID,
		&operation.StartedAt,
		&finishedAt,
		&configJSON,
		&resultJSON,
		&errorJSON,
		&operation.CreatedAt,
		&operation.UpdatedAt,
		&metadata,
	); err != nil {
		return Operation{}, err
	}

	operation.WorkerRunID = nullStringPtr(workerRunID)
	operation.FinishedAt = nullTimePtr(finishedAt)
	operation.ConfigJSON = rawMessage(configJSON)
	operation.ResultJSON = rawMessage(resultJSON)
	operation.ErrorJSON = rawMessage(errorJSON)
	operation.Metadata = rawMessage(metadata)
	return operation, nil
}

func scanArtifact(s scanner) (Artifact, error) {
	var artifact Artifact
	var size sql.NullInt64
	var sha sql.NullString
	var metadata []byte
	if err := s.Scan(
		&artifact.MaintenanceArtifactID,
		&artifact.MaintenanceOperationID,
		&artifact.ArtifactKind,
		&artifact.URI,
		&size,
		&sha,
		&artifact.CreatedAt,
		&metadata,
	); err != nil {
		return Artifact{}, err
	}
	if size.Valid {
		artifact.SizeBytes = &size.Int64
	}
	if sha.Valid {
		artifact.SHA256 = &sha.String
	}
	artifact.Metadata = rawMessage(metadata)
	return artifact, nil
}

func scanFinding(s scanner) (Finding, error) {
	var finding Finding
	var workerRunID sql.NullString
	var resolvedAt sql.NullTime
	var details []byte
	var resolution []byte
	var metadata []byte

	if err := s.Scan(
		&finding.MaintenanceFindingID,
		&finding.FindingKey,
		&finding.WorkerInstanceID,
		&finding.WorkerKey,
		&finding.WorkerKind,
		&workerRunID,
		&finding.FindingKind,
		&finding.Severity,
		&finding.Status,
		&finding.SubjectKind,
		&finding.SubjectID,
		&finding.FirstSeenAt,
		&finding.LastSeenAt,
		&resolvedAt,
		&finding.Summary,
		&details,
		&resolution,
		&finding.CreatedAt,
		&finding.UpdatedAt,
		&metadata,
	); err != nil {
		return Finding{}, err
	}

	finding.WorkerRunID = nullStringPtr(workerRunID)
	finding.ResolvedAt = nullTimePtr(resolvedAt)
	finding.DetailsJSON = rawMessage(details)
	finding.ResolutionJSON = rawMessage(resolution)
	finding.Metadata = rawMessage(metadata)
	return finding, nil
}

func scanDBMaintenanceRun(s scanner) (DBMaintenanceRun, error) {
	var run DBMaintenanceRun
	var finishedAt sql.NullTime
	var resultSummary []byte
	var counters []byte
	if err := s.Scan(
		&run.WorkerRunID,
		&run.RunStatus,
		&run.TriggerKind,
		&run.TriggerRef,
		&run.StartedAt,
		&finishedAt,
		&resultSummary,
		&counters,
	); err != nil {
		return DBMaintenanceRun{}, err
	}
	run.FinishedAt = nullTimePtr(finishedAt)
	run.ResultSummaryJSON = rawMessage(resultSummary)
	run.CountersJSON = rawMessage(counters)
	return run, nil
}

func scanObjectStoreScanRun(s scanner) (ObjectStoreScanRun, error) {
	var run ObjectStoreScanRun
	var finishedAt sql.NullTime
	var resultSummary []byte
	var counters []byte
	if err := s.Scan(
		&run.WorkerRunID,
		&run.RunStatus,
		&run.TriggerKind,
		&run.TriggerRef,
		&run.StartedAt,
		&finishedAt,
		&resultSummary,
		&counters,
	); err != nil {
		return ObjectStoreScanRun{}, err
	}
	run.FinishedAt = nullTimePtr(finishedAt)
	run.ResultSummaryJSON = rawMessage(resultSummary)
	run.CountersJSON = rawMessage(counters)
	return run, nil
}

func newMaintenanceOperationID() string {
	return ids.NewMaintenanceOperationID()
}

func newMaintenanceFindingID() string {
	return ids.NewMaintenanceFindingID()
}

func newMaintenanceArtifactID() string {
	return ids.NewMaintenanceArtifactID()
}

func rawMessage(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(raw)
}

func nullStringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	out := value.String
	return &out
}

func nullTimePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	out := value.Time
	return &out
}
