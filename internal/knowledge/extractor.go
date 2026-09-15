package knowledge

import (
	"fmt"
	"path/filepath"
	"strings"

	"loom.local/loom/internal/storagecatalog"
)

const (
	KnowledgeExtractionSchemaVersion = "knowledge.extraction.v0.8.5"

	ExtractorKeyMarkdownText = "markdown_text"
	ExtractorKeyMetadataOnly = "metadata_only"

	ExtractionStatusExtracted                 = "extracted"
	ExtractionStatusMetadataOnly              = "metadata_only"
	ExtractionStatusTooLarge                  = "too_large"
	ExtractionStatusSourceUnavailable         = "source_unavailable"
	ExtractionStatusUnsupportedBodyExtraction = "unsupported_body_extraction"
	ExtractionStatusPasswordRequired          = "password_required"
	ExtractionStatusNoEmbeddedText            = "no_embedded_text"
	ExtractionStatusOCRDeferred               = "ocr_deferred"
	ExtractionStatusFailed                    = "failed"

	TextSourceEmbeddedText   = "embedded_text"
	TextSourceStructuredText = "structured_text"
	TextSourceMetadataText   = "metadata_text"
)

type ExtractionInput struct {
	Object                KnowledgeObject
	Content               string
	MaxSourceBytes        int64
	MaxExtractedTextBytes int64
	Chunker               ChunkerOptions
}

type ExtractionResult struct {
	Status                 string                  `json:"status"`
	ExtractorKey           string                  `json:"extractor_key"`
	ExtractorVersion       string                  `json:"extractor_version"`
	TextSections           []ExtractedTextSection  `json:"text_sections,omitempty"`
	Metadata               map[string]any          `json:"metadata,omitempty"`
	Warnings               []string                `json:"warnings,omitempty"`
	AbsoluteTimeCandidates []AbsoluteTimeCandidate `json:"absolute_time_candidates,omitempty"`
	AbsoluteTimeWarnings   []AbsoluteTimeWarning   `json:"absolute_time_warnings,omitempty"`
	AbsoluteTime           *AbsoluteTime           `json:"absolute_time,omitempty"`
	Document               TextDocument            `json:"document,omitempty"`
	Chunks                 []TextChunkInput        `json:"chunks,omitempty"`
	Links                  []ExtractedLink         `json:"links,omitempty"`
}

