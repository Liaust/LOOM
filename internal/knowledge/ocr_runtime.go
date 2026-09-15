package knowledge

import "context"

type OCRRequest struct {
	ImagePath  string
	Language   string
	PageNumber int
}
type OCRResult struct {
	Text          string
	Confidence    *float64
	EngineKey     string
	EngineVersion string
	Language      string
}
type OCRRuntime interface {
	Recognize(context.Context, OCRRequest) (OCRResult, error)
}
