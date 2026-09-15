package runtimes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/workers"
)

const defaultLargeTableWarningBytes int64 = 1 << 30

type DBMaintenanceRuntime struct {
	DB            *sql.DB
	Maintenance   maintenance.Service
	DBURL         string
	MigrationsDir string
	Now           func() time.Time
}

type dbMaintenanceConfig struct {
	SchemaVersion          string `json:"schema_version"`
	Mode                   string `json:"mode"`
	CheckMigrations        bool   `json:"check_migrations"`
	CheckTableSizes        bool   `json:"check_table_sizes"`
	CheckQueueDepths       bool   `json:"check_queue_depths"`
	CheckLocks             bool   `json:"check_locks"`
	CheckLongTransactions  bool   `json:"check_long_transactions"`
	LargeTableWarningBytes int64  `json:"large_table_warning_bytes"`
	MaxRuntimeMS           int    `json:"max_runtime_ms"`
}

type tableSizeSummary struct {
	Schema    string `json:"schema"`
	Table     string `json:"table"`
	Bytes     int64  `json:"bytes"`
	Qualified string `json:"qualified"`
}

type queueCounts struct {
	ActiveWorkerRuns       int64 `json:"active_worker_runs"`
	RecentFailedWorkerRuns int64 `json:"recent_failed_worker_runs"`
	QueuedJobs             int64 `json:"queued_jobs"`
	RunningJobs            int64 `json:"running_jobs"`
}

func NewDBMaintenanceRuntime(db *sql.DB, maintenanceService maintenance.Service, dbURL, migrationsDir string) DBMaintenanceRuntime {
	return DBMaintenanceRuntime{
		DB:            db,
		Maintenance:   maintenanceService,
		DBURL:         dbURL,
		MigrationsDir: migrationsDir,
	}
}

func (r DBMaintenanceRuntime) Kind() string {
	return workers.KindDBMaintenance
}

func (r DBMaintenanceRuntime) Describe() workers.KindDescriptor {
	return workers.KindDescriptor{
		WorkerKind:                   workers.KindDBMaintenance,
		DisplayName:                  "Database maintenance",
		Description:                  "Runs lightweight database diagnostics and records maintenance findings.",
		RuntimeOwner:                 workers.RuntimeOwnerLoomd,
		RuntimePackage:               "loom.core.maintenance",
		Status:                       workers.KindStatusActive,
		SupportedLocalities:          []string{workers.LocalityMainOwned},
		DefaultTickPolicyJSON:        json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":300,"run_on_startup":true}`),
		DefaultConcurrencyPolicyJSON: json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
		DefaultRetryPolicyJSON:       json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
		DefaultTimeoutPolicyJSON:     json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":30}`),
		DefaultResourceLimitsJSON:    json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"light"}`),
		ConfigSchemaJSON:             json.RawMessage(`{"schema_version":"db_maintenance.config_schema.v0.2","type":"object"}`),
		CheckpointSchemaJSON:         json.RawMessage(`{"schema_version":"db_maintenance.checkpoint_schema.v0.2","type":"object"}`),
		ResultSchemaJSON:             json.RawMessage(`{"schema_version":"db_maintenance.result_schema.v0.2","type":"object"}`),
		Metadata:                     json.RawMessage(`{"schema_version":"worker_kind.metadata.v0.2","builtin":true}`),
	}
}

func (r DBMaintenanceRuntime) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"schema_version":"db_maintenance.config.v0.2","mode":"light","check_migrations":true,"check_table_sizes":true,"check_queue_depths":true,"check_locks":false,"check_long_transactions":false,"large_table_warning_bytes":1073741824,"max_runtime_ms":30000}`)
}

