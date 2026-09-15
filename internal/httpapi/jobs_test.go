package httpapi

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJobAttentionEndpointsRejectNonPost(t *testing.T) {
	server := NewServer(Services{}, slog.Default()).Handler()
	for _, path := range []string{
		"/v1/jobs/job_test/acknowledge",
		"/v1/jobs/job_test/archive",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		res := httptest.NewRecorder()

		server.ServeHTTP(res, req)

		if res.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s status = %d, want %d", path, res.Code, http.StatusMethodNotAllowed)
		}
	}
}

func TestJobAttentionEndpointsRejectInvalidJSON(t *testing.T) {
	server := NewServer(Services{}, slog.Default()).Handler()
	for _, path := range []string{
		"/v1/jobs/job_test/acknowledge",
		"/v1/jobs/job_test/archive",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{`))
		res := httptest.NewRecorder()

		server.ServeHTTP(res, req)

		if res.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d body=%s, want %d", path, res.Code, res.Body.String(), http.StatusBadRequest)
		}
	}
}
