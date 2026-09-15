package backupstrategy

import (
	"context"
	"errors"
	"fmt"
)

const inventorySchemaVersion = "loom.backupstrategy.inventory.v1"

var (
	ErrInvalidConfig          = errors.New("invalid backup inventory configuration")
	ErrInvalidRemoteInventory = errors.New("invalid remote backup inventory")
	ErrInventoryChanged       = errors.New("backup inventory changed")
)

type ComponentClass string

const (
	ComponentSuccessfulGeneration    ComponentClass = "successful_generation"
	ComponentIncompleteStaging       ComponentClass = "incomplete_staging"
	ComponentSharedStore             ComponentClass = "shared_store"
	ComponentOperationalPackage      ComponentClass = "operational_package"
	ComponentUnknown                 ComponentClass = "unknown_entry"
	ComponentProtectedMilestone      ComponentClass = "protected_milestone"
	ComponentImmutableFailedEvidence ComponentClass = "immutable_failed_evidence"
)

func (class ComponentClass) valid() bool {
	switch class {
	case ComponentSuccessfulGeneration,
		ComponentIncompleteStaging,
		ComponentSharedStore,
		ComponentOperationalPackage,
		ComponentUnknown,
		ComponentProtectedMilestone,
		ComponentImmutableFailedEvidence:
		return true
	default:
		return false
	}
}

func (class ComponentClass) protectedByDefault() bool {
	switch class {
	case ComponentSharedStore, ComponentOperationalPackage, ComponentProtectedMilestone, ComponentImmutableFailedEvidence:
		return true
	default:
		return false
	}
}

type ReclaimPosture string

const (
	ReclaimCandidate ReclaimPosture = "candidate"
	ReclaimProtected ReclaimPosture = "protected"
	ReclaimBlocked   ReclaimPosture = "blocked"
	ReclaimRetained  ReclaimPosture = "retained"
)

type FindingCode string

const (
	FindingSymlink              FindingCode = "symlink"
	FindingSpecialFile          FindingCode = "special_file"
	FindingUnreadable           FindingCode = "unreadable"
	FindingMetadataUnavailable  FindingCode = "metadata_unavailable"
	FindingMissingComponent     FindingCode = "missing_component"
	FindingInconsistentIdentity FindingCode = "inconsistent_inode_identity"
)

const (
	BlockerUnknownEntry  = "unknown_entry"
	BlockerActiveStaging = "active_staging"
	BlockerUnsafeEntry   = "unsafe_component"
)

type EntryKind string

const (
	EntryRegularFile EntryKind = "regular_file"
	EntryDirectory   EntryKind = "directory"
	EntrySymlink     EntryKind = "symlink"
	EntrySpecial     EntryKind = "special"
)

// ByteAccounting uses lstat logical size and st_blocks allocation. Unique and
// shared partition AllocatedBytes. The posture fields can overlap that
// partition: one inode can be shared and protected, for example.
type ByteAccounting struct {
	LogicalBytes              uint64 `json:"logical_bytes"`
	AllocatedBytes            uint64 `json:"allocated_bytes"`
	UniqueBytes               uint64 `json:"unique_bytes"`
	SharedBytes               uint64 `json:"shared_bytes"`
	StagingBytes              uint64 `json:"staging_bytes"`
	UnknownBytes              uint64 `json:"unknown_bytes"`
	ProtectedBytes            uint64 `json:"protected_bytes"`
	PotentialReclaimableBytes uint64 `json:"potential_reclaimable_bytes"`
	ReclaimableBytes          uint64 `json:"reclaimable_bytes"`
}

type Finding struct {
	Code         FindingCode `json:"code"`
	RelativePath string      `json:"relative_path"`
	Detail       string      `json:"detail"`
}

type EntryInventory struct {
	RelativePath     string    `json:"relative_path"`
	Kind             EntryKind `json:"kind"`
	Mode             uint32    `json:"mode"`
	LogicalBytes     uint64    `json:"logical_bytes"`
	AllocatedBytes   uint64    `json:"allocated_bytes"`
	DeviceID         uint64    `json:"device_id"`
	Inode            uint64    `json:"inode"`
	LinkCount        uint64    `json:"link_count"`
	ModifiedUnixNano int64     `json:"modified_unix_nano"`
	ChangedUnixNano  int64     `json:"changed_unix_nano"`
	IdentityKnown    bool      `json:"identity_known"`
}

type ComponentInventory struct {
	RelativePath   string           `json:"relative_path"`
	Class          ComponentClass   `json:"class"`
	Protected      bool             `json:"protected"`
	Safe           bool             `json:"safe"`
	RestorePoint   bool             `json:"restore_point"`
	ReclaimPosture ReclaimPosture   `json:"reclaim_posture"`
	Accounting     ByteAccounting   `json:"accounting"`
	Entries        []EntryInventory `json:"entries"`
	Findings       []Finding        `json:"findings"`
}

type ComponentSpec struct {
	// RelativePath is exactly one direct child of LocalRoot. Nested or escaping
	// declarations are rejected so unknown top-level entries remain visible.
	RelativePath string
	Class        ComponentClass
	Protected    bool
}

type Config struct {
	LocalRoot  string
	Components []ComponentSpec
}

// RemoteArchiveSummary is declared by an injected adapter. StoredBytes is an
// archive-scoped Borg measurement and is deliberately not summed into the
// repository allocation because archive storage can be deduplicated/shared.
type RemoteArchiveSummary struct {
	Reference    string `json:"reference"`
	Class        string `json:"class"`
	LogicalBytes uint64 `json:"logical_bytes"`
	StoredBytes  uint64 `json:"stored_bytes"`
	Protected    bool   `json:"protected"`
}

type RemoteInventory struct {
	RepositoryID string                 `json:"repository_id"`
	Accounting   ByteAccounting         `json:"accounting"`
	Archives     []RemoteArchiveSummary `json:"archives"`
}

type RemoteInventorySource interface {
	Inventory(context.Context) (RemoteInventory, error)
}

type Inventory struct {
	SchemaVersion   string               `json:"schema_version"`
	LocalRoot       string               `json:"local_root"`
	LocalRootEntry  EntryInventory       `json:"local_root_entry"`
	Components      []ComponentInventory `json:"components"`
	LocalTotals     ByteAccounting       `json:"local_totals"`
	Remote          *RemoteInventory     `json:"remote,omitempty"`
	CleanupEligible bool                 `json:"cleanup_eligible"`
	CleanupBlockers []string             `json:"cleanup_blockers"`
	Digest          string               `json:"digest"`
}

type InventoryChangedError struct {
	Expected string
	Actual   string
}

func (err *InventoryChangedError) Error() string {
	return fmt.Sprintf("%v: expected %s, observed %s", ErrInventoryChanged, err.Expected, err.Actual)
}

func (err *InventoryChangedError) Unwrap() error {
	return ErrInventoryChanged
}
