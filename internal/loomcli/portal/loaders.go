package portal

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/cloudstorage"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
	"loom.local/loom/internal/routing"
	"loom.local/loom/internal/search"
	"loom.local/loom/internal/serviceregistry"
	loomsync "loom.local/loom/internal/sync"
	"loom.local/loom/internal/update"
	mainwatchedroots "loom.local/loom/internal/watchedroots"
	"loom.local/loom/internal/workers"
)

type ScreenLoadResult struct {
	Screen        string
	Snapshot      Snapshot
	State         ScreenState
	PartialErrors []SnapshotError
	Err           error
	LoadedAt      time.Time
}

type screenLoadedMsg ScreenLoadResult

func LoadScreenFromSnapshot(screen string, snapshot Snapshot) ScreenLoadResult {
	screen = NormalizeScreen(screen)
	state := ScreenStateFromSnapshot(screen, snapshot)
	return ScreenLoadResult{
		Screen:        screen,
		Snapshot:      snapshot,
		State:         state,
		PartialErrors: state.PartialErrors,
		LoadedAt:      state.LoadedAt,
	}
}

func LoadScreen(ctx context.Context, client Client, correlationID, screen string, fallback Snapshot, options ...SnapshotOptions) ScreenLoadResult {
	screen = NormalizeScreen(screen)
	snapshotOptions := normalizeSnapshotOptions(options...)
	if client == nil {
		result := LoadScreenFromSnapshot(screen, fallback)
		result.Err = ErrMissingClientFor("load " + screen + " screen")
		result.State.Status = ScreenLoadFailed
		result.State.Error = result.Err.Error()
		return result
	}
	if fallback.MainAvailability.State == MainAvailabilityOffline {
		return LoadScreenFromSnapshot(screen, fallback)
	}

	switch screen {
	case ScreenHome:
		return loadHomeScreen(ctx, client, correlationID, fallback, snapshotOptions)
	case ScreenDoctor:
		return loadDoctorScreen(ctx, client, correlationID, fallback, snapshotOptions)
	case ScreenTimeline:
		return loadTimelineScreen(ctx, client, correlationID, fallback, snapshotOptions)
	case ScreenBox:
		return loadBoxScreen(ctx, client, correlationID, fallback, snapshotOptions)
	case ScreenNotes:
		return loadNotesScreen(ctx, client, correlationID, fallback)
	case ScreenStorage:
		return loadStorageScreen(ctx, client, correlationID, fallback)
	case ScreenProjects:
		return loadProjectsScreen(ctx, client, correlationID, fallback, snapshotOptions)
	case ScreenServices:
		return loadServicesScreen(ctx, client, correlationID, fallback)
	case ScreenBackground:
		return loadBackgroundScreen(ctx, client, correlationID, fallback, snapshotOptions)
	case ScreenAutomations:
		return loadAutomationsScreen(ctx, client, correlationID, fallback)
	case ScreenJobs:
		return loadJobsScreen(ctx, client, correlationID, fallback)
	case ScreenNodes:
		return loadNodesScreen(ctx, client, correlationID, fallback)
	case ScreenCapabilities:
		return loadCapabilitiesScreen(ctx, client, correlationID, fallback)
	case ScreenDatabase:
		return loadDatabaseScreen(ctx, client, correlationID, fallback)
	default:
		return loadHomeScreen(ctx, client, correlationID, fallback, snapshotOptions)
	}
}

func loadDoctorScreen(ctx context.Context, client Client, correlationID string, fallback Snapshot, options SnapshotOptions) ScreenLoadResult {
	snapshot, err := CollectSnapshot(ctx, client, correlationID, options)
	if err != nil {
		state := NewScreenState(ScreenDoctor)
		state.Status = ScreenLoadFailed
		state.Error = err.Error()
		return ScreenLoadResult{
			Screen:   ScreenDoctor,
			Snapshot: fallback,
			State:    state,
			Err:      err,
			LoadedAt: time.Now().UTC(),
		}
	}
	return LoadScreenFromSnapshot(ScreenDoctor, snapshot)
}

func loadScreenCmd(client Client, correlationID, screen string, fallback Snapshot, options ...SnapshotOptions) tea.Cmd {
	snapshotOptions := normalizeSnapshotOptions(options...)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return screenLoadedMsg(LoadScreen(ctx, client, correlationID, screen, fallback, snapshotOptions))
	}
}

func refreshScreenCmd(client Client, correlationID, screen string, fallback Snapshot, options ...SnapshotOptions) tea.Cmd {
	snapshotOptions := normalizeSnapshotOptions(options...)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		snapshot, err := CollectSnapshot(ctx, client, correlationID, snapshotOptions)
		if err != nil {
			state := NewScreenState(screen)
			state.Status = ScreenLoadFailed
			state.Error = err.Error()
			state.MainAvailability = fallback.MainAvailability
			return screenLoadedMsg(ScreenLoadResult{
				Screen:   NormalizeScreen(screen),
				Snapshot: fallback,
				State:    state,
				Err:      err,
				LoadedAt: time.Now().UTC(),
			})
		}
		if snapshot.MainAvailability.State == MainAvailabilityOffline || !mainOwnedScreen(screen) {
			return screenLoadedMsg(LoadScreenFromSnapshot(screen, snapshot))
		}
		return screenLoadedMsg(LoadScreen(ctx, client, correlationID, screen, snapshot, snapshotOptions))
	}
}

