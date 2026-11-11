package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/nodeagent/watchedroots"
	loomsync "loom.local/loom/internal/sync"
)

// QueueWatchedRootFileObservation uses the existing durable metadata outbox;
// it never opens a source file, stages its bytes, or changes a backup marker.
func (s Store) QueueWatchedRootFileObservation(state State, action watchedroots.OutputAction) (LocalSyncOutboxItem, error) {
	s, unlock, err := s.lockLocalSync(context.Background())
	if err != nil {
		return LocalSyncOutboxItem{}, err
	}
	defer unlock()
	if action.ActionKind != watchedroots.OutputActionFileObservation || action.SourceGeneration == "" ||
		action.SourcePath != "watched-root://"+action.RootKey+"/"+action.RelativePath {
		return LocalSyncOutboxItem{}, fmt.Errorf("invalid planned file observation")
	}
	current, err := s.loadQueuePathState(action.RootKey, action.RelativePath)
	if err != nil {
		return LocalSyncOutboxItem{}, err
	}
	present := action.SourcePresence == "present" && current.Status == watchedroots.PathStatusIncluded && current.Kind == watchedroots.PathKindFile
	missing := action.SourcePresence == "missing" && current.Status == watchedroots.PathStatusDeleted
	unavailable := action.SourcePresence == "unavailable" && (current.Status == watchedroots.PathStatusSkipped || current.Status == watchedroots.PathStatusError) && current.Classification.ReasonCode == watchedroots.ReasonSkippedPermissionDenied
	if (!present && !missing && !unavailable) || current.SizeBytes != action.SizeBytes || !equalObservationTime(current.ModifiedAt, action.ModifiedAt) ||
		(current.LocalObjectID != "" && current.LocalObjectID != action.LocalObjectID) {
		return LocalSyncOutboxItem{}, fmt.Errorf("file observation plan is no longer current")
	}
	outbox, err := s.LoadSyncOutbox()
	if err != nil {
		return LocalSyncOutboxItem{}, err
	}
	sequence, err := s.nextLocalSequence(loomsync.StreamObjectMetadata)
	if err != nil {
		return LocalSyncOutboxItem{}, err
	}
	now := time.Now().UTC()
	payload := marshalRaw(loomsync.FileObservation{SchemaVersion: loomsync.FileObservationSchema,
		LocalObjectRef: action.LocalObjectID, RootKey: action.RootKey, RelativePath: action.RelativePath,
		Generation: action.SourceGeneration, Presence: action.SourcePresence, ObservedAt: now,
		SourceMtime: action.ModifiedAt, SizeBytes: action.SizeBytes})
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

func equalObservationTime(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
}

func (s Store) applyFileObservationAcknowledgements(outbox []LocalSyncOutboxItem, results []loomsync.SyncItemResult) error {
	type observation struct {
		p loomsync.FileObservation
		r loomsync.SyncItemResult
	}
	accepted := []observation{}
	for _, result := range results {
		for _, local := range outbox {
			if local.LocalRef != result.LocalRef || metadataString(local.PayloadJSON, "schema_version") != loomsync.FileObservationSchema {
				continue
			}
			if result.ItemKind != local.ItemKind || result.StreamName != local.StreamName || result.LocalSequence != local.LocalSequence ||
				result.PayloadHash != "sha256:"+local.PayloadHash ||
				(metadataString(result.Metadata, "local_outbox_id") != "" && metadataString(result.Metadata, "local_outbox_id") != local.LocalOutboxID) {
				return fmt.Errorf("file observation acknowledgement identity mismatch")
			}
			if result.Status != loomsync.ItemStatusAccepted && result.Status != loomsync.ItemStatusDuplicate {
				continue
			}
			if err := ids.Validate("object", result.GlobalRef); err != nil {
				return fmt.Errorf("file observation acknowledgement object mismatch")
			}
			var p loomsync.FileObservation
			if err := json.Unmarshal(local.PayloadJSON, &p); err != nil {
				return err
			}
			accepted = append(accepted, observation{p, result})
		}
	}
	for _, o := range accepted {
		current, err := s.loadQueuePathState(o.p.RootKey, o.p.RelativePath)
		if watchedroots.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if current.LocalObjectID != o.p.LocalObjectRef || current.LastFileObservationSequence >= o.r.LocalSequence {
			continue
		}
		current.MainObjectID = o.r.GlobalRef
		current.LastFileObservationSequence = o.r.LocalSequence
		if err := s.saveQueuePathState(current); err != nil {
			return err
		}
	}
	return nil
}
