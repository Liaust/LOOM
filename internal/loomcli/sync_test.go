package loomcli

import (
	"encoding/json"
	"strings"
	"testing"

	"loom.local/loom/internal/response"
)

func TestSyncStatusRequiresNodeJSON(t *testing.T) {
	stdout, stderr, err := executeRootCommand("--json", "sync", "status")
	if err == nil {
		t.Fatal("sync status without --node should return an error")
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("json error should not write stderr, got: %s", stderr)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Fatal("json error should write a response envelope to stdout")
	}
	var envelope response.ErrorEnvelope
	if decodeErr := json.Unmarshal([]byte(stdout), &envelope); decodeErr != nil {
		t.Fatalf("decode error envelope: %v output=%s", decodeErr, stdout)
	}
	if envelope.OK || envelope.Error.Code != "sync.node_required" {
		t.Fatalf("unexpected error envelope: %#v", envelope)
	}
}
