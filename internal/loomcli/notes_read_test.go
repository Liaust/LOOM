package loomcli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/response"
)

func TestNotesReadProcessingPointerPreservesSourceBytesAndPlain(t *testing.T) {
	result := knowledge.FileReadResult{Status: "source_text", Text: "Exact source bytes without a trailing newline",
		Processing: &knowledge.FileReadProcessing{Publication: "current", InspectCommand: "loom notes pipelines inspect knowledge_object_EXACT --summary --json"}}
	calls := 0
	socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/knowledge/notes/files/read" {
			t.Errorf("unexpected extra processing request: %s", r.URL.Path)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("read", result))
	})
	defer stop()
	for _, mode := range []string{"human", "plain", "json", "source-only"} {
		args := []string{"--socket", socket, "notes", "read", "/source/note.md"}
		if mode == "plain" || mode == "json" {
			args = append(args, "--"+mode)
		}
		if mode == "source-only" {
			result.Processing = nil
		}
		out, stderr, err := executeRootCommand(args...)
		if err != nil {
			t.Fatal(err)
		}
		if mode == "json" {
			var got knowledge.FileReadResult
			if err := json.Unmarshal([]byte(out), &got); err != nil || got.Text != result.Text || got.Processing == nil || stderr != "" {
				t.Fatalf("structured read changed: %s %s %v", out, stderr, err)
			}
			continue
		}
		if out != result.Text {
			t.Fatalf("human metadata changed source stdout: %q", out)
		}
		if mode == "human" {
			if !strings.Contains(stderr, "Publication: current") || !strings.Contains(stderr, result.Processing.InspectCommand) {
				t.Fatalf("missing existing processing pointer: %s", stderr)
			}
		} else if stderr != "" {
			t.Fatalf("invented source-only metadata or contaminated plain mode: %s", stderr)
		}
	}
	if calls != 4 {
		t.Fatalf("read added processing requests: %d", calls)
	}
}
