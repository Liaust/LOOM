package modules

import (
	"encoding/json"
	"time"
)

const (
	PackageKindNativeModule = "native_module"

	PackageStatusRegistered = "registered"

	ModuleKindNative       = "native"
	ModuleKindInternal     = "internal"
	ModuleKindExperimental = "experimental"
	ModuleKindDeprecated   = "deprecated"

	ModuleVersionStatusValid = "valid"

	RequirementKindRuntimeFeature = "runtime_feature"
	RequirementKindConnector      = "connector"
	RequirementKindCredential     = "credential"
	RequirementKindModule         = "module"

	DeclarationTargetProvider   = "provider"
	DeclarationTargetCapability = "capability"

	UsageFormatMarkdown = "markdown"
	UsageFormatPlain    = "plain"

	UsageReviewPending = "pending_review"

	BackupHookManifest = "manifest"
	BackupHookManual   = "manual"

	BackupExportKindManifest = "manifest"

	BackupExportStatusCompleted = "completed"
	BackupExportStatusFailed    = "failed"

	InstallationStatusInstalling = "installing"
	InstallationStatusInstalled  = "installed"
	InstallationStatusEnabled    = "enabled"
	InstallationStatusDisabled   = "disabled"
	InstallationStatusFailed     = "failed"
	InstallationStatusRemoved    = "removed"

	NamespaceKindModule      = "module"
	NamespaceKindProvider    = "provider"
	NamespaceKindCapability  = "capability"
	NamespaceKindEventPrefix = "event_prefix"
	NamespaceKindObjectType  = "object_type"
	NamespaceKindFilesystem  = "filesystem"
	NamespaceKindObjectStore = "object_store"
	NamespaceKindDatabase    = "database"

	NamespaceStatusReserved = "reserved"
	NamespaceStatusActive   = "active"
	NamespaceStatusDisabled = "disabled"
	NamespaceStatusFailed   = "failed"

	ExposureStatusDeclared          = "declared"
	ExposureStatusInstalledDisabled = "installed_disabled"
	ExposureStatusExposed           = "exposed"
	ExposureStatusDisabled          = "disabled"
	ExposureStatusRevoked           = "revoked"

	CompatibilityStatusOK       = "ok"
	CompatibilityStatusDegraded = "degraded"
	CompatibilityStatusBlocked  = "blocked"

	ModuleHealthStatusOK       = "ok"
	ModuleHealthStatusDegraded = "degraded"
	ModuleHealthStatusBlocked  = "blocked"
	ModuleHealthStatusUnknown  = "unknown"

	DefaultModuleLimit = 50
	MaxModuleLimit     = 200
)

