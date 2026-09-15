package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"sort"
	"time"

	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/filesystemconnector"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/projectcontracts"
)

type protectedFolderReconcileOutcome struct {
	Ack       backupcontracts.ProtectedFolderAck
	AckStatus string
	Code      string
	Summary   string
}

func buildProtectedFolderReconcileAck(config Config, state State, store Store, message communication.Message) communication.AckInput {
	ackInput := buildAckInput(state, message)
	payload, err := backupcontracts.DecodeReconcilePayload(message.PayloadJSON)
	if err != nil {
		return protectedFolderPreflightFailure(ackInput, communication.AckStatusFailedPermanent, "reconcile.invalid_request", "The protected-folder desired state is invalid.")
	}
	if message.NodeID != state.NodeID || payload.Evidence.TargetNode != state.NodeID {
		return protectedFolderPreflightFailure(ackInput, communication.AckStatusRejected, "reconcile.wrong_node", "The protected-folder desired state targets a different node.")
	}
	outcome := reconcileProtectedFolders(store, config, payload)
	resultJSON, err := json.Marshal(outcome.Ack)
	if err != nil || len(resultJSON) > communication.MaxProtectedFolderControlPayloadBytes {
		return protectedFolderPreflightFailure(ackInput, communication.AckStatusFailedRetryable, "reconcile.result_failed", "The node could not produce a bounded reconciliation result.")
	}
	ackInput.AckStatus = outcome.AckStatus
	ackInput.ResultJSON = resultJSON
	ackInput.ErrorJSON = objectJSON(map[string]any{})
	if outcome.Code != "" {
		ackInput.ErrorJSON = objectJSON(map[string]any{"code": outcome.Code, "summary": outcome.Summary})
	}
	return ackInput
}

