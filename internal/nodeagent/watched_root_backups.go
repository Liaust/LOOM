package nodeagent

import (
	"archive/tar"
	"bytes"
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
	"syscall"
	"time"

	"loom.local/loom/internal/filetransfer"
	"loom.local/loom/internal/ids"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodeagent/watchedroots"
	loomsync "loom.local/loom/internal/sync"
	mainwatchedroots "loom.local/loom/internal/watchedroots"
)

const (
	localBackupStatusPending    = "pending"
	localBackupStatusAccepted   = "accepted"
	localBackupStatusDuplicate  = "duplicate"
	localBackupStatusConflicted = "conflicted"
	localBackupStatusFailed     = "failed"

	localBackupItemKindBatch = "backup_batch"

	localBackupArtifactsFile = "artifacts.json"
	localBackupBatchesFile   = "batches.json"
	localBackupItemsFile     = "items.json"
	localBackupOutboxFile    = "outbox.json"
	localBackupCursorsFile   = "cursors.json"
	localBackupQueueEvents   = "queue-events.jsonl"
	localBackupCompaction    = "queue-compaction.json"

	localBackupQueueEventsCompactBytes = 2 * 1024 * 1024
)

const localBackupQueueLockFile = "queue.lock"

type LocalWatchedRootBackupStatus struct {
	NodeID            string                        `json:"node_id,omitempty"`
	NodeKey           string                        `json:"node_key,omitempty"`
	RootKey           string                        `json:"root_key,omitempty"`
	Paths             LocalWatchedRootBackupPaths   `json:"paths"`
	Counts            LocalWatchedRootBackupCounts  `json:"counts"`
	Limits            *LocalWatchedRootBackupLimits `json:"limits,omitempty"`
	Failed            []LocalWatchedRootBackupError `json:"failed,omitempty"`
	PolicyVersion     string                        `json:"policy_version,omitempty"`
	PolicyFingerprint string                        `json:"policy_fingerprint,omitempty"`
}

type LocalWatchedRootBackupPaths struct {
	Root         string `json:"root"`
	Artifacts    string `json:"artifacts"`
	Batches      string `json:"batches"`
	Items        string `json:"items"`
	Outbox       string `json:"outbox"`
	Cursors      string `json:"cursors"`
	ArtifactsDir string `json:"artifacts_dir"`
}

type LocalWatchedRootBackupCounts struct {
	Protected    int   `json:"protected"`
	Ignored      int   `json:"ignored"`
	Artifacts    int   `json:"artifacts"`
	Batches      int   `json:"batches"`
	Items        int   `json:"items"`
	Pending      int   `json:"pending"`
	Retryable    int   `json:"retryable"`
	Accepted     int   `json:"accepted"`
	Duplicates   int   `json:"duplicates"`
	Conflicted   int   `json:"conflicted"`
	Failed       int   `json:"failed"`
	ManualAction int   `json:"manual_action"`
	PendingBytes int64 `json:"pending_bytes"`
}

type LocalWatchedRootBackupLimits struct {
	Mode                   string `json:"mode"`
	MaxFileBytes           int64  `json:"max_file_bytes"`
	MaxBatchBytes          int64  `json:"max_batch_bytes"`
	MaxPendingItems        int    `json:"max_pending_items"`
	MaxPendingBytes        int64  `json:"max_pending_bytes"`
	IncludeDeletionMarkers bool   `json:"include_deletion_markers"`
	OnLimit                string `json:"on_limit"`
}

type LocalWatchedRootBackupError struct {
	LocalRef        string `json:"local_ref,omitempty"`
	LocalOutboxID   string `json:"local_outbox_id,omitempty"`
	LocalBatchID    string `json:"local_batch_id,omitempty"`
	LocalItemID     string `json:"local_item_id,omitempty"`
	LocalArtifactID string `json:"local_artifact_id,omitempty"`
	RootKey         string `json:"root_key,omitempty"`
	RelativePath    string `json:"relative_path,omitempty"`
	Status          string `json:"status,omitempty"`
	ErrorCode       string `json:"error_code,omitempty"`
	ErrorMessage    string `json:"error_message,omitempty"`
	Retryable       bool   `json:"retryable,omitempty"`
	ManualAction    bool   `json:"manual_action,omitempty"`
}

type LocalWatchedRootBackupArtifact struct {
	LocalArtifactID            string     `json:"local_artifact_id"`
	RootKey                    string     `json:"root_key"`
	RelativePath               string     `json:"relative_path"`
	ContentHashURI             string     `json:"content_hash_uri"`
	SizeBytes                  int64      `json:"size_bytes"`
	SourceMtime                time.Time  `json:"source_mtime"`
	LocalContentPath           string     `json:"local_content_path"`
	PrivateBackupOperationID   string     `json:"private_backup_operation_id,omitempty"`
	FileTransferID             string     `json:"file_transfer_id,omitempty"`
	FileTransferStorageEntryID string     `json:"file_transfer_storage_entry_id,omitempty"`
	FileTransferAcceptedPath   string     `json:"file_transfer_accepted_path,omitempty"`
	Status                     string     `json:"status"`
	CreatedAt                  time.Time  `json:"created_at"`
	SyncedAt                   *time.Time `json:"synced_at,omitempty"`
	LastErrorCode              string     `json:"last_error_code,omitempty"`
	LastErrorMessage           string     `json:"last_error_message,omitempty"`
}

