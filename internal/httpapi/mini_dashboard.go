package httpapi

import (
	"net/http"

	"loom.local/loom/internal/response"
)

func (s Server) handleMiniDashboardStatus(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "mini_dashboard", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	if s.services.MiniDashboard == nil {
		s.writeError(w, correlationID, http.StatusServiceUnavailable, "mini_dashboard.unavailable", "mini_dashboard", "status", "Mini-dashboard status is unavailable.", nil)
		return
	}
	snapshot, err := s.services.MiniDashboard.Snapshot(ctx)
	if err != nil {
		s.writeError(w, correlationID, http.StatusServiceUnavailable, "mini_dashboard.unavailable", "mini_dashboard", "status", "Mini-dashboard status is unavailable.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, snapshot))
}
