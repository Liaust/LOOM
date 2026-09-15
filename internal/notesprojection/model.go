package notesprojection

import (
	"context"
	"time"
)

const (
	DefaultDirectoryName = "notes"

	ManifestSchemaVersion = 1
	ManifestRelativePath  = ".loom/manifest.json"
	ReadmeRelativePath    = ".loom/README.txt"

	DefaultRebuildResultLimit = 100
	MaxRebuildResultLimit     = 1000
)

const (
	RootKindBoxNotes        = "box_notes"
	RootKindProjectNotes    = "project_notes"
	RootKindBoxTopics       = "box_topics"
	RootKindBoxLibrary      = "box_library"
	RootKindProjectMaterial = "project_material"
)

const (
	SeverityInfo    = "info"
	SeverityWarning = "warning"
	SeverityError   = "error"
)

const (
	CopyModeFileCopy = "file_copy"
	CopyModeSkipped  = "skipped"
	CopyModeMissing  = "missing_source"
)

const (
	ProjectionStatusPlanned      = "planned"
	ProjectionStatusMaterialized = "materialized"
	ProjectionStatusMissing      = "missing_source"
	ProjectionStatusSkipped      = "skipped"
)

type SourceListInput struct {
	Limit int `json:"limit,omitempty"`
}

type SourceProvider interface {
	ListProjectionSources(ctx context.Context, input SourceListInput) ([]SourceObject, error)
}

type SourceObject struct {
	SourceCategory     string     `json:"source_category,omitempty"`
	SourcePosture      string     `json:"source_posture,omitempty"`
	Declaration        string     `json:"declaration,omitempty"`
	TopicKey           string     `json:"topic_key,omitempty"`
	CollectionKey      string     `json:"collection_key,omitempty"`
	RootRelativePath   string     `json:"root_relative_path,omitempty"`
	KnowledgeObjectID  string     `json:"knowledge_object_id"`
	NotesSourceRootID  string     `json:"notes_source_root_id"`
	RootKind           string     `json:"root_kind"`
	SourceNodeKey      string     `json:"source_node_key,omitempty"`
	ProjectID          *string    `json:"project_id,omitempty"`
	ProjectSlug        string     `json:"project_slug,omitempty"`
	StorageEntryID     *string    `json:"storage_entry_id,omitempty"`
	SourcePath         string     `json:"source_path,omitempty"`
	SourceRefKind      string     `json:"source_ref_kind,omitempty"`
	SourceRefURI       string     `json:"source_ref_uri,omitempty"`
	SourceRefMember    string     `json:"source_ref_member,omitempty"`
	RelativePath       string     `json:"relative_path"`
	Title              string     `json:"title,omitempty"`
	FileClass          string     `json:"file_class,omitempty"`
	MimeType           string     `json:"mime_type,omitempty"`
	SizeBytes          *int64     `json:"size_bytes,omitempty"`
	SourceHash         string     `json:"source_hash,omitempty"`
	SourceRevision     string     `json:"source_revision,omitempty"`
	LastSeenAt         time.Time  `json:"last_seen_at,omitempty"`
	LastMaterializedAt *time.Time `json:"last_materialized_at,omitempty"`
}

type RebuildInput struct {
	ProjectionRoot string `json:"projection_root,omitempty"`
	DryRun         bool   `json:"dry_run,omitempty"`
	IncludeAll     bool   `json:"include_all,omitempty"`
	MaxResults     int    `json:"max_results,omitempty"`
	MaxObjects     int    `json:"max_objects,omitempty"`
}

type RebuildResult struct {
	ProjectionRoot           string    `json:"projection_root"`
	DryRun                   bool      `json:"dry_run"`
	Manifest                 Manifest  `json:"manifest"`
	Status                   Status    `json:"status"`
	Changes                  []Change  `json:"changes,omitempty"`
	Findings                 []Finding `json:"findings,omitempty"`
	ChangesTruncated         bool      `json:"changes_truncated,omitempty"`
	FindingsTruncated        bool      `json:"findings_truncated,omitempty"`
	ManifestEntriesTruncated bool      `json:"manifest_entries_truncated,omitempty"`
	GeneratedAt              time.Time `json:"generated_at"`
}

type Status struct {
	ProjectionRoot     string     `json:"projection_root"`
	Exists             bool       `json:"exists"`
	ManifestPath       string     `json:"manifest_path"`
	LastRebuildAt      *time.Time `json:"last_rebuild_at,omitempty"`
	Counts             Counts     `json:"counts"`
	ReadOnly           bool       `json:"read_only"`
	RawWritesSupported bool       `json:"raw_writes_supported"`
	Findings           []Finding  `json:"findings,omitempty"`
	GeneratedAt        time.Time  `json:"generated_at"`
}

type Manifest struct {
	SchemaVersion  int               `json:"schema_version"`
	ProjectionRoot string            `json:"projection_root"`
	GeneratedAt    time.Time         `json:"generated_at"`
	ReadOnly       bool              `json:"read_only"`
	Counts         Counts            `json:"counts"`
	Entries        []ProjectionEntry `json:"entries"`
	Findings       []Finding         `json:"findings,omitempty"`
}

type Counts struct {
	Entries      int `json:"entries"`
	Materialized int `json:"materialized"`
	Missing      int `json:"missing"`
	Skipped      int `json:"skipped"`
}

type ProjectionEntry struct {
	SourceCategory    string     `json:"source_category,omitempty"`
	SourcePosture     string     `json:"source_posture,omitempty"`
	Declaration       string     `json:"declaration,omitempty"`
	TopicKey          string     `json:"topic_key,omitempty"`
	CollectionKey     string     `json:"collection_key,omitempty"`
	ProjectedPath     string     `json:"projected_path"`
	FilesystemPath    string     `json:"filesystem_path"`
	SourcePath        string     `json:"source_path,omitempty"`
	CopyMode          string     `json:"copy_mode"`
	Status            string     `json:"status"`
	NotesSourceRootID string     `json:"notes_source_root_id"`
	RootKind          string     `json:"root_kind"`
	SourceNodeKey     string     `json:"source_node_key,omitempty"`
	ProjectID         *string    `json:"project_id,omitempty"`
	ProjectSlug       string     `json:"project_slug,omitempty"`
	RelativePath      string     `json:"relative_path"`
	KnowledgeObjectID string     `json:"knowledge_object_id"`
	StorageEntryID    *string    `json:"storage_entry_id,omitempty"`
	Title             string     `json:"title,omitempty"`
	FileClass         string     `json:"file_class,omitempty"`
	MimeType          string     `json:"mime_type,omitempty"`
	SizeBytes         *int64     `json:"size_bytes,omitempty"`
	SourceHash        string     `json:"source_hash,omitempty"`
	SourceRevision    string     `json:"source_revision,omitempty"`
	SourceRefKind     string     `json:"source_ref_kind,omitempty"`
	SourceRefURI      string     `json:"source_ref_uri,omitempty"`
	SourceRefMember   string     `json:"source_ref_member,omitempty"`
	LastSeenAt        time.Time  `json:"last_seen_at,omitempty"`
	MaterializedAt    *time.Time `json:"materialized_at,omitempty"`
}

type Change struct {
	Action  string `json:"action"`
	Path    string `json:"path"`
	Source  string `json:"source,omitempty"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type Finding struct {
	Severity string `json:"severity"`
	Kind     string `json:"kind"`
	Path     string `json:"path,omitempty"`
	Summary  string `json:"summary"`
}
