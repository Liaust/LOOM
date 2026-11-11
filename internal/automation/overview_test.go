package automation

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOverviewFilterBoundsAndCursor(t *testing.T) {
	filter, cursor, err := normalizeOverviewFilter(OverviewFilter{})
	if err != nil || filter.Days != 1 || filter.Limit != 50 || cursor != nil {
		t.Fatalf("defaults: %+v %v %v", filter, cursor, err)
	}
	for _, filter := range []OverviewFilter{
		{Days: -1}, {Days: 8}, {Limit: -1}, {Limit: 101}, {Status: "archived"},
		{Cursor: "bogus"}, {Cursor: strings.Repeat("a", 4097)},
	} {
		if _, _, err := normalizeOverviewFilter(filter); err == nil {
			t.Fatalf("accepted %+v", filter)
		}
	}
	if _, _, err := normalizeOverviewFilter(OverviewFilter{Days: 7, Limit: 100}); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(overviewCursor{CreatedAt: calendarTime(t, "2026-11-02T00:00:00Z"), ScheduleID: "schedule_a", Status: "paused"})
	filter = OverviewFilter{Cursor: base64.RawURLEncoding.EncodeToString(encoded), Status: " paused "}
	if _, cursor, err := normalizeOverviewFilter(filter); err != nil || cursor.ScheduleID != "schedule_a" {
		t.Fatalf("cursor roundtrip: %+v %v", cursor, err)
	}
	filter.Status = "active"
	if _, _, err := normalizeOverviewFilter(filter); err == nil {
		t.Fatal("cursor reused with changed filter")
	}
	if _, err := (Service{}).Overview(t.Context(), OverviewFilter{}); err == nil {
		t.Fatal("nil database accepted")
	}
}

func TestOverviewOccurrenceTiming(t *testing.T) {
	for _, tc := range []struct {
		name, kind, expr, zone, next, start, lower, end string
		status, want                                    string
	}{
		{name: "spring skips gap", kind: "cron", expr: "30 2 * * *", zone: "Europe/Amsterdam", next: "2026-03-28T01:30:00Z", lower: "2026-03-29T00:00:00Z", end: "2026-03-31T00:00:00Z", want: "2026-03-30T00:30:00Z"},
		{name: "fold only first copy", kind: "cron", expr: "30 2 * * *", zone: "Europe/Amsterdam", next: "2026-10-25T00:30:00Z", lower: "2026-10-25T00:00:00Z", end: "2026-10-26T02:00:00Z", want: "2026-10-25T00:30:00Z,2026-10-26T01:30:00Z"},
		{name: "fold inside repeated hour", kind: "cron", expr: "30 2 * * *", zone: "Europe/Amsterdam", next: "2026-10-25T00:30:00Z", lower: "2026-10-25T01:15:00Z", end: "2026-10-26T02:00:00Z", want: "2026-10-26T01:30:00Z"},
		{name: "start policy", kind: "cron", expr: "0 * * * *", zone: "UTC", next: "2026-11-02T00:00:00Z", start: "2026-11-02T02:00:00Z", lower: "2026-11-02T00:00:00Z", end: "2026-11-02T03:00:00Z", want: "2026-11-02T02:00:00Z"},
		{name: "interval keeps phase", kind: "interval", expr: "1h", next: "2020-01-01T00:15:00Z", lower: "2026-11-02T00:30:00Z", end: "2026-11-02T02:15:00Z", want: "2026-11-02T01:15:00Z"},
		{name: "interval inclusive start", kind: "interval", expr: "1h", next: "2020-01-01T00:15:00Z", lower: "2026-11-02T00:15:00Z", end: "2026-11-02T01:15:00Z", want: "2026-11-02T00:15:00Z"},
		{name: "interval start bound", kind: "interval", expr: "1h", next: "2026-11-02T00:15:00Z", start: "2026-11-02T01:30:00Z", lower: "2026-11-02T00:00:00Z", end: "2026-11-02T03:00:00Z", want: "2026-11-02T02:15:00Z"},
		{name: "once persisted now", kind: "one_shot", expr: "now", next: "2026-11-02T01:00:00Z", lower: "2026-11-02T00:00:00Z", end: "2026-11-03T00:00:00Z", want: "2026-11-02T01:00:00Z"},
		{name: "overdue once not rearmed", kind: "one_shot", expr: "now", next: "2026-11-01T00:00:00Z", lower: "2026-11-02T00:00:00Z", end: "2026-11-03T00:00:00Z"},
		{name: "paused", kind: "interval", expr: "1h", next: "2026-11-02T01:00:00Z", status: "paused", lower: "2026-11-02T00:00:00Z", end: "2026-11-03T00:00:00Z"},
		{name: "disabled", kind: "interval", expr: "1h", next: "2026-11-02T01:00:00Z", status: "disabled", lower: "2026-11-02T00:00:00Z", end: "2026-11-03T00:00:00Z"},
		{name: "completed", kind: "one_shot", expr: "now", next: "2026-11-02T01:00:00Z", status: "completed", lower: "2026-11-02T00:00:00Z", end: "2026-11-03T00:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := calendarTime(t, tc.next)
			status := tc.status
			if status == "" {
				status = ScheduleStatusActive
			}
			schedule := Schedule{ScheduleID: "schedule_a", Status: status, ScheduleKind: tc.kind, ScheduleExpr: tc.expr, Timezone: tc.zone, NextFireAt: &at}
			if tc.start != "" {
				start := calendarTime(t, tc.start)
				schedule.StartAt = &start
			}
			before, _ := json.Marshal(schedule)
			got, truncated, unavailable, err := overviewOccurrences(t.Context(), []overviewRow{{Item: OverviewSchedule{ScheduleID: schedule.ScheduleID}, Timing: schedule}}, OverviewWindow{Start: calendarTime(t, tc.lower), End: calendarTime(t, tc.end)})
			if err != nil || truncated || len(unavailable) != 0 {
				t.Fatalf("preview: %+v %v %v %v", got, truncated, unavailable, err)
			}
			var timestamps []string
			for _, occurrence := range got {
				timestamps = append(timestamps, occurrence.At.Format(time.RFC3339))
			}
			if strings.Join(timestamps, ",") != tc.want {
				t.Fatalf("got %v, want %s", timestamps, tc.want)
			}
			after, _ := json.Marshal(schedule)
			if string(before) != string(after) {
				t.Fatal("preview changed schedule")
			}
		})
	}
}

