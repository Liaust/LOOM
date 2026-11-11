package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"loom.local/loom/internal/response"
	"loom.local/loom/internal/storagecatalog"
	loomsync "loom.local/loom/internal/sync"
)

func TestPushLocalSyncContinuesAfterIndependentObjectUploadFailure(t *testing.T) {
	t.Parallel()
	store, config, state, dir := localSyncObjectTestStore(t)
	for name, content := range map[string]string{
		"archived-project.md": "preserved archived project object\n",
		"box-notes.md":        "later valid Box Notes object\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	archived, err := store.CreateLocalSyncObject(config, state, LocalSyncObjectCreateInput{
		Path:       filepath.Join(dir, "archived-project.md"),
		ProjectRef: "project_archived",
	})
	if err != nil {
		t.Fatalf("CreateLocalSyncObject archived: %v", err)
	}
	valid, err := store.CreateLocalSyncObject(config, state, LocalSyncObjectCreateInput{
		Path:     filepath.Join(dir, "box-notes.md"),
		ScopeRef: "box-notes",
	})
	if err != nil {
		t.Fatalf("CreateLocalSyncObject valid: %v", err)
	}

	var attempted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/node-agent/sync/object-upload" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		var input loomsync.SyncedObjectInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode object input: %v", err)
		}
		attempted = append(attempted, input.LocalObjectRef)
		if input.ProjectRef == "project_archived" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(response.ErrorEnvelope{
				OK: false,
				Error: response.ErrorBody{
					Code:          "sync.object_upload_failed",
					Summary:       "Could not resolve archived project.",
					CorrelationID: "corr_test",
				},
				Meta: response.NewMeta("corr_test"),
			})
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(response.Success("corr_test", loomsync.SyncedObjectResult{
			Item: loomsync.SyncItemResult{
				LocalRef:      input.LocalObjectRef,
				ItemKind:      loomsync.ItemKindObjectBlob,
				StreamName:    loomsync.StreamObjectBlobs,
				LocalSequence: input.LocalSequence,
				Status:        loomsync.ItemStatusAccepted,
				GlobalRef:     "knowledge_object_valid",
				Metadata:      input.Metadata,
			},
			ObjectID:  "knowledge_object_valid",
			VersionID: "knowledge_object_version_valid",
			HashURI:   input.HashURI,
		}))
	}))
	defer server.Close()
	config.MainURL = server.URL

	run, err := pushLocalSyncOnce(context.Background(), store, config, state, "corr_test", 100, false)
	if err == nil || !strings.Contains(err.Error(), archived.LocalObjectID) {
		t.Fatalf("expected preserved archived-project failure, got run=%#v err=%v", run, err)
	}
	if len(attempted) != 2 || attempted[0] != archived.LocalObjectID || attempted[1] != valid.LocalObjectID {
		t.Fatalf("object attempts = %#v, want archived then valid", attempted)
	}
	if run.SubmittedItems != 1 || len(run.ObjectUploads) != 1 || run.ObjectUploads[0].ObjectID != "knowledge_object_valid" {
		t.Fatalf("successful progress not reported: %#v", run)
	}
	if run.Persistence.SnapshotWrites != 2 || run.Persistence.CommitPages != 1 {
		t.Fatalf("mixed acknowledgements rewrote state per result: %+v", run.Persistence)
	}
	outbox, err := store.LoadSyncOutbox()
	if err != nil {
		t.Fatalf("LoadSyncOutbox: %v", err)
	}
	statusByRef := map[string]string{}
	for _, item := range outbox {
		statusByRef[item.LocalRef] = item.Status
		if item.LastAttemptAt == nil {
			t.Fatalf("outbox item %s did not retain attempt evidence", item.LocalRef)
		}
	}
	if got := statusByRef[archived.LocalObjectID]; got != localSyncStatusPending {
		t.Fatalf("archived object status = %q, want pending", got)
	}
	if got := statusByRef[valid.LocalObjectID]; got != loomsync.ItemStatusAccepted {
		t.Fatalf("valid object status = %q, want accepted", got)
	}
}

func TestCreateLocalSyncObjectAcceptsLargerDocument(t *testing.T) {
	store, config, state, dir := localSyncObjectTestStore(t)
	path := filepath.Join(dir, "scan.pdf")
	content := bytes.Repeat([]byte("%PDF-data\n"), 700000)
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	object, err := store.CreateLocalSyncObject(config, state, LocalSyncObjectCreateInput{Path: path, ProjectRef: "project_test"})
	if err != nil {
		t.Fatal(err)
	}
	if object.SizeBytes != int64(len(content)) || object.HashURI != "sha256:"+rawBytesHashHex(content) {
		t.Fatalf("large document identity lost: size=%d hash=%s", object.SizeBytes, object.HashURI)
	}
}

