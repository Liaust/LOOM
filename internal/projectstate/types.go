package projectstate

import (
	"context"
	"errors"
	"time"

	"loom.local/loom/internal/projects"
)

const (
	SchemaVersion = "loom.project_repository_state.v1"

	SourcePostureRegistered    SourcePosture = "registered"
	SourcePostureNotRegistered SourcePosture = "not_registered"

	DevelopmentStateNotEnabled  DevelopmentStatePosture = "not_enabled"
	DevelopmentStateNotObserved DevelopmentStatePosture = "not_observed"
	DevelopmentStateEnabled     DevelopmentStatePosture = "enabled"
	DevelopmentStateInvalid     DevelopmentStatePosture = "invalid"
	DevelopmentStateMismatch    DevelopmentStatePosture = "mismatched"

	GitHeadObserved GitHeadPosture = "observed"
	GitHeadUnborn   GitHeadPosture = "unborn"

	GitDefaultBranchObserved    GitDefaultBranchPosture = "observed"
	GitDefaultBranchNotObserved GitDefaultBranchPosture = "not_observed"

	ProblemSeverityWarning ProblemSeverity = "warning"
	ProblemSeverityError   ProblemSeverity = "error"
)

var (
	ErrInvalidObservationConfiguration = errors.New("invalid project observation configuration")
	ErrProjectStateUnavailable         = errors.New("project repository state unavailable")
	ErrPathEscape                      = errors.New("project repository path escapes its registered root")
	ErrPathInvalid                     = errors.New("invalid project repository path")
	ErrGitUnavailable                  = errors.New("git observation unavailable")
)

type SourcePosture string
type DevelopmentStatePosture string
type GitHeadPosture string
type GitDefaultBranchPosture string
type ProblemSeverity string

type ProjectProjection struct {
	SchemaVersion string                    `json:"schema_version"`
	Project       ProjectIdentityProjection `json:"project"`
	Facets        []FacetProjection         `json:"facets"`
	Source        SourceProjection          `json:"source"`
	Development   ProjectDevelopmentState   `json:"development"`
	Observation   ObservationSummary        `json:"observation"`
	Members       []RepositoryProjection    `json:"members"`
	ObservedAt    time.Time                 `json:"observed_at"`
}

type ProjectDevelopmentPosture string

const (
	ProjectDevelopmentReady         ProjectDevelopmentPosture = "ready"
	ProjectDevelopmentMissing       ProjectDevelopmentPosture = "missing"
	ProjectDevelopmentUnavailable   ProjectDevelopmentPosture = "unavailable"
	ProjectDevelopmentInvalid       ProjectDevelopmentPosture = "invalid"
	ProjectDevelopmentMismatch      ProjectDevelopmentPosture = "mismatched"
	ProjectDevelopmentArchived      ProjectDevelopmentPosture = "archived"
	ProjectDevelopmentPartial       ProjectDevelopmentPosture = "partial"
	ProjectDevelopmentNotRegistered ProjectDevelopmentPosture = "not_registered"
)

// ProjectDevelopmentState captures source declarations, not accepted semantic
// records. SourceRevision is the registry binding revision; snapshot owners
// allocate their own observation revision, including when content reverts.
type ProjectDevelopmentState struct {
	Posture        ProjectDevelopmentPosture    `json:"posture"`
	ReasonCode     string                       `json:"reason_code,omitempty"`
	ProjectID      string                       `json:"project_id"`
	OwnerNode      string                       `json:"owner_node,omitempty"`
	RootDigest     string                       `json:"root_digest,omitempty"`
	SourceDigest   string                       `json:"source_digest,omitempty"`
	SourceRevision int64                        `json:"source_revision,omitempty"`
	Complete       bool                         `json:"complete"`
	OmittedFiles   int                          `json:"omitted_files"`
	CapturedBytes  int                          `json:"captured_bytes"`
	Purpose        string                       `json:"purpose,omitempty"`
	CurrentFocus   string                       `json:"current_focus,omitempty"`
	Progress       string                       `json:"progress,omitempty"`
	Blockers       string                       `json:"blockers,omitempty"`
	NextAction     string                       `json:"next_action,omitempty"`
	Roadmap        string                       `json:"roadmap,omitempty"`
	Structure      string                       `json:"structure,omitempty"`
	Features       []ProjectDevelopmentFeature  `json:"features"`
	Decisions      []ProjectDevelopmentDecision `json:"decisions"`
	Documents      []ProjectDevelopmentDocument `json:"documents"`
	Git            *ProjectDevelopmentGit       `json:"git,omitempty"`
}

