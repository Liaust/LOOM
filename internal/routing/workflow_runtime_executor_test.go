package routing

import (
	"encoding/json"
	"strings"
	"testing"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/jobs"
)

func TestWorkflowRuntimeExecutorKind(t *testing.T) {
	executor := NewWorkflowRuntimeExecutor(jobs.Service{})
	if got := executor.RuntimeKind(); got != capabilities.RuntimeKindWorkflow {
		t.Fatalf("RuntimeKind() = %q, want %q", got, capabilities.RuntimeKindWorkflow)
	}
}

func TestDecodeWorkflowRuntimeConfig(t *testing.T) {
	cfg, err := decodeWorkflowRuntimeConfig(json.RawMessage(`{
		"workflow_ref": "workflow_test",
		"project_ref": "project-smoke",
		"scope_ref": "scope_test",
		"execution_mode": "wait_for_completion",
		"wait_timeout_seconds": 30
	}`))
	if err != nil {
		t.Fatalf("decodeWorkflowRuntimeConfig returned error: %v", err)
	}
	if cfg.WorkflowRef != "workflow_test" || cfg.ProjectRef != "project-smoke" || cfg.ScopeRef != "scope_test" {
		t.Fatalf("unexpected workflow config refs: %#v", cfg)
	}
	if cfg.ExecutionMode != jobs.ScriptRunWaitForCompletion || cfg.WaitTimeoutSeconds != 30 {
		t.Fatalf("unexpected workflow execution config: %#v", cfg)
	}
}

func TestDecodeWorkflowRuntimeConfigRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{name: "missing workflow ref", raw: json.RawMessage(`{"execution_mode":"wait_for_completion"}`), want: "requires workflow_ref"},
		{name: "unknown field", raw: json.RawMessage(`{"workflow_ref":"workflow_test","execution_mode":"wait_for_completion","extra":true}`), want: "must match"},
		{name: "invalid mode", raw: json.RawMessage(`{"workflow_ref":"workflow_test","execution_mode":"never"}`), want: "invalid execution_mode"},
		{name: "invalid timeout", raw: json.RawMessage(`{"workflow_ref":"workflow_test","execution_mode":"wait_for_completion","wait_timeout_seconds":86401}`), want: "wait_timeout_seconds"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeWorkflowRuntimeConfig(tt.raw)
			if err == nil {
				t.Fatal("expected error")
			}
			code, message, ok := RuntimeFailureCode(err)
			if !ok {
				t.Fatalf("expected typed runtime failure, got %T", err)
			}
			if code != RuntimeFailureInvalidConfig {
				t.Fatalf("failure code = %q, want %q", code, RuntimeFailureInvalidConfig)
			}
			if !strings.Contains(message, tt.want) {
				t.Fatalf("message = %q, want to contain %q", message, tt.want)
			}
		})
	}
}
