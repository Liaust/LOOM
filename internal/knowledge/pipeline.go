package knowledge

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"loom.local/loom/internal/storagecatalog"
)

const (
	KnowledgeObjectPipelineMarkdownText        = "markdown_text_v1"
	KnowledgeObjectPipelineNotesFileExtraction = "notes_file_extraction_v1"
	KnowledgeMarkdownTextPipelineVersion       = "v0.8"
	KnowledgeMarkdownTextExtractorVersion      = "markdown_text_extractor_v1"
	KnowledgeMarkdownTextChunkerVersion        = "heading_paragraph_chunk_v1"
	KnowledgeFileExtractionPipelineVersion     = "v0.8.5"
)

type TextPipelineInput struct {
	ObjectRef             string          `json:"object_ref,omitempty"`
	Object                KnowledgeObject `json:"object,omitempty"`
	Content               string          `json:"content,omitempty"`
	MaxBytes              int64           `json:"max_bytes,omitempty"`
	MaxExtractedTextBytes int64           `json:"max_extracted_text_bytes,omitempty"`
	MaxChunks             int             `json:"max_chunks,omitempty"`
	Chunker               ChunkerOptions  `json:"chunker,omitempty"`
}

type TextPipelineExtraction struct {
	Document   TextDocument     `json:"document"`
	Chunks     []TextChunkInput `json:"chunks"`
	Extraction ExtractionResult `json:"extraction"`
}

type TextPipelineResult struct {
	Object   KnowledgeObject        `json:"object"`
	Version  KnowledgeObjectVersion `json:"version"`
	Chunks   []KnowledgeChunk       `json:"chunks"`
	Links    []ObjectLink           `json:"links"`
	Statuses []PipelineStatus       `json:"statuses"`
}

func (s *Service) BuildTextPipelineExtraction(object KnowledgeObject, content string, options ChunkerOptions) (TextPipelineExtraction, error) {
	if !isMarkdownTextObject(object) {
		return TextPipelineExtraction{}, fmt.Errorf("%w: knowledge object file class %q is not supported by markdown text pipeline", ErrInvalid, object.FileClass)
	}
	extraction, err := DefaultExtractionRegistry().Extract(ExtractionInput{
		Object:  object,
		Content: content,
		Chunker: options,
	})
	if err != nil {
		return TextPipelineExtraction{}, err
	}
	return TextPipelineExtraction{
		Document:   extraction.Document,
		Chunks:     extraction.Chunks,
		Extraction: extraction,
	}, nil
}

func (s *Service) ProcessTextObject(ctx context.Context, input TextPipelineInput) (TextPipelineResult, error) {
	if s == nil || s.store.db == nil {
		return TextPipelineResult{}, fmt.Errorf("knowledge store is not configured")
	}
	object := input.Object
	if object.KnowledgeObjectID == "" {
		if strings.TrimSpace(input.ObjectRef) == "" {
			return TextPipelineResult{}, fmt.Errorf("%w: object_ref or object is required", ErrInvalid)
		}
		var err error
		object, err = s.store.GetKnowledgeObject(ctx, input.ObjectRef)
		if err != nil {
			return TextPipelineResult{}, err
		}
	}
	content := input.Content
	if content == "" || isSyncedKnowledgeObject(object) {
		var err error
		content, err = s.readKnowledgeObjectSource(ctx, object, input.MaxBytes)
		if err != nil {
			return TextPipelineResult{}, err
		}
	}
	extraction, err := s.BuildTextPipelineExtraction(object, content, input.Chunker)
	if err != nil {
		return TextPipelineResult{}, err
	}
	if input.MaxChunks > 0 && len(extraction.Chunks) > input.MaxChunks {
		return TextPipelineResult{}, fmt.Errorf("%w: text chunk count %d exceeds max_chunks %d", ErrInvalid, len(extraction.Chunks), input.MaxChunks)
	}
	return s.applyTextPipeline(ctx, object, extraction)
}

