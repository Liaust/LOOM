package httpapi

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"loom.local/loom/internal/agents"
	"loom.local/loom/internal/artifacts"
	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/box"
	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/cloudstorage"
	"loom.local/loom/internal/communication"
	loomconfig "loom.local/loom/internal/config"
	"loom.local/loom/internal/correlation"
	"loom.local/loom/internal/dropzone"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/events"
	"loom.local/loom/internal/filetransfer"
	"loom.local/loom/internal/health"
	"loom.local/loom/internal/idempotency"
	"loom.local/loom/internal/identity"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/mainstorage"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/minidashboard"
	"loom.local/loom/internal/modules"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/objects"
	"loom.local/loom/internal/policy"
	"loom.local/loom/internal/projectactivation"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectdoctor"
	"loom.local/loom/internal/projectexport"
	"loom.local/loom/internal/projectregistration"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectstate"
	"loom.local/loom/internal/projectwatch"
	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/realtime"
	"loom.local/loom/internal/repostate"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/routing"
	"loom.local/loom/internal/scopes"
	"loom.local/loom/internal/scripts"
	"loom.local/loom/internal/search"
	"loom.local/loom/internal/serviceregistry"
	loomstatus "loom.local/loom/internal/status"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/storagefidelity"
	"loom.local/loom/internal/storageretention"
	loomsync "loom.local/loom/internal/sync"
	mainwatchedroots "loom.local/loom/internal/watchedroots"
	"loom.local/loom/internal/workers"
)

type idempotencyService interface {
	Begin(context.Context, idempotency.BeginInput) (idempotency.BeginResult, error)
	Complete(context.Context, string, string, string, any) error
	Fail(context.Context, string, string, any) error
}

type Services struct {
	DB                              *sql.DB
	Health                          health.Service
	Status                          loomstatus.Service
	Bootstrap                       bootstrap.Service
	Box                             box.Service
	Dropzone                        dropzone.Service
	FileTransfer                    filetransfer.Service
	Idempotency                     idempotencyService
	Identity                        identity.Service
	Nodes                           nodes.Service
	Scopes                          scopes.Service
	Events                          events.Service
	Projects                        projects.Service
	ProjectActivation               projectactivation.Service
	ProjectWatch                    projectwatch.Service
	ProjectArchive                  storagearchive.ProjectRuntimeService
	ProjectPhysicalArchive          ProjectPhysicalArchiveService
	ProjectRepos                    ProjectRepositoryServices
	Objects                         objects.Service
	Search                          search.Service
	StorageArchive                  storagearchive.Service
	WorkspaceArchive                WorkspaceArchiveLifecycleService
	WorkspaceArchiveAuthorizer      WorkspaceArchiveAuthorizer
	WorkspaceArchiveRequestResolver WorkspaceArchiveRequestResolver
	StorageCatalog                  storagecatalog.Service
	StorageFidelity                 storagefidelity.BackfillService
	StorageRetention                storageretention.Service
	MainStorage                     mainstorage.Service
	Scripts                         scripts.Service
	Jobs                            jobs.Service
	Maintenance                     maintenance.Service
	MiniDashboard                   interface {
		Snapshot(context.Context) (minidashboard.DomainSnapshot, error)
	}
	Artifacts                 artifacts.Service
	Automation                automation.Service
	Capabilities              capabilities.Service
	ServiceAllowlists         serviceregistry.AllowlistResolver
	Policy                    policy.Service
	Routing                   routing.Service
	Communication             communication.Service
	Sync                      loomsync.Service
	WatchedRoots              mainwatchedroots.Service
	Realtime                  realtime.Service
	Agents                    agents.Service
	Modules                   modules.Service
	Workers                   workers.Service
	Provenance                provenance.FoundationTransport
	ProvenanceAuthorizer      ProvenanceAuthorizer
	ProvenanceRequestResolver ProvenanceRequestResolver
	EmbeddingRuntime          knowledge.EmbeddingRuntime
	RuntimeConfig             loomconfig.Config
	AllowCloudConfigOverride  bool

	ProjectDeclaration                ProjectDeclarationService
	ProjectDeclarationRequestResolver ProjectDeclarationRequestResolver
}

type Server struct {
	services                  Services
	logger                    *slog.Logger
	projectRegistrationStatus func(context.Context, string) (projects.ProjectRegistrationDetail, error)
	// Private fixture seam; never configured by HTTP input or runtime flags.
	cloudRestoreCleanupConfig func() (cloudstorage.Config, error)
}

func NewServer(services Services, logger *slog.Logger) Server {
	if services.ProjectRepos.State == nil && services.Projects.DB != nil && strings.TrimSpace(services.RuntimeConfig.NodeID) != "" {
		services.ProjectRepos.State = projectstate.Service{
			Reader:    services.Projects,
			LocalNode: services.RuntimeConfig.NodeID,
		}
	}
	if services.ProjectRepos.Provenance == nil {
		if source, ok := services.ProjectRepos.State.(projectstate.ProvenanceProjectObserver); ok {
			services.ProjectRepos.Provenance = source
		}
	}
	if services.ProjectRepos.RepositoryState == nil {
		services.ProjectRepos.RepositoryState = repostate.ProvenanceAdapter{}
	}
	if services.ProjectRepos.Authorizer == nil && services.DB != nil {
		services.ProjectRepos.Authorizer = SQLProjectRepositoryReadAuthorizer{
			DB:           services.DB,
			LocalNodeRef: services.RuntimeConfig.NodeID,
		}
	}
	if services.ProjectRepos.RequestResolver == nil && services.DB != nil {
		services.ProjectRepos.RequestResolver = func(ctx context.Context, correlationID string) (requestctx.Context, error) {
			return requestctx.ResolveBootstrap(ctx, services.DB, correlationID)
		}
	}
	if services.ProjectDeclarationRequestResolver == nil && services.DB != nil {
		services.ProjectDeclarationRequestResolver = func(ctx context.Context, correlationID string) (requestctx.Context, error) {
			return requestctx.ResolveBootstrap(ctx, services.DB, correlationID)
		}
	}
	server := Server{
		services: services,
		logger:   logger,
	}
	if services.Projects.DB != nil {
		server.projectRegistrationStatus = services.Projects.GetProjectRegistrationStatus
	}
	return server
}

func (s Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", s.handleHealth)
	mux.HandleFunc("/v1/status", s.handleStatus)
	mux.HandleFunc("/v1/mini-dashboard/status", s.handleMiniDashboardStatus)
	mux.HandleFunc("/v1/bootstrap/status", s.handleBootstrapStatus)
	mux.HandleFunc("/v1/box/watch-policy/apply", s.handleBoxWatchPolicyApply)
	mux.HandleFunc("/v1/box/watch-status", s.handleBoxWatchStatus)
	mux.HandleFunc("/v1/box/dropzone/upload-sessions", s.handleDropzoneUploadSessions)
	mux.HandleFunc("/v1/box/dropzone/upload-sessions/", s.handleDropzoneUploadSession)
	mux.HandleFunc("/v1/file-transfers", s.handleFileTransfers)
	mux.HandleFunc("/v1/file-transfers/", s.handleFileTransfer)
	mux.HandleFunc("/v1/actors/", s.handleActor)
	mux.HandleFunc("/v1/nodes", s.handleNodes)
	mux.HandleFunc("/v1/nodes/", s.handleNode)
	mux.HandleFunc("/v1/node-enrollment-tokens", s.handleNodeEnrollmentTokens)
	mux.HandleFunc("/v1/node-enrollment-requests", s.handleNodeEnrollmentRequests)
	mux.HandleFunc("/v1/node-enrollment-requests/", s.handleNodeEnrollmentRequest)
	mux.HandleFunc("/v1/node-agent/enroll", s.handleNodeAgentEnroll)
	mux.HandleFunc("/v1/node-agent/heartbeat", s.handleNodeAgentHeartbeat)
	mux.HandleFunc("/v1/node-agent/poll", s.handleNodeAgentPoll)
	mux.HandleFunc("/v1/node-agent/ack", s.handleNodeAgentAck)
	mux.HandleFunc("/v1/node-agent/providers/advertise", s.handleNodeAgentProviderAdvertise)
	mux.HandleFunc("/v1/node-agent/capability-result", s.handleNodeAgentCapabilityResult)
	mux.HandleFunc("/v1/node-agent/sync/batches", s.handleNodeAgentSyncBatches)
	mux.HandleFunc("/v1/node-agent/sync/object-upload", s.handleNodeAgentSyncObjectUpload)
	mux.HandleFunc("/v1/node-agent/sync/private-backup", s.handleNodeAgentSyncPrivateBackup)
	mux.HandleFunc("/v1/node-agent/sync/deletion-request", s.handleNodeAgentSyncDeletionRequest)
	mux.HandleFunc("/v1/node-agent/watched-roots/report", s.handleNodeAgentWatchedRootReport)
	mux.HandleFunc("/v1/node-agent/watched-roots/backup-batches", s.handleNodeAgentWatchedRootBackupBatch)
	mux.HandleFunc("/v1/scopes", s.handleScopes)
	mux.HandleFunc("/v1/scopes/", s.handleScope)
	mux.HandleFunc("/v1/events", s.handleEvents)
	mux.HandleFunc("/v1/events/", s.handleEvent)
	mux.HandleFunc("/v1/project-declaration-plans", s.handleProjectDeclarationPlans)
	mux.HandleFunc("/v1/project-declaration-operations", s.handleProjectDeclarationApply)
	mux.HandleFunc("/v1/project-declaration-operations/", s.handleProjectDeclarationOperation)
	mux.HandleFunc("/v1/project-scaffolds", s.handleProjectScaffolds)
	mux.HandleFunc("/v1/project-facet-additions", s.handleProjectFacetAdditions)
	mux.HandleFunc("/v1/project-layout-migrations", s.handleProjectLayoutMigrations)
	mux.HandleFunc("/v1/project-exports", s.handleProjectExports)
	mux.HandleFunc("/v1/project-contract-analyses", s.handleProjectContractAnalyses)
	mux.HandleFunc("/v1/project-contract-registrations", s.handleProjectContractRegistrations)
	mux.HandleFunc("/v1/project-contract-registrations/from-backend", s.handleProjectContractRegistrationsFromBackend)
	mux.HandleFunc("/v1/project-contract-registrations/", s.handleProjectContractRegistration)
	mux.HandleFunc("/v1/projects", s.handleProjects)
	mux.HandleFunc("/v1/projects/", s.handleProject)
	mux.HandleFunc("/v1/knowledge/notes/overview", s.handleKnowledgeNotesOverview)
	mux.HandleFunc("/v1/knowledge/notes/roots", s.handleKnowledgeNotesRoots)
	mux.HandleFunc("/v1/knowledge/notes/roots/reconcile", s.handleKnowledgeNotesRootsReconcile)
	mux.HandleFunc("/v1/knowledge/notes/objects", s.handleKnowledgeNotesObjects)
	mux.HandleFunc("/v1/knowledge/notes/objects/reconcile", s.handleKnowledgeNotesObjectsReconcile)
	mux.HandleFunc("/v1/knowledge/notes/objects/", s.handleKnowledgeNotesObject)
	mux.HandleFunc("/v1/knowledge/notes/passages/", s.handleKnowledgeNotesPassage)
	mux.HandleFunc("/v1/knowledge/notes/search", s.handleKnowledgeNotesSearch)
	mux.HandleFunc("/v1/knowledge/notes/embeddings/status", s.handleKnowledgeNotesEmbeddingsStatus)
	mux.HandleFunc("/v1/knowledge/notes/embeddings/enable", s.handleKnowledgeNotesEmbeddingsEnable)
	mux.HandleFunc("/v1/knowledge/notes/embeddings/disable", s.handleKnowledgeNotesEmbeddingsDisable)
	mux.HandleFunc("/v1/knowledge/notes/pipelines/status", s.handleKnowledgeNotesPipelinesStatus)
	mux.HandleFunc("/v1/knowledge/notes/pipelines/failures", s.handleKnowledgeNotesPipelineFailures)
	mux.HandleFunc("/v1/knowledge/notes/pipelines/policy", s.handleKnowledgeNotesPipelinePolicy)
	mux.HandleFunc("/v1/knowledge/notes/pipelines/backfill", s.handleKnowledgeNotesPipelineBackfill)
	mux.HandleFunc("/v1/knowledge/notes/pipelines", s.handleKnowledgeNotesPipelines)
	mux.HandleFunc("/v1/knowledge/notes/pipelines/", s.handleKnowledgeNotesPipelineRef)
	mux.HandleFunc("/v1/knowledge/notes/reprocess", s.handleKnowledgeNotesReprocess)
	mux.HandleFunc("/v1/knowledge/notes/projection/status", s.handleKnowledgeNotesProjectionStatus)
	mux.HandleFunc("/v1/knowledge/notes/projection/rebuild", s.handleKnowledgeNotesProjectionRebuild)
	mux.HandleFunc("/v1/knowledge/notes/workers/indexer/run", s.handleKnowledgeNotesIndexerRun)
	mux.HandleFunc("/v1/knowledge/notes/workers/embedder/run", s.handleKnowledgeNotesEmbedderRun)
	mux.HandleFunc("/v1/knowledge/notes/workers/coordinator/run", s.handleKnowledgeNotesIndexerRun)
	mux.HandleFunc("/v1/knowledge/notes/workers/heavy/run", s.handleKnowledgeNotesEmbedderRun)
	mux.HandleFunc("/v1/provenance/health", s.handleProvenanceHealth)
	mux.HandleFunc("/v1/provenance/search", s.handleProvenanceSearch)
	mux.HandleFunc("/v1/provenance/repos", s.handleProvenanceRepositories)
	mux.HandleFunc("/v1/provenance/repos/", s.handleProvenanceRepository)
	mux.HandleFunc("/v1/provenance/projects/", s.handleProvenanceProjectProjection)
	mux.HandleFunc("/v1/provenance/candidates", s.handleProvenanceCandidates)
	mux.HandleFunc("/v1/provenance/candidates/", s.handleProvenanceCandidate)
	mux.HandleFunc("/v1/provenance/records", s.handleProvenanceRecords)
	mux.HandleFunc("/v1/provenance/records/", s.handleProvenanceRecord)
	mux.HandleFunc("/v1/provenance/relationships", s.handleProvenanceRelationships)
	mux.HandleFunc("/v1/provenance/relationships/", s.handleProvenanceRelationship)
	mux.HandleFunc("/v1/provenance/cases", s.handleProvenanceCases)
	mux.HandleFunc("/v1/provenance/cases/", s.handleProvenanceCase)
	mux.HandleFunc("/v1/provenance/operations", s.handleProvenanceOperations)
	mux.HandleFunc("/v1/objects", s.handleObjects)
	mux.HandleFunc("/v1/objects/ingest", s.handleObjectIngest)
	mux.HandleFunc("/v1/objects/", s.handleObject)
	mux.HandleFunc("/v1/storage/tree", s.handleRetiredStorageTree)
	mux.HandleFunc("/v1/storage/filesystem/status", s.handleStorageFilesystemStatus)
	mux.HandleFunc("/v1/storage/main-documents/status", s.handleMainDocumentsStatus)
	mux.HandleFunc("/v1/storage/main-documents/reconcile", s.handleMainDocumentsReconcile)
	mux.HandleFunc("/v1/storage/main-documents/retention/backfill", s.handleMainDocumentsRetentionBackfill)
	mux.HandleFunc("/v1/storage/main-documents/protection", s.handleMainDocumentsProtection)
	mux.HandleFunc("/v1/storage/main-documents/safe-delete", s.handleMainDocumentsSafeDelete)
	mux.HandleFunc("/v1/storage/lane/accept", s.handleLaneAccept)
	mux.HandleFunc("/v1/storage/fidelity/backfill", s.handleStorageFidelityBackfill)
	mux.HandleFunc("/v1/storage/export/status", s.handleStorageExportStatus)
	mux.HandleFunc("/v1/storage/export/refresh", s.handleStorageExportRefresh)
	mux.HandleFunc("/v1/storage/export/rebuild", s.handleStorageExportRebuild)
	mux.HandleFunc("/v1/storage/retention/status", s.handleStorageRetentionStatus)
	mux.HandleFunc("/v1/storage/archive", s.handleStorageArchive)
	mux.HandleFunc("/v1/storage/workspace-archive/plan", s.handleWorkspaceArchivePlan)
	mux.HandleFunc("/v1/storage/workspace-archive/apply", s.handleWorkspaceArchiveApply)
	mux.HandleFunc("/v1/storage/workspace-archive/inspect", s.handleWorkspaceArchiveInspect)
	mux.HandleFunc("/v1/storage/workspace-archive/recover", s.handleWorkspaceArchiveRecover)
	mux.HandleFunc("/v1/storage/workspace-archive/restore-plan", s.handleWorkspaceArchiveRestorePlan)
	mux.HandleFunc("/v1/storage/workspace-archive/restore-apply", s.handleWorkspaceArchiveRestoreApply)
	mux.HandleFunc("/v1/storage/safe-to-delete", s.handleStorageSafeToDelete)
	mux.HandleFunc("/v1/storage/fetch", s.handleStorageFetch)
	mux.HandleFunc("/v1/storage/restore", s.handleStorageRestore)
	mux.HandleFunc("/v1/storage/tombstones", s.handleStorageTombstone)
	mux.HandleFunc("/v1/storage/entries", s.handleStorageEntries)
	mux.HandleFunc("/v1/storage/entries/", s.handleStorageEntry)
	mux.HandleFunc("/v1/storage/inspect-path", s.handleStorageInspectPath)
	mux.HandleFunc("/v1/storage/resolve", s.handleStorageResolve)
	mux.HandleFunc("/v1/search", s.handleSearch)
	mux.HandleFunc("/v1/indexes/status", s.handleIndexStatus)
	mux.HandleFunc("/v1/indexes/queue/", s.handleIndexQueueItem)
	mux.HandleFunc("/v1/indexes/queue", s.handleIndexQueue)
	mux.HandleFunc("/v1/indexes/failures", s.handleIndexFailures)
	mux.HandleFunc("/v1/indexes/retry", s.handleIndexRetry)
	mux.HandleFunc("/v1/indexes/retry-failed", s.handleIndexRetryFailed)
	mux.HandleFunc("/v1/indexes/rebuild/object", s.handleIndexRebuild)
	mux.HandleFunc("/v1/indexes/explain", s.handleIndexExplain)
	mux.HandleFunc("/v1/index/status", s.handleIndexStatus)
	mux.HandleFunc("/v1/index/rebuild", s.handleIndexRebuild)
	mux.HandleFunc("/v1/scripts/register", s.handleScriptRegister)
	mux.HandleFunc("/v1/scripts", s.handleScripts)
	mux.HandleFunc("/v1/scripts/", s.handleScript)
	mux.HandleFunc("/v1/jobs", s.handleJobs)
	mux.HandleFunc("/v1/jobs/", s.handleJob)
	mux.HandleFunc("/v1/runners/status", s.handleRunnersStatus)
	mux.HandleFunc("/v1/runners", s.handleRunners)
	mux.HandleFunc("/v1/runners/", s.handleRunner)
	mux.HandleFunc("/v1/artifacts", s.handleArtifacts)
	mux.HandleFunc("/v1/artifacts/", s.handleArtifact)
	mux.HandleFunc("/v1/automations", s.handleAutomations)
	mux.HandleFunc("/v1/automations/", s.handleAutomation)
	mux.HandleFunc("/v1/integrations", s.handleIntegrations)
	mux.HandleFunc("/v1/integrations/", s.handleIntegration)
	mux.HandleFunc("/v1/integration-auth-profiles/", s.handleIntegrationAuthProfile)
	mux.HandleFunc("/v1/direct-events/ingest/", s.handleDirectEventIngest)
	mux.HandleFunc("/v1/direct-events/failures", s.handleDirectEventFailures)
	mux.HandleFunc("/v1/direct-events/status", s.handleDirectEventStatus)
	mux.HandleFunc("/v1/direct-events", s.handleDirectEvents)
	mux.HandleFunc("/v1/direct-events/", s.handleDirectEvent)
	mux.HandleFunc("/v1/direct-event-endpoints", s.handleDirectEventEndpoints)
	mux.HandleFunc("/v1/direct-event-endpoints/", s.handleDirectEventEndpoint)
	mux.HandleFunc("/v1/schedules/status", s.handleScheduleStatus)
	mux.HandleFunc("/v1/schedules", s.handleSchedules)
	mux.HandleFunc("/v1/schedules/", s.handleSchedule)
	mux.HandleFunc("/v1/schedule-fires", s.handleScheduleFires)
	mux.HandleFunc("/v1/schedule-fires/", s.handleScheduleFire)
	mux.HandleFunc("/v1/invocations/failures", s.handleInvocationFailures)
	mux.HandleFunc("/v1/invocations", s.handleInvocations)
	mux.HandleFunc("/v1/invocations/", s.handleInvocation)
	mux.HandleFunc("/v1/provider-advertisements", s.handleProviderAdvertisements)
	mux.HandleFunc("/v1/provider-advertisements/", s.handleProviderAdvertisement)
	mux.HandleFunc("/v1/providers", s.handleProviders)
	mux.HandleFunc("/v1/providers/", s.handleProvider)
	mux.HandleFunc("/v1/services", s.handleServices)
	mux.HandleFunc("/v1/services/", s.handleService)
	mux.HandleFunc("/v1/service-registrations/", s.handleServiceRegistration)
	mux.HandleFunc("/v1/capabilities/search", s.handleCapabilitySearch)
	mux.HandleFunc("/v1/capability-runtime-bindings", s.handleCapabilityRuntimeBindings)
	mux.HandleFunc("/v1/capability-runtime-bindings/", s.handleCapabilityRuntimeBinding)
	mux.HandleFunc("/v1/capabilities", s.handleCapabilities)
	mux.HandleFunc("/v1/capabilities/", s.handleCapability)
	mux.HandleFunc("/v1/modules/register", s.handleModuleRegister)
	mux.HandleFunc("/v1/modules", s.handleModules)
	mux.HandleFunc("/v1/modules/", s.handleModule)
	mux.HandleFunc("/v1/module-versions/", s.handleModuleVersion)
	mux.HandleFunc("/v1/module-installations/", s.handleModuleInstallation)
	mux.HandleFunc("/v1/module-backup-exports/", s.handleModuleBackupExport)
	mux.HandleFunc("/v1/workers/repair-stale", s.handleWorkersRepairStale)
	mux.HandleFunc("/v1/workers", s.handleWorkers)
	mux.HandleFunc("/v1/workers/", s.handleWorker)
	mux.HandleFunc("/v1/maintenance/status", s.handleMaintenanceStatus)
	mux.HandleFunc("/v1/maintenance/findings", s.handleMaintenanceFindings)
	mux.HandleFunc("/v1/maintenance/db/status", s.handleMaintenanceDBStatus)
	mux.HandleFunc("/v1/maintenance/db/compact", s.handleMaintenanceDBCompact)
	mux.HandleFunc("/v1/maintenance/backup/status", s.handleMaintenanceBackupStatus)
	mux.HandleFunc("/v1/maintenance/backup/list", s.handleMaintenanceBackupList)
	mux.HandleFunc("/v1/maintenance/backup/run", s.handleMaintenanceBackupRun)
	mux.HandleFunc("/v1/maintenance/backup/verify", s.handleMaintenanceBackupVerify)
	mux.HandleFunc("/v1/backup/coverage", s.handleBackupCoverage)
	mux.HandleFunc("/v1/backup/contracts", s.handleBackupContracts)
	mux.HandleFunc("/v1/backup/contracts/", s.handleBackupContract)
	mux.HandleFunc("/v1/maintenance/object-store/status", s.handleMaintenanceObjectStoreStatus)
	mux.HandleFunc("/v1/maintenance/object-store/scan", s.handleMaintenanceObjectStoreScan)
	mux.HandleFunc("/v1/policy/explain", s.handlePolicyExplain)
	mux.HandleFunc("/v1/policy/decisions", s.handlePolicyDecisions)
	mux.HandleFunc("/v1/policy/decisions/", s.handlePolicyDecision)
	mux.HandleFunc("/v1/approvals", s.handleApprovals)
	mux.HandleFunc("/v1/approvals/", s.handleApproval)
	mux.HandleFunc("/v1/grants", s.handleGrants)
	mux.HandleFunc("/v1/grants/", s.handleGrant)
	mux.HandleFunc("/v1/capability-calls", s.handleCapabilityCalls)
	mux.HandleFunc("/v1/capability-calls/", s.handleCapabilityCall)
	mux.HandleFunc("/v1/routes", s.handleRoutes)
	mux.HandleFunc("/v1/routes/", s.handleRoute)
	mux.HandleFunc("/v1/communication/health", s.handleCommunicationHealth)
	mux.HandleFunc("/v1/cloud/status/live", s.handleCloudStatusLive)
	mux.HandleFunc("/v1/cloud/doctor/live", s.handleCloudDoctorLive)
	mux.HandleFunc("/v1/cloud/snapshot/status/live", s.handleCloudSnapshotStatusLive)
	mux.HandleFunc("/v1/cloud/snapshot/run", s.handleCloudSnapshotRun)
	mux.HandleFunc("/v1/cloud/snapshot/list/live", s.handleCloudSnapshotListLive)
	mux.HandleFunc("/v1/cloud/snapshot/verify/live", s.handleCloudSnapshotVerifyLive)
	mux.HandleFunc("/v1/cloud/snapshot/restore-drill/live", s.handleCloudSnapshotRestoreDrillLive)
	mux.HandleFunc("/v1/cloud/snapshot/restore-cleanup/plan", s.handleCloudRestoreCleanupPlan)
	mux.HandleFunc("/v1/cloud/snapshot/restore-cleanup/apply", s.handleCloudRestoreCleanupApply)
	mux.HandleFunc("/v1/cloud/snapshot/retention/plan/live", s.handleCloudSnapshotRetentionPlanLive)
	mux.HandleFunc("/v1/cloud/snapshot/retention/apply/live", s.handleCloudSnapshotRetentionApplyLive)
	mux.HandleFunc("/v1/cloud/snapshot/backend/status/live", s.handleCloudSnapshotBackendStatusLive)
	mux.HandleFunc("/v1/cloud/snapshot/backend/init/live", s.handleCloudSnapshotBackendInitLive)
	mux.HandleFunc("/v1/communication/messages", s.handleCommunicationMessages)
	mux.HandleFunc("/v1/communication/messages/", s.handleCommunicationMessage)
	mux.HandleFunc("/v1/realtime/topics", s.handleRealtimeTopics)
	mux.HandleFunc("/v1/realtime/topics/", s.handleRealtimeTopic)
	mux.HandleFunc("/v1/realtime/subscriptions", s.handleRealtimeSubscriptions)
	mux.HandleFunc("/v1/realtime/subscriptions/", s.handleRealtimeSubscription)
	mux.HandleFunc("/v1/realtime/presence", s.handleRealtimePresence)
	mux.HandleFunc("/v1/realtime/presence/", s.handleRealtimePresenceRef)
	mux.HandleFunc("/v1/realtime/notifications", s.handleRealtimeNotifications)
	mux.HandleFunc("/v1/realtime/notifications/", s.handleRealtimeNotification)
	mux.HandleFunc("/v1/realtime/progress/", s.handleRealtimeProgress)
	mux.HandleFunc("/v1/realtime/leases", s.handleRealtimeLeases)
	mux.HandleFunc("/v1/realtime/leases/", s.handleRealtimeLease)
	mux.HandleFunc("/v1/agents/access-sessions", s.handleAgentAccessSessions)
	mux.HandleFunc("/v1/agents/access-sessions/", s.handleAgentAccessSession)
	mux.HandleFunc("/v1/agents/work-contexts", s.handleAgentWorkContexts)
	mux.HandleFunc("/v1/agents/work-contexts/", s.handleAgentWorkContext)
	mux.HandleFunc("/v1/agents/tool-calls/", s.handleAgentToolCall)
	mux.HandleFunc("/v1/agents/tool-views/", s.handleAgentToolView)
	mux.HandleFunc("/v1/sync/status", s.handleSyncStatus)
	mux.HandleFunc("/v1/sync/batches", s.handleSyncBatches)
	mux.HandleFunc("/v1/sync/conflicts", s.handleSyncConflicts)
	mux.HandleFunc("/v1/sync/replicas", s.handleSyncReplicas)
	mux.HandleFunc("/v1/sync/private-backups", s.handleSyncPrivateBackups)
	mux.HandleFunc("/v1/sync/deletion-requests", s.handleSyncDeletionRequests)
	mux.HandleFunc("/v1/sync/deletion-requests/", s.handleSyncDeletionRequest)
	mux.HandleFunc("/v1/watched-roots/status", s.handleWatchedRootStatus)
	mux.HandleFunc("/v1/watched-roots/findings", s.handleWatchedRootFindings)
	mux.HandleFunc("/v1/watched-roots/backups/status", s.handleWatchedRootBackupStatus)
	mux.HandleFunc("/v1/watched-roots/backups/batches", s.handleWatchedRootBackupBatches)
	mux.HandleFunc("/v1/watched-roots/backups/items", s.handleWatchedRootBackupItems)
	return mux
}

