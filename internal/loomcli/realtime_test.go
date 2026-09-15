package loomcli

import (
	"encoding/json"
	"strings"
	"testing"

	"loom.local/loom/internal/response"
)

func TestRealtimeTopicCreateValidatesRetentionModeLocally(t *testing.T) {
	stdout, stderr, err := executeRootCommand(
		"--json",
		"realtime", "topic", "create", "test/topic",
		"--retention", "retained",
		"--delivery-class", "polling",
	)
	if err == nil {
		t.Fatal("invalid realtime retention should return an error")
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("json error should not write stderr, got: %s", stderr)
	}
	var envelope response.ErrorEnvelope
	if decodeErr := json.Unmarshal([]byte(stdout), &envelope); decodeErr != nil {
		t.Fatalf("decode error response: %v output=%s", decodeErr, stdout)
	}
	if envelope.Error.Code != "realtime.retention_mode_invalid" {
		t.Fatalf("error code = %q, want realtime.retention_mode_invalid", envelope.Error.Code)
	}
	if !strings.Contains(envelope.Error.Summary, "retain_bounded") {
		t.Fatalf("error summary missing allowed values: %q", envelope.Error.Summary)
	}
}

func TestRealtimeTopicCreateValidatesDeliveryClassLocally(t *testing.T) {
	stdout, stderr, err := executeRootCommand(
		"--json",
		"realtime", "topic", "create", "test/topic",
		"--retention", "retain_bounded",
		"--delivery-class", "local",
	)
	if err == nil {
		t.Fatal("invalid realtime delivery class should return an error")
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("json error should not write stderr, got: %s", stderr)
	}
	var envelope response.ErrorEnvelope
	if decodeErr := json.Unmarshal([]byte(stdout), &envelope); decodeErr != nil {
		t.Fatalf("decode error response: %v output=%s", decodeErr, stdout)
	}
	if envelope.Error.Code != "realtime.delivery_class_invalid" {
		t.Fatalf("error code = %q, want realtime.delivery_class_invalid", envelope.Error.Code)
	}
	if !strings.Contains(envelope.Error.Summary, "actor_inbox") {
		t.Fatalf("error summary missing allowed values: %q", envelope.Error.Summary)
	}
}

func TestRealtimeTopicCreateHelpListsEnumValues(t *testing.T) {
	stdout, stderr, err := executeRootCommand("realtime", "topic", "create", "--help")
	if err != nil {
		t.Fatalf("realtime topic create --help returned error: %v stderr=%s", err, stderr)
	}
	for _, want := range []string{
		"retain_latest",
		"retain_bounded",
		"durable_event_only",
		"polling",
		"actor_inbox",
		"main_outbox",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("help missing %q:\n%s", want, stdout)
		}
	}
}
