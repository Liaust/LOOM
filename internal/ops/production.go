package ops

import "strings"

const (
	EnvironmentUnknown    = "unknown"
	EnvironmentDev        = "dev"
	EnvironmentStaging    = "staging"
	EnvironmentProduction = "production"

	HostClassUnknown    = "unknown"
	HostClassDev        = "dev"
	HostClassStaging    = "staging"
	HostClassProduction = "production"
)

type RuntimeIdentity struct {
	Environment   string `json:"environment"`
	NodeID        string `json:"node_id"`
	NodeKind      string `json:"node_kind,omitempty"`
	NodeRole      string `json:"node_role,omitempty"`
	RuntimeClass  string `json:"runtime_class,omitempty"`
	BootstrapMode string `json:"bootstrap_mode,omitempty"`
}

func NormalizeEnvironment(value string) string {
	normalized := normalizeToken(value)
	switch normalized {
	case "", "unknown":
		return EnvironmentUnknown
	case "prod", "production", "live":
		return EnvironmentProduction
	case "stage", "staging", "hardware-main-dev", "hardware_main_dev":
		return EnvironmentStaging
	case "dev", "development", "local", "test":
		return EnvironmentDev
	default:
		if strings.Contains(normalized, "prod") {
			return EnvironmentProduction
		}
		if strings.Contains(normalized, "stage") || strings.Contains(normalized, "staging") || strings.Contains(normalized, "hardware") {
			return EnvironmentStaging
		}
		if strings.Contains(normalized, "dev") || strings.Contains(normalized, "test") {
			return EnvironmentDev
		}
		return normalized
	}
}

func IsProductionEnvironment(value string) bool {
	return NormalizeEnvironment(value) == EnvironmentProduction
}

func IsProductionRuntime(identity RuntimeIdentity) bool {
	if IsProductionEnvironment(identity.Environment) {
		return true
	}
	return normalizeToken(identity.BootstrapMode) == EnvironmentProduction && normalizeToken(identity.NodeID) == "main"
}

func ClassifyHost(host string) string {
	normalized := normalizeToken(host)
	switch normalized {
	case "", "unknown":
		return HostClassUnknown
	case "loom-main":
		return HostClassProduction
	case "loom-dev", "loom-workspace":
		return HostClassDev
	case "loom-hardware", "hardware-main":
		return HostClassStaging
	}
	if strings.Contains(normalized, "vps") {
		return HostClassUnknown
	}
	if strings.Contains(normalized, "prod") || strings.Contains(normalized, "main") {
		return HostClassProduction
	}
	if strings.Contains(normalized, "hardware") || strings.Contains(normalized, "staging") {
		return HostClassStaging
	}
	if strings.Contains(normalized, "dev") || strings.Contains(normalized, "workspace") || strings.Contains(normalized, "test") {
		return HostClassDev
	}
	return HostClassUnknown
}

func IsKnownDevHost(host string) bool {
	return ClassifyHost(host) == HostClassDev
}

func IsKnownProductionHost(host string) bool {
	return ClassifyHost(host) == HostClassProduction
}

func normalizeToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	return value
}
