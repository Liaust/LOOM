package projectexport

import (
	"fmt"
	"strings"

	"loom.local/loom/internal/filepolicy"
)

const (
	SchemaVersion       = "loom.project_export.v1"
	ManagedAgentsMarker = "<!-- loom-managed: project-agent-entry/v1 -->"
	SummaryHeader       = "X-Loom-Project-Export-Summary"
	DefaultMaxEntries   = 200000
	DefaultMaxBytes     = int64(512 << 20)
)

type Mode string

const (
	ModeHuman    Mode = "human"
	ModePortable Mode = "portable"
	ModeArchival Mode = "archival"
)

type Request struct {
	ProjectRef  string `json:"project_ref,omitempty"`
	ProjectRoot string `json:"project_root,omitempty"`
	Mode        Mode   `json:"mode"`
	MaxBytes    int64  `json:"max_bytes,omitempty"`
}

type PlanOptions struct {
	Mode                   Mode
	MaxEntries             int
	MaxBytes               int64
	RegistrationReferences RegistrationReferences
}

type Entry struct {
	RelativePath string              `json:"relative_path"`
	Kind         string              `json:"kind"`
	Mode         int64               `json:"mode"`
	Size         int64               `json:"size"`
	LinkTarget   string              `json:"link_target,omitempty"`
	ContentHash  string              `json:"content_hash,omitempty"`
	Decision     filepolicy.Decision `json:"decision"`
	Generated    []byte              `json:"-"`
}

type Summary struct {
	SchemaVersion     string                  `json:"schema_version"`
	ProjectSlug       string                  `json:"project_slug"`
	Mode              Mode                    `json:"mode"`
	PolicyVersion     string                  `json:"policy_version"`
	PolicyFingerprint string                  `json:"policy_fingerprint"`
	PolicyHashes      map[string]string       `json:"policy_hashes,omitempty"`
	Included          filepolicy.CountSummary `json:"included"`
	Ignored           filepolicy.CountSummary `json:"ignored"`
	ArchiveBytes      int64                   `json:"archive_bytes,omitempty"`
	ArchiveChecksum   string                  `json:"archive_checksum,omitempty"`
	OutputPath        string                  `json:"output_path,omitempty"`
}

type Plan struct {
	ProjectRoot     string  `json:"project_root"`
	ContractPath    string  `json:"contract_path"`
	Entries         []Entry `json:"entries"`
	Ignored         []Entry `json:"ignored,omitempty"`
	Summary         Summary `json:"summary"`
	MaxArchiveBytes int64   `json:"-"`
}

func ParseMode(value string) (Mode, error) {
	mode := Mode(strings.ToLower(strings.TrimSpace(value)))
	switch mode {
	case ModeHuman, ModePortable, ModeArchival:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported project export mode %q", value)
	}
}
