package projects

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
)

const (
	ProjectRegistrationStatusRegistered = "registered"
	ProjectRegistrationStatusBlocked    = "blocked"
	ProjectRegistrationStatusStale      = "stale"
	ProjectRegistrationStatusArchived   = "archived"

	ProjectActivationStatusInactive     = "inactive"
	ProjectActivationStatusBaseActive   = "base_active"
	ProjectActivationStatusFacetPending = "facet_activation_pending"
	ProjectActivationStatusBlocked      = "blocked"

	ProjectFacetStatusDeclared          = "declared"
	ProjectFacetStatusDisabled          = "disabled"
	ProjectFacetStatusMissing           = "missing"
	ProjectFacetStatusPlaceholder       = "placeholder"
	ProjectFacetStatusPendingLaterSlice = "pending_later_slice"
	ProjectFacetStatusUnsupported       = "unsupported"
	ProjectFacetStatusActivated         = "activated"

	ProjectScriptExposureStatusRegistered = "registered"
	ProjectScriptExposureStatusActive     = "active"
	ProjectScriptExposureStatusDisabled   = "disabled"
	ProjectScriptExposureStatusBlocked    = "blocked"
	ProjectScriptExposureStatusStale      = "stale"

	ProjectScheduleRegistrationStatusRegistered = "registered"
	ProjectScheduleRegistrationStatusPaused     = "paused"
	ProjectScheduleRegistrationStatusActive     = "active"
	ProjectScheduleRegistrationStatusDisabled   = "disabled"
	ProjectScheduleRegistrationStatusBlocked    = "blocked"
	ProjectScheduleRegistrationStatusStale      = "stale"

	ProjectDirectEventRegistrationStatusRegistered = "registered"
	ProjectDirectEventRegistrationStatusPaused     = "paused"
	ProjectDirectEventRegistrationStatusActive     = "active"
	ProjectDirectEventRegistrationStatusDisabled   = "disabled"
	ProjectDirectEventRegistrationStatusBlocked    = "blocked"
	ProjectDirectEventRegistrationStatusStale      = "stale"

	ProjectWatchedRootRegistrationStatusRegistered        = "registered"
	ProjectWatchedRootRegistrationStatusPendingAgentApply = "pending_agent_apply"
	ProjectWatchedRootRegistrationStatusApplied           = "applied"
	ProjectWatchedRootRegistrationStatusReported          = "reported"
	ProjectWatchedRootRegistrationStatusBlocked           = "blocked"
	ProjectWatchedRootRegistrationStatusDisabled          = "disabled"
	ProjectWatchedRootRegistrationStatusStale             = "stale"

	ProjectConnectorRegistrationStatusRegistered = "registered"
	ProjectConnectorRegistrationStatusActive     = "active"
	ProjectConnectorRegistrationStatusDisabled   = "disabled"
	ProjectConnectorRegistrationStatusBlocked    = "blocked"
	ProjectConnectorRegistrationStatusStale      = "stale"

	ProjectModuleRegistrationStatusRegistered = "registered"
	ProjectModuleRegistrationStatusDisabled   = "disabled"
	ProjectModuleRegistrationStatusBlocked    = "blocked"
	ProjectModuleRegistrationStatusStale      = "stale"

	ProjectWorkflowRegistrationStatusRegistered = "registered"
	ProjectWorkflowRegistrationStatusActive     = "active"
	ProjectWorkflowRegistrationStatusDisabled   = "disabled"
	ProjectWorkflowRegistrationStatusBlocked    = "blocked"
	ProjectWorkflowRegistrationStatusStale      = "stale"
)

var (
	contractHashPattern                    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	ErrFacetActivationUnsupported          = errors.New("project facet activation is implemented in later v0.3 slices")
	ErrProjectRepositoryOwnershipConflict  = errors.New("project repository ownership conflict")
	ErrProjectRepositoryPersistenceCorrupt = errors.New("project repository persistence is inconsistent")
)

const (
	projectRepositoryValidationReportSchemaV03 = "project.validation_report.v0.3"
	projectRepositoryRegistrationPlanSchemaV03 = "project.plan.v0.3"
)

type RegisterProjectContractInput struct {
	ProjectRoot           string                                `json:"project_root"`
	ContractPath          string                                `json:"contract_path"`
	ContractHash          string                                `json:"contract_hash"`
	ContractSchemaVersion string                                `json:"contract_schema_version"`
	Contract              json.RawMessage                       `json:"contract"`
	ValidationReport      json.RawMessage                       `json:"validation_report"`
	RegistrationPlan      json.RawMessage                       `json:"registration_plan"`
	Project               ProjectContractProjectInput           `json:"project"`
	RepositorySource      *RegisterProjectRepositorySourceInput `json:"repository_source,omitempty"`
	DerivedProviders      json.RawMessage                       `json:"derived_providers,omitempty"`
	Facets                []ProjectContractFacetInput           `json:"facets"`
	PolicyRefs            json.RawMessage                       `json:"policy_refs,omitempty"`
	Metadata              json.RawMessage                       `json:"metadata,omitempty"`
}

type ProjectContractProjectInput struct {
	ID          string `json:"id,omitempty"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	OwnerNode   string `json:"owner_node"`
	Status      string `json:"status,omitempty"`
}

// RegisterProjectRepositorySourceInput contains the repository-contract source
// coordinates produced by the accepted validator. The complete member set is
// deliberately not accepted here: it is reconstructed from, and checked
// against, ValidationReport and RegistrationPlan.
type RegisterProjectRepositorySourceInput struct {
	ContractPath          string `json:"contract_path"`
	ContractHash          string `json:"contract_hash"`
	ContractSchemaVersion string `json:"contract_schema_version"`
}

type ProjectRepositoryRegistrationResult struct {
	Classification ProjectRepositorySourceClassification `json:"classification"`
	SourceRevision int64                                 `json:"source_revision"`
	MemberCount    int                                   `json:"member_count"`
}

type ProjectContractFacetInput struct {
	Key         string `json:"key"`
	Folder      string `json:"folder,omitempty"`
	Enabled     bool   `json:"enabled"`
	Present     bool   `json:"present"`
	Placeholder bool   `json:"placeholder,omitempty"`
}

type ProjectContractRegistration struct {
	ProjectContractRegistrationID string          `json:"project_contract_registration_id"`
	ProjectID                     string          `json:"project_id"`
	ProjectRoot                   string          `json:"project_root"`
	ContractPath                  string          `json:"contract_path"`
	ContractHash                  string          `json:"contract_hash"`
	ContractSchemaVersion         string          `json:"contract_schema_version"`
	Contract                      json.RawMessage `json:"contract"`
	ValidationReport              json.RawMessage `json:"validation_report"`
	RegistrationPlan              json.RawMessage `json:"registration_plan"`
	DerivedProviders              json.RawMessage `json:"derived_providers"`
	PolicyRefs                    json.RawMessage `json:"policy_refs"`
	RegistrationStatus            string          `json:"registration_status"`
	ActivationStatus              string          `json:"activation_status"`
	RegistrationRevision          int             `json:"registration_revision"`
	LastRegisteredByActorID       string          `json:"last_registered_by_actor_id"`
	LastRegisteredAt              time.Time       `json:"last_registered_at"`
	BaseActivatedByActorID        *string         `json:"base_activated_by_actor_id,omitempty"`
	BaseActivatedAt               *time.Time      `json:"base_activated_at,omitempty"`
	Metadata                      json.RawMessage `json:"metadata"`
	CreatedAt                     time.Time       `json:"created_at"`
	UpdatedAt                     time.Time       `json:"updated_at"`
}

type ProjectContractFacet struct {
	ProjectContractFacetID        string          `json:"project_contract_facet_id"`
	ProjectContractRegistrationID string          `json:"project_contract_registration_id"`
	ProjectID                     string          `json:"project_id"`
	FacetKey                      string          `json:"facet_key"`
	Folder                        string          `json:"folder"`
	Enabled                       bool            `json:"enabled"`
	Present                       bool            `json:"present"`
	Placeholder                   bool            `json:"placeholder"`
	FacetStatus                   string          `json:"facet_status"`
	Metadata                      json.RawMessage `json:"metadata"`
	CreatedAt                     time.Time       `json:"created_at"`
	UpdatedAt                     time.Time       `json:"updated_at"`
}

type ProjectRegistrationDetail struct {
	Project                  ProjectDetail                    `json:"project"`
	Registration             *ProjectContractRegistration     `json:"registration,omitempty"`
	Facets                   []ProjectContractFacet           `json:"facets,omitempty"`
	ScriptExposures          []ProjectScriptExposure          `json:"script_exposures,omitempty"`
	ScheduleRegistrations    []ProjectScheduleRegistration    `json:"schedule_registrations,omitempty"`
	DirectEventRegistrations []ProjectDirectEventRegistration `json:"direct_event_registrations,omitempty"`
	WatchedRootRegistrations []ProjectWatchedRootRegistration `json:"watched_root_registrations,omitempty"`
	ConnectorRegistrations   []ProjectConnectorRegistration   `json:"connector_registrations,omitempty"`
	ModuleRegistrations      []ProjectModuleRegistration      `json:"module_registrations,omitempty"`
	WorkflowRegistrations    []ProjectWorkflowRegistration    `json:"workflow_registrations,omitempty"`
}

type RegisterProjectContractResult struct {
	Detail           ProjectRegistrationDetail            `json:"detail"`
	Created          bool                                 `json:"created"`
	Updated          bool                                 `json:"updated"`
	Unchanged        bool                                 `json:"unchanged"`
	ChangedKeys      []string                             `json:"changed_keys,omitempty"`
	EventIDs         []string                             `json:"event_ids,omitempty"`
	RepositorySource *ProjectRepositoryRegistrationResult `json:"repository_source,omitempty"`
}

type RegisterProjectContractFromBackendInput struct {
	ProjectRef  string `json:"project_ref,omitempty"`
	ProjectRoot string `json:"project_root,omitempty"`
	Strict      bool   `json:"strict,omitempty"`
}

type ActivateProjectInput struct {
	Facet       string `json:"facet,omitempty"`
	ProjectRoot string `json:"project_root,omitempty"`
}

type DeactivateProjectInput struct {
	Facet       string `json:"facet,omitempty"`
	ProjectRoot string `json:"project_root,omitempty"`
	Reason      string `json:"reason,omitempty"`
	DryRun      bool   `json:"dry_run,omitempty"`
}

type ProjectDeactivationAction struct {
	Key      string          `json:"key"`
	Kind     string          `json:"kind"`
	Ref      string          `json:"ref,omitempty"`
	Status   string          `json:"status"`
	Summary  string          `json:"summary"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

type ProjectDeactivationResult struct {
	Detail   ProjectRegistrationDetail   `json:"detail"`
	Facet    string                      `json:"facet,omitempty"`
	DryRun   bool                        `json:"dry_run,omitempty"`
	Changed  bool                        `json:"changed"`
	Actions  []ProjectDeactivationAction `json:"actions,omitempty"`
	Warnings []string                    `json:"warnings,omitempty"`
}

type ProjectScriptExposure struct {
	ProjectScriptExposureID       string          `json:"project_script_exposure_id"`
	ProjectContractRegistrationID string          `json:"project_contract_registration_id"`
	ProjectID                     string          `json:"project_id"`
	ScriptKey                     string          `json:"script_key"`
	ScriptFolder                  string          `json:"script_folder"`
	ScriptManifestPath            string          `json:"script_manifest_path"`
	ExposurePath                  string          `json:"exposure_path,omitempty"`
	ExposureHash                  string          `json:"exposure_hash,omitempty"`
	ExposureEnabled               bool            `json:"exposure_enabled"`
	ProviderID                    *string         `json:"provider_id,omitempty"`
	ProviderAddress               string          `json:"provider_address,omitempty"`
	ScriptID                      *string         `json:"script_id,omitempty"`
	ScriptVersionID               *string         `json:"script_version_id,omitempty"`
	CapabilityEndpointID          *string         `json:"capability_endpoint_id,omitempty"`
	CapabilityEndpointVersionID   *string         `json:"capability_endpoint_version_id,omitempty"`
	RuntimeBindingID              *string         `json:"runtime_binding_id,omitempty"`
	CapabilityAddress             string          `json:"capability_address,omitempty"`
	ActivationStatus              string          `json:"activation_status"`
	LastActivatedByActorID        string          `json:"last_activated_by_actor_id"`
	LastActivatedAt               time.Time       `json:"last_activated_at"`
	Metadata                      json.RawMessage `json:"metadata"`
	CreatedAt                     time.Time       `json:"created_at"`
	UpdatedAt                     time.Time       `json:"updated_at"`
}

type UpsertProjectScriptExposureInput struct {
	ProjectContractRegistrationID string
	ProjectID                     string
	ScriptKey                     string
	ScriptFolder                  string
	ScriptManifestPath            string
	ExposurePath                  string
	ExposureHash                  string
	ExposureEnabled               bool
	ProviderID                    string
	ProviderAddress               string
	ScriptID                      string
	ScriptVersionID               string
	CapabilityEndpointID          string
	CapabilityEndpointVersionID   string
	RuntimeBindingID              string
	CapabilityAddress             string
	ActivationStatus              string
	Metadata                      json.RawMessage
}

type ProjectScheduleRegistration struct {
	ProjectScheduleRegistrationID string          `json:"project_schedule_registration_id"`
	ProjectContractRegistrationID string          `json:"project_contract_registration_id"`
	ProjectID                     string          `json:"project_id"`
	ScheduleKey                   string          `json:"schedule_key"`
	BackendScheduleKey            string          `json:"backend_schedule_key"`
	ScheduleFolder                string          `json:"schedule_folder"`
	ScheduleManifestPath          string          `json:"schedule_manifest_path"`
	ScheduleHash                  string          `json:"schedule_hash"`
	InputPath                     string          `json:"input_path,omitempty"`
	InputHash                     string          `json:"input_hash,omitempty"`
	TargetCapability              string          `json:"target_capability,omitempty"`
	AutomationID                  *string         `json:"automation_id,omitempty"`
	ScheduleID                    *string         `json:"schedule_id,omitempty"`
	ActivationStatus              string          `json:"activation_status"`
	LastActivatedByActorID        string          `json:"last_activated_by_actor_id"`
	LastActivatedAt               time.Time       `json:"last_activated_at"`
	Metadata                      json.RawMessage `json:"metadata"`
	CreatedAt                     time.Time       `json:"created_at"`
	UpdatedAt                     time.Time       `json:"updated_at"`
}

type UpsertProjectScheduleRegistrationInput struct {
	ProjectContractRegistrationID string
	ProjectID                     string
	ScheduleKey                   string
	BackendScheduleKey            string
	ScheduleFolder                string
	ScheduleManifestPath          string
	ScheduleHash                  string
	InputPath                     string
	InputHash                     string
	TargetCapability              string
	AutomationID                  string
	ScheduleID                    string
	ActivationStatus              string
	Metadata                      json.RawMessage
}

type ProjectDirectEventRegistration struct {
	ProjectDirectEventRegistrationID string          `json:"project_direct_event_registration_id"`
	ProjectContractRegistrationID    string          `json:"project_contract_registration_id"`
	ProjectID                        string          `json:"project_id"`
	EventKey                         string          `json:"event_key"`
	IntegrationKey                   string          `json:"integration_key"`
	BackendIntegrationKey            string          `json:"backend_integration_key"`
	EndpointSlug                     string          `json:"endpoint_slug"`
	BackendEndpointSlug              string          `json:"backend_endpoint_slug"`
	EndpointPath                     string          `json:"endpoint_path"`
	EventFolder                      string          `json:"event_folder"`
	EventManifestPath                string          `json:"event_manifest_path"`
	EventHash                        string          `json:"event_hash"`
	PayloadExamplePath               string          `json:"payload_example_path,omitempty"`
	PayloadExampleHash               string          `json:"payload_example_hash,omitempty"`
	ExpectedInputPath                string          `json:"expected_input_path,omitempty"`
	ExpectedInputHash                string          `json:"expected_input_hash,omitempty"`
	EventType                        string          `json:"event_type"`
	TargetCapability                 string          `json:"target_capability,omitempty"`
	ResponseMode                     string          `json:"response_mode,omitempty"`
	IntegrationID                    *string         `json:"integration_id,omitempty"`
	AuthProfileID                    *string         `json:"auth_profile_id,omitempty"`
	EndpointID                       *string         `json:"endpoint_id,omitempty"`
	AutomationID                     *string         `json:"automation_id,omitempty"`
	ActivationStatus                 string          `json:"activation_status"`
	LastActivatedByActorID           string          `json:"last_activated_by_actor_id"`
	LastActivatedAt                  time.Time       `json:"last_activated_at"`
	Metadata                         json.RawMessage `json:"metadata"`
	CreatedAt                        time.Time       `json:"created_at"`
	UpdatedAt                        time.Time       `json:"updated_at"`
}

type UpsertProjectDirectEventRegistrationInput struct {
	ProjectContractRegistrationID string
	ProjectID                     string
	EventKey                      string
	IntegrationKey                string
	BackendIntegrationKey         string
	EndpointSlug                  string
	BackendEndpointSlug           string
	EndpointPath                  string
	EventFolder                   string
	EventManifestPath             string
	EventHash                     string
	PayloadExamplePath            string
	PayloadExampleHash            string
	ExpectedInputPath             string
	ExpectedInputHash             string
	EventType                     string
	TargetCapability              string
	ResponseMode                  string
	IntegrationID                 string
	AuthProfileID                 string
	EndpointID                    string
	AutomationID                  string
	ActivationStatus              string
	Metadata                      json.RawMessage
}

type ProjectWatchedRootRegistration struct {
	ProjectWatchedRootRegistrationID string          `json:"project_watched_root_registration_id"`
	ProjectContractRegistrationID    string          `json:"project_contract_registration_id"`
	ProjectID                        string          `json:"project_id"`
	NodeID                           string          `json:"node_id"`
	OwnerNodeKey                     string          `json:"owner_node_key"`
	LocalRootKey                     string          `json:"local_root_key"`
	BackendRootKey                   string          `json:"backend_root_key"`
	WorkerKey                        string          `json:"worker_key"`
	SourceKinds                      json.RawMessage `json:"source_kinds"`
	SafeRootKey                      string          `json:"safe_root_key"`
	RootRelativePath                 string          `json:"root_relative_path"`
	DisplayName                      string          `json:"display_name"`
	SyncMode                         string          `json:"sync_mode"`
	BackupMode                       string          `json:"backup_mode"`
	IndexMode                        string          `json:"index_mode"`
	DeleteMode                       string          `json:"delete_mode"`
	ConfigHash                       string          `json:"config_hash"`
	ConfigJSON                       json.RawMessage `json:"config_json"`
	CommandJSON                      json.RawMessage `json:"command_json"`
	WatchedRootID                    *string         `json:"watched_root_id,omitempty"`
	ActivationStatus                 string          `json:"activation_status"`
	LastAppliedByActorID             *string         `json:"last_applied_by_actor_id,omitempty"`
	LastAppliedAt                    *time.Time      `json:"last_applied_at,omitempty"`
	LastReportedAt                   *time.Time      `json:"last_reported_at,omitempty"`
	Metadata                         json.RawMessage `json:"metadata"`
	CreatedAt                        time.Time       `json:"created_at"`
	UpdatedAt                        time.Time       `json:"updated_at"`
}

type ProjectWatchedRootCommand struct {
	Description string   `json:"description"`
	Command     []string `json:"command"`
	Shell       string   `json:"shell"`
}

type UpsertProjectWatchedRootRegistrationInput struct {
	ProjectContractRegistrationID string
	ProjectID                     string
	NodeID                        string
	OwnerNodeKey                  string
	LocalRootKey                  string
	BackendRootKey                string
	WorkerKey                     string
	SourceKinds                   json.RawMessage
	SafeRootKey                   string
	RootRelativePath              string
	DisplayName                   string
	SyncMode                      string
	BackupMode                    string
	IndexMode                     string
	DeleteMode                    string
	ConfigHash                    string
	ConfigJSON                    json.RawMessage
	CommandJSON                   json.RawMessage
	ActivationStatus              string
	Metadata                      json.RawMessage
}

type ProjectConnectorRegistration struct {
	ProjectConnectorRegistrationID string          `json:"project_connector_registration_id"`
	ProjectContractRegistrationID  string          `json:"project_contract_registration_id"`
	ProjectID                      string          `json:"project_id"`
	ConnectorKey                   string          `json:"connector_key"`
	ConnectorFolder                string          `json:"connector_folder"`
	ConnectorManifestPath          string          `json:"connector_manifest_path"`
	ConnectorHash                  string          `json:"connector_hash"`
	ProviderKey                    string          `json:"provider_key"`
	ProviderAddress                string          `json:"provider_address"`
	ProviderID                     *string         `json:"provider_id,omitempty"`
	ProviderStatus                 string          `json:"provider_status"`
	RuntimeKind                    string          `json:"runtime_kind"`
	CapabilityCount                int             `json:"capability_count"`
	ActiveCapabilityCount          int             `json:"active_capability_count"`
	UsageDocumentCount             int             `json:"usage_document_count"`
	ActivationStatus               string          `json:"activation_status"`
	LastActivatedByActorID         string          `json:"last_activated_by_actor_id"`
	LastActivatedAt                time.Time       `json:"last_activated_at"`
	Metadata                       json.RawMessage `json:"metadata"`
	CreatedAt                      time.Time       `json:"created_at"`
	UpdatedAt                      time.Time       `json:"updated_at"`
}

type UpsertProjectConnectorRegistrationInput struct {
	ProjectContractRegistrationID string
	ProjectID                     string
	ConnectorKey                  string
	ConnectorFolder               string
	ConnectorManifestPath         string
	ConnectorHash                 string
	ProviderKey                   string
	ProviderAddress               string
	ProviderID                    string
	ProviderStatus                string
	RuntimeKind                   string
	CapabilityCount               int
	ActiveCapabilityCount         int
	UsageDocumentCount            int
	ActivationStatus              string
	Metadata                      json.RawMessage
}

type ProjectModuleRegistration struct {
	ProjectModuleRegistrationID   string          `json:"project_module_registration_id"`
	ProjectContractRegistrationID string          `json:"project_contract_registration_id"`
	ProjectID                     string          `json:"project_id"`
	ModuleKey                     string          `json:"module_key"`
	ModuleFolder                  string          `json:"module_folder"`
	ModuleManifestPath            string          `json:"module_manifest_path"`
	ModuleProjectContractPath     string          `json:"module_project_contract_path,omitempty"`
	ModuleManifestHash            string          `json:"module_manifest_hash"`
	ModuleProjectContractHash     string          `json:"module_project_contract_hash,omitempty"`
	ModulePackageHash             string          `json:"module_package_hash,omitempty"`
	ModuleID                      string          `json:"module_id"`
	ModuleName                    string          `json:"module_name"`
	ModuleVersion                 string          `json:"module_version"`
	ModuleKind                    string          `json:"module_kind"`
	ModulePackageID               *string         `json:"module_package_id,omitempty"`
	ModuleVersionID               *string         `json:"module_version_id,omitempty"`
	RequirementCount              int             `json:"requirement_count"`
	ObjectTypeCount               int             `json:"object_type_count"`
	ProviderCount                 int             `json:"provider_count"`
	CapabilityCount               int             `json:"capability_count"`
	UsageDocumentCount            int             `json:"usage_document_count"`
	BackupHookCount               int             `json:"backup_hook_count"`
	RegistrationEnabled           bool            `json:"registration_enabled"`
	InstallPlanJSON               json.RawMessage `json:"install_plan_json"`
	ExposurePlanJSON              json.RawMessage `json:"exposure_plan_json"`
	ActivationStatus              string          `json:"activation_status"`
	LastActivatedByActorID        string          `json:"last_activated_by_actor_id"`
	LastActivatedAt               time.Time       `json:"last_activated_at"`
	Metadata                      json.RawMessage `json:"metadata"`
	CreatedAt                     time.Time       `json:"created_at"`
	UpdatedAt                     time.Time       `json:"updated_at"`
}

type UpsertProjectModuleRegistrationInput struct {
	ProjectContractRegistrationID string
	ProjectID                     string
	ModuleKey                     string
	ModuleFolder                  string
	ModuleManifestPath            string
	ModuleProjectContractPath     string
	ModuleManifestHash            string
	ModuleProjectContractHash     string
	ModulePackageHash             string
	ModuleID                      string
	ModuleName                    string
	ModuleVersion                 string
	ModuleKind                    string
	ModulePackageID               string
	ModuleVersionID               string
	RequirementCount              int
	ObjectTypeCount               int
	ProviderCount                 int
	CapabilityCount               int
	UsageDocumentCount            int
	BackupHookCount               int
	RegistrationEnabled           bool
	InstallPlanJSON               json.RawMessage
	ExposurePlanJSON              json.RawMessage
	ActivationStatus              string
	Metadata                      json.RawMessage
}

type ProjectWorkflowRegistration struct {
	ProjectWorkflowRegistrationID string          `json:"project_workflow_registration_id"`
	ProjectContractRegistrationID string          `json:"project_contract_registration_id"`
	ProjectID                     string          `json:"project_id"`
	WorkflowKey                   string          `json:"workflow_key"`
	WorkflowFolder                string          `json:"workflow_folder"`
	WorkflowManifestPath          string          `json:"workflow_manifest_path"`
	WorkflowManifestHash          string          `json:"workflow_manifest_hash"`
	ImplementationKind            string          `json:"implementation_kind"`
	RuntimeKind                   string          `json:"runtime_kind"`
	WorkflowID                    *string         `json:"workflow_id,omitempty"`
	WorkflowVersionID             *string         `json:"workflow_version_id,omitempty"`
	ProviderID                    *string         `json:"provider_id,omitempty"`
	ProviderAddress               string          `json:"provider_address,omitempty"`
	CapabilityEndpointID          *string         `json:"capability_endpoint_id,omitempty"`
	CapabilityEndpointVersionID   *string         `json:"capability_endpoint_version_id,omitempty"`
	RuntimeBindingID              *string         `json:"runtime_binding_id,omitempty"`
	CapabilityAddress             string          `json:"capability_address,omitempty"`
	ActivationStatus              string          `json:"activation_status"`
	LastActivatedByActorID        string          `json:"last_activated_by_actor_id"`
	LastActivatedAt               time.Time       `json:"last_activated_at"`
	Metadata                      json.RawMessage `json:"metadata"`
	CreatedAt                     time.Time       `json:"created_at"`
	UpdatedAt                     time.Time       `json:"updated_at"`
}

type UpsertProjectWorkflowRegistrationInput struct {
	ProjectContractRegistrationID string
	ProjectID                     string
	WorkflowKey                   string
	WorkflowFolder                string
	WorkflowManifestPath          string
	WorkflowManifestHash          string
	ImplementationKind            string
	RuntimeKind                   string
	WorkflowID                    string
	WorkflowVersionID             string
	ProviderID                    string
	ProviderAddress               string
	CapabilityEndpointID          string
	CapabilityEndpointVersionID   string
	RuntimeBindingID              string
	CapabilityAddress             string
	ActivationStatus              string
	Metadata                      json.RawMessage
}

type ApplyProjectWatchPolicyInput struct {
	ProjectRoot           string `json:"project_root,omitempty"`
	DryRun                bool   `json:"dry_run,omitempty"`
	UseRegisteredSnapshot bool   `json:"use_registered_snapshot,omitempty"`
}

type ApplyProjectWatchPolicyResult struct {
	Detail       ProjectRegistrationDetail        `json:"detail"`
	WatchedRoots []ProjectWatchedRootRegistration `json:"watched_roots"`
	Commands     []ProjectWatchedRootCommand      `json:"commands"`
	DryRun       bool                             `json:"dry_run,omitempty"`
}

func (s Service) RegisterProjectContract(ctx context.Context, req requestctx.Context, input RegisterProjectContractInput) (RegisterProjectContractResult, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return RegisterProjectContractResult{}, err
	}
	defer tx.Rollback()
	result, err := registerProjectContractTx(ctx, tx, req, input)
	if err != nil {
		return RegisterProjectContractResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return RegisterProjectContractResult{}, err
	}
	result.Detail, err = s.GetProjectRegistrationStatus(ctx, result.Detail.Project.Project.ProjectID)
	if err != nil {
		return RegisterProjectContractResult{}, err
	}
	return result, nil
}

