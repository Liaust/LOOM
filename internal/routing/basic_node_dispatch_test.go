package routing

import (
	"encoding/json"
	"testing"
)

func TestSameNodeBasicCapabilitiesUseNodeDispatch(t *testing.T) {
	for _, endpoint := range []string{"echo", "status.read"} {
		v := plannedTarget{ProviderType: "system", ProviderStatus: "active", TargetNodeStatus: "active", EndpointStatus: "active", EndpointVersionStatus: "active", EndpointName: endpoint}
		v.ProviderAddress = "workspace/main@node-agent-system"
		v.CapabilityAddress = v.ProviderAddress + "." + endpoint
		v.ActiveEndpointVersionID = "version-1"
		v.EndpointVersionManifest = json.RawMessage(`{"source":"loom-node-agent","handler":"system.` + endpoint + `","execution":"remote_node","slice_enabled":true}`)
		if !basicSystemEndpointRequiresNodeDispatch(v) {
			t.Fatal("node handler fell back to missing local binding")
		}
		for name, change := range map[string]func(*plannedTarget){
			"wrong_provider":   func(p *plannedTarget) { p.ProviderAddress = "main@system" },
			"wrong_capability": func(p *plannedTarget) { p.CapabilityAddress = "workspace/other@system.echo" },
			"inactive":         func(p *plannedTarget) { p.EndpointStatus = "disabled" },
			"local_binding":    func(p *plannedTarget) { p.RuntimeBindingID = "local" },
			"other_handler":    func(p *plannedTarget) { p.EndpointName = "exec" },
			"manifest":         func(p *plannedTarget) { p.EndpointVersionManifest = json.RawMessage(`{}`) },
		} {
			t.Run(endpoint+"/"+name, func(t *testing.T) {
				bad := v
				change(&bad)
				if basicSystemEndpointRequiresNodeDispatch(bad) {
					t.Fatal("unqualified node dispatch")
				}
			})
		}
	}
}
