package portal

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectdoctor"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/storagearchive"
)

type projectFacetRow struct {
	Key         string
	Status      string
	Description string
	Placeholder bool
}

func renderProjects(builder *strings.Builder, state ScreenState) {
	data := state.Data.Projects
	explorer := normalizeProjectExplorerState(data.Explorer)
	if explorer.Level != ProjectExplorerHome && selectedProjectArchived(data) {
		renderArchivedProjectDetail(builder, state, data)
		return
	}
	switch explorer.Level {
	case ProjectExplorerDetail:
		renderProjectDetail(builder, state, data)
	case ProjectExplorerStructure:
		renderProjectStructure(builder, state, data)
	case ProjectExplorerRuntime:
		renderProjectRuntimeView(builder, state, data)
	case ProjectExplorerStorage:
		renderProjectStorage(builder, state, data)
	case ProjectExplorerTimeline:
		renderProjectTimeline(builder, state, data)
	case ProjectExplorerArchive:
		renderArchivedProjectDetail(builder, state, data)
	default:
		renderProjectsHome(builder, state, data)
	}
}

func renderProjectsHome(builder *strings.Builder, state ScreenState, data ProjectsData) {
	renderProjectAvailableActions(builder, state)
	renderProjectList(builder, state, data)
}

func renderProjectDetail(builder *strings.Builder, state ScreenState, data ProjectsData) {
	renderProjectAvailableActions(builder, state)
	renderSelectedProject(builder, state, data)
	renderProjectDetailSections(builder, state, data)
	renderProjectServices(builder, data)
}

func renderProjectStructure(builder *strings.Builder, state ScreenState, data ProjectsData) {
	renderProjectAvailableActions(builder, state)
	renderSelectedProject(builder, state, data)
	renderProjectRepositoryState(builder, state, data)
	renderProjectFacets(builder, state, data)
	renderProjectValidationAndPlan(builder, state, data)
}

func renderProjectRuntimeView(builder *strings.Builder, state ScreenState, data ProjectsData) {
	renderProjectAvailableActions(builder, state)
	renderSelectedProject(builder, state, data)
	renderProjectCapabilities(builder, state, data)
	renderProjectServices(builder, data)
	renderProjectAutomation(builder, state, data)
	renderProjectRuntime(builder, state, data)
}

func renderProjectServices(builder *strings.Builder, data ProjectsData) {
	renderPrimarySection(builder, "Services")
	if len(data.Services) == 0 {
		renderEmpty(builder, "No services are registered for this project.")
		return
	}
	for _, service := range data.Services {
		fmt.Fprintf(builder, "  %-24s registry=%s  process=%s  health=%s\n",
			firstNonEmpty(service.DisplayName, service.ProviderKey, service.ProviderAddress),
			renderStatus(string(service.RegistryState)),
			renderStatus(string(service.ProcessState)),
			renderStatus(firstNonEmpty(service.HealthStatus, "unknown")),
		)
	}
}

func renderProjectStorage(builder *strings.Builder, state ScreenState, data ProjectsData) {
	renderProjectAvailableActions(builder, state)
	renderSelectedProject(builder, state, data)
	renderProjectDataPolicy(builder, state, data)
}

func renderProjectTimeline(builder *strings.Builder, state ScreenState, data ProjectsData) {
	renderProjectAvailableActions(builder, state)
	renderSelectedProject(builder, state, data)
	renderProjectActivity(builder, state, data)
}

func renderArchivedProjectDetail(builder *strings.Builder, state ScreenState, data ProjectsData) {
	renderProjectAvailableActions(builder, state)
	renderSelectedProject(builder, state, data)
	renderAttentionSection(builder, "Archive State")
	renderKeyValue(builder, "status", renderStatus(firstNonEmpty(selectedProjectForRender(data).Status, "archived")))
	renderKeyValue(builder, "runtime", renderStatus("disabled"))
	renderProjectArchiveState(builder, data)
	renderProjectRepositoryState(builder, state, data)
	renderProjectDataPolicy(builder, state, data)
	renderProjectActivity(builder, state, data)
}

func renderProjectAvailableActions(builder *strings.Builder, state ScreenState) {
	records := ScreenRecordItems(state)
	row := renderTopAvailableActions(builder, state, records)
	renderOperationalActionGroups(builder, state, records, &row)
}

func renderProjectArchiveState(builder *strings.Builder, data ProjectsData) {
	state, ok := storagearchive.ParseProjectRuntimeArchiveState(selectedProjectForRender(data).ArchiveState)
	if !ok {
		renderEmpty(builder, "Archived projects are browseable here. Runtime objects should be managed from their own surfaces before reactivation.")
		return
	}
	renderKeyValue(builder, "archive status", renderStatus(firstNonEmpty(state.Status, "archived")))
	renderKeyValue(builder, "runtime archive", firstNonEmpty(state.ProjectRuntimeArchiveID, "-"))
	renderKeyValue(builder, "runtime manifest", firstNonEmpty(state.RuntimeManifestPath, "-"))
	renderKeyValue(builder, "source", firstNonEmpty(state.SourceRef, "-"))
	renderKeyValue(builder, "target", firstNonEmpty(state.TargetPath, "-"))
	renderKeyValue(builder, "storage skipped", fmt.Sprintf("%t", state.StorageArchiveSkipped))
	renderKeyValue(builder, "safe to delete", fmt.Sprintf("%t", state.SafeToDelete))
	if state.SuccessorPolicy.Status != "" {
		renderKeyValue(builder, "runtime migration", renderStatus(state.SuccessorPolicy.Status))
	}
	if len(state.Warnings) > 0 {
		renderKeyValue(builder, "warnings", fmt.Sprintf("%d", len(state.Warnings)))
	}
}

