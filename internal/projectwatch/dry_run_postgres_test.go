package projectwatch

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/db"
	"loom.local/loom/internal/events"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/projectregistration"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

// Only a harness-created, empty fixture database on a private socket is accepted.
// This deliberately does not consume LOOM_DB_URL or the shared LOOM_TEST_DB_URL.
// See the feature handoff for the socket-only cluster and trap-shutdown harness.
func TestProjectWatchDryRunPostgres(t *testing.T) {
	rawURL := strings.TrimSpace(os.Getenv("LOOM_PROJECT_WATCH_TEST_DB_URL"))
	if rawURL == "" {
		t.Skip("LOOM_PROJECT_WATCH_TEST_DB_URL is unset; requires an empty private PostgreSQL fixture")
	}
	clusterRoot, databaseName, err := dryRunPostgresURL(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	// libpq-style environment must not supply hidden connection settings.
	for _, variable := range os.Environ() {
		key, _, _ := strings.Cut(variable, "=")
		if strings.HasPrefix(key, "PG") {
			t.Setenv(key, "")
		}
	}
	info, err := os.Stat(clusterRoot)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("fixture cluster root must be a private directory: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	sqlDB, err := db.OpenSQL(ctx, rawURL)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	var actualDB, dataDir, listenAddresses, socketDir string
	if err := sqlDB.QueryRowContext(ctx, `SELECT current_database(), current_setting('data_directory'), current_setting('listen_addresses'), current_setting('unix_socket_directories')`).Scan(&actualDB, &dataDir, &listenAddresses, &socketDir); err != nil {
		t.Fatal(err)
	}
	wantData, err := filepath.EvalSymlinks(filepath.Join(clusterRoot, "data"))
	if err != nil {
		t.Fatal(err)
	}
	actualData, err := filepath.EvalSymlinks(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if actualDB != databaseName || actualData != wantData || listenAddresses != "" || socketDir != filepath.Join(clusterRoot, "socket") {
		t.Fatal("server is not the harness-owned socket-only fixture cluster")
	}
	var nonempty bool
	if err := sqlDB.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname !~ '^pg_' AND nspname NOT IN ('information_schema', 'public'))
		OR EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema')
		OR EXISTS (SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema')
		OR EXISTS (SELECT 1 FROM pg_type y JOIN pg_namespace n ON n.oid = y.typnamespace WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema')
	`).Scan(&nonempty); err != nil {
		t.Fatal(err)
	}
	if nonempty {
		t.Fatal("refusing to migrate a nonempty fixture database; create a fresh database for each invocation")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	if result, err := migrations.Up(ctx, rawURL, filepath.Join(filepath.Dir(file), "..", "..", "migrations")); err != nil || result.Status != "ok" {
		t.Fatalf("migrate empty fixture: result=%#v err=%v", result, err)
	}
	if _, err := bootstrap.NewService(sqlDB).EnsureDevBootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	req, err := requestctx.ResolveBootstrap(ctx, sqlDB, "corr_project_watch_dry_run")
	if err != nil {
		t.Fatal(err)
	}
	_, analysis := dryRunFixture(t, projects.ProjectActivationStatusInactive)
	input, err := projectregistration.BuildInput(analysis, "project-watch-dry-run-test")
	if err != nil {
		t.Fatal(err)
	}
	projectService := projects.NewService(sqlDB)
	registered, err := projectService.RegisterProjectContract(ctx, req, input)
	if err != nil {
		t.Fatal(err)
	}
	ref := registered.Detail.Project.Project.ProjectID
	registrationID := registered.Detail.Registration.ProjectContractRegistrationID
	if registered.Detail.Registration.ActivationStatus != projects.ProjectActivationStatusInactive || len(registered.Detail.WatchedRootRegistrations) != 0 {
		t.Fatalf("fixture must start inactive without watched roots: %#v", registered.Detail)
	}
	service := NewService(Deps{Projects: projectService, Nodes: nodes.NewService(sqlDB)})

	checkDryRun := func(t *testing.T, projectRef string, input projects.ApplyProjectWatchPolicyInput, wantStatus, wantError string) {
		t.Helper()
		before := dryRunDatabaseSnapshot(t, ctx, sqlDB)
		for attempt := 0; attempt < 2; attempt++ {
			result, err := service.ApplyDesiredState(ctx, req, projectRef, input)
			if wantError != "" {
				if err == nil || !strings.Contains(err.Error(), wantError) || result.DryRun {
					t.Fatalf("expected %q without success: result=%#v err=%v", wantError, result, err)
				}
			} else if err != nil || !result.DryRun || result.Detail.Registration == nil || result.Detail.Registration.ActivationStatus != wantStatus || len(result.Commands) == 0 || len(result.WatchedRoots) != 0 {
				t.Errorf("dry-run response on attempt %d: result=%#v err=%v", attempt, result, err)
			}
			after := dryRunDatabaseSnapshot(t, ctx, sqlDB)
			for table, rows := range before {
				if rows != after[table] {
					t.Errorf("dry-run changed durable table %s on attempt %d", table, attempt)
				}
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("dry-run changed database state (activation, registrations, events and queues must remain unchanged)")
			}
		}
	}
	t.Run("inactive_replay", func(t *testing.T) {
		checkDryRun(t, ref, projects.ApplyProjectWatchPolicyInput{DryRun: true}, projects.ProjectActivationStatusInactive, "")
	})
	t.Run("malformed", func(t *testing.T) {
		if err := os.WriteFile(analysis.Loaded.ContractPath, []byte("project: ["), 0o600); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.WriteFile(analysis.Loaded.ContractPath, analysis.Loaded.Raw, 0o600); err != nil {
				t.Error(err)
			}
		}()
		checkDryRun(t, ref, projects.ApplyProjectWatchPolicyInput{DryRun: true}, "", "could not be loaded")
	})
	t.Run("stale_and_snapshot", func(t *testing.T) {
		if err := os.WriteFile(analysis.Loaded.ContractPath, append(append([]byte{}, analysis.Loaded.Raw...), []byte("\n# changed fixture\n")...), 0o600); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.WriteFile(analysis.Loaded.ContractPath, analysis.Loaded.Raw, 0o600); err != nil {
				t.Error(err)
			}
		}()
		checkDryRun(t, ref, projects.ApplyProjectWatchPolicyInput{DryRun: true}, "", "hash is stale")
		checkDryRun(t, ref, projects.ApplyProjectWatchPolicyInput{DryRun: true, UseRegisteredSnapshot: true}, projects.ProjectActivationStatusInactive, "")
	})
	t.Run("unresolved", func(t *testing.T) {
		checkDryRun(t, "missing-fixture-project", projects.ApplyProjectWatchPolicyInput{DryRun: true}, "", "no rows")
	})
	t.Run("actual_apply_failure_keeps_activation", func(t *testing.T) {
		failingService := NewService(Deps{Projects: projectService, Nodes: &dryRunReadOnlyStore{}})
		for attempt := 0; attempt < 2; attempt++ {
			result, err := failingService.ApplyDesiredState(ctx, req, ref, projects.ApplyProjectWatchPolicyInput{})
			if err == nil || !strings.Contains(err.Error(), "unexpected node lookup") || result.DryRun || len(result.WatchedRoots) != 0 {
				t.Fatalf("actual apply must propagate owner resolution failure: result=%#v err=%v", result, err)
			}
			detail, err := projectService.GetProjectRegistrationStatus(ctx, ref)
			if err != nil {
				t.Fatal(err)
			}
			if detail.Registration.ActivationStatus != projects.ProjectActivationStatusBaseActive || len(detail.WatchedRootRegistrations) != 0 {
				t.Fatalf("actual apply must preserve its existing partial activation behavior: %#v", detail)
			}
		}
		checkDryRun(t, ref, projects.ApplyProjectWatchPolicyInput{DryRun: true}, projects.ProjectActivationStatusBaseActive, "")
	})
	t.Run("actual_apply_and_active_replay", func(t *testing.T) {
		result, err := service.ApplyDesiredState(ctx, req, ref, projects.ApplyProjectWatchPolicyInput{})
		if err != nil {
			t.Fatal(err)
		}
		if result.DryRun || result.Detail.Registration.ActivationStatus != projects.ProjectActivationStatusBaseActive || len(result.WatchedRoots) != len(analysis.Plan.WatchedRoots) {
			t.Fatalf("actual apply did not activate/register the plan: %#v", result)
		}
		var activationEvents int
		if err := sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM events.events WHERE target_id = $1 AND event_type = $2`, registrationID, events.TypeProjectBaseActivated).Scan(&activationEvents); err != nil {
			t.Fatal(err)
		}
		if activationEvents != 1 || result.Detail.Registration.BaseActivatedAt == nil || (result.Detail.Registration.BaseActivatedByActorID == nil || *result.Detail.Registration.BaseActivatedByActorID != req.ActorID) {
			t.Fatalf("actual apply activation evidence: events=%d registration=%#v", activationEvents, result.Detail.Registration)
		}
		checkDryRun(t, ref, projects.ApplyProjectWatchPolicyInput{DryRun: true}, projects.ProjectActivationStatusBaseActive, "")
	})
}

func dryRunPostgresURL(raw string) (string, string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("invalid fixture URL")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", "", fmt.Errorf("invalid fixture URL query")
	}
	socket := query.Get("host")
	root := filepath.Dir(socket)
	name := strings.TrimPrefix(u.Path, "/")
	if (u.Scheme != "postgresql" && u.Scheme != "postgres") || u.Host != "" || u.User == nil || u.User.String() != "loom_watch_fixture" || u.Fragment != "" || len(query) != 2 || len(query["host"]) != 1 || len(query["sslmode"]) != 1 || query.Get("sslmode") != "disable" || filepath.Dir(root) != "/tmp" || !strings.HasPrefix(filepath.Base(root), "lwp.") || socket != filepath.Join(root, "socket") || !strings.HasPrefix(name, "loom_watch_test_") || strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
		return "", "", fmt.Errorf("requires explicit loom_watch_fixture user, loom_watch_test_* database and private /tmp/lwp.*/socket URL with sslmode=disable")
	}
	return root, name, nil
}