type ExtractedTextSection struct {
	Index          int            `json:"index"`
	Text           string         `json:"text"`
	TextSource     string         `json:"text_source"`
	StructuralPath string         `json:"structural_path,omitempty"`
	StartOffset    int            `json:"start_offset,omitempty"`
	EndOffset      int            `json:"end_offset,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

type Extractor interface {
	Key() string
	Version() string
	Supports(KnowledgeObject) bool
	RequiresContent(KnowledgeObject) bool
	Extract(ExtractionInput) (ExtractionResult, error)
}

type ExtractionRegistry struct {
	extractors []Extractor
}

func NewExtractionRegistry(extractors ...Extractor) ExtractionRegistry {
	return ExtractionRegistry{extractors: append([]Extractor(nil), extractors...)}
}

func DefaultExtractionRegistry() ExtractionRegistry {
	return NewExtractionRegistry(
		newPDFExtractor(nil),
		docxExtractor{},
		officeMetadataExtractor{},
		structuredDataExtractor{},
		codeExtractor{},
		markdownTextExtractor{},
	)
}

func (r ExtractionRegistry) Extract(input ExtractionInput) (ExtractionResult, error) {
	extractor, ok := r.ExtractorFor(input.Object)
	if !ok {
		return metadataOnlyExtraction(input.Object, ExtractionStatusUnsupportedBodyExtraction, "body extraction is not supported for this file class"), nil
	}
	return extractor.Extract(input)
}

func (r ExtractionRegistry) SupportsBodyExtraction(object KnowledgeObject) bool {
	_, ok := r.ExtractorFor(object)
	return ok
}

func (r ExtractionRegistry) ExtractorFor(object KnowledgeObject) (Extractor, bool) {
	for _, extractor := range r.extractors {
		if extractor.Supports(object) {
			return extractor, true
		}
	}
	return nil, false
}

type markdownTextExtractor struct{}

func (markdownTextExtractor) Key() string {
	return ExtractorKeyMarkdownText
}

func (markdownTextExtractor) Version() string {
	return KnowledgeMarkdownTextExtractorVersion
}

func (markdownTextExtractor) Supports(object KnowledgeObject) bool {
	return isMarkdownTextObject(object)
}

func (markdownTextExtractor) RequiresContent(KnowledgeObject) bool {
	return true
}

func (extractor markdownTextExtractor) Extract(input ExtractionInput) (ExtractionResult, error) {
	var document TextDocument
	switch input.Object.FileClass {
	case storagecatalog.FileClassMarkdown:
		document = ExtractMarkdownDocument(input.Content)
	case storagecatalog.FileClassText:
		document = ExtractPlainTextDocument(input.Content)
	default:
		return ExtractionResult{}, fmt.Errorf("%w: knowledge object file class %q is not supported by markdown text extractor", ErrInvalid, input.Object.FileClass)
	}
	if input.MaxExtractedTextBytes > 0 && int64(len(document.Text)) > input.MaxExtractedTextBytes {
		return metadataOnlyExtraction(
			input.Object,
			ExtractionStatusTooLarge,
			fmt.Sprintf("extracted text size %d exceeds max_extracted_text_bytes %d", len(document.Text), input.MaxExtractedTextBytes),
		), nil
	}
	sections := []ExtractedTextSection{}
	if strings.TrimSpace(document.Text) != "" {
		sections = append(sections, ExtractedTextSection{
			Index:       1,
			Text:        document.Text,
			TextSource:  TextSourceEmbeddedText,
			StartOffset: 0,
			EndOffset:   len(document.Text),
			Metadata: map[string]any{
				"file_class": input.Object.FileClass,
			},
		})
	}
	chunks := ChunkTextDocumentWithProvenance(document, input.Chunker, ChunkProvenance{
		TextSource:       TextSourceEmbeddedText,
		ExtractorKey:     extractor.Key(),
		ExtractorVersion: extractor.Version(),
		ExtractionStatus: ExtractionStatusExtracted,
		SectionIndex:     1,
	})
	result := ExtractionResult{
		Status:           ExtractionStatusExtracted,
		ExtractorKey:     extractor.Key(),
		ExtractorVersion: extractor.Version(),
		TextSections:     sections,
		Metadata: map[string]any{
			"file_class": input.Object.FileClass,
		},
		Warnings: document.Warnings,
		Document: document,
		Chunks:   chunks,
		Links:    document.Links,
	}
	if input.Object.FileClass == storagecatalog.FileClassMarkdown {
		result = attachExtractionAbsoluteTime(result, document.AbsoluteTimeCandidates)
	}
	return result, nil
}

func metadataOnlyExtraction(object KnowledgeObject, status, reason string) ExtractionResult {
	status = strings.TrimSpace(status)
	if status == "" {
		status = ExtractionStatusMetadataOnly
	}
	reason = strings.TrimSpace(reason)
	metadata := map[string]any{
		"file_class": object.FileClass,
	}
	if reason != "" {
		metadata["reason"] = reason
	}
	return ExtractionResult{
		Status:           status,
		ExtractorKey:     ExtractorKeyMetadataOnly,
		ExtractorVersion: KnowledgeMarkdownTextExtractorVersion,
		TextSections: []ExtractedTextSection{{
			Index:      1,
			Text:       knowledgeMetadataSearchBody(object),
			TextSource: TextSourceMetadataText,
			Metadata:   metadata,
		}},
		Metadata: metadata,
		Warnings: warningIfNotEmpty(reason),
	}
}

func warningIfNotEmpty(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return []string{value}
}

func attachExtractionAbsoluteTime(result ExtractionResult, candidates []AbsoluteTimeCandidate) ExtractionResult {
	normalized, warnings, err := normalizeAbsoluteTimeCandidates(candidates)
	if err != nil {
		result.Warnings = append(result.Warnings, "absolute_time_candidate_invalid: "+err.Error())
		return result
	}
	result.AbsoluteTimeCandidates = normalized
	result.AbsoluteTimeWarnings = warnings
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	result.Metadata["absolute_time_candidates"] = normalized
	result.Metadata["absolute_time_warnings"] = warnings
	for _, warning := range warnings {
		result.Warnings = appendUniqueString(result.Warnings, fmt.Sprintf("%s: %s: %s", warning.Code, warning.Basis, warning.RawValue))
	}
	return result
}

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func knowledgeObjectExtension(object KnowledgeObject) string {
	for _, value := range []string{object.RelativePath, object.SourcePath, object.Title} {
		if ext := strings.ToLower(filepath.Ext(strings.TrimSpace(value))); ext != "" {
			return ext
		}
	}
	return ""
}

func extractionResultFromDocument(input ExtractionInput, extractor Extractor, status, textSource string, document TextDocument, metadata map[string]any) (ExtractionResult, error) {
	if input.MaxExtractedTextBytes > 0 && int64(len(document.Text)) > input.MaxExtractedTextBytes {
		return metadataOnlyExtraction(
			input.Object,
			ExtractionStatusTooLarge,
			fmt.Sprintf("extracted text size %d exceeds max_extracted_text_bytes %d", len(document.Text), input.MaxExtractedTextBytes),
		), nil
	}
	status = strings.TrimSpace(status)
	if status == "" {
		status = ExtractionStatusExtracted
	}
	textSource = strings.TrimSpace(textSource)
	if textSource == "" {
		textSource = TextSourceEmbeddedText
	}
	sections := []ExtractedTextSection{}
	if strings.TrimSpace(document.Text) != "" {
		sections = append(sections, ExtractedTextSection{
			Index:       1,
			Text:        document.Text,
			TextSource:  textSource,
			StartOffset: 0,
			EndOffset:   len(document.Text),
			Metadata:    metadata,
		})
	}
	chunks := ChunkTextDocumentWithProvenance(document, input.Chunker, ChunkProvenance{
		TextSource:       textSource,
		ExtractorKey:     extractor.Key(),
		ExtractorVersion: extractor.Version(),
		ExtractionStatus: status,
		SectionIndex:     1,
	})
	return ExtractionResult{
		Status:           status,
		ExtractorKey:     extractor.Key(),
		ExtractorVersion: extractor.Version(),
		TextSections:     sections,
		Metadata:         metadata,
		Warnings:         document.Warnings,
		Document:         document,
		Chunks:           chunks,
		Links:            document.Links,
	}, nil
}
