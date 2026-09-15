package storagecatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"loom.local/loom/internal/filesystemmeta"
)

type LaneCustodyInput struct {
	StorageEntryID        string                      `json:"storage_entry_id,omitempty"`
	StoragePhysicalRefID  string                      `json:"storage_physical_ref_id,omitempty"`
	CustodyNodeKey        string                      `json:"custody_node_key,omitempty"`
	SourceNodeKey         string                      `json:"source_node_key"`
	SourceBoxID           string                      `json:"source_box_id,omitempty"`
	BatchID               string                      `json:"batch_id"`
	RelativeLanePath      string                      `json:"relative_lane_path"`
	FileName              string                      `json:"file_name,omitempty"`
	FileSizeBytes         int64                       `json:"file_size_bytes"`
	ChecksumAlgorithm     string                      `json:"checksum_algorithm"`
	ChecksumValue         string                      `json:"checksum_value"`
	FinalCustodyPath      string                      `json:"final_custody_path"`
	AcceptedAt            time.Time                   `json:"accepted_at"`
	FilesystemObservation *filesystemmeta.Observation `json:"filesystem_observation,omitempty"`
}

func (s Service) RegisterLaneCustody(ctx context.Context, input LaneCustodyInput) (EntryDetail, error) {
	normalized, err := normalizeLaneCustodyInput(input)
	if err != nil {
		return EntryDetail{}, err
	}
	viewPath := laneViewPath(normalized)
	existing, existingErr := s.InspectEntry(ctx, viewPath)
	switch {
	case existingErr == nil:
		normalized.StorageEntryID = existing.Entry.StorageEntryID
		if existing.Entry.AvailabilityState != AvailabilityStateAvailable || latestAvailablePhysicalRefID(existing.PhysicalRefs) == "" {
			break
		}
		if existing.Entry.ChecksumAlgorithm == normalized.ChecksumAlgorithm &&
			existing.Entry.ChecksumHex == normalized.ChecksumValue &&
			int64PtrEqual(existing.Entry.SizeBytes, normalized.FileSizeBytes) {
			return existing, nil
		}
	case existingErr != nil && !isStorageEntryNotFound(existingErr):
		return EntryDetail{}, existingErr
	}

	size := normalized.FileSizeBytes
	metadata, err := laneCustodyMetadata(normalized, viewPath)
	if err != nil {
		return EntryDetail{}, err
	}
	entry, err := s.RegisterEntry(ctx, RegisterEntryInput{
		StorageEntryID:     normalized.StorageEntryID,
		StorageClass:       StorageClassLaneCustody,
		SourceArea:         SourceAreaLane,
		OriginNodeKey:      normalized.SourceNodeKey,
		LogicalPath:        laneLogicalPath(normalized),
		OriginalSourcePath: normalized.RelativeLanePath,
		CurrentViewPath:    viewPath,
		ChecksumAlgorithm:  normalized.ChecksumAlgorithm,
		ChecksumHex:        normalized.ChecksumValue,
		SizeBytes:          &size,
		FileClass:          ClassifyPath(firstNonEmptyLane(normalized.RelativeLanePath, normalized.FileName), ""),
		ProcessingState:    ProcessingStateBackupOnly,
		AvailabilityState:  AvailabilityStateAvailable,
		RetentionState:     RetentionStateNone,
		Metadata:           metadata,
	})
	if err != nil {
		return EntryDetail{}, err
	}
	refMetadata, err := lanePhysicalRefMetadata(normalized)
	if err != nil {
		return EntryDetail{}, err
	}
	ref, err := s.RegisterPhysicalRef(ctx, RegisterPhysicalRefInput{
		StoragePhysicalRefID: normalized.StoragePhysicalRefID,
		StorageEntryID:       entry.StorageEntryID,
		RefKind:              PhysicalRefKindLaneFile,
		URI:                  normalized.FinalCustodyPath,
		NodeKey:              normalized.CustodyNodeKey,
		ContentAddress:       contentAddress(normalized.ChecksumAlgorithm, normalized.ChecksumValue),
		Status:               PhysicalRefStatusAvailable,
		Metadata:             refMetadata,
	})
	if err != nil {
		return EntryDetail{}, err
	}
	return EntryDetail{Entry: entry, PhysicalRefs: []PhysicalRef{ref}}, nil
}

