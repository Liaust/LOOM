package projects

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOverviewFilterValidation(t *testing.T) {
	filter, cursor, err := normalizeOverviewFilter(OverviewFilter{})
	if err != nil || filter.Limit != 24 || filter.WorkState != "all" || filter.Status != "all" || !cursor.UpdatedAt.IsZero() {
		t.Fatalf("defaults: filter=%#v cursor=%#v err=%v", filter, cursor, err)
	}
	for _, state := range []string{"all", "working", "passive", "archived", "unclassified"} {
		filter, _, err := normalizeOverviewFilter(OverviewFilter{Limit: 100, WorkState: state, Query: strings.Repeat("界", 160)})
		if err != nil || filter.WorkState != state {
			t.Fatalf("valid state/query rejected: %#v %v", filter, err)
		}
	}
	for _, status := range []string{"active", "paused", "completed", "archived", "abandoned", "blocked"} {
		if _, _, err := normalizeOverviewFilter(OverviewFilter{Status: status}); err != nil {
			t.Fatalf("valid lifecycle %q rejected: %v", status, err)
		}
	}
	for name, filter := range map[string]OverviewFilter{
		"negative limit": {Limit: -1},
		"large limit":    {Limit: 101},
		"invalid group":  {WorkState: "active"},
		"invalid status": {Status: "working"},
		"long query":     {Query: strings.Repeat("界", 161)},
		"invalid utf8":   {Query: string([]byte{0xff})},
		"nul query":      {Query: "a\x00b"},
		"invalid cursor": {Cursor: "not-base64!"},
		"long cursor":    {Cursor: strings.Repeat("x", 1025)},
	} {
		t.Run(name, func(t *testing.T) {
			// Validation must precede any database access.
			if _, err := (Service{}).Overview(context.Background(), filter); !errors.Is(err, ErrInvalidOverviewFilter) {
				t.Fatalf("validation error = %v", err)
			}
		})
	}
}

func TestOverviewCursorValidation(t *testing.T) {
	at := time.Date(2026, 11, 2, 12, 0, 0, 123456000, time.FixedZone("offset", 3600))
	want := projectOverviewCursor{UpdatedAt: at, ProjectID: "project_cursor"}
	raw, _ := json.Marshal(want)
	filter, got, err := normalizeOverviewFilter(OverviewFilter{Cursor: base64.RawURLEncoding.EncodeToString(raw)})
	if err != nil || !got.UpdatedAt.Equal(want.UpdatedAt) || got.ProjectID != want.ProjectID {
		t.Fatalf("cursor round trip: %#v %#v %v", filter, got, err)
	}
	for _, raw := range []string{
		`null`, `{}`, `[]`,
		`{"updated_at":"not-a-time","project_id":"project_x"}`,
		`{"updated_at":"0000-01-01T00:00:00Z","project_id":"project_x"}`,
		`{"updated_at":"2026-11-02T12:00:00Z","project_id":""}`,
		`{"updated_at":"2026-11-02T12:00:00Z","project_id":"scope_x"}`,
		`{"updated_at":"2026-11-02T12:00:00Z","project_id":"project_"}`,
		`{"updated_at":"2026-11-02T12:00:00Z","project_id":"project_\u0000x"}`,
		`{"updated_at":"2026-11-02T12:00:00Z","project_id":"project_x","snapshot":true}`,
		`{"updated_at":"2026-11-02T12:00:00Z","project_id":"project_x"} {}`,
	} {
		if _, _, err := normalizeOverviewFilter(OverviewFilter{Cursor: base64.RawURLEncoding.EncodeToString([]byte(raw))}); !errors.Is(err, ErrInvalidOverviewFilter) {
			t.Errorf("accepted invalid cursor %s: %v", raw, err)
		}
	}
}

