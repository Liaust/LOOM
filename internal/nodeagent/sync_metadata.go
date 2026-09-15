package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/nodeagent/watchedroots"
	loomsync "loom.local/loom/internal/sync"
)

// QueueWatchedRootMetadata does not read the source file or alter its immutable
// upload. Only the latest observation can be reused, so A-B-A is a new sequence.
func (s Store) QueueWatchedRootMetadata(state State, action watchedroots.OutputAction) (LocalSyncOutboxItem, error) {
	s, unlock, err := s.lockLocalSync(context.Background())
	if err != nil {
		return LocalSyncOutboxItem{}, err
	}
	defer unlock()
	if err := s.EnsureSyncDataDirs(); err != nil {
		return LocalSyncOutboxItem{}, err
	}
	if state.NodeID == "" || action.ActionKind != watchedroots.OutputActionSyncMetadata || action.ModifiedAt == nil || action.ModifiedAt.IsZero() ||
		action.SourcePath != "watched-root://"+action.RootKey+"/"+action.RelativePath {
		return LocalSyncOutboxItem{}, fmt.Errorf("invalid watched-root metadata observation")
	}
	current, err := watchedroots.NewStore(s.DataDir).LoadPathState(action.RootKey, action.RelativePath)
	if err != nil {
		return LocalSyncOutboxItem{}, err
	}
	if current.Status != watchedroots.PathStatusIncluded || current.Kind != watchedroots.PathKindFile ||
		current.ModifiedAt == nil || !current.ModifiedAt.Equal(*action.ModifiedAt) || current.ContentHashURI != action.ContentHashURI ||
		current.LocalObjectID != action.LocalObjectID || current.LocalVersionID != action.LocalVersionID ||
		current.MainObjectID != action.MainObjectID || current.MainVersionID != action.MainVersionID {
		return LocalSyncOutboxItem{}, fmt.Errorf("watched-root metadata plan is no longer current")
	}
	objects, err := s.LoadSyncObjects()
	if err != nil {
		return LocalSyncOutboxItem{}, err
	}
	bound := false
	for _, object := range objects {
		if object.LocalObjectID == action.LocalObjectID && object.LocalVersionID == action.LocalVersionID &&
			object.MainObjectID == action.MainObjectID && object.MainVersionID == action.MainVersionID &&
			object.MainObjectID != "" && object.MainVersionID != "" && object.HashURI == action.ContentHashURI &&
			object.SourcePath == action.SourcePath &&
			(object.SyncStatus == loomsync.ItemStatusAccepted || object.SyncStatus == loomsync.ItemStatusDuplicate) {
			bound = true
			break
		}
	}
	if !bound {
		return LocalSyncOutboxItem{}, fmt.Errorf("metadata observation lacks an accepted content binding")
	}
	payload := marshalRaw(loomsync.MetadataObservation{SchemaVersion: loomsync.MetadataObservationSchema,
		LocalObjectRef: action.LocalObjectID, LocalVersionRef: action.LocalVersionID,
		ObjectID: action.MainObjectID, VersionID: action.MainVersionID, HashURI: action.ContentHashURI,
		SourcePath: action.SourcePath, SourceMtime: action.ModifiedAt.UTC()})
	outbox, err := s.LoadSyncOutbox()
	if err != nil {
		return LocalSyncOutboxItem{}, err
	}
	var latest *LocalSyncOutboxItem
	for i := range outbox {
		item := &outbox[i]
		if item.ItemKind == loomsync.ItemKindObjectMetadata && metadataString(item.PayloadJSON, "local_object_ref") == action.LocalObjectID &&
			(latest == nil || item.LocalSequence > latest.LocalSequence) {
			latest = item
		}
	}
	if latest != nil && latest.PayloadHash == rawJSONHash(payload) {
		if latest.Status == loomsync.ItemStatusConflicted {
			return LocalSyncOutboxItem{}, fmt.Errorf("metadata observation has an unresolved sync conflict")
		}
		return *latest, nil
	}
	sequence, err := s.nextLocalSequence(loomsync.StreamObjectMetadata)
	if err != nil {
		return LocalSyncOutboxItem{}, err
	}
	now := time.Now().UTC()
	ref := ids.NewLocalOutboxID()
	item := LocalSyncOutboxItem{LocalOutboxID: ref, LocalRef: ref, ItemKind: loomsync.ItemKindObjectMetadata,
		StreamName: loomsync.StreamObjectMetadata, LocalSequence: sequence, PayloadJSON: payload,
		PayloadHash: rawJSONHash(payload), Status: localSyncStatusPending, CreatedAt: now}
	if err := s.SaveSyncOutbox(append(outbox, item)); err != nil {
		return LocalSyncOutboxItem{}, err
	}
	if err := s.updateLocalQueuedCursor(item.StreamName, sequence, now); err != nil {
		return LocalSyncOutboxItem{}, err
	}
	return item, nil
}

