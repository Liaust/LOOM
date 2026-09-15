package projectcontracts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/capabilityruntime"
	"loom.local/loom/internal/scripts"
)

const (
	ConnectorActivationStatusDisabled = "disabled"
	ConnectorActivationStatusPending  = "pending_later_slice"
	ConnectorActivationStatusBlocked  = "blocked"
)

func validateConnectorFacet(loaded LoadedProject, contract ProjectContract, add func(Diagnostic)) []ConnectorFacetItem {
	if !contract.Facets["connectors"] {
		return nil
	}
	root := filepath.Join(loaded.RootPath, "connectors")
	entries, err := projectFacetEntries(loaded, "connectors")
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		add(Diagnostic{Severity: SeverityError, Code: "connector.facet_read_failed", Message: "could not read connectors folder: " + err.Error(), File: root, Field: "facets.connectors"})
		return nil
	}

	items := []ConnectorFacetItem{}
	seenProviders := map[string]string{}
	seenCapabilities := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || strings.HasPrefix(name, ".") || ignoredConnectorDir(name) {
			continue
		}
		packageLoaded, packageRoot := projectFacetPackage(loaded, "connectors", name)
		item := validateConnectorPackage(packageLoaded, contract, name, packageRoot, add)
		if item.Key == "" {
			continue
		}
		if previous := seenProviders[item.ProviderKey]; previous != "" {
			add(Diagnostic{
				Severity:   SeverityError,
				Code:       "connector.provider_duplicate",
				Message:    "connector provider key is declared more than once: " + item.ProviderKey,
				File:       item.ManifestPath,
				Field:      "provider.key",
				Suggestion: "use unique connector provider keys; previous declaration was in " + previous,
			})
		}
		if item.ProviderKey != "" {
			seenProviders[item.ProviderKey] = item.ManifestPath
		}
		for _, endpoint := range item.Capabilities {
			if endpoint.CapabilityAddress == "" {
				continue
			}
			if previous := seenCapabilities[endpoint.CapabilityAddress]; previous != "" {
				add(Diagnostic{
					Severity:   SeverityError,
					Code:       "connector.capability_duplicate",
					Message:    "connector capability address is declared more than once: " + endpoint.CapabilityAddress,
					File:       item.ManifestPath,
					Field:      "capabilities",
					Suggestion: "use unique connector endpoint names; previous declaration was in " + previous,
				})
			}
			seenCapabilities[endpoint.CapabilityAddress] = item.ManifestPath
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Key < items[j].Key
	})
	if len(items) == 0 {
		add(Diagnostic{
			Severity:   SeverityWarning,
			Code:       "connector.none_found",
			Message:    "connectors facet is enabled but no connector packages were found",
			File:       root,
			Field:      "facets.connectors",
			Suggestion: "create connectors/<connector>/loom.connector.yaml or disable the connectors facet",
		})
	}
	return items
}