func reconcileProtectedFolders(store Store, config Config, payload backupcontracts.ProtectedFolderReconcilePayload) protectedFolderReconcileOutcome {
	fresh, releaseConfig, lockErr := lockWatchedRootConfig(context.Background(), store)
	if lockErr != nil {
		return failedReconcileOutcome(payload, 0, payload.Evidence.ConfigHash, "reconcile.config_unavailable", "The node could not lock and reload its filesystem configuration.", communication.AckStatusFailedRetryable)
	}
	defer func() { _ = releaseConfig() }()
	config = fresh

	stateStore := watchedroots.NewStore(store.DataDir)
	localState, stateErr := stateStore.LoadProtectedFolderReconcileState()
	hadState := stateErr == nil
	if stateErr != nil && !errors.Is(stateErr, fs.ErrNotExist) {
		return failedReconcileOutcome(payload, 0, payload.Evidence.ConfigHash, "reconcile.state_unreadable", "The node could not read protected-folder reconciliation state.", communication.AckStatusFailedRetryable)
	}
	decision, err := communication.CompareRevision(localState.AppliedRevision, localState.ConfigHash, payload.Evidence)
	if err != nil {
		return failedReconcileOutcome(payload, localState.AppliedRevision, firstNonEmptyWatchedRootString(localState.ConfigHash, payload.Evidence.ConfigHash), "reconcile.invalid_revision", "The desired revision evidence is invalid.", communication.AckStatusFailedPermanent)
	}
	payloadHashVerified := false
	switch decision {
	case communication.RevisionDecisionStale:
		return protectedFolderReconcileOutcome{Ack: backupcontracts.ProtectedFolderAck{SchemaVersion: backupcontracts.ProtectedFolderControlSchemaVersion, Evidence: appliedEvidence(payload.Evidence.DesiredRevision, localState.AppliedRevision, localState.ConfigHash, communication.ControlOutcomeStale), ErrorCode: "reconcile.stale_revision", ErrorMessage: "A newer protected-folder revision is already active."}, AckStatus: communication.AckStatusCompleted}
	case communication.RevisionDecisionConflict:
		return failedReconcileOutcome(payload, localState.AppliedRevision, firstNonEmptyWatchedRootString(localState.ConfigHash, payload.Evidence.ConfigHash), "reconcile.revision_conflict", "The same protected-folder revision has a different config hash.", communication.AckStatusFailedPermanent)
	case communication.RevisionDecisionIdempotent:
		computedHash, hashErr := communication.ConfigHash(payload.Roots)
		if hashErr != nil || computedHash != payload.Evidence.ConfigHash {
			return failedReconcileOutcome(payload, localState.AppliedRevision, firstNonEmptyWatchedRootString(localState.ConfigHash, payload.Evidence.ConfigHash), "reconcile.payload_hash_mismatch", "The protected-folder payload does not match its config hash.", communication.AckStatusFailedPermanent)
		}
		payloadHashVerified = true
		if protectedFolderDesiredStateApplied(store, config, payload) {
			return protectedFolderReconcileOutcome{Ack: completedProtectedFolderAck(payload, localState.AppliedRevision, localState.ConfigHash), AckStatus: communication.AckStatusCompleted}
		}
	}

	if !payloadHashVerified {
		computedHash, hashErr := communication.ConfigHash(payload.Roots)
		if hashErr != nil || computedHash != payload.Evidence.ConfigHash {
			return failedReconcileOutcome(payload, localState.AppliedRevision, firstNonEmptyWatchedRootString(localState.ConfigHash, payload.Evidence.ConfigHash), "reconcile.payload_hash_mismatch", "The protected-folder payload does not match its config hash.", communication.AckStatusFailedPermanent)
		}
	}
	originalConfig := config
	runtimeStore := noderuntime.NewStore(store.DataDir)
	originalInstances, err := runtimeStore.LoadInstances()
	if err != nil {
		return failedReconcileOutcome(payload, localState.AppliedRevision, firstNonEmptyWatchedRootString(localState.ConfigHash, payload.Evidence.ConfigHash), "reconcile.runtime_state_unreadable", "The node could not read watched-root runtime state.", communication.AckStatusFailedRetryable)
	}
	candidateConfig := config
	rootConfigs := map[string]watchedroots.ValidatedRoot{}
	targets := map[string]string{}
	for _, desired := range payload.Roots {
		if !desired.Enabled {
			continue
		}
		var rootConfig watchedroots.RootConfig
		if err := communication.DecodeStrictJSONObject(desired.ConfigJSON, &rootConfig); err != nil {
			return failedReconcileOutcome(payload, localState.AppliedRevision, firstNonEmptyWatchedRootString(localState.ConfigHash, payload.Evidence.ConfigHash), "reconcile.root_config_invalid", "A protected-folder root config is invalid.", communication.AckStatusFailedPermanent)
		}
		rootConfig = watchedroots.NormalizeRootConfig(rootConfig)
		if rootConfig.RootKey != desired.RootKey || rootConfig.SafeRootKey != desired.SafeRootKey || watchedroots.ConfigHash(rootConfig) != desired.ConfigHash {
			return failedReconcileOutcome(payload, localState.AppliedRevision, firstNonEmptyWatchedRootString(localState.ConfigHash, payload.Evidence.ConfigHash), "reconcile.root_hash_mismatch", "A protected-folder root does not match its declared keys and config hash.", communication.AckStatusFailedPermanent)
		}
		canonicalTarget, err := canonicalDirectoryPath(desired.TargetPath)
		if err != nil {
			return failedReconcileOutcome(payload, localState.AppliedRevision, firstNonEmptyWatchedRootString(localState.ConfigHash, payload.Evidence.ConfigHash), "reconcile.target_unavailable", "A protected-folder target is not an available directory.", communication.AckStatusFailedRetryable)
		}
		if reason := forbiddenProtectedFolderReason(canonicalTarget, store.DataDir); reason != "" {
			return failedReconcileOutcome(payload, localState.AppliedRevision, firstNonEmptyWatchedRootString(localState.ConfigHash, payload.Evidence.ConfigHash), "reconcile.forbidden_target", reason, communication.AckStatusFailedPermanent)
		}
		for contractKey, otherTarget := range targets {
			if pathsOverlap(canonicalTarget, otherTarget) {
				return failedReconcileOutcome(payload, localState.AppliedRevision, firstNonEmptyWatchedRootString(localState.ConfigHash, payload.Evidence.ConfigHash), "reconcile.target_overlap", fmt.Sprintf("Protected folders %q and %q overlap.", desired.ContractKey, contractKey), communication.AckStatusFailedPermanent)
			}
		}
		for _, existingRoot := range candidateConfig.Filesystem.SafeRoots {
			if existingRoot.RootKey == desired.SafeRootKey {
				continue
			}
			existingPath, pathErr := filesystemconnector.CanonicalSafeRootPath(existingRoot)
			if pathErr == nil && pathsOverlap(canonicalTarget, existingPath) {
				return failedReconcileOutcome(payload, localState.AppliedRevision, firstNonEmptyWatchedRootString(localState.ConfigHash, payload.Evidence.ConfigHash), "reconcile.target_overlap", "A protected-folder target overlaps an existing filesystem safe root.", communication.AckStatusFailedPermanent)
			}
		}
		targets[desired.ContractKey] = canonicalTarget
		item := projectcontracts.ProjectWatchedRootItem{Key: backupcontracts.AreaKey(desired.ContractKey), BackendRootKey: desired.RootKey, SafeRootKey: desired.SafeRootKey, SourceKinds: []string{backupcontracts.SourceKindBoxBackupContract}, ConfigHash: desired.ConfigHash, ConfigJSON: desired.ConfigJSON, Metadata: map[string]any{"source": backupcontracts.MetadataSource, "contract_key": desired.ContractKey, "target_scope": backupcontracts.TargetScopeOwnerNodeAbsolute, "target_path": canonicalTarget, "safe_root_absolute_path": canonicalTarget}}
		safeRoot, managed, err := watchedRootBackupContractSafeRoot(rootConfig, item)
		if err != nil || !managed {
			return failedReconcileOutcome(payload, localState.AppliedRevision, firstNonEmptyWatchedRootString(localState.ConfigHash, payload.Evidence.ConfigHash), "reconcile.safe_root_invalid", "A protected-folder safe root could not be staged.", communication.AckStatusFailedPermanent)
		}
		if existing, exists := filesystemconnector.FindSafeRoot(candidateConfig.Filesystem, safeRoot.RootKey); exists {
			if !isManagedProtectedSafeRoot(existing) || managedSafeRootContractKey(existing) != desired.ContractKey {
				return failedReconcileOutcome(payload, localState.AppliedRevision, firstNonEmptyWatchedRootString(localState.ConfigHash, payload.Evidence.ConfigHash), "reconcile.safe_root_conflict", "A protected-folder safe-root key conflicts with existing node configuration.", communication.AckStatusFailedPermanent)
			}
		}
		candidateConfig.Filesystem = filesystemconnector.UpsertSafeRoot(candidateConfig.Filesystem, safeRoot)
		validated, err := watchedroots.ValidateRootConfig(rootConfig, candidateConfig.Filesystem)
		if err != nil {
			return failedReconcileOutcome(payload, localState.AppliedRevision, firstNonEmptyWatchedRootString(localState.ConfigHash, payload.Evidence.ConfigHash), "reconcile.root_validation_failed", "A protected-folder root failed node-side validation.", communication.AckStatusFailedPermanent)
		}
		rootConfigs[desired.ContractKey] = validated
	}

	finalInstances := map[string]noderuntime.WorkerInstance{}
	originalByKey := map[string]noderuntime.WorkerInstance{}
	for _, instance := range originalInstances {
		finalInstances[instance.WorkerKey] = instance
		originalByKey[instance.WorkerKey] = instance
	}
	desiredWorkerKeys := map[string]bool{}
	for contractKey, validated := range rootConfigs {
		configJSON, _ := json.Marshal(validated.Config)
		workerKey := noderuntime.WatchedRootWorkerKey(validated.Config.RootKey)
		desiredWorkerKeys[workerKey] = true
		instance := noderuntime.WorkerInstance{WorkerKey: workerKey, Kind: noderuntime.KindWatchedRoot, DisplayName: validated.Config.DisplayName, Enabled: true, IntervalSeconds: watchedRootWorkerIntervalSeconds(validated.Config), LeaseTimeoutSeconds: 120, ConfigHash: validated.ConfigHash, ConfigJSON: configJSON, CreatedAt: time.Now().UTC()}
		if existing, ok := originalByKey[workerKey]; ok {
			instance.CreatedAt = existing.CreatedAt
		}
		finalInstances[workerKey] = instance
		_ = contractKey
	}
	for key, instance := range finalInstances {
		if isManagedProtectedInstance(instance, candidateConfig.Filesystem) && !desiredWorkerKeys[key] {
			delete(finalInstances, key)
		}
	}
	referencedSafeRoots := map[string]bool{}
	for _, instance := range finalInstances {
		if instance.Kind != noderuntime.KindWatchedRoot {
			continue
		}
		var rootConfig watchedroots.RootConfig
		if json.Unmarshal(instance.ConfigJSON, &rootConfig) == nil {
			referencedSafeRoots[filesystemconnector.NormalizeRootKey(rootConfig.SafeRootKey)] = true
		}
	}
	for _, safeRoot := range candidateConfig.Filesystem.SafeRoots {
		if isManagedProtectedSafeRoot(safeRoot) && !referencedSafeRoots[safeRoot.RootKey] {
			candidateConfig.Filesystem = filesystemconnector.RemoveSafeRoot(candidateConfig.Filesystem, safeRoot.RootKey)
		}
	}
	if err := filesystemconnector.ValidateConfig(candidateConfig.Filesystem); err != nil {
		return failedReconcileOutcome(payload, localState.AppliedRevision, firstNonEmptyWatchedRootString(localState.ConfigHash, payload.Evidence.ConfigHash), "reconcile.config_invalid", "The staged filesystem configuration is invalid.", communication.AckStatusFailedPermanent)
	}

	changedInstances := map[string]bool{}
	rollback := func() {
		_ = store.SaveConfig(originalConfig)
		for key := range changedInstances {
			if original, exists := originalByKey[key]; exists {
				_ = runtimeStore.SaveInstance(original)
			} else {
				_ = runtimeStore.DeleteInstance(key)
			}
		}
		if hadState {
			_ = stateStore.SaveProtectedFolderReconcileState(localState)
		} else {
			_ = stateStore.DeleteProtectedFolderReconcileState()
		}
	}

	if err := store.SaveConfig(candidateConfig); err != nil {
		return failedReconcileOutcome(payload, localState.AppliedRevision, firstNonEmptyWatchedRootString(localState.ConfigHash, payload.Evidence.ConfigHash), "reconcile.config_write_failed", "The node could not commit protected-folder filesystem configuration.", communication.AckStatusFailedRetryable)
	}
	for _, instance := range finalInstances {
		if original, exists := originalByKey[instance.WorkerKey]; exists && reflect.DeepEqual(original, instance) {
			continue
		}
		changedInstances[instance.WorkerKey] = true
		if err := runtimeStore.SaveInstance(instance); err != nil {
			rollback()
			return failedReconcileOutcome(payload, localState.AppliedRevision, firstNonEmptyWatchedRootString(localState.ConfigHash, payload.Evidence.ConfigHash), "reconcile.worker_write_failed", "The node could not atomically commit protected-folder workers.", communication.AckStatusFailedRetryable)
		}
	}
	for key := range originalByKey {
		if _, keep := finalInstances[key]; !keep {
			changedInstances[key] = true
			if err := runtimeStore.DeleteInstance(key); err != nil {
				rollback()
				return failedReconcileOutcome(payload, localState.AppliedRevision, firstNonEmptyWatchedRootString(localState.ConfigHash, payload.Evidence.ConfigHash), "reconcile.worker_remove_failed", "The node could not atomically remove an obsolete protected-folder worker.", communication.AckStatusFailedRetryable)
			}
		}
	}
	rootKeys := make([]string, 0, len(rootConfigs))
	for _, validated := range rootConfigs {
		rootKeys = append(rootKeys, validated.Config.RootKey)
		_ = stateStore.EnsureRoot(validated.Config.RootKey)
	}
	sort.Strings(rootKeys)
	if err := stateStore.SaveProtectedFolderReconcileState(watchedroots.ProtectedFolderReconcileState{AppliedRevision: payload.Evidence.DesiredRevision, ConfigHash: payload.Evidence.ConfigHash, RootKeys: rootKeys}); err != nil {
		rollback()
		return failedReconcileOutcome(payload, localState.AppliedRevision, firstNonEmptyWatchedRootString(localState.ConfigHash, payload.Evidence.ConfigHash), "reconcile.state_write_failed", "The node could not commit protected-folder reconciliation evidence.", communication.AckStatusFailedRetryable)
	}
	return protectedFolderReconcileOutcome{Ack: completedProtectedFolderAck(payload, payload.Evidence.DesiredRevision, payload.Evidence.ConfigHash), AckStatus: communication.AckStatusCompleted}
}

