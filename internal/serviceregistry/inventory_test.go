package serviceregistry

import (
	"encoding/json"
	"testing"
	"time"

	"loom.local/loom/internal/capabilities"
)

func TestServiceInventoryKeepsRegistryAndProcessStatesDistinct(t *testing.T) {
	profile, _ := MarshalRuntimeProfile(RuntimeProfileInput{Manager: ManagerSystemd, Unit: "example.service", ServiceClass: ServiceClassProject})
	inspection, err := ProjectServiceInspection(capabilities.ProviderInspection{Provider: capabilities.Provider{ProviderType: capabilities.ProviderTypeService, Status: capabilities.ProviderStatusDisabled, RuntimeProfileJSON: profile}, Health: &capabilities.ProviderHealth{HealthStatus: capabilities.HealthStatusOK, AvailabilityStatus: capabilities.AvailabilityStatusAvailable, DetailsJSON: json.RawMessage(`{"process_state":"running"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if inspection.RegistryState != ProviderStateDisabled || inspection.ProcessState != ProcessStateRunning {
		t.Fatalf("states conflated: %#v", inspection)
	}
}

func TestServiceListUsesExplicitObservedStateAndTimestamp(t *testing.T) {
	checked := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	item, err := ProjectServiceListItem(capabilities.ProviderListItem{
		Provider:     capabilities.Provider{ProviderType: capabilities.ProviderTypeService, Status: capabilities.ProviderStatusActive},
		HealthStatus: capabilities.HealthStatusUnknown, AvailabilityStatus: capabilities.AvailabilityStatusUnknown,
		HealthDetailsJSON: json.RawMessage(`{"process_state":"failed"}`), LastCheckedAt: &checked,
	})
	if err != nil {
		t.Fatal(err)
	}
	if item.ProcessState != ProcessStateFailed || item.LastObservedAt == nil || !item.LastObservedAt.Equal(checked) {
		t.Fatalf("list observation = %#v", item)
	}
}
