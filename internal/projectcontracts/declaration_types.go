package projectcontracts

import "time"

// D0 freezes DTOs only. Nothing here enables v0.5 in LoadProject, validates a
// declaration, computes a production plan, dispatches an owner or grants access.
// The normative rules and later implementation owners are in the feature's
// contract.md. In particular, ordinary json/yaml unmarshalling is NOT validation.
const (
	ProjectSchemaV05            = "project.contract.v0.5"
	DeclarationPlanSchemaV05    = "project.declaration_plan.v0.5"
	DeclarationRequestSchemaV05 = "project.declaration_request.v0.5"
	DeclarationResultSchemaV05  = "project.declaration_result.v0.5"
	DeclarationStatusSchemaV05  = "project.declaration_status.v0.5"
)

// ResourceKey is a stable project-local name, not a global registry ID.
type ResourceKey string

type DeclarationResourceKind string

const (
	DeclarationRepository  DeclarationResourceKind = "repository"
	DeclarationKnowledge   DeclarationResourceKind = "knowledge"
	DeclarationProtection  DeclarationResourceKind = "protection"
	DeclarationApplication DeclarationResourceKind = "application"
)

// ProjectDeclaration is the future .loom/project.yaml source document.
// Project reuses the existing identity/domain fields; ID is required in v0.5.
// Resources must be a mapping, including {} for an unenrolled plain project.
type ProjectDeclaration struct {
	Kind            string                              `json:"kind" yaml:"kind"`
	SchemaVersion   string                              `json:"schema_version" yaml:"schema_version"`
	Project         ProjectSpec                         `json:"project" yaml:"project"`
	Resources       map[ResourceKey]ResourceDeclaration `json:"resources" yaml:"resources"`
	LegacyContracts *LegacyContractsDeclaration         `json:"legacy_contracts,omitempty" yaml:"legacy_contracts,omitempty"`
}

// LegacyContractsDeclaration retains selected old source semantics for migration
// comparison. Its presence never authorizes registration or owner adoption.
type LegacyContractsDeclaration struct {
	Project LegacyProjectReference `json:"project" yaml:"project"`
	Notes   *LegacyWatchReference  `json:"notes,omitempty" yaml:"notes,omitempty"`
	Repos   *LegacyWatchReference  `json:"repos,omitempty" yaml:"repos,omitempty"`
}
type LegacyProjectReference struct {
	Ref           string `json:"ref" yaml:"ref"`
	SchemaVersion string `json:"schema_version" yaml:"schema_version"`
	Digest        string `json:"digest" yaml:"digest"`
}
type LegacyWatchReference struct {
	Key           ResourceKey `json:"key" yaml:"key"`
	Ref           string      `json:"ref" yaml:"ref"`
	SchemaVersion string      `json:"schema_version" yaml:"schema_version"`
	Digest        string      `json:"digest" yaml:"digest"`
	Protection    ResourceKey `json:"protection" yaml:"protection"`
}

// ResourceDeclaration is a closed union: exactly the payload named by Kind.
type ResourceDeclaration struct {
	Kind        DeclarationResourceKind `json:"kind" yaml:"kind"`
	Repository  *RepositoryDeclaration  `json:"repository,omitempty" yaml:"repository,omitempty"`
	Knowledge   *KnowledgeDeclaration   `json:"knowledge,omitempty" yaml:"knowledge,omitempty"`
	Protection  *ProtectionDeclaration  `json:"protection,omitempty" yaml:"protection,omitempty"`
	Application *ApplicationDeclaration `json:"application,omitempty" yaml:"application,omitempty"`
}

type RepositoryDeclaration struct {
	ID         string      `json:"id,omitempty" yaml:"id,omitempty"`
	Path       string      `json:"path" yaml:"path"`
	Role       string      `json:"role" yaml:"role"`
	StateRoot  string      `json:"state_root,omitempty" yaml:"state_root,omitempty"`
	Protection ResourceKey `json:"protection,omitempty" yaml:"protection,omitempty"`
}

