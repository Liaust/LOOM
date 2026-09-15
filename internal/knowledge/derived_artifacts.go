package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"loom.local/loom/internal/ids"
)

type DerivedArtifactInput struct {
	ArtifactKind     string         `json:"artifact_kind"`
	SourceLocator    string         `json:"source_locator"`
	Text             string         `json:"text"`
	GeneratorKey     string         `json:"generator_key"`
	GeneratorVersion string         `json:"generator_version"`
	EngineKey        string         `json:"engine_key,omitempty"`
	EngineVersion    string         `json:"engine_version,omitempty"`
	PromptVersion    string         `json:"prompt_version,omitempty"`
	Language         string         `json:"language,omitempty"`
	Confidence       *float64       `json:"confidence,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
}

func ArtifactInputsFromExtraction(result ExtractionResult) []DerivedArtifactInput {
	inputs := make([]DerivedArtifactInput, 0, len(result.TextSections))
	for _, section := range result.TextSections {
		text := normalizeArtifactText(section.Text)
		if text == "" {
			continue
		}
		kind := artifactKindForTextSource(section.TextSource)
		locator := strings.TrimSpace(section.StructuralPath)
		if locator == "" {
			if page := intMapValue(section.Metadata, "page_number"); page > 0 {
				locator = fmt.Sprintf("page:%d", page)
			} else if section.Index > 0 {
				locator = fmt.Sprintf("section:%d", section.Index)
			} else {
				locator = "document"
			}
		}
		inputs = append(inputs, DerivedArtifactInput{
			ArtifactKind: kind, SourceLocator: locator, Text: text,
			GeneratorKey: result.ExtractorKey, GeneratorVersion: result.ExtractorVersion,
			Metadata: cloneStringAnyMap(section.Metadata),
		})
	}
	return inputs
}

func (s *Service) PrepareDerivedArtifact(run PipelineRun, stage PipelineStageRun, version KnowledgeObjectVersion, input DerivedArtifactInput) (DerivedArtifact, error) {
	text := normalizeArtifactText(input.Text)
	metadata, err := json.Marshal(input.Metadata)
	if err != nil {
		return DerivedArtifact{}, err
	}
	if string(metadata) == "null" {
		metadata = []byte(`{}`)
	}
	contentHash := hashArtifactValue(text)
	identity := strings.Join([]string{
		version.KnowledgeObjectVersionID, input.ArtifactKind, input.SourceLocator,
		input.GeneratorKey, input.GeneratorVersion, input.EngineKey, input.EngineVersion,
		input.PromptVersion, input.Language, contentHash,
	}, "\n")
	now := s.currentTime()
	artifact := DerivedArtifact{
		KnowledgeDerivedArtifactID: ids.NewKnowledgeDerivedArtifactID(), KnowledgeObjectID: version.KnowledgeObjectID,
		KnowledgeObjectVersionID: version.KnowledgeObjectVersionID, KnowledgePipelineRunID: run.KnowledgePipelineRunID,
		KnowledgePipelineStageRunID: stage.KnowledgePipelineStageRunID, Generation: run.Generation,
		ArtifactKind: input.ArtifactKind, SourceLocator: strings.TrimSpace(input.SourceLocator), TextContent: &text,
		ContentHash: contentHash, InputHash: hashArtifactValue(identity), GeneratorKey: strings.TrimSpace(input.GeneratorKey),
		GeneratorVersion: strings.TrimSpace(input.GeneratorVersion), EngineKey: strings.TrimSpace(input.EngineKey),
		EngineVersion: strings.TrimSpace(input.EngineVersion), PromptVersion: strings.TrimSpace(input.PromptVersion),
		Language: strings.TrimSpace(input.Language), Confidence: input.Confidence, State: ArtifactStateReusable,
		SourceCreatedAt: version.SourceCreatedAt, SourceModifiedAt: version.SourceModifiedAt,
		RecencyAt: version.RecencyAt, RecencyBasis: version.RecencyBasis, Metadata: metadata, CreatedAt: now,
	}
	return artifact, ValidateDerivedArtifact(artifact)
}

func artifactKindForTextSource(source string) string {
	switch source {
	case TextSourceStructuredText:
		return ArtifactKindStructuredText
	case TextSourceMetadataText:
		return ArtifactKindMetadataText
	default:
		return ArtifactKindEmbeddedText
	}
}

func normalizeArtifactText(value string) string {
	return strings.TrimSpace(normalizeTextNewlines(value))
}

func hashArtifactValue(value string) string {
	hash := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(hash[:])
}

func cloneStringAnyMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return map[string]any{}
	}
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	output := make(map[string]any, len(input))
	for _, key := range keys {
		output[key] = input[key]
	}
	return output
}

func extractionPipelineVersionMetadataNormalized(extraction ExtractionResult, artifactIDs []string) json.RawMessage {
	payload, err := json.Marshal(map[string]any{
		"schema_version": "knowledge.text_object_version.v1", "pipeline_key": KnowledgeObjectPipelineNotesFileExtraction,
		"pipeline_version": PipelineDefinitionVersionV1, "extractor_key": extraction.ExtractorKey,
		"extractor_version": extraction.ExtractorVersion, "extraction_status": extraction.Status,
		"chunker_version": KnowledgeMarkdownTextChunkerVersion, "frontmatter": extraction.Document.Frontmatter,
		"frontmatter_raw": extraction.Document.FrontmatterRaw, "headings": extraction.Document.Headings,
		"links": extraction.Links, "warnings": extraction.Warnings, "absolute_time_candidates": extraction.AbsoluteTimeCandidates,
		"absolute_time_warnings": extraction.AbsoluteTimeWarnings, "absolute_time": extraction.AbsoluteTime,
		"extraction_metadata": extraction.Metadata, "active_artifact_ids": artifactIDs,
		"text_section_count": len(extraction.TextSections), "extracted_text_len": len(extraction.Document.Text),
	})
	if err != nil {
		return emptyJSONObject
	}
	return payload
}
