package jobs

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOverviewBoundsAndCompactJSON(t *testing.T) {
	for _, tc := range []struct {
		hours, limit, wantLimit, buckets int
	}{{1, 0, 30, 60}, {24, -1, 30, 96}, {24, 1, 1, 96}, {1, 100, 100, 60}, {24, 101, 100, 96}} {
		t.Run(fmt.Sprintf("hours_%d_limit_%d", tc.hours, tc.limit), func(t *testing.T) {
			store, service := newOverviewTestService(t)
			store.queries = []overviewTestQuery{
				{columns: 3, rows: [][]driver.Value{{int64(500), int64(200), int64(3)}}},
				{columns: 4, rows: [][]driver.Value{{int64(0), int64(2), int64(1), int64(0)}, {int64(tc.buckets - 1), int64(0), int64(1), int64(3)}}},
				{columns: 15, check: func(query string, args []driver.NamedValue) {
					if !strings.Contains(query, "ORDER BY j.updated_at DESC, j.job_id DESC LIMIT $3") {
						t.Fatal("recent items lack a stable bounded ordering")
					}
					if args[2].Value != int64(tc.wantLimit+1) {
						t.Fatalf("lookahead limit = %v, want %d", args[2].Value, tc.wantLimit+1)
					}
					end := args[1].Value.(time.Time)
					store.itemTime = end.Add(-time.Second)
				}, item: true},
			}
			got, err := service.Overview(t.Context(), OverviewFilter{Hours: tc.hours, Limit: tc.limit})
			if err != nil {
				t.Fatal(err)
			}
			if got.Counts != (OverviewCounts{Queued: 500, Running: 200, Attention: 3}) || len(got.Items) != 1 {
				t.Fatalf("counts must not derive from the recent page: %+v", got)
			}
			if !store.committed || !store.options.ReadOnly || store.options.Isolation != driver.IsolationLevel(sql.LevelRepeatableRead) {
				t.Fatalf("not a committed read-only consistent snapshot: %+v", store.options)
			}
			if !got.ObservedAt.Equal(got.Window.End) || got.Window.End.Sub(got.Window.Start) != time.Duration(tc.hours)*time.Hour || len(got.Buckets) != tc.buckets {
				t.Fatalf("bad overview window/buckets: %+v", got.Window)
			}
			if got.ItemsWindow == nil || got.ItemsWindow.End.Sub(got.ItemsWindow.Start) != 24*time.Hour || got.ItemsTruncated {
				t.Fatalf("default all items must be a bounded recent 24h view: %+v", got.ItemsWindow)
			}
			stride := got.Window.End.Sub(got.Window.Start) / time.Duration(tc.buckets)
			for i, bucket := range got.Buckets {
				if !bucket.At.Equal(got.Window.Start.Add(time.Duration(i) * stride)) {
					t.Fatalf("bucket %d is not aligned to the requested window", i)
				}
				if i != 0 && i != tc.buckets-1 && (bucket.Succeeded != 0 || bucket.Failed != 0 || bucket.Cancelled != 0) {
					t.Fatalf("empty bucket %d is not zero-filled: %+v", i, bucket)
				}
			}
			if got.Buckets[0].Succeeded != 2 || got.Buckets[0].Failed != 1 || got.Buckets[tc.buckets-1].Cancelled != 3 {
				t.Fatalf("outcome aggregate not mapped: %+v", got.Buckets)
			}
			if got.Items[0].ID != "job_recent" || got.Items[0].JobID != "job_recent" || got.Items[0].QueuedAt != nil || got.Items[0].CompletedAt == nil || !got.Items[0].ManualActionRequired || *got.Items[0].ScopeID != "scope_project" {
				t.Fatalf("bad compact item: %+v", got.Items[0])
			}
			payload, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Items []map[string]json.RawMessage `json:"items"`
			}
			if err := json.Unmarshal(payload, &wire); err != nil {
				t.Fatal(err)
			}
			allowed := strings.Fields("id job_id job_type display_name failure_code status created_at updated_at queued_at started_at completed_at failed_at cancelled_at failure_attention_status manual_action_required scope_id")
			if len(wire.Items[0]) != len(allowed) {
				t.Fatalf("unexpected compact JSON fields: %s", payload)
			}
			for _, field := range allowed {
				if _, ok := wire.Items[0][field]; !ok {
					t.Fatalf("missing JSON field %q", field)
				}
			}
		})
	}
}

