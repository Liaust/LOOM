package hermesschedules

import (
	"context"
	"regexp"
	"time"
)

var nativeProfileID = regexp.MustCompile(`^loom-[0-9a-f]{32}$`)

type Event struct {
	ID        string    `json:"event_id"`
	Type      string    `json:"event_type"`
	JobID     string    `json:"job_id"`
	ProjectID string    `json:"project_id"`
	CreatedAt time.Time `json:"created_at"`
}
type EventInput struct {
	BoxID   string `json:"box_id,omitempty"`
	Profile string `json:"profile"`
	Event   Event  `json:"event"`
}
type EventReceipt struct {
	Box           *BoxOwnership `json:"box,omitempty"`
	NativeJobID   string        `json:"native_job_id"`
	SourceEventID string        `json:"source_event_id"`
	Profile       string        `json:"profile"`
	Delivery      string        `json:"delivery"`
}

func (c ProjectClient) AdmitEvent(ctx context.Context, input EventInput) (EventReceipt, error) {
	if c.Source.Revision != PlatformRevision || c.Source.Profile != "mina" || c.Source.Host == "" || !projectKey.MatchString(input.Profile) || (input.BoxID != "" && !boxIdentity.MatchString(input.BoxID)) || (!projectID.MatchString(input.Event.ProjectID) && !(input.BoxID != "" && input.Event.ProjectID == "")) {
		return EventReceipt{}, ErrSource
	}
	raw, err := c.roundTrip(ctx, struct {
		Operation string `json:"operation"`
		Source    Source `json:"source"`
		ProjectID string `json:"project_id"`
		EventInput
	}{"dispatch_event", c.Source, input.Event.ProjectID, input})
	if err != nil {
		return EventReceipt{}, err
	}
	var out struct {
		Source Source       `json:"source"`
		Event  EventReceipt `json:"event"`
		Error  string       `json:"error,omitempty"`
	}
	if decodeStrict(raw, &out) != nil || out.Source != c.Source {
		return EventReceipt{}, ErrMalformed
	}
	if out.Error != "" {
		return EventReceipt{}, ErrUnavailable
	}
	r := out.Event
	boxOK := input.BoxID != "" && validBoxOwnership(r.Box, input.Profile) && r.Box.BoxID == input.BoxID && r.Profile == input.Profile
	if !identifier.MatchString(r.NativeJobID) || r.SourceEventID != input.Event.ID || (input.BoxID != "" && !boxOK) || (input.BoxID == "" && r.Box != nil) || (r.Profile != "mina" && !nativeProfileID.MatchString(r.Profile) && !boxOK) || (r.Delivery != "queued_native" && r.Delivery != "coalesced" && r.Delivery != "duplicate") {
		return EventReceipt{}, ErrMalformed
	}
	return r, nil
}
