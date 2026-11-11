package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"unicode/utf8"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/projectapply"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/response"
)

func (s Server) boxMembershipRequest(w http.ResponseWriter, r *http.Request) (context.Context, string, bool) {
	cid, ctx := requestMeta(r)
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		s.writeError(w, cid, 405, "method.not_allowed", "box", "membership", "Use POST for metadata membership maintenance.", nil)
		return ctx, cid, false
	}
	if trusted, _ := ctx.Value(localFileTransportKey{}).(bool); !trusted {
		s.writeError(w, cid, 403, "files.local_transport_required", "box", "membership", "Membership maintenance requires the operator Unix socket.", nil)
		return ctx, cid, false
	}
	if s.services.Boxes == nil {
		s.writeError(w, cid, 503, "box.catalog_unavailable", "box", "membership", "Box catalog is not configured.", nil)
		return ctx, cid, false
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) != 1 || len(query["all_boxes"]) != 1 || query.Get("all_boxes") != "true" {
		s.writeError(w, cid, 400, "box.explicit_all_required", "box", "membership", "Specify exactly all_boxes=true for administrative membership maintenance.", nil)
		return ctx, cid, false
	}
	selection, err := s.boxSelection(r)
	if err != nil || selection == nil || !selection.AllBoxes {
		s.writeError(w, cid, 400, "box.selection_invalid", "box", "membership", "Could not resolve the explicit all-Box selection.", nil)
		return ctx, cid, false
	}
	return boxregistry.WithSelection(ctx, *selection), cid, true
}

func decodeBoxMembership(r *http.Request, out any, allowed ...string) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 || !utf8.Valid(raw) {
		return fmt.Errorf("invalid bounded JSON")
	}
	if _, err := projectapply.CanonicalJSON(raw); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return fmt.Errorf("an object is required")
	}
	keys := map[string]bool{}
	for _, key := range allowed {
		keys[key] = true
	}
	for key, value := range fields {
		if !keys[key] || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("unknown or null field")
		}
	}
	return json.Unmarshal(raw, out)
}

func validateBoxMembershipIDs(values []string, prefix string, apply bool, catalog string, evidence map[string]string) error {
	if len(values) < 1 || len(values) > projects.MaxBoxMembershipBatch || (catalog != "" && !declarationDigest.MatchString(catalog)) || (apply && !declarationDigest.MatchString(catalog)) {
		return fmt.Errorf("exact IDs and catalog digest required")
	}
	seen := map[string]bool{}
	for _, id := range values {
		if ids.Validate(prefix, id) != nil || seen[id] {
			return fmt.Errorf("unique exact IDs required")
		}
		seen[id] = true
		if apply && !declarationDigest.MatchString(evidence[id]) {
			return fmt.Errorf("exact dry-run evidence required")
		}
	}
	for id, digest := range evidence {
		if !seen[id] || !declarationDigest.MatchString(digest) {
			return fmt.Errorf("evidence must name only requested IDs")
		}
	}
	return nil
}

func (s Server) writeBoxMembershipError(w http.ResponseWriter, cid string, err error) {
	status, code := http.StatusConflict, "box.membership_evidence_changed"
	if errors.Is(err, sql.ErrNoRows) {
		status, code = 404, "box.membership_not_found"
	}
	s.writeError(w, cid, status, code, "box", "membership", "Metadata membership could not be verified; dry-run again without broadening or processing content.", nil)
}