func TestOverviewEmptyAndProjectScope(t *testing.T) {
	store, service := newOverviewTestService(t)
	now := time.Now().UTC()
	store.queries = []overviewTestQuery{
		{columns: 22, rows: [][]driver.Value{{"project_test", "scope_project", "project:test", "test", "Test", "", "actor_test", "actor_test", nil, nil, nil, "active", "normal", "generic", []byte(`{}`), []byte(`{}`), []byte(`{}`), []byte(`{}`), []byte(`{}`), []byte(`{}`), now, now}}, check: func(_ string, args []driver.NamedValue) {
			if args[0].Value != "test" {
				t.Fatalf("untrimmed project ref: %v", args)
			}
		}},
		{columns: 3, rows: [][]driver.Value{{int64(0), int64(0), int64(0)}}, check: func(query string, args []driver.NamedValue) {
			if strings.Count(query, "j.scope_id = $1") != 3 || len(args) != 1 || args[0].Value != "scope_project" {
				t.Fatalf("counts do not share project scope: %s %v", query, args)
			}
		}},
		{columns: 4, check: func(query string, args []driver.NamedValue) {
			if strings.Count(query, "j.scope_id = $4") != 3 || len(args) != 4 || args[3].Value != "scope_project" {
				t.Fatalf("outcomes do not share project scope: %s %v", query, args)
			}
		}},
		{columns: 15, check: func(query string, args []driver.NamedValue) {
			if !strings.Contains(query, "j.scope_id = $3") || len(args) != 4 || args[2].Value != "scope_project" {
				t.Fatalf("items do not share project scope: %s %v", query, args)
			}
		}},
	}
	got, err := service.Overview(t.Context(), OverviewFilter{Hours: 24, ProjectRef: " test "})
	if err != nil {
		t.Fatal(err)
	}
	if got.Items == nil || len(got.Items) != 0 || len(got.Buckets) != 96 || got.Counts != (OverviewCounts{}) {
		t.Fatalf("empty response is not a successful zero observation: %+v", got)
	}
}

