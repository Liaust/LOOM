package capabilities

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"loom.local/loom/internal/requestctx"
)

func TestDisableCapabilityEndpointDisablesEndpoint(t *testing.T) {
	db, store := newCapabilityFakeDB(t)
	defer db.Close()
	svc := NewService(db)
	now := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	metadata := json.RawMessage(`{"reason":"test"}`)

	store.expect("SELECT capability_endpoint_id FROM capabilities.capability_endpoints", func(args []driver.NamedValue) {
		requireDriverArgs(t, args, "main@test.run")
	}, []driver.Value{"endp_test"})
	store.expect("UPDATE capabilities.capability_endpoints SET status = $2", func(args []driver.NamedValue) {
		requireDriverArgs(t, args, "endp_test", EndpointStatusDisabled, metadata)
	}, capabilityEndpointDriverRow(now, EndpointStatusDisabled, []byte(`{"reason":"test"}`)))

	endpoint, err := svc.DisableCapabilityEndpoint(context.Background(), requestctx.Context{}, "main@test.run", metadata)
	if err != nil {
		t.Fatalf("DisableCapabilityEndpoint returned error: %v", err)
	}
	if endpoint.CapabilityEndpointID != "endp_test" {
		t.Fatalf("endpoint id = %q, want endp_test", endpoint.CapabilityEndpointID)
	}
	if endpoint.Status != EndpointStatusDisabled {
		t.Fatalf("endpoint status = %q, want %q", endpoint.Status, EndpointStatusDisabled)
	}
	if string(endpoint.Metadata) != `{"reason":"test"}` {
		t.Fatalf("endpoint metadata = %s", endpoint.Metadata)
	}
	store.requireDone()
}

func TestDisableRuntimeBindingDisablesBinding(t *testing.T) {
	db, store := newCapabilityFakeDB(t)
	defer db.Close()
	svc := NewService(db)
	now := time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)
	metadata := json.RawMessage(`{"reason":"test"}`)

	store.expect("SELECT runtime_binding_id FROM capabilities.endpoint_runtime_bindings", func(args []driver.NamedValue) {
		requireDriverArgs(t, args, "runtime_binding_test")
	}, []driver.Value{"runtime_binding_test"})
	store.expect("UPDATE capabilities.endpoint_runtime_bindings SET status = $2", func(args []driver.NamedValue) {
		requireDriverArgs(t, args, "runtime_binding_test", RuntimeBindingStatusDisabled, metadata)
	}, runtimeBindingDriverRow(now, RuntimeBindingStatusDisabled, []byte(`{"reason":"test"}`)))

	binding, err := svc.DisableRuntimeBinding(context.Background(), requestctx.Context{}, "runtime_binding_test", metadata)
	if err != nil {
		t.Fatalf("DisableRuntimeBinding returned error: %v", err)
	}
	if binding.RuntimeBindingID != "runtime_binding_test" {
		t.Fatalf("runtime binding id = %q, want runtime_binding_test", binding.RuntimeBindingID)
	}
	if binding.Status != RuntimeBindingStatusDisabled {
		t.Fatalf("runtime binding status = %q, want %q", binding.Status, RuntimeBindingStatusDisabled)
	}
	if binding.DisabledAt == nil || !binding.DisabledAt.Equal(now) {
		t.Fatalf("runtime binding disabled_at = %v, want %v", binding.DisabledAt, now)
	}
	if string(binding.Metadata) != `{"reason":"test"}` {
		t.Fatalf("runtime binding metadata = %s", binding.Metadata)
	}
	store.requireDone()
}

type capabilityFakeStore struct {
	t       *testing.T
	mu      sync.Mutex
	queries []capabilityFakeQuery
}

type capabilityFakeQuery struct {
	snippet string
	check   func([]driver.NamedValue)
	row     []driver.Value
}

func newCapabilityFakeDB(t *testing.T) (*sql.DB, *capabilityFakeStore) {
	t.Helper()
	store := &capabilityFakeStore{t: t}
	db := sql.OpenDB(capabilityFakeConnector{store: store})
	return db, store
}

func (s *capabilityFakeStore) expect(snippet string, check func([]driver.NamedValue), row []driver.Value) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, capabilityFakeQuery{snippet: compactSQL(snippet), check: check, row: row})
}

