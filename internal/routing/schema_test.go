package routing

import (
	"encoding/json"
	"testing"
)

func TestValidationHelpersAcceptKnownValues(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{name: "route kind", ok: ValidRouteKind(RouteKindLocal)},
		{name: "execution mode", ok: ValidExecutionMode(ExecutionModeImmediate)},
		{name: "route status", ok: ValidRouteStatus(RouteStatusWaitingForApproval)},
		{name: "call status", ok: ValidCapabilityCallStatus(CapabilityCallStatusApprovalRequired)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.ok {
				t.Fatal("validation helper rejected known value")
			}
		})
	}
}

func TestValidationHelpersRejectUnknownValues(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{name: "route kind", ok: ValidRouteKind("mesh")},
		{name: "execution mode", ok: ValidExecutionMode("lease")},
		{name: "route status", ok: ValidRouteStatus("waiting_for_node")},
		{name: "call status", ok: ValidCapabilityCallStatus("waiting_for_approval")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.ok {
				t.Fatal("validation helper accepted unknown value")
			}
		})
	}
}

func TestValidateCallInputJSON(t *testing.T) {
	if err := ValidateCallInputJSON(json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatalf("ValidateCallInputJSON rejected object: %v", err)
	}
	if err := ValidateCallInputJSON(nil); err != nil {
		t.Fatalf("ValidateCallInputJSON rejected empty input: %v", err)
	}
	if err := ValidateCallInputJSON(json.RawMessage(`[]`)); err == nil {
		t.Fatal("ValidateCallInputJSON accepted array")
	}
}
