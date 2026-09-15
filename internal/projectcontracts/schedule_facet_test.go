package projectcontracts

import (
	"loom.local/loom/internal/automation"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScheduleFacetDiscoversValidSchedule(t *testing.T) {
	root := scheduleProjectFixture(t, true)
	writeSchedulePackage(t, root, "daily_summary", validScheduleContract("daily_summary", "main@schedule-smoke.hello_world", "24h"), `{"message":"scheduled"}`)

	analysis := Analyze(root)
	if !analysis.Report.OK {
		t.Fatalf("expected report ok, diagnostics=%#v", analysis.Report.Diagnostics)
	}
	if len(analysis.Report.Schedules) != 1 {
		t.Fatalf("schedules = %#v", analysis.Report.Schedules)
	}
	schedule := analysis.Report.Schedules[0]
	if schedule.Key != "daily_summary" {
		t.Fatalf("schedule key = %q", schedule.Key)
	}
	if schedule.BackendScheduleKey != "schedule_smoke__daily_summary" {
		t.Fatalf("backend key = %q", schedule.BackendScheduleKey)
	}
	if schedule.TargetCapability != "main@schedule-smoke.hello_world" {
		t.Fatalf("target = %q", schedule.TargetCapability)
	}
	if schedule.ScheduleKind != "interval" || schedule.ScheduleExpr != "24h" {
		t.Fatalf("timing = %q %q", schedule.ScheduleKind, schedule.ScheduleExpr)
	}
	if schedule.ActivationStatus != ScheduleActivationStatusPending {
		t.Fatalf("activation = %q", schedule.ActivationStatus)
	}
	assertPlanAction(t, analysis.Plan.Actions, "would_register_schedule", "schedule_smoke__daily_summary")
	assertPlanAction(t, analysis.Plan.Actions, "would_pause_project_schedule", "schedule_smoke__daily_summary")
}

func TestScheduleFacetDisabledDoesNotScan(t *testing.T) {
	root := scheduleProjectFixture(t, false)
	writeSchedulePackage(t, root, "broken", "bad: [", `{}`)

	analysis := Analyze(root)
	if !analysis.Report.OK {
		t.Fatalf("expected disabled schedule facet to skip scan: %#v", analysis.Report.Diagnostics)
	}
	if len(analysis.Report.Schedules) != 0 {
		t.Fatalf("schedules = %#v", analysis.Report.Schedules)
	}
}

func TestScheduleFacetDiagnostics(t *testing.T) {
	t.Run("missing manifest", func(t *testing.T) {
		root := scheduleProjectFixture(t, true)
		mkdir(t, root, "schedules/daily_summary")
		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatal("expected missing manifest to fail")
		}
		assertDiagnostic(t, analysis.Report.Diagnostics, "schedule.manifest_missing")
	})

	t.Run("invalid kind", func(t *testing.T) {
		root := scheduleProjectFixture(t, true)
		writeSchedulePackage(t, root, "daily_summary", strings.Replace(validScheduleContract("daily_summary", "main@schedule-smoke.hello_world", "24h"), "kind: loom.schedule", "kind: loom.bad", 1), `{}`)
		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatal("expected invalid kind to fail")
		}
		assertDiagnostic(t, analysis.Report.Diagnostics, "schedule.kind_invalid")
	})

	t.Run("invalid target", func(t *testing.T) {
		root := scheduleProjectFixture(t, true)
		writeSchedulePackage(t, root, "daily_summary", validScheduleContract("daily_summary", "not a capability", "24h"), `{}`)
		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatal("expected invalid target to fail")
		}
		assertDiagnostic(t, analysis.Report.Diagnostics, "schedule.target_invalid")
	})

	t.Run("invalid input", func(t *testing.T) {
		root := scheduleProjectFixture(t, true)
		writeSchedulePackage(t, root, "daily_summary", validScheduleContract("daily_summary", "main@schedule-smoke.hello_world", "24h"), `[]`)
		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatal("expected invalid input to fail")
		}
		assertDiagnostic(t, analysis.Report.Diagnostics, "schedule.input_invalid_json")
	})

	t.Run("invalid interval", func(t *testing.T) {
		root := scheduleProjectFixture(t, true)
		writeSchedulePackage(t, root, "daily_summary", validScheduleContract("daily_summary", "main@schedule-smoke.hello_world", "not-a-duration"), `{}`)
		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatal("expected invalid timing to fail")
		}
		assertDiagnostic(t, analysis.Report.Diagnostics, "schedule.timing_invalid")
	})

	t.Run("invalid one shot", func(t *testing.T) {
		root := scheduleProjectFixture(t, true)
		contract := strings.Replace(validScheduleContract("daily_summary", "main@schedule-smoke.hello_world", "24h"), "kind: interval", "kind: one_shot", 1)
		writeSchedulePackage(t, root, "daily_summary", contract, `{}`)
		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatal("expected invalid one-shot timing to fail")
		}
		assertDiagnostic(t, analysis.Report.Diagnostics, "schedule.timing_invalid")
	})

	t.Run("invalid misfire", func(t *testing.T) {
		root := scheduleProjectFixture(t, true)
		contract := strings.Replace(validScheduleContract("daily_summary", "main@schedule-smoke.hello_world", "24h"), "policy: mark_missed", "policy: run_if_late_within", 1)
		writeSchedulePackage(t, root, "daily_summary", contract, `{}`)
		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatal("expected invalid misfire to fail")
		}
		assertDiagnostic(t, analysis.Report.Diagnostics, "schedule.misfire_invalid")
	})
}