func loadProjectsScreenCmd(client Client, correlationID string, fallback Snapshot, selectedProjectRef string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return screenLoadedMsg(loadProjectsScreenWithSelection(ctx, client, correlationID, fallback, selectedProjectRef))
	}
}

type screenLoadBuilder struct {
	screen   string
	snapshot Snapshot
	data     ScreenData
	partials []SnapshotError
	loadedAt time.Time
}

func newScreenLoadBuilder(screen string, fallback Snapshot) screenLoadBuilder {
	return screenLoadBuilder{
		screen:   NormalizeScreen(screen),
		snapshot: fallback,
		loadedAt: time.Now().UTC(),
	}
}

func (b *screenLoadBuilder) addPartial(source string, err error) {
	if err == nil {
		return
	}
	b.partials = append(b.partials, SnapshotError{Source: source, Message: err.Error()})
}

func (b screenLoadBuilder) result() ScreenLoadResult {
	state := NewScreenState(b.screen)
	state.Status = ScreenLoadLoaded
	if len(b.partials) > 0 {
		state.Status = ScreenLoadPartial
	}
	state.LoadedAt = b.loadedAt
	state.PartialErrors = b.partials
	state.MainAvailability = b.snapshot.MainAvailability
	state.Data = b.data
	return ScreenLoadResult{
		Screen:        b.screen,
		Snapshot:      b.snapshot,
		State:         state,
		PartialErrors: b.partials,
		LoadedAt:      b.loadedAt,
	}
}

func loadHomeScreen(ctx context.Context, client Client, correlationID string, fallback Snapshot, options SnapshotOptions) ScreenLoadResult {
	snapshot, err := CollectSnapshot(ctx, client, correlationID, options)
	if err != nil {
		state := NewScreenState(ScreenHome)
		state.Status = ScreenLoadFailed
		state.Error = err.Error()
		return ScreenLoadResult{
			Screen:   ScreenHome,
			Snapshot: fallback,
			State:    state,
			Err:      err,
			LoadedAt: time.Now().UTC(),
		}
	}
	return LoadScreenFromSnapshot(ScreenHome, snapshot)
}

func loadBoxScreen(ctx context.Context, client Client, correlationID string, fallback Snapshot, options SnapshotOptions) ScreenLoadResult {
	builder := newScreenLoadBuilder(ScreenBox, fallback)
	data, partials := collectBoxData(ctx, client, correlationID, options)
	for _, partial := range partials {
		builder.partials = append(builder.partials, partial)
	}
	builder.snapshot.BoxStatus = data.Status
	builder.snapshot.BoxWatchPlan = data.WatchPlan
	builder.snapshot.BoxWatchStatus = data.WatchStatus
	builder.snapshot.BoxWatchStatusAvailable = data.WatchStatusAvailable
	builder.data.Box = data
	return builder.result()
}

