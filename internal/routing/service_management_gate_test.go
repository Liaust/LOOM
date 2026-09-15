package routing

import (
	"encoding/json"
	"testing"

	"loom.local/loom/internal/capabilities"
)

func TestServiceManagementEndpointHealthBypassIsNarrow(t *testing.T) {
	valid := plannedTarget{
		RoutePlan:             RoutePlan{ActiveEndpointVersionID: "endpoint_version_test"},
		EndpointStatus:        capabilities.EndpointStatusActive,
		ProviderType:          capabilities.ProviderTypeService,
		EndpointName:          "service.restart",
		EndpointMetadata:      json.RawMessage(`{"source":"service_registry","operation":"restart"}`),
		EndpointVersionStatus: capabilities.EndpointVersionStatusActive,
		RuntimeBindingID:      "runtime_binding_test",
		RuntimeBindingKind:    capabilities.RuntimeKindServiceManager,
		RuntimeBindingStatus:  capabilities.RuntimeBindingStatusActive,
	}
	if !serviceManagementEndpointAllowsUnhealthyRouting(valid) {
		t.Fatal("registered service-management endpoint did not bypass circular health gate")
	}
	for name, target := range map[string]plannedTarget{
		"generic provider": withServiceGateTarget(valid, func(target *plannedTarget) { target.ProviderType = capabilities.ProviderTypeConnector }),
		"untrusted metadata": withServiceGateTarget(valid, func(target *plannedTarget) {
			target.EndpointMetadata = json.RawMessage(`{"source":"other","operation":"restart"}`)
		}),
		"operation mismatch": withServiceGateTarget(valid, func(target *plannedTarget) {
			target.EndpointMetadata = json.RawMessage(`{"source":"service_registry","operation":"status"}`)
		}),
		"arbitrary operation": withServiceGateTarget(valid, func(target *plannedTarget) {
			target.EndpointName, target.EndpointMetadata = "service.exec", json.RawMessage(`{"source":"service_registry","operation":"exec"}`)
		}),
		"no active endpoint version":  withServiceGateTarget(valid, func(target *plannedTarget) { target.ActiveEndpointVersionID = "" }),
		"inactive endpoint version":   withServiceGateTarget(valid, func(target *plannedTarget) { target.EndpointVersionStatus = capabilities.EndpointVersionStatusDisabled }),
		"no runtime binding":          withServiceGateTarget(valid, func(target *plannedTarget) { target.RuntimeBindingID = "" }),
		"non service manager binding": withServiceGateTarget(valid, func(target *plannedTarget) { target.RuntimeBindingKind = capabilities.RuntimeKindCommand }),
		"disabled runtime binding":    withServiceGateTarget(valid, func(target *plannedTarget) { target.RuntimeBindingStatus = capabilities.RuntimeBindingStatusDisabled }),
	} {
		t.Run(name, func(t *testing.T) {
			if serviceManagementEndpointAllowsUnhealthyRouting(target) {
				t.Fatalf("unexpected bypass for %#v", target)
			}
		})
	}
}

func withServiceGateTarget(target plannedTarget, mutate func(*plannedTarget)) plannedTarget {
	mutate(&target)
	return target
}
