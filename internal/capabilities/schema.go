package capabilities

import (
	"bytes"
	"encoding/json"
	"fmt"
)

func ValidateSchemaJSON(raw json.RawMessage) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}

	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return fmt.Errorf("schema must be a valid JSON object: %w", err)
	}
	if schema == nil {
		return fmt.Errorf("schema must be a JSON object")
	}

	return nil
}
