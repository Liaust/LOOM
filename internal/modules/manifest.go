package modules

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"loom.local/loom/internal/capabilities"
)

var (
	moduleIDPattern    = regexp.MustCompile(`^loom\.[a-z0-9-]+(\.[a-z0-9-]+)*$`)
	providerKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
	endpointPattern    = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)
)

type Manifest struct {
	Module   ManifestModule   `json:"module"`
	Requires ManifestRequires `json:"requires"`
	Storage  ManifestStorage  `json:"storage"`
	Provides ManifestProvides `json:"provides"`
}

type ManifestModule struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Version           string `json:"version"`
	Kind              string `json:"kind"`
	Description       string `json:"description"`
	Owner             string `json:"owner,omitempty"`
	SourceRef         string `json:"source_ref,omitempty"`
	CompatibleCoreMin string `json:"compatible_core_min,omitempty"`
	CompatibleCoreMax string `json:"compatible_core_max,omitempty"`
}

type ManifestRequires struct {
	CoreVersionMin  string   `json:"core_version_min,omitempty"`
	CoreVersionMax  string   `json:"core_version_max,omitempty"`
	RuntimeFeatures []string `json:"runtime_features,omitempty"`
	Modules         []string `json:"modules,omitempty"`
	Connectors      []string `json:"connectors,omitempty"`
	Credentials     []string `json:"credentials,omitempty"`
}

type ManifestStorage struct {
	Database   ManifestStorageNeed `json:"database"`
	Filesystem ManifestStorageNeed `json:"filesystem"`
}

