package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/response"
	"loom.local/loom/internal/workspacefiles"
	"loom.local/loom/internal/workspacenotes"
)

func (s Server) workspaceLinks() workspacenotes.Service {
	return workspacenotes.Service{DB: s.services.DB, Files: s.services.Files, RootBoxID: s.workspaceRootBoxID}
}

func (s Server) workspaceRootBoxID(ctx context.Context, ref string) (string, error) {
	if ref == "" {
		return "", workspacefiles.ErrUnknownRoot
	}
	if s.services.Boxes != nil {
		if box, err := s.services.Boxes.Lookup(ref); err == nil && box.ID != "" && box.RootRef == ref {
			return box.ID, nil
		}
	}
	files := s.services.Files
	if files == nil {
		return "", workspacefiles.ErrUnknownRoot
	}
	if workspacefiles.IsApplicationRoot(ref) {
		root, err := files.ApplicationRoot(ctx, ref)
		if err != nil {
			return "", err
		}
		if root.BoxID != "" {
			return root.BoxID, nil
		}
		if root.ProjectID == "" {
			return "", nil
		}
		// Intentional unscoped/internal reads retain registered attribution;
		// scoped roots already carry the source owner's validated BoxID.
		return s.projectHomeBoxID(ctx, root.ProjectID)
	}
	for _, root := range files.Roots {
		if root.Ref != ref {
			continue
		}
		if root.ProjectID != "" {
			return s.projectHomeBoxID(ctx, root.ProjectID)
		}
		// Application custody under a physical Box does not establish logical
		// membership, and global retained storage is not a Personal workspace.
		if root.SourceKind == "application_data" || workspacefiles.IsApplicationRoot(root.Ref) {
			return "", nil
		}
		if root.BoxID != "" {
			return root.BoxID, nil
		}
		if s.services.Boxes != nil {
			if box, err := s.services.Boxes.ForPath(root.SourcePath); err == nil {
				return box.ID, nil
			}
			for _, box := range s.services.Boxes.Boxes {
				if !box.Primary && box.ID != "" && box.ArchiveRoot != "" && withinWorkspaceRoot(root.SourcePath, box.ArchiveRoot) {
					return box.ID, nil
				}
			}
		}
		return "", nil
	}
	return "", workspacefiles.ErrUnknownRoot
}

