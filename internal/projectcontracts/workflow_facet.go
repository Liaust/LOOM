package projectcontracts

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/scripts"
)

const (
	WorkflowActivationStatusDisabled   = "disabled"
	WorkflowActivationStatusBlocked    = "blocked_until_workflow_runtime"
	WorkflowActivationStatusScriptShim = "script_shim_reference"
	WorkflowActivationStatusDraft      = "draft_workflow_executable"
	WorkflowActivationStatusExecutable = "workflow_executable"

	WorkflowImplementationPlaceholder = "placeholder"
	WorkflowImplementationScript      = "script"
	WorkflowImplementationWorkflow    = "workflow"

	WorkflowCapabilityStatusNotExposed  = "not_exposed"
	WorkflowCapabilityStatusPlaceholder = "placeholder"
	WorkflowCapabilityStatusPlanned     = "planned"
)

func validateWorkflowFacet(loaded LoadedProject, contract ProjectContract, scripts []ScriptFacetItem, connectors []ConnectorFacetItem, add func(Diagnostic)) []WorkflowFacetItem {
	if !contract.Facets["workflows"] {
		return nil
	}
	root := filepath.Join(loaded.RootPath, "workflows")
	entries, err := projectFacetEntries(loaded, "workflows")
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		add(Diagnostic{Severity: SeverityError, Code: "workflow.facet_read_failed", Message: "could not read workflows folder: " + err.Error(), File: root, Field: "facets.workflows"})
		return nil
	}

	items := []WorkflowFacetItem{}
	seenWorkflows := map[string]string{}
	seenCapabilities := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || strings.HasPrefix(name, ".") || ignoredWorkflowDir(name) {
			continue
		}
		packageLoaded, packageRoot := projectFacetPackage(loaded, "workflows", name)
		item := validateWorkflowPackage(packageLoaded, contract, name, packageRoot, scripts, connectors, add)
		if item.Key == "" {
			continue
		}
		if item.WorkflowID != "" {
			if previous := seenWorkflows[item.WorkflowID]; previous != "" {
				add(Diagnostic{
					Severity:   SeverityError,
					Code:       "workflow.id_duplicate",
					Message:    "workflow id is declared more than once: " + item.WorkflowID,
					File:       item.ManifestPath,
					Field:      "workflow.id",
					Suggestion: "use unique workflow ids; previous declaration was in " + previous,
				})
			}
			seenWorkflows[item.WorkflowID] = item.ManifestPath
		}
		if item.CapabilityAddress != "" {
			if previous := seenCapabilities[item.CapabilityAddress]; previous != "" {
				add(Diagnostic{
					Severity:   SeverityError,
					Code:       "workflow.capability_duplicate",
					Message:    "workflow capability address is declared more than once: " + item.CapabilityAddress,
					File:       item.ManifestPath,
					Field:      "expose",
					Suggestion: "use unique workflow expose endpoints; previous declaration was in " + previous,
				})
			}
			seenCapabilities[item.CapabilityAddress] = item.ManifestPath
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Key < items[j].Key
	})
	if len(items) == 0 {
		add(Diagnostic{
			Severity:   SeverityWarning,
			Code:       "workflow.none_found",
			Message:    "workflows facet is enabled but no workflow contracts were found",
			File:       root,
			Field:      "facets.workflows",
			Suggestion: "create workflows/<workflow>/loom.workflow.yaml or disable the workflows facet",
		})
	}
	return items
}

