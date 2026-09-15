package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// applyUnifiedNativeExtraction persists extraction provenance and chronology
// before any chunk or search publication. The artifacts created here therefore
// inherit the refined chronology, while lexical publication remains a later
// atomic stage.
func (s *Service) applyUnifiedNativeExtraction(ctx context.Context, item PipelineWorkItem, extraction ExtractionResult) (KnowledgeObjectVersion, []DerivedArtifact, error) {
	refinedObject, extraction, err := s.refineKnowledgeAbsoluteTime(item.Object, extraction)
	if err != nil {
		return KnowledgeObjectVersion{}, nil, err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return KnowledgeObjectVersion{}, nil, err
	}
	defer tx.Rollback()
	if err = lockPipelineClaimTx(ctx, tx, item); err != nil {
		return KnowledgeObjectVersion{}, nil, err
	}

	object, err := scanKnowledgeObject(tx.QueryRowContext(ctx, `SELECT `+knowledgeObjectColumns()+`
		FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1 FOR UPDATE`, item.Object.KnowledgeObjectID))
	if err != nil {
		return KnowledgeObjectVersion{}, nil, err
	}
	version, err := scanKnowledgeObjectVersion(tx.QueryRowContext(ctx, `SELECT `+knowledgeObjectVersionColumns()+`
		FROM knowledge.knowledge_object_versions WHERE knowledge_object_version_id=$1 FOR UPDATE`, item.Run.KnowledgeObjectVersionID))
	if err != nil {
		return KnowledgeObjectVersion{}, nil, err
	}

	if pipelineSourceVersionPlaceholder(version.Metadata) {
		version, err = updatePipelineSourceVersionTx(ctx, tx, version, refinedObject, extractionPipelineVersionMetadataNormalized(extraction, nil))
		if err != nil {
			return KnowledgeObjectVersion{}, nil, err
		}
	} else {
		refinedObject.SourceCreatedAt = version.SourceCreatedAt
		refinedObject.SourceModifiedAt = version.SourceModifiedAt
		refinedObject.RecencyAt = version.RecencyAt
		refinedObject.RecencyBasis = version.RecencyBasis
		refinedObject.AbsoluteTimeMetadata = append(json.RawMessage(nil), version.AbsoluteTimeMetadata...)
		existing := absoluteTimeFromMetadata(version.AbsoluteTimeMetadata, AbsoluteTime{
			SourceCreatedAt: version.SourceCreatedAt, SourceModifiedAt: version.SourceModifiedAt,
			RecencyAt: version.RecencyAt, RecencyBasis: version.RecencyBasis,
		})
		extraction.AbsoluteTime = &existing
		extraction.AbsoluteTimeWarnings = existing.Warnings
	}

	object.SourceCreatedAt = refinedObject.SourceCreatedAt
	object.SourceModifiedAt = refinedObject.SourceModifiedAt
	object.RecencyAt = refinedObject.RecencyAt
	object.RecencyBasis = refinedObject.RecencyBasis
	object.AbsoluteTimeMetadata = append(json.RawMessage(nil), refinedObject.AbsoluteTimeMetadata...)
	object.Metadata = extractionPipelineObjectMetadata(object.Metadata, version, extraction)
	if extractionHasBodyText(extraction) {
		object.ProcessingState = ProcessingStateTextExtracted
	} else {
		object.ProcessingState = ProcessingStateMetadataOnly
	}
	object.PipelineKey = item.Run.PipelineDefinitionKey
	object.PipelineVersion = item.Run.PipelineDefinitionVersion
	object.UpdatedAt = s.currentTime()
	object, err = updateKnowledgeObjectTx(ctx, tx, object)
	if err != nil {
		return KnowledgeObjectVersion{}, nil, err
	}
	if _, err = s.replaceObjectLinksTx(ctx, tx, object, extraction.Links); err != nil {
		return KnowledgeObjectVersion{}, nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE knowledge.derived_artifacts SET
		source_created_at=$2,source_modified_at=$3,recency_at=$4,recency_basis=$5
		WHERE knowledge_pipeline_run_id=$1`, item.Run.KnowledgePipelineRunID,
		nullableTime(version.SourceCreatedAt), nullableTime(version.SourceModifiedAt), version.RecencyAt, version.RecencyBasis); err != nil {
		return KnowledgeObjectVersion{}, nil, err
	}

	inputs := ArtifactInputsFromExtraction(extraction)
	artifacts := make([]DerivedArtifact, 0, len(inputs))
	artifactIDs := make([]string, 0, len(inputs))
	for _, input := range inputs {
		artifact, prepareErr := s.PrepareDerivedArtifact(item.Run, item.Stage, version, input)
		if prepareErr != nil {
			return KnowledgeObjectVersion{}, nil, prepareErr
		}
		artifact, err = createDerivedArtifactTx(ctx, tx, artifact)
		if err != nil {
			return KnowledgeObjectVersion{}, nil, err
		}
		artifact, err = activateDerivedArtifactTx(ctx, tx, artifact.KnowledgeDerivedArtifactID, item.Run.Generation)
		if err != nil {
			return KnowledgeObjectVersion{}, nil, err
		}
		artifacts = append(artifacts, artifact)
		artifactIDs = append(artifactIDs, artifact.KnowledgeDerivedArtifactID)
	}
	version, err = updatePipelineVersionMetadataTx(ctx, tx, version.KnowledgeObjectVersionID, extractionPipelineVersionMetadataNormalized(extraction, artifactIDs))
	if err != nil {
		return KnowledgeObjectVersion{}, nil, err
	}
	if err = tx.Commit(); err != nil {
		return KnowledgeObjectVersion{}, nil, err
	}
	return version, artifacts, nil
}

func pipelineSourceVersionPlaceholder(raw json.RawMessage) bool {
	var metadata struct {
		SchemaVersion string `json:"schema_version"`
	}
	return json.Unmarshal(raw, &metadata) == nil && metadata.SchemaVersion == "knowledge.pipeline_source_version.v1"
}

func updatePipelineSourceVersionTx(ctx context.Context, tx *sql.Tx, version KnowledgeObjectVersion, object KnowledgeObject, metadata json.RawMessage) (KnowledgeObjectVersion, error) {
	return scanKnowledgeObjectVersion(tx.QueryRowContext(ctx, `UPDATE knowledge.knowledge_object_versions SET
		source_created_at=$2,source_modified_at=$3,recency_at=$4,recency_basis=$5,
		absolute_time_metadata=$6,metadata=$7
		WHERE knowledge_object_version_id=$1 AND metadata->>'schema_version'='knowledge.pipeline_source_version.v1'
		RETURNING `+knowledgeObjectVersionColumns(), version.KnowledgeObjectVersionID,
		nullableTime(object.SourceCreatedAt), nullableTime(object.SourceModifiedAt), object.RecencyAt,
		object.RecencyBasis, object.AbsoluteTimeMetadata, metadata))
}

func updatePipelineVersionMetadataTx(ctx context.Context, tx *sql.Tx, versionID string, metadata json.RawMessage) (KnowledgeObjectVersion, error) {
	version, err := scanKnowledgeObjectVersion(tx.QueryRowContext(ctx, `UPDATE knowledge.knowledge_object_versions
		SET metadata=$2 WHERE knowledge_object_version_id=$1 RETURNING `+knowledgeObjectVersionColumns(), versionID, metadata))
	if err == sql.ErrNoRows {
		return KnowledgeObjectVersion{}, fmt.Errorf("%w: knowledge object version is stale", ErrConflict)
	}
	return version, err
}
