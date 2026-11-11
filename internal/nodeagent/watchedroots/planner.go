package watchedroots

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"loom.local/loom/internal/filesystemconnector"
	"loom.local/loom/internal/filesystemmeta"
	"loom.local/loom/internal/ids"
	loomsync "loom.local/loom/internal/sync"
)

var ErrPolicyInputsChanged = errors.New("file policy changed before output planning")

type OutputPlan struct {
	RootKey     string         `json:"root_key"`
	WorkerKey   string         `json:"worker_key,omitempty"`
	GeneratedAt time.Time      `json:"generated_at"`
	Counts      OutputCounts   `json:"counts"`
	Actions     []OutputAction `json:"actions,omitempty"`
	Page        *OutputPage    `json:"page,omitempty"`
}

type OutputPage struct {
	After      string `json:"after,omitempty"`
	Next       string `json:"next,omitempty"`
	More       bool   `json:"more"`
	StatesRead int    `json:"states_read"`
}

// Match one normal delivery page instead of growing a fresh staging backlog on
// every continuation. Manual full plans remain unaffected.
const OutputBackupPageSize = 16

type OutputCounts struct {
	SyncObjects           int `json:"sync_objects"`
	SyncMetadata          int `json:"sync_metadata"`
	FileObservations      int `json:"file_observations,omitempty"`
	DeletionRequests      int `json:"deletion_requests"`
	BackupFiles           int `json:"backup_files"`
	BackupMetadata        int `json:"backup_metadata"`
	BackupDeletionMarkers int `json:"backup_deletion_markers"`
	BackupSkipped         int `json:"backup_skipped"`
	Skipped               int `json:"skipped"`
	AlreadyCurrent        int `json:"already_current"`
	Failed                int `json:"failed"`
	Deferred              int `json:"deferred,omitempty"`
}

type OutputAction struct {
	ActionKind             string                      `json:"action_kind"`
	Status                 string                      `json:"status"`
	RootKey                string                      `json:"root_key"`
	RelativePath           string                      `json:"relative_path"`
	LocalObjectID          string                      `json:"local_object_id,omitempty"`
	LocalVersionID         string                      `json:"local_version_id,omitempty"`
	LocalSyncOutboxID      string                      `json:"local_sync_outbox_id,omitempty"`
	LocalBackupArtifactID  string                      `json:"local_backup_artifact_id,omitempty"`
	LocalBackupBatchID     string                      `json:"local_backup_batch_id,omitempty"`
	LocalBackupItemID      string                      `json:"local_backup_item_id,omitempty"`
	LocalBackupOutboxID    string                      `json:"local_backup_outbox_id,omitempty"`
	MainObjectID           string                      `json:"main_object_id,omitempty"`
	MainVersionID          string                      `json:"main_version_id,omitempty"`
	ContentHashURI         string                      `json:"content_hash_uri,omitempty"`
	PreviousHashURI        string                      `json:"previous_hash_uri,omitempty"`
	LogicalName            string                      `json:"logical_name,omitempty"`
	SourcePath             string                      `json:"source_path,omitempty"`
	SizeBytes              int64                       `json:"size_bytes,omitempty"`
	ModifiedAt             *time.Time                  `json:"modified_at,omitempty"`
	ProjectRef             string                      `json:"project_ref,omitempty"`
	ScopeRef               string                      `json:"scope_ref,omitempty"`
	BackupMode             string                      `json:"backup_mode,omitempty"`
	BackupItemKind         string                      `json:"backup_item_kind,omitempty"`
	BackupMaxFileBytes     int64                       `json:"backup_max_file_bytes,omitempty"`
	BackupMaxBatchBytes    int64                       `json:"backup_max_batch_bytes,omitempty"`
	BackupMaxPendingItems  int                         `json:"backup_max_pending_items,omitempty"`
	BackupMaxPendingBytes  int64                       `json:"backup_max_pending_bytes,omitempty"`
	SyncPolicy             string                      `json:"sync_policy,omitempty"`
	IndexPolicy            string                      `json:"index_policy,omitempty"`
	DeletePolicy           string                      `json:"delete_policy,omitempty"`
	DeletedAt              *time.Time                  `json:"deleted_at,omitempty"`
	ReasonCode             string                      `json:"reason_code,omitempty"`
	Reason                 string                      `json:"reason,omitempty"`
	Error                  string                      `json:"error,omitempty"`
	Fidelity               *filesystemmeta.Observation `json:"fidelity,omitempty"`
	SourceGeneration       string                      `json:"source_generation,omitempty"`
	SourcePresence         string                      `json:"source_presence,omitempty"`
	ObservationFingerprint string                      `json:"observation_fingerprint,omitempty"`
}

