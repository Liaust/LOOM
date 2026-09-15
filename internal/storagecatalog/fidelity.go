package storagecatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/filesystemmeta"
	"loom.local/loom/internal/ids"
)

const (
	defaultListFilesystemObservationsLimit = 50
	defaultListFidelityFindingsLimit       = 50
	maxListFidelityRowsLimit               = 5000
)

func (s Service) RegisterFilesystemObservation(ctx context.Context, input RegisterFilesystemObservationInput) (FilesystemObservation, error) {
	normalized, err := normalizeRegisterFilesystemObservationInput(input)
	if err != nil {
		return FilesystemObservation{}, err
	}

	row := s.DB.QueryRowContext(ctx, `
		INSERT INTO storage.storage_entry_filesystem_observations (
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
			raw_json
		)
		VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
			$11, $12, $13, $14, $15, $16, $17, $18, $19, $20,
			$21, $22, $23, $24, $25, $26, $27, $28, $29, $30,
			$31, $32, $33, $34, $35, $36, $37, $38
		)
		ON CONFLICT (source_area, source_node_key, source_ref, logical_path) DO UPDATE
		SET storage_entry_id = EXCLUDED.storage_entry_id,
		    source_node_id = EXCLUDED.source_node_id,
		    object_kind = EXCLUDED.object_kind,
		    source_mode = EXCLUDED.source_mode,
		    executable = EXCLUDED.executable,
		    uid = EXCLUDED.uid,
		    gid = EXCLUDED.gid,
		    user_name = EXCLUDED.user_name,
		    group_name = EXCLUDED.group_name,
		    symlink_target = EXCLUDED.symlink_target,
		    device_id = EXCLUDED.device_id,
		    inode = EXCLUDED.inode,
		    link_count = EXCLUDED.link_count,
		    is_hard_link = EXCLUDED.is_hard_link,
		    is_sparse = EXCLUDED.is_sparse,
		    logical_size_bytes = EXCLUDED.logical_size_bytes,
		    allocated_bytes = EXCLUDED.allocated_bytes,
		    has_xattrs = EXCLUDED.has_xattrs,
		    xattr_names_json = EXCLUDED.xattr_names_json,
		    has_acl = EXCLUDED.has_acl,
		    has_resource_fork = EXCLUDED.has_resource_fork,
		    has_finder_tags = EXCLUDED.has_finder_tags,
		    has_quarantine = EXCLUDED.has_quarantine,
		    is_package = EXCLUDED.is_package,
		    package_kind = EXCLUDED.package_kind,
		    unicode_form = EXCLUDED.unicode_form,
		    casefold_key = EXCLUDED.casefold_key,
		    hidden = EXCLUDED.hidden,
		    generated_metadata = EXCLUDED.generated_metadata,
		    permission_denied = EXCLUDED.permission_denied,
		    risks_json = EXCLUDED.risks_json,
		    observed_at = EXCLUDED.observed_at,
		    raw_json = EXCLUDED.raw_json,
		    updated_at = now()
		RETURNING
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
	`,
		normalized.StorageFilesystemObservationID,
		nullableStringArg(normalized.StorageEntryID),
		normalized.SourceArea,
		nullableStringArg(normalized.SourceNodeID),
		normalized.SourceNodeKey,
		normalized.SourceRef,
		normalized.LogicalPath,
		normalized.ObjectKind,
		nullableIntArg(normalized.SourceMode),
		normalized.Executable,
		nullableIntArg(normalized.UID),
		nullableIntArg(normalized.GID),
		normalized.UserName,
		normalized.GroupName,
		normalized.SymlinkTarget,
		nullableInt64Arg(normalized.DeviceID),
		nullableInt64Arg(normalized.Inode),
		nullableInt64Arg(normalized.LinkCount),
		normalized.IsHardLink,
		normalized.IsSparse,
		normalized.LogicalSizeBytes,
		nullableInt64Arg(normalized.AllocatedBytes),
		normalized.HasXattrs,
		mustJSONStringArray(normalized.XattrNames),
		normalized.HasACL,
		normalized.HasResourceFork,
		normalized.HasFinderTags,
		normalized.HasQuarantine,
		normalized.IsPackage,
		normalized.PackageKind,
		normalized.UnicodeForm,
		normalized.CasefoldKey,
		normalized.Hidden,
		normalized.GeneratedMetadata,
		normalized.PermissionDenied,
		mustJSONStringArray(normalized.Risks),
		*normalized.ObservedAt,
		normalized.RawJSON,
	)
	return scanFilesystemObservation(row)
}

