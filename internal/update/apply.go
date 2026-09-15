package update

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/maintenance"
)

// NewOperationalPackageBackupVerifier adapts one exact verified operational
// package to the existing injected update-backup verifier. The closure binds
// the registered backup ref, manifest identity, and pre-update schema head;
// Apply independently checks that schema head against its freshly built plan.
func NewOperationalPackageBackupVerifier(expectedBackupRef, expectedManifestSHA256 string, expectedSchemaHead int64, forbiddenValues []string) BackupVerifier {
	expectedBackupRef = strings.TrimSpace(expectedBackupRef)
	forbiddenValues = append([]string(nil), forbiddenValues...)
	return func(ctx context.Context, input BackupVerificationInput) (maintenance.BackupVerification, error) {
		if strings.TrimSpace(input.BackupRef) != expectedBackupRef {
			return maintenance.BackupVerification{
				Status:         maintenance.VerificationFailed,
				BackupDir:      strings.TrimSpace(input.BackupPath),
				ManifestSchema: maintenance.OperationalBackupManifestSchema,
				CheckedAt:      time.Now().UTC(),
				Checks:         map[string]string{"operational_backup_ref": maintenance.VerificationFailed},
				Errors:         []string{"operational package backup ref does not match the registered package"},
			}, nil
		}
		verification, err := maintenance.VerifyOperationalBackupPackage(ctx, input.BackupPath, expectedManifestSHA256, expectedSchemaHead, forbiddenValues)
		if err == nil && verification.Status == maintenance.VerificationSucceeded {
			verification.BackupOperationID = expectedBackupRef
		}
		return verification, err
	}
}