type RegisterPackageInput struct {
	PackagePath string          `json:"package_path"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
}

type ModuleFilter struct {
	Limit      int
	Status     string
	ModuleID   string
	ProjectRef string
}

type ModulePackage struct {
	ModulePackageID string          `json:"module_package_id"`
	PackageKind     string          `json:"package_kind"`
	SourceURI       string          `json:"source_uri"`
	ContentHash     string          `json:"content_hash"`
	PackageSize     int64           `json:"package_size_bytes"`
	ManifestPath    string          `json:"manifest_path"`
	DiscoveredAt    time.Time       `json:"discovered_at"`
	RegisteredBy    string          `json:"registered_by_actor_id"`
	Status          string          `json:"status"`
	Metadata        json.RawMessage `json:"metadata"`
}

type ModuleVersion struct {
	ModuleVersionID  string          `json:"module_version_id"`
	ModuleID         string          `json:"module_id"`
	ModuleName       string          `json:"module_name"`
	Version          string          `json:"version"`
	ModulePackageID  string          `json:"module_package_id"`
	ManifestHash     string          `json:"manifest_hash"`
	ManifestJSON     json.RawMessage `json:"manifest_json"`
	ModuleKind       string          `json:"module_kind"`
	CompatibleMin    string          `json:"compatible_core_min"`
	CompatibleMax    string          `json:"compatible_core_max"`
	SourceRef        string          `json:"source_ref"`
	Description      string          `json:"description"`
	RegisteredAt     time.Time       `json:"registered_at"`
	RegisteredBy     string          `json:"registered_by_actor_id"`
	Status           string          `json:"status"`
	ValidationErrors json.RawMessage `json:"validation_errors"`
	Metadata         json.RawMessage `json:"metadata"`
}

type RuntimeRequirement struct {
	ModuleRequirementID string          `json:"module_requirement_id"`
	ModuleVersionID     string          `json:"module_version_id"`
	RequirementKind     string          `json:"requirement_kind"`
	RequirementKey      string          `json:"requirement_key"`
	RequiredVersion     string          `json:"required_version"`
	RequiredStatus      string          `json:"required_status"`
	Optional            bool            `json:"optional"`
	Metadata            json.RawMessage `json:"metadata"`
}

type ObjectTypeDeclaration struct {
	ModuleDeclarationID   string          `json:"module_declaration_id"`
	ModuleVersionID       string          `json:"module_version_id"`
	ObjectType            string          `json:"object_type"`
	SchemaJSON            json.RawMessage `json:"schema_json"`
	DefaultClassification string          `json:"default_classification"`
	DefaultIndexingPolicy string          `json:"default_indexing_policy"`
	DefaultBackupPolicy   string          `json:"default_backup_policy"`
	Metadata              json.RawMessage `json:"metadata"`
}

type ProviderDeclaration struct {
	ModuleDeclarationID string          `json:"module_declaration_id"`
	ModuleVersionID     string          `json:"module_version_id"`
	ProviderKey         string          `json:"provider_key"`
	DisplayName         string          `json:"display_name"`
	Description         string          `json:"description"`
	ProviderType        string          `json:"provider_type"`
	RuntimeRequirements json.RawMessage `json:"runtime_requirements"`
	HealthCheckSpec     json.RawMessage `json:"health_check_spec"`
	Metadata            json.RawMessage `json:"metadata"`
}

type CapabilityDeclaration struct {
	ModuleDeclarationID         string          `json:"module_declaration_id"`
	ModuleVersionID             string          `json:"module_version_id"`
	ProviderKey                 string          `json:"provider_key"`
	EndpointName                string          `json:"endpoint_name"`
	CapabilityClassNamespace    string          `json:"capability_class_namespace"`
	CapabilityClassName         string          `json:"capability_class_name"`
	DisplayName                 string          `json:"display_name"`
	Description                 string          `json:"description"`
	Form                        string          `json:"form"`
	InputSchemaJSON             json.RawMessage `json:"input_schema_json"`
	OutputSchemaJSON            json.RawMessage `json:"output_schema_json"`
	ExecutionAuthorizationLevel int             `json:"execution_authorization_level"`
	RiskLevel                   string          `json:"risk_level"`
	SideEffectsJSON             json.RawMessage `json:"side_effects_json"`
	CredentialRequirementsJSON  json.RawMessage `json:"credential_requirements_json"`
	PolicyRequirementsJSON      json.RawMessage `json:"policy_requirements_json"`
	Metadata                    json.RawMessage `json:"metadata"`
}

type UsageDocumentDeclaration struct {
	ModuleDeclarationID  string          `json:"module_declaration_id"`
	ModuleVersionID      string          `json:"module_version_id"`
	TargetKind           string          `json:"target_kind"`
	TargetRef            string          `json:"target_ref"`
	Title                string          `json:"title"`
	Path                 string          `json:"path"`
	BodyFormat           string          `json:"body_format"`
	SectionMapJSON       json.RawMessage `json:"section_map_json"`
	VisibilityPolicyJSON json.RawMessage `json:"visibility_policy_json"`
	ReviewStatus         string          `json:"review_status"`
	ContentHash          string          `json:"content_hash"`
	Metadata             json.RawMessage `json:"metadata"`
}

type BackupHookDeclaration struct {
	ModuleDeclarationID string          `json:"module_declaration_id"`
	ModuleVersionID     string          `json:"module_version_id"`
	HookKey             string          `json:"hook_key"`
	HookKind            string          `json:"hook_kind"`
	TriggerMode         string          `json:"trigger_mode"`
	OutputSchemaJSON    json.RawMessage `json:"output_schema_json"`
	RetentionPolicyJSON json.RawMessage `json:"retention_policy_json"`
	Metadata            json.RawMessage `json:"metadata"`
}

type ModuleRegistration struct {
	Package        ModulePackage              `json:"package"`
	Version        ModuleVersion              `json:"version"`
	Requirements   []RuntimeRequirement       `json:"requirements"`
	ObjectTypes    []ObjectTypeDeclaration    `json:"object_types"`
	Providers      []ProviderDeclaration      `json:"providers"`
	Capabilities   []CapabilityDeclaration    `json:"capabilities"`
	UsageDocuments []UsageDocumentDeclaration `json:"usage_documents"`
	BackupHooks    []BackupHookDeclaration    `json:"backup_hooks"`
	Registered     bool                       `json:"registered"`
	Idempotent     bool                       `json:"idempotent"`
	Message        string                     `json:"message,omitempty"`
}

type ModuleListItem struct {
	ModuleID        string    `json:"module_id"`
	ModuleName      string    `json:"module_name"`
	LatestVersionID string    `json:"latest_module_version_id"`
	LatestVersion   string    `json:"latest_version"`
	ModuleKind      string    `json:"module_kind"`
	Status          string    `json:"status"`
	Description     string    `json:"description"`
	RegisteredAt    time.Time `json:"registered_at"`
	VersionCount    int       `json:"version_count"`
}

type ModuleInspection struct {
	Module         ModuleListItem             `json:"module"`
	Versions       []ModuleVersion            `json:"versions"`
	LatestVersion  ModuleVersion              `json:"latest_version"`
	Requirements   []RuntimeRequirement       `json:"requirements"`
	ObjectTypes    []ObjectTypeDeclaration    `json:"object_types"`
	Providers      []ProviderDeclaration      `json:"providers"`
	Capabilities   []CapabilityDeclaration    `json:"capabilities"`
	UsageDocuments []UsageDocumentDeclaration `json:"usage_documents"`
	BackupHooks    []BackupHookDeclaration    `json:"backup_hooks"`
}

type ModuleVersionInspection struct {
	Package        ModulePackage              `json:"package"`
	Version        ModuleVersion              `json:"version"`
	Requirements   []RuntimeRequirement       `json:"requirements"`
	ObjectTypes    []ObjectTypeDeclaration    `json:"object_types"`
	Providers      []ProviderDeclaration      `json:"providers"`
	Capabilities   []CapabilityDeclaration    `json:"capabilities"`
	UsageDocuments []UsageDocumentDeclaration `json:"usage_documents"`
	BackupHooks    []BackupHookDeclaration    `json:"backup_hooks"`
}

type InstallModuleInput struct {
	ModuleVersionRef string          `json:"module_version_ref,omitempty"`
	TargetNodeRef    string          `json:"target_node_ref,omitempty"`
	InstallScopeRef  string          `json:"install_scope_ref,omitempty"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
}

