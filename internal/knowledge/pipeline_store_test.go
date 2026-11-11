package knowledge

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"

	"encoding/json"
	"fmt"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/search"
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

func TestPipelineStoreWritePageBoundsAndParameters(t *testing.T) {
	rows := make([][]any, pipelineWriteBatchRows+1)
	for i := range rows {
		rows[i] = []any{"café ' quote", nil, i, json.RawMessage(`{"location":"page:2"}`)}
	}
	if end := pipelineWriteBatchEnd(rows, 0); end != pipelineWriteBatchRows {
		t.Fatalf("row-limited end = %d", end)
	}
	if end := pipelineWriteBatchEnd(nil, 0); end != 0 {
		t.Fatalf("empty end = %d", end)
	}
	types := []string{"text", "text", "integer", "jsonb"}
	values, args := pipelineWriteValues(rows[:2], types)
	if values != "($1::text,$2::text,$3::integer,$4::jsonb),($5::text,$6::text,$7::integer,$8::jsonb)" ||
		!reflect.DeepEqual(args, append(append([]any{}, rows[0]...), rows[1]...)) {
		t.Fatalf("parameter identity/order changed: %s %#v", values, args)
	}
	rows = [][]any{{strings.Repeat("界", pipelineWriteBatchBytes/3)}, {"next"}}
	if end := pipelineWriteBatchEnd(rows, 0); end != 1 {
		t.Fatalf("oversized scalar must stand alone: end = %d", end)
	}
	rows = [][]any{{json.RawMessage(strings.Repeat(" ", pipelineWriteBatchBytes/2))}, {strings.Repeat("x", pipelineWriteBatchBytes/2)}}
	if end := pipelineWriteBatchEnd(rows, 0); end != 1 {
		t.Fatalf("JSON bytes not bounded: end = %d", end)
	}
}