type LocalWatchedRootBackupBatch struct {
	LocalBatchID      string     `json:"local_batch_id"`
	RootKey           string     `json:"root_key"`
	WorkerKey         string     `json:"worker_key,omitempty"`
	BackupMode        string     `json:"backup_mode"`
	BatchKind         string     `json:"batch_kind"`
	LocalSequence     int64      `json:"local_sequence"`
	ItemCount         int        `json:"item_count"`
	ArtifactCount     int        `json:"artifact_count"`
	DeletionCount     int        `json:"deletion_count"`
	TotalBytes        int64      `json:"total_bytes"`
	Status            string     `json:"status"`
	MainBackupBatchID string     `json:"main_backup_batch_id,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	SyncedAt          *time.Time `json:"synced_at,omitempty"`
	LastErrorCode     string     `json:"last_error_code,omitempty"`
	LastErrorMessage  string     `json:"last_error_message,omitempty"`
}

type LocalWatchedRootBackupItem struct {
	LocalItemID                string          `json:"local_item_id"`
	LocalBatchID               string          `json:"local_batch_id"`
	RootKey                    string          `json:"root_key"`
	RelativePath               string          `json:"relative_path"`
	ItemKind                   string          `json:"item_kind"`
	BackupMode                 string          `json:"backup_mode"`
	ContentHashURI             string          `json:"content_hash_uri,omitempty"`
	PreviousHashURI            string          `json:"previous_hash_uri,omitempty"`
	SizeBytes                  int64           `json:"size_bytes,omitempty"`
	ModifiedAt                 *time.Time      `json:"modified_at,omitempty"`
	DeletedAt                  *time.Time      `json:"deleted_at,omitempty"`
	LocalArtifactID            string          `json:"local_artifact_id,omitempty"`
	PrivateBackupOperationID   string          `json:"private_backup_operation_id,omitempty"`
	FileTransferID             string          `json:"file_transfer_id,omitempty"`
	FileTransferStorageEntryID string          `json:"file_transfer_storage_entry_id,omitempty"`
	FileTransferAcceptedPath   string          `json:"file_transfer_accepted_path,omitempty"`
	MainBackupItemID           string          `json:"main_backup_item_id,omitempty"`
	Metadata                   json.RawMessage `json:"metadata,omitempty"`
	Status                     string          `json:"status"`
	CreatedAt                  time.Time       `json:"created_at"`
	SyncedAt                   *time.Time      `json:"synced_at,omitempty"`
	LastErrorCode              string          `json:"last_error_code,omitempty"`
	LastErrorMessage           string          `json:"last_error_message,omitempty"`
}

type LocalWatchedRootBackupOutboxItem struct {
	LocalOutboxID    string          `json:"local_outbox_id"`
	LocalRef         string          `json:"local_ref"`
	ItemKind         string          `json:"item_kind"`
	RootKey          string          `json:"root_key"`
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

type LocalWatchedRootBackupCursor struct {
	RootKey              string     `json:"root_key"`
	LastQueuedSequence   int64      `json:"last_queued_sequence"`
	LastAcceptedSequence int64      `json:"last_accepted_sequence"`
	LastSuccessAt        *time.Time `json:"last_success_at,omitempty"`
	LastErrorAt          *time.Time `json:"last_error_at,omitempty"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

type LocalWatchedRootBackupPushRun struct {
	Batches        []mainwatchedroots.BackupBatchResult `json:"batches,omitempty"`
	LocalStatus    LocalWatchedRootBackupStatus         `json:"local_status"`
	SubmittedItems int                                  `json:"submitted_items"`
}

type LocalWatchedRootBackupQueueCompaction struct {
	SchemaVersion string                                `json:"schema_version"`
	CompactedAt   time.Time                             `json:"compacted_at"`
	Counts        LocalWatchedRootBackupQueueCountSet   `json:"counts"`
	Paths         LocalWatchedRootBackupQueueStatePaths `json:"paths"`
}

type LocalWatchedRootBackupQueueCountSet struct {
	Artifacts LocalWatchedRootBackupQueueStatusCounts `json:"artifacts"`
	Batches   LocalWatchedRootBackupQueueStatusCounts `json:"batches"`
	Items     LocalWatchedRootBackupQueueStatusCounts `json:"items"`
	Outbox    LocalWatchedRootBackupQueueStatusCounts `json:"outbox"`
}

type LocalWatchedRootBackupQueueStatusCounts struct {
	Total      int            `json:"total"`
	ByStatus   map[string]int `json:"by_status,omitempty"`
	Pending    int            `json:"pending,omitempty"`
	Accepted   int            `json:"accepted,omitempty"`
	Duplicate  int            `json:"duplicate,omitempty"`
	Conflicted int            `json:"conflicted,omitempty"`
	Failed     int            `json:"failed,omitempty"`
	Other      int            `json:"other,omitempty"`
}

type LocalWatchedRootBackupQueueStatePaths struct {
	Events     string `json:"events"`
	Compaction string `json:"compaction"`
}

type watchedRootBackupQueueError struct {
	Code    string
	Message string
}

func (e watchedRootBackupQueueError) Error() string {
	return e.Message
}

func watchedRootBackupErrorCode(err error) string {
	var queueErr watchedRootBackupQueueError
	if errors.As(err, &queueErr) && queueErr.Code != "" {
		return queueErr.Code
	}
	return watchedroots.FindingBackupStageFailed
}

func (s Store) EnsureWatchedRootBackupDataDirs() error {
	if err := s.ensureWatchedRootBackupRoot(); err != nil {
		return err
	}
	for _, path := range []string{
		s.watchedRootBackupArtifactsPath(),
		s.watchedRootBackupBatchesPath(),
		s.watchedRootBackupItemsPath(),
		s.watchedRootBackupOutboxPath(),
		s.watchedRootBackupCursorsPath(),
	} {
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			if err := writeJSONFile(path, []any{}, 0o600); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	return nil
}

func (s Store) LocalWatchedRootBackupStatus(config Config, state State, rootKey string) (LocalWatchedRootBackupStatus, error) {
	if err := s.EnsureWatchedRootBackupDataDirs(); err != nil {
		return LocalWatchedRootBackupStatus{}, err
	}
	rootKey = strings.TrimSpace(rootKey)
	artifacts, err := s.LoadWatchedRootBackupArtifacts()
	if err != nil {
		return LocalWatchedRootBackupStatus{}, err
	}
	batches, err := s.LoadWatchedRootBackupBatches()
	if err != nil {
		return LocalWatchedRootBackupStatus{}, err
	}
	items, err := s.LoadWatchedRootBackupItems()
	if err != nil {
		return LocalWatchedRootBackupStatus{}, err
	}
	outbox, err := s.LoadWatchedRootBackupOutbox()
	if err != nil {
		return LocalWatchedRootBackupStatus{}, err
	}
	batchesByRef := map[string]LocalWatchedRootBackupBatch{}
	counts := LocalWatchedRootBackupCounts{}
	for _, artifact := range artifacts {
		if rootKey != "" && artifact.RootKey != rootKey {
			continue
		}
		counts.Artifacts++
	}
	for _, batch := range batches {
		if rootKey != "" && batch.RootKey != rootKey {
			continue
		}
		counts.Batches++
		batchesByRef[batch.LocalBatchID] = batch
	}
	for _, item := range items {
		if rootKey != "" && item.RootKey != rootKey {
			continue
		}
		counts.Items++
	}
	for _, item := range outbox {
		if rootKey != "" && item.RootKey != rootKey {
			continue
		}
		switch item.Status {
		case localBackupStatusAccepted:
			counts.Accepted++
		case localBackupStatusDuplicate:
			counts.Duplicates++
		case localBackupStatusConflicted:
			counts.Conflicted++
		case localBackupStatusFailed:
			counts.Failed++
			counts.ManualAction++
		default:
			counts.Pending++
			if watchedRootBackupHasError(item.LastErrorCode, item.LastErrorMessage) {
				counts.Retryable++
			}
			if batch, ok := batchesByRef[item.LocalRef]; ok {
				counts.PendingBytes += batch.TotalBytes
			}
		}
	}
	failed := watchedRootBackupErrors(rootKey, artifacts, batches, items, outbox)
	limits := s.watchedRootBackupLimits(rootKey)
	policyVersion := ""
	policyFingerprint := ""
	if rootKey != "" {
		if summary, summaryErr := watchedroots.NewStore(s.DataDir).LoadLatestSummary(rootKey); summaryErr == nil {
			counts.Protected = summary.Included
			counts.Ignored = summary.Excluded
			policyVersion = summary.PolicyVersion
			policyFingerprint = summary.PolicyFingerprint
		}
	}
	return LocalWatchedRootBackupStatus{
		NodeID:            state.NodeID,
		NodeKey:           config.NodeKey,
		RootKey:           rootKey,
		Paths:             s.watchedRootBackupPaths(),
		Counts:            counts,
		Limits:            limits,
		Failed:            failed,
		PolicyVersion:     policyVersion,
		PolicyFingerprint: policyFingerprint,
	}, nil
}

func (s Store) QueueWatchedRootBackupAction(config Config, state State, action watchedroots.OutputAction, contentPath string) (LocalWatchedRootBackupBatch, LocalWatchedRootBackupOutboxItem, error) {
	var batch LocalWatchedRootBackupBatch
	var outboxItem LocalWatchedRootBackupOutboxItem
	err := s.withWatchedRootBackupQueueLock(func() error {
		var queueErr error
		batch, outboxItem, queueErr = s.queueWatchedRootBackupActionLocked(config, state, action, contentPath)
		return queueErr
	})
	if compactErr := s.compactWatchedRootBackupQueueEventsIfLarge(); err == nil {
		err = compactErr
	}
	return batch, outboxItem, err
}

func (s Store) queueWatchedRootBackupActionLocked(config Config, state State, action watchedroots.OutputAction, contentPath string) (LocalWatchedRootBackupBatch, LocalWatchedRootBackupOutboxItem, error) {
	if err := s.EnsureWatchedRootBackupDataDirs(); err != nil {
		return LocalWatchedRootBackupBatch{}, LocalWatchedRootBackupOutboxItem{}, err
	}
	if strings.TrimSpace(state.NodeID) == "" {
		return LocalWatchedRootBackupBatch{}, LocalWatchedRootBackupOutboxItem{}, errors.New("node credential is not imported: missing node_id")
	}
	if action.RootKey == "" {
		return LocalWatchedRootBackupBatch{}, LocalWatchedRootBackupOutboxItem{}, errors.New("backup action root key is required")
	}
	if action.RelativePath == "" {
		return LocalWatchedRootBackupBatch{}, LocalWatchedRootBackupOutboxItem{}, errors.New("backup action relative path is required")
	}
	if err := s.checkWatchedRootBackupLimits(config, state, action); err != nil {
		return LocalWatchedRootBackupBatch{}, LocalWatchedRootBackupOutboxItem{}, err
	}

	artifacts, err := s.LoadWatchedRootBackupArtifacts()
	if err != nil {
		return LocalWatchedRootBackupBatch{}, LocalWatchedRootBackupOutboxItem{}, err
	}
	batches, err := s.LoadWatchedRootBackupBatches()
	if err != nil {
		return LocalWatchedRootBackupBatch{}, LocalWatchedRootBackupOutboxItem{}, err
	}
	items, err := s.LoadWatchedRootBackupItems()
	if err != nil {
		return LocalWatchedRootBackupBatch{}, LocalWatchedRootBackupOutboxItem{}, err
	}
	outbox, err := s.LoadWatchedRootBackupOutbox()
	if err != nil {
		return LocalWatchedRootBackupBatch{}, LocalWatchedRootBackupOutboxItem{}, err
	}
	if batch, outboxItem, ok := existingWatchedRootBackupQueue(action, batches, items, outbox); ok {
		return batch, outboxItem, nil
	}

	now := time.Now().UTC()
	var artifact LocalWatchedRootBackupArtifact
	if action.ActionKind == watchedroots.OutputActionBackupFile {
		artifact, artifacts, err = s.stageWatchedRootBackupArtifact(config, artifacts, action, contentPath, now)
		if err != nil {
			return LocalWatchedRootBackupBatch{}, LocalWatchedRootBackupOutboxItem{}, err
		}
	}

	sequence := nextWatchedRootBackupSequence(outbox)
	batch := LocalWatchedRootBackupBatch{
		LocalBatchID:  ids.NewLocalBackupBatchID(),
		RootKey:       action.RootKey,
		WorkerKey:     "",
		BackupMode:    action.BackupMode,
		BatchKind:     mainwatchedroots.BackupBatchKindWatchedRoot,
		LocalSequence: sequence,
		ItemCount:     1,
		TotalBytes:    watchedRootBackupActionBytes(action),
		Status:        localBackupStatusPending,
		CreatedAt:     now,
	}
	if action.ActionKind == watchedroots.OutputActionBackupFile {
		batch.ArtifactCount = 1
	}
	if action.ActionKind == watchedroots.OutputActionBackupDeletionMarker {
		batch.DeletionCount = 1
		batch.TotalBytes = 0
	}
	item := LocalWatchedRootBackupItem{
		LocalItemID:     ids.NewLocalBackupItemID(),
		LocalBatchID:    batch.LocalBatchID,
		RootKey:         action.RootKey,
		RelativePath:    action.RelativePath,
		ItemKind:        action.BackupItemKind,
		BackupMode:      action.BackupMode,
		ContentHashURI:  action.ContentHashURI,
		PreviousHashURI: action.PreviousHashURI,
		SizeBytes:       action.SizeBytes,
		ModifiedAt:      action.ModifiedAt,
		DeletedAt:       action.DeletedAt,
		Metadata: objectJSON(map[string]any{
			"filesystem_observation": action.Fidelity,
		}),
		Status:    localBackupStatusPending,
		CreatedAt: now,
	}
	if artifact.LocalArtifactID != "" {
		item.LocalArtifactID = artifact.LocalArtifactID
	}
	payload := objectJSON(map[string]any{
		"source":            "loom-node-agent",
		"node_key":          config.NodeKey,
		"root_key":          action.RootKey,
		"relative_path":     action.RelativePath,
		"backup_mode":       action.BackupMode,
		"item_kind":         action.BackupItemKind,
		"content_hash_uri":  action.ContentHashURI,
		"previous_hash_uri": action.PreviousHashURI,
		"local_batch_id":    batch.LocalBatchID,
		"local_item_id":     item.LocalItemID,
		"local_artifact_id": item.LocalArtifactID,
	})
	outboxItem := LocalWatchedRootBackupOutboxItem{
		LocalOutboxID: ids.NewLocalOutboxID(),
		LocalRef:      batch.LocalBatchID,
		ItemKind:      localBackupItemKindBatch,
		RootKey:       action.RootKey,
		LocalSequence: sequence,
		PayloadHash:   "sha256:" + rawJSONHash(payload),
		Status:        localBackupStatusPending,
		CreatedAt:     now,
		PayloadJSON:   payload,
	}
	artifacts = upsertWatchedRootBackupArtifact(artifacts, artifact)
	batches = append(batches, batch)
	items = append(items, item)
	outbox = append(outbox, outboxItem)
	if err := s.SaveWatchedRootBackupArtifacts(artifacts); err != nil {
		return LocalWatchedRootBackupBatch{}, LocalWatchedRootBackupOutboxItem{}, err
	}
	if err := s.SaveWatchedRootBackupBatches(batches); err != nil {
		return LocalWatchedRootBackupBatch{}, LocalWatchedRootBackupOutboxItem{}, err
	}
	if err := s.SaveWatchedRootBackupItems(items); err != nil {
		return LocalWatchedRootBackupBatch{}, LocalWatchedRootBackupOutboxItem{}, err
	}
	if err := s.SaveWatchedRootBackupOutbox(outbox); err != nil {
		return LocalWatchedRootBackupBatch{}, LocalWatchedRootBackupOutboxItem{}, err
	}
	return batch, outboxItem, nil
}

func flushWatchedRootBackups(ctx context.Context, store Store, config Config, state State, correlationID string) watchedroots.BackupOutputFlush {
	before, err := store.LocalWatchedRootBackupStatus(config, state, "")
	if err != nil {
		return watchedroots.BackupOutputFlush{Attempted: false, Status: watchedroots.OutputStatusFailed, Error: err.Error()}
	}
	flush := watchedroots.BackupOutputFlush{
		Attempted:     true,
		Status:        watchedroots.OutputStatusQueued,
		PendingBefore: before.Counts.Pending,
	}
	if strings.TrimSpace(state.NodeID) == "" || strings.TrimSpace(state.CredentialToken) == "" {
		flush.Attempted = false
		flush.Status = "skipped_missing_credentials"
		flush.PendingAfter = before.Counts.Pending
		flush.RetryableAfter = before.Counts.Retryable
		flush.AcceptedAfter = before.Counts.Accepted
		flush.ConflictedAfter = before.Counts.Conflicted
		flush.FailedAfter = before.Counts.Failed
		flush.ManualAfter = before.Counts.ManualAction
		return flush
	}
	if before.Counts.Pending == 0 {
		flush.Status = watchedroots.OutputStatusAlreadyCurrent
		flush.PendingAfter = before.Counts.Pending
		flush.RetryableAfter = before.Counts.Retryable
		flush.AcceptedAfter = before.Counts.Accepted
		flush.ConflictedAfter = before.Counts.Conflicted
		flush.FailedAfter = before.Counts.Failed
		flush.ManualAfter = before.Counts.ManualAction
		return flush
	}
	push, err := pushLocalWatchedRootBackupsOnce(ctx, store, config, state, correlationID)
	if err != nil {
		flush.Status = watchedroots.OutputStatusFailed
		flush.Error = err.Error()
		if after, statusErr := store.LocalWatchedRootBackupStatus(config, state, ""); statusErr == nil {
			flush.PendingAfter = after.Counts.Pending
			flush.RetryableAfter = after.Counts.Retryable
			flush.AcceptedAfter = after.Counts.Accepted
			flush.ConflictedAfter = after.Counts.Conflicted
			flush.FailedAfter = after.Counts.Failed
			flush.ManualAfter = after.Counts.ManualAction
		}
		return flush
	}
	flush.SubmittedItems = push.SubmittedItems
	flush.PendingAfter = push.LocalStatus.Counts.Pending
	flush.RetryableAfter = push.LocalStatus.Counts.Retryable
	flush.AcceptedAfter = push.LocalStatus.Counts.Accepted
	flush.ConflictedAfter = push.LocalStatus.Counts.Conflicted
	flush.FailedAfter = push.LocalStatus.Counts.Failed
	flush.ManualAfter = push.LocalStatus.Counts.ManualAction
	if push.LocalStatus.Counts.Pending > 0 || push.LocalStatus.Counts.Failed > 0 || push.LocalStatus.Counts.Conflicted > 0 {
		flush.Status = watchedroots.OutputStatusFailed
	} else {
		flush.Status = watchedroots.OutputStatusRecorded
	}
	return flush
}

func pushLocalWatchedRootBackupsOnce(ctx context.Context, store Store, config Config, state State, correlationID string) (LocalWatchedRootBackupPushRun, error) {
	var result LocalWatchedRootBackupPushRun
	err := store.withWatchedRootBackupQueueLock(func() error {
		var pushErr error
		result, pushErr = pushLocalWatchedRootBackupsOnceLocked(ctx, store, config, state, correlationID)
		return pushErr
	})
	if compactErr := store.compactWatchedRootBackupQueueEventsIfLarge(); err == nil {
		err = compactErr
	}
	return result, err
}

func pushLocalWatchedRootBackupsOnceLocked(ctx context.Context, store Store, config Config, state State, correlationID string) (LocalWatchedRootBackupPushRun, error) {
	if strings.TrimSpace(state.NodeID) == "" {
		return LocalWatchedRootBackupPushRun{}, errors.New("node credential is not imported: missing node_id")
	}
	if strings.TrimSpace(state.CredentialToken) == "" {
		return LocalWatchedRootBackupPushRun{}, errors.New("node credential is not imported: missing credential_token")
	}
	artifacts, err := store.LoadWatchedRootBackupArtifacts()
	if err != nil {
		return LocalWatchedRootBackupPushRun{}, err
	}
	batches, err := store.LoadWatchedRootBackupBatches()
	if err != nil {
		return LocalWatchedRootBackupPushRun{}, err
	}
	items, err := store.LoadWatchedRootBackupItems()
	if err != nil {
		return LocalWatchedRootBackupPushRun{}, err
	}
	outbox, err := store.LoadWatchedRootBackupOutbox()
	if err != nil {
		return LocalWatchedRootBackupPushRun{}, err
	}
	pending := pendingWatchedRootBackupOutboxItems(outbox)
	if len(pending) == 0 {
		status, statusErr := store.LocalWatchedRootBackupStatus(config, state, "")
		if statusErr != nil {
			return LocalWatchedRootBackupPushRun{}, statusErr
		}
		return LocalWatchedRootBackupPushRun{LocalStatus: status}, nil
	}
	now := time.Now().UTC()
	pendingOutboxIDs := make(map[string]struct{}, len(pending))
	for _, pendingItem := range pending {
		pendingOutboxIDs[pendingItem.LocalOutboxID] = struct{}{}
	}
	for idx := range outbox {
		if _, ok := pendingOutboxIDs[outbox[idx].LocalOutboxID]; ok {
			outbox[idx].LastAttemptAt = &now
		}
	}
	if err := store.SaveWatchedRootBackupOutbox(outbox); err != nil {
		return LocalWatchedRootBackupPushRun{}, err
	}
	client, err := NewClient(config.MainURL)
	if err != nil {
		return LocalWatchedRootBackupPushRun{}, err
	}

	results := make([]mainwatchedroots.BackupBatchResult, 0, len(pending))
	submittedItems := 0
	for _, outboxItem := range pending {
		batchIdx := watchedRootBackupBatchIndex(batches, outboxItem.LocalRef)
		if batchIdx < 0 {
			err := fmt.Errorf("local backup batch not found for outbox item %s", outboxItem.LocalRef)
			_ = store.markWatchedRootBackupOutboxError(artifacts, batches, items, outbox, outboxItem.LocalOutboxID, outboxItem.LocalRef, "", "", watchedroots.FindingBackupStageFailed, err.Error())
			saveWatchedRootBackupQueueFinding(store, outboxItem.RootKey, "", watchedroots.FindingBackupStageFailed, err.Error())
			continue
		}
		batch := batches[batchIdx]
		itemIndexes := watchedRootBackupItemIndexes(items, batch.LocalBatchID)
		if len(itemIndexes) == 0 {
			err := fmt.Errorf("local backup batch %s has no items", batch.LocalBatchID)
			_ = store.markWatchedRootBackupOutboxError(artifacts, batches, items, outbox, outboxItem.LocalOutboxID, batch.LocalBatchID, "", "", watchedroots.FindingBackupStageFailed, err.Error())
			saveWatchedRootBackupQueueFinding(store, batch.RootKey, "", watchedroots.FindingBackupStageFailed, err.Error())
			continue
		}
		skipBatchUpload := false
		for _, itemIdx := range itemIndexes {
			if items[itemIdx].LocalArtifactID == "" {
				continue
			}
			artifactIdx := watchedRootBackupArtifactIndex(artifacts, items[itemIdx].LocalArtifactID)
			if artifactIdx < 0 {
				err := fmt.Errorf("local backup artifact %s was not found", items[itemIdx].LocalArtifactID)
				_ = store.markWatchedRootBackupOutboxError(artifacts, batches, items, outbox, outboxItem.LocalOutboxID, batch.LocalBatchID, items[itemIdx].LocalItemID, items[itemIdx].LocalArtifactID, watchedroots.FindingBackupStageFailed, err.Error())
				saveWatchedRootBackupQueueFinding(store, items[itemIdx].RootKey, items[itemIdx].RelativePath, watchedroots.FindingBackupStageFailed, err.Error())
				skipBatchUpload = true
				continue
			}
			if artifacts[artifactIdx].PrivateBackupOperationID == "" && artifacts[artifactIdx].FileTransferID == "" {
				if watchedRootBackupUsesFileTransfer(config, artifacts[artifactIdx]) {
					artifact, err := uploadWatchedRootBackupArtifactViaTransfer(ctx, store, client, config, state, correlationID, batch, items[itemIdx], artifacts[artifactIdx])
					if err != nil {
						artifacts, artifactIdx = refreshWatchedRootBackupArtifactsAfterProgress(store, artifacts, artifacts[artifactIdx].LocalArtifactID, artifactIdx)
						code := watchedRootBackupRemoteErrorCode(err, watchedroots.FindingBackupArtifactUploadFailed)
						_ = store.markWatchedRootBackupOutboxError(artifacts, batches, items, outbox, outboxItem.LocalOutboxID, batch.LocalBatchID, items[itemIdx].LocalItemID, artifacts[artifactIdx].LocalArtifactID, code, err.Error())
						saveWatchedRootBackupQueueFinding(store, items[itemIdx].RootKey, items[itemIdx].RelativePath, code, err.Error())
						if watchedRootBackupPermanentLocalError(code) {
							skipBatchUpload = true
							continue
						}
						return LocalWatchedRootBackupPushRun{}, err
					}
					artifacts[artifactIdx] = artifact
				} else {
					artifact, err := uploadWatchedRootBackupArtifactViaPrivateBackup(ctx, client, state, correlationID, batch, items[itemIdx], artifacts[artifactIdx])
					if err != nil {
						if watchedRootBackupShouldFallbackToTransfer(err) {
							artifact, err = uploadWatchedRootBackupArtifactViaTransfer(ctx, store, client, config, state, correlationID, batch, items[itemIdx], artifacts[artifactIdx])
							if err == nil {
								artifacts[artifactIdx] = artifact
								items[itemIdx].PrivateBackupOperationID = artifacts[artifactIdx].PrivateBackupOperationID
								items[itemIdx].FileTransferID = artifacts[artifactIdx].FileTransferID
								items[itemIdx].FileTransferStorageEntryID = artifacts[artifactIdx].FileTransferStorageEntryID
								items[itemIdx].FileTransferAcceptedPath = artifacts[artifactIdx].FileTransferAcceptedPath
								continue
							}
							artifacts, artifactIdx = refreshWatchedRootBackupArtifactsAfterProgress(store, artifacts, artifacts[artifactIdx].LocalArtifactID, artifactIdx)
						}
						code := watchedRootBackupErrorCode(err)
						var queueErr watchedRootBackupQueueError
						if !errors.As(err, &queueErr) {
							code = watchedRootBackupRemoteErrorCode(err, watchedroots.FindingBackupArtifactUploadFailed)
						}
						_ = store.markWatchedRootBackupOutboxError(artifacts, batches, items, outbox, outboxItem.LocalOutboxID, batch.LocalBatchID, items[itemIdx].LocalItemID, artifacts[artifactIdx].LocalArtifactID, code, err.Error())
						saveWatchedRootBackupQueueFinding(store, items[itemIdx].RootKey, items[itemIdx].RelativePath, code, err.Error())
						if watchedRootBackupPermanentLocalError(code) {
							skipBatchUpload = true
							continue
						}
						return LocalWatchedRootBackupPushRun{}, err
					}
					artifacts[artifactIdx] = artifact
				}
			} else if artifacts[artifactIdx].FileTransferID != "" && artifacts[artifactIdx].FileTransferStorageEntryID == "" {
				artifact, err := uploadWatchedRootBackupArtifactViaTransfer(ctx, store, client, config, state, correlationID, batch, items[itemIdx], artifacts[artifactIdx])
				if err != nil {
					artifacts, artifactIdx = refreshWatchedRootBackupArtifactsAfterProgress(store, artifacts, artifacts[artifactIdx].LocalArtifactID, artifactIdx)
					code := watchedRootBackupRemoteErrorCode(err, watchedroots.FindingBackupArtifactUploadFailed)
					_ = store.markWatchedRootBackupOutboxError(artifacts, batches, items, outbox, outboxItem.LocalOutboxID, batch.LocalBatchID, items[itemIdx].LocalItemID, artifacts[artifactIdx].LocalArtifactID, code, err.Error())
					saveWatchedRootBackupQueueFinding(store, items[itemIdx].RootKey, items[itemIdx].RelativePath, code, err.Error())
					return LocalWatchedRootBackupPushRun{}, err
				}
				artifacts[artifactIdx] = artifact
			}
			items[itemIdx].PrivateBackupOperationID = artifacts[artifactIdx].PrivateBackupOperationID
			items[itemIdx].FileTransferID = artifacts[artifactIdx].FileTransferID
			items[itemIdx].FileTransferStorageEntryID = artifacts[artifactIdx].FileTransferStorageEntryID
			items[itemIdx].FileTransferAcceptedPath = artifacts[artifactIdx].FileTransferAcceptedPath
		}
		if skipBatchUpload {
			artifacts, _ = store.LoadWatchedRootBackupArtifacts()
			batches, _ = store.LoadWatchedRootBackupBatches()
			items, _ = store.LoadWatchedRootBackupItems()
			outbox, _ = store.LoadWatchedRootBackupOutbox()
			continue
		}
		if err := store.SaveWatchedRootBackupArtifacts(artifacts); err != nil {
			return LocalWatchedRootBackupPushRun{}, err
		}
		if err := store.SaveWatchedRootBackupItems(items); err != nil {
			return LocalWatchedRootBackupPushRun{}, err
		}
		batchItems := make([]LocalWatchedRootBackupItem, 0, len(itemIndexes))
		for _, itemIdx := range itemIndexes {
			batchItems = append(batchItems, items[itemIdx])
		}
		input := buildWatchedRootBackupBatchInput(state, batch, batchItems)
		envelope, err := client.PushWatchedRootBackupBatch(ctx, correlationID, input.IdempotencyKey, input)
		if err != nil {
			code := watchedRootBackupRemoteErrorCode(err, watchedroots.FindingBackupBatchUploadFailed)
			resetWatchedRootPrivateBackupBindings(artifacts, items, itemIndexes)
			_ = store.markWatchedRootBackupOutboxError(artifacts, batches, items, outbox, outboxItem.LocalOutboxID, batch.LocalBatchID, "", "", code, err.Error())
			saveWatchedRootBackupQueueFinding(store, batch.RootKey, "", code, err.Error())
			return LocalWatchedRootBackupPushRun{}, err
		}
		if err := store.applyWatchedRootBackupBatchResultLocked(outboxItem, envelope.Data); err != nil {
			return LocalWatchedRootBackupPushRun{}, err
		}
		results = append(results, envelope.Data)
		submittedItems += len(envelope.Data.Items)
		artifacts, _ = store.LoadWatchedRootBackupArtifacts()
		batches, _ = store.LoadWatchedRootBackupBatches()
		items, _ = store.LoadWatchedRootBackupItems()
		outbox, _ = store.LoadWatchedRootBackupOutbox()
	}
	status, err := store.LocalWatchedRootBackupStatus(config, state, "")
	if err != nil {
		return LocalWatchedRootBackupPushRun{}, err
	}
	return LocalWatchedRootBackupPushRun{
		Batches:        results,
		LocalStatus:    status,
		SubmittedItems: submittedItems,
	}, nil
}

func resetWatchedRootPrivateBackupBindings(artifacts []LocalWatchedRootBackupArtifact, items []LocalWatchedRootBackupItem, itemIndexes []int) {
	for _, itemIdx := range itemIndexes {
		if itemIdx < 0 || itemIdx >= len(items) || items[itemIdx].LocalArtifactID == "" || items[itemIdx].FileTransferID != "" {
			continue
		}
		artifactIdx := watchedRootBackupArtifactIndex(artifacts, items[itemIdx].LocalArtifactID)
		if artifactIdx < 0 || artifacts[artifactIdx].FileTransferID != "" {
			continue
		}
		items[itemIdx].PrivateBackupOperationID = ""
		artifacts[artifactIdx].PrivateBackupOperationID = ""
		artifacts[artifactIdx].Status = localBackupStatusPending
		artifacts[artifactIdx].SyncedAt = nil
	}
}

func (s Store) checkWatchedRootBackupLimits(config Config, state State, action watchedroots.OutputAction) error {
	actionBytes := watchedRootBackupActionBytes(action)
	if action.BackupMaxBatchBytes > 0 && actionBytes > action.BackupMaxBatchBytes {
		return watchedRootBackupQueueError{Code: watchedroots.FindingBackupQueueLimit, Message: "backup action exceeds backup max_batch_bytes"}
	}
	status, err := s.LocalWatchedRootBackupStatus(config, state, action.RootKey)
	if err != nil {
		return err
	}
	if action.BackupMaxPendingItems > 0 && status.Counts.Pending >= action.BackupMaxPendingItems {
		return watchedRootBackupQueueError{Code: watchedroots.FindingBackupQueueLimit, Message: "backup queue has reached max_pending_items"}
	}
	if action.BackupMaxPendingBytes > 0 && status.Counts.PendingBytes+actionBytes > action.BackupMaxPendingBytes {
		return watchedRootBackupQueueError{Code: watchedroots.FindingBackupQueueLimit, Message: "backup queue has reached max_pending_bytes"}
	}
	return nil
}

func watchedRootBackupActionBytes(action watchedroots.OutputAction) int64 {
	if action.ActionKind == watchedroots.OutputActionBackupFile {
		return action.SizeBytes
	}
	return 0
}

func (s Store) stageWatchedRootBackupArtifact(config Config, artifacts []LocalWatchedRootBackupArtifact, action watchedroots.OutputAction, contentPath string, now time.Time) (LocalWatchedRootBackupArtifact, []LocalWatchedRootBackupArtifact, error) {
	transport := normalizeBackupTransportConfig(config.BackupTransport)
	contentPath, err := filepath.Abs(strings.TrimSpace(contentPath))
	if err != nil {
		return LocalWatchedRootBackupArtifact{}, artifacts, err
	}
	info, err := stableFileInfo(contentPath, transport.StabilityWindow)
	if err != nil {
		if os.IsPermission(err) {
			return LocalWatchedRootBackupArtifact{}, artifacts, watchedRootBackupQueueError{Code: watchedroots.FindingPermissionDenied, Message: err.Error()}
		}
		return LocalWatchedRootBackupArtifact{}, artifacts, err
	}
	if !info.Mode().IsRegular() {
		return LocalWatchedRootBackupArtifact{}, artifacts, watchedRootBackupQueueError{Code: watchedroots.FindingBackupUnsupportedByPolicy, Message: "backup source is not a regular file"}
	}
	if action.BackupMaxFileBytes > 0 && info.Size() > action.BackupMaxFileBytes {
		return LocalWatchedRootBackupArtifact{}, artifacts, watchedRootBackupQueueError{Code: watchedroots.FindingBackupFileTooLarge, Message: "backup file exceeds max_file_bytes"}
	}
	hash, err := copyStableBackupArtifact(contentPath, s.watchedRootBackupArtifactHashDir(), info)
	if err != nil {
		if os.IsPermission(err) {
			return LocalWatchedRootBackupArtifact{}, artifacts, watchedRootBackupQueueError{Code: watchedroots.FindingPermissionDenied, Message: err.Error()}
		}
		return LocalWatchedRootBackupArtifact{}, artifacts, err
	}
	hashURI := "sha256:" + hash
	if action.ContentHashURI != "" && hashURI != action.ContentHashURI {
		return LocalWatchedRootBackupArtifact{}, artifacts, watchedRootBackupQueueError{Code: watchedroots.FindingBackupStageFailed, Message: fmt.Sprintf("backup file hash changed for %s: expected %s got %s", action.RelativePath, action.ContentHashURI, hashURI)}
	}
	localPath := filepath.Join(s.watchedRootBackupArtifactHashDir(), hash)
	sourceMtime := now
	if action.ModifiedAt != nil && !action.ModifiedAt.IsZero() {
		sourceMtime = action.ModifiedAt.UTC()
	} else if !info.ModTime().IsZero() {
		sourceMtime = info.ModTime().UTC()
	}
	artifact := LocalWatchedRootBackupArtifact{
		LocalArtifactID:  ids.NewLocalBackupArtifactID(),
		RootKey:          action.RootKey,
		RelativePath:     action.RelativePath,
		ContentHashURI:   hashURI,
		SizeBytes:        info.Size(),
		SourceMtime:      sourceMtime,
		LocalContentPath: localPath,
		Status:           localBackupStatusPending,
		CreatedAt:        now,
	}
	return artifact, append(artifacts, artifact), nil
}

func stableFileInfo(path string, stabilityWindow string) (os.FileInfo, error) {
	before, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	delay, err := time.ParseDuration(strings.TrimSpace(stabilityWindow))
	if err != nil {
		return nil, fmt.Errorf("parse backup transport stability window: %w", err)
	}
	if delay <= 0 {
		return before, nil
	}
	time.Sleep(delay)
	after, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, watchedRootBackupQueueError{Code: watchedroots.FindingBackupStageFailed, Message: "backup source changed during stability window"}
	}
	return after, nil
}

func copyStableBackupArtifact(sourcePath string, artifactDir string, expected os.FileInfo) (string, error) {
	if err := os.MkdirAll(artifactDir, 0o700); err != nil {
		return "", err
	}
	input, err := os.Open(sourcePath)
	if err != nil {
		return "", err
	}
	defer input.Close()
	tmp, err := os.CreateTemp(artifactDir, ".staging-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hasher), input); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	after, err := os.Stat(sourcePath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	if expected.Size() != after.Size() || !expected.ModTime().Equal(after.ModTime()) {
		_ = os.Remove(tmpPath)
		return "", watchedRootBackupQueueError{Code: watchedroots.FindingBackupStageFailed, Message: "backup source changed while staging artifact"}
	}
	hash := hex.EncodeToString(hasher.Sum(nil))
	finalPath := filepath.Join(artifactDir, hash)
	if _, err := os.Stat(finalPath); errors.Is(err, fs.ErrNotExist) {
		if err := os.Chmod(tmpPath, 0o600); err != nil {
			_ = os.Remove(tmpPath)
			return "", err
		}
		if err := os.Rename(tmpPath, finalPath); err != nil {
			_ = os.Remove(tmpPath)
			return "", err
		}
	} else if err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	} else {
		_ = os.Remove(tmpPath)
	}
	return hash, nil
}

