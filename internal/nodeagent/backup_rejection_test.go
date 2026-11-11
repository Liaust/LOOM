package nodeagent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/response"
	mainwatchedroots "loom.local/loom/internal/watchedroots"
)

func TestRetainedDeletionWithoutHashHasExplicitQualification(t *testing.T) {
	item := LocalWatchedRootBackupItem{LocalItemID: "item", ItemKind: "deletion_marker"}
	for _, hash := range []string{"", "sha256:" + rawBytesHashHex([]byte("old"))} {
		item.PreviousHashURI = hash
		var metadata map[string]any
		input := buildWatchedRootBackupBatchInput(State{}, LocalWatchedRootBackupBatch{LocalBatchID: "batch"}, []LocalWatchedRootBackupItem{item})
		if err := json.Unmarshal(input.Items[0].Metadata, &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata["prior_content_unavailable"] != (hash == "") || input.Items[0].PreviousHashURI != hash {
			t.Fatalf("incorrect prior-content evidence: %#v", input.Items[0])
		}
	}
}

func TestBackupRejectedBatchDoesNotStarveLaterWork(t *testing.T) {
	for _, records := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "records"}[records], func(t *testing.T) {
			store := Store{DataDir: filepath.Join(t.TempDir(), "data")}
			if records {
				if _, err := store.MigrateQueueRecords(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			config := Config{NodeKey: "test"}
			state := State{NodeID: "node_test", CredentialToken: "test-token"}
			now := time.Now().UTC()
			for _, name := range []string{"bad.md", "good.md"} {
				_, _, err := store.QueueWatchedRootBackupAction(config, state, watchedroots.OutputAction{
					RootKey: "notes", RelativePath: name, ActionKind: watchedroots.OutputActionBackupDeletionMarker,
					BackupItemKind: "deletion_marker", BackupMode: "incremental_raw", DeletedAt: &now,
				}, "")
				if err != nil {
					t.Fatal(err)
				}
			}
			badCalls, goodCalls := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/node-agent/watched-roots/backup-batches" {
					t.Errorf("unexpected request %s", r.URL.Path)
					http.Error(w, "unexpected", 500)
					return
				}
				var input mainwatchedroots.BackupBatchInput
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Error(err)
					return
				}
				if input.Items[0].RelativePath == "bad.md" {
					badCalls++
					http.Error(w, "permanent validation failure", http.StatusBadRequest)
					return
				}
				goodCalls++
				_ = json.NewEncoder(w).Encode(response.Success("corr", mainwatchedroots.BackupBatchResult{
					Batch: mainwatchedroots.BackupBatch{WatchedRootBackupBatchID: "accepted-batch", Status: "accepted"},
					Items: []mainwatchedroots.BackupItem{{LocalItemRef: input.Items[0].LocalItemRef,
						WatchedRootBackupItemID: "accepted-item", Status: "accepted"}},
				}))
			}))
			defer server.Close()
			config.MainURL = server.URL
			for range 2 {
				if _, err := pushLocalWatchedRootBackupsOnce(t.Context(), store, config, state, "corr"); err != nil {
					t.Fatal(err)
				}
			}
			status, err := store.LocalWatchedRootBackupStatus(config, state, "notes")
			if err != nil {
				t.Fatal(err)
			}
			if badCalls != 1 || goodCalls != 1 || status.Counts.Pending != 0 || status.Counts.ManualAction != 1 || status.Counts.Accepted != 1 {
				t.Fatalf("calls bad=%d good=%d counts=%+v", badCalls, goodCalls, status.Counts)
			}
		})
	}
}

func TestBackupBindingRepairStopsAfterOneRetry(t *testing.T) {
	store := Store{DataDir: filepath.Join(t.TempDir(), "data")}
	if _, err := store.MigrateQueueRecords(t.Context()); err != nil {
		t.Fatal(err)
	}
	config := Config{NodeKey: "test"}
	state := State{NodeID: "node_test", CredentialToken: "test-token"}
	now := time.Now().UTC()
	if _, _, err := store.QueueWatchedRootBackupAction(config, state, watchedroots.OutputAction{
		RootKey: "notes", RelativePath: "old.md", ActionKind: watchedroots.OutputActionBackupDeletionMarker,
		BackupItemKind: "deletion_marker", BackupMode: "incremental_raw", DeletedAt: &now,
	}, ""); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "identity mismatch", http.StatusBadRequest)
	}))
	defer server.Close()
	config.MainURL = server.URL
	if _, err := pushLocalWatchedRootBackupsOnce(t.Context(), store, config, state, "corr"); err == nil {
		t.Fatal("first binding refusal must retain retryable failure")
	}
	for range 2 {
		if _, err := pushLocalWatchedRootBackupsOnce(t.Context(), store, config, state, "corr"); err != nil {
			t.Fatal(err)
		}
	}
	status, err := store.LocalWatchedRootBackupStatus(config, state, "notes")
	if err != nil || calls != 2 || status.Counts.ManualAction != 1 || status.Counts.Pending != 0 {
		t.Fatalf("binding retry loop: calls=%d status=%+v err=%v", calls, status, err)
	}
}
