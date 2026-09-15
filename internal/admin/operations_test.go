package admin

import "testing"

func TestValidAuthorizationLevel(t *testing.T) {
	for _, level := range []int{1, 2, 3, 4, 5} {
		if !ValidAuthorizationLevel(level) {
			t.Fatalf("level %d should be valid", level)
		}
	}
	for _, level := range []int{0, 6} {
		if ValidAuthorizationLevel(level) {
			t.Fatalf("level %d should be invalid", level)
		}
	}
}

func TestValidStatus(t *testing.T) {
	for _, status := range []string{
		StatusRequested,
		StatusApproved,
		StatusRejected,
		StatusRunning,
		StatusCompleted,
		StatusFailed,
		StatusCancelled,
	} {
		if !ValidStatus(status) {
			t.Fatalf("status %q should be valid", status)
		}
	}
	if ValidStatus("unknown") {
		t.Fatal("unknown status should be invalid")
	}
}

func TestKnownOperationType(t *testing.T) {
	if !KnownOperationType(OperationIndexRebuild) {
		t.Fatalf("%s should be known", OperationIndexRebuild)
	}
	if KnownOperationType("main@admin.unknown") {
		t.Fatal("unknown operation should be invalid")
	}
}
