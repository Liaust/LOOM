package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/workers"

	"loom.local/loom/internal/storagecatalog"
)

type PipelineListInput struct {
	Status       string `json:"status,omitempty"`
	ObjectID     string `json:"object_id,omitempty"`
	FailuresOnly bool   `json:"failures_only,omitempty"`
	Limit        int    `json:"limit,omitempty"`
}
type PipelineInspect struct {
	Source    *PipelineSourceStatus `json:"source,omitempty"`
	Refresh   PipelineRefreshStatus `json:"refresh"`
	Run       PipelineRun           `json:"run"`
	Stages    []PipelineStageRun    `json:"stages"`
	Artifacts []DerivedArtifact     `json:"artifacts"`
}
type PipelineOverallStatus struct {
	Progress *PipelineProgress `json:"progress,omitempty"`
	// NativePublication is the typed notesworkspacesync.WorkspaceProgress HTTP
	// extension; a raw extension avoids the knowledge/Notes ownership import cycle.
	NativePublication json.RawMessage          `json:"native_publication,omitempty"`
	Counts            map[string]int           `json:"counts"`
	Current           []PipelineRun            `json:"current"`
	Policy            PipelinePolicyStatus     `json:"policy"`
	Operations        PipelineOperationsStatus `json:"operations"`
}
type PipelineOperationsStatus struct {
	Unknown                map[string]string `json:"unknown,omitempty"`
	CapacityKnown          bool              `json:"capacity_known"`
	ExecutorKnown          bool              `json:"executor_known"`
	IntegrityCheckedAt     *time.Time        `json:"integrity_checked_at,omitempty"`
	WaitingHeavy           int               `json:"waiting_heavy"`
	Blocked                int               `json:"blocked"`
	LexicalDocuments       int               `json:"lexical_documents"`
	ActiveVectors          int               `json:"active_vectors"`
	HeavyCapacity          int               `json:"heavy_capacity"`
	HeavyActiveLeases      int               `json:"heavy_active_leases"`
	HeavyExecutorAvailable bool              `json:"heavy_executor_available"`
	OldestActiveSeconds    int64             `json:"oldest_active_seconds"`
	StaleClaims            int               `json:"stale_claims"`
	ActivationMismatches   int               `json:"activation_mismatches"`
	Tools                  map[string]bool   `json:"tools"`
	Runtimes               map[string]bool   `json:"runtimes"`
	Models                 map[string]bool   `json:"models"`
}

// Preserve the legacy Go reader shape while representing unobserved numeric
// values as JSON null. The additive unknown map carries the reason.
func (status PipelineOperationsStatus) MarshalJSON() ([]byte, error) {
	type readable PipelineOperationsStatus
	raw, err := json.Marshal(readable(status))
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	for field := range status.Unknown {
		if _, exists := fields[field]; exists {
			fields[field] = json.RawMessage("null")
		}
	}
	if !status.CapacityKnown {
		fields["heavy_capacity"] = json.RawMessage("null")
		fields["heavy_active_leases"] = json.RawMessage("null")
	}
	if !status.ExecutorKnown {
		fields["heavy_executor_available"] = json.RawMessage("null")
	}
	return json.Marshal(fields)
}

