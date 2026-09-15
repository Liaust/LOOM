package portal

import (
	"time"

	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/box"
	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/cloudstorage"
	"loom.local/loom/internal/filetransfer"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/lane"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/mainstorage"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/objects"
	"loom.local/loom/internal/projectdoctor"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
	"loom.local/loom/internal/routing"
	"loom.local/loom/internal/search"
	"loom.local/loom/internal/serviceregistry"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/storagedoctor"
	"loom.local/loom/internal/storageexport"
	"loom.local/loom/internal/storageview"
	loomsync "loom.local/loom/internal/sync"
	"loom.local/loom/internal/update"
	mainwatchedroots "loom.local/loom/internal/watchedroots"
	"loom.local/loom/internal/workers"
)

type ScreenLoadStatus string

const (
	ScreenLoadIdle        ScreenLoadStatus = "idle"
	ScreenLoadLoading     ScreenLoadStatus = "loading"
	ScreenLoadLoaded      ScreenLoadStatus = "loaded"
	ScreenLoadPartial     ScreenLoadStatus = "partial"
	ScreenLoadUnavailable ScreenLoadStatus = "unavailable"
	ScreenLoadFailed      ScreenLoadStatus = "failed"
)

type ScreenState struct {
	Screen              string
	Status              ScreenLoadStatus
	LoadedAt            time.Time
	SelectedIndex       int
	RawDetails          bool
	ExpandedActionGroup string
	PartialErrors       []SnapshotError
	Error               string
	MainAvailability    MainAvailability
	Data                ScreenData
}

type ScreenData struct {
	Home         HomeData
	Doctor       DoctorData
	Box          BoxData
	Projects     ProjectsData
	Services     ServicesData
	Background   BackgroundData
	Automations  AutomationsData
	Jobs         JobsData
	Notes        NotesData
	Nodes        NodesData
	Capabilities CapabilitiesData
	Database     DatabaseData
	Storage      StorageData
	Timeline     TimelineData
}

type HomeData struct {
	Snapshot   Snapshot
	Connection []HomeSummaryItem
	System     []HomeSummaryItem
	Network    []HomeSummaryItem
	Storage    []HomeSummaryItem
	Runtime    []HomeSummaryItem
	Attention  []AttentionItem
	Running    []HomeActivityItem
	Recent     []HomeActivityItem
}

type HomeSummaryItem struct {
	Label  string
	Status string
	Detail string
}

type AttentionItem struct {
	ID              string
	Domain          string
	Severity        string
	Title           string
	Reason          string
	Suggestion      string
	FailureClass    string
	TargetKind      string
	TargetRef       string
	RelatedActionID string
	InspectActionID string
	RepairActionID  string
	CreatedAt       time.Time
}

type HomeActivityItem struct {
	ID          string
	Domain      string
	Title       string
	Status      string
	Detail      string
	TargetKind  string
	TargetRef   string
	Progress    *float64
	StartedAt   *time.Time
	CompletedAt *time.Time
}

type OperationProgress struct {
	Current   int64
	Total     int64
	Unit      string
	Label     string
	UpdatedAt *time.Time
}

type TimelineEvent struct {
	ID            string
	Domain        string
	Kind          string
	Title         string
	Status        string
	StartedAt     time.Time
	FinishedAt    *time.Time
	Progress      *OperationProgress
	TargetKind    string
	TargetRef     string
	Summary       string
	ErrorCode     string
	ErrorMessage  string
	RelatedAction string
	CorrelationID string
	Payload       map[string]string
}

type TimelineData struct {
	Snapshot Snapshot
	Events   []TimelineEvent
	Running  []TimelineEvent
	Recent   []TimelineEvent
	Failed   []TimelineEvent
}

func (d TimelineData) DisplayEvents() []TimelineEvent {
	events := make([]TimelineEvent, 0, len(d.Running)+len(d.Recent))
	events = append(events, d.Running...)
	events = append(events, d.Recent...)
	return events
}

type BoxData struct {
	Status               box.Status
	WatchPlan            box.WatchPlan
	WatchStatus          box.WatchStatusResult
	WatchStatusAvailable bool
}

