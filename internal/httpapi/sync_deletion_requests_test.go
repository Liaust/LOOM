package httpapi

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSyncDeletionRequestActionsRejectNonPost(t *testing.T) {
	server := NewServer(Services{}, slog.Default()).Handler()
	for _, path := range []string{
		"/v1/sync/deletion-requests/deletion_request_test/review",
		"/v1/sync/deletion-requests/deletion_request_test/approve",
		"/v1/sync/deletion-requests/deletion_request_test/deny",
		"/v1/sync/deletion-requests/deletion_request_test/complete",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		res := httptest.NewRecorder()

		server.ServeHTTP(res, req)

		if res.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s status = %d, want %d", path, res.Code, http.StatusMethodNotAllowed)
		}
	}
}

func TestSyncDeletionRequestActionsRejectInvalidJSON(t *testing.T) {
	server := NewServer(Services{}, slog.Default()).Handler()
	for _, path := range []string{
		"/v1/sync/deletion-requests/deletion_request_test/review",
		"/v1/sync/deletion-requests/deletion_request_test/approve",
		"/v1/sync/deletion-requests/deletion_request_test/deny",
		"/v1/sync/deletion-requests/deletion_request_test/complete",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{`))
		res := httptest.NewRecorder()

		server.ServeHTTP(res, req)

		if res.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d body=%s, want %d", path, res.Code, res.Body.String(), http.StatusBadRequest)
		}
	}
}
