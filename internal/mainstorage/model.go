package mainstorage

import (
	"time"

	"loom.local/loom/internal/filesystemmeta"
)

const (
	StatePendingImport    = "pending_import"
	StateCataloged        = "cataloged"
	StateAccepted         = StateCataloged
	StateCopyingUnstable  = "copying_or_unstable"
	StateHashing          = "hashing"
	StateRetentionCopying = "retention_copying"
	StateExportPending    = "export_pending"
	StateExported         = "exported"
	StateCloudPending     = "cloud_pending"
	StateCloudVerified    = "cloud_verified"
	StateBudgetDeferred   = "budget_deferred"
	StateFailedImport     = "failed_import"
	StateIgnored          = "ignored"
	StateMissingSource    = "missing_source"
	StateMissingDeferred  = "missing_deferred"
	StateTombstoned       = "tombstoned"

	StateDiscovered  = StatePendingImport
	StateStabilizing = StateCopyingUnstable
	StateFailed      = StateFailedImport
	StateSkipped     = StateIgnored
)

type Config struct {
	BackingRoot          string        `json:"backing_root"`
	LegacyRoot           string        `json:"legacy_root,omitempty"`
	RetentionRoot        string        `json:"retention_root,omitempty"`
	NodeKey              string        `json:"node_key,omitempty"`
	StableWindow         time.Duration `json:"stable_window"`
	MaxFilesPerRun       int           `json:"max_files_per_run"`
	MaxBytesHashedPerRun int64         `json:"max_bytes_hashed_per_run,omitempty"`
	MaxRuntime           time.Duration `json:"max_runtime,omitempty"`
	Now                  func() time.Time
}

type Status struct {
	BackingRoot            string       `json:"backing_root"`
	RetentionRoot          string       `json:"retention_root,omitempty"`
	Exists                 bool         `json:"exists"`
	StableWindowSeconds    int64        `json:"stable_window_seconds"`
	MaxFilesPerRun         int          `json:"max_files_per_run"`
	MaxBytesHashedPerRun   int64        `json:"max_bytes_hashed_per_run,omitempty"`
	MaxRuntimeSeconds      int64        `json:"max_runtime_seconds,omitempty"`
	ByteBudgetExhausted    bool         `json:"byte_budget_exhausted,omitempty"`
	RuntimeBudgetExhausted bool         `json:"runtime_budget_exhausted,omitempty"`
	FilesDiscovered        int64        `json:"files_discovered"`
	FilesAccepted          int64        `json:"files_accepted"`
	FilesDelayed           int64        `json:"files_delayed"`
	FilesSkipped           int64        `json:"files_skipped"`
	DirectoriesObserved    int64        `json:"directories_observed,omitempty"`
	ObservationsRecorded   int64        `json:"observations_recorded,omitempty"`
	FilesAlreadyCataloged  int64        `json:"files_already_cataloged"`
	FilesRemaining         int64        `json:"files_remaining"`
	FilesFailed            int64        `json:"files_failed"`
	FilesMissingCataloged  int64        `json:"files_missing_cataloged"`
	FilesTombstoned        int64        `json:"files_tombstoned"`
	BytesHashed            int64        `json:"bytes_hashed"`
	LatestAcceptedAt       *time.Time   `json:"latest_accepted_at,omitempty"`
	Imports                []FileStatus `json:"imports,omitempty"`
	Metrics                Metrics      `json:"metrics,omitempty"`
	GeneratedAt            time.Time    `json:"generated_at"`
}

type Metrics struct {
	DiscoverDurationMS           int64 `json:"discover_duration_ms,omitempty"`
	ActiveCatalogFetchDurationMS int64 `json:"active_catalog_fetch_duration_ms,omitempty"`
	HashDurationMS               int64 `json:"hash_duration_ms,omitempty"`
	HashOperations               int   `json:"hash_operations,omitempty"`
	RetentionCopyDurationMS      int64 `json:"retention_copy_duration_ms,omitempty"`
	RetentionCopyOperations      int   `json:"retention_copy_operations,omitempty"`
	RegisterDurationMS           int64 `json:"register_duration_ms,omitempty"`
	RegisterOperations           int   `json:"register_operations,omitempty"`
	ReconcileDurationMS          int64 `json:"reconcile_duration_ms,omitempty"`
	BudgetStopCount              int   `json:"budget_stop_count,omitempty"`
	TotalDurationMS              int64 `json:"total_duration_ms,omitempty"`
}

type FileStatus struct {
	RelativePath   string                      `json:"relative_path"`
	State          string                      `json:"state"`
	ObjectKind     string                      `json:"object_kind,omitempty"`
	SizeBytes      int64                       `json:"size_bytes,omitempty"`
	ModifiedAt     *time.Time                  `json:"modified_at,omitempty"`
	ChecksumURI    string                      `json:"checksum_uri,omitempty"`
	RetentionPath  string                      `json:"retention_path,omitempty"`
	StorageEntryID string                      `json:"storage_entry_id,omitempty"`
	Error          string                      `json:"error,omitempty"`
	DelayReason    string                      `json:"delay_reason,omitempty"`
	IgnoredReason  string                      `json:"ignored_reason,omitempty"`
	Fidelity       *filesystemmeta.Observation `json:"fidelity,omitempty"`
}

