package knowledge

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	ExtractorKeyStructuredData     = "structured_data"
	ExtractorVersionStructuredData = "structured_data_extractor_v1"
)

var structuredDataExtensions = map[string]bool{
	".csv":  true,
	".json": true,
	".toml": true,
	".tsv":  true,
	".xml":  true,
	".yaml": true,
	".yml":  true,
}

type structuredDataExtractor struct{}

func (structuredDataExtractor) Key() string {
	return ExtractorKeyStructuredData
}

func (structuredDataExtractor) Version() string {
	return ExtractorVersionStructuredData
}

func (structuredDataExtractor) Supports(object KnowledgeObject) bool {
	return structuredDataExtensions[knowledgeObjectExtension(object)]
}

func (structuredDataExtractor) RequiresContent(KnowledgeObject) bool {
	return true
}

func (extractor structuredDataExtractor) Extract(input ExtractionInput) (ExtractionResult, error) {
	ext := knowledgeObjectExtension(input.Object)
	metadata := map[string]any{
		"file_class": input.Object.FileClass,
		"format":     strings.TrimPrefix(ext, "."),
	}
	var document TextDocument
	switch ext {
	case ".json":
		document = extractJSONStructuredText(input.Content, metadata)
	case ".yaml", ".yml":
		document = extractYAMLStructuredText(input.Content, metadata)
	case ".xml":
		document = extractXMLStructuredText(input.Content, metadata)
	case ".csv":
		document = extractDelimitedStructuredText(input.Content, ',', metadata)
	case ".tsv":
		document = extractDelimitedStructuredText(input.Content, '\t', metadata)
	case ".toml":
		metadata["parse_status"] = "not_parsed"
		document = extractBoundedPlainTextDocument(input.Content, []string{"toml_parse_deferred"})
	default:
		metadata["parse_status"] = "fallback_text"
		document = extractBoundedPlainTextDocument(input.Content, []string{"structured_format_unrecognized"})
	}
	return extractionResultFromDocument(input, extractor, ExtractionStatusExtracted, TextSourceStructuredText, document, metadata)
}

func extractJSONStructuredText(content string, metadata map[string]any) TextDocument {
	var decoded any
	if err := json.Unmarshal([]byte(content), &decoded); err != nil {
		metadata["parse_status"] = "fallback_text"
		return extractBoundedPlainTextDocument(content, nonEmptyWarning("json_parse_failed", err))
	}
	metadata["parse_status"] = "parsed"
	metadata["top_level_keys"] = topLevelKeys(decoded)
	lines := []string{}
	flattenStructuredValue("", decoded, &lines)
	return TextDocument{Text: strings.Join(lines, "\n")}
}

func extractYAMLStructuredText(content string, metadata map[string]any) TextDocument {
	var decoded any
	if err := yaml.Unmarshal([]byte(content), &decoded); err != nil {
		metadata["parse_status"] = "fallback_text"
		return extractBoundedPlainTextDocument(content, nonEmptyWarning("yaml_parse_failed", err))
	}
	decoded = normalizeYAMLValue(decoded)
	metadata["parse_status"] = "parsed"
	metadata["top_level_keys"] = topLevelKeys(decoded)
	lines := []string{}
	flattenStructuredValue("", decoded, &lines)
	return TextDocument{Text: strings.Join(lines, "\n")}
}

func extractXMLStructuredText(content string, metadata map[string]any) TextDocument {
	decoder := xml.NewDecoder(strings.NewReader(content))
	lines := []string{}
	elementStack := []string{}
	elementCounts := map[string]int{}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			metadata["parse_status"] = "fallback_text"
			return extractBoundedPlainTextDocument(content, nonEmptyWarning("xml_parse_failed", err))
		}
		switch typed := token.(type) {
		case xml.StartElement:
			elementStack = append(elementStack, typed.Name.Local)
			elementCounts[typed.Name.Local]++
		case xml.EndElement:
			if len(elementStack) > 0 {
				elementStack = elementStack[:len(elementStack)-1]
			}
		case xml.CharData:
			text := strings.TrimSpace(string([]byte(typed)))
			if text != "" {
				lines = append(lines, strings.Join(elementStack, ".")+": "+text)
			}
		}
	}
	metadata["parse_status"] = "parsed"
	metadata["elements"] = sortedMapKeys(elementCounts)
	return TextDocument{Text: strings.Join(lines, "\n")}
}

func extractDelimitedStructuredText(content string, comma rune, metadata map[string]any) TextDocument {
	reader := csv.NewReader(bytes.NewBufferString(content))
	reader.Comma = comma
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		metadata["parse_status"] = "fallback_text"
		return extractBoundedPlainTextDocument(content, nonEmptyWarning("delimited_parse_failed", err))
	}
	metadata["parse_status"] = "parsed"
	metadata["row_count"] = len(rows)
	headers := []string{}
	if len(rows) > 0 {
		headers = append(headers, rows[0]...)
		metadata["headers"] = headers
	}
	lines := []string{}
	for rowIndex, row := range rows {
		if rowIndex == 0 && len(headers) > 0 {
			continue
		}
		fields := []string{}
		for fieldIndex, value := range row {
			label := fmt.Sprintf("column_%d", fieldIndex+1)
			if fieldIndex < len(headers) && strings.TrimSpace(headers[fieldIndex]) != "" {
				label = strings.TrimSpace(headers[fieldIndex])
			}
			fields = append(fields, label+"="+strings.TrimSpace(value))
		}
		if len(fields) > 0 {
			lines = append(lines, fmt.Sprintf("row_%d: %s", rowIndex+1, strings.Join(fields, " | ")))
		}
	}
	return TextDocument{Text: strings.Join(lines, "\n")}
}

func topLevelKeys(value any) []string {
	switch typed := value.(type) {
	case map[string]any:
		return sortedMapKeys(typed)
	default:
		return nil
	}
}

func flattenStructuredValue(prefix string, value any, lines *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		keys := sortedMapKeys(typed)
		for _, key := range keys {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			flattenStructuredValue(path, typed[key], lines)
		}
	case []any:
		for i, item := range typed {
			path := fmt.Sprintf("%s[%d]", prefix, i)
			flattenStructuredValue(path, item, lines)
		}
	default:
		text := strings.TrimSpace(fmt.Sprint(typed))
		if text != "" {
			*lines = append(*lines, strings.TrimPrefix(prefix+": "+text, ": "))
		}
	}
}

func sortedMapKeys[V any](input map[string]V) []string {
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