func (r DBMaintenanceRuntime) DefaultInstances() []workers.InstanceDescriptor {
	return []workers.InstanceDescriptor{
		{
			WorkerKey:          "main.db_maintenance",
			WorkerKind:         workers.KindDBMaintenance,
			DisplayName:        "Database maintenance",
			Description:        "Runs lightweight database diagnostics.",
			Locality:           workers.LocalityMainOwned,
			ConfigJSON:         r.DefaultConfig(),
			TickPolicyJSON:     json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":300,"run_on_startup":true}`),
			ConcurrencyJSON:    json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
			RetryPolicyJSON:    json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
			TimeoutPolicyJSON:  json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":30}`),
			ResourceLimitsJSON: json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"light"}`),
			VisibilityJSON:     json.RawMessage(`{}`),
			Metadata:           json.RawMessage(`{"schema_version":"worker_instance.metadata.v0.2","seeded_by":"workers.SeedBuiltins"}`),
		},
	}
}

func (r DBMaintenanceRuntime) ValidateConfig(ctx context.Context, config json.RawMessage) error {
	_, err := parseDBMaintenanceConfig(config)
	return err
}

func (r DBMaintenanceRuntime) RunOnce(ctx context.Context, run workers.RunContext) (workers.RunResult, error) {
	if r.DB == nil {
		return workers.RunResult{}, fmt.Errorf("database maintenance database is not configured")
	}
	if r.Maintenance.DB == nil {
		return workers.RunResult{}, fmt.Errorf("database maintenance service is not configured")
	}

	config, err := parseDBMaintenanceConfig(run.Instance.ConfigJSON)
	if err != nil {
		return workers.RunResult{}, err
	}

	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}

	var one int
	if err := r.DB.QueryRowContext(ctx, `SELECT 1`).Scan(&one); err != nil {
		return workers.RunResult{}, fmt.Errorf("database connectivity check failed: %w", err)
	}
	if one != 1 {
		return workers.RunResult{}, fmt.Errorf("database connectivity check returned %d", one)
	}

	migrationResult := migrations.Result{Status: "not_checked"}
	if config.CheckMigrations {
		migrationResult = migrations.Status(ctx, r.DBURL, r.MigrationsDir)
		if err := r.recordMigrationFinding(ctx, run, migrationResult); err != nil {
			return workers.RunResult{}, err
		}
	}

	largestTables := []tableSizeSummary{}
	if config.CheckTableSizes {
		largestTables, err = r.collectTableSizes(ctx)
		if err != nil {
			return workers.RunResult{}, err
		}
		if err := r.recordLargeTableFindings(ctx, run, largestTables, config.LargeTableWarningBytes); err != nil {
			return workers.RunResult{}, err
		}
	}

	queues := queueCounts{}
	if config.CheckQueueDepths {
		queues, err = r.collectQueueCounts(ctx)
		if err != nil {
			return workers.RunResult{}, err
		}
	}

	status := dbMaintenanceStatus(migrationResult)
	resultSummary, err := dbMaintenanceSummary(migrationResult, largestTables, queues, status)
	if err != nil {
		return workers.RunResult{}, err
	}
	checkpoint, err := dbMaintenanceCheckpoint(now, run.Run.WorkerRunID, migrationResult, queues, status)
	if err != nil {
		return workers.RunResult{}, err
	}
	tickPolicy, err := workers.ParseTickPolicy(run.Instance.TickPolicyJSON)
	if err != nil {
		return workers.RunResult{}, err
	}

	return workers.RunResult{
		Status:        workers.RunStatusSucceeded,
		ResultSummary: resultSummary,
		Counters: map[string]int64{
			"database_checks":           1,
			"tables_checked":            int64(len(largestTables)),
			"active_worker_runs":        queues.ActiveWorkerRuns,
			"recent_failed_worker_runs": queues.RecentFailedWorkerRuns,
			"queued_jobs":               queues.QueuedJobs,
			"running_jobs":              queues.RunningJobs,
		},
		ResourceUsage: json.RawMessage(`{}`),
		CheckpointUpdates: []workers.CheckpointUpdate{
			{
				Key:           "default",
				SchemaVersion: "db_maintenance.checkpoint.v0.2",
				Value:         checkpoint,
				Metadata:      json.RawMessage(`{"schema_version":"worker_checkpoint.metadata.v0.2"}`),
			},
		},
		NextRunAfter: tickPolicy.NextAfter(now),
		Retryable:    false,
	}, nil
}

func parseDBMaintenanceConfig(raw json.RawMessage) (dbMaintenanceConfig, error) {
	normalized, err := workers.JSONObject(raw, "config_json")
	if err != nil {
		return dbMaintenanceConfig{}, err
	}
	raw = normalized
	config := dbMaintenanceConfig{
		SchemaVersion:          "db_maintenance.config.v0.2",
		Mode:                   "light",
		CheckMigrations:        true,
		CheckTableSizes:        true,
		CheckQueueDepths:       true,
		LargeTableWarningBytes: defaultLargeTableWarningBytes,
		MaxRuntimeMS:           30000,
	}
	if len(raw) > 0 && strings.TrimSpace(string(raw)) != "{}" {
		if err := json.Unmarshal(raw, &config); err != nil {
			return dbMaintenanceConfig{}, fmt.Errorf("%w: config_json is invalid JSON: %w", workers.ErrInvalid, err)
		}
	}
	config.Mode = strings.TrimSpace(config.Mode)
	if config.Mode == "" {
		config.Mode = "light"
	}
	if config.Mode != "light" {
		return dbMaintenanceConfig{}, fmt.Errorf("%w: db_maintenance mode %q is not supported", workers.ErrInvalid, config.Mode)
	}
	if config.LargeTableWarningBytes <= 0 {
		config.LargeTableWarningBytes = defaultLargeTableWarningBytes
	}
	return config, nil
}

func (r DBMaintenanceRuntime) collectTableSizes(ctx context.Context) ([]tableSizeSummary, error) {
	rows, err := r.DB.QueryContext(ctx, `
		SELECT n.nspname, c.relname, pg_total_relation_size(c.oid) AS size_bytes
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname IN ('capabilities', 'events', 'jobs', 'search', 'sync', 'modules', 'workers', 'maintenance')
		  AND c.relkind IN ('r', 'p', 'm')
		ORDER BY size_bytes DESC, n.nspname ASC, c.relname ASC
		LIMIT 20
	`)
	if err != nil {
		return nil, fmt.Errorf("collect table sizes: %w", err)
	}
	defer rows.Close()

	var out []tableSizeSummary
	for rows.Next() {
		var item tableSizeSummary
		if err := rows.Scan(&item.Schema, &item.Table, &item.Bytes); err != nil {
			return nil, err
		}
		item.Qualified = item.Schema + "." + item.Table
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r DBMaintenanceRuntime) collectQueueCounts(ctx context.Context) (queueCounts, error) {
	var counts queueCounts
	if err := r.DB.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM workers.worker_instances
		WHERE current_run_id IS NOT NULL
	`).Scan(&counts.ActiveWorkerRuns); err != nil {
		return queueCounts{}, fmt.Errorf("count active worker runs: %w", err)
	}
	if err := r.DB.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM workers.worker_runs
		WHERE run_status = 'failed'
		  AND started_at >= now() - interval '1 hour'
	`).Scan(&counts.RecentFailedWorkerRuns); err != nil {
		return queueCounts{}, fmt.Errorf("count recent failed worker runs: %w", err)
	}
	if err := r.DB.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM jobs.jobs
		WHERE status = 'queued'
	`).Scan(&counts.QueuedJobs); err != nil {
		return queueCounts{}, fmt.Errorf("count queued jobs: %w", err)
	}
	if err := r.DB.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM jobs.jobs
		WHERE status = 'running'
	`).Scan(&counts.RunningJobs); err != nil {
		return queueCounts{}, fmt.Errorf("count running jobs: %w", err)
	}
	return counts, nil
}

func (r DBMaintenanceRuntime) recordMigrationFinding(ctx context.Context, run workers.RunContext, result migrations.Result) error {
	key := run.Instance.WorkerKey + ":migration_not_current:database:migrations"
	if result.Status == "ok" {
		_, err := r.Maintenance.ResolveFinding(ctx, key, "Migrations are current.")
		if errors.Is(err, maintenance.ErrNotFound) {
			return nil
		}
		return err
	}

	severity := maintenance.SeverityWarning
	if result.Status == "unhealthy" {
		severity = maintenance.SeverityCritical
	}
	details, err := workers.JSONObject(mustJSON(map[string]any{
		"schema_version":   "db_maintenance.finding.v0.2",
		"migration_status": result.Status,
		"current_version":  result.CurrentVersion,
		"latest_version":   result.LatestVersion,
		"pending":          result.Pending,
		"detail":           result.Detail,
		"error":            result.Error,
	}), "details_json")
	if err != nil {
		return err
	}
	_, _, err = r.Maintenance.UpsertFinding(ctx, maintenance.UpsertFindingInput{
		FindingKey:       key,
		WorkerInstanceID: run.Instance.WorkerInstanceID,
		WorkerRunID:      run.Run.WorkerRunID,
		FindingKind:      "migration_not_current",
		Severity:         severity,
		SubjectKind:      "database",
		SubjectID:        "migrations",
		Summary:          "Database migrations are not current.",
		DetailsJSON:      details,
		Metadata:         json.RawMessage(`{"schema_version":"maintenance_finding.metadata.v0.2","source":"db_maintenance"}`),
	})
	return err
}

func (r DBMaintenanceRuntime) recordLargeTableFindings(ctx context.Context, run workers.RunContext, tables []tableSizeSummary, threshold int64) error {
	for _, table := range tables {
		if table.Bytes < threshold {
			continue
		}
		details, err := workers.JSONObject(mustJSON(map[string]any{
			"schema_version": "db_maintenance.finding.v0.2",
			"schema":         table.Schema,
			"table":          table.Table,
			"qualified":      table.Qualified,
			"bytes":          table.Bytes,
			"threshold":      threshold,
		}), "details_json")
		if err != nil {
			return err
		}
		_, _, err = r.Maintenance.UpsertFinding(ctx, maintenance.UpsertFindingInput{
			FindingKey:       run.Instance.WorkerKey + ":large_table:table:" + table.Qualified,
			WorkerInstanceID: run.Instance.WorkerInstanceID,
			WorkerRunID:      run.Run.WorkerRunID,
			FindingKind:      "large_table",
			Severity:         maintenance.SeverityWarning,
			SubjectKind:      "table",
			SubjectID:        table.Qualified,
			Summary:          "Database table is above the configured size warning threshold.",
			DetailsJSON:      details,
			Metadata:         json.RawMessage(`{"schema_version":"maintenance_finding.metadata.v0.2","source":"db_maintenance"}`),
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func dbMaintenanceStatus(result migrations.Result) string {
	switch result.Status {
	case "", "not_checked", "ok":
		return "ok"
	case "degraded":
		return "warning"
	default:
		return "critical"
	}
}

func dbMaintenanceSummary(result migrations.Result, tables []tableSizeSummary, queues queueCounts, status string) (json.RawMessage, error) {
	raw, err := json.Marshal(map[string]any{
		"schema_version":   "db_maintenance.result.v0.2",
		"status":           status,
		"migration_status": result.Status,
		"current_version":  result.CurrentVersion,
		"latest_version":   result.LatestVersion,
		"pending":          result.Pending,
		"largest_tables":   tables,
		"queue_counts":     queues,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal db maintenance summary: %w", err)
	}
	return workers.JSONObject(raw, "result_summary")
}

func dbMaintenanceCheckpoint(now time.Time, runID string, result migrations.Result, queues queueCounts, status string) (json.RawMessage, error) {
	raw, err := json.Marshal(map[string]any{
		"schema_version":      "db_maintenance.checkpoint.v0.2",
		"last_db_maintenance": now.Format(time.RFC3339Nano),
		"last_run_id":         runID,
		"status":              status,
		"migration_status":    result.Status,
		"current_version":     result.CurrentVersion,
		"latest_version":      result.LatestVersion,
		"queue_counts":        queues,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal db maintenance checkpoint: %w", err)
	}
	return workers.JSONObject(raw, "checkpoint")
}

func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}
