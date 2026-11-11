package httpapi

import (
	"encoding/json"
	"io"
	"net/http"

	"loom.local/loom/internal/response"
	"loom.local/loom/internal/workspacefiles"
)

func (s Server) handleFileCreate(w http.ResponseWriter, r *http.Request) {
	cid, _ := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, cid, 405, "method.not_allowed", "files", "create", "Use POST to create a new note or folder.", nil)
		return
	}
	if trusted, _ := r.Context().Value(localFileTransportKey{}).(bool); !trusted {
		s.writeError(w, cid, 403, "files.local_transport_required", "files", "create", "File creation requires the existing operator Unix socket.", nil)
		return
	}
	if s.services.Files == nil {
		s.writeError(w, cid, 503, "files.unavailable", "files", "create", "Original-file access is not configured.", nil)
		return
	}
	var input workspacefiles.CreateInput
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<17))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(&struct{}{}) != io.EOF {
		s.writeError(w, cid, 400, "files.invalid_create", "files", "create", "Supply an exact Box path and bounded new-note content.", nil)
		return
	}
	entry, err := s.services.Files.Create(r.Context(), input)
	if err != nil {
		s.writeFileError(w, cid, err)
		return
	}
	if entry.Kind == "file" {
		s.observeWorkspaceLinks(r.Context(), input.Root, entry.RelativePath)
	}
	response.WriteJSON(w, 201, response.Success(cid, struct {
		Entry workspacefiles.Entry `json:"entry"`
		State string               `json:"state"`
	}{entry, "source_created_processing_not_verified"}))
}
