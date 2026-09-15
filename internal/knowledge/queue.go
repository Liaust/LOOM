package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	defaultKnowledgeClaimLimit         = 50
	maxKnowledgeClaimLimit             = 200
	defaultKnowledgeLeaseDuration      = 2 * time.Minute
	defaultKnowledgeRetryMaxAttempts   = 3
	defaultKnowledgeRetryBaseDelay     = time.Minute
	defaultKnowledgeRetryMaximumDelay  = time.Hour
	defaultKnowledgeMaxTextBytes       = int64(5 * 1024 * 1024)
	defaultKnowledgeMaxExtractedBytes  = int64(10 * 1024 * 1024)
	defaultKnowledgeMaxChunksPerObject = 1000
)

type KnowledgeWorkClaimOptions struct {
	Limit         int
	LeaseDuration time.Duration
	Now           time.Time
}

type KnowledgeWorkItem struct {
	PipelineStatus PipelineStatus  `json:"knowledge_pipeline_status"`
	Object         KnowledgeObject `json:"object"`
}

type KnowledgeWorkProcessOptions struct {
	MaxTextBytesPerObject      int64
	MaxExtractedBytesPerObject int64
	MaxChunksPerObject         int
}

type KnowledgeWorkProcessResult struct {
	KnowledgeObjectID string `json:"knowledge_object_id"`
	Status            string `json:"status"`
	ExtractionStatus  string `json:"extraction_status,omitempty"`
	TextBytesRead     int64  `json:"text_bytes_read"`
	ChunksCreated     int64  `json:"chunks_created"`
	LinksCreated      int64  `json:"links_created"`
	StatusesWritten   int64  `json:"statuses_written"`
	Retryable         bool   `json:"retryable"`
	LastErrorCode     string `json:"last_error_code,omitempty"`
	LastErrorMessage  string `json:"last_error_message,omitempty"`
}

type KnowledgeWorkFailure struct {
	ErrorCode    string
	ErrorMessage string
	Retryable    bool
	MaxAttempts  int
	BaseDelay    time.Duration
	MaxDelay     time.Duration
	WorkerRunID  string
	Now          time.Time
}

type KnowledgeIndexerRunInput struct {
	WorkerRunID           string        `json:"worker_run_id,omitempty"`
	Limit                 int           `json:"limit,omitempty"`
	LeaseDuration         time.Duration `json:"-"`
	MaxTextBytesPerObject int64         `json:"max_text_bytes_per_object,omitempty"`
	MaxExtractedTextBytes int64         `json:"max_extracted_text_bytes,omitempty"`
	MaxChunksPerObject    int           `json:"max_chunks_per_object,omitempty"`
	RetryMaxAttempts      int           `json:"retry_max_attempts,omitempty"`
	RetryBaseDelay        time.Duration `json:"-"`
	RetryMaximumDelay     time.Duration `json:"-"`
	Now                   time.Time     `json:"-"`
}

type KnowledgeIndexerRunResult struct {
	SchemaVersion         string `json:"schema_version"`
	Status                string `json:"status"`
	ReleasedExpired       int64  `json:"released_expired"`
	Claimed               int64  `json:"claimed"`
	Processed             int64  `json:"processed"`
	SkippedUnsupported    int64  `json:"skipped_unsupported"`
	Extracted             int64  `json:"extracted"`
	MetadataOnly          int64  `json:"metadata_only"`
	TooLarge              int64  `json:"too_large"`
	PasswordRequired      int64  `json:"password_required"`
	NoEmbeddedText        int64  `json:"no_embedded_text"`
	SourceUnavailable     int64  `json:"source_unavailable"`
	RetryScheduled        int64  `json:"retry_scheduled"`
	FailedTerminal        int64  `json:"failed_terminal"`
	TextBytesRead         int64  `json:"text_bytes_read"`
	ChunksCreated         int64  `json:"chunks_created"`
	LinksCreated          int64  `json:"links_created"`
	StatusesWritten       int64  `json:"statuses_written"`
	MoreWork              bool   `json:"more_work"`
	LastPipelineStatusID  string `json:"last_pipeline_status_id,omitempty"`
	LastKnowledgeObjectID string `json:"last_knowledge_object_id,omitempty"`
}

