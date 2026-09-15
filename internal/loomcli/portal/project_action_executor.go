package portal

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectdoctor"
	"loom.local/loom/internal/projectexport"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
	"loom.local/loom/internal/storagearchive"
)

func (e ActionExecutor) executeProjectInspect(ctx context.Context, action PortalAction) PortalActionResult {
	ref := firstNonEmpty(action.Executor.Target, action.TargetRef, action.Executor.Payload["project_ref"])
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if ref == "" {
		return failedActionResult(action, "portal.action_target_missing", "Project inspect action is missing a project ref.")
	}
	projectEnvelope, err := e.Client.GetProject(ctx, e.CorrelationID, ref)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	detailEnvelope, err := e.Client.GetProjectRegistrationStatus(ctx, e.CorrelationID, ref)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	project := projectEnvelope.Data.Project
	detail := detailEnvelope.Data
	reg := detailEnvelope.Data.Registration
	var backendAnalysis *projectdoctor.BackendAnalysisResult
	if analysisEnvelope, analysisErr := e.Client.AnalyzeProjectContractBackend(ctx, e.CorrelationID, projectcontracts.BackendAnalysisInput{ProjectRef: ref}); analysisErr == nil {
		analysis := analysisEnvelope.Data
		backendAnalysis = &analysis
	}
	surface := projectSurfaceStatus(ProjectsData{
		SelectedProjectRef: ref,
		SelectedProject:    projectEnvelope.Data,
		RegistrationDetail: detail,
	})
	fields := []ActionResultField{
		{Label: "Project", Value: projectLabel(project)},
		{Label: "Slug", Value: firstNonEmpty(project.Slug, "-")},
		{Label: "Owner Node", Value: firstNonEmpty(surface.BackendOwner, ptrOrDash(project.HomeNodeID))},
		{Label: "Lifecycle", Value: firstNonEmpty(surface.Lifecycle, "-")},
		{Label: "Registration", Value: firstNonEmpty(surface.Registration, "-")},
		{Label: "Activation", Value: firstNonEmpty(surface.Activation, "-")},
		{Label: "Facets", Value: projectFacetsSummary(surface.Facets)},
		{Label: "Contract Health", Value: firstNonEmpty(surface.ContractHealth, "-")},
		{Label: "Next Action", Value: firstNonEmpty(surface.NextAction, "-")},
		{Label: "Capabilities", Value: fmt.Sprintf("%d", len(detail.ScriptExposures))},
	}
	if reg != nil {
		fields = append(fields,
			ActionResultField{Label: "Root", Value: firstNonEmpty(reg.ProjectRoot, "-")},
			ActionResultField{Label: "Revision", Value: fmt.Sprintf("%d", reg.RegistrationRevision)},
		)
	}
	if layout, _, contractPath := projectResolvedLayout(detail, backendAnalysis); layout != "" {
		fields = append(fields,
			ActionResultField{Label: "Layout", Value: string(layout)},
			ActionResultField{Label: "Contract", Value: firstNonEmpty(contractPath, "-")},
		)
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "Project detail loaded.",
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(projectEnvelope.Meta.CorrelationID, detailEnvelope.Meta.CorrelationID, e.CorrelationID),
		RefreshScreen: action.RefreshScreen,
	}
}

func (e ActionExecutor) executeProjectValidateLocal(action PortalAction) PortalActionResult {
	root := firstNonEmpty(action.Executor.Payload["project_root"], action.RawDetails["project_root"])
	if root == "" {
		return failedActionResult(action, "project.validate_local_unavailable", "Project root is not available.")
	}
	analysis := projectcontracts.Analyze(root)
	if analysis.Loaded == nil {
		return failedActionResult(action, "project.validate_local_unavailable", "Project contract could not be loaded from "+root+".")
	}
	report := analysis.Report
	status := ActionLifecycleSucceeded
	summary := "Project contract is valid."
	if !report.OK || report.Summary.Errors > 0 {
		status = ActionLifecycleFailed
		summary = "Project contract validation failed."
	}
	return PortalActionResult{
		ActionID: action.ID,
		Title:    action.Label,
		Status:   status,
		Summary:  summary,
		Fields: []ActionResultField{
			{Label: "Project", Value: firstNonEmpty(report.Project.Slug, report.Project.Name, "-")},
			{Label: "Root", Value: root},
			{Label: "Registerable", Value: fmt.Sprintf("%t", report.Registerable)},
			{Label: "Errors", Value: fmt.Sprintf("%d", report.Summary.Errors)},
			{Label: "Warnings", Value: fmt.Sprintf("%d", report.Summary.Warnings)},
			{Label: "Infos", Value: fmt.Sprintf("%d", report.Summary.Infos)},
		},
		RawCommand:    append([]string{}, action.RawCommand...),
		RefreshScreen: action.RefreshScreen,
	}
}

func (e ActionExecutor) executeProjectValidateBackend(ctx context.Context, action PortalAction) PortalActionResult {
	ref := firstNonEmpty(action.Executor.Target, action.TargetRef, action.Executor.Payload["project_ref"])
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if ref == "" {
		return failedActionResult(action, "portal.action_target_missing", "Project validation action is missing a project ref.")
	}
	envelope, err := e.Client.AnalyzeProjectContractBackend(ctx, e.CorrelationID, projectcontracts.BackendAnalysisInput{
		ProjectRef:  ref,
		ProjectRoot: strings.TrimSpace(action.Executor.Payload["project_root"]),
	})
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	result := envelope.Data
	analysis := result.Analysis
	report := analysis.Report
	status := ActionLifecycleSucceeded
	summary := "Project contract is valid."
	if !report.OK || report.Summary.Errors > 0 {
		status = ActionLifecycleFailed
		summary = "Project contract validation failed."
	} else if !result.Current {
		summary = "Project contract is valid but registration needs attention."
	}
	root := strings.TrimSpace(action.Executor.Payload["project_root"])
	if analysis.Loaded != nil {
		root = firstNonEmpty(analysis.Loaded.RootPath, root)
	}
	return PortalActionResult{
		ActionID: action.ID,
		Title:    action.Label,
		Status:   status,
		Summary:  summary,
		Fields: []ActionResultField{
			{Label: "Project", Value: firstNonEmpty(report.Project.Slug, report.Project.Name, ref)},
			{Label: "Root", Value: firstNonEmpty(root, "-")},
			{Label: "Registerable", Value: fmt.Sprintf("%t", report.Registerable)},
			{Label: "Errors", Value: fmt.Sprintf("%d", report.Summary.Errors)},
			{Label: "Warnings", Value: fmt.Sprintf("%d", report.Summary.Warnings)},
			{Label: "Infos", Value: fmt.Sprintf("%d", report.Summary.Infos)},
			{Label: "Drift", Value: string(result.DriftStatus)},
			{Label: "Current", Value: fmt.Sprintf("%t", result.Current)},
			{Label: "Changed", Value: fmt.Sprintf("%d", result.Diff.Summary.Changed)},
			{Label: "Added", Value: fmt.Sprintf("%d", result.Diff.Summary.Added)},
			{Label: "Removed", Value: fmt.Sprintf("%d", result.Diff.Summary.Removed)},
			{Label: "Missing", Value: fmt.Sprintf("%d", result.Diff.Summary.Missing)},
			{Label: "Next Action", Value: firstNonEmpty(result.NextAction, "-")},
		},
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		RefreshScreen: action.RefreshScreen,
	}
}

