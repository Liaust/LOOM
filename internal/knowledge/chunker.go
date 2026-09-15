package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	defaultTextChunkTargetCharacters = 5000
	defaultTextChunkMaxCharacters    = 8000
)

type ChunkerOptions struct {
	TargetCharacters int
	MaxCharacters    int
}

type TextChunkInput struct {
	Index              int      `json:"index"`
	Text               string   `json:"text"`
	StructuralPath     string   `json:"structural_path,omitempty"`
	StartOffset        int      `json:"start_offset"`
	EndOffset          int      `json:"end_offset"`
	TokenCountEstimate int      `json:"token_count_estimate"`
	ChunkHash          string   `json:"chunk_hash"`
	TextSource         string   `json:"text_source,omitempty"`
	ExtractorKey       string   `json:"extractor_key,omitempty"`
	ExtractorVersion   string   `json:"extractor_version,omitempty"`
	ExtractionStatus   string   `json:"extraction_status,omitempty"`
	SectionIndex       int      `json:"section_index,omitempty"`
	ArtifactIDs        []string `json:"artifact_ids,omitempty"`
	SourceLocators     []string `json:"source_locators,omitempty"`
	SourceKinds        []string `json:"source_kinds,omitempty"`
}

type textParagraph struct {
	Text           string
	StartOffset    int
	EndOffset      int
	StructuralPath string
}

func ChunkTextDocument(document TextDocument, options ChunkerOptions) []TextChunkInput {
	return ChunkTextDocumentWithProvenance(document, options, ChunkProvenance{})
}

type ChunkProvenance struct {
	TextSource       string
	ExtractorKey     string
	ExtractorVersion string
	ExtractionStatus string
	SectionIndex     int
}

func ChunkTextDocumentWithProvenance(document TextDocument, options ChunkerOptions, provenance ChunkProvenance) []TextChunkInput {
	targetCharacters := options.TargetCharacters
	if targetCharacters <= 0 {
		targetCharacters = defaultTextChunkTargetCharacters
	}
	maxCharacters := options.MaxCharacters
	if maxCharacters <= 0 {
		maxCharacters = defaultTextChunkMaxCharacters
	}
	if maxCharacters < targetCharacters {
		maxCharacters = targetCharacters
	}

	paragraphs := splitTextParagraphs(document.Text)
	if len(paragraphs) == 0 {
		trimmed := strings.TrimSpace(document.Text)
		if trimmed == "" {
			return nil
		}
		return []TextChunkInput{newTextChunkInput(1, trimmed, 0, len(document.Text), "", provenance)}
	}

	chunks := []TextChunkInput{}
	var current strings.Builder
	currentStart := -1
	currentEnd := -1
	currentPath := ""

	flush := func() {
		body := strings.TrimSpace(current.String())
		if body == "" {
			current.Reset()
			currentStart = -1
			currentEnd = -1
			return
		}
		chunks = append(chunks, newTextChunkInput(len(chunks)+1, body, currentStart, currentEnd, currentPath, provenance))
		current.Reset()
		currentStart = -1
		currentEnd = -1
	}

	activePath := ""
	for _, paragraph := range paragraphs {
		if level, title, ok := parseMarkdownHeadingLine(firstLine(paragraph.Text)); ok {
			if current.Len() > 0 {
				flush()
			}
			activePath = headingPathAtOffset(document.Headings, level, title, paragraph.StartOffset)
			paragraph.StructuralPath = activePath
		} else {
			paragraph.StructuralPath = activePath
		}
		if len(paragraph.Text) > maxCharacters {
			flush()
			for _, chunk := range splitLongTextParagraph(paragraph, targetCharacters) {
				chunk.Index = len(chunks) + 1
				chunk.ChunkHash = hashChunkText(chunk.Index, chunk.Text)
				chunk = applyChunkProvenance(chunk, provenance)
				chunks = append(chunks, chunk)
			}
			continue
		}
		if current.Len() > 0 && current.Len()+len(paragraph.Text)+2 > targetCharacters {
			flush()
		}
		if currentStart < 0 {
			currentStart = paragraph.StartOffset
			currentPath = paragraph.StructuralPath
		}
		if current.Len() > 0 {
			current.WriteString("\n\n")
		}
		current.WriteString(paragraph.Text)
		currentEnd = paragraph.EndOffset
	}
	flush()
	return chunks
}

