package httpapi

import (
	"errors"
	"fmt"
	"testing"

	loomerrors "loom.local/loom/internal/errors"
)

func TestScheduleResponsePreservesActionableError(t *testing.T) {
	for _, code := range []string{"schedule.disabled", "schedule.target_unavailable"} {
		cause := loomerrors.New(code, "automation", "pilot", "Actionable schedule error")
		got := scheduleResponseError(fmt.Errorf("wrapped: %w", cause), "schedule.fire_failed", "other", "Could not fire schedule.")
		if got != cause {
			t.Fatal(got)
		}
	}
	got := scheduleResponseError(errors.New("private database detail"), "schedule.fire_failed", "pilot", "Could not fire schedule.")
	if got.Summary != "Could not fire schedule." || got.Code != "schedule.fire_failed" {
		t.Fatal(got)
	}
}
