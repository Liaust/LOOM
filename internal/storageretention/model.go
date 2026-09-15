package storageretention

import (
	"context"
	"time"

	"loom.local/loom/internal/storagecatalog"
)

const (
	DecisionSafe          = "safe"
	DecisionNotSafe       = "not_safe"
	DecisionPartiallySafe = "partially_safe"
	DecisionUnknown       = "unknown"
)

type Catalog interface {
	InspectEntry(ctx context.Context, ref string) (storagecatalog.EntryDetail, error)
	InspectMainDocumentByPath(ctx context.Context, relativePath string, opts storagecatalog.InspectOptions) (storagecatalog.EntryDetail, error)
	ListEntries(ctx context.Context, filter storagecatalog.ListFilter) ([]storagecatalog.Entry, error)
	RetentionStatus(ctx context.Context) (storagecatalog.RetentionStatus, error)
	CreateTombstone(ctx context.Context, input storagecatalog.TombstoneInput) (storagecatalog.Tombstone, error)
}

type MainDocumentCloudCoverageProvider func(ctx context.Context) (storagecatalog.MainDocumentCloudBackup, error)

type Service struct {
	Catalog                   Catalog
	MainDocumentCloudCoverage MainDocumentCloudCoverageProvider
	Now                       func() time.Time
}

type SafeToDeleteInput struct {
	Ref string `json:"ref"`
}

type SafeToDeleteResult struct {
	Ref          string                       `json:"ref"`
	Decision     string                       `json:"decision"`
	Safe         bool                         `json:"safe"`
	Reasons      []string                     `json:"reasons,omitempty"`
	Blockers     []string                     `json:"blockers,omitempty"`
	Warnings     []string                     `json:"warnings,omitempty"`
	Entry        *storagecatalog.Entry        `json:"entry,omitempty"`
	PhysicalRefs []storagecatalog.PhysicalRef `json:"physical_refs,omitempty"`
	CheckedAt    time.Time                    `json:"checked_at"`
}

type FetchInput struct {
	Ref             string `json:"ref"`
	DestinationPath string `json:"destination_path"`
	Overwrite       bool   `json:"overwrite,omitempty"`
}

type FetchResult struct {
	Ref             string                      `json:"ref"`
	DestinationPath string                      `json:"destination_path"`
	BytesWritten    int64                       `json:"bytes_written"`
	SourceRef       *storagecatalog.PhysicalRef `json:"source_ref,omitempty"`
	Entry           storagecatalog.Entry        `json:"entry"`
	CreatedAt       time.Time                   `json:"created_at"`
}

type RestoreInput struct {
	Ref             string `json:"ref"`
	DestinationPath string `json:"destination_path"`
	Overwrite       bool   `json:"overwrite,omitempty"`
	Reason          string `json:"reason,omitempty"`
	CreatedBy       string `json:"created_by,omitempty"`
}

type RestoreResult struct {
	FetchResult FetchResult               `json:"fetch_result"`
	Tombstone   *storagecatalog.Tombstone `json:"tombstone,omitempty"`
	RestoredAt  time.Time                 `json:"restored_at"`
}

type RecordTombstoneInput struct {
	Ref           string `json:"ref"`
	TombstoneKind string `json:"tombstone_kind,omitempty"`
	Reason        string `json:"reason,omitempty"`
	CreatedBy     string `json:"created_by,omitempty"`
	MarkEntry     bool   `json:"mark_entry,omitempty"`
}

type RecordTombstoneResult struct {
	Entry     storagecatalog.Entry     `json:"entry"`
	Tombstone storagecatalog.Tombstone `json:"tombstone"`
	CreatedAt time.Time                `json:"created_at"`
}
