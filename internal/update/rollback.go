package update

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

func Rollback(ctx context.Context, input RollbackInput) (RollbackResult, error) {
	now := currentTime(input.Now)
	manifestPath := strings.TrimSpace(input.ManifestPath)
	if manifestPath == "" {
		stateDir := strings.TrimSpace(input.StateDir)
		if stateDir == "" {
			stateDir = DefaultStateDir("")
		}
		manifestPath = ActiveManifestPath(stateDir)
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
		return refuseRollback(result, "update rollback requires --yes")
	}
	if !input.AllowNonProduction && !isProductionMain(input.Runtime) {
		return refuseRollback(result, "update rollback requires the production main runtime; pass --allow-non-production only for dev drills")
	}
	if input.SkipRebuild && !input.AllowNonProduction {
		return refuseRollback(result, "production rollback cannot skip nixos-rebuild")
	}
	if input.SkipHealthCheck && !input.AllowNonProduction {
		return refuseRollback(result, "production rollback cannot skip health check")
	}
	if input.RestoreRequired || manifest.Rollback.RestoreRequired {
		result.Status = UpdateStatusDatabaseRestoreRequired
		result.Runbook = restoreRequiredRunbook(manifest)
		return result, nil
	}
	if !input.ServiceOnly {
		return refuseRollback(result, "service rollback requires --service-only, or use --restore-required for migration rollback instructions")
	}

	toPath := strings.TrimSpace(input.ToReleasePath)
	if toPath == "" {
		toPath = manifest.Active.Path
	}
	activePath := metadataString(manifest.Metadata, "active_path")
	if activePath == "" {
		activePath = "/srv/loom/current"
	}
	if filepath.Clean(toPath) == filepath.Clean(activePath) {
		toPath = metadataString(manifest.Metadata, "active_release_before")
	}
	if toPath == "" {
		return refuseRollback(result, "rollback target release path is required")
	}
	result.TargetPath = toPath

	rollbackManifest := manifest
	rollbackManifest.UpdateID = rollbackUpdateID(manifest.UpdateID, now)
	rollbackManifest.Status = UpdateStatusDryRun
	rollbackManifest.StartedAt = now
	rollbackManifest.FinishedAt = nil
	rollbackManifest.Active = manifest.Target
	rollbackManifest.Target = ReleaseState{
		ReleaseID: releaseID(toPath, manifest.Active.Version, manifest.Active.Commit),
		Path:      filepath.Clean(toPath),
		Version:   manifest.Active.Version,
		Commit:    manifest.Active.Commit,
		Source:    "rollback_target",
	}
	rollbackManifest.Metadata = map[string]any{
		"schema_version":       "loom.update.rollback.metadata.v0.5.1",
		"rollback_of":          manifest.UpdateID,
		"allow_non_production": input.AllowNonProduction,
		"skip_rebuild":         input.SkipRebuild,
		"skip_health_check":    input.SkipHealthCheck,
		"runtime_environment":  input.Runtime.Environment,
		"runtime_node_id":      input.Runtime.NodeID,
		"runtime_node_role":    input.Runtime.NodeRole,
	}
	result.Manifest = &rollbackManifest

	if input.DryRun {
		result.Status = UpdateStatusDryRun
		result.Changed = append(result.Changed, UpdateChange{Step: "rollback", Status: "dry_run", Message: "No production state was changed."})
		return result, nil
	}
	paths := manifestPaths(firstNonEmpty(input.StateDir, filepath.Dir(filepath.Clean(manifestPath))), rollbackManifest.UpdateID)
	result.ManifestPath = paths.active
	result.HistoryPath = paths.history

	if err := switchActiveRelease(activePath, toPath); err != nil {
		return result, err
	}
	result.Changed = append(result.Changed, UpdateChange{Step: "switch_release", Status: "changed", Path: activePath})

	runner := firstRunner(input.Runner)
	if !input.SkipRebuild {
		if _, err := runner(ctx, "sudo", "nixos-rebuild", "switch", "--flake", flakeArgument(toPath, manifest.NixFlakeOutput()), "--show-trace"); err != nil {
			return result, err
		}
		result.Changed = append(result.Changed, UpdateChange{Step: "nixos_rebuild", Status: "changed"})
	} else {
		result.Changed = append(result.Changed, UpdateChange{Step: "nixos_rebuild", Status: "skipped"})
	}
	if !input.SkipHealthCheck {
		if err := runHealthCheck(ctx, runner); err != nil {
			return result, err
		}
		result.Changed = append(result.Changed, UpdateChange{Step: "health_check", Status: "succeeded"})
	} else {
		result.Changed = append(result.Changed, UpdateChange{Step: "health_check", Status: "skipped"})
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

func refuseRollback(result RollbackResult, reason string) (RollbackResult, error) {
	result.Refused = true
	result.Refusal = reason
	result.Status = "refused"
	return result, nil
}

func restoreRequiredRunbook(manifest UpdateManifest) []string {
	backupRef := firstNonEmpty(manifest.BackupRef, manifest.BackupPath, "<backup-ref-or-path>")
	return []string{
		"Rollback is database-restore required because target migrations may have applied.",
		"Do not attempt service-only rollback as a complete recovery.",
		"Verify backup: loom backup verify " + backupRef,
		"Run restore drill first: loom backup restore-drill " + backupRef,
		"Follow docs/Operations - Backup And Restore.md for destructive database restore.",
	}
}

func rollbackUpdateID(updateID string, now time.Time) string {
	updateID = strings.TrimSpace(updateID)
	if updateID == "" {
		updateID = "update_unknown"
	}
	return updateID + "_rollback_" + now.UTC().Format("20060102150405")
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, ok := metadata[key]
	if !ok {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func (m UpdateManifest) NixFlakeOutput() string {
	if m.Target.FlakeOutput != "" {
		return m.Target.FlakeOutput
	}
	if m.Active.FlakeOutput != "" {
		return m.Active.FlakeOutput
	}
	return DefaultProductionFlakeOutput
}