type ImportResult struct {
	Status Status `json:"status"`
}

type ReconcileInput struct {
	DryRun        bool      `json:"dry_run"`
	Yes           bool      `json:"yes,omitempty"`
	CompareLegacy bool      `json:"compare_legacy,omitempty"`
	CreatedBy     string    `json:"created_by,omitempty"`
	Reason        string    `json:"reason,omitempty"`
	Now           time.Time `json:"now,omitempty"`
}

type ReconcileResult struct {
	BackingRoot                string                 `json:"backing_root"`
	LegacyRoot                 string                 `json:"legacy_root,omitempty"`
	DryRun                     bool                   `json:"dry_run"`
	FilesPresent               int64                  `json:"files_present"`
	ActiveCataloged            int64                  `json:"active_cataloged"`
	MissingCataloged           int64                  `json:"missing_cataloged"`
	Tombstoned                 int64                  `json:"tombstoned"`
	Retained                   int64                  `json:"retained"`
	Skipped                    int64                  `json:"skipped"`
	Failed                     int64                  `json:"failed"`
	Items                      []FileStatus           `json:"items,omitempty"`
	Migration                  DocumentsMigrationPlan `json:"migration"`
	CatalogMutationBlocked     bool                   `json:"catalog_mutation_blocked,omitempty"`
	CatalogMutationBlockReason string                 `json:"catalog_mutation_block_reason,omitempty"`
	GeneratedAt                time.Time              `json:"generated_at"`
}

const (
	DocumentsMigrationNotRequested = "not_requested"
	DocumentsMigrationNotNeeded    = "not_needed"
	DocumentsMigrationReady        = "ready"
	DocumentsMigrationBlocked      = "blocked"

	DocumentsMigrationLegacyOnly    = "legacy_only"
	DocumentsMigrationCanonicalOnly = "canonical_only"
	DocumentsMigrationEquivalent    = "equivalent"
	DocumentsMigrationConflict      = "conflict"
)

type DocumentsMigrationPlan struct {
	Status          string                   `json:"status"`
	LegacyRoot      string                   `json:"legacy_root,omitempty"`
	CanonicalRoot   string                   `json:"canonical_root"`
	LegacyExists    bool                     `json:"legacy_exists"`
	CanonicalExists bool                     `json:"canonical_exists"`
	LegacyOnly      int64                    `json:"legacy_only"`
	CanonicalOnly   int64                    `json:"canonical_only"`
	Equivalent      int64                    `json:"equivalent"`
	Conflicts       int64                    `json:"conflicts"`
	Items           []DocumentsMigrationItem `json:"items,omitempty"`
	ItemsTruncated  bool                     `json:"items_truncated,omitempty"`
}

type DocumentsMigrationItem struct {
	RelativePath       string `json:"relative_path"`
	State              string `json:"state"`
	LegacyPath         string `json:"legacy_path,omitempty"`
	CanonicalPath      string `json:"canonical_path,omitempty"`
	LegacyType         string `json:"legacy_type,omitempty"`
	CanonicalType      string `json:"canonical_type,omitempty"`
	LegacySizeBytes    int64  `json:"legacy_size_bytes,omitempty"`
	CanonicalSizeBytes int64  `json:"canonical_size_bytes,omitempty"`
	LegacySHA256       string `json:"legacy_sha256,omitempty"`
	CanonicalSHA256    string `json:"canonical_sha256,omitempty"`
	Reason             string `json:"reason,omitempty"`
}

type RetentionBackfillInput struct {
	DryRun bool      `json:"dry_run"`
	Yes    bool      `json:"yes,omitempty"`
	Limit  int       `json:"limit,omitempty"`
	Now    time.Time `json:"now,omitempty"`
}

type RetentionBackfillResult struct {
	BackingRoot     string                  `json:"backing_root"`
	RetentionRoot   string                  `json:"retention_root"`
	DryRun          bool                    `json:"dry_run"`
	Scanned         int64                   `json:"scanned"`
	AlreadyRetained int64                   `json:"already_retained"`
	Created         int64                   `json:"created"`
	WouldCreate     int64                   `json:"would_create"`
	MissingSource   int64                   `json:"missing_source"`
	Failed          int64                   `json:"failed"`
	Items           []RetentionBackfillItem `json:"items,omitempty"`
	GeneratedAt     time.Time               `json:"generated_at"`
}

type RetentionBackfillItem struct {
	RelativePath   string `json:"relative_path"`
	StorageEntryID string `json:"storage_entry_id,omitempty"`
	State          string `json:"state"`
	SourcePath     string `json:"source_path,omitempty"`
	RetentionPath  string `json:"retention_path,omitempty"`
	Error          string `json:"error,omitempty"`
}
