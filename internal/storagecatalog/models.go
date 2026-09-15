package storagecatalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"loom.local/loom/internal/filesystemmeta"
)

var ErrInvalid = errors.New("invalid storage catalog input")

const (
	StorageClassObjectBlob    = "object_blob"
	StorageClassPrivateBackup = "private_backup"
	// StorageClassDropzoneCustody is retained for historical decode/filtering.
	StorageClassDropzoneCustody   = "dropzone_custody"
	StorageClassLaneCustody       = "lane_custody"
	StorageClassMainDocument      = "main_document"
	StorageClassArchiveEntry      = "archive_entry"
	StorageClassViewEntry         = "view_entry"
	StorageClassRetentionSnapshot = "retention_snapshot"
)

const (
	SourceAreaProjects  = "projects"
	SourceAreaNotes     = "notes"
	SourceAreaDocuments = "documents"
	// SourceAreaDropzone is retained for historical decode/filtering.
	SourceAreaDropzone            = "dropzone"
	SourceAreaLane                = "lane"
	SourceAreaExternalWatchedRoot = "external_watched_root"
	SourceAreaMainDocuments       = "main_documents"
	SourceAreaMainArchive         = "main_archive"
	SourceAreaUnknown             = "unknown"
)

const (
	FileClassMarkdown          = "markdown"
	FileClassText              = "text"
	FileClassPDF               = "pdf"
	FileClassImage             = "image"
	FileClassVideo             = "video"
	FileClassAudio             = "audio"
	FileClassArchive           = "archive"
	FileClassCode              = "code"
	FileClassOfficeDocument    = "office_document"
	FileClassDirectory         = "directory"
	FileClassPackage           = "package"
	FileClassGeneratedMetadata = "generated_metadata"
	FileClassBinary            = "binary"
	FileClassUnknown           = "unknown"
)

const (
	ClassificationSourceExtension         = "extension"
	ClassificationSourceMIME              = "mime"
	ClassificationSourceContentSniff      = "content_sniff"
	ClassificationSourceShebang           = "shebang"
	ClassificationSourcePackageExtension  = "package_extension"
	ClassificationSourceGeneratedMetadata = "generated_metadata"
	ClassificationSourceManualPolicy      = "manual_policy"
)

const (
	IndexingStateIndexed          = "indexed"
	IndexingStateMetadataOnly     = "metadata_only"
	IndexingStateTooLarge         = "too_large"
	IndexingStateBinary           = "binary"
	IndexingStateUnsupported      = "unsupported"
	IndexingStatePermissionDenied = "permission_denied"
	IndexingStateGeneratedIgnored = "generated_ignored"
)

const (
	ProcessingStateUnprocessed      = "unprocessed"
	ProcessingStateBackupOnly       = "backup_only"
	ProcessingStateObjectRegistered = "object_registered"
	ProcessingStateTextIndexed      = "text_indexed"
	ProcessingStateMetadataOnly     = "metadata_only"
	ProcessingStateExcluded         = "excluded"
	ProcessingStateFailed           = "failed"
)

const (
	AvailabilityStateDiscovered = "discovered"
	AvailabilityStatePending    = "pending"
	AvailabilityStateAvailable  = "available"
	AvailabilityStateDeleted    = "deleted"
	AvailabilityStateTombstoned = "tombstoned"
	AvailabilityStateArchived   = "archived"
	AvailabilityStateSuperseded = "superseded"
	AvailabilityStateFailed     = "failed"
)

const (
	RetentionStateNone     = "none"
	RetentionStateRetained = "retained"
	RetentionStateSnapshot = "snapshot"
	RetentionStateExpired  = "expired"
	RetentionStatePending  = "pending"
)

const (
	PhysicalRefKindLocalPath      = "local_path"
	PhysicalRefKindObjectBlob     = "object_blob"
	PhysicalRefKindBackupArtifact = "backup_artifact"
	// PhysicalRefKindDropzoneFile is retained for historical decode/filtering.
	PhysicalRefKindDropzoneFile     = "dropzone_file"
	PhysicalRefKindLaneFile         = "lane_file"
	PhysicalRefKindRetentionPayload = "retention_payload"
	PhysicalRefKindArchiveFile      = "archive_file"
	PhysicalRefKindExternalURI      = "external_uri"
	PhysicalRefKindCloudObject      = "cloud_object"
)

