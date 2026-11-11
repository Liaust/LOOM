package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"path/filepath"
	"strings"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/storagearchive"
)

// Lifecycle owners are chosen once from request context, never from an operation
// ID or a later navigation default. Legacy unscoped servers remain Personal-only.
func (s Server) lifecycleRequestContext(ctx context.Context, r *http.Request) (context.Context, error) {
	if _, scoped := boxregistry.FromContext(ctx); !scoped {
		selection, err := s.boxSelection(r)
		if err != nil {
			return ctx, err
		}
		if selection != nil {
			ctx = boxregistry.WithSelection(ctx, *selection)
		}
	}
	_, err := s.lifecycleBox(ctx)
	return ctx, err
}

func (s Server) lifecycleBox(ctx context.Context) (boxregistry.Box, error) {
	selection, scoped := boxregistry.FromContext(ctx)
	if scoped && (selection.AllBoxes || len(selection.BoxIDs) != 1) {
		return boxregistry.Box{}, fmt.Errorf("%w: choose exactly one Box for a lifecycle operation", boxregistry.ErrSelection)
	}
	if s.services.Boxes == nil {
		if scoped {
			return boxregistry.Box{}, boxregistry.ErrUnavailable
		}
		return boxregistry.Box{Primary: true, RootRef: "box"}, nil
	}
	ref := "box"
	if scoped {
		ref = selection.BoxIDs[0]
	}
	return s.services.Boxes.Get(ref)
}

func (s Server) validateLifecycleRoots(ctx context.Context, roots storagearchive.TrustedWorkspaceRoots) error {
	box, err := s.lifecycleBox(ctx)
	if err != nil || s.services.Boxes == nil {
		return err
	}
	if roots.BoxID != box.ID && !(roots.BoxID == "" && box.Primary) || roots.BoxRoot != box.RootPath || roots.StorageRoot != box.ArchiveRoot {
		return fmt.Errorf("%w: lifecycle owner does not match selected Box roots", boxregistry.ErrUnavailable)
	}
	return nil
}

func (s Server) validateLifecycleBoxID(ctx context.Context, id string) error {
	box, err := s.lifecycleBox(ctx)
	if err != nil {
		return err
	}
	if s.services.Boxes == nil {
		if id != "" {
			return fmt.Errorf("%w: a bound operation requires its registered Box", boxregistry.ErrSelection)
		}
		return nil
	}
	if id != box.ID && !(id == "" && box.Primary) {
		return fmt.Errorf("%w: saved operation belongs to another Box", boxregistry.ErrSelection)
	}
	return nil
}

func (s Server) validateLifecycleInspection(ctx context.Context, id string, inspection storagearchive.WorkspaceArchiveInspection) error {
	if inspection.Operation.OperationID != id {
		return fmt.Errorf("saved operation ID does not match the selected owner receipt")
	}
	if err := s.validateLifecycleBoxID(ctx, inspection.Operation.BoxID); err != nil {
		return err
	}
	return s.validateLifecycleBoxID(ctx, inspection.Plan.BoxID)
}

func (s Server) lifecycleWorkspaceArchive(ctx context.Context) (WorkspaceArchiveLifecycleService, error) {
	box, err := s.lifecycleBox(ctx)
	if err != nil {
		return nil, err
	}
	owner := s.services.BoxWorkspaceArchives[box.ID]
	if projectArchiveDependencyNil(owner) && box.Primary {
		owner = s.services.WorkspaceArchive
	}
	if projectArchiveDependencyNil(owner) {
		return nil, fmt.Errorf("%w: selected Box archive owner is unavailable", boxregistry.ErrUnavailable)
	}
	switch value := owner.(type) {
	case storagearchive.WorkspaceMoveService:
		err = s.validateLifecycleRoots(ctx, value.Roots)
	case *storagearchive.WorkspaceMoveService:
		err = s.validateLifecycleRoots(ctx, value.Roots)
	}
	return owner, err
}

func (s Server) lifecycleWorkspacePaths(ctx context.Context) (*storagearchive.WorkspacePathService, error) {
	box, err := s.lifecycleBox(ctx)
	if err != nil {
		return nil, err
	}
	owner := s.services.BoxWorkspacePaths[box.ID]
	if owner == nil && box.Primary {
		owner = s.services.WorkspacePaths
	}
	if owner == nil {
		return nil, fmt.Errorf("%w: selected Box path owner is unavailable", boxregistry.ErrUnavailable)
	}
	return owner, s.validateLifecycleRoots(ctx, owner.Roots)
}

