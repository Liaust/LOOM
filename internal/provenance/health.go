package provenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	HealthSchemaVersion = "loom.provenance.health.v1"
	HealthCountLimit    = int64(1_000_000)
	DefaultBackupMaxAge = 24 * time.Hour

	BackupStateComplete    = "complete"
	BackupStateInterrupted = "interrupted"
	BackupStateMissing     = "missing"
)

var (
	ErrLedgerIntegrity               = errors.New("provenance ledger integrity check failed")
	ErrUnsupportedRecoverySchemaHead = errors.New("unsupported provenance recovery schema head")
	databaseNameRE                   = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	recoveryRelationsBySchemaHead    = map[int][]string{
		6: {
			"candidate_events",
			"candidate_evidence_links",
			"candidate_lineage",
			"candidates",
			"case_events",
			"case_members",
			"evidence_registrations",
			"operation_history",
			"processing_runs",
			"record_events",
			"record_producers",
			"record_sources",
			"records",
			"registration_replays",
			"relationship_events",
			"relationships",
			"resolution_cases",
			"schema_migrations",
			"source_references",
		},
		7: {
			"candidate_events",
			"candidate_evidence_links",
			"candidate_lineage",
			"candidates",
			"case_events",
			"case_members",
			"evidence_registrations",
			"operation_history",
			"processing_runs",
			"project_projection_snapshots",
			"record_events",
			"record_producers",
			"record_sources",
			"records",
			"registration_replays",
			"relationship_events",
			"relationships",
			"repository_projection_snapshots",
			"resolution_cases",
			"schema_migrations",
			"source_references",
		},
	}
	healthCountRelations = []string{
		"candidate_events",
		"candidate_evidence_links",
		"candidates",
		"case_events",
		"evidence_registrations",
		"operation_history",
		"processing_runs",
		"record_events",
		"record_producers",
		"records",
		"registration_replays",
		"relationship_events",
		"relationships",
		"resolution_cases",
		"source_references",
	}
)

// RecoverySnapshot is a content-complete but payload-free description of one
// transactionally consistent ledger state. The digest covers every logical row
// in every provenance-owned relation in deterministic relation/row order.
type RecoverySnapshot struct {
	SchemaHead     int              `json:"schema_head"`
	RelationCounts map[string]int64 `json:"relation_counts"`
	GraphDigest    string           `json:"graph_digest"`
}

type BackupObservation struct {
	State       string
	CompletedAt *time.Time
	VerifiedAt  *time.Time
	MaxAge      time.Duration
}

type DatabaseHealth struct {
	Name         string         `json:"name,omitempty"`
	State        ReadinessState `json:"state"`
	Code         ReadinessCode  `json:"code"`
	AppliedHead  int            `json:"applied_head"`
	PackagedHead int            `json:"packaged_head"`
}

type LifecycleCounts struct {
	Candidates          int64 `json:"candidates"`
	Sources             int64 `json:"sources"`
	Records             int64 `json:"records"`
	Producers           int64 `json:"producers"`
	Relationships       int64 `json:"relationships"`
	Cases               int64 `json:"cases"`
	LifecycleEvents     int64 `json:"lifecycle_events"`
	Operations          int64 `json:"operations"`
	ProcessingRuns      int64 `json:"processing_runs"`
	RegistrationReplays int64 `json:"registration_replays"`
	Truncated           bool  `json:"truncated"`
}