// Hash covers the entire bounded source file. Excerpt is captured from those
// exact bytes; Truncated distinguishes excerpts from full-file reconstruction.
// Posture is present, missing, unreadable, malformed, unsafe, or omitted.
type ProjectDevelopmentDocument struct {
	Path       string `json:"path"`
	Hash       string `json:"hash,omitempty"`
	Excerpt    string `json:"excerpt,omitempty"`
	Posture    string `json:"posture"`
	ReasonCode string `json:"reason_code,omitempty"`
	SizeBytes  int64  `json:"size_bytes"`
	Truncated  bool   `json:"truncated"`
}

type ProjectDevelopmentFeature struct {
	Slug          string `json:"slug"`
	Title         string `json:"title,omitempty"`
	Status        string `json:"status,omitempty"`
	NextAction    string `json:"next_action,omitempty"`
	BlockedReason string `json:"blocked_reason,omitempty"`
	Path          string `json:"path"`
	Hash          string `json:"hash"`
}

type ProjectDevelopmentDecision struct {
	ID     string `json:"id,omitempty"`
	Title  string `json:"title,omitempty"`
	Status string `json:"status,omitempty"`
	Date   string `json:"date,omitempty"`
	Path   string `json:"path"`
	Hash   string `json:"hash"`
}

type ProjectDevelopmentGit struct {
	// Commit is populated only when every captured source matches that tree.
	Commit  string `json:"commit,omitempty"`
	Head    string `json:"head,omitempty"`
	Posture string `json:"posture"` // committed, uncommitted, or unavailable
}

type ProjectDevelopmentInput struct {
	ProjectRoot    string
	ProjectID      string
	OwnerNode      string
	LocalNode      string
	Lifecycle      string
	SourceRevision int64
	ObserveGit     bool
}

type ProjectDevelopmentInspector interface {
	Inspect(context.Context, ProjectDevelopmentInput) ProjectDevelopmentState
}