func (s Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)

	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "runtime", r.URL.Path, "Method is not allowed.", nil)
		return
	}

	report := s.services.Health.Check(ctx)
	s.logger.Info("health request",
		slog.String("component", "httpapi"),
		slog.String("correlation_id", correlationID),
		slog.String("status", report.Status),
	)
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, report))
}

func (s Server) handleBoxWatchPolicyApply(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "box", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input box.WatchApplyInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "box", "body", "Request body is not valid Box watch apply JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request_context.unavailable", "runtime", "bootstrap", "Could not resolve request context.", err)
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "box.watch_policy.apply", map[string]any{"input": input})
	if !proceed {
		return
	}
	result, err := s.services.Box.ApplyWatchPolicy(ctx, req, input)
	if err != nil {
		idemErr := loomerrors.Wrap("box.watch_policy_apply_failed", "box", input.Resolved.RootPath, "Could not apply Box watch policy.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, http.StatusBadRequest, "box.watch_policy_apply_failed", "box", input.Resolved.RootPath, "Could not apply Box watch policy.", err)
		return
	}
	resultRef := result.Plan.BoxID
	if resultRef == "" {
		resultRef = result.Plan.RootPath
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, result)
	s.completeIdempotency(ctx, idemRecord, "box_watch_policy", resultRef, envelope)
	response.WriteJSON(w, http.StatusOK, envelope)
}

func (s Server) handleBoxWatchStatus(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "box", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input box.WatchStatusInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "box", "body", "Request body is not valid Box watch status JSON.", err)
		return
	}
	result, err := s.services.Box.WatchStatus(ctx, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "box.watch_status_failed", "box", input.Resolved.RootPath, "Could not read Box watch status.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleDropzoneUploadSessions(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	switch r.Method {
	case http.MethodGet:
		limit, err := parseLimit(r, 50)
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "dropzone", "limit", "Limit must be a positive integer.", err)
			return
		}
		sessions, err := s.services.Dropzone.ListUploadSessions(ctx, limit)
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "dropzone.upload_sessions_list_failed", "dropzone", "upload_sessions", "Could not list Dropzone upload sessions.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, sessions))
	case http.MethodPost:
		s.writeError(w, correlationID, http.StatusGone, "dropzone.runtime_retired", "dropzone", "upload_sessions", "Dropzone admission is retired; historical upload sessions are read-only.", nil)
	default:
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "dropzone", r.URL.Path, "Method is not allowed.", nil)
	}
}

func (s Server) handleDropzoneUploadSession(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	ref := pathRef(r.URL.Path, "/v1/box/dropzone/upload-sessions/")
	sessionID, action := splitDropzoneSessionAction(ref)
	if sessionID == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "dropzone.upload_session_ref_required", "dropzone", "upload_session", "Upload session reference is required.", nil)
		return
	}
	switch action {
	case "":
		if r.Method != http.MethodGet {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "dropzone", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		session, err := s.services.Dropzone.GetUploadSession(ctx, sessionID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusNotFound, "dropzone.upload_session_not_found", "dropzone", sessionID, "Dropzone upload session was not found.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, session))
	case "chunks":
		s.writeError(w, correlationID, http.StatusGone, "dropzone.runtime_retired", "dropzone", sessionID, "Dropzone chunk admission is retired; historical upload sessions are read-only.", nil)
	case "complete":
		s.writeError(w, correlationID, http.StatusGone, "dropzone.runtime_retired", "dropzone", sessionID, "Dropzone completion is retired; historical upload sessions are read-only.", nil)
	case "abort":
		s.writeError(w, correlationID, http.StatusGone, "dropzone.runtime_retired", "dropzone", sessionID, "Dropzone mutation is retired; historical upload sessions are read-only.", nil)
	default:
		s.writeError(w, correlationID, http.StatusNotFound, "dropzone.upload_session_action_unknown", "dropzone", action, "Unknown Dropzone upload session action.", nil)
	}
}

func (s Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "runtime", r.URL.Path, "Method is not allowed.", nil)
		return
	}

	report := s.services.Status.Check(ctx)
	s.logger.Info("status request",
		slog.String("component", "httpapi"),
		slog.String("correlation_id", correlationID),
		slog.String("status", report.Status),
	)
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, report))
}

func (s Server) handleBootstrapStatus(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "bootstrap", r.URL.Path, "Method is not allowed.", nil)
		return
	}

	status, err := s.services.Bootstrap.BootstrapStatus(ctx)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "bootstrap.status_failed", "bootstrap", "status", "Could not read bootstrap status.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, status))
}

func (s Server) handleActor(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "identity", r.URL.Path, "Method is not allowed.", nil)
		return
	}

	ref := pathRef(r.URL.Path, "/v1/actors/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "actor.ref_required", "identity", "actor", "Actor reference is required.", nil)
		return
	}

	actor, err := s.services.Identity.GetActor(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "identity", ref, "Actor was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, actor))
}

func (s Server) handleNode(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	ref := pathRef(r.URL.Path, "/v1/nodes/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "node.ref_required", "nodes", "node", "Node reference is required.", nil)
		return
	}

	if strings.HasSuffix(ref, "/credentials/issue") {
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "nodes", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		nodeRef := strings.Trim(strings.TrimSuffix(ref, "/credentials/issue"), "/")
		var input nodes.IssueNodeCredentialInput
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "nodes", "body", "Request body is not valid node credential issue JSON.", err)
			return
		}
		input.NodeRef = nodeRef
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "nodes", "bootstrap", "Could not resolve request context.", err)
			return
		}
		idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "node.credential.issue", input)
		if !proceed {
			return
		}
		result, err := s.services.Nodes.IssueNodeCredential(ctx, req, input)
		if err != nil {
			idemErr := loomerrors.Wrap("node.credential_issue_failed", "nodes", nodeRef, "Could not issue node credential.", err)
			s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
			s.writeError(w, correlationID, http.StatusBadRequest, "node.credential_issue_failed", "nodes", nodeRef, "Could not issue node credential.", err)
			return
		}
		envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, result)
		s.completeIdempotency(ctx, idemRecord, "node_credential", result.Credential.NodeCredentialID, envelope)
		response.WriteJSON(w, http.StatusCreated, envelope)
		return
	}

	if strings.HasSuffix(ref, "/decommission") {
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "nodes", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		nodeRef := strings.Trim(strings.TrimSuffix(ref, "/decommission"), "/")
		var input nodes.DecommissionNodeInput
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "nodes", "body", "Request body is not valid node decommission JSON.", err)
			return
		}
		input.NodeRef = nodeRef
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "nodes", "bootstrap", "Could not resolve request context.", err)
			return
		}
		idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "node.decommission", input)
		if !proceed {
			return
		}
		result, err := s.services.Nodes.DecommissionNode(ctx, req, input)
		if err != nil {
			idemErr := loomerrors.Wrap("node.decommission_failed", "nodes", nodeRef, "Could not decommission node.", err)
			s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
			s.writeError(w, correlationID, http.StatusBadRequest, "node.decommission_failed", "nodes", nodeRef, "Could not decommission node.", err)
			return
		}
		envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, result)
		s.completeIdempotency(ctx, idemRecord, "node", result.Node.NodeID, envelope)
		response.WriteJSON(w, http.StatusOK, envelope)
		return
	}

	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "nodes", r.URL.Path, "Method is not allowed.", nil)
		return
	}

	if strings.HasSuffix(ref, "/health") {
		nodeRef := strings.Trim(strings.TrimSuffix(ref, "/health"), "/")
		health, err := s.services.Nodes.GetNodeHealth(ctx, nodeRef)
		if err != nil {
			s.writeLookupError(w, correlationID, "nodes", nodeRef, "Node health was not found.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, health))
		return
	}

	node, err := s.services.Nodes.GetNode(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "nodes", ref, "Node was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, node))
}

func (s Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "nodes", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "nodes", "limit", "Limit must be a positive integer.", err)
		return
	}
	nodes, err := s.services.Nodes.ListNodes(ctx, limit)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "nodes.list_failed", "nodes", "list", "Could not list nodes.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, nodes))
}

func (s Server) handleNodeEnrollmentTokens(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "nodes", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input nodes.CreateEnrollmentTokenInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "nodes", "body", "Request body is not valid enrollment token JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "nodes", "bootstrap", "Could not resolve request context.", err)
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "node.enrollment_token.create", input)
	if !proceed {
		return
	}
	result, err := s.services.Nodes.CreateEnrollmentToken(ctx, req, input)
	if err != nil {
		idemErr := loomerrors.Wrap("node_enrollment_token.create_failed", "nodes", "enrollment_token", "Could not create node enrollment token.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, http.StatusBadRequest, "node_enrollment_token.create_failed", "nodes", "enrollment_token", "Could not create node enrollment token.", err)
		return
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, result)
	s.completeIdempotency(ctx, idemRecord, "node_enrollment_token", result.Token.NodeEnrollmentTokenID, envelope)
	response.WriteJSON(w, http.StatusCreated, envelope)
}

func (s Server) handleNodeEnrollmentRequests(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleNodeEnrollmentRequestList(w, r)
	case http.MethodPost:
		s.handleNodeEnrollmentRequestCreate(w, r)
	default:
		correlationID := correlation.Normalize(r.Header.Get(correlation.Header))
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "nodes", r.URL.Path, "Method is not allowed.", nil)
	}
}

func (s Server) handleNodeEnrollmentRequestList(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "nodes", "limit", "Limit must be a positive integer.", err)
		return
	}
	requests, err := s.services.Nodes.ListEnrollmentRequests(ctx, nodes.ListEnrollmentRequestsFilter{
		Limit:  limit,
		Status: r.URL.Query().Get("status"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "node_enrollment_requests.list_failed", "nodes", "enrollment_requests", "Could not list node enrollment requests.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, requests))
}

func (s Server) handleNodeEnrollmentRequestCreate(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	var input nodes.CreateEnrollmentRequestInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "nodes", "body", "Request body is not valid enrollment request JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "nodes", "bootstrap", "Could not resolve request context.", err)
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "node.enrollment_request.create", input)
	if !proceed {
		return
	}
	enrollment, err := s.services.Nodes.CreateEnrollmentRequest(ctx, req, input)
	if err != nil {
		idemErr := loomerrors.Wrap("node_enrollment_request.create_failed", "nodes", input.RequestedNodeKey, "Could not create node enrollment request.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, http.StatusBadRequest, "node_enrollment_request.create_failed", "nodes", input.RequestedNodeKey, "Could not create node enrollment request.", err)
		return
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, enrollment)
	s.completeIdempotency(ctx, idemRecord, "node_enrollment_request", enrollment.NodeEnrollmentRequestID, envelope)
	response.WriteJSON(w, http.StatusCreated, envelope)
}

func (s Server) handleNodeEnrollmentRequest(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	ref := pathRef(r.URL.Path, "/v1/node-enrollment-requests/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "node_enrollment_request.ref_required", "nodes", "enrollment_request", "Enrollment request reference is required.", nil)
		return
	}

	if strings.HasSuffix(ref, "/approve") {
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "nodes", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		requestRef := strings.Trim(strings.TrimSuffix(ref, "/approve"), "/")
		var input nodes.ApproveEnrollmentInput
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "nodes", "body", "Request body is not valid enrollment approval JSON.", err)
			return
		}
		input.EnrollmentRequestRef = requestRef
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "nodes", "bootstrap", "Could not resolve request context.", err)
			return
		}
		idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "node.enrollment_request.approve", input)
		if !proceed {
			return
		}
		result, err := s.services.Nodes.ApproveEnrollment(ctx, req, input)
		if err != nil {
			idemErr := loomerrors.Wrap("node_enrollment_request.approve_failed", "nodes", requestRef, "Could not approve node enrollment request.", err)
			s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
			s.writeError(w, correlationID, http.StatusBadRequest, "node_enrollment_request.approve_failed", "nodes", requestRef, "Could not approve node enrollment request.", err)
			return
		}
		envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, result)
		s.completeIdempotency(ctx, idemRecord, "node", result.Node.NodeID, envelope)
		response.WriteJSON(w, http.StatusOK, envelope)
		return
	}

	if strings.HasSuffix(ref, "/deny") {
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "nodes", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		requestRef := strings.Trim(strings.TrimSuffix(ref, "/deny"), "/")
		var input nodes.DenyEnrollmentInput
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "nodes", "body", "Request body is not valid enrollment denial JSON.", err)
			return
		}
		input.EnrollmentRequestRef = requestRef
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "nodes", "bootstrap", "Could not resolve request context.", err)
			return
		}
		idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "node.enrollment_request.deny", input)
		if !proceed {
			return
		}
		enrollment, err := s.services.Nodes.DenyEnrollment(ctx, req, input)
		if err != nil {
			idemErr := loomerrors.Wrap("node_enrollment_request.deny_failed", "nodes", requestRef, "Could not deny node enrollment request.", err)
			s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
			s.writeError(w, correlationID, http.StatusBadRequest, "node_enrollment_request.deny_failed", "nodes", requestRef, "Could not deny node enrollment request.", err)
			return
		}
		envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, enrollment)
		s.completeIdempotency(ctx, idemRecord, "node_enrollment_request", enrollment.NodeEnrollmentRequestID, envelope)
		response.WriteJSON(w, http.StatusOK, envelope)
		return
	}

	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "nodes", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	enrollment, err := s.services.Nodes.GetEnrollmentRequest(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "nodes", ref, "Enrollment request was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, enrollment))
}

func (s Server) handleNodeAgentEnroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		correlationID := correlation.Normalize(r.Header.Get(correlation.Header))
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "nodes", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	s.handleNodeEnrollmentRequestCreate(w, r)
}

func (s Server) handleNodeAgentHeartbeat(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "nodes", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input nodes.HeartbeatInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "nodes", "body", "Request body is not valid heartbeat JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "nodes", "bootstrap", "Could not resolve request context.", err)
		return
	}
	heartbeat, err := s.services.Nodes.IngestHeartbeat(ctx, req, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "node_heartbeat.failed", "nodes", input.NodeRef, "Could not ingest node heartbeat.", err)
		return
	}
	if _, err := s.services.Realtime.ProjectNodeHeartbeat(ctx, req, realtime.NodePresenceInput{
		NodeID:      heartbeat.NodeID,
		HeartbeatID: heartbeat.NodeHeartbeatID,
		State:       heartbeat.PresenceState,
		LastSeenAt:  heartbeat.ReceivedAt,
		Metadata:    heartbeat.Metadata,
	}); err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "realtime.presence_projection_failed", "realtime", heartbeat.NodeID, "Could not project heartbeat into realtime presence.", err)
		return
	}
	response.WriteJSON(w, http.StatusCreated, response.Success(correlationID, heartbeat))
}

func (s Server) handleNodeAgentPoll(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "communication", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input communication.PollInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "communication", "body", "Request body is not valid node-agent poll JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "communication", "bootstrap", "Could not resolve request context.", err)
		return
	}
	result, err := s.services.Communication.Poll(ctx, req, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "communication.poll_failed", "communication", input.NodeRef, "Could not poll node messages.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleNodeAgentAck(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "communication", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input communication.AckInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "communication", "body", "Request body is not valid node-agent ack JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "communication", "bootstrap", "Could not resolve request context.", err)
		return
	}
	result, err := s.services.Communication.Ack(ctx, req, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "communication.ack_failed", "communication", input.CommunicationMessageRef, "Could not acknowledge node message.", err)
		return
	}
	if err := backupcontracts.NewPreflightService(s.services.DB).ProjectAcknowledgement(ctx, result); err != nil {
		s.logger.Warn("protected-folder acknowledgement projection deferred",
			"communication_message_id", result.Message.CommunicationMessageID,
			"error", err)
	}
	if err := backupcontracts.NewReconcileService(s.services.DB).ProjectAcknowledgement(ctx, result); err != nil {
		s.logger.Warn("protected-folder reconciliation projection deferred",
			"communication_message_id", result.Message.CommunicationMessageID,
			"error", err)
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleNodeAgentProviderAdvertise(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "capabilities", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input capabilities.ProviderAdvertisementInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "capabilities", "body", "Request body is not valid provider advertisement JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "capabilities", "bootstrap", "Could not resolve request context.", err)
		return
	}
	advertisement, err := s.services.Capabilities.AdvertiseProvider(ctx, req, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "provider_advertisement.failed", "capabilities", input.Provider.ProviderKey, "Could not ingest provider advertisement.", err)
		return
	}
	response.WriteJSON(w, http.StatusCreated, response.Success(correlationID, advertisement))
}

func (s Server) handleNodeAgentCapabilityResult(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "routing", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input routing.RemoteResultInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "routing", "body", "Request body is not valid capability result JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "routing", "bootstrap", "Could not resolve request context.", err)
		return
	}
	result, err := s.services.Routing.IngestRemoteResult(ctx, req, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "capability_result.failed", "routing", input.Payload.CapabilityCallID, "Could not ingest remote capability result.", err)
		return
	}
	response.WriteJSON(w, http.StatusCreated, response.Success(correlationID, result))
}

func (s Server) handleNodeAgentSyncBatches(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "sync", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input loomsync.PushBatchInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "sync", "body", "Request body is not valid sync batch JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "sync", "bootstrap", "Could not resolve request context.", err)
		return
	}
	result, err := s.services.Sync.PushBatch(ctx, req, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "sync.batch_failed", "sync", input.NodeRef, "Could not ingest sync batch.", err)
		return
	}
	response.WriteJSON(w, http.StatusCreated, response.Success(correlationID, result))
}

func (s Server) handleNodeAgentSyncObjectUpload(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "sync", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input loomsync.SyncedObjectInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "sync", "body", "Request body is not valid synced object JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "sync", "bootstrap", "Could not resolve request context.", err)
		return
	}
	result, err := s.services.Sync.IngestSyncedObject(ctx, req, s.services.Objects, s.services.Search, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "sync.object_upload_failed", "sync", input.LocalObjectRef, "Could not ingest synced object.", err)
		return
	}
	response.WriteJSON(w, http.StatusCreated, response.Success(correlationID, result))
}

func (s Server) handleNodeAgentSyncPrivateBackup(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "sync", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input loomsync.PrivateBackupInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "sync", "body", "Request body is not valid private backup JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "sync", "bootstrap", "Could not resolve request context.", err)
		return
	}
	result, err := s.services.Sync.StorePrivateBackup(ctx, req, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "sync.private_backup_failed", "sync", input.NodeRef, "Could not store private backup.", err)
		return
	}
	response.WriteJSON(w, http.StatusCreated, response.Success(correlationID, result))
}

func (s Server) handleNodeAgentSyncDeletionRequest(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "sync", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input loomsync.DeletionRequestInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "sync", "body", "Request body is not valid deletion request JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "sync", "bootstrap", "Could not resolve request context.", err)
		return
	}
	result, err := s.services.Sync.CreateDeletionRequest(ctx, req, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "sync.deletion_request_failed", "sync", input.TargetRef, "Could not record deletion request.", err)
		return
	}
	response.WriteJSON(w, http.StatusCreated, response.Success(correlationID, result))
}

func (s Server) handleNodeAgentWatchedRootReport(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "watched_roots", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input mainwatchedroots.ReportInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "watched_roots", "body", "Request body is not valid watched-root report JSON.", err)
		return
	}
	result, err := s.services.WatchedRoots.Report(ctx, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "watched_roots.report_failed", "watched_roots", input.RootKey, "Could not record watched-root report.", err)
		return
	}
	response.WriteJSON(w, http.StatusCreated, response.Success(correlationID, result))
}

func (s Server) handleNodeAgentWatchedRootBackupBatch(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "watched_roots", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input mainwatchedroots.BackupBatchInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "watched_roots", "body", "Request body is not valid watched-root backup batch JSON.", err)
		return
	}
	result, err := s.services.WatchedRoots.RecordBackupBatch(ctx, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "watched_roots.backup_batch_failed", "watched_roots", input.RootKey, "Could not record watched-root backup batch.", err)
		return
	}
	response.WriteJSON(w, http.StatusCreated, response.Success(correlationID, result))
}

func (s Server) handleScopes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleScopeList(w, r)
	case http.MethodPost:
		s.handleScopeCreate(w, r)
	default:
		correlationID := correlation.Normalize(r.Header.Get(correlation.Header))
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "scopes", r.URL.Path, "Method is not allowed.", nil)
	}
}

func (s Server) handleScopeList(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "scopes", "limit", "Limit must be a positive integer.", err)
		return
	}

	scopes, err := s.services.Scopes.ListScopes(ctx, limit)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "scopes.list_failed", "scopes", "list", "Could not list scopes.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, scopes))
}

func (s Server) handleScopeCreate(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)

	var input scopes.CreateInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "scopes", "body", "Request body is not valid scope JSON.", err)
		return
	}

	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request_context.unavailable", "runtime", "bootstrap", "Could not resolve request context.", err)
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "scope.create", input)
	if !proceed {
		return
	}

	result, err := s.services.Scopes.CreateProjectScope(ctx, req, input)
	if err != nil {
		idemErr := loomerrors.Wrap("scope.create_failed", "scopes", input.Slug, "Could not create project scope.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, http.StatusBadRequest, "scope.create_failed", "scopes", input.Slug, "Could not create project scope.", err)
		return
	}

	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, result)
	s.completeIdempotency(ctx, idemRecord, "scope", result.Scope.ScopeID, envelope)
	response.WriteJSON(w, status, envelope)
}

func (s Server) handleScope(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "scopes", r.URL.Path, "Method is not allowed.", nil)
		return
	}

	ref := pathRef(r.URL.Path, "/v1/scopes/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "scope.ref_required", "scopes", "scope", "Scope reference is required.", nil)
		return
	}

	scope, err := s.services.Scopes.GetScope(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "scopes", ref, "Scope was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, scope))
}

func (s Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "events", r.URL.Path, "Method is not allowed.", nil)
		return
	}

	filter, err := s.eventFilter(ctx, r)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "events.invalid_filter", "events", "filter", "Event filter is invalid.", err)
		return
	}

	events, err := s.services.Events.ListEvents(ctx, filter)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "events.list_failed", "events", "list", "Could not list events.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, events))
}

func (s Server) handleEvent(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "events", r.URL.Path, "Method is not allowed.", nil)
		return
	}

	ref := pathRef(r.URL.Path, "/v1/events/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "event.ref_required", "events", "event", "Event reference is required.", nil)
		return
	}

	event, err := s.services.Events.GetEvent(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "events", ref, "Event was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, event))
}

func (s Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleProjectList(w, r)
	case http.MethodPost:
		s.handleProjectCreate(w, r)
	default:
		correlationID := correlation.Normalize(r.Header.Get(correlation.Header))
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "projects", r.URL.Path, "Method is not allowed.", nil)
	}
}

func (s Server) handleProjectList(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "projects", "limit", "Limit must be a positive integer.", err)
		return
	}

	projects, err := s.services.Projects.ListProjects(ctx, limit)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "projects.list_failed", "projects", "list", "Could not list projects.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, projects))
}