func TestOverviewValidationAndReadErrors(t *testing.T) {
	for _, hours := range []int{-1, 0, 2, 25} {
		if _, err := (Service{}).Overview(t.Context(), OverviewFilter{Hours: hours}); err == nil || !strings.Contains(err.Error(), "hours") {
			t.Fatalf("accepted invalid hours %d: %v", hours, err)
		}
	}
	if _, err := (Service{}).Overview(t.Context(), OverviewFilter{Hours: 1}); err == nil {
		t.Fatal("accepted unconfigured service")
	}
	sentinel := errors.New("read failed")
	for _, stage := range []string{"project", "counts", "outcomes", "items", "item_stream", "commit"} {
		t.Run(stage, func(t *testing.T) {
			store, service := newOverviewTestService(t)
			filter := OverviewFilter{Hours: 1}
			store.queries = []overviewTestQuery{
				{columns: 3, rows: [][]driver.Value{{int64(0), int64(0), int64(0)}}},
				{columns: 4}, {columns: 15},
			}
			switch stage {
			case "project":
				filter.ProjectRef = "missing"
				store.queries = []overviewTestQuery{{err: sentinel}}
			case "counts":
				store.queries[0].err = sentinel
			case "outcomes":
				store.queries[1].err = sentinel
			case "items":
				store.queries[2].err = sentinel
			case "item_stream":
				store.queries[2].streamErr = sentinel
			case "commit":
				store.commitErr = sentinel
			}
			got, err := service.Overview(t.Context(), filter)
			if !errors.Is(err, sentinel) || !reflect.DeepEqual(got, Overview{}) || store.committed {
				t.Fatalf("read error became a successful/partial observation: %+v %v", got, err)
			}
			if stage != "project" && stage != "commit" && !store.rolledBack {
				t.Fatal("failed read did not release its transaction")
			}
		})
	}
	store, service := newOverviewTestService(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := service.Overview(ctx, OverviewFilter{Hours: 1}); !errors.Is(err, context.Canceled) || store.committed {
		t.Fatalf("cancellation not preserved: %v", err)
	}
}

func TestOverviewSQLUsesCurrentPredicatesAndBoundedTerminalRanges(t *testing.T) {
	counts, outcomes := overviewQueries(false)
	items, _ := overviewItemsQuery("all", &OverviewWindow{}, "", 30)
	for _, want := range []string{
		"WHERE j.status = '" + StatusQueued + "'",
		"WHERE j.status = '" + StatusRunning + "'",
		"j.failure_attention_status = '" + FailureAttentionStatusActive + "'",
		"(j.status IN ('failed', 'timed_out') OR j.manual_action_required = true)",
	} {
		if !strings.Contains(counts, want) {
			t.Fatalf("missing current predicate %q: %s", want, counts)
		}
	}
	for _, field := range []string{"completed_at", "failed_at", "cancelled_at"} {
		if !strings.Contains(outcomes, "j."+field+" >= $1 AND j."+field+" < $2") {
			t.Fatalf("unbounded outcome timestamp %s: %s", field, outcomes)
		}
	}
	if strings.Contains(outcomes, "COALESCE") || strings.Contains(outcomes, "failure_attention_status") || strings.Count(outcomes, "UNION ALL") != 2 {
		t.Fatalf("outcomes must use disjoint latest terminal states, independent of attention: %s", outcomes)
	}
	if !strings.Contains(items, "j.updated_at >= $1 AND j.updated_at < $2") {
		t.Fatal("recent item range is unbounded")
	}
	for _, forbidden := range []string{"input", "output", "prompt", "config", "metadata", "log", "SELECT *"} {
		if strings.Contains(items, forbidden) {
			t.Fatalf("compact read selects %q", forbidden)
		}
	}
}

func TestOverviewItemStateAndRangeQueries(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	for _, state := range []string{"all", "running", "queued", "attention", "completed"} {
		t.Run(state, func(t *testing.T) {
			window, err := overviewItemsWindow(OverviewFilter{Hours: 1, State: state}, now)
			if err != nil {
				t.Fatal(err)
			}
			query, args := overviewItemsQuery(state, window, "scope_a", 31)
			switch state {
			case "all":
				if window == nil || window.End.Sub(window.Start) != 24*time.Hour {
					t.Fatalf("all default range: %+v", window)
				}
			case "completed":
				if window == nil || window.End.Sub(window.Start) != time.Hour || strings.Contains(query, "AND j.updated_at") {
					t.Fatalf("completed must use the outcome range: %s %+v", query, window)
				}
				for _, field := range []string{"completed_at", "failed_at", "cancelled_at"} {
					if !strings.Contains(query, "j."+field+" >= $1 AND j."+field+" < $2") {
						t.Fatalf("missing %s terminal range: %s", field, query)
					}
				}
				if !strings.Contains(query, "ORDER BY CASE") {
					t.Fatal("terminal items not ordered by their outcome timestamp")
				}
			default:
				if window != nil || strings.Contains(query, "updated_at >=") || len(args) != 2 || args[0] != "scope_a" {
					t.Fatalf("current-state drill-down lost old jobs: %s %v", query, args)
				}
				if state == "attention" {
					if !strings.Contains(query, overviewAttentionPredicate) {
						t.Fatal("attention items do not use the counts' distinct predicate")
					}
				} else if !strings.Contains(query, "AND j.status = '"+state+"'") {
					t.Fatalf("missing explicit current state predicate: %s", query)
				}
			}
			if args[len(args)-1] != 31 {
				t.Fatalf("missing source-side bounded lookahead: %v", args)
			}
			if state != "completed" && !strings.Contains(query, "ORDER BY j.updated_at DESC, j.job_id DESC") {
				t.Fatal("unstable recent ordering")
			}
		})
	}
}

func TestOverviewItemsWindowValidation(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	past := now.Add(-48 * time.Hour)
	pastEnd := past.Add(time.Hour)
	minuteEnd := past.Add(time.Minute)
	quarterHourEnd := past.Add(15 * time.Minute)
	halfHour := now.Add(-30 * time.Minute)
	zero := time.Time{}
	for _, tc := range []struct {
		name         string
		hours        int
		since, until *time.Time
		want         OverviewWindow
		invalid      bool
	}{
		{"original_bucket", 1, &past, &pastEnd, OverviewWindow{Start: past, End: pastEnd}, false},
		{"one_hour_chart_bucket", 1, &past, &minuteEnd, OverviewWindow{Start: past, End: minuteEnd}, false},
		{"daily_chart_bucket", 24, &past, &quarterHourEnd, OverviewWindow{Start: past, End: quarterHourEnd}, false},
		{"since_only", 1, &halfHour, nil, OverviewWindow{Start: halfHour, End: now}, false},
		{"until_only", 24, nil, &pastEnd, OverviewWindow{Start: pastEnd.Add(-24 * time.Hour), End: pastEnd}, false},
		{"empty", 1, &past, &past, OverviewWindow{}, true},
		{"reversed", 1, &pastEnd, &past, OverviewWindow{}, true},
		{"too_wide", 1, &past, &now, OverviewWindow{}, true},
		{"zero_since", 24, &zero, &now, OverviewWindow{}, true},
		{"zero_until", 24, nil, &zero, OverviewWindow{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filter := OverviewFilter{Hours: tc.hours, State: "all", Since: tc.since, Until: tc.until}
			got, err := overviewItemsWindow(filter, now)
			if tc.invalid {
				if err == nil {
					t.Fatalf("accepted invalid item range: %+v", got)
				}
				if _, err := (Service{}).Overview(t.Context(), filter); err == nil || !strings.Contains(err.Error(), "item range") {
					t.Fatalf("range not validated before database access: %v", err)
				}
			} else if err != nil || got == nil || !got.Start.Equal(tc.want.Start) || !got.End.Equal(tc.want.End) {
				t.Fatalf("got=%+v err=%v want=%+v", got, err, tc.want)
			}
		})
	}
	if _, err := (Service{}).Overview(t.Context(), OverviewFilter{Hours: 1, State: "unknown"}); err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("accepted unknown state: %v", err)
	}
}