func (s Store) applyMetadataAcknowledgements(outbox []LocalSyncOutboxItem, results []loomsync.SyncItemResult) error {
	// Validate the entire metadata response before acknowledging any observation.
	type observation struct {
		item LocalSyncOutboxItem
		p    loomsync.MetadataObservation
	}
	accepted := []observation{}
	for _, result := range results {
		var local *LocalSyncOutboxItem
		for i := range outbox {
			if (outbox[i].LocalRef == result.LocalRef || outbox[i].LocalOutboxID == metadataString(result.Metadata, "local_outbox_id")) && outbox[i].ItemKind == loomsync.ItemKindObjectMetadata {
				local = &outbox[i]
				break
			}
		}
		if result.ItemKind != loomsync.ItemKindObjectMetadata && local == nil {
			continue
		}
		if local == nil || result.LocalRef != local.LocalRef || result.ItemKind != local.ItemKind || result.StreamName != local.StreamName ||
			result.LocalSequence != local.LocalSequence || result.PayloadHash != "sha256:"+local.PayloadHash ||
			(metadataString(result.Metadata, "local_outbox_id") != "" && metadataString(result.Metadata, "local_outbox_id") != local.LocalOutboxID) {
			return fmt.Errorf("metadata acknowledgement identity mismatch")
		}
		var p loomsync.MetadataObservation
		if err := json.Unmarshal(local.PayloadJSON, &p); err != nil {
			return fmt.Errorf("invalid queued metadata observation")
		}
		if result.Status == loomsync.ItemStatusAccepted || result.Status == loomsync.ItemStatusDuplicate {
			if result.GlobalRef != p.ObjectID {
				return fmt.Errorf("metadata acknowledgement object mismatch")
			}
			accepted = append(accepted, observation{*local, p})
		}
	}
	for _, observation := range accepted {
		p, item := observation.p, observation.item
		root, relative, ok := strings.Cut(strings.TrimPrefix(p.SourcePath, "watched-root://"), "/")
		if !ok || root == "" || relative == "" {
			return fmt.Errorf("invalid queued metadata path")
		}
		ws := watchedroots.NewStore(s.DataDir)
		current, err := ws.LoadPathState(root, relative)
		if watchedroots.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if current.LocalObjectID != p.LocalObjectRef || current.LocalVersionID != p.LocalVersionRef ||
			current.MainObjectID != p.ObjectID || current.MainVersionID != p.VersionID || current.LastSyncedHashURI != p.HashURI ||
			item.LocalSequence <= current.LastSyncedMetadataSequence {
			continue
		}
		mtime := p.SourceMtime.UTC()
		current.LastSyncedModifiedAt = &mtime
		current.LastSyncedMetadataSequence = item.LocalSequence
		// Persist before terminalizing the outbox: a crash retries the same item.
		if err := ws.SavePathState(current); err != nil {
			return err
		}
	}
	return nil
}
