package projectapply

import (
	"slices"
	"strings"

	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectquiescence"
	sr "loom.local/loom/internal/serviceregistry"
)

var applicationPreflightMessages = map[string]struct{ state, message string }{
	"application_execution_authority_required":     {"permission_required", "The current actor lacks project write access or application execution level 5 on this node. A runtime grant does not supply that permission."},
	"application_runtime_unavailable":              {"unavailable", "The application helper could not supply current prerequisites. Restore its availability, then plan again."},
	"application_grant_required":                   {"missing", "No application grant exists. Declare artifact_descriptor to use node-policy provisioning, or retain the explicitly provisioned deployment path."},
	"application_node_policy_required":             {"missing", "Self-service application allocation is disabled on this node. Configure the node allocation root and limits once, then plan again."},
	"application_repository_required":              {"missing", "Register the declared repository before publishing its application artifact."},
	"application_project_binding_changed":          {"conflict", "The repository or project location no longer matches the runtime grant. Reconcile that binding before deployment."},
	"application_publication_required":             {"missing", "No matching artifact publication exists. Declare artifact_descriptor for verified publication through project apply, or use the explicit publisher."},
	"application_publication_mismatch":             {"conflict", "Published artifact evidence does not match the declared artifact or current grant. Publish the matching version before applying."},
	"application_service_registration_required":    {"missing", "No verified service registration is attached to the publication. The artifact_descriptor path creates this registration during project apply."},
	"application_loopback_adapter_only":            {"unsupported", "Use loopback or declare public_https with artifact_descriptor and an exact hostname in the node's approved application suffix. Other endpoint_ref/private adapters are not connected to this installer."},
	"application_installation_fenced":              {"conflict", "The retained installation is fenced or retired. Inspect its lifecycle before changing it."},
	"application_data_set_mismatch":                {"conflict", "The application manifest, declaration and grant must describe the same data bindings."},
	"application_data_binding_required":            {"missing", "Declare a logical data binding_ref matching the retained allocation. The node policy derives physical paths; do not substitute a guessed host path."},
	"application_data_protection_adapter_required": {"unsupported", "Project-relative protection policies do not cover allocated application data. For the node's cloud file history, declare backup: cloud_history on the data binding; project status stays pending until a matching archive succeeds. Custom retention or application-consistent database backup needs its own adapter."},
	"application_quota_not_supported":              {"unsupported", "This runtime cannot enforce the requested disk quota. Planned capacity is not an enforced quota."},
	"application_data_unavailable":                 {"unavailable", "The existing data pool is unavailable or its custody conflicts. Resolve that existing allocation before applying."},
	"application_credential_binding_required":      {"missing", "Declare credential_sources with pass://SHARE/ITEM/FIELD, or pass+generate://SHARE/password when node policy and Proton Editor access permit creation. Keep opaque IDs including padding. Never put credential values in project files."},
	"application_credential_unavailable":           {"unavailable", "An existing credential binding is unavailable. Restore the approved credential source before applying."},
}

func managedApplicationPreflight(target pc.DeclarationTarget, key pc.ResourceKey, d pc.ApplicationDeclaration, repo string, m sr.ApplicationManifest, f *sr.ApplicationPrerequisiteSnapshot) []pc.DeclarationPreflightIssue {
	out := []pc.DeclarationPreflightIssue{}
	credentialIssue := false
	sourcesValid := len(d.CredentialSources) == len(d.Credentials)
	for _, ref := range d.Credentials {
		_, _, ok := pc.ApplicationCredentialSourceShare(d.CredentialSources[ref])
		sourcesValid = sourcesValid && ok
	}
	for _, issue := range applicationPreflight(target, key, d, repo, m, f) {
		switch issue.Code {
		case "application_grant_required", "application_publication_required", "application_publication_mismatch", "application_service_registration_required":
			continue
		case "application_data_set_mismatch":
			if len(d.Data) == len(m.Data) {
				if issue.Field == "data" {
					continue
				}
				if _, ok := m.Data[pc.ResourceKey(strings.TrimPrefix(issue.Field, "data."))]; ok {
					continue
				}
			}
		case "application_data_binding_required":
			k := pc.ResourceKey(strings.TrimPrefix(issue.Field, "data."))
			v := d.Data[k]
			if f != nil {
				if _, exists := f.Data[string(k)]; !exists && v.Path == "" && v.BindingRef != "" {
					continue
				}
			}
		case "application_credential_binding_required":
			if sourcesValid {
				continue
			}
			credentialIssue = true
		case "application_credential_unavailable":
			// The helper materializes missing inputs during apply, not preflight.
			// Existing unreadable/corrupt files still fail at that exact owner.
			if sourcesValid {
				continue
			}
		}
		out = append(out, issue)
	}
	if len(d.Credentials) > 0 && !credentialIssue && !sourcesValid {
		out = append(out, applicationPreflightIssue("application_credential_binding_required", key, "credentials"))
	}
	return out
}

func applicationPreflightIssue(code string, resource pc.ResourceKey, field string) pc.DeclarationPreflightIssue {
	definition := applicationPreflightMessages[code]
	return pc.DeclarationPreflightIssue{Resource: resource, Field: field, State: definition.state, Code: code, Message: definition.message}
}

