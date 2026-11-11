package nodeagent

import (
	"testing"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/nodeagent/watchedroots"
	loomsync "loom.local/loom/internal/sync"
)

func TestFileObservationOwnerQueuesWithoutSourceBytesAndAcknowledges(t *testing.T) {
	s, _, state, _ := localSyncObjectTestStore(t)
	ws := watchedroots.NewStore(s.DataDir)
	now := time.Now().UTC()
	localID, canonicalID := ids.NewObjectID(), ids.NewObjectID()
	ps := watchedroots.PathState{RootKey: "notes", RelativePath: "not-on-disk.pdf", Kind: watchedroots.PathKindFile,
		Status: watchedroots.PathStatusIncluded, SizeBytes: 2 << 30, ModifiedAt: &now, LocalObjectID: localID}
	if err := ws.SavePathState(ps); err != nil {
		t.Fatal(err)
	}
	action := watchedroots.OutputAction{ActionKind: watchedroots.OutputActionFileObservation, RootKey: ps.RootKey,
		RelativePath: ps.RelativePath, LocalObjectID: localID, SourceGeneration: "generation",
		SourcePresence: "present", SourcePath: "watched-root://notes/" + ps.RelativePath, SizeBytes: ps.SizeBytes, ModifiedAt: ps.ModifiedAt}
	item, err := s.QueueWatchedRootFileObservation(state, action)
	if err != nil {
		t.Fatal(err)
	}
	objects, err := s.LoadSyncObjects()
	if err != nil || len(objects) != 0 {
		t.Fatalf("observation created/staged a blob: %v %v", objects, err)
	}
	ack := metadataAck(item)
	ack.GlobalRef = canonicalID
	bad := ack
	bad.PayloadHash = "mismatched"
	if err := s.ApplySyncPushResult(loomsync.PushBatchResult{Items: []loomsync.SyncItemResult{bad}}); err == nil {
		t.Fatal("mismatched acknowledgement accepted")
	}
	if err := s.ApplySyncPushResult(loomsync.PushBatchResult{Items: []loomsync.SyncItemResult{ack}}); err != nil {
		t.Fatal(err)
	}
	ps, err = ws.LoadPathState(ps.RootKey, ps.RelativePath)
	if err != nil || ps.MainObjectID != canonicalID || ps.MainVersionID != "" || ps.LastFileObservationSequence != item.LocalSequence {
		t.Fatalf("metadata identity did not converge: %+v %v", ps, err)
	}
	ps.Status = watchedroots.PathStatusDeleted
	if err := ws.SavePathState(ps); err != nil {
		t.Fatal(err)
	}
	action.SourcePresence = "missing"
	missing, err := s.QueueWatchedRootFileObservation(state, action)
	if err != nil || missing.LocalSequence <= item.LocalSequence {
		t.Fatalf("withdrawal sequence: %+v %v", missing, err)
	}
	ack = metadataAck(missing)
	ack.GlobalRef = canonicalID
	if err := s.ApplySyncPushResult(loomsync.PushBatchResult{Items: []loomsync.SyncItemResult{ack}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplySyncPushResult(loomsync.PushBatchResult{Items: []loomsync.SyncItemResult{metadataAckWithFile(item, canonicalID)}}); err != nil {
		t.Fatal(err)
	}
	ps, err = ws.LoadPathState(ps.RootKey, ps.RelativePath)
	if err != nil || ps.Status != watchedroots.PathStatusDeleted || ps.LastFileObservationSequence != missing.LocalSequence || ps.DeletionStatus != "" {
		t.Fatalf("old acknowledgement changed withdrawal/backup state: %+v %v", ps, err)
	}
}

func metadataAckWithFile(item LocalSyncOutboxItem, canonicalID string) loomsync.SyncItemResult {
	r := metadataAck(item)
	r.GlobalRef = canonicalID
	return r
}