type BackupFreshness struct {
	State         string     `json:"state"`
	Freshness     string     `json:"freshness"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	VerifiedAt    *time.Time `json:"verified_at,omitempty"`
	AgeSeconds    *int64     `json:"age_seconds,omitempty"`
	MaxAgeSeconds int64      `json:"max_age_seconds"`
	RecoveryReady bool       `json:"recovery_ready"`
}

// HealthReport deliberately has no field capable of carrying a semantic body,
// source excerpt, submitted document, credential, or connection string.
type HealthReport struct {
	SchemaVersion string          `json:"schema_version"`
	CapturedAt    time.Time       `json:"captured_at"`
	Database      DatabaseHealth  `json:"database"`
	Counts        LifecycleCounts `json:"lifecycle_counts"`
	Backup        BackupFreshness `json:"backup"`
}

func RecoveryRelations() []string {
	relations, err := RecoveryRelationsForSchemaHead(SchemaHead)
	if err != nil {
		panic(err)
	}
	return relations
}

// RecoveryRelationsForSchemaHead returns the one exact logical recovery
// relation contract embedded for a supported historical schema head. Recovery
// callers must never infer historical coverage by subtracting fields from the
// current relation set.
func RecoveryRelationsForSchemaHead(schemaHead int) ([]string, error) {
	relations, ok := recoveryRelationsBySchemaHead[schemaHead]
	if !ok {
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedRecoverySchemaHead, schemaHead)
	}
	return append([]string(nil), relations...), nil
}

func SnapshotLedger(ctx context.Context, pool *pgxpool.Pool, expectedDatabase string) (RecoverySnapshot, error) {
	return SnapshotLedgerAtHead(ctx, pool, expectedDatabase, SchemaHead)
}

// SnapshotLedgerAtHead authenticates and snapshots a restored ledger against
// the exact embedded schema and relation contract for its declared head. It is
// intentionally separate from current runtime readiness so a valid historical
// package can be proved before the disposable target is migrated.
func SnapshotLedgerAtHead(ctx context.Context, pool *pgxpool.Pool, expectedDatabase string, schemaHead int) (RecoverySnapshot, error) {
	tx, _, err := beginRecoverySnapshotAtHead(ctx, pool, expectedDatabase, schemaHead)
	if err != nil {
		return RecoverySnapshot{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	snapshot, err := snapshotLedgerTxAtHead(ctx, tx, schemaHead)
	if err != nil {
		return RecoverySnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RecoverySnapshot{}, fmt.Errorf("commit provenance recovery snapshot: %w", err)
	}
	return snapshot, nil
}

func beginRecoverySnapshot(ctx context.Context, pool *pgxpool.Pool, expectedDatabase string) (pgx.Tx, MigrationStatus, error) {
	return beginRecoverySnapshotAtHead(ctx, pool, expectedDatabase, SchemaHead)
}

func beginRecoverySnapshotAtHead(ctx context.Context, pool *pgxpool.Pool, expectedDatabase string, schemaHead int) (pgx.Tx, MigrationStatus, error) {
	if pool == nil {
		return nil, MigrationStatus{}, errors.New("provenance recovery snapshot pool is required")
	}
	if _, err := RecoveryRelationsForSchemaHead(schemaHead); err != nil {
		return nil, MigrationStatus{}, err
	}
	expectedDatabase = strings.TrimSpace(expectedDatabase)
	if expectedDatabase == "" || expectedDatabase == "loom_main" || !databaseNameRE.MatchString(expectedDatabase) {
		return nil, MigrationStatus{}, fmt.Errorf("%w: invalid recovery database identity", ErrWrongDatabase)
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, MigrationStatus{}, fmt.Errorf("begin provenance recovery snapshot: %w", err)
	}
	fail := func(err error) (pgx.Tx, MigrationStatus, error) {
		_ = tx.Rollback(context.Background())
		return nil, MigrationStatus{}, err
	}
	if _, err := tx.Exec(ctx, `SET LOCAL TIME ZONE 'UTC'`); err != nil {
		return fail(fmt.Errorf("normalize provenance recovery snapshot timezone: %w", err))
	}

	database, err := currentDatabase(ctx, tx)
	if err != nil {
		return fail(err)
	}
	if database != expectedDatabase || database == "loom_main" {
		return fail(fmt.Errorf("%w: connected=%q expected=%q", ErrWrongDatabase, database, expectedDatabase))
	}
	migrations, err := loadMigrations()
	if err != nil {
		return fail(err)
	}
	status, err := inspectSchemaAtHead(ctx, tx, database, migrations, schemaHead)
	if err != nil {
		return fail(err)
	}
	if !status.Ready || status.AppliedHead != schemaHead {
		return fail(fmt.Errorf("%w: recovery snapshot schema is not ready", ErrSchemaBehind))
	}
	if err := verifyRecoveryRelations(ctx, tx); err != nil {
		return fail(err)
	}
	return tx, status, nil
}

func snapshotLedgerTx(ctx context.Context, tx pgx.Tx) (RecoverySnapshot, error) {
	return snapshotLedgerTxAtHead(ctx, tx, SchemaHead)
}

func snapshotLedgerTxAtHead(ctx context.Context, tx pgx.Tx, schemaHead int) (RecoverySnapshot, error) {
	relations, err := RecoveryRelationsForSchemaHead(schemaHead)
	if err != nil {
		return RecoverySnapshot{}, err
	}
	sort.Strings(relations)
	digest := sha256.New()
	counts := make(map[string]int64, len(relations))
	for _, relation := range relations {
		qualified := pgx.Identifier{"provenance", relation}.Sanitize()
		var count int64
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+qualified).Scan(&count); err != nil {
			return RecoverySnapshot{}, fmt.Errorf("count provenance recovery relation %s: %w", relation, err)
		}
		counts[relation] = count
		writeDigestPart(digest, relation)
		rows, err := tx.Query(ctx, `SELECT to_jsonb(row_value)::text FROM `+qualified+` AS row_value ORDER BY to_jsonb(row_value)::text`)
		if err != nil {
			return RecoverySnapshot{}, fmt.Errorf("read provenance recovery relation %s: %w", relation, err)
		}
		for rows.Next() {
			var row string
			if err := rows.Scan(&row); err != nil {
				rows.Close()
				return RecoverySnapshot{}, fmt.Errorf("scan provenance recovery relation %s: %w", relation, err)
			}
			writeDigestPart(digest, row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return RecoverySnapshot{}, fmt.Errorf("iterate provenance recovery relation %s: %w", relation, err)
		}
		rows.Close()
	}
	return RecoverySnapshot{
		SchemaHead:     schemaHead,
		RelationCounts: counts,
		GraphDigest:    "sha256:" + hex.EncodeToString(digest.Sum(nil)),
	}, nil
}

func CompareRecoverySnapshots(before, after RecoverySnapshot) error {
	if before.SchemaHead != after.SchemaHead {
		return fmt.Errorf("provenance restore schema head mismatch: before=%d after=%d", before.SchemaHead, after.SchemaHead)
	}
	relations, err := RecoveryRelationsForSchemaHead(before.SchemaHead)
	if err != nil {
		return err
	}
	for _, relation := range relations {
		beforeCount, beforeOK := before.RelationCounts[relation]
		afterCount, afterOK := after.RelationCounts[relation]
		if !beforeOK || !afterOK || beforeCount != afterCount {
			return fmt.Errorf("provenance restore relation count mismatch for %s", relation)
		}
	}
	if len(before.RelationCounts) != len(relations) || len(after.RelationCounts) != len(relations) {
		return errors.New("provenance restore snapshot has an incomplete relation set")
	}
	if before.GraphDigest == "" || before.GraphDigest != after.GraphDigest {
		return errors.New("provenance restore graph digest mismatch")
	}
	return nil
}

// healthLedgerCounts verifies database, schema, and relationship integrity in
// one repeatable-read transaction, then reads only the aggregate counts used by
// HealthReport. Semantic rows are never serialized, sorted, or hashed here.
func healthLedgerCounts(ctx context.Context, pool *pgxpool.Pool, expectedDatabase string) (map[string]int64, error) {
	if pool == nil {
		return nil, errors.New("provenance health pool is required")
	}
	expectedDatabase = strings.TrimSpace(expectedDatabase)
	if expectedDatabase == "" || expectedDatabase == "loom_main" || !databaseNameRE.MatchString(expectedDatabase) {
		return nil, fmt.Errorf("%w: invalid health database identity", ErrWrongDatabase)
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("begin provenance health inspection: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SET LOCAL TIME ZONE 'UTC'`); err != nil {
		return nil, fmt.Errorf("normalize provenance health inspection timezone: %w", err)
	}

	database, err := currentDatabase(ctx, tx)
	if err != nil {
		return nil, err
	}
	if database != expectedDatabase || database == "loom_main" {
		return nil, fmt.Errorf("%w: connected=%q expected=%q", ErrWrongDatabase, database, expectedDatabase)
	}
	migrations, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	status, err := inspectSchema(ctx, tx, database, migrations)
	if err != nil {
		return nil, err
	}
	if !status.Ready || status.AppliedHead != SchemaHead {
		return nil, fmt.Errorf("%w: health schema is not ready", ErrSchemaBehind)
	}
	if err := verifyRecoveryRelations(ctx, tx); err != nil {
		return nil, err
	}

	counts := make(map[string]int64, len(healthCountRelations))
	for _, relation := range healthCountRelations {
		qualified := pgx.Identifier{"provenance", relation}.Sanitize()
		var count int64
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+qualified).Scan(&count); err != nil {
			return nil, fmt.Errorf("count provenance health relation %s: %w", relation, err)
		}
		counts[relation] = count
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit provenance health inspection: %w", err)
	}
	return counts, nil
}