type ProjectIdentityProjection struct {
	ProjectID       string    `json:"project_id"`
	ProjectScopeID  string    `json:"project_scope_id"`
	ProjectScopeKey string    `json:"project_scope_key"`
	Slug            string    `json:"slug"`
	Name            string    `json:"name"`
	Lifecycle       string    `json:"lifecycle"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type FacetProjection struct {
	Key         string `json:"key"`
	Folder      string `json:"folder,omitempty"`
	Enabled     bool   `json:"enabled"`
	Present     bool   `json:"present"`
	Placeholder bool   `json:"placeholder"`
	Status      string `json:"status"`
}

type SourceProjection struct {
	Posture                      SourcePosture `json:"posture"`
	ProjectContractSchemaVersion string        `json:"project_contract_schema_version,omitempty"`
	ReposContractSchemaVersion   string        `json:"repos_contract_schema_version,omitempty"`
	OwnerNode                    string        `json:"owner_node,omitempty"`
	SemanticDigest               string        `json:"semantic_digest,omitempty"`
	LocationDigest               string        `json:"location_digest,omitempty"`
	SourceRevision               int64         `json:"source_revision,omitempty"`
	RegisteredAt                 *time.Time    `json:"registered_at,omitempty"`
}

type ObservationSummary struct {
	Posture           projects.ProjectRepositoryObservationPosture `json:"posture"`
	MemberCount       int                                          `json:"member_count"`
	Observed          int                                          `json:"observed"`
	NotObserved       int                                          `json:"not_observed"`
	RemoteUnavailable int                                          `json:"remote_unavailable"`
}

type RepositoryProjection struct {
	RepositoryID             string                                       `json:"repository_id"`
	RepositoryOwnerProjectID string                                       `json:"repository_owner_project_id"`
	Key                      string                                       `json:"key"`
	Role                     projects.ProjectRepositoryRole               `json:"role"`
	RelativeSource           string                                       `json:"relative_source"`
	StateRoot                string                                       `json:"state_root,omitempty"`
	MembershipLifecycle      projects.RepositoryLifecycleStatus           `json:"membership_lifecycle"`
	RepositoryLifecycle      projects.RepositoryLifecycleStatus           `json:"repository_lifecycle"`
	SourceBindingDigest      string                                       `json:"source_binding_digest"`
	ObservationPosture       projects.ProjectRepositoryObservationPosture `json:"observation_posture"`
	ReasonCode               string                                       `json:"reason_code,omitempty"`
	ObservedAt               *time.Time                                   `json:"observed_at,omitempty"`
	DevelopmentState         DevelopmentStateProjection                   `json:"development_state"`
	Git                      *GitProjection                               `json:"git,omitempty"`
	Problems                 []Problem                                    `json:"problems"`
}

type DevelopmentStateProjection struct {
	Posture      DevelopmentStatePosture `json:"posture"`
	RelativePath string                  `json:"relative_path,omitempty"`
	SourceDigest string                  `json:"source_digest,omitempty"`
	ReasonCode   string                  `json:"reason_code,omitempty"`
}

type GitProjection struct {
	LocalIdentityDigest           string                  `json:"local_identity_digest"`
	Worktree                      bool                    `json:"worktree"`
	WorktreeCanonicalProjectState bool                    `json:"worktree_canonical_project_state"`
	Bare                          bool                    `json:"bare"`
	RootMatchesMember             bool                    `json:"root_matches_member"`
	CurrentBranch                 string                  `json:"current_branch,omitempty"`
	Detached                      bool                    `json:"detached"`
	DefaultBranch                 string                  `json:"default_branch,omitempty"`
	DefaultBranchPosture          GitDefaultBranchPosture `json:"default_branch_posture"`
	Head                          string                  `json:"head,omitempty"`
	HeadPosture                   GitHeadPosture          `json:"head_posture"`
	Upstream                      string                  `json:"upstream,omitempty"`
	Ahead                         int                     `json:"ahead"`
	Behind                        int                     `json:"behind"`
	AheadBehindObserved           bool                    `json:"ahead_behind_observed"`
	Dirty                         GitDirtyProjection      `json:"dirty"`
}

type GitDirtyProjection struct {
	Dirty            bool `json:"dirty"`
	TrackedChanges   bool `json:"tracked_changes"`
	UntrackedChanges bool `json:"untracked_changes"`
	Conflicts        bool `json:"conflicts"`
	SubmoduleChanges bool `json:"submodule_changes"`
}

type Problem struct {
	Code     string          `json:"code"`
	Severity ProblemSeverity `json:"severity"`
	Summary  string          `json:"summary"`
}

type Reader interface {
	ReadProjectRepositoryState(context.Context, string) (projects.ProjectRepositoryReadModel, error)
}

type GitObserver interface {
	Observe(context.Context, string) (GitProjection, error)
}

type PathResolver interface {
	ResolveWithin(string, string) (ResolvedPath, error)
}

type DevelopmentStateInspector interface {
	Inspect(context.Context, DevelopmentStateInput) DevelopmentStateProjection
}

type Clock interface {
	Now() time.Time
}

type ResolvedPath struct {
	Path      string
	Exists    bool
	Directory bool
}

type DevelopmentStateInput struct {
	MemberRoot   string
	StateRoot    string
	ProjectID    string
	RepositoryID string
}
