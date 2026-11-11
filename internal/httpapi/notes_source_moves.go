package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/notesworkspacesync"
	"loom.local/loom/internal/response"
)

func (s Server) handleNotesSourceMoves(w http.ResponseWriter, r *http.Request) {
	correlation, ctx := requestMeta(r)
	if r.Method != http.MethodPost {
		s.writeError(w, correlation, 405, "method.not_allowed", "notes", "source_move", "Use POST to review or confirm an exact source move.", nil)
		return
	}
	var input notesworkspacesync.SourceMoveInput
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(&struct{}{}) != io.EOF || input.Validate() != nil || len(r.URL.Query()) != 0 {
		s.writeError(w, correlation, 400, "notes.invalid_source_move", "notes", "source_move", "Use exact workspace from/to paths; confirmation requires the current review token.", nil)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	runtime := notesworkspacesync.Runtime{DB: s.services.DB, Knowledge: knowledge.NewService(s.services.DB), NodeKey: s.services.RuntimeConfig.NodeID}
	if path := s.services.RuntimeConfig.NotesWorkspaceConfig; path != "" {
		runtime.Config, _ = notesworkspacesync.LoadRuntimeConfig(path)
	}
	result, err := runtime.SourceMove(ctx, input)
	if err != nil {
		code := "notes.source_move_unavailable"
		if errors.Is(err, notesworkspacesync.ErrConflictReview) {
			code = "notes.source_move_review_changed"
		}
		s.writeError(w, correlation, 409, code, "notes", "source_move", "The exact source absence, destination publication and current bindings must be verified. No file was overwritten.", nil)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	response.WriteJSON(w, 200, response.Success(correlation, result))
}
