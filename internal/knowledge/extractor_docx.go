package knowledge

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"loom.local/loom/internal/storagecatalog"
)

const (
	ExtractorKeyDOCX     = "docx"
	ExtractorVersionDOCX = "docx_extractor_v1"
)

var docxHeadingStylePattern = regexp.MustCompile(`(?i)^heading([1-6])$`)

type docxExtractor struct{}

func (docxExtractor) Key() string {
	return ExtractorKeyDOCX
}

func (docxExtractor) Version() string {
	return ExtractorVersionDOCX
}

func (docxExtractor) Supports(object KnowledgeObject) bool {
	return object.FileClass == storagecatalog.FileClassOfficeDocument && knowledgeObjectExtension(object) == ".docx"
}

func (docxExtractor) RequiresContent(KnowledgeObject) bool {
	return false
}

func (extractor docxExtractor) Extract(input ExtractionInput) (ExtractionResult, error) {
	sourcePath := strings.TrimSpace(input.Object.SourcePath)
	if sourcePath == "" {
		return metadataOnlyExtraction(input.Object, ExtractionStatusSourceUnavailable, "source_path is required for DOCX extraction"), nil
	}
	reader, err := zip.OpenReader(sourcePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
			return metadataOnlyExtraction(input.Object, ExtractionStatusSourceUnavailable, err.Error()), nil
		}
		return ExtractionResult{}, fmt.Errorf("open docx: %w", err)
	}
	defer reader.Close()

	metadata := map[string]any{
		"file_class": input.Object.FileClass,
		"format":     "docx",
	}
	for key, value := range docxCoreProperties(&reader.Reader) {
		metadata[key] = value
	}
	metadata["embedded_media_count"] = docxEmbeddedMediaCount(&reader.Reader)

	rels := docxRelationships(&reader.Reader, "word/_rels/document.xml.rels")
	blocks, links, warnings := docxTextBlocks(&reader.Reader, "word/document.xml", rels)
	for _, part := range []struct {
		name  string
		label string
	}{
		{name: "word/footnotes.xml", label: "Footnotes"},
		{name: "word/endnotes.xml", label: "Endnotes"},
		{name: "word/comments.xml", label: "Comments"},
	} {
		partBlocks, partLinks, partWarnings := docxTextBlocks(&reader.Reader, part.name, nil)
		warnings = append(warnings, partWarnings...)
		if len(partBlocks) == 0 {
			continue
		}
		blocks = append(blocks, docxBlock{Text: part.label, HeadingLevel: 2})
		blocks = append(blocks, partBlocks...)
		links = append(links, partLinks...)
	}
	if len(blocks) == 0 {
		return attachDOCXAbsoluteTime(officeMetadataOnly(input.Object, extractor.Key(), extractor.Version(), ExtractionStatusNoEmbeddedText, metadata, append(warnings, "docx_no_text")), metadata), nil
	}
	document := docxDocumentFromBlocks(blocks)
	document.Warnings = warnings
	document.Links = append(document.Links, links...)
	result, err := extractionResultFromDocument(input, extractor, ExtractionStatusExtracted, TextSourceEmbeddedText, document, metadata)
	return attachDOCXAbsoluteTime(result, metadata), err
}

func attachDOCXAbsoluteTime(result ExtractionResult, metadata map[string]any) ExtractionResult {
	candidates := []AbsoluteTimeCandidate{}
	for _, field := range []struct {
		key   string
		kind  string
		basis string
	}{
		{key: "modified", kind: AbsoluteTimeKindModified, basis: AbsoluteTimeBasisEmbeddedModifiedAt},
		{key: "created", kind: AbsoluteTimeKindCreated, basis: AbsoluteTimeBasisEmbeddedCreatedAt},
	} {
		raw, _ := metadata[field.key].(string)
		raw = strings.TrimSpace(raw)
		if raw != "" {
			candidates = append(candidates, AbsoluteTimeCandidate{Kind: field.kind, Basis: field.basis, RawValue: raw})
		}
	}
	return attachExtractionAbsoluteTime(result, candidates)
}

type docxBlock struct {
	Text         string
	HeadingLevel int
	Links        []docxBlockLink
}

type docxBlockLink struct {
	Target string
	Text   string
	Start  int
	End    int
}

type docxParagraph struct {
	Text          strings.Builder
	Style         string
	Links         []docxBlockLink
	activeLink    *docxActiveLink
	insideText    bool
	insideHyper   int
	insideFldText bool
}

type docxActiveLink struct {
	Target string
	Start  int
	Text   strings.Builder
}

