package projectcontracts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/scripts"
)

var capabilityClassIdentPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)

func LoadScriptExposure(path string) (ScriptExposure, []byte, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return ScriptExposure{}, nil, fmt.Errorf("read script exposure: %w", err)
	}
	exposure, err := ParseScriptExposure(payload)
	if err != nil {
		return ScriptExposure{}, nil, err
	}
	return exposure, payload, nil
}

func ParseScriptExposure(payload []byte) (ScriptExposure, error) {
	var exposure ScriptExposure
	if err := yaml.Unmarshal(payload, &exposure); err != nil {
		return ScriptExposure{}, fmt.Errorf("parse script exposure yaml: %w", err)
	}
	return NormalizeScriptExposure(exposure), nil
}

func NormalizeScriptExposure(exposure ScriptExposure) ScriptExposure {
	exposure.Kind = strings.TrimSpace(exposure.Kind)
	exposure.SchemaVersion = strings.TrimSpace(exposure.SchemaVersion)
	exposure.Expose.Provider = strings.TrimSpace(exposure.Expose.Provider)
	exposure.Expose.Endpoint = strings.TrimSpace(exposure.Expose.Endpoint)
	exposure.Expose.DisplayName = strings.TrimSpace(exposure.Expose.DisplayName)
	exposure.Expose.Description = strings.TrimSpace(exposure.Expose.Description)
	exposure.Capability.ClassNamespace = strings.TrimSpace(exposure.Capability.ClassNamespace)
	exposure.Capability.ClassName = strings.TrimSpace(exposure.Capability.ClassName)
	exposure.Capability.Form = strings.TrimSpace(exposure.Capability.Form)
	exposure.Capability.RiskLevel = strings.TrimSpace(exposure.Capability.RiskLevel)
	exposure.Execution.DefaultMode = strings.TrimSpace(exposure.Execution.DefaultMode)
	exposure.Credentials = normalizeCredentialSpec(exposure.Credentials)
	if exposure.Metadata == nil {
		exposure.Metadata = map[string]any{}
	}
	if exposure.Capability.InputSchema == nil {
		exposure.Capability.InputSchema = map[string]any{"type": "object"}
	}
	if exposure.Capability.OutputSchema == nil {
		exposure.Capability.OutputSchema = map[string]any{"type": "object"}
	}
	return exposure
}

func ValidateScriptExposure(project ProjectContract, manifest scripts.Manifest, exposure ScriptExposure) []Diagnostic {
	diagnostics := []Diagnostic{}
	validateScriptExposure(project, manifest.ID, "", exposure, func(diagnostic Diagnostic) {
		diagnostics = append(diagnostics, diagnostic)
	})
	return diagnostics
}

