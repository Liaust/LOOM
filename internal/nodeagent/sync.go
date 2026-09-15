package nodeagent

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/correlation"
	"loom.local/loom/internal/events"
	"loom.local/loom/internal/filesystemmeta"
	"loom.local/loom/internal/ids"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/storagecatalog"
	loomsync "loom.local/loom/internal/sync"
)

const (
	localSyncStatusPending    = "pending"
	localSyncStatusAccepted   = "accepted"
	localSyncStatusDuplicate  = "duplicate"
	localSyncStatusConflicted = "conflicted"
	localSyncStatusFailed     = "failed"

	localSyncEventsFile    = "events.json"
	localSyncOutboxFile    = "outbox.json"
	localSyncCursorsFile   = "cursors.json"
	localSyncConflictsFile = "conflicts.json"
	localSyncObjectsFile   = "objects.json"
	localSyncDeletionsFile = "deletions.json"
)

type LocalSyncStatus struct {
	NodeID    string              `json:"node_id,omitempty"`
	NodeKey   string              `json:"node_key,omitempty"`
	Paths     LocalSyncPaths      `json:"paths"`
	Counts    LocalSyncCounts     `json:"counts"`
	Cursors   []LocalSyncCursor   `json:"cursors"`
	Conflicts []LocalSyncConflict `json:"conflicts,omitempty"`
}

type LocalSyncPaths struct {
	Root      string `json:"root"`
	Events    string `json:"events"`
	Objects   string `json:"objects"`
	Deletions string `json:"deletions"`
	Outbox    string `json:"outbox"`
	Cursors   string `json:"cursors"`
	Conflicts string `json:"conflicts"`
}

type LocalSyncCounts struct {
	Events     int `json:"events"`
	Objects    int `json:"objects"`
	Deletions  int `json:"deletions"`
	Pending    int `json:"pending"`
	Accepted   int `json:"accepted"`
	Duplicates int `json:"duplicates"`
	Conflicted int `json:"conflicted"`
	Failed     int `json:"failed"`
	Conflicts  int `json:"conflicts"`
}

type LocalSyncObject struct {
	LocalObjectID        string          `json:"local_object_id"`
	LocalVersionID       string          `json:"local_version_id"`
	LocalSequence        int64           `json:"local_sequence"`
	ProjectRef           string          `json:"project_ref,omitempty"`
	ScopeRef             string          `json:"scope_ref,omitempty"`
	LogicalName          string          `json:"logical_name"`
	SourcePath           string          `json:"source_path"`
	ContentPath          string          `json:"content_path,omitempty"`
	SourceMtime          time.Time       `json:"source_mtime"`
	SourceMtimeBasis     string          `json:"source_mtime_basis,omitempty"`
	SourceCreatedAt      *time.Time      `json:"source_created_at,omitempty"`
	SourceCreatedBasis   string          `json:"source_created_basis,omitempty"`
	SizeBytes            int64           `json:"size_bytes"`
	MimeType             string          `json:"mime_type"`
	HashURI              string          `json:"hash_uri"`
	IndexPolicy          string          `json:"index_policy"`
	RawBackupPolicy      string          `json:"raw_backup_policy"`
	FileClass            string          `json:"file_class,omitempty"`
	ClassificationSource string          `json:"classification_source,omitempty"`
	IndexingState        string          `json:"indexing_state,omitempty"`
	IndexingReason       string          `json:"indexing_reason,omitempty"`
	CreatedAt            time.Time       `json:"created_at"`
	Metadata             json.RawMessage `json:"metadata"`
	SyncStatus           string          `json:"sync_status"`
	MainObjectID         string          `json:"main_object_id,omitempty"`
	MainVersionID        string          `json:"main_version_id,omitempty"`
	MainBlobID           string          `json:"main_blob_id,omitempty"`
	SyncedAt             *time.Time      `json:"synced_at,omitempty"`
	LastErrorCode        string          `json:"last_error_code,omitempty"`
	LastErrorMessage     string          `json:"last_error_message,omitempty"`
}

type LocalSyncDeletionRequest struct {
	LocalDeletionID   string          `json:"local_deletion_id"`
	LocalSequence     int64           `json:"local_sequence"`
	RootKey           string          `json:"root_key,omitempty"`
	RelativePath      string          `json:"relative_path,omitempty"`
	LocalObjectID     string          `json:"local_object_id,omitempty"`
	MainObjectID      string          `json:"main_object_id,omitempty"`
	ContentHashURI    string          `json:"content_hash_uri,omitempty"`
	TargetKind        string          `json:"target_kind"`
	TargetRef         string          `json:"target_ref"`
	RequestedAction   string          `json:"requested_action"`
	Reason            string          `json:"reason,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	Metadata          json.RawMessage `json:"metadata"`
	SyncStatus        string          `json:"sync_status"`
	DeletionRequestID string          `json:"deletion_request_id,omitempty"`
	SyncedAt          *time.Time      `json:"synced_at,omitempty"`
	LastErrorCode     string          `json:"last_error_code,omitempty"`
	LastErrorMessage  string          `json:"last_error_message,omitempty"`
}

type LocalSyncObjectCreateInput struct {
	Path            string
	ProjectRef      string
	ScopeRef        string
	LogicalName     string
	IndexPolicy     string
	RawBackupPolicy string
}

type LocalSyncEvent struct {
	LocalEventID     string          `json:"local_event_id"`
	LocalSequence    int64           `json:"local_sequence"`
	StreamName       string          `json:"stream_name"`
	EventType        string          `json:"event_type"`
	EventLevel       string          `json:"event_level"`
	CreatedAt        time.Time       `json:"created_at"`
	PayloadJSON      json.RawMessage `json:"payload_json"`
	Metadata         json.RawMessage `json:"metadata"`
	SyncStatus       string          `json:"sync_status"`
	GlobalEventID    string          `json:"global_event_id,omitempty"`
	SyncedAt         *time.Time      `json:"synced_at,omitempty"`
	LastErrorCode    string          `json:"last_error_code,omitempty"`
	LastErrorMessage string          `json:"last_error_message,omitempty"`
}

type LocalSyncOutboxItem struct {
	LocalOutboxID    string          `json:"local_outbox_id"`
	LocalRef         string          `json:"local_ref"`
	ItemKind         string          `json:"item_kind"`
	StreamName       string          `json:"stream_name"`
	LocalSequence    int64           `json:"local_sequence"`
	PayloadHash      string          `json:"payload_hash"`
	Status           string          `json:"status"`
	CreatedAt        time.Time       `json:"created_at"`
	LastAttemptAt    *time.Time      `json:"last_attempt_at,omitempty"`
	SyncedAt         *time.Time      `json:"synced_at,omitempty"`
	GlobalRef        string          `json:"global_ref,omitempty"`
	LastErrorCode    string          `json:"last_error_code,omitempty"`
	LastErrorMessage string          `json:"last_error_message,omitempty"`
	PayloadJSON      json.RawMessage `json:"payload_json"`
}

type LocalSyncCursor struct {
	StreamName           string     `json:"stream_name"`
	LastQueuedSequence   int64      `json:"last_queued_sequence"`
	LastPushedSequence   int64      `json:"last_pushed_sequence"`
	LastAcceptedSequence int64      `json:"last_accepted_sequence"`
	LastSuccessAt        *time.Time `json:"last_success_at,omitempty"`
	LastErrorAt          *time.Time `json:"last_error_at,omitempty"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

type LocalSyncConflict struct {
	LocalConflictID  string          `json:"local_conflict_id"`
	LocalRef         string          `json:"local_ref"`
	ConflictType     string          `json:"conflict_type"`
	Status           string          `json:"status"`
	Summary          string          `json:"summary"`
	PayloadJSON      json.RawMessage `json:"payload_json"`
	MainResponseJSON json.RawMessage `json:"main_response_json"`
	CreatedAt        time.Time       `json:"created_at"`
}

type LocalSyncPushRun struct {
	Batch            loomsync.PushBatchResult         `json:"batch"`
	ObjectUploads    []loomsync.SyncedObjectResult    `json:"object_uploads,omitempty"`
	DeletionRequests []loomsync.DeletionRequestResult `json:"deletion_requests,omitempty"`
	LocalStatus      LocalSyncStatus                  `json:"local_status"`
	SubmittedItems   int                              `json:"submitted_items"`
}

func newSyncCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Manage local workspace sync queue",
	}
	cmd.AddCommand(newSyncInitLocalCommand(opts))
	cmd.AddCommand(newSyncAppendTestEventCommand(opts))
	cmd.AddCommand(newSyncCreateTestObjectCommand(opts))
	cmd.AddCommand(newSyncRequestDeleteCommand(opts))
	cmd.AddCommand(newSyncPushCommand(opts))
	cmd.AddCommand(newSyncStatusCommand(opts))
	cmd.AddCommand(newSyncRepairCommand(opts))
	return cmd
}

func newSyncInitLocalCommand(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "init-local",
		Short: "Initialize local workspace sync files",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, config, state, err := opts.loadAll()
			if err != nil {
				return err
			}
			if err := store.EnsureSyncDataDirs(); err != nil {
				return err
			}
			status, err := store.LocalSyncStatus(config, state)
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), status))
			}
			_, err = fmt.Fprintf(opts.out, "sync_root=%s events=%d pending=%d conflicts=%d\n",
				status.Paths.Root,
				status.Counts.Events,
				status.Counts.Pending,
				status.Counts.Conflicts,
			)
			return err
		},
	}
}

func newSyncAppendTestEventCommand(opts *rootOptions) *cobra.Command {
	var message string
	var localEventID string
	var sequence int64
	cmd := &cobra.Command{
		Use:   "append-test-event",
		Short: "Append a deterministic local sync event for smoke testing",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, config, state, err := opts.loadAll()
			if err != nil {
				return err
			}
			if strings.TrimSpace(state.NodeID) == "" {
				return errors.New("node credential is not imported: missing node_id")
			}
			event, err := store.AppendLocalTestEvent(config, state, message, localEventID, sequence)
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), event))
			}
			_, err = fmt.Fprintf(opts.out, "local_event=%s sequence=%d status=%s\n", event.LocalEventID, event.LocalSequence, event.SyncStatus)
			return err
		},
	}
	cmd.Flags().StringVar(&message, "message", "", "Message stored in the test event payload")
	cmd.Flags().StringVar(&localEventID, "local-event-id", "", "Stable local event id to use instead of generating one")
	cmd.Flags().Int64Var(&sequence, "sequence", 0, "Local stream sequence; defaults to next sequence")
	return cmd
}

