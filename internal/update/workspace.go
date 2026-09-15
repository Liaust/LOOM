package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"loom.local/loom/internal/setup"
)

const (
	WorkspaceUpdateSchemaVersion = "loom.update.workspace.v0.5.3"
	DefaultWorkspaceMainHost     = "loom-main"
	DefaultWorkspaceService      = "launchd"
	ServiceManagerNone           = "none"
)

var workspaceLaunchdRepairDelay = 750 * time.Millisecond

func PlanWorkspaceUpdate(ctx context.Context, input WorkspaceUpdatePlanInput) (WorkspaceUpdatePlan, error) {
	now := currentTime(input.Now)
	spec, diagnostics, err := normalizeWorkspaceSpec(input.Spec)
	if err != nil {
		return WorkspaceUpdatePlan{}, err
	}
	active := discoverWorkspaceActiveRelease(spec)
	target, targetDiagnostics, err := discoverTargetRelease(UpdateSpec{ReleasePath: spec.ReleasePath})
	diagnostics = append(diagnostics, targetDiagnostics...)
	if err != nil {
		return WorkspaceUpdatePlan{}, err
	}
	diagnostics = append(diagnostics, workspacePlanDiagnostics(spec, active, target)...)
	rollback := RollbackPlan{
		Class:               RollbackClassServiceOnly,
		Reason:              "Workspace updates switch user-level release symlinks and restart the node-agent; no local database migrations are applied.",
		ServiceOnlyPossible: true,
		RestoreRequired:     false,
	}
	plan := WorkspaceUpdatePlan{
		SchemaVersion: WorkspaceUpdateSchemaVersion,
		Status:        planStatus(diagnostics),
		CreatedAt:     now,
		Spec:          spec,
		Active:        active,
		Target:        target,
		Backup: BackupRequirement{
			Required:       false,
			Reason:         "Workspace updates do not mutate the production main database or object store.",
			MinimumCommand: "",
		},
		ServiceImpact: ServiceImpact{
			LoomdRestartExpected:           false,
			PostgresRemainUpExpected:       true,
			SchedulerPauseRecommended:      false,
			DirectEventPauseRecommended:    false,
			OptionalWorkerPauseRecommended: false,
		},
		Rollback:    rollback,
		Steps:       workspaceUpdateSteps(spec),
		Diagnostics: diagnostics,
		Metadata: map[string]string{
			"target_kind": "workspace",
			"node_key":    spec.NodeKey,
		},
	}
	plan.PlanHash = HashWorkspacePlan(plan)
	plan.PlanID = planIDFromHash(plan.PlanHash)
	_ = ctx
	return plan, nil
}

