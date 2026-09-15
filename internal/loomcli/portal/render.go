package portal

import (
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/loomcli/actions"
	"loom.local/loom/internal/loomcli/ui"
	"loom.local/loom/internal/search"
	"loom.local/loom/internal/update"
	"loom.local/loom/internal/workers"
)

const (
	ScreenHome         = "home"
	ScreenDoctor       = "doctor"
	ScreenTimeline     = "timeline"
	ScreenBox          = "box"
	ScreenProjects     = "projects"
	ScreenServices     = "services"
	ScreenNotes        = "notes"
	ScreenBackground   = "background"
	ScreenAutomations  = "automations"
	ScreenJobs         = "jobs"
	ScreenNodes        = "nodes"
	ScreenCapabilities = "capabilities"
	ScreenDatabase     = "database"
	ScreenStorage      = "storage"
)

func NormalizeScreen(screen string) string {
	switch strings.TrimSpace(strings.ToLower(screen)) {
	case "", ScreenHome:
		return ScreenHome
	case ScreenDoctor, "portal doctor", "doctor surface", "health", "repair", "attention", "problems", "fix":
		return ScreenDoctor
	case ScreenTimeline, "operations", "operation timeline", "activity", "progress":
		return ScreenTimeline
	case ScreenBox, "loom box", "box workspace", "box surface":
		return ScreenBox
	case ScreenProjects, "project", "project cockpit", "project surface":
		return ScreenProjects
	case ScreenServices, "service", "service registry", "registered services":
		return ScreenServices
	case ScreenNotes, "loom notes", "notes knowledge", "knowledge notes":
		return ScreenNotes
	case ScreenBackground, "workers", "background operations":
		return ScreenBackground
	case ScreenAutomations, "automation", "automation center":
		return ScreenAutomations
	case ScreenJobs, "jobs_search", "jobs and search", "search":
		return ScreenJobs
	case ScreenNodes, "watched-roots", "watched_roots", "nodes and watched roots":
		return ScreenNodes
	case ScreenCapabilities, "providers":
		return ScreenCapabilities
	case ScreenDatabase, "databases", "objects", "object database", "database and objects", "knowledge":
		return ScreenDatabase
	case ScreenStorage, "main storage", "loom main storage", "main-storage", "storage surface", "archives", "transfers":
		return ScreenStorage
	default:
		return ScreenHome
	}
}

type RenderInput struct {
	Mode         ui.Mode
	HomeSnapshot Snapshot
	Registry     actions.Registry
	Screen       string
	State        ScreenState
	Width        int
	Height       int
	Logo         PortalLogo
}

func RenderScreen(mode ui.Mode, snapshot Snapshot, registry actions.Registry, screen string) string {
	screen = NormalizeScreen(screen)
	return RenderScreenWithState(RenderInput{
		Mode:         mode,
		HomeSnapshot: snapshot,
		Registry:     registry,
		Screen:       screen,
		State:        ScreenStateFromSnapshot(screen, snapshot),
		Width:        mode.TTY.Width,
		Height:       mode.TTY.Height,
	})
}

func RenderScreenWithState(input RenderInput) string {
	ctx := newRenderContext(input)
	restore := setActiveRenderContext(ctx)
	defer restore()
	mode := input.Mode
	snapshot := input.HomeSnapshot
	registry := input.Registry
	screen := input.Screen
	screen = NormalizeScreen(screen)
	state := input.State
	if strings.TrimSpace(state.Screen) == "" {
		state = ScreenStateFromSnapshot(screen, snapshot)
	}
	var builder strings.Builder
	if screen != ScreenHome {
		renderTitle(&builder, mode, screenTitle(screen))
	}
	if state.Status == ScreenLoadLoading {
		renderLoading(&builder, screenTitle(screen))
		renderFooter(&builder)
		return builder.String()
	}
	if state.Status == ScreenLoadUnavailable {
		renderUnavailableScreen(&builder, state)
		renderFooter(&builder)
		return builder.String()
	}
	if state.Status == ScreenLoadFailed {
		renderScreenError(&builder, screenTitle(screen), state)
		renderFooter(&builder)
		return builder.String()
	}
	switch screen {
	case ScreenDoctor:
		renderDoctor(&builder, state)
	case ScreenTimeline:
		renderTimeline(&builder, state)
	case ScreenProjects:
		renderProjects(&builder, state)
	case ScreenServices:
		renderServices(&builder, state)
	case ScreenBox:
		renderBox(&builder, state)
	case ScreenBackground:
		renderBackground(&builder, state)
	case ScreenAutomations:
		renderAutomations(&builder, state)
	case ScreenJobs:
		renderJobs(&builder, state)
	case ScreenNotes:
		renderNotes(&builder, state)
	case ScreenNodes:
		renderNodes(&builder, state)
	case ScreenCapabilities:
		renderCapabilitiesExplorer(&builder, state)
	case ScreenDatabase:
		renderDatabase(&builder, state)
	case ScreenStorage:
		renderStorage(&builder, state)
	default:
		renderHome(&builder, state, snapshot, registry)
	}
	partials := state.PartialErrors
	if len(partials) == 0 && screen == ScreenHome && snapshot.MainAvailability.State != MainAvailabilityOffline {
		partials = snapshot.PartialErrors
	}
	if screen == ScreenProjects {
		partials = projectPartialsForRender(state, partials)
	}
	renderPartialErrors(&builder, partials)
	if state.RawDetails {
		renderSection(&builder, "Raw Details")
		fmt.Fprintf(&builder, "  screen=%s status=%s selected=%d width=%d height=%d\n",
			screen,
			state.Status,
			state.SelectedIndex,
			input.Width,
			input.Height,
		)
		fmt.Fprintf(&builder, "  loaded_at=%s\n", timeOrDash(state.LoadedAt))
		fmt.Fprintf(&builder, "  partial_errors=%d\n", len(partials))
	}
	if screen == ScreenDatabase {
		renderFooterFor(&builder, footerDatabase)
	} else if screen == ScreenNotes {
		renderFooterFor(&builder, footerNotes)
	} else {
		renderFooter(&builder)
	}
	return builder.String()
}

func projectPartialsForRender(state ScreenState, partials []SnapshotError) []SnapshotError {
	if normalizeProjectExplorerState(state.Data.Projects.Explorer).Level != ProjectExplorerHome {
		return partials
	}
	filtered := make([]SnapshotError, 0, len(partials))
	for _, partial := range partials {
		if partial.Source == "projects" {
			filtered = append(filtered, partial)
		}
	}
	return filtered
}

func RenderSearch(mode ui.Mode, query string, results []actions.SearchResult) string {
	portalResults := make([]PortalSearchResult, 0, len(results))
	for _, result := range results {
		action := PortalActionFromRegistry(result.Action)
		if shouldHideRawSearchAction(action) {
			continue
		}
		portalResults = append(portalResults, PortalSearchResult{
			Action:      action,
			Category:    portalSearchCategory(action),
			Title:       action.Label,
			Description: action.Description,
			Disabled:    action.Disabled(),
			Reason:      action.DisabledReason,
			Score:       result.Score,
		})
	}
	return RenderPortalSearchWithSelection(mode, query, portalResults, 0, mode.TTY.Width, mode.TTY.Height)
}

func RenderSearchWithSelection(mode ui.Mode, query string, results []actions.SearchResult, selected int) string {
	portalResults := make([]PortalSearchResult, 0, len(results))
	for _, result := range results {
		action := PortalActionFromRegistry(result.Action)
		if shouldHideRawSearchAction(action) {
			continue
		}
		portalResults = append(portalResults, PortalSearchResult{
			Action:      action,
			Category:    portalSearchCategory(action),
			Title:       action.Label,
			Description: action.Description,
			Disabled:    action.Disabled(),
			Reason:      action.DisabledReason,
			Score:       result.Score,
		})
	}
	return RenderPortalSearchWithSelection(mode, query, portalResults, selected, mode.TTY.Width, mode.TTY.Height)
}

func RenderPortalSearch(mode ui.Mode, query string, results []PortalSearchResult) string {
	return RenderPortalSearchWithSelection(mode, query, results, 0, mode.TTY.Width, mode.TTY.Height)
}

func RenderPortalSearchWithSelection(mode ui.Mode, query string, results []PortalSearchResult, selected int, width int, height int) string {
	ctx := renderContext{Mode: mode, Styles: NewPortalStyles(mode), Width: width, Height: height, Logo: SelectPortalLogo(width, nil)}
	restore := setActiveRenderContext(ctx)
	defer restore()
	var builder strings.Builder
	input := ParsePortalSearchInput(query)
	title, placeholder := searchBoxCopy(input)
	renderTitle(&builder, mode, title)
	renderSearchBoxWithTitle(&builder, title, placeholder, query, width)
	if len(results) == 0 {
		renderEmpty(&builder, emptySearchMessage(input))
		renderFooterFor(&builder, footerSearch)
		return builder.String()
	}
	for idx, result := range results {
		action := result.Action
		marker := renderSelectedMarker(idx, selected)
		risk := renderSearchRisk(action)
		title := trimForWidth(firstNonEmpty(result.Title, action.Label), usableWidth(width, 22))
		description := trimForWidth(firstNonEmpty(result.Description, action.Description), usableWidth(width, 6))
		meta := renderCategory(result.Category)
		if risk != "" {
			meta += "  " + risk
		}
		fmt.Fprintf(&builder, "%s %-18s %s\n", marker, meta, title)
		if idx == selected && description != "" {
			fmt.Fprintf(&builder, "  %s\n", portalRenderContext().Styles.Muted.Render(description))
		}
		if idx == selected && result.Disabled && result.Reason != "" {
			fmt.Fprintf(&builder, "  %s\n", portalRenderContext().Styles.Disabled.Render(result.Reason))
		}
	}
	renderFooterFor(&builder, footerSearch)
	return builder.String()
}

