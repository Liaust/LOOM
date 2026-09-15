package loomcli

import (
	"encoding/json"
	"strings"
	"testing"

	"loom.local/loom/internal/response"
)

func TestAgentWorklogListRequiresWorkContextJSON(t *testing.T) {
	stdout, stderr, err := executeRootCommand("--json", "agent", "worklog", "list", "--limit", "5")
	if err == nil {
		t.Fatal("agent worklog list without --work-context should return an error")
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("json error should not write stderr, got: %s", stderr)
	}
	var envelope response.ErrorEnvelope
	if decodeErr := json.Unmarshal([]byte(stdout), &envelope); decodeErr != nil {
		t.Fatalf("decode error response: %v output=%s", decodeErr, stdout)
	}
	if envelope.Error.Code != "agents.work_context_required" {
		t.Fatalf("error code = %q, want agents.work_context_required", envelope.Error.Code)
	}
	if envelope.Error.Target != "work_context" {
		t.Fatalf("error target = %q, want work_context", envelope.Error.Target)
	}
}

func TestAgentWorklogWriteRequiresWorkContext(t *testing.T) {
	stdout, stderr, err := executeRootCommand("agent", "worklog", "write", "--summary", "test")
	if err == nil {
		t.Fatal("agent worklog write without --work-context should return an error")
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("text error should not write stdout, got: %s", stdout)
	}
	for _, want := range []string{"agents.work_context_required", "Pass --work-context"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr missing %q:\n%s", want, stderr)
		}
	}
}