func (s *Service) RunKnowledgeIndexerOnce(ctx context.Context, input KnowledgeIndexerRunInput) (KnowledgeIndexerRunResult, error) {
	input = normalizeKnowledgeIndexerRunInput(input)
	now := input.Now
	if now.IsZero() {
		now = s.currentTime()
	}
	coordinated, err := s.RunPipelineCoordinatorOnce(ctx, PipelineCoordinatorRunInput{
		WorkerRunID: input.WorkerRunID, Limit: input.Limit, LeaseDuration: input.LeaseDuration,
		MaxTextBytesPerObject: input.MaxTextBytesPerObject, MaxExtractedTextBytes: input.MaxExtractedTextBytes,
		MaxChunksPerObject: input.MaxChunksPerObject, Now: now,
	})
	if err != nil {
		return KnowledgeIndexerRunResult{}, err
	}
	result := KnowledgeIndexerRunResult{
		SchemaVersion:         "knowledge_indexer.result.v1",
		Status:                "ok",
		ReleasedExpired:       coordinated.ReleasedExpired,
		Claimed:               coordinated.Claimed,
		Processed:             coordinated.Completed,
		FailedTerminal:        coordinated.Failed,
		MoreWork:              coordinated.MoreWork,
		LastPipelineStatusID:  coordinated.LastRunID,
		LastKnowledgeObjectID: coordinated.LastObjectID,
	}
	return result, nil
}

func (r *KnowledgeIndexerRunResult) incrementExtractionCounter(status string) {
	switch strings.TrimSpace(status) {
	case ExtractionStatusExtracted:
		r.Extracted++
	case ExtractionStatusMetadataOnly, ExtractionStatusUnsupportedBodyExtraction:
		r.MetadataOnly++
	case ExtractionStatusTooLarge:
		r.TooLarge++
	case ExtractionStatusPasswordRequired:
		r.PasswordRequired++
	case ExtractionStatusNoEmbeddedText, ExtractionStatusOCRDeferred:
		r.NoEmbeddedText++
	case ExtractionStatusSourceUnavailable:
		r.SourceUnavailable++
	}
}

func (s *Service) ReleaseExpiredKnowledgeClaims(ctx context.Context, now time.Time) (int, error) {
	if s == nil || s.store.db == nil {
		return 0, fmt.Errorf("knowledge store is not configured")
	}
	if now.IsZero() {
		now = s.currentTime()
	}
	result, err := s.store.db.ExecContext(ctx, `
		UPDATE knowledge.pipeline_statuses
		SET status = 'queued',
		    claimed_by_worker_run_id = null,
		    claim_expires_at = null,
		    updated_at = now()
		WHERE pipeline_key IN ($1, $2)
		  AND stage = $3
		  AND knowledge_object_version_id IS NULL
		  AND status = 'processing'
		  AND claimed_by_worker_run_id IS NOT NULL
		  AND claim_expires_at IS NOT NULL
		  AND claim_expires_at <= $4
	`, KnowledgeObjectPipelineNotesFileExtraction, KnowledgeObjectPipelineMarkdownText, PipelineStageTextExtraction, now)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}

