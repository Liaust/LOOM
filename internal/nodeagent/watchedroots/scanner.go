package watchedroots

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/filesystemmeta"
)

type ScanRequest struct {
	Mode           string
	Root           ValidatedRoot
	StartedAt      time.Time
	Force          bool
	StabilityDelay time.Duration
	WorkerKey      string
}

func Reconcile(ctx context.Context, store Store, req ScanRequest) (ScanResult, error) {
	root := req.Root
	mode := normalizeScanMode(req.Mode)
	started := req.StartedAt
	if started.IsZero() {
		started = time.Now().UTC()
	}
	workerKey := strings.TrimSpace(req.WorkerKey)
	if workerKey == "" {
		workerKey = "node-agent.watched_root." + root.Config.RootKey
	}
	if err := store.EnsureRoot(root.Config.RootKey); err != nil {
		return ScanResult{}, err
	}
	dirtyHints, err := store.ListDirtyHints(root.Config.RootKey)
	if err != nil {
		return ScanResult{}, err
	}
	dirtyHints, ignoredHintPaths := effectiveDirtyHints(root, dirtyHints)
	if len(ignoredHintPaths) > 0 {
		if err := store.ClearDirtyHints(root.Config.RootKey, ignoredHintPaths); err != nil {
			return ScanResult{}, err
		}
	}
	checkpoint, checkpointErr := store.LoadCheckpoint(root.Config.RootKey)
	if checkpointErr != nil && !IsNotExist(checkpointErr) {
		return ScanResult{}, checkpointErr
	}
	if mode == ScanModeAuto {
		mode = autoScanMode(root, checkpoint, dirtyHints, started)
	}
	if mode == ScanModeDirty && hasRescanHint(dirtyHints) {
		mode = ScanModeFull
	}

	result := ScanResult{
		RootKey:   root.Config.RootKey,
		Mode:      mode,
		Status:    RunStatusHealthy,
		StartedAt: started,
		Counts: ScanCounts{
			DirtyHintsBefore:  len(dirtyHints),
			IgnoredDirtyHints: len(ignoredHintPaths),
			IgnoredBySource:   map[string]int{},
		},
	}
	if root.PolicyResolver != nil {
		result.PolicyVersion = filePolicyVersion(root)
		result.PolicyFingerprint = root.PolicyResolver.Fingerprint()
	}
	if mode == RunStatusSkipped {
		return finishScan(store, root, workerKey, result, checkpoint, dirtyHints, "no dirty hints and full rescan is not due")
	}
	if !root.RootReachable {
		finding := rootUnavailableFinding(root.Config.RootKey, "watched root path is not reachable")
		_ = store.SaveFinding(finding)
		result.Findings = append(result.Findings, finding)
		result.Counts.Findings = len(result.Findings)
		result.Status = RunStatusBlocked
		result.Message = "watched root is not reachable"
		return finishScan(store, root, workerKey, result, checkpoint, dirtyHints, result.Message)
	}
	if info, err := os.Stat(root.RootPath); err != nil || !info.IsDir() {
		message := "watched root is not reachable"
		if err == nil {
			message = "watched root is not a directory"
		}
		finding := rootUnavailableFinding(root.Config.RootKey, message)
		_ = store.SaveFinding(finding)
		result.Findings = append(result.Findings, finding)
		result.Counts.Findings = len(result.Findings)
		result.Status = RunStatusBlocked
		result.Message = message
		return finishScan(store, root, workerKey, result, checkpoint, dirtyHints, message)
	}

	switch mode {
	case ScanModeFull:
		err = reconcileFull(ctx, store, root, &result, started)
	case ScanModeDirty:
		err = reconcileDirty(ctx, store, root, &result, dirtyHints, started)
	default:
		err = fmt.Errorf("unsupported scan mode %q", mode)
	}
	if err != nil {
		return ScanResult{}, err
	}
	if scanCanResolveMissingFindings(result) {
		resolved, err := store.ResolveMissingFindings(root.Config.RootKey, result.Findings, started)
		if err != nil {
			return ScanResult{}, err
		}
		result.Counts.FindingsResolved = resolved
	}
	if result.Status == "" || result.Status == RunStatusHealthy {
		result.Status = statusFromResult(result)
	}
	if result.Message == "" {
		result.Message = "reconciliation completed"
	}
	return finishScan(store, root, workerKey, result, checkpoint, dirtyHints, result.Message)
}