func buildWatchedRootPrivateBackupInput(state State, batch LocalWatchedRootBackupBatch, item LocalWatchedRootBackupItem, artifact LocalWatchedRootBackupArtifact) (loomsync.PrivateBackupInput, error) {
	payload, err := watchedRootBackupArtifactTarPayload(batch, item, artifact)
	if err != nil {
		return loomsync.PrivateBackupInput{}, err
	}
	if int64(len(payload)) > loomsync.MaxPrivateBackupBytes {
		return loomsync.PrivateBackupInput{}, watchedRootBackupQueueError{Code: watchedroots.FindingBackupTransportLimitExceeded, Message: fmt.Sprintf("watched-root backup artifact payload exceeds private backup transport limit: %d > %d", len(payload), loomsync.MaxPrivateBackupBytes)}
	}
	return loomsync.PrivateBackupInput{
		NodeRef:         state.NodeID,
		CredentialToken: state.CredentialToken,
		IdempotencyKey:  watchedRootBackupArtifactIdempotencyKey(state.NodeID, batch, item, artifact),
		PayloadBase64:   base64.StdEncoding.EncodeToString(payload),
		CoarseSizeBytes: int64(len(payload)),
		Metadata: objectJSON(map[string]any{
			"source":              "loom-node-agent",
			"backup_kind":         "watched_root_file",
			"root_key":            artifact.RootKey,
			"relative_path":       artifact.RelativePath,
			"content_hash_uri":    artifact.ContentHashURI,
			"backup_mode":         batch.BackupMode,
			"local_batch_id":      batch.LocalBatchID,
			"local_artifact_id":   artifact.LocalArtifactID,
			"local_item_id":       item.LocalItemID,
			"client_generated_at": time.Now().UTC().Format(time.RFC3339Nano),
		}),
	}, nil
}

