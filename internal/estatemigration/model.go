// Package estatemigration inventories explicitly configured source roots and
// seals reviewed subsets into deterministic migration manifests. Publication
// is limited to verified staging and atomic promotion; byte transport remains
// behind an injected adapter and registration, cleanup, and source mutation
// are deliberately absent.
package estatemigration

import (
	"context"
	"errors"
	"io/fs"

	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/filesystemmeta"
)

const InventorySchemaVersion = "loom.digital_estate_inventory.v1"

var (
	ErrInvalidConfig    = errors.New("invalid digital estate inventory configuration")
	ErrBudgetExhausted  = errors.New("digital estate inventory budget exhausted")
	ErrSourceChanged    = errors.New("digital estate source changed during inventory")
	ErrGitUnavailable   = errors.New("digital estate Git observation unavailable")
	ErrMetadataOverflow = errors.New("digital estate metadata accounting overflow")
)

type Domain string

const (
	DomainProjects     Domain = "projects"
	DomainRepositories Domain = "repositories"
	DomainNotes        Domain = "notes"
	DomainDocuments    Domain = "documents"
	DomainAgentSources Domain = "agent_sources"
)

func (domain Domain) valid() bool {
	switch domain {
	case DomainProjects, DomainRepositories, DomainNotes, DomainDocuments, DomainAgentSources:
		return true
	default:
		return false
	}
}

// RootSpec is input-only configuration. Path is deliberately excluded from
// JSON so a caller cannot accidentally serialize an unredacted host path.
type RootSpec struct {
	ID     string       `json:"id"`
	Path   string       `json:"-"`
	Domain Domain       `json:"domain"`
	Ignore IgnoreConfig `json:"ignore"`
}

type IgnoreConfig struct {
	Profile           filepolicy.Profile `json:"profile,omitempty"`
	DiscoverUserRules bool               `json:"discover_user_rules"`
	ContractIncludes  []string           `json:"contract_includes,omitempty"`
	ContractExcludes  []string           `json:"contract_excludes,omitempty"`
}

type Bounds struct {
	MaxRoots          int   `json:"max_roots"`
	MaxEntries        int   `json:"max_entries"`
	MaxDepth          int   `json:"max_depth"`
	MaxRepositories   int   `json:"max_repositories"`
	MaxGitRefs        int   `json:"max_git_refs"`
	MaxGitUntracked   int   `json:"max_git_untracked"`
	MaxGitSubmodules  int   `json:"max_git_submodules"`
	MaxGitWorktrees   int   `json:"max_git_worktrees"`
	MaxGitRemotes     int   `json:"max_git_remotes"`
	MaxGitOutputBytes int   `json:"max_git_output_bytes"`
	MaxPolicyBytes    int64 `json:"max_policy_bytes"`
}

type Config struct {
	Roots  []RootSpec `json:"roots"`
	Bounds Bounds     `json:"bounds"`
}

type Inventory struct {
	SchemaVersion    string                  `json:"schema_version"`
	Bounds           Bounds                  `json:"bounds"`
	Roots            []RootInventory         `json:"roots"`
	Repositories     []RepositoryObservation `json:"repositories"`
	Overlaps         []RootOverlap           `json:"overlaps"`
	DuplicateObjects []DuplicateObject       `json:"duplicate_objects"`
	Totals           Accounting              `json:"totals"`
	Digest           string                  `json:"digest"`
}

type RootInventory struct {
	ID                string      `json:"id"`
	Locator           string      `json:"locator"`
	Domain            Domain      `json:"domain"`
	PolicyFingerprint string      `json:"policy_fingerprint"`
	Entries           []Entry     `json:"entries"`
	Summary           RootSummary `json:"summary"`
}

type RootSummary struct {
	EntryCount           int        `json:"entry_count"`
	IncludedCount        int        `json:"included_count"`
	IgnoredCount         int        `json:"ignored_count"`
	RegularFileCount     int        `json:"regular_file_count"`
	DirectoryCount       int        `json:"directory_count"`
	SymlinkCount         int        `json:"symlink_count"`
	HardLinkCount        int        `json:"hard_link_count"`
	SpecialFileCount     int        `json:"special_file_count"`
	MetadataRiskCount    int        `json:"metadata_risk_count"`
	MetadataUnknownCount int        `json:"metadata_unknown_count"`
	RepositoryCount      int        `json:"repository_count"`
	Accounting           Accounting `json:"accounting"`
}

// Accounting distinguishes path-level totals from physical allocation. A
// hard-linked or overlapping inode is counted once in UniqueAllocatedBytes.
type Accounting struct {
	LogicalBytes                    uint64 `json:"logical_bytes"`
	AllocatedBytes                  uint64 `json:"allocated_bytes"`
	UniqueAllocatedBytes            uint64 `json:"unique_allocated_bytes"`
	DuplicateAllocatedBytes         uint64 `json:"duplicate_allocated_bytes"`
	IncludedLogicalBytes            uint64 `json:"included_logical_bytes"`
	IncludedAllocatedBytes          uint64 `json:"included_allocated_bytes"`
	IncludedUniqueAllocatedBytes    uint64 `json:"included_unique_allocated_bytes"`
	IncludedDuplicateAllocatedBytes uint64 `json:"included_duplicate_allocated_bytes"`
	UnknownAllocationCount          int    `json:"unknown_allocation_count"`
}