type ProjectsData struct {
	Box                  BoxData
	Projects             []projects.Project
	SelectedProjectRef   string
	SelectedProject      projects.ProjectDetail
	RegistrationDetail   projects.ProjectRegistrationDetail
	BackendAnalysis      *projectdoctor.BackendAnalysisResult
	Providers            []capabilities.ProviderListItem
	Services             []serviceregistry.ServiceListItem
	Capabilities         []capabilities.CapabilityListItem
	RuntimeBindings      []capabilities.RuntimeBindingInspection
	Schedules            []automation.Schedule
	DirectEventEndpoints []automation.DirectEventEndpoint
	DirectEvents         []automation.DirectEvent
	Invocations          []automation.Invocation
	InvocationFailures   []automation.Invocation
	Jobs                 []jobs.Job
	FailedJobs           []jobs.Job
	CapabilityCalls      []routing.CapabilityCall
	WatchedRoots         []mainwatchedroots.RootStatus
	WatchedRootFindings  []mainwatchedroots.Finding
	WatchPlan            projectwatch.ProjectWatchPlan
	SyncStatus           projectwatch.ProjectSyncStatus
	BackupStatus         projectwatch.ProjectBackupStatus
	RepositoryStatus     localclient.ProjectRepositoryStatusResult
	RepositoryStateReady bool
	Workflows            []ProjectWorkflowIntent
	Explorer             ProjectExplorerState
}

type ServicesData struct {
	Services         []serviceregistry.ServiceListItem
	Inspections      map[string]serviceregistry.ServiceInspection
	ArchivedScopeIDs map[string]bool
	BoundedLogLines  map[string][]string
}

type ProjectSurfaceStatus struct {
	Lifecycle      string
	Registration   string
	Activation     string
	BackendOwner   string
	Root           string
	Revision       string
	Facets         []string
	ContractHealth string
	ContractDrift  string
	RuntimeHealth  string
	ArchiveState   string
	NextAction     string
}

type ProjectExplorerState struct {
	Level              ProjectExplorerLevel
	SelectedProjectRef string
	ExpandedRef        string
	ShowArchived       bool
}

type ProjectExplorerLevel string

const (
	ProjectExplorerHome      ProjectExplorerLevel = "home"
	ProjectExplorerDetail    ProjectExplorerLevel = "detail"
	ProjectExplorerStructure ProjectExplorerLevel = "structure"
	ProjectExplorerRuntime   ProjectExplorerLevel = "runtime"
	ProjectExplorerStorage   ProjectExplorerLevel = "storage"
	ProjectExplorerTimeline  ProjectExplorerLevel = "timeline"
	ProjectExplorerArchive   ProjectExplorerLevel = "archive"
)

func defaultProjectExplorerState() ProjectExplorerState {
	return ProjectExplorerState{Level: ProjectExplorerHome}
}

func normalizeProjectExplorerState(explorer ProjectExplorerState) ProjectExplorerState {
	if explorer.Level == "" {
		explorer.Level = ProjectExplorerHome
	}
	return explorer
}

type ProjectWorkflowIntent struct {
	WorkflowID             string
	Name                   string
	Status                 string
	ImplementationKind     string
	ImplementationRequired bool
	Placeholder            bool
	CapabilityAddress      string
	RuntimeKind            string
	RuntimeBindingID       string
	WorkflowPackageID      string
	WorkflowVersionID      string
	ActivationStatus       string
	Executable             bool
	Source                 string
}

type BackgroundData struct {
	Workers                 []workers.WorkerListItem
	WorkerRunsByWorker      map[string][]workers.WorkerRun
	ArchivedProjects        []projects.Project
	Maintenance             maintenance.Status
	MaintenanceFindings     []maintenance.Finding
	DBStatus                maintenance.DBStatus
	BackupStatus            maintenance.BackupStatus
	BackupOperations        []maintenance.BackupOperation
	CloudStatus             cloudstorage.StatusReport
	CloudStatusAvailable    bool
	CloudSnapshots          cloudstorage.SnapshotListResult
	CloudSnapshotsAvailable bool
	UpdateStatus            update.UpdateStatus
	ObjectStoreStatus       maintenance.ObjectStoreStatus
}

type AutomationsData struct {
	Projects             []projects.Project
	Automations          []automation.Automation
	Schedules            []automation.Schedule
	ArchivedProjects     []projects.Project
	StaleProjectRefs     []string
	ScheduleStatus       automation.ScheduleStatus
	ScheduleFires        []automation.ScheduleFire
	Integrations         []automation.Integration
	DirectEventEndpoints []automation.DirectEventEndpoint
	DirectEvents         []automation.DirectEvent
	DirectEventStatus    automation.DirectEventStatus
	Invocations          []automation.Invocation
	InvocationFailures   []automation.Invocation
	DirectEventFailures  []automation.DirectEvent
}

