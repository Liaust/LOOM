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

	"loom.local/loom/internal/storagecatalog"
)

// Raster-heavy PDFs need a binary input budget, not the text-file read limit.
// Extracted text, page count and chunk limits remain independently bounded.
func pipelineNativeSourceLimit(object KnowledgeObject, textLimit int64) int64 {
	if object.FileClass == storagecatalog.FileClassPDF {
		return DefaultPDFMaxSourceBytes
	}
	return textLimit
}

type PipelineCoordinatorRunInput struct {
	chunks                *pipelineChunkCache
	PreferredRunID        string
	WorkerRunID           string
	Limit                 int
	LeaseDuration         time.Duration
	MaxTextBytesPerObject int64
	MaxExtractedTextBytes int64
	MaxChunksPerObject    int
	Now                   time.Time
}

type PipelineCoordinatorRunResult struct {
	ElapsedMS       map[string]int64
	ReleasedExpired int64
	Claimed         int64
	Completed       int64
	Failed          int64
	MoreWork        bool
	LastRunID       string
	LastObjectID    string
}

type pipelineChunkCache struct {
	runID, artifactID string
	inputs            []TextChunkInput
}

// RunPipelineCoordinatorOnce drains a bounded number of lightweight stages.
// Claim just-in-time so a slow stage does not strand a preclaimed batch.
func (s *Service) RunPipelineCoordinatorOnce(ctx context.Context, input PipelineCoordinatorRunInput) (PipelineCoordinatorRunResult, error) {
	if s == nil || s.store.db == nil {
		return PipelineCoordinatorRunResult{}, fmt.Errorf("knowledge store is not configured")
	}
	released, err := s.ReleaseExpiredPipelineClaims(ctx, input.Now)
	if err != nil {
		return PipelineCoordinatorRunResult{}, err
	}
	limit := input.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	result := PipelineCoordinatorRunResult{ReleasedExpired: released, ElapsedMS: map[string]int64{}}
	input.chunks = &pipelineChunkCache{}
	preferredRun := input.PreferredRunID
	for result.Claimed < int64(limit) {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < 2*time.Second {
			result.MoreWork = true
			return result, nil
		}
		started := time.Now()
		items, err := s.ClaimPipelineRuns(ctx, PipelineExecutionCoordinator, input.WorkerRunID, PipelineClaimOptions{PreferredRunID: preferredRun, Limit: 1, LeaseDuration: input.LeaseDuration, Now: s.currentTime()})
		claimMS := time.Since(started).Milliseconds()
		result.ElapsedMS["claim"] += claimMS
		if err != nil {
			return result, err
		}
		if len(items) == 0 {
			result.ElapsedMS["claim.empty"] += claimMS
			return result, nil
		}
		item := items[0]
		result.ElapsedMS["claim."+item.Stage.StageKey] += claimMS
		preferredRun = item.Run.KnowledgePipelineRunID
		result.Claimed++
		result.LastRunID, result.LastObjectID = item.Run.KnowledgePipelineRunID, item.Object.KnowledgeObjectID
		started = time.Now()
		warnings, stageErr := s.executeCoordinatorStage(ctx, item, input)
		result.ElapsedMS[item.Stage.StageKey] += time.Since(started).Milliseconds()
		if stageErr != nil {
			if failErr := s.FailPipelineStage(ctx, item, stageErr, RetryableKnowledgeError(stageErr)); failErr != nil {
				return PipelineCoordinatorRunResult{}, fmt.Errorf("record pipeline stage failure: %w", failErr)
			}
			result.Failed++
			preferredRun = ""
			continue
		}
		started = time.Now()
		if _, err := s.CompletePipelineStage(ctx, item, warnings); err != nil {
			return PipelineCoordinatorRunResult{}, err
		}
		result.Completed++
		result.ElapsedMS["completion"] += time.Since(started).Milliseconds()
	}
	result.MoreWork = true
	return result, nil
}

