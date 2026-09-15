package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type HeavyStageObservation struct {
	EnforcedPolicy json.RawMessage `json:"enforced_policy"`
	Observed       json.RawMessage `json:"observed"`
}

const heavyStageMoreUnits = "__pipeline_more_units__"

type HeavyStageHandler interface {
	Execute(context.Context, PipelineWorkItem, HeavyResourcePolicy) (HeavyStageObservation, []string, error)
}

type HeavyStageHandlerFunc func(context.Context, PipelineWorkItem, HeavyResourcePolicy) (HeavyStageObservation, []string, error)

func (f HeavyStageHandlerFunc) Execute(ctx context.Context, item PipelineWorkItem, policy HeavyResourcePolicy) (HeavyStageObservation, []string, error) {
	return f(ctx, item, policy)
}

type HeavyExecutorRunInput struct {
	WorkerRunID   string
	LeaseDuration time.Duration
	Policy        HeavyResourcePolicy
	Handlers      map[string]HeavyStageHandler
	Now           time.Time
}

type HeavyExecutorRunResult struct {
	Claimed       int64                 `json:"claimed"`
	Completed     int64                 `json:"completed"`
	Failed        int64                 `json:"failed"`
	StageKey      string                `json:"stage_key,omitempty"`
	PipelineRunID string                `json:"pipeline_run_id,omitempty"`
	Observation   HeavyStageObservation `json:"resource_observation"`
}

func (s *Service) RunHeavyExecutorOnce(ctx context.Context, input HeavyExecutorRunInput) (HeavyExecutorRunResult, error) {
	items, err := s.ClaimPipelineRuns(ctx, PipelineExecutionHeavy, input.WorkerRunID, PipelineClaimOptions{Limit: 1, LeaseDuration: input.LeaseDuration, Now: input.Now})
	if err != nil {
		return HeavyExecutorRunResult{}, err
	}
	if len(items) == 0 {
		return HeavyExecutorRunResult{}, nil
	}
	return s.ExecuteClaimedHeavyStage(ctx, items[0], input.Policy, input.Handlers)
}

func (s *Service) ExecuteClaimedHeavyStage(ctx context.Context, item PipelineWorkItem, policy HeavyResourcePolicy, handlers map[string]HeavyStageHandler) (HeavyExecutorRunResult, error) {
	result := HeavyExecutorRunResult{Claimed: 1, StageKey: item.Stage.StageKey, PipelineRunID: item.Run.KnowledgePipelineRunID}
	handler := handlers[item.Stage.StageKey]
	if handler == nil {
		handler = s.defaultHeavyStageHandler(item.Stage.StageKey)
	}
	if policy.TimeoutSeconds == 0 {
		policy = DefaultHeavyResourcePolicy()
	}
	bounded, cancel := context.WithTimeout(ctx, policy.Timeout())
	defer cancel()
	observation, warnings, err := handler.Execute(bounded, item, policy)
	result.Observation = observation
	if err != nil {
		if failErr := s.FailPipelineStage(ctx, item, err, RetryableKnowledgeError(err)); failErr != nil {
			return HeavyExecutorRunResult{}, failErr
		}
		result.Failed = 1
		return result, nil
	}
	if err := s.storePipelineStageObservation(ctx, item, observation); err != nil {
		return HeavyExecutorRunResult{}, err
	}
	if containsWarning(warnings, heavyStageMoreUnits) {
		if err := s.ReleasePipelineClaim(ctx, item); err != nil {
			return HeavyExecutorRunResult{}, err
		}
		return result, nil
	}
	if _, err := s.CompletePipelineStage(ctx, item, warnings); err != nil {
		return HeavyExecutorRunResult{}, err
	}
	result.Completed = 1
	return result, nil
}

func containsWarning(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (s *Service) defaultHeavyStageHandler(stage string) HeavyStageHandler {
	switch stage {
	case FilePipelineStagePDFOCR:
		return PDFOCRStageHandler{Service: s}
	case FilePipelineStageImageDescription:
		return ImageDescriptionStageHandler{Service: s}
	case FilePipelineStageEmbedding:
		return EmbeddingStageHandler{Service: s}
	default:
		return controlledUnsupportedHeavyHandler(stage)
	}
}

func (s *Service) ReleasePipelineClaim(ctx context.Context, item PipelineWorkItem) error {
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockPipelineClaimTx(ctx, tx, item); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE knowledge.pipeline_stage_runs SET status='ready',claimed_by_worker_run_id='',updated_at=now() WHERE knowledge_pipeline_stage_run_id=$1 AND claim_generation=$2 AND claimed_by_worker_run_id=$3 AND status='processing'`, item.Stage.KnowledgePipelineStageRunID, item.Run.ClaimGeneration, item.Run.ClaimedByWorkerRunID)
	if err != nil {
		return err
	}
	if err = requireOneRow(result, "pipeline stage release fence is stale"); err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, `UPDATE knowledge.pipeline_runs SET status='waiting_heavy',claimed_by_worker_run_id='',claim_expires_at=NULL,updated_at=now() WHERE knowledge_pipeline_run_id=$1 AND generation=$2 AND claim_generation=$3 AND claimed_by_worker_run_id=$4 AND status='processing'`, item.Run.KnowledgePipelineRunID, item.Run.Generation, item.Run.ClaimGeneration, item.Run.ClaimedByWorkerRunID)
	if err != nil {
		return err
	}
	if err = requireOneRow(result, "pipeline run release fence is stale"); err != nil {
		return err
	}
	return tx.Commit()
}

func controlledUnsupportedHeavyHandler(stage string) HeavyStageHandler {
	return HeavyStageHandlerFunc(func(_ context.Context, _ PipelineWorkItem, policy HeavyResourcePolicy) (HeavyStageObservation, []string, error) {
		enforced, _ := json.Marshal(policy)
		return HeavyStageObservation{EnforcedPolicy: enforced, Observed: json.RawMessage(`{"available":false}`)}, []string{fmt.Sprintf("heavy stage %s is registered but not implemented", stage)}, nil
	})
}

func (s *Service) storePipelineStageObservation(ctx context.Context, item PipelineWorkItem, observation HeavyStageObservation) error {
	payload, _ := json.Marshal(observation)
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockPipelineClaimTx(ctx, tx, item); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE knowledge.pipeline_stage_runs SET resource_usage=$2,updated_at=now() WHERE knowledge_pipeline_stage_run_id=$1 AND claim_generation=$3 AND claimed_by_worker_run_id=$4 AND status='processing'`, item.Stage.KnowledgePipelineStageRunID, payload, item.Run.ClaimGeneration, item.Run.ClaimedByWorkerRunID)
	if err != nil {
		return err
	}
	if err = requireOneRow(result, "pipeline stage observation fence is stale"); err != nil {
		return err
	}
	return tx.Commit()
}
