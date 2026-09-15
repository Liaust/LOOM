package nodeagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/response"
	loomsync "loom.local/loom/internal/sync"
)

func metadataOwnerFixture(t *testing.T) (Store, Config, State, watchedroots.OutputAction, LocalSyncObject) {
	t.Helper()
	s, config, state, dir := localSyncObjectTestStore(t)
	path := filepath.Join(dir, "note.md")
	if err := os.WriteFile(path, []byte("a tiny source"), 0600); err != nil {
		t.Fatal(err)
	}
	a := watchedroots.OutputAction{ActionKind: watchedroots.OutputActionSyncObject, RootKey: "notes", RelativePath: "note.md",
		LocalObjectID: ids.NewObjectID(), LocalVersionID: ids.NewObjectVersionID(), SourcePath: "watched-root://notes/note.md",
		ScopeRef: "system", IndexPolicy: watchedroots.IndexModeNone}
	object, item, err := s.QueueWatchedRootObject(config, state, a, path)
	if err != nil {
		t.Fatal(err)
	}
	mainObject, mainVersion := ids.NewObjectID(), ids.NewObjectVersionID()
	ws := watchedroots.NewStore(s.DataDir)
	ps := watchedroots.PathState{RootKey: a.RootKey, RelativePath: a.RelativePath, Status: watchedroots.PathStatusIncluded,
		Kind: watchedroots.PathKindFile, LocalObjectID: object.LocalObjectID, LocalVersionID: object.LocalVersionID,
		ContentHashURI: object.HashURI, LastQueuedHashURI: object.HashURI, SyncStatus: watchedroots.OutputStatusQueued}
	if err := ws.SavePathState(ps); err != nil {
		t.Fatal(err)
	}
	result := loomsync.SyncedObjectResult{ObjectID: mainObject, VersionID: mainVersion,
		Item: loomsync.SyncItemResult{LocalRef: object.LocalObjectID, Status: loomsync.ItemStatusAccepted,
			Metadata: objectJSON(map[string]any{"local_outbox_id": item.LocalOutboxID, "local_version_ref": object.LocalVersionID})}}
	if err := s.ApplySyncedObjectResult(result); err != nil {
		t.Fatal(err)
	}
	ps, err = ws.LoadPathState(a.RootKey, a.RelativePath)
	if err != nil {
		t.Fatal(err)
	}
	if ps.LastSyncedModifiedAt == nil || !ps.LastSyncedModifiedAt.Equal(object.SourceMtime) {
		t.Fatal("blob mtime not acknowledged")
	}
	mtime := object.SourceMtime.Add(time.Minute)
	ps.ModifiedAt = &mtime
	if err := ws.SavePathState(ps); err != nil {
		t.Fatal(err)
	}
	a.ActionKind = watchedroots.OutputActionSyncMetadata
	a.ContentHashURI, a.ModifiedAt = object.HashURI, &mtime
	a.MainObjectID, a.MainVersionID = mainObject, mainVersion
	// Missing source bytes prove metadata queuing/transport never reread the blob.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	return s, config, state, a, object
}

func metadataAck(item LocalSyncOutboxItem) loomsync.SyncItemResult {
	return loomsync.SyncItemResult{LocalRef: item.LocalRef, ItemKind: item.ItemKind, StreamName: item.StreamName,
		LocalSequence: item.LocalSequence, PayloadHash: "sha256:" + item.PayloadHash, Status: loomsync.ItemStatusAccepted,
		GlobalRef: metadataString(item.PayloadJSON, "object_id"), Metadata: objectJSON(map[string]any{"local_outbox_id": item.LocalOutboxID})}
}

