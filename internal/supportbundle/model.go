package supportbundle

import "time"

const (
	SchemaVersion = "loom.support_bundle.v0.9.1"
	ArchiveRoot   = "loom-support"

	RedactionProfileDefault = "default"
)

const (
	SectionStatusIncluded  = "included"
	SectionStatusSkipped   = "skipped"
	SectionStatusFailed    = "failed"
	SectionStatusTruncated = "truncated"
)

const (
	PrivacyMetadata          = "metadata"
	PrivacyDiagnosticSummary = "diagnostic_summary"
	PrivacyPathMetadata      = "path_metadata"
	PrivacyLogExcerpt        = "log_excerpt"
	PrivacyRawOutput         = "raw_output"
)

type Limits struct {
	MaxItems int           `json:"max_items"`
	MaxBytes int64         `json:"max_bytes"`
	Timeout  time.Duration `json:"-"`
	TimeoutS string        `json:"timeout"`
}

type RuntimeInfo struct {
	Environment  string `json:"environment,omitempty"`
	NodeID       string `json:"node_id,omitempty"`
	NodeKind     string `json:"node_kind,omitempty"`
	NodeRole     string `json:"node_role,omitempty"`
	RuntimeClass string `json:"runtime_class,omitempty"`
	Hostname     string `json:"hostname,omitempty"`
	Version      string `json:"version,omitempty"`
	ReleaseID    string `json:"release_id,omitempty"`
}

type PrivacyFlags struct {
	LogsIncluded             bool     `json:"logs_included"`
	LiveProbesAllowed        bool     `json:"live_probes_allowed"`
	AbsolutePathsPreserved   bool     `json:"absolute_paths_preserved"`
	UserFileContentsIncluded bool     `json:"user_file_contents_included"`
	LiveSections             []string `json:"live_sections,omitempty"`
	LiveProbeWarnings        []string `json:"live_probe_warnings,omitempty"`
}

type Manifest struct {
	SchemaVersion    string            `json:"schema_version"`
	BundleID         string            `json:"bundle_id"`
	GeneratedAt      time.Time         `json:"generated_at"`
	Profile          Profile           `json:"profile"`
	RedactionProfile string            `json:"redaction_profile"`
	OutputPath       string            `json:"output_path,omitempty"`
	Runtime          RuntimeInfo       `json:"runtime"`
	Command          []string          `json:"command,omitempty"`
	Limits           Limits            `json:"limits"`
	Privacy          PrivacyFlags      `json:"privacy"`
	Counts           SectionCounts     `json:"counts"`
	Sections         []ManifestSection `json:"sections"`
	Warnings         []string          `json:"warnings,omitempty"`
	Errors           []SectionError    `json:"errors,omitempty"`
}

type ManifestSection struct {
	Key          string `json:"key"`
	Title        string `json:"title,omitempty"`
	Status       string `json:"status"`
	Path         string `json:"path,omitempty"`
	Reason       string `json:"reason,omitempty"`
	Error        string `json:"error,omitempty"`
	PrivacyClass string `json:"privacy_class"`
	Bytes        int64  `json:"bytes,omitempty"`
	Truncated    bool   `json:"truncated,omitempty"`
	Redactions   int    `json:"redactions,omitempty"`
}

type SectionCounts struct {
	Included   int `json:"included"`
	Skipped    int `json:"skipped"`
	Failed     int `json:"failed"`
	Truncated  int `json:"truncated"`
	Warnings   int `json:"warnings"`
	Redactions int `json:"redactions"`
}

type SectionError struct {
	Key     string `json:"key"`
	Title   string `json:"title,omitempty"`
	Message string `json:"message"`
}

type File struct {
	Path         string
	ContentType  string
	PrivacyClass string
	Data         []byte
}

type SectionResult struct {
	Key          string   `json:"key"`
	Title        string   `json:"title,omitempty"`
	Status       string   `json:"status"`
	Path         string   `json:"path,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	Error        string   `json:"error,omitempty"`
	PrivacyClass string   `json:"privacy_class"`
	Bytes        int64    `json:"bytes,omitempty"`
	Truncated    bool     `json:"truncated,omitempty"`
	Redactions   int      `json:"redactions,omitempty"`
	Warnings     []string `json:"warnings,omitempty"`
}

type Result struct {
	BundleID    string          `json:"bundle_id"`
	Status      string          `json:"status"`
	OutputPath  string          `json:"output_path,omitempty"`
	Profile     Profile         `json:"profile"`
	DryRun      bool            `json:"dry_run"`
	Plan        CollectionPlan  `json:"plan"`
	Manifest    Manifest        `json:"manifest"`
	Sections    []SectionResult `json:"sections"`
	Warnings    []string        `json:"warnings,omitempty"`
	Errors      []SectionError  `json:"errors,omitempty"`
	ArchivePath string          `json:"archive_path,omitempty"`
}
