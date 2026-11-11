package loomcli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/response"
)

func TestAutomationDiagnosticsStatusScope(t *testing.T) {
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	renderScheduleStatus(cmd, automation.ScheduleStatus{MissedFireCount: 4, FailedInvocationCount: 2})
	for _, want := range []string{"retained history excluding archived-project automations", "not a recent window", "missed=4", "failed_or_attention=2", "waiting_approval", "requires_manual_action, not hard failures"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %s", want, out.String())
		}
	}
}

func TestAutomationDiagnosticsScheduleHumanAndJSON(t *testing.T) {
	project, scope, fire := "project_test", "scope_test", "schedule_fire_test"
	last := time.Date(2026, 10, 13, 2, 50, 0, 0, time.UTC)
	detail := automation.ScheduleDetail{
		Schedule: automation.Schedule{ScheduleID: "schedule_test", ScheduleKey: "conditional_review", Status: "active",
			ScheduleKind: "cron", ScheduleExpr: "50 2 * * *", Timezone: "Europe/Amsterdam",
			RunAsActorID: "actor_authorization", ProjectID: &project, ScopeID: &scope,
			LastFireAt: &last, LastScheduleFireID: &fire,
			TargetProfileJSON: json.RawMessage(`{"capability_ref":"main@script_runner.run","private":"hidden target data"}`),
			InputJSON:         json.RawMessage(`{"script_ref":"conditional-review@0.2","private":"hidden input data","model":"never infer persona","cwd":"private path"}`)},
		Automation: automation.Automation{AutomationID: "automation_test"},
	}
	socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet || req.URL.Path != "/v1/schedules/schedule_test" {
			t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("inspect", detail))
	})
	defer stop()
	out, stderr, err := executeRootCommand("--socket", socket, "schedule", "inspect", "--", "schedule_test")
	if err != nil {
		t.Fatalf("inspect: %v %s", err, stderr)
	}
	for _, want := range []string{"Target: main@script_runner.run", "Run-as authorization actor: actor_authorization",
		"Project: project_test", "Scope: scope_test", "Script: conditional-review@0.2",
		"Script detail: loom script inspect -- 'conditional-review@0.2'", "Last fire: 2026-10-13T02:50:00Z (schedule_fire_test)",
		"Fires: loom schedule fires -- 'schedule_test'", "Automation detail: loom automation inspect -- 'automation_test'", "Europe/Amsterdam"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
	for _, forbidden := range []string{"hidden target data", "hidden input data", "never infer persona", "private path"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("non-whitelisted field leaked: %s", out)
		}
	}
	out, stderr, err = executeRootCommand("--json", "--socket", socket, "schedule", "inspect", "schedule_test")
	var got response.Envelope[automation.ScheduleDetail]
	if err != nil || json.Unmarshal([]byte(out), &got) != nil || got.Data.Schedule.RunAsActorID != detail.Schedule.RunAsActorID || !bytes.Equal(got.Data.Schedule.InputJSON, detail.Schedule.InputJSON) || !got.Data.Schedule.LastFireAt.Equal(last) {
		t.Fatalf("JSON payload changed: %s %v %s", out, err, stderr)
	}
}

func TestAutomationDiagnosticsDownstreamRoutes(t *testing.T) {
	invocation, job := "invocation_test", "job_test"
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	renderScheduleFireList(cmd, []automation.ScheduleFire{{ScheduleFireID: "schedule_fire_test", InvocationID: &invocation, JobID: &job, MisfireStatus: "late_run"}})
	renderInvocation(cmd, automation.Invocation{InvocationID: invocation, JobID: &job})
	for _, want := range []string{"Invocation detail: loom invocation inspect -- 'invocation_test'", "Job/result detail: loom job inspect --summary -- 'job_test'", "Positive lateness below one second"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, out.String())
		}
	}
	out.Reset()
	renderScheduleFireList(cmd, []automation.ScheduleFire{{ScheduleFireID: "schedule_fire_unknown"}})
	renderInvocation(cmd, automation.Invocation{})
	if strings.Contains(out.String(), "detail: loom") {
		t.Fatal("invented downstream pointer", out.String())
	}
}

func TestAutomationDiagnosticsBoundedAndSafe(t *testing.T) {
	for _, input := range []string{`{}`, `{"script_ref":12}`, `{"nested":{"script_ref":"hidden"}}`, `{"script_ref":"bad\nline"}`, `{"script_ref":"` + strings.Repeat("x", 241) + `"}`} {
		if scheduleScriptRef(json.RawMessage(input)) != "" {
			t.Fatal("accepted unsafe/non-whitelisted script reference", input)
		}
	}
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	renderScheduleDetail(cmd, automation.ScheduleDetail{DryRun: true, Schedule: automation.Schedule{ScheduleID: "not-created", ScheduleKey: "bad\x1b[31m", ScheduleExpr: strings.Repeat("x", 300)}})
	if strings.Contains(out.String(), "\x1b") || strings.Contains(out.String(), strings.Repeat("x", 241)) || strings.Contains(out.String(), "schedule fires") {
		t.Fatal("unsafe/unbounded text or invented preview history", out.String())
	}
	out.Reset()
	renderAutomationReadRoute(cmd, "Script detail", "script inspect", "script';echo secret")
	if !strings.Contains(out.String(), "-- 'script'\\'';echo secret'") {
		t.Fatal("reference was not shell-quoted", out.String())
	}
	out.Reset()
	renderAutomationReadRoute(cmd, "Script detail", "script inspect", strings.Repeat("'", 200))
	if out.Len() != 0 {
		t.Fatal("escaped route exceeded the human line budget", out.String())
	}
}
