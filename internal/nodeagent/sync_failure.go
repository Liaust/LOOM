package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
	"loom.local/loom/internal/correlation"
	loomsync "loom.local/loom/internal/sync"
)

func syncOutboxForRoot(items []LocalSyncOutboxItem, byRef, byVersion map[string]LocalSyncObject, root string) []LocalSyncOutboxItem {
	if root == "" {
		return items
	}
	filtered := make([]LocalSyncOutboxItem, 0)
	for _, item := range items {
		key := ""
		switch item.ItemKind {
		case loomsync.ItemKindObjectBlob, loomsync.ItemKindObjectMetadata:
			ref := item
			if item.ItemKind == loomsync.ItemKindObjectMetadata {
				ref.LocalRef = metadataString(item.PayloadJSON, "local_object_ref")
			}
			if object, ok := localSyncObjectForOutbox(ref, byRef, byVersion); ok {
				key = metadataString(object.Metadata, "watched_root")
			}
		case loomsync.ItemKindDeletionRequest:
			key = metadataString(item.PayloadJSON, "root_key")
		}
		if key == root {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func (s Store) localSyncRootStatus(config Config, state State, root string) (LocalSyncStatus, error) {
	if root == "" {
		return s.LocalSyncStatus(config, state)
	}
	s, unlock, err := s.lockLocalSync(context.Background())
	if err != nil {
		return LocalSyncStatus{}, err
	}
	defer unlock()
	if s.syncBatch != nil {
		return s.localSyncRootStatusUncached(config, state, root)
	}
	return cachedLocalStatus("sync-root:"+root+":"+state.NodeID+":"+config.NodeKey,
		[]string{s.syncOutboxPath(), s.syncObjectsPath()}, func() (LocalSyncStatus, error) {
			return s.localSyncRootStatusUncached(config, state, root)
		})
}

func (s Store) localSyncRootStatusUncached(config Config, state State, root string) (LocalSyncStatus, error) {
	items, err := s.LoadSyncOutbox()
	if err != nil {
		return LocalSyncStatus{}, err
	}
	objects, err := s.LoadSyncObjects()
	if err != nil {
		return LocalSyncStatus{}, err
	}
	refs := make(map[string]LocalSyncObject, len(objects))
	versions := make(map[string]LocalSyncObject, len(objects))
	for _, object := range objects {
		refs[object.LocalObjectID] = object
		versions[localObjectVersionKey(object.LocalObjectID, object.LocalVersionID)] = object
	}
	status := LocalSyncStatus{NodeID: state.NodeID, NodeKey: config.NodeKey}
	for _, item := range syncOutboxForRoot(items, refs, versions, root) {
		switch item.Status {
		case localSyncStatusAccepted:
			status.Counts.Accepted++
		case localSyncStatusDuplicate:
			status.Counts.Duplicates++
		case localSyncStatusFailed:
			status.Counts.Failed++
		case localSyncStatusConflicted:
			status.Counts.Conflicted++
		default:
			status.Counts.Pending++
		}
	}
	return status, nil
}

func (s Store) recordSyncUploadError(item LocalSyncOutboxItem, err error) error {
	var remote RemoteRequestError
	status, code, message := localSyncStatusPending, "sync.upload_unavailable", "Upload did not complete; it remains pending for retry."
	if errors.As(err, &remote) {
		if remote.Envelope.Error.Code != "" {
			code, message = remote.Envelope.Error.Code, remote.Envelope.Error.Summary
		}
		// Only the explicit missing-project result stops automatic retry. Transport
		// failures, authentication failures and generic server errors remain pending.
		if remote.StatusCode == http.StatusUnprocessableEntity && code == "sync.project_not_found" {
			status = localSyncStatusFailed
		}
	}
	return s.ApplySyncedObjectResult(loomsync.SyncedObjectResult{Item: loomsync.SyncItemResult{
		LocalRef: item.LocalRef, ItemKind: item.ItemKind, Status: status, ErrorCode: code, ErrorMessage: message,
		Metadata: localSyncItemMetadata(nil, item),
	}})
}

func newSyncRetryObjectCommand(opts *rootOptions) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{Use: "retry-object <outbox-id>", Short: "Requeue one failed object after repairing its cause; preserve its exact source and identity", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return errors.New("retry requires --yes")
			}
			store, _, _, err := opts.loadAll()
			if err != nil {
				return err
			}
			item, err := store.retrySyncObject(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), item))
		}}
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm requeuing this exact failed item")
	return cmd
}

func (s Store) retrySyncObject(ctx context.Context, id string) (LocalSyncOutboxItem, error) {
	s, unlock, err := s.lockLocalSync(ctx)
	if err != nil {
		return LocalSyncOutboxItem{}, err
	}
	defer unlock()
	items, err := s.LoadSyncOutbox()
	if err != nil {
		return LocalSyncOutboxItem{}, err
	}
	for _, item := range items {
		if item.LocalOutboxID != id {
			continue
		}
		if item.ItemKind != loomsync.ItemKindObjectBlob || item.Status != localSyncStatusFailed {
			return LocalSyncOutboxItem{}, fmt.Errorf("retry requires an exact failed object outbox item")
		}
		item.Status = localSyncStatusPending
		// Retain the preceding error as evidence until a successful acknowledgement.
		err := s.ApplySyncedObjectResult(loomsync.SyncedObjectResult{Item: loomsync.SyncItemResult{LocalRef: item.LocalRef, ItemKind: item.ItemKind, Status: item.Status, ErrorCode: item.LastErrorCode, ErrorMessage: item.LastErrorMessage, Metadata: localSyncItemMetadata(nil, item)}})
		return item, err
	}
	return LocalSyncOutboxItem{}, fmt.Errorf("object outbox item not found")
}
