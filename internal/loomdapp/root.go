package loomdapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/agents"
	"loom.local/loom/internal/artifacts"
	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/backupcoverage"
	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/box"
	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/cloudstorage"
	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/db"
	"loom.local/loom/internal/dropzone"
	"loom.local/loom/internal/events"
	"loom.local/loom/internal/filetransfer"
	"loom.local/loom/internal/health"
	"loom.local/loom/internal/hermesschedules"
	"loom.local/loom/internal/httpapi"
	"loom.local/loom/internal/idempotency"
	"loom.local/loom/internal/identity"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/logging"
	"loom.local/loom/internal/mainstorage"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/minidashboard"
	"loom.local/loom/internal/modules"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/notesworkspace"
	"loom.local/loom/internal/notesworkspacesync"
	"loom.local/loom/internal/objects"
	"loom.local/loom/internal/objectstore"
	"loom.local/loom/internal/policy"
	"loom.local/loom/internal/projectactivation"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/realtime"
	"loom.local/loom/internal/requestctx"
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
	"loom.local/loom/internal/version"
	mainwatchedroots "loom.local/loom/internal/watchedroots"
	"loom.local/loom/internal/workers"
	workerruntimes "loom.local/loom/internal/workers/runtimes"
	"loom.local/loom/internal/workspacefiles"
)

type options struct {
	config config.Overrides
}

type storageServiceRoots struct {
	Storage                 string
	CanonicalUserBackups    string
	UserBackups             string
	Archive                 string
	ArchiveRuntimeManifests string
}

type mainDatabaseCloser interface {
	Close() error
}

type provenanceRuntimeCloser interface {
	Close()
}

func closeRuntimeDatabases(mainDB mainDatabaseCloser, provenanceRuntime provenanceRuntimeCloser) error {
	if provenanceRuntime != nil {
		provenanceRuntime.Close()
	}
	if mainDB != nil {
		return mainDB.Close()
	}
	return nil
}

func runtimeStorageRoots(cfg config.Config) storageServiceRoots {
	archive := filepath.Clean(strings.TrimSpace(cfg.ArchiveRoot))
	return storageServiceRoots{
		Storage:                 filepath.Clean(strings.TrimSpace(cfg.StorageRoot)),
		CanonicalUserBackups:    filepath.Clean(strings.TrimSpace(cfg.EffectiveCanonicalUserBackupsRoot())),
		UserBackups:             filepath.Clean(strings.TrimSpace(cfg.UserBackupsRoot)),
		Archive:                 archive,
		ArchiveRuntimeManifests: filepath.Join(archive, "runtime-manifests"),
	}
}

func workspaceArchiveRuntime(cfg config.Config, catalog storagecatalog.Service, credentialsDirectory string) (*storagearchive.WorkspaceMoveService, error) {
	if !cfg.WorkspaceArchiveEnabled {
		return nil, nil
	}
	provider, err := storagearchive.NewSystemdWorkspaceManifestKeyProvider(credentialsDirectory, cfg.WorkspaceArchiveManifestKeyID)
	if err != nil {
		return nil, err
	}
	key, err := provider.Lookup(context.Background(), cfg.WorkspaceArchiveManifestKeyID)
	if err != nil {
		return nil, err
	}
	for index := range key {
		key[index] = 0
	}
	return &storagearchive.WorkspaceMoveService{
		Roots: storagearchive.TrustedWorkspaceRoots{
			BoxRoot:     filepath.Clean(strings.TrimSpace(cfg.BoxPath)),
			StorageRoot: filepath.Clean(strings.TrimSpace(cfg.StorageRoot)),
		},
		Catalog:         catalog,
		Journal:         catalog,
		ManifestKeyID:   cfg.WorkspaceArchiveManifestKeyID,
		ManifestKey:     provider.Lookup,
		NamedPlacement:  true,
		CurrentLocation: catalog.ResolveCustodyLocation,
	}, nil
}

func projectArchiveQuiescenceRuntime(service routing.Service) storagearchive.ProjectRuntimeQuiescenceVerifier {
	return storagearchive.NewRoutedProjectRuntimeQuiescenceVerifier(service)
}

func NewRootCommand() *cobra.Command {
	opts := &options{}

	cmd := &cobra.Command{
		Use:          "loomd",
		Short:        "Run the LOOM daemon",
		SilenceUsage: true,
	}

	cmd.PersistentFlags().StringVar(&opts.config.ConfigFile, "config", "", "path to LOOM config file")
	cmd.PersistentFlags().StringVar(&opts.config.Env, "env", "", "LOOM environment name")
	cmd.PersistentFlags().StringVar(&opts.config.NodeID, "node-id", "", "LOOM node id")
	cmd.PersistentFlags().StringVar(&opts.config.NodeKind, "node-kind", "", "LOOM node kind")
	cmd.PersistentFlags().StringVar(&opts.config.NodeRole, "node-role", "", "LOOM node role")
	cmd.PersistentFlags().StringVar(&opts.config.RuntimeClass, "runtime-class", "", "LOOM runtime class")
	cmd.PersistentFlags().StringVar(&opts.config.DataDir, "data-dir", "", "LOOM data directory")
	cmd.PersistentFlags().StringVar(&opts.config.ObjectStore, "object-store", "", "LOOM object-store directory")
	cmd.PersistentFlags().StringVar(&opts.config.StorageRetention, "storage-retention-root", "", "LOOM storage retention root")
	cmd.PersistentFlags().StringVar(&opts.config.MainDocuments, "main-documents-root", "", "LOOM main Documents backing root")
	cmd.PersistentFlags().StringVar(&opts.config.BoxPath, "box-path", "", "LOOM Box path")
	cmd.PersistentFlags().StringVar(&opts.config.BoxProfile, "box-profile", "", "LOOM Box profile")
	cmd.PersistentFlags().StringVar(&opts.config.DBURL, "db-url", "", "PostgreSQL connection URL")
	cmd.PersistentFlags().StringVar(&opts.config.ProvenanceDBURL, "provenance-db-url", "", "dedicated provenance PostgreSQL connection URL")
	cmd.PersistentFlags().StringVar(&opts.config.SocketPath, "socket", "", "Unix socket path")
	cmd.PersistentFlags().StringVar(&opts.config.HTTPListenAddr, "http-listen-addr", "", "optional private HTTP listen address")
	cmd.PersistentFlags().StringVar(&opts.config.ServiceAllowlist, "service-allowlist", "", "reviewed target-node service allowlist path")
	cmd.PersistentFlags().StringVar(&opts.config.LogLevel, "log-level", "", "log level: debug, info, warn, error")
	cmd.PersistentFlags().StringVar(&opts.config.MigrationsDir, "migrations-dir", "", "database migrations directory")
	cmd.PersistentFlags().StringVar(&opts.config.AutoMigrate, "auto-migrate", "", "apply migrations before serving: true or false")
	cmd.PersistentFlags().StringVar(&opts.config.BootstrapDev, "bootstrap-dev", "", "ensure deterministic development bootstrap records: true or false")
	cmd.PersistentFlags().StringVar(&opts.config.BootstrapMode, "bootstrap-mode", "", "bootstrap mode: none, dev, production")

	cmd.AddCommand(newServeCommand(opts))
	cmd.AddCommand(newMigrateCommand(opts))
	cmd.AddCommand(newVersionCommand())

	return cmd
}

func newServeCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the LOOM daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(opts.config)
			if err != nil {
				return err
			}

			logger := logging.New(cfg.LogLevel, os.Stdout)
			logger.Info("starting loomd",
				slog.String("component", "loomd"),
				slog.String("service", "loomd"),
				slog.String("version", version.Current().Version),
				slog.String("env", cfg.Env),
				slog.String("node_id", cfg.NodeID),
				slog.String("node_kind", cfg.NodeKind),
				slog.String("node_role", cfg.NodeRole),
				slog.String("runtime_class", cfg.RuntimeClass),
				slog.String("socket_path", cfg.SocketPath),
				slog.String("http_listen_addr", cfg.HTTPListenAddr),
				slog.String("data_dir", cfg.DataDir),
				slog.String("object_store", cfg.ObjectStore),
				slog.String("deprecated_storage_export", cfg.StorageExport),
				slog.String("storage_retention", cfg.StorageRetention),
				slog.String("main_documents", cfg.MainDocumentsRoot()),
				slog.String("deprecated_main_documents", cfg.MainDocuments),
				slog.String("box_path", cfg.BoxPath),
				slog.String("box_profile", cfg.BoxProfile),
				slog.String("migrations_dir", cfg.MigrationsDir),
				slog.Bool("auto_migrate", cfg.AutoMigrate),
				slog.Bool("bootstrap_dev", cfg.BootstrapDev),
				slog.String("bootstrap_mode", cfg.BootstrapMode),
				slog.Bool("db_configured", cfg.DBURL != ""),
				slog.Bool("provenance_db_configured", cfg.ProvenanceDBURL != ""),
			)

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			if cfg.AutoMigrate {
				result, err := migrations.Up(ctx, cfg.DBURL, cfg.MigrationsDir)
				logger.Info("migration check",
					slog.String("component", "migrations"),
					slog.String("status", result.Status),
					slog.Int64("current_version", result.CurrentVersion),
					slog.Int64("latest_version", result.LatestVersion),
				)
				if err != nil {
					logger.Error("migrations failed",
						slog.String("component", "migrations"),
						slog.String("error", err.Error()),
					)
					return err
				}
			}

			sqlDB, err := db.OpenSQL(ctx, cfg.DBURL)
			if err != nil {
				logger.Error("database connection failed",
					slog.String("component", "db"),
					slog.String("error", err.Error()),
				)
				return err
			}

			provenanceRuntime, provenanceReadiness, err := provenance.OpenRuntime(ctx, cfg.ProvenanceDBURL, cfg.AutoMigrate)
			logger.Info("provenance database readiness",
				slog.String("component", "provenance"),
				slog.String("state", string(provenanceReadiness.State)),
				slog.String("code", string(provenanceReadiness.Code)),
				slog.String("database", provenanceReadiness.Database),
				slog.String("role", provenanceReadiness.Role),
				slog.Int("applied_head", provenanceReadiness.AppliedHead),
				slog.Int("packaged_head", provenanceReadiness.PackagedHead),
			)
			if err != nil {
				_ = sqlDB.Close()
				return err
			}
			provenanceFoundation, err := provenance.NewFoundationAPI(provenanceRuntime)
			if err != nil {
				provenanceRuntime.Close()
				_ = sqlDB.Close()
				return err
			}
			defer func() {
				if err := closeRuntimeDatabases(sqlDB, provenanceRuntime); err != nil {
					logger.Error("database shutdown failed",
						slog.String("component", "db"),
						slog.String("error", err.Error()),
					)
				}
			}()

			bootstrapService := bootstrap.NewService(sqlDB)
			switch cfg.BootstrapMode {
			case "production":
				status, err := bootstrapService.EnsureProductionBootstrap(ctx, productionBootstrapInputFromConfig(cfg))
				if err != nil {
					logger.Error("production bootstrap failed",
						slog.String("component", "bootstrap"),
						slog.String("error", err.Error()),
					)
					return err
				}
				logger.Info("production bootstrap check",
					slog.String("component", "bootstrap"),
					slog.Bool("ready", status.Ready),
					slog.Bool("created", status.Created),
					slog.Int("bootstrap_event_count", status.BootstrapEventCount),
				)
				if !status.Ready {
					return fmt.Errorf("production bootstrap incomplete: missing %v", status.Missing)
				}
			case "dev":
				status, err := bootstrapService.EnsureDevBootstrap(ctx)
				if err != nil {
					logger.Error("bootstrap failed",
						slog.String("component", "bootstrap"),
						slog.String("error", err.Error()),
					)
					return err
				}
				logger.Info("bootstrap check",
					slog.String("component", "bootstrap"),
					slog.Bool("ready", status.Ready),
					slog.Bool("created", status.Created),
					slog.Int("bootstrap_event_count", status.BootstrapEventCount),
				)
				if !status.Ready {
					return fmt.Errorf("bootstrap incomplete: missing %v", status.Missing)
				}
			default:
				if cfg.BootstrapDev {
					status, err := bootstrapService.EnsureDevBootstrap(ctx)
					if err != nil {
						logger.Error("bootstrap failed",
							slog.String("component", "bootstrap"),
							slog.String("error", err.Error()),
						)
						return err
					}
					logger.Info("bootstrap check",
						slog.String("component", "bootstrap"),
						slog.Bool("ready", status.Ready),
						slog.Bool("created", status.Created),
						slog.Int("bootstrap_event_count", status.BootstrapEventCount),
					)
					if !status.Ready {
						return fmt.Errorf("bootstrap incomplete: missing %v", status.Missing)
					}
				}
			}

			healthService := health.NewService(cfg, version.Current())
			store := objectstore.New(cfg.ObjectStore)
			objectService := objects.NewService(sqlDB, store)
			searchService := search.NewService(sqlDB, store, knowledge.CurrentFileBodyDocumentSQL())
			knowledgeService := knowledge.NewService(sqlDB, knowledge.WithNotesSearchBackend(cfg.NotesSearchBackend))
			if err := knowledgeService.PrepareNotesSearchBackend(ctx); err != nil {
				return err
			}
			storageCatalogService := storagecatalog.NewService(sqlDB)
			workspaceArchiveService, workspaceArchiveErr := workspaceArchiveRuntime(cfg, storageCatalogService, os.Getenv("CREDENTIALS_DIRECTORY"))
			if workspaceArchiveErr != nil {
				logger.Warn("workspace archive runtime is not ready",
					slog.String("component", "workspace_archive"),
					slog.String("reason", "manifest credential unavailable or invalid"),
				)
			}
			var workspaceArchiveLifecycle httpapi.WorkspaceArchiveLifecycleService
			var projectWorkspaceMove storagearchive.ProjectWorkspaceArchivePlanner
			if workspaceArchiveService != nil {
				workspaceArchiveLifecycle = workspaceArchiveService
				projectWorkspaceMove = workspaceArchiveService
			}
			storageFidelityService := storagefidelity.NewBackfillService(storageCatalogService)
			storageRoots := runtimeStorageRoots(cfg)
			fileTransferService := filetransfer.NewServiceWithRoots(filetransfer.NewStore(sqlDB), storageCatalogService, cfg.DataDir, storageRoots.Storage, storageRoots.CanonicalUserBackups, storageRoots.UserBackups, cfg.NodeID)
			storageRetentionService := storageretention.NewService(storageCatalogService)
			if load, err := cloudstorage.LoadConfig(cloudstorage.DefaultConfigPath); err == nil && load.Config.Enabled {
				storageRetentionService.MainDocumentCloudCoverage = func(ctx context.Context) (storagecatalog.MainDocumentCloudBackup, error) {
					return cloudstorage.LatestVerifiedRetentionCoverage(ctx, load.Config)
				}
			}
			storageArchiveService := storagearchive.NewService(storageCatalogService, storageRoots.Archive)
			storageArchiveService.SafeToDelete = storageRetentionService
			projectRuntimeArchiveRoot := storageRoots.ArchiveRuntimeManifests
			mainDocumentsRoot := cfg.MainDocumentsRoot()
			mainStorageService := mainstorage.NewService(storageCatalogService, mainstorage.Config{
				BackingRoot:   mainDocumentsRoot,
				LegacyRoot:    cfg.MainDocuments,
				RetentionRoot: cfg.StorageRetention,
				NodeKey:       cfg.NodeID,
			})
			scriptService := scripts.NewService(sqlDB)
			artifactService := artifacts.NewService(sqlDB, objectService, searchService)
			jobService := jobs.NewService(sqlDB, cfg.DataDir, scriptService, objectService, artifactService)
			capabilityService := capabilities.NewService(sqlDB)
			eventService := events.NewService(sqlDB)
			var boxes *boxregistry.Catalog
			if cfg.BoxesFile != "" {
				catalog, catalogErr := cfg.BoxCatalog()
				if catalogErr != nil {
					return fmt.Errorf("load workspace Boxes: %w", catalogErr)
				}
				boxes = &catalog
			}
			projectService := projects.NewService(sqlDB)
			primaryWorkspaceRoots := storagearchive.TrustedWorkspaceRoots{Primary: true, BoxRoot: cfg.BoxPath, StorageRoot: cfg.StorageRoot}
			if boxes != nil {
				projectService = projectService.WithBoxCatalog(*boxes)
				primaryWorkspaceRoots, err = storagearchive.WorkspaceRootsForBox(*boxes, "box")
				if err != nil {
					return fmt.Errorf("bind primary workspace lifecycle: %w", err)
				}
				if workspaceArchiveService != nil {
					workspaceArchiveService.Roots = primaryWorkspaceRoots
				}
			}
			wireWorkspaceActiveLocation(sqlDB, primaryWorkspaceRoots, workspaceArchiveService)
			policyService := policy.NewService(sqlDB)
			maintenanceService := maintenance.NewService(sqlDB)
			maintenanceService.RecoveryDataDir = cfg.DataDir
			seedReq, err := requestctx.ResolveBootstrap(ctx, sqlDB, "corr_bootstrap_capabilities")
			if err != nil {
				logger.Error("capability registry seed context failed",
					slog.String("component", "capabilities"),
					slog.String("error", err.Error()),
				)
				return err
			}
			seedResult, err := capabilities.SeedMainNodeRegistry(ctx, seedReq, capabilityService)
			if err != nil {
				logger.Error("capability registry seed failed",
					slog.String("component", "capabilities"),
					slog.String("error", err.Error()),
				)
				return err
			}
			logger.Info("capability registry seed check",
				slog.String("component", "capabilities"),
				slog.Int("providers_created", seedResult.ProvidersCreated),
				slog.Int("classes_created", seedResult.ClassesCreated),
				slog.Int("endpoints_created", seedResult.EndpointsCreated),
				slog.Int("versions_created", seedResult.VersionsCreated),
				slog.Int("usage_documents_created", seedResult.UsageDocumentsCreated),
			)
			statusService := loomstatus.NewService(sqlDB, healthService)
			runtimeRegistry := routing.NewProviderRuntimeRegistry()
			systemAdapter := routing.NewSystemAdapter(healthService, statusService)
			systemAdapter.AgentEvents = hermesEventAdapter{DB: sqlDB, Config: cfg}
			if err := runtimeRegistry.Register("main@system", systemAdapter); err != nil {
				return err
			}
			if err := runtimeRegistry.Register("main@script-runner", routing.NewScriptRunnerAdapter(jobService, version.Current().Version)); err != nil {
				return err
			}
			if err := runtimeRegistry.RegisterRuntimeExecutor(routing.NewScriptRuntimeExecutor(jobService)); err != nil {
				return err
			}
			if err := runtimeRegistry.RegisterRuntimeExecutor(routing.NewWorkflowRuntimeExecutor(jobService)); err != nil {
				return err
			}
			if err := runtimeRegistry.RegisterRuntimeExecutor(routing.NewCommandRuntimeExecutor()); err != nil {
				return err
			}
			if err := runtimeRegistry.RegisterRuntimeExecutor(routing.NewHTTPRuntimeExecutor()); err != nil {
				return err
			}
			if err := runtimeRegistry.Register("main@loom-project-cockpit", modules.NewProjectCockpitAdapter(sqlDB)); err != nil {
				return err
			}
			routingService := routing.NewServiceWithRuntime(sqlDB, policyService, runtimeRegistry)
			if localNode, err := nodes.NewService(sqlDB).GetNode(ctx, cfg.NodeID); err == nil {
				routingService.RemoteDispatchQueued = func(target string) {
					if target == localNode.NodeID {
						if err := noderuntime.NewStore(filepath.Join(cfg.DataDir, "node-agent")).RequestPoll(); err != nil {
							logger.Warn("local dispatch wake unavailable; periodic delivery remains enabled")
						}
					}
				}
			}
			automationService := automation.NewService(sqlDB, routingService)
			communicationService := communication.NewService(sqlDB)
			syncService := loomsync.NewServiceWithPrivateBackupRoot(sqlDB, storageRoots.UserBackups)
			watchedRootsService := mainwatchedroots.NewServiceWithBackupRoot(sqlDB, storageRoots.UserBackups)
			boxService := box.NewService(sqlDB)
			mainBoxResolved, err := box.Resolve(box.ResolveInput{
				ConfiguredPath:   cfg.BoxPath,
				RuntimeStateRoot: cfg.BoxStateRoot,
				ConfigProfile:    cfg.BoxProfile,
				NodeID:           cfg.NodeID,
				NodeRole:         cfg.NodeRole,
			})
			if err != nil {
				return err
			}
			mainBoxRuntimeState, err := box.ResolveRuntimeState(mainBoxResolved)
			if err != nil {
				return err
			}
			if cfg.NodeRole == "main" {
				if err := storagearchive.PrepareRetainedRoots(cfg.StorageRoot); err != nil {
					return fmt.Errorf("prepare named retained roots: %w", err)
				}
			}
			dropzoneService := dropzone.NewInspectionService(mainBoxResolved.RootPath, mainBoxRuntimeState.ReadRoot)
			realtimeService := realtime.NewService(sqlDB)
			agentsService := agents.NewServiceWithRuntime(sqlDB, routingService, realtimeService)
			moduleService := modules.NewServiceWithDataDir(sqlDB, cfg.DataDir)
			projectActivationService := projectactivation.NewService(projectactivation.Deps{
				Applications: serviceregistry.ApplicationHelperClient{SocketPath: serviceregistry.ApplicationSocketPath},
				Projects:     projectService,
				Scripts:      scriptService,
				Workflows:    jobService.Workflows,
				Capabilities: capabilityService,
				Automation:   automationService,
				Modules:      moduleService,
				Events:       eventService,
				Allowlists:   serviceregistry.FileAllowlistResolver{Path: cfg.ServiceAllowlist},
			})
			projectArchiveService := storagearchive.NewProjectRuntimeService(storagearchive.ProjectRuntimeDeps{
				PauseNativeSchedules: projectHermesArchivePause(func() (config.Config, error) { return config.Load(opts.config) }),
				Projects:             projectService,
				RepositoryState:      projectService,
				Activation:           projectActivationService,
				StorageArchive:       storageArchiveService,
				WorkspaceMove:        projectWorkspaceMove,
				RuntimeQuiescence:    projectArchiveQuiescenceRuntime(routingService),
				WorkspaceRoots:       primaryWorkspaceRoots,
				RuntimeManifestRoot:  projectRuntimeArchiveRoot,
				Now:                  func() time.Time { return time.Now().UTC() },
			})
			projectWatchService := projectwatch.NewService(projectwatch.Deps{
				Projects:     projectService,
				Nodes:        nodes.NewService(sqlDB),
				WatchedRoots: watchedRootsService,
				Sync:         syncService,
			})
			workerRegistry := workers.NewRegistry()
			if err := workerRegistry.Register(workerruntimes.NewSelfcheckRuntime(sqlDB)); err != nil {
				return err
			}
			if err := workerRegistry.Register(workerruntimes.NewPolicyExpiryRuntime(policyService)); err != nil {
				return err
			}
			if err := workerRegistry.Register(workerruntimes.NewRealtimeExpiryRuntime(realtimeService)); err != nil {
				return err
			}
			if err := workerRegistry.Register(workerruntimes.NewDBMaintenanceRuntime(sqlDB, maintenanceService, cfg.DBURL, cfg.MigrationsDir)); err != nil {
				return err
			}
			custodyRead := func(ctx context.Context) (func(), error) {
				return storagecatalog.AcquireWorkspaceCustodyReader(ctx, sqlDB)
			}
			mainBackupRuntime := workerruntimes.NewMainBackupRuntime(sqlDB, maintenanceService, healthService, cfg.DBURL, cfg.DataDir, cfg.ObjectStore, version.Current().Version).WithProvenance(provenanceRuntime, cfg.ProvenanceDBURL).WithBoxCatalog(cfg.BoxCatalog)
			mainBackupRuntime.CustodyRead = custodyRead
			if err := workerRegistry.Register(mainBackupRuntime); err != nil {
				return err
			}
			if err := workerRegistry.Register(workerruntimes.NewProvenanceArchivistRuntime(provenanceFoundation)); err != nil {
				return err
			}
			cloudRuntime := workerruntimes.NewCloudSnapshotUploadRuntime(maintenanceService, cfg.DataDir, cfg.NodeID, backupcoverage.OptionsFromConfig(cfg))
			cloudRuntime.CustodyRead = custodyRead
			if err := workerRegistry.Register(cloudRuntime); err != nil {
				return err
			}
			if err := workerRegistry.Register(workerruntimes.NewObjectStoreIntegrityRuntime(sqlDB, maintenanceService, cfg.ObjectStore)); err != nil {
				return err
			}
			if err := workerRegistry.Register(workerruntimes.NewIndexerTextRuntime(searchService)); err != nil {
				return err
			}
			if err := workerRegistry.Register(workerruntimes.NewKnowledgeIndexerRuntime(knowledgeService).
				WithArchiveService(workspaceArchiveService, cfg.NodeID)); err != nil {
				return err
			}
			if err := workerRegistry.Register(workerruntimes.NewKnowledgeRootsRuntime(knowledgeService)); err != nil {
				return err
			}
			// Keep the low-frequency projection reader's large prepared plans on
			// one stable connection instead of churning with general worker traffic.
			projectionDB, err := db.OpenSQL(ctx, cfg.DBURL)
			if err != nil {
				return err
			}
			defer projectionDB.Close()
			projectionDB.SetMaxOpenConns(1)
			projectionDB.SetMaxIdleConns(1)
			projectionDB.SetConnMaxLifetime(0)
			if err := registerNotesProjection(sqlDB, cfg, knowledgeService, knowledge.NewStore(projectionDB), workerRegistry); err != nil {
				return err
			}
			var visionRuntime knowledge.VisionRuntime
			if cfg.VisionEnabled {
				visionRuntime = knowledge.OllamaVisionRuntime{Endpoint: cfg.VisionOllamaURL, Model: cfg.VisionModel}
			}
			embeddingRuntime, err := knowledge.NewOllamaEmbeddingRuntime(knowledge.OllamaEmbeddingOptions{
				Endpoint: cfg.EmbeddingOllamaURL,
				Model:    cfg.EmbeddingModel,
			})
			if err != nil {
				return err
			}
			if err := workerRegistry.Register(workerruntimes.NewKnowledgeHeavyRuntimeWithRuntimes(knowledgeService, visionRuntime, embeddingRuntime)); err != nil {
				return err
			}
			var notesWorkspace *notesworkspacesync.Runtime
			if cfg.NotesWorkspaceConfig != "" {
				workspaceConfig, err := notesworkspacesync.LoadRuntimeConfig(cfg.NotesWorkspaceConfig)
				if err != nil {
					return err
				}
				notesWorkspace = &notesworkspacesync.Runtime{Config: workspaceConfig, DB: sqlDB, Knowledge: knowledgeService, NodeKey: cfg.NodeID, Lifetime: ctx}
				if workspaceArchiveService != nil {
					notesWorkspace.Archives = &knowledge.WorkspaceArchiveSourceReader{Knowledge: knowledgeService, Workspace: *workspaceArchiveService, NodeKey: cfg.NodeID}
				}
			}
			if err := workerRegistry.Register(workerruntimes.NotesWorkspaceRuntime{Runtime: notesWorkspace}); err != nil {
				return err
			}
			if err := workerRegistry.Register(workerruntimes.NewKnowledgeEmbedderRuntime(knowledgeService, embeddingRuntime)); err != nil {
				return err
			}
			projectDeclarations := newProjectDeclarationService(sqlDB, func() (config.Config, error) { return config.Load(opts.config) })
			if err := workerRegistry.Register(workerruntimes.ApplicationBuilderRuntime{Coordinator: projectDeclarations.ApplicationBuilds}); err != nil {
				return err
			}
			if err := workerRegistry.Register(workerruntimes.NewJobRunnerRuntime(jobService, version.Current().Version)); err != nil {
				return err
			}
			if err := workerRegistry.Register(workerruntimes.NewJobSweeperRuntime(jobService)); err != nil {
				return err
			}
			if err := workerRegistry.Register(workerruntimes.NewAutomationSchedulerRuntime(automationService)); err != nil {
				return err
			}
			if err := workerRegistry.Register(workerruntimes.NewDirectEventIngestRuntime(automationService)); err != nil {
				return err
			}
			if err := workerRegistry.Register(workerruntimes.NewAutomationDispatcherRuntime(automationService)); err != nil {
				return err
			}
			if err := workerRegistry.Register(workerruntimes.NewMainDocumentsImportRuntime(mainStorageService, mainDocumentsRoot)); err != nil {
				return err
			}
			workerService := workers.NewService(sqlDB, workerRegistry, logger)
			workerSeedReq, err := requestctx.ResolveBootstrap(ctx, sqlDB, "corr_bootstrap_workers")
			if err != nil {
				logger.Error("worker seed context failed",
					slog.String("component", "workers"),
					slog.String("error", err.Error()),
				)
				return err
			}
			workerSeedResult, err := workerService.SeedBuiltins(ctx, workerSeedReq, "")
			if err != nil {
				logger.Error("worker seed failed",
					slog.String("component", "workers"),
					slog.String("error", err.Error()),
				)
				return err
			}
			logger.Info("worker seed check",
				slog.String("component", "workers"),
				slog.Int("kinds_created", workerSeedResult.KindsCreated),
				slog.Int("kinds_updated", workerSeedResult.KindsUpdated),
				slog.Int("instances_created", workerSeedResult.InstancesCreated),
				slog.Int("instances_updated", workerSeedResult.InstancesUpdated),
				slog.Int("health_created", workerSeedResult.HealthCreated),
			)
			workerWakes := workers.NewQueueWakes()
			workerWakes.NodeDeliveryQueued = routingService.RemoteDispatchQueued
			if notesWorkspace != nil {
				notesWorkspace.Wake = func() { workerWakes.SignalLocal("notes_workspace_sync") }
				workerWakes.NotesWorkspaceQueued = notesWorkspace.InvalidateEnrollment
				workerWakes.NotesDependencyChanged = notesWorkspace.DependencyChanged
				if err := notesWorkspace.WatchRecovery(ctx); err != nil {
					return err
				}
				notesWorkspace.OpenSourceWatch = func(scope notesworkspacesync.InventoryScope, persist func(context.Context, string) error) (notesworkspacesync.SourceWatch, error) {
					return notesworkspacesync.StartSharedSourceWatch(ctx, filepath.Join(cfg.DataDir, "node-agent"), scope, persist)
				}
				notesWorkspace.SourceRepairInterval = func(scope notesworkspacesync.InventoryScope) time.Duration {
					return notesworkspacesync.SharedSourceRepairInterval(filepath.Join(cfg.DataDir, "node-agent"), scope)
				}
				defer notesWorkspace.CloseSourceWatches()
			}
			nodeService := nodes.NewService(sqlDB)
			miniDashboardService := minidashboard.DomainService{
				Node: minidashboard.NodeIdentity{Key: cfg.NodeID, Label: "MAIN"},
				Deps: minidashboard.DomainDependencies{
					Runtime: func(ctx context.Context) (minidashboard.RuntimeState, error) {
						return minidashboard.RuntimeStatus(ctx, maintenanceService, workerService, jobService, time.Now().UTC())
					},
					Protection: func(ctx context.Context) (minidashboard.ProtectionSnapshot, error) {
						return minidashboard.ProtectionStatus(ctx, maintenanceService, time.Now().UTC())
					},
					Network: func(ctx context.Context) (minidashboard.NetworkState, error) {
						return minidashboard.NetworkStatus(ctx, nodeService, communicationService, cloudstorage.DefaultConfigPath, time.Now().UTC())
					},
					Activity: func(ctx context.Context) (minidashboard.ActivityState, error) {
						return minidashboard.ActivityStatus(ctx, workerService, time.Now().UTC())
					},
				},
			}
			hermesSchedules, err := runtimeHermesSchedules(cfg)
			if err != nil {
				return err
			}
			originalFiles := &workspacefiles.Service{Applications: serviceregistry.ApplicationHelperClient{SocketPath: serviceregistry.ApplicationSocketPath}, ApplicationProjectBoxes: projectService.ApplicationProjectBoxes}
			if boxes != nil {
				for _, b := range boxes.Boxes {
					if b.Available && b.RootRef != "" {
						originalFiles.Roots = append(originalFiles.Roots, workspacefiles.Root{Ref: b.RootRef, BoxID: b.ID, BoxKey: b.Key, Label: b.Label, Node: b.Node, SourceKind: "box", SourcePath: b.RootPath, Writable: true, Verification: "not_checked"})
					}
				}
			}
			for _, root := range []workspacefiles.Root{
				{Ref: "box", Label: "Workspace", Node: cfg.NodeID, SourcePath: mainBoxResolved.RootPath, Verification: "not_checked"},
				{Ref: "storage", Label: "Storage", Node: cfg.NodeID, SourcePath: cfg.StorageRoot, ReadOnly: true, Verification: "not_checked"},
				// Retain the old reference for already-open previews and offline pins.
				{Ref: "archive", Label: "Storage / Archive", Node: cfg.NodeID, SourcePath: cfg.ArchiveRoot, ReadOnly: true, Verification: "not_checked"},
			} {
				if root.Ref == "box" && boxes != nil {
					continue
				}
				if filepath.IsAbs(root.SourcePath) {
					originalFiles.Roots = append(originalFiles.Roots, root)
				}
			}
			defer originalFiles.Close()
			workspaceHost := minidashboard.NewHostCollector(cfg.StorageRoot)
			retainedRelocations := &storagearchive.RetainedRelocationService{Roots: storagearchive.TrustedWorkspaceRoots{BoxRoot: mainBoxResolved.RootPath, StorageRoot: cfg.StorageRoot}, Catalog: storageCatalogService}
			retainedRelocations.Prove = func(ctx context.Context, ref storagearchive.WorkspacePathRef, bound string, inventory storagearchive.NoFollowInventory) (storagearchive.RetainedCustodyProof, error) {
				return retainedRelocations.ProveSupported(ctx, workspaceArchiveService, ref, bound, inventory)
			}
			retainedRelocations.ReadersReady = func(ctx context.Context, plan storagearchive.RetainedRelocationPlan) error {
				if cfg.NodeRole != "main" {
					return fmt.Errorf("retained relocation is owned by Main")
				}
				if !strings.HasPrefix(plan.CustodyID, "legacy-import:") && !strings.HasPrefix(plan.CustodyID, "lane:") && !(strings.HasPrefix(plan.CustodyID, "workspace:") && workspaceArchiveService != nil && workspaceArchiveService.NamedPlacement) {
					return fmt.Errorf("this custody kind still requires its compatible restore/member reader")
				}
				backup, err := workerService.InspectWorker(ctx, "main.main_backup")
				if err != nil {
					return err
				}
				var settings struct {
					Producer string `json:"producer_mode"`
				}
				if json.Unmarshal(backup.Instance.ConfigJSON, &settings) != nil || settings.Producer != "recovery_packages_v1" {
					return fmt.Errorf("legacy retained relocation requires the current operational-package backup owner")
				}
				cloud, err := workerService.InspectWorker(ctx, "main.cloud_snapshot_upload")
				if err != nil {
					return err
				}
				return cloudRuntime.RetainedStorageReady(ctx, cloud.Instance.ConfigJSON, plan.Destination.AbsolutePath)
			}
			workspacePaths := workspacePathRuntime(sqlDB, originalFiles, primaryWorkspaceRoots, cfg.NodeID, projectService, storageCatalogService, workspaceArchiveService)
			if workspacePaths != nil {
				workspacePaths.ReadersReady = func(ctx context.Context, plan storagearchive.WorkspacePathPlan) error {
					if plan.Kind == "relocate_project" {
						return nil
					}
					backup, err := workerService.InspectWorker(ctx, "main.main_backup")
					if err != nil {
						return err
					}
					var settings struct {
						Producer string `json:"producer_mode"`
					}
					if json.Unmarshal(backup.Instance.ConfigJSON, &settings) != nil || settings.Producer != "recovery_packages_v1" {
						return fmt.Errorf("current operational-package backup owner required")
					}
					cloud, err := workerService.InspectWorker(ctx, "main.cloud_snapshot_upload")
					if err != nil {
						return err
					}
					return cloudRuntime.RetainedStorageReady(ctx, cloud.Instance.ConfigJSON, plan.Move.Destination.AbsolutePath)
				}
			}
			boxWorkspacePaths := map[string]*storagearchive.WorkspacePathService{}
			boxWorkspaceArchives := map[string]httpapi.WorkspaceArchiveLifecycleService{}
			boxProjectArchives := map[string]storagearchive.ProjectRuntimeService{}
			if boxes != nil && workspaceArchiveService != nil {
				additionalRoots := additionalBoxArchiveRoots(*boxes, func(binding boxregistry.Box, err error) {
					logger.Warn("additional Box archive lifecycle unavailable", "box_id", binding.ID, "error", err)
				})
				for _, binding := range boxes.Boxes {
					if !binding.Available || binding.ID == "" {
						continue
					}
					if binding.Primary {
						boxWorkspacePaths[binding.ID] = workspacePaths
						boxWorkspaceArchives[binding.ID] = workspaceArchiveService
						boxProjectArchives[binding.ID] = projectArchiveService
						continue
					}
					boundRoots, available := additionalRoots[binding.ID]
					if !available {
						continue
					}
					boundArchive := *workspaceArchiveService
					boundArchive.Roots = boundRoots
					wireWorkspaceActiveLocation(sqlDB, boundRoots, &boundArchive)
					boundPaths := workspacePathRuntime(sqlDB, originalFiles, boundRoots, cfg.NodeID, projectService, storageCatalogService, &boundArchive)
					if boundPaths != nil && workspacePaths != nil {
						boundPaths.ReadersReady = workspacePaths.ReadersReady
					}
					boundProjects := projectArchiveService
					boundProjects.WorkspaceMove = &boundArchive
					boundProjects.WorkspaceRoots = boundRoots
					boundProjects.RuntimeManifestRoot = filepath.Join(projectRuntimeArchiveRoot, "boxes", binding.ID)
					boxWorkspacePaths[binding.ID] = boundPaths
					boxWorkspaceArchives[binding.ID] = &boundArchive
					boxProjectArchives[binding.ID] = boundProjects
				}
			}
			var contextArchiveResolvers []projectContextArchiveForBox
			if boxes != nil {
				contextArchiveResolvers = append(contextArchiveResolvers, func(ctx context.Context, id string) (storagearchive.ProjectRuntimeService, error) {
					if _, err := boxes.Get(id); err != nil {
						return storagearchive.ProjectRuntimeService{}, err
					}
					owner, ok := boxProjectArchives[id]
					if !ok {
						return storagearchive.ProjectRuntimeService{}, fmt.Errorf("archive Box owner is unavailable")
					}
					return owner, nil
				})
			}
			if err := seedProjectContextRefresh(ctx, sqlDB, cfg, provenanceFoundation, workerRegistry, logger, workerSeedReq, projectArchiveService, workerWakes, contextArchiveResolvers...); err != nil {
				return fmt.Errorf("seed project context refresh: %w", err)
			}
			workerSupervisor := workers.NewSupervisor(workerService, logger)
			workerSupervisor.Wakes = workerWakes
			go workerSupervisor.Wakes.Listen(ctx, cfg.DBURL, logger)
			go workerSupervisor.Run(ctx, workerSeedReq)
			workspaceSystemDisk := minidashboard.NewHostCollector(cfg.DataDir)
			workspaceHostConfig := minidashboard.DefaultConfig()
			workspaceHostConfig.HostStaleAfter = 30 * time.Second
			var workspaceHostMu sync.Mutex
			var lastWorkspaceHost minidashboard.HostSnapshot
			var lastWorkspaceDisk minidashboard.Metric
			server := httpapi.NewServer(httpapi.Services{
				DB:                   sqlDB,
				Health:               healthService,
				Status:               statusService,
				Bootstrap:            bootstrapService,
				Box:                  boxService,
				Dropzone:             dropzoneService,
				FileTransfer:         fileTransferService,
				Files:                originalFiles,
				Boxes:                boxes,
				RetainedRelocations:  retainedRelocations,
				WorkspacePaths:       workspacePaths,
				BoxWorkspacePaths:    boxWorkspacePaths,
				BoxWorkspaceArchives: boxWorkspaceArchives,
				BoxProjectArchives:   boxProjectArchives,
				WorkspaceHost: func(ctx context.Context) (minidashboard.HostSnapshot, minidashboard.Metric, error) {
					workspaceHostMu.Lock()
					defer workspaceHostMu.Unlock()
					if err := ctx.Err(); err != nil {
						return minidashboard.HostSnapshot{}, minidashboard.Metric{}, err
					}
					// On-demand, shared sampling; a cold CPU delta is unavailable until the next real sample.
					if lastWorkspaceHost.GeneratedAt.IsZero() || time.Since(lastWorkspaceHost.GeneratedAt) >= time.Second {
						lastWorkspaceHost = workspaceHost.Collect(workspaceHostConfig)
						lastWorkspaceDisk = workspaceSystemDisk.StorageMetric(workspaceHostConfig)
					}
					return lastWorkspaceHost, lastWorkspaceDisk, nil
				},
				Idempotency:           idempotency.NewService(sqlDB),
				Identity:              identity.NewService(sqlDB),
				Nodes:                 nodeService,
				Scopes:                scopes.NewService(sqlDB),
				Events:                eventService,
				Projects:              projectService,
				ProjectActivation:     projectActivationService,
				ProjectWatch:          projectWatchService,
				ProjectArchive:        projectArchiveService,
				NotesRecovery:         notesWorkspace.RecoveryInventory,
				NotesRecoveryPrepare:  notesWorkspace.PrepareRecoveryInventory,
				Objects:               objectService,
				Search:                searchService,
				NotesFileSourceReader: notesworkspace.FileReader{NodeKey: cfg.NodeID},
				StorageArchive:        storageArchiveService,

				WorkspaceArchive:           workspaceArchiveLifecycle,
				WorkspaceArchiveAuthorizer: httpapi.NewPolicyWorkspaceArchiveAuthorizer(policyService),
				WorkspaceArchiveRequestResolver: func(ctx context.Context, correlationID string) (requestctx.Context, error) {
					return requestctx.ResolveBootstrap(ctx, sqlDB, correlationID)
				},

				StorageCatalog:       storageCatalogService,
				StorageFidelity:      storageFidelityService,
				StorageRetention:     storageRetentionService,
				MainStorage:          mainStorageService,
				Scripts:              scriptService,
				Jobs:                 jobService,
				Maintenance:          maintenanceService,
				MiniDashboard:        miniDashboardService,
				Artifacts:            artifactService,
				Automation:           automationService,
				HermesSchedules:      hermesSchedules,
				Capabilities:         capabilityService,
				ServiceAllowlists:    serviceregistry.FileAllowlistResolver{Path: cfg.ServiceAllowlist},
				Policy:               policyService,
				Routing:              routingService,
				Communication:        communicationService,
				Sync:                 syncService,
				WatchedRoots:         watchedRootsService,
				Realtime:             realtimeService,
				Agents:               agentsService,
				Modules:              moduleService,
				Workers:              workerService,
				Provenance:           provenanceFoundation,
				ProvenanceAuthorizer: httpapi.NewPolicyProvenanceAuthorizer(policyService),
				EmbeddingRuntime:     embeddingRuntime,
				RuntimeConfig:        cfg,

				ProjectDeclaration: projectDeclarations,
			}, logger)

			if err := serveConfiguredListeners(ctx, cfg, server.Handler(), logger); err != nil {
				logger.Error("loomd stopped with error",
					slog.String("component", "loomd"),
					slog.String("error", err.Error()),
				)
				return err
			}

			logger.Info("loomd stopped",
				slog.String("component", "loomd"),
			)
			return nil
		},
	}
}

