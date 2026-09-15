package knowledge

import (
	"context"
	"testing"
)

func TestControlledUnsupportedHeavyHandlerReturnsWarningAndSeparatedObservation(t *testing.T) {
	observation, warnings, err := controlledUnsupportedHeavyHandler(FilePipelineStagePDFOCR).Execute(context.Background(), PipelineWorkItem{}, DefaultHeavyResourcePolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || len(observation.EnforcedPolicy) == 0 || len(observation.Observed) == 0 {
		t.Fatalf("observation=%s warnings=%v", observation.Observed, warnings)
	}
}
