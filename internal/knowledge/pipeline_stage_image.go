package knowledge

import (
	"context"
	"fmt"
)

type ImageDescriptionStageHandler struct {
	Service       *Service
	Runtime       VisionRuntime
	PreparePolicy ImagePreparePolicy
}

func (handler ImageDescriptionStageHandler) Execute(ctx context.Context, item PipelineWorkItem, policy HeavyResourcePolicy) (HeavyStageObservation, []string, error) {
	if pipelineFileFamily(item.Object) != "image" {
		return HeavyStageObservation{}, nil, fmt.Errorf("%w: image description requires a standalone image object", ErrInvalid)
	}
	if handler.Runtime == nil {
		return observedHeavy(policy, 0, 0), []string{"local vision runtime is disabled or unavailable"}, nil
	}
	prepare := handler.PreparePolicy
	if prepare.MaxBytes == 0 {
		prepare = DefaultImagePreparePolicy()
	}
	if prepare.MaxBytes > policy.MaxInputBytes {
		prepare.MaxBytes = policy.MaxInputBytes
	}
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
	result, err := handler.Runtime.Describe(ctx, VisionRequest{Image: image.Bytes, MimeType: image.MimeType, Prompt: ImageDescriptionPrompt()})
	if err != nil {
		return HeavyStageObservation{}, nil, err
	}
	version, err := handler.Service.store.getKnowledgeObjectVersion(ctx, item.Run.KnowledgeObjectVersionID)
	if err != nil {
		return HeavyStageObservation{}, nil, err
	}
	_, err = handler.Service.createAndActivatePipelineArtifact(ctx, item, version, DerivedArtifactInput{ArtifactKind: ArtifactKindVisionDescription, SourceLocator: "image:document", Text: result.Description, GeneratorKey: "loom.image_description", GeneratorVersion: "image_description.v1", EngineKey: result.RuntimeKey, EngineVersion: result.RuntimeVersion, PromptVersion: VisionPromptVersionV1, Metadata: map[string]any{"model": result.Model, "mime_type": image.MimeType, "width": image.Width, "height": image.Height, "representation": image.Representation}})
	return observedHeavy(policy, 1, int64(len(image.Bytes))), nil, err
}
