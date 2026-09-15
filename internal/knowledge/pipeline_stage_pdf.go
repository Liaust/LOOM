package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"loom.local/loom/internal/ids"
)

const defaultPDFOCRPagesPerClaim = 4

func (s *Service) PreparePDFPageAnalysis(ctx context.Context, item PipelineWorkItem, maxBytes int64) ([]string, error) {
	if pipelineFileFamily(item.Object) != "pdf" {
		return nil, fmt.Errorf("%w: PDF page analysis requires a PDF object", ErrInvalid)
	}
	object, cleanup, err := s.preparePathBackedExtractionObject(ctx, item.Object, maxBytes)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	pages, err := AnalyzePDFPages(ctx, nil, object.SourcePath, 24)
	if err != nil {
		return nil, err
	}
	version, err := s.store.getKnowledgeObjectVersion(ctx, item.Run.KnowledgeObjectVersionID)
	if err != nil {
		return nil, err
	}
	ocrStage, err := s.store.getCurrentPipelineStage(ctx, item.Run.KnowledgePipelineRunID, FilePipelineStagePDFOCR)
	if err != nil {
		return nil, err
	}
	selected := 0
	for _, page := range pages {
		if page.UsefulEmbeddedText {
			if strings.TrimSpace(page.Text) != "" {
				if _, err := s.createAndActivatePipelineArtifact(ctx, item, version, DerivedArtifactInput{ArtifactKind: ArtifactKindEmbeddedText, SourceLocator: fmt.Sprintf("page:%d", page.Number), Text: page.Text, GeneratorKey: ExtractorKeyPDF, GeneratorVersion: ExtractorVersionPDF, Metadata: map[string]any{"page_number": page.Number, "analysis_version": PDFPageAnalysisVersion}}); err != nil {
					return nil, err
				}
			}
			continue
		}
		selected++
		if err := s.store.upsertPDFOCRUnit(ctx, ocrStage, page, item.Run.SourceHash); err != nil {
			return nil, err
		}
	}
	if selected == 0 {
		_, err = s.store.db.ExecContext(ctx, `UPDATE knowledge.pipeline_stage_runs SET status='skipped_not_applicable',completed_at=now(),updated_at=now(),metadata=metadata||'{"skip_reason":"all_pages_have_useful_embedded_text"}'::jsonb WHERE knowledge_pipeline_stage_run_id=$1`, ocrStage.KnowledgePipelineStageRunID)
		return nil, err
	}
	return nil, nil
}

func (s Store) upsertPDFOCRUnit(ctx context.Context, stage PipelineStageRun, page PDFPage, sourceHash string) error {
	inputHash := hashArtifactValue(fmt.Sprintf("%s\n%d\n%s", sourceHash, page.Number, PDFPageAnalysisVersion))
	_, err := s.db.ExecContext(ctx, `INSERT INTO knowledge.pipeline_stage_units (knowledge_pipeline_stage_unit_id,knowledge_pipeline_stage_run_id,unit_key,page_number,unit_input_hash,status,resource_usage,warning_json,error_json,metadata,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,'ready','{}','[]','{}',$6,now(),now()) ON CONFLICT (knowledge_pipeline_stage_run_id,unit_key) DO UPDATE SET unit_input_hash=EXCLUDED.unit_input_hash,status=CASE WHEN knowledge.pipeline_stage_units.unit_input_hash=EXCLUDED.unit_input_hash AND knowledge.pipeline_stage_units.status='complete' THEN 'complete' ELSE 'ready' END,claimed_by_worker_run_id='',claim_generation=0,updated_at=now()`, ids.NewKnowledgePipelineStageUnitID(), stage.KnowledgePipelineStageRunID, fmt.Sprintf("page:%d", page.Number), page.Number, inputHash, json.RawMessage(`{"schema_version":"knowledge.pdf_ocr_unit.v1"}`))
	return err
}

