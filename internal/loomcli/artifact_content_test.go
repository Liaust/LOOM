package loomcli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/artifacts"
	"loom.local/loom/internal/response"
)

func TestArtifactReadCLIContentAndDownload(t *testing.T) {
	socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/artifacts/artifact_fixture/content" || req.Method != "GET" {
			t.Errorf("wrong request: %s", req.URL)
		}
		result := artifacts.ReadResult{ArtifactID: "artifact_fixture", ObjectVersionID: "version_exact", Hash: "sha256:verified", SizeBytes: 7, Content: "answer\n"}
		if req.URL.Query().Get("download") == "true" {
			result.Content, result.Payload = "", []byte("answer\n")
		}
		response.WriteJSON(w, http.StatusOK, response.Success("read", result))
	})
	defer stop()
	out, _, err := executeRootCommand("--socket", socket, "artifact", "read", "artifact_fixture")
	if err != nil || out != "answer\n" {
		t.Fatalf("content=%q error=%v", out, err)
	}
	dest := filepath.Join(t.TempDir(), "answer.txt")
	out, _, err = executeRootCommand("--socket", socket, "--json", "artifact", "read", "artifact_fixture", "--output", dest)
	var got struct {
		artifacts.ReadResult
		Output string `json:"output"`
	}
	if err != nil || json.Unmarshal([]byte(out), &got) != nil || got.Output != dest || got.ObjectVersionID != "version_exact" || len(got.Payload) != 0 {
		t.Fatalf("receipt=%s error=%v", out, err)
	}
	data, err := os.ReadFile(dest)
	info, statErr := os.Stat(dest)
	if err != nil || statErr != nil || string(data) != "answer\n" || info.Mode().Perm() != 0600 {
		t.Fatal("download did not preserve exact bytes/private local mode")
	}
	_, _, err = executeRootCommand("--socket", socket, "artifact", "read", "artifact_fixture", "--output", dest)
	if err == nil {
		t.Fatal("existing download overwritten")
	}
	out, _, err = executeRootCommand("--socket", socket, "--json", "artifact", "read", "artifact_fixture")
	if err != nil || !strings.Contains(out, `"object_version_id":"version_exact"`) {
		t.Fatalf("version-bound read missing: %s %v", out, err)
	}
}
