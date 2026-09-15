package knowledge

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

func (s Store) GetEmbeddingSettings(ctx context.Context) (EmbeddingSettings, error) {
	if s.db == nil {
		return EmbeddingSettings{}, fmt.Errorf("knowledge store is not configured")
	}
	settings, err := scanEmbeddingSettings(s.db.QueryRowContext(ctx, `SELECT `+embeddingSettingsColumns()+`
		FROM knowledge.embedding_settings
		WHERE embedding_settings_id = $1`, EmbeddingSettingsID))
	if err == sql.ErrNoRows {
		return defaultEmbeddingSettings(time.Time{}), nil
	}
	return settings, err
}

func (s Store) UpsertEmbeddingSettingsEnabled(ctx context.Context, enabled bool) (EmbeddingSettings, error) {
	if s.db == nil {
		return EmbeddingSettings{}, fmt.Errorf("knowledge store is not configured")
	}
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO knowledge.embedding_settings (
			embedding_settings_id, enabled, runtime_key, model_key, dimensions,
			distance_metric, ollama_url, quiet_window_seconds, global_concurrency,
			history_per_lineage, metadata, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'{}'::jsonb,now(),now())
		ON CONFLICT (embedding_settings_id)
		DO UPDATE
		SET enabled = EXCLUDED.enabled,
		    updated_at = now()
		RETURNING `+embeddingSettingsColumns(),
		EmbeddingSettingsID,
		enabled,
		EmbeddingRuntimeOllama,
		EmbeddingModelMXBAIEmbedLarge,
		DefaultEmbeddingDimensions,
		EmbeddingDistanceCosine,
		"http://127.0.0.1:11434",
		DefaultEmbeddingQuietWindowSec,
		DefaultEmbeddingConcurrency,
		DefaultEmbeddingHistoryPerLineage,
	)
	return scanEmbeddingSettings(row)
}

func (s Store) GetEmbeddingQueueCounts(ctx context.Context, now time.Time) (EmbeddingQueueCounts, error) {
	if s.db == nil {
		return EmbeddingQueueCounts{}, fmt.Errorf("knowledge store is not configured")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT status,
		       COUNT(*)::int,
		       COUNT(*) FILTER (WHERE status = 'queued' AND eligible_at <= $1)::int
		FROM knowledge.embedding_work_items
		GROUP BY status
	`, now)
	if err != nil {
		return EmbeddingQueueCounts{}, err
	}
	defer rows.Close()
	var counts EmbeddingQueueCounts
	for rows.Next() {
		var status string
		var count int
		var ready int
		if err := rows.Scan(&status, &count, &ready); err != nil {
			return EmbeddingQueueCounts{}, err
		}
		addEmbeddingQueueCount(&counts, status, count)
		counts.Ready += ready
	}
	return counts, rows.Err()
}

func (s Store) GetEmbeddingObjectCounts(ctx context.Context) (EmbeddingObjectCounts, error) {
	if s.db == nil {
		return EmbeddingObjectCounts{}, fmt.Errorf("knowledge store is not configured")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT status, COUNT(*)::int
		FROM knowledge.embedding_object_states
		GROUP BY status
	`)
	if err != nil {
		return EmbeddingObjectCounts{}, err
	}
	defer rows.Close()
	var counts EmbeddingObjectCounts
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return EmbeddingObjectCounts{}, err
		}
		addEmbeddingObjectCount(&counts, status, count)
	}
	return counts, rows.Err()
}

func (s Store) GetEmbeddingVectorCounts(ctx context.Context) (active int, historical int, reusable int, err error) {
	if s.db == nil {
		return 0, 0, 0, fmt.Errorf("knowledge store is not configured")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT status, COUNT(*)::int
		FROM knowledge.chunk_embeddings
		GROUP BY status
	`)
	if err != nil {
		return 0, 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return 0, 0, 0, err
		}
		switch status {
		case ChunkEmbeddingStatusActive:
			active += count
		case ChunkEmbeddingStatusHistorical:
			historical += count
		case ChunkEmbeddingStatusReusable:
			reusable += count
		}
	}
	return active, historical, reusable, rows.Err()
}

func defaultEmbeddingSettings(now time.Time) EmbeddingSettings {
	return EmbeddingSettings{
		EmbeddingSettingsID: EmbeddingSettingsID,
		Enabled:             false,
		RuntimeKey:          EmbeddingRuntimeOllama,
		ModelKey:            EmbeddingModelMXBAIEmbedLarge,
		Dimensions:          DefaultEmbeddingDimensions,
		DistanceMetric:      EmbeddingDistanceCosine,
		OllamaURL:           "http://127.0.0.1:11434",
		QuietWindowSeconds:  DefaultEmbeddingQuietWindowSec,
		GlobalConcurrency:   DefaultEmbeddingConcurrency,
		HistoryPerLineage:   DefaultEmbeddingHistoryPerLineage,
		Metadata:            emptyJSONObject,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
}

func embeddingSettingsColumns() string {
	return `embedding_settings_id, enabled, runtime_key, model_key, dimensions,
	        distance_metric, ollama_url, quiet_window_seconds, global_concurrency,
	        history_per_lineage, metadata, created_at, updated_at`
}

type embeddingSettingsScanner interface {
	Scan(dest ...any) error
}

func scanEmbeddingSettings(scanner embeddingSettingsScanner) (EmbeddingSettings, error) {
	var settings EmbeddingSettings
	var metadata []byte
	if err := scanner.Scan(
		&settings.EmbeddingSettingsID,
		&settings.Enabled,
		&settings.RuntimeKey,
		&settings.ModelKey,
		&settings.Dimensions,
		&settings.DistanceMetric,
		&settings.OllamaURL,
		&settings.QuietWindowSeconds,
		&settings.GlobalConcurrency,
		&settings.HistoryPerLineage,
		&metadata,
		&settings.CreatedAt,
		&settings.UpdatedAt,
	); err != nil {
		return EmbeddingSettings{}, err
	}
	settings.Metadata = jsonObjectOrEmpty(metadata)
	return settings, nil
}

func addEmbeddingQueueCount(counts *EmbeddingQueueCounts, status string, count int) {
	switch status {
	case EmbeddingWorkStatusQueued:
		counts.Queued += count
	case EmbeddingWorkStatusProcessing:
		counts.Processing += count
	case EmbeddingWorkStatusComplete:
		counts.Complete += count
	case EmbeddingWorkStatusStale:
		counts.Stale += count
	case EmbeddingWorkStatusFailed:
		counts.Failed += count
	case EmbeddingWorkStatusDisabledByPolicy:
		counts.DisabledByPolicy += count
	case EmbeddingWorkStatusSkippedUnsupported:
		counts.SkippedUnsupported += count
	}
}

func addEmbeddingObjectCount(counts *EmbeddingObjectCounts, status string, count int) {
	switch status {
	case EmbeddingObjectStatusNotStarted:
		counts.NotStarted += count
	case EmbeddingObjectStatusQueued:
		counts.Queued += count
	case EmbeddingObjectStatusProcessing:
		counts.Processing += count
	case EmbeddingObjectStatusComplete:
		counts.Complete += count
	case EmbeddingObjectStatusStale:
		counts.Stale += count
	case EmbeddingObjectStatusFailed:
		counts.Failed += count
	case EmbeddingObjectStatusDisabledByPolicy:
		counts.DisabledByPolicy += count
	case EmbeddingObjectStatusSkippedUnsupported:
		counts.SkippedUnsupported += count
	}
}
