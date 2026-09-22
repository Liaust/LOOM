package knowledge

import (
	"context"
	"errors"
	"fmt"
)

type ImageOCRStageHandler struct {
	Service *Service
	OCR     OCRRuntime
}

func (handler ImageOCRStageHandler) Execute(ctx context.Context, item PipelineWorkItem, policy HeavyResourcePolicy) (HeavyStageObservation, []string, error) {
	if pipelineFileFamily(item.Object) != "image" {
		return HeavyStageObservation{}, nil, fmt.Errorf("%w: image OCR requires a standalone image", ErrInvalid)
	}
	prepare := DefaultImagePreparePolicy()
	prepare.MaxBytes = min(prepare.MaxBytes, policy.MaxInputBytes)
	object, cleanup, err := handler.Service.preparePathBackedExtractionObject(ctx, item.Object, prepare.MaxBytes)
	if err != nil {
		return HeavyStageObservation{}, nil, err
	}
	defer cleanup()
	image, err := PrepareImage(object.SourcePath, object.MimeType, prepare)
	if err != nil {
		return HeavyStageObservation{}, nil, err
	}
	if err = policy.ValidateWork(1, int64(len(image.Bytes))); err != nil {
		return HeavyStageObservation{}, nil, err
	}
	ocr := handler.OCR
	if ocr == nil {
		ocr = TesseractOCR{}
	}
	result, err := ocr.Recognize(ctx, OCRRequest{ImagePath: object.SourcePath, Language: "eng"})
	observation := observedHeavy(policy, 1, int64(len(image.Bytes)))
	// Images without recognizable text still proceed to independent vision.
	if errors.Is(err, ErrNoOCRText) {
		return observation, nil, nil
	}
	if err != nil {
		return observation, nil, err
	}
	version, err := handler.Service.store.getKnowledgeObjectVersion(ctx, item.Run.KnowledgeObjectVersionID)
	if err != nil {
		return observation, nil, err
	}
	_, err = handler.Service.createAndActivatePipelineArtifact(ctx, item, version, DerivedArtifactInput{
		ArtifactKind: ArtifactKindOCRText, SourceLocator: "image:document", Text: result.Text,
		GeneratorKey: "loom.image_ocr", GeneratorVersion: "image_ocr.v1", EngineKey: result.EngineKey,
		EngineVersion: result.EngineVersion, Language: result.Language, Confidence: result.Confidence,
		Metadata: map[string]any{"width": image.Width, "height": image.Height, "representation": "transcription"},
	})
	return observation, nil, err
}
