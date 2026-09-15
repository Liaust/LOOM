package routing

import (
	"encoding/json"
	"fmt"
	"strings"
)

func ValidRouteKind(value string) bool {
	switch value {
	case RouteKindLocal, RouteKindRemote:
		return true
	default:
		return false
	}
}

func ValidExecutionMode(value string) bool {
	switch value {
	case ExecutionModeImmediate, ExecutionModeJob, ExecutionModeSession, ExecutionModeStream:
		return true
	default:
		return false
	}
}

func ValidRouteStatus(value string) bool {
	switch value {
	case RouteStatusPlanned,
		RouteStatusAuthorized,
		RouteStatusWaitingForApproval,
		RouteStatusQueued,
		RouteStatusDispatched,
		RouteStatusExecuting,
		RouteStatusCompleted,
		RouteStatusFailed,
		RouteStatusCancelled,
		RouteStatusExpired:
		return true
	default:
		return false
	}
}

func ValidCapabilityCallStatus(value string) bool {
	switch value {
	case CapabilityCallStatusPlanned,
		CapabilityCallStatusApprovalRequired,
		CapabilityCallStatusAuthorized,
		CapabilityCallStatusDispatched,
		CapabilityCallStatusExecuting,
		CapabilityCallStatusCompleted,
		CapabilityCallStatusFailed,
		CapabilityCallStatusCancelled:
		return true
	default:
		return false
	}
}

func ValidateCallInputJSON(raw json.RawMessage) error {
	return validateJSONObject(raw, "input")
}

func validateJSONObject(raw json.RawMessage, name string) error {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return nil
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return fmt.Errorf("%s must be valid JSON object: %w", name, err)
	}
	if object == nil {
		return fmt.Errorf("%s must be a JSON object", name)
	}
	return nil
}
