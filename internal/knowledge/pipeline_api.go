package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"loom.local/loom/internal/config"
	"loom.local/loom/internal/storagecatalog"
)

type PipelineListInput struct {
	Status       string `json:"status,omitempty"`
	ObjectID     string `json:"object_id,omitempty"`
	FailuresOnly bool   `json:"failures_only,omitempty"`
	Limit        int    `json:"limit,omitempty"`
}
type PipelineInspect struct {
	Run       PipelineRun        `json:"run"`
	Stages    []PipelineStageRun `json:"stages"`
	Artifacts []DerivedArtifact  `json:"artifacts"`
}
type PipelineOverallStatus struct {
	Counts     map[string]int           `json:"counts"`
	Current    []PipelineRun            `json:"current"`
	Policy     PipelinePolicyStatus     `json:"policy"`
	Operations PipelineOperationsStatus `json:"operations"`
}
type PipelineOperationsStatus struct {
	WaitingHeavy           int             `json:"waiting_heavy"`
	Blocked                int             `json:"blocked"`
	LexicalDocuments       int             `json:"lexical_documents"`
	ActiveVectors          int             `json:"active_vectors"`
	HeavyCapacity          int             `json:"heavy_capacity"`
	HeavyActiveLeases      int             `json:"heavy_active_leases"`
	HeavyExecutorAvailable bool            `json:"heavy_executor_available"`
	OldestActiveSeconds    int64           `json:"oldest_active_seconds"`
	StaleClaims            int             `json:"stale_claims"`
	ActivationMismatches   int             `json:"activation_mismatches"`
	Tools                  map[string]bool `json:"tools"`
	Runtimes               map[string]bool `json:"runtimes"`
	Models                 map[string]bool `json:"models"`
}
type PipelinePolicyStatus struct {
	Policy            PipelinePolicy    `json:"policy"`
	EmbeddingSettings EmbeddingSettings `json:"embedding_settings"`
}
type PipelinePolicyUpdate struct {
	PDFOCREnabled            *bool `json:"pdf_ocr_enabled,omitempty"`
	ImageDescriptionsEnabled *bool `json:"image_descriptions_enabled,omitempty"`
	EmbeddingsEnabled        *bool `json:"embeddings_enabled,omitempty"`
}
type PipelineRetryInput struct {
	StageKey string `json:"stage_key,omitempty"`
	Priority int    `json:"priority,omitempty"`
}

