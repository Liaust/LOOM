package httpapi

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"loom.local/loom/internal/workspacefiles"
)

func TestFileHistoryPrivateReadOnlyInput(t *testing.T) {
	files := &workspacefiles.Service{Roots: []workspacefiles.Root{{Ref: "box", SourcePath: t.TempDir()}}}
	t.Cleanup(func() { _ = files.Close() })
	s := NewServer(Services{Files: files, DB: new(sql.DB)}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, tc := range []struct {
		method, target string
		local          bool
		status         int
	}{{"GET", "/v1/files/history?root=box", false, 403}, {"POST", "/v1/files/history?root=box", true, 405}, {"GET", "/v1/files/history?root=box&from=bad", true, 400}, {"GET", "/v1/files/history?root=box&limit=300", true, 400}, {"GET", "/v1/files/history?root=box&action=restore", true, 400}, {"GET", "/v1/files/history?root=box&path=../private", true, 409}} {
		r := httptest.NewRequest(tc.method, tc.target, nil)
		if tc.local {
			r = r.WithContext(context.WithValue(r.Context(), localFileTransportKey{}, true))
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.target, w.Code, w.Body.String())
		}
	}
}
