package httpapi

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/dropzone"
)

func TestDropzoneHTTPRetainsInspectionAndRejectsMutation(t *testing.T) {
	stateRoot := filepath.Join(t.TempDir(), "state")
	sessionRoot := filepath.Join(stateRoot, "dropzone", "incoming", "drop_session_historical")
	if err := os.MkdirAll(sessionRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join("..", "dropzone", "testdata", "historical", "upload-session-v0.4.2.json"))
	if err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(sessionRoot, "session.json")
	if err := os.WriteFile(sessionPath, fixture, 0o644); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := httptest.NewServer(NewServer(Services{
		Dropzone: dropzone.NewInspectionService(filepath.Join(t.TempDir(), "box"), stateRoot),
	}, logger).Handler())
	defer server.Close()

	for _, path := range []string{
		"/v1/box/dropzone/upload-sessions?limit=10",
		"/v1/box/dropzone/upload-sessions/drop_session_historical",
	} {
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "drop_session_historical") {
			t.Fatalf("GET %s status/body = %d/%s", path, resp.StatusCode, body)
		}
	}

	requests := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/v1/box/dropzone/upload-sessions"},
		{http.MethodPut, "/v1/box/dropzone/upload-sessions/drop_session_historical/chunks/0"},
		{http.MethodPost, "/v1/box/dropzone/upload-sessions/drop_session_historical/complete"},
		{http.MethodPost, "/v1/box/dropzone/upload-sessions/drop_session_historical/abort"},
	}
	for _, item := range requests {
		req, err := http.NewRequest(item.method, server.URL+item.path, bytes.NewReader([]byte(`{}`)))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", item.method, item.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusGone || !strings.Contains(string(body), "dropzone.runtime_retired") {
			t.Fatalf("%s %s status/body = %d/%s, want 410 retirement", item.method, item.path, resp.StatusCode, body)
		}
	}

	after, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, fixture) {
		t.Fatal("retired Dropzone HTTP mutations changed historical session evidence")
	}
}