type KnowledgeCategory string

const (
	KnowledgeCategoryNotes    KnowledgeCategory = "notes"
	KnowledgeCategoryDocs     KnowledgeCategory = "docs"
	KnowledgeCategoryResearch KnowledgeCategory = "research"
)

type KnowledgeDeclaration struct {
	Path       string            `json:"path" yaml:"path"`
	Category   KnowledgeCategory `json:"category" yaml:"category"`
	Protection ResourceKey       `json:"protection,omitempty" yaml:"protection,omitempty"`
}

// ProtectionDeclaration references a project-relative backup.policy.v0.3 file,
// resolved by the projectcontracts compiler (not a named backupcontracts lookup).
// Each explicit Path or referring resource target selects exactly one policy
// root with that same path. Other policy roots enroll nothing. D1/D2 own this
// adapter; D4 preserves old coverage, owner keys and disabled states.
type ProtectionDeclaration struct {
	PolicyRef string `json:"policy_ref" yaml:"policy_ref"`
	Path      string `json:"path,omitempty" yaml:"path,omitempty"`
}

// ApplicationDeclaration is only the shared composition envelope. E1 owns the
// referenced manifest's artifact/config/process/readiness schema and compiler.
type ApplicationDeclaration struct {
	CredentialSources  map[string]string               `json:"credential_sources,omitempty" yaml:"credential_sources,omitempty"`
	ArtifactDescriptor string                          `json:"artifact_descriptor,omitempty" yaml:"artifact_descriptor,omitempty"`
	Repository         ResourceKey                     `json:"repository" yaml:"repository"`
	Manifest           string                          `json:"manifest" yaml:"manifest"`
	Data               map[ResourceKey]ApplicationData `json:"data,omitempty" yaml:"data,omitempty"`
	Endpoint           *ApplicationEndpoint            `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`
	Credentials        []string                        `json:"credentials,omitempty" yaml:"credentials,omitempty"`
}

// ApplicationData names persistent data, never an implicit worktree directory.
// Exactly one of Path (project-relative) and BindingRef (future E1/E2 allocation
// prerequisite, not an existing named resolver). No approval is implied.
type ApplicationData struct {
	Backup     string               `json:"backup,omitempty" yaml:"backup,omitempty"`
	Path       string               `json:"path,omitempty" yaml:"path,omitempty"`
	BindingRef string               `json:"binding_ref,omitempty" yaml:"binding_ref,omitempty"`
	Capacity   *ApplicationCapacity `json:"capacity,omitempty" yaml:"capacity,omitempty"`
	Protection ResourceKey          `json:"protection,omitempty" yaml:"protection,omitempty"`
}

// ApplicationCapacity is a planning request in bytes. It is neither a quota nor
// a monitoring threshold. E1 must model those separate effects in its manifest.
type ApplicationCapacity struct {
	PlannedBytes uint64 `json:"planned_bytes" yaml:"planned_bytes"`
}

type ApplicationExposure string

const (
	ApplicationLoopback    ApplicationExposure = "loopback"
	ApplicationPrivate     ApplicationExposure = "private"
	ApplicationPublicHTTPS ApplicationExposure = "public_https"
)

type ApplicationEndpoint struct {
	Hostname    string              `json:"hostname,omitempty" yaml:"hostname,omitempty"`
	Exposure    ApplicationExposure `json:"exposure" yaml:"exposure"`
	EndpointRef string              `json:"endpoint_ref,omitempty" yaml:"endpoint_ref,omitempty"`
}

// DeclarationTarget is resolved output, not a portable declaration or a caller
// claim of authority. LocationRevision fences owner-node path/custody changes.
type DeclarationTarget struct {
	ProjectID        string `json:"project_id"`
	OwnerNodeID      string `json:"owner_node_id"`
	ProjectRoot      string `json:"project_root"`
	LocationRevision string `json:"location_revision"`
}

