package automation

import (
	"encoding/json"
	"testing"
)

func TestHermesInternalEventSubscription(t *testing.T) {
	input := CreateDirectEventEndpointInput{ProjectRef: "project_example"}
	if err := ConfigureHermesEvents(&input, "reader", []string{"job.failed", "job.timed_out"}); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*CreateDirectEventEndpointInput){
		func(v *CreateDirectEventEndpointInput) { v.ProjectRef = "" },
		func(v *CreateDirectEventEndpointInput) { v.TargetCapability = "other" },
		func(v *CreateDirectEventEndpointInput) {
			v.Metadata = json.RawMessage(`{"hermes_internal_events":["job.completed"]}`)
		},
		func(v *CreateDirectEventEndpointInput) { v.AuthProfileRefs = json.RawMessage(`["auth_profile"]`) },
		func(v *CreateDirectEventEndpointInput) { v.MaxAttempts = 2 },
	} {
		bad := input
		change(&bad)
		if validateHermesEvents(bad) == nil {
			t.Fatalf("accepted invalid subscription: %+v", bad)
		}
	}
	if validateHermesEvents(CreateDirectEventEndpointInput{}) != nil {
		t.Fatal("changed ordinary endpoint behavior")
	}
}