func (s Server) handleProjectCreate(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)

	var input projects.CreateInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "projects", "body", "Request body is not valid project JSON.", err)
		return
	}

	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request_context.unavailable", "runtime", "bootstrap", "Could not resolve request context.", err)
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "project.create", input)
	if !proceed {
		return
	}

	result, err := s.services.Projects.CreateProject(ctx, req, input)
	if err != nil {
		idemErr := loomerrors.Wrap("project.create_failed", "projects", input.Slug, "Could not create project.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, http.StatusBadRequest, "project.create_failed", "projects", input.Slug, "Could not create project.", err)
		return
	}

	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, result)
	s.completeIdempotency(ctx, idemRecord, "project", result.Project.Project.ProjectID, envelope)
	response.WriteJSON(w, status, envelope)
}

func (s Server) handleProjectScaffolds(w http.ResponseWriter, r *http.Request) {
	correlationID, _ := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "projects", r.URL.Path, "Method is not allowed.", nil)
		return
	}

	var input projectcontracts.ScaffoldOptions
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "projects", "body", "Request body is not valid project scaffold JSON.", err)
		return
	}

	normalized, err := s.normalizeProjectScaffoldOptions(input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "project.scaffold_invalid", "projects", input.Name, "Could not prepare project scaffold request.", err)
		return
	}
	result, err := projectcontracts.ScaffoldProject(normalized)
	if err != nil {
		if result.SourceState == "source_created" {
			s.writeProjectCreatePending(w, r, result, "project.scaffold_failed", "Project source was retained, but creation did not finish.")
			return
		}
		s.writeError(w, correlationID, http.StatusBadRequest, "project.scaffold_failed", "projects", normalized.Name, "Could not scaffold project.", err)
		return
	}
	if normalized.Mode == projectcontracts.ScaffoldModeDeclaration && !normalized.DryRun {
		if !s.connectCreatedProject(w, r, &result) {
			return
		}
	}
	status := http.StatusCreated
	if normalized.DryRun {
		status = http.StatusOK
	}
	response.WriteJSON(w, status, response.Success(correlationID, result))
}

func (s Server) normalizeProjectScaffoldOptions(input projectcontracts.ScaffoldOptions) (projectcontracts.ScaffoldOptions, error) {
	cfg := s.services.RuntimeConfig
	if strings.TrimSpace(input.Preset) == "" {
		input.Preset = projectcontracts.PresetMinimal
	}
	if strings.TrimSpace(input.OwnerNode) == "" {
		input.OwnerNode = firstNonEmptyHTTP(strings.TrimSpace(cfg.NodeID), "main")
	}
	if strings.TrimSpace(input.Directory) != "" {
		if input.DirectorySource == "" {
			input.DirectorySource = projectcontracts.ScaffoldDirectoryExplicit
		}
		return input, nil
	}

	boxRoot := strings.TrimSpace(firstNonEmptyHTTP(input.BoxRoot, cfg.BoxPath))
	if boxRoot == "" {
		return projectcontracts.ScaffoldOptions{}, fmt.Errorf("backend LOOM Box path is not configured; pass directory or set LOOM_BOX_PATH")
	}
	profile := strings.TrimSpace(firstNonEmptyHTTP(input.BoxProfile, cfg.BoxProfile))
	if profile == "" {
		if strings.EqualFold(strings.TrimSpace(cfg.NodeRole), box.ProfileMain) {
			profile = box.ProfileMain
		} else {
			profile = box.ProfileWorkspace
		}
	}
	resolved := box.Resolved{
		RootPath:         boxRoot,
		Profile:          profile,
		OwnerNode:        input.OwnerNode,
		NodeRole:         cfg.NodeRole,
		RuntimeStateRoot: cfg.BoxStateRoot,
	}
	status := box.Inspect(resolved)
	if status.State != "ok" {
		return projectcontracts.ScaffoldOptions{}, fmt.Errorf("backend LOOM Box is %s at %s", status.State, status.RootPath)
	}
	input.Directory = status.DefaultProjectPath
	input.DirectorySource = projectcontracts.ScaffoldDirectoryBox
	input.BoxDefaultUsed = true
	input.BoxRoot = status.RootPath
	input.BoxProfile = status.Profile
	input.BoxContractPath = status.ContractPath
	if strings.TrimSpace(status.OwnerNode) != "" {
		input.OwnerNode = status.OwnerNode
	}
	return input, nil
}

func (s Server) handleProjectFacetAdditions(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "projects", r.URL.Path, "Method is not allowed.", nil)
		return
	}

	var input projectcontracts.AddProjectFacetsOptions
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "projects", "body", "Request body is not valid project facet JSON.", err)
		return
	}

	normalized, err := s.normalizeProjectFacetAddOptions(ctx, input)
	if err != nil {
		if s.writeProjectRuntimeArchivedError(w, correlationID, "projects", firstNonEmptyHTTP(input.ProjectRef, input.ProjectRoot), err) {
			return
		}
		s.writeError(w, correlationID, http.StatusBadRequest, "project.facet_add_invalid", "projects", firstNonEmptyHTTP(input.ProjectRef, input.ProjectRoot), "Could not prepare project facet addition.", err)
		return
	}
	result, err := projectcontracts.AddProjectFacets(normalized)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "project.facet_add_failed", "projects", firstNonEmptyHTTP(normalized.ProjectRef, normalized.ProjectRoot), "Could not add project facets.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) normalizeProjectFacetAddOptions(ctx context.Context, input projectcontracts.AddProjectFacetsOptions) (projectcontracts.AddProjectFacetsOptions, error) {
	root, err := s.resolveBackendProjectRoot(ctx, input.ProjectRef, input.ProjectRoot)
	if err != nil {
		return projectcontracts.AddProjectFacetsOptions{}, err
	}
	input.ProjectRoot = root
	if err := s.ensureBackendProjectMutationAllowed(ctx, input.ProjectRef, input.ProjectRoot, "project_facet", strings.Join(input.Facets, ",")); err != nil {
		return projectcontracts.AddProjectFacetsOptions{}, err
	}
	input = s.withBackendProjectFacetBootstrap(ctx, input)
	return input, nil
}

func (s Server) withBackendProjectFacetBootstrap(ctx context.Context, input projectcontracts.AddProjectFacetsOptions) projectcontracts.AddProjectFacetsOptions {
	discovery, err := projectcontracts.InspectProjectLayout(input.ProjectRoot)
	if err != nil || discovery.CanonicalPresent || discovery.LegacyPresent {
		return input
	}
	slug := filepath.Base(input.ProjectRoot)
	name := slug
	ownerNode := firstNonEmptyHTTP(strings.TrimSpace(s.services.RuntimeConfig.NodeID), "main")
	if ref := strings.TrimSpace(input.ProjectRef); ref != "" && s.services.Projects.DB != nil {
		if detail, err := s.services.Projects.GetProjectRegistrationStatus(ctx, ref); err == nil {
			if value := strings.TrimSpace(detail.Project.Project.Slug); value != "" {
				slug = value
			}
			if value := strings.TrimSpace(detail.Project.Project.Name); value != "" {
				name = value
			}
			if detail.Project.Project.HomeNodeID != nil && s.services.Nodes.DB != nil {
				if node, err := s.services.Nodes.GetNode(ctx, *detail.Project.Project.HomeNodeID); err == nil && strings.TrimSpace(node.NodeKey) != "" {
					ownerNode = node.NodeKey
				}
			}
		}
	}
	input.BootstrapMissing = true
	input.BootstrapName = name
	input.BootstrapSlug = slug
	input.BootstrapOwnerNodeKey = ownerNode
	return input
}

func (s Server) handleProjectExports(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "projects", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input projectexport.Request
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "projects", "body", "Request body is not valid project export JSON.", err)
		return
	}
	mode, err := projectexport.ParseMode(string(input.Mode))
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "project.export_mode_invalid", "projects", string(input.Mode), "Project export mode is invalid.", err)
		return
	}
	root, err := s.resolveBackendProjectExportRoot(ctx, input.ProjectRef, input.ProjectRoot)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "project.export_target_invalid", "projects", firstNonEmptyHTTP(input.ProjectRef, input.ProjectRoot), "Could not resolve project export target.", err)
		return
	}
	maxBytes := input.MaxBytes
	if maxBytes <= 0 || maxBytes > projectexport.DefaultMaxBytes {
		maxBytes = projectexport.DefaultMaxBytes
	}
	plan, err := projectexport.PlanProject(ctx, root, projectexport.PlanOptions{
		Mode:                   mode,
		MaxBytes:               maxBytes,
		RegistrationReferences: s.projectExportRegistrationReferences(ctx, input.ProjectRef),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "project.export_plan_failed", "projects", firstNonEmptyHTTP(input.ProjectRef, input.ProjectRoot), "Could not plan project export.", err)
		return
	}
	temporary, err := os.CreateTemp("", "loom-project-export-*.tar")
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "project.export_temp_failed", "projects", plan.Summary.ProjectSlug, "Could not create bounded project export output.", err)
		return
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	result, err := projectexport.WriteArchive(ctx, temporary, plan)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "project.export_archive_failed", "projects", plan.Summary.ProjectSlug, "Could not create project export archive.", err)
		return
	}
	if err := temporary.Sync(); err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "project.export_archive_failed", "projects", plan.Summary.ProjectSlug, "Could not finalize project export archive.", err)
		return
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "project.export_archive_failed", "projects", plan.Summary.ProjectSlug, "Could not read project export archive.", err)
		return
	}
	summaryPayload, err := json.Marshal(result)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "project.export_summary_failed", "projects", plan.Summary.ProjectSlug, "Could not encode project export summary.", err)
		return
	}
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", plan.Summary.ProjectSlug+"-"+string(mode)+".tar"))
	w.Header().Set(projectexport.SummaryHeader, base64.RawURLEncoding.EncodeToString(summaryPayload))
	w.Header().Set(correlation.Header, correlationID)
	w.Header().Set("Content-Length", strconv.FormatInt(result.ArchiveBytes, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, temporary)
}

func (s Server) projectExportRegistrationReferences(ctx context.Context, ref string) projectexport.RegistrationReferences {
	refs := projectexport.RegistrationReferences{ProjectRef: strings.TrimSpace(ref)}
	if s.services.Projects.DB == nil || refs.ProjectRef == "" {
		return refs
	}
	detail, err := s.services.Projects.GetProjectRegistrationStatus(ctx, refs.ProjectRef)
	if err != nil {
		return refs
	}
	refs.ProjectID = detail.Project.Project.ProjectID
	refs.ProjectScopeID = detail.Project.Project.ProjectScopeID
	if detail.Project.Project.HomeNodeID != nil {
		refs.HomeNodeID = *detail.Project.Project.HomeNodeID
	}
	if detail.Registration != nil {
		refs.ContractRegistrationID = detail.Registration.ProjectContractRegistrationID
		refs.ContractHash = detail.Registration.ContractHash
	}
	for _, item := range detail.ScriptExposures {
		refs.RegistrationRefs = append(refs.RegistrationRefs, "script:"+item.ProjectScriptExposureID)
	}
	for _, item := range detail.ScheduleRegistrations {
		refs.RegistrationRefs = append(refs.RegistrationRefs, "schedule:"+item.ProjectScheduleRegistrationID)
	}
	for _, item := range detail.DirectEventRegistrations {
		refs.RegistrationRefs = append(refs.RegistrationRefs, "direct_event:"+item.ProjectDirectEventRegistrationID)
	}
	for _, item := range detail.WatchedRootRegistrations {
		refs.RegistrationRefs = append(refs.RegistrationRefs, "watched_root:"+item.ProjectWatchedRootRegistrationID)
	}
	for _, item := range detail.ConnectorRegistrations {
		refs.RegistrationRefs = append(refs.RegistrationRefs, "connector:"+item.ProjectConnectorRegistrationID)
	}
	for _, item := range detail.ModuleRegistrations {
		refs.RegistrationRefs = append(refs.RegistrationRefs, "module:"+item.ProjectModuleRegistrationID)
	}
	for _, item := range detail.WorkflowRegistrations {
		refs.RegistrationRefs = append(refs.RegistrationRefs, "workflow:"+item.ProjectWorkflowRegistrationID)
	}
	sort.Strings(refs.RegistrationRefs)
	return refs
}

func (s Server) handleProjectLayoutMigrations(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "projects", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input projectcontracts.LayoutMigrationOptions
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "projects", "body", "Request body is not valid project layout migration JSON.", err)
		return
	}
	root, err := s.resolveBackendProjectRoot(ctx, input.ProjectRef, input.ProjectRoot)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "project.layout_migration_invalid", "projects", firstNonEmptyHTTP(input.ProjectRef, input.ProjectRoot), "Could not resolve project layout migration target.", err)
		return
	}
	input.ProjectRoot = root
	if input.Apply {
		if err := s.ensureBackendProjectMutationAllowed(ctx, input.ProjectRef, input.ProjectRoot, "project_layout", "canonical"); err != nil {
			if s.writeProjectRuntimeArchivedError(w, correlationID, "projects", firstNonEmptyHTTP(input.ProjectRef, input.ProjectRoot), err) {
				return
			}
			s.writeError(w, correlationID, http.StatusBadRequest, "project.layout_migration_blocked", "projects", firstNonEmptyHTTP(input.ProjectRef, input.ProjectRoot), "Project layout migration is not allowed.", err)
			return
		}
	}
	result, err := projectcontracts.MigrateProjectLayout(input)
	if err != nil {
		if strings.Contains(err.Error(), "archived project") {
			s.writeError(w, correlationID, http.StatusConflict, "project_runtime.archived", "projects", firstNonEmptyHTTP(input.ProjectRef, input.ProjectRoot), "Project is archived; restore or reactivate it before migrating layout.", err)
			return
		}
		s.writeError(w, correlationID, http.StatusBadRequest, "project.layout_migration_failed", "projects", firstNonEmptyHTTP(input.ProjectRef, input.ProjectRoot), "Could not migrate project layout.", err)
		return
	}
	if ref := strings.TrimSpace(input.ProjectRef); ref != "" {
		result.NextActions = []string{
			"loom project validate " + ref + " --backend",
			"loom project diff " + ref + " --backend",
			"loom project register " + ref + " --backend",
		}
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) ensureBackendProjectMutationAllowed(ctx context.Context, projectRef, projectRoot, resourceKind, resourceRef string) error {
	if s.services.Projects.DB == nil {
		return nil
	}
	ref := strings.TrimSpace(projectRef)
	if ref == "" {
		root := strings.TrimSpace(projectRoot)
		if root != "" {
			ref = filepath.Base(root)
		}
	}
	if ref == "" {
		return nil
	}
	detail, err := s.services.Projects.GetProjectRegistrationStatus(ctx, ref)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("could not inspect project mutation state for %q: %w", ref, err)
	}
	return projects.EnsureProjectRegistrationMutable(detail, resourceKind, resourceRef)
}

func (s Server) resolveBackendProjectRoot(ctx context.Context, projectRef, projectRoot string) (string, error) {
	root, err := s.resolveBackendProjectRootCandidate(ctx, projectRef, projectRoot)
	if err != nil {
		return "", err
	}
	return s.normalizeBackendProjectRoot(root)
}

func (s Server) resolveBackendProjectExportRoot(ctx context.Context, projectRef, projectRoot string) (string, error) {
	root, err := s.resolveBackendProjectRootCandidate(ctx, projectRef, projectRoot)
	if err != nil {
		return "", err
	}
	return s.canonicalBackendProjectExportRoot(root)
}

func (s Server) resolveBackendProjectRootCandidate(ctx context.Context, projectRef, projectRoot string) (string, error) {
	if strings.TrimSpace(projectRoot) != "" {
		return projectRoot, nil
	}

	ref := strings.TrimSpace(projectRef)
	if ref == "" {
		return "", fmt.Errorf("project ref or project root is required")
	}
	if s.projectRegistrationStatus != nil {
		detail, err := s.projectRegistrationStatus(ctx, ref)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("could not inspect project registration for %q: %w", ref, err)
		}
		if err == nil && detail.Registration != nil && strings.TrimSpace(detail.Registration.ProjectRoot) != "" {
			return detail.Registration.ProjectRoot, nil
		}
	}
	if !projectcontracts.ValidProjectSlug(ref) {
		return "", fmt.Errorf("project ref %q is not registered and is not a valid project slug", ref)
	}
	parent, err := s.backendProjectParent()
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, ref), nil
}

func (s Server) canonicalBackendProjectExportRoot(root string) (string, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "." || !filepath.IsAbs(root) {
		return "", fmt.Errorf("backend project root must be an absolute path")
	}
	parent, err := s.backendProjectParent()
	if err != nil {
		return "", err
	}
	parent, err = filepath.Abs(parent)
	if err != nil {
		return "", fmt.Errorf("resolve backend Projects parent: %w", err)
	}
	canonicalParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("canonicalize backend Projects parent %q: %w", parent, err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve backend project root: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("inspect backend project root %q: %w", root, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("backend project root %q must not be a symlink", root)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("canonicalize backend project root %q: %w", root, err)
	}
	canonicalInfo, err := os.Stat(canonicalRoot)
	if err != nil {
		return "", fmt.Errorf("inspect canonical backend project root %q: %w", canonicalRoot, err)
	}
	if !canonicalInfo.IsDir() {
		return "", fmt.Errorf("backend project root %q is not a directory", canonicalRoot)
	}
	relative, err := filepath.Rel(canonicalParent, canonicalRoot)
	if err != nil {
		return "", fmt.Errorf("compare backend project root with Projects parent: %w", err)
	}
	if relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("canonical backend project root must be a strict descendant of %s", canonicalParent)
	}
	return canonicalRoot, nil
}

func (s Server) normalizeBackendProjectRoot(root string) (string, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "." || !filepath.IsAbs(root) {
		return "", fmt.Errorf("backend project root must be an absolute path")
	}
	parent, err := s.backendProjectParent()
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(parent, root)
	if err != nil {
		return "", err
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", fmt.Errorf("backend project root must be under %s", parent)
	}
	return root, nil
}

func (s Server) backendProjectParent() (string, error) {
	cfg := s.services.RuntimeConfig
	boxRoot := strings.TrimSpace(cfg.BoxPath)
	if boxRoot == "" {
		return "", fmt.Errorf("backend LOOM Box path is not configured")
	}
	profile := strings.TrimSpace(cfg.BoxProfile)
	if profile == "" {
		if strings.EqualFold(strings.TrimSpace(cfg.NodeRole), box.ProfileMain) {
			profile = box.ProfileMain
		} else {
			profile = box.ProfileWorkspace
		}
	}
	resolved := box.Resolved{
		RootPath:         boxRoot,
		Profile:          profile,
		OwnerNode:        firstNonEmptyHTTP(strings.TrimSpace(cfg.NodeID), "main"),
		NodeRole:         cfg.NodeRole,
		RuntimeStateRoot: cfg.BoxStateRoot,
	}
	status := box.Inspect(resolved)
	if status.State != "ok" {
		return "", fmt.Errorf("backend LOOM Box is %s at %s", status.State, status.RootPath)
	}
	if strings.TrimSpace(status.DefaultProjectPath) == "" {
		return "", fmt.Errorf("backend LOOM Box project path is not configured")
	}
	return filepath.Clean(status.DefaultProjectPath), nil
}

func (s Server) handleProjectContractAnalyses(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "projects", r.URL.Path, "Method is not allowed.", nil)
		return
	}

	var input projectcontracts.BackendAnalysisInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "projects", "body", "Request body is not valid project analysis JSON.", err)
		return
	}
	result, err := s.analyzeBackendProjectContractWithHealth(ctx, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "project.contract_analysis_failed", "projects", firstNonEmptyHTTP(input.ProjectRef, input.ProjectRoot), "Could not analyze backend project contract.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleProjectContractRegistrationsFromBackend(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "projects", r.URL.Path, "Method is not allowed.", nil)
		return
	}

	var input projects.RegisterProjectContractFromBackendInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "projects", "body", "Request body is not valid backend project contract registration JSON.", err)
		return
	}
	analysis, err := s.analyzeBackendProjectContract(ctx, projectcontracts.BackendAnalysisInput{ProjectRef: input.ProjectRef, ProjectRoot: input.ProjectRoot})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "project.contract_analysis_failed", "projects", firstNonEmptyHTTP(input.ProjectRef, input.ProjectRoot), "Could not analyze backend project contract.", err)
		return
	}
	if analysis.Report.Summary.Errors > 0 {
		s.writeError(w, correlationID, http.StatusBadRequest, "project.contract_invalid", "projects", firstNonEmptyHTTP(analysis.Plan.Project.Slug, input.ProjectRef, input.ProjectRoot), fmt.Sprintf("Project contract validation failed with %d error(s).", analysis.Report.Summary.Errors), nil)
		return
	}
	if input.Strict && analysis.Report.Summary.Warnings > 0 {
		s.writeError(w, correlationID, http.StatusBadRequest, "project.contract_invalid", "projects", firstNonEmptyHTTP(analysis.Plan.Project.Slug, input.ProjectRef, input.ProjectRoot), fmt.Sprintf("Project contract validation failed strict mode with %d warning(s).", analysis.Report.Summary.Warnings), nil)
		return
	}
	registrationInput, err := buildProjectContractRegistrationInputHTTP(analysis, "loom.project.register.backend")
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "project.contract_invalid", "projects", firstNonEmptyHTTP(input.ProjectRef, input.ProjectRoot), "Project contract could not be prepared for registration.", err)
		return
	}

	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request_context.unavailable", "runtime", "bootstrap", "Could not resolve request context.", err)
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "project.contract.register.backend", input)
	if !proceed {
		return
	}

	result, err := s.services.Projects.RegisterProjectContract(ctx, req, registrationInput)
	if err != nil {
		idemErr := loomerrors.Wrap("project.contract_register_failed", "projects", registrationInput.Project.Slug, "Could not register backend project contract.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, http.StatusBadRequest, "project.contract_register_failed", "projects", registrationInput.Project.Slug, "Could not register backend project contract.", err)
		return
	}

	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, result)
	resultRef := result.Detail.Project.Project.ProjectID
	if result.Detail.Registration != nil {
		resultRef = result.Detail.Registration.ProjectContractRegistrationID
	}
	s.completeIdempotency(ctx, idemRecord, "project_contract_registration", resultRef, envelope)
	response.WriteJSON(w, status, envelope)
}

func (s Server) analyzeBackendProjectContract(ctx context.Context, input projectcontracts.BackendAnalysisInput) (projectcontracts.Analysis, error) {
	result, err := s.analyzeBackendProjectContractWithHealth(ctx, input)
	if err != nil {
		return projectcontracts.Analysis{}, err
	}
	return result.Analysis, nil
}

func (s Server) analyzeBackendProjectContractWithHealth(ctx context.Context, input projectcontracts.BackendAnalysisInput) (projectdoctor.BackendAnalysisResult, error) {
	root, err := s.resolveBackendProjectRoot(ctx, input.ProjectRef, input.ProjectRoot)
	if err != nil {
		return projectdoctor.BackendAnalysisResult{}, err
	}
	analysis := projectcontracts.Analyze(root)
	var detail *projects.ProjectRegistrationDetail
	ref := firstNonEmptyHTTP(input.ProjectRef, analysis.Plan.Project.Slug, analysis.Report.Project.Slug)
	if ref != "" && s.services.Projects.DB != nil {
		registrationDetail, err := s.services.Projects.GetProjectRegistrationStatus(ctx, ref)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return projectdoctor.BackendAnalysisResult{}, fmt.Errorf("could not inspect project registration for %q: %w", ref, err)
		}
		if err == nil {
			detail = &registrationDetail
		}
	}
	return projectdoctor.BuildBackendAnalysisResult(analysis, detail), nil
}

func buildProjectContractRegistrationInputHTTP(analysis projectcontracts.Analysis, source string) (projects.RegisterProjectContractInput, error) {
	return projectregistration.BuildInput(analysis, source)
}

func firstNonEmptyHTTP(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (s Server) handleProjectContractRegistrations(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "projects", r.URL.Path, "Method is not allowed.", nil)
		return
	}

	var input projects.RegisterProjectContractInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "projects", "body", "Request body is not valid project contract registration JSON.", err)
		return
	}

	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request_context.unavailable", "runtime", "bootstrap", "Could not resolve request context.", err)
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "project.contract.register", input)
	if !proceed {
		return
	}

	result, err := s.services.Projects.RegisterProjectContract(ctx, req, input)
	if err != nil {
		idemErr := loomerrors.Wrap("project.contract_register_failed", "projects", input.Project.Slug, "Could not register project contract.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, http.StatusBadRequest, "project.contract_register_failed", "projects", input.Project.Slug, "Could not register project contract.", err)
		return
	}

	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, result)
	resultRef := result.Detail.Project.Project.ProjectID
	if result.Detail.Registration != nil {
		resultRef = result.Detail.Registration.ProjectContractRegistrationID
	}
	s.completeIdempotency(ctx, idemRecord, "project_contract_registration", resultRef, envelope)
	response.WriteJSON(w, status, envelope)
}

