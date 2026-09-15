package knowledge

import "strings"

type KnowledgeObjectExtractionSummary struct {
	Status           string `json:"status"`
	ExtractorKey     string `json:"extractor_key,omitempty"`
	ExtractorVersion string `json:"extractor_version,omitempty"`
	TextSectionCount int    `json:"text_section_count,omitempty"`
	ChunkCount       int    `json:"chunk_count,omitempty"`
	LinkCount        int    `json:"link_count,omitempty"`
	HeadingCount     int    `json:"heading_count,omitempty"`
	WarningCount     int    `json:"warning_count,omitempty"`
	MetadataOnly     bool   `json:"metadata_only,omitempty"`
}

func SummarizeKnowledgeObjectExtraction(object KnowledgeObject) KnowledgeObjectExtractionSummary {
	extraction := objectExtractionMetadata(object.Metadata)
	status := normalizeNotesExtractionStatus(stringMapValue(extraction, "status"), object.ProcessingState)
	return KnowledgeObjectExtractionSummary{
		Status:           status,
		ExtractorKey:     stringMapValue(extraction, "extractor_key"),
		ExtractorVersion: stringMapValue(extraction, "extractor_version"),
		TextSectionCount: intMapValue(extraction, "text_section_count"),
		ChunkCount:       intMapValue(extraction, "chunk_count"),
		LinkCount:        intMapValue(extraction, "link_count"),
		HeadingCount:     intMapValue(extraction, "heading_count"),
		WarningCount:     lenMapList(extraction, "warnings"),
		MetadataOnly:     extractionStatusIsMetadataOnly(status),
	}
}

func KnowledgeObjectExtractionStatus(object KnowledgeObject) string {
	return SummarizeKnowledgeObjectExtraction(object).Status
}

func extractionStatusIsMetadataOnly(status string) bool {
	switch strings.TrimSpace(status) {
	case ExtractionStatusMetadataOnly,
		ExtractionStatusTooLarge,
		ExtractionStatusSourceUnavailable,
		ExtractionStatusUnsupportedBodyExtraction,
		ExtractionStatusPasswordRequired,
		ExtractionStatusNoEmbeddedText,
		ExtractionStatusOCRDeferred:
		return true
	default:
		return false
	}
}

func lenMapList(input map[string]any, key string) int {
	if input == nil {
		return 0
	}
	switch value := input[key].(type) {
	case []any:
		return len(value)
	case []string:
		return len(value)
	default:
		return 0
	}
}
