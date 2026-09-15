package knowledge

import (
	"regexp"
	"strings"

	"loom.local/loom/internal/storagecatalog"
)

const (
	ExtractorKeyCode     = "code_text"
	ExtractorVersionCode = "code_text_extractor_v1"
)

var codeSymbolPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?m)^\s*func\s+([A-Za-z_][A-Za-z0-9_]*)`),
	regexp.MustCompile(`(?m)^\s*def\s+([A-Za-z_][A-Za-z0-9_]*)`),
	regexp.MustCompile(`(?m)^\s*class\s+([A-Za-z_][A-Za-z0-9_]*)`),
	regexp.MustCompile(`(?m)^\s*(?:export\s+)?(?:async\s+)?function\s+([A-Za-z_][A-Za-z0-9_]*)`),
	regexp.MustCompile(`(?m)^\s*(?:const|let|var)\s+([A-Za-z_][A-Za-z0-9_]*)\s*=`),
	regexp.MustCompile(`(?m)^\s*([A-Za-z_][A-Za-z0-9_]*)\s*\(\)\s*\{`),
}

type codeExtractor struct{}

func (codeExtractor) Key() string {
	return ExtractorKeyCode
}

func (codeExtractor) Version() string {
	return ExtractorVersionCode
}

func (codeExtractor) Supports(object KnowledgeObject) bool {
	return object.FileClass == storagecatalog.FileClassCode
}

func (codeExtractor) RequiresContent(KnowledgeObject) bool {
	return true
}

func (extractor codeExtractor) Extract(input ExtractionInput) (ExtractionResult, error) {
	text := normalizeTextNewlines(input.Content)
	metadata := map[string]any{
		"file_class":    input.Object.FileClass,
		"parser_mode":   "regex_symbols",
		"line_count":    countLines(text),
		"symbols":       extractCodeSymbols(text),
		"source_format": strings.TrimPrefix(knowledgeObjectExtension(input.Object), "."),
	}
	document := TextDocument{Text: text}
	return extractionResultFromDocument(input, extractor, ExtractionStatusExtracted, TextSourceEmbeddedText, document, metadata)
}

func extractCodeSymbols(text string) []string {
	seen := map[string]bool{}
	symbols := []string{}
	for _, pattern := range codeSymbolPatterns {
		for _, match := range pattern.FindAllStringSubmatch(text, -1) {
			if len(match) < 2 {
				continue
			}
			symbol := strings.TrimSpace(match[1])
			if symbol == "" || seen[symbol] {
				continue
			}
			seen[symbol] = true
			symbols = append(symbols, symbol)
		}
	}
	return symbols
}

func countLines(text string) int {
	if text == "" {
		return 0
	}
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}
