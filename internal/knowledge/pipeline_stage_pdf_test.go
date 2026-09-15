package knowledge

import (
	"context"
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
