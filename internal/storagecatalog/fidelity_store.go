package storagecatalog

import (
	"database/sql"
	"encoding/json"
)

func filesystemObservationSelectSQL() string {
	return `
		SELECT
			storage_filesystem_observation_id,
			storage_entry_id,
			source_area,
			source_node_id,
			source_node_key,
			source_ref,
			logical_path,
			object_kind,
			source_mode,
			executable,
			uid,
			gid,
			user_name,
			group_name,
			symlink_target,
			device_id,
			inode,
			link_count,
			is_hard_link,
			is_sparse,
			logical_size_bytes,
			allocated_bytes,
			has_xattrs,
			xattr_names_json,
			has_acl,
			has_resource_fork,
			has_finder_tags,
			has_quarantine,
			is_package,
			package_kind,
			unicode_form,
			casefold_key,
			hidden,
			generated_metadata,
			permission_denied,
			risks_json,
			observed_at,
			raw_json,
			created_at,
			updated_at
		FROM storage.storage_entry_filesystem_observations`
}

func fidelityFindingSelectSQL() string {
	return `
		SELECT
			storage_fidelity_finding_id,
			storage_filesystem_observation_id,
			storage_entry_id,
			node_id,
			node_key,
			source_area,
			source_ref,
			logical_path,
			severity,
			finding_kind,
			summary,
			detail_json,
			status,
			created_at,
			updated_at,
			resolved_at
		FROM storage.storage_fidelity_findings`
}

func scanFilesystemObservation(row scanner) (FilesystemObservation, error) {
	var observation FilesystemObservation
	var storageEntryID sql.NullString
	var sourceNodeID sql.NullString
	var sourceMode sql.NullInt64
	var uid sql.NullInt64
	var gid sql.NullInt64
	var deviceID sql.NullInt64
	var inode sql.NullInt64
	var linkCount sql.NullInt64
	var allocatedBytes sql.NullInt64
	var xattrNames []byte
	var risks []byte
	var raw []byte

	if err := row.Scan(
		&observation.StorageFilesystemObservationID,
		&storageEntryID,
		&observation.SourceArea,
		&sourceNodeID,
		&observation.SourceNodeKey,
		&observation.SourceRef,
		&observation.LogicalPath,
		&observation.ObjectKind,
		&sourceMode,
		&observation.Executable,
		&uid,
		&gid,
		&observation.UserName,
		&observation.GroupName,
		&observation.SymlinkTarget,
		&deviceID,
		&inode,
		&linkCount,
		&observation.IsHardLink,
		&observation.IsSparse,
		&observation.LogicalSizeBytes,
		&allocatedBytes,
		&observation.HasXattrs,
		&xattrNames,
		&observation.HasACL,
		&observation.HasResourceFork,
		&observation.HasFinderTags,
		&observation.HasQuarantine,
		&observation.IsPackage,
		&observation.PackageKind,
		&observation.UnicodeForm,
		&observation.CasefoldKey,
		&observation.Hidden,
		&observation.GeneratedMetadata,
		&observation.PermissionDenied,
		&risks,
		&observation.ObservedAt,
		&raw,
		&observation.CreatedAt,
		&observation.UpdatedAt,
	); err != nil {
		return FilesystemObservation{}, err
	}

	observation.StorageEntryID = nullableStringPtr(storageEntryID)
	observation.SourceNodeID = nullableStringPtr(sourceNodeID)
	if sourceMode.Valid {
		value := int(sourceMode.Int64)
		observation.SourceMode = &value
	}
	if uid.Valid {
		value := int(uid.Int64)
		observation.UID = &value
	}
	if gid.Valid {
		value := int(gid.Int64)
		observation.GID = &value
	}
	if deviceID.Valid {
		observation.DeviceID = &deviceID.Int64
	}
	if inode.Valid {
		observation.Inode = &inode.Int64
	}
	if linkCount.Valid {
		observation.LinkCount = &linkCount.Int64
	}
	if allocatedBytes.Valid {
		observation.AllocatedBytes = &allocatedBytes.Int64
	}
	observation.XattrNames = scannedStringArray(xattrNames)
	observation.Risks = scannedStringArray(risks)
	observation.RawJSON = normalizedScannedJSON(raw)
	return observation, nil
}

func scanFidelityFinding(row scanner) (FidelityFinding, error) {
	var finding FidelityFinding
	var observationID sql.NullString
	var storageEntryID sql.NullString
	var nodeID sql.NullString
	var detail []byte
	var resolvedAt sql.NullTime

	if err := row.Scan(
		&finding.StorageFidelityFindingID,
		&observationID,
		&storageEntryID,
		&nodeID,
		&finding.NodeKey,
		&finding.SourceArea,
		&finding.SourceRef,
		&finding.LogicalPath,
		&finding.Severity,
		&finding.FindingKind,
		&finding.Summary,
		&detail,
		&finding.Status,
		&finding.CreatedAt,
		&finding.UpdatedAt,
		&resolvedAt,
	); err != nil {
		return FidelityFinding{}, err
	}

	finding.StorageFilesystemObservationID = nullableStringPtr(observationID)
	finding.StorageEntryID = nullableStringPtr(storageEntryID)
	finding.NodeID = nullableStringPtr(nodeID)
	finding.DetailJSON = normalizedScannedJSON(detail)
	if resolvedAt.Valid {
		finding.ResolvedAt = &resolvedAt.Time
	}
	return finding, nil
}

func scannedStringArray(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil
	}
	return values
}
