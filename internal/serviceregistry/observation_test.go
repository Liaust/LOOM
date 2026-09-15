package serviceregistry

import (
	"encoding/json"
	"testing"
	"time"

	"loom.local/loom/internal/capabilities"
)

func TestProviderHealthFromManagerResultMappings(t *testing.T) {
	observedAt := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		state        ObservedProcessState
		health       string
		availability string
		lastOK       bool
	}{
		{ProcessStateRunning, capabilities.HealthStatusOK, capabilities.AvailabilityStatusAvailable, true},
		{ProcessStateStopped, capabilities.HealthStatusDegraded, capabilities.AvailabilityStatusUnavailable, false},
		{ProcessStateFailed, capabilities.HealthStatusUnhealthy, capabilities.AvailabilityStatusUnavailable, false},
		{ProcessStateUnknown, capabilities.HealthStatusUnknown, capabilities.AvailabilityStatusUnknown, false},
	}
	for _, test := range tests {
		t.Run(string(test.state), func(t *testing.T) {
			input, err := ProviderHealthFromManagerResult(ManagerResult{Operation: OperationStatus, Success: true, ProcessState: test.state, Message: "status"}, observedAt)
			if err != nil {
				t.Fatal(err)
			}
			if input.HealthStatus != test.health || input.AvailabilityStatus != test.availability || (input.LastOKAt != nil) != test.lastOK {
				t.Fatalf("projection = %#v", input)
			}
			var details struct {
				ProcessState ObservedProcessState `json:"process_state"`
				ObservedAt   string               `json:"observed_at"`
			}
			if err := json.Unmarshal(input.DetailsJSON, &details); err != nil || details.ProcessState != test.state || details.ObservedAt != observedAt.Format(time.RFC3339Nano) {
				t.Fatalf("details = %s err=%v", input.DetailsJSON, err)
			}
		})
	}
}

func TestDecodeManagerResultRejectsUnknownAndTrailingJSON(t *testing.T) {
	for _, raw := range []string{
		`{"operation":"status","success":true,"process_state":"running","secret":"bad"}`,
		`{"operation":"status","success":true,"process_state":"running"} {}`,
	} {
		if _, err := DecodeManagerResult(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted malformed manager result: %s", raw)
		}
	}
}