func TestCreateLocalSyncObjectClassifiesLargeMarkdownMetadataOnly(t *testing.T) {
	t.Parallel()
	store, config, state, dir := localSyncObjectTestStore(t)
	sourcePath := filepath.Join(dir, "large.md")
	content := append([]byte("# Large\n"), bytes.Repeat([]byte("x"), storagecatalog.DefaultMaxIndexBytes+1)...)
	if err := os.WriteFile(sourcePath, content, 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	object, err := store.CreateLocalSyncObject(config, state, LocalSyncObjectCreateInput{
		Path:       sourcePath,
		ProjectRef: "project_test",
	})
	if err != nil {
		t.Fatalf("CreateLocalSyncObject failed: %v", err)
	}
	if object.FileClass != storagecatalog.FileClassMarkdown ||
		object.ClassificationSource != storagecatalog.ClassificationSourceExtension ||
		object.IndexingState != storagecatalog.IndexingStateTooLarge ||
		object.IndexPolicy != loomsync.IndexPolicyMetadataOnly {
		t.Fatalf("unexpected classification: %#v", object)
	}
}

func TestCreateLocalSyncObjectAllowsZeroByteMarkdown(t *testing.T) {
	t.Parallel()
	store, config, state, dir := localSyncObjectTestStore(t)
	sourcePath := filepath.Join(dir, "empty.md")
	if err := os.WriteFile(sourcePath, nil, 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	object, err := store.CreateLocalSyncObject(config, state, LocalSyncObjectCreateInput{
		Path:       sourcePath,
		ProjectRef: "project_test",
	})
	if err != nil {
		t.Fatalf("CreateLocalSyncObject failed: %v", err)
	}
	if object.FileClass != storagecatalog.FileClassMarkdown ||
		object.IndexingState != storagecatalog.IndexingStateIndexed ||
		object.HashURI != "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("unexpected zero-byte object: %#v", object)
	}
}

func TestCreateLocalSyncObjectMarksGeneratedMetadata(t *testing.T) {
	t.Parallel()
	store, config, state, dir := localSyncObjectTestStore(t)
	sourcePath := filepath.Join(dir, ".DS_Store")
	if err := os.WriteFile(sourcePath, []byte("generated"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	object, err := store.CreateLocalSyncObject(config, state, LocalSyncObjectCreateInput{
		Path:       sourcePath,
		ProjectRef: "project_test",
	})
	if err != nil {
		t.Fatalf("CreateLocalSyncObject failed: %v", err)
	}
	if object.FileClass != storagecatalog.FileClassGeneratedMetadata ||
		object.IndexingState != storagecatalog.IndexingStateGeneratedIgnored ||
		object.IndexPolicy != loomsync.IndexPolicyMetadataOnly {
		t.Fatalf("unexpected generated metadata object: %#v", object)
	}
}

func TestFileChangedDuringReadGuard(t *testing.T) {
	t.Parallel()
	now := time.Now()
	before := fakeFileInfo{size: 10, modTime: now}
	same := fakeFileInfo{size: 10, modTime: now}
	changedSize := fakeFileInfo{size: 11, modTime: now}
	changedTime := fakeFileInfo{size: 10, modTime: now.Add(time.Second)}

	if fileChangedDuringRead(before, same) {
		t.Fatal("unchanged file should not trip live-mutation guard")
	}
	if !fileChangedDuringRead(before, changedSize) {
		t.Fatal("size change should trip live-mutation guard")
	}
	if !fileChangedDuringRead(before, changedTime) {
		t.Fatal("mtime change should trip live-mutation guard")
	}
}

func localSyncObjectTestStore(t *testing.T) (Store, Config, State, string) {
	t.Helper()
	dir := t.TempDir()
	store := Store{
		ConfigPath: filepath.Join(dir, "config", "config.json"),
		StatePath:  filepath.Join(dir, "state", "state.json"),
		DataDir:    filepath.Join(dir, "data"),
	}
	config := Config{
		MainURL:     "http://10.44.0.2:8080",
		NodeKey:     "workspace-test",
		DisplayName: "Workspace Test",
	}
	state := State{
		NodeID:          "node_test",
		CredentialToken: "node_cred_test",
	}
	return store, config, state, dir
}

type fakeFileInfo struct {
	size    int64
	modTime time.Time
}

func (f fakeFileInfo) Name() string       { return "file" }
func (f fakeFileInfo) Size() int64        { return f.size }
func (f fakeFileInfo) Mode() os.FileMode  { return 0o600 }
func (f fakeFileInfo) ModTime() time.Time { return f.modTime }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }

func TestSyncQueueBatchAckLockPreservesConcurrentTerminalResult(t *testing.T) {
	store, config, state, dir := localSyncObjectTestStore(t)
	path := filepath.Join(dir, "note.md")
	if err := os.WriteFile(path, []byte("exact source version"), 0o600); err != nil {
		t.Fatal(err)
	}
	object, err := store.CreateLocalSyncObject(config, state, LocalSyncObjectCreateInput{Path: path, ScopeRef: "box-notes"})
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan loomsync.SyncedObjectInput, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input loomsync.SyncedObjectInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		requests <- input
		<-release
		_ = json.NewEncoder(w).Encode(response.Success("fixture", loomsync.SyncedObjectResult{
			Item:     loomsync.SyncItemResult{LocalRef: input.LocalObjectRef, ItemKind: loomsync.ItemKindObjectBlob, Status: loomsync.ItemStatusAccepted, Metadata: input.Metadata},
			ObjectID: "main-exact", VersionID: "version-exact", HashURI: input.HashURI,
		}))
	}))
	defer server.Close()
	defer releaseOnce.Do(func() { close(release) })
	config.MainURL = server.URL
	pushDone := make(chan error, 1)
	go func() {
		_, err := pushLocalSyncOnce(context.Background(), store, config, state, "fixture", 1, false)
		pushDone <- err
	}()
	var input loomsync.SyncedObjectInput
	select {
	case input = <-requests:
	case <-time.After(5 * time.Second):
		t.Fatal("delivery did not start")
	}
	ackDone := make(chan error, 1)
	go func() {
		ackDone <- store.ApplySyncedObjectResult(loomsync.SyncedObjectResult{Item: loomsync.SyncItemResult{
			LocalRef: object.LocalObjectID, ItemKind: loomsync.ItemKindObjectBlob, Status: loomsync.ItemStatusFailed,
			ErrorCode: "stale-delivery-failure", Metadata: input.Metadata,
		}})
	}()
	select {
	case err := <-ackDone:
		t.Fatalf("ack bypassed held delivery lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-pushDone; err != nil {
		t.Fatal(err)
	}
	if err := <-ackDone; err != nil {
		t.Fatal(err)
	}
	got, err := store.ResolveLocalSyncObject(object.LocalObjectID)
	if err != nil || got.SyncStatus != loomsync.ItemStatusAccepted || got.MainVersionID != "version-exact" {
		t.Fatalf("concurrent stale acknowledgement erased receipt: %+v %v", got, err)
	}
	beforeObjects, _ := os.Stat(store.syncObjectsPath())
	beforeOutbox, _ := os.Stat(store.syncOutboxPath())
	if err := store.ApplySyncedObjectResult(loomsync.SyncedObjectResult{Item: loomsync.SyncItemResult{
		LocalRef: object.LocalObjectID, ItemKind: loomsync.ItemKindObjectBlob, Status: loomsync.ItemStatusAccepted, Metadata: input.Metadata,
	}, ObjectID: "main-exact", VersionID: "version-exact"}); err != nil {
		t.Fatal(err)
	}
	afterObjects, _ := os.Stat(store.syncObjectsPath())
	afterOutbox, _ := os.Stat(store.syncOutboxPath())
	if !os.SameFile(beforeObjects, afterObjects) || !os.SameFile(beforeOutbox, afterOutbox) {
		t.Fatal("repeated terminal ack rewrote snapshots")
	}
}

