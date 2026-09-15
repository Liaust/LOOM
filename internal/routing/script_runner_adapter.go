package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/requestctx"
)

const ScriptRunAddress = "main@script-runner.script.run"

type ScriptRunnerAdapter struct {
	Jobs    jobs.Service
	Version string
}

func NewScriptRunnerAdapter(jobService jobs.Service, version string) ScriptRunnerAdapter {
	return ScriptRunnerAdapter{
		Jobs:    jobService,
		Version: strings.TrimSpace(version),
	}
}

func (a ScriptRunnerAdapter) Execute(ctx context.Context, execCtx ExecutionContext, input json.RawMessage) (ExecutionResult, error) {
	operation := strings.TrimPrefix(strings.TrimSpace(execCtx.Operation), capabilityOperationPrefix)
	if operation != ScriptRunAddress {
		return ExecutionResult{}, fmt.Errorf("script-runner adapter does not support %s", operation)
	}

	runInput, err := decodeScriptRunInput(input)
	if err != nil {
		return ExecutionResult{}, err
	}
	executionMode := jobs.NormalizeScriptRunMode(strings.TrimSpace(runInput.ExecutionMode))
	if executionMode == "" {
		return ExecutionResult{}, fmt.Errorf("script-runner execution_mode is invalid: %s", runInput.ExecutionMode)
	}
	runInput.ExecutionMode = executionMode
	runInput.RouteID = execCtx.RouteID
	runInput.CapabilityCallID = execCtx.CapabilityCallID
	return executeScriptRun(ctx, a.Jobs, execCtx, runInput)
}

func executeScriptRun(ctx context.Context, jobService jobs.Service, execCtx ExecutionContext, runInput jobs.CreateScriptRunInput) (ExecutionResult, error) {
	executionMode := jobs.NormalizeScriptRunMode(strings.TrimSpace(runInput.ExecutionMode))
	if executionMode == "" {
		return ExecutionResult{}, fmt.Errorf("script execution_mode is invalid: %s", runInput.ExecutionMode)
	}
	runInput.ExecutionMode = executionMode
	runInput.RouteID = execCtx.RouteID
	runInput.CapabilityCallID = execCtx.CapabilityCallID
	req := requestctx.Context{
		ActorID:       execCtx.ActorID,
		OriginNodeID:  execCtx.OriginNodeID,
		ScopeID:       execCtx.ScopeID,
		CorrelationID: execCtx.CorrelationID,
		Source:        "routing.capability_call",
		FreshnessMode: "live_required",
	}

	job, err := jobService.CreateScriptRun(ctx, req, runInput)
	if err != nil {
		return ExecutionResult{}, err
	}
	waitResult := jobs.WaitResultQueued
	switch executionMode {
	case jobs.ScriptRunWaitUntilStarted:
		_, waitResult, err = jobService.WaitForJobStatus(ctx, job.JobID, []string{jobs.StatusRunning}, scriptRunnerWaitOptions(runInput.WaitTimeoutSecs))
	case jobs.ScriptRunWaitForCompletion:
		_, waitResult, err = jobService.WaitForJobTerminal(ctx, job.JobID, scriptRunnerWaitOptions(runInput.WaitTimeoutSecs))
	case jobs.ScriptRunEnqueueOnly:
		waitResult = jobs.WaitResultQueued
	}
	if err != nil {
		return ExecutionResult{}, err
	}
	result, err := jobService.ScriptRunResult(ctx, job.JobID, executionMode, waitResult)
	if err != nil {
		return ExecutionResult{}, err
	}
	if executionMode != jobs.ScriptRunEnqueueOnly && waitResult == jobs.WaitResultWaitTimeout {
		return scriptRunExecutionResult(CapabilityCallStatusFailed, result)
	}
	if executionMode == jobs.ScriptRunWaitForCompletion && result.Job.Job.Status != jobs.StatusCompleted && jobs.IsTerminalStatus(result.Job.Job.Status) {
		return scriptRunExecutionResult(CapabilityCallStatusFailed, result)
	}
	if executionMode == jobs.ScriptRunWaitUntilStarted {
		switch result.Job.Job.Status {
		case jobs.StatusFailed, jobs.StatusTimedOut, jobs.StatusCancelled:
			return scriptRunExecutionResult(CapabilityCallStatusFailed, result)
		}
	}
	return scriptRunExecutionResult(CapabilityCallStatusCompleted, result)
}

func decodeScriptRunInput(input json.RawMessage) (jobs.CreateScriptRunInput, error) {
	input = objectOrDefault(input)
	var out jobs.CreateScriptRunInput
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&out); err != nil {
		return jobs.CreateScriptRunInput{}, fmt.Errorf("script-runner input must match script run JSON: %w", err)
	}
	out.ScriptRef = strings.TrimSpace(out.ScriptRef)
	out.ObjectRef = strings.TrimSpace(out.ObjectRef)
	out.ProjectRef = strings.TrimSpace(out.ProjectRef)
	out.ScopeRef = strings.TrimSpace(out.ScopeRef)
	if out.ScriptRef == "" {
		return jobs.CreateScriptRunInput{}, fmt.Errorf("script_ref is required")
	}
	return out, nil
}

func scriptRunExecutionResult(status string, result jobs.RunResult) (ExecutionResult, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return ExecutionResult{}, err
	}
	jobID := result.Job.Job.JobID
	refs, err := json.Marshal(map[string]any{
		"job_id": jobID,
	})
	if err != nil {
		return ExecutionResult{}, err
	}
	return ExecutionResult{
		Status:     status,
		Result:     json.RawMessage(raw),
		ResultRefs: json.RawMessage(refs),
		JobID:      jobID,
	}, nil
}

func scriptRunnerWaitOptions(timeoutSeconds int) jobs.WaitOptions {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 120
	}
	if timeoutSeconds > 86400 {
		timeoutSeconds = 86400
	}
	return jobs.WaitOptions{
		Timeout:      time.Duration(timeoutSeconds) * time.Second,
		PollInterval: 500 * time.Millisecond,
	}
}
