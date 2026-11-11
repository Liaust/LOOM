package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/workspacefiles"
)

func fileServerFixture(t *testing.T) (http.Handler, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("current unindexed note"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "book.pdf"), []byte("%PDF-1.7 original bytes for range read"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "empty.md"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	service := &workspacefiles.Service{Roots: []workspacefiles.Root{{Ref: "box", Node: "main", SourcePath: root}}}
	t.Cleanup(func() { _ = service.Close() })
	server := NewServer(Services{Files: service}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// Central route wiring belongs to the integrator. Mount this new handler
	// explicitly so its transport boundary is tested before that integration.
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/files/explain", server.handleFileExplain)
	mux.Handle("/", server.Handler())
	return mux, root
}

func TestFilesRejectTCPAndCallerSuppliedTrust(t *testing.T) {
	handler, _ := fileServerFixture(t)
	for _, target := range []string{"/v1/files/roots", "/v1/files/entries?root=box", "/v1/files/stat?root=box&path=note.md", "/v1/files/explain?root=box&path=note.md", "/v1/files/content?root=box&path=note.md", "/v1/files/operations?root=box&path=note.md", "/v1/files/retained-move?id=not-trusted"} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("X-Loom-Local", "true")
		req.Header.Set("X-Forwarded-For", "127.0.0.1")
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, req)
		if result.Code != http.StatusForbidden {
			t.Fatalf("TCP allowed %s: %d %s", target, result.Code, result.Body.String())
		}
	}
}

func TestFilesOriginalRangeHeadAndConditional(t *testing.T) {
	handler, root := fileServerFixture(t)
	request := func(method, path string, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		// Production assigns this marker from a real Unix connection, never a header.
		req = req.WithContext(context.WithValue(req.Context(), localFileTransportKey{}, true))
		for name, value := range headers {
			req.Header.Set(name, value)
		}
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, req)
		return result
	}
	result := request(http.MethodGet, "/v1/files/content?root=box&path=book.pdf", map[string]string{"Range": "bytes=5-12"})
	if result.Code != http.StatusPartialContent || result.Body.String() != "1.7 orig" || result.Header().Get("Content-Range") == "" {
		t.Fatalf("range: %d %q %v", result.Code, result.Body.String(), result.Header())
	}
	head := request(http.MethodHead, "/v1/files/content?root=box&path=book.pdf", nil)
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") == "" {
		t.Fatalf("HEAD: %d %s", head.Code, head.Body.String())
	}
	conditional := request(http.MethodGet, "/v1/files/content?root=box&path=book.pdf", map[string]string{"If-None-Match": head.Header().Get("ETag")})
	if conditional.Code != http.StatusNotModified || conditional.Body.Len() != 0 {
		t.Fatalf("conditional read: %d", conditional.Code)
	}
	// A metadata-derived weak validator must not authorize combining byte ranges.
	weakRange := request(http.MethodGet, "/v1/files/content?root=box&path=book.pdf", map[string]string{"Range": "bytes=0-2", "If-Range": head.Header().Get("ETag")})
	if weakRange.Code != http.StatusOK {
		t.Fatalf("weak If-Range incorrectly accepted: %d", weakRange.Code)
	}
	for _, tc := range []struct {
		target string
		status int
	}{
		{"/v1/files/content?root=box&path=missing.md", 404},
		{"/v1/files/content?root=box&path=..%2Fprivate", 400},
		{"/v1/files/stat?root=other&path=note.md", 404},
		{"/v1/files/entries?root=box&limit=-1", 400},
		{"/v1/files/entries?root=box&show_ignored=maybe", 400},
		{"/v1/files/content?root=box&path=empty.md", 200},
	} {
		if got := request(http.MethodGet, tc.target, nil); got.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.target, got.Code, got.Body.String())
		}
	}
	if got := request(http.MethodPost, "/v1/files/content?root=box&path=note.md", nil); got.Code != 405 {
		t.Fatalf("mutation allowed: %d", got.Code)
	}
	body, err := os.ReadFile(filepath.Join(root, "note.md"))
	if err != nil || string(body) != "current unindexed note" {
		t.Fatalf("source changed: %q %v", body, err)
	}
}

func TestFilesActualUnixSocketAndStreamingClient(t *testing.T) {
	handler, _ := fileServerFixture(t)
	directory, err := os.MkdirTemp("/tmp", "loom-files-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "s")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ConnContext: unixFileTransportContext}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		if err := <-done; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Error(err)
		}
	})
	client := localclient.New(socket)
	roots, err := client.FileRoots(context.Background(), "files-test")
	if err != nil || len(roots.Data) != 1 || !roots.Data[0].Available {
		t.Fatalf("roots: %+v %v", roots.Data, err)
	}
	page, err := client.FileEntries(context.Background(), "files-test", workspacefiles.ListInput{Root: "box", Limit: 1})
	if err != nil || len(page.Data.Entries) != 1 || !page.Data.HasMore {
		t.Fatalf("list: %+v %v", page.Data, err)
	}
	metadata, err := client.FileStat(context.Background(), "files-test", "box", "note.md")
	if err != nil || metadata.Data.Kind != "file" {
		t.Fatalf("stat: %+v %v", metadata.Data, err)
	}
	explained, err := client.FileExplain(context.Background(), "files-test", "box", "note.md")
	if err != nil || explained.Data.Source.Kind != "file" || explained.Data.Intent.State != "unknown" || explained.Data.AppliedConfig != "not_checked" {
		t.Fatalf("explain: %+v %v", explained.Data, err)
	}
	var output bytes.Buffer
	if err := client.ReadFile(context.Background(), "files-test", "box", "note.md", 8, 9, &output); err != nil || output.String() != "unindexed" {
		t.Fatalf("stream: %q %v", output.String(), err)
	}
	output.Reset()
	if err := client.ReadFile(context.Background(), "files-test", "box", "empty.md", 0, 0, &output); err != nil || output.Len() != 0 {
		t.Fatalf("empty read: %q %v", output.String(), err)
	}
}

func TestFileExplainSerializedFieldsAndReadBoundary(t *testing.T) {
	handler, root := fileServerFixture(t)
	request := func(method, target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, nil)
		req = req.WithContext(context.WithValue(req.Context(), localFileTransportKey{}, true))
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, req)
		return result
	}
	got := request(http.MethodGet, "/v1/files/explain?root=box&path=note.md")
	if got.Code != http.StatusOK {
		t.Fatalf("explain: %d %s", got.Code, got.Body.String())
	}
	var envelope response.Envelope[workspacefiles.Explanation]
	if err := json.Unmarshal(got.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	data := envelope.Data
	if data.SourcePath != filepath.Join(root, "note.md") || data.RootRegistration != "unknown" || data.DeviceLocalAvailability != "unknown" || data.AppliedConfig != "not_checked" || data.BackupCoverage != "not_verified" || data.Intent.Search != "unknown" || data.Intent.Protect != nil {
		t.Fatalf("qualifiers lost: %+v", data)
	}
	for _, tc := range []struct {
		method, target string
		status         int
	}{
		{http.MethodGet, "/v1/files/explain?root=box&path=..%2Fprivate", 400},
		{http.MethodGet, "/v1/files/explain?root=box&path=missing.md", 404},
		{http.MethodGet, "/v1/files/explain?root=unregistered&path=note.md", 404},
		{http.MethodPost, "/v1/files/explain?root=box&path=note.md", 405},
	} {
		if result := request(tc.method, tc.target); result.Code != tc.status {
			t.Errorf("%s %s: %d %s", tc.method, tc.target, result.Code, result.Body.String())
		}
	}
}