func validateWorkflowPackage(loaded LoadedProject, contract ProjectContract, folderName, workflowRoot string, scripts []ScriptFacetItem, connectors []ConnectorFacetItem, add func(Diagnostic)) WorkflowFacetItem {
	manifestPath := filepath.Join(workflowRoot, "loom.workflow.yaml")
	folder := filepath.ToSlash(mustRelPath(loaded.RootPath, workflowRoot))
	item := WorkflowFacetItem{
		Key:                folderName,
		Folder:             folder,
		ManifestPath:       filepath.ToSlash(manifestPath),
		ContractStatus:     ProjectStatusDraft,
		ImplementationKind: WorkflowImplementationPlaceholder,
		ActivationStatus:   WorkflowActivationStatusBlocked,
	}
	if !pathExists(manifestPath) {
		add(Diagnostic{
			Severity:   SeverityError,
			Code:       "workflow.wrapper_missing",
			Message:    "workflow package is missing loom.workflow.yaml",
			File:       manifestPath,
			Field:      "workflows." + folderName,
			Suggestion: "add loom.workflow.yaml or remove the workflow folder",
		})
		return item
	}

	workflow, payload, err := LoadWorkflowContract(manifestPath)
	if err != nil {
		add(Diagnostic{
			Severity: SeverityError,
			Code:     "workflow.manifest_invalid",
			Message:  "workflow contract is invalid: " + err.Error(),
			File:     manifestPath,
			Field:    "workflows." + folderName,
		})
		return item
	}
	item.ManifestHash = hashBytesURI(payload)
	implementationKindMissing := strings.TrimSpace(workflow.Implementation.Kind) == ""
	normalized := NormalizeWorkflowContract(workflow)
	item.Key = firstNonEmptyString(normalized.Workflow.ID, folderName)
	item.SchemaVersion = normalized.SchemaVersion
	item.WorkflowID = normalized.Workflow.ID
	item.Name = normalized.Workflow.Name
	item.Description = normalized.Workflow.Description
	item.Version = normalized.Workflow.Version
	item.ContractStatus = normalized.Workflow.Status
	item.ImplementationKind = normalized.Implementation.Kind
	item.ScriptRef = normalized.Implementation.ScriptRef
	item.Executable = executableWorkflowContract(normalized)
	item.PackageRoot = folder
	item.Entrypoint = normalized.Entrypoint
	item.Runtime = normalized.Runtime
	item.Execution = normalized.Execution
	item.Capability = normalized.Capability
	item.Credentials = normalized.Credentials
	item.CredentialJSON = credentialRequirementsJSON(normalized.Credentials)
	item.Artifacts = normalized.Artifacts
	item.UsageDocuments = normalized.UsageDocuments
	item.ExposeEnabled = normalized.Expose.Enabled
	item.ProviderKey = normalized.Expose.Provider
	item.Endpoint = normalized.Expose.Endpoint
	item.StepCount = len(normalized.Steps)
	item.Inputs = normalized.Inputs
	item.Outputs = normalized.Outputs
	item.Metadata = normalized.Metadata
	item.ActivationStatus = workflowActivationStatus(normalized)
	if item.ProviderKey == "project" {
		item.ProviderKey = normalizeProviderDefault(contract.ProviderDefaults.WorkflowsProvider, contract.Project.Slug, manifestPath, "expose.provider", add)
	}

	validateWorkflowContractBasics(normalized, folderName, manifestPath, implementationKindMissing, add)
	validateWorkflowExpose(&item, contract, manifestPath, add)
	validateWorkflowSteps(normalized.Steps, manifestPath, add)
	validateWorkflowImplementation(&item, normalized, loaded, workflowRoot, manifestPath, scripts, add)
	validateExecutableWorkflowPackage(&item, normalized, workflowRoot, manifestPath, add)
	_ = connectors
	return item
}

func LoadWorkflowContract(path string) (WorkflowContract, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return WorkflowContract{}, nil, err
	}
	contract, err := ParseWorkflowContract(raw)
	if err != nil {
		return WorkflowContract{}, raw, err
	}
	return contract, raw, nil
}

func ParseWorkflowContract(payload []byte) (WorkflowContract, error) {
	var contract WorkflowContract
	decoder := yaml.NewDecoder(bytes.NewReader(payload))
	decoder.KnownFields(true)
	if err := decoder.Decode(&contract); err != nil {
		return WorkflowContract{}, fmt.Errorf("parse workflow yaml: %w", err)
	}
	return contract, nil
}

