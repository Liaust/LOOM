package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/filesystemconnector"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodeagent/watchedroots"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectquiescence"
	"loom.local/loom/internal/projectregistration"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
)

// This reserved runtime lock serializes whole-config read/modify/write. Acquire
// it before any worker lock; release it before scans or network requests.
func lockWatchedRootConfig(ctx context.Context, store Store) (Config, func() error, error) {
	release, err := noderuntime.NewStore(store.DataDir).AcquireWorkerExecutionLock(ctx, "__node_filesystem_config__")
	if err != nil {
		return Config{}, nil, err
	}
	config, err := store.LoadConfig()
	if err != nil {
		_ = release()
		return Config{}, nil, err
	}
	return config, release, nil
}

// BuildProjectWatchReconcileAck is the typed D3b handler. D3c owns adding its
// message kind to the main switch. ACK proves configuration, never processing,
// indexing health, backup custody or payload verification.
func BuildProjectWatchReconcileAck(ctx context.Context, store Store, state State, message communication.Message) communication.AckInput {
	ack := buildAckInput(state, message)
	payload, err := projectwatch.DecodeDeclarationWatchPayload(message.PayloadJSON)
	if err != nil || message.Kind != communication.KindProjectWatchReconcile || message.Direction != communication.DirectionMainToNode {
		return protectedFolderPreflightFailure(ack, communication.AckStatusFailedPermanent, "project_watch.invalid_request", "The project watched-root request is invalid.")
	}
	result := projectwatch.DeclarationWatchAck{SchemaVersion: projectwatch.DeclarationWatchSchemaVersion, OperationID: payload.OperationID, ProjectID: payload.Group.ProjectID, NodeID: state.NodeID, GroupHash: payload.GroupHash, Evidence: communication.AppliedStateEvidence{SchemaVersion: communication.ControlEvidenceSchemaVersion, DesiredRevision: payload.Evidence.DesiredRevision, ConfigHash: payload.GroupHash, Outcome: communication.ControlOutcomeFailed}}
	status := communication.AckStatusFailedRetryable
	if message.NodeID != state.NodeID || payload.Group.NodeID != state.NodeID {
		result.ErrorCode = "project_watch.wrong_node"
		status = communication.AckStatusRejected
	} else {
		config, release, lockErr := lockWatchedRootConfig(ctx, store)
		if lockErr != nil {
			result.ErrorCode = "project_watch.config_unavailable"
		} else {
			result, status = reconcileProjectWatchLocked(ctx, store, config, state, payload, result)
			if releaseErr := release(); releaseErr != nil {
				result.ErrorCode = "project_watch.config_lock_release_failed"
				result.Evidence.Outcome = communication.ControlOutcomeFailed
				status = communication.AckStatusFailedRetryable
			}
		}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return protectedFolderPreflightFailure(ack, communication.AckStatusFailedRetryable, "project_watch.result_unavailable", "The node could not record project configuration evidence.")
	}
	ack.AckStatus = status
	ack.ResultJSON = raw
	ack.ErrorJSON = json.RawMessage(`{}`)
	if result.ErrorCode != "" {
		ack.ErrorJSON = projectWatchJSON(map[string]string{"code": result.ErrorCode})
	}
	return ack
}

