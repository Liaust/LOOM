package httpapi

import (
	"net/http"

	"loom.local/loom/internal/response"
)

func (s Server) handleNotesRecoveryInventory(w http.ResponseWriter, r *http.Request) {
	correlationID, _ := requestMeta(r)
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "backup", "notes-inventory", "Use GET to inspect or POST to refresh recovery metadata.", nil)
		return
	}
	if trusted, _ := r.Context().Value(localRootTransportKey{}).(bool); !trusted {
		s.writeError(w, correlationID, http.StatusForbidden, "backup.root_transport_required", "backup", "notes-inventory", "Notes recovery preparation requires the root-owned local Unix connection.", nil)
		return
	}
	prepare := s.services.NotesRecovery
	if r.Method == http.MethodPost {
		prepare = s.services.NotesRecoveryPrepare
	}
	if prepare == nil {
		s.writeError(w, correlationID, http.StatusServiceUnavailable, "backup.notes_unavailable", "backup", "notes-inventory", "Notes recovery preparation is not configured.", nil)
		return
	}
	result, err := prepare(r.Context())
	if err != nil {
		s.writeError(w, correlationID, http.StatusConflict, "backup.notes_membership_held", "backup", "notes-inventory", "Current Notes recovery membership could not be verified.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}
