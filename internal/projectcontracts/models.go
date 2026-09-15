package projectcontracts

import (
	"encoding/json"
	"time"

	"loom.local/loom/internal/modules"
	"loom.local/loom/internal/scripts"
)

const (
	ProjectMetadataDir        = ".loom"
	CanonicalRootContractPath = ".loom/project.yaml"
	LegacyRootContractPath    = "loom.project.yaml"
	ProjectKind               = "loom.project"
	ProjectSchemaV03          = "project.contract.v0.3"
	ProjectSchemaV04          = "project.contract.v0.4"
	CredentialPolicyKind      = "loom.credentials_policy"
	CredentialPolicySchemaV03 = "credentials.policy.v0.3"
	PlanSchemaV03             = "project.plan.v0.3"
	ReportSchemaV03           = "project.validation_report.v0.3"
	SeverityError             = "error"
	SeverityWarning           = "warning"
	SeverityInfo              = "info"
	ProjectStatusDraft        = "draft"
	ProjectStatusActive       = "active"
	ProjectStatusPaused       = "paused"
	ProjectStatusArchived     = "archived"
	ScriptExposureKind        = "loom.script_exposure"
	ScriptExposureSchemaV03   = "script.exposure.v0.3"
	WorkflowContractKind      = "loom.workflow"
	WorkflowSchemaV03         = "workflow.contract.v0.3"
	WorkflowSchemaV031        = "workflow.contract.v0.3.1"
	ScheduleContractKind      = "loom.schedule"
	ScheduleSchemaV03         = "schedule.contract.v0.3"
	DirectEventContractKind   = "loom.direct_event"
	DirectEventSchemaV03      = "direct_event.contract.v0.3"
	ConnectorContractKind     = "loom.connector"
	ConnectorSchemaV03        = "connector.contract.v0.3"
	ModuleProjectKind         = "loom.module_project"
	ModuleProjectSchemaV03    = "module_project.contract.v0.3"
	NotesContractKind         = "loom.notes"
	NotesSchemaV03            = "notes.contract.v0.3"
	ReposContractKind         = "loom.repos"
	ReposSchemaV03            = "repos.contract.v0.3"
	ReposSchemaV04            = "repos.contract.v0.4"
	RepositoryIDPrefix        = "repo"
	RepositoryRolePrimary     = "primary"
	RepositoryRoleComponent   = "component"
	RepositoryRoleReference   = "reference"
	SyncPolicyKind            = "loom.project_sync_policy"
	SyncPolicySchemaV03       = "sync.policy.v0.3"
	BackupPolicyKind          = "loom.project_backup_policy"
	BackupPolicySchemaV03     = "backup.policy.v0.3"
)

type ProjectLayout string

const (
	ProjectLayoutCanonical           ProjectLayout = "canonical"
	ProjectLayoutLegacy              ProjectLayout = "legacy"
	ProjectLayoutCanonicalWithLegacy ProjectLayout = "canonical_with_legacy"
)

type ProjectContract struct {
	Kind             string           `json:"kind" yaml:"kind"`
	SchemaVersion    string           `json:"schema_version" yaml:"schema_version"`
	Project          ProjectSpec      `json:"project" yaml:"project"`
	Facets           map[string]bool  `json:"facets,omitempty" yaml:"facets"`
	ProviderDefaults ProviderDefaults `json:"provider_defaults,omitempty" yaml:"provider_defaults"`
	Policies         PolicyRefs       `json:"policies,omitempty" yaml:"policies"`
	Portal           PortalSpec       `json:"portal,omitempty" yaml:"portal"`
	Metadata         map[string]any   `json:"metadata,omitempty" yaml:"metadata"`
}

type ProjectSpec struct {
	ID          string `json:"id,omitempty" yaml:"id,omitempty"`
	Slug        string `json:"slug" yaml:"slug"`
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description"`
	OwnerNode   string `json:"owner_node" yaml:"owner_node"`
	Status      string `json:"status,omitempty" yaml:"status"`
}

type ProviderDefaults struct {
	ScriptsProvider   string `json:"scripts_provider,omitempty" yaml:"scripts_provider"`
	WorkflowsProvider string `json:"workflows_provider,omitempty" yaml:"workflows_provider"`
}

type PolicyRefs struct {
	Sync        string `json:"sync,omitempty" yaml:"sync"`
	Backup      string `json:"backup,omitempty" yaml:"backup"`
	Workers     string `json:"workers,omitempty" yaml:"workers"`
	Credentials string `json:"credentials,omitempty" yaml:"credentials"`
}

type PortalSpec struct {
	DisplayGroup string `json:"display_group,omitempty" yaml:"display_group"`
	Summary      string `json:"summary,omitempty" yaml:"summary"`
}

type LoadedProject struct {
	Declaration      *ProjectDeclaration `json:"declaration,omitempty"`
	candidateSources *scaffoldCandidateSources
	RootPath         string                 `json:"root_path"`
	ContractPath     string                 `json:"contract_path"`
	Layout           ProjectLayout          `json:"layout"`
	Discovery        ProjectLayoutDiscovery `json:"discovery"`
	Raw              []byte                 `json:"-"`
	Contract         ProjectContract        `json:"contract"`
}

type ProjectLayoutDiscovery struct {
	CanonicalPath    string `json:"canonical_path"`
	LegacyPath       string `json:"legacy_path"`
	CanonicalPresent bool   `json:"canonical_present"`
	LegacyPresent    bool   `json:"legacy_present"`
}