func reconcileFull(ctx context.Context, store Store, root ValidatedRoot, result *ScanResult, started time.Time) error {
	previousStates, err := store.ListPathStates(root.Config.RootKey, 0)
	if err != nil {
		return err
	}
	previousByPath := map[string]PathState{}
	for _, state := range previousStates {
		previousByPath[state.RelativePath] = state
	}
	observed := map[string]struct{}{}
	casefoldSeen := map[string]string{}
	limits := scanLimits(root, started, result)
	err = filepath.WalkDir(root.RootPath, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			result.Counts.Errors++
			finding := Finding{
				RootKey:      root.Config.RootKey,
				Severity:     FindingSeverityWarning,
				Status:       FindingStatusOpen,
				Kind:         FindingPermissionDenied,
				RelativePath: relativeFromPath(root.RootPath, path),
				Summary:      "path could not be scanned",
				Details:      mustJSON(map[string]any{"error": walkErr.Error()}),
			}
			result.Findings = append(result.Findings, finding)
			_ = store.SaveFinding(finding)
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if limits.exhausted() {
			finding := budgetFinding(root.Config.RootKey, result.Counts)
			result.Findings = append(result.Findings, finding)
			_ = store.SaveFinding(finding)
			result.Counts.BudgetExhausted++
			result.Status = RunStatusDegraded
			return filepath.SkipAll
		}
		relativePath := relativeFromPath(root.RootPath, path)
		if relativePath == "." {
			return nil
		}
		obs := ObservePath(root, relativePath)
		observed[relativePath] = struct{}{}
		if err := recordPathCollisionFinding(store, root, casefoldSeen, &obs, result); err != nil {
			return err
		}
		if obs.Kind == PathKindDirectory {
			result.Counts.Directories++
		} else {
			result.Counts.Files++
		}
		result.Counts.Visited++
		state, change, err := reconcileObservation(store, root, previousByPath[relativePath], obs, result)
		if err != nil {
			return err
		}
		if change != nil {
			result.ChangedPaths = append(result.ChangedPaths, *change)
		}
		if obs.Kind == PathKindDirectory && !state.Classification.Included {
			switch state.Classification.ReasonCode {
			case ReasonExcludedFilePolicy:
				if root.PolicyResolver == nil || !root.PolicyResolver.MayIncludeDescendant(relativePath) {
					return filepath.SkipDir
				}
			case ReasonExcludedByPattern, ReasonExcludedHidden, ReasonSkippedSymlink, ReasonExcludedPackageBoundary:
				return filepath.SkipDir
			}
		}
		limits.update(result)
		return nil
	})
	if errors.Is(err, filepath.SkipAll) {
		err = nil
	}
	if err != nil {
		return err
	}
	if result.Counts.BudgetExhausted > 0 {
		return nil
	}
	if err := markExcludedByCurrentPolicy(store, root, previousStates, observed, started, result); err != nil {
		return err
	}
	return reconcileDeletedPaths(store, root, previousStates, observed, result)
}

func markExcludedByCurrentPolicy(store Store, root ValidatedRoot, previousStates []PathState, observed map[string]struct{}, now time.Time, result *ScanResult) error {
	for _, previousState := range previousStates {
		if previousState.Status == PathStatusExcluded || previousState.Status == PathStatusDeleted || previousState.Status == PathStatusMissingDeferred {
			continue
		}
		excluded, decision := excludedByCurrentFilePolicy(root, previousState.RelativePath, previousState.Kind == PathKindDirectory)
		if !excluded {
			continue
		}
		observed[previousState.RelativePath] = struct{}{}
		next := previousState
		next.SchemaVersion = PathStateSchemaVersion
		next.Status = PathStatusExcluded
		next.LastSeenAt = now
		next.LastScannedAt = now
		reasonCode := ReasonExcludedFilePolicy
		reason := "excluded by file policy pattern " + decision.Pattern
		if root.PolicyResolver == nil {
			reasonCode = ReasonExcludedByPattern
			reason = "excluded by pattern " + decision.Pattern
		}
		next.Classification = Classification{
			Included:       false,
			ReasonCode:     reasonCode,
			Reason:         reason,
			MatchedExclude: decision.Pattern,
			Safe:           true,
			Policies:       EffectivePolicies(root.Config, previousState.RelativePath),
		}
		if root.PolicyResolver != nil {
			next.Classification = withPolicyEvidence(next.Classification, decision, policyFingerprint(root))
		}
		next.HashStatus = HashStatusNotNeeded
		next.ContentHashURI = ""
		if err := store.SavePathState(next); err != nil {
			return err
		}
		result.Counts.Excluded++
		if root.PolicyResolver != nil {
			result.Counts.IgnoredBySource[string(decision.RuleCategory)]++
		}
		result.Counts.Changed++
		result.ChangedPaths = append(result.ChangedPaths, PathChange{
			RelativePath:   previousState.RelativePath,
			ChangeKind:     PathStatusExcluded,
			PreviousHash:   previousState.ContentHashURI,
			PreviousStatus: previousState.Status,
			CurrentStatus:  next.Status,
		})
	}
	return nil
}

