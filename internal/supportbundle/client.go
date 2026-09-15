package supportbundle

import (
	"context"

	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/cloudstorage"
	"loom.local/loom/internal/health"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/mainstorage"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/search"
	loomstatus "loom.local/loom/internal/status"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/storagedoctor"
	"loom.local/loom/internal/workers"
)

type CoreClient interface {
	Health(context.Context, string) (response.Envelope[health.Report], error)
	Status(context.Context, string) (response.Envelope[loomstatus.Report], error)
	BootstrapStatus(context.Context, string) (response.Envelope[bootstrap.Summary], error)
	MaintenanceDBStatus(context.Context, string) (response.Envelope[maintenance.DBStatus], error)
}

type WorkersClient interface {
	ListWorkers(context.Context, string, workers.WorkerFilter) (response.Envelope[[]workers.WorkerListItem], error)
}

type JobsClient interface {
	JobStatus(context.Context, string) (response.Envelope[jobs.QueueSummary], error)
	ListJobs(context.Context, string, jobs.ListFilter) (response.Envelope[[]jobs.Job], error)
	ListIndexFailures(context.Context, string, search.StatusFilter) (response.Envelope[[]search.IndexStatus], error)
}

type NotesClient interface {
	GetKnowledgeNotesOverview(context.Context, string, knowledge.NotesOverviewInput) (response.Envelope[knowledge.NotesOverview], error)
	ListIndexStatus(context.Context, string, search.StatusFilter) (response.Envelope[[]search.IndexStatus], error)
}

type StorageClient interface {
	GetMainDocumentsStatus(context.Context, string) (response.Envelope[mainstorage.Status], error)
	GetStorageFilesystemStatus(context.Context, string) (response.Envelope[storagedoctor.FilesystemStatus], error)
	GetStorageRetentionStatus(context.Context, string) (response.Envelope[storagecatalog.RetentionStatus], error)
}

type BackupClient interface {
	MaintenanceBackupStatus(context.Context, string) (response.Envelope[maintenance.BackupStatus], error)
}

type ProjectsClient interface {
	ListProjects(context.Context, string, int) (response.Envelope[[]projects.Project], error)
	GetProjectRegistrationStatus(context.Context, string, string) (response.Envelope[projects.ProjectRegistrationDetail], error)
}

type CloudStatusProvider func(context.Context, Options) (cloudstorage.StatusReport, error)
