package modules

import (
	"context"
	"database/sql"
	"strings"
)

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type scanner interface {
	Scan(dest ...any) error
}

func inspectModuleVersion(ctx context.Context, q queryer, ref string) (ModuleVersionInspection, error) {
	versionID, err := resolveModuleVersionID(ctx, q, ref)
	if err != nil {
		return ModuleVersionInspection{}, err
	}
	version, err := getModuleVersion(ctx, q, versionID)
	if err != nil {
		return ModuleVersionInspection{}, err
	}
	pkg, err := getModulePackage(ctx, q, version.ModulePackageID)
	if err != nil {
		return ModuleVersionInspection{}, err
	}
	requirements, err := listRequirements(ctx, q, version.ModuleVersionID)
	if err != nil {
		return ModuleVersionInspection{}, err
	}
	objectTypes, err := listObjectTypeDeclarations(ctx, q, version.ModuleVersionID)
	if err != nil {
		return ModuleVersionInspection{}, err
	}
	providers, err := listProviderDeclarations(ctx, q, version.ModuleVersionID)
	if err != nil {
		return ModuleVersionInspection{}, err
	}
	capabilities, err := listCapabilityDeclarations(ctx, q, version.ModuleVersionID)
	if err != nil {
		return ModuleVersionInspection{}, err
	}
	usageDocs, err := listUsageDocumentDeclarations(ctx, q, version.ModuleVersionID)
	if err != nil {
		return ModuleVersionInspection{}, err
	}
	backupHooks, err := listBackupHookDeclarations(ctx, q, version.ModuleVersionID)
	if err != nil {
		return ModuleVersionInspection{}, err
	}
	return ModuleVersionInspection{
		Package:        pkg,
		Version:        version,
		Requirements:   requirements,
		ObjectTypes:    objectTypes,
		Providers:      providers,
		Capabilities:   capabilities,
		UsageDocuments: usageDocs,
		BackupHooks:    backupHooks,
	}, nil
}

func resolveModuleID(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", sql.ErrNoRows
	}
	var moduleID string
	err := q.QueryRowContext(ctx, `
		SELECT module_id
		FROM modules.module_versions
		WHERE module_id = $1 OR module_version_id = $1
		ORDER BY registered_at DESC
		LIMIT 1
	`, ref).Scan(&moduleID)
	return moduleID, err
}

func resolveModuleVersionID(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", sql.ErrNoRows
	}
	if moduleID, version, ok := strings.Cut(ref, "@"); ok {
		var versionID string
		err := q.QueryRowContext(ctx, `
			SELECT module_version_id
			FROM modules.module_versions
			WHERE module_id = $1 AND version = $2
		`, moduleID, version).Scan(&versionID)
		return versionID, err
	}
	var versionID string
	err := q.QueryRowContext(ctx, `
		SELECT module_version_id
		FROM modules.module_versions
		WHERE module_version_id = $1 OR module_id = $1
		ORDER BY registered_at DESC
		LIMIT 1
	`, ref).Scan(&versionID)
	return versionID, err
}

func getModulePackage(ctx context.Context, q queryer, ref string) (ModulePackage, error) {
	row := q.QueryRowContext(ctx, modulePackageSelectSQL()+" WHERE module_package_id = $1", ref)
	return scanModulePackage(row)
}

func getModuleVersion(ctx context.Context, q queryer, ref string) (ModuleVersion, error) {
	row := q.QueryRowContext(ctx, moduleVersionSelectSQL()+" WHERE module_version_id = $1", ref)
	return scanModuleVersion(row)
}

func getModuleVersionByModuleAndVersion(ctx context.Context, q queryer, moduleID, version string) (ModuleVersion, error) {
	row := q.QueryRowContext(ctx, moduleVersionSelectSQL()+" WHERE module_id = $1 AND version = $2", moduleID, version)
	return scanModuleVersion(row)
}