func TestProjectScheduleKeyIsStableAndBounded(t *testing.T) {
	got := ProjectScheduleKey("gmail-automation", "daily_summary")
	if got != "gmail_automation__daily_summary" {
		t.Fatalf("ProjectScheduleKey = %q", got)
	}
	long := ProjectScheduleKey("project-"+strings.Repeat("a", 90), "schedule_"+strings.Repeat("b", 90))
	if len(long) > 81 {
		t.Fatalf("schedule key length = %d, want <= 81: %q", len(long), long)
	}
	if !scheduleKeyPattern.MatchString(long) {
		t.Fatalf("schedule key does not match backend key pattern: %q", long)
	}
	if long != ProjectScheduleKey("project-"+strings.Repeat("a", 90), "schedule_"+strings.Repeat("b", 90)) {
		t.Fatal("schedule key should be stable")
	}
}

func scheduleProjectFixture(t *testing.T, enabled bool) string {
	t.Helper()
	enabledText := "false"
	if enabled {
		enabledText = "true"
	}
	root := projectFixture(t, `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: schedule-smoke
  name: Schedule Smoke
  owner_node: main
facets:
  schedules: `+enabledText+`
`)
	mkdir(t, root, "schedules")
	return root
}

func writeSchedulePackage(t *testing.T, root, key, contract, input string) {
	t.Helper()
	packageRoot := filepath.Join(root, "schedules", key)
	mkdir(t, root, filepath.ToSlash(filepath.Join("schedules", key)))
	writeFile(t, packageRoot, "loom.schedule.yaml", contract, 0o600)
	writeFile(t, packageRoot, "input.example.json", input, 0o600)
}

func validScheduleContract(key, target, every string) string {
	return `
kind: loom.schedule
schema_version: schedule.contract.v0.3
schedule:
  key: ` + key + `
  display_name: Daily Summary
  description: Test schedule.
  status: draft
target:
  capability: ` + target + `
  input_file: input.example.json
  run_as: owner
timing:
  kind: interval
  expression: ` + every + `
  timezone: UTC
misfire:
  policy: mark_missed
  lateness_window_seconds: 0
concurrency:
  policy: allow_parallel
approval:
  policy: none
timeout:
  seconds: 60
retry:
  max_attempts: 1
`
}

func TestScheduleFacetScaffoldIsValid(t *testing.T) {
	result, err := ScaffoldProject(ScaffoldOptions{
		Name:      "Schedule Scaffold",
		Slug:      "schedule-scaffold",
		OwnerNode: "main",
		Preset:    PresetAutomation,
		Directory: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("ScaffoldProject returned error: %v", err)
	}
	analysis := Analyze(result.ProjectRoot)
	if !analysis.Report.OK {
		t.Fatalf("expected scaffold to validate, diagnostics=%#v", analysis.Report.Diagnostics)
	}
	if len(analysis.Report.Schedules) != 1 {
		t.Fatalf("expected one scaffolded schedule, got %#v", analysis.Report.Schedules)
	}
	if _, err := os.Stat(filepath.Join(result.ProjectRoot, "schedules", "example_schedule", "input.example.json")); err != nil {
		t.Fatalf("expected schedule input fixture: %v", err)
	}
}

func TestScheduleFacetCalendarParity(t *testing.T) {
	root := scheduleProjectFixture(t, true)
	contract := strings.Replace(validScheduleContract("daily_summary", "main@schedule-smoke.hello_world", "'15 9,13,18 * * *'"), "kind: interval", "kind: cron", 1)
	contract = strings.Replace(contract, "timezone: UTC", "timezone: Europe/Amsterdam", 1)
	writeSchedulePackage(t, root, "daily_summary", contract, `{"message":"scheduled"}`)
	analysis := Analyze(root)
	if !analysis.Report.OK || len(analysis.Report.Schedules) != 1 {
		t.Fatalf("calendar report: %+v", analysis.Report)
	}
	schedule := analysis.Report.Schedules[0]
	if schedule.ScheduleKind != "cron" || schedule.ScheduleExpr != "15 9,13,18 * * *" || schedule.Timezone != "Europe/Amsterdam" || schedule.BackendScheduleKey != "schedule_smoke__daily_summary" || schedule.TargetCapability != "main@schedule-smoke.hello_world" || schedule.RunAs != "owner" || schedule.ActivationStatus != ScheduleActivationStatusPending {
		t.Fatalf("calendar facet: %+v", schedule)
	}
	assertPlanAction(t, analysis.Plan.Actions, "would_register_schedule", "schedule_smoke__daily_summary")
	assertPlanAction(t, analysis.Plan.Actions, "would_pause_project_schedule", "schedule_smoke__daily_summary")
	after := time.Date(2026, 9, 10, 7, 15, 0, 0, time.UTC)
	plan, err := automation.ParseScheduleExpressionInTimezone(schedule.ScheduleKind, schedule.ScheduleExpr, schedule.Timezone, after)
	if err != nil || !plan.NextFireAt.Equal(time.Date(2026, 9, 10, 11, 15, 0, 0, time.UTC)) {
		t.Fatalf("facet next time: %+v %v", plan, err)
	}
	for _, zone := range []string{"", "Local", "unknown/zone"} {
		writeSchedulePackage(t, root, "daily_summary", strings.Replace(contract, "timezone: Europe/Amsterdam", "timezone: "+zone, 1), `{}`)
		report := Analyze(root).Report
		if report.OK {
			t.Fatalf("accepted cron zone %q", zone)
		}
		assertDiagnostic(t, report.Diagnostics, "schedule.timing_invalid")
	}
}