type Diagnostic struct {
	Severity   string `json:"severity"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	File       string `json:"file,omitempty"`
	Field      string `json:"field,omitempty"`
	Suggestion string `json:"suggestion,omitempty"`
}

type DiagnosticSummary struct {
	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
	Infos    int `json:"infos"`
}

// RepositorySourceSnapshot identifies the exact repositories contract bytes
// consumed by validation. It is projected unchanged into the registration
// plan so registration never needs to reopen the contract after analysis.
type RepositorySourceSnapshot struct {
	ContractPath          string `json:"contract_path"`
	ContractHash          string `json:"contract_hash"`
	ContractSchemaVersion string `json:"contract_schema_version"`
}

type ValidationReport struct {
	Declaration       *DeclarationCompilation   `json:"declaration,omitempty"`
	SchemaVersion     string                    `json:"schema_version"`
	GeneratedAt       time.Time                 `json:"generated_at"`
	ProjectRoot       string                    `json:"project_root"`
	ContractPath      string                    `json:"contract_path"`
	OK                bool                      `json:"ok"`
	Registerable      bool                      `json:"registerable"`
	Summary           DiagnosticSummary         `json:"summary"`
	Project           PlanProject               `json:"project"`
	DerivedProviders  []PlanProvider            `json:"derived_providers"`
	Facets            []PlanFacet               `json:"facets"`
	PolicyRefs        []PlanPolicyRef           `json:"policy_refs"`
	Notes             []NotesFacetItem          `json:"notes,omitempty"`
	Repos             []RepoFacetItem           `json:"repos,omitempty"`
	RepositoryMembers []RepoMemberSpec          `json:"repository_members,omitempty"`
	RepositorySource  *RepositorySourceSnapshot `json:"repository_source,omitempty"`
	Scripts           []ScriptFacetItem         `json:"scripts,omitempty"`
	Workflows         []WorkflowFacetItem       `json:"workflows,omitempty"`
	Connectors        []ConnectorFacetItem      `json:"connectors,omitempty"`
	Modules           []ModuleFacetItem         `json:"modules,omitempty"`
	Schedules         []ScheduleFacetItem       `json:"schedules,omitempty"`
	DirectEvents      []DirectEventFacetItem    `json:"direct_events,omitempty"`
	Services          []ServiceFacetItem        `json:"services,omitempty"`
	WatchedRoots      []ProjectWatchedRootItem  `json:"watched_roots,omitempty"`
	Diagnostics       []Diagnostic              `json:"diagnostics"`
}

type ProjectPlan struct {
	Declaration         *DeclarationCompilation   `json:"declaration,omitempty"`
	SchemaVersion       string                    `json:"schema_version"`
	GeneratedAt         time.Time                 `json:"generated_at"`
	ProjectRoot         string                    `json:"project_root"`
	ContractPath        string                    `json:"contract_path"`
	Registerable        bool                      `json:"registerable"`
	Project             PlanProject               `json:"project"`
	DerivedProviders    []PlanProvider            `json:"derived_providers"`
	Facets              []PlanFacet               `json:"facets"`
	PolicyRefs          []PlanPolicyRef           `json:"policy_refs"`
	Notes               []NotesFacetItem          `json:"notes,omitempty"`
	Repos               []RepoFacetItem           `json:"repos,omitempty"`
	RepositoryMembers   []RepoMemberSpec          `json:"repository_members,omitempty"`
	RepositorySource    *RepositorySourceSnapshot `json:"repository_source,omitempty"`
	Scripts             []ScriptFacetItem         `json:"scripts,omitempty"`
	Workflows           []WorkflowFacetItem       `json:"workflows,omitempty"`
	Connectors          []ConnectorFacetItem      `json:"connectors,omitempty"`
	Modules             []ModuleFacetItem         `json:"modules,omitempty"`
	Schedules           []ScheduleFacetItem       `json:"schedules,omitempty"`
	DirectEvents        []DirectEventFacetItem    `json:"direct_events,omitempty"`
	Services            []ServiceFacetItem        `json:"services,omitempty"`
	WatchedRoots        []ProjectWatchedRootItem  `json:"watched_roots,omitempty"`
	UnsupportedFeatures []PlanUnsupportedFeature  `json:"unsupported_features"`
	Actions             []PlanAction              `json:"actions"`
	Diagnostics         []Diagnostic              `json:"diagnostics"`
	Summary             DiagnosticSummary         `json:"summary"`
}

type PlanProject struct {
	ID          string `json:"id,omitempty"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	OwnerNode   string `json:"owner_node"`
	Status      string `json:"status"`
}

type PlanProvider struct {
	Kind           string `json:"kind"`
	ProviderKey    string `json:"provider_key"`
	CompactAddress string `json:"compact_address"`
	Source         string `json:"source"`
}

type PlanFacet struct {
	Key         string `json:"key"`
	Folder      string `json:"folder,omitempty"`
	Enabled     bool   `json:"enabled"`
	Present     bool   `json:"present"`
	Placeholder bool   `json:"placeholder,omitempty"`
}

// ServiceFacetItem is the validated project-local declaration projected into
// registration. Provisioning remains external: no executable or unit content
// is represented here.
type ServiceFacetItem struct {
	Key              string                      `json:"key"`
	ContractPath     string                      `json:"contract_path"`
	ContractHash     string                      `json:"contract_hash"`
	ProviderKey      string                      `json:"provider_key"`
	ProviderAddress  string                      `json:"provider_address"`
	Provisioning     string                      `json:"provisioning"`
	ActivationStatus string                      `json:"activation_status"`
	Contract         ServiceRegistrationContract `json:"contract"`
}

type PlanPolicyRef struct {
	Key     string `json:"key"`
	Path    string `json:"path"`
	Present bool   `json:"present"`
}

type ScriptFacetItem struct {
	Key                string           `json:"key"`
	Folder             string           `json:"folder"`
	ManifestPath       string           `json:"manifest_path"`
	ExposurePath       string           `json:"exposure_path,omitempty"`
	Manifest           scripts.Manifest `json:"manifest"`
	Exposure           *ScriptExposure  `json:"exposure,omitempty"`
	ManifestHash       string           `json:"manifest_hash"`
	PackageHash        string           `json:"package_hash"`
	ExposureHash       string           `json:"exposure_hash,omitempty"`
	ProviderKey        string           `json:"provider_key,omitempty"`
	ProviderAddress    string           `json:"provider_address,omitempty"`
	Endpoint           string           `json:"endpoint,omitempty"`
	CapabilityAddress  string           `json:"capability_address,omitempty"`
	Form               string           `json:"form,omitempty"`
	RiskLevel          string           `json:"risk_level,omitempty"`
	ExecutionMode      string           `json:"execution_mode,omitempty"`
	WaitTimeoutSeconds int              `json:"wait_timeout_seconds,omitempty"`
	Credentials        CredentialSpec   `json:"credentials,omitempty"`
	CredentialJSON     json.RawMessage  `json:"credential_requirements_json,omitempty"`
	Exposed            bool             `json:"exposed"`
	ActivationStatus   string           `json:"activation_status"`
}

type ScriptExposure struct {
	Kind          string                  `json:"kind" yaml:"kind"`
	SchemaVersion string                  `json:"schema_version" yaml:"schema_version"`
	Expose        ScriptExposeSpec        `json:"expose" yaml:"expose"`
	Capability    ScriptCapabilitySpec    `json:"capability" yaml:"capability"`
	Execution     ScriptExposureExecution `json:"execution" yaml:"execution"`
	Credentials   CredentialSpec          `json:"credentials,omitempty" yaml:"credentials"`
	Metadata      map[string]any          `json:"metadata,omitempty" yaml:"metadata"`
}

type ScriptExposeSpec struct {
	Enabled     bool   `json:"enabled" yaml:"enabled"`
	Provider    string `json:"provider,omitempty" yaml:"provider"`
	Endpoint    string `json:"endpoint,omitempty" yaml:"endpoint"`
	DisplayName string `json:"display_name,omitempty" yaml:"display_name"`
	Description string `json:"description,omitempty" yaml:"description"`
}

type ScriptCapabilitySpec struct {
	ClassNamespace              string         `json:"class_namespace,omitempty" yaml:"class_namespace"`
	ClassName                   string         `json:"class_name,omitempty" yaml:"class_name"`
	Form                        string         `json:"form,omitempty" yaml:"form"`
	RiskLevel                   string         `json:"risk_level,omitempty" yaml:"risk_level"`
	ExecutionAuthorizationLevel int            `json:"execution_authorization_level,omitempty" yaml:"execution_authorization_level"`
	RequiresApproval            bool           `json:"requires_approval,omitempty" yaml:"requires_approval"`
	SideEffects                 []string       `json:"side_effects,omitempty" yaml:"side_effects"`
	InputSchema                 map[string]any `json:"input_schema,omitempty" yaml:"input_schema"`
	OutputSchema                map[string]any `json:"output_schema,omitempty" yaml:"output_schema"`
}

