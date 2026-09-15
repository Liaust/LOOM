package portal

import (
	"context"
	"fmt"

	"loom.local/loom/internal/loomcli/actions"
)

type ActionExecution struct {
	Action         actions.Action
	WorkerRunID    string
	WorkerKey      string
	IdempotencyKey string
}

func ExecuteAction(ctx context.Context, client Client, correlationID string, registry actions.Registry, actionID string, confirmed bool) (ActionExecution, error) {
	action, ok := registry.Get(actionID)
	if !ok || !action.Enabled {
		return ActionExecution{}, fmt.Errorf("action %q is not registered", actionID)
	}
	result, err := ExecutePortalAction(ctx, client, correlationID, PortalActionFromRegistry(action), confirmed)
	if err != nil {
		return ActionExecution{}, err
	}
	if result.Status == ActionLifecycleFailed {
		return ActionExecution{}, fmt.Errorf("%s", firstNonEmpty(result.ErrorMessage, result.Summary, "portal action failed"))
	}
	return ActionExecution{
		Action:         action,
		WorkerRunID:    resultFieldValue(result, "Run"),
		WorkerKey:      resultFieldValue(result, "Worker"),
		IdempotencyKey: result.IdempotencyKey,
	}, nil
}

func resultFieldValue(result PortalActionResult, label string) string {
	for _, field := range result.Fields {
		if field.Label == label {
			return field.Value
		}
	}
	return ""
}
