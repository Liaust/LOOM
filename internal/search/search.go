package search

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/objectstore"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

const (
	ExtractorVersion = "plain_text_v1"
	ChunkerVersion   = "paragraph_chunk_v1"
	IndexVersion     = "full_text_v1"
	LanguageConfig   = "simple"

	maxChunkCharacters    = 8000
	targetChunkCharacters = 5000
)

type Service struct {
	DB    *sql.DB
	Store objectstore.Store
}

func NewService(db *sql.DB, store objectstore.Store) Service {
	return Service{DB: db, Store: store}
}

type SearchInput struct {
	Query      string `json:"query"`
	ProjectRef string `json:"project_ref,omitempty"`
	ScopeRef   string `json:"scope_ref,omitempty"`
	ObjectType string `json:"object_type,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type SearchResultSet struct {
	Query       string         `json:"query"`
	ResultCount int            `json:"result_count"`
	Results     []SearchResult `json:"results"`
}

type SearchResult struct {
	SearchDocumentID   string    `json:"search_document_id"`
	ResultKind         string    `json:"result_kind"`
	ObjectID           string    `json:"object_id,omitempty"`
	ObjectVersionID    string    `json:"object_version_id,omitempty"`
	DocumentChunkID    string    `json:"document_chunk_id,omitempty"`
	ScopeID            string    `json:"scope_id,omitempty"`
	SourceNodeID       string    `json:"source_node_id,omitempty"`
	Title              string    `json:"title,omitempty"`
	Snippet            string    `json:"snippet,omitempty"`
	RankScore          float64   `json:"rank_score"`
	LanguageConfig     string    `json:"language_config"`
	DataClassification string    `json:"data_classification"`
	TrustLevel         string    `json:"trust_level"`
	FreshnessState     string    `json:"freshness_state"`
	IndexedAt          time.Time `json:"indexed_at"`
	Citation           Citation  `json:"citation"`
}

type Citation struct {
	Label     string `json:"label"`
	SourceRef string `json:"source_ref"`
}

type StatusFilter struct {
	Limit      int
	ObjectRef  string
	ProjectRef string
	ScopeRef   string
	IndexType  string
	Status     string
	FailedOnly bool
}

type IndexQueueFilter struct {
	Limit         int
	ObjectRef     string
	ProjectRef    string
	ScopeRef      string
	Status        string
	IncludeActive bool
}

type IndexStatus struct {
	IndexStatusID    string          `json:"index_status_id"`
	SourceKind       string          `json:"source_kind"`
	SourceID         string          `json:"source_id"`
	SourceVersionID  string          `json:"source_version_id,omitempty"`
	ObjectID         string          `json:"object_id,omitempty"`
	ObjectVersionID  string          `json:"object_version_id,omitempty"`
	ScopeID          string          `json:"scope_id,omitempty"`
	IndexType        string          `json:"index_type"`
	Status           string          `json:"status"`
	IndexVersion     string          `json:"index_version"`
	ExtractorVersion string          `json:"extractor_version,omitempty"`
	ChunkerVersion   string          `json:"chunker_version,omitempty"`
	QueuedAt         *time.Time      `json:"queued_at,omitempty"`
	StartedAt        *time.Time      `json:"started_at,omitempty"`
	CompletedAt      *time.Time      `json:"completed_at,omitempty"`
	FailedAt         *time.Time      `json:"failed_at,omitempty"`
	LastErrorCode    string          `json:"last_error_code,omitempty"`
	LastErrorMessage string          `json:"last_error_message,omitempty"`
	Metadata         json.RawMessage `json:"metadata"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	AttemptCount     int             `json:"attempt_count"`
	NextAttemptAt    *time.Time      `json:"next_attempt_at,omitempty"`
	ClaimedByRunID   string          `json:"claimed_by_worker_run_id,omitempty"`
	ClaimExpiresAt   *time.Time      `json:"claim_expires_at,omitempty"`
	Priority         int             `json:"priority"`
	ManualAction     bool            `json:"manual_action_required"`
	LastWorkerRunID  string          `json:"last_worker_run_id,omitempty"`
	LastAttemptAt    *time.Time      `json:"last_attempt_at,omitempty"`
}

type IndexEnqueueInput struct {
	ObjectRef       string `json:"object_ref,omitempty"`
	ObjectID        string `json:"object_id,omitempty"`
	ObjectVersionID string `json:"object_version_id,omitempty"`
	Priority        int    `json:"priority,omitempty"`
	Force           bool   `json:"force,omitempty"`
}

type IndexEnqueueResult struct {
	ObjectID        string      `json:"object_id"`
	ObjectVersionID string      `json:"object_version_id"`
	Status          string      `json:"status"`
	Reason          string      `json:"reason,omitempty"`
	IndexStatus     IndexStatus `json:"index_status"`
}

type IndexExplainInput struct {
	ObjectRef string `json:"object_ref"`
}

type IndexExplainResult struct {
	ObjectRef       string        `json:"object_ref"`
	ObjectID        string        `json:"object_id"`
	ObjectVersionID string        `json:"object_version_id"`
	QueueStatus     string        `json:"queue_status,omitempty"`
	Statuses        []IndexStatus `json:"statuses"`
}

type IndexStatusRefInput struct {
	IndexStatusID string `json:"index_status_id"`
}

type IndexRetryFailedInput struct {
	Limit      int    `json:"limit,omitempty"`
	ObjectRef  string `json:"object_ref,omitempty"`
	ProjectRef string `json:"project_ref,omitempty"`
	ScopeRef   string `json:"scope_ref,omitempty"`
	IndexType  string `json:"index_type,omitempty"`
}

type IndexRetrySummary struct {
	Retried int           `json:"retried"`
	Skipped int           `json:"skipped"`
	Items   []IndexStatus `json:"items"`
}

type TextWorkClaimOptions struct {
	Limit         int
	LeaseDuration time.Duration
	Now           time.Time
}

type IndexWorkItem struct {
	IndexStatus IndexStatus `json:"index_status"`
	ObjectID    string      `json:"object_id"`
	VersionID   string      `json:"object_version_id"`
}

type TextWorkProcessOptions struct {
	MaxTextBytesPerObject int64
	MaxChunksPerObject    int
}

type TextWorkProcessResult struct {
	ObjectID             string `json:"object_id"`
	ObjectVersionID      string `json:"object_version_id"`
	Status               string `json:"status"`
	TextBytesRead        int64  `json:"text_bytes_read"`
	ChunksCreated        int64  `json:"chunks_created"`
	DocumentsCreated     int64  `json:"documents_created"`
	ExtractedTextID      string `json:"extracted_text_id,omitempty"`
	Retryable            bool   `json:"retryable"`
	ManualAction         bool   `json:"manual_action_required"`
	LastErrorCode        string `json:"last_error_code,omitempty"`
	LastErrorMessage     string `json:"last_error_message,omitempty"`
	AttemptCount         int    `json:"attempt_count,omitempty"`
	NextAttemptScheduled bool   `json:"next_attempt_scheduled,omitempty"`
}

type TextWorkFailure struct {
	ErrorCode    string
	ErrorMessage string
	Retryable    bool
	MaxAttempts  int
	BaseDelay    time.Duration
	MaxDelay     time.Duration
	WorkerRunID  string
	Now          time.Time
}

type indexLimits struct {
	MaxTextBytesPerObject int64
	MaxChunksPerObject    int
}

type RebuildInput struct {
	ObjectRef string `json:"object_ref"`
	Force     bool   `json:"force,omitempty"`
}

type IndexResult struct {
	ObjectID            string        `json:"object_id"`
	ObjectVersionID     string        `json:"object_version_id"`
	ExtractedTextID     string        `json:"extracted_text_id,omitempty"`
	ChunkCount          int           `json:"chunk_count"`
	SearchDocumentCount int           `json:"search_document_count"`
	Status              string        `json:"status"`
	Statuses            []IndexStatus `json:"statuses,omitempty"`
	EventIDs            []string      `json:"event_ids,omitempty"`
}