func (s Service) ListFilesystemObservations(ctx context.Context, filter FilesystemObservationFilter) ([]FilesystemObservation, error) {
	if filter.Limit <= 0 {
		filter.Limit = defaultListFilesystemObservationsLimit
	} else if filter.Limit > maxListFidelityRowsLimit {
		filter.Limit = maxListFidelityRowsLimit
	}
	if filter.Offset < 0 {
		return nil, fmt.Errorf("%w: offset cannot be negative", ErrInvalid)
	}

	query := filesystemObservationSelectSQL() + ` WHERE 1 = 1`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}

	if value := strings.TrimSpace(filter.StorageEntryID); value != "" {
		if err := ids.Validate(ids.StorageEntryPrefix, value); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		add("storage_entry_id =", value)
	}
	if value := strings.TrimSpace(filter.SourceArea); value != "" {
		if err := requireValid("source_area", value, ValidSourceArea); err != nil {
			return nil, err
		}
		add("source_area =", value)
	}
	if value := strings.TrimSpace(filter.SourceNodeKey); value != "" {
		add("source_node_key =", value)
	}
	if value := strings.TrimSpace(filter.SourceRef); value != "" {
		add("source_ref =", value)
	}
	if value := strings.Trim(strings.TrimSpace(filter.LogicalPath), "/"); value != "" {
		add("logical_path =", value)
	}
	if value := strings.TrimSpace(filter.ObjectKind); value != "" {
		if !filesystemmeta.ValidObjectKind(value) {
			return nil, fmt.Errorf("%w: unsupported object_kind %q", ErrInvalid, value)
		}
		add("object_kind =", value)
	}
	if filter.PermissionOnly {
		query += " AND permission_denied = true"
	}

	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY observed_at DESC, logical_path, storage_filesystem_observation_id LIMIT $%d", len(args))
	if filter.Offset > 0 {
		args = append(args, filter.Offset)
		query += fmt.Sprintf(" OFFSET $%d", len(args))
	}

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var observations []FilesystemObservation
	for rows.Next() {
		observation, err := scanFilesystemObservation(rows)
		if err != nil {
			return nil, err
		}
		observations = append(observations, observation)
	}
	return observations, rows.Err()
}

func (s Service) RegisterFidelityFinding(ctx context.Context, input RegisterFidelityFindingInput) (FidelityFinding, error) {
	normalized, err := normalizeRegisterFidelityFindingInput(input)
	if err != nil {
		return FidelityFinding{}, err
	}

	row := s.DB.QueryRowContext(ctx, `
		INSERT INTO storage.storage_fidelity_findings (
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
			resolved_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (storage_fidelity_finding_id) DO UPDATE
		SET storage_filesystem_observation_id = EXCLUDED.storage_filesystem_observation_id,
		    storage_entry_id = EXCLUDED.storage_entry_id,
		    node_id = EXCLUDED.node_id,
		    node_key = EXCLUDED.node_key,
		    source_area = EXCLUDED.source_area,
		    source_ref = EXCLUDED.source_ref,
		    logical_path = EXCLUDED.logical_path,
		    severity = EXCLUDED.severity,
		    finding_kind = EXCLUDED.finding_kind,
		    summary = EXCLUDED.summary,
		    detail_json = EXCLUDED.detail_json,
		    status = EXCLUDED.status,
		    resolved_at = EXCLUDED.resolved_at,
		    updated_at = now()
		RETURNING
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
	`,
		normalized.StorageFidelityFindingID,
		nullableStringArg(normalized.StorageFilesystemObservationID),
		nullableStringArg(normalized.StorageEntryID),
		nullableStringArg(normalized.NodeID),
		normalized.NodeKey,
		normalized.SourceArea,
		normalized.SourceRef,
		normalized.LogicalPath,
		normalized.Severity,
		normalized.FindingKind,
		normalized.Summary,
		normalized.DetailJSON,
		normalized.Status,
		nullableTimeArg(normalized.ResolvedAt),
	)
	return scanFidelityFinding(row)
}