const (
	PhysicalRefStatusAvailable  = "available"
	PhysicalRefStatusMissing    = "missing"
	PhysicalRefStatusVerifying  = "verifying"
	PhysicalRefStatusFailed     = "failed"
	PhysicalRefStatusSuperseded = "superseded"
)

const (
	PhysicalBackendLocal = "local"
	PhysicalBackendCloud = "cloud"
)

const (
	CustodyStateLocalPrimary      = "local_primary"
	CustodyStateReplicatedToCloud = "replicated_to_cloud"
	CustodyStateCloudOnly         = "cloud_only"
)

const (
	TombstoneKindSourceDeleted     = "source_deleted"
	TombstoneKindArchivedElsewhere = "archived_elsewhere"
	TombstoneKindManualDelete      = "manual_delete"
	TombstoneKindRetentionExpired  = "retention_expired"
)

type Entry struct {
	StorageEntryID           string          `json:"storage_entry_id"`
	StorageClass             string          `json:"storage_class"`
	SourceArea               string          `json:"source_area"`
	OriginNodeID             *string         `json:"origin_node_id,omitempty"`
	OriginNodeKey            string          `json:"origin_node_key,omitempty"`
	ProjectID                *string         `json:"project_id,omitempty"`
	WatchedRootID            *string         `json:"watched_root_id,omitempty"`
	WatchedRootKey           string          `json:"watched_root_key,omitempty"`
	DropzoneTransferID       string          `json:"dropzone_transfer_id,omitempty"`
	PrivateBackupOperationID *string         `json:"private_backup_operation_id,omitempty"`
	PrivateBackupItemID      *string         `json:"private_backup_item_id,omitempty"`
	ObjectID                 *string         `json:"object_id,omitempty"`
	ObjectVersionID          *string         `json:"object_version_id,omitempty"`
	ArchiveManifestID        *string         `json:"archive_manifest_id,omitempty"`
	LogicalPath              string          `json:"logical_path"`
	OriginalSourcePath       string          `json:"original_source_path,omitempty"`
	CurrentViewPath          string          `json:"current_view_path,omitempty"`
	ChecksumAlgorithm        string          `json:"checksum_algorithm,omitempty"`
	ChecksumHex              string          `json:"checksum_hex,omitempty"`
	SizeBytes                *int64          `json:"size_bytes,omitempty"`
	MimeType                 string          `json:"mime_type,omitempty"`
	FileClass                string          `json:"file_class"`
	ProcessingState          string          `json:"processing_state"`
	AvailabilityState        string          `json:"availability_state"`
	RetentionState           string          `json:"retention_state"`
	Metadata                 json.RawMessage `json:"metadata"`
	CreatedAt                time.Time       `json:"created_at"`
	UpdatedAt                time.Time       `json:"updated_at"`
	DeletedAt                *time.Time      `json:"deleted_at,omitempty"`
}

type PhysicalRef struct {
	StoragePhysicalRefID  string          `json:"storage_physical_ref_id"`
	StorageEntryID        string          `json:"storage_entry_id"`
	StorageEntryVersionID *string         `json:"storage_entry_version_id,omitempty"`
	RefKind               string          `json:"ref_kind"`
	URI                   string          `json:"uri"`
	NodeID                *string         `json:"node_id,omitempty"`
	NodeKey               string          `json:"node_key,omitempty"`
	ContentAddress        string          `json:"content_address,omitempty"`
	Status                string          `json:"status"`
	Metadata              json.RawMessage `json:"metadata"`
	CreatedAt             time.Time       `json:"created_at"`
	UpdatedAt             time.Time       `json:"updated_at"`
}

type EntryDetail struct {
	Entry        Entry         `json:"entry"`
	PhysicalRefs []PhysicalRef `json:"physical_refs,omitempty"`
}

type InspectOptions struct {
	IncludeDeleted bool `json:"include_deleted,omitempty"`
}

type MainDocumentProtectionInput struct {
	RelativePath  string                  `json:"relative_path,omitempty"`
	ViewPath      string                  `json:"view_path,omitempty"`
	CloudCoverage MainDocumentCloudBackup `json:"cloud_coverage,omitempty"`
}

