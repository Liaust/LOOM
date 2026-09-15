package ops

import "testing"

func TestNormalizeEnvironment(t *testing.T) {
	tests := map[string]string{
		"":                  EnvironmentUnknown,
		"production":        EnvironmentProduction,
		"prod":              EnvironmentProduction,
		"hardware-main-dev": EnvironmentStaging,
		"staging":           EnvironmentStaging,
		"dev":               EnvironmentDev,
		"development":       EnvironmentDev,
		"test":              EnvironmentDev,
		"custom-prod-blue":  EnvironmentProduction,
	}
	for input, want := range tests {
		if got := NormalizeEnvironment(input); got != want {
			t.Fatalf("NormalizeEnvironment(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestIsProductionRuntime(t *testing.T) {
	if !IsProductionRuntime(RuntimeIdentity{Environment: "production", NodeID: "main"}) {
		t.Fatal("production environment should be production runtime")
	}
	if !IsProductionRuntime(RuntimeIdentity{BootstrapMode: "production", NodeID: "main"}) {
		t.Fatal("main production bootstrap should be production runtime")
	}
	if IsProductionRuntime(RuntimeIdentity{Environment: "hardware-main-dev", NodeID: "hardware-main", BootstrapMode: "dev"}) {
		t.Fatal("hardware staging runtime should not be production")
	}
}

func TestClassifyHost(t *testing.T) {
	tests := map[string]string{
		"loom-main":      HostClassProduction,
		"loom-dev":       HostClassDev,
		"loom-workspace": HostClassDev,
		"loom-hardware":  HostClassStaging,
		"some-vps":       HostClassUnknown,
		"":               HostClassUnknown,
	}
	for input, want := range tests {
		if got := ClassifyHost(input); got != want {
			t.Fatalf("ClassifyHost(%q) = %q, want %q", input, got, want)
		}
	}
}
