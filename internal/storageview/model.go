package storageview

import (
	"encoding/json"
	"time"

	"loom.local/loom/internal/storagecatalog"
)

const (
	DefaultViewKey = "loom-main"
)

const (
	EntryKindFile      = "file"
	EntryKindDirectory = "directory"
	EntryKindStatus    = "generated_status"
	EntryKindControl   = "control"
)

const (
	PermissionReadOnly   = "read_only"
	PermissionWritable   = "writable"
	PermissionControlled = "controlled"
)

type Tree struct {
	ViewKey     string      `json:"view_key"`
	Root        Node        `json:"root"`
	Entries     []ViewEntry `json:"entries"`
	Counts      Counts      `json:"counts"`
	GeneratedAt time.Time   `json:"generated_at"`
}

type Node struct {
	Name           string `json:"name"`
	Path           string `json:"path"`
	EntryKind      string `json:"entry_kind"`
	Permissions    string `json:"permissions"`
	Writable       bool   `json:"writable"`
	Generated      bool   `json:"generated"`
	ReadOnlyReason string `json:"read_only_reason,omitempty"`
	Children       []Node `json:"children,omitempty"`
}

type ViewEntry struct {
	ViewPath          string          `json:"view_path"`
	EntryKind         string          `json:"entry_kind"`
	StorageEntryID    string          `json:"storage_entry_id,omitempty"`
	StorageClass      string          `json:"storage_class,omitempty"`
	SourceArea        string          `json:"source_area,omitempty"`
	OriginNodeKey     string          `json:"origin_node_key,omitempty"`
	LogicalPath       string          `json:"logical_path,omitempty"`
	DisplayName       string          `json:"display_name"`
	Permissions       string          `json:"permissions"`
	Writable          bool            `json:"writable"`
	Generated         bool            `json:"generated"`
	ReadOnlyReason    string          `json:"read_only_reason,omitempty"`
	SizeBytes         *int64          `json:"size_bytes,omitempty"`
	ChecksumAlgorithm string          `json:"checksum_algorithm,omitempty"`
	ChecksumHex       string          `json:"checksum_hex,omitempty"`
	FileClass         string          `json:"file_class,omitempty"`
	ProcessingState   string          `json:"processing_state,omitempty"`
	AvailabilityState string          `json:"availability_state,omitempty"`
	RetentionState    string          `json:"retention_state,omitempty"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
}

type Counts struct {
	Entries     int `json:"entries"`
	Files       int `json:"files"`
	Directories int `json:"directories"`
	Generated   int `json:"generated"`
	Writable    int `json:"writable"`
	ReadOnly    int `json:"read_only"`
	Controlled  int `json:"controlled"`
}

type ResolveResult struct {
	Path        string                      `json:"path"`
	ViewEntry   ViewEntry                   `json:"view_entry"`
	EntryDetail *storagecatalog.EntryDetail `json:"entry_detail,omitempty"`
}