func PlanOutputs(store Store, root ValidatedRoot, workerKey string) (OutputPlan, error) {
	return planOutputs(store, root, workerKey, func() ([]PathState, error) {
		return store.ListPathStates(root.Config.RootKey, 0)
	}, nil, 0)
}

func PlanOutputsPage(store Store, root ValidatedRoot, workerKey, after string, stateLimit, actionLimit int) (OutputPlan, error) {
	if actionLimit < 3 {
		return OutputPlan{}, fmt.Errorf("output page must allow all actions for one path")
	}
	page := &OutputPage{After: after, Next: after}
	return planOutputs(store, root, workerKey, func() ([]PathState, error) {
		states, more, err := store.ListPathStatesPage(root.Config.RootKey, after, stateLimit)
		page.More, page.StatesRead = more, len(states)
		return states, err
	}, page, actionLimit)
}

// PlanOutputsForPaths plans exact persisted rows, not prefixes or a complete
// inventory. Its caller must retain full planning for previously unsettled work.
func PlanOutputsForPaths(store Store, root ValidatedRoot, workerKey string, paths []string) (OutputPlan, error) {
	return planOutputs(store, root, workerKey, func() ([]PathState, error) {
		selected := make(map[string]bool, len(paths))
		for _, path := range paths {
			relative, _, err := filesystemconnector.NormalizeRelativePath(path)
			if err != nil || relative == "." || relative == "__rescan__" {
				return nil, fmt.Errorf("invalid output planning path %q", path)
			}
			selected[relative] = true
		}
		ordered := make([]string, 0, len(selected))
		for path := range selected {
			ordered = append(ordered, path)
		}
		sort.Strings(ordered)
		states := make([]PathState, 0, len(ordered))
		for _, path := range ordered {
			state, err := store.LoadPathState(root.Config.RootKey, path)
			if err != nil {
				return nil, err
			}
			if state.RootKey != root.Config.RootKey || state.RelativePath != path {
				return nil, fmt.Errorf("output planning path identity mismatch for %q", path)
			}
			states = append(states, state)
		}
		return states, nil
	}, nil, 0)
}