func (e ActionExecutor) executeProjectRegisterBackend(ctx context.Context, action PortalAction) PortalActionResult {
	ref := firstNonEmpty(action.Executor.Target, action.TargetRef, action.Executor.Payload["project_ref"])
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if ref == "" {
		return failedActionResult(action, "portal.action_target_missing", "Project registration action is missing a project ref.")
	}
	strict := false
	if raw := strings.TrimSpace(action.InputValues["strict"]); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return failedActionResult(action, "portal.action_input_invalid", "Strict must be true or false.")
		}
		strict = parsed
	}
	envelope, err := e.Client.RegisterProjectContractFromBackend(ctx, e.CorrelationID, projects.RegisterProjectContractFromBackendInput{
		ProjectRef:  ref,
		ProjectRoot: strings.TrimSpace(action.Executor.Payload["project_root"]),
		Strict:      strict,
	})
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	result := envelope.Data
	summary := "Project contract registered."
	switch {
	case result.Updated:
		summary = "Project contract updated."
	case result.Unchanged:
		summary = "Project contract already matched the backend files."
	}
	root := ""
	revision := "-"
	status := "-"
	if result.Detail.Registration != nil {
		root = result.Detail.Registration.ProjectRoot
		revision = fmt.Sprintf("%d", result.Detail.Registration.RegistrationRevision)
		status = firstNonEmpty(result.Detail.Registration.RegistrationStatus, "-")
	}
	return PortalActionResult{
		ActionID: action.ID,
		Title:    action.Label,
		Status:   ActionLifecycleSucceeded,
		Summary:  summary,
		Fields: []ActionResultField{
			{Label: "Project", Value: projectLabel(result.Detail.Project.Project)},
			{Label: "Root", Value: firstNonEmpty(root, "-")},
			{Label: "Registration", Value: status},
			{Label: "Revision", Value: revision},
			{Label: "Created", Value: fmt.Sprintf("%t", result.Created)},
			{Label: "Updated", Value: fmt.Sprintf("%t", result.Updated)},
			{Label: "Unchanged", Value: fmt.Sprintf("%t", result.Unchanged)},
			{Label: "Strict", Value: fmt.Sprintf("%t", strict)},
		},
		RawCommand:     append([]string{}, action.RawCommand...),
		CorrelationID:  firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		IdempotencyKey: envelope.Meta.IdempotencyKey,
		RefreshScreen:  ScreenProjects,
	}
}

func (e ActionExecutor) executeProjectScaffoldCleanup(action PortalAction) PortalActionResult {
	root := firstNonEmpty(action.Executor.Payload["project_root"], action.RawDetails["project_root"])
	if root == "" {
		return failedActionResult(action, "project.scaffold_cleanup_unavailable", "Project root is not available.")
	}
	dryRun := true
	if raw := strings.TrimSpace(action.InputValues["dry_run"]); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return failedActionResult(action, "portal.action_input_invalid", "Dry Run must be true or false.")
		}
		dryRun = parsed
	}
	result, err := projectcontracts.CleanupScaffoldExamples(projectcontracts.ScaffoldCleanupOptions{ProjectRoot: root, DryRun: dryRun})
	if err != nil {
		return failedActionResult(action, "project.scaffold_cleanup_failed", err.Error())
	}
	removed, skipped, absent, planned := portalScaffoldCleanupActionCounts(result.Packages)
	summary := "Scaffold cleanup planned."
	if !dryRun {
		summary = "Scaffold cleanup completed."
	}
	fields := []ActionResultField{
		{Label: "Project", Value: firstNonEmpty(result.Slug, result.Name, "-")},
		{Label: "Root", Value: result.ProjectRoot},
		{Label: "Dry Run", Value: fmt.Sprintf("%t", result.DryRun)},
		{Label: "Removed", Value: fmt.Sprintf("%d", removed)},
		{Label: "Planned Remove", Value: fmt.Sprintf("%d", planned)},
		{Label: "Skipped", Value: fmt.Sprintf("%d", skipped)},
		{Label: "Absent", Value: fmt.Sprintf("%d", absent)},
		{Label: "Validation", Value: portalScaffoldValidationSummary(result.Validation)},
	}
	for _, skippedItem := range result.Skipped {
		fields = append(fields, ActionResultField{Label: "Skipped " + skippedItem.Path, Value: skippedItem.Reason})
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       summary,
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		RefreshScreen: action.RefreshScreen,
	}
}

func portalScaffoldCleanupActionCounts(packages []projectcontracts.ScaffoldCleanupPackage) (removed, skipped, absent, planned int) {
	for _, pkg := range packages {
		switch pkg.Action {
		case "removed":
			removed++
		case "skipped":
			skipped++
		case "absent":
			absent++
		case "planned_remove":
			planned++
		}
	}
	return removed, skipped, absent, planned
}

func portalScaffoldValidationSummary(summary projectcontracts.ScaffoldValidationSummary) string {
	switch summary.State {
	case projectcontracts.ScaffoldValidationPlannedOnly:
		return "planned_only"
	case projectcontracts.ScaffoldValidationNotRun:
		return "not_run"
	case projectcontracts.ScaffoldValidationPassed:
		return fmt.Sprintf("passed (%d errors, %d warnings)", summary.Errors, summary.Warnings)
	case projectcontracts.ScaffoldValidationFailed:
		return fmt.Sprintf("failed (%d errors, %d warnings)", summary.Errors, summary.Warnings)
	default:
		return firstNonEmpty(summary.State, "-")
	}
}

