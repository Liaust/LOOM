package httpapi

import (
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNotesSourceMovesRejectInvalidAndUnconfiguredRequests(t *testing.T) {
	h := NewServer(Services{}, slog.Default()).Handler()
	for _, test := range []struct {
		method, body string
		status       int
	}{
		{"GET", "", 405}, {"POST", `{}`, 400},
		{"POST", `{"from":"/etc/private.md","to":"Notes/b.md"}`, 400},
		{"POST", `{"from":"Notes/a.md","to":"Notes/b.md","confirm":true}`, 400},
		{"POST", `{"from":"Notes/a.md","to":"Notes/b.md","hostPath":"/tmp/private"}`, 400},
		{"POST", `{"from":"Notes/a.md","to":"Notes/b.md"}`, 409},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(test.method, "/v1/knowledge/notes/moves", strings.NewReader(test.body)))
		if w.Code != test.status {
			t.Fatalf("%s: %d %s", test.body, w.Code, w.Body.String())
		}
	}
}