func (s *Service) ProcessExtractionObject(ctx context.Context, input TextPipelineInput) (TextPipelineResult, error) {
	if s == nil || s.store.db == nil {
		return TextPipelineResult{}, fmt.Errorf("knowledge store is not configured")
	}
	object := input.Object
	if object.KnowledgeObjectID == "" {
		if strings.TrimSpace(input.ObjectRef) == "" {
			return TextPipelineResult{}, fmt.Errorf("%w: object_ref or object is required", ErrInvalid)
		}
		var err error
		object, err = s.store.GetKnowledgeObject(ctx, input.ObjectRef)
		if err != nil {
			return TextPipelineResult{}, err
		}
	}
	registry := DefaultExtractionRegistry()
	extractor, ok := registry.ExtractorFor(object)
	if !ok {
		return s.applyExtractionPipeline(ctx, object, metadataOnlyExtraction(object, ExtractionStatusUnsupportedBodyExtraction, "body extraction is not supported for this file class"))
	}
	content := input.Content
	if extractor.RequiresContent(object) && (content == "" || isSyncedKnowledgeObject(object)) {
		var err error
		content, err = s.readKnowledgeObjectSource(ctx, object, input.MaxBytes)
		if err != nil {
			if metadata, ok := extractionMetadataOnlyForReadError(object, err); ok {
				return s.applyExtractionPipeline(ctx, object, metadata)
			}
			return TextPipelineResult{}, err
		}
	}
	extractionObject := object
	if !extractor.RequiresContent(object) {
		prepared, cleanup, err := s.preparePathBackedExtractionObject(ctx, object, input.MaxBytes)
		if err != nil {
			if metadata, ok := extractionMetadataOnlyForReadError(object, err); ok {
				return s.applyExtractionPipeline(ctx, object, metadata)
			}
			return TextPipelineResult{}, err
		}
		defer cleanup()
		extractionObject = prepared
	}
	extraction, err := extractor.Extract(ExtractionInput{
		Object:                extractionObject,
		Content:               content,
		MaxSourceBytes:        input.MaxBytes,
		MaxExtractedTextBytes: input.MaxExtractedTextBytes,
		Chunker:               input.Chunker,
	})
	if err != nil {
		return TextPipelineResult{}, err
	}
	if input.MaxChunks > 0 && len(extraction.Chunks) > input.MaxChunks {
		return TextPipelineResult{}, fmt.Errorf("%w: text chunk count %d exceeds max_chunks %d", ErrInvalid, len(extraction.Chunks), input.MaxChunks)
	}
	return s.applyExtractionPipeline(ctx, object, extraction)
}

// ExtractObject runs a native extractor without publishing chunks, lexical
// documents, or embedding queue work. Unified pipeline stages use this path so
// publication occurs only after every selected extraction stage completes.
func (s *Service) ExtractObject(ctx context.Context, input TextPipelineInput) (ExtractionResult, error) {
	if s == nil || s.store.db == nil {
		return ExtractionResult{}, fmt.Errorf("knowledge store is not configured")
	}
	object := input.Object
	if object.KnowledgeObjectID == "" {
		var err error
		object, err = s.store.GetKnowledgeObject(ctx, input.ObjectRef)
		if err != nil {
			return ExtractionResult{}, err
		}
	}
	extractor, ok := DefaultExtractionRegistry().ExtractorFor(object)
	if !ok {
		return metadataOnlyExtraction(object, ExtractionStatusUnsupportedBodyExtraction, "body extraction is not supported for this file class"), nil
	}
	content := input.Content
	if extractor.RequiresContent(object) && (content == "" || isSyncedKnowledgeObject(object)) {
		var err error
		content, err = s.readKnowledgeObjectSource(ctx, object, input.MaxBytes)
		if err != nil {
			if metadata, ok := extractionMetadataOnlyForReadError(object, err); ok {
				return metadata, nil
			}
			return ExtractionResult{}, err
		}
	}
	if !extractor.RequiresContent(object) {
		prepared, cleanup, err := s.preparePathBackedExtractionObject(ctx, object, input.MaxBytes)
		if err != nil {
			if metadata, ok := extractionMetadataOnlyForReadError(object, err); ok {
				return metadata, nil
			}
			return ExtractionResult{}, err
		}
		defer cleanup()
		object = prepared
	}
	extraction, err := extractor.Extract(ExtractionInput{Object: object, Content: content, MaxSourceBytes: input.MaxBytes, MaxExtractedTextBytes: input.MaxExtractedTextBytes, Chunker: input.Chunker})
	if err != nil {
		return ExtractionResult{}, err
	}
	if input.MaxChunks > 0 && len(extraction.Chunks) > input.MaxChunks {
		return ExtractionResult{}, fmt.Errorf("%w: text chunk count %d exceeds max_chunks %d", ErrInvalid, len(extraction.Chunks), input.MaxChunks)
	}
	return extraction, nil
}