func uploadWatchedRootBackupArtifactViaPrivateBackup(ctx context.Context, client Client, state State, correlationID string, batch LocalWatchedRootBackupBatch, item LocalWatchedRootBackupItem, artifact LocalWatchedRootBackupArtifact) (LocalWatchedRootBackupArtifact, error) {
	input, err := buildWatchedRootPrivateBackupInput(state, batch, item, artifact)
	if err != nil {
		return LocalWatchedRootBackupArtifact{}, err
	}
	envelope, err := client.PushPrivateBackup(ctx, correlationID, input.IdempotencyKey, input)
	if err != nil {
		return LocalWatchedRootBackupArtifact{}, err
	}
	acceptedAt := time.Now().UTC()
	artifact.PrivateBackupOperationID = envelope.Data.Operation.PrivateBackupOperationID
	artifact.Status = localBackupStatusAccepted
	artifact.SyncedAt = &acceptedAt
	artifact.LastErrorCode = ""
	artifact.LastErrorMessage = ""
	return artifact, nil
}

func watchedRootBackupUsesFileTransfer(config Config, artifact LocalWatchedRootBackupArtifact) bool {
	transport := normalizeBackupTransportConfig(config.BackupTransport)
	if transport.ForceChunked {
		return true
	}
	if transport.DirectMaxBytes <= 0 {
		return false
	}
	return artifact.SizeBytes > transport.DirectMaxBytes
}