func (s Server) handleProjectContractRegistration(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "projects", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	ref := pathRef(r.URL.Path, "/v1/project-contract-registrations/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "project_contract_registration.ref_required", "projects", "project_contract_registration", "Project contract registration reference is required.", nil)
		return
	}
	detail, err := s.services.Projects.GetProjectRegistrationStatus(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "projects", ref, "Project registration was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, detail))
}

func (s Server) handleProject(w http.ResponseWriter, r *http.Request) {
	if ref, ok := projectDeclarationStatusRoute(r); ok {
		s.handleProjectDeclarationStatus(w, r, ref)
		return
	}
	if ref, kind, action, ok := projectPhysicalArchiveRoute(r.URL.Path); ok {
		s.handleProjectPhysicalArchive(w, r, ref, kind, action)
		return
	}
	correlationID, ctx := requestMeta(r)
	if projectRef, operation, repositoryRef, ok := projectRepositoryRoute(r); ok {
		s.handleProjectRepositories(w, r, projectRef, operation, repositoryRef)
		return
	}
	ref := pathRef(r.URL.Path, "/v1/projects/")
	if strings.HasSuffix(ref, "/watch-plan") {
		s.handleProjectWatchPlan(w, r, strings.Trim(strings.TrimSuffix(ref, "/watch-plan"), "/"))
		return
	}
	if strings.HasSuffix(ref, "/watch-policy/apply") {
		s.handleProjectWatchPolicyApply(w, r, strings.Trim(strings.TrimSuffix(ref, "/watch-policy/apply"), "/"))
		return
	}
	if strings.HasSuffix(ref, "/sync-status") {
		s.handleProjectSyncStatus(w, r, strings.Trim(strings.TrimSuffix(ref, "/sync-status"), "/"))
		return
	}
	if strings.HasSuffix(ref, "/backup-status") {
		s.handleProjectBackupStatus(w, r, strings.Trim(strings.TrimSuffix(ref, "/backup-status"), "/"))
		return
	}
	if strings.HasSuffix(ref, "/archive/inspect") {
		s.handleProjectArchiveInspect(w, r, strings.Trim(strings.TrimSuffix(ref, "/archive/inspect"), "/"))
		return
	}
	if strings.HasSuffix(ref, "/archive/restore") {
		s.handleProjectArchiveRestore(w, r, strings.Trim(strings.TrimSuffix(ref, "/archive/restore"), "/"))
		return
	}
	if strings.HasSuffix(ref, "/archive/migrate-runtime") {
		s.handleProjectArchiveRuntimeMigration(w, r, strings.Trim(strings.TrimSuffix(ref, "/archive/migrate-runtime"), "/"))
		return
	}
	if strings.HasSuffix(ref, "/archive") {
		s.handleProjectArchive(w, r, strings.Trim(strings.TrimSuffix(ref, "/archive"), "/"))
		return
	}
	if strings.HasSuffix(ref, "/activate") {
		projectRef := strings.Trim(strings.TrimSuffix(ref, "/activate"), "/")
		if projectRef == "" {
			s.writeError(w, correlationID, http.StatusNotFound, "project.ref_required", "projects", "project", "Project reference is required.", nil)
			return
		}
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "projects", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		var input projects.ActivateProjectInput
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "projects", "body", "Request body is not valid project activation JSON.", err)
			return
		}
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request_context.unavailable", "runtime", "bootstrap", "Could not resolve request context.", err)
			return
		}
		facet := strings.TrimSpace(input.Facet)
		idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "project.activate", map[string]string{"project_ref": projectRef, "facet": facet})
		if !proceed {
			return
		}
		detail, err := s.services.ProjectActivation.Activate(ctx, req, projectRef, input)
		if err != nil {
			if code, message := projectRuntimeGuardError(err); code != "" {
				idemErr := loomerrors.Wrap(code, "projects", projectRef, message, nil)
				s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
				s.writeProjectRuntimeArchivedError(w, correlationID, "projects", projectRef, err)
				return
			}
			if errors.Is(err, projects.ErrFacetActivationUnsupported) {
				idemErr := loomerrors.Wrap("project.facet_activation_unsupported", "projects", input.Facet, "Facet activation is implemented in later v0.3 slices.", err)
				s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
				s.writeError(w, correlationID, http.StatusBadRequest, "project.facet_activation_unsupported", "projects", input.Facet, "Facet activation is implemented in later v0.3 slices.", err)
				return
			}
			summary := "Could not activate project."
			if facet == "schedules" {
				summary = "Could not activate project schedule facet: " + err.Error()
			}
			if facet == "direct-events" || facet == "direct_events" {
				summary = "Could not activate project direct-events facet: " + err.Error()
			}
			if facet == "workflows" {
				summary = "Could not activate project workflows facet: " + err.Error()
			}
			idemErr := loomerrors.Wrap("project.activate_failed", "projects", projectRef, summary, err)
			s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
			s.writeError(w, correlationID, http.StatusBadRequest, "project.activate_failed", "projects", projectRef, summary, err)
			return
		}
		envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, detail)
		resultRef := detail.Project.Project.ProjectID
		if detail.Registration != nil {
			resultRef = detail.Registration.ProjectContractRegistrationID
		}
		s.completeIdempotency(ctx, idemRecord, "project_contract_registration", resultRef, envelope)
		response.WriteJSON(w, http.StatusOK, envelope)
		return
	}
	if strings.HasSuffix(ref, "/deactivate") {
		projectRef := strings.Trim(strings.TrimSuffix(ref, "/deactivate"), "/")
		if projectRef == "" {
			s.writeError(w, correlationID, http.StatusNotFound, "project.ref_required", "projects", "project", "Project reference is required.", nil)
			return
		}
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "projects", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		var input projects.DeactivateProjectInput
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "projects", "body", "Request body is not valid project deactivation JSON.", err)
			return
		}
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request_context.unavailable", "runtime", "bootstrap", "Could not resolve request context.", err)
			return
		}
		facet := strings.TrimSpace(input.Facet)
		idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "project.deactivate", map[string]string{"project_ref": projectRef, "facet": facet, "dry_run": fmt.Sprintf("%t", input.DryRun)})
		if !proceed {
			return
		}
		result, err := s.services.ProjectActivation.Deactivate(ctx, req, projectRef, input)
		if err != nil {
			if errors.Is(err, projects.ErrFacetActivationUnsupported) {
				idemErr := loomerrors.Wrap("project.facet_deactivation_unsupported", "projects", input.Facet, "Facet deactivation is not supported.", err)
				s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
				s.writeError(w, correlationID, http.StatusBadRequest, "project.facet_deactivation_unsupported", "projects", input.Facet, "Facet deactivation is not supported.", err)
				return
			}
			idemErr := loomerrors.Wrap("project.deactivate_failed", "projects", projectRef, "Could not deactivate project facet.", err)
			s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
			s.writeError(w, correlationID, http.StatusBadRequest, "project.deactivate_failed", "projects", projectRef, "Could not deactivate project facet.", err)
			return
		}
		envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, result)
		resultRef := result.Detail.Project.Project.ProjectID
		if result.Detail.Registration != nil {
			resultRef = result.Detail.Registration.ProjectContractRegistrationID
		}
		s.completeIdempotency(ctx, idemRecord, "project_contract_registration", resultRef, envelope)
		response.WriteJSON(w, http.StatusOK, envelope)
		return
	}

	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "projects", r.URL.Path, "Method is not allowed.", nil)
		return
	}

	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "project.ref_required", "projects", "project", "Project reference is required.", nil)
		return
	}

	project, err := s.services.Projects.GetProject(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "projects", ref, "Project was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, project))
}

func (s Server) handleProjectArchive(w http.ResponseWriter, r *http.Request, projectRef string) {
	correlationID, ctx := requestMeta(r)
	if projectRef == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "project.ref_required", "projects", "project", "Project reference is required.", nil)
		return
	}
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "projects", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input storagearchive.ProjectArchiveInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "projects", "body", "Request body is not valid project archive JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request_context.unavailable", "runtime", "bootstrap", "Could not resolve request context.", err)
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "project.archive", map[string]string{
		"project_ref": projectRef,
		"source_ref":  input.SourceRef,
		"target_path": input.TargetPath,
		"dry_run":     fmt.Sprintf("%t", input.DryRun),
	})
	if !proceed {
		return
	}
	result, err := s.services.ProjectArchive.ArchiveProject(ctx, req, projectRef, input)
	if err != nil {
		idemErr := loomerrors.Wrap("project.archive_failed", "projects", projectRef, "Could not archive project.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, http.StatusBadRequest, "project.archive_failed", "projects", projectRef, "Could not archive project.", err)
		return
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, result)
	resultRef := result.Project.Project.Project.ProjectID
	if result.RuntimeManifest.ProjectRuntimeArchiveID != "" {
		resultRef = result.RuntimeManifest.ProjectRuntimeArchiveID
	}
	s.completeIdempotency(ctx, idemRecord, "project_runtime_archive", resultRef, envelope)
	response.WriteJSON(w, http.StatusOK, envelope)
}

func (s Server) handleProjectArchiveInspect(w http.ResponseWriter, r *http.Request, projectRef string) {
	correlationID, ctx := requestMeta(r)
	if projectRef == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "project.ref_required", "projects", "project", "Project reference is required.", nil)
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "projects", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	result, err := s.services.ProjectArchive.InspectProjectArchive(ctx, projectRef)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "project.archive_inspect_failed", "projects", projectRef, "Could not inspect project archive.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleProjectArchiveRestore(w http.ResponseWriter, r *http.Request, projectRef string) {
	correlationID, ctx := requestMeta(r)
	if projectRef == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "project.ref_required", "projects", "project", "Project reference is required.", nil)
		return
	}
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "projects", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input storagearchive.ProjectArchiveRestoreInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "projects", "body", "Request body is not valid project archive restore JSON.", err)
		return
	}
	input.DryRun = true
	result, err := s.services.ProjectArchive.PlanProjectArchiveRestore(ctx, projectRef, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "project.archive_restore_failed", "projects", projectRef, "Could not plan project archive restore.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleProjectArchiveRuntimeMigration(w http.ResponseWriter, r *http.Request, projectRef string) {
	correlationID, ctx := requestMeta(r)
	if projectRef == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "project.ref_required", "projects", "project", "Project reference is required.", nil)
		return
	}
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "projects", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input storagearchive.ProjectRuntimeMigrationInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "projects", "body", "Request body is not valid project runtime migration JSON.", err)
		return
	}
	input.DryRun = true
	result, err := s.services.ProjectArchive.PlanProjectRuntimeMigration(ctx, projectRef, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "project.runtime_migration_failed", "projects", projectRef, "Could not plan project runtime migration.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleProjectWatchPlan(w http.ResponseWriter, r *http.Request, projectRef string) {
	correlationID, ctx := requestMeta(r)
	if projectRef == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "project.ref_required", "projects", "project", "Project reference is required.", nil)
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "projects", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	query := r.URL.Query()
	plan, err := s.services.ProjectWatch.BuildPlan(ctx, projectRef, projectwatch.BuildPlanInput{
		ProjectRoot:           query.Get("project_root"),
		UseRegisteredSnapshot: parseBoolQuery(query, "use_registered_snapshot"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "project.watch_plan_failed", "projects", projectRef, "Could not build project watch plan.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, plan))
}

func (s Server) handleProjectWatchPolicyApply(w http.ResponseWriter, r *http.Request, projectRef string) {
	correlationID, ctx := requestMeta(r)
	if projectRef == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "project.ref_required", "projects", "project", "Project reference is required.", nil)
		return
	}
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "projects", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input projects.ApplyProjectWatchPolicyInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "projects", "body", "Request body is not valid project watch policy JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request_context.unavailable", "runtime", "bootstrap", "Could not resolve request context.", err)
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "project.watch_policy.apply", map[string]any{"project_ref": projectRef, "input": input})
	if !proceed {
		return
	}
	result, err := s.services.ProjectWatch.ApplyDesiredState(ctx, req, projectRef, input)
	if err != nil {
		idemErr := loomerrors.Wrap("project.watch_policy_apply_failed", "projects", projectRef, "Could not apply project watch policy.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, http.StatusBadRequest, "project.watch_policy_apply_failed", "projects", projectRef, "Could not apply project watch policy.", err)
		return
	}
	resultRef := projectRef
	if result.Detail.Registration != nil {
		resultRef = result.Detail.Registration.ProjectContractRegistrationID
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, result)
	s.completeIdempotency(ctx, idemRecord, "project_watch_policy", resultRef, envelope)
	response.WriteJSON(w, http.StatusOK, envelope)
}

func (s Server) handleProjectSyncStatus(w http.ResponseWriter, r *http.Request, projectRef string) {
	correlationID, ctx := requestMeta(r)
	if projectRef == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "project.ref_required", "projects", "project", "Project reference is required.", nil)
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "projects", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	status, err := s.services.ProjectWatch.SyncStatus(ctx, projectRef)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "project.sync_status_failed", "projects", projectRef, "Could not read project sync status.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, status))
}

func (s Server) handleProjectBackupStatus(w http.ResponseWriter, r *http.Request, projectRef string) {
	correlationID, ctx := requestMeta(r)
	if projectRef == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "project.ref_required", "projects", "project", "Project reference is required.", nil)
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "projects", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	status, err := s.services.ProjectWatch.BackupStatus(ctx, projectRef)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "project.backup_status_failed", "projects", projectRef, "Could not read project backup status.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, status))
}

func (s Server) handleObjects(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "objects", r.URL.Path, "Method is not allowed.", nil)
		return
	}

	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "objects", "limit", "Limit must be a positive integer.", err)
		return
	}

	query := r.URL.Query()
	objects, err := s.services.Objects.ListObjects(ctx, objects.ListFilter{
		Limit:      limit,
		ProjectRef: query.Get("project"),
		ScopeRef:   query.Get("scope"),
		ObjectType: query.Get("type"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "objects.list_failed", "objects", "list", "Could not list objects.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, objects))
}

func (s Server) handleObjectIngest(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "objects", r.URL.Path, "Method is not allowed.", nil)
		return
	}

	var input objects.IngestFileInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "objects", "body", "Request body is not valid object ingest JSON.", err)
		return
	}

	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request_context.unavailable", "runtime", "bootstrap", "Could not resolve request context.", err)
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "object.ingest", objectIngestIdempotencyRequest(input))
	if !proceed {
		return
	}

	result, err := s.services.Objects.IngestFile(ctx, req, input)
	if err != nil {
		idemErr := loomerrors.Wrap("object.ingest_failed", "objects", input.Path, "Could not ingest object.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, http.StatusBadRequest, "object.ingest_failed", "objects", input.Path, "Could not ingest object.", err)
		return
	}
	if result.Object.LatestVersion != nil {
		indexResult, err := s.services.Search.EnqueueObjectVersion(ctx, req, search.IndexEnqueueInput{
			ObjectID:        result.Object.Object.ObjectID,
			ObjectVersionID: result.Object.LatestVersion.ObjectVersionID,
		})
		if err != nil {
			s.logger.Warn("object ingest index enqueue failed",
				slog.String("component", "search"),
				slog.String("correlation_id", correlationID),
				slog.String("object_id", result.Object.Object.ObjectID),
				slog.String("object_version_id", result.Object.LatestVersion.ObjectVersionID),
				slog.String("error", err.Error()),
			)
		} else {
			s.logger.Info("object ingest index queued",
				slog.String("component", "search"),
				slog.String("correlation_id", correlationID),
				slog.String("object_id", indexResult.ObjectID),
				slog.String("object_version_id", indexResult.ObjectVersionID),
				slog.String("status", indexResult.Status),
			)
		}
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, result)
	s.completeIdempotency(ctx, idemRecord, "object", result.Object.Object.ObjectID, envelope)
	response.WriteJSON(w, http.StatusCreated, envelope)
}

func (s Server) handleObject(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "objects", r.URL.Path, "Method is not allowed.", nil)
		return
	}

	ref := pathRef(r.URL.Path, "/v1/objects/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "object.ref_required", "objects", "object", "Object reference is required.", nil)
		return
	}

	if strings.HasSuffix(ref, "/versions") {
		objectRef := strings.TrimSuffix(ref, "/versions")
		objectRef = strings.Trim(objectRef, "/")
		versions, err := s.services.Objects.ListObjectVersions(ctx, objectRef)
		if err != nil {
			s.writeLookupError(w, correlationID, "objects", objectRef, "Object versions were not found.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, versions))
		return
	}

	object, err := s.services.Objects.GetObject(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "objects", ref, "Object was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, object))
}

func (s Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "search", r.URL.Path, "Method is not allowed.", nil)
		return
	}

	var input search.SearchInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "search", "body", "Request body is not valid search JSON.", err)
		return
	}

	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request_context.unavailable", "runtime", "bootstrap", "Could not resolve request context.", err)
		return
	}

	results, err := s.services.Search.Search(ctx, req, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "search.failed", "search", input.Query, "Could not run search.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, results))
}

func (s Server) handleIndexStatus(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "search", r.URL.Path, "Method is not allowed.", nil)
		return
	}

	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "search", "limit", "Limit must be a positive integer.", err)
		return
	}

	query := r.URL.Query()
	statuses, err := s.services.Search.ListIndexStatus(ctx, search.StatusFilter{
		Limit:      limit,
		ObjectRef:  query.Get("object"),
		ProjectRef: query.Get("project"),
		ScopeRef:   query.Get("scope"),
		IndexType:  query.Get("index_type"),
		Status:     query.Get("status"),
		FailedOnly: parseBoolQuery(query, "failed") || parseBoolQuery(query, "failed_only"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "search.index_status_failed", "search", "index_status", "Could not list index status records.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, statuses))
}

func (s Server) handleIndexQueue(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "search", r.URL.Path, "Method is not allowed.", nil)
		return
	}

	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "search", "limit", "Limit must be a positive integer.", err)
		return
	}

	query := r.URL.Query()
	statuses, err := s.services.Search.ListIndexQueue(ctx, search.IndexQueueFilter{
		Limit:         limit,
		ObjectRef:     query.Get("object"),
		ProjectRef:    query.Get("project"),
		ScopeRef:      query.Get("scope"),
		Status:        query.Get("status"),
		IncludeActive: parseBoolQuery(query, "active") || parseBoolQuery(query, "include_active"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "search.index_queue_failed", "search", "index_queue", "Could not list index queue records.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, statuses))
}

func (s Server) handleIndexQueueItem(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "search", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	indexStatusID := strings.Trim(pathRef(r.URL.Path, "/v1/indexes/queue/"), "/")
	if strings.TrimSpace(indexStatusID) == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "search.index_status_required", "search", "index_status", "Index status id is required.", nil)
		return
	}
	status, err := s.services.Search.GetIndexStatus(ctx, indexStatusID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusNotFound, "search.index_status_not_found", "search", indexStatusID, "Index status was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, status))
}

func (s Server) handleIndexFailures(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "search", r.URL.Path, "Method is not allowed.", nil)
		return
	}

	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "search", "limit", "Limit must be a positive integer.", err)
		return
	}

	query := r.URL.Query()
	statuses, err := s.services.Search.ListIndexFailures(ctx, search.StatusFilter{
		Limit:      limit,
		ObjectRef:  query.Get("object"),
		ProjectRef: query.Get("project"),
		ScopeRef:   query.Get("scope"),
		IndexType:  query.Get("index_type"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "search.index_failures_failed", "search", "index_failures", "Could not list index failures.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, statuses))
}

func (s Server) handleIndexRetry(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "search", r.URL.Path, "Method is not allowed.", nil)
		return
	}

	var input search.IndexStatusRefInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "search", "body", "Request body is not valid index retry JSON.", err)
		return
	}

	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request_context.unavailable", "runtime", "bootstrap", "Could not resolve request context.", err)
		return
	}
	status, err := s.services.Search.RetryIndexWork(ctx, req, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "search.index_retry_failed", "search", input.IndexStatusID, "Could not retry index work.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, status))
}

func (s Server) handleIndexRetryFailed(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "search", r.URL.Path, "Method is not allowed.", nil)
		return
	}

	var input search.IndexRetryFailedInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "search", "body", "Request body is not valid retry-failed JSON.", err)
		return
	}

	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request_context.unavailable", "runtime", "bootstrap", "Could not resolve request context.", err)
		return
	}
	summary, err := s.services.Search.RetryFailedIndexWork(ctx, req, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "search.index_retry_failed_failed", "search", "retry_failed", "Could not retry failed index work.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, summary))
}

func (s Server) handleIndexExplain(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "search", r.URL.Path, "Method is not allowed.", nil)
		return
	}

	objectRef := r.URL.Query().Get("object")
	if strings.TrimSpace(objectRef) == "" {
		s.writeError(w, correlationID, http.StatusBadRequest, "search.object_required", "search", "object", "Object reference is required.", nil)
		return
	}
	result, err := s.services.Search.ExplainObjectIndex(ctx, search.IndexExplainInput{ObjectRef: objectRef})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "search.index_explain_failed", "search", objectRef, "Could not explain object index state.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleIndexRebuild(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "search", r.URL.Path, "Method is not allowed.", nil)
		return
	}

	var input search.RebuildInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "search", "body", "Request body is not valid rebuild JSON.", err)
		return
	}

	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request_context.unavailable", "runtime", "bootstrap", "Could not resolve request context.", err)
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "index.rebuild", input)
	if !proceed {
		return
	}

	result, err := s.services.Search.RebuildObject(ctx, req, input.ObjectRef)
	if err != nil {
		idemErr := loomerrors.Wrap("search.rebuild_failed", "search", input.ObjectRef, "Could not rebuild index.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, http.StatusBadRequest, "search.rebuild_failed", "search", input.ObjectRef, "Could not rebuild index.", err)
		return
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, result)
	s.completeIdempotency(ctx, idemRecord, "object", result.ObjectID, envelope)
	response.WriteJSON(w, http.StatusOK, envelope)
}

func (s Server) handleScriptRegister(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "scripts", r.URL.Path, "Method is not allowed.", nil)
		return
	}

	var input scripts.RegisterInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "scripts", "body", "Request body is not valid script registration JSON.", err)
		return
	}

	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request_context.unavailable", "runtime", "bootstrap", "Could not resolve request context.", err)
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "script.register", input)
	if !proceed {
		return
	}
	result, err := s.services.Scripts.RegisterScript(ctx, req, input)
	if err != nil {
		idemErr := loomerrors.Wrap("script.register_failed", "scripts", input.ManifestPath, "Could not register script.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, http.StatusBadRequest, "script.register_failed", "scripts", input.ManifestPath, "Could not register script.", err)
		return
	}
	status := http.StatusOK
	if result.ScriptCreated || result.VersionCreated {
		status = http.StatusCreated
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, result)
	s.completeIdempotency(ctx, idemRecord, "script", result.Script.ScriptID, envelope)
	response.WriteJSON(w, status, envelope)
}

func (s Server) handleScripts(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "scripts", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "scripts", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	scripts, err := s.services.Scripts.ListScripts(ctx, scripts.ListFilter{
		Limit:      limit,
		Status:     query.Get("status"),
		ProjectRef: query.Get("project"),
		ScopeRef:   query.Get("scope"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "scripts.list_failed", "scripts", "list", "Could not list scripts.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, scripts))
}

func (s Server) handleScript(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	ref := pathRef(r.URL.Path, "/v1/scripts/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "script.ref_required", "scripts", "script", "Script reference is required.", nil)
		return
	}

	if strings.HasSuffix(ref, "/run") {
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "scripts", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		scriptRef := strings.Trim(strings.TrimSuffix(ref, "/run"), "/")
		var input jobs.CreateScriptRunInput
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "jobs", "body", "Request body is not valid script run JSON.", err)
			return
		}
		input.ScriptRef = scriptRef

		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request_context.unavailable", "runtime", "bootstrap", "Could not resolve request context.", err)
			return
		}
		idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "script.run", input)
		if !proceed {
			return
		}
		mode := jobs.NormalizeScriptRunMode(strings.TrimSpace(input.ExecutionMode))
		if mode == "" {
			err := fmt.Errorf("execution_mode must be one of %s, %s, or %s", jobs.ScriptRunEnqueueOnly, jobs.ScriptRunWaitUntilStarted, jobs.ScriptRunWaitForCompletion)
			idemErr := loomerrors.Wrap("job.invalid_execution_mode", "jobs", scriptRef, "Script run execution mode is invalid.", err)
			s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
			s.writeError(w, correlationID, http.StatusBadRequest, "job.invalid_execution_mode", "jobs", scriptRef, "Script run execution mode is invalid.", err)
			return
		}
		input.ExecutionMode = mode
		job, err := s.services.Jobs.CreateScriptRun(ctx, req, input)
		if err != nil {
			idemErr := loomerrors.Wrap("job.create_failed", "jobs", scriptRef, "Could not create script run job.", err)
			s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
			s.writeError(w, correlationID, http.StatusBadRequest, "job.create_failed", "jobs", scriptRef, "Could not create script run job.", err)
			return
		}
		waitResult := jobs.WaitResultQueued
		if mode == jobs.ScriptRunWaitUntilStarted {
			_, waitResult, err = s.services.Jobs.WaitForJobStatus(ctx, job.JobID, []string{jobs.StatusRunning}, jobs.WaitOptions{Timeout: scriptRunWaitTimeout(input.WaitTimeoutSecs)})
		}
		if mode == jobs.ScriptRunWaitForCompletion {
			_, waitResult, err = s.services.Jobs.WaitForJobTerminal(ctx, job.JobID, jobs.WaitOptions{Timeout: scriptRunWaitTimeout(input.WaitTimeoutSecs)})
		}
		if err != nil {
			idemErr := loomerrors.Wrap("job.wait_failed", "jobs", job.JobID, "Could not wait for script job.", err)
			s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
			s.writeError(w, correlationID, http.StatusInternalServerError, "job.wait_failed", "jobs", job.JobID, "Could not wait for script job.", err)
			return
		}
		result, err := s.services.Jobs.ScriptRunResult(ctx, job.JobID, mode, waitResult)
		if err != nil {
			idemErr := loomerrors.Wrap("job.inspect_failed", "jobs", job.JobID, "Could not inspect script job.", err)
			s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
			s.writeError(w, correlationID, http.StatusInternalServerError, "job.inspect_failed", "jobs", job.JobID, "Could not inspect script job.", err)
			return
		}
		envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, result)
		s.completeIdempotency(ctx, idemRecord, "job", result.Job.Job.JobID, envelope)
		response.WriteJSON(w, http.StatusCreated, envelope)
		return
	}

	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "scripts", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	detail, err := s.services.Scripts.GetScript(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "scripts", ref, "Script was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, detail))
}

func scriptRunWaitTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		return 120 * time.Second
	}
	if seconds > 24*60*60 {
		seconds = 24 * 60 * 60
	}
	return time.Duration(seconds) * time.Second
}

func (s Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "jobs", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	filter, ok := s.jobListFilterFromRequest(w, r, correlationID, 50)
	if !ok {
		return
	}
	jobs, err := s.services.Jobs.ListJobs(ctx, filter)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "jobs.list_failed", "jobs", "list", "Could not list jobs.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, jobs))
}

