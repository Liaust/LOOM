package setup

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

func RenderPlan(w io.Writer, plan SetupPlan) {
	fmt.Fprintln(w, "LOOM setup plan")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Target")
	fmt.Fprintf(w, "  node: %s\n", plan.Spec.NodeKey)
	fmt.Fprintf(w, "  kind: %s\n", plan.Spec.NodeKind)
	fmt.Fprintf(w, "  role: %s\n", plan.Spec.NodeRole)
	fmt.Fprintf(w, "  runtime: %s\n", plan.Spec.RuntimeClass)
	fmt.Fprintf(w, "  bootstrap: %s\n", plan.Spec.BootstrapMode)
	if plan.Spec.MainURL != "" {
		fmt.Fprintf(w, "  main: %s\n", plan.Spec.MainURL)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Profiles")
	fmt.Fprintf(w, "  authority: %s\n", plan.Profile.AuthorityProfileKey)
	fmt.Fprintf(w, "  runtime: %s\n", plan.Profile.RuntimeProfileKey)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Paths")
	fmt.Fprintf(w, "  Box: %s\n", firstNonEmpty(plan.Paths.BoxPath, "-"))
	fmt.Fprintf(w, "  config: %s\n", firstNonEmpty(plan.Paths.ConfigDir, "-"))
	fmt.Fprintf(w, "  state: %s\n", firstNonEmpty(plan.Paths.StateDir, "-"))
	fmt.Fprintf(w, "  manifest: %s\n", firstNonEmpty(plan.Paths.ManifestPath, "-"))
	if plan.Paths.NodeAgentConfigPath != "" {
		fmt.Fprintf(w, "  node-agent config: %s\n", plan.Paths.NodeAgentConfigPath)
	}
	if plan.Paths.LaunchAgentPlistPath != "" {
		fmt.Fprintf(w, "  LaunchAgent: %s\n", plan.Paths.LaunchAgentPlistPath)
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
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", compactStatus(step.Status), step.Category, step.Title)
	}
	_ = tw.Flush()
	if plan.RetiredIntakeCleanup != nil {
		fmt.Fprintln(w)
		RenderRetiredIntakeCleanupPlan(w, *plan.RetiredIntakeCleanup)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Plan hash: %s\n", plan.PlanHash)
}

func RenderRetiredIntakeCleanupPlan(w io.Writer, plan RetiredIntakeCleanupPlan) {
	fmt.Fprintln(w, "Retired Main Intake Cleanup (plan only)")
	fmt.Fprintf(w, "  total: %d  absent: %d  empty directories: %d  known links: %d  skipped: %d\n",
		plan.Summary.Total,
		plan.Summary.Absent,
		plan.Summary.EligibleEmptyDirs,
		plan.Summary.EligibleKnownLinks,
		plan.Summary.Skipped,
	)
	for _, item := range plan.Items {
		fmt.Fprintf(w, "  %s\t%s\t%s\n", item.State, item.ID, item.Path)
		if item.Reason != "" {
			fmt.Fprintf(w, "    %s\n", item.Reason)
		}
	}
	fmt.Fprintf(w, "  cleanup digest: %s\n", plan.PlanDigest)
	fmt.Fprintln(w, "  apply: save and review the complete setup plan, then use loom setup cleanup apply with its exact cleanup digest")
}

func RenderRetiredIntakeCleanupResult(w io.Writer, result RetiredIntakeCleanupResult) {
	fmt.Fprintln(w, "LOOM retired Main intake cleanup")
	fmt.Fprintf(w, "  status: %s\n", result.Status)
	fmt.Fprintf(w, "  dry-run: %t\n", result.DryRun)
	fmt.Fprintf(w, "  cleanup digest: %s\n", result.PlanDigest)
	for _, change := range result.Removed {
		fmt.Fprintf(w, "  %s\t%s\t%s\n", change.Status, change.ID, change.Path)
	}
	for _, change := range result.Skipped {
		fmt.Fprintf(w, "  %s\t%s\t%s\n", change.Status, change.ID, change.Path)
		if change.Message != "" {
			fmt.Fprintf(w, "    %s\n", change.Message)
		}
	}
}

func RenderManifestInspection(w io.Writer, inspection ManifestInspection) {
	if !inspection.Exists {
		if inspection.Error != "" {
			fmt.Fprintf(w, "Setup manifest error at %s: %s\n", inspection.Path, inspection.Error)
			return
		}
		fmt.Fprintf(w, "No setup manifest found at %s\n", inspection.Path)
		return
	}
	manifest := inspection.Manifest
	fmt.Fprintln(w, "LOOM install manifest")
	fmt.Fprintf(w, "  path: %s\n", inspection.Path)
	fmt.Fprintf(w, "  node: %s\n", manifest.NodeKey)
	fmt.Fprintf(w, "  kind: %s\n", manifest.NodeKind)
	fmt.Fprintf(w, "  role: %s\n", manifest.NodeRole)
	fmt.Fprintf(w, "  runtime: %s\n", manifest.RuntimeClass)
	fmt.Fprintf(w, "  bootstrap: %s\n", firstNonEmpty(manifest.BootstrapMode, "-"))
	fmt.Fprintf(w, "  authority: %s\n", manifest.AuthorityProfile)
	fmt.Fprintf(w, "  runtime profile: %s\n", manifest.RuntimeProfile)
	fmt.Fprintf(w, "  install mode: %s\n", manifest.InstallMode)
	fmt.Fprintf(w, "  Box: %s\n", firstNonEmpty(manifest.BoxPath, "-"))
	fmt.Fprintf(w, "  updated: %s\n", manifest.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"))
}

func RenderStatus(w io.Writer, status SetupStatus, verbose bool) {
	fmt.Fprintf(w, "LOOM setup: %s\n", firstNonEmpty(status.Summary.Status, SummaryUnknown))
	if status.Node.NodeKey != "" {
		fmt.Fprintf(w, "Node: %s %s/%s %s\n", status.Node.NodeKey, status.Node.NodeKind, status.Node.NodeRole, status.Node.RuntimeClass)
	}
	fmt.Fprintf(w, "Manifest: %s %s\n", status.Manifest.State, status.Manifest.Path)
	if status.Box.Enabled {
		fmt.Fprintf(w, "Box: %s %s\n", status.Box.Status, firstNonEmpty(status.Box.Path, "-"))
	} else {
		fmt.Fprintln(w, "Box: disabled")
	}
	if status.NodeAgent.Enabled {
		fmt.Fprintf(w, "Node-agent: %s\n", status.NodeAgent.Status)
	} else {
		fmt.Fprintln(w, "Node-agent: disabled")
	}
	if len(status.Services) > 0 {
		for _, service := range status.Services {
			label := firstNonEmpty(service.Label, service.Name)
			fmt.Fprintf(w, "Service: %s %s %s\n", service.Manager, label, service.Status)
		}
	}
	fmt.Fprintf(w, "Enrollment: %s\n", status.Enrollment.Status)
	if status.MainConnectivity.Required {
		fmt.Fprintf(w, "Main: %s %s\n", status.MainConnectivity.Status, firstNonEmpty(status.MainConnectivity.MainURL, "-"))
	}
	if verbose && len(status.Paths) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Paths")
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, path := range status.Paths {
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", path.Status, path.Key, path.Path)
		}
		_ = tw.Flush()
	}
	if verbose && len(status.Diagnostics) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Diagnostics")
		for _, diagnostic := range status.Diagnostics {
			fmt.Fprintf(w, "  %s %s: %s\n", diagnostic.Severity, diagnostic.Code, diagnostic.Message)
		}
	}
	fmt.Fprintln(w, "Next: loom setup doctor")
}

func RenderDoctor(w io.Writer, report DoctorReport, verbose bool) {
	fmt.Fprintf(w, "LOOM setup doctor: %d issue", len(report.Findings))
	if len(report.Findings) != 1 {
		fmt.Fprint(w, "s")
	}
	fmt.Fprintf(w, " (%s)\n", report.Summary)
	for _, finding := range report.Findings {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "%s %s\n", finding.Severity, finding.Code)
		fmt.Fprintf(w, "  %s\n", finding.Message)
		if finding.RepairHint != "" {
			fmt.Fprintf(w, "  Repair: %s\n", finding.RepairHint)
		}
		if verbose && len(finding.Evidence) > 0 {
			fmt.Fprintf(w, "  Evidence: %v\n", finding.Evidence)
		}
	}
	if len(report.Findings) == 0 {
		fmt.Fprintln(w, "No local setup issues found.")
	}
}

