package loomdapp

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/workspacefiles"
)

func workspacePathRuntime(db *sql.DB, files *workspacefiles.Service, roots storagearchive.TrustedWorkspaceRoots, node string, project projects.Service, catalog storagecatalog.Service, archive *storagearchive.WorkspaceMoveService) *storagearchive.WorkspacePathService {
	if archive == nil || node != "main" {
		return nil
	}
	withNotes, commitNotes := workspacePathNotesRuntime(db, files, roots, node)
	service := &storagearchive.WorkspacePathService{Roots: roots, Projects: project, Catalog: catalog, WithNotesLock: withNotes}
	service.Snapshot = func(ctx context.Context, id, operation string) (json.RawMessage, error) {
		snapshot, err := project.CurrentProjectLocationSnapshotLocked(ctx, id, operation)
		if err != nil {
			return nil, err
		}
		var metadata map[string]json.RawMessage
		if json.Unmarshal(snapshot.Project.Metadata, &metadata) != nil {
			return nil, fmt.Errorf("invalid project metadata")
		}
		if raw, ok := metadata["workspace_filesystem_operation"]; ok {
			var marker struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(raw, &marker) != nil || marker.ID != operation {
				return nil, fmt.Errorf("another filesystem operation is pending")
			}
			delete(metadata, "workspace_filesystem_operation")
			snapshot.Project.Metadata, _ = json.Marshal(metadata)
		}
		raw, err := json.Marshal(snapshot)
		return raw, err
	}
	service.Dependencies = workspacePathDependencies(db, project)
	service.ReadersReady = func(context.Context, storagearchive.WorkspacePathPlan) error {
		return fmt.Errorf("backup location reader readiness must be wired")
	}
	service.Commit = func(ctx context.Context, tx *sql.Tx, p storagearchive.WorkspacePathPlan) error {
		if p.Kind == "relocate_project" {
			var snapshot projects.ProjectLocationSnapshot
			if json.Unmarshal(p.ProjectSnapshot, &snapshot) != nil {
				return fmt.Errorf("saved project location snapshot unavailable")
			}
			evidence, err := project.CommitProjectLocationTx(ctx, tx, snapshot, p.Move.Destination.AbsolutePath, p.Move.ID)
			if err != nil {
				return err
			}
			raw, err := json.Marshal(evidence)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO storage.retained_relocation_events(relocation_id,phase,evidence) VALUES($1,'location_successor',$2)`, p.Move.ID, raw); err != nil {
				return err
			}
		}
		return commitNotes(ctx, tx, p)
	}
	return service
}

func wireWorkspaceActiveLocation(db *sql.DB, roots storagearchive.TrustedWorkspaceRoots, archive *storagearchive.WorkspaceMoveService) {
	if archive == nil {
		return
	}
	archive.ActiveProjectLocation = func(ctx context.Context, id string) (*storagearchive.WorkspaceActiveLocation, error) {
		var relative, current, phase, schema string
		var location sql.NullString
		var raw []byte
		var pending bool
		var registrationRevision, repositoryRevision int64
		err := db.QueryRowContext(ctx, `SELECT r.project_root,l.relocation_id,r.registration_revision,COALESCE(s.source_revision,0),
 COALESCE(l.current_prefix,''),COALESCE(o.state,''),COALESCE(o.plan_json->>'schema',''),COALESCE(e.evidence,'null'::jsonb),
 EXISTS(SELECT 1 FROM storage.retained_relocations q WHERE q.custody_id='project-location:'||r.project_id AND q.state='prepared')
 FROM projects.project_contract_registrations r
 LEFT JOIN storage.custody_locations l ON l.custody_id='project-location:'||r.project_id
 LEFT JOIN storage.retained_relocations o ON o.relocation_id=l.relocation_id
 LEFT JOIN storage.retained_relocation_events e ON e.relocation_id=l.relocation_id AND e.phase='location_successor'
 LEFT JOIN projects.project_repository_sources s ON s.project_id=r.project_id WHERE r.project_id=$1`, id).Scan(&relative, &location, &registrationRevision, &repositoryRevision, &current, &phase, &schema, &raw, &pending)
		if err != nil {
			return nil, err
		}
		if pending {
			return nil, fmt.Errorf("project location operation needs exact recovery")
		}
		if !location.Valid {
			return nil, nil
		}
		var evidence projects.ProjectLocationEvidence
		if phase != "complete" || schema != storagearchive.WorkspacePathSchema || current != relative || json.Unmarshal(raw, &evidence) != nil || evidence.OperationID != location.String || evidence.Next.ProjectID != id || evidence.Next.Project.ProjectID != id || evidence.Next.Root != relative || evidence.Next.Registration.ProjectID != id || evidence.Next.Registration.ProjectRoot != relative || int64(evidence.Next.Registration.RegistrationRevision) != registrationRevision {
			return nil, fmt.Errorf("accepted project location evidence changed or is unavailable")
		}
		var sourceRevision int64
		if evidence.Next.RepositorySource != nil {
			sourceRevision = evidence.Next.RepositorySource.SourceRevision
			if evidence.Next.RepositorySource.ProjectRoot != relative {
				return nil, fmt.Errorf("repository location evidence changed")
			}
		}
		if sourceRevision != repositoryRevision {
			return nil, fmt.Errorf("repository source revision changed since accepted relocation")
		}
		path, err := filepath.Rel(roots.BoxRoot, relative)
		if err != nil || strings.HasPrefix(path, "..") || filepath.IsAbs(path) {
			return nil, fmt.Errorf("accepted project location left Box")
		}
		return &storagearchive.WorkspaceActiveLocation{ProjectID: id, Path: storagearchive.WorkspacePathRef{Root: storagearchive.WorkspaceRootBox, RelativePath: filepath.ToSlash(path), AbsolutePath: relative}, LocationID: location.String, RegistrationRevision: registrationRevision, RepositorySourceRevision: repositoryRevision}, nil
	}
}
