package projectcontracts

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"loom.local/loom/internal/modules"
)

const (
	ModuleActivationStatusDisabled = "disabled"
	ModuleActivationStatusPending  = "pending_later_slice"
)

func validateModuleFacet(loaded LoadedProject, contract ProjectContract, add func(Diagnostic)) []ModuleFacetItem {
	if !contract.Facets["modules"] {
		return nil
	}
	root := filepath.Join(loaded.RootPath, "modules")
	entries, err := projectFacetEntries(loaded, "modules")
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		add(Diagnostic{Severity: SeverityError, Code: "module.facet_read_failed", Message: "could not read modules folder: " + err.Error(), File: root, Field: "facets.modules"})
		return nil
	}

	items := []ModuleFacetItem{}
	seenModules := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || strings.HasPrefix(name, ".") || ignoredModuleDir(name) {
			continue
		}
		packageLoaded, packageRoot := projectFacetPackage(loaded, "modules", name)
		item := validateModulePackage(packageLoaded, name, packageRoot, add)
		if item.Key == "" {
			continue
		}
		if item.ModuleID != "" {
			if previous := seenModules[item.ModuleID]; previous != "" {
				add(Diagnostic{
					Severity:   SeverityError,
					Code:       "module.module_id_duplicate",
					Message:    "module id is declared more than once: " + item.ModuleID,
					File:       item.ManifestPath,
					Field:      "module.id",
					Suggestion: "use unique module ids; previous declaration was in " + previous,
				})
			}
			seenModules[item.ModuleID] = item.ManifestPath
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Key < items[j].Key
	})
	if len(items) == 0 {
		add(Diagnostic{
			Severity:   SeverityWarning,
			Code:       "module.none_found",
			Message:    "modules facet is enabled but no module packages were found",
			File:       root,
			Field:      "facets.modules",
			Suggestion: "create modules/<module>/module.json and modules/<module>/loom.module_project.yaml or disable the modules facet",
		})
	}
	return items
}

