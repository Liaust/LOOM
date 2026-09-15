package knowledge

import (
	"fmt"
	"sort"
	"strings"

	"loom.local/loom/internal/storagecatalog"
)

const (
	PipelineDefinitionVersionV1 = "notes_file_pipeline.v1"
	StageContractVersionV1      = "v1"
)

type PipelineStageDefinition struct {
	StageKey            string   `json:"stage_key"`
	ContractVersion     string   `json:"contract_version"`
	ExecutionClass      string   `json:"execution_class"`
	Dependencies        []string `json:"dependencies,omitempty"`
	InputArtifactKinds  []string `json:"input_artifact_kinds,omitempty"`
	OutputArtifactKinds []string `json:"output_artifact_kinds,omitempty"`
	QuietWindow         bool     `json:"quiet_window,omitempty"`
	Required            bool     `json:"required"`
	ImplementationKey   string   `json:"implementation_key"`
}

type PipelineDefinition struct {
	Key        string                    `json:"key"`
	Version    string                    `json:"version"`
	FileFamily string                    `json:"file_family"`
	Stages     []PipelineStageDefinition `json:"stages"`
}

type PipelinePolicy struct {
	PDFOCREnabled            bool `json:"pdf_ocr_enabled"`
	ImageDescriptionsEnabled bool `json:"image_descriptions_enabled"`
	EmbeddingsEnabled        bool `json:"embeddings_enabled"`
}

func DefaultPipelinePolicy() PipelinePolicy {
	return PipelinePolicy{PDFOCREnabled: true, ImageDescriptionsEnabled: true, EmbeddingsEnabled: true}
}

func PipelineDefinitionForObject(object KnowledgeObject) PipelineDefinition {
	family := pipelineFileFamily(object)
	key := "notes_" + family
	coordinator := func(stage string, deps []string, inputs, outputs []string) PipelineStageDefinition {
		return PipelineStageDefinition{StageKey: stage, ContractVersion: StageContractVersionV1, ExecutionClass: PipelineExecutionCoordinator, Dependencies: deps, InputArtifactKinds: inputs, OutputArtifactKinds: outputs, Required: true, ImplementationKey: stage + ".v1"}
	}
	heavy := func(stage string, deps []string, inputs, outputs []string) PipelineStageDefinition {
		value := coordinator(stage, deps, inputs, outputs)
		value.ExecutionClass = PipelineExecutionHeavy
		value.QuietWindow = true
		return value
	}
	metadata := coordinator(FilePipelineStageMetadata, nil, nil, []string{ArtifactKindMetadataText})
	native := coordinator(FilePipelineStageNativeText, []string{FilePipelineStageMetadata}, nil, []string{ArtifactKindEmbeddedText, ArtifactKindStructuredText})
	consolidateAfter := func(dependency string) PipelineStageDefinition {
		return coordinator(FilePipelineStageConsolidateText, []string{dependency}, []string{ArtifactKindEmbeddedText, ArtifactKindStructuredText, ArtifactKindOCRText, ArtifactKindVisionDescription}, []string{ArtifactKindConsolidatedText})
	}
	finish := func(dependency string) []PipelineStageDefinition {
		chunk := coordinator(FilePipelineStageChunk, []string{dependency}, []string{ArtifactKindConsolidatedText}, nil)
		lexical := coordinator(FilePipelineStageLexicalIndex, []string{FilePipelineStageChunk}, nil, nil)
		embedding := heavy(FilePipelineStageEmbedding, []string{FilePipelineStageLexicalIndex}, []string{ArtifactKindConsolidatedText}, nil)
		finalize := coordinator(FilePipelineStageFinalize, []string{FilePipelineStageEmbedding}, nil, nil)
		return []PipelineStageDefinition{chunk, lexical, embedding, finalize}
	}

	definition := PipelineDefinition{Key: key, Version: PipelineDefinitionVersionV1, FileFamily: family}
	switch family {
	case "pdf":
		analysis := coordinator(FilePipelineStagePDFPageAnalysis, []string{FilePipelineStageNativeText}, []string{ArtifactKindEmbeddedText}, nil)
		ocr := heavy(FilePipelineStagePDFOCR, []string{FilePipelineStagePDFPageAnalysis}, nil, []string{ArtifactKindOCRText})
		definition.Stages = append([]PipelineStageDefinition{metadata, native, analysis, ocr, consolidateAfter(FilePipelineStagePDFOCR)}, finish(FilePipelineStageConsolidateText)...)
	case "image":
		description := heavy(FilePipelineStageImageDescription, []string{FilePipelineStageMetadata}, nil, []string{ArtifactKindVisionDescription})
		definition.Stages = append([]PipelineStageDefinition{metadata, description, consolidateAfter(FilePipelineStageImageDescription)}, finish(FilePipelineStageConsolidateText)...)
	case "metadata_only":
		lexical := coordinator(FilePipelineStageLexicalIndex, []string{FilePipelineStageMetadata}, []string{ArtifactKindMetadataText}, nil)
		finalize := coordinator(FilePipelineStageFinalize, []string{FilePipelineStageLexicalIndex}, nil, nil)
		definition.Stages = []PipelineStageDefinition{metadata, lexical, finalize}
	default:
		definition.Stages = append([]PipelineStageDefinition{metadata, native, consolidateAfter(FilePipelineStageNativeText)}, finish(FilePipelineStageConsolidateText)...)
	}
	return definition
}

