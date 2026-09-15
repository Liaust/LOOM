package automation

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeInitialScheduleStatusDefaultsToActive(t *testing.T) {
	got, err := normalizeInitialScheduleStatus("")
	if err != nil {
		t.Fatalf("normalizeInitialScheduleStatus returned error: %v", err)
	}
	if got != ScheduleStatusActive {
		t.Fatalf("status = %q, want %q", got, ScheduleStatusActive)
	}
}

func TestNormalizeInitialScheduleStatusAcceptsLifecycleStates(t *testing.T) {
	for _, status := range []string{ScheduleStatusActive, ScheduleStatusPaused, ScheduleStatusDisabled} {
		got, err := normalizeInitialScheduleStatus(" " + status + " ")
		if err != nil {
			t.Fatalf("normalizeInitialScheduleStatus(%q) returned error: %v", status, err)
		}
		if got != status {
			t.Fatalf("status = %q, want %q", got, status)
		}
	}
}

func TestNormalizeInitialScheduleStatusRejectsUnsupportedStatus(t *testing.T) {
	if _, err := normalizeInitialScheduleStatus("archived"); err == nil {
		t.Fatal("expected unsupported status error")
	}
}

func TestAutomationStatusForScheduleStatus(t *testing.T) {
	tests := []struct {
		schedule string
		want     string
	}{
		{schedule: ScheduleStatusActive, want: AutomationStatusActive},
		{schedule: ScheduleStatusPaused, want: AutomationStatusPaused},
		{schedule: ScheduleStatusDisabled, want: AutomationStatusDisabled},
	}
	for _, tt := range tests {
		if got := automationStatusForScheduleStatus(tt.schedule); got != tt.want {
			t.Fatalf("automationStatusForScheduleStatus(%q) = %q, want %q", tt.schedule, got, tt.want)
		}
	}
}

func TestManualScheduleFireDispatcherInputLeavesWorkerRunEmpty(t *testing.T) {
	now := time.Date(2026, 7, 6, 18, 30, 0, 0, time.UTC)
	input := manualScheduleFireDispatcherInput(now)
	if !input.Now.Equal(now) {
		t.Fatalf("Now = %s, want %s", input.Now, now)
	}
	if input.WorkerRunID != "" {
		t.Fatalf("WorkerRunID = %q, want empty for manual schedule dispatch", input.WorkerRunID)
	}
}

func TestArchivedProjectRuntimeFilters(t *testing.T) {
	if hasExplicitRuntimeRef("", "  ") {
		t.Fatal("empty refs should not be explicit")
	}
	if !hasExplicitRuntimeRef("", "automation_test") {
		t.Fatal("non-empty automation ref should be explicit")
	}
	byProject := archivedProjectByProjectColumnSQL("project_id")
	for _, want := range []string{"projects.projects", "project_id", "status = 'archived'"} {
		if !strings.Contains(byProject, want) {
			t.Fatalf("project archived filter missing %q: %s", want, byProject)
		}
	}
	byAutomation := archivedProjectByAutomationColumnSQL("automation_id")
	for _, want := range []string{"automation.automations", "projects.projects", "automation_id", "status = 'archived'"} {
		if !strings.Contains(byAutomation, want) {
			t.Fatalf("automation archived filter missing %q: %s", want, byAutomation)
		}
	}
}
