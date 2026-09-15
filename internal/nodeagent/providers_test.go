package nodeagent

import (
	"strings"
	"testing"

	"loom.local/loom/internal/filesystemconnector"
	"loom.local/loom/internal/routing"
)

func TestMainAdvertisementDoesNotReplaceDaemonSystemProvider(t *testing.T) {
	config := Config{NodeKey: "main", RuntimeClass: "main_full"}
	provider := buildSystemProviderAdvertisement(config, State{NodeID: "node_main"}).Provider
	if provider.ProviderKey != "node-agent-system" || provider.CompactAddress != "workspace/main@node-agent-system" {
		t.Fatalf("Main provider collides with the core registry: %#v", provider)
	}
	for _, endpoint := range provider.Capabilities {
		if !strings.HasPrefix(endpoint.CompactAddress, provider.CompactAddress+".") {
			t.Fatalf("endpoint has another provider: %s", endpoint.CompactAddress)
		}
		if strings.HasPrefix(endpoint.EndpointName, "project.application.") {
			dispatch := routing.RemoteDispatchPayload{ProviderAddress: provider.CompactAddress, CapabilityAddress: endpoint.CompactAddress, Operation: "capability:" + endpoint.CompactAddress}
			if applicationDispatchOperation(config, dispatch) != strings.TrimPrefix(endpoint.EndpointName, "project.application.") {
				t.Fatalf("Main cannot route %s", endpoint.CompactAddress)
			}
			dispatch.ProviderAddress = "workspace/main@system"
			if applicationDispatchOperation(config, dispatch) != "" {
				t.Fatal("accepted the colliding provider identity")
			}
		}
	}
}

func TestBuildProviderAdvertisementDefaultsToSystem(t *testing.T) {
	t.Parallel()
	input, err := buildProviderAdvertisement(Config{
		NodeKey:      "workspace-test",
		DisplayName:  "Workspace Test",
		RuntimeClass: "workspace",
	}, State{NodeID: "node_test"}, "")
	if err != nil {
		t.Fatalf("buildProviderAdvertisement failed: %v", err)
	}
	if input.Provider.ProviderKey != "system" {
		t.Fatalf("expected system provider, got %q", input.Provider.ProviderKey)
	}
	if len(input.Provider.Capabilities) != 7 {
		t.Fatalf("expected 7 system capabilities, got %d", len(input.Provider.Capabilities))
	}
	prerequisite := input.Provider.Capabilities[6]
	if prerequisite.EndpointName != "project.application.prerequisites" || prerequisite.CompactAddress != "workspace/workspace-test@system.project.application.prerequisites" || prerequisite.ExecutionAuthorizationLevel != 1 || prerequisite.Form != "query" || prerequisite.RiskLevel != "low" || string(prerequisite.SideEffectsJSON) != `{"side_effects":[]}` {
		t.Fatalf("prerequisite endpoint declaration = %#v", prerequisite)
	}
	endpoint := input.Provider.Capabilities[2]
	if endpoint.EndpointName != "project.archive.quiesce" || endpoint.CompactAddress != "workspace/workspace-test@system.project.archive.quiesce" || endpoint.RiskLevel != "high" || endpoint.ExecutionAuthorizationLevel != 5 {
		t.Fatalf("archive quiescence endpoint declaration = %#v", endpoint)
	}
	if !strings.Contains(string(endpoint.ManifestJSON), `"handler":"system.project.archive.quiesce"`) || string(endpoint.SideEffectsJSON) != `{"side_effects":["watched_root.disable","service.stop","project_archive.fence.write","project_archive.receipt.write"]}` {
		t.Fatalf("archive quiescence endpoint metadata = manifest=%s side_effects=%s", endpoint.ManifestJSON, endpoint.SideEffectsJSON)
	}
	if !strings.Contains(string(endpoint.InputSchemaJSON), `"oneOf"`) || !strings.Contains(string(endpoint.InputSchemaJSON), `"additionalProperties":false`) || !strings.Contains(string(endpoint.OutputSchemaJSON), `"project.quiescence_receipt.v1"`) {
		t.Fatalf("archive quiescence endpoint schemas are not exact: input=%s output=%s", endpoint.InputSchemaJSON, endpoint.OutputSchemaJSON)
	}
}

func TestBuildProviderAdvertisementForFilesystem(t *testing.T) {
	t.Parallel()
	config := Config{
		NodeKey:      "workspace-test",
		DisplayName:  "Workspace Test",
		RuntimeClass: "workspace",
		Filesystem: filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{
			filesystemconnector.DefaultSafeRoot("slice13", t.TempDir()),
		}},
	}
	input, err := buildProviderAdvertisement(config, State{NodeID: "node_test"}, "filesystem")
	if err != nil {
		t.Fatalf("buildProviderAdvertisement failed: %v", err)
	}
	if input.Provider.ProviderKey != filesystemconnector.ProviderKey {
		t.Fatalf("expected filesystem provider, got %q", input.Provider.ProviderKey)
	}
	if input.NodeRef != "node_test" {
		t.Fatalf("expected node ref from imported credential, got %q", input.NodeRef)
	}
	if input.Provider.CompactAddress != "workspace/workspace-test@filesystem" {
		t.Fatalf("unexpected provider address %q", input.Provider.CompactAddress)
	}
	if len(input.Provider.Capabilities) != 3 {
		t.Fatalf("expected 3 filesystem capabilities, got %d", len(input.Provider.Capabilities))
	}
}

func TestBuildProviderAdvertisementRejectsFilesystemWithoutSafeRoot(t *testing.T) {
	t.Parallel()
	_, err := buildProviderAdvertisement(Config{
		NodeKey:     "workspace-test",
		DisplayName: "Workspace Test",
	}, State{}, "filesystem")
	if err == nil || !strings.Contains(err.Error(), "safe root") {
		t.Fatalf("expected safe root error, got %v", err)
	}
}