func ApplyWorkspaceUpdate(ctx context.Context, input WorkspaceApplyInput) (WorkspaceApplyResult, error) {
	now := currentTime(input.Now)
	plan, err := PlanWorkspaceUpdate(ctx, WorkspaceUpdatePlanInput{Spec: input.Spec, Now: input.Now})
	if err != nil {
		return WorkspaceApplyResult{}, err
	}
	updateID := updateIDFromWorkspacePlan(plan)
	manifest := workspaceManifestFromPlan(updateID, plan, UpdateStatusPlanned, now)
	result := WorkspaceApplyResult{
		Status:   manifest.Status,
		UpdateID: updateID,
		Plan:     plan,
		Manifest: manifest,
		Changed:  []UpdateChange{},
	}
	if !input.Yes {
		return refuseWorkspaceApply(result, "workspace update apply requires --yes")
	}
	if plan.Status == PlanStatusBlocked {
		return refuseWorkspaceApply(result, "workspace update plan is blocked")
	}
	if input.DryRun {
		manifest.Status = UpdateStatusDryRun
		result.Status = manifest.Status
		result.Manifest = manifest
		result.Changed = append(result.Changed, UpdateChange{Step: "workspace_apply", Status: "dry_run", Message: "No workspace state was changed."})
		return result, nil
	}

	paths := manifestPaths(plan.Spec.StateDir, updateID)
	result.PendingPath = paths.pending
	result.ManifestPath = paths.active
	result.HistoryPath = paths.history
	manifest.Status = UpdateStatusRunning
	result.Status = manifest.Status
	result.Manifest = manifest
	if err := writeAllUpdateManifests(paths, manifest, false); err != nil {
		return result, err
	}

	runner := firstRunner(input.Runner)
	if err := restartWorkspaceService(ctx, runner, plan.Spec, "stop"); err != nil {
		return failWorkspaceApply(result, paths, manifest, "launchd_stop", err)
	}
	result.Changed = append(result.Changed, workspaceServiceChange(plan.Spec, "launchd_stop"))

	if err := switchActiveRelease(plan.Spec.ActivePath, plan.Target.Path); err != nil {
		return failWorkspaceApply(result, paths, manifest, "switch_release", err)
	}
	result.Changed = append(result.Changed, UpdateChange{Step: "switch_release", Status: "changed", Path: plan.Spec.ActivePath})

	if changes, err := refreshWorkspaceBinaryLinks(plan.Spec); err != nil {
		return failWorkspaceApply(result, paths, manifest, "refresh_binary_links", err)
	} else {
		result.Changed = append(result.Changed, changes...)
	}

	if err := restartWorkspaceService(ctx, runner, plan.Spec, "start"); err != nil {
		return failWorkspaceApply(result, paths, manifest, "launchd_start", err)
	}
	result.Changed = append(result.Changed, workspaceServiceChange(plan.Spec, "launchd_start"))

	if plan.Spec.SkipHealthCheck {
		result.Changed = append(result.Changed, UpdateChange{Step: "workspace_health", Status: "skipped", Message: "Skipped by operator flag."})
	} else if err := runWorkspaceHealthChecks(ctx, runner, plan.Spec); err != nil {
		return failWorkspaceApply(result, paths, manifest, "workspace_health", err)
	} else {
		result.Changed = append(result.Changed, UpdateChange{Step: "node_agent_status", Status: "succeeded", Path: plan.Spec.NodeAgentBinary})
		result.Changed = append(result.Changed, UpdateChange{Step: "node_agent_heartbeat", Status: "succeeded", Path: plan.Spec.NodeAgentBinary})
		result.Changed = append(result.Changed, UpdateChange{Step: "main_node_health", Status: "succeeded", Message: plan.Spec.MainHost + " " + plan.Spec.NodeKey})
	}

	finished := currentTime(input.Now)
	manifest.Status = UpdateStatusSucceeded
	manifest.FinishedAt = &finished
	result.Status = manifest.Status
	result.Manifest = manifest
	if err := writeAllUpdateManifests(paths, manifest, true); err != nil {
		return result, err
	}
	result.Changed = append(result.Changed, UpdateChange{Step: "manifest", Status: "written", Path: paths.active})
	return result, nil
}

