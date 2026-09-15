package knowledge

import (
	"context"
	"database/sql"
	"fmt"
)

func (s Store) CreateDerivedArtifact(ctx context.Context, artifact DerivedArtifact) (DerivedArtifact, error) {
	if s.db == nil {
		return DerivedArtifact{}, fmt.Errorf("knowledge store is not configured")
	}
	if err := ValidateDerivedArtifact(artifact); err != nil {
		return DerivedArtifact{}, err
	}
	return createDerivedArtifactTx(ctx, s.db, artifact)
}

type derivedArtifactQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func createDerivedArtifactTx(ctx context.Context, query derivedArtifactQuerier, artifact DerivedArtifact) (DerivedArtifact, error) {
	return scanDerivedArtifact(query.QueryRowContext(ctx, `
		INSERT INTO knowledge.derived_artifacts (
			knowledge_derived_artifact_id, knowledge_object_id, knowledge_object_version_id,
			knowledge_pipeline_run_id, knowledge_pipeline_stage_run_id, generation,
			artifact_kind, source_locator, text_content, payload_ref, content_hash,
			input_hash, generator_key, generator_version, engine_key, engine_version,
			prompt_version, language, confidence, state, active, source_created_at,
			source_modified_at, recency_at, recency_basis, metadata, created_at,
			activated_at, deactivated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29)
		RETURNING `+derivedArtifactColumns(),
		artifact.KnowledgeDerivedArtifactID, artifact.KnowledgeObjectID, artifact.KnowledgeObjectVersionID,
		artifact.KnowledgePipelineRunID, artifact.KnowledgePipelineStageRunID, artifact.Generation,
		artifact.ArtifactKind, artifact.SourceLocator, nullableString(artifact.TextContent), artifact.PayloadRef,
		artifact.ContentHash, artifact.InputHash, artifact.GeneratorKey, artifact.GeneratorVersion,
		artifact.EngineKey, artifact.EngineVersion, artifact.PromptVersion, artifact.Language,
		artifact.Confidence, artifact.State, artifact.Active, nullableTime(artifact.SourceCreatedAt),
		nullableTime(artifact.SourceModifiedAt), artifact.RecencyAt, artifact.RecencyBasis,
		artifact.Metadata, artifact.CreatedAt, nullableTime(artifact.ActivatedAt), nullableTime(artifact.DeactivatedAt)))
}

func activateDerivedArtifactTx(ctx context.Context, tx *sql.Tx, artifactID string, generation int64) (DerivedArtifact, error) {
	artifact, err := scanDerivedArtifact(tx.QueryRowContext(ctx, `SELECT `+derivedArtifactColumns()+`
		FROM knowledge.derived_artifacts WHERE knowledge_derived_artifact_id=$1 FOR UPDATE`, artifactID))
	if err != nil {
		return DerivedArtifact{}, err
	}
	var current bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM knowledge.pipeline_runs
		WHERE knowledge_pipeline_run_id=$1 AND knowledge_object_version_id=$2
		  AND generation=$3 AND status NOT IN ('stale','cancelled')
	)`, artifact.KnowledgePipelineRunID, artifact.KnowledgeObjectVersionID, generation).Scan(&current)
	if err != nil {
		return DerivedArtifact{}, err
	}
	if !current || artifact.Generation != generation {
		return DerivedArtifact{}, fmt.Errorf("%w: artifact generation is stale", ErrConflict)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE knowledge.derived_artifacts
		SET active=false, state='historical', deactivated_at=now()
		WHERE knowledge_object_version_id=$1 AND artifact_kind=$2 AND source_locator=$3
		  AND generator_key=$4 AND generator_version=$5 AND active=true
		  AND knowledge_derived_artifact_id<>$6`, artifact.KnowledgeObjectVersionID, artifact.ArtifactKind,
		artifact.SourceLocator, artifact.GeneratorKey, artifact.GeneratorVersion, artifact.KnowledgeDerivedArtifactID); err != nil {
		return DerivedArtifact{}, err
	}
	return scanDerivedArtifact(tx.QueryRowContext(ctx, `UPDATE knowledge.derived_artifacts
		SET active=true, state='active', activated_at=COALESCE(activated_at,now()), deactivated_at=NULL
		WHERE knowledge_derived_artifact_id=$1 AND generation=$2 RETURNING `+derivedArtifactColumns(), artifactID, generation))
}

