package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/box"
	"loom.local/loom/internal/enrollmentflow"
	"loom.local/loom/internal/filesystemconnector"
	"loom.local/loom/internal/nodeagent"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	agentwatchedroots "loom.local/loom/internal/nodeagent/watchedroots"
)

const (
	safeRootModeReadOnly  = "read_only"
	safeRootModeReadWrite = "read_write"

	boxSafeRootMaxFileBytes = int64(60 * 1024 * 1024 * 1024)
)

func applyNonMainPlan(plan SetupPlan, input ApplyInput, result ApplyResult) (ApplyResult, error) {
	if blocked := validateNonMainApplyPlan(plan); len(blocked) > 0 {
		result.Blocked = blocked
		result.Manifest = nonMainManifestFromPlan(plan)
		return result, nil
	}
	if !input.DryRun && !input.Yes {
		result.Refused = true
		if input.NoInteractive {
			result.Refusal = "setup apply is non-interactive; pass --yes to configure the local non-main node"
		} else {
			result.Refusal = "setup apply requires --yes before mutating local setup state"
		}
		result.Manifest = nonMainManifestFromPlan(plan)
		return result, nil
	}

	manifest := nonMainManifestFromPlan(plan)
	pathChanges, err := applyNonMainPaths(plan, input.DryRun)
	result.Changed = append(result.Changed, pathChanges.changed...)
	result.Skipped = append(result.Skipped, pathChanges.skipped...)
	result.Blocked = append(result.Blocked, pathChanges.blocked...)
	if err != nil {
		return result, err
	}
	if len(result.Blocked) > 0 {
		result.Manifest = manifest
		return result, nil
	}

	agentChanges, err := applyNodeAgentState(plan, input.DryRun)
	result.Changed = append(result.Changed, agentChanges.changed...)
	result.Skipped = append(result.Skipped, agentChanges.skipped...)
	result.Blocked = append(result.Blocked, agentChanges.blocked...)
	if err != nil {
		return result, err
	}
	if len(result.Blocked) > 0 {
		result.Manifest = manifest
		return result, nil
	}

	serviceChanges, err := applyLaunchAgent(plan, input.DryRun, input.LaunchdRunner)
	result.Changed = append(result.Changed, serviceChanges.changed...)
	result.Skipped = append(result.Skipped, serviceChanges.skipped...)
	result.Blocked = append(result.Blocked, serviceChanges.blocked...)
	if err != nil {
		return result, err
	}
	if len(result.Blocked) > 0 {
		result.Manifest = manifest
		return result, nil
	}

	if plan.Spec.RunEnrollment {
		enrollmentResult, enrollmentChange := runNonMainEnrollmentFlow(plan, input)
		result.Enrollment = &enrollmentResult
		manifest = ManifestWithEnrollmentResult(manifest, enrollmentResult)
		appendApplyChange(&result, enrollmentChange)
	}

	manifest.LastStatus = nonMainManifestStatus(manifest)
	manifestChange, err := writeManifestChange(plan.Paths.ManifestPath, manifest, input.DryRun)
	if err != nil {
		return result, err
	}
	appendApplyChange(&result, manifestChange)
	result.Manifest = manifest

	statusInput := StatusInput{
		ManifestPath:  plan.Paths.ManifestPath,
		Spec:          plan.Spec,
		Facts:         input.Facts,
		Now:           input.Now,
		CollectFacts:  input.CollectFacts,
		LaunchdRunner: input.LaunchdRunner,
	}
	status, statusErr := Status(statusInput)
	if statusErr == nil {
		result.Status = status
		doctor, doctorErr := Doctor(DoctorInput{StatusInput: statusInput})
		if doctorErr == nil {
			result.Doctor = doctor
		}
	}
	return result, statusErr
}