func RollbackWorkspaceUpdate(ctx context.Context, input WorkspaceRollbackInput) (RollbackResult, error) {
	now := currentTime(input.Now)
	spec, err := normalizeWorkspaceRollbackSpec(input)
	if err != nil {
		return RollbackResult{}, err
	}
	manifestPath := strings.TrimSpace(input.ManifestPath)
	if manifestPath == "" {
		manifestPath = ActiveManifestPath(spec.StateDir)
	}
	manifest, err := ReadUpdateManifest(manifestPath)
	if err != nil {
		return RollbackResult{}, err
	}
	result := RollbackResult{
		Status:       "planned",
		UpdateID:     manifest.UpdateID,
		ManifestPath: manifestPath,
		Manifest:     &manifest,
		Changed:      []UpdateChange{},
	}
	if !input.Yes {
		return refuseRollback(result, "workspace update rollback requires --yes")
	}
	toPath := strings.TrimSpace(input.ToReleasePath)
	if toPath == "" {
		toPath = firstNonEmpty(manifest.Active.Path, metadataString(manifest.Metadata, "active_release_before"))
	}
	if toPath == "" {
		return refuseRollback(result, "workspace rollback target release path is required")
	}
	result.TargetPath = filepath.Clean(toPath)

	rollbackManifest := manifest
	rollbackManifest.UpdateID = rollbackUpdateID(manifest.UpdateID, now)
	rollbackManifest.Status = UpdateStatusDryRun
	rollbackManifest.StartedAt = now
	rollbackManifest.FinishedAt = nil
	rollbackManifest.Active = manifest.Target
	rollbackManifest.Target = ReleaseState{
		ReleaseID: releaseID(result.TargetPath, manifest.Active.Version, manifest.Active.Commit),
		Path:      result.TargetPath,
		Version:   manifest.Active.Version,
		Commit:    manifest.Active.Commit,
		Source:    "workspace_rollback_target",
	}
	rollbackManifest.Metadata = map[string]any{
		"schema_version":        "loom.update.workspace.rollback.metadata.v0.5.3",
		"target_kind":           "workspace",
		"rollback_of":           manifest.UpdateID,
		"active_path":           spec.ActivePath,
		"active_release_before": manifest.Target.Path,
		"node_key":              spec.NodeKey,
		"main_host":             spec.MainHost,
		"service_manager":       spec.ServiceManager,
	}
	result.Manifest = &rollbackManifest

	if input.DryRun {
		result.Status = UpdateStatusDryRun
		result.Changed = append(result.Changed, UpdateChange{Step: "workspace_rollback", Status: "dry_run", Message: "No workspace state was changed."})
		return result, nil
	}
	paths := manifestPaths(spec.StateDir, rollbackManifest.UpdateID)
	result.ManifestPath = paths.active
	result.HistoryPath = paths.history

	runner := firstRunner(input.Runner)
	if err := restartWorkspaceService(ctx, runner, spec, "stop"); err != nil {
		return result, err
	}
	result.Changed = append(result.Changed, workspaceServiceChange(spec, "launchd_stop"))

	if err := switchActiveRelease(spec.ActivePath, result.TargetPath); err != nil {
		return result, err
	}
	result.Changed = append(result.Changed, UpdateChange{Step: "switch_release", Status: "changed", Path: spec.ActivePath})

	if changes, err := refreshWorkspaceBinaryLinks(spec); err != nil {
		return result, err
	} else {
		result.Changed = append(result.Changed, changes...)
	}
	if err := restartWorkspaceService(ctx, runner, spec, "start"); err != nil {
		return result, err
	}
	result.Changed = append(result.Changed, workspaceServiceChange(spec, "launchd_start"))

	if spec.SkipHealthCheck {
		result.Changed = append(result.Changed, UpdateChange{Step: "workspace_health", Status: "skipped", Message: "Skipped by operator flag."})
	} else if err := runWorkspaceHealthChecks(ctx, runner, spec); err != nil {
		return result, err
	} else {
		result.Changed = append(result.Changed, UpdateChange{Step: "node_agent_status", Status: "succeeded", Path: spec.NodeAgentBinary})
		result.Changed = append(result.Changed, UpdateChange{Step: "node_agent_heartbeat", Status: "succeeded", Path: spec.NodeAgentBinary})
		result.Changed = append(result.Changed, UpdateChange{Step: "main_node_health", Status: "succeeded", Message: spec.MainHost + " " + spec.NodeKey})
	}

	finished := currentTime(input.Now)
	rollbackManifest.Status = UpdateStatusRolledBack
	rollbackManifest.FinishedAt = &finished
	result.Status = rollbackManifest.Status
	result.Manifest = &rollbackManifest
	if err := writeAllUpdateManifests(paths, rollbackManifest, true); err != nil {
		return result, err
	}
	result.Changed = append(result.Changed, UpdateChange{Step: "manifest", Status: "written", Path: paths.active})
	return result, nil
}

