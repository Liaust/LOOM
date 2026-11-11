package nodeagent

import (
	"loom.local/loom/internal/nodeagent/watchedroots"
	loomsync "loom.local/loom/internal/sync"
)

// Keep the exact queued record and hashes, but never upload application-owned
// transaction history as user content. No remote acceptance is fabricated.
func (s Store) retainInternalSyncItems(pending, items []LocalSyncOutboxItem, refs, versions map[string]LocalSyncObject) ([]LocalSyncOutboxItem, error) {
	retired := map[string]bool{}
	remaining := make([]LocalSyncOutboxItem, 0, len(pending))
	for _, item := range pending {
		owned := false
		switch item.ItemKind {
		case loomsync.ItemKindObjectBlob, loomsync.ItemKindObjectMetadata, loomsync.ItemKindDeletionRequest:
			owned = watchedroots.IsNotesRecoveryPath(metadataString(item.PayloadJSON, "source_path")) ||
				watchedroots.IsNotesRecoveryPath(metadataString(item.PayloadJSON, "relative_path"))
			lookup := item
			if item.ItemKind == loomsync.ItemKindObjectMetadata {
				lookup.LocalRef = metadataString(item.PayloadJSON, "local_object_ref")
			}
			if object, ok := localSyncObjectForOutbox(lookup, refs, versions); ok {
				owned = owned || watchedroots.IsNotesRecoveryPath(object.SourcePath)
			}
		}
		if owned {
			retired[item.LocalOutboxID] = true
		} else {
			remaining = append(remaining, item)
		}
	}
	if len(retired) == 0 {
		return remaining, nil
	}
	for i := range items {
		if retired[items[i].LocalOutboxID] {
			items[i].Status = localSyncStatusRetainedInternal
			items[i].LastErrorCode = "sync.owned_recovery_journal"
			items[i].LastErrorMessage = "Retained internal Notes recovery record; not user content and not remotely acknowledged."
		}
	}
	return remaining, s.SaveSyncOutbox(items)
}