func validateModulePackage(loaded LoadedProject, folderName, moduleRoot string, add func(Diagnostic)) ModuleFacetItem {
	manifestPath := filepath.Join(moduleRoot, "module.json")
	wrapperPath := filepath.Join(moduleRoot, "loom.module_project.yaml")
	folder := filepath.ToSlash(mustRelPath(loaded.RootPath, moduleRoot))
	item := ModuleFacetItem{
		Key:                   folderName,
		Folder:                folder,
		ManifestPath:          filepath.ToSlash(manifestPath),
		ProjectContractPath:   filepath.ToSlash(wrapperPath),
		ContractStatus:        ProjectStatusDraft,
		RegistrationEnabled:   false,
		OwnedByProject:        true,
		ExposeInProjectPortal: true,
		InstallPlan:           defaultModuleInstallPlan(),
		ExposurePlan:          defaultModuleExposurePlan(),
		Validation:            defaultModuleValidation(),
		ActivationStatus:      ModuleActivationStatusDisabled,
	}

	wrapper, payload, err := LoadModuleProjectContract(wrapperPath)
	if err != nil {
		if os.IsNotExist(err) {
			add(Diagnostic{
				Severity:   SeverityError,
				Code:       "module.project_contract_missing",
				Message:    "module package is missing loom.module_project.yaml",
				File:       wrapperPath,
				Field:      "modules." + folderName,
				Suggestion: "add loom.module_project.yaml to declare project registration intent",
			})
		} else {
			add(Diagnostic{Severity: SeverityError, Code: "module.project_contract_invalid", Message: "module project contract is invalid: " + err.Error(), File: wrapperPath, Field: "modules." + folderName})
		}
	} else {
		item.ProjectContractHash = hashBytesURI(payload)
		normalized := NormalizeModuleProjectContract(wrapper)
		item.ContractStatus = normalized.Module.Status
		item.RegistrationEnabled = moduleRegistrationEnabled(normalized)
		item.OwnedByProject = boolPtrDefault(normalized.Project.OwnedByProject, true)
		item.ExposeInProjectPortal = boolPtrDefault(normalized.Project.ExposeInProjectPortal, true)
		item.InstallPlan = normalized.Install
		item.ExposurePlan = normalized.Exposure
		item.Validation = normalized.Validation
		item.Metadata = normalized.Metadata
		item.ActivationStatus = ModuleActivationStatusPending
		if !item.RegistrationEnabled {
			item.ActivationStatus = ModuleActivationStatusDisabled
		}
		validateModuleProjectContractBasics(normalized, wrapperPath, add)
		manifestRel := firstNonEmptyString(normalized.Module.Manifest, normalized.Module.Package)
		if manifestRel != "" && manifestRel != "module.json" {
			add(Diagnostic{
				Severity:   SeverityError,
				Code:       "module.manifest_path_unsupported",
				Message:    "module.manifest must be module.json because the module substrate registers module packages by package root",
				File:       wrapperPath,
				Field:      "module.manifest",
				Suggestion: "set module.manifest: module.json",
			})
		}
	}

	loadedPackage, err := modules.LoadPackage(moduleRoot)
	if err != nil {
		add(Diagnostic{
			Severity: SeverityError,
			Code:     "module.package_invalid",
			Message:  "module package is invalid: " + err.Error(),
			File:     manifestPath,
			Field:    "modules." + folderName,
		})
		return item
	}
	item.ManifestPath = filepath.ToSlash(loadedPackage.ManifestPath)
	item.ManifestHash = loadedPackage.ManifestHash
	item.PackageHash = loadedPackage.ContentHash
	item.PackageSizeBytes = loadedPackage.PackageSizeBytes
	item.Manifest = loadedPackage.Manifest
	item.ModuleID = loadedPackage.Manifest.Module.ID
	item.ModuleName = loadedPackage.Manifest.Module.Name
	item.Version = loadedPackage.Manifest.Module.Version
	item.ModuleKind = loadedPackage.Manifest.Module.Kind
	item.Description = loadedPackage.Manifest.Module.Description
	item.SourceRef = loadedPackage.Manifest.Module.SourceRef
	item.RequirementCount = len(loadedPackage.Manifest.Requires.RuntimeFeatures) + len(loadedPackage.Manifest.Requires.Modules) + len(loadedPackage.Manifest.Requires.Connectors) + len(loadedPackage.Manifest.Requires.Credentials)
	item.ObjectTypeCount = len(loadedPackage.Manifest.Provides.ObjectTypes)
	item.ProviderCount = len(loadedPackage.Manifest.Provides.Providers)
	item.CapabilityCount = len(loadedPackage.Manifest.Provides.Capabilities)
	item.UsageDocumentCount = len(loadedPackage.Manifest.Provides.UsageDocuments)
	item.BackupHookCount = len(loadedPackage.Manifest.Provides.BackupHooks)
	validateModuleProjectPolicy(item, add)
	return item
}

func LoadModuleProjectContract(path string) (ModuleProjectContract, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ModuleProjectContract{}, nil, err
	}
	contract, err := ParseModuleProjectContract(raw)
	if err != nil {
		return ModuleProjectContract{}, raw, err
	}
	return contract, raw, nil
}

func ParseModuleProjectContract(payload []byte) (ModuleProjectContract, error) {
	var contract ModuleProjectContract
	decoder := yaml.NewDecoder(bytes.NewReader(payload))
	decoder.KnownFields(true)
	if err := decoder.Decode(&contract); err != nil {
		return ModuleProjectContract{}, fmt.Errorf("parse module project yaml: %w", err)
	}
	return contract, nil
}

