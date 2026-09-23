package knowledge

import (
	"encoding/json"
	"testing"
)

func TestReusableStageMetadataSeparatesObservationsFromContract(t *testing.T) {
	for _, tc := range []struct {
		name, stage, status string
		observations        map[string]any
		want                bool
	}{
		{"chunk observations", FilePipelineStageChunk, PipelineStageStatusComplete, map[string]any{"chunk_count": 25, "consolidated_artifact_id": "artifact"}, true},
		{"PDF unnecessary OCR", FilePipelineStagePDFOCR, PipelineStageStatusSkippedNotApplicable, map[string]any{"skip_reason": "all_pages_have_useful_embedded_text"}, true},
		{"wrong OCR outcome", FilePipelineStagePDFOCR, PipelineStageStatusComplete, map[string]any{"skip_reason": "all_pages_have_useful_embedded_text"}, false},
		{"unknown skip", FilePipelineStagePDFOCR, PipelineStageStatusSkippedNotApplicable, map[string]any{"skip_reason": "unrecognized"}, false},
		{"implementation drift", FilePipelineStageChunk, PipelineStageStatusComplete, map[string]any{"chunk_count": 25, "implementation_key": "changed"}, false},
		{"selection drift", FilePipelineStagePDFOCR, PipelineStageStatusSkippedNotApplicable, map[string]any{"skip_reason": "all_pages_have_useful_embedded_text", "selected": false}, false},
		{"unknown metadata", FilePipelineStageChunk, PipelineStageStatusComplete, map[string]any{"unexpected": true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			compiled := CompiledPipelineStage{StageKey: tc.stage, Selected: true, ImplementationKey: "current", Required: true}
			stage := PipelineStageRun{StageKey: tc.stage, Status: tc.status, Metadata: pipelineStageMetadata(compiled)}
			var metadata map[string]any
			if err := json.Unmarshal(stage.Metadata, &metadata); err != nil {
				t.Fatal(err)
			}
			for key, value := range tc.observations {
				metadata[key] = value
			}
			stage.Metadata, _ = json.Marshal(metadata)
			if got := reusableStageMetadataMatches(stage, compiled); got != tc.want {
				t.Fatalf("reusable=%t, want %t: %s", got, tc.want, stage.Metadata)
			}
		})
	}
}