type CompatibilityInput struct {
	ModuleVersionRef string `json:"module_version_ref"`
	TargetNodeRef    string `json:"target_node_ref"`
	InstallScopeRef  string `json:"install_scope_ref,omitempty"`
}

type CompatibilityReport struct {
	CanInstall          bool     `json:"can_install"`
	Status              string   `json:"status"`
	ModuleVersionID     string   `json:"module_version_id,omitempty"`
	ModuleID            string   `json:"module_id,omitempty"`
	TargetNodeID        string   `json:"target_node_id,omitempty"`
	InstallScopeID      string   `json:"install_scope_id,omitempty"`
	MissingRequirements []string `json:"missing_requirements"`
	Warnings            []string `json:"warnings"`
	RequiredApprovals   []string `json:"required_approvals"`
}

type ModuleInstallation struct {
	ModuleInstallationID string          `json:"module_installation_id"`
	ModuleVersionID      string          `json:"module_version_id"`
	TargetNodeID         string          `json:"target_node_id"`
	InstallScopeID       string          `json:"install_scope_id"`
	InstalledByActorID   string          `json:"installed_by_actor_id"`
	Status               string          `json:"status"`
	NamespaceRoot        string          `json:"namespace_root"`
	FilesystemPath       string          `json:"filesystem_path"`
	ObjectStorePrefix    string          `json:"object_store_prefix"`
	DatabaseName         string          `json:"database_name"`
	CompatibilityReport  json.RawMessage `json:"compatibility_report"`
	FailureReason        string          `json:"failure_reason"`
	InstalledAt          *time.Time      `json:"installed_at,omitempty"`
	EnabledAt            *time.Time      `json:"enabled_at,omitempty"`
	DisabledAt           *time.Time      `json:"disabled_at,omitempty"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
	Metadata             json.RawMessage `json:"metadata"`
}

type ModuleNamespace struct {
	ModuleNamespaceID    string          `json:"module_namespace_id"`
	ModuleInstallationID string          `json:"module_installation_id"`
	ModuleVersionID      string          `json:"module_version_id"`
	NamespaceKind        string          `json:"namespace_kind"`
	NamespaceKey         string          `json:"namespace_key"`
	NamespaceValue       string          `json:"namespace_value"`
	Status               string          `json:"status"`
	Metadata             json.RawMessage `json:"metadata"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
}