func NormalizeModuleProjectContract(contract ModuleProjectContract) ModuleProjectContract {
	contract.Kind = strings.TrimSpace(contract.Kind)
	contract.SchemaVersion = strings.TrimSpace(contract.SchemaVersion)
	contract.Module.Manifest = strings.TrimSpace(contract.Module.Manifest)
	contract.Module.Package = strings.TrimSpace(contract.Module.Package)
	if contract.Module.Manifest == "" && contract.Module.Package != "" {
		contract.Module.Manifest = contract.Module.Package
	}
	if contract.Module.Manifest == "" {
		contract.Module.Manifest = "module.json"
	}
	contract.Module.Status = strings.ToLower(strings.TrimSpace(contract.Module.Status))
	if contract.Module.Status == "" {
		contract.Module.Status = ProjectStatusDraft
	}
	if contract.Registration.Register == nil && contract.Registration.AutoRegister != nil {
		contract.Registration.Register = contract.Registration.AutoRegister
	}
	contract.Install.Plan = strings.TrimSpace(contract.Install.Plan)
	if contract.Install.Plan == "" {
		contract.Install.Plan = "explicit_only"
	}
	contract.Install.TargetNode = strings.TrimSpace(contract.Install.TargetNode)
	if contract.Install.TargetNode == "" {
		contract.Install.TargetNode = "main"
	}
	contract.Install.Scope = strings.TrimSpace(contract.Install.Scope)
	if contract.Install.Scope == "" {
		contract.Install.Scope = "system"
	}
	contract.Exposure.Plan = strings.TrimSpace(contract.Exposure.Plan)
	if contract.Exposure.Plan == "" {
		contract.Exposure.Plan = "explicit_only"
	}
	contract.Exposure.Capabilities = normalizeModuleStringList(contract.Exposure.Capabilities)
	if contract.Metadata == nil {
		contract.Metadata = map[string]any{}
	}
	return contract
}

func validateModuleProjectContractBasics(contract ModuleProjectContract, wrapperPath string, add func(Diagnostic)) {
	if contract.Kind != ModuleProjectKind {
		add(Diagnostic{Severity: SeverityError, Code: "module.project_kind_invalid", Message: "module project contract kind must be " + ModuleProjectKind, File: wrapperPath, Field: "kind", Suggestion: "set kind: loom.module_project"})
	}
	switch contract.SchemaVersion {
	case ModuleProjectSchemaV03:
	case "module.project.contract.v0.3":
		add(Diagnostic{
			Severity:   SeverityWarning,
			Code:       "module.project_schema_legacy",
			Message:    "module project contract uses the legacy schema version spelling",
			File:       wrapperPath,
			Field:      "schema_version",
			Suggestion: "set schema_version: " + ModuleProjectSchemaV03,
		})
	default:
		add(Diagnostic{Severity: SeverityError, Code: "module.project_schema_version_invalid", Message: "module project contract schema_version must be " + ModuleProjectSchemaV03, File: wrapperPath, Field: "schema_version", Suggestion: "set schema_version: module_project.contract.v0.3"})
	}
	if !validModuleProjectStatus(contract.Module.Status) {
		add(Diagnostic{Severity: SeverityError, Code: "module.status_invalid", Message: "module.status is not supported", File: wrapperPath, Field: "module.status", Suggestion: "use draft, active, paused, archived, or disabled"})
	}
	if _, err := normalizeRelativePath(contract.Module.Manifest); err != nil || contract.Module.Manifest == "" {
		add(Diagnostic{Severity: SeverityError, Code: "module.manifest_path_invalid", Message: "module.manifest must be a safe project-relative path", File: wrapperPath, Field: "module.manifest", Suggestion: "set module.manifest: module.json"})
	}
	if contract.Install.Plan != "explicit_only" {
		add(Diagnostic{Severity: SeverityError, Code: "module.install_plan_unsupported", Message: "module install plan must be explicit_only in v0.3", File: wrapperPath, Field: "install.plan"})
	}
	if contract.Install.InstallAfterRegister {
		add(Diagnostic{Severity: SeverityError, Code: "module.install_after_register_unsupported", Message: "module install_after_register must stay false in v0.3", File: wrapperPath, Field: "install.install_after_register"})
	}
	if contract.Install.EnableAfterInstall {
		add(Diagnostic{Severity: SeverityError, Code: "module.enable_after_install_unsupported", Message: "module enable_after_install must stay false in v0.3", File: wrapperPath, Field: "install.enable_after_install"})
	}
	if boolPtrDefault(contract.Registration.AutoInstall, false) {
		add(Diagnostic{Severity: SeverityError, Code: "module.auto_install_unsupported", Message: "legacy registration.auto_install must stay false in v0.3", File: wrapperPath, Field: "registration.auto_install"})
	}
	if boolPtrDefault(contract.Registration.AutoExpose, false) {
		add(Diagnostic{Severity: SeverityError, Code: "module.auto_expose_unsupported", Message: "legacy registration.auto_expose must stay false in v0.3", File: wrapperPath, Field: "registration.auto_expose"})
	}
	if contract.Exposure.Plan != "explicit_only" {
		add(Diagnostic{Severity: SeverityError, Code: "module.exposure_plan_unsupported", Message: "module exposure plan must be explicit_only in v0.3", File: wrapperPath, Field: "exposure.plan"})
	}
	if contract.Exposure.ExposeAfterEnable {
		add(Diagnostic{Severity: SeverityError, Code: "module.expose_after_enable_unsupported", Message: "module expose_after_enable must stay false in v0.3", File: wrapperPath, Field: "exposure.expose_after_enable"})
	}
}