func loadTimelineScreen(ctx context.Context, client Client, correlationID string, fallback Snapshot, options SnapshotOptions) ScreenLoadResult {
	snapshot, err := CollectSnapshot(ctx, client, correlationID, options)
	if err != nil {
		state := NewScreenState(ScreenTimeline)
		state.Status = ScreenLoadFailed
		state.Error = err.Error()
		return ScreenLoadResult{
			Screen:   ScreenTimeline,
			Snapshot: fallback,
			State:    state,
			Err:      err,
			LoadedAt: time.Now().UTC(),
		}
	}
	builder := newScreenLoadBuilder(ScreenTimeline, snapshot)
	builder.partials = append(builder.partials, snapshot.PartialErrors...)

	timelineInput := TimelineBuildInput{
		Snapshot: snapshot,
		Background: BackgroundData{
			Workers:            snapshot.Workers,
			WorkerRunsByWorker: map[string][]workers.WorkerRun{},
			CloudStatus:        cloudstorage.StatusReport{},
		},
		Jobs: JobsData{
			JobStatus:     snapshot.JobStatus,
			Workers:       snapshot.Workers,
			IndexFailures: snapshot.IndexFailures,
		},
		Automations: AutomationsData{
			Schedules:           snapshot.Schedules,
			ScheduleStatus:      snapshot.ScheduleStatus,
			DirectEventStatus:   snapshot.DirectEventStatus,
			InvocationFailures:  snapshot.InvocationFailures,
			DirectEventFailures: snapshot.DirectEventFailures,
		},
		Storage: StorageData{LaneStatus: snapshot.BoxStatus.Lane},
	}

	for idx, worker := range snapshot.Workers {
		if idx >= 5 {
			break
		}
		if worker.WorkerKey == "" {
			continue
		}
		envelope, err := client.ListWorkerRuns(ctx, correlationID, worker.WorkerKey, workers.RunFilter{Limit: 3})
		if err != nil {
			builder.addPartial("timeline_worker_runs", err)
			continue
		}
		timelineInput.Background.WorkerRunsByWorker[worker.WorkerKey] = envelope.Data
	}
	if envelope, err := client.ListJobs(ctx, correlationID, jobs.ListFilter{Limit: 20}); err != nil {
		builder.addPartial("timeline_jobs", err)
	} else {
		timelineInput.Jobs.Jobs = envelope.Data
	}
	if envelope, err := client.ListQueuedJobs(ctx, correlationID, jobs.ListFilter{Limit: 20}); err != nil {
		builder.addPartial("timeline_queued_jobs", err)
	} else {
		timelineInput.Jobs.QueuedJobs = envelope.Data
	}
	if envelope, err := client.ListFailedJobs(ctx, correlationID, jobs.ListFilter{Limit: 20}); err != nil {
		builder.addPartial("timeline_failed_jobs", err)
	} else {
		timelineInput.Jobs.FailedJobs = envelope.Data
	}
	if envelope, err := client.ListIndexStatus(ctx, correlationID, search.StatusFilter{Limit: 20}); err != nil {
		builder.addPartial("timeline_index_status", err)
	} else {
		timelineInput.Jobs.IndexStatuses = envelope.Data
	}
	if envelope, err := client.ListIndexQueue(ctx, correlationID, search.IndexQueueFilter{Limit: 20, IncludeActive: true}); err != nil {
		builder.addPartial("timeline_index_queue", err)
	} else {
		timelineInput.Jobs.IndexQueue = envelope.Data
	}
	if envelope, err := client.ListIndexFailures(ctx, correlationID, search.StatusFilter{Limit: 20}); err != nil {
		builder.addPartial("timeline_index_failures", err)
	} else {
		timelineInput.Jobs.IndexFailures = envelope.Data
	}
	if envelope, err := client.ListScheduleFires(ctx, correlationID, automation.ScheduleFireFilter{Limit: 20}); err != nil {
		builder.addPartial("timeline_schedule_fires", err)
	} else {
		timelineInput.Automations.ScheduleFires = envelope.Data
	}
	if envelope, err := client.ListDirectEvents(ctx, correlationID, automation.DirectEventFilter{Limit: 20}); err != nil {
		builder.addPartial("timeline_direct_events", err)
	} else {
		timelineInput.Automations.DirectEvents = envelope.Data
	}
	if envelope, err := client.ListInvocations(ctx, correlationID, automation.InvocationFilter{Limit: 20}); err != nil {
		builder.addPartial("timeline_invocations", err)
	} else {
		timelineInput.Automations.Invocations = envelope.Data
	}
	if status, err := loadMainCloudStatus(ctx, client, correlationID); err != nil {
		builder.addPartial("timeline_cloud_status", err)
	} else {
		timelineInput.Storage.CloudStatus = status
		timelineInput.Storage.CloudStatusAvailable = true
	}

	builder.data.Timeline = BuildTimelineData(timelineInput)
	return builder.result()
}

func loadProjectsScreen(ctx context.Context, client Client, correlationID string, fallback Snapshot, options SnapshotOptions) ScreenLoadResult {
	return loadProjectsScreenWithSelection(ctx, client, correlationID, fallback, "", options)
}

func loadServicesScreen(ctx context.Context, client Client, correlationID string, fallback Snapshot) ScreenLoadResult {
	builder := newScreenLoadBuilder(ScreenServices, fallback)
	data := ServicesData{
		Inspections:      map[string]serviceregistry.ServiceInspection{},
		ArchivedScopeIDs: map[string]bool{},
		BoundedLogLines:  map[string][]string{},
	}
	envelope, err := client.ListServices(ctx, correlationID, serviceregistry.ServiceFilter{Limit: 100})
	if err != nil {
		state := NewScreenState(ScreenServices)
		state.Status = ScreenLoadFailed
		state.Error = err.Error()
		return ScreenLoadResult{Screen: ScreenServices, Snapshot: fallback, State: state, Err: err, LoadedAt: time.Now().UTC()}
	}
	data.Services = envelope.Data
	for _, service := range data.Services {
		ref := firstNonEmpty(service.ProviderAddress, service.ProviderID)
		inspection, inspectErr := client.GetService(ctx, correlationID, ref)
		if inspectErr != nil {
			builder.addPartial("service_detail", inspectErr)
			continue
		}
		data.Inspections[service.ProviderAddress] = inspection.Data
		data.Inspections[service.ProviderID] = inspection.Data
	}
	projectsEnvelope, projectsErr := client.ListProjects(ctx, correlationID, 100)
	if projectsErr != nil {
		builder.addPartial("service_projects", projectsErr)
	} else {
		for _, project := range projectsEnvelope.Data {
			if projectArchived(project) && project.ProjectScopeID != "" {
				data.ArchivedScopeIDs[project.ProjectScopeID] = true
			}
		}
	}
	builder.data.Services = data
	return builder.result()
}