func reconcileDirty(ctx context.Context, store Store, root ValidatedRoot, result *ScanResult, hints []DirtyHint, started time.Time) error {
	seen := map[string]struct{}{}
	for _, hint := range hints {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		relativePath := strings.TrimSpace(hint.RelativePath)
		if relativePath == "" || relativePath == "__rescan__" {
			continue
		}
		if _, exists := seen[relativePath]; exists {
			continue
		}
		seen[relativePath] = struct{}{}
		previous, prevErr := store.LoadPathState(root.Config.RootKey, relativePath)
		if prevErr != nil && !IsNotExist(prevErr) {
			return prevErr
		}
		obs := ObservePath(root, relativePath)
		if !obs.Exists {
			if prevErr == nil && previous.Status != PathStatusDeleted {
				deleted := deletedState(previous, time.Now().UTC(), false)
				if err := store.SavePathState(deleted); err != nil {
					return err
				}
				result.Counts.Deleted++
				result.Counts.Changed++
				result.ChangedPaths = append(result.ChangedPaths, PathChange{
					RelativePath:   previous.RelativePath,
					ChangeKind:     "deleted",
					PreviousHash:   previous.ContentHashURI,
					PreviousStatus: previous.Status,
					CurrentStatus:  deleted.Status,
				})
			}
			continue
		}
		if obs.Kind == PathKindDirectory {
			result.Counts.Directories++
		} else {
			result.Counts.Files++
		}
		result.Counts.Visited++
		state, change, err := reconcileObservation(store, root, previous, obs, result)
		if err != nil {
			return err
		}
		_ = state
		if change != nil {
			result.ChangedPaths = append(result.ChangedPaths, *change)
		}
	}
	return nil
}

func reconcileObservation(store Store, root ValidatedRoot, previous PathState, obs PathObservation, result *ScanResult) (PathState, *PathChange, error) {
	now := time.Now().UTC()
	classification := ClassifyPath(obs, root)
	state := PathState{
		SchemaVersion:    PathStateSchemaVersion,
		RootKey:          root.Config.RootKey,
		PathKey:          PathKey(root.Config.RootKey, obs.RelativePath),
		RelativePath:     obs.RelativePath,
		Status:           statusForClassification(classification),
		Kind:             obs.Kind,
		Classification:   classification,
		SizeBytes:        obs.SizeBytes,
		Mode:             obs.Mode,
		Fidelity:         obs.Fidelity,
		FirstSeenAt:      previous.FirstSeenAt,
		LastSeenAt:       now,
		LastScannedAt:    now,
		HashStatus:       HashStatusNotNeeded,
		LastErrorCode:    obs.ErrorCode,
		LastErrorMessage: obs.Error,
	}
	if !obs.ModifiedAt.IsZero() {
		modified := obs.ModifiedAt
		state.ModifiedAt = &modified
	}
	if state.FirstSeenAt.IsZero() {
		state.FirstSeenAt = now
	}
	if previous.DeletedAt != nil && state.Status == PathStatusDeleted {
		state.DeletedAt = previous.DeletedAt
	}
	preserveOutputState(previous, &state)
	if state.Status == PathStatusIncluded && obs.Kind == PathKindFile {
		hashURI, hashStatus, err := hashStableFile(root, obs.RelativePath, previous, result)
		if err != nil {
			state.HashStatus = HashStatusError
			state.LastErrorCode = HashStatusError
			state.LastErrorMessage = err.Error()
			result.Counts.Errors++
		} else {
			state.ContentHashURI = hashURI
			state.HashStatus = hashStatus
		}
	} else if state.Status == PathStatusSkipped && classification.ReasonCode == ReasonSkippedTooLarge {
		state.HashStatus = HashStatusTooLarge
	} else if obs.Kind != PathKindFile {
		state.HashStatus = HashStatusNotRegularFile
	}
	if err := recordObservationFinding(store, root, state, result); err != nil {
		return PathState{}, nil, err
	}
	countState(result, state)
	change := stateChange(previous, state)
	if change != nil {
		result.Counts.Changed++
	} else {
		result.Counts.Unchanged++
	}
	if err := store.SavePathState(state); err != nil {
		return PathState{}, nil, err
	}
	return state, change, nil
}

