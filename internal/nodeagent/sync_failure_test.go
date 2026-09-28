package nodeagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/response"
	loomsync "loom.local/loom/internal/sync"
)

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
