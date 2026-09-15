package routing

import (
	"encoding/json"
	"errors"
	"testing"

	"loom.local/loom/internal/jobs"
)

func TestDecodeScriptRuntimeConfig(t *testing.T) {
	cfg, err := decodeScriptRuntimeConfig(json.RawMessage(`{"script_ref":"script_123","execution_mode":"wait_until_started","wait_timeout_seconds":5}`))
	if err != nil {
		t.Fatalf("decodeScriptRuntimeConfig returned error: %v", err)
	}
	if cfg.ScriptRef != "script_123" {
		t.Fatalf("script_ref = %q", cfg.ScriptRef)
	}
	if cfg.ExecutionMode != jobs.ScriptRunWaitUntilStarted {
		t.Fatalf("execution_mode = %q", cfg.ExecutionMode)
	}
	if cfg.WaitTimeoutSeconds != 5 {
		t.Fatalf("wait_timeout_seconds = %d", cfg.WaitTimeoutSeconds)
	}
}

func TestDecodeScriptRuntimeConfigRejectsInvalidConfig(t *testing.T) {
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{}`),
		json.RawMessage(`{"script_ref":"script_123","execution_mode":"inline"}`),
		json.RawMessage(`{"script_ref":"script_123","unknown":true}`),
	} {
		_, err := decodeScriptRuntimeConfig(raw)
		if err == nil {
			t.Fatalf("decodeScriptRuntimeConfig(%s) returned nil error", raw)
		}
		var failure *ExecutionFailure
		if !errors.As(err, &failure) || failure.Code != RuntimeFailureInvalidConfig {
			t.Fatalf("error = %#v, want invalid config execution failure", err)
		}
	}
}
