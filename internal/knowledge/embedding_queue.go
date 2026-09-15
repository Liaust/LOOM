package knowledge

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

const (
	defaultEmbeddingClaimLimit    = 1
	maxEmbeddingClaimLimit        = 25
	defaultEmbeddingLeaseDuration = 10 * time.Minute
	defaultEmbeddingPriority      = 100
)

type EmbeddingQueueAllInput struct {
	Now      time.Time `json:"-"`
	Priority int       `json:"priority,omitempty"`
}

type EmbeddingQueueResult struct {
	Queued      int               `json:"queued"`
	Skipped     int               `json:"skipped"`
	Settings    EmbeddingSettings `json:"settings"`
	GeneratedAt time.Time         `json:"generated_at"`
}

type EmbeddingWorkClaimOptions struct {
	Limit         int
	LeaseDuration time.Duration
	Now           time.Time
}

type embeddingTrajectoryPlanInput struct {
	Settings           EmbeddingSettings
	Object             KnowledgeObject
	KnowledgeVersionID string
	Chunks             []KnowledgeChunk
	PreviousGeneration int64
	ChangedAt          time.Time
	Priority           int
}

type embeddingTrajectoryPlan struct {
	State        EmbeddingObjectState
	WorkItems    []EmbeddingWorkItem
	StaleBefore  int64
	EmbeddingsOn bool
}

func (s *Service) GetEmbeddingStatus(ctx context.Context) (EmbeddingStatus, error) {
	if s == nil || s.store.db == nil {
		return EmbeddingStatus{}, fmt.Errorf("knowledge store is not configured")
	}
	now := s.currentTime()
	settings, err := s.store.GetEmbeddingSettings(ctx)
	if err != nil {
		return EmbeddingStatus{}, err
	}
	queueCounts, objectCounts, err := s.store.GetUnifiedEmbeddingCounts(ctx)
	if err != nil {
		return EmbeddingStatus{}, err
	}
	active, historical, reusable, err := s.store.GetEmbeddingVectorCounts(ctx)
	if err != nil {
		return EmbeddingStatus{}, err
	}
	return EmbeddingStatus{
		Settings:        settings,
		Queue:           queueCounts,
		Objects:         objectCounts,
		ActiveVectors:   active,
		Historical:      historical,
		ReusableVectors: reusable,
		GeneratedAt:     now,
	}, nil
}