func TestOverviewReadAndPagination(t *testing.T) {
	at := time.Date(2026, 11, 2, 12, 0, 0, 123456000, time.UTC)
	items := []ProjectOverviewItem{
		{ProjectID: "project_c", ProjectScopeID: "scope_c", Name: "Gamma", Slug: "gamma", Status: "active", WorkState: "working", UpdatedAt: at},
		{ProjectID: "project_b", ProjectScopeID: "scope_b", Name: "Beta", Slug: "beta", Status: "active", WorkState: "working", UpdatedAt: at},
		{ProjectID: "project_a", ProjectScopeID: "scope_a", Name: "Alpha", Slug: "alpha", Status: "active", WorkState: "working", UpdatedAt: at},
	}
	calls := 0
	service := overviewTestService(t, func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
		calls++
		if ctx.Value(overviewContextKey{}) != "preserved" {
			t.Fatal("request context was not passed to SQL")
		}
		if query != projectOverviewSQL || len(args) != 6 {
			t.Fatalf("unexpected query/args: %s %#v", query, args)
		}
		want := []any{"working", "O'Reilly_%", "active", nil, "", int64(3)}
		if calls == 2 {
			want[3], want[4] = at, "project_b"
		}
		for i, value := range want {
			if !reflect.DeepEqual(args[i].Value, value) {
				t.Fatalf("argument %d = %#v, want %#v", i+1, args[i].Value, value)
			}
		}
		page := items
		if calls == 2 {
			page = items[2:]
		}
		return overviewTestResult(at, 900, ProjectOverviewCounts{Working: 300, Passive: 200, Archived: 100, Unclassified: 300}, 300, page), nil
	})
	ctx := context.WithValue(context.Background(), overviewContextKey{}, "preserved")
	filter := OverviewFilter{Limit: 2, WorkState: "working", Status: "active", Query: " O'Reilly_% "}
	first, err := service.Overview(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 900 || first.FilteredTotal != 300 || first.Counts.Working != 300 || first.Complete || first.NextCursor == "" || len(first.Items) != 2 || !first.ObservedAt.Equal(at) {
		t.Fatalf("incorrect first page: %#v", first)
	}
	_, cursor, err := normalizeOverviewFilter(OverviewFilter{Cursor: first.NextCursor})
	if err != nil || !cursor.UpdatedAt.Equal(at) || cursor.ProjectID != "project_b" {
		t.Fatalf("cursor must use last included row, not lookahead: %#v %v", cursor, err)
	}
	filter.Cursor = first.NextCursor
	last, err := service.Overview(ctx, filter)
	if err != nil || !last.Complete || last.NextCursor != "" || len(last.Items) != 1 || last.Items[0].ProjectID != "project_a" || last.FilteredTotal != 300 || calls != 2 {
		t.Fatalf("incorrect last page/call count: %#v calls=%d err=%v", last, calls, err)
	}
}

func TestOverviewEmptyAndExactLimitPages(t *testing.T) {
	for _, n := range []int{0, 24} {
		service := overviewTestService(t, func(_ context.Context, _ string, args []driver.NamedValue) (driver.Rows, error) {
			if args[5].Value != int64(25) {
				t.Fatalf("default page did not request one lookahead: %#v", args)
			}
			return overviewTestResult(time.Now(), 80, ProjectOverviewCounts{Working: 80}, int64(n), make([]ProjectOverviewItem, n)), nil
		})
		result, err := service.Overview(context.Background(), OverviewFilter{})
		if err != nil || !result.Complete || result.NextCursor != "" || result.Items == nil || len(result.Items) != n || result.Total != 80 {
			t.Fatalf("n=%d result=%#v err=%v", n, result, err)
		}
	}
}

func TestOverviewReadErrors(t *testing.T) {
	cause := errors.New("database unavailable")
	for _, test := range []struct {
		name string
		row  []driver.Value
		err  error
	}{
		{name: "query", err: cause},
		{name: "no row"},
		{name: "scan", row: []driver.Value{"bad timestamp", int64(0), int64(0), int64(0), int64(0), int64(0), int64(0), []byte(`[]`)}},
		{name: "invalid json", row: []driver.Value{time.Now(), int64(0), int64(0), int64(0), int64(0), int64(0), int64(0), []byte(`invalid`)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := overviewTestService(t, func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
				return &overviewTestRows{values: test.row}, test.err
			})
			if _, err := service.Overview(context.Background(), OverviewFilter{}); err == nil || (test.err != nil && !errors.Is(err, cause)) {
				t.Fatalf("read error = %v", err)
			}
		})
	}
}

