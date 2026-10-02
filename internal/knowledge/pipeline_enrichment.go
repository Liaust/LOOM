package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/storagecatalog"
	"os/exec"
	"reflect"
	"strings"
)

// EnrichmentStages is explicit one-version intent, never a source default.
type EnrichmentStages struct {
	OCR        bool `json:"ocr"`
	Vision     bool `json:"vision"`
	Embeddings bool `json:"embeddings"`
}

func (s EnrichmentStages) any() bool { return s.OCR || s.Vision || s.Embeddings }
func (s EnrichmentStages) contains(other EnrichmentStages) bool {
	return (!other.OCR || s.OCR) && (!other.Vision || s.Vision) && (!other.Embeddings || s.Embeddings)
}
func (s EnrichmentStages) union(other EnrichmentStages) EnrichmentStages {
	return EnrichmentStages{s.OCR || other.OCR, s.Vision || other.Vision, s.Embeddings || other.Embeddings}
}

type EnrichmentBinding struct {
	ObjectID       string `json:"object_id"`
	SourceRevision string `json:"source_revision"`
	SourceHash     string `json:"source_hash"`
	AdmissionHash  string `json:"admission_hash"`
}
type PipelineEnrichment struct {
	Binding         EnrichmentBinding `json:"binding"`
	Requested       EnrichmentStages  `json:"requested"`
	ActorID         string            `json:"actor_id"`
	AutomaticPolicy PipelinePolicy    `json:"automatic_policy"`
	HostPolicy      PipelinePolicy    `json:"host_policy"`
	// Retained OCR/vision stages are copied, never implicitly executed again.
	Retained  EnrichmentStages `json:"retained"`
	Preserved EnrichmentStages `json:"preserved"`
}

func enrichmentBinding(object KnowledgeObject) (EnrichmentBinding, error) {
	source, err := knowledgeSourcePolicy(object)
	if err != nil {
		return EnrichmentBinding{}, err
	}
	// Refresh timing does not change processing authority.
	if source != nil {
		source.Refresh = nil
	}
	raw, err := json.Marshal([]any{object.NotesSourceRootID, object.SourcePath, object.RelativePath,
		object.SourceNodeID, object.SourceNodeKey, object.ProjectID, object.FileClass, source})
	if err != nil {
		return EnrichmentBinding{}, err
	}
	return EnrichmentBinding{object.KnowledgeObjectID, object.SourceRevision, object.SourceHash, hashArtifactValue(string(raw))}, nil
}

func compileEnrichmentPlan(object KnowledgeObject, host PipelinePolicy, intent PipelineEnrichment) (CompiledPipelinePlan, error) {
	binding, err := enrichmentBinding(object)
	if err != nil {
		return CompiledPipelinePlan{}, err
	}
	if binding != intent.Binding || !reflect.DeepEqual(host, intent.HostPolicy) {
		return CompiledPipelinePlan{}, fmt.Errorf("%w: enrichment source or processing policy changed; preview again", ErrConflict)
	}
	stages := intent.Requested.union(intent.Retained)
	family := pipelineFileFamily(object)
	if stages.OCR && family != "pdf" && family != "image" {
		return CompiledPipelinePlan{}, fmt.Errorf("%w: OCR is supported for PDF and image objects only", ErrInvalid)
	}
	if stages.Vision && family != "image" {
		return CompiledPipelinePlan{}, fmt.Errorf("%w: vision is supported for images only; PDF vision is unavailable", ErrInvalid)
	}
	if stages.Embeddings && family == "metadata_only" {
		return CompiledPipelinePlan{}, fmt.Errorf("%w: embeddings are unsupported for metadata-only objects", ErrInvalid)
	}
	if (stages.OCR && ((family == "pdf" && !host.PDFOCREnabled) || (family == "image" && !host.ImageOCREnabled))) ||
		(stages.Vision && !host.ImageDescriptionsEnabled) || (stages.Embeddings && !host.EmbeddingsEnabled) {
		return CompiledPipelinePlan{}, fmt.Errorf("%w: requested enrichment stage is disabled by host policy", ErrInvalid)
	}
	plan, err := CompilePipelinePlan(object, host)
	if err != nil {
		return plan, err
	}
	effective := effectiveSourcePipelinePolicy(host, plan.SourcePolicy)
	effective.PDFOCREnabled = stages.OCR && family == "pdf"
	effective.ImageOCREnabled = stages.OCR && family == "image"
	effective.ImageDescriptionsEnabled = stages.Vision
	effective.EmbeddingsEnabled = effective.EmbeddingsEnabled || stages.Embeddings
	for i := range plan.Stages {
		stage := &plan.Stages[i]
		switch stage.StageKey {
		case FilePipelineStagePDFOCR:
			stage.Selected = effective.PDFOCREnabled
		case FilePipelineStageImageOCR:
			stage.Selected = effective.ImageOCREnabled
		case FilePipelineStageImageDescription:
			stage.Selected = effective.ImageDescriptionsEnabled
		case FilePipelineStageEmbedding:
			stage.Selected = effective.EmbeddingsEnabled
		default:
			continue
		}
		if stage.Selected {
			stage.SkipReason = ""
		} else if stage.SkipReason == "" {
			stage.SkipReason = "not_requested_for_enrichment"
		}
	}
	plan.EffectivePolicy, plan.Enrichment = &effective, &intent
	return plan, nil
}

