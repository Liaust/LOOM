package automation

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

const maxCalendarExpressionBytes = 256

// Only the parser/next-occurrence API is used; durable LOOM claims remain the
// scheduler. The pinned parser searches at most five years into the future.
// Expressions without a match in that horizon fail rather than poll forever.
var calendarParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func nextCalendarFire(expr, timezone string, after time.Time) (time.Time, error) {
	if len(expr) > maxCalendarExpressionBytes {
		return time.Time{}, fmt.Errorf("cron schedule_expr must be at most %d bytes", maxCalendarExpressionBytes)
	}
	if len(strings.Fields(expr)) != 5 || strings.ContainsAny(expr, "=@") {
		return time.Time{}, fmt.Errorf("cron schedule_expr requires exactly five fields without descriptors or inline timezone")
	}
	timezone = strings.TrimSpace(timezone)
	if timezone == "" || timezone == "Local" {
		return time.Time{}, fmt.Errorf("cron requires an explicit IANA timezone (UTC is allowed; Local is not)")
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, fmt.Errorf("timezone is invalid: %w", err)
	}
	parsed, err := calendarParser.Parse(expr)
	if err != nil {
		return time.Time{}, fmt.Errorf("cron schedule_expr is invalid: %w", err)
	}
	// The parser defaults to Local, meaning the input time's location. Pin the
	// parsed spec itself so no host-local timezone can affect computation.
	parsed.(*cron.SpecSchedule).Location = location
	for attempts := 0; attempts < 8; attempts++ {
		next := parsed.Next(after.In(location))
		if next.IsZero() {
			return time.Time{}, fmt.Errorf("cron schedule_expr has no future occurrence within the parser's five-year search horizon")
		}
		// ZoneBounds identifies the transition that introduced this UTC offset.
		// A backward transition repeats delta seconds of local time. Exclude that
		// entire second interval, including when the caller starts inside it;
		// comparing only with the caller's wall clock would replay old minutes.
		start, _ := next.ZoneBounds()
		if !start.IsZero() {
			_, offset := next.Zone()
			_, previousOffset := start.Add(-time.Nanosecond).In(location).Zone()
			delta := time.Duration(previousOffset-offset) * time.Second
			end := start.Add(delta)
			if delta > 0 && next.Before(end) {
				after = end.Add(-time.Nanosecond)
				continue
			}
		}
		return next.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("cron schedule_expr exceeded the bounded timezone-transition search")
}