type InstalledProvider struct {
	ModuleInstallationID string          `json:"module_installation_id"`
	ModuleDeclarationID  string          `json:"module_declaration_id"`
	ProviderID           string          `json:"provider_id"`
	ProviderKey          string          `json:"provider_key"`
	CompactAddress       string          `json:"compact_address"`
	DisplayName          string          `json:"display_name"`
	Description          string          `json:"description"`
	ProviderType         string          `json:"provider_type"`
	Status               string          `json:"status"`
	ExposureStatus       string          `json:"exposure_status"`
	HealthStatus         string          `json:"health_status"`
	AvailabilityStatus   string          `json:"availability_status"`
	Metadata             json.RawMessage `json:"metadata"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
}

type InstalledCapability struct {
	ModuleInstallationID        string          `json:"module_installation_id"`
	ModuleDeclarationID         string          `json:"module_declaration_id"`
	CapabilityClassID           string          `json:"capability_class_id"`
	CapabilityEndpointID        string          `json:"capability_endpoint_id"`
	CapabilityEndpointVersionID string          `json:"capability_endpoint_version_id"`
	EndpointName                string          `json:"endpoint_name"`
	CompactAddress              string          `json:"compact_address"`
	ClassName                   string          `json:"class_name"`
	DisplayName                 string          `json:"display_name"`
	Description                 string          `json:"description"`
	Form                        string          `json:"form"`
	RiskLevel                   string          `json:"risk_level"`
	ExecutionAuthorizationLevel int             `json:"execution_authorization_level"`
	Status                      string          `json:"status"`
	VersionStatus               string          `json:"version_status"`
	ExposureStatus              string          `json:"exposure_status"`
	UsageDocumentIDs            json.RawMessage `json:"usage_document_ids"`
	Metadata                    json.RawMessage `json:"metadata"`
	CreatedAt                   time.Time       `json:"created_at"`
	UpdatedAt                   time.Time       `json:"updated_at"`
}

type ModuleHealthSnapshot struct {
	ModuleHealthID       string          `json:"module_health_id"`
	ModuleInstallationID string          `json:"module_installation_id"`
	HealthStatus         string          `json:"health_status"`
	InstallationStatus   string          `json:"installation_status"`
	TargetNodeStatus     string          `json:"target_node_status"`
	ProviderCount        int             `json:"provider_count"`
	CapabilityCount      int             `json:"capability_count"`
	NamespaceCount       int             `json:"namespace_count"`
	MissingRequirements  json.RawMessage `json:"missing_requirements"`
	Warnings             json.RawMessage `json:"warnings"`
	DetailsJSON          json.RawMessage `json:"details_json"`
	CheckedAt            time.Time       `json:"checked_at"`
	Metadata             json.RawMessage `json:"metadata"`
}

type ModuleHealthDetail struct {
	Installation  ModuleInstallation    `json:"installation"`
	Health        ModuleHealthSnapshot  `json:"health"`
	Namespaces    []ModuleNamespace     `json:"namespaces"`
	Providers     []InstalledProvider   `json:"providers"`
	Capabilities  []InstalledCapability `json:"capabilities"`
	Compatibility CompatibilityReport   `json:"compatibility"`
}

type ModuleInstallationDetail struct {
	Installation  ModuleInstallation    `json:"installation"`
	Version       ModuleVersion         `json:"version"`
	Namespaces    []ModuleNamespace     `json:"namespaces"`
	Providers     []InstalledProvider   `json:"providers"`
	Capabilities  []InstalledCapability `json:"capabilities"`
	Health        *ModuleHealthSnapshot `json:"health,omitempty"`
	Compatibility CompatibilityReport   `json:"compatibility"`
	Idempotent    bool                  `json:"idempotent,omitempty"`
	Message       string                `json:"message,omitempty"`
}

type ExposeModuleCapabilityInput struct {
	InstallationRef string          `json:"installation_ref,omitempty"`
	CapabilityRef   string          `json:"capability_ref,omitempty"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
}

