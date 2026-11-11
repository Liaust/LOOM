package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/response"
	loomsync "loom.local/loom/internal/sync"
)

func TestSupersededWatchedBlobRetainedAfterCurrentAcknowledgement(t *testing.T) {
	for _, records := range []bool{false, true} {
		for _, streamed := range []bool{false, true} {
			name := "legacy-inline"
			if records {
				name = "records-inline"
			}
			if streamed {
				name += "-stream"
			}
			t.Run(name, func(t *testing.T) {
				store, config, state, dir := localSyncObjectTestStore(t)
				p := filepath.Join(dir, "changing.pdf")
				oldBytes, currentBytes := []byte("older version"), []byte("current version")
				if streamed {
					oldBytes = bytes.Repeat([]byte("a"), loomsync.MaxInlineObjectUploadBytes+1)
					currentBytes = bytes.Repeat([]byte("b"), len(oldBytes))
				}
				if err := os.WriteFile(p, oldBytes, 0600); err != nil {
					t.Fatal(err)
				}
				action := watchedroots.OutputAction{RootKey: "notebooks", RelativePath: "changing.pdf", ProjectRef: "same-project", SourcePath: "watched-root://notebooks/changing.pdf"}
				old, oldItem, err := store.QueueWatchedRootObject(config, state, action, p)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, currentBytes, 0600); err != nil {
					t.Fatal(err)
				}
				action.LocalObjectID = old.LocalObjectID
				current, currentItem, err := store.QueueWatchedRootObject(config, state, action, p)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.ApplySyncedObjectResult(loomsync.SyncedObjectResult{ObjectID: "main-object", VersionID: "main-current", Item: loomsync.SyncItemResult{
					LocalRef: current.LocalObjectID, ItemKind: currentItem.ItemKind, Status: localSyncStatusAccepted, Metadata: localSyncItemMetadata(nil, currentItem),
				}}); err != nil {
					t.Fatal(err)
				}
				watchedStore := watchedroots.NewStore(store.DataDir)
				if err := watchedStore.SavePathState(watchedroots.PathState{RootKey: "notebooks", RelativePath: "changing.pdf", LocalObjectID: current.LocalObjectID, LocalVersionID: current.LocalVersionID,
					SyncStatus: localSyncStatusPending, LastOutputErrorCode: "sync.upload_unavailable", MainObjectID: "main-object", MainVersionID: "main-current", LastSyncedHashURI: current.HashURI}); err != nil {
					t.Fatal(err)
				}
				if records {
					if _, err := store.MigrateQueueRecords(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
				server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("overwritten revision was uploaded") }))
				defer server.Close()
				config.MainURL = server.URL
				// Use the normal scoped producer: its record view must include the
				// acknowledged successor, not just the pending old version.
				if _, err := pushLocalSyncRootOnce(t.Context(), store, config, state, "test", 100, false, "notebooks"); err != nil {
					t.Fatal(err)
				}
				items, err := store.LoadSyncOutbox()
				if err != nil || len(items) != 2 {
					t.Fatalf("history: %d %v", len(items), err)
				}
				if got := items[0]; got.Status != localSyncStatusRetainedSuperseded || got.SyncedAt != nil || got.GlobalRef != "" ||
					!bytes.Equal(got.PayloadJSON, oldItem.PayloadJSON) || got.PayloadHash != oldItem.PayloadHash ||
					got.LocalSequence != oldItem.LocalSequence || !got.CreatedAt.Equal(oldItem.CreatedAt) || !strings.Contains(got.LastErrorMessage, current.LocalVersionID) {
					t.Fatalf("old evidence changed or falsely acknowledged: %+v", got)
				}
				objects, err := store.LoadSyncObjects()
				if err != nil || len(objects) != 2 || objects[0].HashURI != old.HashURI || objects[0].MainVersionID != "" || objects[1].MainVersionID != "main-current" {
					t.Fatal("object history changed", err)
				}
				counts, err := store.localSyncRootStatus(config, state, "notebooks")
				if err != nil || counts.Counts.Pending != 0 || counts.Counts.Accepted != 1 || counts.Counts.RetainedSuperseded != 1 {
					t.Fatalf("counts: %+v %v", counts.Counts, err)
				}
				if len(eligibleOutboxItems(items, false)) != 0 {
					t.Fatal("superseded history retried")
				}
				pathState, err := watchedStore.LoadPathState("notebooks", "changing.pdf")
				if err != nil || pathState.LocalVersionID != current.LocalVersionID || pathState.SyncStatus != localSyncStatusAccepted || pathState.LastSyncedHashURI != current.HashURI {
					t.Fatal("current path was rewound", err)
				}
				if _, err := pushLocalSyncRootOnce(t.Context(), store, config, state, "test", 100, false, "notebooks"); err != nil {
					t.Fatal(err)
				}
				if got, err := os.ReadFile(p); err != nil || !bytes.Equal(got, currentBytes) {
					t.Fatal("current bytes changed", err)
				}
			})
		}
	}
}