func NormalizeWorkflowContract(contract WorkflowContract) WorkflowContract {
	contract.Kind = strings.TrimSpace(contract.Kind)
	contract.SchemaVersion = strings.TrimSpace(contract.SchemaVersion)
	contract.Workflow.ID = strings.TrimSpace(contract.Workflow.ID)
	contract.Workflow.Name = strings.TrimSpace(contract.Workflow.Name)
	contract.Workflow.Description = strings.TrimSpace(contract.Workflow.Description)
	contract.Workflow.Status = strings.ToLower(strings.TrimSpace(contract.Workflow.Status))
	if contract.Workflow.Status == "" {
		contract.Workflow.Status = ProjectStatusDraft
	}
	contract.Workflow.Version = strings.TrimSpace(contract.Workflow.Version)
	contract.Implementation.Kind = strings.ToLower(strings.TrimSpace(contract.Implementation.Kind))
	if contract.Implementation.Kind == "" {
		contract.Implementation.Kind = WorkflowImplementationPlaceholder
	}
	if contract.Workflow.Version == "" && executableWorkflowContract(contract) {
		contract.Workflow.Version = "0.1.0"
	}
	contract.Implementation.ScriptRef = strings.TrimSpace(contract.Implementation.ScriptRef)
	contract.Implementation.CapabilityRef = strings.TrimSpace(contract.Implementation.CapabilityRef)
	for i := range contract.Entrypoint.Command {
		contract.Entrypoint.Command[i] = strings.TrimSpace(contract.Entrypoint.Command[i])
	}
	if contract.Runtime == nil {
		contract.Runtime = map[string]any{}
	}
	contract.Expose.Provider = strings.TrimSpace(contract.Expose.Provider)
	if contract.Expose.Provider == "" {
		contract.Expose.Provider = "project"
	}
	contract.Expose.Endpoint = strings.TrimSpace(contract.Expose.Endpoint)
	if contract.Expose.Endpoint == "" {
		contract.Expose.Endpoint = contract.Workflow.ID
	}
	contract.Expose.DisplayName = strings.TrimSpace(contract.Expose.DisplayName)
	contract.Expose.Description = strings.TrimSpace(contract.Expose.Description)
	contract.Capability.ClassNamespace = strings.TrimSpace(contract.Capability.ClassNamespace)
	if contract.Capability.ClassNamespace == "" {
		contract.Capability.ClassNamespace = "project.workflow"
	}
	contract.Capability.ClassName = strings.TrimSpace(contract.Capability.ClassName)
	if contract.Capability.ClassName == "" {
		contract.Capability.ClassName = strings.ReplaceAll(contract.Workflow.ID, "-", "_")
	}
	contract.Capability.Form = strings.TrimSpace(contract.Capability.Form)
	if contract.Capability.Form == "" {
		contract.Capability.Form = capabilities.CapabilityFormJob
	}
	contract.Capability.RiskLevel = strings.TrimSpace(contract.Capability.RiskLevel)
	if contract.Capability.RiskLevel == "" {
		contract.Capability.RiskLevel = capabilities.RiskLevelMedium
	}
	if contract.Capability.ExecutionAuthorizationLevel == 0 {
		contract.Capability.ExecutionAuthorizationLevel = defaultAuthorizationForRisk(contract.Capability.RiskLevel)
	}
	for i := range contract.Capability.SideEffects {
		contract.Capability.SideEffects[i] = strings.TrimSpace(contract.Capability.SideEffects[i])
	}
	contract.Credentials = normalizeCredentialSpec(contract.Credentials)
	if contract.Capability.InputSchema == nil {
		contract.Capability.InputSchema = map[string]any{"type": "object"}
	}
	if contract.Capability.OutputSchema == nil {
		contract.Capability.OutputSchema = map[string]any{"type": "object"}
	}
	if contract.Inputs == nil {
		contract.Inputs = map[string]any{}
	}
	if contract.Outputs == nil {
		contract.Outputs = map[string]any{}
	}
	if contract.Metadata == nil {
		contract.Metadata = map[string]any{}
	}
	for i := range contract.Steps {
		contract.Steps[i].ID = strings.TrimSpace(contract.Steps[i].ID)
		contract.Steps[i].Kind = strings.TrimSpace(contract.Steps[i].Kind)
		contract.Steps[i].Target = strings.TrimSpace(contract.Steps[i].Target)
		contract.Steps[i].ScriptRef = strings.TrimSpace(contract.Steps[i].ScriptRef)
		if contract.Steps[i].Input == nil {
			contract.Steps[i].Input = map[string]any{}
		}
		if contract.Steps[i].Metadata == nil {
			contract.Steps[i].Metadata = map[string]any{}
		}
	}
	return contract
}

