package update

import (
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
	"time"
)

func RenderPlan(w io.Writer, plan UpdatePlan) {
	fmt.Fprintf(w, "LOOM update plan: %s\n", plan.Status)
	fmt.Fprintf(w, "Plan: %s\n", plan.PlanID)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Release")
	fmt.Fprintf(w, "  active: %s %s\n", firstNonEmpty(plan.Active.ReleaseID, "-"), firstNonEmpty(plan.Active.Path, "-"))
	fmt.Fprintf(w, "  target: %s %s\n", firstNonEmpty(plan.Target.ReleaseID, "-"), firstNonEmpty(plan.Target.Path, "-"))
	fmt.Fprintf(w, "  target flake: %s\n", firstNonEmpty(plan.Nix.TargetFlakeOutput, "-"))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Migrations")
	fmt.Fprintf(w, "  current: %d\n", plan.Migrations.CurrentVersion)
	fmt.Fprintf(w, "  active latest: %d\n", plan.Migrations.ActiveLatestVersion)
	fmt.Fprintf(w, "  target latest: %d\n", plan.Migrations.TargetLatestVersion)
	fmt.Fprintf(w, "  pending: %d\n", plan.Migrations.Pending)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Backup")
	fmt.Fprintf(w, "  required: %t\n", plan.Backup.Required)
	fmt.Fprintf(w, "  command: %s\n", plan.Backup.MinimumCommand)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Rollback")
	fmt.Fprintf(w, "  class: %s\n", plan.Rollback.Class)
	fmt.Fprintf(w, "  restore required: %t\n", plan.Rollback.RestoreRequired)
	if plan.Rollback.Reason != "" {
		fmt.Fprintf(w, "  reason: %s\n", plan.Rollback.Reason)
	}
	if len(plan.Diagnostics) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Diagnostics")
		for _, diagnostic := range plan.Diagnostics {
			fmt.Fprintf(w, "  %s %s: %s\n", diagnostic.Severity, diagnostic.Code, diagnostic.Message)
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Steps")
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, step := range plan.Steps {
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", step.Status, step.ID, step.Title)
	}
	_ = tw.Flush()
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Plan hash: %s\n", plan.PlanHash)
}

func RenderStatus(w io.Writer, status UpdateStatus) {
	fmt.Fprintln(w, "LOOM update status")
	fmt.Fprintf(w, "State: %s\n", status.StateDir)
	if status.Active != nil {
		fmt.Fprintf(w, "Active: %s %s\n", status.Active.UpdateID, status.Active.Status)
		fmt.Fprintf(w, "Target: %s\n", firstNonEmpty(status.Active.Target.Path, "-"))
	} else {
		fmt.Fprintf(w, "Active: none (%s)\n", status.ActiveManifestPath)
	}
	fmt.Fprintf(w, "History: %d\n", len(status.History))
	if len(status.Diagnostics) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Diagnostics")
		for _, diagnostic := range status.Diagnostics {
			fmt.Fprintf(w, "  %s %s: %s\n", diagnostic.Severity, diagnostic.Code, diagnostic.Message)
		}
	}
}

func RenderManifest(w io.Writer, path string, manifest UpdateManifest) {
	fmt.Fprintln(w, "LOOM update manifest")
	fmt.Fprintf(w, "Path: %s\n", path)
	fmt.Fprintf(w, "Update: %s %s\n", manifest.UpdateID, manifest.Status)
	fmt.Fprintf(w, "Plan: %s\n", firstNonEmpty(manifest.PlanHash, "-"))
	fmt.Fprintf(w, "Active: %s\n", firstNonEmpty(manifest.Active.Path, "-"))
	fmt.Fprintf(w, "Target: %s\n", firstNonEmpty(manifest.Target.Path, "-"))
	fmt.Fprintf(w, "Started: %s\n", formatTime(manifest.StartedAt))
	if manifest.FinishedAt != nil {
		fmt.Fprintf(w, "Finished: %s\n", formatTime(*manifest.FinishedAt))
	}
	fmt.Fprintf(w, "Rollback: %s\n", manifest.Rollback.Class)
}

