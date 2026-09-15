package knowledge

import "context"

const VisionPromptVersionV1 = "notes_image_factual.v1"

type VisionRequest struct {
	Image    []byte
	MimeType string
	Prompt   string
}
type VisionResult struct {
	Description    string
	RuntimeKey     string
	RuntimeVersion string
	Model          string
}
type VisionRuntime interface {
	Describe(context.Context, VisionRequest) (VisionResult, error)
}

func ImageDescriptionPrompt() string {
	return "Describe this standalone image in one brief factual paragraph. Mention only visible subjects, text, layout, and notable colors. Do not speculate about identity, intent, location, or events."
}
