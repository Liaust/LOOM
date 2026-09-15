package knowledge

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestScanPipelineStatusAllowsNullableWorkerFields(t *testing.T) {
	now := time.Date(2026, 7, 4, 11, 15, 0, 0, time.UTC)

	status, err := scanPipelineStatus(fakePipelineStatusScanner{values: []any{
		"knowledge_pipeline_status_test",
		"knowledge_object_test",
		nil,
		KnowledgeObjectPipelineMarkdownText,
		KnowledgeMarkdownTextPipelineVersion,
		PipelineStageTextExtraction,
		PipelineStatusQueued,
		now,
		nil,
		nil,
		nil,
		"",
		"",
		[]byte(`{}`),
		0,
		nil,
		nil,
		nil,
		100,
		false,
		nil,
		nil,
		now,
		now,
	}})
	if err != nil {
		t.Fatalf("scanPipelineStatus returned error: %v", err)
	}

	if status.KnowledgeObjectVersionID != nil {
		t.Fatalf("version id = %q, want nil", *status.KnowledgeObjectVersionID)
	}
	if status.ClaimedByWorkerRunID != "" {
		t.Fatalf("claimed worker run id = %q, want empty", status.ClaimedByWorkerRunID)
	}
	if status.LastWorkerRunID != "" {
		t.Fatalf("last worker run id = %q, want empty", status.LastWorkerRunID)
	}
	if status.QueuedAt == nil || !status.QueuedAt.Equal(now) {
		t.Fatalf("queued_at = %v, want %s", status.QueuedAt, now)
	}
}

func TestProcessingStateForExtraction(t *testing.T) {
	if got := processingStateForExtraction(ExtractionResult{
		Status: ExtractionStatusExtracted,
		Chunks: []TextChunkInput{{
			Text:       "body",
			TextSource: TextSourceEmbeddedText,
		}},
	}, 1); got != ProcessingStateChunked {
		t.Fatalf("extracted state = %q, want chunked", got)
	}
	if got := processingStateForExtraction(ExtractionResult{Status: ExtractionStatusUnsupportedBodyExtraction}, 0); got != ProcessingStateMetadataOnly {
		t.Fatalf("metadata-only state = %q, want metadata_only", got)
	}
	if got := processingStateForExtraction(ExtractionResult{Status: ExtractionStatusFailed}, 0); got != ProcessingStateFailed {
		t.Fatalf("failed state = %q, want failed", got)
	}
}

func TestChunkMetadataIncludesExtractionProvenance(t *testing.T) {
	raw := chunkMetadata(TextChunkInput{
		Index:            1,
		Text:             "hello",
		TextSource:       TextSourceEmbeddedText,
		ExtractorKey:     ExtractorKeyMarkdownText,
		ExtractorVersion: "extractor_v1",
		ExtractionStatus: ExtractionStatusExtracted,
		SectionIndex:     7,
	})

	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil {
		t.Fatalf("metadata is invalid JSON: %v", err)
	}
	for key, want := range map[string]any{
		"text_source":       TextSourceEmbeddedText,
		"extractor_key":     ExtractorKeyMarkdownText,
		"extractor_version": "extractor_v1",
		"extraction_status": ExtractionStatusExtracted,
	} {
		if metadata[key] != want {
			t.Fatalf("metadata[%s] = %#v, want %#v in %#v", key, metadata[key], want, metadata)
		}
	}
	if metadata["section_index"] != float64(7) {
		t.Fatalf("section_index = %#v, want 7", metadata["section_index"])
	}
}

