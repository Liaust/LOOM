package knowledge

import (
	"encoding/json"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
)

func TestArtifactInputsFromExtractionPreserveProvenance(t *testing.T) {
	result := ExtractionResult{ExtractorKey: "pdf", ExtractorVersion: "v1", TextSections: []ExtractedTextSection{
		{Index: 1, Text: " embedded text ", TextSource: TextSourceEmbeddedText, Metadata: map[string]any{"page_number": 1}},
		{Index: 2, Text: "key: value", TextSource: TextSourceStructuredText, StructuralPath: "section:data"},
	}}
	inputs := ArtifactInputsFromExtraction(result)
	if len(inputs) != 2 {
		t.Fatalf("inputs = %d, want 2", len(inputs))
	}
	if inputs[0].ArtifactKind != ArtifactKindEmbeddedText || inputs[0].SourceLocator != "page:1" {
		t.Fatalf("embedded input = %#v", inputs[0])
	}
	if inputs[1].ArtifactKind != ArtifactKindStructuredText || inputs[1].SourceLocator != "section:data" {
		t.Fatalf("structured input = %#v", inputs[1])
	}
}

func TestPrepareDerivedArtifactIdentityIncludesGeneratorContract(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	service := NewService(nil, WithClock(func() time.Time { return now }))
	run := PipelineRun{KnowledgePipelineRunID: ids.NewKnowledgePipelineRunID(), Generation: 2}
	stage := PipelineStageRun{KnowledgePipelineStageRunID: ids.NewKnowledgePipelineStageRunID()}
	version := KnowledgeObjectVersion{KnowledgeObjectVersionID: ids.NewKnowledgeObjectVersionID(), KnowledgeObjectID: ids.NewKnowledgeObjectID(), RecencyAt: now, RecencyBasis: AbsoluteTimeBasisObservedAtFallback}
	input := DerivedArtifactInput{ArtifactKind: ArtifactKindVisionDescription, SourceLocator: "image", Text: "A factual diagram.", GeneratorKey: "vision", GeneratorVersion: "v1", EngineKey: "ollama", EngineVersion: "0.11", PromptVersion: "prompt-v1", Metadata: map[string]any{}}
	first, err := service.PrepareDerivedArtifact(run, stage, version, input)
	if err != nil {
		t.Fatal(err)
	}
	input.PromptVersion = "prompt-v2"
	second, err := service.PrepareDerivedArtifact(run, stage, version, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.ContentHash != second.ContentHash {
		t.Fatal("content hash changed with prompt contract")
	}
	if first.InputHash == second.InputHash {
		t.Fatal("artifact identity did not include prompt version")
	}
	if first.RecencyAt != now || first.RecencyBasis != AbsoluteTimeBasisObservedAtFallback {
		t.Fatalf("chronology = %v/%s", first.RecencyAt, first.RecencyBasis)
	}
}

func TestNormalizedVersionMetadataOmitsFullTextSections(t *testing.T) {
	metadata := extractionPipelineVersionMetadataNormalized(ExtractionResult{TextSections: []ExtractedTextSection{{Text: "secret body"}}}, []string{"artifact-1"})
	var decoded map[string]any
	if err := json.Unmarshal(metadata, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["text_sections"]; ok {
		t.Fatal("normalized metadata contains full text_sections")
	}
	if decoded["text_section_count"].(float64) != 1 {
		t.Fatalf("summary = %#v", decoded)
	}
}