func renderSearchRisk(action PortalAction) string {
	if action.Risk == ActionRiskInspect {
		return ""
	}
	return renderRisk(action.Risk)
}

func searchBoxCopy(input PortalSearchInput) (string, string) {
	switch input.Mode {
	case PortalSearchModeScoped:
		if !input.ScopeComplete {
			return "Scoped Search", "#doctor, #box, #notes, #projects, #capabilities, #storage, #database, #workers, #automation, #jobs, #nodes"
		}
		switch input.Scope {
		case "doctor":
			return "Doctor Search", "#doctor storage"
		case "box":
			return "Box Search", "#box documents"
		case "capabilities":
			return "Capability Search", "#capabilities main@system"
		case "workers":
			return "Worker Search", "#workers indexer"
		case "database":
			return "Object Diagnostics Search", "#database object"
		case "notes":
			return "Notes Search", "#notes osint"
		case "storage":
			return "Storage Search", "#storage macbook"
		case "automation":
			return "Automation Search", "#automation morning"
		case "jobs":
			return "Job Search", "#jobs failed"
		case "nodes":
			return "Node Search", "#nodes workspace"
		default:
			return "Scoped Search", "#capabilities main@system"
		}
	default:
		return "Command Palette", "type to navigate, # for scoped search, $ for commands"
	}
}

func emptySearchMessage(input PortalSearchInput) string {
	switch input.Mode {
	case PortalSearchModeScoped:
		if !input.ScopeComplete {
			if strings.TrimSpace(input.ScopeRaw) != "" {
				return "No matching scopes."
			}
			return "Type a scope: #doctor, #box, #notes, #projects, #capabilities, #storage, #database, #workers, #automation, #jobs, or #nodes."
		}
		return "No matching records in this scope."
	default:
		return "No matching navigation results."
	}
}

func renderTitle(builder *strings.Builder, mode ui.Mode, title string) {
	builder.WriteString(portalRenderContext().Styles.Title.Render(title))
	builder.WriteString("\n\n")
}

func screenTitle(screen string) string {
	switch screen {
	case ScreenDoctor:
		return "Doctor"
	case ScreenTimeline:
		return "Timeline"
	case ScreenProjects:
		return "Projects"
	case ScreenServices:
		return "Services"
	case ScreenBox:
		return "LOOM Box"
	case ScreenBackground:
		return "Background Operations"
	case ScreenAutomations:
		return "Automation Center"
	case ScreenJobs:
		return "Jobs"
	case ScreenNotes:
		return "LOOM Notes"
	case ScreenNodes:
		return "Nodes And Watched Roots"
	case ScreenCapabilities:
		return "Capabilities And Providers"
	case ScreenDatabase:
		return "Object Store Diagnostics"
	case ScreenStorage:
		return "LOOM Main Storage"
	default:
		return "LOOM"
	}
}

func renderHome(builder *strings.Builder, state ScreenState, fallback Snapshot, registry actions.Registry) {
	data := state.Data.Home
	if data.Snapshot.CapturedAt.IsZero() {
		data = BuildHomeData(fallback)
	}
	renderLogo(builder, portalRenderContext().Logo)
	renderHomeDashboard(builder, state, data)
	if state.RawDetails {
		renderSection(builder, "Section Commands")
		for _, action := range registry.All() {
			if !strings.HasSuffix(action.ID, ".open") {
				continue
			}
			fmt.Fprintf(builder, "  %s -> %s\n", action.Title, strings.Join(action.RawCommand, " "))
		}
	}
}

func renderLogo(builder *strings.Builder, logo PortalLogo) {
	if len(logo.Lines) == 0 {
		logo = CompactPortalLogo()
	}
	for _, line := range logo.RenderLines(len(logo.Lines)) {
		builder.WriteString(portalRenderContext().Styles.Logo.Render(line))
		builder.WriteByte('\n')
	}
	builder.WriteByte('\n')
	builder.WriteString(portalRenderContext().Styles.Muted.Render("LOOM Portal"))
	builder.WriteByte('\n')
	builder.WriteByte('\n')
}

func screenDescription(screen string) string {
	switch NormalizeScreen(screen) {
	case ScreenDoctor:
		return "Active LOOM issues, grouped findings, safe next actions, and repair routing"
	case ScreenTimeline:
		return "Running, recent, and failed operations across LOOM"
	case ScreenProjects:
		return "Projects, facets, exposed capabilities, automations, and data policy"
	case ScreenServices:
		return "Registered services, observed process state, health, and bounded operations"
	case ScreenBox:
		return "Filesystem workspace, default projects path, Notes/Documents watches, and profile-local intake"
	case ScreenBackground:
		return "Workers, maintenance, backups, and object-store scans"
	case ScreenAutomations:
		return "Schedules, direct events, integrations, and invocations"
	case ScreenJobs:
		return "Jobs, runners, indexing, and search operations"
	case ScreenNotes:
		return "Notes source roots, file types, projection, and notes search context"
	case ScreenNodes:
		return "Nodes, watched roots, sync, and workspace backups"
	case ScreenCapabilities:
		return "Providers and callable capabilities"
	case ScreenDatabase:
		return "Object store records, text-index diagnostics, and sync records"
	case ScreenStorage:
		return "Main storage tree, mounted export state, archives, transfers, and retrieval actions"
	default:
		return "Operational dashboard and attention summary"
	}
}

func renderBackground(builder *strings.Builder, state ScreenState) {
	data := state.Data.Background
	fmt.Fprintf(builder, "Workers: %d total, %d needing attention\n", len(data.Workers), degradedWorkerCount(data.Workers))
	fmt.Fprintf(builder, "Maintenance: %s, open findings=%d\n\n", renderStatus(data.Maintenance.OverallStatus), data.Maintenance.Findings.Open)

	records := ScreenRecordItems(state)
	row := renderTopAvailableActions(builder, state, records)
	renderOperationalActionGroups(builder, state, records, &row)

	renderBackgroundAttention(builder, state, &row)
	renderBackgroundProtection(builder, state, &row)
	renderWorkerGroups(builder, state, &row)
	renderMaintenanceDetails(builder, state, &row)
}

