package storagearchive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	WorkspaceArchiveOperationSchemaVersion = "storage.workspace_archive_operation.v1"
	WorkspaceArchivePlanSchemaVersion      = "storage.workspace_archive_plan.v1"
	WorkspaceArchiveManifestSchemaVersion  = "storage.workspace_archive_manifest.v1"
	WorkspaceArchiveStatusSchemaVersion    = "storage.workspace_archive_status.v1"
	WorkspaceArchiveFindingSchemaVersion   = "storage.workspace_archive_finding.v1"
	WorkspaceLifecycleEventSchemaVersion   = "storage.workspace_lifecycle_event.v1"
	NoFollowInventorySchemaVersion         = "storage.workspace_archive_inventory.v1"

	PhysicalWorkspaceMoveEvidence = "physical_workspace_move"
	WorkspaceLifecycleEventKind   = "workspace.lifecycle_changed"

	maxWorkspaceArchiveReasonBytes  = 1024
	maxWorkspaceArchiveSummaryBytes = 512
	maxWorkspaceArchiveFindings     = 128
	maxWorkspaceInventoryEntries    = 250_000
	maxWorkspaceEvidenceFields      = 32
	maxWorkspacePathBytes           = 4096
)

var (
	workspaceSlugPattern        = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`)
	workspaceIDPattern          = regexp.MustCompile(`^[a-z][a-z0-9_]{2,127}$`)
	workspaceProjectIDPattern   = regexp.MustCompile(`^project_[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	workspaceActorIDPattern     = regexp.MustCompile(`^actor_[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	workspaceOperationID        = regexp.MustCompile(`^workspace_archive_operation_[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	workspaceFindingID          = regexp.MustCompile(`^workspace_archive_finding_[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	workspaceLifecycleID        = regexp.MustCompile(`^workspace_lifecycle_event_[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	sha256DigestPattern         = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	hmacSHA256DigestPattern     = regexp.MustCompile(`^hmac-sha256:[0-9a-f]{64}$`)
	manifestKeyIDPattern        = regexp.MustCompile(`^[a-z][a-z0-9_.-]{2,127}$`)
	evidenceFieldKeyPattern     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	storageEntryIDPattern       = regexp.MustCompile(`^storage_entry_[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	storagePhysicalRefIDPattern = regexp.MustCompile(`^storage_physical_ref_[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
)

type WorkspaceKind string

const (
	WorkspaceKindTopic       WorkspaceKind = "topic"
	WorkspaceKindProject     WorkspaceKind = "project"
	WorkspaceKindLibraryItem WorkspaceKind = "library_item"
)

var workspaceKinds = []WorkspaceKind{
	WorkspaceKindTopic,
	WorkspaceKindProject,
	WorkspaceKindLibraryItem,
}

type WorkspaceOperationKind string

const (
	WorkspaceOperationArchive WorkspaceOperationKind = "archive"
	WorkspaceOperationRestore WorkspaceOperationKind = "restore"
)

type WorkspaceRoot string

const (
	WorkspaceRootBox     WorkspaceRoot = "box"
	WorkspaceRootStorage WorkspaceRoot = "storage"
)

type WorkspaceLifecycleState string

const (
	WorkspaceLifecycleActive   WorkspaceLifecycleState = "active"
	WorkspaceLifecycleArchived WorkspaceLifecycleState = "archived"
)

type WorkspaceLifecycleTransition string

const (
	WorkspaceTransitionArchived WorkspaceLifecycleTransition = "active_to_archived"
	WorkspaceTransitionRestored WorkspaceLifecycleTransition = "archived_to_active"
)

type WorkspaceArchivePhase string

const (
	PhaseArchivePlanned              WorkspaceArchivePhase = "archive_planned"
	PhaseArchiveIntentCommitted      WorkspaceArchivePhase = "archive_intent_committed"
	PhaseArchivePayloadMoved         WorkspaceArchivePhase = "archive_payload_moved"
	PhaseArchiveProjectionsCommitted WorkspaceArchivePhase = "archive_projections_committed"
	PhaseArchiveComplete             WorkspaceArchivePhase = "archive_complete"
	PhaseRestorePlanned              WorkspaceArchivePhase = "restore_planned"
	PhaseRestoreIntentCommitted      WorkspaceArchivePhase = "restore_intent_committed"
	PhaseRestorePayloadMoved         WorkspaceArchivePhase = "restore_payload_moved"
	PhaseRestoreProjectionsCommitted WorkspaceArchivePhase = "restore_projections_committed"
	PhaseRestoreComplete             WorkspaceArchivePhase = "restore_complete"
	PhaseBlocked                     WorkspaceArchivePhase = "blocked"
	PhaseManualRepairRequired        WorkspaceArchivePhase = "manual_repair_required"
)

var workspaceArchivePhases = []WorkspaceArchivePhase{
	PhaseArchivePlanned,
	PhaseArchiveIntentCommitted,
	PhaseArchivePayloadMoved,
	PhaseArchiveProjectionsCommitted,
	PhaseArchiveComplete,
	PhaseRestorePlanned,
	PhaseRestoreIntentCommitted,
	PhaseRestorePayloadMoved,
	PhaseRestoreProjectionsCommitted,
	PhaseRestoreComplete,
	PhaseBlocked,
	PhaseManualRepairRequired,
}

type WorkspaceArchiveOperationStatus string

const (
	OperationStatusPending      WorkspaceArchiveOperationStatus = "pending"
	OperationStatusRunning      WorkspaceArchiveOperationStatus = "running"
	OperationStatusComplete     WorkspaceArchiveOperationStatus = "complete"
	OperationStatusBlocked      WorkspaceArchiveOperationStatus = "blocked"
	OperationStatusManualRepair WorkspaceArchiveOperationStatus = "manual_repair"
)

type PathPresence string

const (
	PathPresent PathPresence = "present"
	PathAbsent  PathPresence = "absent"
)

type InventoryEntryKind string

const (
	InventoryEntryFile      InventoryEntryKind = "file"
	InventoryEntryDirectory InventoryEntryKind = "directory"
	InventoryEntrySymlink   InventoryEntryKind = "symlink"
)

type WorkspaceArchiveFindingCode string

const (
	FindingDestinationCollision    WorkspaceArchiveFindingCode = "destination_collision"
	FindingSymlinkEscape           WorkspaceArchiveFindingCode = "symlink_escape"
	FindingCrossDevice             WorkspaceArchiveFindingCode = "cross_device"
	FindingSourceDrift             WorkspaceArchiveFindingCode = "source_drift"
	FindingDestinationSubstitution WorkspaceArchiveFindingCode = "destination_substitution"
	FindingReplayConflict          WorkspaceArchiveFindingCode = "replay_conflict"
	FindingHistoricalCopyEvidence  WorkspaceArchiveFindingCode = "historical_copy_evidence"
	FindingAmbiguousCustody        WorkspaceArchiveFindingCode = "ambiguous_custody"
	FindingInvalidBinding          WorkspaceArchiveFindingCode = "invalid_binding"
	FindingManualRepairRequired    WorkspaceArchiveFindingCode = "manual_repair_required"
)

type WorkspaceArchiveFindingSeverity string

const (
	FindingSeverityInfo    WorkspaceArchiveFindingSeverity = "info"
	FindingSeverityWarning WorkspaceArchiveFindingSeverity = "warning"
	FindingSeverityError   WorkspaceArchiveFindingSeverity = "error"
)

type WorkspacePathMapping struct {
	Kind                 WorkspaceKind
	ActiveRoot           WorkspaceRoot
	ActiveRelativePath   string
	ArchiveRoot          WorkspaceRoot
	ArchiveContainerPath string
	ArchivePayloadPath   string
	ArchiveManifestPath  string
}

type TrustedWorkspaceRoots struct {
	BoxRoot     string
	StorageRoot string
}

type WorkspacePathRef struct {
	Root         WorkspaceRoot `json:"root"`
	RelativePath string        `json:"relative_path"`
	AbsolutePath string        `json:"absolute_path"`
}

type ResolvedWorkspacePaths struct {
	Mapping          WorkspacePathMapping
	Active           WorkspacePathRef
	ArchiveContainer WorkspacePathRef
	ArchivePayload   WorkspacePathRef
	ArchiveManifest  WorkspacePathRef
}

type PathIdentity struct {
	Presence       PathPresence `json:"presence"`
	ObjectKind     string       `json:"object_kind,omitempty"`
	DeviceID       uint64       `json:"device_id,omitempty"`
	Inode          uint64       `json:"inode,omitempty"`
	Mode           uint32       `json:"mode,omitempty"`
	SizeBytes      int64        `json:"size_bytes,omitempty"`
	ModifiedUnixNS int64        `json:"modified_unix_ns,omitempty"`
}

type WorkspacePathBinding struct {
	Path           WorkspacePathRef `json:"path"`
	Identity       PathIdentity     `json:"identity"`
	ParentIdentity PathIdentity     `json:"parent_identity"`
}

// WorkspaceAncestorBinding records every already-existing directory traversed
// from a trusted root to a planned parent. RelativePath is "." for the trusted
// root itself. Slice 2 requires this evidence so replacing an intermediate
// directory cannot preserve an otherwise plausible leaf path.
type WorkspaceAncestorBinding struct {
	RelativePath string       `json:"relative_path"`
	Identity     PathIdentity `json:"identity"`
}

// WorkspaceCatalogRebind is the exact reviewed catalog mutation associated
// with one physical move. Empty old/new entry paths mean that only the physical
// ref is rebound. The plan digest covers the complete sorted collection.
type WorkspaceCatalogRebind struct {
	StorageEntryID          string `json:"storage_entry_id"`
	StoragePhysicalRefID    string `json:"storage_physical_ref_id,omitempty"`
	ExpectedURI             string `json:"expected_uri,omitempty"`
	NewURI                  string `json:"new_uri,omitempty"`
	ExpectedOriginalPath    string `json:"expected_original_path,omitempty"`
	NewOriginalPath         string `json:"new_original_path,omitempty"`
	ExpectedCurrentViewPath string `json:"expected_current_view_path,omitempty"`
	NewCurrentViewPath      string `json:"new_current_view_path,omitempty"`
}

type NoFollowInventoryEntry struct {
	RelativePath   string             `json:"relative_path"`
	Kind           InventoryEntryKind `json:"kind"`
	DeviceID       uint64             `json:"device_id"`
	Inode          uint64             `json:"inode"`
	Mode           uint32             `json:"mode"`
	SizeBytes      int64              `json:"size_bytes"`
	ModifiedUnixNS int64              `json:"modified_unix_ns"`
	ContentDigest  string             `json:"content_digest,omitempty"`
	SymlinkTarget  string             `json:"symlink_target,omitempty"`
	TargetDigest   string             `json:"target_digest,omitempty"`
}

type NoFollowInventory struct {
	SchemaVersion  string                   `json:"schema_version"`
	RootIdentity   PathIdentity             `json:"root_identity"`
	Entries        []NoFollowInventoryEntry `json:"entries"`
	EntryCount     int                      `json:"entry_count"`
	FileCount      int                      `json:"file_count"`
	DirectoryCount int                      `json:"directory_count"`
	SymlinkCount   int                      `json:"symlink_count"`
	TotalBytes     int64                    `json:"total_bytes"`
	Digest         string                   `json:"digest"`
}

type WorkspaceArchivePlan struct {
	SchemaVersion         string                     `json:"schema_version"`
	EvidenceKind          string                     `json:"evidence_kind"`
	OperationID           string                     `json:"operation_id"`
	OperationKind         WorkspaceOperationKind     `json:"operation_kind"`
	ArchiveOperationID    string                     `json:"archive_operation_id,omitempty"`
	ArchiveManifestDigest string                     `json:"archive_manifest_digest,omitempty"`
	Kind                  WorkspaceKind              `json:"kind"`
	ObjectID              string                     `json:"object_id"`
	Slug                  string                     `json:"slug"`
	Source                WorkspacePathBinding       `json:"source"`
	Destination           WorkspacePathBinding       `json:"destination"`
	SourceAncestors       []WorkspaceAncestorBinding `json:"source_ancestors,omitempty"`
	DestinationAncestors  []WorkspaceAncestorBinding `json:"destination_ancestors,omitempty"`
	Inventory             NoFollowInventory          `json:"inventory"`
	CatalogRebinds        []WorkspaceCatalogRebind   `json:"catalog_rebinds,omitempty"`
	ActorID               string                     `json:"actor_id"`
	Reason                string                     `json:"reason"`
	PlannedAt             time.Time                  `json:"planned_at"`
	PlanDigest            string                     `json:"plan_digest"`
}

type WorkspaceArchiveEvidenceField struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type WorkspaceArchiveFinding struct {
	SchemaVersion string                          `json:"schema_version"`
	FindingID     string                          `json:"finding_id,omitempty"`
	OperationID   string                          `json:"operation_id"`
	Code          WorkspaceArchiveFindingCode     `json:"code"`
	Severity      WorkspaceArchiveFindingSeverity `json:"severity"`
	AtPhase       WorkspaceArchivePhase           `json:"at_phase"`
	Summary       string                          `json:"summary"`
	Repairable    bool                            `json:"repairable"`
	Evidence      []WorkspaceArchiveEvidenceField `json:"evidence,omitempty"`
	CreatedAt     time.Time                       `json:"created_at,omitempty"`
}

type WorkspaceArchiveOperation struct {
	SchemaVersion          string                          `json:"schema_version"`
	EvidenceKind           string                          `json:"evidence_kind"`
	OperationID            string                          `json:"operation_id"`
	OperationKind          WorkspaceOperationKind          `json:"operation_kind"`
	Kind                   WorkspaceKind                   `json:"kind"`
	ObjectID               string                          `json:"object_id"`
	Slug                   string                          `json:"slug"`
	Phase                  WorkspaceArchivePhase           `json:"phase"`
	LastSafePhase          WorkspaceArchivePhase           `json:"last_safe_phase,omitempty"`
	Status                 WorkspaceArchiveOperationStatus `json:"status"`
	PlanDigest             string                          `json:"plan_digest"`
	Source                 WorkspacePathBinding            `json:"source"`
	Destination            WorkspacePathBinding            `json:"destination"`
	ActorID                string                          `json:"actor_id"`
	Reason                 string                          `json:"reason"`
	Findings               []WorkspaceArchiveFinding       `json:"findings,omitempty"`
	PlannedAt              time.Time                       `json:"planned_at"`
	IntentCommittedAt      *time.Time                      `json:"intent_committed_at,omitempty"`
	PayloadMovedAt         *time.Time                      `json:"payload_moved_at,omitempty"`
	ProjectionsCommittedAt *time.Time                      `json:"projections_committed_at,omitempty"`
	CompletedAt            *time.Time                      `json:"completed_at,omitempty"`
	UpdatedAt              time.Time                       `json:"updated_at"`
}

type WorkspaceArchiveStatus struct {
	SchemaVersion string                          `json:"schema_version"`
	OperationID   string                          `json:"operation_id"`
	Phase         WorkspaceArchivePhase           `json:"phase"`
	LastSafePhase WorkspaceArchivePhase           `json:"last_safe_phase,omitempty"`
	Status        WorkspaceArchiveOperationStatus `json:"status"`
	Findings      []WorkspaceArchiveFinding       `json:"findings,omitempty"`
	UpdatedAt     time.Time                       `json:"updated_at"`
}

type ManifestAuthentication struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"key_id"`
	Tag       string `json:"tag"`
}

type WorkspaceArchiveManifest struct {
	SchemaVersion         string                  `json:"schema_version"`
	EvidenceKind          string                  `json:"evidence_kind"`
	ArchiveOperationID    string                  `json:"archive_operation_id"`
	RestoreOperationID    string                  `json:"restore_operation_id,omitempty"`
	Kind                  WorkspaceKind           `json:"kind"`
	ObjectID              string                  `json:"object_id"`
	Slug                  string                  `json:"slug"`
	LifecycleState        WorkspaceLifecycleState `json:"lifecycle_state"`
	ActivePath            WorkspacePathRef        `json:"active_path"`
	ArchivePayloadPath    WorkspacePathRef        `json:"archive_payload_path"`
	ArchiveSourceIdentity PathIdentity            `json:"archive_source_identity"`
	InventoryDigest       string                  `json:"inventory_digest"`
	PlanDigest            string                  `json:"plan_digest"`
	RestorePlanDigest     string                  `json:"restore_plan_digest,omitempty"`
	LifecycleEventID      string                  `json:"lifecycle_event_id"`
	ActorID               string                  `json:"actor_id"`
	Reason                string                  `json:"reason"`
	ArchivedAt            time.Time               `json:"archived_at"`
	RestoredAt            *time.Time              `json:"restored_at,omitempty"`
	Authentication        ManifestAuthentication  `json:"authentication"`
}

type WorkspaceLifecycleEvent struct {
	SchemaVersion   string                       `json:"schema_version"`
	EventID         string                       `json:"event_id"`
	EventKind       string                       `json:"event_kind"`
	OperationID     string                       `json:"operation_id"`
	Kind            WorkspaceKind                `json:"kind"`
	ObjectID        string                       `json:"object_id"`
	Slug            string                       `json:"slug"`
	Transition      WorkspaceLifecycleTransition `json:"transition"`
	FromState       WorkspaceLifecycleState      `json:"from_state"`
	ToState         WorkspaceLifecycleState      `json:"to_state"`
	SourcePath      WorkspacePathRef             `json:"source_path"`
	DestinationPath WorkspacePathRef             `json:"destination_path"`
	ActorID         string                       `json:"actor_id"`
	Reason          string                       `json:"reason"`
	OccurredAt      time.Time                    `json:"occurred_at"`
}

func AllWorkspaceKinds() []WorkspaceKind {
	return append([]WorkspaceKind(nil), workspaceKinds...)
}

func AllWorkspaceArchivePhases() []WorkspaceArchivePhase {
	return append([]WorkspaceArchivePhase(nil), workspaceArchivePhases...)
}

func WorkspaceMapping(kind WorkspaceKind, slug string) (WorkspacePathMapping, error) {
	if err := ValidateWorkspaceSlug(slug); err != nil {
		return WorkspacePathMapping{}, err
	}
	mapping := WorkspacePathMapping{Kind: kind, ActiveRoot: WorkspaceRootBox, ArchiveRoot: WorkspaceRootStorage}
	switch kind {
	case WorkspaceKindTopic:
		mapping.ActiveRelativePath = pathpkg.Join("Topics", slug)
		mapping.ArchiveContainerPath = pathpkg.Join("archive", "topics", slug)
		mapping.ArchivePayloadPath = pathpkg.Join(mapping.ArchiveContainerPath, "content")
	case WorkspaceKindProject:
		mapping.ActiveRelativePath = pathpkg.Join("Projects", slug)
		mapping.ArchiveContainerPath = pathpkg.Join("archive", "projects", slug)
		mapping.ArchivePayloadPath = pathpkg.Join(mapping.ArchiveContainerPath, "project")
	case WorkspaceKindLibraryItem:
		mapping.ActiveRelativePath = pathpkg.Join("Library", slug)
		mapping.ArchiveContainerPath = pathpkg.Join("archive", "library", slug)
		mapping.ArchivePayloadPath = pathpkg.Join(mapping.ArchiveContainerPath, "content")
	default:
		return WorkspacePathMapping{}, fmt.Errorf("unsupported workspace kind %q", kind)
	}
	mapping.ArchiveManifestPath = pathpkg.Join(mapping.ArchiveContainerPath, "archive.json")
	return mapping, nil
}

func ValidateWorkspaceSlug(slug string) error {
	if !workspaceSlugPattern.MatchString(slug) {
		return fmt.Errorf("workspace slug must be lowercase URL-safe and 3-64 characters")
	}
	return nil
}

func ResolveWorkspacePaths(roots TrustedWorkspaceRoots, kind WorkspaceKind, slug string) (ResolvedWorkspacePaths, error) {
	if err := validateTrustedWorkspaceRoot(WorkspaceRootBox, roots.BoxRoot); err != nil {
		return ResolvedWorkspacePaths{}, err
	}
	if err := validateTrustedWorkspaceRoot(WorkspaceRootStorage, roots.StorageRoot); err != nil {
		return ResolvedWorkspacePaths{}, err
	}
	mapping, err := WorkspaceMapping(kind, slug)
	if err != nil {
		return ResolvedWorkspacePaths{}, err
	}
	ref := func(root WorkspaceRoot, relative string) WorkspacePathRef {
		base := roots.BoxRoot
		if root == WorkspaceRootStorage {
			base = roots.StorageRoot
		}
		return WorkspacePathRef{Root: root, RelativePath: relative, AbsolutePath: filepath.Join(base, filepath.FromSlash(relative))}
	}
	return ResolvedWorkspacePaths{
		Mapping:          mapping,
		Active:           ref(mapping.ActiveRoot, mapping.ActiveRelativePath),
		ArchiveContainer: ref(mapping.ArchiveRoot, mapping.ArchiveContainerPath),
		ArchivePayload:   ref(mapping.ArchiveRoot, mapping.ArchivePayloadPath),
		ArchiveManifest:  ref(mapping.ArchiveRoot, mapping.ArchiveManifestPath),
	}, nil
}

func InventoryMappedSource(ctx context.Context, roots TrustedWorkspaceRoots, operation WorkspaceOperationKind, kind WorkspaceKind, slug string) (NoFollowInventory, error) {
	paths, err := ResolveWorkspacePaths(roots, kind, slug)
	if err != nil {
		return NoFollowInventory{}, err
	}
	var source WorkspacePathRef
	switch operation {
	case WorkspaceOperationArchive:
		source = paths.Active
	case WorkspaceOperationRestore:
		source = paths.ArchivePayload
	default:
		return NoFollowInventory{}, fmt.Errorf("unsupported workspace operation %q", operation)
	}
	root, rootInfo, err := openMappedDirectoryNoFollow(roots, source)
	if err != nil {
		return NoFollowInventory{}, err
	}
	defer root.Close()
	return inventoryOpenRootNoFollow(ctx, root, rootInfo)
}

func ValidateWorkspaceArchivePlan(plan WorkspaceArchivePlan, roots TrustedWorkspaceRoots) error {
	if plan.SchemaVersion != WorkspaceArchivePlanSchemaVersion {
		return fmt.Errorf("unsupported workspace archive plan schema %q", plan.SchemaVersion)
	}
	if plan.EvidenceKind != PhysicalWorkspaceMoveEvidence {
		return fmt.Errorf("plan evidence_kind must be %q", PhysicalWorkspaceMoveEvidence)
	}
	if !workspaceOperationID.MatchString(plan.OperationID) {
		return fmt.Errorf("invalid workspace archive operation_id")
	}
	if err := validateWorkspaceObject(plan.Kind, plan.ObjectID, plan.Slug); err != nil {
		return err
	}
	if !validWorkspaceActorID(plan.ActorID) {
		return fmt.Errorf("invalid actor_id")
	}
	if err := validateBoundedText("reason", plan.Reason, maxWorkspaceArchiveReasonBytes); err != nil {
		return err
	}
	if plan.PlannedAt.IsZero() {
		return fmt.Errorf("planned_at is required")
	}
	paths, err := ResolveWorkspacePaths(roots, plan.Kind, plan.Slug)
	if err != nil {
		return err
	}
	var source, destination WorkspacePathRef
	switch plan.OperationKind {
	case WorkspaceOperationArchive:
		if plan.ArchiveOperationID != "" || plan.ArchiveManifestDigest != "" {
			return fmt.Errorf("archive plan cannot bind prior archive evidence")
		}
		source, destination = paths.Active, paths.ArchivePayload
	case WorkspaceOperationRestore:
		if !workspaceOperationID.MatchString(plan.ArchiveOperationID) || plan.ArchiveOperationID == plan.OperationID || !sha256DigestPattern.MatchString(plan.ArchiveManifestDigest) {
			return fmt.Errorf("restore plan requires a distinct archive operation and authenticated manifest digest")
		}
		source, destination = paths.ArchivePayload, paths.Active
	default:
		return fmt.Errorf("unsupported workspace operation %q", plan.OperationKind)
	}
	if err := validatePathBinding("source", plan.Source, source, PathPresent); err != nil {
		return err
	}
	if err := validatePathBinding("destination", plan.Destination, destination, PathAbsent); err != nil {
		return err
	}
	if plan.Source.Identity.DeviceID != plan.Destination.ParentIdentity.DeviceID {
		return fmt.Errorf("source and destination roots must be on the same filesystem")
	}
	if err := ValidateNoFollowInventory(plan.Inventory); err != nil {
		return err
	}
	if !SamePathIdentity(plan.Inventory.RootIdentity, plan.Source.Identity) {
		return fmt.Errorf("inventory root identity does not match planned source identity")
	}
	if err := validateWorkspaceAncestorBindings(plan.SourceAncestors); err != nil {
		return fmt.Errorf("source ancestors: %w", err)
	}
	if err := validateWorkspaceAncestorBindings(plan.DestinationAncestors); err != nil {
		return fmt.Errorf("destination ancestors: %w", err)
	}
	if err := validateWorkspaceCatalogRebinds(plan.CatalogRebinds); err != nil {
		return err
	}
	digest, err := WorkspaceArchivePlanDigest(plan)
	if err != nil {
		return err
	}
	if plan.PlanDigest != digest {
		return fmt.Errorf("plan_digest mismatch")
	}
	return nil
}

func validateWorkspaceAncestorBindings(bindings []WorkspaceAncestorBinding) error {
	previous := ""
	for index, binding := range bindings {
		if binding.RelativePath != "." {
			clean := pathpkg.Clean(binding.RelativePath)
			if clean != binding.RelativePath || pathpkg.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
				return fmt.Errorf("ancestor %d has unsafe relative_path", index)
			}
		}
		if index > 0 && (binding.RelativePath == "." || binding.RelativePath <= previous) {
			return fmt.Errorf("ancestor bindings must be ordered and unique")
		}
		if err := validatePathIdentity(binding.Identity, PathPresent, InventoryEntryDirectory); err != nil {
			return fmt.Errorf("ancestor %q: %w", binding.RelativePath, err)
		}
		previous = binding.RelativePath
	}
	return nil
}

func validateWorkspaceCatalogRebinds(rebinds []WorkspaceCatalogRebind) error {
	previous := ""
	for index, rebind := range rebinds {
		if !storageEntryIDPattern.MatchString(rebind.StorageEntryID) {
			return fmt.Errorf("catalog rebind %d has invalid storage_entry_id", index)
		}
		key := rebind.StorageEntryID + "\x00" + rebind.StoragePhysicalRefID
		if index > 0 && key <= previous {
			return fmt.Errorf("catalog rebinds must be strictly sorted and unique")
		}
		previous = key
		physical := rebind.StoragePhysicalRefID != "" || rebind.ExpectedURI != "" || rebind.NewURI != ""
		entry := rebind.ExpectedOriginalPath != rebind.NewOriginalPath || rebind.ExpectedCurrentViewPath != rebind.NewCurrentViewPath
		if physical {
			if !storagePhysicalRefIDPattern.MatchString(rebind.StoragePhysicalRefID) || rebind.ExpectedURI == "" || rebind.NewURI == "" || rebind.ExpectedURI == rebind.NewURI {
				return fmt.Errorf("catalog rebind %d has incomplete physical-ref evidence", index)
			}
			if entry {
				return fmt.Errorf("catalog rebind %d combines physical-ref and entry mutations", index)
			}
		} else if rebind.ExpectedURI != "" || rebind.NewURI != "" {
			return fmt.Errorf("catalog rebind %d has partial physical-ref evidence", index)
		}
		if !physical && !entry {
			return fmt.Errorf("catalog rebind %d has no mutation", index)
		}
		if entry && ((rebind.ExpectedOriginalPath == "") != (rebind.NewOriginalPath == "") || (rebind.ExpectedCurrentViewPath == "") != (rebind.NewCurrentViewPath == "")) {
			return fmt.Errorf("catalog rebind %d has an incomplete entry path pair", index)
		}
	}
	return nil
}

func WorkspaceArchivePlanDigest(plan WorkspaceArchivePlan) (string, error) {
	plan.PlanDigest = ""
	payload, err := json.Marshal(plan)
	if err != nil {
		return "", fmt.Errorf("encode workspace archive plan: %w", err)
	}
	return sha256Digest(payload), nil
}

func SealWorkspaceArchivePlan(plan *WorkspaceArchivePlan) error {
	if plan == nil {
		return fmt.Errorf("workspace archive plan is required")
	}
	digest, err := WorkspaceArchivePlanDigest(*plan)
	if err != nil {
		return err
	}
	plan.PlanDigest = digest
	return nil
}

func ValidateNoFollowInventory(inventory NoFollowInventory) error {
	if inventory.SchemaVersion != NoFollowInventorySchemaVersion {
		return fmt.Errorf("unsupported no-follow inventory schema %q", inventory.SchemaVersion)
	}
	if err := validatePathIdentity(inventory.RootIdentity, PathPresent, InventoryEntryDirectory); err != nil {
		return fmt.Errorf("inventory root identity: %w", err)
	}
	if len(inventory.Entries) > maxWorkspaceInventoryEntries {
		return fmt.Errorf("inventory contains more than %d entries", maxWorkspaceInventoryEntries)
	}
	var files, directories, symlinks int
	var total int64
	previous := ""
	for index, entry := range inventory.Entries {
		if entry.RelativePath == "" || entry.RelativePath == "." || pathpkg.Clean(entry.RelativePath) != entry.RelativePath || pathpkg.IsAbs(entry.RelativePath) || entry.RelativePath == ".." || strings.HasPrefix(entry.RelativePath, "../") {
			return fmt.Errorf("inventory entry %d has unsafe relative_path", index)
		}
		if previous != "" && entry.RelativePath <= previous {
			return fmt.Errorf("inventory entries must be strictly sorted and unique")
		}
		previous = entry.RelativePath
		if len(entry.RelativePath) > maxWorkspacePathBytes || entry.DeviceID == 0 || entry.Inode == 0 || entry.DeviceID != inventory.RootIdentity.DeviceID || entry.SizeBytes < 0 {
			return fmt.Errorf("inventory entry %q has invalid or cross-device identity", entry.RelativePath)
		}
		switch entry.Kind {
		case InventoryEntryFile:
			files++
			if entry.SizeBytes > int64(^uint64(0)>>1)-total {
				return fmt.Errorf("inventory byte total overflows int64")
			}
			total += entry.SizeBytes
			if !sha256DigestPattern.MatchString(entry.ContentDigest) || entry.SymlinkTarget != "" || entry.TargetDigest != "" {
				return fmt.Errorf("file inventory entry %q has invalid digest fields", entry.RelativePath)
			}
		case InventoryEntryDirectory:
			directories++
			if entry.ContentDigest != "" || entry.SymlinkTarget != "" || entry.TargetDigest != "" {
				return fmt.Errorf("directory inventory entry %q has payload evidence", entry.RelativePath)
			}
		case InventoryEntrySymlink:
			symlinks++
			if err := validateSymlinkTarget(entry.RelativePath, entry.SymlinkTarget); err != nil {
				return err
			}
			if !sha256DigestPattern.MatchString(entry.TargetDigest) || entry.ContentDigest != "" {
				return fmt.Errorf("symlink inventory entry %q has invalid target digest", entry.RelativePath)
			}
		default:
			return fmt.Errorf("inventory entry %q has unsupported kind %q", entry.RelativePath, entry.Kind)
		}
	}
	if inventory.EntryCount != len(inventory.Entries) || inventory.FileCount != files || inventory.DirectoryCount != directories || inventory.SymlinkCount != symlinks || inventory.TotalBytes != total {
		return fmt.Errorf("inventory summary does not match entries")
	}
	digest, err := noFollowInventoryDigest(inventory)
	if err != nil {
		return err
	}
	if inventory.Digest != digest {
		return fmt.Errorf("inventory digest mismatch")
	}
	return nil
}

func EvaluatePlanEvidence(plan WorkspaceArchivePlan, observedSource, observedDestination WorkspacePathBinding) []WorkspaceArchiveFinding {
	findings := []WorkspaceArchiveFinding{}
	add := func(code WorkspaceArchiveFindingCode, summary string) {
		findings = append(findings, WorkspaceArchiveFinding{
			SchemaVersion: WorkspaceArchiveFindingSchemaVersion,
			OperationID:   plan.OperationID,
			Code:          code,
			Severity:      FindingSeverityError,
			AtPhase:       plannedPhase(plan.OperationKind),
			Summary:       summary,
			Repairable:    true,
		})
	}
	if !samePathRef(plan.Source.Path, observedSource.Path) ||
		!SamePathIdentity(plan.Source.Identity, observedSource.Identity) ||
		!SamePathIdentity(plan.Source.ParentIdentity, observedSource.ParentIdentity) {
		add(FindingSourceDrift, "source path, identity, or parent identity changed after plan review")
	}
	if !samePathRef(plan.Destination.Path, observedDestination.Path) || !SamePathIdentity(plan.Destination.ParentIdentity, observedDestination.ParentIdentity) {
		add(FindingDestinationSubstitution, "destination path or parent identity changed after plan review")
	}
	if plan.Destination.Identity.Presence == PathAbsent && observedDestination.Identity.Presence == PathPresent {
		add(FindingDestinationCollision, "planned-absent destination now exists")
	}
	if observedSource.Identity.DeviceID != 0 && observedDestination.ParentIdentity.DeviceID != 0 && observedSource.Identity.DeviceID != observedDestination.ParentIdentity.DeviceID {
		add(FindingCrossDevice, "source and destination resolve to different filesystems")
	}
	return findings
}

func EvaluateReplay(existing WorkspaceArchiveOperation, incoming WorkspaceArchivePlan) (bool, *WorkspaceArchiveFinding) {
	if existing.OperationID == incoming.OperationID && existing.PlanDigest == incoming.PlanDigest {
		return true, nil
	}
	if existing.OperationID == incoming.OperationID {
		return false, &WorkspaceArchiveFinding{
			SchemaVersion: WorkspaceArchiveFindingSchemaVersion,
			OperationID:   incoming.OperationID,
			Code:          FindingReplayConflict,
			Severity:      FindingSeverityError,
			AtPhase:       existing.Phase,
			Summary:       "operation replay used a different reviewed plan digest",
			Repairable:    false,
		}
	}
	return false, nil
}

func ValidateWorkspaceArchiveOperation(operation WorkspaceArchiveOperation) error {
	if operation.SchemaVersion != WorkspaceArchiveOperationSchemaVersion || operation.EvidenceKind != PhysicalWorkspaceMoveEvidence {
		return fmt.Errorf("unsupported workspace archive operation contract")
	}
	if !workspaceOperationID.MatchString(operation.OperationID) || !sha256DigestPattern.MatchString(operation.PlanDigest) {
		return fmt.Errorf("invalid operation identity or plan digest")
	}
	if err := validateWorkspaceObject(operation.Kind, operation.ObjectID, operation.Slug); err != nil {
		return err
	}
	if err := validateMappedBindings(operation.OperationKind, operation.Kind, operation.Slug, operation.Source, operation.Destination); err != nil {
		return err
	}
	if err := validatePathIdentity(operation.Source.Identity, PathPresent, InventoryEntryDirectory); err != nil {
		return fmt.Errorf("source identity: %w", err)
	}
	if err := validatePathIdentity(operation.Source.ParentIdentity, PathPresent, InventoryEntryDirectory); err != nil {
		return fmt.Errorf("source parent identity: %w", err)
	}
	if err := validatePathIdentity(operation.Destination.Identity, PathAbsent, ""); err != nil {
		return fmt.Errorf("destination identity: %w", err)
	}
	if err := validatePathIdentity(operation.Destination.ParentIdentity, PathPresent, InventoryEntryDirectory); err != nil {
		return fmt.Errorf("destination parent identity: %w", err)
	}
	if operation.Source.Identity.DeviceID != operation.Destination.ParentIdentity.DeviceID {
		return fmt.Errorf("operation source and destination are cross-device")
	}
	if !validWorkspaceActorID(operation.ActorID) {
		return fmt.Errorf("invalid actor_id")
	}
	if err := validateBoundedText("reason", operation.Reason, maxWorkspaceArchiveReasonBytes); err != nil {
		return err
	}
	effective := operation.Phase
	switch operation.Phase {
	case PhaseBlocked:
		if operation.Status != OperationStatusBlocked {
			return fmt.Errorf("blocked phase requires blocked status")
		}
		effective = operation.LastSafePhase
	case PhaseManualRepairRequired:
		if operation.Status != OperationStatusManualRepair {
			return fmt.Errorf("manual repair phase requires manual_repair status")
		}
		effective = operation.LastSafePhase
	default:
		if operation.LastSafePhase != "" {
			return fmt.Errorf("last_safe_phase is only valid for blocked or manual repair operations")
		}
		want, ok := statusForPhase(operation.Phase)
		if !ok || operation.Status != want {
			return fmt.Errorf("operation status %q does not match phase %q", operation.Status, operation.Phase)
		}
	}
	rank, ok := phaseRank(operation.OperationKind, effective)
	if !ok {
		return fmt.Errorf("last safe phase %q is invalid for operation kind %q", effective, operation.OperationKind)
	}
	if (operation.Phase == PhaseBlocked || operation.Phase == PhaseManualRepairRequired) && rank >= 4 {
		return fmt.Errorf("a complete phase cannot be recorded as a blocked last-safe boundary")
	}
	if operation.PlannedAt.IsZero() || operation.UpdatedAt.IsZero() {
		return fmt.Errorf("operation planned_at and updated_at are required")
	}
	if err := validateOperationTimestamps(operation, rank); err != nil {
		return err
	}
	if len(operation.Findings) > maxWorkspaceArchiveFindings {
		return fmt.Errorf("operation has too many findings")
	}
	for _, finding := range operation.Findings {
		if err := ValidateWorkspaceArchiveFinding(finding); err != nil {
			return err
		}
		if finding.OperationID != operation.OperationID {
			return fmt.Errorf("embedded finding operation_id does not match owning operation")
		}
	}
	return nil
}

func ValidateWorkspaceArchiveOperationForRoots(operation WorkspaceArchiveOperation, roots TrustedWorkspaceRoots) error {
	if err := ValidateWorkspaceArchiveOperation(operation); err != nil {
		return err
	}
	paths, err := ResolveWorkspacePaths(roots, operation.Kind, operation.Slug)
	if err != nil {
		return err
	}
	source, destination := paths.Active, paths.ArchivePayload
	if operation.OperationKind == WorkspaceOperationRestore {
		source, destination = paths.ArchivePayload, paths.Active
	}
	if !samePathRef(operation.Source.Path, source) || !samePathRef(operation.Destination.Path, destination) {
		return fmt.Errorf("operation paths do not match trusted workspace roots")
	}
	return nil
}

func ValidateWorkspaceArchiveStatus(status WorkspaceArchiveStatus) error {
	if status.SchemaVersion != WorkspaceArchiveStatusSchemaVersion || !workspaceOperationID.MatchString(status.OperationID) || status.UpdatedAt.IsZero() {
		return fmt.Errorf("invalid workspace archive status identity")
	}
	op := WorkspaceArchiveOperation{Phase: status.Phase, LastSafePhase: status.LastSafePhase, Status: status.Status}
	switch status.Phase {
	case PhaseBlocked:
		if status.Status != OperationStatusBlocked || !validLastSafePhase(status.LastSafePhase) {
			return fmt.Errorf("invalid blocked status")
		}
	case PhaseManualRepairRequired:
		if status.Status != OperationStatusManualRepair || !validLastSafePhase(status.LastSafePhase) {
			return fmt.Errorf("invalid manual repair status")
		}
	default:
		want, ok := statusForPhase(op.Phase)
		if !ok || want != status.Status || status.LastSafePhase != "" {
			return fmt.Errorf("status does not match phase")
		}
	}
	if len(status.Findings) > maxWorkspaceArchiveFindings {
		return fmt.Errorf("status has too many findings")
	}
	for _, finding := range status.Findings {
		if err := ValidateWorkspaceArchiveFinding(finding); err != nil {
			return err
		}
		if finding.OperationID != status.OperationID {
			return fmt.Errorf("embedded finding operation_id does not match owning status")
		}
	}
	return nil
}

func ValidateWorkspaceArchiveFinding(finding WorkspaceArchiveFinding) error {
	if finding.SchemaVersion != WorkspaceArchiveFindingSchemaVersion || !workspaceOperationID.MatchString(finding.OperationID) {
		return fmt.Errorf("invalid finding contract identity")
	}
	if finding.FindingID != "" && !workspaceFindingID.MatchString(finding.FindingID) {
		return fmt.Errorf("invalid finding_id")
	}
	if !validFindingCode(finding.Code) || (finding.Severity != FindingSeverityInfo && finding.Severity != FindingSeverityWarning && finding.Severity != FindingSeverityError) {
		return fmt.Errorf("unsupported finding code or severity")
	}
	if !validPhase(finding.AtPhase) {
		return fmt.Errorf("unsupported finding phase")
	}
	if err := validateBoundedText("finding summary", finding.Summary, maxWorkspaceArchiveSummaryBytes); err != nil {
		return err
	}
	if len(finding.Evidence) > maxWorkspaceEvidenceFields {
		return fmt.Errorf("finding has too many evidence fields")
	}
	seen := map[string]struct{}{}
	for _, field := range finding.Evidence {
		if !evidenceFieldKeyPattern.MatchString(field.Key) || len(field.Value) > maxWorkspaceArchiveSummaryBytes {
			return fmt.Errorf("invalid finding evidence field")
		}
		if _, ok := seen[field.Key]; ok {
			return fmt.Errorf("duplicate finding evidence key %q", field.Key)
		}
		seen[field.Key] = struct{}{}
	}
	return nil
}

func ValidateWorkspaceArchiveManifest(manifest WorkspaceArchiveManifest) error {
	if manifest.SchemaVersion != WorkspaceArchiveManifestSchemaVersion || manifest.EvidenceKind != PhysicalWorkspaceMoveEvidence {
		return fmt.Errorf("manifest is not physical workspace move evidence")
	}
	if !workspaceOperationID.MatchString(manifest.ArchiveOperationID) || !workspaceLifecycleID.MatchString(manifest.LifecycleEventID) {
		return fmt.Errorf("invalid physical manifest operation or event identity")
	}
	if manifest.RestoreOperationID != "" && !workspaceOperationID.MatchString(manifest.RestoreOperationID) {
		return fmt.Errorf("invalid restore operation identity")
	}
	if err := validateWorkspaceObject(manifest.Kind, manifest.ObjectID, manifest.Slug); err != nil {
		return err
	}
	mapping, _ := WorkspaceMapping(manifest.Kind, manifest.Slug)
	if err := validateMappedPathRef("active_path", manifest.ActivePath, mapping.ActiveRoot, mapping.ActiveRelativePath); err != nil {
		return err
	}
	if err := validateMappedPathRef("archive_payload_path", manifest.ArchivePayloadPath, mapping.ArchiveRoot, mapping.ArchivePayloadPath); err != nil {
		return err
	}
	if err := validatePathIdentity(manifest.ArchiveSourceIdentity, PathPresent, InventoryEntryDirectory); err != nil {
		return fmt.Errorf("archive_source_identity: %w", err)
	}
	if !sha256DigestPattern.MatchString(manifest.InventoryDigest) || !sha256DigestPattern.MatchString(manifest.PlanDigest) {
		return fmt.Errorf("invalid manifest inventory or plan digest")
	}
	if !validWorkspaceActorID(manifest.ActorID) || manifest.ArchivedAt.IsZero() {
		return fmt.Errorf("invalid manifest actor or archive time")
	}
	if err := validateBoundedText("reason", manifest.Reason, maxWorkspaceArchiveReasonBytes); err != nil {
		return err
	}
	switch manifest.LifecycleState {
	case WorkspaceLifecycleArchived:
		if manifest.RestoreOperationID != "" || manifest.RestorePlanDigest != "" || manifest.RestoredAt != nil {
			return fmt.Errorf("archived manifest cannot contain restore completion evidence")
		}
	case WorkspaceLifecycleActive:
		if manifest.RestoreOperationID == "" || !sha256DigestPattern.MatchString(manifest.RestorePlanDigest) || manifest.RestoredAt == nil || manifest.RestoredAt.IsZero() {
			return fmt.Errorf("active manifest requires restore completion evidence")
		}
		if manifest.RestoredAt.Before(manifest.ArchivedAt) {
			return fmt.Errorf("restored_at precedes archived_at")
		}
	default:
		return fmt.Errorf("unsupported manifest lifecycle state %q", manifest.LifecycleState)
	}
	if manifest.Authentication.Algorithm != "hmac-sha256" || !manifestKeyIDPattern.MatchString(manifest.Authentication.KeyID) || !hmacSHA256DigestPattern.MatchString(manifest.Authentication.Tag) {
		return fmt.Errorf("invalid manifest authentication contract")
	}
	return nil
}

func ValidateWorkspaceArchiveManifestForRoots(manifest WorkspaceArchiveManifest, roots TrustedWorkspaceRoots) error {
	if err := ValidateWorkspaceArchiveManifest(manifest); err != nil {
		return err
	}
	paths, err := ResolveWorkspacePaths(roots, manifest.Kind, manifest.Slug)
	if err != nil {
		return err
	}
	if !samePathRef(manifest.ActivePath, paths.Active) || !samePathRef(manifest.ArchivePayloadPath, paths.ArchivePayload) {
		return fmt.Errorf("physical manifest paths do not match trusted workspace roots")
	}
	return nil
}

func ValidateWorkspaceArchiveManifestForOperations(manifest WorkspaceArchiveManifest, archiveOperation WorkspaceArchiveOperation, restoreOperation *WorkspaceArchiveOperation) error {
	if err := ValidateWorkspaceArchiveManifest(manifest); err != nil {
		return err
	}
	if err := ValidateWorkspaceArchiveOperation(archiveOperation); err != nil {
		return fmt.Errorf("archive operation: %w", err)
	}
	if archiveOperation.OperationKind != WorkspaceOperationArchive ||
		archiveOperation.OperationID != manifest.ArchiveOperationID ||
		archiveOperation.Kind != manifest.Kind ||
		archiveOperation.ObjectID != manifest.ObjectID ||
		archiveOperation.Slug != manifest.Slug ||
		archiveOperation.PlanDigest != manifest.PlanDigest ||
		!samePathRef(archiveOperation.Source.Path, manifest.ActivePath) ||
		!samePathRef(archiveOperation.Destination.Path, manifest.ArchivePayloadPath) ||
		!SamePathIdentity(archiveOperation.Source.Identity, manifest.ArchiveSourceIdentity) ||
		archiveOperation.ActorID != manifest.ActorID ||
		archiveOperation.Reason != manifest.Reason {
		return fmt.Errorf("manifest does not match its archive operation")
	}
	archiveRank, ok := effectivePhaseRank(archiveOperation)
	if !ok || archiveRank < 2 || archiveOperation.PayloadMovedAt == nil ||
		manifest.ArchivedAt.Before(*archiveOperation.PayloadMovedAt) || manifest.ArchivedAt.After(archiveOperation.UpdatedAt) {
		return fmt.Errorf("manifest archive time is outside its payload-moved operation interval")
	}
	if manifest.LifecycleState == WorkspaceLifecycleArchived {
		if restoreOperation != nil {
			return fmt.Errorf("archived manifest cannot bind a restore operation")
		}
		return nil
	}
	if restoreOperation == nil {
		return fmt.Errorf("active manifest requires its restore operation")
	}
	if err := ValidateWorkspaceArchiveOperation(*restoreOperation); err != nil {
		return fmt.Errorf("restore operation: %w", err)
	}
	if restoreOperation.OperationKind != WorkspaceOperationRestore ||
		restoreOperation.OperationID != manifest.RestoreOperationID ||
		restoreOperation.Kind != manifest.Kind ||
		restoreOperation.ObjectID != manifest.ObjectID ||
		restoreOperation.Slug != manifest.Slug ||
		restoreOperation.PlanDigest != manifest.RestorePlanDigest ||
		!samePathRef(restoreOperation.Source.Path, manifest.ArchivePayloadPath) ||
		!samePathRef(restoreOperation.Destination.Path, manifest.ActivePath) {
		return fmt.Errorf("manifest does not match its restore operation")
	}
	restoreRank, ok := effectivePhaseRank(*restoreOperation)
	if !ok || restoreRank < 2 || restoreOperation.PayloadMovedAt == nil || manifest.RestoredAt == nil ||
		manifest.RestoredAt.Before(*restoreOperation.PayloadMovedAt) || manifest.RestoredAt.After(restoreOperation.UpdatedAt) {
		return fmt.Errorf("manifest restore time is outside its payload-moved operation interval")
	}
	return nil
}

func ValidateWorkspaceLifecycleEvent(event WorkspaceLifecycleEvent) error {
	if event.SchemaVersion != WorkspaceLifecycleEventSchemaVersion || event.EventKind != WorkspaceLifecycleEventKind {
		return fmt.Errorf("unsupported workspace lifecycle event contract")
	}
	if !workspaceLifecycleID.MatchString(event.EventID) || !workspaceOperationID.MatchString(event.OperationID) {
		return fmt.Errorf("invalid workspace lifecycle event identity")
	}
	if err := validateWorkspaceObject(event.Kind, event.ObjectID, event.Slug); err != nil {
		return err
	}
	mapping, _ := WorkspaceMapping(event.Kind, event.Slug)
	switch event.Transition {
	case WorkspaceTransitionArchived:
		if event.FromState != WorkspaceLifecycleActive || event.ToState != WorkspaceLifecycleArchived {
			return fmt.Errorf("archive event has invalid lifecycle states")
		}
		if err := validateMappedPathRef("source_path", event.SourcePath, mapping.ActiveRoot, mapping.ActiveRelativePath); err != nil {
			return err
		}
		if err := validateMappedPathRef("destination_path", event.DestinationPath, mapping.ArchiveRoot, mapping.ArchivePayloadPath); err != nil {
			return err
		}
	case WorkspaceTransitionRestored:
		if event.FromState != WorkspaceLifecycleArchived || event.ToState != WorkspaceLifecycleActive {
			return fmt.Errorf("restore event has invalid lifecycle states")
		}
		if err := validateMappedPathRef("source_path", event.SourcePath, mapping.ArchiveRoot, mapping.ArchivePayloadPath); err != nil {
			return err
		}
		if err := validateMappedPathRef("destination_path", event.DestinationPath, mapping.ActiveRoot, mapping.ActiveRelativePath); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported lifecycle transition %q", event.Transition)
	}
	if !validWorkspaceActorID(event.ActorID) || event.OccurredAt.IsZero() {
		return fmt.Errorf("invalid lifecycle event actor or time")
	}
	return validateBoundedText("reason", event.Reason, maxWorkspaceArchiveReasonBytes)
}

func ValidateWorkspaceLifecycleEventForRoots(event WorkspaceLifecycleEvent, roots TrustedWorkspaceRoots) error {
	if err := ValidateWorkspaceLifecycleEvent(event); err != nil {
		return err
	}
	paths, err := ResolveWorkspacePaths(roots, event.Kind, event.Slug)
	if err != nil {
		return err
	}
	source, destination := paths.Active, paths.ArchivePayload
	if event.Transition == WorkspaceTransitionRestored {
		source, destination = paths.ArchivePayload, paths.Active
	}
	if !samePathRef(event.SourcePath, source) || !samePathRef(event.DestinationPath, destination) {
		return fmt.Errorf("lifecycle event paths do not match trusted workspace roots")
	}
	return nil
}

func ValidateWorkspaceLifecycleEventForOperation(event WorkspaceLifecycleEvent, operation WorkspaceArchiveOperation) error {
	if err := ValidateWorkspaceLifecycleEvent(event); err != nil {
		return err
	}
	if err := ValidateWorkspaceArchiveOperation(operation); err != nil {
		return err
	}
	wantTransition := WorkspaceTransitionArchived
	if operation.OperationKind == WorkspaceOperationRestore {
		wantTransition = WorkspaceTransitionRestored
	}
	rank, ok := effectivePhaseRank(operation)
	if !ok || rank < 3 || operation.ProjectionsCommittedAt == nil {
		return fmt.Errorf("lifecycle event requires a projection-committed owning operation")
	}
	if event.OperationID != operation.OperationID ||
		event.Kind != operation.Kind ||
		event.ObjectID != operation.ObjectID ||
		event.Slug != operation.Slug ||
		event.Transition != wantTransition ||
		!samePathRef(event.SourcePath, operation.Source.Path) ||
		!samePathRef(event.DestinationPath, operation.Destination.Path) ||
		event.ActorID != operation.ActorID ||
		event.Reason != operation.Reason ||
		event.OccurredAt.Before(*operation.ProjectionsCommittedAt) ||
		event.OccurredAt.After(operation.UpdatedAt) {
		return fmt.Errorf("lifecycle event contradicts its owning operation")
	}
	return nil
}

func SamePathIdentity(left, right PathIdentity) bool {
	return left == right
}

func plannedPhase(operation WorkspaceOperationKind) WorkspaceArchivePhase {
	if operation == WorkspaceOperationRestore {
		return PhaseRestorePlanned
	}
	return PhaseArchivePlanned
}

func validateTrustedWorkspaceRoot(kind WorkspaceRoot, root string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return fmt.Errorf("trusted %s root must be absolute and clean", kind)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("inspect trusted %s root: %w", kind, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("trusted %s root must be a real directory", kind)
	}
	opened, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("open trusted %s root: %w", kind, err)
	}
	defer opened.Close()
	handle, err := opened.Open(".")
	if err != nil {
		return fmt.Errorf("hold trusted %s root: %w", kind, err)
	}
	openedInfo, statErr := handle.Stat()
	closeErr := handle.Close()
	if statErr != nil {
		return statErr
	}
	if closeErr != nil {
		return closeErr
	}
	if !os.SameFile(info, openedInfo) {
		return fmt.Errorf("trusted %s root changed while opening", kind)
	}
	return nil
}

func openMappedDirectoryNoFollow(roots TrustedWorkspaceRoots, ref WorkspacePathRef) (*os.Root, os.FileInfo, error) {
	base := roots.BoxRoot
	if ref.Root == WorkspaceRootStorage {
		base = roots.StorageRoot
	} else if ref.Root != WorkspaceRootBox {
		return nil, nil, fmt.Errorf("unsupported mapped root %q", ref.Root)
	}
	expectedAbsolute := filepath.Join(base, filepath.FromSlash(ref.RelativePath))
	if ref.AbsolutePath != expectedAbsolute {
		return nil, nil, fmt.Errorf("mapped path does not match trusted root")
	}
	current, err := os.OpenRoot(base)
	if err != nil {
		return nil, nil, fmt.Errorf("open trusted mapped root: %w", err)
	}
	parts := strings.Split(ref.RelativePath, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, `/\\`) {
			_ = current.Close()
			return nil, nil, fmt.Errorf("mapped path contains an unsafe component")
		}
		info, err := current.Lstat(part)
		if err != nil {
			_ = current.Close()
			return nil, nil, fmt.Errorf("inspect mapped directory component %q: %w", part, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			_ = current.Close()
			return nil, nil, fmt.Errorf("mapped directory component %q must be a real directory", part)
		}
		next, err := current.OpenRoot(part)
		if err != nil {
			_ = current.Close()
			return nil, nil, fmt.Errorf("open mapped directory component %q: %w", part, err)
		}
		held, err := next.Open(".")
		if err != nil {
			_ = next.Close()
			_ = current.Close()
			return nil, nil, fmt.Errorf("hold mapped directory component %q: %w", part, err)
		}
		openedInfo, statErr := held.Stat()
		closeErr := held.Close()
		if statErr != nil || closeErr != nil || !os.SameFile(info, openedInfo) {
			_ = next.Close()
			_ = current.Close()
			if statErr != nil {
				return nil, nil, statErr
			}
			if closeErr != nil {
				return nil, nil, closeErr
			}
			return nil, nil, fmt.Errorf("mapped directory component %q changed while opening", part)
		}
		if err := current.Close(); err != nil {
			_ = next.Close()
			return nil, nil, err
		}
		current = next
	}
	info, err := current.Lstat(".")
	if err != nil {
		_ = current.Close()
		return nil, nil, err
	}
	return current, info, nil
}

func inventoryOpenRootNoFollow(ctx context.Context, root *os.Root, rootInfo os.FileInfo) (NoFollowInventory, error) {
	if err := ctx.Err(); err != nil {
		return NoFollowInventory{}, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return NoFollowInventory{}, fmt.Errorf("inventory root must be a real directory")
	}
	rootIdentity, err := pathIdentityFromInfo(rootInfo)
	if err != nil {
		return NoFollowInventory{}, err
	}
	held, err := root.Open(".")
	if err != nil {
		return NoFollowInventory{}, fmt.Errorf("hold inventory root: %w", err)
	}
	heldInfo, statErr := held.Stat()
	closeErr := held.Close()
	if statErr != nil {
		return NoFollowInventory{}, statErr
	}
	if closeErr != nil {
		return NoFollowInventory{}, closeErr
	}
	if !os.SameFile(rootInfo, heldInfo) {
		return NoFollowInventory{}, fmt.Errorf("inventory root changed while opening")
	}

	inventory := NoFollowInventory{SchemaVersion: NoFollowInventorySchemaVersion, RootIdentity: rootIdentity}
	err = fs.WalkDir(root.FS(), ".", func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if current == "." {
			return nil
		}
		relative := pathpkg.Clean(filepath.ToSlash(current))
		if relative == "." || relative == ".." || strings.HasPrefix(relative, "../") || pathpkg.IsAbs(relative) {
			return fmt.Errorf("inventory path escaped root: %q", current)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		identity, err := pathIdentityFromInfo(info)
		if err != nil {
			return err
		}
		if identity.DeviceID != rootIdentity.DeviceID {
			return fmt.Errorf("inventory entry %q crosses a filesystem boundary", relative)
		}
		item := NoFollowInventoryEntry{
			RelativePath:   relative,
			DeviceID:       identity.DeviceID,
			Inode:          identity.Inode,
			Mode:           identity.Mode,
			SizeBytes:      identity.SizeBytes,
			ModifiedUnixNS: identity.ModifiedUnixNS,
		}
		switch {
		case info.Mode().IsRegular():
			item.Kind = InventoryEntryFile
			file, err := root.Open(current)
			if err != nil {
				return err
			}
			openedInfo, err := file.Stat()
			if err != nil {
				_ = file.Close()
				return err
			}
			if !os.SameFile(info, openedInfo) || openedInfo.Mode()&os.ModeSymlink != 0 || !openedInfo.Mode().IsRegular() {
				_ = file.Close()
				return fmt.Errorf("inventory file %q changed while opening", relative)
			}
			hash := sha256.New()
			_, copyErr := io.Copy(hash, contextInventoryReader{ctx: ctx, reader: file})
			afterInfo, afterErr := file.Stat()
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if afterErr != nil {
				return afterErr
			}
			if closeErr != nil {
				return closeErr
			}
			if !stableInventoryFile(openedInfo, afterInfo) {
				return fmt.Errorf("inventory file %q changed while hashing", relative)
			}
			namedAfter, err := root.Lstat(current)
			if err != nil || !os.SameFile(afterInfo, namedAfter) {
				return fmt.Errorf("inventory file %q was substituted while hashing", relative)
			}
			item.ContentDigest = "sha256:" + hex.EncodeToString(hash.Sum(nil))
			inventory.FileCount++
			inventory.TotalBytes += info.Size()
		case info.IsDir():
			item.Kind = InventoryEntryDirectory
			inventory.DirectoryCount++
		case info.Mode()&os.ModeSymlink != 0:
			item.Kind = InventoryEntrySymlink
			target, err := root.Readlink(current)
			if err != nil {
				return err
			}
			if err := validateSymlinkTarget(relative, target); err != nil {
				return err
			}
			item.SymlinkTarget = target
			item.TargetDigest = sha256Digest([]byte(target))
			inventory.SymlinkCount++
		default:
			return fmt.Errorf("inventory entry %q has unsupported special-file type", relative)
		}
		inventory.Entries = append(inventory.Entries, item)
		if len(inventory.Entries) > maxWorkspaceInventoryEntries {
			return fmt.Errorf("inventory contains more than %d entries", maxWorkspaceInventoryEntries)
		}
		return nil
	})
	if err != nil {
		return NoFollowInventory{}, err
	}
	sort.Slice(inventory.Entries, func(i, j int) bool { return inventory.Entries[i].RelativePath < inventory.Entries[j].RelativePath })
	inventory.EntryCount = len(inventory.Entries)
	digest, err := noFollowInventoryDigest(inventory)
	if err != nil {
		return NoFollowInventory{}, err
	}
	inventory.Digest = digest
	rootAfter, err := root.Lstat(".")
	if err != nil || !os.SameFile(rootInfo, rootAfter) {
		return NoFollowInventory{}, fmt.Errorf("inventory root changed while scanning")
	}
	if err := ValidateNoFollowInventory(inventory); err != nil {
		return NoFollowInventory{}, err
	}
	return inventory, nil
}

func noFollowInventoryDigest(inventory NoFollowInventory) (string, error) {
	inventory.Digest = ""
	payload, err := json.Marshal(inventory)
	if err != nil {
		return "", fmt.Errorf("encode no-follow inventory: %w", err)
	}
	return sha256Digest(payload), nil
}

func validateSymlinkTarget(relativePath, target string) error {
	if target == "" || pathpkg.IsAbs(filepath.ToSlash(target)) || filepath.IsAbs(target) {
		return fmt.Errorf("symlink %q has an absolute or empty target", relativePath)
	}
	targetSlash := filepath.ToSlash(target)
	resolved := pathpkg.Clean(pathpkg.Join(pathpkg.Dir(relativePath), targetSlash))
	if resolved == ".." || strings.HasPrefix(resolved, "../") || pathpkg.IsAbs(resolved) {
		return fmt.Errorf("symlink %q escapes inventory root", relativePath)
	}
	return nil
}

func pathIdentityFromInfo(info os.FileInfo) (PathIdentity, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return PathIdentity{}, fmt.Errorf("filesystem identity is unavailable")
	}
	kind := "other"
	switch {
	case info.Mode().IsRegular():
		kind = string(InventoryEntryFile)
	case info.IsDir():
		kind = string(InventoryEntryDirectory)
	case info.Mode()&os.ModeSymlink != 0:
		kind = string(InventoryEntrySymlink)
	}
	return PathIdentity{
		Presence:       PathPresent,
		ObjectKind:     kind,
		DeviceID:       uint64(stat.Dev),
		Inode:          uint64(stat.Ino),
		Mode:           uint32(info.Mode()),
		SizeBytes:      info.Size(),
		ModifiedUnixNS: info.ModTime().UnixNano(),
	}, nil
}

func validatePathBinding(label string, binding WorkspacePathBinding, expected WorkspacePathRef, presence PathPresence) error {
	if !samePathRef(binding.Path, expected) {
		return fmt.Errorf("%s path does not match closed kind/root mapping", label)
	}
	kind := InventoryEntryDirectory
	if presence == PathAbsent {
		kind = ""
	}
	if err := validatePathIdentity(binding.Identity, presence, kind); err != nil {
		return fmt.Errorf("%s identity: %w", label, err)
	}
	if err := validatePathIdentity(binding.ParentIdentity, PathPresent, InventoryEntryDirectory); err != nil {
		return fmt.Errorf("%s parent identity: %w", label, err)
	}
	return nil
}

func validateMappedBindings(operation WorkspaceOperationKind, kind WorkspaceKind, slug string, source, destination WorkspacePathBinding) error {
	mapping, err := WorkspaceMapping(kind, slug)
	if err != nil {
		return err
	}
	var sourceRoot, destinationRoot WorkspaceRoot
	var sourceRelative, destinationRelative string
	switch operation {
	case WorkspaceOperationArchive:
		sourceRoot, sourceRelative = mapping.ActiveRoot, mapping.ActiveRelativePath
		destinationRoot, destinationRelative = mapping.ArchiveRoot, mapping.ArchivePayloadPath
	case WorkspaceOperationRestore:
		sourceRoot, sourceRelative = mapping.ArchiveRoot, mapping.ArchivePayloadPath
		destinationRoot, destinationRelative = mapping.ActiveRoot, mapping.ActiveRelativePath
	default:
		return fmt.Errorf("unsupported workspace operation %q", operation)
	}
	if err := validateMappedPathRef("source", source.Path, sourceRoot, sourceRelative); err != nil {
		return err
	}
	return validateMappedPathRef("destination", destination.Path, destinationRoot, destinationRelative)
}

func validateMappedPathRef(label string, ref WorkspacePathRef, root WorkspaceRoot, relative string) error {
	if ref.Root != root || ref.RelativePath != relative {
		return fmt.Errorf("%s does not match closed kind/root mapping", label)
	}
	if len(ref.RelativePath) > maxWorkspacePathBytes || len(ref.AbsolutePath) > maxWorkspacePathBytes || !filepath.IsAbs(ref.AbsolutePath) || filepath.Clean(ref.AbsolutePath) != ref.AbsolutePath {
		return fmt.Errorf("%s absolute_path must be absolute and clean", label)
	}
	slash := filepath.ToSlash(ref.AbsolutePath)
	if slash != "/"+relative && !strings.HasSuffix(slash, "/"+relative) {
		return fmt.Errorf("%s absolute_path does not end at its mapped relative path", label)
	}
	return nil
}

func validatePathIdentity(identity PathIdentity, presence PathPresence, kind InventoryEntryKind) error {
	if identity.Presence != presence {
		return fmt.Errorf("presence = %q, want %q", identity.Presence, presence)
	}
	if presence == PathAbsent {
		if identity.ObjectKind != "" || identity.DeviceID != 0 || identity.Inode != 0 || identity.Mode != 0 || identity.SizeBytes != 0 || identity.ModifiedUnixNS != 0 {
			return fmt.Errorf("absent path identity must not contain present-object evidence")
		}
		return nil
	}
	if identity.DeviceID == 0 || identity.Inode == 0 || identity.Mode == 0 || identity.ModifiedUnixNS == 0 || identity.SizeBytes < 0 {
		return fmt.Errorf("present path identity is incomplete")
	}
	if kind != "" && identity.ObjectKind != string(kind) {
		return fmt.Errorf("object_kind = %q, want %q", identity.ObjectKind, kind)
	}
	return nil
}

func validateWorkspaceObject(kind WorkspaceKind, objectID, slug string) error {
	if _, err := WorkspaceMapping(kind, slug); err != nil {
		return err
	}
	if !workspaceIDPattern.MatchString(objectID) && !(kind == WorkspaceKindProject && workspaceProjectIDPattern.MatchString(objectID)) {
		return fmt.Errorf("invalid stable workspace object_id")
	}
	return nil
}

func validWorkspaceActorID(actorID string) bool {
	return workspaceActorIDPattern.MatchString(actorID) || workspaceIDPattern.MatchString(actorID)
}

func statusForPhase(phase WorkspaceArchivePhase) (WorkspaceArchiveOperationStatus, bool) {
	switch phase {
	case PhaseArchivePlanned, PhaseRestorePlanned:
		return OperationStatusPending, true
	case PhaseArchiveIntentCommitted, PhaseArchivePayloadMoved, PhaseArchiveProjectionsCommitted,
		PhaseRestoreIntentCommitted, PhaseRestorePayloadMoved, PhaseRestoreProjectionsCommitted:
		return OperationStatusRunning, true
	case PhaseArchiveComplete, PhaseRestoreComplete:
		return OperationStatusComplete, true
	case PhaseBlocked:
		return OperationStatusBlocked, true
	case PhaseManualRepairRequired:
		return OperationStatusManualRepair, true
	default:
		return "", false
	}
}

func phaseRank(operation WorkspaceOperationKind, phase WorkspaceArchivePhase) (int, bool) {
	archive := map[WorkspaceArchivePhase]int{
		PhaseArchivePlanned:              0,
		PhaseArchiveIntentCommitted:      1,
		PhaseArchivePayloadMoved:         2,
		PhaseArchiveProjectionsCommitted: 3,
		PhaseArchiveComplete:             4,
	}
	restore := map[WorkspaceArchivePhase]int{
		PhaseRestorePlanned:              0,
		PhaseRestoreIntentCommitted:      1,
		PhaseRestorePayloadMoved:         2,
		PhaseRestoreProjectionsCommitted: 3,
		PhaseRestoreComplete:             4,
	}
	if operation == WorkspaceOperationArchive {
		rank, ok := archive[phase]
		return rank, ok
	}
	if operation == WorkspaceOperationRestore {
		rank, ok := restore[phase]
		return rank, ok
	}
	return 0, false
}

func validPhase(phase WorkspaceArchivePhase) bool {
	_, ok := statusForPhase(phase)
	return ok
}

func validLastSafePhase(phase WorkspaceArchivePhase) bool {
	if rank, ok := phaseRank(WorkspaceOperationArchive, phase); ok {
		return rank < 4
	}
	if rank, ok := phaseRank(WorkspaceOperationRestore, phase); ok {
		return rank < 4
	}
	return false
}

func effectivePhaseRank(operation WorkspaceArchiveOperation) (int, bool) {
	effective := operation.Phase
	if effective == PhaseBlocked || effective == PhaseManualRepairRequired {
		effective = operation.LastSafePhase
	}
	return phaseRank(operation.OperationKind, effective)
}

func validFindingCode(code WorkspaceArchiveFindingCode) bool {
	switch code {
	case FindingDestinationCollision, FindingSymlinkEscape, FindingCrossDevice, FindingSourceDrift,
		FindingDestinationSubstitution, FindingReplayConflict, FindingHistoricalCopyEvidence,
		FindingAmbiguousCustody, FindingInvalidBinding, FindingManualRepairRequired:
		return true
	default:
		return false
	}
}

func samePathRef(left, right WorkspacePathRef) bool {
	return left.Root == right.Root && left.RelativePath == right.RelativePath && left.AbsolutePath == right.AbsolutePath
}

func stableInventoryFile(left, right os.FileInfo) bool {
	return os.SameFile(left, right) && left.Mode() == right.Mode() && left.Size() == right.Size() && left.ModTime().Equal(right.ModTime())
}

func validateOperationTimestamps(operation WorkspaceArchiveOperation, rank int) error {
	timestamps := []struct {
		name     string
		value    *time.Time
		required bool
	}{
		{"intent_committed_at", operation.IntentCommittedAt, rank >= 1},
		{"payload_moved_at", operation.PayloadMovedAt, rank >= 2},
		{"projections_committed_at", operation.ProjectionsCommittedAt, rank >= 3},
		{"completed_at", operation.CompletedAt, rank >= 4},
	}
	previousName := "planned_at"
	previous := operation.PlannedAt
	for _, timestamp := range timestamps {
		if (timestamp.value != nil) != timestamp.required {
			if timestamp.required {
				return fmt.Errorf("%s is required at the effective phase", timestamp.name)
			}
			return fmt.Errorf("%s is forbidden before its effective phase", timestamp.name)
		}
		if timestamp.value == nil {
			continue
		}
		if timestamp.value.Before(previous) {
			return fmt.Errorf("%s precedes %s", timestamp.name, previousName)
		}
		previousName = timestamp.name
		previous = *timestamp.value
	}
	if operation.UpdatedAt.Before(previous) {
		return fmt.Errorf("updated_at precedes %s", previousName)
	}
	return nil
}

func sha256Digest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validateBoundedText(label, value string, maxBytes int) error {
	if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) || len(value) > maxBytes {
		return fmt.Errorf("%s must be non-empty, trimmed, and at most %d bytes", label, maxBytes)
	}
	return nil
}

type contextInventoryReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextInventoryReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}
