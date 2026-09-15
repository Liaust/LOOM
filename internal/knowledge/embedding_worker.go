package knowledge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

const (
	defaultEmbeddingWorkerLimit       = 1
	defaultEmbeddingWorkerMaxAttempts = 3
	defaultEmbeddingWorkerRetryDelay  = time.Minute
)

type EmbeddingWorkerRunInput struct {
	WorkerRunID         string
	Limit               int
	LeaseDuration       time.Duration
	Runtime             EmbeddingRuntime
	Now                 time.Time
	RetryMaxAttempts    int
	RetryDelay          time.Duration
	RetentionBatchLimit int
}

type EmbeddingWorkerRunResult struct {
	SchemaVersion       string `json:"schema_version"`
	Status              string `json:"status"`
	ReleasedExpired     int64  `json:"released_expired"`
	Claimed             int64  `json:"claimed"`
	Embedded            int64  `json:"embedded"`
	Reused              int64  `json:"reused"`
	Activated           int64  `json:"activated"`
	HistoricalWritten   int64  `json:"historical_written"`
	StaleOutputs        int64  `json:"stale_outputs"`
	FailedRetryable     int64  `json:"failed_retryable"`
	FailedTerminal      int64  `json:"failed_terminal"`
	RetentionDeleted    int64  `json:"retention_deleted"`
	MoreWork            bool   `json:"more_work"`
	LastWorkItemID      string `json:"last_work_item_id,omitempty"`
	LastKnowledgeObject string `json:"last_knowledge_object_id,omitempty"`
}

type EmbeddingWorkFailure struct {
	ErrorCode    string
	ErrorMessage string
	Retryable    bool
	MaxAttempts  int
	RetryDelay   time.Duration
	Now          time.Time
}

type EmbeddingActivationResult struct {
	Activated         bool
	Reused            bool
	Stale             bool
	HistoricalWritten bool
}

func (s *Service) RunEmbeddingWorkerOnce(ctx context.Context, input EmbeddingWorkerRunInput) (EmbeddingWorkerRunResult, error) {
	if s == nil || s.store.db == nil {
		return EmbeddingWorkerRunResult{}, fmt.Errorf("knowledge store is not configured")
	}
	input = normalizeEmbeddingWorkerRunInput(input)
	now := input.Now
	if now.IsZero() {
		now = s.currentTime()
	}
	result := EmbeddingWorkerRunResult{
		SchemaVersion: "knowledge_embedder.result.v0.8.6",
		Status:        "ok",
	}
	settings, err := s.store.GetEmbeddingSettings(ctx)
	if err != nil {
		return EmbeddingWorkerRunResult{}, err
	}
	if !settings.Enabled {
		result.Status = "disabled"
		return result, nil
	}
	if input.Runtime == nil {
		return EmbeddingWorkerRunResult{}, fmt.Errorf("%w: embedding runtime is required when embeddings are enabled", ErrInvalid)
	}
	released, err := s.ReleaseExpiredEmbeddingClaims(ctx, now)
	if err != nil {
		return EmbeddingWorkerRunResult{}, fmt.Errorf("release expired embedding claims: %w", err)
	}
	result.ReleasedExpired = int64(released)
	items, err := s.ClaimEmbeddingWork(ctx, input.WorkerRunID, EmbeddingWorkClaimOptions{
		Limit:         input.Limit,
		LeaseDuration: input.LeaseDuration,
		Now:           now,
	})
	if err != nil {
		return EmbeddingWorkerRunResult{}, fmt.Errorf("claim embedding work: %w", err)
	}
	result.Claimed = int64(len(items))
	for _, item := range items {
		result.LastWorkItemID = item.KnowledgeEmbeddingWorkItemID
		result.LastKnowledgeObject = item.KnowledgeObjectID
		activation, processErr := s.processEmbeddingWorkItem(ctx, item, input.Runtime, now)
		if processErr != nil {
			failure := classifyEmbeddingWorkFailure(processErr, input, now)
			if err := s.FailEmbeddingWork(ctx, item, failure); err != nil {
				return EmbeddingWorkerRunResult{}, fmt.Errorf("record embedding failure: %w", err)
			}
			if failure.Retryable {
				result.FailedRetryable++
			} else {
				result.FailedTerminal++
			}
			continue
		}
		if activation.Stale {
			result.StaleOutputs++
			continue
		}
		if activation.Reused {
			result.Reused++
		} else {
			result.Embedded++
		}
		if activation.Activated {
			result.Activated++
		}
		if activation.HistoricalWritten {
			result.HistoricalWritten++
		}
	}
	if len(items) == input.Limit && input.Limit > 0 {
		result.MoreWork = true
	}
	deleted, err := s.PruneEmbeddingRetention(ctx, DefaultEmbeddingHistoryPerLineage, input.RetentionBatchLimit)
	if err != nil {
		return EmbeddingWorkerRunResult{}, fmt.Errorf("prune embedding retention: %w", err)
	}
	result.RetentionDeleted = int64(deleted)
	return result, nil
}

