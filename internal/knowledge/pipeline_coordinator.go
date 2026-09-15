package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type PipelineCoordinatorRunInput struct {
	WorkerRunID           string
	Limit                 int
	LeaseDuration         time.Duration
	MaxTextBytesPerObject int64
	MaxExtractedTextBytes int64
	MaxChunksPerObject    int
	Now                   time.Time
}

type PipelineCoordinatorRunResult struct {
	ReleasedExpired int64
	Claimed         int64
	Completed       int64
	Failed          int64
	MoreWork        bool
	LastRunID       string
	LastObjectID    string
}

// RunPipelineCoordinatorOnce advances at most one lightweight stage per claimed
// file. Heavy stages are claimed exclusively by the heavy executor.
func (s *Service) RunPipelineCoordinatorOnce(ctx context.Context, input PipelineCoordinatorRunInput) (PipelineCoordinatorRunResult, error) {
	if s == nil || s.store.db == nil {
		return PipelineCoordinatorRunResult{}, fmt.Errorf("knowledge store is not configured")
	}
	released, err := s.ReleaseExpiredPipelineClaims(ctx, input.Now)
	if err != nil {
		return PipelineCoordinatorRunResult{}, err
	}
	items, err := s.ClaimPipelineRuns(ctx, PipelineExecutionCoordinator, input.WorkerRunID, PipelineClaimOptions{Limit: input.Limit, LeaseDuration: input.LeaseDuration, Now: input.Now})
	if err != nil {
		return PipelineCoordinatorRunResult{}, err
	}
	result := PipelineCoordinatorRunResult{ReleasedExpired: released, Claimed: int64(len(items))}
	for _, item := range items {
		result.LastRunID, result.LastObjectID = item.Run.KnowledgePipelineRunID, item.Object.KnowledgeObjectID
		warnings, stageErr := s.executeCoordinatorStage(ctx, item, input)
		if stageErr != nil {
			if failErr := s.FailPipelineStage(ctx, item, stageErr, RetryableKnowledgeError(stageErr)); failErr != nil {
				return PipelineCoordinatorRunResult{}, fmt.Errorf("record pipeline stage failure: %w", failErr)
			}
			result.Failed++
			continue
		}
		if _, err := s.CompletePipelineStage(ctx, item, warnings); err != nil {
			return PipelineCoordinatorRunResult{}, err
		}
		result.Completed++
	}
	result.MoreWork = input.Limit > 0 && len(items) == input.Limit
	return result, nil
}