func normalizeWorkspaceSpec(spec WorkspaceUpdateSpec) (WorkspaceUpdateSpec, []UpdateDiagnostic, error) {
	diagnostics := []UpdateDiagnostic{}
	home, err := workspaceHome(spec.HomeDir)
	if err != nil {
		return WorkspaceUpdateSpec{}, nil, err
	}
	spec.HomeDir = home
	if strings.TrimSpace(spec.ReleasePath) == "" {
		return WorkspaceUpdateSpec{}, nil, fmt.Errorf("release path is required")
	}
	releasePath, err := filepath.Abs(spec.ReleasePath)
	if err != nil {
		return WorkspaceUpdateSpec{}, nil, err
	}
	spec.ReleasePath = filepath.Clean(releasePath)
	if info, err := os.Stat(spec.ReleasePath); err != nil {
		return WorkspaceUpdateSpec{}, nil, fmt.Errorf("release path is not readable: %w", err)
	} else if !info.IsDir() {
		return WorkspaceUpdateSpec{}, nil, fmt.Errorf("release path is not a directory: %s", spec.ReleasePath)
	}
	if strings.TrimSpace(spec.StateDir) == "" {
		spec.StateDir = filepath.Join(home, ".local", "state", "loom", "update")
	} else {
		spec.StateDir = normalizeWorkspacePath(spec.StateDir, home)
	}
	if strings.TrimSpace(spec.ActivePath) == "" {
		spec.ActivePath = filepath.Join(home, ".local", "share", "loom", "current")
	} else {
		spec.ActivePath = normalizeWorkspacePath(spec.ActivePath, home)
	}
	if strings.TrimSpace(spec.LocalBinDir) == "" {
		spec.LocalBinDir = filepath.Join(home, ".local", "bin")
	} else {
		spec.LocalBinDir = normalizeWorkspacePath(spec.LocalBinDir, home)
	}
	if strings.TrimSpace(spec.NodeKey) == "" {
		spec.NodeKey = setup.DefaultWorkspaceNodeKey
	}
	if strings.TrimSpace(spec.MainHost) == "" {
		spec.MainHost = DefaultWorkspaceMainHost
	}
	if strings.TrimSpace(spec.ServiceManager) == "" {
		spec.ServiceManager = DefaultWorkspaceService
	}
	if strings.TrimSpace(spec.LaunchAgentLabel) == "" {
		spec.LaunchAgentLabel = setup.LaunchAgentLabel
	}
	if strings.TrimSpace(spec.LaunchAgentPlist) == "" {
		spec.LaunchAgentPlist = setup.LaunchAgentPlistPath(home)
	} else {
		spec.LaunchAgentPlist = normalizeWorkspacePath(spec.LaunchAgentPlist, home)
	}
	if strings.TrimSpace(spec.LoomBinary) == "" {
		spec.LoomBinary = filepath.Join(spec.LocalBinDir, "loom")
	} else {
		spec.LoomBinary = normalizeWorkspacePath(spec.LoomBinary, home)
	}
	if strings.TrimSpace(spec.NodeAgentBinary) == "" {
		spec.NodeAgentBinary = filepath.Join(spec.LocalBinDir, "loom-node-agent")
	} else {
		spec.NodeAgentBinary = normalizeWorkspacePath(spec.NodeAgentBinary, home)
	}
	if info, err := os.Lstat(spec.ActivePath); err == nil && info.Mode()&os.ModeSymlink == 0 {
		diagnostics = append(diagnostics, UpdateDiagnostic{Severity: DiagnosticBlocking, Code: "update.workspace_active_not_symlink", Message: "Workspace active path exists and is not a symlink.", Path: spec.ActivePath})
	} else if err != nil && !os.IsNotExist(err) {
		diagnostics = append(diagnostics, UpdateDiagnostic{Severity: DiagnosticBlocking, Code: "update.workspace_active_unreadable", Message: err.Error(), Path: spec.ActivePath})
	} else if os.IsNotExist(err) {
		diagnostics = append(diagnostics, UpdateDiagnostic{Severity: DiagnosticWarning, Code: "update.workspace_active_missing", Message: "Workspace active release symlink does not exist yet.", Path: spec.ActivePath})
	}
	return spec, diagnostics, nil
}

