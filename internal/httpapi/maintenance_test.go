package httpapi

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMaintenanceDatabaseCompactRejectsInvalidJSON(t *testing.T) {
	server := NewServer(Services{}, slog.Default()).Handler()
	req := httptest.NewRequest(http.MethodPost, "/v1/maintenance/db/compact", strings.NewReader(`{"dry_run":`))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "request.invalid_json") {
		t.Fatalf("body missing invalid json code: %s", rec.Body.String())
	}
}

func TestMaintenanceDatabaseCompactMethodGuard(t *testing.T) {
	server := NewServer(Services{}, slog.Default()).Handler()
	req := httptest.NewRequest(http.MethodGet, "/v1/maintenance/db/compact", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 body=%s", rec.Code, rec.Body.String())
	}
}