func BuildHealthReport(ctx context.Context, pool *pgxpool.Pool, readiness RuntimeReadiness, backup BackupObservation, now time.Time) (HealthReport, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	report := HealthReport{
		SchemaVersion: HealthSchemaVersion,
		CapturedAt:    now,
		Database: DatabaseHealth{
			State:        readiness.State,
			Code:         readiness.Code,
			AppliedHead:  readiness.AppliedHead,
			PackagedHead: readiness.PackagedHead,
		},
		Backup: backupFreshness(backup, now),
	}
	if databaseNameRE.MatchString(readiness.Database) {
		report.Database.Name = readiness.Database
	}
	if readiness.State != ReadinessReady || readiness.Code != ReadinessCodeReady {
		return report, nil
	}
	counts, err := healthLedgerCounts(ctx, pool, readiness.Database)
	if err != nil {
		return HealthReport{}, err
	}
	report.Counts = lifecycleCounts(counts)
	return report, nil
}

func (r *Runtime) Health(ctx context.Context, backup BackupObservation, now time.Time) (HealthReport, error) {
	if r == nil {
		return BuildHealthReport(ctx, nil, RuntimeReadiness{State: ReadinessNotReady, Code: ReadinessCodeInvalidConfiguration, PackagedHead: SchemaHead}, backup, now)
	}
	return BuildHealthReport(ctx, r.pool, r.Readiness(), backup, now)
}

