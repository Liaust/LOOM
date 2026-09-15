package loomdapp

import (
	"context"
	"database/sql"
	"log/slog"

	"loom.local/loom/internal/config"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectstate"
	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/repostate"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/workers"
	workerruntimes "loom.local/loom/internal/workers/runtimes"
)

func seedProjectContextRefresh(ctx context.Context, db *sql.DB, cfg config.Config, foundation *provenance.FoundationAPI, registry *workers.Registry, logger *slog.Logger, req requestctx.Context) error {
	if cfg.NodeRole != "main" {
		return nil
	}
	projectService := projects.NewService(db)
	refresh := provenance.ProjectRefreshService{Source: projectstate.Service{Reader: projectService, LocalNode: cfg.NodeID}, Repositories: repostate.ProvenanceAdapter{}, Projection: foundation}
	runtime := workerruntimes.ProjectContextRefreshRuntime{Sources: projectService, Refresh: refresh, LocalNode: cfg.NodeID}
	if err := registry.Register(runtime); err != nil {
		return err
	}
	// Seeding this additive worker cannot replace another worker's saved policy.
	addition := workers.NewRegistry()
	if err := addition.Register(runtime); err != nil {
		return err
	}
	_, err := workers.NewService(db, addition, logger).SeedBuiltins(ctx, req, "")
	return err
}