func listModuleVersions(ctx context.Context, q queryer, moduleID string) ([]ModuleVersion, error) {
	rows, err := q.QueryContext(ctx, moduleVersionSelectSQL()+" WHERE module_id = $1 ORDER BY registered_at DESC", moduleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ModuleVersion{}
	for rows.Next() {
		item, err := scanModuleVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func listRequirements(ctx context.Context, q queryer, moduleVersionID string) ([]RuntimeRequirement, error) {
	rows, err := q.QueryContext(ctx, runtimeRequirementSelectSQL()+" WHERE module_version_id = $1 ORDER BY requirement_kind, requirement_key", moduleVersionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RuntimeRequirement{}
	for rows.Next() {
		item, err := scanRuntimeRequirement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func listObjectTypeDeclarations(ctx context.Context, q queryer, moduleVersionID string) ([]ObjectTypeDeclaration, error) {
	rows, err := q.QueryContext(ctx, objectTypeDeclarationSelectSQL()+" WHERE module_version_id = $1 ORDER BY object_type", moduleVersionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ObjectTypeDeclaration{}
	for rows.Next() {
		item, err := scanObjectTypeDeclaration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func listProviderDeclarations(ctx context.Context, q queryer, moduleVersionID string) ([]ProviderDeclaration, error) {
	rows, err := q.QueryContext(ctx, providerDeclarationSelectSQL()+" WHERE module_version_id = $1 ORDER BY provider_key", moduleVersionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProviderDeclaration{}
	for rows.Next() {
		item, err := scanProviderDeclaration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func listCapabilityDeclarations(ctx context.Context, q queryer, moduleVersionID string) ([]CapabilityDeclaration, error) {
	rows, err := q.QueryContext(ctx, capabilityDeclarationSelectSQL()+" WHERE module_version_id = $1 ORDER BY provider_key, endpoint_name", moduleVersionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CapabilityDeclaration{}
	for rows.Next() {
		item, err := scanCapabilityDeclaration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func listUsageDocumentDeclarations(ctx context.Context, q queryer, moduleVersionID string) ([]UsageDocumentDeclaration, error) {
	rows, err := q.QueryContext(ctx, usageDocumentDeclarationSelectSQL()+" WHERE module_version_id = $1 ORDER BY target_kind, target_ref, path", moduleVersionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageDocumentDeclaration{}
	for rows.Next() {
		item, err := scanUsageDocumentDeclaration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func listBackupHookDeclarations(ctx context.Context, q queryer, moduleVersionID string) ([]BackupHookDeclaration, error) {
	rows, err := q.QueryContext(ctx, backupHookDeclarationSelectSQL()+" WHERE module_version_id = $1 ORDER BY hook_key", moduleVersionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BackupHookDeclaration{}
	for rows.Next() {
		item, err := scanBackupHookDeclaration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func modulePackageSelectSQL(prefix ...string) string {
	columns := `
		       module_package_id, package_kind, source_uri, content_hash, package_size_bytes,
		       manifest_path, discovered_at, registered_by_actor_id, status, metadata
	`
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + ` RETURNING ` + columns
	}
	return `SELECT ` + columns + ` FROM modules.packages`
}

func moduleVersionSelectSQL(prefix ...string) string {
	columns := `
		       module_version_id, module_id, module_name, version, module_package_id,
		       manifest_hash, manifest_json, module_kind, compatible_core_min,
		       compatible_core_max, source_ref, description, registered_at,
		       registered_by_actor_id, status, validation_errors, metadata
	`
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + ` RETURNING ` + columns
	}
	return `SELECT ` + columns + ` FROM modules.module_versions`
}

func runtimeRequirementSelectSQL(prefix ...string) string {
	columns := `
		       module_requirement_id, module_version_id, requirement_kind, requirement_key,
		       required_version, required_status, optional, metadata
	`
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + ` RETURNING ` + columns
	}
	return `SELECT ` + columns + ` FROM modules.runtime_requirements`
}

func objectTypeDeclarationSelectSQL(prefix ...string) string {
	columns := `
		       module_declaration_id, module_version_id, object_type, schema_json,
		       default_classification, default_indexing_policy, default_backup_policy, metadata
	`
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + ` RETURNING ` + columns
	}
	return `SELECT ` + columns + ` FROM modules.object_type_declarations`
}

func providerDeclarationSelectSQL(prefix ...string) string {
	columns := `
		       module_declaration_id, module_version_id, provider_key, display_name,
		       description, provider_type, runtime_requirements, health_check_spec, metadata
	`
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + ` RETURNING ` + columns
	}
	return `SELECT ` + columns + ` FROM modules.provider_declarations`
}

func capabilityDeclarationSelectSQL(prefix ...string) string {
	columns := `
		       module_declaration_id, module_version_id, provider_key, endpoint_name,
		       capability_class_namespace, capability_class_name, display_name,
		       description, form, input_schema_json, output_schema_json,
		       execution_authorization_level, risk_level, side_effects_json,
		       credential_requirements_json, policy_requirements_json, metadata
	`
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + ` RETURNING ` + columns
	}
	return `SELECT ` + columns + ` FROM modules.capability_declarations`
}

func usageDocumentDeclarationSelectSQL(prefix ...string) string {
	columns := `
		       module_declaration_id, module_version_id, target_kind, target_ref,
		       title, path, body_format, section_map_json, visibility_policy_json,
		       review_status, content_hash, metadata
	`
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + ` RETURNING ` + columns
	}
	return `SELECT ` + columns + ` FROM modules.usage_document_declarations`
}

func backupHookDeclarationSelectSQL(prefix ...string) string {
	columns := `
		       module_declaration_id, module_version_id, hook_key, hook_kind,
		       trigger_mode, output_schema_json, retention_policy_json, metadata
	`
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + ` RETURNING ` + columns
	}
	return `SELECT ` + columns + ` FROM modules.backup_hook_declarations`
}

func scanModulePackage(s scanner) (ModulePackage, error) {
	var item ModulePackage
	var metadata []byte
	if err := s.Scan(
		&item.ModulePackageID,
		&item.PackageKind,
		&item.SourceURI,
		&item.ContentHash,
		&item.PackageSize,
		&item.ManifestPath,
		&item.DiscoveredAt,
		&item.RegisteredBy,
		&item.Status,
		&metadata,
	); err != nil {
		return ModulePackage{}, err
	}
	item.Metadata = jsonRaw(metadata)
	return item, nil
}

func scanModuleVersion(s scanner) (ModuleVersion, error) {
	var item ModuleVersion
	var manifestJSON, validationErrors, metadata []byte
	if err := s.Scan(
		&item.ModuleVersionID,
		&item.ModuleID,
		&item.ModuleName,
		&item.Version,
		&item.ModulePackageID,
		&item.ManifestHash,
		&manifestJSON,
		&item.ModuleKind,
		&item.CompatibleMin,
		&item.CompatibleMax,
		&item.SourceRef,
		&item.Description,
		&item.RegisteredAt,
		&item.RegisteredBy,
		&item.Status,
		&validationErrors,
		&metadata,
	); err != nil {
		return ModuleVersion{}, err
	}
	item.ManifestJSON = jsonRaw(manifestJSON)
	item.ValidationErrors = jsonRaw(validationErrors)
	item.Metadata = jsonRaw(metadata)
	return item, nil
}

func scanRuntimeRequirement(s scanner) (RuntimeRequirement, error) {
	var item RuntimeRequirement
	var metadata []byte
	if err := s.Scan(
		&item.ModuleRequirementID,
		&item.ModuleVersionID,
		&item.RequirementKind,
		&item.RequirementKey,
		&item.RequiredVersion,
		&item.RequiredStatus,
		&item.Optional,
		&metadata,
	); err != nil {
		return RuntimeRequirement{}, err
	}
	item.Metadata = jsonRaw(metadata)
	return item, nil
}

func scanObjectTypeDeclaration(s scanner) (ObjectTypeDeclaration, error) {
	var item ObjectTypeDeclaration
	var schemaJSON, metadata []byte
	if err := s.Scan(
		&item.ModuleDeclarationID,
		&item.ModuleVersionID,
		&item.ObjectType,
		&schemaJSON,
		&item.DefaultClassification,
		&item.DefaultIndexingPolicy,
		&item.DefaultBackupPolicy,
		&metadata,
	); err != nil {
		return ObjectTypeDeclaration{}, err
	}
	item.SchemaJSON = jsonRaw(schemaJSON)
	item.Metadata = jsonRaw(metadata)
	return item, nil
}

func scanProviderDeclaration(s scanner) (ProviderDeclaration, error) {
	var item ProviderDeclaration
	var runtimeRequirements, healthCheck, metadata []byte
	if err := s.Scan(
		&item.ModuleDeclarationID,
		&item.ModuleVersionID,
		&item.ProviderKey,
		&item.DisplayName,
		&item.Description,
		&item.ProviderType,
		&runtimeRequirements,
		&healthCheck,
		&metadata,
	); err != nil {
		return ProviderDeclaration{}, err
	}
	item.RuntimeRequirements = jsonRaw(runtimeRequirements)
	item.HealthCheckSpec = jsonRaw(healthCheck)
	item.Metadata = jsonRaw(metadata)
	return item, nil
}

func scanCapabilityDeclaration(s scanner) (CapabilityDeclaration, error) {
	var item CapabilityDeclaration
	var inputSchema, outputSchema, sideEffects, credentialReqs, policyReqs, metadata []byte
	if err := s.Scan(
		&item.ModuleDeclarationID,
		&item.ModuleVersionID,
		&item.ProviderKey,
		&item.EndpointName,
		&item.CapabilityClassNamespace,
		&item.CapabilityClassName,
		&item.DisplayName,
		&item.Description,
		&item.Form,
		&inputSchema,
		&outputSchema,
		&item.ExecutionAuthorizationLevel,
		&item.RiskLevel,
		&sideEffects,
		&credentialReqs,
		&policyReqs,
		&metadata,
	); err != nil {
		return CapabilityDeclaration{}, err
	}
	item.InputSchemaJSON = jsonRaw(inputSchema)
	item.OutputSchemaJSON = jsonRaw(outputSchema)
	item.SideEffectsJSON = jsonRaw(sideEffects)
	item.CredentialRequirementsJSON = jsonRaw(credentialReqs)
	item.PolicyRequirementsJSON = jsonRaw(policyReqs)
	item.Metadata = jsonRaw(metadata)
	return item, nil
}

func scanUsageDocumentDeclaration(s scanner) (UsageDocumentDeclaration, error) {
	var item UsageDocumentDeclaration
	var sectionMap, visibilityPolicy, metadata []byte
	if err := s.Scan(
		&item.ModuleDeclarationID,
		&item.ModuleVersionID,
		&item.TargetKind,
		&item.TargetRef,
		&item.Title,
		&item.Path,
		&item.BodyFormat,
		&sectionMap,
		&visibilityPolicy,
		&item.ReviewStatus,
		&item.ContentHash,
		&metadata,
	); err != nil {
		return UsageDocumentDeclaration{}, err
	}
	item.SectionMapJSON = jsonRaw(sectionMap)
	item.VisibilityPolicyJSON = jsonRaw(visibilityPolicy)
	item.Metadata = jsonRaw(metadata)
	return item, nil
}

func scanBackupHookDeclaration(s scanner) (BackupHookDeclaration, error) {
	var item BackupHookDeclaration
	var outputSchema, retentionPolicy, metadata []byte
	if err := s.Scan(
		&item.ModuleDeclarationID,
		&item.ModuleVersionID,
		&item.HookKey,
		&item.HookKind,
		&item.TriggerMode,
		&outputSchema,
		&retentionPolicy,
		&metadata,
	); err != nil {
		return BackupHookDeclaration{}, err
	}
	item.OutputSchemaJSON = jsonRaw(outputSchema)
	item.RetentionPolicyJSON = jsonRaw(retentionPolicy)
	item.Metadata = jsonRaw(metadata)
	return item, nil
}

func jsonRaw(value []byte) []byte {
	if len(value) == 0 {
		return []byte(`{}`)
	}
	return append([]byte(nil), value...)
}