func normalizeLaneCustodyInput(input LaneCustodyInput) (LaneCustodyInput, error) {
	input.StorageEntryID = strings.TrimSpace(input.StorageEntryID)
	input.StoragePhysicalRefID = strings.TrimSpace(input.StoragePhysicalRefID)
	input.CustodyNodeKey = strings.TrimSpace(input.CustodyNodeKey)
	if input.CustodyNodeKey == "" {
		input.CustodyNodeKey = "main"
	}
	input.SourceNodeKey = strings.TrimSpace(input.SourceNodeKey)
	if input.SourceNodeKey == "" {
		return LaneCustodyInput{}, fmt.Errorf("%w: source_node_key is required", ErrInvalid)
	}
	input.SourceBoxID = strings.TrimSpace(input.SourceBoxID)
	input.BatchID = strings.TrimSpace(input.BatchID)
	if input.BatchID == "" {
		return LaneCustodyInput{}, fmt.Errorf("%w: batch_id is required", ErrInvalid)
	}
	relativePath, err := NormalizeLogicalPath(input.RelativeLanePath)
	if err != nil {
		return LaneCustodyInput{}, fmt.Errorf("%w: relative_lane_path is invalid: %v", ErrInvalid, err)
	}
	input.RelativeLanePath = relativePath
	input.FileName = strings.TrimSpace(input.FileName)
	if input.FileName == "" {
		input.FileName = path.Base(relativePath)
	}
	if input.FileSizeBytes < 0 {
		return LaneCustodyInput{}, fmt.Errorf("%w: file_size_bytes cannot be negative", ErrInvalid)
	}
	input.ChecksumAlgorithm = strings.ToLower(strings.TrimSpace(input.ChecksumAlgorithm))
	input.ChecksumValue = strings.ToLower(strings.TrimSpace(input.ChecksumValue))
	if input.ChecksumValue != "" && input.ChecksumAlgorithm == "" {
		return LaneCustodyInput{}, fmt.Errorf("%w: checksum_algorithm is required when checksum_value is set", ErrInvalid)
	}
	input.FinalCustodyPath = NormalizeSourcePath(input.FinalCustodyPath)
	if input.FinalCustodyPath == "" {
		return LaneCustodyInput{}, fmt.Errorf("%w: final_custody_path is required", ErrInvalid)
	}
	if input.AcceptedAt.IsZero() {
		input.AcceptedAt = time.Now().UTC()
	} else {
		input.AcceptedAt = input.AcceptedAt.UTC()
	}
	return input, nil
}

func laneViewPath(input LaneCustodyInput) string {
	return path.Join(input.SourceNodeKey, "Lane", input.AcceptedAt.Format("2006-01-02"), input.BatchID, input.RelativeLanePath)
}

func laneLogicalPath(input LaneCustodyInput) string {
	return path.Join(input.AcceptedAt.Format("2006-01-02"), input.BatchID, input.RelativeLanePath)
}

func laneCustodyMetadata(input LaneCustodyInput, viewPath string) (json.RawMessage, error) {
	payload := map[string]any{
		"schema_version":         "storage.lane_custody.metadata.v0.6.3",
		"source":                 "lane.rsync_accept",
		"source_node_key":        input.SourceNodeKey,
		"source_box_id":          input.SourceBoxID,
		"batch_id":               input.BatchID,
		"relative_lane_path":     input.RelativeLanePath,
		"file_name":              input.FileName,
		"final_custody_path":     input.FinalCustodyPath,
		"visible_storage_path":   viewPath,
		"accepted_at":            input.AcceptedAt.Format(time.RFC3339Nano),
		"filesystem_observation": input.FilesystemObservation,
	}
	addSourceTimestampMetadata(payload, input.FilesystemObservation, nil, nil, "", "")
	return json.Marshal(payload)
}

func lanePhysicalRefMetadata(input LaneCustodyInput) (json.RawMessage, error) {
	payload := map[string]any{
		"schema_version":         "storage.lane_physical_ref.metadata.v0.6.3",
		"source":                 "lane.rsync_accept",
		"source_node_key":        input.SourceNodeKey,
		"batch_id":               input.BatchID,
		"relative_lane_path":     input.RelativeLanePath,
		"filesystem_observation": input.FilesystemObservation,
	}
	addSourceTimestampMetadata(payload, input.FilesystemObservation, nil, nil, "", "")
	return json.Marshal(payload)
}

func firstNonEmptyLane(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
