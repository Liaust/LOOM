package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
	"loom.local/loom/internal/correlation"
	"loom.local/loom/internal/nodeagent/watchedroots"
	loomsync "loom.local/loom/internal/sync"
)

func syncOutboxForRoot(items []LocalSyncOutboxItem, byRef, byVersion map[string]LocalSyncObject, root string) []LocalSyncOutboxItem {
	if root == "" {
		return items
	}
	filtered := make([]LocalSyncOutboxItem, 0)
	for _, item := range items {
		if syncOutboxRoot(item, byRef, byVersion) == root {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func syncOutboxRoot(item LocalSyncOutboxItem, byRef, byVersion map[string]LocalSyncObject) string {
	switch item.ItemKind {
	case loomsync.ItemKindObjectBlob, loomsync.ItemKindObjectMetadata:
		if item.ItemKind == loomsync.ItemKindObjectMetadata {
			if metadataString(item.PayloadJSON, "schema_version") == loomsync.FileObservationSchema {
				return metadataString(item.PayloadJSON, "root_key")
			}
			item.LocalRef = metadataString(item.PayloadJSON, "local_object_ref")
		}
		if object, ok := localSyncObjectForOutbox(item, byRef, byVersion); ok {
			return metadataString(object.Metadata, "watched_root")
		}
	case loomsync.ItemKindDeletionRequest:
		return metadataString(item.PayloadJSON, "root_key")
	}
	return ""
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
	counts, err := s.syncCountsByRoot()
	return LocalSyncStatus{NodeID: state.NodeID, NodeKey: config.NodeKey, Counts: counts[root]}, err
}

type syncSourceContentChanged struct {
	Path, Expected, Actual string
}

func (e syncSourceContentChanged) Error() string {
	return fmt.Sprintf("local file hash changed for %s: expected %s got %s", e.Path, e.Expected, e.Actual)
}

// A mutable source cannot supply an overwritten queued revision. Retain that
// exact record only after the current bytes have a newer real acknowledgement.
func (s Store) retainSupersededSyncUpload(item LocalSyncOutboxItem, old LocalSyncObject, versions map[string]LocalSyncObject, uploadErr error) (bool, error) {
	var changed syncSourceContentChanged
	root, relative := metadataString(old.Metadata, "watched_root"), metadataString(old.Metadata, "relative_path")
	contentPath := old.ContentPath
	if contentPath == "" {
		contentPath = old.SourcePath
	}
	if !errors.As(uploadErr, &changed) || item.Status != localSyncStatusPending || item.ItemKind != loomsync.ItemKindObjectBlob ||
		root == "" || relative == "" || changed.Expected != old.HashURI || changed.Actual == old.HashURI ||
		changed.Path != contentPath || changed.Actual == "" ||
		item.LocalRef != old.LocalObjectID || metadataString(item.PayloadJSON, "local_version_ref") != old.LocalVersionID ||
		item.SyncedAt != nil || item.GlobalRef != "" {
		return false, nil
	}
	current := old
	for _, candidate := range versions {
		if candidate.LocalObjectID == old.LocalObjectID && candidate.LocalSequence > current.LocalSequence {
			current = candidate
		}
	}
	if current.LocalVersionID == old.LocalVersionID || current.HashURI != changed.Actual ||
		(current.SyncStatus != localSyncStatusAccepted && current.SyncStatus != localSyncStatusDuplicate) ||
		current.MainObjectID == "" || current.MainVersionID == "" || current.SyncedAt == nil ||
		current.SourcePath != old.SourcePath || current.ContentPath != old.ContentPath ||
		current.ProjectRef != old.ProjectRef || current.ScopeRef != old.ScopeRef || current.LogicalName != old.LogicalName ||
		metadataString(current.Metadata, "watched_root") != root || metadataString(current.Metadata, "relative_path") != relative ||
		metadataString(current.Metadata, "node_key") != metadataString(old.Metadata, "node_key") {
		return false, nil
	}
	err := s.ApplySyncedObjectResult(loomsync.SyncedObjectResult{Item: loomsync.SyncItemResult{
		LocalRef: item.LocalRef, ItemKind: item.ItemKind, Status: localSyncStatusRetainedSuperseded,
		ErrorCode:    "sync.source_revision_superseded",
		ErrorMessage: fmt.Sprintf("Original queued revision retained without remote acceptance; current source matches acknowledged successor %s (%s).", current.LocalVersionID, current.MainVersionID),
		Metadata:     localSyncItemMetadata(nil, item),
	}})
	if err != nil {
		return false, err
	}
	// Older local-error handling could mark the already-acknowledged current
	// path pending. Repair only that status, with its existing exact receipt.
	pathState, err := s.loadQueuePathState(root, relative)
	if watchedroots.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if pathState.LocalObjectID == current.LocalObjectID && pathState.LocalVersionID == current.LocalVersionID &&
		pathState.MainObjectID == current.MainObjectID && pathState.MainVersionID == current.MainVersionID &&
		pathState.LastSyncedHashURI == current.HashURI && pathState.SyncStatus == localSyncStatusPending &&
		pathState.LastOutputErrorCode == "sync.upload_unavailable" {
		pathState.SyncStatus = current.SyncStatus
		pathState.LastOutputErrorCode, pathState.LastOutputErrorMessage = "", ""
		err = s.saveQueuePathState(pathState)
	}
	return err == nil, err
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
