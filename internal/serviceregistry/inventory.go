package serviceregistry

import (
	"encoding/json"
	"fmt"
	"time"

	"loom.local/loom/internal/capabilities"
)

type ServiceFilter struct {
	Limit         int                   `json:"limit,omitempty"`
	NodeRef       string                `json:"node,omitempty"`
	ScopeRef      string                `json:"scope,omitempty"`
	ProjectRef    string                `json:"project,omitempty"`
	RegistryState ProviderRegistryState `json:"registry_state,omitempty"`
	Health        string                `json:"health,omitempty"`
}

type ServiceListItem struct {
	ProviderID         string                `json:"provider_id"`
	ProviderKey        string                `json:"provider_key"`
	ProviderAddress    string                `json:"provider_address"`
	DisplayName        string                `json:"display_name"`
	NodeID             string                `json:"node_id"`
	ScopeID            string                `json:"scope_id"`
	RegistryState      ProviderRegistryState `json:"registry_state"`
	ProcessState       ObservedProcessState  `json:"process_state"`
	HealthStatus       string                `json:"health_status"`
	AvailabilityStatus string                `json:"availability_status"`
	LastObservedAt     *time.Time            `json:"last_observed_at,omitempty"`
	Provisioning       string                `json:"provisioning"`
}

type ServiceInspection struct {
	ServiceListItem
	Description    string                            `json:"description,omitempty"`
	RuntimeProfile RuntimeProfile                    `json:"runtime_profile"`
	Health         *capabilities.ProviderHealth      `json:"health,omitempty"`
	Endpoints      []capabilities.CapabilityEndpoint `json:"endpoints"`
	References     RuntimeReferences                 `json:"references"`
}

func ProjectServiceListItem(item capabilities.ProviderListItem) (ServiceListItem, error) {
	if item.ProviderType != capabilities.ProviderTypeService {
		return ServiceListItem{}, fmt.Errorf("provider is not a service")
	}
	state := ObservedProcessStateFromHealth(item.HealthStatus, item.AvailabilityStatus, item.HealthDetailsJSON)
	return ServiceListItem{ProviderID: item.ProviderID, ProviderKey: item.ProviderKey, ProviderAddress: item.CompactAddress, DisplayName: item.DisplayName, NodeID: item.NodeID, ScopeID: item.ScopeID, RegistryState: ProviderRegistryState(item.Status), ProcessState: state, HealthStatus: item.HealthStatus, AvailabilityStatus: item.AvailabilityStatus, LastObservedAt: item.LastCheckedAt, Provisioning: "external"}, nil
}

func ProjectServiceInspection(inspection capabilities.ProviderInspection) (ServiceInspection, error) {
	if inspection.Provider.ProviderType != capabilities.ProviderTypeService {
		return ServiceInspection{}, fmt.Errorf("provider is not a service")
	}
	var profile RuntimeProfile
	if err := json.Unmarshal(inspection.Provider.RuntimeProfileJSON, &profile); err != nil {
		return ServiceInspection{}, fmt.Errorf("decode service runtime profile: %w", err)
	}
	healthStatus, availability := "unknown", "unknown"
	var checked *time.Time
	if inspection.Health != nil {
		healthStatus = inspection.Health.HealthStatus
		availability = inspection.Health.AvailabilityStatus
		checked = inspection.Health.LastCheckedAt
	}
	details := json.RawMessage(nil)
	if inspection.Health != nil {
		details = inspection.Health.DetailsJSON
	}
	base, err := ProjectServiceListItem(capabilities.ProviderListItem{Provider: inspection.Provider, HealthStatus: healthStatus, AvailabilityStatus: availability, HealthDetailsJSON: details, LastCheckedAt: checked})
	if err != nil {
		return ServiceInspection{}, err
	}
	return ServiceInspection{ServiceListItem: base, Description: inspection.Provider.Description, RuntimeProfile: profile, Health: inspection.Health, Endpoints: inspection.Endpoints, References: profile.References}, nil
}

func ObservedProcessStateFromHealth(health, availability string, details json.RawMessage) ObservedProcessState {
	var payload struct {
		ProcessState ObservedProcessState `json:"process_state"`
	}
	if json.Unmarshal(details, &payload) == nil && ValidObservedProcessState(payload.ProcessState) {
		return payload.ProcessState
	}
	switch health {
	case capabilities.HealthStatusOK:
		return ProcessStateRunning
	case capabilities.HealthStatusUnhealthy:
		return ProcessStateFailed
	case capabilities.HealthStatusOffline:
		return ProcessStateUnavailable
	case capabilities.HealthStatusDegraded:
		if availability == capabilities.AvailabilityStatusUnavailable {
			return ProcessStateStopped
		}
	}
	return ProcessStateUnknown
}
