package knowledge

import (
	"context"
	"testing"
)

func TestTesseractOCRNormalizesFakeOutput(t *testing.T) {
	runner := &fakeCommandRunner{outputs: map[string][]byte{"tesseract page.png stdout -l eng": []byte(" hello\r\n")}, errors: map[string]error{}}
	runtime := TesseractOCR{Runner: runner}
	result, err := runtime.Recognize(context.Background(), OCRRequest{ImagePath: "page.png"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "hello" || result.Language != "eng" {
		t.Fatalf("result = %#v", result)
	}
}

func TestExtractionDoesNotIndexSuccessfulToolDiagnostics(t *testing.T) {
	output, err := (execCommandRunner{}).Run(context.Background(), "sh", "-c", "printf 'actual text'; printf 'Estimating resolution' >&2")
	if err != nil || string(output) != "actual text" {
		t.Fatalf("output=%q error=%v", output, err)
	}
}