func (s *Service) executeCoordinatorStage(ctx context.Context, item PipelineWorkItem, input PipelineCoordinatorRunInput) ([]string, error) {
	if item.Stage.ExecutionClass != PipelineExecutionCoordinator {
		return nil, fmt.Errorf("%w: stage %q is not coordinator work", ErrInvalid, item.Stage.StageKey)
	}
	version, err := s.store.getKnowledgeObjectVersion(ctx, item.Run.KnowledgeObjectVersionID)
	if err != nil {
		return nil, err
	}
	switch item.Stage.StageKey {
	case FilePipelineStageMetadata:
		_, err = s.createAndActivatePipelineArtifact(ctx, item, version, DerivedArtifactInput{
			ArtifactKind: ArtifactKindMetadataText, SourceLocator: "document", Text: knowledgeMetadataSearchBody(item.Object),
			GeneratorKey: "loom.metadata", GeneratorVersion: StageContractVersionV1,
			Metadata: map[string]any{"schema_version": "knowledge.metadata_artifact.v1"},
		})
		return nil, err
	case FilePipelineStageNativeText:
		extraction, err := s.ExtractObject(ctx, TextPipelineInput{Object: item.Object, MaxBytes: input.MaxTextBytesPerObject, MaxExtractedTextBytes: input.MaxExtractedTextBytes, MaxChunks: input.MaxChunksPerObject})
		if err != nil {
			return nil, err
		}
		_, artifacts, err := s.applyUnifiedNativeExtraction(ctx, item, extraction)
		if err != nil {
			return nil, err
		}
		if len(artifacts) == 0 {
			return []string{"native extraction produced no body text"}, nil
		}
		return nil, nil
	case FilePipelineStagePDFPageAnalysis:
		return s.PreparePDFPageAnalysis(ctx, item, input.MaxTextBytesPerObject)
	case FilePipelineStageConsolidateText:
		artifacts, err := s.store.ListDerivedArtifacts(ctx, item.Run.KnowledgePipelineRunID, true)
		if err != nil {
			return nil, err
		}
		consolidated := ConsolidateCurrentArtifacts(artifacts, defaultConsolidatedTextMaxBytes)
		if consolidated.Text == "" {
			return []string{"no body artifacts were available for consolidation"}, nil
		}
		_, err = s.createAndActivatePipelineArtifact(ctx, item, version, DerivedArtifactInput{
			ArtifactKind: ArtifactKindConsolidatedText, SourceLocator: "document", Text: consolidated.Text,
			GeneratorKey: "loom.consolidation", GeneratorVersion: StageContractVersionV1,
			Metadata: map[string]any{"schema_version": "knowledge.consolidated_text.v1", "artifact_ids": consolidated.ArtifactIDs, "source_locators": consolidated.SourceLocators, "source_kinds": consolidated.SourceKinds, "segments": consolidated.Segments, "omitted": consolidated.Omitted},
		})
		return consolidated.Warnings, err
	case FilePipelineStageChunk:
		artifact, found, err := currentConsolidatedArtifact(ctx, s.store, item.Run.KnowledgePipelineRunID)
		if err != nil {
			return nil, err
		}
		if !found {
			return []string{"no consolidated body text available for chunking"}, nil
		}
		inputs, err := chunkInputsFromConsolidatedArtifact(artifact)
		if err != nil {
			return nil, err
		}
		tx, err := s.store.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		if err = lockPipelineClaimTx(ctx, tx, item); err != nil {
			return nil, err
		}
		result, err := tx.ExecContext(ctx, `UPDATE knowledge.pipeline_stage_runs SET progress_completed=$2::integer,progress_total=$2::integer,metadata=metadata||jsonb_build_object('consolidated_artifact_id',$3::text,'chunk_count',$2::integer),updated_at=now() WHERE knowledge_pipeline_stage_run_id=$1 AND claim_generation=$4 AND claimed_by_worker_run_id=$5 AND status='processing'`, item.Stage.KnowledgePipelineStageRunID, len(inputs), artifact.KnowledgeDerivedArtifactID, item.Run.ClaimGeneration, item.Run.ClaimedByWorkerRunID)
		if err != nil {
			return nil, err
		}
		if err = requireOneRow(result, "chunk stage claim fence is stale"); err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	case FilePipelineStageLexicalIndex:
		artifact, found, artifactErr := currentConsolidatedArtifact(ctx, s.store, item.Run.KnowledgePipelineRunID)
		if artifactErr != nil {
			return nil, artifactErr
		}
		if found {
			return nil, s.publishConsolidatedLexical(ctx, item, version, artifact)
		}
		if pipelineFileFamily(item.Object) == "metadata_only" || pipelineFileFamily(item.Object) == "image" {
			tx, err := s.store.db.BeginTx(ctx, nil)
			if err != nil {
				return nil, err
			}
			defer tx.Rollback()
			if err = lockPipelineClaimTx(ctx, tx, item); err != nil {
				return nil, err
			}
			if err = replaceKnowledgeMetadataSearchDocumentForObjectTx(ctx, tx, item.Object); err != nil {
				return nil, err
			}
			return nil, tx.Commit()
		}
		return nil, nil
	case FilePipelineStageFinalize:
		tx, err := s.store.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		if err = lockPipelineClaimTx(ctx, tx, item); err != nil {
			return nil, err
		}
		result, err := tx.ExecContext(ctx, `UPDATE knowledge.knowledge_objects SET processing_state=CASE WHEN processing_state='embedded' THEN 'embedded' ELSE 'indexed' END, pipeline_key=$2, pipeline_version=$3, last_processed_at=now(), last_error_code='', last_error_message='', updated_at=now() WHERE knowledge_object_id=$1`, item.Object.KnowledgeObjectID, item.Run.PipelineDefinitionKey, item.Run.PipelineDefinitionVersion)
		if err != nil {
			return nil, err
		}
		if err = requireOneRow(result, "pipeline object no longer exists"); err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	default:
		return nil, fmt.Errorf("%w: coordinator stage %q is not implemented", ErrInvalid, item.Stage.StageKey)
	}
}

func currentConsolidatedArtifact(ctx context.Context, store Store, runID string) (DerivedArtifact, bool, error) {
	artifacts, err := store.ListDerivedArtifacts(ctx, runID, true)
	if err != nil {
		return DerivedArtifact{}, false, err
	}
	for index := len(artifacts) - 1; index >= 0; index-- {
		if artifacts[index].ArtifactKind == ArtifactKindConsolidatedText {
			return artifacts[index], true, nil
		}
	}
	return DerivedArtifact{}, false, nil
}