func (s Store) GetUnifiedEmbeddingCounts(ctx context.Context) (EmbeddingQueueCounts, EmbeddingObjectCounts, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.status,count(*)::int FROM knowledge.pipeline_stage_runs s JOIN knowledge.pipeline_runs r ON r.knowledge_pipeline_run_id=s.knowledge_pipeline_run_id WHERE s.stage_key='embedding' AND r.status NOT IN ('stale','cancelled') GROUP BY s.status`)
	if err != nil {
		return EmbeddingQueueCounts{}, EmbeddingObjectCounts{}, err
	}
	defer rows.Close()
	var queue EmbeddingQueueCounts
	var objects EmbeddingObjectCounts
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return queue, objects, err
		}
		switch status {
		case PipelineStageStatusReady, PipelineStageStatusWaitingDependency, PipelineStageStatusWaitingQuietWindow, PipelineStageStatusFailedRetryable:
			queue.Queued += count
			objects.Queued += count
		case PipelineStageStatusProcessing:
			queue.Processing += count
			objects.Processing += count
		case PipelineStageStatusComplete, PipelineStageStatusCompleteWithWarning:
			queue.Complete += count
			objects.Complete += count
		case PipelineStageStatusSkippedByPolicy:
			queue.DisabledByPolicy += count
			objects.DisabledByPolicy += count
		case PipelineStageStatusSkippedNotApplicable:
			queue.SkippedUnsupported += count
			objects.SkippedUnsupported += count
		case PipelineStageStatusStale:
			queue.Stale += count
			objects.Stale += count
		case PipelineStageStatusBlockedManual:
			queue.Failed += count
			objects.Failed += count
		}
	}
	return queue, objects, rows.Err()
}

func (s *Service) SetEmbeddingsEnabled(ctx context.Context, enabled bool) (SetEmbeddingsEnabledResult, error) {
	if s == nil || s.store.db == nil {
		return SetEmbeddingsEnabledResult{}, fmt.Errorf("knowledge store is not configured")
	}
	policy, queued, err := s.updatePipelinePolicyAndReconcile(ctx, PipelinePolicyUpdate{EmbeddingsEnabled: &enabled})
	if err != nil {
		return SetEmbeddingsEnabledResult{}, err
	}
	if enabled {
		backfill, queueErr := s.QueueAllCurrentEmbeddingWork(ctx, EmbeddingQueueAllInput{})
		if queueErr != nil {
			return SetEmbeddingsEnabledResult{}, queueErr
		}
		queued += backfill.Queued
	}
	status, err := s.GetEmbeddingStatus(ctx)
	if err != nil {
		return SetEmbeddingsEnabledResult{}, err
	}
	return SetEmbeddingsEnabledResult{
		Settings: policy.EmbeddingSettings,
		Queued:   queued,
		Status:   status,
	}, nil
}

func (s *Service) QueueAllCurrentEmbeddingWork(ctx context.Context, input EmbeddingQueueAllInput) (EmbeddingQueueResult, error) {
	if s == nil || s.store.db == nil {
		return EmbeddingQueueResult{}, fmt.Errorf("knowledge store is not configured")
	}
	now := input.Now
	if now.IsZero() {
		now = s.currentTime()
	}
	settings, err := s.store.GetEmbeddingSettings(ctx)
	if err != nil {
		return EmbeddingQueueResult{}, err
	}
	result := EmbeddingQueueResult{
		Settings:    settings,
		GeneratedAt: now,
	}
	if !settings.Enabled {
		return result, nil
	}
	chunksByObject, objectsByID, err := s.listCurrentEmbeddingChunks(ctx)
	if err != nil {
		return EmbeddingQueueResult{}, err
	}
	policy, err := s.GetPipelinePolicy(ctx)
	if err != nil {
		return EmbeddingQueueResult{}, err
	}
	for objectID := range chunksByObject {
		object := objectsByID[objectID]
		_, created, err := s.ensurePipelineRun(ctx, object, policy.Policy, false, input.Priority)
		if err != nil {
			return EmbeddingQueueResult{}, err
		}
		if created {
			result.Queued++
		} else {
			result.Skipped++
		}
	}
	return result, nil
}

func (s *Service) RefreshEmbeddingTrajectoryForObject(ctx context.Context, object KnowledgeObject, versionID string, chunks []KnowledgeChunk, changedAt time.Time, priority int) (int, error) {
	policy, err := s.GetPipelinePolicy(ctx)
	if err != nil {
		return 0, err
	}
	_, created, err := s.ensurePipelineRun(ctx, object, policy.Policy, false, priority)
	if err != nil {
		return 0, err
	}
	if !created {
		return 0, nil
	}
	return 1, nil
}

func (s *Service) ReleaseExpiredEmbeddingClaims(ctx context.Context, now time.Time) (int, error) {
	if s == nil || s.store.db == nil {
		return 0, fmt.Errorf("knowledge store is not configured")
	}
	if now.IsZero() {
		now = s.currentTime()
	}
	result, err := s.store.db.ExecContext(ctx, `
		UPDATE knowledge.embedding_work_items
		SET status = 'queued',
		    claimed_by_worker_run_id = '',
		    claim_expires_at = null,
		    updated_at = now()
		WHERE status = 'processing'
		  AND claimed_by_worker_run_id <> ''
		  AND claim_expires_at IS NOT NULL
		  AND claim_expires_at <= $1
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

func (s *Service) ClaimEmbeddingWork(ctx context.Context, workerRunID string, opts EmbeddingWorkClaimOptions) ([]EmbeddingWorkItem, error) {
	if s == nil || s.store.db == nil {
		return nil, fmt.Errorf("knowledge store is not configured")
	}
	workerRunID = strings.TrimSpace(workerRunID)
	if workerRunID == "" {
		return nil, fmt.Errorf("%w: worker_run_id is required", ErrInvalid)
	}
	opts = normalizeEmbeddingWorkClaimOptions(opts)
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
			SELECT wi.knowledge_embedding_work_item_id
			FROM knowledge.embedding_work_items wi
			JOIN knowledge.embedding_object_states eos
			  ON eos.knowledge_object_id = wi.knowledge_object_id
			 AND eos.runtime_key = wi.runtime_key
			 AND eos.model_key = wi.model_key
			 AND eos.dimensions = wi.dimensions
			JOIN knowledge.knowledge_objects ko
			  ON ko.knowledge_object_id = wi.knowledge_object_id
			LEFT JOIN knowledge.knowledge_chunks kc
			  ON kc.knowledge_chunk_id = wi.knowledge_chunk_id
			WHERE EXISTS (
				SELECT 1
				FROM knowledge.embedding_settings es
				WHERE es.embedding_settings_id = $1
				  AND es.enabled = true
			)
			  AND wi.status = 'queued'
			  AND wi.eligible_at <= $2
			  AND wi.generation = eos.generation
			  AND ko.deleted_at IS NULL
			  AND (wi.knowledge_chunk_id IS NULL OR kc.status IN ('created', 'indexed'))
			  AND (
				wi.claimed_by_worker_run_id = ''
				OR wi.claim_expires_at IS NULL
				OR wi.claim_expires_at <= $2
			  )
			ORDER BY wi.priority ASC, wi.eligible_at ASC, wi.queued_at ASC
			LIMIT $3
			FOR UPDATE SKIP LOCKED
		)
		UPDATE knowledge.embedding_work_items wi
		SET status = 'processing',
		    claimed_by_worker_run_id = $4,
		    claim_expires_at = $5,
		    started_at = COALESCE(started_at, $2),
		    updated_at = now()
		FROM candidates
		WHERE wi.knowledge_embedding_work_item_id = candidates.knowledge_embedding_work_item_id
		RETURNING `+embeddingWorkItemColumns(),
		EmbeddingSettingsID,
		now,
		opts.Limit,
		workerRunID,
		claimExpiresAt,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []EmbeddingWorkItem{}
	for rows.Next() {
		item, err := scanEmbeddingWorkItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Service) refreshEmbeddingTrajectoryTx(ctx context.Context, tx *sql.Tx, object KnowledgeObject, versionID string, chunks []KnowledgeChunk, changedAt time.Time, priority int) (int, error) {
	return 0, fmt.Errorf("%w: specialized embedding queue is deprecated; create a unified pipeline run", ErrInvalid)
	/* Rollback-only trajectory construction retained below for schema/history compatibility.
	settings, err := getEmbeddingSettingsTx(ctx, tx)
	if err != nil {
		return 0, err
	}
	previousGeneration, err := getEmbeddingGenerationForUpdateTx(ctx, tx, object.KnowledgeObjectID, settings)
	if err != nil {
		return 0, err
	}
	plan, err := buildEmbeddingTrajectoryPlan(embeddingTrajectoryPlanInput{
		Settings:           settings,
		Object:             object,
		KnowledgeVersionID: versionID,
		Chunks:             chunks,
		PreviousGeneration: previousGeneration,
		ChangedAt:          changedAt,
		Priority:           priority,
	})
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE knowledge.embedding_work_items
		SET status = 'stale',
		    updated_at = now(),
		    metadata = jsonb_set(metadata, '{superseded_by_generation}', to_jsonb($3::bigint), true)
		WHERE knowledge_object_id = $1
		  AND runtime_key = $4
		  AND model_key = $5
		  AND dimensions = $6
		  AND status = 'queued'
		  AND generation < $2
	`, object.KnowledgeObjectID, plan.StaleBefore, plan.State.Generation, settings.RuntimeKey, settings.ModelKey, settings.Dimensions); err != nil {
		return 0, err
	}
	if err := upsertEmbeddingObjectStateTx(ctx, tx, plan.State); err != nil {
		return 0, err
	}
	for _, item := range plan.WorkItems {
		if err := insertEmbeddingWorkItemTx(ctx, tx, item); err != nil {
			return 0, err
		}
	}
	return len(plan.WorkItems), nil */
}

