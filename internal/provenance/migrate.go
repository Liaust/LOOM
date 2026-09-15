package provenance

import (
	"context"
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	SchemaHead                    = 7
	expectedAppendOnlyTriggerType = 1 | 2 | 8 | 16 // ROW | BEFORE | DELETE | UPDATE
	expectedRejectFunctionSource  = `BEGIN
    RAISE EXCEPTION 'append-only table: %.%', TG_TABLE_SCHEMA, TG_TABLE_NAME
        USING ERRCODE = '55000';
END;`
)

var (
	ErrWrongDatabase   = errors.New("provenance migrations target the wrong database")
	ErrSchemaBehind    = errors.New("provenance schema is behind the packaged head")
	ErrSchemaAhead     = errors.New("provenance schema is ahead of the packaged head")
	ErrHistoryMismatch = errors.New("provenance migration history does not match packaged migrations")
	ErrSchemaTampered  = errors.New("provenance schema objects do not match the packaged contract")
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type migration struct {
	Version int
	Name    string
	SHA256  string
	SQL     string
}

type MigrationStatus struct {
	Database        string `json:"database"`
	AppliedHead     int    `json:"applied_head"`
	PackagedHead    int    `json:"packaged_head"`
	AppliedVersions []int  `json:"applied_versions"`
	PendingVersions []int  `json:"pending_versions"`
	Ready           bool   `json:"ready"`
}

// ApplyMigrations applies the embedded provenance history only when the caller
// proves it is connected to the expected dedicated database. The canonical
// loom_main database is rejected even if passed as the expected name.
func ApplyMigrations(ctx context.Context, pool *pgxpool.Pool, expectedDatabase string) (MigrationStatus, error) {
	if pool == nil {
		return MigrationStatus{}, errors.New("provenance migration pool is required")
	}
	expectedDatabase = strings.TrimSpace(expectedDatabase)
	if expectedDatabase == "" {
		return MigrationStatus{}, errors.New("expected provenance database is required")
	}

	migrations, err := loadMigrations()
	if err != nil {
		return MigrationStatus{}, err
	}
	connection, err := pool.Acquire(ctx)
	if err != nil {
		return MigrationStatus{}, fmt.Errorf("acquire provenance migration connection: %w", err)
	}
	defer connection.Release()

	database, err := currentDatabase(ctx, connection)
	if err != nil {
		return MigrationStatus{}, err
	}
	if database == "loom_main" || database != expectedDatabase {
		return MigrationStatus{Database: database, PackagedHead: SchemaHead}, fmt.Errorf(
			"%w: connected=%q expected=%q", ErrWrongDatabase, database, expectedDatabase,
		)
	}

	if _, err := connection.Exec(ctx, `SELECT pg_advisory_lock(hashtext('loom.provenance.migrations'))`); err != nil {
		return MigrationStatus{}, fmt.Errorf("lock provenance migrations: %w", err)
	}
	defer func() {
		_, _ = connection.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtext('loom.provenance.migrations'))`)
	}()

	if err := ensureMigrationLedger(ctx, connection); err != nil {
		return MigrationStatus{}, err
	}
	status, err := inspectSchema(ctx, connection, database, migrations)
	if err != nil && !errors.Is(err, ErrSchemaBehind) {
		return status, err
	}

	applied := make(map[int]bool, len(status.AppliedVersions))
	for _, version := range status.AppliedVersions {
		applied[version] = true
	}
	for _, item := range migrations {
		if applied[item.Version] {
			continue
		}
		tx, beginErr := connection.Begin(ctx)
		if beginErr != nil {
			return MigrationStatus{}, fmt.Errorf("begin provenance migration %d: %w", item.Version, beginErr)
		}
		if _, execErr := tx.Exec(ctx, item.SQL); execErr != nil {
			_ = tx.Rollback(ctx)
			return MigrationStatus{}, fmt.Errorf("apply provenance migration %03d: %w", item.Version, execErr)
		}
		if _, execErr := tx.Exec(ctx, `
			INSERT INTO provenance.schema_migrations(version, name, sha256, applied_at)
			VALUES ($1, $2, $3, $4)
		`, item.Version, item.Name, item.SHA256, time.Now().UTC()); execErr != nil {
			_ = tx.Rollback(ctx)
			return MigrationStatus{}, fmt.Errorf("record provenance migration %03d: %w", item.Version, execErr)
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return MigrationStatus{}, fmt.Errorf("commit provenance migration %03d: %w", item.Version, commitErr)
		}
	}

	status, err = inspectSchema(ctx, connection, database, migrations)
	if err != nil {
		return status, err
	}
	return status, nil
}

// InspectSchema never initializes or repairs storage. It fails closed when the
// connected database, ordered version history, or migration digests differ.
func InspectSchema(ctx context.Context, pool *pgxpool.Pool, expectedDatabase string) (MigrationStatus, error) {
	return InspectSchemaAtHead(ctx, pool, expectedDatabase, SchemaHead)
}

// InspectSchemaAtHead proves one exact embedded historical recovery contract
// without initializing or advancing it. Only explicitly versioned recovery
// heads are accepted; an otherwise valid newer database is ahead of an older
// package and cannot stand in for its at-head verification.
func InspectSchemaAtHead(ctx context.Context, pool *pgxpool.Pool, expectedDatabase string, schemaHead int) (MigrationStatus, error) {
	if pool == nil {
		return MigrationStatus{}, errors.New("provenance migration pool is required")
	}
	if _, err := RecoveryRelationsForSchemaHead(schemaHead); err != nil {
		return MigrationStatus{}, err
	}
	migrations, err := loadMigrations()
	if err != nil {
		return MigrationStatus{}, err
	}
	connection, err := pool.Acquire(ctx)
	if err != nil {
		return MigrationStatus{}, fmt.Errorf("acquire provenance status connection: %w", err)
	}
	defer connection.Release()

	database, err := currentDatabase(ctx, connection)
	if err != nil {
		return MigrationStatus{}, err
	}
	if database == "loom_main" || database != strings.TrimSpace(expectedDatabase) {
		return MigrationStatus{Database: database, PackagedHead: SchemaHead}, fmt.Errorf(
			"%w: connected=%q expected=%q", ErrWrongDatabase, database, expectedDatabase,
		)
	}
	return inspectSchemaAtHead(ctx, connection, database, migrations, schemaHead)
}

func ensureMigrationLedger(ctx context.Context, connection *pgxpool.Conn) error {
	tx, err := connection.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin provenance migration ledger: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS provenance;
		CREATE TABLE IF NOT EXISTS provenance.schema_migrations (
			version INTEGER PRIMARY KEY CHECK (version > 0),
			name TEXT NOT NULL CHECK (btrim(name) <> ''),
			sha256 TEXT NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
			applied_at TIMESTAMPTZ NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("initialize provenance migration ledger: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit provenance migration ledger: %w", err)
	}
	return nil
}

type schemaQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func inspectSchema(ctx context.Context, querier schemaQuerier, database string, migrations []migration) (MigrationStatus, error) {
	return inspectSchemaAtHead(ctx, querier, database, migrations, SchemaHead)
}

func inspectSchemaAtHead(ctx context.Context, querier schemaQuerier, database string, migrations []migration, schemaHead int) (MigrationStatus, error) {
	status := MigrationStatus{Database: database, PackagedHead: SchemaHead}
	if _, err := RecoveryRelationsForSchemaHead(schemaHead); err != nil {
		return status, err
	}
	var exists bool
	if err := querier.QueryRow(ctx, `SELECT to_regclass('provenance.schema_migrations') IS NOT NULL`).Scan(&exists); err != nil {
		return status, fmt.Errorf("inspect provenance migration ledger: %w", err)
	}
	if !exists {
		for version := 1; version <= SchemaHead; version++ {
			status.PendingVersions = append(status.PendingVersions, version)
		}
		return status, ErrSchemaBehind
	}

	rows, err := querier.Query(ctx, `
		SELECT version, name, sha256
		FROM provenance.schema_migrations
		ORDER BY version
	`)
	if err != nil {
		return status, fmt.Errorf("read provenance migration history: %w", err)
	}
	defer rows.Close()

	packaged := make(map[int]migration, len(migrations))
	for _, item := range migrations {
		packaged[item.Version] = item
	}
	for rows.Next() {
		var version int
		var name, digest string
		if err := rows.Scan(&version, &name, &digest); err != nil {
			return status, fmt.Errorf("scan provenance migration history: %w", err)
		}
		status.AppliedVersions = append(status.AppliedVersions, version)
		if version > schemaHead {
			status.AppliedHead = version
			return status, fmt.Errorf("%w: applied version %d", ErrSchemaAhead, version)
		}
		item, ok := packaged[version]
		if !ok || item.Name != name || item.SHA256 != digest {
			return status, fmt.Errorf("%w: version %d", ErrHistoryMismatch, version)
		}
	}
	if err := rows.Err(); err != nil {
		return status, fmt.Errorf("iterate provenance migration history: %w", err)
	}

	for index, version := range status.AppliedVersions {
		if version != index+1 {
			return status, fmt.Errorf("%w: non-contiguous version %d after %d", ErrHistoryMismatch, version, index)
		}
	}
	if len(status.AppliedVersions) > 0 {
		status.AppliedHead = status.AppliedVersions[len(status.AppliedVersions)-1]
	}
	for version := status.AppliedHead + 1; version <= SchemaHead; version++ {
		status.PendingVersions = append(status.PendingVersions, version)
	}
	status.Ready = status.AppliedHead == schemaHead
	if !status.Ready {
		return status, ErrSchemaBehind
	}
	if err := verifySchemaContractAtHead(ctx, querier, schemaHead); err != nil {
		status.Ready = false
		return status, err
	}
	return status, nil
}

func verifySchemaContract(ctx context.Context, querier schemaQuerier) error {
	return verifySchemaContractAtHead(ctx, querier, SchemaHead)
}

func verifySchemaContractAtHead(ctx context.Context, querier schemaQuerier, schemaHead int) error {
	expectedTables, err := RecoveryRelationsForSchemaHead(schemaHead)
	if err != nil {
		return err
	}
	sort.Strings(expectedTables)
	expectedImmutableTables := make([]string, 0, len(expectedTables)-2)
	for _, table := range expectedTables {
		if table != "processing_runs" && table != "schema_migrations" {
			expectedImmutableTables = append(expectedImmutableTables, table)
		}
	}
	tables, err := queryNames(ctx, querier, `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = 'provenance' AND table_type = 'BASE TABLE'
		ORDER BY table_name
	`)
	if err != nil {
		return fmt.Errorf("inspect provenance tables: %w", err)
	}
	if strings.Join(tables, "\x00") != strings.Join(expectedTables, "\x00") {
		return fmt.Errorf("%w: tables=%v", ErrSchemaTampered, tables)
	}

	functionOID, err := verifyRejectFunctionContract(ctx, querier)
	if err != nil {
		return err
	}
	rows, err := querier.Query(ctx, `
		SELECT class.relname, trigger.tgenabled::text, trigger.tgtype::integer,
		       trigger.tgfoid::bigint, trigger.tgnargs::integer,
		       trigger.tgqual IS NULL, trigger.tgconstraint = 0,
		       NOT trigger.tgdeferrable, NOT trigger.tginitdeferred,
		       trigger.tgoldtable IS NULL, trigger.tgnewtable IS NULL,
		       trigger.tgattr::text = ''
		FROM pg_catalog.pg_trigger AS trigger
		JOIN pg_catalog.pg_class AS class ON class.oid = trigger.tgrelid
		JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = class.relnamespace
		WHERE namespace.nspname = 'provenance'
		  AND trigger.tgname = 'reject_ledger_mutation'
		  AND NOT trigger.tgisinternal
		ORDER BY class.relname
	`)
	if err != nil {
		return fmt.Errorf("inspect provenance append-only triggers: %w", err)
	}
	defer rows.Close()
	var immutableTables []string
	for rows.Next() {
		var table, enabled string
		var triggerType, triggerArgs int
		var triggerFunctionOID int64
		var noWhen, ordinary, notDeferrable, notInitiallyDeferred, noOldTable, noNewTable, allUpdateColumns bool
		if err := rows.Scan(&table, &enabled, &triggerType, &triggerFunctionOID, &triggerArgs,
			&noWhen, &ordinary, &notDeferrable, &notInitiallyDeferred, &noOldTable, &noNewTable,
			&allUpdateColumns); err != nil {
			return fmt.Errorf("scan provenance append-only trigger contract: %w", err)
		}
		immutableTables = append(immutableTables, table)
		if enabled != "O" || triggerType != expectedAppendOnlyTriggerType ||
			triggerFunctionOID != functionOID || triggerArgs != 0 || !noWhen || !ordinary ||
			!notDeferrable || !notInitiallyDeferred || !noOldTable || !noNewTable || !allUpdateColumns {
			return fmt.Errorf(
				"%w: append-only trigger on %s has enabled=%q type=%d function_oid=%d args=%d when_absent=%t ordinary=%t not_deferrable=%t not_initially_deferred=%t transition_tables_absent=%t all_update_columns=%t",
				ErrSchemaTampered, table, enabled, triggerType, triggerFunctionOID, triggerArgs,
				noWhen, ordinary, notDeferrable, notInitiallyDeferred, noOldTable && noNewTable,
				allUpdateColumns,
			)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate provenance append-only trigger contract: %w", err)
	}
	if strings.Join(immutableTables, "\x00") != strings.Join(expectedImmutableTables, "\x00") {
		return fmt.Errorf("%w: immutable tables=%v", ErrSchemaTampered, immutableTables)
	}
	return nil
}

func verifyRejectFunctionContract(ctx context.Context, querier schemaQuerier) (int64, error) {
	var oid int64
	var language, identityArguments, result, kind, volatility, parallel, source string
	var securityInvoker, notLeakproof, notStrict, noConfiguration bool
	err := querier.QueryRow(ctx, `
		SELECT procedure.oid::bigint, language.lanname,
		       pg_catalog.pg_get_function_identity_arguments(procedure.oid),
		       pg_catalog.pg_get_function_result(procedure.oid),
		       procedure.prokind::text, procedure.provolatile::text,
		       procedure.proparallel::text, NOT procedure.prosecdef,
		       NOT procedure.proleakproof, NOT procedure.proisstrict,
		       procedure.proconfig IS NULL, procedure.prosrc
		FROM pg_catalog.pg_proc AS procedure
		JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = procedure.pronamespace
		JOIN pg_catalog.pg_language AS language ON language.oid = procedure.prolang
		WHERE namespace.nspname = 'provenance'
		  AND procedure.proname = 'reject_ledger_mutation'
		  AND procedure.pronargs = 0
	`).Scan(&oid, &language, &identityArguments, &result, &kind, &volatility, &parallel,
		&securityInvoker, &notLeakproof, &notStrict, &noConfiguration, &source)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("%w: provenance.reject_ledger_mutation() is missing", ErrSchemaTampered)
	}
	if err != nil {
		return 0, fmt.Errorf("inspect provenance reject function: %w", err)
	}
	if language != "plpgsql" || identityArguments != "" || result != "trigger" ||
		kind != "f" || volatility != "v" || parallel != "u" || !securityInvoker ||
		!notLeakproof || !notStrict || !noConfiguration || strings.TrimSpace(source) != expectedRejectFunctionSource {
		return 0, fmt.Errorf(
			"%w: reject function oid=%d language=%q arguments=%q result=%q kind=%q volatility=%q parallel=%q security_invoker=%t not_leakproof=%t not_strict=%t no_configuration=%t source_matches=%t",
			ErrSchemaTampered, oid, language, identityArguments, result, kind, volatility, parallel,
			securityInvoker, notLeakproof, notStrict, noConfiguration,
			strings.TrimSpace(source) == expectedRejectFunctionSource,
		)
	}
	return oid, nil
}

func queryNames(ctx context.Context, querier schemaQuerier, query string) ([]string, error) {
	rows, err := querier.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		result = append(result, name)
	}
	return result, rows.Err()
}

func currentDatabase(ctx context.Context, querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (string, error) {
	var database string
	if err := querier.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		return "", fmt.Errorf("read current provenance database: %w", err)
	}
	return database, nil
}

func loadMigrations() ([]migration, error) {
	paths, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return nil, fmt.Errorf("list embedded provenance migrations: %w", err)
	}
	sort.Strings(paths)
	result := make([]migration, 0, len(paths))
	for _, path := range paths {
		name := filepath.Base(path)
		prefix, _, ok := strings.Cut(name, "_")
		if !ok {
			return nil, fmt.Errorf("invalid provenance migration name %q", name)
		}
		version, err := strconv.Atoi(prefix)
		if err != nil {
			return nil, fmt.Errorf("invalid provenance migration version %q: %w", prefix, err)
		}
		contents, err := migrationFiles.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read embedded provenance migration %q: %w", name, err)
		}
		digest := fmt.Sprintf("%x", sha256.Sum256(contents))
		result = append(result, migration{Version: version, Name: name, SHA256: digest, SQL: string(contents)})
	}
	if len(result) != SchemaHead {
		return nil, fmt.Errorf("embedded provenance migration count = %d, want %d", len(result), SchemaHead)
	}
	for index, item := range result {
		if item.Version != index+1 {
			return nil, fmt.Errorf("embedded provenance migration history is not contiguous at %q", item.Name)
		}
	}
	return result, nil
}
