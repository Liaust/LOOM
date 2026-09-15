package storageexport

import (
	"encoding/json"
	"time"

	"loom.local/loom/internal/storageview"
)

const (
	DefaultViewKey = storageview.DefaultViewKey

	ManifestSchemaVersion = 1

	DefaultRebuildResultLimit = 100
	MaxRebuildResultLimit     = 1000

	ManifestRelativePath = ".loom/storage-export/manifest.json"
)

const (
	SeverityInfo    = "info"
	SeverityWarning = "warning"
	SeverityError   = "error"
)

const (
	RebuildModeFullRepair         = "full_repair"
	RebuildModeIncrementalRefresh = "incremental_refresh"
)

const (
	MaterializationDirectory  = "directory"
	MaterializationSymlink    = "symlink"
	MaterializationLinkedFile = "linked_file"
	MaterializationExtracted  = "extracted_file"
	MaterializationStatus     = "status_file"
	MaterializationSidecar    = "metadata_sidecar"
	MaterializationSkipped    = "skipped"
	MaterializationMissing    = "missing_source"
)

const (
	ExportPolicySafeView        = "safe_view"
	ExportPolicyFaithfulRestore = "faithful_restore"
	ExportPolicyRawArchive      = "raw_archive"
)

type RebuildInput struct {
	DryRun     bool `json:"dry_run,omitempty"`
	IncludeAll bool `json:"include_all,omitempty"`
	MaxResults int  `json:"max_results,omitempty"`
}

type RefreshInput struct {
	DryRun                 bool     `json:"dry_run,omitempty"`
	ChangedStorageEntryIDs []string `json:"changed_storage_entry_ids,omitempty"`
	ChangedViewPrefixes    []string `json:"changed_view_prefixes,omitempty"`
	ChangedSourceArea      string   `json:"changed_source_area,omitempty"`
	ChangedOriginNodeKey   string   `json:"changed_origin_node_key,omitempty"`
}

type RebuildResult struct {
	ExportRoot               string    `json:"export_root"`
	ViewKey                  string    `json:"view_key"`
	DryRun                   bool      `json:"dry_run"`
	Mode                     string    `json:"mode,omitempty"`
	Manifest                 Manifest  `json:"manifest"`
	Status                   Status    `json:"status"`
	Changes                  []Change  `json:"changes,omitempty"`
	Findings                 []Finding `json:"findings,omitempty"`
	ChangesTruncated         bool      `json:"changes_truncated,omitempty"`
	FindingsTruncated        bool      `json:"findings_truncated,omitempty"`
	ManifestEntriesTruncated bool      `json:"manifest_entries_truncated,omitempty"`
	Metrics                  Metrics   `json:"metrics,omitempty"`
	GeneratedAt              time.Time `json:"generated_at"`
}

type Status struct {
	ExportRoot       string             `json:"export_root"`
	ViewKey          string             `json:"view_key"`
	Exists           bool               `json:"exists"`
	Mode             string             `json:"mode,omitempty"`
	ManifestPath     string             `json:"manifest_path"`
	SystemStatusPath string             `json:"system_status_path"`
	LastRebuildAt    *time.Time         `json:"last_rebuild_at,omitempty"`
	Counts           storageview.Counts `json:"counts"`
	Findings         []Finding          `json:"findings,omitempty"`
	Metrics          Metrics            `json:"metrics,omitempty"`
	Refresh          *RefreshStatus     `json:"refresh,omitempty"`
	GeneratedAt      time.Time          `json:"generated_at"`
}

// RefreshRequest and related records are decode-only compatibility shapes for
// one-release clients and saved Portal snapshots. No runtime controller or
// filesystem mutation implementation remains in this package.
type RefreshRequest struct {
	Reason                 string   `json:"reason,omitempty"`
	ChangedHints           []string `json:"changed_hints,omitempty"`
	ChangedStorageEntryIDs []string `json:"changed_storage_entry_ids,omitempty"`
	ChangedViewPrefixes    []string `json:"changed_view_prefixes,omitempty"`
	ChangedSourceArea      string   `json:"changed_source_area,omitempty"`
	ChangedOriginNodeKey   string   `json:"changed_origin_node_key,omitempty"`
	DebounceMillis         int64    `json:"debounce_millis,omitempty"`
}

type RefreshRequestResult struct {
	Accepted bool          `json:"accepted"`
	Reason   string        `json:"reason,omitempty"`
	Status   RefreshStatus `json:"status"`
}

