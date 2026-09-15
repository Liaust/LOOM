package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

func (s *Service) applyTextPipeline(ctx context.Context, object KnowledgeObject, extraction TextPipelineExtraction) (TextPipelineResult, error) {
	if extraction.Extraction.Status == "" {
		extraction.Extraction = extractionResultFromTextPipelineExtraction(extraction)
	}
	return s.applyExtractionPipeline(ctx, object, extraction.Extraction)
}

func (s *Service) applyExtractionPipeline(ctx context.Context, object KnowledgeObject, extraction ExtractionResult) (TextPipelineResult, error) {
	var err error
	object, extraction, err = s.refineKnowledgeAbsoluteTime(object, extraction)
	if err != nil {
		return TextPipelineResult{}, err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return TextPipelineResult{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	version, err := s.getOrCreateKnowledgeObjectVersionTx(ctx, tx, object, extractionPipelineVersionMetadata(extraction))
	if err != nil {
		return TextPipelineResult{}, err
	}
	now := s.currentTime()
	chunks, err := s.replaceKnowledgeChunksTx(ctx, tx, object, version, extraction.Chunks)
	if err != nil {
		return TextPipelineResult{}, err
	}
	chunks, _, err = s.replaceKnowledgeSearchDocumentsTx(ctx, tx, object, version, chunks)
	if err != nil {
		return TextPipelineResult{}, err
	}
	links, err := s.replaceObjectLinksTx(ctx, tx, object, extraction.Links)
	if err != nil {
		return TextPipelineResult{}, err
	}
	statuses, err := s.upsertExtractionPipelineStatusesTx(ctx, tx, object, version, extraction, now)
	if err != nil {
		return TextPipelineResult{}, err
	}
	processedAt := now
	object.ProcessingState = processingStateForExtraction(extraction, len(chunks))
	if extraction.Status == ExtractionStatusExtracted && len(chunks) > 0 {
		object.ProcessingState = ProcessingStateChunked
	}
	object.PipelineKey = KnowledgeObjectPipelineNotesFileExtraction
	object.PipelineVersion = KnowledgeFileExtractionPipelineVersion
	object.LastProcessedAt = &processedAt
	object.LastErrorCode = ""
	object.LastErrorMessage = ""
	object.Metadata = extractionPipelineObjectMetadata(object.Metadata, version, extraction)
	object.UpdatedAt = now
	object, err = updateKnowledgeObjectTx(ctx, tx, object)
	if err != nil {
		return TextPipelineResult{}, err
	}
	if !extractionHasBodyText(extraction) {
		if err := replaceKnowledgeMetadataSearchDocumentForObjectTx(ctx, tx, object); err != nil {
			return TextPipelineResult{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return TextPipelineResult{}, err
	}
	return TextPipelineResult{
		Object:   object,
		Version:  version,
		Chunks:   chunks,
		Links:    links,
		Statuses: statuses,
	}, nil
}

func (s *Service) getOrCreateKnowledgeObjectVersionTx(ctx context.Context, tx *sql.Tx, object KnowledgeObject, metadata json.RawMessage) (KnowledgeObjectVersion, error) {
	version, found, err := getKnowledgeObjectVersionByRevisionTx(ctx, tx, object.KnowledgeObjectID, object.SourceHash, object.SourceRevision)
	if err != nil {
		return KnowledgeObjectVersion{}, err
	}
	if found {
		if pipelineSourceVersionPlaceholder(metadata) && !pipelineSourceVersionPlaceholder(version.Metadata) {
			metadata = version.Metadata
		}
		return updateKnowledgeObjectVersionMetadataTx(ctx, tx, version, object, metadata)
	}
	versionNumber, err := nextKnowledgeObjectVersionNumberTx(ctx, tx, object.KnowledgeObjectID)
	if err != nil {
		return KnowledgeObjectVersion{}, err
	}
	version, err = s.prepareKnowledgeObjectVersionForObject(object, versionNumber, metadata)
	if err != nil {
		return KnowledgeObjectVersion{}, err
	}
	row := tx.QueryRowContext(ctx, `
		INSERT INTO knowledge.knowledge_object_versions (
			knowledge_object_version_id, knowledge_object_id, version_number,
			storage_entry_id, source_hash, source_revision, size_bytes,
			mime_type, file_class, source_path, source_created_at,
			source_modified_at, recency_at, recency_basis, absolute_time_metadata,
			metadata, observed_at, created_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		RETURNING `+knowledgeObjectVersionColumns(),
		version.KnowledgeObjectVersionID,
		version.KnowledgeObjectID,
		version.VersionNumber,
		nullableString(version.StorageEntryID),
		version.SourceHash,
		version.SourceRevision,
		nullableInt64(version.SizeBytes),
		version.MimeType,
		version.FileClass,
		version.SourcePath,
		nullableTime(version.SourceCreatedAt),
		nullableTime(version.SourceModifiedAt),
		version.RecencyAt,
		version.RecencyBasis,
		version.AbsoluteTimeMetadata,
		version.Metadata,
		version.ObservedAt,
		version.CreatedAt,
	)
	return scanKnowledgeObjectVersion(row)
}

func (s *Service) prepareKnowledgeObjectVersionForObject(object KnowledgeObject, versionNumber int, metadata json.RawMessage) (KnowledgeObjectVersion, error) {
	return s.PrepareKnowledgeObjectVersion(KnowledgeObjectVersion{
		KnowledgeObjectID:    object.KnowledgeObjectID,
		VersionNumber:        versionNumber,
		StorageEntryID:       object.StorageEntryID,
		SourceHash:           object.SourceHash,
		SourceRevision:       object.SourceRevision,
		SizeBytes:            object.SizeBytes,
		MimeType:             object.MimeType,
		FileClass:            object.FileClass,
		SourcePath:           object.SourcePath,
		SourceCreatedAt:      object.SourceCreatedAt,
		SourceModifiedAt:     object.SourceModifiedAt,
		RecencyAt:            object.RecencyAt,
		RecencyBasis:         object.RecencyBasis,
		AbsoluteTimeMetadata: object.AbsoluteTimeMetadata,
		Metadata:             metadata,
	})
}

func getKnowledgeObjectVersionByRevisionTx(ctx context.Context, tx *sql.Tx, objectID, sourceHash, sourceRevision string) (KnowledgeObjectVersion, bool, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+knowledgeObjectVersionColumns()+`
		FROM knowledge.knowledge_object_versions
		WHERE knowledge_object_id = $1
		  AND source_hash = $2
		  AND source_revision = $3
		ORDER BY version_number DESC
		LIMIT 1`, objectID, sourceHash, sourceRevision)
	version, err := scanKnowledgeObjectVersion(row)
	if err == sql.ErrNoRows {
		return KnowledgeObjectVersion{}, false, nil
	}
	if err != nil {
		return KnowledgeObjectVersion{}, false, err
	}
	return version, true, nil
}

func updateKnowledgeObjectVersionMetadataTx(ctx context.Context, tx *sql.Tx, version KnowledgeObjectVersion, object KnowledgeObject, metadata json.RawMessage) (KnowledgeObjectVersion, error) {
	chronology, enrichLegacy := chronologyForExistingRevision(version, object)
	if !enrichLegacy {
		row := tx.QueryRowContext(ctx, `UPDATE knowledge.knowledge_object_versions
			SET metadata = $2
			WHERE knowledge_object_version_id = $1
			RETURNING `+knowledgeObjectVersionColumns(),
			version.KnowledgeObjectVersionID,
			metadata)
		return scanKnowledgeObjectVersion(row)
	}
	row := tx.QueryRowContext(ctx, `UPDATE knowledge.knowledge_object_versions
		SET source_created_at = CASE
		        WHEN source_created_at IS NULL
		         AND source_modified_at IS NULL
		         AND recency_basis = 'observed_at_fallback'
		         AND recency_at = observed_at
		         AND absolute_time_metadata = '{}'::jsonb
		        THEN $2 ELSE source_created_at END,
		    source_modified_at = CASE
		        WHEN source_created_at IS NULL
		         AND source_modified_at IS NULL
		         AND recency_basis = 'observed_at_fallback'
		         AND recency_at = observed_at
		         AND absolute_time_metadata = '{}'::jsonb
		        THEN $3 ELSE source_modified_at END,
		    recency_at = CASE
		        WHEN source_created_at IS NULL
		         AND source_modified_at IS NULL
		         AND recency_basis = 'observed_at_fallback'
		         AND recency_at = observed_at
		         AND absolute_time_metadata = '{}'::jsonb
		        THEN $4 ELSE recency_at END,
		    recency_basis = CASE
		        WHEN source_created_at IS NULL
		         AND source_modified_at IS NULL
		         AND recency_basis = 'observed_at_fallback'
		         AND recency_at = observed_at
		         AND absolute_time_metadata = '{}'::jsonb
		        THEN $5 ELSE recency_basis END,
		    absolute_time_metadata = CASE
		        WHEN source_created_at IS NULL
		         AND source_modified_at IS NULL
		         AND recency_basis = 'observed_at_fallback'
		         AND recency_at = observed_at
		         AND absolute_time_metadata = '{}'::jsonb
		        THEN $6 ELSE absolute_time_metadata END,
		    metadata = $7
		WHERE knowledge_object_version_id = $1
		RETURNING `+knowledgeObjectVersionColumns(),
		version.KnowledgeObjectVersionID,
		nullableTime(chronology.SourceCreatedAt),
		nullableTime(chronology.SourceModifiedAt),
		chronology.RecencyAt,
		chronology.RecencyBasis,
		chronology.AbsoluteTimeMetadata,
		metadata)
	return scanKnowledgeObjectVersion(row)
}

// chronologyForExistingRevision preserves the first absolute-time decision for
// an immutable source revision. The sole exception is a row demonstrably
// created before absolute-time metadata existed: migration 00059 labels those
// rows with observation fallback, no source times, and an empty metadata
// object. Such a row may receive one stronger source-time decision; afterward
// it no longer matches this gate and cannot be rewritten by reprocessing.
func chronologyForExistingRevision(version KnowledgeObjectVersion, object KnowledgeObject) (KnowledgeObjectVersion, bool) {
	if version.SourceCreatedAt != nil || version.SourceModifiedAt != nil ||
		version.RecencyAt.IsZero() || version.ObservedAt.IsZero() ||
		!version.RecencyAt.Equal(version.ObservedAt) ||
		version.RecencyBasis != AbsoluteTimeBasisObservedAtFallback ||
		!isEmptyAbsoluteTimeMetadata(version.AbsoluteTimeMetadata) {
		return version, false
	}
	if object.RecencyAt.IsZero() || object.RecencyBasis == "" ||
		object.RecencyBasis == AbsoluteTimeBasisObservedAtFallback ||
		(object.SourceCreatedAt == nil && object.SourceModifiedAt == nil) ||
		isEmptyAbsoluteTimeMetadata(object.AbsoluteTimeMetadata) {
		return version, false
	}

	enriched := version
	enriched.SourceCreatedAt = object.SourceCreatedAt
	enriched.SourceModifiedAt = object.SourceModifiedAt
	enriched.RecencyAt = object.RecencyAt
	enriched.RecencyBasis = object.RecencyBasis
	enriched.AbsoluteTimeMetadata = append(json.RawMessage(nil), object.AbsoluteTimeMetadata...)
	return enriched, true
}

func isEmptyAbsoluteTimeMetadata(raw json.RawMessage) bool {
	var value map[string]any
	return len(raw) == 0 || json.Unmarshal(raw, &value) == nil && len(value) == 0
}

func (s *Service) refineKnowledgeAbsoluteTime(object KnowledgeObject, extraction ExtractionResult) (KnowledgeObject, ExtractionResult, error) {
	observedAt := object.LastSeenAt
	if observedAt.IsZero() {
		observedAt = s.currentTime()
	}
	existing := absoluteTimeFromMetadata(object.AbsoluteTimeMetadata, AbsoluteTime{
		SourceCreatedAt:  object.SourceCreatedAt,
		SourceModifiedAt: object.SourceModifiedAt,
		RecencyAt:        object.RecencyAt,
		RecencyBasis:     object.RecencyBasis,
	})
	resolved, err := refineAbsoluteTime(observedAt, existing, extraction.AbsoluteTimeCandidates)
	if err != nil {
		return KnowledgeObject{}, ExtractionResult{}, err
	}
	object.SourceCreatedAt = resolved.SourceCreatedAt
	object.SourceModifiedAt = resolved.SourceModifiedAt
	object.RecencyAt = resolved.RecencyAt
	object.RecencyBasis = resolved.RecencyBasis
	object.AbsoluteTimeMetadata = absoluteTimeMetadataJSON(resolved)
	extraction.AbsoluteTime = &resolved
	extraction.AbsoluteTimeWarnings = resolved.Warnings
	if extraction.Metadata == nil {
		extraction.Metadata = map[string]any{}
	}
	extraction.Metadata["absolute_time"] = resolved
	extraction.Metadata["absolute_time_candidates"] = extraction.AbsoluteTimeCandidates
	extraction.Metadata["absolute_time_warnings"] = resolved.Warnings
	return object, extraction, nil
}

func nextKnowledgeObjectVersionNumberTx(ctx context.Context, tx *sql.Tx, objectID string) (int, error) {
	var next int
	err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(version_number), 0) + 1
		FROM knowledge.knowledge_object_versions
		WHERE knowledge_object_id = $1
	`, objectID).Scan(&next)
	return next, err
}

func (s *Service) replaceKnowledgeChunksTx(ctx context.Context, tx *sql.Tx, object KnowledgeObject, version KnowledgeObjectVersion, inputs []TextChunkInput) ([]KnowledgeChunk, error) {
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM knowledge.knowledge_chunks
		WHERE knowledge_object_version_id = $1
	`, version.KnowledgeObjectVersionID); err != nil {
		return nil, err
	}
	chunks := make([]KnowledgeChunk, 0, len(inputs))
	versionID := version.KnowledgeObjectVersionID
	for _, input := range inputs {
		startOffset := input.StartOffset
		endOffset := input.EndOffset
		tokenEstimate := input.TokenCountEstimate
		chunk, err := s.PrepareKnowledgeChunk(KnowledgeChunk{
			KnowledgeObjectID:        object.KnowledgeObjectID,
			KnowledgeObjectVersionID: &versionID,
			ChunkIndex:               input.Index,
			ChunkText:                input.Text,
			ChunkHash:                input.ChunkHash,
			StructuralPath:           input.StructuralPath,
			StartOffset:              &startOffset,
			EndOffset:                &endOffset,
			TokenCountEstimate:       &tokenEstimate,
			ChunkerVersion:           KnowledgeMarkdownTextChunkerVersion,
			Status:                   ChunkStatusCreated,
			Metadata:                 chunkMetadata(input),
		})
		if err != nil {
			return nil, err
		}
		row := tx.QueryRowContext(ctx, `
			INSERT INTO knowledge.knowledge_chunks (
				knowledge_chunk_id, knowledge_object_id, knowledge_object_version_id,
				chunk_index, chunk_text, chunk_hash, structural_path, start_offset,
				end_offset, token_count_estimate, chunker_version, status, metadata,
				created_at, indexed_at
			)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
			RETURNING `+knowledgeChunkColumns(),
			chunk.KnowledgeChunkID,
			chunk.KnowledgeObjectID,
			nullableString(chunk.KnowledgeObjectVersionID),
			chunk.ChunkIndex,
			chunk.ChunkText,
			chunk.ChunkHash,
			chunk.StructuralPath,
			nullableInt(chunk.StartOffset),
			nullableInt(chunk.EndOffset),
			nullableInt(chunk.TokenCountEstimate),
			chunk.ChunkerVersion,
			chunk.Status,
			chunk.Metadata,
			chunk.CreatedAt,
			nullableTime(chunk.IndexedAt),
		)
		chunk, err = scanKnowledgeChunk(row)
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
	}
	return chunks, nil
}

func (s *Service) replaceObjectLinksTx(ctx context.Context, tx *sql.Tx, object KnowledgeObject, inputs []ExtractedLink) ([]ObjectLink, error) {
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM knowledge.object_links
		WHERE source_knowledge_object_id = $1
	`, object.KnowledgeObjectID); err != nil {
		return nil, err
	}
	links := make([]ObjectLink, 0, len(inputs))
	for _, input := range inputs {
		link, err := s.PrepareObjectLink(ObjectLink{
			SourceKnowledgeObjectID: object.KnowledgeObjectID,
			LinkKind:                input.Kind,
			RawTarget:               input.RawTarget,
			NormalizedTarget:        input.NormalizedTarget,
			LinkText:                input.LinkText,
			Status:                  LinkStatusUnresolved,
			Metadata:                linkMetadata(input),
		})
		if err != nil {
			return nil, err
		}
		row := tx.QueryRowContext(ctx, `
			INSERT INTO knowledge.object_links (
				knowledge_object_link_id, source_knowledge_object_id,
				target_knowledge_object_id, chunk_id, link_kind, raw_target,
				normalized_target, link_text, status, metadata, created_at, updated_at
			)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			RETURNING `+objectLinkColumns(),
			link.KnowledgeObjectLinkID,
			link.SourceKnowledgeObjectID,
			nullableString(link.TargetKnowledgeObjectID),
			nullableString(link.ChunkID),
			link.LinkKind,
			link.RawTarget,
			link.NormalizedTarget,
			link.LinkText,
			link.Status,
			link.Metadata,
			link.CreatedAt,
			link.UpdatedAt,
		)
		link, err = scanObjectLink(row)
		if err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	return links, nil
}

func (s *Service) upsertTextPipelineStatusesTx(ctx context.Context, tx *sql.Tx, object KnowledgeObject, version KnowledgeObjectVersion, extraction TextPipelineExtraction, now time.Time) ([]PipelineStatus, error) {
	if extraction.Extraction.Status == "" {
		extraction.Extraction = extractionResultFromTextPipelineExtraction(extraction)
	}
	return s.upsertExtractionPipelineStatusesTx(ctx, tx, object, version, extraction.Extraction, now)
}

func (s *Service) upsertExtractionPipelineStatusesTx(ctx context.Context, tx *sql.Tx, object KnowledgeObject, version KnowledgeObjectVersion, extraction ExtractionResult, now time.Time) ([]PipelineStatus, error) {
	versionID := version.KnowledgeObjectVersionID
	chunkingStatus := PipelineStatusComplete
	if !extractionHasBodyText(extraction) {
		chunkingStatus = PipelineStatusSkippedUnsupported
	}
	inputs := []PipelineStatus{
		{
			KnowledgeObjectID:        object.KnowledgeObjectID,
			KnowledgeObjectVersionID: &versionID,
			PipelineKey:              KnowledgeObjectPipelineNotesFileExtraction,
			PipelineVersion:          KnowledgeFileExtractionPipelineVersion,
			Stage:                    PipelineStageTextExtraction,
			Status:                   PipelineStatusComplete,
			StartedAt:                &now,
			CompletedAt:              &now,
			Metadata:                 extractionPipelineStatusMetadata(extraction, PipelineStageTextExtraction),
		},
		{
			KnowledgeObjectID:        object.KnowledgeObjectID,
			KnowledgeObjectVersionID: &versionID,
			PipelineKey:              KnowledgeObjectPipelineNotesFileExtraction,
			PipelineVersion:          KnowledgeFileExtractionPipelineVersion,
			Stage:                    PipelineStageChunking,
			Status:                   chunkingStatus,
			StartedAt:                &now,
			CompletedAt:              &now,
			Metadata:                 extractionPipelineStatusMetadata(extraction, PipelineStageChunking),
		},
		{
			KnowledgeObjectID:        object.KnowledgeObjectID,
			KnowledgeObjectVersionID: &versionID,
			PipelineKey:              KnowledgeSearchIndexKey,
			PipelineVersion:          KnowledgeFileExtractionPipelineVersion,
			Stage:                    PipelineStageBM25,
			Status:                   PipelineStatusComplete,
			StartedAt:                &now,
			CompletedAt:              &now,
			Metadata:                 extractionPipelineStatusMetadata(extraction, PipelineStageBM25),
		},
	}
	statuses := make([]PipelineStatus, 0, len(inputs))
	for _, input := range inputs {
		status, err := s.PreparePipelineStatus(input)
		if err != nil {
			return nil, err
		}
		row := tx.QueryRowContext(ctx, `
			INSERT INTO knowledge.pipeline_statuses (
				knowledge_pipeline_status_id, knowledge_object_id,
				knowledge_object_version_id, pipeline_key, pipeline_version,
				stage, status, queued_at, started_at, completed_at, failed_at,
				last_error_code, last_error_message, metadata, attempt_count,
				next_attempt_at, claimed_by_worker_run_id, claim_expires_at,
				priority, manual_action_required, last_worker_run_id,
				last_attempt_at, created_at, updated_at
			)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,
			        $17,$18,$19,$20,$21,$22,$23,$24)
			ON CONFLICT (knowledge_object_id, (COALESCE(knowledge_object_version_id, '')), pipeline_key, stage)
			DO UPDATE
			SET pipeline_version = EXCLUDED.pipeline_version,
			    status = EXCLUDED.status,
			    queued_at = EXCLUDED.queued_at,
			    started_at = EXCLUDED.started_at,
			    completed_at = EXCLUDED.completed_at,
			    failed_at = EXCLUDED.failed_at,
			    last_error_code = EXCLUDED.last_error_code,
			    last_error_message = EXCLUDED.last_error_message,
			    metadata = EXCLUDED.metadata,
			    attempt_count = EXCLUDED.attempt_count,
			    next_attempt_at = EXCLUDED.next_attempt_at,
			    claimed_by_worker_run_id = EXCLUDED.claimed_by_worker_run_id,
			    claim_expires_at = EXCLUDED.claim_expires_at,
			    priority = EXCLUDED.priority,
			    manual_action_required = EXCLUDED.manual_action_required,
			    last_worker_run_id = EXCLUDED.last_worker_run_id,
			    last_attempt_at = EXCLUDED.last_attempt_at,
			    updated_at = now()
			RETURNING `+pipelineStatusColumns(),
			status.KnowledgePipelineStatusID,
			status.KnowledgeObjectID,
			nullableString(status.KnowledgeObjectVersionID),
			status.PipelineKey,
			status.PipelineVersion,
			status.Stage,
			status.Status,
			nullableTime(status.QueuedAt),
			nullableTime(status.StartedAt),
			nullableTime(status.CompletedAt),
			nullableTime(status.FailedAt),
			status.LastErrorCode,
			status.LastErrorMessage,
			status.Metadata,
			status.AttemptCount,
			nullableTime(status.NextAttemptAt),
			status.ClaimedByWorkerRunID,
			nullableTime(status.ClaimExpiresAt),
			status.Priority,
			status.ManualActionRequired,
			status.LastWorkerRunID,
			nullableTime(status.LastAttemptAt),
			status.CreatedAt,
			status.UpdatedAt,
		)
		status, err = scanPipelineStatus(row)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func chunkMetadata(input TextChunkInput) json.RawMessage {
	payload, err := json.Marshal(map[string]any{
		"schema_version":    "knowledge.chunk.v0.8.5",
		"chunker_version":   KnowledgeMarkdownTextChunkerVersion,
		"start_offset":      input.StartOffset,
		"end_offset":        input.EndOffset,
		"text_source":       input.TextSource,
		"extractor_key":     input.ExtractorKey,
		"extractor_version": input.ExtractorVersion,
		"extraction_status": input.ExtractionStatus,
		"section_index":     input.SectionIndex,
		"artifact_ids":      input.ArtifactIDs,
		"source_locators":   input.SourceLocators,
		"source_kinds":      input.SourceKinds,
	})
	if err != nil {
		return emptyJSONObject
	}
	return payload
}

func extractionHasBodyText(extraction ExtractionResult) bool {
	if extraction.Status != ExtractionStatusExtracted {
		return false
	}
	for _, chunk := range extraction.Chunks {
		if strings.TrimSpace(chunk.Text) != "" && strings.TrimSpace(chunk.TextSource) != TextSourceMetadataText {
			return true
		}
	}
	return false
}

func processingStateForExtraction(extraction ExtractionResult, chunkCount int) string {
	switch extraction.Status {
	case ExtractionStatusExtracted:
		if chunkCount > 0 {
			return ProcessingStateChunked
		}
		return ProcessingStateTextExtracted
	case ExtractionStatusFailed:
		return ProcessingStateFailed
	default:
		return ProcessingStateMetadataOnly
	}
}

func linkMetadata(input ExtractedLink) json.RawMessage {
	payload, err := json.Marshal(map[string]any{
		"schema_version": "knowledge.object_link.v0.8",
		"start_offset":   input.StartOffset,
		"end_offset":     input.EndOffset,
	})
	if err != nil {
		return emptyJSONObject
	}
	return payload
}

func knowledgeObjectVersionColumns() string {
	return `knowledge_object_version_id, knowledge_object_id, version_number,
	        storage_entry_id, source_hash, source_revision, size_bytes, mime_type,
	        file_class, source_path, source_created_at, source_modified_at,
	        recency_at, recency_basis, absolute_time_metadata, metadata,
	        observed_at, created_at`
}

func knowledgeChunkColumns() string {
	return `knowledge_chunk_id, knowledge_object_id, knowledge_object_version_id,
	        chunk_index, chunk_text, chunk_hash, structural_path, start_offset,
	        end_offset, token_count_estimate, chunker_version, status, metadata,
	        created_at, indexed_at`
}

func pipelineStatusColumns() string {
	return `knowledge_pipeline_status_id, knowledge_object_id,
	        knowledge_object_version_id, pipeline_key, pipeline_version, stage,
	        status, queued_at, started_at, completed_at, failed_at,
	        last_error_code, last_error_message, metadata, attempt_count,
	        next_attempt_at, claimed_by_worker_run_id, claim_expires_at,
	        priority, manual_action_required, last_worker_run_id,
	        last_attempt_at, created_at, updated_at`
}

func objectLinkColumns() string {
	return `knowledge_object_link_id, source_knowledge_object_id,
	        target_knowledge_object_id, chunk_id, link_kind, raw_target,
	        normalized_target, link_text, status, metadata, created_at, updated_at`
}

type knowledgeObjectVersionScanner interface {
	Scan(dest ...any) error
}

type knowledgeChunkScanner interface {
	Scan(dest ...any) error
}

type pipelineStatusScanner interface {
	Scan(dest ...any) error
}

type objectLinkScanner interface {
	Scan(dest ...any) error
}

func scanKnowledgeObjectVersion(scanner knowledgeObjectVersionScanner) (KnowledgeObjectVersion, error) {
	var version KnowledgeObjectVersion
	var storageEntryID sql.NullString
	var sizeBytes sql.NullInt64
	var sourceCreatedAt, sourceModifiedAt sql.NullTime
	var absoluteTimeMetadata, metadata []byte
	if err := scanner.Scan(
		&version.KnowledgeObjectVersionID,
		&version.KnowledgeObjectID,
		&version.VersionNumber,
		&storageEntryID,
		&version.SourceHash,
		&version.SourceRevision,
		&sizeBytes,
		&version.MimeType,
		&version.FileClass,
		&version.SourcePath,
		&sourceCreatedAt,
		&sourceModifiedAt,
		&version.RecencyAt,
		&version.RecencyBasis,
		&absoluteTimeMetadata,
		&metadata,
		&version.ObservedAt,
		&version.CreatedAt,
	); err != nil {
		return KnowledgeObjectVersion{}, err
	}
	version.StorageEntryID = nullStringPtr(storageEntryID)
	version.SizeBytes = nullInt64Ptr(sizeBytes)
	version.SourceCreatedAt = nullTimePtr(sourceCreatedAt)
	version.SourceModifiedAt = nullTimePtr(sourceModifiedAt)
	version.AbsoluteTimeMetadata = jsonObjectOrEmpty(absoluteTimeMetadata)
	version.Metadata = jsonObjectOrEmpty(metadata)
	return version, nil
}

func scanKnowledgeChunk(scanner knowledgeChunkScanner) (KnowledgeChunk, error) {
	var chunk KnowledgeChunk
	var versionID sql.NullString
	var startOffset, endOffset, tokenCount sql.NullInt64
	var indexedAt sql.NullTime
	var metadata []byte
	if err := scanner.Scan(
		&chunk.KnowledgeChunkID,
		&chunk.KnowledgeObjectID,
		&versionID,
		&chunk.ChunkIndex,
		&chunk.ChunkText,
		&chunk.ChunkHash,
		&chunk.StructuralPath,
		&startOffset,
		&endOffset,
		&tokenCount,
		&chunk.ChunkerVersion,
		&chunk.Status,
		&metadata,
		&chunk.CreatedAt,
		&indexedAt,
	); err != nil {
		return KnowledgeChunk{}, err
	}
	chunk.KnowledgeObjectVersionID = nullStringPtr(versionID)
	chunk.StartOffset = nullIntPtr(startOffset)
	chunk.EndOffset = nullIntPtr(endOffset)
	chunk.TokenCountEstimate = nullIntPtr(tokenCount)
	chunk.Metadata = jsonObjectOrEmpty(metadata)
	chunk.IndexedAt = nullTimePtr(indexedAt)
	return chunk, nil
}

func scanPipelineStatus(scanner pipelineStatusScanner) (PipelineStatus, error) {
	var status PipelineStatus
	var versionID, claimedByWorkerRunID, lastWorkerRunID sql.NullString
	var queuedAt, startedAt, completedAt, failedAt sql.NullTime
	var nextAttemptAt, claimExpiresAt, lastAttemptAt sql.NullTime
	var metadata []byte
	if err := scanner.Scan(
		&status.KnowledgePipelineStatusID,
		&status.KnowledgeObjectID,
		&versionID,
		&status.PipelineKey,
		&status.PipelineVersion,
		&status.Stage,
		&status.Status,
		&queuedAt,
		&startedAt,
		&completedAt,
		&failedAt,
		&status.LastErrorCode,
		&status.LastErrorMessage,
		&metadata,
		&status.AttemptCount,
		&nextAttemptAt,
		&claimedByWorkerRunID,
		&claimExpiresAt,
		&status.Priority,
		&status.ManualActionRequired,
		&lastWorkerRunID,
		&lastAttemptAt,
		&status.CreatedAt,
		&status.UpdatedAt,
	); err != nil {
		return PipelineStatus{}, err
	}
	status.KnowledgeObjectVersionID = nullStringPtr(versionID)
	status.QueuedAt = nullTimePtr(queuedAt)
	status.StartedAt = nullTimePtr(startedAt)
	status.CompletedAt = nullTimePtr(completedAt)
	status.FailedAt = nullTimePtr(failedAt)
	status.Metadata = jsonObjectOrEmpty(metadata)
	status.NextAttemptAt = nullTimePtr(nextAttemptAt)
	if claimedByWorkerRunID.Valid {
		status.ClaimedByWorkerRunID = strings.TrimSpace(claimedByWorkerRunID.String)
	}
	status.ClaimExpiresAt = nullTimePtr(claimExpiresAt)
	if lastWorkerRunID.Valid {
		status.LastWorkerRunID = strings.TrimSpace(lastWorkerRunID.String)
	}
	status.LastAttemptAt = nullTimePtr(lastAttemptAt)
	return status, nil
}

func scanObjectLink(scanner objectLinkScanner) (ObjectLink, error) {
	var link ObjectLink
	var targetID, chunkID sql.NullString
	var metadata []byte
	if err := scanner.Scan(
		&link.KnowledgeObjectLinkID,
		&link.SourceKnowledgeObjectID,
		&targetID,
		&chunkID,
		&link.LinkKind,
		&link.RawTarget,
		&link.NormalizedTarget,
		&link.LinkText,
		&link.Status,
		&metadata,
		&link.CreatedAt,
		&link.UpdatedAt,
	); err != nil {
		return ObjectLink{}, err
	}
	link.TargetKnowledgeObjectID = nullStringPtr(targetID)
	link.ChunkID = nullStringPtr(chunkID)
	link.Metadata = jsonObjectOrEmpty(metadata)
	return link, nil
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullIntPtr(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	out := int(value.Int64)
	return &out
}