func TestRefineKnowledgeAbsoluteTimePreservesSourceMtimeOverEmbeddedCandidate(t *testing.T) {
	service := NewService(nil)
	observed := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	sourceMtime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	embedded := time.Date(2025, 6, 7, 8, 9, 10, 0, time.UTC)
	base, err := ResolveAbsoluteTime(AbsoluteTimeResolutionInput{
		ObservedAt: observed,
		Candidates: []AbsoluteTimeCandidate{{
			Kind: AbsoluteTimeKindModified, Basis: AbsoluteTimeBasisSourceFilesystemMtime,
			RawValue: sourceMtime.Format(time.RFC3339), Timestamp: &sourceMtime,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	object := KnowledgeObject{
		LastSeenAt:           observed,
		SourceModifiedAt:     base.SourceModifiedAt,
		RecencyAt:            base.RecencyAt,
		RecencyBasis:         base.RecencyBasis,
		AbsoluteTimeMetadata: absoluteTimeMetadataJSON(base),
	}
	extraction := attachExtractionAbsoluteTime(ExtractionResult{Metadata: map[string]any{}}, []AbsoluteTimeCandidate{{
		Kind: AbsoluteTimeKindModified, Basis: AbsoluteTimeBasisEmbeddedModifiedAt,
		RawValue: embedded.Format(time.RFC3339), Timestamp: &embedded,
	}})

	refinedObject, refinedExtraction, err := service.refineKnowledgeAbsoluteTime(object, extraction)
	if err != nil {
		t.Fatalf("refineKnowledgeAbsoluteTime: %v", err)
	}
	if refinedObject.SourceModifiedAt == nil || !refinedObject.SourceModifiedAt.Equal(sourceMtime) ||
		refinedObject.RecencyBasis != AbsoluteTimeBasisSourceFilesystemMtime {
		t.Fatalf("refined object = %#v, want preserved source mtime", refinedObject)
	}
	if refinedExtraction.AbsoluteTime == nil || len(refinedExtraction.AbsoluteTime.Candidates) != 3 {
		t.Fatalf("refined extraction absolute time = %#v", refinedExtraction.AbsoluteTime)
	}
}

func TestRefineKnowledgeAbsoluteTimeUsesFrontmatterOverObservationFallback(t *testing.T) {
	service := NewService(nil)
	observed := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	base, err := ResolveAbsoluteTime(AbsoluteTimeResolutionInput{ObservedAt: observed})
	if err != nil {
		t.Fatal(err)
	}
	frontmatterTime := time.Date(2020, 2, 3, 4, 5, 6, 0, time.UTC)
	object := KnowledgeObject{
		LastSeenAt:           observed,
		RecencyAt:            base.RecencyAt,
		RecencyBasis:         base.RecencyBasis,
		AbsoluteTimeMetadata: absoluteTimeMetadataJSON(base),
	}
	extraction := attachExtractionAbsoluteTime(ExtractionResult{
		Metadata: map[string]any{},
		Document: TextDocument{FrontmatterRaw: "updated_at: 2020-02-03T04:05:06Z\n"},
	}, []AbsoluteTimeCandidate{{
		Kind: AbsoluteTimeKindModified, Basis: AbsoluteTimeBasisFrontmatterUpdatedAt,
		RawValue: frontmatterTime.Format(time.RFC3339), Timestamp: &frontmatterTime,
	}})

	refinedObject, refinedExtraction, err := service.refineKnowledgeAbsoluteTime(object, extraction)
	if err != nil {
		t.Fatalf("refineKnowledgeAbsoluteTime: %v", err)
	}
	if refinedObject.SourceModifiedAt == nil || !refinedObject.SourceModifiedAt.Equal(frontmatterTime) ||
		refinedObject.RecencyBasis != AbsoluteTimeBasisFrontmatterUpdatedAt {
		t.Fatalf("refined object = %#v, want frontmatter chronology", refinedObject)
	}
	metadata := extractionPipelineVersionMetadata(refinedExtraction)
	if !strings.Contains(string(metadata), "frontmatter_updated_at") ||
		!strings.Contains(string(metadata), "updated_at: 2020-02-03T04:05:06Z") {
		t.Fatalf("version metadata = %s, want candidate basis and raw frontmatter", metadata)
	}
}

func TestExistingRevisionChronologyDoesNotChangeWhenCandidatesChange(t *testing.T) {
	observed := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	firstModified := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	changedCandidate := time.Date(2025, 6, 7, 8, 9, 10, 0, time.UTC)
	existing := KnowledgeObjectVersion{
		SourceModifiedAt:     &firstModified,
		RecencyAt:            firstModified,
		RecencyBasis:         AbsoluteTimeBasisSourceFilesystemMtime,
		AbsoluteTimeMetadata: mustJSON(t, map[string]any{"selected": "first"}),
		ObservedAt:           observed,
	}
	candidate := KnowledgeObject{
		SourceModifiedAt:     &changedCandidate,
		RecencyAt:            changedCandidate,
		RecencyBasis:         AbsoluteTimeBasisEmbeddedModifiedAt,
		AbsoluteTimeMetadata: mustJSON(t, map[string]any{"selected": "changed"}),
	}

	got, enriched := chronologyForExistingRevision(existing, candidate)
	if enriched {
		t.Fatal("existing revision chronology was eligible for rewrite")
	}
	if got.SourceModifiedAt == nil || !got.SourceModifiedAt.Equal(firstModified) ||
		!got.RecencyAt.Equal(firstModified) || got.RecencyBasis != AbsoluteTimeBasisSourceFilesystemMtime ||
		string(got.AbsoluteTimeMetadata) != string(existing.AbsoluteTimeMetadata) {
		t.Fatalf("chronology = %#v, want first immutable decision %#v", got, existing)
	}
}

func TestLegacyRevisionChronologyCanBeEnrichedOnlyOnce(t *testing.T) {
	observed := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	sourceModified := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	legacy := KnowledgeObjectVersion{
		RecencyAt:            observed,
		RecencyBasis:         AbsoluteTimeBasisObservedAtFallback,
		AbsoluteTimeMetadata: json.RawMessage(`{}`),
		ObservedAt:           observed,
	}
	candidate := KnowledgeObject{
		SourceModifiedAt:     &sourceModified,
		RecencyAt:            sourceModified,
		RecencyBasis:         AbsoluteTimeBasisEmbeddedModifiedAt,
		AbsoluteTimeMetadata: mustJSON(t, map[string]any{"selected": "embedded_modified_at"}),
	}

	enriched, ok := chronologyForExistingRevision(legacy, candidate)
	if !ok || enriched.SourceModifiedAt == nil || !enriched.SourceModifiedAt.Equal(sourceModified) ||
		!enriched.RecencyAt.Equal(sourceModified) || enriched.RecencyBasis != AbsoluteTimeBasisEmbeddedModifiedAt {
		t.Fatalf("legacy enrichment = %#v, ok=%t", enriched, ok)
	}
	changedCandidate := sourceModified.Add(24 * time.Hour)
	second, ok := chronologyForExistingRevision(enriched, KnowledgeObject{
		SourceModifiedAt:     &changedCandidate,
		RecencyAt:            changedCandidate,
		RecencyBasis:         AbsoluteTimeBasisEmbeddedModifiedAt,
		AbsoluteTimeMetadata: mustJSON(t, map[string]any{"selected": "later"}),
	})
	if ok || second.SourceModifiedAt == nil || !second.SourceModifiedAt.Equal(sourceModified) ||
		string(second.AbsoluteTimeMetadata) != string(enriched.AbsoluteTimeMetadata) {
		t.Fatalf("second enrichment rewrote chronology: %#v, ok=%t", second, ok)
	}
}

type fakePipelineStatusScanner struct {
	values []any
}

func (s fakePipelineStatusScanner) Scan(dest ...any) error {
	if len(dest) != len(s.values) {
		return fmt.Errorf("scan destination count = %d, want %d", len(dest), len(s.values))
	}
	for i := range dest {
		if err := assignScanValue(dest[i], s.values[i]); err != nil {
			return fmt.Errorf("column %d: %w", i, err)
		}
	}
	return nil
}

func assignScanValue(dest any, value any) error {
	switch target := dest.(type) {
	case *string:
		if value == nil {
			return fmt.Errorf("converting NULL to string is unsupported")
		}
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("cannot assign %T to string", value)
		}
		*target = text
	case *sql.NullString:
		if value == nil {
			*target = sql.NullString{}
			return nil
		}
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("cannot assign %T to NullString", value)
		}
		*target = sql.NullString{String: text, Valid: true}
	case *sql.NullTime:
		if value == nil {
			*target = sql.NullTime{}
			return nil
		}
		timestamp, ok := value.(time.Time)
		if !ok {
			return fmt.Errorf("cannot assign %T to NullTime", value)
		}
		*target = sql.NullTime{Time: timestamp, Valid: true}
	case *sql.NullInt64:
		if value == nil {
			*target = sql.NullInt64{}
			return nil
		}
		integer, ok := value.(int64)
		if !ok {
			return fmt.Errorf("cannot assign %T to NullInt64", value)
		}
		*target = sql.NullInt64{Int64: integer, Valid: true}
	case *time.Time:
		timestamp, ok := value.(time.Time)
		if !ok {
			return fmt.Errorf("cannot assign %T to Time", value)
		}
		*target = timestamp
	case *[]byte:
		switch typed := value.(type) {
		case nil:
			*target = nil
		case []byte:
			*target = typed
		case string:
			*target = []byte(typed)
		default:
			return fmt.Errorf("cannot assign %T to bytes", value)
		}
	case *int:
		integer, ok := value.(int)
		if !ok {
			return fmt.Errorf("cannot assign %T to int", value)
		}
		*target = integer
	case *bool:
		boolean, ok := value.(bool)
		if !ok {
			return fmt.Errorf("cannot assign %T to bool", value)
		}
		*target = boolean
	default:
		return fmt.Errorf("unsupported scan destination %T", dest)
	}
	return nil
}