func RenderHistory(w io.Writer, history []UpdateManifest) {
	if len(history) == 0 {
		fmt.Fprintln(w, "No update history.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "UPDATE\tSTATUS\tSTARTED\tTARGET\tROLLBACK")
	for _, manifest := range history {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			manifest.UpdateID,
			manifest.Status,
			formatTime(manifest.StartedAt),
			firstNonEmpty(manifest.Target.ReleaseID, manifest.Target.Path, "-"),
			manifest.Rollback.Class,
		)
	}
	_ = tw.Flush()
}

func RenderReleaseStageResult(w io.Writer, result ReleaseStageResult) {
	fmt.Fprintf(w, "LOOM release stage: %s\n", result.Status)
	fmt.Fprintf(w, "Source: %s\n", firstNonEmpty(result.SourcePath, "-"))
	fmt.Fprintf(w, "Stage: %s\n", firstNonEmpty(result.StagePath, "-"))
	fmt.Fprintf(w, "Target: %s\n", firstNonEmpty(result.TargetPath, "-"))
	if result.ManifestPath != "" {
		fmt.Fprintf(w, "Manifest: %s\n", result.ManifestPath)
	}
	fmt.Fprintf(w, "Release: %s\n", firstNonEmpty(result.ReleaseID, "-"))
	fmt.Fprintf(w, "Target flake: %s\n", firstNonEmpty(result.FlakeOutput, "-"))
	renderChanges(w, result.Changed)
	if len(result.Diagnostics) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Diagnostics")
		for _, diagnostic := range result.Diagnostics {
			fmt.Fprintf(w, "  %s %s: %s\n", diagnostic.Severity, diagnostic.Code, diagnostic.Message)
		}
	}
}

func RenderReleaseRetentionPlan(w io.Writer, plan ReleaseRetentionPlan) {
	fmt.Fprintf(w, "LOOM release retention plan: %s\n", plan.Status)
	fmt.Fprintf(w, "Releases: %s\n", plan.Spec.ReleasesDir)
	fmt.Fprintf(w, "Protected: %d releases, %s\n", len(plan.Protected), formatReleaseRetentionBytes(plan.ProtectedByte))
	fmt.Fprintf(w, "Delete: %d releases, %s\n", len(plan.Delete), formatReleaseRetentionBytes(plan.DeleteBytes))
	if len(plan.Protected) > 0 {
		fmt.Fprintln(w, "\nProtected releases")
		for _, entry := range plan.Protected {
			fmt.Fprintf(w, "  %s  %s  %v\n", entry.Name, formatReleaseRetentionBytes(entry.Bytes), entry.Reasons)
		}
	}
	if len(plan.Delete) > 0 {
		fmt.Fprintln(w, "\nDeletion candidates")
		for _, entry := range plan.Delete {
			fmt.Fprintf(w, "  %s  %s\n", entry.Name, formatReleaseRetentionBytes(entry.Bytes))
		}
	}
	if len(plan.Diagnostics) > 0 {
		fmt.Fprintln(w, "\nDiagnostics")
		for _, diagnostic := range plan.Diagnostics {
			fmt.Fprintf(w, "  %s %s: %s\n", diagnostic.Severity, diagnostic.Code, diagnostic.Message)
		}
	}
	fmt.Fprintf(w, "\nPlan hash: %s\n", plan.PlanHash)
}

func RenderReleaseRetentionApplyResult(w io.Writer, result ReleaseRetentionApplyResult) {
	fmt.Fprintf(w, "LOOM release retention apply: %s\n", result.Status)
	if result.Refused {
		fmt.Fprintf(w, "Refused: %s\n", result.Refusal)
		return
	}
	fmt.Fprintf(w, "Deleted: %d releases\n", len(result.Deleted))
	fmt.Fprintf(w, "Freed: %s\n", formatReleaseRetentionBytes(result.FreedBytes))
}

