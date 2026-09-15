package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pressly/goose/v3"
)

const (
	projectRepositoryTestActorID       = "actor_repository_migration"
	projectRepositoryTestProjectA      = "project_repository_a"
	projectRepositoryTestProjectB      = "project_repository_b"
	projectRepositoryTestRegistrationA = "project_contract_registration_repository_a"
	projectRepositoryTestRegistrationB = "project_contract_registration_repository_b"
	projectRepositoryTestRepoID        = "repo_01ARZ3NDEKTSV4RRFFQ69G5FAV"
)

func TestProjectRepositoryPersistenceMigrationPostgres(t *testing.T) {
	dbURL := strings.TrimSpace(os.Getenv("LOOM_TEST_DB_URL"))
	if dbURL == "" {
		t.Skip("LOOM_TEST_DB_URL is not set; recent root migration acceptance requires a dedicated disposable PostgreSQL database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	dir, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("resolve migration directory: %v", err)
	}

	db, err := open(ctx, dbURL)
	if err != nil {
		t.Fatalf("open disposable PostgreSQL database: %v", err)
	}
	requireEmptyDisposableDatabase(t, ctx, db)
	if err := db.Close(); err != nil {
		t.Fatalf("close preflight database connection: %v", err)
	}

	if err := ensureDialect(); err != nil {
		t.Fatalf("configure migration dialect: %v", err)
	}
	db, err = open(ctx, dbURL)
	if err != nil {
		t.Fatalf("reopen disposable PostgreSQL database for migration: %v", err)
	}
	if err := goose.UpToContext(ctx, db, dir, 62); err != nil {
		_ = db.Close()
		t.Fatalf("apply migrations through 00062: %v", err)
	}
	version, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("read migration version after UpTo: %v", err)
	}
	if version != 62 {
		_ = db.Close()
		t.Fatalf("migration version after UpTo = %d, want 62", version)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close migrated database connection: %v", err)
	}

	db, err = open(ctx, dbURL)
	if err != nil {
		t.Fatalf("reopen migrated PostgreSQL database: %v", err)
	}
	defer db.Close()

	insertProjectRepositoryPrerequisites(t, ctx, db)
	auditProjectRepositoryConstraintCatalog(t, ctx, db)
	testCanonicalRepositoryIDConstraint(t, ctx, db)
	testRepositoryMembershipConstraints(t, ctx, db)
	testProjectRepositorySourcePairingAndDeletes(t, ctx, db)
	testSourceHistoryAndObservationConstraints(t, ctx, db)

	if err := goose.UpToContext(ctx, db, dir, 63); err != nil {
		t.Fatalf("apply migration 00063: %v", err)
	}
	version, err = goose.GetDBVersionContext(ctx, db)
	if err != nil {
		t.Fatalf("read migration version after applying 00063: %v", err)
	}
	if version != 63 {
		t.Fatalf("migration version after applying workspace archive lifecycle = %d, want 63", version)
	}
	testWorkspaceArchiveLifecycleConstraints(t, ctx, db)
	if err := goose.DownToContext(ctx, db, dir, 62); err != nil {
		t.Fatalf("roll back migration 00063: %v", err)
	}

	if err := goose.DownToContext(ctx, db, dir, 61); err != nil {
		t.Fatalf("roll back migration 00062: %v", err)
	}
	version, err = goose.GetDBVersionContext(ctx, db)
	if err != nil {
		t.Fatalf("read migration version after rollback: %v", err)
	}
	if version != 61 {
		t.Fatalf("migration version after rollback = %d, want 61", version)
	}

	for _, relation := range []string{
		"projects.repositories",
		"projects.project_repository_memberships",
		"projects.project_repository_sources",
		"projects.project_repository_source_history",
		"projects.project_repository_observations",
	} {
		var got sql.NullString
		if err := db.QueryRowContext(ctx, "SELECT to_regclass($1)::text", relation).Scan(&got); err != nil {
			t.Fatalf("check rolled-back relation %s: %v", relation, err)
		}
		if got.Valid {
			t.Fatalf("relation %s still exists after rolling back migration 00062", relation)
		}
	}

	var parentConstraintCount int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM pg_constraint
		WHERE conname = 'project_contract_registrations_registration_project_key'
	`).Scan(&parentConstraintCount); err != nil {
		t.Fatalf("check rolled-back parent key: %v", err)
	}
	if parentConstraintCount != 0 {
		t.Fatal("project registration composite key still exists after rolling back migration 00062")
	}
}

type workspaceArchivePostgresFixture struct {
	operationID              string
	operationKind            string
	planDigest               string
	inventoryDigest          string
	sourceIdentityJSON       []byte
	sourceParentIdentityJSON []byte
	destinationIdentityJSON  []byte
	destParentIdentityJSON   []byte
	plannedAt                time.Time
	projectionsCommittedAt   time.Time
	completedAt              time.Time
}

func testWorkspaceArchiveLifecycleConstraints(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	archive := insertWorkspaceArchiveOperationFixture(
		t,
		ctx,
		db,
		"workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		"archive",
		migrationDigest("a"),
	)

	for _, test := range []struct {
		name  string
		id    string
		query string
	}{
		{
			name: "reviewed plan semantic projection",
			id:   "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAX",
			query: `
				jsonb_set(
					jsonb_set(plan_json, '{operation_id}', to_jsonb($1::text)),
					'{object_id}',
					'"topic_object_other"'::jsonb
				)
			`,
		},
		{
			name:  "chronological timestamps",
			id:    "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAY",
			query: "jsonb_set(plan_json, '{operation_id}', to_jsonb($1::text))",
		},
		{
			name:  "exact phase timestamps",
			id:    "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAZ",
			query: "jsonb_set(plan_json, '{operation_id}', to_jsonb($1::text))",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			phase := "archive_complete"
			status := "complete"
			intentExpression := "intent_committed_at"
			if test.name == "chronological timestamps" {
				intentExpression = "planned_at - interval '1 second'"
			}
			if test.name == "exact phase timestamps" {
				phase = "archive_planned"
				status = "pending"
			}
			query := fmt.Sprintf(`
				INSERT INTO storage.workspace_archive_operations (
					workspace_archive_operation_id, schema_version, evidence_kind,
					operation_kind, workspace_kind, object_id, slug, phase,
					last_safe_phase, terminal_status, plan_digest, plan_json,
					source_root, source_relative_path, source_identity_json,
					source_parent_identity_json, destination_root,
					destination_relative_path, destination_identity_json,
					destination_parent_identity_json, inventory_digest, actor_id,
					reason, planned_at, intent_committed_at, payload_moved_at,
					projections_committed_at, completed_at, updated_at
				)
				SELECT
					$1, schema_version, evidence_kind, operation_kind, workspace_kind,
					object_id, slug, %s, NULL, %s, plan_digest, %s,
					source_root, source_relative_path, source_identity_json,
					source_parent_identity_json, destination_root,
					destination_relative_path, destination_identity_json,
					destination_parent_identity_json, inventory_digest, actor_id,
					reason, planned_at, %s, payload_moved_at,
					projections_committed_at, completed_at, updated_at
				FROM storage.workspace_archive_operations
				WHERE workspace_archive_operation_id = $2
			`, quoteSQLLiteral(phase), quoteSQLLiteral(status), test.query, intentExpression)
			_, err := db.ExecContext(ctx, query, test.id, archive.operationID)
			assertMigrationSQLState(t, err, "23514")
		})
	}

	badManifest := workspaceArchiveManifestJSON(t, archive, "active", "", "", nil)
	_, err := db.ExecContext(ctx, `
		INSERT INTO storage.workspace_archive_manifests (
			workspace_archive_operation_id, schema_version, evidence_kind,
			workspace_kind, object_id, slug, lifecycle_state, plan_digest,
			restore_plan_digest, inventory_digest, archive_source_identity_json,
			manifest_json, authentication_key_id, authentication_tag, archived_at
		) VALUES (
			$1, 'storage.workspace_archive_manifest.v1', 'physical_workspace_move',
			'topic', 'topic_object_one', 'topic-one', 'archived', $2, NULL, $3,
			$4::jsonb, $5::jsonb, 'workspace-archive-main', $6, $7
		)
	`, archive.operationID, archive.planDigest, archive.inventoryDigest, archive.sourceIdentityJSON, badManifest, "hmac-sha256:"+strings.Repeat("a", 64), archive.completedAt)
	assertMigrationSQLState(t, err, "23514")

	restore := insertWorkspaceArchiveOperationFixture(
		t,
		ctx,
		db,
		"workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAW",
		"restore",
		migrationDigest("b"),
	)
	wrongRestorePlan := migrationDigest("c")
	activeManifest := workspaceArchiveManifestJSON(t, archive, "active", restore.operationID, wrongRestorePlan, &restore.completedAt)
	_, err = db.ExecContext(ctx, `
		INSERT INTO storage.workspace_archive_manifests (
			workspace_archive_operation_id, schema_version, evidence_kind,
			workspace_kind, object_id, slug, lifecycle_state, plan_digest,
			restore_plan_digest, inventory_digest, archive_source_identity_json,
			manifest_json, authentication_key_id, authentication_tag, archived_at,
			restore_operation_id, restore_operation_kind, restored_at
		) VALUES (
			$1, 'storage.workspace_archive_manifest.v1', 'physical_workspace_move',
			'topic', 'topic_object_one', 'topic-one', 'active', $2, $3, $4,
			$5::jsonb, $6::jsonb, 'workspace-archive-main', $7, $8,
			$9, 'restore', $10
		)
	`, archive.operationID, archive.planDigest, wrongRestorePlan, archive.inventoryDigest, archive.sourceIdentityJSON, activeManifest, "hmac-sha256:"+strings.Repeat("a", 64), archive.completedAt, restore.operationID, restore.completedAt)
	assertMigrationSQLState(t, err, "23503")

	_, err = db.ExecContext(ctx, `
		INSERT INTO storage.workspace_lifecycle_events (
			workspace_lifecycle_event_id, schema_version, event_kind,
			workspace_archive_operation_id, operation_kind, workspace_kind,
			object_id, slug, transition, from_state, to_state, source_root,
			source_relative_path, destination_root, destination_relative_path,
			actor_id, reason, occurred_at
		) VALUES (
			'workspace_lifecycle_event_01ARZ3NDEKTSV4RRFFQ69G5FAV',
			'storage.workspace_lifecycle_event.v1', 'workspace.lifecycle_changed',
			$1, 'archive', 'topic', 'topic_object_other', 'topic-one',
			'active_to_archived', 'active', 'archived', 'box', 'Topics/topic-one',
			'storage', 'archive/topics/topic-one/content', $2, $3, $4
		)
	`, archive.operationID, projectRepositoryTestActorID, "archive completed topic", archive.projectionsCommittedAt)
	assertMigrationSQLState(t, err, "23503")
}

func insertWorkspaceArchiveOperationFixture(t *testing.T, ctx context.Context, db *sql.DB, operationID, operationKind, planDigest string) workspaceArchivePostgresFixture {
	t.Helper()
	plannedAt := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	fixture := workspaceArchivePostgresFixture{
		operationID:            operationID,
		operationKind:          operationKind,
		planDigest:             planDigest,
		inventoryDigest:        migrationDigest("d"),
		plannedAt:              plannedAt,
		projectionsCommittedAt: plannedAt.Add(3 * time.Second),
		completedAt:            plannedAt.Add(4 * time.Second),
	}
	fixture.sourceIdentityJSON = marshalMigrationJSON(t, map[string]any{
		"presence": "present", "object_kind": "directory", "device_id": 10,
		"inode": 101, "mode": 2147484136, "size_bytes": 1, "modified_unix_ns": 1700000000000000000,
	})
	fixture.sourceParentIdentityJSON = marshalMigrationJSON(t, map[string]any{
		"presence": "present", "object_kind": "directory", "device_id": 10,
		"inode": 100, "mode": 2147484136, "size_bytes": 1, "modified_unix_ns": 1700000000000000000,
	})
	fixture.destinationIdentityJSON = marshalMigrationJSON(t, map[string]any{"presence": "absent"})
	fixture.destParentIdentityJSON = marshalMigrationJSON(t, map[string]any{
		"presence": "present", "object_kind": "directory", "device_id": 10,
		"inode": 200, "mode": 2147484136, "size_bytes": 1, "modified_unix_ns": 1700000000000000000,
	})
	sourceRoot, sourceRelative := "box", "Topics/topic-one"
	destinationRoot, destinationRelative := "storage", "archive/topics/topic-one/content"
	phase := "archive_complete"
	if operationKind == "restore" {
		sourceRoot, sourceRelative = destinationRoot, destinationRelative
		destinationRoot, destinationRelative = "box", "Topics/topic-one"
		phase = "restore_complete"
	}
	planJSON := marshalMigrationJSON(t, map[string]any{
		"schema_version": "storage.workspace_archive_plan.v1",
		"evidence_kind":  "physical_workspace_move",
		"operation_id":   operationID,
		"operation_kind": operationKind,
		"kind":           "topic",
		"object_id":      "topic_object_one",
		"slug":           "topic-one",
		"source": map[string]any{
			"path":     map[string]any{"root": sourceRoot, "relative_path": sourceRelative, "absolute_path": "/tmp/" + sourceRelative},
			"identity": json.RawMessage(fixture.sourceIdentityJSON), "parent_identity": json.RawMessage(fixture.sourceParentIdentityJSON),
		},
		"destination": map[string]any{
			"path":     map[string]any{"root": destinationRoot, "relative_path": destinationRelative, "absolute_path": "/tmp/" + destinationRelative},
			"identity": json.RawMessage(fixture.destinationIdentityJSON), "parent_identity": json.RawMessage(fixture.destParentIdentityJSON),
		},
		"inventory":   map[string]any{"digest": fixture.inventoryDigest},
		"actor_id":    projectRepositoryTestActorID,
		"reason":      "archive completed topic",
		"planned_at":  plannedAt,
		"plan_digest": planDigest,
	})
	mustExecMigrationSQL(t, ctx, db, `
		INSERT INTO storage.workspace_archive_operations (
			workspace_archive_operation_id, schema_version, evidence_kind,
			operation_kind, workspace_kind, object_id, slug, phase,
			terminal_status, plan_digest, plan_json, source_root,
			source_relative_path, source_identity_json, source_parent_identity_json,
			destination_root, destination_relative_path, destination_identity_json,
			destination_parent_identity_json, inventory_digest, actor_id, reason,
			planned_at, intent_committed_at, payload_moved_at,
			projections_committed_at, completed_at, updated_at
		) VALUES (
			$1, 'storage.workspace_archive_operation.v1', 'physical_workspace_move',
			$2, 'topic', 'topic_object_one', 'topic-one', $3, 'complete', $4,
			$5::jsonb, $6, $7, $8::jsonb, $9::jsonb, $10, $11, $12::jsonb,
			$13::jsonb, $14, $15, $16, $17, $18, $19, $20, $21, $21
		)
	`, operationID, operationKind, phase, planDigest, planJSON, sourceRoot, sourceRelative,
		fixture.sourceIdentityJSON, fixture.sourceParentIdentityJSON, destinationRoot, destinationRelative,
		fixture.destinationIdentityJSON, fixture.destParentIdentityJSON, fixture.inventoryDigest,
		projectRepositoryTestActorID, "archive completed topic", plannedAt, plannedAt.Add(time.Second),
		plannedAt.Add(2*time.Second), fixture.projectionsCommittedAt, fixture.completedAt)
	return fixture
}

func workspaceArchiveManifestJSON(t *testing.T, archive workspaceArchivePostgresFixture, lifecycleState, restoreOperationID, restorePlanDigest string, restoredAt *time.Time) []byte {
	t.Helper()
	payload := map[string]any{
		"schema_version":          "storage.workspace_archive_manifest.v1",
		"evidence_kind":           "physical_workspace_move",
		"archive_operation_id":    archive.operationID,
		"kind":                    "topic",
		"object_id":               "topic_object_one",
		"slug":                    "topic-one",
		"lifecycle_state":         lifecycleState,
		"archive_source_identity": json.RawMessage(archive.sourceIdentityJSON),
		"inventory_digest":        archive.inventoryDigest,
		"plan_digest":             archive.planDigest,
		"archived_at":             archive.completedAt,
		"authentication": map[string]any{
			"algorithm": "hmac-sha256",
			"key_id":    "workspace-archive-main",
			"tag":       "hmac-sha256:" + strings.Repeat("a", 64),
		},
	}
	if restoreOperationID != "" {
		payload["restore_operation_id"] = restoreOperationID
		payload["restore_plan_digest"] = restorePlanDigest
		payload["restored_at"] = restoredAt
	}
	return marshalMigrationJSON(t, payload)
}

func marshalMigrationJSON(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal migration fixture JSON: %v", err)
	}
	return payload
}

func quoteSQLLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func auditProjectRepositoryConstraintCatalog(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()

	for _, expectation := range []struct {
		relation string
		checks   int
		fks      int
	}{
		{"projects.repositories", 3, 1},
		{"projects.project_repository_memberships", 6, 2},
		{"projects.project_repository_sources", 8, 3},
		{"projects.project_repository_source_history", 10, 2},
		{"projects.project_repository_observations", 6, 1},
	} {
		var checks, fks, unvalidated int
		if err := db.QueryRowContext(ctx, `
			SELECT
				count(*) FILTER (WHERE contype = 'c'),
				count(*) FILTER (WHERE contype = 'f'),
				count(*) FILTER (WHERE contype IN ('c', 'f') AND NOT convalidated)
			FROM pg_constraint
			WHERE conrelid = $1::regclass
		`, expectation.relation).Scan(&checks, &fks, &unvalidated); err != nil {
			t.Fatalf("audit constraints for %s: %v", expectation.relation, err)
		}
		if checks != expectation.checks || fks != expectation.fks || unvalidated != 0 {
			t.Fatalf(
				"%s constraints = %d checks, %d foreign keys, %d unvalidated; want %d, %d, 0",
				expectation.relation,
				checks,
				fks,
				unvalidated,
				expectation.checks,
				expectation.fks,
			)
		}
	}

	for _, expectation := range []struct {
		name       string
		constraint string
	}{
		{
			"project_contract_registrations_registration_project_key",
			"UNIQUE (project_contract_registration_id, project_id)",
		},
		{
			"project_repository_sources_registration_project_fkey",
			"FOREIGN KEY (project_contract_registration_id, project_id) REFERENCES projects.project_contract_registrations(project_contract_registration_id, project_id) ON DELETE CASCADE",
		},
	} {
		var definition string
		if err := db.QueryRowContext(ctx, `
			SELECT pg_get_constraintdef(oid)
			FROM pg_constraint
			WHERE conname = $1
		`, expectation.name).Scan(&definition); err != nil {
			t.Fatalf("read constraint %s: %v", expectation.name, err)
		}
		if definition != expectation.constraint {
			t.Fatalf("constraint %s = %q, want %q", expectation.name, definition, expectation.constraint)
		}
	}
}

func requireEmptyDisposableDatabase(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()

	var databaseName string
	if err := db.QueryRowContext(ctx, "SELECT current_database()").Scan(&databaseName); err != nil {
		t.Fatalf("read disposable database name: %v", err)
	}
	if databaseName == "postgres" || databaseName == "template0" || databaseName == "template1" {
		t.Fatalf("LOOM_TEST_DB_URL points to reserved database %q, not a dedicated disposable database", databaseName)
	}

	var userRelationCount int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM pg_class AS c
		JOIN pg_namespace AS n ON n.oid = c.relnamespace
		WHERE n.nspname NOT IN ('pg_catalog', 'information_schema')
		  AND n.nspname !~ '^pg_toast'
		  AND c.relkind IN ('r', 'p', 'v', 'm', 'S', 'f')
	`).Scan(&userRelationCount); err != nil {
		t.Fatalf("inspect disposable database: %v", err)
	}
	if userRelationCount != 0 {
		t.Fatalf("LOOM_TEST_DB_URL database %q is not empty (%d user relations); refusing migration acceptance against a non-disposable database", databaseName, userRelationCount)
	}
}

