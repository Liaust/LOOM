package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"loom.local/loom/internal/artifacts"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/response"
)

func (s Server) handleArtifactContent(w http.ResponseWriter, r *http.Request, ref string) {
	cid, ctx := requestMeta(r)
	principal, err := s.declarationPrincipal(r)
	if err != nil {
		s.writeError(w, cid, http.StatusForbidden, "artifact.read_denied", "artifacts", ref, "Artifact read identity is unavailable.", nil)
		return
	}
	limit := 0
	if raw := r.URL.Query().Get("max_bytes"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			s.writeError(w, cid, http.StatusBadRequest, "artifact.invalid_read", "artifacts", ref, "max_bytes must be an integer.", nil)
			return
		}
	}
	result, err := s.services.Artifacts.ReadContent(ctx, requestctx.Context{ActorID: principal.ActorID, OriginNodeID: principal.OriginNodeID}, ref, limit, r.URL.Query().Get("download") == "true")
	if err != nil {
		status, summary := http.StatusConflict, "Artifact content is unavailable or failed verification."
		if errors.Is(err, artifacts.ErrReadDenied) {
			status, summary = http.StatusForbidden, "Current scope access does not permit reading this artifact."
		}
		if errors.Is(err, artifacts.ErrBinaryContent) {
			summary = "Binary artifact; use loom artifact read <id> --output <file>."
		}
		if errors.Is(err, artifacts.ErrInvalidRead) {
			status, summary = http.StatusBadRequest, "Artifact read limit is invalid."
		}
		s.writeError(w, cid, status, artifacts.ContentErrorCode(err), "artifacts", ref, summary, nil)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(cid, result))
}