func chunkInputsFromConsolidatedArtifact(artifact DerivedArtifact) ([]TextChunkInput, error) {
	if artifact.TextContent == nil {
		return nil, nil
	}
	metadata := jsonObject(artifact.Metadata)
	provenance := ChunkProvenance{TextSource: ArtifactKindConsolidatedText, ExtractorKey: artifact.GeneratorKey, ExtractorVersion: artifact.GeneratorVersion, ExtractionStatus: ExtractionStatusExtracted}
	if raw, present := metadata["segments"]; present {
		payload, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		var segments []ConsolidatedSegment
		if err := json.Unmarshal(payload, &segments); err != nil {
			return nil, fmt.Errorf("%w: invalid consolidated segments", ErrInvalid)
		}
		var inputs []TextChunkInput
		end := 0
		for _, segment := range segments {
			gap := segment.StartByte - end
			if segment.StartByte < end || segment.EndByte <= segment.StartByte || segment.EndByte > len(*artifact.TextContent) || segment.ArtifactID == "" || segment.SourceLocator == "" || segment.SourceKind == "" || (end == 0 && gap != 0) || (end > 0 && (gap != 2 || (*artifact.TextContent)[end:segment.StartByte] != "\n\n")) {
				return nil, fmt.Errorf("%w: invalid consolidated segment boundary", ErrInvalid)
			}
			text := (*artifact.TextContent)[segment.StartByte:segment.EndByte]
			if !utf8.ValidString(text) {
				return nil, fmt.Errorf("%w: invalid consolidated UTF-8 boundary", ErrInvalid)
			}
			chunks := ChunkTextDocumentWithProvenance(TextDocument{Text: text}, ChunkerOptions{}, provenance)
			for _, chunk := range chunks {
				chunk.Index = len(inputs) + 1
				chunk.StartOffset += segment.StartByte
				chunk.EndOffset += segment.StartByte
				if chunk.StructuralPath == "" {
					chunk.StructuralPath = segment.SourceLocator
				}
				chunk.ArtifactIDs = []string{segment.ArtifactID}
				chunk.SourceLocators = []string{segment.SourceLocator}
				chunk.SourceKinds = []string{segment.SourceKind}
				inputs = append(inputs, chunk)
			}
			end = segment.EndByte
		}
		if end != len(*artifact.TextContent) {
			return nil, fmt.Errorf("%w: incomplete consolidated segments", ErrInvalid)
		}
		return inputs, nil
	}
	// Historical artifacts retain their original whole-document chunk contract.
	inputs := ChunkTextDocumentWithProvenance(TextDocument{Text: *artifact.TextContent}, ChunkerOptions{}, provenance)
	artifactIDs := stringSliceValue(metadata["artifact_ids"])
	locators := stringSliceValue(metadata["source_locators"])
	kinds := stringSliceValue(metadata["source_kinds"])
	for index := range inputs {
		inputs[index].ArtifactIDs = artifactIDs
		inputs[index].SourceLocators = locators
		inputs[index].SourceKinds = kinds
	}
	return inputs, nil
}