func (s Server) handleJob(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	ref := pathRef(r.URL.Path, "/v1/jobs/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "job.ref_required", "jobs", "job", "Job reference is required.", nil)
		return
	}
	if ref == "queue" {
		s.handleJobQueue(w, r, correlationID, ctx)
		return
	}
	if ref == "failures" {
		s.handleJobFailures(w, r, correlationID, ctx)
		return
	}
	if ref == "status" {
		s.handleJobStatus(w, r, correlationID, ctx)
		return
	}
	if ref == "run-next" {
		s.handleJobRunNext(w, r, correlationID, ctx)
		return
	}
	if strings.HasSuffix(ref, "/acknowledge") {
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "jobs", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		jobRef := strings.Trim(strings.TrimSuffix(ref, "/acknowledge"), "/")
		input, ok := decodeJobAttentionInput(w, r, s, correlationID, jobRef, "acknowledge")
		if !ok {
			return
		}
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request_context.unavailable", "runtime", "bootstrap", "Could not resolve request context.", err)
			return
		}
		job, err := s.services.Jobs.AcknowledgeJobAttention(ctx, req, input)
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "job.attention_acknowledge_failed", "jobs", jobRef, "Could not acknowledge job attention.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, job))
		return
	}
	if strings.HasSuffix(ref, "/archive") {
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "jobs", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		jobRef := strings.Trim(strings.TrimSuffix(ref, "/archive"), "/")
		input, ok := decodeJobAttentionInput(w, r, s, correlationID, jobRef, "archive")
		if !ok {
			return
		}
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request_context.unavailable", "runtime", "bootstrap", "Could not resolve request context.", err)
			return
		}
		job, err := s.services.Jobs.ArchiveJobAttention(ctx, req, input)
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "job.attention_archive_failed", "jobs", jobRef, "Could not archive job attention.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, job))
		return
	}
	if strings.HasSuffix(ref, "/cancel") {
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "jobs", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		jobRef := strings.Trim(strings.TrimSuffix(ref, "/cancel"), "/")
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request_context.unavailable", "runtime", "bootstrap", "Could not resolve request context.", err)
			return
		}
		job, err := s.services.Jobs.CancelJob(ctx, req, jobs.CancelJobInput{JobRef: jobRef})
		if err != nil {
			if s.writeProjectRuntimeArchivedError(w, correlationID, "jobs", jobRef, err) {
				return
			}
			s.writeError(w, correlationID, http.StatusBadRequest, "job.cancel_failed", "jobs", jobRef, "Could not cancel job.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, job))
		return
	}
	if strings.HasSuffix(ref, "/retry") {
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "jobs", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		jobRef := strings.Trim(strings.TrimSuffix(ref, "/retry"), "/")
		var input jobs.RetryJobInput
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "jobs", "body", "Request body is not valid job retry JSON.", err)
			return
		}
		input.JobRef = jobRef
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request_context.unavailable", "runtime", "bootstrap", "Could not resolve request context.", err)
			return
		}
		job, err := s.services.Jobs.RetryJob(ctx, req, input)
		if err != nil {
			if s.writeProjectRuntimeArchivedError(w, correlationID, "jobs", jobRef, err) {
				return
			}
			s.writeError(w, correlationID, http.StatusBadRequest, "job.retry_failed", "jobs", jobRef, "Could not retry job.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, job))
		return
	}
	if strings.HasSuffix(ref, "/logs") {
		if r.Method != http.MethodGet {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "jobs", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		jobRef := strings.Trim(strings.TrimSuffix(ref, "/logs"), "/")
		logs, err := s.services.Jobs.ListJobLogs(ctx, jobRef)
		if err != nil {
			s.writeLookupError(w, correlationID, "jobs", jobRef, "Job logs were not found.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, logs))
		return
	}
	if strings.HasSuffix(ref, "/outputs") {
		if r.Method != http.MethodGet {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "jobs", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		jobRef := strings.Trim(strings.TrimSuffix(ref, "/outputs"), "/")
		outputs, err := s.services.Jobs.ListJobOutputs(ctx, jobRef)
		if err != nil {
			s.writeLookupError(w, correlationID, "jobs", jobRef, "Job outputs were not found.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, outputs))
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "jobs", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	detail, err := s.services.Jobs.GetJob(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "jobs", ref, "Job was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, detail))
}

func (s Server) handleJobQueue(w http.ResponseWriter, r *http.Request, correlationID string, ctx context.Context) {
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "jobs", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	filter, ok := s.jobListFilterFromRequest(w, r, correlationID, 50)
	if !ok {
		return
	}
	jobs, err := s.services.Jobs.ListQueuedJobs(ctx, filter)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "jobs.queue_failed", "jobs", "queue", "Could not list queued jobs.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, jobs))
}

func (s Server) handleJobFailures(w http.ResponseWriter, r *http.Request, correlationID string, ctx context.Context) {
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "jobs", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	filter, ok := s.jobListFilterFromRequest(w, r, correlationID, 50)
	if !ok {
		return
	}
	jobs, err := s.services.Jobs.ListFailedJobs(ctx, filter)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "jobs.failures_failed", "jobs", "failures", "Could not list failed jobs.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, jobs))
}

func (s Server) handleJobStatus(w http.ResponseWriter, r *http.Request, correlationID string, ctx context.Context) {
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "jobs", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	status, err := s.services.Jobs.JobQueueSummary(ctx)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "jobs.status_failed", "jobs", "status", "Could not summarize job queue.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, status))
}

func (s Server) handleJobRunNext(w http.ResponseWriter, r *http.Request, correlationID string, ctx context.Context) {
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "jobs", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input workers.RunOnceInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "jobs", "body", "Request body is not valid job runner JSON.", err)
		return
	}
	if input.IdempotencyKey == "" {
		input.IdempotencyKey = strings.TrimSpace(r.Header.Get(idempotency.Header))
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "jobs", "bootstrap", "Could not resolve request context.", err)
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "jobs.run_next", input)
	if !proceed {
		return
	}
	result, err := s.services.Workers.RunOnce(ctx, req, "main.job_runner", input)
	if err != nil && result.Run.WorkerRunID == "" {
		idemErr := loomerrors.Wrap("jobs.run_next_failed", "jobs", "main.job_runner", "Could not run next queued job.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, http.StatusBadRequest, "jobs.run_next_failed", "jobs", "main.job_runner", "Could not run next queued job.", err)
		return
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, result)
	s.completeIdempotency(ctx, idemRecord, "worker_run", result.Run.WorkerRunID, envelope)
	response.WriteJSON(w, http.StatusOK, envelope)
}

func (s Server) jobListFilterFromRequest(w http.ResponseWriter, r *http.Request, correlationID string, defaultLimit int) (jobs.ListFilter, bool) {
	limit, err := parseLimit(r, defaultLimit)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "jobs", "limit", "Limit must be a positive integer.", err)
		return jobs.ListFilter{}, false
	}
	query := r.URL.Query()
	return jobs.ListFilter{
		Limit:               limit,
		Status:              query.Get("status"),
		JobType:             query.Get("type"),
		ScriptRef:           query.Get("script"),
		WorkflowRef:         query.Get("workflow"),
		ProjectRef:          query.Get("project"),
		ScopeRef:            query.Get("scope"),
		ManualOnly:          parseBoolQuery(query, "manual"),
		AttentionStatus:     query.Get("attention_status"),
		IncludeAcknowledged: parseBoolQuery(query, "include_acknowledged"),
		IncludeArchived:     parseBoolQuery(query, "include_archived"),
	}, true
}

func decodeJobAttentionInput(w http.ResponseWriter, r *http.Request, s Server, correlationID, jobRef, action string) (jobs.JobAttentionInput, bool) {
	var input jobs.JobAttentionInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "jobs", "body", "Request body is not valid job attention "+action+" JSON.", err)
		return jobs.JobAttentionInput{}, false
	}
	input.JobRef = jobRef
	return input, true
}

func (s Server) handleRunners(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "runners", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "runners", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	runners, err := s.services.Jobs.ListRunners(ctx, jobs.RunnerFilter{
		Limit:  limit,
		Status: query.Get("status"),
		NodeID: query.Get("node"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "runners.list_failed", "runners", "list", "Could not list runners.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, runners))
}

func (s Server) handleRunnersStatus(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "runners", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	status, err := s.services.Jobs.JobQueueSummary(ctx)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "runners.status_failed", "runners", "status", "Could not summarize runners.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, status))
}

func (s Server) handleRunner(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	ref := pathRef(r.URL.Path, "/v1/runners/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "runner.ref_required", "runners", "runner", "Runner reference is required.", nil)
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "runners", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	runner, err := s.services.Jobs.GetRunner(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "runners", ref, "Runner was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, runner))
}

func (s Server) handleArtifacts(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "artifacts", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "artifacts", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	artifacts, err := s.services.Artifacts.ListArtifacts(ctx, artifacts.ListFilter{
		Limit:     limit,
		JobRef:    query.Get("job"),
		ScopeRef:  query.Get("scope"),
		ObjectRef: query.Get("object"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "artifacts.list_failed", "artifacts", "list", "Could not list artifacts.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, artifacts))
}

func (s Server) handleArtifact(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "artifacts", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	ref := pathRef(r.URL.Path, "/v1/artifacts/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "artifact.ref_required", "artifacts", "artifact", "Artifact reference is required.", nil)
		return
	}
	detail, err := s.services.Artifacts.GetArtifact(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "artifacts", ref, "Artifact was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, detail))
}

func (s Server) handleProviderAdvertisements(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "provider_advertisements", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "provider_advertisements", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	advertisements, err := s.services.Capabilities.ListProviderAdvertisements(ctx, capabilities.ProviderAdvertisementFilter{
		Limit:       limit,
		NodeRef:     query.Get("node"),
		ProviderRef: query.Get("provider"),
		Status:      query.Get("status"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "provider_advertisements.list_failed", "capabilities", "provider-advertisements", "Could not list provider advertisements.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, advertisements))
}

func (s Server) handleProviderAdvertisement(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	ref := pathRef(r.URL.Path, "/v1/provider-advertisements/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "provider_advertisement.ref_required", "capabilities", "provider_advertisement", "Provider advertisement reference is required.", nil)
		return
	}
	if strings.HasSuffix(ref, "/approve") {
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "provider_advertisements", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		adRef := strings.Trim(strings.TrimSuffix(ref, "/approve"), "/")
		var input capabilities.ApproveProviderAdvertisementInput
		if r.Body != nil {
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
				s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "capabilities", "body", "Request body is not valid approval JSON.", err)
				return
			}
		}
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "capabilities", "bootstrap", "Could not resolve request context.", err)
			return
		}
		detail, err := s.services.Capabilities.ApproveProviderAdvertisement(ctx, req, adRef, input)
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "provider_advertisement.approve_failed", "capabilities", adRef, "Could not approve provider advertisement.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, detail))
		return
	}
	if strings.HasSuffix(ref, "/reject") {
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "provider_advertisements", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		adRef := strings.Trim(strings.TrimSuffix(ref, "/reject"), "/")
		var input capabilities.RejectProviderAdvertisementInput
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "capabilities", "body", "Request body is not valid rejection JSON.", err)
			return
		}
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "capabilities", "bootstrap", "Could not resolve request context.", err)
			return
		}
		detail, err := s.services.Capabilities.RejectProviderAdvertisement(ctx, req, adRef, input)
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "provider_advertisement.reject_failed", "capabilities", adRef, "Could not reject provider advertisement.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, detail))
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "provider_advertisements", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	detail, err := s.services.Capabilities.InspectProviderAdvertisement(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "capabilities", ref, "Provider advertisement was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, detail))
}

func (s Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "providers", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "providers", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	providers, err := s.services.Capabilities.ListProviders(ctx, capabilities.ProviderFilter{
		Limit:                 limit,
		NodeRef:               query.Get("node"),
		ScopeRef:              query.Get("scope"),
		ProjectRef:            query.Get("project"),
		ProviderType:          query.Get("type"),
		Status:                query.Get("status"),
		Health:                query.Get("health"),
		RequireActiveEndpoint: parseBoolQuery(query, "require_active_endpoint"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "providers.list_failed", "capabilities", "providers", "Could not list providers.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, providers))
}

func (s Server) handleProvider(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	ref := pathRef(r.URL.Path, "/v1/providers/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "provider.ref_required", "capabilities", "provider", "Provider reference is required.", nil)
		return
	}
	if strings.HasSuffix(ref, "/health") {
		if r.Method != http.MethodGet {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "providers", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		providerRef := strings.Trim(strings.TrimSuffix(ref, "/health"), "/")
		detail, err := s.services.Capabilities.InspectProvider(ctx, providerRef)
		if err != nil {
			s.writeLookupError(w, correlationID, "capabilities", providerRef, "Provider was not found.", err)
			return
		}
		if detail.Health == nil {
			s.writeError(w, correlationID, http.StatusNotFound, "provider_health.not_found", "capabilities", providerRef, "Provider health was not found.", sql.ErrNoRows)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, *detail.Health))
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "providers", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	detail, err := s.services.Capabilities.InspectProvider(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "capabilities", ref, "Provider was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, detail))
}

func (s Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "capabilities", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "capabilities", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	authLevel, err := parseOptionalIntQuery(query, "authorization_level")
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_authorization_level", "capabilities", "authorization_level", "Authorization level must be an integer.", err)
		return
	}
	capabilityList, err := s.services.Capabilities.ListCapabilities(ctx, capabilities.CapabilityFilter{
		Limit:              limit,
		ProviderRef:        query.Get("provider"),
		ClassRef:           query.Get("class"),
		NodeRef:            query.Get("node"),
		ScopeRef:           query.Get("scope"),
		ProjectRef:         query.Get("project"),
		Form:               query.Get("form"),
		Status:             query.Get("status"),
		Risk:               query.Get("risk"),
		AuthorizationLevel: authLevel,
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "capabilities.list_failed", "capabilities", "list", "Could not list capabilities.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, capabilityList))
}

func (s Server) handleCapability(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	ref := pathRef(r.URL.Path, "/v1/capabilities/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "capability.ref_required", "capabilities", "capability", "Capability reference is required.", nil)
		return
	}
	if strings.HasSuffix(ref, "/usage-docs") {
		if r.Method != http.MethodGet {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "capabilities", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		capabilityRef := strings.Trim(strings.TrimSuffix(ref, "/usage-docs"), "/")
		docs, err := s.services.Capabilities.InspectUsageDocuments(ctx, capabilityRef)
		if err != nil {
			s.writeLookupError(w, correlationID, "capabilities", capabilityRef, "Capability usage documents were not found.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, docs))
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "capabilities", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	detail, err := s.services.Capabilities.InspectCapability(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "capabilities", ref, "Capability was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, detail))
}

func (s Server) handleCapabilitySearch(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "capabilities", r.URL.Path, "Method is not allowed.", nil)
		return
	}

	var input capabilities.CapabilitySearchInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "capabilities", "body", "Request body is not valid capability search JSON.", err)
		return
	}
	results, err := s.services.Capabilities.SearchCapabilities(ctx, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "capabilities.search_failed", "capabilities", input.Query, "Could not search capabilities.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, results))
}

func (s Server) handleCapabilityRuntimeBindings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleCapabilityRuntimeBindingList(w, r)
	case http.MethodPost:
		s.handleCapabilityRuntimeBindingRegister(w, r)
	default:
		correlationID := correlation.Normalize(r.Header.Get(correlation.Header))
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "capabilities", r.URL.Path, "Method is not allowed.", nil)
	}
}

func (s Server) handleCapabilityRuntimeBindingList(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "capabilities", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	bindings, err := s.services.Capabilities.ListRuntimeBindings(ctx, capabilities.RuntimeBindingFilter{
		Limit:              limit,
		CapabilityRef:      query.Get("capability"),
		EndpointVersionRef: query.Get("endpoint_version"),
		ProviderRef:        query.Get("provider"),
		RuntimeKind:        query.Get("runtime_kind"),
		Status:             query.Get("status"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "runtime_bindings.list_failed", "capabilities", "runtime_bindings", "Could not list runtime bindings.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, bindings))
}

func (s Server) handleCapabilityRuntimeBindingRegister(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	var input capabilities.RegisterRuntimeBindingInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "capabilities", "body", "Request body is not valid runtime binding JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "capabilities", "bootstrap", "Could not resolve request context.", err)
		return
	}
	binding, err := s.services.Capabilities.RegisterRuntimeBinding(ctx, req, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "runtime_binding.register_failed", "capabilities", input.EndpointVersionRef, "Could not register runtime binding.", err)
		return
	}
	response.WriteJSON(w, http.StatusCreated, response.Success(correlationID, binding))
}

func (s Server) handleCapabilityRuntimeBinding(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if ref, action, ok := runtimeBindingActionRef(r.URL.Path); ok {
		switch action {
		case "validate":
			if r.Method != http.MethodGet {
				s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "capabilities", r.URL.Path, "Method is not allowed.", nil)
				return
			}
			result, err := s.services.Routing.ValidateRuntimeBinding(ctx, ref)
			if err != nil {
				s.writeLookupError(w, correlationID, "capabilities", ref, "Runtime binding was not found.", err)
				return
			}
			response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
			return
		case "test":
			if r.Method != http.MethodPost {
				s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "capabilities", r.URL.Path, "Method is not allowed.", nil)
				return
			}
			var input routing.RuntimeBindingTestInput
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil {
				s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "capabilities", "body", "Request body is not valid runtime binding test JSON.", err)
				return
			}
			req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
			if err != nil {
				s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "capabilities", "bootstrap", "Could not resolve request context.", err)
				return
			}
			result, err := s.services.Routing.TestRuntimeBinding(ctx, req, ref, input)
			if err != nil {
				s.writeLookupError(w, correlationID, "capabilities", ref, "Runtime binding was not found.", err)
				return
			}
			response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
			return
		}
	}
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "capabilities", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	ref := pathRef(r.URL.Path, "/v1/capability-runtime-bindings/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "runtime_binding.ref_required", "capabilities", "runtime_binding", "Runtime binding reference is required.", nil)
		return
	}
	binding, err := s.services.Capabilities.InspectRuntimeBinding(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "capabilities", ref, "Runtime binding was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, binding))
}

func runtimeBindingActionRef(path string) (string, string, bool) {
	trimmed := strings.Trim(strings.TrimPrefix(path, "/v1/capability-runtime-bindings/"), "/")
	if trimmed == "" {
		return "", "", false
	}
	ref, action, ok := strings.Cut(trimmed, "/")
	if !ok || strings.TrimSpace(ref) == "" || strings.TrimSpace(action) == "" {
		return "", "", false
	}
	return ref, action, true
}

func (s Server) handleModuleRegister(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "modules", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input modules.RegisterPackageInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "modules", "body", "Request body is not valid module registration JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "modules", "bootstrap", "Could not resolve request context.", err)
		return
	}
	registration, err := s.services.Modules.RegisterPackage(ctx, req, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "modules.register_failed", "modules", input.PackagePath, "Could not register module package.", err)
		return
	}
	response.WriteJSON(w, http.StatusCreated, response.Success(correlationID, registration))
}

func (s Server) handleModules(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "modules", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	limit, err := parseLimit(r, modules.DefaultModuleLimit)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "modules", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	moduleList, err := s.services.Modules.ListModules(ctx, modules.ModuleFilter{
		Limit:      limit,
		Status:     query.Get("status"),
		ModuleID:   query.Get("module_id"),
		ProjectRef: query.Get("project"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "modules.list_failed", "modules", "list", "Could not list modules.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, moduleList))
}

func (s Server) handleModule(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	ref := pathRef(r.URL.Path, "/v1/modules/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "module.ref_required", "modules", "module", "Module reference is required.", nil)
		return
	}

	if strings.HasSuffix(ref, "/install") {
		moduleRef := strings.Trim(strings.TrimSuffix(ref, "/install"), "/")
		if moduleRef == "" {
			s.writeError(w, correlationID, http.StatusNotFound, "module.ref_required", "modules", "module", "Module reference is required.", nil)
			return
		}
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "modules", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		var input modules.InstallModuleInput
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "modules", "body", "Request body is not valid module install JSON.", err)
			return
		}
		if strings.TrimSpace(input.ModuleVersionRef) == "" {
			input.ModuleVersionRef = moduleRef
		}
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "modules", "bootstrap", "Could not resolve request context.", err)
			return
		}
		detail, err := s.services.Modules.InstallModule(ctx, req, input)
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "modules.install_failed", "modules", moduleRef, "Could not install module.", err)
			return
		}
		response.WriteJSON(w, http.StatusCreated, response.Success(correlationID, detail))
		return
	}

	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "modules", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	detail, err := s.services.Modules.InspectModule(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "modules", ref, "Module was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, detail))
}

func (s Server) handleModuleVersion(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	ref := pathRef(r.URL.Path, "/v1/module-versions/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "module_version.ref_required", "modules", "module_version", "Module version reference is required.", nil)
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "modules", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	detail, err := s.services.Modules.InspectModuleVersion(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "modules", ref, "Module version was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, detail))
}

func (s Server) handleModuleInstallation(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	ref := pathRef(r.URL.Path, "/v1/module-installations/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "module_installation.ref_required", "modules", "module_installation", "Module installation reference is required.", nil)
		return
	}

	if strings.HasSuffix(ref, "/enable") && !strings.Contains(ref, "/capabilities/") {
		installationRef := strings.Trim(strings.TrimSuffix(ref, "/enable"), "/")
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "modules", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "modules", "bootstrap", "Could not resolve request context.", err)
			return
		}
		detail, err := s.services.Modules.EnableInstallation(ctx, req, installationRef)
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "modules.enable_failed", "modules", installationRef, "Could not enable module installation.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, detail))
		return
	}

	if strings.HasSuffix(ref, "/disable") && !strings.Contains(ref, "/capabilities/") {
		installationRef := strings.Trim(strings.TrimSuffix(ref, "/disable"), "/")
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "modules", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "modules", "bootstrap", "Could not resolve request context.", err)
			return
		}
		detail, err := s.services.Modules.DisableInstallation(ctx, req, installationRef)
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "modules.disable_failed", "modules", installationRef, "Could not disable module installation.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, detail))
		return
	}

	if strings.HasSuffix(ref, "/health") {
		installationRef := strings.Trim(strings.TrimSuffix(ref, "/health"), "/")
		if r.Method != http.MethodGet {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "modules", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		detail, err := s.services.Modules.InspectHealth(ctx, installationRef)
		if err != nil {
			s.writeLookupError(w, correlationID, "modules", installationRef, "Module installation health was not found.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, detail))
		return
	}

	if installationRef, capabilityRef, action, ok := parseModuleCapabilityAction(ref); ok {
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "modules", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "modules", "bootstrap", "Could not resolve request context.", err)
			return
		}
		switch action {
		case "expose":
			var input modules.ExposeModuleCapabilityInput
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
				s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "modules", "body", "Request body is not valid module capability exposure JSON.", err)
				return
			}
			input.InstallationRef = installationRef
			input.CapabilityRef = capabilityRef
			result, err := s.services.Modules.ExposeModuleCapability(ctx, req, input)
			if err != nil {
				s.writeError(w, correlationID, http.StatusBadRequest, "modules.capability_expose_failed", "modules", capabilityRef, "Could not expose module capability.", err)
				return
			}
			response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
			return
		case "disable":
			var input modules.DisableModuleCapabilityInput
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
				s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "modules", "body", "Request body is not valid module capability disable JSON.", err)
				return
			}
			input.InstallationRef = installationRef
			input.CapabilityRef = capabilityRef
			result, err := s.services.Modules.DisableModuleCapability(ctx, req, input)
			if err != nil {
				s.writeError(w, correlationID, http.StatusBadRequest, "modules.capability_disable_failed", "modules", capabilityRef, "Could not disable module capability.", err)
				return
			}
			response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
			return
		}
	}

	if strings.HasSuffix(ref, "/backup-exports") {
		installationRef := strings.Trim(strings.TrimSuffix(ref, "/backup-exports"), "/")
		switch r.Method {
		case http.MethodPost:
			var input modules.ExportModuleBackupInput
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
				s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "modules", "body", "Request body is not valid module backup export JSON.", err)
				return
			}
			input.InstallationRef = installationRef
			req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
			if err != nil {
				s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "modules", "bootstrap", "Could not resolve request context.", err)
				return
			}
			detail, err := s.services.Modules.ExportModuleBackup(ctx, req, input)
			if err != nil {
				s.writeError(w, correlationID, http.StatusBadRequest, "modules.backup_export_failed", "modules", installationRef, "Could not export module backup.", err)
				return
			}
			response.WriteJSON(w, http.StatusCreated, response.Success(correlationID, detail))
			return
		case http.MethodGet:
			limit, err := parseLimit(r, modules.DefaultModuleLimit)
			if err != nil {
				s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "modules", "limit", "Limit must be a positive integer.", err)
				return
			}
			exports, err := s.services.Modules.ListModuleBackupExports(ctx, modules.ModuleBackupExportFilter{
				InstallationRef: installationRef,
				Kind:            r.URL.Query().Get("kind"),
				Limit:           limit,
			})
			if err != nil {
				s.writeLookupError(w, correlationID, "modules", installationRef, "Module backup exports were not found.", err)
				return
			}
			response.WriteJSON(w, http.StatusOK, response.Success(correlationID, exports))
			return
		default:
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "modules", r.URL.Path, "Method is not allowed.", nil)
			return
		}
	}

	if strings.HasSuffix(ref, "/providers") {
		installationRef := strings.Trim(strings.TrimSuffix(ref, "/providers"), "/")
		if r.Method != http.MethodGet {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "modules", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		providers, err := s.services.Modules.ListInstallationProviders(ctx, installationRef)
		if err != nil {
			s.writeLookupError(w, correlationID, "modules", installationRef, "Module installation providers were not found.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, providers))
		return
	}

	if strings.HasSuffix(ref, "/capabilities") {
		installationRef := strings.Trim(strings.TrimSuffix(ref, "/capabilities"), "/")
		if r.Method != http.MethodGet {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "modules", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		capabilityList, err := s.services.Modules.ListInstallationCapabilities(ctx, installationRef)
		if err != nil {
			s.writeLookupError(w, correlationID, "modules", installationRef, "Module installation capabilities were not found.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, capabilityList))
		return
	}

	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "modules", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	detail, err := s.services.Modules.InspectInstallation(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "modules", ref, "Module installation was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, detail))
}

func (s Server) handleModuleBackupExport(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	ref := pathRef(r.URL.Path, "/v1/module-backup-exports/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "module_backup_export.ref_required", "modules", "module_backup_export", "Module backup export reference is required.", nil)
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "modules", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	detail, err := s.services.Modules.InspectModuleBackupExport(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "modules", ref, "Module backup export was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, detail))
}

func (s Server) handleWorkers(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "workers", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "workers", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	workerList, err := s.services.Workers.ListWorkers(ctx, workers.WorkerFilter{
		Limit:   limit,
		Kind:    query.Get("kind"),
		Status:  query.Get("status"),
		Health:  query.Get("health"),
		NodeRef: query.Get("node"),
	})
	if err != nil {
		s.writeWorkerError(w, correlationID, "list", "Could not list workers.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, workerList))
}

func (s Server) handleWorkersRepairStale(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "workers", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "workers", "bootstrap", "Could not resolve request context.", err)
		return
	}
	result, err := s.services.Workers.RepairStaleRuns(ctx, req, time.Now().UTC())
	if err != nil {
		s.writeWorkerError(w, correlationID, "repair-stale", "Could not repair stale worker runs.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleWorker(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	ref := pathRef(r.URL.Path, "/v1/workers/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "worker.ref_required", "workers", "worker", "Worker reference is required.", nil)
		return
	}

	if strings.HasSuffix(ref, "/runs") {
		workerRef := strings.Trim(strings.TrimSuffix(ref, "/runs"), "/")
		if r.Method != http.MethodGet {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "workers", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		limit, err := parseLimit(r, 50)
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "workers", "limit", "Limit must be a positive integer.", err)
			return
		}
		query := r.URL.Query()
		runs, err := s.services.Workers.ListRuns(ctx, workerRef, workers.RunFilter{
			Limit:   limit,
			Status:  query.Get("status"),
			Trigger: query.Get("trigger"),
		})
		if err != nil {
			s.writeWorkerError(w, correlationID, workerRef, "Could not list worker runs.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, runs))
		return
	}

	if strings.HasSuffix(ref, "/run-once") {
		workerRef := strings.Trim(strings.TrimSuffix(ref, "/run-once"), "/")
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "workers", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		var input workers.RunOnceInput
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "workers", "body", "Request body is not valid worker run JSON.", err)
			return
		}
		if input.IdempotencyKey == "" {
			input.IdempotencyKey = strings.TrimSpace(r.Header.Get(idempotency.Header))
		}
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "workers", "bootstrap", "Could not resolve request context.", err)
			return
		}
		idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "worker.run_once", input)
		if !proceed {
			return
		}
		result, err := s.services.Workers.RunOnce(ctx, req, workerRef, input)
		if err != nil && result.Run.WorkerRunID == "" {
			status := http.StatusBadRequest
			if errors.Is(err, workers.ErrConflict) {
				status = http.StatusConflict
			}
			if errors.Is(err, workers.ErrNotFound) {
				status = http.StatusNotFound
			}
			idemErr := loomerrors.Wrap("worker.run_once_failed", "workers", workerRef, "Could not run worker once.", err)
			s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
			if s.writeProjectRuntimeArchivedError(w, correlationID, "workers", workerRef, err) {
				return
			}
			s.writeError(w, correlationID, status, "worker.run_once_failed", "workers", workerRef, "Could not run worker once.", err)
			return
		}
		envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, result)
		s.completeIdempotency(ctx, idemRecord, "worker_run", result.Run.WorkerRunID, envelope)
		response.WriteJSON(w, http.StatusOK, envelope)
		return
	}

	if strings.HasSuffix(ref, "/policy") {
		workerRef := strings.Trim(strings.TrimSuffix(ref, "/policy"), "/")
		if workerRef == "" {
			s.writeError(w, correlationID, http.StatusNotFound, "worker.ref_required", "workers", "policy", "Worker reference is required.", nil)
			return
		}
		switch r.Method {
		case http.MethodGet:
			policy, err := s.services.Workers.InspectWorkerPolicy(ctx, workerRef)
			if err != nil {
				s.writeWorkerError(w, correlationID, workerRef, "Could not inspect worker policy.", err)
				return
			}
			response.WriteJSON(w, http.StatusOK, response.Success(correlationID, policy))
			return
		case http.MethodPost:
			var input workers.SetWorkerPolicyInput
			const maxWorkerPolicyRequestBytes = 16 << 10
			r.Body = http.MaxBytesReader(w, r.Body, maxWorkerPolicyRequestBytes)
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil {
				s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "workers", workerRef, "Request body is not valid worker policy JSON.", err)
				return
			}
			if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
				s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "workers", workerRef, "Request body must contain exactly one worker policy object.", err)
				return
			}
			input.IdempotencyKey = strings.TrimSpace(r.Header.Get(idempotency.Header))
			normalizedInput, err := workers.NormalizeSetWorkerPolicyInput(input)
			if err != nil {
				s.writeWorkerError(w, correlationID, workerRef, "Worker policy request is invalid.", err)
				return
			}
			input = normalizedInput
			req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
			if err != nil {
				s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "workers", workerRef, "Could not resolve request context.", err)
				return
			}
			var idemRecord idempotency.Record
			if !input.DryRun {
				var proceed bool
				idemRecord, proceed = s.beginRecoverableIdempotency(w, r, ctx, correlationID, req, "worker.policy.set", map[string]any{"worker_ref": workerRef, "input": input})
				if !proceed {
					return
				}
			}
			result, err := s.services.Workers.SetWorkerPolicy(ctx, req, workerRef, input)
			if err != nil {
				status := http.StatusBadRequest
				if errors.Is(err, workers.ErrConflict) {
					status = http.StatusConflict
				} else if errors.Is(err, workers.ErrNotFound) {
					status = http.StatusNotFound
				}
				policyErr := loomerrors.Wrap("worker.policy_set_failed", "workers", workerRef, "Could not set worker policy.", err)
				s.failIdempotency(ctx, idemRecord, policyErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, policyErr))
				s.writeErrorWithIdempotency(w, correlationID, idemRecord.Key, status, policyErr.Code, "workers", workerRef, policyErr.Summary, err)
				return
			}
			if input.DryRun {
				response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
				return
			}
			envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, result)
			s.completeIdempotency(ctx, idemRecord, "worker_instance", result.WorkerInstanceID, envelope)
			response.WriteJSON(w, http.StatusOK, envelope)
			return
		default:
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "workers", r.URL.Path, "Method is not allowed.", nil)
			return
		}
	}

	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "workers", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	detail, err := s.services.Workers.InspectWorker(ctx, ref)
	if err != nil {
		s.writeWorkerError(w, correlationID, ref, "Worker was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, detail))
}

func (s Server) handleMaintenanceStatus(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "maintenance", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	status, err := s.services.Maintenance.Status(ctx)
	if err != nil {
		s.writeMaintenanceError(w, correlationID, "status", "Could not read maintenance status.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, status))
}

func (s Server) handleMaintenanceFindings(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "maintenance", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "maintenance", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	findings, err := s.services.Maintenance.ListFindings(ctx, maintenance.FindingFilter{
		Limit:     limit,
		Status:    query.Get("status"),
		Severity:  query.Get("severity"),
		WorkerRef: query.Get("worker"),
	})
	if err != nil {
		s.writeMaintenanceError(w, correlationID, "findings", "Could not list maintenance findings.", err)
		return
	}
	if findings == nil {
		findings = []maintenance.Finding{}
	}
	response.WriteJSON(w, http.StatusOK, struct {
		OK   bool                  `json:"ok"`
		Data []maintenance.Finding `json:"data"`
		Meta response.Meta         `json:"meta"`
	}{
		OK:   true,
		Data: findings,
		Meta: response.NewMeta(correlationID),
	})
}

func (s Server) handleMaintenanceDBStatus(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "maintenance", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	status, err := s.services.Maintenance.DBStatus(ctx)
	if err != nil {
		s.writeMaintenanceError(w, correlationID, "db", "Could not read maintenance database status.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, status))
}

func (s Server) handleMaintenanceDBCompact(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "maintenance", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input maintenance.DatabaseCompactInput
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "maintenance", "body", "Request body is not valid database compact JSON.", err)
		return
	}
	result, err := s.services.Maintenance.CompactDatabase(ctx, input)
	if err != nil {
		s.writeMaintenanceError(w, correlationID, "db", "Could not compact maintenance database.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleMaintenanceBackupStatus(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "maintenance", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	status, err := s.services.Maintenance.BackupStatus(ctx)
	if err != nil {
		s.writeMaintenanceError(w, correlationID, "backup", "Could not read maintenance backup status.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, status))
}

func (s Server) handleMaintenanceBackupList(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "maintenance", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "maintenance", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	backups, err := s.services.Maintenance.ListBackups(ctx, maintenance.OperationFilter{
		Kind:        query.Get("kind"),
		Status:      query.Get("status"),
		SubjectKind: query.Get("subject_kind"),
		SubjectID:   query.Get("subject_id"),
		Limit:       limit,
	})
	if err != nil {
		s.writeMaintenanceError(w, correlationID, "backup", "Could not list maintenance backups.", err)
		return
	}
	if backups == nil {
		backups = []maintenance.BackupOperation{}
	}
	response.WriteJSON(w, http.StatusOK, struct {
		OK   bool                          `json:"ok"`
		Data []maintenance.BackupOperation `json:"data"`
		Meta response.Meta                 `json:"meta"`
	}{
		OK:   true,
		Data: backups,
		Meta: response.NewMeta(correlationID),
	})
}

func (s Server) handleMaintenanceBackupRun(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "maintenance", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input maintenance.BackupRunInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "maintenance", "body", "Request body is not valid backup run JSON.", err)
		return
	}
	if input.IdempotencyKey == "" {
		input.IdempotencyKey = strings.TrimSpace(r.Header.Get(idempotency.Header))
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "maintenance", "bootstrap", "Could not resolve request context.", err)
		return
	}
	workerInput := workers.RunOnceInput{
		Reason:         strings.TrimSpace(input.Reason),
		Metadata:       input.Metadata,
		IdempotencyKey: input.IdempotencyKey,
	}
	if workerInput.Reason == "" {
		workerInput.Reason = "manual backup request"
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "maintenance.backup.run", workerInput)
	if !proceed {
		return
	}
	result, err := s.services.Workers.RunOnce(ctx, req, "main.main_backup", workerInput)
	if err != nil && result.Run.WorkerRunID == "" {
		status := http.StatusBadRequest
		if errors.Is(err, workers.ErrConflict) {
			status = http.StatusConflict
		}
		if errors.Is(err, workers.ErrNotFound) {
			status = http.StatusNotFound
		}
		idemErr := loomerrors.Wrap("maintenance.backup_run_failed", "maintenance", "backup", "Could not run maintenance backup.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, status, "maintenance.backup_run_failed", "maintenance", "backup", "Could not run maintenance backup.", err)
		return
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, result)
	s.completeIdempotency(ctx, idemRecord, "worker_run", result.Run.WorkerRunID, envelope)
	response.WriteJSON(w, http.StatusOK, envelope)
}

func (s Server) handleMaintenanceBackupVerify(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "maintenance", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input maintenance.BackupVerifyInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "maintenance", "body", "Request body is not valid backup verify JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "maintenance", "bootstrap", "Could not resolve request context.", err)
		return
	}
	verification, operational, err := s.verifyOperationalMaintenanceBackup(ctx, input.BackupRef)
	if err != nil {
		s.writeMaintenanceError(w, correlationID, "backup", "Could not verify operational maintenance backup.", err)
		return
	}
	if operational {
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, verification))
		return
	}
	verification, err = s.services.Maintenance.VerifyBackup(ctx, req, input.BackupRef)
	if err != nil {
		s.writeMaintenanceError(w, correlationID, "backup", "Could not verify maintenance backup.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, verification))
}

func (s Server) verifyOperationalMaintenanceBackup(ctx context.Context, ref string) (maintenance.BackupVerification, bool, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return maintenance.BackupVerification{}, false, nil
	}
	backups, err := s.services.Maintenance.ListBackups(ctx, maintenance.OperationFilter{Kind: maintenance.OperationKindMainBackup, Status: maintenance.OperationSucceeded, Limit: 100})
	if err != nil {
		return maintenance.BackupVerification{}, false, err
	}
	for _, backup := range backups {
		var result struct {
			SchemaVersion  string `json:"schema_version"`
			Phase          string `json:"phase"`
			Committed      bool   `json:"committed"`
			PackageID      string `json:"package_id"`
			PackageDir     string `json:"package_dir"`
			ManifestSHA256 string `json:"manifest_sha256"`
			SchemaHead     int64  `json:"schema_head"`
		}
		if json.Unmarshal(backup.Operation.ResultJSON, &result) != nil || result.SchemaVersion != "main_backup.result.v1" || result.Phase != "recovery_packages" || !result.Committed {
			continue
		}
		if ref != backup.Operation.MaintenanceOperationID && ref != result.PackageID && ref != result.PackageDir {
			continue
		}
		verification, verifyErr := maintenance.VerifyOperationalBackupPackage(ctx, result.PackageDir, result.ManifestSHA256, result.SchemaHead, nil)
		verification.BackupOperationID = backup.Operation.MaintenanceOperationID
		return verification, true, verifyErr
	}
	return maintenance.BackupVerification{}, false, nil
}

func (s Server) handleMaintenanceObjectStoreStatus(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "maintenance", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	status, err := s.services.Maintenance.ObjectStoreStatus(ctx)
	if err != nil {
		s.writeMaintenanceError(w, correlationID, "object-store", "Could not read object-store maintenance status.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, status))
}

func (s Server) handleMaintenanceObjectStoreScan(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "maintenance", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input maintenance.ObjectStoreScanInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "maintenance", "body", "Request body is not valid object-store scan JSON.", err)
		return
	}
	if input.IdempotencyKey == "" {
		input.IdempotencyKey = strings.TrimSpace(r.Header.Get(idempotency.Header))
	}
	metadata, err := objectStoreScanMetadata(input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "maintenance", "metadata", "Object-store scan metadata must be a JSON object.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "maintenance", "bootstrap", "Could not resolve request context.", err)
		return
	}
	reason := strings.TrimSpace(input.Reason)
	if reason == "" {
		if strings.TrimSpace(input.TargetBlobRef) != "" {
			reason = "manual targeted object-store integrity scan"
		} else {
			reason = "manual object-store integrity sample scan"
		}
	}
	workerInput := workers.RunOnceInput{
		Reason:         reason,
		Metadata:       metadata,
		IdempotencyKey: input.IdempotencyKey,
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "maintenance.object_store.scan", workerInput)
	if !proceed {
		return
	}
	result, err := s.services.Workers.RunOnce(ctx, req, "main.object_store_integrity_sample", workerInput)
	if err != nil && result.Run.WorkerRunID == "" {
		status := http.StatusBadRequest
		if errors.Is(err, workers.ErrConflict) {
			status = http.StatusConflict
		}
		if errors.Is(err, workers.ErrNotFound) {
			status = http.StatusNotFound
		}
		idemErr := loomerrors.Wrap("maintenance.object_store_scan_failed", "maintenance", "object-store", "Could not run object-store integrity scan.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, status, "maintenance.object_store_scan_failed", "maintenance", "object-store", "Could not run object-store integrity scan.", err)
		return
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, result)
	s.completeIdempotency(ctx, idemRecord, "worker_run", result.Run.WorkerRunID, envelope)
	response.WriteJSON(w, http.StatusOK, envelope)
}

func objectStoreScanMetadata(input maintenance.ObjectStoreScanInput) (json.RawMessage, error) {
	metadata := map[string]any{}
	if len(input.Metadata) > 0 && strings.TrimSpace(string(input.Metadata)) != "" && strings.TrimSpace(string(input.Metadata)) != "{}" {
		if err := json.Unmarshal(input.Metadata, &metadata); err != nil {
			return nil, err
		}
		if metadata == nil {
			return nil, fmt.Errorf("metadata must be a JSON object")
		}
	}
	mode := strings.TrimSpace(input.Mode)
	if mode == "" {
		mode = "sample"
	}
	metadata["schema_version"] = "object_store_scan.request.v0.2"
	metadata["source"] = "maintenance.object_store.scan"
	metadata["action"] = "scan"
	metadata["mode"] = mode
	if target := strings.TrimSpace(input.TargetBlobRef); target != "" {
		metadata["target_blob_ref"] = target
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func (s Server) handlePolicyExplain(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "policy", r.URL.Path, "Method is not allowed.", nil)
		return
	}

	var input policy.DecisionInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "policy", "body", "Request body is not valid policy explanation JSON.", err)
		return
	}

	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "policy", "bootstrap", "Could not resolve request context.", err)
		return
	}
	explanation, err := s.services.Policy.Explain(ctx, req, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "policy.explain_failed", "policy", input.Operation, "Could not explain policy decision.", err)
		return
	}
	if explanation.Decision.ReasonCode == "policy.unsupported_operation" {
		s.writeError(w, correlationID, http.StatusBadRequest, "policy.unsupported_operation", "policy", input.Operation, "Operation is not a supported policy target.", nil)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, explanation))
}