func (s *Service) ListPipelineRuns(ctx context.Context, input PipelineListInput) ([]PipelineRun, error) {
	if input.Limit <= 0 || input.Limit > 500 {
		input.Limit = 100
	}
	clauses := []string{"1=1"}
	args := []any{}
	add := func(sql string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(sql, len(args)))
	}
	if input.Status != "" {
		add("status=$%d", input.Status)
	}
	if input.ObjectID != "" {
		add("knowledge_object_id=$%d", input.ObjectID)
	}
	if input.FailuresOnly {
		clauses = append(clauses, "status IN ('failed','blocked_manual_action')")
	}
	args = append(args, input.Limit)
	rows, err := s.store.db.QueryContext(ctx, `SELECT `+pipelineRunColumns()+` FROM knowledge.pipeline_runs WHERE `+strings.Join(clauses, " AND ")+fmt.Sprintf(` ORDER BY updated_at DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []PipelineRun
	for rows.Next() {
		run, err := scanPipelineRun(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, redactPipelineRun(run))
	}
	return result, rows.Err()
}

func (s *Service) InspectPipeline(ctx context.Context, ref string) (PipelineInspect, error) {
	ref = strings.TrimSpace(ref)
	run, err := s.store.GetPipelineRun(ctx, ref)
	if err == sql.ErrNoRows {
		object, objectErr := s.store.GetKnowledgeObject(ctx, ref)
		if objectErr != nil {
			return PipelineInspect{}, objectErr
		}
		run, err = scanPipelineRun(s.store.db.QueryRowContext(ctx, `SELECT `+pipelineRunColumns()+` FROM knowledge.pipeline_runs WHERE knowledge_object_id=$1 ORDER BY generation DESC LIMIT 1`, object.KnowledgeObjectID))
	}
	if err != nil {
		return PipelineInspect{}, err
	}
	stages, err := s.store.ListPipelineStageRuns(ctx, run.KnowledgePipelineRunID)
	if err != nil {
		return PipelineInspect{}, err
	}
	artifacts, err := s.store.ListDerivedArtifacts(ctx, run.KnowledgePipelineRunID, false)
	if err != nil {
		return PipelineInspect{}, err
	}
	for index := range artifacts {
		artifacts[index].TextContent = nil
		artifacts[index].PayloadRef = ""
	}
	for index := range stages {
		stages[index].Error = redactPipelineStageError(stages[index].Error)
	}
	return PipelineInspect{Run: redactPipelineRun(run), Stages: stages, Artifacts: artifacts}, nil
}

func redactPipelineRun(run PipelineRun) PipelineRun {
	run.LastErrorMessage = ""
	return run
}

func redactPipelineStageError(raw json.RawMessage) json.RawMessage {
	var decoded map[string]any
	if json.Unmarshal(raw, &decoded) != nil || len(decoded) == 0 {
		return json.RawMessage(`{}`)
	}
	safe := map[string]any{}
	for _, key := range []string{"code", "retryable"} {
		if value, ok := decoded[key]; ok {
			safe[key] = value
		}
	}
	payload, _ := json.Marshal(safe)
	return payload
}

func (s *Service) GetPipelineOverallStatus(ctx context.Context) (PipelineOverallStatus, error) {
	rows, err := s.store.db.QueryContext(ctx, `SELECT status,count(*)::int FROM knowledge.pipeline_runs GROUP BY status`)
	if err != nil {
		return PipelineOverallStatus{}, err
	}
	counts := map[string]int{}
	for rows.Next() {
		var status string
		var count int
		if err = rows.Scan(&status, &count); err != nil {
			rows.Close()
			return PipelineOverallStatus{}, err
		}
		counts[status] = count
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return PipelineOverallStatus{}, err
	}
	if err = rows.Close(); err != nil {
		return PipelineOverallStatus{}, err
	}
	currentRows, err := s.store.db.QueryContext(ctx, `SELECT `+pipelineRunColumns()+` FROM knowledge.pipeline_runs
		WHERE status NOT IN ('complete','complete_with_warnings','stale','cancelled')
		ORDER BY updated_at DESC LIMIT 100`)
	if err != nil {
		return PipelineOverallStatus{}, err
	}
	current := []PipelineRun{}
	for currentRows.Next() {
		run, scanErr := scanPipelineRun(currentRows)
		if scanErr != nil {
			currentRows.Close()
			return PipelineOverallStatus{}, scanErr
		}
		current = append(current, redactPipelineRun(run))
	}
	if err = currentRows.Err(); err != nil {
		currentRows.Close()
		return PipelineOverallStatus{}, err
	}
	if err = currentRows.Close(); err != nil {
		return PipelineOverallStatus{}, err
	}
	policy, err := s.GetPipelinePolicy(ctx)
	if err != nil {
		return PipelineOverallStatus{}, err
	}
	operations, err := s.pipelineOperationsStatus(ctx, policy)
	if err != nil {
		return PipelineOverallStatus{}, err
	}
	return PipelineOverallStatus{Counts: counts, Current: current, Policy: policy, Operations: operations}, nil
}

func (s *Service) pipelineOperationsStatus(ctx context.Context, policy PipelinePolicyStatus) (PipelineOperationsStatus, error) {
	status := PipelineOperationsStatus{Tools: map[string]bool{}, Runtimes: map[string]bool{}, Models: map[string]bool{}}
	for _, tool := range []string{"pdfinfo", "pdftotext", "pdftoppm", "tesseract"} {
		_, err := exec.LookPath(tool)
		status.Tools[tool] = err == nil
	}
	runtimeConfig, configErr := config.Load(config.Overrides{})
	status.Runtimes["embedding"] = configErr == nil && strings.TrimSpace(runtimeConfig.EmbeddingRuntime) != ""
	status.Runtimes["vision"] = configErr == nil && strings.TrimSpace(runtimeConfig.VisionRuntime) != ""
	status.Models["embedding"] = !policy.Policy.EmbeddingsEnabled || strings.TrimSpace(policy.EmbeddingSettings.ModelKey) != ""
	status.Models["vision"] = !policy.Policy.ImageDescriptionsEnabled || configErr == nil && strings.TrimSpace(runtimeConfig.VisionModel) != ""
	if err := s.store.db.QueryRowContext(ctx, `SELECT count(*) FILTER(WHERE status='waiting_heavy')::int,count(*) FILTER(WHERE status IN ('failed','blocked_manual_action'))::int,COALESCE(EXTRACT(EPOCH FROM (now()-(min(created_at) FILTER(WHERE status NOT IN ('complete','complete_with_warnings','stale','cancelled'))))),0)::bigint,count(*) FILTER(WHERE status='processing' AND claim_expires_at<=now())::int FROM knowledge.pipeline_runs WHERE status<>'stale'`).Scan(&status.WaitingHeavy, &status.Blocked, &status.OldestActiveSeconds, &status.StaleClaims); err != nil {
		return status, err
	}
	if err := s.store.db.QueryRowContext(ctx, `SELECT count(*)::int FROM search.search_documents WHERE source_kind=$1`, KnowledgeSearchSourceKind).Scan(&status.LexicalDocuments); err != nil {
		return status, err
	}
	if err := s.store.db.QueryRowContext(ctx, `SELECT count(*)::int FROM knowledge.chunk_embeddings WHERE active=true`).Scan(&status.ActiveVectors); err != nil {
		return status, err
	}
	if err := s.store.db.QueryRowContext(ctx, `SELECT COALESCE(max(c.capacity),0)::int,count(l.worker_resource_lease_id)::int FROM workers.resource_capacities c LEFT JOIN workers.resource_leases l ON l.resource_key=c.resource_key AND l.status='active' AND l.expires_at>now() WHERE c.resource_key='knowledge_heavy'`).Scan(&status.HeavyCapacity, &status.HeavyActiveLeases); err != nil {
		return status, err
	}
	if err := s.store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workers.worker_instances WHERE worker_key='main.knowledge_heavy' AND enabled=true AND paused=false AND lifecycle_status NOT IN ('disabled','retired'))`).Scan(&status.HeavyExecutorAvailable); err != nil {
		return status, err
	}
	if err := s.store.db.QueryRowContext(ctx, `SELECT ((SELECT count(*) FROM knowledge.derived_artifacts a JOIN knowledge.pipeline_runs r ON r.knowledge_pipeline_run_id=a.knowledge_pipeline_run_id WHERE a.active=true AND (a.generation<>r.generation OR r.status IN ('stale','cancelled'))) + (SELECT count(*) FROM knowledge.chunk_embeddings e LEFT JOIN knowledge.knowledge_chunks c ON c.knowledge_chunk_id=e.knowledge_chunk_id WHERE e.active=true AND (c.knowledge_chunk_id IS NULL OR e.knowledge_object_version_id IS DISTINCT FROM c.knowledge_object_version_id)))::int`).Scan(&status.ActivationMismatches); err != nil {
		return status, err
	}
	return status, nil
}