type MainDocumentCloudBackup struct {
	Confirmed              bool       `json:"confirmed"`
	Ref                    string     `json:"ref,omitempty"`
	VerifiedAt             *time.Time `json:"verified_at,omitempty"`
	CoversStorageRetention bool       `json:"covers_storage_retention"`
}

type MainDocumentProtectionStatus struct {
	AcceptedByCatalog         bool       `json:"accepted_by_catalog"`
	StorageEntryID            string     `json:"storage_entry_id,omitempty"`
	ViewPath                  string     `json:"view_path,omitempty"`
	LogicalPath               string     `json:"logical_path,omitempty"`
	RelativePath              string     `json:"relative_path,omitempty"`
	AvailabilityState         string     `json:"availability_state"`
	RetentionState            string     `json:"retention_state,omitempty"`
	CurrentSourcePresent      bool       `json:"current_source_present"`
	CurrentSourcePath         string     `json:"current_source_path,omitempty"`
	RetainedPayloadPresent    bool       `json:"retained_payload_present"`
	RetainedPayloadPath       string     `json:"retained_payload_path,omitempty"`
	RetainedPayloadVerified   bool       `json:"retained_payload_verified"`
	RetainedPayloadVerifiedAt *time.Time `json:"retained_payload_verified_at,omitempty"`
	CloudBackupConfirmed      bool       `json:"cloud_backup_confirmed"`
	CloudBackupRef            string     `json:"cloud_backup_ref,omitempty"`
	SafeToDeleteSource        bool       `json:"safe_to_delete_source"`
	UnsafeReason              string     `json:"unsafe_reason,omitempty"`
	CheckedAt                 time.Time  `json:"checked_at"`
}

type EntryVersion struct {
	StorageEntryVersionID string          `json:"storage_entry_version_id"`
	StorageEntryID        string          `json:"storage_entry_id"`
	VersionNumber         int             `json:"version_number"`
	ChecksumAlgorithm     string          `json:"checksum_algorithm,omitempty"`
	ChecksumHex           string          `json:"checksum_hex,omitempty"`
	SizeBytes             *int64          `json:"size_bytes,omitempty"`
	PhysicalRefID         *string         `json:"physical_ref_id,omitempty"`
	Metadata              json.RawMessage `json:"metadata"`
	CreatedAt             time.Time       `json:"created_at"`
}

type RetentionEntry struct {
	StorageRetentionEntryID string          `json:"storage_retention_entry_id"`
	StorageEntryID          string          `json:"storage_entry_id"`
	PolicyKey               string          `json:"policy_key"`
	RetentionState          string          `json:"retention_state"`
	RetainedUntil           *time.Time      `json:"retained_until,omitempty"`
	Metadata                json.RawMessage `json:"metadata"`
	CreatedAt               time.Time       `json:"created_at"`
	UpdatedAt               time.Time       `json:"updated_at"`
}

type Tombstone struct {
	StorageTombstoneID string          `json:"storage_tombstone_id"`
	StorageEntryID     string          `json:"storage_entry_id"`
	TombstoneKind      string          `json:"tombstone_kind"`
	Reason             string          `json:"reason,omitempty"`
	CreatedBy          string          `json:"created_by,omitempty"`
	Metadata           json.RawMessage `json:"metadata"`
	CreatedAt          time.Time       `json:"created_at"`
}

type ArchiveManifest struct {
	ArchiveManifestID string          `json:"archive_manifest_id"`
	ArchiveKey        string          `json:"archive_key"`
	ArchiveKind       string          `json:"archive_kind"`
	OwnerNodeID       *string         `json:"owner_node_id,omitempty"`
	OwnerNodeKey      string          `json:"owner_node_key,omitempty"`
	SourceRef         string          `json:"source_ref,omitempty"`
	Status            string          `json:"status"`
	ManifestJSON      json.RawMessage `json:"manifest_json"`
	CreatedAt         time.Time       `json:"created_at"`
	FinalizedAt       *time.Time      `json:"finalized_at,omitempty"`
}

