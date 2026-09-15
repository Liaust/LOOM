package projectcontracts

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

func LoadCredentialPolicy(root string, contract ProjectContract) (CredentialPolicy, string, error) {
	policyPath := strings.TrimSpace(contract.Policies.Credentials)
	if policyPath == "" && !contract.Facets["secrets"] {
		return CredentialPolicy{}, "", fmt.Errorf("project contract does not declare policies.credentials")
	}
	loaded, err := LoadProject(root)
	if err != nil {
		return CredentialPolicy{}, "", err
	}
	resolution, err := ResolveSingletonContract(loaded, ProjectContractCredentials, policyPath)
	if err != nil {
		return CredentialPolicy{}, "", err
	}
	resolved := resolution.Path
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return CredentialPolicy{}, resolved, fmt.Errorf("read credential policy %s: %w", resolution.RelativePath, err)
	}
	policy, err := ParseCredentialPolicy(raw)
	if err != nil {
		return CredentialPolicy{}, resolved, err
	}
	diagnostics := ValidateCredentialPolicy(policy, resolved)
	if len(diagnostics) > 0 {
		return CredentialPolicy{}, resolved, fmt.Errorf("credential policy %s is invalid: %s", resolution.RelativePath, diagnostics[0].Message)
	}
	return NormalizeCredentialPolicy(policy), resolved, nil
}

func ParseCredentialPolicy(payload []byte) (CredentialPolicy, error) {
	var policy CredentialPolicy
	decoder := yaml.NewDecoder(bytes.NewReader(payload))
	decoder.KnownFields(true)
	if err := decoder.Decode(&policy); err != nil {
		return CredentialPolicy{}, fmt.Errorf("parse credential policy: %w", err)
	}
	return policy, nil
}

func NormalizeCredentialPolicy(policy CredentialPolicy) CredentialPolicy {
	policy.Kind = strings.TrimSpace(policy.Kind)
	policy.SchemaVersion = strings.TrimSpace(policy.SchemaVersion)
	refs := make([]CredentialPolicyReference, 0, len(policy.Credentials.References))
	for _, ref := range policy.Credentials.References {
		item := CredentialPolicyReference{
			Ref:    strings.TrimSpace(ref.Ref),
			Status: strings.ToLower(strings.TrimSpace(ref.Status)),
			Source: CredentialPolicySource{
				Kind: strings.ToLower(strings.TrimSpace(ref.Source.Kind)),
				Env:  strings.TrimSpace(ref.Source.Env),
				Path: strings.TrimSpace(ref.Source.Path),
			},
		}
		if item.Status == "" {
			item.Status = "required"
		}
		refs = append(refs, item)
	}
	policy.Credentials.References = refs
	return policy
}

func ValidateCredentialPolicy(policy CredentialPolicy, path string) []Diagnostic {
	policy = NormalizeCredentialPolicy(policy)
	diagnostics := []Diagnostic{}
	add := func(diag Diagnostic) {
		diagnostics = append(diagnostics, diag)
	}
	if policy.Kind != CredentialPolicyKind {
		add(Diagnostic{Severity: SeverityError, Code: "credentials_policy.kind_invalid", Message: "credential policy kind must be " + CredentialPolicyKind, File: path, Field: "kind"})
	}
	if policy.SchemaVersion != CredentialPolicySchemaV03 {
		add(Diagnostic{Severity: SeverityError, Code: "credentials_policy.schema_version_unsupported", Message: "credential policy schema_version must be " + CredentialPolicySchemaV03, File: path, Field: "schema_version"})
	}
	if policy.Credentials.InlineSecretsAllowed {
		add(Diagnostic{Severity: SeverityError, Code: "credentials_policy.inline_secrets_forbidden", Message: "credential policy must not allow inline secret values", File: path, Field: "credentials.inline_secrets_allowed"})
	}
	seen := map[string]bool{}
	for idx, ref := range policy.Credentials.References {
		field := "credentials.references"
		if len(policy.Credentials.References) > 1 {
			field += "[" + itoa(idx) + "]"
		}
		if ref.Ref == "" {
			add(Diagnostic{Severity: SeverityError, Code: "credentials_policy.ref_required", Message: "credential reference ref is required", File: path, Field: field + ".ref"})
		} else if !validCredentialRef(ref.Ref) {
			add(Diagnostic{Severity: SeverityError, Code: "credentials_policy.ref_invalid", Message: "credential reference ref must be dotted lowercase such as telegram.bot_token", File: path, Field: field + ".ref"})
		} else if seen[ref.Ref] {
			add(Diagnostic{Severity: SeverityError, Code: "credentials_policy.ref_duplicate", Message: "credential reference is declared more than once: " + ref.Ref, File: path, Field: field + ".ref"})
		}
		seen[ref.Ref] = true
		switch ref.Source.Kind {
		case "env":
			if ref.Source.Env == "" {
				add(Diagnostic{Severity: SeverityError, Code: "credentials_policy.source_env_required", Message: "env credential source requires source.env", File: path, Field: field + ".source.env"})
			} else if !credentialExposeEnvPattern.MatchString(ref.Source.Env) {
				add(Diagnostic{Severity: SeverityError, Code: "credentials_policy.source_env_invalid", Message: "env credential source must be an uppercase environment variable name", File: path, Field: field + ".source.env"})
			}
			if ref.Source.Path != "" {
				add(Diagnostic{Severity: SeverityError, Code: "credentials_policy.source_path_unexpected", Message: "env credential source must not also set source.path", File: path, Field: field + ".source.path"})
			}
		case "file":
			if ref.Source.Path == "" {
				add(Diagnostic{Severity: SeverityError, Code: "credentials_policy.source_path_required", Message: "file credential source requires source.path", File: path, Field: field + ".source.path"})
			} else if !filepath.IsAbs(ref.Source.Path) {
				add(Diagnostic{Severity: SeverityError, Code: "credentials_policy.source_path_relative", Message: "file credential source path must be absolute", File: path, Field: field + ".source.path"})
			}
			if ref.Source.Env != "" {
				add(Diagnostic{Severity: SeverityError, Code: "credentials_policy.source_env_unexpected", Message: "file credential source must not also set source.env", File: path, Field: field + ".source.env"})
			}
		default:
			add(Diagnostic{Severity: SeverityError, Code: "credentials_policy.source_kind_invalid", Message: "credential source kind must be env or file", File: path, Field: field + ".source.kind"})
		}
		if ref.Status != "" && ref.Status != "required" && ref.Status != "optional" {
			add(Diagnostic{Severity: SeverityError, Code: "credentials_policy.status_invalid", Message: "credential reference status must be required or optional", File: path, Field: field + ".status"})
		}
	}
	return diagnostics
}

func (policy CredentialPolicy) ReferenceMap() map[string]CredentialPolicyReference {
	policy = NormalizeCredentialPolicy(policy)
	refs := map[string]CredentialPolicyReference{}
	for _, ref := range policy.Credentials.References {
		refs[ref.Ref] = ref
	}
	return refs
}
