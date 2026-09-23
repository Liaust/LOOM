package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	DefaultEmbeddingPassageTargetCharacters = 2800
	DefaultEmbeddingPassageMaxCharacters    = 3600
)

type EmbeddingPassageOptions struct {
	TargetCharacters int
	MaxCharacters    int
}

type EmbeddingPassage struct {
	KnowledgeChunkID         string `json:"knowledge_chunk_id"`
	KnowledgeObjectID        string `json:"knowledge_object_id"`
	KnowledgeObjectVersionID string `json:"knowledge_object_version_id,omitempty"`
	ChunkIndex               int    `json:"chunk_index"`
	PassageIndex             int    `json:"passage_index"`
	Text                     string `json:"text"`
	StructuralPath           string `json:"structural_path,omitempty"`
	StartOffset              *int   `json:"start_offset,omitempty"`
	EndOffset                *int   `json:"end_offset,omitempty"`
	TokenCountEstimate       int    `json:"token_count_estimate"`
	ChunkHash                string `json:"chunk_hash"`
	PassageHash              string `json:"passage_hash"`
}

func PrepareEmbeddingPassages(chunks []KnowledgeChunk, options EmbeddingPassageOptions) ([]EmbeddingPassage, error) {
	targetCharacters := options.TargetCharacters
	if targetCharacters <= 0 {
		targetCharacters = DefaultEmbeddingPassageTargetCharacters
	}
	maxCharacters := options.MaxCharacters
	if maxCharacters <= 0 {
		maxCharacters = DefaultEmbeddingPassageMaxCharacters
	}
	if maxCharacters < targetCharacters {
		maxCharacters = targetCharacters
	}

	passages := []EmbeddingPassage{}
	for _, chunk := range chunks {
		if strings.TrimSpace(chunk.ChunkText) == "" {
			continue
		}
		if chunk.KnowledgeChunkID == "" {
			return nil, fmt.Errorf("%w: knowledge_chunk_id is required for embedding passage mapping", ErrInvalid)
		}
		if chunk.ChunkHash == "" {
			return nil, fmt.Errorf("%w: chunk_hash is required for embedding passage mapping", ErrInvalid)
		}
		segments := splitEmbeddingPassageText(chunk.ChunkText, targetCharacters, maxCharacters)
		if len(segments) == 0 {
			continue
		}
		for index, segment := range segments {
			passageIndex := index + 1
			startOffset, endOffset := embeddingPassageOffsets(chunk, segment.Start, segment.End)
			passage := EmbeddingPassage{
				KnowledgeChunkID:   chunk.KnowledgeChunkID,
				KnowledgeObjectID:  chunk.KnowledgeObjectID,
				ChunkIndex:         chunk.ChunkIndex,
				PassageIndex:       passageIndex,
				Text:               segment.Text,
				StructuralPath:     chunk.StructuralPath,
				StartOffset:        startOffset,
				EndOffset:          endOffset,
				TokenCountEstimate: len(strings.Fields(segment.Text)),
				ChunkHash:          chunk.ChunkHash,
				PassageHash:        hashEmbeddingPassage(chunk.ChunkHash, passageIndex, segment.Text),
			}
			if chunk.KnowledgeObjectVersionID != nil {
				passage.KnowledgeObjectVersionID = *chunk.KnowledgeObjectVersionID
			}
			passages = append(passages, passage)
		}
	}
	return passages, nil
}

func EmbeddingRuntimeInputsFromPassages(passages []EmbeddingPassage) ([]string, error) {
	inputs := make([]string, 0, len(passages))
	for _, passage := range passages {
		text := strings.TrimSpace(passage.Text)
		if text == "" {
			return nil, fmt.Errorf("%w: embedding passage text is required", ErrInvalid)
		}
		inputs = append(inputs, text)
	}
	return inputs, nil
}