// DeclarationSource binds an exact transitive document. Ref is project-relative
// for source files (including protection policy bytes), or an owner-qualified
// reference for supplied artifact/prerequisite facts. E1/E2 own application,
// allocation and endpoint resolution; this DTO does not assert those resolvers
// exist. Hash covers bytes; Revision fences mutable reference resolution.
type DeclarationSource struct {
	Ref           string `json:"ref"`
	SchemaVersion string `json:"schema_version"`
	Hash          string `json:"hash"`
	Revision      string `json:"revision"`
}

// DeclarationBinding preserves existing domain IDs; no second registry is
// created. ID-less source repositories resolve or allocate their repo_ ID once.
type DeclarationBinding struct {
	Kind         DeclarationResourceKind `json:"kind"`
	RepositoryID string                  `json:"repository_id,omitempty"`
	OwnerRef     string                  `json:"owner_ref,omitempty"`
}

type DeclarationEffect string

const (
	DeclarationReconcile   DeclarationEffect = "reconcile"
	DeclarationProjections DeclarationEffect = "refresh_projections"
)

type DeclarationActionKind string

const (
	DeclarationRegisterProject     DeclarationActionKind = "register_project"
	DeclarationRegisterRepository  DeclarationActionKind = "register_repository"
	DeclarationEnrollKnowledge     DeclarationActionKind = "enroll_knowledge"
	DeclarationReconcileProtection DeclarationActionKind = "reconcile_protection"
	DeclarationApplyApplication    DeclarationActionKind = "apply_application"
	DeclarationRefreshProjection   DeclarationActionKind = "refresh_projection"
	DeclarationRetireResource      DeclarationActionKind = "retire_resource"
)

type DeclarationOwner string

const (
	DeclarationOwnerProjects    DeclarationOwner = "projects"
	DeclarationOwnerKnowledge   DeclarationOwner = "knowledge"
	DeclarationOwnerProtection  DeclarationOwner = "backupcontracts"
	DeclarationOwnerApplication DeclarationOwner = "serviceregistry"
	DeclarationOwnerNotes       DeclarationOwner = "notesprojection"
	DeclarationOwnerProvenance  DeclarationOwner = "provenance"
)

// DeclarationAction describes one typed owner effect. InputHash binds the
// owner's complete normalized payload, including E1's later application plan.
// IDs and dependency order are deterministic, with no display prose in identity.
type DeclarationAction struct {
	PublicURL     string                   `json:"public_url,omitempty"`
	ID            string                   `json:"id"`
	Kind          DeclarationActionKind    `json:"kind"`
	Resource      ResourceKey              `json:"resource,omitempty"`
	Owner         DeclarationOwner         `json:"owner"`
	TargetRef     string                   `json:"target_ref"`
	InputHash     string                   `json:"input_hash"`
	DependsOn     []string                 `json:"depends_on"`
	Authorization DeclarationAuthorization `json:"authorization"`
}

// DeclarationAuthorization states required execution authority, never a grant.
// Actor identity comes from authenticated request context, not request JSON.
type DeclarationAuthorization struct {
	ActorID          string `json:"actor_id"`
	NodeID           string `json:"node_id"`
	Level            int    `json:"level"`
	PolicyRevision   string `json:"policy_revision"`
	ApprovalRequired bool   `json:"approval_required"`
}

// DeclarationPlanBasis is the complete identity input. See contract.md for
// canonical ordering and hashing. Revisions contains the relevant existing
// registry/lifecycle/policy/authorization/owner-fact revisions, never a clock.
type DeclarationPlanBasis struct {
	SchemaVersion string                             `json:"schema_version"`
	Target        DeclarationTarget                  `json:"target"`
	Sources       []DeclarationSource                `json:"sources"`
	Bindings      map[ResourceKey]DeclarationBinding `json:"bindings"`
	Revisions     map[string]string                  `json:"revisions"`
	Effects       []DeclarationEffect                `json:"effects"`
	Actions       []DeclarationAction                `json:"actions"`
}