func (s *Service) processEmbeddingWorkItem(ctx context.Context, item EmbeddingWorkItem, runtime EmbeddingRuntime, now time.Time) (EmbeddingActivationResult, error) {
	chunk, err := s.store.GetKnowledgeChunk(ctx, valueOrEmpty(item.KnowledgeChunkID))
	if err != nil {
		return EmbeddingActivationResult{}, err
	}
	reuse, err := s.TryReuseChunkEmbedding(ctx, item, chunk, now)
	if err != nil {
		return EmbeddingActivationResult{}, err
	}
	if reuse.Reused || reuse.Stale {
		return reuse, nil
	}
	passages, err := PrepareEmbeddingPassages([]KnowledgeChunk{chunk}, EmbeddingPassageOptions{})
	if err != nil {
		return EmbeddingActivationResult{}, err
	}
	inputs, err := EmbeddingRuntimeInputsFromPassages(passages)
	if err != nil {
		return EmbeddingActivationResult{}, err
	}
	response, err := runtime.Embed(ctx, EmbeddingRuntimeRequest{
		Model:    item.ModelKey,
		Inputs:   inputs,
		Truncate: false,
	})
	if err != nil {
		return EmbeddingActivationResult{}, err
	}
	vector, err := AverageEmbeddingVectors(response.Embeddings)
	if err != nil {
		return EmbeddingActivationResult{}, err
	}
	if len(vector) != item.Dimensions {
		return EmbeddingActivationResult{}, fmt.Errorf("%w: embedding dimensions %d do not match configured dimensions %d", ErrInvalid, len(vector), item.Dimensions)
	}
	return s.ActivateGeneratedChunkEmbedding(ctx, item, chunk, vector, hashEmbeddingInput(passages), now)
}

func (s Store) GetKnowledgeChunk(ctx context.Context, ref string) (KnowledgeChunk, error) {
	if s.db == nil {
		return KnowledgeChunk{}, fmt.Errorf("knowledge store is not configured")
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return KnowledgeChunk{}, fmt.Errorf("%w: knowledge_chunk_id is required", ErrInvalid)
	}
	return scanKnowledgeChunk(s.db.QueryRowContext(ctx, `SELECT `+knowledgeChunkColumns()+`
		FROM knowledge.knowledge_chunks
		WHERE knowledge_chunk_id = $1`, ref))
}