func validateConnectorPackage(loaded LoadedProject, project ProjectContract, folderName, connectorRoot string, add func(Diagnostic)) ConnectorFacetItem {
	manifestPath := filepath.Join(connectorRoot, "loom.connector.yaml")
	folder := filepath.ToSlash(mustRelPath(loaded.RootPath, connectorRoot))
	item := ConnectorFacetItem{
		Key:              folderName,
		Folder:           folder,
		ManifestPath:     filepath.ToSlash(manifestPath),
		ProviderStatus:   ProjectStatusDraft,
		ContractStatus:   ProjectStatusDraft,
		RuntimeKind:      capabilities.RuntimeKindScript,
		RuntimeBaseDir:   "scripts",
		ActivationStatus: ConnectorActivationStatusDisabled,
	}
	if !pathExists(manifestPath) {
		add(Diagnostic{
			Severity:   SeverityError,
			Code:       "connector.manifest_missing",
			Message:    "connector package is missing loom.connector.yaml",
			File:       manifestPath,
			Field:      "connectors." + folderName,
			Suggestion: "add loom.connector.yaml or remove the connector package folder",
		})
		return item
	}

	connector, payload, err := LoadConnectorContract(manifestPath)
	if err != nil {
		add(Diagnostic{
			Severity: SeverityError,
			Code:     "connector.manifest_invalid",
			Message:  "connector manifest is invalid: " + err.Error(),
			File:     manifestPath,
			Field:    "connectors." + folderName,
		})
		return item
	}
	item.ManifestHash = hashBytesURI(payload)
	normalized := NormalizeConnectorContract(connector)
	item.Key = normalized.Provider.Key
	item.ProviderKey = normalized.Provider.Key
	item.DisplayName = normalized.Provider.DisplayName
	item.Description = normalized.Provider.Description
	item.Version = normalized.Provider.Version
	item.ProviderStatus = normalized.Provider.Status
	item.ContractStatus = normalized.Provider.Status
	item.RuntimeKind = normalized.Runtime.Kind
	item.RuntimeBaseDir = normalized.Runtime.BaseDir
	item.Metadata = normalized.Metadata
	if item.ProviderStatus != "disabled" {
		item.ActivationStatus = ConnectorActivationStatusPending
	}
	if !projectConnectorRuntimeSupported(item.RuntimeKind) {
		item.ActivationStatus = ConnectorActivationStatusBlocked
	}

	validateConnectorContractBasics(normalized, manifestPath, add)
	if item.ProviderKey != "" {
		address := project.Project.OwnerNode + "@" + item.ProviderKey
		if normalizedAddress, err := capabilities.NormalizeProviderAddress(address); err != nil {
			add(Diagnostic{Severity: SeverityError, Code: "connector.provider_address_invalid", Message: "connector provider address is invalid: " + err.Error(), File: manifestPath, Field: "provider.key"})
		} else {
			item.ProviderAddress = normalizedAddress
		}
	}
	item.Capabilities = validateConnectorCapabilities(connectorRoot, manifestPath, normalized, item, add)
	item.UsageDocuments = validateConnectorUsageDocuments(connectorRoot, manifestPath, normalized, add)
	return item
}

func LoadConnectorContract(path string) (ConnectorContract, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ConnectorContract{}, nil, err
	}
	contract, err := ParseConnectorContract(raw)
	if err != nil {
		return ConnectorContract{}, raw, err
	}
	return contract, raw, nil
}

func ParseConnectorContract(payload []byte) (ConnectorContract, error) {
	var contract ConnectorContract
	decoder := yaml.NewDecoder(bytes.NewReader(payload))
	decoder.KnownFields(true)
	if err := decoder.Decode(&contract); err != nil {
		return ConnectorContract{}, fmt.Errorf("parse connector yaml: %w", err)
	}
	return contract, nil
}