func normalizeWorkspaceRollbackSpec(input WorkspaceRollbackInput) (WorkspaceUpdateSpec, error) {
	spec := WorkspaceUpdateSpec{
		HomeDir:            input.HomeDir,
		StateDir:           input.StateDir,
		ActivePath:         input.ActivePath,
		LocalBinDir:        input.LocalBinDir,
		NodeKey:            input.NodeKey,
		MainHost:           input.MainHost,
		ServiceManager:     input.ServiceManager,
		LaunchAgentLabel:   input.LaunchAgentLabel,
		LaunchAgentPlist:   input.LaunchAgentPlist,
		LoomBinary:         input.LoomBinary,
		NodeAgentBinary:    input.NodeAgentBinary,
		SkipServiceRestart: input.SkipServiceRestart,
		SkipHealthCheck:    input.SkipHealthCheck,
		ReleasePath:        firstNonEmpty(input.ToReleasePath, "."),
	}
	home, err := workspaceHome(spec.HomeDir)
	if err != nil {
		return WorkspaceUpdateSpec{}, err
	}
	spec.HomeDir = home
	if strings.TrimSpace(spec.StateDir) == "" {
		spec.StateDir = filepath.Join(home, ".local", "state", "loom", "update")
	} else {
		spec.StateDir = normalizeWorkspacePath(spec.StateDir, home)
	}
	if strings.TrimSpace(spec.ActivePath) == "" {
		spec.ActivePath = filepath.Join(home, ".local", "share", "loom", "current")
	} else {
		spec.ActivePath = normalizeWorkspacePath(spec.ActivePath, home)
	}
	if strings.TrimSpace(spec.LocalBinDir) == "" {
		spec.LocalBinDir = filepath.Join(home, ".local", "bin")
	} else {
		spec.LocalBinDir = normalizeWorkspacePath(spec.LocalBinDir, home)
	}
	if strings.TrimSpace(spec.NodeKey) == "" {
		spec.NodeKey = setup.DefaultWorkspaceNodeKey
	}
	if strings.TrimSpace(spec.MainHost) == "" {
		spec.MainHost = DefaultWorkspaceMainHost
	}
	if strings.TrimSpace(spec.ServiceManager) == "" {
		spec.ServiceManager = DefaultWorkspaceService
	}
	if strings.TrimSpace(spec.LaunchAgentLabel) == "" {
		spec.LaunchAgentLabel = setup.LaunchAgentLabel
	}
	if strings.TrimSpace(spec.LaunchAgentPlist) == "" {
		spec.LaunchAgentPlist = setup.LaunchAgentPlistPath(home)
	} else {
		spec.LaunchAgentPlist = normalizeWorkspacePath(spec.LaunchAgentPlist, home)
	}
	if strings.TrimSpace(spec.LoomBinary) == "" {
		spec.LoomBinary = filepath.Join(spec.LocalBinDir, "loom")
	} else {
		spec.LoomBinary = normalizeWorkspacePath(spec.LoomBinary, home)
	}
	if strings.TrimSpace(spec.NodeAgentBinary) == "" {
		spec.NodeAgentBinary = filepath.Join(spec.LocalBinDir, "loom-node-agent")
	} else {
		spec.NodeAgentBinary = normalizeWorkspacePath(spec.NodeAgentBinary, home)
	}
	return spec, nil
}

func discoverWorkspaceActiveRelease(spec WorkspaceUpdateSpec) ReleaseState {
	activePath := resolvedSymlinkTarget(spec.ActivePath)
	source := "active_path"
	if activePath == "" {
		activePath = spec.ActivePath
	} else {
		source = "active_path_resolved"
	}
	state := ReleaseState{
		Path:   activePath,
		Source: source,
	}
	if manifest, manifestPath, err := LoadReleaseManifest(activePath); err == nil {
		state.ReleaseID = firstNonEmpty(manifest.ReleaseID, releaseID(activePath, manifest.Version, manifest.Commit))
		state.Version = manifest.Version
		state.Commit = manifest.Commit
		state.ManifestPath = manifestPath
		state.FlakeOutput = manifest.FlakeOutput
		state.MigrationsDir = manifest.MigrationsDir
	}
	if state.Commit == "" {
		state.Commit = gitCommit(activePath)
	}
	if state.Version == "" {
		state.Version = runtime.Version()
	}
	if state.ReleaseID == "" {
		state.ReleaseID = releaseID(activePath, state.Version, state.Commit)
	}
	return state
}

func workspacePlanDiagnostics(spec WorkspaceUpdateSpec, _ ReleaseState, target ReleaseState) []UpdateDiagnostic {
	diagnostics := []UpdateDiagnostic{}
	for _, binary := range []string{"loom", "loom-node-agent"} {
		path := filepath.Join(target.Path, "bin", binary)
		if info, err := os.Stat(path); err != nil {
			diagnostics = append(diagnostics, UpdateDiagnostic{Severity: DiagnosticBlocking, Code: "update.workspace_target_binary_missing", Message: "Target workspace release is missing required binary " + binary + ".", Path: path})
		} else if info.IsDir() {
			diagnostics = append(diagnostics, UpdateDiagnostic{Severity: DiagnosticBlocking, Code: "update.workspace_target_binary_invalid", Message: "Target workspace binary path is a directory.", Path: path})
		}
	}
	if spec.ServiceManager == DefaultWorkspaceService && !spec.SkipServiceRestart {
		if info, err := os.Stat(spec.LaunchAgentPlist); err != nil {
			diagnostics = append(diagnostics, UpdateDiagnostic{Severity: DiagnosticWarning, Code: "update.workspace_launch_agent_plist_missing", Message: "LaunchAgent plist is missing; restart may fail until setup repair recreates it.", Path: spec.LaunchAgentPlist})
		} else if info.IsDir() {
			diagnostics = append(diagnostics, UpdateDiagnostic{Severity: DiagnosticBlocking, Code: "update.workspace_launch_agent_plist_invalid", Message: "LaunchAgent plist path is a directory.", Path: spec.LaunchAgentPlist})
		}
	}
	return diagnostics
}

