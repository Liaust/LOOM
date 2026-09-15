package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/storagecatalog"
)

const DefaultPipelinePriority = 100

func (s *Service) EnsurePipelineRun(ctx context.Context, object KnowledgeObject, policy PipelinePolicy, force bool, priority int) (PipelineRun, error) {
	run, _, err := s.ensurePipelineRun(ctx, object, policy, force, priority)
	return run, err
}

func (s *Service) ensurePipelineRun(ctx context.Context, object KnowledgeObject, policy PipelinePolicy, force bool, priority int) (PipelineRun, bool, error) {
	return s.ensurePipelineRunWithRetry(ctx, object, policy, force, priority, nil)
}

type pipelineRetrySpec struct {
	Source      PipelineInspect
	TargetStage string
}

func (s *Service) ensurePipelineRunWithRetry(ctx context.Context, object KnowledgeObject, policy PipelinePolicy, force bool, priority int, retry *pipelineRetrySpec) (PipelineRun, bool, error) {
	if s == nil || s.store.db == nil {
		return PipelineRun{}, false, fmt.Errorf("knowledge store is not configured")
	}
	if strings.TrimSpace(object.KnowledgeObjectID) == "" {
		return PipelineRun{}, false, fmt.Errorf("%w: knowledge_object_id is required", ErrInvalid)
	}
	if priority <= 0 {
		priority = DefaultPipelinePriority
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return PipelineRun{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	run, created, err := s.ensurePipelineRunTx(ctx, tx, object.KnowledgeObjectID, policy, force, priority, retry)
	if err != nil {
		return PipelineRun{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return PipelineRun{}, false, err
	}
	return run, created, nil
}

func (s *Service) ensurePipelineRunTx(ctx context.Context, tx *sql.Tx, objectID string, policy PipelinePolicy, force bool, priority int, retry *pipelineRetrySpec) (PipelineRun, bool, error) {
	object, err := lockCurrentPipelineObjectTx(ctx, tx, objectID)
	if err != nil {
		return PipelineRun{}, false, err
	}
	plan, err := CompilePipelinePlan(object, policy)
	if err != nil {
		return PipelineRun{}, false, err
	}
	snapshot, err := plan.Snapshot()
	if err != nil {
		return PipelineRun{}, false, err
	}
	return s.ensureLockedPipelineRunTx(ctx, tx, object, plan, snapshot, force, priority, retry)
}

func (s *Service) ensureLockedPipelineRunTx(ctx context.Context, tx *sql.Tx, object KnowledgeObject, plan CompiledPipelinePlan, snapshot json.RawMessage, force bool, priority int, retry *pipelineRetrySpec) (PipelineRun, bool, error) {
	retryIndex := -1
	if retry != nil {
		if retry.Source.Run.KnowledgeObjectID != object.KnowledgeObjectID || !isRetryablePipelineRunStatus(retry.Source.Run.Status) {
			return PipelineRun{}, false, fmt.Errorf("%w: pipeline run is not retryable", ErrInvalid)
		}
		for index, stage := range plan.Stages {
			if stage.StageKey == retry.TargetStage {
				if !stage.Selected {
					return PipelineRun{}, false, fmt.Errorf("%w: pipeline stage %q is disabled by policy", ErrInvalid, retry.TargetStage)
				}
				retryIndex = index
				break
			}
		}
		if retryIndex < 0 {
			return PipelineRun{}, false, fmt.Errorf("%w: pipeline stage %q was not found", ErrInvalid, retry.TargetStage)
		}
	}

	version, err := s.getOrCreateKnowledgeObjectVersionTx(ctx, tx, object, json.RawMessage(`{"schema_version":"knowledge.pipeline_source_version.v1"}`))
	if err != nil {
		return PipelineRun{}, false, err
	}
	if retry != nil {
		locked, validateErr := validateRetryReuseTx(ctx, tx, object, version, plan, snapshot, retry.Source.Run.KnowledgePipelineRunID, retryIndex)
		if validateErr != nil {
			return PipelineRun{}, false, validateErr
		}
		retry.Source = locked
	}
	if !force {
		existing, found, findErr := findReusablePipelineRunTx(ctx, tx, version.KnowledgeObjectVersionID, plan.DefinitionKey, plan.DefinitionVersion)
		if findErr != nil {
			return PipelineRun{}, false, findErr
		}
		if found && pipelinePlanSnapshotsEqual(existing.PlanSnapshot, snapshot) {
			return existing, false, nil
		}
	}
	now := s.currentTime()
	if err = supersedePipelineRunsTx(ctx, tx, object.KnowledgeObjectID, now); err != nil {
		return PipelineRun{}, false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE knowledge.pipeline_statuses SET status='stale', claimed_by_worker_run_id=NULL, claim_expires_at=NULL, updated_at=$2
		WHERE knowledge_object_id=$1 AND knowledge_object_version_id IS NULL
		  AND pipeline_key IN ($3,$4) AND status IN ('queued','processing','failed')`, object.KnowledgeObjectID, now, KnowledgeObjectPipelineNotesFileExtraction, KnowledgeObjectPipelineMarkdownText); err != nil {
		return PipelineRun{}, false, err
	}
	var generation int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(max(generation),0)+1 FROM knowledge.pipeline_runs WHERE knowledge_object_id=$1`, object.KnowledgeObjectID).Scan(&generation); err != nil {
		return PipelineRun{}, false, err
	}
	run := PipelineRun{
		KnowledgePipelineRunID: ids.NewKnowledgePipelineRunID(), KnowledgeObjectID: object.KnowledgeObjectID,
		KnowledgeObjectVersionID: version.KnowledgeObjectVersionID, PipelineDefinitionKey: plan.DefinitionKey,
		PipelineDefinitionVersion: plan.DefinitionVersion, Generation: generation, Status: FilePipelineStatusQueued,
		Priority: priority, SourceRevision: object.SourceRevision, SourceHash: object.SourceHash,
		PlanSnapshot: snapshot, ResourceTotals: json.RawMessage(`{}`), Metadata: json.RawMessage(`{"schema_version":"knowledge.pipeline_run.metadata.v1"}`),
		CreatedAt: now, UpdatedAt: now,
	}
	if eligibleAt, ok := pipelineQuietWindowEligibleAt(object, plan, now); ok {
		run.QuietWindowEligibleAt = &eligibleAt
	}
	startIndex := 0
	if retryIndex >= 0 {
		startIndex = retryIndex
	}
	if len(plan.Stages) > startIndex {
		run.CurrentStageKey = plan.Stages[startIndex].StageKey
		run.CurrentExecutionClass = plan.Stages[startIndex].ExecutionClass
	}
	quietNow := retryIndex >= 0 && plan.Stages[startIndex].QuietWindow && run.QuietWindowEligibleAt != nil && run.QuietWindowEligibleAt.After(now)
	run.Status = waitingPipelineStatus(run.CurrentExecutionClass, quietNow)
	if err := ValidatePipelineRun(run); err != nil {
		return PipelineRun{}, false, err
	}
	run, err = insertPipelineRunTx(ctx, tx, run)
	if err != nil {
		return PipelineRun{}, false, err
	}
	newStageByKey := make(map[string]PipelineStageRun, len(plan.Stages))
	oldStageByKey := make(map[string]PipelineStageRun, len(plan.Stages))
	if retry != nil {
		for _, oldStage := range retry.Source.Stages {
			oldStageByKey[oldStage.StageKey] = oldStage
		}
	}
	for index, compiled := range plan.Stages {
		status := PipelineStageStatusWaitingDependency
		var reusedOld PipelineStageRun
		reuseUpstream := false
		if !compiled.Selected {
			status = PipelineStageStatusSkippedByPolicy
		}
		if index == startIndex {
			status = PipelineStageStatusReady
		} else if retryIndex >= 0 && index < retryIndex {
			oldStage, found := oldStageByKey[compiled.StageKey]
			if !found || !isReusableUpstreamStageStatus(oldStage.Status) {
				return PipelineRun{}, false, fmt.Errorf("%w: upstream stage %q is not reusable", ErrInvalid, compiled.StageKey)
			}
			status = oldStage.Status
			reusedOld = oldStage
			reuseUpstream = true
		}
		dependencies, _ := json.Marshal(compiled.Dependencies)
		if len(compiled.Dependencies) == 0 {
			dependencies = []byte(`[]`)
		}
		stage := PipelineStageRun{
			KnowledgePipelineStageRunID: ids.NewKnowledgePipelineStageRunID(), KnowledgePipelineRunID: run.KnowledgePipelineRunID,
			StageKey: compiled.StageKey, StageContractVersion: compiled.ContractVersion, Ordinal: compiled.Ordinal,
			DependencySnapshot: dependencies, ExecutionClass: compiled.ExecutionClass, Status: status,
			ResourceRequest: json.RawMessage(`{}`), ResourceUsage: json.RawMessage(`{}`), Warnings: json.RawMessage(`[]`),
			Error: json.RawMessage(`{}`), Metadata: pipelineStageMetadata(compiled), CreatedAt: now, UpdatedAt: now,
		}
		if reuseUpstream {
			stage.InputHash = reusedOld.InputHash
			stage.OutputArtifactCount = reusedOld.OutputArtifactCount
			stage.ProgressCompleted = reusedOld.ProgressCompleted
			stage.ProgressTotal = reusedOld.ProgressTotal
			stage.ResourceUsage = reusedOld.ResourceUsage
			stage.Warnings = reusedOld.Warnings
			stage.CompletedAt = reusedOld.CompletedAt
		}
		if err := ValidatePipelineStageRun(stage); err != nil {
			return PipelineRun{}, false, err
		}
		if _, err = insertPipelineStageRunTx(ctx, tx, stage); err != nil {
			return PipelineRun{}, false, err
		}
		newStageByKey[compiled.StageKey] = stage
	}
	if retryIndex >= 0 {
		if err = cloneRetryUpstreamArtifactsTx(ctx, tx, retry.Source, run, plan.Stages[retryIndex].Ordinal, newStageByKey); err != nil {
			return PipelineRun{}, false, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE knowledge.knowledge_objects
		SET processing_state='stale', pipeline_key=$2, pipeline_version=$3, updated_at=$4
		WHERE knowledge_object_id=$1`, object.KnowledgeObjectID, run.PipelineDefinitionKey, run.PipelineDefinitionVersion, now); err != nil {
		return PipelineRun{}, false, err
	}
	return run, true, nil
}

// lockCurrentPipelineObjectTx is the sole run-creation entry to current object
// state. It intentionally takes trajectory locks before the object lock so
// policy reconciliation, ordinary reconciliation, retry, and worker
// publication all share run -> stage/unit -> object -> version/artifact order.
func lockCurrentPipelineObjectTx(ctx context.Context, tx *sql.Tx, objectID string) (KnowledgeObject, error) {
	if err := storagecatalog.LockWorkspaceCustodyReaderTx(ctx, tx); err != nil {
		return KnowledgeObject{}, err
	}
	if err := lockPipelineTrajectoryRowsTx(ctx, tx, objectID); err != nil {
		return KnowledgeObject{}, err
	}
	object, err := scanKnowledgeObject(tx.QueryRowContext(ctx, `SELECT `+knowledgeObjectColumns()+`
		FROM knowledge.knowledge_objects
		WHERE knowledge_object_id=$1 AND deleted_at IS NULL FOR UPDATE`, objectID))
	if err != nil {
		return KnowledgeObject{}, err
	}
	if err = ValidateKnowledgeObject(object); err != nil {
		return KnowledgeObject{}, err
	}
	if err := requireNotesCustodyWriteTx(ctx, tx, object); err != nil {
		return KnowledgeObject{}, err
	}
	return object, nil
}

func isReusableUpstreamStageStatus(status string) bool {
	switch status {
	case PipelineStageStatusComplete, PipelineStageStatusCompleteWithWarning,
		PipelineStageStatusSkippedNotApplicable, PipelineStageStatusSkippedByPolicy:
		return true
	default:
		return false
	}
}

func isRetryablePipelineRunStatus(status string) bool {
	switch status {
	case FilePipelineStatusComplete, FilePipelineStatusCompleteWithWarning,
		FilePipelineStatusFailed, FilePipelineStatusBlockedManual:
		return true
	default:
		return false
	}
}

func supersedePipelineRunsTx(ctx context.Context, tx *sql.Tx, objectID string, now time.Time) error {
	if err := lockPipelineTrajectoryRowsTx(ctx, tx, objectID); err != nil {
		return err
	}
	artifactRows, err := tx.QueryContext(ctx, `SELECT a.knowledge_derived_artifact_id
		FROM knowledge.derived_artifacts a
		JOIN knowledge.pipeline_runs r ON r.knowledge_pipeline_run_id=a.knowledge_pipeline_run_id
		WHERE r.knowledge_object_id=$1
		  AND r.status NOT IN ('stale','cancelled')
		ORDER BY a.knowledge_derived_artifact_id FOR UPDATE OF a`, objectID)
	if err != nil {
		return err
	}
	for artifactRows.Next() {
		var ignored string
		if err = artifactRows.Scan(&ignored); err != nil {
			artifactRows.Close()
			return err
		}
	}
	if err = artifactRows.Err(); err != nil {
		artifactRows.Close()
		return err
	}
	if err = artifactRows.Close(); err != nil {
		return err
	}
	// Ownership and normalized outputs are cleared from the leaves upward before
	// the parent run becomes stale, preserving every migration 00060 claim
	// invariant at each statement boundary.
	predicate := `r.knowledge_object_id=$1 AND r.status NOT IN ('stale','cancelled')`
	if _, err := tx.ExecContext(ctx, `UPDATE knowledge.pipeline_stage_units u
		SET status='stale', claimed_by_worker_run_id='', claim_generation=0,
		    completed_at=COALESCE(u.completed_at,$2), updated_at=$2
		FROM knowledge.pipeline_stage_runs s, knowledge.pipeline_runs r
		WHERE u.knowledge_pipeline_stage_run_id=s.knowledge_pipeline_stage_run_id
		  AND s.knowledge_pipeline_run_id=r.knowledge_pipeline_run_id AND `+predicate+`
		  AND u.status NOT IN ('complete','complete_with_warnings','skipped_not_applicable','skipped_by_policy','stale','cancelled')`, objectID, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE knowledge.pipeline_stage_runs s
		SET status='stale', claimed_by_worker_run_id='', claim_generation=0,
		    next_attempt_at=NULL, completed_at=COALESCE(s.completed_at,$2), updated_at=$2
		FROM knowledge.pipeline_runs r
		WHERE s.knowledge_pipeline_run_id=r.knowledge_pipeline_run_id AND `+predicate+`
		  AND s.status NOT IN ('complete','complete_with_warnings','skipped_not_applicable','skipped_by_policy','stale','cancelled')`, objectID, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE knowledge.derived_artifacts a
		SET active=false,state='stale',deactivated_at=COALESCE(a.deactivated_at,$2)
		FROM knowledge.pipeline_runs r
		WHERE a.knowledge_pipeline_run_id=r.knowledge_pipeline_run_id AND `+predicate+`
		  AND a.active=true`, objectID, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE knowledge.pipeline_runs r
		SET status='stale', claimed_by_worker_run_id='', claim_expires_at=NULL,
		    completed_at=COALESCE(completed_at,$2), updated_at=$2
		WHERE `+predicate, objectID, now); err != nil {
		return err
	}
	return nil
}

func lockPipelineTrajectoryRowsTx(ctx context.Context, tx *sql.Tx, objectID string) error {
	lockQueries := []string{
		`SELECT r.knowledge_pipeline_run_id FROM knowledge.pipeline_runs r
		 WHERE r.knowledge_object_id=$1
		   AND r.status NOT IN ('stale','cancelled')
		 ORDER BY r.knowledge_pipeline_run_id FOR UPDATE OF r`,
		`SELECT s.knowledge_pipeline_stage_run_id
		 FROM knowledge.pipeline_stage_runs s
		 JOIN knowledge.pipeline_runs r ON r.knowledge_pipeline_run_id=s.knowledge_pipeline_run_id
		 WHERE r.knowledge_object_id=$1
		   AND r.status NOT IN ('stale','cancelled')
		 ORDER BY s.knowledge_pipeline_stage_run_id FOR UPDATE OF s`,
		`SELECT u.knowledge_pipeline_stage_unit_id
		 FROM knowledge.pipeline_stage_units u
		 JOIN knowledge.pipeline_stage_runs s ON s.knowledge_pipeline_stage_run_id=u.knowledge_pipeline_stage_run_id
		 JOIN knowledge.pipeline_runs r ON r.knowledge_pipeline_run_id=s.knowledge_pipeline_run_id
		 WHERE r.knowledge_object_id=$1
		   AND r.status NOT IN ('stale','cancelled')
		 ORDER BY u.knowledge_pipeline_stage_unit_id FOR UPDATE OF u`,
	}
	for _, query := range lockQueries {
		rows, err := tx.QueryContext(ctx, query, objectID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var ignored string
			if err = rows.Scan(&ignored); err != nil {
				rows.Close()
				return err
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

func validateRetryReuseTx(ctx context.Context, tx *sql.Tx, object KnowledgeObject, version KnowledgeObjectVersion, plan CompiledPipelinePlan, snapshot json.RawMessage, sourceRunID string, retryIndex int) (PipelineInspect, error) {
	source, err := scanPipelineRun(tx.QueryRowContext(ctx, `SELECT `+pipelineRunColumns()+` FROM knowledge.pipeline_runs WHERE knowledge_pipeline_run_id=$1 FOR UPDATE`, sourceRunID))
	if err != nil {
		return PipelineInspect{}, err
	}
	if source.KnowledgeObjectID != object.KnowledgeObjectID || !isRetryablePipelineRunStatus(source.Status) {
		return PipelineInspect{}, fmt.Errorf("%w: pipeline run is not retryable", ErrInvalid)
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+pipelineStageRunColumns()+` FROM knowledge.pipeline_stage_runs WHERE knowledge_pipeline_run_id=$1 ORDER BY ordinal FOR UPDATE`, sourceRunID)
	if err != nil {
		return PipelineInspect{}, err
	}
	var stages []PipelineStageRun
	for rows.Next() {
		stage, scanErr := scanPipelineStageRun(rows)
		if scanErr != nil {
			rows.Close()
			return PipelineInspect{}, scanErr
		}
		stages = append(stages, stage)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return PipelineInspect{}, err
	}
	if err = rows.Close(); err != nil {
		return PipelineInspect{}, err
	}
	artifactRows, err := tx.QueryContext(ctx, `SELECT `+derivedArtifactColumns()+` FROM knowledge.derived_artifacts WHERE knowledge_pipeline_run_id=$1 FOR UPDATE`, sourceRunID)
	if err != nil {
		return PipelineInspect{}, err
	}
	var artifacts []DerivedArtifact
	for artifactRows.Next() {
		artifact, scanErr := scanDerivedArtifact(artifactRows)
		if scanErr != nil {
			artifactRows.Close()
			return PipelineInspect{}, scanErr
		}
		artifacts = append(artifacts, artifact)
	}
	if err = artifactRows.Err(); err != nil {
		artifactRows.Close()
		return PipelineInspect{}, err
	}
	if err = artifactRows.Close(); err != nil {
		return PipelineInspect{}, err
	}
	inspect := PipelineInspect{Run: source, Stages: stages, Artifacts: artifacts}
	if retryIndex == 0 {
		return inspect, nil
	}
	if source.KnowledgeObjectVersionID != version.KnowledgeObjectVersionID || source.SourceRevision != object.SourceRevision || source.SourceHash != object.SourceHash {
		return PipelineInspect{}, fmt.Errorf("%w: retry source revision is not current", ErrInvalid)
	}
	if source.PipelineDefinitionKey != plan.DefinitionKey || source.PipelineDefinitionVersion != plan.DefinitionVersion || !pipelinePlanSnapshotsEqual(source.PlanSnapshot, snapshot) {
		return PipelineInspect{}, fmt.Errorf("%w: retry source plan is incompatible with current policy or definition", ErrInvalid)
	}
	byKey := make(map[string]PipelineStageRun, len(stages))
	for _, stage := range stages {
		byKey[stage.StageKey] = stage
	}
	for index := 0; index < retryIndex; index++ {
		compiled := plan.Stages[index]
		stage, found := byKey[compiled.StageKey]
		if !found || !isReusableUpstreamStageStatus(stage.Status) {
			return PipelineInspect{}, fmt.Errorf("%w: upstream stage %q is not reusable", ErrInvalid, compiled.StageKey)
		}
		dependencies, _ := json.Marshal(compiled.Dependencies)
		if len(compiled.Dependencies) == 0 {
			dependencies = []byte(`[]`)
		}
		if stage.Ordinal != compiled.Ordinal || stage.StageContractVersion != compiled.ContractVersion || stage.ExecutionClass != compiled.ExecutionClass || !pipelinePlanSnapshotsEqual(stage.DependencySnapshot, dependencies) || !pipelinePlanSnapshotsEqual(stage.Metadata, pipelineStageMetadata(compiled)) {
			return PipelineInspect{}, fmt.Errorf("%w: upstream stage %q contract is incompatible", ErrInvalid, compiled.StageKey)
		}
		if !compiled.Selected && stage.Status != PipelineStageStatusSkippedByPolicy {
			return PipelineInspect{}, fmt.Errorf("%w: upstream stage %q policy state is incompatible", ErrInvalid, compiled.StageKey)
		}
		if stage.Status == PipelineStageStatusSkippedByPolicy && compiled.Selected {
			return PipelineInspect{}, fmt.Errorf("%w: upstream stage %q is now enabled", ErrInvalid, compiled.StageKey)
		}
		if compiled.Selected && (stage.Status == PipelineStageStatusComplete || stage.Status == PipelineStageStatusCompleteWithWarning) && len(compiled.OutputArtifactKinds) > 0 && !hasReusableStageArtifact(artifacts, source, stage, compiled.OutputArtifactKinds) {
			return PipelineInspect{}, fmt.Errorf("%w: upstream stage %q has no complete active output", ErrInvalid, compiled.StageKey)
		}
	}
	return inspect, nil
}

func hasReusableStageArtifact(artifacts []DerivedArtifact, run PipelineRun, stage PipelineStageRun, outputKinds []string) bool {
	for _, artifact := range artifacts {
		if !artifact.Active || artifact.KnowledgePipelineStageRunID != stage.KnowledgePipelineStageRunID || artifact.KnowledgeObjectVersionID != run.KnowledgeObjectVersionID || artifact.Generation != run.Generation {
			continue
		}
		for _, kind := range outputKinds {
			if artifact.ArtifactKind == kind {
				return true
			}
		}
	}
	return false
}

func cloneRetryUpstreamArtifactsTx(ctx context.Context, tx *sql.Tx, source PipelineInspect, run PipelineRun, targetOrdinal int, newStages map[string]PipelineStageRun) error {
	for _, artifact := range source.Artifacts {
		oldStage, found := stageByID(source.Stages, artifact.KnowledgePipelineStageRunID)
		if !found || oldStage.Ordinal >= targetOrdinal || !artifact.Active {
			continue
		}
		newStage, found := newStages[oldStage.StageKey]
		if !found {
			return fmt.Errorf("%w: retry stage mapping for %q is missing", ErrInvalid, oldStage.StageKey)
		}
		artifact.KnowledgeDerivedArtifactID = ids.NewKnowledgeDerivedArtifactID()
		artifact.KnowledgePipelineRunID = run.KnowledgePipelineRunID
		artifact.KnowledgePipelineStageRunID = newStage.KnowledgePipelineStageRunID
		artifact.Generation = run.Generation
		artifact.State = ArtifactStateReusable
		artifact.Active = false
		artifact.ActivatedAt = nil
		artifact.DeactivatedAt = nil
		artifact.CreatedAt = run.CreatedAt
		created, err := createDerivedArtifactTx(ctx, tx, artifact)
		if err != nil {
			return err
		}
		if _, err = activateDerivedArtifactTx(ctx, tx, created.KnowledgeDerivedArtifactID, run.Generation); err != nil {
			return err
		}
	}
	return nil
}

func stageByID(stages []PipelineStageRun, id string) (PipelineStageRun, bool) {
	for _, stage := range stages {
		if stage.KnowledgePipelineStageRunID == id {
			return stage, true
		}
	}
	return PipelineStageRun{}, false
}

func pipelinePlanSnapshotsEqual(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func pipelineQuietWindowEligibleAt(object KnowledgeObject, plan CompiledPipelinePlan, now time.Time) (time.Time, bool) {
	hasQuietStage := false
	for _, stage := range plan.Stages {
		if stage.Selected && stage.QuietWindow {
			hasQuietStage = true
			break
		}
	}
	if !hasQuietStage {
		return time.Time{}, false
	}
	changedAt := object.UpdatedAt
	if changedAt.IsZero() {
		changedAt = object.LastSeenAt
	}
	if changedAt.IsZero() {
		changedAt = now
	}
	return changedAt.Add(time.Duration(DefaultEmbeddingQuietWindowSec) * time.Second), true
}

func waitingPipelineStatus(executionClass string, quiet bool) string {
	if quiet {
		return FilePipelineStatusWaitingQuietWindow
	}
	if executionClass == PipelineExecutionHeavy {
		return FilePipelineStatusWaitingHeavy
	}
	return FilePipelineStatusWaitingCoordinator
}

func pipelineStageMetadata(stage CompiledPipelineStage) json.RawMessage {
	payload, err := json.Marshal(map[string]any{"schema_version": "knowledge.pipeline_stage.metadata.v1", "selected": stage.Selected, "skip_reason": stage.SkipReason, "implementation_key": stage.ImplementationKey, "input_artifact_kinds": stage.InputArtifactKinds, "output_artifact_kinds": stage.OutputArtifactKinds, "quiet_window": stage.QuietWindow, "required": stage.Required})
	if err != nil {
		return emptyJSONObject
	}
	return payload
}

func (s *Service) RefreshPipelineForObject(ctx context.Context, objectRef string, force bool, priority int) (PipelineRun, error) {
	object, err := s.store.GetKnowledgeObject(ctx, strings.TrimSpace(objectRef))
	if err != nil {
		return PipelineRun{}, err
	}
	policy, err := s.GetPipelinePolicy(ctx)
	if err != nil {
		return PipelineRun{}, err
	}
	return s.EnsurePipelineRun(ctx, object, policy.Policy, force, priority)
}
