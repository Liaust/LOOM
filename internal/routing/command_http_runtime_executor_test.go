package routing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/capabilities"
)

func TestCommandRuntimeExecutorReturnsJSONObject(t *testing.T) {
	executor := NewCommandRuntimeExecutor()
	result, err := executor.Execute(context.Background(), ExecutionContext{CapabilityCallID: "call_test"}, capabilities.EndpointRuntimeBinding{
		RuntimeBindingID: "runtime_binding_test",
		RuntimeKind:      capabilities.RuntimeKindCommand,
		Status:           capabilities.RuntimeBindingStatusActive,
		RuntimeConfigJSON: json.RawMessage(`{
			"argv":["printf","{\"ok\":true}"],
			"output":{"mode":"json"}
		}`),
	}, json.RawMessage(`{"input":true}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Status != CapabilityCallStatusCompleted || string(result.Result) != `{"ok":true}` {
		t.Fatalf("unexpected result %#v", result)
	}
}

func TestCommandRuntimeExecutorRejectsInvalidJSONOutput(t *testing.T) {
	executor := NewCommandRuntimeExecutor()
	_, err := executor.Execute(context.Background(), ExecutionContext{}, capabilities.EndpointRuntimeBinding{
		RuntimeBindingID:  "runtime_binding_test",
		RuntimeKind:       capabilities.RuntimeKindCommand,
		Status:            capabilities.RuntimeBindingStatusActive,
		RuntimeConfigJSON: json.RawMessage(`{"argv":["printf","not-json"]}`),
	}, json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected invalid JSON output error")
	}
	code, _, ok := RuntimeFailureCode(err)
	if !ok || code != RuntimeFailureCommandOutputInvalid {
		t.Fatalf("failure = %q ok=%t err=%v", code, ok, err)
	}
}

func TestCommandRuntimeExecutorRejectsMissingPathCommand(t *testing.T) {
	executor := NewCommandRuntimeExecutor()
	_, err := executor.Execute(context.Background(), ExecutionContext{}, capabilities.EndpointRuntimeBinding{
		RuntimeBindingID:  "runtime_binding_test",
		RuntimeKind:       capabilities.RuntimeKindCommand,
		Status:            capabilities.RuntimeBindingStatusActive,
		RuntimeConfigJSON: json.RawMessage(`{"argv":["` + filepath.Join(t.TempDir(), "missing-command") + `"]}`),
	}, json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected missing command error")
	}
	code, _, ok := RuntimeFailureCode(err)
	if !ok || code != RuntimeFailureCommandNotFound {
		t.Fatalf("failure = %q ok=%t err=%v", code, ok, err)
	}
}

func TestHTTPRuntimeExecutorReturnsJSONObject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	executor := NewHTTPRuntimeExecutor()
	result, err := executor.Execute(context.Background(), ExecutionContext{CapabilityCallID: "call_test"}, capabilities.EndpointRuntimeBinding{
		RuntimeBindingID: "runtime_binding_test",
		RuntimeKind:      capabilities.RuntimeKindHTTP,
		Status:           capabilities.RuntimeBindingStatusActive,
		RuntimeConfigJSON: json.RawMessage(`{
			"method":"POST",
			"url":"` + server.URL + `",
			"response":{"mode":"json"}
		}`),
	}, json.RawMessage(`{"input":true}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Status != CapabilityCallStatusCompleted || string(result.Result) != `{"ok":true}` {
		t.Fatalf("unexpected result %#v", result)
	}
}

func TestHTTPRuntimeExecutorRejectsBadStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusTeapot)
	}))
	defer server.Close()

	executor := NewHTTPRuntimeExecutor()
	_, err := executor.Execute(context.Background(), ExecutionContext{}, capabilities.EndpointRuntimeBinding{
		RuntimeBindingID:  "runtime_binding_test",
		RuntimeKind:       capabilities.RuntimeKindHTTP,
		Status:            capabilities.RuntimeBindingStatusActive,
		RuntimeConfigJSON: json.RawMessage(`{"url":"` + server.URL + `"}`),
	}, json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected status failure")
	}
	code, _, ok := RuntimeFailureCode(err)
	if !ok || code != RuntimeFailureHTTPStatusFailed {
		t.Fatalf("failure = %q ok=%t err=%v", code, ok, err)
	}
}