type indexTarget struct {
	ObjectID        string
	ObjectName      string
	ObjectType      string
	HomeScopeID     string
	ObjectVersionID string
	BlobID          string
	SourceNodeID    string
	MimeType        string
	StoragePath     string
	HashURI         string
	LogicalName     string
	Extension       string
	TextExtractable bool
	IndexPolicy     string
}

type chunkInput struct {
	Index          int
	Text           string
	StartOffset    int
	EndOffset      int
	StructuralPath string
	TokenEstimate  int
	HashURI        string
}

func (s Service) IndexObjectVersion(ctx context.Context, req requestctx.Context, objectID string, versionID string) (IndexResult, error) {
	return s.indexObjectVersionWithLimits(ctx, req, objectID, versionID, indexLimits{})
}

func (s Service) indexObjectVersionWithLimits(ctx context.Context, req requestctx.Context, objectID string, versionID string, limits indexLimits) (IndexResult, error) {
	target, err := s.resolveTarget(ctx, objectID, versionID)
	if err != nil {
		return IndexResult{}, err
	}

	if disabledByPolicy(target.IndexPolicy) {
		status, statusErr := s.recordStatus(ctx, target, "text_extraction", "disabled_by_policy", "", "")
		result := IndexResult{
			ObjectID:        target.ObjectID,
			ObjectVersionID: target.ObjectVersionID,
			Status:          "disabled_by_policy",
			Statuses:        []IndexStatus{status},
		}
		return result, statusErr
	}
	if !target.TextExtractable || !supportedTextTarget(target) {
		status, statusErr := s.recordStatus(ctx, target, "text_extraction", "skipped_unsupported", "", "")
		result := IndexResult{
			ObjectID:        target.ObjectID,
			ObjectVersionID: target.ObjectVersionID,
			Status:          "skipped_unsupported",
			Statuses:        []IndexStatus{status},
		}
		return result, statusErr
	}

	text, err := readExtractableTextWithLimit(target.StoragePath, limits.MaxTextBytesPerObject)
	if err != nil {
		result, recordErr := s.recordFailure(ctx, req, target, "text_extraction", events.TypeTextExtractionFailed, "text_extraction_failed", err)
		if recordErr != nil {
			return result, fmt.Errorf("%w; additionally failed to record index failure: %v", err, recordErr)
		}
		return result, err
	}
	chunks := chunkText(text)
	if limits.MaxChunksPerObject > 0 && len(chunks) > limits.MaxChunksPerObject {
		err := fmt.Errorf("text chunk count %d exceeds max_chunks_per_object %d", len(chunks), limits.MaxChunksPerObject)
		result, recordErr := s.recordFailure(ctx, req, target, "full_text", events.TypeSearchIndexFailed, "search_index_failed", err)
		if recordErr != nil {
			return result, fmt.Errorf("%w; additionally failed to record index failure: %v", err, recordErr)
		}
		return result, err
	}

	result, err := s.writeIndex(ctx, req, target, text, chunks)
	if err != nil {
		failureResult, recordErr := s.recordFailure(ctx, req, target, "full_text", events.TypeSearchIndexFailed, "search_index_failed", err)
		if recordErr != nil {
			return failureResult, fmt.Errorf("%w; additionally failed to record index failure: %v", err, recordErr)
		}
		return failureResult, err
	}
	return result, nil
}

func (s Service) RebuildObject(ctx context.Context, req requestctx.Context, objectRef string) (IndexResult, error) {
	objectID, versionID, err := s.resolveLatestVersionRef(ctx, objectRef)
	if err != nil {
		return IndexResult{}, err
	}
	enqueue, err := s.EnqueueObjectVersion(ctx, req, IndexEnqueueInput{
		ObjectID:        objectID,
		ObjectVersionID: versionID,
		Force:           true,
	})
	if err != nil {
		return IndexResult{}, err
	}
	return IndexResult{
		ObjectID:        enqueue.ObjectID,
		ObjectVersionID: enqueue.ObjectVersionID,
		Status:          enqueue.Status,
		Statuses:        []IndexStatus{enqueue.IndexStatus},
	}, nil
}

func (s Service) EnqueueObjectVersion(ctx context.Context, req requestctx.Context, input IndexEnqueueInput) (IndexEnqueueResult, error) {
	objectID := strings.TrimSpace(input.ObjectID)
	versionID := strings.TrimSpace(input.ObjectVersionID)
	if strings.TrimSpace(input.ObjectRef) != "" {
		resolvedObjectID, resolvedVersionID, err := s.resolveLatestVersionRef(ctx, input.ObjectRef)
		if err != nil {
			return IndexEnqueueResult{}, fmt.Errorf("resolve object ref: %w", err)
		}
		objectID = resolvedObjectID
		versionID = resolvedVersionID
	}
	if objectID == "" || versionID == "" {
		return IndexEnqueueResult{}, fmt.Errorf("object_id and object_version_id are required")
	}

	target, err := s.resolveTarget(ctx, objectID, versionID)
	if err != nil {
		return IndexEnqueueResult{}, err
	}
	_ = req // reserved for enqueue events once the worker pipeline owns execution.

	if disabledByPolicy(target.IndexPolicy) {
		status, statusErr := s.recordStatus(ctx, target, "full_text", "disabled_by_policy", "", "")
		return IndexEnqueueResult{
			ObjectID:        target.ObjectID,
			ObjectVersionID: target.ObjectVersionID,
			Status:          "disabled_by_policy",
			Reason:          "indexing disabled by policy",
			IndexStatus:     status,
		}, statusErr
	}
	if !target.TextExtractable || !supportedTextTarget(target) {
		status, statusErr := s.recordStatus(ctx, target, "full_text", "skipped_unsupported", "", "")
		return IndexEnqueueResult{
			ObjectID:        target.ObjectID,
			ObjectVersionID: target.ObjectVersionID,
			Status:          "skipped_unsupported",
			Reason:          "object version is not a supported text target",
			IndexStatus:     status,
		}, statusErr
	}

	if !input.Force {
		status, found, err := s.getFullTextStatus(ctx, target.ObjectVersionID)
		if err != nil {
			return IndexEnqueueResult{}, err
		}
		if found && status.Status == "indexed" {
			return IndexEnqueueResult{
				ObjectID:        target.ObjectID,
				ObjectVersionID: target.ObjectVersionID,
				Status:          "already_indexed",
				Reason:          "full text index already exists",
				IndexStatus:     status,
			}, nil
		}
		if found && queueLikeStatus(status.Status) && !status.ManualAction {
			return IndexEnqueueResult{
				ObjectID:        target.ObjectID,
				ObjectVersionID: target.ObjectVersionID,
				Status:          status.Status,
				Reason:          "index work already queued",
				IndexStatus:     status,
			}, nil
		}
	}

	priority := input.Priority
	if priority <= 0 {
		priority = 100
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return IndexEnqueueResult{}, err
	}
	defer tx.Rollback()

	status, err := upsertStatusTx(ctx, tx, target, "full_text", "queued", "", "")
	if err != nil {
		return IndexEnqueueResult{}, err
	}
	row := tx.QueryRowContext(ctx, `
		UPDATE search.index_status
		SET queued_at = COALESCE(queued_at, now()),
		    started_at = null,
		    completed_at = null,
		    failed_at = null,
		    last_error_code = null,
		    last_error_message = null,
		    next_attempt_at = null,
		    claimed_by_worker_run_id = null,
		    claim_expires_at = null,
		    priority = $2,
		    manual_action_required = false,
		    updated_at = now()
		WHERE index_status_id = $1
		RETURNING `+indexStatusColumns()+`
	`, status.IndexStatusID, priority)
	status, err = scanIndexStatus(row)
	if err != nil {
		return IndexEnqueueResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return IndexEnqueueResult{}, err
	}
	return IndexEnqueueResult{
		ObjectID:        target.ObjectID,
		ObjectVersionID: target.ObjectVersionID,
		Status:          "queued",
		IndexStatus:     status,
	}, nil
}

