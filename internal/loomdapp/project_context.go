package loomdapp

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectstate"
	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/repostate"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/workers"
	workerruntimes "loom.local/loom/internal/workers/runtimes"
)

// The optional callback receives verified home_box_id, not a project ID or
// the current navigation default. Box-aware instances never fall back to Personal;
// an identity-free primary with no callback retains the legacy inspector.
type projectContextArchiveForBox func(context.Context, string) (storagearchive.ProjectRuntimeService, error)

func seedProjectContextRefresh(ctx context.Context, db *sql.DB, cfg config.Config, foundation *provenance.FoundationAPI, registry *workers.Registry, logger *slog.Logger, req requestctx.Context, archive storagearchive.ProjectRuntimeService, wakes *workers.QueueWakes, archiveForBox ...projectContextArchiveForBox) error {
	if cfg.NodeRole != "main" {
		return nil
	}
	projectService := projects.NewService(db)
	homeBoxID := func(ctx context.Context, id string) (string, error) {
		return projectContextHomeBoxID(ctx, projectService, id)
	}
	inspector := projectContextDevelopmentInspector{HomeBoxID: homeBoxID, PrimaryArchive: archive}
	if len(archiveForBox) > 1 {
		return fmt.Errorf("project context refresh accepts one archive resolver")
	}
	if len(archiveForBox) == 1 {
		inspector.ArchiveForBox = archiveForBox[0]
	}
	refresh := provenance.ProjectRefreshService{Source: projectstate.Service{Reader: projectService, LocalNode: cfg.NodeID, ProjectDevelopment: inspector}, Repositories: repostate.ProvenanceAdapter{}, Projection: foundation,
		HomeBoxID: homeBoxID,
	}
	changes := projects.NewProjectContextChanges(projectService, cfg.NodeID, func() { wakes.SignalLocal(workers.KindProjectContextRefresh) })
	changes.Start(ctx)
	runtime := workerruntimes.ProjectContextRefreshRuntime{Sources: projectService, Refresh: refresh, LocalNode: cfg.NodeID, Changes: changes,
		LoadCheckpoint: func(ctx context.Context, instanceID string) (json.RawMessage, error) {
			var raw json.RawMessage
			err := db.QueryRowContext(ctx, `SELECT checkpoint_json FROM workers.worker_checkpoints WHERE worker_instance_id=$1 AND checkpoint_key='default'`, instanceID).Scan(&raw)
			if err == sql.ErrNoRows {
				return nil, nil
			}
			return raw, err
		},
	}
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

type projectContextDevelopmentInspector struct {
	HomeBoxID      func(context.Context, string) (string, error)
	PrimaryArchive storagearchive.ProjectRuntimeService
	ArchiveForBox  projectContextArchiveForBox
}

func (s projectContextDevelopmentInspector) archive(ctx context.Context, projectID string) (storagearchive.ProjectRuntimeService, error) {
	if s.ArchiveForBox == nil && s.PrimaryArchive.WorkspaceRoots.BoxID == "" {
		return s.PrimaryArchive, nil
	}
	unavailable := fmt.Errorf("%w: archive Box binding unavailable", projectstate.ErrProjectStateUnavailable)
	if s.HomeBoxID == nil {
		return storagearchive.ProjectRuntimeService{}, unavailable
	}
	home, err := s.HomeBoxID(ctx, projectID)
	if err != nil || !boxregistry.IsBoxRef("box:"+home) {
		return storagearchive.ProjectRuntimeService{}, unavailable
	}
	selected := s.PrimaryArchive
	if s.ArchiveForBox != nil {
		selected, err = s.ArchiveForBox(ctx, home)
		if err != nil {
			return storagearchive.ProjectRuntimeService{}, unavailable
		}
	}
	if selected.WorkspaceRoots.BoxID != home {
		return storagearchive.ProjectRuntimeService{}, unavailable
	}
	return selected, nil
}

func (s projectContextDevelopmentInspector) Inspect(ctx context.Context, input projectstate.ProjectDevelopmentInput) projectstate.ProjectDevelopmentState {
	input.VerifiedArchive = false
	if input.Lifecycle != "archived" {
		return projectstate.ReadProjectDevelopment(ctx, input)
	}
	archive, err := s.archive(ctx, input.ProjectID)
	if err == nil {
		return (storagearchive.ProjectDevelopmentInspector{Archive: archive}).Inspect(ctx, input)
	}
	// Archived + unverified returns before touching ProjectRoot. Keep the
	// ordinary qualified result, but explain that no matching owner was bound.
	result := projectstate.ReadProjectDevelopment(ctx, input)
	result.Posture = projectstate.ProjectDevelopmentUnavailable
	result.ReasonCode = "archive_box_binding_unavailable"
	return result
}

func projectContextHomeBoxID(ctx context.Context, reader interface {
	ResolveProjectRef(context.Context, string) (projects.Project, error)
}, id string) (string, error) {
	project, err := reader.ResolveProjectRef(ctx, id)
	if err != nil {
		return "", err
	}
	return projects.HomeBoxID(project.Metadata), nil
}
