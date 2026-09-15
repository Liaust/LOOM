package filepolicy

import (
	"fmt"
	"path"
	"strings"
)

// BuiltInPolicyVersion changes only when LOOM's built-in policy semantics do.
const BuiltInPolicyVersion = "loom-file-policy-v1"

type Profile string

const (
	ProfileManaged    Profile = "managed"
	ProfileFaithful   Profile = "faithful"
	ProfileSourceOnly Profile = "source_only"
	ProfileExact      Profile = "exact"
)

var validProfiles = map[Profile]bool{
	ProfileManaged:    true,
	ProfileFaithful:   true,
	ProfileSourceOnly: true,
	ProfileExact:      true,
}

func ParseProfile(value string) (Profile, error) {
	profile := Profile(strings.ToLower(strings.TrimSpace(value)))
	if !validProfiles[profile] {
		return "", fmt.Errorf("unsupported file-policy profile %q", value)
	}
	return profile, nil
}

func (profile Profile) AppliesReconstructibleRules() bool {
	return profile == ProfileManaged || profile == ProfileSourceOnly
}

func (profile Profile) DiscoversUserRules() bool {
	return profile != ProfileExact
}

var mandatoryDirectoryRules = []string{
	".loom/state",
	".loom/tmp",
	".loom-partial",
}

var reconstructibleDirectoryRules = []string{
	"node_modules",
	".venv",
	"venv",
	"__pycache__",
	".pytest_cache",
	".mypy_cache",
	".ruff_cache",
}

// EvaluateBuiltIn returns the decision made by the mandatory and built-in
// profile layers. User and contract rules are added by Resolver in later
// layers.
func EvaluateBuiltIn(profile Profile, relativePath string) Decision {
	normalized := normalizePolicyPath(relativePath)
	decision := Decision{
		Path:          normalized,
		Included:      true,
		Profile:       profile,
		RuleCategory:  RuleCategoryNone,
		PolicyVersion: BuiltInPolicyVersion,
	}
	if matched := matchDirectoryRule(normalized, mandatoryDirectoryRules); matched != "" {
		decision.Included = false
		decision.RuleCategory = RuleCategoryMandatorySafety
		decision.Pattern = matched + "/"
		return decision
	}
	if profile.AppliesReconstructibleRules() {
		if matched := matchDirectoryRule(normalized, reconstructibleDirectoryRules); matched != "" {
			decision.Included = false
			decision.RuleCategory = RuleCategoryReconstructible
			decision.Pattern = matched + "/"
		}
	}
	return decision
}

func normalizePolicyPath(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	value = strings.TrimPrefix(path.Clean("/"+value), "/")
	if value == "." {
		return ""
	}
	return value
}

func matchDirectoryRule(relativePath string, rules []string) string {
	parts := strings.Split(relativePath, "/")
	for index := range parts {
		for _, rule := range rules {
			ruleParts := strings.Split(rule, "/")
			if index+len(ruleParts) > len(parts) {
				continue
			}
			matched := true
			for offset := range ruleParts {
				if parts[index+offset] != ruleParts[offset] {
					matched = false
					break
				}
			}
			if matched {
				return rule
			}
		}
	}
	return ""
}

func MandatoryDirectoryRules() []string {
	return append([]string{}, mandatoryDirectoryRules...)
}

func ReconstructibleDirectoryRules() []string {
	return append([]string{}, reconstructibleDirectoryRules...)
}
