package capabilities

import (
	"encoding/json"
	"testing"
)

func TestValidateSchemaJSONAcceptsObjectsAndEmptyInput(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
	}{
		{name: "empty", raw: nil},
		{name: "blank", raw: json.RawMessage("   ")},
		{name: "empty object", raw: json.RawMessage(`{}`)},
		{name: "object schema", raw: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateSchemaJSON(tt.raw); err != nil {
				t.Fatalf("ValidateSchemaJSON returned error: %v", err)
			}
		})
	}
}

func TestValidateSchemaJSONRejectsInvalidShapes(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
	}{
		{name: "invalid json", raw: json.RawMessage(`{"type":`)},
		{name: "null", raw: json.RawMessage(`null`)},
		{name: "array", raw: json.RawMessage(`[]`)},
		{name: "string", raw: json.RawMessage(`"object"`)},
		{name: "number", raw: json.RawMessage(`42`)},
		{name: "boolean", raw: json.RawMessage(`true`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateSchemaJSON(tt.raw); err == nil {
				t.Fatal("ValidateSchemaJSON accepted invalid schema")
			}
		})
	}
}