func (s *Service) GetPipelinePolicy(ctx context.Context) (PipelinePolicyStatus, error) {
	settings, err := s.store.GetEmbeddingSettings(ctx)
	if err != nil {
		return PipelinePolicyStatus{}, err
	}
	policy := pipelinePolicyFromSettings(settings)
	return PipelinePolicyStatus{Policy: policy, EmbeddingSettings: settings}, nil
}

func (s *Service) UpdatePipelinePolicy(ctx context.Context, input PipelinePolicyUpdate) (PipelinePolicyStatus, error) {
	status, _, err := s.updatePipelinePolicyAndReconcile(ctx, input)
	return status, err
}

func pipelinePolicyFromSettings(settings EmbeddingSettings) PipelinePolicy {
	policy := DefaultPipelinePolicy()
	policy.EmbeddingsEnabled = settings.Enabled
	metadata := jsonObject(settings.Metadata)
	if value, ok := metadata["pdf_ocr_enabled"].(bool); ok {
		policy.PDFOCREnabled = value
	}
	if value, ok := metadata["image_descriptions_enabled"].(bool); ok {
		policy.ImageDescriptionsEnabled = value
	}
	return policy
}

func (s *Service) updatePipelinePolicyAndReconcile(ctx context.Context, input PipelinePolicyUpdate) (PipelinePolicyStatus, int, error) {
	if s == nil || s.store.db == nil {
		return PipelinePolicyStatus{}, 0, fmt.Errorf("knowledge store is not configured")
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return PipelinePolicyStatus{}, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := storagecatalog.LockWorkspaceCustodyReaderTx(ctx, tx); err != nil {
		return PipelinePolicyStatus{}, 0, err
	}
	settings, err := getEmbeddingSettingsTx(ctx, tx)
	if err != nil {
		return PipelinePolicyStatus{}, 0, err
	}
	currentPolicy := pipelinePolicyFromSettings(settings)
	policy := currentPolicy
	if input.PDFOCREnabled != nil {
		policy.PDFOCREnabled = *input.PDFOCREnabled
	}
	if input.ImageDescriptionsEnabled != nil {
		policy.ImageDescriptionsEnabled = *input.ImageDescriptionsEnabled
	}
	if input.EmbeddingsEnabled != nil {
		policy.EmbeddingsEnabled = *input.EmbeddingsEnabled
	}
	metadata := jsonObject(settings.Metadata)
	metadata["schema_version"] = "knowledge.pipeline_policy.v1"
	metadata["pdf_ocr_enabled"] = policy.PDFOCREnabled
	metadata["image_descriptions_enabled"] = policy.ImageDescriptionsEnabled
	payload, _ := json.Marshal(metadata)
	settings, err = scanEmbeddingSettings(tx.QueryRowContext(ctx, `INSERT INTO knowledge.embedding_settings (
		embedding_settings_id,enabled,runtime_key,model_key,dimensions,distance_metric,ollama_url,
		quiet_window_seconds,global_concurrency,history_per_lineage,metadata,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,now(),now())
		ON CONFLICT (embedding_settings_id) DO UPDATE SET enabled=EXCLUDED.enabled,metadata=EXCLUDED.metadata,updated_at=now()
		RETURNING `+embeddingSettingsColumns(), EmbeddingSettingsID, policy.EmbeddingsEnabled,
		settings.RuntimeKey, settings.ModelKey, settings.Dimensions, settings.DistanceMetric, settings.OllamaURL,
		settings.QuietWindowSeconds, settings.GlobalConcurrency, settings.HistoryPerLineage, payload))
	if err != nil {
		return PipelinePolicyStatus{}, 0, err
	}
	if currentPolicy == policy {
		if err = tx.Commit(); err != nil {
			return PipelinePolicyStatus{}, 0, err
		}
		return PipelinePolicyStatus{Policy: policy, EmbeddingSettings: settings}, 0, nil
	}

	// Discovery is intentionally unlocked. Each ID is rescanned after acquiring
	// the canonical run/stage/unit -> object lock order, so a source update made
	// while this list is being traversed cannot compile a stale trajectory.
	rows, err := tx.QueryContext(ctx, `SELECT `+knowledgeObjectColumns()+` FROM knowledge.knowledge_objects WHERE deleted_at IS NULL ORDER BY knowledge_object_id`)
	if err != nil {
		return PipelinePolicyStatus{}, 0, err
	}
	var objects []KnowledgeObject
	for rows.Next() {
		object, scanErr := scanKnowledgeObject(rows)
		if scanErr != nil {
			rows.Close()
			return PipelinePolicyStatus{}, 0, scanErr
		}
		objects = append(objects, object)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return PipelinePolicyStatus{}, 0, err
	}
	if err = rows.Close(); err != nil {
		return PipelinePolicyStatus{}, 0, err
	}
	queued := 0
	for _, object := range objects {
		created, ensureErr := s.reconcilePipelinePolicyObjectTx(ctx, tx, object.KnowledgeObjectID, currentPolicy, policy)
		if ensureErr != nil {
			return PipelinePolicyStatus{}, 0, ensureErr
		}
		if created {
			queued++
		}
	}
	if err = tx.Commit(); err != nil {
		return PipelinePolicyStatus{}, 0, err
	}
	return PipelinePolicyStatus{Policy: policy, EmbeddingSettings: settings}, queued, nil
}

func (s *Service) reconcilePipelinePolicyObjectTx(ctx context.Context, tx *sql.Tx, objectID string, oldPolicy, newPolicy PipelinePolicy) (bool, error) {
	object, err := lockCurrentPipelineObjectTx(ctx, tx, objectID)
	if err != nil {
		if err == sql.ErrNoRows || errors.Is(err, ErrNotesCustodyPaused) {
			return false, nil
		}
		return false, err
	}
	oldPlan, err := CompilePipelinePlan(object, oldPolicy)
	if err != nil {
		return false, err
	}
	newPlan, err := CompilePipelinePlan(object, newPolicy)
	if err != nil {
		return false, err
	}
	oldSnapshot, err := oldPlan.Snapshot()
	if err != nil {
		return false, err
	}
	newSnapshot, err := newPlan.Snapshot()
	if err != nil {
		return false, err
	}
	if pipelinePlanSnapshotsEqual(oldSnapshot, newSnapshot) {
		return false, nil
	}
	eligible, err := policyTransitionObjectEligibleTx(ctx, tx, object, oldPolicy, newPolicy)
	if err != nil || !eligible {
		return false, err
	}
	_, created, err := s.ensureLockedPipelineRunTx(ctx, tx, object, newPlan, newSnapshot, false, DefaultPipelinePriority, nil)
	return created, err
}

func policyTransitionObjectEligibleTx(ctx context.Context, tx *sql.Tx, object KnowledgeObject, oldPolicy, newPolicy PipelinePolicy) (bool, error) {
	oldPlan, err := CompilePipelinePlan(object, oldPolicy)
	if err != nil {
		return false, err
	}
	newPlan, err := CompilePipelinePlan(object, newPolicy)
	if err != nil {
		return false, err
	}
	if !oldPolicy.PDFOCREnabled && newPolicy.PDFOCREnabled {
		if compiledPlanContainsSelectedStage(newPlan, FilePipelineStagePDFOCR) {
			return true, nil
		}
	}
	if !oldPolicy.ImageDescriptionsEnabled && newPolicy.ImageDescriptionsEnabled {
		if compiledPlanContainsSelectedStage(newPlan, FilePipelineStageImageDescription) {
			return true, nil
		}
	}
	if !oldPolicy.EmbeddingsEnabled && newPolicy.EmbeddingsEnabled {
		if compiledPlanContainsSelectedStage(newPlan, FilePipelineStageEmbedding) {
			var hasCurrentChunk bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
				SELECT 1 FROM knowledge.knowledge_object_versions v
				JOIN knowledge.knowledge_chunks c ON c.knowledge_object_version_id=v.knowledge_object_version_id
				WHERE v.knowledge_object_id=$1 AND v.version_number=(SELECT max(v2.version_number) FROM knowledge.knowledge_object_versions v2 WHERE v2.knowledge_object_id=v.knowledge_object_id)
				  AND c.status IN ('created','indexed'))`, object.KnowledgeObjectID).Scan(&hasCurrentChunk); err != nil {
				return false, err
			}
			if hasCurrentChunk {
				return true, nil
			}
		}
	}
	if oldPolicy.PDFOCREnabled && !newPolicy.PDFOCREnabled && compiledPlanContainsSelectedStage(oldPlan, FilePipelineStagePDFOCR) {
		return hasCurrentPipelineRunTx(ctx, tx, object.KnowledgeObjectID)
	}
	if oldPolicy.ImageDescriptionsEnabled && !newPolicy.ImageDescriptionsEnabled && compiledPlanContainsSelectedStage(oldPlan, FilePipelineStageImageDescription) {
		return hasCurrentPipelineRunTx(ctx, tx, object.KnowledgeObjectID)
	}
	if oldPolicy.EmbeddingsEnabled && !newPolicy.EmbeddingsEnabled && compiledPlanContainsSelectedStage(oldPlan, FilePipelineStageEmbedding) {
		return hasCurrentPipelineRunTx(ctx, tx, object.KnowledgeObjectID)
	}
	return false, nil
}

func hasCurrentPipelineRunTx(ctx context.Context, tx *sql.Tx, objectID string) (bool, error) {
	var found bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM knowledge.pipeline_runs
		WHERE knowledge_object_id=$1 AND status NOT IN ('stale','cancelled'))`, objectID).Scan(&found)
	return found, err
}

func compiledPlanContainsSelectedStage(plan CompiledPipelinePlan, stageKey string) bool {
	for _, stage := range plan.Stages {
		if stage.StageKey == stageKey && stage.Selected {
			return true
		}
	}
	return false
}

func (s *Service) RetryPipeline(ctx context.Context, ref string, input PipelineRetryInput) (PipelineRun, error) {
	inspection, err := s.InspectPipeline(ctx, ref)
	if err != nil {
		return PipelineRun{}, err
	}
	input.StageKey = strings.TrimSpace(input.StageKey)
	if input.StageKey != "" {
		found := false
		for _, stage := range inspection.Stages {
			if stage.StageKey == input.StageKey {
				found = true
				break
			}
		}
		if !found {
			return PipelineRun{}, fmt.Errorf("%w: pipeline stage %q was not found", ErrInvalid, input.StageKey)
		}
	}
	object, err := s.store.GetKnowledgeObject(ctx, inspection.Run.KnowledgeObjectID)
	if err != nil {
		return PipelineRun{}, err
	}
	policyStatus, err := s.GetPipelinePolicy(ctx)
	if err != nil {
		return PipelineRun{}, err
	}
	if input.StageKey == "" {
		return s.EnsurePipelineRun(ctx, object, policyStatus.Policy, true, input.Priority)
	}
	inspection.Artifacts, err = s.store.ListDerivedArtifacts(ctx, inspection.Run.KnowledgePipelineRunID, false)
	if err != nil {
		return PipelineRun{}, err
	}
	run, _, err := s.ensurePipelineRunWithRetry(ctx, object, policyStatus.Policy, true, input.Priority, &pipelineRetrySpec{Source: inspection, TargetStage: input.StageKey})
	return run, err
}
