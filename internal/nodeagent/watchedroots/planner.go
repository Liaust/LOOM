package watchedroots

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"time"

	"loom.local/loom/internal/filesystemmeta"
	"loom.local/loom/internal/ids"
	loomsync "loom.local/loom/internal/sync"
)

type OutputPlan struct {
	RootKey     string         `json:"root_key"`
	WorkerKey   string         `json:"worker_key,omitempty"`
	GeneratedAt time.Time      `json:"generated_at"`
	Counts      OutputCounts   `json:"counts"`
	Actions     []OutputAction `json:"actions,omitempty"`
}

type OutputCounts struct {
	SyncObjects           int `json:"sync_objects"`
	SyncMetadata          int `json:"sync_metadata"`
	DeletionRequests      int `json:"deletion_requests"`
	BackupFiles           int `json:"backup_files"`
	BackupMetadata        int `json:"backup_metadata"`
	BackupDeletionMarkers int `json:"backup_deletion_markers"`
	BackupSkipped         int `json:"backup_skipped"`
	Skipped               int `json:"skipped"`
	AlreadyCurrent        int `json:"already_current"`
	Failed                int `json:"failed"`
}

type OutputAction struct {
	ActionKind            string                      `json:"action_kind"`
	Status                string                      `json:"status"`
	RootKey               string                      `json:"root_key"`
	RelativePath          string                      `json:"relative_path"`
	LocalObjectID         string                      `json:"local_object_id,omitempty"`
	LocalVersionID        string                      `json:"local_version_id,omitempty"`
	LocalSyncOutboxID     string                      `json:"local_sync_outbox_id,omitempty"`
	LocalBackupArtifactID string                      `json:"local_backup_artifact_id,omitempty"`
	LocalBackupBatchID    string                      `json:"local_backup_batch_id,omitempty"`
	LocalBackupItemID     string                      `json:"local_backup_item_id,omitempty"`
	LocalBackupOutboxID   string                      `json:"local_backup_outbox_id,omitempty"`
	MainObjectID          string                      `json:"main_object_id,omitempty"`
	MainVersionID         string                      `json:"main_version_id,omitempty"`
	ContentHashURI        string                      `json:"content_hash_uri,omitempty"`
	PreviousHashURI       string                      `json:"previous_hash_uri,omitempty"`
	LogicalName           string                      `json:"logical_name,omitempty"`
	SourcePath            string                      `json:"source_path,omitempty"`
	SizeBytes             int64                       `json:"size_bytes,omitempty"`
	ModifiedAt            *time.Time                  `json:"modified_at,omitempty"`
	ProjectRef            string                      `json:"project_ref,omitempty"`
	ScopeRef              string                      `json:"scope_ref,omitempty"`
	BackupMode            string                      `json:"backup_mode,omitempty"`
	BackupItemKind        string                      `json:"backup_item_kind,omitempty"`
	BackupMaxFileBytes    int64                       `json:"backup_max_file_bytes,omitempty"`
	BackupMaxBatchBytes   int64                       `json:"backup_max_batch_bytes,omitempty"`
	BackupMaxPendingItems int                         `json:"backup_max_pending_items,omitempty"`
	BackupMaxPendingBytes int64                       `json:"backup_max_pending_bytes,omitempty"`
	SyncPolicy            string                      `json:"sync_policy,omitempty"`
	IndexPolicy           string                      `json:"index_policy,omitempty"`
	DeletePolicy          string                      `json:"delete_policy,omitempty"`
	DeletedAt             *time.Time                  `json:"deleted_at,omitempty"`
	ReasonCode            string                      `json:"reason_code,omitempty"`
	Reason                string                      `json:"reason,omitempty"`
	Error                 string                      `json:"error,omitempty"`
	Fidelity              *filesystemmeta.Observation `json:"fidelity,omitempty"`
}

