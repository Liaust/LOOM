package minidashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestServerServesOnlyFixedAssetsStatusAndHealth(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	view := Project(now, healthyHostFixture(now), nil, DefaultConfig())
	handler := NewDisplayHandler(func() ViewSnapshot { return view })
	assets := map[string]string{"/": "text/html", "/dashboard.css": "text/css", "/dashboard.js": "text/javascript", "/fonts/Geist-Variable.woff2": "font/woff2", "/healthz": "text/plain"}
	for path, contentType := range assets {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, response.Code)
		}
		if response.Header().Get("Content-Security-Policy") == "" {
			t.Fatalf("%s lacks CSP", path)
		}
		if got := response.Header().Get("Content-Type"); !strings.Contains(got, contentType) {
			t.Fatalf("%s content type = %q", path, got)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var got ViewSnapshot
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != SchemaVersion {
		t.Fatalf("status = %+v", got)
	}
}

func TestServerRejectsStatusMutation(t *testing.T) {
	handler := NewDisplayHandler(func() ViewSnapshot { return ViewSnapshot{} })
	request := httptest.NewRequest(http.MethodPost, "/api/status", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestServerHealthSurfacesPollerCacheFailure(t *testing.T) {
	handler := NewDisplayHandler(func() ViewSnapshot { return ViewSnapshot{} }, func() PollerDiagnostic {
		return PollerDiagnostic{SourceState: SourceLive, CacheState: SourceFailed, LastCacheFailureAt: time.Now().UTC()}
	})
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"degraded"`) || !strings.Contains(response.Body.String(), `"cache_state":"failed"`) {
		t.Fatalf("health response = %d %s", response.Code, response.Body.String())
	}
}