func (e ActionExecutor) executeProjectRegistrationPlan(ctx context.Context, action PortalAction) PortalActionResult {
	detail, envelope, err := e.projectRegistrationDetail(ctx, action)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	if detail.Registration == nil {
		return failedActionResult(action, "project.registration_missing", "Project has no registered contract.")
	}
	var plan projectcontracts.ProjectPlan
	if err := json.Unmarshal(detail.Registration.RegistrationPlan, &plan); err != nil {
		return failedActionResult(action, "project.registration_plan_unavailable", err.Error())
	}
	return PortalActionResult{
		ActionID: action.ID,
		Title:    action.Label,
		Status:   ActionLifecycleSucceeded,
		Summary:  "Project registration plan loaded.",
		Fields: []ActionResultField{
			{Label: "Project", Value: firstNonEmpty(plan.Project.Slug, projectLabel(detail.Project.Project))},
			{Label: "Registerable", Value: fmt.Sprintf("%t", plan.Registerable)},
			{Label: "Actions", Value: fmt.Sprintf("%d", len(plan.Actions))},
			{Label: "Facets", Value: fmt.Sprintf("%d", len(plan.Facets))},
			{Label: "Workflows", Value: fmt.Sprintf("%d", len(plan.Workflows))},
			{Label: "Unsupported", Value: fmt.Sprintf("%d", len(plan.UnsupportedFeatures))},
			{Label: "Errors", Value: fmt.Sprintf("%d", plan.Summary.Errors)},
			{Label: "Warnings", Value: fmt.Sprintf("%d", plan.Summary.Warnings)},
		},
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(envelope.CorrelationID, e.CorrelationID),
		RefreshScreen: action.RefreshScreen,
	}
}

func (e ActionExecutor) executeProjectDoctor(ctx context.Context, action PortalAction) PortalActionResult {
	ref := firstNonEmpty(action.Executor.Target, action.TargetRef, action.Executor.Payload["project_ref"])
	root := firstNonEmpty(action.Executor.Payload["project_root"], action.RawDetails["project_root"])
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	detailEnvelope, err := e.Client.GetProjectRegistrationStatus(ctx, e.CorrelationID, ref)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	var analysis *projectcontracts.Analysis
	var backendAnalysis *projectdoctor.BackendAnalysisResult
	if root != "" {
		local := projectcontracts.Analyze(root)
		analysis = &local
	} else if ref != "" {
		if envelope, err := e.Client.AnalyzeProjectContractBackend(ctx, e.CorrelationID, projectcontracts.BackendAnalysisInput{ProjectRef: ref}); err == nil {
			backend := envelope.Data
			backendAnalysis = &backend
			analysis = &backend.Analysis
		}
	}
	var syncStatus *projectwatch.ProjectSyncStatus
	if envelope, err := e.Client.GetProjectSyncStatus(ctx, e.CorrelationID, ref); err == nil {
		status := envelope.Data
		syncStatus = &status
	}
	var backupStatus *projectwatch.ProjectBackupStatus
	if envelope, err := e.Client.GetProjectBackupStatus(ctx, e.CorrelationID, ref); err == nil {
		status := envelope.Data
		backupStatus = &status
	}
	report := projectdoctor.Report{}
	if backendAnalysis != nil {
		report = backendAnalysis.Report
	} else {
		report = projectdoctor.BuildReport(projectdoctor.ReportInput{
			ProjectRef: ref,
			Source:     "portal.project.doctor",
			Local:      analysis,
			Detail:     &detailEnvelope.Data,
			Sync:       syncStatus,
			Backup:     backupStatus,
		})
	}
	status := ActionLifecycleSucceeded
	summary := "Project doctor completed."
	if projectdoctor.HasFailures(report) {
		status = ActionLifecycleFailed
		summary = "Project doctor found blocking checks."
	}
	return PortalActionResult{
		ActionID: action.ID,
		Title:    action.Label,
		Status:   status,
		Summary:  summary,
		Fields: []ActionResultField{
			{Label: "Project", Value: firstNonEmpty(report.ProjectRef, ref, "-")},
			{Label: "OK", Value: fmt.Sprintf("%d", report.Summary.OK)},
			{Label: "Warnings", Value: fmt.Sprintf("%d", report.Summary.Warnings)},
			{Label: "Errors", Value: fmt.Sprintf("%d", report.Summary.Errors)},
			{Label: "Blocked", Value: fmt.Sprintf("%d", report.Summary.Blocked)},
			{Label: "Unknown", Value: fmt.Sprintf("%d", report.Summary.Unknown)},
			{Label: "Skipped", Value: fmt.Sprintf("%d", report.Summary.Skipped)},
			{Label: "Drift", Value: projectDoctorDriftField(backendAnalysis)},
			{Label: "Next Action", Value: projectDoctorNextActionField(backendAnalysis)},
		},
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(detailEnvelope.Meta.CorrelationID, e.CorrelationID),
		RefreshScreen: action.RefreshScreen,
	}
}

func (e ActionExecutor) executeProjectDiff(ctx context.Context, action PortalAction) PortalActionResult {
	ref := firstNonEmpty(action.Executor.Target, action.TargetRef, action.Executor.Payload["project_ref"])
	root := firstNonEmpty(action.Executor.Payload["project_root"], action.RawDetails["project_root"])
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	var report projectdoctor.DiffReport
	correlationID := e.CorrelationID
	if root == "" {
		if ref == "" {
			return failedActionResult(action, "project.diff_unavailable", "Project ref or project root is required.")
		}
		envelope, err := e.Client.AnalyzeProjectContractBackend(ctx, e.CorrelationID, projectcontracts.BackendAnalysisInput{ProjectRef: ref})
		if err != nil {
			return failedActionResult(action, "portal.action_failed", err.Error())
		}
		report = envelope.Data.Diff
		correlationID = firstNonEmpty(envelope.Meta.CorrelationID, correlationID)
	} else {
		analysis := projectcontracts.Analyze(root)
		if analysis.Loaded == nil {
			return failedActionResult(action, "project.diff_unavailable", "Project contract could not be loaded from "+root+".")
		}
		detailEnvelope, err := e.Client.GetProjectRegistrationStatus(ctx, e.CorrelationID, ref)
		if err != nil {
			return failedActionResult(action, "portal.action_failed", err.Error())
		}
		report = projectdoctor.BuildDiff(analysis, &detailEnvelope.Data)
		correlationID = firstNonEmpty(detailEnvelope.Meta.CorrelationID, correlationID)
	}
	return PortalActionResult{
		ActionID: action.ID,
		Title:    action.Label,
		Status:   ActionLifecycleSucceeded,
		Summary:  "Project diff loaded.",
		Fields: []ActionResultField{
			{Label: "Project", Value: firstNonEmpty(report.ProjectRef, ref, "-")},
			{Label: "Unchanged", Value: fmt.Sprintf("%d", report.Summary.Unchanged)},
			{Label: "Added", Value: fmt.Sprintf("%d", report.Summary.Added)},
			{Label: "Removed", Value: fmt.Sprintf("%d", report.Summary.Removed)},
			{Label: "Changed", Value: fmt.Sprintf("%d", report.Summary.Changed)},
			{Label: "Missing", Value: fmt.Sprintf("%d", report.Summary.Missing)},
		},
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: correlationID,
		RefreshScreen: action.RefreshScreen,
	}
}