// This uses the existing guarded disposable DB owner. When no DB is configured,
// the integrator must run it on Main; local unit checks are not SQL acceptance.
func TestPipelineStoreBulkReplacementPostgres(t *testing.T) {
	db, url := boxSourcesDatabase(t)
	if _, err := migrations.Up(t.Context(), url, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	s, object := pipelineFixture(t, db, "markdown", "text/markdown", "bulk writes", time.Now().UTC())
	run, err := s.EnsurePipelineRun(t.Context(), object, PipelinePolicy{}, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	version, err := s.store.getKnowledgeObjectVersion(t.Context(), run.KnowledgeObjectVersionID)
	if err != nil {
		t.Fatal(err)
	}
	inputs := make([]TextChunkInput, pipelineWriteBatchRows+1)
	linkInputs := make([]ExtractedLink, len(inputs))
	for i := range inputs {
		text := fmt.Sprintf("café ' 日本語 %d", i)
		// Deliberately descending/noncontiguous ordinals must never be reassigned.
		inputs[i] = TextChunkInput{Index: (len(inputs) - i) * 2, Text: text, ChunkHash: hashArtifactValue(text),
			StartOffset: i * 100, EndOffset: i*100 + len(text), TokenCountEstimate: i + 1, StructuralPath: "heading ' / 界",
			ArtifactIDs: []string{"artifact-test"}, SourceLocators: []string{"page:2"}, SourceKinds: []string{"native_text"}}
		linkInputs[i] = ExtractedLink{Kind: LinkKindWikilink, RawTarget: fmt.Sprintf("日本語 '%d", i), NormalizedTarget: fmt.Sprintf("target/%d", i), LinkText: text}
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	chunks, err := s.replaceKnowledgeChunksTx(t.Context(), tx, object, version, inputs)
	if err != nil {
		t.Fatal(err)
	}
	links, err := s.replaceObjectLinksTx(t.Context(), tx, object, linkInputs)
	if err != nil {
		t.Fatal(err)
	}
	// The lexical writer extends an existing search document, as in production.
	if _, err := tx.ExecContext(t.Context(), `INSERT INTO search.search_documents
	 (search_document_id,source_kind,source_id,body,body_hash,tsv,index_version)
	 VALUES ($1,'object_metadata',$2,'bulk',$3,to_tsvector('simple','bulk'),'bulk-test')`,
		"search_document_"+object.KnowledgeObjectID, object.KnowledgeObjectID, hashArtifactValue("bulk")); err != nil {
		t.Fatal(err)
	}
	lexical, err := search.UpsertLexicalDocumentTx(t.Context(), tx, search.LexicalDocumentInput{
		SearchDocumentID: "search_document_" + object.KnowledgeObjectID, IndexVersion: "bulk-test", SourceKind: "knowledge_object", SourceID: object.KnowledgeObjectID,
		Fields: []search.LexicalFieldInput{{Key: search.LexicalFieldBody, Text: strings.Repeat("café café 日本語 ", 3) + bulkLexicalUniqueTerms()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for i, chunk := range chunks {
		input := inputs[i]
		if chunk.ChunkIndex != input.Index || chunk.ChunkText != input.Text || chunk.ChunkHash != input.ChunkHash ||
			*chunk.StartOffset != input.StartOffset || *chunk.EndOffset != input.EndOffset || *chunk.TokenCountEstimate != input.TokenCountEstimate ||
			chunk.KnowledgeObjectVersionID == nil || *chunk.KnowledgeObjectVersionID != version.KnowledgeObjectVersionID ||
			!strings.Contains(string(chunk.Metadata), "page:2") || chunk.IndexedAt != nil {
			t.Fatalf("chunk %d changed identity/offset/provenance: %#v", i, chunk)
		}
		if links[i].RawTarget != linkInputs[i].RawTarget || links[i].LinkText != linkInputs[i].LinkText || links[i].ChunkID != nil || links[i].TargetKnowledgeObjectID != nil {
			t.Fatalf("link %d changed order/null semantics: %#v", i, links[i])
		}
	}
	var termCount, frequency int
	if err = db.QueryRow(`SELECT count(*),max(term_frequency) FROM search.lexical_terms WHERE search_document_id=$1`, lexical.SearchDocumentID).Scan(&termCount, &frequency); err != nil || termCount != len(lexical.Terms) || frequency != 6 {
		t.Fatalf("bulk terms count/frequency = %d/%d: %v", termCount, frequency, err)
	}
	// A later-page uniqueness failure must roll back the earlier delete and page.
	bad := append([]TextChunkInput{}, inputs...)
	bad[len(bad)-1].Index = bad[0].Index
	tx, err = db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.replaceKnowledgeChunksTx(t.Context(), tx, object, version, bad); err == nil {
		t.Fatal("expected duplicate ordinal failure")
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var original int
	if err = db.QueryRow(`SELECT count(*) FROM knowledge.knowledge_chunks WHERE knowledge_chunk_id=$1`, chunks[0].KnowledgeChunkID).Scan(&original); err != nil || original != 1 {
		t.Fatalf("rollback lost original chunk: %d %v", original, err)
	}
	// Empty pages still delete the old terms/chunks/links atomically;
	// no invalid VALUES/unnest page is issued.
	tx, err = db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if chunks, err := s.replaceKnowledgeChunksTx(context.Background(), tx, object, version, nil); err != nil || len(chunks) != 0 {
		t.Fatalf("empty chunks: %v", err)
	}
	if links, err := s.replaceObjectLinksTx(t.Context(), tx, object, nil); err != nil || len(links) != 0 {
		t.Fatalf("empty links: %v", err)
	}
	if _, err := search.UpsertLexicalDocumentTx(t.Context(), tx, search.LexicalDocumentInput{SearchDocumentID: lexical.SearchDocumentID, IndexVersion: "empty", SourceKind: "knowledge_object", SourceID: object.KnowledgeObjectID}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"knowledge.knowledge_chunks", "knowledge.object_links", "search.lexical_terms"} {
		key, id := "knowledge_object_version_id", version.KnowledgeObjectVersionID
		if table == "knowledge.object_links" {
			key, id = "source_knowledge_object_id", object.KnowledgeObjectID
		}
		if table == "search.lexical_terms" {
			key, id = "search_document_id", lexical.SearchDocumentID
		}
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM `+table+` WHERE `+key+`=$1`, id).Scan(&count); err != nil || count != 0 {
			t.Fatalf("empty replacement %s: %d %v", table, count, err)
		}
	}
}

func bulkLexicalUniqueTerms() string {
	var text strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&text, " term%d", i)
	}
	return text.String()
}
