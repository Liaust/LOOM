package httpapi

import (
	"context"
	"net/http"
	"time"

	"loom.local/loom/internal/response"
	"loom.local/loom/internal/workspaceactions"
)

func (s Server) handleFileOperations(w http.ResponseWriter, r *http.Request) {
	files, cid, ok := s.filesTransport(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, cid, 405, "method.not_allowed", "files", "operations", "Use GET to inspect a file operation.", nil)
		return
	}
	if s.services.DB == nil {
		s.writeError(w, cid, 503, "files.operations_unavailable", "files", "operations", "Project classification is unavailable; no files were changed.", nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	q := r.URL.Query()
	in := workspaceactions.Input{Action: q.Get("action"), Root: q.Get("root"), Path: q.Get("path"), ToRoot: q.Get("to_root"), ToPath: q.Get("to_path")}
	if in.Action == "" {
		in.Action = "inspect"
	}
	if in.Action != "inspect" && in.Action != "move" && in.Action != "delete" && in.Action != "copy" && in.Action != "edit" {
		s.writeError(w, cid, 400, "files.operations_invalid", "files", "operations", "Unknown file operation.", nil)
		return
	}
	if _, _, err := files.SourcePath(ctx, in.Root, in.Path); err != nil {
		s.writeFileError(w, cid, err)
		return
	}
	from, err := files.InspectTarget(in.Root, in.Path)
	if err != nil {
		s.writeFileError(w, cid, err)
		return
	}
	to := ""
	if in.ToRoot != "" {
		to, err = files.InspectTarget(in.ToRoot, in.ToPath)
		if err != nil {
			s.writeFileError(w, cid, err)
			return
		}
	}
	if (in.Action == "move" || in.Action == "copy") && to == "" {
		s.writeError(w, cid, 400, "files.operations_invalid", "files", "operations", "Choose an exact destination.", nil)
		return
	}
	items, err := workspaceactions.RelatedProjects(ctx, s.services.DB, from, to)
	if err != nil {
		s.writeError(w, cid, 409, "files.operations_unavailable", "files", "operations", "The current project paths could not be verified; no files were changed.", err)
		return
	}
	response.WriteJSON(w, 200, response.Success(cid, workspaceactions.Classify(in, from, to, items)))
}