func (s Store) listReadyPDFOCRUnits(ctx context.Context, item PipelineWorkItem, limit int) ([]PipelineStageUnit, error) {
	if limit <= 0 {
		limit = defaultPDFOCRPagesPerClaim
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = lockPipelineClaimTx(ctx, tx, item); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `UPDATE knowledge.pipeline_stage_units SET status='processing',attempt_count=attempt_count+1,claimed_by_worker_run_id=$3,claim_generation=$4,started_at=COALESCE(started_at,now()),updated_at=now() WHERE knowledge_pipeline_stage_unit_id IN (SELECT knowledge_pipeline_stage_unit_id FROM knowledge.pipeline_stage_units WHERE knowledge_pipeline_stage_run_id=$1 AND status IN ('ready','failed_retryable') ORDER BY page_number LIMIT $2 FOR UPDATE SKIP LOCKED) RETURNING knowledge_pipeline_stage_unit_id,knowledge_pipeline_stage_run_id,unit_key,page_number,unit_input_hash,status,attempt_count,claimed_by_worker_run_id,claim_generation,output_artifact_id,confidence,resource_usage,warning_json,error_json,metadata,created_at,started_at,updated_at,completed_at`, item.Stage.KnowledgePipelineStageRunID, limit, item.Run.ClaimedByWorkerRunID, item.Run.ClaimGeneration)
	if err != nil {
		return nil, err
	}
	var result []PipelineStageUnit
	for rows.Next() {
		unit, err := scanPipelineStageUnit(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, unit)
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s Store) cleanupPDFOCRBatch(ctx context.Context, item PipelineWorkItem, failedUnitID, failedStatus string, failure json.RawMessage) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE knowledge.pipeline_stage_units SET status=$2,
		claimed_by_worker_run_id='',claim_generation=0,error_json=$3,updated_at=now()
		WHERE knowledge_pipeline_stage_unit_id=$1 AND status='processing'
		  AND claimed_by_worker_run_id=$4 AND claim_generation=$5`, failedUnitID, failedStatus, failure,
		item.Run.ClaimedByWorkerRunID, item.Run.ClaimGeneration)
	if err != nil {
		return err
	}
	if err = requireOneRow(result, "PDF OCR failed unit claim is stale"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE knowledge.pipeline_stage_units SET status='ready',
		claimed_by_worker_run_id='',claim_generation=0,updated_at=now()
		WHERE knowledge_pipeline_stage_run_id=$1 AND status='processing'
		  AND claimed_by_worker_run_id=$2 AND claim_generation=$3`, item.Stage.KnowledgePipelineStageRunID,
		item.Run.ClaimedByWorkerRunID, item.Run.ClaimGeneration); err != nil {
		return err
	}
	return tx.Commit()
}

type PDFPageRenderer interface {
	Render(context.Context, string, int, string) (string, error)
}
type PopplerPageRenderer struct{ Runner commandRunner }

