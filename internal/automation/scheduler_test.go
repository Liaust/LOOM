package automation

import (
	"testing"
	"time"
)

func TestSchedulerFireDecisionMarkMissed(t *testing.T) {
	scheduledFor := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	status, misfireStatus, shouldInvoke := schedulerFireDecision(MisfireProfile{Policy: MisfireMarkMissed}, scheduledFor, scheduledFor)
	if status != FireStatusCreated || misfireStatus != fireMisfireNone || !shouldInvoke {
		t.Fatalf("on-time decision = (%q, %q, %v), want created none true", status, misfireStatus, shouldInvoke)
	}

	status, misfireStatus, shouldInvoke = schedulerFireDecision(MisfireProfile{Policy: MisfireMarkMissed}, scheduledFor.Add(time.Second), scheduledFor)
	if status != FireStatusMissed || misfireStatus != fireMisfireMissed || shouldInvoke {
		t.Fatalf("late decision = (%q, %q, %v), want missed missed false", status, misfireStatus, shouldInvoke)
	}
}

func TestSchedulerFireDecisionRunIfLateWithin(t *testing.T) {
	scheduledFor := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	profile := MisfireProfile{Policy: MisfireRunIfLateWithin, LatenessWindowSeconds: 60}

	status, misfireStatus, shouldInvoke := schedulerFireDecision(profile, scheduledFor.Add(30*time.Second), scheduledFor)
	if status != FireStatusCreated || misfireStatus != fireMisfireLateRun || !shouldInvoke {
		t.Fatalf("within-window decision = (%q, %q, %v), want created late_run true", status, misfireStatus, shouldInvoke)
	}

	status, misfireStatus, shouldInvoke = schedulerFireDecision(profile, scheduledFor.Add(61*time.Second), scheduledFor)
	if status != FireStatusMissed || misfireStatus != fireMisfireMissed || shouldInvoke {
		t.Fatalf("outside-window decision = (%q, %q, %v), want missed missed false", status, misfireStatus, shouldInvoke)
	}
}