func validateModuleProjectPolicy(item ModuleFacetItem, add func(Diagnostic)) {
	if item.Validation.RequireUsageDocsForCapabilities && item.CapabilityCount > 0 && item.UsageDocumentCount == 0 {
		add(Diagnostic{
			Severity:   SeverityError,
			Code:       "module.usage_docs_required",
			Message:    "module validation requires usage documents for declared capabilities",
			File:       item.ManifestPath,
			Field:      "provides.usage_documents",
			Suggestion: "add module usage documents or set validation.require_usage_docs: false",
		})
	}
	stateful := item.Manifest.Storage.Database.Required || item.Manifest.Storage.Filesystem.Required
	if item.Validation.RequireBackupHooksForStatefulStorage && stateful && item.BackupHookCount == 0 {
		add(Diagnostic{
			Severity:   SeverityError,
			Code:       "module.backup_hooks_required",
			Message:    "stateful module packages require backup hooks by project policy",
			File:       item.ManifestPath,
			Field:      "provides.backup_hooks",
			Suggestion: "declare backup hooks or set validation.require_backup_hooks_for_stateful_storage: false",
		})
	}
}

func moduleSummary(items []ModuleFacetItem) (discovered, registrable int) {
	for _, item := range items {
		if item.ManifestPath == "" {
			continue
		}
		discovered++
		if item.RegistrationEnabled && item.ModuleID != "" && item.ManifestHash != "" {
			registrable++
		}
	}
	return discovered, registrable
}

func modulePackageSummaries(items []ModuleFacetItem) []string {
	out := []string{}
	for _, item := range items {
		if item.ManifestPath == "" {
			continue
		}
		out = append(out, fmt.Sprintf("%s %s %s providers=%d capabilities=%d", item.ModuleID, item.Version, item.ActivationStatus, item.ProviderCount, item.CapabilityCount))
	}
	sort.Strings(out)
	return out
}

func moduleRegistrationEnabled(contract ModuleProjectContract) bool {
	return contract.Module.Status != "disabled" && boolPtrDefault(contract.Registration.Register, true)
}

func defaultModuleInstallPlan() ModuleProjectInstallPlan {
	return ModuleProjectInstallPlan{Plan: "explicit_only", TargetNode: "main", Scope: "system"}
}

func defaultModuleExposurePlan() ModuleProjectExposurePlan {
	return ModuleProjectExposurePlan{Plan: "explicit_only", Capabilities: []string{}}
}

func defaultModuleValidation() ModuleProjectValidation {
	return ModuleProjectValidation{RequireBackupHooksForStatefulStorage: true}
}

func validModuleProjectStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case ProjectStatusDraft, ProjectStatusActive, ProjectStatusPaused, ProjectStatusArchived, "disabled":
		return true
	default:
		return false
	}
}

func boolPtrDefault(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func normalizeModuleStringList(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func ignoredModuleDir(name string) bool {
	switch name {
	case "node_modules", "tmp", "result", ".cache", ".git":
		return true
	default:
		return false
	}
}
