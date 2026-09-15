package nodeprofiles

import (
	"fmt"
	"strings"
)

func Resolve(input ResolveInput) (Assignment, error) {
	kind, knownKind, kindWarnings := normalizeNodeKind(input.NodeKind)
	role := normalizeNodeRole(input.NodeRole, kind)
	runtimeProfile, err := normalizeRuntimeProfile(input.RuntimeClass)
	if err != nil {
		return Assignment{}, err
	}

	warnings := append([]Diagnostic{}, kindWarnings...)
	authorityProfile := resolveAuthorityProfile(kind, role, &warnings)
	if err := validateCompatibility(kind, knownKind, runtimeProfile); err != nil {
		return Assignment{}, err
	}

	return Assignment{
		AuthorityProfileKey: authorityProfile,
		RuntimeProfileKey:   runtimeProfile,
		Warnings:            warnings,
	}, nil
}

func DefaultRoleForKind(nodeKind string) string {
	kind, _, _ := normalizeNodeKind(nodeKind)
	return normalizeNodeRole("", kind)
}

func DefaultRuntimeClassFor(nodeKind, nodeRole string) string {
	kind, _, _ := normalizeNodeKind(nodeKind)
	role := normalizeNodeRole(nodeRole, kind)
	switch kind {
	case "main":
		return RuntimeMainFull
	case "workspace":
		if role == "secondary_workspace" {
			return RuntimeWorkspaceLight
		}
		return RuntimeWorkspaceFull
	case "hardware":
		switch role {
		case "compute_node":
			return RuntimeComputeRunner
		case "storage_node":
			return RuntimeStorageEdge
		default:
			return RuntimeHardwareAgent
		}
	case "integration":
		return RuntimeHardwareAgent
	case "guest":
		return RuntimeGuestRestricted
	default:
		return RuntimeGuestRestricted
	}
}

func normalizeNodeKind(raw string) (string, bool, []Diagnostic) {
	value := normalizeToken(raw)
	switch value {
	case "":
		return "workspace", true, nil
	case "main":
		return "main", true, nil
	case "server":
		return "main", true, []Diagnostic{{
			Severity: SeverityWarning,
			Code:     "node_profile.node_kind_legacy_alias",
			Message:  "node_kind server is treated as main for profile resolution",
			Field:    "node_kind",
		}}
	case "workspace":
		return "workspace", true, nil
	case "hardware":
		return "hardware", true, nil
	case "guest":
		return "guest", true, nil
	case "integration":
		return "integration", true, nil
	default:
		return value, false, []Diagnostic{{
			Severity: SeverityWarning,
			Code:     "node_profile.node_kind_unknown",
			Message:  fmt.Sprintf("node_kind %q is not canonical; authority defaults to guest restricted", value),
			Field:    "node_kind",
		}}
	}
}

func normalizeNodeRole(raw, kind string) string {
	value := normalizeToken(raw)
	if value != "" {
		return value
	}
	switch kind {
	case "main":
		return "main"
	case "hardware":
		return "capability_node"
	case "integration":
		return "automation_edge"
	case "guest":
		return "guest"
	default:
		return "workspace"
	}
}

func normalizeRuntimeProfile(raw string) (string, error) {
	value := normalizeToken(raw)
	switch value {
	case "":
		return RuntimeWorkspaceFull, nil
	case RuntimeMainFull:
		return RuntimeMainFull, nil
	case "main-node", "main_node", RuntimeMainNodeDefault:
		return RuntimeMainFull, nil
	case RuntimeWorkspaceFull:
		return RuntimeWorkspaceFull, nil
	case "workspace", "database_capable", RuntimeOwnedWorkspaceDefault:
		return RuntimeWorkspaceFull, nil
	case RuntimeWorkspaceLight:
		return RuntimeWorkspaceLight, nil
	case RuntimeHardwareAgent:
		return RuntimeHardwareAgent, nil
	case RuntimeComputeRunner:
		return RuntimeComputeRunner, nil
	case RuntimeStorageEdge:
		return RuntimeStorageEdge, nil
	case RuntimeGuestRestricted, RuntimeGuestRestrictedLegacy, "guest":
		return RuntimeGuestRestricted, nil
	default:
		return "", fmt.Errorf("unsupported runtime_class: %s", value)
	}
}

func resolveAuthorityProfile(kind, role string, warnings *[]Diagnostic) string {
	switch kind {
	case "main":
		return AuthorityMainNodeDefault
	case "workspace":
		switch role {
		case "primary_workspace":
			return AuthorityPrimaryWorkspaceDefault
		case "secondary_workspace", "workspace":
			return AuthoritySecondaryWorkspaceDefault
		default:
			*warnings = append(*warnings, Diagnostic{
				Severity: SeverityWarning,
				Code:     "node_profile.node_role_unknown",
				Message:  fmt.Sprintf("workspace node_role %q defaults to secondary workspace authority", role),
				Field:    "node_role",
			})
			return AuthoritySecondaryWorkspaceDefault
		}
	case "hardware":
		switch role {
		case "capability_node", "hardware":
			return AuthorityHardwareCapabilityDefault
		case "compute_node":
			return AuthorityComputeRunnerDefault
		case "storage_node":
			return AuthorityStorageEdgeDefault
		default:
			*warnings = append(*warnings, Diagnostic{
				Severity: SeverityWarning,
				Code:     "node_profile.node_role_unknown",
				Message:  fmt.Sprintf("hardware node_role %q defaults to hardware capability authority", role),
				Field:    "node_role",
			})
			return AuthorityHardwareCapabilityDefault
		}
	case "integration":
		switch role {
		case "automation_edge", "integration":
			return AuthorityAutomationEdgeDefault
		default:
			*warnings = append(*warnings, Diagnostic{
				Severity: SeverityWarning,
				Code:     "node_profile.node_role_unknown",
				Message:  fmt.Sprintf("integration node_role %q defaults to automation edge authority", role),
				Field:    "node_role",
			})
			return AuthorityAutomationEdgeDefault
		}
	case "guest":
		return AuthorityGuestRestrictedDefault
	default:
		return AuthorityGuestRestrictedDefault
	}
}

func validateCompatibility(kind string, knownKind bool, runtimeProfile string) error {
	if !knownKind {
		return nil
	}
	valid := map[string]map[string]bool{
		"main": {
			RuntimeMainFull: true,
		},
		"workspace": {
			RuntimeWorkspaceFull:  true,
			RuntimeWorkspaceLight: true,
		},
		"hardware": {
			RuntimeHardwareAgent: true,
			RuntimeComputeRunner: true,
			RuntimeStorageEdge:   true,
		},
		"integration": {
			RuntimeHardwareAgent:   true,
			RuntimeGuestRestricted: true,
		},
		"guest": {
			RuntimeGuestRestricted: true,
		},
	}
	if valid[kind][runtimeProfile] {
		return nil
	}
	return fmt.Errorf("runtime_class %s is not compatible with node_kind %s", runtimeProfile, kind)
}

func normalizeToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