func insertProjectRepositoryPrerequisites(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	mustExecMigrationSQL(t, ctx, db, `
		INSERT INTO identity.actors (actor_id, actor_key, display_name, actor_kind, status)
		VALUES ($1, 'repository-migration', 'Repository migration', 'agent', 'active')
	`, projectRepositoryTestActorID)

	for _, fixture := range []struct {
		scopeID   string
		scopeKey  string
		slug      string
		projectID string
		name      string
	}{
		{"scope_repository_a", "repository-a", "repository-a", projectRepositoryTestProjectA, "Repository A"},
		{"scope_repository_b", "repository-b", "repository-b", projectRepositoryTestProjectB, "Repository B"},
	} {
		mustExecMigrationSQL(t, ctx, db, `
			INSERT INTO scopes.scopes (scope_id, scope_type, scope_key, slug, display_name, status)
			VALUES ($1, 'project', $2, $3, $4, 'active')
		`, fixture.scopeID, fixture.scopeKey, fixture.slug, fixture.name)
		mustExecMigrationSQL(t, ctx, db, `
			INSERT INTO projects.projects (
				project_id, project_scope_id, slug, name, owner_actor_id, created_by_actor_id, status
			) VALUES ($1, $2, $3, $4, $5, $5, 'active')
		`, fixture.projectID, fixture.scopeID, fixture.slug, fixture.name, projectRepositoryTestActorID)
	}

	for _, fixture := range []struct {
		registrationID string
		projectID      string
		root           string
	}{
		{projectRepositoryTestRegistrationA, projectRepositoryTestProjectA, "/tmp/repository-a"},
		{projectRepositoryTestRegistrationB, projectRepositoryTestProjectB, "/tmp/repository-b"},
	} {
		mustExecMigrationSQL(t, ctx, db, `
			INSERT INTO projects.project_contract_registrations (
				project_contract_registration_id, project_id, project_root, contract_path,
				contract_hash, contract_schema_version, contract_json, validation_report_json,
				registration_plan_json, registration_status, activation_status,
				last_registered_by_actor_id
			) VALUES (
				$1, $2, $3, '.project/project.yaml', $4, 'project.contract.v0.4',
				'{}'::jsonb, '{}'::jsonb, '{}'::jsonb, 'registered', 'base_active', $5
			)
		`, fixture.registrationID, fixture.projectID, fixture.root, migrationDigest("a"), projectRepositoryTestActorID)
	}
}