func validateWorkflowContractBasics(contract WorkflowContract, folderName, manifestPath string, implementationKindMissing bool, add func(Diagnostic)) {
	if contract.Kind != WorkflowContractKind {
		add(Diagnostic{Severity: SeverityError, Code: "workflow.kind_invalid", Message: "workflow contract kind must be " + WorkflowContractKind, File: manifestPath, Field: "kind", Suggestion: "set kind: loom.workflow"})
	}
	if !validWorkflowSchemaVersion(contract.SchemaVersion) {
		add(Diagnostic{Severity: SeverityError, Code: "workflow.schema_version_invalid", Message: "workflow contract schema_version must be " + WorkflowSchemaV03 + " or " + WorkflowSchemaV031, File: manifestPath, Field: "schema_version", Suggestion: "set schema_version: workflow.contract.v0.3.1 for executable workflows"})
	}
	if contract.Workflow.ID == "" {
		add(Diagnostic{Severity: SeverityError, Code: "workflow.id_required", Message: "workflow.id is required", File: manifestPath, Field: "workflow.id"})
	} else if !providerKeyPattern.MatchString(contract.Workflow.ID) {
		add(Diagnostic{Severity: SeverityError, Code: "workflow.id_invalid", Message: "workflow.id must be a lowercase workflow key", File: manifestPath, Field: "workflow.id"})
	} else if contract.Workflow.ID != folderName {
		add(Diagnostic{Severity: SeverityError, Code: "workflow.id_folder_mismatch", Message: "workflow.id must match its folder name in v0.3", File: manifestPath, Field: "workflow.id", Suggestion: "set workflow.id to " + folderName})
	}
	if contract.Workflow.Name == "" {
		add(Diagnostic{Severity: SeverityError, Code: "workflow.name_required", Message: "workflow.name is required", File: manifestPath, Field: "workflow.name"})
	}
	if !validWorkflowStatus(contract.Workflow.Status) {
		add(Diagnostic{Severity: SeverityError, Code: "workflow.status_invalid", Message: "workflow.status is not supported", File: manifestPath, Field: "workflow.status", Suggestion: "use draft, active, or disabled"})
	} else if contract.Workflow.Status == ProjectStatusActive && contract.Implementation.Kind == WorkflowImplementationPlaceholder {
		add(Diagnostic{Severity: SeverityError, Code: "workflow.status_invalid", Message: "placeholder workflows cannot be active", File: manifestPath, Field: "workflow.status", Suggestion: "use draft or disabled for placeholder workflows"})
	}
	if implementationKindMissing {
		add(Diagnostic{Severity: SeverityError, Code: "workflow.implementation_required", Message: "workflow implementation.kind is required", File: manifestPath, Field: "implementation.kind", Suggestion: "set implementation.kind to placeholder, script, or workflow"})
	} else if !validWorkflowImplementationKind(contract.Implementation.Kind) {
		add(Diagnostic{Severity: SeverityError, Code: "workflow.implementation_unsupported", Message: "workflow implementation kind is not supported", File: manifestPath, Field: "implementation.kind", Suggestion: "use placeholder, script, or workflow"})
	}
	switch contract.Implementation.Kind {
	case WorkflowImplementationPlaceholder:
		add(Diagnostic{Severity: SeverityWarning, Code: "workflow.placeholder", Message: "workflow contract is design-only and will not create a callable workflow endpoint", File: manifestPath, Field: "implementation.kind", Suggestion: "use implementation.kind: workflow with schema_version: workflow.contract.v0.3.1 for an executable workflow package"})
	case WorkflowImplementationWorkflow:
		if contract.SchemaVersion != WorkflowSchemaV031 {
			add(Diagnostic{Severity: SeverityWarning, Code: "workflow.runtime_unsupported", Message: "workflow runtime contracts require " + WorkflowSchemaV031 + "; this v0.3 contract remains reserved and non-executable", File: manifestPath, Field: "schema_version", Suggestion: "set schema_version: workflow.contract.v0.3.1 and add entrypoint/execution fields"})
		}
	}
}

