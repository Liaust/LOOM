package routing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"loom.local/loom/internal/capabilities"
)

type RuntimeBindingResolver interface {
	ActiveRuntimeBindingForEndpointVersion(ctx context.Context, versionRef string) (capabilities.EndpointRuntimeBinding, error)
}

type DeclarativeRuntimeAdapter struct {
	Resolver RuntimeBindingResolver
	Registry *ProviderRuntimeRegistry
}

func NewDeclarativeRuntimeAdapter(resolver RuntimeBindingResolver, registry *ProviderRuntimeRegistry) DeclarativeRuntimeAdapter {
	return DeclarativeRuntimeAdapter{
		Resolver: resolver,
		Registry: registry,
	}
}

func (a DeclarativeRuntimeAdapter) Execute(ctx context.Context, execCtx ExecutionContext, input json.RawMessage) (ExecutionResult, error) {
	versionID := strings.TrimSpace(execCtx.ActiveEndpointVersionID)
	if versionID == "" {
		return ExecutionResult{}, NewExecutionFailure(
			RuntimeFailureActiveVersionMissing,
			fmt.Sprintf("capability endpoint %s has no active endpoint version", execCtx.CapabilityEndpointID),
			nil,
		)
	}
	if a.Resolver == nil {
		return ExecutionResult{}, NewExecutionFailure(
			"provider.adapter_missing",
			"declarative runtime binding resolver is not configured",
			nil,
		)
	}
	if a.Registry == nil {
		return ExecutionResult{}, NewExecutionFailure(
			"provider.adapter_missing",
			"declarative runtime executor registry is not configured",
			nil,
		)
	}
	binding, err := a.Resolver.ActiveRuntimeBindingForEndpointVersion(ctx, versionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ExecutionResult{}, NewExecutionFailure(
				RuntimeFailureMissing,
				fmt.Sprintf("active endpoint version %s has no runtime binding", versionID),
				err,
			)
		}
		return ExecutionResult{}, NewExecutionFailure(
			RuntimeFailureLookupFailed,
			fmt.Sprintf("could not load runtime binding for active endpoint version %s", versionID),
			err,
		)
	}
	if binding.Status != capabilities.RuntimeBindingStatusActive {
		return ExecutionResult{}, NewExecutionFailure(
			RuntimeFailureInactive,
			fmt.Sprintf("runtime binding %s is not active: %s", binding.RuntimeBindingID, binding.Status),
			nil,
		)
	}
	executor, ok := a.Registry.RuntimeExecutor(binding.RuntimeKind)
	if !ok {
		return ExecutionResult{}, UnsupportedRuntimeExecutorFailure(binding.RuntimeKind)
	}
	return executor.Execute(ctx, execCtx, binding, input)
}