func protectedFolderDesiredStateApplied(store Store, config Config, payload backupcontracts.ProtectedFolderReconcilePayload) bool {
	runtimeStore := noderuntime.NewStore(store.DataDir)
	desiredSafeRoots := map[string]bool{}
	for _, desired := range payload.Roots {
		if !desired.Enabled {
			continue
		}
		var rootConfig watchedroots.RootConfig
		if communication.DecodeStrictJSONObject(desired.ConfigJSON, &rootConfig) != nil {
			return false
		}
		rootConfig = watchedroots.NormalizeRootConfig(rootConfig)
		if rootConfig.RootKey != desired.RootKey || rootConfig.SafeRootKey != desired.SafeRootKey || watchedroots.ConfigHash(rootConfig) != desired.ConfigHash {
			return false
		}
		safeRoot, ok := filesystemconnector.FindSafeRoot(config.Filesystem, desired.SafeRootKey)
		if !ok || !safeRoot.PrivateBackupOnly || !isManagedProtectedSafeRoot(safeRoot) || managedSafeRootContractKey(safeRoot) != desired.ContractKey {
			return false
		}
		absolutePath, err := filesystemconnector.CanonicalSafeRootPath(safeRoot)
		if err != nil || filepath.Clean(absolutePath) != filepath.Clean(desired.TargetPath) || safeRoot.MaxFileBytes < watchedRootRequiredSafeRootMaxFileBytes(rootConfig) {
			return false
		}
		if _, err := watchedroots.ValidateRootConfig(rootConfig, config.Filesystem); err != nil {
			return false
		}
		instance, err := runtimeStore.LoadInstance(noderuntime.WatchedRootWorkerKey(rootConfig.RootKey))
		if err != nil || !instance.Enabled || instance.Kind != noderuntime.KindWatchedRoot || instance.ConfigHash != desired.ConfigHash {
			return false
		}
		var instanceConfig watchedroots.RootConfig
		if json.Unmarshal(instance.ConfigJSON, &instanceConfig) != nil || watchedroots.ConfigHash(watchedroots.NormalizeRootConfig(instanceConfig)) != desired.ConfigHash {
			return false
		}
		desiredSafeRoots[filesystemconnector.NormalizeRootKey(desired.SafeRootKey)] = true
	}
	for _, safeRoot := range config.Filesystem.SafeRoots {
		if isManagedProtectedSafeRoot(safeRoot) && !desiredSafeRoots[filesystemconnector.NormalizeRootKey(safeRoot.RootKey)] {
			return false
		}
	}
	return true
}