func (s Service) ListFidelityFindings(ctx context.Context, filter FidelityFindingFilter) ([]FidelityFinding, error) {
	if filter.Limit <= 0 {
		filter.Limit = defaultListFidelityFindingsLimit
	} else if filter.Limit > maxListFidelityRowsLimit {
		filter.Limit = maxListFidelityRowsLimit
	}
	if filter.Offset < 0 {
		return nil, fmt.Errorf("%w: offset cannot be negative", ErrInvalid)
	}

	query := fidelityFindingSelectSQL() + ` WHERE 1 = 1`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}

	if value := strings.TrimSpace(filter.StorageEntryID); value != "" {
		if err := ids.Validate(ids.StorageEntryPrefix, value); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		add("storage_entry_id =", value)
	}
	if value := strings.TrimSpace(filter.StorageFilesystemObservationID); value != "" {
		if err := ids.Validate(ids.StorageFilesystemObservationPrefix, value); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		add("storage_filesystem_observation_id =", value)
	}
	if value := strings.TrimSpace(filter.NodeKey); value != "" {
		add("node_key =", value)
	}
	if value := strings.TrimSpace(filter.SourceArea); value != "" {
		if err := requireValid("source_area", value, ValidSourceArea); err != nil {
			return nil, err
		}
		add("source_area =", value)
	}
	if value := strings.TrimSpace(filter.SourceRef); value != "" {
		add("source_ref =", value)
	}
	if value := strings.Trim(strings.TrimSpace(filter.LogicalPath), "/"); value != "" {
		add("logical_path =", value)
	}
	if value := strings.TrimSpace(filter.Severity); value != "" {
		if !filesystemmeta.ValidFindingSeverity(value) {
			return nil, fmt.Errorf("%w: unsupported severity %q", ErrInvalid, value)
		}
		add("severity =", value)
	}
	if value := strings.TrimSpace(filter.Status); value != "" {
		if !filesystemmeta.ValidFindingStatus(value) {
			return nil, fmt.Errorf("%w: unsupported finding status %q", ErrInvalid, value)
		}
		add("status =", value)
	} else if !filter.IncludeResolved {
		query += " AND status = 'open'"
	}

	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY created_at DESC, severity, logical_path, storage_fidelity_finding_id LIMIT $%d", len(args))
	if filter.Offset > 0 {
		args = append(args, filter.Offset)
		query += fmt.Sprintf(" OFFSET $%d", len(args))
	}

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var findings []FidelityFinding
	for rows.Next() {
		finding, err := scanFidelityFinding(rows)
		if err != nil {
			return nil, err
		}
		findings = append(findings, finding)
	}
	return findings, rows.Err()
}

func (s Service) ResolveFidelityFinding(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if err := ids.Validate(ids.StorageFidelityFindingPrefix, id); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	_, err := scanFidelityFinding(s.DB.QueryRowContext(ctx, `
		UPDATE storage.storage_fidelity_findings
		SET status = $2,
		    resolved_at = now(),
		    updated_at = now()
		WHERE storage_fidelity_finding_id = $1
		RETURNING
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
	`, id, filesystemmeta.FindingStatusResolved))
	return err
}

