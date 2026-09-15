package nodeagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	noderuntime "loom.local/loom/internal/nodeagent/runtime"
)

func TestNodeAgentDoesNotExposeDropzoneCommand(t *testing.T) {
	t.Parallel()
	for _, command := range NewRootCommand().Commands() {
		if command.Name() == "dropzone" {
			t.Fatal("retired Dropzone command remains registered")
		}
	}
}

func TestHistoricalDropzoneWorkerCannotExecute(t *testing.T) {
	t.Parallel()
	store := noderuntime.NewStore(t.TempDir())
	instance := noderuntime.WorkerInstance{
		WorkerKey:           noderuntime.WorkerKeyDropzoneTransfer,
		Kind:                noderuntime.KindDropzoneTransfer,
		DisplayName:         "Historical LOOM Box Dropzone Transfer",
		Enabled:             true,
		IntervalSeconds:     60,
		LeaseTimeoutSeconds: 3600,
		ConfigJSON:          json.RawMessage(`{"schema_version":"loom.node_agent.dropzone_worker.v0.4.2"}`),
		CreatedAt:           time.Now().UTC(),
		UpdatedAt:           time.Now().UTC(),
	}
	if err := store.SaveInstance(instance); err != nil {
		t.Fatal(err)
	}
	_, err := nodeAgentRuntimeRegistry().RunOnce(context.Background(), store, noderuntime.Env{}, instance.WorkerKey, "corr-retired-dropzone")
	if err == nil || !strings.Contains(err.Error(), "is not registered") {
		t.Fatalf("historical Dropzone worker remained executable: %v", err)
	}
	loaded, err := store.LoadInstance(instance.WorkerKey)
	if err != nil || loaded.Kind != noderuntime.KindDropzoneTransfer {
		t.Fatalf("historical worker evidence was not preserved: %#v err=%v", loaded, err)
	}
}