type PipelinePolicyStatus struct {
	Policy            PipelinePolicy    `json:"policy"`
	EmbeddingSettings EmbeddingSettings `json:"embedding_settings"`
}
type PipelinePolicyUpdate struct {
	PDFOCREnabled            *bool `json:"pdf_ocr_enabled,omitempty"`
	ImageOCREnabled          *bool `json:"image_ocr_enabled,omitempty"`
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
	clauses = append(clauses, notesSelectedPipelineRunSQL(ctx, "pipeline_runs", &args))
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
	refresh, err := s.pipelineRefreshStatus(ctx, run)
	if err != nil {
		return PipelineInspect{}, err
	}
	sources, sourceErr := s.pipelineSourceStatuses(ctx, []string{run.KnowledgePipelineRunID})
	source := sources[run.KnowledgePipelineRunID]
	if sourceErr != nil || source == nil {
		source = &PipelineSourceStatus{ObservedAt: s.currentTime(), Reason: "source_evidence_unavailable"}
	}
	return PipelineInspect{Run: redactPipelineRun(run), Stages: stages, Artifacts: artifacts, Refresh: refresh, Source: source}, nil
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

const pipelineProgressLimit = 100

func newPipelineProgress(now time.Time) *PipelineProgress {
	return &PipelineProgress{SchemaVersion: "knowledge.pipeline_progress.v1", ObservedAt: now,
		Scope: "bounded_selected_unified_queue", Limit: pipelineProgressLimit,
		Runs: []PipelineRunProgress{}, Workers: []PipelineWorkerProgress{},
		Unknown: map[string]string{
			"counts":               "no maintained selected-generation aggregate; queue window is not a total",
			"body_lexical_totals":  "no maintained selected-generation publication aggregate",
			"embedding_totals":     "no maintained eligible/current/reused aggregate; unified stage observations only",
			"client_delivery":      "native publication has no per-device receipt",
			"source_acceptance":    "no bounded exact-generation source-acceptance receipt reader",
			"protected_generation": "no exact selected-generation protection receipt; local publication is not protection",
			"useful_rate":          "worker turn counters have no stable generation denominator or rolling interval",
			"eta":                  "stable total and observed generation throughput unavailable",
		}}
}

// Bound each indexed lane before inspecting latest-generation identity.
// completed/stale history never enters this read; the window is never a total.
func pipelineProgressRunsSQL(selections ...string) string {
	selection := "true"
	if len(selections) > 0 {
		selection = selections[0]
	}
	var lanes []string
	for _, class := range []string{PipelineExecutionCoordinator, PipelineExecutionHeavy, ""} {
		for _, status := range []string{FilePipelineStatusQueued, FilePipelineStatusWaitingQuietWindow,
			FilePipelineStatusWaitingCoordinator, FilePipelineStatusWaitingHeavy, FilePipelineStatusProcessing,
			FilePipelineStatusBlockedManual, FilePipelineStatusFailed} {
			lanes = append(lanes, fmt.Sprintf(`(SELECT knowledge_pipeline_run_id FROM knowledge.pipeline_runs
			 WHERE current_execution_class='%s' AND status='%s' AND %s
			 ORDER BY priority,quiet_window_eligible_at,created_at LIMIT 100)`, class, status, selection))
		}
	}
	return `WITH candidates AS MATERIALIZED (` + strings.Join(lanes, " UNION ALL ") + `)
	 SELECT ` + pipelineRunColumns() + ` FROM knowledge.pipeline_runs r
	 WHERE knowledge_pipeline_run_id IN (SELECT knowledge_pipeline_run_id FROM candidates)
	 AND NOT EXISTS(SELECT 1 FROM knowledge.pipeline_runs newer WHERE newer.knowledge_object_id=r.knowledge_object_id AND newer.generation>r.generation)
	 ORDER BY priority,created_at,knowledge_pipeline_run_id LIMIT 100`
}

func (s *Service) GetPipelineOverallStatus(ctx context.Context) (PipelineOverallStatus, error) {
	if s == nil || s.store.db == nil {
		return PipelineOverallStatus{}, fmt.Errorf("knowledge store is not configured")
	}
	status := PipelineOverallStatus{Counts: map[string]int{}, Current: []PipelineRun{}, Progress: newPipelineProgress(s.currentTime())}
	partial := func(component string) {
		status.Progress.PartialErrors = append(status.Progress.PartialErrors, component)
	}
	args := []any{}
	selection := notesSelectedPipelineRunSQL(ctx, "pipeline_runs", &args)
	rows, err := s.store.db.QueryContext(ctx, pipelineProgressRunsSQL(selection), args...)
	if err != nil {
		partial("current_runs_unavailable")
	} else {
		for rows.Next() {
			run, scanErr := scanPipelineRun(rows)
			if scanErr != nil {
				err = scanErr
				break
			}
			status.Current = append(status.Current, redactPipelineRun(run))
		}
		if err == nil {
			err = rows.Err()
		}
		closeErr := rows.Close()
		if err == nil {
			err = closeErr
		}
		status.Progress.CurrentKnown = err == nil
		if err != nil {
			partial("current_runs_incomplete")
		}
	}
	status.Progress.WindowFull = len(status.Current) == pipelineProgressLimit
	policy, policyErr := s.GetPipelinePolicy(ctx)
	if policyErr != nil {
		partial("policy_unavailable")
	} else {
		status.Policy = policy
	}
	status.Operations, err = s.pipelineOperationsStatus(ctx, policy)
	if err != nil {
		partial("heavy_capacity_unavailable")
	}
	runIDs := make([]string, 0, len(status.Current))
	for _, run := range status.Current {
		runIDs = append(runIDs, run.KnowledgePipelineRunID)
	}
	publications, publicationErr := s.pipelineProgressPublications(ctx, runIDs)
	if publicationErr != nil {
		partial("publications_incomplete")
	}
	stages, stageErr := s.pipelineProgressStages(ctx, runIDs)
	if stageErr != nil {
		partial("stages_incomplete")
	}
	sources, sourceErr := s.pipelineSourceStatuses(ctx, runIDs)
	if sourceErr != nil {
		partial("source_evidence_unavailable")
	}
	for _, run := range status.Current {
		progress := pipelineRunProgress(run, s.currentTime())
		progress.Source = sources[run.KnowledgePipelineRunID]
		if progress.Source == nil {
			progress.Source = &PipelineSourceStatus{ObservedAt: s.currentTime(), Reason: "source_evidence_unavailable"}
		}
		progress.Publication = publications[run.KnowledgePipelineRunID]
		if progress.Publication == nil {
			partial("publication_unavailable:" + run.KnowledgePipelineRunID)
		}
		for _, stage := range stages[run.KnowledgePipelineRunID] {
			progress.Stages = append(progress.Stages, pipelineStageProgress(run, stage))
			if stage.CompletedAt != nil && (progress.LastProgressAt == nil || stage.CompletedAt.After(*progress.LastProgressAt)) {
				progress.LastProgressAt = stage.CompletedAt
			}
			if stage.StageKey == run.CurrentStageKey && stage.NextAttemptAt != nil &&
				(progress.NextEligibleAt == nil || stage.NextAttemptAt.After(*progress.NextEligibleAt)) {
				progress.NextEligibleAt = stage.NextAttemptAt
			}
		}
		status.Progress.Runs = append(status.Progress.Runs, progress)
	}
	status.Progress.Workers, err = s.pipelineProgressWorkers(ctx)
	if err != nil {
		partial("worker_receipts_unavailable")
	}
	return status, nil
}

func (s *Service) pipelineProgressPublications(ctx context.Context, ids []string) (map[string]*PipelineProgressPublication, error) {
	result := map[string]*PipelineProgressPublication{}
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT r.knowledge_pipeline_run_id,r.knowledge_object_version_id,r.source_revision,r.source_hash,
	 o.source_revision,o.source_hash,COALESCE(o.lexical_version_id,''),COALESCE(o.semantic_version_id,''),o.lexical_published_at,o.semantic_published_at,
	 EXISTS(SELECT 1 FROM knowledge.derived_artifacts a WHERE a.knowledge_pipeline_run_id=r.knowledge_pipeline_run_id
	 AND a.knowledge_object_version_id=r.knowledge_object_version_id AND a.active=true AND a.artifact_kind='consolidated_text'
	 AND octet_length(a.text_content)>0)
	 FROM knowledge.pipeline_runs r JOIN knowledge.knowledge_objects o ON o.knowledge_object_id=r.knowledge_object_id
	 WHERE r.knowledge_pipeline_run_id=ANY($1) AND o.deleted_at IS NULL
	 AND NOT EXISTS(SELECT 1 FROM knowledge.pipeline_runs newer WHERE newer.knowledge_object_id=r.knowledge_object_id AND newer.generation>r.generation)`, ids)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var run PipelineRun
		var refresh PipelineRefreshStatus
		var lexical, semantic sql.NullTime
		var body bool
		if err := rows.Scan(&id, &run.KnowledgeObjectVersionID, &run.SourceRevision, &run.SourceHash, &refresh.LatestSourceRevision, &refresh.LatestSourceHash,
			&refresh.LexicalVersionID, &refresh.SemanticVersionID, &lexical, &semantic, &body); err != nil {
			return result, err
		}
		refresh.Pending = run.SourceRevision != refresh.LatestSourceRevision || run.SourceHash != refresh.LatestSourceHash
		refresh.LexicalPublishedAt, refresh.SemanticPublishedAt = nullTimePtr(lexical), nullTimePtr(semantic)
		result[id] = pipelineProgressPublication(run, refresh, body)
	}
	return result, rows.Err()
}

func (s *Service) pipelineProgressStages(ctx context.Context, ids []string) (map[string][]PipelineStageRun, error) {
	result := map[string][]PipelineStageRun{}
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT `+pipelineStageRunColumns()+` FROM knowledge.pipeline_stage_runs
	 WHERE knowledge_pipeline_run_id=ANY($1) ORDER BY knowledge_pipeline_run_id,ordinal`, ids)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		stage, err := scanPipelineStageRun(rows)
		if err != nil {
			return result, err
		}
		result[stage.KnowledgePipelineRunID] = append(result[stage.KnowledgePipelineRunID], stage)
	}
	return result, rows.Err()
}