// Only our finite diagnostics may cross the public error boundary. Reconstruct
// messages rather than forwarding strings from a host or source document.
func (f *Failure) PublicPreflight() []pc.DeclarationPreflightIssue {
	if f == nil || f.Cause != "application_preflight_blocked" || len(f.Preflight) == 0 || len(f.Preflight) > 8192 {
		return nil
	}
	out := make([]pc.DeclarationPreflightIssue, 0, len(f.Preflight))
	for _, issue := range f.Preflight {
		_, known := applicationPreflightMessages[issue.Code]
		field := issue.Field
		validField := slices.Contains([]string{"authority", "runtime", "grant", "repository", "publication", "service", "endpoint", "installation", "data", "credentials"}, field) || (strings.HasPrefix(field, "data.") && keyPattern.MatchString(strings.TrimPrefix(field, "data.")))
		if !known || !keyPattern.MatchString(string(issue.Resource)) || !validField {
			return nil
		}
		out = append(out, applicationPreflightIssue(issue.Code, issue.Resource, field))
	}
	return out
}

// Evaluate independent requirements even when the first host prerequisite is
// missing. Nil facts mean unavailable evidence, not an empty installation.
func applicationPreflight(target pc.DeclarationTarget, key pc.ResourceKey, d pc.ApplicationDeclaration, repo string, m sr.ApplicationManifest, f *sr.ApplicationPrerequisiteSnapshot) []pc.DeclarationPreflightIssue {
	out := []pc.DeclarationPreflightIssue{}
	add := func(code, field string) { out = append(out, applicationPreflightIssue(code, key, field)) }
	if repo == "" {
		add("application_repository_required", "repository")
	}
	if !managedApplicationEndpoint(d) {
		add("application_loopback_adapter_only", "endpoint")
	}
	if len(d.Data) != len(m.Data) {
		add("application_data_set_mismatch", "data")
	}
	keys := make([]pc.ResourceKey, 0, len(d.Data))
	for k := range d.Data {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		data := d.Data[k]
		field := "data." + string(k)
		requirement, exists := m.Data[k]
		if !exists {
			add("application_data_set_mismatch", field)
		}
		if data.Protection != "" {
			add("application_data_protection_adapter_required", field)
		}
		if requirement.QuotaBytes != 0 {
			add("application_quota_not_supported", field)
		}
	}
	if f == nil {
		add("application_runtime_unavailable", "runtime")
		return out
	}
	owner := sr.ApplicationOwner{ProjectID: target.ProjectID, NodeID: target.OwnerNodeID, Resource: string(key)}
	if f.SchemaVersion != sr.ApplicationPrerequisiteSchema || f.Owner != owner || (!f.GrantMissing && (f.RepositoryID != repo || f.LocationRevision != target.LocationRevision)) {
		add("application_project_binding_changed", "grant")
		return out
	}
	if f.GrantMissing {
		add("application_grant_required", "grant")
	}
	p := f.Publication
	if !p.Present || p.Artifact == nil {
		add("application_publication_required", "publication")
	} else if !f.GrantMissing && (p.GrantMatches == nil || !*p.GrantMatches || p.Artifact.RepositoryID != repo || p.Artifact.Artifact != m.Artifact || f.Platform != m.Artifact.Platform || p.Artifact.ConfigurationSchema != m.Config.Schema || p.PolicyRevision != f.PolicyRevision || p.LocationRevision != f.LocationRevision) {
		add("application_publication_mismatch", "publication")
	}
	if p.ArchiveTarget == nil || projectquiescence.ValidateTarget(*p.ArchiveTarget) != nil || p.ArchiveTarget.Kind != projectquiescence.TargetKindService || p.ArchiveTarget.Unit != owner.Unit() || p.ArchiveTarget.AllowlistKey != owner.AllowlistKey() || p.ArchiveTarget.Manager != "systemd" {
		add("application_service_registration_required", "service")
	}
	if f.Installation.Present && (f.Installation.Fenced == nil || *f.Installation.Fenced || f.Installation.Retired == nil || *f.Installation.Retired) {
		add("application_installation_fenced", "installation")
	}
	if !f.GrantMissing && len(d.Data) != len(f.Data) {
		add("application_data_set_mismatch", "data")
	}
	for _, k := range keys {
		data := d.Data[k]
		fact, exists := f.Data[string(k)]
		field := "data." + string(k)
		if !exists || data.Path != "" || data.BindingRef == "" || data.BindingRef != fact.BindingRef {
			add("application_data_binding_required", field)
		} else if fact.Availability == "unavailable" || fact.Custody == "conflict" {
			add("application_data_unavailable", field)
		}
	}
	missing, unavailable := len(d.Credentials) != len(f.Credentials), false
	for _, ref := range d.Credentials {
		c, exists := f.Credentials[ref]
		missing = missing || !exists
		unavailable = unavailable || (exists && (c.Availability != "available" || c.Revision == ""))
	}
	if missing {
		add("application_credential_binding_required", "credentials")
	}
	if unavailable {
		add("application_credential_unavailable", "credentials")
	}
	return out
}