func NormalizeConnectorContract(contract ConnectorContract) ConnectorContract {
	contract.Kind = strings.TrimSpace(contract.Kind)
	contract.SchemaVersion = strings.TrimSpace(contract.SchemaVersion)
	contract.Provider.Key = strings.ToLower(strings.TrimSpace(contract.Provider.Key))
	contract.Provider.DisplayName = strings.TrimSpace(contract.Provider.DisplayName)
	if contract.Provider.DisplayName == "" {
		contract.Provider.DisplayName = contract.Provider.Key
	}
	contract.Provider.Description = strings.TrimSpace(contract.Provider.Description)
	contract.Provider.Type = strings.TrimSpace(contract.Provider.Type)
	if contract.Provider.Type == "" {
		contract.Provider.Type = capabilities.ProviderTypeConnector
	}
	contract.Provider.Version = strings.TrimSpace(contract.Provider.Version)
	if contract.Provider.Version == "" {
		contract.Provider.Version = "0.1.0"
	}
	contract.Provider.Status = strings.ToLower(strings.TrimSpace(contract.Provider.Status))
	if contract.Provider.Status == "" {
		contract.Provider.Status = ProjectStatusDraft
	}
	contract.Runtime.Kind = strings.TrimSpace(contract.Runtime.Kind)
	if contract.Runtime.Kind == "" {
		contract.Runtime.Kind = capabilities.RuntimeKindScript
	}
	contract.Runtime.BaseDir = strings.TrimSpace(contract.Runtime.BaseDir)
	if contract.Runtime.BaseDir == "" {
		contract.Runtime.BaseDir = "scripts"
	}
	for i := range contract.Capabilities {
		capability := &contract.Capabilities[i]
		capability.Endpoint = strings.ToLower(strings.TrimSpace(capability.Endpoint))
		capability.DisplayName = strings.TrimSpace(capability.DisplayName)
		if capability.DisplayName == "" {
			capability.DisplayName = capability.Endpoint
		}
		capability.Description = strings.TrimSpace(capability.Description)
		capability.ClassNamespace = strings.TrimSpace(capability.ClassNamespace)
		capability.ClassName = strings.TrimSpace(capability.ClassName)
		capability.Form = strings.TrimSpace(capability.Form)
		if capability.Form == "" {
			capability.Form = capabilities.CapabilityFormJob
		}
		capability.RiskLevel = strings.TrimSpace(capability.RiskLevel)
		if capability.RiskLevel == "" {
			capability.RiskLevel = capabilities.RiskLevelLow
		}
		if capability.InputSchema == nil {
			capability.InputSchema = map[string]any{"type": "object"}
		}
		if capability.OutputSchema == nil {
			capability.OutputSchema = map[string]any{"type": "object"}
		}
		if capability.PolicyRequirements == nil {
			capability.PolicyRequirements = map[string]any{}
		}
		if capability.CredentialRequirements == nil {
			capability.CredentialRequirements = map[string]any{}
		}
		capability.Runtime.Kind = strings.TrimSpace(capability.Runtime.Kind)
		capability.Runtime.Script = strings.TrimSpace(capability.Runtime.Script)
		capability.Runtime.DefaultMode = strings.TrimSpace(capability.Runtime.DefaultMode)
		if capability.Runtime.DefaultMode == "" {
			capability.Runtime.DefaultMode = "wait_until_started"
		}
		if capability.Runtime.WaitTimeoutSeconds == 0 {
			capability.Runtime.WaitTimeoutSeconds = 10
		}
		capability.Status = strings.ToLower(strings.TrimSpace(capability.Status))
		if capability.Status == "" {
			capability.Status = contract.Provider.Status
		}
	}
	for i := range contract.UsageDocuments {
		contract.UsageDocuments[i].Path = strings.TrimSpace(contract.UsageDocuments[i].Path)
		contract.UsageDocuments[i].Target = strings.TrimSpace(contract.UsageDocuments[i].Target)
		if contract.UsageDocuments[i].Target == "" {
			contract.UsageDocuments[i].Target = "provider"
		}
		contract.UsageDocuments[i].Endpoint = strings.ToLower(strings.TrimSpace(contract.UsageDocuments[i].Endpoint))
		contract.UsageDocuments[i].Title = strings.TrimSpace(contract.UsageDocuments[i].Title)
	}
	if contract.Metadata == nil {
		contract.Metadata = map[string]any{}
	}
	return contract
}