func validateWorkflowExpose(item *WorkflowFacetItem, contract ProjectContract, manifestPath string, add func(Diagnostic)) {
	provider := item.ProviderKey
	if provider == "" {
		provider = normalizeProviderDefault(contract.ProviderDefaults.WorkflowsProvider, contract.Project.Slug, manifestPath, "expose.provider", add)
	}
	if provider != "project" && !providerKeyPattern.MatchString(provider) {
		add(Diagnostic{Severity: SeverityError, Code: "workflow.expose_provider_invalid", Message: "workflow expose.provider must be project or a provider key", File: manifestPath, Field: "expose.provider"})
	}
	if provider == "project" {
		provider = normalizeProviderDefault(contract.ProviderDefaults.WorkflowsProvider, contract.Project.Slug, manifestPath, "expose.provider", add)
	}
	item.ProviderKey = provider
	if item.Endpoint == "" {
		item.Endpoint = item.WorkflowID
	}
	if item.Endpoint != "" && !capabilityClassIdentPattern.MatchString(item.Endpoint) {
		add(Diagnostic{Severity: SeverityError, Code: "workflow.expose_endpoint_invalid", Message: "workflow expose.endpoint must be lowercase dot-separated snake case", File: manifestPath, Field: "expose.endpoint"})
	}
	if provider == "" || item.Endpoint == "" {
		return
	}
	providerAddress := contract.Project.OwnerNode + "@" + provider
	if normalized, err := capabilities.NormalizeProviderAddress(providerAddress); err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "workflow.expose_address_invalid", Message: "workflow provider address is invalid: " + err.Error(), File: manifestPath, Field: "expose.provider"})
	} else {
		item.ProviderAddress = normalized
	}
	if item.ExposeEnabled {
		address := providerAddress + "." + item.Endpoint
		if normalized, err := capabilities.NormalizeAddress(address); err != nil {
			add(Diagnostic{Severity: SeverityError, Code: "workflow.expose_address_invalid", Message: "workflow capability address is invalid: " + err.Error(), File: manifestPath, Field: "expose"})
		} else {
			item.CapabilityAddress = normalized
		}
		if item.Executable {
			item.WorkflowCapabilityStatus = WorkflowCapabilityStatusPlanned
		} else {
			item.WorkflowCapabilityStatus = WorkflowCapabilityStatusPlaceholder
		}
	} else {
		item.WorkflowCapabilityStatus = WorkflowCapabilityStatusNotExposed
	}
}

func validateWorkflowSteps(steps []WorkflowStepSpec, manifestPath string, add func(Diagnostic)) {
	seen := map[string]bool{}
	for idx, step := range steps {
		field := fmt.Sprintf("steps[%d]", idx)
		if step.ID == "" {
			add(Diagnostic{Severity: SeverityError, Code: "workflow.step_id_required", Message: "workflow step id is required", File: manifestPath, Field: field + ".id"})
		} else if !providerKeyPattern.MatchString(step.ID) {
			add(Diagnostic{Severity: SeverityError, Code: "workflow.step_id_invalid", Message: "workflow step id must be a lowercase key", File: manifestPath, Field: field + ".id"})
		}
		if seen[step.ID] {
			add(Diagnostic{Severity: SeverityError, Code: "workflow.step_id_duplicate", Message: "workflow step id is duplicated: " + step.ID, File: manifestPath, Field: field + ".id"})
		}
		seen[step.ID] = true
		if !validWorkflowStepKind(step.Kind) {
			add(Diagnostic{Severity: SeverityError, Code: "workflow.step_kind_invalid", Message: "workflow step kind is not supported", File: manifestPath, Field: field + ".kind"})
		}
		if step.Kind == "capability_call" {
			if step.Target == "" {
				add(Diagnostic{Severity: SeverityError, Code: "workflow.step_target_required", Message: "capability_call steps must set target", File: manifestPath, Field: field + ".target"})
			} else if _, err := capabilities.NormalizeAddress(step.Target); err != nil {
				add(Diagnostic{Severity: SeverityError, Code: "workflow.step_target_invalid", Message: "workflow step target is invalid: " + err.Error(), File: manifestPath, Field: field + ".target"})
			}
		}
	}
}

