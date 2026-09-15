package knowledge

import (
	"strings"
	"testing"

	"loom.local/loom/internal/storagecatalog"
)

func TestCodeExtractorPreservesTextAndDetectsSymbols(t *testing.T) {
	object := testKnowledgeObjectForExtraction(storagecatalog.FileClassCode, "tool.py")

	extraction, err := DefaultExtractionRegistry().Extract(ExtractionInput{
		Object: object,
		Content: `class Runner:
    pass

def collect_signal():
    return "ok"
`,
		Chunker: ChunkerOptions{TargetCharacters: 200},
	})
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if extraction.ExtractorKey != ExtractorKeyCode {
		t.Fatalf("extractor = %q, want code", extraction.ExtractorKey)
	}
	if extraction.TextSections[0].TextSource != TextSourceEmbeddedText {
		t.Fatalf("text source = %q, want embedded_text", extraction.TextSections[0].TextSource)
	}
	if !strings.Contains(extraction.Document.Text, "collect_signal") {
		t.Fatalf("document text = %q, want original code", extraction.Document.Text)
	}
	symbols, _ := extraction.Metadata["symbols"].([]string)
	if !containsString(symbols, "Runner") || !containsString(symbols, "collect_signal") {
		t.Fatalf("symbols = %#v, want Runner and collect_signal", symbols)
	}
	if extraction.Metadata["line_count"] != 5 {
		t.Fatalf("line_count = %#v, want 5", extraction.Metadata["line_count"])
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
