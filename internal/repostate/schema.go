// Package repostate defines the portable, Git-tracked repository development
// state contract. Parsing, validation, extraction, and migration are added by
// later feature slices; this package currently freezes only the v1 schema and
// compatibility rules those implementations must consume.
package repostate

const (
	StateRoot               = ".repo"
	RepositoryManifestPath  = ".repo/repo.yaml"
	RepositoryManifestKind  = "loom.repository_state"
	RepositorySchemaVersion = "repo.state.v1"
	RepositoryIDPrefix      = "repo"
	ProjectIDPrefix         = "project"
)

// RepositoryRole is the repository's role in its owning project. A reference
// is a relationship from another project and is intentionally not a valid
// self-declared repository role.
type RepositoryRole string

const (
	RepositoryRolePrimary   RepositoryRole = "primary"
	RepositoryRoleComponent RepositoryRole = "component"
)

// RepositoryManifest is the complete machine-readable identity and discovery
// declaration at .repo/repo.yaml. It contains no host or worktree paths.
type RepositoryManifest struct {
	Kind          string                 `json:"kind" yaml:"kind"`
	SchemaVersion string                 `json:"schema_version" yaml:"schema_version"`
	Repository    RepositoryIdentity     `json:"repository" yaml:"repository"`
	OwnerProject  OwnerProjectBacklink   `json:"owner_project" yaml:"owner_project"`
	Branches      RepositoryBranches     `json:"branches,omitempty" yaml:"branches,omitempty"`
	Source        RepositorySourcePolicy `json:"source" yaml:"source"`
}

type RepositoryIdentity struct {
	ID      string         `json:"id" yaml:"id"`
	Name    string         `json:"name" yaml:"name"`
	Aliases []string       `json:"aliases" yaml:"aliases"`
	Role    RepositoryRole `json:"role" yaml:"role"`
	Purpose string         `json:"purpose" yaml:"purpose"`
	Topics  []string       `json:"topics" yaml:"topics"`
}

type OwnerProjectBacklink struct {
	ID   string `json:"id" yaml:"id"`
	Slug string `json:"slug" yaml:"slug"`
}

// Stable is the integration target used by the repository workflow. Default
// is the repository's Git default branch. Either may be omitted when unknown.
type RepositoryBranches struct {
	Stable  string `json:"stable,omitempty" yaml:"stable,omitempty"`
	Default string `json:"default,omitempty" yaml:"default,omitempty"`
}

// RepositorySourcePolicy freezes the portable source boundary. Tracking must
// be git and StateRoot must be .repo in v1.
type RepositorySourcePolicy struct {
	Tracking  string `json:"tracking" yaml:"tracking"`
	StateRoot string `json:"state_root" yaml:"state_root"`
}

// ObjectKind identifies a portable repository-development lifecycle object.
type ObjectKind string

const (
	ObjectKindFuture     ObjectKind = "future"
	ObjectKindInitiative ObjectKind = "initiative"
	ObjectKindFeature    ObjectKind = "feature"
	ObjectKindDecision   ObjectKind = "decision"
	ObjectKindRelease    ObjectKind = "release"
)

type LifecycleSchema struct {
	Kind             ObjectKind
	SchemaVersion    string
	ManifestPattern  string
	StableIDPattern  string
	RequiredFields   []string
	OptionalFields   []string
	AllowedStatuses  []string
	RequiredFiles    []string
	ConditionalFiles []string
}

type PathRequirement string

const (
	PathRequired    PathRequirement = "required"
	PathOptional    PathRequirement = "optional"
	PathConditional PathRequirement = "conditional"
)

type PathSchema struct {
	Pattern     string
	Requirement PathRequirement
	Purpose     string
}

type CompatibilityDisposition string

const (
	DispositionPreserve CompatibilityDisposition = "preserve"
	DispositionRename   CompatibilityDisposition = "rename"
	DispositionSplit    CompatibilityDisposition = "split"
	DispositionArchive  CompatibilityDisposition = "archive"
	DispositionDrop     CompatibilityDisposition = "drop"
)

type CompatibilityEntry struct {
	ProjectSource string
	RepoTarget    string
	Disposition   CompatibilityDisposition
	Rule          string
}

// SourcePosture is field-level provenance for deterministic extraction. These
// values never mean accepted semantic context.
type SourcePosture string

const (
	PostureDeclared SourcePosture = "declared"
	PostureObserved SourcePosture = "observed"
	PostureDerived  SourcePosture = "derived"
	PostureMissing  SourcePosture = "missing"
	PostureStale    SourcePosture = "stale"
	PostureInvalid  SourcePosture = "invalid"
)

type DiscoveryField struct {
	Name            string
	ValueShape      string
	PrimaryPosture  SourcePosture
	Source          string
	AllowedPostures []SourcePosture
	Rule            string
}

type SourceDigestPolicy struct {
	Algorithm string
	InputSet  string
	Ordering  string
	Framing   string
	Output    string
}

type FreshnessPolicy struct {
	ValueShape   string
	TrackedState []string
	StaleReasons []string
	Rule         string
}

type TrackingStatus string

const (
	TrackingNotEnabled           TrackingStatus = "not_enabled"
	TrackingMalformed            TrackingStatus = "malformed"
	TrackingStaleVersion         TrackingStatus = "stale_version"
	TrackingMembershipUnresolved TrackingStatus = "membership_unresolved"
	TrackingMismatchedRepository TrackingStatus = "mismatched_repository"
	TrackingMismatchedOwner      TrackingStatus = "mismatched_owner"
	TrackingMismatchedMembership TrackingStatus = "mismatched_membership"
	TrackingValid                TrackingStatus = "valid"
)

// MembershipRule order is precedence order. The future validator must return
// the first applicable classification without mutating either source.
type MembershipRule struct {
	Code       string
	Status     TrackingStatus
	Rule       string
	Precedence int
}

type VersionPolicy struct {
	SupportedRepositoryVersions []string
	CompatibilityRule           string
	UnknownVersionRule          string
	ChangeRule                  string
	UpgradeRule                 string
	UnknownFieldRule            string
}

type Contract struct {
	RootPaths             []PathSchema
	LifecycleSchemas      []LifecycleSchema
	CompatibilityMap      []CompatibilityEntry
	DiscoveryFields       []DiscoveryField
	SourceDigestPolicy    SourceDigestPolicy
	FreshnessPolicy       FreshnessPolicy
	MembershipRules       []MembershipRule
	VersionPolicy         VersionPolicy
	PortableTrackingRules []string
	ForbiddenPortableData []string
	AcceptedContextRule   string
}