func normalizeRegisterFilesystemObservationInput(input RegisterFilesystemObservationInput) (RegisterFilesystemObservationInput, error) {
	input.StorageFilesystemObservationID = strings.TrimSpace(input.StorageFilesystemObservationID)
	if input.StorageFilesystemObservationID == "" {
		input.StorageFilesystemObservationID = ids.NewStorageFilesystemObservationID()
	}
	if err := ids.Validate(ids.StorageFilesystemObservationPrefix, input.StorageFilesystemObservationID); err != nil {
		return RegisterFilesystemObservationInput{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}

	input.StorageEntryID = strings.TrimSpace(input.StorageEntryID)
	if input.StorageEntryID != "" {
		if err := ids.Validate(ids.StorageEntryPrefix, input.StorageEntryID); err != nil {
			return RegisterFilesystemObservationInput{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	input.SourceNodeID = strings.TrimSpace(input.SourceNodeID)
	if input.SourceNodeID != "" {
		if err := ids.Validate(ids.NodePrefix, input.SourceNodeID); err != nil {
			return RegisterFilesystemObservationInput{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	input.SourceArea = strings.TrimSpace(input.SourceArea)
	if input.SourceArea == "" {
		input.SourceArea = SourceAreaUnknown
	}
	if err := requireValid("source_area", input.SourceArea, ValidSourceArea); err != nil {
		return RegisterFilesystemObservationInput{}, err
	}
	input.SourceNodeKey = strings.TrimSpace(input.SourceNodeKey)
	input.SourceRef = strings.TrimSpace(input.SourceRef)
	logicalPath, err := NormalizeLogicalPath(input.LogicalPath)
	if err != nil {
		return RegisterFilesystemObservationInput{}, err
	}
	input.LogicalPath = logicalPath
	input.ObjectKind = strings.TrimSpace(input.ObjectKind)
	if input.ObjectKind == "" {
		input.ObjectKind = filesystemmeta.ObjectKindUnknown
	}
	if !filesystemmeta.ValidObjectKind(input.ObjectKind) {
		return RegisterFilesystemObservationInput{}, fmt.Errorf("%w: unsupported object_kind %q", ErrInvalid, input.ObjectKind)
	}
	if input.SourceMode != nil && *input.SourceMode < 0 {
		return RegisterFilesystemObservationInput{}, fmt.Errorf("%w: source_mode cannot be negative", ErrInvalid)
	}
	if input.LogicalSizeBytes < 0 {
		return RegisterFilesystemObservationInput{}, fmt.Errorf("%w: logical_size_bytes cannot be negative", ErrInvalid)
	}
	if input.AllocatedBytes != nil && *input.AllocatedBytes < 0 {
		return RegisterFilesystemObservationInput{}, fmt.Errorf("%w: allocated_bytes cannot be negative", ErrInvalid)
	}
	for _, risk := range input.Risks {
		if !filesystemmeta.ValidFidelityRisk(strings.TrimSpace(risk)) {
			return RegisterFilesystemObservationInput{}, fmt.Errorf("%w: unsupported fidelity risk %q", ErrInvalid, risk)
		}
	}
	input.UserName = strings.TrimSpace(input.UserName)
	input.GroupName = strings.TrimSpace(input.GroupName)
	input.SymlinkTarget = strings.TrimSpace(input.SymlinkTarget)
	input.PackageKind = strings.TrimSpace(input.PackageKind)
	input.UnicodeForm = strings.TrimSpace(input.UnicodeForm)
	input.CasefoldKey = strings.TrimSpace(input.CasefoldKey)
	if input.ObservedAt == nil {
		now := time.Now().UTC()
		input.ObservedAt = &now
	} else {
		observedAt := input.ObservedAt.UTC()
		input.ObservedAt = &observedAt
	}
	rawJSON, err := normalizeJSONObject(input.RawJSON)
	if err != nil {
		return RegisterFilesystemObservationInput{}, fmt.Errorf("%w: raw_json must be a JSON object: %v", ErrInvalid, err)
	}
	input.RawJSON = rawJSON
	return input, nil
}

func normalizeRegisterFidelityFindingInput(input RegisterFidelityFindingInput) (RegisterFidelityFindingInput, error) {
	input.StorageFidelityFindingID = strings.TrimSpace(input.StorageFidelityFindingID)
	if input.StorageFidelityFindingID == "" {
		input.StorageFidelityFindingID = ids.NewStorageFidelityFindingID()
	}
	if err := ids.Validate(ids.StorageFidelityFindingPrefix, input.StorageFidelityFindingID); err != nil {
		return RegisterFidelityFindingInput{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	input.StorageFilesystemObservationID = strings.TrimSpace(input.StorageFilesystemObservationID)
	if input.StorageFilesystemObservationID != "" {
		if err := ids.Validate(ids.StorageFilesystemObservationPrefix, input.StorageFilesystemObservationID); err != nil {
			return RegisterFidelityFindingInput{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	input.StorageEntryID = strings.TrimSpace(input.StorageEntryID)
	if input.StorageEntryID != "" {
		if err := ids.Validate(ids.StorageEntryPrefix, input.StorageEntryID); err != nil {
			return RegisterFidelityFindingInput{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	input.NodeID = strings.TrimSpace(input.NodeID)
	if input.NodeID != "" {
		if err := ids.Validate(ids.NodePrefix, input.NodeID); err != nil {
			return RegisterFidelityFindingInput{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	input.NodeKey = strings.TrimSpace(input.NodeKey)
	input.SourceArea = strings.TrimSpace(input.SourceArea)
	if input.SourceArea == "" {
		input.SourceArea = SourceAreaUnknown
	}
	if err := requireValid("source_area", input.SourceArea, ValidSourceArea); err != nil {
		return RegisterFidelityFindingInput{}, err
	}
	input.SourceRef = strings.TrimSpace(input.SourceRef)
	logicalPath, err := normalizeCatalogPath("logical path", input.LogicalPath, true)
	if err != nil {
		return RegisterFidelityFindingInput{}, err
	}
	input.LogicalPath = logicalPath
	input.Severity = strings.TrimSpace(input.Severity)
	if input.Severity == "" {
		input.Severity = filesystemmeta.FindingSeverityWarning
	}
	if !filesystemmeta.ValidFindingSeverity(input.Severity) {
		return RegisterFidelityFindingInput{}, fmt.Errorf("%w: unsupported severity %q", ErrInvalid, input.Severity)
	}
	input.FindingKind = strings.TrimSpace(input.FindingKind)
	if input.FindingKind == "" {
		return RegisterFidelityFindingInput{}, fmt.Errorf("%w: finding_kind is required", ErrInvalid)
	}
	input.Summary = strings.TrimSpace(input.Summary)
	input.Status = strings.TrimSpace(input.Status)
	if input.Status == "" {
		input.Status = filesystemmeta.FindingStatusOpen
	}
	if !filesystemmeta.ValidFindingStatus(input.Status) {
		return RegisterFidelityFindingInput{}, fmt.Errorf("%w: unsupported finding status %q", ErrInvalid, input.Status)
	}
	if input.ResolvedAt != nil {
		resolvedAt := input.ResolvedAt.UTC()
		input.ResolvedAt = &resolvedAt
	}
	detailJSON, err := normalizeJSONObject(input.DetailJSON)
	if err != nil {
		return RegisterFidelityFindingInput{}, fmt.Errorf("%w: detail_json must be a JSON object: %v", ErrInvalid, err)
	}
	input.DetailJSON = detailJSON
	return input, nil
}

func nullableIntArg(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func mustJSONStringArray(values []string) json.RawMessage {
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			normalized = append(normalized, value)
		}
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return json.RawMessage(`[]`)
	}
	return json.RawMessage(raw)
}
