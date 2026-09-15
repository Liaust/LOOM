package portal

import (
	"context"
	"strings"
	"testing"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/loomcli/actions"
	"loom.local/loom/internal/serviceregistry"
)

func TestLoadServicesScreenRendersTypedInventoryAndProjectSubsection(t *testing.T) {
	client := newFakePortalClient()
	result := LoadScreen(context.Background(), client, "corr_test", ScreenServices, Snapshot{})
	if result.State.Status != ScreenLoadLoaded {
		t.Fatalf("status = %q, want loaded: %#v", result.State.Status, result.State.PartialErrors)
	}
	if len(result.State.Data.Services.Services) != 1 || client.serviceRef != "main@portal-service" {
		t.Fatalf("service data was not loaded: %#v", result.State.Data.Services)
	}
	output := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenServices, State: result.State, Registry: actions.DefaultRegistry(), Width: 72, Height: 30})
	for _, want := range []string{"Service Inventory", "Node: main", "Portal Service", "registry state", "observed process", "local.loom.portal-service", "Bounded Logs"} {
		if !strings.Contains(output, want) {
			t.Fatalf("services output missing %q:\n%s", want, output)
		}
	}

	project := loadProjectsScreenWithSelection(context.Background(), client, "corr_test", Snapshot{}, "project_portal")
	project.State.Data.Projects.Explorer.Level = ProjectExplorerDetail
	projectOutput := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenProjects, State: project.State, Registry: actions.DefaultRegistry(), Width: 72, Height: 30})
	if !strings.Contains(projectOutput, "Services") || !strings.Contains(projectOutput, "Portal Service") {
		t.Fatalf("project detail missing services subsection:\n%s", projectOutput)
	}
}

func TestServiceActionsUseRegisteredCapabilityAndConfirmation(t *testing.T) {
	client := newFakePortalClient()
	state := LoadScreen(context.Background(), client, "corr_test", ScreenServices, Snapshot{}).State
	actionsByOperation := map[string]PortalAction{}
	for _, action := range ScreenAvailableActions(state) {
		if operation := serviceOperationFromAction(action); operation != "" {
			actionsByOperation[operation] = action
		}
	}
	for _, operation := range []string{"status", "start", "stop", "restart", "logs"} {
		action, ok := actionsByOperation[operation]
		if !ok {
			t.Fatalf("missing %s action: %#v", operation, actionsByOperation)
		}
		if action.Executor.Kind != PortalExecutorCapabilityCall || !strings.HasSuffix(action.Executor.Target, ".service."+operation) {
			t.Fatalf("%s action bypasses registered capability: %#v", operation, action)
		}
	}
	if _, err := ExecutePortalAction(context.Background(), client, "corr_test", actionsByOperation["restart"], false); err != ErrActionConfirmationRequired {
		t.Fatalf("restart without confirmation error = %v", err)
	}
	result, err := ExecutePortalAction(context.Background(), client, "corr_test", actionsByOperation["restart"], true)
	if err != nil || result.Status != ActionLifecycleSucceeded {
		t.Fatalf("restart result = %#v, err=%v", result, err)
	}
	if client.callCapability.Target != "main@portal-service.service.restart" {
		t.Fatalf("capability target = %q", client.callCapability.Target)
	}
}

func TestArchivedAndInactiveServicesHideLifecycleActions(t *testing.T) {
	base := serviceregistry.ServiceListItem{ProviderID: "provider_service", ProviderAddress: "main@service", DisplayName: "Service", ScopeID: "scope_archived", RegistryState: serviceregistry.ProviderStateActive, ProcessState: serviceregistry.ProcessStateRunning}
	inspection := serviceregistry.ServiceInspection{ServiceListItem: base, Endpoints: []capabilities.CapabilityEndpoint{
		{EndpointName: "service.status", CompactAddress: "main@service.service.status", Status: capabilities.EndpointStatusActive},
		{EndpointName: "service.restart", CompactAddress: "main@service.service.restart", Status: capabilities.EndpointStatusActive},
	}}
	for _, test := range []struct {
		name     string
		service  serviceregistry.ServiceListItem
		archived bool
	}{
		{name: "archived", service: base, archived: true},
		{name: "disabled", service: func() serviceregistry.ServiceListItem {
			item := base
			item.RegistryState = serviceregistry.ProviderStateDisabled
			return item
		}()},
		{name: "revoked", service: func() serviceregistry.ServiceListItem {
			item := base
			item.RegistryState = serviceregistry.ProviderStateRevoked
			return item
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			inspection.ServiceListItem = test.service
			state := NewScreenState(ScreenServices)
			state.Status = ScreenLoadLoaded
			state.Data.Services = ServicesData{Services: []serviceregistry.ServiceListItem{test.service}, Inspections: map[string]serviceregistry.ServiceInspection{"main@service": inspection}, ArchivedScopeIDs: map[string]bool{"scope_archived": test.archived}}
			operations := map[string]bool{}
			for _, action := range ScreenAvailableActions(state) {
				operations[serviceOperationFromAction(action)] = true
			}
			if operations["restart"] {
				t.Fatalf("lifecycle action exposed for %s", test.name)
			}
			if !operations["status"] {
				t.Fatalf("inspect action missing for %s", test.name)
			}
		})
	}
}

func TestServicesOfflineDoesNotRenderHealthyAndLogsAreBounded(t *testing.T) {
	offline := Snapshot{MainAvailability: MainAvailability{State: MainAvailabilityOffline}}
	state := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenServices, offline).State
	if state.Status != ScreenLoadUnavailable {
		t.Fatalf("offline status = %q, want unavailable", state.Status)
	}
	output := RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenServices, State: state, Registry: actions.DefaultRegistry(), Width: 48, Height: 20})
	if strings.Contains(strings.ToLower(output), "healthy") {
		t.Fatalf("offline screen inferred health:\n%s", output)
	}

	loaded := LoadScreen(context.Background(), newFakePortalClient(), "corr_test", ScreenServices, Snapshot{}).State
	lines := make([]string, portalServiceLogLineLimit+5)
	for index := range lines {
		lines[index] = "bounded line"
	}
	loaded.Data.Services.BoundedLogLines["main@portal-service"] = lines
	output = RenderScreenWithState(RenderInput{Mode: testMode(), Screen: ScreenServices, State: loaded, Registry: actions.DefaultRegistry(), Width: 48, Height: 20})
	if got := strings.Count(output, "bounded line"); got != portalServiceLogLineLimit {
		t.Fatalf("rendered log lines = %d, want %d", got, portalServiceLogLineLimit)
	}
}
