package knowledge

import (
	"context"
	"errors"
	"testing"

	"loom.local/loom/internal/storagecatalog"
)

func TestImageDescriptionRejectsPDFBeforeVisionCall(t *testing.T) {
	runtime := &countingVision{}
	_, _, err := (ImageDescriptionStageHandler{Runtime: runtime}).Execute(context.Background(), PipelineWorkItem{Object: KnowledgeObject{FileClass: storagecatalog.FileClassPDF, RelativePath: "paper.pdf"}}, DefaultHeavyResourcePolicy())
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error=%v", err)
	}
	if runtime.calls != 0 {
		t.Fatalf("calls=%d", runtime.calls)
	}
}

type countingVision struct{ calls int }

func (runtime *countingVision) Describe(context.Context, VisionRequest) (VisionResult, error) {
	runtime.calls++
	return VisionResult{}, nil
}
