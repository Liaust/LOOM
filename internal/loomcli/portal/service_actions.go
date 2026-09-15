package portal

import (
	"fmt"
	"strings"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/serviceregistry"
)

func servicesSelectableItems(state ScreenState) []SelectableItem {
	data := state.Data.Services
	items := make([]SelectableItem, 0, len(data.Services))
	for _, service := range data.Services {
		inspection, ok := serviceInspection(data, service)
		actions := []PortalAction{}
		if ok {
			archived := data.ArchivedScopeIDs[service.ScopeID]
			for _, endpoint := range inspection.Endpoints {
				operation, valid := serviceEndpointOperation(endpoint)
				if !valid || endpoint.Status != capabilities.EndpointStatusActive {
					continue
				}
				if archived && serviceOperationMutates(operation) {
					continue
				}
				if service.RegistryState != serviceregistry.ProviderStateActive && serviceOperationMutates(operation) {
					continue
				}
				actions = append(actions, newServiceCapabilityAction(service, endpoint, operation))
			}
		}
		items = append(items, SelectableItem{
			Kind:           SelectableKindRecord,
			Label:          firstNonEmpty(service.DisplayName, service.ProviderKey, service.ProviderAddress),
			Description:    fmt.Sprintf("registry=%s process=%s health=%s", service.RegistryState, service.ProcessState, firstNonEmpty(service.HealthStatus, "unknown")),
			Screen:         ScreenServices,
			RecordKind:     "service",
			RecordRef:      firstNonEmpty(service.ProviderAddress, service.ProviderID),
			RecordLabel:    firstNonEmpty(service.DisplayName, service.ProviderKey, service.ProviderAddress),
			RelatedActions: actions,
		})
	}
	return withOperationalActionGroups(state, items)
}

func newServiceCapabilityAction(service serviceregistry.ServiceListItem, endpoint capabilities.CapabilityEndpoint, operation serviceregistry.Operation) PortalAction {
	policy, _ := serviceregistry.StandardOperationPolicy(operation)
	inputJSON := "{}"
	if operation == serviceregistry.OperationLogs {
		inputJSON = `{"lines":100}`
	}
	risk := ActionRiskInspect
	if policy.MutatesState {
		risk = ActionRiskSensitive
	}
	action := PortalAction{
		ID:                  fmt.Sprintf("service.%s.%s", safeActionID(firstNonEmpty(service.ProviderAddress, service.ProviderID)), operation),
		Label:               serviceActionLabel(operation),
		Description:         "Route the bounded service operation through its registered capability endpoint.",
		Domain:              "services",
		SourceScreen:        ScreenServices,
		TargetKind:          "service",
		TargetRef:           firstNonEmpty(service.ProviderAddress, service.ProviderID),
		TargetLabel:         firstNonEmpty(service.DisplayName, service.ProviderKey, service.ProviderAddress),
		Risk:                risk,
		State:               ActionAvailable,
		InputValues:         map[string]string{"input_json": inputJSON},
		Executor:            PortalActionExecutor{Kind: PortalExecutorCapabilityCall, Target: endpoint.CompactAddress, Payload: map[string]string{"operation": string(operation), "service": service.ProviderAddress}},
		ExecutionDependency: ExecutionDependencyMain,
		RawCommand:          []string{"loom", "service", string(operation), firstNonEmpty(service.ProviderAddress, service.ProviderID)},
		RefreshScreen:       ScreenServices,
	}
	if operation == serviceregistry.OperationLogs {
		action.RawCommand = append(action.RawCommand, "--lines", "100")
	}
	if service.RegistryState != serviceregistry.ProviderStateActive {
		action.State = ActionDisabled
		action.DisabledReason = "The service provider is not active."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func serviceEndpointOperation(endpoint capabilities.CapabilityEndpoint) (serviceregistry.Operation, bool) {
	name := strings.TrimSpace(endpoint.EndpointName)
	if name == "" {
		parts := strings.Split(endpoint.CompactAddress, ".")
		if len(parts) >= 2 {
			name = strings.Join(parts[len(parts)-2:], ".")
		}
	}
	name = strings.TrimPrefix(name, "service.")
	operation := serviceregistry.Operation(name)
	_, err := serviceregistry.StandardOperationPolicy(operation)
	return operation, err == nil
}

func serviceOperationFromAction(action PortalAction) string {
	if action.Executor.Payload != nil && action.Executor.Payload["operation"] != "" {
		return action.Executor.Payload["operation"]
	}
	parts := strings.Split(action.ID, ".")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}

func serviceOperationMutates(operation serviceregistry.Operation) bool {
	policy, err := serviceregistry.StandardOperationPolicy(operation)
	return err == nil && policy.MutatesState
}

func serviceActionLabel(operation serviceregistry.Operation) string {
	switch operation {
	case serviceregistry.OperationStatus:
		return "Check Status"
	case serviceregistry.OperationLogs:
		return "Read Logs"
	default:
		return strings.ToUpper(string(operation[:1])) + string(operation[1:]) + " Service"
	}
}

func serviceInspection(data ServicesData, service serviceregistry.ServiceListItem) (serviceregistry.ServiceInspection, bool) {
	inspection, ok := data.Inspections[service.ProviderAddress]
	if !ok {
		inspection, ok = data.Inspections[service.ProviderID]
	}
	return inspection, ok
}