type JobsData struct {
	JobStatus        jobs.QueueSummary
	RunnerStatus     jobs.QueueSummary
	ArchivedProjects []projects.Project
	Jobs             []jobs.Job
	QueuedJobs       []jobs.Job
	FailedJobs       []jobs.Job
	Runners          []jobs.Runner
	Workers          []workers.WorkerListItem
	IndexStatuses    []search.IndexStatus
	IndexQueue       []search.IndexStatus
	IndexFailures    []search.IndexStatus
}

type NotesData struct {
	Overview            knowledge.NotesOverview
	Pipelines           knowledge.PipelineOverallStatus
	PipelinesAvailable  bool
	Embeddings          knowledge.EmbeddingStatus
	EmbeddingsAvailable bool
	Search              NotesSearchData
}

type NotesSearchData struct {
	Query     string
	Input     knowledge.NotesSearchInput
	ResultSet knowledge.NotesSearchResultSet
	Status    ScreenLoadStatus
	Error     string
	LoadedAt  time.Time
}

type NodesData struct {
	Nodes                 []nodes.Node
	ProtectedFolders      []backupcontracts.ProtectedFolderRecord
	ProtectedFolderCounts map[string]int
	WatchedRoots          []mainwatchedroots.RootStatus
	WatchedRootFindings   []mainwatchedroots.Finding
	BackupStatus          mainwatchedroots.BackupStatus
	BackupBatches         []mainwatchedroots.BackupBatch
	BackupItems           []mainwatchedroots.BackupItem
	SyncStatus            loomsync.SyncStatus
	SyncBatches           []loomsync.SyncBatch
	SyncConflicts         []loomsync.SyncConflict
	SyncReplicas          []loomsync.SyncReplica
	PrivateBackups        []loomsync.PrivateBackupOperation
	DeletionRequests      []loomsync.DeletionRequest
}

func (d NodesData) BackupStatusSelectableCount() int {
	if d.BackupStatus.Root.RootKey == "" && d.BackupStatus.Status == "" {
		return 0
	}
	return 1
}

type CapabilitiesData struct {
	Providers        []capabilities.ProviderListItem
	Capabilities     []capabilities.CapabilityListItem
	Advertisements   []capabilities.ProviderAdvertisement
	ArchivedProjects []projects.Project
	Explorer         CapabilityExplorerState
}

type CapabilityExplorerLevel string

const (
	CapabilityExplorerNodes        CapabilityExplorerLevel = "nodes"
	CapabilityExplorerProviders    CapabilityExplorerLevel = "providers"
	CapabilityExplorerCapabilities CapabilityExplorerLevel = "capabilities"
)

type CapabilityExplorerState struct {
	Level            CapabilityExplorerLevel
	SelectedScope    string
	SelectedProvider string
	ExpandedRef      string
	Query            string
}

type DatabaseData struct {
	Objects          []objects.Object
	IndexStatuses    []search.IndexStatus
	IndexQueue       []search.IndexStatus
	IndexFailures    []search.IndexStatus
	SyncStatus       loomsync.SyncStatus
	SyncBatches      []loomsync.SyncBatch
	SyncConflicts    []loomsync.SyncConflict
	SyncReplicas     []loomsync.SyncReplica
	PrivateBackups   []loomsync.PrivateBackupOperation
	DeletionRequests []loomsync.DeletionRequest
	Workers          []workers.WorkerListItem
	Search           DatabaseSearchData
}

type DatabaseSearchData struct {
	Query     string
	ResultSet search.SearchResultSet
	Status    ScreenLoadStatus
	Error     string
	LoadedAt  time.Time
}

type StorageData struct {
	Tree                      storageview.Tree
	Entries                   []storagecatalog.Entry
	FilesystemStatus          storagedoctor.FilesystemStatus
	FilesystemStatusAvailable bool
	// ExportStatus is retained only for decoding one-release timeline snapshots;
	// active loaders and actions no longer populate or consume it.
	ExportStatus                    storageexport.Status
	ExportStatusAvailable           bool
	RetentionStatus                 storagecatalog.RetentionStatus
	RetentionStatusAvailable        bool
	MainDocumentsStatus             mainstorage.Status
	MainDocumentsStatusAvailable    bool
	TransferStatuses                []filetransfer.Status
	TransferStatusesAvailable       bool
	WatchedRootBackupItems          []mainwatchedroots.BackupItem
	WatchedRootBackupItemsAvailable bool
	LaneStatus                      *lane.Status
	CloudStatus                     cloudstorage.StatusReport
	CloudStatusAvailable            bool
	CloudSnapshots                  cloudstorage.SnapshotListResult
	CloudSnapshotsAvailable         bool
	DBStatus                        maintenance.DBStatus
	DBStatusAvailable               bool
	BackupStatus                    maintenance.BackupStatus
	BackupStatusAvailable           bool
}