func stringSliceValue(value any) []string {
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(raw))
	for _, entry := range raw {
		if text, ok := entry.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func (s *Service) publishConsolidatedLexical(ctx context.Context, item PipelineWorkItem, version KnowledgeObjectVersion, artifact DerivedArtifact) error {
	inputs, err := chunkInputsFromConsolidatedArtifact(artifact)
	if err != nil {
		return err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockPipelineClaimTx(ctx, tx, item); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE knowledge.chunk_embeddings SET active=false,status='reusable',deactivated_at=COALESCE(deactivated_at,now()) WHERE active=true AND knowledge_chunk_id IN (SELECT knowledge_chunk_id FROM knowledge.knowledge_chunks WHERE knowledge_object_version_id=$1)`, version.KnowledgeObjectVersionID); err != nil {
		return err
	}
	chunks, err := s.replaceKnowledgeChunksTx(ctx, tx, item.Object, version, inputs)
	if err != nil {
		return err
	}
	if _, _, err = s.replaceKnowledgeSearchDocumentsTx(ctx, tx, item.Object, version, chunks); err != nil {
		return err
	}
	object := item.Object
	now := s.currentTime()
	object.ProcessingState = ProcessingStateIndexed
	object.PipelineKey = item.Run.PipelineDefinitionKey
	object.PipelineVersion = item.Run.PipelineDefinitionVersion
	object.LastProcessedAt = &now
	object.LastErrorCode = ""
	object.LastErrorMessage = ""
	object.UpdatedAt = now
	if _, err = updateKnowledgeObjectTx(ctx, tx, object); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) createAndActivatePipelineArtifact(ctx context.Context, item PipelineWorkItem, version KnowledgeObjectVersion, input DerivedArtifactInput) (DerivedArtifact, error) {
	artifact, err := s.PrepareDerivedArtifact(item.Run, item.Stage, version, input)
	if err != nil {
		return DerivedArtifact{}, err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return DerivedArtifact{}, err
	}
	defer tx.Rollback()
	if err = lockPipelineClaimTx(ctx, tx, item); err != nil {
		return DerivedArtifact{}, err
	}
	artifact, err = createDerivedArtifactTx(ctx, tx, artifact)
	if err != nil {
		return DerivedArtifact{}, err
	}
	artifact, err = activateDerivedArtifactTx(ctx, tx, artifact.KnowledgeDerivedArtifactID, item.Run.Generation)
	if err != nil {
		return DerivedArtifact{}, err
	}
	if err = tx.Commit(); err != nil {
		return DerivedArtifact{}, err
	}
	return artifact, nil
}

func consolidatedArtifactText(artifacts []DerivedArtifact) string {
	sort.SliceStable(artifacts, func(i, j int) bool { return artifacts[i].SourceLocator < artifacts[j].SourceLocator })
	parts := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.TextContent == nil || artifact.ArtifactKind == ArtifactKindMetadataText || artifact.ArtifactKind == ArtifactKindConsolidatedText {
			continue
		}
		if text := strings.TrimSpace(*artifact.TextContent); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func (s Store) getKnowledgeObjectVersion(ctx context.Context, id string) (KnowledgeObjectVersion, error) {
	return scanKnowledgeObjectVersion(s.db.QueryRowContext(ctx, `SELECT `+knowledgeObjectVersionColumns()+` FROM knowledge.knowledge_object_versions WHERE knowledge_object_version_id=$1`, id))
}

func (s *Service) FailPipelineStage(ctx context.Context, item PipelineWorkItem, cause error, retryable bool) error {
	status := PipelineStageStatusBlockedManual
	runStatus := FilePipelineStatusBlockedManual
	var next any
	if retryable && item.Stage.AttemptCount < defaultKnowledgeRetryMaxAttempts {
		status = PipelineStageStatusFailedRetryable
		runStatus = waitingPipelineStatus(item.Stage.ExecutionClass, false)
		next = s.currentTime().Add(knowledgeRetryDelay(defaultKnowledgeRetryBaseDelay, defaultKnowledgeRetryMaximumDelay, item.Stage.AttemptCount+1))
	}
	payload, _ := json.Marshal(map[string]any{"code": knowledgeProcessingErrorCode(cause), "message": cause.Error(), "retryable": retryable})
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockPipelineClaimTx(ctx, tx, item); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE knowledge.pipeline_stage_runs SET status=$2, next_attempt_at=$3, claimed_by_worker_run_id='', error_json=$4, updated_at=now() WHERE knowledge_pipeline_stage_run_id=$1 AND claim_generation=$5 AND claimed_by_worker_run_id=$6 AND status='processing'`, item.Stage.KnowledgePipelineStageRunID, status, next, payload, item.Run.ClaimGeneration, item.Run.ClaimedByWorkerRunID)
	if err != nil {
		return err
	}
	if err = requireOneRow(result, "pipeline stage failure fence is stale"); err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, `UPDATE knowledge.pipeline_runs SET status=$2, claimed_by_worker_run_id='', claim_expires_at=NULL, last_error_code=$3, last_error_message=$4, updated_at=now() WHERE knowledge_pipeline_run_id=$1 AND generation=$5 AND claim_generation=$6 AND claimed_by_worker_run_id=$7 AND status='processing'`, item.Run.KnowledgePipelineRunID, runStatus, knowledgeProcessingErrorCode(cause), cause.Error(), item.Run.Generation, item.Run.ClaimGeneration, item.Run.ClaimedByWorkerRunID)
	if err != nil {
		return err
	}
	if err = requireOneRow(result, "pipeline run failure fence is stale"); err != nil {
		return err
	}
	return tx.Commit()
}

var _ = sql.ErrNoRows
