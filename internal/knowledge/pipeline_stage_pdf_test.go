package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"loom.local/loom/internal/storagecatalog"
)

func TestPDFOCRStageRejectsNonPDFWithoutCallingOCR(t *testing.T) {
	ocr := &countingOCR{}
	_, _, err := (PDFOCRStageHandler{OCR: ocr}).Execute(context.Background(), PipelineWorkItem{Object: KnowledgeObject{FileClass: storagecatalog.FileClassImage}}, DefaultHeavyResourcePolicy())
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v", err)
	}
	if ocr.calls != 0 {
		t.Fatalf("OCR calls = %d", ocr.calls)
	}
}

type countingOCR struct{ calls int }

func (runtime *countingOCR) Recognize(context.Context, OCRRequest) (OCRResult, error) {
	runtime.calls++
	return OCRResult{}, nil
}

func TestPDFDefinitionNeverContainsVision(t *testing.T) {
	definition := PipelineDefinitionForObject(KnowledgeObject{FileClass: storagecatalog.FileClassPDF, RelativePath: "paper.pdf"})
	for _, stage := range definition.Stages {
		if stage.StageKey == FilePipelineStageImageDescription {
			t.Fatal("PDF definition contains vision")
		}
	}
}

func TestPDFPageAnalysisReusesOnlyExactNativeArtifacts(t *testing.T) {
	item := PipelineWorkItem{Run: PipelineRun{KnowledgePipelineRunID: "run", KnowledgeObjectVersionID: "version", SourceHash: "hash", SourceRevision: "r1", Generation: 2, PlanSnapshot: json.RawMessage(`{"schema_version":"knowledge.pipeline_plan.v2"}`)}}
	version := KnowledgeObjectVersion{KnowledgeObjectVersionID: "version", SourceHash: "hash", SourceRevision: "r1", Metadata: json.RawMessage(`{"extractor_key":"pdf_poppler","extractor_version":"pdf_poppler_native_v2","extraction_status":"extracted","extraction_metadata":{"page_count":2},"text_section_count":1}`)}
	text := "Useful native text that comfortably clears the page threshold"
	artifact := DerivedArtifact{KnowledgePipelineRunID: "run", KnowledgeObjectVersionID: "version", Generation: 2, ArtifactKind: ArtifactKindEmbeddedText, GeneratorKey: ExtractorKeyPDF, GeneratorVersion: ExtractorVersionPDFNative, TextContent: &text, ContentHash: hashArtifactValue(text), SourceLocator: "page:1", Metadata: json.RawMessage(`{"page_number":1}`)}
	pages, ok := nativePDFPagesForAnalysis(item, version, []DerivedArtifact{artifact})
	if !ok || len(pages) != 2 || !pages[0].UsefulEmbeddedText || pages[1].UsefulEmbeddedText {
		t.Fatalf("%+v %v", pages, ok)
	}
	for _, mutate := range []func(*DerivedArtifact){
		func(a *DerivedArtifact) { a.KnowledgePipelineRunID = "other" },
		func(a *DerivedArtifact) { a.Generation++ },
		func(a *DerivedArtifact) { a.GeneratorVersion = ExtractorVersionPDF },
		func(a *DerivedArtifact) { a.ContentHash = "mismatch" },
	} {
		bad := artifact
		mutate(&bad)
		if _, ok := nativePDFPagesForAnalysis(item, version, []DerivedArtifact{bad}); ok {
			t.Fatal("reused incompatible artifact")
		}
	}
	if _, ok := nativePDFPagesForAnalysis(item, version, nil); ok {
		t.Fatal("treated missing text as blank pages")
	}
	item.Run.PlanSnapshot = json.RawMessage(`{"schema_version":"knowledge.pipeline_plan.v1"}`)
	if _, ok := nativePDFPagesForAnalysis(item, version, []DerivedArtifact{artifact}); ok {
		t.Fatal("changed saved V1 semantics")
	}
}

func TestPDFPageAnalysisReusesCompletedEmptyNativeMetadata(t *testing.T) {
	item := PipelineWorkItem{Run: PipelineRun{KnowledgePipelineRunID: "run", SourceHash: "hash", SourceRevision: "r1", PlanSnapshot: json.RawMessage(`{"schema_version":"knowledge.pipeline_plan.v2"}`)}}
	version := KnowledgeObjectVersion{SourceHash: "hash", SourceRevision: "r1", Metadata: json.RawMessage(`{"extractor_key":"pdf_poppler","extractor_version":"pdf_poppler_native_v2","extraction_status":"no_embedded_text","text_section_count":1,"extraction_metadata":{"page_count":2}}`)}
	// Empty native extraction produces a metadata artifact, not zero artifacts.
	pages, ok := nativePDFPagesForAnalysis(item, version, []DerivedArtifact{{ArtifactKind: ArtifactKindMetadataText}})
	if !ok || len(pages) != 2 || pages[0].UsefulEmbeddedText || pages[1].UsefulEmbeddedText {
		t.Fatalf("%+v %v", pages, ok)
	}
}