func (s *Service) TryReuseChunkEmbedding(ctx context.Context, item EmbeddingWorkItem, chunk KnowledgeChunk, now time.Time) (EmbeddingActivationResult, error) {
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return EmbeddingActivationResult{}, err
	}
	defer tx.Rollback()
	current, err := verifyEmbeddingWorkCurrentTx(ctx, tx, item, chunk)
	if err != nil {
		return EmbeddingActivationResult{}, err
	}
	if !current {
		if err := markEmbeddingWorkStaleTx(ctx, tx, item, now); err != nil {
			return EmbeddingActivationResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return EmbeddingActivationResult{}, err
		}
		return EmbeddingActivationResult{Stale: true}, nil
	}
	historical, err := deactivateCurrentChunkEmbeddingTx(ctx, tx, item, chunk, now)
	if err != nil {
		return EmbeddingActivationResult{}, err
	}
	var insertedID string
	err = tx.QueryRowContext(ctx, `
		WITH source_embedding AS (
			SELECT embedding_runtime_model_id, distance_metric, input_hash,
			       token_count_estimate, embedding, metadata
			FROM knowledge.chunk_embeddings
			WHERE chunk_hash = $1
			  AND chunker_version = $2
			  AND runtime_key = $3
			  AND model_key = $4
			  AND dimensions = $5
			  AND status IN ('active', 'historical', 'reusable')
			ORDER BY active DESC, created_at DESC
			LIMIT 1
		)
		INSERT INTO knowledge.chunk_embeddings (
			knowledge_chunk_embedding_id, knowledge_chunk_id, knowledge_object_id,
			knowledge_object_version_id, embedding_runtime_model_id, runtime_key,
			model_key, dimensions, distance_metric, chunk_hash, chunker_version,
			input_hash, token_count_estimate, source_generation, embedding, status,
			active, metadata, created_at, activated_at, deactivated_at
		)
		SELECT $6,$7,$8,$9,source_embedding.embedding_runtime_model_id,$3,
		       $4,$5,source_embedding.distance_metric,$1,$2,
		       source_embedding.input_hash,source_embedding.token_count_estimate,$10,
		       source_embedding.embedding,'active',true,source_embedding.metadata,$11,$11,null
		FROM source_embedding
		RETURNING knowledge_chunk_embedding_id
	`, chunk.ChunkHash, chunk.ChunkerVersion, item.RuntimeKey, item.ModelKey, item.Dimensions, newKnowledgeChunkEmbeddingID(), chunk.KnowledgeChunkID, chunk.KnowledgeObjectID, nullableString(chunk.KnowledgeObjectVersionID), item.Generation, now).Scan(&insertedID)
	if err == sql.ErrNoRows {
		return EmbeddingActivationResult{}, nil
	}
	if err != nil {
		return EmbeddingActivationResult{}, err
	}
	if err := markEmbeddingWorkCompleteTx(ctx, tx, item, now); err != nil {
		return EmbeddingActivationResult{}, err
	}
	if err := updateEmbeddingObjectStateAfterWorkTx(ctx, tx, item, now); err != nil {
		return EmbeddingActivationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return EmbeddingActivationResult{}, err
	}
	return EmbeddingActivationResult{Activated: true, Reused: true, HistoricalWritten: historical}, nil
}

func (s *Service) ActivateGeneratedChunkEmbedding(ctx context.Context, item EmbeddingWorkItem, chunk KnowledgeChunk, vector []float32, inputHash string, now time.Time) (EmbeddingActivationResult, error) {
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return EmbeddingActivationResult{}, err
	}
	defer tx.Rollback()
	current, err := verifyEmbeddingWorkCurrentTx(ctx, tx, item, chunk)
	if err != nil {
		return EmbeddingActivationResult{}, err
	}
	if !current {
		if err := insertGeneratedChunkEmbeddingTx(ctx, tx, item, chunk, vector, inputHash, ChunkEmbeddingStatusReusable, false, now); err != nil {
			return EmbeddingActivationResult{}, err
		}
		if err := markEmbeddingWorkStaleTx(ctx, tx, item, now); err != nil {
			return EmbeddingActivationResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return EmbeddingActivationResult{}, err
		}
		return EmbeddingActivationResult{Stale: true}, nil
	}
	historical, err := deactivateCurrentChunkEmbeddingTx(ctx, tx, item, chunk, now)
	if err != nil {
		return EmbeddingActivationResult{}, err
	}
	if err := insertGeneratedChunkEmbeddingTx(ctx, tx, item, chunk, vector, inputHash, ChunkEmbeddingStatusActive, true, now); err != nil {
		return EmbeddingActivationResult{}, err
	}
	if err := markEmbeddingWorkCompleteTx(ctx, tx, item, now); err != nil {
		return EmbeddingActivationResult{}, err
	}
	if err := updateEmbeddingObjectStateAfterWorkTx(ctx, tx, item, now); err != nil {
		return EmbeddingActivationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return EmbeddingActivationResult{}, err
	}
	return EmbeddingActivationResult{Activated: true, HistoricalWritten: historical}, nil
}