func PlanOutputs(store Store, root ValidatedRoot, workerKey string) (OutputPlan, error) {
	now := time.Now().UTC()
	plan := OutputPlan{
		RootKey:     root.Config.RootKey,
		WorkerKey:   workerKey,
		GeneratedAt: now,
	}
	states, err := store.ListPathStates(root.Config.RootKey, 0)
	if err != nil {
		return OutputPlan{}, err
	}
	for _, state := range states {
		actions := planOutputActions(root, state)
		for _, action := range actions {
			if action.Status == OutputStatusAlreadyCurrent {
				plan.Counts.AlreadyCurrent++
				continue
			}
			action.RootKey = root.Config.RootKey
			if action.Status == "" {
				action.Status = OutputStatusPending
			}
			plan.Actions = append(plan.Actions, action)
			switch action.ActionKind {
			case OutputActionSyncObject:
				plan.Counts.SyncObjects++
			case OutputActionSyncMetadata:
				plan.Counts.SyncMetadata++
			case OutputActionDeletionRequest:
				plan.Counts.DeletionRequests++
			case OutputActionBackupFile:
				plan.Counts.BackupFiles++
			case OutputActionBackupMetadata:
				plan.Counts.BackupMetadata++
			case OutputActionBackupDeletionMarker:
				plan.Counts.BackupDeletionMarkers++
			case OutputActionBackupSkipped:
				plan.Counts.BackupSkipped++
			case OutputActionSkipped:
				plan.Counts.Skipped++
			}
		}
	}
	return plan, nil
}

func planOutputActions(root ValidatedRoot, state PathState) []OutputAction {
	if isExcludedByCurrentRootConfig(root, state.RelativePath) {
		return nil
	}
	actions := []OutputAction{}
	if action, ok := planSyncOutputAction(root, state); ok {
		actions = append(actions, action)
	}
	if action, ok := planBackupOutputAction(root, state); ok {
		actions = append(actions, action)
	}
	return actions
}

func isExcludedByCurrentRootConfig(root ValidatedRoot, relativePath string) bool {
	if relativePath == "" {
		return false
	}
	excluded, _ := excludedByCurrentFilePolicy(root, relativePath, false)
	return excluded
}

func planSyncOutputAction(root ValidatedRoot, state PathState) (OutputAction, bool) {
	if state.Status == PathStatusDeleted {
		return planDeletionAction(root, state)
	}
	policies := outputPolicies(root, state)
	if policies.Sync != SyncModeSelectedFiles {
		return OutputAction{}, false
	}
	if state.Status != PathStatusIncluded || state.Kind != PathKindFile {
		return OutputAction{}, false
	}
	base := OutputAction{
		ActionKind:      OutputActionSyncObject,
		RelativePath:    state.RelativePath,
		ContentHashURI:  state.ContentHashURI,
		PreviousHashURI: state.LastQueuedHashURI,
		LogicalName:     syncLogicalName(root, state.RelativePath),
		SourcePath:      watchedRootSourcePath(root.Config.RootKey, state.RelativePath),
		SizeBytes:       state.SizeBytes,
		ModifiedAt:      state.ModifiedAt,
		ProjectRef:      root.Config.SyncPolicy.ProjectRef,
		ScopeRef:        root.Config.SyncPolicy.ScopeRef,
		SyncPolicy:      policies.Sync,
		IndexPolicy:     policies.Index,
		DeletePolicy:    policies.Delete,
		Fidelity:        state.Fidelity,
	}
	if state.ContentHashURI == "" {
		base.ActionKind = OutputActionSkipped
		base.Status = OutputStatusSkipped
		base.ReasonCode = HashStatusNotNeeded
		base.Reason = "path has no content hash to sync"
		return base, true
	}
	if state.HashStatus == HashStatusDeferred {
		base.ActionKind = OutputActionSkipped
		base.Status = OutputStatusSkipped
		base.ReasonCode = HashStatusDeferred
		base.Reason = "path hash is deferred until the file becomes stable"
		return base, true
	}
	if maxFileBytes := effectiveSyncMaxFileBytes(root.Config.SyncPolicy.MaxFileBytes); maxFileBytes > 0 && state.SizeBytes > maxFileBytes {
		base.ActionKind = OutputActionSkipped
		base.Status = OutputStatusSkipped
		base.ReasonCode = ReasonSkippedTooLarge
		base.Reason = "path exceeds watched-root inline sync limit"
		return base, true
	}
	if state.ContentHashURI == state.LastSyncedHashURI &&
		(state.SyncStatus == loomsync.ItemStatusAccepted || state.SyncStatus == loomsync.ItemStatusDuplicate) &&
		state.MainObjectID != "" && state.MainVersionID != "" && state.LocalVersionID != "" &&
		state.ModifiedAt != nil && !state.ModifiedAt.IsZero() &&
		(state.LastSyncedModifiedAt == nil || !state.ModifiedAt.Equal(*state.LastSyncedModifiedAt) ||
			(state.LastQueuedMetadataMtime != nil && !state.ModifiedAt.Equal(*state.LastQueuedMetadataMtime))) {
		base.ActionKind = OutputActionSyncMetadata
		base.LocalObjectID, base.LocalVersionID = state.LocalObjectID, state.LocalVersionID
		base.MainObjectID, base.MainVersionID = state.MainObjectID, state.MainVersionID
		return base, true
	}
	if state.ContentHashURI == state.LastQueuedHashURI ||
		(state.ContentHashURI == state.LastSyncedHashURI && state.SyncStatus != OutputStatusQueued) {
		base.Status = OutputStatusAlreadyCurrent
		return base, true
	}
	base.LocalObjectID = state.LocalObjectID
	if base.LocalObjectID == "" {
		base.LocalObjectID = ids.NewObjectID()
	}
	base.LocalVersionID = ids.NewObjectVersionID()
	return base, true
}

