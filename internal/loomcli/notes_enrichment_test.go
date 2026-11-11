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

func TestNotesEnrichmentPreviewNamesRetainedOutputs(t *testing.T) {
	socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatal("preview mutated work")
		}
		preview := knowledge.EnrichmentPreview{Entries: []knowledge.EnrichmentPreviewEntry{{
			EffectivePlan: &knowledge.CompiledPipelinePlan{Enrichment: &knowledge.PipelineEnrichment{Retained: knowledge.EnrichmentStages{OCR: true}}},
			Reuse:         &knowledge.EnrichmentReuse{Mode: "existing_run", SourceRunID: "run", RunStatus: "completed"},
		}}}
		response.WriteJSON(w, http.StatusOK, response.Success("preview", preview))
	})
	defer stop()
	out, _, err := executeRootCommand("--socket", socket, "notes", "enrich", "object", "--embeddings")
	if err != nil || !strings.Contains(out, "retained outputs (not rerun): OCR=true") || !strings.Contains(out, "existing_run from run (completed)") || !strings.Contains(out, enrichmentReplayPlanNotice) || strings.Contains(out, "eligible after") {
		t.Fatalf("misleading preview: %s %v", out, err)
	}
}

func TestNotesEnrichmentCompactPreviewPreservesSavedApplyAndDetails(t *testing.T) {
	binding := knowledge.EnrichmentBinding{ObjectID: "object", SourceRevision: "r1", SourceHash: "hash1", AdmissionHash: "admission1"}
	preview := knowledge.EnrichmentPreview{
		Selection: knowledge.EnrichmentSelection{Ref: "object", Kind: "object", Stages: knowledge.EnrichmentStages{Embeddings: true}},
		Entries: []knowledge.EnrichmentPreviewEntry{{Binding: binding,
			EffectivePlan: &knowledge.CompiledPipelinePlan{SchemaVersion: "knowledge.pipeline_plan.v2", Enrichment: &knowledge.PipelineEnrichment{
				Requested: knowledge.EnrichmentStages{Embeddings: true}, Retained: knowledge.EnrichmentStages{OCR: true}, Preserved: knowledge.EnrichmentStages{OCR: true},
			}, Stages: []knowledge.CompiledPipelineStage{{StageKey: "embedding", ImplementationKey: strings.Repeat("large-stage-detail", 1000)}}},
			Reuse: &knowledge.EnrichmentReuse{Mode: "existing_run", SourceRunID: "same-run", RunStatus: "complete"},
		}},
	}
	gets, posts := 0, 0
	socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets++
			response.WriteJSON(w, http.StatusOK, response.Success("preview", preview))
			return
		}
		posts++
		var input knowledge.EnrichmentApplyInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input.Bindings) != 1 || input.Bindings[0] != binding || !input.Stages.Embeddings || input.Stages.OCR || input.Stages.Vision || !input.Confirm {
			t.Errorf("compact preview changed apply selection: %+v %v", input, err)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("apply", knowledge.EnrichmentReceipt{Results: []knowledge.EnrichmentResult{{Binding: binding, Run: &knowledge.PipelineRun{KnowledgePipelineRunID: "same-run", Status: "complete"}}}}))
	})
	defer stop()
	compact, _, err := executeRootCommand("--json", "--socket", socket, "notes", "enrich", "object", "--embeddings")
	if err != nil || strings.Contains(compact, "large-stage-detail") || !strings.Contains(compact, "--details") || !strings.Contains(compact, enrichmentReplayPlanNotice) {
		t.Fatalf("verbose/default preview: %s %v", compact, err)
	}
	var saved knowledge.EnrichmentPreview
	decoder := json.NewDecoder(strings.NewReader(compact))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&saved); err != nil || saved.Entries[0].Binding != binding || !saved.Entries[0].EffectivePlan.Enrichment.Retained.OCR || saved.Entries[0].Reuse.SourceRunID != "same-run" {
		t.Fatalf("compact output is not an exact reusable selection: %+v %v", saved, err)
	}
	full, _, err := executeRootCommand("--json", "--socket", socket, "notes", "enrich", "object", "--embeddings", "--details")
	if err != nil || !strings.Contains(full, "large-stage-detail") || len(compact)*2 >= len(full) {
		t.Fatalf("full preview lost or compact projection ineffective: compact=%d full=%d %v", len(compact), len(full), err)
	}
	file := filepath.Join(t.TempDir(), "preview.json")
	if err := os.WriteFile(file, []byte(compact), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err = executeRootCommand("--json", "--socket", socket, "notes", "enrich", "--preview-file", file, "--yes")
	if err != nil || gets != 2 || posts != 1 || len(preview.Entries[0].EffectivePlan.Stages) != 1 {
		t.Fatalf("saved apply reselected or mutated original plan: gets=%d posts=%d err=%v", gets, posts, err)
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
