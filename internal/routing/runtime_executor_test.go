package routing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"loom.local/loom/internal/capabilities"
)

type fakeRuntimeExecutor struct {
	kind string
	err  error
}

func (e fakeRuntimeExecutor) RuntimeKind() string {
	return e.kind
}

func (e fakeRuntimeExecutor) Execute(context.Context, ExecutionContext, capabilities.EndpointRuntimeBinding, json.RawMessage) (ExecutionResult, error) {
	if e.err != nil {
		return ExecutionResult{}, e.err
	}
	return ExecutionResult{Status: CapabilityCallStatusCompleted, Result: json.RawMessage(`{"ok":true}`), ResultRefs: json.RawMessage(`{}`)}, nil
}

type fakeBindingResolver struct {
	binding capabilities.EndpointRuntimeBinding
	err     error
}

func (r fakeBindingResolver) ActiveRuntimeBindingForEndpointVersion(context.Context, string) (capabilities.EndpointRuntimeBinding, error) {
	if r.err != nil {
		return capabilities.EndpointRuntimeBinding{}, r.err
	}
	return r.binding, nil
}

func TestRuntimeExecutorRegistry(t *testing.T) {
	registry := NewProviderRuntimeRegistry()
	executor := fakeRuntimeExecutor{kind: capabilities.RuntimeKindScript}
	if err := registry.RegisterRuntimeExecutor(executor); err != nil {
		t.Fatalf("RegisterRuntimeExecutor returned error: %v", err)
	}
	if _, ok := registry.RuntimeExecutor(capabilities.RuntimeKindScript); !ok {
		t.Fatal("registered runtime executor was not found")
	}
	if err := registry.RegisterRuntimeExecutor(executor); err == nil {
		t.Fatal("expected duplicate runtime executor registration to fail")
	}
	if err := registry.RegisterRuntimeExecutor(fakeRuntimeExecutor{}); err == nil {
		t.Fatal("expected empty runtime kind to fail")
	}
}

func TestDeclarativeRuntimeAdapterFailureCodes(t *testing.T) {
	tests := []struct {
		name    string
		adapter DeclarativeRuntimeAdapter
		execCtx ExecutionContext
		want    string
	}{
		{
			name:    "missing active endpoint version",
			adapter: NewDeclarativeRuntimeAdapter(fakeBindingResolver{}, NewProviderRuntimeRegistry()),
			execCtx: ExecutionContext{CapabilityEndpointID: "endp_test"},
			want:    RuntimeFailureActiveVersionMissing,
		},
		{
			name: "missing binding",
			adapter: NewDeclarativeRuntimeAdapter(fakeBindingResolver{
				err: sql.ErrNoRows,
			}, NewProviderRuntimeRegistry()),
			execCtx: ExecutionContext{CapabilityEndpointID: "endp_test", ActiveEndpointVersionID: "endpv_test"},
			want:    RuntimeFailureMissing,
		},
		{
			name: "missing registry",
			adapter: NewDeclarativeRuntimeAdapter(fakeBindingResolver{
				binding: capabilities.EndpointRuntimeBinding{RuntimeBindingID: "runtime_binding_test", RuntimeKind: capabilities.RuntimeKindScript, Status: capabilities.RuntimeBindingStatusActive},
			}, nil),
			execCtx: ExecutionContext{CapabilityEndpointID: "endp_test", ActiveEndpointVersionID: "endpv_test"},
			want:    "provider.adapter_missing",
		},
		{
			name: "inactive binding",
			adapter: NewDeclarativeRuntimeAdapter(fakeBindingResolver{
				binding: capabilities.EndpointRuntimeBinding{RuntimeBindingID: "runtime_binding_test", RuntimeKind: capabilities.RuntimeKindScript, Status: capabilities.RuntimeBindingStatusRegistered},
			}, NewProviderRuntimeRegistry()),
			execCtx: ExecutionContext{CapabilityEndpointID: "endp_test", ActiveEndpointVersionID: "endpv_test"},
			want:    RuntimeFailureInactive,
		},
		{
			name: "unsupported executor",
			adapter: NewDeclarativeRuntimeAdapter(fakeBindingResolver{
				binding: capabilities.EndpointRuntimeBinding{RuntimeBindingID: "runtime_binding_test", RuntimeKind: capabilities.RuntimeKindScript, Status: capabilities.RuntimeBindingStatusActive},
			}, NewProviderRuntimeRegistry()),
			execCtx: ExecutionContext{CapabilityEndpointID: "endp_test", ActiveEndpointVersionID: "endpv_test"},
			want:    RuntimeFailureExecutorUnsupported,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.adapter.Execute(context.Background(), tt.execCtx, nil)
			if err == nil {
				t.Fatal("expected error")
			}
			code, _, ok := RuntimeFailureCode(err)
			if !ok {
				t.Fatalf("expected typed runtime failure, got %T", err)
			}
			if code != tt.want {
				t.Fatalf("code = %q, want %q", code, tt.want)
			}
		})
	}
}

func TestDeclarativeRuntimeAdapterExecutorFailureCodePassesThrough(t *testing.T) {
	registry := NewProviderRuntimeRegistry()
	expected := NewExecutionFailure(RuntimeFailureInvalidConfig, "bad config", errors.New("decode failed"))
	if err := registry.RegisterRuntimeExecutor(fakeRuntimeExecutor{kind: capabilities.RuntimeKindScript, err: expected}); err != nil {
		t.Fatalf("RegisterRuntimeExecutor returned error: %v", err)
	}
	adapter := NewDeclarativeRuntimeAdapter(fakeBindingResolver{
		binding: capabilities.EndpointRuntimeBinding{
			RuntimeBindingID: "runtime_binding_test",
			RuntimeKind:      capabilities.RuntimeKindScript,
			Status:           capabilities.RuntimeBindingStatusActive,
		},
	}, registry)
	_, err := adapter.Execute(context.Background(), ExecutionContext{CapabilityEndpointID: "endp_test", ActiveEndpointVersionID: "endpv_test"}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	code, message, ok := RuntimeFailureCode(err)
	if !ok {
		t.Fatalf("expected typed runtime failure, got %T", err)
	}
	if code != RuntimeFailureInvalidConfig || message != "bad config" {
		t.Fatalf("failure = %q/%q, want %q/bad config", code, message, RuntimeFailureInvalidConfig)
	}
}
