package portal

import (
	"context"
	"errors"
	"loom.local/loom/internal/automation"
	"reflect"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/hermesschedules"
	"loom.local/loom/internal/response"
)

type hermesScheduleFake struct {
	Client
	observation hermesschedules.Observation
	err         error
}

func (c hermesScheduleFake) HermesSchedules(context.Context, string) (response.Envelope[hermesschedules.Observation], error) {
	return response.Success("test", c.observation), c.err
}

func TestHermesSchedulePortalReadOnlyAndFailureTruth(t *testing.T) {
	enabled := false
	for _, tc := range []struct {
		name        string
		observation hermesschedules.Observation
		err         error
		want        string
	}{
		{"empty", hermesschedules.Observation{Availability: hermesschedules.Available, Jobs: []hermesschedules.Job{}}, nil, "No jobs in the observed native store"},
		{"paused", hermesschedules.Observation{Availability: hermesschedules.Available, Jobs: []hermesschedules.Job{{ID: "012345abcdef", Enabled: &enabled, NativeState: hermesschedules.Paused}}}, nil, "enabled=false  stored_state=paused"},
		{"malformed", hermesschedules.Observation{Availability: hermesschedules.Malformed}, nil, "not an empty schedule list"},
		{"transport", hermesschedules.Observation{}, errors.New("PRIVATE_SENTINEL"), "availability=unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obs := loadHermesSchedules(context.Background(), hermesScheduleFake{observation: tc.observation, err: tc.err}, "test")
			var b strings.Builder
			renderHermesSchedules(&b, obs)
			s := b.String()
			if !strings.Contains(s, tc.want) || !strings.Contains(s, "Hermes Schedules (read-only)") || strings.Contains(s, "PRIVATE_SENTINEL") {
				t.Fatal(s)
			}
			if !strings.Contains(s, "scheduler_liveness=unknown") {
				t.Fatal(s)
			}
		})
	}
	if got := loadHermesSchedules(context.Background(), &fakePortalClient{}, "test"); got != nil {
		t.Fatalf("legacy client became observed: %+v", got)
	}
}

func TestHermesSchedulePortalDisplayAndActionIsolation(t *testing.T) {
	when, _ := time.Parse(time.RFC3339Nano, "2026-09-28T17:00:00.123456+02:00")
	expr, minutes, delivery := "0 9 * * MON-FRI", int64(30), true
	obs := &hermesschedules.Observation{Owner: "hermes", Availability: hermesschedules.Available, AttemptedAt: when, ObservedAt: &when, StoreUpdatedAt: &when,
		Jobs: []hermesschedules.Job{
			{ID: "native-cron", Schedule: hermesschedules.Schedule{Kind: hermesschedules.Cron, Expression: &expr}, NextRunAt: &when, PausedAt: &when, LastRunAt: &when, DeliveryErrorReported: &delivery},
			{ID: "native-interval", Schedule: hermesschedules.Schedule{Kind: hermesschedules.Interval, Minutes: &minutes}},
			{ID: "native-once", Schedule: hermesschedules.Schedule{Kind: hermesschedules.Once, RunAt: &when}},
		},
	}
	var b strings.Builder
	renderHermesSchedules(&b, obs)
	for _, want := range []string{`expression="0 9 * * MON-FRI"`, "minutes=30", "run_at=2026-09-28T17:00:00.123456+02:00", "paused_at=2026-09-28T17:00:00.123456+02:00", "stored_next=2026-09-28T17:00:00.123456+02:00", "result_at=2026-09-28T17:00:00.123456+02:00", "observed=2026-09-28T17:00:00.123456+02:00", "store_updated=2026-09-28T17:00:00.123456+02:00", "delivery=unknown", "delivery=error_reported", "last_result=unknown", "stored_state=unknown", "current_completion=unknown"} {
		if !strings.Contains(b.String(), want) {
			t.Fatalf("missing %q in %s", want, b.String())
		}
	}
	state := NewScreenState(ScreenAutomations)
	state.Data.Automations.Schedules = []automation.Schedule{{ScheduleID: "loom-owned", Status: "active"}}
	for _, raw := range []bool{false, true} {
		state.RawDetails = raw
		state.Data.Automations.HermesSchedules = nil
		before := ScreenSelectableItems(state)
		beforeActions := ScreenAvailableActionsForRecords(state, ScreenRecordItems(state))
		beforeGroups := automationSurfaceGroups(state.Data.Automations)
		state.Data.Automations.HermesSchedules = obs
		if !reflect.DeepEqual(before, ScreenSelectableItems(state)) || !reflect.DeepEqual(beforeActions, ScreenAvailableActionsForRecords(state, ScreenRecordItems(state))) || !reflect.DeepEqual(beforeGroups, automationSurfaceGroups(state.Data.Automations)) {
			t.Fatal("native inventory entered LOOM selections, action targets or schedule groups")
		}
	}
	obs.Availability = hermesschedules.Malformed
	b.Reset()
	renderHermesSchedules(&b, obs)
	if strings.Contains(b.String(), "native-cron") || strings.Contains(b.String(), "No jobs in") {
		t.Fatal("failed inventory displayed stale jobs as usable")
	}
}

type cancelingHermesScheduleClient struct {
	Client
	cancel context.CancelFunc
	called bool
}

func (c *cancelingHermesScheduleClient) HermesSchedules(ctx context.Context, _ string) (response.Envelope[hermesschedules.Observation], error) {
	c.called = true
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
		panic("unbounded Hermes read")
	}
	c.cancel()
	return response.Success("test", hermesschedules.Observation{Availability: hermesschedules.Available, Jobs: []hermesschedules.Job{}}), nil
}
func TestHermesSchedulePortalCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &cancelingHermesScheduleClient{cancel: cancel}
	out := loadHermesSchedules(ctx, client, "test")
	if !client.called || out.Availability != hermesschedules.Unavailable || out.Jobs != nil || out.ObservedAt != nil {
		t.Fatal("late success after cancellation accepted", out)
	}
	client.called = false
	loadHermesSchedules(ctx, client, "test")
	if client.called {
		t.Fatal("read with canceled context")
	}
}
