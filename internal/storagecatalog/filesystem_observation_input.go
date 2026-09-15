package storagecatalog

import (
	"encoding/json"
	"time"

	"loom.local/loom/internal/filesystemmeta"
)

type FilesystemObservationInputOptions struct {
	StorageEntryID string
	SourceArea     string
	SourceNodeKey  string
	SourceRef      string
	LogicalPath    string
	ObservedAt     time.Time
}

func FilesystemObservationInputFromMeta(options FilesystemObservationInputOptions, observation filesystemmeta.Observation) RegisterFilesystemObservationInput {
	sourceMode := int(observation.SourceMode)
	input := RegisterFilesystemObservationInput{
		StorageEntryID:    options.StorageEntryID,
		SourceArea:        options.SourceArea,
		SourceNodeKey:     options.SourceNodeKey,
		SourceRef:         options.SourceRef,
		LogicalPath:       options.LogicalPath,
		ObjectKind:        observation.Kind,
		SourceMode:        &sourceMode,
		Executable:        observation.Executable,
		UID:               observation.UID,
		GID:               observation.GID,
		UserName:          observation.UserName,
		GroupName:         observation.GroupName,
		SymlinkTarget:     observation.SymlinkTarget,
		DeviceID:          observation.DeviceID,
		Inode:             observation.Inode,
		LinkCount:         observation.LinkCount,
		IsHardLink:        observation.IsHardLink,
		IsSparse:          observation.IsSparse,
		LogicalSizeBytes:  observation.LogicalSizeBytes,
		AllocatedBytes:    observation.AllocatedBytes,
		HasXattrs:         observation.HasXattrs,
		XattrNames:        append([]string(nil), observation.XattrNames...),
		HasACL:            observation.HasACL,
		HasResourceFork:   observation.HasResourceFork,
		HasFinderTags:     observation.HasFinderTags,
		HasQuarantine:     observation.HasQuarantine,
		IsPackage:         observation.IsPackage,
		PackageKind:       observation.PackageKind,
		UnicodeForm:       observation.UnicodeForm,
		CasefoldKey:       observation.CasefoldKey,
		Hidden:            observation.Hidden,
		GeneratedMetadata: observation.GeneratedMetadata,
		PermissionDenied:  observation.PermissionDenied,
		Risks:             append([]string(nil), observation.Risks...),
	}
	if !options.ObservedAt.IsZero() {
		observedAt := options.ObservedAt.UTC()
		input.ObservedAt = &observedAt
	}
	if raw, err := json.Marshal(observation); err == nil {
		input.RawJSON = raw
	} else {
		input.RawJSON = json.RawMessage(`{}`)
	}
	return input
}

func addSourceTimestampMetadata(payload map[string]any, observation *filesystemmeta.Observation, sourceModifiedAt, sourceCreatedAt *time.Time, sourceModifiedBasis, sourceCreatedBasis string) {
	if observation != nil {
		if observation.SourceModifiedAt != nil {
			sourceModifiedAt = observation.SourceModifiedAt
			sourceModifiedBasis = observation.SourceModifiedBasis
		}
		if observation.SourceCreatedAt != nil {
			sourceCreatedAt = observation.SourceCreatedAt
			sourceCreatedBasis = observation.SourceCreatedBasis
		}
	}
	if sourceModifiedAt != nil && !sourceModifiedAt.IsZero() {
		payload["source_modified_at"] = sourceModifiedAt.UTC().Format(time.RFC3339Nano)
		payload["source_modified_basis"] = firstSourceTimeBasis(sourceModifiedBasis, filesystemmeta.SourceTimeBasisFilesystemMtime)
	}
	if sourceCreatedAt != nil && !sourceCreatedAt.IsZero() {
		payload["source_created_at"] = sourceCreatedAt.UTC().Format(time.RFC3339Nano)
		payload["source_created_basis"] = firstSourceTimeBasis(sourceCreatedBasis, filesystemmeta.SourceTimeBasisFilesystemBirthtime)
	}
}

func firstSourceTimeBasis(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