// registerProjectContractTx is private to the projects owner. Declaration token
// receipts can commit with the existing whole source/member/event transaction;
// callers outside this domain never receive a raw transaction hook.
func registerProjectContractTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, input RegisterProjectContractInput) (RegisterProjectContractResult, error) {
	input, err := normalizeRegisterProjectContractInput(input)
	if err != nil {
		return RegisterProjectContractResult{}, err
	}
	prepared := preparedProjectRegistration{input: input}
	if input.RepositorySource != nil {
		projectID := input.Project.ID
		if projectID == "" {
			projectID = "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"
		}
		source, err := buildProjectRepositoryValidatedSourceInput(input, projectID)
		if err != nil {
			return RegisterProjectContractResult{}, err
		}
		prepared.source = &source
	}
	return registerPreparedProjectContractTx(ctx, tx, req, prepared)
}

// Constructed only by guarded ordinary preparation or by the journal owner
// after its transaction has authenticated and compared the exact predecessor.
type preparedProjectRegistration struct {
	input  RegisterProjectContractInput
	source *ProjectRepositoryValidatedSourceInput
}

func registerPreparedProjectContractTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, prepared preparedProjectRegistration) (RegisterProjectContractResult, error) {
	input := prepared.input
	initialMetadata, err := projectRegistrationMetadata(nil, input)
	if err != nil {
		return RegisterProjectContractResult{}, err
	}
	createInput, err := normalizeCreateInput(CreateInput{
		Name:        input.Project.Name,
		Slug:        input.Project.Slug,
		Description: input.Project.Description,
		HomeNodeRef: input.Project.OwnerNode,
		IfNotExists: true,
		Metadata:    initialMetadata,
	})
	if err != nil {
		return RegisterProjectContractResult{}, err
	}

	if err := lockProjectRepositoryRegistrationProjectTx(ctx, tx, input); err != nil {
		return RegisterProjectContractResult{}, err
	}

	existingProject, err := getProjectByIDOrSlugTx(ctx, tx, input.Project.ID, input.Project.Slug, true)
	projectExists := err == nil
	if err != nil && err != sql.ErrNoRows {
		return RegisterProjectContractResult{}, err
	}
	if projectExists {
		if input.Project.ID != "" && existingProject.ProjectID != input.Project.ID {
			return RegisterProjectContractResult{}, fmt.Errorf("project slug %q belongs to project %s, not %s", input.Project.Slug, existingProject.ProjectID, input.Project.ID)
		}
		if existingProject.Slug != input.Project.Slug {
			return RegisterProjectContractResult{}, fmt.Errorf("project id %s belongs to slug %q, not %q", existingProject.ProjectID, existingProject.Slug, input.Project.Slug)
		}
		if err := EnsureProjectMutable(existingProject, "project_registration", existingProject.ProjectID); err != nil {
			return RegisterProjectContractResult{}, err
		}
	}
	projectID := input.Project.ID
	if projectExists {
		projectID = existingProject.ProjectID
	} else if projectID == "" {
		projectID = ids.NewProjectID()
	}

	existingRegistration := ProjectContractRegistration{}
	registrationExists := false
	existingFacets := []ProjectContractFacet{}
	if projectExists {
		existingRegistration, err = getContractRegistrationByProjectTx(ctx, tx, projectID)
		registrationExists = err == nil
		if err != nil && err != sql.ErrNoRows {
			return RegisterProjectContractResult{}, err
		}
		if registrationExists && existingRegistration.ContractSchemaVersion == ProjectRepositoryProjectSchemaV05 && input.ContractSchemaVersion != ProjectRepositoryProjectSchemaV05 {
			return RegisterProjectContractResult{}, fmt.Errorf("declaration registration cannot downgrade its project source")
		}
		existingFacets, err = listContractFacetsByProjectTx(ctx, tx, projectID)
		if err != nil {
			return RegisterProjectContractResult{}, err
		}
	}

	var repositoryPlan *projectRepositoryRegistrationPlan
	if input.RepositorySource != nil {
		if prepared.source == nil {
			return RegisterProjectContractResult{}, fmt.Errorf("prepared repository source required")
		}
		source := *prepared.source
		source.ProjectID = projectID
		repositoryPlan, err = prepareProjectRepositoryRegistrationSourceTx(ctx, tx, input, projectID, existingRegistration, registrationExists, source)
		if err != nil {
			return RegisterProjectContractResult{}, err
		}
	} else if projectExists {
		if _, sourceErr := getProjectRepositorySourceTx(ctx, tx, projectID); sourceErr == nil {
			return RegisterProjectContractResult{}, fmt.Errorf("repository source metadata is required for project %s", projectID)
		} else if sourceErr != sql.ErrNoRows {
			return RegisterProjectContractResult{}, sourceErr
		}
	}

	createResult, err := createProjectTx(ctx, tx, req, createInput, createProjectTxOptions{
		ProjectID:   projectID,
		EventSource: "project.register",
	})
	if err != nil {
		return RegisterProjectContractResult{}, err
	}
	project := createResult.Project.Project
	homeNodeID, err := resolveNodeRefTx(ctx, tx, input.Project.OwnerNode)
	if err != nil {
		return RegisterProjectContractResult{}, fmt.Errorf("resolve owner node: %w", err)
	}

	changedKeys := changedProjectKeys(project, homeNodeID, input)
	if registrationExists && existingRegistration.ContractHash != input.ContractHash {
		changedKeys = append(changedKeys, "contract_hash")
	}
	if registrationExists && existingRegistration.ContractSchemaVersion != input.ContractSchemaVersion {
		changedKeys = append(changedKeys, "contract_schema_version")
	}
	if registrationExists && existingRegistration.ProjectRoot != input.ProjectRoot {
		changedKeys = append(changedKeys, "project_root")
	}
	if registrationExists && existingRegistration.ContractPath != input.ContractPath {
		changedKeys = append(changedKeys, "contract_path")
	}
	if registrationExists && !jsonRawEqual(existingRegistration.PolicyRefs, input.PolicyRefs) {
		changedKeys = append(changedKeys, "policy_refs")
	}
	if registrationExists && !jsonRawEqual(existingRegistration.DerivedProviders, input.DerivedProviders) {
		changedKeys = append(changedKeys, "derived_providers")
	}
	if registrationExists && !jsonRawEqualIgnoringKeys(existingRegistration.RegistrationPlan, input.RegistrationPlan, "generated_at") {
		changedKeys = append(changedKeys, "registration_plan")
	}
	if registrationExists && facetDeclarationInputSignature(input.Facets) != facetDeclarationRecordSignature(existingFacets) {
		changedKeys = append(changedKeys, "facets")
	}
	if repositoryPlan != nil && repositoryPlan.Change.Classification != ProjectRepositorySourceClassificationIdenticalReplay {
		changedKeys = append(changedKeys, "repository_source")
	}
	changedKeys = uniqueStrings(changedKeys)
	if repositoryPlan != nil && projectExists && registrationExists && repositoryPlan.Change.Classification == ProjectRepositorySourceClassificationIdenticalReplay && len(changedKeys) == 0 {
		detail := ProjectRegistrationDetail{Project: ProjectDetail{Project: existingProject}, Registration: &existingRegistration}
		return RegisterProjectContractResult{
			Detail:    detail,
			Unchanged: true,
			RepositorySource: &ProjectRepositoryRegistrationResult{
				Classification: repositoryPlan.Change.Classification,
				SourceRevision: repositoryPlan.SourceRevision,
				MemberCount:    len(repositoryPlan.Next.Snapshot.Members),
			},
		}, nil
	}

	projectMetadata, err := projectRegistrationMetadata(project.Metadata, input)
	if err != nil {
		return RegisterProjectContractResult{}, err
	}
	projectStatus := projectStatusFromContract(input.Project.Status)
	if err := updateProjectFromContractTx(ctx, tx, project, homeNodeID, projectStatus, projectMetadata, input); err != nil {
		return RegisterProjectContractResult{}, err
	}

	registrationID := ids.NewProjectContractRegistrationID()
	revision := 1
	activationStatus := ProjectActivationStatusInactive
	if registrationExists {
		registrationID = existingRegistration.ProjectContractRegistrationID
		revision = existingRegistration.RegistrationRevision
		activationStatus = existingRegistration.ActivationStatus
		if len(changedKeys) > 0 {
			revision++
		}
	}

	metadata, err := normalizeJSON(input.Metadata)
	if err != nil {
		return RegisterProjectContractResult{}, fmt.Errorf("registration metadata must be a valid JSON object: %w", err)
	}

	registration, err := upsertContractRegistrationTx(ctx, tx, upsertContractRegistrationInput{
		RegistrationID:        registrationID,
		ProjectID:             project.ProjectID,
		Input:                 input,
		RegistrationStatus:    ProjectRegistrationStatusRegistered,
		ActivationStatus:      activationStatus,
		RegistrationRevision:  revision,
		LastRegisteredActorID: req.ActorID,
		Metadata:              metadata,
	})
	if err != nil {
		return RegisterProjectContractResult{}, err
	}
	if err := replaceContractFacetsTx(ctx, tx, registration, input.Facets, existingFacets); err != nil {
		return RegisterProjectContractResult{}, err
	}
	if repositoryPlan != nil && repositoryPlan.Change.Classification != ProjectRepositorySourceClassificationIdenticalReplay {
		if err := persistProjectRepositoryRegistrationTx(ctx, tx, req.ActorID, registration.ProjectContractRegistrationID, *repositoryPlan); err != nil {
			return RegisterProjectContractResult{}, err
		}
	}

	eventIDs := []string{}
	eventType := events.TypeProjectContractRegistered
	status := "registered"
	if registrationExists && len(changedKeys) > 0 {
		eventType = events.TypeProjectContractUpdated
		status = "updated"
	}
	if !registrationExists || len(changedKeys) > 0 {
		event, err := events.AppendTx(ctx, tx, events.AppendInput{
			EventType:  eventType,
			EventLevel: "audit",
			Request:    req,
			ScopeID:    project.ProjectScopeID,
			TargetKind: "project_contract_registration",
			TargetID:   registration.ProjectContractRegistrationID,
			Status:     status,
			Result:     "ok",
			Payload: map[string]any{
				"project_id":                       project.ProjectID,
				"project_slug":                     project.Slug,
				"project_contract_registration_id": registration.ProjectContractRegistrationID,
				"contract_hash":                    input.ContractHash,
				"registration_revision":            revision,
				"changed_keys":                     changedKeys,
				"source":                           "project.register",
			},
			VisibilityClass: "internal",
		})
		if err != nil {
			return RegisterProjectContractResult{}, err
		}
		eventIDs = append(eventIDs, event.EventID)
	}

	detail := ProjectRegistrationDetail{Project: ProjectDetail{Project: project}, Registration: &registration}
	created := createResult.Created || !registrationExists
	updated := registrationExists && len(changedKeys) > 0
	result := RegisterProjectContractResult{
		Detail:      detail,
		Created:     created,
		Updated:     updated,
		Unchanged:   !created && !updated,
		ChangedKeys: changedKeys,
		EventIDs:    eventIDs,
	}
	if repositoryPlan != nil {
		result.RepositorySource = &ProjectRepositoryRegistrationResult{
			Classification: repositoryPlan.Change.Classification,
			SourceRevision: repositoryPlan.SourceRevision,
			MemberCount:    len(repositoryPlan.Next.Snapshot.Members),
		}
	}
	return result, nil
}

func (s Service) GetProjectRegistrationStatus(ctx context.Context, ref string) (ProjectRegistrationDetail, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ProjectRegistrationDetail{}, fmt.Errorf("project ref is required")
	}

	if strings.HasPrefix(ref, ids.ProjectContractRegistrationPrefix+"_") {
		registration, err := s.getContractRegistration(ctx, ref)
		if err != nil {
			return ProjectRegistrationDetail{}, err
		}
		projectDetail, err := s.GetProject(ctx, registration.ProjectID)
		if err != nil {
			return ProjectRegistrationDetail{}, err
		}
		facets, err := s.listContractFacets(ctx, registration.ProjectID)
		if err != nil {
			return ProjectRegistrationDetail{}, err
		}
		scriptExposures, err := s.ListProjectScriptExposures(ctx, registration.ProjectID)
		if err != nil {
			return ProjectRegistrationDetail{}, err
		}
		scheduleRegistrations, err := s.ListProjectScheduleRegistrations(ctx, registration.ProjectID)
		if err != nil {
			return ProjectRegistrationDetail{}, err
		}
		directEventRegistrations, err := s.ListProjectDirectEventRegistrations(ctx, registration.ProjectID)
		if err != nil {
			return ProjectRegistrationDetail{}, err
		}
		watchedRootRegistrations, err := s.ListProjectWatchedRootRegistrations(ctx, registration.ProjectID)
		if err != nil {
			return ProjectRegistrationDetail{}, err
		}
		connectorRegistrations, err := s.ListProjectConnectorRegistrations(ctx, registration.ProjectID)
		if err != nil {
			return ProjectRegistrationDetail{}, err
		}
		moduleRegistrations, err := s.ListProjectModuleRegistrations(ctx, registration.ProjectID)
		if err != nil {
			return ProjectRegistrationDetail{}, err
		}
		workflowRegistrations, err := s.ListProjectWorkflowRegistrations(ctx, registration.ProjectID)
		if err != nil {
			return ProjectRegistrationDetail{}, err
		}
		return ProjectRegistrationDetail{Project: projectDetail, Registration: &registration, Facets: facets, ScriptExposures: scriptExposures, ScheduleRegistrations: scheduleRegistrations, DirectEventRegistrations: directEventRegistrations, WatchedRootRegistrations: watchedRootRegistrations, ConnectorRegistrations: connectorRegistrations, ModuleRegistrations: moduleRegistrations, WorkflowRegistrations: workflowRegistrations}, nil
	}

	projectDetail, err := s.GetProject(ctx, ref)
	if err != nil {
		return ProjectRegistrationDetail{}, err
	}
	registration, err := s.getContractRegistrationByProject(ctx, projectDetail.Project.ProjectID)
	if err == sql.ErrNoRows {
		return ProjectRegistrationDetail{Project: projectDetail, Facets: []ProjectContractFacet{}}, nil
	}
	if err != nil {
		return ProjectRegistrationDetail{}, err
	}
	facets, err := s.listContractFacets(ctx, projectDetail.Project.ProjectID)
	if err != nil {
		return ProjectRegistrationDetail{}, err
	}
	scriptExposures, err := s.ListProjectScriptExposures(ctx, projectDetail.Project.ProjectID)
	if err != nil {
		return ProjectRegistrationDetail{}, err
	}
	scheduleRegistrations, err := s.ListProjectScheduleRegistrations(ctx, projectDetail.Project.ProjectID)
	if err != nil {
		return ProjectRegistrationDetail{}, err
	}
	directEventRegistrations, err := s.ListProjectDirectEventRegistrations(ctx, projectDetail.Project.ProjectID)
	if err != nil {
		return ProjectRegistrationDetail{}, err
	}
	watchedRootRegistrations, err := s.ListProjectWatchedRootRegistrations(ctx, projectDetail.Project.ProjectID)
	if err != nil {
		return ProjectRegistrationDetail{}, err
	}
	connectorRegistrations, err := s.ListProjectConnectorRegistrations(ctx, projectDetail.Project.ProjectID)
	if err != nil {
		return ProjectRegistrationDetail{}, err
	}
	moduleRegistrations, err := s.ListProjectModuleRegistrations(ctx, projectDetail.Project.ProjectID)
	if err != nil {
		return ProjectRegistrationDetail{}, err
	}
	workflowRegistrations, err := s.ListProjectWorkflowRegistrations(ctx, projectDetail.Project.ProjectID)
	if err != nil {
		return ProjectRegistrationDetail{}, err
	}
	return ProjectRegistrationDetail{Project: projectDetail, Registration: &registration, Facets: facets, ScriptExposures: scriptExposures, ScheduleRegistrations: scheduleRegistrations, DirectEventRegistrations: directEventRegistrations, WatchedRootRegistrations: watchedRootRegistrations, ConnectorRegistrations: connectorRegistrations, ModuleRegistrations: moduleRegistrations, WorkflowRegistrations: workflowRegistrations}, nil
}