func (s Server) handlePolicyDecisions(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "policy", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "policy", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	decisions, err := s.services.Policy.ListDecisions(ctx, policy.DecisionFilter{
		Limit:                 limit,
		ActorRef:              query.Get("actor"),
		OriginNodeRef:         query.Get("origin_node"),
		TargetNodeRef:         query.Get("target_node"),
		Operation:             query.Get("operation"),
		Decision:              query.Get("decision"),
		CapabilityEndpointRef: query.Get("capability"),
		ApprovalRef:           query.Get("approval"),
		GrantRef:              query.Get("grant"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "policy.decisions_list_failed", "policy", "decisions", "Could not list policy decisions.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, decisions))
}

func (s Server) handlePolicyDecision(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "policy", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	ref := pathRef(r.URL.Path, "/v1/policy/decisions/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "policy.decision_ref_required", "policy", "decision", "Policy decision reference is required.", nil)
		return
	}
	decision, err := s.services.Policy.GetDecision(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "policy", ref, "Policy decision was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, decision))
}

func (s Server) handleApprovals(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "approvals", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "approvals", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	approvals, err := s.services.Policy.ListApprovals(ctx, policy.ApprovalFilter{
		Limit:                 limit,
		Status:                query.Get("status"),
		ActorRef:              query.Get("actor"),
		ApprovingActorRef:     query.Get("approving_actor"),
		TargetNodeRef:         query.Get("target_node"),
		CapabilityEndpointRef: query.Get("capability"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "approvals.list_failed", "policy", "approvals", "Could not list approvals.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, approvals))
}

func (s Server) handleApproval(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	ref := pathRef(r.URL.Path, "/v1/approvals/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "approval.ref_required", "policy", "approval", "Approval reference is required.", nil)
		return
	}
	if strings.HasSuffix(ref, "/decide") {
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "approvals", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		approvalRef := strings.Trim(strings.TrimSuffix(ref, "/decide"), "/")
		var input policy.ApprovalDecisionInput
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "policy", "body", "Request body is not valid approval decision JSON.", err)
			return
		}
		input.ApprovalRef = approvalRef
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "policy", "bootstrap", "Could not resolve request context.", err)
			return
		}
		result, err := s.services.Policy.DecideApproval(ctx, req, input)
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "approval.decide_failed", "policy", approvalRef, "Could not decide approval.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "approvals", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	approval, err := s.services.Policy.GetApproval(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "policy", ref, "Approval was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, approval))
}

func (s Server) handleGrants(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "grants", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "grants", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	grants, err := s.services.Policy.ListGrants(ctx, policy.GrantFilter{
		Limit:                 limit,
		Status:                query.Get("status"),
		GrantType:             query.Get("type"),
		GrantedToActorRef:     query.Get("actor"),
		GrantedByActorRef:     query.Get("granted_by"),
		ApprovalRef:           query.Get("approval"),
		TargetNodeRef:         query.Get("target_node"),
		CapabilityEndpointRef: query.Get("capability"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "grants.list_failed", "policy", "grants", "Could not list grants.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, grants))
}

func (s Server) handleGrant(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	ref := pathRef(r.URL.Path, "/v1/grants/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "grant.ref_required", "policy", "grant", "Grant reference is required.", nil)
		return
	}
	if strings.HasSuffix(ref, "/revoke") {
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "grants", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		grantRef := strings.Trim(strings.TrimSuffix(ref, "/revoke"), "/")
		var input policy.GrantRevokeInput
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "policy", "body", "Request body is not valid grant revoke JSON.", err)
			return
		}
		input.GrantRef = grantRef
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "policy", "bootstrap", "Could not resolve request context.", err)
			return
		}
		grant, err := s.services.Policy.RevokeGrant(ctx, req, input)
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "grant.revoke_failed", "policy", grantRef, "Could not revoke grant.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, grant))
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "grants", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	grant, err := s.services.Policy.GetGrant(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "policy", ref, "Grant was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, grant))
}

func (s Server) handleCapabilityCalls(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handleCapabilityCallCreate(w, r)
	case http.MethodGet:
		s.handleCapabilityCallList(w, r)
	default:
		correlationID := correlation.Normalize(r.Header.Get(correlation.Header))
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "routing", r.URL.Path, "Method is not allowed.", nil)
	}
}

func (s Server) handleCapabilityCallCreate(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	var input routing.CapabilityCallInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "routing", "body", "Request body is not valid capability call JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "routing", "bootstrap", "Could not resolve request context.", err)
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "capability.call", input)
	if !proceed {
		return
	}

	outcome, err := s.services.Routing.Call(ctx, req, input, idemRecord.Key)
	if err != nil {
		idemErr := loomerrors.Wrap("capability_call.failed", "routing", input.Target, "Could not call capability.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		if s.writeProjectRuntimeArchivedError(w, correlationID, "routing", input.Target, err) {
			return
		}
		s.writeError(w, correlationID, http.StatusBadRequest, "capability_call.failed", "routing", input.Target, "Could not call capability.", err)
		return
	}
	if outcome.ApprovalID != "" {
		if _, err := s.services.Realtime.CreateApprovalNotification(ctx, req, realtime.ApprovalNotificationInput{
			ApprovalRef:       outcome.ApprovalID,
			RouteRef:          outcome.Route.RouteID,
			CapabilityCallRef: outcome.CapabilityCall.CapabilityCallID,
			PolicyDecisionRef: outcome.PolicyDecisionID,
			ScopeRef:          pointerValue(outcome.CapabilityCall.ScopeID),
		}); err != nil {
			idemErr := loomerrors.Wrap("realtime.approval_notification_failed", "realtime", outcome.ApprovalID, "Capability call required approval but notification creation failed.", err)
			s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
			s.writeError(w, correlationID, http.StatusInternalServerError, "realtime.approval_notification_failed", "realtime", outcome.ApprovalID, "Capability call required approval but notification creation failed.", err)
			return
		}
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, outcome)
	s.completeIdempotency(ctx, idemRecord, "capability_call", outcome.CapabilityCall.CapabilityCallID, envelope)
	response.WriteJSON(w, http.StatusCreated, envelope)
}