func docxDocumentFromBlocks(blocks []docxBlock) TextDocument {
	var builder strings.Builder
	headings := []TextHeading{}
	links := []ExtractedLink{}
	stack := map[int]string{}
	for _, block := range blocks {
		text := strings.TrimSpace(block.Text)
		if text == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteString("\n\n")
		}
		start := builder.Len()
		linkOffset := 0
		if block.HeadingLevel > 0 {
			if block.HeadingLevel > 6 {
				block.HeadingLevel = 6
			}
			prefix := strings.Repeat("#", block.HeadingLevel) + " "
			linkOffset = len(prefix)
			builder.WriteString(prefix)
			builder.WriteString(text)
			for existingLevel := block.HeadingLevel; existingLevel <= 6; existingLevel++ {
				delete(stack, existingLevel)
			}
			stack[block.HeadingLevel] = text
			headings = append(headings, TextHeading{
				Level:          block.HeadingLevel,
				Text:           text,
				StructuralPath: structuralPathFromHeadingStack(stack),
				StartOffset:    start,
				EndOffset:      builder.Len(),
			})
		} else {
			builder.WriteString(text)
		}
		for _, link := range block.Links {
			target := strings.TrimSpace(link.Target)
			if target == "" {
				continue
			}
			links = append(links, ExtractedLink{
				Kind:             LinkKindURL,
				RawTarget:        target,
				NormalizedTarget: normalizeLinkTarget(target),
				LinkText:         strings.TrimSpace(link.Text),
				StartOffset:      start + linkOffset + link.Start,
				EndOffset:        start + linkOffset + link.End,
			})
		}
	}
	return TextDocument{
		Text:     normalizeTextNewlines(builder.String()),
		Headings: headings,
		Links:    links,
	}
}

func docxTextBlocks(reader *zip.Reader, name string, rels map[string]string) ([]docxBlock, []ExtractedLink, []string) {
	payload, ok, err := readDOCXZipFile(reader, name)
	if err != nil {
		return nil, nil, []string{"docx_part_read_failed: " + name + ": " + err.Error()}
	}
	if !ok {
		return nil, nil, nil
	}
	decoder := xml.NewDecoder(strings.NewReader(string(payload)))
	blocks := []docxBlock{}
	standaloneLinks := []ExtractedLink{}
	var paragraph *docxParagraph
	tableDepth := 0
	rowDepth := 0
	cellDepth := 0
	cellParagraphs := []string{}
	rowCells := []string{}

	flushParagraph := func() {
		if paragraph == nil {
			return
		}
		text := normalizeDOCXBlockText(paragraph.Text.String())
		if text != "" {
			block := docxBlock{
				Text:         text,
				HeadingLevel: docxHeadingLevel(paragraph.Style),
				Links:        paragraph.Links,
			}
			if tableDepth > 0 || cellDepth > 0 {
				cellParagraphs = append(cellParagraphs, block.Text)
			} else {
				blocks = append(blocks, block)
			}
		}
		paragraph = nil
	}

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return blocks, standaloneLinks, []string{"docx_xml_parse_failed: " + name + ": " + err.Error()}
		}
		switch typed := token.(type) {
		case xml.StartElement:
			switch typed.Name.Local {
			case "tbl":
				tableDepth++
			case "tr":
				rowDepth++
				if rowDepth == 1 {
					rowCells = nil
				}
			case "tc":
				cellDepth++
				if cellDepth == 1 {
					cellParagraphs = nil
				}
			case "p":
				paragraph = &docxParagraph{}
			case "pStyle":
				if paragraph != nil {
					paragraph.Style = xmlAttr(typed.Attr, "val")
				}
			case "hyperlink":
				if paragraph != nil {
					target := rels[xmlAttr(typed.Attr, "id")]
					if target == "" {
						target = "#" + xmlAttr(typed.Attr, "anchor")
					}
					if strings.TrimSpace(target) != "#" {
						paragraph.activeLink = &docxActiveLink{
							Target: target,
							Start:  paragraph.Text.Len(),
						}
						paragraph.insideHyper++
					}
				}
			case "t":
				if paragraph != nil {
					paragraph.insideText = true
				}
			case "instrText":
				if paragraph != nil {
					paragraph.insideFldText = true
				}
			case "tab", "br":
				if paragraph != nil {
					docxParagraphWrite(paragraph, " ")
				}
			}
		case xml.EndElement:
			switch typed.Name.Local {
			case "p":
				flushParagraph()
			case "hyperlink":
				if paragraph != nil && paragraph.activeLink != nil {
					linkText := normalizeDOCXBlockText(paragraph.activeLink.Text.String())
					paragraph.Links = append(paragraph.Links, docxBlockLink{
						Target: paragraph.activeLink.Target,
						Text:   linkText,
						Start:  paragraph.activeLink.Start,
						End:    paragraph.Text.Len(),
					})
					paragraph.activeLink = nil
					if paragraph.insideHyper > 0 {
						paragraph.insideHyper--
					}
				}
			case "t":
				if paragraph != nil {
					paragraph.insideText = false
				}
			case "instrText":
				if paragraph != nil {
					paragraph.insideFldText = false
				}
			case "tc":
				if cellDepth > 0 {
					cellDepth--
				}
				cellText := strings.TrimSpace(strings.Join(cellParagraphs, " / "))
				if cellText != "" {
					rowCells = append(rowCells, cellText)
				}
				cellParagraphs = nil
			case "tr":
				if rowDepth > 0 {
					rowDepth--
				}
				rowText := strings.TrimSpace(strings.Join(rowCells, " | "))
				if rowText != "" {
					blocks = append(blocks, docxBlock{Text: rowText})
				}
				rowCells = nil
			case "tbl":
				if tableDepth > 0 {
					tableDepth--
				}
			}
		case xml.CharData:
			if paragraph != nil && (paragraph.insideText || paragraph.insideFldText) {
				docxParagraphWrite(paragraph, string([]byte(typed)))
			}
		}
	}
	return blocks, standaloneLinks, nil
}