type RefreshStatus struct {
	State             string                `json:"state"`
	Running           bool                  `json:"running"`
	Pending           bool                  `json:"pending"`
	Scheduled         bool                  `json:"scheduled"`
	RequestedAt       *time.Time            `json:"requested_at,omitempty"`
	LastStartedAt     *time.Time            `json:"last_started_at,omitempty"`
	LastFinishedAt    *time.Time            `json:"last_finished_at,omitempty"`
	LastError         string                `json:"last_error,omitempty"`
	LastResultSummary *RefreshResultSummary `json:"last_result_summary,omitempty"`
	ChangedHints      []string              `json:"changed_hints,omitempty"`
	ChangedEntryIDs   []string              `json:"changed_storage_entry_ids,omitempty"`
	ChangedPrefixes   []string              `json:"changed_view_prefixes,omitempty"`
	ChangedSourceArea string                `json:"changed_source_area,omitempty"`
	ChangedOriginNode string                `json:"changed_origin_node_key,omitempty"`
	DebounceMillis    int64                 `json:"debounce_millis"`
	RebuildTimeoutMS  int64                 `json:"rebuild_timeout_ms"`
	RunCount          int64                 `json:"run_count"`
}

type RefreshResultSummary struct {
	ExportRoot    string    `json:"export_root"`
	ViewKey       string    `json:"view_key"`
	Mode          string    `json:"mode,omitempty"`
	Entries       int       `json:"entries"`
	Changes       int       `json:"changes"`
	Findings      int       `json:"findings"`
	TotalDuration int64     `json:"total_duration_ms,omitempty"`
	GeneratedAt   time.Time `json:"generated_at"`
}

type Manifest struct {
	SchemaVersion int                `json:"schema_version"`
	ViewKey       string             `json:"view_key"`
	ExportRoot    string             `json:"export_root"`
	Mode          string             `json:"mode,omitempty"`
	GeneratedAt   time.Time          `json:"generated_at"`
	Counts        storageview.Counts `json:"counts"`
	Metrics       Metrics            `json:"metrics,omitempty"`
	Entries       []ManifestEntry    `json:"entries"`
	Findings      []Finding          `json:"findings,omitempty"`
}

type Metrics struct {
	ListEntriesDurationMS        int64 `json:"list_entries_duration_ms,omitempty"`
	ListEntriesCount             int   `json:"list_entries_count,omitempty"`
	Incremental                  bool  `json:"incremental,omitempty"`
	ChangedEntriesCount          int   `json:"changed_entries_count,omitempty"`
	ChangedViewPrefixesCount     int   `json:"changed_view_prefixes_count,omitempty"`
	BuildTreeDurationMS          int64 `json:"build_tree_duration_ms,omitempty"`
	FileEntryCount               int   `json:"file_entry_count,omitempty"`
	InspectDetailsDurationMS     int64 `json:"inspect_details_duration_ms,omitempty"`
	InspectDetailsQueryCount     int   `json:"inspect_details_query_count,omitempty"`
	ManifestBuildDurationMS      int64 `json:"manifest_build_duration_ms,omitempty"`
	PruneDurationMS              int64 `json:"prune_duration_ms,omitempty"`
	MaterializeDurationMS        int64 `json:"materialize_duration_ms,omitempty"`
	StatusWriteDurationMS        int64 `json:"status_write_duration_ms,omitempty"`
	PermissionFinalizeDurationMS int64 `json:"permission_finalize_duration_ms,omitempty"`
	TotalDurationMS              int64 `json:"total_duration_ms,omitempty"`
	ChangesCount                 int   `json:"changes_count,omitempty"`
	FindingsCount                int   `json:"findings_count,omitempty"`
}

type ManifestEntry struct {
	ViewPath          string          `json:"view_path"`
	FilesystemPath    string          `json:"filesystem_path"`
	EntryKind         string          `json:"entry_kind"`
	Permissions       string          `json:"permissions"`
	Writable          bool            `json:"writable"`
	Generated         bool            `json:"generated"`
	StorageEntryID    string          `json:"storage_entry_id,omitempty"`
	StorageClass      string          `json:"storage_class,omitempty"`
	SourceArea        string          `json:"source_area,omitempty"`
	OriginNodeKey     string          `json:"origin_node_key,omitempty"`
	LogicalPath       string          `json:"logical_path,omitempty"`
	SizeBytes         *int64          `json:"size_bytes,omitempty"`
	SourceURI         string          `json:"source_uri,omitempty"`
	SourceRefKind     string          `json:"source_ref_kind,omitempty"`
	ExportPolicy      string          `json:"export_policy,omitempty"`
	Materialization   string          `json:"materialization"`
	Status            string          `json:"status"`
	ChecksumAlgorithm string          `json:"checksum_algorithm,omitempty"`
	ChecksumHex       string          `json:"checksum_hex,omitempty"`
	FilesystemKind    string          `json:"filesystem_kind,omitempty"`
	SourceMode        *int            `json:"source_mode,omitempty"`
	SymlinkTarget     string          `json:"symlink_target,omitempty"`
	FidelityRisks     []string        `json:"fidelity_risks,omitempty"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
}

type Finding struct {
	Severity string `json:"severity"`
	Kind     string `json:"kind"`
	Path     string `json:"path,omitempty"`
	Summary  string `json:"summary"`
}

type Change struct {
	Action  string `json:"action"`
	Path    string `json:"path"`
	Source  string `json:"source,omitempty"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}