func renderProjectList(builder *strings.Builder, state ScreenState, data ProjectsData) {
	renderSummarySection(builder, "Project List")
	active, archived, paused := projectStatusCounts(data.Projects)
	renderMetricLine(builder,
		fmt.Sprintf("total=%d", len(data.Projects)),
		fmt.Sprintf("active=%d", active),
		fmt.Sprintf("archived=%d", archived),
		fmt.Sprintf("paused=%d", paused),
	)
	if len(data.Projects) == 0 {
		renderEmpty(builder, "No projects yet. Create one in the Box Projects folder, or use $ create project <name>.")
		return
	}
	for _, group := range groupedProjectsByLifecycle(data) {
		if len(group.Projects) == 0 {
			continue
		}
		if group.Title == "Archived" {
			renderDetailsSection(builder, group.Title)
			renderEmpty(builder, fmt.Sprintf("%d archived project(s). They remain inspectable, but runtime actions are quarantined.", len(group.Projects)))
		} else {
			renderPrimarySection(builder, group.Title)
		}
		for _, project := range group.Projects {
			renderProjectListRow(builder, state, data, project)
		}
	}
}

func renderSelectedProject(builder *strings.Builder, state ScreenState, data ProjectsData) {
	project := selectedProjectForRender(data)
	surface := projectSurfaceStatus(data)

	if selectedProjectArchived(data) {
		renderAttentionSection(builder, "Archived Project")
		renderEmpty(builder, "Runtime is disabled. Only archive inspection and restore planning actions are available.")
	} else {
		renderPrimarySection(builder, "Project Status")
	}
	renderKeyValue(builder, "name", firstNonEmpty(project.Name, project.Slug, "-"))
	renderHealthStrip(builder,
		portalHealthItem{Label: "lifecycle", Status: surface.Lifecycle, Detail: "", Domain: portalDomainProjects},
		portalHealthItem{Label: "registration", Status: surface.Registration, Detail: "", Domain: portalDomainProjects},
		portalHealthItem{Label: "activation", Status: surface.Activation, Detail: "", Domain: portalDomainProjects},
	)
	renderHealthStrip(builder,
		portalHealthItem{Label: "contract", Status: surface.ContractHealth, Detail: "drift " + surface.ContractDrift, Domain: portalDomainProjects},
		portalHealthItem{Label: "runtime", Status: surface.RuntimeHealth, Detail: "", Domain: portalDomainProjects},
		portalHealthItem{Label: "archive", Status: surface.ArchiveState, Detail: "", Domain: portalDomainProjects},
	)
	renderKeyValue(builder, "next action", surface.NextAction)
	renderKeyValue(builder, "lifecycle", renderStatus(surface.Lifecycle))
	renderKeyValue(builder, "registration", renderStatus(surface.Registration))
	renderKeyValue(builder, "activation", renderStatus(surface.Activation))
	renderKeyValue(builder, "contract health", renderStatus(surface.ContractHealth))
	renderKeyValue(builder, "contract drift", renderStatus(surface.ContractDrift))
	renderKeyValue(builder, "runtime", renderStatus(surface.RuntimeHealth))
	renderKeyValue(builder, "archive", renderStatus(surface.ArchiveState))
	renderDetailsSection(builder, "Project Metadata")
	renderKeyValue(builder, "backend owner", firstNonEmpty(surface.BackendOwner, "-"))
	renderKeyValue(builder, "facets", projectFacetsSummary(surface.Facets))
	if surface.Root != "" {
		renderKeyValue(builder, "root", surface.Root)
	}
	if layout, _, contractPath := projectResolvedLayout(data.RegistrationDetail, data.BackendAnalysis); layout != "" {
		renderKeyValue(builder, "layout", string(layout))
		if contractPath != "" {
			renderKeyValue(builder, "contract", contractPath)
		}
		if layout == projectcontracts.ProjectLayoutLegacy || layout == projectcontracts.ProjectLayoutCanonicalWithLegacy {
			renderKeyValue(builder, "compatibility", "migration available in Structure actions")
		}
	}
	if state.RawDetails {
		renderKeyValue(builder, "slug", firstNonEmpty(project.Slug, "-"))
	}
	if state.RawDetails && data.RegistrationDetail.Registration != nil {
		renderKeyValue(builder, "revision", surface.Revision)
	}
}