func validateNonMainApplyPlan(plan SetupPlan) []ApplyChange {
	blocked := []ApplyChange{}
	if plan.Spec.NodeKind == "main" {
		return []ApplyChange{applyBlocked("validate_non_main_node", "preflight", "non-main apply received a main node plan")}
	}
	if !plan.Spec.EnableNodeAgent {
		blocked = append(blocked, applyBlocked("validate_node_agent_enabled", "preflight", "non-main setup requires enable_node_agent=true"))
	}
	if plan.Spec.RunEnrollment && plan.Spec.SkipEnroll {
		blocked = append(blocked, applyBlocked("validate_enrollment_mode", "enrollment", "setup cannot run enrollment while skip_enroll=true"))
	}
	if strings.TrimSpace(plan.Spec.MainURL) == "" {
		blocked = append(blocked, applyBlocked("validate_main_url", "preflight", "non-main setup requires main_url"))
	}
	if plan.Spec.EnableBox {
		if strings.TrimSpace(plan.Paths.BoxPath) == "" {
			blocked = append(blocked, applyBlocked("validate_box_path", "box", "workspace setup requires a Box path"))
		}
		if plan.Spec.BoxProfile == box.ProfileMain {
			blocked = append(blocked, applyBlocked("validate_box_profile", "box", "non-main setup must not use the main Box profile"))
		}
	}
	if strings.TrimSpace(plan.Paths.NodeAgentConfigPath) == "" || strings.TrimSpace(plan.Paths.NodeAgentStatePath) == "" || strings.TrimSpace(plan.Paths.NodeAgentDataDir) == "" {
		blocked = append(blocked, applyBlocked("validate_node_agent_paths", "node_agent", "non-main setup requires node-agent config, state, and data paths"))
	}
	for _, diagnostic := range plan.Diagnostics {
		if diagnostic.Severity == DiagnosticError || diagnostic.Severity == DiagnosticBlocking {
			blocked = append(blocked, ApplyChange{
				ID:       "diagnostic_" + diagnostic.Code,
				Category: "preflight",
				Status:   ApplyStatusBlocked,
				Path:     diagnostic.Path,
				Message:  diagnostic.Message,
			})
		}
	}
	return blocked
}

func nonMainManifestFromPlan(plan SetupPlan) InstallManifest {
	manifest := ManifestFromPlan(plan)
	manifest.Enrollment.Status = "not_started"
	if plan.Spec.SkipEnroll {
		manifest.Enrollment.Status = "skipped"
	}
	manifest.Credential.Configured = false
	return manifest
}