func reconcileProjectWatchLocked(ctx context.Context, store Store, config Config, state State, payload projectwatch.DeclarationWatchPayload, result projectwatch.DeclarationWatchAck) (projectwatch.DeclarationWatchAck, string) {
	failure := func(code string) (projectwatch.DeclarationWatchAck, string) {
		result.ErrorCode = "project_watch." + code
		return result, communication.AckStatusFailedRetryable
	}
	if config.NodeKey != payload.Group.NodeKey {
		return failure("node_binding_changed")
	}
	root, err := canonicalDirectoryPath(payload.Group.ProjectRoot)
	if err != nil {
		return failure("project_root_unavailable")
	}
	box, err := canonicalDirectoryPath(config.BoxRootPath)
	if err != nil {
		return failure("configured_box_required")
	}
	rel, err := filepath.Rel(box, root)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return failure("outside_configured_box")
	}
	if reason := forbiddenProtectedFolderReason(root, store.DataDir); reason != "" {
		return failure("forbidden_project_root")
	}
	if err = verifyLocalProjectWatchSource(payload); err != nil {
		return failure("source_changed_or_invalid")
	}
	safeKey, err := projectwatch.DeclarationSafeRootKey(payload.Group.ProjectID)
	if err != nil {
		return failure("invalid_project_identity")
	}
	localStore := watchedroots.NewStore(store.DataDir)
	current, err := localStore.LoadProjectWatchReconcileState(payload.Group.ProjectID)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return failure("state_unreadable")
	}
	known := err == nil
	if known && (current.NodeID != state.NodeID || current.ProjectRoot != root || current.SafeRootKey != safeKey) {
		return failure("project_binding_changed")
	}
	result.Evidence.AppliedRevision = current.AppliedRevision
	newest, newestHash := current.AppliedRevision, current.ConfigHash
	if current.PendingRevision > newest {
		newest, newestHash = current.PendingRevision, current.PendingHash
	}
	tokenHash, err := communication.ConfigHash(payload.Tokens)
	if err != nil {
		return failure("invalid_token_evidence")
	}
	if known && newest == payload.Evidence.DesiredRevision && (current.OperationID != payload.OperationID || current.TokenHash != tokenHash) {
		return failure("revision_token_conflict")
	}
	decision, err := communication.CompareRevision(newest, newestHash, payload.Evidence)
	if err != nil {
		return failure("invalid_revision")
	}
	if decision == communication.RevisionDecisionStale {
		result.ErrorCode = "project_watch.stale_revision"
		result.Evidence.Outcome = communication.ControlOutcomeStale
		return result, communication.AckStatusCompleted
	}
	if decision == communication.RevisionDecisionConflict {
		return failure("revision_conflict")
	}
	if payload.Group.Predecessor != nil {
		return reconcileLegacyProjectWatchLocked(ctx, store, config, state, payload, result, current, known, root, safeKey, tokenHash)
	}
	runtimeStore := noderuntime.NewStore(store.DataDir)
	owned := map[string]bool{}
	for _, key := range current.RootKeys {
		owned[key] = true
	}
	// Stage a detached config; never adopt a manual/global/foreign safe root.
	configRaw, _ := json.Marshal(config)
	var candidate Config
	if json.Unmarshal(configRaw, &candidate) != nil {
		return failure("config_invalid")
	}
	metadata := projectWatchJSON(map[string]string{"source": "project.declaration", "project_id": payload.Group.ProjectID, "project_root": root, "node_id": state.NodeID})
	safeRoot := filesystemconnector.SafeRoot{RootKey: safeKey, DisplayName: payload.Group.ProjectID, AbsolutePath: root, AllowList: true, AllowMetadata: true, AllowIngest: true, MaxFileBytes: filesystemconnector.DefaultMaxFileBytes, Metadata: metadata}
	if existing, exists := filesystemconnector.FindSafeRoot(candidate.Filesystem, safeKey); exists {
		if !known || !projectWatchSameJSON(existing.Metadata, metadata) || filepath.Clean(existing.AbsolutePath) != root {
			return failure("safe_root_owner_conflict")
		}
		safeRoot = existing
	}
	for _, existing := range candidate.Filesystem.SafeRoots {
		if existing.RootKey == safeKey {
			continue
		}
		existingPath, pathErr := filesystemconnector.CanonicalSafeRootPath(existing)
		if pathErr == nil && (existingPath == root || strings.HasPrefix(existing.RootKey, "declaration_") && pathsOverlap(existingPath, root)) {
			return failure("project_path_owner_conflict")
		}
	}
	desired := map[string]projects.DeclarationWatchRoot{}
	validated := map[string]watchedroots.ValidatedRoot{}
	for _, item := range payload.Group.Roots {
		desired[item.BackendRootKey] = item
		var cfg watchedroots.RootConfig
		if communication.DecodeStrictJSONObject(item.ConfigJSON, &cfg) != nil {
			return failure("effective_config_invalid")
		}
		if existing, loadErr := runtimeStore.LoadInstance(item.WorkerKey); loadErr == nil {
			if !owned[item.BackendRootKey] {
				return failure("worker_owner_conflict")
			}
			var prior watchedroots.RootConfig
			if existing.Kind != noderuntime.KindWatchedRoot || json.Unmarshal(existing.ConfigJSON, &prior) != nil || prior.SafeRootKey != safeKey || prior.RootKey != item.BackendRootKey {
				return failure("worker_binding_conflict")
			}
		} else if !errors.Is(loadErr, fs.ErrNotExist) {
			return failure("worker_unreadable")
		}
		if limit := watchedRootRequiredSafeRootMaxFileBytes(cfg); limit > safeRoot.MaxFileBytes {
			safeRoot.MaxFileBytes = limit
		}
	}
	if len(payload.Group.Roots) > 0 || len(owned) > 0 {
		candidate.Filesystem = filesystemconnector.UpsertSafeRoot(candidate.Filesystem, safeRoot)
	}
	if err = filesystemconnector.ValidateConfig(candidate.Filesystem); err != nil {
		return failure("safe_root_invalid")
	}
	for _, item := range payload.Group.Roots {
		if !item.Enabled {
			continue
		}
		var cfg watchedroots.RootConfig
		_ = json.Unmarshal(item.ConfigJSON, &cfg)
		v, err := watchedroots.ValidateRootConfig(cfg, candidate.Filesystem)
		if err != nil || v.ConfigHash != item.ConfigHash {
			return failure("root_validation_failed")
		}
		fenced, err := runtimeStore.ProjectArchiveFenceActive(projectquiescence.TargetKindWatchedRoot, item.WorkerKey)
		if err != nil || fenced {
			return failure("archive_fenced")
		}
		validated[item.BackendRootKey] = v
	}
	if current.AppliedRevision == payload.Evidence.DesiredRevision && current.ConfigHash == payload.GroupHash && projectWatchDesiredApplied(runtimeStore, config, metadata, safeKey, owned, desired) {
		result.Evidence.Outcome = communication.ControlOutcomeCompleted
		return result, communication.AckStatusCompleted
	}
	for key := range desired {
		owned[key] = true
	}
	keys := []string{}
	for key := range owned {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pending := watchedroots.ProjectWatchReconcileState{OperationID: payload.OperationID, TokenHash: tokenHash, ProjectID: payload.Group.ProjectID, NodeID: state.NodeID, ProjectRoot: root, SafeRootKey: safeKey, AppliedRevision: current.AppliedRevision, ConfigHash: current.ConfigHash, PendingRevision: payload.Evidence.DesiredRevision, PendingHash: payload.GroupHash, RootKeys: keys}
	// Reservation comes after collision checks, before any config/worker changes.
	// Failed/partial application is resumable with this exact source and revision.
	if err = localStore.SaveProjectWatchReconcileState(pending); err != nil {
		return failure("pending_state_write_failed")
	}
	if !projectWatchSameJSON(projectWatchJSON(config), projectWatchJSON(candidate)) {
		if err = store.SaveConfig(candidate); err != nil {
			return failure("config_write_failed")
		}
	}
	for _, key := range keys {
		if err = ctx.Err(); err != nil {
			return failure("cancelled")
		}
		item, wanted := desired[key]
		if wanted && item.Enabled {
			if _, err = applyWatchedRootValidated(store, candidate, validated[key], item.LocalRootKey); err != nil {
				return failure("worker_apply_failed")
			}
		} else {
			workerKey := noderuntime.WatchedRootWorkerKey(key)
			release, lockErr := runtimeStore.AcquireWorkerExecutionLock(ctx, workerKey)
			if lockErr != nil {
				return failure("worker_lock_failed")
			}
			instance, loadErr := runtimeStore.LoadInstance(workerKey)
			if loadErr == nil {
				var cfg watchedroots.RootConfig
				if json.Unmarshal(instance.ConfigJSON, &cfg) != nil || cfg.SafeRootKey != safeKey || cfg.RootKey != key {
					_ = release()
					return failure("retirement_owner_conflict")
				}
				if instance.Enabled {
					instance.Enabled = false
					loadErr = runtimeStore.SaveInstanceWhileLocked(instance)
				}
			}
			releaseErr := release()
			if loadErr != nil && !errors.Is(loadErr, fs.ErrNotExist) || releaseErr != nil {
				return failure("worker_retirement_failed")
			}
		}
	}
	if err = verifyLocalProjectWatchSource(payload); err != nil {
		return failure("source_changed_during_apply")
	}
	if !projectWatchDesiredApplied(runtimeStore, candidate, metadata, safeKey, owned, desired) {
		return failure("applied_config_not_observed")
	}
	pending.AppliedRevision = payload.Evidence.DesiredRevision
	pending.ConfigHash = payload.GroupHash
	pending.PendingRevision = 0
	pending.PendingHash = ""
	if err = localStore.SaveProjectWatchReconcileState(pending); err != nil {
		return failure("applied_state_write_failed")
	}
	result.Evidence.AppliedRevision = pending.AppliedRevision
	result.Evidence.Outcome = communication.ControlOutcomeCompleted
	return result, communication.AckStatusCompleted
}
func verifyLocalProjectWatchSource(payload projectwatch.DeclarationWatchPayload) error {
	analysis := pc.Analyze(payload.Group.ProjectRoot)
	if analysis.Loaded == nil || analysis.Loaded.Declaration == nil || analysis.Loaded.Declaration.Project.Status == "archived" {
		return fmt.Errorf("source unavailable or archived")
	}
	expected, err := projectwatch.BuildDeclarationWatchGroup(analysis, payload.Group.NodeID, payload.Group.NodeKey, payload.Group.Contributors)
	if err != nil {
		return err
	}
	desired := payload.Group
	desired.Predecessor = nil
	if p := payload.Group.Predecessor; p != nil {
		intent, e := projectregistration.BuildDeclarationIntent(analysis, "project.declaration")
		physical, pathErr := canonicalDirectoryPath(payload.Group.ProjectRoot)
		if e != nil || pathErr != nil || physical != p.PhysicalRoot || !projectWatchSameJSON(intent.LegacyContract, p.Contract) {
			return fmt.Errorf("legacy source binding changed")
		}
	} else if analysis.Loaded.Declaration.LegacyContracts != nil {
		return fmt.Errorf("legacy predecessor required")
	}
	if !projectWatchSameJSON(projectWatchJSON(expected), projectWatchJSON(desired)) {
		return fmt.Errorf("source/group mismatch")
	}
	return nil
}

