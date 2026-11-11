package loomcli

import (
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
	"loom.local/loom/internal/storagearchive"
)

func projectRegistrationSummary(detail projects.ProjectRegistrationDetail) map[string]any {
	p := detail.Project.Project
	out := map[string]any{
		"schema_version": "project.registration_summary.v1", "project_id": p.ProjectID,
		"slug": p.Slug, "status": p.Status,
		"facets": len(detail.Facets), "watched_roots": len(detail.WatchedRootRegistrations),
		"schedules": len(detail.ScheduleRegistrations), "workflows": len(detail.WorkflowRegistrations),
		"full_command": "loom project registration-status " + quoteDeclarationArg(p.ProjectID) + " --json",
	}
	if r := detail.Registration; r != nil {
		out["registration_id"], out["registration_revision"] = r.ProjectContractRegistrationID, r.RegistrationRevision
		out["registration_status"], out["activation_status"] = r.RegistrationStatus, r.ActivationStatus
		out["project_root"], out["contract_hash"] = r.ProjectRoot, r.ContractHash
	}
	return out
}

func projectWatchPlanSummary(plan projectwatch.ProjectWatchPlan, ref string) map[string]any {
	roots := make([]map[string]any, 0, min(24, len(plan.WatchedRoots)))
	for _, r := range plan.WatchedRoots[:min(24, len(plan.WatchedRoots))] {
		roots = append(roots, map[string]any{"key": r.Key, "backend_root_key": r.BackendRootKey,
			"worker_key": r.WorkerKey, "owner_node": r.OwnerNode, "root_relative_path": r.RootRelativePath,
			"config_hash": r.ConfigHash, "activation_status": r.ActivationStatus})
	}
	out := map[string]any{
		"schema_version": "project.watch_plan_summary.v1", "project_root": plan.ProjectRoot,
		"contract_hash": plan.ContractHash, "valid": plan.Report.OK,
		"diagnostics": plan.Report.Summary, "root_count": len(plan.WatchedRoots), "roots": roots,
		"roots_omitted": len(plan.WatchedRoots) - len(roots), "command_count": len(plan.Commands),
		"root_state_basis":    "compiled_intent_not_live_node_observation",
		"live_status_command": "loom project sync-status " + quoteDeclarationArg(ref),
		"full_command":        "loom project watch-plan " + quoteDeclarationArg(ref) + " --json",
		"usage":               "Inspection only. Root activation labels describe compiled intent, not current node ACKs. The full watch plan is required by loom-node-agent watched-roots apply-plan.",
	}
	if plan.Detail != nil {
		out["registration"] = projectRegistrationSummary(*plan.Detail)
	}
	return out
}

func projectArchiveInspectionSummary(result storagearchive.ProjectArchiveInspectResult) map[string]any {
	return map[string]any{
		"schema_version": "project.archive_inspection_summary.v1",
		"registration":   projectRegistrationSummary(result.Project), "physical": result.Physical,
		"historical_evidence": result.HistoricalEvidence,
		"warnings":            result.Warnings[:min(8, len(result.Warnings))], "warnings_omitted": max(0, len(result.Warnings)-8),
		"full_command": "loom project archive inspect " + quoteDeclarationArg(result.Project.Project.Project.ProjectID) + " --json",
	}
}
