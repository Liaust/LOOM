package nodeagent

import (
	"encoding/json"
	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/routing"
	"loom.local/loom/internal/serviceregistry"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplicationDispatchExactAddressAndGrants(t *testing.T) {
	cfg := Config{NodeKey: "fixture"}
	base := "workspace/fixture@system"
	for _, op := range []string{"inspect", "apply", "retire", "prerequisites"} {
		d := routing.RemoteDispatchPayload{ProviderAddress: base, CapabilityAddress: base + ".project.application." + op, Operation: "capability:" + base + ".project.application." + op}
		if applicationDispatchOperation(cfg, d) != op {
			t.Fatal("exact endpoint rejected")
		}
		d.ProviderAddress = "workspace/other@system"
		if applicationDispatchOperation(cfg, d) != "" {
			t.Fatal("other provider accepted")
		}
	}
	caps := applicationCapabilities(base)
	for _, c := range caps {
		want := 5
		if c.EndpointName == "project.application.inspect" || c.EndpointName == "project.application.prerequisites" {
			want = 1
		}
		if c.ExecutionAuthorizationLevel != want {
			t.Fatal("incorrect grant level")
		}
		var marker map[string]any
		if json.Unmarshal(c.ManifestJSON, &marker) != nil || marker["explicit_authorization"] != true {
			t.Fatal("missing exact marker")
		}
	}
}

func TestApplicationPrerequisiteDispatchRejectsBeforeHelper(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "must-not-create")
	cfg := Config{NodeKey: "fixture", ServiceManager: ServiceManagerConfig{ApplicationSocketPath: filepath.Join(root, "missing.sock")}}
	state := State{NodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV"}
	q := serviceregistry.ApplicationPrerequisiteQuery{SchemaVersion: serviceregistry.ApplicationPrerequisiteSchema, Owner: serviceregistry.ApplicationOwner{ProjectID: "project_01ARZ3NDEKTSV4RRFFQ69G5FAV", NodeID: state.NodeID, Resource: "app"}}
	raw, _ := json.Marshal(q)
	base := "workspace/fixture@system"
	valid := routing.RemoteDispatchPayload{TargetNodeID: state.NodeID, ProviderID: "provider", CapabilityEndpointID: "endpoint", ProviderAddress: base, CapabilityAddress: base + ".project.application.prerequisites", Operation: base + ".project.application.prerequisites", Input: raw}
	for name, mutate := range map[string]func(*routing.RemoteDispatchPayload){
		"owner_node": func(d *routing.RemoteDispatchPayload) {
			d.Input = json.RawMessage(strings.Replace(string(raw), state.NodeID, "node_01ARZ3NDEKTSV4RRFFQ69G5FAX", 1))
		},
		"target_node":     func(d *routing.RemoteDispatchPayload) { d.TargetNodeID = "other" },
		"provider_id":     func(d *routing.RemoteDispatchPayload) { d.ProviderID = "" },
		"endpoint_id":     func(d *routing.RemoteDispatchPayload) { d.CapabilityEndpointID = "" },
		"runtime_binding": func(d *routing.RemoteDispatchPayload) { d.RuntimeBinding = &routing.RuntimeBindingSnapshot{} },
		"wrong_operation": func(d *routing.RemoteDispatchPayload) { d.Operation = "other" },
		"unknown_query_field": func(d *routing.RemoteDispatchPayload) {
			d.Input = json.RawMessage(strings.Replace(string(raw), `"owner":`, `"path":"/private","owner":`, 1))
		},
	} {
		t.Run(name, func(t *testing.T) {
			d := valid
			mutate(&d)
			_, err := executeApplicationDispatch(t.Context(), cfg, state, Store{DataDir: data}, d)
			if err == nil || strings.Contains(err.Error(), "helper.") || strings.Contains(err.Error(), "socket.") {
				t.Fatalf("not rejected before helper: %v", err)
			}
			if _, err := os.Stat(data); !os.IsNotExist(err) {
				t.Fatal("created application/runtime state")
			}
		})
	}
	for _, c := range applicationCapabilities(base) {
		if c.EndpointName != "project.application.prerequisites" {
			continue
		}
		if c.Form != capabilities.CapabilityFormQuery || c.RiskLevel != capabilities.RiskLevelLow || c.ExecutionAuthorizationLevel != 1 {
			t.Fatal("wrong advertisement")
		}
		if !strings.Contains(string(c.InputSchemaJSON), serviceregistry.ApplicationPrerequisiteSchema) || strings.Contains(string(c.InputSchemaJSON), "operation_token") {
			t.Fatal("runtime request substituted")
		}
		if string(c.SideEffectsJSON) != `{"side_effects":[]}` {
			t.Fatal("query advertises application effects")
		}
	}
}