func newSyncCreateTestObjectCommand(opts *rootOptions) *cobra.Command {
	var projectRef string
	var scopeRef string
	var logicalName string
	var indexPolicy string
	var rawBackupPolicy string
	cmd := &cobra.Command{
		Use:   "create-test-object --path <file>",
		Short: "Queue a small local file object for workspace-to-main sync",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := cmd.Flags().GetString("path")
			if err != nil {
				return err
			}
			store, config, state, err := opts.loadAll()
			if err != nil {
				return err
			}
			if strings.TrimSpace(state.NodeID) == "" {
				return errors.New("node credential is not imported: missing node_id")
			}
			object, err := store.CreateLocalSyncObject(config, state, LocalSyncObjectCreateInput{
				Path:            path,
				ProjectRef:      projectRef,
				ScopeRef:        scopeRef,
				LogicalName:     logicalName,
				IndexPolicy:     indexPolicy,
				RawBackupPolicy: rawBackupPolicy,
			})
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), object))
			}
			_, err = fmt.Fprintf(opts.out, "local_object=%s version=%s sequence=%d status=%s\n", object.LocalObjectID, object.LocalVersionID, object.LocalSequence, object.SyncStatus)
			return err
		},
	}
	cmd.Flags().String("path", "", "File path to queue")
	cmd.Flags().StringVar(&projectRef, "project", "", "Main project ID, slug, or scope key")
	cmd.Flags().StringVar(&scopeRef, "scope", "", "Main scope ID, key, or slug")
	cmd.Flags().StringVar(&logicalName, "name", "", "Logical object name")
	cmd.Flags().StringVar(&indexPolicy, "index-policy", loomsync.IndexPolicyTextLater, "Index policy")
	cmd.Flags().StringVar(&rawBackupPolicy, "raw-backup-policy", loomsync.RawBackupPolicyNormal, "Raw backup policy")
	_ = cmd.MarkFlagRequired("path")
	return cmd
}

func newSyncRequestDeleteCommand(opts *rootOptions) *cobra.Command {
	var localObjectRef string
	var reason string
	cmd := &cobra.Command{
		Use:   "request-delete --local-object <local-ref>",
		Short: "Record a main-side deletion/tombstone request without hard deletion",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, config, state, err := opts.loadAll()
			if err != nil {
				return err
			}
			if strings.TrimSpace(state.NodeID) == "" {
				return errors.New("node credential is not imported: missing node_id")
			}
			if strings.TrimSpace(state.CredentialToken) == "" {
				return errors.New("node credential is not imported: missing credential_token")
			}
			object, err := store.ResolveLocalSyncObject(localObjectRef)
			if err != nil {
				return err
			}
			targetRef := object.MainObjectID
			if strings.TrimSpace(targetRef) == "" {
				targetRef = object.LocalObjectID
			}
			client, err := NewClient(config.MainURL)
			if err != nil {
				return err
			}
			idempotencyKey := "node-agent.deletion-request." + state.NodeID + "." + object.LocalObjectID
			input := loomsync.DeletionRequestInput{
				NodeRef:         state.NodeID,
				CredentialToken: state.CredentialToken,
				IdempotencyKey:  idempotencyKey,
				TargetKind:      "object",
				TargetRef:       targetRef,
				RequestedAction: "tombstone",
				Reason:          reason,
				Metadata: objectJSON(map[string]any{
					"source":           "loom-node-agent",
					"local_object_ref": object.LocalObjectID,
				}),
			}
			envelope, err := client.CreateDeletionRequest(cmd.Context(), correlation.Normalize(opts.correlationID), idempotencyKey, input)
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, envelope)
			}
			_, err = fmt.Fprintf(opts.out, "deletion_request=%s status=%s target=%s\n",
				envelope.Data.Request.DeletionRequestID,
				envelope.Data.Request.Status,
				envelope.Data.Request.TargetRef,
			)
			return err
		},
	}
	cmd.Flags().StringVar(&localObjectRef, "local-object", "", "Local object reference")
	cmd.Flags().StringVar(&reason, "reason", "", "Deletion request reason")
	_ = cmd.MarkFlagRequired("local-object")
	return cmd
}

func newSyncPushCommand(opts *rootOptions) *cobra.Command {
	var once bool
	var maxItems int
	var includeSynced bool
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Push queued local sync items to main",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !once {
				return errors.New("sync push currently requires --once")
			}
			result, err := opts.pushLocalSync(cmd.Context(), maxItems, includeSynced)
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), result))
			}
			_, err = fmt.Fprintf(opts.out, "sync_batch=%s submitted=%d accepted=%d conflicted=%d pending=%d\n",
				result.Batch.Batch.SyncBatchID,
				result.SubmittedItems,
				countSyncItemResults(result.Batch.Items, loomsync.ItemStatusAccepted)+countSyncItemResults(result.Batch.Items, loomsync.ItemStatusDuplicate),
				countSyncItemResults(result.Batch.Items, loomsync.ItemStatusConflicted),
				result.LocalStatus.Counts.Pending,
			)
			return err
		},
	}
	cmd.Flags().BoolVar(&once, "once", false, "Run one push cycle and exit")
	cmd.Flags().IntVar(&maxItems, "max-items", 100, "Maximum queued items to push")
	cmd.Flags().BoolVar(&includeSynced, "include-synced", false, "Also submit previously accepted local items for idempotency checks")
	return cmd
}

func newSyncStatusCommand(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print local workspace sync status",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, config, state, err := opts.loadAll()
			if err != nil {
				return err
			}
			status, err := store.LocalSyncStatus(config, state)
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), status))
			}
			_, err = fmt.Fprintf(opts.out, "events=%d objects=%d deletions=%d pending=%d accepted=%d duplicates=%d conflicted=%d failed=%d conflicts=%d\n",
				status.Counts.Events,
				status.Counts.Objects,
				status.Counts.Deletions,
				status.Counts.Pending,
				status.Counts.Accepted,
				status.Counts.Duplicates,
				status.Counts.Conflicted,
				status.Counts.Failed,
				status.Counts.Conflicts,
			)
			return err
		},
	}
}

func (opts *rootOptions) pushLocalSync(ctx context.Context, maxItems int, includeSynced bool) (LocalSyncPushRun, error) {
	store, config, state, err := opts.loadAll()
	if err != nil {
		return LocalSyncPushRun{}, err
	}
	return pushLocalSyncOnce(ctx, store, config, state, correlation.Normalize(opts.correlationID), maxItems, includeSynced)
}