func formatReleaseRetentionBytes(value int64) string {
	if value < 1024 {
		return strconv.FormatInt(value, 10) + " B"
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	size := float64(value)
	unit := "B"
	for _, candidate := range units {
		size /= 1024
		unit = candidate
		if size < 1024 {
			break
		}
	}
	return fmt.Sprintf("%.1f %s", size, unit)
}

func RenderApplyResult(w io.Writer, result ApplyResult) {
	fmt.Fprintf(w, "LOOM update apply: %s\n", result.Status)
	if result.Refused {
		fmt.Fprintf(w, "Refused: %s\n", result.Refusal)
		return
	}
	fmt.Fprintf(w, "Update: %s\n", result.UpdateID)
	if result.ManifestPath != "" {
		fmt.Fprintf(w, "Manifest: %s\n", result.ManifestPath)
	}
	if result.HistoryPath != "" {
		fmt.Fprintf(w, "History: %s\n", result.HistoryPath)
	}
	renderChanges(w, result.Changed)
	if result.Manifest.Rollback.Class != "" {
		fmt.Fprintf(w, "Rollback: %s\n", result.Manifest.Rollback.Class)
	}
}

func RenderRollbackResult(w io.Writer, result RollbackResult) {
	fmt.Fprintf(w, "LOOM update rollback: %s\n", result.Status)
	if result.Refused {
		fmt.Fprintf(w, "Refused: %s\n", result.Refusal)
		return
	}
	if result.UpdateID != "" {
		fmt.Fprintf(w, "Update: %s\n", result.UpdateID)
	}
	if result.TargetPath != "" {
		fmt.Fprintf(w, "Target: %s\n", result.TargetPath)
	}
	if result.ManifestPath != "" {
		fmt.Fprintf(w, "Manifest: %s\n", result.ManifestPath)
	}
	if len(result.Runbook) > 0 {
		fmt.Fprintln(w, "Runbook:")
		for _, line := range result.Runbook {
			fmt.Fprintf(w, "  - %s\n", line)
		}
	}
	renderChanges(w, result.Changed)
}

func RenderMaintenanceWindow(w io.Writer, manifestPath, updateID string, window *MaintenanceWindow) {
	fmt.Fprintln(w, "LOOM update maintenance")
	fmt.Fprintf(w, "Update: %s\n", firstNonEmpty(updateID, "-"))
	fmt.Fprintf(w, "Manifest: %s\n", firstNonEmpty(manifestPath, "-"))
	if window == nil {
		fmt.Fprintln(w, "Status: none")
		return
	}
	fmt.Fprintf(w, "Window: %s\n", firstNonEmpty(window.WindowID, "-"))
	fmt.Fprintf(w, "Status: %s\n", firstNonEmpty(window.Status, "-"))
	fmt.Fprintf(w, "Started: %s\n", formatTime(window.StartedAt))
	if window.FinishedAt != nil {
		fmt.Fprintf(w, "Finished: %s\n", formatTime(*window.FinishedAt))
	}
	fmt.Fprintf(w, "Policy: schedules=%t direct_events=%t optional_workers=%t\n",
		window.PausePolicy.PauseSchedules,
		window.PausePolicy.PauseDirectEventEndpoints,
		window.PausePolicy.PauseOptionalWorkers,
	)
	renderMaintenanceItems(w, "Schedules", window.Schedules)
	renderMaintenanceItems(w, "Direct Events", window.DirectEvents)
	renderMaintenanceItems(w, "Workers", window.Workers)
	if len(window.Diagnostics) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Diagnostics")
		for _, diagnostic := range window.Diagnostics {
			fmt.Fprintf(w, "  %s %s: %s\n", diagnostic.Severity, diagnostic.Code, diagnostic.Message)
		}
	}
}

func RenderResumeMaintenanceResult(w io.Writer, result ResumeMaintenanceResult) {
	fmt.Fprintf(w, "LOOM update maintenance resume: %s\n", result.Status)
	if result.Refused {
		fmt.Fprintf(w, "Refused: %s\n", result.Refusal)
		return
	}
	if result.UpdateID != "" {
		fmt.Fprintf(w, "Update: %s\n", result.UpdateID)
	}
	if result.ManifestPath != "" {
		fmt.Fprintf(w, "Manifest: %s\n", result.ManifestPath)
	}
	if result.Window != nil {
		fmt.Fprintf(w, "Window: %s\n", result.Window.WindowID)
	}
	renderChanges(w, result.Changed)
}

