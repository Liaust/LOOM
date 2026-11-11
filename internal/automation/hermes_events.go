package automation

import (
	"encoding/json"
	"fmt"
	"regexp"

	"loom.local/loom/internal/capabilities"
)

const HermesInternalEventsKey = "hermes_internal_events"

var hermesProfileKey = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

func hasHermesEvents(raw json.RawMessage) bool {
	var metadata map[string]json.RawMessage
	_ = json.Unmarshal(raw, &metadata)
	_, ok := metadata[HermesInternalEventsKey]
	return ok
}

// ConfigureHermesEvents builds the fixed internal-only mapping, not a webhook.
func ConfigureHermesEvents(input *CreateDirectEventEndpointInput, profile string, kinds []string) error {
	if !hermesProfileKey.MatchString(profile) || len(kinds) == 0 {
		return fmt.Errorf("internal Hermes events require a profile and event types")
	}
	input.TargetCapability = capabilities.HermesEventCapability
	input.EventType = "loom.internal.job"
	input.ResponseMode = DirectEventResponseAccepted
	input.MappingProfile = mustJSON(MappingProfile{
		Defaults:      map[string]any{"profile": profile},
		FieldMappings: map[string]string{"event": "$.body.event"},
		Required:      []string{"event"},
	})
	input.Metadata = mustJSON(map[string]any{HermesInternalEventsKey: kinds})
	input.MaxAttempts = 1
	return validateHermesEvents(*input)
}

func validateHermesEvents(input CreateDirectEventEndpointInput) error {
	if !hasHermesEvents(input.Metadata) {
		return nil
	}
	var metadata map[string]json.RawMessage
	_ = json.Unmarshal(input.Metadata, &metadata)
	var kinds []string
	if json.Unmarshal(metadata[HermesInternalEventsKey], &kinds) != nil || len(kinds) == 0 || len(kinds) > 2 {
		return fmt.Errorf("Hermes internal events require one or two supported event types")
	}
	seen := map[string]bool{}
	for _, kind := range kinds {
		if (kind != "job.failed" && kind != "job.timed_out") || seen[kind] {
			return fmt.Errorf("unsupported or duplicate Hermes internal event")
		}
		seen[kind] = true
	}
	_, mapping, err := NormalizeMappingProfile(input.MappingProfile)
	if err != nil {
		return err
	}
	var auth []string
	if len(input.AuthProfileRefs) > 0 && json.Unmarshal(input.AuthProfileRefs, &auth) != nil {
		return fmt.Errorf("invalid internal event auth references")
	}
	profile, profileOK := mapping.Defaults["profile"].(string)
	if input.ProjectRef == "" || input.TargetCapability != capabilities.HermesEventCapability || len(auth) != 0 || input.MaxAttempts > 1 || mapping.FieldMappings["event"] != "$.body.event" || len(mapping.FieldMappings) != 1 || len(mapping.Defaults) != 1 || !profileOK || !hermesProfileKey.MatchString(profile) {
		return fmt.Errorf("Hermes internal events require an explicit project, fixed dispatch mapping, no HTTP auth and one attempt")
	}
	return nil
}