func renderAutomations(builder *strings.Builder, state ScreenState) {
	data := state.Data.Automations
	records := ScreenRecordItems(state)
	row := renderTopAvailableActions(builder, state, records)
	renderOperationalActionGroups(builder, state, records, &row)

	renderAutomationFailures(builder, state, &row)

	renderPrimarySection(builder, "Active Automations")
	if len(data.Automations) == 0 {
		renderEmpty(builder, "No automations returned by the backend.")
	} else {
		for _, group := range automationSurfaceGroups(data) {
			automation := group.Automation
			fmt.Fprintf(builder, "  %s %s  %s  source=%s  schedule=%s  endpoint=%s  last=%s  next=%s  failures=%d\n",
				renderSelectedMarker(row, state.SelectedIndex),
				firstNonEmpty(automation.DisplayName, automation.AutomationKey, automation.AutomationID),
				renderStatus(firstNonEmpty(automation.Status, "-")),
				firstNonEmpty(automation.SourceKind, "-"),
				renderStatus(group.ScheduleStatus),
				renderStatus(group.EndpointStatus),
				renderStatus(group.LastRunStatus),
				group.NextRun,
				group.FailureCount,
			)
			if state.RawDetails {
				fmt.Fprintf(builder, "      automation_id=%s key=%s scope=%s project=%s schedules=%d endpoints=%d fires=%d invocations=%d direct_events=%d\n",
					firstNonEmpty(automation.AutomationID, "-"),
					firstNonEmpty(automation.AutomationKey, "-"),
					stringPtrOrDash(automation.ScopeID),
					stringPtrOrDash(automation.ProjectID),
					len(group.Schedules),
					len(group.Endpoints),
					len(group.Fires),
					len(group.Invocations),
					len(group.DirectEvents),
				)
			}
			row++
		}
	}

	renderSummarySection(builder, "Automation Health")
	renderMetricLine(builder,
		fmt.Sprintf("active=%d", data.ScheduleStatus.ActiveScheduleCount),
		fmt.Sprintf("paused=%d", data.ScheduleStatus.PausedScheduleCount),
		fmt.Sprintf("disabled=%d", data.ScheduleStatus.DisabledScheduleCount),
		fmt.Sprintf("due=%d", data.ScheduleStatus.DueScheduleCount),
		fmt.Sprintf("missed=%d", data.ScheduleStatus.MissedFireCount),
		fmt.Sprintf("pending=%d", data.ScheduleStatus.PendingInvocationCount),
		fmt.Sprintf("failed=%d", data.ScheduleStatus.FailedInvocationCount),
	)
	renderMetricLine(builder,
		fmt.Sprintf("active_endpoints=%d", data.DirectEventStatus.ActiveEndpointCount),
		fmt.Sprintf("failed_events=%d", data.DirectEventStatus.FailedCount),
		fmt.Sprintf("mapping_pending=%d", data.DirectEventStatus.MappingPendingCount),
		fmt.Sprintf("invocation_created=%d", data.DirectEventStatus.InvocationCreatedCount),
	)

	if len(data.Schedules) > 0 {
		renderDetailsSection(builder, "Recent Schedules")
		for _, schedule := range data.Schedules {
			fmt.Fprintf(builder, "  %s %s  %s  kind=%s  next=%s\n",
				renderSelectedMarker(row, state.SelectedIndex),
				firstNonEmpty(schedule.DisplayName, schedule.ScheduleKey, schedule.ScheduleID),
				renderStatus(firstNonEmpty(schedule.Status, "-")),
				firstNonEmpty(schedule.ScheduleKind, "-"),
				timePtrOrDash(schedule.NextFireAt),
			)
			if state.RawDetails {
				fmt.Fprintf(builder, "      schedule_id=%s automation_id=%s expr=%s\n",
					firstNonEmpty(schedule.ScheduleID, "-"),
					firstNonEmpty(schedule.AutomationID, "-"),
					firstNonEmpty(schedule.ScheduleExpr, "-"),
				)
			}
			row++
		}
	}

	if state.RawDetails {
		renderDetailsSection(builder, "Integrations")
		if len(data.Integrations) == 0 {
			renderEmpty(builder, "No integrations returned by the backend.")
		} else {
			for _, integration := range data.Integrations {
				fmt.Fprintf(builder, "  %s %s  %s\n",
					renderSelectedMarker(row, state.SelectedIndex),
					firstNonEmpty(integration.DisplayName, integration.IntegrationKey, integration.IntegrationID),
					renderStatus(firstNonEmpty(integration.Status, "-")),
				)
				fmt.Fprintf(builder, "      integration_id=%s actor=%s auth_level=%s\n",
					firstNonEmpty(integration.IntegrationID, "-"),
					firstNonEmpty(integration.ActorID, "-"),
					intPtrOrDash(integration.MainAuthLevel),
				)
				row++
			}
		}
	}

	if state.RawDetails {
		renderDetailsSection(builder, "Recent Schedule Fires")
		if len(data.ScheduleFires) == 0 {
			renderEmpty(builder, "No recent schedule fires returned by the backend.")
		} else {
			for _, fire := range data.ScheduleFires {
				fmt.Fprintf(builder, "  %s %s  %s  scheduled=%s  job=%s\n",
					renderSelectedMarker(row, state.SelectedIndex),
					firstNonEmpty(fire.ScheduleFireID, "-"),
					renderStatus(firstNonEmpty(fire.Status, "-")),
					timeOrDash(fire.ScheduledFor),
					stringPtrOrDash(fire.JobID),
				)
				fmt.Fprintf(builder, "      schedule_id=%s automation_id=%s invocation=%s route=%s capability_call=%s\n",
					firstNonEmpty(fire.ScheduleID, "-"),
					firstNonEmpty(fire.AutomationID, "-"),
					stringPtrOrDash(fire.InvocationID),
					stringPtrOrDash(fire.RouteID),
					stringPtrOrDash(fire.CapabilityCallID),
				)
				row++
			}
		}
	}

	if len(data.DirectEventEndpoints) > 0 {
		renderDetailsSection(builder, "Direct Events")
		for _, endpoint := range data.DirectEventEndpoints {
			fmt.Fprintf(builder, "  %s %s  %s  %s  %s\n",
				renderSelectedMarker(row, state.SelectedIndex),
				firstNonEmpty(endpoint.DisplayName, endpoint.EndpointSlug, endpoint.EndpointID),
				renderStatus(firstNonEmpty(endpoint.Status, "-")),
				firstNonEmpty(endpoint.EventType, "-"),
				firstNonEmpty(endpoint.ResponseMode, "-"),
			)
			if state.RawDetails {
				fmt.Fprintf(builder, "      endpoint_id=%s integration_id=%s automation_id=%s path=%s\n",
					firstNonEmpty(endpoint.EndpointID, "-"),
					firstNonEmpty(endpoint.IntegrationID, "-"),
					firstNonEmpty(endpoint.AutomationID, "-"),
					firstNonEmpty(endpoint.EndpointPath, "-"),
				)
			}
			row++
		}
	}

	if state.RawDetails {
		renderDetailsSection(builder, "Recent Direct Events")
		if len(data.DirectEvents) == 0 {
			renderEmpty(builder, "No recent direct events returned by the backend.")
		} else {
			for _, event := range data.DirectEvents {
				fmt.Fprintf(builder, "  %s %s  %s  path=%s  received=%s\n",
					renderSelectedMarker(row, state.SelectedIndex),
					firstNonEmpty(event.DirectEventID, "-"),
					renderStatus(firstNonEmpty(event.Status, "-")),
					firstNonEmpty(event.RequestPath, "-"),
					timeOrDash(event.ReceivedAt),
				)
				fmt.Fprintf(builder, "      endpoint_id=%s integration_id=%s automation_id=%s invocation=%s\n",
					firstNonEmpty(event.EndpointID, "-"),
					firstNonEmpty(event.IntegrationID, "-"),
					firstNonEmpty(event.AutomationID, "-"),
					stringPtrOrDash(event.InvocationID),
				)
				row++
			}
		}
	}

	if state.RawDetails {
		renderDetailsSection(builder, "Recent Invocations")
		if len(data.Invocations) == 0 {
			renderEmpty(builder, "No recent invocations returned by the backend.")
		} else {
			for _, invocation := range data.Invocations {
				fmt.Fprintf(builder, "  %s %s  %s  source=%s  capability=%s\n",
					renderSelectedMarker(row, state.SelectedIndex),
					firstNonEmpty(invocation.InvocationID, "-"),
					renderStatus(firstNonEmpty(invocation.Status, "-")),
					firstNonEmpty(invocation.SourceKind, "-"),
					firstNonEmpty(invocation.TargetCapability, "-"),
				)
				fmt.Fprintf(builder, "      automation_id=%s source_ref=%s job=%s route=%s capability_call=%s\n",
					firstNonEmpty(invocation.AutomationID, "-"),
					firstNonEmpty(invocation.SourceRef, "-"),
					stringPtrOrDash(invocation.JobID),
					stringPtrOrDash(invocation.RouteID),
					stringPtrOrDash(invocation.CapabilityCallID),
				)
				row++
			}
		}
	}
}

func renderJobs(builder *strings.Builder, state ScreenState) {
	data := state.Data.Jobs
	records := ScreenRecordItems(state)
	row := renderTopAvailableActions(builder, state, records)
	renderOperationalActionGroups(builder, state, records, &row)
	renderPrimarySection(builder, "Job Queue")
	renderMetricLine(builder,
		fmt.Sprintf("queued=%d", data.JobStatus.QueuedCount),
		fmt.Sprintf("running=%d", data.JobStatus.RunningCount),
		fmt.Sprintf("failed=%d", data.JobStatus.FailedCount),
		fmt.Sprintf("timed_out=%d", data.JobStatus.TimedOutCount),
		fmt.Sprintf("cancelled=%d", data.JobStatus.CancelledCount),
	)
	renderJobsDoctorRoute(builder, data, state.RawDetails)

	if state.RawDetails {
		renderJobsByStatusSection(builder, "Failed Jobs", data.FailedJobs, state, &row)
	}
	renderJobsByStatusSection(builder, "Running Jobs", jobsWithStatus(data.Jobs, jobs.StatusRunning), state, &row)
	renderJobsByStatusSection(builder, "Queued Jobs", data.QueuedJobs, state, &row)

	renderSummarySection(builder, "Runner Health")
	renderHealthStrip(builder,
		portalHealthItem{Label: "runners", Status: fmt.Sprintf("%d", data.RunnerStatus.RunnerCount), Domain: portalDomainJobs},
		portalHealthItem{Label: "idle", Status: fmt.Sprintf("%d", data.RunnerStatus.IdleRunnerCount), Domain: portalDomainJobs},
		portalHealthItem{Label: "running", Status: fmt.Sprintf("%d", data.RunnerStatus.RunningRunnerCount), Domain: portalDomainJobs},
		portalHealthItem{Label: "offline", Status: fmt.Sprintf("%d", data.RunnerStatus.OfflineRunnerCount), Domain: portalDomainJobs},
		portalHealthItem{Label: "failed", Status: fmt.Sprintf("%d", data.RunnerStatus.FailedRunnerCount), Domain: portalDomainJobs},
	)
	if len(data.Runners) == 0 {
		renderEmpty(builder, "No runners returned by the backend.")
	} else {
		for _, runner := range data.Runners {
			fmt.Fprintf(builder, "  %s %s  %s  node=%s  current=%s\n",
				renderSelectedMarker(row, state.SelectedIndex),
				firstNonEmpty(runner.RunnerKey, runner.RunnerID),
				renderStatus(firstNonEmpty(runner.Status, "-")),
				firstNonEmpty(runner.NodeID, "-"),
				stringPtrOrDash(runner.CurrentJobID),
			)
			if state.RawDetails {
				fmt.Fprintf(builder, "      runner_id=%s type=%s heartbeat=%s\n",
					firstNonEmpty(runner.RunnerID, "-"),
					firstNonEmpty(runner.RunnerType, "-"),
					timePtrOrDash(runner.LastHeartbeatAt),
				)
			}
			row++
		}
	}

	if state.RawDetails {
		renderDetailsSection(builder, "Recent Jobs")
		recentJobs := jobsWithoutStatus(data.Jobs, jobs.StatusRunning)
		if len(recentJobs) == 0 {
			renderEmpty(builder, "No recent jobs returned by the backend.")
		} else {
			for _, job := range recentJobs {
				renderJobRow(builder, row, state.SelectedIndex, job, state.RawDetails)
				row++
			}
		}
	}

	renderSummarySection(builder, "Indexing")
	renderMetricLine(builder,
		fmt.Sprintf("status=%d", len(data.IndexStatuses)),
		fmt.Sprintf("queue=%d", len(data.IndexQueue)),
		fmt.Sprintf("failures=%d", len(data.IndexFailures)),
	)

	if state.RawDetails {
		renderAttentionSection(builder, "Index Failures")
		if len(data.IndexFailures) == 0 {
			renderEmpty(builder, "No index failures returned by the backend.")
		} else {
			for _, failure := range data.IndexFailures {
				renderIndexStatusRow(builder, row, state.SelectedIndex, failure, state.RawDetails)
				row++
			}
		}
		renderDetailsSection(builder, "Index Queue")
		if len(data.IndexQueue) == 0 {
			renderEmpty(builder, "No index queue items returned by the backend.")
		} else {
			for _, item := range data.IndexQueue {
				renderIndexStatusRow(builder, row, state.SelectedIndex, item, state.RawDetails)
				row++
			}
		}
		renderDetailsSection(builder, "Recent Index Statuses")
		if len(data.IndexStatuses) == 0 {
			renderEmpty(builder, "No recent index status rows returned by the backend.")
		} else {
			for _, status := range data.IndexStatuses {
				renderIndexStatusRow(builder, row, state.SelectedIndex, status, state.RawDetails)
				row++
			}
		}
	}
}