func (s Server) lifecycleProjectArchive(ctx context.Context) (ProjectPhysicalArchiveService, error) {
	box, err := s.lifecycleBox(ctx)
	if err != nil {
		return nil, err
	}
	if owner, exists := s.services.BoxProjectArchives[box.ID]; exists {
		return owner, s.validateLifecycleRoots(ctx, owner.WorkspaceRoots)
	}
	if !box.Primary {
		return nil, fmt.Errorf("%w: selected Box project owner is unavailable", boxregistry.ErrUnavailable)
	}
	owner := s.services.ProjectPhysicalArchive
	if projectArchiveDependencyNil(owner) {
		owner = s.services.ProjectArchive
	}
	switch value := owner.(type) {
	case storagearchive.ProjectRuntimeService:
		err = s.validateLifecycleRoots(ctx, value.WorkspaceRoots)
	case *storagearchive.ProjectRuntimeService:
		if value != nil {
			err = s.validateLifecycleRoots(ctx, value.WorkspaceRoots)
		}
	}
	return owner, err
}

func (s Server) lifecycleProjectRuntime(ctx context.Context) (storagearchive.ProjectRuntimeService, error) {
	box, err := s.lifecycleBox(ctx)
	if err != nil {
		return storagearchive.ProjectRuntimeService{}, err
	}
	if owner, exists := s.services.BoxProjectArchives[box.ID]; exists {
		return owner, s.validateLifecycleRoots(ctx, owner.WorkspaceRoots)
	}
	if !box.Primary {
		return storagearchive.ProjectRuntimeService{}, fmt.Errorf("%w: selected Box project owner is unavailable", boxregistry.ErrUnavailable)
	}
	return s.services.ProjectArchive, s.validateLifecycleRoots(ctx, s.services.ProjectArchive.WorkspaceRoots)
}

func (s Server) writeLifecycleBoxError(w http.ResponseWriter, cid string, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, boxregistry.ErrUnavailable) {
		status = http.StatusServiceUnavailable
	}
	s.writeError(w, cid, status, "box.lifecycle_binding", "box", "lifecycle", err.Error(), nil)
}

// Signed plans keep their logical box/storage refs. Only the non-signed Files
// projection translates between a bound archive subtree and global Storage.
func (s Server) lifecycleStoragePrefix(ctx context.Context) (string, error) {
	box, err := s.lifecycleBox(ctx)
	if err != nil || box.Primary {
		return "", err
	}
	primary, err := s.services.Boxes.Lookup("box")
	if err != nil || !filepath.IsAbs(primary.ArchiveRoot) || !filepath.IsAbs(box.ArchiveRoot) {
		return "", fmt.Errorf("%w: global Storage binding is unavailable", boxregistry.ErrUnavailable)
	}
	prefix, err := filepath.Rel(primary.ArchiveRoot, box.ArchiveRoot)
	if err != nil || prefix == "." || prefix == ".." || strings.HasPrefix(prefix, ".."+string(filepath.Separator)) || filepath.IsAbs(prefix) {
		return "", fmt.Errorf("%w: Box archive is not a distinct global Storage namespace", boxregistry.ErrUnavailable)
	}
	return filepath.ToSlash(prefix), nil
}

func boundStoragePath(prefix, relative string) (string, error) {
	if relative == "" || path.IsAbs(relative) || path.Clean(relative) != relative || strings.ContainsAny(relative, "\\\x00\r\n") || relative == ".." || strings.HasPrefix(relative, "../") {
		return "", fmt.Errorf("invalid global Storage destination")
	}
	if prefix == "" {
		return relative, nil
	}
	if !strings.HasPrefix(relative, prefix+"/") {
		return "", fmt.Errorf("Storage destination is outside the selected Box archive namespace")
	}
	return strings.TrimPrefix(relative, prefix+"/"), nil
}

func (s Server) lifecyclePathSummary(ctx context.Context, plan storagearchive.WorkspacePathPlan, state string) (map[string]any, error) {
	if err := s.validateLifecycleBoxID(ctx, plan.Move.BoxID); err != nil {
		return nil, err
	}
	box, err := s.lifecycleBox(ctx)
	if err != nil {
		return nil, err
	}
	summary := plan.Summary(state)
	for _, field := range []struct {
		root, relative string
		ref            storagearchive.WorkspacePathRef
	}{{"from_root", "source", plan.Move.Source}, {"to_root", "destination", plan.Move.Destination}} {
		switch field.ref.Root {
		case storagearchive.WorkspaceRootBox:
			summary[field.root] = box.RootRef
		case storagearchive.WorkspaceRootStorage:
			summary[field.root] = "storage"
			prefix, err := s.lifecycleStoragePrefix(ctx)
			if err != nil {
				return nil, err
			}
			if prefix != "" {
				summary[field.relative] = path.Join(prefix, field.ref.RelativePath)
			}
		default:
			return nil, fmt.Errorf("unsupported workspace path root")
		}
	}
	return summary, nil
}
