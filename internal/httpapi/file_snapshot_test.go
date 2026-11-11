package httpapi

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/projectexport"
	"loom.local/loom/internal/workspacefiles"
)

func TestFileSnapshotReviewAndExactApply(t *testing.T) {
	handler, root := fileServerFixture(t)
	if err := os.Mkdir(filepath.Join(root, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "folder", "note.md"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	request := func(body map[string]string, trusted bool) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v1/files/snapshot", bytes.NewReader(raw))
		if trusted {
			req = req.WithContext(context.WithValue(req.Context(), localFileTransportKey{}, true))
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	files := workspacefiles.Service{Roots: []workspacefiles.Root{{Ref: "box", SourcePath: root}}}
	defer files.Close()
	metadata, err := files.Stat(context.Background(), "box", "folder")
	if err != nil {
		t.Fatal(err)
	}
	in := map[string]string{"root": "box", "path": "folder", "revision": metadata.Revision, "action": "plan"}
	if got := request(in, false); got.Code != 403 {
		t.Fatalf("TCP allowed: %d", got.Code)
	}
	review := request(in, true)
	if review.Code != 200 {
		t.Fatalf("%d %s", review.Code, review.Body.String())
	}
	var parsed struct {
		Data struct {
			PlanHash string `json:"plan_hash"`
		}
	}
	if err := json.Unmarshal(review.Body.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	in["action"], in["plan_hash"] = "apply", parsed.Data.PlanHash
	result := request(in, true)
	if result.Code != 200 {
		t.Fatalf("%d %s", result.Code, result.Body.String())
	}
	summary, _ := base64.RawURLEncoding.DecodeString(result.Header().Get(projectexport.SummaryHeader))
	var value projectexport.Summary
	if json.Unmarshal(summary, &value) != nil || value.SchemaVersion != projectexport.DirectorySchemaVersion || value.SourcePath != "folder" {
		t.Fatalf("%s", summary)
	}
	reader := tar.NewReader(bytes.NewReader(result.Body.Bytes()))
	header, err := reader.Next()
	if err != nil || header.Name != "note.md" {
		t.Fatalf("%+v %v", header, err)
	}
	text, _ := io.ReadAll(reader)
	if string(text) != "original" {
		t.Fatal("changed payload")
	}
	if err := os.WriteFile(filepath.Join(root, "folder", "note.md"), []byte("new version"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := request(in, true); got.Code != 409 {
		t.Fatalf("stale review allowed: %d %s", got.Code, got.Body.String())
	}
}