func (s Store) ListDerivedArtifacts(ctx context.Context, pipelineRunID string, activeOnly bool) ([]DerivedArtifact, error) {
	if s.db == nil {
		return nil, fmt.Errorf("knowledge store is not configured")
	}
	query := `SELECT ` + derivedArtifactColumns() + ` FROM knowledge.derived_artifacts WHERE knowledge_pipeline_run_id = $1`
	if activeOnly {
		query += ` AND active = true`
	}
	query += ` ORDER BY created_at, knowledge_derived_artifact_id`
	rows, err := s.db.QueryContext(ctx, query, pipelineRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []DerivedArtifact
	for rows.Next() {
		artifact, err := scanDerivedArtifact(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, artifact)
	}
	return result, rows.Err()
}

func (s Store) FindReusableDerivedArtifact(ctx context.Context, artifact DerivedArtifact) (DerivedArtifact, bool, error) {
	if s.db == nil {
		return DerivedArtifact{}, false, fmt.Errorf("knowledge store is not configured")
	}
	found, err := scanDerivedArtifact(s.db.QueryRowContext(ctx, `SELECT `+derivedArtifactColumns()+`
		FROM knowledge.derived_artifacts
		WHERE artifact_kind=$1 AND source_locator=$2 AND input_hash=$3
		  AND generator_key=$4 AND generator_version=$5
		  AND engine_key=$6 AND engine_version=$7 AND prompt_version=$8
		  AND state IN ('active','reusable','historical')
		ORDER BY active DESC, created_at DESC LIMIT 1`,
		artifact.ArtifactKind, artifact.SourceLocator, artifact.InputHash, artifact.GeneratorKey,
		artifact.GeneratorVersion, artifact.EngineKey, artifact.EngineVersion, artifact.PromptVersion))
	if err == sql.ErrNoRows {
		return DerivedArtifact{}, false, nil
	}
	return found, err == nil, err
}

func (s Store) ActivateDerivedArtifact(ctx context.Context, artifactID string, generation int64) (artifact DerivedArtifact, err error) {
	if s.db == nil {
		return DerivedArtifact{}, fmt.Errorf("knowledge store is not configured")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DerivedArtifact{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	artifact, err = activateDerivedArtifactTx(ctx, tx, artifactID, generation)
	if err != nil {
		return DerivedArtifact{}, err
	}
	err = tx.Commit()
	return artifact, err
}

func (s Store) StaleDerivedArtifacts(ctx context.Context, pipelineRunID string, generation int64) (int64, error) {
	if s.db == nil {
		return 0, fmt.Errorf("knowledge store is not configured")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE knowledge.derived_artifacts
		SET active=false, state='stale', deactivated_at=COALESCE(deactivated_at,now())
		WHERE knowledge_pipeline_run_id=$1 AND generation=$2 AND state<>'stale'`, pipelineRunID, generation)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func derivedArtifactColumns() string {
	return `knowledge_derived_artifact_id, knowledge_object_id, knowledge_object_version_id,
	        knowledge_pipeline_run_id, knowledge_pipeline_stage_run_id, generation,
	        artifact_kind, source_locator, text_content, payload_ref, content_hash,
	        input_hash, generator_key, generator_version, engine_key, engine_version,
	        prompt_version, language, confidence, state, active, source_created_at,
	        source_modified_at, recency_at, recency_basis, metadata, created_at,
	        activated_at, deactivated_at`
}

func scanDerivedArtifact(scanner interface{ Scan(dest ...any) error }) (DerivedArtifact, error) {
	var artifact DerivedArtifact
	var text sql.NullString
	var confidence sql.NullFloat64
	var sourceCreated, sourceModified, activated, deactivated sql.NullTime
	var metadata []byte
	err := scanner.Scan(&artifact.KnowledgeDerivedArtifactID, &artifact.KnowledgeObjectID,
		&artifact.KnowledgeObjectVersionID, &artifact.KnowledgePipelineRunID,
		&artifact.KnowledgePipelineStageRunID, &artifact.Generation, &artifact.ArtifactKind,
		&artifact.SourceLocator, &text, &artifact.PayloadRef, &artifact.ContentHash, &artifact.InputHash,
		&artifact.GeneratorKey, &artifact.GeneratorVersion, &artifact.EngineKey, &artifact.EngineVersion,
		&artifact.PromptVersion, &artifact.Language, &confidence, &artifact.State, &artifact.Active,
		&sourceCreated, &sourceModified, &artifact.RecencyAt, &artifact.RecencyBasis, &metadata,
		&artifact.CreatedAt, &activated, &deactivated)
	if err != nil {
		return DerivedArtifact{}, err
	}
	artifact.TextContent = nullStringPtr(text)
	if confidence.Valid {
		value := confidence.Float64
		artifact.Confidence = &value
	}
	artifact.SourceCreatedAt = nullTimePtr(sourceCreated)
	artifact.SourceModifiedAt = nullTimePtr(sourceModified)
	artifact.Metadata = jsonObjectOrEmpty(metadata)
	artifact.ActivatedAt = nullTimePtr(activated)
	artifact.DeactivatedAt = nullTimePtr(deactivated)
	return artifact, nil
}