func testCanonicalRepositoryIDConstraint(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	mustExecMigrationSQL(t, ctx, db, `
		INSERT INTO projects.repositories (repository_id, owning_project_id, lifecycle_status)
		VALUES ($1, $2, 'active')
	`, projectRepositoryTestRepoID, projectRepositoryTestProjectA)

	for _, test := range []struct {
		name string
		id   string
	}{
		{"wildcard bypass", "repoX01ARZ3NDEKTSV4RRFFQ69G5FAV"},
		{"malformed alphabet", "repo_01ARZ3NDEKTSV4RRFFQ69G5FAI"},
		{"lowercase", "repo_01arz3ndektsv4rrffq69g5fav"},
		{"wrong length", "repo_01ARZ3NDEKTSV4RRFFQ69G5FA"},
		{"invalid suffix", projectRepositoryTestRepoID + "_extra"},
		{"overflowing ULID", "repo_81ARZ3NDEKTSV4RRFFQ69G5FAV"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := db.ExecContext(ctx, `
				INSERT INTO projects.repositories (repository_id, owning_project_id, lifecycle_status)
				VALUES ($1, $2, 'active')
			`, test.id, projectRepositoryTestProjectA)
			assertMigrationSQLState(t, err, "23514")
		})
	}

	_, err := db.ExecContext(ctx, `
		INSERT INTO projects.repositories (repository_id, owning_project_id, lifecycle_status)
		VALUES ('repo_01ARZ3NDEKTSV4RRFFQ69G5FAW', 'project_missing', 'active')
	`)
	assertMigrationSQLState(t, err, "23503")
	mustExecMigrationSQL(t, ctx, db, `
		INSERT INTO projects.repositories (repository_id, owning_project_id, lifecycle_status)
		VALUES ('repo_01ARZ3NDEKTSV4RRFFQ69G5FAW', $1, 'active')
	`, projectRepositoryTestProjectA)
}

