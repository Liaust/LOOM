package nodeagent

import (
	"context"

	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodeagent/watchedroots"
)

type watchedRootRuntime struct{}

func (watchedRootRuntime) Kind() string {
	return noderuntime.KindWatchedRoot
}

func (watchedRootRuntime) RunOnce(ctx context.Context, runtimeStore noderuntime.Store, env noderuntime.Env, instance noderuntime.WorkerInstance) (noderuntime.RunResult, error) {
	store, config, state, err := loadRuntimeNodeAgentState(env)
	if err != nil {
		return nodeRuntimeFailure(err), nil
	}
	rootConfig, err := watchedRootConfigFromInstance(instance)
	if err != nil {
		return watchedRootRuntimeFailure(err), nil
	}
	validated, err := watchedroots.ValidateRootConfigForRun(rootConfig, config.Filesystem)
	if err != nil {
		return watchedRootRuntimeFailure(err), nil
	}
	result, err := runWatchedRootReconcileAndPlan(ctx, store, config, state, instance, validated, watchedroots.ScanModeAuto, true, true, true, "")
	if err != nil {
		return nodeRuntimeFailure(err), nil
	}
	return noderuntime.RunResult{
		Status:           watchedRootRunStatus(result.Status),
		HealthStatus:     watchedRootHealthStatus(result.Status),
		Message:          result.Message,
		ResultJSON:       mustMarshalJSON(result),
		QueueSummaryJSON: mustMarshalJSON(result.Summary),
	}, nil
}

func watchedRootRuntimeFailure(err error) noderuntime.RunResult {
	return noderuntime.RunResult{
		Status:       noderuntime.RunStatusFailed,
		HealthStatus: noderuntime.WorkerStatusBlocked,
		Message:      err.Error(),
		ResultJSON: mustMarshalJSON(map[string]any{
			"error": err.Error(),
		}),
	}
}

func watchedRootRunStatus(status string) string {
	switch status {
	case watchedroots.RunStatusSkipped:
		return noderuntime.RunStatusSkipped
	case watchedroots.RunStatusHealthy, watchedroots.RunStatusDegraded, watchedroots.RunStatusBlocked, watchedroots.RunStatusRequiresManualAction:
		return noderuntime.RunStatusSucceeded
	case "":
		return noderuntime.RunStatusSucceeded
	default:
		return noderuntime.RunStatusSucceeded
	}
}

func watchedRootHealthStatus(status string) string {
	switch status {
	case watchedroots.RunStatusHealthy, watchedroots.RunStatusSkipped, "":
		return noderuntime.WorkerStatusHealthy
	case watchedroots.RunStatusDegraded:
		return noderuntime.WorkerStatusDegraded
	case watchedroots.RunStatusBlocked:
		return noderuntime.WorkerStatusBlocked
	case watchedroots.RunStatusRequiresManualAction:
		return noderuntime.WorkerStatusRequiresManualAction
	default:
		return noderuntime.WorkerStatusDegraded
	}
}