type ScriptExposureExecution struct {
	DefaultMode        string `json:"default_mode,omitempty" yaml:"default_mode"`
	WaitTimeoutSeconds int    `json:"wait_timeout_seconds,omitempty" yaml:"wait_timeout_seconds"`
}

type CredentialSpec struct {
	Required []CredentialRequirement `json:"required,omitempty" yaml:"required"`
}

type CredentialRequirement struct {
	Ref      string `json:"ref" yaml:"ref"`
	ExposeAs string `json:"expose_as" yaml:"expose_as"`
	Kind     string `json:"kind" yaml:"kind"`
}

type CredentialPolicy struct {
	Kind          string               `json:"kind" yaml:"kind"`
	SchemaVersion string               `json:"schema_version" yaml:"schema_version"`
	Credentials   CredentialPolicyBody `json:"credentials" yaml:"credentials"`
	Metadata      map[string]any       `json:"metadata,omitempty" yaml:"metadata"`
}

type CredentialPolicyBody struct {
	InlineSecretsAllowed bool                        `json:"inline_secrets_allowed" yaml:"inline_secrets_allowed"`
	References           []CredentialPolicyReference `json:"references,omitempty" yaml:"references"`
}

type CredentialPolicyReference struct {
	Ref    string                 `json:"ref" yaml:"ref"`
	Source CredentialPolicySource `json:"source" yaml:"source"`
	Status string                 `json:"status,omitempty" yaml:"status"`
}

type CredentialPolicySource struct {
	Kind string `json:"kind" yaml:"kind"`
	Env  string `json:"env,omitempty" yaml:"env"`
	Path string `json:"path,omitempty" yaml:"path"`
}

type WorkflowFacetItem struct {
	Key                      string                  `json:"key"`
	SchemaVersion            string                  `json:"schema_version,omitempty"`
	Folder                   string                  `json:"folder"`
	ManifestPath             string                  `json:"manifest_path"`
	ManifestHash             string                  `json:"manifest_hash,omitempty"`
	WorkflowID               string                  `json:"workflow_id"`
	Name                     string                  `json:"name"`
	Description              string                  `json:"description,omitempty"`
	Version                  string                  `json:"version,omitempty"`
	ContractStatus           string                  `json:"contract_status"`
	ImplementationKind       string                  `json:"implementation_kind"`
	ScriptRef                string                  `json:"script_ref,omitempty"`
	ScriptKey                string                  `json:"script_key,omitempty"`
	ScriptManifestPath       string                  `json:"script_manifest_path,omitempty"`
	ScriptCapabilityAddress  string                  `json:"script_capability_address,omitempty"`
	Executable               bool                    `json:"executable,omitempty"`
	PackageRoot              string                  `json:"package_root,omitempty"`
	PackageHash              string                  `json:"package_hash,omitempty"`
	Entrypoint               scripts.Entrypoint      `json:"entrypoint,omitempty"`
	Runtime                  map[string]any          `json:"runtime,omitempty"`
	Execution                scripts.Execution       `json:"execution,omitempty"`
	Capability               ScriptCapabilitySpec    `json:"capability,omitempty"`
	Credentials              CredentialSpec          `json:"credentials,omitempty"`
	CredentialJSON           json.RawMessage         `json:"credential_requirements_json,omitempty"`
	Artifacts                []scripts.ArtifactSpec  `json:"artifacts,omitempty"`
	UsageDocuments           []scripts.UsageDocument `json:"usage_documents,omitempty"`
	ExposeEnabled            bool                    `json:"expose_enabled"`
	ProviderKey              string                  `json:"provider_key,omitempty"`
	ProviderAddress          string                  `json:"provider_address,omitempty"`
	Endpoint                 string                  `json:"endpoint,omitempty"`
	CapabilityAddress        string                  `json:"capability_address,omitempty"`
	WorkflowCapabilityStatus string                  `json:"workflow_capability_status,omitempty"`
	RegisteredWorkflowID     string                  `json:"registered_workflow_id,omitempty"`
	RegisteredVersionID      string                  `json:"registered_workflow_version_id,omitempty"`
	RegisteredRuntimeKind    string                  `json:"registered_runtime_kind,omitempty"`
	RuntimeBindingID         string                  `json:"runtime_binding_id,omitempty"`
	CapabilityEndpointID     string                  `json:"capability_endpoint_id,omitempty"`
	EndpointVersionID        string                  `json:"capability_endpoint_version_id,omitempty"`
	RegistrationStatus       string                  `json:"registration_status,omitempty"`
	StepCount                int                     `json:"step_count"`
	ActivationStatus         string                  `json:"activation_status"`
	Inputs                   map[string]any          `json:"inputs,omitempty"`
	Outputs                  map[string]any          `json:"outputs,omitempty"`
	Metadata                 map[string]any          `json:"metadata,omitempty"`
}

type WorkflowContract struct {
	Kind           string                  `json:"kind" yaml:"kind"`
	SchemaVersion  string                  `json:"schema_version" yaml:"schema_version"`
	Workflow       WorkflowSpec            `json:"workflow" yaml:"workflow"`
	Implementation WorkflowImplementation  `json:"implementation" yaml:"implementation"`
	Entrypoint     scripts.Entrypoint      `json:"entrypoint,omitempty" yaml:"entrypoint"`
	Runtime        map[string]any          `json:"runtime,omitempty" yaml:"runtime"`
	Execution      scripts.Execution       `json:"execution,omitempty" yaml:"execution"`
	Expose         WorkflowExposeSpec      `json:"expose,omitempty" yaml:"expose"`
	Capability     ScriptCapabilitySpec    `json:"capability,omitempty" yaml:"capability"`
	Credentials    CredentialSpec          `json:"credentials,omitempty" yaml:"credentials"`
	Inputs         map[string]any          `json:"inputs,omitempty" yaml:"inputs"`
	Outputs        map[string]any          `json:"outputs,omitempty" yaml:"outputs"`
	Artifacts      []scripts.ArtifactSpec  `json:"artifacts,omitempty" yaml:"artifacts"`
	UsageDocuments []scripts.UsageDocument `json:"usage_documents,omitempty" yaml:"usage_documents"`
	Steps          []WorkflowStepSpec      `json:"steps,omitempty" yaml:"steps"`
	Metadata       map[string]any          `json:"metadata,omitempty" yaml:"metadata"`
}

type WorkflowSpec struct {
	ID          string `json:"id" yaml:"id"`
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description"`
	Status      string `json:"status,omitempty" yaml:"status"`
	Version     string `json:"version,omitempty" yaml:"version"`
}

type WorkflowImplementation struct {
	Kind          string `json:"kind" yaml:"kind"`
	ScriptRef     string `json:"script_ref,omitempty" yaml:"script_ref"`
	CapabilityRef string `json:"capability_ref,omitempty" yaml:"capability_ref"`
}

