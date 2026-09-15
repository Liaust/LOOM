package workers

import (
	"encoding/json"
	"fmt"
	"strings"
)

func JSONObject(raw json.RawMessage, field string) (json.RawMessage, error) {
	return normalizeJSONObject(raw, field)
}

func rawJSONObjectOrDefault(raw json.RawMessage) json.RawMessage {
	normalized, err := normalizeJSONObject(raw, "json")
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return normalized
}

func marshalJSONObject(value any, field string) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal %s: %w", ErrInvalid, field, err)
	}
	return normalizeJSONObject(raw, field)
}

func errorJSON(err error) json.RawMessage {
	if err == nil {
		return json.RawMessage(`{}`)
	}
	raw, marshalErr := marshalJSONObject(map[string]any{
		"schema_version": "worker_error.v0.2",
		"message":        strings.TrimSpace(err.Error()),
	}, "error_json")
	if marshalErr != nil {
		return json.RawMessage(`{"schema_version":"worker_error.v0.2","message":"worker error"}`)
	}
	return raw
}