func TestSupersededWatchedBlobRequiresExactAcceptedSuccessor(t *testing.T) {
	store, config, state, dir := localSyncObjectTestStore(t)
	p := filepath.Join(dir, "note.md")
	if err := os.WriteFile(p, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	old, item, err := store.QueueWatchedRootObject(config, state, watchedroots.OutputAction{RootKey: "notes", RelativePath: "note.md", ProjectRef: "p"}, p)
	if err != nil {
		t.Fatal(err)
	}
	current := old
	current.LocalVersionID = "next-version"
	current.LocalSequence++
	current.HashURI = "sha256:current"
	current.SyncStatus, current.MainObjectID, current.MainVersionID = localSyncStatusAccepted, "main-object", "main-version"
	current.SyncedAt = &current.CreatedAt
	changed := syncSourceContentChanged{Path: p, Expected: old.HashURI, Actual: current.HashURI}
	cases := []struct {
		name   string
		mutate func(*LocalSyncObject)
		err    error
	}{
		{"pending", func(c *LocalSyncObject) { c.SyncStatus = localSyncStatusPending }, changed},
		{"other-hash", func(c *LocalSyncObject) { c.HashURI = "sha256:other" }, changed},
		{"other-path", func(c *LocalSyncObject) { c.ContentPath += "-other" }, changed},
		{"other-scope", func(c *LocalSyncObject) { c.ProjectRef = "other" }, changed},
		{"other-root", func(c *LocalSyncObject) {
			c.Metadata = objectJSON(map[string]any{"watched_root": "other", "relative_path": "note.md"})
		}, changed},
		{"no-ack", func(c *LocalSyncObject) { c.SyncedAt = nil }, changed},
		{"transport", func(*LocalSyncObject) {}, errors.New("connection failed")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := current
			tc.mutate(&candidate)
			if retained, err := store.retainSupersededSyncUpload(item, old, map[string]LocalSyncObject{"candidate": candidate}, tc.err); err != nil || retained {
				t.Fatalf("unverified replacement admitted: %v %v", retained, err)
			}
		})
	}
	items, err := store.LoadSyncOutbox()
	if err != nil || len(items) != 1 || items[0].Status != localSyncStatusPending {
		t.Fatal("pending history changed", err)
	}
}

func TestQueuedMissingNotesJournalIsRetainedWithoutUploadOrFalseAck(t *testing.T) {
	for _, records := range []bool{false, true} {
		name := "legacy"
		if records {
			name = "records"
		}
		t.Run(name, func(t *testing.T) {
			store, config, state, dir := localSyncObjectTestStore(t)
			p := filepath.Join(dir, ".loom-notes-"+strings.Repeat("a", 64), "original")
			if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("retained source"), 0600); err != nil {
				t.Fatal(err)
			}
			object, err := store.CreateLocalSyncObject(config, state, LocalSyncObjectCreateInput{Path: p, ScopeRef: "scope:notes"})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if records {
				if _, err := store.MigrateQueueRecords(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("internal journal was uploaded") }))
			defer server.Close()
			config.MainURL = server.URL
			if _, err := pushLocalSyncOnce(t.Context(), store, config, state, "test", 100, false); err != nil {
				t.Fatal(err)
			}
			items, err := store.LoadSyncOutbox()
			if err != nil || len(items) != 1 || items[0].Status != localSyncStatusRetainedInternal || items[0].SyncedAt != nil || items[0].GlobalRef != "" {
				t.Fatalf("false acknowledgement or lost record: %+v %v", items, err)
			}
			if got, err := store.ResolveLocalSyncObject(object.LocalObjectID); err != nil || got.HashURI != object.HashURI {
				t.Fatal("retained object history changed", err)
			}
			if len(eligibleOutboxItems(items, true)) != 0 {
				t.Fatal("internal history retried")
			}
			status, err := store.LocalSyncStatus(config, state)
			if err != nil || status.Counts.Pending != 0 || status.Counts.RetainedInternal != 1 {
				t.Fatalf("incorrect status: %+v %v", status.Counts, err)
			}
		})
	}
}