func validateScriptExposure(project ProjectContract, manifestID, path string, exposure ScriptExposure, add func(Diagnostic)) ScriptExposure {
	if exposure.Kind != ScriptExposureKind {
		add(Diagnostic{Severity: SeverityError, Code: "script_exposure.kind_invalid", Message: "script exposure kind must be " + ScriptExposureKind, File: path, Field: "kind"})
	}
	if exposure.SchemaVersion != ScriptExposureSchemaV03 {
		add(Diagnostic{Severity: SeverityError, Code: "script_exposure.schema_version_invalid", Message: "script exposure schema_version must be " + ScriptExposureSchemaV03, File: path, Field: "schema_version"})
	}
	if strings.TrimSpace(exposure.Expose.Provider) == "" {
		exposure.Expose.Provider = "project"
	}
	providerKey := exposure.Expose.Provider
	if providerKey == "project" {
		providerKey = normalizeProviderDefault(project.ProviderDefaults.ScriptsProvider, project.Project.Slug, path, "expose.provider", add)
	} else if !providerKeyPattern.MatchString(providerKey) {
		add(Diagnostic{Severity: SeverityError, Code: "script_exposure.provider_invalid", Message: "script exposure provider must be project or a provider key", File: path, Field: "expose.provider"})
	}
	exposure.Expose.Provider = providerKey

	if exposure.Expose.Endpoint == "" {
		exposure.Expose.Endpoint = manifestID
	}
	if !capabilityClassIdentPattern.MatchString(exposure.Expose.Endpoint) {
		add(Diagnostic{Severity: SeverityError, Code: "script_exposure.endpoint_invalid", Message: "script exposure endpoint must be lowercase dot-separated snake case", File: path, Field: "expose.endpoint"})
	}
	address := project.Project.OwnerNode + "@" + providerKey + "." + exposure.Expose.Endpoint
	if _, err := capabilities.NormalizeAddress(address); err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "script_exposure.address_invalid", Message: "script exposure capability address is invalid: " + err.Error(), File: path, Field: "expose"})
	}

	if exposure.Capability.ClassNamespace == "" {
		exposure.Capability.ClassNamespace = "project"
	}
	if !capabilityClassIdentPattern.MatchString(exposure.Capability.ClassNamespace) {
		add(Diagnostic{Severity: SeverityError, Code: "script_exposure.class_namespace_invalid", Message: "capability class namespace must be lowercase dot-separated snake case", File: path, Field: "capability.class_namespace"})
	}
	if exposure.Capability.ClassName == "" {
		exposure.Capability.ClassName = strings.ReplaceAll(project.Project.Slug, "-", "_")
	}
	if !capabilityClassIdentPattern.MatchString(exposure.Capability.ClassName) {
		add(Diagnostic{Severity: SeverityError, Code: "script_exposure.class_name_invalid", Message: "capability class name must be lowercase dot-separated snake case", File: path, Field: "capability.class_name"})
	}

	if exposure.Capability.Form == "" {
		exposure.Capability.Form = capabilities.CapabilityFormJob
	}
	if exposure.Capability.Form == "action" {
		exposure.Capability.Form = capabilities.CapabilityFormJob
		add(Diagnostic{
			Severity:   SeverityWarning,
			Code:       "script_exposure.form_alias_action",
			Message:    "capability.form action is treated as job for script-backed capabilities",
			File:       path,
			Field:      "capability.form",
			Suggestion: "use capability.form: job",
		})
	}
	if !capabilities.ValidCapabilityForm(exposure.Capability.Form) {
		add(Diagnostic{Severity: SeverityError, Code: "script_exposure.form_invalid", Message: "capability form is not supported", File: path, Field: "capability.form"})
	}
	if exposure.Capability.RiskLevel == "" {
		exposure.Capability.RiskLevel = capabilities.RiskLevelLow
	}
	if !capabilities.ValidRiskLevel(exposure.Capability.RiskLevel) {
		add(Diagnostic{Severity: SeverityError, Code: "script_exposure.risk_invalid", Message: "risk level is not supported", File: path, Field: "capability.risk_level"})
	}
	if exposure.Capability.ExecutionAuthorizationLevel == 0 {
		exposure.Capability.ExecutionAuthorizationLevel = defaultAuthorizationForRisk(exposure.Capability.RiskLevel)
	}
	if !capabilities.ValidExecutionAuthorizationLevel(exposure.Capability.ExecutionAuthorizationLevel) {
		add(Diagnostic{Severity: SeverityError, Code: "script_exposure.authorization_invalid", Message: "execution authorization level must be between 1 and 5", File: path, Field: "capability.execution_authorization_level"})
	}
	if err := validateSchemaMap(exposure.Capability.InputSchema); err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "script_exposure.input_schema_invalid", Message: "input schema is invalid: " + err.Error(), File: path, Field: "capability.input_schema"})
	}
	if err := validateSchemaMap(exposure.Capability.OutputSchema); err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "script_exposure.output_schema_invalid", Message: "output schema is invalid: " + err.Error(), File: path, Field: "capability.output_schema"})
	}
	if exposure.Execution.DefaultMode == "" {
		exposure.Execution.DefaultMode = "wait_until_started"
	}
	if !validScriptExecutionMode(exposure.Execution.DefaultMode) {
		add(Diagnostic{Severity: SeverityError, Code: "script_exposure.execution_mode_invalid", Message: "script exposure execution mode is not supported", File: path, Field: "execution.default_mode"})
	}
	if exposure.Execution.WaitTimeoutSeconds == 0 {
		exposure.Execution.WaitTimeoutSeconds = 120
	}
	if exposure.Execution.WaitTimeoutSeconds < 0 || exposure.Execution.WaitTimeoutSeconds > 86400 {
		add(Diagnostic{Severity: SeverityError, Code: "script_exposure.timeout_invalid", Message: "script exposure wait timeout must be between 1 and 86400 seconds", File: path, Field: "execution.wait_timeout_seconds"})
	}
	exposure.Credentials = validateCredentialSpec(exposure.Credentials, path, "credentials", add)
	return exposure
}

func defaultAuthorizationForRisk(risk string) int {
	switch risk {
	case capabilities.RiskLevelMedium:
		return 2
	case capabilities.RiskLevelHigh:
		return 3
	case capabilities.RiskLevelCritical:
		return 4
	default:
		return 1
	}
}

func validateSchemaMap(schema map[string]any) error {
	raw, err := json.Marshal(schema)
	if err != nil {
		return err
	}
	return capabilities.ValidateSchemaJSON(raw)
}

func validScriptExecutionMode(mode string) bool {
	switch strings.TrimSpace(mode) {
	case "enqueue_only", "wait_until_started", "wait_for_completion":
		return true
	default:
		return false
	}
}

func hashBytesURI(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}