func TestSyncQueueBatchBoundsDeliveryTurn(t *testing.T) {
	store, config, state, _ := localSyncObjectTestStore(t)
	events := make([]LocalSyncEvent, localQueueCommitPageSize+1)
	outbox := make([]LocalSyncOutboxItem, len(events))
	for i := range events {
		ref := fmt.Sprintf("event-%d", i)
		events[i] = LocalSyncEvent{LocalEventID: ref, StreamName: loomsync.StreamEvents, LocalSequence: int64(i + 1), SyncStatus: localSyncStatusPending, PayloadJSON: json.RawMessage(`{}`)}
		outbox[i] = LocalSyncOutboxItem{LocalOutboxID: "outbox-" + ref, LocalRef: ref, ItemKind: loomsync.ItemKindEvent, StreamName: loomsync.StreamEvents, LocalSequence: int64(i + 1), Status: localSyncStatusPending, PayloadJSON: json.RawMessage(`{}`)}
	}
	if err := store.SaveSyncEvents(events); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSyncOutbox(outbox); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input loomsync.PushBatchInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		if len(input.Items) != localQueueCommitPageSize {
			t.Errorf("unbounded delivery: %d", len(input.Items))
		}
		result := loomsync.PushBatchResult{}
		for _, item := range input.Items {
			result.Items = append(result.Items, loomsync.SyncItemResult{LocalRef: item.LocalRef, Status: loomsync.ItemStatusAccepted, GlobalRef: "main-" + item.LocalRef, Metadata: item.Metadata})
		}
		_ = json.NewEncoder(w).Encode(response.Success("fixture", result))
	}))
	defer server.Close()
	config.MainURL = server.URL
	run, err := pushLocalSyncOnce(context.Background(), store, config, state, "fixture", 100, false)
	if err != nil {
		t.Fatal(err)
	}
	if run.SubmittedItems != localQueueCommitPageSize || run.LocalStatus.Counts.Pending != 1 || run.Persistence.CommitPages != 1 || run.Persistence.SnapshotWrites != 2 {
		t.Fatalf("bounded turn lost pending work: %+v", run)
	}
}
