package agents

import (
	"encoding/json"
	"testing"
)

func TestValidationHelpersAcceptKnownValues(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{name: "access session status", ok: ValidAccessSessionStatus(AccessSessionStatusActive)},
		{name: "work context status", ok: ValidWorkContextStatus(WorkContextStatusCompleted)},
		{name: "tool view status", ok: ValidToolViewStatus(ToolViewStatusActive)},
		{name: "entry kind", ok: ValidEntryKind(EntryKindOperatingTool)},
		{name: "visibility", ok: ValidVisibilityState(VisibilityRequestable)},
		{name: "source layer", ok: ValidSourceLayer(SourceLayerOperating)},
		{name: "origin kind", ok: ValidOriginKind(OriginKindLocalCLI)},
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
		{name: "access session status", ok: ValidAccessSessionStatus("running")},
		{name: "work context status", ok: ValidWorkContextStatus("paused")},
		{name: "tool view status", ok: ValidToolViewStatus("stale")},
		{name: "entry kind", ok: ValidEntryKind("agent_memory")},
		{name: "visibility", ok: ValidVisibilityState("hidden")},
		{name: "source layer", ok: ValidSourceLayer("model")},
		{name: "origin kind", ok: ValidOriginKind("agent_runtime")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.ok {
				t.Fatal("validation helper accepted unknown value")
			}
		})
	}
}

func TestValidateObjectJSONAcceptsObjectsAndEmptyInput(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
	}{
		{name: "empty", raw: nil},
		{name: "blank", raw: json.RawMessage("   ")},
		{name: "empty object", raw: json.RawMessage(`{}`)},
		{name: "object", raw: json.RawMessage(`{"agent_owned":false}`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateObjectJSON(tt.raw, "metadata"); err != nil {
				t.Fatalf("validateObjectJSON returned error: %v", err)
			}
		})
	}
}

func TestValidateObjectJSONRejectsInvalidShapes(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
	}{
		{name: "invalid", raw: json.RawMessage(`{"agent":`)},
		{name: "null", raw: json.RawMessage(`null`)},
		{name: "array", raw: json.RawMessage(`[]`)},
		{name: "string", raw: json.RawMessage(`"x"`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateObjectJSON(tt.raw, "metadata"); err == nil {
				t.Fatal("validateObjectJSON accepted invalid shape")
			}
		})
	}
}

func TestCapabilityToolNameIsModelSafe(t *testing.T) {
	got := capabilityToolName("main@system.status.read")
	if got != "cap_main_system_status_read" {
		t.Fatalf("tool name = %q", got)
	}
}
