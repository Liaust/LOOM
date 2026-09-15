package dropzone

import (
	"encoding/json"
	"time"

	"loom.local/loom/internal/filesystemmeta"
)

// Dropzone is retired as an active runtime. The types in this package are
// retained for one compatibility release so historical evidence can still be
// decoded and inspected. They must not be used to admit or mutate transfers.
const (
	TransferSchemaVersion      = "loom.dropzone.transfer.v0.4.2"
	PolicySchemaVersion        = "loom.box.transfer_policy.v0.4.2"
	LegacyPolicySchemaV041     = "loom.box.transfer_policy.v0.4.1"
	UploadSessionSchemaVersion = "loom.dropzone.upload_session.v0.4.2"
	CustodySchemaVersion       = "loom.dropzone.custody.v0.4.2"

	StatusDiscovered = "discovered"
	StatusSettling   = "settling"
	StatusReady      = "ready"
	StatusHashing    = "hashing"
	StatusUploading  = "uploading"
	StatusVerifying  = "verifying"
	StatusAccepted   = "accepted"
	StatusFailed     = "failed"
	StatusPaused     = "paused"
	StatusAbandoned  = "abandoned"

	RuntimeRetired        = "retired"
	RuntimeInactive       = "inactive"
	RuntimeDisabled       = "disabled"
	RuntimeNotInitialized = "not_initialized"

	ChecksumSHA256 = "sha256"
	FileKindFile   = "file"

	UploadSessionStatusReceiving = "receiving"
	UploadSessionStatusVerifying = "verifying"
	UploadSessionStatusAccepted  = "accepted"
	UploadSessionStatusFailed    = "failed"
	UploadSessionStatusAborted   = "aborted"

	// Control action names remain decodable for transitional Portal records,
	// but every executor rejects them.
	ControlActionRetry  = "retry"
	ControlActionPause  = "pause"
	ControlActionResume = "resume"
)

// ActiveStatuses classifies statuses found in historical records. It does not
// indicate that an active Dropzone runtime remains available.
var ActiveStatuses = map[string]bool{
	StatusDiscovered: true,
	StatusSettling:   true,
	StatusReady:      true,
	StatusHashing:    true,
	StatusUploading:  true,
	StatusVerifying:  true,
}

// Policy is deprecated and decode-only.
type Policy struct {
	SchemaVersion                string   `json:"schema_version" yaml:"schema_version"`
	SourceSchemaVersion          string   `json:"source_schema_version,omitempty" yaml:"-"`
	Area                         string   `json:"area" yaml:"area"`
	Path                         string   `json:"path" yaml:"path"`
	Enabled                      bool     `json:"enabled" yaml:"enabled"`
	Mode                         string   `json:"mode" yaml:"mode"`
	RuntimeStatus                string   `json:"runtime_status" yaml:"runtime_status"`
	Target                       string   `json:"target" yaml:"target"`
	TargetMainNode               string   `json:"target_main_node,omitempty" yaml:"target_main_node,omitempty"`
	StatusDir                    string   `json:"status_dir" yaml:"status_dir"`
	TransfersDir                 string   `json:"transfers_dir" yaml:"transfers_dir"`
	ScanInterval                 string   `json:"scan_interval" yaml:"scan_interval"`
	SettleDuration               string   `json:"settle_duration" yaml:"settle_duration"`
	IgnorePatterns               []string `json:"ignore_patterns" yaml:"ignore_patterns"`
	MaxParallelTransfers         int      `json:"max_parallel_transfers" yaml:"max_parallel_transfers"`
	ChunkSizeBytes               int64    `json:"chunk_size_bytes" yaml:"chunk_size_bytes"`
	ChecksumAlgorithm            string   `json:"checksum_algorithm" yaml:"checksum_algorithm"`
	RetryCount                   int      `json:"retry_count" yaml:"retry_count"`
	RetryBackoff                 string   `json:"retry_backoff" yaml:"retry_backoff"`
	MaxFileSizeBytes             int64    `json:"max_file_size_bytes,omitempty" yaml:"max_file_size_bytes,omitempty"`
	AcceptedRetention            string   `json:"accepted_retention" yaml:"accepted_retention"`
	AutomaticDeleteAfterSafe     bool     `json:"automatic_delete_after_safe" yaml:"automatic_delete_after_safe"`
	DeleteSemantics              string   `json:"delete_semantics" yaml:"delete_semantics"`
	FailurePolicy                string   `json:"failure_policy" yaml:"failure_policy"`
	BandwidthLimitBytesPerSecond int64    `json:"bandwidth_limit_bytes_per_second,omitempty" yaml:"bandwidth_limit_bytes_per_second,omitempty"`
	AllowFolders                 bool     `json:"allow_folders" yaml:"allow_folders"`
	Notes                        string   `json:"notes,omitempty" yaml:"notes,omitempty"`
}

