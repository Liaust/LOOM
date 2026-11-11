package httpapi

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/ids"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
)

func scaffoldBoxHTTPFixture(t *testing.T) (Server, string, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	catalog := boxregistry.Catalog{DefaultBox: "personal"}
	for _, key := range []string{"personal", "company"} {
		root := filepath.Join(base, key)
		result, err := box.Init(box.InitInput{Resolved: box.Resolved{RootPath: root, Profile: box.ProfileMain, OwnerNode: "main", NodeRole: "main", RuntimeStateRoot: filepath.Join(base, "state", key)}, NewBoxID: func() string { return "box_" + key }})
		if err != nil {
			t.Fatal(err)
		}
		catalog.Boxes = append(catalog.Boxes, boxregistry.Box{ID: result.StatusAfter.Contract.BoxID, Key: key, Node: "main", RootPath: root, Available: true})
	}
	s := Server{services: Services{Boxes: &catalog}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return s, catalog.Boxes[0].RootPath, catalog.Boxes[1].RootPath
}

func TestProjectScaffoldBoxForeignSlugAndIfNotExistsCannotReplayBeforeScope(t *testing.T) {
	s, _, company := scaffoldBoxHTTPFixture(t)
	db, store := newHTTPStorageFakeDB(t)
	defer db.Close()
	s.services.Projects = projects.NewService(db)
	stub := &provenanceIdempotencyStub{decision: "replay"}
	s.services.Idempotency = stub
	store.expect("p.metadata->>'home_box_id' IN", nil)
	r := httptest.NewRequest("POST", "/?box=company", strings.NewReader(`{"name":"Same","slug":"same","if_not_exists":true}`)).WithContext(context.WithValue(t.Context(), localFileTransportKey{}, true))
	r.Header.Set("Idempotency-Key", "foreign-receipt")
	w := httptest.NewRecorder()
	s.handleProjectsInBox(w, r)
	if w.Code != 409 || stub.beginInput.Key != "" {
		t.Fatalf("receipt replay preceded scope check: %d %s", w.Code, w.Body.String())
	}
	now := time.Now()
	store.expect("FROM projects.projects p", nil, []driver.Value{ids.NewProjectID(), "scope", "scope:personal", "same", "Same", "", "actor", "actor", nil, nil, nil, "active", "normal", "normal", []byte(`{}`), []byte(`{}`), []byte(`{}`), []byte(`{}`), []byte(`{}`), []byte(`{"home_box_id":"box_personal"}`), now, now})
	r = httptest.NewRequest("POST", "/?box=company", strings.NewReader(`{"name":"Same","mode":"declaration"}`)).WithContext(context.WithValue(t.Context(), localFileTransportKey{}, true))
	w = httptest.NewRecorder()
	s.handleProjectScaffoldsInBox(w, r)
	if w.Code != 409 {
		t.Fatalf("foreign slug accepted before source creation: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(company, "Projects", "same")); !os.IsNotExist(err) {
		t.Fatal("foreign slug collision created source root")
	}
	store.requireDone()
}

func TestProjectScaffoldBoxNormalizesSelectedDestinationBeforeCreation(t *testing.T) {
	s, personal, company := scaffoldBoxHTTPFixture(t)
	r := httptest.NewRequest("POST", "/?box=company", nil)
	selected := boxregistry.ResolvedSelection{BoxIDs: []string{"box_company"}}
	options, root, err := s.normalizeProjectScaffoldInBox(r, pc.ScaffoldOptions{Name: "Same Name", Mode: pc.ScaffoldModeDeclaration}, selected)
	if err != nil || root != filepath.Join(company, "Projects", "same-name") || options.BoxRoot != company {
		t.Fatalf("selected destination: %+v %s %v", options, root, err)
	}
	for _, input := range []pc.ScaffoldOptions{
		{Name: "Same", Directory: filepath.Join(personal, "Projects")},
		{Name: "Same", BoxRoot: personal},
		{Name: "Same", OwnerNode: "foreign"},
		{Name: "Same", Slug: "../personal"},
	} {
		if _, _, err := s.normalizeProjectScaffoldInBox(r, input, selected); err == nil {
			t.Fatalf("foreign/unsafe target accepted: %+v", input)
		}
	}
	alias := filepath.Join(company, "Projects", "foreign")
	if err := os.Symlink(filepath.Join(personal, "Projects"), alias); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.normalizeProjectScaffoldInBox(r, pc.ScaffoldOptions{Name: "Same", Directory: alias}, selected); err == nil {
		t.Fatal("symlink parent crossed Boxes")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("validation created project root")
	}
}

func TestProjectScaffoldBoxRejectsForeignReplayAndUnavailableRegistryBeforeWrites(t *testing.T) {
	s, personal, company := scaffoldBoxHTTPFixture(t)
	for _, input := range []pc.ScaffoldOptions{
		{Name: "Same", Directory: filepath.Join(personal, "Projects"), Force: true},
		{Name: "Same", Mode: pc.ScaffoldModeDeclaration},
	} {
		raw, _ := json.Marshal(input)
		r := httptest.NewRequest("POST", "/?box=company", strings.NewReader(string(raw))).WithContext(context.WithValue(t.Context(), localFileTransportKey{}, true))
		w := httptest.NewRecorder()
		s.handleProjectScaffoldsInBox(w, r)
		if w.Code != 400 && w.Code != 503 {
			t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
		}
		if _, err := os.Stat(filepath.Join(company, "Projects", "same")); !os.IsNotExist(err) {
			t.Fatal("project was created before registry/scope validation")
		}
		if _, err := os.Stat(filepath.Join(personal, "Projects", "same")); !os.IsNotExist(err) {
			t.Fatal("foreign project was created")
		}
	}
}
