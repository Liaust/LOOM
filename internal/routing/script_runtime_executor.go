package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/jobs"
)

type ScriptRuntimeExecutor struct {
	Jobs jobs.Service
}

type scriptRuntimeConfig struct {
	ScriptRef          string                   `json:"script_ref"`
	ProjectRef         string                   `json:"project_ref,omitempty"`
	ScopeRef           string                   `json:"scope_ref,omitempty"`
	ExecutionMode      string                   `json:"execution_mode,omitempty"`
	WaitTimeoutSeconds int                      `json:"wait_timeout_seconds,omitempty"`
	CredentialBindings []jobs.CredentialBinding `json:"credential_bindings,omitempty"`
}

func NewScriptRuntimeExecutor(jobService jobs.Service) ScriptRuntimeExecutor {
	return ScriptRuntimeExecutor{Jobs: jobService}
}

func (e ScriptRuntimeExecutor) RuntimeKind() string {
	return capabilities.RuntimeKindScript
}

func (e ScriptRuntimeExecutor) Execute(ctx context.Context, execCtx ExecutionContext, binding capabilities.EndpointRuntimeBinding, input json.RawMessage) (ExecutionResult, error) {
	cfg, err := decodeScriptRuntimeConfig(binding.RuntimeConfigJSON)
	if err != nil {
		return ExecutionResult{}, err
	}
	runInput := jobs.CreateScriptRunInput{
		ScriptRef:          cfg.ScriptRef,
		ProjectRef:         cfg.ProjectRef,
		ScopeRef:           cfg.ScopeRef,
		Input:              objectOrDefault(input),
		CredentialBindings: cfg.CredentialBindings,
		ExecutionMode:      cfg.ExecutionMode,
		WaitTimeoutSecs:    cfg.WaitTimeoutSeconds,
	}
	return executeScriptRun(ctx, e.Jobs, execCtx, runInput)
}

func decodeScriptRuntimeConfig(raw json.RawMessage) (scriptRuntimeConfig, error) {
	raw = objectOrDefault(raw)
	var cfg scriptRuntimeConfig
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return scriptRuntimeConfig{}, NewExecutionFailure(RuntimeFailureInvalidConfig, "script runtime config must match the script runtime schema", err)
	}
	cfg.ScriptRef = strings.TrimSpace(cfg.ScriptRef)
	cfg.ProjectRef = strings.TrimSpace(cfg.ProjectRef)
	cfg.ScopeRef = strings.TrimSpace(cfg.ScopeRef)
	cfg.ExecutionMode = jobs.NormalizeScriptRunMode(strings.TrimSpace(cfg.ExecutionMode))
	if cfg.ScriptRef == "" {
		return scriptRuntimeConfig{}, NewExecutionFailure(RuntimeFailureInvalidConfig, "script runtime config requires script_ref", nil)
	}
	if cfg.ExecutionMode == "" {
		return scriptRuntimeConfig{}, NewExecutionFailure(RuntimeFailureInvalidConfig, fmt.Sprintf("script runtime config has invalid execution_mode: %s", cfg.ExecutionMode), nil)
	}
	if cfg.WaitTimeoutSeconds < 0 || cfg.WaitTimeoutSeconds > 86400 {
		return scriptRuntimeConfig{}, NewExecutionFailure(RuntimeFailureInvalidConfig, "script runtime wait_timeout_seconds must be between 0 and 86400", nil)
	}
	return cfg, nil
}
