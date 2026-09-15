package workers

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"
)

func TestParseTickPolicyDefaultsToManual(t *testing.T) {
	policy, err := ParseTickPolicy(nil)
	if err != nil {
		t.Fatalf("ParseTickPolicy returned error: %v", err)
	}
	if policy.Mode != TickModeManual {
		t.Fatalf("mode = %q, want %q", policy.Mode, TickModeManual)
	}
	if policy.Due(time.Now(), nil) {
		t.Fatal("manual tick policy should never be due")
	}
}

func TestParseTickPolicyInterval(t *testing.T) {
	now := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	policy, err := ParseTickPolicy(json.RawMessage(`{"mode":"interval","interval_seconds":60,"run_on_startup":true}`))
	if err != nil {
		t.Fatalf("ParseTickPolicy returned error: %v", err)
	}
	if !policy.Due(now, nil) {
		t.Fatal("run_on_startup interval should be due with nil next_run_after")
	}
	next := policy.NextAfter(now)
	if next == nil || !next.Equal(now.Add(time.Minute)) {
		t.Fatalf("next = %v, want %v", next, now.Add(time.Minute))
	}
	future := now.Add(time.Second)
	if policy.Due(now, &future) {
		t.Fatal("interval should not be due before next_run_after")
	}
	past := now.Add(-time.Second)
	if !policy.Due(now, &past) {
		t.Fatal("interval should be due after next_run_after")
	}
}

func TestDailyLocalNextAfterEuropeAmsterdamNormalAndDSTDays(t *testing.T) {
	policy, err := ParseTickPolicy(json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"daily_local","local_time":"03:15","timezone":"Europe/Amsterdam"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{name: "before normal occurrence", now: time.Date(2026, 2, 10, 1, 0, 0, 0, time.UTC), want: time.Date(2026, 2, 10, 2, 15, 0, 0, time.UTC)},
		{name: "after normal occurrence", now: time.Date(2026, 2, 10, 3, 0, 0, 0, time.UTC), want: time.Date(2026, 2, 11, 2, 15, 0, 0, time.UTC)},
		{name: "spring forward", now: time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC), want: time.Date(2026, 3, 29, 1, 15, 0, 0, time.UTC)},
		{name: "fall back", now: time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC), want: time.Date(2026, 10, 25, 2, 15, 0, 0, time.UTC)},
	} {
		t.Run(test.name, func(t *testing.T) {
			next := policy.NextAfter(test.now)
			if next == nil || !next.Equal(test.want) {
				t.Fatalf("next = %v, want %v", next, test.want)
			}
			if policy.Due(test.now, nil) {
				t.Fatal("daily_local policy must not run on startup without durable next_run_after")
			}
		})
	}
}

func TestDailyLocalDSTGapAndOverlapFireAtMostOncePerCalendarDay(t *testing.T) {
	policy := TickPolicy{Mode: TickModeDailyLocal, LocalTime: "02:30", Timezone: "Europe/Amsterdam"}

	// 02:30 does not exist on the spring-forward day, so the next occurrence is
	// the following local calendar day.
	spring := policy.NextAfter(time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC))
	wantSpring := time.Date(2026, 3, 30, 0, 30, 0, 0, time.UTC)
	if spring == nil || !spring.Equal(wantSpring) {
		t.Fatalf("spring next = %v, want %v", spring, wantSpring)
	}

	// Between the two repeated 02:30 wall-clock instants, the first occurrence
	// already owns the fall-back day; do not schedule the repeated hour again.
	fall := policy.NextAfter(time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC))
	wantFall := time.Date(2026, 10, 26, 1, 30, 0, 0, time.UTC)
	if fall == nil || !fall.Equal(wantFall) {
		t.Fatalf("fall next = %v, want %v", fall, wantFall)
	}
}