func loadProjectsScreenWithSelection(ctx context.Context, client Client, correlationID string, fallback Snapshot, selectedProjectRef string, options ...SnapshotOptions) ScreenLoadResult {
	builder := newScreenLoadBuilder(ScreenProjects, fallback)
	data := ProjectsData{Explorer: defaultProjectExplorerState()}
	snapshotOptions := normalizeSnapshotOptions(options...)
	boxData, _ := collectBoxData(ctx, client, correlationID, snapshotOptions)
	data.Box = boxData

	projectsEnvelope, err := client.ListProjects(ctx, correlationID, 100)
	if err != nil {
		state := NewScreenState(ScreenProjects)
		state.Status = ScreenLoadFailed
		state.Error = err.Error()
		return ScreenLoadResult{
			Screen:   ScreenProjects,
			Snapshot: fallback,
			State:    state,
			Err:      err,
			LoadedAt: time.Now().UTC(),
		}
	}
	data.Projects = projectsEnvelope.Data
	if len(data.Projects) == 0 {
		builder.data.Projects = data
		return builder.result()
	}

	selected := data.Projects[0]
	selectedProjectRef = firstNonEmpty(selectedProjectRef, data.Explorer.SelectedProjectRef)
	if selectedProjectRef != "" {
		for _, project := range data.Projects {
			if projectRef(project) == selectedProjectRef || project.ProjectID == selectedProjectRef {
				selected = project
				break
			}
		}
	}
	ref := projectRef(selected)
	data.SelectedProjectRef = ref
	data.Explorer.SelectedProjectRef = ref
	if selectedProjectRef != "" {
		data.Explorer.Level = ProjectExplorerDetail
	}

	if envelope, err := client.GetProject(ctx, correlationID, ref); err != nil {
		builder.addPartial("project_detail", err)
	} else {
		data.SelectedProject = envelope.Data
	}
	if envelope, err := client.GetProjectRegistrationStatus(ctx, correlationID, ref); err != nil {
		builder.addPartial("project_registration", err)
	} else {
		data.RegistrationDetail = envelope.Data
		data.Workflows = projectWorkflowIntentsFromRegistration(envelope.Data)
	}
	if envelope, err := client.AnalyzeProjectContractBackend(ctx, correlationID, projectcontracts.BackendAnalysisInput{ProjectRef: ref}); err != nil {
		builder.addPartial("project_backend_analysis", err)
	} else {
		analysis := envelope.Data
		data.BackendAnalysis = &analysis
	}
	if envelope, err := client.ListProviders(ctx, correlationID, capabilities.ProviderFilter{ProjectRef: ref, Limit: 100}); err != nil {
		builder.addPartial("project_providers", err)
	} else {
		data.Providers = envelope.Data
	}
	if envelope, err := client.ListServices(ctx, correlationID, serviceregistry.ServiceFilter{ProjectRef: ref, Limit: 100}); err != nil {
		builder.addPartial("project_services", err)
	} else {
		data.Services = envelope.Data
	}
	if envelope, err := client.ListCapabilities(ctx, correlationID, capabilities.CapabilityFilter{ProjectRef: ref, Limit: 200}); err != nil {
		builder.addPartial("project_capabilities", err)
	} else {
		data.Capabilities = envelope.Data
	}
	data.RuntimeBindings = loadProjectRuntimeBindings(ctx, client, correlationID, data.Providers, &builder)
	if envelope, err := client.ListSchedules(ctx, correlationID, automation.ScheduleFilter{ProjectRef: ref, Limit: 50}); err != nil {
		builder.addPartial("project_schedules", err)
	} else {
		data.Schedules = envelope.Data
	}
	if envelope, err := client.ListDirectEventEndpoints(ctx, correlationID, automation.DirectEventEndpointFilter{ProjectRef: ref, Limit: 50}); err != nil {
		builder.addPartial("project_direct_event_endpoints", err)
	} else {
		data.DirectEventEndpoints = envelope.Data
	}
	if envelope, err := client.ListDirectEvents(ctx, correlationID, automation.DirectEventFilter{ProjectRef: ref, Limit: 20}); err != nil {
		builder.addPartial("project_direct_events", err)
	} else {
		data.DirectEvents = envelope.Data
	}
	if envelope, err := client.ListInvocations(ctx, correlationID, automation.InvocationFilter{ProjectRef: ref, Limit: 20}); err != nil {
		builder.addPartial("project_invocations", err)
	} else {
		data.Invocations = envelope.Data
	}
	if envelope, err := client.ListInvocationFailures(ctx, correlationID, automation.InvocationFilter{ProjectRef: ref, Limit: 20}); err != nil {
		builder.addPartial("project_invocation_failures", err)
	} else {
		data.InvocationFailures = envelope.Data
	}
	if envelope, err := client.ListJobs(ctx, correlationID, jobs.ListFilter{ProjectRef: ref, Limit: 20}); err != nil {
		builder.addPartial("project_jobs", err)
	} else {
		data.Jobs = envelope.Data
	}
	if envelope, err := client.ListFailedJobs(ctx, correlationID, jobs.ListFilter{ProjectRef: ref, Limit: 20}); err != nil {
		builder.addPartial("project_failed_jobs", err)
	} else {
		data.FailedJobs = envelope.Data
	}
	scopeRef := selected.ProjectScopeKey
	if scopeRef == "" && data.SelectedProject.Project.ProjectScopeKey != "" {
		scopeRef = data.SelectedProject.Project.ProjectScopeKey
	}
	if scopeRef != "" {
		if envelope, err := client.ListCapabilityCalls(ctx, correlationID, routing.CapabilityCallFilter{ScopeRef: scopeRef, Limit: 20}); err != nil {
			builder.addPartial("project_capability_calls", err)
		} else {
			data.CapabilityCalls = envelope.Data
		}
	}
	if envelope, err := client.ListWatchedRootStatus(ctx, correlationID, mainwatchedroots.StatusFilter{ProjectRef: ref, Limit: 50}); err != nil {
		builder.addPartial("project_watched_roots", err)
	} else {
		data.WatchedRoots = envelope.Data
	}
	if envelope, err := client.ListWatchedRootFindings(ctx, correlationID, mainwatchedroots.FindingFilter{ProjectRef: ref, Status: "open", Limit: 20}); err != nil {
		builder.addPartial("project_watched_root_findings", err)
	} else {
		data.WatchedRootFindings = envelope.Data
	}
	if envelope, err := client.BuildProjectWatchPlan(ctx, correlationID, ref, projectwatch.BuildPlanInput{UseRegisteredSnapshot: true}); err != nil {
		builder.addPartial("project_watch_plan", err)
	} else {
		data.WatchPlan = envelope.Data
	}
	if envelope, err := client.GetProjectSyncStatus(ctx, correlationID, ref); err != nil {
		builder.addPartial("project_sync_status", err)
	} else {
		data.SyncStatus = envelope.Data
	}
	if envelope, err := client.GetProjectBackupStatus(ctx, correlationID, ref); err != nil {
		builder.addPartial("project_backup_status", err)
	} else {
		data.BackupStatus = envelope.Data
	}
	if envelope, err := client.GetProjectRepositoryStatus(ctx, correlationID, ref, localclient.ProjectRepositoryPageOptions{Limit: 100}); err != nil {
		builder.addPartial("project_repository_status", err)
	} else {
		data.RepositoryStatus = envelope.Data
		data.RepositoryStateReady = true
	}

	builder.data.Projects = data
	return builder.result()
}

