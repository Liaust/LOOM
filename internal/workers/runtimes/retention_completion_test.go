package runtimes

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/workers"
)

func TestRetentionCompletionRequiresCommittedSuccessfulCloud(t *testing.T) {
	root := t.TempDir()
	runtime := CloudSnapshotUploadRuntime{DataDir: root}
	request := filepath.Join(root, "backups/local-retention-request.json")
	run := workers.WorkerRun{WorkerRunID: "completed-fixture", RunStatus: workers.RunStatusFailed, ResultSummaryJSON: json.RawMessage(`{"status":"succeeded","committed":true,"archive":"history-fixture","manifest_sha256":"` + strings.Repeat("a", 64) + `"}`)}
	if err := runtime.AfterRunCompletion(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(request); !os.IsNotExist(err) {
		t.Fatal("failed run requested retention")
	}
	run.RunStatus = workers.RunStatusSucceeded
	if err := runtime.AfterRunCompletion(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(request); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(request)
	if err := (MainBackupRuntime{DataDir: root}).AfterRunCompletion(context.Background(), workers.WorkerRun{RunStatus: workers.RunStatusSucceeded, ResultSummaryJSON: json.RawMessage(`{"committed":false}`)}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(request)
	if string(before) != string(after) {
		t.Fatal("uncommitted backup woke retention")
	}
	run.ResultSummaryJSON = json.RawMessage(`{"status":"failed","committed":false}`)
	if err := runtime.AfterRunCompletion(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	after, _ = os.ReadFile(request)
	if string(before) != string(after) {
		t.Fatal("failed cloud verification rewrote retention request")
	}
}
