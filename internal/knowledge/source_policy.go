package knowledge

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"

	"loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/projectcontracts"
)

func knowledgeSourcePolicy(object KnowledgeObject) (*projectcontracts.KnowledgeSourcePolicy, error) {
	policy, _, err := knowledgeSourcePolicyAndSearch(object)
	return policy, err
}

func knowledgeSourcePolicyAndSearch(object KnowledgeObject) (*projectcontracts.KnowledgeSourcePolicy, string, error) {
	var metadata struct {
		Root struct {
			RelativePath string `json:"root_relative_path"`
			Source       struct {
				Policy json.RawMessage `json:"policy"`
			} `json:"knowledge_source"`
		} `json:"source_root"`
	}
	if len(object.Metadata) == 0 {
		return nil, "", nil
	}
	if err := json.Unmarshal(object.Metadata, &metadata); err != nil {
		return nil, "", fmt.Errorf("%w: invalid knowledge source metadata", ErrInvalid)
	}
	if len(metadata.Root.Source.Policy) == 0 {
		return nil, "", nil
	}
	policy, err := projectcontracts.DecodeKnowledgeSourcePolicy(metadata.Root.Source.Policy)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %s", ErrInvalid, err)
	}
	if p := policy.Processing; p != nil {
		if len(p.Paths) > 0 {
			matched, _ := watchedroots.MatchAny(p.Paths, object.RelativePath)
			if !matched {
				policy.Processing = nil
			}
		}
		if policy.Processing != nil && len(p.EmbeddingFileTypes) > 0 && !slices.Contains(p.EmbeddingFileTypes, PipelineDefinitionForObject(object).FileFamily) {
			disabled := false
			p.Embeddings = &disabled
		}
	}
	search := ""
	if policy.Files != nil {
		base, relative := metadata.Root.RelativePath, object.RelativePath
		if !fs.ValidPath(base) || !fs.ValidPath(relative) || strings.ContainsAny(base+relative, "\\\x00") {
			return nil, "", fmt.Errorf("%w: files.rules requires canonical source_root.root_relative_path and object.relative_path", ErrInvalid)
		}
		resolved, err := policy.Files.Resolve(path.Join(base, relative))
		if err != nil {
			return nil, "", fmt.Errorf("%w: %s", ErrInvalid, err)
		}
		search = resolved.Search
		metadataOnly := search == "none" || search == "metadata"
		if resolved.Enrichment != nil || metadataOnly {
			if policy.Processing == nil {
				policy.Processing = &projectcontracts.KnowledgeProcessingPolicy{}
			}
			p := policy.Processing
			disabled := false
			if metadataOnly || !slices.Contains(*resolved.Enrichment, "ocr") {
				p.OCR = "off"
			}
			if metadataOnly || !slices.Contains(*resolved.Enrichment, "vision") {
				p.ImageDescriptions = &disabled
			}
			if metadataOnly || !slices.Contains(*resolved.Enrichment, "embeddings") {
				p.Embeddings = &disabled
			}
		}
	}
	return &policy, search, nil
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