func completedProtectedFolderAck(payload backupcontracts.ProtectedFolderReconcilePayload, appliedRevision int64, configHash string) backupcontracts.ProtectedFolderAck {
	ack := backupcontracts.ProtectedFolderAck{SchemaVersion: backupcontracts.ProtectedFolderControlSchemaVersion, Evidence: appliedEvidence(payload.Evidence.DesiredRevision, appliedRevision, configHash, communication.ControlOutcomeCompleted)}
	for _, root := range payload.Roots {
		if root.Enabled {
			ack.Roots = append(ack.Roots, backupcontracts.ProtectedFolderRootAck{ContractKey: root.ContractKey, ConfigHash: root.ConfigHash, Applied: true})
		}
	}
	return ack
}

func failedReconcileOutcome(payload backupcontracts.ProtectedFolderReconcilePayload, appliedRevision int64, configHash, code, summary, ackStatus string) protectedFolderReconcileOutcome {
	return protectedFolderReconcileOutcome{Ack: backupcontracts.ProtectedFolderAck{SchemaVersion: backupcontracts.ProtectedFolderControlSchemaVersion, Evidence: appliedEvidence(payload.Evidence.DesiredRevision, appliedRevision, configHash, communication.ControlOutcomeFailed), ErrorCode: code, ErrorMessage: summary}, AckStatus: ackStatus, Code: code, Summary: summary}
}

