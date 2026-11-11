package hermesschedules

import (
	"context"
	"encoding/json"
	"fmt"
)

type WorkspaceSpec struct {
	Name     string `json:"name"`
	Prompt   string `json:"prompt"`
	Schedule string `json:"schedule"`
}
type WorkspaceRequest struct {
	Operation        string         `json:"operation"`
	Source           Source         `json:"source"`
	Profile          string         `json:"profile"`
	BoxID            string         `json:"box_id,omitempty"`
	ID               string         `json:"id,omitempty"`
	ExpectedRevision string         `json:"expected_revision,omitempty"`
	Token            string         `json:"token,omitempty"`
	Spec             *WorkspaceSpec `json:"spec,omitempty"`
}

// WorkspaceCall uses the existing peer-authenticated native owner. Prompts are
// returned only by explicit detail/action calls, never the schedule observer.
// Callers bind BoxID from trusted selection; the owner resolves profile homes.
func (c ProjectClient) WorkspaceCall(ctx context.Context, req WorkspaceRequest) (json.RawMessage, error) {
	if c.Source.Profile != "mina" || c.Source.Revision != PlatformRevision || c.Source.Host == "" || !validWorkspaceSelection(req) {
		return nil, ErrSource
	}
	req.Source = c.Source
	raw, err := c.roundTrip(ctx, req)
	if err != nil {
		return nil, err
	}
	return decodeWorkspaceResponse(raw, c.Source)
}

func validWorkspaceSelection(req WorkspaceRequest) bool {
	if req.BoxID == "" {
		return req.Profile == "mina"
	}
	return boxIdentity.MatchString(req.BoxID) && (projectKey.MatchString(req.Profile) || (req.Operation == "workspace_info" && req.Profile == ""))
}

func decodeWorkspaceResponse(raw []byte, source Source) (json.RawMessage, error) {
	var out struct {
		Source Source          `json:"source"`
		Jobs   json.RawMessage `json:"jobs"`
		Error  string          `json:"error"`
	}
	if decodeStrict(raw, &out) != nil || out.Source != source {
		return nil, ErrMalformed
	}
	if out.Error != "" {
		return nil, fmt.Errorf("Hermes schedule %s: refresh the exact routine; project-declared or terminal routines must use their original owner", out.Error)
	}
	if len(out.Jobs) == 0 || string(out.Jobs) == "null" {
		return nil, ErrMalformed
	}
	return out.Jobs, nil
}