type WorkflowExposeSpec struct {
	Enabled     bool   `json:"enabled" yaml:"enabled"`
	Provider    string `json:"provider,omitempty" yaml:"provider"`
	Endpoint    string `json:"endpoint,omitempty" yaml:"endpoint"`
	DisplayName string `json:"display_name,omitempty" yaml:"display_name"`
	Description string `json:"description,omitempty" yaml:"description"`
}

type WorkflowStepSpec struct {
	ID        string         `json:"id" yaml:"id"`
	Kind      string         `json:"kind" yaml:"kind"`
	Target    string         `json:"target,omitempty" yaml:"target"`
	ScriptRef string         `json:"script_ref,omitempty" yaml:"script_ref"`
	Input     map[string]any `json:"input,omitempty" yaml:"input"`
	Metadata  map[string]any `json:"metadata,omitempty" yaml:"metadata"`
}

type ScheduleFacetItem struct {
	Key                string          `json:"key"`
	BackendScheduleKey string          `json:"backend_schedule_key"`
	Folder             string          `json:"folder"`
	ManifestPath       string          `json:"manifest_path"`
	InputPath          string          `json:"input_path,omitempty"`
	ManifestHash       string          `json:"manifest_hash"`
	InputHash          string          `json:"input_hash,omitempty"`
	TargetCapability   string          `json:"target_capability"`
	InputJSON          json.RawMessage `json:"input_json,omitempty"`
	ScheduleKind       string          `json:"schedule_kind"`
	ScheduleExpr       string          `json:"schedule_expr"`
	Timezone           string          `json:"timezone"`
	DisplayName        string          `json:"display_name"`
	Description        string          `json:"description,omitempty"`
	ContractStatus     string          `json:"contract_status"`
	ActivationStatus   string          `json:"activation_status"`
	MisfirePolicy      string          `json:"misfire_policy"`
	LatenessWindowSecs int             `json:"lateness_window_seconds,omitempty"`
	ConcurrencyPolicy  string          `json:"concurrency_policy"`
	ApprovalPolicy     string          `json:"approval_policy"`
	TimeoutSeconds     int             `json:"timeout_seconds"`
	MaxAttempts        int             `json:"max_attempts"`
	RunAs              string          `json:"run_as,omitempty"`
	Metadata           map[string]any  `json:"metadata,omitempty"`
}

type ScheduleContract struct {
	Kind          string                  `json:"kind" yaml:"kind"`
	SchemaVersion string                  `json:"schema_version" yaml:"schema_version"`
	Schedule      ScheduleSpec            `json:"schedule" yaml:"schedule"`
	Target        ScheduleTargetSpec      `json:"target" yaml:"target"`
	Timing        ScheduleTimingSpec      `json:"timing" yaml:"timing"`
	Misfire       ScheduleMisfireSpec     `json:"misfire,omitempty" yaml:"misfire"`
	Concurrency   ScheduleConcurrencySpec `json:"concurrency,omitempty" yaml:"concurrency"`
	Approval      ScheduleApprovalSpec    `json:"approval,omitempty" yaml:"approval"`
	Timeout       ScheduleTimeoutSpec     `json:"timeout,omitempty" yaml:"timeout"`
	Retry         ScheduleRetrySpec       `json:"retry,omitempty" yaml:"retry"`
	Metadata      map[string]any          `json:"metadata,omitempty" yaml:"metadata"`
}

type ScheduleSpec struct {
	Key         string `json:"key" yaml:"key"`
	DisplayName string `json:"display_name,omitempty" yaml:"display_name"`
	Description string `json:"description,omitempty" yaml:"description"`
	Status      string `json:"status,omitempty" yaml:"status"`
}

type ScheduleTargetSpec struct {
	Capability string `json:"capability" yaml:"capability"`
	InputFile  string `json:"input_file,omitempty" yaml:"input_file"`
	RunAs      string `json:"run_as,omitempty" yaml:"run_as"`
}

type ScheduleTimingSpec struct {
	Kind       string `json:"kind" yaml:"kind"`
	Expression string `json:"expression" yaml:"expression"`
	Timezone   string `json:"timezone,omitempty" yaml:"timezone"`
}

type ScheduleMisfireSpec struct {
	Policy                string `json:"policy,omitempty" yaml:"policy"`
	LatenessWindowSeconds int    `json:"lateness_window_seconds,omitempty" yaml:"lateness_window_seconds"`
}

type ScheduleConcurrencySpec struct {
	Policy string `json:"policy,omitempty" yaml:"policy"`
}

type ScheduleApprovalSpec struct {
	Policy string `json:"policy,omitempty" yaml:"policy"`
}

type ScheduleTimeoutSpec struct {
	Seconds int `json:"seconds,omitempty" yaml:"seconds"`
}

type ScheduleRetrySpec struct {
	MaxAttempts int `json:"max_attempts,omitempty" yaml:"max_attempts"`
}

type DirectEventFacetItem struct {
	Key                      string                       `json:"key"`
	BackendIntegrationKey    string                       `json:"backend_integration_key"`
	BackendEndpointSlug      string                       `json:"backend_endpoint_slug"`
	EndpointPath             string                       `json:"endpoint_path"`
	Folder                   string                       `json:"folder"`
	ManifestPath             string                       `json:"manifest_path"`
	PayloadExamplePath       string                       `json:"payload_example_path,omitempty"`
	ExpectedInputPath        string                       `json:"expected_input_path,omitempty"`
	ManifestHash             string                       `json:"manifest_hash"`
	PayloadExampleHash       string                       `json:"payload_example_hash,omitempty"`
	ExpectedInputHash        string                       `json:"expected_input_hash,omitempty"`
	IntegrationKey           string                       `json:"integration_key"`
	IntegrationMainAuthLevel int                          `json:"integration_main_auth_level,omitempty"`
	EndpointSlug             string                       `json:"endpoint_slug"`
	DisplayName              string                       `json:"display_name"`
	Description              string                       `json:"description,omitempty"`
	EventType                string                       `json:"event_type"`
	TargetCapability         string                       `json:"target_capability"`
	ResponseMode             string                       `json:"response_mode"`
	SyncWaitTimeoutSecs      int                          `json:"sync_wait_timeout_seconds,omitempty"`
	MappingProfileJSON       json.RawMessage              `json:"mapping_profile_json"`
	IdempotencyProfileJSON   json.RawMessage              `json:"idempotency_profile_json"`
	CommunicationProfileJSON json.RawMessage              `json:"communication_profile_json"`
	StorageProfileJSON       json.RawMessage              `json:"storage_profile_json"`
	AuthProfiles             []DirectEventAuthProfileItem `json:"auth_profiles,omitempty"`
	ExamplePayloadJSON       json.RawMessage              `json:"example_payload_json,omitempty"`
	ExpectedInputJSON        json.RawMessage              `json:"expected_input_json,omitempty"`
	MappedInputJSON          json.RawMessage              `json:"mapped_input_json,omitempty"`
	ContractStatus           string                       `json:"contract_status"`
	ActivationStatus         string                       `json:"activation_status"`
	TimeoutSeconds           int                          `json:"timeout_seconds"`
	MaxAttempts              int                          `json:"max_attempts"`
	Metadata                 map[string]any               `json:"metadata,omitempty"`
}