func preserveOutputState(previous PathState, state *PathState) {
	state.SyncStatus = previous.SyncStatus
	state.IndexStatus = previous.IndexStatus
	state.DeletionStatus = previous.DeletionStatus
	state.BackupStatus = previous.BackupStatus
	state.BackupMode = previous.BackupMode
	state.LocalObjectID = previous.LocalObjectID
	state.LocalVersionID = previous.LocalVersionID
	state.LocalSyncOutboxID = previous.LocalSyncOutboxID
	state.LocalBackupArtifactID = previous.LocalBackupArtifactID
	state.LocalBackupOutboxID = previous.LocalBackupOutboxID
	state.LocalBackupBatchID = previous.LocalBackupBatchID
	state.LastQueuedHashURI = previous.LastQueuedHashURI
	state.LastSyncedHashURI = previous.LastSyncedHashURI
	state.LastSyncedModifiedAt = previous.LastSyncedModifiedAt
	state.LastQueuedMetadataMtime = previous.LastQueuedMetadataMtime
	state.LastSyncedMetadataSequence = previous.LastSyncedMetadataSequence
	state.LastQueuedBackupHashURI = previous.LastQueuedBackupHashURI
	state.LastBackedUpHashURI = previous.LastBackedUpHashURI
	state.MainObjectID = previous.MainObjectID
	state.MainVersionID = previous.MainVersionID
	state.MainBlobID = previous.MainBlobID
	state.MainBackupBatchID = previous.MainBackupBatchID
	state.MainBackupItemID = previous.MainBackupItemID
	state.PrivateBackupOperationID = previous.PrivateBackupOperationID
	state.FileTransferID = previous.FileTransferID
	state.FileTransferStorageEntryID = previous.FileTransferStorageEntryID
	state.DeletionRequestID = previous.DeletionRequestID
	state.BackupDeletionMarkerID = previous.BackupDeletionMarkerID
	state.LastOutputPlannedAt = previous.LastOutputPlannedAt
	state.LastOutputAppliedAt = previous.LastOutputAppliedAt
	state.LastBackupQueuedAt = previous.LastBackupQueuedAt
	state.LastBackedUpAt = previous.LastBackedUpAt
	state.LastOutputErrorCode = previous.LastOutputErrorCode
	state.LastOutputErrorMessage = previous.LastOutputErrorMessage
	state.LastBackupErrorCode = previous.LastBackupErrorCode
	state.LastBackupErrorMessage = previous.LastBackupErrorMessage
}

func reconcileDeletedPaths(store Store, root ValidatedRoot, previous []PathState, observed map[string]struct{}, result *ScanResult) error {
	candidates := []PathState{}
	active := 0
	for _, state := range previous {
		if deletionReconcileActiveState(state) {
			active++
		}
		if _, ok := observed[state.RelativePath]; ok {
			continue
		}
		if !deletionReconcileCandidate(state) {
			continue
		}
		candidates = append(candidates, state)
	}
	if len(candidates) == 0 {
		return nil
	}
	percent := 0
	if active > 0 {
		percent = (len(candidates) * 100) / active
	}
	massDelete := len(candidates) >= root.Config.DeletePolicy.MassDeleteThresholdCount &&
		percent >= root.Config.DeletePolicy.MassDeleteThresholdPercent
	now := time.Now().UTC()
	if massDelete {
		finding := Finding{
			RootKey:  root.Config.RootKey,
			Severity: FindingSeverityCritical,
			Status:   FindingStatusOpen,
			Kind:     FindingMassDeleteDeferred,
			Summary:  "mass deletion protection deferred deletion output",
			Details: mustJSON(map[string]any{
				"candidate_count": len(candidates),
				"active_count":    active,
				"percent":         percent,
			}),
		}
		result.Findings = append(result.Findings, finding)
		if err := store.SaveFinding(finding); err != nil {
			return err
		}
		result.Status = RunStatusRequiresManualAction
	}
	for _, previousState := range candidates {
		next := deletedState(previousState, now, massDelete)
		if err := store.SavePathState(next); err != nil {
			return err
		}
		if massDelete {
			result.Counts.MissingDeferred++
		} else {
			result.Counts.Deleted++
		}
		result.Counts.Changed++
		result.ChangedPaths = append(result.ChangedPaths, PathChange{
			RelativePath:   previousState.RelativePath,
			ChangeKind:     next.Status,
			PreviousHash:   previousState.ContentHashURI,
			PreviousStatus: previousState.Status,
			CurrentStatus:  next.Status,
		})
	}
	return nil
}

func deletionReconcileActiveState(state PathState) bool {
	if state.Status == PathStatusDeleted || state.Status == PathStatusMissingDeferred {
		return false
	}
	if state.Status == PathStatusExcluded && !backedDirectoryMetadataState(state) {
		return false
	}
	return true
}

func deletionReconcileCandidate(state PathState) bool {
	if state.Status == PathStatusDeleted || state.Status == PathStatusMissingDeferred {
		return false
	}
	if state.Status == PathStatusExcluded && !backedDirectoryMetadataState(state) {
		return false
	}
	return true
}

func backedDirectoryMetadataState(state PathState) bool {
	if state.Kind != PathKindDirectory || state.Classification.ReasonCode != ReasonExcludedDirectoryMetadata {
		return false
	}
	return state.BackupStatus == OutputStatusQueued ||
		state.BackupStatus == OutputStatusRecorded ||
		state.BackupStatus == OutputStatusAlreadyCurrent ||
		state.LastQueuedBackupHashURI != "" ||
		state.LastBackedUpHashURI != ""
}