func (s Service) Search(ctx context.Context, req requestctx.Context, input SearchInput) (SearchResultSet, error) {
	queryText := strings.TrimSpace(input.Query)
	if queryText == "" {
		return SearchResultSet{}, fmt.Errorf("search query is required")
	}
	if input.Limit <= 0 || input.Limit > 50 {
		input.Limit = 10
	}

	scopeID, err := s.resolveOptionalScope(ctx, input.ProjectRef, input.ScopeRef)
	if err != nil {
		return SearchResultSet{}, err
	}

	sqlText := `
		SELECT sd.search_document_id, sd.source_kind, COALESCE(sd.object_id, ''),
		       COALESCE(sd.object_version_id, ''), COALESCE(sd.document_chunk_id, ''),
		       COALESCE(sd.scope_id, ''), COALESCE(sd.source_node_id, ''),
		       COALESCE(sd.title, ''),
		       ts_headline('simple', sd.body, websearch_to_tsquery('simple', $1),
		                   'MaxWords=32, MinWords=8, ShortWord=3') AS snippet,
		       ts_rank_cd(sd.tsv, websearch_to_tsquery('simple', $1)) AS rank_score,
		       sd.language_config, sd.data_classification, sd.trust_level,
		       sd.freshness_state, sd.indexed_at
		FROM search.search_documents sd
		LEFT JOIN objects.objects o ON o.object_id = sd.object_id
		WHERE sd.tsv @@ websearch_to_tsquery('simple', $1)
	`
	args := []any{queryText}
	add := func(condition string, value any) {
		args = append(args, value)
		sqlText += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if scopeID != "" {
		add("sd.scope_id =", scopeID)
	}
	if strings.TrimSpace(input.ObjectType) != "" {
		add("o.object_type =", strings.TrimSpace(input.ObjectType))
	}
	args = append(args, input.Limit)
	sqlText += fmt.Sprintf(" ORDER BY rank_score DESC, sd.indexed_at DESC LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return SearchResultSet{}, err
	}
	defer rows.Close()

	results := []SearchResult{}
	for rows.Next() {
		result, err := scanSearchResult(rows)
		if err != nil {
			return SearchResultSet{}, err
		}
		result.Citation = buildCitation(result)
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return SearchResultSet{}, err
	}

	_ = req // reserved for actor-scoped visibility in the next search slice.
	return SearchResultSet{
		Query:       queryText,
		ResultCount: len(results),
		Results:     results,
	}, nil
}

func (s Service) ListIndexStatus(ctx context.Context, filter StatusFilter) ([]IndexStatus, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}

	query := indexStatusSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}

	if strings.TrimSpace(filter.ObjectRef) != "" {
		objectID, err := s.resolveObjectID(ctx, filter.ObjectRef)
		if err != nil {
			return nil, fmt.Errorf("resolve object filter: %w", err)
		}
		add("object_id =", objectID)
	}
	if strings.TrimSpace(filter.ProjectRef) != "" || strings.TrimSpace(filter.ScopeRef) != "" {
		scopeID, err := s.resolveOptionalScope(ctx, filter.ProjectRef, filter.ScopeRef)
		if err != nil {
			return nil, err
		}
		if scopeID != "" {
			add("scope_id =", scopeID)
		}
	}
	if strings.TrimSpace(filter.IndexType) != "" {
		add("index_type =", strings.TrimSpace(filter.IndexType))
	}
	if strings.TrimSpace(filter.Status) != "" {
		add("status =", strings.TrimSpace(filter.Status))
	} else if filter.FailedOnly {
		add("status =", "failed")
	}

	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY updated_at DESC, created_at DESC LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	statuses := []IndexStatus{}
	for rows.Next() {
		status, err := scanIndexStatus(rows)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, status)
	}
	return statuses, rows.Err()
}

func (s Service) ListIndexQueue(ctx context.Context, filter IndexQueueFilter) ([]IndexStatus, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}

	query := indexStatusSelectSQL() + ` WHERE index_type = 'full_text'`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}

	if strings.TrimSpace(filter.ObjectRef) != "" {
		objectID, err := s.resolveObjectID(ctx, filter.ObjectRef)
		if err != nil {
			return nil, fmt.Errorf("resolve object filter: %w", err)
		}
		add("object_id =", objectID)
	}
	if strings.TrimSpace(filter.ProjectRef) != "" || strings.TrimSpace(filter.ScopeRef) != "" {
		scopeID, err := s.resolveOptionalScope(ctx, filter.ProjectRef, filter.ScopeRef)
		if err != nil {
			return nil, err
		}
		if scopeID != "" {
			add("scope_id =", scopeID)
		}
	}
	if strings.TrimSpace(filter.Status) != "" {
		add("status =", strings.TrimSpace(filter.Status))
	} else if filter.IncludeActive {
		query += ` AND (
			status IN ('queued', 'stale', 'rebuilding', 'extracting', 'chunking', 'indexing')
			OR (status = 'failed' AND manual_action_required = false)
		)`
	} else {
		query += ` AND (
			status IN ('queued', 'stale', 'rebuilding')
			OR (status = 'failed' AND manual_action_required = false)
		)`
	}

	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY priority ASC, COALESCE(next_attempt_at, queued_at, updated_at) ASC, updated_at ASC LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	statuses := []IndexStatus{}
	for rows.Next() {
		status, err := scanIndexStatus(rows)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, status)
	}
	return statuses, rows.Err()
}

func (s Service) ListIndexFailures(ctx context.Context, filter StatusFilter) ([]IndexStatus, error) {
	filter.FailedOnly = true
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}

	query := indexStatusSelectSQL() + ` WHERE (status = 'failed' OR manual_action_required = true)`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}

	if strings.TrimSpace(filter.ObjectRef) != "" {
		objectID, err := s.resolveObjectID(ctx, filter.ObjectRef)
		if err != nil {
			return nil, fmt.Errorf("resolve object filter: %w", err)
		}
		add("object_id =", objectID)
	}
	if strings.TrimSpace(filter.ProjectRef) != "" || strings.TrimSpace(filter.ScopeRef) != "" {
		scopeID, err := s.resolveOptionalScope(ctx, filter.ProjectRef, filter.ScopeRef)
		if err != nil {
			return nil, err
		}
		if scopeID != "" {
			add("scope_id =", scopeID)
		}
	}
	if strings.TrimSpace(filter.IndexType) != "" {
		add("index_type =", strings.TrimSpace(filter.IndexType))
	}

	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY COALESCE(failed_at, updated_at) DESC, updated_at DESC LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	statuses := []IndexStatus{}
	for rows.Next() {
		status, err := scanIndexStatus(rows)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, status)
	}
	return statuses, rows.Err()
}

func (s Service) ExplainObjectIndex(ctx context.Context, input IndexExplainInput) (IndexExplainResult, error) {
	objectID, versionID, err := s.resolveLatestVersionRef(ctx, input.ObjectRef)
	if err != nil {
		return IndexExplainResult{}, err
	}
	statuses, err := s.ListIndexStatus(ctx, StatusFilter{
		Limit:     50,
		ObjectRef: objectID,
	})
	if err != nil {
		return IndexExplainResult{}, err
	}

	result := IndexExplainResult{
		ObjectRef:       input.ObjectRef,
		ObjectID:        objectID,
		ObjectVersionID: versionID,
		Statuses:        []IndexStatus{},
	}
	for _, status := range statuses {
		if status.ObjectVersionID != versionID {
			continue
		}
		result.Statuses = append(result.Statuses, status)
		if status.IndexType == "full_text" {
			result.QueueStatus = status.Status
		}
	}
	return result, nil
}

func (s Service) GetIndexStatus(ctx context.Context, indexStatusID string) (IndexStatus, error) {
	indexStatusID = strings.TrimSpace(indexStatusID)
	if indexStatusID == "" {
		return IndexStatus{}, fmt.Errorf("index_status_id is required")
	}
	row := s.DB.QueryRowContext(ctx, indexStatusSelectSQL()+`
		WHERE index_status_id = $1
	`, indexStatusID)
	status, err := scanIndexStatus(row)
	if err != nil {
		return IndexStatus{}, err
	}
	return status, nil
}

