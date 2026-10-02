package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/notesworkspacesync"
	"loom.local/loom/internal/response"
)

func (s Server) handleNotesConflicts(w http.ResponseWriter, r *http.Request) {
	correlation, ctx := requestMeta(r)
	fail := func(status int, code, message string) {
		s.writeError(w, correlation, status, code, "notes", "conflicts", message, nil)
	}
	var input notesworkspacesync.ResolveConflictInput
	q := r.URL.Query()
	if r.Method == http.MethodGet {
		if len(q) > 1 || len(q["id"]) > 1 || len(q["after"]) > 1 || len(q) == 1 && q.Get("id") == "" && q.Get("after") == "" {
			fail(400, "notes.invalid_conflict_request", "Use either one conflict id or an after cursor.")
			return
		}
	} else if r.Method == http.MethodPost {
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
		d.DisallowUnknownFields()
		if d.Decode(&input) != nil || d.Decode(&struct{}{}) != io.EOF || input.Validate() != nil || len(q) != 0 {
			fail(400, "notes.invalid_conflict_request", "An exact review, explicit choice and confirmation are required.")
			return
		}
	} else {
		fail(405, "method.not_allowed", "Method is not allowed.")
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	runtime := notesworkspacesync.Runtime{DB: s.services.DB, Knowledge: knowledge.NewService(s.services.DB), NodeKey: s.services.RuntimeConfig.NodeID}
	if path := s.services.RuntimeConfig.NotesWorkspaceConfig; path != "" {
		runtime.Config, _ = notesworkspacesync.LoadRuntimeConfig(path)
	}
	var result any
	var err error
	if r.Method == http.MethodPost {
		result, err = runtime.ResolveConflict(ctx, input)
	} else if q.Get("id") != "" {
		result, err = runtime.ShowConflict(ctx, q.Get("id"))
	} else {
		result, err = runtime.ListConflicts(ctx, q.Get("after"))
	}
	if errors.Is(err, notesworkspacesync.ErrConflictReview) {
		fail(409, "notes.conflict_review_changed", "The source or resolution changed. Refresh the comparison before choosing again.")
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		fail(404, "notes.conflict_not_found", "The conflict is not available in the current workspace.")
		return
	}
	if err != nil {
		fail(409, "notes.conflict_unavailable", "The current workspace or conflict cannot be accessed. No resolution was confirmed.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	response.WriteJSON(w, 200, response.Success(correlation, result))
}
