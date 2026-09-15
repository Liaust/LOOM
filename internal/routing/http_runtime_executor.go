package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/capabilityruntime"
)

type HTTPRuntimeExecutor struct {
	Client *http.Client
}

func NewHTTPRuntimeExecutor() HTTPRuntimeExecutor {
	return HTTPRuntimeExecutor{}
}

func (e HTTPRuntimeExecutor) RuntimeKind() string {
	return capabilities.RuntimeKindHTTP
}

func (e HTTPRuntimeExecutor) Execute(ctx context.Context, execCtx ExecutionContext, binding capabilities.EndpointRuntimeBinding, input json.RawMessage) (ExecutionResult, error) {
	cfg, validation := capabilityruntime.DecodeHTTP(binding.RuntimeConfigJSON, capabilityruntime.ValidationModeExecute)
	if !validation.Valid {
		code := RuntimeFailureInvalidConfig
		for _, item := range validation.Errors {
			if item.Code == "runtime_http.network_denied" {
				code = RuntimeFailureHTTPNetworkDenied
				break
			}
		}
		return ExecutionResult{}, NewExecutionFailure(code, diagnosticsMessage(validation.Errors), nil)
	}

	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var body io.Reader
	if cfg.Body.Mode == "input_json" || cfg.Body.Mode == "" {
		body = bytes.NewReader(objectOrDefault(input))
	}
	req, err := http.NewRequestWithContext(runCtx, cfg.Method, cfg.URL, body)
	if err != nil {
		return ExecutionResult{}, NewExecutionFailure(RuntimeFailureHTTPInvalidURL, "could not build HTTP runtime request", err)
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range cfg.Headers {
		req.Header.Set(key, value)
	}
	for key, ref := range cfg.HeadersFromEnv {
		value, ok := os.LookupEnv(ref.Env)
		if !ok && ref.Required {
			return ExecutionResult{}, NewExecutionFailure(RuntimeFailureInvalidConfig, fmt.Sprintf("required header env ref %s is not set", ref.Env), nil)
		}
		if ok {
			req.Header.Set(key, value)
		}
	}

	client := e.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		if runCtx.Err() == context.DeadlineExceeded {
			return ExecutionResult{}, NewExecutionFailure(RuntimeFailureHTTPRequestFailed, fmt.Sprintf("http runtime timed out after %s", timeout), err)
		}
		return ExecutionResult{}, NewExecutionFailure(RuntimeFailureHTTPRequestFailed, "http runtime request failed", err)
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, int64(cfg.Response.MaxBodyBytes)+1)
	payload, err := io.ReadAll(limited)
	if err != nil {
		return ExecutionResult{}, NewExecutionFailure(RuntimeFailureHTTPRequestFailed, "could not read http runtime response", err)
	}
	if len(payload) > cfg.Response.MaxBodyBytes {
		return ExecutionResult{}, NewExecutionFailure(RuntimeFailureHTTPResponseTooLarge, "http runtime response exceeded configured byte limit", nil)
	}
	if !httpStatusAllowed(resp.StatusCode, cfg.Response.SuccessStatuses) {
		return ExecutionResult{}, NewExecutionFailure(RuntimeFailureHTTPStatusFailed, fmt.Sprintf("http runtime returned status %d", resp.StatusCode), nil)
	}

	result, err := httpRuntimeResult(cfg, payload)
	if err != nil {
		return ExecutionResult{}, err
	}
	resultRefs := objectOrDefault(mustJSON(map[string]any{
		"runtime_kind":       capabilities.RuntimeKindHTTP,
		"runtime_binding_id": binding.RuntimeBindingID,
		"status_code":        resp.StatusCode,
		"method":             req.Method,
		"host":               req.URL.Hostname(),
		"body_bytes":         len(payload),
		"capability_call_id": execCtx.CapabilityCallID,
	}))
	return ExecutionResult{
		Status:     CapabilityCallStatusCompleted,
		Result:     result,
		ResultRefs: resultRefs,
	}, nil
}

func httpStatusAllowed(status int, allowed []int) bool {
	for _, item := range allowed {
		if item == status {
			return true
		}
	}
	return false
}

func httpRuntimeResult(cfg capabilityruntime.HTTPConfig, payload []byte) (json.RawMessage, error) {
	if cfg.Response.Mode == "text" {
		return objectOrDefault(mustJSON(map[string]any{"body": string(payload)})), nil
	}
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var object map[string]any
	if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
		if err == nil {
			err = fmt.Errorf("response JSON is not an object")
		}
		return nil, NewExecutionFailure(RuntimeFailureHTTPResponseInvalid, "http runtime response body must be a JSON object", err)
	}
	raw, err := json.Marshal(object)
	if err != nil {
		return nil, NewExecutionFailure(RuntimeFailureHTTPResponseInvalid, "could not normalize http runtime response body", err)
	}
	return json.RawMessage(raw), nil
}

func redactRuntimeText(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return "[REDACTED]"
}