func uploadWatchedRootBackupArtifactViaTransfer(ctx context.Context, store Store, client Client, config Config, state State, correlationID string, batch LocalWatchedRootBackupBatch, item LocalWatchedRootBackupItem, artifact LocalWatchedRootBackupArtifact) (LocalWatchedRootBackupArtifact, error) {
	transport := normalizeBackupTransportConfig(config.BackupTransport)
	if artifact.FileTransferID == "" {
		manifest := watchedRootBackupFileTransferManifest(config, state, batch, item, artifact, transport)
		envelope, err := client.CreateFileTransfer(ctx, correlationID, manifest.IdempotencyKey, manifest)
		if err != nil {
			return LocalWatchedRootBackupArtifact{}, err
		}
		artifact.FileTransferID = envelope.Data.Manifest.TransferID
		if err := store.saveWatchedRootBackupArtifactProgress(artifact); err != nil {
			return LocalWatchedRootBackupArtifact{}, err
		}
	}

	statusEnvelope, err := client.GetFileTransfer(ctx, correlationID, artifact.FileTransferID)
	if err != nil {
		return LocalWatchedRootBackupArtifact{}, err
	}
	status := statusEnvelope.Data
	if status.Manifest.Status != filetransfer.StatusAccepted {
		if err := uploadMissingWatchedRootTransferChunks(ctx, client, correlationID, artifact, status); err != nil {
			return LocalWatchedRootBackupArtifact{}, err
		}
		completeEnvelope, err := client.CompleteFileTransfer(ctx, correlationID, watchedRootBackupTransferCompleteIdempotencyKey(state.NodeID, artifact), artifact.FileTransferID)
		if err != nil {
			return LocalWatchedRootBackupArtifact{}, err
		}
		status.Manifest = completeEnvelope.Data.Manifest
		status.AcceptedPath = completeEnvelope.Data.AcceptedPath
		artifact.FileTransferStorageEntryID = completeEnvelope.Data.StorageEntryID
		artifact.FileTransferAcceptedPath = completeEnvelope.Data.AcceptedPath
	} else {
		artifact.FileTransferStorageEntryID = status.Manifest.StorageEntryID
		artifact.FileTransferAcceptedPath = firstNonEmpty(status.AcceptedPath, status.Manifest.AcceptedPath)
	}
	if status.Manifest.Status != filetransfer.StatusAccepted {
		return LocalWatchedRootBackupArtifact{}, fmt.Errorf("file transfer %s completed with status %s", artifact.FileTransferID, status.Manifest.Status)
	}
	acceptedAt := time.Now().UTC()
	artifact.Status = localBackupStatusAccepted
	artifact.SyncedAt = &acceptedAt
	artifact.LastErrorCode = ""
	artifact.LastErrorMessage = ""
	if err := store.saveWatchedRootBackupArtifactProgress(artifact); err != nil {
		return LocalWatchedRootBackupArtifact{}, err
	}
	return artifact, nil
}

func watchedRootBackupFileTransferManifest(config Config, state State, batch LocalWatchedRootBackupBatch, item LocalWatchedRootBackupItem, artifact LocalWatchedRootBackupArtifact, transport BackupTransportConfig) filetransfer.Manifest {
	checksum := strings.TrimPrefix(strings.TrimSpace(artifact.ContentHashURI), filetransfer.ChecksumSHA256+":")
	manifest := filetransfer.Manifest{
		SourceNodeID:           state.NodeID,
		SourceNodeKey:          config.NodeKey,
		SourceRootKey:          artifact.RootKey,
		SourceRelativePath:     artifact.RelativePath,
		DestinationLogicalPath: artifact.RelativePath,
		TransferKind:           filetransfer.KindWatchedRootBackup,
		CustodyMode:            filetransfer.CustodyModeBackupCopy,
		FileSizeBytes:          artifact.SizeBytes,
		ModTime:                &artifact.SourceMtime,
		ChecksumAlgorithm:      filetransfer.ChecksumSHA256,
		ChecksumHex:            checksum,
		ChunkSizeBytes:         transport.ChunkSizeBytes,
		Metadata: objectJSON(map[string]any{
			"source":              "loom-node-agent",
			"backup_kind":         "watched_root_file",
			"root_key":            artifact.RootKey,
			"relative_path":       artifact.RelativePath,
			"content_hash_uri":    artifact.ContentHashURI,
			"backup_mode":         batch.BackupMode,
			"local_batch_id":      batch.LocalBatchID,
			"local_item_id":       item.LocalItemID,
			"local_artifact_id":   artifact.LocalArtifactID,
			"client_generated_at": time.Now().UTC().Format(time.RFC3339Nano),
		}),
	}
	manifest.IdempotencyKey = filetransfer.IdempotencyKey(manifest)
	return manifest
}