func deletedState(previous PathState, now time.Time, deferred bool) PathState {
	next := previous
	next.SchemaVersion = PathStateSchemaVersion
	if deferred {
		next.Status = PathStatusMissingDeferred
	} else {
		next.Status = PathStatusDeleted
	}
	next.Kind = PathKindMissing
	next.LastSeenAt = now
	next.LastScannedAt = now
	next.DeletedAt = &now
	next.Classification = Classification{
		Included:   false,
		ReasonCode: ReasonDeletedLocalState,
		Reason:     "path was not observed in the latest successful scan",
		Safe:       true,
		Policies:   previous.Classification.Policies,
	}
	return next
}

func finishScan(store Store, root ValidatedRoot, workerKey string, result ScanResult, previous RootCheckpoint, dirtyHints []DirtyHint, message string) (ScanResult, error) {
	finished := time.Now().UTC()
	result.FinishedAt = finished
	result.DurationMS = finished.Sub(result.StartedAt).Milliseconds()
	result.Message = message
	if result.Status == "" {
		result.Status = statusFromResult(result)
	}
	if result.Status != RunStatusBlocked {
		if result.Mode == ScanModeFull {
			result.Counts.DirtyHintsCleared = len(dirtyHints)
			if err := store.ClearDirtyHints(root.Config.RootKey, nil); err != nil {
				return ScanResult{}, err
			}
		} else if result.Mode == ScanModeDirty {
			paths := make([]string, 0, len(dirtyHints))
			for _, hint := range dirtyHints {
				paths = append(paths, hint.RelativePath)
			}
			result.Counts.DirtyHintsCleared = len(paths)
			if err := store.ClearDirtyHints(root.Config.RootKey, paths); err != nil {
				return ScanResult{}, err
			}
		}
	}
	findings, err := store.ListFindings(root.Config.RootKey, 0)
	if err != nil {
		return ScanResult{}, err
	}
	states, err := store.ListPathStates(root.Config.RootKey, 0)
	if err != nil {
		return ScanResult{}, err
	}
	if result.Status == RunStatusSkipped || result.Mode == RunStatusSkipped {
		applyStoredStateCounts(&result, states)
	}
	result.Counts.Findings = countActiveFindings(findings)
	result.Checkpoint = buildCheckpoint(root, workerKey, previous, result, states)
	result.Summary = buildSummary(root, workerKey, result, findings)
	if err := store.SaveCheckpoint(root.Config.RootKey, result.Checkpoint); err != nil {
		return ScanResult{}, err
	}
	if err := store.SaveLatestSummary(root.Config.RootKey, result.Summary); err != nil {
		return ScanResult{}, err
	}
	return result, nil
}

func scanCanResolveMissingFindings(result ScanResult) bool {
	return result.Mode == ScanModeFull &&
		result.Status != RunStatusBlocked &&
		result.Counts.BudgetExhausted == 0
}

func applyStoredStateCounts(result *ScanResult, states []PathState) {
	for _, state := range states {
		countState(result, state)
		switch state.Kind {
		case PathKindFile:
			result.Counts.Files++
		case PathKindDirectory:
			result.Counts.Directories++
		}
	}
}

func hashStableFile(root ValidatedRoot, relativePath string, previous PathState, result *ScanResult) (string, string, error) {
	target := filepath.Join(root.RootPath, filepath.FromSlash(relativePath))
	before, err := os.Stat(target)
	if err != nil {
		return "", HashStatusError, err
	}
	delay, err := time.ParseDuration(root.Config.Scan.StabilityWindow)
	if err != nil {
		return "", HashStatusError, err
	}
	if delay > 0 {
		time.Sleep(delay)
		after, err := os.Stat(target)
		if err != nil {
			return "", HashStatusError, err
		}
		if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
			result.Counts.HashDeferred++
			return previous.ContentHashURI, HashStatusDeferred, nil
		}
	}
	if before.Size() > root.Config.Scan.MaxHashFileBytes {
		return "", HashStatusTooLarge, nil
	}
	if before.Size() > root.Config.Scan.MaxHashBytesPerRun {
		result.Counts.HashDeferred++
		return previous.ContentHashURI, HashStatusDeferred, nil
	}
	file, err := os.Open(target)
	if err != nil {
		return "", HashStatusError, err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", HashStatusError, err
	}
	hashURI := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if hashURI == previous.ContentHashURI && previous.HashStatus != "" {
		result.Counts.HashUnchanged++
		return hashURI, HashStatusUnchanged, nil
	}
	result.Counts.HashComputed++
	return hashURI, HashStatusComputed, nil
}

func stateChange(previous PathState, current PathState) *PathChange {
	if previous.RelativePath == "" {
		return &PathChange{
			RelativePath:  current.RelativePath,
			ChangeKind:    "created",
			CurrentHash:   current.ContentHashURI,
			CurrentStatus: current.Status,
		}
	}
	if previous.Status == current.Status &&
		previous.ContentHashURI == current.ContentHashURI &&
		previous.SizeBytes == current.SizeBytes &&
		previous.Classification.ReasonCode == current.Classification.ReasonCode &&
		fidelityFingerprint(previous.Fidelity) == fidelityFingerprint(current.Fidelity) {
		return nil
	}
	kind := "modified"
	if current.Status == PathStatusDeleted || current.Status == PathStatusMissingDeferred {
		kind = current.Status
	} else if previous.Status != current.Status {
		kind = "status_changed"
	}
	return &PathChange{
		RelativePath:   current.RelativePath,
		ChangeKind:     kind,
		PreviousHash:   previous.ContentHashURI,
		CurrentHash:    current.ContentHashURI,
		PreviousStatus: previous.Status,
		CurrentStatus:  current.Status,
	}
}

