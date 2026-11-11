package httpapi

import (
	"net/http"

	"loom.local/loom/internal/minidashboard"
	"loom.local/loom/internal/response"
)

func (s Server) handleWorkspaceHost(w http.ResponseWriter, r *http.Request) {
	cid, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, cid, 405, "method.not_allowed", "system", "host", "Use GET for the current host snapshot.", nil)
		return
	}
	if trusted, _ := ctx.Value(localFileTransportKey{}).(bool); !trusted {
		s.writeError(w, cid, 403, "system.local_transport_required", "system", "host", "Host metrics require the existing operator Unix socket.", nil)
		return
	}
	if s.services.WorkspaceHost == nil {
		s.writeError(w, cid, 503, "system.host_unavailable", "system", "host", "Host metrics are unavailable.", nil)
		return
	}
	host, systemDisk, err := s.services.WorkspaceHost(ctx)
	if err != nil {
		s.writeError(w, cid, 503, "system.host_unavailable", "system", "host", "The host snapshot did not finish.", nil)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	response.WriteJSON(w, 200, response.Success(cid, struct {
		Host       minidashboard.HostSnapshot `json:"host"`
		SystemDisk minidashboard.Metric       `json:"system_disk"`
	}{host, systemDisk}))
}