type DirectEventAuthProfileItem struct {
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	CreateIfMissing bool   `json:"create_if_missing"`
	ExistingRef     string `json:"existing_ref,omitempty"`
}

type DirectEventContract struct {
	Kind          string                     `json:"kind" yaml:"kind"`
	SchemaVersion string                     `json:"schema_version" yaml:"schema_version"`
	Event         DirectEventEventSpec       `json:"event" yaml:"event"`
	Integration   DirectEventIntegrationSpec `json:"integration" yaml:"integration"`
	Endpoint      DirectEventEndpointSpec    `json:"endpoint" yaml:"endpoint"`
	Target        DirectEventTargetSpec      `json:"target" yaml:"target"`
	Response      DirectEventResponseSpec    `json:"response,omitempty" yaml:"response"`
	Auth          DirectEventAuthSpec        `json:"auth,omitempty" yaml:"auth"`
	Mapping       DirectEventMappingSpec     `json:"mapping" yaml:"mapping"`
	Idempotency   DirectEventIdempotencySpec `json:"idempotency,omitempty" yaml:"idempotency"`
	Timeout       DirectEventTimeoutSpec     `json:"timeout,omitempty" yaml:"timeout"`
	Retry         DirectEventRetrySpec       `json:"retry,omitempty" yaml:"retry"`
	Storage       DirectEventStorageSpec     `json:"storage,omitempty" yaml:"storage"`
	Examples      DirectEventExamplesSpec    `json:"examples,omitempty" yaml:"examples"`
	Metadata      map[string]any             `json:"metadata,omitempty" yaml:"metadata"`
}

type DirectEventEventSpec struct {
	Key         string `json:"key" yaml:"key"`
	DisplayName string `json:"display_name,omitempty" yaml:"display_name"`
	Description string `json:"description,omitempty" yaml:"description"`
	Status      string `json:"status,omitempty" yaml:"status"`
}

type DirectEventIntegrationSpec struct {
	Key           string `json:"key" yaml:"key"`
	DisplayName   string `json:"display_name,omitempty" yaml:"display_name"`
	Description   string `json:"description,omitempty" yaml:"description"`
	MainAuthLevel int    `json:"main_auth_level,omitempty" yaml:"main_auth_level"`
}

type DirectEventEndpointSpec struct {
	Slug        string `json:"slug" yaml:"slug"`
	DisplayName string `json:"display_name,omitempty" yaml:"display_name"`
	Description string `json:"description,omitempty" yaml:"description"`
	EventType   string `json:"event_type" yaml:"event_type"`
	Status      string `json:"status,omitempty" yaml:"status"`
}

type DirectEventTargetSpec struct {
	Capability string `json:"capability" yaml:"capability"`
}

type DirectEventResponseSpec struct {
	Mode                   string `json:"mode,omitempty" yaml:"mode"`
	SyncWaitTimeoutSeconds int    `json:"sync_wait_timeout_seconds,omitempty" yaml:"sync_wait_timeout_seconds"`
}

type DirectEventAuthSpec struct {
	Profiles []DirectEventAuthProfileSpec `json:"profiles,omitempty" yaml:"profiles"`
}

type DirectEventAuthProfileSpec struct {
	Name            string `json:"name,omitempty" yaml:"name"`
	Kind            string `json:"kind,omitempty" yaml:"kind"`
	CreateIfMissing bool   `json:"create_if_missing,omitempty" yaml:"create_if_missing"`
	ExistingRef     string `json:"existing_ref,omitempty" yaml:"existing_ref"`
	Token           string `json:"token,omitempty" yaml:"token"`
}

type DirectEventMappingSpec struct {
	Required []string                               `json:"required,omitempty" yaml:"required"`
	Fields   map[string]DirectEventMappingFieldSpec `json:"fields,omitempty" yaml:"fields"`
	Defaults map[string]any                         `json:"defaults,omitempty" yaml:"defaults"`
}

type DirectEventMappingFieldSpec struct {
	Source  string `json:"source,omitempty" yaml:"source"`
	Expr    string `json:"expr,omitempty" yaml:"expr"`
	Literal any    `json:"literal,omitempty" yaml:"literal"`
}

type DirectEventIdempotencySpec struct {
	Strategy string `json:"strategy,omitempty" yaml:"strategy"`
	Path     string `json:"path,omitempty" yaml:"path"`
	Header   string `json:"header,omitempty" yaml:"header"`
}

type DirectEventTimeoutSpec struct {
	Seconds int `json:"seconds,omitempty" yaml:"seconds"`
}

type DirectEventRetrySpec struct {
	MaxAttempts int `json:"max_attempts,omitempty" yaml:"max_attempts"`
}

type DirectEventStorageSpec struct {
	PayloadLimitBytes int  `json:"payload_limit_bytes,omitempty" yaml:"payload_limit_bytes"`
	StoreRawBody      bool `json:"store_raw_body,omitempty" yaml:"store_raw_body"`
}

type DirectEventExamplesSpec struct {
	Payload             string `json:"payload,omitempty" yaml:"payload"`
	ExpectedMappedInput string `json:"expected_mapped_input,omitempty" yaml:"expected_mapped_input"`
}

type ConnectorFacetItem struct {
	Key              string                       `json:"key"`
	Folder           string                       `json:"folder"`
	ManifestPath     string                       `json:"manifest_path"`
	ManifestHash     string                       `json:"manifest_hash"`
	ProviderKey      string                       `json:"provider_key"`
	ProviderAddress  string                       `json:"provider_address"`
	DisplayName      string                       `json:"display_name"`
	Description      string                       `json:"description,omitempty"`
	Version          string                       `json:"version"`
	ProviderStatus   string                       `json:"provider_status"`
	ContractStatus   string                       `json:"contract_status"`
	RuntimeKind      string                       `json:"runtime_kind"`
	RuntimeBaseDir   string                       `json:"runtime_base_dir,omitempty"`
	Capabilities     []ConnectorCapabilityItem    `json:"capabilities,omitempty"`
	UsageDocuments   []ConnectorUsageDocumentItem `json:"usage_documents,omitempty"`
	ActivationStatus string                       `json:"activation_status"`
	Metadata         map[string]any               `json:"metadata,omitempty"`
}