func pipelineRunProgress(run PipelineRun, now time.Time) PipelineRunProgress {
	out := PipelineRunProgress{PipelineRunID: run.KnowledgePipelineRunID, ObjectID: run.KnowledgeObjectID,
		SelectedVersionID: run.KnowledgeObjectVersionID, Generation: run.Generation,
		SourceRevision: run.SourceRevision, SourceHash: run.SourceHash,
		RunAgeSeconds: max(0, now.Sub(run.CreatedAt).Seconds()),
		Status:        run.Status, CurrentStage: run.CurrentStageKey, LastChangedAt: run.UpdatedAt,
		Reason: firstPipelineReason(run.LastErrorCode, run.Status),
		Stages: []PipelineStageProgress{}}
	if run.Status == FilePipelineStatusWaitingQuietWindow {
		out.NextEligibleAt = run.QuietWindowEligibleAt
	}
	return out
}

func firstPipelineReason(code, status string) string {
	if code != "" {
		return code
	}
	return status
}

func pipelineProgressPublication(run PipelineRun, refresh PipelineRefreshStatus, body bool) *PipelineProgressPublication {
	current := !refresh.Pending
	lexical := current && refresh.LexicalVersionID != "" && refresh.LexicalVersionID == run.KnowledgeObjectVersionID
	semantic := current && refresh.SemanticVersionID != "" && refresh.SemanticVersionID == run.KnowledgeObjectVersionID
	return &PipelineProgressPublication{CatalogCurrent: current, RefreshPending: refresh.Pending,
		LexicalVersionID: refresh.LexicalVersionID, SemanticVersionID: refresh.SemanticVersionID,
		LexicalCurrent: lexical, SemanticCurrent: semantic, BodyCurrent: lexical && body,
		LexicalPublishedAt: refresh.LexicalPublishedAt, SemanticPublishedAt: refresh.SemanticPublishedAt}
}