func (s *Service) FailEmbeddingWork(ctx context.Context, item EmbeddingWorkItem, failure EmbeddingWorkFailure) error {
	if s == nil || s.store.db == nil {
		return fmt.Errorf("knowledge store is not configured")
	}
	now := failure.Now
	if now.IsZero() {
		now = s.currentTime()
	}
	maxAttempts := failure.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultEmbeddingWorkerMaxAttempts
	}
	retryDelay := failure.RetryDelay
	if retryDelay <= 0 {
		retryDelay = defaultEmbeddingWorkerRetryDelay
	}
	attempt := item.AttemptCount + 1
	code := strings.TrimSpace(failure.ErrorCode)
	if code == "" {
		code = "embedding_failed"
	}
	message := strings.TrimSpace(failure.ErrorMessage)
	if message == "" {
		message = "embedding failed"
	}
	status := EmbeddingWorkStatusFailed
	eligibleAt := now
	if failure.Retryable && attempt < maxAttempts {
		status = EmbeddingWorkStatusQueued
		eligibleAt = now.Add(retryDelay)
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
		UPDATE knowledge.embedding_work_items
		SET status = $2,
		    eligible_at = $3,
		    failed_at = CASE WHEN $2 = 'failed' THEN $4 ELSE failed_at END,
		    attempt_count = $5,
		    claimed_by_worker_run_id = '',
		    claim_expires_at = null,
		    last_error_code = $6,
		    last_error_message = $7,
		    updated_at = now()
		WHERE knowledge_embedding_work_item_id = $1
	`, item.KnowledgeEmbeddingWorkItemID, status, eligibleAt, now, attempt, code, message)
	if err != nil {
		return err
	}
	if status == EmbeddingWorkStatusFailed {
		if _, err := tx.ExecContext(ctx, `
			UPDATE knowledge.embedding_object_states
			SET status = 'failed',
			    last_failed_at = $2,
			    last_error_code = $3,
			    last_error_message = $4,
			    updated_at = now()
			WHERE knowledge_object_id = $1
			  AND runtime_key = $5
			  AND model_key = $6
			  AND dimensions = $7
		`, item.KnowledgeObjectID, now, code, message, item.RuntimeKey, item.ModelKey, item.Dimensions); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func normalizeEmbeddingWorkerRunInput(input EmbeddingWorkerRunInput) EmbeddingWorkerRunInput {
	input.WorkerRunID = strings.TrimSpace(input.WorkerRunID)
	if input.Limit <= 0 {
		input.Limit = defaultEmbeddingWorkerLimit
	}
	if input.Limit > 1 {
		input.Limit = 1
	}
	if input.LeaseDuration <= 0 {
		input.LeaseDuration = defaultEmbeddingLeaseDuration
	}
	if input.RetryMaxAttempts <= 0 {
		input.RetryMaxAttempts = defaultEmbeddingWorkerMaxAttempts
	}
	if input.RetryDelay <= 0 {
		input.RetryDelay = defaultEmbeddingWorkerRetryDelay
	}
	return input
}

func classifyEmbeddingWorkFailure(err error, input EmbeddingWorkerRunInput, now time.Time) EmbeddingWorkFailure {
	retryable := IsEmbeddingRuntimeTimeout(err) || IsEmbeddingRuntimeUnavailable(err)
	code := "embedding_failed"
	if retryable {
		code = "embedding_runtime_unavailable"
	}
	return EmbeddingWorkFailure{
		ErrorCode:    code,
		ErrorMessage: err.Error(),
		Retryable:    retryable,
		MaxAttempts:  input.RetryMaxAttempts,
		RetryDelay:   input.RetryDelay,
		Now:          now,
	}
}

func verifyEmbeddingWorkCurrentTx(ctx context.Context, tx *sql.Tx, item EmbeddingWorkItem, chunk KnowledgeChunk) (bool, error) {
	var current bool
	err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM knowledge.embedding_work_items wi
			JOIN knowledge.embedding_object_states eos
			  ON eos.knowledge_object_id = wi.knowledge_object_id
			 AND eos.runtime_key = wi.runtime_key
			 AND eos.model_key = wi.model_key
			 AND eos.dimensions = wi.dimensions
			JOIN knowledge.knowledge_objects ko
			  ON ko.knowledge_object_id = wi.knowledge_object_id
			JOIN knowledge.knowledge_chunks kc
			  ON kc.knowledge_chunk_id = wi.knowledge_chunk_id
			WHERE wi.knowledge_embedding_work_item_id = $1
			  AND wi.status = 'processing'
			  AND wi.generation = eos.generation
			  AND ko.deleted_at IS NULL
			  AND kc.knowledge_chunk_id = $2
			  AND kc.knowledge_object_id = wi.knowledge_object_id
			  AND kc.chunk_hash = $3
			  AND kc.chunker_version = $4
			  AND ($5 = '' OR kc.knowledge_object_version_id = $5)
		)
	`, item.KnowledgeEmbeddingWorkItemID, chunk.KnowledgeChunkID, chunk.ChunkHash, chunk.ChunkerVersion, valueOrEmpty(chunk.KnowledgeObjectVersionID)).Scan(&current)
	return current, err
}