func (s *Service) preparePathBackedExtractionObject(ctx context.Context, object KnowledgeObject, maxBytes int64) (KnowledgeObject, func(), error) {
	cleanup := func() {}
	if isSyncedKnowledgeObject(object) {
		payload, err := s.readSyncedObjectSource(ctx, object, maxBytes)
		if err != nil {
			return object, cleanup, err
		}
		path, cleanup, err := writeExtractionTempFile(object, payload)
		if err == nil {
			object.SourcePath = path
		}
		return object, cleanup, err
	}
	sourcePath := strings.TrimSpace(object.SourcePath)
	if sourcePath == "" {
		if object.StorageEntryID == nil || strings.TrimSpace(*object.StorageEntryID) == "" || s == nil || s.store.db == nil {
			return object, cleanup, fmt.Errorf("%w: source_path is required for file extraction", ErrInvalid)
		}
		return s.materializeRetainedSourceForExtraction(ctx, object, maxBytes)
	}
	info, err := os.Stat(sourcePath)
	if err == nil {
		if info.IsDir() {
			return object, cleanup, fmt.Errorf("source file is a directory: %s", sourcePath)
		}
		if maxBytes > 0 && info.Size() > maxBytes {
			return object, cleanup, fmt.Errorf("source file size %d exceeds max_bytes %d", info.Size(), maxBytes)
		}
		return object, cleanup, nil
	}
	if !isSourceUnavailableReadError(err) || object.StorageEntryID == nil || strings.TrimSpace(*object.StorageEntryID) == "" || s == nil || s.store.db == nil {
		return object, cleanup, fmt.Errorf("stat source file: %w", err)
	}
	return s.materializeRetainedSourceForExtraction(ctx, object, maxBytes)
}

func (s *Service) materializeRetainedSourceForExtraction(ctx context.Context, object KnowledgeObject, maxBytes int64) (KnowledgeObject, func(), error) {
	storageEntryID := ""
	if object.StorageEntryID != nil {
		storageEntryID = strings.TrimSpace(*object.StorageEntryID)
	}
	if storageEntryID == "" || s == nil || s.store.db == nil {
		return object, func() {}, fmt.Errorf("source_path is unavailable and no storage entry is available")
	}
	detail, err := storagecatalog.NewService(s.store.db).InspectEntry(ctx, storageEntryID)
	if err != nil {
		return object, func() {}, err
	}
	var lastErr error
	for _, ref := range storagecatalog.PreferredReadablePhysicalRefs(detail.PhysicalRefs) {
		switch {
		case ref.RefKind == storagecatalog.PhysicalRefKindBackupArtifact:
			payload, err := readBackupArtifactBytes(ref.URI, storagecatalog.BackupArtifactContentMember(ref), maxBytes)
			if err != nil {
				lastErr = err
				continue
			}
			path, cleanup, err := writeExtractionTempFile(object, payload)
			if err != nil {
				return object, func() {}, err
			}
			object.SourcePath = path
			return object, cleanup, nil
		case storagecatalog.IsFilesystemPhysicalRefKind(ref.RefKind):
			prepared, cleanup, err := prepareLocalPhysicalRefForExtraction(object, ref, maxBytes)
			if err != nil {
				lastErr = err
				continue
			}
			return prepared, cleanup, nil
		}
	}
	if lastErr != nil {
		return object, func() {}, lastErr
	}
	return object, func() {}, fmt.Errorf("no available retained source for storage entry %s", storageEntryID)
}