func pipelineStageProgress(run PipelineRun, stage PipelineStageRun) PipelineStageProgress {
	out := PipelineStageProgress{StageKey: stage.StageKey, Status: stage.Status,
		Attempts:       stage.AttemptCount,
		CompletedUnits: stage.ProgressCompleted, NextAttemptAt: stage.NextAttemptAt,
		Unknown: map[string]string{"reused_units": "stage progress does not distinguish saved from reused units"}}
	if stage.ProgressTotal > 0 {
		total := stage.ProgressTotal
		out.TotalUnits = &total
	} else {
		out.Unknown["total_units"] = "stage has not recorded an enumerated denominator"
	}
	var plan CompiledPipelinePlan
	if json.Unmarshal(run.PlanSnapshot, &plan) == nil {
		for _, planned := range plan.Stages {
			if planned.StageKey == stage.StageKey {
				selected := planned.Selected
				out.Selected = &selected
				out.SkipReason = planned.SkipReason
				break
			}
		}
	}
	var usage struct {
		Admission *workers.ResourceTurnAdmission `json:"admission"`
	}
	if json.Unmarshal(stage.ResourceUsage, &usage) == nil {
		out.Admission = usage.Admission
	}
	return out
}

func (s *Service) pipelineProgressWorkers(ctx context.Context) ([]PipelineWorkerProgress, error) {
	rows, err := s.store.db.QueryContext(ctx, `SELECT i.worker_key,COALESCE(i.current_run_id,''),COALESCE(r.worker_run_id,''),COALESCE(r.run_status,''),r.started_at,r.finished_at,i.next_run_after,r.counters_json,r.result_summary_json,
	 (SELECT jsonb_agg(recent.started_at ORDER BY recent.started_at DESC) FROM
	 (SELECT started_at FROM workers.worker_runs WHERE worker_instance_id=i.worker_instance_id AND started_at IS NOT NULL ORDER BY started_at DESC LIMIT 3) recent)
	 FROM workers.worker_instances i LEFT JOIN workers.worker_runs r ON r.worker_run_id=i.last_run_id
	 WHERE i.worker_key IN ('main.knowledge_indexer','main.knowledge_heavy') ORDER BY i.worker_key LIMIT 2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []PipelineWorkerProgress{}
	for rows.Next() {
		var item PipelineWorkerProgress
		var started, finished, next sql.NullTime
		var counters, summary, recent []byte
		if err := rows.Scan(&item.WorkerKey, &item.CurrentRunID, &item.RunID, &item.Status, &started, &finished, &next, &counters, &summary, &recent); err != nil {
			return result, err
		}
		item.StartedAt, item.FinishedAt, item.NextRunAfter = nullTimePtr(started), nullTimePtr(finished), nullTimePtr(next)
		if len(counters) > 0 && json.Unmarshal(counters, &item.Counters) != nil {
			return result, fmt.Errorf("invalid worker counters")
		}
		if err := decodePipelineWorkerSummary(&item, summary); err != nil {
			return result, err
		}
		item.CounterScope = "last_finished_turn"
		var starts []time.Time
		if len(recent) > 0 && json.Unmarshal(recent, &starts) != nil {
			return result, fmt.Errorf("invalid worker run history")
		}
		for index := 1; index < len(starts); index++ {
			item.RecentIntervalsSeconds = append(item.RecentIntervalsSeconds, max(0, starts[index-1].Sub(starts[index]).Seconds()))
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func decodePipelineWorkerSummary(item *PipelineWorkerProgress, summary []byte) error {
	var decoded struct {
		Stages            []HeavyExecutorRunResult `json:"stages"`
		MoreWork          *bool                    `json:"more_work"`
		AdmissionTimedOut *bool                    `json:"admission_timed_out"`
		AdmissionWarning  *AdmissionPhaseWarning   `json:"admission_warning"`
	}
	if len(summary) > 0 && json.Unmarshal(summary, &decoded) != nil {
		return fmt.Errorf("invalid worker summary")
	}
	item.Turns, item.MoreWork = decoded.Stages, decoded.MoreWork
	item.AdmissionTimedOut = decoded.AdmissionTimedOut
	item.AdmissionWarning = NormalizeAdmissionPhaseWarning(decoded.AdmissionWarning)
	return nil
}

func (s *Service) pipelineOperationsStatus(ctx context.Context, _ PipelinePolicyStatus) (PipelineOperationsStatus, error) {
	status := PipelineOperationsStatus{Tools: map[string]bool{}, Runtimes: map[string]bool{}, Models: map[string]bool{},
		Unknown: map[string]string{
			"waiting_heavy": "no maintained selected-generation aggregate", "blocked": "no maintained selected-generation aggregate",
			"lexical_documents": "full publication count belongs to explicit inspection", "active_vectors": "full vector count belongs to explicit inspection",
			"oldest_active_seconds": "bounded queue window cannot establish global oldest", "stale_claims": "full claim audit not requested",
			"activation_mismatches": "integrity inspection not requested", "tools": "availability not probed by progress read",
			"runtimes": "availability not probed by progress read", "models": "availability not probed by progress read"}}
	if err := s.store.db.QueryRowContext(ctx, `SELECT COALESCE(max(c.capacity),0)::int,count(l.worker_resource_lease_id)::int FROM workers.resource_capacities c LEFT JOIN workers.resource_leases l ON l.resource_key=c.resource_key AND l.status='active' AND l.expires_at>now() WHERE c.resource_key='knowledge_heavy'`).Scan(&status.HeavyCapacity, &status.HeavyActiveLeases); err != nil {
		return status, err
	}
	status.CapacityKnown = true
	if err := s.store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workers.worker_instances WHERE worker_key='main.knowledge_heavy' AND enabled=true AND paused=false AND lifecycle_status NOT IN ('disabled','retired'))`).Scan(&status.HeavyExecutorAvailable); err != nil {
		return status, err
	}
	status.ExecutorKnown = true
	return status, nil
}