func testRepositoryMembershipConstraints(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	mustExecMigrationSQL(t, ctx, db, `
		INSERT INTO projects.project_repository_memberships (
			project_id, repository_id, repository_owner_project_id,
			member_key, member_path, role, lifecycle_status
		) VALUES ($1, $2, $1, 'primary', '.', 'primary', 'active')
	`, projectRepositoryTestProjectA, projectRepositoryTestRepoID)

	_, err := db.ExecContext(ctx, `
		INSERT INTO projects.project_repository_memberships (
			project_id, repository_id, repository_owner_project_id,
			member_key, member_path, role, lifecycle_status
		) VALUES ($1, $2, $1, 'wrong-owner', 'wrong-owner', 'primary', 'active')
	`, projectRepositoryTestProjectB, "repo_01ARZ3NDEKTSV4RRFFQ69G5FAW")
	assertMigrationSQLState(t, err, "23503")

	_, err = db.ExecContext(ctx, `
		INSERT INTO projects.project_repository_memberships (
			project_id, repository_id, repository_owner_project_id,
			member_key, member_path, role, lifecycle_status
		) VALUES ($1, $2, $3, 'bad-reference', 'bad-reference', 'reference', 'active')
	`, projectRepositoryTestProjectA, projectRepositoryTestRepoID, projectRepositoryTestProjectA)
	assertMigrationSQLState(t, err, "23514")
}