func TestOverviewOccurrencesFairBoundAndUnavailable(t *testing.T) {
	start := calendarTime(t, "2026-11-02T00:00:00Z")
	selected := []overviewRow{}
	for index := range 100 {
		id := fmt.Sprintf("schedule_%03d", index)
		expr := "1s"
		if index == 99 {
			expr = "12h"
		}
		selected = append(selected, overviewRow{Item: OverviewSchedule{ScheduleID: id}, Timing: Schedule{ScheduleID: id,
			Status: "active", ScheduleKind: "interval", ScheduleExpr: expr, NextFireAt: &start}})
	}
	window := OverviewWindow{Start: start, End: start.Add(24 * time.Hour)}
	got, truncated, unavailable, err := overviewOccurrences(t.Context(), selected, window)
	if err != nil || !truncated || len(got) != 500 || len(unavailable) != 0 {
		t.Fatalf("bound: len=%d truncated=%v unavailable=%v err=%v", len(got), truncated, unavailable, err)
	}
	counts := map[string]int{}
	for index, occurrence := range got {
		counts[occurrence.ScheduleID]++
		if index > 0 && got[index-1].At.After(occurrence.At) {
			t.Fatal("occurrences not chronological")
		}
	}
	if len(counts) != 100 || counts["schedule_099"] != 2 {
		t.Fatalf("frequent schedule starved slow schedule: %v", counts)
	}
	selected = selected[:3]
	selected[0].Timing.NextFireAt = nil
	selected[1].Timing.ScheduleExpr = "invalid"
	selected[2].Timing.Status = "paused"
	got, truncated, unavailable, err = overviewOccurrences(t.Context(), selected, window)
	if err != nil || len(got) != 0 || truncated || !reflect.DeepEqual(unavailable, []string{"schedule_000", "schedule_001"}) {
		t.Fatalf("unavailable timing: %v %v %v %v", got, truncated, unavailable, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, _, err := overviewOccurrences(ctx, selected, window); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	// Exactly 500 matches is complete, not a false-positive cap warning.
	selected = []overviewRow{{Item: OverviewSchedule{ScheduleID: "schedule_exact"}, Timing: Schedule{Status: "active", ScheduleKind: "interval", ScheduleExpr: "1s", NextFireAt: &start}}}
	got, truncated, _, err = overviewOccurrences(t.Context(), selected, OverviewWindow{Start: start, End: start.Add(500 * time.Second)})
	if err != nil || truncated || len(got) != 500 {
		t.Fatalf("exact bound: %d %v %v", len(got), truncated, err)
	}
}

func TestOverviewReadOnlyPagingAndExactCounts(t *testing.T) {
	created := calendarTime(t, "2026-11-01T00:00:00Z")
	values := [][]driver.Value{}
	for index := 2; index >= 0; index-- {
		values = append(values, overviewTestValues(fmt.Sprintf("schedule_%d", index), created))
	}
	queries := 0
	db := overviewTestDB(t, func(query string, args []driver.NamedValue) (driver.Rows, error) {
		queries++
		if !strings.Contains(query, "archived_project.status = 'archived'") {
			t.Fatal("lost default archive exclusion")
		}
		for _, forbidden := range []string{"input_json", "profile_json", "result_json", "failure_message", "FOR UPDATE", "INSERT INTO", "UPDATE automation"} {
			if strings.Contains(query, forbidden) {
				t.Fatalf("unsafe overview query contains %s", forbidden)
			}
		}
		if strings.HasPrefix(query, "SELECT") {
			if len(args) != 0 || strings.Contains(query, "schedules.created_at, schedules.schedule_id") {
				t.Fatal("cursor narrowed exact counts")
			}
			return &overviewTestRows{values: [][]driver.Value{{int64(1), int64(1), int64(0), int64(3)}}}, nil
		}
		if !strings.Contains(query, "ORDER BY schedules.created_at DESC, schedules.schedule_id DESC") || !strings.Contains(query, "LEFT JOIN jobs.jobs") {
			t.Fatal("missing stable order or bulk result evidence")
		}
		if queries == 2 {
			if len(args) != 1 || args[0].Value != int64(3) {
				t.Fatalf("page bound: %v", args)
			}
			return &overviewTestRows{values: values}, nil
		}
		if !strings.Contains(query, "(schedules.created_at, schedules.schedule_id) < ($1, $2)") ||
			len(args) != 3 || args[0].Value != created || args[1].Value != "schedule_1" {
			t.Fatalf("continuation: %s %v", query, args)
		}
		return &overviewTestRows{values: values[2:]}, nil
	})
	svc := Service{DB: db}
	page, err := svc.Overview(t.Context(), OverviewFilter{Limit: 2})
	if err != nil || page.Complete || page.NextCursor == "" || len(page.Items) != 2 || !page.PreviewLimited || page.Coverage.Complete {
		t.Fatalf("first page: %+v %v", page, err)
	}
	if page.Counts != (OverviewCounts{Active: 1, Paused: 1, Total: 3}) || page.Coverage.TotalSchedules != 3 || page.Coverage.SelectedSchedules != 2 {
		t.Fatalf("counts are not inventory-wide: %+v", page)
	}
	if !page.Window.Start.Equal(page.ObservedAt) || page.Window.End.Sub(page.Window.Start) != 24*time.Hour {
		t.Fatalf("window: %+v", page.Window)
	}
	last, err := svc.Overview(t.Context(), OverviewFilter{Limit: 2, Cursor: page.NextCursor})
	if err != nil || !last.Complete || last.NextCursor != "" || len(last.Items) != 1 || last.Items[0].ScheduleID != "schedule_0" || !last.PreviewLimited {
		t.Fatalf("last page: %+v %v", last, err)
	}
	if queries != 4 {
		t.Fatalf("per-item fanout: %d queries", queries)
	}
}

func TestOverviewResultsAreCompactEvidence(t *testing.T) {
	now := calendarTime(t, "2026-11-02T00:00:00Z")
	for _, tc := range []struct {
		name, fire, invocation, jobID, job, source, status string
	}{
		{name: "no evidence"},
		{name: "dispatch only", fire: "completed", invocation: "succeeded", source: "invocation", status: "dispatched"},
		{name: "fire only", fire: "completed", source: "schedule_fire", status: "dispatched"},
		{name: "running job overrides dispatch", fire: "completed", invocation: "succeeded", jobID: "job_a", job: "running", source: "job", status: "running"},
		{name: "completed job", fire: "completed", invocation: "succeeded", jobID: "job_a", job: "completed", source: "job", status: "completed"},
		{name: "missing linked job", fire: "completed", invocation: "succeeded", jobID: "job_a"},
		{name: "missed", fire: "missed", source: "schedule_fire", status: "missed"},
		{name: "approval", fire: "invocation_created", invocation: "waiting_approval", source: "invocation", status: "waiting_approval"},
		{name: "untrusted status", fire: "secret-value", source: "schedule_fire", status: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := overviewTestValues("schedule_a", now)
			for _, field := range []struct {
				index int
				value string
			}{{13, tc.fire}, {15, tc.invocation}, {17, tc.jobID}, {18, tc.job}} {
				if field.value != "" {
					values[field.index] = field.value
				}
			}
			values[14], values[16], values[19] = now, now, now
			values[1] = "Display\x1b\nName"
			rows := &overviewTestRows{values: [][]driver.Value{values}}
			db := overviewTestDB(t, func(string, []driver.NamedValue) (driver.Rows, error) { return rows, nil })
			sqlRows, err := db.QueryContext(t.Context(), "SELECT fixture")
			if err != nil {
				t.Fatal(err)
			}
			defer sqlRows.Close()
			sqlRows.Next()
			row, err := scanOverviewRow(sqlRows)
			if err != nil {
				t.Fatal(err)
			}
			if tc.source == "" {
				if row.Item.LastResult != nil {
					t.Fatalf("invented result: %+v", row.Item.LastResult)
				}
			} else if got := row.Item.LastResult; got == nil || got.Source != tc.source || got.Status != tc.status || !got.At.Equal(now) {
				t.Fatalf("result: %+v", got)
			}
			if row.Item.DisplayName != "Display  Name" || !row.Item.MetadataLimited {
				t.Fatalf("display sanitation: %+v", row.Item)
			}
			raw, _ := json.Marshal(row.Item)
			for _, forbidden := range []string{"secret-value", "input_json", "prompt", "config", "profile", "failure_message"} {
				if strings.Contains(string(raw), forbidden) {
					t.Fatalf("leaked %s: %s", forbidden, raw)
				}
			}
		})
	}
}

func TestOverviewInventoryBeyondDefaultPageAndPayloadBound(t *testing.T) {
	for _, longMetadata := range []bool{false, true} {
		t.Run(fmt.Sprintf("long_metadata_%v", longMetadata), func(t *testing.T) {
			created := calendarTime(t, "2026-11-01T00:00:00Z")
			values := [][]driver.Value{}
			for index := 74; index >= 0; index-- {
				row := overviewTestValues(fmt.Sprintf("schedule_%03d", index), created)
				if longMetadata {
					row[1], row[5], row[6] = strings.Repeat("<", 256), strings.Repeat("<", 256), strings.Repeat("<", 256)
				}
				values = append(values, row)
			}
			db := overviewTestDB(t, func(query string, args []driver.NamedValue) (driver.Rows, error) {
				if strings.HasPrefix(query, "SELECT") {
					return &overviewTestRows{values: [][]driver.Value{{int64(0), int64(75), int64(0), int64(75)}}}, nil
				}
				remaining := values
				if len(args) > 1 {
					id := args[1].Value.(string)
					remaining = nil
					for _, row := range values {
						if row[0].(string) < id {
							remaining = append(remaining, row)
						}
					}
				}
				limit := int(args[len(args)-1].Value.(int64))
				if len(remaining) > limit {
					remaining = remaining[:limit]
				}
				return &overviewTestRows{values: remaining}, nil
			})
			svc := Service{DB: db}
			filter := OverviewFilter{}
			if longMetadata {
				filter.Limit = 100
			}
			seen := map[string]bool{}
			for pageNumber := 0; ; pageNumber++ {
				if pageNumber > 10 {
					t.Fatal("cursor made no progress")
				}
				page, err := svc.Overview(t.Context(), filter)
				if err != nil {
					t.Fatal(err)
				}
				if !longMetadata && pageNumber == 0 && len(page.Items) != 50 {
					t.Fatalf("default page size: %d", len(page.Items))
				}
				raw, _ := json.Marshal(page)
				if len(raw) > maxOverviewBytes || page.Counts.Total != 75 {
					t.Fatalf("unbounded response or page-local count: %d %+v", len(raw), page.Counts)
				}
				for _, item := range page.Items {
					if seen[item.ScheduleID] {
						t.Fatalf("duplicate %s", item.ScheduleID)
					}
					seen[item.ScheduleID] = true
				}
				if page.Complete {
					break
				}
				if page.NextCursor == "" {
					t.Fatal("truncation lost continuation")
				}
				filter.Cursor = page.NextCursor
			}
			if len(seen) != 75 {
				t.Fatalf("inventory stopped early: %d", len(seen))
			}
		})
	}
}

func TestOverviewEmptyAndUnavailableCoverage(t *testing.T) {
	for _, withUnavailable := range []bool{false, true} {
		t.Run(fmt.Sprintf("unavailable_%v", withUnavailable), func(t *testing.T) {
			values := [][]driver.Value{}
			if withUnavailable {
				row := overviewTestValues("schedule_missing_next", time.Now().UTC())
				row[3] = "active"
				values = append(values, row)
			}
			db := overviewTestDB(t, func(query string, args []driver.NamedValue) (driver.Rows, error) {
				if strings.HasPrefix(query, "SELECT") {
					return &overviewTestRows{values: [][]driver.Value{{int64(len(values)), int64(0), int64(0), int64(len(values))}}}, nil
				}
				return &overviewTestRows{values: values}, nil
			})
			page, err := (Service{DB: db}).Overview(t.Context(), OverviewFilter{Days: 7})
			if err != nil || !page.Complete || page.PreviewLimited != withUnavailable || page.Coverage.Complete == withUnavailable || page.Window.End.Sub(page.Window.Start) != 7*24*time.Hour {
				t.Fatalf("coverage: %+v %v", page, err)
			}
			if page.Occurrences == nil || page.Items == nil || page.Coverage.UnavailableScheduleIDs == nil {
				t.Fatal("successful empty arrays must not be null")
			}
		})
	}
}

func overviewTestValues(id string, created time.Time) []driver.Value {
	return []driver.Value{id, "Name", "key", "paused", "interval", "1h", "UTC", nil, nil, nil, nil, created, false,
		nil, nil, nil, nil, nil, nil, nil}
}

type overviewTestConnector struct {
	t     *testing.T
	query func(string, []driver.NamedValue) (driver.Rows, error)
}

func overviewTestDB(t *testing.T, query func(string, []driver.NamedValue) (driver.Rows, error)) *sql.DB {
	t.Helper()
	db := sql.OpenDB(overviewTestConnector{t: t, query: query})
	t.Cleanup(func() { db.Close() })
	return db
}

func (c overviewTestConnector) Connect(context.Context) (driver.Conn, error) { return c, nil }
func (c overviewTestConnector) Driver() driver.Driver                        { return overviewTestDriver{} }
func (c overviewTestConnector) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepared statement")
}
func (c overviewTestConnector) Close() error { return nil }
func (c overviewTestConnector) Begin() (driver.Tx, error) {
	return nil, errors.New("expected explicit read-only transaction")
}
func (c overviewTestConnector) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if !opts.ReadOnly || opts.Isolation != driver.IsolationLevel(sql.LevelRepeatableRead) {
		c.t.Fatalf("not a read-only snapshot: %+v", opts)
	}
	return overviewTestTx{}, nil
}
func (c overviewTestConnector) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.query(query, args)
}

type overviewTestDriver struct{}

func (overviewTestDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type overviewTestTx struct{}

func (overviewTestTx) Commit() error   { return nil }
func (overviewTestTx) Rollback() error { return nil }

type overviewTestRows struct {
	values [][]driver.Value
	index  int
}

func (r *overviewTestRows) Columns() []string {
	if len(r.values) == 0 {
		return make([]string, 20)
	}
	return make([]string, len(r.values[0]))
}
func (r *overviewTestRows) Close() error { return nil }
func (r *overviewTestRows) Next(dest []driver.Value) error {
	if r.index == len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}
