package bootstrap

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"loom.local/loom/internal/db"
	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/nodeprofiles"
)

func TestEnsureProductionBootstrapIntegration(t *testing.T) {
	dbURL := os.Getenv("LOOM_BOOTSTRAP_TEST_DB_URL")
	if strings.TrimSpace(dbURL) == "" {
		t.Skip("set LOOM_BOOTSTRAP_TEST_DB_URL to run production bootstrap integration tests against a dedicated database")
	}

	ctx := context.Background()
	if _, err := migrations.Up(ctx, dbURL, repoMigrationsDir(t)); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	sqlDB, err := db.OpenSQL(ctx, dbURL)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	defer sqlDB.Close()

	service := NewService(sqlDB)
	suffix := safeIdentifier(ids.NewNodeID())
	input := NormalizeProductionInput(ProductionInput{
		NodeKey:             "main-test-" + suffix,
		DisplayName:         "Main Test " + suffix,
		OwnerActorKey:       "owner:" + suffix,
		OwnerDisplayName:    "Owner " + suffix,
		ServiceActorKey:     "service:loomd:" + suffix,
		SchedulerActorKey:   "scheduler:loom:" + suffix,
		SystemScopeKey:      "system-" + suffix,
		InstallID:           "install_" + suffix,
		PlanHash:            "sha256:" + suffix,
		AuthorityProfileKey: nodeprofiles.AuthorityMainNodeDefault,
		RuntimeProfileKey:   nodeprofiles.RuntimeMainFull,
	})

	devFixtureCountBefore := countRows(t, sqlDB, `SELECT count(*) FROM identity.actors WHERE actor_key = 'agent:dev-low'`)

	first, err := service.EnsureProductionBootstrap(ctx, input)
	if err != nil {
		t.Fatalf("EnsureProductionBootstrap first returned error: %v", err)
	}
	if !first.Ready || !first.Created || first.BootstrapEventCount != 1 {
		t.Fatalf("first summary unexpected: %#v", first)
	}

	second, err := service.EnsureProductionBootstrap(ctx, input)
	if err != nil {
		t.Fatalf("EnsureProductionBootstrap second returned error: %v", err)
	}
	if !second.Ready || second.Created || second.BootstrapEventCount != 1 {
		t.Fatalf("second summary unexpected: %#v", second)
	}

	if after := countRows(t, sqlDB, `SELECT count(*) FROM identity.actors WHERE actor_key = 'agent:dev-low'`); after != devFixtureCountBefore {
		t.Fatalf("dev fixture count changed from %d to %d", devFixtureCountBefore, after)
	}

	var nodeKind, runtimeClass string
	if err := sqlDB.QueryRowContext(ctx, `
		SELECT node_kind, runtime_class
		FROM nodes.nodes
		WHERE node_key = $1
	`, input.NodeKey).Scan(&nodeKind, &runtimeClass); err != nil {
		t.Fatalf("read node: %v", err)
	}
	if nodeKind != "main" || runtimeClass != nodeprofiles.RuntimeMainFull {
		t.Fatalf("node profile = %s/%s", nodeKind, runtimeClass)
	}

	var authorityProfile, runtimeProfile string
	if err := sqlDB.QueryRowContext(ctx, `
		SELECT ap.profile_key, rp.profile_key
		FROM nodes.node_profile_assignments assignment
		JOIN nodes.nodes node ON node.node_id = assignment.node_id
		JOIN nodes.authority_profiles ap ON ap.authority_profile_id = assignment.authority_profile_id
		JOIN nodes.runtime_profiles rp ON rp.runtime_profile_id = assignment.runtime_profile_id
		WHERE node.node_key = $1
	`, input.NodeKey).Scan(&authorityProfile, &runtimeProfile); err != nil {
		t.Fatalf("read profile assignment: %v", err)
	}
	if authorityProfile != nodeprofiles.AuthorityMainNodeDefault || runtimeProfile != nodeprofiles.RuntimeMainFull {
		t.Fatalf("profile assignment = %s/%s", authorityProfile, runtimeProfile)
	}

	var payloadBytes []byte
	if err := sqlDB.QueryRowContext(ctx, `
		SELECT payload
		FROM events.events
		WHERE event_type = $1
		  AND payload->>'mode' = 'production'
		  AND payload->>'node_key' = $2
	`, events.TypeSystemBootstrapped, input.NodeKey).Scan(&payloadBytes); err != nil {
		t.Fatalf("read bootstrap event: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload["mode"] != "production" || payload["node_key"] != input.NodeKey {
		t.Fatalf("event payload = %#v", payload)
	}
}

func TestEnsureProductionBootstrapIntegrationBlocksActorConflict(t *testing.T) {
	dbURL := os.Getenv("LOOM_BOOTSTRAP_TEST_DB_URL")
	if strings.TrimSpace(dbURL) == "" {
		t.Skip("set LOOM_BOOTSTRAP_TEST_DB_URL to run production bootstrap integration tests against a dedicated database")
	}

	ctx := context.Background()
	if _, err := migrations.Up(ctx, dbURL, repoMigrationsDir(t)); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	sqlDB, err := db.OpenSQL(ctx, dbURL)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	defer sqlDB.Close()

	suffix := safeIdentifier(ids.NewNodeID())
	ownerKey := "owner-conflict:" + suffix
	if _, err := sqlDB.ExecContext(ctx, `
		INSERT INTO identity.actors (actor_id, actor_key, display_name, actor_kind, status, metadata)
		VALUES ($1, $2, 'Conflicting Agent', 'agent', 'active', '{}'::jsonb)
	`, ids.NewActorID(), ownerKey); err != nil {
		t.Fatalf("insert conflicting actor: %v", err)
	}

	input := NormalizeProductionInput(ProductionInput{
		NodeKey:         "main-conflict-" + suffix,
		OwnerActorKey:   ownerKey,
		ServiceActorKey: "service:loomd-conflict:" + suffix,
		SystemScopeKey:  "system-conflict-" + suffix,
	})
	_, err = NewService(sqlDB).EnsureProductionBootstrap(ctx, input)
	if err == nil {
		t.Fatal("EnsureProductionBootstrap accepted actor kind conflict")
	}
	if !strings.Contains(err.Error(), "exists with kind") {
		t.Fatalf("error = %q", err)
	}
}

func repoMigrationsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "migrations")
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return count
}
