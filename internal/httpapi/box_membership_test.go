package httpapi

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/projects"
)

func membershipHTTPServer() Server {
	catalog := boxregistry.Catalog{DefaultBox: "personal", Boxes: []boxregistry.Box{{ID: "box_personal", Key: "personal", RootRef: "box", Available: true}, {ID: "box_company", Key: "company", RootRef: "box:box_company", Available: true}}}
	return Server{services: Services{Boxes: &catalog}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestBoxMembershipHTTPInventoryPagesOnlyRegistryIDs(t *testing.T) {
	s := membershipHTTPServer()
	db, store := newHTTPStorageFakeDB(t)
	defer db.Close()
	s.services.DB = db
	s.services.Projects = projects.NewService(db)
	for _, kind := range []string{"projects", "sources"} {
		first, second := ids.NewProjectID(), ids.NewProjectID()
		query := "SELECT project_id FROM projects.projects WHERE project_id>$1 ORDER BY project_id LIMIT $2"
		if kind == "sources" {
			first, second = ids.NewNotesSourceRootID(), ids.NewNotesSourceRootID()
			query = "SELECT notes_source_root_id FROM knowledge.notes_source_roots WHERE notes_source_root_id>$1 ORDER BY notes_source_root_id LIMIT $2"
		}
		store.expect(query, func(args []driver.NamedValue) {
			if len(args) != 2 || args[0].Value != "" || args[1].Value != int64(2) {
				t.Fatalf("unbounded inventory args: %v", args)
			}
		}, []driver.Value{first}, []driver.Value{second})
		r := httptest.NewRequest("POST", "/?all_boxes=true", strings.NewReader(`{"kind":"`+kind+`","limit":1}`)).WithContext(context.WithValue(t.Context(), localFileTransportKey{}, true))
		r.Header.Set("X-Loom-Box", "company")
		w := httptest.NewRecorder()
		s.handleBoxMembershipInventory(w, r)
		var result struct {
			Data struct {
				projects.BoxMembershipInventoryPage
				CatalogDigest string `json:"catalog_digest"`
			} `json:"data"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Data.IDs) != 1 || result.Data.IDs[0] != first || result.Data.NextAfter != first || result.Data.CatalogDigest != projects.BoxCatalogDigest(*s.services.Boxes) {
			t.Fatalf("inventory envelope/page: %d %s", w.Code, w.Body.String())
		}
	}
	store.requireDone()
}

func TestBoxMembershipHTTPTransportSelectionAndMethods(t *testing.T) {
	s := membershipHTTPServer()
	for _, handler := range []http.HandlerFunc{s.handleProjectBoxMembership, s.handleSourceBoxMembership, s.handleBoxMembershipInventory} {
		for _, tc := range []struct {
			method, query string
			unix          bool
			want          int
		}{
			{"GET", "?all_boxes=true", true, 405}, {"POST", "?all_boxes=true", false, 403},
			{"POST", "", true, 400}, {"POST", "?box=company", true, 400}, {"POST", "?all_boxes=false", true, 400},
			{"POST", "?all_boxes=true&all_boxes=true", true, 400}, {"POST", "?all_boxes=true&box=company", true, 400}, {"POST", "?all_boxes=true&bad=%zz", true, 400},
		} {
			r := httptest.NewRequest(tc.method, "/v1/boxes/membership"+tc.query, strings.NewReader(`{}`))
			r.Header.Set("X-Root-Peer", "true")
			r.Header.Set("X-Loom-Local-Transport", "true")
			if tc.unix {
				r = r.WithContext(context.WithValue(r.Context(), localFileTransportKey{}, true))
			}
			w := httptest.NewRecorder()
			handler(w, r)
			if w.Code != tc.want || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("%s %s: %d %s", tc.method, tc.query, w.Code, w.Body.String())
			}
		}
	}
	r := httptest.NewRequest("POST", "/?all_boxes=true", nil).WithContext(context.WithValue(t.Context(), localFileTransportKey{}, true))
	ctx, _, ok := s.boxMembershipRequest(httptest.NewRecorder(), r)
	selection, bound := boxregistry.FromContext(ctx)
	if !ok || !bound || !selection.AllBoxes {
		t.Fatal("admin inventory was not explicitly all-Box")
	}
}

func TestBoxMembershipHTTPExactInputsAndUnavailableServices(t *testing.T) {
	s := membershipHTTPServer()
	project, source := ids.NewProjectID(), ids.NewNotesSourceRootID()
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		handler http.HandlerFunc
		body    any
		want    int
	}{
		{s.handleProjectBoxMembership, map[string]any{"project_ids": []string{project}}, 503},
		{s.handleSourceBoxMembership, map[string]any{"source_root_ids": []string{source}}, 503},
		{s.handleProjectBoxMembership, map[string]any{"project_ids": []string{project}, "apply": true, "catalog_digest": digest, "evidence": map[string]string{project: digest}}, 503},
		{s.handleProjectBoxMembership, map[string]any{"project_ids": []string{project}, "apply": true, "catalog_digest": digest}, 400},
		{s.handleProjectBoxMembership, map[string]any{"project_ids": []string{project, project}}, 400},
		{s.handleProjectBoxMembership, map[string]any{"project_ids": []string{"slug-not-an-id"}}, 400},
		{s.handleSourceBoxMembership, map[string]any{"source_root_ids": []string{project}}, 400},
		{s.handleSourceBoxMembership, map[string]any{"source_root_ids": []string{source}, "evidence": map[string]string{"foreign": digest}}, 400},
		{s.handleBoxMembershipInventory, map[string]any{"kind": "projects", "limit": 100}, 503},
		{s.handleBoxMembershipInventory, map[string]any{"kind": "sources", "limit": 100}, 503},
		{s.handleBoxMembershipInventory, map[string]any{"kind": "projects", "limit": 101}, 400},
		{s.handleBoxMembershipInventory, map[string]any{"kind": "objects", "limit": 1}, 400},
	} {
		raw, _ := json.Marshal(tc.body)
		r := httptest.NewRequest("POST", "/?all_boxes=true", strings.NewReader(string(raw))).WithContext(context.WithValue(t.Context(), localFileTransportKey{}, true))
		w := httptest.NewRecorder()
		tc.handler(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s: %d %s", raw, w.Code, w.Body.String())
		}
	}
	for _, raw := range []string{`null`, `{"project_ids":[]}{}`, `{"project_ids":[],"project_ids":[]}`, `{"PROJECT_IDS":[]}`, `{"project_ids":null}`, `{"project_ids":[],"allow":true}`, strings.Repeat(" ", 65537)} {
		r := httptest.NewRequest("POST", "/?all_boxes=true", strings.NewReader(raw)).WithContext(context.WithValue(t.Context(), localFileTransportKey{}, true))
		w := httptest.NewRecorder()
		s.handleProjectBoxMembership(w, r)
		if w.Code != 400 {
			t.Fatalf("invalid body accepted: %d", w.Code)
		}
	}
}
