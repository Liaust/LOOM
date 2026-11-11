package portal

import (
	"context"
	"encoding/json"
	"strings"

	"loom.local/loom/internal/response"
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

func TestRenderNotesProgressKeepsUnknownAndPartialVisible(t *testing.T) {
	status := knowledge.PipelineOverallStatus{Progress: &knowledge.PipelineProgress{Limit: 100, Unknown: map[string]string{"counts": "no aggregate"}, PartialErrors: []string{"policy_unavailable"}}, NativePublication: json.RawMessage(`{"inventory":[{"collection":"c","generation":"g","complete":false,"counters_known":false,"totals_known":false,"asset_reason":"publication_pending"}],"partial_errors":["inventory_unavailable:other"]}`)}
	var out strings.Builder
	renderNotesPipelineStatus(&out, status, true, true)
	for _, want := range []string{"observed=0", "totals=unknown", "Policies: unknown", "native counts=unknown", "publication_pending", "Client receipt: unknown", "Protected generation/receipt: unknown", "Partial: inventory_unavailable:other", "Partial: policy_unavailable", "Integrity: explicit"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, out.String())
		}
	}
	for _, bad := range []string{"lexical=0", "semantic=0", "pending_text=0", "activation_mismatches=0"} {
		if strings.Contains(out.String(), bad) {
			t.Fatalf("false zero %q: %s", bad, out.String())
		}
	}
}

type progressNotesClient struct {
	*fakePortalClient
	embeddingReads int
}

func (c *progressNotesClient) GetKnowledgeNotesPipelineStatus(context.Context, string) (response.Envelope[knowledge.PipelineOverallStatus], error) {
	return response.Success("test", knowledge.PipelineOverallStatus{Progress: &knowledge.PipelineProgress{PartialErrors: []string{"worker_receipts_unavailable"}}, NativePublication: json.RawMessage(`{"partial_errors":["source_mapping_unknown:one"]}`)}), nil
}
func (c *progressNotesClient) GetKnowledgeNotesEmbeddingStatus(context.Context, string) (response.Envelope[knowledge.EmbeddingStatus], error) {
	c.embeddingReads++
	return response.Success("test", knowledge.EmbeddingStatus{}), nil
}
func TestNotesProgressLoaderSkipsHistoricalVectorCounts(t *testing.T) {
	client := &progressNotesClient{fakePortalClient: &fakePortalClient{}}
	result := loadNotesScreen(t.Context(), client, "test", Snapshot{})
	if len(result.PartialErrors) != 2 || client.embeddingReads != 0 || !result.State.Data.Notes.PipelinesAvailable || result.State.Data.Notes.Pipelines.Progress == nil {
		t.Fatalf("progress loader repeated historical counts: %+v reads=%d", result, client.embeddingReads)
	}
}
