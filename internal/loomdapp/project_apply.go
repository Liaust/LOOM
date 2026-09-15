package loomdapp

import (
	"database/sql"
	"loom.local/loom/internal/capabilities"

	"loom.local/loom/internal/config"
	"loom.local/loom/internal/maintenance"
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
	resolver.Applications = serviceregistry.ApplicationHelperClient{SocketPath: serviceregistry.ApplicationSocketPath}
	watch := projectapply.WatchOwner{Resolver: resolver, Reconciler: projectwatch.NewDeclarationReconciler(db)}
	return projectapply.NewService(db, resolver, projectapply.NewCurrentAuthority(db), map[pc.DeclarationOwner]projectapply.Owner{
		pc.DeclarationOwnerProjects:    projectapply.ProjectsOwner{Resolver: resolver, Projects: projects.NewService(db)},
		pc.DeclarationOwnerKnowledge:   watch,
		pc.DeclarationOwnerProtection:  watch,
		pc.DeclarationOwnerApplication: projectapply.ApplicationOwner{Backup: projectapply.CloudApplicationBackupStatus{Operations: maintenance.NewService(db), Config: current}, Calls: routing.NewService(db), Inspector: serviceregistry.ApplicationHelperClient{SocketPath: serviceregistry.ApplicationSocketPath}, Preparer: projectapply.ManagedApplicationPreparer{Helper: serviceregistry.ApplicationHelperClient{SocketPath: serviceregistry.ApplicationSocketPath}, Registry: capabilities.NewService(db), Projects: projects.NewService(db)}},
	})
}
