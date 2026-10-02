package httpapi

import (
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNotesConflictsRejectInvalidAndUnconfiguredRequests(t *testing.T) {
	h := NewServer(Services{}, slog.Default()).Handler()
	for _, test := range []struct {
		method, query, body string
		status              int
	}{
		{"GET", "?id=a&after=b", "", 400}, {"GET", "?path=/tmp/private", "", 400},
		{"POST", "", `{"id":"x"}`, 400}, {"POST", "", `{} {}`, 400},
		{"DELETE", "", "", 405}, {"GET", "", "", 409},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(test.method, "/v1/knowledge/notes/conflicts"+test.query, strings.NewReader(test.body)))
		if w.Code != test.status {
			t.Fatalf("%s %s: %d %s", test.method, test.query, w.Code, w.Body.String())
		}
	}
}