// Configuration lock is already held. Take every selected worker lock in sorted
// order, validate the whole set, and retain the locks through the local commit.
func reconcileLegacyProjectWatchLocked(ctx context.Context, store Store, config Config, state State, payload projectwatch.DeclarationWatchPayload, result projectwatch.DeclarationWatchAck, current watchedroots.ProjectWatchReconcileState, known bool, root, safeKey, tokenHash string) (out projectwatch.DeclarationWatchAck, status string) {
	fail := func(code string) (projectwatch.DeclarationWatchAck, string) {
		result.ErrorCode = "project_watch." + code
		return result, communication.AckStatusFailedRetryable
	}
	runtimeStore := noderuntime.NewStore(store.DataDir)
	localStore := watchedroots.NewStore(store.DataDir)
	keys := []string{}
	for _, item := range payload.Group.Roots {
		keys = append(keys, item.WorkerKey)
	}
	sort.Strings(keys)
	releases := []func() error{}
	defer func() {
		for i := len(releases) - 1; i >= 0; i-- {
			if err := releases[i](); err != nil {
				out, status = fail("worker_lock_release_failed")
			}
		}
	}()
	for _, key := range keys {
		release, err := runtimeStore.AcquireWorkerExecutionLock(ctx, key)
		if err != nil {
			return fail("worker_lock_failed")
		}
		releases = append(releases, release)
	}
	p := payload.Group.Predecessor
	oldSafe, exists := filesystemconnector.FindSafeRoot(config.Filesystem, "project")
	oldPath, pathErr := filesystemconnector.CanonicalSafeRootPath(oldSafe)
	if !exists || pathErr != nil || oldPath != root || root != p.PhysicalRoot {
		return fail("legacy_safe_root_binding_changed")
	}
	metadata := projectWatchJSON(map[string]string{"source": "project.declaration", "project_id": payload.Group.ProjectID, "project_root": root, "node_id": state.NodeID})
	successorSafe := oldSafe
	successorSafe.RootKey = safeKey
	successorSafe.Metadata = metadata
	for _, existing := range config.Filesystem.SafeRoots {
		if existing.RootKey == "project" || existing.RootKey == safeKey {
			continue
		}
		path, err := filesystemconnector.CanonicalSafeRootPath(existing)
		if err == nil && (path == root || strings.HasPrefix(existing.RootKey, "declaration_") && pathsOverlap(path, root)) {
			return fail("project_path_owner_conflict")
		}
	}
	proof := current.Legacy
	if known {
		if proof == nil || proof.OperationID != payload.OperationID || proof.TokenHash != tokenHash || proof.GroupHash != payload.GroupHash || proof.Revision != payload.Evidence.DesiredRevision || proof.PredecessorDigest != p.Digest || !projectWatchSameJSON(projectWatchJSON(proof.SafeRoot), projectWatchJSON(oldSafe)) || !projectWatchSameJSON(projectWatchJSON(proof.SuccessorSafeRoot), projectWatchJSON(successorSafe)) || len(proof.Members) != len(payload.Group.Roots) {
			return fail("legacy_pending_binding_changed")
		}
	} else {
		proof = &watchedroots.ProjectWatchLegacyObservation{OperationID: payload.OperationID, TokenHash: tokenHash, GroupHash: payload.GroupHash, Revision: payload.Evidence.DesiredRevision, PredecessorDigest: p.Digest, SafeRoot: oldSafe, SuccessorSafeRoot: successorSafe, Members: []watchedroots.ProjectWatchLegacyWorker{}}
	}
	actualSafe, hasSuccessorSafe := filesystemconnector.FindSafeRoot(config.Filesystem, safeKey)
	if hasSuccessorSafe && (!known || !projectWatchSameJSON(projectWatchJSON(actualSafe), projectWatchJSON(successorSafe))) {
		return fail("safe_root_owner_conflict")
	}
	if current.AppliedRevision == payload.Evidence.DesiredRevision && !hasSuccessorSafe {
		return fail("legacy_applied_safe_root_missing")
	}
	candidate := config
	candidate.Filesystem = filesystemconnector.Config{SafeRoots: append([]filesystemconnector.SafeRoot{}, config.Filesystem.SafeRoots...)}
	candidate.Filesystem = filesystemconnector.UpsertSafeRoot(candidate.Filesystem, successorSafe)
	if filesystemconnector.ValidateConfig(candidate.Filesystem) != nil {
		return fail("safe_root_invalid")
	}
	allApplied := hasSuccessorSafe
	for i, item := range payload.Group.Roots {
		legacy := p.Members[i].Registration
		actual, err := runtimeStore.LoadInstance(item.WorkerKey)
		if err != nil {
			return fail("legacy_worker_unavailable")
		}
		if info, err := os.Stat(localStore.RootDir(item.BackendRootKey)); err != nil || !info.IsDir() {
			return fail("legacy_root_state_unavailable")
		}
		var member watchedroots.ProjectWatchLegacyWorker
		if known {
			member = proof.Members[i]
		} else {
			member = watchedroots.ProjectWatchLegacyWorker{RootKey: item.BackendRootKey, Predecessor: actual}
		}
		before := member.Predecessor
		if member.RootKey != item.BackendRootKey || before.WorkerKey != item.WorkerKey || before.Kind != noderuntime.KindWatchedRoot || !before.Enabled || before.LocalRootKey != item.LocalRootKey || before.DisplayName != legacy.DisplayName || before.ConfigHash != legacy.ConfigHash || !projectWatchSameJSON(before.ConfigJSON, legacy.ConfigJSON) || before.CreatedAt.IsZero() || before.IntervalSeconds <= 0 || before.LeaseTimeoutSeconds <= 0 {
			return fail("legacy_worker_binding_changed")
		}
		var oldConfig, desiredConfig watchedroots.RootConfig
		if communication.DecodeStrictJSONObject(before.ConfigJSON, &oldConfig) != nil || communication.DecodeStrictJSONObject(item.ConfigJSON, &desiredConfig) != nil {
			return fail("effective_config_invalid")
		}
		oldValidated, oldErr := watchedroots.ValidateRootConfig(oldConfig, config.Filesystem)
		validated, err := watchedroots.ValidateRootConfig(desiredConfig, candidate.Filesystem)
		if oldErr != nil || oldValidated.ConfigHash != before.ConfigHash || err != nil || validated.ConfigHash != item.ConfigHash || successorSafe.MaxFileBytes < watchedRootRequiredSafeRootMaxFileBytes(desiredConfig) {
			return fail("root_validation_failed")
		}
		after := before
		after.ConfigJSON = item.ConfigJSON
		after.ConfigHash = item.ConfigHash
		after.UpdatedAt = time.Time{} // runtime owns save time
		if known && !projectWatchSameJSON(projectWatchJSON(member.Successor), projectWatchJSON(after)) {
			return fail("legacy_successor_binding_changed")
		}
		member.Successor = after
		applied := legacyWorkerSame(actual, after)
		if !applied && !projectWatchSameJSON(projectWatchJSON(actual), projectWatchJSON(before)) || applied && !hasSuccessorSafe {
			return fail("legacy_worker_drift")
		}
		if current.AppliedRevision == payload.Evidence.DesiredRevision && !applied {
			return fail("legacy_applied_worker_changed")
		}
		allApplied = allApplied && applied
		fenced, err := runtimeStore.ProjectArchiveFenceActive(projectquiescence.TargetKindWatchedRoot, item.WorkerKey)
		if err != nil || fenced {
			return fail("archive_fenced")
		}
		if !known {
			proof.Members = append(proof.Members, member)
		}
	}
	if ctx.Err() != nil {
		return fail("cancelled")
	}
	if allApplied && current.AppliedRevision == payload.Evidence.DesiredRevision {
		result.Evidence.Outcome = communication.ControlOutcomeCompleted
		return result, communication.AckStatusCompleted
	}
	proof.Digest, _ = watchedroots.ProjectWatchLegacyObservationHash(*proof)
	rootKeys := []string{}
	for _, member := range proof.Members {
		rootKeys = append(rootKeys, member.RootKey)
	}
	pending := watchedroots.ProjectWatchReconcileState{OperationID: payload.OperationID, TokenHash: tokenHash, ProjectID: payload.Group.ProjectID, NodeID: state.NodeID, ProjectRoot: root, SafeRootKey: safeKey, AppliedRevision: current.AppliedRevision, ConfigHash: current.ConfigHash, PendingRevision: payload.Evidence.DesiredRevision, PendingHash: payload.GroupHash, RootKeys: rootKeys, Legacy: proof}
	if err := localStore.SaveProjectWatchReconcileState(pending); err != nil {
		return fail("pending_state_write_failed")
	}
	if !hasSuccessorSafe {
		if err := store.SaveConfig(candidate); err != nil {
			return fail("config_write_failed")
		}
	}
	for _, member := range proof.Members {
		if ctx.Err() != nil {
			return fail("cancelled")
		}
		actual, err := runtimeStore.LoadInstance(member.Successor.WorkerKey)
		if err != nil {
			return fail("worker_unreadable")
		}
		if !legacyWorkerSame(actual, member.Successor) {
			if err := applyProjectWatchInstanceWhileLocked(store, member.Successor); err != nil {
				return fail("worker_apply_failed")
			}
		}
	}
	if verifyLocalProjectWatchSource(payload) != nil {
		return fail("source_changed_during_apply")
	}
	for _, member := range proof.Members {
		actual, err := runtimeStore.LoadInstance(member.Successor.WorkerKey)
		if err != nil || !legacyWorkerSame(actual, member.Successor) {
			return fail("applied_config_not_observed")
		}
	}
	pending.AppliedRevision = payload.Evidence.DesiredRevision
	pending.ConfigHash = payload.GroupHash
	pending.PendingRevision = 0
	pending.PendingHash = ""
	if err := localStore.SaveProjectWatchReconcileState(pending); err != nil {
		return fail("applied_state_write_failed")
	}
	result.Evidence.AppliedRevision = pending.AppliedRevision
	result.Evidence.Outcome = communication.ControlOutcomeCompleted
	return result, communication.AckStatusCompleted
}
func legacyWorkerSame(a, b noderuntime.WorkerInstance) bool {
	// SaveInstance owns UpdatedAt; every configuration/scheduling/identity field
	// remains exact, including CreatedAt. It is not a migration authorization bit.
	a.UpdatedAt = b.UpdatedAt
	return projectWatchSameJSON(projectWatchJSON(a), projectWatchJSON(b))
}
func projectWatchSameJSON(a, b []byte) bool {
	av, ea := pc.DecodeDeclarationEvidenceJSON(a)
	bv, eb := pc.DecodeDeclarationEvidenceJSON(b)
	return ea == nil && eb == nil && reflect.DeepEqual(av, bv)
}
func projectWatchDesiredApplied(runtimeStore noderuntime.Store, config Config, metadata []byte, safeKey string, owned map[string]bool, desired map[string]projects.DeclarationWatchRoot) bool {
	if len(desired) > 0 || len(owned) > 0 {
		root, ok := filesystemconnector.FindSafeRoot(config.Filesystem, safeKey)
		if !ok || !projectWatchSameJSON(root.Metadata, metadata) {
			return false
		}
	}
	for key := range owned {
		item, wanted := desired[key]
		instance, err := runtimeStore.LoadInstance(noderuntime.WatchedRootWorkerKey(key))
		if !wanted || !item.Enabled {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil || instance.Enabled {
				return false
			}
			continue
		}
		var rootConfig watchedroots.RootConfig
		if json.Unmarshal(item.ConfigJSON, &rootConfig) != nil {
			return false
		}
		if _, validateErr := watchedroots.ValidateRootConfig(rootConfig, config.Filesystem); validateErr != nil {
			return false
		}
		safeRoot, found := filesystemconnector.FindSafeRoot(config.Filesystem, safeKey)
		if !found || safeRoot.MaxFileBytes < watchedRootRequiredSafeRootMaxFileBytes(rootConfig) {
			return false
		}
		if err != nil || !instance.Enabled || instance.Kind != noderuntime.KindWatchedRoot || instance.LocalRootKey != item.LocalRootKey || instance.ConfigHash != item.ConfigHash || !projectWatchSameJSON(instance.ConfigJSON, item.ConfigJSON) {
			return false
		}
	}
	return true
}

func projectWatchJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return raw
}