func validateWorkflowImplementation(item *WorkflowFacetItem, contract WorkflowContract, loaded LoadedProject, workflowRoot, manifestPath string, scripts []ScriptFacetItem, add func(Diagnostic)) {
	if contract.Implementation.Kind != WorkflowImplementationScript {
		return
	}
	if contract.Implementation.ScriptRef == "" {
		add(Diagnostic{Severity: SeverityError, Code: "workflow.script_ref_required", Message: "script-backed workflows must set implementation.script_ref", File: manifestPath, Field: "implementation.script_ref"})
		return
	}
	scriptPath, rel, ok := resolveWorkflowScriptRef(loaded.RootPath, workflowRoot, contract.Implementation.ScriptRef)
	if ok {
		scriptPath, ok = candidateWorkflowScriptPath(loaded, rel, scriptPath)
	}
	if !ok {
		add(Diagnostic{Severity: SeverityError, Code: "workflow.script_ref_unsafe", Message: "implementation.script_ref must resolve inside the project scripts folder", File: manifestPath, Field: "implementation.script_ref"})
		return
	}
	item.ScriptManifestPath = filepath.ToSlash(scriptPath)
	item.ScriptRef = rel
	if !pathExists(scriptPath) {
		add(Diagnostic{Severity: SeverityError, Code: "workflow.script_ref_missing", Message: "referenced workflow script manifest does not exist", File: manifestPath, Field: "implementation.script_ref"})
		return
	}
	script, found := workflowReferencedScript(scriptPath, scripts)
	if !found {
		add(Diagnostic{Severity: SeverityError, Code: "workflow.script_ref_unmatched", Message: "referenced workflow script is not declared by the project scripts facet", File: manifestPath, Field: "implementation.script_ref", Suggestion: "enable the scripts facet and ensure the script package validates"})
		return
	}
	item.ScriptKey = script.Key
	item.ScriptCapabilityAddress = script.CapabilityAddress
	if contract.Implementation.CapabilityRef != "" {
		if normalized, err := capabilities.NormalizeAddress(contract.Implementation.CapabilityRef); err != nil {
			add(Diagnostic{Severity: SeverityError, Code: "workflow.script_capability_ref_invalid", Message: "implementation.capability_ref is invalid: " + err.Error(), File: manifestPath, Field: "implementation.capability_ref"})
		} else {
			item.ScriptCapabilityAddress = normalized
		}
	}
	if item.ScriptCapabilityAddress == "" {
		add(Diagnostic{Severity: SeverityWarning, Code: "workflow.script_ref_not_exposed", Message: "script-backed workflow references a script that is not exposed as a capability", File: manifestPath, Field: "implementation.script_ref", Suggestion: "enable the script exposure before activating workflow shims"})
	}
}