func testProjectRepositorySourcePairingAndDeletes(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	_, err := insertProjectRepositorySource(ctx, db, projectRepositoryTestProjectB, projectRepositoryTestRegistrationA)
	assertMigrationSQLState(t, err, "23503")

	mustInsertProjectRepositorySource(t, ctx, db, projectRepositoryTestProjectA, projectRepositoryTestRegistrationA)
	mustExecMigrationSQL(t, ctx, db, `
		DELETE FROM projects.project_contract_registrations
		WHERE project_contract_registration_id = $1
	`, projectRepositoryTestRegistrationA)
	assertMigrationRowCount(t, ctx, db, "projects.project_repository_sources", projectRepositoryTestProjectA, 0)

	mustInsertProjectRepositorySource(t, ctx, db, projectRepositoryTestProjectB, projectRepositoryTestRegistrationB)
	mustExecMigrationSQL(t, ctx, db, `DELETE FROM projects.projects WHERE project_id = $1`, projectRepositoryTestProjectB)
	assertMigrationRowCount(t, ctx, db, "projects.project_repository_sources", projectRepositoryTestProjectB, 0)
}

func testSourceHistoryAndObservationConstraints(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	mustExecMigrationSQL(t, ctx, db, `
		INSERT INTO projects.project_repository_source_history (
			project_id, project_contract_schema_version, repos_contract_schema_version,
			project_root, project_contract_path, repos_contract_path,
			project_contract_digest, repos_contract_digest, semantic_digest, location_digest,
			source_snapshot_json, source_revision, source_change_kind, accepted_by_actor_id
		) VALUES (
			$1, 'project.contract.v0.4', 'repos.contract.v0.4', '/tmp/repository-a',
			'.project/project.yaml', '.project/repos.yaml', $2, $2, $2, $2,
			'{}'::jsonb, 1, 'first_registration', $3
		)
	`, projectRepositoryTestProjectA, migrationDigest("b"), projectRepositoryTestActorID)

	_, err := db.ExecContext(ctx, `
		INSERT INTO projects.project_repository_source_history (
			project_id, project_contract_schema_version, repos_contract_schema_version,
			project_root, project_contract_path, repos_contract_path,
			project_contract_digest, repos_contract_digest, semantic_digest, location_digest,
			source_snapshot_json, source_revision, source_change_kind, accepted_by_actor_id
		) VALUES (
			$1, 'project.contract.v0.4', 'repos.contract.v0.4', '/tmp/repository-a',
			'.project/project.yaml', '.project/repos.yaml', $2, $2, $2, $2,
			'{}'::jsonb, 2, 'project_archived', $3
		)
	`, projectRepositoryTestProjectA, migrationDigest("c"), projectRepositoryTestActorID)
	assertMigrationSQLState(t, err, "23514")

	mustExecMigrationSQL(t, ctx, db, `
		INSERT INTO projects.project_repository_observations (
			project_id, repository_id, source_binding_digest, observation_posture
		) VALUES ($1, $2, $3, 'not_observed')
	`, projectRepositoryTestProjectA, projectRepositoryTestRepoID, migrationDigest("d"))

	_, err = db.ExecContext(ctx, `
		UPDATE projects.project_repository_observations
		SET observed_at = now()
		WHERE project_id = $1 AND repository_id = $2
	`, projectRepositoryTestProjectA, projectRepositoryTestRepoID)
	assertMigrationSQLState(t, err, "23514")

	_, err = db.ExecContext(ctx, `
		INSERT INTO projects.project_repository_observations (
			project_id, repository_id, source_binding_digest, observation_posture
		) VALUES ($1, 'repo_01ARZ3NDEKTSV4RRFFQ69G5FAX', $2, 'not_observed')
	`, projectRepositoryTestProjectA, migrationDigest("e"))
	assertMigrationSQLState(t, err, "23503")
}

