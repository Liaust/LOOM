package automation

import (
	"testing"
	"time"

	"loom.local/loom/internal/requestctx"
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

func TestScheduleFireLatenessDescription(t *testing.T) {
	at := time.Date(2026, 10, 14, 18, 10, 0, 0, time.UTC)
	for _, late := range []time.Duration{0, time.Nanosecond, 60 * time.Millisecond, time.Second} {
		_, misfire, _ := schedulerFireDecision(MisfireProfile{Policy: MisfireRunIfLateWithin, LatenessWindowSeconds: 60}, at.Add(late), at)
		f := ScheduleFire{MisfireStatus: misfire, LatenessSeconds: int(late / time.Second)}
		if got := f.LatenessDescription() != ""; got != (late > 0 && late < time.Second) {
			t.Fatalf("lateness %s: %q", late, f.LatenessDescription())
		}
	}
}

func TestSchedulerFireDecisionPreciseBoundaries(t *testing.T) {
	scheduledFor := time.Date(2026, 10, 12, 12, 0, 0, 123456789, time.UTC)
	for _, tc := range []struct {
		name    string
		profile MisfireProfile
		late    time.Duration
		status  string
		misfire string
		invoke  bool
	}{
		{"strict on time", MisfireProfile{Policy: MisfireMarkMissed}, 0, FireStatusCreated, fireMisfireNone, true},
		{"strict fraction late", MisfireProfile{Policy: MisfireMarkMissed}, time.Nanosecond, FireStatusMissed, fireMisfireMissed, false},
		{"strict early", MisfireProfile{Policy: MisfireMarkMissed}, -time.Nanosecond, FireStatusCreated, fireMisfireNone, false},
		{"bounded early", MisfireProfile{Policy: MisfireRunIfLateWithin, LatenessWindowSeconds: 120}, -time.Nanosecond, FireStatusCreated, fireMisfireNone, false},
		{"bounded on time", MisfireProfile{Policy: MisfireRunIfLateWithin, LatenessWindowSeconds: 120}, 0, FireStatusCreated, fireMisfireNone, true},
		{"bounded fraction late", MisfireProfile{Policy: MisfireRunIfLateWithin, LatenessWindowSeconds: 120}, time.Nanosecond, FireStatusCreated, fireMisfireLateRun, true},
		{"just inside", MisfireProfile{Policy: MisfireRunIfLateWithin, LatenessWindowSeconds: 120}, 120*time.Second - time.Nanosecond, FireStatusCreated, fireMisfireLateRun, true},
		{"exact boundary", MisfireProfile{Policy: MisfireRunIfLateWithin, LatenessWindowSeconds: 120}, 120 * time.Second, FireStatusCreated, fireMisfireLateRun, true},
		{"just outside", MisfireProfile{Policy: MisfireRunIfLateWithin, LatenessWindowSeconds: 120}, 120*time.Second + time.Nanosecond, FireStatusMissed, fireMisfireMissed, false},
		{"fraction outside", MisfireProfile{Policy: MisfireRunIfLateWithin, LatenessWindowSeconds: 120}, 120*time.Second + 500*time.Millisecond, FireStatusMissed, fireMisfireMissed, false},
		{"large window", MisfireProfile{Policy: MisfireRunIfLateWithin, LatenessWindowSeconds: 10000000000}, time.Hour, FireStatusCreated, fireMisfireLateRun, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, misfire, invoke := schedulerFireDecision(tc.profile, scheduledFor.Add(tc.late), scheduledFor)
			if status != tc.status || misfire != tc.misfire || invoke != tc.invoke {
				t.Fatalf("decision = (%q, %q, %v), want (%q, %q, %v)", status, misfire, invoke, tc.status, tc.misfire, tc.invoke)
			}
		})
	}
}

func TestSchedulerRejectsEarlyOccurrenceBeforeTransactionWork(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	due := now.Add(time.Nanosecond)
	_, err := (Service{}).processDueScheduleTx(t.Context(), nil, requestctx.Context{}, Schedule{ScheduleID: "future", NextFireAt: &due}, SchedulerRunInput{Now: now, LookaheadWindow: time.Minute})
	if err == nil {
		t.Fatal("future occurrence was consumed")
	}
}