func TestOverviewResponseShapeAndByteLimit(t *testing.T) {
	items := make([]ProjectOverviewItem, 100)
	for i := range items {
		items[i] = ProjectOverviewItem{ProjectID: "project_preview", ProjectScopeID: "scope_preview", Name: strings.Repeat("<", 160), Description: strings.Repeat("<", 512), Status: "active", WorkState: "unclassified", UpdatedAt: time.Now().UTC()}
	}
	result, err := finishProjectOverview(ProjectOverview{Items: items, Total: 100, FilteredTotal: 100}, 100)
	if err != nil || result.Complete || result.NextCursor == "" || len(result.Items) == 0 || len(result.Items) >= 100 {
		t.Fatalf("byte-limited page = %#v err=%v", result, err)
	}
	raw, err := json.Marshal(result)
	if err != nil || len(raw) > 128*1024 {
		t.Fatalf("serialized page is not bounded: %d %v", len(raw), err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) != 7 {
		t.Fatalf("unexpected response fields: %#v", payload)
	}
	for _, key := range []string{"observed_at", "counts", "total", "filtered_total", "items", "next_cursor", "complete"} {
		if _, ok := payload[key]; !ok {
			t.Errorf("response missing %s", key)
		}
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(payload["items"], &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows[0]) != 8 {
		t.Fatalf("unexpected compact row fields: %#v", rows[0])
	}
	for _, key := range []string{"project_id", "project_scope_id", "name", "slug", "description", "status", "work_state", "updated_at"} {
		if _, ok := rows[0][key]; !ok {
			t.Errorf("compact row missing %s", key)
		}
	}
	_, cursor, err := normalizeOverviewFilter(OverviewFilter{Cursor: result.NextCursor})
	if err != nil || !cursor.UpdatedAt.Equal(result.Items[len(result.Items)-1].UpdatedAt) {
		t.Fatalf("byte-limited cursor = %#v err=%v", cursor, err)
	}
	items[0].Name = strings.Repeat("x", 128*1024)
	if _, err := finishProjectOverview(ProjectOverview{Items: items[:1]}, 1); err == nil {
		t.Fatal("single oversized item must fail, not return an unpageable empty result")
	}
}

func TestOverviewSQLReadContract(t *testing.T) {
	query := strings.Join(strings.Fields(projectOverviewSQL), " ")
	for _, fragment := range []string{
		"JOIN scopes.scopes scope ON scope.scope_id = p.project_scope_id",
		"CASE WHEN p.status = 'archived' THEN 'archived' WHEN p.metadata->>'work_state' IN ('working', 'passive') THEN p.metadata->>'work_state' ELSE 'unclassified' END AS work_state",
		"WHERE ($1 = 'all' OR work_state = $1)",
		"(updated_at, project_id) < ($4::timestamptz, $5::text)",
		"ORDER BY updated_at DESC, project_id DESC LIMIT $6",
		"(SELECT count(*) FROM filtered)",
		"SELECT statement_timestamp()",
	} {
		if !strings.Contains(query, fragment) {
			t.Errorf("missing SQL contract: %s", fragment)
		}
	}
	for _, forbidden := range []string{"UPDATE ", "INSERT ", "DELETE ", "FOR UPDATE", "indexing_policy", "backup_policy", "sharing_policy", "archive_state", "owner_actor_id", "default_policy_ref"} {
		if strings.Contains(query, forbidden) {
			t.Errorf("overview selected private data or attempted a mutation: %s", forbidden)
		}
	}
}

type overviewContextKey struct{}
type overviewTestQuery func(context.Context, string, []driver.NamedValue) (driver.Rows, error)

func overviewTestService(t *testing.T, query overviewTestQuery) Service {
	t.Helper()
	db := sql.OpenDB(overviewTestConnector{query})
	t.Cleanup(func() { _ = db.Close() })
	return NewService(db)
}

type overviewTestConnector struct{ query overviewTestQuery }

func (c overviewTestConnector) Connect(context.Context) (driver.Conn, error) {
	return overviewTestConn{c.query}, nil
}
func (overviewTestConnector) Driver() driver.Driver { return overviewTestDriver{} }

type overviewTestDriver struct{}

func (overviewTestDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use overview test connector")
}

type overviewTestConn struct{ query overviewTestQuery }

func (overviewTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("overview must use a direct read")
}
func (overviewTestConn) Close() error { return nil }
func (overviewTestConn) Begin() (driver.Tx, error) {
	return nil, errors.New("overview must not start a transaction")
}
func (c overviewTestConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	return c.query(ctx, q, args)
}

type overviewTestRows struct{ values []driver.Value }

func (*overviewTestRows) Columns() []string {
	return []string{"observed_at", "total", "working", "passive", "archived", "unclassified", "filtered_total", "items"}
}
func (*overviewTestRows) Close() error { return nil }
func (r *overviewTestRows) Next(dest []driver.Value) error {
	if r.values == nil {
		return io.EOF
	}
	copy(dest, r.values)
	r.values = nil
	return nil
}

func overviewTestResult(at time.Time, total int64, counts ProjectOverviewCounts, filtered int64, items []ProjectOverviewItem) driver.Rows {
	raw, _ := json.Marshal(items)
	return &overviewTestRows{values: []driver.Value{at, total, counts.Working, counts.Passive, counts.Archived, counts.Unclassified, filtered, raw}}
}
