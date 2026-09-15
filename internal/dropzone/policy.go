package dropzone

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadPolicy decodes a legacy policy only to locate and explain historical
// state. The returned policy is always forced to the retired runtime state.
func LoadPolicy(path string, profile string) (Policy, []Diagnostic) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return DefaultPolicy(profile), []Diagnostic{{
			Severity:   "warning",
			Code:       "dropzone.policy_unavailable",
			Message:    err.Error(),
			Path:       path,
			Suggestion: "historical transfer records remain inspectable from the node-owned Dropzone state root",
		}}
	}
	policy, err := ParsePolicy(payload, profile)
	if err != nil {
		return DefaultPolicy(profile), []Diagnostic{{
			Severity:   "error",
			Code:       "dropzone.policy_invalid",
			Message:    err.Error(),
			Path:       path,
			Suggestion: "preserve the legacy policy as evidence; active Dropzone runtime cannot be re-enabled",
		}}
	}
	diagnostics := ValidatePolicy(policy, profile)
	for i := range diagnostics {
		if diagnostics[i].Path == "" {
			diagnostics[i].Path = path
		}
	}
	return policy, diagnostics
}

func ParsePolicy(payload []byte, profile string) (Policy, error) {
	var policy Policy
	if err := yaml.Unmarshal(payload, &policy); err != nil {
		return Policy{}, fmt.Errorf("parse historical Dropzone policy yaml: %w", err)
	}
	return NormalizePolicy(policy, profile), nil
}

func DefaultPolicy(_ string) Policy {
	return Policy{
		SchemaVersion:       PolicySchemaVersion,
		SourceSchemaVersion: PolicySchemaVersion,
		Area:                "dropzone",
		Path:                "Dropzone",
		Enabled:             false,
		Mode:                "custody_transfer",
		RuntimeStatus:       RuntimeRetired,
		Target:              RuntimeRetired,
		StatusDir:           ".loom/state/dropzone",
		TransfersDir:        ".loom/state/dropzone/transfers",
		ChecksumAlgorithm:   ChecksumSHA256,
	}
}

func NormalizePolicy(policy Policy, profile string) Policy {
	defaults := DefaultPolicy(profile)
	sourceSchema := strings.TrimSpace(policy.SchemaVersion)
	if sourceSchema == "" {
		sourceSchema = PolicySchemaVersion
	}
	policy.SourceSchemaVersion = sourceSchema
	if sourceSchema == LegacyPolicySchemaV041 || policy.SchemaVersion == "" {
		policy.SchemaVersion = PolicySchemaVersion
	}
	policy.Area = strings.ToLower(strings.TrimSpace(firstNonEmpty(policy.Area, defaults.Area)))
	policy.Path = filepath.ToSlash(strings.TrimSpace(firstNonEmpty(policy.Path, defaults.Path)))
	policy.Mode = strings.ToLower(strings.TrimSpace(firstNonEmpty(policy.Mode, defaults.Mode)))
	policy.StatusDir = filepath.ToSlash(strings.TrimSpace(firstNonEmpty(policy.StatusDir, defaults.StatusDir)))
	policy.TransfersDir = filepath.ToSlash(strings.TrimSpace(firstNonEmpty(policy.TransfersDir, defaults.TransfersDir)))
	policy.ChecksumAlgorithm = strings.ToLower(strings.TrimSpace(firstNonEmpty(policy.ChecksumAlgorithm, defaults.ChecksumAlgorithm)))
	policy.Enabled = false
	policy.RuntimeStatus = RuntimeRetired
	policy.Target = RuntimeRetired
	return policy
}

func ValidatePolicy(policy Policy, _ string) []Diagnostic {
	diagnostics := []Diagnostic{}
	if policy.SchemaVersion != PolicySchemaVersion {
		diagnostics = append(diagnostics, Diagnostic{Severity: "error", Code: "dropzone.policy.schema_version_invalid", Message: fmt.Sprintf("historical Dropzone policy schema_version must be %s", PolicySchemaVersion)})
	}
	if policy.Area != "dropzone" {
		diagnostics = append(diagnostics, Diagnostic{Severity: "error", Code: "dropzone.policy.area_invalid", Message: "historical Dropzone policy area must be dropzone"})
	}
	for label, value := range map[string]string{"path": policy.Path, "status_dir": policy.StatusDir, "transfers_dir": policy.TransfersDir} {
		if err := validateRelativePath(value); err != nil {
			diagnostics = append(diagnostics, Diagnostic{Severity: "error", Code: "dropzone.policy." + label + "_invalid", Message: fmt.Sprintf("%s is invalid: %v", label, err)})
		}
	}
	diagnostics = append(diagnostics, Diagnostic{
		Severity:   "info",
		Code:       "dropzone.runtime_retired",
		Message:    "Dropzone runtime is retired; this policy is retained for historical inspection only.",
		Suggestion: "use workspace Lane for supported intake",
	})
	return diagnostics
}

func firstNonEmpty(value string, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func validateRelativePath(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("path is required")
	}
	if strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("path contains NUL")
	}
	value = strings.ReplaceAll(value, "\\", "/")
	if filepath.IsAbs(value) || strings.HasPrefix(value, "/") {
		return fmt.Errorf("path must be relative")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return fmt.Errorf("path must not contain ..")
		}
	}
	clean := filepath.ToSlash(filepath.Clean(value))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("path must resolve inside the box")
	}
	return nil
}