func pushLocalSyncOnce(ctx context.Context, store Store, config Config, state State, correlationID string, maxItems int, includeSynced bool) (LocalSyncPushRun, error) {
	store, unlock, err := store.lockLocalSync(ctx)
	if err != nil {
		return LocalSyncPushRun{}, err
	}
	defer unlock()
	if strings.TrimSpace(state.NodeID) == "" {
		return LocalSyncPushRun{}, errors.New("node credential is not imported: missing node_id")
	}
	if strings.TrimSpace(state.CredentialToken) == "" {
		return LocalSyncPushRun{}, errors.New("node credential is not imported: missing credential_token")
	}
	items, err := store.LoadSyncOutbox()
	if err != nil {
		return LocalSyncPushRun{}, err
	}
	eventsByRef, err := store.localSyncEventsByRef()
	if err != nil {
		return LocalSyncPushRun{}, err
	}
	objectsByRef, err := store.localSyncObjectsByRef()
	if err != nil {
		return LocalSyncPushRun{}, err
	}
	objectsByVersion, err := store.localSyncObjectsByObjectVersion()
	if err != nil {
		return LocalSyncPushRun{}, err
	}
	deletionsByRef, err := store.localSyncDeletionsByRef()
	if err != nil {
		return LocalSyncPushRun{}, err
	}
	pending := eligibleOutboxItems(items, includeSynced)
	if maxItems <= 0 || maxItems > len(pending) {
		maxItems = len(pending)
	}
	pending = pending[:maxItems]
	if len(pending) == 0 {
		status, statusErr := store.LocalSyncStatus(config, state)
		if statusErr != nil {
			return LocalSyncPushRun{}, statusErr
		}
		return LocalSyncPushRun{LocalStatus: status}, nil
	}
	eventOutbox := []LocalSyncOutboxItem{}
	metadataOutbox := []LocalSyncOutboxItem{}
	objectOutbox := []LocalSyncOutboxItem{}
	deletionOutbox := []LocalSyncOutboxItem{}
	for _, outboxItem := range pending {
		switch outboxItem.ItemKind {
		case loomsync.ItemKindEvent:
			eventOutbox = append(eventOutbox, outboxItem)
		case loomsync.ItemKindObjectMetadata:
			metadataOutbox = append(metadataOutbox, outboxItem)
		case loomsync.ItemKindObjectBlob:
			objectOutbox = append(objectOutbox, outboxItem)
		case loomsync.ItemKindDeletionRequest:
			deletionOutbox = append(deletionOutbox, outboxItem)
		default:
			return LocalSyncPushRun{}, fmt.Errorf("unsupported local sync item kind: %s", outboxItem.ItemKind)
		}
	}
	pushItems := make([]loomsync.PushBatchItem, 0, len(eventOutbox))
	for _, outboxItem := range eventOutbox {
		event, ok := eventsByRef[outboxItem.LocalRef]
		if !ok {
			return LocalSyncPushRun{}, fmt.Errorf("local sync event not found for outbox item %s", outboxItem.LocalRef)
		}
		createdAt := event.CreatedAt
		pushItems = append(pushItems, loomsync.PushBatchItem{
			LocalRef:      event.LocalEventID,
			ItemKind:      loomsync.ItemKindEvent,
			StreamName:    event.StreamName,
			LocalSequence: event.LocalSequence,
			EventType:     event.EventType,
			EventLevel:    event.EventLevel,
			CreatedAt:     &createdAt,
			PayloadJSON:   event.PayloadJSON,
			Metadata:      localSyncItemMetadata(event.Metadata, outboxItem),
		})
	}
	for _, item := range metadataOutbox {
		createdAt := item.CreatedAt
		pushItems = append(pushItems, loomsync.PushBatchItem{LocalRef: item.LocalRef, ItemKind: item.ItemKind,
			StreamName: item.StreamName, LocalSequence: item.LocalSequence, PayloadJSON: item.PayloadJSON,
			CreatedAt: &createdAt, Metadata: localSyncItemMetadata(nil, item)})
	}
	now := time.Now().UTC()
	pendingOutboxIDs := make(map[string]struct{}, len(pending))
	for _, pendingItem := range pending {
		pendingOutboxIDs[pendingItem.LocalOutboxID] = struct{}{}
	}
	for idx := range items {
		if _, ok := pendingOutboxIDs[items[idx].LocalOutboxID]; ok {
			items[idx].LastAttemptAt = &now
		}
	}
	if err := store.SaveSyncOutbox(items); err != nil {
		return LocalSyncPushRun{}, err
	}
	client, err := NewClient(config.MainURL)
	if err != nil {
		return LocalSyncPushRun{}, err
	}
	var batchResult loomsync.PushBatchResult
	if len(pushItems) > 0 {
		batchKind := loomsync.BatchKindEvents
		if len(metadataOutbox) > 0 {
			batchKind = loomsync.BatchKindObjectMetadata
			if len(eventOutbox) > 0 {
				batchKind = loomsync.BatchKindMixed
			}
		}
		input := loomsync.PushBatchInput{
			NodeRef:         state.NodeID,
			CredentialToken: state.CredentialToken,
			IdempotencyKey:  syncBatchIdempotencyKey(state.NodeID, pushItems),
			BatchKind:       batchKind,
			Items:           pushItems,
			Metadata: objectJSON(map[string]any{
				"source":   "loom-node-agent",
				"node_key": config.NodeKey,
				"slice":    "12_part_1",
			}),
		}
		envelope, err := client.PushSyncBatch(ctx, correlationID, input.IdempotencyKey, input)
		if err != nil {
			return LocalSyncPushRun{}, err
		}
		batchResult = envelope.Data
		if err := store.ApplySyncPushResult(envelope.Data); err != nil {
			return LocalSyncPushRun{}, err
		}
	}
	objectUploads := make([]loomsync.SyncedObjectResult, 0, len(objectOutbox))
	objectUploadErrors := make([]error, 0)
	for _, outboxItem := range objectOutbox {
		object, ok := localSyncObjectForOutbox(outboxItem, objectsByRef, objectsByVersion)
		if !ok {
			return LocalSyncPushRun{}, fmt.Errorf("local sync object not found for outbox item %s", outboxItem.LocalRef)
		}
		input, err := buildSyncedObjectInput(state, outboxItem, object)
		if err != nil {
			return LocalSyncPushRun{}, err
		}
		envelope, err := client.UploadSyncedObject(ctx, correlationID, input.IdempotencyKey, input)
		if err != nil {
			objectUploadErrors = append(objectUploadErrors, fmt.Errorf("upload local sync object %s: %w", outboxItem.LocalRef, err))
			continue
		}
		objectUploads = append(objectUploads, envelope.Data)
		if err := store.ApplySyncedObjectResult(envelope.Data); err != nil {
			return LocalSyncPushRun{}, err
		}
	}
	deletionRequests := make([]loomsync.DeletionRequestResult, 0, len(deletionOutbox))
	for _, outboxItem := range deletionOutbox {
		deletion, ok := deletionsByRef[outboxItem.LocalRef]
		if !ok {
			return LocalSyncPushRun{}, fmt.Errorf("local sync deletion request not found for outbox item %s", outboxItem.LocalRef)
		}
		input := buildDeletionRequestInput(state, outboxItem, deletion)
		envelope, err := client.CreateDeletionRequest(ctx, correlationID, input.IdempotencyKey, input)
		if err != nil {
			return LocalSyncPushRun{}, err
		}
		deletionRequests = append(deletionRequests, envelope.Data)
		if err := store.ApplyDeletionRequestResult(outboxItem, envelope.Data); err != nil {
			return LocalSyncPushRun{}, err
		}
	}
	status, err := store.LocalSyncStatus(config, state)
	if err != nil {
		return LocalSyncPushRun{}, err
	}
	run := LocalSyncPushRun{
		Batch:            batchResult,
		ObjectUploads:    objectUploads,
		DeletionRequests: deletionRequests,
		LocalStatus:      status,
		SubmittedItems:   len(pushItems) + len(objectUploads) + len(deletionRequests),
	}
	if len(objectUploadErrors) > 0 {
		return run, errors.Join(objectUploadErrors...)
	}
	return run, nil
}

// All shared queue transactions use the same cross-process lock. Per-worker
// locks do not serialize different watched roots or an operator's sync push.
func (s Store) lockLocalSync(ctx context.Context) (Store, func() error, error) {
	if s.syncLocked {
		return s, func() error { return nil }, nil
	}
	wait, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	unlock, err := noderuntime.NewStore(s.DataDir).AcquireWorkerExecutionLock(wait, "node-agent.shared-sync-queue")
	if err != nil {
		return s, nil, err
	}
	s.syncLocked = true
	return s, unlock, nil
}

