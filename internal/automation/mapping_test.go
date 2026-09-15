package automation

import (
	"encoding/json"
	"testing"
)

func TestApplyMappingPreviewMapsNestedFields(t *testing.T) {
	profile := json.RawMessage(`{
		"field_mappings": {
			"url": "$.body.url",
			"message.id": "$.body.message_id",
			"source": "literal:gmail"
		},
		"defaults": {
			"priority": "normal"
		},
		"required": ["url", "message.id"]
	}`)

	out, missing, count, err := ApplyMappingPreview(profile, MappingPreviewInput{
		BodyJSON: json.RawMessage(`{"url":"https://example.com","message_id":"msg_1"}`),
	})
	if err != nil {
		t.Fatalf("ApplyMappingPreview returned error: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing = %v, want none", missing)
	}
	if count != 4 {
		t.Fatalf("count = %d, want 4", count)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is invalid JSON: %v", err)
	}
	if got["url"] != "https://example.com" {
		t.Fatalf("url = %v", got["url"])
	}
	message := got["message"].(map[string]any)
	if message["id"] != "msg_1" {
		t.Fatalf("message.id = %v", message["id"])
	}
	if got["source"] != "gmail" {
		t.Fatalf("source = %v", got["source"])
	}
	if got["priority"] != "normal" {
		t.Fatalf("priority = %v", got["priority"])
	}
}

func TestApplyMappingPreviewReportsMissingRequiredFields(t *testing.T) {
	profile := json.RawMessage(`{
		"field_mappings": {
			"url": "$.body.url"
		},
		"required": ["url", "message.id"]
	}`)

	_, missing, _, err := ApplyMappingPreview(profile, MappingPreviewInput{
		BodyJSON: json.RawMessage(`{"url":"https://example.com"}`),
	})
	if err != nil {
		t.Fatalf("ApplyMappingPreview returned error: %v", err)
	}
	if len(missing) != 1 || missing[0] != "message.id" {
		t.Fatalf("missing = %v, want [message.id]", missing)
	}
}