func TestSourceMetadataOwnerQueueTransportReplayAndOrdering(t *testing.T) {
	s, config, state, a, original := metadataOwnerFixture(t)
	first, err := s.QueueWatchedRootMetadata(state, a)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.QueueWatchedRootMetadata(state, a)
	if err != nil || replay.LocalOutboxID != first.LocalOutboxID {
		t.Fatalf("queue replay: %+v %v", replay, err)
	}
	ws := watchedroots.NewStore(s.DataDir)
	change := func(mtime time.Time) LocalSyncOutboxItem {
		t.Helper()
		ps, err := ws.LoadPathState(a.RootKey, a.RelativePath)
		if err != nil {
			t.Fatal(err)
		}
		ps.ModifiedAt = &mtime
		if err := ws.SavePathState(ps); err != nil {
			t.Fatal(err)
		}
		a.ModifiedAt = &mtime
		item, err := s.QueueWatchedRootMetadata(state, a)
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	second := change(original.SourceMtime.Add(-time.Hour))
	third := change(*firstTime(t, first))
	if first.LocalSequence >= second.LocalSequence || second.LocalSequence >= third.LocalSequence || first.LocalOutboxID == third.LocalOutboxID {
		t.Fatal("A-B-A reused an old observation")
	}
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if r.URL.Path != "/v1/node-agent/sync/batches" {
			t.Errorf("unexpected content transport %s", r.URL.Path)
			w.WriteHeader(500)
			return
		}
		var input loomsync.PushBatchInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		if input.BatchKind != loomsync.BatchKindObjectMetadata || len(input.Items) != 3 {
			t.Errorf("unexpected batch %+v", input)
		}
		if requestCount == 1 {
			w.WriteHeader(503)
			return
		}
		result := loomsync.PushBatchResult{}
		for _, i := range input.Items {
			if i.ItemKind != loomsync.ItemKindObjectMetadata || metadataString(i.PayloadJSON, "content_base64") != "" {
				t.Error("content in metadata batch")
			}
			result.Items = append(result.Items, loomsync.SyncItemResult{LocalRef: i.LocalRef, ItemKind: i.ItemKind, StreamName: i.StreamName, LocalSequence: i.LocalSequence,
				PayloadHash: "sha256:" + rawJSONHash(i.PayloadJSON), GlobalRef: a.MainObjectID, Status: loomsync.ItemStatusAccepted, Metadata: i.Metadata})
		}
		json.NewEncoder(w).Encode(response.Success("corr_metadata", result))
	}))
	defer server.Close()
	config.MainURL = server.URL
	if _, err := pushLocalSyncOnce(context.Background(), s, config, state, "corr_metadata", 100, false); err == nil {
		t.Fatal("lost network failure")
	}
	outbox, _ := s.LoadSyncOutbox()
	for _, item := range outbox {
		if item.ItemKind == loomsync.ItemKindObjectMetadata && item.Status != localSyncStatusPending {
			t.Fatal("network failure consumed item")
		}
	}
	if _, err := pushLocalSyncOnce(context.Background(), s, config, state, "corr_metadata", 100, false); err != nil {
		t.Fatal(err)
	}
	ps, _ := ws.LoadPathState(a.RootKey, a.RelativePath)
	if ps.LastSyncedMetadataSequence != third.LocalSequence || !ps.LastSyncedModifiedAt.Equal(*a.ModifiedAt) {
		t.Fatalf("final observation lost: %+v", ps)
	}
	if err := s.ApplySyncPushResult(loomsync.PushBatchResult{Items: []loomsync.SyncItemResult{metadataAck(second)}}); err != nil {
		t.Fatal(err)
	}
	ps, _ = ws.LoadPathState(a.RootKey, a.RelativePath)
	if ps.LastSyncedMetadataSequence != third.LocalSequence {
		t.Fatal("old receipt replaced current acknowledgement")
	}
	objects, _ := s.LoadSyncObjects()
	if len(objects) != 1 || objects[0].LocalVersionID != original.LocalVersionID || !objects[0].SourceMtime.Equal(original.SourceMtime) {
		t.Fatal("metadata changed immutable content upload")
	}
	if requestCount != 2 {
		t.Fatalf("requests=%d", requestCount)
	}
}

func firstTime(t *testing.T, item LocalSyncOutboxItem) *time.Time {
	t.Helper()
	var p loomsync.MetadataObservation
	if err := json.Unmarshal(item.PayloadJSON, &p); err != nil {
		t.Fatal(err)
	}
	return &p.SourceMtime
}