func (s Service) RetryIndexWork(ctx context.Context, req requestctx.Context, input IndexStatusRefInput) (IndexStatus, error) {
	indexStatusID := strings.TrimSpace(input.IndexStatusID)
	if indexStatusID == "" {
		return IndexStatus{}, fmt.Errorf("index_status_id is required")
	}
	_ = req // reserved for retry audit event in a later operator slice.

	row := s.DB.QueryRowContext(ctx, `
		UPDATE search.index_status
		SET status = 'queued',
		    queued_at = now(),
		    started_at = null,
		    completed_at = null,
		    failed_at = null,
		    last_error_code = null,
		    last_error_message = null,
		    next_attempt_at = null,
		    claimed_by_worker_run_id = null,
		    claim_expires_at = null,
		    manual_action_required = false,
		    updated_at = now()
		WHERE index_status_id = $1
		  AND index_type = 'full_text'
		RETURNING `+indexStatusColumns()+`
	`, indexStatusID)
	status, err := scanIndexStatus(row)
	if err != nil {
		return IndexStatus{}, err
	}
	return status, nil
}

func (s Service) RetryFailedIndexWork(ctx context.Context, req requestctx.Context, input IndexRetryFailedInput) (IndexRetrySummary, error) {
	limit := input.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	indexType := strings.TrimSpace(input.IndexType)
	if indexType == "" {
		indexType = "full_text"
	}
	failures, err := s.ListIndexFailures(ctx, StatusFilter{
		Limit:      limit,
		ObjectRef:  input.ObjectRef,
		ProjectRef: input.ProjectRef,
		ScopeRef:   input.ScopeRef,
		IndexType:  indexType,
	})
	if err != nil {
		return IndexRetrySummary{}, err
	}
	summary := IndexRetrySummary{Items: []IndexStatus{}}
	for _, failure := range failures {
		if failure.Status != "failed" && !failure.ManualAction {
			summary.Skipped++
			continue
		}
		status, err := s.RetryIndexWork(ctx, req, IndexStatusRefInput{IndexStatusID: failure.IndexStatusID})
		if err != nil {
			return summary, err
		}
		summary.Retried++
		summary.Items = append(summary.Items, status)
	}
	return summary, nil
}

func (s Service) ReleaseExpiredTextClaims(ctx context.Context, now time.Time) (int, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	result, err := s.DB.ExecContext(ctx, `
		UPDATE search.index_status
		SET status = 'queued',
		    claimed_by_worker_run_id = null,
		    claim_expires_at = null,
		    updated_at = now()
		WHERE index_type = 'full_text'
		  AND claimed_by_worker_run_id IS NOT NULL
		  AND claim_expires_at IS NOT NULL
		  AND claim_expires_at <= $1
		  AND status IN ('extracting', 'chunking', 'indexing', 'rebuilding')
	`, now)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}

