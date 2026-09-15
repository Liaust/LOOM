package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/requestctx"
)

type WorkflowRuntimeExecutor struct {
	Jobs jobs.Service
}

type workflowRuntimeConfig struct {
	WorkflowRef        string                   `json:"workflow_ref"`
	ProjectRef         string                   `json:"project_ref,omitempty"`
	ScopeRef           string                   `json:"scope_ref,omitempty"`
	ExecutionMode      string                   `json:"execution_mode,omitempty"`
	WaitTimeoutSeconds int                      `json:"wait_timeout_seconds,omitempty"`
	CredentialBindings []jobs.CredentialBinding `json:"credential_bindings,omitempty"`
}

func NewWorkflowRuntimeExecutor(jobService jobs.Service) WorkflowRuntimeExecutor {
	return WorkflowRuntimeExecutor{Jobs: jobService}
}

func (e WorkflowRuntimeExecutor) RuntimeKind() string {
	return capabilities.RuntimeKindWorkflow
}

func (e WorkflowRuntimeExecutor) Execute(ctx context.Context, execCtx ExecutionContext, binding capabilities.EndpointRuntimeBinding, input json.RawMessage) (ExecutionResult, error) {
	cfg, err := decodeWorkflowRuntimeConfig(binding.RuntimeConfigJSON)
	if err != nil {
		return ExecutionResult{}, err
	}
	runInput := jobs.CreateWorkflowRunInput{
		WorkflowRef:        cfg.WorkflowRef,
		ProjectRef:         cfg.ProjectRef,
		ScopeRef:           cfg.ScopeRef,
		Input:              objectOrDefault(input),
		CredentialBindings: cfg.CredentialBindings,
		ExecutionMode:      cfg.ExecutionMode,
		WaitTimeoutSecs:    cfg.WaitTimeoutSeconds,
		RouteID:            execCtx.RouteID,
		CapabilityCallID:   execCtx.CapabilityCallID,
	}
	return executeWorkflowRun(ctx, e.Jobs, execCtx, runInput)
}

func decodeWorkflowRuntimeConfig(raw json.RawMessage) (workflowRuntimeConfig, error) {
	raw = objectOrDefault(raw)
	var cfg workflowRuntimeConfig
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return workflowRuntimeConfig{}, NewExecutionFailure(RuntimeFailureInvalidConfig, "workflow runtime config must match the workflow runtime schema", err)
	}
	cfg.WorkflowRef = strings.TrimSpace(cfg.WorkflowRef)
	cfg.ProjectRef = strings.TrimSpace(cfg.ProjectRef)
	cfg.ScopeRef = strings.TrimSpace(cfg.ScopeRef)
	cfg.ExecutionMode = jobs.NormalizeScriptRunMode(strings.TrimSpace(cfg.ExecutionMode))
	if cfg.WorkflowRef == "" {
		return workflowRuntimeConfig{}, NewExecutionFailure(RuntimeFailureInvalidConfig, "workflow runtime config requires workflow_ref", nil)
	}
	if cfg.ExecutionMode == "" {
		return workflowRuntimeConfig{}, NewExecutionFailure(RuntimeFailureInvalidConfig, fmt.Sprintf("workflow runtime config has invalid execution_mode: %s", cfg.ExecutionMode), nil)
	}
	if cfg.WaitTimeoutSeconds < 0 || cfg.WaitTimeoutSeconds > 86400 {
		return workflowRuntimeConfig{}, NewExecutionFailure(RuntimeFailureInvalidConfig, "workflow runtime wait_timeout_seconds must be between 0 and 86400", nil)
	}
	return cfg, nil
}

func executeWorkflowRun(ctx context.Context, jobService jobs.Service, execCtx ExecutionContext, runInput jobs.CreateWorkflowRunInput) (ExecutionResult, error) {
	executionMode := jobs.NormalizeScriptRunMode(strings.TrimSpace(runInput.ExecutionMode))
	if executionMode == "" {
		return ExecutionResult{}, fmt.Errorf("workflow execution_mode is invalid: %s", runInput.ExecutionMode)
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

	job, err := jobService.CreateWorkflowRun(ctx, req, runInput)
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
	result, err := jobService.WorkflowRunResult(ctx, job.JobID, executionMode, waitResult)
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