func projectDoctorDriftField(result *projectdoctor.BackendAnalysisResult) string {
	if result == nil {
		return "not_checked"
	}
	return string(result.DriftStatus)
}

func projectDoctorNextActionField(result *projectdoctor.BackendAnalysisResult) string {
	if result == nil {
		return "-"
	}
	return firstNonEmpty(result.NextAction, "-")
}

func (e ActionExecutor) executeProjectActivate(ctx context.Context, action PortalAction) PortalActionResult {
	ref := firstNonEmpty(action.Executor.Target, action.TargetRef, action.Executor.Payload["project_ref"])
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if ref == "" {
		return failedActionResult(action, "portal.action_target_missing", "Project activation action is missing a project ref.")
	}
	projectRoot, rootErr := normalizeProjectActionRoot(action.Executor.Payload["project_root"])
	if rootErr != nil {
		return failedActionResult(action, "project.root_invalid", "Project root override is invalid: "+rootErr.Error())
	}
	envelope, err := e.Client.ActivateProject(ctx, e.CorrelationID, ref, projects.ActivateProjectInput{
		Facet:       action.Executor.Payload["facet"],
		ProjectRoot: projectRoot,
	})
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	detail := envelope.Data
	fields := []ActionResultField{
		{Label: "Project", Value: projectLabel(detail.Project.Project)},
		{Label: "Activation", Value: projectRegistrationState(detail)},
		{Label: "Facet", Value: firstNonEmpty(action.Executor.Payload["facet"], "base")},
		{Label: "Facets", Value: fmt.Sprintf("%d", len(detail.Facets))},
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "Project activation completed.",
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		RefreshScreen: ScreenProjects,
	}
}

func (e ActionExecutor) executeProjectDeactivate(ctx context.Context, action PortalAction) PortalActionResult {
	ref := firstNonEmpty(action.Executor.Target, action.TargetRef, action.Executor.Payload["project_ref"])
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if ref == "" {
		return failedActionResult(action, "portal.action_target_missing", "Project deactivation action is missing a project ref.")
	}
	envelope, err := e.Client.DeactivateProject(ctx, e.CorrelationID, ref, projects.DeactivateProjectInput{
		Facet: action.Executor.Payload["facet"],
	})
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	result := envelope.Data
	fields := []ActionResultField{
		{Label: "Project", Value: projectLabel(result.Detail.Project.Project)},
		{Label: "Facet", Value: firstNonEmpty(result.Facet, action.Executor.Payload["facet"], "-")},
		{Label: "Changed", Value: fmt.Sprintf("%t", result.Changed)},
		{Label: "Actions", Value: fmt.Sprintf("%d", len(result.Actions))},
		{Label: "Warnings", Value: fmt.Sprintf("%d", len(result.Warnings))},
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "Project deactivation completed.",
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		RefreshScreen: ScreenProjects,
	}
}

func (e ActionExecutor) executeProjectArchive(ctx context.Context, action PortalAction) PortalActionResult {
	if action.Executor.Payload["physical_archive"] == "true" {
		return e.executeProjectPhysicalMove(ctx, action, false)
	}
	ref := firstNonEmpty(action.Executor.Target, action.TargetRef, action.Executor.Payload["project_ref"])
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if ref == "" {
		return failedActionResult(action, "portal.action_target_missing", "Project archive action is missing a project ref.")
	}
	dryRun := true
	if raw := strings.TrimSpace(action.InputValues["dry_run"]); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return failedActionResult(action, "portal.action_input_invalid", "Dry Run must be true or false.")
		}
		dryRun = parsed
	}
	skipStorage := false
	if raw := strings.TrimSpace(action.InputValues["skip_storage_archive"]); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return failedActionResult(action, "portal.action_input_invalid", "Skip Storage Archive must be true or false.")
		}
		skipStorage = parsed
	}
	input := storagearchive.ProjectArchiveInput{
		SourceRef:          strings.TrimSpace(action.InputValues["source_ref"]),
		TargetPath:         strings.TrimSpace(action.InputValues["target_path"]),
		Reason:             strings.TrimSpace(action.InputValues["reason"]),
		SkipStorageArchive: skipStorage,
		DryRun:             dryRun,
	}
	envelope, err := e.Client.ArchiveProject(ctx, e.CorrelationID, ref, input)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	result := envelope.Data
	summary := "Project archive planned."
	if !result.DryRun {
		summary = "Project archived."
	}
	return PortalActionResult{
		ActionID: action.ID,
		Title:    action.Label,
		Status:   ActionLifecycleSucceeded,
		Summary:  summary,
		Fields: []ActionResultField{
			{Label: "Project", Value: projectLabel(result.Project.Project.Project)},
			{Label: "Dry Run", Value: fmt.Sprintf("%t", result.DryRun)},
			{Label: "Storage Archive Skipped", Value: fmt.Sprintf("%t", result.StorageArchiveSkipped)},
			{Label: "Runtime Archive", Value: firstNonEmpty(result.RuntimeManifest.ProjectRuntimeArchiveID, "-")},
			{Label: "Runtime Manifest", Value: firstNonEmpty(result.RuntimeManifestPath, "-")},
			{Label: "Source", Value: firstNonEmpty(result.RuntimeManifest.SourceRef, input.SourceRef, "-")},
			{Label: "Target", Value: firstNonEmpty(result.RuntimeManifest.TargetPath, input.TargetPath, "-")},
			{Label: "Deactivations", Value: fmt.Sprintf("%d", len(result.Deactivations))},
			{Label: "Warnings", Value: fmt.Sprintf("%d", len(result.Warnings))},
			{Label: "Safe To Delete", Value: fmt.Sprintf("%t", result.SafeToDelete)},
		},
		RawCommand:     append([]string{}, action.RawCommand...),
		CorrelationID:  firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		IdempotencyKey: envelope.Meta.IdempotencyKey,
		RefreshScreen:  ScreenProjects,
	}
}

