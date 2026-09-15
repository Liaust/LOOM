package storagearchive

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

const (
	ManifestSchemaVersion        = "storage.archive_manifest.v0.7"
	CatalogRecoverySchemaVersion = "storage.archive_catalog_recovery.v0.7"
)

type ManifestDocument struct {
	SchemaVersion        string         `json:"schema_version"`
	CatalogRecovery      string         `json:"catalog_recovery_schema,omitempty"`
	ArchiveManifestID    string         `json:"archive_manifest_id"`
	ArchiveKey           string         `json:"archive_key"`
	ArchiveKind          string         `json:"archive_kind"`
	SourceRef            string         `json:"source_ref"`
	TargetPath           string         `json:"target_path"`
	OwnerNodeKey         string         `json:"owner_node_key"`
	MarkSourceArchived   bool           `json:"mark_source_archived,omitempty"`
	MarkSourceSuperseded bool           `json:"mark_source_superseded,omitempty"`
	ExpectedEntryCount   int            `json:"expected_entry_count"`
	ArchivedEntryCount   int            `json:"archived_entry_count"`
	Complete             bool           `json:"complete"`
	Entries              []ManifestItem `json:"entries"`
	CreatedAt            time.Time      `json:"created_at"`
}

type ManifestItem struct {
	SourceStorageEntryID    string `json:"source_storage_entry_id"`
	ArchiveStorageEntryID   string `json:"archive_storage_entry_id,omitempty"`
	ArchivePhysicalRefID    string `json:"archive_physical_ref_id,omitempty"`
	SourceViewPath          string `json:"source_view_path"`
	SourceLogicalPath       string `json:"source_logical_path"`
	SourceOriginalPath      string `json:"source_original_path"`
	SourceMimeType          string `json:"source_mime_type,omitempty"`
	SourceRefKind           string `json:"source_ref_kind"`
	SourceRefURI            string `json:"source_ref_uri"`
	SourceAvailabilityState string `json:"source_availability_state"`
	ArchiveViewPath         string `json:"archive_view_path"`
	ArchiveLogicalPath      string `json:"archive_logical_path"`
	ChecksumAlgorithm       string `json:"checksum_algorithm,omitempty"`
	ChecksumHex             string `json:"checksum_hex,omitempty"`
	SizeBytes               *int64 `json:"size_bytes,omitempty"`
	FileClass               string `json:"file_class,omitempty"`
	ContentKey              string `json:"content_key"`
	// ArchiveObjectPath is relative to the archive-key directory so custody
	// evidence remains portable when a backup is restored under another root.
	ArchiveObjectPath string `json:"archive_object_path"`
	Deduped           bool   `json:"deduped"`
}

type ManifestEvidenceClass string

const (
	ManifestEvidencePhysicalMove   ManifestEvidenceClass = "physical_move"
	ManifestEvidenceHistoricalCopy ManifestEvidenceClass = "historical_copy"
)

type HistoricalCopyManifest struct {
	SchemaVersion     string          `json:"schema_version"`
	EvidenceKind      string          `json:"evidence_kind,omitempty"`
	ArchiveManifestID string          `json:"archive_manifest_id,omitempty"`
	ArchiveKey        string          `json:"archive_key,omitempty"`
	ArchiveKind       string          `json:"archive_kind,omitempty"`
	Complete          bool            `json:"complete,omitempty"`
	Raw               json.RawMessage `json:"-"`
}

type ManifestCompatibility struct {
	Class          ManifestEvidenceClass
	Physical       *WorkspaceArchiveManifest
	HistoricalCopy *HistoricalCopyManifest
}

func ClassifyManifestEvidence(payload []byte) (ManifestCompatibility, error) {
	var envelope struct {
		SchemaVersion string `json:"schema_version"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return ManifestCompatibility{}, fmt.Errorf("decode archive manifest envelope: %w", err)
	}
	switch envelope.SchemaVersion {
	case WorkspaceArchiveManifestSchemaVersion:
		manifest, err := DecodePhysicalWorkspaceArchiveManifest(payload)
		if err != nil {
			return ManifestCompatibility{}, err
		}
		return ManifestCompatibility{Class: ManifestEvidencePhysicalMove, Physical: &manifest}, nil
	case "storage.archive_manifest.v0.6", ManifestSchemaVersion, ProjectRuntimeArchiveManifestSchemaVersion:
		var historical HistoricalCopyManifest
		if err := json.Unmarshal(payload, &historical); err != nil {
			return ManifestCompatibility{}, fmt.Errorf("decode historical copy manifest: %w", err)
		}
		if historical.EvidenceKind == PhysicalWorkspaceMoveEvidence {
			return ManifestCompatibility{}, fmt.Errorf("historical copy schema cannot claim physical-move evidence")
		}
		historical.Raw = append(json.RawMessage(nil), payload...)
		return ManifestCompatibility{Class: ManifestEvidenceHistoricalCopy, HistoricalCopy: &historical}, nil
	default:
		return ManifestCompatibility{}, fmt.Errorf("unsupported archive manifest schema %q", envelope.SchemaVersion)
	}
}

func DecodePhysicalWorkspaceArchiveManifest(payload []byte) (WorkspaceArchiveManifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var manifest WorkspaceArchiveManifest
	if err := decoder.Decode(&manifest); err != nil {
		return WorkspaceArchiveManifest{}, fmt.Errorf("decode physical workspace archive manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return WorkspaceArchiveManifest{}, fmt.Errorf("physical workspace archive manifest contains multiple JSON values")
		}
		return WorkspaceArchiveManifest{}, fmt.Errorf("decode physical workspace archive manifest trailer: %w", err)
	}
	if err := ValidateWorkspaceArchiveManifest(manifest); err != nil {
		return WorkspaceArchiveManifest{}, err
	}
	return manifest, nil
}
