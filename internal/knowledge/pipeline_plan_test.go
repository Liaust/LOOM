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
	if decoded.DefinitionVersion != PipelineDefinitionVersionV2 || len(decoded.Stages) != len(plan.Stages) {
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

func TestPipelinePlanReferenceImageSkipsBodyAndHeavy(t *testing.T) {
	object := enrichmentTestObject(t, storagecatalog.FileClassImage)
	host := PipelinePolicy{ImageOCREnabled: true, ImageDescriptionsEnabled: true, EmbeddingsEnabled: true}
	plan, err := CompilePipelinePlan(object, host)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range plan.Stages {
		want := stage.StageKey == FilePipelineStageMetadata || stage.StageKey == FilePipelineStageLexicalIndex || stage.StageKey == FilePipelineStageFinalize
		if stage.Selected != want {
			t.Fatalf("unexpected stage: %#v", stage)
		}
		if stage.ExecutionClass == PipelineExecutionHeavy && stage.SkipReason != "disabled_by_source" {
			t.Fatalf("lost source posture: %#v", stage)
		}
	}
	if _, ok := pipelineQuietWindowEligibleAt(object, plan, object.UpdatedAt); ok {
		t.Fatal("reference image has heavy quiet window")
	}
	intent := enrichmentTestIntent(t, object, host, EnrichmentStages{OCR: true})
	enriched, err := compileEnrichmentPlan(object, host, intent)
	if err != nil {
		t.Fatal(err)
	}
	if !compiledPlanContainsSelectedStage(enriched, FilePipelineStageConsolidateText) || !compiledPlanContainsSelectedStage(enriched, FilePipelineStageChunk) {
		t.Fatal("selected OCR lost body consumers")
	}
	if compiledPlanContainsSelectedStage(enriched, FilePipelineStageEmbedding) {
		t.Fatal("source-disabled embeddings enabled")
	}
}

func TestPipelinePlanPDFKeepsNativeTextWithoutPageAnalysis(t *testing.T) {
	plan, err := CompilePipelinePlan(KnowledgeObject{FileClass: storagecatalog.FileClassPDF}, PipelinePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{FilePipelineStageNativeText, FilePipelineStageConsolidateText, FilePipelineStageChunk, FilePipelineStageLexicalIndex} {
		if !compiledPlanContainsSelectedStage(plan, key) {
			t.Fatalf("native text lost %s", key)
		}
	}
	if compiledPlanContainsSelectedStage(plan, FilePipelineStagePDFPageAnalysis) {
		t.Fatal("no page consumer")
	}
	plan.SchemaVersion = "knowledge.pipeline_plan.v1"
	snapshot, _ := plan.Snapshot()
	if effectiveCheapPipelinePlan(snapshot) {
		t.Fatal("old saved plan acquired new semantics")
	}
}

func TestPipelinePlanUnchangedTextKeepsV1Identity(t *testing.T) {
	plan, err := CompilePipelinePlan(KnowledgeObject{FileClass: storagecatalog.FileClassMarkdown}, PipelinePolicy{})
	if err != nil || plan.SchemaVersion != "knowledge.pipeline_plan.v1" || plan.DefinitionVersion != PipelineDefinitionVersionV1 {
		t.Fatalf("unchanged text was given new semantics: %+v %v", plan, err)
	}
}