func loadProjectRuntimeBindings(ctx context.Context, client Client, correlationID string, providers []capabilities.ProviderListItem, builder *screenLoadBuilder) []capabilities.RuntimeBindingInspection {
	bindings := []capabilities.RuntimeBindingInspection{}
	seen := map[string]bool{}
	for _, provider := range providers {
		ref := provider.Provider.CompactAddress
		if ref == "" {
			ref = provider.Provider.ProviderID
		}
		if ref == "" {
			continue
		}
		envelope, err := client.ListRuntimeBindings(ctx, correlationID, capabilities.RuntimeBindingFilter{ProviderRef: ref, Limit: 50})
		if err != nil {
			builder.addPartial("project_runtime_bindings", err)
			continue
		}
		for _, binding := range envelope.Data {
			id := binding.Binding.RuntimeBindingID
			if id == "" {
				id = binding.Endpoint.CompactAddress + ":" + binding.Binding.RuntimeKind
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			bindings = append(bindings, binding)
		}
	}
	return bindings
}

func loadBackgroundScreen(ctx context.Context, client Client, correlationID string, fallback Snapshot, options SnapshotOptions) ScreenLoadResult {
	builder := newScreenLoadBuilder(ScreenBackground, fallback)
	data := BackgroundData{WorkerRunsByWorker: map[string][]workers.WorkerRun{}}

	if envelope, err := client.ListWorkers(ctx, correlationID, workers.WorkerFilter{Limit: 100}); err != nil {
		builder.addPartial("workers", err)
	} else {
		data.Workers = envelope.Data
	}
	data.ArchivedProjects = loadArchivedProjectsForActionQuarantine(ctx, client, correlationID, &builder)
	for idx, worker := range data.Workers {
		if idx >= 5 {
			break
		}
		if worker.WorkerKey == "" {
			continue
		}
		envelope, err := client.ListWorkerRuns(ctx, correlationID, worker.WorkerKey, workers.RunFilter{Limit: 3})
		if err != nil {
			builder.addPartial("worker_runs", err)
			continue
		}
		data.WorkerRunsByWorker[worker.WorkerKey] = envelope.Data
	}
	if envelope, err := client.MaintenanceStatus(ctx, correlationID); err != nil {
		builder.addPartial("maintenance", err)
	} else {
		data.Maintenance = envelope.Data
	}
	if envelope, err := client.ListMaintenanceFindings(ctx, correlationID, maintenance.FindingFilter{Status: "open", Limit: 20}); err != nil {
		builder.addPartial("maintenance_findings", err)
	} else {
		data.MaintenanceFindings = envelope.Data
	}
	if envelope, err := client.MaintenanceDBStatus(ctx, correlationID); err != nil {
		builder.addPartial("maintenance_db", err)
	} else {
		data.DBStatus = envelope.Data
	}
	if envelope, err := client.MaintenanceBackupStatus(ctx, correlationID); err != nil {
		builder.addPartial("maintenance_backup_status", err)
	} else {
		data.BackupStatus = envelope.Data
	}
	if envelope, err := client.ListMaintenanceBackups(ctx, correlationID, maintenance.OperationFilter{Limit: 20}); err != nil {
		builder.addPartial("maintenance_backup_list", err)
	} else {
		data.BackupOperations = envelope.Data
	}
	if status, err := loadMainCloudStatus(ctx, client, correlationID); err != nil {
		builder.addPartial("cloud_status", err)
	} else {
		data.CloudStatus = status
		data.CloudStatusAvailable = true
	}
	if envelope, err := client.MaintenanceObjectStoreStatus(ctx, correlationID); err != nil {
		builder.addPartial("maintenance_object_store", err)
	} else {
		data.ObjectStoreStatus = envelope.Data
	}
	data.UpdateStatus = update.Status(update.StatusInput{StateDir: firstNonEmpty(options.UpdateStateDir, fallback.UpdateStatus.StateDir), Limit: 20})

	builder.data.Background = data
	return builder.result()
}

func loadMainCloudStatus(ctx context.Context, client Client, correlationID string) (cloudstorage.StatusReport, error) {
	cloudCtx, cloudCancel := context.WithTimeout(ctx, 3*time.Second)
	defer cloudCancel()
	envelope, err := client.CloudStatusLive(cloudCtx, correlationID, cloudstorage.CloudStatusLiveInput{
		ConfigPath: cloudstorage.DefaultConfigPath,
	})
	if err != nil {
		return cloudstorage.StatusReport{}, err
	}
	return envelope.Data, nil
}

func loadAutomationsScreen(ctx context.Context, client Client, correlationID string, fallback Snapshot) ScreenLoadResult {
	builder := newScreenLoadBuilder(ScreenAutomations, fallback)
	data := AutomationsData{}
	data.Projects, data.ArchivedProjects = loadProjectInventoryForActionQuarantine(ctx, client, correlationID, &builder)

	if envelope, err := client.ListAutomations(ctx, correlationID, automation.AutomationFilter{Limit: 20}); err != nil {
		builder.addPartial("automations", err)
	} else {
		data.Automations = envelope.Data
	}
	if envelope, err := client.ListSchedules(ctx, correlationID, automation.ScheduleFilter{Limit: 20}); err != nil {
		builder.addPartial("schedule_list", err)
	} else {
		data.Schedules = envelope.Data
	}
	if envelope, err := client.ScheduleStatus(ctx, correlationID); err != nil {
		builder.addPartial("schedules", err)
	} else {
		data.ScheduleStatus = envelope.Data
	}
	if envelope, err := client.ListScheduleFires(ctx, correlationID, automation.ScheduleFireFilter{Limit: 20}); err != nil {
		builder.addPartial("schedule_fires", err)
	} else {
		data.ScheduleFires = envelope.Data
	}
	if envelope, err := client.ListIntegrations(ctx, correlationID, automation.IntegrationFilter{Limit: 20}); err != nil {
		builder.addPartial("integrations", err)
	} else {
		data.Integrations = envelope.Data
	}
	if envelope, err := client.ListDirectEventEndpoints(ctx, correlationID, automation.DirectEventEndpointFilter{Limit: 20}); err != nil {
		builder.addPartial("direct_event_endpoints", err)
	} else {
		data.DirectEventEndpoints = envelope.Data
	}
	if envelope, err := client.ListDirectEvents(ctx, correlationID, automation.DirectEventFilter{Limit: 20}); err != nil {
		builder.addPartial("direct_event_list", err)
	} else {
		data.DirectEvents = envelope.Data
	}
	if envelope, err := client.DirectEventStatus(ctx, correlationID); err != nil {
		builder.addPartial("direct_events", err)
	} else {
		data.DirectEventStatus = envelope.Data
	}
	if envelope, err := client.ListInvocations(ctx, correlationID, automation.InvocationFilter{Limit: 20}); err != nil {
		builder.addPartial("invocations", err)
	} else {
		data.Invocations = envelope.Data
	}
	if envelope, err := client.ListInvocationFailures(ctx, correlationID, automation.InvocationFilter{Limit: 20}); err != nil {
		builder.addPartial("invocation_failures", err)
	} else {
		data.InvocationFailures = envelope.Data
	}
	if envelope, err := client.ListDirectEventFailures(ctx, correlationID, automation.DirectEventFilter{Limit: 20}); err != nil {
		builder.addPartial("direct_event_failures", err)
	} else {
		data.DirectEventFailures = envelope.Data
	}
	data.StaleProjectRefs = staleAutomationProjectRefs(data)
	if len(data.StaleProjectRefs) > 0 {
		builder.addPartial("stale_automation_project_refs", fmt.Errorf("suppressed automation actions for missing project refs: %s", strings.Join(data.StaleProjectRefs, ", ")))
	}

	builder.data.Automations = data
	return builder.result()
}

func loadJobsScreen(ctx context.Context, client Client, correlationID string, fallback Snapshot) ScreenLoadResult {
	builder := newScreenLoadBuilder(ScreenJobs, fallback)
	data := JobsData{}
	data.ArchivedProjects = loadArchivedProjectsForActionQuarantine(ctx, client, correlationID, &builder)

	if envelope, err := client.JobStatus(ctx, correlationID); err != nil {
		builder.addPartial("jobs", err)
	} else {
		data.JobStatus = envelope.Data
	}
	if envelope, err := client.RunnerStatus(ctx, correlationID); err != nil {
		builder.addPartial("runner_status", err)
	} else {
		data.RunnerStatus = envelope.Data
	}
	if envelope, err := client.ListJobs(ctx, correlationID, jobs.ListFilter{Limit: 20}); err != nil {
		builder.addPartial("job_list", err)
	} else {
		data.Jobs = envelope.Data
	}
	if envelope, err := client.ListQueuedJobs(ctx, correlationID, jobs.ListFilter{Limit: 20}); err != nil {
		builder.addPartial("queued_jobs", err)
	} else {
		data.QueuedJobs = envelope.Data
	}
	if envelope, err := client.ListFailedJobs(ctx, correlationID, jobs.ListFilter{Limit: 20}); err != nil {
		builder.addPartial("failed_jobs", err)
	} else {
		data.FailedJobs = envelope.Data
	}
	if envelope, err := client.ListRunners(ctx, correlationID, jobs.RunnerFilter{Limit: 20}); err != nil {
		builder.addPartial("runners", err)
	} else {
		data.Runners = envelope.Data
	}
	if envelope, err := client.ListWorkers(ctx, correlationID, workers.WorkerFilter{Limit: 100}); err != nil {
		builder.addPartial("job_workers", err)
	} else {
		data.Workers = envelope.Data
	}
	if envelope, err := client.ListIndexStatus(ctx, correlationID, search.StatusFilter{Limit: 20}); err != nil {
		builder.addPartial("index_status", err)
	} else {
		data.IndexStatuses = envelope.Data
	}
	if envelope, err := client.ListIndexQueue(ctx, correlationID, search.IndexQueueFilter{Limit: 20, IncludeActive: true}); err != nil {
		builder.addPartial("index_queue", err)
	} else {
		data.IndexQueue = envelope.Data
	}
	if envelope, err := client.ListIndexFailures(ctx, correlationID, search.StatusFilter{Limit: 20}); err != nil {
		builder.addPartial("index_failures", err)
	} else {
		data.IndexFailures = envelope.Data
	}

	builder.data.Jobs = data
	return builder.result()
}

func loadNodesScreen(ctx context.Context, client Client, correlationID string, fallback Snapshot) ScreenLoadResult {
	builder := newScreenLoadBuilder(ScreenNodes, fallback)
	data := NodesData{}

	if envelope, err := client.ListNodes(ctx, correlationID, 50); err != nil {
		builder.addPartial("nodes", err)
	} else {
		data.Nodes = envelope.Data
	}
	if envelope, err := client.ListProtectedFolders(ctx, correlationID, backupcontracts.ProtectedFolderFilter{Limit: 100}); err != nil {
		builder.addPartial("protected_folders", err)
	} else {
		data.ProtectedFolders = envelope.Data.Folders
		data.ProtectedFolderCounts = envelope.Data.Counts
	}
	if envelope, err := client.ListWatchedRootStatus(ctx, correlationID, mainwatchedroots.StatusFilter{Limit: 50}); err != nil {
		builder.addPartial("watched_roots", err)
	} else {
		data.WatchedRoots = envelope.Data
	}
	if envelope, err := client.ListWatchedRootFindings(ctx, correlationID, mainwatchedroots.FindingFilter{Status: "open", Limit: 20}); err != nil {
		builder.addPartial("watched_root_findings", err)
	} else {
		data.WatchedRootFindings = envelope.Data
	}
	if len(data.WatchedRoots) > 0 {
		root := data.WatchedRoots[0].Root
		backupFilter := mainwatchedroots.BackupFilter{NodeRef: root.NodeID, RootKey: root.RootKey, Limit: 20}
		if envelope, err := client.GetWatchedRootBackupStatus(ctx, correlationID, backupFilter); err != nil {
			builder.addPartial("watched_root_backup_status", err)
		} else {
			data.BackupStatus = envelope.Data
		}
	}
	if envelope, err := client.ListWatchedRootBackupBatches(ctx, correlationID, mainwatchedroots.BackupFilter{Limit: 20}); err != nil {
		builder.addPartial("watched_root_backup_batches", err)
	} else {
		data.BackupBatches = envelope.Data
	}
	if envelope, err := client.ListWatchedRootBackupItems(ctx, correlationID, mainwatchedroots.BackupItemFilter{Limit: 20}); err != nil {
		builder.addPartial("watched_root_backup_items", err)
	} else {
		data.BackupItems = envelope.Data
	}
	if envelope, err := client.GetSyncStatus(ctx, correlationID, portalMainSyncStatusFilter()); err != nil {
		builder.addPartial("sync_status", err)
	} else {
		data.SyncStatus = envelope.Data
	}
	if envelope, err := client.ListSyncBatches(ctx, correlationID, loomsync.ListFilter{Limit: 20}); err != nil {
		builder.addPartial("sync_batches", err)
	} else {
		data.SyncBatches = envelope.Data
	}
	if envelope, err := client.ListSyncConflicts(ctx, correlationID, loomsync.ListFilter{Status: "open", Limit: 20}); err != nil {
		builder.addPartial("sync_conflicts", err)
	} else {
		data.SyncConflicts = envelope.Data
	}
	if envelope, err := client.ListSyncReplicas(ctx, correlationID, loomsync.ListFilter{Limit: 20}); err != nil {
		builder.addPartial("sync_replicas", err)
	} else {
		data.SyncReplicas = envelope.Data
	}
	if envelope, err := client.ListPrivateBackups(ctx, correlationID, loomsync.ListFilter{Limit: 20}); err != nil {
		builder.addPartial("private_backups", err)
	} else {
		data.PrivateBackups = envelope.Data
	}
	if envelope, err := client.ListDeletionRequests(ctx, correlationID, loomsync.ListFilter{ActiveOnly: true, Limit: 20}); err != nil {
		builder.addPartial("deletion_requests", err)
	} else {
		data.DeletionRequests = envelope.Data
	}

	builder.data.Nodes = data
	return builder.result()
}

func loadCapabilitiesScreen(ctx context.Context, client Client, correlationID string, fallback Snapshot) ScreenLoadResult {
	builder := newScreenLoadBuilder(ScreenCapabilities, fallback)
	data := CapabilitiesData{Explorer: defaultCapabilityExplorerState()}
	data.ArchivedProjects = loadArchivedProjectsForActionQuarantine(ctx, client, correlationID, &builder)

	if envelope, err := client.ListProviders(ctx, correlationID, capabilities.ProviderFilter{Limit: 50, Status: capabilities.ProviderStatusActive, RequireActiveEndpoint: true}); err != nil {
		builder.addPartial("providers", err)
	} else {
		data.Providers = envelope.Data
	}
	if envelope, err := client.ListCapabilities(ctx, correlationID, capabilities.CapabilityFilter{Limit: 100, Status: capabilities.EndpointStatusActive}); err != nil {
		builder.addPartial("capabilities", err)
	} else {
		data.Capabilities = envelope.Data
	}
	if envelope, err := client.ListProviderAdvertisements(ctx, correlationID, capabilities.ProviderAdvertisementFilter{Limit: 20}); err != nil {
		builder.addPartial("provider_advertisements", err)
	} else {
		data.Advertisements = envelope.Data
	}

	builder.data.Capabilities = data
	return builder.result()
}

func loadArchivedProjectsForActionQuarantine(ctx context.Context, client Client, correlationID string, builder *screenLoadBuilder) []projects.Project {
	_, archived := loadProjectInventoryForActionQuarantine(ctx, client, correlationID, builder)
	return archived
}

func loadProjectInventoryForActionQuarantine(ctx context.Context, client Client, correlationID string, builder *screenLoadBuilder) ([]projects.Project, []projects.Project) {
	envelope, err := client.ListProjects(ctx, correlationID, 100)
	if err != nil {
		builder.addPartial("archived_project_quarantine", err)
		return nil, nil
	}
	projectsList := envelope.Data
	archived := archivedProjectsFromProjects(projectsList)
	return projectsList, archived
}

func archivedProjectsFromProjects(projectsList []projects.Project) []projects.Project {
	archived := []projects.Project{}
	for _, project := range projectsList {
		if projectArchived(project) {
			archived = append(archived, project)
		}
	}
	return archived
}