func validateConnectorContractBasics(contract ConnectorContract, manifestPath string, add func(Diagnostic)) {
	if contract.Kind != ConnectorContractKind {
		add(Diagnostic{Severity: SeverityError, Code: "connector.kind_invalid", Message: "connector contract kind must be " + ConnectorContractKind, File: manifestPath, Field: "kind", Suggestion: "set kind: loom.connector"})
	}
	if contract.SchemaVersion != ConnectorSchemaV03 {
		add(Diagnostic{Severity: SeverityError, Code: "connector.schema_version_invalid", Message: "connector contract schema_version must be " + ConnectorSchemaV03, File: manifestPath, Field: "schema_version", Suggestion: "set schema_version: connector.contract.v0.3"})
	}
	if contract.Provider.Key == "" {
		add(Diagnostic{Severity: SeverityError, Code: "connector.provider_key_required", Message: "provider.key is required", File: manifestPath, Field: "provider.key"})
	} else if contract.Provider.Key == "project" || !providerKeyPattern.MatchString(contract.Provider.Key) {
		add(Diagnostic{Severity: SeverityError, Code: "connector.provider_key_invalid", Message: "provider.key must be a backend-safe connector provider key and cannot be project", File: manifestPath, Field: "provider.key"})
	}
	if contract.Provider.Type != capabilities.ProviderTypeConnector {
		add(Diagnostic{Severity: SeverityError, Code: "connector.provider_type_invalid", Message: "provider.type must be connector", File: manifestPath, Field: "provider.type"})
	}
	if !validConnectorContractStatus(contract.Provider.Status) {
		add(Diagnostic{Severity: SeverityError, Code: "connector.status_invalid", Message: "provider.status is not supported", File: manifestPath, Field: "provider.status", Suggestion: "use draft, active, paused, or disabled"})
	}
	if !capabilities.ValidRuntimeKind(contract.Runtime.Kind) {
		add(Diagnostic{Severity: SeverityError, Code: "connector.runtime_kind_invalid", Message: "runtime.kind is not supported", File: manifestPath, Field: "runtime.kind"})
	} else if !projectConnectorRuntimeSupported(contract.Runtime.Kind) {
		add(Diagnostic{Severity: SeverityWarning, Code: "connector.runtime_not_activatable", Message: "connector runtime " + contract.Runtime.Kind + " is known but activation is deferred until a later slice", File: manifestPath, Field: "runtime.kind"})
	}
	if _, err := normalizeRelativePath(contract.Runtime.BaseDir); err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "connector.runtime_base_dir_unsafe", Message: "runtime.base_dir is unsafe: " + contract.Runtime.BaseDir, File: manifestPath, Field: "runtime.base_dir"})
	}
	if len(contract.Capabilities) == 0 {
		add(Diagnostic{Severity: SeverityWarning, Code: "connector.capabilities_empty", Message: "connector declares no capabilities", File: manifestPath, Field: "capabilities"})
	}
}