func TestProjectWatchDryRunPostgresURLGuard(t *testing.T) {
	valid := "postgresql://loom_watch_fixture@/loom_watch_test_guard?host=/tmp/lwp.guard/socket&sslmode=disable"
	if _, _, err := dryRunPostgresURL(valid); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"", "postgresql:///postgres", strings.Replace(valid, "loom_watch_test_guard", "postgres", 1),
		strings.Replace(valid, "loom_watch_test_guard", "template1", 1),
		strings.Replace(valid, "@/", "@localhost/", 1), strings.Replace(valid, "/tmp/lwp.guard/socket", "/var/run/postgresql", 1),
		strings.Replace(valid, "loom_watch_fixture", "other", 1), valid + "&host=localhost", valid + "&service=production",
	} {
		if _, _, err := dryRunPostgresURL(raw); err == nil {
			t.Errorf("accepted unsafe fixture URL: %q", raw)
		}
	}
}

// Snapshot every user table, including activation columns and timestamps,
// registrations, audit/event payloads, and any job/queue state. Checking complete
// rows also catches updates to existing rows that a count-only assertion misses.
func dryRunDatabaseSnapshot(t *testing.T, ctx context.Context, sqlDB *sql.DB) map[string]string {
	t.Helper()
	rows, err := sqlDB.QueryContext(ctx, `SELECT quote_ident(n.nspname) || '.' || quote_ident(c.relname) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.relkind IN ('r', 'p') AND n.nspname !~ '^pg_' AND n.nspname <> 'information_schema' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	snapshot := make(map[string]string, len(tables))
	for _, table := range tables {
		var data string
		if err := sqlDB.QueryRowContext(ctx, `SELECT COALESCE(jsonb_agg(row_data ORDER BY row_data::text), '[]'::jsonb)::text FROM (SELECT to_jsonb(t) AS row_data FROM `+table+` t) rows`).Scan(&data); err != nil {
			t.Fatalf("snapshot %s: %v", table, err)
		}
		snapshot[table] = data
	}
	return snapshot
}