// TransferRecord is deprecated and decode-only.
type TransferRecord struct {
	SchemaVersion         string                      `json:"schema_version"`
	TransferID            string                      `json:"transfer_id"`
	SourceNodeKey         string                      `json:"source_node_key"`
	SourceBoxID           string                      `json:"source_box_id"`
	SourceBoxRootPath     string                      `json:"source_box_root_path"`
	LocalAbsolutePath     string                      `json:"local_absolute_path"`
	RelativeDropzonePath  string                      `json:"relative_dropzone_path"`
	FileName              string                      `json:"file_name"`
	FileKind              string                      `json:"file_kind"`
	FileSizeBytes         int64                       `json:"file_size_bytes"`
	ModTime               time.Time                   `json:"mod_time"`
	ChecksumAlgorithm     string                      `json:"checksum_algorithm"`
	ChecksumValue         string                      `json:"checksum_value,omitempty"`
	UploadSessionID       string                      `json:"upload_session_id,omitempty"`
	ChunkSizeBytes        int64                       `json:"chunk_size_bytes"`
	TotalChunks           int64                       `json:"total_chunks"`
	UploadedBytes         int64                       `json:"uploaded_bytes"`
	UploadedChunks        []int64                     `json:"uploaded_chunks,omitempty"`
	Status                string                      `json:"status"`
	FailureCode           string                      `json:"failure_code,omitempty"`
	FailureMessage        string                      `json:"failure_message,omitempty"`
	RetryCount            int                         `json:"retry_count"`
	DiscoveredAt          time.Time                   `json:"discovered_at"`
	FirstSeenAt           time.Time                   `json:"first_seen_at"`
	LastObservedAt        time.Time                   `json:"last_observed_at"`
	UpdatedAt             time.Time                   `json:"updated_at"`
	AcceptedAt            *time.Time                  `json:"accepted_at,omitempty"`
	RemoteStoragePath     string                      `json:"remote_storage_path,omitempty"`
	MainCustodyID         string                      `json:"main_custody_id,omitempty"`
	StorageEntryID        string                      `json:"storage_entry_id,omitempty"`
	StoragePhysicalRefID  string                      `json:"storage_physical_ref_id,omitempty"`
	SafeToDelete          bool                        `json:"safe_to_delete"`
	FilesystemObservation *filesystemmeta.Observation `json:"filesystem_observation,omitempty"`
	RawMainResponse       json.RawMessage             `json:"raw_main_response,omitempty"`
}

// Status is deprecated and decode/inspection-only.
type Status struct {
	RuntimeState       string            `json:"runtime_state"`
	Profile            string            `json:"profile"`
	RootPath           string            `json:"root_path"`
	DropzonePath       string            `json:"dropzone_path"`
	StatusDir          string            `json:"status_dir"`
	TransfersDir       string            `json:"transfers_dir"`
	PolicyEnabled      bool              `json:"policy_enabled"`
	PolicySchema       string            `json:"policy_schema"`
	SourcePolicySchema string            `json:"source_policy_schema,omitempty"`
	Target             string            `json:"target"`
	Counts             map[string]int    `json:"counts"`
	Active             []TransferSummary `json:"active,omitempty"`
	Accepted           []TransferSummary `json:"accepted,omitempty"`
	Failed             []TransferSummary `json:"failed,omitempty"`
	Diagnostics        []Diagnostic      `json:"diagnostics,omitempty"`
	InspectedAt        time.Time         `json:"inspected_at"`
}