type ConnectorCapabilityItem struct {
	Endpoint                    string           `json:"endpoint"`
	CapabilityAddress           string           `json:"capability_address"`
	DisplayName                 string           `json:"display_name"`
	Description                 string           `json:"description,omitempty"`
	ClassNamespace              string           `json:"class_namespace"`
	ClassName                   string           `json:"class_name"`
	Form                        string           `json:"form"`
	RiskLevel                   string           `json:"risk_level"`
	SideEffects                 []string         `json:"side_effects,omitempty"`
	ExecutionAuthorizationLevel int              `json:"execution_authorization_level"`
	RequiresApproval            bool             `json:"requires_approval"`
	InputSchema                 map[string]any   `json:"input_schema"`
	OutputSchema                map[string]any   `json:"output_schema"`
	PolicyRequirements          map[string]any   `json:"policy_requirements,omitempty"`
	CredentialRequirements      map[string]any   `json:"credential_requirements,omitempty"`
	RuntimeKind                 string           `json:"runtime_kind"`
	RuntimeScript               string           `json:"runtime_script,omitempty"`
	ScriptManifestPath          string           `json:"script_manifest_path,omitempty"`
	ScriptManifest              scripts.Manifest `json:"script_manifest,omitempty"`
	ScriptManifestHash          string           `json:"script_manifest_hash,omitempty"`
	ScriptPackageHash           string           `json:"script_package_hash,omitempty"`
	RuntimeConfigJSON           json.RawMessage  `json:"runtime_config_json,omitempty"`
	ImplementationHash          string           `json:"implementation_hash,omitempty"`
	ContractStatus              string           `json:"contract_status"`
	ExecutionMode               string           `json:"execution_mode,omitempty"`
	WaitTimeoutSeconds          int              `json:"wait_timeout_seconds,omitempty"`
}

type ConnectorUsageDocumentItem struct {
	Path        string `json:"path"`
	Target      string `json:"target"`
	Endpoint    string `json:"endpoint,omitempty"`
	Title       string `json:"title,omitempty"`
	ContentHash string `json:"content_hash,omitempty"`
}

type ModuleFacetItem struct {
	Key                   string                    `json:"key"`
	Folder                string                    `json:"folder"`
	ManifestPath          string                    `json:"manifest_path"`
	ProjectContractPath   string                    `json:"project_contract_path,omitempty"`
	ManifestHash          string                    `json:"manifest_hash"`
	ProjectContractHash   string                    `json:"project_contract_hash,omitempty"`
	PackageHash           string                    `json:"package_hash"`
	PackageSizeBytes      int64                     `json:"package_size_bytes"`
	ModuleID              string                    `json:"module_id"`
	ModuleName            string                    `json:"module_name"`
	Version               string                    `json:"version"`
	ModuleKind            string                    `json:"module_kind"`
	Description           string                    `json:"description,omitempty"`
	SourceRef             string                    `json:"source_ref,omitempty"`
	Manifest              modules.Manifest          `json:"manifest"`
	ContractStatus        string                    `json:"contract_status"`
	RegistrationEnabled   bool                      `json:"registration_enabled"`
	OwnedByProject        bool                      `json:"owned_by_project"`
	ExposeInProjectPortal bool                      `json:"expose_in_project_portal"`
	InstallPlan           ModuleProjectInstallPlan  `json:"install_plan"`
	ExposurePlan          ModuleProjectExposurePlan `json:"exposure_plan"`
	Validation            ModuleProjectValidation   `json:"validation"`
	RequirementCount      int                       `json:"requirement_count"`
	ObjectTypeCount       int                       `json:"object_type_count"`
	ProviderCount         int                       `json:"provider_count"`
	CapabilityCount       int                       `json:"capability_count"`
	UsageDocumentCount    int                       `json:"usage_document_count"`
	BackupHookCount       int                       `json:"backup_hook_count"`
	ActivationStatus      string                    `json:"activation_status"`
	Metadata              map[string]any            `json:"metadata,omitempty"`
}

type ModuleProjectContract struct {
	Kind          string                    `json:"kind" yaml:"kind"`
	SchemaVersion string                    `json:"schema_version" yaml:"schema_version"`
	Module        ModuleProjectModuleSpec   `json:"module" yaml:"module"`
	Project       ModuleProjectProjectSpec  `json:"project,omitempty" yaml:"project"`
	Registration  ModuleProjectRegistration `json:"registration,omitempty" yaml:"registration"`
	Install       ModuleProjectInstallPlan  `json:"install,omitempty" yaml:"install"`
	Exposure      ModuleProjectExposurePlan `json:"exposure,omitempty" yaml:"exposure"`
	Validation    ModuleProjectValidation   `json:"validation,omitempty" yaml:"validation"`
	Metadata      map[string]any            `json:"metadata,omitempty" yaml:"metadata"`
}

type ModuleProjectModuleSpec struct {
	Manifest string `json:"manifest,omitempty" yaml:"manifest"`
	Package  string `json:"package,omitempty" yaml:"package"`
	Status   string `json:"status,omitempty" yaml:"status"`
}

type ModuleProjectProjectSpec struct {
	OwnedByProject        *bool `json:"owned_by_project,omitempty" yaml:"owned_by_project"`
	ExposeInProjectPortal *bool `json:"expose_in_project_portal,omitempty" yaml:"expose_in_project_portal"`
}

type ModuleProjectRegistration struct {
	Register     *bool `json:"register,omitempty" yaml:"register"`
	AutoRegister *bool `json:"auto_register,omitempty" yaml:"auto_register"`
	AutoInstall  *bool `json:"auto_install,omitempty" yaml:"auto_install"`
	AutoExpose   *bool `json:"auto_expose,omitempty" yaml:"auto_expose"`
}

type ModuleProjectInstallPlan struct {
	Plan                 string `json:"plan,omitempty" yaml:"plan"`
	TargetNode           string `json:"target_node,omitempty" yaml:"target_node"`
	Scope                string `json:"scope,omitempty" yaml:"scope"`
	InstallAfterRegister bool   `json:"install_after_register,omitempty" yaml:"install_after_register"`
	EnableAfterInstall   bool   `json:"enable_after_install,omitempty" yaml:"enable_after_install"`
}

type ModuleProjectExposurePlan struct {
	Plan              string   `json:"plan,omitempty" yaml:"plan"`
	ExposeAfterEnable bool     `json:"expose_after_enable,omitempty" yaml:"expose_after_enable"`
	Capabilities      []string `json:"capabilities,omitempty" yaml:"capabilities"`
}

type ModuleProjectValidation struct {
	RequireUsageDocsForCapabilities      bool `json:"require_usage_docs,omitempty" yaml:"require_usage_docs"`
	RequireBackupHooksForStatefulStorage bool `json:"require_backup_hooks_for_stateful_storage,omitempty" yaml:"require_backup_hooks_for_stateful_storage"`
}

type ConnectorContract struct {
	Kind           string                       `json:"kind" yaml:"kind"`
	SchemaVersion  string                       `json:"schema_version" yaml:"schema_version"`
	Provider       ConnectorProviderSpec        `json:"provider" yaml:"provider"`
	Runtime        ConnectorRuntimeSpec         `json:"runtime,omitempty" yaml:"runtime"`
	Capabilities   []ConnectorCapabilitySpec    `json:"capabilities,omitempty" yaml:"capabilities"`
	UsageDocuments []ConnectorUsageDocumentSpec `json:"usage_documents,omitempty" yaml:"usage_documents"`
	Metadata       map[string]any               `json:"metadata,omitempty" yaml:"metadata"`
}

