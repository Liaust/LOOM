package automation

import (
	"testing"
	"time"
)

func TestScheduleTimingRequiresValidExplicitTiming(t *testing.T) {
	now := time.Date(2026, 10, 12, 18, 45, 0, 0, time.UTC)
	valid := ScheduleTimingInput{Cron: "0 5 * * 1", Timezone: "Europe/Amsterdam", MisfirePolicy: MisfireRunIfLateWithin, LatenessWindowSeconds: 120}
	plan, profile, err := normalizeScheduleTiming(valid, now)
	if err != nil || plan.NextFireAt == nil || !plan.NextFireAt.Equal(time.Date(2026, 10, 19, 3, 0, 0, 0, time.UTC)) || profile.LatenessWindowSeconds != 120 {
		t.Fatalf("plan=%+v profile=%+v err=%v", plan, profile, err)
	}
	for _, field := range []string{"cron", "timezone", "policy", "window"} {
		bad := valid
		switch field {
		case "cron":
			bad.Cron = "bad"
		case "timezone":
			bad.Timezone = ""
		case "policy":
			bad.MisfirePolicy = "bad"
		case "window":
			bad.LatenessWindowSeconds = -1
		}
		if _, _, err := normalizeScheduleTiming(bad, now); err == nil {
			t.Fatalf("accepted invalid %s", field)
		}
	}
}