type TransferSummary struct {
	TransferID           string     `json:"transfer_id"`
	RelativeDropzonePath string     `json:"relative_dropzone_path"`
	FileSizeBytes        int64      `json:"file_size_bytes"`
	Status               string     `json:"status"`
	UploadedBytes        int64      `json:"uploaded_bytes"`
	SafeToDelete         bool       `json:"safe_to_delete"`
	RemoteStoragePath    string     `json:"remote_storage_path,omitempty"`
	StorageEntryID       string     `json:"storage_entry_id,omitempty"`
	FailureMessage       string     `json:"failure_message,omitempty"`
	FidelityWarnings     []string   `json:"fidelity_warnings,omitempty"`
	UpdatedAt            time.Time  `json:"updated_at"`
	AcceptedAt           *time.Time `json:"accepted_at,omitempty"`
}

type Diagnostic struct {
	Severity   string `json:"severity"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Path       string `json:"path,omitempty"`
	Suggestion string `json:"suggestion,omitempty"`
}

type DiskCheck struct {
	Path           string `json:"path"`
	RequiredBytes  int64  `json:"required_bytes"`
	AvailableBytes int64  `json:"available_bytes"`
}

// UploadSession is deprecated and decode-only.
type UploadSession struct {
	SchemaVersion             string                      `json:"schema_version"`
	SessionID                 string                      `json:"session_id"`
	TransferID                string                      `json:"transfer_id"`
	SourceNodeKey             string                      `json:"source_node_key"`
	SourceBoxID               string                      `json:"source_box_id"`
	RelativeDropzonePath      string                      `json:"relative_dropzone_path"`
	FileName                  string                      `json:"file_name"`
	ExpectedFileSizeBytes     int64                       `json:"expected_file_size_bytes"`
	ExpectedChecksumAlgorithm string                      `json:"expected_checksum_algorithm"`
	ExpectedChecksumValue     string                      `json:"expected_checksum_value"`
	ChunkSizeBytes            int64                       `json:"chunk_size_bytes"`
	TotalChunks               int64                       `json:"total_chunks"`
	AcceptedChunks            []int64                     `json:"accepted_chunks,omitempty"`
	AcceptedBytes             int64                       `json:"accepted_bytes"`
	Status                    string                      `json:"status"`
	MainBoxRootPath           string                      `json:"main_box_root_path"`
	IncomingDir               string                      `json:"incoming_dir"`
	ChunksDir                 string                      `json:"chunks_dir"`
	PartialPath               string                      `json:"partial_path"`
	FinalCustodyPath          string                      `json:"final_custody_path"`
	CustodyMetadataPath       string                      `json:"custody_metadata_path,omitempty"`
	FinalChecksumValue        string                      `json:"final_checksum_value,omitempty"`
	CustodyID                 string                      `json:"custody_id,omitempty"`
	StorageEntryID            string                      `json:"storage_entry_id,omitempty"`
	StoragePhysicalRefID      string                      `json:"storage_physical_ref_id,omitempty"`
	FailureCode               string                      `json:"failure_code,omitempty"`
	FailureMessage            string                      `json:"failure_message,omitempty"`
	CreatedAt                 time.Time                   `json:"created_at"`
	UpdatedAt                 time.Time                   `json:"updated_at"`
	CompletedAt               *time.Time                  `json:"completed_at,omitempty"`
	AbortedAt                 *time.Time                  `json:"aborted_at,omitempty"`
	DiskCheck                 *DiskCheck                  `json:"disk_check,omitempty"`
	FilesystemObservation     *filesystemmeta.Observation `json:"filesystem_observation,omitempty"`
}

// CustodyRecord is deprecated and decode-only.
type CustodyRecord struct {
	SchemaVersion             string    `json:"schema_version"`
	CustodyID                 string    `json:"custody_id"`
	SessionID                 string    `json:"session_id"`
	TransferID                string    `json:"transfer_id"`
	SourceNodeKey             string    `json:"source_node_key"`
	SourceBoxID               string    `json:"source_box_id"`
	RelativeDropzonePath      string    `json:"relative_dropzone_path"`
	FileName                  string    `json:"file_name"`
	FileSizeBytes             int64     `json:"file_size_bytes"`
	ChecksumAlgorithm         string    `json:"checksum_algorithm"`
	ChecksumValue             string    `json:"checksum_value"`
	FinalCustodyPath          string    `json:"final_custody_path"`
	CustodyMetadataPath       string    `json:"custody_metadata_path"`
	StorageEntryID            string    `json:"storage_entry_id,omitempty"`
	ReceivedUploadSessionPath string    `json:"received_upload_session_path"`
	AcceptedAt                time.Time `json:"accepted_at"`
}