// InspectPipelineIntegrity is an explicit full audit, never an ordinary refresh.
func (s *Service) InspectPipelineIntegrity(ctx context.Context) (PipelineOperationsStatus, error) {
	if s == nil || s.store.db == nil {
		return PipelineOperationsStatus{}, fmt.Errorf("knowledge store is not configured")
	}
	status, err := s.pipelineOperationsStatus(ctx, PipelinePolicyStatus{})
	if err != nil {
		return status, err
	}
	if err = s.store.db.QueryRowContext(ctx, `SELECT count(*)::int FROM search.search_documents WHERE source_kind=$1`, KnowledgeSearchSourceKind).Scan(&status.LexicalDocuments); err != nil {
		return status, err
	}
	if err = s.store.db.QueryRowContext(ctx, `SELECT count(*)::int FROM knowledge.chunk_embeddings WHERE active=true`).Scan(&status.ActiveVectors); err != nil {
		return status, err
	}
	if err = s.store.db.QueryRowContext(ctx, `SELECT ((SELECT count(*) FROM knowledge.derived_artifacts a JOIN knowledge.pipeline_runs r ON r.knowledge_pipeline_run_id=a.knowledge_pipeline_run_id WHERE a.active=true AND (a.generation<>r.generation OR r.status IN ('stale','cancelled'))) + (SELECT count(*) FROM knowledge.chunk_embeddings e LEFT JOIN knowledge.knowledge_chunks c ON c.knowledge_chunk_id=e.knowledge_chunk_id WHERE e.active=true AND (c.knowledge_chunk_id IS NULL OR e.knowledge_object_version_id IS DISTINCT FROM c.knowledge_object_version_id)))::int`).Scan(&status.ActivationMismatches); err != nil {
		return status, err
	}
	delete(status.Unknown, "lexical_documents")
	delete(status.Unknown, "active_vectors")
	delete(status.Unknown, "activation_mismatches")
	at := s.currentTime()
	status.IntegrityCheckedAt = &at
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
	if value, ok := metadata["image_ocr_enabled"].(bool); ok {
		policy.ImageOCREnabled = value
	}
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
	if input.ImageOCREnabled != nil {
		policy.ImageOCREnabled = *input.ImageOCREnabled
	}
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
	metadata["image_ocr_enabled"] = policy.ImageOCREnabled
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
	oldPlan, err := recordedEnrichmentPlanTx(ctx, tx, object, oldPolicy)
	if err != nil {
		return false, err
	}
	newPlan, err := recordedEnrichmentPlanTx(ctx, tx, object, newPolicy)
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
	oldPlan, err := recordedEnrichmentPlanTx(ctx, tx, object, oldPolicy)
	if err != nil {
		return false, err
	}
	newPlan, err := recordedEnrichmentPlanTx(ctx, tx, object, newPolicy)
	if err != nil {
		return false, err
	}
	if !oldPolicy.PDFOCREnabled && newPolicy.PDFOCREnabled {
		if compiledPlanContainsSelectedStage(newPlan, FilePipelineStagePDFOCR) {
			return true, nil
		}
	}
	if !oldPolicy.ImageOCREnabled && newPolicy.ImageOCREnabled && compiledPlanContainsSelectedStage(newPlan, FilePipelineStageImageOCR) {
		return true, nil
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
	if oldPolicy.ImageOCREnabled && !newPolicy.ImageOCREnabled && compiledPlanContainsSelectedStage(oldPlan, FilePipelineStageImageOCR) {
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
	if inspection.Run.SourceRevision != "" {
		current, e := s.store.GetKnowledgeObject(ctx, inspection.Run.KnowledgeObjectID)
		if e != nil {
			return PipelineRun{}, e
		}
		if current.SourceHash != inspection.Run.SourceHash || current.SourceRevision != inspection.Run.SourceRevision {
			return PipelineRun{}, fmt.Errorf("%w: retry source version is no longer current", ErrConflict)
		}
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
	var intentPlan CompiledPipelinePlan
	if json.Unmarshal(inspection.Run.PlanSnapshot, &intentPlan) == nil && intentPlan.Enrichment != nil {
		if _, e := compileEnrichmentPlan(object, policyStatus.Policy, *intentPlan.Enrichment); e != nil {
			return PipelineRun{}, e
		}
		if input.StageKey == "" {
			input.StageKey = FilePipelineStageEmbedding
			if intentPlan.Enrichment.Requested.Vision {
				input.StageKey = FilePipelineStageImageDescription
			}
			if intentPlan.Enrichment.Requested.OCR {
				input.StageKey = FilePipelineStageImageOCR
				if intentPlan.FileFamily == "pdf" {
					input.StageKey = FilePipelineStagePDFPageAnalysis
				}
			}
		}
		if input.StageKey == FilePipelineStagePDFOCR {
			input.StageKey = FilePipelineStagePDFPageAnalysis
		}
	}
	if input.StageKey == "" {
		return s.EnsurePipelineRun(ctx, object, policyStatus.Policy, true, input.Priority)
	}
	inspection.Artifacts, err = s.store.ListDerivedArtifacts(ctx, inspection.Run.KnowledgePipelineRunID, false)
	if err != nil {
		return PipelineRun{}, err
	}
	retry := &pipelineRetrySpec{Source: inspection, TargetStage: input.StageKey, ReuseStages: map[string]bool{}}
	if intentPlan.Enrichment != nil {
		for _, compiled := range intentPlan.Stages {
			if !enrichmentRetainedStage(*intentPlan.Enrichment, compiled.StageKey) {
				continue
			}
			if input.StageKey == compiled.StageKey {
				return PipelineRun{}, fmt.Errorf("%w: stage was retained from earlier work; request it explicitly with notes enrich", ErrInvalid)
			}
			var previous PipelineStageRun
			for _, stage := range inspection.Stages {
				if stage.StageKey == compiled.StageKey {
					previous = stage
				}
			}
			if !isReusableUpstreamStageStatus(previous.Status) || !reusableStageMetadataMatches(previous, compiled) || (previous.Status != PipelineStageStatusSkippedNotApplicable && !hasReusableStageArtifact(inspection.Artifacts, inspection.Run, previous, compiled.OutputArtifactKinds)) {
				return PipelineRun{}, fmt.Errorf("%w: retained stage %s is no longer reusable", ErrConflict, compiled.StageKey)
			}
			retry.ReuseStages[compiled.StageKey] = true
		}
	}
	run, _, err := s.ensurePipelineRunWithRetry(ctx, object, policyStatus.Policy, true, input.Priority, retry)
	return run, err
}