func (renderer PopplerPageRenderer) Render(ctx context.Context, source string, page int, dir string) (string, error) {
	runner := renderer.Runner
	if runner == nil {
		runner = execCommandRunner{}
	}
	prefix := filepath.Join(dir, fmt.Sprintf("page-%d", page))
	if output, err := runner.Run(ctx, "pdftoppm", "-f", fmt.Sprint(page), "-l", fmt.Sprint(page), "-singlefile", "-png", source, prefix); err != nil {
		return "", fmt.Errorf("pdftoppm failed: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return prefix + ".png", nil
}

type PDFOCRStageHandler struct {
	Service  *Service
	Renderer PDFPageRenderer
	OCR      OCRRuntime
}

func (handler PDFOCRStageHandler) Execute(ctx context.Context, item PipelineWorkItem, policy HeavyResourcePolicy) (HeavyStageObservation, []string, error) {
	if pipelineFileFamily(item.Object) != "pdf" {
		return HeavyStageObservation{}, nil, fmt.Errorf("%w: PDF OCR cannot process %s", ErrInvalid, pipelineFileFamily(item.Object))
	}
	object, cleanup, err := handler.Service.preparePathBackedExtractionObject(ctx, item.Object, policy.MaxInputBytes)
	if err != nil {
		return HeavyStageObservation{}, nil, err
	}
	defer cleanup()
	units, err := handler.Service.store.listReadyPDFOCRUnits(ctx, item, minInt(defaultPDFOCRPagesPerClaim, policy.MaxUnits))
	if err != nil {
		return HeavyStageObservation{}, nil, err
	}
	if len(units) == 0 {
		return observedHeavy(policy, 0, 0), nil, nil
	}
	renderer := handler.Renderer
	if renderer == nil {
		renderer = PopplerPageRenderer{}
	}
	ocr := handler.OCR
	if ocr == nil {
		ocr = TesseractOCR{}
	}
	version, err := handler.Service.store.getKnowledgeObjectVersion(ctx, item.Run.KnowledgeObjectVersionID)
	if err != nil {
		return HeavyStageObservation{}, nil, err
	}
	var inputBytes int64
	for _, unit := range units {
		recordFailure := func(cause error) error {
			retryable := RetryableKnowledgeError(cause)
			status := PipelineStageStatusBlockedManual
			if retryable {
				status = PipelineStageStatusFailedRetryable
			}
			failure, _ := json.Marshal(map[string]any{"code": knowledgeProcessingErrorCode(cause), "retryable": retryable})
			if cleanupErr := handler.Service.store.cleanupPDFOCRBatch(ctx, item, unit.KnowledgePipelineStageUnitID, status, failure); cleanupErr != nil {
				return fmt.Errorf("%w (cleanup PDF OCR batch: %v)", cause, cleanupErr)
			}
			return cause
		}
		if unit.PageNumber == nil {
			continue
		}
		dir, err := os.MkdirTemp("", "loom-pdf-ocr-*")
		if err != nil {
			return HeavyStageObservation{}, nil, recordFailure(err)
		}
		image, renderErr := renderer.Render(ctx, object.SourcePath, *unit.PageNumber, dir)
		if renderErr != nil {
			os.RemoveAll(dir)
			return HeavyStageObservation{}, nil, recordFailure(renderErr)
		}
		if info, statErr := os.Stat(image); statErr == nil {
			inputBytes += info.Size()
		}
		if err := policy.ValidateWork(len(units), inputBytes); err != nil {
			os.RemoveAll(dir)
			return HeavyStageObservation{}, nil, recordFailure(err)
		}
		if inputBytes > policy.MaxTempBytes {
			os.RemoveAll(dir)
			return HeavyStageObservation{}, nil, recordFailure(fmt.Errorf("%w: temporary storage budget exceeded", ErrInvalid))
		}
		recognized, ocrErr := ocr.Recognize(ctx, OCRRequest{ImagePath: image, Language: "eng", PageNumber: *unit.PageNumber})
		os.RemoveAll(dir)
		if ocrErr != nil {
			return HeavyStageObservation{}, nil, recordFailure(ocrErr)
		}
		artifact, err := handler.Service.createAndActivatePipelineArtifact(ctx, item, version, DerivedArtifactInput{ArtifactKind: ArtifactKindOCRText, SourceLocator: fmt.Sprintf("page:%d", *unit.PageNumber), Text: recognized.Text, GeneratorKey: "loom.pdf_ocr", GeneratorVersion: "pdf_ocr.v1", EngineKey: recognized.EngineKey, EngineVersion: recognized.EngineVersion, Language: recognized.Language, Confidence: recognized.Confidence, Metadata: map[string]any{"page_number": *unit.PageNumber, "unit_input_hash": unit.UnitInputHash}})
		if err != nil {
			return HeavyStageObservation{}, nil, recordFailure(err)
		}
		result, completeErr := handler.Service.store.db.ExecContext(ctx, `UPDATE knowledge.pipeline_stage_units SET status='complete',claimed_by_worker_run_id='',claim_generation=0,output_artifact_id=$2,confidence=$3,completed_at=now(),updated_at=now() WHERE knowledge_pipeline_stage_unit_id=$1 AND status='processing' AND claimed_by_worker_run_id=$4 AND claim_generation=$5`, unit.KnowledgePipelineStageUnitID, artifact.KnowledgeDerivedArtifactID, recognized.Confidence, item.Run.ClaimedByWorkerRunID, item.Run.ClaimGeneration)
		if completeErr != nil {
			return HeavyStageObservation{}, nil, recordFailure(completeErr)
		}
		if err = requireOneRow(result, "PDF OCR unit completion fence is stale"); err != nil {
			return HeavyStageObservation{}, nil, recordFailure(err)
		}
	}
	var remaining int
	if err := handler.Service.store.db.QueryRowContext(ctx, `SELECT count(*) FROM knowledge.pipeline_stage_units WHERE knowledge_pipeline_stage_run_id=$1 AND status IN ('planned','ready','failed_retryable','processing')`, item.Stage.KnowledgePipelineStageRunID).Scan(&remaining); err != nil {
		return HeavyStageObservation{}, nil, err
	}
	warnings := []string(nil)
	if remaining > 0 {
		warnings = []string{heavyStageMoreUnits}
	}
	return observedHeavy(policy, len(units), inputBytes), warnings, nil
}

func observedHeavy(policy HeavyResourcePolicy, units int, bytes int64) HeavyStageObservation {
	enforced, _ := json.Marshal(policy)
	observed, _ := json.Marshal(map[string]any{"units": units, "input_bytes": bytes})
	return HeavyStageObservation{EnforcedPolicy: enforced, Observed: observed}
}

func scanPipelineStageUnit(scanner interface{ Scan(...any) error }) (PipelineStageUnit, error) {
	var unit PipelineStageUnit
	var page sql.NullInt64
	var artifact sql.NullString
	var confidence sql.NullFloat64
	var resource, warnings, failure, metadata []byte
	var started, completed sql.NullTime
	err := scanner.Scan(&unit.KnowledgePipelineStageUnitID, &unit.KnowledgePipelineStageRunID, &unit.UnitKey, &page, &unit.UnitInputHash, &unit.Status, &unit.AttemptCount, &unit.ClaimedByWorkerRunID, &unit.ClaimGeneration, &artifact, &confidence, &resource, &warnings, &failure, &metadata, &unit.CreatedAt, &started, &unit.UpdatedAt, &completed)
	if err != nil {
		return PipelineStageUnit{}, err
	}
	if page.Valid {
		value := int(page.Int64)
		unit.PageNumber = &value
	}
	if artifact.Valid {
		value := artifact.String
		unit.OutputArtifactID = &value
	}
	if confidence.Valid {
		value := confidence.Float64
		unit.Confidence = &value
	}
	unit.ResourceUsage = jsonObjectOrEmpty(resource)
	unit.Warnings = jsonArrayOrEmpty(warnings)
	unit.Error = jsonObjectOrEmpty(failure)
	unit.Metadata = jsonObjectOrEmpty(metadata)
	unit.StartedAt = nullTimePtr(started)
	unit.CompletedAt = nullTimePtr(completed)
	return unit, nil
}