func pipelineFileFamily(object KnowledgeObject) string {
	switch object.FileClass {
	case storagecatalog.FileClassMarkdown:
		return "markdown"
	case storagecatalog.FileClassText:
		return "text"
	case storagecatalog.FileClassCode:
		switch knowledgeObjectExtension(object) {
		case ".json", ".yaml", ".yml", ".toml", ".xml", ".csv", ".tsv":
			return "structured"
		default:
			return "code"
		}
	case storagecatalog.FileClassOfficeDocument:
		if knowledgeObjectExtension(object) == ".docx" {
			return "docx"
		}
		return "metadata_only"
	case storagecatalog.FileClassPDF:
		return "pdf"
	case storagecatalog.FileClassImage:
		return "image"
	default:
		return "metadata_only"
	}
}

func ValidatePipelineDefinition(definition PipelineDefinition) error {
	if strings.TrimSpace(definition.Key) == "" || strings.TrimSpace(definition.Version) == "" || len(definition.Stages) == 0 {
		return fmt.Errorf("%w: pipeline definition key, version, and stages are required", ErrInvalid)
	}
	byKey := make(map[string]PipelineStageDefinition, len(definition.Stages))
	for _, stage := range definition.Stages {
		if !isFilePipelineStage(stage.StageKey) || !isPipelineExecutionClass(stage.ExecutionClass) || strings.TrimSpace(stage.ContractVersion) == "" || strings.TrimSpace(stage.ImplementationKey) == "" {
			return fmt.Errorf("%w: invalid stage declaration %q", ErrInvalid, stage.StageKey)
		}
		if _, exists := byKey[stage.StageKey]; exists {
			return fmt.Errorf("%w: duplicate stage %q", ErrInvalid, stage.StageKey)
		}
		byKey[stage.StageKey] = stage
	}
	for _, stage := range definition.Stages {
		for _, dependency := range stage.Dependencies {
			if _, ok := byKey[dependency]; !ok {
				return fmt.Errorf("%w: stage %q has missing dependency %q", ErrInvalid, stage.StageKey, dependency)
			}
		}
	}
	visiting := map[string]bool{}
	visited := map[string]bool{}
	var visit func(string) error
	visit = func(key string) error {
		if visiting[key] {
			return fmt.Errorf("%w: pipeline definition contains a cycle at %q", ErrInvalid, key)
		}
		if visited[key] {
			return nil
		}
		visiting[key] = true
		for _, dependency := range byKey[key].Dependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visiting[key] = false
		visited[key] = true
		return nil
	}
	for key := range byKey {
		if err := visit(key); err != nil {
			return err
		}
	}
	return nil
}

func sortedStageDependencies(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