func lifecycleCounts(counts map[string]int64) LifecycleCounts {
	result := LifecycleCounts{}
	result.Candidates, result.Truncated = boundedCount(counts["candidates"], result.Truncated)
	result.Sources, result.Truncated = boundedSum(counts, []string{"source_references", "evidence_registrations", "candidate_evidence_links"}, result.Truncated)
	result.Records, result.Truncated = boundedCount(counts["records"], result.Truncated)
	result.Producers, result.Truncated = boundedCount(counts["record_producers"], result.Truncated)
	result.Relationships, result.Truncated = boundedCount(counts["relationships"], result.Truncated)
	result.Cases, result.Truncated = boundedCount(counts["resolution_cases"], result.Truncated)
	result.LifecycleEvents, result.Truncated = boundedSum(counts, []string{"candidate_events", "record_events", "relationship_events", "case_events"}, result.Truncated)
	result.Operations, result.Truncated = boundedCount(counts["operation_history"], result.Truncated)
	result.ProcessingRuns, result.Truncated = boundedCount(counts["processing_runs"], result.Truncated)
	result.RegistrationReplays, result.Truncated = boundedCount(counts["registration_replays"], result.Truncated)
	return result
}

func boundedCount(value int64, alreadyTruncated bool) (int64, bool) {
	if value < 0 || value > HealthCountLimit {
		return HealthCountLimit, true
	}
	return value, alreadyTruncated
}

func boundedSum(counts map[string]int64, keys []string, alreadyTruncated bool) (int64, bool) {
	total := int64(0)
	truncated := alreadyTruncated
	for _, key := range keys {
		value := counts[key]
		if value < 0 || value > HealthCountLimit-total {
			return HealthCountLimit, true
		}
		total += value
	}
	return total, truncated
}

func backupFreshness(observation BackupObservation, now time.Time) BackupFreshness {
	maxAge := observation.MaxAge
	if maxAge <= 0 {
		maxAge = DefaultBackupMaxAge
	}
	result := BackupFreshness{
		State:         strings.TrimSpace(observation.State),
		Freshness:     "missing",
		MaxAgeSeconds: int64(maxAge / time.Second),
	}
	if result.State == "" {
		result.State = BackupStateMissing
	}
	if observation.CompletedAt != nil {
		value := observation.CompletedAt.UTC()
		result.CompletedAt = &value
	}
	if observation.VerifiedAt != nil {
		value := observation.VerifiedAt.UTC()
		result.VerifiedAt = &value
	}
	if result.State != BackupStateComplete {
		if result.State == BackupStateInterrupted {
			result.Freshness = "interrupted"
		}
		return result
	}
	if result.CompletedAt == nil {
		result.Freshness = "incomplete"
		return result
	}
	if result.VerifiedAt == nil {
		result.Freshness = "unverified"
		return result
	}
	if result.VerifiedAt.Before(*result.CompletedAt) || result.VerifiedAt.After(now) {
		result.Freshness = "invalid"
		return result
	}
	age := now.Sub(*result.CompletedAt)
	if age < 0 {
		result.Freshness = "future"
		return result
	}
	ageSeconds := int64(age / time.Second)
	result.AgeSeconds = &ageSeconds
	if age > maxAge {
		result.Freshness = "stale"
		return result
	}
	result.Freshness = "current"
	result.RecoveryReady = true
	return result
}

