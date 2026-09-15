package projectcontracts

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/ids"
)

var (
	projectSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`)
	nodeKeyPattern     = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
	providerKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
)

var supportedProjectStatuses = map[string]bool{
	ProjectStatusDraft:    true,
	ProjectStatusActive:   true,
	ProjectStatusPaused:   true,
	ProjectStatusArchived: true,
}

func ValidProjectSlug(slug string) bool {
	return projectSlugPattern.MatchString(strings.TrimSpace(slug))
}

var supportedFacets = map[string]bool{
	"notes":         true,
	"repos":         true,
	"scripts":       true,
	"workflows":     true,
	"connectors":    true,
	"schedules":     true,
	"direct_events": true,
	"modules":       true,
	"services":      true,
	"datasets":      true,
	"docs":          true,
	"tests":         true,
	"secrets":       true,
	"sync_policy":   true,
	"backup_policy": true,
	"worker_policy": true,
	"portal":        true,
}

var facetOrder = []string{
	"notes",
	"repos",
	"scripts",
	"workflows",
	"connectors",
	"schedules",
	"direct_events",
	"modules",
	"services",
	"datasets",
	"docs",
	"tests",
	"secrets",
	"sync_policy",
	"backup_policy",
	"worker_policy",
	"portal",
}

var facetFolders = map[string]string{
	"notes":         "notes",
	"repos":         "repos",
	"scripts":       "scripts",
	"workflows":     "workflows",
	"connectors":    "connectors",
	"schedules":     "schedules",
	"direct_events": "direct_events",
	"modules":       "modules",
	"services":      ".loom/contracts/services",
	"datasets":      "datasets",
	"docs":          "docs",
	"tests":         "tests",
	"secrets":       "secrets",
}

func Validate(loaded LoadedProject) ValidationReport {
	if loaded.Contract.SchemaVersion == ProjectSchemaV05 {
		return validateDeclaration(loaded)
	}
	report := emptyReport(loaded.RootPath, loaded.ContractPath, nowUTC())
	contract := NormalizeContract(loaded.Contract)
	report.Project = planProject(contract)

	add := func(diag Diagnostic) {
		report.Diagnostics = append(report.Diagnostics, diag)
	}
	if loaded.Layout == ProjectLayoutLegacy {
		add(Diagnostic{
			Severity:   SeverityWarning,
			Code:       "contract.layout_legacy",
			Message:    "project uses the legacy root contract layout at " + LegacyRootContractPath,
			File:       loaded.ContractPath,
			Suggestion: "run loom project migrate-layout " + loaded.RootPath + " --dry-run",
		})
	}
	if loaded.Layout == ProjectLayoutCanonicalWithLegacy {
		add(Diagnostic{
			Severity:   SeverityWarning,
			Code:       "contract.layout_duplicate",
			Message:    "legacy root contract is redundant because it is semantically equivalent to the canonical contract",
			File:       loaded.Discovery.LegacyPath,
			Suggestion: "run loom project migrate-layout " + loaded.RootPath + " --dry-run before removing the redundant legacy copy",
		})
	}

	if contract.Kind != ProjectKind {
		add(Diagnostic{
			Severity:   SeverityError,
			Code:       "contract.kind_invalid",
			Message:    "project contract kind must be " + ProjectKind,
			File:       loaded.ContractPath,
			Field:      "kind",
			Suggestion: "set kind: loom.project",
		})
	}
	if contract.SchemaVersion != ProjectSchemaV03 && contract.SchemaVersion != ProjectSchemaV04 {
		add(Diagnostic{
			Severity:   SeverityError,
			Code:       "contract.schema_version_invalid",
			Message:    "project contract schema_version must be " + ProjectSchemaV03 + " or " + ProjectSchemaV04,
			File:       loaded.ContractPath,
			Field:      "schema_version",
			Suggestion: "set schema_version to a supported project contract version",
		})
	}
	if contract.SchemaVersion == ProjectSchemaV03 && contract.Project.ID != "" {
		add(Diagnostic{Severity: SeverityError, Code: "project.id_not_supported", Message: "project.id requires " + ProjectSchemaV04, File: loaded.ContractPath, Field: "project.id", Suggestion: "migrate the contract to project.contract.v0.4 before binding identity"})
	}
	if contract.SchemaVersion == ProjectSchemaV04 {
		if contract.Project.ID == "" {
			add(Diagnostic{Severity: SeverityError, Code: "project.id_required", Message: "project.id is required by project.contract.v0.4", File: loaded.ContractPath, Field: "project.id"})
		} else if err := ids.Validate(ids.ProjectPrefix, contract.Project.ID); err != nil {
			add(Diagnostic{Severity: SeverityError, Code: "project.id_invalid", Message: "project.id must be a valid typed LOOM project ID: " + err.Error(), File: loaded.ContractPath, Field: "project.id"})
		}
	}
	if contract.Project.Slug == "" {
		add(Diagnostic{Severity: SeverityError, Code: "project.slug_required", Message: "project.slug is required", File: loaded.ContractPath, Field: "project.slug"})
	} else if !projectSlugPattern.MatchString(contract.Project.Slug) {
		add(Diagnostic{
			Severity:   SeverityError,
			Code:       "project.slug_invalid",
			Message:    "project slug must be lowercase URL-safe and 3-64 characters",
			File:       loaded.ContractPath,
			Field:      "project.slug",
			Suggestion: "use lowercase letters, numbers, and hyphens, for example gmail-automation",
		})
	}
	if contract.Project.Name == "" {
		add(Diagnostic{Severity: SeverityError, Code: "project.name_required", Message: "project.name is required", File: loaded.ContractPath, Field: "project.name"})
	}
	if contract.Project.OwnerNode == "" {
		add(Diagnostic{Severity: SeverityError, Code: "project.owner_node_required", Message: "project.owner_node is required", File: loaded.ContractPath, Field: "project.owner_node"})
	} else if !nodeKeyPattern.MatchString(contract.Project.OwnerNode) {
		add(Diagnostic{
			Severity:   SeverityError,
			Code:       "project.owner_node_invalid",
			Message:    "project.owner_node must be a lowercase node key",
			File:       loaded.ContractPath,
			Field:      "project.owner_node",
			Suggestion: "use the node key that should own this project, for example macbook",
		})
	}
	if !supportedProjectStatuses[contract.Project.Status] {
		add(Diagnostic{
			Severity:   SeverityError,
			Code:       "project.status_invalid",
			Message:    "project.status is not supported",
			File:       loaded.ContractPath,
			Field:      "project.status",
			Suggestion: "use draft, active, paused, or archived",
		})
	}

	report.DerivedProviders = derivedProviders(contract, loaded.ContractPath, add)
	report.Facets = validateFacets(loaded, contract, add)
	report.PolicyRefs = validatePolicyRefs(loaded, contract, add)
	report.Notes = validateNotesFacet(loaded, contract, add)
	report.Repos, report.RepositoryMembers, report.RepositorySource = validateReposFacet(loaded, contract, add)
	report.Scripts = validateScriptFacet(loaded, contract, add)
	report.Connectors = validateConnectorFacet(loaded, contract, add)
	report.Workflows = validateWorkflowFacet(loaded, contract, report.Scripts, report.Connectors, add)
	report.Modules = validateModuleFacet(loaded, contract, add)
	report.Schedules = validateScheduleFacet(loaded, contract, add)
	report.DirectEvents = validateDirectEventFacet(loaded, contract, add)
	report.Services = validateServiceFacet(loaded, contract, add)
	report.WatchedRoots = validateProjectWatchPolicies(loaded, contract, report.Notes, report.Repos, add)
	validateCrossFacetDependencies(&report, add)

	report.Diagnostics = sortDiagnostics(report.Diagnostics)
	report.Summary = summarizeDiagnostics(report.Diagnostics)
	report.OK = report.Summary.Errors == 0
	report.Registerable = report.OK
	return report
}

func NormalizeContract(contract ProjectContract) ProjectContract {
	contract.Kind = strings.TrimSpace(contract.Kind)
	contract.SchemaVersion = strings.TrimSpace(contract.SchemaVersion)
	contract.Project.ID = strings.TrimSpace(contract.Project.ID)
	contract.Project.Slug = strings.TrimSpace(contract.Project.Slug)
	contract.Project.Name = strings.TrimSpace(contract.Project.Name)
	contract.Project.Description = strings.TrimSpace(contract.Project.Description)
	contract.Project.OwnerNode = strings.TrimSpace(contract.Project.OwnerNode)
	contract.Project.Status = strings.ToLower(strings.TrimSpace(contract.Project.Status))
	if contract.Project.Status == "" {
		contract.Project.Status = ProjectStatusDraft
	}
	contract.ProviderDefaults.ScriptsProvider = strings.TrimSpace(contract.ProviderDefaults.ScriptsProvider)
	contract.ProviderDefaults.WorkflowsProvider = strings.TrimSpace(contract.ProviderDefaults.WorkflowsProvider)
	contract.Policies.Sync = strings.TrimSpace(contract.Policies.Sync)
	contract.Policies.Backup = strings.TrimSpace(contract.Policies.Backup)
	contract.Policies.Workers = strings.TrimSpace(contract.Policies.Workers)
	contract.Policies.Credentials = strings.TrimSpace(contract.Policies.Credentials)
	contract.Portal.DisplayGroup = strings.TrimSpace(contract.Portal.DisplayGroup)
	contract.Portal.Summary = strings.TrimSpace(contract.Portal.Summary)
	return contract
}

func emptyReport(root, contractPath string, generatedAt time.Time) ValidationReport {
	return ValidationReport{
		SchemaVersion: ReportSchemaV03,
		GeneratedAt:   generatedAt,
		ProjectRoot:   root,
		ContractPath:  contractPath,
		OK:            false,
		Registerable:  false,
	}
}

func planProject(contract ProjectContract) PlanProject {
	return PlanProject{
		ID:          contract.Project.ID,
		Slug:        contract.Project.Slug,
		Name:        contract.Project.Name,
		Description: contract.Project.Description,
		OwnerNode:   contract.Project.OwnerNode,
		Status:      contract.Project.Status,
	}
}

func derivedProviders(contract ProjectContract, file string, add func(Diagnostic)) []PlanProvider {
	if contract.Project.OwnerNode == "" || contract.Project.Slug == "" {
		return nil
	}
	if !projectSlugPattern.MatchString(contract.Project.Slug) || !nodeKeyPattern.MatchString(contract.Project.OwnerNode) {
		return nil
	}
	providers := []PlanProvider{}
	scriptsProvider := normalizeProviderDefault(contract.ProviderDefaults.ScriptsProvider, contract.Project.Slug, file, "provider_defaults.scripts_provider", add)
	if scriptsProvider != "" {
		address := contract.Project.OwnerNode + "@" + scriptsProvider
		normalized, err := capabilities.NormalizeProviderAddress(address)
		if err != nil {
			add(Diagnostic{
				Severity: SeverityError,
				Code:     "provider.address_invalid",
				Message:  "derived scripts provider address is invalid: " + err.Error(),
				File:     file,
				Field:    "provider_defaults.scripts_provider",
			})
		} else {
			providers = append(providers, PlanProvider{Kind: "scripts", ProviderKey: scriptsProvider, CompactAddress: normalized, Source: "project"})
		}
	}
	workflowsProvider := normalizeProviderDefault(contract.ProviderDefaults.WorkflowsProvider, contract.Project.Slug, file, "provider_defaults.workflows_provider", add)
	if workflowsProvider != "" && workflowsProvider != scriptsProvider {
		address := contract.Project.OwnerNode + "@" + workflowsProvider
		normalized, err := capabilities.NormalizeProviderAddress(address)
		if err != nil {
			add(Diagnostic{
				Severity: SeverityError,
				Code:     "provider.address_invalid",
				Message:  "derived workflows provider address is invalid: " + err.Error(),
				File:     file,
				Field:    "provider_defaults.workflows_provider",
			})
		} else {
			providers = append(providers, PlanProvider{Kind: "workflows", ProviderKey: workflowsProvider, CompactAddress: normalized, Source: "project"})
		}
	}
	return providers
}

func normalizeProviderDefault(value, projectSlug, file, field string, add func(Diagnostic)) string {
	if value == "" || value == "project" {
		return projectSlug
	}
	if !providerKeyPattern.MatchString(value) {
		add(Diagnostic{
			Severity:   SeverityError,
			Code:       "provider.default_invalid",
			Message:    "provider default must be project or a lowercase provider key",
			File:       file,
			Field:      field,
			Suggestion: "use project to derive the provider from the project slug",
		})
		return ""
	}
	return value
}

func validateFacets(loaded LoadedProject, contract ProjectContract, add func(Diagnostic)) []PlanFacet {
	facets := []PlanFacet{}
	for key := range contract.Facets {
		if !supportedFacets[key] {
			add(Diagnostic{
				Severity:   SeverityError,
				Code:       "facet.unknown",
				Message:    "unknown project facet: " + key,
				File:       loaded.ContractPath,
				Field:      "facets." + key,
				Suggestion: "remove the facet or use one of the supported v0.3 facet keys",
			})
			continue
		}
	}
	for _, key := range facetOrder {
		enabled, explicitlySet := contract.Facets[key]
		if !explicitlySet {
			continue
		}
		folder := facetFolders[key]
		present := false
		if folder != "" {
			present = pathExists(filepath.Join(loaded.RootPath, filepath.FromSlash(folder)))
		}
		facet := PlanFacet{Key: key, Folder: folder, Enabled: enabled, Present: present}
		if folder != "" && enabled && !present && key != "portal" {
			add(Diagnostic{
				Severity:   SeverityWarning,
				Code:       "facet.folder_missing",
				Message:    folder + " is enabled but does not exist",
				File:       loaded.ContractPath,
				Field:      "facets." + key,
				Suggestion: "create " + folder + " or disable the facet",
			})
		}
		if folder != "" && !enabled && present {
			add(Diagnostic{
				Severity:   SeverityWarning,
				Code:       "facet.folder_present_but_disabled",
				Message:    folder + " exists but the facet is disabled",
				File:       loaded.ContractPath,
				Field:      "facets." + key,
				Suggestion: "enable the facet or remove the folder",
			})
		}
		facets = append(facets, facet)
	}
	return facets
}

func validatePolicyRefs(loaded LoadedProject, contract ProjectContract, add func(Diagnostic)) []PlanPolicyRef {
	refs := []PlanPolicyRef{}
	for _, item := range []struct {
		kind  string
		key   string
		path  string
		field string
	}{
		{kind: ProjectContractSync, key: "sync", path: contract.Policies.Sync, field: "policies.sync"},
		{kind: ProjectContractBackup, key: "backup", path: contract.Policies.Backup, field: "policies.backup"},
		{kind: ProjectContractWorkers, key: "workers", path: contract.Policies.Workers, field: "policies.workers"},
		{kind: ProjectContractCredentials, key: "credentials", path: contract.Policies.Credentials, field: "policies.credentials"},
	} {
		if item.path == "" && !contract.Facets[singletonContractFacet(item.kind)] {
			continue
		}
		if item.path != "" {
			if _, err := normalizeRelativePath(item.path); err != nil {
				add(Diagnostic{
					Severity:   SeverityError,
					Code:       "policy.path_unsafe",
					Message:    "policy path is unsafe: " + item.path,
					File:       loaded.ContractPath,
					Field:      item.field,
					Suggestion: "use a safe path relative to the project root",
				})
				continue
			}
		}
		resolution, ok := resolveSingletonForValidation(loaded, item.kind, item.path, item.field, add)
		if !ok {
			continue
		}
		if !resolution.Present {
			add(Diagnostic{
				Severity:   SeverityWarning,
				Code:       "policy.path_missing",
				Message:    "policy file does not exist: " + resolution.RelativePath,
				File:       loaded.ContractPath,
				Field:      item.field,
				Suggestion: "create the policy file before registering this project facet",
			})
		}
		refs = append(refs, PlanPolicyRef{Key: item.key, Path: resolution.RelativePath, Present: resolution.Present})
	}
	return refs
}

func normalizeRelativePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if filepath.IsAbs(value) {
		return "", os.ErrInvalid
	}
	clean := filepath.ToSlash(filepath.Clean(value))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", os.ErrInvalid
	}
	return clean, nil
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func summarizeDiagnostics(diags []Diagnostic) DiagnosticSummary {
	var summary DiagnosticSummary
	for _, diag := range diags {
		switch diag.Severity {
		case SeverityError:
			summary.Errors++
		case SeverityWarning:
			summary.Warnings++
		case SeverityInfo:
			summary.Infos++
		}
	}
	return summary
}

func sortDiagnostics(diags []Diagnostic) []Diagnostic {
	out := append([]Diagnostic{}, diags...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return severityRank(out[i].Severity) < severityRank(out[j].Severity)
		}
		if out[i].Code != out[j].Code {
			return out[i].Code < out[j].Code
		}
		if out[i].Field != out[j].Field {
			return out[i].Field < out[j].Field
		}
		return out[i].Message < out[j].Message
	})
	return out
}

func severityRank(severity string) int {
	switch severity {
	case SeverityError:
		return 0
	case SeverityWarning:
		return 1
	case SeverityInfo:
		return 2
	default:
		return 3
	}
}

func nowUTC() time.Time {
	return time.Now().UTC().Round(0)
}
