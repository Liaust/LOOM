package projectactivation

import (
	"context"
	"encoding/json"
	"testing"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/projects"
)

func TestArchiveRecognisesOnlyAuthenticatedDisabledScriptDispatchers(t *testing.T) {
	for _, change := range []string{"none", "provider_active", "endpoint_active", "binding_active", "other_project", "other_scope", "unknown_field", "wrong_owner", "inspection_changed"} {
		t.Run(change, func(t *testing.T) {
			detail := registeredProjectDetail(t, "unused", "sha256:fixture", nil, nil)
			id := detail.Project.Project.ProjectID
			profile, _ := json.Marshal(map[string]any{"runtime": "project_scripts", "project_id": id})
			owner, _ := json.Marshal(map[string]any{"source": "project.activate", "facet": "scripts", "project_id": id})
			item := capabilities.ProviderListItem{Provider: capabilities.Provider{ProviderID: "provider-script", ProviderKey: "script-dispatch", ProviderType: capabilities.ProviderTypeService, CompactAddress: "main@script-dispatch", ScopeID: detail.Project.Project.ProjectScopeID, Status: capabilities.ProviderStatusDisabled, RuntimeProfileJSON: profile, Metadata: owner}}
			endpoint := capabilities.CapabilityEndpoint{CapabilityEndpointID: "endpoint-script", ProviderID: item.ProviderID, Status: capabilities.EndpointStatusDisabled}
			switch change {
			case "provider_active":
				item.Status = capabilities.ProviderStatusActive
			case "endpoint_active":
				endpoint.Status = capabilities.EndpointStatusActive
			case "other_project":
				item.RuntimeProfileJSON = json.RawMessage(`{"runtime":"project_scripts","project_id":"other"}`)
			case "other_scope":
				item.ScopeID = "other"
			case "unknown_field":
				item.RuntimeProfileJSON = json.RawMessage(`{"runtime":"project_scripts","project_id":"other","unit":"real.service"}`)
			case "wrong_owner":
				item.Metadata = json.RawMessage(`{"source":"not-project.activate"}`)
			}
			inspection := capabilities.ProviderInspection{Provider: item.Provider, Endpoints: []capabilities.CapabilityEndpoint{endpoint}}
			if change == "inspection_changed" {
				inspection.Provider.Status = capabilities.ProviderStatusActive
			}
			cap := capabilities.CapabilityInspection{Endpoint: endpoint}
			if change == "binding_active" {
				cap.RuntimeBinding = &capabilities.EndpointRuntimeBinding{Status: capabilities.RuntimeBindingStatusActive}
			}
			caps := &fakeCapabilityService{providerItems: []capabilities.ProviderListItem{item}, providerInspections: map[string]capabilities.ProviderInspection{item.ProviderID: inspection}, capabilityInspections: map[string]capabilities.CapabilityInspection{endpoint.CapabilityEndpointID: cap}}
			s := NewService(Deps{Projects: &fakeProjectService{detail: detail}, Capabilities: caps})
			action, err := s.inactiveProjectScriptArchiveAction(context.Background(), detail, item)
			if change == "none" {
				if err != nil || action == nil || action.Status != "skipped" || action.Ref != "" {
					t.Fatal("disabled dispatcher misclassified", err)
				}
				result, err := s.DeactivateForPhysicalArchive(context.Background(), activationRequest(), "script-smoke", projects.DeactivateProjectInput{Facet: "services", DryRun: true})
				if err != nil || result.Changed || len(result.Actions) != 1 {
					t.Fatal("dispatcher required a fictional host unit", err)
				}
			} else if err == nil {
				t.Fatal("unsafe dispatcher accepted", change)
			}
		})
	}
}
