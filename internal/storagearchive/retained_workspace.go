package storagearchive

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/workspaceactions"
)

func validWorkspaceRetainedDestination(roots TrustedWorkspaceRoots, p RetainedRelocationPlan) bool {
	rel, err := filepath.Rel(roots.StorageRoot, p.Original)
	if err != nil {
		return false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) != 4 || parts[0] != "archive" || parts[1] != "projects" || parts[3] != "project" || ValidateWorkspaceSlug(parts[2]) != nil {
		return false
	}
	return p.Destination.RelativePath == "Projects/"+parts[2]
}

// This proof uses immutable owner evidence directly. Ordinary current-path
// readers correctly refuse a prepared relocation and must not be called here.
func (s RetainedRelocationService) ProveWorkspaceArchive(ctx context.Context, workspace WorkspaceMoveService, ref WorkspacePathRef, bound string, inventory NoFollowInventory) (RetainedCustodyProof, error) {
	id := strings.TrimPrefix(bound, "workspace:")
	if bound == "" {
		related, err := workspaceactions.RelatedProjects(ctx, s.Catalog.DB, ref.AbsolutePath, "")
		if err != nil {
			return RetainedCustodyProof{}, err
		}
		if len(related) != 1 || related[0].Status != "archived" || related[0].Root != ref.AbsolutePath {
			return RetainedCustodyProof{}, fmt.Errorf("exact completed archived project payload required")
		}
		if err = s.Catalog.DB.QueryRowContext(ctx, `SELECT archive_state->>'operation_id' FROM projects.projects WHERE project_id=$1 AND status='archived'`, related[0].ID).Scan(&id); err != nil {
			return RetainedCustodyProof{}, err
		}
	}
	if !workspaceOperationID.MatchString(id) || bound != "" && bound != "workspace:"+id {
		return RetainedCustodyProof{}, fmt.Errorf("exact workspace custody required")
	}
	plan, _, manifest, err := workspace.loadCompletedArchiveEvidence(ctx, id)
	if err != nil {
		return RetainedCustodyProof{}, err
	}
	var raw json.RawMessage
	if err = s.Catalog.DB.QueryRowContext(ctx, `SELECT archive_state FROM projects.projects WHERE project_id=$1 AND status='archived'`, plan.ObjectID).Scan(&raw); err != nil {
		return RetainedCustodyProof{}, err
	}
	state, ok := projects.ParseProjectPhysicalArchiveState(raw)
	if !ok || state.OperationID != id || state.Restore != nil || state.Phase != projects.ProjectArchivePhaseComplete || state.WorkspacePlanDigest != plan.PlanDigest || state.ArchivePath != plan.Destination.Path.AbsolutePath {
		return RetainedCustodyProof{}, fmt.Errorf("project archive is not complete or restore has begun")
	}
	var unfinished bool
	if err = s.Catalog.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM storage.workspace_archive_operations WHERE object_id=$1 AND workspace_kind='project' AND terminal_status<>'complete')`, plan.ObjectID).Scan(&unfinished); err != nil || unfinished {
		return RetainedCustodyProof{}, fmt.Errorf("workspace has unfinished lifecycle operation: %v", err)
	}
	if !retainedInventoriesMatch(plan.Inventory, inventory) {
		return RetainedCustodyProof{}, fmt.Errorf("payload differs from original completed archive inventory")
	}
	current := plan.Destination.Path.AbsolutePath
	if err = s.Catalog.DB.QueryRowContext(ctx, `SELECT COALESCE((SELECT current_prefix FROM storage.custody_locations WHERE custody_id=$1 AND original_prefix=$2),$2)`, "workspace:"+id, current).Scan(&current); err != nil {
		return RetainedCustodyProof{}, err
	}
	if current != ref.AbsolutePath {
		return RetainedCustodyProof{}, fmt.Errorf("selected payload is not the recorded current archive")
	}
	paths, err := ResolveWorkspacePlanPaths(s.Roots, plan)
	if err != nil {
		return RetainedCustodyProof{}, err
	}
	meta, err := openHeldDirectoryChain(s.Roots.StorageRoot, paths.ArchiveContainer.RelativePath, nil)
	if err != nil {
		return RetainedCustodyProof{}, err
	}
	defer meta.Close()
	if err = verifyContainerEntries(meta.ParentFD(), map[string]bool{"archive.json": true, "project": plan.Placement == nil}); err != nil {
		return RetainedCustodyProof{}, err
	}
	manifestBytes, present, err := readFileAtNoFollow(meta.ParentFD(), "archive.json", workspaceManifestMaxBytes)
	if err != nil || !present || workspace.verifyCanonicalManifestPayload(ctx, plan, manifest, manifestBytes) != nil || sha256Digest(manifestBytes) != state.ArchiveManifestDigest {
		return RetainedCustodyProof{}, fmt.Errorf("original archive manifest differs from completed project evidence")
	}
	return RetainedCustodyProof{ID: "workspace:" + id, Original: plan.Destination.Path.AbsolutePath, Digest: sha256Digest(manifestBytes)}, nil
}