func TestOverviewItemsTruncationAndIndependentGraphWindow(t *testing.T) {
	for _, count := range []int{2, 3, 4} {
		t.Run(fmt.Sprintf("rows_%d", count), func(t *testing.T) {
			store, service := newOverviewTestService(t)
			since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			until := since.Add(time.Minute)
			var rows [][]driver.Value
			for i := range count {
				rows = append(rows, []driver.Value{fmt.Sprintf("job_%d", i), TypeScriptRun, StatusCompleted, since, until, nil, nil, since, nil, nil, FailureAttentionStatusArchived, false, nil})
			}
			store.queries = []overviewTestQuery{
				{columns: 3, rows: [][]driver.Value{{int64(5), int64(2), int64(1)}}},
				{columns: 4, check: func(_ string, args []driver.NamedValue) {
					if !args[1].Value.(time.Time).After(until) {
						t.Fatal("item drill-down rewound the main graph window")
					}
				}},
				{columns: 15, rows: rows, check: func(query string, args []driver.NamedValue) {
					if !args[0].Value.(time.Time).Equal(since) || !args[1].Value.(time.Time).Equal(until) || args[2].Value != int64(4) || strings.Contains(query, "AND j.updated_at") {
						t.Fatalf("item bounds/filter not applied at source: %s %v", query, args)
					}
				}},
			}
			got, err := service.Overview(t.Context(), OverviewFilter{Hours: 1, Limit: 3, State: "completed", Since: &since, Until: &until})
			if err != nil {
				t.Fatal(err)
			}
			wantItems := min(count, 3)
			if len(got.Items) != wantItems || got.ItemsTruncated != (count > 3) || got.Counts != (OverviewCounts{Queued: 5, Running: 2, Attention: 1}) || len(got.Buckets) != 60 || !got.ItemsWindow.Start.Equal(since) || !got.ItemsWindow.End.Equal(until) {
				t.Fatalf("bad bounded drill-down metadata: %+v", got)
			}
		})
	}
}