func validateConnectorCapabilities(connectorRoot, manifestPath string, contract ConnectorContract, item ConnectorFacetItem, add func(Diagnostic)) []ConnectorCapabilityItem {
	out := []ConnectorCapabilityItem{}
	seenEndpoints := map[string]bool{}
	for idx, spec := range contract.Capabilities {
		field := fmt.Sprintf("capabilities[%d]", idx)
		capability := ConnectorCapabilityItem{
			Endpoint:                    spec.Endpoint,
			DisplayName:                 spec.DisplayName,
			Description:                 spec.Description,
			ClassNamespace:              firstNonEmptyString(spec.ClassNamespace, "connector."+strings.ReplaceAll(contract.Provider.Key, "-", "_")),
			ClassName:                   firstNonEmptyString(spec.ClassName, strings.ReplaceAll(spec.Endpoint, ".", "_")),
			Form:                        spec.Form,
			RiskLevel:                   spec.RiskLevel,
			SideEffects:                 append([]string{}, spec.SideEffects...),
			ExecutionAuthorizationLevel: spec.ExecutionAuthorizationLevel,
			RequiresApproval:            spec.RequiresApproval,
			InputSchema:                 spec.InputSchema,
			OutputSchema:                spec.OutputSchema,
			PolicyRequirements:          spec.PolicyRequirements,
			CredentialRequirements:      spec.CredentialRequirements,
			RuntimeKind:                 firstNonEmptyString(spec.Runtime.Kind, contract.Runtime.Kind),
			RuntimeScript:               spec.Runtime.Script,
			ContractStatus:              spec.Status,
			ExecutionMode:               spec.Runtime.DefaultMode,
			WaitTimeoutSeconds:          spec.Runtime.WaitTimeoutSeconds,
		}
		if capability.ContractStatus == "" {
			capability.ContractStatus = contract.Provider.Status
		}
		if capability.ExecutionAuthorizationLevel == 0 {
			capability.ExecutionAuthorizationLevel = defaultAuthorizationForRisk(capability.RiskLevel)
		}
		if capability.Endpoint == "" {
			add(Diagnostic{Severity: SeverityError, Code: "connector.endpoint_required", Message: "capability endpoint is required", File: manifestPath, Field: field + ".endpoint"})
		} else if !capabilityClassIdentPattern.MatchString(capability.Endpoint) {
			add(Diagnostic{Severity: SeverityError, Code: "connector.endpoint_invalid", Message: "capability endpoint must be lowercase dot-separated snake case", File: manifestPath, Field: field + ".endpoint"})
		}
		if seenEndpoints[capability.Endpoint] {
			add(Diagnostic{Severity: SeverityError, Code: "connector.endpoint_duplicate", Message: "capability endpoint is duplicated: " + capability.Endpoint, File: manifestPath, Field: field + ".endpoint"})
		}
		seenEndpoints[capability.Endpoint] = true
		if item.ProviderAddress != "" && capability.Endpoint != "" {
			address := item.ProviderAddress + "." + capability.Endpoint
			if normalized, err := capabilities.NormalizeAddress(address); err != nil {
				add(Diagnostic{Severity: SeverityError, Code: "connector.capability_address_invalid", Message: "connector capability address is invalid: " + err.Error(), File: manifestPath, Field: field + ".endpoint"})
			} else {
				capability.CapabilityAddress = normalized
			}
		}
		if capability.Form == "action" {
			capability.Form = capabilities.CapabilityFormJob
			add(Diagnostic{Severity: SeverityWarning, Code: "connector.form_alias_action", Message: "capability form action is treated as job for script-backed connectors", File: manifestPath, Field: field + ".form", Suggestion: "use form: job"})
		}
		if !capabilities.ValidCapabilityForm(capability.Form) {
			add(Diagnostic{Severity: SeverityError, Code: "connector.form_invalid", Message: "capability form is not supported", File: manifestPath, Field: field + ".form"})
		}
		if !capabilities.ValidRiskLevel(capability.RiskLevel) {
			add(Diagnostic{Severity: SeverityError, Code: "connector.risk_invalid", Message: "risk level is not supported", File: manifestPath, Field: field + ".risk_level"})
		}
		if !capabilities.ValidExecutionAuthorizationLevel(capability.ExecutionAuthorizationLevel) {
			add(Diagnostic{Severity: SeverityError, Code: "connector.authorization_invalid", Message: "execution authorization level must be between 1 and 5", File: manifestPath, Field: field + ".execution_authorization_level"})
		}
		if !capabilityClassIdentPattern.MatchString(capability.ClassNamespace) {
			add(Diagnostic{Severity: SeverityError, Code: "connector.class_namespace_invalid", Message: "capability class namespace must be lowercase dot-separated snake case", File: manifestPath, Field: field + ".class_namespace"})
		}
		if !capabilityClassIdentPattern.MatchString(capability.ClassName) {
			add(Diagnostic{Severity: SeverityError, Code: "connector.class_name_invalid", Message: "capability class name must be lowercase dot-separated snake case", File: manifestPath, Field: field + ".class_name"})
		}
		if err := validateSchemaMap(capability.InputSchema); err != nil {
			add(Diagnostic{Severity: SeverityError, Code: "connector.input_schema_invalid", Message: "input schema is invalid: " + err.Error(), File: manifestPath, Field: field + ".input_schema"})
		}
		if err := validateSchemaMap(capability.OutputSchema); err != nil {
			add(Diagnostic{Severity: SeverityError, Code: "connector.output_schema_invalid", Message: "output schema is invalid: " + err.Error(), File: manifestPath, Field: field + ".output_schema"})
		}
		if !validConnectorContractStatus(capability.ContractStatus) {
			add(Diagnostic{Severity: SeverityError, Code: "connector.capability_status_invalid", Message: "capability status is not supported", File: manifestPath, Field: field + ".status"})
		}
		if !validScriptExecutionMode(capability.ExecutionMode) {
			add(Diagnostic{Severity: SeverityError, Code: "connector.execution_mode_invalid", Message: "connector script execution mode is not supported", File: manifestPath, Field: field + ".runtime.default_mode"})
		}
		if capability.WaitTimeoutSeconds < 0 || capability.WaitTimeoutSeconds > 86400 {
			add(Diagnostic{Severity: SeverityError, Code: "connector.timeout_invalid", Message: "connector wait timeout must be between 1 and 86400 seconds", File: manifestPath, Field: field + ".runtime.wait_timeout_seconds"})
		}
		if !capabilities.ValidRuntimeKind(capability.RuntimeKind) {
			add(Diagnostic{Severity: SeverityError, Code: "connector.capability_runtime_kind_invalid", Message: "capability runtime kind is not supported", File: manifestPath, Field: field + ".runtime.kind"})
		} else if !projectConnectorRuntimeSupported(capability.RuntimeKind) {
			add(Diagnostic{Severity: SeverityWarning, Code: "connector.capability_runtime_not_activatable", Message: "capability runtime " + capability.RuntimeKind + " is known but activation is deferred until a later slice", File: manifestPath, Field: field + ".runtime.kind"})
		}
		switch capability.RuntimeKind {
		case capabilities.RuntimeKindScript:
			validateConnectorScriptRuntime(&capability, connectorRoot, manifestPath, contract, spec, field, add)
		case capabilities.RuntimeKindCommand, capabilities.RuntimeKindHTTP:
			validateConnectorDeclarativeRuntime(&capability, contract, spec, manifestPath, field, add)
		}
		if len(capability.RuntimeConfigJSON) == 0 {
			capability.RuntimeConfigJSON = connectorProfileJSON(map[string]any{
				"runtime_kind":         capability.RuntimeKind,
				"script_manifest_path": capability.ScriptManifestPath,
				"execution_mode":       capability.ExecutionMode,
				"wait_timeout_seconds": capability.WaitTimeoutSeconds,
				"implementation_hash":  capability.ImplementationHash,
			})
		}
		out = append(out, capability)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Endpoint < out[j].Endpoint
	})
	return out
}

