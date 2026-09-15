package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/capabilityruntime"
)

type CommandRuntimeExecutor struct {
	LookPath func(string) (string, error)
}

func NewCommandRuntimeExecutor() CommandRuntimeExecutor {
	return CommandRuntimeExecutor{}
}

func (e CommandRuntimeExecutor) RuntimeKind() string {
	return capabilities.RuntimeKindCommand
}

func (e CommandRuntimeExecutor) Execute(ctx context.Context, execCtx ExecutionContext, binding capabilities.EndpointRuntimeBinding, input json.RawMessage) (ExecutionResult, error) {
	cfg, validation := capabilityruntime.DecodeCommand(binding.RuntimeConfigJSON, capabilityruntime.ValidationModeExecute)
	if !validation.Valid {
		return ExecutionResult{}, NewExecutionFailure(RuntimeFailureInvalidConfig, diagnosticsMessage(validation.Errors), nil)
	}

	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	executable, err := e.resolveExecutable(cfg.Argv[0])
	if err != nil {
		return ExecutionResult{}, NewExecutionFailure(RuntimeFailureCommandNotFound, err.Error(), err)
	}

	cmd := exec.CommandContext(runCtx, executable, cfg.Argv[1:]...)
	if strings.TrimSpace(cfg.WorkingDir) != "" {
		cmd.Dir = cfg.WorkingDir
	}
	cmd.Env, err = commandEnvironment(cfg)
	if err != nil {
		return ExecutionResult{}, NewExecutionFailure(RuntimeFailureInvalidConfig, err.Error(), err)
	}
	if cfg.Stdin.Mode == "input_json" || cfg.Stdin.Mode == "" {
		cmd.Stdin = bytes.NewReader(objectOrDefault(input))
	}

	stdout := newLimitedBuffer(cfg.Output.MaxStdoutBytes)
	stderr := newLimitedBuffer(cfg.Output.MaxStderrBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err = cmd.Run()
	exitCode := commandRuntimeExitCode(err)
	if runCtx.Err() == context.DeadlineExceeded {
		return ExecutionResult{}, NewExecutionFailure(RuntimeFailureCommandTimedOut, fmt.Sprintf("command runtime timed out after %s", timeout), err)
	}
	if err != nil && errors.Is(err, os.ErrNotExist) {
		return ExecutionResult{}, NewExecutionFailure(RuntimeFailureCommandNotFound, err.Error(), err)
	}
	if stdout.Truncated() || stderr.Truncated() {
		return ExecutionResult{}, NewExecutionFailure(RuntimeFailureCommandOutputTooLarge, "command runtime output exceeded configured byte limits", nil)
	}
	if err != nil && !allowedExitCode(exitCode, cfg.AllowedExitCodes) {
		return ExecutionResult{}, NewExecutionFailure(RuntimeFailureCommandExitFailed, fmt.Sprintf("command exited with code %d", exitCode), err)
	}

	result, err := commandRuntimeResult(cfg, stdout.Bytes())
	if err != nil {
		return ExecutionResult{}, err
	}
	resultRefs := objectOrDefault(mustJSON(map[string]any{
		"runtime_kind":       capabilities.RuntimeKindCommand,
		"runtime_binding_id": binding.RuntimeBindingID,
		"exit_code":          exitCode,
		"stderr_bytes":       len(stderr.Bytes()),
		"stderr_redacted":    len(strings.TrimSpace(stderr.String())) > 0,
		"capability_call_id": execCtx.CapabilityCallID,
	}))
	return ExecutionResult{
		Status:     CapabilityCallStatusCompleted,
		Result:     result,
		ResultRefs: resultRefs,
	}, nil
}

func (e CommandRuntimeExecutor) resolveExecutable(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("command executable is required")
	}
	if strings.Contains(name, "/") {
		return name, nil
	}
	lookPath := e.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	return lookPath(name)
}

func commandEnvironment(cfg capabilityruntime.CommandConfig) ([]string, error) {
	values := os.Environ()
	for key, value := range cfg.Env {
		key = strings.TrimSpace(key)
		if key != "" {
			values = append(values, key+"="+value)
		}
	}
	for target, ref := range cfg.EnvRefs {
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		value, ok := os.LookupEnv(ref.Env)
		if !ok && ref.Required {
			return nil, fmt.Errorf("required env ref %s is not set", ref.Env)
		}
		if ok {
			values = append(values, target+"="+value)
		}
	}
	return values, nil
}

func commandRuntimeResult(cfg capabilityruntime.CommandConfig, stdout []byte) (json.RawMessage, error) {
	if cfg.Output.Mode == "text" {
		return objectOrDefault(mustJSON(map[string]any{"stdout": string(stdout)})), nil
	}
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var object map[string]any
	if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
		if err == nil {
			err = fmt.Errorf("stdout JSON is not an object")
		}
		return nil, NewExecutionFailure(RuntimeFailureCommandOutputInvalid, "command runtime stdout must be a JSON object", err)
	}
	raw, err := json.Marshal(object)
	if err != nil {
		return nil, NewExecutionFailure(RuntimeFailureCommandOutputInvalid, "could not normalize command runtime stdout", err)
	}
	return json.RawMessage(raw), nil
}

func commandRuntimeExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func allowedExitCode(code int, allowed []int) bool {
	for _, item := range allowed {
		if item == code {
			return true
		}
	}
	return false
}

type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func newLimitedBuffer(limit int) *limitedBuffer {
	if limit <= 0 {
		limit = capabilityruntime.DefaultMaxStdoutBytes
	}
	return &limitedBuffer{limit: limit}
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.limit {
		remaining := b.limit - b.buf.Len()
		if remaining > 0 {
			_, _ = b.buf.Write(p[:remaining])
		}
		b.truncated = true
		return len(p), nil
	}
	_, _ = b.buf.Write(p)
	return len(p), nil
}

func (b *limitedBuffer) Bytes() []byte {
	return b.buf.Bytes()
}

func (b *limitedBuffer) String() string {
	return b.buf.String()
}

func (b *limitedBuffer) Truncated() bool {
	return b.truncated
}

func diagnosticsMessage(items []capabilityruntime.Diagnostic) string {
	if len(items) == 0 {
		return "runtime config is invalid"
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.Field) != "" {
			parts = append(parts, item.Field+": "+item.Message)
		} else {
			parts = append(parts, item.Message)
		}
	}
	return strings.Join(parts, "; ")
}
