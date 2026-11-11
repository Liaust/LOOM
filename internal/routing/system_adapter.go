package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/health"
	loomstatus "loom.local/loom/internal/status"
)

const (
	SystemHealthReadAddress = "main@system.health.read"
	SystemStatusReadAddress = "main@system.status.read"
)

type SystemAdapter struct {
	AgentEvents ProviderAdapter
	Health      health.Service
	Status      loomstatus.Service
}

func NewSystemAdapter(healthService health.Service, statusService loomstatus.Service) SystemAdapter {
	return SystemAdapter{
		Health: healthService,
		Status: statusService,
	}
}

func (a SystemAdapter) Execute(ctx context.Context, execCtx ExecutionContext, input json.RawMessage) (ExecutionResult, error) {
	_ = input
	operation := strings.TrimPrefix(strings.TrimSpace(execCtx.Operation), capabilityOperationPrefix)
	var result any
	switch operation {
	case capabilities.HermesEventCapability:
		if a.AgentEvents == nil {
			return ExecutionResult{}, fmt.Errorf("native Hermes event owner is unavailable")
		}
		return a.AgentEvents.Execute(ctx, execCtx, input)
	case SystemHealthReadAddress:
		result = a.Health.Check(ctx)
	case SystemStatusReadAddress:
		result = a.Status.Check(ctx)
	default:
		return ExecutionResult{}, fmt.Errorf("system adapter does not support %s", operation)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return ExecutionResult{}, err
	}
	return ExecutionResult{
		Status:     CapabilityCallStatusCompleted,
		Result:     json.RawMessage(raw),
		ResultRefs: json.RawMessage(`{}`),
	}, nil
}
