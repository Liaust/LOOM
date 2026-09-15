package projectcontracts

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

var credentialExposeEnvPattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

func normalizeCredentialSpec(spec CredentialSpec) CredentialSpec {
	out := CredentialSpec{Required: make([]CredentialRequirement, 0, len(spec.Required))}
	for _, requirement := range spec.Required {
		item := CredentialRequirement{
			Ref:      strings.TrimSpace(requirement.Ref),
			ExposeAs: strings.TrimSpace(requirement.ExposeAs),
			Kind:     strings.ToLower(strings.TrimSpace(requirement.Kind)),
		}
		if item.Kind == "" {
			item.Kind = "env"
		}
		out.Required = append(out.Required, item)
	}
	sort.SliceStable(out.Required, func(i, j int) bool {
		if out.Required[i].Ref == out.Required[j].Ref {
			return out.Required[i].ExposeAs < out.Required[j].ExposeAs
		}
		return out.Required[i].Ref < out.Required[j].Ref
	})
	return out
}

func validateCredentialSpec(spec CredentialSpec, path, fieldPrefix string, add func(Diagnostic)) CredentialSpec {
	spec = normalizeCredentialSpec(spec)
	seenRefs := map[string]bool{}
	seenExpose := map[string]bool{}
	for idx, requirement := range spec.Required {
		field := fieldPrefix + ".required"
		if len(spec.Required) > 1 {
			field = field + "[" + itoa(idx) + "]"
		}
		if requirement.Ref == "" {
			add(Diagnostic{Severity: SeverityError, Code: "credential.ref_required", Message: "credential requirement ref is required", File: path, Field: field + ".ref"})
		} else if !validCredentialRef(requirement.Ref) {
			add(Diagnostic{Severity: SeverityError, Code: "credential.ref_invalid", Message: "credential ref must be a dotted lowercase reference such as telegram.bot_token", File: path, Field: field + ".ref"})
		} else if seenRefs[requirement.Ref] {
			add(Diagnostic{Severity: SeverityError, Code: "credential.ref_duplicate", Message: "credential ref is declared more than once: " + requirement.Ref, File: path, Field: field + ".ref"})
		}
		seenRefs[requirement.Ref] = true

		if requirement.Kind != "env" && requirement.Kind != "file" {
			add(Diagnostic{Severity: SeverityError, Code: "credential.kind_invalid", Message: "credential kind must be env or file", File: path, Field: field + ".kind"})
		}
		if requirement.ExposeAs == "" {
			add(Diagnostic{Severity: SeverityError, Code: "credential.expose_as_required", Message: "credential expose_as is required", File: path, Field: field + ".expose_as"})
		} else if requirement.Kind == "env" && !credentialExposeEnvPattern.MatchString(requirement.ExposeAs) {
			add(Diagnostic{Severity: SeverityError, Code: "credential.expose_as_invalid", Message: "env credential expose_as must be an uppercase environment variable name", File: path, Field: field + ".expose_as"})
		} else if requirement.Kind == "file" && strings.Contains(requirement.ExposeAs, "..") {
			add(Diagnostic{Severity: SeverityError, Code: "credential.expose_as_invalid", Message: "file credential expose_as must not traverse parent directories", File: path, Field: field + ".expose_as"})
		} else if seenExpose[requirement.Kind+"\x00"+requirement.ExposeAs] {
			add(Diagnostic{Severity: SeverityError, Code: "credential.expose_as_duplicate", Message: "credential expose_as is declared more than once: " + requirement.ExposeAs, File: path, Field: field + ".expose_as"})
		}
		seenExpose[requirement.Kind+"\x00"+requirement.ExposeAs] = true
	}
	return spec
}

func validCredentialRef(ref string) bool {
	if strings.ContainsAny(ref, " \t\r\n/\\:$") {
		return false
	}
	parts := strings.Split(ref, ".")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if !providerKeyPattern.MatchString(part) {
			return false
		}
	}
	return true
}

func credentialRequirementsObject(spec CredentialSpec) map[string]any {
	spec = normalizeCredentialSpec(spec)
	if len(spec.Required) == 0 {
		return map[string]any{}
	}
	required := make([]map[string]any, 0, len(spec.Required))
	for _, item := range spec.Required {
		required = append(required, map[string]any{
			"ref":       item.Ref,
			"expose_as": item.ExposeAs,
			"kind":      item.Kind,
		})
	}
	return map[string]any{"required": required}
}

func credentialRequirementsJSON(spec CredentialSpec) json.RawMessage {
	return json.RawMessage(jsonObjectFromMap(credentialRequirementsObject(spec)))
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := [20]byte{}
	i := len(digits)
	for value > 0 {
		i--
		digits[i] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[i:])
}