func renderJobsDoctorRoute(builder *strings.Builder, data JobsData, rawDetails bool) {
	issues := len(data.FailedJobs) + len(data.IndexFailures)
	if data.JobStatus.TimedOutCount > 0 {
		issues += data.JobStatus.TimedOutCount
	}
	if data.JobStatus.CancelledCount > 0 {
		issues += data.JobStatus.CancelledCount
	}
	if issues == 0 {
		return
	}
	renderAttentionSection(builder, "Jobs Attention")
	renderEmpty(builder, fmt.Sprintf("Doctor owns %d jobs/indexing issue(s). Use Retry/Logs action groups here for explicit operator actions.", issues))
	if !rawDetails {
		renderEmpty(builder, "Press tab for failed job rows, index failures, queue entries, and recent index statuses.")
	}
}

func renderBackgroundAttention(builder *strings.Builder, state ScreenState, _ *int) {
	data := state.Data.Background
	attention := []string{}
	if degraded := degradedWorkerCount(data.Workers); degraded > 0 {
		attention = append(attention, fmt.Sprintf("workers=%d", degraded))
	}
	if data.Maintenance.Findings.Open > 0 {
		attention = append(attention, fmt.Sprintf("maintenance_findings=%d", data.Maintenance.Findings.Open))
	}
	if hasBackupStatus(data.BackupStatus) && data.BackupStatus.OpenFindings.Open > 0 {
		attention = append(attention, fmt.Sprintf("backup_findings=%d", data.BackupStatus.OpenFindings.Open))
	}
	if cloudFindings := cloudRootFindingCount(data); cloudFindings > 0 {
		attention = append(attention, fmt.Sprintf("cloud_findings=%d", cloudFindings))
	}
	if hasObjectStoreStatus(data.ObjectStoreStatus) && objectStoreNeedsAttention(data.ObjectStoreStatus.Status) {
		attention = append(attention, "object_store="+data.ObjectStoreStatus.Status)
	}
	if !updateStatusHealthy(data.UpdateStatus) {
		attention = append(attention, "production_update="+updateStatusLabel(data.UpdateStatus))
	}
	if len(attention) == 0 {
		return
	}
	renderAttentionSection(builder, "Operational Attention")
	renderEmpty(builder, fmt.Sprintf("Doctor owns %d background issue(s). Use Run/Check/Logs groups here for explicit operator actions.", len(attention)))
	renderMetricLine(builder, attention...)
	if state.RawDetails && len(data.MaintenanceFindings) > 0 {
		for _, finding := range data.MaintenanceFindings {
			fmt.Fprintf(builder, "  %s  %s\n", renderStatus(finding.Severity), firstNonEmpty(finding.Summary, finding.FindingKey, "-"))
		}
	}
}

func renderBackgroundProtection(builder *strings.Builder, state ScreenState, row *int) {
	data := state.Data.Background
	renderPrimarySection(builder, "Backup And Cloud Protection")
	if !hasBackupStatus(data.BackupStatus) {
		renderEmpty(builder, "No main backup status returned by the backend.")
	} else {
		latestSuccess := "-"
		if data.BackupStatus.LatestSuccessful != nil {
			latestSuccess = firstNonEmpty(data.BackupStatus.LatestSuccessful.Operation.MaintenanceOperationID, "-")
		}
		latestFailure := "-"
		if data.BackupStatus.LatestFailed != nil {
			latestFailure = firstNonEmpty(data.BackupStatus.LatestFailed.Operation.MaintenanceOperationID, "-")
		}
		fmt.Fprintf(builder, "  %s Main Backups  status=%s  latest_success=%s  latest_failure=%s  open_findings=%d\n",
			renderSelectedMarker(*row, state.SelectedIndex),
			renderStatus(firstNonEmpty(data.BackupStatus.Status, "-")),
			latestSuccess,
			latestFailure,
			data.BackupStatus.OpenFindings.Open,
		)
		(*row)++
	}
	if len(data.BackupOperations) == 0 {
		renderEmpty(builder, "No backup operations returned by the backend.")
	} else {
		for _, backup := range data.BackupOperations {
			fmt.Fprintf(builder, "  %s %s  %s  started=%s  artifacts=%d\n",
				renderSelectedMarker(*row, state.SelectedIndex),
				firstNonEmpty(backup.Operation.MaintenanceOperationID, backup.Operation.OperationKey, "-"),
				renderStatus(firstNonEmpty(backup.Operation.Status, "-")),
				timeOrDash(backup.Operation.StartedAt),
				len(backup.Artifacts),
			)
			if state.RawDetails {
				fmt.Fprintf(builder, "      kind=%s worker_run=%s subject=%s:%s\n",
					firstNonEmpty(backup.Operation.OperationKind, "-"),
					stringPtrOrDash(backup.Operation.WorkerRunID),
					firstNonEmpty(backup.Operation.SubjectKind, "-"),
					firstNonEmpty(backup.Operation.SubjectID, "-"),
				)
			}
			(*row)++
		}
	}
	renderCloudMaintenanceDetails(builder, state, row)
	renderProductionUpdateStatus(builder, state, row)
}

func renderProductionUpdateStatus(builder *strings.Builder, state ScreenState, row *int) {
	data := state.Data.Background
	renderPrimarySection(builder, "Production Updates")
	if !hasUpdateStatus(data.UpdateStatus) {
		renderEmpty(builder, "No update status returned.")
		return
	}
	if data.UpdateStatus.Active == nil {
		fmt.Fprintf(builder, "  %s status=%s  active=%s  history=%d\n",
			renderSelectedMarker(*row, state.SelectedIndex),
			renderStatus("none"),
			"missing",
			len(data.UpdateStatus.History),
		)
		if state.RawDetails {
			fmt.Fprintf(builder, "      state_dir=%s manifest=%s diagnostics=%d\n",
				firstNonEmpty(data.UpdateStatus.StateDir, "-"),
				firstNonEmpty(data.UpdateStatus.ActiveManifestPath, "-"),
				len(data.UpdateStatus.Diagnostics),
			)
		}
		(*row)++
		return
	}
	active := data.UpdateStatus.Active
	maintenanceStatus := "-"
	if active.Maintenance != nil {
		maintenanceStatus = firstNonEmpty(active.Maintenance.Status, "-")
	}
	fmt.Fprintf(builder, "  %s status=%s  update=%s  rollback=%s  maintenance=%s  history=%d\n",
		renderSelectedMarker(*row, state.SelectedIndex),
		renderStatus(firstNonEmpty(active.Status, "-")),
		firstNonEmpty(active.UpdateID, "-"),
		firstNonEmpty(active.Rollback.Class, "-"),
		renderStatus(maintenanceStatus),
		len(data.UpdateStatus.History),
	)
	if state.RawDetails {
		fmt.Fprintf(builder, "      active=%s target=%s backup=%s manifest=%s\n",
			firstNonEmpty(active.Active.Path, "-"),
			firstNonEmpty(active.Target.Path, "-"),
			firstNonEmpty(active.BackupRef, active.BackupPath, "-"),
			firstNonEmpty(data.UpdateStatus.ActiveManifestPath, "-"),
		)
	}
	(*row)++
}

func renderWorkerGroups(builder *strings.Builder, state ScreenState, row *int) {
	data := state.Data.Background
	renderPrimarySection(builder, "Worker Attention")
	renderedAttention := false
	for _, group := range groupWorkers(data.Workers) {
		workersInGroup := workersWithAttention(group.Workers)
		if len(workersInGroup) == 0 {
			continue
		}
		renderedAttention = true
		fmt.Fprintf(builder, "  %s\n", group.Name)
		for _, worker := range workersInGroup {
			renderWorkerRow(builder, state, row, worker)
		}
	}
	if !renderedAttention {
		renderEmpty(builder, "No running or unhealthy workers.")
	}

	renderSummarySection(builder, "Healthy Worker Groups")
	for _, group := range groupWorkers(data.Workers) {
		healthy := workersWithoutAttention(group.Workers)
		if len(healthy) == 0 {
			continue
		}
		fmt.Fprintf(builder, "  %s: %d healthy\n", group.Name, len(healthy))
		for _, worker := range healthy {
			renderWorkerRow(builder, state, row, worker)
		}
	}

	if state.RawDetails && len(data.MaintenanceFindings) > 0 {
		renderAttentionSection(builder, "Open Maintenance Findings")
		for _, finding := range data.MaintenanceFindings {
			fmt.Fprintf(builder, "  %s %s  %s\n", renderSelectedMarker(*row, state.SelectedIndex), renderStatus(finding.Severity), firstNonEmpty(finding.Summary, finding.FindingKey, "-"))
			(*row)++
		}
	}
}

func renderWorkerRow(builder *strings.Builder, state ScreenState, row *int, worker workers.WorkerListItem) {
	fmt.Fprintf(builder, "  %s %s  %s  %s  last_success=%s  current=%s\n",
		renderSelectedMarker(*row, state.SelectedIndex),
		worker.WorkerKey,
		renderStatus(worker.LifecycleStatus),
		renderStatus(worker.HealthStatus),
		timePtrOrDash(worker.LastSuccessAt),
		ptrOrDash(worker.CurrentRunID),
	)
	renderWorkerRuns(builder, state.Data.Background.WorkerRunsByWorker[worker.WorkerKey], state.RawDetails)
	(*row)++
}