func (e ActionExecutor) executeProjectArchiveInspect(ctx context.Context, action PortalAction) PortalActionResult {
	if action.InputValues["physical_recovery"] != "" {
		return e.executeProjectPhysicalRecovery(ctx, action)
	}
	ref := firstNonEmpty(action.Executor.Target, action.TargetRef, action.Executor.Payload["project_ref"])
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if ref == "" {
		return failedActionResult(action, "portal.action_target_missing", "Project archive inspect action is missing a project ref.")
	}
	envelope, err := e.Client.InspectProjectArchive(ctx, e.CorrelationID, ref)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	result := envelope.Data
	if physical := result.Physical; physical != nil {
		out := PortalActionResult{
			ActionID: action.ID, Title: action.Label, Status: ActionLifecycleSucceeded,
			Summary: "Project physical archive evidence inspected; runtime remains guarded.",
			Fields: []ActionResultField{
				{Label: "Project", Value: physical.ProjectID},
				{Label: "Operation", Value: physical.OperationID},
				{Label: "Plan digest", Value: physical.PlanDigest},
				{Label: "Phase", Value: physical.Phase},
				{Label: "Evidence", Value: physical.EvidenceStatus},
				{Label: "Runtime", Value: firstNonEmpty(string(physical.ActivationState), "not yet verified inactive")},
				{Label: "Mutation Blocked", Value: fmt.Sprintf("%t", physical.MutationBlocked)},
				{Label: "Next", Value: physical.NextAction},
			},
			RawCommand:    append([]string{}, action.RawCommand...),
			CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID), RefreshScreen: action.RefreshScreen,
		}
		if (physical.NextAction == "recover_archive" || physical.NextAction == "recover_restore") && (physical.EvidenceStatus == "verified" || physical.EvidenceStatus == "pending") {
			out.Status = ActionLifecycleReview
			out.Summary = "Review the retained operation before explicit recovery; no activation."
			out.NextInput = map[string]string{"physical_recovery": physical.NextAction, "operation_id": physical.OperationID, "plan_digest": physical.PlanDigest}
		}
		return out
	}
	fields := []ActionResultField{
		{Label: "Project", Value: projectLabel(result.Project.Project.Project)},
		{Label: "Runtime Manifest", Value: firstNonEmpty(result.RuntimeManifestPath, "-")},
		{Label: "Warnings", Value: fmt.Sprintf("%d", len(result.Warnings))},
	}
	if state, ok := storagearchive.ParseProjectRuntimeArchiveState(result.ArchiveState); ok {
		fields = append(fields,
			ActionResultField{Label: "Archive Status", Value: firstNonEmpty(state.Status, "-")},
			ActionResultField{Label: "Runtime Archive", Value: firstNonEmpty(state.ProjectRuntimeArchiveID, "-")},
			ActionResultField{Label: "Source", Value: firstNonEmpty(state.SourceRef, "-")},
			ActionResultField{Label: "Target", Value: firstNonEmpty(state.TargetPath, "-")},
			ActionResultField{Label: "Safe To Delete", Value: fmt.Sprintf("%t", state.SafeToDelete)},
		)
	}
	if result.RuntimeManifest != nil {
		fields = append(fields,
			ActionResultField{Label: "Scripts", Value: fmt.Sprintf("%d", len(result.RuntimeManifest.Scripts))},
			ActionResultField{Label: "Workflows", Value: fmt.Sprintf("%d", len(result.RuntimeManifest.Workflows))},
			ActionResultField{Label: "Connectors", Value: fmt.Sprintf("%d", len(result.RuntimeManifest.Connectors))},
			ActionResultField{Label: "Schedules", Value: fmt.Sprintf("%d", len(result.RuntimeManifest.Schedules))},
			ActionResultField{Label: "Direct Events", Value: fmt.Sprintf("%d", len(result.RuntimeManifest.DirectEvents))},
			ActionResultField{Label: "Watched Roots", Value: fmt.Sprintf("%d", len(result.RuntimeManifest.WatchedRoots))},
			ActionResultField{Label: "Modules", Value: fmt.Sprintf("%d", len(result.RuntimeManifest.Modules))},
			ActionResultField{Label: "Runtime Migration", Value: firstNonEmpty(result.RuntimeManifest.SuccessorPolicy.Status, "-")},
		)
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "Project archive inspected.",
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		RefreshScreen: action.RefreshScreen,
	}
}

func (e ActionExecutor) executeProjectArchiveRestore(ctx context.Context, action PortalAction) PortalActionResult {
	if action.Executor.Payload["physical_archive"] == "true" {
		return e.executeProjectPhysicalMove(ctx, action, true)
	}
	ref := firstNonEmpty(action.Executor.Target, action.TargetRef, action.Executor.Payload["project_ref"])
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if ref == "" {
		return failedActionResult(action, "portal.action_target_missing", "Project archive restore action is missing a project ref.")
	}
	if raw := strings.TrimSpace(action.InputValues["dry_run"]); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return failedActionResult(action, "portal.action_input_invalid", "Dry Run must be true or false.")
		}
		if !parsed {
			return failedActionResult(action, "portal.action_input_invalid", "Project archive restore is dry-run only in this slice.")
		}
	}
	input := storagearchive.ProjectArchiveRestoreInput{
		ToNode: strings.TrimSpace(action.InputValues["to_node"]),
		DryRun: true,
	}
	envelope, err := e.Client.PlanProjectArchiveRestore(ctx, e.CorrelationID, ref, input)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	result := envelope.Data
	fields := []ActionResultField{
		{Label: "Project", Value: projectLabel(result.Project.Project.Project)},
		{Label: "Dry Run", Value: fmt.Sprintf("%t", result.DryRun)},
		{Label: "To Node", Value: firstNonEmpty(input.ToNode, "original owner node")},
		{Label: "Steps", Value: fmt.Sprintf("%d", len(result.Steps))},
		{Label: "Warnings", Value: fmt.Sprintf("%d", len(result.Warnings))},
	}
	for _, step := range result.Steps {
		fields = append(fields, ActionResultField{Label: "Step " + firstNonEmpty(step.Key, step.Kind), Value: firstNonEmpty(step.Status, "-")})
	}
	if state, ok := storagearchive.ParseProjectRuntimeArchiveState(result.ArchiveState); ok {
		fields = append(fields,
			ActionResultField{Label: "Archive Status", Value: firstNonEmpty(state.Status, "-")},
			ActionResultField{Label: "Source", Value: firstNonEmpty(state.SourceRef, "-")},
			ActionResultField{Label: "Target", Value: firstNonEmpty(state.TargetPath, "-")},
		)
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "Project archive restore plan generated.",
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		RefreshScreen: action.RefreshScreen,
	}
}

