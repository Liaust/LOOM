package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/projectquiescence"
)

type Env struct {
	ConfigPath           string
	StatePath            string
	DataDir              string
	NodeID               string
	NodeKey              string
	MainURL              string
	CorrelationID        string
	CredentialConfigured bool
}

type RunResult struct {
	Status           string
	HealthStatus     string
	Message          string
	ResultJSON       json.RawMessage
	QueueSummaryJSON json.RawMessage
}

type Runner interface {
	Kind() string
	RunOnce(ctx context.Context, store Store, env Env, instance WorkerInstance) (RunResult, error)
}

type Registry struct {
	runners map[string]Runner
}

func NewRegistry(runners ...Runner) Registry {
	registry := Registry{runners: map[string]Runner{}}
	for _, runner := range runners {
		registry.Register(runner)
	}
	return registry
}

func DefaultRegistry() Registry {
	return NewRegistry(
		SelfcheckRunner{},
		QueueReporterRunner{},
		PlaceholderRunner{kind: KindHeartbeat},
		PlaceholderRunner{kind: KindPoll},
		PlaceholderRunner{kind: KindOutboxFlusher},
		PlaceholderRunner{kind: KindStorageMount},
		PlaceholderRunner{kind: KindLaneHousekeeping},
	)
}

func (r Registry) Register(runner Runner) {
	if runner == nil {
		return
	}
	r.runners[runner.Kind()] = runner
}

func (r Registry) RunOnce(ctx context.Context, store Store, env Env, workerKey, correlationID string) (RunOutput, error) {
	if err := store.Ensure(); err != nil {
		return RunOutput{}, err
	}
	env.CorrelationID = strings.TrimSpace(correlationID)
	release, err := store.AcquireWorkerExecutionLock(ctx, workerKey)
	if err != nil {
		return RunOutput{}, err
	}
	defer func() { _ = release() }()
	instance, err := store.LoadInstance(workerKey)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return RunOutput{}, fmt.Errorf("unknown node-agent worker %q", workerKey)
		}
		return RunOutput{}, err
	}
	runner, ok := r.runners[instance.Kind]
	if !ok {
		return RunOutput{}, fmt.Errorf("node-agent worker kind %q is not registered", instance.Kind)
	}

	started := time.Now().UTC()
	run := WorkerRun{
		LocalRunID:    NewLocalRunID(),
		WorkerKey:     instance.WorkerKey,
		Kind:          instance.Kind,
		Status:        RunStatusStarted,
		StartedAt:     started,
		CorrelationID: correlationID,
	}

	var result RunResult
	var runErr error
	fenced := false
	if instance.Kind == KindWatchedRoot {
		fenced, err = store.ProjectArchiveFenceActive(projectquiescence.TargetKindWatchedRoot, instance.WorkerKey)
		if err != nil {
			return RunOutput{}, fmt.Errorf("inspect watched-root archive fence: %w", err)
		}
	}
	if !instance.Enabled || fenced {
		reason := "disabled"
		message := "worker is disabled"
		if fenced {
			reason = "project_archive_fenced"
			message = "worker is fenced for project archive"
		}
		result = RunResult{
			Status:       RunStatusSkipped,
			HealthStatus: WorkerStatusPaused,
			Message:      message,
			ResultJSON:   mustJSON(map[string]any{"skipped": true, "reason": reason}),
		}
	} else {
		result, runErr = runner.RunOnce(ctx, store, env, instance)
	}

	finished := time.Now().UTC()
	run.FinishedAt = &finished
	run.DurationMS = finished.Sub(started).Milliseconds()
	run.ResultJSON = result.ResultJSON
	run.HealthStatus = normalizeHealthStatus(result.HealthStatus)
	run.HealthMessage = strings.TrimSpace(result.Message)
	if strings.TrimSpace(result.Status) == "" {
		run.Status = RunStatusSucceeded
	} else {
		run.Status = strings.TrimSpace(result.Status)
	}
	if runErr != nil {
		run.Status = RunStatusFailed
		run.HealthStatus = WorkerStatusBlocked
		run.ErrorCode = "node_agent_runtime.worker_failed"
		run.ErrorMessage = runErr.Error()
	}

	checkpoint, err := store.LoadCheckpoint(instance.WorkerKey)
	if errors.Is(err, fs.ErrNotExist) {
		checkpoint = WorkerCheckpoint{WorkerKey: instance.WorkerKey}
	} else if err != nil {
		return RunOutput{}, err
	}
	checkpoint.LastRunID = run.LocalRunID
	checkpoint.LastStartedAt = &started
	checkpoint.LastFinishedAt = &finished
	if run.Status == RunStatusFailed {
		checkpoint.LastFailureAt = &finished
		checkpoint.ConsecutiveFailures++
		checkpoint.LastErrorCode = run.ErrorCode
		checkpoint.LastErrorMessage = run.ErrorMessage
	} else {
		checkpoint.LastSuccessAt = &finished
		checkpoint.ConsecutiveFailures = 0
		checkpoint.LastErrorCode = ""
		checkpoint.LastErrorMessage = ""
	}
	checkpoint.Metadata = mustJSON(map[string]any{
		"health_status": run.HealthStatus,
		"message":       run.HealthMessage,
	})

	health := WorkerHealth{
		WorkerKey:           instance.WorkerKey,
		Kind:                instance.Kind,
		Status:              run.HealthStatus,
		LastRunID:           run.LocalRunID,
		LastSuccessAt:       checkpoint.LastSuccessAt,
		LastFailureAt:       checkpoint.LastFailureAt,
		ConsecutiveFailures: checkpoint.ConsecutiveFailures,
		Message:             run.HealthMessage,
		QueueSummaryJSON:    result.QueueSummaryJSON,
		UpdatedAt:           finished,
	}

	if err := store.AppendRun(run); err != nil {
		return RunOutput{}, err
	}
	if err := store.SaveCheckpoint(checkpoint); err != nil {
		return RunOutput{}, err
	}
	if err := store.SaveHealth(health); err != nil {
		return RunOutput{}, err
	}

	return RunOutput{
		Instance:   instance,
		Run:        run,
		Health:     health,
		Checkpoint: checkpoint,
	}, runErr
}

