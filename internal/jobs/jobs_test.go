package jobs

import "testing"

func TestValidStatus(t *testing.T) {
	for _, status := range []string{
		StatusCreated,
		StatusQueued,
		StatusRunning,
		StatusCompleted,
		StatusFailed,
		StatusCancelled,
		StatusTimedOut,
	} {
		if !ValidStatus(status) {
			t.Fatalf("expected status %q to be valid", status)
		}
	}
	if ValidStatus("waiting_for_resource") {
		t.Fatal("unexpected advanced status accepted in Slice 5 v0")
	}
}

func TestNormalizeScriptRunMode(t *testing.T) {
	for _, mode := range []string{
		ScriptRunEnqueueOnly,
		ScriptRunWaitUntilStarted,
		ScriptRunWaitForCompletion,
	} {
		if got := NormalizeScriptRunMode(mode); got != mode {
			t.Fatalf("NormalizeScriptRunMode(%q) = %q", mode, got)
		}
	}
	if got := NormalizeScriptRunMode(""); got != ScriptRunWaitForCompletion {
		t.Fatalf("empty mode = %q, want wait_for_completion", got)
	}
	if got := NormalizeScriptRunMode("inline"); got != "" {
		t.Fatalf("invalid mode = %q, want empty", got)
	}
}

func TestIsTerminalStatus(t *testing.T) {
	for _, status := range []string{StatusCompleted, StatusFailed, StatusCancelled, StatusTimedOut} {
		if !IsTerminalStatus(status) {
			t.Fatalf("expected %q to be terminal", status)
		}
	}
	if IsTerminalStatus(StatusQueued) {
		t.Fatal("queued should not be terminal")
	}
}

func TestFailureAttentionStatusHelpers(t *testing.T) {
	for _, status := range []string{
		FailureAttentionStatusActive,
		FailureAttentionStatusAcknowledged,
		FailureAttentionStatusArchived,
	} {
		if !ValidFailureAttentionStatus(status) {
			t.Fatalf("expected attention status %q to be valid", status)
		}
		if got := NormalizeFailureAttentionStatus("  " + status + "  "); got != status {
			t.Fatalf("NormalizeFailureAttentionStatus(%q) = %q", status, got)
		}
	}
	if ValidFailureAttentionStatus("retired") {
		t.Fatal("unexpected attention status accepted")
	}
	if got := EffectiveFailureAttentionStatus(""); got != FailureAttentionStatusActive {
		t.Fatalf("empty effective attention status = %q, want active", got)
	}
}

func TestJobNeedsFailureAttention(t *testing.T) {
	for _, job := range []Job{
		{Status: StatusFailed},
		{Status: StatusTimedOut},
		{Status: StatusRunning, ManualAction: true},
	} {
		if !JobNeedsFailureAttention(job) {
			t.Fatalf("expected job %#v to need failure attention", job)
		}
	}
	if JobNeedsFailureAttention(Job{Status: StatusCompleted}) {
		t.Fatal("completed job should not need failure attention")
	}
}

func TestFailureAttentionStatusForTransition(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{StatusFailed, FailureAttentionStatusActive},
		{StatusTimedOut, FailureAttentionStatusActive},
		{StatusCompleted, FailureAttentionStatusArchived},
		{StatusCancelled, FailureAttentionStatusArchived},
		{StatusQueued, ""},
		{StatusRunning, ""},
	}
	for _, test := range tests {
		if got := FailureAttentionStatusForTransition(test.status); got != test.want {
			t.Fatalf("FailureAttentionStatusForTransition(%q) = %q, want %q", test.status, got, test.want)
		}
	}
}

func TestJobAttentionStatusesFromFilter(t *testing.T) {
	statuses, err := jobAttentionStatusesFromFilter(ListFilter{})
	if err != nil {
		t.Fatalf("default filter returned error: %v", err)
	}
	if len(statuses) != 1 || statuses[0] != FailureAttentionStatusActive {
		t.Fatalf("default statuses = %#v, want active only", statuses)
	}

	statuses, err = jobAttentionStatusesFromFilter(ListFilter{IncludeAcknowledged: true, IncludeArchived: true})
	if err != nil {
		t.Fatalf("include filter returned error: %v", err)
	}
	want := []string{FailureAttentionStatusActive, FailureAttentionStatusAcknowledged, FailureAttentionStatusArchived}
	if len(statuses) != len(want) {
		t.Fatalf("statuses = %#v, want %#v", statuses, want)
	}
	for i := range want {
		if statuses[i] != want[i] {
			t.Fatalf("statuses = %#v, want %#v", statuses, want)
		}
	}

	statuses, err = jobAttentionStatusesFromFilter(ListFilter{AttentionStatus: FailureAttentionStatusAll})
	if err != nil {
		t.Fatalf("all filter returned error: %v", err)
	}
	if statuses != nil {
		t.Fatalf("all statuses = %#v, want nil", statuses)
	}

	if _, err := jobAttentionStatusesFromFilter(ListFilter{AttentionStatus: "retired"}); err == nil {
		t.Fatal("expected invalid attention status error")
	}
}