func buildCheckpoint(root ValidatedRoot, workerKey string, previous RootCheckpoint, result ScanResult, states []PathState) RootCheckpoint {
	checkpoint := previous
	checkpoint.SchemaVersion = CheckpointSchemaVersion
	checkpoint.RootKey = root.Config.RootKey
	checkpoint.WorkerKey = workerKey
	checkpoint.ConfigHash = root.ConfigHash
	checkpoint.LastSequence++
	checkpoint.LastStartedAt = &result.StartedAt
	checkpoint.LastFinishedAt = &result.FinishedAt
	checkpoint.RootReachable = result.Status != RunStatusBlocked
	checkpoint.PathStateCount = len(states)
	checkpoint.PathStateSnapshotHash = pathStateSnapshotHash(states)
	if result.Status != RunStatusBlocked {
		checkpoint.LastSuccessfulReconcileAt = &result.FinishedAt
		if result.Mode == ScanModeFull {
			checkpoint.LastFullRescanAt = &result.FinishedAt
		}
		checkpoint.PendingRescan = false
		checkpoint.LastErrorCode = ""
		checkpoint.LastErrorMessage = ""
	} else {
		checkpoint.LastErrorCode = ReasonRootUnavailable
		checkpoint.LastErrorMessage = result.Message
	}
	return checkpoint
}

func buildSummary(root ValidatedRoot, workerKey string, result ScanResult, findings []Finding) RootSummary {
	summary := RootSummary{
		SchemaVersion:     SummarySchemaVersion,
		RootKey:           root.Config.RootKey,
		WorkerKey:         workerKey,
		Status:            result.Status,
		RootReachable:     result.Status != RunStatusBlocked,
		LastScanAt:        result.FinishedAt,
		Included:          result.Counts.Included,
		Excluded:          result.Counts.Excluded,
		Skipped:           result.Counts.Skipped,
		Deleted:           result.Counts.Deleted,
		MissingDeferred:   result.Counts.MissingDeferred,
		Changed:           result.Counts.Changed,
		HashComputed:      result.Counts.HashComputed,
		BudgetExhausted:   result.Counts.BudgetExhausted,
		DirtyHints:        result.Counts.DirtyHintsBefore - result.Counts.DirtyHintsCleared,
		Findings:          countActiveFindings(findings),
		Message:           result.Message,
		GeneratedAt:       result.FinishedAt,
		PolicyVersion:     result.PolicyVersion,
		PolicyFingerprint: result.PolicyFingerprint,
		IgnoredBySource:   copyStringIntMap(result.Counts.IgnoredBySource),
	}
	if result.Mode == ScanModeFull {
		summary.LastFullScanAt = result.FinishedAt
	}
	return summary
}

func countActiveFindings(findings []Finding) int {
	count := 0
	for _, finding := range findings {
		if FindingIsActive(finding) {
			count++
		}
	}
	return count
}

