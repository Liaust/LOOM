package knowledge

import (
	"context"
	"errors"
	"testing"

	"loom.local/loom/internal/storagecatalog"
)

func TestImageOCRIsSeparateFromVision(t *testing.T) {
	object := KnowledgeObject{FileClass: storagecatalog.FileClassImage, RelativePath: "scan.png"}
	for _, enabled := range []bool{false, true} {
		plan, err := CompilePipelinePlan(object, PipelinePolicy{ImageOCREnabled: enabled})
		if err != nil {
			t.Fatal(err)
		}
		if plan.DefinitionVersion != "notes_image_pipeline.v2" {
			t.Fatal("image pipeline revision was not advanced")
		}
		var found bool
		for _, stage := range plan.Stages {
			if stage.StageKey == FilePipelineStageImageOCR {
				found = true
				if stage.Selected != enabled || stage.ExecutionClass != PipelineExecutionHeavy || stage.OutputArtifactKinds[0] != ArtifactKindOCRText {
					t.Fatalf("unexpected OCR stage: %+v", stage)
				}
			}
			if stage.StageKey == FilePipelineStageImageDescription && stage.Selected {
				t.Fatal("OCR implicitly enabled vision")
			}
		}
		if !found {
			t.Fatal("missing OCR stage")
		}
	}
}

func TestImageOCRRejectsNonImage(t *testing.T) {
	_, _, err := (ImageOCRStageHandler{}).Execute(context.Background(), PipelineWorkItem{Object: KnowledgeObject{FileClass: storagecatalog.FileClassPDF}}, DefaultHeavyResourcePolicy())
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error=%v", err)
	}
}

func TestTesseractEmptyTextHasDistinctOutcome(t *testing.T) {
	runner := &fakeCommandRunner{outputs: map[string][]byte{"tesseract blank.png stdout -l eng": []byte("\n")}, errors: map[string]error{}}
	_, err := (TesseractOCR{Runner: runner}).Recognize(context.Background(), OCRRequest{ImagePath: "blank.png"})
	if !errors.Is(err, ErrNoOCRText) || !errors.Is(err, ErrInvalid) {
		t.Fatalf("error=%v", err)
	}
}
