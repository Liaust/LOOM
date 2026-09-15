package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"loom.local/loom/internal/health"
	loomstatus "loom.local/loom/internal/status"
)

const (
	SystemHealthReadAddress = "main@system.health.read"
	SystemStatusReadAddress = "main@system.status.read"
)

type SystemAdapter struct {
	Health health.Service
	Status loomstatus.Service
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