func splitTextParagraphs(text string) []textParagraph {
	paragraphs := []textParagraph{}
	start := -1
	var builder strings.Builder
	lineStart := 0
	for lineStart <= len(text) {
		lineEnd := strings.IndexByte(text[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(text)
		} else {
			lineEnd += lineStart
		}
		line := text[lineStart:lineEnd]
		if strings.TrimSpace(line) == "" {
			if builder.Len() > 0 {
				paragraphs = append(paragraphs, textParagraph{
					Text:        strings.TrimSpace(builder.String()),
					StartOffset: start,
					EndOffset:   lineStart,
				})
				builder.Reset()
				start = -1
			}
		} else {
			if start < 0 {
				start = lineStart
			}
			if builder.Len() > 0 {
				builder.WriteByte('\n')
			}
			builder.WriteString(line)
		}
		if lineEnd == len(text) {
			break
		}
		lineStart = lineEnd + 1
	}
	if builder.Len() > 0 {
		paragraphs = append(paragraphs, textParagraph{
			Text:        strings.TrimSpace(builder.String()),
			StartOffset: start,
			EndOffset:   len(text),
		})
	}
	return paragraphs
}

func splitLongTextParagraph(paragraph textParagraph, targetCharacters int) []TextChunkInput {
	chunks := []TextChunkInput{}
	start := 0
	for start < len(paragraph.Text) {
		end := start + targetCharacters
		if end > len(paragraph.Text) {
			end = len(paragraph.Text)
		}
		if end < len(paragraph.Text) {
			if boundary := strings.LastIndexAny(paragraph.Text[start:end], " \n\t"); boundary > targetCharacters/2 {
				end = start + boundary
			}
		}
		body := strings.TrimSpace(paragraph.Text[start:end])
		if body != "" {
			chunks = append(chunks, newTextChunkInput(0, body, paragraph.StartOffset+start, paragraph.StartOffset+end, paragraph.StructuralPath, ChunkProvenance{}))
		}
		start = end
	}
	return chunks
}

func newTextChunkInput(index int, text string, startOffset, endOffset int, structuralPath string, provenance ChunkProvenance) TextChunkInput {
	chunk := TextChunkInput{
		Index:              index,
		Text:               text,
		StructuralPath:     structuralPath,
		StartOffset:        startOffset,
		EndOffset:          endOffset,
		TokenCountEstimate: len(strings.Fields(text)),
		ChunkHash:          hashChunkText(index, text),
	}
	return applyChunkProvenance(chunk, provenance)
}

func applyChunkProvenance(chunk TextChunkInput, provenance ChunkProvenance) TextChunkInput {
	chunk.TextSource = strings.TrimSpace(provenance.TextSource)
	chunk.ExtractorKey = strings.TrimSpace(provenance.ExtractorKey)
	chunk.ExtractorVersion = strings.TrimSpace(provenance.ExtractorVersion)
	chunk.ExtractionStatus = strings.TrimSpace(provenance.ExtractionStatus)
	chunk.SectionIndex = provenance.SectionIndex
	return chunk
}

func hashChunkText(index int, text string) string {
	hash := sha256.Sum256([]byte(fmt.Sprintf("%d\n%s", index, text)))
	return "sha256:" + hex.EncodeToString(hash[:])
}

func firstLine(text string) string {
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		return text[:idx]
	}
	return text
}

func headingPathAtOffset(headings []TextHeading, level int, title string, offset int) string {
	for _, heading := range headings {
		if heading.Level == level && heading.Text == title && heading.StartOffset == offset {
			return heading.StructuralPath
		}
	}
	return strings.TrimSpace(title)
}