func workersWithAttention(workerList []workers.WorkerListItem) []workers.WorkerListItem {
	result := []workers.WorkerListItem{}
	for _, worker := range workerList {
		if workerNeedsOperationalAttention(worker) {
			result = append(result, worker)
		}
	}
	return result
}

func workersWithoutAttention(workerList []workers.WorkerListItem) []workers.WorkerListItem {
	result := []workers.WorkerListItem{}
	for _, worker := range workerList {
		if !workerNeedsOperationalAttention(worker) {
			result = append(result, worker)
		}
	}
	return result
}

func workerNeedsOperationalAttention(worker workers.WorkerListItem) bool {
	if worker.AttentionRequired {
		return true
	}
	switch strings.TrimSpace(strings.ToLower(worker.HealthStatus)) {
	case workers.HealthFailed,
		workers.HealthDegraded,
		workers.HealthLagging,
		"unhealthy",
		"stale",
		"missed",
		"timed_out",
		"timeout":
		return true
	default:
		return false
	}
}

func objectStoreNeedsAttention(status string) bool {
	switch classifyStatus(status) {
	case statusClassDanger, statusClassProgress:
		return true
	default:
		return false
	}
}

func updateStatusHealthy(status update.UpdateStatus) bool {
	if !hasUpdateStatus(status) || status.Active == nil {
		return true
	}
	return classifyStatus(status.Active.Status) != statusClassDanger
}

func updateStatusLabel(status update.UpdateStatus) string {
	if !hasUpdateStatus(status) {
		return "missing"
	}
	if status.Active == nil {
		return "none"
	}
	return firstNonEmpty(status.Active.Status, "unknown")
}

type automationSurfaceGroup struct {
	Automation     automation.Automation
	Schedules      []automation.Schedule
	Endpoints      []automation.DirectEventEndpoint
	Fires          []automation.ScheduleFire
	DirectEvents   []automation.DirectEvent
	Invocations    []automation.Invocation
	ScheduleStatus string
	EndpointStatus string
	LastRunStatus  string
	NextRun        string
	FailureCount   int
}

func automationSurfaceGroups(data AutomationsData) []automationSurfaceGroup {
	groups := make([]automationSurfaceGroup, 0, len(data.Automations))
	index := map[string]int{}
	addAlias := func(alias string, idx int) {
		alias = strings.TrimSpace(alias)
		if alias != "" {
			index[alias] = idx
		}
	}
	for _, item := range data.Automations {
		idx := len(groups)
		groups = append(groups, automationSurfaceGroup{Automation: item, ScheduleStatus: "-", EndpointStatus: "-", LastRunStatus: "-", NextRun: "-"})
		addAlias(item.AutomationID, idx)
		addAlias(item.AutomationKey, idx)
		addAlias(item.DisplayName, idx)
	}
	groupIndex := func(refs ...string) (int, bool) {
		for _, ref := range refs {
			if idx, ok := index[strings.TrimSpace(ref)]; ok {
				return idx, true
			}
		}
		return 0, false
	}
	for _, schedule := range data.Schedules {
		if idx, ok := groupIndex(schedule.AutomationID, schedule.ScheduleKey, schedule.DisplayName); ok {
			group := &groups[idx]
			group.Schedules = append(group.Schedules, schedule)
			group.ScheduleStatus = firstNonEmpty(group.ScheduleStatus, schedule.Status, "-")
			if group.ScheduleStatus == "-" {
				group.ScheduleStatus = firstNonEmpty(schedule.Status, "-")
			}
			if group.NextRun == "-" {
				group.NextRun = timePtrOrDash(schedule.NextFireAt)
			}
		}
	}
	for _, endpoint := range data.DirectEventEndpoints {
		if idx, ok := groupIndex(endpoint.AutomationID, endpoint.EndpointSlug, endpoint.DisplayName); ok {
			group := &groups[idx]
			group.Endpoints = append(group.Endpoints, endpoint)
			if group.EndpointStatus == "-" {
				group.EndpointStatus = firstNonEmpty(endpoint.Status, "-")
			}
		}
	}
	for _, fire := range data.ScheduleFires {
		if idx, ok := groupIndex(fire.AutomationID, fire.ScheduleID); ok {
			group := &groups[idx]
			group.Fires = append(group.Fires, fire)
			if group.LastRunStatus == "-" {
				group.LastRunStatus = firstNonEmpty(fire.Status, "-")
			}
		}
	}
	for _, event := range data.DirectEvents {
		if idx, ok := groupIndex(event.AutomationID, event.EndpointID); ok {
			group := &groups[idx]
			group.DirectEvents = append(group.DirectEvents, event)
			if group.LastRunStatus == "-" {
				group.LastRunStatus = firstNonEmpty(event.Status, "-")
			}
		}
	}
	for _, invocation := range data.Invocations {
		if idx, ok := groupIndex(invocation.AutomationID, invocation.SourceRef); ok {
			group := &groups[idx]
			group.Invocations = append(group.Invocations, invocation)
			if group.LastRunStatus == "-" {
				group.LastRunStatus = firstNonEmpty(invocation.Status, "-")
			}
		}
	}
	for _, invocation := range data.InvocationFailures {
		if idx, ok := groupIndex(invocation.AutomationID, invocation.SourceRef); ok {
			groups[idx].FailureCount++
		}
	}
	for _, event := range data.DirectEventFailures {
		if idx, ok := groupIndex(event.AutomationID, event.EndpointID); ok {
			groups[idx].FailureCount++
		}
	}
	return groups
}

func renderAutomationFailures(builder *strings.Builder, state ScreenState, row *int) {
	data := state.Data.Automations
	renderAttentionSection(builder, "Automation Failures")
	renderMetricLine(builder,
		fmt.Sprintf("failed_invocations=%d", len(data.InvocationFailures)),
		fmt.Sprintf("direct_event_failures=%d", len(data.DirectEventFailures)),
	)
	if len(data.InvocationFailures) == 0 && len(data.DirectEventFailures) == 0 {
		renderEmpty(builder, "No automation failures returned by the backend.")
		return
	}
	renderEmpty(builder, fmt.Sprintf("Doctor owns %d automation issue(s). Use Controls/Logs groups here for explicit operator actions.", len(data.InvocationFailures)+len(data.DirectEventFailures)))
	if !state.RawDetails {
		renderEmpty(builder, "Press tab for failed invocation and direct-event rows.")
		return
	}
	for _, invocation := range data.InvocationFailures {
		fmt.Fprintf(builder, "  %s invocation %s  %s  capability=%s\n", renderSelectedMarker(*row, state.SelectedIndex), invocation.InvocationID, renderStatus(invocation.Status), firstNonEmpty(invocation.TargetCapability, "-"))
		(*row)++
	}
	for _, event := range data.DirectEventFailures {
		fmt.Fprintf(builder, "  %s event %s  %s  path=%s\n", renderSelectedMarker(*row, state.SelectedIndex), event.DirectEventID, renderStatus(event.Status), firstNonEmpty(event.RequestPath, "-"))
		(*row)++
	}
}

func renderJobsByStatusSection(builder *strings.Builder, title string, jobList []jobs.Job, state ScreenState, row *int) {
	tier := portalSectionPrimary
	if title == "Failed Jobs" {
		tier = portalSectionAttention
	}
	renderSectionWithTier(builder, tier, title)
	if len(jobList) == 0 {
		renderEmpty(builder, "No "+strings.ToLower(title)+" returned by the backend.")
		return
	}
	for _, job := range jobList {
		renderJobRow(builder, *row, state.SelectedIndex, job, state.RawDetails)
		(*row)++
	}
}

func jobsWithStatus(jobList []jobs.Job, status string) []jobs.Job {
	result := []jobs.Job{}
	for _, job := range jobList {
		if strings.EqualFold(strings.TrimSpace(job.Status), status) {
			result = append(result, job)
		}
	}
	return result
}

func jobsWithoutStatus(jobList []jobs.Job, status string) []jobs.Job {
	result := []jobs.Job{}
	for _, job := range jobList {
		if !strings.EqualFold(strings.TrimSpace(job.Status), status) {
			result = append(result, job)
		}
	}
	return result
}

func renderAvailableActions(builder *strings.Builder, selectedIndex int, startRow int, actions ...PortalAction) {
	if len(actions) == 0 {
		return
	}
	renderSection(builder, "Available Actions")
	row := startRow
	for _, action := range actions {
		status := renderRisk(action.Risk)
		if action.Disabled() {
			status = renderStatus("disabled")
		}
		target := ""
		if action.TargetLabel != "" && action.TargetLabel != action.Label {
			target = "  " + action.TargetLabel
		}
		fmt.Fprintf(builder, "  %s %s%s  %s\n", renderSelectedMarker(row, selectedIndex), action.Label, target, status)
		if action.Disabled() && action.DisabledReason != "" {
			fmt.Fprintf(builder, "      %s\n", action.DisabledReason)
		}
		row++
	}
}

func renderTopAvailableActions(builder *strings.Builder, state ScreenState, records []SelectableItem) int {
	actions := screenTopAvailableActionsForRecords(state, records)
	renderAvailableActions(builder, state.SelectedIndex, 0, actions...)
	return len(actions)
}