func effectiveSyncMaxFileBytes(configured int64) int64 {
	inlineLimit := int64(loomsync.MaxInlineObjectUploadBytes)
	if configured <= 0 || configured > inlineLimit {
		return inlineLimit
	}
	return configured
}

func planDeletionAction(root ValidatedRoot, state PathState) (OutputAction, bool) {
	policies := outputPolicies(root, state)
	if policies.Delete != DeleteModeTombstone {
		return OutputAction{}, false
	}
	if state.LocalObjectID == "" && state.MainObjectID == "" {
		return OutputAction{}, false
	}
	if state.DeletionStatus == OutputStatusQueued || state.DeletionStatus == OutputStatusRecorded || state.DeletionStatus == OutputStatusAlreadyCurrent {
		return OutputAction{}, false
	}
	return OutputAction{
		ActionKind:      OutputActionDeletionRequest,
		Status:          OutputStatusPending,
		RelativePath:    state.RelativePath,
		LocalObjectID:   state.LocalObjectID,
		MainObjectID:    state.MainObjectID,
		ContentHashURI:  state.ContentHashURI,
		ProjectRef:      root.Config.SyncPolicy.ProjectRef,
		ScopeRef:        root.Config.SyncPolicy.ScopeRef,
		SyncPolicy:      policies.Sync,
		IndexPolicy:     policies.Index,
		DeletePolicy:    policies.Delete,
		ReasonCode:      ReasonDeletedLocalState,
		Reason:          "path was deleted from watched root",
		PreviousHashURI: state.LastQueuedHashURI,
		Fidelity:        state.Fidelity,
	}, true
}

func planBackupOutputAction(root ValidatedRoot, state PathState) (OutputAction, bool) {
	policy := root.Config.BackupPolicy
	policies := outputPolicies(root, state)
	if policies.Backup == BackupModeNone {
		return OutputAction{}, false
	}
	if state.Status == PathStatusDeleted {
		return planBackupDeletionAction(root, state)
	}
	if state.Kind == PathKindDirectory && state.Classification.ReasonCode == ReasonExcludedDirectoryMetadata {
		return planBackupDirectoryAction(root, state)
	}
	if state.Status != PathStatusIncluded || state.Kind != PathKindFile {
		return OutputAction{}, false
	}
	actionKind := OutputActionBackupFile
	itemKind := "file"
	if policies.Backup == BackupModeMetadataOnly {
		actionKind = OutputActionBackupMetadata
		itemKind = "metadata"
	}
	base := OutputAction{
		ActionKind:            actionKind,
		RelativePath:          state.RelativePath,
		ContentHashURI:        state.ContentHashURI,
		PreviousHashURI:       state.LastQueuedBackupHashURI,
		SizeBytes:             state.SizeBytes,
		ModifiedAt:            state.ModifiedAt,
		BackupMode:            policies.Backup,
		BackupItemKind:        itemKind,
		BackupMaxFileBytes:    policy.MaxFileBytes,
		BackupMaxBatchBytes:   policy.MaxBatchBytes,
		BackupMaxPendingItems: policy.MaxPendingItems,
		BackupMaxPendingBytes: policy.MaxPendingBytes,
		Fidelity:              state.Fidelity,
	}
	if state.HashStatus == HashStatusDeferred {
		base.ActionKind = OutputActionBackupSkipped
		base.Status = OutputStatusSkipped
		base.ReasonCode = HashStatusDeferred
		base.Reason = "path hash is deferred until the file becomes stable"
		return base, true
	}
	if state.ContentHashURI == "" {
		base.ActionKind = OutputActionBackupSkipped
		base.Status = OutputStatusSkipped
		base.ReasonCode = HashStatusNotNeeded
		base.Reason = "path has no content hash to back up"
		return base, true
	}
	if policies.Backup != BackupModeMetadataOnly && state.SizeBytes > policy.MaxFileBytes {
		base.ActionKind = OutputActionBackupSkipped
		base.Status = OutputStatusSkipped
		base.ReasonCode = FindingBackupFileTooLarge
		base.Reason = "path exceeds backup policy max_file_bytes"
		return base, true
	}
	if state.ContentHashURI == state.LastQueuedBackupHashURI || state.ContentHashURI == state.LastBackedUpHashURI {
		base.Status = OutputStatusAlreadyCurrent
		return base, true
	}
	return base, true
}