func TestSourceMetadataOwnerRefusesSubstitutionAndOldVersion(t *testing.T) {
	s, _, state, a, _ := metadataOwnerFixture(t)
	item, err := s.QueueWatchedRootMetadata(state, a)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*loomsync.SyncItemResult){
		func(r *loomsync.SyncItemResult) { r.LocalSequence++ }, func(r *loomsync.SyncItemResult) { r.PayloadHash = "wrong" },
		func(r *loomsync.SyncItemResult) { r.PayloadHash = item.PayloadHash },
		func(r *loomsync.SyncItemResult) { r.GlobalRef = ids.NewObjectID() }, func(r *loomsync.SyncItemResult) { r.LocalRef = "other" },
		func(r *loomsync.SyncItemResult) { r.ItemKind = loomsync.ItemKindEvent },
		func(r *loomsync.SyncItemResult) { r.Metadata = objectJSON(map[string]any{"local_outbox_id": "other"}) },
	} {
		r := metadataAck(item)
		mutate(&r)
		if err := s.ApplySyncPushResult(loomsync.PushBatchResult{Items: []loomsync.SyncItemResult{r}}); err == nil {
			t.Fatal("accepted substituted receipt")
		}
	}
	ws := watchedroots.NewStore(s.DataDir)
	ps, _ := ws.LoadPathState(a.RootKey, a.RelativePath)
	ps.LocalVersionID = ids.NewObjectVersionID()
	if err := ws.SavePathState(ps); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueueWatchedRootMetadata(state, a); err == nil {
		t.Fatal("accepted stale plan")
	}
	if err := s.ApplySyncPushResult(loomsync.PushBatchResult{Items: []loomsync.SyncItemResult{metadataAck(item)}}); err != nil {
		t.Fatal(err)
	}
	after, _ := ws.LoadPathState(a.RootKey, a.RelativePath)
	if after.LocalVersionID != ps.LocalVersionID || after.LastSyncedMetadataSequence != 0 {
		t.Fatal("old metadata receipt changed new version")
	}
}

func TestSourceMetadataOwnerConcurrentQueue(t *testing.T) {
	s, _, state, a, _ := metadataOwnerFixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.QueueWatchedRootMetadata(state, a); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	outbox, err := s.LoadSyncOutbox()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, item := range outbox {
		if item.ItemKind == loomsync.ItemKindObjectMetadata {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("concurrent queue produced %d observations", count)
	}
}

func TestSourceMetadataOwnerCrashReplayAndLateBlob(t *testing.T) {
	s, _, state, a, object := metadataOwnerFixture(t)
	item, err := s.QueueWatchedRootMetadata(state, a)
	if err != nil {
		t.Fatal(err)
	}
	outbox, err := s.LoadSyncOutbox()
	if err != nil {
		t.Fatal(err)
	}
	// Crash boundary: path acknowledgement reached disk, outbox is still pending.
	if err := s.applyMetadataAcknowledgements(outbox, []loomsync.SyncItemResult{metadataAck(item)}); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplySyncPushResult(loomsync.PushBatchResult{Items: []loomsync.SyncItemResult{metadataAck(item)}}); err != nil {
		t.Fatal(err)
	}
	ws := watchedroots.NewStore(s.DataDir)
	ps, err := ws.LoadPathState(a.RootKey, a.RelativePath)
	if err != nil {
		t.Fatal(err)
	}
	ps.LocalVersionID = ids.NewObjectVersionID()
	ps.SyncStatus = watchedroots.OutputStatusQueued
	if err := ws.SavePathState(ps); err != nil {
		t.Fatal(err)
	}
	r := loomsync.SyncedObjectResult{ObjectID: a.MainObjectID, VersionID: a.MainVersionID,
		Item: loomsync.SyncItemResult{LocalRef: object.LocalObjectID, Status: loomsync.ItemStatusDuplicate,
			Metadata: objectJSON(map[string]any{"local_version_ref": object.LocalVersionID})}}
	if err := s.updateWatchedRootStateFromSyncedObject([]LocalSyncObject{object}, r, time.Now()); err != nil {
		t.Fatal(err)
	}
	after, err := ws.LoadPathState(a.RootKey, a.RelativePath)
	if err != nil {
		t.Fatal(err)
	}
	if after.LocalVersionID != ps.LocalVersionID || after.SyncStatus != ps.SyncStatus || !after.LastSyncedModifiedAt.Equal(*a.ModifiedAt) {
		t.Fatal("late blob acknowledgement regressed current owner state")
	}
}
