package httpapi

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"loom.local/loom/internal/workspacefiles"
)

func TestFileLinksCatalogRetainsPrivateTransportAndValidatesBeforeQuery(t *testing.T) {
	files := &workspacefiles.Service{Roots: []workspacefiles.Root{{Ref: "box", SourcePath: t.TempDir()}}}
	t.Cleanup(func() { _ = files.Close() })
	// No connected DB: invalid input/scope must never issue a catalog query.
	server := NewServer(Services{Files: files, DB: new(sql.DB)}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, tc := range []struct {
		method, target, body string
		local                bool
		status               int
	}{
		{"GET", "/v1/files/links?action=catalog&root=box", "", false, 403},
		{"GET", "/v1/files/links?action=catalog&root=box&limit=201", "", true, 400},
		{"GET", "/v1/files/links?action=catalog&root=box&limit=0", "", true, 400},
		{"GET", "/v1/files/links?action=catalog&root=box&limit=oops", "", true, 400},
		{"GET", "/v1/files/links?action=catalog&root=box&path=..%2Fprivate", "", true, 400},
		{"GET", "/v1/files/links?action=catalog&root=box&query=a&query=b", "", true, 400},
		{"GET", "/v1/files/links?action=catalog&root=box&sql=SELECT", "", true, 400},
		{"GET", "/v1/files/links?action=catalog&root=box&after=../outside", "", true, 409},
		{"GET", "/v1/files/links?action=catalog&root=box&path=.loom/state", "", true, 403},
		{"GET", "/v1/files/links?action=catalog&root=hidden", "", true, 404},
		{"GET", "/v1/files/links?action=catalog&root=box&path=missing", "", true, 404},
		{"POST", "/v1/files/links", `{"root":"box","action":"catalog"}`, true, 400},
	} {
		r := httptest.NewRequest(tc.method, tc.target, strings.NewReader(tc.body))
		r.Header.Set("X-Loom-Local", "true")
		if tc.local {
			r = r.WithContext(context.WithValue(r.Context(), localFileTransportKey{}, true))
		}
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s %s: %d %s", tc.method, tc.target, w.Code, w.Body.String())
		}
		if tc.local && w.Header().Get("Cache-Control") != "private, no-cache" {
			t.Fatal("private response cache control lost")
		}
	}
}