func validateExecutableWorkflowPackage(item *WorkflowFacetItem, contract WorkflowContract, workflowRoot, manifestPath string, add func(Diagnostic)) {
	if !executableWorkflowContract(contract) {
		return
	}
	if contract.Workflow.Version == "" {
		add(Diagnostic{Severity: SeverityError, Code: "workflow.version_required", Message: "executable workflows must set workflow.version", File: manifestPath, Field: "workflow.version"})
	}
	if len(contract.Entrypoint.Command) == 0 {
		add(Diagnostic{Severity: SeverityError, Code: "workflow.entrypoint_required", Message: "executable workflows must set entrypoint.command", File: manifestPath, Field: "entrypoint.command"})
	} else {
		for idx, part := range contract.Entrypoint.Command {
			if strings.TrimSpace(part) == "" {
				add(Diagnostic{Severity: SeverityError, Code: "workflow.entrypoint_command_invalid", Message: "workflow entrypoint.command contains an empty value", File: manifestPath, Field: fmt.Sprintf("entrypoint.command[%d]", idx)})
			}
		}
	}
	if contract.Execution.TimeoutSeconds <= 0 {
		add(Diagnostic{Severity: SeverityError, Code: "workflow.execution_timeout_invalid", Message: "executable workflows must set a positive execution.timeout_seconds", File: manifestPath, Field: "execution.timeout_seconds"})
	}
	validateWorkflowCapability(contract, manifestPath, add)
	manifest := scripts.Manifest{
		Kind:           scripts.ManifestKind,
		ID:             contract.Workflow.ID,
		Name:           contract.Workflow.Name,
		Version:        contract.Workflow.Version,
		Description:    contract.Workflow.Description,
		Entrypoint:     contract.Entrypoint,
		Runtime:        contract.Runtime,
		Inputs:         contract.Inputs,
		Outputs:        contract.Outputs,
		Execution:      contract.Execution,
		Artifacts:      contract.Artifacts,
		UsageDocuments: contract.UsageDocuments,
		Metadata:       contract.Metadata,
	}
	if err := scripts.ValidateManifest(manifest); err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "workflow.package_invalid", Message: "workflow executable package is invalid: " + err.Error(), File: manifestPath, Field: "implementation.kind"})
		return
	}
	hash, err := scripts.HashPackage(workflowRoot)
	if err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "workflow.package_hash_failed", Message: "could not hash workflow package: " + err.Error(), File: workflowRoot, Field: "workflows." + item.Key})
		return
	}
	item.PackageHash = hash
}

func validateWorkflowCapability(contract WorkflowContract, manifestPath string, add func(Diagnostic)) {
	if !capabilityClassIdentPattern.MatchString(contract.Capability.ClassNamespace) {
		add(Diagnostic{Severity: SeverityError, Code: "workflow.capability_class_namespace_invalid", Message: "workflow capability class namespace must be lowercase dot-separated snake case", File: manifestPath, Field: "capability.class_namespace"})
	}
	if !capabilityClassIdentPattern.MatchString(contract.Capability.ClassName) {
		add(Diagnostic{Severity: SeverityError, Code: "workflow.capability_class_name_invalid", Message: "workflow capability class name must be lowercase dot-separated snake case", File: manifestPath, Field: "capability.class_name"})
	}
	if !capabilities.ValidCapabilityForm(contract.Capability.Form) {
		add(Diagnostic{Severity: SeverityError, Code: "workflow.capability_form_invalid", Message: "workflow capability form is not supported", File: manifestPath, Field: "capability.form"})
	}
	if !capabilities.ValidRiskLevel(contract.Capability.RiskLevel) {
		add(Diagnostic{Severity: SeverityError, Code: "workflow.capability_risk_invalid", Message: "workflow capability risk level is not supported", File: manifestPath, Field: "capability.risk_level"})
	}
	if !capabilities.ValidExecutionAuthorizationLevel(contract.Capability.ExecutionAuthorizationLevel) {
		add(Diagnostic{Severity: SeverityError, Code: "workflow.capability_authorization_invalid", Message: "workflow capability execution authorization level must be between 1 and 5", File: manifestPath, Field: "capability.execution_authorization_level"})
	}
	if err := validateSchemaMap(contract.Capability.InputSchema); err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "workflow.capability_input_schema_invalid", Message: "workflow capability input schema is invalid: " + err.Error(), File: manifestPath, Field: "capability.input_schema"})
	}
	if err := validateSchemaMap(contract.Capability.OutputSchema); err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "workflow.capability_output_schema_invalid", Message: "workflow capability output schema is invalid: " + err.Error(), File: manifestPath, Field: "capability.output_schema"})
	}
	validateCredentialSpec(contract.Credentials, manifestPath, "credentials", add)
}

