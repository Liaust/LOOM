package routing

import (
	"encoding/json"
	"testing"

	"loom.local/loom/internal/capabilities"
)

func TestServiceManagerNodeDispatchRequiresRegisteredOwnerEndpoint(t *testing.T) {
	valid := plannedTarget{
		RoutePlan:             RoutePlan{ActiveEndpointVersionID: "endpv_service", TargetNodeID: "node_owner", OriginNodeID: "node_owner"},
		ProviderType:          capabilities.ProviderTypeService,
		ProviderStatus:        capabilities.ProviderStatusActive,
		TargetNodeStatus:      "active",
		EndpointStatus:        capabilities.EndpointStatusActive,
		EndpointVersionStatus: capabilities.EndpointVersionStatusActive,
		EndpointName:          "service.status",
		EndpointMetadata:      json.RawMessage(`{"source":"service_registry","operation":"status"}`),
		RuntimeBindingID:      "rtbind_service",
		RuntimeBindingKind:    capabilities.RuntimeKindServiceManager,
		RuntimeBindingStatus:  capabilities.RuntimeBindingStatusActive,
	}
	for _, operation := range []string{"status", "start", "stop", "restart", "logs"} {
		t.Run(operation, func(t *testing.T) {
			target := valid
			target.EndpointName = "service." + operation
			target.EndpointMetadata = json.RawMessage(`{"source":"service_registry","operation":"` + operation + `"}`)
			if !serviceManagerNodeEndpointRequiresNodeDispatch(target) {
				t.Fatalf("registered %s must dispatch to its owner even when origin equals owner", operation)
			}
		})
	}
	for name, mutate := range map[string]func(*plannedTarget){
		"ordinary local capability": func(p *plannedTarget) { p.EndpointName = "status.read" },
		"prefix only":               func(p *plannedTarget) { p.EndpointMetadata = json.RawMessage(`{}`) },
		"wrong provider":            func(p *plannedTarget) { p.ProviderType = capabilities.ProviderTypeConnector },
		"inactive provider":         func(p *plannedTarget) { p.ProviderStatus = capabilities.ProviderStatusDisabled },
		"inactive node":             func(p *plannedTarget) { p.TargetNodeStatus = "disabled" },
		"inactive endpoint":         func(p *plannedTarget) { p.EndpointStatus = capabilities.EndpointStatusDisabled },
		"missing version":           func(p *plannedTarget) { p.ActiveEndpointVersionID = "" },
		"inactive version":          func(p *plannedTarget) { p.EndpointVersionStatus = capabilities.EndpointVersionStatusDisabled },
		"missing binding":           func(p *plannedTarget) { p.RuntimeBindingID = "" },
		"inactive binding":          func(p *plannedTarget) { p.RuntimeBindingStatus = capabilities.RuntimeBindingStatusDisabled },
		"wrong runtime":             func(p *plannedTarget) { p.RuntimeBindingKind = capabilities.RuntimeKindCommand },
		"unknown operation": func(p *plannedTarget) {
			p.EndpointName = "service.exec"
			p.EndpointMetadata = json.RawMessage(`{"source":"service_registry","operation":"exec"}`)
		},
		"substituted source": func(p *plannedTarget) {
			p.EndpointMetadata = json.RawMessage(`{"source":"other","operation":"status"}`)
		},
		"substituted operation": func(p *plannedTarget) {
			p.EndpointMetadata = json.RawMessage(`{"source":"service_registry","operation":"restart"}`)
		},
		"malformed metadata": func(p *plannedTarget) { p.EndpointMetadata = json.RawMessage(`{`) },
	} {
		t.Run(name, func(t *testing.T) {
			target := valid
			mutate(&target)
			if serviceManagerNodeEndpointRequiresNodeDispatch(target) {
				t.Fatal("unqualified endpoint gained owner-node dispatch")
			}
		})
	}
}