type FilesystemObservation struct {
	StorageFilesystemObservationID string          `json:"storage_filesystem_observation_id"`
	StorageEntryID                 *string         `json:"storage_entry_id,omitempty"`
	SourceArea                     string          `json:"source_area"`
	SourceNodeID                   *string         `json:"source_node_id,omitempty"`
	SourceNodeKey                  string          `json:"source_node_key,omitempty"`
	SourceRef                      string          `json:"source_ref,omitempty"`
	LogicalPath                    string          `json:"logical_path"`
	ObjectKind                     string          `json:"object_kind"`
	SourceMode                     *int            `json:"source_mode,omitempty"`
	Executable                     bool            `json:"executable"`
	UID                            *int            `json:"uid,omitempty"`
	GID                            *int            `json:"gid,omitempty"`
	UserName                       string          `json:"user_name,omitempty"`
	GroupName                      string          `json:"group_name,omitempty"`
	SymlinkTarget                  string          `json:"symlink_target,omitempty"`
	DeviceID                       *int64          `json:"device_id,omitempty"`
	Inode                          *int64          `json:"inode,omitempty"`
	LinkCount                      *int64          `json:"link_count,omitempty"`
	IsHardLink                     bool            `json:"is_hard_link"`
	IsSparse                       bool            `json:"is_sparse"`
	LogicalSizeBytes               int64           `json:"logical_size_bytes"`
	AllocatedBytes                 *int64          `json:"allocated_bytes,omitempty"`
	HasXattrs                      bool            `json:"has_xattrs"`
	XattrNames                     []string        `json:"xattr_names,omitempty"`
	HasACL                         bool            `json:"has_acl"`
	HasResourceFork                bool            `json:"has_resource_fork"`
	HasFinderTags                  bool            `json:"has_finder_tags"`
	HasQuarantine                  bool            `json:"has_quarantine"`
	IsPackage                      bool            `json:"is_package"`
	PackageKind                    string          `json:"package_kind,omitempty"`
	UnicodeForm                    string          `json:"unicode_form,omitempty"`
	CasefoldKey                    string          `json:"casefold_key,omitempty"`
	Hidden                         bool            `json:"hidden"`
	GeneratedMetadata              bool            `json:"generated_metadata"`
	PermissionDenied               bool            `json:"permission_denied"`
	Risks                          []string        `json:"risks,omitempty"`
	ObservedAt                     time.Time       `json:"observed_at"`
	RawJSON                        json.RawMessage `json:"raw_json"`
	CreatedAt                      time.Time       `json:"created_at"`
	UpdatedAt                      time.Time       `json:"updated_at"`
}

type FidelityFinding struct {
	StorageFidelityFindingID       string          `json:"storage_fidelity_finding_id"`
	StorageFilesystemObservationID *string         `json:"storage_filesystem_observation_id,omitempty"`
	StorageEntryID                 *string         `json:"storage_entry_id,omitempty"`
	NodeID                         *string         `json:"node_id,omitempty"`
	NodeKey                        string          `json:"node_key,omitempty"`
	SourceArea                     string          `json:"source_area"`
	SourceRef                      string          `json:"source_ref,omitempty"`
	LogicalPath                    string          `json:"logical_path,omitempty"`
	Severity                       string          `json:"severity"`
	FindingKind                    string          `json:"finding_kind"`
	Summary                        string          `json:"summary,omitempty"`
	DetailJSON                     json.RawMessage `json:"detail_json"`
	Status                         string          `json:"status"`
	CreatedAt                      time.Time       `json:"created_at"`
	UpdatedAt                      time.Time       `json:"updated_at"`
	ResolvedAt                     *time.Time      `json:"resolved_at,omitempty"`
}

type RetentionSnapshotInput struct {
	StorageEntryVersionID   string          `json:"storage_entry_version_id,omitempty"`
	StorageRetentionEntryID string          `json:"storage_retention_entry_id,omitempty"`
	StorageEntryID          string          `json:"storage_entry_id"`
	StoragePhysicalRefID    string          `json:"storage_physical_ref_id,omitempty"`
	PolicyKey               string          `json:"policy_key,omitempty"`
	RetentionState          string          `json:"retention_state,omitempty"`
	RetainedUntil           *time.Time      `json:"retained_until,omitempty"`
	Metadata                json.RawMessage `json:"metadata,omitempty"`
}