func (e ActionExecutor) executeProjectAddFacet(ctx context.Context, action PortalAction) PortalActionResult {
	ref := firstNonEmpty(action.Executor.Target, action.TargetRef, action.Executor.Payload["project_ref"])
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if ref == "" {
		return failedActionResult(action, "portal.action_target_missing", "Project facet add action is missing a project ref.")
	}
	facets := splitCommaField(action.InputValues["facets"])
	if len(facets) == 0 {
		return failedActionResult(action, "portal.action_input_missing", "At least one facet is required.")
	}
	dryRun := true
	if raw := strings.TrimSpace(action.InputValues["dry_run"]); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return failedActionResult(action, "portal.action_input_invalid", "Dry Run must be true or false.")
		}
		dryRun = parsed
	}
	force := false
	if raw := strings.TrimSpace(action.InputValues["force"]); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return failedActionResult(action, "portal.action_input_invalid", "Force must be true or false.")
		}
		force = parsed
	}
	registerAfterApply := true
	if raw := strings.TrimSpace(action.InputValues["register_after_apply"]); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return failedActionResult(action, "portal.action_input_invalid", "Register After Apply must be true or false.")
		}
		registerAfterApply = parsed
	}
	input := projectcontracts.AddProjectFacetsOptions{
		ProjectRef:  ref,
		ProjectRoot: strings.TrimSpace(action.Executor.Payload["project_root"]),
		Facets:      facets,
		DryRun:      dryRun,
		Force:       force,
	}
	envelope, err := e.Client.AddProjectFacets(ctx, e.CorrelationID, input)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	result := envelope.Data
	requested := strings.Join(result.RequestedFacets, ", ")
	if requested == "" {
		requested = strings.Join(facets, ", ")
	}
	added := strings.Join(result.AddedFacets, ", ")
	if added == "" {
		added = "-"
	}
	existing := strings.Join(result.ExistingFacets, ", ")
	if existing == "" {
		existing = "-"
	}
	allFacets := strings.Join(result.Facets, ", ")
	if allFacets == "" {
		allFacets = "-"
	}
	summary := "Project facet addition completed."
	if result.DryRun || dryRun {
		summary = "Project facet addition planned."
	}
	fields := []ActionResultField{
		{Label: "Project", Value: firstNonEmpty(result.Slug, ref)},
		{Label: "Path", Value: firstNonEmpty(result.ProjectRoot, input.ProjectRoot, "-")},
		{Label: "Requested", Value: requested},
		{Label: "Added", Value: added},
		{Label: "Already Enabled", Value: existing},
		{Label: "Facets", Value: allFacets},
		{Label: "Dry Run", Value: fmt.Sprintf("%t", result.DryRun || dryRun)},
		{Label: "Force", Value: fmt.Sprintf("%t", force)},
		{Label: "Validation", Value: portalScaffoldValidationSummary(result.Validation)},
		{Label: "Files", Value: strconv.Itoa(len(result.Files))},
	}
	if !dryRun && !result.DryRun && registerAfterApply {
		registerEnvelope, err := e.Client.RegisterProjectContractFromBackend(ctx, e.CorrelationID, projects.RegisterProjectContractFromBackendInput{
			ProjectRef:  firstNonEmpty(result.Slug, ref),
			ProjectRoot: result.ProjectRoot,
		})
		if err != nil {
			fields = append(fields,
				ActionResultField{Label: "Registration", Value: "failed"},
				ActionResultField{Label: "Registration Error", Value: err.Error()},
				ActionResultField{Label: "Next Steps", Value: "Run Validate Contract, then Re-register Contract from Projects."},
			)
			return PortalActionResult{
				ActionID:      action.ID,
				Title:         action.Label,
				Status:        ActionLifecycleFailed,
				Summary:       "Project facets were added, but backend registration failed.",
				Fields:        fields,
				RawCommand:    append([]string{}, action.RawCommand...),
				CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
				RefreshScreen: ScreenProjects,
			}
		}
		registration := registerEnvelope.Data
		registrationStatus := "-"
		registrationRevision := "-"
		registrationRoot := result.ProjectRoot
		if registration.Detail.Registration != nil {
			registrationStatus = firstNonEmpty(registration.Detail.Registration.RegistrationStatus, "-")
			registrationRevision = strconv.Itoa(registration.Detail.Registration.RegistrationRevision)
			registrationRoot = firstNonEmpty(registration.Detail.Registration.ProjectRoot, registrationRoot)
		}
		summary = "Project facets added and contract registered."
		fields = append(fields,
			ActionResultField{Label: "Registration", Value: registrationStatus},
			ActionResultField{Label: "Revision", Value: registrationRevision},
			ActionResultField{Label: "Registered Root", Value: firstNonEmpty(registrationRoot, "-")},
			ActionResultField{Label: "Created", Value: fmt.Sprintf("%t", registration.Created)},
			ActionResultField{Label: "Updated", Value: fmt.Sprintf("%t", registration.Updated)},
			ActionResultField{Label: "Unchanged", Value: fmt.Sprintf("%t", registration.Unchanged)},
			ActionResultField{Label: "Next Steps", Value: "Refresh Projects, then activate the newly added facets if needed."},
		)
	} else if dryRun || result.DryRun {
		fields = append(fields,
			ActionResultField{Label: "Registration", Value: "skipped_dry_run"},
			ActionResultField{Label: "Next Steps", Value: "If the dry run looks correct, rerun with Dry Run disabled."},
		)
	} else {
		fields = append(fields,
			ActionResultField{Label: "Registration", Value: "not_requested"},
			ActionResultField{Label: "Next Steps", Value: "Run Re-register Contract from Projects to update the backend snapshot."},
		)
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       summary,
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		RefreshScreen: ScreenProjects,
	}
}

