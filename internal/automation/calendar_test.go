package automation

import (
	"strings"
	"testing"
	"time"
)

func calendarTime(t *testing.T, s string) time.Time {
	t.Helper()
	out, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCalendarOccurrences(t *testing.T) {
	for _, tt := range []struct{ name, expr, zone, after, want string }{
		{"utc", "15 9 * * *", "UTC", "2026-09-10T09:15:00Z", "2026-09-11T09:15:00Z"},
		{"amsterdam", "15 9 * * *", "Europe/Amsterdam", "2026-09-10T07:14:59Z", "2026-09-10T07:15:00Z"},
		{"multiple daily", "15 9,13,18 * * *", "Europe/Amsterdam", "2026-09-10T07:15:00Z", "2026-09-10T11:15:00Z"},
		{"leap", "0 0 29 2 *", "UTC", "2025-03-01T00:00:00Z", "2028-02-29T00:00:00Z"},
		{"dom dow OR", "0 0 13 * MON", "UTC", "2026-09-10T00:00:00Z", "2026-09-13T00:00:00Z"},
		{"spring skips gap", "30 2 * * *", "Europe/Amsterdam", "2026-03-28T01:30:00Z", "2026-03-30T00:30:00Z"},
		{"fold first", "30 2 * * *", "Europe/Amsterdam", "2026-10-24T23:00:00Z", "2026-10-25T00:30:00Z"},
		{"fold after first", "30 2 * * *", "Europe/Amsterdam", "2026-10-25T00:30:00Z", "2026-10-26T01:30:00Z"},
		{"fold between copies", "30 2 * * *", "Europe/Amsterdam", "2026-10-25T01:15:00Z", "2026-10-26T01:30:00Z"},
		{"fold minute wildcard", "* * * * *", "Europe/Amsterdam", "2026-10-25T00:59:00Z", "2026-10-25T02:00:00Z"},
		{"fold second copy", "* * * * *", "Europe/Amsterdam", "2026-10-25T01:20:00Z", "2026-10-25T02:00:00Z"},
		{"half hour fold", "45 1 * * *", "Australia/Lord_Howe", "2026-04-04T14:45:00Z", "2026-04-05T15:15:00Z"},
		{"strict subseconds", "* * * * *", "UTC", "2026-09-10T09:15:00.000000001Z", "2026-09-10T09:16:00Z"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			after := calendarTime(t, tt.after)
			plan, err := ParseScheduleExpressionInTimezone(ScheduleKindCron, tt.expr, tt.zone, after)
			if err != nil {
				t.Fatal(err)
			}
			want := calendarTime(t, tt.want)
			if plan.NextFireAt == nil || !plan.NextFireAt.Equal(want) || plan.NextFireAt.Location() != time.UTC {
				t.Fatalf("next = %v, want UTC %v", plan.NextFireAt, want)
			}
			next, err := ComputeNextFire(Schedule{ScheduleKind: ScheduleKindCron, ScheduleExpr: tt.expr, Timezone: tt.zone}, after)
			if err != nil || next == nil || !next.Equal(want) {
				t.Fatalf("recompute = %v, %v; want %v", next, err, want)
			}
		})
	}
}

func TestCalendarRejectsInvalidContract(t *testing.T) {
	for _, expr := range []string{"", "  ", "* * * *", "0 * * * * *", "* * * * * 2026", "@daily", "CRON_TZ=UTC 0 9 * * *", "TZ=UTC * * * *", "60 * * * *", "*/0 * * * *", "x x x x x", strings.Repeat(" ", 257) + "* * * * *", strings.Repeat("1,", 200) + "1 * * * *", "0 0 30 2 *"} {
		t.Run(expr, func(t *testing.T) {
			if _, err := ParseScheduleExpressionInTimezone(ScheduleKindCron, expr, "UTC", calendarTime(t, "2026-01-01T00:00:00Z")); err == nil {
				t.Fatal("accepted invalid cron")
			}
		})
	}
	for _, zone := range []string{"", " ", "Local", "not/a/timezone", "+02:00"} {
		if _, err := ParseScheduleExpressionInTimezone(ScheduleKindCron, "* * * * *", zone, time.Now()); err == nil {
			t.Fatalf("accepted timezone %q", zone)
		}
	}
	if _, err := ParseScheduleExpression(ScheduleKindCron, "* * * * *", time.Now()); err == nil {
		t.Fatal("legacy API accepted implicit cron zone")
	}
}

func TestCalendarPreservesOldKinds(t *testing.T) {
	after := calendarTime(t, "2026-03-28T09:00:00Z")
	for _, tt := range []struct{ kind, expr string }{{ScheduleKindOneShot, "now"}, {ScheduleKindOneShot, "2026-09-10T09:00:00+02:00"}, {ScheduleKindInterval, "24h"}} {
		old, err := ParseScheduleExpression(tt.kind, tt.expr, after)
		if err != nil {
			t.Fatal(err)
		}
		zoned, err := ParseScheduleExpressionInTimezone(tt.kind, tt.expr, "Europe/Amsterdam", after)
		if err != nil || !old.NextFireAt.Equal(*zoned.NextFireAt) {
			t.Fatalf("old-kind initial behavior changed: %v", err)
		}
		next, err := ComputeNextFire(Schedule{ScheduleKind: tt.kind, ScheduleExpr: tt.expr, Timezone: "Europe/Amsterdam"}, after)
		if err != nil {
			t.Fatal(err)
		}
		if tt.kind == ScheduleKindOneShot && next != nil {
			t.Fatal("one-shot repeated")
		}
		if tt.kind == ScheduleKindInterval && (next == nil || !next.Equal(after.Add(24*time.Hour))) {
			t.Fatal("interval changed to wall-clock duration")
		}
	}
}

func TestCalendarNoFutureHorizon(t *testing.T) {
	// 2100 is not a leap year; the next match is beyond the pinned parser horizon.
	_, err := ParseScheduleExpressionInTimezone(ScheduleKindCron, "0 0 29 2 *", "UTC", calendarTime(t, "2096-03-01T00:00:00Z"))
	if err == nil || !strings.Contains(err.Error(), "five-year search horizon") {
		t.Fatalf("no-future error = %v", err)
	}
}