func RenderRepair(w io.Writer, result RepairResult) {
	fmt.Fprintln(w, "LOOM setup repair")
	if result.Refused {
		fmt.Fprintf(w, "refused: %s\n", result.Refusal)
		return
	}
	for _, change := range result.Changed {
		fmt.Fprintf(w, "%s %s\n", change.Status, change.RepairID)
		if change.Path != "" {
			fmt.Fprintf(w, "  %s\n", change.Path)
		}
		if change.Message != "" {
			fmt.Fprintf(w, "  %s\n", change.Message)
		}
	}
	for _, skipped := range result.Skipped {
		fmt.Fprintf(w, "skipped %s\n", skipped.RepairID)
		if skipped.Message != "" {
			fmt.Fprintf(w, "  %s\n", skipped.Message)
		}
	}
	if len(result.Changed) == 0 && len(result.Skipped) == 0 {
		fmt.Fprintln(w, "No repairs selected.")
	}
}

func RenderApply(w io.Writer, result ApplyResult) {
	fmt.Fprintln(w, "LOOM setup apply")
	fmt.Fprintln(w)
	if result.Refused {
		fmt.Fprintf(w, "refused: %s\n", result.Refusal)
		return
	}
	if len(result.Blocked) > 0 {
		fmt.Fprintln(w, "Blocked")
		for _, change := range result.Blocked {
			fmt.Fprintf(w, "  %s %s\n", firstNonEmpty(change.Category, "setup"), change.ID)
			if change.Path != "" {
				fmt.Fprintf(w, "    %s\n", change.Path)
			}
			if change.Message != "" {
				fmt.Fprintf(w, "    %s\n", change.Message)
			}
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, "Target")
	fmt.Fprintf(w, "  node: %s\n", result.Manifest.NodeKey)
	fmt.Fprintf(w, "  kind: %s\n", result.Manifest.NodeKind)
	fmt.Fprintf(w, "  runtime: %s\n", result.Manifest.RuntimeClass)
	fmt.Fprintf(w, "  plan: %s\n", result.PlanHash)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Changed")
	for _, change := range result.Changed {
		fmt.Fprintf(w, "  %s\t%s\t%s\n", compactStatus(change.Status), firstNonEmpty(change.Category, "setup"), firstNonEmpty(change.Path, change.Message, change.ID))
	}
	if len(result.Changed) == 0 {
		fmt.Fprintln(w, "  none")
	}
	if len(result.Skipped) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Already Satisfied / Skipped")
		for _, change := range result.Skipped {
			fmt.Fprintf(w, "  %s\t%s\t%s\n", compactStatus(change.Status), firstNonEmpty(change.Category, "setup"), firstNonEmpty(change.Path, change.Message, change.ID))
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Status")
	fmt.Fprintf(w, "  setup: %s\n", firstNonEmpty(result.Manifest.LastStatus.Status, result.Status.Summary.Status, SummaryUnknown))
	if result.Manifest.ProductionBootstrap.Checked {
		fmt.Fprintf(w, "  bootstrap: %t\n", result.Manifest.ProductionBootstrap.Ready)
	} else {
		fmt.Fprintln(w, "  bootstrap: not checked")
	}
	if result.ServiceEnv != "" {
		fmt.Fprintf(w, "  env: %s\n", result.ServiceEnv)
	}
	if result.Manifest.BoxPath != "" {
		fmt.Fprintf(w, "  Box: %s\n", result.Manifest.BoxPath)
	}
}

func RenderWorkspaceInstall(w io.Writer, result WorkspaceInstallResult) {
	fmt.Fprintln(w, "LOOM workspace install")
	fmt.Fprintln(w)
	if result.Refused {
		fmt.Fprintf(w, "refused: %s\n", result.Refusal)
		return
	}
	fmt.Fprintf(w, "Status: %s\n", firstNonEmpty(result.Status, "planned"))
	fmt.Fprintf(w, "Dry-run: %t\n", result.DryRun)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Workspace")
	fmt.Fprintf(w, "  node: %s\n", result.Plan.Setup.Spec.NodeKey)
	fmt.Fprintf(w, "  role: %s\n", result.Plan.Setup.Spec.NodeRole)
	fmt.Fprintf(w, "  runtime: %s\n", result.Plan.Setup.Spec.RuntimeClass)
	fmt.Fprintf(w, "  main: %s\n", firstNonEmpty(result.Plan.Setup.Spec.MainURL, "-"))
	fmt.Fprintf(w, "  Box: %s\n", firstNonEmpty(result.Plan.Setup.Paths.BoxPath, "-"))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Release")
	fmt.Fprintf(w, "  id: %s\n", result.Plan.Release.ReleaseID)
	fmt.Fprintf(w, "  path: %s\n", result.Plan.Release.ReleasePath)
	fmt.Fprintf(w, "  current: %s\n", result.Plan.Release.CurrentPath)
	fmt.Fprintf(w, "  local bin: %s\n", result.Plan.Release.LocalBinDir)
	if result.Plan.Release.Commit != "" {
		fmt.Fprintf(w, "  commit: %s\n", result.Plan.Release.Commit)
	}
	if len(result.Release.Blocked) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Release blocked")
		for _, change := range result.Release.Blocked {
			fmt.Fprintf(w, "  %s %s\n", change.ID, firstNonEmpty(change.Path, "-"))
			if change.Message != "" {
				fmt.Fprintf(w, "    %s\n", change.Message)
			}
		}
	}
	if len(result.Setup.Blocked) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Setup blocked")
		for _, change := range result.Setup.Blocked {
			fmt.Fprintf(w, "  %s %s\n", change.ID, firstNonEmpty(change.Path, change.Message, "-"))
		}
	}
	if result.Enrollment != nil {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Enrollment")
		fmt.Fprintf(w, "  status: %s\n", result.Enrollment.Status)
		if result.Enrollment.EnrollmentRequestID != "" {
			fmt.Fprintf(w, "  request: %s\n", result.Enrollment.EnrollmentRequestID)
		}
		if result.Enrollment.NodeID != "" {
			fmt.Fprintf(w, "  node: %s\n", result.Enrollment.NodeID)
		}
		if result.Enrollment.NodeCredentialID != "" {
			fmt.Fprintf(w, "  credential: %s\n", result.Enrollment.NodeCredentialID)
		}
		if result.Enrollment.CredentialHint != "" {
			fmt.Fprintf(w, "  credential hint: %s\n", result.Enrollment.CredentialHint)
		}
		if result.Enrollment.HeartbeatID != "" {
			fmt.Fprintf(w, "  heartbeat: %s\n", result.Enrollment.HeartbeatID)
		}
		fmt.Fprintf(w, "  verified on main: %t\n", result.Enrollment.VerifiedOnMain)
		if result.Enrollment.FailureCode != "" {
			fmt.Fprintf(w, "  failure: %s\n", result.Enrollment.FailureCode)
		}
		if result.Enrollment.FailureMessage != "" {
			fmt.Fprintf(w, "  message: %s\n", result.Enrollment.FailureMessage)
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Next")
	fmt.Fprintln(w, "  loom setup status")
	fmt.Fprintln(w, "  loom-node-agent --json status")
}

func compactStatus(status string) string {
	switch strings.TrimSpace(status) {
	case StepStatusAlreadySatisfied:
		return "ok"
	case StepStatusWouldChange:
		return "change"
	case StepStatusBlocked:
		return "blocked"
	case StepStatusSkipped:
		return "skip"
	default:
		return firstNonEmpty(status, "pending")
	}
}