func renderProjectDetailSections(builder *strings.Builder, state ScreenState, data ProjectsData) {
	renderSummarySection(builder, "Project Areas")
	rows := []struct {
		key     ProjectExplorerLevel
		label   string
		status  string
		summary string
	}{
		{
			key:     ProjectExplorerStructure,
			label:   "Structure",
			status:  projectValidationStatus(data.RegistrationDetail),
			summary: fmt.Sprintf("repos=%d facets=%d workflows=%d", data.RepositoryStatus.Observation.MemberCount, len(projectFacetDisplayRows(data)), len(data.Workflows)),
		},
		{
			key:     ProjectExplorerRuntime,
			label:   "Runtime",
			status:  projectRuntimeStatus(data),
			summary: fmt.Sprintf("providers=%d capabilities=%d schedules=%d endpoints=%d", len(data.Providers), len(data.Capabilities), len(data.Schedules), len(data.DirectEventEndpoints)),
		},
		{
			key:     ProjectExplorerStorage,
			label:   "Storage",
			status:  projectStorageStatus(data),
			summary: fmt.Sprintf("watched_roots=%d sync_roots=%d backup_roots=%d findings=%d", len(data.WatchedRoots), data.SyncStatus.SyncRoots, data.BackupStatus.BackupRoots, len(data.WatchedRootFindings)),
		},
		{
			key:     ProjectExplorerTimeline,
			label:   "Timeline",
			status:  projectActivityStatus(data),
			summary: fmt.Sprintf("jobs=%d failed_jobs=%d calls=%d invocations=%d", len(data.Jobs), len(data.FailedJobs), len(data.CapabilityCalls), len(data.Invocations)),
		},
	}
	for _, row := range rows {
		ref := data.SelectedProjectRef + "." + string(row.key)
		marker := projectSelectionMarker(state, projectSectionRecordKind(row.key), ref)
		fmt.Fprintf(builder, "%s %-10s %s  %s\n", marker, row.label, renderStatus(row.status), portalRenderContext().Styles.Muted.Render(row.summary))
	}
	if selectedProjectArchived(data) {
		ref := data.SelectedProjectRef + "." + string(ProjectExplorerArchive)
		marker := projectSelectionMarker(state, projectSectionRecordKind(ProjectExplorerArchive), ref)
		fmt.Fprintf(builder, "%s %-10s %s  %s\n", marker, "Archive", renderStatus("archived"), portalRenderContext().Styles.Muted.Render("archived project detail and retained activity"))
	}
}

func renderProjectRepositoryState(builder *strings.Builder, state ScreenState, data ProjectsData) {
	renderPrimarySection(builder, "Repositories")
	if !data.RepositoryStateReady {
		renderEmpty(builder, "Authorized repository state is unavailable for this project load.")
		return
	}
	status := data.RepositoryStatus
	renderKeyValue(builder, "source", string(status.Source.Posture))
	if status.Source.ProjectContractSchemaVersion != "" || status.Source.ReposContractSchemaVersion != "" {
		renderKeyValue(builder, "source versions", fmt.Sprintf("project=%s repos=%s revision=%d", status.Source.ProjectContractSchemaVersion, status.Source.ReposContractSchemaVersion, status.Source.SourceRevision))
	}
	renderKeyValue(builder, "freshness", fmt.Sprintf("%s observed=%d not_observed=%d remote_unavailable=%d", status.Observation.Posture, status.Observation.Observed, status.Observation.NotObserved, status.Observation.RemoteUnavailable))
	if !status.ObservedAt.IsZero() {
		renderKeyValue(builder, "observed", status.ObservedAt.UTC().Format(time.RFC3339))
	}
	if len(status.Repositories) == 0 {
		renderEmpty(builder, "No explicit repository members are registered.")
		return
	}
	for _, repository := range status.Repositories {
		marker := projectSelectionMarker(state, "project_repository", data.SelectedProjectRef+".repository."+repository.RepositoryID)
		git := "not_observed"
		if repository.Git != nil {
			branch := firstNonEmpty(repository.Git.CurrentBranch, "detached")
			dirty := "clean"
			if repository.Git.Dirty.Dirty {
				dirty = "dirty"
			}
			git = branch + "/" + dirty
			if repository.Git.Worktree {
				git += "/worktree(noncanonical)"
			}
		}
		fmt.Fprintf(builder, "%s %-20s role=%s  path=%s  fresh=%s  git=%s  .repo=%s\n",
			marker,
			firstNonEmpty(repository.Key, repository.RepositoryID),
			repository.Role,
			repository.RelativePath,
			repository.ObservationPosture,
			git,
			repository.DevelopmentState.Posture,
		)
		if state.RawDetails {
			renderKeyValue(builder, "repository id", repository.RepositoryID)
			if repository.RepositoryOwnerProjectID != "" {
				renderKeyValue(builder, "owner project id", repository.RepositoryOwnerProjectID)
			}
		}
	}
	if status.Page.HasMore {
		renderEmpty(builder, fmt.Sprintf("More repository members are available after %s.", status.Page.NextAfter))
	}
}

func renderProjectFacets(builder *strings.Builder, state ScreenState, data ProjectsData) {
	renderPrimarySection(builder, "Facets")
	rows := projectFacetDisplayRows(data)
	if len(rows) == 0 {
		renderEmpty(builder, "No registered project facets.")
		return
	}
	for _, row := range rows {
		label := row.Key
		if row.Placeholder {
			label += " placeholder"
		}
		ref := data.SelectedProjectRef + "." + row.Key
		marker := projectSelectionMarker(state, "project_facet", ref)
		fmt.Fprintf(builder, "%s %-16s %s  %s\n", marker, label, renderStatus(row.Status), portalRenderContext().Styles.Muted.Render(row.Description))
	}
}

func renderProjectValidationAndPlan(builder *strings.Builder, state ScreenState, data ProjectsData) {
	renderSummarySection(builder, "Validation And Plan")
	renderKeyValue(builder, "validation", projectValidationSummary(data.RegistrationDetail))
	renderKeyValue(builder, "plan", projectPlanSummary(data.RegistrationDetail))
	if len(data.Workflows) > 0 {
		renderKeyValue(builder, "workflows", projectWorkflowSummary(data.Workflows))
		for idx, workflow := range data.Workflows {
			if idx >= 4 {
				break
			}
			ref := data.SelectedProjectRef + ".workflow." + firstNonEmpty(workflow.WorkflowID, workflow.Name)
			marker := projectSelectionMarker(state, "project_workflow", ref)
			right := firstNonEmpty(workflow.CapabilityAddress, workflow.RuntimeKind, workflow.ImplementationKind, "placeholder")
			fmt.Fprintf(builder, "%s %s  %s  %s\n",
				marker,
				firstNonEmpty(workflow.WorkflowID, workflow.Name, "-"),
				renderStatus(firstNonEmpty(workflow.Status, "draft")),
				portalRenderContext().Styles.Muted.Render(right),
			)
		}
	}
}