func (s Service) ClaimTextWork(ctx context.Context, workerRunID string, opts TextWorkClaimOptions) ([]IndexWorkItem, error) {
	workerRunID = strings.TrimSpace(workerRunID)
	if workerRunID == "" {
		return nil, fmt.Errorf("worker_run_id is required")
	}
	if opts.Limit <= 0 || opts.Limit > 200 {
		opts.Limit = 50
	}
	if opts.LeaseDuration <= 0 {
		opts.LeaseDuration = 2 * time.Minute
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	claimExpiresAt := now.Add(opts.LeaseDuration)

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		WITH candidates AS (
			SELECT index_status_id AS candidate_index_status_id
			FROM search.index_status
			WHERE index_type = 'full_text'
			  AND manual_action_required = false
			  AND (
			    status IN ('queued', 'stale', 'rebuilding')
			    OR (status = 'failed' AND (next_attempt_at IS NULL OR next_attempt_at <= $2))
			  )
			  AND (
			    claimed_by_worker_run_id IS NULL
			    OR claim_expires_at IS NULL
			    OR claim_expires_at <= $2
			  )
			ORDER BY priority ASC, COALESCE(next_attempt_at, queued_at, updated_at) ASC, updated_at ASC
			LIMIT $3
			FOR UPDATE SKIP LOCKED
		)
		UPDATE search.index_status s
		SET status = 'indexing',
		    claimed_by_worker_run_id = $1,
		    claim_expires_at = $4,
		    last_worker_run_id = $1,
		    started_at = COALESCE(started_at, $2),
		    completed_at = null,
		    failed_at = null,
		    updated_at = now()
		FROM candidates
		WHERE s.index_status_id = candidates.candidate_index_status_id
		RETURNING `+indexStatusColumns()+`
	`, workerRunID, now, opts.Limit, claimExpiresAt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []IndexWorkItem{}
	for rows.Next() {
		status, err := scanIndexStatus(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, IndexWorkItem{
			IndexStatus: status,
			ObjectID:    status.ObjectID,
			VersionID:   status.ObjectVersionID,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s Service) ProcessTextWork(ctx context.Context, req requestctx.Context, item IndexWorkItem, opts TextWorkProcessOptions) (TextWorkProcessResult, error) {
	if strings.TrimSpace(item.ObjectID) == "" || strings.TrimSpace(item.VersionID) == "" {
		return TextWorkProcessResult{}, fmt.Errorf("index work item is missing object or version reference")
	}
	textBytes := int64(0)
	target, targetErr := s.resolveTarget(ctx, item.ObjectID, item.VersionID)
	if targetErr == nil && strings.TrimSpace(target.StoragePath) != "" {
		if info, err := os.Stat(target.StoragePath); err == nil {
			textBytes = info.Size()
		}
	}

	result, err := s.indexObjectVersionWithLimits(ctx, req, item.ObjectID, item.VersionID, indexLimits{
		MaxTextBytesPerObject: opts.MaxTextBytesPerObject,
		MaxChunksPerObject:    opts.MaxChunksPerObject,
	})
	processResult := TextWorkProcessResult{
		ObjectID:         item.ObjectID,
		ObjectVersionID:  item.VersionID,
		TextBytesRead:    textBytes,
		Retryable:        false,
		ChunksCreated:    int64(result.ChunkCount),
		DocumentsCreated: int64(result.SearchDocumentCount),
		ExtractedTextID:  result.ExtractedTextID,
		Status:           result.Status,
	}
	if err != nil {
		processResult.Status = "failed"
		processResult.Retryable = RetryableIndexError(err)
		processResult.LastErrorCode = indexErrorCode(err)
		processResult.LastErrorMessage = err.Error()
		return processResult, err
	}
	if processResult.Status == "" {
		processResult.Status = "indexed"
	}
	return processResult, nil
}

func (s Service) CompleteTextWork(ctx context.Context, item IndexWorkItem, result TextWorkProcessResult, workerRunID string) (IndexStatus, error) {
	finalStatus := strings.TrimSpace(result.Status)
	if finalStatus == "" {
		finalStatus = "indexed"
	}
	switch finalStatus {
	case "indexed", "disabled_by_policy", "skipped_unsupported":
	default:
		finalStatus = "indexed"
	}
	row := s.DB.QueryRowContext(ctx, `
		UPDATE search.index_status
		SET status = $2,
		    completed_at = CASE WHEN $2 = 'indexed' THEN now() ELSE completed_at END,
		    failed_at = null,
		    last_error_code = null,
		    last_error_message = null,
		    next_attempt_at = null,
		    claimed_by_worker_run_id = null,
		    claim_expires_at = null,
		    manual_action_required = false,
		    last_worker_run_id = nullif($3, ''),
		    updated_at = now()
		WHERE index_status_id = $1
		RETURNING `+indexStatusColumns()+`
	`, item.IndexStatus.IndexStatusID, finalStatus, strings.TrimSpace(workerRunID))
	return scanIndexStatus(row)
}

func (s Service) FailTextWork(ctx context.Context, item IndexWorkItem, failure TextWorkFailure) (IndexStatus, error) {
	now := failure.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	maxAttempts := failure.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	baseDelay := failure.BaseDelay
	if baseDelay <= 0 {
		baseDelay = time.Minute
	}
	maxDelay := failure.MaxDelay
	if maxDelay <= 0 {
		maxDelay = time.Hour
	}

	currentAttempts := item.IndexStatus.AttemptCount
	nextAttemptCount := currentAttempts + 1
	manualAction := !failure.Retryable || nextAttemptCount >= maxAttempts
	var nextAttemptAt any
	if !manualAction {
		delay := retryDelay(baseDelay, maxDelay, nextAttemptCount)
		nextAttemptAt = now.Add(delay)
	}
	code := strings.TrimSpace(failure.ErrorCode)
	if code == "" {
		code = "indexing_failed"
	}
	message := strings.TrimSpace(failure.ErrorMessage)
	if message == "" {
		message = "indexing failed"
	}

	row := s.DB.QueryRowContext(ctx, `
		UPDATE search.index_status
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
		WHERE index_status_id = $1
		RETURNING `+indexStatusColumns()+`
	`, item.IndexStatus.IndexStatusID, nextAttemptCount, now, code, message, nextAttemptAt, manualAction, strings.TrimSpace(failure.WorkerRunID))
	return scanIndexStatus(row)
}

func (s Service) writeIndex(ctx context.Context, req requestctx.Context, target indexTarget, text string, chunks []chunkInput) (IndexResult, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return IndexResult{}, err
	}
	defer tx.Rollback()

	extractedTextID, err := upsertExtractedTextTx(ctx, tx, target, text)
	if err != nil {
		return IndexResult{}, err
	}
	if err := deletePreviousChunkIndexTx(ctx, tx, target.ObjectVersionID, extractedTextID); err != nil {
		return IndexResult{}, err
	}

	statuses := []IndexStatus{}
	textStatus, err := upsertStatusTx(ctx, tx, target, "text_extraction", "indexed", "", "")
	if err != nil {
		return IndexResult{}, err
	}
	statuses = append(statuses, textStatus)
	chunkStatus, err := upsertStatusTx(ctx, tx, target, "chunking", "indexed", "", "")
	if err != nil {
		return IndexResult{}, err
	}
	statuses = append(statuses, chunkStatus)

	searchDocumentCount := 0
	for _, chunk := range chunks {
		chunkID, err := insertChunkTx(ctx, tx, target, extractedTextID, chunk)
		if err != nil {
			return IndexResult{}, err
		}
		if _, err := upsertSearchDocumentTx(ctx, tx, target, chunkID, chunk); err != nil {
			return IndexResult{}, err
		}
		searchDocumentCount++
	}

	fullTextStatus, err := upsertStatusTx(ctx, tx, target, "full_text", "indexed", "", "")
	if err != nil {
		return IndexResult{}, err
	}
	statuses = append(statuses, fullTextStatus)

	eventIDs := []string{}
	for _, input := range []events.AppendInput{
		{
			EventType:  events.TypeTextExtracted,
			EventLevel: "node_activity",
			Request:    req,
			ScopeID:    target.HomeScopeID,
			TargetKind: "object",
			TargetID:   target.ObjectID,
			Status:     "indexed",
			Result:     "ok",
			Payload: map[string]any{
				"object_id":         target.ObjectID,
				"object_version_id": target.ObjectVersionID,
				"extracted_text_id": extractedTextID,
				"text_hash":         hashText(text),
				"extractor_version": ExtractorVersion,
			},
			VisibilityClass: "internal",
		},
		{
			EventType:  events.TypeChunksCreated,
			EventLevel: "node_activity",
			Request:    req,
			ScopeID:    target.HomeScopeID,
			TargetKind: "object",
			TargetID:   target.ObjectID,
			Status:     "indexed",
			Result:     "ok",
			Payload: map[string]any{
				"object_id":         target.ObjectID,
				"object_version_id": target.ObjectVersionID,
				"chunk_count":       len(chunks),
				"chunker_version":   ChunkerVersion,
			},
			VisibilityClass: "internal",
		},
		{
			EventType:  events.TypeSearchIndexed,
			EventLevel: "node_activity",
			Request:    req,
			ScopeID:    target.HomeScopeID,
			TargetKind: "object",
			TargetID:   target.ObjectID,
			Status:     "indexed",
			Result:     "ok",
			Payload: map[string]any{
				"object_id":             target.ObjectID,
				"object_version_id":     target.ObjectVersionID,
				"search_document_count": searchDocumentCount,
				"index_version":         IndexVersion,
				"language_config":       LanguageConfig,
			},
			VisibilityClass: "internal",
		},
	} {
		event, err := events.AppendTx(ctx, tx, input)
		if err != nil {
			return IndexResult{}, err
		}
		eventIDs = append(eventIDs, event.EventID)
	}

	if err := tx.Commit(); err != nil {
		return IndexResult{}, err
	}
	return IndexResult{
		ObjectID:            target.ObjectID,
		ObjectVersionID:     target.ObjectVersionID,
		ExtractedTextID:     extractedTextID,
		ChunkCount:          len(chunks),
		SearchDocumentCount: searchDocumentCount,
		Status:              "indexed",
		Statuses:            statuses,
		EventIDs:            eventIDs,
	}, nil
}

func upsertExtractedTextTx(ctx context.Context, tx *sql.Tx, target indexTarget, text string) (string, error) {
	extractedTextID := ids.NewExtractedTextID()
	err := tx.QueryRowContext(ctx, `
		INSERT INTO search.extracted_text (
			extracted_text_id, object_id, object_version_id, blob_id, source_node_id,
			scope_id, extraction_method, extractor_version, status, text_content,
			text_hash, language, data_classification, trust_level, index_policy,
			extracted_at, metadata
		)
		VALUES ($1, $2, $3, nullif($4::text, ''), nullif($5::text, ''), nullif($6::text, ''),
		        'plain_text_read', $7, 'extracted', $8::text, $9::text, 'unknown',
		        'internal', 'trusted_system', $10, now(), '{"slice":"4","stage":"text_extraction"}'::jsonb)
		ON CONFLICT (object_version_id, extractor_version)
		DO UPDATE SET
			status = 'extracted',
			text_content = EXCLUDED.text_content,
			text_hash = EXCLUDED.text_hash,
			index_policy = EXCLUDED.index_policy,
			extracted_at = now(),
			updated_at = now(),
			metadata = EXCLUDED.metadata
		RETURNING extracted_text_id
	`, extractedTextID, target.ObjectID, target.ObjectVersionID, target.BlobID, target.SourceNodeID, target.HomeScopeID,
		ExtractorVersion, text, hashText(text), target.IndexPolicy).Scan(&extractedTextID)
	return extractedTextID, err
}

func deletePreviousChunkIndexTx(ctx context.Context, tx *sql.Tx, objectVersionID, extractedTextID string) error {
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM search.search_documents
		WHERE object_version_id = $1 AND index_version = $2
	`, objectVersionID, IndexVersion); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		DELETE FROM search.document_chunks
		WHERE extracted_text_id = $1
	`, extractedTextID)
	return err
}

func insertChunkTx(ctx context.Context, tx *sql.Tx, target indexTarget, extractedTextID string, chunk chunkInput) (string, error) {
	chunkID := ids.NewDocumentChunkID()
	err := tx.QueryRowContext(ctx, `
		INSERT INTO search.document_chunks (
			document_chunk_id, object_id, object_version_id, extracted_text_id,
			source_node_id, scope_id, chunk_index, chunk_text, start_offset,
			end_offset, structural_path, token_count_estimate, chunk_hash,
			chunker_version, status, data_classification, trust_level,
			indexed_at, metadata
		)
		VALUES ($1, $2, $3, $4, nullif($5::text, ''), nullif($6::text, ''), $7, $8::text,
		        $9, $10, nullif($11::text, ''), $12, $13, $14, 'indexed',
		        'internal', 'trusted_system', now(), '{"slice":"4","stage":"chunking"}'::jsonb)
		RETURNING document_chunk_id
	`, chunkID, target.ObjectID, target.ObjectVersionID, extractedTextID, target.SourceNodeID, target.HomeScopeID,
		chunk.Index, chunk.Text, nullableOffset(chunk.StartOffset), nullableOffset(chunk.EndOffset), chunk.StructuralPath,
		chunk.TokenEstimate, chunk.HashURI, ChunkerVersion).Scan(&chunkID)
	return chunkID, err
}

func upsertSearchDocumentTx(ctx context.Context, tx *sql.Tx, target indexTarget, chunkID string, chunk chunkInput) (string, error) {
	searchDocumentID := ids.NewSearchDocumentID()
	title := target.LogicalName
	if strings.TrimSpace(title) == "" {
		title = target.ObjectName
	}
	summary := chunk.StructuralPath
	err := tx.QueryRowContext(ctx, `
		INSERT INTO search.search_documents (
			search_document_id, source_kind, source_id, source_version_id, object_id,
			object_version_id, document_chunk_id, source_node_id, scope_id,
			title, summary, body, body_hash, tsv, language_config,
			data_classification, trust_level, freshness_state, source_updated_at,
			indexed_at, index_version, metadata
		)
		VALUES ($1, 'document_chunk', $2, $3, $4, $5, $2, nullif($6::text, ''), nullif($7::text, ''),
		        nullif($8::text, ''), nullif($9::text, ''), $10::text, $11::text,
		        setweight(to_tsvector('simple', COALESCE($8::text, '')), 'A') ||
		        setweight(to_tsvector('simple', COALESCE($9::text, '')), 'B') ||
		        setweight(to_tsvector('simple', $10::text), 'C'),
		        $12, 'internal', 'trusted_system', 'fresh', now(), now(), $13,
		        '{"slice":"4","stage":"full_text"}'::jsonb)
		ON CONFLICT (source_kind, source_id, index_version)
		DO UPDATE SET
			source_version_id = EXCLUDED.source_version_id,
			object_id = EXCLUDED.object_id,
			object_version_id = EXCLUDED.object_version_id,
			document_chunk_id = EXCLUDED.document_chunk_id,
			source_node_id = EXCLUDED.source_node_id,
			scope_id = EXCLUDED.scope_id,
			title = EXCLUDED.title,
			summary = EXCLUDED.summary,
			body = EXCLUDED.body,
			body_hash = EXCLUDED.body_hash,
			tsv = EXCLUDED.tsv,
			language_config = EXCLUDED.language_config,
			data_classification = EXCLUDED.data_classification,
			trust_level = EXCLUDED.trust_level,
			freshness_state = 'fresh',
			source_updated_at = now(),
			indexed_at = now(),
			metadata = EXCLUDED.metadata
		RETURNING search_document_id
	`, searchDocumentID, chunkID, target.ObjectVersionID, target.ObjectID, target.ObjectVersionID,
		target.SourceNodeID, target.HomeScopeID, title, summary, chunk.Text, chunk.HashURI,
		LanguageConfig, IndexVersion).Scan(&searchDocumentID)
	return searchDocumentID, err
}

func (s Service) recordStatus(ctx context.Context, target indexTarget, indexType, status, errorCode, errorMessage string) (IndexStatus, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return IndexStatus{}, err
	}
	defer tx.Rollback()

	indexStatus, err := upsertStatusTx(ctx, tx, target, indexType, status, errorCode, errorMessage)
	if err != nil {
		return IndexStatus{}, err
	}
	if err := tx.Commit(); err != nil {
		return IndexStatus{}, err
	}
	return indexStatus, nil
}

func (s Service) recordFailure(ctx context.Context, req requestctx.Context, target indexTarget, indexType, eventType, code string, cause error) (IndexResult, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return IndexResult{}, err
	}
	defer tx.Rollback()

	status, err := upsertStatusTx(ctx, tx, target, indexType, "failed", code, cause.Error())
	if err != nil {
		return IndexResult{}, err
	}
	event, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  eventType,
		EventLevel: "audit",
		Request:    req,
		ScopeID:    target.HomeScopeID,
		TargetKind: "object",
		TargetID:   target.ObjectID,
		Status:     "failed",
		Result:     "error",
		Payload: map[string]any{
			"object_id":         target.ObjectID,
			"object_version_id": target.ObjectVersionID,
			"index_type":        indexType,
			"error_code":        code,
			"error_message":     cause.Error(),
		},
		VisibilityClass: "internal",
	})
	if err != nil {
		return IndexResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return IndexResult{}, err
	}
	return IndexResult{
		ObjectID:        target.ObjectID,
		ObjectVersionID: target.ObjectVersionID,
		Status:          "failed",
		Statuses:        []IndexStatus{status},
		EventIDs:        []string{event.EventID},
	}, nil
}

func upsertStatusTx(ctx context.Context, tx *sql.Tx, target indexTarget, indexType, status, errorCode, errorMessage string) (IndexStatus, error) {
	indexStatusID := ""
	err := tx.QueryRowContext(ctx, `
		SELECT index_status_id
		FROM search.index_status
		WHERE source_kind = 'object_version'
		  AND source_id = $1
		  AND COALESCE(source_version_id, '') = $1
		  AND index_type = $2
		  AND index_version = $3
		LIMIT 1
	`, target.ObjectVersionID, indexType, statusIndexVersion(indexType)).Scan(&indexStatusID)
	if err != nil && err != sql.ErrNoRows {
		return IndexStatus{}, err
	}

	if err == sql.ErrNoRows {
		indexStatusID = ids.NewIndexStatusID()
		row := tx.QueryRowContext(ctx, `
			INSERT INTO search.index_status (
				index_status_id, source_kind, source_id, source_version_id, object_id,
				object_version_id, scope_id, index_type, status, index_version,
				extractor_version, chunker_version, queued_at, started_at, completed_at,
				failed_at, last_error_code, last_error_message, metadata
			)
			VALUES ($1, 'object_version', $2, $2, $3, $2, nullif($4, ''), $5, $6, $7,
			        nullif($8, ''), nullif($9, ''), $10, $11, $12, $13, nullif($14, ''),
			        nullif($15, ''), '{"slice":"4"}'::jsonb)
			RETURNING `+indexStatusColumns()+`
		`, indexStatusID, target.ObjectVersionID, target.ObjectID, target.HomeScopeID, indexType, status,
			statusIndexVersion(indexType), statusExtractorVersion(indexType), statusChunkerVersion(indexType),
			queuedAt(status), startedAt(status), completedAt(status), failedAt(status), errorCode, errorMessage)
		return scanIndexStatus(row)
	}

	row := tx.QueryRowContext(ctx, `
		UPDATE search.index_status
		SET status = $2,
		    queued_at = CASE WHEN $2 = 'queued' THEN COALESCE(queued_at, now()) ELSE queued_at END,
		    started_at = $3,
		    completed_at = $4,
		    failed_at = $5,
		    last_error_code = nullif($6, ''),
		    last_error_message = nullif($7, ''),
		    next_attempt_at = CASE WHEN $2 IN ('queued', 'indexed', 'disabled_by_policy', 'skipped_unsupported') THEN null ELSE next_attempt_at END,
		    claimed_by_worker_run_id = CASE WHEN $2 IN ('queued', 'indexed', 'disabled_by_policy', 'skipped_unsupported') THEN null ELSE claimed_by_worker_run_id END,
		    claim_expires_at = CASE WHEN $2 IN ('queued', 'indexed', 'disabled_by_policy', 'skipped_unsupported') THEN null ELSE claim_expires_at END,
		    manual_action_required = CASE WHEN $2 IN ('queued', 'indexed', 'disabled_by_policy', 'skipped_unsupported') THEN false ELSE manual_action_required END,
		    updated_at = now(),
		    metadata = '{"slice":"4"}'::jsonb
		WHERE index_status_id = $1
		RETURNING `+indexStatusColumns()+`
	`, indexStatusID, status, startedAt(status), completedAt(status), failedAt(status), errorCode, errorMessage)
	return scanIndexStatus(row)
}

func (s Service) resolveTarget(ctx context.Context, objectID string, versionID string) (indexTarget, error) {
	objectID = strings.TrimSpace(objectID)
	versionID = strings.TrimSpace(versionID)
	if objectID == "" || versionID == "" {
		return indexTarget{}, fmt.Errorf("object_id and object_version_id are required")
	}

	row := s.DB.QueryRowContext(ctx, `
		SELECT o.object_id, o.name, o.object_type, COALESCE(o.home_scope_id, ''),
		       v.object_version_id, COALESCE(v.blob_id, ''), COALESCE(v.source_node_id, ''),
		       COALESCE(v.mime_type, ''), COALESCE(b.storage_path, ''), COALESCE(b.hash_uri, ''),
		       COALESCE(f.logical_name, ''), COALESCE(f.extension, ''),
		       COALESCE(f.text_extractable, false), COALESCE(f.index_policy, 'metadata_only')
		FROM objects.objects o
		JOIN objects.object_versions v ON v.object_id = o.object_id
		LEFT JOIN files.blobs b ON b.blob_id = v.blob_id
		LEFT JOIN files.file_metadata f ON f.object_id = o.object_id
		WHERE o.object_id = $1 AND v.object_version_id = $2
		LIMIT 1
	`, objectID, versionID)
	var target indexTarget
	err := row.Scan(
		&target.ObjectID,
		&target.ObjectName,
		&target.ObjectType,
		&target.HomeScopeID,
		&target.ObjectVersionID,
		&target.BlobID,
		&target.SourceNodeID,
		&target.MimeType,
		&target.StoragePath,
		&target.HashURI,
		&target.LogicalName,
		&target.Extension,
		&target.TextExtractable,
		&target.IndexPolicy,
	)
	return target, err
}

func (s Service) resolveLatestVersionRef(ctx context.Context, objectRef string) (string, string, error) {
	ref := strings.TrimSpace(objectRef)
	if ref == "" {
		return "", "", fmt.Errorf("object_ref is required")
	}

	var objectID string
	var versionID string
	err := s.DB.QueryRowContext(ctx, `
		SELECT o.object_id, v.object_version_id
		FROM objects.objects o
		JOIN objects.object_versions v ON v.object_id = o.object_id
		WHERE o.object_id = $1 OR o.slug = $1 OR o.name = $1
		ORDER BY v.version_number DESC
		LIMIT 1
	`, ref).Scan(&objectID, &versionID)
	return objectID, versionID, err
}

func (s Service) resolveObjectID(ctx context.Context, objectRef string) (string, error) {
	ref := strings.TrimSpace(objectRef)
	if ref == "" {
		return "", fmt.Errorf("object ref is required")
	}
	var objectID string
	err := s.DB.QueryRowContext(ctx, `
		SELECT object_id
		FROM objects.objects
		WHERE object_id = $1 OR slug = $1 OR name = $1
		LIMIT 1
	`, ref).Scan(&objectID)
	return objectID, err
}

func (s Service) getFullTextStatus(ctx context.Context, objectVersionID string) (IndexStatus, bool, error) {
	row := s.DB.QueryRowContext(ctx, indexStatusSelectSQL()+`
		WHERE source_kind = 'object_version'
		  AND source_id = $1
		  AND COALESCE(source_version_id, '') = $1
		  AND object_version_id = $1
		  AND index_type = 'full_text'
		  AND index_version = $2
		LIMIT 1
	`, objectVersionID, IndexVersion)
	status, err := scanIndexStatus(row)
	if err == sql.ErrNoRows {
		return IndexStatus{}, false, nil
	}
	if err != nil {
		return IndexStatus{}, false, err
	}
	return status, true, nil
}

func (s Service) resolveOptionalScope(ctx context.Context, projectRef, scopeRef string) (string, error) {
	projectRef = strings.TrimSpace(projectRef)
	scopeRef = strings.TrimSpace(scopeRef)
	if projectRef != "" && scopeRef != "" {
		return "", fmt.Errorf("provide either project_ref or scope_ref, not both")
	}
	if projectRef != "" {
		project, err := projects.NewService(s.DB).ResolveProjectRef(ctx, projectRef)
		if err != nil {
			return "", fmt.Errorf("resolve project: %w", err)
		}
		return project.ProjectScopeID, nil
	}
	if scopeRef == "" {
		return "", nil
	}
	var scopeID string
	err := s.DB.QueryRowContext(ctx, `
		SELECT scope_id
		FROM scopes.scopes
		WHERE scope_id = $1 OR scope_key = $1 OR slug = $1
		LIMIT 1
	`, scopeRef).Scan(&scopeID)
	if err != nil {
		return "", fmt.Errorf("resolve scope: %w", err)
	}
	return scopeID, nil
}

func queueLikeStatus(status string) bool {
	switch status {
	case "queued", "stale", "rebuilding", "extracting", "chunking", "indexing":
		return true
	default:
		return false
	}
}

func retryDelay(baseDelay, maxDelay time.Duration, attempt int) time.Duration {
	if attempt <= 1 {
		return baseDelay
	}
	delay := baseDelay
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= maxDelay {
			return maxDelay
		}
	}
	return delay
}

func RetryableIndexError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, terminal := range []string{
		"not valid utf-8",
		"exceeds max_text_bytes_per_object",
		"exceeds max_chunks_per_object",
		"object_id and object_version_id are required",
		"not a supported text target",
	} {
		if strings.Contains(message, terminal) {
			return false
		}
	}
	for _, retryable := range []string{
		"read object-store blob",
		"connection",
		"timeout",
		"temporarily",
		"deadlock",
		"serialization",
		"lock",
	} {
		if strings.Contains(message, retryable) {
			return true
		}
	}
	return true
}

func indexErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "object_version_not_found"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "not valid utf-8"):
		return "text_decode_failed"
	case strings.Contains(message, "max_text_bytes_per_object"):
		return "text_too_large"
	case strings.Contains(message, "max_chunks_per_object"):
		return "too_many_chunks"
	case strings.Contains(message, "read object-store blob"):
		return "object_store_read_failed"
	default:
		return "indexing_failed"
	}
}

func readExtractableText(path string) (string, error) {
	return readExtractableTextWithLimit(path, 0)
}

func readExtractableTextWithLimit(path string, maxBytes int64) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("object version has no stored blob path")
	}
	if maxBytes > 0 {
		info, err := os.Stat(path)
		if err != nil {
			return "", fmt.Errorf("read object-store blob: %w", err)
		}
		if info.Size() > maxBytes {
			return "", fmt.Errorf("text size %d exceeds max_text_bytes_per_object %d", info.Size(), maxBytes)
		}
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read object-store blob: %w", err)
	}
	if !utf8.Valid(payload) {
		return "", fmt.Errorf("blob is not valid utf-8 text")
	}
	text := strings.ReplaceAll(string(payload), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return text, nil
}

func supportedTextTarget(target indexTarget) bool {
	extension := strings.TrimPrefix(strings.ToLower(target.Extension), ".")
	mimeType := strings.ToLower(target.MimeType)
	switch extension {
	case "txt", "md", "markdown", "json", "yaml", "yml", "csv", "tsv", "log", "go", "nix", "sh", "toml":
		return true
	}
	return strings.HasPrefix(mimeType, "text/") ||
		strings.Contains(mimeType, "json") ||
		strings.Contains(mimeType, "yaml") ||
		strings.Contains(mimeType, "toml")
}

func disabledByPolicy(policy string) bool {
	switch strings.TrimSpace(policy) {
	case "none", "private_no_index":
		return true
	default:
		return false
	}
}

func chunkText(text string) []chunkInput {
	paragraphs := splitParagraphs(text)
	if len(paragraphs) == 0 {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return nil
		}
		return []chunkInput{newChunk(1, trimmed, 0, len(text), "")}
	}

	chunks := []chunkInput{}
	current := strings.Builder{}
	currentStart := -1
	currentEnd := -1
	currentPath := ""
	lastPath := ""

	flush := func() {
		body := strings.TrimSpace(current.String())
		if body == "" {
			current.Reset()
			currentStart = -1
			currentEnd = -1
			return
		}
		chunks = append(chunks, newChunk(len(chunks)+1, body, currentStart, currentEnd, currentPath))
		current.Reset()
		currentStart = -1
		currentEnd = -1
		currentPath = lastPath
	}

	for _, paragraph := range paragraphs {
		if heading := markdownHeading(paragraph.Text); heading != "" {
			lastPath = heading
		}
		if len(paragraph.Text) > maxChunkCharacters {
			flush()
			for _, chunk := range splitLongParagraph(paragraph, lastPath) {
				chunk.Index = len(chunks) + 1
				chunk.HashURI = hashText(fmt.Sprintf("%d\n%s", chunk.Index, chunk.Text))
				chunks = append(chunks, chunk)
			}
			continue
		}
		if current.Len() > 0 && current.Len()+len(paragraph.Text)+2 > targetChunkCharacters {
			flush()
		}
		if currentStart < 0 {
			currentStart = paragraph.Start
			currentPath = lastPath
		}
		if current.Len() > 0 {
			current.WriteString("\n\n")
		}
		current.WriteString(paragraph.Text)
		currentEnd = paragraph.End
	}
	flush()
	return chunks
}

type paragraph struct {
	Text  string
	Start int
	End   int
}

func splitParagraphs(text string) []paragraph {
	paragraphs := []paragraph{}
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
				body := strings.TrimSpace(builder.String())
				paragraphs = append(paragraphs, paragraph{Text: body, Start: start, End: lineStart})
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
		paragraphs = append(paragraphs, paragraph{Text: strings.TrimSpace(builder.String()), Start: start, End: len(text)})
	}
	return paragraphs
}

func splitLongParagraph(par paragraph, structuralPath string) []chunkInput {
	chunks := []chunkInput{}
	start := 0
	for start < len(par.Text) {
		end := start + targetChunkCharacters
		if end > len(par.Text) {
			end = len(par.Text)
		}
		if end < len(par.Text) {
			if boundary := strings.LastIndexAny(par.Text[start:end], " \n\t"); boundary > targetChunkCharacters/2 {
				end = start + boundary
			}
		}
		body := strings.TrimSpace(par.Text[start:end])
		if body != "" {
			chunks = append(chunks, newChunk(0, body, par.Start+start, par.Start+end, structuralPath))
		}
		start = end
	}
	return chunks
}

func newChunk(index int, text string, start, end int, structuralPath string) chunkInput {
	hashInput := fmt.Sprintf("%d\n%s", index, text)
	return chunkInput{
		Index:          index,
		Text:           text,
		StartOffset:    start,
		EndOffset:      end,
		StructuralPath: structuralPath,
		TokenEstimate:  len(strings.Fields(text)),
		HashURI:        hashText(hashInput),
	}
}

func markdownHeading(text string) string {
	line := strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
	if !strings.HasPrefix(line, "#") {
		return ""
	}
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level >= len(line) || line[level] != ' ' {
		return ""
	}
	return strings.TrimSpace(line[level+1:])
}

func hashText(text string) string {
	hash := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(hash[:])
}

func nullableOffset(offset int) any {
	if offset < 0 {
		return nil
	}
	return offset
}

func queuedAt(status string) any {
	if status == "queued" {
		return time.Now().UTC()
	}
	return nil
}

func startedAt(status string) any {
	if status == "queued" {
		return nil
	}
	return time.Now().UTC()
}

func completedAt(status string) any {
	switch status {
	case "indexed", "skipped_unsupported", "disabled_by_policy":
		return time.Now().UTC()
	default:
		return nil
	}
}

func failedAt(status string) any {
	if status == "failed" {
		return time.Now().UTC()
	}
	return nil
}

func statusIndexVersion(indexType string) string {
	if indexType == "text_extraction" {
		return ExtractorVersion
	}
	if indexType == "chunking" {
		return ChunkerVersion
	}
	return IndexVersion
}

func statusExtractorVersion(indexType string) string {
	if indexType == "text_extraction" {
		return ExtractorVersion
	}
	return ""
}

func statusChunkerVersion(indexType string) string {
	if indexType == "chunking" {
		return ChunkerVersion
	}
	return ""
}

func buildCitation(result SearchResult) Citation {
	label := result.Title
	if label == "" {
		label = filepath.Base(result.ObjectID)
	}
	sourceRef := result.ObjectID
	if result.DocumentChunkID != "" {
		sourceRef = result.DocumentChunkID
	}
	return Citation{Label: label, SourceRef: sourceRef}
}

func scanSearchResult(scanner rowScanner) (SearchResult, error) {
	var result SearchResult
	err := scanner.Scan(
		&result.SearchDocumentID,
		&result.ResultKind,
		&result.ObjectID,
		&result.ObjectVersionID,
		&result.DocumentChunkID,
		&result.ScopeID,
		&result.SourceNodeID,
		&result.Title,
		&result.Snippet,
		&result.RankScore,
		&result.LanguageConfig,
		&result.DataClassification,
		&result.TrustLevel,
		&result.FreshnessState,
		&result.IndexedAt,
	)
	return result, err
}

func indexStatusSelectSQL() string {
	return `SELECT ` + indexStatusColumns() + ` FROM search.index_status`
}

func indexStatusColumns() string {
	return `
		index_status_id, source_kind, source_id, COALESCE(source_version_id, ''),
		COALESCE(object_id, ''), COALESCE(object_version_id, ''), COALESCE(scope_id, ''),
		index_type, status, index_version, COALESCE(extractor_version, ''),
		COALESCE(chunker_version, ''), queued_at, started_at, completed_at, failed_at,
		COALESCE(last_error_code, ''), COALESCE(last_error_message, ''),
		metadata, created_at, updated_at, COALESCE(attempt_count, 0), next_attempt_at,
		COALESCE(claimed_by_worker_run_id, ''), claim_expires_at, COALESCE(priority, 100),
		COALESCE(manual_action_required, false), COALESCE(last_worker_run_id, ''),
		last_attempt_at`
}

func scanIndexStatus(scanner rowScanner) (IndexStatus, error) {
	var status IndexStatus
	var queuedAt sql.NullTime
	var startedAt sql.NullTime
	var completedAt sql.NullTime
	var failedAt sql.NullTime
	var nextAttemptAt sql.NullTime
	var claimExpiresAt sql.NullTime
	var lastAttemptAt sql.NullTime
	var metadata []byte
	err := scanner.Scan(
		&status.IndexStatusID,
		&status.SourceKind,
		&status.SourceID,
		&status.SourceVersionID,
		&status.ObjectID,
		&status.ObjectVersionID,
		&status.ScopeID,
		&status.IndexType,
		&status.Status,
		&status.IndexVersion,
		&status.ExtractorVersion,
		&status.ChunkerVersion,
		&queuedAt,
		&startedAt,
		&completedAt,
		&failedAt,
		&status.LastErrorCode,
		&status.LastErrorMessage,
		&metadata,
		&status.CreatedAt,
		&status.UpdatedAt,
		&status.AttemptCount,
		&nextAttemptAt,
		&status.ClaimedByRunID,
		&claimExpiresAt,
		&status.Priority,
		&status.ManualAction,
		&status.LastWorkerRunID,
		&lastAttemptAt,
	)
	if err != nil {
		return IndexStatus{}, err
	}
	status.QueuedAt = timePtr(queuedAt)
	status.StartedAt = timePtr(startedAt)
	status.CompletedAt = timePtr(completedAt)
	status.FailedAt = timePtr(failedAt)
	status.NextAttemptAt = timePtr(nextAttemptAt)
	status.ClaimExpiresAt = timePtr(claimExpiresAt)
	status.LastAttemptAt = timePtr(lastAttemptAt)
	status.Metadata = jsonOrEmpty(metadata)
	return status, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func timePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time
	return &t
}

func jsonOrEmpty(payload []byte) json.RawMessage {
	if len(payload) == 0 {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(payload)
}