func renderOperationalActionGroups(builder *strings.Builder, state ScreenState, records []SelectableItem, row *int) {
	hasRows := false
	for _, item := range records {
		if selectableIsOperationalActionPresentation(item) {
			hasRows = true
			break
		}
	}
	if !hasRows {
		return
	}
	renderSection(builder, "Available Actions")
	lastSection := ""
	for _, item := range records {
		switch {
		case selectableIsOperationalActionGroup(item):
			renderOperationalActionSubsection(builder, item, &lastSection)
			count := len(item.RelatedActions)
			stateLabel := "enter"
			if state.ExpandedActionGroup == item.RecordRef {
				stateLabel = "open"
			}
			fmt.Fprintf(builder, "    %s %s  %s  %s\n",
				renderSelectedMarker(*row, state.SelectedIndex),
				item.Label,
				portalRenderContext().Styles.Muted.Render(fmt.Sprintf("%d actions", count)),
				portalRenderContext().Styles.Muted.Render(stateLabel),
			)
			if item.Description != "" {
				fmt.Fprintf(builder, "        %s\n", portalRenderContext().Styles.Muted.Render(item.Description))
			}
			(*row)++
		case selectableIsOperationalActionGroupAction(item):
			renderOperationalActionSubsection(builder, item, &lastSection)
			action := PortalAction{}
			if item.PrimaryAction != nil {
				action = *item.PrimaryAction
			}
			status := renderRisk(action.Risk)
			if action.Disabled() {
				status = renderStatus("disabled")
			}
			fmt.Fprintf(builder, "      %s %s  %s  %s\n",
				renderSelectedMarker(*row, state.SelectedIndex),
				firstNonEmpty(item.Label, action.Label),
				portalRenderContext().Styles.Muted.Render(firstNonEmpty(action.TargetLabel, action.TargetRef, "-")),
				status,
			)
			if action.Disabled() && action.DisabledReason != "" {
				fmt.Fprintf(builder, "          %s\n", portalRenderContext().Styles.Disabled.Render(action.DisabledReason))
			}
			(*row)++
		case selectableIsOperationalActionDirect(item):
			renderOperationalActionSubsection(builder, item, &lastSection)
			action := PortalAction{}
			if item.PrimaryAction != nil {
				action = *item.PrimaryAction
			}
			status := renderRisk(action.Risk)
			if action.Disabled() {
				status = renderStatus("disabled")
			}
			target := ""
			if action.TargetLabel != "" && action.TargetLabel != action.Label {
				target = "  " + action.TargetLabel
			}
			fmt.Fprintf(builder, "    %s %s%s  %s\n",
				renderSelectedMarker(*row, state.SelectedIndex),
				firstNonEmpty(item.Label, action.Label),
				target,
				status,
			)
			if action.Disabled() && action.DisabledReason != "" {
				fmt.Fprintf(builder, "        %s\n", portalRenderContext().Styles.Disabled.Render(action.DisabledReason))
			}
			(*row)++
		default:
			return
		}
	}
}

func renderOperationalActionSubsection(builder *strings.Builder, item SelectableItem, lastSection *string) {
	label := firstNonEmpty(item.ActionSectionLabel, portalActionSectionLabel(item.ActionSection), "Actions")
	if label == "" || *lastSection == label {
		return
	}
	fmt.Fprintf(builder, "  %s\n", portalRenderContext().Styles.ActionSubsection.Render(label))
	*lastSection = label
}

func contentSelectableItems(items []SelectableItem) []SelectableItem {
	result := make([]SelectableItem, 0, len(items))
	for _, item := range items {
		if selectableIsOperationalActionPresentation(item) {
			continue
		}
		result = append(result, item)
	}
	return result
}

func selectedContentIndex(items []SelectableItem, selectedIndex int) int {
	for idx, item := range items {
		if item.RowIndex == selectedIndex {
			return idx
		}
	}
	return 0
}

func renderCapabilitiesExplorer(builder *strings.Builder, state ScreenState) {
	data := state.Data.Capabilities
	explorer := normalizeCapabilityExplorerState(data)

	renderCapabilityTaskSummary(builder, data)
	renderSection(builder, "Capability Explorer")
	fmt.Fprintf(builder, "  %s\n", portalRenderContext().Styles.Muted.Render(capabilityExplorerBreadcrumb(explorer)))
	items := ScreenRecordItems(state)
	if len(items) == 0 {
		if len(data.Providers) == 0 {
			renderEmpty(builder, "No providers returned by the backend.")
		}
		if len(data.Capabilities) == 0 {
			renderEmpty(builder, "No capabilities returned by the backend.")
		}
	} else {
		start, end := visibleCapabilityExplorerWindow(len(items), state.SelectedIndex, capabilityExplorerVisibleRowLimit(portalRenderContext().Height))
		if start > 0 || end < len(items) {
			renderEmpty(builder, fmt.Sprintf("Showing %d-%d of %d. Use j/k or arrow keys to move.", start+1, end, len(items)))
		}
		if start > 0 {
			renderEmpty(builder, fmt.Sprintf("%d rows above", start))
		}
		for idx := start; idx < end; idx++ {
			renderCapabilityExplorerSelectableItem(builder, items[idx], idx, state.SelectedIndex, state.RawDetails, explorer)
		}
		if end < len(items) {
			renderEmpty(builder, fmt.Sprintf("%d rows below", len(items)-end))
		}
	}

	if explorer.Level == CapabilityExplorerNodes {
		advertisements := capabilityAdvertisementRows(data)
		attentionAdvertisements := capabilityAdvertisementAttentionRows(data)
		if len(attentionAdvertisements) > 0 || state.RawDetails {
			renderSection(builder, "Provider Advertisements")
		}
		if len(attentionAdvertisements) > 0 && !state.RawDetails {
			for _, row := range attentionAdvertisements {
				renderCapabilityExplorerRow(builder, row, -1, state.SelectedIndex, false)
			}
			if hidden := len(advertisements) - len(attentionAdvertisements); hidden > 0 {
				renderEmpty(builder, fmt.Sprintf("%d non-actionable provider advertisements hidden. Press tab for details.", hidden))
			}
		} else if state.RawDetails {
			if len(advertisements) == 0 {
				renderEmpty(builder, "No provider advertisements returned by the backend.")
				return
			}
			limit := 5
			if len(advertisements) < limit {
				limit = len(advertisements)
			}
			for idx := 0; idx < limit; idx++ {
				renderCapabilityExplorerRow(builder, advertisements[idx], -1, state.SelectedIndex, true)
			}
			if len(advertisements) > limit {
				renderEmpty(builder, fmt.Sprintf("%d more provider advertisements hidden.", len(advertisements)-limit))
			}
		}
	}
}

func capabilityExplorerVisibleRowLimit(height int) int {
	if height <= 0 {
		return 0
	}
	limit := height - 14
	if limit < 4 {
		return 4
	}
	if limit > 20 {
		return 20
	}
	return limit
}

func visibleCapabilityExplorerWindow(total int, selected int, limit int) (int, int) {
	if total <= 0 {
		return 0, 0
	}
	if limit <= 0 || total <= limit {
		return 0, total
	}
	selected = clampIndex(selected, total)
	start := selected - limit/2
	if start < 0 {
		start = 0
	}
	end := start + limit
	if end > total {
		end = total
		start = end - limit
		if start < 0 {
			start = 0
		}
	}
	return start, end
}

func renderCapabilityExplorerSelectableItem(builder *strings.Builder, item SelectableItem, index int, selected int, rawDetails bool, explorer CapabilityExplorerState) {
	marker := renderSelectedMarker(index, selected)
	width := portalRenderContext().Width
	switch item.RecordKind {
	case "capability_scope":
		fmt.Fprintf(builder, "  %s %s  %s\n",
			marker,
			trimForWidth(firstNonEmpty(item.RecordLabel, item.Label), usableWidth(width, 20)),
			portalRenderContext().Styles.Muted.Render(item.Description),
		)
		if rawDetails {
			fmt.Fprintf(builder, "      scope=%s\n", firstNonEmpty(item.RecordRef, "-"))
		}
	case "provider":
		status := capabilityItemStatus(item)
		hint := "space actions"
		if explorer.ExpandedRef == item.RecordRef {
			hint = "actions open"
		}
		fmt.Fprintf(builder, "  %s %s  %s  %s  %s\n",
			marker,
			trimForWidth(firstNonEmpty(item.RecordRef, item.RecordLabel, item.Label), usableWidth(width, 44)),
			portalRenderContext().Styles.Muted.Render(item.Description),
			renderStatus(status),
			portalRenderContext().Styles.Muted.Render(hint),
		)
		if rawDetails {
			renderCapabilityItemRawDetails(builder, item)
		}
	case "capability":
		status := capabilityItemStatus(item)
		hint := "space actions"
		if explorer.ExpandedRef == item.RecordRef {
			hint = "actions open"
		}
		fmt.Fprintf(builder, "  %s %s  %s  %s  %s\n",
			marker,
			trimForWidth(firstNonEmpty(item.RecordRef, item.RecordLabel, item.Label), usableWidth(width, 44)),
			portalRenderContext().Styles.Muted.Render(item.Description),
			renderStatus(status),
			portalRenderContext().Styles.Muted.Render(hint),
		)
		if rawDetails {
			renderCapabilityItemRawDetails(builder, item)
		}
	case "capability_inline_action":
		action := PortalAction{}
		if item.PrimaryAction != nil {
			action = *item.PrimaryAction
		}
		status := renderRisk(action.Risk)
		if action.Disabled() {
			status = renderStatus("disabled")
		}
		fmt.Fprintf(builder, "    %s %s  %s  %s\n",
			marker,
			firstNonEmpty(item.Label, action.Label),
			portalRenderContext().Styles.Muted.Render(firstNonEmpty(item.RecordLabel, action.TargetLabel, action.TargetRef)),
			status,
		)
		if item.Description != "" {
			fmt.Fprintf(builder, "      %s\n", portalRenderContext().Styles.Muted.Render(item.Description))
		}
		if action.Disabled() && action.DisabledReason != "" {
			fmt.Fprintf(builder, "      %s\n", portalRenderContext().Styles.Disabled.Render(action.DisabledReason))
		}
		if rawDetails {
			fmt.Fprintf(builder, "      action_id=%s raw=%s\n", firstNonEmpty(action.ID, item.ActionID, "-"), strings.Join(action.RawCommand, " "))
		}
	default:
		fmt.Fprintf(builder, "  %s %s  %s\n", marker, firstNonEmpty(item.RecordLabel, item.Label), portalRenderContext().Styles.Muted.Render(item.Description))
	}
}