func insertProjectRepositorySource(ctx context.Context, db *sql.DB, projectID, registrationID string) (sql.Result, error) {
	return db.ExecContext(ctx, `
		INSERT INTO projects.project_repository_sources (
			project_id, project_contract_registration_id,
			project_contract_schema_version, repos_contract_schema_version,
			project_root, project_contract_path, repos_contract_path,
			project_contract_digest, repos_contract_digest, semantic_digest, location_digest,
			source_snapshot_json, source_revision, registered_by_actor_id
		) VALUES (
			$1, $2, 'project.contract.v0.4', 'repos.contract.v0.4', '/tmp/repository',
			'.project/project.yaml', '.project/repos.yaml', $3, $3, $3, $3,
			'{}'::jsonb, 1, $4
		)
	`, projectID, registrationID, migrationDigest("f"), projectRepositoryTestActorID)
}

func mustInsertProjectRepositorySource(t *testing.T, ctx context.Context, db *sql.DB, projectID, registrationID string) {
	t.Helper()
	if _, err := insertProjectRepositorySource(ctx, db, projectID, registrationID); err != nil {
		t.Fatalf("insert valid project repository source: %v", err)
	}
}

func assertMigrationRowCount(t *testing.T, ctx context.Context, db *sql.DB, table, projectID string, want int) {
	t.Helper()
	query := fmt.Sprintf("SELECT count(*) FROM %s WHERE project_id = $1", table)
	var got int
	if err := db.QueryRowContext(ctx, query, projectID).Scan(&got); err != nil {
		t.Fatalf("count %s rows: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s rows for %s = %d, want %d", table, projectID, got, want)
	}
}

func mustExecMigrationSQL(t *testing.T, ctx context.Context, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(ctx, query, args...); err != nil {
		t.Fatalf("execute migration acceptance fixture: %v", err)
	}
}

func assertMigrationSQLState(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("statement succeeded, want PostgreSQL SQLSTATE %s", want)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("statement error = %T %v, want PostgreSQL SQLSTATE %s", err, err, want)
	}
	if pgErr.Code != want {
		t.Fatalf("statement SQLSTATE = %s (%s), want %s", pgErr.Code, pgErr.ConstraintName, want)
	}
}

func migrationDigest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}