type SelfcheckRunner struct{}

func (SelfcheckRunner) Kind() string {
	return KindSupervisorSelfcheck
}

func (SelfcheckRunner) RunOnce(ctx context.Context, store Store, env Env, instance WorkerInstance) (RunResult, error) {
	select {
	case <-ctx.Done():
		return RunResult{}, ctx.Err()
	default:
	}
	checks := map[string]any{}
	status := WorkerStatusHealthy
	messages := []string{}

	if err := store.Ensure(); err != nil {
		return RunResult{}, err
	}
	if err := checkWritableDir(env.DataDir); err != nil {
		status = WorkerStatusBlocked
		messages = append(messages, "data directory is not writable")
		checks["data_dir_writable"] = false
		checks["data_dir_error"] = err.Error()
	} else {
		checks["data_dir_writable"] = true
	}
	if err := validateJSONFile(env.ConfigPath); err != nil {
		status = WorkerStatusBlocked
		messages = append(messages, "config file is not readable JSON")
		checks["config_valid"] = false
		checks["config_error"] = err.Error()
	} else {
		checks["config_valid"] = true
	}
	if err := validateOptionalJSONFile(env.StatePath); err != nil {
		status = WorkerStatusBlocked
		messages = append(messages, "state file is not readable JSON")
		checks["state_valid"] = false
		checks["state_error"] = err.Error()
	} else {
		checks["state_valid"] = true
	}
	if strings.TrimSpace(env.NodeID) == "" {
		if status == WorkerStatusHealthy {
			status = WorkerStatusDegraded
		}
		messages = append(messages, "node credential is not imported")
		checks["node_id_present"] = false
	} else {
		checks["node_id_present"] = true
	}
	checks["credential_configured"] = env.CredentialConfigured
	if !env.CredentialConfigured {
		if status == WorkerStatusHealthy {
			status = WorkerStatusDegraded
		}
		messages = append(messages, "credential token is not configured")
	}
	outbox, err := store.OutboxSummary()
	if err != nil {
		return RunResult{}, err
	}
	inbox, err := store.InboxSummary()
	if err != nil {
		return RunResult{}, err
	}
	checks["outbox"] = outbox
	checks["inbox"] = inbox

	return RunResult{
		Status:       RunStatusSucceeded,
		HealthStatus: status,
		Message:      strings.Join(messages, "; "),
		ResultJSON: mustJSON(map[string]any{
			"status": status,
			"checks": checks,
		}),
	}, nil
}

