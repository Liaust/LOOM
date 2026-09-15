package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"loom.local/loom/internal/capabilities"
)

const (
	RuntimeFailureActiveVersionMissing  = "runtime_binding.active_version_missing"
	RuntimeFailureMissing               = "runtime_binding.missing"
	RuntimeFailureInactive              = "runtime_binding.inactive"
	RuntimeFailureInvalidConfig         = "runtime_binding.invalid_config"
	RuntimeFailureLookupFailed          = "runtime_binding.lookup_failed"
	RuntimeFailureExecutorUnsupported   = "runtime_executor.unsupported"
	RuntimeFailureCommandNotFound       = "runtime_command.not_found"
	RuntimeFailureCommandExitFailed     = "runtime_command.exit_failed"
	RuntimeFailureCommandTimedOut       = "runtime_command.timed_out"
	RuntimeFailureCommandOutputTooLarge = "runtime_command.output_too_large"
	RuntimeFailureCommandOutputInvalid  = "runtime_command.output_invalid"
	RuntimeFailureHTTPInvalidURL        = "runtime_http.invalid_url"
	RuntimeFailureHTTPRequestFailed     = "runtime_http.request_failed"
	RuntimeFailureHTTPStatusFailed      = "runtime_http.status_failed"
	RuntimeFailureHTTPResponseTooLarge  = "runtime_http.response_too_large"
	RuntimeFailureHTTPResponseInvalid   = "runtime_http.response_invalid"
	RuntimeFailureHTTPNetworkDenied     = "runtime_http.network_denied"
)

type RuntimeExecutor interface {
	RuntimeKind() string
	Execute(ctx context.Context, execCtx ExecutionContext, binding capabilities.EndpointRuntimeBinding, input json.RawMessage) (ExecutionResult, error)
}

type ExecutionFailure struct {
	Code    string
	Message string
	Err     error
}

func NewExecutionFailure(code, message string, err error) *ExecutionFailure {
	return &ExecutionFailure{
		Code:    strings.TrimSpace(code),
		Message: strings.TrimSpace(message),
		Err:     err,
	}
}

func (e *ExecutionFailure) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	if e.Code != "" {
		return e.Code
	}
	return "runtime execution failed"
}

func (e *ExecutionFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func RuntimeFailureCode(err error) (string, string, bool) {
	var failure *ExecutionFailure
	if !errors.As(err, &failure) || failure == nil {
		return "", "", false
	}
	code := strings.TrimSpace(failure.Code)
	message := strings.TrimSpace(failure.Message)
	if message == "" && failure.Err != nil {
		message = failure.Err.Error()
	}
	if message == "" {
		message = code
	}
	if code == "" {
		code = "provider.execution_failed"
	}
	return code, message, true
}

func UnsupportedRuntimeExecutorFailure(kind string) *ExecutionFailure {
	kind = strings.TrimSpace(kind)
	return NewExecutionFailure(
		RuntimeFailureExecutorUnsupported,
		fmt.Sprintf("no runtime executor registered for %s", kind),
		nil,
	)
}
