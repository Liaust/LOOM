package knowledge

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type EmbeddingStageHandler struct {
	Service *Service
	Runtime EmbeddingRuntime
}

func (handler EmbeddingStageHandler) Execute(ctx context.Context, item PipelineWorkItem, policy HeavyResourcePolicy) (HeavyStageObservation, []string, error) {
	settings, err := handler.Service.store.GetEmbeddingSettings(ctx)
	if err != nil {
		return HeavyStageObservation{}, nil, err
	}
	if !settings.Enabled {
		return observedHeavy(policy, 0, 0), []string{"embeddings are disabled by policy"}, nil
	}
	if handler.Runtime == nil {
		return HeavyStageObservation{}, nil, &EmbeddingRuntimeError{Kind: EmbeddingRuntimeErrorUnavailable, Message: "embedding runtime is unavailable"}
	}
	chunks, err := handler.Service.store.listKnowledgeChunksForVersion(ctx, item.Run.KnowledgeObjectVersionID)
	if err != nil {
		return HeavyStageObservation{}, nil, err
	}
	var inputBytes int64
	for _, chunk := range chunks {
		inputBytes += int64(len(chunk.ChunkText))
	}
	if err = policy.ValidateWork(len(chunks), inputBytes); err != nil {
		return HeavyStageObservation{}, nil, err
	}
	for _, chunk := range chunks {
		reused, err := handler.Service.reuseUnifiedChunkEmbedding(ctx, item, chunk, settings)
		if err != nil {
			return HeavyStageObservation{}, nil, err
		}
		if reused {
			continue
		}
		passages, err := PrepareEmbeddingPassages([]KnowledgeChunk{chunk}, EmbeddingPassageOptions{})
		if err != nil {
			return HeavyStageObservation{}, nil, err
		}
		inputs, err := EmbeddingRuntimeInputsFromPassages(passages)
		if err != nil {
			return HeavyStageObservation{}, nil, err
		}
		response, err := handler.Runtime.Embed(ctx, EmbeddingRuntimeRequest{Model: settings.ModelKey, Inputs: inputs, Truncate: false})
		if err != nil {
			return HeavyStageObservation{}, nil, err
		}
		vector, err := AverageEmbeddingVectors(response.Embeddings)
		if err != nil {
			return HeavyStageObservation{}, nil, err
		}
		if len(vector) != settings.Dimensions {
			return HeavyStageObservation{}, nil, fmt.Errorf("%w: embedding dimensions %d do not match configured dimensions %d", ErrInvalid, len(vector), settings.Dimensions)
		}
		if err = handler.Service.activateUnifiedChunkEmbedding(ctx, item, chunk, settings, vector, hashEmbeddingInput(passages)); err != nil {
			return HeavyStageObservation{}, nil, err
		}
	}
	if err = handler.Service.publishUnifiedEmbeddingObjectState(ctx, item); err != nil {
		return HeavyStageObservation{}, nil, err
	}
	return observedHeavy(policy, len(chunks), inputBytes), nil, nil
}

func (s *Service) publishUnifiedEmbeddingObjectState(ctx context.Context, item PipelineWorkItem) error {
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockPipelineClaimTx(ctx, tx, item); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE knowledge.knowledge_objects SET processing_state='embedded',updated_at=now() WHERE knowledge_object_id=$1`, item.Object.KnowledgeObjectID)
	if err != nil {
		return err
	}
	if err = requireOneRow(result, "embedding object publication fence is stale"); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s Store) listKnowledgeChunksForVersion(ctx context.Context, versionID string) ([]KnowledgeChunk, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+knowledgeChunkColumns()+` FROM knowledge.knowledge_chunks WHERE knowledge_object_version_id=$1 ORDER BY chunk_index`, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []KnowledgeChunk
	for rows.Next() {
		chunk, err := scanKnowledgeChunk(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, chunk)
	}
	return result, rows.Err()
}

func unifiedEmbeddingItem(item PipelineWorkItem, chunk KnowledgeChunk, settings EmbeddingSettings) EmbeddingWorkItem {
	return EmbeddingWorkItem{KnowledgeObjectID: chunk.KnowledgeObjectID, KnowledgeObjectVersionID: chunk.KnowledgeObjectVersionID, KnowledgeChunkID: &chunk.KnowledgeChunkID, RuntimeKey: settings.RuntimeKey, ModelKey: settings.ModelKey, Dimensions: settings.Dimensions, Generation: item.Run.Generation, ChunkHash: chunk.ChunkHash, ChunkerVersion: chunk.ChunkerVersion}
}

func (s *Service) reuseUnifiedChunkEmbedding(ctx context.Context, item PipelineWorkItem, chunk KnowledgeChunk, settings EmbeddingSettings) (bool, error) {
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err = lockPipelineClaimTx(ctx, tx, item); err != nil {
		return false, err
	}
	work := unifiedEmbeddingItem(item, chunk, settings)
	if _, err = deactivateCurrentChunkEmbeddingTx(ctx, tx, work, chunk, s.currentTime()); err != nil {
		return false, err
	}
	var inserted string
	err = tx.QueryRowContext(ctx, `WITH source_embedding AS (SELECT embedding_runtime_model_id,distance_metric,input_hash,token_count_estimate,embedding,metadata FROM knowledge.chunk_embeddings WHERE chunk_hash=$1 AND chunker_version=$2 AND runtime_key=$3 AND model_key=$4 AND dimensions=$5 AND status IN ('active','historical','reusable') ORDER BY active DESC,created_at DESC LIMIT 1) INSERT INTO knowledge.chunk_embeddings (knowledge_chunk_embedding_id,knowledge_chunk_id,knowledge_object_id,knowledge_object_version_id,embedding_runtime_model_id,runtime_key,model_key,dimensions,distance_metric,chunk_hash,chunker_version,input_hash,token_count_estimate,source_generation,embedding,status,active,metadata,created_at,activated_at) SELECT $6,$7,$8,$9,embedding_runtime_model_id,$3,$4,$5,distance_metric,$1,$2,input_hash,token_count_estimate,$10,embedding,'active',true,metadata,$11,$11 FROM source_embedding RETURNING knowledge_chunk_embedding_id`, chunk.ChunkHash, chunk.ChunkerVersion, settings.RuntimeKey, settings.ModelKey, settings.Dimensions, newKnowledgeChunkEmbeddingID(), chunk.KnowledgeChunkID, chunk.KnowledgeObjectID, nullableString(chunk.KnowledgeObjectVersionID), item.Run.Generation, s.currentTime()).Scan(&inserted)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *Service) activateUnifiedChunkEmbedding(ctx context.Context, item PipelineWorkItem, chunk KnowledgeChunk, settings EmbeddingSettings, vector []float32, inputHash string) error {
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockPipelineClaimTx(ctx, tx, item); err != nil {
		return err
	}
	work := unifiedEmbeddingItem(item, chunk, settings)
	now := s.currentTime()
	if _, err = deactivateCurrentChunkEmbeddingTx(ctx, tx, work, chunk, now); err != nil {
		return err
	}
	if err = insertGeneratedChunkEmbeddingTx(ctx, tx, work, chunk, vector, inputHash, ChunkEmbeddingStatusActive, true, now); err != nil {
		return err
	}
	return tx.Commit()
}

var _ = time.Time{}