type ManifestStorageNeed struct {
	Required bool            `json:"required"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

type ManifestProvides struct {
	ObjectTypes    []ManifestObjectType    `json:"object_types"`
	Providers      []ManifestProvider      `json:"providers"`
	Capabilities   []ManifestCapability    `json:"capabilities"`
	UsageDocuments []ManifestUsageDocument `json:"usage_documents"`
	BackupHooks    []ManifestBackupHook    `json:"backup_hooks"`
}

type ManifestObjectType struct {
	ObjectType            string          `json:"object_type"`
	SchemaJSON            json.RawMessage `json:"schema_json,omitempty"`
	DefaultClassification string          `json:"default_classification,omitempty"`
	DefaultIndexingPolicy string          `json:"default_indexing_policy,omitempty"`
	DefaultBackupPolicy   string          `json:"default_backup_policy,omitempty"`
	Metadata              json.RawMessage `json:"metadata,omitempty"`
}

type ManifestProvider struct {
	ProviderKey         string          `json:"provider_key"`
	DisplayName         string          `json:"display_name"`
	Description         string          `json:"description,omitempty"`
	RuntimeRequirements json.RawMessage `json:"runtime_requirements,omitempty"`
	HealthCheckSpec     json.RawMessage `json:"health_check_spec,omitempty"`
	Metadata            json.RawMessage `json:"metadata,omitempty"`
}

type ManifestCapability struct {
	ProviderKey                 string          `json:"provider_key"`
	EndpointName                string          `json:"endpoint_name"`
	CapabilityClassNamespace    string          `json:"capability_class_namespace"`
	CapabilityClassName         string          `json:"capability_class_name"`
	DisplayName                 string          `json:"display_name"`
	Description                 string          `json:"description,omitempty"`
	Form                        string          `json:"form"`
	InputSchemaJSON             json.RawMessage `json:"input_schema_json,omitempty"`
	OutputSchemaJSON            json.RawMessage `json:"output_schema_json,omitempty"`
	ExecutionAuthorizationLevel int             `json:"execution_authorization_level,omitempty"`
	RiskLevel                   string          `json:"risk_level,omitempty"`
	SideEffectsJSON             json.RawMessage `json:"side_effects_json,omitempty"`
	CredentialRequirementsJSON  json.RawMessage `json:"credential_requirements_json,omitempty"`
	PolicyRequirementsJSON      json.RawMessage `json:"policy_requirements_json,omitempty"`
	Metadata                    json.RawMessage `json:"metadata,omitempty"`
}

type ManifestUsageDocument struct {
	TargetKind           string          `json:"target_kind"`
	TargetRef            string          `json:"target_ref"`
	Title                string          `json:"title"`
	Path                 string          `json:"path"`
	BodyFormat           string          `json:"body_format,omitempty"`
	SectionMapJSON       json.RawMessage `json:"section_map_json,omitempty"`
	VisibilityPolicyJSON json.RawMessage `json:"visibility_policy_json,omitempty"`
	ReviewStatus         string          `json:"review_status,omitempty"`
	Metadata             json.RawMessage `json:"metadata,omitempty"`
	ContentHash          string          `json:"-"`
}

type ManifestBackupHook struct {
	HookKey             string          `json:"hook_key"`
	HookKind            string          `json:"hook_kind"`
	TriggerMode         string          `json:"trigger_mode,omitempty"`
	OutputSchemaJSON    json.RawMessage `json:"output_schema_json,omitempty"`
	RetentionPolicyJSON json.RawMessage `json:"retention_policy_json,omitempty"`
	Metadata            json.RawMessage `json:"metadata,omitempty"`
}

type LoadedPackage struct {
	RootPath         string
	SourceURI        string
	ManifestPath     string
	ManifestBytes    []byte
	Manifest         Manifest
	ManifestHash     string
	ContentHash      string
	PackageSizeBytes int64
}

func LoadPackage(packagePath string) (LoadedPackage, error) {
	packagePath = strings.TrimSpace(packagePath)
	if packagePath == "" {
		return LoadedPackage{}, fmt.Errorf("package_path is required")
	}
	absRoot, err := filepath.Abs(packagePath)
	if err != nil {
		return LoadedPackage{}, err
	}
	root, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return LoadedPackage{}, fmt.Errorf("package path is not readable: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return LoadedPackage{}, err
	}
	if !info.IsDir() {
		return LoadedPackage{}, fmt.Errorf("package path must be a directory")
	}

	manifestPath := filepath.Join(root, "module.json")
	manifestBytes, err := readFileWithinRoot(root, "module.json")
	if err != nil {
		return LoadedPackage{}, err
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return LoadedPackage{}, fmt.Errorf("module.json is invalid: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return LoadedPackage{}, fmt.Errorf("module.json contains trailing JSON values")
	}
	manifest = normalizeManifest(manifest)
	if err := validateManifest(root, manifest); err != nil {
		return LoadedPackage{}, err
	}

	manifestHash := hashBytes(manifestBytes)
	packageSize := int64(len(manifestBytes))
	fileHashes := []string{"module.json=" + manifestHash}
	for i := range manifest.Provides.UsageDocuments {
		body, err := readFileWithinRoot(root, manifest.Provides.UsageDocuments[i].Path)
		if err != nil {
			return LoadedPackage{}, fmt.Errorf("usage document %q is not readable: %w", manifest.Provides.UsageDocuments[i].Path, err)
		}
		contentHash := hashBytes(body)
		manifest.Provides.UsageDocuments[i].ContentHash = contentHash
		packageSize += int64(len(body))
		fileHashes = append(fileHashes, manifest.Provides.UsageDocuments[i].Path+"="+contentHash)
	}
	sort.Strings(fileHashes)
	packageHash := hashBytes([]byte(strings.Join(fileHashes, "\n")))

	return LoadedPackage{
		RootPath:         root,
		SourceURI:        root,
		ManifestPath:     manifestPath,
		ManifestBytes:    manifestBytes,
		Manifest:         manifest,
		ManifestHash:     manifestHash,
		ContentHash:      packageHash,
		PackageSizeBytes: packageSize,
	}, nil
}

func normalizeManifest(manifest Manifest) Manifest {
	manifest.Module.ID = strings.TrimSpace(manifest.Module.ID)
	manifest.Module.Name = strings.TrimSpace(manifest.Module.Name)
	manifest.Module.Version = strings.TrimSpace(manifest.Module.Version)
	manifest.Module.Kind = strings.TrimSpace(manifest.Module.Kind)
	if manifest.Module.Kind == "" {
		manifest.Module.Kind = ModuleKindNative
	}
	manifest.Module.Description = strings.TrimSpace(manifest.Module.Description)
	manifest.Module.SourceRef = strings.TrimSpace(manifest.Module.SourceRef)
	manifest.Module.CompatibleCoreMin = strings.TrimSpace(firstNonEmpty(manifest.Module.CompatibleCoreMin, manifest.Requires.CoreVersionMin))
	manifest.Module.CompatibleCoreMax = strings.TrimSpace(firstNonEmpty(manifest.Module.CompatibleCoreMax, manifest.Requires.CoreVersionMax))

	manifest.Requires.RuntimeFeatures = normalizeStringList(manifest.Requires.RuntimeFeatures)
	manifest.Requires.Modules = normalizeStringList(manifest.Requires.Modules)
	manifest.Requires.Connectors = normalizeStringList(manifest.Requires.Connectors)
	manifest.Requires.Credentials = normalizeStringList(manifest.Requires.Credentials)

	for i := range manifest.Provides.ObjectTypes {
		obj := &manifest.Provides.ObjectTypes[i]
		obj.ObjectType = strings.TrimSpace(obj.ObjectType)
		obj.SchemaJSON = objectOrDefault(obj.SchemaJSON)
		obj.Metadata = objectOrDefault(obj.Metadata)
		if strings.TrimSpace(obj.DefaultClassification) == "" {
			obj.DefaultClassification = "internal"
		}
		if strings.TrimSpace(obj.DefaultIndexingPolicy) == "" {
			obj.DefaultIndexingPolicy = "none"
		}
		if strings.TrimSpace(obj.DefaultBackupPolicy) == "" {
			obj.DefaultBackupPolicy = "module_owned"
		}
	}
	for i := range manifest.Provides.Providers {
		provider := &manifest.Provides.Providers[i]
		provider.ProviderKey = strings.TrimSpace(provider.ProviderKey)
		provider.DisplayName = strings.TrimSpace(provider.DisplayName)
		provider.Description = strings.TrimSpace(provider.Description)
		provider.RuntimeRequirements = objectOrDefault(provider.RuntimeRequirements)
		provider.HealthCheckSpec = objectOrDefault(provider.HealthCheckSpec)
		provider.Metadata = objectOrDefault(provider.Metadata)
	}
	for i := range manifest.Provides.Capabilities {
		capability := &manifest.Provides.Capabilities[i]
		capability.ProviderKey = strings.TrimSpace(capability.ProviderKey)
		capability.EndpointName = strings.TrimSpace(capability.EndpointName)
		capability.CapabilityClassNamespace = strings.TrimSpace(capability.CapabilityClassNamespace)
		capability.CapabilityClassName = strings.TrimSpace(capability.CapabilityClassName)
		capability.DisplayName = strings.TrimSpace(capability.DisplayName)
		capability.Description = strings.TrimSpace(capability.Description)
		capability.Form = strings.TrimSpace(capability.Form)
		if capability.Form == "" {
			capability.Form = capabilities.CapabilityFormQuery
		}
		capability.InputSchemaJSON = objectOrDefault(capability.InputSchemaJSON)
		capability.OutputSchemaJSON = objectOrDefault(capability.OutputSchemaJSON)
		if capability.ExecutionAuthorizationLevel == 0 {
			capability.ExecutionAuthorizationLevel = 1
		}
		capability.RiskLevel = strings.TrimSpace(capability.RiskLevel)
		if capability.RiskLevel == "" {
			capability.RiskLevel = capabilities.RiskLevelLow
		}
		capability.SideEffectsJSON = objectOrDefault(capability.SideEffectsJSON)
		capability.CredentialRequirementsJSON = objectOrDefault(capability.CredentialRequirementsJSON)
		capability.PolicyRequirementsJSON = objectOrDefault(capability.PolicyRequirementsJSON)
		capability.Metadata = objectOrDefault(capability.Metadata)
	}
	for i := range manifest.Provides.UsageDocuments {
		doc := &manifest.Provides.UsageDocuments[i]
		doc.TargetKind = strings.TrimSpace(doc.TargetKind)
		doc.TargetRef = strings.TrimSpace(doc.TargetRef)
		doc.Title = strings.TrimSpace(doc.Title)
		doc.Path = normalizeRelPath(doc.Path)
		doc.BodyFormat = strings.TrimSpace(doc.BodyFormat)
		if doc.BodyFormat == "" {
			doc.BodyFormat = UsageFormatMarkdown
		}
		doc.SectionMapJSON = objectOrDefault(doc.SectionMapJSON)
		doc.VisibilityPolicyJSON = objectOrDefault(doc.VisibilityPolicyJSON)
		doc.ReviewStatus = strings.TrimSpace(doc.ReviewStatus)
		if doc.ReviewStatus == "" {
			doc.ReviewStatus = UsageReviewPending
		}
		doc.Metadata = objectOrDefault(doc.Metadata)
	}
	for i := range manifest.Provides.BackupHooks {
		hook := &manifest.Provides.BackupHooks[i]
		hook.HookKey = strings.TrimSpace(hook.HookKey)
		hook.HookKind = strings.TrimSpace(hook.HookKind)
		if hook.HookKind == "" {
			hook.HookKind = BackupHookManifest
		}
		hook.TriggerMode = strings.TrimSpace(hook.TriggerMode)
		if hook.TriggerMode == "" {
			hook.TriggerMode = BackupHookManual
		}
		hook.OutputSchemaJSON = objectOrDefault(hook.OutputSchemaJSON)
		hook.RetentionPolicyJSON = objectOrDefault(hook.RetentionPolicyJSON)
		hook.Metadata = objectOrDefault(hook.Metadata)
	}
	return manifest
}

func validateManifest(root string, manifest Manifest) error {
	if !moduleIDPattern.MatchString(manifest.Module.ID) {
		return fmt.Errorf("module.id must use the loom.<slug> namespace")
	}
	if manifest.Module.Name == "" {
		return fmt.Errorf("module.name is required")
	}
	if manifest.Module.Version == "" {
		return fmt.Errorf("module.version is required")
	}
	if !validModuleKind(manifest.Module.Kind) {
		return fmt.Errorf("unsupported native module kind: %s", manifest.Module.Kind)
	}
	if err := requireJSONObject(manifest.Storage.Database.Metadata, "storage.database metadata"); err != nil {
		return err
	}
	if err := requireJSONObject(manifest.Storage.Filesystem.Metadata, "storage.filesystem metadata"); err != nil {
		return err
	}
	if len(manifest.Provides.Providers) == 0 && len(manifest.Provides.Capabilities) > 0 {
		return fmt.Errorf("capabilities require at least one provider declaration")
	}
	providerKeys := map[string]struct{}{}
	for _, provider := range manifest.Provides.Providers {
		if !providerKeyPattern.MatchString(provider.ProviderKey) {
			return fmt.Errorf("provider key %q is invalid", provider.ProviderKey)
		}
		if provider.DisplayName == "" {
			return fmt.Errorf("provider %q display_name is required", provider.ProviderKey)
		}
		if err := requireJSONObject(provider.RuntimeRequirements, "provider runtime_requirements"); err != nil {
			return err
		}
		if err := requireJSONObject(provider.HealthCheckSpec, "provider health_check_spec"); err != nil {
			return err
		}
		if err := requireJSONObject(provider.Metadata, "provider metadata"); err != nil {
			return err
		}
		providerKeys[provider.ProviderKey] = struct{}{}
	}
	for _, obj := range manifest.Provides.ObjectTypes {
		if obj.ObjectType == "" {
			return fmt.Errorf("object_type is required")
		}
		if err := requireJSONObject(obj.SchemaJSON, "object type schema_json"); err != nil {
			return err
		}
		if err := requireJSONObject(obj.Metadata, "object type metadata"); err != nil {
			return err
		}
	}
	for _, capability := range manifest.Provides.Capabilities {
		if _, ok := providerKeys[capability.ProviderKey]; !ok {
			return fmt.Errorf("capability provider_key %q has no provider declaration", capability.ProviderKey)
		}
		if !endpointPattern.MatchString(capability.EndpointName) {
			return fmt.Errorf("capability endpoint_name %q is invalid", capability.EndpointName)
		}
		if capability.CapabilityClassNamespace == "" {
			return fmt.Errorf("capability %q capability_class_namespace is required", capability.EndpointName)
		}
		if capability.CapabilityClassName == "" {
			return fmt.Errorf("capability %q capability_class_name is required", capability.EndpointName)
		}
		if capability.DisplayName == "" {
			return fmt.Errorf("capability %q display_name is required", capability.EndpointName)
		}
		if !validCapabilityForm(capability.Form) {
			return fmt.Errorf("capability %q form %q is invalid", capability.EndpointName, capability.Form)
		}
		if capability.ExecutionAuthorizationLevel < 1 || capability.ExecutionAuthorizationLevel > 5 {
			return fmt.Errorf("capability %q execution_authorization_level must be 1-5", capability.EndpointName)
		}
		if !validRiskLevel(capability.RiskLevel) {
			return fmt.Errorf("capability %q risk_level %q is invalid", capability.EndpointName, capability.RiskLevel)
		}
		for _, field := range []struct {
			label string
			value json.RawMessage
		}{
			{"capability input_schema_json", capability.InputSchemaJSON},
			{"capability output_schema_json", capability.OutputSchemaJSON},
			{"capability side_effects_json", capability.SideEffectsJSON},
			{"capability credential_requirements_json", capability.CredentialRequirementsJSON},
			{"capability policy_requirements_json", capability.PolicyRequirementsJSON},
			{"capability metadata", capability.Metadata},
		} {
			if err := requireJSONObject(field.value, field.label); err != nil {
				return err
			}
		}
	}
	targets := declaredTargets(manifest)
	for _, doc := range manifest.Provides.UsageDocuments {
		if doc.TargetKind != DeclarationTargetProvider && doc.TargetKind != DeclarationTargetCapability {
			return fmt.Errorf("usage document target_kind %q is invalid", doc.TargetKind)
		}
		if doc.TargetRef == "" {
			return fmt.Errorf("usage document target_ref is required")
		}
		if _, ok := targets[doc.TargetKind+":"+doc.TargetRef]; !ok {
			return fmt.Errorf("usage document target %s:%s is not declared", doc.TargetKind, doc.TargetRef)
		}
		if doc.Title == "" {
			return fmt.Errorf("usage document title is required")
		}
		if err := validateRelPath(doc.Path); err != nil {
			return fmt.Errorf("usage document path %q is invalid: %w", doc.Path, err)
		}
		if doc.BodyFormat != UsageFormatMarkdown && doc.BodyFormat != UsageFormatPlain {
			return fmt.Errorf("usage document %q body_format %q is invalid", doc.Path, doc.BodyFormat)
		}
		if err := requireJSONObject(doc.SectionMapJSON, "usage document section_map_json"); err != nil {
			return err
		}
		if err := requireJSONObject(doc.VisibilityPolicyJSON, "usage document visibility_policy_json"); err != nil {
			return err
		}
		if err := requireJSONObject(doc.Metadata, "usage document metadata"); err != nil {
			return err
		}
		if _, err := readFileWithinRoot(root, doc.Path); err != nil {
			return fmt.Errorf("usage document %q is not readable: %w", doc.Path, err)
		}
	}
	for _, hook := range manifest.Provides.BackupHooks {
		if !providerKeyPattern.MatchString(hook.HookKey) {
			return fmt.Errorf("backup hook key %q is invalid", hook.HookKey)
		}
		if !validBackupHookKind(hook.HookKind) {
			return fmt.Errorf("backup hook %q kind %q is invalid", hook.HookKey, hook.HookKind)
		}
		if !validBackupTriggerMode(hook.TriggerMode) {
			return fmt.Errorf("backup hook %q trigger_mode %q is invalid", hook.HookKey, hook.TriggerMode)
		}
		if err := requireJSONObject(hook.OutputSchemaJSON, "backup hook output_schema_json"); err != nil {
			return err
		}
		if err := requireJSONObject(hook.RetentionPolicyJSON, "backup hook retention_policy_json"); err != nil {
			return err
		}
		if err := requireJSONObject(hook.Metadata, "backup hook metadata"); err != nil {
			return err
		}
	}
	return nil
}

func declaredTargets(manifest Manifest) map[string]struct{} {
	targets := map[string]struct{}{}
	for _, provider := range manifest.Provides.Providers {
		targets[DeclarationTargetProvider+":"+provider.ProviderKey] = struct{}{}
	}
	for _, capability := range manifest.Provides.Capabilities {
		targets[DeclarationTargetCapability+":"+capability.ProviderKey+"."+capability.EndpointName] = struct{}{}
		targets[DeclarationTargetCapability+":"+capability.EndpointName] = struct{}{}
	}
	return targets
}

func readFileWithinRoot(root, rel string) ([]byte, error) {
	if err := validateRelPath(rel); err != nil {
		return nil, err
	}
	cleanRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(cleanRoot, rel)
	cleanFile, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(cleanRoot, cleanFile)
	if err != nil {
		return nil, err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("path escapes package root")
	}
	info, err := os.Stat(cleanFile)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("path is a directory")
	}
	return os.ReadFile(cleanFile)
}

func validateRelPath(rel string) error {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return fmt.Errorf("path is required")
	}
	if filepath.IsAbs(rel) {
		return fmt.Errorf("absolute paths are not allowed")
	}
	clean := filepath.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path traversal is not allowed")
	}
	if strings.ContainsRune(rel, 0) {
		return fmt.Errorf("null bytes are not allowed")
	}
	return nil
}

func normalizeRelPath(rel string) string {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(rel))
}

func objectOrDefault(value json.RawMessage) json.RawMessage {
	value = bytes.TrimSpace(value)
	if len(value) == 0 || bytes.Equal(value, []byte("null")) {
		return json.RawMessage(`{}`)
	}
	return value
}

func requireJSONObject(value json.RawMessage, label string) error {
	value = objectOrDefault(value)
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return fmt.Errorf("%s must be valid JSON: %w", label, err)
	}
	if _, ok := decoded.(map[string]any); !ok {
		return fmt.Errorf("%s must be a JSON object", label)
	}
	return nil
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func normalizeStringList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func validModuleKind(value string) bool {
	switch value {
	case ModuleKindNative, ModuleKindInternal, ModuleKindExperimental, ModuleKindDeprecated:
		return true
	default:
		return false
	}
}

func validCapabilityForm(value string) bool {
	switch value {
	case capabilities.CapabilityFormQuery,
		capabilities.CapabilityFormCommand,
		capabilities.CapabilityFormJob,
		capabilities.CapabilityFormSession,
		capabilities.CapabilityFormStream,
		capabilities.CapabilityFormSubscription,
		capabilities.CapabilityFormLease:
		return true
	default:
		return false
	}
}

func validRiskLevel(value string) bool {
	switch value {
	case capabilities.RiskLevelLow, capabilities.RiskLevelMedium, capabilities.RiskLevelHigh, capabilities.RiskLevelCritical:
		return true
	default:
		return false
	}
}

func validBackupHookKind(value string) bool {
	switch value {
	case "manifest", "database_export", "file_export", "object_manifest", "restore_metadata":
		return true
	default:
		return false
	}
}

func validBackupTriggerMode(value string) bool {
	switch value {
	case "manual", "scheduled_later", "pre_update", "pre_remove":
		return true
	default:
		return false
	}
}
