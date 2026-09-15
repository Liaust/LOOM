package routing

import (
	"encoding/json"
	"loom.local/loom/internal/capabilities"
	"testing"
)

func TestMainApplicationNodeDispatchUsesDedicatedProvider(t *testing.T) {
	for _, operation := range []string{"apply", "inspect", "retire", "prerequisites"} {
		endpoint := "project.application." + operation
		manifest, _ := json.Marshal(map[string]any{"source": "loom-node-agent", "handler": "system." + endpoint, "execution": "remote_node", "explicit_authorization": true, "slice_enabled": true})
		target := plannedTarget{
			ProviderType: capabilities.ProviderTypeSystem, ProviderStatus: capabilities.ProviderStatusActive,
			TargetNodeStatus: "active", EndpointStatus: capabilities.EndpointStatusActive,
			EndpointVersionStatus: capabilities.EndpointVersionStatusActive,
			EndpointName:          endpoint, EndpointVersionManifest: manifest,
			RoutePlan: RoutePlan{ActiveEndpointVersionID: "version-main", ProviderAddress: "workspace/main@node-agent-system", CapabilityAddress: "workspace/main@node-agent-system." + endpoint},
		}
		if !applicationSystemEndpointRequiresNodeDispatch(target) {
			t.Fatalf("Main %s incorrectly falls back to a local runtime binding", operation)
		}
		if got := isApplicationPrerequisiteOperation("capability:" + target.CapabilityAddress); got != (operation == "prerequisites") {
			t.Fatalf("wrong prerequisite classification for %s", operation)
		}
		target.ProviderAddress = "workspace/main@system"
		target.CapabilityAddress = target.ProviderAddress + "." + endpoint
		if applicationSystemEndpointRequiresNodeDispatch(target) || isApplicationPrerequisiteOperation(target.CapabilityAddress) {
			t.Fatal("accepted Main's retired colliding address")
		}
	}
}

func TestApplicationNodeDispatchRequiresActiveExactIdentity(t *testing.T) {
	valid := plannedTarget{ProviderType: capabilities.ProviderTypeSystem, ProviderStatus: capabilities.ProviderStatusActive, TargetNodeStatus: "active", EndpointStatus: capabilities.EndpointStatusActive, EndpointVersionStatus: capabilities.EndpointVersionStatusActive, EndpointName: "project.application.apply", EndpointVersionManifest: json.RawMessage(`{"source":"loom-node-agent","handler":"system.project.application.apply","execution":"remote_node","explicit_authorization":true,"slice_enabled":true}`)}
	valid.ActiveEndpointVersionID = "version-a"
	valid.ProviderAddress = "workspace/fixture@system"
	valid.CapabilityAddress = "workspace/fixture@system.project.application.apply"
	if !applicationSystemEndpointRequiresNodeDispatch(valid) {
		t.Fatal("valid owner endpoint rejected")
	}
	for name, change := range map[string]func(*plannedTarget){"node": func(p *plannedTarget) { p.TargetNodeStatus = "disabled" }, "provider": func(p *plannedTarget) { p.ProviderType = capabilities.ProviderTypeService }, "manifest": func(p *plannedTarget) { p.EndpointVersionManifest = json.RawMessage(`{"source":"loom-node-agent"}`) }, "suffix": func(p *plannedTarget) { p.CapabilityAddress = "workspace/evil@system.project.application.apply" }, "binding": func(p *plannedTarget) { p.RuntimeBindingID = "arbitrary" }, "version": func(p *plannedTarget) { p.ActiveEndpointVersionID = "" }, "unknown": func(p *plannedTarget) { p.EndpointName = "project.application.exec" }} {
		t.Run(name, func(t *testing.T) {
			v := valid
			change(&v)
			if applicationSystemEndpointRequiresNodeDispatch(v) {
				t.Fatal("unqualified endpoint dispatch accepted")
			}
		})
	}
}

func TestApplicationPrerequisiteNodeDispatchClassifier(t *testing.T) {
	valid := plannedTarget{ProviderType: capabilities.ProviderTypeSystem, ProviderStatus: capabilities.ProviderStatusActive, TargetNodeStatus: "active", EndpointStatus: capabilities.EndpointStatusActive, EndpointVersionStatus: capabilities.EndpointVersionStatusActive, EndpointName: "project.application.prerequisites", RoutePlan: RoutePlan{ActiveEndpointVersionID: "v1", ProviderAddress: "workspace/fixture@system", CapabilityAddress: "workspace/fixture@system.project.application.prerequisites"}, EndpointVersionManifest: json.RawMessage(`{"source":"loom-node-agent","handler":"system.project.application.prerequisites","execution":"remote_node","explicit_authorization":true,"slice_enabled":true}`)}
	if !applicationSystemEndpointRequiresNodeDispatch(valid) {
		t.Fatal("prerequisite query not dispatched")
	}
	for name, mutate := range map[string]func(*plannedTarget){
		"disabled_endpoint": func(v *plannedTarget) { v.EndpointStatus = "disabled" },
		"stale_version":     func(v *plannedTarget) { v.EndpointVersionStatus = "retired" },
		"missing_version":   func(v *plannedTarget) { v.ActiveEndpointVersionID = "" },
		"disabled_provider": func(v *plannedTarget) { v.ProviderStatus = "disabled" },
		"inactive_node":     func(v *plannedTarget) { v.TargetNodeStatus = "inactive" },
		"runtime_binding":   func(v *plannedTarget) { v.RuntimeBindingID = "binding" },
		"wrong_address":     func(v *plannedTarget) { v.ProviderAddress = "workspace/other@system" },
	} {
		t.Run(name, func(t *testing.T) {
			v := valid
			mutate(&v)
			if applicationSystemEndpointRequiresNodeDispatch(v) {
				t.Fatal("unqualified query dispatch")
			}
		})
	}
}