func capabilityItemStatus(item SelectableItem) string {
	for _, action := range appendCapabilityItemActions(item) {
		if status := strings.TrimSpace(action.RawDetails["status"]); status != "" {
			return status
		}
		if health := strings.TrimSpace(action.RawDetails["health"]); health != "" {
			return health
		}
	}
	return "-"
}

func appendCapabilityItemActions(item SelectableItem) []PortalAction {
	actions := make([]PortalAction, 0, 1+len(item.RelatedActions))
	if item.PrimaryAction != nil {
		actions = append(actions, *item.PrimaryAction)
	}
	actions = append(actions, item.RelatedActions...)
	return actions
}

func renderCapabilityItemRawDetails(builder *strings.Builder, item SelectableItem) {
	for _, action := range appendCapabilityItemActions(item) {
		if len(action.RawDetails) == 0 {
			continue
		}
		switch item.RecordKind {
		case "provider":
			fmt.Fprintf(builder, "      provider_id=%s node=%s scope=%s health=%s availability=%s\n",
				firstNonEmpty(action.RawDetails["provider_id"], "-"),
				firstNonEmpty(action.RawDetails["node"], "-"),
				firstNonEmpty(action.RawDetails["scope"], "-"),
				firstNonEmpty(action.RawDetails["health"], "-"),
				firstNonEmpty(action.RawDetails["availability"], "-"),
			)
		case "capability":
			fmt.Fprintf(builder, "      endpoint_id=%s class=%s provider=%s provider_health=%s form=%s risk=%s auth=%s\n",
				firstNonEmpty(action.RawDetails["capability_id"], "-"),
				firstNonEmpty(action.RawDetails["class"], "-"),
				firstNonEmpty(action.RawDetails["provider_address"], "-"),
				firstNonEmpty(action.RawDetails["provider_health"], "-"),
				firstNonEmpty(action.RawDetails["form"], "-"),
				firstNonEmpty(action.RawDetails["risk"], "-"),
				firstNonEmpty(action.RawDetails["auth_level"], "-"),
			)
		}
		return
	}
	fmt.Fprintf(builder, "      kind=%s ref=%s\n", firstNonEmpty(item.RecordKind, "-"), firstNonEmpty(item.RecordRef, "-"))
}

func renderCapabilityExplorerRow(builder *strings.Builder, row CapabilityExplorerRow, index int, selected int, rawDetails bool) {
	switch row.Kind {
	case CapabilityExplorerRowScope:
		fmt.Fprintf(builder, "  %s %s  %s\n",
			renderSelectedMarker(index, selected),
			row.Label,
			portalRenderContext().Styles.Muted.Render(row.Description),
		)
		if rawDetails {
			fmt.Fprintf(builder, "      scope=%s providers=%d capabilities=%d\n", row.Scope, row.ProviderCount, row.CapabilityCount)
		}
	case CapabilityExplorerRowProvider:
		status := row.Status
		if row.Provider != nil {
			status = firstNonEmpty(row.Provider.Provider.Status, row.Provider.HealthStatus, row.Status, "-")
		}
		fmt.Fprintf(builder, "  %s %s  %s  %s\n",
			renderSelectedMarker(index, selected),
			firstNonEmpty(row.ProviderAddress, row.Address, row.Label),
			portalRenderContext().Styles.Muted.Render(row.Description),
			renderStatus(status),
		)
		if rawDetails && row.Provider != nil {
			fmt.Fprintf(builder, "      provider_id=%s node=%s scope=%s health=%s availability=%s\n",
				firstNonEmpty(row.Provider.Provider.ProviderID, "-"),
				firstNonEmpty(row.Provider.Provider.NodeID, "-"),
				firstNonEmpty(row.Provider.Provider.ScopeID, "-"),
				firstNonEmpty(row.Provider.HealthStatus, "-"),
				firstNonEmpty(row.Provider.AvailabilityStatus, "-"),
			)
		}
	case CapabilityExplorerRowCapability:
		risk := "-"
		auth := 0
		form := "-"
		if row.Capability != nil {
			risk = firstNonEmpty(row.Capability.CapabilityEndpoint.RiskLevel, "-")
			auth = row.Capability.CapabilityEndpoint.ExecutionAuthorizationLevel
			form = firstNonEmpty(row.Capability.CapabilityEndpoint.Form, "-")
		}
		fmt.Fprintf(builder, "  %s %s  %s  form=%s  risk=%s  auth=%d  %s\n",
			renderSelectedMarker(index, selected),
			firstNonEmpty(row.Address, row.Label),
			portalRenderContext().Styles.Muted.Render(row.Description),
			form,
			risk,
			auth,
			renderStatus(row.Status),
		)
		if rawDetails && row.Capability != nil {
			fmt.Fprintf(builder, "      endpoint_id=%s class=%s provider=%s provider_health=%s\n",
				firstNonEmpty(row.Capability.CapabilityEndpoint.CapabilityEndpointID, "-"),
				firstNonEmpty(row.Capability.ClassName, "-"),
				firstNonEmpty(row.Capability.ProviderAddress, "-"),
				firstNonEmpty(row.Capability.ProviderHealth, "-"),
			)
		}
	case CapabilityExplorerRowAdvertisement:
		if row.Advertisement == nil {
			return
		}
		advertisement := *row.Advertisement
		fmt.Fprintf(builder, "  %s %s  node=%s  status=%s  received=%s\n",
			renderSelectedMarker(index, selected),
			firstNonEmpty(advertisement.ProviderAdvertisementID, "-"),
			nodeFriendlyName(advertisement.OriginNodeID),
			renderStatus(firstNonEmpty(advertisement.Status, "-")),
			timeOrDash(advertisement.ReceivedAt),
		)
		if rawDetails {
			fmt.Fprintf(builder, "      provider_id=%s hash=%s rejection=%s\n",
				stringPtrOrDash(advertisement.ProviderID),
				firstNonEmpty(advertisement.AdvertisementHash, "-"),
				firstNonEmpty(advertisement.RejectionReason, "-"),
			)
		}
	}
}

func capabilityExplorerBreadcrumb(explorer CapabilityExplorerState) string {
	switch explorer.Level {
	case CapabilityExplorerProviders:
		return "Capabilities / " + firstNonEmpty(explorer.SelectedScope, "-")
	case CapabilityExplorerCapabilities:
		provider := firstNonEmpty(explorer.SelectedProvider, "-")
		return "Capabilities / " + firstNonEmpty(explorer.SelectedScope, scopeFromAddress(provider), "-") + " / " + firstNonEmpty(capabilityProviderKeyFromAddress(provider), provider)
	default:
		return "Capabilities"
	}
}

func renderMaintenanceDetails(builder *strings.Builder, state ScreenState, row *int) {
	data := state.Data.Background

	renderDetailsSection(builder, "Database Maintenance")
	if !hasDBStatus(data.DBStatus) {
		renderEmpty(builder, "No database maintenance status returned by the backend.")
	} else {
		fmt.Fprintf(builder, "  %s status=%s  migration=%s  current=%d latest=%d pending=%d\n",
			renderSelectedMarker(*row, state.SelectedIndex),
			renderStatus(firstNonEmpty(data.DBStatus.Status, "-")),
			renderStatus(firstNonEmpty(data.DBStatus.MigrationStatus, "-")),
			data.DBStatus.CurrentVersion,
			data.DBStatus.LatestVersion,
			data.DBStatus.Pending,
		)
		if data.DBStatus.LatestRun != nil {
			fmt.Fprintf(builder, "      latest_run=%s status=%s started=%s\n",
				firstNonEmpty(data.DBStatus.LatestRun.WorkerRunID, "-"),
				renderStatus(firstNonEmpty(data.DBStatus.LatestRun.RunStatus, "-")),
				timeOrDash(data.DBStatus.LatestRun.StartedAt),
			)
		}
		if len(data.DBStatus.Warnings) > 0 {
			fmt.Fprintf(builder, "      warnings=%d next=loom maintenance retention dry-run --json\n", len(data.DBStatus.Warnings))
		}
		if state.RawDetails && len(data.DBStatus.Retention.Plans) > 0 {
			for idx, plan := range data.DBStatus.Retention.Plans {
				if idx >= 4 {
					fmt.Fprintf(builder, "      %d more retention plan row(s) hidden\n", len(data.DBStatus.Retention.Plans)-idx)
					break
				}
				fmt.Fprintf(builder, "      retention %s candidates=%d keep=%s\n", plan.Table, plan.CandidateRows, plan.KeepRule)
			}
		}
		(*row)++
	}

	renderDiagnosticsSection(builder, "Object Store")
	if !hasObjectStoreStatus(data.ObjectStoreStatus) {
		renderEmpty(builder, "No object-store status returned by the backend.")
		return
	}
	fmt.Fprintf(builder, "  %s status=%s  total=%d verified=%d pending=%d missing=%d corrupt=%d findings=%d\n",
		renderSelectedMarker(*row, state.SelectedIndex),
		renderStatus(firstNonEmpty(data.ObjectStoreStatus.Status, "-")),
		data.ObjectStoreStatus.BlobCounts.Total,
		data.ObjectStoreStatus.BlobCounts.Verified,
		data.ObjectStoreStatus.BlobCounts.Pending,
		data.ObjectStoreStatus.BlobCounts.Missing,
		data.ObjectStoreStatus.BlobCounts.Corrupt,
		data.ObjectStoreStatus.Findings.Open,
	)
	if data.ObjectStoreStatus.LatestRun != nil {
		fmt.Fprintf(builder, "      latest_scan=%s status=%s started=%s\n",
			firstNonEmpty(data.ObjectStoreStatus.LatestRun.WorkerRunID, "-"),
			renderStatus(firstNonEmpty(data.ObjectStoreStatus.LatestRun.RunStatus, "-")),
			timeOrDash(data.ObjectStoreStatus.LatestRun.StartedAt),
		)
	}
	(*row)++
}