func NewScreenState(screen string) ScreenState {
	return ScreenState{
		Screen:        NormalizeScreen(screen),
		Status:        ScreenLoadIdle,
		SelectedIndex: 0,
	}
}

func ScreenStateFromSnapshot(screen string, snapshot Snapshot) ScreenState {
	screen = NormalizeScreen(screen)
	if snapshot.MainAvailability.State == MainAvailabilityOffline && mainOwnedScreen(screen) {
		return unavailableScreenState(screen, snapshot)
	}
	partials := partialsForScreen(screen, snapshot.PartialErrors)
	if snapshot.MainAvailability.State == MainAvailabilityOffline {
		partials = withoutPartialSource(partials, "main_availability")
		if screen == ScreenBox {
			partials = append(partials, SnapshotError{
				Source:  "box_watch_status",
				Message: "Main is offline. Backend watch and registration status is unavailable.",
			})
		}
	}
	status := ScreenLoadLoaded
	if len(partials) > 0 {
		status = ScreenLoadPartial
	}
	return ScreenState{
		Screen:           screen,
		Status:           status,
		LoadedAt:         snapshot.CapturedAt,
		SelectedIndex:    0,
		PartialErrors:    partials,
		MainAvailability: snapshot.MainAvailability,
		Data:             ScreenDataFromSnapshot(screen, snapshot),
	}
}

func unavailableScreenState(screen string, snapshot Snapshot) ScreenState {
	loadedAt := snapshot.MainAvailability.CheckedAt
	if loadedAt.IsZero() {
		loadedAt = snapshot.CapturedAt
	}
	return ScreenState{
		Screen:           NormalizeScreen(screen),
		Status:           ScreenLoadUnavailable,
		LoadedAt:         loadedAt,
		SelectedIndex:    0,
		MainAvailability: snapshot.MainAvailability,
	}
}

func mainOwnedScreen(screen string) bool {
	switch NormalizeScreen(screen) {
	case ScreenHome, ScreenDoctor, ScreenBox:
		return false
	default:
		return true
	}
}

func withoutPartialSource(partials []SnapshotError, source string) []SnapshotError {
	filtered := make([]SnapshotError, 0, len(partials))
	for _, partial := range partials {
		if partial.Source != source {
			filtered = append(filtered, partial)
		}
	}
	return filtered
}

func ScreenDataFromSnapshot(screen string, snapshot Snapshot) ScreenData {
	screen = NormalizeScreen(screen)
	data := ScreenData{}
	switch screen {
	case ScreenDoctor:
		data.Doctor = BuildDoctorData(snapshot)
	case ScreenBox:
		data.Box = BoxData{
			Status:               snapshot.BoxStatus,
			WatchPlan:            snapshot.BoxWatchPlan,
			WatchStatus:          snapshot.BoxWatchStatus,
			WatchStatusAvailable: snapshot.BoxWatchStatusAvailable,
		}
	case ScreenProjects:
		data.Projects = ProjectsData{Explorer: defaultProjectExplorerState()}
	case ScreenServices:
		data.Services = ServicesData{
			Inspections:      map[string]serviceregistry.ServiceInspection{},
			ArchivedScopeIDs: map[string]bool{},
			BoundedLogLines:  map[string][]string{},
		}
	case ScreenBackground:
		data.Background = BackgroundData{
			Workers:             snapshot.Workers,
			WorkerRunsByWorker:  map[string][]workers.WorkerRun{},
			Maintenance:         snapshot.Maintenance,
			MaintenanceFindings: snapshot.MaintenanceFindings,
			UpdateStatus:        snapshot.UpdateStatus,
		}
	case ScreenAutomations:
		data.Automations = AutomationsData{
			Schedules:            snapshot.Schedules,
			ScheduleStatus:       snapshot.ScheduleStatus,
			DirectEventEndpoints: snapshot.DirectEventEndpoints,
			DirectEventStatus:    snapshot.DirectEventStatus,
			InvocationFailures:   snapshot.InvocationFailures,
			DirectEventFailures:  snapshot.DirectEventFailures,
		}
	case ScreenJobs:
		data.Jobs = JobsData{
			JobStatus:     snapshot.JobStatus,
			IndexFailures: snapshot.IndexFailures,
		}
	case ScreenNotes:
		data.Notes = NotesData{}
	case ScreenNodes:
		data.Nodes = NodesData{
			Nodes:               snapshot.Nodes,
			WatchedRoots:        snapshot.WatchedRoots,
			WatchedRootFindings: snapshot.WatchedRootFindings,
		}
	case ScreenCapabilities:
		data.Capabilities = CapabilitiesData{Explorer: defaultCapabilityExplorerState()}
	case ScreenDatabase:
		data.Database = DatabaseData{}
	case ScreenStorage:
		data.Storage = StorageData{}
	case ScreenTimeline:
		data.Timeline = BuildTimelineData(TimelineBuildInput{Snapshot: snapshot})
	default:
		data.Home = BuildHomeData(snapshot)
	}
	return data
}