func (e ActionExecutor) executeProjectMigrateLayout(ctx context.Context, action PortalAction) PortalActionResult {
	ref := firstNonEmpty(action.Executor.Target, action.TargetRef, action.Executor.Payload["project_ref"])
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if ref == "" {
		return failedActionResult(action, "portal.action_target_missing", "Project layout migration action is missing a project ref.")
	}
	dryRun := true
	if raw := strings.TrimSpace(action.InputValues["dry_run"]); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return failedActionResult(action, "portal.action_input_invalid", "Dry Run must be true or false.")
		}
		dryRun = parsed
	}
	input := projectcontracts.LayoutMigrationOptions{
		ProjectRef:  ref,
		ProjectRoot: strings.TrimSpace(action.Executor.Payload["project_root"]),
		Apply:       !dryRun,
		Yes:         !dryRun,
	}
	envelope, err := e.Client.MigrateProjectLayout(ctx, e.CorrelationID, input)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	result := envelope.Data
	status := ActionLifecycleSucceeded
	summary := "Project layout migration planned."
	if result.Applied {
		summary = "Project layout migrated to the canonical project-local .loom layout."
	}
	if !result.OK {
		status = ActionLifecycleFailed
		summary = "Project layout migration is blocked."
	}
	layout := string(result.BeforeLayout)
	if result.AfterLayout != "" {
		layout += " -> " + string(result.AfterLayout)
	}
	fields := []ActionResultField{
		{Label: "Project", Value: ref},
		{Label: "Root", Value: firstNonEmpty(result.ProjectRoot, input.ProjectRoot, "-")},
		{Label: "Layout", Value: firstNonEmpty(layout, "-")},
		{Label: "Dry Run", Value: fmt.Sprintf("%t", result.DryRun || dryRun)},
		{Label: "Actions", Value: strconv.Itoa(len(result.Actions))},
		{Label: "Collisions", Value: strconv.Itoa(len(result.Collisions))},
		{Label: "Preserved", Value: strconv.Itoa(len(result.Skips))},
	}
	if result.RecordPath != "" {
		fields = append(fields, ActionResultField{Label: "Record", Value: result.RecordPath})
	}
	if len(result.NextActions) > 0 {
		fields = append(fields, ActionResultField{Label: "Next Steps", Value: strings.Join(result.NextActions, "; ")})
	} else if result.Applied {
		fields = append(fields, ActionResultField{Label: "Next Steps", Value: "Validate, inspect drift, and re-register the backend contract."})
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        status,
		Summary:       summary,
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		RefreshScreen: ScreenProjects,
	}
}

func (e ActionExecutor) executeProjectFacetInspect(action PortalAction) PortalActionResult {
	payload := action.Executor.Payload
	return PortalActionResult{
		ActionID: action.ID,
		Title:    action.Label,
		Status:   ActionLifecycleSucceeded,
		Summary:  "Project facet detail loaded.",
		Fields: []ActionResultField{
			{Label: "Project", Value: firstNonEmpty(payload["project_ref"], "-")},
			{Label: "Facet", Value: firstNonEmpty(payload["facet"], "-")},
			{Label: "Status", Value: firstNonEmpty(payload["status"], "-")},
			{Label: "Folder", Value: firstNonEmpty(payload["folder"], "-")},
			{Label: "Enabled", Value: firstNonEmpty(payload["enabled"], "-")},
			{Label: "Present", Value: firstNonEmpty(payload["present"], "-")},
			{Label: "Placeholder", Value: firstNonEmpty(payload["placeholder"], "-")},
		},
		RawCommand:    append([]string{}, action.RawCommand...),
		RefreshScreen: action.RefreshScreen,
	}
}

func (e ActionExecutor) executeProjectWatchPlan(ctx context.Context, action PortalAction) PortalActionResult {
	ref := firstNonEmpty(action.Executor.Target, action.TargetRef, action.Executor.Payload["project_ref"])
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	envelope, err := e.Client.BuildProjectWatchPlan(ctx, e.CorrelationID, ref, projectwatch.BuildPlanInput{UseRegisteredSnapshot: true})
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	plan := envelope.Data
	return PortalActionResult{
		ActionID: action.ID,
		Title:    action.Label,
		Status:   ActionLifecycleSucceeded,
		Summary:  "Project watch plan loaded.",
		Fields: []ActionResultField{
			{Label: "Project", Value: ref},
			{Label: "Desired Roots", Value: fmt.Sprintf("%d", len(plan.WatchedRoots))},
			{Label: "Agent Commands", Value: fmt.Sprintf("%d", len(plan.Commands))},
			{Label: "Errors", Value: fmt.Sprintf("%d", plan.Report.Summary.Errors)},
			{Label: "Warnings", Value: fmt.Sprintf("%d", plan.Report.Summary.Warnings)},
		},
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		RefreshScreen: action.RefreshScreen,
	}
}

func (e ActionExecutor) executeProjectSyncStatus(ctx context.Context, action PortalAction) PortalActionResult {
	ref := firstNonEmpty(action.Executor.Target, action.TargetRef, action.Executor.Payload["project_ref"])
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	envelope, err := e.Client.GetProjectSyncStatus(ctx, e.CorrelationID, ref)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	status := envelope.Data
	return PortalActionResult{
		ActionID: action.ID,
		Title:    action.Label,
		Status:   ActionLifecycleSucceeded,
		Summary:  "Project sync status loaded.",
		Fields: []ActionResultField{
			{Label: "Project", Value: status.ProjectRef},
			{Label: "Sync Roots", Value: fmt.Sprintf("%d", status.SyncRoots)},
			{Label: "Pending Agent Apply", Value: fmt.Sprintf("%d", status.PendingAgentApply)},
			{Label: "Reported", Value: fmt.Sprintf("%d", status.Reported)},
			{Label: "Stale", Value: fmt.Sprintf("%d", status.Stale)},
			{Label: "Blocked", Value: fmt.Sprintf("%d", status.Blocked)},
		},
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		RefreshScreen: action.RefreshScreen,
	}
}

func (e ActionExecutor) executeProjectBackupStatus(ctx context.Context, action PortalAction) PortalActionResult {
	ref := firstNonEmpty(action.Executor.Target, action.TargetRef, action.Executor.Payload["project_ref"])
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	envelope, err := e.Client.GetProjectBackupStatus(ctx, e.CorrelationID, ref)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	status := envelope.Data
	protected, ignored, countsAvailable := projectBackupProtectionCounts(status)
	protectedValue := "-"
	ignoredValue := "-"
	if countsAvailable {
		protectedValue = strconv.Itoa(protected)
		ignoredValue = strconv.Itoa(ignored)
	}
	return PortalActionResult{
		ActionID: action.ID,
		Title:    action.Label,
		Status:   ActionLifecycleSucceeded,
		Summary:  "Project backup status loaded.",
		Fields: []ActionResultField{
			{Label: "Project", Value: status.ProjectRef},
			{Label: "Backup Roots", Value: fmt.Sprintf("%d", status.BackupRoots)},
			{Label: "Pending Agent Apply", Value: fmt.Sprintf("%d", status.PendingAgentApply)},
			{Label: "Reported", Value: fmt.Sprintf("%d", status.Reported)},
			{Label: "Stale", Value: fmt.Sprintf("%d", status.Stale)},
			{Label: "Blocked", Value: fmt.Sprintf("%d", status.Blocked)},
			{Label: "Protected", Value: protectedValue},
			{Label: "Ignored", Value: ignoredValue},
		},
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		RefreshScreen: action.RefreshScreen,
	}
}

