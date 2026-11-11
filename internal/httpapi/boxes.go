package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/response"
)

func (s Server) boxIdempotencyInput(ctx context.Context, input any) any {
	selection, scoped := boxregistry.FromContext(ctx)
	if !scoped {
		return input
	}
	// Existing Personal requests retain their hashes so pending operations can
	// recover. Additional or explicitly combined Boxes cannot replay them.
	if s.services.Boxes != nil && !selection.AllBoxes && len(selection.BoxIDs) == 1 {
		if primary, err := s.services.Boxes.Get("box"); err == nil && selection.BoxIDs[0] == primary.ID {
			return input
		}
	}
	return struct {
		BoxIDs   []string `json:"box_ids"`
		AllBoxes bool     `json:"all_boxes"`
		Input    any      `json:"input"`
	}{selection.BoxIDs, selection.AllBoxes, input}
}

func (s Server) handleBoxes(w http.ResponseWriter, r *http.Request) {
	_, correlationID, ok := s.filesRequest(w, r)
	if !ok {
		return
	}
	if s.services.Boxes == nil {
		s.writeError(w, correlationID, http.StatusServiceUnavailable, "box.catalog_unavailable", "box", "catalog", "Box catalog is not configured.", nil)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, *s.services.Boxes))
}

func (s Server) withBoxContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !boxContextRoute(r) {
			next.ServeHTTP(w, r)
			return
		}
		// An explicit file-root request carries its own workspace, independently
		// of another tab's default discovery context.
		q := r.URL.Query()
		if s.services.Boxes != nil && !q.Has("box_id") && !q.Has("box") && !q.Has("all_boxes") && q.Get("root") != "" {
			if b, err := s.services.Boxes.Lookup(q.Get("root")); err == nil && b.ID != "" {
				r = r.Clone(r.Context())
				r.Header.Set("X-Loom-Box", b.ID)
			}
		}
		selection, err := s.boxSelection(r)
		if err != nil {
			cid, _ := requestMeta(r)
			status := http.StatusBadRequest
			if errors.Is(err, boxregistry.ErrUnavailable) {
				status = http.StatusServiceUnavailable
			}
			s.writeError(w, cid, status, "box.selection_invalid", "box", "selection", err.Error(), nil)
			return
		}
		if selection != nil {
			r = r.WithContext(boxregistry.WithSelection(r.Context(), *selection))
			w.Header().Add("Vary", "X-Loom-Box")
		}
		next.ServeHTTP(w, r)
	})
}

func boxContextRoute(r *http.Request) bool {
	for _, prefix := range []string{"/v1/project", "/v1/knowledge/", "/v1/provenance/", "/v1/files/", "/v1/storage/workspace-archive/", "/v1/jobs", "/v1/schedules", "/v1/schedule-fires", "/v1/automations", "/v1/invocations"} {
		if strings.HasPrefix(r.URL.Path, prefix) {
			return true
		}
	}
	return r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/scripts/") && strings.HasSuffix(r.URL.Path, "/run")
}

// boxSelection supplies default discovery context, not authentication or an ACL.
// A nil result is reserved for legacy/test servers without a configured catalog.
func (s Server) boxSelection(r *http.Request) (*boxregistry.ResolvedSelection, error) {
	q := r.URL.Query()
	input := boxregistry.Selection{BoxIDs: append([]string(nil), q["box_id"]...)}
	if q.Has("box") {
		input.BoxIDs = append(input.BoxIDs, q.Get("box"))
	}
	if q.Has("all_boxes") {
		var err error
		input.AllBoxes, err = strconv.ParseBool(q.Get("all_boxes"))
		if err != nil {
			return nil, boxregistry.ErrSelection
		}
	}
	return s.resolveBoxSelection(r, input)
}

func (s Server) resolveBoxSelection(r *http.Request, input boxregistry.Selection) (*boxregistry.ResolvedSelection, error) {
	if s.services.Boxes == nil {
		if len(input.BoxIDs) > 0 || input.AllBoxes || r.Header.Get("X-Loom-Box") != "" {
			return nil, fmt.Errorf("%w: catalog unavailable", boxregistry.ErrUnavailable)
		}
		return nil, nil
	}
	selection, err := s.services.Boxes.Select(input, r.Header.Get("X-Loom-Box"))
	if err != nil {
		return nil, err
	}
	return &selection, nil
}