func (s *Service) readKnowledgeObjectSource(ctx context.Context, object KnowledgeObject, maxBytes int64) (string, error) {
	if isSyncedKnowledgeObject(object) {
		payload, err := s.readSyncedObjectSource(ctx, object, maxBytes)
		if err != nil {
			return "", err
		}
		if !utf8.Valid(payload) {
			return "", fmt.Errorf("%w: synced source is not valid UTF-8", ErrInvalid)
		}
		return normalizeTextNewlines(string(payload)), nil
	}
	content, err := readTextSourceFile(object.SourcePath, maxBytes)
	if err == nil {
		return content, nil
	}
	if !isSourceUnavailableReadError(err) || object.StorageEntryID == nil || strings.TrimSpace(*object.StorageEntryID) == "" || s == nil || s.store.db == nil {
		return "", err
	}
	fallback, fallbackErr := s.readKnowledgeObjectRetainedSource(ctx, *object.StorageEntryID, maxBytes)
	if fallbackErr != nil {
		if isSourceTooLargeReadError(fallbackErr) {
			return "", fallbackErr
		}
		return "", err
	}
	return fallback, nil
}

func (s *Service) readKnowledgeObjectRetainedSource(ctx context.Context, storageEntryID string, maxBytes int64) (string, error) {
	detail, err := storagecatalog.NewService(s.store.db).InspectEntry(ctx, storageEntryID)
	if err != nil {
		return "", err
	}
	var lastErr error
	for _, ref := range storagecatalog.PreferredReadablePhysicalRefs(detail.PhysicalRefs) {
		switch {
		case ref.RefKind == storagecatalog.PhysicalRefKindBackupArtifact:
			content, err := readTextBackupArtifact(ref.URI, storagecatalog.BackupArtifactContentMember(ref), maxBytes)
			if err == nil {
				return content, nil
			}
			lastErr = err
		case storagecatalog.IsFilesystemPhysicalRefKind(ref.RefKind):
			content, err := readTextPhysicalRef(ref, maxBytes)
			if err == nil {
				return content, nil
			}
			lastErr = err
		}
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("no available retained source for storage entry %s", storageEntryID)
}

func isMarkdownTextObject(object KnowledgeObject) bool {
	switch object.FileClass {
	case storagecatalog.FileClassMarkdown, storagecatalog.FileClassText:
		return object.DeletedAt == nil && object.ProcessingState != ProcessingStateDeleted
	default:
		return false
	}
}

func extractionMetadataOnlyForReadError(object KnowledgeObject, err error) (ExtractionResult, bool) {
	if err == nil {
		return ExtractionResult{}, false
	}
	message := strings.ToLower(err.Error())
	switch {
	case isSourceTooLargeReadError(err):
		return metadataOnlyExtraction(object, ExtractionStatusTooLarge, err.Error()), true
	case strings.Contains(message, "source_path is required"):
		return metadataOnlyExtraction(object, ExtractionStatusSourceUnavailable, err.Error()), true
	case errors.Is(err, os.ErrNotExist), errors.Is(err, os.ErrPermission), strings.Contains(message, "stat source file"), strings.Contains(message, "read source file"):
		return metadataOnlyExtraction(object, ExtractionStatusSourceUnavailable, err.Error()), true
	default:
		return ExtractionResult{}, false
	}
}

func isSourceTooLargeReadError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "exceeds max_bytes")
}