func workspaceUpdateSteps(spec WorkspaceUpdateSpec) []UpdateStep {
	serviceStatus := StepStatusPending
	healthStatus := StepStatusPending
	if spec.SkipServiceRestart || spec.ServiceManager == ServiceManagerNone {
		serviceStatus = StepStatusReady
	}
	if spec.SkipHealthCheck {
		healthStatus = StepStatusReady
	}
	return []UpdateStep{
		{ID: "preflight", Title: "Check workspace release and service paths", Status: StepStatusReady, Mutating: false},
		{ID: "launchd_stop", Title: "Stop workspace node-agent LaunchAgent", Status: serviceStatus, Mutating: true},
		{ID: "switch_release", Title: "Switch workspace current release symlink", Status: StepStatusPending, Mutating: true},
		{ID: "refresh_binary_links", Title: "Refresh ~/.local/bin workspace binary symlinks", Status: StepStatusPending, Mutating: true},
		{ID: "launchd_start", Title: "Start workspace node-agent LaunchAgent", Status: serviceStatus, Mutating: true},
		{ID: "workspace_health", Title: "Verify node-agent status, heartbeat, and main node health", Status: healthStatus, Mutating: false},
		{ID: "rollback_model", Title: "Record rollback model: " + RollbackClassServiceOnly, Status: StepStatusReady, Mutating: false},
	}
}

func workspaceManifestFromPlan(updateID string, plan WorkspaceUpdatePlan, status string, now time.Time) UpdateManifest {
	active := plan.Active
	if resolved := resolvedSymlinkTarget(plan.Spec.ActivePath); resolved != "" {
		active.Path = resolved
		active.ReleaseID = releaseID(resolved, active.Version, active.Commit)
		active.Source = "active_path_resolved"
	}
	return UpdateManifest{
		SchemaVersion: UpdateManifestSchemaVersion,
		UpdateID:      updateID,
		Status:        status,
		PlanHash:      plan.PlanHash,
		StartedAt:     now,
		Active:        active,
		Target:        plan.Target,
		Migrations: MigrationPlan{
			Status: "skipped",
		},
		Rollback: plan.Rollback,
		Metadata: map[string]any{
			"schema_version":          "loom.update.workspace.apply.metadata.v0.5.3",
			"target_kind":             "workspace",
			"active_path":             plan.Spec.ActivePath,
			"active_release_before":   active.Path,
			"home_dir":                plan.Spec.HomeDir,
			"local_bin_dir":           plan.Spec.LocalBinDir,
			"node_key":                plan.Spec.NodeKey,
			"main_host":               plan.Spec.MainHost,
			"service_manager":         plan.Spec.ServiceManager,
			"launch_agent_label":      plan.Spec.LaunchAgentLabel,
			"launch_agent_plist":      plan.Spec.LaunchAgentPlist,
			"skip_service_restart":    plan.Spec.SkipServiceRestart,
			"skip_health_check":       plan.Spec.SkipHealthCheck,
			"main_update_gates":       "skipped",
			"database_migration_gate": "skipped",
			"backup_gate":             "skipped",
			"nixos_rebuild":           "skipped",
		},
	}
}

