package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

type ProviderAdapter interface {
	Execute(ctx context.Context, execCtx ExecutionContext, input json.RawMessage) (ExecutionResult, error)
}

type ProviderRuntimeRegistry struct {
	mu        sync.RWMutex
	adapters  map[string]ProviderAdapter
	executors map[string]RuntimeExecutor
}

func NewProviderRuntimeRegistry() *ProviderRuntimeRegistry {
	return &ProviderRuntimeRegistry{
		adapters:  map[string]ProviderAdapter{},
		executors: map[string]RuntimeExecutor{},
	}
}

func (r *ProviderRuntimeRegistry) Register(ref string, adapter ProviderAdapter) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return fmt.Errorf("provider adapter ref is required")
	}
	if adapter == nil {
		return fmt.Errorf("provider adapter is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[ref] = adapter
	return nil
}

func (r *ProviderRuntimeRegistry) Adapter(ref string) (ProviderAdapter, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" || r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	adapter, ok := r.adapters[ref]
	return adapter, ok
}

func (r *ProviderRuntimeRegistry) RegisterRuntimeExecutor(executor RuntimeExecutor) error {
	if executor == nil {
		return fmt.Errorf("runtime executor is required")
	}
	kind := strings.TrimSpace(executor.RuntimeKind())
	if kind == "" {
		return fmt.Errorf("runtime executor kind is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.executors[kind]; exists {
		return fmt.Errorf("runtime executor already registered for %s", kind)
	}
	r.executors[kind] = executor
	return nil
}

func (r *ProviderRuntimeRegistry) RuntimeExecutor(kind string) (RuntimeExecutor, bool) {
	kind = strings.TrimSpace(kind)
	if kind == "" || r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	executor, ok := r.executors[kind]
	return executor, ok
}