type DeclarationPlan struct {
	SchemaVersion string               `json:"schema_version"`
	PlanID        string               `json:"plan_id"`
	Basis         DeclarationPlanBasis `json:"basis"`
	Readiness     DeclarationReadiness `json:"readiness"`
	Errors        []DeclarationError   `json:"errors"`
	GeneratedAt   string               `json:"generated_at"`
}

// DeclarationPlanRequest resolves the selected project/location without source
// writes. Effect omission defaults to reconcile; explicit [] is invalid.
type DeclarationPlanRequest struct {
	SchemaVersion string              `json:"schema_version"`
	ProjectRef    string              `json:"project_ref"`
	NodeRef       string              `json:"node_ref,omitempty"`
	Effects       []DeclarationEffect `json:"effects,omitempty"`
}

// DeclarationApplyRequest carries a reviewed identity, not agent-shuttled plan
// files. The server reconstructs and compares the complete basis. OperationID
// is present only for resume; IdempotencyKey is stable for lost-response retry.
type DeclarationApplyRequest struct {
	SchemaVersion  string              `json:"schema_version"`
	ProjectRef     string              `json:"project_ref"`
	NodeRef        string              `json:"node_ref,omitempty"`
	Effects        []DeclarationEffect `json:"effects"`
	PlanID         string              `json:"plan_id"`
	IdempotencyKey string              `json:"idempotency_key"`
	OperationID    string              `json:"operation_id,omitempty"`
	ApprovalRefs   []string            `json:"approval_refs,omitempty"`
}

type DeclarationFactState string

const (
	DeclarationUnknown       DeclarationFactState = "unknown"
	DeclarationNotApplicable DeclarationFactState = "not_applicable"
	DeclarationPending       DeclarationFactState = "pending"
	DeclarationSatisfied     DeclarationFactState = "satisfied"
	DeclarationFailed        DeclarationFactState = "failed"
)

// DeclarationFact is revision-qualified evidence for one dimension only.
// EvidenceRef names an existing owner receipt; an absent observation is unknown.
type DeclarationFact struct {
	State       DeclarationFactState `json:"state"`
	Revision    string               `json:"revision"`
	EvidenceRef string               `json:"evidence_ref,omitempty"`
}

type DeclarationReadiness struct {
	Endpoint   *ApplicationEndpointStatus `json:"endpoint,omitempty"`
	Desired    DeclarationFact            `json:"desired"`
	Queued     DeclarationFact            `json:"queued"`
	Applied    DeclarationFact            `json:"applied"`
	Processing DeclarationFact            `json:"processing"`
	Healthy    DeclarationFact            `json:"healthy"`
	Protected  DeclarationFact            `json:"protected"`
	Verified   DeclarationFact            `json:"verified"`
}