type QueueReporterRunner struct{}

func (QueueReporterRunner) Kind() string {
	return KindLocalQueueReporter
}

func (QueueReporterRunner) RunOnce(ctx context.Context, store Store, env Env, instance WorkerInstance) (RunResult, error) {
	select {
	case <-ctx.Done():
		return RunResult{}, ctx.Err()
	default:
	}
	summary, err := store.BuildLocalSummary()
	if err != nil {
		return RunResult{}, err
	}
	if err := store.SaveLatestSummary(summary); err != nil {
		return RunResult{}, err
	}
	raw := mustJSON(summary)
	return RunResult{
		Status:           RunStatusSucceeded,
		HealthStatus:     WorkerStatusHealthy,
		Message:          "local queue summary updated",
		ResultJSON:       raw,
		QueueSummaryJSON: raw,
	}, nil
}

type PlaceholderRunner struct {
	kind string
}

func (r PlaceholderRunner) Kind() string {
	return r.kind
}

func (r PlaceholderRunner) RunOnce(ctx context.Context, store Store, env Env, instance WorkerInstance) (RunResult, error) {
	return RunResult{
		Status:       RunStatusSkipped,
		HealthStatus: WorkerStatusDegraded,
		Message:      "worker runtime will be implemented in Slice 08 Part 2 or Part 3",
		ResultJSON: mustJSON(map[string]any{
			"implemented": false,
			"kind":        r.kind,
		}),
	}, nil
}

func validateJSONFile(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("path is empty")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	var value map[string]any
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	return decoder.Decode(&value)
}

func validateOptionalJSONFile(path string) error {
	err := validateJSONFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func checkWritableDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("dir is empty")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".selfcheck-*")
	if err != nil {
		return err
	}
	path := tmp.Name()
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Remove(path)
}

func normalizeHealthStatus(status string) string {
	switch strings.TrimSpace(status) {
	case WorkerStatusHealthy,
		WorkerStatusOfflineQueueing,
		WorkerStatusDegraded,
		WorkerStatusBlocked,
		WorkerStatusRequiresManualAction,
		WorkerStatusPaused,
		WorkerStatusShuttingDown:
		return strings.TrimSpace(status)
	default:
		return WorkerStatusHealthy
	}
}

func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func RedactOutboxItems(items []OutboxItem) []OutboxItem {
	redacted := make([]OutboxItem, 0, len(items))
	for _, item := range items {
		item.PayloadJSON = RedactRawJSON(item.PayloadJSON)
		item.ResultJSON = RedactRawJSON(item.ResultJSON)
		redacted = append(redacted, item)
	}
	return redacted
}

func RedactRawJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return raw
	}
	return mustJSON(redactValue(value))
}

func redactValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, value := range typed {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "credential") {
				out[key] = "[redacted]"
				continue
			}
			out[key] = redactValue(value)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, value := range typed {
			out = append(out, redactValue(value))
		}
		return out
	default:
		return typed
	}
}

func RuntimeEnv(configPath, statePath, dataDir, nodeID, nodeKey, mainURL string, credentialConfigured bool) Env {
	return Env{
		ConfigPath:           filepath.Clean(configPath),
		StatePath:            filepath.Clean(statePath),
		DataDir:              filepath.Clean(dataDir),
		NodeID:               strings.TrimSpace(nodeID),
		NodeKey:              strings.TrimSpace(nodeKey),
		MainURL:              strings.TrimSpace(mainURL),
		CredentialConfigured: credentialConfigured,
	}
}