func (s Server) handleCapabilityCallList(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "routing", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	calls, err := s.services.Routing.ListCapabilityCalls(ctx, routing.CapabilityCallFilter{
		Limit:                 limit,
		Status:                query.Get("status"),
		ActorRef:              query.Get("actor"),
		OriginNodeRef:         query.Get("origin_node"),
		ScopeRef:              query.Get("scope"),
		TargetNodeRef:         query.Get("target_node"),
		ProviderRef:           query.Get("provider"),
		CapabilityEndpointRef: query.Get("capability"),
		PolicyDecisionRef:     query.Get("policy_decision"),
		ApprovalRef:           query.Get("approval"),
		GrantRef:              query.Get("grant"),
		JobRef:                query.Get("job"),
		CorrelationID:         query.Get("correlation"),
		IdempotencyKey:        query.Get("idempotency_key"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "capability_calls.list_failed", "routing", "capability_calls", "Could not list capability calls.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, calls))
}

func (s Server) handleCapabilityCall(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "routing", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	ref := pathRef(r.URL.Path, "/v1/capability-calls/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "capability_call.ref_required", "routing", "capability_call", "Capability call reference is required.", nil)
		return
	}
	call, err := s.services.Routing.GetCapabilityCall(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "routing", ref, "Capability call was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, call))
}

func (s Server) handleRoutes(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "routing", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "routing", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	routes, err := s.services.Routing.ListRoutes(ctx, routing.RouteFilter{
		Limit:                 limit,
		Status:                query.Get("status"),
		ActorRef:              query.Get("actor"),
		OriginNodeRef:         query.Get("origin_node"),
		OriginScopeRef:        query.Get("scope"),
		RuntimeNodeRef:        query.Get("runtime_node"),
		TargetNodeRef:         query.Get("target_node"),
		ProviderRef:           query.Get("provider"),
		CapabilityEndpointRef: query.Get("capability"),
		PolicyDecisionRef:     query.Get("policy_decision"),
		ApprovalRef:           query.Get("approval"),
		GrantRef:              query.Get("grant"),
		JobRef:                query.Get("job"),
		CorrelationID:         query.Get("correlation"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "routes.list_failed", "routing", "routes", "Could not list routes.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, routes))
}

func (s Server) handleRoute(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "routing", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	ref := pathRef(r.URL.Path, "/v1/routes/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "route.ref_required", "routing", "route", "Route reference is required.", nil)
		return
	}
	route, err := s.services.Routing.GetRoute(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "routing", ref, "Route was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, route))
}

func (s Server) handleCommunicationHealth(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "communication", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	health, err := s.services.Communication.Health(ctx)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "communication.health_failed", "communication", "health", "Could not read communication health.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, health))
}

func (s Server) handleCommunicationMessages(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleCommunicationMessageList(w, r)
	case http.MethodPost:
		s.handleCommunicationMessageEnqueue(w, r)
	default:
		correlationID := correlation.Normalize(r.Header.Get(correlation.Header))
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "communication", r.URL.Path, "Method is not allowed.", nil)
	}
}

func (s Server) handleCommunicationMessageList(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "communication", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	messages, err := s.services.Communication.ListMessages(ctx, communication.MessageFilter{
		Limit:          limit,
		NodeRef:        query.Get("node"),
		Status:         query.Get("status"),
		Kind:           query.Get("kind"),
		Direction:      query.Get("direction"),
		CorrelationID:  query.Get("correlation"),
		IdempotencyKey: query.Get("idempotency_key"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "communication.messages_list_failed", "communication", "messages", "Could not list communication messages.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, messages))
}

func (s Server) handleCommunicationMessageEnqueue(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	var input communication.EnqueueInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "communication", "body", "Request body is not valid communication message JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "communication", "bootstrap", "Could not resolve request context.", err)
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "communication.message.enqueue", input)
	if !proceed {
		return
	}
	message, err := s.services.Communication.Enqueue(ctx, req, input)
	if err != nil {
		idemErr := loomerrors.Wrap("communication.message_enqueue_failed", "communication", input.NodeRef, "Could not enqueue communication message.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, http.StatusBadRequest, "communication.message_enqueue_failed", "communication", input.NodeRef, "Could not enqueue communication message.", err)
		return
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, message)
	s.completeIdempotency(ctx, idemRecord, "communication_message", message.CommunicationMessageID, envelope)
	response.WriteJSON(w, http.StatusCreated, envelope)
}

func (s Server) handleCommunicationMessage(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "communication", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	ref := pathRef(r.URL.Path, "/v1/communication/messages/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "communication_message.ref_required", "communication", "message", "Communication message reference is required.", nil)
		return
	}
	message, err := s.services.Communication.GetMessage(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "communication", ref, "Communication message was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, message))
}

func (s Server) handleRealtimeTopics(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleRealtimeTopicList(w, r)
	case http.MethodPost:
		s.handleRealtimeTopicCreate(w, r)
	default:
		correlationID := correlation.Normalize(r.Header.Get(correlation.Header))
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "realtime", r.URL.Path, "Method is not allowed.", nil)
	}
}

func (s Server) handleRealtimeTopicList(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "realtime", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	topics, err := s.services.Realtime.ListTopics(ctx, realtime.TopicFilter{
		Limit:    limit,
		Status:   query.Get("status"),
		ScopeRef: query.Get("scope"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "realtime.topics_list_failed", "realtime", "topics", "Could not list realtime topics.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, topics))
}

func (s Server) handleRealtimeTopicCreate(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	var input realtime.CreateTopicInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "realtime", "body", "Request body is not valid realtime topic JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "realtime", "bootstrap", "Could not resolve request context.", err)
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "realtime.topic.create", input)
	if !proceed {
		return
	}
	topic, err := s.services.Realtime.CreateTopic(ctx, req, input)
	if err != nil {
		idemErr := loomerrors.Wrap("realtime.topic_create_failed", "realtime", input.TopicPath, "Could not create realtime topic.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, http.StatusBadRequest, "realtime.topic_create_failed", "realtime", input.TopicPath, "Could not create realtime topic.", err)
		return
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, topic)
	s.completeIdempotency(ctx, idemRecord, "realtime_topic", topic.TopicID, envelope)
	response.WriteJSON(w, http.StatusCreated, envelope)
}

func (s Server) handleRealtimeTopic(w http.ResponseWriter, r *http.Request) {
	if ref, ok := realtimeActionRef(r.URL.Path, "/v1/realtime/topics/", "publish"); ok {
		s.handleRealtimeTopicPublish(w, r, ref)
		return
	}
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "realtime", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	ref := pathRef(r.URL.Path, "/v1/realtime/topics/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "realtime.topic_ref_required", "realtime", "topic", "Realtime topic reference is required.", nil)
		return
	}
	topic, err := s.services.Realtime.GetTopic(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "realtime", ref, "Realtime topic was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, topic))
}

func (s Server) handleRealtimeTopicPublish(w http.ResponseWriter, r *http.Request, ref string) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "realtime", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "realtime.topic_ref_required", "realtime", "topic", "Realtime topic reference is required.", nil)
		return
	}
	var input realtime.PublishInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "realtime", "body", "Request body is not valid realtime publish JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "realtime", "bootstrap", "Could not resolve request context.", err)
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "realtime.topic.publish", input)
	if !proceed {
		return
	}
	publication, err := s.services.Realtime.PublishTopic(ctx, req, ref, input)
	if err != nil {
		idemErr := loomerrors.Wrap("realtime.topic_publish_failed", "realtime", ref, "Could not publish realtime topic message.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, http.StatusBadRequest, "realtime.topic_publish_failed", "realtime", ref, "Could not publish realtime topic message.", err)
		return
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, publication)
	s.completeIdempotency(ctx, idemRecord, "realtime_topic_publication", publication.TopicPublicationID, envelope)
	response.WriteJSON(w, http.StatusCreated, envelope)
}

func (s Server) handleRealtimeSubscriptions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleRealtimeSubscriptionList(w, r)
	case http.MethodPost:
		s.handleRealtimeSubscriptionCreate(w, r)
	default:
		correlationID := correlation.Normalize(r.Header.Get(correlation.Header))
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "realtime", r.URL.Path, "Method is not allowed.", nil)
	}
}

func (s Server) handleRealtimeSubscriptionList(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "realtime", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	subscriptions, err := s.services.Realtime.ListSubscriptions(ctx, realtime.SubscriptionFilter{
		Limit:    limit,
		Status:   query.Get("status"),
		TopicRef: query.Get("topic"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "realtime.subscriptions_list_failed", "realtime", "subscriptions", "Could not list realtime subscriptions.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, subscriptions))
}

func (s Server) handleRealtimeSubscriptionCreate(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	var input realtime.CreateSubscriptionInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "realtime", "body", "Request body is not valid realtime subscription JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "realtime", "bootstrap", "Could not resolve request context.", err)
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "realtime.subscription.create", input)
	if !proceed {
		return
	}
	subscription, err := s.services.Realtime.CreateSubscription(ctx, req, input)
	if err != nil {
		idemErr := loomerrors.Wrap("realtime.subscription_create_failed", "realtime", input.TopicRef, "Could not create realtime subscription.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, http.StatusBadRequest, "realtime.subscription_create_failed", "realtime", input.TopicRef, "Could not create realtime subscription.", err)
		return
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, subscription)
	s.completeIdempotency(ctx, idemRecord, "realtime_subscription", subscription.SubscriptionID, envelope)
	response.WriteJSON(w, http.StatusCreated, envelope)
}

func (s Server) handleRealtimeSubscription(w http.ResponseWriter, r *http.Request) {
	if ref, ok := realtimeActionRef(r.URL.Path, "/v1/realtime/subscriptions/", "poll"); ok {
		s.handleRealtimeSubscriptionPoll(w, r, ref)
		return
	}
	if ref, ok := realtimeActionRef(r.URL.Path, "/v1/realtime/subscriptions/", "ack"); ok {
		s.handleRealtimeSubscriptionAck(w, r, ref)
		return
	}
	if ref, ok := realtimeActionRef(r.URL.Path, "/v1/realtime/subscriptions/", "cancel"); ok {
		s.handleRealtimeSubscriptionCancel(w, r, ref)
		return
	}
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "realtime", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	ref := pathRef(r.URL.Path, "/v1/realtime/subscriptions/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "realtime.subscription_ref_required", "realtime", "subscription", "Realtime subscription reference is required.", nil)
		return
	}
	subscription, err := s.services.Realtime.GetSubscription(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "realtime", ref, "Realtime subscription was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, subscription))
}

func (s Server) handleRealtimeSubscriptionPoll(w http.ResponseWriter, r *http.Request, ref string) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "realtime", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input realtime.PollSubscriptionInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if r.Body != http.NoBody {
		if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "realtime", "body", "Request body is not valid realtime poll JSON.", err)
			return
		}
	}
	result, err := s.services.Realtime.PollSubscription(ctx, ref, input)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "realtime.subscription_poll_failed", "realtime", ref, "Could not poll realtime subscription.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) handleRealtimeSubscriptionAck(w http.ResponseWriter, r *http.Request, ref string) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "realtime", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input realtime.AcknowledgeSubscriptionInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "realtime", "body", "Request body is not valid realtime subscription ack JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "realtime", "bootstrap", "Could not resolve request context.", err)
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "realtime.subscription.ack", input)
	if !proceed {
		return
	}
	subscription, err := s.services.Realtime.AcknowledgeSubscription(ctx, req, ref, input)
	if err != nil {
		idemErr := loomerrors.Wrap("realtime.subscription_ack_failed", "realtime", ref, "Could not acknowledge realtime subscription.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, http.StatusBadRequest, "realtime.subscription_ack_failed", "realtime", ref, "Could not acknowledge realtime subscription.", err)
		return
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, subscription)
	s.completeIdempotency(ctx, idemRecord, "realtime_subscription", subscription.SubscriptionID, envelope)
	response.WriteJSON(w, http.StatusOK, envelope)
}

func (s Server) handleRealtimeSubscriptionCancel(w http.ResponseWriter, r *http.Request, ref string) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "realtime", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "realtime", "bootstrap", "Could not resolve request context.", err)
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "realtime.subscription.cancel", map[string]string{"subscription_ref": ref})
	if !proceed {
		return
	}
	subscription, err := s.services.Realtime.CancelSubscription(ctx, req, ref)
	if err != nil {
		idemErr := loomerrors.Wrap("realtime.subscription_cancel_failed", "realtime", ref, "Could not cancel realtime subscription.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, http.StatusBadRequest, "realtime.subscription_cancel_failed", "realtime", ref, "Could not cancel realtime subscription.", err)
		return
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, subscription)
	s.completeIdempotency(ctx, idemRecord, "realtime_subscription", subscription.SubscriptionID, envelope)
	response.WriteJSON(w, http.StatusOK, envelope)
}

func (s Server) handleRealtimePresence(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "realtime", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "realtime", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	presence, err := s.services.Realtime.ListPresence(ctx, realtime.PresenceFilter{
		Limit:       limit,
		SubjectKind: query.Get("subject_kind"),
		State:       query.Get("state"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "realtime.presence_list_failed", "realtime", "presence", "Could not list realtime presence.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, presence))
}

func (s Server) handleRealtimePresenceRef(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "realtime", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	ref := pathRef(r.URL.Path, "/v1/realtime/presence/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "realtime.presence_ref_required", "realtime", "presence", "Realtime presence reference is required.", nil)
		return
	}
	presence, err := s.services.Realtime.GetPresence(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "realtime", ref, "Realtime presence was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, presence))
}

func (s Server) handleRealtimeNotifications(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "realtime", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "realtime", "limit", "Limit must be a positive integer.", err)
		return
	}
	query := r.URL.Query()
	notifications, err := s.services.Realtime.ListNotifications(ctx, realtime.NotificationFilter{
		Limit:       limit,
		Status:      query.Get("status"),
		Category:    query.Get("category"),
		TargetKind:  query.Get("target_kind"),
		TargetRef:   query.Get("target_ref"),
		ApprovalRef: query.Get("approval"),
	})
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "realtime.notifications_list_failed", "realtime", "notifications", "Could not list realtime notifications.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, notifications))
}

func (s Server) handleRealtimeNotification(w http.ResponseWriter, r *http.Request) {
	if ref, ok := realtimeActionRef(r.URL.Path, "/v1/realtime/notifications/", "ack"); ok {
		s.handleRealtimeNotificationAck(w, r, ref)
		return
	}
	if ref, ok := realtimeActionRef(r.URL.Path, "/v1/realtime/notifications/", "dismiss"); ok {
		s.handleRealtimeNotificationDismiss(w, r, ref)
		return
	}
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "realtime", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	ref := pathRef(r.URL.Path, "/v1/realtime/notifications/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "realtime.notification_ref_required", "realtime", "notification", "Realtime notification reference is required.", nil)
		return
	}
	notification, err := s.services.Realtime.GetNotification(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "realtime", ref, "Realtime notification was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, notification))
}

func (s Server) handleRealtimeNotificationAck(w http.ResponseWriter, r *http.Request, ref string) {
	s.handleRealtimeNotificationTransition(w, r, ref, "ack")
}

func (s Server) handleRealtimeNotificationDismiss(w http.ResponseWriter, r *http.Request, ref string) {
	s.handleRealtimeNotificationTransition(w, r, ref, "dismiss")
}

func (s Server) handleRealtimeNotificationTransition(w http.ResponseWriter, r *http.Request, ref, action string) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "realtime", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "realtime", "bootstrap", "Could not resolve request context.", err)
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "realtime.notification."+action, map[string]string{"notification_ref": ref})
	if !proceed {
		return
	}
	var notification realtime.Notification
	if action == "ack" {
		notification, err = s.services.Realtime.AcknowledgeNotification(ctx, req, ref)
	} else {
		notification, err = s.services.Realtime.DismissNotification(ctx, req, ref)
	}
	if err != nil {
		idemErr := loomerrors.Wrap("realtime.notification_"+action+"_failed", "realtime", ref, "Could not update realtime notification.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, http.StatusBadRequest, "realtime.notification_"+action+"_failed", "realtime", ref, "Could not update realtime notification.", err)
		return
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, notification)
	s.completeIdempotency(ctx, idemRecord, "realtime_notification", notification.NotificationID, envelope)
	response.WriteJSON(w, http.StatusOK, envelope)
}

func (s Server) handleRealtimeProgress(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "realtime", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	ref := pathRef(r.URL.Path, "/v1/realtime/progress/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "realtime.progress_ref_required", "realtime", "progress", "Realtime progress source is required.", nil)
		return
	}
	limit, err := parseLimit(r, 50)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "realtime", "limit", "Limit must be a positive integer.", err)
		return
	}
	progress, err := s.services.Realtime.GetProgress(ctx, ref, limit)
	if err != nil {
		s.writeLookupError(w, correlationID, "realtime", ref, "Realtime progress feed was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, progress))
}

func (s Server) handleRealtimeLeases(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	switch r.Method {
	case http.MethodGet:
		limit, err := parseLimit(r, 50)
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "realtime", "limit", "Limit must be a positive integer.", err)
			return
		}
		query := r.URL.Query()
		leases, err := s.services.Realtime.ListLeases(ctx, realtime.LeaseFilter{
			Limit:      limit,
			Status:     query.Get("status"),
			Resource:   query.Get("resource"),
			HolderKind: query.Get("holder_kind"),
			HolderRef:  query.Get("holder_ref"),
		})
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "realtime.leases_list_failed", "realtime", "leases", "Could not list realtime leases.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, leases))
	case http.MethodPost:
		var input realtime.RequestLeaseInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "realtime", "body", "Request body is not valid realtime lease JSON.", err)
			return
		}
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "realtime", "bootstrap", "Could not resolve request context.", err)
			return
		}
		idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "realtime.lease.request", input)
		if !proceed {
			return
		}
		lease, err := s.services.Realtime.RequestLease(ctx, req, input)
		if err != nil {
			idemErr := loomerrors.Wrap("realtime.lease_request_failed", "realtime", input.Resource, "Could not request realtime lease.", err)
			s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
			s.writeError(w, correlationID, http.StatusConflict, "realtime.lease_request_failed", "realtime", input.Resource, "Could not request realtime lease.", err)
			return
		}
		envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, lease)
		s.completeIdempotency(ctx, idemRecord, "realtime_lease", lease.LeaseID, envelope)
		response.WriteJSON(w, http.StatusCreated, envelope)
	default:
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "realtime", r.URL.Path, "Method is not allowed.", nil)
	}
}

func (s Server) handleRealtimeLease(w http.ResponseWriter, r *http.Request) {
	if ref, ok := realtimeActionRef(r.URL.Path, "/v1/realtime/leases/", "release"); ok {
		s.handleRealtimeLeaseRelease(w, r, ref)
		return
	}
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "realtime", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	ref := pathRef(r.URL.Path, "/v1/realtime/leases/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "realtime.lease_ref_required", "realtime", "lease", "Realtime lease reference is required.", nil)
		return
	}
	lease, err := s.services.Realtime.GetLease(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "realtime", ref, "Realtime lease was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, lease))
}

func (s Server) handleRealtimeLeaseRelease(w http.ResponseWriter, r *http.Request, ref string) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "realtime", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	var input realtime.ReleaseLeaseInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "realtime", "body", "Request body is not valid realtime lease release JSON.", err)
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "realtime", "bootstrap", "Could not resolve request context.", err)
		return
	}
	idemRecord, proceed := s.beginIdempotency(w, r, ctx, correlationID, req, "realtime.lease.release", input)
	if !proceed {
		return
	}
	lease, err := s.services.Realtime.ReleaseLease(ctx, req, ref, input)
	if err != nil {
		idemErr := loomerrors.Wrap("realtime.lease_release_failed", "realtime", ref, "Could not release realtime lease.", err)
		s.failIdempotency(ctx, idemRecord, idemErr.Code, response.FailureWithIdempotency(correlationID, idemRecord.Key, idemErr))
		s.writeError(w, correlationID, http.StatusBadRequest, "realtime.lease_release_failed", "realtime", ref, "Could not release realtime lease.", err)
		return
	}
	envelope := response.SuccessWithIdempotency(correlationID, idemRecord.Key, lease)
	s.completeIdempotency(ctx, idemRecord, "realtime_lease", lease.LeaseID, envelope)
	response.WriteJSON(w, http.StatusOK, envelope)
}

func (s Server) handleAgentAccessSessions(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	switch r.Method {
	case http.MethodGet:
		limit, err := parseLimit(r, 50)
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "agents", "limit", "Limit must be a positive integer.", err)
			return
		}
		query := r.URL.Query()
		sessions, err := s.services.Agents.ListAccessSessions(ctx, agents.AccessSessionFilter{
			Limit:    limit,
			ActorRef: query.Get("actor"),
			Status:   query.Get("status"),
		})
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "agents.access_sessions_list_failed", "agents", "access_sessions", "Could not list agent access sessions.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, sessions))
	case http.MethodPost:
		var input agents.CreateAccessSessionInput
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "agents", "body", "Request body is not valid agent access session JSON.", err)
			return
		}
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "agents", "bootstrap", "Could not resolve request context.", err)
			return
		}
		session, err := s.services.Agents.CreateAccessSession(ctx, req, input)
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "agents.access_session_create_failed", "agents", input.ActorRef, "Could not create agent access session.", err)
			return
		}
		response.WriteJSON(w, http.StatusCreated, response.Success(correlationID, session))
	default:
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "agents", r.URL.Path, "Method is not allowed.", nil)
	}
}

func (s Server) handleAgentAccessSession(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "agents", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	ref := pathRef(r.URL.Path, "/v1/agents/access-sessions/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "agents.access_session_ref_required", "agents", "access_session", "Agent access session reference is required.", nil)
		return
	}
	session, err := s.services.Agents.GetAccessSession(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "agents", ref, "Agent access session was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, session))
}

func (s Server) handleAgentWorkContexts(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	switch r.Method {
	case http.MethodGet:
		limit, err := parseLimit(r, 50)
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "agents", "limit", "Limit must be a positive integer.", err)
			return
		}
		query := r.URL.Query()
		contexts, err := s.services.Agents.ListWorkContexts(ctx, agents.WorkContextFilter{
			Limit:            limit,
			ActorRef:         query.Get("actor"),
			AccessSessionRef: query.Get("access_session"),
			Status:           query.Get("status"),
		})
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "agents.work_contexts_list_failed", "agents", "work_contexts", "Could not list agent work contexts.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, contexts))
	case http.MethodPost:
		var input agents.CreateWorkContextInput
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "agents", "body", "Request body is not valid agent work context JSON.", err)
			return
		}
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "agents", "bootstrap", "Could not resolve request context.", err)
			return
		}
		detail, err := s.services.Agents.CreateWorkContext(ctx, req, input)
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "agents.work_context_create_failed", "agents", input.AccessSessionRef, "Could not create agent work context.", err)
			return
		}
		response.WriteJSON(w, http.StatusCreated, response.Success(correlationID, detail))
	default:
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "agents", r.URL.Path, "Method is not allowed.", nil)
	}
}

func (s Server) handleAgentWorkContext(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if workContextRef, ok := agentWorkContextWorklogRef(r); ok {
		switch r.Method {
		case http.MethodGet:
			limit, err := parseLimit(r, 50)
			if err != nil {
				s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_limit", "agents", "limit", "Limit must be a positive integer.", err)
				return
			}
			entries, err := s.services.Agents.ListWorklog(ctx, agents.WorklogFilter{WorkContextRef: workContextRef, Limit: limit})
			if err != nil {
				s.writeError(w, correlationID, http.StatusBadRequest, "agents.worklog_list_failed", "agents", workContextRef, "Could not list agent worklog entries.", err)
				return
			}
			response.WriteJSON(w, http.StatusOK, response.Success(correlationID, entries))
		case http.MethodPost:
			var input agents.WriteWorklogInput
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil {
				s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "agents", "body", "Request body is not valid agent worklog JSON.", err)
				return
			}
			input.WorkContextRef = workContextRef
			req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
			if err != nil {
				s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "agents", "bootstrap", "Could not resolve request context.", err)
				return
			}
			entry, err := s.services.Agents.WriteWorklog(ctx, req, input)
			if err != nil {
				s.writeError(w, correlationID, http.StatusBadRequest, "agents.worklog_write_failed", "agents", workContextRef, "Could not write agent worklog entry.", err)
				return
			}
			response.WriteJSON(w, http.StatusCreated, response.Success(correlationID, entry))
		default:
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "agents", r.URL.Path, "Method is not allowed.", nil)
		}
		return
	}
	if workContextRef, toolRef, ok := agentWorkContextToolCallRef(r); ok {
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "agents", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		var input agents.AgentToolCallInput
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "agents", "body", "Request body is not valid agent tool call JSON.", err)
			return
		}
		input.WorkContextRef = workContextRef
		input.ToolRef = toolRef
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "agents", "bootstrap", "Could not resolve request context.", err)
			return
		}
		outcome, err := s.services.Agents.CallTool(ctx, req, input)
		if errors.Is(err, agents.ErrToolNotAvailable) {
			s.writeError(w, correlationID, http.StatusNotFound, "agents.tool_not_available", "agents", toolRef, "Tool is not available in this work context.", err)
			return
		}
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "agents.tool_call_failed", "agents", toolRef, "Could not call agent-visible tool.", err)
			return
		}
		response.WriteJSON(w, http.StatusCreated, response.Success(correlationID, outcome))
		return
	}
	if workContextRef, ok := agentWorkContextToolsSearchRef(r); ok {
		if r.Method != http.MethodPost {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "agents", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		var input agents.ToolSearchInput
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "agents", "body", "Request body is not valid agent tool search JSON.", err)
			return
		}
		input.WorkContextRef = workContextRef
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "agents", "bootstrap", "Could not resolve request context.", err)
			return
		}
		result, err := s.services.Agents.SearchTools(ctx, req, input)
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "agents.tool_search_failed", "agents", workContextRef, "Could not search agent-visible tools.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
		return
	}
	if workContextRef, toolRef, ok := agentWorkContextToolInspectRef(r); ok {
		if r.Method != http.MethodGet {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "agents", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		query := r.URL.Query()
		maxSections, err := parseOptionalIntQuery(query, "max_sections")
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_max_sections", "agents", "max_sections", "max_sections must be an integer.", err)
			return
		}
		maxChars, err := parseOptionalIntQuery(query, "max_chars_per_section")
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_max_chars_per_section", "agents", "max_chars_per_section", "max_chars_per_section must be an integer.", err)
			return
		}
		input := agents.ToolInspectInput{
			WorkContextRef:     workContextRef,
			ToolRef:            toolRef,
			Query:              query.Get("query"),
			UsageSectionLabel:  query.Get("usage_section_label"),
			MaxSections:        maxSections,
			MaxCharsPerSection: maxChars,
		}
		req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
		if err != nil {
			s.writeError(w, correlationID, http.StatusInternalServerError, "request.context_failed", "agents", "bootstrap", "Could not resolve request context.", err)
			return
		}
		result, err := s.services.Agents.InspectTool(ctx, req, input)
		if errors.Is(err, agents.ErrToolNotAvailable) {
			s.writeError(w, correlationID, http.StatusNotFound, "agents.tool_not_available", "agents", toolRef, "Tool is not available in this work context.", err)
			return
		}
		if err != nil {
			s.writeError(w, correlationID, http.StatusBadRequest, "agents.tool_inspect_failed", "agents", toolRef, "Could not inspect agent-visible tool.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "agents", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	ref := pathRef(r.URL.Path, "/v1/agents/work-contexts/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "agents.work_context_ref_required", "agents", "work_context", "Agent work context reference is required.", nil)
		return
	}
	detail, err := s.services.Agents.GetWorkContext(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "agents", ref, "Agent work context was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, detail))
}

func (s Server) handleAgentToolCall(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "agents", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	ref := pathRef(r.URL.Path, "/v1/agents/tool-calls/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "agents.tool_call_ref_required", "agents", "agent_tool_call", "Agent tool call reference is required.", nil)
		return
	}
	call, err := s.services.Agents.GetAgentToolCall(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "agents", ref, "Agent tool call was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, call))
}

func (s Server) handleAgentToolView(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "agents", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	ref := pathRef(r.URL.Path, "/v1/agents/tool-views/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "agents.tool_view_ref_required", "agents", "tool_view", "Agent tool view reference is required.", nil)
		return
	}
	detail, err := s.services.Agents.GetToolView(ctx, ref)
	if err != nil {
		s.writeLookupError(w, correlationID, "agents", ref, "Agent tool view was not found.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, detail))
}

func (s Server) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "sync", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	nodeRef := strings.TrimSpace(r.URL.Query().Get("node"))
	if nodeRef == "" {
		s.writeError(w, correlationID, http.StatusBadRequest, "sync.node_required", "sync", "node", "Node filter is required.", nil)
		return
	}
	status, err := s.services.Sync.GetStatus(ctx, nodeRef)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "sync.status_failed", "sync", nodeRef, "Could not inspect sync status.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, status))
}

func (s Server) handleSyncBatches(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "sync", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	filter, err := s.syncListFilter(r)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "sync.invalid_filter", "sync", "filter", "Sync filter is invalid.", err)
		return
	}
	batches, err := s.services.Sync.ListBatches(ctx, filter)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "sync.batches_failed", "sync", "batches", "Could not list sync batches.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, batches))
}