func withinWorkspaceRoot(path, root string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && (path == root || strings.HasPrefix(path, root+string(filepath.Separator)))
}
func (s Server) observeWorkspaceLinks(ctx context.Context, root, path string) string {
	if s.services.DB == nil || s.services.Files == nil {
		return "unavailable"
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := s.workspaceLinks().Observe(ctx, root, path); err != nil {
		return "deferred"
	}
	return "current"
}
func (s Server) handleFileLinks(w http.ResponseWriter, r *http.Request) {
	files, cid, ok := s.filesTransport(w, r)
	if !ok {
		return
	}
	if s.services.DB == nil {
		s.writeError(w, cid, 503, "files.links_unavailable", "files", "links", "The Main navigation catalog is unavailable.", nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	owner := s.workspaceLinks()
	if r.Method == http.MethodPost {
		var input struct {
			Root   string `json:"root"`
			Action string `json:"action"`
			Path   string `json:"path,omitempty"`
			ID     string `json:"id,omitempty"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF {
			s.writeError(w, cid, 400, "files.links_invalid", "files", "links", "Use the bounded catalog seed operation.", nil)
			return
		}
		if input.Action != "seed" {
			var result workspacenotes.Refresh
			var err error
			switch input.Action {
			case "refresh_start":
				result, err = owner.StartRefresh(ctx, input.Root, input.Path)
			case "refresh_step":
				result, err = owner.StepRefresh(ctx, input.ID)
			case "refresh_pause":
				result, err = owner.ControlRefresh(ctx, input.ID, "pause")
			case "refresh_cancel":
				result, err = owner.ControlRefresh(ctx, input.ID, "cancel")
			case "refresh_retry":
				result, err = owner.ControlRefresh(ctx, input.ID, "retry")
			default:
				s.writeError(w, cid, 400, "files.links_invalid", "files", "links", "Unknown metadata operation.", nil)
				return
			}
			if err != nil {
				s.writeError(w, cid, 409, "files.metadata_refresh_paused", "files", "links", "Metadata refresh did not advance; progress and source files are preserved.", err)
				return
			}
			response.WriteJSON(w, 200, response.Success(cid, result))
			return
		}
		count, err := owner.Seed(ctx, input.Root)
		if err != nil {
			s.writeError(w, cid, 409, "files.links_seed_failed", "files", "links", "Could not seed the known Markdown catalog.", err)
			return
		}
		response.WriteJSON(w, 200, response.Success(cid, map[string]any{"seeded": count, "coverage": "existing indexed Markdown metadata; no filesystem crawl or enrichment"}))
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, cid, 405, "method.not_allowed", "files", "links", "Use GET or the explicit catalog seed POST.", nil)
		return
	}
	q := withoutBoundBoxSelectors(ctx, r.URL.Query())
	root, relative, action := q.Get("root"), q.Get("path"), q.Get("action")
	if action == "refresh_status" || action == "refresh_latest" {
		var result workspacenotes.Refresh
		var err error
		if action == "refresh_status" {
			result, err = owner.RefreshStatus(ctx, q.Get("id"))
		} else {
			result, err = owner.LatestRefresh(ctx, root)
		}
		if err != nil {
			s.writeError(w, cid, 409, "files.metadata_refresh_unavailable", "files", "links", "No matching metadata refresh is available.", err)
			return
		}
		response.WriteJSON(w, 200, response.Success(cid, result))
		return
	}
	if action == "catalog" {
		input, err := workspacenotes.ParseCatalogInput(q)
		if err != nil {
			s.writeFileError(w, cid, err)
			return
		}
		result, err := owner.Catalog(ctx, input)
		if err != nil {
			s.writeFileError(w, cid, err)
			return
		}
		response.WriteJSON(w, 200, response.Success(cid, result))
		return
	}
	if _, _, err := files.SourcePath(ctx, root, ""); err != nil {
		s.writeFileError(w, cid, err)
		return
	}
	var result any
	var err error
	switch action {
	case "tag":
		var items []workspacenotes.Item
		items, err = owner.TagFiles(ctx, root, q.Get("query"))
		result = map[string]any{"items": items, "coverage": "up to 100 verified known tagged files"}
	case "complete":
		var items []workspacenotes.Item
		items, err = owner.Complete(ctx, root, q.Get("kind"), q.Get("query"))
		result = map[string]any{"items": items, "coverage": "known catalog and observed files"}
	case "resolve":
		navigate := false
		if values, present := q["navigate"]; present {
			if len(values) != 1 || values[0] != "true" && values[0] != "false" {
				s.writeError(w, cid, http.StatusBadRequest, "files.links_invalid", "files", "links", "navigate must appear once as true or false.", nil)
				return
			}
			navigate = values[0] == "true"
		}
		if _, e := files.Stat(ctx, root, relative); e != nil {
			s.writeFileError(w, cid, e)
			return
		}
		var items []workspacenotes.Item
		if navigate {
			items, err = owner.ResolveExplicit(ctx, root, relative, q.Get("reference"))
		} else {
			items, err = owner.Resolve(ctx, root, relative, q.Get("reference"))
		}
		result = map[string]any{"choices": items}
	case "observe":
		result, err = owner.Observe(ctx, root, relative)
	case "", "links":
		result, err = owner.Links(ctx, root, relative)
	default:
		err = fmt.Errorf("unknown navigation action")
	}
	if err != nil {
		s.writeError(w, cid, 409, "files.links_unavailable", "files", "links", "Navigation metadata could not be verified; source files are unchanged.", err)
		return
	}
	response.WriteJSON(w, 200, response.Success(cid, result))
}
