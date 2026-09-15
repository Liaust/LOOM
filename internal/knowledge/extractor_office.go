package knowledge

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"

	"loom.local/loom/internal/storagecatalog"
)

const (
	ExtractorKeyOfficeMetadata     = "office_metadata"
	ExtractorVersionOfficeMetadata = "office_metadata_extractor_v1"
)

var googleDocIDPattern = regexp.MustCompile(`/document/d/([^/?#]+)`)

type officeMetadataExtractor struct{}

func (officeMetadataExtractor) Key() string {
	return ExtractorKeyOfficeMetadata
}

func (officeMetadataExtractor) Version() string {
	return ExtractorVersionOfficeMetadata
}

func (officeMetadataExtractor) Supports(object KnowledgeObject) bool {
	return object.FileClass == storagecatalog.FileClassOfficeDocument
}

func (officeMetadataExtractor) RequiresContent(KnowledgeObject) bool {
	return false
}

func (extractor officeMetadataExtractor) Extract(input ExtractionInput) (ExtractionResult, error) {
	ext := knowledgeObjectExtension(input.Object)
	metadata := map[string]any{
		"file_class": input.Object.FileClass,
		"format":     strings.TrimPrefix(ext, "."),
	}
	switch ext {
	case ".gdoc":
		warnings := []string{}
		if err := enrichGoogleDocPointerMetadata(input.Object, metadata, input.MaxSourceBytes); err != nil {
			warnings = append(warnings, "gdoc_pointer_parse_failed: "+err.Error())
		}
		return officeMetadataOnly(input.Object, extractor.Key(), extractor.Version(), ExtractionStatusMetadataOnly, metadata, warnings), nil
	case ".doc", ".odt", ".rtf":
		warning := fmt.Sprintf("%s_body_extraction_unsupported", strings.TrimPrefix(ext, "."))
		return officeMetadataOnly(input.Object, extractor.Key(), extractor.Version(), ExtractionStatusUnsupportedBodyExtraction, metadata, []string{warning}), nil
	default:
		return officeMetadataOnly(input.Object, extractor.Key(), extractor.Version(), ExtractionStatusUnsupportedBodyExtraction, metadata, []string{"office_body_extraction_unsupported"}), nil
	}
}

func enrichGoogleDocPointerMetadata(object KnowledgeObject, metadata map[string]any, maxBytes int64) error {
	sourcePath := strings.TrimSpace(object.SourcePath)
	if sourcePath == "" {
		return fmt.Errorf("source_path is required for Google Docs pointer metadata")
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		return err
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		return fmt.Errorf("source file size %d exceeds max_bytes %d", info.Size(), maxBytes)
	}
	payload, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return err
	}
	for _, key := range []string{"url", "doc_id", "resource_id", "title", "email"} {
		if value, ok := decoded[key].(string); ok && strings.TrimSpace(value) != "" {
			metadata[key] = strings.TrimSpace(value)
		}
	}
	if _, ok := metadata["doc_id"]; !ok {
		if docID := googleDocIDFromURL(stringMapValue(metadata, "url")); docID != "" {
			metadata["doc_id"] = docID
		}
	}
	return nil
}

func googleDocIDFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err == nil && parsed.Path != "" {
		raw = parsed.Path
	}
	match := googleDocIDPattern.FindStringSubmatch(raw)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

func officeMetadataOnly(object KnowledgeObject, extractorKey, extractorVersion, status string, metadata map[string]any, warnings []string) ExtractionResult {
	result := metadataOnlyExtraction(object, status, strings.Join(warnings, "; "))
	result.ExtractorKey = extractorKey
	result.ExtractorVersion = extractorVersion
	result.Metadata = metadata
	result.Warnings = warnings
	metadataText := strings.TrimSpace(knowledgeMetadataSearchBody(object) + "\n" + searchableMetadataText(metadata))
	if metadataText != "" && len(result.TextSections) > 0 {
		result.TextSections[0].Text = metadataText
	}
	return result
}
