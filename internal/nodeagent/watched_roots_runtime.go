package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/filesystemconnector"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodeagent/watchedroots"
)

type watchedRootRuntime struct{}

func (watchedRootRuntime) Kind() string {
	return noderuntime.KindWatchedRoot
}

func (watchedRootRuntime) Watch(ctx context.Context, runtimeStore noderuntime.Store, env noderuntime.Env, instance noderuntime.WorkerInstance, wake func()) (noderuntime.WakeSource, error) {
	_, config, _, err := loadRuntimeNodeAgentState(env)
	if err != nil {
		return nil, err
	}
	rootConfig, err := watchedRootConfigFromInstance(instance)
	if err != nil {
		return nil, err
	}
	if !rootConfig.Watch.Enabled || rootConfig.Watch.Backend != watchedroots.WatchBackendAuto {
		return nil, nil
	}
	root, err := watchedroots.ValidateRootConfigForRun(rootConfig, config.Filesystem)
	if err != nil {
		return nil, err
	}
	watch, err := watchedroots.StartNativeWatch(ctx, watchedroots.NewStore(runtimeStore.DataDir), root, wake)
	if err != nil {
		return nil, err
	}
	return watch, nil
}

func (watchedRootRuntime) NextRunAt(runtimeStore noderuntime.Store, env noderuntime.Env, instance noderuntime.WorkerInstance, now, lastStarted time.Time, watching bool) (time.Time, error) {
	store, config, state, err := loadRuntimeNodeAgentState(env)
	if err != nil {
		return time.Time{}, err
	}
	rootConfig, err := watchedRootConfigFromInstance(instance)
	if err != nil {
		return time.Time{}, err
	}
	rootConfig, err = watchedroots.ValidatePortableRootConfig(rootConfig)
	if err != nil {
		return time.Time{}, err
	}
	safe, ok := filesystemconnector.FindSafeRoot(config.Filesystem, rootConfig.SafeRootKey)
	if !ok {
		return time.Time{}, fmt.Errorf("filesystem safe root %q is not configured", rootConfig.SafeRootKey)
	}
	if err := filesystemconnector.ValidateSafeRoot(safe); err != nil {
		return time.Time{}, err
	}
	// Eligibility needs the effective config, not source traversal or policy
	// discovery. Watch registration and RunOnce retain full source validation.
	rootConfig.Scan.MaxHashFileBytes = min(rootConfig.Scan.MaxHashFileBytes, safe.MaxFileBytes)
	root := watchedroots.ValidatedRoot{Config: rootConfig, ConfigHash: watchedroots.ConfigHash(rootConfig)}
	interval := time.Duration(max(1, instance.IntervalSeconds)) * time.Second
	next, err := watchedroots.NextNativeReconcile(watchedroots.NewStore(runtimeStore.DataDir), root, now, lastStarted, watching, interval)
	if err != nil {
		return next, err
	}
	return watchedRootDeliveryDeadline(store, config, state, root.Config.RootKey, next, lastStarted, interval)
}

func watchedRootDeliveryDeadline(store Store, config Config, state State, rootKey string, next, lastStarted time.Time, interval time.Duration) (time.Time, error) {
	if store.syncBatch == nil && store.recordsExist() {
		return recordDeliveryDeadline(store, rootKey, next, lastStarted, interval)
	}
	// Scheduling must not wait behind a delivery/scan transaction: that would
	// stall native wakes for every other root. Retry the hint shortly when busy;
	// actual delivery still takes the full authoritative queue lock.
	wait, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	locked, unlock, err := store.lockLocalSync(wait)
	if errors.Is(err, context.DeadlineExceeded) {
		return minDeadline(next, time.Now().Add(time.Second)), nil
	}
	if err != nil {
		return next, err
	}
	defer unlock()
	store = locked
	syncStatus, err := store.localSyncRootStatus(config, state, rootKey)
	if err != nil {
		return next, err
	}
	backups, err := store.backupDeliveryCountsByRoot()
	if err != nil {
		return next, err
	}
	backup := backups[rootKey]
	if syncStatus.Counts.Pending > 0 || backup.Pending > 0 || backup.Retryable > 0 {
		if retry := lastStarted.Add(min(interval, time.Minute)); retry.Before(next) {
			next = retry
		}
	}
	return next, nil
}

func minDeadline(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
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
	validated, err := watchedroots.ValidateRuntimeRoot(watchedroots.NewStore(runtimeStore.DataDir), rootConfig, config.Filesystem, time.Now())
	if err != nil {
		if strings.HasPrefix(rootConfig.SafeRootKey, "declaration_app_") {
			for _, safe := range config.Filesystem.SafeRoots {
				if safe.RootKey == rootConfig.SafeRootKey {
					root := watchedroots.ValidatedRoot{Config: rootConfig, ConfigHash: watchedroots.ConfigHash(rootConfig), SafeRoot: safe}
					_ = reportWatchedRootRun(ctx, store, config, state, instance, root, watchedroots.ScanResult{Status: watchedroots.RunStatusBlocked, Message: "application_data_path_unavailable"}, "")
				}
			}
		}
		return watchedRootRuntimeFailure(err), nil
	}
	result, err := runWatchedRootReconcileAndPlan(ctx, store, config, state, instance, validated, watchedroots.ScanModeAuto, true, true, true, "")
	if errors.Is(err, watchedroots.ErrPolicyInputsChanged) {
		watchedStore := watchedroots.NewStore(runtimeStore.DataDir)
		watchedroots.InvalidateRuntimePolicy(watchedStore, rootConfig.RootKey)
		if err = watchedStore.MarkRescanRequired(rootConfig.RootKey, "output_policy_changed"); err == nil {
			validated, err = watchedroots.ValidateRuntimeRoot(watchedStore, rootConfig, config.Filesystem, time.Now())
			if err == nil {
				result, err = runWatchedRootReconcileAndPlan(ctx, store, config, state, instance, validated, watchedroots.ScanModeAuto, true, true, true, "")
			}
		}
	}
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