func deactivateCurrentChunkEmbeddingTx(ctx context.Context, tx *sql.Tx, item EmbeddingWorkItem, chunk KnowledgeChunk, now time.Time) (bool, error) {
	result, err := tx.ExecContext(ctx, `
		UPDATE knowledge.chunk_embeddings
		SET active = false,
		    status = 'historical',
		    deactivated_at = $5
		WHERE knowledge_chunk_id = $1
		  AND runtime_key = $2
		  AND model_key = $3
		  AND dimensions = $4
		  AND active = true
	`, chunk.KnowledgeChunkID, item.RuntimeKey, item.ModelKey, item.Dimensions, now)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

func insertGeneratedChunkEmbeddingTx(ctx context.Context, tx *sql.Tx, item EmbeddingWorkItem, chunk KnowledgeChunk, vector []float32, inputHash string, status string, active bool, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO knowledge.chunk_embeddings (
			knowledge_chunk_embedding_id, knowledge_chunk_id, knowledge_object_id,
			knowledge_object_version_id, embedding_runtime_model_id, runtime_key,
			model_key, dimensions, distance_metric, chunk_hash, chunker_version,
			input_hash, token_count_estimate, source_generation, embedding, status,
			active, metadata, created_at, activated_at, deactivated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,
		        $12,$13,$14,$15::vector,$16,$17,'{}'::jsonb,$18::timestamptz,
		        CASE WHEN $17 THEN $18::timestamptz ELSE null END,null)
	`, newKnowledgeChunkEmbeddingID(), nullableString(&chunk.KnowledgeChunkID), chunk.KnowledgeObjectID, nullableString(chunk.KnowledgeObjectVersionID), defaultEmbeddingRuntimeModelID(item), item.RuntimeKey, item.ModelKey, item.Dimensions, EmbeddingDistanceCosine, chunk.ChunkHash, chunk.ChunkerVersion, inputHash, nullableInt(chunk.TokenCountEstimate), item.Generation, embeddingVectorLiteral(vector), status, active, now)
	return err
}

func markEmbeddingWorkCompleteTx(ctx context.Context, tx *sql.Tx, item EmbeddingWorkItem, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE knowledge.embedding_work_items
		SET status = 'complete',
		    completed_at = $2,
		    claimed_by_worker_run_id = '',
		    claim_expires_at = null,
		    last_error_code = '',
		    last_error_message = '',
		    updated_at = now()
		WHERE knowledge_embedding_work_item_id = $1
	`, item.KnowledgeEmbeddingWorkItemID, now)
	return err
}

