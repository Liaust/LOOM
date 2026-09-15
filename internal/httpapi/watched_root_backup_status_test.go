package httpapi

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/watchedroots"
)

const backupStatusDiagnosticHint = "Inspect loom watched-roots status with the same filters. For a known declared project, use loom project plan <project> to inspect enrollment prerequisites."
const backupStatusDiagnosticSummary = "No matching reported watched root exists for these filters."

type backupStatusDiagnosticConnector struct {
	cause   error
	queries atomic.Int64
}

func (c *backupStatusDiagnosticConnector) Connect(context.Context) (driver.Conn, error) {
	return &backupStatusDiagnosticConn{c}, nil
}
func (c *backupStatusDiagnosticConnector) Driver() driver.Driver {
	return backupStatusDiagnosticDriver{}
}

type backupStatusDiagnosticDriver struct{}

func (backupStatusDiagnosticDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("fixture requires connector")
}

type backupStatusDiagnosticConn struct {
	c *backupStatusDiagnosticConnector
}

func (c *backupStatusDiagnosticConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("read fixture forbids prepare")
}
func (c *backupStatusDiagnosticConn) Close() error { return nil }
func (c *backupStatusDiagnosticConn) Begin() (driver.Tx, error) {
	return nil, errors.New("status fixture forbids transactions")
}
func (c *backupStatusDiagnosticConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	c.c.queries.Add(1)
	if !strings.HasPrefix(strings.TrimSpace(q), "SELECT") {
		return nil, errors.New("status attempted a write")
	}
	if c.c.cause != nil {
		return nil, c.c.cause
	}
	return &httpStorageFakeRows{columns: []string{"empty"}}, nil
}
func backupStatusDiagnosticHandler(t *testing.T, cause error) (http.Handler, *backupStatusDiagnosticConnector) {
	t.Helper()
	connector := &backupStatusDiagnosticConnector{cause: cause}
	db := sql.OpenDB(connector)
	t.Cleanup(func() { _ = db.Close() })
	return NewServer(Services{WatchedRoots: watchedroots.NewService(db)}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler(), connector
}

func TestWatchedRootBackupStatusDiagnosticHTTPClient(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cause  error
		status int
		code   string
	}{{"no_root", nil, 404, "watched_roots.backup_target_not_found"}, {"wrapped_no_rows", fmt.Errorf("wrapped fixture: %w", sql.ErrNoRows), 404, "watched_roots.backup_target_not_found"}, {"database_failure", errors.New("SELECT private_path=/fixture/private token=synthetic-driver-marker"), 500, "watched_roots.backup_status_failed"}} {
		t.Run(tc.name, func(t *testing.T) {
			handler, connector := backupStatusDiagnosticHandler(t, tc.cause)
			server := httptest.NewServer(handler)
			defer server.Close()
			client, err := localclient.NewHTTP(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.GetWatchedRootBackupStatus(t.Context(), "corr_backup_diagnostic", watchedroots.BackupFilter{RootKey: "fixture", Limit: 50})
			var requestErr *localclient.RequestError
			if !errors.As(err, &requestErr) || requestErr.StatusCode != tc.status || requestErr.Envelope.Error.Code != tc.code {
				t.Fatalf("missing exact classification: %#v %v", requestErr, err)
			}
			env := requestErr.Envelope
			if env.Error.Domain != "watched_roots" || env.Error.Target != "status" || env.Error.CorrelationID != "corr_backup_diagnostic" || env.Meta.CorrelationID != "corr_backup_diagnostic" || env.OK || connector.queries.Load() != 1 {
				t.Fatalf("lost failure envelope: %#v", env)
			}
			if tc.status == 404 && (env.Error.Summary != backupStatusDiagnosticSummary || env.Error.Hint != backupStatusDiagnosticHint) {
				t.Fatalf("missing bounded diagnostic: %#v", env.Error)
			}
			b, _ := json.Marshal(env)
			for _, secret := range []string{"SELECT", "private_path", "synthetic-driver-marker", "wrapped fixture"} {
				if strings.Contains(string(b), secret) {
					t.Fatalf("driver cause leaked: %s", b)
				}
			}
		})
	}
}
func TestWatchedRootBackupStatusDiagnosticFilterFailure(t *testing.T) {
	handler, connector := backupStatusDiagnosticHandler(t, nil)
	for _, query := range []string{"?limit=invalid", "?limit=-1"} {
		r := httptest.NewRequest(http.MethodGet, "/v1/watched-roots/backups/status"+query, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		var env response.ErrorEnvelope
		_ = json.Unmarshal(w.Body.Bytes(), &env)
		if w.Code != 400 || env.Error.Code != "watched_roots.invalid_filter" || connector.queries.Load() != 0 {
			t.Fatalf("invalid filter changed: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestBackupStatusDiagnosticDoesNotInterpolateFilters(t *testing.T) {
	handler, _ := backupStatusDiagnosticHandler(t, nil)
	server := httptest.NewServer(handler)
	defer server.Close()
	client, err := localclient.NewHTTP(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	malicious := "$(touch /fixture/must-not-run)\n\x1b[31m"
	_, err = client.GetWatchedRootBackupStatus(t.Context(), "corr_safe_filter", watchedroots.BackupFilter{RootKey: malicious})
	var failure *localclient.RequestError
	if !errors.As(err, &failure) || failure.Envelope.Error.Hint != backupStatusDiagnosticHint {
		t.Fatalf("filter changed advice: %#v %v", failure, err)
	}
	encoded, _ := json.Marshal(failure.Envelope)
	if strings.Contains(string(encoded), "must-not-run") || strings.Contains(string(encoded), "touch") {
		t.Fatalf("filter interpolated into response: %s", encoded)
	}
}