func TestParseTickPolicyRejectsInvalidDailyLocalShapes(t *testing.T) {
	for _, raw := range []string{
		`{"mode":"daily_local","local_time":"3:15","timezone":"Europe/Amsterdam"}`,
		`{"mode":"daily_local","local_time":"24:00","timezone":"Europe/Amsterdam"}`,
		`{"mode":"daily_local","local_time":"03:60","timezone":"Europe/Amsterdam"}`,
		`{"mode":"daily_local","local_time":"03:15","timezone":" Europe/Amsterdam"}`,
		`{"mode":"daily_local","local_time":"03:15","timezone":"Local"}`,
		`{"mode":"daily_local","local_time":"03:15","timezone":"Not/A_Zone"}`,
		`{"mode":"daily_local","local_time":"03:15","timezone":"Europe/Amsterdam","interval_seconds":0}`,
		`{"mode":"daily_local","local_time":"03:15","timezone":"Europe/Amsterdam","run_on_startup":false}`,
		`{"mode":"interval","interval_seconds":60,"local_time":"03:15"}`,
	} {
		if _, err := ParseTickPolicy(json.RawMessage(raw)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("ParseTickPolicy(%s) error = %v, want ErrInvalid", raw, err)
		}
	}
}

func TestParseTickPolicyRejectsTooLowInterval(t *testing.T) {
	_, err := ParseTickPolicy(json.RawMessage(`{"mode":"interval","interval_seconds":1}`))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestParseTickPolicyRejectsDurationOverflow(t *testing.T) {
	_, err := ParseTickPolicy(json.RawMessage(fmt.Sprintf(`{"mode":"interval","interval_seconds":%d}`, uint64(math.MaxInt64/(3*int64(time.Second)))+1)))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestTickPolicyFreshnessWindow(t *testing.T) {
	window := (TickPolicy{Mode: TickModeInterval, IntervalSeconds: 60}).FreshnessWindow()
	if window.WarningAfter != 2*time.Minute || window.CriticalAfter != 3*time.Minute {
		t.Fatalf("window = %+v", window)
	}
	if got := (TickPolicy{Mode: TickModeManual}).FreshnessWindow(); got != (FreshnessDeadlines{}) {
		t.Fatalf("manual window = %+v", got)
	}
}

func TestTickPolicyFingerprintUsesNormalizedPolicy(t *testing.T) {
	first, err := TickPolicyFingerprint(json.RawMessage(`{"mode":"interval","run_on_startup":true,"interval_seconds":60}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := TickPolicyFingerprint(json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","interval_seconds":60,"mode":"interval","run_on_startup":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first == "" {
		t.Fatalf("normalized fingerprints = %q and %q", first, second)
	}
	normalized, raw, err := NormalizeTickPolicy(TickPolicy{Mode: TickModeManual, IntervalSeconds: 99, RunOnStartup: true})
	if err != nil {
		t.Fatal(err)
	}
	if normalized.SchemaVersion != TickPolicySchemaVersion || normalized.Mode != TickModeManual || normalized.IntervalSeconds != 0 || normalized.RunOnStartup || string(raw) != `{"schema_version":"worker_tick_policy.v0.2","mode":"manual"}` {
		t.Fatalf("normalized manual policy = %#v raw=%s", normalized, raw)
	}
	_, intervalRaw, err := NormalizeTickPolicy(TickPolicy{Mode: TickModeInterval, IntervalSeconds: 60, RunOnStartup: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(intervalRaw) != `{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":60,"run_on_startup":true}` {
		t.Fatalf("normalized v0.2 interval policy changed: %s", intervalRaw)
	}
	daily, dailyRaw, err := NormalizeTickPolicy(TickPolicy{Mode: TickModeDailyLocal, LocalTime: "03:15", Timezone: "Europe/Amsterdam"})
	if err != nil {
		t.Fatal(err)
	}
	if daily.IntervalSeconds != 0 || daily.RunOnStartup || string(dailyRaw) != `{"schema_version":"worker_tick_policy.v0.2","mode":"daily_local","local_time":"03:15","timezone":"Europe/Amsterdam"}` {
		t.Fatalf("normalized daily policy = %#v raw=%s", daily, dailyRaw)
	}
}
