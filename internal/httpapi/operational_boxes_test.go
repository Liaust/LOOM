package httpapi

import (
	"context"
	"database/sql"
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/hermesschedules"
)

func TestScheduleOverviewWrongBoxCursorIsRequestErrorNotOutage(t *testing.T) {
	cursor := base64.RawURLEncoding.EncodeToString([]byte(`{"created_at":"2026-11-08T12:00:00Z","schedule_id":"schedule_one","selection_digest":"other"}`))
	for _, value := range []string{cursor, "malformed"} {
		r := httptest.NewRequest("GET", "/v1/schedules/overview?cursor="+value, nil)
		ctx := context.WithValue(r.Context(), localFileTransportKey{}, true)
		r = r.WithContext(boxregistry.WithSelection(ctx, boxregistry.ResolvedSelection{BoxIDs: []string{"box_company"}}))
		w := httptest.NewRecorder()
		// Cursor rejection happens before an owner storage query.
		NewServer(Services{DB: &sql.DB{}}, slog.Default()).handleWorkspaceOverview(w, r)
		if w.Code != 400 {
			t.Fatalf("cursor status = %d: %s", w.Code, w.Body.String())
		}
	}
}

func operationalBoxes() boxregistry.Catalog {
	return boxregistry.Catalog{DefaultBox: "personal", Boxes: []boxregistry.Box{
		{ID: "box_personal", Key: "personal", RootRef: "box", Available: true},
		{ID: "box_company", Key: "company", RootRef: "box:box_company", Available: true},
	}}
}

func TestOperationalRoutesBindSelectionBeforeOwnersAndIdempotency(t *testing.T) {
	catalog := operationalBoxes()
	s := Server{services: Services{Boxes: &catalog}}
	for _, route := range []string{"/v1/jobs", "/v1/jobs/job_one/logs", "/v1/jobs/overview", "/v1/schedules/overview", "/v1/schedules/hermes", "/v1/schedules/hermes/workspace", "/v1/schedules/schedule_one/pause", "/v1/schedule-fires/fire_one", "/v1/automations/one", "/v1/invocations/one", "/v1/scripts/source/run"} {
		r := httptest.NewRequest("POST", route+"?box=company", nil)
		out := httptest.NewRecorder()
		called := false
		s.withBoxContext(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			selected, ok := boxregistry.FromContext(r.Context())
			if !ok || !selected.Contains("box_company") || selected.Contains("box_personal") {
				t.Fatalf("%s selection = %+v", route, selected)
			}
			if s.boxIdempotencyInput(r.Context(), "same") == "same" {
				t.Fatal("Company idempotency lost its Box context")
			}
		})).ServeHTTP(out, r)
		if !called || out.Code != 200 {
			t.Fatalf("route %s did not reach its scoped owner: %d", route, out.Code)
		}
	}
}

func TestHermesWorkspaceRequiresExactSelectedBoxAndProfile(t *testing.T) {
	catalog := operationalBoxes()
	s := Server{services: Services{Boxes: &catalog}}
	for _, tc := range []struct {
		name, selected, profile, bodyBox, operation string
		all, refuse                                 bool
	}{
		{name: "personal-default", selected: "box_personal", operation: "workspace_info"},
		{name: "company-discovery", selected: "box_company", operation: "workspace_info"},
		{name: "company-action", selected: "box_company", profile: "tyler", operation: "workspace_create"},
		{name: "company-not-mina", selected: "box_company", profile: "mina", operation: "workspace_detail", refuse: true},
		{name: "company-body-conflict", selected: "box_company", profile: "tyler", bodyBox: "box_personal", operation: "workspace_create", refuse: true},
		{name: "personal-not-tyler", selected: "box_personal", profile: "tyler", operation: "workspace_detail", refuse: true},
		{name: "company-action-not-inferred", selected: "box_company", operation: "workspace_pause", refuse: true},
		{name: "main-wide-is-not-creation-scope", all: true, profile: "mina", operation: "workspace_create", refuse: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selection := boxregistry.ResolvedSelection{AllBoxes: tc.all}
			if tc.selected != "" {
				selection.BoxIDs = []string{tc.selected}
			}
			ctx := boxregistry.WithSelection(t.Context(), selection)
			input := hermesschedules.WorkspaceRequest{Operation: tc.operation, Profile: tc.profile, BoxID: tc.bodyBox}
			err := s.bindHermesWorkspace(ctx, &input)
			if (err != nil) != tc.refuse {
				t.Fatalf("input=%+v err=%v", input, err)
			}
			if err == nil && tc.selected == "box_company" && input.BoxID != tc.selected {
				t.Fatal("Company context not bound")
			}
			if err == nil && tc.selected == "box_personal" && (input.Profile != "mina" || input.BoxID != "") {
				t.Fatal("Personal compatibility changed")
			}
		})
	}
}

func TestHermesObservationFiltersBoxOwnershipWithoutGuessingUnknownProfiles(t *testing.T) {
	catalog := operationalBoxes()
	s := Server{services: Services{Boxes: &catalog}}
	for _, selected := range []string{"box_personal", "box_company"} {
		observation := hermesschedules.Observation{Source: hermesschedules.Source{Profile: "mina"}, Jobs: []hermesschedules.Job{
			{ID: "personal", Profile: "mina"},
			{ID: "company", Profile: "tyler", Box: &hermesschedules.BoxOwnership{BoxID: "box_company"}},
			{ID: "unknown", Profile: "unassigned"},
		}}
		ctx := boxregistry.WithSelection(context.Background(), boxregistry.ResolvedSelection{BoxIDs: []string{selected}})
		if err := s.filterHermesBox(ctx, &observation); err != nil {
			t.Fatal(err)
		}
		if len(observation.Jobs) != 1 {
			t.Fatalf("%s inventory=%+v", selected, observation.Jobs)
		}
		if selected == "box_company" && observation.Jobs[0].ID != "company" {
			t.Fatal("Personal record leaked")
		}
		if selected == "box_personal" && observation.Jobs[0].ID != "personal" {
			t.Fatal("Company record leaked")
		}
	}
}