func isSourceUnavailableReadError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "source_path is required") ||
		errors.Is(err, os.ErrNotExist) ||
		errors.Is(err, os.ErrPermission) ||
		strings.Contains(message, "stat source file") ||
		strings.Contains(message, "read source file")
}

func readTextSourceFile(path string, maxBytes int64) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("%w: source_path is required for text extraction", ErrInvalid)
	}
	if maxBytes > 0 {
		info, err := os.Stat(path)
		if err != nil {
			return "", fmt.Errorf("stat source file: %w", err)
		}
		if info.Size() > maxBytes {
			return "", fmt.Errorf("source file size %d exceeds max_bytes %d", info.Size(), maxBytes)
		}
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read source file: %w", err)
	}
	if !utf8.Valid(payload) {
		return "", fmt.Errorf("%w: source file is not valid UTF-8", ErrInvalid)
	}
	return normalizeTextNewlines(string(payload)), nil
}

func readTextBackupArtifact(tarPath, member string, maxBytes int64) (string, error) {
	payload, err := readBackupArtifactBytes(tarPath, member, maxBytes)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(payload) {
		return "", fmt.Errorf("%w: source file is not valid UTF-8", ErrInvalid)
	}
	return normalizeTextNewlines(string(payload)), nil
}

func readTextPhysicalRef(ref storagecatalog.PhysicalRef, maxBytes int64) (string, error) {
	if !storagecatalog.IsFilesystemPhysicalRefKind(ref.RefKind) {
		return "", fmt.Errorf("%w: physical ref kind %q is not readable as a local source", ErrInvalid, ref.RefKind)
	}
	return readTextSourceFile(ref.URI, maxBytes)
}

func prepareLocalPhysicalRefForExtraction(object KnowledgeObject, ref storagecatalog.PhysicalRef, maxBytes int64) (KnowledgeObject, func(), error) {
	cleanup := func() {}
	if !storagecatalog.IsFilesystemPhysicalRefKind(ref.RefKind) {
		return object, cleanup, fmt.Errorf("%w: physical ref kind %q is not readable as a local source", ErrInvalid, ref.RefKind)
	}
	sourcePath := strings.TrimSpace(ref.URI)
	if sourcePath == "" {
		return object, cleanup, fmt.Errorf("%w: physical ref uri is required for extraction", ErrInvalid)
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		return object, cleanup, fmt.Errorf("stat source file: %w", err)
	}
	if info.IsDir() {
		return object, cleanup, fmt.Errorf("source file is a directory: %s", sourcePath)
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		return object, cleanup, fmt.Errorf("source file size %d exceeds max_bytes %d", info.Size(), maxBytes)
	}
	object.SourcePath = sourcePath
	return object, cleanup, nil
}

func readBackupArtifactBytes(tarPath, member string, maxBytes int64) ([]byte, error) {
	if strings.TrimSpace(tarPath) == "" {
		return nil, fmt.Errorf("%w: backup artifact uri is required for extraction", ErrInvalid)
	}
	if strings.TrimSpace(member) == "" {
		member = "content"
	}
	file, err := os.Open(tarPath)
	if err != nil {
		return nil, fmt.Errorf("open backup artifact: %w", err)
	}
	defer file.Close()
	reader := tar.NewReader(file)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("backup artifact member %q was not found", member)
		}
		if err != nil {
			return nil, fmt.Errorf("read backup artifact: %w", err)
		}
		if header == nil || header.Name != member {
			continue
		}
		if header.FileInfo().IsDir() {
			return nil, fmt.Errorf("backup artifact member %q is a directory", member)
		}
		if maxBytes > 0 && header.Size > maxBytes {
			return nil, fmt.Errorf("source file size %d exceeds max_bytes %d", header.Size, maxBytes)
		}
		var payload []byte
		if maxBytes > 0 {
			payload, err = io.ReadAll(io.LimitReader(reader, maxBytes+1))
		} else {
			payload, err = io.ReadAll(reader)
		}
		if err != nil {
			return nil, fmt.Errorf("read backup artifact member: %w", err)
		}
		if maxBytes > 0 && int64(len(payload)) > maxBytes {
			return nil, fmt.Errorf("source file size %d exceeds max_bytes %d", len(payload), maxBytes)
		}
		return payload, nil
	}
}

