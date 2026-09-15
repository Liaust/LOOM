package storagecatalog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/filesystemmeta"
)

var ErrDropzoneRetired = errors.New("Dropzone custody registration is retired")

// DropzoneCustodyInput is retained only so older callers fail with an explicit
// retirement error instead of silently registering new custody.
// Deprecated: Dropzone evidence is decode/inspection-only.
type DropzoneCustodyInput struct {
	StorageEntryID        string                      `json:"storage_entry_id,omitempty"`
	StoragePhysicalRefID  string                      `json:"storage_physical_ref_id,omitempty"`
	CustodyNodeKey        string                      `json:"custody_node_key,omitempty"`
	SessionID             string                      `json:"session_id,omitempty"`
	TransferID            string                      `json:"transfer_id"`
	CustodyID             string                      `json:"custody_id,omitempty"`
	SourceNodeKey         string                      `json:"source_node_key"`
	SourceBoxID           string                      `json:"source_box_id,omitempty"`
	RelativeDropzonePath  string                      `json:"relative_dropzone_path"`
	FileName              string                      `json:"file_name,omitempty"`
	FileSizeBytes         int64                       `json:"file_size_bytes"`
	ChecksumAlgorithm     string                      `json:"checksum_algorithm"`
	ChecksumValue         string                      `json:"checksum_value"`
	FinalCustodyPath      string                      `json:"final_custody_path"`
	CustodyMetadataPath   string                      `json:"custody_metadata_path,omitempty"`
	AcceptedAt            time.Time                   `json:"accepted_at"`
	UploadSessionJSONPath string                      `json:"upload_session_json_path,omitempty"`
	FilesystemObservation *filesystemmeta.Observation `json:"filesystem_observation,omitempty"`
}

func (s Service) RegisterDropzoneCustody(_ context.Context, _ DropzoneCustodyInput) (EntryDetail, error) {
	return EntryDetail{}, fmt.Errorf("%w; historical rows remain readable", ErrDropzoneRetired)
}

func contentAddress(algorithm, value string) string {
	algorithm = strings.TrimSpace(algorithm)
	value = strings.TrimSpace(value)
	if algorithm == "" || value == "" {
		return ""
	}
	return algorithm + ":" + value
}