func TestSyncMissingProjectRetainedFailureAndExplicitRetry(t *testing.T) {
	store, config, state, dir := localSyncObjectTestStore(t)
	p := filepath.Join(dir, "retained.md")
	if err := os.WriteFile(p, []byte("retained source"), 0o600); err != nil {
		t.Fatal(err)
	}
	o, err := store.CreateLocalSyncObject(config, state, LocalSyncObjectCreateInput{Path: p, ProjectRef: "missing-project"})
	if err != nil {
		t.Fatal(err)
	}
	objects, err := store.LoadSyncObjects()
	if err != nil {
		t.Fatal(err)
	}
	objects[0].Metadata = objectJSON(map[string]any{"watched_root": "old-root"})
	if err := store.SaveSyncObjects(objects); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(response.ErrorEnvelope{Error: response.ErrorBody{Code: "sync.project_not_found", Summary: "Project does not exist."}})
	}))
	defer server.Close()
	config.MainURL = server.URL
	ctx := context.Background()
	if _, err := pushLocalSyncOnce(ctx, store, config, state, "test", 100, false); err == nil {
		t.Fatal("missing project was accepted")
	}
	items, err := store.LoadSyncOutbox()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != localSyncStatusFailed || items[0].LastErrorCode != "sync.project_not_found" {
		t.Fatalf("failure not retained: %+v", items)
	}
	failed, err := store.ResolveLocalSyncObject(o.LocalObjectID)
	if err != nil || failed.SyncStatus != localSyncStatusFailed || failed.ProjectRef != o.ProjectRef || failed.HashURI != o.HashURI {
		t.Fatalf("object changed: %v", err)
	}
	if _, err := pushLocalSyncOnce(ctx, store, config, state, "test", 100, false); err != nil || attempts != 1 {
		t.Fatalf("failed item automatically retried: %d, %v", attempts, err)
	}
	if got := flushWatchedRootOutputs(ctx, store, config, state, "test", "other-root"); got.Status != watchedroots.OutputStatusAlreadyCurrent || got.FailedAfter != 0 {
		t.Fatalf("unrelated root contaminated: %+v", got)
	}
	if got := flushWatchedRootOutputs(ctx, store, config, state, "test", "old-root"); got.Status != watchedroots.OutputStatusFailed || got.FailedAfter != 1 {
		t.Fatalf("own failure hidden: %+v", got)
	}
	if _, err := store.retrySyncObject(ctx, items[0].LocalOutboxID); err != nil {
		t.Fatal(err)
	}
	if _, err := pushLocalSyncOnce(ctx, store, config, state, "test", 100, false); err == nil || attempts != 2 {
		t.Fatalf("explicit retry not attempted: %d, %v", attempts, err)
	}
	if raw, err := os.ReadFile(p); err != nil || string(raw) != "retained source" {
		t.Fatal("source changed")
	}
}

func TestSyncWatchedRootFlushDoesNotSubmitForeignPendingObject(t *testing.T) {
	store, config, state, dir := localSyncObjectTestStore(t)
	if err := os.WriteFile(filepath.Join(dir, "foreign.md"), []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateLocalSyncObject(config, state, LocalSyncObjectCreateInput{Path: filepath.Join(dir, "foreign.md"), ProjectRef: "missing"}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("foreign queue item was submitted") }))
	defer server.Close()
	config.MainURL = server.URL
	if got := flushWatchedRootOutputs(context.Background(), store, config, state, "test", "current-root"); got.Status != watchedroots.OutputStatusAlreadyCurrent {
		t.Fatalf("flush: %+v", got)
	}
	items, err := store.LoadSyncOutbox()
	if err != nil || len(items) != 1 || items[0].Status != localSyncStatusPending || items[0].LastAttemptAt != nil {
		t.Fatal("foreign queue changed")
	}
}

func TestSyncRootFilterUsesExactObjectVersionAndMetadataOwner(t *testing.T) {
	a := LocalSyncObject{LocalObjectID: "o", LocalVersionID: "v1", Metadata: objectJSON(map[string]any{"watched_root": "a"})}
	b := LocalSyncObject{LocalObjectID: "o", LocalVersionID: "v2", Metadata: objectJSON(map[string]any{"watched_root": "b"})}
	items := []LocalSyncOutboxItem{
		{LocalOutboxID: "a", LocalRef: "o", ItemKind: loomsync.ItemKindObjectBlob, PayloadJSON: localObjectPayload(a)},
		{LocalOutboxID: "b", LocalRef: "o", ItemKind: loomsync.ItemKindObjectBlob, PayloadJSON: localObjectPayload(b)},
		{LocalOutboxID: "metadata", LocalRef: "m", ItemKind: loomsync.ItemKindObjectMetadata, PayloadJSON: objectJSON(map[string]any{"local_object_ref": "o", "local_version_ref": "v1"})},
		{LocalOutboxID: "deletion", ItemKind: loomsync.ItemKindDeletionRequest, PayloadJSON: objectJSON(map[string]any{"root_key": "a"})},
	}
	got := syncOutboxForRoot(items, map[string]LocalSyncObject{"o": b}, map[string]LocalSyncObject{localObjectVersionKey("o", "v1"): a, localObjectVersionKey("o", "v2"): b}, "a")
	if len(got) != 3 || got[0].LocalOutboxID != "a" || got[1].LocalOutboxID != "metadata" || got[2].LocalOutboxID != "deletion" {
		t.Fatalf("wrong root: %+v", got)
	}
}
