package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/notesworkspacesync"
	"loom.local/loom/internal/response"
)

func (s Server) handleNotesWorkspaceNavigation(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "knowledge", "notes_navigation", "Method is not allowed.", nil)
		return
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	valid := err == nil && (len(values) == 3 || (len(values) == 4 && len(values["source_lifecycle"]) == 1))
	for _, key := range []string{"object_id", "version_id", "source_hash"} {
		valid = valid && len(values[key]) == 1
	}
	input := knowledge.NotesPassageInput{KnowledgeChunkID: strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/knowledge/notes/passages/"), "/workspace"),
		KnowledgeObjectID: values.Get("object_id"), KnowledgeObjectVersionID: values.Get("version_id"), SourceHash: values.Get("source_hash"), SourceLifecycle: knowledge.SourceLifecycleFilter(values.Get("source_lifecycle"))}
	if !valid || knowledge.ValidateNotesPassageInput(input) != nil {
		s.writeError(w, correlationID, http.StatusBadRequest, "knowledge.invalid_passage", "knowledge", "notes_navigation", "An exact object, version, chunk and source hash are required.", nil)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	runtime := notesworkspacesync.Runtime{DB: s.services.DB, Knowledge: knowledge.NewService(s.services.DB), NodeKey: s.services.RuntimeConfig.NodeID}
	if path := s.services.RuntimeConfig.NotesWorkspaceConfig; path != "" {
		// An invalid/missing operator config fails closed as workspace unavailable;
		// passage authorization still runs and no raw configuration is returned.
		runtime.Config, _ = notesworkspacesync.LoadRuntimeConfig(path)
	}
	result, err := runtime.LocatePassage(ctx, input)
	if errors.Is(err, sql.ErrNoRows) {
		s.writeError(w, correlationID, http.StatusNotFound, "knowledge.notes_passage_not_found", "knowledge", "notes_navigation", "The requested passage is not available.", nil)
		return
	}
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "knowledge.notes_navigation_failed", "knowledge", "notes_navigation", "Could not resolve workspace navigation.", nil)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}