type RetentionSnapshotResult struct {
	Entry     Entry          `json:"entry"`
	Version   EntryVersion   `json:"version"`
	Retention RetentionEntry `json:"retention"`
}

type TombstoneInput struct {
	StorageTombstoneID string          `json:"storage_tombstone_id,omitempty"`
	StorageEntryID     string          `json:"storage_entry_id"`
	TombstoneKind      string          `json:"tombstone_kind,omitempty"`
	Reason             string          `json:"reason,omitempty"`
	CreatedBy          string          `json:"created_by,omitempty"`
	Metadata           json.RawMessage `json:"metadata,omitempty"`
	MarkEntry          bool            `json:"mark_entry,omitempty"`
}

type RetentionStatus struct {
	Entries          int       `json:"entries"`
	Retained         int       `json:"retained"`
	Snapshots        int       `json:"snapshots"`
	Pending          int       `json:"pending"`
	Expired          int       `json:"expired"`
	Tombstoned       int       `json:"tombstoned"`
	Failed           int       `json:"failed"`
	GeneratedAt      time.Time `json:"generated_at"`
	SafeCandidates   int       `json:"safe_candidates"`
	UnsafeCandidates int       `json:"unsafe_candidates"`
}

type CreateArchiveManifestInput struct {
	ArchiveManifestID string          `json:"archive_manifest_id,omitempty"`
	ArchiveKey        string          `json:"archive_key,omitempty"`
	ArchiveKind       string          `json:"archive_kind,omitempty"`
	OwnerNodeID       string          `json:"owner_node_id,omitempty"`
	OwnerNodeKey      string          `json:"owner_node_key,omitempty"`
	SourceRef         string          `json:"source_ref,omitempty"`
	Status            string          `json:"status,omitempty"`
	ManifestJSON      json.RawMessage `json:"manifest_json,omitempty"`
	FinalizedAt       *time.Time      `json:"finalized_at,omitempty"`
}