// Character counts cannot predict model tokens for equations, tables or scripts.
// Only an explicit context-length refusal narrows the passages. Every byte of
// non-whitespace source text remains represented; other failures are not retried.
func embedChunkPassages(ctx context.Context, runtime EmbeddingRuntime, model string, chunk KnowledgeChunk) (EmbeddingRuntimeResponse, []EmbeddingPassage, error) {
	options := EmbeddingPassageOptions{TargetCharacters: DefaultEmbeddingPassageTargetCharacters, MaxCharacters: DefaultEmbeddingPassageMaxCharacters}
	for {
		if err := ctx.Err(); err != nil {
			return EmbeddingRuntimeResponse{}, nil, err
		}
		passages, err := PrepareEmbeddingPassages([]KnowledgeChunk{chunk}, options)
		if err != nil {
			return EmbeddingRuntimeResponse{}, nil, err
		}
		inputs, err := EmbeddingRuntimeInputsFromPassages(passages)
		if err != nil {
			return EmbeddingRuntimeResponse{}, nil, err
		}
		response, err := runtime.Embed(ctx, EmbeddingRuntimeRequest{Model: model, Inputs: inputs, Truncate: false})
		var runtimeErr *EmbeddingRuntimeError
		if err == nil || !errors.As(err, &runtimeErr) || runtimeErr.Kind != EmbeddingRuntimeErrorContextLength || options.MaxCharacters <= 64 {
			return response, passages, err
		}
		options.TargetCharacters = max(64, options.TargetCharacters/2)
		options.MaxCharacters = max(64, options.MaxCharacters/2)
	}
}

type embeddingTextSegment struct {
	Text  string
	Start int
	End   int
}

func splitEmbeddingPassageText(text string, targetCharacters int, maxCharacters int) []embeddingTextSegment {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if len(text) <= maxCharacters {
		if segment, ok := newEmbeddingTextSegment(text, 0, len(text)); ok {
			return []embeddingTextSegment{segment}
		}
		return nil
	}
	segments := []embeddingTextSegment{}
	start := 0
	for start < len(text) {
		end := start + targetCharacters
		if end > len(text) {
			end = len(text)
		}
		if end < len(text) {
			if boundary := strings.LastIndexAny(text[start:end], " \n\t"); boundary > targetCharacters/2 {
				end = start + boundary
			}
		}
		if end <= start {
			end = start + targetCharacters
			if end > len(text) {
				end = len(text)
			}
		}
		// Offsets are bytes, but JSON/model inputs must not split UTF-8 runes.
		for end < len(text) && end > start && !utf8.RuneStart(text[end]) {
			end--
		}
		if end == start {
			_, size := utf8.DecodeRuneInString(text[start:])
			end = start + size
		}
		if segment, ok := newEmbeddingTextSegment(text, start, end); ok {
			segments = append(segments, segment)
		}
		start = end
	}
	return segments
}

func newEmbeddingTextSegment(text string, start int, end int) (embeddingTextSegment, bool) {
	raw := text[start:end]
	trimLeft := len(raw) - len(strings.TrimLeft(raw, " \n\t"))
	trimRight := len(strings.TrimRight(raw, " \n\t"))
	if trimRight <= trimLeft {
		return embeddingTextSegment{}, false
	}
	return embeddingTextSegment{
		Text:  raw[trimLeft:trimRight],
		Start: start + trimLeft,
		End:   start + trimRight,
	}, true
}

func embeddingPassageOffsets(chunk KnowledgeChunk, relativeStart int, relativeEnd int) (*int, *int) {
	if chunk.StartOffset == nil {
		return nil, nil
	}
	start := *chunk.StartOffset + relativeStart
	end := *chunk.StartOffset + relativeEnd
	if chunk.EndOffset != nil && end > *chunk.EndOffset {
		end = *chunk.EndOffset
	}
	return &start, &end
}

func hashEmbeddingPassage(chunkHash string, passageIndex int, text string) string {
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s\n%d\n%s", chunkHash, passageIndex, text)))
	return "sha256:" + hex.EncodeToString(hash[:])
}
