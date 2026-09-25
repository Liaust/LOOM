package knowledge

import (
	"context"
	"fmt"
)

const defaultEmbeddingRetentionBatchLimit = 500

func (s *Service) PruneEmbeddingRetention(ctx context.Context, historyPerLineage int, batchLimit int) (int, error) {
	if s == nil || s.store.db == nil {
		return 0, fmt.Errorf("knowledge store is not configured")
	}
	inactiveLimit := historyPerLineage - 1
	if inactiveLimit < 0 {
		inactiveLimit = DefaultEmbeddingHistoryPerLineage - 1
	}
	if batchLimit <= 0 {
		batchLimit = defaultEmbeddingRetentionBatchLimit
	}
	result, err := s.store.db.ExecContext(ctx, `
		WITH ranked AS (
			SELECT knowledge_chunk_embedding_id,
			       row_number() OVER (
					PARTITION BY knowledge_object_id, chunk_hash, chunker_version,
					             runtime_key, model_key, dimensions
					ORDER BY created_at DESC, knowledge_chunk_embedding_id DESC
			       ) AS rn
			FROM knowledge.chunk_embeddings
			WHERE active = false
			  AND status IN ('historical', 'reusable', 'stale')
			  AND NOT EXISTS (SELECT 1 FROM knowledge.knowledge_objects o
			    WHERE o.knowledge_object_id=knowledge.chunk_embeddings.knowledge_object_id
			    AND knowledge.chunk_embeddings.knowledge_object_version_id IN (o.lexical_version_id,o.semantic_version_id))
			  AND NOT EXISTS (SELECT 1 FROM knowledge.pipeline_runs r
			    WHERE r.knowledge_object_version_id=knowledge.chunk_embeddings.knowledge_object_version_id
			    AND r.status IN ('processing','queued','waiting_coordinator','waiting_heavy','waiting_quiet_window'))
		),
		victims AS (
			SELECT knowledge_chunk_embedding_id
			FROM ranked
			WHERE rn > $1
			LIMIT $2
		)
		DELETE FROM knowledge.chunk_embeddings ce
		USING victims
		WHERE ce.knowledge_chunk_embedding_id = victims.knowledge_chunk_embedding_id
		  AND ce.active = false
	`, inactiveLimit, batchLimit)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}

type embeddingRetentionCandidate struct {
	ID      string
	Lineage string
	Active  bool
	Created int
}

func retainedEmbeddingIDs(candidates []embeddingRetentionCandidate, historyPerLineage int) map[string]struct{} {
	inactiveLimit := historyPerLineage - 1
	if inactiveLimit < 0 {
		inactiveLimit = DefaultEmbeddingHistoryPerLineage - 1
	}
	retained := map[string]struct{}{}
	seenInactive := map[string]int{}
	for _, candidate := range candidates {
		if candidate.Active {
			retained[candidate.ID] = struct{}{}
			continue
		}
		if seenInactive[candidate.Lineage] < inactiveLimit {
			retained[candidate.ID] = struct{}{}
			seenInactive[candidate.Lineage]++
		}
	}
	return retained
}