func RenderWorkspacePlan(w io.Writer, plan WorkspaceUpdatePlan) {
	fmt.Fprintf(w, "LOOM workspace update plan: %s\n", plan.Status)
	fmt.Fprintf(w, "Plan: %s\n", plan.PlanID)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Workspace")
	fmt.Fprintf(w, "  node: %s\n", firstNonEmpty(plan.Spec.NodeKey, "-"))
	fmt.Fprintf(w, "  home: %s\n", firstNonEmpty(plan.Spec.HomeDir, "-"))
	fmt.Fprintf(w, "  current: %s\n", firstNonEmpty(plan.Spec.ActivePath, "-"))
	fmt.Fprintf(w, "  state: %s\n", firstNonEmpty(plan.Spec.StateDir, "-"))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Release")
	fmt.Fprintf(w, "  active: %s %s\n", firstNonEmpty(plan.Active.ReleaseID, "-"), firstNonEmpty(plan.Active.Path, "-"))
	fmt.Fprintf(w, "  target: %s %s\n", firstNonEmpty(plan.Target.ReleaseID, "-"), firstNonEmpty(plan.Target.Path, "-"))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Gates")
	fmt.Fprintf(w, "  backup required: %t\n", plan.Backup.Required)
	fmt.Fprintf(w, "  nixos-rebuild: false\n")
	fmt.Fprintf(w, "  database migrations: skipped\n")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Rollback")
	fmt.Fprintf(w, "  class: %s\n", plan.Rollback.Class)
	fmt.Fprintf(w, "  restore required: %t\n", plan.Rollback.RestoreRequired)
	if len(plan.Diagnostics) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Diagnostics")
		for _, diagnostic := range plan.Diagnostics {
			fmt.Fprintf(w, "  %s %s: %s\n", diagnostic.Severity, diagnostic.Code, diagnostic.Message)
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Steps")
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, step := range plan.Steps {
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", step.Status, step.ID, step.Title)
	}
	_ = tw.Flush()
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Plan hash: %s\n", plan.PlanHash)
}

func RenderWorkspaceApplyResult(w io.Writer, result WorkspaceApplyResult) {
	fmt.Fprintf(w, "LOOM workspace update apply: %s\n", result.Status)
	if result.Refused {
		fmt.Fprintf(w, "Refused: %s\n", result.Refusal)
		return
	}
	fmt.Fprintf(w, "Update: %s\n", result.UpdateID)
	if result.ManifestPath != "" {
		fmt.Fprintf(w, "Manifest: %s\n", result.ManifestPath)
	}
	if result.HistoryPath != "" {
		fmt.Fprintf(w, "History: %s\n", result.HistoryPath)
	}
	renderChanges(w, result.Changed)
	if result.Manifest.Rollback.Class != "" {
		fmt.Fprintf(w, "Rollback: %s\n", result.Manifest.Rollback.Class)
	}
}

func renderMaintenanceItems(w io.Writer, title string, items []MaintenancePausedItem) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, title)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "REF\tPAUSE\tRESUME\tNAME")
	for _, item := range items {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			firstNonEmpty(item.Ref, "-"),
			firstNonEmpty(item.PauseStatus, "-"),
			firstNonEmpty(item.ResumeStatus, "-"),
			firstNonEmpty(item.DisplayName, "-"),
		)
	}
	_ = tw.Flush()
}

func renderChanges(w io.Writer, changes []UpdateChange) {
	if len(changes) == 0 {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Changes")
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "STEP\tSTATUS\tDETAIL")
	for _, change := range changes {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", change.Step, change.Status, firstNonEmpty(change.Path, change.Message, "-"))
	}
	_ = tw.Flush()
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}