type ConnectorProviderSpec struct {
	Key         string `json:"key" yaml:"key"`
	DisplayName string `json:"display_name,omitempty" yaml:"display_name"`
	Description string `json:"description,omitempty" yaml:"description"`
	Type        string `json:"type,omitempty" yaml:"type"`
	Version     string `json:"version,omitempty" yaml:"version"`
	Status      string `json:"status,omitempty" yaml:"status"`
}

type ConnectorRuntimeSpec struct {
	Kind    string         `json:"kind,omitempty" yaml:"kind"`
	BaseDir string         `json:"base_dir,omitempty" yaml:"base_dir"`
	Config  map[string]any `json:"config,omitempty" yaml:"config"`
}

type ConnectorCapabilitySpec struct {
	Endpoint                    string                         `json:"endpoint" yaml:"endpoint"`
	DisplayName                 string                         `json:"display_name,omitempty" yaml:"display_name"`
	Description                 string                         `json:"description,omitempty" yaml:"description"`
	ClassNamespace              string                         `json:"class_namespace,omitempty" yaml:"class_namespace"`
	ClassName                   string                         `json:"class_name,omitempty" yaml:"class_name"`
	Form                        string                         `json:"form,omitempty" yaml:"form"`
	RiskLevel                   string                         `json:"risk_level,omitempty" yaml:"risk_level"`
	SideEffects                 []string                       `json:"side_effects,omitempty" yaml:"side_effects"`
	ExecutionAuthorizationLevel int                            `json:"execution_authorization_level,omitempty" yaml:"execution_authorization_level"`
	RequiresApproval            bool                           `json:"requires_approval,omitempty" yaml:"requires_approval"`
	InputSchema                 map[string]any                 `json:"input_schema,omitempty" yaml:"input_schema"`
	OutputSchema                map[string]any                 `json:"output_schema,omitempty" yaml:"output_schema"`
	PolicyRequirements          map[string]any                 `json:"policy_requirements,omitempty" yaml:"policy_requirements"`
	CredentialRequirements      map[string]any                 `json:"credential_requirements,omitempty" yaml:"credential_requirements"`
	Runtime                     ConnectorCapabilityRuntimeSpec `json:"runtime,omitempty" yaml:"runtime"`
	Implementation              ConnectorImplementationSpec    `json:"implementation,omitempty" yaml:"implementation"`
	Status                      string                         `json:"status,omitempty" yaml:"status"`
}

type ConnectorCapabilityRuntimeSpec struct {
	Kind               string         `json:"kind,omitempty" yaml:"kind"`
	Script             string         `json:"script,omitempty" yaml:"script"`
	DefaultMode        string         `json:"default_mode,omitempty" yaml:"default_mode"`
	WaitTimeoutSeconds int            `json:"wait_timeout_seconds,omitempty" yaml:"wait_timeout_seconds"`
	Config             map[string]any `json:"config,omitempty" yaml:"config"`
}

type ConnectorImplementationSpec struct {
	Command []string `json:"command,omitempty" yaml:"command"`
}

type ConnectorUsageDocumentSpec struct {
	Path     string `json:"path" yaml:"path"`
	Target   string `json:"target,omitempty" yaml:"target"`
	Endpoint string `json:"endpoint,omitempty" yaml:"endpoint"`
	Title    string `json:"title,omitempty" yaml:"title"`
}

type NotesContract struct {
	Kind          string                `json:"kind" yaml:"kind"`
	SchemaVersion string                `json:"schema_version" yaml:"schema_version"`
	Notes         NotesPolicySpec       `json:"notes" yaml:"notes"`
	Material      []ProjectMaterialSpec `json:"material,omitempty" yaml:"material,omitempty"`
	Metadata      map[string]any        `json:"metadata,omitempty" yaml:"metadata"`
}

// Material declarations are opt-in narrative roots, separate from legacy notes.
type ProjectMaterialSpec struct {
	Key      string   `json:"key" yaml:"key"`
	Category string   `json:"category" yaml:"category"`
	Path     string   `json:"path" yaml:"path"`
	Enabled  bool     `json:"enabled" yaml:"enabled"`
	Include  []string `json:"include,omitempty" yaml:"include,omitempty"`
	Exclude  []string `json:"exclude,omitempty" yaml:"exclude,omitempty"`
}

type NotesPolicySpec struct {
	Status  string   `json:"status,omitempty" yaml:"status"`
	Sync    *bool    `json:"sync,omitempty" yaml:"sync"`
	Index   *bool    `json:"index,omitempty" yaml:"index"`
	Backup  *bool    `json:"backup,omitempty" yaml:"backup"`
	RootKey string   `json:"root_key,omitempty" yaml:"root_key"`
	Path    string   `json:"path,omitempty" yaml:"path"`
	Include []string `json:"include,omitempty" yaml:"include"`
	Exclude []string `json:"exclude,omitempty" yaml:"exclude"`
}

type NotesFacetItem struct {
	MaterialCategory string         `json:"material_category,omitempty"`
	RootKey          string         `json:"root_key"`
	Path             string         `json:"path"`
	ProjectPath      string         `json:"project_path"`
	ContractPath     string         `json:"contract_path,omitempty"`
	ContractHash     string         `json:"contract_hash,omitempty"`
	Status           string         `json:"status"`
	Sync             bool           `json:"sync"`
	Index            bool           `json:"index"`
	Backup           bool           `json:"backup"`
	Include          []string       `json:"include,omitempty"`
	Exclude          []string       `json:"exclude,omitempty"`
	ActivationStatus string         `json:"activation_status"`
	Metadata         map[string]any `json:"metadata,omitempty"`
}

type ReposContract struct {
	Kind          string          `json:"kind" yaml:"kind"`
	SchemaVersion string          `json:"schema_version" yaml:"schema_version"`
	Repos         ReposPolicySpec `json:"repos" yaml:"repos"`
	Metadata      map[string]any  `json:"metadata,omitempty" yaml:"metadata"`
}

type ReposPolicySpec struct {
	Status     string               `json:"status,omitempty" yaml:"status"`
	Defaults   RepoRootPolicySpec   `json:"defaults,omitempty" yaml:"defaults"`
	Roots      []RepoRootPolicySpec `json:"roots,omitempty" yaml:"roots,omitempty"`
	WatchRoots []RepoRootPolicySpec `json:"watch_roots,omitempty" yaml:"watch_roots,omitempty"`
	Members    []RepoMemberSpec     `json:"members,omitempty" yaml:"members,omitempty"`
}

type RepoRootPolicySpec struct {
	Key         string   `json:"key,omitempty" yaml:"key"`
	Path        string   `json:"path,omitempty" yaml:"path"`
	DisplayName string   `json:"display_name,omitempty" yaml:"display_name"`
	Sync        *bool    `json:"sync,omitempty" yaml:"sync"`
	Backup      *bool    `json:"backup,omitempty" yaml:"backup"`
	Index       *bool    `json:"index,omitempty" yaml:"index"`
	Include     []string `json:"include,omitempty" yaml:"include"`
	Exclude     []string `json:"exclude,omitempty" yaml:"exclude"`
}