func restartWorkspaceService(ctx context.Context, runner CommandRunner, spec WorkspaceUpdateSpec, phase string) error {
	if spec.SkipServiceRestart || spec.ServiceManager == ServiceManagerNone {
		return nil
	}
	if spec.ServiceManager != DefaultWorkspaceService {
		return fmt.Errorf("unsupported workspace service manager %q", spec.ServiceManager)
	}
	target := launchAgentTarget(spec.LaunchAgentLabel)
	switch phase {
	case "stop":
		// Keep the LaunchAgent loaded while the release symlink is switched.
		// User-level launchd jobs can remain in a transient state after bootout;
		// kickstart in the start phase restarts the loaded job onto the new
		// symlink target without the unload/bootstrap race.
		return nil
	case "start":
		if !workspaceLaunchAgentLoaded(ctx, runner, target) {
			if err := bootstrapWorkspaceLaunchAgent(ctx, runner, spec); err != nil {
				return err
			}
		}
		if _, err := runner(ctx, "launchctl", "enable", target); err != nil {
			return err
		}
		if _, err := runner(ctx, "launchctl", "kickstart", "-k", target); err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unsupported workspace service restart phase %q", phase)
	}
}

func workspaceLaunchAgentLoaded(ctx context.Context, runner CommandRunner, target string) bool {
	_, err := runner(ctx, "launchctl", "print", target)
	return err == nil
}

func bootstrapWorkspaceLaunchAgent(ctx context.Context, runner CommandRunner, spec WorkspaceUpdateSpec) error {
	const bootstrapAttempts = 4
	var firstErr error
	var firstOutput []byte
	var retryErr error
	var retryOutput []byte
	for attempt := 0; attempt < bootstrapAttempts; attempt++ {
		output, err := runner(ctx, "launchctl", "bootstrap", launchAgentDomain(), spec.LaunchAgentPlist)
		if err == nil {
			return nil
		}
		if firstErr == nil {
			firstErr = err
			firstOutput = output
		}
		retryErr = err
		retryOutput = output
		if alreadyLoadedLaunchdBootstrapError(output, err) {
			return nil
		}
		if !recoverableLaunchdBootstrapError(output, err) {
			return err
		}
		if attempt == bootstrapAttempts-1 {
			break
		}
		if err := waitWorkspaceLaunchdRepairDelay(ctx, attempt); err != nil {
			return fmt.Errorf("wait before retrying workspace LaunchAgent bootstrap: %w", err)
		}
	}
	return fmt.Errorf("workspace LaunchAgent bootstrap failed after retries: original=%v original_output=%s retry=%v output=%s", firstErr, strings.TrimSpace(string(firstOutput)), retryErr, strings.TrimSpace(string(retryOutput)))
}

func recoverableLaunchdBootstrapError(output []byte, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(string(output) + " " + err.Error()))
	return strings.Contains(text, "bootstrap failed: 5") || strings.Contains(text, "input/output error")
}

func alreadyLoadedLaunchdBootstrapError(output []byte, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(string(output) + " " + err.Error()))
	return strings.Contains(text, "already loaded") || strings.Contains(text, "already bootstrapped")
}