type Entry struct {
	Locator        string          `json:"locator"`
	Domain         Domain          `json:"domain"`
	Kind           string          `json:"kind"`
	ObjectIdentity string          `json:"object_identity,omitempty"`
	LogicalBytes   uint64          `json:"logical_bytes"`
	AllocatedBytes *uint64         `json:"allocated_bytes,omitempty"`
	LinkCount      *uint64         `json:"link_count,omitempty"`
	HardLink       bool            `json:"hard_link"`
	SymlinkTarget  string          `json:"symlink_target,omitempty"`
	Metadata       MetadataPosture `json:"metadata"`
	Ignore         IgnoreDecision  `json:"ignore"`
}

type MetadataPosture struct {
	Known            bool     `json:"known"`
	Mode             uint32   `json:"mode"`
	Executable       bool     `json:"executable"`
	Sparse           bool     `json:"sparse"`
	HasXattrs        bool     `json:"has_xattrs"`
	XattrNames       []string `json:"xattr_names"`
	HasACL           bool     `json:"has_acl"`
	HasResourceFork  bool     `json:"has_resource_fork"`
	HasFinderTags    bool     `json:"has_finder_tags"`
	HasQuarantine    bool     `json:"has_quarantine"`
	Package          bool     `json:"package"`
	PackageKind      string   `json:"package_kind,omitempty"`
	PermissionDenied bool     `json:"permission_denied"`
	Risks            []string `json:"risks"`
}

type IgnoreDecision struct {
	Included      bool                    `json:"included"`
	Profile       filepolicy.Profile      `json:"profile"`
	RuleCategory  filepolicy.RuleCategory `json:"rule_category"`
	Pattern       string                  `json:"pattern,omitempty"`
	PolicyVersion string                  `json:"policy_version"`
	Source        string                  `json:"source,omitempty"`
	SourceLine    int                     `json:"source_line,omitempty"`
	Negated       bool                    `json:"negated,omitempty"`
}

type RootOverlap struct {
	AncestorRootID   string `json:"ancestor_root_id"`
	DescendantRootID string `json:"descendant_root_id"`
}

type DuplicateObject struct {
	ObjectIdentity string   `json:"object_identity"`
	Kind           string   `json:"kind"`
	Locators       []string `json:"locators"`
}

type RepositoryObservation struct {
	Locator        string         `json:"locator"`
	IdentityDigest string         `json:"identity_digest"`
	Bare           bool           `json:"bare"`
	RemoteDigests  []string       `json:"remote_digests"`
	Head           GitHead        `json:"head"`
	Refs           []GitRef       `json:"refs"`
	Dirty          bool           `json:"dirty"`
	Untracked      []string       `json:"untracked"`
	Submodules     []GitSubmodule `json:"submodules"`
	LinkedWorktree bool           `json:"linked_worktree"`
	Worktrees      []GitWorktree  `json:"worktrees"`
}

type GitHead struct {
	Commit   string `json:"commit,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Detached bool   `json:"detached"`
	Unborn   bool   `json:"unborn"`
}

type GitRef struct {
	Name   string `json:"name"`
	Object string `json:"object"`
}

type GitSubmodule struct {
	Path   string `json:"path"`
	Commit string `json:"commit"`
	State  string `json:"state"`
}

type GitWorktree struct {
	Locator  string `json:"locator"`
	Head     string `json:"head,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Detached bool   `json:"detached"`
	Bare     bool   `json:"bare"`
	Prunable bool   `json:"prunable"`
}

// Filesystem and FilesystemRoot form the read-only injected filesystem
// boundary. Implementations must not follow a discovered symlink while walking.
type Filesystem interface {
	OpenRoot(string) (FilesystemRoot, error)
}

type FilesystemRoot interface {
	CanonicalPath() string
	Lstat(string) (fs.FileInfo, error)
	ReadDir(string) ([]fs.DirEntry, error)
	Readlink(string) (string, error)
	Metadata(string) (filesystemmeta.Observation, error)
	Close() error
}

type Policy interface {
	Resolve(string, bool) (filepolicy.Resolution, error)
	Fingerprint() string
}

type PolicyFactory interface {
	New(context.Context, string, IgnoreConfig, Bounds) (Policy, error)
}

type GitAdapter interface {
	Observe(context.Context, string, Bounds) (RawGitObservation, error)
}

// RawGitObservation is allowed to contain host paths only across the injected
// adapter boundary. Service.Inventory always redacts it before returning.
type RawGitObservation struct {
	CommonDirectory string
	RemoteDigests   []string
	Bare            bool
	Head            GitHead
	Refs            []GitRef
	Dirty           bool
	UntrackedPaths  []string
	Submodules      []RawGitSubmodule
	LinkedWorktree  bool
	Worktrees       []RawGitWorktree
}

type RawGitSubmodule struct {
	Path   string
	Commit string
	State  string
}

type RawGitWorktree struct {
	Path     string
	Head     string
	Branch   string
	Detached bool
	Bare     bool
	Prunable bool
}
