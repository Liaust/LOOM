package knowledge

import (
	"encoding/json"
	"testing"

	"loom.local/loom/internal/storagecatalog"
)

func TestCompilePipelinePlanSnapshotsPolicy(t *testing.T) {
	plan, err := CompilePipelinePlan(KnowledgeObject{FileClass: storagecatalog.FileClassPDF, RelativePath: "mixed.pdf"}, PipelinePolicy{PDFOCREnabled: false, EmbeddingsEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
	selected := map[string]CompiledPipelineStage{}
	for _, stage := range plan.Stages {
		selected[stage.StageKey] = stage
	}
	if selected[FilePipelineStagePDFOCR].Selected || selected[FilePipelineStagePDFOCR].SkipReason != "disabled_by_policy" {
		t.Fatalf("PDF OCR policy snapshot = %#v", selected[FilePipelineStagePDFOCR])
	}
	if selected[FilePipelineStageEmbedding].Selected {
		t.Fatal("embedding should be skipped by policy")
	}
	snapshot, err := plan.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var decoded CompiledPipelinePlan
	if err := json.Unmarshal(snapshot, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.DefinitionVersion != PipelineDefinitionVersionV1 || len(decoded.Stages) != len(plan.Stages) {
		t.Fatalf("snapshot = %#v", decoded)
	}
}

func TestCompileImagePlanContainsNoOCR(t *testing.T) {
	plan, err := CompilePipelinePlan(KnowledgeObject{FileClass: storagecatalog.FileClassImage, RelativePath: "photo.jpg"}, DefaultPipelinePolicy())
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range plan.Stages {
		if stage.StageKey == FilePipelineStagePDFOCR {
			t.Fatal("image plan contains PDF OCR")
		}
	}
}