func uploadMissingWatchedRootTransferChunks(ctx context.Context, client Client, correlationID string, artifact LocalWatchedRootBackupArtifact, status filetransfer.Status) error {
	chunksByIndex := map[int64]filetransfer.Chunk{}
	for _, chunk := range status.Chunks {
		chunksByIndex[chunk.Index] = chunk
	}
	missing := append([]int64(nil), status.MissingChunks...)
	sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
	for _, index := range missing {
		chunk, ok := chunksByIndex[index]
		if !ok {
			return fmt.Errorf("file transfer %s is missing planned chunk %d", artifact.FileTransferID, index)
		}
		payload, err := readWatchedRootTransferChunk(artifact.LocalContentPath, chunk)
		if err != nil {
			return err
		}
		checksum := filetransfer.SHA256Hex(payload)
		_, err = client.UploadFileTransferChunk(
			ctx,
			correlationID,
			watchedRootBackupTransferChunkIdempotencyKey(artifact, index, checksum),
			artifact.FileTransferID,
			index,
			payload,
			filetransfer.ChecksumSHA256,
			checksum,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

func readWatchedRootTransferChunk(path string, chunk filetransfer.Chunk) ([]byte, error) {
	if chunk.SizeBytes < 0 {
		return nil, fmt.Errorf("file transfer chunk %d has negative size", chunk.Index)
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsPermission(err) {
			return nil, watchedRootBackupQueueError{Code: watchedroots.FindingPermissionDenied, Message: err.Error()}
		}
		return nil, err
	}
	defer file.Close()
	payload := make([]byte, int(chunk.SizeBytes))
	read, err := file.ReadAt(payload, chunk.OffsetBytes)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if int64(read) != chunk.SizeBytes {
		return nil, fmt.Errorf("file transfer chunk %d read %d bytes, want %d", chunk.Index, read, chunk.SizeBytes)
	}
	return payload, nil
}

func watchedRootBackupArtifactTarPayload(batch LocalWatchedRootBackupBatch, item LocalWatchedRootBackupItem, artifact LocalWatchedRootBackupArtifact) ([]byte, error) {
	content, err := os.ReadFile(artifact.LocalContentPath)
	if err != nil {
		if os.IsPermission(err) {
			return nil, watchedRootBackupQueueError{Code: watchedroots.FindingPermissionDenied, Message: err.Error()}
		}
		return nil, err
	}
	hashURI := "sha256:" + rawBytesHashHex(content)
	if artifact.ContentHashURI != "" && hashURI != artifact.ContentHashURI {
		return nil, fmt.Errorf("staged backup artifact hash changed for %s: expected %s got %s", artifact.RelativePath, artifact.ContentHashURI, hashURI)
	}
	manifest := objectJSON(map[string]any{
		"schema_version":    "watched_root.backup_artifact.v0.2",
		"root_key":          artifact.RootKey,
		"relative_path":     artifact.RelativePath,
		"content_hash_uri":  artifact.ContentHashURI,
		"size_bytes":        artifact.SizeBytes,
		"source_mtime":      artifact.SourceMtime.Format(time.RFC3339Nano),
		"backup_mode":       batch.BackupMode,
		"local_batch_id":    batch.LocalBatchID,
		"local_item_id":     item.LocalItemID,
		"local_artifact_id": artifact.LocalArtifactID,
	})
	var buf bytes.Buffer
	writer := tar.NewWriter(&buf)
	if err := writeWatchedRootBackupTarEntry(writer, "manifest.json", manifest, time.Now().UTC()); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := writeWatchedRootBackupTarEntry(writer, "content", content, artifact.SourceMtime); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeWatchedRootBackupTarEntry(writer *tar.Writer, name string, content []byte, modTime time.Time) error {
	if modTime.IsZero() {
		modTime = time.Now().UTC()
	}
	if err := writer.WriteHeader(&tar.Header{
		Name:    name,
		Mode:    0o600,
		Size:    int64(len(content)),
		ModTime: modTime.UTC(),
	}); err != nil {
		return err
	}
	_, err := writer.Write(content)
	return err
}

func buildWatchedRootBackupBatchInput(state State, batch LocalWatchedRootBackupBatch, items []LocalWatchedRootBackupItem) mainwatchedroots.BackupBatchInput {
	input := mainwatchedroots.BackupBatchInput{
		NodeRef:         state.NodeID,
		CredentialToken: state.CredentialToken,
		IdempotencyKey:  watchedRootBackupBatchIdempotencyKey(state.NodeID, batch),
		RootKey:         batch.RootKey,
		WorkerKey:       batch.WorkerKey,
		// Batch kind identifies the watched-root custody protocol, not the
		// individual item shape. Canonicalize queued legacy records at the
		// transport boundary so an exact retry cannot create more malformed
		// main custody after an agent upgrade.
		BatchKind:  mainwatchedroots.BackupBatchKindWatchedRoot,
		BackupMode: batch.BackupMode,
		Items:      make([]mainwatchedroots.BackupBatchItemInput, 0, len(items)),
		Metadata: objectJSON(map[string]any{
			"source":         "loom-node-agent",
			"local_batch_id": batch.LocalBatchID,
			"local_sequence": batch.LocalSequence,
		}),
	}
	for _, item := range items {
		artifactKind := ""
		artifactRef := ""
		if item.PrivateBackupOperationID != "" {
			artifactKind = mainwatchedroots.BackupArtifactKindPrivateBackupOperation
			artifactRef = item.PrivateBackupOperationID
		} else if item.FileTransferID != "" {
			artifactKind = mainwatchedroots.BackupArtifactKindFileTransfer
			artifactRef = item.FileTransferID
		}
		input.Items = append(input.Items, mainwatchedroots.BackupBatchItemInput{
			LocalItemRef:             item.LocalItemID,
			ItemKind:                 item.ItemKind,
			Status:                   mainwatchedroots.BackupItemStatusAccepted,
			BackupMode:               item.BackupMode,
			RelativePath:             item.RelativePath,
			ContentHashURI:           item.ContentHashURI,
			PreviousHashURI:          item.PreviousHashURI,
			SizeBytes:                item.SizeBytes,
			ModifiedAt:               item.ModifiedAt,
			DeletedAt:                item.DeletedAt,
			ArtifactKind:             artifactKind,
			ArtifactRef:              artifactRef,
			PrivateBackupOperationID: item.PrivateBackupOperationID,
			Metadata:                 watchedRootBackupItemMetadata(batch, item),
		})
	}
	return input
}

func watchedRootBackupItemMetadata(batch LocalWatchedRootBackupBatch, item LocalWatchedRootBackupItem) json.RawMessage {
	metadata := map[string]any{
		"source":                         "loom-node-agent",
		"local_batch_id":                 batch.LocalBatchID,
		"local_item_id":                  item.LocalItemID,
		"local_artifact_id":              item.LocalArtifactID,
		"file_transfer_id":               item.FileTransferID,
		"file_transfer_storage_entry_id": item.FileTransferStorageEntryID,
		"file_transfer_accepted_path":    item.FileTransferAcceptedPath,
		"client_recorded_at":             time.Now().UTC().Format(time.RFC3339Nano),
	}
	var stored map[string]any
	if len(item.Metadata) > 0 && json.Unmarshal(item.Metadata, &stored) == nil {
		for key, value := range stored {
			metadata[key] = value
		}
	}
	return objectJSON(metadata)
}

func existingWatchedRootBackupQueue(action watchedroots.OutputAction, batches []LocalWatchedRootBackupBatch, items []LocalWatchedRootBackupItem, outbox []LocalWatchedRootBackupOutboxItem) (LocalWatchedRootBackupBatch, LocalWatchedRootBackupOutboxItem, bool) {
	batchesByID := map[string]LocalWatchedRootBackupBatch{}
	outboxByRef := map[string]LocalWatchedRootBackupOutboxItem{}
	for _, batch := range batches {
		batchesByID[batch.LocalBatchID] = batch
	}
	for _, item := range outbox {
		outboxByRef[item.LocalRef] = item
	}
	for _, item := range items {
		if item.RootKey != action.RootKey ||
			item.RelativePath != action.RelativePath ||
			item.ItemKind != action.BackupItemKind ||
			item.ContentHashURI != action.ContentHashURI ||
			item.PreviousHashURI != action.PreviousHashURI ||
			item.Status == localBackupStatusFailed {
			continue
		}
		batch, batchOK := batchesByID[item.LocalBatchID]
		outboxItem, outboxOK := outboxByRef[item.LocalBatchID]
		if batchOK && outboxOK {
			return batch, outboxItem, true
		}
	}
	return LocalWatchedRootBackupBatch{}, LocalWatchedRootBackupOutboxItem{}, false
}

func (s Store) LoadWatchedRootBackupArtifacts() ([]LocalWatchedRootBackupArtifact, error) {
	if err := s.EnsureWatchedRootBackupDataDirs(); err != nil {
		return nil, err
	}
	var artifacts []LocalWatchedRootBackupArtifact
	if err := readJSONFile(s.watchedRootBackupArtifactsPath(), &artifacts); err != nil {
		return nil, err
	}
	return artifacts, nil
}

func (s Store) SaveWatchedRootBackupArtifacts(artifacts []LocalWatchedRootBackupArtifact) error {
	if err := s.ensureWatchedRootBackupRoot(); err != nil {
		return err
	}
	if err := writeJSONFile(s.watchedRootBackupArtifactsPath(), artifacts, 0o600); err != nil {
		return err
	}
	return s.appendWatchedRootBackupQueueEvent("artifacts", countWatchedRootBackupArtifacts(artifacts))
}

func (s Store) LoadWatchedRootBackupBatches() ([]LocalWatchedRootBackupBatch, error) {
	if err := s.EnsureWatchedRootBackupDataDirs(); err != nil {
		return nil, err
	}
	var batches []LocalWatchedRootBackupBatch
	if err := readJSONFile(s.watchedRootBackupBatchesPath(), &batches); err != nil {
		return nil, err
	}
	return batches, nil
}

func (s Store) SaveWatchedRootBackupBatches(batches []LocalWatchedRootBackupBatch) error {
	if err := s.ensureWatchedRootBackupRoot(); err != nil {
		return err
	}
	if err := writeJSONFile(s.watchedRootBackupBatchesPath(), batches, 0o600); err != nil {
		return err
	}
	return s.appendWatchedRootBackupQueueEvent("batches", countWatchedRootBackupBatches(batches))
}

func (s Store) LoadWatchedRootBackupItems() ([]LocalWatchedRootBackupItem, error) {
	if err := s.EnsureWatchedRootBackupDataDirs(); err != nil {
		return nil, err
	}
	var items []LocalWatchedRootBackupItem
	if err := readJSONFile(s.watchedRootBackupItemsPath(), &items); err != nil {
		return nil, err
	}
	return items, nil
}

func (s Store) SaveWatchedRootBackupItems(items []LocalWatchedRootBackupItem) error {
	if err := s.ensureWatchedRootBackupRoot(); err != nil {
		return err
	}
	if err := writeJSONFile(s.watchedRootBackupItemsPath(), items, 0o600); err != nil {
		return err
	}
	return s.appendWatchedRootBackupQueueEvent("items", countWatchedRootBackupItems(items))
}

func (s Store) LoadWatchedRootBackupOutbox() ([]LocalWatchedRootBackupOutboxItem, error) {
	if err := s.EnsureWatchedRootBackupDataDirs(); err != nil {
		return nil, err
	}
	var outbox []LocalWatchedRootBackupOutboxItem
	if err := readJSONFile(s.watchedRootBackupOutboxPath(), &outbox); err != nil {
		return nil, err
	}
	return outbox, nil
}

func (s Store) SaveWatchedRootBackupOutbox(outbox []LocalWatchedRootBackupOutboxItem) error {
	if err := s.ensureWatchedRootBackupRoot(); err != nil {
		return err
	}
	if err := writeJSONFile(s.watchedRootBackupOutboxPath(), outbox, 0o600); err != nil {
		return err
	}
	return s.appendWatchedRootBackupQueueEvent("outbox", countWatchedRootBackupOutbox(outbox))
}

func (s Store) CompactWatchedRootBackupQueues() (LocalWatchedRootBackupQueueCompaction, error) {
	var result LocalWatchedRootBackupQueueCompaction
	err := s.withWatchedRootBackupQueueLock(func() error {
		var compactErr error
		result, compactErr = s.compactWatchedRootBackupQueuesLocked()
		return compactErr
	})
	return result, err
}

func (s Store) compactWatchedRootBackupQueuesLocked() (LocalWatchedRootBackupQueueCompaction, error) {
	if err := s.EnsureWatchedRootBackupDataDirs(); err != nil {
		return LocalWatchedRootBackupQueueCompaction{}, err
	}
	artifacts, err := s.LoadWatchedRootBackupArtifacts()
	if err != nil {
		return LocalWatchedRootBackupQueueCompaction{}, err
	}
	batches, err := s.LoadWatchedRootBackupBatches()
	if err != nil {
		return LocalWatchedRootBackupQueueCompaction{}, err
	}
	items, err := s.LoadWatchedRootBackupItems()
	if err != nil {
		return LocalWatchedRootBackupQueueCompaction{}, err
	}
	outbox, err := s.LoadWatchedRootBackupOutbox()
	if err != nil {
		return LocalWatchedRootBackupQueueCompaction{}, err
	}
	result := LocalWatchedRootBackupQueueCompaction{
		SchemaVersion: "watched_root_backup_queue_compaction.v0.6.7",
		CompactedAt:   time.Now().UTC(),
		Counts: LocalWatchedRootBackupQueueCountSet{
			Artifacts: countWatchedRootBackupArtifacts(artifacts),
			Batches:   countWatchedRootBackupBatches(batches),
			Items:     countWatchedRootBackupItems(items),
			Outbox:    countWatchedRootBackupOutbox(outbox),
		},
		Paths: LocalWatchedRootBackupQueueStatePaths{
			Events:     s.watchedRootBackupQueueEventsPath(),
			Compaction: s.watchedRootBackupCompactionPath(),
		},
	}
	for _, write := range []func() error{
		func() error { return writeJSONFile(s.watchedRootBackupArtifactsPath(), artifacts, 0o600) },
		func() error { return writeJSONFile(s.watchedRootBackupBatchesPath(), batches, 0o600) },
		func() error { return writeJSONFile(s.watchedRootBackupItemsPath(), items, 0o600) },
		func() error { return writeJSONFile(s.watchedRootBackupOutboxPath(), outbox, 0o600) },
		func() error { return writeJSONFile(s.watchedRootBackupCompactionPath(), result, 0o600) },
	} {
		if err := write(); err != nil {
			return LocalWatchedRootBackupQueueCompaction{}, err
		}
	}
	if err := s.replaceWatchedRootBackupQueueEvents(result); err != nil {
		return LocalWatchedRootBackupQueueCompaction{}, err
	}
	return result, nil
}

func (s Store) ApplyWatchedRootBackupBatchResult(outboxItem LocalWatchedRootBackupOutboxItem, result mainwatchedroots.BackupBatchResult) error {
	err := s.withWatchedRootBackupQueueLock(func() error {
		return s.applyWatchedRootBackupBatchResultLocked(outboxItem, result)
	})
	if compactErr := s.compactWatchedRootBackupQueueEventsIfLarge(); err == nil {
		err = compactErr
	}
	return err
}

func (s Store) applyWatchedRootBackupBatchResultLocked(outboxItem LocalWatchedRootBackupOutboxItem, result mainwatchedroots.BackupBatchResult) error {
	if err := s.EnsureWatchedRootBackupDataDirs(); err != nil {
		return err
	}
	artifacts, err := s.LoadWatchedRootBackupArtifacts()
	if err != nil {
		return err
	}
	batches, err := s.LoadWatchedRootBackupBatches()
	if err != nil {
		return err
	}
	items, err := s.LoadWatchedRootBackupItems()
	if err != nil {
		return err
	}
	outbox, err := s.LoadWatchedRootBackupOutbox()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	resultItemsByLocalRef := make(map[string]mainwatchedroots.BackupItem, len(result.Items))
	for _, item := range result.Items {
		resultItemsByLocalRef[item.LocalItemRef] = item
	}
	for idx := range batches {
		if batches[idx].LocalBatchID != outboxItem.LocalRef {
			continue
		}
		batches[idx].MainBackupBatchID = result.Batch.WatchedRootBackupBatchID
		batches[idx].Status = localBackupStatusAccepted
		if result.Batch.Status == mainwatchedroots.BackupBatchStatusFailed {
			batches[idx].Status = localBackupStatusFailed
		}
		batches[idx].SyncedAt = &now
		batches[idx].LastErrorCode = ""
		batches[idx].LastErrorMessage = ""
	}
	for idx := range items {
		resultItem, ok := resultItemsByLocalRef[items[idx].LocalItemID]
		if !ok || items[idx].LocalBatchID != outboxItem.LocalRef {
			continue
		}
		items[idx].Status = resultItem.Status
		items[idx].MainBackupItemID = resultItem.WatchedRootBackupItemID
		if resultItem.PrivateBackupOperationID != nil {
			items[idx].PrivateBackupOperationID = *resultItem.PrivateBackupOperationID
		}
		if resultItem.ArtifactKind == mainwatchedroots.BackupArtifactKindFileTransfer {
			items[idx].FileTransferID = resultItem.ArtifactRef
		}
		if resultItem.Status == mainwatchedroots.BackupItemStatusAccepted || resultItem.Status == mainwatchedroots.BackupItemStatusDuplicate {
			items[idx].SyncedAt = &now
			items[idx].LastErrorCode = ""
			items[idx].LastErrorMessage = ""
		} else {
			items[idx].LastErrorCode = resultItem.ErrorCode
			items[idx].LastErrorMessage = resultItem.ErrorMessage
		}
		if items[idx].LocalArtifactID != "" && items[idx].PrivateBackupOperationID != "" {
			for artifactIdx := range artifacts {
				if artifacts[artifactIdx].LocalArtifactID != items[idx].LocalArtifactID {
					continue
				}
				artifacts[artifactIdx].PrivateBackupOperationID = items[idx].PrivateBackupOperationID
				if items[idx].SyncedAt != nil {
					artifacts[artifactIdx].Status = localBackupStatusAccepted
					artifacts[artifactIdx].SyncedAt = items[idx].SyncedAt
					artifacts[artifactIdx].LastErrorCode = ""
					artifacts[artifactIdx].LastErrorMessage = ""
				}
			}
		}
		if items[idx].LocalArtifactID != "" && items[idx].FileTransferID != "" {
			for artifactIdx := range artifacts {
				if artifacts[artifactIdx].LocalArtifactID != items[idx].LocalArtifactID {
					continue
				}
				artifacts[artifactIdx].FileTransferID = items[idx].FileTransferID
				if items[idx].SyncedAt != nil {
					artifacts[artifactIdx].Status = localBackupStatusAccepted
					artifacts[artifactIdx].SyncedAt = items[idx].SyncedAt
					artifacts[artifactIdx].LastErrorCode = ""
					artifacts[artifactIdx].LastErrorMessage = ""
				}
			}
		}
	}
	for idx := range outbox {
		if outbox[idx].LocalOutboxID != outboxItem.LocalOutboxID {
			continue
		}
		outbox[idx].Status = localBackupStatusAccepted
		if result.Batch.Status == mainwatchedroots.BackupBatchStatusFailed {
			outbox[idx].Status = localBackupStatusFailed
		}
		outbox[idx].GlobalRef = result.Batch.WatchedRootBackupBatchID
		outbox[idx].SyncedAt = &now
		outbox[idx].LastErrorCode = ""
		outbox[idx].LastErrorMessage = ""
	}
	if err := s.SaveWatchedRootBackupArtifacts(artifacts); err != nil {
		return err
	}
	if err := s.SaveWatchedRootBackupBatches(batches); err != nil {
		return err
	}
	if err := s.SaveWatchedRootBackupItems(items); err != nil {
		return err
	}
	if err := s.SaveWatchedRootBackupOutbox(outbox); err != nil {
		return err
	}
	return s.updateWatchedRootStateFromBackupResult(outboxItem.LocalRef, items, result, now)
}

func (s Store) withWatchedRootBackupQueueLock(fn func() error) error {
	if err := s.ensureWatchedRootBackupRoot(); err != nil {
		return err
	}
	lockPath := s.watchedRootBackupQueueLockPath()
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	}()
	return fn()
}

func (s Store) markWatchedRootBackupOutboxError(artifacts []LocalWatchedRootBackupArtifact, batches []LocalWatchedRootBackupBatch, items []LocalWatchedRootBackupItem, outbox []LocalWatchedRootBackupOutboxItem, outboxID, batchID, itemID, artifactID, code, message string) error {
	now := time.Now().UTC()
	permanent := watchedRootBackupPermanentLocalError(code)
	for idx := range artifacts {
		if artifactID == "" || artifacts[idx].LocalArtifactID != artifactID {
			continue
		}
		artifacts[idx].LastErrorCode = code
		artifacts[idx].LastErrorMessage = message
		if permanent {
			artifacts[idx].Status = localBackupStatusFailed
		}
	}
	for idx := range batches {
		if batchID == "" || batches[idx].LocalBatchID != batchID {
			continue
		}
		batches[idx].LastErrorCode = code
		batches[idx].LastErrorMessage = message
		if permanent {
			batches[idx].Status = localBackupStatusFailed
		}
	}
	for idx := range items {
		if itemID == "" {
			if batchID == "" || items[idx].LocalBatchID != batchID {
				continue
			}
		} else if items[idx].LocalItemID != itemID {
			continue
		}
		items[idx].LastErrorCode = code
		items[idx].LastErrorMessage = message
		if permanent {
			items[idx].Status = localBackupStatusFailed
		}
	}
	for idx := range outbox {
		if outbox[idx].LocalOutboxID != outboxID {
			continue
		}
		outbox[idx].LastAttemptAt = &now
		outbox[idx].LastErrorCode = code
		outbox[idx].LastErrorMessage = message
		if permanent {
			outbox[idx].Status = localBackupStatusFailed
		}
	}
	s.markWatchedRootBackupPathStateError(items, batchID, itemID, code, message, permanent, now)
	if err := s.SaveWatchedRootBackupArtifacts(artifacts); err != nil {
		return err
	}
	if err := s.SaveWatchedRootBackupBatches(batches); err != nil {
		return err
	}
	if err := s.SaveWatchedRootBackupItems(items); err != nil {
		return err
	}
	return s.SaveWatchedRootBackupOutbox(outbox)
}

func (s Store) markWatchedRootBackupPathStateError(items []LocalWatchedRootBackupItem, batchID, itemID, code, message string, permanent bool, now time.Time) {
	watchedStore := watchedroots.NewStore(s.DataDir)
	for _, item := range items {
		if itemID == "" {
			if batchID == "" || item.LocalBatchID != batchID {
				continue
			}
		} else if item.LocalItemID != itemID {
			continue
		}
		if strings.TrimSpace(item.RootKey) == "" || strings.TrimSpace(item.RelativePath) == "" {
			continue
		}
		pathState, err := watchedStore.LoadPathState(item.RootKey, item.RelativePath)
		if err != nil {
			continue
		}
		if permanent {
			pathState.BackupStatus = watchedroots.OutputStatusFailed
		} else if pathState.BackupStatus == "" {
			pathState.BackupStatus = watchedroots.OutputStatusQueued
		}
		pathState.LastBackupErrorCode = code
		pathState.LastBackupErrorMessage = message
		pathState.LastOutputAppliedAt = &now
		_ = watchedStore.SavePathState(pathState)
	}
}

func watchedRootBackupPermanentLocalError(code string) bool {
	switch code {
	case watchedroots.FindingBackupFileTooLarge,
		watchedroots.FindingBackupTransportLimitExceeded,
		watchedroots.FindingBackupUnsupportedByPolicy,
		watchedroots.ReasonSkippedPathEscape,
		watchedroots.ReasonSkippedPermissionDenied,
		watchedroots.ReasonSkippedSpecialFile,
		watchedroots.FindingPathEscapeSkipped,
		watchedroots.FindingPermissionDenied:
		return true
	default:
		return false
	}
}

func watchedRootBackupRemoteErrorCode(err error, fallback string) string {
	var remoteErr RemoteRequestError
	if errors.As(err, &remoteErr) && remoteErr.StatusCode == http.StatusRequestEntityTooLarge {
		return watchedroots.FindingBackupTransportLimitExceeded
	}
	if strings.TrimSpace(fallback) != "" {
		return fallback
	}
	return watchedroots.FindingBackupStageFailed
}

func watchedRootBackupShouldFallbackToTransfer(err error) bool {
	var queueErr watchedRootBackupQueueError
	if errors.As(err, &queueErr) && queueErr.Code == watchedroots.FindingBackupTransportLimitExceeded {
		return true
	}
	var remoteErr RemoteRequestError
	return errors.As(err, &remoteErr) && remoteErr.StatusCode == http.StatusRequestEntityTooLarge
}

func watchedRootBackupHasError(code string, message string) bool {
	return strings.TrimSpace(code) != "" || strings.TrimSpace(message) != ""
}

func (s Store) updateWatchedRootStateFromBackupResult(localBatchID string, items []LocalWatchedRootBackupItem, result mainwatchedroots.BackupBatchResult, now time.Time) error {
	resultItemsByLocalRef := make(map[string]mainwatchedroots.BackupItem, len(result.Items))
	for _, item := range result.Items {
		resultItemsByLocalRef[item.LocalItemRef] = item
	}
	watchedStore := watchedroots.NewStore(s.DataDir)
	for _, item := range items {
		if item.LocalBatchID != localBatchID {
			continue
		}
		resultItem, ok := resultItemsByLocalRef[item.LocalItemID]
		if !ok {
			continue
		}
		if resultItem.Status != mainwatchedroots.BackupItemStatusAccepted && resultItem.Status != mainwatchedroots.BackupItemStatusDuplicate {
			continue
		}
		pathState, err := watchedStore.LoadPathState(item.RootKey, item.RelativePath)
		if err != nil {
			continue
		}
		pathState.BackupStatus = watchedroots.OutputStatusRecorded
		pathState.BackupMode = item.BackupMode
		pathState.MainBackupBatchID = result.Batch.WatchedRootBackupBatchID
		pathState.MainBackupItemID = resultItem.WatchedRootBackupItemID
		pathState.LastBackedUpAt = &now
		pathState.LastBackupErrorCode = ""
		pathState.LastBackupErrorMessage = ""
		if item.PrivateBackupOperationID != "" {
			pathState.PrivateBackupOperationID = item.PrivateBackupOperationID
		}
		if item.FileTransferID != "" {
			pathState.FileTransferID = item.FileTransferID
			pathState.FileTransferStorageEntryID = item.FileTransferStorageEntryID
		}
		if item.ItemKind == mainwatchedroots.BackupItemKindDeletionMarker {
			pathState.BackupDeletionMarkerID = resultItem.WatchedRootBackupItemID
		} else if item.ContentHashURI != "" {
			pathState.LastBackedUpHashURI = item.ContentHashURI
		}
		if err := watchedStore.SavePathState(pathState); err != nil {
			return err
		}
	}
	return nil
}

func (s Store) backupStatusForWatchedRoot(config Config, state State, rootKey string) (*watchedroots.BackupOutputStatus, error) {
	status, err := s.LocalWatchedRootBackupStatus(config, state, rootKey)
	if err != nil {
		return nil, err
	}
	return &watchedroots.BackupOutputStatus{
		RootKey:           status.RootKey,
		Artifacts:         status.Counts.Artifacts,
		Batches:           status.Counts.Batches,
		Items:             status.Counts.Items,
		Pending:           status.Counts.Pending,
		Retryable:         status.Counts.Retryable,
		Accepted:          status.Counts.Accepted,
		Duplicates:        status.Counts.Duplicates,
		Conflicted:        status.Counts.Conflicted,
		Failed:            status.Counts.Failed,
		ManualAction:      status.Counts.ManualAction,
		PendingBytes:      status.Counts.PendingBytes,
		Protected:         status.Counts.Protected,
		Ignored:           status.Counts.Ignored,
		PolicyVersion:     status.PolicyVersion,
		PolicyFingerprint: status.PolicyFingerprint,
	}, nil
}

func (s Store) ensureWatchedRootBackupRoot() error {
	if err := s.EnsureDataDirs(); err != nil {
		return err
	}
	for _, dir := range []string{s.watchedRootBackupRoot(), s.watchedRootBackupArtifactHashDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func (s Store) watchedRootBackupPaths() LocalWatchedRootBackupPaths {
	return LocalWatchedRootBackupPaths{
		Root:         s.watchedRootBackupRoot(),
		Artifacts:    s.watchedRootBackupArtifactsPath(),
		Batches:      s.watchedRootBackupBatchesPath(),
		Items:        s.watchedRootBackupItemsPath(),
		Outbox:       s.watchedRootBackupOutboxPath(),
		Cursors:      s.watchedRootBackupCursorsPath(),
		ArtifactsDir: s.watchedRootBackupArtifactHashDir(),
	}
}

func (s Store) watchedRootBackupRoot() string {
	return filepath.Join(s.DataDir, "watched-root-backups")
}

func (s Store) watchedRootBackupArtifactsPath() string {
	return filepath.Join(s.watchedRootBackupRoot(), localBackupArtifactsFile)
}

func (s Store) watchedRootBackupBatchesPath() string {
	return filepath.Join(s.watchedRootBackupRoot(), localBackupBatchesFile)
}

func (s Store) watchedRootBackupItemsPath() string {
	return filepath.Join(s.watchedRootBackupRoot(), localBackupItemsFile)
}

func (s Store) watchedRootBackupOutboxPath() string {
	return filepath.Join(s.watchedRootBackupRoot(), localBackupOutboxFile)
}

func (s Store) watchedRootBackupCursorsPath() string {
	return filepath.Join(s.watchedRootBackupRoot(), localBackupCursorsFile)
}

func (s Store) watchedRootBackupQueueEventsPath() string {
	return filepath.Join(s.watchedRootBackupRoot(), localBackupQueueEvents)
}

func (s Store) watchedRootBackupQueueLockPath() string {
	return filepath.Join(s.watchedRootBackupRoot(), localBackupQueueLockFile)
}

func (s Store) watchedRootBackupCompactionPath() string {
	return filepath.Join(s.watchedRootBackupRoot(), localBackupCompaction)
}

func (s Store) watchedRootBackupArtifactHashDir() string {
	return filepath.Join(s.watchedRootBackupRoot(), "artifacts", "sha256")
}

func (s Store) appendWatchedRootBackupQueueEvent(kind string, counts LocalWatchedRootBackupQueueStatusCounts) error {
	if err := s.ensureWatchedRootBackupRoot(); err != nil {
		return err
	}
	event := map[string]any{
		"schema_version": "watched_root_backup_queue_event.v0.6.7",
		"event":          "saved",
		"kind":           kind,
		"count":          counts.Total,
		"counts":         counts,
		"recorded_at":    time.Now().UTC(),
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(s.watchedRootBackupQueueEventsPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(payload, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return nil
}

func (s Store) replaceWatchedRootBackupQueueEvents(compaction LocalWatchedRootBackupQueueCompaction) error {
	event := map[string]any{
		"schema_version": compaction.SchemaVersion,
		"event":          "compacted",
		"counts":         compaction.Counts,
		"recorded_at":    compaction.CompactedAt,
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return os.WriteFile(s.watchedRootBackupQueueEventsPath(), append(payload, '\n'), 0o600)
}

func (s Store) compactWatchedRootBackupQueueEventsIfLarge() error {
	info, err := os.Stat(s.watchedRootBackupQueueEventsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size() < localBackupQueueEventsCompactBytes {
		return nil
	}
	_, err = s.CompactWatchedRootBackupQueues()
	return err
}

func countWatchedRootBackupArtifacts(artifacts []LocalWatchedRootBackupArtifact) LocalWatchedRootBackupQueueStatusCounts {
	counts := LocalWatchedRootBackupQueueStatusCounts{Total: len(artifacts), ByStatus: map[string]int{}}
	for _, item := range artifacts {
		counts.addStatus(item.Status)
	}
	return counts
}

func countWatchedRootBackupBatches(batches []LocalWatchedRootBackupBatch) LocalWatchedRootBackupQueueStatusCounts {
	counts := LocalWatchedRootBackupQueueStatusCounts{Total: len(batches), ByStatus: map[string]int{}}
	for _, item := range batches {
		counts.addStatus(item.Status)
	}
	return counts
}

func countWatchedRootBackupItems(items []LocalWatchedRootBackupItem) LocalWatchedRootBackupQueueStatusCounts {
	counts := LocalWatchedRootBackupQueueStatusCounts{Total: len(items), ByStatus: map[string]int{}}
	for _, item := range items {
		counts.addStatus(item.Status)
	}
	return counts
}

func countWatchedRootBackupOutbox(outbox []LocalWatchedRootBackupOutboxItem) LocalWatchedRootBackupQueueStatusCounts {
	counts := LocalWatchedRootBackupQueueStatusCounts{Total: len(outbox), ByStatus: map[string]int{}}
	for _, item := range outbox {
		counts.addStatus(item.Status)
	}
	return counts
}

func (c *LocalWatchedRootBackupQueueStatusCounts) addStatus(status string) {
	status = strings.TrimSpace(status)
	if status == "" {
		status = "unknown"
	}
	c.ByStatus[status]++
	switch status {
	case localBackupStatusPending:
		c.Pending++
	case localBackupStatusAccepted:
		c.Accepted++
	case localBackupStatusDuplicate:
		c.Duplicate++
	case localBackupStatusConflicted:
		c.Conflicted++
	case localBackupStatusFailed:
		c.Failed++
	default:
		c.Other++
	}
}

func nextWatchedRootBackupSequence(outbox []LocalWatchedRootBackupOutboxItem) int64 {
	var max int64
	for _, item := range outbox {
		if item.LocalSequence > max {
			max = item.LocalSequence
		}
	}
	return max + 1
}

func upsertWatchedRootBackupArtifact(artifacts []LocalWatchedRootBackupArtifact, artifact LocalWatchedRootBackupArtifact) []LocalWatchedRootBackupArtifact {
	if artifact.LocalArtifactID == "" {
		return artifacts
	}
	for idx := range artifacts {
		if artifacts[idx].LocalArtifactID == artifact.LocalArtifactID {
			artifacts[idx] = artifact
			return artifacts
		}
	}
	return append(artifacts, artifact)
}

func (s Store) saveWatchedRootBackupArtifactProgress(artifact LocalWatchedRootBackupArtifact) error {
	artifacts, err := s.LoadWatchedRootBackupArtifacts()
	if err != nil {
		return err
	}
	artifacts = upsertWatchedRootBackupArtifact(artifacts, artifact)
	return s.SaveWatchedRootBackupArtifacts(artifacts)
}

func refreshWatchedRootBackupArtifactsAfterProgress(store Store, artifacts []LocalWatchedRootBackupArtifact, localArtifactID string, fallbackIdx int) ([]LocalWatchedRootBackupArtifact, int) {
	fresh, err := store.LoadWatchedRootBackupArtifacts()
	if err != nil {
		return artifacts, fallbackIdx
	}
	idx := watchedRootBackupArtifactIndex(fresh, localArtifactID)
	if idx < 0 {
		return artifacts, fallbackIdx
	}
	return fresh, idx
}

func pendingWatchedRootBackupOutboxItems(outbox []LocalWatchedRootBackupOutboxItem) []LocalWatchedRootBackupOutboxItem {
	items := make([]LocalWatchedRootBackupOutboxItem, 0, len(outbox))
	for _, item := range outbox {
		if item.Status == "" || item.Status == localBackupStatusPending {
			items = append(items, item)
		}
	}
	return items
}

func watchedRootBackupBatchIndex(batches []LocalWatchedRootBackupBatch, localBatchID string) int {
	for idx := range batches {
		if batches[idx].LocalBatchID == localBatchID {
			return idx
		}
	}
	return -1
}

func watchedRootBackupItemIndexes(items []LocalWatchedRootBackupItem, localBatchID string) []int {
	indexes := []int{}
	for idx := range items {
		if items[idx].LocalBatchID == localBatchID {
			indexes = append(indexes, idx)
		}
	}
	return indexes
}

func watchedRootBackupArtifactIndex(artifacts []LocalWatchedRootBackupArtifact, localArtifactID string) int {
	for idx := range artifacts {
		if artifacts[idx].LocalArtifactID == localArtifactID {
			return idx
		}
	}
	return -1
}

func watchedRootBackupArtifactIdempotencyKey(nodeID string, batch LocalWatchedRootBackupBatch, item LocalWatchedRootBackupItem, artifact LocalWatchedRootBackupArtifact) string {
	identity := strings.Join([]string{
		strings.TrimSpace(nodeID),
		strings.TrimSpace(batch.LocalBatchID),
		strings.TrimSpace(item.LocalItemID),
		strings.TrimSpace(artifact.LocalArtifactID),
		strings.TrimSpace(artifact.RootKey),
		filepath.ToSlash(filepath.Clean(artifact.RelativePath)),
		strings.TrimSpace(artifact.ContentHashURI),
	}, "\x00")
	sum := sha256.Sum256([]byte(identity))
	return "node-agent.watched-root.backup.artifact.v2." + hex.EncodeToString(sum[:])
}

func watchedRootBackupBatchIdempotencyKey(nodeID string, batch LocalWatchedRootBackupBatch) string {
	return "node-agent.watched-root.backup.batch." + strings.TrimSpace(nodeID) + "." + batch.RootKey + "." + batch.LocalBatchID
}

func watchedRootBackupTransferChunkIdempotencyKey(artifact LocalWatchedRootBackupArtifact, index int64, checksum string) string {
	return "node-agent.watched-root.file-transfer.chunk." + artifact.LocalArtifactID + "." + fmt.Sprintf("%d", index) + "." + checksum
}

func watchedRootBackupTransferCompleteIdempotencyKey(nodeID string, artifact LocalWatchedRootBackupArtifact) string {
	return "node-agent.watched-root.file-transfer.complete." + strings.TrimSpace(nodeID) + "." + artifact.LocalArtifactID
}

func saveWatchedRootBackupQueueFinding(store Store, rootKey, relativePath, code, message string) {
	if strings.TrimSpace(code) == "" {
		code = watchedroots.FindingBackupStageFailed
	}
	if strings.TrimSpace(message) == "" {
		message = code
	}
	_ = watchedroots.NewStore(store.DataDir).SaveFinding(watchedroots.Finding{
		RootKey:      rootKey,
		Severity:     watchedRootBackupFindingSeverity(code),
		Status:       watchedroots.FindingStatusOpen,
		Kind:         code,
		RelativePath: relativePath,
		Summary:      message,
	})
}

func watchedRootBackupErrors(rootKey string, artifacts []LocalWatchedRootBackupArtifact, batches []LocalWatchedRootBackupBatch, items []LocalWatchedRootBackupItem, outbox []LocalWatchedRootBackupOutboxItem) []LocalWatchedRootBackupError {
	errors := []LocalWatchedRootBackupError{}
	for _, item := range outbox {
		if rootKey != "" && item.RootKey != rootKey {
			continue
		}
		if item.Status != localBackupStatusFailed && item.Status != localBackupStatusConflicted && item.LastErrorCode == "" && item.LastErrorMessage == "" {
			continue
		}
		errors = append(errors, LocalWatchedRootBackupError{
			LocalRef:      item.LocalRef,
			LocalOutboxID: item.LocalOutboxID,
			RootKey:       item.RootKey,
			Status:        item.Status,
			ErrorCode:     item.LastErrorCode,
			ErrorMessage:  item.LastErrorMessage,
			Retryable:     item.Status != localBackupStatusFailed && watchedRootBackupHasError(item.LastErrorCode, item.LastErrorMessage),
			ManualAction:  item.Status == localBackupStatusFailed,
		})
	}
	for _, batch := range batches {
		if rootKey != "" && batch.RootKey != rootKey {
			continue
		}
		if batch.Status != localBackupStatusFailed && batch.Status != localBackupStatusConflicted && batch.LastErrorCode == "" && batch.LastErrorMessage == "" {
			continue
		}
		errors = append(errors, LocalWatchedRootBackupError{
			LocalBatchID: batch.LocalBatchID,
			RootKey:      batch.RootKey,
			Status:       batch.Status,
			ErrorCode:    batch.LastErrorCode,
			ErrorMessage: batch.LastErrorMessage,
			Retryable:    batch.Status != localBackupStatusFailed && watchedRootBackupHasError(batch.LastErrorCode, batch.LastErrorMessage),
			ManualAction: batch.Status == localBackupStatusFailed,
		})
	}
	for _, item := range items {
		if rootKey != "" && item.RootKey != rootKey {
			continue
		}
		if item.Status != localBackupStatusFailed && item.Status != localBackupStatusConflicted && item.LastErrorCode == "" && item.LastErrorMessage == "" {
			continue
		}
		errors = append(errors, LocalWatchedRootBackupError{
			LocalBatchID:    item.LocalBatchID,
			LocalItemID:     item.LocalItemID,
			LocalArtifactID: item.LocalArtifactID,
			RootKey:         item.RootKey,
			RelativePath:    item.RelativePath,
			Status:          item.Status,
			ErrorCode:       item.LastErrorCode,
			ErrorMessage:    item.LastErrorMessage,
			Retryable:       item.Status != localBackupStatusFailed && watchedRootBackupHasError(item.LastErrorCode, item.LastErrorMessage),
			ManualAction:    item.Status == localBackupStatusFailed,
		})
	}
	for _, artifact := range artifacts {
		if rootKey != "" && artifact.RootKey != rootKey {
			continue
		}
		if artifact.Status != localBackupStatusFailed && artifact.Status != localBackupStatusConflicted && artifact.LastErrorCode == "" && artifact.LastErrorMessage == "" {
			continue
		}
		errors = append(errors, LocalWatchedRootBackupError{
			LocalArtifactID: artifact.LocalArtifactID,
			RootKey:         artifact.RootKey,
			RelativePath:    artifact.RelativePath,
			Status:          artifact.Status,
			ErrorCode:       artifact.LastErrorCode,
			ErrorMessage:    artifact.LastErrorMessage,
			Retryable:       artifact.Status != localBackupStatusFailed && watchedRootBackupHasError(artifact.LastErrorCode, artifact.LastErrorMessage),
			ManualAction:    artifact.Status == localBackupStatusFailed,
		})
	}
	return errors
}

func (s Store) watchedRootBackupLimits(rootKey string) *LocalWatchedRootBackupLimits {
	rootKey = strings.TrimSpace(rootKey)
	if rootKey == "" {
		return nil
	}
	instance, err := noderuntime.NewStore(s.DataDir).LoadInstance(noderuntime.WatchedRootWorkerKey(rootKey))
	if err != nil {
		return nil
	}
	rootConfig, err := watchedRootConfigFromInstance(instance)
	if err != nil {
		return nil
	}
	policy := rootConfig.BackupPolicy
	includeDeletionMarkers := false
	if policy.IncludeDeletionMarkers != nil {
		includeDeletionMarkers = *policy.IncludeDeletionMarkers
	}
	return &LocalWatchedRootBackupLimits{
		Mode:                   policy.Mode,
		MaxFileBytes:           policy.MaxFileBytes,
		MaxBatchBytes:          policy.MaxBatchBytes,
		MaxPendingItems:        policy.MaxPendingItems,
		MaxPendingBytes:        policy.MaxPendingBytes,
		IncludeDeletionMarkers: includeDeletionMarkers,
		OnLimit:                policy.OnLimit,
	}
}
