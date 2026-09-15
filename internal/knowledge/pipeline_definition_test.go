package knowledge

import (
	"errors"
	"testing"

	"loom.local/loom/internal/storagecatalog"
)

func TestPipelineDefinitionFileFamilies(t *testing.T) {
	tests := []struct {
		name, class, path, family string
		want, forbid              []string
	}{
		{"markdown", storagecatalog.FileClassMarkdown, "note.md", "markdown", []string{FilePipelineStageNativeText, FilePipelineStageEmbedding}, []string{FilePipelineStagePDFOCR, FilePipelineStageImageDescription}},
		{"structured", storagecatalog.FileClassCode, "data.json", "structured", []string{FilePipelineStageNativeText, FilePipelineStageEmbedding}, []string{FilePipelineStagePDFOCR, FilePipelineStageImageDescription}},
		{"docx", storagecatalog.FileClassOfficeDocument, "report.docx", "docx", []string{FilePipelineStageNativeText}, []string{FilePipelineStagePDFOCR, FilePipelineStageImageDescription}},
		{"pdf", storagecatalog.FileClassPDF, "scan.pdf", "pdf", []string{FilePipelineStagePDFPageAnalysis, FilePipelineStagePDFOCR}, []string{FilePipelineStageImageDescription}},
		{"image", storagecatalog.FileClassImage, "photo.png", "image", []string{FilePipelineStageImageDescription}, []string{FilePipelineStageNativeText, FilePipelineStagePDFOCR}},
		{"metadata", storagecatalog.FileClassArchive, "bundle.zip", "metadata_only", []string{FilePipelineStageMetadata, FilePipelineStageLexicalIndex, FilePipelineStageFinalize}, []string{FilePipelineStageEmbedding, FilePipelineStagePDFOCR, FilePipelineStageImageDescription}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			definition := PipelineDefinitionForObject(KnowledgeObject{FileClass: tt.class, RelativePath: tt.path})
			if definition.FileFamily != tt.family {
				t.Fatalf("family = %q, want %q", definition.FileFamily, tt.family)
			}
			if err := ValidatePipelineDefinition(definition); err != nil {
				t.Fatalf("ValidatePipelineDefinition: %v", err)
			}
			keys := map[string]bool{}
			for _, stage := range definition.Stages {
				keys[stage.StageKey] = true
			}
			for _, key := range tt.want {
				if !keys[key] {
					t.Errorf("missing stage %q", key)
				}
			}
			for _, key := range tt.forbid {
				if keys[key] {
					t.Errorf("unexpected stage %q", key)
				}
			}
		})
	}
}

func TestValidatePipelineDefinitionRejectsMissingDependencyAndCycle(t *testing.T) {
	definition := PipelineDefinitionForObject(KnowledgeObject{FileClass: storagecatalog.FileClassMarkdown})
	definition.Stages[1].Dependencies = []string{"missing"}
	if err := ValidatePipelineDefinition(definition); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing dependency error = %v", err)
	}
	definition = PipelineDefinitionForObject(KnowledgeObject{FileClass: storagecatalog.FileClassMarkdown})
	definition.Stages[0].Dependencies = []string{FilePipelineStageFinalize}
	if err := ValidatePipelineDefinition(definition); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cycle error = %v", err)
	}
}