func writeExtractionTempFile(object KnowledgeObject, payload []byte) (string, func(), error) {
	ext := strings.ToLower(filepath.Ext(firstNonEmpty(object.RelativePath, object.SourcePath, object.Title)))
	if ext == "" || strings.ContainsAny(ext, `/\`) {
		ext = ".bin"
	}
	file, err := os.CreateTemp("", "loom-knowledge-source-*"+ext)
	if err != nil {
		return "", func() {}, err
	}
	path := file.Name()
	cleanup := func() { _ = os.Remove(path) }
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return path, cleanup, nil
}

func textPipelineObjectMetadata(existing json.RawMessage, version KnowledgeObjectVersion, extraction TextPipelineExtraction) json.RawMessage {
	if extraction.Extraction.Status == "" {
		extraction.Extraction = extractionResultFromTextPipelineExtraction(extraction)
	}
	return extractionPipelineObjectMetadata(existing, version, extraction.Extraction)
}

func extractionPipelineObjectMetadata(existing json.RawMessage, version KnowledgeObjectVersion, extraction ExtractionResult) json.RawMessage {
	metadata := jsonObject(existing)
	if metadata == nil {
		metadata = map[string]any{}
	}
	frontmatterKeys := []string{}
	for key := range extraction.Document.Frontmatter {
		frontmatterKeys = append(frontmatterKeys, key)
	}
	sort.Strings(frontmatterKeys)
	metadata["extraction"] = map[string]any{
		"schema_version":              KnowledgeExtractionSchemaVersion,
		"pipeline_key":                KnowledgeObjectPipelineNotesFileExtraction,
		"pipeline_version":            KnowledgeFileExtractionPipelineVersion,
		"extractor_key":               extraction.ExtractorKey,
		"extractor_version":           extraction.ExtractorVersion,
		"status":                      extraction.Status,
		"knowledge_object_version_id": version.KnowledgeObjectVersionID,
		"text_section_count":          len(extraction.TextSections),
		"chunk_count":                 len(extraction.Chunks),
		"link_count":                  len(extraction.Links),
		"heading_count":               len(extraction.Document.Headings),
		"frontmatter_keys":            frontmatterKeys,
		"warnings":                    extraction.Warnings,
		"absolute_time_candidates":    extraction.AbsoluteTimeCandidates,
		"absolute_time_warnings":      extraction.AbsoluteTimeWarnings,
		"absolute_time":               extraction.AbsoluteTime,
		"metadata":                    extraction.Metadata,
	}
	metadata["text_pipeline"] = map[string]any{
		"schema_version":              "knowledge.text_pipeline.v0.8",
		"pipeline_key":                KnowledgeObjectPipelineMarkdownText,
		"pipeline_version":            KnowledgeMarkdownTextPipelineVersion,
		"extractor_key":               extraction.ExtractorKey,
		"extractor_version":           extraction.ExtractorVersion,
		"extraction_status":           extraction.Status,
		"chunker_version":             KnowledgeMarkdownTextChunkerVersion,
		"knowledge_object_version_id": version.KnowledgeObjectVersionID,
		"chunk_count":                 len(extraction.Chunks),
		"link_count":                  len(extraction.Links),
		"heading_count":               len(extraction.Document.Headings),
		"frontmatter_keys":            frontmatterKeys,
		"warnings":                    extraction.Warnings,
		"absolute_time_candidates":    extraction.AbsoluteTimeCandidates,
		"absolute_time_warnings":      extraction.AbsoluteTimeWarnings,
		"absolute_time":               extraction.AbsoluteTime,
	}
	payload, err := json.Marshal(metadata)
	if err != nil {
		return existing
	}
	return payload
}

func textPipelineVersionMetadata(extraction TextPipelineExtraction) json.RawMessage {
	if extraction.Extraction.Status == "" {
		extraction.Extraction = extractionResultFromTextPipelineExtraction(extraction)
	}
	return extractionPipelineVersionMetadata(extraction.Extraction)
}

func extractionPipelineVersionMetadata(extraction ExtractionResult) json.RawMessage {
	payload, err := json.Marshal(map[string]any{
		"schema_version":           "knowledge.text_object_version.v0.8.5",
		"pipeline_key":             KnowledgeObjectPipelineNotesFileExtraction,
		"pipeline_version":         KnowledgeFileExtractionPipelineVersion,
		"extractor_key":            extraction.ExtractorKey,
		"extractor_version":        extraction.ExtractorVersion,
		"extraction_status":        extraction.Status,
		"chunker_version":          KnowledgeMarkdownTextChunkerVersion,
		"frontmatter":              extraction.Document.Frontmatter,
		"frontmatter_raw":          extraction.Document.FrontmatterRaw,
		"headings":                 extraction.Document.Headings,
		"links":                    extraction.Links,
		"warnings":                 extraction.Warnings,
		"absolute_time_candidates": extraction.AbsoluteTimeCandidates,
		"absolute_time_warnings":   extraction.AbsoluteTimeWarnings,
		"absolute_time":            extraction.AbsoluteTime,
		"extraction_metadata":      extraction.Metadata,
		"text_sections":            extraction.TextSections,
		"extracted_text_len":       len(extraction.Document.Text),
	})
	if err != nil {
		return emptyJSONObject
	}
	return payload
}

func textPipelineStatusMetadata(extraction TextPipelineExtraction, stage string) json.RawMessage {
	if extraction.Extraction.Status == "" {
		extraction.Extraction = extractionResultFromTextPipelineExtraction(extraction)
	}
	return extractionPipelineStatusMetadata(extraction.Extraction, stage)
}

func extractionPipelineStatusMetadata(extraction ExtractionResult, stage string) json.RawMessage {
	pipelineKey := KnowledgeObjectPipelineNotesFileExtraction
	if stage == PipelineStageBM25 {
		pipelineKey = KnowledgeSearchIndexKey
	}
	payload, err := json.Marshal(map[string]any{
		"schema_version":    "knowledge.pipeline_status.v0.8",
		"pipeline_key":      pipelineKey,
		"pipeline_version":  KnowledgeFileExtractionPipelineVersion,
		"stage":             stage,
		"extractor_key":     extraction.ExtractorKey,
		"extractor_version": extraction.ExtractorVersion,
		"extraction_status": extraction.Status,
		"chunker_version":   KnowledgeMarkdownTextChunkerVersion,
		"chunk_count":       len(extraction.Chunks),
		"link_count":        len(extraction.Links),
		"warning_count":     len(extraction.Warnings),
	})
	if err != nil {
		return emptyJSONObject
	}
	return payload
}

func extractionResultFromTextPipelineExtraction(extraction TextPipelineExtraction) ExtractionResult {
	return ExtractionResult{
		Status:           ExtractionStatusExtracted,
		ExtractorKey:     ExtractorKeyMarkdownText,
		ExtractorVersion: KnowledgeMarkdownTextExtractorVersion,
		Document:         extraction.Document,
		Chunks:           extraction.Chunks,
		Links:            extraction.Document.Links,
		Warnings:         extraction.Document.Warnings,
	}
}
