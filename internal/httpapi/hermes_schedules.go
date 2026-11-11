package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/hermesschedules"
	"loom.local/loom/internal/response"
)

func (s Server) handleHermesSchedules(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", "hermes", "Method is not allowed.", nil)
		return
	}
	for key := range r.URL.Query() {
		if key != "box_id" && key != "box" && key != "all_boxes" {
			s.writeError(w, correlationID, http.StatusBadRequest, "request.invalid_query", "automation", "hermes", "Hermes inventory accepts only the validated Box selection.", nil)
			return
		}
	}
	observation := hermesschedules.Observation{
		Owner: "hermes", Availability: hermesschedules.Unavailable, AttemptedAt: time.Now().UTC(),
	}
	if s.services.HermesSchedules != nil {
		// Failures are represented by availability, never by an empty healthy list
		// or raw reader errors that could contain private paths or payloads.
		observation, _ = s.services.HermesSchedules.Observe(ctx)
	}
	if observation.Availability == hermesschedules.Available {
		if err := s.filterHermesBox(ctx, &observation); err != nil {
			observation.Availability, observation.Jobs = hermesschedules.Unavailable, nil
		}
	}
	envelope := response.Success(correlationID, observation)
	envelope.Meta.Source = "hermes-native-store"
	envelope.Meta.Freshness = string(observation.Availability)
	response.WriteJSON(w, http.StatusOK, envelope)
}

func (s Server) handleHermesWorkspace(w http.ResponseWriter, r *http.Request) {
	_, correlationID, ok := s.filesTransport(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "automation", "hermes", "Use the typed workspace action.", nil)
		return
	}
	var input hermesschedules.WorkspaceRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || input.Source != (hermesschedules.Source{}) {
		s.writeError(w, correlationID, 400, "request.invalid_json", "automation", "hermes", "Invalid native schedule request.", nil)
		return
	}
	if err := s.bindHermesWorkspace(r.Context(), &input); err != nil {
		s.writeError(w, correlationID, 409, "hermes.box_context_refused", "automation", "hermes", "Select the routine's exact Box and registered native profile.", nil)
		return
	}
	if s.services.HermesSchedules == nil {
		s.writeError(w, correlationID, 503, "hermes.unavailable", "automation", "hermes", "Native schedule owner unavailable.", nil)
		return
	}
	observation, err := s.services.HermesSchedules.Observe(r.Context())
	if err != nil || observation.Availability != hermesschedules.Available {
		s.writeError(w, correlationID, 503, "hermes.unavailable", "automation", "hermes", "Native schedule inventory unavailable.", nil)
		return
	}
	result, err := (hermesschedules.ProjectClient{Source: observation.Source}).WorkspaceCall(r.Context(), input)
	if err != nil {
		s.writeError(w, correlationID, 409, "hermes.workspace_action_refused", "automation", "hermes", "Native schedule action refused. Refresh the routine; project-declared and terminal routines stay with their original owner.", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, result))
}

func (s Server) bindHermesWorkspace(ctx context.Context, input *hermesschedules.WorkspaceRequest) error {
	selection, scoped := boxregistry.FromContext(ctx)
	if !scoped {
		if input.BoxID != "" || input.Profile != "mina" {
			return fmt.Errorf("explicit native Box selection required")
		}
		return nil
	}
	if selection.AllBoxes || len(selection.BoxIDs) != 1 || s.services.Boxes == nil {
		return boxregistry.ErrSelection
	}
	selected := selection.BoxIDs[0]
	if input.BoxID != "" && input.BoxID != selected {
		return boxregistry.ErrSelection
	}
	primary, err := s.services.Boxes.Get("box")
	if err != nil {
		return err
	}
	if selected == primary.ID {
		if input.Profile != "" && input.Profile != "mina" {
			return boxregistry.ErrSelection
		}
		input.Profile, input.BoxID = "mina", ""
		return nil
	}
	input.BoxID = selected
	if input.Profile == "mina" || input.Profile == "" && input.Operation != "workspace_info" {
		return boxregistry.ErrSelection
	}
	return nil
}

// Native profile bindings and project membership supply ownership, never titles.
func (s Server) filterHermesBox(ctx context.Context, observation *hermesschedules.Observation) error {
	selection, scoped := boxregistry.FromContext(ctx)
	if !scoped || selection.AllBoxes {
		return nil
	}
	if s.services.Boxes == nil {
		return boxregistry.ErrUnavailable
	}
	primary, err := s.services.Boxes.Get("box")
	if err != nil {
		return err
	}
	projectIDs, seen := []string{}, map[string]bool{}
	for _, job := range observation.Jobs {
		if job.Box == nil && job.Project != nil && !seen[job.Project.ProjectID] {
			seen[job.Project.ProjectID] = true
			projectIDs = append(projectIDs, job.Project.ProjectID)
		}
	}
	memberships, err := s.services.Projects.ApplicationProjectBoxes(ctx, projectIDs)
	if err != nil {
		return err
	}
	filtered := make([]hermesschedules.Job, 0, len(observation.Jobs))
	for _, job := range observation.Jobs {
		owner := ""
		switch {
		case job.Box != nil:
			owner = job.Box.BoxID
		case job.Project != nil:
			owner = memberships[job.Project.ProjectID]
		case job.Profile == "mina" || job.Profile == "" && observation.Source.Profile == "mina":
			owner = primary.ID // The configured root profile, not an unknown project.
		}
		if owner != "" && selection.Contains(owner) {
			filtered = append(filtered, job)
		}
	}
	observation.Jobs = filtered
	return nil
}