func (s Server) handleSyncConflicts(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "sync", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	filter, err := s.syncListFilter(r)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "sync.invalid_filter", "sync", "filter", "Sync filter is invalid.", err)
		return
	}
	conflicts, err := s.services.Sync.ListConflicts(ctx, filter)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "sync.conflicts_failed", "sync", "conflicts", "Could not list sync conflicts.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, conflicts))
}

func (s Server) handleSyncReplicas(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "sync", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	filter, err := s.syncListFilter(r)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "sync.invalid_filter", "sync", "filter", "Sync filter is invalid.", err)
		return
	}
	replicas, err := s.services.Sync.ListReplicas(ctx, filter)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "sync.replicas_failed", "sync", "replicas", "Could not list sync replicas.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, replicas))
}

func (s Server) handleSyncPrivateBackups(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "sync", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	filter, err := s.syncListFilter(r)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "sync.invalid_filter", "sync", "filter", "Sync filter is invalid.", err)
		return
	}
	operations, err := s.services.Sync.ListPrivateBackups(ctx, filter)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "sync.private_backups_failed", "sync", "private_backups", "Could not list private backups.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, operations))
}

func (s Server) handleSyncDeletionRequests(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "sync", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	filter, err := s.syncListFilter(r)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "sync.invalid_filter", "sync", "filter", "Sync filter is invalid.", err)
		return
	}
	requests, err := s.services.Sync.ListDeletionRequests(ctx, filter)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "sync.deletion_requests_failed", "sync", "deletion_requests", "Could not list deletion requests.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, requests))
}

func (s Server) handleSyncDeletionRequest(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	ref := pathRef(r.URL.Path, "/v1/sync/deletion-requests/")
	if ref == "" {
		s.writeError(w, correlationID, http.StatusNotFound, "sync.deletion_request_required", "sync", "deletion_request", "Deletion request reference is required.", nil)
		return
	}
	action := ""
	for _, suffix := range []string{"/review", "/approve", "/deny", "/complete"} {
		if strings.HasSuffix(ref, suffix) {
			action = strings.TrimPrefix(suffix, "/")
			ref = strings.Trim(strings.TrimSuffix(ref, suffix), "/")
			break
		}
	}
	if action == "" {
		if r.Method != http.MethodGet {
			s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "sync", r.URL.Path, "Method is not allowed.", nil)
			return
		}
		request, err := s.services.Sync.GetDeletionRequest(ctx, ref)
		if err != nil {
			s.writeLookupError(w, correlationID, "sync", ref, "Deletion request was not found.", err)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success(correlationID, request))
		return
	}
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "sync", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	input, ok := decodeDeletionRequestUpdateInput(w, r, s, correlationID, ref, action)
	if !ok {
		return
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.services.DB, correlationID)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "request_context.unavailable", "runtime", "bootstrap", "Could not resolve request context.", err)
		return
	}
	var request loomsync.DeletionRequest
	switch action {
	case "review":
		request, err = s.services.Sync.ReviewDeletionRequest(ctx, req, input)
	case "approve":
		request, err = s.services.Sync.ApproveDeletionRequest(ctx, req, input)
	case "deny":
		request, err = s.services.Sync.DenyDeletionRequest(ctx, req, input)
	case "complete":
		request, err = s.services.Sync.CompleteDeletionRequest(ctx, req, input)
	}
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "sync.deletion_request_update_failed", "sync", ref, "Could not update deletion request.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, request))
}

func decodeDeletionRequestUpdateInput(w http.ResponseWriter, r *http.Request, s Server, correlationID, requestRef, action string) (loomsync.DeletionRequestUpdateInput, bool) {
	var input loomsync.DeletionRequestUpdateInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_json", "sync", "body", "Request body is not valid deletion request "+action+" JSON.", err)
		return loomsync.DeletionRequestUpdateInput{}, false
	}
	input.RequestRef = requestRef
	return input, true
}

func (s Server) handleWatchedRootStatus(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "watched_roots", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	filter, err := watchedRootStatusFilter(r)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "watched_roots.invalid_filter", "watched_roots", "filter", "Watched-root status filter is invalid.", err)
		return
	}
	statuses, err := s.services.WatchedRoots.ListStatus(ctx, filter)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "watched_roots.status_failed", "watched_roots", "status", "Could not list watched-root status.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, statuses))
}

func (s Server) handleWatchedRootFindings(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "watched_roots", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	filter, err := watchedRootFindingFilter(r)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "watched_roots.invalid_filter", "watched_roots", "filter", "Watched-root finding filter is invalid.", err)
		return
	}
	findings, err := s.services.WatchedRoots.ListFindings(ctx, filter)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "watched_roots.findings_failed", "watched_roots", "findings", "Could not list watched-root findings.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, findings))
}

func (s Server) handleWatchedRootBackupStatus(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "watched_roots", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	filter, err := watchedRootBackupFilter(r)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "watched_roots.invalid_filter", "watched_roots", "filter", "Watched-root backup filter is invalid.", err)
		return
	}
	status, err := s.services.WatchedRoots.GetBackupStatus(ctx, filter)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.writeWatchedRootBackupTargetNotFound(w, correlationID, err)
		} else {
			s.writeError(w, correlationID, http.StatusInternalServerError, "watched_roots.backup_status_failed", "watched_roots", "status", "Could not get watched-root backup status.", err)
		}
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, status))
}

// Keep the missing-target advice read-only and independent of untrusted filter
// text. Unknown selectors and absent reporting share this exact category.
func (s Server) writeWatchedRootBackupTargetNotFound(w http.ResponseWriter, correlationID string, cause error) {
	const code = "watched_roots.backup_target_not_found"
	err := loomerrors.Wrap(code, "watched_roots", "status", "No matching reported watched root exists for these filters.", cause)
	envelope := response.Failure(correlationID, err)
	envelope.Error.Hint = "Inspect loom watched-roots status with the same filters. For a known declared project, use loom project plan <project> to inspect enrollment prerequisites."
	s.logger.Warn("request failed",
		slog.String("component", "httpapi"), slog.String("correlation_id", correlationID),
		slog.Int("status", http.StatusNotFound), slog.String("code", code),
		slog.String("domain", "watched_roots"), slog.String("target", "status"),
		slog.String("cause", cause.Error()))
	response.WriteJSON(w, http.StatusNotFound, envelope)
}

func (s Server) handleWatchedRootBackupBatches(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "watched_roots", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	filter, err := watchedRootBackupFilter(r)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "watched_roots.invalid_filter", "watched_roots", "filter", "Watched-root backup filter is invalid.", err)
		return
	}
	batches, err := s.services.WatchedRoots.ListBackupBatches(ctx, filter)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "watched_roots.backup_batches_failed", "watched_roots", "batches", "Could not list watched-root backup batches.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, batches))
}

func (s Server) handleWatchedRootBackupItems(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "watched_roots", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	filter, err := watchedRootBackupItemFilter(r)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "watched_roots.invalid_filter", "watched_roots", "filter", "Watched-root backup item filter is invalid.", err)
		return
	}
	items, err := s.services.WatchedRoots.ListBackupItems(ctx, filter)
	if err != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "watched_roots.backup_items_failed", "watched_roots", "items", "Could not list watched-root backup items.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, items))
}

func (s Server) syncListFilter(r *http.Request) (loomsync.ListFilter, error) {
	limit, err := parseLimit(r, 50)
	if err != nil {
		return loomsync.ListFilter{}, err
	}
	query := r.URL.Query()
	return loomsync.ListFilter{
		NodeRef:         query.Get("node"),
		Status:          query.Get("status"),
		ProjectRef:      query.Get("project"),
		ActiveOnly:      parseBoolQuery(query, "active_only"),
		IncludeResolved: parseBoolQuery(query, "include_resolved"),
		Limit:           limit,
	}, nil
}

func watchedRootStatusFilter(r *http.Request) (mainwatchedroots.StatusFilter, error) {
	limit, err := parseLimit(r, 50)
	if err != nil {
		return mainwatchedroots.StatusFilter{}, err
	}
	query := r.URL.Query()
	return mainwatchedroots.StatusFilter{
		NodeRef:    strings.TrimSpace(query.Get("node")),
		RootKey:    strings.TrimSpace(query.Get("root")),
		ProjectRef: strings.TrimSpace(query.Get("project")),
		Status:     strings.TrimSpace(query.Get("status")),
		Limit:      limit,
	}, nil
}

func watchedRootFindingFilter(r *http.Request) (mainwatchedroots.FindingFilter, error) {
	limit, err := parseLimit(r, 50)
	if err != nil {
		return mainwatchedroots.FindingFilter{}, err
	}
	query := r.URL.Query()
	return mainwatchedroots.FindingFilter{
		NodeRef:    strings.TrimSpace(query.Get("node")),
		RootKey:    strings.TrimSpace(query.Get("root")),
		ProjectRef: strings.TrimSpace(query.Get("project")),
		Status:     strings.TrimSpace(query.Get("status")),
		Severity:   strings.TrimSpace(query.Get("severity")),
		Limit:      limit,
	}, nil
}

func watchedRootBackupFilter(r *http.Request) (mainwatchedroots.BackupFilter, error) {
	limit, err := parseLimit(r, 50)
	if err != nil {
		return mainwatchedroots.BackupFilter{}, err
	}
	query := r.URL.Query()
	return mainwatchedroots.BackupFilter{
		NodeRef:    strings.TrimSpace(query.Get("node")),
		RootKey:    strings.TrimSpace(query.Get("root")),
		ProjectRef: strings.TrimSpace(query.Get("project")),
		Status:     strings.TrimSpace(query.Get("status")),
		Limit:      limit,
	}, nil
}

func watchedRootBackupItemFilter(r *http.Request) (mainwatchedroots.BackupItemFilter, error) {
	limit, err := parseLimit(r, 50)
	if err != nil {
		return mainwatchedroots.BackupItemFilter{}, err
	}
	query := r.URL.Query()
	return mainwatchedroots.BackupItemFilter{
		NodeRef:    strings.TrimSpace(query.Get("node")),
		RootKey:    strings.TrimSpace(query.Get("root")),
		ProjectRef: strings.TrimSpace(query.Get("project")),
		Status:     strings.TrimSpace(query.Get("status")),
		BatchRef:   strings.TrimSpace(query.Get("batch")),
		Path:       strings.TrimSpace(query.Get("path")),
		Limit:      limit,
	}, nil
}

func (s Server) eventFilter(ctx context.Context, r *http.Request) (events.ListFilter, error) {
	limit, err := parseLimit(r, 50)
	if err != nil {
		return events.ListFilter{}, err
	}

	query := r.URL.Query()
	filter := events.ListFilter{
		Limit:         limit,
		EventType:     query.Get("type"),
		CorrelationID: query.Get("correlation"),
		JobRef:        query.Get("job"),
		TargetKind:    query.Get("target_kind"),
		TargetID:      query.Get("target_id"),
	}

	if actorRef := strings.TrimSpace(query.Get("actor")); actorRef != "" {
		actorID, err := s.services.Identity.ResolveActorRef(ctx, actorRef)
		if err != nil {
			return events.ListFilter{}, fmt.Errorf("resolve actor filter %q: %w", actorRef, err)
		}
		filter.ActorRef = actorID
	}
	if nodeRef := strings.TrimSpace(query.Get("node")); nodeRef != "" {
		nodeID, err := s.services.Nodes.ResolveNodeRef(ctx, nodeRef)
		if err != nil {
			return events.ListFilter{}, fmt.Errorf("resolve node filter %q: %w", nodeRef, err)
		}
		filter.NodeRef = nodeID
	}
	if scopeRef := strings.TrimSpace(query.Get("scope")); scopeRef != "" {
		scopeID, err := s.services.Scopes.ResolveScopeRef(ctx, scopeRef)
		if err != nil {
			return events.ListFilter{}, fmt.Errorf("resolve scope filter %q: %w", scopeRef, err)
		}
		filter.ScopeRef = scopeID
	}

	return filter, nil
}

func (s Server) beginIdempotency(w http.ResponseWriter, r *http.Request, ctx context.Context, correlationID string, req requestctx.Context, operation string, input any) (idempotency.Record, bool) {
	return s.beginIdempotencyWithRecovery(w, r, ctx, correlationID, req, operation, input, false)
}

func (s Server) beginRecoverableIdempotency(w http.ResponseWriter, r *http.Request, ctx context.Context, correlationID string, req requestctx.Context, operation string, input any) (idempotency.Record, bool) {
	return s.beginIdempotencyWithRecovery(w, r, ctx, correlationID, req, operation, input, true)
}

func (s Server) beginIdempotencyWithRecovery(w http.ResponseWriter, r *http.Request, ctx context.Context, correlationID string, req requestctx.Context, operation string, input any, resumeInProgress bool) (idempotency.Record, bool) {
	key := strings.TrimSpace(r.Header.Get(idempotency.Header))
	if key == "" {
		return idempotency.Record{}, true
	}
	if s.services.Idempotency == nil {
		s.writeErrorWithIdempotency(w, correlationID, key, http.StatusServiceUnavailable, "idempotency.unavailable", "interface", key, "Idempotency service is unavailable.", nil)
		return idempotency.Record{}, false
	}

	result, err := s.services.Idempotency.Begin(ctx, idempotency.BeginInput{
		ActorID:   req.ActorID,
		NodeID:    req.OriginNodeID,
		Key:       key,
		Operation: operation,
		Request:   input,
	})
	if err != nil {
		s.writeErrorWithIdempotency(w, correlationID, key, http.StatusInternalServerError, "idempotency.begin_failed", "interface", key, "Could not begin idempotent request.", err)
		return idempotency.Record{}, false
	}

	switch result.Decision {
	case idempotency.DecisionNew:
		return result.Record, true
	case idempotency.DecisionReplayCompleted, idempotency.DecisionReplayFailed:
		if len(result.Record.ResponseSnapshot) == 0 {
			s.writeErrorWithIdempotency(w, correlationID, key, http.StatusConflict, "idempotency.snapshot_missing", "interface", key, "Idempotent request has no replayable snapshot.", nil)
			return idempotency.Record{}, false
		}
		status := http.StatusOK
		if result.Decision == idempotency.DecisionReplayFailed {
			status = http.StatusConflict
		}
		writeRawJSON(w, status, result.Record.ResponseSnapshot)
		return idempotency.Record{}, false
	case idempotency.DecisionConflict:
		s.writeErrorWithIdempotency(w, correlationID, key, http.StatusConflict, "idempotency.conflict", "interface", key, "Idempotency key was already used with a different request.", nil)
		return idempotency.Record{}, false
	case idempotency.DecisionExpired:
		s.writeErrorWithIdempotency(w, correlationID, key, http.StatusConflict, "idempotency.expired", "interface", key, "Idempotency key has expired.", nil)
		return idempotency.Record{}, false
	case idempotency.DecisionInProgress:
		if resumeInProgress {
			return result.Record, true
		}
		s.writeErrorWithIdempotency(w, correlationID, key, http.StatusConflict, "idempotency.in_progress", "interface", key, "Idempotency key is already in progress.", nil)
		return idempotency.Record{}, false
	default:
		s.writeErrorWithIdempotency(w, correlationID, key, http.StatusInternalServerError, "idempotency.decision_invalid", "interface", key, "Idempotency service returned an invalid decision.", nil)
		return idempotency.Record{}, false
	}
}

func (s Server) completeIdempotency(ctx context.Context, record idempotency.Record, resultKind, resultRef string, envelope any) {
	if record.IdempotencyID == "" {
		return
	}
	if s.services.Idempotency == nil {
		return
	}
	if err := s.services.Idempotency.Complete(ctx, record.IdempotencyID, resultKind, resultRef, envelope); err != nil {
		s.logger.Warn("idempotency completion failed",
			slog.String("component", "idempotency"),
			slog.String("idempotency_id", record.IdempotencyID),
			slog.String("key", record.Key),
			slog.String("error", err.Error()),
		)
	}
}

func (s Server) failIdempotency(ctx context.Context, record idempotency.Record, code string, envelope any) {
	if record.IdempotencyID == "" {
		return
	}
	if s.services.Idempotency == nil {
		return
	}
	if err := s.services.Idempotency.Fail(ctx, record.IdempotencyID, code, envelope); err != nil {
		s.logger.Warn("idempotency failure record failed",
			slog.String("component", "idempotency"),
			slog.String("idempotency_id", record.IdempotencyID),
			slog.String("key", record.Key),
			slog.String("error", err.Error()),
		)
	}
}

type objectIngestIdempotencyPayload struct {
	objects.IngestFileInput
	SourceSizeBytes int64  `json:"source_size_bytes,omitempty"`
	SourceModTime   string `json:"source_mod_time,omitempty"`
}

func objectIngestIdempotencyRequest(input objects.IngestFileInput) any {
	info, err := os.Stat(input.Path)
	if err != nil {
		return input
	}
	return objectIngestIdempotencyPayload{
		IngestFileInput: input,
		SourceSizeBytes: info.Size(),
		SourceModTime:   info.ModTime().UTC().Format(time.RFC3339Nano),
	}
}

func writeRawJSON(w http.ResponseWriter, status int, payload json.RawMessage) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

func requestMeta(r *http.Request) (string, context.Context) {
	correlationID := correlation.Normalize(r.Header.Get(correlation.Header))
	return correlationID, correlation.WithContext(r.Context(), correlationID)
}

func parseLimit(r *http.Request, defaultLimit int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return defaultLimit, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		return 0, fmt.Errorf("invalid limit: %q", raw)
	}
	if limit > 200 {
		limit = 200
	}
	return limit, nil
}

func parseBoolQuery(values url.Values, key string) bool {
	raw := strings.ToLower(strings.TrimSpace(values.Get(key)))
	return raw == "1" || raw == "true" || raw == "yes"
}

func parseOptionalIntQuery(values url.Values, key string) (int, error) {
	raw := strings.TrimSpace(values.Get(key))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	return value, nil
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func realtimeActionRef(path, prefix, action string) (string, bool) {
	suffix := "/" + action
	trimmed := strings.TrimSuffix(strings.TrimRight(path, "/"), suffix)
	if trimmed == strings.TrimRight(path, "/") {
		return "", false
	}
	return pathRef(trimmed, prefix), true
}

func agentWorkContextToolsSearchRef(r *http.Request) (string, bool) {
	path := strings.TrimRight(r.URL.EscapedPath(), "/")
	const prefix = "/v1/agents/work-contexts/"
	const suffix = "/tools/search"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	ref := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if ref == "" || strings.Contains(ref, "/") {
		return "", false
	}
	if decoded, err := url.PathUnescape(ref); err == nil {
		ref = decoded
	}
	return ref, ref != ""
}

func agentWorkContextWorklogRef(r *http.Request) (string, bool) {
	path := strings.TrimRight(r.URL.EscapedPath(), "/")
	const prefix = "/v1/agents/work-contexts/"
	const suffix = "/worklog"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	ref := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if ref == "" || strings.Contains(ref, "/") {
		return "", false
	}
	if decoded, err := url.PathUnescape(ref); err == nil {
		ref = decoded
	}
	return ref, ref != ""
}

func agentWorkContextToolCallRef(r *http.Request) (string, string, bool) {
	path := strings.TrimRight(r.URL.EscapedPath(), "/")
	const prefix = "/v1/agents/work-contexts/"
	const marker = "/tools/"
	const suffix = "/call"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", "", false
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	markerIndex := strings.Index(inner, marker)
	if markerIndex < 0 {
		return "", "", false
	}
	workContextRef := inner[:markerIndex]
	toolRef := inner[markerIndex+len(marker):]
	if workContextRef == "" || toolRef == "" || strings.Contains(workContextRef, "/") {
		return "", "", false
	}
	if decoded, err := url.PathUnescape(workContextRef); err == nil {
		workContextRef = decoded
	}
	if decoded, err := url.PathUnescape(toolRef); err == nil {
		toolRef = decoded
	}
	return workContextRef, toolRef, workContextRef != "" && toolRef != ""
}

func agentWorkContextToolInspectRef(r *http.Request) (string, string, bool) {
	path := strings.TrimRight(r.URL.EscapedPath(), "/")
	const prefix = "/v1/agents/work-contexts/"
	const marker = "/tools/"
	const suffix = "/inspect"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", "", false
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	markerIndex := strings.Index(inner, marker)
	if markerIndex < 0 {
		return "", "", false
	}
	workContextRef := inner[:markerIndex]
	toolRef := inner[markerIndex+len(marker):]
	if workContextRef == "" || toolRef == "" || strings.Contains(workContextRef, "/") {
		return "", "", false
	}
	if decoded, err := url.PathUnescape(workContextRef); err == nil {
		workContextRef = decoded
	}
	if decoded, err := url.PathUnescape(toolRef); err == nil {
		toolRef = decoded
	}
	return workContextRef, toolRef, workContextRef != "" && toolRef != ""
}

func pathRef(path, prefix string) string {
	ref := strings.TrimPrefix(path, prefix)
	ref = strings.Trim(ref, "/")
	if decoded, err := url.PathUnescape(ref); err == nil {
		ref = decoded
	}
	return ref
}

func splitDropzoneSessionAction(ref string) (string, string) {
	ref = strings.Trim(ref, "/")
	if ref == "" {
		return "", ""
	}
	parts := strings.Split(ref, "/")
	sessionID := strings.TrimSpace(parts[0])
	if len(parts) == 1 {
		return sessionID, ""
	}
	return sessionID, strings.TrimSpace(parts[1])
}

func parseModuleCapabilityAction(ref string) (installationRef, capabilityRef, action string, ok bool) {
	marker := "/capabilities/"
	idx := strings.Index(ref, marker)
	if idx < 0 {
		return "", "", "", false
	}
	installationRef = strings.Trim(ref[:idx], "/")
	rest := strings.Trim(ref[idx+len(marker):], "/")
	capabilityRef, action, ok = strings.Cut(rest, "/")
	if !ok {
		return "", "", "", false
	}
	action = strings.Trim(action, "/")
	if installationRef == "" || capabilityRef == "" || (action != "expose" && action != "disable") || strings.Contains(action, "/") {
		return "", "", "", false
	}
	return installationRef, capabilityRef, action, true
}

func (s Server) writeLookupError(w http.ResponseWriter, correlationID, domain, target, summary string, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		s.writeError(w, correlationID, http.StatusNotFound, domain+".not_found", domain, target, summary, err)
		return
	}
	s.writeError(w, correlationID, http.StatusInternalServerError, domain+".lookup_failed", domain, target, "Lookup failed.", err)
}

func (s Server) writeWorkerError(w http.ResponseWriter, correlationID, target, summary string, err error) {
	switch {
	case projects.IsProjectRuntimeArchived(err):
		s.writeProjectRuntimeArchivedError(w, correlationID, "workers", target, err)
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, workers.ErrNotFound):
		s.writeError(w, correlationID, http.StatusNotFound, "workers.not_found", "workers", target, summary, err)
	case errors.Is(err, workers.ErrConflict):
		s.writeError(w, correlationID, http.StatusConflict, "workers.conflict", "workers", target, summary, err)
	case errors.Is(err, workers.ErrInvalid), errors.Is(err, workers.ErrAmbiguous):
		s.writeError(w, correlationID, http.StatusBadRequest, "workers.invalid", "workers", target, summary, err)
	default:
		s.writeError(w, correlationID, http.StatusInternalServerError, "workers.failed", "workers", target, summary, err)
	}
}

func (s Server) writeMaintenanceError(w http.ResponseWriter, correlationID, target, summary string, err error) {
	switch {
	case errors.Is(err, maintenance.ErrNotFound), errors.Is(err, sql.ErrNoRows):
		s.writeError(w, correlationID, http.StatusNotFound, "maintenance.not_found", "maintenance", target, summary, err)
	case errors.Is(err, maintenance.ErrInvalid):
		s.writeError(w, correlationID, http.StatusBadRequest, "maintenance.invalid", "maintenance", target, summary, err)
	default:
		s.writeError(w, correlationID, http.StatusInternalServerError, "maintenance.failed", "maintenance", target, summary, err)
	}
}

func (s Server) writeError(w http.ResponseWriter, correlationID string, status int, code, domain, target, summary string, cause error) {
	s.writeErrorWithIdempotency(w, correlationID, "", status, code, domain, target, summary, cause)
}

func (s Server) writeErrorWithIdempotency(w http.ResponseWriter, correlationID, idempotencyKey string, status int, code, domain, target, summary string, cause error) {
	err := loomerrors.Wrap(code, domain, target, summary, cause)
	attrs := []any{
		slog.String("component", "httpapi"),
		slog.String("correlation_id", correlationID),
		slog.Int("status", status),
		slog.String("code", code),
		slog.String("domain", domain),
		slog.String("target", target),
	}
	if cause != nil {
		attrs = append(attrs, slog.String("cause", cause.Error()))
	}
	s.logger.Warn("request failed", attrs...)
	if idempotencyKey != "" {
		response.WriteJSON(w, status, response.FailureWithIdempotency(correlationID, idempotencyKey, err))
		return
	}
	response.WriteJSON(w, status, response.Failure(correlationID, err))
}

func ServeUnix(ctx context.Context, socketPath string, handler http.Handler, logger *slog.Logger) error {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		return err
	}
	if err := removeStaleSocket(socketPath); err != nil {
		return err
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(socketPath, 0o660); err != nil {
		_ = listener.Close()
		return err
	}

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()

	logger.Info("loomd listening",
		slog.String("component", "httpapi"),
		slog.String("socket_path", socketPath),
	)

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		err := <-errCh
		_ = os.Remove(socketPath)
		return err
	case err := <-errCh:
		_ = os.Remove(socketPath)
		return err
	}
}

func ServeTCP(ctx context.Context, listenAddr string, handler http.Handler, logger *slog.Logger) error {
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return err
	}

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()

	logger.Info("loomd listening",
		slog.String("component", "httpapi"),
		slog.String("listen_addr", listener.Addr().String()),
	)

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return <-errCh
	case err := <-errCh:
		return err
	}
}

func removeStaleSocket(socketPath string) error {
	info, err := os.Lstat(socketPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return &os.PathError{Op: "remove", Path: socketPath, Err: errors.New("existing path is not a Unix socket")}
	}
	return os.Remove(socketPath)
}
