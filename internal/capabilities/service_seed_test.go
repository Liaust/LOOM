package capabilities

import (
	"strings"
	"testing"
)

func TestServiceCapabilityClassInputs(t *testing.T) {
	inputs := ServiceCapabilityClassInputs()
	if len(inputs) != 5 {
		t.Fatalf("classes=%d", len(inputs))
	}
	for index, name := range []string{"status", "start", "stop", "restart", "logs"} {
		input := inputs[index]
		if input.Namespace != "service" || input.Name != name || input.Status != CapabilityClassStatusActive {
			t.Fatalf("class %d: %#v", index, input)
		}
		if (name == "start" || name == "stop" || name == "restart") && input.DefaultRiskLevel != RiskLevelHigh {
			t.Fatalf("mutation risk: %#v", input)
		}
	}
}

func TestProvenanceCapabilityClassInputsAreExplicitAndBounded(t *testing.T) {
	inputs := ProvenanceCapabilityClassInputs()
	if len(inputs) != 4 {
		t.Fatalf("classes=%d", len(inputs))
	}
	want := []struct {
		name string
		form string
		risk string
	}{
		{"health.read", CapabilityFormQuery, RiskLevelLow},
		{"foundation.read", CapabilityFormQuery, RiskLevelLow},
		{"candidate.register", CapabilityFormCommand, RiskLevelMedium},
		{"lifecycle.apply", CapabilityFormCommand, RiskLevelHigh},
	}
	for index, expected := range want {
		input := inputs[index]
		if input.Namespace != "provenance" || input.Name != expected.name || input.Form != expected.form || input.DefaultRiskLevel != expected.risk || input.Status != CapabilityClassStatusActive {
			t.Fatalf("class %d: %#v", index, input)
		}
	}
	addresses := []string{
		ProvenanceHealthReadCapability,
		ProvenanceFoundationReadCapability,
		ProvenanceRegisterCapability,
		ProvenanceLifecycleCapability,
	}
	for index, address := range addresses {
		if address == "" || address != "main@provenance."+want[index].name {
			t.Fatalf("capability %d=%q", index, address)
		}
	}
}

func TestWorkspaceArchiveCapabilitiesAreOperationScopedWithoutRawStorageAuthority(t *testing.T) {
	classes := WorkspaceArchiveCapabilityClassInputs()
	specs := WorkspaceArchiveCapabilitySpecs()
	if len(classes) != 6 || len(specs) != 6 {
		t.Fatalf("classes=%d specs=%d", len(classes), len(specs))
	}
	for index, spec := range specs {
		class := classes[index]
		if spec.Address != "main@workspace-archive."+spec.Name || class.Namespace != "workspace_archive" || class.Name != spec.Name || class.Form != spec.Form || class.DefaultRiskLevel != spec.Risk {
			t.Fatalf("capability %d: spec=%#v class=%#v", index, spec, class)
		}
		if spec.Form == CapabilityFormQuery && spec.AuthorizationLevel != 2 {
			t.Fatalf("query authorization level=%d for %s", spec.AuthorizationLevel, spec.Address)
		}
		if spec.Form == CapabilityFormCommand && spec.AuthorizationLevel != 4 {
			t.Fatalf("mutation authorization level=%d for %s", spec.AuthorizationLevel, spec.Address)
		}
		if strings.Contains(spec.Address, "storage.write") || strings.Contains(spec.Name, "path") || strings.Contains(spec.Name, "filesystem") {
			t.Fatalf("capability implies raw storage authority: %#v", spec)
		}
	}
}