func (s Store) EnsureSyncDataDirs() error {
	s, unlock, err := s.lockLocalSync(context.Background())
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.EnsureDataDirs(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.syncRoot(), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(s.syncEventsPath()); errors.Is(err, fs.ErrNotExist) {
		if err := s.SaveSyncEvents([]LocalSyncEvent{}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if _, err := os.Stat(s.syncObjectsPath()); errors.Is(err, fs.ErrNotExist) {
		if err := s.SaveSyncObjects([]LocalSyncObject{}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if _, err := os.Stat(s.syncDeletionsPath()); errors.Is(err, fs.ErrNotExist) {
		if err := s.SaveSyncDeletions([]LocalSyncDeletionRequest{}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if _, err := os.Stat(s.syncOutboxPath()); errors.Is(err, fs.ErrNotExist) {
		if err := s.SaveSyncOutbox([]LocalSyncOutboxItem{}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if _, err := os.Stat(s.syncCursorsPath()); errors.Is(err, fs.ErrNotExist) {
		if err := s.SaveSyncCursors([]LocalSyncCursor{}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if _, err := os.Stat(s.syncConflictsPath()); errors.Is(err, fs.ErrNotExist) {
		if err := s.SaveSyncConflicts([]LocalSyncConflict{}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	return nil
}

func (s Store) AppendLocalTestEvent(config Config, state State, message, localEventID string, sequence int64) (LocalSyncEvent, error) {
	s, unlock, err := s.lockLocalSync(context.Background())
	if err != nil {
		return LocalSyncEvent{}, err
	}
	defer unlock()
	if err := s.EnsureSyncDataDirs(); err != nil {
		return LocalSyncEvent{}, err
	}
	localEventID = strings.TrimSpace(localEventID)
	if localEventID == "" {
		localEventID = ids.NewLocalEventID()
	}
	if sequence <= 0 {
		next, err := s.nextLocalSequence(loomsync.StreamEvents)
		if err != nil {
			return LocalSyncEvent{}, err
		}
		sequence = next
	}
	now := time.Now().UTC()
	event := LocalSyncEvent{
		LocalEventID:  localEventID,
		LocalSequence: sequence,
		StreamName:    loomsync.StreamEvents,
		EventType:     events.TypeNodeLocalTestEvent,
		EventLevel:    "node_activity",
		CreatedAt:     now,
		PayloadJSON: objectJSON(map[string]any{
			"message":  strings.TrimSpace(message),
			"node_id":  state.NodeID,
			"node_key": config.NodeKey,
		}),
		Metadata: objectJSON(map[string]any{
			"source":   "loom-node-agent",
			"node_key": config.NodeKey,
			"slice":    "12_part_1",
		}),
		SyncStatus: localSyncStatusPending,
	}
	events, err := s.LoadSyncEvents()
	if err != nil {
		return LocalSyncEvent{}, err
	}
	events = append(events, event)
	if err := s.SaveSyncEvents(events); err != nil {
		return LocalSyncEvent{}, err
	}
	outbox, err := s.LoadSyncOutbox()
	if err != nil {
		return LocalSyncEvent{}, err
	}
	outbox = append(outbox, LocalSyncOutboxItem{
		LocalOutboxID: ids.NewLocalOutboxID(),
		LocalRef:      event.LocalEventID,
		ItemKind:      loomsync.ItemKindEvent,
		StreamName:    event.StreamName,
		LocalSequence: event.LocalSequence,
		PayloadHash:   rawJSONHash(event.PayloadJSON),
		Status:        localSyncStatusPending,
		CreatedAt:     now,
		PayloadJSON:   event.PayloadJSON,
	})
	if err := s.SaveSyncOutbox(outbox); err != nil {
		return LocalSyncEvent{}, err
	}
	if err := s.updateLocalQueuedCursor(event.StreamName, event.LocalSequence, now); err != nil {
		return LocalSyncEvent{}, err
	}
	return event, nil
}

func (s Store) CreateLocalSyncObject(config Config, state State, input LocalSyncObjectCreateInput) (LocalSyncObject, error) {
	s, unlock, err := s.lockLocalSync(context.Background())
	if err != nil {
		return LocalSyncObject{}, err
	}
	defer unlock()
	if err := s.EnsureSyncDataDirs(); err != nil {
		return LocalSyncObject{}, err
	}
	sourcePath, err := filepath.Abs(strings.TrimSpace(input.Path))
	if err != nil {
		return LocalSyncObject{}, err
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		return LocalSyncObject{}, err
	}
	if info.IsDir() {
		return LocalSyncObject{}, fmt.Errorf("source path is a directory")
	}
	if strings.TrimSpace(input.ProjectRef) == "" && strings.TrimSpace(input.ScopeRef) == "" {
		return LocalSyncObject{}, fmt.Errorf("--project or --scope is required")
	}
	if strings.TrimSpace(input.ProjectRef) != "" && strings.TrimSpace(input.ScopeRef) != "" {
		return LocalSyncObject{}, fmt.Errorf("provide either --project or --scope, not both")
	}
	content, finalInfo, err := readStableInlineFile(sourcePath, loomsync.MaxInlineObjectUploadBytes)
	if err != nil {
		return LocalSyncObject{}, err
	}
	logicalName := strings.TrimSpace(input.LogicalName)
	if logicalName == "" {
		logicalName = filepath.Base(sourcePath)
	}
	indexPolicy := strings.TrimSpace(input.IndexPolicy)
	if indexPolicy == "" {
		indexPolicy = loomsync.IndexPolicyTextLater
	}
	rawBackupPolicy := strings.TrimSpace(input.RawBackupPolicy)
	if rawBackupPolicy == "" {
		rawBackupPolicy = loomsync.RawBackupPolicyNormal
	}
	mimeType := detectLocalMIME(sourcePath, content)
	classification := classifyLocalSyncFile(sourcePath, mimeType, content, finalInfo.Size(), nil)
	indexPolicy = effectiveLocalIndexPolicy(indexPolicy, classification)
	sequence, err := s.nextLocalSequence(loomsync.StreamObjectBlobs)
	if err != nil {
		return LocalSyncObject{}, err
	}
	now := time.Now().UTC()
	var sourceCreatedAt *time.Time
	var sourceCreatedBasis string
	if observation, detectErr := filesystemmeta.DetectPath(sourcePath, filesystemmeta.DetectOptions{RootPath: filepath.Dir(sourcePath)}); detectErr == nil {
		sourceCreatedAt = observation.SourceCreatedAt
		sourceCreatedBasis = observation.SourceCreatedBasis
	}
	object := LocalSyncObject{
		LocalObjectID:        ids.NewObjectID(),
		LocalVersionID:       ids.NewObjectVersionID(),
		LocalSequence:        sequence,
		ProjectRef:           strings.TrimSpace(input.ProjectRef),
		ScopeRef:             strings.TrimSpace(input.ScopeRef),
		LogicalName:          logicalName,
		SourcePath:           sourcePath,
		ContentPath:          sourcePath,
		SourceMtime:          finalInfo.ModTime().UTC(),
		SourceMtimeBasis:     filesystemmeta.SourceTimeBasisFilesystemMtime,
		SourceCreatedAt:      sourceCreatedAt,
		SourceCreatedBasis:   sourceCreatedBasis,
		SizeBytes:            finalInfo.Size(),
		MimeType:             mimeType,
		HashURI:              "sha256:" + rawBytesHashHex(content),
		IndexPolicy:          indexPolicy,
		RawBackupPolicy:      rawBackupPolicy,
		FileClass:            classification.FileClass,
		ClassificationSource: classification.ClassificationSource,
		IndexingState:        classification.IndexingState,
		IndexingReason:       classification.Reason,
		CreatedAt:            now,
		Metadata: objectJSON(map[string]any{
			"source":                "loom-node-agent",
			"node_key":              config.NodeKey,
			"slice":                 "12_part_2",
			"file_class":            classification.FileClass,
			"classification_source": classification.ClassificationSource,
			"indexing_state":        classification.IndexingState,
			"indexing_reason":       classification.Reason,
		}),
		SyncStatus: localSyncStatusPending,
	}
	objects, err := s.LoadSyncObjects()
	if err != nil {
		return LocalSyncObject{}, err
	}
	objects = append(objects, object)
	if err := s.SaveSyncObjects(objects); err != nil {
		return LocalSyncObject{}, err
	}
	outbox, err := s.LoadSyncOutbox()
	if err != nil {
		return LocalSyncObject{}, err
	}
	payload := localObjectPayload(object)
	outbox = append(outbox, LocalSyncOutboxItem{
		LocalOutboxID: ids.NewLocalOutboxID(),
		LocalRef:      object.LocalObjectID,
		ItemKind:      loomsync.ItemKindObjectBlob,
		StreamName:    loomsync.StreamObjectBlobs,
		LocalSequence: object.LocalSequence,
		PayloadHash:   rawJSONHash(payload),
		Status:        localSyncStatusPending,
		CreatedAt:     now,
		PayloadJSON:   payload,
	})
	if err := s.SaveSyncOutbox(outbox); err != nil {
		return LocalSyncObject{}, err
	}
	if err := s.updateLocalQueuedCursor(loomsync.StreamObjectBlobs, object.LocalSequence, now); err != nil {
		return LocalSyncObject{}, err
	}
	_ = state
	return object, nil
}

func (s Store) QueueWatchedRootObject(config Config, state State, action watchedroots.OutputAction, contentPath string) (LocalSyncObject, LocalSyncOutboxItem, error) {
	s, unlock, err := s.lockLocalSync(context.Background())
	if err != nil {
		return LocalSyncObject{}, LocalSyncOutboxItem{}, err
	}
	defer unlock()
	if err := s.EnsureSyncDataDirs(); err != nil {
		return LocalSyncObject{}, LocalSyncOutboxItem{}, err
	}
	if strings.TrimSpace(state.NodeID) == "" {
		return LocalSyncObject{}, LocalSyncOutboxItem{}, errors.New("node credential is not imported: missing node_id")
	}
	contentPath, err = filepath.Abs(strings.TrimSpace(contentPath))
	if err != nil {
		return LocalSyncObject{}, LocalSyncOutboxItem{}, err
	}
	info, err := os.Stat(contentPath)
	if err != nil {
		return LocalSyncObject{}, LocalSyncOutboxItem{}, err
	}
	if info.IsDir() {
		return LocalSyncObject{}, LocalSyncOutboxItem{}, fmt.Errorf("watched-root source path is a directory")
	}
	content, finalInfo, err := readStableInlineFile(contentPath, loomsync.MaxInlineObjectUploadBytes)
	if err != nil {
		return LocalSyncObject{}, LocalSyncOutboxItem{}, err
	}
	hashURI := "sha256:" + rawBytesHashHex(content)
	if action.ContentHashURI != "" && hashURI != action.ContentHashURI {
		return LocalSyncObject{}, LocalSyncOutboxItem{}, fmt.Errorf("watched-root file hash changed for %s: expected %s got %s", action.RelativePath, action.ContentHashURI, hashURI)
	}
	localObjectID := strings.TrimSpace(action.LocalObjectID)
	if localObjectID == "" {
		localObjectID = ids.NewObjectID()
	}
	localVersionID := strings.TrimSpace(action.LocalVersionID)
	if localVersionID == "" {
		localVersionID = ids.NewObjectVersionID()
	}
	objects, err := s.LoadSyncObjects()
	if err != nil {
		return LocalSyncObject{}, LocalSyncOutboxItem{}, err
	}
	outbox, err := s.LoadSyncOutbox()
	if err != nil {
		return LocalSyncObject{}, LocalSyncOutboxItem{}, err
	}
	for _, object := range objects {
		if object.LocalObjectID == localObjectID && object.LocalVersionID == localVersionID {
			for _, item := range outbox {
				if item.LocalRef == localObjectID &&
					item.ItemKind == loomsync.ItemKindObjectBlob &&
					metadataString(item.PayloadJSON, "local_version_ref") == localVersionID {
					return object, item, nil
				}
			}
			now := time.Now().UTC()
			payload := localObjectPayload(object)
			item := LocalSyncOutboxItem{
				LocalOutboxID: ids.NewLocalOutboxID(),
				LocalRef:      object.LocalObjectID,
				ItemKind:      loomsync.ItemKindObjectBlob,
				StreamName:    loomsync.StreamObjectBlobs,
				LocalSequence: object.LocalSequence,
				PayloadHash:   rawJSONHash(payload),
				Status:        localSyncStatusPending,
				CreatedAt:     now,
				PayloadJSON:   payload,
			}
			outbox = append(outbox, item)
			if err := s.SaveSyncOutbox(outbox); err != nil {
				return LocalSyncObject{}, LocalSyncOutboxItem{}, err
			}
			if err := s.updateLocalQueuedCursor(loomsync.StreamObjectBlobs, object.LocalSequence, now); err != nil {
				return LocalSyncObject{}, LocalSyncOutboxItem{}, err
			}
			return object, item, nil
		}
	}
	sequence, err := s.nextLocalSequence(loomsync.StreamObjectBlobs)
	if err != nil {
		return LocalSyncObject{}, LocalSyncOutboxItem{}, err
	}
	now := time.Now().UTC()
	sourceMtime := finalInfo.ModTime().UTC()
	if action.ModifiedAt != nil && !action.ModifiedAt.IsZero() {
		sourceMtime = action.ModifiedAt.UTC()
	}
	indexPolicy := mapWatchedRootIndexPolicy(action.IndexPolicy)
	mimeType := detectLocalMIME(contentPath, content)
	classification := classifyLocalSyncFile(contentPath, mimeType, content, finalInfo.Size(), action.Fidelity)
	indexPolicy = effectiveLocalIndexPolicy(indexPolicy, classification)
	object := LocalSyncObject{
		LocalObjectID:        localObjectID,
		LocalVersionID:       localVersionID,
		LocalSequence:        sequence,
		ProjectRef:           strings.TrimSpace(action.ProjectRef),
		ScopeRef:             strings.TrimSpace(action.ScopeRef),
		LogicalName:          strings.TrimSpace(action.LogicalName),
		SourcePath:           strings.TrimSpace(action.SourcePath),
		ContentPath:          contentPath,
		SourceMtime:          sourceMtime,
		SourceMtimeBasis:     filesystemmeta.SourceTimeBasisFilesystemMtime,
		SizeBytes:            finalInfo.Size(),
		MimeType:             mimeType,
		HashURI:              hashURI,
		IndexPolicy:          indexPolicy,
		RawBackupPolicy:      loomsync.RawBackupPolicyNormal,
		FileClass:            classification.FileClass,
		ClassificationSource: classification.ClassificationSource,
		IndexingState:        classification.IndexingState,
		IndexingReason:       classification.Reason,
		CreatedAt:            now,
		Metadata: objectJSON(map[string]any{
			"source":                 "loom-node-agent",
			"node_key":               config.NodeKey,
			"slice":                  "10_part_1",
			"watched_root":           action.RootKey,
			"relative_path":          action.RelativePath,
			"filesystem_observation": action.Fidelity,
			"file_class":             classification.FileClass,
			"classification_source":  classification.ClassificationSource,
			"indexing_state":         classification.IndexingState,
			"indexing_reason":        classification.Reason,
		}),
		SyncStatus: localSyncStatusPending,
	}
	if action.Fidelity != nil {
		object.SourceCreatedAt = action.Fidelity.SourceCreatedAt
		object.SourceCreatedBasis = action.Fidelity.SourceCreatedBasis
	}
	if object.LogicalName == "" {
		object.LogicalName = action.RelativePath
	}
	if object.SourcePath == "" {
		object.SourcePath = contentPath
	}
	objects = append(objects, object)
	if err := s.SaveSyncObjects(objects); err != nil {
		return LocalSyncObject{}, LocalSyncOutboxItem{}, err
	}
	payload := localObjectPayload(object)
	item := LocalSyncOutboxItem{
		LocalOutboxID: ids.NewLocalOutboxID(),
		LocalRef:      object.LocalObjectID,
		ItemKind:      loomsync.ItemKindObjectBlob,
		StreamName:    loomsync.StreamObjectBlobs,
		LocalSequence: object.LocalSequence,
		PayloadHash:   rawJSONHash(payload),
		Status:        localSyncStatusPending,
		CreatedAt:     now,
		PayloadJSON:   payload,
	}
	outbox = append(outbox, item)
	if err := s.SaveSyncOutbox(outbox); err != nil {
		return LocalSyncObject{}, LocalSyncOutboxItem{}, err
	}
	if err := s.updateLocalQueuedCursor(loomsync.StreamObjectBlobs, object.LocalSequence, now); err != nil {
		return LocalSyncObject{}, LocalSyncOutboxItem{}, err
	}
	return object, item, nil
}

func (s Store) QueueWatchedRootDeletion(config Config, state State, action watchedroots.OutputAction) (LocalSyncDeletionRequest, LocalSyncOutboxItem, error) {
	s, unlock, err := s.lockLocalSync(context.Background())
	if err != nil {
		return LocalSyncDeletionRequest{}, LocalSyncOutboxItem{}, err
	}
	defer unlock()
	if err := s.EnsureSyncDataDirs(); err != nil {
		return LocalSyncDeletionRequest{}, LocalSyncOutboxItem{}, err
	}
	if strings.TrimSpace(state.NodeID) == "" {
		return LocalSyncDeletionRequest{}, LocalSyncOutboxItem{}, errors.New("node credential is not imported: missing node_id")
	}
	deletions, err := s.LoadSyncDeletions()
	if err != nil {
		return LocalSyncDeletionRequest{}, LocalSyncOutboxItem{}, err
	}
	outbox, err := s.LoadSyncOutbox()
	if err != nil {
		return LocalSyncDeletionRequest{}, LocalSyncOutboxItem{}, err
	}
	targetRef := strings.TrimSpace(action.LocalObjectID)
	if strings.TrimSpace(action.MainObjectID) != "" {
		targetRef = strings.TrimSpace(action.MainObjectID)
	}
	if targetRef == "" {
		targetRef = strings.TrimSpace(action.RelativePath)
	}
	for _, deletion := range deletions {
		if deletion.LocalObjectID == action.LocalObjectID && deletion.RelativePath == action.RelativePath && deletion.SyncStatus == localSyncStatusPending {
			for _, item := range outbox {
				if item.LocalRef == deletion.LocalDeletionID && item.ItemKind == loomsync.ItemKindDeletionRequest {
					return deletion, item, nil
				}
			}
		}
	}
	sequence, err := s.nextLocalSequence(loomsync.StreamDeletionRequests)
	if err != nil {
		return LocalSyncDeletionRequest{}, LocalSyncOutboxItem{}, err
	}
	now := time.Now().UTC()
	deletion := LocalSyncDeletionRequest{
		LocalDeletionID: ids.NewDeletionRequestID(),
		LocalSequence:   sequence,
		RootKey:         action.RootKey,
		RelativePath:    action.RelativePath,
		LocalObjectID:   action.LocalObjectID,
		MainObjectID:    action.MainObjectID,
		ContentHashURI:  action.ContentHashURI,
		TargetKind:      "object",
		TargetRef:       targetRef,
		RequestedAction: "tombstone",
		Reason:          action.Reason,
		CreatedAt:       now,
		Metadata: objectJSON(map[string]any{
			"source":        "loom-node-agent",
			"node_key":      config.NodeKey,
			"slice":         "10_part_1",
			"watched_root":  action.RootKey,
			"relative_path": action.RelativePath,
		}),
		SyncStatus: localSyncStatusPending,
	}
	deletions = append(deletions, deletion)
	if err := s.SaveSyncDeletions(deletions); err != nil {
		return LocalSyncDeletionRequest{}, LocalSyncOutboxItem{}, err
	}
	payload := localDeletionPayload(deletion)
	item := LocalSyncOutboxItem{
		LocalOutboxID: ids.NewLocalOutboxID(),
		LocalRef:      deletion.LocalDeletionID,
		ItemKind:      loomsync.ItemKindDeletionRequest,
		StreamName:    loomsync.StreamDeletionRequests,
		LocalSequence: deletion.LocalSequence,
		PayloadHash:   rawJSONHash(payload),
		Status:        localSyncStatusPending,
		CreatedAt:     now,
		PayloadJSON:   payload,
	}
	outbox = append(outbox, item)
	if err := s.SaveSyncOutbox(outbox); err != nil {
		return LocalSyncDeletionRequest{}, LocalSyncOutboxItem{}, err
	}
	if err := s.updateLocalQueuedCursor(loomsync.StreamDeletionRequests, deletion.LocalSequence, now); err != nil {
		return LocalSyncDeletionRequest{}, LocalSyncOutboxItem{}, err
	}
	return deletion, item, nil
}

func mapWatchedRootIndexPolicy(mode string) string {
	switch mode {
	case watchedroots.IndexModeNone:
		return loomsync.IndexPolicyNone
	case watchedroots.IndexModeMetadataOnly:
		return loomsync.IndexPolicyMetadataOnly
	default:
		return loomsync.IndexPolicyTextLater
	}
}

func (s Store) ApplySyncPushResult(result loomsync.PushBatchResult) error {
	s, unlock, err := s.lockLocalSync(context.Background())
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.EnsureSyncDataDirs(); err != nil {
		return err
	}
	events, err := s.LoadSyncEvents()
	if err != nil {
		return err
	}
	outbox, err := s.LoadSyncOutbox()
	if err != nil {
		return err
	}
	conflicts, err := s.LoadSyncConflicts()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := s.applyMetadataAcknowledgements(outbox, result.Items); err != nil {
		return err
	}
	for _, item := range result.Items {
		localOutboxID := metadataString(item.Metadata, "local_outbox_id")
		for idx := range events {
			if events[idx].LocalEventID != item.LocalRef {
				continue
			}
			if events[idx].SyncStatus != localSyncStatusPending && events[idx].SyncStatus != loomsync.ItemStatusFailed {
				continue
			}
			events[idx].SyncStatus = item.Status
			if item.Status == loomsync.ItemStatusAccepted || item.Status == loomsync.ItemStatusDuplicate {
				events[idx].GlobalEventID = item.GlobalRef
				events[idx].SyncedAt = &now
				events[idx].LastErrorCode = ""
				events[idx].LastErrorMessage = ""
			} else {
				events[idx].LastErrorCode = item.ErrorCode
				events[idx].LastErrorMessage = item.ErrorMessage
			}
		}
		for idx := range outbox {
			if localOutboxID != "" {
				if outbox[idx].LocalOutboxID != localOutboxID {
					continue
				}
			} else if outbox[idx].LocalRef != item.LocalRef || outbox[idx].Status != localSyncStatusPending {
				continue
			}
			outbox[idx].Status = item.Status
			if item.Status == loomsync.ItemStatusAccepted || item.Status == loomsync.ItemStatusDuplicate {
				outbox[idx].GlobalRef = item.GlobalRef
				outbox[idx].SyncedAt = &now
				outbox[idx].LastErrorCode = ""
				outbox[idx].LastErrorMessage = ""
			} else {
				outbox[idx].LastErrorCode = item.ErrorCode
				outbox[idx].LastErrorMessage = item.ErrorMessage
			}
		}
		if item.Status == loomsync.ItemStatusConflicted {
			conflicts = append(conflicts, LocalSyncConflict{
				LocalConflictID:  ids.NewLocalConflictID(),
				LocalRef:         item.LocalRef,
				ConflictType:     loomsync.ConflictDuplicatePayloadMismatch,
				Status:           "open",
				Summary:          item.ErrorMessage,
				PayloadJSON:      objectJSON(map[string]any{"local_ref": item.LocalRef}),
				MainResponseJSON: marshalRaw(item),
				CreatedAt:        now,
			})
		}
	}
	if err := s.SaveSyncEvents(events); err != nil {
		return err
	}
	if err := s.SaveSyncOutbox(outbox); err != nil {
		return err
	}
	if err := s.SaveSyncConflicts(conflicts); err != nil {
		return err
	}
	for _, cursor := range result.Cursors {
		if err := s.applyRemoteCursor(cursor, now); err != nil {
			return err
		}
	}
	return nil
}

func (s Store) ApplySyncedObjectResult(result loomsync.SyncedObjectResult) error {
	s, unlock, err := s.lockLocalSync(context.Background())
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.EnsureSyncDataDirs(); err != nil {
		return err
	}
	objects, err := s.LoadSyncObjects()
	if err != nil {
		return err
	}
	outbox, err := s.LoadSyncOutbox()
	if err != nil {
		return err
	}
	conflicts, err := s.LoadSyncConflicts()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	localOutboxID := metadataString(result.Item.Metadata, "local_outbox_id")
	localVersionID := ""
	if localOutboxID != "" {
		for _, item := range outbox {
			if item.LocalOutboxID == localOutboxID {
				localVersionID = metadataString(item.PayloadJSON, "local_version_ref")
				break
			}
		}
	}
	for idx := range objects {
		if objects[idx].LocalObjectID != result.Item.LocalRef {
			continue
		}
		if localVersionID != "" && objects[idx].LocalVersionID != localVersionID {
			continue
		}
		objects[idx].SyncStatus = result.Item.Status
		if result.Item.Status == loomsync.ItemStatusAccepted || result.Item.Status == loomsync.ItemStatusDuplicate {
			objects[idx].MainObjectID = result.ObjectID
			objects[idx].MainVersionID = result.VersionID
			objects[idx].MainBlobID = result.BlobID
			objects[idx].SyncedAt = &now
			objects[idx].LastErrorCode = ""
			objects[idx].LastErrorMessage = ""
		} else {
			objects[idx].LastErrorCode = result.Item.ErrorCode
			objects[idx].LastErrorMessage = result.Item.ErrorMessage
		}
	}
	for idx := range outbox {
		if localOutboxID != "" {
			if outbox[idx].LocalOutboxID != localOutboxID {
				continue
			}
		} else if outbox[idx].LocalRef != result.Item.LocalRef || outbox[idx].ItemKind != loomsync.ItemKindObjectBlob {
			continue
		}
		if outbox[idx].Status != localSyncStatusPending && outbox[idx].Status != loomsync.ItemStatusFailed {
			continue
		}
		outbox[idx].Status = result.Item.Status
		outbox[idx].GlobalRef = result.ObjectID
		if result.Item.Status == loomsync.ItemStatusAccepted || result.Item.Status == loomsync.ItemStatusDuplicate {
			outbox[idx].SyncedAt = &now
			outbox[idx].LastErrorCode = ""
			outbox[idx].LastErrorMessage = ""
		} else {
			outbox[idx].LastErrorCode = result.Item.ErrorCode
			outbox[idx].LastErrorMessage = result.Item.ErrorMessage
		}
	}
	if result.Conflict != nil {
		conflicts = append(conflicts, LocalSyncConflict{
			LocalConflictID:  ids.NewLocalConflictID(),
			LocalRef:         result.Item.LocalRef,
			ConflictType:     result.Conflict.ConflictType,
			Status:           result.Conflict.Status,
			Summary:          result.Conflict.Summary,
			PayloadJSON:      result.Conflict.LocalPayload,
			MainResponseJSON: result.Conflict.MainPayload,
			CreatedAt:        now,
		})
	}
	if err := s.SaveSyncObjects(objects); err != nil {
		return err
	}
	if err := s.SaveSyncOutbox(outbox); err != nil {
		return err
	}
	if err := s.SaveSyncConflicts(conflicts); err != nil {
		return err
	}
	if result.Cursor.StreamName != "" {
		if err := s.applyRemoteCursor(result.Cursor, now); err != nil {
			return err
		}
	}
	if err := s.updateWatchedRootStateFromSyncedObject(objects, result, now); err != nil {
		return err
	}
	return nil
}

func (s Store) ApplyDeletionRequestResult(outboxItem LocalSyncOutboxItem, result loomsync.DeletionRequestResult) error {
	s, unlock, err := s.lockLocalSync(context.Background())
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.EnsureSyncDataDirs(); err != nil {
		return err
	}
	deletions, err := s.LoadSyncDeletions()
	if err != nil {
		return err
	}
	outbox, err := s.LoadSyncOutbox()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	var matched *LocalSyncDeletionRequest
	for idx := range deletions {
		if deletions[idx].LocalDeletionID != outboxItem.LocalRef {
			continue
		}
		deletions[idx].SyncStatus = loomsync.ItemStatusAccepted
		deletions[idx].DeletionRequestID = result.Request.DeletionRequestID
		deletions[idx].SyncedAt = &now
		deletions[idx].LastErrorCode = ""
		deletions[idx].LastErrorMessage = ""
		matched = &deletions[idx]
	}
	for idx := range outbox {
		if outbox[idx].LocalOutboxID != outboxItem.LocalOutboxID {
			continue
		}
		outbox[idx].Status = loomsync.ItemStatusAccepted
		outbox[idx].GlobalRef = result.Request.DeletionRequestID
		outbox[idx].SyncedAt = &now
		outbox[idx].LastErrorCode = ""
		outbox[idx].LastErrorMessage = ""
	}
	if err := s.SaveSyncDeletions(deletions); err != nil {
		return err
	}
	if err := s.SaveSyncOutbox(outbox); err != nil {
		return err
	}
	if matched != nil {
		if err := s.updateWatchedRootStateFromDeletion(*matched, result, now); err != nil {
			return err
		}
	}
	return nil
}

func (s Store) LocalSyncStatus(config Config, state State) (LocalSyncStatus, error) {
	s, unlock, err := s.lockLocalSync(context.Background())
	if err != nil {
		return LocalSyncStatus{}, err
	}
	defer unlock()
	if err := s.EnsureSyncDataDirs(); err != nil {
		return LocalSyncStatus{}, err
	}
	events, err := s.LoadSyncEvents()
	if err != nil {
		return LocalSyncStatus{}, err
	}
	outbox, err := s.LoadSyncOutbox()
	if err != nil {
		return LocalSyncStatus{}, err
	}
	objects, err := s.LoadSyncObjects()
	if err != nil {
		return LocalSyncStatus{}, err
	}
	deletions, err := s.LoadSyncDeletions()
	if err != nil {
		return LocalSyncStatus{}, err
	}
	cursors, err := s.LoadSyncCursors()
	if err != nil {
		return LocalSyncStatus{}, err
	}
	conflicts, err := s.LoadSyncConflicts()
	if err != nil {
		return LocalSyncStatus{}, err
	}
	counts := LocalSyncCounts{
		Events:    len(events),
		Objects:   len(objects),
		Deletions: len(deletions),
		Conflicts: len(conflicts),
	}
	for _, item := range outbox {
		switch item.Status {
		case loomsync.ItemStatusAccepted:
			counts.Accepted++
		case loomsync.ItemStatusDuplicate:
			counts.Duplicates++
		case loomsync.ItemStatusConflicted:
			counts.Conflicted++
		case loomsync.ItemStatusFailed:
			counts.Failed++
		default:
			counts.Pending++
		}
	}
	return LocalSyncStatus{
		NodeID:    state.NodeID,
		NodeKey:   config.NodeKey,
		Paths:     s.syncPaths(),
		Counts:    counts,
		Cursors:   cursors,
		Conflicts: conflicts,
	}, nil
}

func (s Store) LoadSyncEvents() ([]LocalSyncEvent, error) {
	if err := s.EnsureSyncDataDirs(); err != nil {
		return nil, err
	}
	var events []LocalSyncEvent
	if err := readJSONFile(s.syncEventsPath(), &events); err != nil {
		return nil, err
	}
	return events, nil
}

func (s Store) SaveSyncEvents(events []LocalSyncEvent) error {
	return writeJSONFile(s.syncEventsPath(), events, 0o600)
}

func (s Store) LoadSyncObjects() ([]LocalSyncObject, error) {
	if err := s.EnsureSyncDataDirs(); err != nil {
		return nil, err
	}
	var objects []LocalSyncObject
	if err := readJSONFile(s.syncObjectsPath(), &objects); err != nil {
		return nil, err
	}
	return objects, nil
}

func (s Store) SaveSyncObjects(objects []LocalSyncObject) error {
	return writeJSONFile(s.syncObjectsPath(), objects, 0o600)
}

func (s Store) LoadSyncDeletions() ([]LocalSyncDeletionRequest, error) {
	if err := s.EnsureSyncDataDirs(); err != nil {
		return nil, err
	}
	var deletions []LocalSyncDeletionRequest
	if err := readJSONFile(s.syncDeletionsPath(), &deletions); err != nil {
		return nil, err
	}
	return deletions, nil
}

func (s Store) SaveSyncDeletions(deletions []LocalSyncDeletionRequest) error {
	return writeJSONFile(s.syncDeletionsPath(), deletions, 0o600)
}

func (s Store) LoadSyncOutbox() ([]LocalSyncOutboxItem, error) {
	if err := s.EnsureSyncDataDirs(); err != nil {
		return nil, err
	}
	var outbox []LocalSyncOutboxItem
	if err := readJSONFile(s.syncOutboxPath(), &outbox); err != nil {
		return nil, err
	}
	return outbox, nil
}

func (s Store) SaveSyncOutbox(outbox []LocalSyncOutboxItem) error {
	return writeJSONFile(s.syncOutboxPath(), outbox, 0o600)
}

func (s Store) LoadSyncCursors() ([]LocalSyncCursor, error) {
	if err := s.EnsureSyncDataDirs(); err != nil {
		return nil, err
	}
	var cursors []LocalSyncCursor
	if err := readJSONFile(s.syncCursorsPath(), &cursors); err != nil {
		return nil, err
	}
	sort.Slice(cursors, func(i, j int) bool {
		return cursors[i].StreamName < cursors[j].StreamName
	})
	return cursors, nil
}

func (s Store) SaveSyncCursors(cursors []LocalSyncCursor) error {
	return writeJSONFile(s.syncCursorsPath(), cursors, 0o600)
}

func (s Store) LoadSyncConflicts() ([]LocalSyncConflict, error) {
	if err := s.EnsureSyncDataDirs(); err != nil {
		return nil, err
	}
	var conflicts []LocalSyncConflict
	if err := readJSONFile(s.syncConflictsPath(), &conflicts); err != nil {
		return nil, err
	}
	return conflicts, nil
}

func (s Store) SaveSyncConflicts(conflicts []LocalSyncConflict) error {
	return writeJSONFile(s.syncConflictsPath(), conflicts, 0o600)
}

func (s Store) nextLocalSequence(streamName string) (int64, error) {
	var maxSequence int64
	events, err := s.LoadSyncEvents()
	if err != nil {
		return 0, err
	}
	for _, event := range events {
		if event.StreamName == streamName && event.LocalSequence > maxSequence {
			maxSequence = event.LocalSequence
		}
	}
	objects, err := s.LoadSyncObjects()
	if err != nil {
		return 0, err
	}
	for _, object := range objects {
		if streamName == loomsync.StreamObjectBlobs && object.LocalSequence > maxSequence {
			maxSequence = object.LocalSequence
		}
	}
	deletions, err := s.LoadSyncDeletions()
	if err != nil {
		return 0, err
	}
	for _, deletion := range deletions {
		if streamName == loomsync.StreamDeletionRequests && deletion.LocalSequence > maxSequence {
			maxSequence = deletion.LocalSequence
		}
	}
	outbox, err := s.LoadSyncOutbox()
	if err != nil {
		return 0, err
	}
	for _, item := range outbox {
		if item.StreamName == streamName && item.LocalSequence > maxSequence {
			maxSequence = item.LocalSequence
		}
	}
	return maxSequence + 1, nil
}

func (s Store) updateLocalQueuedCursor(streamName string, sequence int64, now time.Time) error {
	cursors, err := s.LoadSyncCursors()
	if err != nil {
		return err
	}
	for idx := range cursors {
		if cursors[idx].StreamName != streamName {
			continue
		}
		if sequence > cursors[idx].LastQueuedSequence {
			cursors[idx].LastQueuedSequence = sequence
		}
		cursors[idx].UpdatedAt = now
		return s.SaveSyncCursors(cursors)
	}
	cursors = append(cursors, LocalSyncCursor{
		StreamName:         streamName,
		LastQueuedSequence: sequence,
		UpdatedAt:          now,
	})
	return s.SaveSyncCursors(cursors)
}

func (s Store) applyRemoteCursor(cursor loomsync.SyncCursor, now time.Time) error {
	cursors, err := s.LoadSyncCursors()
	if err != nil {
		return err
	}
	for idx := range cursors {
		if cursors[idx].StreamName != cursor.StreamName {
			continue
		}
		if cursor.LastAcceptedSequence > cursors[idx].LastPushedSequence {
			cursors[idx].LastPushedSequence = cursor.LastAcceptedSequence
		}
		if cursor.LastAcceptedSequence > cursors[idx].LastAcceptedSequence {
			cursors[idx].LastAcceptedSequence = cursor.LastAcceptedSequence
		}
		cursors[idx].LastSuccessAt = &now
		cursors[idx].UpdatedAt = now
		return s.SaveSyncCursors(cursors)
	}
	cursors = append(cursors, LocalSyncCursor{
		StreamName:           cursor.StreamName,
		LastPushedSequence:   cursor.LastAcceptedSequence,
		LastAcceptedSequence: cursor.LastAcceptedSequence,
		LastSuccessAt:        &now,
		UpdatedAt:            now,
	})
	return s.SaveSyncCursors(cursors)
}

func (s Store) localSyncEventsByRef() (map[string]LocalSyncEvent, error) {
	events, err := s.LoadSyncEvents()
	if err != nil {
		return nil, err
	}
	result := make(map[string]LocalSyncEvent, len(events))
	for _, event := range events {
		result[event.LocalEventID] = event
	}
	return result, nil
}

func (s Store) localSyncObjectsByRef() (map[string]LocalSyncObject, error) {
	objects, err := s.LoadSyncObjects()
	if err != nil {
		return nil, err
	}
	result := make(map[string]LocalSyncObject, len(objects))
	for _, object := range objects {
		result[object.LocalObjectID] = object
	}
	return result, nil
}

func (s Store) localSyncObjectsByObjectVersion() (map[string]LocalSyncObject, error) {
	objects, err := s.LoadSyncObjects()
	if err != nil {
		return nil, err
	}
	result := make(map[string]LocalSyncObject, len(objects))
	for _, object := range objects {
		result[localObjectVersionKey(object.LocalObjectID, object.LocalVersionID)] = object
	}
	return result, nil
}

func localSyncObjectForOutbox(outboxItem LocalSyncOutboxItem, byRef map[string]LocalSyncObject, byVersion map[string]LocalSyncObject) (LocalSyncObject, bool) {
	versionRef := metadataString(outboxItem.PayloadJSON, "local_version_ref")
	if versionRef != "" {
		if object, ok := byVersion[localObjectVersionKey(outboxItem.LocalRef, versionRef)]; ok {
			return object, true
		}
	}
	object, ok := byRef[outboxItem.LocalRef]
	return object, ok
}

func localObjectVersionKey(objectID, versionID string) string {
	return strings.TrimSpace(objectID) + "\x00" + strings.TrimSpace(versionID)
}

func (s Store) localSyncDeletionsByRef() (map[string]LocalSyncDeletionRequest, error) {
	deletions, err := s.LoadSyncDeletions()
	if err != nil {
		return nil, err
	}
	result := make(map[string]LocalSyncDeletionRequest, len(deletions))
	for _, deletion := range deletions {
		result[deletion.LocalDeletionID] = deletion
	}
	return result, nil
}

func (s Store) ResolveLocalSyncObject(ref string) (LocalSyncObject, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return LocalSyncObject{}, fmt.Errorf("local object ref is required")
	}
	objects, err := s.LoadSyncObjects()
	if err != nil {
		return LocalSyncObject{}, err
	}
	for _, object := range objects {
		if object.LocalObjectID == ref || object.MainObjectID == ref {
			return object, nil
		}
	}
	return LocalSyncObject{}, fmt.Errorf("local object not found: %s", ref)
}

func eligibleOutboxItems(items []LocalSyncOutboxItem, includeSynced bool) []LocalSyncOutboxItem {
	result := make([]LocalSyncOutboxItem, 0, len(items))
	for _, item := range items {
		switch item.Status {
		case loomsync.ItemStatusAccepted, loomsync.ItemStatusDuplicate:
			if includeSynced {
				result = append(result, item)
			}
		case loomsync.ItemStatusConflicted, loomsync.ItemStatusFailed:
			continue
		default:
			result = append(result, item)
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].StreamName != result[j].StreamName {
			return result[i].StreamName < result[j].StreamName
		}
		if result[i].LocalSequence != result[j].LocalSequence {
			return result[i].LocalSequence < result[j].LocalSequence
		}
		return result[i].LocalRef < result[j].LocalRef
	})
	return result
}

func syncBatchIdempotencyKey(nodeID string, items []loomsync.PushBatchItem) string {
	type keyItem struct {
		LocalRef string
		Hash     string
	}
	keyItems := make([]keyItem, 0, len(items))
	for _, item := range items {
		keyItems = append(keyItems, keyItem{
			LocalRef: item.LocalRef,
			Hash:     rawJSONHash(item.PayloadJSON),
		})
	}
	sort.Slice(keyItems, func(i, j int) bool {
		return keyItems[i].LocalRef < keyItems[j].LocalRef
	})
	raw, _ := json.Marshal(keyItems)
	sum := sha256.Sum256(raw)
	return "node-agent.sync." + strings.TrimSpace(nodeID) + "." + hex.EncodeToString(sum[:])[:24]
}

func buildSyncedObjectInput(state State, outboxItem LocalSyncOutboxItem, object LocalSyncObject) (loomsync.SyncedObjectInput, error) {
	contentPath := strings.TrimSpace(object.ContentPath)
	if contentPath == "" {
		contentPath = object.SourcePath
	}
	content, finalInfo, err := readStableInlineFile(contentPath, loomsync.MaxInlineObjectUploadBytes)
	if err != nil {
		return loomsync.SyncedObjectInput{}, err
	}
	hashURI := "sha256:" + rawBytesHashHex(content)
	if hashURI != object.HashURI {
		return loomsync.SyncedObjectInput{}, fmt.Errorf("local file hash changed for %s: expected %s got %s", contentPath, object.HashURI, hashURI)
	}
	sourceMtime := object.SourceMtime
	mimeType := object.MimeType
	if strings.TrimSpace(mimeType) == "" {
		mimeType = detectLocalMIME(contentPath, content)
	}
	classification := classificationForLocalObject(object, contentPath, mimeType, content, finalInfo.Size())
	input := loomsync.SyncedObjectInput{
		NodeRef:              state.NodeID,
		CredentialToken:      state.CredentialToken,
		IdempotencyKey:       syncObjectIdempotencyKey(state.NodeID, object, outboxItem),
		LocalObjectRef:       object.LocalObjectID,
		LocalVersionRef:      object.LocalVersionID,
		LocalSequence:        object.LocalSequence,
		ProjectRef:           object.ProjectRef,
		ScopeRef:             object.ScopeRef,
		LogicalName:          object.LogicalName,
		SourcePath:           object.SourcePath,
		SourceMtime:          &sourceMtime,
		SourceMtimeBasis:     object.SourceMtimeBasis,
		SourceCreatedAt:      object.SourceCreatedAt,
		SourceCreatedBasis:   object.SourceCreatedBasis,
		SizeBytes:            finalInfo.Size(),
		MimeType:             mimeType,
		HashURI:              object.HashURI,
		IndexPolicy:          effectiveLocalIndexPolicy(object.IndexPolicy, classification),
		RawBackupPolicy:      object.RawBackupPolicy,
		FileClass:            classification.FileClass,
		ClassificationSource: classification.ClassificationSource,
		IndexingState:        classification.IndexingState,
		IndexingReason:       classification.Reason,
		ContentBase64:        base64.StdEncoding.EncodeToString(content),
		Metadata:             localSyncItemMetadata(object.Metadata, outboxItem),
	}
	return input, nil
}

func syncObjectIdempotencyKey(nodeID string, object LocalSyncObject, outboxItem LocalSyncOutboxItem) string {
	raw, _ := json.Marshal(map[string]any{
		"node_id":          nodeID,
		"local_outbox_id":  outboxItem.LocalOutboxID,
		"local_object_id":  object.LocalObjectID,
		"local_version_id": object.LocalVersionID,
		"hash_uri":         object.HashURI,
	})
	sum := sha256.Sum256(raw)
	return "node-agent.sync.object." + strings.TrimSpace(nodeID) + "." + hex.EncodeToString(sum[:])[:24]
}

func buildDeletionRequestInput(state State, outboxItem LocalSyncOutboxItem, deletion LocalSyncDeletionRequest) loomsync.DeletionRequestInput {
	return loomsync.DeletionRequestInput{
		NodeRef:         state.NodeID,
		CredentialToken: state.CredentialToken,
		IdempotencyKey:  deletionRequestIdempotencyKey(state.NodeID, deletion),
		TargetKind:      deletion.TargetKind,
		TargetRef:       deletion.TargetRef,
		RequestedAction: deletion.RequestedAction,
		Reason:          deletion.Reason,
		Metadata:        localSyncItemMetadata(deletion.Metadata, outboxItem),
	}
}

func deletionRequestIdempotencyKey(nodeID string, deletion LocalSyncDeletionRequest) string {
	rootKey := strings.TrimSpace(deletion.RootKey)
	pathKey := watchedroots.PathKey(rootKey, deletion.RelativePath)
	deleteRef := strings.TrimPrefix(strings.TrimSpace(deletion.ContentHashURI), "sha256:")
	if deleteRef == "" {
		deleteRef = fmt.Sprintf("%d", deletion.LocalSequence)
	}
	return "node-agent.watched-root.delete." + strings.TrimSpace(nodeID) + "." + rootKey + "." + pathKey + "." + deleteRef
}

func localObjectPayload(object LocalSyncObject) json.RawMessage {
	return objectJSON(map[string]any{
		"local_object_ref":      object.LocalObjectID,
		"local_version_ref":     object.LocalVersionID,
		"local_sequence":        object.LocalSequence,
		"stream_name":           loomsync.StreamObjectBlobs,
		"logical_name":          object.LogicalName,
		"source_path":           object.SourcePath,
		"source_mtime":          object.SourceMtime,
		"source_mtime_basis":    object.SourceMtimeBasis,
		"source_created_at":     object.SourceCreatedAt,
		"source_created_basis":  object.SourceCreatedBasis,
		"size_bytes":            object.SizeBytes,
		"mime_type":             object.MimeType,
		"hash_uri":              object.HashURI,
		"index_policy":          object.IndexPolicy,
		"raw_backup_policy":     object.RawBackupPolicy,
		"file_class":            object.FileClass,
		"classification_source": object.ClassificationSource,
		"indexing_state":        object.IndexingState,
		"indexing_reason":       object.IndexingReason,
	})
}

func localDeletionPayload(deletion LocalSyncDeletionRequest) json.RawMessage {
	return objectJSON(map[string]any{
		"local_deletion_ref": deletion.LocalDeletionID,
		"local_sequence":     deletion.LocalSequence,
		"stream_name":        loomsync.StreamDeletionRequests,
		"target_kind":        deletion.TargetKind,
		"target_ref":         deletion.TargetRef,
		"requested_action":   deletion.RequestedAction,
		"reason":             deletion.Reason,
		"root_key":           deletion.RootKey,
		"relative_path":      deletion.RelativePath,
		"content_hash_uri":   deletion.ContentHashURI,
	})
}

func (s Store) updateWatchedRootStateFromSyncedObject(objects []LocalSyncObject, result loomsync.SyncedObjectResult, now time.Time) error {
	localOutboxID := metadataString(result.Item.Metadata, "local_outbox_id")
	localVersionID := metadataString(result.Item.Metadata, "local_version_ref")
	for _, object := range objects {
		if object.LocalObjectID != result.Item.LocalRef {
			continue
		}
		if localVersionID != "" && object.LocalVersionID != localVersionID {
			continue
		}
		rootKey := metadataString(object.Metadata, "watched_root")
		relativePath := metadataString(object.Metadata, "relative_path")
		if rootKey == "" || relativePath == "" {
			continue
		}
		watchedStore := watchedroots.NewStore(s.DataDir)
		pathState, err := watchedStore.LoadPathState(rootKey, relativePath)
		if err != nil {
			if watchedroots.IsNotExist(err) {
				continue
			}
			return err
		}
		if pathState.LocalObjectID != "" && pathState.LocalObjectID != object.LocalObjectID ||
			pathState.LocalVersionID != "" && pathState.LocalVersionID != object.LocalVersionID {
			continue
		}
		pathState.LocalObjectID = object.LocalObjectID
		pathState.LocalVersionID = object.LocalVersionID
		if localOutboxID != "" {
			pathState.LocalSyncOutboxID = localOutboxID
		}
		pathState.LastOutputAppliedAt = &now
		if result.Item.Status == loomsync.ItemStatusAccepted || result.Item.Status == loomsync.ItemStatusDuplicate {
			if pathState.MainVersionID != result.VersionID || pathState.LastSyncedModifiedAt == nil {
				mtime := object.SourceMtime.UTC()
				pathState.LastSyncedModifiedAt = &mtime
				pathState.LastSyncedMetadataSequence = 0
			}
			pathState.SyncStatus = result.Item.Status
			pathState.LastSyncedHashURI = object.HashURI
			pathState.MainObjectID = result.ObjectID
			pathState.MainVersionID = result.VersionID
			pathState.MainBlobID = result.BlobID
			pathState.IndexStatus = result.IndexStatus
			pathState.LastOutputErrorCode = ""
			pathState.LastOutputErrorMessage = ""
		} else {
			pathState.SyncStatus = result.Item.Status
			pathState.LastOutputErrorCode = result.Item.ErrorCode
			pathState.LastOutputErrorMessage = result.Item.ErrorMessage
		}
		if err := watchedStore.SavePathState(pathState); err != nil {
			return err
		}
	}
	return nil
}

func (s Store) updateWatchedRootStateFromDeletion(deletion LocalSyncDeletionRequest, result loomsync.DeletionRequestResult, now time.Time) error {
	rootKey := metadataString(deletion.Metadata, "watched_root")
	relativePath := metadataString(deletion.Metadata, "relative_path")
	if rootKey == "" {
		rootKey = deletion.RootKey
	}
	if relativePath == "" {
		relativePath = deletion.RelativePath
	}
	if rootKey == "" || relativePath == "" {
		return nil
	}
	watchedStore := watchedroots.NewStore(s.DataDir)
	pathState, err := watchedStore.LoadPathState(rootKey, relativePath)
	if err != nil {
		if watchedroots.IsNotExist(err) {
			return nil
		}
		return err
	}
	pathState.DeletionStatus = watchedroots.OutputStatusRecorded
	pathState.DeletionRequestID = result.Request.DeletionRequestID
	pathState.LastOutputAppliedAt = &now
	pathState.LastOutputErrorCode = ""
	pathState.LastOutputErrorMessage = ""
	return watchedStore.SavePathState(pathState)
}

func detectLocalMIME(path string, content []byte) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown":
		return "text/markdown"
	case ".txt":
		return "text/plain"
	}
	if len(content) == 0 {
		return "application/octet-stream"
	}
	sample := content
	if len(sample) > 512 {
		sample = sample[:512]
	}
	return http.DetectContentType(sample)
}

func readStableInlineFile(path string, maxBytes int64) ([]byte, os.FileInfo, error) {
	before, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if before.IsDir() {
		return nil, nil, fmt.Errorf("source path is a directory")
	}
	if maxBytes > 0 && before.Size() > maxBytes {
		return nil, nil, fmt.Errorf("source file exceeds %d byte inline sync limit", maxBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()

	limit := maxBytes
	if limit <= 0 {
		limit = before.Size()
	}
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, nil, err
	}
	if maxBytes > 0 && int64(len(content)) > maxBytes {
		return nil, nil, fmt.Errorf("source file exceeds %d byte inline sync limit", maxBytes)
	}
	after, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if fileChangedDuringRead(before, after) {
		return nil, nil, fmt.Errorf("source file changed during sync read: %s", path)
	}
	if int64(len(content)) != after.Size() {
		return nil, nil, fmt.Errorf("source file size changed during sync read: %s", path)
	}
	return content, after, nil
}

func fileChangedDuringRead(before, after os.FileInfo) bool {
	if before == nil || after == nil {
		return false
	}
	return before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime())
}

func classifyLocalSyncFile(path, mimeType string, content []byte, sizeBytes int64, fidelity *filesystemmeta.Observation) storagecatalog.ClassificationResult {
	input := storagecatalog.ClassificationInput{
		Path:          path,
		MimeType:      mimeType,
		ContentSample: contentSample(content),
		SizeBytes:     sizeBytes,
		MaxIndexBytes: storagecatalog.DefaultMaxIndexBytes,
	}
	if fidelity != nil {
		input.IsDirectory = fidelity.Kind == filesystemmeta.ObjectKindDirectory || fidelity.Kind == filesystemmeta.ObjectKindPackage
		input.IsPackage = fidelity.IsPackage
		input.GeneratedMetadata = fidelity.GeneratedMetadata
		input.PermissionDenied = fidelity.PermissionDenied
	}
	return storagecatalog.Classify(input)
}

func classificationForLocalObject(object LocalSyncObject, path, mimeType string, content []byte, sizeBytes int64) storagecatalog.ClassificationResult {
	if object.FileClass != "" && object.ClassificationSource != "" && object.IndexingState != "" {
		return storagecatalog.ClassificationResult{
			FileClass:            object.FileClass,
			ClassificationSource: object.ClassificationSource,
			IndexingState:        object.IndexingState,
			Reason:               object.IndexingReason,
		}
	}
	return classifyLocalSyncFile(path, mimeType, content, sizeBytes, nil)
}

func contentSample(content []byte) []byte {
	if len(content) <= 4096 {
		return content
	}
	return content[:4096]
}

func effectiveLocalIndexPolicy(policy string, classification storagecatalog.ClassificationResult) string {
	policy = strings.TrimSpace(policy)
	if policy == "" {
		policy = loomsync.IndexPolicyTextLater
	}
	switch classification.IndexingState {
	case storagecatalog.IndexingStateIndexed:
		if storagecatalog.IsTextIndexCandidate(classification.FileClass) {
			return policy
		}
		return loomsync.IndexPolicyMetadataOnly
	case storagecatalog.IndexingStateTooLarge,
		storagecatalog.IndexingStateBinary,
		storagecatalog.IndexingStateUnsupported,
		storagecatalog.IndexingStatePermissionDenied,
		storagecatalog.IndexingStateGeneratedIgnored,
		storagecatalog.IndexingStateMetadataOnly:
		return loomsync.IndexPolicyMetadataOnly
	default:
		return policy
	}
}

func rawBytesHashHex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func rawJSONHash(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		trimmed = "{}"
	}
	var value any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		sum := sha256.Sum256([]byte(trimmed))
		return hex.EncodeToString(sum[:])
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		normalized = []byte(trimmed)
	}
	sum := sha256.Sum256(normalized)
	return hex.EncodeToString(sum[:])
}

func marshalRaw(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func localSyncItemMetadata(raw json.RawMessage, item LocalSyncOutboxItem) json.RawMessage {
	object := rawJSONObject(raw)
	object["local_outbox_id"] = item.LocalOutboxID
	object["payload_hash"] = item.PayloadHash
	return marshalRaw(object)
}

func rawJSONObject(raw json.RawMessage) map[string]any {
	object := map[string]any{}
	_ = json.Unmarshal(raw, &object)
	if object == nil {
		return map[string]any{}
	}
	return object
}

func metadataString(raw json.RawMessage, key string) string {
	value, ok := rawJSONObject(raw)[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func countSyncItemResults(items []loomsync.SyncItemResult, status string) int {
	count := 0
	for _, item := range items {
		if item.Status == status {
			count++
		}
	}
	return count
}

func (s Store) syncRoot() string {
	return filepath.Join(s.DataDir, "sync")
}

func (s Store) syncEventsPath() string {
	return filepath.Join(s.syncRoot(), localSyncEventsFile)
}

func (s Store) syncObjectsPath() string {
	return filepath.Join(s.syncRoot(), localSyncObjectsFile)
}

func (s Store) syncDeletionsPath() string {
	return filepath.Join(s.syncRoot(), localSyncDeletionsFile)
}

func (s Store) syncOutboxPath() string {
	return filepath.Join(s.syncRoot(), localSyncOutboxFile)
}

func (s Store) syncCursorsPath() string {
	return filepath.Join(s.syncRoot(), localSyncCursorsFile)
}

func (s Store) syncConflictsPath() string {
	return filepath.Join(s.syncRoot(), localSyncConflictsFile)
}

func (s Store) syncPaths() LocalSyncPaths {
	return LocalSyncPaths{
		Root:      s.syncRoot(),
		Events:    s.syncEventsPath(),
		Objects:   s.syncObjectsPath(),
		Deletions: s.syncDeletionsPath(),
		Outbox:    s.syncOutboxPath(),
		Cursors:   s.syncCursorsPath(),
		Conflicts: s.syncConflictsPath(),
	}
}
