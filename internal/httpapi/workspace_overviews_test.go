package httpapi

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"testing"
)

func TestWorkspaceOverviewReadBoundary(t *testing.T) {
	s := NewServer(Services{}, slog.Default())
	for _, route := range []string{"/v1/projects/overview", "/v1/jobs/overview", "/v1/schedules/overview", "/v1/system/runtime", "/v1/system/protection"} {
		for _, trusted := range []bool{false, true} {
			r := httptest.NewRequest("GET", route, nil)
			if trusted {
				r = r.WithContext(context.WithValue(r.Context(), localFileTransportKey{}, true))
			}
			w := httptest.NewRecorder()
			s.handleWorkspaceOverview(w, r)
			want := 403
			if trusted {
				want = 503
			}
			if w.Code != want {
				t.Fatalf("%s trusted=%v got %d want %d", route, trusted, w.Code, want)
			}
		}
	}
}
func TestWorkspaceOverviewQueryValidation(t *testing.T) {
	for _, query := range []string{"?limit=0", "?limit=101", "?limit=2&limit=3", "?query=%zz", "?wrong=1", "?state=unknown"} {
		r := httptest.NewRequest("GET", "/v1/projects/overview"+query, nil)
		r = r.WithContext(context.WithValue(r.Context(), localFileTransportKey{}, true))
		w := httptest.NewRecorder()
		NewServer(Services{}, slog.Default()).handleWorkspaceOverview(w, r)
		if w.Code != 400 {
			t.Fatalf("%s got %d", query, w.Code)
		}
	}
}

func TestWorkspaceOverviewAcceptsOnlyValidatedBoxSelectors(t *testing.T) {
	catalog := contextCatalog()
	s := NewServer(Services{Boxes: &catalog}, slog.Default())
	for _, check := range []struct {
		query  string
		status int
	}{
		{"?box_id=box_company&limit=24", 503},
		{"?box=company", 503},
		{"?all_boxes=true", 503},
		{"?box_id=missing", 400},
		{"?all_boxes=invalid", 400},
		{"?box_id=box_company&root=box", 400},
		{"?box_id=box_company&wrong=1", 400},
	} {
		r := httptest.NewRequest("GET", "/v1/projects/overview"+check.query, nil)
		r = r.WithContext(context.WithValue(r.Context(), localFileTransportKey{}, true))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != check.status {
			t.Fatalf("%s: got %d, want %d: %s", check.query, w.Code, check.status, w.Body.String())
		}
	}
}