func resolveWorkflowScriptRef(projectRoot, workflowRoot, raw string) (path, rel string, ok bool) {
	value := strings.TrimSpace(raw)
	if value == "" || filepath.IsAbs(value) {
		return "", "", false
	}
	candidate := filepath.Clean(filepath.Join(workflowRoot, filepath.FromSlash(value)))
	rootRel, err := filepath.Rel(projectRoot, candidate)
	if err != nil {
		return "", "", false
	}
	rootRel = filepath.ToSlash(rootRel)
	if rootRel == "." || strings.HasPrefix(rootRel, "../") || rootRel == ".." {
		return "", "", false
	}
	if !strings.HasPrefix(rootRel, "scripts/") || filepath.Base(candidate) != "loom.script.yaml" {
		return "", "", false
	}
	return candidate, rootRel, true
}

func workflowReferencedScript(path string, scripts []ScriptFacetItem) (ScriptFacetItem, bool) {
	want := filepath.Clean(path)
	for _, script := range scripts {
		if filepath.Clean(filepath.FromSlash(script.ManifestPath)) == want {
			return script, true
		}
	}
	return ScriptFacetItem{}, false
}

func workflowActivationStatus(contract WorkflowContract) string {
	if contract.Workflow.Status == "disabled" {
		return WorkflowActivationStatusDisabled
	}
	if executableWorkflowContract(contract) {
		if contract.Workflow.Status == ProjectStatusActive {
			return WorkflowActivationStatusExecutable
		}
		return WorkflowActivationStatusDraft
	}
	if contract.Implementation.Kind == WorkflowImplementationScript {
		return WorkflowActivationStatusScriptShim
	}
	return WorkflowActivationStatusBlocked
}

func executableWorkflowContract(contract WorkflowContract) bool {
	return contract.SchemaVersion == WorkflowSchemaV031 && contract.Implementation.Kind == WorkflowImplementationWorkflow
}

func validWorkflowSchemaVersion(version string) bool {
	switch strings.TrimSpace(version) {
	case WorkflowSchemaV03, WorkflowSchemaV031:
		return true
	default:
		return false
	}
}

func validWorkflowStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case ProjectStatusDraft, ProjectStatusActive, "disabled":
		return true
	default:
		return false
	}
}

func validWorkflowImplementationKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case WorkflowImplementationPlaceholder, WorkflowImplementationScript, WorkflowImplementationWorkflow:
		return true
	default:
		return false
	}
}

func validWorkflowStepKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "capability_call", "script", "manual", "condition", "note":
		return true
	default:
		return false
	}
}

func workflowSummary(items []WorkflowFacetItem) (discovered, executable, scriptBacked, blocked int) {
	for _, item := range items {
		if item.ManifestPath == "" {
			continue
		}
		discovered++
		if item.Executable {
			executable++
		}
		if item.ImplementationKind == WorkflowImplementationScript {
			scriptBacked++
		}
		if item.ActivationStatus == WorkflowActivationStatusBlocked {
			blocked++
		}
	}
	return discovered, executable, scriptBacked, blocked
}

func workflowSummaries(items []WorkflowFacetItem) []string {
	out := []string{}
	for _, item := range items {
		if item.ManifestPath == "" {
			continue
		}
		target := "blocked"
		if item.Executable && item.CapabilityAddress != "" {
			target = "-> " + item.CapabilityAddress
		} else if item.Executable {
			target = "executable"
		}
		if item.ScriptCapabilityAddress != "" {
			target = "-> " + item.ScriptCapabilityAddress
		}
		out = append(out, fmt.Sprintf("%s %s %s steps=%d %s", item.WorkflowID, item.ContractStatus, item.ImplementationKind, item.StepCount, target))
	}
	sort.Strings(out)
	return out
}

func ignoredWorkflowDir(name string) bool {
	switch name {
	case "tmp", "result", ".cache", ".git", "node_modules":
		return true
	default:
		return false
	}
}
