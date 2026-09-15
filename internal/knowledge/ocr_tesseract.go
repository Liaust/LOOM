package knowledge

import (
	"context"
	"fmt"
	"strings"
)

type TesseractOCR struct{ Runner commandRunner }

func (runtime TesseractOCR) Recognize(ctx context.Context, request OCRRequest) (OCRResult, error) {
	runner := runtime.Runner
	if runner == nil {
		runner = execCommandRunner{}
	}
	language := strings.TrimSpace(request.Language)
	if language == "" {
		language = "eng"
	}
	output, err := runner.Run(ctx, "tesseract", request.ImagePath, "stdout", "-l", language)
	if err != nil {
		return OCRResult{}, fmt.Errorf("tesseract failed: %w", err)
	}
	text := normalizeArtifactText(string(output))
	if text == "" {
		return OCRResult{}, fmt.Errorf("%w: tesseract produced empty text", ErrInvalid)
	}
	return OCRResult{Text: text, EngineKey: "tesseract", EngineVersion: "tesseract.v1", Language: language}, nil
}