// Caller holds the canonical trajectory/object locks. Only the latest trajectory
// may carry authority forward; historical intents never revive after withdrawal.
func recordedEnrichmentPlanTx(ctx context.Context, tx *sql.Tx, object KnowledgeObject, host PipelinePolicy) (CompiledPipelinePlan, error) {
	automatic, err := CompilePipelinePlan(object, host)
	if err != nil {
		return automatic, err
	}
	run, err := latestEnrichmentRunTx(ctx, tx, object.KnowledgeObjectID)
	if err == sql.ErrNoRows {
		return automatic, nil
	}
	if err != nil {
		return automatic, err
	}
	if run.Status == FilePipelineStatusStale || run.Status == FilePipelineStatusCancelled {
		return automatic, nil
	}
	var previous CompiledPipelinePlan
	if json.Unmarshal(run.PlanSnapshot, &previous) != nil || previous.Enrichment == nil {
		return automatic, nil
	}
	selected := object
	if run.StartedAt != nil && len(run.SourceSnapshot) > 0 && !isRetryablePipelineRunStatus(run.Status) {
		captured, e := capturedPipelineObject(run, object)
		if e == nil && pipelineAdmissionMatches(captured, object) {
			selected = captured
		}
	}
	plan, err := compileEnrichmentPlan(selected, host, *previous.Enrichment)
	if err != nil {
		return automatic, nil
	}
	return plan, nil
}
func latestEnrichmentRunTx(ctx context.Context, tx *sql.Tx, id string) (PipelineRun, error) {
	return scanPipelineRun(tx.QueryRowContext(ctx, `SELECT `+pipelineRunColumns()+` FROM knowledge.pipeline_runs WHERE knowledge_object_id=$1 ORDER BY generation DESC LIMIT 1`, id))
}