func renderProjectCapabilities(builder *strings.Builder, state ScreenState, data ProjectsData) {
	renderPrimarySection(builder, "Capabilities")
	if len(data.Providers) == 0 && len(data.Capabilities) == 0 {
		renderEmpty(builder, "No project-owned capabilities returned by the backend.")
		return
	}
	for _, provider := range data.Providers {
		ref := firstNonEmpty(provider.Provider.CompactAddress, provider.Provider.ProviderKey, provider.Provider.ProviderID)
		marker := projectSelectionMarker(state, "project_provider", ref)
		fmt.Fprintf(builder, "%s provider %s  %s  %s\n",
			marker,
			portalRenderContext().Styles.Text.Render(providerLabel(provider)),
			renderStatus(provider.Provider.Status),
			firstNonEmpty(provider.Provider.ProviderType, "-"),
		)
	}
	for _, capability := range data.Capabilities {
		address := firstNonEmpty(capability.CapabilityEndpoint.CompactAddress, capability.CapabilityEndpoint.CapabilityEndpointID)
		marker := projectSelectionMarker(state, "project_capability", address)
		fmt.Fprintf(builder, "%s %s  %s  risk=%s  runtime=%s\n",
			marker,
			portalRenderContext().Styles.Text.Render(address),
			renderStatus(capability.CapabilityEndpoint.Status),
			firstNonEmpty(capability.CapabilityEndpoint.RiskLevel, "-"),
			firstNonEmpty(runtimeKindForCapability(data, capability), "-"),
		)
	}
}

func renderProjectAutomation(builder *strings.Builder, state ScreenState, data ProjectsData) {
	renderSummarySection(builder, "Automation")
	renderMetricLine(builder,
		fmt.Sprintf("schedules: %d", len(data.Schedules)),
		fmt.Sprintf("endpoints: %d", len(data.DirectEventEndpoints)),
		fmt.Sprintf("events: %d", len(data.DirectEvents)),
		fmt.Sprintf("failed invocations: %d", len(data.InvocationFailures)),
	)
	for idx, schedule := range data.Schedules {
		if idx >= 3 {
			break
		}
		ref := firstNonEmpty(schedule.ScheduleID, schedule.ScheduleKey)
		marker := " "
		if projectRecordSelected(state, "project_schedule", ref) {
			marker = portalRenderContext().Styles.SelectedMarker.Render(">")
		}
		fmt.Fprintf(builder, "%s schedule %s  %s  next=%s\n", marker, firstNonEmpty(schedule.DisplayName, schedule.ScheduleKey, schedule.ScheduleID), renderStatus(schedule.Status), timePtrOrDash(schedule.NextFireAt))
	}
	for idx, endpoint := range data.DirectEventEndpoints {
		if idx >= 3 {
			break
		}
		ref := firstNonEmpty(endpoint.EndpointID, endpoint.EndpointSlug)
		marker := " "
		if projectRecordSelected(state, "project_direct_event_endpoint", ref) {
			marker = portalRenderContext().Styles.SelectedMarker.Render(">")
		}
		fmt.Fprintf(builder, "%s endpoint %s  %s  %s\n", marker, firstNonEmpty(endpoint.DisplayName, endpoint.EndpointSlug, endpoint.EndpointID), renderStatus(endpoint.Status), firstNonEmpty(endpoint.EndpointPath, "-"))
	}
	for idx, event := range data.DirectEvents {
		if idx >= 3 {
			break
		}
		ref := event.DirectEventID
		marker := projectSelectionMarker(state, "project_direct_event", ref)
		fmt.Fprintf(builder, "%s event %s  %s  attempts=%d\n", marker, firstNonEmpty(event.ExternalEventID, ref), renderStatus(event.Status), event.AttemptCount)
	}
	for idx, invocation := range data.Invocations {
		if idx >= 3 {
			break
		}
		ref := invocation.InvocationID
		marker := projectSelectionMarker(state, "project_invocation", ref)
		fmt.Fprintf(builder, "%s invocation %s  %s  target=%s\n", marker, ref, renderStatus(invocation.Status), firstNonEmpty(invocation.TargetCapability, "-"))
	}
}