func docxParagraphWrite(paragraph *docxParagraph, text string) {
	paragraph.Text.WriteString(text)
	if paragraph.activeLink != nil {
		paragraph.activeLink.Text.WriteString(text)
	}
}

func docxHeadingLevel(style string) int {
	style = strings.ToLower(strings.TrimSpace(style))
	style = strings.NewReplacer(" ", "", "-", "", "_", "").Replace(style)
	match := docxHeadingStylePattern.FindStringSubmatch(style)
	if len(match) != 2 {
		return 0
	}
	level, _ := strconv.Atoi(match[1])
	return level
}

func normalizeDOCXBlockText(text string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(normalizeTextNewlines(text)), " "))
}

func docxEmbeddedMediaCount(reader *zip.Reader) int {
	count := 0
	for _, file := range reader.File {
		name := strings.ToLower(file.Name)
		if strings.HasPrefix(name, "word/media/") && !strings.HasSuffix(name, "/") {
			count++
		}
	}
	return count
}

func docxCoreProperties(reader *zip.Reader) map[string]any {
	payload, ok, err := readDOCXZipFile(reader, "docProps/core.xml")
	if err != nil || !ok {
		return nil
	}
	wanted := map[string]string{
		"title":          "title",
		"subject":        "subject",
		"creator":        "creator",
		"keywords":       "keywords",
		"description":    "description",
		"created":        "created",
		"modified":       "modified",
		"lastModifiedBy": "last_modified_by",
		"category":       "category",
	}
	values := map[string]string{}
	decoder := xml.NewDecoder(strings.NewReader(string(payload)))
	current := ""
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return valuesAny(values)
		}
		switch typed := token.(type) {
		case xml.StartElement:
			current = wanted[typed.Name.Local]
		case xml.EndElement:
			current = ""
		case xml.CharData:
			if current != "" {
				values[current] = strings.TrimSpace(values[current] + string([]byte(typed)))
			}
		}
	}
	return valuesAny(values)
}

func valuesAny(values map[string]string) map[string]any {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]any, len(values))
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if value := strings.TrimSpace(values[key]); value != "" {
			out[key] = value
		}
	}
	return out
}

func docxRelationships(reader *zip.Reader, name string) map[string]string {
	payload, ok, err := readDOCXZipFile(reader, name)
	if err != nil || !ok {
		return nil
	}
	type relationship struct {
		ID         string `xml:"Id,attr"`
		Type       string `xml:"Type,attr"`
		Target     string `xml:"Target,attr"`
		TargetMode string `xml:"TargetMode,attr"`
	}
	var decoded struct {
		Relationships []relationship `xml:"Relationship"`
	}
	if err := xml.Unmarshal(payload, &decoded); err != nil {
		return nil
	}
	out := map[string]string{}
	for _, rel := range decoded.Relationships {
		if strings.TrimSpace(rel.ID) == "" || strings.TrimSpace(rel.Target) == "" {
			continue
		}
		if strings.Contains(strings.ToLower(rel.Type), "/hyperlink") || strings.EqualFold(rel.TargetMode, "External") {
			out[rel.ID] = rel.Target
		}
	}
	return out
}

func readDOCXZipFile(reader *zip.Reader, name string) ([]byte, bool, error) {
	cleanName := filepath.ToSlash(strings.TrimPrefix(name, "/"))
	for _, file := range reader.File {
		if filepath.ToSlash(file.Name) != cleanName {
			continue
		}
		handle, err := file.Open()
		if err != nil {
			return nil, true, err
		}
		defer handle.Close()
		payload, err := io.ReadAll(handle)
		return payload, true, err
	}
	return nil, false, nil
}

func xmlAttr(attrs []xml.Attr, local string) string {
	for _, attr := range attrs {
		if attr.Name.Local == local {
			return strings.TrimSpace(attr.Value)
		}
	}
	return ""
}