// EnrichPipelineObject binds and queues one exact admitted version atomically.
// Empty version constraints are permitted only for a direct single-object call;
// folder callers supply the complete preview binding.
func (s *Service) EnrichPipelineObject(ctx context.Context, expected EnrichmentBinding, requested EnrichmentStages, actor string) (PipelineRun, error) {
	if !requested.any() || strings.TrimSpace(actor) == "" {
		return PipelineRun{}, fmt.Errorf("%w: enrichment stages and actor are required", ErrInvalid)
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return PipelineRun{}, err
	}
	defer tx.Rollback()
	if err = storagecatalog.LockWorkspaceCustodyReaderTx(ctx, tx); err != nil {
		return PipelineRun{}, err
	}
	settings, err := getEmbeddingSettingsTx(ctx, tx)
	if err != nil {
		return PipelineRun{}, err
	}
	host := pipelinePolicyFromSettings(settings)
	object, err := lockCurrentPipelineObjectTx(ctx, tx, expected.ObjectID)
	if err != nil {
		return PipelineRun{}, err
	}
	var admitted bool
	if err = tx.QueryRowContext(ctx, `SELECT `+visibleNotesKnowledgeObjectSQL("o")+` FROM knowledge.knowledge_objects o WHERE knowledge_object_id=$1`, object.KnowledgeObjectID).Scan(&admitted); err != nil {
		return PipelineRun{}, err
	}
	if !admitted {
		return PipelineRun{}, fmt.Errorf("%w: object is not currently admitted", ErrInvalid)
	}
	binding, err := enrichmentBinding(object)
	if err != nil {
		return PipelineRun{}, err
	}
	if (expected.SourceRevision != "" && expected.SourceRevision != binding.SourceRevision) ||
		(expected.SourceHash != "" && expected.SourceHash != binding.SourceHash) ||
		(expected.AdmissionHash != "" && expected.AdmissionHash != binding.AdmissionHash) {
		return PipelineRun{}, fmt.Errorf("%w: stale enrichment preview; source version or admission changed", ErrConflict)
	}
	if object.SizeBytes != nil && *object.SizeBytes > DefaultPDFMaxSourceBytes {
		return PipelineRun{}, fmt.Errorf("%w: source exceeds the 1 GiB preparation limit", ErrInvalid)
	}
	source, err := knowledgeSourcePolicy(object)
	if err != nil {
		return PipelineRun{}, err
	}
	intent := PipelineEnrichment{Binding: binding, Requested: requested, ActorID: actor, HostPolicy: host, AutomaticPolicy: effectiveSourcePipelinePolicy(host, source)}
	previous, previousErr := latestEnrichmentRunTx(ctx, tx, object.KnowledgeObjectID)
	if previousErr != nil && previousErr != sql.ErrNoRows {
		return PipelineRun{}, previousErr
	}
	var oldPlan CompiledPipelinePlan
	if previousErr == nil && previous.Status != FilePipelineStatusStale && previous.Status != FilePipelineStatusCancelled && json.Unmarshal(previous.PlanSnapshot, &oldPlan) == nil {
		if oldPlan.Enrichment != nil {
			if _, e := compileEnrichmentPlan(object, host, *oldPlan.Enrichment); e == nil {
				embeddingsPublishedForOutput := false
				if requested.Embeddings && oldPlan.Enrichment.Preserved.Embeddings && !oldPlan.Enrichment.Requested.Embeddings && !oldPlan.Enrichment.Retained.Embeddings {
					// Prior permission is not evidence that newly derived text was
					// vectorized. The object is locked, so publication cannot move
					// between this check and queue creation.
					if err = tx.QueryRowContext(ctx, `SELECT COALESCE(lexical_version_id=semantic_version_id AND lexical_version_id=$2,false)
                        FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1`, object.KnowledgeObjectID, previous.KnowledgeObjectVersionID).Scan(&embeddingsPublishedForOutput); err != nil {
						return PipelineRun{}, err
					}
				}
				if enrichmentReplayStages(*oldPlan.Enrichment, embeddingsPublishedForOutput).contains(requested) {
					if err = tx.Commit(); err != nil {
						return PipelineRun{}, err
					}
					return previous, nil
				}
				if !isRetryablePipelineRunStatus(previous.Status) {
					return PipelineRun{}, fmt.Errorf("%w: enrichment is already in progress; wait before adding stages", ErrConflict)
				}
				intent.Preserved = oldPlan.Enrichment.Requested.union(oldPlan.Enrichment.Retained).union(oldPlan.Enrichment.Preserved)
			}
		}
	}
	plan, err := compileEnrichmentPlan(object, host, intent)
	if err != nil {
		return PipelineRun{}, err
	}
	if err = s.checkEnrichmentRuntime(ctx, plan.FileFamily, requested, settings); err != nil {
		return PipelineRun{}, err
	}
	// Reuse only a finished compatible prefix of this exact version. Embedding-only
	// keeps valid extraction/chunks, including previously authorized OCR/vision.
	var retry *pipelineRetrySpec
	if previousErr == nil && isRetryablePipelineRunStatus(previous.Status) && previous.SourceHash == object.SourceHash && previous.SourceRevision == object.SourceRevision {
		candidate := plan
		candidateIntent := intent
		if oldPlan.Enrichment == nil || intent.Preserved.any() {
			for _, stage := range oldPlan.Stages {
				if !stage.Selected {
					continue
				}
				switch stage.StageKey {
				case FilePipelineStagePDFOCR, FilePipelineStageImageOCR:
					candidateIntent.Retained.OCR = !requested.OCR
				case FilePipelineStageImageDescription:
					candidateIntent.Retained.Vision = !requested.Vision
				}
			}
			// Retained outputs need either the unchanged source defaults or valid prior intent.
			if oldPlan.Enrichment == nil && !reflect.DeepEqual(oldPlan.SourcePolicy, plan.SourcePolicy) {
				candidateIntent.Retained = EnrichmentStages{}
			}
			if p, e := compileEnrichmentPlan(object, host, candidateIntent); e == nil {
				candidate = p
			}
		}
		target := FilePipelineStageEmbedding
		if requested.Vision {
			target = FilePipelineStageImageDescription
		}
		if requested.OCR {
			target = FilePipelineStageImageOCR
			if plan.FileFamily == "pdf" {
				target = FilePipelineStagePDFPageAnalysis
			}
		}
		// Start at the earliest newly requested stage, preserving only its prefix.
		for i, stage := range candidate.Stages {
			if stage.StageKey != target {
				continue
			}
			candidate.HeavyQuietWindowSeconds = settings.QuietWindowSeconds
			snapshot, _ := candidate.Snapshot()
			version := KnowledgeObjectVersion{KnowledgeObjectVersionID: previous.KnowledgeObjectVersionID}
			inspection, e := validateRetryReuseTx(ctx, tx, object, version, candidate, snapshot, previous.KnowledgePipelineRunID, i)
			if e == nil {
				extra := map[string]bool{}
				for _, compiled := range candidate.Stages {
					retain := enrichmentRetainedStage(candidateIntent, compiled.StageKey)
					if !retain || compiled.Ordinal < stage.Ordinal {
						continue
					}
					var old PipelineStageRun
					for _, oldStage := range inspection.Stages {
						if oldStage.StageKey == compiled.StageKey {
							old = oldStage
						}
					}
					if !isReusableUpstreamStageStatus(old.Status) || !reusableStageMetadataMatches(old, compiled) || (old.Status != PipelineStageStatusSkippedNotApplicable && !hasReusableStageArtifact(inspection.Artifacts, inspection.Run, old, compiled.OutputArtifactKinds)) {
						e = fmt.Errorf("%w: existing %s output is not reusable", ErrConflict, compiled.StageKey)
						break
					}
					extra[compiled.StageKey] = true
				}
				if e == nil {
					plan = candidate
					retry = &pipelineRetrySpec{Source: inspection, TargetStage: target, ReuseStages: extra}
				}
			}
			break
		}
	}
	if retry == nil && ((!requested.OCR && intent.Preserved.OCR) || (!requested.Vision && intent.Preserved.Vision)) {
		return PipelineRun{}, fmt.Errorf("%w: prior enrichment outputs are not reusable; retry the prior run or explicitly select those stages again", ErrConflict)
	}
	snapshot, err := plan.Snapshot()
	if err != nil {
		return PipelineRun{}, err
	}
	run, _, err := s.ensureLockedPipelineRunTx(ctx, tx, object, plan, snapshot, true, DefaultPipelinePriority, retry)
	if err != nil {
		return PipelineRun{}, err
	}
	if err = tx.Commit(); err != nil {
		return PipelineRun{}, err
	}
	s.cleanupSupersededPipelineInputs(ctx, object.KnowledgeObjectID)
	return run, nil
}