func (s *Service) executeCoordinatorStage(ctx context.Context, item PipelineWorkItem, input PipelineCoordinatorRunInput) ([]string, error) {
	if item.Stage.ExecutionClass != PipelineExecutionCoordinator {
		return nil, fmt.Errorf("%w: stage %q is not coordinator work", ErrInvalid, item.Stage.StageKey)
	}
	item, err := s.capturePipelineSource(ctx, item)
	if err != nil {
		return nil, err
	}
	var version KnowledgeObjectVersion
	switch item.Stage.StageKey {
	case FilePipelineStageMetadata, FilePipelineStageConsolidateText, FilePipelineStageLexicalIndex:
		version, err = s.store.getKnowledgeObjectVersion(ctx, item.Run.KnowledgeObjectVersionID)
		if err != nil {
			return nil, err
		}
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
		extraction, err := s.extractPipelineNative(ctx, item, input)
		if err != nil {
			return nil, err
		}
		_, artifacts, err := s.applyUnifiedNativeExtraction(ctx, item, extraction)
		if err != nil {
			return nil, err
		}
		if effectiveCheapPipelinePlan(item.Run.PlanSnapshot) && (extraction.Status == ExtractionStatusNoEmbeddedText || referenceOnlyPDFExtraction(item.Run.PlanSnapshot, extraction.Status)) {
			var plan CompiledPipelinePlan
			_ = json.Unmarshal(item.Run.PlanSnapshot, &plan)
			if !compiledPlanContainsSelectedStage(plan, FilePipelineStagePDFOCR) {
				return nil, s.skipEmptyPipelineBody(ctx, item)
			}
		}
		if len(artifacts) == 0 {
			return []string{"native extraction produced no body text"}, nil
		}
		return nil, nil
	case FilePipelineStagePDFPageAnalysis:
		return s.PreparePDFPageAnalysis(ctx, item, DefaultPDFMaxSourceBytes)
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
		if input.chunks != nil {
			*input.chunks = pipelineChunkCache{runID: item.Run.KnowledgePipelineRunID, artifactID: artifact.KnowledgeDerivedArtifactID, inputs: inputs}
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
			if input.chunks != nil && input.chunks.runID == item.Run.KnowledgePipelineRunID && input.chunks.artifactID == artifact.KnowledgeDerivedArtifactID {
				return nil, s.publishConsolidatedLexicalInputs(ctx, item, version, artifact, input.chunks.inputs)
			}
			return nil, s.publishConsolidatedLexical(ctx, item, version, artifact)
		}
		// An empty replacement is a publication too; it must retire the old body.
		return nil, s.publishConsolidatedLexical(ctx, item, version, DerivedArtifact{})
	case FilePipelineStageFinalize:
		tx, err := s.store.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		if err = lockPipelineClaimTx(ctx, tx, item); err != nil {
			return nil, err
		}
		result, err := tx.ExecContext(ctx, `UPDATE knowledge.knowledge_objects SET processing_state=CASE WHEN source_hash<>$4 OR source_revision<>$5 THEN 'stale' WHEN processing_state='embedded' THEN 'embedded' ELSE 'indexed' END, pipeline_key=$2, pipeline_version=$3, last_processed_at=now(), last_error_code='', last_error_message='', updated_at=now() WHERE knowledge_object_id=$1`, item.Object.KnowledgeObjectID, item.Run.PipelineDefinitionKey, item.Run.PipelineDefinitionVersion, item.Run.SourceHash, item.Run.SourceRevision)
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
	return s.publishConsolidatedLexicalInputs(ctx, item, version, artifact, inputs)
}

func (s *Service) publishConsolidatedLexicalInputs(ctx context.Context, item PipelineWorkItem, version KnowledgeObjectVersion, artifact DerivedArtifact, inputs []TextChunkInput) error {
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
	if len(chunks) == 0 {
		current, readErr := scanKnowledgeObject(tx.QueryRowContext(ctx, `SELECT `+knowledgeObjectColumns()+` FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1`, item.Object.KnowledgeObjectID))
		if readErr != nil {
			return readErr
		}
		// Metadata describes the current source, never a superseded capture.
		if err = replaceKnowledgeMetadataSearchDocumentForObjectTx(ctx, tx, current); err != nil {
			return err
		}
		// No body means no current semantic publication, including when an
		// otherwise enabled embedding stage intentionally skips empty output.
		// Retain all historical rows for citations and compatible cache reuse.
		if _, err = tx.ExecContext(ctx, `UPDATE knowledge.knowledge_objects SET semantic_version_id=NULL,semantic_published_at=NULL,semantic_runtime_key=NULL,semantic_model_key=NULL,semantic_dimensions=NULL WHERE knowledge_object_id=$1`, item.Object.KnowledgeObjectID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE knowledge.chunk_embeddings SET active=false,status='historical',deactivated_at=COALESCE(deactivated_at,now()) WHERE knowledge_object_id=$1 AND active`, item.Object.KnowledgeObjectID); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE knowledge.knowledge_objects SET lexical_version_id=$2,lexical_published_at=$3,
		processing_state=CASE WHEN source_hash=$4 AND source_revision=$5 THEN 'indexed' ELSE 'stale' END,
		last_processed_at=$3,updated_at=$3 WHERE knowledge_object_id=$1`, item.Object.KnowledgeObjectID, version.KnowledgeObjectVersionID, s.currentTime(), item.Run.SourceHash, item.Run.SourceRevision); err != nil {
		return err
	}
	if err = MarkProjectionDirtyTx(ctx, tx, ProjectionDirtyInput{ObjectIDs: []string{item.Object.KnowledgeObjectID}}); err != nil {
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
	if err := notifyPipelineWorkTx(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

var _ = sql.ErrNoRows

// V1 trajectories still use their original extractor. V2 native extraction
// does not inventory PDF images: no selected stage consumes that inventory.
func (s *Service) extractPipelineNative(ctx context.Context, item PipelineWorkItem, input PipelineCoordinatorRunInput) (ExtractionResult, error) {
	if pipelineFileFamily(item.Object) != "pdf" || !effectiveCheapPipelinePlan(item.Run.PlanSnapshot) {
		return s.ExtractObject(ctx, TextPipelineInput{Object: item.Object, MaxBytes: pipelineNativeSourceLimit(item.Object, input.MaxTextBytesPerObject), MaxExtractedTextBytes: input.MaxExtractedTextBytes, MaxChunks: input.MaxChunksPerObject})
	}
	object, cleanup, err := s.preparePathBackedExtractionObject(ctx, item.Object, DefaultPDFMaxSourceBytes)
	if err != nil {
		return ExtractionResult{}, err
	}
	defer cleanup()
	extractor := newPDFExtractor(nil)
	extractor.nativeOnly = true
	result, err := extractor.Extract(ExtractionInput{Object: object, MaxSourceBytes: DefaultPDFMaxSourceBytes, MaxExtractedTextBytes: input.MaxExtractedTextBytes})
	if err != nil {
		return result, err
	}
	switch result.Status {
	case ExtractionStatusPasswordRequired, ExtractionStatusTooLarge, ExtractionStatusSourceUnavailable, ExtractionStatusFailed:
		if !referenceOnlyPDFExtraction(item.Run.PlanSnapshot, result.Status) {
			return result, fmt.Errorf("%w: PDF native extraction %s", ErrInvalid, result.Status)
		}
	}
	if input.MaxChunksPerObject > 0 && len(result.Chunks) > input.MaxChunksPerObject {
		return result, fmt.Errorf("%w: text chunk count %d exceeds max_chunks %d", ErrInvalid, len(result.Chunks), input.MaxChunksPerObject)
	}
	return result, nil
}

// An automatic reference-only PDF retains metadata when its body exceeds the
// extraction cap. Explicit enrichment and selected heavy work still fail closed.
func referenceOnlyPDFExtraction(snapshot json.RawMessage, status string) bool {
	if status != ExtractionStatusTooLarge {
		return false
	}
	var plan CompiledPipelinePlan
	if json.Unmarshal(snapshot, &plan) != nil || plan.FileFamily != "pdf" || plan.Enrichment != nil || !effectiveCheapPipelinePlan(snapshot) {
		return false
	}
	for _, stage := range plan.Stages {
		if stage.Selected && stage.ExecutionClass == PipelineExecutionHeavy {
			return false
		}
	}
	return true
}

func (s *Service) skipEmptyPipelineBody(ctx context.Context, item PipelineWorkItem) error {
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockPipelineClaimTx(ctx, tx, item); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE knowledge.pipeline_stage_runs SET status='skipped_not_applicable',completed_at=now(),updated_at=now(),metadata=metadata||'{"skip_reason":"no_native_body_or_selected_enrichment"}'::jsonb
	 WHERE knowledge_pipeline_run_id=$1 AND stage_key IN ('consolidate_text','chunk','embedding') AND status='waiting_dependency'`, item.Run.KnowledgePipelineRunID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
