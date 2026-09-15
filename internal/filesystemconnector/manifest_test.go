package filesystemconnector

import (
	"strings"
	"testing"

	"loom.local/loom/internal/capabilities"
)

func TestBuildProviderIncludesFilesystemCapabilities(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	provider, err := BuildProvider(ProviderManifestInput{
		ScopeSegment: "workspace-test",
		NodeKey:      "workspace-test",
		NodeID:       "node_test",
		DisplayName:  "Workspace Test",
		RuntimeClass: "workspace",
		Version:      "v-test",
		Config: Config{SafeRoots: []SafeRoot{
			DefaultSafeRoot("slice13", dir),
		}},
	})
	if err != nil {
		t.Fatalf("BuildProvider failed: %v", err)
	}
	if provider.ProviderKey != ProviderKey {
		t.Fatalf("unexpected provider key %q", provider.ProviderKey)
	}
	if provider.ProviderType != capabilities.ProviderTypeConnector {
		t.Fatalf("unexpected provider type %q", provider.ProviderType)
	}
	if provider.CompactAddress != "workspace/workspace-test@filesystem" {
		t.Fatalf("unexpected provider address %q", provider.CompactAddress)
	}
	if len(provider.Capabilities) != 3 {
		t.Fatalf("expected three capabilities, got %d", len(provider.Capabilities))
	}
	for _, cap := range provider.Capabilities {
		if !strings.HasPrefix(cap.CompactAddress, provider.CompactAddress+".") {
			t.Fatalf("capability address %q does not belong to provider %q", cap.CompactAddress, provider.CompactAddress)
		}
		if cap.VersionLabel != "v-test" {
			t.Fatalf("unexpected capability version %q", cap.VersionLabel)
		}
	}
	if strings.Contains(string(provider.RuntimeProfileJSON), dir) || strings.Contains(string(provider.Health.DetailsJSON), dir) {
		t.Fatal("provider advertisement leaked an absolute safe root path")
	}
}

func TestBuildProviderRequiresAvailableSafeRoot(t *testing.T) {
	t.Parallel()
	_, err := BuildProvider(ProviderManifestInput{
		ScopeSegment: "workspace-test",
		NodeKey:      "workspace-test",
		Config: Config{SafeRoots: []SafeRoot{
			{
				RootKey:           "private",
				AbsolutePath:      t.TempDir(),
				PrivateBackupOnly: true,
			},
		}},
	})
	if err == nil {
		t.Fatal("expected available safe root error")
	}
}

func TestIngestCapabilityUsesCommandFormAndLevelTwoAuth(t *testing.T) {
	t.Parallel()
	provider, err := BuildProvider(ProviderManifestInput{
		ScopeSegment: "workspace-test",
		NodeKey:      "workspace-test",
		Config:       Config{SafeRoots: []SafeRoot{DefaultSafeRoot("slice13", t.TempDir())}},
	})
	if err != nil {
		t.Fatalf("BuildProvider failed: %v", err)
	}
	var ingest *capabilities.AdvertisedCapability
	for i := range provider.Capabilities {
		if provider.Capabilities[i].EndpointName == EndpointIngestFile {
			ingest = &provider.Capabilities[i]
			break
		}
	}
	if ingest == nil {
		t.Fatal("ingest_file capability not found")
	}
	if ingest.Form != capabilities.CapabilityFormCommand {
		t.Fatalf("expected command form, got %q", ingest.Form)
	}
	if ingest.ExecutionAuthorizationLevel != 2 || ingest.RiskLevel != capabilities.RiskLevelMedium {
		t.Fatalf("unexpected ingest risk/auth: %#v", ingest)
	}
}