func renderProjectDataPolicy(builder *strings.Builder, state ScreenState, data ProjectsData) {
	renderSummarySection(builder, "Data Policy")
	renderMetricLine(builder,
		fmt.Sprintf("watched roots: %d", len(data.WatchedRoots)),
		fmt.Sprintf("watch plan: %d desired", len(data.WatchPlan.WatchedRoots)),
		fmt.Sprintf("sync roots: %d", data.SyncStatus.SyncRoots),
		fmt.Sprintf("backup roots: %d", data.BackupStatus.BackupRoots),
		fmt.Sprintf("findings: %d", len(data.WatchedRootFindings)),
	)
	for idx, status := range data.WatchedRoots {
		if idx >= 4 {
			break
		}
		ref := status.Root.NodeID + "/" + status.Root.RootKey
		marker := projectSelectionMarker(state, "project_watched_root", ref)
		protected, ignored, countsAvailable := watchedRootProtectionCounts(status.Root.SummaryJSON)
		protection := "protected=- ignored=-"
		if countsAvailable {
			protection = fmt.Sprintf("protected=%d ignored=%d", protected, ignored)
		}
		fmt.Fprintf(builder, "%s root %s  %s  node=%s  %s\n",
			marker,
			firstNonEmpty(status.Root.DisplayName, status.Root.RootKey, ref),
			renderStatus(status.Root.Status),
			firstNonEmpty(status.Root.NodeID, "-"),
			protection,
		)
	}
	for idx, finding := range data.WatchedRootFindings {
		if idx >= 3 {
			break
		}
		ref := firstNonEmpty(finding.WatchedRootFindingID, finding.NodeID+"/"+finding.RootKey+"/"+finding.FindingKey)
		marker := projectSelectionMarker(state, "project_watched_root_finding", ref)
		fmt.Fprintf(builder, "%s finding %s  %s  %s\n", marker, firstNonEmpty(finding.Summary, ref), renderStatus(firstNonEmpty(finding.Status, "-")), firstNonEmpty(finding.Severity, "-"))
	}
	if state.RawDetails {
		for idx, root := range data.RegistrationDetail.WatchedRootRegistrations {
			if idx >= 4 {
				break
			}
			fmt.Fprintf(builder, "  %s  %s  sync=%s backup=%s index=%s\n",
				firstNonEmpty(root.DisplayName, root.LocalRootKey, root.BackendRootKey),
				renderStatus(root.ActivationStatus),
				firstNonEmpty(root.SyncMode, "-"),
				firstNonEmpty(root.BackupMode, "-"),
				firstNonEmpty(root.IndexMode, "-"),
			)
		}
	} else if len(data.RegistrationDetail.WatchedRootRegistrations) > 0 {
		renderEmpty(builder, fmt.Sprintf("%d raw watched-root contract rows hidden. Press tab for details.", len(data.RegistrationDetail.WatchedRootRegistrations)))
	}
	fmt.Fprintf(builder, "%s Sync Status  %s roots\n", projectSelectionMarker(state, "project_sync_status", data.SelectedProjectRef+".sync_status"), fmt.Sprintf("%d", data.SyncStatus.SyncRoots))
	protected, ignored, countsAvailable := projectBackupProtectionCounts(data.BackupStatus)
	protection := "protected=- ignored=-"
	if countsAvailable {
		protection = fmt.Sprintf("protected=%d ignored=%d", protected, ignored)
	}
	fmt.Fprintf(builder, "%s Backup Status  %d roots  %s\n", projectSelectionMarker(state, "project_backup_status", data.SelectedProjectRef+".backup_status"), data.BackupStatus.BackupRoots, protection)
}

func renderProjectRuntime(builder *strings.Builder, state ScreenState, data ProjectsData) {
	renderDetailsSection(builder, "Runtime")
	if len(data.RuntimeBindings) == 0 {
		renderEmpty(builder, "No runtime bindings returned for project providers.")
		return
	}
	counts := map[string]int{}
	for _, binding := range data.RuntimeBindings {
		counts[firstNonEmpty(binding.Binding.RuntimeKind, "-")]++
	}
	metrics := []string{}
	for kind, count := range counts {
		metrics = append(metrics, fmt.Sprintf("%s: %d", kind, count))
	}
	renderMetricLine(builder, metrics...)
	for idx, binding := range data.RuntimeBindings {
		if idx >= 5 {
			break
		}
		marker := projectSelectionMarker(state, "project_runtime_binding", binding.Binding.RuntimeBindingID)
		fmt.Fprintf(builder, "%s %s  %s  %s\n",
			marker,
			firstNonEmpty(binding.Endpoint.CompactAddress, binding.Binding.RuntimeBindingID),
			renderStatus(binding.Binding.Status),
			firstNonEmpty(binding.Binding.RuntimeKind, "-"),
		)
	}
}

func renderProjectActivity(builder *strings.Builder, state ScreenState, data ProjectsData) {
	renderPrimarySection(builder, "Recent Activity")
	renderMetricLine(builder,
		fmt.Sprintf("jobs: %d", len(data.Jobs)),
		fmt.Sprintf("failed jobs: %d", len(data.FailedJobs)),
		fmt.Sprintf("capability calls: %d", len(data.CapabilityCalls)),
		fmt.Sprintf("invocations: %d", len(data.Invocations)),
	)
	jobRows := 0
	for _, job := range data.FailedJobs {
		if jobRows >= 4 {
			break
		}
		marker := projectSelectionMarker(state, "project_job", job.JobID)
		fmt.Fprintf(builder, "%s job %s  %s  %s\n", marker, jobLabel(job), renderStatus(firstNonEmpty(job.Status, "failed")), firstNonEmpty(job.JobType, "-"))
		jobRows++
	}
	for _, job := range data.Jobs {
		if jobRows >= 4 {
			break
		}
		marker := projectSelectionMarker(state, "project_job", job.JobID)
		fmt.Fprintf(builder, "%s job %s  %s  %s\n", marker, jobLabel(job), renderStatus(job.Status), firstNonEmpty(job.JobType, "-"))
		jobRows++
	}
	for idx, call := range data.CapabilityCalls {
		if idx >= 3 {
			break
		}
		marker := projectSelectionMarker(state, "project_capability_call", call.CapabilityCallID)
		fmt.Fprintf(builder, "%s call %s  %s  %s\n", marker, firstNonEmpty(call.Operation, call.CapabilityCallID), renderStatus(call.Status), firstNonEmpty(call.ExecutionMode, "-"))
	}
}

func projectLabel(project projects.Project) string {
	return firstNonEmpty(project.Name, project.Slug, project.ProjectID)
}

type projectLifecycleGroup struct {
	Title    string
	Projects []projects.Project
}

