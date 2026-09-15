package knowledge

import (
	"errors"
	"testing"

	"loom.local/loom/internal/storagecatalog"
)

func TestNormalizeReprocessInputInfersStaleScope(t *testing.T) {
	input, err := NormalizeReprocessInput(ReprocessInput{})
	if err != nil {
		t.Fatalf("NormalizeReprocessInput returned error: %v", err)
	}
	if input.Scope != ReprocessScopeStale {
		t.Fatalf("scope = %q, want %q", input.Scope, ReprocessScopeStale)
	}
	if !input.StaleOnly {
		t.Fatal("stale scope should set stale_only")
	}
	if input.PipelineKey != KnowledgeObjectPipelineNotesFileExtraction {
		t.Fatalf("pipeline = %q, want %q", input.PipelineKey, KnowledgeObjectPipelineNotesFileExtraction)
	}
	if input.Limit != defaultKnowledgeReprocessLimit {
		t.Fatalf("limit = %d, want %d", input.Limit, defaultKnowledgeReprocessLimit)
	}
}

func TestNormalizeReprocessInputRejectsUnsupportedPipeline(t *testing.T) {
	_, err := NormalizeReprocessInput(ReprocessInput{
		Scope:       ReprocessScopeStale,
		PipelineKey: KnowledgeSearchIndexKey,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestNormalizeReprocessInputAcceptsLegacyMarkdownPipelineAlias(t *testing.T) {
	input, err := NormalizeReprocessInput(ReprocessInput{
		Scope:       ReprocessScopeStale,
		PipelineKey: KnowledgeObjectPipelineMarkdownText,
	})
	if err != nil {
		t.Fatalf("NormalizeReprocessInput returned error: %v", err)
	}
	if input.PipelineKey != KnowledgeObjectPipelineNotesFileExtraction {
		t.Fatalf("pipeline = %q, want canonical %q", input.PipelineKey, KnowledgeObjectPipelineNotesFileExtraction)
	}
}

func TestNormalizeReprocessInputRejectsUnsupportedFileClass(t *testing.T) {
	_, err := NormalizeReprocessInput(ReprocessInput{
		Scope:     ReprocessScopeFileClass,
		FileClass: storagecatalog.FileClassAudio,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestNormalizeReprocessInputAcceptsExtractionFileClasses(t *testing.T) {
	for _, fileClass := range []string{
		storagecatalog.FileClassMarkdown,
		storagecatalog.FileClassText,
		storagecatalog.FileClassCode,
		storagecatalog.FileClassPDF,
		storagecatalog.FileClassOfficeDocument,
		storagecatalog.FileClassImage,
	} {
		t.Run(fileClass, func(t *testing.T) {
			input, err := NormalizeReprocessInput(ReprocessInput{
				Scope:     ReprocessScopeFileClass,
				FileClass: fileClass,
			})
			if err != nil {
				t.Fatalf("NormalizeReprocessInput returned error: %v", err)
			}
			if input.FileClass != fileClass {
				t.Fatalf("file class = %q, want %q", input.FileClass, fileClass)
			}
		})
	}
}