func (s *capabilityFakeStore) query(query string, args []driver.NamedValue) (driver.Rows, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queries) == 0 {
		s.t.Fatalf("unexpected query: %s", query)
	}
	expected := s.queries[0]
	s.queries = s.queries[1:]
	if !strings.Contains(compactSQL(query), expected.snippet) {
		s.t.Fatalf("query = %q, want to contain %q", compactSQL(query), expected.snippet)
	}
	if expected.check != nil {
		expected.check(args)
	}
	return &capabilityFakeRows{columns: capabilityFakeColumns(len(expected.row)), row: expected.row}, nil
}

func (s *capabilityFakeStore) requireDone() {
	s.t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queries) != 0 {
		s.t.Fatalf("unconsumed queries: %d", len(s.queries))
	}
}

type capabilityFakeConnector struct {
	store *capabilityFakeStore
}

func (c capabilityFakeConnector) Connect(context.Context) (driver.Conn, error) {
	return capabilityFakeConn{store: c.store}, nil
}

func (c capabilityFakeConnector) Driver() driver.Driver {
	return capabilityFakeDriver{}
}

type capabilityFakeDriver struct{}

func (capabilityFakeDriver) Open(string) (driver.Conn, error) {
	return nil, fmt.Errorf("use sql.OpenDB with capabilityFakeConnector")
}

type capabilityFakeConn struct {
	store *capabilityFakeStore
}

func (c capabilityFakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("prepared statements are not supported by capabilityFakeConn")
}

func (c capabilityFakeConn) Close() error {
	return nil
}

func (c capabilityFakeConn) Begin() (driver.Tx, error) {
	return nil, fmt.Errorf("transactions are not supported by capabilityFakeConn")
}

func (c capabilityFakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.store.query(query, args)
}

type capabilityFakeRows struct {
	columns []string
	row     []driver.Value
	sent    bool
}

func (r *capabilityFakeRows) Columns() []string {
	return r.columns
}

func (r *capabilityFakeRows) Close() error {
	return nil
}

func (r *capabilityFakeRows) Next(dest []driver.Value) error {
	if r.sent {
		return io.EOF
	}
	copy(dest, r.row)
	r.sent = true
	return nil
}

func requireDriverArgs(t *testing.T, args []driver.NamedValue, want ...any) {
	t.Helper()
	if len(args) != len(want) {
		t.Fatalf("arg count = %d, want %d", len(args), len(want))
	}
	for i := range want {
		got := args[i].Value
		switch wantValue := want[i].(type) {
		case json.RawMessage:
			if string(driverBytes(got)) != string(wantValue) {
				t.Fatalf("arg %d = %q, want %q", i+1, string(driverBytes(got)), string(wantValue))
			}
		default:
			if fmt.Sprint(got) != fmt.Sprint(wantValue) {
				t.Fatalf("arg %d = %v, want %v", i+1, got, wantValue)
			}
		}
	}
}

func driverBytes(value any) []byte {
	switch typed := value.(type) {
	case []byte:
		return typed
	case string:
		return []byte(typed)
	default:
		return []byte(fmt.Sprint(typed))
	}
}

func compactSQL(query string) string {
	return strings.Join(strings.Fields(query), " ")
}

func capabilityFakeColumns(count int) []string {
	columns := make([]string, count)
	for i := range columns {
		columns[i] = fmt.Sprintf("col_%d", i)
	}
	return columns
}

func capabilityEndpointDriverRow(now time.Time, status string, metadata []byte) []driver.Value {
	return []driver.Value{
		"endp_test",
		"prov_test",
		"cls_test",
		"endpv_test",
		"run",
		"main@test.run",
		CapabilityFormJob,
		[]byte(`{}`),
		[]byte(`{}`),
		RiskLevelLow,
		int64(1),
		[]byte(`{}`),
		[]byte(`{}`),
		[]byte(`{}`),
		[]byte(`{}`),
		[]byte(`{}`),
		[]byte(`{}`),
		[]byte(`{}`),
		[]byte(`{}`),
		status,
		now,
		now,
		nil,
		metadata,
	}
}

func runtimeBindingDriverRow(now time.Time, status string, metadata []byte) []driver.Value {
	return []driver.Value{
		"runtime_binding_test",
		"endpv_test",
		RuntimeKindScript,
		[]byte(`{}`),
		[]byte(`{}`),
		[]byte(`{}`),
		status,
		"actor_test",
		nil,
		nil,
		now,
		now,
		now,
		metadata,
	}
}