func (s Server) handleProjectBoxMembership(w http.ResponseWriter, r *http.Request) {
	ctx, cid, ok := s.boxMembershipRequest(w, r)
	if !ok {
		return
	}
	var input projects.BoxMembershipReconcileInput
	err := decodeBoxMembership(r, &input, "project_ids", "apply", "catalog_digest", "evidence")
	if err == nil {
		err = validateBoxMembershipIDs(input.ProjectIDs, ids.ProjectPrefix, input.Apply, input.CatalogDigest, input.Evidence)
	}
	if err != nil {
		s.writeError(w, cid, 400, "box.membership_invalid_request", "box", "membership", "Supply at most 100 exact project IDs and matching dry-run evidence for apply.", nil)
		return
	}
	if s.services.Projects.DB == nil {
		s.writeError(w, cid, 503, "box.membership_unavailable", "box", "membership", "Project metadata service is unavailable.", nil)
		return
	}
	result, err := s.services.Projects.WithBoxCatalog(*s.services.Boxes).ReconcileBoxMembership(ctx, input)
	if err != nil {
		s.writeBoxMembershipError(w, cid, err)
		return
	}
	response.WriteJSON(w, 200, response.Success(cid, result))
}

func (s Server) handleSourceBoxMembership(w http.ResponseWriter, r *http.Request) {
	ctx, cid, ok := s.boxMembershipRequest(w, r)
	if !ok {
		return
	}
	var input knowledge.SourceBoxMembershipInput
	err := decodeBoxMembership(r, &input, "source_root_ids", "apply", "catalog_digest", "evidence")
	if err == nil {
		err = validateBoxMembershipIDs(input.SourceRootIDs, ids.NotesSourceRootPrefix, input.Apply, input.CatalogDigest, input.Evidence)
	}
	if err != nil {
		s.writeError(w, cid, 400, "box.membership_invalid_request", "box", "membership", "Supply at most 100 exact source-root IDs and matching dry-run evidence for apply.", nil)
		return
	}
	if s.services.DB == nil {
		s.writeError(w, cid, 503, "box.membership_unavailable", "box", "membership", "Knowledge metadata service is unavailable.", nil)
		return
	}
	result, err := knowledge.NewService(s.services.DB).ReconcileSourceBoxMembership(ctx, *s.services.Boxes, input)
	if err != nil {
		s.writeBoxMembershipError(w, cid, err)
		return
	}
	response.WriteJSON(w, 200, response.Success(cid, result))
}

func (s Server) handleBoxMembershipInventory(w http.ResponseWriter, r *http.Request) {
	ctx, cid, ok := s.boxMembershipRequest(w, r)
	if !ok {
		return
	}
	var input struct {
		Kind string `json:"kind"`
		projects.BoxMembershipInventoryInput
	}
	err := decodeBoxMembership(r, &input, "kind", "after", "limit")
	if input.Limit == 0 {
		input.Limit = 100
	}
	prefix := ids.ProjectPrefix
	if input.Kind == "sources" {
		prefix = ids.NotesSourceRootPrefix
	}
	if err != nil || (input.Kind != "projects" && input.Kind != "sources") || input.Limit < 1 || input.Limit > 100 || (input.After != "" && ids.Validate(prefix, input.After) != nil) {
		s.writeError(w, cid, 400, "box.membership_invalid_request", "box", "membership", "Choose projects or sources and a bounded exact-ID inventory page.", nil)
		return
	}
	var page projects.BoxMembershipInventoryPage
	if input.Kind == "projects" {
		if s.services.Projects.DB == nil {
			s.writeError(w, cid, 503, "box.membership_unavailable", "box", "membership", "Project metadata service is unavailable.", nil)
			return
		}
		page, err = s.services.Projects.ListBoxMembershipCandidates(ctx, input.BoxMembershipInventoryInput)
	} else {
		if s.services.DB == nil {
			s.writeError(w, cid, 503, "box.membership_unavailable", "box", "membership", "Knowledge metadata service is unavailable.", nil)
			return
		}
		page, err = knowledge.NewService(s.services.DB).ListSourceBoxMembershipCandidates(ctx, input.BoxMembershipInventoryInput)
	}
	if err != nil {
		s.writeBoxMembershipError(w, cid, err)
		return
	}
	response.WriteJSON(w, 200, response.Success(cid, struct {
		Kind          string `json:"kind"`
		CatalogDigest string `json:"catalog_digest"`
		projects.BoxMembershipInventoryPage
	}{input.Kind, projects.BoxCatalogDigest(*s.services.Boxes), page}))
}