func pathStateSnapshotHash(states []PathState) string {
	builder := strings.Builder{}
	for _, state := range states {
		builder.WriteString(state.RelativePath)
		builder.WriteByte('\n')
		builder.WriteString(state.Status)
		builder.WriteByte('\n')
		builder.WriteString(state.ContentHashURI)
		builder.WriteByte('\n')
		builder.WriteString(fidelityFingerprint(state.Fidelity))
		builder.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(builder.String()))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func recordPathCollisionFinding(store Store, root ValidatedRoot, seen map[string]string, obs *PathObservation, result *ScanResult) error {
	if obs == nil || obs.Fidelity == nil || root.Config.FidelityPolicy.PathCollisionMode != PathCollisionModeWarn {
		return nil
	}
	key := strings.TrimSpace(obs.Fidelity.CasefoldKey)
	if key == "" {
		return nil
	}
	if previous, ok := seen[key]; ok && previous != obs.RelativePath {
		obs.Fidelity.Risks = appendUniqueString(obs.Fidelity.Risks, filesystemmeta.FidelityRiskPathCollision)
		finding := Finding{
			RootKey:      root.Config.RootKey,
			Severity:     FindingSeverityWarning,
			Status:       FindingStatusOpen,
			Kind:         FindingPathCollisionWarning,
			RelativePath: obs.RelativePath,
			Summary:      "path has a case or Unicode-normalized name collision inside the watched root",
			Details: mustJSON(map[string]any{
				"casefold_key":  key,
				"previous_path": previous,
				"current_path":  obs.RelativePath,
			}),
		}
		result.Findings = append(result.Findings, finding)
		return store.SaveFinding(finding)
	}
	seen[key] = obs.RelativePath
	return nil
}

func recordObservationFinding(store Store, root ValidatedRoot, state PathState, result *ScanResult) error {
	if state.Fidelity == nil {
		return nil
	}
	var finding Finding
	switch {
	case state.Fidelity.Kind == filesystemmeta.ObjectKindSymlink && containsString(state.Fidelity.Risks, filesystemmeta.FidelityRiskExternalReference):
		finding = Finding{
			RootKey:      root.Config.RootKey,
			Severity:     FindingSeverityWarning,
			Status:       FindingStatusOpen,
			Kind:         FindingExternalSymlinkReference,
			RelativePath: state.RelativePath,
			Summary:      "symlink points outside the watched root",
		}
	case state.Fidelity.Kind == filesystemmeta.ObjectKindSpecial:
		finding = Finding{
			RootKey:      root.Config.RootKey,
			Severity:     FindingSeverityWarning,
			Status:       FindingStatusOpen,
			Kind:         FindingSpecialFileObserved,
			RelativePath: state.RelativePath,
			Summary:      "special filesystem entry was observed and skipped",
		}
	case state.Fidelity.Kind == filesystemmeta.ObjectKindPackage && state.Classification.ReasonCode == ReasonExcludedPackageBoundary:
		finding = Finding{
			RootKey:      root.Config.RootKey,
			Severity:     FindingSeverityInfo,
			Status:       FindingStatusOpen,
			Kind:         FindingPackageBoundaryObserved,
			RelativePath: state.RelativePath,
			Summary:      "package directory boundary was observed and not descended into",
		}
	case state.Fidelity.GeneratedMetadata && state.Classification.ReasonCode == ReasonExcludedGeneratedMetadata:
		finding = Finding{
			RootKey:      root.Config.RootKey,
			Severity:     FindingSeverityInfo,
			Status:       FindingStatusOpen,
			Kind:         FindingGeneratedMetadataObserved,
			RelativePath: state.RelativePath,
			Summary:      "generated Apple metadata was observed and excluded",
		}
	default:
		return nil
	}
	finding.Details = mustJSON(map[string]any{
		"fidelity": state.Fidelity,
	})
	result.Findings = append(result.Findings, finding)
	return store.SaveFinding(finding)
}

func fidelityFingerprint(obs *filesystemmeta.Observation) string {
	if obs == nil {
		return ""
	}
	raw, _ := json.Marshal(obs)
	return string(raw)
}

func appendUniqueString(values []string, value string) []string {
	if containsString(values, value) {
		return values
	}
	return append(values, value)
}

func containsString(values []string, value string) bool {
	for _, existing := range values {
		if existing == value {
			return true
		}
	}
	return false
}

func statusForClassification(classification Classification) string {
	if classification.Included {
		return PathStatusIncluded
	}
	switch classification.ReasonCode {
	case ReasonSkippedPathEscape, ReasonSkippedSymlink, ReasonSkippedSpecialFile, ReasonSkippedPermissionDenied, ReasonSkippedTooLarge:
		return PathStatusSkipped
	default:
		return PathStatusExcluded
	}
}

func countState(result *ScanResult, state PathState) {
	switch state.Status {
	case PathStatusIncluded:
		result.Counts.Included++
	case PathStatusExcluded:
		result.Counts.Excluded++
		if state.Classification.PolicyRuleSource != "" && state.Classification.PolicyRuleSource != string(filepolicy.RuleCategoryNone) {
			if result.Counts.IgnoredBySource == nil {
				result.Counts.IgnoredBySource = map[string]int{}
			}
			result.Counts.IgnoredBySource[state.Classification.PolicyRuleSource]++
		}
	case PathStatusSkipped:
		result.Counts.Skipped++
	case PathStatusDeleted:
		result.Counts.Deleted++
	case PathStatusMissingDeferred:
		result.Counts.MissingDeferred++
	case PathStatusError:
		result.Counts.Errors++
	}
}

func effectiveDirtyHints(root ValidatedRoot, hints []DirtyHint) ([]DirtyHint, []string) {
	if root.PolicyResolver == nil {
		return hints, nil
	}
	effective := make([]DirtyHint, 0, len(hints))
	ignored := []string{}
	for _, hint := range hints {
		if hint.RelativePath == "" || hint.RelativePath == "__rescan__" {
			effective = append(effective, hint)
			continue
		}
		isDir := false
		if info, err := os.Lstat(filepath.Join(root.RootPath, filepath.FromSlash(hint.RelativePath))); err == nil {
			isDir = info.IsDir()
		}
		excluded, _ := excludedByCurrentFilePolicy(root, hint.RelativePath, isDir)
		if excluded {
			ignored = append(ignored, hint.RelativePath)
			continue
		}
		effective = append(effective, hint)
	}
	return effective, ignored
}

func excludedByCurrentFilePolicy(root ValidatedRoot, relativePath string, isDir bool) (bool, filepolicy.Decision) {
	if root.PolicyResolver != nil {
		resolution, err := root.PolicyResolver.Resolve(relativePath, isDir)
		if err == nil {
			return !resolution.Decision.Included, resolution.Decision
		}
	}
	excluded, pattern := MatchAny(root.Config.Exclude, relativePath)
	return excluded, filepolicy.Decision{Path: relativePath, Included: !excluded, RuleCategory: filepolicy.RuleCategoryContract, Pattern: pattern}
}

func policyFingerprint(root ValidatedRoot) string {
	if root.PolicyResolver == nil {
		return ""
	}
	return root.PolicyResolver.Fingerprint()
}

func filePolicyVersion(root ValidatedRoot) string {
	if root.PolicyResolver == nil {
		return ""
	}
	return filepolicy.BuiltInPolicyVersion
}

func copyStringIntMap(input map[string]int) map[string]int {
	if len(input) == 0 {
		return nil
	}
	result := make(map[string]int, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func statusFromResult(result ScanResult) string {
	if result.Status == RunStatusRequiresManualAction || result.Status == RunStatusBlocked {
		return result.Status
	}
	if result.Counts.BudgetExhausted > 0 || result.Counts.Errors > 0 || hasDegradingFindings(result.Findings) {
		return RunStatusDegraded
	}
	return RunStatusHealthy
}

func hasDegradingFindings(findings []Finding) bool {
	for _, finding := range findings {
		if finding.Severity != FindingSeverityInfo {
			return true
		}
	}
	return false
}

func normalizeScanMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "", ScanModeAuto:
		return ScanModeAuto
	case ScanModeFull, ScanModeDirty:
		return mode
	default:
		return mode
	}
}

func autoScanMode(root ValidatedRoot, checkpoint RootCheckpoint, hints []DirtyHint, now time.Time) string {
	if len(hints) > 0 {
		return ScanModeDirty
	}
	if checkpoint.LastFullRescanAt == nil {
		return ScanModeFull
	}
	interval, err := time.ParseDuration(root.Config.Scan.FullRescanInterval)
	if err != nil || interval <= 0 {
		return ScanModeFull
	}
	if checkpoint.LastFullRescanAt.Add(interval).Before(now) {
		return ScanModeFull
	}
	return RunStatusSkipped
}

func hasRescanHint(hints []DirtyHint) bool {
	for _, hint := range hints {
		if hint.HintKind == DirtyHintRescanRequired || hint.RelativePath == "__rescan__" {
			return true
		}
	}
	return false
}

func relativeFromPath(rootPath, path string) string {
	relativePath, err := filepath.Rel(rootPath, path)
	if err != nil {
		return filepath.ToSlash(filepath.Base(path))
	}
	if relativePath == "" {
		return "."
	}
	return filepath.ToSlash(relativePath)
}

func rootUnavailableFinding(rootKey, message string) Finding {
	return Finding{
		RootKey:  rootKey,
		Severity: FindingSeverityCritical,
		Status:   FindingStatusOpen,
		Kind:     FindingRootUnavailable,
		Summary:  message,
	}
}

func budgetFinding(rootKey string, counts ScanCounts) Finding {
	return Finding{
		RootKey:  rootKey,
		Severity: FindingSeverityWarning,
		Status:   FindingStatusOpen,
		Kind:     FindingScanBudgetExceeded,
		Summary:  "scan budget was exhausted before the root was fully reconciled",
		Details: mustJSON(map[string]any{
			"visited":                         counts.Visited,
			"files":                           counts.Files,
			"directories":                     counts.Directories,
			"deletion_reconciliation_skipped": true,
		}),
	}
}

type scanBudget struct {
	root       ValidatedRoot
	started    time.Time
	maxRuntime time.Duration
	result     *ScanResult
}

func scanLimits(root ValidatedRoot, started time.Time, result *ScanResult) scanBudget {
	maxRuntime, _ := time.ParseDuration(root.Config.Scan.MaxRuntime)
	return scanBudget{root: root, started: started, maxRuntime: maxRuntime, result: result}
}

func (b scanBudget) exhausted() bool {
	if b.root.Config.Scan.MaxFilesPerRun > 0 && b.result.Counts.Files >= b.root.Config.Scan.MaxFilesPerRun {
		return true
	}
	if b.root.Config.Scan.MaxDirsPerRun > 0 && b.result.Counts.Directories >= b.root.Config.Scan.MaxDirsPerRun {
		return true
	}
	if b.maxRuntime > 0 && time.Since(b.started) > b.maxRuntime {
		return true
	}
	return false
}

func (b scanBudget) update(result *ScanResult) {
}

func mustJSON(value map[string]any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}
