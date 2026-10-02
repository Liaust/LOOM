package loomcli

import (
	"encoding/json"
	"errors"
	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/response"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNotesEnrichmentPartialFailureOwnsOneJSONReceipt(t *testing.T) {
	binding := knowledge.EnrichmentBinding{ObjectID: "object", SourceRevision: "r1", SourceHash: "hash", AdmissionHash: "admission"}
	preview := knowledge.EnrichmentPreview{Selection: knowledge.EnrichmentSelection{Stages: knowledge.EnrichmentStages{Embeddings: true}}, Entries: []knowledge.EnrichmentPreviewEntry{{Binding: binding}}}
	raw, _ := json.Marshal(preview)
	file := filepath.Join(t.TempDir(), "preview.json")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		response.WriteJSON(w, http.StatusOK, response.Success("apply", knowledge.EnrichmentReceipt{Failed: 1, Results: []knowledge.EnrichmentResult{{Binding: binding, Error: "stale preview"}}}))
	})
	defer stop()
	out, _, err := executeRootCommand("--json", "--socket", socket, "notes", "enrich", "--preview-file", file, "--yes")
	var owned interface{ cliOutputOwned() }
	if err == nil || !errors.As(err, &owned) || !json.Valid([]byte(out)) || !strings.Contains(out, "stale preview") {
		t.Fatalf("partial receipt lost ownership: %s %v", out, err)
	}
}

func TestNotesEnrichmentPreviewDoesNotApply(t *testing.T) {
	calls := 0
	socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		q := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != "/v1/knowledge/notes/enrich" || q.Get("ref") != "/remote/folder" || q.Get("kind") != "folder" || q.Get("recursive") != "true" || q.Get("embeddings") != "true" {
			t.Errorf("unexpected preview %s %s", r.Method, r.URL)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("preview", knowledge.EnrichmentPreview{Inventory: "admitted database entries only"}))
	})
	defer stop()
	out, _, err := executeRootCommand("--json", "--socket", socket, "notes", "enrich", "/remote/folder", "--folder", "--recursive", "--embeddings")
	if err != nil || calls != 1 || !strings.Contains(out, "admitted database entries") {
		t.Fatalf("preview %s %v calls=%d", out, err, calls)
	}
}
func TestNotesEnrichmentAppliesSavedVersionsWithoutReselection(t *testing.T) {
	binding := knowledge.EnrichmentBinding{ObjectID: "object", SourceRevision: "r1", SourceHash: "hash1", AdmissionHash: "admission1"}
	preview := knowledge.EnrichmentPreview{Selection: knowledge.EnrichmentSelection{Stages: knowledge.EnrichmentStages{Embeddings: true}}, Entries: []knowledge.EnrichmentPreviewEntry{{Binding: binding}}}
	raw, _ := json.Marshal(preview)
	file := filepath.Join(t.TempDir(), "preview.json")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/knowledge/notes/enrich" {
			t.Errorf("reselected instead of applying: %s %s", r.Method, r.URL)
		}
		var input knowledge.EnrichmentApplyInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		if len(input.Bindings) != 1 || input.Bindings[0] != binding || !input.Stages.Embeddings || input.Stages.OCR || input.Stages.Vision || !input.Confirm {
			t.Errorf("lost bound request: %#v", input)
		}
		if r.Header.Get("X-Loom-Idempotency-Key") != "saved-preview" {
			t.Errorf("idempotency lost: %v", r.Header)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("apply", knowledge.EnrichmentReceipt{Results: []knowledge.EnrichmentResult{{Binding: binding, Run: &knowledge.PipelineRun{KnowledgePipelineRunID: "run", Status: "waiting_heavy"}}}}))
	})
	defer stop()
	out, _, err := executeRootCommand("--json", "--socket", socket, "notes", "enrich", "--preview-file", file, "--yes", "--idempotency-key", "saved-preview")
	if err != nil || calls != 1 || !strings.Contains(out, "waiting_heavy") {
		t.Fatalf("apply %s %v calls=%d", out, err, calls)
	}
}
func TestNotesEnrichmentRejectsUnreviewedFolderAndEmptyApply(t *testing.T) {
	for _, args := range [][]string{{"notes", "enrich", "/source", "--folder", "--ocr", "--yes"}, {"notes", "enrich", "object", "--yes"}} {
		_, _, err := executeRootCommand(args...)
		if err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