type portalProjectExportClient interface {
	ExportProject(context.Context, string, projectexport.Request, string, bool) (projectexport.Summary, error)
}

func (e ActionExecutor) executeProjectExport(ctx context.Context, action PortalAction, backend bool) PortalActionResult {
	mode, err := projectexport.ParseMode(action.Executor.Payload["mode"])
	if err != nil {
		return failedActionResult(action, "portal.project_export_mode_invalid", err.Error())
	}
	outputPath := strings.TrimSpace(action.InputValues["output_path"])
	if outputPath == "" {
		return failedActionResult(action, "portal.project_export_output_required", "Project export requires an output tar path.")
	}
	overwrite := boolActionInput(action, "overwrite", false)
	var summary projectexport.Summary
	if backend {
		client, ok := e.Client.(portalProjectExportClient)
		if !ok {
			return failedActionResult(action, "portal.project_export_client_unavailable", "The configured Portal client cannot download project exports.")
		}
		ref := firstNonEmpty(action.Executor.Payload["project_ref"], action.Executor.Target, action.TargetRef)
		if ref == "" {
			return failedActionResult(action, "portal.action_target_missing", "Backend project export is missing a project ref.")
		}
		summary, err = client.ExportProject(ctx, e.CorrelationID, projectexport.Request{ProjectRef: ref, Mode: mode}, outputPath, overwrite)
	} else {
		root := firstNonEmpty(action.Executor.Payload["project_root"], action.Executor.Target, action.TargetRef)
		if !localProjectRootReadable(root) {
			return failedActionResult(action, "portal.project_export_local_unavailable", "The registered project root is not readable from this machine.")
		}
		summary, err = projectexport.ExportToFile(ctx, root, outputPath, overwrite, projectexport.PlanOptions{Mode: mode})
	}
	if err != nil {
		return failedActionResult(action, "portal.project_export_failed", err.Error())
	}
	return PortalActionResult{
		ActionID: action.ID,
		Title:    action.Label,
		Status:   ActionLifecycleSucceeded,
		Summary:  "Project export was written to the caller-local output path.",
		Fields: []ActionResultField{
			{Label: "Project", Value: firstNonEmpty(summary.ProjectSlug, action.Executor.Payload["project_ref"], "-")},
			{Label: "Mode", Value: string(summary.Mode)},
			{Label: "Output", Value: summary.OutputPath},
			{Label: "Included", Value: fmt.Sprintf("%d files / %s", summary.Included.Count, storageFormatBytes(summary.Included.Bytes))},
			{Label: "Ignored", Value: fmt.Sprintf("%d files / %s", summary.Ignored.Count, storageFormatBytes(summary.Ignored.Bytes))},
			{Label: "Policy Version", Value: summary.PolicyVersion},
			{Label: "Policy Files", Value: strconv.Itoa(len(summary.PolicyHashes))},
			{Label: "Archive", Value: storageFormatBytes(summary.ArchiveBytes)},
			{Label: "Checksum", Value: summary.ArchiveChecksum},
		},
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: e.CorrelationID,
		RefreshScreen: action.RefreshScreen,
	}
}

func projectBackupProtectionCounts(status projectwatch.ProjectBackupStatus) (protected int, ignored int, ok bool) {
	for _, backup := range status.Backups {
		if included, excluded, available := watchedRootProtectionCounts(backup.Root.SummaryJSON); available {
			protected += included
			ignored += excluded
			ok = true
		}
	}
	return protected, ignored, ok
}

func (e ActionExecutor) executeRuntimeBindingInspect(ctx context.Context, action PortalAction) PortalActionResult {
	ref := firstNonEmpty(action.Executor.Target, action.TargetRef, action.Executor.Payload["runtime_binding_id"])
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if ref == "" {
		return failedActionResult(action, "portal.action_target_missing", "Runtime binding inspect action is missing a binding ref.")
	}
	envelope, err := e.Client.GetRuntimeBinding(ctx, e.CorrelationID, ref)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	binding := envelope.Data
	return PortalActionResult{
		ActionID: action.ID,
		Title:    action.Label,
		Status:   ActionLifecycleSucceeded,
		Summary:  "Runtime binding loaded.",
		Fields: []ActionResultField{
			{Label: "Binding", Value: firstNonEmpty(binding.Binding.RuntimeBindingID, ref)},
			{Label: "Capability", Value: firstNonEmpty(binding.Endpoint.CompactAddress, binding.Endpoint.CapabilityEndpointID)},
			{Label: "Provider", Value: firstNonEmpty(binding.Provider.CompactAddress, binding.Provider.ProviderID)},
			{Label: "Runtime Kind", Value: firstNonEmpty(binding.Binding.RuntimeKind, "-")},
			{Label: "Status", Value: firstNonEmpty(binding.Binding.Status, "-")},
			{Label: "Version", Value: firstNonEmpty(binding.EndpointVersion.VersionLabel, binding.EndpointVersion.CapabilityEndpointVersionID, "-")},
		},
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		RefreshScreen: action.RefreshScreen,
	}
}

func (e ActionExecutor) projectRegistrationDetail(ctx context.Context, action PortalAction) (projects.ProjectRegistrationDetail, responseEnvelopeMeta, error) {
	ref := firstNonEmpty(action.Executor.Target, action.TargetRef, action.Executor.Payload["project_ref"])
	if e.Client == nil {
		return projects.ProjectRegistrationDetail{}, responseEnvelopeMeta{}, ErrMissingClient
	}
	if ref == "" {
		return projects.ProjectRegistrationDetail{}, responseEnvelopeMeta{}, fmt.Errorf("project action is missing a project ref")
	}
	envelope, err := e.Client.GetProjectRegistrationStatus(ctx, e.CorrelationID, ref)
	if err != nil {
		return projects.ProjectRegistrationDetail{}, responseEnvelopeMeta{}, err
	}
	return envelope.Data, responseEnvelopeMeta{CorrelationID: envelope.Meta.CorrelationID}, nil
}

type responseEnvelopeMeta struct {
	CorrelationID string
}

func normalizeProjectActionRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", nil
	}
	if filepath.IsAbs(root) {
		return filepath.Clean(root), nil
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}
