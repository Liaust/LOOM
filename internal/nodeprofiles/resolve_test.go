package nodeprofiles

import "testing"

func TestResolveCanonicalProfiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		input     ResolveInput
		authority string
		runtime   string
	}{
		{
			name:      "main full",
			input:     ResolveInput{NodeKind: "main", NodeRole: "main", RuntimeClass: "main_full"},
			authority: AuthorityMainNodeDefault,
			runtime:   RuntimeMainFull,
		},
		{
			name:      "primary workspace",
			input:     ResolveInput{NodeKind: "workspace", NodeRole: "primary_workspace", RuntimeClass: "workspace_full"},
			authority: AuthorityPrimaryWorkspaceDefault,
			runtime:   RuntimeWorkspaceFull,
		},
		{
			name:      "secondary workspace light",
			input:     ResolveInput{NodeKind: "workspace", NodeRole: "secondary_workspace", RuntimeClass: "workspace_light"},
			authority: AuthoritySecondaryWorkspaceDefault,
			runtime:   RuntimeWorkspaceLight,
		},
		{
			name:      "hardware capability",
			input:     ResolveInput{NodeKind: "hardware", NodeRole: "capability_node", RuntimeClass: "hardware_agent"},
			authority: AuthorityHardwareCapabilityDefault,
			runtime:   RuntimeHardwareAgent,
		},
		{
			name:      "compute runner",
			input:     ResolveInput{NodeKind: "hardware", NodeRole: "compute_node", RuntimeClass: "compute_runner"},
			authority: AuthorityComputeRunnerDefault,
			runtime:   RuntimeComputeRunner,
		},
		{
			name:      "storage edge",
			input:     ResolveInput{NodeKind: "hardware", NodeRole: "storage_node", RuntimeClass: "storage_edge"},
			authority: AuthorityStorageEdgeDefault,
			runtime:   RuntimeStorageEdge,
		},
		{
			name:      "automation edge",
			input:     ResolveInput{NodeKind: "integration", NodeRole: "automation_edge", RuntimeClass: "hardware_agent"},
			authority: AuthorityAutomationEdgeDefault,
			runtime:   RuntimeHardwareAgent,
		},
		{
			name:      "guest",
			input:     ResolveInput{NodeKind: "guest", NodeRole: "guest", RuntimeClass: "guest_restricted"},
			authority: AuthorityGuestRestrictedDefault,
			runtime:   RuntimeGuestRestricted,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assignment, err := Resolve(tt.input)
			if err != nil {
				t.Fatalf("Resolve returned error: %v", err)
			}
			if assignment.AuthorityProfileKey != tt.authority || assignment.RuntimeProfileKey != tt.runtime {
				t.Fatalf("assignment = %#v, want authority=%s runtime=%s", assignment, tt.authority, tt.runtime)
			}
		})
	}
}

func TestResolveLegacyRuntimeAliases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   ResolveInput
		runtime string
	}{
		{
			name:    "workspace runtime alias",
			input:   ResolveInput{NodeKind: "workspace", NodeRole: "workspace", RuntimeClass: "workspace"},
			runtime: RuntimeWorkspaceFull,
		},
		{
			name:    "database capable alias",
			input:   ResolveInput{NodeKind: "workspace", NodeRole: "workspace", RuntimeClass: "database_capable"},
			runtime: RuntimeWorkspaceFull,
		},
		{
			name:    "server main-node alias",
			input:   ResolveInput{NodeKind: "server", NodeRole: "main", RuntimeClass: "main-node"},
			runtime: RuntimeMainFull,
		},
		{
			name:    "legacy guest profile alias",
			input:   ResolveInput{NodeKind: "guest", NodeRole: "guest", RuntimeClass: "guest_restricted_default"},
			runtime: RuntimeGuestRestricted,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assignment, err := Resolve(tt.input)
			if err != nil {
				t.Fatalf("Resolve returned error: %v", err)
			}
			if assignment.RuntimeProfileKey != tt.runtime {
				t.Fatalf("runtime = %s, want %s", assignment.RuntimeProfileKey, tt.runtime)
			}
		})
	}
}

func TestResolveRejectsIncompatibleKnownKinds(t *testing.T) {
	t.Parallel()
	tests := []ResolveInput{
		{NodeKind: "main", NodeRole: "main", RuntimeClass: "hardware_agent"},
		{NodeKind: "workspace", NodeRole: "primary_workspace", RuntimeClass: "main_full"},
		{NodeKind: "hardware", NodeRole: "storage_node", RuntimeClass: "workspace_full"},
		{NodeKind: "guest", NodeRole: "guest", RuntimeClass: "main_full"},
	}
	for _, input := range tests {
		input := input
		t.Run(input.NodeKind+"_"+input.RuntimeClass, func(t *testing.T) {
			t.Parallel()
			if _, err := Resolve(input); err == nil {
				t.Fatalf("Resolve(%#v) returned nil error", input)
			}
		})
	}
}

func TestResolveUnknownKindDefaultsToGuestAuthority(t *testing.T) {
	t.Parallel()
	assignment, err := Resolve(ResolveInput{NodeKind: "sensor", NodeRole: "edge", RuntimeClass: "hardware_agent"})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if assignment.AuthorityProfileKey != AuthorityGuestRestrictedDefault {
		t.Fatalf("authority = %s, want %s", assignment.AuthorityProfileKey, AuthorityGuestRestrictedDefault)
	}
	if len(assignment.Warnings) == 0 {
		t.Fatal("expected warning for unknown node kind")
	}
}

func TestDefaultRoleAndRuntimeClass(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		kind        string
		role        string
		wantRole    string
		wantRuntime string
	}{
		{name: "main", kind: "main", wantRole: "main", wantRuntime: RuntimeMainFull},
		{name: "workspace", kind: "workspace", wantRole: "workspace", wantRuntime: RuntimeWorkspaceFull},
		{name: "secondary workspace", kind: "workspace", role: "secondary_workspace", wantRole: "workspace", wantRuntime: RuntimeWorkspaceLight},
		{name: "hardware", kind: "hardware", wantRole: "capability_node", wantRuntime: RuntimeHardwareAgent},
		{name: "compute", kind: "hardware", role: "compute_node", wantRole: "capability_node", wantRuntime: RuntimeComputeRunner},
		{name: "storage", kind: "hardware", role: "storage_node", wantRole: "capability_node", wantRuntime: RuntimeStorageEdge},
		{name: "guest", kind: "guest", wantRole: "guest", wantRuntime: RuntimeGuestRestricted},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := DefaultRoleForKind(tt.kind); got != tt.wantRole {
				t.Fatalf("DefaultRoleForKind(%q) = %q, want %q", tt.kind, got, tt.wantRole)
			}
			if got := DefaultRuntimeClassFor(tt.kind, tt.role); got != tt.wantRuntime {
				t.Fatalf("DefaultRuntimeClassFor(%q, %q) = %q, want %q", tt.kind, tt.role, got, tt.wantRuntime)
			}
		})
	}
}
