package workers

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestHealthFailedAndTimeout(t *testing.T) {
	finished := time.Now().UTC()
	for _, terminal := range []string{RunStatusFailed, RunStatusTimedOut} {
		for _, count := range []int{1, 3} {
			got, err := failedHealthUpdate(WorkerInstance{}, WorkerRun{FinishedAt: &finished}, count, context.DeadlineExceeded)
			if terminal == RunStatusTimedOut {
				got, err = timedOutHealthUpdate("instance", "run", finished, count, context.DeadlineExceeded)
			}
			want, severity := HealthDegraded, SeverityWarning
			if count == 3 {
				want, severity = HealthFailed, SeverityCritical
			}
			if err != nil || got.Status != want || got.Severity != severity || !got.AttentionRequired || got.ConsecutiveFailures != count || got.LastFailureAt == nil || !got.LastFailureAt.Equal(finished) || got.LastSuccessAt != nil {
				t.Fatalf("health=%+v err=%v", got, err)
			}
			var detail map[string]any
			if err := json.Unmarshal(got.DetailsJSON, &detail); err != nil || detail["last_terminal_status"] != terminal {
				t.Fatalf("detail=%v err=%v", detail, err)
			}
		}
	}
}

func TestHealthPartialPhaseQualification(t *testing.T) {
	got, err := succeededHealthUpdate(WorkerInstance{Enabled: true}, WorkerRun{
		ResultSummaryJSON: json.RawMessage(`{"admission_timed_out":true,"admission_warning":{"phase":"catalog_hydration","reason":"deadline_exceeded","elapsed_ms":120,"budget_ms":100,"retryable":true,"cause":"secret"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var detail struct {
		Warning map[string]any `json:"admission_warning"`
	}
	if err := json.Unmarshal(got.DetailsJSON, &detail); err != nil || detail.Warning["phase"] != "catalog_hydration" || detail.Warning["elapsed_ms"] != float64(120) || len(detail.Warning) != 5 {
		t.Fatalf("warning=%v err=%v", detail.Warning, err)
	}
	if admissionHealthWarning(json.RawMessage(`{"admission_warning":{"phase":"private source","reason":"deadline_exceeded"}}`)) != nil {
		t.Fatal("forwarded arbitrary phase")
	}
}

func TestHealthSucceededAndPartial(t *testing.T) {
	finished := time.Now().UTC()
	instance := WorkerInstance{WorkerInstanceID: "worker_instance_test", Enabled: true, ConsecutiveFails: 2}
	for _, tc := range []struct {
		name, summary, status string
		partial               bool
	}{
		{"complete", `{}`, HealthHealthy, false},
		{"partial", `{"admission_timed_out":true,"processed":3}`, HealthDegraded, true},
		{"newer complete", `{"admission_timed_out":false}`, HealthHealthy, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := WorkerRun{RunStatus: RunStatusSucceeded, WorkerRunID: "worker_run_test", CorrelationID: "corr_test", FinishedAt: &finished, Retryable: tc.partial, ResultSummaryJSON: json.RawMessage(tc.summary)}
			got, err := succeededHealthUpdate(instance, run)
			if err != nil || got.Status != tc.status || got.LastSuccessAt != &finished {
				t.Fatalf("health=%+v err=%v", got, err)
			}
			if tc.partial {
				if got.Severity != SeverityWarning || !got.AttentionRequired || !got.PreserveQueueDepth || got.ConsecutiveFailures != 2 {
					t.Fatal("lost partial qualification/failure history/queue", got)
				}
				var detail map[string]any
				if err := json.Unmarshal(got.DetailsJSON, &detail); err != nil || detail["reason"] != "admission_timeout" || detail["last_terminal_status"] != RunStatusSucceeded || detail["retryable"] != true || detail["correlation_id"] != "corr_test" {
					t.Fatalf("partial detail=%v err=%v", detail, err)
				}
			} else if got.Severity != SeverityInfo || got.AttentionRequired || got.ConsecutiveFailures != 0 {
				t.Fatal("fully complete run did not restore health", got)
			}
		})
	}
}

func TestHealthSucceededDisabledAndPaused(t *testing.T) {
	for _, instance := range []WorkerInstance{
		{Enabled: false}, {Enabled: true, LifecycleStatus: LifecycleDisabled},
		{Enabled: true, LifecycleStatus: LifecycleRetired}, {Enabled: true, Paused: true},
	} {
		got, err := succeededHealthUpdate(instance, WorkerRun{ResultSummaryJSON: json.RawMessage(`{"admission_timed_out":true}`)})
		want := HealthDisabled
		if instance.Paused {
			want = HealthPaused
		}
		if err != nil || got.Status != want {
			t.Fatalf("health=%+v err=%v want=%s", got, err, want)
		}
	}
}

func TestWorkspaceHealthDoesNotClaimHeldCollectionsDelivered(t *testing.T) {
	for _, held := range []bool{false, true} {
		holds := `[]`
		if held {
			holds = `[{"failure":"custody_unavailable"}]`
		}
		got, err := succeededHealthUpdate(WorkerInstance{Enabled: true, WorkerKind: "notes_workspace_sync"}, WorkerRun{
			ResultSummaryJSON: json.RawMessage(`{"held":0,"retained_holds":73,"enrollment":{"holds":` + holds + `,"collections":[{"source_intake_stage":"hashing"}]}}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		var details map[string]any
		if err := json.Unmarshal(got.DetailsJSON, &details); err != nil {
			t.Fatal(err)
		}
		if details["source_intake_pending"] != float64(1) || details["client_delivery_verified"] != false {
			t.Fatal("lost delivery qualification", details)
		}
		if held != got.AttentionRequired || held != (got.Status == HealthDegraded) {
			t.Fatalf("held=%v health=%+v", held, got)
		}
	}
}