func groupedProjectsByLifecycle(data ProjectsData) []projectLifecycleGroup {
	groups := []projectLifecycleGroup{
		{Title: "Active"},
		{Title: "Draft / Needs Setup"},
		{Title: "Paused"},
		{Title: "Archived"},
	}
	for _, project := range data.Projects {
		switch projectLifecycleGroupIndex(project.Status) {
		case 1:
			groups[1].Projects = append(groups[1].Projects, project)
		case 2:
			groups[2].Projects = append(groups[2].Projects, project)
		case 3:
			groups[3].Projects = append(groups[3].Projects, project)
		default:
			groups[0].Projects = append(groups[0].Projects, project)
		}
	}
	for idx := range groups {
		sort.SliceStable(groups[idx].Projects, func(i, j int) bool {
			left := groups[idx].Projects[i]
			right := groups[idx].Projects[j]
			leftRank := projectHomeAttentionRank(left, data)
			rightRank := projectHomeAttentionRank(right, data)
			if leftRank != rightRank {
				return leftRank > rightRank
			}
			return strings.ToLower(projectLabel(left)) < strings.ToLower(projectLabel(right))
		})
	}
	return groups
}

func projectHomeAttentionRank(project projects.Project, data ProjectsData) int {
	if projectArchived(project) {
		return 0
	}
	status := strings.TrimSpace(strings.ToLower(project.Status))
	switch status {
	case "blocked", "invalid", "needs_setup", "not_registered":
		return 40
	case "paused", "disabled", "disabled_by_policy":
		return 10
	}
	if projectRef(project) != data.SelectedProjectRef {
		return 0
	}
	surface := projectSurfaceStatus(data)
	rank := 0
	for _, value := range []string{surface.ContractHealth, surface.ContractDrift, surface.RuntimeHealth, surface.Registration, surface.Activation} {
		if projectSurfaceValueNeedsAttention(value) {
			rank += 10
		}
	}
	return rank
}

func projectSurfaceValueNeedsAttention(value string) bool {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "", "-", "ok", "current", "active", "registered", "healthy", "enabled", projects.ProjectActivationStatusBaseActive:
		return false
	default:
		return true
	}
}

func projectLifecycleGroupIndex(status string) int {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "archived", "retired":
		return 3
	case "paused", "disabled", "disabled_by_policy":
		return 2
	case "draft", "created", "pending", "planned", "needs_setup", "not_registered", "invalid", "blocked":
		return 1
	default:
		return 0
	}
}

func renderProjectListRow(builder *strings.Builder, state ScreenState, data ProjectsData, project projects.Project) {
	ref := projectRef(project)
	marker := " "
	if projectRecordSelected(state, "project", ref) {
		marker = portalRenderContext().Styles.SelectedMarker.Render(">")
	}
	status := project.Status
	facets := "-"
	if ref == data.SelectedProjectRef {
		surface := projectSurfaceStatus(data)
		if projectArchived(project) || strings.EqualFold(surface.Lifecycle, "archived") {
			status = "archived"
		} else {
			status = firstNonEmpty(surface.Activation, surface.Registration, status)
		}
		facets = projectFacetsSummary(surface.Facets)
	}
	detail := strings.TrimSpace(project.ProjectType)
	if detail == "" {
		detail = "project"
	}
	owner := firstNonEmpty(ptrOrDash(project.HomeNodeID), "-")
	fmt.Fprintf(builder, "%s %s  %s  %s  owner=%s  facets=%s\n",
		marker,
		portalRenderContext().Styles.Text.Render(projectLabel(project)),
		renderStatus(status),
		portalRenderContext().Styles.Muted.Render(detail),
		owner,
		facets,
	)
}

func projectStatusCounts(projectsList []projects.Project) (active int, archived int, paused int) {
	for _, project := range projectsList {
		switch strings.ToLower(strings.TrimSpace(project.Status)) {
		case "archived":
			archived++
		case "paused":
			paused++
		default:
			active++
		}
	}
	return active, archived, paused
}

func projectRef(project projects.Project) string {
	return firstNonEmpty(project.Slug, project.ProjectID)
}

func selectedProjectForRender(data ProjectsData) projects.Project {
	project := data.SelectedProject.Project
	if project.ProjectID == "" && data.RegistrationDetail.Project.Project.ProjectID != "" {
		project = data.RegistrationDetail.Project.Project
	}
	if project.ProjectID == "" && len(data.Projects) > 0 {
		project = data.Projects[0]
	}
	return project
}

func selectedProjectArchived(data ProjectsData) bool {
	return projectArchived(selectedProjectForRender(data))
}