func partialsForScreen(screen string, partials []SnapshotError) []SnapshotError {
	if len(partials) == 0 {
		return nil
	}
	screen = NormalizeScreen(screen)
	filtered := []SnapshotError{}
	for _, partial := range partials {
		if partialAppliesToScreen(screen, partial.Source) {
			filtered = append(filtered, partial)
		}
	}
	return filtered
}

func partialAppliesToScreen(screen, source string) bool {
	switch screen {
	case ScreenDoctor:
		return true
	case ScreenHome:
		return true
	case ScreenProjects:
		switch source {
		case "projects", "project_detail", "project_registration", "project_providers", "project_capabilities", "project_runtime_bindings", "project_schedules", "project_direct_event_endpoints", "project_direct_events", "project_invocations", "project_invocation_failures", "project_jobs", "project_failed_jobs", "project_capability_calls", "project_watched_roots", "project_watched_root_findings", "project_watch_plan", "project_sync_status", "project_backup_status":
			return true
		}
	case ScreenServices:
		return source == "services" || source == "service_detail" || source == "service_projects"
	case ScreenBox:
		switch source {
		case "box", "box_watch_plan", "box_watch_status":
			return true
		}
	case ScreenBackground:
		switch source {
		case "workers", "worker_runs", "maintenance", "maintenance_findings", "maintenance_db", "maintenance_backup_status", "maintenance_backup_list", "maintenance_object_store":
			return true
		}
	case ScreenAutomations:
		switch source {
		case "automations", "integrations", "schedule_list", "schedules", "schedule_fires", "direct_event_endpoints", "direct_event_list", "direct_events", "invocations", "invocation_failures", "direct_event_failures":
			return true
		}
	case ScreenJobs:
		switch source {
		case "jobs", "job_list", "queued_jobs", "failed_jobs", "runners", "runner_status", "job_workers", "index_status", "index_queue", "index_failures":
			return true
		}
	case ScreenNotes:
		switch source {
		case "notes_overview":
			return true
		}
	case ScreenNodes:
		switch source {
		case "nodes", "watched_roots", "watched_root_findings", "watched_root_backup_status", "watched_root_backup_batches", "watched_root_backup_items", "sync_status", "sync_batches", "sync_conflicts", "sync_replicas", "private_backups", "deletion_requests":
			return true
		}
	case ScreenCapabilities:
		switch source {
		case "providers", "capabilities", "provider_advertisements":
			return true
		}
	case ScreenDatabase:
		switch source {
		case "database_objects", "database_index_status", "database_index_queue", "database_index_failures", "database_sync_status", "database_sync_batches", "database_sync_conflicts", "database_sync_replicas", "database_private_backups", "database_deletion_requests", "database_workers", "database_search":
			return true
		}
	case ScreenStorage:
		switch source {
		case "storage_entries", "storage_catalog_path", "storage_filesystem_status", "storage_retention_status", "storage_main_documents_status", "storage_file_transfers", "storage_watched_root_backup_items", "storage_cloud_status", "storage_cloud_config", "storage_cloud_snapshots", "storage_database_status", "storage_backup_status":
			return true
		}
	}
	return false
}

func (s ScreenState) WithSelection(delta int, max int) ScreenState {
	if max <= 0 {
		s.SelectedIndex = 0
		return s
	}
	next := s.SelectedIndex + delta
	if next < 0 {
		next = 0
	}
	if next >= max {
		next = max - 1
	}
	s.SelectedIndex = next
	return s
}

func (s ScreenState) ToggleRawDetails() ScreenState {
	s.RawDetails = !s.RawDetails
	return s
}

func (s ScreenState) MarkLoading() ScreenState {
	s.Status = ScreenLoadLoading
	s.Error = ""
	return s
}