func markEmbeddingWorkStaleTx(ctx context.Context, tx *sql.Tx, item EmbeddingWorkItem, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE knowledge.embedding_work_items
		SET status = 'stale',
		    claimed_by_worker_run_id = '',
		    claim_expires_at = null,
		    updated_at = now()
		WHERE knowledge_embedding_work_item_id = $1
	`, item.KnowledgeEmbeddingWorkItemID)
	return err
}

func updateEmbeddingObjectStateAfterWorkTx(ctx context.Context, tx *sql.Tx, item EmbeddingWorkItem, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE knowledge.embedding_object_states eos
		SET status = CASE
				WHEN EXISTS (
					SELECT 1
					FROM knowledge.embedding_work_items wi
					WHERE wi.knowledge_object_id = eos.knowledge_object_id
					  AND wi.runtime_key = eos.runtime_key
					  AND wi.model_key = eos.model_key
					  AND wi.dimensions = eos.dimensions
					  AND wi.generation = eos.generation
					  AND wi.status IN ('queued', 'processing')
				) THEN 'processing'
				ELSE 'complete'
			END,
		    last_completed_at = CASE
				WHEN NOT EXISTS (
					SELECT 1
					FROM knowledge.embedding_work_items wi
					WHERE wi.knowledge_object_id = eos.knowledge_object_id
					  AND wi.runtime_key = eos.runtime_key
					  AND wi.model_key = eos.model_key
					  AND wi.dimensions = eos.dimensions
					  AND wi.generation = eos.generation
					  AND wi.status IN ('queued', 'processing')
				) THEN $5
				ELSE last_completed_at
			END,
		    updated_at = now()
		WHERE eos.knowledge_object_id = $1
		  AND eos.runtime_key = $2
		  AND eos.model_key = $3
		  AND eos.dimensions = $4
	`, item.KnowledgeObjectID, item.RuntimeKey, item.ModelKey, item.Dimensions, now)
	return err
}

func AverageEmbeddingVectors(vectors [][]float32) ([]float32, error) {
	if len(vectors) == 0 {
		return nil, fmt.Errorf("%w: embedding vectors are required", ErrInvalid)
	}
	dimensions := len(vectors[0])
	if dimensions == 0 {
		return nil, fmt.Errorf("%w: embedding vectors must not be empty", ErrInvalid)
	}
	out := make([]float32, dimensions)
	for _, vector := range vectors {
		if len(vector) != dimensions {
			return nil, fmt.Errorf("%w: embedding vectors have inconsistent dimensions", ErrInvalid)
		}
		for i, value := range vector {
			out[i] += value
		}
	}
	scale := float32(len(vectors))
	for i := range out {
		out[i] = out[i] / scale
	}
	return out, nil
}

func embeddingVectorLiteral(vector []float32) string {
	parts := make([]string, 0, len(vector))
	for _, value := range vector {
		parts = append(parts, strconv.FormatFloat(float64(value), 'g', -1, 32))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func hashEmbeddingInput(passages []EmbeddingPassage) string {
	h := sha256.New()
	for _, passage := range passages {
		h.Write([]byte(passage.PassageHash))
		h.Write([]byte{0})
		h.Write([]byte(passage.Text))
		h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func defaultEmbeddingRuntimeModelID(item EmbeddingWorkItem) string {
	model := strings.NewReplacer("-", "_", ".", "_", ":", "_").Replace(item.ModelKey)
	return fmt.Sprintf("embedding_runtime_model_%s_%s_%d", item.RuntimeKey, model, item.Dimensions)
}

func newKnowledgeChunkEmbeddingID() string {
	return "knowledge_chunk_embedding_" + ulid.Make().String()
}

func mustEmbeddingWorkerJSON(value any) json.RawMessage {
	payload, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return payload
}