func Apply(ctx context.Context, input ApplyInput) (ApplyResult, error) {
	now := currentTime(input.Now)
	plan, err := Plan(ctx, PlannerInput{Spec: input.Spec, Now: input.Now})
	if err != nil {
		return ApplyResult{}, err
	}
	updateID := updateIDFromPlan(plan)
	manifest := manifestFromPlan(updateID, plan, input, UpdateStatusPlanned, now)
	result := ApplyResult{
		Status:   manifest.Status,
		UpdateID: updateID,
		Plan:     plan,
		Manifest: manifest,
		Changed:  []UpdateChange{},
	}

	if !input.Yes {
		return refuseApply(result, "update apply requires --yes")
	}
	if !input.AllowNonProduction && !isProductionMain(input.Runtime) {
		return refuseApply(result, "update apply requires the production main runtime; pass --allow-non-production only for dev drills")
	}
	if plan.Status == PlanStatusBlocked {
		return refuseApply(result, "update plan is blocked")
	}
	if input.SkipBackup && !input.AllowNonProduction {
		return refuseApply(result, "production update cannot skip backup")
	}
	if input.SkipRebuild && !input.AllowNonProduction {
		return refuseApply(result, "production update cannot skip nixos-rebuild")
	}
	if input.SkipHealthCheck && !input.AllowNonProduction {
		return refuseApply(result, "production update cannot skip health check")
	}
	if plan.Backup.Required && !input.SkipBackup {
		backupPath := strings.TrimSpace(input.BackupPath)
		if backupPath == "" {
			return refuseApply(result, "update apply requires --backup-path for a locally verified backup")
		}
		production := !input.AllowNonProduction && isProductionMain(input.Runtime)
		backupRef := strings.TrimSpace(input.BackupRef)
		if production && backupRef == "" {
			return refuseApply(result, "production update apply requires --backup-ref for the registered backup operation")
		}
		if production {
			if exact, ok := exactCleanAbsolutePath(input.BackupPath); !ok || exact != backupPath {
				return refuseApply(result, "production backup path must be an exact clean absolute path")
			}
			if backupRef != input.BackupRef {
				return refuseApply(result, "production backup ref must be an exact maintenance operation ID")
			}
			if input.BackupVerifier == nil {
				return refuseApply(result, "production update requires service-identity backup verification")
			}
		}

		var verification maintenance.BackupVerification
		var err error
		if input.BackupVerifier != nil {
			verification, err = input.BackupVerifier(ctx, BackupVerificationInput{
				BackupPath: input.BackupPath,
				BackupRef:  input.BackupRef,
			})
		} else {
			// Direct filesystem verification is retained only for explicit
			// non-production drills. Production must use the injected loomd
			// verifier so immutable service-owned custody remains unreadable to
			// the coordinating operator process.
			verification, err = maintenance.VerifyBackupDirectory(ctx, input.BackupPath)
		}
		if err != nil {
			return result, err
		}
		if verification.Status != maintenance.VerificationSucceeded {
			return refuseApply(result, "backup verification failed; run loom backup verify for details")
		}
		if verification.ManifestSchema == maintenance.OperationalBackupManifestSchema {
			if !operationalPackageCompatibleWithPlan(plan) {
				return refuseApply(result, "operational package requires a valid operational-only release backup-scope declaration; use a complete backup")
			}
			if verification.CurrentMigration == nil || *verification.CurrentMigration != plan.Migrations.CurrentVersion {
				return refuseApply(result, "operational package schema head does not match the current update plan")
			}
		}
		if production {
			if verification.BackupOperationID == "" || verification.BackupOperationID != backupRef {
				return refuseApply(result, "backup verifier returned a different maintenance operation identity")
			}
			verifiedPath, ok := exactCleanAbsolutePath(verification.BackupDir)
			if !ok || verifiedPath != backupPath || verification.BackupDir != input.BackupPath {
				return refuseApply(result, "backup verifier resolved a different backup directory")
			}
		}
		result.Changed = append(result.Changed, UpdateChange{Step: "backup", Status: "verified", Path: input.BackupPath})
	}
	if !input.AllowNonProduction && isProductionMain(input.Runtime) && input.Maintenance == nil && maintenancePolicyRequestsPause(input.MaintenancePolicy) {
		return refuseApply(result, "production update requires a maintenance coordinator for pause/resume")
	}

	if input.DryRun {
		manifest.Status = UpdateStatusDryRun
		result.Status = manifest.Status
		result.Manifest = manifest
		result.Changed = append(result.Changed, UpdateChange{Step: "apply", Status: "dry_run", Message: "No production state was changed."})
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

	maintenanceCoordinator := input.Maintenance
	if maintenanceCoordinator == nil {
		maintenanceCoordinator = NoopMaintenanceCoordinator{}
	}
	window, err := maintenanceCoordinator.Open(ctx, MaintenanceOpenInput{
		UpdateID: updateID,
		Policy:   input.MaintenancePolicy,
		Now:      input.Now,
	})
	manifest.Maintenance = &window
	result.Manifest = manifest
	result.Changed = append(result.Changed, UpdateChange{Step: "maintenance_window", Status: window.Status, Message: maintenanceWindowChangeMessage(window)})
	if writeErr := writeAllUpdateManifests(paths, manifest, false); writeErr != nil {
		return result, writeErr
	}
	if err != nil {
		return failApply(result, paths, manifest, "maintenance_window", err)
	}

	if err := switchActiveRelease(plan.Spec.ActivePath, plan.Target.Path); err != nil {
		return failApply(result, paths, manifest, "switch_release", err)
	}
	result.Changed = append(result.Changed, UpdateChange{Step: "switch_release", Status: "changed", Path: plan.Spec.ActivePath})

	runner := firstRunner(input.Runner)
	if !input.SkipRebuild {
		if _, err := runner(ctx, "sudo", "nixos-rebuild", "switch", "--flake", flakeArgument(plan.Target.Path, plan.Nix.TargetFlakeOutput), "--show-trace"); err != nil {
			return failApply(result, paths, manifest, "nixos_rebuild", err)
		}
		result.Changed = append(result.Changed, UpdateChange{Step: "nixos_rebuild", Status: "changed", Message: "nixos-rebuild switch completed."})
	} else {
		result.Changed = append(result.Changed, UpdateChange{Step: "nixos_rebuild", Status: "skipped", Message: "Skipped by operator flag."})
	}
	if !input.SkipHealthCheck {
		if err := runHealthCheck(ctx, runner); err != nil {
			return failApply(result, paths, manifest, "health_check", err)
		}
		result.Changed = append(result.Changed, UpdateChange{Step: "health_check", Status: "succeeded"})
	} else {
		result.Changed = append(result.Changed, UpdateChange{Step: "health_check", Status: "skipped", Message: "Skipped by operator flag."})
	}
	if manifest.Maintenance != nil && maintenanceWindowNeedsResume(manifest.Maintenance) {
		resumed, err := maintenanceCoordinator.Resume(ctx, MaintenanceResumeInput{
			UpdateID: updateID,
			Window:   *manifest.Maintenance,
			Now:      input.Now,
		})
		manifest.Maintenance = &resumed
		result.Changed = append(result.Changed, UpdateChange{Step: "maintenance_resume", Status: resumed.Status, Message: maintenanceWindowChangeMessage(resumed)})
		if err != nil {
			manifest.Diagnostics = append(manifest.Diagnostics, UpdateDiagnostic{
				Severity: DiagnosticWarning,
				Code:     "update.maintenance_resume_required",
				Message:  err.Error(),
			})
			markMaintenanceResumeRequired(manifest.Maintenance)
			result.Changed = append(result.Changed, UpdateChange{Step: "maintenance_resume", Status: MaintenanceWindowStatusResumeRequired, Message: "Some resources still need manual resume."})
		}
	}

	finished := currentTime(input.Now)
	manifest.Status = UpdateStatusSucceeded
	manifest.FinishedAt = &finished
	manifest.Metadata["nix_generation_after"] = currentNixGenerationPath()
	result.Status = manifest.Status
	result.Manifest = manifest
	if err := writeAllUpdateManifests(paths, manifest, true); err != nil {
		return result, err
	}
	result.Changed = append(result.Changed, UpdateChange{Step: "manifest", Status: "written", Path: paths.active})
	return result, nil
}

// operationalPackageCompatibleWithPlan trusts only the normalized, hashed
// declaration derived from the release manifest. Generic update step IDs and
// rebuild/migration combinations never imply filesystem safety.
func operationalPackageCompatibleWithPlan(plan UpdatePlan) bool {
	scope := plan.BackupScope
	return scope.DeclarationStatus == BackupScopeDeclarationValid &&
		scope.DeclaredSchema == ReleaseBackupScopeSchemaVersion &&
		scope.DeclaredClass == ReleaseBackupScopeOperationalOnly &&
		scope.RequiredBackup == UpdateBackupRequirementOperational &&
		scope.OperationalPackageAllowed
}

func exactCleanAbsolutePath(value string) (string, bool) {
	if value == "" || value != strings.TrimSpace(value) || !filepath.IsAbs(value) {
		return "", false
	}
	cleaned := filepath.Clean(value)
	return cleaned, cleaned == value
}

func failApply(result ApplyResult, paths updateManifestPaths, manifest UpdateManifest, step string, cause error) (ApplyResult, error) {
	finished := time.Now().UTC()
	manifest.Status = UpdateStatusFailed
	manifest.FinishedAt = &finished
	markMaintenanceResumeRequired(manifest.Maintenance)
	manifest.Diagnostics = append(manifest.Diagnostics, UpdateDiagnostic{
		Severity: DiagnosticError,
		Code:     "update.apply_failed",
		Message:  cause.Error(),
	})
	manifest.Metadata["failed_step"] = step
	result.Status = manifest.Status
	result.Manifest = manifest
	result.Changed = append(result.Changed, UpdateChange{Step: step, Status: "failed", Message: cause.Error()})
	_ = writeAllUpdateManifests(paths, manifest, true)
	return result, cause
}

func refuseApply(result ApplyResult, reason string) (ApplyResult, error) {
	result.Refused = true
	result.Refusal = reason
	result.Status = "refused"
	result.Manifest.Status = "refused"
	return result, nil
}

func maintenancePolicyRequestsPause(policy MaintenancePausePolicy) bool {
	return policy.PauseSchedules || policy.PauseDirectEventEndpoints || policy.PauseOptionalWorkers
}

func maintenanceWindowChangeMessage(window MaintenanceWindow) string {
	return fmt.Sprintf("schedules=%d direct_events=%d workers=%d", len(window.Schedules), len(window.DirectEvents), len(window.Workers))
}

func manifestFromPlan(updateID string, plan UpdatePlan, input ApplyInput, status string, now time.Time) UpdateManifest {
	active := plan.Active
	if resolvedActive := resolvedSymlinkTarget(plan.Spec.ActivePath); resolvedActive != "" {
		active.Path = resolvedActive
		active.ReleaseID = releaseID(resolvedActive, active.Version, active.Commit)
		active.Source = "active_path_resolved"
	}
	metadata := map[string]any{
		"schema_version":        "loom.update.apply.metadata.v0.5.1",
		"active_path":           plan.Spec.ActivePath,
		"active_release_before": active.Path,
		"nix_generation_before": plan.Nix.CurrentGeneration,
		"allow_non_production":  input.AllowNonProduction,
		"skip_backup":           input.SkipBackup,
		"skip_rebuild":          input.SkipRebuild,
		"skip_health_check":     input.SkipHealthCheck,
		"runtime_environment":   input.Runtime.Environment,
		"runtime_node_id":       input.Runtime.NodeID,
		"runtime_node_role":     input.Runtime.NodeRole,
	}
	return UpdateManifest{
		SchemaVersion: UpdateManifestSchemaVersion,
		UpdateID:      updateID,
		Status:        status,
		PlanHash:      plan.PlanHash,
		StartedAt:     now,
		Active:        active,
		Target:        plan.Target,
		Migrations:    plan.Migrations,
		BackupRef:     strings.TrimSpace(input.BackupRef),
		BackupPath:    strings.TrimSpace(input.BackupPath),
		Rollback:      plan.Rollback,
		Diagnostics:   append([]UpdateDiagnostic{}, plan.Diagnostics...),
		Metadata:      metadata,
	}
}

type updateManifestPaths struct {
	active  string
	pending string
	history string
}

func manifestPaths(stateDir, updateID string) updateManifestPaths {
	stateDir = filepath.Clean(strings.TrimSpace(stateDir))
	return updateManifestPaths{
		active:  ActiveManifestPath(stateDir),
		pending: filepath.Join(PendingDir(stateDir), updateID+".yaml"),
		history: filepath.Join(HistoryDir(stateDir), updateID+".yaml"),
	}
}

func writeAllUpdateManifests(paths updateManifestPaths, manifest UpdateManifest, includeHistory bool) error {
	if err := WriteUpdateManifest(paths.pending, manifest); err != nil {
		return err
	}
	if err := WriteUpdateManifest(paths.active, manifest); err != nil {
		return err
	}
	if includeHistory {
		if err := WriteUpdateManifest(paths.history, manifest); err != nil {
			return err
		}
	}
	return nil
}

func switchActiveRelease(activePath, targetPath string) error {
	activePath = filepath.Clean(strings.TrimSpace(activePath))
	targetPath = filepath.Clean(strings.TrimSpace(targetPath))
	if activePath == "." || activePath == "" {
		return fmt.Errorf("active path is required")
	}
	if targetPath == "." || targetPath == "" {
		return fmt.Errorf("target release path is required")
	}
	info, err := os.Lstat(activePath)
	if err == nil && info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("active path exists and is not a symlink: %s", activePath)
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(activePath), 0o755); err != nil {
		return err
	}
	tmp := activePath + ".next-" + fmt.Sprintf("%d", time.Now().UnixNano())
	if err := os.Symlink(targetPath, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, activePath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func resolvedSymlinkTarget(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	target, err := os.Readlink(path)
	if err != nil {
		return ""
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	if abs, err := filepath.Abs(target); err == nil {
		target = abs
	}
	return filepath.Clean(target)
}

func flakeArgument(releasePath, flakeOutput string) string {
	releasePath = filepath.Clean(strings.TrimSpace(releasePath))
	flakeOutput = strings.TrimSpace(flakeOutput)
	switch {
	case flakeOutput == "":
		return releasePath + "#loom-main"
	case strings.HasPrefix(flakeOutput, ".#"):
		return releasePath + strings.TrimPrefix(flakeOutput, ".")
	case strings.HasPrefix(flakeOutput, "#"):
		return releasePath + flakeOutput
	default:
		return flakeOutput
	}
}

func runHealthCheck(ctx context.Context, runner CommandRunner) error {
	output, err := runner(ctx, "loom", "health", "--json")
	if err != nil {
		return err
	}
	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(output, &envelope); err != nil {
		return fmt.Errorf("parse loom health output: %w", err)
	}
	if !envelope.OK || envelope.Data.Status != "ok" {
		return fmt.Errorf("loom health is not ok")
	}
	return nil
}

func firstRunner(runner CommandRunner) CommandRunner {
	if runner != nil {
		return runner
	}
	return defaultCommandRunner
}

func defaultCommandRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "sudo" && strings.TrimSpace(os.Getenv("LOOM_SUDO_PASSWORD_STDIN")) == "1" {
		args = append([]string{"-S", "-p", ""}, args...)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	if name == "sudo" && strings.TrimSpace(os.Getenv("LOOM_SUDO_PASSWORD_STDIN")) == "1" {
		cmd.Stdin = os.Stdin
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s %s failed: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func isProductionMain(runtime RuntimeIdentity) bool {
	environment := strings.ToLower(strings.TrimSpace(runtime.Environment))
	nodeID := strings.ToLower(strings.TrimSpace(runtime.NodeID))
	nodeRole := strings.ToLower(strings.TrimSpace(runtime.NodeRole))
	return environment == "production" && (nodeID == "main" || nodeRole == "main")
}

func updateIDFromPlan(plan UpdatePlan) string {
	hash := strings.TrimPrefix(plan.PlanHash, "sha256:")
	if len(hash) < 16 {
		return "update_pending"
	}
	return "update_" + hash[:16]
}