func planBackupDirectoryAction(root ValidatedRoot, state PathState) (OutputAction, bool) {
	policy := root.Config.BackupPolicy
	policies := outputPolicies(root, state)
	if policies.Backup == BackupModeNone {
		return OutputAction{}, false
	}
	hashURI := metadataHashURI("directory", state)
	base := OutputAction{
		ActionKind:            OutputActionBackupMetadata,
		RelativePath:          state.RelativePath,
		ContentHashURI:        hashURI,
		PreviousHashURI:       state.LastQueuedBackupHashURI,
		SizeBytes:             state.SizeBytes,
		ModifiedAt:            state.ModifiedAt,
		BackupMode:            policies.Backup,
		BackupItemKind:        "directory",
		BackupMaxBatchBytes:   policy.MaxBatchBytes,
		BackupMaxPendingItems: policy.MaxPendingItems,
		BackupMaxPendingBytes: policy.MaxPendingBytes,
		Fidelity:              state.Fidelity,
	}
	if hashURI == state.LastQueuedBackupHashURI || hashURI == state.LastBackedUpHashURI {
		base.Status = OutputStatusAlreadyCurrent
		return base, true
	}
	return base, true
}

func planBackupDeletionAction(root ValidatedRoot, state PathState) (OutputAction, bool) {
	policy := root.Config.BackupPolicy
	policies := outputPolicies(root, state)
	if policies.Backup == BackupModeNone || !backupDeletionMarkersEnabled(policy) {
		return OutputAction{}, false
	}
	if state.BackupDeletionMarkerID != "" && (state.BackupStatus == OutputStatusQueued || state.BackupStatus == OutputStatusRecorded || state.BackupStatus == OutputStatusAlreadyCurrent) {
		return OutputAction{}, false
	}
	deletedAt := state.DeletedAt
	return OutputAction{
		ActionKind:            OutputActionBackupDeletionMarker,
		Status:                OutputStatusPending,
		RelativePath:          state.RelativePath,
		ContentHashURI:        state.ContentHashURI,
		PreviousHashURI:       firstNonEmpty(state.LastQueuedBackupHashURI, state.LastBackedUpHashURI, state.ContentHashURI),
		SizeBytes:             state.SizeBytes,
		BackupMode:            policies.Backup,
		BackupItemKind:        "deletion_marker",
		BackupMaxBatchBytes:   policy.MaxBatchBytes,
		BackupMaxPendingItems: policy.MaxPendingItems,
		BackupMaxPendingBytes: policy.MaxPendingBytes,
		DeletedAt:             deletedAt,
		ReasonCode:            ReasonDeletedLocalState,
		Reason:                "path was deleted from watched root",
		Fidelity:              state.Fidelity,
	}, true
}

func syncLogicalName(root ValidatedRoot, relativePath string) string {
	switch root.Config.SyncPolicy.LogicalNameStrategy {
	case SyncLogicalNameBasename:
		return filepath.Base(filepath.FromSlash(relativePath))
	default:
		return relativePath
	}
}

func watchedRootSourcePath(rootKey string, relativePath string) string {
	return "watched-root://" + rootKey + "/" + relativePath
}

func backupDeletionMarkersEnabled(policy BackupPolicy) bool {
	if policy.IncludeDeletionMarkers == nil {
		return true
	}
	return *policy.IncludeDeletionMarkers
}

func outputPolicies(root ValidatedRoot, state PathState) Policies {
	policies := state.Classification.Policies
	if policies.Backup == "" && policies.Sync == "" && policies.Index == "" && policies.Delete == "" {
		return EffectivePolicies(root.Config, state.RelativePath)
	}
	return policies
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func metadataHashURI(kind string, state PathState) string {
	raw, _ := json.Marshal(map[string]any{
		"kind":     kind,
		"path":     state.RelativePath,
		"mode":     state.Mode,
		"fidelity": state.Fidelity,
	})
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
