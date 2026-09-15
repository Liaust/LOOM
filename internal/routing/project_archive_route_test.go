package routing

import (
	"encoding/json"
	"testing"

	"loom.local/loom/internal/capabilities"
)

func TestSystemProjectArchiveNodeAgentRouteExceptionIsExact(t *testing.T) {
	valid := plannedTarget{
		RoutePlan: RoutePlan{
			ProviderAddress:         "workspace/main@node-agent-system",
			CapabilityAddress:       "workspace/main@node-agent-system.project.archive.quiesce",
			ActiveEndpointVersionID: "endpv_project_archive",
		},
		ProviderType:            capabilities.ProviderTypeSystem,
		ProviderStatus:          capabilities.ProviderStatusActive,
		EndpointStatus:          capabilities.EndpointStatusActive,
		EndpointVersionStatus:   capabilities.EndpointVersionStatusActive,
		EndpointName:            "project.archive.quiesce",
		EndpointVersionManifest: json.RawMessage(`{"source":"loom-node-agent","handler":"system.project.archive.quiesce","execution":"remote_node","explicit_authorization":true,"slice_enabled":true}`),
	}
	if !projectArchiveSystemEndpointRequiresNodeDispatch(valid) {
		t.Fatal("exact project archive system endpoint did not require node-agent dispatch")
	}

	tests := map[string]func(*plannedTarget){
		"ordinary system endpoint": func(target *plannedTarget) { target.EndpointName = "status.read" },
		"ordinary provider":        func(target *plannedTarget) { target.ProviderType = capabilities.ProviderTypeService },
		"disabled provider":        func(target *plannedTarget) { target.ProviderStatus = capabilities.ProviderStatusDisabled },
		"disabled endpoint":        func(target *plannedTarget) { target.EndpointStatus = capabilities.EndpointStatusDisabled },
		"inactive version":         func(target *plannedTarget) { target.EndpointVersionStatus = capabilities.EndpointVersionStatusDisabled },
		"missing active version":   func(target *plannedTarget) { target.ActiveEndpointVersionID = "" },
		"wrong provider address":   func(target *plannedTarget) { target.ProviderAddress = "main@system" },
		"wrong capability address": func(target *plannedTarget) { target.CapabilityAddress = "workspace/main@system.status.read" },
		"wrong handler": func(target *plannedTarget) {
			target.EndpointVersionManifest = json.RawMessage(`{"source":"loom-node-agent","handler":"system.status.read","execution":"remote_node","explicit_authorization":true,"slice_enabled":true}`)
		},
		"wrong source": func(target *plannedTarget) {
			target.EndpointVersionManifest = json.RawMessage(`{"source":"other","handler":"system.project.archive.quiesce","execution":"remote_node","explicit_authorization":true,"slice_enabled":true}`)
		},
		"wrong execution": func(target *plannedTarget) {
			target.EndpointVersionManifest = json.RawMessage(`{"source":"loom-node-agent","handler":"system.project.archive.quiesce","execution":"local","explicit_authorization":true,"slice_enabled":true}`)
		},
		"authorization not explicit": func(target *plannedTarget) {
			target.EndpointVersionManifest = json.RawMessage(`{"source":"loom-node-agent","handler":"system.project.archive.quiesce","execution":"remote_node","explicit_authorization":false,"slice_enabled":true}`)
		},
		"slice disabled": func(target *plannedTarget) {
			target.EndpointVersionManifest = json.RawMessage(`{"source":"loom-node-agent","handler":"system.project.archive.quiesce","execution":"remote_node","explicit_authorization":true,"slice_enabled":false}`)
		},
		"unreviewed manifest expansion": func(target *plannedTarget) {
			target.EndpointVersionManifest = json.RawMessage(`{"source":"loom-node-agent","handler":"system.project.archive.quiesce","execution":"remote_node","explicit_authorization":true,"slice_enabled":true,"alternate_executor":true}`)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			target := valid
			mutate(&target)
			if projectArchiveSystemEndpointRequiresNodeDispatch(target) {
				t.Fatalf("non-exact endpoint received project archive route exception: %#v", target)
			}
		})
	}
}