func runNonMainEnrollmentFlow(plan SetupPlan, input ApplyInput) (enrollmentflow.Result, ApplyChange) {
	if input.DryRun {
		return enrollmentflow.Result{Status: enrollmentflow.StateNotStarted, Redacted: true}, ApplyChange{
			ID:       "run_enrollment_flow",
			Category: "enrollment",
			Status:   ApplyStatusWouldChange,
			Message:  "would create token, submit request, approve, import credential, and heartbeat",
		}
	}
	mainRunner := input.EnrollmentMainRunner
	if mainRunner == nil {
		mainRunner = defaultNonMainEnrollmentMainRunner(plan)
	}
	targetRunner := input.EnrollmentTargetRunner
	if targetRunner == nil {
		targetRunner = enrollmentflow.NewLocalTargetRunner(
			plan.Paths.NodeAgentConfigPath,
			plan.Paths.NodeAgentStatePath,
			plan.Paths.NodeAgentDataDir,
			"setup.enroll."+plan.Spec.NodeKey,
		)
	}
	flowSpec := enrollmentflow.Spec{
		NodeKey:         plan.Spec.NodeKey,
		DisplayName:     plan.Spec.DisplayName,
		NodeKind:        plan.Spec.NodeKind,
		NodeRole:        plan.Spec.NodeRole,
		RuntimeClass:    plan.Spec.RuntimeClass,
		TokenTTLSeconds: plan.Spec.EnrollmentTTLSeconds,
		Approve:         plan.Spec.ApproveEnrollment,
		VerifyHeartbeat: plan.Spec.VerifyHeartbeat,
		VerifyMain:      plan.Spec.VerifyHeartbeat,
		Resume:          input.Resume,
	}
	if input.Resume {
		flowSpec.State = localEnrollmentResumeState(plan)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	result, err := enrollmentflow.Run(ctx, flowSpec, mainRunner, targetRunner)
	change := ApplyChange{
		ID:       "run_enrollment_flow",
		Category: "enrollment",
		Status:   ApplyStatusChanged,
		Message:  result.Status,
		Metadata: map[string]any{
			"status":                result.Status,
			"enrollment_request_id": result.EnrollmentRequestID,
			"node_id":               result.NodeID,
			"node_credential_id":    result.NodeCredentialID,
			"verified_on_main":      result.VerifiedOnMain,
			"failure_code":          result.FailureCode,
		},
	}
	if err != nil || result.Status == enrollmentflow.StateFailed || result.Status == enrollmentflow.StateUnrecoverable {
		if nonMainEnrollmentFailureIsNonBlocking(result) {
			change.Status = ApplyStatusSkipped
			change.Message = firstNonEmpty(result.FailureMessage, "main verification failed after local credential import; existing credential was preserved")
		} else {
			change.Status = ApplyStatusBlocked
			change.Message = result.FailureMessage
			if strings.TrimSpace(change.Message) == "" && err != nil {
				change.Message = err.Error()
			}
		}
	}
	return result, change
}

func defaultNonMainEnrollmentMainRunner(plan SetupPlan) enrollmentflow.MainRunner {
	correlationID := "setup.enroll." + plan.Spec.NodeKey
	if strings.TrimSpace(plan.Spec.MainURL) != "" {
		return enrollmentflow.NewHTTPMainRunner(plan.Spec.MainURL, correlationID)
	}
	return enrollmentflow.NewLocalMainRunner(plan.Spec.SocketPath, correlationID)
}

func localEnrollmentResumeState(plan SetupPlan) enrollmentflow.ResumeState {
	store := nodeagent.Store{
		ConfigPath: plan.Paths.NodeAgentConfigPath,
		StatePath:  plan.Paths.NodeAgentStatePath,
		DataDir:    plan.Paths.NodeAgentDataDir,
	}
	state, err := store.LoadState()
	if err != nil {
		return enrollmentflow.ResumeState{Status: enrollmentflow.StateNotStarted}
	}
	resume := enrollmentflow.ResumeState{
		Status:              enrollmentflow.StateNotStarted,
		EnrollmentRequestID: state.EnrollmentRequestID,
		NodeID:              state.NodeID,
		NodeCredentialID:    state.NodeCredentialID,
		CredentialImported:  strings.TrimSpace(state.NodeID) != "" && strings.TrimSpace(state.NodeCredentialID) != "" && strings.TrimSpace(state.CredentialToken) != "",
		CredentialTokenHeld: strings.TrimSpace(state.CredentialToken) != "",
	}
	switch {
	case state.LastHeartbeat != nil:
		resume.Status = enrollmentflow.StateHeartbeatVerified
		resume.HeartbeatID = state.LastHeartbeat.NodeHeartbeatID
	case resume.CredentialImported:
		resume.Status = enrollmentflow.StateCredentialImported
	case resume.EnrollmentRequestID != "":
		resume.Status = enrollmentflow.StateRequestSubmitted
	}
	return resume
}

func ManifestWithEnrollmentResult(manifest InstallManifest, result enrollmentflow.Result) InstallManifest {
	manifest.Enrollment.Status = setupEnrollmentStatus(result)
	manifest.Enrollment.EnrollmentRequestID = result.EnrollmentRequestID
	manifest.Enrollment.FailureCode = result.FailureCode
	manifest.Enrollment.FailureMessage = result.FailureMessage
	manifest.Enrollment.PresenceState = result.PresenceState
	manifest.Enrollment.VerifiedOnMain = result.VerifiedOnMain
	if strings.TrimSpace(result.NodeID) != "" {
		manifest.NodeID = result.NodeID
	}
	if strings.TrimSpace(result.NodeCredentialID) != "" {
		manifest.Credential.NodeCredentialID = result.NodeCredentialID
	}
	if strings.TrimSpace(result.CredentialHint) != "" {
		manifest.Credential.CredentialHint = result.CredentialHint
	}
	manifest.Credential.Configured = result.Status == enrollmentflow.StateCredentialImported ||
		result.Status == enrollmentflow.StateHeartbeatVerified ||
		result.Status == enrollmentflow.StateVerifiedOnMain ||
		manifest.Credential.Configured ||
		nonMainEnrollmentFailureIsNonBlocking(result)
	if !result.HeartbeatAt.IsZero() {
		heartbeatAt := result.HeartbeatAt
		manifest.LastStatus.LastHeartbeatAt = &heartbeatAt
	}
	return manifest
}

func setupEnrollmentStatus(result enrollmentflow.Result) string {
	switch result.Status {
	case enrollmentflow.StateRequestSubmitted:
		return "pending"
	case enrollmentflow.StateApproved, enrollmentflow.StateCredentialImported, enrollmentflow.StateHeartbeatVerified, enrollmentflow.StateVerifiedOnMain:
		return "approved"
	case enrollmentflow.StateFailed, enrollmentflow.StateUnrecoverable:
		if nonMainEnrollmentFailureIsNonBlocking(result) {
			return "approved"
		}
		return "failed"
	case enrollmentflow.StateNotStarted:
		return "not_started"
	default:
		return result.Status
	}
}

func nonMainEnrollmentFailureIsNonBlocking(result enrollmentflow.Result) bool {
	return result.Status == enrollmentflow.StateFailed &&
		result.FailureCode == enrollmentflow.FailureMainVerification &&
		strings.TrimSpace(result.NodeID) != "" &&
		strings.TrimSpace(result.NodeCredentialID) != "" &&
		strings.TrimSpace(result.HeartbeatID) != ""
}

func applyNonMainPaths(plan SetupPlan, dryRun bool) (applyChanges, error) {
	out := applyChanges{}
	runtimeState, err := resolvePlanBoxRuntimeState(plan)
	if err != nil {
		out.blocked = append(out.blocked, ApplyChange{ID: "resolve_box_runtime_state", Category: "box", Status: ApplyStatusBlocked, Path: plan.Paths.BoxPath, Message: err.Error()})
		return out, nil
	}
	dirs := []applyDir{
		{id: "ensure_config_dir", category: "paths", path: plan.Paths.ConfigDir, mode: 0o700},
		{id: "ensure_data_dir", category: "paths", path: plan.Paths.DataDir, mode: 0o700},
		{id: "ensure_state_dir", category: "paths", path: plan.Paths.StateDir, mode: 0o700},
		{id: "ensure_log_dir", category: "paths", path: plan.Paths.LogDir, mode: 0o700},
		{id: "ensure_node_agent_config_dir", category: "node_agent", path: filepath.Dir(plan.Paths.NodeAgentConfigPath), mode: 0o700},
		{id: "ensure_node_agent_state_dir", category: "node_agent", path: filepath.Dir(plan.Paths.NodeAgentStatePath), mode: 0o700},
		{id: "ensure_node_agent_data_dir", category: "node_agent", path: plan.Paths.NodeAgentDataDir, mode: 0o700},
	}
	if plan.Spec.EnableBox && runtimeState.Source != "legacy" {
		dirs = append(dirs, applyDir{id: "ensure_box_state_root", category: "box", path: plan.Paths.BoxStateRoot, mode: 0o700})
	} else if plan.Spec.EnableBox {
		out.skipped = append(out.skipped, ApplyChange{ID: "ensure_box_state_root", Category: "box", Status: ApplyStatusSkipped, Path: plan.Paths.BoxStateRoot, Message: "legacy Box runtime state remains the effective read/write root until explicit cutover"})
	}
	for _, dir := range dirs {
		change, err := ensureDirChange(dir.id, dir.category, dir.path, dir.mode, dryRun)
		if err != nil {
			return out, err
		}
		appendToApplyChanges(&out, change)
	}
	if plan.Spec.EnableBox {
		boxResult, err := box.Init(box.InitInput{
			Resolved: box.Resolved{
				RootPath:         plan.Paths.BoxPath,
				Profile:          firstNonEmpty(plan.Spec.BoxProfile, box.ProfileWorkspace),
				OwnerNode:        plan.Spec.NodeKey,
				NodeRole:         plan.Spec.NodeRole,
				RuntimeStateRoot: plan.Paths.BoxStateRoot,
				LegacyStateRoot:  plan.Paths.LegacyBoxStateRoot,
			},
			DryRun: dryRun,
		})
		if err != nil {
			out.blocked = append(out.blocked, ApplyChange{
				ID:       "init_box",
				Category: "box",
				Status:   ApplyStatusBlocked,
				Path:     plan.Paths.BoxPath,
				Message:  err.Error(),
			})
			return out, nil
		}
		for _, path := range boxResult.CreatedDirs {
			out.changed = append(out.changed, ApplyChange{ID: "init_box_dir", Category: "box", Status: ApplyStatusChanged, Path: path})
		}
		for _, path := range boxResult.CreatedFiles {
			out.changed = append(out.changed, ApplyChange{ID: "init_box_file", Category: "box", Status: ApplyStatusChanged, Path: path})
		}
		for _, path := range boxResult.PlannedDirs {
			out.changed = append(out.changed, ApplyChange{ID: "init_box_dir", Category: "box", Status: ApplyStatusWouldChange, Path: path})
		}
		for _, path := range boxResult.PlannedFiles {
			out.changed = append(out.changed, ApplyChange{ID: "init_box_file", Category: "box", Status: ApplyStatusWouldChange, Path: path})
		}
		if len(boxResult.CreatedDirs)+len(boxResult.CreatedFiles)+len(boxResult.PlannedDirs)+len(boxResult.PlannedFiles) == 0 {
			out.skipped = append(out.skipped, ApplyChange{ID: "init_box", Category: "box", Status: ApplyStatusAlreadySatisfied, Path: plan.Paths.BoxPath})
		}
	}
	return out, nil
}

func applyNodeAgentState(plan SetupPlan, dryRun bool) (applyChanges, error) {
	out := applyChanges{}
	store := nodeagent.Store{
		ConfigPath: plan.Paths.NodeAgentConfigPath,
		StatePath:  plan.Paths.NodeAgentStatePath,
		DataDir:    plan.Paths.NodeAgentDataDir,
	}
	agentConfig, err := nodeAgentConfigFromPlan(plan)
	if err != nil {
		out.blocked = append(out.blocked, ApplyChange{ID: "write_node_agent_config", Category: "node_agent", Status: ApplyStatusBlocked, Path: store.ConfigPath, Message: err.Error()})
		return out, nil
	}
	configChange, err := writeNodeAgentConfigChange(store, agentConfig, dryRun)
	if err != nil {
		return out, err
	}
	appendToApplyChanges(&out, configChange)

	stateChange, err := ensureNodeAgentStateFile(store, dryRun)
	if err != nil {
		return out, err
	}
	appendToApplyChanges(&out, stateChange)

	runtimeChange, err := ensureNodeAgentRuntimeDefaults(store, agentConfig, dryRun)
	if err != nil {
		return out, err
	}
	appendToApplyChanges(&out, runtimeChange)

	if plan.Spec.EnableWatchedRoots && plan.Spec.EnableBox {
		watchChanges, err := configureBoxWatchedRoots(plan, store, agentConfig, dryRun)
		if err != nil {
			return out, err
		}
		out.changed = append(out.changed, watchChanges.changed...)
		out.skipped = append(out.skipped, watchChanges.skipped...)
		out.blocked = append(out.blocked, watchChanges.blocked...)
	}
	return out, nil
}

func nodeAgentConfigFromPlan(plan SetupPlan) (nodeagent.Config, error) {
	fsConfig, err := filesystemConfigFromPlan(plan)
	if err != nil {
		return nodeagent.Config{}, err
	}
	return nodeagent.Config{
		MainURL:                  plan.Spec.MainURL,
		NodeKey:                  plan.Spec.NodeKey,
		DisplayName:              plan.Spec.DisplayName,
		NodeKind:                 plan.Spec.NodeKind,
		NodeRole:                 plan.Spec.NodeRole,
		RuntimeClass:             plan.Spec.RuntimeClass,
		BoxRootPath:              plan.Paths.BoxPath,
		BoxStateRoot:             plan.Paths.BoxStateRoot,
		HeartbeatIntervalSeconds: 30,
		PollIntervalSeconds:      30,
		Filesystem:               fsConfig,
	}, nil
}

func filesystemConfigFromPlan(plan SetupPlan) (filesystemconnector.Config, error) {
	config := filesystemconnector.Config{}
	if plan.Spec.EnableBox && strings.TrimSpace(plan.Paths.BoxPath) != "" {
		config = upsertSafeRootPreserveCeiling(config, filesystemconnector.SafeRoot{
			RootKey:       "loom_box",
			DisplayName:   "loom-box",
			AbsolutePath:  plan.Paths.BoxPath,
			AllowList:     true,
			AllowMetadata: true,
			AllowIngest:   true,
			MaxFileBytes:  boxSafeRootMaxFileBytes,
		})
	}
	for _, spec := range plan.Spec.SafeRoots {
		root, err := safeRootFromSpec(spec)
		if err != nil {
			return filesystemconnector.Config{}, err
		}
		config = upsertSafeRootPreserveCeiling(config, root)
	}
	config = filesystemconnector.NormalizeConfig(config)
	if err := filesystemconnector.ValidateConfig(config); err != nil {
		return filesystemconnector.Config{}, err
	}
	return config, nil
}

func safeRootFromSpec(spec SafeRootSpec) (filesystemconnector.SafeRoot, error) {
	mode := strings.TrimSpace(spec.Mode)
	if mode == "" {
		mode = safeRootModeReadOnly
	}
	root := filesystemconnector.DefaultSafeRoot(spec.Name, spec.Path)
	root.AllowList = true
	root.AllowMetadata = true
	switch mode {
	case safeRootModeReadOnly:
		root.AllowIngest = false
	case safeRootModeReadWrite:
		root.AllowIngest = true
	default:
		return filesystemconnector.SafeRoot{}, fmt.Errorf("unsupported safe root mode %q for %s", mode, spec.Name)
	}
	return root, nil
}

func upsertSafeRootPreserveCeiling(config filesystemconnector.Config, root filesystemconnector.SafeRoot) filesystemconnector.Config {
	root = filesystemconnector.NormalizeSafeRoot(root)
	if existing, ok := filesystemconnector.FindSafeRoot(config, root.RootKey); ok && existing.MaxFileBytes > root.MaxFileBytes {
		root.MaxFileBytes = existing.MaxFileBytes
	}
	return filesystemconnector.UpsertSafeRoot(config, root)
}

func writeNodeAgentConfigChange(store nodeagent.Store, desired nodeagent.Config, dryRun bool) (ApplyChange, error) {
	existing, err := store.LoadConfig()
	if err == nil {
		desired = mergeExistingNodeAgentSafeRoots(existing, desired)
		desired.BackupTransport = existing.BackupTransport
		desired.CreatedAt = existing.CreatedAt
		if nodeAgentConfigEquivalent(existing, desired) {
			return ApplyChange{ID: "write_node_agent_config", Category: "node_agent", Status: ApplyStatusAlreadySatisfied, Path: store.ConfigPath}, nil
		}
	} else if !strings.Contains(err.Error(), "not initialized") && !os.IsNotExist(err) {
		return ApplyChange{}, err
	}
	if dryRun {
		return ApplyChange{ID: "write_node_agent_config", Category: "node_agent", Status: ApplyStatusWouldChange, Path: store.ConfigPath}, nil
	}
	if err := store.SaveConfig(desired); err != nil {
		return ApplyChange{}, err
	}
	return ApplyChange{ID: "write_node_agent_config", Category: "node_agent", Status: ApplyStatusChanged, Path: store.ConfigPath}, nil
}

func mergeExistingNodeAgentSafeRoots(existing nodeagent.Config, desired nodeagent.Config) nodeagent.Config {
	for _, desiredRoot := range desired.Filesystem.SafeRoots {
		existingRoot, ok := filesystemconnector.FindSafeRoot(existing.Filesystem, desiredRoot.RootKey)
		if !ok || existingRoot.MaxFileBytes <= desiredRoot.MaxFileBytes {
			continue
		}
		desiredRoot.MaxFileBytes = existingRoot.MaxFileBytes
		desired.Filesystem = filesystemconnector.UpsertSafeRoot(desired.Filesystem, desiredRoot)
	}
	for _, existingRoot := range existing.Filesystem.SafeRoots {
		if _, declared := filesystemconnector.FindSafeRoot(desired.Filesystem, existingRoot.RootKey); declared {
			continue
		}
		if !existingRoot.PrivateBackupOnly || !managedProtectedFolderSafeRoot(existingRoot) {
			continue
		}
		desired.Filesystem = filesystemconnector.UpsertSafeRoot(desired.Filesystem, existingRoot)
	}
	return desired
}

func managedProtectedFolderSafeRoot(root filesystemconnector.SafeRoot) bool {
	var metadata map[string]any
	if json.Unmarshal(root.Metadata, &metadata) != nil {
		return false
	}
	source, _ := metadata["source"].(string)
	contractKey, _ := metadata["contract_key"].(string)
	return source == backupcontracts.MetadataSource && strings.TrimSpace(contractKey) != ""
}

func nodeAgentConfigEquivalent(existing nodeagent.Config, desired nodeagent.Config) bool {
	existing.CreatedAt = time.Time{}
	existing.UpdatedAt = time.Time{}
	desired.CreatedAt = time.Time{}
	desired.UpdatedAt = time.Time{}
	existingJSON, _ := json.Marshal(existing)
	desiredJSON, _ := json.Marshal(desired)
	return string(existingJSON) == string(desiredJSON)
}

func ensureNodeAgentStateFile(store nodeagent.Store, dryRun bool) (ApplyChange, error) {
	if _, err := os.Stat(store.StatePath); err == nil {
		return ApplyChange{ID: "initialize_node_agent_state", Category: "node_agent", Status: ApplyStatusAlreadySatisfied, Path: store.StatePath}, nil
	} else if err != nil && !os.IsNotExist(err) {
		return ApplyChange{}, err
	}
	if dryRun {
		return ApplyChange{ID: "initialize_node_agent_state", Category: "node_agent", Status: ApplyStatusWouldChange, Path: store.StatePath}, nil
	}
	if err := store.SaveState(nodeagent.State{}); err != nil {
		return ApplyChange{}, err
	}
	return ApplyChange{ID: "initialize_node_agent_state", Category: "node_agent", Status: ApplyStatusChanged, Path: store.StatePath}, nil
}

func ensureNodeAgentRuntimeDefaults(store nodeagent.Store, config nodeagent.Config, dryRun bool) (ApplyChange, error) {
	runtimeStore := noderuntime.NewStore(store.DataDir)
	laneConfigJSON, err := json.Marshal(map[string]string{
		"box_root_path":      config.BoxRootPath,
		"runtime_state_root": config.BoxStateRoot,
	})
	if err != nil {
		return ApplyChange{}, err
	}
	laneConfigHash := rawConfigHash(laneConfigJSON)
	missing := []string{}
	for _, instance := range noderuntime.DefaultInstances(noderuntime.DefaultInstanceInput{
		HeartbeatIntervalSeconds: config.HeartbeatIntervalSeconds,
		PollIntervalSeconds:      config.PollIntervalSeconds,
	}) {
		existing, err := runtimeStore.LoadInstance(instance.WorkerKey)
		if os.IsNotExist(err) {
			missing = append(missing, instance.WorkerKey)
		} else if err != nil && !os.IsNotExist(err) {
			missing = append(missing, instance.WorkerKey)
		} else if instance.WorkerKey == noderuntime.WorkerKeyLaneHousekeeping && config.BoxRootPath != "" && existing.ConfigHash != laneConfigHash {
			missing = append(missing, instance.WorkerKey)
		}
	}
	if len(missing) == 0 {
		return ApplyChange{ID: "initialize_node_agent_runtime", Category: "node_agent", Status: ApplyStatusAlreadySatisfied, Path: store.DataDir}, nil
	}
	if dryRun {
		return ApplyChange{ID: "initialize_node_agent_runtime", Category: "node_agent", Status: ApplyStatusWouldChange, Path: store.DataDir, Metadata: map[string]any{"missing_workers": missing}}, nil
	}
	if err := runtimeStore.EnsureDefaultInstances(noderuntime.DefaultInstanceInput{
		HeartbeatIntervalSeconds: config.HeartbeatIntervalSeconds,
		PollIntervalSeconds:      config.PollIntervalSeconds,
	}); err != nil {
		return ApplyChange{}, err
	}
	if config.BoxRootPath != "" {
		instance, err := runtimeStore.LoadInstance(noderuntime.WorkerKeyLaneHousekeeping)
		if err != nil {
			return ApplyChange{}, err
		}
		instance.ConfigJSON = laneConfigJSON
		instance.ConfigHash = laneConfigHash
		instance.UpdatedAt = time.Now().UTC()
		if err := runtimeStore.SaveInstance(instance); err != nil {
			return ApplyChange{}, err
		}
	}
	return ApplyChange{ID: "initialize_node_agent_runtime", Category: "node_agent", Status: ApplyStatusChanged, Path: store.DataDir, Metadata: map[string]any{"missing_workers": missing}}, nil
}

func configureBoxWatchedRoots(plan SetupPlan, store nodeagent.Store, config nodeagent.Config, dryRun bool) (applyChanges, error) {
	out := applyChanges{}
	if dryRun {
		out.changed = append(out.changed, ApplyChange{ID: "configure_box_watched_roots", Category: "node_agent", Status: ApplyStatusWouldChange, Path: plan.Paths.BoxPath})
		return out, nil
	}
	watchPlan, err := box.BuildWatchPlan(box.WatchStatusInput{Resolved: box.Resolved{
		RootPath:         plan.Paths.BoxPath,
		Profile:          plan.Spec.BoxProfile,
		OwnerNode:        plan.Spec.NodeKey,
		NodeRole:         plan.Spec.NodeRole,
		RuntimeStateRoot: plan.Paths.BoxStateRoot,
		LegacyStateRoot:  plan.Paths.LegacyBoxStateRoot,
	}})
	if err != nil {
		out.blocked = append(out.blocked, ApplyChange{ID: "configure_box_watched_roots", Category: "node_agent", Status: ApplyStatusBlocked, Path: plan.Paths.BoxPath, Message: err.Error()})
		return out, nil
	}
	if len(watchPlan.WatchedRoots) == 0 {
		out.skipped = append(out.skipped, ApplyChange{ID: "configure_box_watched_roots", Category: "node_agent", Status: ApplyStatusSkipped, Path: plan.Paths.BoxPath, Message: "Box watch plan has no enabled watched roots"})
		return out, nil
	}
	runtimeStore := noderuntime.NewStore(store.DataDir)
	watchedStore := agentwatchedroots.NewStore(store.DataDir)
	for _, item := range watchPlan.WatchedRoots {
		var rootConfig agentwatchedroots.RootConfig
		if err := json.Unmarshal(item.ConfigJSON, &rootConfig); err != nil {
			out.blocked = append(out.blocked, ApplyChange{ID: "configure_watched_root", Category: "node_agent", Status: ApplyStatusBlocked, Message: err.Error(), Metadata: map[string]any{"root": item.BackendRootKey}})
			continue
		}
		validated, err := agentwatchedroots.ValidateRootConfig(rootConfig, config.Filesystem)
		if err != nil {
			out.blocked = append(out.blocked, ApplyChange{ID: "configure_watched_root", Category: "node_agent", Status: ApplyStatusBlocked, Message: err.Error(), Metadata: map[string]any{"root": item.BackendRootKey}})
			continue
		}
		workerKey := noderuntime.WatchedRootWorkerKey(validated.Config.RootKey)
		if existing, err := runtimeStore.LoadInstance(workerKey); err == nil && existing.ConfigHash == validated.ConfigHash {
			out.skipped = append(out.skipped, ApplyChange{ID: "configure_watched_root", Category: "node_agent", Status: ApplyStatusAlreadySatisfied, Path: watchedStore.RootDir(validated.Config.RootKey), Metadata: map[string]any{"root": validated.Config.RootKey}})
			continue
		}
		configJSON, err := json.Marshal(validated.Config)
		if err != nil {
			return out, err
		}
		now := time.Now().UTC()
		instance := noderuntime.WorkerInstance{
			WorkerKey:           workerKey,
			Kind:                noderuntime.KindWatchedRoot,
			DisplayName:         validated.Config.DisplayName,
			Enabled:             true,
			IntervalSeconds:     setupWatchedRootWorkerIntervalSeconds(validated.Config),
			LeaseTimeoutSeconds: 120,
			ConfigHash:          validated.ConfigHash,
			ConfigJSON:          configJSON,
			CreatedAt:           now,
			UpdatedAt:           now,
		}
		if existing, err := runtimeStore.LoadInstance(workerKey); err == nil {
			instance.CreatedAt = existing.CreatedAt
		}
		if err := runtimeStore.SaveInstance(instance); err != nil {
			return out, err
		}
		if err := watchedStore.EnsureRoot(validated.Config.RootKey); err != nil {
			return out, err
		}
		out.changed = append(out.changed, ApplyChange{ID: "configure_watched_root", Category: "node_agent", Status: ApplyStatusChanged, Path: watchedStore.RootDir(validated.Config.RootKey), Metadata: map[string]any{"root": validated.Config.RootKey, "worker_key": workerKey}})
	}
	return out, nil
}

func setupWatchedRootWorkerIntervalSeconds(config agentwatchedroots.RootConfig) int {
	if strings.HasPrefix(config.RootKey, "loom_box__") {
		return 60
	}
	return agentwatchedroots.DefaultWorkerIntervalSeconds()
}

func rawConfigHash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func nonMainManifestStatus(manifest InstallManifest) SetupStatusSummary {
	status := SummaryConfigured
	if manifest.Credential.Configured == false {
		status = SummaryPartial
	}
	now := time.Now().UTC()
	return SetupStatusSummary{Status: status, LastCheckedAt: &now, LastHeartbeatAt: manifest.LastStatus.LastHeartbeatAt}
}