func validateConnectorScriptRuntime(item *ConnectorCapabilityItem, connectorRoot, manifestPath string, contract ConnectorContract, spec ConnectorCapabilitySpec, field string, add func(Diagnostic)) {
	scriptRel := strings.TrimSpace(item.RuntimeScript)
	if scriptRel == "" && len(spec.Implementation.Command) > 0 {
		add(Diagnostic{Severity: SeverityError, Code: "connector.runtime_script_required", Message: "script-backed connector capabilities must reference a script package", File: manifestPath, Field: field + ".runtime.script", Suggestion: "set runtime.script to scripts/<endpoint>"})
		return
	}
	if scriptRel == "" {
		add(Diagnostic{Severity: SeverityError, Code: "connector.runtime_script_required", Message: "script-backed connector capabilities must set runtime.script", File: manifestPath, Field: field + ".runtime.script"})
		return
	}
	rel, err := normalizeRelativePath(scriptRel)
	if err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "connector.runtime_script_unsafe", Message: "runtime.script is unsafe: " + scriptRel, File: manifestPath, Field: field + ".runtime.script"})
		return
	}
	if baseDir := strings.TrimSpace(contract.Runtime.BaseDir); baseDir != "" && !strings.HasPrefix(rel, strings.TrimSuffix(filepath.ToSlash(baseDir), "/")+"/") && rel != filepath.ToSlash(baseDir) {
		add(Diagnostic{Severity: SeverityWarning, Code: "connector.runtime_script_outside_base_dir", Message: "runtime.script is outside runtime.base_dir", File: manifestPath, Field: field + ".runtime.script"})
	}
	packageRoot := filepath.Join(connectorRoot, filepath.FromSlash(rel))
	manifest := filepath.Join(packageRoot, "loom.script.yaml")
	item.RuntimeScript = rel
	item.ScriptManifestPath = filepath.ToSlash(manifest)
	loaded, err := scripts.LoadManifest(manifest)
	if err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "connector.script_manifest_invalid", Message: "connector script manifest is invalid: " + err.Error(), File: manifest, Field: field + ".runtime.script"})
		return
	}
	item.ScriptManifest = loaded
	if hash, err := scripts.HashManifest(loaded); err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "connector.script_manifest_hash_failed", Message: "could not hash connector script manifest: " + err.Error(), File: manifest, Field: field + ".runtime.script"})
	} else {
		item.ScriptManifestHash = hash
	}
	if hash, err := scripts.HashPackage(packageRoot); err != nil {
		add(Diagnostic{Severity: SeverityError, Code: "connector.script_package_hash_failed", Message: "could not hash connector script package: " + err.Error(), File: manifest, Field: field + ".runtime.script"})
	} else {
		item.ScriptPackageHash = hash
	}
	validateScriptEntrypoint(packageRoot, manifest, loaded, add)
	validateScriptUsageDocuments(packageRoot, manifest, loaded, add)
	item.ImplementationHash = connectorImplementationHash(contract, spec, item)
}

