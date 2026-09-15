package routing

import (
	"testing"
	"time"

	"loom.local/loom/internal/capabilities"
)

func TestRemoteDispatchRuntimeMetadataIncludesSnapshotIdentity(t *testing.T) {
	metadata := remoteDispatchRuntimeMetadata(&RuntimeBindingSnapshot{
		RuntimeBindingID: "runtime_binding_test",
		RuntimeKind:      capabilities.RuntimeKindCommand,
		SnapshotHash:     "sha256:test",
		CapturedAt:       time.Now().UTC(),
	})
	if metadata["runtime_kind"] != capabilities.RuntimeKindCommand {
		t.Fatalf("runtime_kind = %#v", metadata["runtime_kind"])
	}
	if metadata["runtime_binding_id"] != "runtime_binding_test" {
		t.Fatalf("runtime_binding_id = %#v", metadata["runtime_binding_id"])
	}
	if metadata["runtime_snapshot_hash"] != "sha256:test" {
		t.Fatalf("runtime_snapshot_hash = %#v", metadata["runtime_snapshot_hash"])
	}
}