func waitWorkspaceLaunchdRepairDelay(ctx context.Context, attempt int) error {
	if workspaceLaunchdRepairDelay <= 0 {
		return nil
	}
	delay := workspaceLaunchdRepairDelay
	for i := 0; i < attempt; i++ {
		delay *= 2
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func workspaceServiceChange(spec WorkspaceUpdateSpec, step string) UpdateChange {
	if spec.SkipServiceRestart || spec.ServiceManager == ServiceManagerNone {
		return UpdateChange{Step: step, Status: "skipped", Message: "Workspace service restart skipped."}
	}
	if step == "launchd_stop" && spec.ServiceManager == DefaultWorkspaceService {
		return UpdateChange{Step: step, Status: "skipped", Message: "LaunchAgent remains loaded; restart deferred to launchd_start."}
	}
	return UpdateChange{Step: step, Status: "changed", Message: spec.LaunchAgentLabel}
}

func refreshWorkspaceBinaryLinks(spec WorkspaceUpdateSpec) ([]UpdateChange, error) {
	changes := []UpdateChange{}
	for _, binary := range []string{"loom", "loom-node-agent"} {
		linkPath := filepath.Join(spec.LocalBinDir, binary)
		targetPath := filepath.Join(spec.ActivePath, "bin", binary)
		if err := switchActiveRelease(linkPath, targetPath); err != nil {
			return changes, err
		}
		changes = append(changes, UpdateChange{Step: "link_" + binary, Status: "changed", Path: linkPath, Message: targetPath})
	}
	return changes, nil
}

func runWorkspaceHealthChecks(ctx context.Context, runner CommandRunner, spec WorkspaceUpdateSpec) error {
	if err := runJSONOK(ctx, runner, spec.NodeAgentBinary, "--json", "status"); err != nil {
		return fmt.Errorf("node-agent status failed: %w", err)
	}
	if err := runJSONOK(ctx, runner, spec.NodeAgentBinary, "--json", "heartbeat", "--once"); err != nil {
		return fmt.Errorf("node-agent heartbeat failed: %w", err)
	}
	if strings.TrimSpace(spec.MainHost) != "" {
		if err := runJSONOK(ctx, runner, "ssh", spec.MainHost, "loom", "--json", "node", "health", spec.NodeKey); err != nil {
			return fmt.Errorf("main node health failed: %w", err)
		}
	}
	return nil
}

func runJSONOK(ctx context.Context, runner CommandRunner, name string, args ...string) error {
	output, err := runner(ctx, name, args...)
	if err != nil {
		return err
	}
	var envelope struct {
		OK     *bool           `json:"ok"`
		Data   json.RawMessage `json:"data"`
		Status string          `json:"status"`
	}
	if err := json.Unmarshal(output, &envelope); err != nil {
		return fmt.Errorf("parse json output: %w", err)
	}
	if envelope.OK != nil && !*envelope.OK {
		return fmt.Errorf("json envelope ok=false")
	}
	if envelope.OK == nil && strings.EqualFold(envelope.Status, "failed") {
		return fmt.Errorf("json status failed")
	}
	return nil
}

func HashWorkspacePlan(plan WorkspaceUpdatePlan) string {
	plan.PlanID = ""
	plan.PlanHash = ""
	raw, _ := json.Marshal(plan)
	hash := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(hash[:])
}

func updateIDFromWorkspacePlan(plan WorkspaceUpdatePlan) string {
	hash := strings.TrimPrefix(plan.PlanHash, "sha256:")
	if len(hash) < 16 {
		return "workspace_update_pending"
	}
	return "workspace_update_" + hash[:16]
}

func failWorkspaceApply(result WorkspaceApplyResult, paths updateManifestPaths, manifest UpdateManifest, step string, cause error) (WorkspaceApplyResult, error) {
	finished := time.Now().UTC()
	manifest.Status = UpdateStatusFailed
	manifest.FinishedAt = &finished
	manifest.Diagnostics = append(manifest.Diagnostics, UpdateDiagnostic{
		Severity: DiagnosticError,
		Code:     "update.workspace_apply_failed",
		Message:  cause.Error(),
	})
	manifest.Metadata["failed_step"] = step
	result.Status = manifest.Status
	result.Manifest = manifest
	result.Changed = append(result.Changed, UpdateChange{Step: step, Status: "failed", Message: cause.Error()})
	_ = writeAllUpdateManifests(paths, manifest, true)
	return result, cause
}

func refuseWorkspaceApply(result WorkspaceApplyResult, reason string) (WorkspaceApplyResult, error) {
	result.Refused = true
	result.Refusal = reason
	result.Status = "refused"
	result.Manifest.Status = "refused"
	return result, nil
}

func workspaceHome(explicit string) (string, error) {
	home := strings.TrimSpace(explicit)
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", err
		}
	}
	if strings.HasPrefix(home, "~") {
		if current, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(current, strings.TrimPrefix(strings.TrimPrefix(home, "~"), string(filepath.Separator)))
		}
	}
	if !filepath.IsAbs(home) {
		abs, err := filepath.Abs(home)
		if err != nil {
			return "", err
		}
		home = abs
	}
	return filepath.Clean(home), nil
}

func normalizeWorkspacePath(path string, home string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "~") {
		path = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), string(filepath.Separator)))
	}
	if !filepath.IsAbs(path) {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
	}
	return filepath.Clean(path)
}

func launchAgentDomain() string {
	return "gui/" + strconv.Itoa(os.Getuid())
}

func launchAgentTarget(label string) string {
	label = firstNonEmpty(label, setup.LaunchAgentLabel)
	return launchAgentDomain() + "/" + label
}

func currentUserName() string {
	if current, err := user.Current(); err == nil {
		return current.Username
	}
	return ""
}