func (s *Service) ClaimKnowledgeWork(ctx context.Context, workerRunID string, opts KnowledgeWorkClaimOptions) ([]KnowledgeWorkItem, error) {
	if s == nil || s.store.db == nil {
		return nil, fmt.Errorf("knowledge store is not configured")
	}
	workerRunID = strings.TrimSpace(workerRunID)
	if workerRunID == "" {
		return nil, fmt.Errorf("%w: worker_run_id is required", ErrInvalid)
	}
	if opts.Limit <= 0 || opts.Limit > maxKnowledgeClaimLimit {
		opts.Limit = defaultKnowledgeClaimLimit
	}
	if opts.LeaseDuration <= 0 {
		opts.LeaseDuration = defaultKnowledgeLeaseDuration
	}
	now := opts.Now
	if now.IsZero() {
		now = s.currentTime()
	}
	claimExpiresAt := now.Add(opts.LeaseDuration)

	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		WITH candidates AS (
			SELECT knowledge_pipeline_status_id AS candidate_status_id
			FROM knowledge.pipeline_statuses
			WHERE pipeline_key IN ($1, $2)
			  AND stage = $3
			  AND knowledge_object_version_id IS NULL
			  AND manual_action_required = false
			  AND (
				status IN ('queued', 'stale')
				OR (status = 'failed' AND (next_attempt_at IS NULL OR next_attempt_at <= $4))
			  )
			  AND (
				claimed_by_worker_run_id IS NULL
				OR claim_expires_at IS NULL
				OR claim_expires_at <= $4
			  )
			ORDER BY priority ASC, COALESCE(next_attempt_at, queued_at, updated_at) ASC, updated_at ASC
			LIMIT $5
			FOR UPDATE SKIP LOCKED
		)
		UPDATE knowledge.pipeline_statuses ps
		SET status = 'processing',
		    claimed_by_worker_run_id = $6,
		    claim_expires_at = $7,
		    last_worker_run_id = $6,
		    started_at = COALESCE(started_at, $4),
		    completed_at = null,
		    failed_at = null,
		    updated_at = now()
		FROM candidates
		WHERE ps.knowledge_pipeline_status_id = candidates.candidate_status_id
		RETURNING `+pipelineStatusColumns()+`
	`, KnowledgeObjectPipelineNotesFileExtraction, KnowledgeObjectPipelineMarkdownText, PipelineStageTextExtraction, now, opts.Limit, workerRunID, claimExpiresAt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	statuses := []PipelineStatus{}
	for rows.Next() {
		status, err := scanPipelineStatus(rows)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, status)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	items := make([]KnowledgeWorkItem, 0, len(statuses))
	for _, status := range statuses {
		object, err := s.store.GetKnowledgeObject(ctx, status.KnowledgeObjectID)
		if err != nil {
			return nil, err
		}
		items = append(items, KnowledgeWorkItem{PipelineStatus: status, Object: object})
	}
	return items, nil
}

func (s *Service) ProcessKnowledgeWork(ctx context.Context, item KnowledgeWorkItem, opts KnowledgeWorkProcessOptions) (KnowledgeWorkProcessResult, error) {
	result := KnowledgeWorkProcessResult{
		KnowledgeObjectID: item.Object.KnowledgeObjectID,
		Status:            PipelineStatusComplete,
	}
	if strings.TrimSpace(item.Object.SourcePath) != "" {
		if info, err := os.Stat(item.Object.SourcePath); err == nil {
			result.TextBytesRead = info.Size()
		}
	}
	pipelineResult, err := s.ProcessExtractionObject(ctx, TextPipelineInput{
		Object:                item.Object,
		MaxBytes:              opts.MaxTextBytesPerObject,
		MaxExtractedTextBytes: opts.MaxExtractedBytesPerObject,
		MaxChunks:             opts.MaxChunksPerObject,
	})
	if err != nil {
		result.Status = PipelineStatusFailed
		result.Retryable = RetryableKnowledgeError(err)
		result.LastErrorCode = knowledgeProcessingErrorCode(err)
		result.LastErrorMessage = err.Error()
		return result, err
	}
	result.ChunksCreated = int64(len(pipelineResult.Chunks))
	result.LinksCreated = int64(len(pipelineResult.Links))
	result.StatusesWritten = int64(len(pipelineResult.Statuses))
	result.ExtractionStatus = objectExtractionStatus(pipelineResult.Object.Metadata)
	return result, nil
}

func (s *Service) CompleteKnowledgeWork(ctx context.Context, item KnowledgeWorkItem, result KnowledgeWorkProcessResult, workerRunID string) (PipelineStatus, error) {
	finalStatus := strings.TrimSpace(result.Status)
	switch finalStatus {
	case PipelineStatusComplete, PipelineStatusDisabledByPolicy, PipelineStatusSkippedUnsupported:
	default:
		finalStatus = PipelineStatusComplete
	}
	row := s.store.db.QueryRowContext(ctx, `
		UPDATE knowledge.pipeline_statuses
		SET status = $2,
		    completed_at = CASE WHEN $2 = 'complete' THEN now() ELSE completed_at END,
		    failed_at = null,
		    last_error_code = '',
		    last_error_message = '',
		    next_attempt_at = null,
		    claimed_by_worker_run_id = null,
		    claim_expires_at = null,
		    manual_action_required = false,
		    last_worker_run_id = nullif($3, ''),
		    updated_at = now()
		WHERE knowledge_pipeline_status_id = $1
		RETURNING `+pipelineStatusColumns()+`
	`, item.PipelineStatus.KnowledgePipelineStatusID, finalStatus, strings.TrimSpace(workerRunID))
	return scanPipelineStatus(row)
}

func (s *Service) FailKnowledgeWork(ctx context.Context, item KnowledgeWorkItem, failure KnowledgeWorkFailure) (PipelineStatus, error) {
	now := failure.Now
	if now.IsZero() {
		now = s.currentTime()
	}
	maxAttempts := failure.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultKnowledgeRetryMaxAttempts
	}
	baseDelay := failure.BaseDelay
	if baseDelay <= 0 {
		baseDelay = defaultKnowledgeRetryBaseDelay
	}
	maxDelay := failure.MaxDelay
	if maxDelay <= 0 {
		maxDelay = defaultKnowledgeRetryMaximumDelay
	}
	nextAttemptCount := item.PipelineStatus.AttemptCount + 1
	manualAction := !failure.Retryable || nextAttemptCount >= maxAttempts
	var nextAttemptAt any
	if !manualAction {
		nextAttemptAt = now.Add(knowledgeRetryDelay(baseDelay, maxDelay, nextAttemptCount))
	}
	code := strings.TrimSpace(failure.ErrorCode)
	if code == "" {
		code = "knowledge_processing_failed"
	}
	message := strings.TrimSpace(failure.ErrorMessage)
	if message == "" {
		message = "knowledge processing failed"
	}
	row := s.store.db.QueryRowContext(ctx, `
		UPDATE knowledge.pipeline_statuses
		SET status = 'failed',
		    attempt_count = $2,
		    last_attempt_at = $3,
		    failed_at = $3,
		    last_error_code = $4,
		    last_error_message = $5,
		    next_attempt_at = $6,
		    manual_action_required = $7,
		    claimed_by_worker_run_id = null,
		    claim_expires_at = null,
		    last_worker_run_id = nullif($8, ''),
		    updated_at = now()
		WHERE knowledge_pipeline_status_id = $1
		RETURNING `+pipelineStatusColumns()+`
	`, item.PipelineStatus.KnowledgePipelineStatusID, nextAttemptCount, now, code, message, nextAttemptAt, manualAction, strings.TrimSpace(failure.WorkerRunID))
	status, err := scanPipelineStatus(row)
	if err != nil {
		return PipelineStatus{}, err
	}
	if _, updateErr := s.store.db.ExecContext(ctx, `
		UPDATE knowledge.knowledge_objects
		SET processing_state = 'failed',
		    last_error_code = $2,
		    last_error_message = $3,
		    updated_at = now()
		WHERE knowledge_object_id = $1
	`, item.Object.KnowledgeObjectID, code, message); updateErr != nil {
		return PipelineStatus{}, updateErr
	}
	return status, nil
}

func (s *Service) queueKnowledgeObjectTx(ctx context.Context, tx *sql.Tx, object KnowledgeObject, input ReprocessInput) (PipelineStatus, error) {
	now := s.currentTime()
	if _, err := tx.ExecContext(ctx, `
		UPDATE knowledge.knowledge_objects
		SET processing_state = 'stale',
		    updated_at = $2
		WHERE knowledge_object_id = $1
	`, object.KnowledgeObjectID, now); err != nil {
		return PipelineStatus{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE knowledge.pipeline_statuses
		SET status = 'stale',
		    updated_at = $4
		WHERE knowledge_object_id = $1
		  AND pipeline_key IN ($2, $3)
		  AND knowledge_object_version_id IS NOT NULL
		  AND status = 'complete'
		  AND (pipeline_version <> $5 OR $6)
	`, object.KnowledgeObjectID, input.PipelineKey, KnowledgeSearchIndexKey, now, KnowledgeFileExtractionPipelineVersion, input.Force); err != nil {
		return PipelineStatus{}, err
	}
	status, err := s.PreparePipelineStatus(PipelineStatus{
		KnowledgeObjectID: object.KnowledgeObjectID,
		PipelineKey:       input.PipelineKey,
		PipelineVersion:   KnowledgeFileExtractionPipelineVersion,
		Stage:             PipelineStageTextExtraction,
		Status:            PipelineStatusQueued,
		QueuedAt:          &now,
		Priority:          input.Priority,
		Metadata:          reprocessStatusMetadata(object, input),
	})
	if err != nil {
		return PipelineStatus{}, err
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
		VALUES ($1,$2,null,$3,$4,$5,$6,$7,null,null,null,
		        '', '', $8, 0, null, null, null, $9, false, null, null, $10, $11)
		ON CONFLICT (knowledge_object_id, (COALESCE(knowledge_object_version_id, '')), pipeline_key, stage)
		DO UPDATE
		SET pipeline_version = EXCLUDED.pipeline_version,
		    status = 'queued',
		    queued_at = EXCLUDED.queued_at,
		    started_at = null,
		    completed_at = null,
		    failed_at = null,
		    last_error_code = '',
		    last_error_message = '',
		    metadata = EXCLUDED.metadata,
		    attempt_count = 0,
		    next_attempt_at = null,
		    claimed_by_worker_run_id = null,
		    claim_expires_at = null,
		    priority = EXCLUDED.priority,
		    manual_action_required = false,
		    last_worker_run_id = null,
		    last_attempt_at = null,
		    updated_at = now()
		RETURNING `+pipelineStatusColumns()+`
	`,
		status.KnowledgePipelineStatusID,
		status.KnowledgeObjectID,
		status.PipelineKey,
		status.PipelineVersion,
		status.Stage,
		status.Status,
		nullableTime(status.QueuedAt),
		status.Metadata,
		status.Priority,
		status.CreatedAt,
		status.UpdatedAt,
	)
	queuedStatus, err := scanPipelineStatus(row)
	if err != nil {
		return PipelineStatus{}, err
	}
	if err := markEmbeddingObjectChangedTx(ctx, tx, object.KnowledgeObjectID, now); err != nil {
		return PipelineStatus{}, err
	}
	return queuedStatus, nil
}

func normalizeKnowledgeIndexerRunInput(input KnowledgeIndexerRunInput) KnowledgeIndexerRunInput {
	input.WorkerRunID = strings.TrimSpace(input.WorkerRunID)
	if input.Limit <= 0 {
		input.Limit = defaultKnowledgeClaimLimit
	} else if input.Limit > maxKnowledgeClaimLimit {
		input.Limit = maxKnowledgeClaimLimit
	}
	if input.LeaseDuration <= 0 {
		input.LeaseDuration = defaultKnowledgeLeaseDuration
	}
	if input.MaxTextBytesPerObject <= 0 {
		input.MaxTextBytesPerObject = defaultKnowledgeMaxTextBytes
	}
	if input.MaxExtractedTextBytes <= 0 {
		input.MaxExtractedTextBytes = defaultKnowledgeMaxExtractedBytes
	}
	if input.MaxChunksPerObject <= 0 {
		input.MaxChunksPerObject = defaultKnowledgeMaxChunksPerObject
	}
	if input.RetryMaxAttempts <= 0 {
		input.RetryMaxAttempts = defaultKnowledgeRetryMaxAttempts
	}
	if input.RetryBaseDelay <= 0 {
		input.RetryBaseDelay = defaultKnowledgeRetryBaseDelay
	}
	if input.RetryMaximumDelay <= 0 {
		input.RetryMaximumDelay = defaultKnowledgeRetryMaximumDelay
	}
	if input.RetryMaximumDelay < input.RetryBaseDelay {
		input.RetryMaximumDelay = input.RetryBaseDelay
	}
	return input
}

func RetryableKnowledgeError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrInvalid) || errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, terminal := range []string{
		"not valid utf-8",
		"source_path is required",
		"not supported by markdown text pipeline",
		"exceeds max_bytes",
		"exceeds max_chunks",
		"permission denied",
		"is a directory",
	} {
		if strings.Contains(message, terminal) {
			return false
		}
	}
	for _, retryable := range []string{
		"temporarily",
		"timeout",
		"connection",
		"deadlock",
		"serialization",
		"lock",
		"resource busy",
	} {
		if strings.Contains(message, retryable) {
			return true
		}
	}
	return true
}

func knowledgeProcessingErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, ErrInvalid) {
		return "invalid_knowledge_object"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "read source file"):
		return "source_read_failed"
	case strings.Contains(message, "stat source file"):
		return "source_stat_failed"
	case strings.Contains(message, "not valid utf-8"):
		return "invalid_utf8"
	case strings.Contains(message, "exceeds max"):
		return "limit_exceeded"
	default:
		return "knowledge_processing_failed"
	}
}

func knowledgeRetryDelay(baseDelay, maxDelay time.Duration, attempt int) time.Duration {
	if attempt <= 0 {
		attempt = 1
	}
	delay := baseDelay
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= maxDelay {
			return maxDelay
		}
	}
	if delay > maxDelay {
		return maxDelay
	}
	return delay
}

func knowledgeIndexerResultJSON(result KnowledgeIndexerRunResult) json.RawMessage {
	payload, err := json.Marshal(result)
	if err != nil {
		return json.RawMessage(`{"schema_version":"knowledge_indexer.result.v0.8","status":"marshal_failed"}`)
	}
	return payload
}
