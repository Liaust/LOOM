package loomdapp

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/config"
	hs "loom.local/loom/internal/hermesschedules"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/notesworkspacesync"
	"loom.local/loom/internal/projectapply"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
	"loom.local/loom/internal/routing"
	"loom.local/loom/internal/serviceregistry"
)

// Resolve configuration on each request, so reviewed targets cannot outlive a
// change of local owner or Box root. Incomplete composition stays fail-closed.
func newProjectDeclarationService(db *sql.DB, current func() (config.Config, error)) *projectapply.Service {
	if db == nil || current == nil {
		return nil
	}
	resolver := projectapply.NewLocalResolver(db, current)
	resolver.NotesPlacement = func(ctx context.Context, cfg config.Config, node, id, name, root, workState string, declaration pc.ProjectDeclaration) ([]pc.DeclarationNotesPlacement, error) {
		for _, resource := range declaration.Resources {
			if resource.Knowledge != nil {
				workspace, err := notesworkspacesync.LoadRuntimeConfig(cfg.NotesWorkspaceConfig)
				if err != nil {
					return nil, err
				}
				return notesworkspacesync.PreviewProjectPlacement(ctx, db, workspace, node, id, name, root, workState, declaration)
			}
		}
		return nil, nil
	}
	configureProjectHermes(resolver, current)
	resolver.Applications = serviceregistry.ApplicationHelperClient{SocketPath: serviceregistry.ApplicationSocketPath}
	source := applicationSourceAdapter()
	builds := &projectapply.ApplicationBuildCoordinator{Jobs: jobs.Service{DB: db, DataDir: "/var/lib/loom"}, Source: source, Snapshots: "/var/lib/loom/application-builds/snapshots"}
	resolver.SourceApplications = source
	watch := projectapply.WatchOwner{Resolver: resolver, Reconciler: projectwatch.NewDeclarationReconciler(db)}
	currentProjects := func() (projects.Service, error) {
		cfg, err := current()
		if err != nil {
			return projects.Service{}, fmt.Errorf("project registration configuration unavailable: %w", err)
		}
		return projectDeclarationProjects(db, cfg)
	}
	service := projectapply.NewService(db, resolver, projectapply.NewCurrentAuthority(db), map[pc.DeclarationOwner]projectapply.Owner{
		pc.DeclarationOwnerProjects:    projectapply.ProjectsOwner{Resolver: resolver, Projects: projects.NewService(db), CurrentProjects: currentProjects},
		pc.DeclarationOwnerKnowledge:   watch,
		pc.DeclarationOwnerProtection:  watch,
		pc.DeclarationOwnerAutomation:  projectapply.ScheduleOwner{Resolver: resolver},
		pc.DeclarationOwnerHermes:      projectapply.HermesScheduleOwner{Resolver: resolver},
		pc.DeclarationOwnerApplication: projectapply.ApplicationOwner{Builds: builds, Source: source, Backup: projectapply.CloudApplicationBackupStatus{Operations: maintenance.NewService(db), Config: current}, Calls: routing.NewService(db), Inspector: serviceregistry.ApplicationHelperClient{SocketPath: serviceregistry.ApplicationSocketPath}, Preparer: projectapply.ManagedApplicationPreparer{Helper: serviceregistry.ApplicationHelperClient{SocketPath: serviceregistry.ApplicationSocketPath}, Registry: capabilities.NewService(db), Projects: projects.NewService(db)}},
	})
	builds.Parent = service
	service.ApplicationBuilds = builds
	return service
}

func projectDeclarationProjects(db *sql.DB, cfg config.Config) (projects.Service, error) {
	service := projects.NewService(db)
	if cfg.BoxesFile != "" {
		catalog, err := cfg.BoxCatalog()
		if err != nil {
			return projects.Service{}, fmt.Errorf("project registration Box catalog unavailable: %w", err)
		}
		service = service.WithBoxCatalog(catalog)
	}
	return service, nil
}

// This explicit opt-in is supplied by the fixed native owner socket module.
func configureProjectHermes(r *projectapply.LocalResolver, current func() (config.Config, error)) {
	if os.Getenv("LOOM_HERMES_PROJECT_SCHEDULES") != "1" {
		return
	}
	cfg, err := current()
	if err == nil {
		r.HermesSource, _ = cfg.HermesScheduleSource()
	}
	// Invalid enabled configuration remains unavailable, never silently disabled.
	r.Hermes = hs.ProjectClient{Source: r.HermesSource}
}
func projectHermesArchivePause(current func() (config.Config, error)) func(context.Context, string) error {
	if os.Getenv("LOOM_HERMES_PROJECT_SCHEDULES") != "1" {
		return nil
	}
	return func(ctx context.Context, projectID string) error {
		r := &projectapply.LocalResolver{}
		configureProjectHermes(r, current)
		return r.PauseHermesProject(ctx, projectID)
	}
}