func productionBootstrapInputFromConfig(cfg config.Config) bootstrap.ProductionInput {
	input := bootstrap.DefaultProductionInput()
	if strings.TrimSpace(cfg.NodeID) != "" && cfg.NodeID != config.DefaultNodeID {
		input.NodeKey = strings.TrimSpace(cfg.NodeID)
		input.DisplayName = input.NodeKey
		input.NodeScopeKey = ""
		input.NodeScopeSlug = ""
		input.NodeScopeName = ""
	}
	input.NodeKind = cfg.NodeKind
	input.NodeRole = cfg.NodeRole
	input.RuntimeClass = cfg.RuntimeClass
	input.Metadata = map[string]any{
		"environment":               cfg.Env,
		"box_path":                  cfg.BoxPath,
		"box_profile":               cfg.BoxProfile,
		"deprecated_storage_export": cfg.StorageExport,
		"storage_retention":         cfg.StorageRetention,
		"main_documents":            cfg.MainDocumentsRoot(),
		"deprecated_main_documents": cfg.MainDocuments,
	}
	return bootstrap.NormalizeProductionInput(input)
}

func serveConfiguredListeners(ctx context.Context, cfg config.Config, handler http.Handler, logger *slog.Logger) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	listenerCount := 1
	errCh := make(chan error, 2)

	go func() {
		errCh <- httpapi.ServeUnix(runCtx, cfg.SocketPath, handler, logger)
	}()

	if cfg.HTTPListenAddr != "" {
		listenerCount++
		go func() {
			errCh <- httpapi.ServeTCP(runCtx, cfg.HTTPListenAddr, handler, logger)
		}()
	}

	remaining := listenerCount
	for remaining > 0 {
		select {
		case err := <-errCh:
			remaining--
			if err != nil {
				cancel()
				for remaining > 0 {
					<-errCh
					remaining--
				}
				return err
			}
		case <-ctx.Done():
			cancel()
			var err error
			for remaining > 0 {
				if shutdownErr := <-errCh; shutdownErr != nil && err == nil {
					err = shutdownErr
				}
				remaining--
			}
			return err
		}
	}
	return nil
}

func newMigrateCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Manage LOOM database migrations",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show migration status",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(opts.config)
			if err != nil {
				return err
			}

			result := migrations.Status(cmd.Context(), cfg.DBURL, cfg.MigrationsDir)
			fmt.Fprintf(
				cmd.OutOrStdout(),
				"migrations: %s current=%d latest=%d pending=%d dir=%s\n",
				result.Status,
				result.CurrentVersion,
				result.LatestVersion,
				result.Pending,
				result.Directory,
			)
			if result.Error != "" {
				return errors.New(result.Error)
			}
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "up",
		Short: "Apply all pending migrations",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(opts.config)
			if err != nil {
				return err
			}

			result, err := migrations.Up(cmd.Context(), cfg.DBURL, cfg.MigrationsDir)
			fmt.Fprintf(
				cmd.OutOrStdout(),
				"migrations: %s current=%d latest=%d pending=%d dir=%s\n",
				result.Status,
				result.CurrentVersion,
				result.LatestVersion,
				result.Pending,
				result.Directory,
			)
			return err
		},
	})

	return cmd
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print daemon version",
		Run: func(cmd *cobra.Command, args []string) {
			info := version.Current()
			if info.Commit != "" || info.BuildDate != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "loomd %s commit=%s build_date=%s\n", info.Version, info.Commit, info.BuildDate)
				return
			}
			fmt.Fprintf(cmd.OutOrStdout(), "loomd %s\n", info.Version)
		},
	}
}

// runtimeHermesSchedules binds only configured source identity. Construction has
// no filesystem side effects; missing/inaccessible stores remain unavailable at
// observation time and never impede daemon startup or provision a native profile.
func runtimeHermesSchedules(cfg config.Config) (*hermesschedules.Observer, error) {
	source, err := cfg.HermesScheduleSource()
	if err != nil {
		return nil, err
	}
	if source.Profile == "" {
		return nil, nil
	}
	// Never fall back to reading the private home in loomd. An absent helper is
	// unavailable until the trusted installed binding is supplied.
	return &hermesschedules.Observer{Source: source, ReadSanitized: hermesschedules.HelperReader(cfg.HermesSchedulesSocket)}, nil
}