func markEmbeddingObjectChangedTx(ctx context.Context, tx *sql.Tx, objectID string, changedAt time.Time) error {
	objectID = strings.TrimSpace(objectID)
	if objectID == "" {
		return fmt.Errorf("%w: knowledge_object_id is required for embedding trajectory reset", ErrInvalid)
	}
	// Unified pipeline reconciliation owns trajectory invalidation. The legacy
	// embedding queue tables are rollback-only and must not block object writes.
	return nil
}

func (s *Service) listCurrentEmbeddingChunks(ctx context.Context) (map[string][]KnowledgeChunk, map[string]KnowledgeObject, error) {
	rows, err := s.store.db.QueryContext(ctx, `
		WITH latest_versions AS (
			SELECT knowledge_object_id, MAX(version_number) AS version_number
			FROM knowledge.knowledge_object_versions
			GROUP BY knowledge_object_id
		)
		SELECT `+knowledgeObjectColumnsWithAlias("ko")+`, `+knowledgeChunkColumnsWithAlias("kc")+`
		FROM knowledge.knowledge_objects ko
		JOIN latest_versions latest
		  ON latest.knowledge_object_id = ko.knowledge_object_id
		JOIN knowledge.knowledge_object_versions kov
		  ON kov.knowledge_object_id = latest.knowledge_object_id
		 AND kov.version_number = latest.version_number
		JOIN knowledge.knowledge_chunks kc
		  ON kc.knowledge_object_version_id = kov.knowledge_object_version_id
		WHERE ko.deleted_at IS NULL
		  AND ko.processing_state IN ('chunked', 'indexed', 'embedded', 'stale')
		  AND kc.status IN ('created', 'indexed')
		  AND `+visibleNotesKnowledgeRelativePathSQL("ko.relative_path")+`
		ORDER BY ko.updated_at ASC, ko.relative_path ASC, kc.chunk_index ASC
	`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	chunksByObject := map[string][]KnowledgeChunk{}
	objectsByID := map[string]KnowledgeObject{}
	for rows.Next() {
		object, chunk, err := scanKnowledgeObjectAndChunk(rows)
		if err != nil {
			return nil, nil, err
		}
		definition := PipelineDefinitionForObject(object)
		eligible := false
		for _, stage := range definition.Stages {
			if stage.StageKey == FilePipelineStageEmbedding {
				eligible = true
				break
			}
		}
		if !eligible {
			continue
		}
		objectsByID[object.KnowledgeObjectID] = object
		chunksByObject[object.KnowledgeObjectID] = append(chunksByObject[object.KnowledgeObjectID], chunk)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return chunksByObject, objectsByID, nil
}

func buildEmbeddingTrajectoryPlan(input embeddingTrajectoryPlanInput) (embeddingTrajectoryPlan, error) {
	settings := input.Settings
	if settings.EmbeddingSettingsID == "" {
		settings = defaultEmbeddingSettings(time.Time{})
	}
	if settings.RuntimeKey == "" {
		settings.RuntimeKey = EmbeddingRuntimeOllama
	}
	if settings.ModelKey == "" {
		settings.ModelKey = EmbeddingModelMXBAIEmbedLarge
	}
	if settings.Dimensions <= 0 {
		settings.Dimensions = DefaultEmbeddingDimensions
	}
	if settings.DistanceMetric == "" {
		settings.DistanceMetric = EmbeddingDistanceCosine
	}
	if settings.QuietWindowSeconds <= 0 {
		settings.QuietWindowSeconds = DefaultEmbeddingQuietWindowSec
	}
	if input.ChangedAt.IsZero() {
		input.ChangedAt = time.Now().UTC()
	}
	priority := input.Priority
	if priority <= 0 {
		priority = defaultEmbeddingPriority
	}
	nextGeneration := input.PreviousGeneration + 1
	eligibleAt := input.ChangedAt.Add(time.Duration(settings.QuietWindowSeconds) * time.Second)
	status := EmbeddingObjectStatusDisabledByPolicy
	var stateEligibleAt *time.Time
	if settings.Enabled {
		if len(input.Chunks) == 0 {
			status = EmbeddingObjectStatusComplete
		} else {
			status = EmbeddingObjectStatusQueued
			stateEligibleAt = &eligibleAt
		}
	}
	state := EmbeddingObjectState{
		KnowledgeObjectID:        input.Object.KnowledgeObjectID,
		KnowledgeObjectVersionID: stringPtrIfNotEmpty(input.KnowledgeVersionID),
		RuntimeKey:               settings.RuntimeKey,
		ModelKey:                 settings.ModelKey,
		Dimensions:               settings.Dimensions,
		Status:                   status,
		Generation:               nextGeneration,
		LastContentChangeAt:      &input.ChangedAt,
		EligibleAt:               stateEligibleAt,
		Metadata:                 emptyJSONObject,
		CreatedAt:                input.ChangedAt,
		UpdatedAt:                input.ChangedAt,
	}
	workItems := []EmbeddingWorkItem{}
	if settings.Enabled {
		for _, chunk := range input.Chunks {
			if strings.TrimSpace(chunk.KnowledgeChunkID) == "" {
				return embeddingTrajectoryPlan{}, fmt.Errorf("%w: knowledge_chunk_id is required for embedding queue work", ErrInvalid)
			}
			chunkID := chunk.KnowledgeChunkID
			versionID := input.KnowledgeVersionID
			if versionID == "" && chunk.KnowledgeObjectVersionID != nil {
				versionID = *chunk.KnowledgeObjectVersionID
			}
			workItems = append(workItems, EmbeddingWorkItem{
				KnowledgeEmbeddingWorkItemID: newKnowledgeEmbeddingWorkItemID(),
				KnowledgeObjectID:            input.Object.KnowledgeObjectID,
				KnowledgeObjectVersionID:     stringPtrIfNotEmpty(versionID),
				KnowledgeChunkID:             &chunkID,
				RuntimeKey:                   settings.RuntimeKey,
				ModelKey:                     settings.ModelKey,
				Dimensions:                   settings.Dimensions,
				Generation:                   nextGeneration,
				ChunkHash:                    chunk.ChunkHash,
				ChunkerVersion:               chunk.ChunkerVersion,
				Status:                       EmbeddingWorkStatusQueued,
				EligibleAt:                   eligibleAt,
				QueuedAt:                     input.ChangedAt,
				AttemptCount:                 0,
				Priority:                     priority,
				Metadata:                     emptyJSONObject,
				CreatedAt:                    input.ChangedAt,
				UpdatedAt:                    input.ChangedAt,
			})
		}
	}
	return embeddingTrajectoryPlan{
		State:        state,
		WorkItems:    workItems,
		StaleBefore:  nextGeneration,
		EmbeddingsOn: settings.Enabled,
	}, nil
}

func normalizeEmbeddingWorkClaimOptions(opts EmbeddingWorkClaimOptions) EmbeddingWorkClaimOptions {
	if opts.Limit <= 0 {
		opts.Limit = defaultEmbeddingClaimLimit
	}
	if opts.Limit > maxEmbeddingClaimLimit {
		opts.Limit = maxEmbeddingClaimLimit
	}
	if opts.LeaseDuration <= 0 {
		opts.LeaseDuration = defaultEmbeddingLeaseDuration
	}
	return opts
}

func limitReadyEmbeddingWorkItems(items []EmbeddingWorkItem, limit int, now time.Time) []EmbeddingWorkItem {
	opts := normalizeEmbeddingWorkClaimOptions(EmbeddingWorkClaimOptions{Limit: limit})
	out := []EmbeddingWorkItem{}
	for _, item := range items {
		if item.Status != EmbeddingWorkStatusQueued || item.EligibleAt.After(now) {
			continue
		}
		out = append(out, item)
		if len(out) >= opts.Limit {
			return out
		}
	}
	return out
}

func getEmbeddingSettingsTx(ctx context.Context, tx *sql.Tx) (EmbeddingSettings, error) {
	settings, err := scanEmbeddingSettings(tx.QueryRowContext(ctx, `SELECT `+embeddingSettingsColumns()+`
		FROM knowledge.embedding_settings
		WHERE embedding_settings_id = $1
		FOR UPDATE`, EmbeddingSettingsID))
	if err == sql.ErrNoRows {
		return defaultEmbeddingSettings(time.Now().UTC()), nil
	}
	return settings, err
}

func getEmbeddingGenerationForUpdateTx(ctx context.Context, tx *sql.Tx, objectID string, settings EmbeddingSettings) (int64, error) {
	var generation int64
	err := tx.QueryRowContext(ctx, `
		SELECT generation
		FROM knowledge.embedding_object_states
		WHERE knowledge_object_id = $1
		  AND runtime_key = $2
		  AND model_key = $3
		  AND dimensions = $4
		FOR UPDATE
	`, objectID, settings.RuntimeKey, settings.ModelKey, settings.Dimensions).Scan(&generation)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return generation, err
}

func upsertEmbeddingObjectStateTx(ctx context.Context, tx *sql.Tx, state EmbeddingObjectState) error {
	return fmt.Errorf("%w: specialized embedding object states are deprecated", ErrInvalid)
	/* Rollback-only writer retained with the historical tables.
	_, err := tx.ExecContext(ctx, `
		INSERT INTO knowledge.embedding_object_states (
			knowledge_object_id, knowledge_object_version_id, runtime_key,
			model_key, dimensions, status, generation, last_content_change_at,
			eligible_at, last_queued_at, last_started_at, last_completed_at,
			last_failed_at, last_error_code, last_error_message, metadata,
			created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8::timestamptz,$9::timestamptz,
		        CASE WHEN $6::text = 'queued' THEN $8::timestamptz ELSE null::timestamptz END,
		        null,null,null,'','',$10,$11,$12)
		ON CONFLICT (knowledge_object_id, runtime_key, model_key, dimensions)
		DO UPDATE
		SET knowledge_object_version_id = EXCLUDED.knowledge_object_version_id,
		    status = EXCLUDED.status,
		    generation = EXCLUDED.generation,
		    last_content_change_at = EXCLUDED.last_content_change_at,
		    eligible_at = EXCLUDED.eligible_at,
		    last_queued_at = EXCLUDED.last_queued_at,
		    last_error_code = '',
		    last_error_message = '',
		    metadata = EXCLUDED.metadata,
		    updated_at = EXCLUDED.updated_at
	`, state.KnowledgeObjectID, nullableString(state.KnowledgeObjectVersionID), state.RuntimeKey, state.ModelKey, state.Dimensions, state.Status, state.Generation, nullableTime(state.LastContentChangeAt), nullableTime(state.EligibleAt), state.Metadata, state.CreatedAt, state.UpdatedAt)
	return err */
}

func insertEmbeddingWorkItemTx(ctx context.Context, tx *sql.Tx, item EmbeddingWorkItem) error {
	return fmt.Errorf("%w: specialized embedding work items are deprecated", ErrInvalid)
	/* Rollback-only writer retained with the historical tables.
	_, err := tx.ExecContext(ctx, `
		INSERT INTO knowledge.embedding_work_items (
			knowledge_embedding_work_item_id, knowledge_object_id,
			knowledge_object_version_id, knowledge_chunk_id, runtime_key,
			model_key, dimensions, generation, chunk_hash, chunker_version,
			status, eligible_at, queued_at, started_at, completed_at, failed_at,
			attempt_count, priority, claimed_by_worker_run_id, claim_expires_at,
			last_error_code, last_error_message, metadata, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,
		        $11,$12,$13,null,null,null,$14,$15,'',null,'','',$16,$17,$18)
		ON CONFLICT (knowledge_chunk_id, runtime_key, model_key, dimensions, generation)
		WHERE knowledge_chunk_id IS NOT NULL AND status IN ('queued', 'processing')
		DO UPDATE
		SET status = 'queued',
		    eligible_at = EXCLUDED.eligible_at,
		    queued_at = EXCLUDED.queued_at,
		    started_at = null,
		    completed_at = null,
		    failed_at = null,
		    attempt_count = 0,
		    priority = EXCLUDED.priority,
		    claimed_by_worker_run_id = '',
		    claim_expires_at = null,
		    last_error_code = '',
		    last_error_message = '',
		    metadata = EXCLUDED.metadata,
		    updated_at = EXCLUDED.updated_at
	`, item.KnowledgeEmbeddingWorkItemID, item.KnowledgeObjectID, nullableString(item.KnowledgeObjectVersionID), nullableString(item.KnowledgeChunkID), item.RuntimeKey, item.ModelKey, item.Dimensions, item.Generation, item.ChunkHash, item.ChunkerVersion, item.Status, item.EligibleAt, item.QueuedAt, item.AttemptCount, item.Priority, item.Metadata, item.CreatedAt, item.UpdatedAt)
	return err */
}

func embeddingWorkItemColumns() string {
	return `knowledge_embedding_work_item_id, knowledge_object_id,
	        knowledge_object_version_id, knowledge_chunk_id, runtime_key,
	        model_key, dimensions, generation, chunk_hash, chunker_version,
	        status, eligible_at, queued_at, started_at, completed_at, failed_at,
	        attempt_count, priority, claimed_by_worker_run_id, claim_expires_at,
	        last_error_code, last_error_message, metadata, created_at, updated_at`
}

type embeddingWorkItemScanner interface {
	Scan(dest ...any) error
}

func scanEmbeddingWorkItem(scanner embeddingWorkItemScanner) (EmbeddingWorkItem, error) {
	var item EmbeddingWorkItem
	var versionID, chunkID sql.NullString
	var startedAt, completedAt, failedAt, claimExpiresAt sql.NullTime
	var metadata []byte
	if err := scanner.Scan(
		&item.KnowledgeEmbeddingWorkItemID,
		&item.KnowledgeObjectID,
		&versionID,
		&chunkID,
		&item.RuntimeKey,
		&item.ModelKey,
		&item.Dimensions,
		&item.Generation,
		&item.ChunkHash,
		&item.ChunkerVersion,
		&item.Status,
		&item.EligibleAt,
		&item.QueuedAt,
		&startedAt,
		&completedAt,
		&failedAt,
		&item.AttemptCount,
		&item.Priority,
		&item.ClaimedByWorkerRunID,
		&claimExpiresAt,
		&item.LastErrorCode,
		&item.LastErrorMessage,
		&metadata,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return EmbeddingWorkItem{}, err
	}
	item.KnowledgeObjectVersionID = nullStringPtr(versionID)
	item.KnowledgeChunkID = nullStringPtr(chunkID)
	item.StartedAt = nullTimePtr(startedAt)
	item.CompletedAt = nullTimePtr(completedAt)
	item.FailedAt = nullTimePtr(failedAt)
	item.ClaimExpiresAt = nullTimePtr(claimExpiresAt)
	item.Metadata = jsonObjectOrEmpty(metadata)
	return item, nil
}

func scanKnowledgeObjectAndChunk(scanner interface{ Scan(dest ...any) error }) (KnowledgeObject, KnowledgeChunk, error) {
	var object KnowledgeObject
	var chunk KnowledgeChunk
	var objectStorageEntryID, sourceNodeID, projectID sql.NullString
	var objectSizeBytes sql.NullInt64
	var sourceCreatedAt, sourceModifiedAt, lastProcessedAt, deletedAt sql.NullTime
	var absoluteTimeMetadata, objectMetadata []byte
	var versionID sql.NullString
	var startOffset, endOffset, tokenCount sql.NullInt64
	var indexedAt sql.NullTime
	var chunkMetadata []byte
	if err := scanner.Scan(
		&object.KnowledgeObjectID,
		&object.NotesSourceRootID,
		&objectStorageEntryID,
		&sourceNodeID,
		&object.SourceNodeKey,
		&projectID,
		&object.SourcePath,
		&object.RelativePath,
		&object.Title,
		&object.FileClass,
		&object.MimeType,
		&objectSizeBytes,
		&object.SourceHash,
		&object.SourceRevision,
		&object.ProcessingState,
		&object.PipelineKey,
		&object.PipelineVersion,
		&sourceCreatedAt,
		&sourceModifiedAt,
		&object.RecencyAt,
		&object.RecencyBasis,
		&absoluteTimeMetadata,
		&object.LastSeenAt,
		&lastProcessedAt,
		&object.LastErrorCode,
		&object.LastErrorMessage,
		&objectMetadata,
		&object.CreatedAt,
		&object.UpdatedAt,
		&deletedAt,
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
		&chunkMetadata,
		&chunk.CreatedAt,
		&indexedAt,
	); err != nil {
		return KnowledgeObject{}, KnowledgeChunk{}, err
	}
	object.StorageEntryID = nullStringPtr(objectStorageEntryID)
	object.SourceNodeID = nullStringPtr(sourceNodeID)
	object.ProjectID = nullStringPtr(projectID)
	object.SizeBytes = nullInt64Ptr(objectSizeBytes)
	object.SourceCreatedAt = nullTimePtr(sourceCreatedAt)
	object.SourceModifiedAt = nullTimePtr(sourceModifiedAt)
	object.AbsoluteTimeMetadata = jsonObjectOrEmpty(absoluteTimeMetadata)
	object.LastProcessedAt = nullTimePtr(lastProcessedAt)
	object.DeletedAt = nullTimePtr(deletedAt)
	object.Metadata = jsonObjectOrEmpty(objectMetadata)
	chunk.KnowledgeObjectVersionID = nullStringPtr(versionID)
	chunk.StartOffset = nullIntPtr(startOffset)
	chunk.EndOffset = nullIntPtr(endOffset)
	chunk.TokenCountEstimate = nullIntPtr(tokenCount)
	chunk.Metadata = jsonObjectOrEmpty(chunkMetadata)
	chunk.IndexedAt = nullTimePtr(indexedAt)
	return object, chunk, nil
}

func knowledgeObjectColumnsWithAlias(alias string) string {
	columns := strings.Split(knowledgeObjectColumns(), ",")
	for i, column := range columns {
		columns[i] = alias + "." + strings.TrimSpace(column)
	}
	return strings.Join(columns, ", ")
}

func knowledgeChunkColumnsWithAlias(alias string) string {
	columns := strings.Split(knowledgeChunkColumns(), ",")
	for i, column := range columns {
		columns[i] = alias + "." + strings.TrimSpace(column)
	}
	return strings.Join(columns, ", ")
}

func newKnowledgeEmbeddingWorkItemID() string {
	return "knowledge_embedding_work_item_" + ulid.Make().String()
}