func projectSurfaceStatus(data ProjectsData) ProjectSurfaceStatus {
	project := selectedProjectForRender(data)
	detail := data.RegistrationDetail
	plan := parseStoredProjectPlan(detail)
	report := parseStoredValidationReport(detail)
	surface := ProjectSurfaceStatus{
		Lifecycle:      firstNonEmpty(project.Status, "unknown"),
		Registration:   "not_registered",
		Activation:     projects.ProjectActivationStatusInactive,
		BackendOwner:   firstNonEmpty(ptrOrDash(project.HomeNodeID), "-"),
		Facets:         projectFacetKeys(projectFacetDisplayRows(data)),
		ContractHealth: projectValidationStatus(detail),
		ContractDrift:  "not_checked",
		RuntimeHealth:  projectRuntimeStatus(data),
		ArchiveState:   "active",
		NextAction:     "inspect project",
	}
	forcedNextAction := ""
	if plan != nil {
		surface.BackendOwner = firstNonEmpty(plan.Project.OwnerNode, surface.BackendOwner)
		surface.Lifecycle = firstNonEmpty(project.Status, plan.Project.Status, surface.Lifecycle)
	}
	if report != nil {
		surface.BackendOwner = firstNonEmpty(report.Project.OwnerNode, surface.BackendOwner)
	}
	if detail.Registration != nil {
		surface.Registration = firstNonEmpty(detail.Registration.RegistrationStatus, "registered")
		surface.Activation = firstNonEmpty(detail.Registration.ActivationStatus, projects.ProjectActivationStatusInactive)
		surface.Root = strings.TrimSpace(detail.Registration.ProjectRoot)
		surface.Revision = fmt.Sprintf("%d", detail.Registration.RegistrationRevision)
	}
	if data.BackendAnalysis != nil {
		surface.ContractDrift = string(data.BackendAnalysis.DriftStatus)
		surface.Root = firstNonEmpty(surface.Root, data.BackendAnalysis.ProjectRoot)
		switch data.BackendAnalysis.DriftStatus {
		case projectdoctor.BackendDriftCurrent:
			surface.ContractHealth = "ok"
		case projectdoctor.BackendDriftStale:
			surface.ContractHealth = "stale"
			forcedNextAction = firstNonEmpty(data.BackendAnalysis.NextAction, "re-register contract from backend")
		case projectdoctor.BackendDriftUnregistered:
			surface.Registration = "not_registered"
			surface.ContractHealth = "not_registered"
			forcedNextAction = firstNonEmpty(data.BackendAnalysis.NextAction, "register contract from backend")
		case projectdoctor.BackendDriftInvalid:
			surface.ContractHealth = "failed"
			forcedNextAction = firstNonEmpty(data.BackendAnalysis.NextAction, "fix backend project contract")
		case projectdoctor.BackendDriftUnavailable:
			surface.ContractHealth = "unavailable"
			forcedNextAction = firstNonEmpty(data.BackendAnalysis.NextAction, "inspect registration and backend contract")
		}
	}
	if selectedProjectArchived(data) {
		surface.Lifecycle = "archived"
		if state, ok := storagearchive.ParseProjectRuntimeArchiveState(project.ArchiveState); ok {
			surface.ArchiveState = firstNonEmpty(state.Status, "archived")
		} else {
			surface.ArchiveState = "archived"
		}
		surface.RuntimeHealth = "disabled"
		surface.NextAction = "inspect archive or plan restore"
		return surface
	}
	if state, ok := storagearchive.ParseProjectRuntimeArchiveState(project.ArchiveState); ok {
		surface.ArchiveState = firstNonEmpty(state.Status, "has_archive_state")
	}
	switch {
	case forcedNextAction != "":
		surface.NextAction = forcedNextAction
	case detail.Registration == nil:
		surface.NextAction = "register contract from backend"
	case surface.Registration == projects.ProjectRegistrationStatusStale:
		surface.NextAction = "re-register contract from backend"
	case surface.ContractHealth == "failed" || surface.ContractHealth == "blocked":
		surface.NextAction = "fix contract and re-register"
	case surface.Activation == projects.ProjectActivationStatusInactive:
		surface.NextAction = "activate project"
	case len(surface.Facets) == 0:
		surface.NextAction = "add facet"
	case surface.RuntimeHealth == "needs_attention":
		surface.NextAction = "inspect automation health"
	default:
		surface.NextAction = "inspect, add facet, or archive"
	}
	return surface
}