func planOutputs(store Store, root ValidatedRoot, workerKey string, loadStates func() ([]PathState, error), page *OutputPage, actionLimit int) (OutputPlan, error) {
	now := time.Now().UTC()
	plan := OutputPlan{
		RootKey:     root.Config.RootKey,
		WorkerKey:   workerKey,
		GeneratedAt: now,
		Page:        page,
	}
	checkpoint, err := store.LoadCheckpoint(root.Config.RootKey)
	if err != nil && !IsNotExist(err) {
		return OutputPlan{}, err
	}
	if coverageConfigMatches(root, checkpoint) && checkpoint.ObservationConfigHash != "" {
		root.ObservationConfigHash = checkpoint.ObservationConfigHash
	}
	states, err := loadStates()
	if err != nil {
		return OutputPlan{}, err
	}
	if root.PolicyResolver != nil {
		paths := make([]string, 0, len(states))
		for _, state := range states {
			paths = append(paths, state.RelativePath)
		}
		current, err := root.PolicyResolver.CurrentForPaths(paths)
		if err != nil {
			return OutputPlan{}, err
		}
		if !current {
			return OutputPlan{}, ErrPolicyInputsChanged
		}
	}
	backupActions := 0
	for _, state := range states {
		actions := planOutputActions(root, state)
		pending, backups := 0, 0
		for _, action := range actions {
			if action.Status != OutputStatusAlreadyCurrent {
				pending++
				switch action.ActionKind {
				case OutputActionBackupFile, OutputActionBackupMetadata, OutputActionBackupDeletionMarker:
					backups++
				}
			}
		}
		if actionLimit > 0 && (len(plan.Actions)+pending > actionLimit || backupActions+backups > OutputBackupPageSize) {
			page.More = true
			break
		}
		backupActions += backups
		if page != nil {
			page.Next = state.PathKey
		}
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
			case OutputActionFileObservation:
				plan.Counts.FileObservations++
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
	if root.Config.SchemaVersion == IndependentConfigSchemaVersion {
		if _, err := effectivePolicies(root.Config, state.RelativePath); err != nil {
			return nil
		}
	}
	actions := []OutputAction{}
	if action, ok := planFileObservation(root, state); ok {
		state.LocalObjectID = action.LocalObjectID
		actions = append(actions, action)
	}
	if action, ok := planSyncOutputAction(root, state); ok {
		actions = append(actions, action)
	}
	if action, ok := planBackupOutputAction(root, state); ok {
		actions = append(actions, action)
	}
	return actions
}

func planFileObservation(root ValidatedRoot, state PathState) (OutputAction, bool) {
	if root.ConfigHash == "" || !root.RootReachable {
		return OutputAction{}, false
	}
	presence := "present"
	switch {
	case state.Status == PathStatusIncluded && state.Kind == PathKindFile && state.ModifiedAt != nil:
	case state.Status == PathStatusDeleted && state.LocalObjectID != "":
		presence = "missing"
	case (state.Status == PathStatusError || state.Status == PathStatusSkipped) && state.LocalObjectID != "" && state.Classification.ReasonCode == ReasonSkippedPermissionDenied:
		presence = "unavailable"
	default:
		// Unreadable/incomplete scopes are not deletion evidence. Excluded and
		// internal paths are not newly enrolled by a metadata observation.
		return OutputAction{}, false
	}
	payload, _ := json.Marshal(struct {
		Generation, Presence string
		Size                 int64
		Mtime                *time.Time
	}{firstNonEmpty(root.ObservationConfigHash, root.ConfigHash), presence, state.SizeBytes, state.ModifiedAt})
	digest := sha256.Sum256(payload)
	fingerprint := hex.EncodeToString(digest[:])
	if fingerprint == state.LastQueuedFileObservation {
		return OutputAction{}, false
	}
	localID := state.LocalObjectID
	if localID == "" {
		localID = ids.NewObjectID()
	}
	return OutputAction{ActionKind: OutputActionFileObservation, RelativePath: state.RelativePath,
		LocalObjectID: localID, SourcePath: watchedRootSourcePath(root.Config.RootKey, state.RelativePath),
		SourceGeneration: root.ConfigHash, SourcePresence: presence, SizeBytes: state.SizeBytes,
		ModifiedAt: state.ModifiedAt, ObservationFingerprint: fingerprint}, true
}

func isExcludedByCurrentRootConfig(root ValidatedRoot, relativePath string) bool {
	if relativePath == "" {
		return false
	}
	if root.Config.SchemaVersion == IndependentConfigSchemaVersion {
		if included, _ := MatchAny(root.Config.Include, relativePath); !included {
			return true
		}
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
		base.Reason = "path exceeds watched-root sync file limit"
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
	inlineLimit := int64(loomsync.MaxStreamObjectUploadBytes)
	if configured <= 0 || configured > inlineLimit {
		return inlineLimit
	}
	return configured
}

func planDeletionAction(root ValidatedRoot, state PathState) (OutputAction, bool) {
	policies := outputPolicies(root, state)
	if root.Config.SchemaVersion == IndependentConfigSchemaVersion && policies.Sync != SyncModeSelectedFiles {
		return OutputAction{}, false
	}
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
	if state.BackupStatus == OutputStatusFailed && state.LastBackupErrorCode == FindingBackupBatchRejected &&
		state.DeletedAt != nil && state.LastBackupQueuedAt != nil && !state.DeletedAt.After(*state.LastBackupQueuedAt) {
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
	if root.Config.SchemaVersion == IndependentConfigSchemaVersion {
		return EffectivePolicies(root.Config, state.RelativePath)
	}
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