func validateConnectorUsageDocuments(connectorRoot, manifestPath string, contract ConnectorContract, add func(Diagnostic)) []ConnectorUsageDocumentItem {
	out := []ConnectorUsageDocumentItem{}
	for idx, spec := range contract.UsageDocuments {
		field := fmt.Sprintf("usage_documents[%d]", idx)
		item := ConnectorUsageDocumentItem{
			Target:   strings.TrimSpace(spec.Target),
			Endpoint: strings.TrimSpace(spec.Endpoint),
			Title:    strings.TrimSpace(spec.Title),
		}
		if item.Target == "" {
			item.Target = "provider"
		}
		rel, err := normalizeRelativePath(spec.Path)
		if err != nil || rel == "" {
			add(Diagnostic{Severity: SeverityError, Code: "connector.usage_document_path_unsafe", Message: "usage document path is unsafe: " + spec.Path, File: manifestPath, Field: field + ".path"})
			continue
		}
		path := filepath.Join(connectorRoot, filepath.FromSlash(rel))
		raw, err := os.ReadFile(path)
		if err != nil {
			add(Diagnostic{Severity: SeverityWarning, Code: "connector.usage_document_missing", Message: "usage document could not be read: " + err.Error(), File: path, Field: field + ".path"})
			continue
		}
		switch item.Target {
		case "provider", "endpoint", "capability", "capability_endpoint":
		default:
			add(Diagnostic{Severity: SeverityError, Code: "connector.usage_document_target_invalid", Message: "usage document target is not supported", File: manifestPath, Field: field + ".target", Suggestion: "use provider or endpoint"})
		}
		if item.Target != "provider" && item.Endpoint == "" {
			add(Diagnostic{Severity: SeverityError, Code: "connector.usage_document_endpoint_required", Message: "endpoint usage documents must set endpoint", File: manifestPath, Field: field + ".endpoint"})
		}
		item.Path = filepath.ToSlash(path)
		item.ContentHash = hashBytesURI(raw)
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Target != out[j].Target {
			return out[i].Target < out[j].Target
		}
		if out[i].Endpoint != out[j].Endpoint {
			return out[i].Endpoint < out[j].Endpoint
		}
		return out[i].Path < out[j].Path
	})
	return out
}

func validConnectorContractStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case ProjectStatusDraft, "active", "paused", "disabled":
		return true
	default:
		return false
	}
}

func connectorSummary(items []ConnectorFacetItem) (discovered, activatable int) {
	for _, item := range items {
		if item.ManifestPath == "" {
			continue
		}
		discovered++
		if item.ContractStatus == "disabled" || !projectConnectorRuntimeSupported(item.RuntimeKind) {
			continue
		}
		for _, capability := range item.Capabilities {
			if capability.ContractStatus != "disabled" && projectConnectorRuntimeSupported(capability.RuntimeKind) {
				activatable++
			}
		}
	}
	return discovered, activatable
}

func connectorProviderSummaries(items []ConnectorFacetItem) []string {
	out := []string{}
	for _, item := range items {
		if item.ManifestPath == "" {
			continue
		}
		for _, capability := range item.Capabilities {
			out = append(out, fmt.Sprintf("%s %s %s %s", item.ProviderAddress, capability.Endpoint, capability.RuntimeKind, capability.RiskLevel))
		}
		if len(item.Capabilities) == 0 {
			out = append(out, fmt.Sprintf("%s %s %s", item.ProviderAddress, item.RuntimeKind, item.ActivationStatus))
		}
	}
	sort.Strings(out)
	return out
}

func connectorCapabilityAddresses(items []ConnectorFacetItem) []string {
	out := []string{}
	for _, item := range items {
		for _, capability := range item.Capabilities {
			if capability.CapabilityAddress != "" {
				out = append(out, capability.CapabilityAddress)
			}
		}
	}
	sort.Strings(out)
	return out
}

func connectorProfileJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(raw)
}

func connectorImplementationHash(contract ConnectorContract, spec ConnectorCapabilitySpec, item *ConnectorCapabilityItem) string {
	payload, err := json.Marshal(map[string]any{
		"provider_key":         contract.Provider.Key,
		"provider_version":     contract.Provider.Version,
		"endpoint":             spec.Endpoint,
		"script_manifest_hash": item.ScriptManifestHash,
		"script_package_hash":  item.ScriptPackageHash,
		"runtime_config_json":  item.RuntimeConfigJSON,
		"runtime_kind":         item.RuntimeKind,
	})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validateConnectorDeclarativeRuntime(item *ConnectorCapabilityItem, contract ConnectorContract, spec ConnectorCapabilitySpec, manifestPath, field string, add func(Diagnostic)) {
	config := spec.Runtime.Config
	if config == nil {
		config = contract.Runtime.Config
	}
	if config == nil {
		config = map[string]any{}
	}
	raw := connectorProfileJSON(config)
	validation := capabilityruntime.NormalizeAndValidate(item.RuntimeKind, raw, capabilityruntime.ValidationModeRegister)
	if !validation.Valid {
		for _, diag := range validation.Errors {
			add(Diagnostic{
				Severity: SeverityError,
				Code:     diag.Code,
				Message:  diag.Message,
				File:     manifestPath,
				Field:    field + ".runtime.config" + diagnosticFieldSuffix(diag.Field),
			})
		}
		return
	}
	item.RuntimeConfigJSON = validation.Config
	item.ImplementationHash = connectorImplementationHash(contract, spec, item)
}

func projectConnectorRuntimeSupported(kind string) bool {
	switch strings.TrimSpace(kind) {
	case capabilities.RuntimeKindScript, capabilities.RuntimeKindCommand, capabilities.RuntimeKindHTTP:
		return true
	default:
		return false
	}
}

func diagnosticFieldSuffix(field string) string {
	field = strings.TrimSpace(field)
	if field == "" {
		return ""
	}
	return "." + field
}

func ignoredConnectorDir(name string) bool {
	switch name {
	case "tmp", "result", ".cache", ".git", "node_modules":
		return true
	default:
		return false
	}
}
