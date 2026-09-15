package capabilities

import "testing"

func TestNodeSystemProviderAddressSeparatesMain(t *testing.T) {
	for key, want := range map[string]string{
		"main":    "workspace/main@node-agent-system",
		"macbook": "workspace/macbook@system",
	} {
		got := NodeSystemProviderAddress(key)
		parsed, err := ParseProviderAddress(got)
		if err != nil || got != want || parsed.ProviderKey != NodeSystemProviderKey(key) {
			t.Fatalf("provider for %s = %q, %v", key, got, err)
		}
	}
}

func TestParseAddress(t *testing.T) {
	address, err := ParseAddress("main@system.status.read")
	if err != nil {
		t.Fatalf("ParseAddress returned error: %v", err)
	}

	if address.ScopePath != "main" {
		t.Fatalf("ScopePath = %q, want main", address.ScopePath)
	}
	if address.ProviderKey != "system" {
		t.Fatalf("ProviderKey = %q, want system", address.ProviderKey)
	}
	if address.CapabilityName != "status.read" {
		t.Fatalf("CapabilityName = %q, want status.read", address.CapabilityName)
	}
	if address.CompactAddress != "main@system.status.read" {
		t.Fatalf("CompactAddress = %q, want main@system.status.read", address.CompactAddress)
	}
}

func TestParseProviderAddress(t *testing.T) {
	address, err := ParseProviderAddress(" Main@System ")
	if err != nil {
		t.Fatalf("ParseProviderAddress returned error: %v", err)
	}
	if address.ScopePath != "main" {
		t.Fatalf("ScopePath = %q, want main", address.ScopePath)
	}
	if address.ProviderKey != "system" {
		t.Fatalf("ProviderKey = %q, want system", address.ProviderKey)
	}
	if address.CompactAddress != "main@system" {
		t.Fatalf("CompactAddress = %q, want main@system", address.CompactAddress)
	}
}

func TestParseAddressDottedCapabilityAndSlashScope(t *testing.T) {
	address, err := ParseAddress("workspace/project_alpha@obsidian.note.create.daily")
	if err != nil {
		t.Fatalf("ParseAddress returned error: %v", err)
	}

	if address.ScopePath != "workspace/project_alpha" {
		t.Fatalf("ScopePath = %q, want workspace/project_alpha", address.ScopePath)
	}
	if address.ProviderKey != "obsidian" {
		t.Fatalf("ProviderKey = %q, want obsidian", address.ProviderKey)
	}
	if address.CapabilityName != "note.create.daily" {
		t.Fatalf("CapabilityName = %q, want note.create.daily", address.CapabilityName)
	}
}

func TestNormalizeAddress(t *testing.T) {
	normalized, err := NormalizeAddress("  Main/Node-Main@System.Status.Read  ")
	if err != nil {
		t.Fatalf("NormalizeAddress returned error: %v", err)
	}

	if normalized != "main/node-main@system.status.read" {
		t.Fatalf("normalized = %q, want main/node-main@system.status.read", normalized)
	}
}

func TestParseAddressRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "empty", raw: ""},
		{name: "missing at", raw: "main-system.status.read"},
		{name: "multiple at", raw: "main@system@status.read"},
		{name: "missing dot after provider", raw: "main@system"},
		{name: "empty scope", raw: "@system.status.read"},
		{name: "empty provider", raw: "main@.status.read"},
		{name: "empty capability", raw: "main@system."},
		{name: "empty capability segment", raw: "main@system.status..read"},
		{name: "invalid scope segment", raw: "main//@system.status.read"},
		{name: "capability segment with dash", raw: "main@system.status-read"},
		{name: "provider containing invalid character", raw: "main@sys+tem.status.read"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseAddress(tt.raw); err == nil {
				t.Fatal("ParseAddress accepted invalid input")
			}
		})
	}
}
