package automation_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/httpapi"
	"loom.local/loom/internal/idempotency"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/loomcli"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/response"
)

// This opt-in fixture cannot use LOOM_DB_URL or a production endpoint. The
// worker/operator supplies a fresh, private local cluster. Databases are retained
// for inspection; only our own connections/listener are closed by the tests.
func calendarDatabase(t *testing.T) (*sql.DB, string, requestctx.Context) {
	t.Helper()
	raw := os.Getenv("LOOM_CALENDAR_TEST_DB_URL")
	if raw == "" {
		t.Skip("LOOM_CALENDAR_TEST_DB_URL is unset; requires worker-owned disposable PostgreSQL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "" || !strings.HasPrefix(u.Query().Get("host"), "/tmp/loom-calendar-w1.") || u.Path != "/loom_calendar_w1" {
		t.Fatal("calendar fixture must name loom_calendar_w1 on a private /tmp/loom-calendar-w1.* Unix socket")
	}
	admin, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("loom_calendar_%d", time.Now().UnixNano())
	if _, err = admin.ExecContext(t.Context(), `CREATE DATABASE "`+name+`"`); err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	result, err := migrations.Up(t.Context(), u.String(), filepath.Join("..", "..", "migrations"))
	if err != nil || result.CurrentVersion != 69 {
		t.Fatalf("migration=%+v err=%v", result, err)
	}
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err = bootstrap.NewService(db).EnsureDevBootstrap(t.Context()); err != nil {
		t.Fatal(err)
	}
	req, err := requestctx.ResolveBootstrap(t.Context(), db, "corr_calendar_fixture")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("PostgreSQL fixture: %s, migration %d; retained for review", u.String(), result.CurrentVersion)
	return db, u.String(), req
}

func seedCalendarTarget(t *testing.T, db *sql.DB, req requestctx.Context) projects.Project {
	t.Helper()
	svc := capabilities.NewService(db)
	provider, err := svc.RegisterProvider(t.Context(), req, capabilities.RegisterProviderInput{ProviderKey: "calendar", CompactAddress: "main@calendar", DisplayName: "Calendar fixture", ProviderType: "system", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	class, err := svc.RegisterCapabilityClass(t.Context(), req, capabilities.RegisterCapabilityClassInput{Namespace: "calendar", Name: "read", DisplayName: "Synthetic read", Form: "query", DefaultRiskLevel: "low"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.RegisterCapabilityEndpoint(t.Context(), req, capabilities.RegisterCapabilityEndpointInput{ProviderRef: provider.ProviderID, CapabilityClassRef: class.CapabilityClassID, EndpointName: "read", CompactAddress: "main@calendar.read", Form: "query", RiskLevel: "low", ExecutionAuthorizationLevel: 1, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := projects.NewService(db).CreateProject(t.Context(), req, projects.CreateInput{Name: "Calendar fixture", Slug: "calendar-fixture", HomeNodeRef: "main"})
	if err != nil {
		t.Fatal(err)
	}
	return project.Project.Project
}

func calendarCounts(t *testing.T, db *sql.DB) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, table := range []string{"automation.schedules", "automation.automations", "automation.schedule_fires", "automation.invocations", "jobs.jobs", "interface.idempotency_keys"} {
		var n int
		if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		counts[table] = n
	}
	return counts
}

func TestCalendarPostgresSupportedPreviewAndDurableClaims(t *testing.T) {
	db, dbURL, req := calendarDatabase(t)
	project := seedCalendarTarget(t, db, req)
	svc := automation.Service{DB: db}
	handler := httpapi.NewServer(httpapi.Services{DB: db, Automation: svc, Idempotency: idempotency.NewService(db)}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	socket := filepath.Join(os.TempDir(), fmt.Sprintf("loom-cal-%d.sock", time.Now().UnixNano()))
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	client := localclient.New(socket).WithIdempotencyKey("calendar-preview-must-not-persist")
	input := automation.CreateScheduleInput{ScheduleKey: "calendar_fixture__daily", ScheduleKind: "cron", ScheduleExpr: "15 9,13,18 * * *", Timezone: "Europe/Amsterdam", TargetCapability: "main@calendar.read", ProjectRef: project.ProjectID, ScopeRef: project.ProjectScopeID, RunAsActorRef: "owner", InputJSON: json.RawMessage(`{"task":"synthetic"}`), DryRun: true}
	assertEmpty := func() {
		t.Helper()
		for table, n := range calendarCounts(t, db) {
			if n != 0 {
				t.Fatalf("preview wrote %s: %d rows", table, n)
			}
		}
	}
	assertPreview := func(d automation.ScheduleDetail) {
		t.Helper()
		s := d.Schedule
		if !d.DryRun || s.ScheduleID != "" || s.AutomationID != "" || s.Status != "disabled" || s.ScheduleKind != "cron" || s.ScheduleExpr != input.ScheduleExpr || s.Timezone != input.Timezone || s.ProjectID == nil || *s.ProjectID != project.ProjectID || s.ScopeID == nil || *s.ScopeID != project.ProjectScopeID || s.RunAsActorID != req.ActorID || string(s.InputJSON) != `{"task":"synthetic"}` {
			t.Fatalf("preview contract lost: %+v", d)
		}
		if s.StartAt == nil || s.NextFireAt == nil {
			t.Fatal("preview missing next time")
		}
		plan, err := automation.ParseScheduleExpressionInTimezone("cron", input.ScheduleExpr, input.Timezone, *s.StartAt)
		if err != nil || !plan.NextFireAt.Equal(*s.NextFireAt) {
			t.Fatalf("preview time differs: %+v %v", plan, err)
		}
	}
	t.Run("client HTTP preview has no persistence", func(t *testing.T) {
		result, err := client.CreateSchedule(t.Context(), "corr_calendar_preview", input)
		if err != nil {
			t.Fatal(err)
		}
		assertPreview(result.Data)
		assertEmpty()
	})
	t.Run("CLI client HTTP preview has no persistence", func(t *testing.T) {
		cmd := loomcli.NewRootCommand()
		var out, stderr bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&stderr)
		cmd.SetArgs([]string{"--socket", socket, "--json", "schedules", "create", "--key", input.ScheduleKey, "--cron", input.ScheduleExpr, "--timezone", input.Timezone, "--target", input.TargetCapability, "--project", input.ProjectRef, "--scope", input.ScopeRef, "--run-as", "owner", "--input-json", string(input.InputJSON), "--dry-run", "--idempotency-key", "cli-preview-key"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%v: %s", err, stderr.String())
		}
		var envelope response.Envelope[automation.ScheduleDetail]
		if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		assertPreview(envelope.Data)
		assertEmpty()
	})
	t.Run("preview validates same references and input as apply", func(t *testing.T) {
		for _, mutate := range []func(*automation.CreateScheduleInput){
			func(i *automation.CreateScheduleInput) { i.Timezone = "" }, func(i *automation.CreateScheduleInput) { i.Timezone = "Local" },
			func(i *automation.CreateScheduleInput) { i.ScheduleExpr = "0 * * * * *" }, func(i *automation.CreateScheduleInput) { i.TargetCapability = "main@missing.read" },
			func(i *automation.CreateScheduleInput) { i.ProjectRef = "project_missing" }, func(i *automation.CreateScheduleInput) { i.ScopeRef = "scope_missing" },
			func(i *automation.CreateScheduleInput) { i.RunAsActorRef = "actor_missing" }, func(i *automation.CreateScheduleInput) { i.InputJSON = json.RawMessage(`[]`) },
		} {
			bad := input
			mutate(&bad)
			for _, dry := range []bool{true, false} {
				bad.DryRun = dry
				if _, err := localclient.New(socket).CreateSchedule(t.Context(), "corr_bad_calendar", bad); err == nil {
					t.Fatalf("accepted invalid dry_run=%t: %+v", dry, bad)
				}
			}
			assertEmpty()
		}
		if _, err := db.Exec(`UPDATE projects.projects SET status='archived' WHERE project_id=$1`, project.ProjectID); err != nil {
			t.Fatal(err)
		}
		if _, err := client.CreateSchedule(t.Context(), "corr_archived_calendar", input); err == nil {
			t.Fatal("preview accepted archived project")
		}
		assertEmpty()
		if _, err := db.Exec(`UPDATE projects.projects SET status='active' WHERE project_id=$1`, project.ProjectID); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("migration down and up without cron data", func(t *testing.T) {
		dir := filepath.Join("..", "..", "migrations")
		if err := goose.DownContext(t.Context(), db, dir); err != nil {
			t.Fatal(err)
		}
		if _, err := migrations.Up(t.Context(), dbURL, dir); err != nil {
			t.Fatal(err)
		}
	})
	input.DryRun = false
	result, err := localclient.New(socket).CreateSchedule(t.Context(), "corr_calendar_apply", input)
	if err != nil {
		t.Fatal(err)
	}
	s := result.Data.Schedule
	if result.Data.DryRun || s.ScheduleID == "" || s.Status != "disabled" || s.Timezone != input.Timezone {
		t.Fatalf("apply: %+v", result.Data)
	}
	persisted, err := svc.GetSchedule(t.Context(), s.ScheduleID)
	if err != nil {
		t.Fatal(err)
	}
	if !persisted.Schedule.NextFireAt.Equal(*s.NextFireAt) {
		t.Fatal("initial due changed on reload")
	}
	if _, err := db.Exec(`UPDATE automation.schedules SET schedule_kind='bogus' WHERE schedule_id=$1`, s.ScheduleID); err == nil {
		t.Fatal("invalid kind passed constraint")
	}
	if err := goose.DownContext(t.Context(), db, filepath.Join("..", "..", "migrations")); err == nil || !strings.Contains(err.Error(), "retained cron") {
		t.Fatalf("rollback did not refuse retained cron: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM automation.schedules WHERE schedule_id=$1 AND schedule_kind='cron'`, s.ScheduleID).Scan(&n); err != nil || n != 1 {
		t.Fatal("rollback damaged cron row")
	}
	// Use explicit synthetic due times and activation only in this disposable DB.
	due := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC)
	if _, err := db.Exec(`UPDATE automation.schedules SET status='active',schedule_expr='30 2 * * *',next_fire_at=$2 WHERE schedule_id=$1`, s.ScheduleID, due); err != nil {
		t.Fatal(err)
	}
	// A new connection/service simulates process restart; due identity is persisted.
	restartedDB, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer restartedDB.Close()
	restarted := automation.Service{DB: restartedDB}
	loaded, err := restarted.GetSchedule(t.Context(), s.ScheduleID)
	if err != nil || !loaded.Schedule.NextFireAt.Equal(due) {
		t.Fatalf("restart due: %+v %v", loaded, err)
	}
	var wg sync.WaitGroup
	results := make(chan automation.SchedulerRunResult, 2)
	errs := make(chan error, 2)
	start := make(chan struct{})
	for _, runner := range []automation.Service{svc, restarted} {
		wg.Add(1)
		go func(r automation.Service) {
			defer wg.Done()
			<-start
			v, e := r.RunScheduler(context.Background(), req, automation.SchedulerRunInput{Now: due})
			results <- v
			errs <- e
		}(runner)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var fires, invocations int64
	for r := range results {
		fires += r.CreatedFires
		invocations += r.CreatedInvocations
	}
	if fires != 1 || invocations != 1 {
		t.Fatalf("concurrent claims fires=%d invocations=%d", fires, invocations)
	}
	next := time.Date(2026, 10, 26, 1, 30, 0, 0, time.UTC)
	loaded, err = restarted.GetSchedule(t.Context(), s.ScheduleID)
	if err != nil || !loaded.Schedule.NextFireAt.Equal(next) {
		t.Fatalf("fold replay: %+v %v", loaded, err)
	}
	var fireID, occurrence, projectID, scopeID, actorID, target, key string
	var scheduled time.Time
	var payload []byte
	err = db.QueryRow(`SELECT f.schedule_fire_id,f.scheduled_for,i.source_occurrence_ref,i.project_id,i.scope_id,i.actor_id,i.target_capability,i.input_json,i.idempotency_key FROM automation.schedule_fires f JOIN automation.invocations i ON i.invocation_id=f.invocation_id WHERE f.schedule_id=$1`, s.ScheduleID).Scan(&fireID, &scheduled, &occurrence, &projectID, &scopeID, &actorID, &target, &payload, &key)
	if err != nil {
		t.Fatal(err)
	}
	if occurrence != fireID || !scheduled.Equal(due) || key != "automation-invocation:schedule_fire:"+fireID || projectID != project.ProjectID || scopeID != project.ProjectScopeID || actorID != req.ActorID || target != input.TargetCapability || !bytes.Contains(payload, []byte("synthetic")) {
		t.Fatal("fire identity, target, input, run-as or project scope was lost")
	}
	replay, err := restarted.RunScheduler(t.Context(), req, automation.SchedulerRunInput{Now: due.Add(time.Hour)})
	if err != nil || replay.CreatedFires != 0 {
		t.Fatalf("fold second copy fired: %+v %v", replay, err)
	}
	// A long outage records one missed fire, advances beyond now and does not
	// create a catch-up invocation storm, even for a per-minute expression.
	overdue := due.AddDate(0, 0, -10)
	now := due.Add(2 * time.Hour)
	if _, err := db.Exec(`UPDATE automation.schedules SET schedule_expr='* * * * *',next_fire_at=$2 WHERE schedule_id=$1`, s.ScheduleID, overdue); err != nil {
		t.Fatal(err)
	}
	missed, err := restarted.RunScheduler(t.Context(), req, automation.SchedulerRunInput{Now: now})
	if err != nil || missed.MissedFires != 1 || missed.CreatedInvocations != 0 || missed.NextDueAt == nil || !missed.NextDueAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("misfire %+v %v", missed, err)
	}
	again, err := restarted.RunScheduler(t.Context(), req, automation.SchedulerRunInput{Now: now})
	if err != nil || again.CreatedFires != 0 {
		t.Fatalf("catch-up storm: %+v %v", again, err)
	}
	// Preserve existing pending-run exclusion for cron schedules.
	if _, err := db.Exec(`UPDATE automation.schedules SET next_fire_at=$2,concurrency_profile_json='{"policy":"skip_if_pending"}' WHERE schedule_id=$1`, s.ScheduleID, now); err != nil {
		t.Fatal(err)
	}
	skipped, err := restarted.RunScheduler(t.Context(), req, automation.SchedulerRunInput{Now: now})
	if err != nil || skipped.SkippedFires != 1 || skipped.CreatedInvocations != 0 {
		t.Fatalf("concurrency profile: %+v %v", skipped, err)
	}

	// Explicit lateness policy admits one late run and still advances past now.
	lateDue := now.Add(2 * time.Minute)
	if _, err := db.Exec(`UPDATE automation.schedules SET next_fire_at=$2,concurrency_profile_json='{"policy":"allow_parallel"}',misfire_profile_json='{"policy":"run_if_late_within","lateness_window_seconds":60}' WHERE schedule_id=$1`, s.ScheduleID, lateDue); err != nil {
		t.Fatal(err)
	}
	late, err := restarted.RunScheduler(t.Context(), req, automation.SchedulerRunInput{Now: lateDue.Add(30 * time.Second)})
	if err != nil || late.CreatedFires != 1 || late.CreatedInvocations != 1 || late.MissedFires != 0 {
		t.Fatalf("late policy %+v %v", late, err)
	}
	if _, err := restarted.PauseSchedule(t.Context(), req, s.ScheduleID, automation.UpdateScheduleStatusInput{}); err != nil {
		t.Fatal(err)
	}
	paused, err := restarted.RunScheduler(t.Context(), req, automation.SchedulerRunInput{Now: lateDue.Add(time.Hour)})
	if err != nil || paused.CreatedFires != 0 {
		t.Fatalf("paused calendar fired %+v %v", paused, err)
	}
	// Existing default posture and disabled calendar default are additive. False
	// dry_run remains an ordinary apply; explicit active cron is supported only
	// under the same admission path and only used here against synthetic data.
	for _, kind := range []string{"one_shot", "interval", "cron"} {
		old := input
		old.ScheduleKey = "calendar_default_" + kind
		old.ScheduleKind = kind
		switch kind {
		case "one_shot":
			old.ScheduleExpr = "2030-01-01T00:00:00Z"
		case "interval":
			old.ScheduleExpr = "24h"
		case "cron":
			old.Status = "active"
		}
		d, err := localclient.New(socket).CreateSchedule(t.Context(), "corr_calendar_defaults", old)
		if err != nil || d.Data.DryRun || d.Data.Schedule.Status != "active" || d.Data.Schedule.ScheduleID == "" {
			t.Fatalf("%s default/apply changed: %+v %v", kind, d, err)
		}
	}
}
