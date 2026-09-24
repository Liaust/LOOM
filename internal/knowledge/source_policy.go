package knowledge

import (
	"encoding/json"
	"fmt"

	"loom.local/loom/internal/projectcontracts"
)

func knowledgeSourcePolicy(object KnowledgeObject) (*projectcontracts.KnowledgeSourcePolicy, error) {
	var metadata struct {
		Root struct {
			Source struct {
				Policy json.RawMessage `json:"policy"`
			} `json:"knowledge_source"`
		} `json:"source_root"`
	}
	if len(object.Metadata) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(object.Metadata, &metadata); err != nil {
		return nil, fmt.Errorf("%w: invalid knowledge source metadata", ErrInvalid)
	}
	if len(metadata.Root.Source.Policy) == 0 {
		return nil, nil
	}
	policy, err := projectcontracts.DecodeKnowledgeSourcePolicy(metadata.Root.Source.Policy)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalid, err)
	}
	return &policy, nil
}

func effectiveSourcePipelinePolicy(host PipelinePolicy, source *projectcontracts.KnowledgeSourcePolicy) PipelinePolicy {
	if source == nil || source.Processing == nil {
		return host
	}
	p := source.Processing
	if p.OCR == "off" {
		host.PDFOCREnabled, host.ImageOCREnabled = false, false
	}
	if p.Embeddings != nil {
		host.EmbeddingsEnabled = host.EmbeddingsEnabled && *p.Embeddings
	}
	if p.ImageDescriptions != nil {
		host.ImageDescriptionsEnabled = host.ImageDescriptionsEnabled && *p.ImageDescriptions
	}
	return host
}

func sourceStageDisabled(stage string, source *projectcontracts.KnowledgeSourcePolicy) bool {
	if source == nil || source.Processing == nil {
		return false
	}
	p := source.Processing
	switch stage {
	case FilePipelineStagePDFOCR, FilePipelineStageImageOCR:
		return p.OCR == "off"
	case FilePipelineStageEmbedding:
		return p.Embeddings != nil && !*p.Embeddings
	case FilePipelineStageImageDescription:
		return p.ImageDescriptions != nil && !*p.ImageDescriptions
	}
	return false
}
