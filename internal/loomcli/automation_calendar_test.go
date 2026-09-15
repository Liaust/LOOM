package loomcli

import (
	"encoding/json"
	"net/http"
	"testing"

	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/idempotency"
	"loom.local/loom/internal/response"
)

func TestCalendarScheduleCLIPreviewWireAndConflicts(t *testing.T) {
	calls := 0
	socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/v1/schedules" || r.Header.Get(idempotency.Header) != "" {
			t.Errorf("preview request %s %s idempotency=%q", r.Method, r.URL.Path, r.Header.Get(idempotency.Header))
		}
		var input automation.CreateScheduleInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		if input.ScheduleKind != "cron" || input.ScheduleExpr != "15 9 * * *" || input.Timezone != "Europe/Amsterdam" || !input.DryRun || input.ProjectRef != "calendar-project" || input.RunAsActorRef != "owner" {
			t.Errorf("wire input: %+v", input)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("corr_calendar_cli", automation.ScheduleDetail{DryRun: true, Schedule: automation.Schedule{ScheduleKind: "cron", ScheduleExpr: input.ScheduleExpr, Timezone: input.Timezone, Status: "disabled"}}))
	})
	defer stop()
	base := []string{"--socket", socket, "--json", "schedules", "create", "--key", "calendar", "--target", "main@calendar.read"}
	args := append(append([]string{}, base...), "--cron", "15 9 * * *", "--timezone", "Europe/Amsterdam", "--project", "calendar-project", "--run-as", "owner", "--dry-run", "--idempotency-key", "should-not-send")
	out, stderr, err := executeRootCommand(args...)
	if err != nil {
		t.Fatalf("%v %s", err, stderr)
	}
	var result response.Envelope[automation.ScheduleDetail]
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Data.DryRun || result.Data.Schedule.Status != "disabled" {
		t.Fatalf("preview result %+v", result)
	}
	for _, flags := range [][]string{
		{"--cron", "15 9 * * *"},
		{"--cron", "15 9 * * *", "--timezone", "Local"},
		{"--cron", "15 9 * * *", "--timezone", ""},
		{"--cron", "0 * * * * *", "--timezone", "UTC"},
		{"--cron", "", "--timezone", "UTC"},
		{"--cron", "15 9 * * *", "--timezone", "UTC", "--every", "1h"},
		{"--cron", "15 9 * * *", "--timezone", "UTC", "--one-shot", "now"},
		{"--every", "1h", "--one-shot", "now"},
	} {
		_, _, err := executeRootCommand(append(append([]string{}, base...), flags...)...)
		if err == nil {
			t.Fatalf("accepted invalid flags %v", flags)
		}
	}
	if calls != 1 {
		t.Fatalf("invalid flags reached server: %d calls", calls)
	}
}
