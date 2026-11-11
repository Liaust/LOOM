package httpapi

import (
	"net/http"
	"strconv"

	"loom.local/loom/internal/response"
	"loom.local/loom/internal/workspacehistory"
)

func (s Server) handleFileHistory(w http.ResponseWriter, r *http.Request) {
	files, cid, ok := s.filesTransport(w, r)
	if !ok {
		return
	}
	if s.services.DB == nil {
		s.writeError(w, cid, 503, "files.history_unavailable", "files", "history", "The source receipt owner is unavailable.", nil)
		return
	}
	if r.Method != http.MethodGet {
		s.writeError(w, cid, 405, "method.not_allowed", "files", "history", "History is read-only.", nil)
		return
	}
	q := r.URL.Query()
	from, e1 := workspacehistory.ParseTime(q.Get("from"))
	to, e2 := workspacehistory.ParseTime(q.Get("to"))
	limit := 100
	if q.Has("limit") {
		var e error
		limit, e = strconv.Atoi(q.Get("limit"))
		if e != nil || limit < 1 || limit > 200 {
			e1 = strconv.ErrSyntax
		}
	}
	if e1 != nil || e2 != nil {
		s.writeError(w, cid, 400, "files.history_invalid", "files", "history", "Use bounded timeline dates and limit.", nil)
		return
	}
	owner := workspacehistory.Service{DB: s.services.DB, Files: files}
	input := workspacehistory.Input{Root: q.Get("root"), Path: q.Get("path"), After: q.Get("after"), Kind: q.Get("kind"), Operation: q.Get("operation"), From: from, To: to, Limit: limit}
	var result any
	var err error
	switch q.Get("action") {
	case "", "list":
		result, err = owner.List(r.Context(), input)
	case "compare":
		result, err = owner.Compare(r.Context(), input)
	default:
		s.writeError(w, cid, 400, "files.history_invalid", "files", "history", "Unknown history read.", nil)
		return
	}
	if err != nil {
		s.writeError(w, cid, 409, "files.history_unavailable", "files", "history", "Recorded history could not be read in this scope; no source files were changed.", err)
		return
	}
	response.WriteJSON(w, 200, response.Success(cid, result))
}