func (s *Service) checkEnrichmentRuntime(ctx context.Context, family string, stages EnrichmentStages, settings EmbeddingSettings) error {
	if stages.OCR {
		required := []string{"tesseract"}
		if family == "pdf" {
			required = append(required, "pdfinfo", "pdftotext", "pdftoppm")
		}
		for _, tool := range required {
			if _, err := exec.LookPath(tool); err != nil {
				return fmt.Errorf("%w: OCR requires the host tool %s", ErrInvalid, tool)
			}
		}
	}
	if stages.Embeddings {
		if settings.RuntimeKey != EmbeddingRuntimeOllama {
			return fmt.Errorf("%w: embedding runtime is unavailable", ErrInvalid)
		}
		available, models := ollamaModelAvailability(ctx, settings.OllamaURL)
		if !available || !models[ollamaModelName(settings.ModelKey)] {
			return fmt.Errorf("%w: configured embedding runtime/model is unavailable", ErrInvalid)
		}
	}
	if stages.Vision {
		cfg, err := config.Load(config.Overrides{})
		if err != nil || cfg.VisionRuntime != "ollama" {
			return fmt.Errorf("%w: image vision runtime is unavailable", ErrInvalid)
		}
		available, models := ollamaModelAvailability(ctx, cfg.VisionOllamaURL)
		if !available || !models[ollamaModelName(cfg.VisionModel)] {
			return fmt.Errorf("%w: configured image vision model is unavailable", ErrInvalid)
		}
	}
	return nil
}

func enrichmentRetainedStage(intent PipelineEnrichment, stage string) bool {
	switch stage {
	case FilePipelineStagePDFOCR, FilePipelineStageImageOCR:
		return intent.Retained.OCR
	case FilePipelineStageImageDescription:
		return intent.Retained.Vision
	default:
		return false
	}
}

// A current embedding request still owns its queued/failed/completed run. Only
// embeddings carried from earlier work need proof of publication for this output.
func enrichmentReplayStages(intent PipelineEnrichment, embeddingsPublishedForOutput bool) EnrichmentStages {
	stages := intent.Requested.union(intent.Retained).union(intent.Preserved)
	if !intent.Requested.Embeddings && !intent.Retained.Embeddings && !embeddingsPublishedForOutput {
		stages.Embeddings = false
	}
	return stages
}