type RepoMemberSpec struct {
	ID        string `json:"id,omitempty" yaml:"id,omitempty"`
	Key       string `json:"key" yaml:"key"`
	Path      string `json:"path" yaml:"path"`
	Role      string `json:"role" yaml:"role"`
	StateRoot string `json:"state_root,omitempty" yaml:"state_root,omitempty"`
}

type RepoFacetItem struct {
	Key              string         `json:"key"`
	Path             string         `json:"path"`
	ProjectPath      string         `json:"project_path"`
	DisplayName      string         `json:"display_name,omitempty"`
	ContractPath     string         `json:"contract_path,omitempty"`
	ContractHash     string         `json:"contract_hash,omitempty"`
	Status           string         `json:"status"`
	Sync             bool           `json:"sync"`
	Index            bool           `json:"index"`
	Backup           bool           `json:"backup"`
	Include          []string       `json:"include,omitempty"`
	Exclude          []string       `json:"exclude,omitempty"`
	ActivationStatus string         `json:"activation_status"`
	Metadata         map[string]any `json:"metadata,omitempty"`
}

type ProjectSyncPolicyContract struct {
	Kind          string         `json:"kind" yaml:"kind"`
	SchemaVersion string         `json:"schema_version" yaml:"schema_version"`
	Sync          SyncPolicySpec `json:"sync" yaml:"sync"`
	Metadata      map[string]any `json:"metadata,omitempty" yaml:"metadata"`
}

type SyncPolicySpec struct {
	Enabled  *bool                `json:"enabled,omitempty" yaml:"enabled"`
	Defaults SyncRootPolicySpec   `json:"defaults,omitempty" yaml:"defaults"`
	Roots    []SyncRootPolicySpec `json:"roots,omitempty" yaml:"roots"`
}

type SyncRootPolicySpec struct {
	Key                 string          `json:"key,omitempty" yaml:"key"`
	Path                string          `json:"path,omitempty" yaml:"path"`
	DisplayName         string          `json:"display_name,omitempty" yaml:"display_name"`
	SafeRoot            string          `json:"safe_root,omitempty" yaml:"safe_root"`
	Mode                PolicyModeValue `json:"mode,omitempty" yaml:"mode"`
	Index               PolicyModeValue `json:"index,omitempty" yaml:"index"`
	Delete              string          `json:"delete,omitempty" yaml:"delete"`
	ProjectRef          string          `json:"project_ref,omitempty" yaml:"project_ref"`
	ScopeRef            string          `json:"scope_ref,omitempty" yaml:"scope_ref"`
	MaxFileBytes        int64           `json:"max_file_bytes,omitempty" yaml:"max_file_bytes"`
	MaxTextBytes        int64           `json:"max_text_bytes,omitempty" yaml:"max_text_bytes"`
	LogicalNameStrategy string          `json:"logical_name_strategy,omitempty" yaml:"logical_name_strategy"`
	Include             []string        `json:"include,omitempty" yaml:"include"`
	Exclude             []string        `json:"exclude,omitempty" yaml:"exclude"`
}

type ProjectBackupPolicyContract struct {
	Kind          string           `json:"kind" yaml:"kind"`
	SchemaVersion string           `json:"schema_version" yaml:"schema_version"`
	Backup        BackupPolicySpec `json:"backup" yaml:"backup"`
	Metadata      map[string]any   `json:"metadata,omitempty" yaml:"metadata"`
}

type BackupPolicySpec struct {
	Enabled  *bool                  `json:"enabled,omitempty" yaml:"enabled"`
	Defaults BackupRootPolicySpec   `json:"defaults,omitempty" yaml:"defaults"`
	Roots    []BackupRootPolicySpec `json:"roots,omitempty" yaml:"roots"`
}

type BackupRootPolicySpec struct {
	Key                    string   `json:"key,omitempty" yaml:"key"`
	Path                   string   `json:"path,omitempty" yaml:"path"`
	DisplayName            string   `json:"display_name,omitempty" yaml:"display_name"`
	SafeRoot               string   `json:"safe_root,omitempty" yaml:"safe_root"`
	Mode                   string   `json:"mode,omitempty" yaml:"mode"`
	MaxFileBytes           int64    `json:"max_file_bytes,omitempty" yaml:"max_file_bytes"`
	MaxBatchBytes          int64    `json:"max_batch_bytes,omitempty" yaml:"max_batch_bytes"`
	MaxPendingItems        int      `json:"max_pending_items,omitempty" yaml:"max_pending_items"`
	MaxPendingBytes        int64    `json:"max_pending_bytes,omitempty" yaml:"max_pending_bytes"`
	IncludeDeletionMarkers *bool    `json:"include_deletion_markers,omitempty" yaml:"include_deletion_markers"`
	OnLimit                string   `json:"on_limit,omitempty" yaml:"on_limit"`
	Include                []string `json:"include,omitempty" yaml:"include"`
	Exclude                []string `json:"exclude,omitempty" yaml:"exclude"`
}

type PolicyModeValue string

type ProjectWatchedRootItem struct {
	Key              string                      `json:"key"`
	BackendRootKey   string                      `json:"backend_root_key"`
	WorkerKey        string                      `json:"worker_key"`
	SourceKinds      []string                    `json:"source_kinds"`
	OwnerNode        string                      `json:"owner_node"`
	SafeRootKey      string                      `json:"safe_root_key"`
	RootRelativePath string                      `json:"root_relative_path"`
	DisplayName      string                      `json:"display_name"`
	Include          []string                    `json:"include,omitempty"`
	Exclude          []string                    `json:"exclude,omitempty"`
	SyncMode         string                      `json:"sync_mode"`
	BackupMode       string                      `json:"backup_mode"`
	IndexMode        string                      `json:"index_mode"`
	DeleteMode       string                      `json:"delete_mode"`
	ConfigHash       string                      `json:"config_hash"`
	ConfigJSON       json.RawMessage             `json:"config_json"`
	AgentCommands    []ProjectWatchedRootCommand `json:"agent_commands,omitempty"`
	ActivationStatus string                      `json:"activation_status"`
	Metadata         map[string]any              `json:"metadata,omitempty"`
}

type ProjectWatchedRootCommand struct {
	Description string   `json:"description"`
	Command     []string `json:"command"`
	Shell       string   `json:"shell"`
}

type PlanUnsupportedFeature struct {
	Feature string `json:"feature"`
	Reason  string `json:"reason"`
}

type PlanAction struct {
	Action      string `json:"action"`
	Status      string `json:"status"`
	TargetKind  string `json:"target_kind"`
	TargetRef   string `json:"target_ref"`
	Description string `json:"description"`
}

type Analysis struct {
	Loaded *LoadedProject   `json:"loaded,omitempty"`
	Report ValidationReport `json:"report"`
	Plan   ProjectPlan      `json:"plan"`
}

type BackendAnalysisInput struct {
	ProjectRef  string `json:"project_ref,omitempty"`
	ProjectRoot string `json:"project_root,omitempty"`
}
