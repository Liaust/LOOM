package automation_test

import (
	"testing"

	"loom.local/loom/internal/automation"
)

func TestOverviewPostgresInventoryAndArchiveScope(t *testing.T) {
	db, _, req := calendarDatabase(t)
	project := seedCalendarTarget(t, db, req)
	svc := automation.Service{DB: db}
	seed, err := svc.CreateSchedule(t.Context(), req, automation.CreateScheduleInput{
		ScheduleKey: "overview_seed", ScheduleKind: "interval", ScheduleExpr: "1h",
		TargetCapability: "main@calendar.read", ProjectRef: project.ProjectID, RunAsActorRef: "owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	// The existing disposable fixture owns these rows. Shared timestamps force
	// the secondary schedule_id key to carry pagination past the default 50.
	_, err = db.ExecContext(t.Context(), `INSERT INTO automation.schedules
		(schedule_id, automation_id, schedule_key, display_name, status, schedule_kind,
		 schedule_expr, timezone, next_fire_at, created_by_actor_id, run_as_actor_id, project_id, created_at)
		SELECT 'schedule_overview_' || lpad(n::text, 3, '0'), automation_id,
		       'overview_' || n::text, 'Overview fixture',
		       CASE WHEN n % 3 = 0 THEN 'active' WHEN n % 3 = 1 THEN 'paused' ELSE 'disabled' END,
		       schedule_kind, schedule_expr, timezone, next_fire_at, created_by_actor_id,
		       run_as_actor_id, project_id, created_at
		FROM automation.schedules CROSS JOIN generate_series(1, 66) AS n WHERE schedule_id=$1`, seed.Schedule.ScheduleID)
	if err != nil {
		t.Fatal(err)
	}
	var want automation.OverviewCounts
	err = db.QueryRowContext(t.Context(), `SELECT count(*) FILTER (WHERE status='active'),
		count(*) FILTER (WHERE status='paused'), count(*) FILTER (WHERE status='disabled'), count(*)
		FROM automation.schedules`).Scan(&want.Active, &want.Paused, &want.Disabled, &want.Total)
	if err != nil {
		t.Fatal(err)
	}
	filter := automation.OverviewFilter{}
	seen := map[string]bool{}
	for {
		page, err := svc.Overview(t.Context(), filter)
		if err != nil || page.Counts != want {
			t.Fatalf("page counts: %+v want %+v err=%v", page.Counts, want, err)
		}
		for _, item := range page.Items {
			if seen[item.ScheduleID] {
				t.Fatalf("duplicate schedule: %s", item.ScheduleID)
			}
			seen[item.ScheduleID] = true
		}
		if page.Complete {
			break
		}
		if page.NextCursor == "" {
			t.Fatal("missing continuation")
		}
		filter.Cursor = page.NextCursor
	}
	if int64(len(seen)) != want.Total || want.Total != 67 {
		t.Fatalf("incomplete inventory: %d want %d", len(seen), want.Total)
	}
	filtered, err := svc.Overview(t.Context(), automation.OverviewFilter{Status: "paused"})
	if err != nil || filtered.Counts.Total != want.Paused || filtered.Counts.Paused != want.Paused || filtered.Counts.Active != 0 || len(filtered.Occurrences) != 0 {
		t.Fatalf("status scope: %+v %v", filtered, err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE projects.projects SET status='archived' WHERE project_id=$1`, project.ProjectID); err != nil {
		t.Fatal(err)
	}
	page, err := svc.Overview(t.Context(), automation.OverviewFilter{})
	if err != nil || page.Counts.Total != 0 || len(page.Items) != 0 || !page.Complete {
		t.Fatalf("default archive exclusion: %+v %v", page, err)
	}
	for _, filter := range []automation.OverviewFilter{{ProjectRef: project.ProjectID}, {ProjectRef: project.Slug}, {AutomationRef: seed.Automation.AutomationID}} {
		page, err := svc.Overview(t.Context(), filter)
		if err != nil || page.Counts != want {
			t.Fatalf("explicit archive scope: %+v %v", page, err)
		}
	}
	var fires, invocations int
	if err := db.QueryRowContext(t.Context(), `SELECT (SELECT count(*) FROM automation.schedule_fires),
		(SELECT count(*) FROM automation.invocations)`).Scan(&fires, &invocations); err != nil || fires != 0 || invocations != 0 {
		t.Fatalf("overview dispatched work: fires=%d invocations=%d err=%v", fires, invocations, err)
	}
}