func projectFacetKeys(rows []projectFacetRow) []string {
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		key := strings.TrimSpace(row.Key)
		if key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

func projectFacetsSummary(facets []string) string {
	if len(facets) == 0 {
		return "none"
	}
	return strings.Join(facets, ", ")
}

func projectRegistrationState(detail projects.ProjectRegistrationDetail) string {
	if detail.Registration == nil {
		return "not_registered"
	}
	return firstNonEmpty(detail.Registration.ActivationStatus, detail.Registration.RegistrationStatus, "registered")
}

func projectFacetDisplayRows(data ProjectsData) []projectFacetRow {
	detail := data.RegistrationDetail
	rows := make([]projectFacetRow, 0, len(detail.Facets)+7)
	if len(detail.Facets) > 0 {
		for _, facet := range detail.Facets {
			description := fmt.Sprintf("present=%t enabled=%t", facet.Present, facet.Enabled)
			rows = append(rows, projectFacetRow{Key: facet.FacetKey, Status: firstNonEmpty(facet.FacetStatus, "registered"), Description: description, Placeholder: facet.Placeholder})
		}
		return rows
	}
	if len(detail.ScriptExposures) > 0 {
		rows = append(rows, projectFacetRow{Key: "scripts", Status: "registered", Description: fmt.Sprintf("%d exposed scripts", len(detail.ScriptExposures))})
	}
	if len(detail.ScheduleRegistrations) > 0 {
		rows = append(rows, projectFacetRow{Key: "schedules", Status: "registered", Description: fmt.Sprintf("%d schedules", len(detail.ScheduleRegistrations))})
	}
	if len(detail.DirectEventRegistrations) > 0 {
		rows = append(rows, projectFacetRow{Key: "direct_events", Status: "registered", Description: fmt.Sprintf("%d direct events", len(detail.DirectEventRegistrations))})
	}
	if len(detail.WatchedRootRegistrations) > 0 {
		rows = append(rows, projectFacetRow{Key: "watched_roots", Status: "registered", Description: fmt.Sprintf("%d roots", len(detail.WatchedRootRegistrations))})
	}
	if len(detail.ConnectorRegistrations) > 0 {
		rows = append(rows, projectFacetRow{Key: "connectors", Status: "registered", Description: fmt.Sprintf("%d connectors", len(detail.ConnectorRegistrations))})
	}
	if len(detail.ModuleRegistrations) > 0 {
		rows = append(rows, projectFacetRow{Key: "modules", Status: "registered", Description: fmt.Sprintf("%d modules", len(detail.ModuleRegistrations))})
	}
	if len(data.Workflows) > 0 {
		status := "placeholder"
		placeholder := true
		if len(detail.WorkflowRegistrations) > 0 {
			status = "registered"
			placeholder = false
		}
		rows = append(rows, projectFacetRow{Key: "workflows", Status: status, Description: projectWorkflowSummary(data.Workflows), Placeholder: placeholder})
	}
	return rows
}

func projectWorkflowSummary(workflows []ProjectWorkflowIntent) string {
	executable := 0
	shim := 0
	placeholder := 0
	active := 0
	for _, workflow := range workflows {
		if strings.TrimSpace(workflow.CapabilityAddress) != "" || workflow.Status == projects.ProjectWorkflowRegistrationStatusActive {
			active++
		}
		switch workflow.ImplementationKind {
		case projectcontracts.WorkflowImplementationWorkflow:
			executable++
		case projectcontracts.WorkflowImplementationScript:
			shim++
		default:
			placeholder++
		}
	}
	return fmt.Sprintf("%d workflows  active=%d executable=%d shims=%d design=%d", len(workflows), active, executable, shim, placeholder)
}

func projectValidationSummary(detail projects.ProjectRegistrationDetail) string {
	if detail.Registration == nil || len(detail.Registration.ValidationReport) == 0 {
		return renderStatus("not_registered")
	}
	var report projectcontracts.ValidationReport
	if err := json.Unmarshal(detail.Registration.ValidationReport, &report); err != nil {
		return renderStatus("unavailable")
	}
	status := "ok"
	if !report.OK || report.Summary.Errors > 0 {
		status = "failed"
	}
	return fmt.Sprintf("%s  errors=%d warnings=%d infos=%d", renderStatus(status), report.Summary.Errors, report.Summary.Warnings, report.Summary.Infos)
}

func projectValidationStatus(detail projects.ProjectRegistrationDetail) string {
	if detail.Registration == nil || len(detail.Registration.ValidationReport) == 0 {
		return "not_registered"
	}
	var report projectcontracts.ValidationReport
	if err := json.Unmarshal(detail.Registration.ValidationReport, &report); err != nil {
		return "unavailable"
	}
	if !report.OK || report.Summary.Errors > 0 {
		return "failed"
	}
	if report.Summary.Warnings > 0 {
		return "warning"
	}
	return "ok"
}

func projectPlanSummary(detail projects.ProjectRegistrationDetail) string {
	if detail.Registration == nil || len(detail.Registration.RegistrationPlan) == 0 {
		return renderStatus("not_registered")
	}
	var plan projectcontracts.ProjectPlan
	if err := json.Unmarshal(detail.Registration.RegistrationPlan, &plan); err != nil {
		return renderStatus("unavailable")
	}
	status := "ok"
	if !plan.Registerable || plan.Summary.Errors > 0 {
		status = "blocked"
	}
	return fmt.Sprintf("%s  actions=%d facets=%d unsupported=%d", renderStatus(status), len(plan.Actions), len(plan.Facets), len(plan.UnsupportedFeatures))
}

func projectRuntimeStatus(data ProjectsData) string {
	if len(data.InvocationFailures) > 0 || len(data.FailedJobs) > 0 {
		return "needs_attention"
	}
	if len(data.Providers) == 0 && len(data.Capabilities) == 0 && len(data.RuntimeBindings) == 0 && len(data.Schedules) == 0 && len(data.DirectEventEndpoints) == 0 {
		return "empty"
	}
	return "ok"
}

func projectStorageStatus(data ProjectsData) string {
	if len(data.WatchedRootFindings) > 0 {
		return "needs_attention"
	}
	if len(data.WatchedRoots) == 0 && len(data.WatchPlan.WatchedRoots) == 0 && data.SyncStatus.SyncRoots == 0 && data.BackupStatus.BackupRoots == 0 {
		return "empty"
	}
	return "ok"
}

func projectActivityStatus(data ProjectsData) string {
	if len(data.FailedJobs) > 0 || len(data.InvocationFailures) > 0 {
		return "failed"
	}
	if len(data.Jobs) == 0 && len(data.CapabilityCalls) == 0 && len(data.Invocations) == 0 {
		return "empty"
	}
	return "ok"
}

func runtimeKindForCapability(data ProjectsData, capability capabilities.CapabilityListItem) string {
	endpointID := capability.CapabilityEndpoint.CapabilityEndpointID
	address := capability.CapabilityEndpoint.CompactAddress
	for _, binding := range data.RuntimeBindings {
		if binding.Endpoint.CapabilityEndpointID == endpointID || binding.Endpoint.CompactAddress == address {
			return binding.Binding.RuntimeKind
		}
	}
	return ""
}

func projectRecordSelected(state ScreenState, kind string, ref string) bool {
	item, ok := selectedRecordItem(state)
	if !ok {
		return false
	}
	return item.RecordKind == kind && item.RecordRef == ref
}

func projectSelectionMarker(state ScreenState, kind string, ref string) string {
	if projectRecordSelected(state, kind, ref) {
		return portalRenderContext().Styles.SelectedMarker.Render(">")
	}
	return " "
}