func renderCloudMaintenanceDetails(builder *strings.Builder, state ScreenState, row *int) {
	data := state.Data.Background

	renderSection(builder, "Cloud Snapshots")
	if !data.CloudStatusAvailable {
		renderEmpty(builder, "No cloud snapshot status returned.")
		return
	}
	status := data.CloudStatus
	latestSnapshot := "-"
	latestArchive := "-"
	latestUpload := "-"
	workerStatus := "-"
	if worker := workerByKey(data.Workers, "main.cloud_snapshot_upload"); worker.WorkerKey != "" {
		latestUpload = timePtrOrDash(worker.LastSuccessAt)
		workerStatus = firstNonEmpty(worker.HealthStatus, worker.LifecycleStatus, "-")
	}
	backend := firstNonEmpty(status.Config.SnapshotBackend, "-")
	backendStatus := "-"
	if status.SnapshotStore != nil {
		backendStatus = firstNonEmpty(status.SnapshotStore.Status, "-")
	}
	fmt.Fprintf(builder, "  status=%s  provider=%s  backend=%s/%s  latest_snapshot=%s  latest_archive=%s  worker=%s  latest_upload=%s  restore_drill=%s  findings=%d\n",
		renderStatus(firstNonEmpty(status.Status, "-")),
		firstNonEmpty(status.Config.Provider, "-"),
		backend,
		renderStatus(backendStatus),
		latestSnapshot,
		latestArchive,
		renderStatus(workerStatus),
		latestUpload,
		renderStatus("not_tracked"),
		cloudRootFindingCount(data),
	)
	if state.RawDetails {
		fmt.Fprintf(builder, "      enabled=%t mode=%s remote=%s snapshot_inventory=explicit_live_action main_snapshots_root=%s full_offload_root=%s cloud_folder_root=%s\n",
			status.Config.Enabled,
			firstNonEmpty(status.Mode, "cached"),
			firstNonEmpty(status.Config.RemoteName, "-"),
			firstNonEmpty(status.Config.MainSnapshotsRoot, "-"),
			firstNonEmpty(status.Config.FullOffloadRoot, "-"),
			firstNonEmpty(status.Config.CloudFolderRoot, "-"),
		)
		if status.RemoteState != nil {
			fmt.Fprintf(builder, "      remote_state=%s last_success=%s last_failure=%s next_live_check=%s last_error=%s\n",
				renderStatus(firstNonEmpty(status.RemoteState.State, "-")),
				timePtrOrDash(status.RemoteState.LastSuccessAt),
				timePtrOrDash(status.RemoteState.LastFailureAt),
				timePtrOrDash(status.RemoteState.NextLiveCheckAfter),
				firstNonEmpty(status.RemoteState.LastErrorClass, "-"),
			)
		}
		if status.SnapshotStore != nil {
			fmt.Fprintf(builder, "      snapshot_store initialized=%t repository=%s archive_count=%d\n",
				status.SnapshotStore.Initialized,
				firstNonEmpty(status.SnapshotStore.Repository, "-"),
				status.SnapshotStore.ArchiveCount,
			)
		}
		for _, root := range status.Roots {
			fmt.Fprintf(builder, "      root=%s status=%s prefix=%s\n",
				firstNonEmpty(root.Name, "-"),
				renderStatus(firstNonEmpty(root.Status, "-")),
				firstNonEmpty(root.Prefix, "-"),
			)
		}
	}
}

func renderWorkerRuns(builder *strings.Builder, runs []workers.WorkerRun, rawDetails bool) {
	if len(runs) == 0 {
		return
	}
	limit := 1
	if rawDetails {
		limit = len(runs)
	}
	for idx, run := range runs {
		if idx >= limit {
			break
		}
		if rawDetails {
			fmt.Fprintf(builder, "      run=%s status=%s trigger=%s started=%s finished=%s\n",
				firstNonEmpty(run.WorkerRunID, "-"),
				renderStatus(firstNonEmpty(run.RunStatus, "-")),
				firstNonEmpty(run.TriggerKind, "-"),
				timeOrDash(run.StartedAt),
				timePtrOrDash(run.FinishedAt),
			)
		} else {
			fmt.Fprintf(builder, "      last_run=%s trigger=%s at=%s\n",
				renderStatus(firstNonEmpty(run.RunStatus, "-")),
				firstNonEmpty(run.TriggerKind, "-"),
				timeOrDash(run.StartedAt),
			)
		}
	}
}

func renderJobRow(builder *strings.Builder, index, selected int, job jobs.Job, rawDetails bool) {
	fmt.Fprintf(builder, "  %s %s  %s  type=%s  attempts=%d/%d\n",
		renderSelectedMarker(index, selected),
		firstNonEmpty(job.JobID, "-"),
		renderStatus(firstNonEmpty(job.Status, "-")),
		firstNonEmpty(job.JobType, "-"),
		job.AttemptCount,
		job.MaxAttempts,
	)
	if rawDetails {
		fmt.Fprintf(builder, "      target=%s:%s script=%s worker_run=%s created=%s\n",
			stringPtrOrDash(job.TargetKind),
			stringPtrOrDash(job.TargetID),
			stringPtrOrDash(job.ScriptID),
			stringPtrOrDash(job.LastWorkerRunID),
			timeOrDash(job.CreatedAt),
		)
	}
}

func renderIndexStatusRow(builder *strings.Builder, index, selected int, status search.IndexStatus, rawDetails bool) {
	fmt.Fprintf(builder, "  %s %s  %s  %s  object=%s  error=%s\n",
		renderSelectedMarker(index, selected),
		firstNonEmpty(status.IndexStatusID, "-"),
		renderStatus(firstNonEmpty(status.Status, "-")),
		firstNonEmpty(status.IndexType, "-"),
		firstNonEmpty(status.ObjectID, "-"),
		firstNonEmpty(status.LastErrorCode, status.LastErrorMessage, "-"),
	)
	if rawDetails {
		fmt.Fprintf(builder, "      source=%s:%s version=%s attempts=%d worker_run=%s\n",
			firstNonEmpty(status.SourceKind, "-"),
			firstNonEmpty(status.SourceID, "-"),
			firstNonEmpty(status.SourceVersionID, "-"),
			status.AttemptCount,
			firstNonEmpty(status.LastWorkerRunID, "-"),
		)
	}
}

type workerGroup struct {
	Name    string
	Workers []workers.WorkerListItem
}

func groupWorkers(workerList []workers.WorkerListItem) []workerGroup {
	groupByName := map[string][]workers.WorkerListItem{}
	for _, worker := range workerList {
		name := workerGroupName(worker.WorkerKind)
		groupByName[name] = append(groupByName[name], worker)
	}
	order := []string{
		"Main maintenance",
		"Automation",
		"Search and indexing",
		"Jobs and scripts",
		"Workspace nodes",
		"Platform",
	}
	groups := make([]workerGroup, 0, len(order))
	for _, name := range order {
		workersInGroup := groupByName[name]
		if len(workersInGroup) == 0 {
			continue
		}
		groups = append(groups, workerGroup{Name: name, Workers: workersInGroup})
	}
	return groups
}

func degradedWorkerCount(workerList []workers.WorkerListItem) int {
	count := 0
	for _, worker := range workerList {
		if workerNeedsOperationalAttention(worker) {
			count++
		}
	}
	return count
}

func workerByKey(workerList []workers.WorkerListItem, key string) workers.WorkerListItem {
	for _, worker := range workerList {
		if worker.WorkerKey == key {
			return worker
		}
	}
	return workers.WorkerListItem{}
}

func cloudRootFindingCount(data BackgroundData) int {
	count := 0
	switch data.CloudStatus.Status {
	case "unreachable", "lock_busy":
		count++
	}
	for _, root := range data.CloudStatus.Roots {
		if root.Status != "" && root.Status != "present" {
			count++
		}
	}
	if data.CloudStatus.SnapshotStore != nil && data.CloudStatus.SnapshotStore.Status != "" && data.CloudStatus.SnapshotStore.Status != "ok" {
		count++
	}
	return count
}

func workerGroupName(kind string) string {
	switch kind {
	case workers.KindDBMaintenance, workers.KindMainBackup, workers.KindCloudSnapshotUpload, workers.KindObjectStore:
		return "Main maintenance"
	case workers.KindAutomationScheduler, workers.KindAutomationDispatcher, workers.KindDirectEventIngest:
		return "Automation"
	case workers.KindIndexerText:
		return "Search and indexing"
	case workers.KindJobRunner, workers.KindJobSweeper:
		return "Jobs and scripts"
	case "watched_root_scanner", "watched_root_sync", "watched_root_backup":
		return "Workspace nodes"
	default:
		return "Platform"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func ptrOrDash(value *string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return "-"
	}
	return *value
}

func stringPtrOrDash(value *string) string {
	return ptrOrDash(value)
}

func intPtrOrDash(value *int) string {
	if value == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *value)
}

func timePtrOrDash(value *time.Time) string {
	if value == nil || value.IsZero() {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}

func timeOrDash(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}