type overviewTestQuery struct {
	columns   int
	rows      [][]driver.Value
	check     func(string, []driver.NamedValue)
	err       error
	streamErr error
	item      bool
}

type overviewTestStore struct {
	t                     *testing.T
	queries               []overviewTestQuery
	options               driver.TxOptions
	committed, rolledBack bool
	commitErr             error
	itemTime              time.Time
}

func newOverviewTestService(t *testing.T) (*overviewTestStore, Service) {
	t.Helper()
	store := &overviewTestStore{t: t}
	db := sql.OpenDB(overviewTestConnector{store})
	t.Cleanup(func() { db.Close() })
	return store, Service{DB: db}
}

type overviewTestConnector struct{ store *overviewTestStore }

func (c overviewTestConnector) Connect(context.Context) (driver.Conn, error) {
	return overviewTestConn{c.store}, nil
}
func (overviewTestConnector) Driver() driver.Driver { return overviewTestDriver{} }

type overviewTestDriver struct{}

func (overviewTestDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use connector")
}

type overviewTestConn struct{ store *overviewTestStore }

func (overviewTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (overviewTestConn) Close() error { return nil }
func (overviewTestConn) Begin() (driver.Tx, error) {
	return nil, errors.New("use BeginTx")
}
func (c overviewTestConn) BeginTx(_ context.Context, options driver.TxOptions) (driver.Tx, error) {
	c.store.options = options
	return overviewTestTx{c.store}, nil
}
func (c overviewTestConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if len(c.store.queries) == 0 {
		return nil, fmt.Errorf("unexpected query: %s", query)
	}
	expected := c.store.queries[0]
	c.store.queries = c.store.queries[1:]
	if expected.check != nil {
		expected.check(query, args)
	}
	if expected.err != nil {
		return nil, expected.err
	}
	if expected.item {
		now := c.store.itemTime
		expected.rows = [][]driver.Value{{"job_recent", TypeScriptRun, StatusCompleted, now, now, nil, nil, now, nil, nil, FailureAttentionStatusArchived, true, "scope_project"}}
	}
	if expected.columns == 15 {
		for i, row := range expected.rows {
			if len(row) == 13 {
				expected.rows[i] = append(row, "Example task", "")
			}
		}
	}
	return &overviewTestRows{query: expected}, nil
}

type overviewTestTx struct{ store *overviewTestStore }

func (tx overviewTestTx) Commit() error {
	if tx.store.commitErr != nil {
		return tx.store.commitErr
	}
	tx.store.committed = true
	return nil
}
func (tx overviewTestTx) Rollback() error { tx.store.rolledBack = true; return nil }

type overviewTestRows struct{ query overviewTestQuery }

func (r *overviewTestRows) Columns() []string {
	columns := make([]string, r.query.columns)
	for i := range columns {
		columns[i] = fmt.Sprintf("column_%d", i)
	}
	return columns
}
func (*overviewTestRows) Close() error { return nil }
func (r *overviewTestRows) Next(dest []driver.Value) error {
	if len(r.query.rows) == 0 {
		if r.query.streamErr != nil {
			return r.query.streamErr
		}
		return io.EOF
	}
	copy(dest, r.query.rows[0])
	r.query.rows = r.query.rows[1:]
	return nil
}