type DisableModuleCapabilityInput struct {
	InstallationRef string          `json:"installation_ref,omitempty"`
	CapabilityRef   string          `json:"capability_ref,omitempty"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
}

type ModuleCapabilityExposureResult struct {
	Installation ModuleInstallation    `json:"installation"`
	Version      ModuleVersion         `json:"version"`
	Provider     InstalledProvider     `json:"provider"`
	Capability   InstalledCapability   `json:"capability"`
	Health       *ModuleHealthSnapshot `json:"health,omitempty"`
	Status       string                `json:"status"`
	Message      string                `json:"message,omitempty"`
}

type ExportModuleBackupInput struct {
	InstallationRef string          `json:"installation_ref,omitempty"`
	Kind            string          `json:"kind,omitempty"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
}

type ModuleBackupExportFilter struct {
	InstallationRef string
	Kind            string
	Limit           int
}

type ModuleBackupExport struct {
	ModuleBackupExportID    string          `json:"module_backup_export_id"`
	ModuleInstallationID    string          `json:"module_installation_id"`
	ModuleVersionID         string          `json:"module_version_id"`
	BackupHookDeclarationID *string         `json:"backup_hook_declaration_id,omitempty"`
	ExportKind              string          `json:"export_kind"`
	ExportStatus            string          `json:"export_status"`
	PayloadJSON             json.RawMessage `json:"payload_json"`
	PayloadHash             string          `json:"payload_hash"`
	StorageURI              string          `json:"storage_uri"`
	CreatedByActorID        string          `json:"created_by_actor_id"`
	CreatedAt               time.Time       `json:"created_at"`
	Metadata                json.RawMessage `json:"metadata"`
}

type ModuleBackupExportDetail struct {
	Export       ModuleBackupExport     `json:"export"`
	Installation ModuleInstallation     `json:"installation"`
	Version      ModuleVersion          `json:"version"`
	Hook         *BackupHookDeclaration `json:"hook,omitempty"`
}