type ApplicationEndpointStatus struct {
	URL        string    `json:"url"`
	DNS        string    `json:"dns"`
	TLS        string    `json:"tls"`
	Routing    string    `json:"routing"`
	HTTPStatus int       `json:"http_status,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
}

type DeclarationOperationState string

const (
	DeclarationOperationQueued     DeclarationOperationState = "queued"
	DeclarationOperationRunning    DeclarationOperationState = "running"
	DeclarationOperationPartial    DeclarationOperationState = "partial"
	DeclarationOperationSucceeded  DeclarationOperationState = "succeeded"
	DeclarationOperationFailed     DeclarationOperationState = "failed"
	DeclarationOperationSuperseded DeclarationOperationState = "superseded"
)

type DeclarationActionResult struct {
	ActionID  string                    `json:"action_id"`
	State     DeclarationOperationState `json:"state"`
	EffectRef string                    `json:"effect_ref,omitempty"`
	Error     *DeclarationError         `json:"error,omitempty"`
}

type DeclarationResult struct {
	SchemaVersion string                    `json:"schema_version"`
	OperationID   string                    `json:"operation_id"`
	PlanID        string                    `json:"plan_id"`
	Target        DeclarationTarget         `json:"target"`
	Revision      string                    `json:"revision"`
	State         DeclarationOperationState `json:"state"`
	Actions       []DeclarationActionResult `json:"actions"`
	Readiness     DeclarationReadiness      `json:"readiness"`
	Errors        []DeclarationError        `json:"errors"`
	SupersededBy  string                    `json:"superseded_by,omitempty"`
}

type DeclarationResourceStatus struct {
	Binding   DeclarationBinding   `json:"binding"`
	Readiness DeclarationReadiness `json:"readiness"`
	Errors    []DeclarationError   `json:"errors"`
}

type DeclarationStatus struct {
	Development   *DeclarationDevelopmentContext            `json:"development,omitempty"`
	SchemaVersion string                                    `json:"schema_version"`
	Target        DeclarationTarget                         `json:"target"`
	Revision      string                                    `json:"revision"`
	Operation     *DeclarationResult                        `json:"operation,omitempty"`
	Resources     map[ResourceKey]DeclarationResourceStatus `json:"resources"`
	Readiness     DeclarationReadiness                      `json:"readiness"`
	Errors        []DeclarationError                        `json:"errors"`
	ObservedAt    string                                    `json:"observed_at"`
}

// A compact, source-declared context alongside technical readiness. Detailed
// captured evidence belongs to the project Provenance snapshot, not this view.
type DeclarationDevelopmentContext struct {
	Posture      string `json:"posture"`
	SourceDigest string `json:"source_digest,omitempty"`
	Purpose      string `json:"purpose,omitempty"`
	CurrentFocus string `json:"current_focus,omitempty"`
	Progress     string `json:"progress,omitempty"`
	Blockers     string `json:"blockers,omitempty"`
	NextAction   string `json:"next_action,omitempty"`
	SnapshotRef  string `json:"snapshot_ref,omitempty"`
}

type DeclarationErrorCode string

const (
	DeclarationInvalid           DeclarationErrorCode = "declaration.invalid"
	DeclarationUnsupported       DeclarationErrorCode = "declaration.unsupported"
	DeclarationIdentityConflict  DeclarationErrorCode = "declaration.identity_conflict"
	DeclarationReferenceMissing  DeclarationErrorCode = "declaration.reference_missing"
	DeclarationTargetUnavailable DeclarationErrorCode = "declaration.target_unavailable"
	DeclarationPlanStale         DeclarationErrorCode = "declaration.plan_stale"
	DeclarationUnauthorized      DeclarationErrorCode = "declaration.unauthorized"
	DeclarationApprovalRequired  DeclarationErrorCode = "declaration.approval_required"
	DeclarationOperationConflict DeclarationErrorCode = "declaration.operation_conflict"
	DeclarationOwnerFailed       DeclarationErrorCode = "declaration.owner_failed"
)

// DeclarationError preserves the typed owner cause and known completed effects.
// Message must be redacted. Retryable never means bypass authority or replan.
type DeclarationError struct {
	Code             DeclarationErrorCode        `json:"code"`
	Resource         ResourceKey                 `json:"resource,omitempty"`
	ActionID         string                      `json:"action_id,omitempty"`
	Field            string                      `json:"field,omitempty"`
	Message          string                      `json:"message"`
	CauseCode        string                      `json:"cause_code,omitempty"`
	Retryable        bool                        `json:"retryable"`
	CompletedEffects []string                    `json:"completed_effects"`
	Preflight        []DeclarationPreflightIssue `json:"preflight,omitempty"`
}

// Preflight reports independent prerequisites, not an executable partial plan.
// State distinguishes missing setup from unavailable evidence and unsupported
// product behavior. Messages contain guidance, never raw dependency errors.
type DeclarationPreflightIssue struct {
	Resource ResourceKey `json:"resource"`
	Field    string      `json:"field"`
	State    string      `json:"state"`
	Code     string      `json:"code"`
	Message  string      `json:"message"`
}