type ArchiveItemInput struct {
	ArchiveManifestID string          `json:"archive_manifest_id"`
	StorageEntryID    string          `json:"storage_entry_id"`
	ArchivePath       string          `json:"archive_path"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
}

// ArchiveCommitEntryInput is one immutable archive catalog tuple. The service
// commits the manifest, entries, physical refs, items, and optional source
// dispositions in one database transaction.
type ArchiveCommitEntryInput struct {
	Entry RegisterEntryInput       `json:"entry"`
	Ref   RegisterPhysicalRefInput `json:"ref"`
	Item  ArchiveItemInput         `json:"item"`
}

type ArchiveSourceDispositionInput struct {
	StorageEntryID       string `json:"storage_entry_id"`
	ExpectedAvailability string `json:"expected_availability_state"`
	AvailabilityState    string `json:"availability_state"`
}

type ArchiveCommitInput struct {
	Manifest           CreateArchiveManifestInput      `json:"manifest"`
	Entries            []ArchiveCommitEntryInput       `json:"entries"`
	SourceDispositions []ArchiveSourceDispositionInput `json:"source_dispositions,omitempty"`
}

type ArchiveCommitResult struct {
	Manifest ArchiveManifest `json:"manifest"`
	Entries  []Entry         `json:"entries"`
	Refs     []PhysicalRef   `json:"physical_refs"`
}

type RegisterEntryInput struct {
	StorageEntryID           string          `json:"storage_entry_id,omitempty"`
	StorageClass             string          `json:"storage_class"`
	SourceArea               string          `json:"source_area,omitempty"`
	OriginNodeID             string          `json:"origin_node_id,omitempty"`
	OriginNodeKey            string          `json:"origin_node_key,omitempty"`
	ProjectID                string          `json:"project_id,omitempty"`
	WatchedRootID            string          `json:"watched_root_id,omitempty"`
	WatchedRootKey           string          `json:"watched_root_key,omitempty"`
	DropzoneTransferID       string          `json:"dropzone_transfer_id,omitempty"`
	PrivateBackupOperationID string          `json:"private_backup_operation_id,omitempty"`
	PrivateBackupItemID      string          `json:"private_backup_item_id,omitempty"`
	ObjectID                 string          `json:"object_id,omitempty"`
	ObjectVersionID          string          `json:"object_version_id,omitempty"`
	ArchiveManifestID        string          `json:"archive_manifest_id,omitempty"`
	LogicalPath              string          `json:"logical_path"`
	OriginalSourcePath       string          `json:"original_source_path,omitempty"`
	CurrentViewPath          string          `json:"current_view_path,omitempty"`
	ChecksumAlgorithm        string          `json:"checksum_algorithm,omitempty"`
	ChecksumHex              string          `json:"checksum_hex,omitempty"`
	SizeBytes                *int64          `json:"size_bytes,omitempty"`
	MimeType                 string          `json:"mime_type,omitempty"`
	FileClass                string          `json:"file_class,omitempty"`
	ProcessingState          string          `json:"processing_state,omitempty"`
	AvailabilityState        string          `json:"availability_state,omitempty"`
	RetentionState           string          `json:"retention_state,omitempty"`
	Metadata                 json.RawMessage `json:"metadata,omitempty"`
}

type RegisterPhysicalRefInput struct {
	StoragePhysicalRefID  string          `json:"storage_physical_ref_id,omitempty"`
	StorageEntryID        string          `json:"storage_entry_id"`
	StorageEntryVersionID string          `json:"storage_entry_version_id,omitempty"`
	RefKind               string          `json:"ref_kind"`
	URI                   string          `json:"uri"`
	NodeID                string          `json:"node_id,omitempty"`
	NodeKey               string          `json:"node_key,omitempty"`
	ContentAddress        string          `json:"content_address,omitempty"`
	Status                string          `json:"status,omitempty"`
	Metadata              json.RawMessage `json:"metadata,omitempty"`
}

type PhysicalRefPathRebind struct {
	StoragePhysicalRefID string `json:"storage_physical_ref_id"`
	StorageEntryID       string `json:"storage_entry_id"`
	ExpectedURI          string `json:"expected_uri"`
	NewURI               string `json:"new_uri"`
}

type EntryPathRebind struct {
	StorageEntryID             string `json:"storage_entry_id"`
	ExpectedOriginalSourcePath string `json:"expected_original_source_path,omitempty"`
	NewOriginalSourcePath      string `json:"new_original_source_path,omitempty"`
	ExpectedCurrentViewPath    string `json:"expected_current_view_path,omitempty"`
	NewCurrentViewPath         string `json:"new_current_view_path,omitempty"`
}

type RebindPathsInput struct {
	PhysicalRefs []PhysicalRefPathRebind `json:"physical_refs,omitempty"`
	Entries      []EntryPathRebind       `json:"entries,omitempty"`
}

// RebindPathsBatchLoader reloads one bounded child of a reviewed migration
// artifact. Callers must treat index as zero-based and return deterministic
// content every time it is requested: the catalog preflights all children,
// then reloads them while applying the complete set in one transaction.
type RebindPathsBatchLoader func(index int) (RebindPathsInput, error)

type RebindPathsResult struct {
	PhysicalRefsUpdated int `json:"physical_refs_updated"`
	EntriesUpdated      int `json:"entries_updated"`
	AlreadyApplied      int `json:"already_applied"`
}

type RegisterFilesystemObservationInput struct {
	StorageFilesystemObservationID string          `json:"storage_filesystem_observation_id,omitempty"`
	StorageEntryID                 string          `json:"storage_entry_id,omitempty"`
	SourceArea                     string          `json:"source_area,omitempty"`
	SourceNodeID                   string          `json:"source_node_id,omitempty"`
	SourceNodeKey                  string          `json:"source_node_key,omitempty"`
	SourceRef                      string          `json:"source_ref,omitempty"`
	LogicalPath                    string          `json:"logical_path"`
	ObjectKind                     string          `json:"object_kind,omitempty"`
	SourceMode                     *int            `json:"source_mode,omitempty"`
	Executable                     bool            `json:"executable,omitempty"`
	UID                            *int            `json:"uid,omitempty"`
	GID                            *int            `json:"gid,omitempty"`
	UserName                       string          `json:"user_name,omitempty"`
	GroupName                      string          `json:"group_name,omitempty"`
	SymlinkTarget                  string          `json:"symlink_target,omitempty"`
	DeviceID                       *int64          `json:"device_id,omitempty"`
	Inode                          *int64          `json:"inode,omitempty"`
	LinkCount                      *int64          `json:"link_count,omitempty"`
	IsHardLink                     bool            `json:"is_hard_link,omitempty"`
	IsSparse                       bool            `json:"is_sparse,omitempty"`
	LogicalSizeBytes               int64           `json:"logical_size_bytes,omitempty"`
	AllocatedBytes                 *int64          `json:"allocated_bytes,omitempty"`
	HasXattrs                      bool            `json:"has_xattrs,omitempty"`
	XattrNames                     []string        `json:"xattr_names,omitempty"`
	HasACL                         bool            `json:"has_acl,omitempty"`
	HasResourceFork                bool            `json:"has_resource_fork,omitempty"`
	HasFinderTags                  bool            `json:"has_finder_tags,omitempty"`
	HasQuarantine                  bool            `json:"has_quarantine,omitempty"`
	IsPackage                      bool            `json:"is_package,omitempty"`
	PackageKind                    string          `json:"package_kind,omitempty"`
	UnicodeForm                    string          `json:"unicode_form,omitempty"`
	CasefoldKey                    string          `json:"casefold_key,omitempty"`
	Hidden                         bool            `json:"hidden,omitempty"`
	GeneratedMetadata              bool            `json:"generated_metadata,omitempty"`
	PermissionDenied               bool            `json:"permission_denied,omitempty"`
	Risks                          []string        `json:"risks,omitempty"`
	ObservedAt                     *time.Time      `json:"observed_at,omitempty"`
	RawJSON                        json.RawMessage `json:"raw_json,omitempty"`
}

type RegisterFidelityFindingInput struct {
	StorageFidelityFindingID       string          `json:"storage_fidelity_finding_id,omitempty"`
	StorageFilesystemObservationID string          `json:"storage_filesystem_observation_id,omitempty"`
	StorageEntryID                 string          `json:"storage_entry_id,omitempty"`
	NodeID                         string          `json:"node_id,omitempty"`
	NodeKey                        string          `json:"node_key,omitempty"`
	SourceArea                     string          `json:"source_area,omitempty"`
	SourceRef                      string          `json:"source_ref,omitempty"`
	LogicalPath                    string          `json:"logical_path,omitempty"`
	Severity                       string          `json:"severity,omitempty"`
	FindingKind                    string          `json:"finding_kind"`
	Summary                        string          `json:"summary,omitempty"`
	DetailJSON                     json.RawMessage `json:"detail_json,omitempty"`
	Status                         string          `json:"status,omitempty"`
	ResolvedAt                     *time.Time      `json:"resolved_at,omitempty"`
}

type ListFilter struct {
	StorageClass  string
	SourceArea    string
	OriginNodeKey string
	// PathPrefixes limits results to entries whose current view, logical, or
	// original source path is the exact relative prefix or a descendant. The
	// service escapes SQL wildcard characters; callers cannot broaden a query
	// by supplying LIKE syntax.
	PathPrefixes      []string
	FileClass         string
	ProcessingState   string
	AvailabilityState string
	IncludeDeleted    bool
	Limit             int
	Offset            int
}

type FilesystemObservationFilter struct {
	StorageEntryID string
	SourceArea     string
	SourceNodeKey  string
	SourceRef      string
	LogicalPath    string
	ObjectKind     string
	PermissionOnly bool
	Limit          int
	Offset         int
}

type FidelityFindingFilter struct {
	StorageEntryID                 string
	StorageFilesystemObservationID string
	NodeKey                        string
	SourceArea                     string
	SourceRef                      string
	LogicalPath                    string
	Severity                       string
	Status                         string
	IncludeResolved                bool
	Limit                          int
	Offset                         int
}

func ValidStorageClass(value string) bool {
	return validStorageClasses[value]
}

func ValidSourceArea(value string) bool {
	return validSourceAreas[value]
}

func ValidFileClass(value string) bool {
	return validFileClasses[value]
}

func ValidProcessingState(value string) bool {
	return validProcessingStates[value]
}

func ValidAvailabilityState(value string) bool {
	return validAvailabilityStates[value]
}

func ValidRetentionState(value string) bool {
	return validRetentionStates[value]
}

func ValidPhysicalRefKind(value string) bool {
	return validPhysicalRefKinds[value]
}

func ValidPhysicalRefStatus(value string) bool {
	return validPhysicalRefStatuses[value]
}

func ValidTombstoneKind(value string) bool {
	return validTombstoneKinds[value]
}

func ValidFilesystemObjectKind(value string) bool {
	return filesystemmeta.ValidObjectKind(value)
}

func ValidFidelityRisk(value string) bool {
	return filesystemmeta.ValidFidelityRisk(value)
}

func ValidFidelityFindingSeverity(value string) bool {
	return filesystemmeta.ValidFindingSeverity(value)
}

func ValidFidelityFindingStatus(value string) bool {
	return filesystemmeta.ValidFindingStatus(value)
}

func requireValid(label, value string, valid func(string) bool) error {
	if !valid(value) {
		return fmt.Errorf("%w: unsupported %s %q", ErrInvalid, label, value)
	}
	return nil
}

var validStorageClasses = map[string]bool{
	StorageClassObjectBlob:        true,
	StorageClassPrivateBackup:     true,
	StorageClassDropzoneCustody:   true,
	StorageClassLaneCustody:       true,
	StorageClassMainDocument:      true,
	StorageClassArchiveEntry:      true,
	StorageClassViewEntry:         true,
	StorageClassRetentionSnapshot: true,
}

var validSourceAreas = map[string]bool{
	SourceAreaProjects:            true,
	SourceAreaNotes:               true,
	SourceAreaDocuments:           true,
	SourceAreaDropzone:            true,
	SourceAreaLane:                true,
	SourceAreaExternalWatchedRoot: true,
	SourceAreaMainDocuments:       true,
	SourceAreaMainArchive:         true,
	SourceAreaUnknown:             true,
}

var validFileClasses = map[string]bool{
	FileClassMarkdown:          true,
	FileClassText:              true,
	FileClassPDF:               true,
	FileClassImage:             true,
	FileClassVideo:             true,
	FileClassAudio:             true,
	FileClassArchive:           true,
	FileClassCode:              true,
	FileClassOfficeDocument:    true,
	FileClassDirectory:         true,
	FileClassPackage:           true,
	FileClassGeneratedMetadata: true,
	FileClassBinary:            true,
	FileClassUnknown:           true,
}

var validProcessingStates = map[string]bool{
	ProcessingStateUnprocessed:      true,
	ProcessingStateBackupOnly:       true,
	ProcessingStateObjectRegistered: true,
	ProcessingStateTextIndexed:      true,
	ProcessingStateMetadataOnly:     true,
	ProcessingStateExcluded:         true,
	ProcessingStateFailed:           true,
}

var validAvailabilityStates = map[string]bool{
	AvailabilityStateDiscovered: true,
	AvailabilityStatePending:    true,
	AvailabilityStateAvailable:  true,
	AvailabilityStateDeleted:    true,
	AvailabilityStateTombstoned: true,
	AvailabilityStateArchived:   true,
	AvailabilityStateSuperseded: true,
	AvailabilityStateFailed:     true,
}

var validRetentionStates = map[string]bool{
	RetentionStateNone:     true,
	RetentionStateRetained: true,
	RetentionStateSnapshot: true,
	RetentionStateExpired:  true,
	RetentionStatePending:  true,
}

var validPhysicalRefKinds = map[string]bool{
	PhysicalRefKindLocalPath:        true,
	PhysicalRefKindObjectBlob:       true,
	PhysicalRefKindBackupArtifact:   true,
	PhysicalRefKindDropzoneFile:     true,
	PhysicalRefKindLaneFile:         true,
	PhysicalRefKindRetentionPayload: true,
	PhysicalRefKindArchiveFile:      true,
	PhysicalRefKindExternalURI:      true,
	PhysicalRefKindCloudObject:      true,
}

var validPhysicalRefStatuses = map[string]bool{
	PhysicalRefStatusAvailable:  true,
	PhysicalRefStatusMissing:    true,
	PhysicalRefStatusVerifying:  true,
	PhysicalRefStatusFailed:     true,
	PhysicalRefStatusSuperseded: true,
}

var validTombstoneKinds = map[string]bool{
	TombstoneKindSourceDeleted:     true,
	TombstoneKindArchivedElsewhere: true,
	TombstoneKindManualDelete:      true,
	TombstoneKindRetentionExpired:  true,
}
