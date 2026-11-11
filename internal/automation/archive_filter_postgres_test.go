package automation_test

import (
	"encoding/json"
	"testing"

	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/projects"
)

// Use the existing opt-in, owned database fixture to check real caller SQL.
func TestArchiveFilterPostgresRuntimeReaders(t *testing.T) {
	db, _, req := calendarDatabase(t)
	active := seedCalendarTarget(t, db, req)
	archived, err := projects.NewService(db).CreateProject(t.Context(), req, projects.CreateInput{
		Name: "Archived runtime", Slug: "archived-runtime", HomeNodeRef: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := automation.Service{DB: db}
	integration, err := svc.CreateIntegration(t.Context(), req, automation.CreateIntegrationInput{
		IntegrationKey: "archive_filter", DisplayName: "Archive filter fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	var archivedAutomation, archivedEndpoint string
	for _, item := range []struct{ key, project string }{
		{"active_runtime", active.ProjectID}, {"archived_runtime", archived.Project.Project.ProjectID}, {"global_runtime", ""},
	} {
		detail, err := svc.CreateSchedule(t.Context(), req, automation.CreateScheduleInput{
			ScheduleKey: item.key, ScheduleKind: "interval", ScheduleExpr: "1h",
			TargetCapability: "main@calendar.read", ProjectRef: item.project,
			RunAsActorRef: "owner", InputJSON: json.RawMessage(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		fire, err := svc.FireScheduleNow(t.Context(), req, item.key, automation.FireScheduleInput{})
		if err != nil {
			t.Fatal(err)
		}
		for _, update := range []struct{ query, id string }{
			{`UPDATE automation.schedule_fires SET status='missed' WHERE schedule_fire_id=$1`, fire.Fire.ScheduleFireID},
			{`UPDATE automation.invocations SET status='waiting_approval' WHERE invocation_id=$1`, fire.Invocation.InvocationID},
		} {
			if _, err := db.ExecContext(t.Context(), update.query, update.id); err != nil {
				t.Fatal(err)
			}
		}
		endpoint, err := svc.CreateDirectEventEndpoint(t.Context(), req, automation.CreateDirectEventEndpointInput{
			EndpointSlug: item.key + "_event", IntegrationRef: integration.Integration.IntegrationID,
			EventType: "fixture", TargetCapability: "main@calendar.read", ProjectRef: item.project,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), `INSERT INTO automation.direct_events
			(direct_event_id, endpoint_id, integration_id, automation_id, status, payload_hash)
			VALUES ($1, $2, $3, $4, 'failed', $5)`, "direct_event_"+item.key, endpoint.Endpoint.EndpointID,
			integration.Integration.IntegrationID, endpoint.Automation.AutomationID, "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
			t.Fatal(err)
		}
		if item.key == "archived_runtime" {
			archivedAutomation, archivedEndpoint = detail.Automation.AutomationID, endpoint.Endpoint.EndpointID
		}
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE projects.projects SET status='archived' WHERE project_id=$1`, archived.Project.Project.ProjectID); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{archived.Project.Project.ProjectID, archived.Project.Project.Slug} {
		rows, err := svc.ListSchedules(t.Context(), automation.ScheduleFilter{ProjectRef: ref})
		if err != nil || len(rows) != 1 {
			t.Fatalf("explicit archived project schedules %s: %d %v", ref, len(rows), err)
		}
		invocations, err := svc.ListInvocations(t.Context(), automation.InvocationFilter{ProjectRef: ref})
		if err != nil || len(invocations) != 1 {
			t.Fatalf("explicit archived project invocations %s: %d %v", ref, len(invocations), err)
		}
		if _, err := svc.CreateSchedule(t.Context(), req, automation.CreateScheduleInput{
			ScheduleKey: "must_not_revive_archive", ScheduleKind: "interval", ScheduleExpr: "1h",
			TargetCapability: "main@calendar.read", ProjectRef: ref, RunAsActorRef: "owner", InputJSON: json.RawMessage(`{}`),
		}); err == nil {
			t.Fatal("read-only resolution enabled archived schedule creation")
		}
	}
	status, err := svc.ScheduleStatus(t.Context())
	if err != nil || status.ActiveScheduleCount != 2 || status.MissedFireCount != 2 || status.FailedInvocationCount != 2 {
		t.Fatalf("schedule status: %+v err=%v", status, err)
	}
	events, err := svc.DirectEventStatus(t.Context())
	if err != nil || events.ActiveEndpointCount != 2 || events.FailedCount != 2 {
		t.Fatalf("event status: %+v err=%v", events, err)
	}
	invocations, err := svc.ListInvocationFailures(t.Context(), automation.InvocationFilter{})
	if err != nil || len(invocations) != 2 {
		t.Fatalf("default invocation attention: %d err=%v", len(invocations), err)
	}
	fires, err := svc.ListScheduleFires(t.Context(), automation.ScheduleFireFilter{})
	if err != nil || len(fires) != 2 {
		t.Fatalf("default fires: %d err=%v", len(fires), err)
	}
	endpoints, err := svc.ListDirectEventEndpoints(t.Context(), automation.DirectEventEndpointFilter{})
	if err != nil || len(endpoints) != 2 {
		t.Fatalf("default endpoints: %d err=%v", len(endpoints), err)
	}
	for _, failuresOnly := range []bool{false, true} {
		list := svc.ListDirectEvents
		if failuresOnly {
			list = svc.ListDirectEventFailures
		}
		rows, err := list(t.Context(), automation.DirectEventFilter{})
		if err != nil || len(rows) != 2 {
			t.Fatalf("default events failures=%v: %d err=%v", failuresOnly, len(rows), err)
		}
		rows, err = list(t.Context(), automation.DirectEventFilter{EndpointRef: archivedEndpoint})
		if err != nil || len(rows) != 1 {
			t.Fatalf("explicit archived events: %d err=%v", len(rows), err)
		}
	}
	invocations, err = svc.ListInvocationFailures(t.Context(), automation.InvocationFilter{AutomationRef: archivedAutomation})
	if err != nil || len(invocations) != 1 {
		t.Fatalf("explicit archived invocation: %d err=%v", len(invocations), err)
	}
}