func verifyRecoveryRelations(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) error {
	checks := []struct {
		name  string
		query string
	}{
		{"candidate source", `SELECT EXISTS (
			SELECT 1 FROM provenance.candidates c
			WHERE NOT EXISTS (SELECT 1 FROM provenance.source_references s WHERE s.candidate_id = c.id)
			  AND NOT EXISTS (SELECT 1 FROM provenance.candidate_evidence_links l WHERE l.candidate_id = c.id)
		)`},
		{"record source", `SELECT EXISTS (
			SELECT 1 FROM provenance.records r
			WHERE NOT EXISTS (SELECT 1 FROM provenance.record_sources s WHERE s.record_id = r.id)
		)`},
		{"record producer", `SELECT EXISTS (
			SELECT 1 FROM provenance.records r
			WHERE NOT EXISTS (SELECT 1 FROM provenance.record_producers p WHERE p.record_id = r.id)
		)`},
		{"orphan source candidate", `SELECT EXISTS (SELECT 1 FROM provenance.source_references s LEFT JOIN provenance.candidates c ON c.id=s.candidate_id WHERE s.candidate_id IS NOT NULL AND c.id IS NULL)`},
		{"orphan record source", `SELECT EXISTS (SELECT 1 FROM provenance.record_sources s LEFT JOIN provenance.records r ON r.id=s.record_id LEFT JOIN provenance.source_references x ON x.id=s.source_reference_id WHERE r.id IS NULL OR x.id IS NULL)`},
		{"orphan candidate evidence", `SELECT EXISTS (SELECT 1 FROM provenance.candidate_evidence_links l LEFT JOIN provenance.candidates c ON c.id=l.candidate_id LEFT JOIN provenance.source_references s ON s.id=l.source_reference_id LEFT JOIN provenance.operation_history o ON o.id=l.operation_id WHERE c.id IS NULL OR s.id IS NULL OR o.id IS NULL)`},
		{"orphan evidence registration", `SELECT EXISTS (SELECT 1 FROM provenance.evidence_registrations e LEFT JOIN provenance.source_references s ON s.id=e.source_reference_id LEFT JOIN provenance.resolution_cases c ON c.id=e.resolution_case_id WHERE s.id IS NULL OR (e.resolution_case_id IS NOT NULL AND c.id IS NULL))`},
		{"case event evidence", `SELECT EXISTS (
			SELECT 1 FROM provenance.case_events e
			WHERE jsonb_typeof(e.evidence_json) <> 'array'
			   OR EXISTS (
				SELECT 1
				FROM jsonb_array_elements_text(CASE WHEN jsonb_typeof(e.evidence_json) = 'array' THEN e.evidence_json ELSE '[]'::jsonb END) AS cited(source_id)
				LEFT JOIN provenance.source_references s ON s.id::text = cited.source_id
				WHERE s.id IS NULL
			   )
		)`},
		{"relationship evidence", `SELECT EXISTS (
			SELECT 1 FROM provenance.relationships r
			WHERE jsonb_typeof(r.evidence_json) <> 'array'
			   OR EXISTS (
				SELECT 1
				FROM jsonb_array_elements_text(CASE WHEN jsonb_typeof(r.evidence_json) = 'array' THEN r.evidence_json ELSE '[]'::jsonb END) AS cited(source_id)
				LEFT JOIN provenance.source_references s ON s.id::text = cited.source_id
				WHERE s.id IS NULL
			   )
		)`},
	}
	for _, check := range checks {
		var broken bool
		if err := queryer.QueryRow(ctx, check.query).Scan(&broken); err != nil {
			return fmt.Errorf("%w: %s relation unavailable", ErrLedgerIntegrity, check.name)
		}
		if broken {
			return fmt.Errorf("%w: missing %s relation", ErrLedgerIntegrity, check.name)
		}
	}
	return nil
}

func writeDigestPart(target hash.Hash, value string) {
	_, _ = fmt.Fprintf(target, "%d:", len(value))
	_, _ = target.Write([]byte(value))
	_, _ = target.Write([]byte{'\n'})
}