func appliedEvidence(desiredRevision, appliedRevision int64, configHash, outcome string) communication.AppliedStateEvidence {
	return communication.AppliedStateEvidence{SchemaVersion: communication.ControlEvidenceSchemaVersion, DesiredRevision: desiredRevision, AppliedRevision: appliedRevision, ConfigHash: configHash, Outcome: outcome}
}

func isManagedProtectedSafeRoot(root filesystemconnector.SafeRoot) bool {
	if !root.PrivateBackupOnly {
		return false
	}
	var metadata map[string]any
	if json.Unmarshal(root.Metadata, &metadata) != nil {
		return false
	}
	source, _ := metadata["source"].(string)
	return source == backupcontracts.MetadataSource
}

func managedSafeRootContractKey(root filesystemconnector.SafeRoot) string {
	var metadata map[string]any
	if json.Unmarshal(root.Metadata, &metadata) != nil {
		return ""
	}
	key, _ := metadata["contract_key"].(string)
	return key
}

func isManagedProtectedInstance(instance noderuntime.WorkerInstance, filesystem filesystemconnector.Config) bool {
	if instance.Kind != noderuntime.KindWatchedRoot {
		return false
	}
	var rootConfig watchedroots.RootConfig
	if json.Unmarshal(instance.ConfigJSON, &rootConfig) != nil {
		return false
	}
	safeRoot, ok := filesystemconnector.FindSafeRoot(filesystem, rootConfig.SafeRootKey)
	return ok && isManagedProtectedSafeRoot(safeRoot)
}