func (s Service) ActivateProjectBase(ctx context.Context, req requestctx.Context, ref string) (ProjectRegistrationDetail, error) {
	detail, err := s.GetProjectRegistrationStatus(ctx, ref)
	if err != nil {
		return ProjectRegistrationDetail{}, err
	}
	resourceRef := strings.TrimSpace(ref)
	if resourceRef == "" {
		resourceRef = "base"
	}
	if err := EnsureProjectRegistrationMutable(detail, "project_activation", resourceRef); err != nil {
		return ProjectRegistrationDetail{}, err
	}
	if detail.Registration == nil {
		return ProjectRegistrationDetail{}, fmt.Errorf("project has no registered project contract")
	}
	if detail.Registration.ActivationStatus == ProjectActivationStatusBaseActive {
		return detail, nil
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ProjectRegistrationDetail{}, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		UPDATE projects.project_contract_registrations
		SET activation_status = $2,
		    base_activated_by_actor_id = $3,
		    base_activated_at = now(),
		    updated_at = now()
		WHERE project_contract_registration_id = $1
	`, detail.Registration.ProjectContractRegistrationID, ProjectActivationStatusBaseActive, req.ActorID); err != nil {
		return ProjectRegistrationDetail{}, err
	}

	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  events.TypeProjectBaseActivated,
		EventLevel: "audit",
		Request:    req,
		ScopeID:    detail.Project.Project.ProjectScopeID,
		TargetKind: "project_contract_registration",
		TargetID:   detail.Registration.ProjectContractRegistrationID,
		Status:     ProjectActivationStatusBaseActive,
		Result:     "ok",
		Payload: map[string]any{
			"project_id":                       detail.Project.Project.ProjectID,
			"project_slug":                     detail.Project.Project.Slug,
			"project_contract_registration_id": detail.Registration.ProjectContractRegistrationID,
			"source":                           "project.activate",
			"activated_facets":                 []string{},
		},
		VisibilityClass: "internal",
	}); err != nil {
		return ProjectRegistrationDetail{}, err
	}

	if err := tx.Commit(); err != nil {
		return ProjectRegistrationDetail{}, err
	}
	return s.GetProjectRegistrationStatus(ctx, detail.Project.Project.ProjectID)
}

func (s Service) ListProjectScriptExposures(ctx context.Context, projectRef string) ([]ProjectScriptExposure, error) {
	projectID := strings.TrimSpace(projectRef)
	if projectID == "" {
		return nil, fmt.Errorf("project ref is required")
	}
	if !strings.HasPrefix(projectID, ids.ProjectPrefix+"_") {
		project, err := s.ResolveProjectRef(ctx, projectRef)
		if err != nil {
			return nil, err
		}
		projectID = project.ProjectID
	}
	rows, err := s.DB.QueryContext(ctx, projectScriptExposureSelectSQL()+`
		WHERE project_id = $1
		ORDER BY script_key
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	exposures := []ProjectScriptExposure{}
	for rows.Next() {
		exposure, err := scanProjectScriptExposure(rows)
		if err != nil {
			return nil, err
		}
		exposures = append(exposures, exposure)
	}
	return exposures, rows.Err()
}

func (s Service) UpsertProjectScriptExposure(ctx context.Context, req requestctx.Context, input UpsertProjectScriptExposureInput) (ProjectScriptExposure, error) {
	if strings.TrimSpace(input.ProjectContractRegistrationID) == "" || strings.TrimSpace(input.ProjectID) == "" {
		return ProjectScriptExposure{}, fmt.Errorf("project registration and project id are required")
	}
	if strings.TrimSpace(input.ScriptKey) == "" {
		return ProjectScriptExposure{}, fmt.Errorf("script key is required")
	}
	if strings.TrimSpace(input.ActivationStatus) == "" {
		input.ActivationStatus = ProjectScriptExposureStatusRegistered
	}
	if !validProjectScriptExposureStatus(input.ActivationStatus) {
		return ProjectScriptExposure{}, fmt.Errorf("unsupported project script exposure status: %s", input.ActivationStatus)
	}
	metadata, err := normalizeJSON(input.Metadata)
	if err != nil {
		return ProjectScriptExposure{}, fmt.Errorf("script exposure metadata must be a JSON object: %w", err)
	}
	row := s.DB.QueryRowContext(ctx, `
		INSERT INTO projects.project_script_exposures (
			project_script_exposure_id, project_contract_registration_id, project_id,
			script_key, script_folder, script_manifest_path, exposure_path,
			exposure_hash, exposure_enabled, provider_id, provider_address,
			script_id, script_version_id, capability_endpoint_id,
			capability_endpoint_version_id, runtime_binding_id,
			capability_address, activation_status, last_activated_by_actor_id,
			metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, nullif($10, ''), $11,
		        nullif($12, ''), nullif($13, ''), nullif($14, ''),
		        nullif($15, ''), nullif($16, ''), $17, $18, $19, $20)
		ON CONFLICT (project_id, script_key) DO UPDATE
		SET project_contract_registration_id = EXCLUDED.project_contract_registration_id,
		    script_folder = EXCLUDED.script_folder,
		    script_manifest_path = EXCLUDED.script_manifest_path,
		    exposure_path = EXCLUDED.exposure_path,
		    exposure_hash = EXCLUDED.exposure_hash,
		    exposure_enabled = EXCLUDED.exposure_enabled,
		    provider_id = EXCLUDED.provider_id,
		    provider_address = EXCLUDED.provider_address,
		    script_id = EXCLUDED.script_id,
		    script_version_id = EXCLUDED.script_version_id,
		    capability_endpoint_id = EXCLUDED.capability_endpoint_id,
		    capability_endpoint_version_id = EXCLUDED.capability_endpoint_version_id,
		    runtime_binding_id = EXCLUDED.runtime_binding_id,
		    capability_address = EXCLUDED.capability_address,
		    activation_status = EXCLUDED.activation_status,
		    last_activated_by_actor_id = EXCLUDED.last_activated_by_actor_id,
		    last_activated_at = now(),
		    metadata = EXCLUDED.metadata,
		    updated_at = now()
		RETURNING `+projectScriptExposureColumns(),
		ids.NewProjectScriptExposureID(),
		input.ProjectContractRegistrationID,
		input.ProjectID,
		input.ScriptKey,
		input.ScriptFolder,
		input.ScriptManifestPath,
		input.ExposurePath,
		input.ExposureHash,
		input.ExposureEnabled,
		input.ProviderID,
		input.ProviderAddress,
		input.ScriptID,
		input.ScriptVersionID,
		input.CapabilityEndpointID,
		input.CapabilityEndpointVersionID,
		input.RuntimeBindingID,
		input.CapabilityAddress,
		input.ActivationStatus,
		req.ActorID,
		metadata,
	)
	return scanProjectScriptExposure(row)
}

func (s Service) ListProjectScheduleRegistrations(ctx context.Context, projectRef string) ([]ProjectScheduleRegistration, error) {
	projectID := strings.TrimSpace(projectRef)
	if projectID == "" {
		return nil, fmt.Errorf("project ref is required")
	}
	if !strings.HasPrefix(projectID, ids.ProjectPrefix+"_") {
		project, err := s.ResolveProjectRef(ctx, projectRef)
		if err != nil {
			return nil, err
		}
		projectID = project.ProjectID
	}
	rows, err := s.DB.QueryContext(ctx, projectScheduleRegistrationSelectSQL()+`
		WHERE project_id = $1
		ORDER BY schedule_key
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	registrations := []ProjectScheduleRegistration{}
	for rows.Next() {
		registration, err := scanProjectScheduleRegistration(rows)
		if err != nil {
			return nil, err
		}
		registrations = append(registrations, registration)
	}
	return registrations, rows.Err()
}

func (s Service) UpsertProjectScheduleRegistration(ctx context.Context, req requestctx.Context, input UpsertProjectScheduleRegistrationInput) (ProjectScheduleRegistration, error) {
	if strings.TrimSpace(input.ProjectContractRegistrationID) == "" || strings.TrimSpace(input.ProjectID) == "" {
		return ProjectScheduleRegistration{}, fmt.Errorf("project registration and project id are required")
	}
	if strings.TrimSpace(input.ScheduleKey) == "" {
		return ProjectScheduleRegistration{}, fmt.Errorf("schedule key is required")
	}
	if strings.TrimSpace(input.BackendScheduleKey) == "" {
		return ProjectScheduleRegistration{}, fmt.Errorf("backend schedule key is required")
	}
	if strings.TrimSpace(input.ActivationStatus) == "" {
		input.ActivationStatus = ProjectScheduleRegistrationStatusRegistered
	}
	if !validProjectScheduleRegistrationStatus(input.ActivationStatus) {
		return ProjectScheduleRegistration{}, fmt.Errorf("unsupported project schedule registration status: %s", input.ActivationStatus)
	}
	metadata, err := normalizeJSON(input.Metadata)
	if err != nil {
		return ProjectScheduleRegistration{}, fmt.Errorf("schedule registration metadata must be a JSON object: %w", err)
	}
	row := s.DB.QueryRowContext(ctx, `
		INSERT INTO projects.project_schedule_registrations (
			project_schedule_registration_id, project_contract_registration_id,
			project_id, schedule_key, backend_schedule_key, schedule_folder,
			schedule_manifest_path, schedule_hash, input_path, input_hash,
			target_capability, automation_id, schedule_id, activation_status,
			last_activated_by_actor_id, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11,
		        nullif($12, ''), nullif($13, ''), $14, $15, $16)
		ON CONFLICT (project_id, schedule_key) DO UPDATE
		SET project_contract_registration_id = EXCLUDED.project_contract_registration_id,
		    backend_schedule_key = EXCLUDED.backend_schedule_key,
		    schedule_folder = EXCLUDED.schedule_folder,
		    schedule_manifest_path = EXCLUDED.schedule_manifest_path,
		    schedule_hash = EXCLUDED.schedule_hash,
		    input_path = EXCLUDED.input_path,
		    input_hash = EXCLUDED.input_hash,
		    target_capability = EXCLUDED.target_capability,
		    automation_id = EXCLUDED.automation_id,
		    schedule_id = EXCLUDED.schedule_id,
		    activation_status = EXCLUDED.activation_status,
		    last_activated_by_actor_id = EXCLUDED.last_activated_by_actor_id,
		    last_activated_at = now(),
		    metadata = EXCLUDED.metadata,
		    updated_at = now()
		RETURNING `+projectScheduleRegistrationColumns(),
		ids.NewProjectScheduleRegistrationID(),
		input.ProjectContractRegistrationID,
		input.ProjectID,
		input.ScheduleKey,
		input.BackendScheduleKey,
		input.ScheduleFolder,
		input.ScheduleManifestPath,
		input.ScheduleHash,
		input.InputPath,
		input.InputHash,
		input.TargetCapability,
		input.AutomationID,
		input.ScheduleID,
		input.ActivationStatus,
		req.ActorID,
		metadata,
	)
	return scanProjectScheduleRegistration(row)
}

func (s Service) ListProjectDirectEventRegistrations(ctx context.Context, projectRef string) ([]ProjectDirectEventRegistration, error) {
	projectID := strings.TrimSpace(projectRef)
	if projectID == "" {
		return nil, fmt.Errorf("project ref is required")
	}
	if !strings.HasPrefix(projectID, ids.ProjectPrefix+"_") {
		project, err := s.ResolveProjectRef(ctx, projectRef)
		if err != nil {
			return nil, err
		}
		projectID = project.ProjectID
	}
	rows, err := s.DB.QueryContext(ctx, projectDirectEventRegistrationSelectSQL()+`
		WHERE project_id = $1
		ORDER BY event_key
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	registrations := []ProjectDirectEventRegistration{}
	for rows.Next() {
		registration, err := scanProjectDirectEventRegistration(rows)
		if err != nil {
			return nil, err
		}
		registrations = append(registrations, registration)
	}
	return registrations, rows.Err()
}

func (s Service) UpsertProjectDirectEventRegistration(ctx context.Context, req requestctx.Context, input UpsertProjectDirectEventRegistrationInput) (ProjectDirectEventRegistration, error) {
	if strings.TrimSpace(input.ProjectContractRegistrationID) == "" || strings.TrimSpace(input.ProjectID) == "" {
		return ProjectDirectEventRegistration{}, fmt.Errorf("project registration and project id are required")
	}
	if strings.TrimSpace(input.EventKey) == "" {
		return ProjectDirectEventRegistration{}, fmt.Errorf("direct event key is required")
	}
	if strings.TrimSpace(input.BackendIntegrationKey) == "" {
		return ProjectDirectEventRegistration{}, fmt.Errorf("backend integration key is required")
	}
	if strings.TrimSpace(input.BackendEndpointSlug) == "" {
		return ProjectDirectEventRegistration{}, fmt.Errorf("backend endpoint slug is required")
	}
	if strings.TrimSpace(input.ActivationStatus) == "" {
		input.ActivationStatus = ProjectDirectEventRegistrationStatusRegistered
	}
	if !validProjectDirectEventRegistrationStatus(input.ActivationStatus) {
		return ProjectDirectEventRegistration{}, fmt.Errorf("unsupported project direct event registration status: %s", input.ActivationStatus)
	}
	metadata, err := normalizeJSON(input.Metadata)
	if err != nil {
		return ProjectDirectEventRegistration{}, fmt.Errorf("direct event registration metadata must be a JSON object: %w", err)
	}
	row := s.DB.QueryRowContext(ctx, `
		INSERT INTO projects.project_direct_event_registrations (
			project_direct_event_registration_id, project_contract_registration_id,
			project_id, event_key, integration_key, backend_integration_key,
			endpoint_slug, backend_endpoint_slug, endpoint_path, event_folder,
			event_manifest_path, event_hash, payload_example_path,
			payload_example_hash, expected_input_path, expected_input_hash,
			event_type, target_capability, response_mode, integration_id,
			auth_profile_id, endpoint_id, automation_id, activation_status,
			last_activated_by_actor_id, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
		        $11, $12, $13, $14, $15, $16, $17, $18, $19,
		        nullif($20, ''), nullif($21, ''), nullif($22, ''),
		        nullif($23, ''), $24, $25, $26)
		ON CONFLICT (project_id, event_key) DO UPDATE
		SET project_contract_registration_id = EXCLUDED.project_contract_registration_id,
		    integration_key = EXCLUDED.integration_key,
		    backend_integration_key = EXCLUDED.backend_integration_key,
		    endpoint_slug = EXCLUDED.endpoint_slug,
		    backend_endpoint_slug = EXCLUDED.backend_endpoint_slug,
		    endpoint_path = EXCLUDED.endpoint_path,
		    event_folder = EXCLUDED.event_folder,
		    event_manifest_path = EXCLUDED.event_manifest_path,
		    event_hash = EXCLUDED.event_hash,
		    payload_example_path = EXCLUDED.payload_example_path,
		    payload_example_hash = EXCLUDED.payload_example_hash,
		    expected_input_path = EXCLUDED.expected_input_path,
		    expected_input_hash = EXCLUDED.expected_input_hash,
		    event_type = EXCLUDED.event_type,
		    target_capability = EXCLUDED.target_capability,
		    response_mode = EXCLUDED.response_mode,
		    integration_id = EXCLUDED.integration_id,
		    auth_profile_id = EXCLUDED.auth_profile_id,
		    endpoint_id = EXCLUDED.endpoint_id,
		    automation_id = EXCLUDED.automation_id,
		    activation_status = EXCLUDED.activation_status,
		    last_activated_by_actor_id = EXCLUDED.last_activated_by_actor_id,
		    last_activated_at = now(),
		    metadata = EXCLUDED.metadata,
		    updated_at = now()
		RETURNING `+projectDirectEventRegistrationColumns(),
		ids.NewProjectDirectEventRegistrationID(),
		input.ProjectContractRegistrationID,
		input.ProjectID,
		input.EventKey,
		input.IntegrationKey,
		input.BackendIntegrationKey,
		input.EndpointSlug,
		input.BackendEndpointSlug,
		input.EndpointPath,
		input.EventFolder,
		input.EventManifestPath,
		input.EventHash,
		input.PayloadExamplePath,
		input.PayloadExampleHash,
		input.ExpectedInputPath,
		input.ExpectedInputHash,
		input.EventType,
		input.TargetCapability,
		input.ResponseMode,
		input.IntegrationID,
		input.AuthProfileID,
		input.EndpointID,
		input.AutomationID,
		input.ActivationStatus,
		req.ActorID,
		metadata,
	)
	return scanProjectDirectEventRegistration(row)
}

func (s Service) ListProjectWatchedRootRegistrations(ctx context.Context, projectRef string) ([]ProjectWatchedRootRegistration, error) {
	projectID := strings.TrimSpace(projectRef)
	if projectID == "" {
		return nil, fmt.Errorf("project ref is required")
	}
	if !strings.HasPrefix(projectID, ids.ProjectPrefix+"_") {
		project, err := s.ResolveProjectRef(ctx, projectRef)
		if err != nil {
			return nil, err
		}
		projectID = project.ProjectID
	}
	rows, err := s.DB.QueryContext(ctx, projectWatchedRootRegistrationSelectSQL()+`
		WHERE project_id = $1
		ORDER BY backend_root_key
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	registrations := []ProjectWatchedRootRegistration{}
	for rows.Next() {
		registration, err := scanProjectWatchedRootRegistration(rows)
		if err != nil {
			return nil, err
		}
		registrations = append(registrations, registration)
	}
	return registrations, rows.Err()
}

func (s Service) UpsertProjectWatchedRootRegistration(ctx context.Context, req requestctx.Context, input UpsertProjectWatchedRootRegistrationInput) (ProjectWatchedRootRegistration, error) {
	return upsertProjectWatchedRootRegistration(ctx, s.DB, req, input, true)
}

type projectWatchRowWriter interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func upsertProjectWatchedRootRegistration(ctx context.Context, writer projectWatchRowWriter, req requestctx.Context, input UpsertProjectWatchedRootRegistrationInput, applied bool) (ProjectWatchedRootRegistration, error) {
	if strings.TrimSpace(input.ProjectContractRegistrationID) == "" || strings.TrimSpace(input.ProjectID) == "" {
		return ProjectWatchedRootRegistration{}, fmt.Errorf("project registration and project id are required")
	}
	if strings.TrimSpace(input.NodeID) == "" {
		return ProjectWatchedRootRegistration{}, fmt.Errorf("node id is required")
	}
	if strings.TrimSpace(input.LocalRootKey) == "" {
		return ProjectWatchedRootRegistration{}, fmt.Errorf("local root key is required")
	}
	if strings.TrimSpace(input.BackendRootKey) == "" {
		return ProjectWatchedRootRegistration{}, fmt.Errorf("backend root key is required")
	}
	if strings.TrimSpace(input.ActivationStatus) == "" {
		input.ActivationStatus = ProjectWatchedRootRegistrationStatusRegistered
	}
	if !validProjectWatchedRootRegistrationStatus(input.ActivationStatus) {
		return ProjectWatchedRootRegistration{}, fmt.Errorf("unsupported project watched-root registration status: %s", input.ActivationStatus)
	}
	sourceKinds, err := normalizeJSONArray(input.SourceKinds)
	if err != nil {
		return ProjectWatchedRootRegistration{}, fmt.Errorf("watched-root source kinds must be a JSON array: %w", err)
	}
	config, err := normalizeJSON(input.ConfigJSON)
	if err != nil {
		return ProjectWatchedRootRegistration{}, fmt.Errorf("watched-root config must be a JSON object: %w", err)
	}
	commands, err := normalizeJSONArray(input.CommandJSON)
	if err != nil {
		return ProjectWatchedRootRegistration{}, fmt.Errorf("watched-root command recipes must be a JSON array: %w", err)
	}
	metadata, err := normalizeJSON(input.Metadata)
	if err != nil {
		return ProjectWatchedRootRegistration{}, fmt.Errorf("watched-root metadata must be a JSON object: %w", err)
	}
	appliedActor := ""
	if applied {
		appliedActor = req.ActorID
	}
	row := writer.QueryRowContext(ctx, `
		INSERT INTO projects.project_watched_root_registrations (
			project_watched_root_registration_id, project_contract_registration_id,
			project_id, node_id, owner_node_key, local_root_key, backend_root_key,
			worker_key, source_kinds_json, safe_root_key, root_relative_path,
			display_name, sync_mode, backup_mode, index_mode, delete_mode,
			config_hash, config_json, command_json, activation_status,
			last_applied_by_actor_id, last_applied_at, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
		        $11, $12, $13, $14, $15, $16, $17, $18, $19,
		        $20, nullif($21,''), CASE WHEN $23 THEN now() ELSE NULL END, $22)
		ON CONFLICT (project_id, backend_root_key) DO UPDATE
		SET project_contract_registration_id = EXCLUDED.project_contract_registration_id,
		    node_id = EXCLUDED.node_id,
		    owner_node_key = EXCLUDED.owner_node_key,
		    local_root_key = EXCLUDED.local_root_key,
		    worker_key = EXCLUDED.worker_key,
		    source_kinds_json = EXCLUDED.source_kinds_json,
		    safe_root_key = EXCLUDED.safe_root_key,
		    root_relative_path = EXCLUDED.root_relative_path,
		    display_name = EXCLUDED.display_name,
		    sync_mode = EXCLUDED.sync_mode,
		    backup_mode = EXCLUDED.backup_mode,
		    index_mode = EXCLUDED.index_mode,
		    delete_mode = EXCLUDED.delete_mode,
		    config_hash = EXCLUDED.config_hash,
		    config_json = EXCLUDED.config_json,
		    command_json = EXCLUDED.command_json,
		    activation_status = EXCLUDED.activation_status,
		    last_applied_by_actor_id = EXCLUDED.last_applied_by_actor_id,
		    last_applied_at = EXCLUDED.last_applied_at,
		    metadata = EXCLUDED.metadata,
		    updated_at = now()
		RETURNING `+projectWatchedRootRegistrationColumns(),
		ids.NewProjectWatchedRootRegistrationID(),
		input.ProjectContractRegistrationID,
		input.ProjectID,
		input.NodeID,
		input.OwnerNodeKey,
		input.LocalRootKey,
		input.BackendRootKey,
		input.WorkerKey,
		sourceKinds,
		input.SafeRootKey,
		input.RootRelativePath,
		input.DisplayName,
		input.SyncMode,
		input.BackupMode,
		input.IndexMode,
		input.DeleteMode,
		input.ConfigHash,
		config,
		commands,
		input.ActivationStatus,
		appliedActor,
		metadata,
		applied,
	)
	return scanProjectWatchedRootRegistration(row)
}

func (s Service) ListProjectConnectorRegistrations(ctx context.Context, projectRef string) ([]ProjectConnectorRegistration, error) {
	projectID := strings.TrimSpace(projectRef)
	if projectID == "" {
		return nil, fmt.Errorf("project ref is required")
	}
	if !strings.HasPrefix(projectID, ids.ProjectPrefix+"_") {
		project, err := s.ResolveProjectRef(ctx, projectRef)
		if err != nil {
			return nil, err
		}
		projectID = project.ProjectID
	}
	rows, err := s.DB.QueryContext(ctx, projectConnectorRegistrationSelectSQL()+`
		WHERE project_id = $1
		ORDER BY provider_address, connector_key
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	registrations := []ProjectConnectorRegistration{}
	for rows.Next() {
		registration, err := scanProjectConnectorRegistration(rows)
		if err != nil {
			return nil, err
		}
		registrations = append(registrations, registration)
	}
	return registrations, rows.Err()
}

func (s Service) UpsertProjectConnectorRegistration(ctx context.Context, req requestctx.Context, input UpsertProjectConnectorRegistrationInput) (ProjectConnectorRegistration, error) {
	if strings.TrimSpace(input.ProjectContractRegistrationID) == "" || strings.TrimSpace(input.ProjectID) == "" {
		return ProjectConnectorRegistration{}, fmt.Errorf("project registration and project id are required")
	}
	if strings.TrimSpace(input.ConnectorKey) == "" {
		return ProjectConnectorRegistration{}, fmt.Errorf("connector key is required")
	}
	if strings.TrimSpace(input.ProviderKey) == "" {
		return ProjectConnectorRegistration{}, fmt.Errorf("provider key is required")
	}
	if input.CapabilityCount < 0 || input.ActiveCapabilityCount < 0 || input.UsageDocumentCount < 0 {
		return ProjectConnectorRegistration{}, fmt.Errorf("connector counts must be non-negative")
	}
	if strings.TrimSpace(input.ActivationStatus) == "" {
		input.ActivationStatus = ProjectConnectorRegistrationStatusRegistered
	}
	if !validProjectConnectorRegistrationStatus(input.ActivationStatus) {
		return ProjectConnectorRegistration{}, fmt.Errorf("unsupported project connector registration status: %s", input.ActivationStatus)
	}
	metadata, err := normalizeJSON(input.Metadata)
	if err != nil {
		return ProjectConnectorRegistration{}, fmt.Errorf("connector registration metadata must be a JSON object: %w", err)
	}
	row := s.DB.QueryRowContext(ctx, `
		INSERT INTO projects.project_connector_registrations (
			project_connector_registration_id, project_contract_registration_id,
			project_id, connector_key, connector_folder, connector_manifest_path,
			connector_hash, provider_key, provider_address, provider_id,
			provider_status, runtime_kind, capability_count,
			active_capability_count, usage_document_count, activation_status,
			last_activated_by_actor_id, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, nullif($10, ''),
		        $11, $12, $13, $14, $15, $16, $17, $18)
		ON CONFLICT (project_id, connector_key) DO UPDATE
		SET project_contract_registration_id = EXCLUDED.project_contract_registration_id,
		    connector_folder = EXCLUDED.connector_folder,
		    connector_manifest_path = EXCLUDED.connector_manifest_path,
		    connector_hash = EXCLUDED.connector_hash,
		    provider_key = EXCLUDED.provider_key,
		    provider_address = EXCLUDED.provider_address,
		    provider_id = EXCLUDED.provider_id,
		    provider_status = EXCLUDED.provider_status,
		    runtime_kind = EXCLUDED.runtime_kind,
		    capability_count = EXCLUDED.capability_count,
		    active_capability_count = EXCLUDED.active_capability_count,
		    usage_document_count = EXCLUDED.usage_document_count,
		    activation_status = EXCLUDED.activation_status,
		    last_activated_by_actor_id = EXCLUDED.last_activated_by_actor_id,
		    last_activated_at = now(),
		    metadata = EXCLUDED.metadata,
		    updated_at = now()
		RETURNING `+projectConnectorRegistrationColumns(),
		ids.NewProjectConnectorRegistrationID(),
		input.ProjectContractRegistrationID,
		input.ProjectID,
		input.ConnectorKey,
		input.ConnectorFolder,
		input.ConnectorManifestPath,
		input.ConnectorHash,
		input.ProviderKey,
		input.ProviderAddress,
		input.ProviderID,
		input.ProviderStatus,
		input.RuntimeKind,
		input.CapabilityCount,
		input.ActiveCapabilityCount,
		input.UsageDocumentCount,
		input.ActivationStatus,
		req.ActorID,
		metadata,
	)
	return scanProjectConnectorRegistration(row)
}

func (s Service) ListProjectModuleRegistrations(ctx context.Context, projectRef string) ([]ProjectModuleRegistration, error) {
	projectID := strings.TrimSpace(projectRef)
	if projectID == "" {
		return nil, fmt.Errorf("project ref is required")
	}
	if !strings.HasPrefix(projectID, ids.ProjectPrefix+"_") {
		project, err := s.ResolveProjectRef(ctx, projectRef)
		if err != nil {
			return nil, err
		}
		projectID = project.ProjectID
	}
	rows, err := s.DB.QueryContext(ctx, projectModuleRegistrationSelectSQL()+`
		WHERE project_id = $1
		ORDER BY module_id, module_key
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	registrations := []ProjectModuleRegistration{}
	for rows.Next() {
		registration, err := scanProjectModuleRegistration(rows)
		if err != nil {
			return nil, err
		}
		registrations = append(registrations, registration)
	}
	return registrations, rows.Err()
}

func (s Service) UpsertProjectModuleRegistration(ctx context.Context, req requestctx.Context, input UpsertProjectModuleRegistrationInput) (ProjectModuleRegistration, error) {
	if strings.TrimSpace(input.ProjectContractRegistrationID) == "" || strings.TrimSpace(input.ProjectID) == "" {
		return ProjectModuleRegistration{}, fmt.Errorf("project registration and project id are required")
	}
	if strings.TrimSpace(input.ModuleKey) == "" {
		return ProjectModuleRegistration{}, fmt.Errorf("module key is required")
	}
	if input.RequirementCount < 0 || input.ObjectTypeCount < 0 || input.ProviderCount < 0 || input.CapabilityCount < 0 || input.UsageDocumentCount < 0 || input.BackupHookCount < 0 {
		return ProjectModuleRegistration{}, fmt.Errorf("module counts must be non-negative")
	}
	if strings.TrimSpace(input.ActivationStatus) == "" {
		input.ActivationStatus = ProjectModuleRegistrationStatusRegistered
	}
	if !validProjectModuleRegistrationStatus(input.ActivationStatus) {
		return ProjectModuleRegistration{}, fmt.Errorf("unsupported project module registration status: %s", input.ActivationStatus)
	}
	installPlan, err := normalizeJSON(input.InstallPlanJSON)
	if err != nil {
		return ProjectModuleRegistration{}, fmt.Errorf("module install plan must be a JSON object: %w", err)
	}
	exposurePlan, err := normalizeJSON(input.ExposurePlanJSON)
	if err != nil {
		return ProjectModuleRegistration{}, fmt.Errorf("module exposure plan must be a JSON object: %w", err)
	}
	metadata, err := normalizeJSON(input.Metadata)
	if err != nil {
		return ProjectModuleRegistration{}, fmt.Errorf("module registration metadata must be a JSON object: %w", err)
	}
	row := s.DB.QueryRowContext(ctx, `
		INSERT INTO projects.project_module_registrations (
			project_module_registration_id, project_contract_registration_id,
			project_id, module_key, module_folder, module_manifest_path,
			module_project_contract_path, module_manifest_hash,
			module_project_contract_hash, module_package_hash,
			module_id, module_name, module_version, module_kind,
			module_package_id, module_version_id, requirement_count,
			object_type_count, provider_count, capability_count,
			usage_document_count, backup_hook_count, registration_enabled,
			install_plan_json, exposure_plan_json, activation_status,
			last_activated_by_actor_id, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
		        $11, $12, $13, $14, nullif($15, ''), nullif($16, ''),
		        $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28)
		ON CONFLICT (project_id, module_key) DO UPDATE
		SET project_contract_registration_id = EXCLUDED.project_contract_registration_id,
		    module_folder = EXCLUDED.module_folder,
		    module_manifest_path = EXCLUDED.module_manifest_path,
		    module_project_contract_path = EXCLUDED.module_project_contract_path,
		    module_manifest_hash = EXCLUDED.module_manifest_hash,
		    module_project_contract_hash = EXCLUDED.module_project_contract_hash,
		    module_package_hash = EXCLUDED.module_package_hash,
		    module_id = EXCLUDED.module_id,
		    module_name = EXCLUDED.module_name,
		    module_version = EXCLUDED.module_version,
		    module_kind = EXCLUDED.module_kind,
		    module_package_id = EXCLUDED.module_package_id,
		    module_version_id = EXCLUDED.module_version_id,
		    requirement_count = EXCLUDED.requirement_count,
		    object_type_count = EXCLUDED.object_type_count,
		    provider_count = EXCLUDED.provider_count,
		    capability_count = EXCLUDED.capability_count,
		    usage_document_count = EXCLUDED.usage_document_count,
		    backup_hook_count = EXCLUDED.backup_hook_count,
		    registration_enabled = EXCLUDED.registration_enabled,
		    install_plan_json = EXCLUDED.install_plan_json,
		    exposure_plan_json = EXCLUDED.exposure_plan_json,
		    activation_status = EXCLUDED.activation_status,
		    last_activated_by_actor_id = EXCLUDED.last_activated_by_actor_id,
		    last_activated_at = now(),
		    metadata = EXCLUDED.metadata,
		    updated_at = now()
		RETURNING `+projectModuleRegistrationColumns(),
		ids.NewProjectModuleRegistrationID(),
		input.ProjectContractRegistrationID,
		input.ProjectID,
		input.ModuleKey,
		input.ModuleFolder,
		input.ModuleManifestPath,
		input.ModuleProjectContractPath,
		input.ModuleManifestHash,
		input.ModuleProjectContractHash,
		input.ModulePackageHash,
		input.ModuleID,
		input.ModuleName,
		input.ModuleVersion,
		input.ModuleKind,
		input.ModulePackageID,
		input.ModuleVersionID,
		input.RequirementCount,
		input.ObjectTypeCount,
		input.ProviderCount,
		input.CapabilityCount,
		input.UsageDocumentCount,
		input.BackupHookCount,
		input.RegistrationEnabled,
		installPlan,
		exposurePlan,
		input.ActivationStatus,
		req.ActorID,
		metadata,
	)
	return scanProjectModuleRegistration(row)
}

func (s Service) ListProjectWorkflowRegistrations(ctx context.Context, projectRef string) ([]ProjectWorkflowRegistration, error) {
	projectID := strings.TrimSpace(projectRef)
	if projectID == "" {
		return nil, fmt.Errorf("project ref is required")
	}
	if !strings.HasPrefix(projectID, ids.ProjectPrefix+"_") {
		project, err := s.ResolveProjectRef(ctx, projectRef)
		if err != nil {
			return nil, err
		}
		projectID = project.ProjectID
	}
	rows, err := s.DB.QueryContext(ctx, projectWorkflowRegistrationSelectSQL()+`
		WHERE project_id = $1
		ORDER BY workflow_key
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	registrations := []ProjectWorkflowRegistration{}
	for rows.Next() {
		registration, err := scanProjectWorkflowRegistration(rows)
		if err != nil {
			return nil, err
		}
		registrations = append(registrations, registration)
	}
	return registrations, rows.Err()
}

func (s Service) UpsertProjectWorkflowRegistration(ctx context.Context, req requestctx.Context, input UpsertProjectWorkflowRegistrationInput) (ProjectWorkflowRegistration, error) {
	if strings.TrimSpace(input.ProjectContractRegistrationID) == "" || strings.TrimSpace(input.ProjectID) == "" {
		return ProjectWorkflowRegistration{}, fmt.Errorf("project registration and project id are required")
	}
	if strings.TrimSpace(input.WorkflowKey) == "" {
		return ProjectWorkflowRegistration{}, fmt.Errorf("workflow key is required")
	}
	if strings.TrimSpace(input.ActivationStatus) == "" {
		input.ActivationStatus = ProjectWorkflowRegistrationStatusRegistered
	}
	if !validProjectWorkflowRegistrationStatus(input.ActivationStatus) {
		return ProjectWorkflowRegistration{}, fmt.Errorf("unsupported project workflow registration status: %s", input.ActivationStatus)
	}
	metadata, err := normalizeJSON(input.Metadata)
	if err != nil {
		return ProjectWorkflowRegistration{}, fmt.Errorf("workflow registration metadata must be a JSON object: %w", err)
	}
	row := s.DB.QueryRowContext(ctx, `
		INSERT INTO projects.project_workflow_registrations (
			project_workflow_registration_id, project_contract_registration_id,
			project_id, workflow_key, workflow_folder, workflow_manifest_path,
			workflow_manifest_hash, implementation_kind, runtime_kind, workflow_id,
			workflow_version_id, provider_id, provider_address,
			capability_endpoint_id, capability_endpoint_version_id,
			runtime_binding_id, capability_address, activation_status,
			last_activated_by_actor_id, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, nullif($10, ''),
		        nullif($11, ''), nullif($12, ''), $13, nullif($14, ''),
		        nullif($15, ''), nullif($16, ''), $17, $18, $19, $20)
		ON CONFLICT (project_id, workflow_key) DO UPDATE
		SET project_contract_registration_id = EXCLUDED.project_contract_registration_id,
		    workflow_folder = EXCLUDED.workflow_folder,
		    workflow_manifest_path = EXCLUDED.workflow_manifest_path,
		    workflow_manifest_hash = EXCLUDED.workflow_manifest_hash,
		    implementation_kind = EXCLUDED.implementation_kind,
		    runtime_kind = EXCLUDED.runtime_kind,
		    workflow_id = EXCLUDED.workflow_id,
		    workflow_version_id = EXCLUDED.workflow_version_id,
		    provider_id = EXCLUDED.provider_id,
		    provider_address = EXCLUDED.provider_address,
		    capability_endpoint_id = EXCLUDED.capability_endpoint_id,
		    capability_endpoint_version_id = EXCLUDED.capability_endpoint_version_id,
		    runtime_binding_id = EXCLUDED.runtime_binding_id,
		    capability_address = EXCLUDED.capability_address,
		    activation_status = EXCLUDED.activation_status,
		    last_activated_by_actor_id = EXCLUDED.last_activated_by_actor_id,
		    last_activated_at = now(),
		    metadata = EXCLUDED.metadata,
		    updated_at = now()
		RETURNING `+projectWorkflowRegistrationColumns(),
		ids.NewProjectWorkflowRegistrationID(),
		input.ProjectContractRegistrationID,
		input.ProjectID,
		input.WorkflowKey,
		input.WorkflowFolder,
		input.WorkflowManifestPath,
		input.WorkflowManifestHash,
		input.ImplementationKind,
		input.RuntimeKind,
		input.WorkflowID,
		input.WorkflowVersionID,
		input.ProviderID,
		input.ProviderAddress,
		input.CapabilityEndpointID,
		input.CapabilityEndpointVersionID,
		input.RuntimeBindingID,
		input.CapabilityAddress,
		input.ActivationStatus,
		req.ActorID,
		metadata,
	)
	return scanProjectWorkflowRegistration(row)
}

func (s Service) MarkStaleProjectModuleRegistrations(ctx context.Context, req requestctx.Context, projectID, registrationID string, activeModuleKeys []string) error {
	projectID = strings.TrimSpace(projectID)
	registrationID = strings.TrimSpace(registrationID)
	if projectID == "" || registrationID == "" {
		return fmt.Errorf("project id and registration id are required")
	}
	args := []any{projectID, registrationID, ProjectModuleRegistrationStatusStale}
	where := `
		project_id = $1
		AND project_contract_registration_id = $2
	`
	if len(activeModuleKeys) > 0 {
		placeholders := make([]string, 0, len(activeModuleKeys))
		for _, key := range activeModuleKeys {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			args = append(args, key)
			placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
		}
		if len(placeholders) > 0 {
			where += `
		AND module_key NOT IN (` + strings.Join(placeholders, ", ") + `)`
		}
	}
	args = append(args, req.ActorID)
	_, err := s.DB.ExecContext(ctx, `
		UPDATE projects.project_module_registrations
		SET activation_status = $3,
		    last_activated_by_actor_id = nullif($`+fmt.Sprintf("%d", len(args))+`, ''),
		    last_activated_at = now(),
		    updated_at = now()
		WHERE `+where, args...)
	return err
}

func (s Service) MarkStaleProjectWatchedRoots(ctx context.Context, req requestctx.Context, projectID, registrationID string, activeRootKeys []string) error {
	projectID = strings.TrimSpace(projectID)
	registrationID = strings.TrimSpace(registrationID)
	if projectID == "" || registrationID == "" {
		return fmt.Errorf("project id and registration id are required")
	}
	args := []any{projectID, registrationID, ProjectWatchedRootRegistrationStatusStale}
	where := `
		project_id = $1
		AND project_contract_registration_id = $2
	`
	if len(activeRootKeys) > 0 {
		placeholders := make([]string, 0, len(activeRootKeys))
		for _, key := range activeRootKeys {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			args = append(args, key)
			placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
		}
		if len(placeholders) > 0 {
			where += `
		AND local_root_key NOT IN (` + strings.Join(placeholders, ", ") + `)`
		}
	}
	args = append(args, req.ActorID)
	_, err := s.DB.ExecContext(ctx, `
		UPDATE projects.project_watched_root_registrations
		SET activation_status = $3,
		    last_applied_by_actor_id = nullif($`+fmt.Sprintf("%d", len(args))+`, ''),
		    last_applied_at = now(),
		    updated_at = now()
		WHERE `+where, args...)
	return err
}

func (s Service) MarkProjectFacetDeactivated(ctx context.Context, req requestctx.Context, projectID, registrationID, facetKey string, metadata json.RawMessage) error {
	metadata, err := normalizeJSON(metadata)
	if err != nil {
		return fmt.Errorf("facet deactivation metadata must be a JSON object: %w", err)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		UPDATE projects.project_contract_facets
		SET facet_status = $4,
		    metadata = $5,
		    updated_at = now()
		WHERE project_contract_registration_id = $1
		  AND project_id = $2
		  AND facet_key = $3
	`, registrationID, projectID, facetKey, ProjectFacetStatusDisabled, metadata); err != nil {
		return err
	}
	project, err := getProjectTx(ctx, tx, projectID)
	if err != nil {
		return err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  events.TypeProjectFacetDeactivated,
		EventLevel: "audit",
		Request:    req,
		ScopeID:    project.ProjectScopeID,
		TargetKind: "project_contract_facet",
		TargetID:   registrationID + ":" + facetKey,
		Status:     ProjectFacetStatusDisabled,
		Result:     "ok",
		Payload: map[string]any{
			"project_id":                       project.ProjectID,
			"project_slug":                     project.Slug,
			"project_contract_registration_id": registrationID,
			"facet_key":                        facetKey,
			"source":                           "project.deactivate",
		},
		VisibilityClass: "internal",
	}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s Service) MarkProjectScriptExposuresDeactivated(ctx context.Context, req requestctx.Context, projectID string, scriptKeys []string, metadata json.RawMessage) error {
	return s.markProjectRowsDeactivated(ctx, req, projectRowDeactivateInput{
		Table:       "projects.project_script_exposures",
		ProjectID:   projectID,
		KeyColumn:   "script_key",
		Keys:        scriptKeys,
		Status:      ProjectScriptExposureStatusDisabled,
		ActorColumn: "last_activated_by_actor_id",
		TimeColumn:  "last_activated_at",
		Metadata:    metadata,
	})
}

func (s Service) MarkProjectScheduleRegistrationsDeactivated(ctx context.Context, req requestctx.Context, projectID string, scheduleKeys []string, metadata json.RawMessage) error {
	return s.markProjectRowsDeactivated(ctx, req, projectRowDeactivateInput{
		Table:       "projects.project_schedule_registrations",
		ProjectID:   projectID,
		KeyColumn:   "schedule_key",
		Keys:        scheduleKeys,
		Status:      ProjectScheduleRegistrationStatusDisabled,
		ActorColumn: "last_activated_by_actor_id",
		TimeColumn:  "last_activated_at",
		Metadata:    metadata,
	})
}

func (s Service) MarkProjectDirectEventRegistrationsDeactivated(ctx context.Context, req requestctx.Context, projectID string, eventKeys []string, metadata json.RawMessage) error {
	return s.markProjectRowsDeactivated(ctx, req, projectRowDeactivateInput{
		Table:       "projects.project_direct_event_registrations",
		ProjectID:   projectID,
		KeyColumn:   "event_key",
		Keys:        eventKeys,
		Status:      ProjectDirectEventRegistrationStatusDisabled,
		ActorColumn: "last_activated_by_actor_id",
		TimeColumn:  "last_activated_at",
		Metadata:    metadata,
	})
}

func (s Service) MarkProjectWatchedRootsDeactivated(ctx context.Context, req requestctx.Context, projectID string, localRootKeys []string, metadata json.RawMessage) error {
	return s.markProjectRowsDeactivated(ctx, req, projectRowDeactivateInput{
		Table:       "projects.project_watched_root_registrations",
		ProjectID:   projectID,
		KeyColumn:   "local_root_key",
		Keys:        localRootKeys,
		Status:      ProjectWatchedRootRegistrationStatusDisabled,
		ActorColumn: "last_applied_by_actor_id",
		TimeColumn:  "last_applied_at",
		Metadata:    metadata,
	})
}

func (s Service) MarkProjectConnectorRegistrationsDeactivated(ctx context.Context, req requestctx.Context, projectID string, connectorKeys []string, metadata json.RawMessage) error {
	return s.markProjectRowsDeactivated(ctx, req, projectRowDeactivateInput{
		Table:       "projects.project_connector_registrations",
		ProjectID:   projectID,
		KeyColumn:   "connector_key",
		Keys:        connectorKeys,
		Status:      ProjectConnectorRegistrationStatusDisabled,
		ActorColumn: "last_activated_by_actor_id",
		TimeColumn:  "last_activated_at",
		Metadata:    metadata,
	})
}

func (s Service) MarkProjectModuleRegistrationsDeactivated(ctx context.Context, req requestctx.Context, projectID string, moduleKeys []string, metadata json.RawMessage) error {
	return s.markProjectRowsDeactivated(ctx, req, projectRowDeactivateInput{
		Table:       "projects.project_module_registrations",
		ProjectID:   projectID,
		KeyColumn:   "module_key",
		Keys:        moduleKeys,
		Status:      ProjectModuleRegistrationStatusDisabled,
		ActorColumn: "last_activated_by_actor_id",
		TimeColumn:  "last_activated_at",
		Metadata:    metadata,
	})
}

func (s Service) MarkProjectWorkflowRegistrationsDeactivated(ctx context.Context, req requestctx.Context, projectID string, workflowKeys []string, metadata json.RawMessage) error {
	return s.markProjectRowsDeactivated(ctx, req, projectRowDeactivateInput{
		Table:       "projects.project_workflow_registrations",
		ProjectID:   projectID,
		KeyColumn:   "workflow_key",
		Keys:        workflowKeys,
		Status:      ProjectWorkflowRegistrationStatusDisabled,
		ActorColumn: "last_activated_by_actor_id",
		TimeColumn:  "last_activated_at",
		Metadata:    metadata,
	})
}

type projectRowDeactivateInput struct {
	Table       string
	ProjectID   string
	KeyColumn   string
	Keys        []string
	Status      string
	ActorColumn string
	TimeColumn  string
	Metadata    json.RawMessage
}

func (s Service) markProjectRowsDeactivated(ctx context.Context, req requestctx.Context, input projectRowDeactivateInput) error {
	projectID := strings.TrimSpace(input.ProjectID)
	if projectID == "" {
		return fmt.Errorf("project id is required")
	}
	keys := normalizeStringSet(input.Keys)
	if len(keys) == 0 {
		return nil
	}
	metadata, err := normalizeJSON(input.Metadata)
	if err != nil {
		return fmt.Errorf("deactivation metadata must be a JSON object: %w", err)
	}
	args := []any{projectID, input.Status, req.ActorID, metadata}
	placeholders := make([]string, 0, len(keys))
	for _, key := range keys {
		args = append(args, key)
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
	}
	_, err = s.DB.ExecContext(ctx, `
		UPDATE `+input.Table+`
		SET activation_status = $2,
		    `+input.ActorColumn+` = nullif($3, ''),
		    `+input.TimeColumn+` = now(),
		    metadata = $4,
		    updated_at = now()
		WHERE project_id = $1
		  AND `+input.KeyColumn+` IN (`+strings.Join(placeholders, ", ")+`)
	`, args...)
	return err
}

func normalizeStringSet(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
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

func (s Service) CorrelateProjectWatchedRootReports(ctx context.Context, projectRef string) error {
	project, err := s.ResolveProjectRef(ctx, projectRef)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `
		UPDATE projects.project_watched_root_registrations pwr
		SET watched_root_id = wr.watched_root_id,
		    last_reported_at = wr.last_reported_at,
		    activation_status = CASE
		        WHEN pwr.activation_status IN ($2, $3) THEN pwr.activation_status
		        WHEN wr.config_hash = pwr.config_hash THEN $4
		        ELSE $5
		    END,
		    metadata = CASE
		        WHEN wr.config_hash = pwr.config_hash THEN pwr.metadata - 'config_drift'
		        ELSE jsonb_set(pwr.metadata, '{config_drift}', 'true'::jsonb, true)
		    END,
		    updated_at = now()
		FROM watched_roots.roots wr
		WHERE pwr.project_id = $1
		  AND pwr.node_id = wr.node_id
		  AND pwr.backend_root_key = wr.root_key
	`, project.ProjectID, ProjectWatchedRootRegistrationStatusDisabled, ProjectWatchedRootRegistrationStatusStale, ProjectWatchedRootRegistrationStatusReported, ProjectWatchedRootRegistrationStatusPendingAgentApply)
	return err
}

func (s Service) MarkProjectFacetActivated(ctx context.Context, req requestctx.Context, projectID, registrationID, facetKey string, metadata json.RawMessage) error {
	metadata, err := normalizeJSON(metadata)
	if err != nil {
		return fmt.Errorf("facet activation metadata must be a JSON object: %w", err)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		UPDATE projects.project_contract_facets
		SET facet_status = $4,
		    metadata = $5,
		    updated_at = now()
		WHERE project_contract_registration_id = $1
		  AND project_id = $2
		  AND facet_key = $3
	`, registrationID, projectID, facetKey, ProjectFacetStatusActivated, metadata); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE projects.project_contract_registrations
		SET activation_status = $2,
		    base_activated_by_actor_id = COALESCE(base_activated_by_actor_id, $3),
		    base_activated_at = COALESCE(base_activated_at, now()),
		    updated_at = now()
		WHERE project_contract_registration_id = $1
	`, registrationID, ProjectActivationStatusBaseActive, req.ActorID); err != nil {
		return err
	}
	project, err := getProjectTx(ctx, tx, projectID)
	if err != nil {
		return err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  events.TypeProjectFacetActivated,
		EventLevel: "audit",
		Request:    req,
		ScopeID:    project.ProjectScopeID,
		TargetKind: "project_contract_facet",
		TargetID:   registrationID + ":" + facetKey,
		Status:     ProjectFacetStatusActivated,
		Result:     "ok",
		Payload: map[string]any{
			"project_id":                       project.ProjectID,
			"project_slug":                     project.Slug,
			"project_contract_registration_id": registrationID,
			"facet_key":                        facetKey,
			"source":                           "project.activate",
		},
		VisibilityClass: "internal",
	}); err != nil {
		return err
	}
	return tx.Commit()
}

func normalizeRegisterProjectContractInput(input RegisterProjectContractInput) (RegisterProjectContractInput, error) {
	if err := rejectLegacyDeclarationRegistration(input.Contract); err != nil {
		return input, err
	}
	input, err := normalizeRegisterProjectContractFields(input)
	if err != nil {
		return input, err
	}
	if input.RepositorySource != nil {
		projectID := input.Project.ID
		if projectID == "" {
			projectID = "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"
		}
		if _, err := buildProjectRepositoryValidatedSourceInput(input, projectID); err != nil {
			return input, err
		}
	} else if required, err := projectRepositorySourceRequiredByValidatorOutput(input); err != nil {
		return input, err
	} else if required {
		return input, fmt.Errorf("repository source metadata is required for the validator-declared repository facet and complete member set")
	}
	return input, nil
}

// Field normalization alone is pure and never grants registration authority.
func normalizeRegisterProjectContractFields(input RegisterProjectContractInput) (RegisterProjectContractInput, error) {
	input.Facets = append([]ProjectContractFacetInput(nil), input.Facets...)
	if input.RepositorySource != nil {
		repositorySource := *input.RepositorySource
		input.RepositorySource = &repositorySource
	}
	input.ProjectRoot = strings.TrimSpace(input.ProjectRoot)
	input.ContractPath = strings.TrimSpace(input.ContractPath)
	input.ContractHash = strings.TrimSpace(input.ContractHash)
	input.ContractSchemaVersion = strings.TrimSpace(input.ContractSchemaVersion)
	input.Project.ID = strings.TrimSpace(input.Project.ID)
	input.Project.Slug = strings.TrimSpace(input.Project.Slug)
	input.Project.Name = strings.TrimSpace(input.Project.Name)
	input.Project.Description = strings.TrimSpace(input.Project.Description)
	input.Project.OwnerNode = strings.TrimSpace(input.Project.OwnerNode)
	input.Project.Status = strings.ToLower(strings.TrimSpace(input.Project.Status))
	if input.Project.Status == "" {
		input.Project.Status = "draft"
	}
	if !map[string]bool{"draft": true, "active": true, "paused": true, "archived": true}[input.Project.Status] {
		return input, fmt.Errorf("project status is not supported")
	}
	if input.ProjectRoot == "" {
		return input, fmt.Errorf("project root is required")
	}
	if input.ContractPath == "" {
		return input, fmt.Errorf("contract path is required")
	}
	if !contractHashPattern.MatchString(input.ContractHash) {
		return input, fmt.Errorf("contract hash must be a sha256 URI")
	}
	if input.ContractSchemaVersion == "" {
		return input, fmt.Errorf("contract schema version is required")
	}
	if input.Project.Name == "" {
		return input, fmt.Errorf("project name is required")
	}
	if !slugPattern.MatchString(input.Project.Slug) {
		return input, fmt.Errorf("project slug must be lowercase URL-safe and 3-64 characters")
	}
	if input.Project.OwnerNode == "" {
		return input, fmt.Errorf("project owner node is required")
	}
	if input.Project.ID != "" {
		if err := ids.Validate(ids.ProjectPrefix, input.Project.ID); err != nil {
			return input, fmt.Errorf("project id is invalid: %w", err)
		}
	}
	if _, err := normalizeJSON(input.Contract); err != nil {
		return input, fmt.Errorf("contract must be a valid JSON object: %w", err)
	}
	if _, err := normalizeJSON(input.ValidationReport); err != nil {
		return input, fmt.Errorf("validation report must be a valid JSON object: %w", err)
	}
	if _, err := normalizeJSON(input.RegistrationPlan); err != nil {
		return input, fmt.Errorf("registration plan must be a valid JSON object: %w", err)
	}
	derived, err := normalizeJSONArray(input.DerivedProviders)
	if err != nil {
		return input, fmt.Errorf("derived providers must be a valid JSON array: %w", err)
	}
	input.DerivedProviders = derived
	policyRefs, err := normalizeJSONArray(input.PolicyRefs)
	if err != nil {
		return input, fmt.Errorf("policy refs must be a valid JSON array: %w", err)
	}
	input.PolicyRefs = policyRefs
	if _, err := normalizeJSON(input.Metadata); err != nil {
		return input, fmt.Errorf("metadata must be a valid JSON object: %w", err)
	}
	for i := range input.Facets {
		input.Facets[i].Key = strings.TrimSpace(input.Facets[i].Key)
		input.Facets[i].Folder = strings.TrimSpace(input.Facets[i].Folder)
		if input.Facets[i].Key == "" {
			return input, fmt.Errorf("facet key is required")
		}
	}
	if input.ContractSchemaVersion == ProjectRepositoryProjectSchemaV05 && input.RepositorySource == nil {
		input.RepositorySource = &RegisterProjectRepositorySourceInput{ContractPath: input.ContractPath, ContractHash: input.ContractHash, ContractSchemaVersion: ProjectRepositoryProjectSchemaV05}
	}
	if input.RepositorySource != nil {
		input.RepositorySource.ContractPath = strings.TrimSpace(input.RepositorySource.ContractPath)
		input.RepositorySource.ContractHash = strings.TrimSpace(input.RepositorySource.ContractHash)
		input.RepositorySource.ContractSchemaVersion = strings.TrimSpace(input.RepositorySource.ContractSchemaVersion)
	}
	return input, nil
}

type projectRepositoryValidatorMember struct {
	RepositoryID string `json:"id"`
	Key          string `json:"key"`
	Path         string `json:"path"`
	Role         string `json:"role"`
	StateRoot    string `json:"state_root,omitempty"`
}

type projectRepositoryValidatorReposSource struct {
	ContractPath string `json:"contract_path"`
	ContractHash string `json:"contract_hash"`
}

type projectRepositoryValidatorSourceSnapshot struct {
	ContractPath          string `json:"contract_path"`
	ContractHash          string `json:"contract_hash"`
	ContractSchemaVersion string `json:"contract_schema_version"`
}

type projectRepositoryValidatorDocument struct {
	SchemaVersion     string                                    `json:"schema_version"`
	ProjectRoot       string                                    `json:"project_root"`
	ContractPath      string                                    `json:"contract_path"`
	OK                bool                                      `json:"ok"`
	Registerable      bool                                      `json:"registerable"`
	Project           ProjectContractProjectInput               `json:"project"`
	Repos             []projectRepositoryValidatorReposSource   `json:"repos"`
	RepositoryMembers []projectRepositoryValidatorMember        `json:"repository_members"`
	RepositorySource  *projectRepositoryValidatorSourceSnapshot `json:"repository_source"`
	Facets            []ProjectContractFacetInput               `json:"facets"`
}

type projectRepositoryContractDocument struct {
	Kind          string                      `json:"kind"`
	SchemaVersion string                      `json:"schema_version"`
	Project       ProjectContractProjectInput `json:"project"`
}

func buildProjectRepositoryValidatedSourceInput(input RegisterProjectContractInput, projectID string) (ProjectRepositoryValidatedSourceInput, error) {
	if input.ContractSchemaVersion == ProjectRepositoryProjectSchemaV05 {
		return buildDeclarationRepositorySource(input, projectID)
	}
	if input.RepositorySource == nil {
		return ProjectRepositoryValidatedSourceInput{}, fmt.Errorf("repository source metadata is required")
	}
	if strings.EqualFold(strings.TrimSpace(input.Project.Status), "archived") {
		return ProjectRepositoryValidatedSourceInput{}, fmt.Errorf("archived project source cannot mutate repository registration")
	}

	var contract projectRepositoryContractDocument
	if err := json.Unmarshal(input.Contract, &contract); err != nil {
		return ProjectRepositoryValidatedSourceInput{}, fmt.Errorf("decode validated project contract: %w", err)
	}
	contract.Kind = strings.TrimSpace(contract.Kind)
	contract.SchemaVersion = strings.TrimSpace(contract.SchemaVersion)
	contract.Project = normalizeProjectRepositoryValidatorProject(contract.Project)
	if contract.Kind != "loom.project" || contract.SchemaVersion != input.ContractSchemaVersion {
		return ProjectRepositoryValidatedSourceInput{}, fmt.Errorf("validated project contract kind/version does not match registration input")
	}

	var report projectRepositoryValidatorDocument
	if err := json.Unmarshal(input.ValidationReport, &report); err != nil {
		return ProjectRepositoryValidatedSourceInput{}, fmt.Errorf("decode validator report repository source: %w", err)
	}
	var plan projectRepositoryValidatorDocument
	if err := json.Unmarshal(input.RegistrationPlan, &plan); err != nil {
		return ProjectRepositoryValidatedSourceInput{}, fmt.Errorf("decode validator registration plan repository source: %w", err)
	}
	if !report.OK || !report.Registerable || !plan.Registerable {
		return ProjectRepositoryValidatedSourceInput{}, fmt.Errorf("repository source requires a successful registerable validator report and plan")
	}
	report.SchemaVersion = strings.TrimSpace(report.SchemaVersion)
	plan.SchemaVersion = strings.TrimSpace(plan.SchemaVersion)
	if report.SchemaVersion != projectRepositoryValidationReportSchemaV03 || plan.SchemaVersion != projectRepositoryRegistrationPlanSchemaV03 {
		return ProjectRepositoryValidatedSourceInput{}, fmt.Errorf("repository source requires the accepted validator report and registration plan schemas")
	}

	registrationProject := normalizeProjectRepositoryValidatorProject(input.Project)
	report.Project = normalizeProjectRepositoryValidatorProject(report.Project)
	plan.Project = normalizeProjectRepositoryValidatorProject(plan.Project)
	if !projectRepositoryValidatorProjectsEqual(registrationProject, contract.Project) ||
		!projectRepositoryValidatorProjectsEqual(registrationProject, report.Project) ||
		!projectRepositoryValidatorProjectsEqual(registrationProject, plan.Project) {
		return ProjectRepositoryValidatedSourceInput{}, fmt.Errorf("project registration identity differs across contract, validator report, and plan")
	}
	if input.ContractSchemaVersion == ProjectRepositoryProjectSchemaV04 && registrationProject.ID == "" {
		return ProjectRepositoryValidatedSourceInput{}, fmt.Errorf("project.contract.v0.4 repository source requires project.id")
	}
	if registrationProject.ID != "" && registrationProject.ID != projectID {
		return ProjectRepositoryValidatedSourceInput{}, fmt.Errorf("validated project id %s does not match registration project id %s", registrationProject.ID, projectID)
	}

	projectRoot, err := normalizeExactProjectRepositoryPath(input.ProjectRoot, "project root", true)
	if err != nil {
		return ProjectRepositoryValidatedSourceInput{}, err
	}
	projectContractPath, err := normalizeExactProjectRepositoryPath(input.ContractPath, "project contract path", true)
	if err != nil {
		return ProjectRepositoryValidatedSourceInput{}, err
	}
	for _, item := range []struct {
		name     string
		document projectRepositoryValidatorDocument
	}{{name: "validator report", document: report}, {name: "registration plan", document: plan}} {
		name, document := item.name, item.document
		documentRoot, pathErr := normalizeExactProjectRepositoryPath(document.ProjectRoot, name+" project root", true)
		if pathErr != nil || documentRoot != projectRoot {
			return ProjectRepositoryValidatedSourceInput{}, fmt.Errorf("%s project root does not match validated registration source", name)
		}
		documentContractPath, pathErr := normalizeExactProjectRepositoryPath(document.ContractPath, name+" contract path", true)
		if pathErr != nil || documentContractPath != projectContractPath {
			return ProjectRepositoryValidatedSourceInput{}, fmt.Errorf("%s contract path does not match validated registration source", name)
		}
	}

	reportMembers := normalizeProjectRepositoryValidatorMembers(report.RepositoryMembers)
	planMembers := normalizeProjectRepositoryValidatorMembers(plan.RepositoryMembers)
	if !reflect.DeepEqual(reportMembers, planMembers) {
		return ProjectRepositoryValidatedSourceInput{}, fmt.Errorf("validator report and registration plan do not contain the same complete repository member set")
	}
	if err := validateProjectRepositorySourceSnapshot(report.RepositorySource, *input.RepositorySource, "validator report"); err != nil {
		return ProjectRepositoryValidatedSourceInput{}, err
	}
	if err := validateProjectRepositorySourceSnapshot(plan.RepositorySource, *input.RepositorySource, "registration plan"); err != nil {
		return ProjectRepositoryValidatedSourceInput{}, err
	}
	if *report.RepositorySource != *plan.RepositorySource {
		return ProjectRepositoryValidatedSourceInput{}, fmt.Errorf("validator report and registration plan repository source snapshots do not match")
	}
	if err := validateProjectRepositoryReposSourceRefs(report.Repos, *input.RepositorySource, "validator report"); err != nil {
		return ProjectRepositoryValidatedSourceInput{}, err
	}
	if err := validateProjectRepositoryReposSourceRefs(plan.Repos, *input.RepositorySource, "registration plan"); err != nil {
		return ProjectRepositoryValidatedSourceInput{}, err
	}

	validated := ProjectRepositoryValidatedSourceInput{
		ProjectID:             projectID,
		ProjectRoot:           projectRoot,
		ProjectContractPath:   projectContractPath,
		ReposContractPath:     input.RepositorySource.ContractPath,
		OwnerNode:             registrationProject.OwnerNode,
		Versions:              ProjectRepositorySourceVersions{ProjectContract: input.ContractSchemaVersion, ReposContract: input.RepositorySource.ContractSchemaVersion},
		ProjectContractDigest: input.ContractHash,
		ReposContractDigest:   input.RepositorySource.ContractHash,
		RegistrationPlan:      append(json.RawMessage(nil), input.RegistrationPlan...),
		Members:               planMembers,
	}
	if _, err := BuildProjectRepositoryCanonicalSource(validated); err != nil {
		return ProjectRepositoryValidatedSourceInput{}, err
	}
	return validated, nil
}

func projectRepositorySourceRequiredByValidatorOutput(input RegisterProjectContractInput) (bool, error) {
	for _, item := range []struct {
		name string
		raw  json.RawMessage
	}{{name: "validator report", raw: input.ValidationReport}, {name: "registration plan", raw: input.RegistrationPlan}} {
		name, raw := item.name, item.raw
		var document projectRepositoryValidatorDocument
		if err := json.Unmarshal(raw, &document); err != nil {
			return false, fmt.Errorf("decode %s repository declaration: %w", name, err)
		}
		if document.RepositorySource != nil || len(document.RepositoryMembers) > 0 || len(document.Repos) > 0 {
			return true, nil
		}
		for _, facet := range document.Facets {
			if strings.EqualFold(strings.TrimSpace(facet.Key), "repos") && (facet.Enabled || facet.Present) {
				return true, nil
			}
		}
	}
	return false, nil
}

func validateProjectRepositorySourceSnapshot(snapshot *projectRepositoryValidatorSourceSnapshot, source RegisterProjectRepositorySourceInput, document string) error {
	if snapshot == nil {
		return fmt.Errorf("%s repository source snapshot is required", document)
	}
	if snapshot.ContractPath != source.ContractPath {
		return fmt.Errorf("%s repository source contract path does not match registration input", document)
	}
	if snapshot.ContractHash != source.ContractHash {
		return fmt.Errorf("%s repository source contract hash does not match registration input", document)
	}
	if snapshot.ContractSchemaVersion != source.ContractSchemaVersion {
		return fmt.Errorf("%s repository source schema version does not match registration input", document)
	}
	return nil
}

func normalizeProjectRepositoryValidatorProject(project ProjectContractProjectInput) ProjectContractProjectInput {
	project.ID = strings.TrimSpace(project.ID)
	project.Slug = strings.TrimSpace(project.Slug)
	project.Name = strings.TrimSpace(project.Name)
	project.Description = strings.TrimSpace(project.Description)
	project.OwnerNode = strings.TrimSpace(project.OwnerNode)
	project.Status = strings.ToLower(strings.TrimSpace(project.Status))
	if project.Status == "" {
		project.Status = "draft"
	}
	return project
}

func projectRepositoryValidatorProjectsEqual(left, right ProjectContractProjectInput) bool {
	return normalizeProjectRepositoryValidatorProject(left) == normalizeProjectRepositoryValidatorProject(right)
}

func normalizeProjectRepositoryValidatorMembers(input []projectRepositoryValidatorMember) []ProjectRepositoryValidatedMember {
	members := make([]ProjectRepositoryValidatedMember, 0, len(input))
	for _, member := range input {
		members = append(members, ProjectRepositoryValidatedMember{
			RepositoryID: strings.TrimSpace(member.RepositoryID),
			Key:          strings.TrimSpace(member.Key),
			Path:         strings.TrimSpace(member.Path),
			Role:         ProjectRepositoryRole(strings.ToLower(strings.TrimSpace(member.Role))),
			StateRoot:    strings.TrimSpace(member.StateRoot),
		})
	}
	sort.Slice(members, func(i, j int) bool {
		if members[i].Key != members[j].Key {
			return members[i].Key < members[j].Key
		}
		return members[i].RepositoryID < members[j].RepositoryID
	})
	return members
}

func validateProjectRepositoryReposSourceRefs(refs []projectRepositoryValidatorReposSource, source RegisterProjectRepositorySourceInput, document string) error {
	for _, ref := range refs {
		pathValue := strings.TrimSpace(ref.ContractPath)
		hashValue := strings.TrimSpace(ref.ContractHash)
		if pathValue != "" && pathValue != source.ContractPath {
			return fmt.Errorf("%s repos contract path does not match repository source", document)
		}
		if hashValue != "" && hashValue != source.ContractHash {
			return fmt.Errorf("%s repos contract hash does not match repository source", document)
		}
	}
	return nil
}

type projectRepositoryPersistedMember struct {
	Membership ProjectRepositoryMembership
	Repository RepositoryIdentity
}

type projectRepositoryRegistrationPlan struct {
	Current             *ProjectRepositoryCanonicalSource
	Next                ProjectRepositoryCanonicalSource
	Change              ProjectRepositorySourceChange
	SourceRevision      int64
	CurrentMembers      []projectRepositoryPersistedMember
	CurrentObservations map[string]ProjectRepositoryObservation
	NextObservations    map[string]ProjectRepositoryObservationBindingTransition
	RepositoryOwners    map[string]string
}

func prepareProjectRepositoryRegistrationTx(ctx context.Context, tx *sql.Tx, input RegisterProjectContractInput, projectID string, registration ProjectContractRegistration, registrationExists bool) (*projectRepositoryRegistrationPlan, error) {
	validated, err := buildProjectRepositoryValidatedSourceInput(input, projectID)
	if err != nil {
		return nil, err
	}
	return prepareProjectRepositoryRegistrationSourceTx(ctx, tx, input, projectID, registration, registrationExists, validated)
}

func prepareProjectRepositoryRegistrationSourceTx(ctx context.Context, tx *sql.Tx, input RegisterProjectContractInput, projectID string, registration ProjectContractRegistration, registrationExists bool, validated ProjectRepositoryValidatedSourceInput) (*projectRepositoryRegistrationPlan, error) {
	var err error
	if validated.Versions == projectRepositorySourceV05V05 {
		validated, err = resolveDeclarationRepositoryIDsTx(ctx, tx, validated)
		if err != nil {
			return nil, err
		}
	}
	next, err := BuildProjectRepositoryCanonicalSource(validated)
	if err != nil {
		return nil, err
	}

	var current *ProjectRepositoryCanonicalSource
	currentSource, err := getProjectRepositorySourceTx(ctx, tx, projectID)
	sourceExists := err == nil
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if sourceExists {
		if !registrationExists || currentSource.ProjectContractRegistrationID != registration.ProjectContractRegistrationID {
			return nil, fmt.Errorf("%w: current repository source is not bound to the current project contract registration", ErrProjectRepositoryPersistenceCorrupt)
		}
		decoded, decodeErr := decodeAndValidateProjectRepositorySourceRow(currentSource)
		if decodeErr != nil {
			return nil, decodeErr
		}
		current = &decoded
	}

	var currentVersions *ProjectRepositorySourceVersions
	if current != nil {
		versions := current.Snapshot.SourceVersions
		currentVersions = &versions
	}
	if _, err := PlanProjectRepositorySourceVersionTransition(currentVersions, next.Snapshot.SourceVersions); err != nil {
		return nil, err
	}
	change, err := ClassifyProjectRepositorySourceChange(current, next)
	if err != nil {
		return nil, err
	}

	repositoryIDs := make([]string, 0, len(next.Snapshot.Members))
	for _, member := range next.Snapshot.Members {
		repositoryIDs = append(repositoryIDs, member.RepositoryID)
	}
	if current != nil {
		for _, member := range current.Snapshot.Members {
			repositoryIDs = append(repositoryIDs, member.RepositoryID)
		}
	}
	if err := lockProjectRepositoryRegistrationRepositoriesTx(ctx, tx, repositoryIDs); err != nil {
		return nil, err
	}

	currentMembers, err := listProjectRepositoryPersistedMembersTx(ctx, tx, projectID)
	if err != nil {
		return nil, err
	}
	currentObservations, err := listProjectRepositoryObservationsTx(ctx, tx, projectID)
	if err != nil {
		return nil, err
	}
	if err := validateCurrentProjectRepositoryProjection(current, currentMembers, currentObservations); err != nil {
		return nil, err
	}
	repositoryOwners, err := validateNextProjectRepositoryOwnershipTx(ctx, tx, projectID, next.Snapshot.Members)
	if err != nil {
		return nil, err
	}

	nextObservations := make(map[string]ProjectRepositoryObservationBindingTransition, len(next.ObservationBindings))
	for _, binding := range next.ObservationBindings {
		var existing *ProjectRepositoryObservation
		if observation, ok := currentObservations[binding.RepositoryID]; ok {
			observationCopy := observation
			existing = &observationCopy
		}
		transition, err := PlanProjectRepositoryObservationBinding(existing, binding)
		if err != nil {
			return nil, err
		}
		nextObservations[binding.RepositoryID] = transition
	}

	revision := int64(1)
	if currentSource.SourceRevision > 0 {
		revision = currentSource.SourceRevision
		if change.Classification != ProjectRepositorySourceClassificationIdenticalReplay {
			if revision == math.MaxInt64 {
				return nil, fmt.Errorf("project repository source revision overflow")
			}
			revision++
		}
	}
	return &projectRepositoryRegistrationPlan{
		Current:             current,
		Next:                next,
		Change:              change,
		SourceRevision:      revision,
		CurrentMembers:      currentMembers,
		CurrentObservations: currentObservations,
		NextObservations:    nextObservations,
		RepositoryOwners:    repositoryOwners,
	}, nil
}

func lockProjectRepositoryRegistrationProjectTx(ctx context.Context, tx *sql.Tx, input RegisterProjectContractInput) error {
	keys := []string{"slug:" + input.Project.Slug}
	if input.Project.ID != "" {
		keys = append(keys, "id:"+input.Project.ID)
	}
	return acquireProjectRepositoryAdvisoryLocksTx(ctx, tx, "project", keys)
}

func lockProjectRepositoryRegistrationRepositoriesTx(ctx context.Context, tx *sql.Tx, repositoryIDs []string) error {
	keys := make([]string, 0, len(repositoryIDs))
	for _, repositoryID := range repositoryIDs {
		keys = append(keys, "id:"+strings.TrimSpace(repositoryID))
	}
	return acquireProjectRepositoryAdvisoryLocksTx(ctx, tx, "repository", keys)
}

func acquireProjectRepositoryAdvisoryLocksTx(ctx context.Context, tx *sql.Tx, namespace string, keys []string) error {
	keys = sortedUniqueProjectRepositoryLockKeys(keys)
	for _, key := range keys {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "loom:project-repository:"+namespace+":"+key); err != nil {
			return fmt.Errorf("acquire %s advisory lock %q: %w", namespace, key, err)
		}
	}
	return nil
}

func sortedUniqueProjectRepositoryLockKeys(keys []string) []string {
	seen := make(map[string]struct{}, len(keys))
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func getProjectRepositorySourceTx(ctx context.Context, tx *sql.Tx, projectID string) (ProjectRepositorySource, error) {
	var source ProjectRepositorySource
	var snapshot []byte
	err := tx.QueryRowContext(ctx, `
		SELECT project_id, project_contract_registration_id,
		       project_contract_schema_version, repos_contract_schema_version,
		       project_root, project_contract_path, repos_contract_path,
		       project_contract_digest, repos_contract_digest, semantic_digest,
		       location_digest, source_snapshot_json, source_revision,
		       registered_by_actor_id, registered_at, created_at, updated_at
		FROM projects.project_repository_sources
		WHERE project_id = $1
		FOR UPDATE
	`, projectID).Scan(
		&source.ProjectID,
		&source.ProjectContractRegistrationID,
		&source.ProjectContractSchemaVersion,
		&source.ReposContractSchemaVersion,
		&source.ProjectRoot,
		&source.ProjectContractPath,
		&source.ReposContractPath,
		&source.ProjectContractDigest,
		&source.ReposContractDigest,
		&source.SemanticDigest,
		&source.LocationDigest,
		&snapshot,
		&source.SourceRevision,
		&source.RegisteredByActorID,
		&source.RegisteredAt,
		&source.CreatedAt,
		&source.UpdatedAt,
	)
	source.SourceSnapshot = append(json.RawMessage(nil), snapshot...)
	return source, err
}

func decodeAndValidateProjectRepositorySourceRow(source ProjectRepositorySource) (ProjectRepositoryCanonicalSource, error) {
	canonical, err := DecodeProjectRepositoryCanonicalSource(source.SourceSnapshot, source.SemanticDigest, source.LocationDigest)
	if err != nil {
		return ProjectRepositoryCanonicalSource{}, fmt.Errorf("%w: %v", ErrProjectRepositoryPersistenceCorrupt, err)
	}
	snapshot := canonical.Snapshot
	if source.SourceRevision <= 0 || source.ProjectID != snapshot.ProjectID ||
		source.ProjectContractSchemaVersion != snapshot.SourceVersions.ProjectContract ||
		source.ReposContractSchemaVersion != snapshot.SourceVersions.ReposContract ||
		source.ProjectRoot != snapshot.Location.ProjectRoot ||
		source.ProjectContractPath != snapshot.Location.ProjectContractPath ||
		source.ReposContractPath != snapshot.Location.ReposContractPath ||
		source.ProjectContractDigest != snapshot.ProjectContractDigest ||
		source.ReposContractDigest != snapshot.ReposContractDigest {
		return ProjectRepositoryCanonicalSource{}, fmt.Errorf("%w: current source columns do not match its canonical snapshot", ErrProjectRepositoryPersistenceCorrupt)
	}
	return canonical, nil
}

func listProjectRepositoryPersistedMembersTx(ctx context.Context, tx *sql.Tx, projectID string) ([]projectRepositoryPersistedMember, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT m.project_id, m.repository_id, m.repository_owner_project_id,
		       m.member_key, m.member_path, m.role, m.state_root,
		       m.lifecycle_status, m.metadata, m.created_at, m.updated_at,
		       r.repository_id, r.owning_project_id, r.lifecycle_status,
		       r.metadata, r.created_at, r.updated_at
		FROM projects.project_repository_memberships m
		JOIN projects.repositories r ON r.repository_id = m.repository_id
		WHERE m.project_id = $1
		ORDER BY m.repository_id, m.member_key
		FOR UPDATE OF m, r
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []projectRepositoryPersistedMember{}
	for rows.Next() {
		var item projectRepositoryPersistedMember
		var membershipMetadata, repositoryMetadata []byte
		if err := rows.Scan(
			&item.Membership.ProjectID,
			&item.Membership.RepositoryID,
			&item.Membership.RepositoryOwnerProjectID,
			&item.Membership.MemberKey,
			&item.Membership.MemberPath,
			&item.Membership.Role,
			&item.Membership.StateRoot,
			&item.Membership.LifecycleStatus,
			&membershipMetadata,
			&item.Membership.CreatedAt,
			&item.Membership.UpdatedAt,
			&item.Repository.RepositoryID,
			&item.Repository.OwningProjectID,
			&item.Repository.LifecycleStatus,
			&repositoryMetadata,
			&item.Repository.CreatedAt,
			&item.Repository.UpdatedAt,
		); err != nil {
			return nil, err
		}
		item.Membership.Metadata = jsonOrEmpty(membershipMetadata)
		item.Repository.Metadata = jsonOrEmpty(repositoryMetadata)
		result = append(result, item)
	}
	return result, rows.Err()
}

func listProjectRepositoryObservationsTx(ctx context.Context, tx *sql.Tx, projectID string) (map[string]ProjectRepositoryObservation, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT project_id, repository_id, source_binding_digest,
		       observation_posture, reason_code, observed_at,
		       observation_json, observation_revision, created_at, updated_at
		FROM projects.project_repository_observations
		WHERE project_id = $1
		ORDER BY repository_id
		FOR UPDATE
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]ProjectRepositoryObservation{}
	for rows.Next() {
		var observation ProjectRepositoryObservation
		var observedAt sql.NullTime
		var payload []byte
		if err := rows.Scan(
			&observation.ProjectID,
			&observation.RepositoryID,
			&observation.SourceBindingDigest,
			&observation.ObservationPosture,
			&observation.ReasonCode,
			&observedAt,
			&payload,
			&observation.ObservationRevision,
			&observation.CreatedAt,
			&observation.UpdatedAt,
		); err != nil {
			return nil, err
		}
		observation.ObservedAt = timePtr(observedAt)
		observation.Observation = jsonOrEmpty(payload)
		result[observation.RepositoryID] = observation
	}
	return result, rows.Err()
}

func validateCurrentProjectRepositoryProjection(current *ProjectRepositoryCanonicalSource, members []projectRepositoryPersistedMember, observations map[string]ProjectRepositoryObservation) error {
	if current == nil {
		if len(members) != 0 || len(observations) != 0 {
			return fmt.Errorf("%w: member or observation rows exist without a current source", ErrProjectRepositoryPersistenceCorrupt)
		}
		return nil
	}
	if len(members) != len(current.Snapshot.Members) || len(observations) != len(current.Snapshot.Members) {
		return fmt.Errorf("%w: current source, member, and observation counts differ", ErrProjectRepositoryPersistenceCorrupt)
	}
	for index, sourceMember := range current.Snapshot.Members {
		persisted := members[index]
		membership := persisted.Membership
		if membership.ProjectID != current.Snapshot.ProjectID ||
			membership.RepositoryID != sourceMember.RepositoryID ||
			membership.MemberKey != sourceMember.Key ||
			membership.MemberPath != sourceMember.Path ||
			membership.Role != sourceMember.Role ||
			membership.StateRoot != sourceMember.StateRoot ||
			membership.LifecycleStatus != RepositoryLifecycleActive ||
			persisted.Repository.RepositoryID != sourceMember.RepositoryID ||
			membership.RepositoryOwnerProjectID != persisted.Repository.OwningProjectID {
			return fmt.Errorf("%w: current member %s does not match its source snapshot", ErrProjectRepositoryPersistenceCorrupt, sourceMember.RepositoryID)
		}
		switch sourceMember.Role {
		case ProjectRepositoryRolePrimary, ProjectRepositoryRoleComponent:
			if persisted.Repository.OwningProjectID != current.Snapshot.ProjectID || persisted.Repository.LifecycleStatus != RepositoryLifecycleActive {
				return fmt.Errorf("%w: owning repository %s is not active under its project", ErrProjectRepositoryPersistenceCorrupt, sourceMember.RepositoryID)
			}
		case ProjectRepositoryRoleReference:
			if persisted.Repository.OwningProjectID == current.Snapshot.ProjectID {
				return fmt.Errorf("%w: reference repository %s grants ownership", ErrProjectRepositoryPersistenceCorrupt, sourceMember.RepositoryID)
			}
		}
		observation, ok := observations[sourceMember.RepositoryID]
		if !ok || observation.ProjectID != current.Snapshot.ProjectID || observation.SourceBindingDigest != sourceMember.ObservationBindingDigest {
			return fmt.Errorf("%w: observation for %s is absent or bound to another source", ErrProjectRepositoryPersistenceCorrupt, sourceMember.RepositoryID)
		}
		if err := validateProjectRepositoryObservation(observation); err != nil {
			return fmt.Errorf("%w: observation for %s: %v", ErrProjectRepositoryPersistenceCorrupt, sourceMember.RepositoryID, err)
		}
	}
	return nil
}

func validateNextProjectRepositoryOwnershipTx(ctx context.Context, tx *sql.Tx, projectID string, members []ProjectRepositorySourceMemberSnapshot) (map[string]string, error) {
	owners := make(map[string]string, len(members))
	for _, member := range members {
		var owner string
		err := tx.QueryRowContext(ctx, `
			SELECT owning_project_id
			FROM projects.repositories
			WHERE repository_id = $1
			FOR UPDATE
		`, member.RepositoryID).Scan(&owner)
		switch member.Role {
		case ProjectRepositoryRolePrimary, ProjectRepositoryRoleComponent:
			if err == sql.ErrNoRows {
				owners[member.RepositoryID] = projectID
				continue
			}
			if err != nil {
				return nil, err
			}
			if owner != projectID {
				return nil, fmt.Errorf("%w: repository %s is owned by project %s", ErrProjectRepositoryOwnershipConflict, member.RepositoryID, owner)
			}
			owners[member.RepositoryID] = owner
		case ProjectRepositoryRoleReference:
			if err == sql.ErrNoRows {
				return nil, fmt.Errorf("%w: referenced repository %s has no owning project", ErrProjectRepositoryOwnershipConflict, member.RepositoryID)
			}
			if err != nil {
				return nil, err
			}
			if owner == projectID {
				return nil, fmt.Errorf("%w: repository %s owned by project %s cannot be registered there as a reference", ErrProjectRepositoryOwnershipConflict, member.RepositoryID, projectID)
			}
			owners[member.RepositoryID] = owner
		default:
			return nil, fmt.Errorf("unsupported repository role %q", member.Role)
		}
	}
	return owners, nil
}

func persistProjectRepositoryRegistrationTx(ctx context.Context, tx *sql.Tx, actorID, registrationID string, plan projectRepositoryRegistrationPlan) error {
	projectID := plan.Next.Snapshot.ProjectID
	currentByID := make(map[string]projectRepositoryPersistedMember, len(plan.CurrentMembers))
	for _, item := range plan.CurrentMembers {
		currentByID[item.Membership.RepositoryID] = item
	}
	nextByID := make(map[string]ProjectRepositorySourceMemberSnapshot, len(plan.Next.Snapshot.Members))
	for _, member := range plan.Next.Snapshot.Members {
		nextByID[member.RepositoryID] = member
		if member.Role != ProjectRepositoryRoleReference {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO projects.repositories (
					repository_id, owning_project_id, lifecycle_status, metadata
				) VALUES ($1, $2, 'active', '{}'::jsonb)
				ON CONFLICT (repository_id) DO UPDATE
				SET lifecycle_status = 'active',
				    updated_at = now()
				WHERE projects.repositories.owning_project_id = EXCLUDED.owning_project_id
			`, member.RepositoryID, projectID); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM projects.project_repository_memberships
		WHERE project_id = $1
	`, projectID); err != nil {
		return err
	}
	for _, item := range plan.CurrentMembers {
		member := item.Membership
		if member.Role == ProjectRepositoryRoleReference {
			continue
		}
		if _, retained := nextByID[member.RepositoryID]; retained {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE projects.repositories
			SET lifecycle_status = 'archived',
			    updated_at = now()
			WHERE repository_id = $1
			  AND owning_project_id = $2
		`, member.RepositoryID, projectID); err != nil {
			return err
		}
	}

	for _, member := range plan.Next.Snapshot.Members {
		owner := plan.RepositoryOwners[member.RepositoryID]
		metadata := json.RawMessage(`{}`)
		createdAt := time.Time{}
		if current, ok := currentByID[member.RepositoryID]; ok {
			metadata = current.Membership.Metadata
			createdAt = current.Membership.CreatedAt
		}
		if createdAt.IsZero() {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO projects.project_repository_memberships (
					project_id, repository_id, repository_owner_project_id,
					member_key, member_path, role, state_root,
					lifecycle_status, metadata
				) VALUES ($1, $2, $3, $4, $5, $6, $7, 'active', $8)
			`, projectID, member.RepositoryID, owner, member.Key, member.Path, member.Role, member.StateRoot, metadata); err != nil {
				return err
			}
		} else if _, err := tx.ExecContext(ctx, `
			INSERT INTO projects.project_repository_memberships (
				project_id, repository_id, repository_owner_project_id,
				member_key, member_path, role, state_root,
				lifecycle_status, metadata, created_at, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, 'active', $8, $9, now())
		`, projectID, member.RepositoryID, owner, member.Key, member.Path, member.Role, member.StateRoot, metadata, createdAt); err != nil {
			return err
		}

		transition, ok := plan.NextObservations[member.RepositoryID]
		if !ok {
			return fmt.Errorf("missing observation transition for repository %s", member.RepositoryID)
		}
		if err := insertProjectRepositoryObservationTx(ctx, tx, transition.Observation, transition.Changed); err != nil {
			return err
		}
	}

	snapshot := plan.Next.Snapshot
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO projects.project_repository_sources (
			project_id, project_contract_registration_id,
			project_contract_schema_version, repos_contract_schema_version,
			project_root, project_contract_path, repos_contract_path,
			project_contract_digest, repos_contract_digest, semantic_digest,
			location_digest, source_snapshot_json, source_revision,
			registered_by_actor_id
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14
		)
		ON CONFLICT (project_id) DO UPDATE
		SET project_contract_registration_id = EXCLUDED.project_contract_registration_id,
		    project_contract_schema_version = EXCLUDED.project_contract_schema_version,
		    repos_contract_schema_version = EXCLUDED.repos_contract_schema_version,
		    project_root = EXCLUDED.project_root,
		    project_contract_path = EXCLUDED.project_contract_path,
		    repos_contract_path = EXCLUDED.repos_contract_path,
		    project_contract_digest = EXCLUDED.project_contract_digest,
		    repos_contract_digest = EXCLUDED.repos_contract_digest,
		    semantic_digest = EXCLUDED.semantic_digest,
		    location_digest = EXCLUDED.location_digest,
		    source_snapshot_json = EXCLUDED.source_snapshot_json,
		    source_revision = EXCLUDED.source_revision,
		    registered_by_actor_id = EXCLUDED.registered_by_actor_id,
		    registered_at = now(),
		    updated_at = now()
	`,
		projectID,
		registrationID,
		snapshot.SourceVersions.ProjectContract,
		snapshot.SourceVersions.ReposContract,
		snapshot.Location.ProjectRoot,
		snapshot.Location.ProjectContractPath,
		snapshot.Location.ReposContractPath,
		snapshot.ProjectContractDigest,
		snapshot.ReposContractDigest,
		plan.Next.SemanticDigest,
		plan.Next.LocationDigest,
		plan.Next.SnapshotJSON,
		plan.SourceRevision,
		actorID,
	); err != nil {
		return err
	}
	changeSummary, err := json.Marshal(plan.Change.Summary)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO projects.project_repository_source_history (
			project_id, project_contract_schema_version, repos_contract_schema_version,
			project_root, project_contract_path, repos_contract_path,
			project_contract_digest, repos_contract_digest, semantic_digest,
			location_digest, source_snapshot_json, source_revision,
			source_change_kind, change_summary_json, accepted_by_actor_id
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15
		)
	`,
		projectID,
		snapshot.SourceVersions.ProjectContract,
		snapshot.SourceVersions.ReposContract,
		snapshot.Location.ProjectRoot,
		snapshot.Location.ProjectContractPath,
		snapshot.Location.ReposContractPath,
		snapshot.ProjectContractDigest,
		snapshot.ReposContractDigest,
		plan.Next.SemanticDigest,
		plan.Next.LocationDigest,
		plan.Next.SnapshotJSON,
		plan.SourceRevision,
		plan.Change.HistoryChangeKind,
		changeSummary,
		actorID,
	)
	return err
}

func insertProjectRepositoryObservationTx(ctx context.Context, tx *sql.Tx, observation ProjectRepositoryObservation, changed bool) error {
	if err := validateProjectRepositoryObservation(observation); err != nil {
		return err
	}
	if observation.CreatedAt.IsZero() {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO projects.project_repository_observations (
				project_id, repository_id, source_binding_digest,
				observation_posture, reason_code, observed_at,
				observation_json, observation_revision
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`, observation.ProjectID, observation.RepositoryID, observation.SourceBindingDigest,
			observation.ObservationPosture, observation.ReasonCode, observation.ObservedAt,
			observation.Observation, observation.ObservationRevision)
		return err
	}
	if changed {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO projects.project_repository_observations (
				project_id, repository_id, source_binding_digest,
				observation_posture, reason_code, observed_at,
				observation_json, observation_revision, created_at, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
		`, observation.ProjectID, observation.RepositoryID, observation.SourceBindingDigest,
			observation.ObservationPosture, observation.ReasonCode, observation.ObservedAt,
			observation.Observation, observation.ObservationRevision, observation.CreatedAt)
		return err
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO projects.project_repository_observations (
			project_id, repository_id, source_binding_digest,
			observation_posture, reason_code, observed_at,
			observation_json, observation_revision, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, observation.ProjectID, observation.RepositoryID, observation.SourceBindingDigest,
		observation.ObservationPosture, observation.ReasonCode, observation.ObservedAt,
		observation.Observation, observation.ObservationRevision, observation.CreatedAt, observation.UpdatedAt)
	return err
}

func normalizeJSONArray(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return []byte(`[]`), nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	if _, ok := value.([]any); !ok {
		return nil, fmt.Errorf("expected array")
	}
	return raw, nil
}

func (s Service) resolveNodeRef(ctx context.Context, ref string) (string, error) {
	var nodeID string
	err := s.DB.QueryRowContext(ctx, `
		SELECT node_id
		FROM nodes.nodes
		WHERE node_id = $1 OR node_key = $1
	`, strings.TrimSpace(ref)).Scan(&nodeID)
	return nodeID, err
}

func changedProjectKeys(project Project, homeNodeID string, input RegisterProjectContractInput) []string {
	keys := []string{}
	if project.Name != input.Project.Name {
		keys = append(keys, "project.name")
	}
	if project.Description != input.Project.Description {
		keys = append(keys, "project.description")
	}
	if ptrValue(project.HomeNodeID) != homeNodeID {
		keys = append(keys, "project.owner_node")
	}
	if project.Status != projectStatusFromContract(input.Project.Status) {
		keys = append(keys, "project.status")
	}
	return keys
}

func projectStatusFromContract(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "paused":
		return "paused"
	case "archived":
		return "archived"
	default:
		return "active"
	}
}

func projectRegistrationMetadata(existing json.RawMessage, input RegisterProjectContractInput) (json.RawMessage, error) {
	metadata := map[string]any{}
	if len(existing) > 0 {
		if err := json.Unmarshal(existing, &metadata); err != nil {
			return nil, err
		}
	}
	metadata["v0_3_project_contract"] = map[string]any{
		"source":                  "project.register",
		"project_root":            input.ProjectRoot,
		"contract_path":           input.ContractPath,
		"contract_hash":           input.ContractHash,
		"last_contract_hash":      input.ContractHash,
		"contract_status":         input.Project.Status,
		"contract_schema_version": input.ContractSchemaVersion,
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

func updateProjectFromContractTx(ctx context.Context, tx *sql.Tx, project Project, homeNodeID, status string, metadata json.RawMessage, input RegisterProjectContractInput) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE projects.projects
		SET name = $2,
		    description = $3,
		    home_node_id = $4,
		    status = $5,
		    metadata = $6,
		    updated_at = now()
		WHERE project_id = $1
	`, project.ProjectID, input.Project.Name, input.Project.Description, homeNodeID, status, metadata); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE scopes.scopes
		SET display_name = $2,
		    home_node_id = $3,
		    status = CASE WHEN $4 = 'archived' THEN 'archived' WHEN $4 = 'paused' THEN 'paused' ELSE 'active' END,
		    updated_at = now()
		WHERE scope_id = $1
	`, project.ProjectScopeID, input.Project.Name, homeNodeID, status)
	return err
}

type upsertContractRegistrationInput struct {
	RegistrationID        string
	ProjectID             string
	Input                 RegisterProjectContractInput
	RegistrationStatus    string
	ActivationStatus      string
	RegistrationRevision  int
	LastRegisteredActorID string
	Metadata              []byte
}

func upsertContractRegistrationTx(ctx context.Context, tx *sql.Tx, input upsertContractRegistrationInput) (ProjectContractRegistration, error) {
	row := tx.QueryRowContext(ctx, `
		INSERT INTO projects.project_contract_registrations (
			project_contract_registration_id, project_id, project_root, contract_path, contract_hash,
			contract_schema_version, contract_json, validation_report_json, registration_plan_json,
			derived_providers_json, policy_refs_json, registration_status, activation_status,
			registration_revision, last_registered_by_actor_id, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		ON CONFLICT (project_id) DO UPDATE
		SET project_root = EXCLUDED.project_root,
		    contract_path = EXCLUDED.contract_path,
		    contract_hash = EXCLUDED.contract_hash,
		    contract_schema_version = EXCLUDED.contract_schema_version,
		    contract_json = EXCLUDED.contract_json,
		    validation_report_json = EXCLUDED.validation_report_json,
		    registration_plan_json = EXCLUDED.registration_plan_json,
		    derived_providers_json = EXCLUDED.derived_providers_json,
		    policy_refs_json = EXCLUDED.policy_refs_json,
		    registration_status = EXCLUDED.registration_status,
		    activation_status = EXCLUDED.activation_status,
		    registration_revision = EXCLUDED.registration_revision,
		    last_registered_by_actor_id = EXCLUDED.last_registered_by_actor_id,
		    last_registered_at = now(),
		    metadata = EXCLUDED.metadata,
		    updated_at = now()
		RETURNING project_contract_registration_id, project_id, project_root, contract_path, contract_hash,
		          contract_schema_version, contract_json, validation_report_json, registration_plan_json,
		          derived_providers_json, policy_refs_json, registration_status, activation_status,
		          registration_revision, last_registered_by_actor_id, last_registered_at,
		          base_activated_by_actor_id, base_activated_at, metadata, created_at, updated_at
	`,
		input.RegistrationID,
		input.ProjectID,
		input.Input.ProjectRoot,
		input.Input.ContractPath,
		input.Input.ContractHash,
		input.Input.ContractSchemaVersion,
		input.Input.Contract,
		input.Input.ValidationReport,
		input.Input.RegistrationPlan,
		input.Input.DerivedProviders,
		input.Input.PolicyRefs,
		input.RegistrationStatus,
		input.ActivationStatus,
		input.RegistrationRevision,
		input.LastRegisteredActorID,
		input.Metadata,
	)
	return scanContractRegistration(row)
}

func replaceContractFacetsTx(ctx context.Context, tx *sql.Tx, registration ProjectContractRegistration, facets []ProjectContractFacetInput, existingFacets []ProjectContractFacet) error {
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM projects.project_contract_facets
		WHERE project_contract_registration_id = $1
	`, registration.ProjectContractRegistrationID); err != nil {
		return err
	}
	existingByKey := map[string]ProjectContractFacet{}
	for _, facet := range existingFacets {
		existingByKey[facet.FacetKey] = facet
	}
	for _, facet := range facets {
		facetID, status, metadata := replacementFacetState(facet, existingByKey)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO projects.project_contract_facets (
				project_contract_facet_id, project_contract_registration_id, project_id,
				facet_key, folder, enabled, present, placeholder, facet_status, metadata
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb)
		`,
			facetID,
			registration.ProjectContractRegistrationID,
			registration.ProjectID,
			facet.Key,
			facet.Folder,
			facet.Enabled,
			facet.Present,
			facet.Placeholder,
			status,
			metadata,
		); err != nil {
			return err
		}
	}
	return nil
}

func replacementFacetState(facet ProjectContractFacetInput, existingByKey map[string]ProjectContractFacet) (string, string, json.RawMessage) {
	id := ids.NewProjectContractFacetID()
	status := facetStatus(facet)
	metadata := json.RawMessage(`{}`)
	existing, ok := existingByKey[facet.Key]
	if !ok || !facetDeclarationMatches(facet, existing) {
		return id, status, metadata
	}
	if existing.ProjectContractFacetID != "" {
		id = existing.ProjectContractFacetID
	}
	status = existing.FacetStatus
	if len(existing.Metadata) > 0 {
		metadata = existing.Metadata
	}
	return id, status, metadata
}

func facetDeclarationMatches(input ProjectContractFacetInput, existing ProjectContractFacet) bool {
	return input.Key == existing.FacetKey &&
		input.Folder == existing.Folder &&
		input.Enabled == existing.Enabled &&
		input.Present == existing.Present &&
		input.Placeholder == existing.Placeholder
}

func facetStatus(facet ProjectContractFacetInput) string {
	switch {
	case !facet.Enabled:
		return ProjectFacetStatusDisabled
	case !facet.Present:
		return ProjectFacetStatusMissing
	case facet.Placeholder:
		return ProjectFacetStatusPlaceholder
	default:
		return ProjectFacetStatusPendingLaterSlice
	}
}

func (s Service) getContractRegistration(ctx context.Context, ref string) (ProjectContractRegistration, error) {
	row := s.DB.QueryRowContext(ctx, contractRegistrationSelectSQL()+`
		WHERE project_contract_registration_id = $1
	`, ref)
	return scanContractRegistration(row)
}

func (s Service) getContractRegistrationByProject(ctx context.Context, projectID string) (ProjectContractRegistration, error) {
	row := s.DB.QueryRowContext(ctx, contractRegistrationSelectSQL()+`
		WHERE project_id = $1
	`, projectID)
	return scanContractRegistration(row)
}

func getContractRegistrationByProjectTx(ctx context.Context, tx *sql.Tx, projectID string) (ProjectContractRegistration, error) {
	row := tx.QueryRowContext(ctx, contractRegistrationSelectSQL()+`
		WHERE project_id = $1
	`, projectID)
	return scanContractRegistration(row)
}

func (s Service) listContractFacets(ctx context.Context, projectID string) ([]ProjectContractFacet, error) {
	rows, err := s.DB.QueryContext(ctx, contractFacetSelectSQL()+`
		WHERE project_id = $1
		ORDER BY facet_key
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanContractFacets(rows)
}

func listContractFacetsByProjectTx(ctx context.Context, tx *sql.Tx, projectID string) ([]ProjectContractFacet, error) {
	rows, err := tx.QueryContext(ctx, contractFacetSelectSQL()+`
		WHERE project_id = $1
		ORDER BY facet_key
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanContractFacets(rows)
}

func contractRegistrationSelectSQL() string {
	return `
		SELECT project_contract_registration_id, project_id, project_root, contract_path, contract_hash,
		       contract_schema_version, contract_json, validation_report_json, registration_plan_json,
		       derived_providers_json, policy_refs_json, registration_status, activation_status,
		       registration_revision, last_registered_by_actor_id, last_registered_at,
		       base_activated_by_actor_id, base_activated_at, metadata, created_at, updated_at
		FROM projects.project_contract_registrations
	`
}

func contractFacetSelectSQL() string {
	return `
		SELECT project_contract_facet_id, project_contract_registration_id, project_id,
		       facet_key, folder, enabled, present, placeholder, facet_status, metadata,
		       created_at, updated_at
		FROM projects.project_contract_facets
	`
}

func projectScriptExposureSelectSQL() string {
	return `
		SELECT ` + projectScriptExposureColumns() + `
		FROM projects.project_script_exposures
	`
}

func projectScheduleRegistrationSelectSQL() string {
	return `
		SELECT ` + projectScheduleRegistrationColumns() + `
		FROM projects.project_schedule_registrations
	`
}

func projectDirectEventRegistrationSelectSQL() string {
	return `
		SELECT ` + projectDirectEventRegistrationColumns() + `
		FROM projects.project_direct_event_registrations
	`
}

func projectWatchedRootRegistrationSelectSQL() string {
	return `
		SELECT ` + projectWatchedRootRegistrationColumns() + `
		FROM projects.project_watched_root_registrations
	`
}

func projectConnectorRegistrationSelectSQL() string {
	return `
		SELECT ` + projectConnectorRegistrationColumns() + `
		FROM projects.project_connector_registrations
	`
}

func projectModuleRegistrationSelectSQL() string {
	return `
		SELECT ` + projectModuleRegistrationColumns() + `
		FROM projects.project_module_registrations
	`
}

func projectWorkflowRegistrationSelectSQL() string {
	return `
		SELECT ` + projectWorkflowRegistrationColumns() + `
		FROM projects.project_workflow_registrations
	`
}

func projectScriptExposureColumns() string {
	return `project_script_exposure_id, project_contract_registration_id, project_id,
	        script_key, script_folder, script_manifest_path, exposure_path,
	        exposure_hash, exposure_enabled, provider_id, provider_address,
	        script_id, script_version_id, capability_endpoint_id,
	        capability_endpoint_version_id, runtime_binding_id, capability_address,
	        activation_status, last_activated_by_actor_id, last_activated_at,
	        metadata, created_at, updated_at`
}

func projectScheduleRegistrationColumns() string {
	return `project_schedule_registration_id, project_contract_registration_id,
	        project_id, schedule_key, backend_schedule_key, schedule_folder,
	        schedule_manifest_path, schedule_hash, input_path, input_hash,
	        target_capability, automation_id, schedule_id, activation_status,
	        last_activated_by_actor_id, last_activated_at, metadata,
	        created_at, updated_at`
}

func projectDirectEventRegistrationColumns() string {
	return `project_direct_event_registration_id, project_contract_registration_id,
	        project_id, event_key, integration_key, backend_integration_key,
	        endpoint_slug, backend_endpoint_slug, endpoint_path, event_folder,
	        event_manifest_path, event_hash, payload_example_path,
	        payload_example_hash, expected_input_path, expected_input_hash,
	        event_type, target_capability, response_mode, integration_id,
	        auth_profile_id, endpoint_id, automation_id, activation_status,
	        last_activated_by_actor_id, last_activated_at, metadata,
	        created_at, updated_at`
}

func projectWatchedRootRegistrationColumns() string {
	return `project_watched_root_registration_id, project_contract_registration_id,
	        project_id, node_id, owner_node_key, local_root_key,
	        backend_root_key, worker_key, source_kinds_json, safe_root_key,
	        root_relative_path, display_name, sync_mode, backup_mode,
	        index_mode, delete_mode, config_hash, config_json, command_json,
	        watched_root_id, activation_status, last_applied_by_actor_id,
	        last_applied_at, last_reported_at, metadata, created_at,
	        updated_at`
}

func projectConnectorRegistrationColumns() string {
	return `project_connector_registration_id, project_contract_registration_id,
	        project_id, connector_key, connector_folder, connector_manifest_path,
	        connector_hash, provider_key, provider_address, provider_id,
	        provider_status, runtime_kind, capability_count,
	        active_capability_count, usage_document_count, activation_status,
	        last_activated_by_actor_id, last_activated_at, metadata,
	        created_at, updated_at`
}

func projectModuleRegistrationColumns() string {
	return `project_module_registration_id, project_contract_registration_id,
	        project_id, module_key, module_folder, module_manifest_path,
	        module_project_contract_path, module_manifest_hash,
	        module_project_contract_hash, module_package_hash, module_id,
	        module_name, module_version, module_kind, module_package_id,
	        module_version_id, requirement_count, object_type_count,
	        provider_count, capability_count, usage_document_count,
	        backup_hook_count, registration_enabled, install_plan_json,
	        exposure_plan_json, activation_status, last_activated_by_actor_id,
	        last_activated_at, metadata, created_at, updated_at`
}

func projectWorkflowRegistrationColumns() string {
	return `project_workflow_registration_id, project_contract_registration_id,
	        project_id, workflow_key, workflow_folder, workflow_manifest_path,
	        workflow_manifest_hash, implementation_kind, runtime_kind,
	        workflow_id, workflow_version_id, provider_id, provider_address,
	        capability_endpoint_id, capability_endpoint_version_id,
	        runtime_binding_id, capability_address, activation_status,
	        last_activated_by_actor_id, last_activated_at, metadata,
	        created_at, updated_at`
}

type contractRegistrationScanner interface {
	Scan(dest ...any) error
}

type projectScriptExposureScanner interface {
	Scan(dest ...any) error
}

type projectScheduleRegistrationScanner interface {
	Scan(dest ...any) error
}

type projectDirectEventRegistrationScanner interface {
	Scan(dest ...any) error
}

type projectWatchedRootRegistrationScanner interface {
	Scan(dest ...any) error
}

type projectConnectorRegistrationScanner interface {
	Scan(dest ...any) error
}

type projectModuleRegistrationScanner interface {
	Scan(dest ...any) error
}

type projectWorkflowRegistrationScanner interface {
	Scan(dest ...any) error
}

func scanContractRegistration(scanner contractRegistrationScanner) (ProjectContractRegistration, error) {
	var registration ProjectContractRegistration
	var contractJSON, reportJSON, planJSON, providersJSON, policyRefsJSON, metadata []byte
	var baseActor sql.NullString
	var baseActivatedAt sql.NullTime
	if err := scanner.Scan(
		&registration.ProjectContractRegistrationID,
		&registration.ProjectID,
		&registration.ProjectRoot,
		&registration.ContractPath,
		&registration.ContractHash,
		&registration.ContractSchemaVersion,
		&contractJSON,
		&reportJSON,
		&planJSON,
		&providersJSON,
		&policyRefsJSON,
		&registration.RegistrationStatus,
		&registration.ActivationStatus,
		&registration.RegistrationRevision,
		&registration.LastRegisteredByActorID,
		&registration.LastRegisteredAt,
		&baseActor,
		&baseActivatedAt,
		&metadata,
		&registration.CreatedAt,
		&registration.UpdatedAt,
	); err != nil {
		return ProjectContractRegistration{}, err
	}
	registration.Contract = jsonOrEmpty(contractJSON)
	registration.ValidationReport = jsonOrEmpty(reportJSON)
	registration.RegistrationPlan = jsonOrEmpty(planJSON)
	registration.DerivedProviders = jsonArrayOrEmpty(providersJSON)
	registration.PolicyRefs = jsonArrayOrEmpty(policyRefsJSON)
	registration.BaseActivatedByActorID = stringPtr(baseActor)
	registration.BaseActivatedAt = timePtr(baseActivatedAt)
	registration.Metadata = jsonOrEmpty(metadata)
	return registration, nil
}

func scanContractFacets(rows *sql.Rows) ([]ProjectContractFacet, error) {
	facets := []ProjectContractFacet{}
	for rows.Next() {
		var facet ProjectContractFacet
		var metadata []byte
		if err := rows.Scan(
			&facet.ProjectContractFacetID,
			&facet.ProjectContractRegistrationID,
			&facet.ProjectID,
			&facet.FacetKey,
			&facet.Folder,
			&facet.Enabled,
			&facet.Present,
			&facet.Placeholder,
			&facet.FacetStatus,
			&metadata,
			&facet.CreatedAt,
			&facet.UpdatedAt,
		); err != nil {
			return nil, err
		}
		facet.Metadata = jsonOrEmpty(metadata)
		facets = append(facets, facet)
	}
	return facets, rows.Err()
}

func scanProjectScriptExposure(scanner projectScriptExposureScanner) (ProjectScriptExposure, error) {
	var exposure ProjectScriptExposure
	var providerID sql.NullString
	var scriptID sql.NullString
	var scriptVersionID sql.NullString
	var endpointID sql.NullString
	var endpointVersionID sql.NullString
	var runtimeBindingID sql.NullString
	var metadata []byte
	if err := scanner.Scan(
		&exposure.ProjectScriptExposureID,
		&exposure.ProjectContractRegistrationID,
		&exposure.ProjectID,
		&exposure.ScriptKey,
		&exposure.ScriptFolder,
		&exposure.ScriptManifestPath,
		&exposure.ExposurePath,
		&exposure.ExposureHash,
		&exposure.ExposureEnabled,
		&providerID,
		&exposure.ProviderAddress,
		&scriptID,
		&scriptVersionID,
		&endpointID,
		&endpointVersionID,
		&runtimeBindingID,
		&exposure.CapabilityAddress,
		&exposure.ActivationStatus,
		&exposure.LastActivatedByActorID,
		&exposure.LastActivatedAt,
		&metadata,
		&exposure.CreatedAt,
		&exposure.UpdatedAt,
	); err != nil {
		return ProjectScriptExposure{}, err
	}
	exposure.ProviderID = stringPtr(providerID)
	exposure.ScriptID = stringPtr(scriptID)
	exposure.ScriptVersionID = stringPtr(scriptVersionID)
	exposure.CapabilityEndpointID = stringPtr(endpointID)
	exposure.CapabilityEndpointVersionID = stringPtr(endpointVersionID)
	exposure.RuntimeBindingID = stringPtr(runtimeBindingID)
	exposure.Metadata = jsonOrEmpty(metadata)
	return exposure, nil
}

func scanProjectScheduleRegistration(scanner projectScheduleRegistrationScanner) (ProjectScheduleRegistration, error) {
	var registration ProjectScheduleRegistration
	var automationID sql.NullString
	var scheduleID sql.NullString
	var metadata []byte
	if err := scanner.Scan(
		&registration.ProjectScheduleRegistrationID,
		&registration.ProjectContractRegistrationID,
		&registration.ProjectID,
		&registration.ScheduleKey,
		&registration.BackendScheduleKey,
		&registration.ScheduleFolder,
		&registration.ScheduleManifestPath,
		&registration.ScheduleHash,
		&registration.InputPath,
		&registration.InputHash,
		&registration.TargetCapability,
		&automationID,
		&scheduleID,
		&registration.ActivationStatus,
		&registration.LastActivatedByActorID,
		&registration.LastActivatedAt,
		&metadata,
		&registration.CreatedAt,
		&registration.UpdatedAt,
	); err != nil {
		return ProjectScheduleRegistration{}, err
	}
	registration.AutomationID = stringPtr(automationID)
	registration.ScheduleID = stringPtr(scheduleID)
	registration.Metadata = jsonOrEmpty(metadata)
	return registration, nil
}

func scanProjectDirectEventRegistration(scanner projectDirectEventRegistrationScanner) (ProjectDirectEventRegistration, error) {
	var registration ProjectDirectEventRegistration
	var integrationID sql.NullString
	var authProfileID sql.NullString
	var endpointID sql.NullString
	var automationID sql.NullString
	var metadata []byte
	if err := scanner.Scan(
		&registration.ProjectDirectEventRegistrationID,
		&registration.ProjectContractRegistrationID,
		&registration.ProjectID,
		&registration.EventKey,
		&registration.IntegrationKey,
		&registration.BackendIntegrationKey,
		&registration.EndpointSlug,
		&registration.BackendEndpointSlug,
		&registration.EndpointPath,
		&registration.EventFolder,
		&registration.EventManifestPath,
		&registration.EventHash,
		&registration.PayloadExamplePath,
		&registration.PayloadExampleHash,
		&registration.ExpectedInputPath,
		&registration.ExpectedInputHash,
		&registration.EventType,
		&registration.TargetCapability,
		&registration.ResponseMode,
		&integrationID,
		&authProfileID,
		&endpointID,
		&automationID,
		&registration.ActivationStatus,
		&registration.LastActivatedByActorID,
		&registration.LastActivatedAt,
		&metadata,
		&registration.CreatedAt,
		&registration.UpdatedAt,
	); err != nil {
		return ProjectDirectEventRegistration{}, err
	}
	registration.IntegrationID = stringPtr(integrationID)
	registration.AuthProfileID = stringPtr(authProfileID)
	registration.EndpointID = stringPtr(endpointID)
	registration.AutomationID = stringPtr(automationID)
	registration.Metadata = jsonOrEmpty(metadata)
	return registration, nil
}

func scanProjectWatchedRootRegistration(scanner projectWatchedRootRegistrationScanner) (ProjectWatchedRootRegistration, error) {
	var registration ProjectWatchedRootRegistration
	var sourceKinds []byte
	var configJSON []byte
	var commandJSON []byte
	var watchedRootID sql.NullString
	var appliedByActorID sql.NullString
	var lastAppliedAt sql.NullTime
	var lastReportedAt sql.NullTime
	var metadata []byte
	if err := scanner.Scan(
		&registration.ProjectWatchedRootRegistrationID,
		&registration.ProjectContractRegistrationID,
		&registration.ProjectID,
		&registration.NodeID,
		&registration.OwnerNodeKey,
		&registration.LocalRootKey,
		&registration.BackendRootKey,
		&registration.WorkerKey,
		&sourceKinds,
		&registration.SafeRootKey,
		&registration.RootRelativePath,
		&registration.DisplayName,
		&registration.SyncMode,
		&registration.BackupMode,
		&registration.IndexMode,
		&registration.DeleteMode,
		&registration.ConfigHash,
		&configJSON,
		&commandJSON,
		&watchedRootID,
		&registration.ActivationStatus,
		&appliedByActorID,
		&lastAppliedAt,
		&lastReportedAt,
		&metadata,
		&registration.CreatedAt,
		&registration.UpdatedAt,
	); err != nil {
		return ProjectWatchedRootRegistration{}, err
	}
	registration.SourceKinds = jsonArrayOrEmpty(sourceKinds)
	registration.ConfigJSON = jsonOrEmpty(configJSON)
	registration.CommandJSON = jsonArrayOrEmpty(commandJSON)
	registration.WatchedRootID = stringPtr(watchedRootID)
	registration.LastAppliedByActorID = stringPtr(appliedByActorID)
	registration.LastAppliedAt = timePtr(lastAppliedAt)
	registration.LastReportedAt = timePtr(lastReportedAt)
	registration.Metadata = jsonOrEmpty(metadata)
	return registration, nil
}

func scanProjectConnectorRegistration(scanner projectConnectorRegistrationScanner) (ProjectConnectorRegistration, error) {
	var registration ProjectConnectorRegistration
	var providerID sql.NullString
	var metadata []byte
	if err := scanner.Scan(
		&registration.ProjectConnectorRegistrationID,
		&registration.ProjectContractRegistrationID,
		&registration.ProjectID,
		&registration.ConnectorKey,
		&registration.ConnectorFolder,
		&registration.ConnectorManifestPath,
		&registration.ConnectorHash,
		&registration.ProviderKey,
		&registration.ProviderAddress,
		&providerID,
		&registration.ProviderStatus,
		&registration.RuntimeKind,
		&registration.CapabilityCount,
		&registration.ActiveCapabilityCount,
		&registration.UsageDocumentCount,
		&registration.ActivationStatus,
		&registration.LastActivatedByActorID,
		&registration.LastActivatedAt,
		&metadata,
		&registration.CreatedAt,
		&registration.UpdatedAt,
	); err != nil {
		return ProjectConnectorRegistration{}, err
	}
	registration.ProviderID = stringPtr(providerID)
	registration.Metadata = jsonOrEmpty(metadata)
	return registration, nil
}

func scanProjectModuleRegistration(scanner projectModuleRegistrationScanner) (ProjectModuleRegistration, error) {
	var registration ProjectModuleRegistration
	var packageID sql.NullString
	var versionID sql.NullString
	var installPlan, exposurePlan, metadata []byte
	if err := scanner.Scan(
		&registration.ProjectModuleRegistrationID,
		&registration.ProjectContractRegistrationID,
		&registration.ProjectID,
		&registration.ModuleKey,
		&registration.ModuleFolder,
		&registration.ModuleManifestPath,
		&registration.ModuleProjectContractPath,
		&registration.ModuleManifestHash,
		&registration.ModuleProjectContractHash,
		&registration.ModulePackageHash,
		&registration.ModuleID,
		&registration.ModuleName,
		&registration.ModuleVersion,
		&registration.ModuleKind,
		&packageID,
		&versionID,
		&registration.RequirementCount,
		&registration.ObjectTypeCount,
		&registration.ProviderCount,
		&registration.CapabilityCount,
		&registration.UsageDocumentCount,
		&registration.BackupHookCount,
		&registration.RegistrationEnabled,
		&installPlan,
		&exposurePlan,
		&registration.ActivationStatus,
		&registration.LastActivatedByActorID,
		&registration.LastActivatedAt,
		&metadata,
		&registration.CreatedAt,
		&registration.UpdatedAt,
	); err != nil {
		return ProjectModuleRegistration{}, err
	}
	registration.ModulePackageID = stringPtr(packageID)
	registration.ModuleVersionID = stringPtr(versionID)
	registration.InstallPlanJSON = jsonOrEmpty(installPlan)
	registration.ExposurePlanJSON = jsonOrEmpty(exposurePlan)
	registration.Metadata = jsonOrEmpty(metadata)
	return registration, nil
}

func scanProjectWorkflowRegistration(scanner projectWorkflowRegistrationScanner) (ProjectWorkflowRegistration, error) {
	var registration ProjectWorkflowRegistration
	var workflowID sql.NullString
	var workflowVersionID sql.NullString
	var providerID sql.NullString
	var endpointID sql.NullString
	var endpointVersionID sql.NullString
	var runtimeBindingID sql.NullString
	var metadata []byte
	if err := scanner.Scan(
		&registration.ProjectWorkflowRegistrationID,
		&registration.ProjectContractRegistrationID,
		&registration.ProjectID,
		&registration.WorkflowKey,
		&registration.WorkflowFolder,
		&registration.WorkflowManifestPath,
		&registration.WorkflowManifestHash,
		&registration.ImplementationKind,
		&registration.RuntimeKind,
		&workflowID,
		&workflowVersionID,
		&providerID,
		&registration.ProviderAddress,
		&endpointID,
		&endpointVersionID,
		&runtimeBindingID,
		&registration.CapabilityAddress,
		&registration.ActivationStatus,
		&registration.LastActivatedByActorID,
		&registration.LastActivatedAt,
		&metadata,
		&registration.CreatedAt,
		&registration.UpdatedAt,
	); err != nil {
		return ProjectWorkflowRegistration{}, err
	}
	registration.WorkflowID = stringPtr(workflowID)
	registration.WorkflowVersionID = stringPtr(workflowVersionID)
	registration.ProviderID = stringPtr(providerID)
	registration.CapabilityEndpointID = stringPtr(endpointID)
	registration.CapabilityEndpointVersionID = stringPtr(endpointVersionID)
	registration.RuntimeBindingID = stringPtr(runtimeBindingID)
	registration.Metadata = jsonOrEmpty(metadata)
	return registration, nil
}

func validProjectScriptExposureStatus(status string) bool {
	switch status {
	case ProjectScriptExposureStatusRegistered,
		ProjectScriptExposureStatusActive,
		ProjectScriptExposureStatusDisabled,
		ProjectScriptExposureStatusBlocked,
		ProjectScriptExposureStatusStale:
		return true
	default:
		return false
	}
}

func validProjectScheduleRegistrationStatus(status string) bool {
	switch status {
	case ProjectScheduleRegistrationStatusRegistered,
		ProjectScheduleRegistrationStatusPaused,
		ProjectScheduleRegistrationStatusActive,
		ProjectScheduleRegistrationStatusDisabled,
		ProjectScheduleRegistrationStatusBlocked,
		ProjectScheduleRegistrationStatusStale:
		return true
	default:
		return false
	}
}

func validProjectDirectEventRegistrationStatus(status string) bool {
	switch status {
	case ProjectDirectEventRegistrationStatusRegistered,
		ProjectDirectEventRegistrationStatusPaused,
		ProjectDirectEventRegistrationStatusActive,
		ProjectDirectEventRegistrationStatusDisabled,
		ProjectDirectEventRegistrationStatusBlocked,
		ProjectDirectEventRegistrationStatusStale:
		return true
	default:
		return false
	}
}

func validProjectWatchedRootRegistrationStatus(status string) bool {
	switch status {
	case ProjectWatchedRootRegistrationStatusRegistered,
		ProjectWatchedRootRegistrationStatusPendingAgentApply,
		ProjectWatchedRootRegistrationStatusApplied,
		ProjectWatchedRootRegistrationStatusReported,
		ProjectWatchedRootRegistrationStatusBlocked,
		ProjectWatchedRootRegistrationStatusDisabled,
		ProjectWatchedRootRegistrationStatusStale:
		return true
	default:
		return false
	}
}

func validProjectConnectorRegistrationStatus(status string) bool {
	switch status {
	case ProjectConnectorRegistrationStatusRegistered,
		ProjectConnectorRegistrationStatusActive,
		ProjectConnectorRegistrationStatusDisabled,
		ProjectConnectorRegistrationStatusBlocked,
		ProjectConnectorRegistrationStatusStale:
		return true
	default:
		return false
	}
}

func validProjectModuleRegistrationStatus(status string) bool {
	switch status {
	case ProjectModuleRegistrationStatusRegistered,
		ProjectModuleRegistrationStatusDisabled,
		ProjectModuleRegistrationStatusBlocked,
		ProjectModuleRegistrationStatusStale:
		return true
	default:
		return false
	}
}

func validProjectWorkflowRegistrationStatus(status string) bool {
	switch status {
	case ProjectWorkflowRegistrationStatusRegistered,
		ProjectWorkflowRegistrationStatusActive,
		ProjectWorkflowRegistrationStatusDisabled,
		ProjectWorkflowRegistrationStatusBlocked,
		ProjectWorkflowRegistrationStatusStale:
		return true
	default:
		return false
	}
}

func jsonArrayOrEmpty(value []byte) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`[]`)
	}
	return json.RawMessage(value)
}

func ptrValue(ptr *string) string {
	if ptr == nil {
		return ""
	}
	return *ptr
}

func facetDeclarationInputSignature(facets []ProjectContractFacetInput) string {
	parts := make([]string, 0, len(facets))
	for _, facet := range facets {
		parts = append(parts, fmt.Sprintf("%s|%s|%t|%t|%t", facet.Key, facet.Folder, facet.Enabled, facet.Present, facet.Placeholder))
	}
	sort.Strings(parts)
	return strings.Join(parts, "\n")
}

func facetDeclarationRecordSignature(facets []ProjectContractFacet) string {
	parts := make([]string, 0, len(facets))
	for _, facet := range facets {
		parts = append(parts, fmt.Sprintf("%s|%s|%t|%t|%t", facet.FacetKey, facet.Folder, facet.Enabled, facet.Present, facet.Placeholder))
	}
	sort.Strings(parts)
	return strings.Join(parts, "\n")
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func jsonRawEqual(left, right json.RawMessage) bool {
	return jsonRawEqualIgnoringKeys(left, right)
}

func jsonRawEqualIgnoringKeys(left, right json.RawMessage, ignoredKeys ...string) bool {
	var leftValue any
	var rightValue any
	if len(left) == 0 {
		left = json.RawMessage(`null`)
	}
	if len(right) == 0 {
		right = json.RawMessage(`null`)
	}
	if err := json.Unmarshal(left, &leftValue); err != nil {
		return false
	}
	if err := json.Unmarshal(right, &rightValue); err != nil {
		return false
	}
	if len(ignoredKeys) > 0 {
		ignored := map[string]bool{}
		for _, key := range ignoredKeys {
			ignored[key] = true
		}
		leftValue = stripJSONKeys(leftValue, ignored)
		rightValue = stripJSONKeys(rightValue, ignored)
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func stripJSONKeys(value any, ignored map[string]bool) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if ignored[key] {
				continue
			}
			out[key] = stripJSONKeys(item, ignored)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, stripJSONKeys(item, ignored))
		}
		return out
	default:
		return value
	}
}
