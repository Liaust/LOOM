package portal

import (
	"strings"
	"testing"

	"loom.local/loom/internal/knowledge"
)

func TestRenderNotesPipelineStatusIsCompactAndHidesZeroFailures(t *testing.T) {
	var builder strings.Builder
	renderNotesPipelineStatus(&builder, knowledge.PipelineOverallStatus{Current: []knowledge.PipelineRun{{KnowledgeObjectID: "knowledge_object_test", CurrentStageKey: knowledge.FilePipelineStagePDFOCR, Status: knowledge.FilePipelineStatusWaitingHeavy}}, Policy: knowledge.PipelinePolicyStatus{Policy: knowledge.PipelinePolicy{PDFOCREnabled: true}}, Operations: knowledge.PipelineOperationsStatus{WaitingHeavy: 1, LexicalDocuments: 4, ActiveVectors: 2, HeavyExecutorAvailable: true, HeavyCapacity: 1}}, true, false)
	output := builder.String()
	for _, want := range []string{"active=1", "waiting_heavy=1", "stage=pdf_ocr", "Policies: OCR=true", "heavy=true"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "blocked=0") {
		t.Fatalf("zero failures should remain quiet:\n%s", output)
	}
}

func TestRenderNotesPipelineStatusOfflineIsTruthful(t *testing.T) {
	var builder strings.Builder
	renderNotesPipelineStatus(&builder, knowledge.PipelineOverallStatus{}, false, false)
	if output := builder.String(); !strings.Contains(output, "unavailable") || strings.Contains(output, "active=") {
		t.Fatalf("offline output=%s", output)
	}
}

func TestRenderNotesPipelineRawDiagnosticsSeparateToolsModelsAndState(t *testing.T) {
	var builder strings.Builder
	status := knowledge.PipelineOverallStatus{Operations: knowledge.PipelineOperationsStatus{
		StaleClaims:          2,
		ActivationMismatches: 1,
		Tools:                map[string]bool{"tesseract": false},
		Runtimes:             map[string]bool{"vision": true},
		Models:               map[string]bool{"vision": false},
	}}
	renderNotesPipelineStatus(&builder, status, true, true)
	output := builder.String()
	for _, want := range []string{"stale_claims=2", "activation_mismatches=1", "missing_tools=tesseract", "missing_runtimes=", "missing_models=vision"} {
		if !strings.Contains(output, want) {
			t.Fatalf("raw diagnostics missing %q:\n%s", want, output)
		}
	}
}

func TestNotesPipelineActionsUseFourCompactGroups(t *testing.T) {
	state := ScreenState{Screen: ScreenNotes}
	state.Data.Notes.PipelinesAvailable = true
	state.Data.Notes.Pipelines.Operations.Blocked = 1
	items := ScreenSelectableItems(state)
	want := map[string]bool{
		"Process...":  false,
		"Inspect...":  false,
		"Policies...": false,
		"Repair...":   false,
	}
	for _, item := range items {
		if _, ok := want[item.Label]; ok {
			want[item.Label] = true
		}
	}
	for label, found := range want {
		if !found {
			t.Fatalf("missing grouped Notes pipeline action %q in %#v", label, items)
		}
	}
}
