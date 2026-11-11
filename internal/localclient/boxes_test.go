package localclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"loom.local/loom/internal/projectcontracts"
)

func TestBoxContextStaysOnClientInvocation(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Loom-Box") != "company" || r.URL.Query().Get("all_boxes") != "true" {
			t.Errorf("missing Box context %v %s", r.Header, r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"data":{"schema_version":"loom.box_catalog.v1","default_box":"personal","boxes":[]}}`))
	}))
	defer s.Close()
	c, err := NewHTTP(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	bound := c.WithBoxContext("company", true)
	if _, err := bound.Boxes(context.Background(), "corr_box_context"); err != nil {
		t.Fatal(err)
	}
	if c.boxContext != "" || c.allBoxes {
		t.Fatal("binding mutated a shared client")
	}
}

func TestBoxContextSurvivesSpecializedTransports(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("X-Loom-Box") != "box_company" {
			t.Errorf("missing Company context on %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"data":{"project_id":"project_ref","resource":"source","action":"check","state":"ready"}}`)
	}))
	defer server.Close()
	client, err := NewHTTP(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client = client.WithBoxContext("box_company", false)
	ctx := context.Background()
	checks := []func() error{
		func() error {
			_, err := doProvenanceJSON[json.RawMessage](client, ctx, http.MethodGet, "/v1/provenance/search", "corr_box", nil, false)
			return err
		},
		func() error {
			_, err := declarationJSON[projectcontracts.ProjectActionResult](client, ctx, http.MethodPost, "/v1/projects/apply", "corr_box", map[string]string{})
			return err
		},
		func() error {
			_, err := scaffoldJSON(client, ctx, "corr_box", projectcontracts.ScaffoldOptions{})
			return err
		},
		func() error {
			_, err := projectArchiveJSON[json.RawMessage](client, ctx, "corr_box", "project_ref", "plan", map[string]string{})
			return err
		},
		func() error { return client.ReadFile(ctx, "corr_box", "box:box_company", "note.md", 0, 0, io.Discard) },
		func() error {
			return client.ReadArchiveMember(ctx, "corr_box", "appdata_test", "file.zip", "note.pdf", "rev", io.Discard)
		},
	}
	for i, check := range checks {
		if err := check(); err != nil {
			t.Errorf("transport %d: %v", i, err)
		}
	}
	if requests != len(checks) {
		t.Fatalf("expected %d scoped requests, got %d", len(checks), requests)
	}
}
