package modules

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
)

type Service struct {
	DB      *sql.DB
	DataDir string
}

func NewService(db *sql.DB) Service {
	return Service{DB: db, DataDir: "/var/lib/loom"}
}

func NewServiceWithDataDir(db *sql.DB, dataDir string) Service {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		dataDir = "/var/lib/loom"
	}
	return Service{DB: db, DataDir: dataDir}
}

func (s Service) RegisterPackage(ctx context.Context, req requestctx.Context, input RegisterPackageInput) (ModuleRegistration, error) {
	input.PackagePath = strings.TrimSpace(input.PackagePath)
	if input.PackagePath == "" {
		err := fmt.Errorf("package_path is required")
		s.appendRegistrationFailed(ctx, req, "", err)
		return ModuleRegistration{}, err
	}
	if err := requireJSONObject(input.Metadata, "metadata"); err != nil {
		s.appendRegistrationFailed(ctx, req, input.PackagePath, err)
		return ModuleRegistration{}, err
	}

	loaded, err := LoadPackage(input.PackagePath)
	if err != nil {
		s.appendRegistrationFailed(ctx, req, input.PackagePath, err)
		return ModuleRegistration{}, err
	}
	manifestJSON, err := json.Marshal(loaded.Manifest)
	if err != nil {
		s.appendRegistrationFailed(ctx, req, loaded.SourceURI, err)
		return ModuleRegistration{}, err
	}

	existing, err := getModuleVersionByModuleAndVersion(ctx, s.DB, loaded.Manifest.Module.ID, loaded.Manifest.Module.Version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ModuleRegistration{}, err
	}
	if err == nil {
		if existing.ManifestHash != loaded.ManifestHash {
			conflict := fmt.Errorf("module %s version %s already exists with a different manifest hash", loaded.Manifest.Module.ID, loaded.Manifest.Module.Version)
			s.appendRegistrationFailed(ctx, req, loaded.SourceURI, conflict)
			return ModuleRegistration{}, conflict
		}
		detail, err := s.InspectModuleVersion(ctx, existing.ModuleVersionID)
		if err != nil {
			return ModuleRegistration{}, err
		}
		return ModuleRegistration{
			Package:        detail.Package,
			Version:        detail.Version,
			Requirements:   detail.Requirements,
			ObjectTypes:    detail.ObjectTypes,
			Providers:      detail.Providers,
			Capabilities:   detail.Capabilities,
			UsageDocuments: detail.UsageDocuments,
			BackupHooks:    detail.BackupHooks,
			Registered:     false,
			Idempotent:     true,
			Message:        "module version already registered with identical manifest hash",
		}, nil
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ModuleRegistration{}, err
	}
	defer tx.Rollback()

	pkg, err := insertPackage(ctx, tx, req, loaded, input.Metadata)
	if err != nil {
		return ModuleRegistration{}, err
	}
	version, err := insertModuleVersion(ctx, tx, req, loaded, pkg.ModulePackageID, manifestJSON, input.Metadata)
	if err != nil {
		return ModuleRegistration{}, err
	}
	requirements, err := insertRequirements(ctx, tx, version.ModuleVersionID, loaded.Manifest)
	if err != nil {
		return ModuleRegistration{}, err
	}
	objectTypes, err := insertObjectTypeDeclarations(ctx, tx, version.ModuleVersionID, loaded.Manifest)
	if err != nil {
		return ModuleRegistration{}, err
	}
	providers, err := insertProviderDeclarations(ctx, tx, version.ModuleVersionID, loaded.Manifest)
	if err != nil {
		return ModuleRegistration{}, err
	}
	capabilityDecls, err := insertCapabilityDeclarations(ctx, tx, version.ModuleVersionID, loaded.Manifest)
	if err != nil {
		return ModuleRegistration{}, err
	}
	usageDocs, err := insertUsageDocumentDeclarations(ctx, tx, version.ModuleVersionID, loaded.Manifest)
	if err != nil {
		return ModuleRegistration{}, err
	}
	backupHooks, err := insertBackupHookDeclarations(ctx, tx, version.ModuleVersionID, loaded.Manifest)
	if err != nil {
		return ModuleRegistration{}, err
	}

	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       events.TypeModuleManifestValidated,
		EventLevel:      "audit",
		Request:         req,
		TargetKind:      "module_version",
		TargetID:        version.ModuleVersionID,
		Status:          "valid",
		Result:          "validated",
		VisibilityClass: "internal",
		Payload: map[string]any{
			"module_id":         version.ModuleID,
			"module_version_id": version.ModuleVersionID,
			"manifest_hash":     version.ManifestHash,
		},
	}); err != nil {
		return ModuleRegistration{}, err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       events.TypeModuleRegistered,
		EventLevel:      "audit",
		Request:         req,
		TargetKind:      "module_version",
		TargetID:        version.ModuleVersionID,
		Status:          version.Status,
		Result:          "registered",
		VisibilityClass: "internal",
		Payload: map[string]any{
			"module_id":            version.ModuleID,
			"module_version_id":    version.ModuleVersionID,
			"module_package_id":    pkg.ModulePackageID,
			"provider_count":       len(providers),
			"capability_count":     len(capabilityDecls),
			"usage_document_count": len(usageDocs),
			"backup_hook_count":    len(backupHooks),
		},
	}); err != nil {
		return ModuleRegistration{}, err
	}
	if err := tx.Commit(); err != nil {
		return ModuleRegistration{}, err
	}

	return ModuleRegistration{
		Package:        pkg,
		Version:        version,
		Requirements:   requirements,
		ObjectTypes:    objectTypes,
		Providers:      providers,
		Capabilities:   capabilityDecls,
		UsageDocuments: usageDocs,
		BackupHooks:    backupHooks,
		Registered:     true,
		Message:        "module package registered; no installation or capability exposure was created",
	}, nil
}

func (s Service) ListModules(ctx context.Context, filter ModuleFilter) ([]ModuleListItem, error) {
	if filter.Limit <= 0 || filter.Limit > MaxModuleLimit {
		filter.Limit = DefaultModuleLimit
	}
	query := `
		WITH ranked AS (
			SELECT mv.*,
			       count(*) OVER (PARTITION BY module_id) AS version_count,
			       row_number() OVER (PARTITION BY module_id ORDER BY registered_at DESC, module_version_id DESC) AS rn
			FROM modules.module_versions mv
			WHERE true
	`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if strings.TrimSpace(filter.Status) != "" {
		add("status =", strings.TrimSpace(filter.Status))
	}
	if strings.TrimSpace(filter.ModuleID) != "" {
		add("module_id =", strings.TrimSpace(filter.ModuleID))
	}
	if strings.TrimSpace(filter.ProjectRef) != "" {
		args = append(args, strings.TrimSpace(filter.ProjectRef))
		query += fmt.Sprintf(`
			AND EXISTS (
				SELECT 1
				FROM projects.project_module_registrations pmr
				JOIN projects.projects p ON p.project_id = pmr.project_id
				WHERE pmr.module_version_id = mv.module_version_id
				  AND pmr.activation_status NOT IN ('stale', 'disabled')
				  AND (pmr.project_id = $%d OR p.slug = $%d OR p.project_scope_id = $%d)
			)
		`, len(args), len(args), len(args))
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(`
		)
		SELECT module_id, module_name, module_version_id, version, module_kind, status,
		       description, registered_at, version_count
		FROM ranked
		WHERE rn = 1
		ORDER BY registered_at DESC
		LIMIT $%d
	`, len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	modules := []ModuleListItem{}
	for rows.Next() {
		var item ModuleListItem
		if err := rows.Scan(
			&item.ModuleID,
			&item.ModuleName,
			&item.LatestVersionID,
			&item.LatestVersion,
			&item.ModuleKind,
			&item.Status,
			&item.Description,
			&item.RegisteredAt,
			&item.VersionCount,
		); err != nil {
			return nil, err
		}
		modules = append(modules, item)
	}
	return modules, rows.Err()
}

func (s Service) InspectModule(ctx context.Context, ref string) (ModuleInspection, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ModuleInspection{}, sql.ErrNoRows
	}
	moduleID, err := resolveModuleID(ctx, s.DB, ref)
	if err != nil {
		return ModuleInspection{}, err
	}
	list, err := s.ListModules(ctx, ModuleFilter{Limit: 1, ModuleID: moduleID})
	if err != nil {
		return ModuleInspection{}, err
	}
	if len(list) == 0 {
		return ModuleInspection{}, sql.ErrNoRows
	}
	versions, err := listModuleVersions(ctx, s.DB, moduleID)
	if err != nil {
		return ModuleInspection{}, err
	}
	latest, err := inspectModuleVersion(ctx, s.DB, list[0].LatestVersionID)
	if err != nil {
		return ModuleInspection{}, err
	}
	return ModuleInspection{
		Module:         list[0],
		Versions:       versions,
		LatestVersion:  latest.Version,
		Requirements:   latest.Requirements,
		ObjectTypes:    latest.ObjectTypes,
		Providers:      latest.Providers,
		Capabilities:   latest.Capabilities,
		UsageDocuments: latest.UsageDocuments,
		BackupHooks:    latest.BackupHooks,
	}, nil
}

func (s Service) InspectModuleVersion(ctx context.Context, ref string) (ModuleVersionInspection, error) {
	return inspectModuleVersion(ctx, s.DB, ref)
}

func (s Service) appendRegistrationFailed(ctx context.Context, req requestctx.Context, target string, cause error) {
	if s.DB == nil || strings.TrimSpace(req.ActorID) == "" || strings.TrimSpace(req.OriginNodeID) == "" {
		return
	}
	_, _ = events.NewService(s.DB).Append(ctx, events.AppendInput{
		EventType:       events.TypeModuleRegistrationFailed,
		EventLevel:      "audit",
		Request:         req,
		TargetKind:      "module_package",
		TargetID:        target,
		Status:          "invalid",
		Result:          "failed",
		VisibilityClass: "internal",
		Payload: map[string]any{
			"package_path": target,
			"error":        cause.Error(),
		},
	})
}

func insertPackage(ctx context.Context, tx *sql.Tx, req requestctx.Context, loaded LoadedPackage, metadata json.RawMessage) (ModulePackage, error) {
	row := tx.QueryRowContext(ctx, modulePackageSelectSQL(`
		INSERT INTO modules.packages (
			module_package_id, package_kind, source_uri, content_hash, package_size_bytes,
			manifest_path, registered_by_actor_id, status, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (package_kind, source_uri, content_hash)
		DO UPDATE SET status = EXCLUDED.status, metadata = EXCLUDED.metadata
	`),
		ids.NewModulePackageID(),
		PackageKindNativeModule,
		loaded.SourceURI,
		loaded.ContentHash,
		loaded.PackageSizeBytes,
		"module.json",
		req.ActorID,
		PackageStatusRegistered,
		objectOrDefault(metadata),
	)
	return scanModulePackage(row)
}

func insertModuleVersion(ctx context.Context, tx *sql.Tx, req requestctx.Context, loaded LoadedPackage, packageID string, manifestJSON []byte, metadata json.RawMessage) (ModuleVersion, error) {
	row := tx.QueryRowContext(ctx, moduleVersionSelectSQL(`
		INSERT INTO modules.module_versions (
			module_version_id, module_id, module_name, version, module_package_id,
			manifest_hash, manifest_json, module_kind, compatible_core_min, compatible_core_max,
			source_ref, description, registered_by_actor_id, status, validation_errors, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, '[]'::jsonb, $15)
	`),
		ids.NewModuleVersionID(),
		loaded.Manifest.Module.ID,
		loaded.Manifest.Module.Name,
		loaded.Manifest.Module.Version,
		packageID,
		loaded.ManifestHash,
		manifestJSON,
		loaded.Manifest.Module.Kind,
		loaded.Manifest.Module.CompatibleCoreMin,
		loaded.Manifest.Module.CompatibleCoreMax,
		loaded.Manifest.Module.SourceRef,
		loaded.Manifest.Module.Description,
		req.ActorID,
		ModuleVersionStatusValid,
		objectOrDefault(metadata),
	)
	return scanModuleVersion(row)
}

func insertRequirements(ctx context.Context, tx *sql.Tx, moduleVersionID string, manifest Manifest) ([]RuntimeRequirement, error) {
	requirements := []RuntimeRequirement{}
	add := func(kind, key string) error {
		if strings.TrimSpace(key) == "" {
			return nil
		}
		row := tx.QueryRowContext(ctx, runtimeRequirementSelectSQL(`
			INSERT INTO modules.runtime_requirements (
				module_requirement_id, module_version_id, requirement_kind, requirement_key,
				required_version, required_status, optional, metadata
			)
			VALUES ($1, $2, $3, $4, '', '', false, '{}'::jsonb)
		`), ids.NewModuleRequirementID(), moduleVersionID, kind, key)
		requirement, err := scanRuntimeRequirement(row)
		if err != nil {
			return err
		}
		requirements = append(requirements, requirement)
		return nil
	}
	for _, key := range manifest.Requires.RuntimeFeatures {
		if err := add(RequirementKindRuntimeFeature, key); err != nil {
			return nil, err
		}
	}
	for _, key := range manifest.Requires.Modules {
		if err := add(RequirementKindModule, key); err != nil {
			return nil, err
		}
	}
	for _, key := range manifest.Requires.Connectors {
		if err := add(RequirementKindConnector, key); err != nil {
			return nil, err
		}
	}
	for _, key := range manifest.Requires.Credentials {
		if err := add(RequirementKindCredential, key); err != nil {
			return nil, err
		}
	}
	return requirements, nil
}

func insertObjectTypeDeclarations(ctx context.Context, tx *sql.Tx, moduleVersionID string, manifest Manifest) ([]ObjectTypeDeclaration, error) {
	out := make([]ObjectTypeDeclaration, 0, len(manifest.Provides.ObjectTypes))
	for _, obj := range manifest.Provides.ObjectTypes {
		row := tx.QueryRowContext(ctx, objectTypeDeclarationSelectSQL(`
			INSERT INTO modules.object_type_declarations (
				module_declaration_id, module_version_id, object_type, schema_json,
				default_classification, default_indexing_policy, default_backup_policy, metadata
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`),
			ids.NewModuleDeclarationID(),
			moduleVersionID,
			obj.ObjectType,
			obj.SchemaJSON,
			obj.DefaultClassification,
			obj.DefaultIndexingPolicy,
			obj.DefaultBackupPolicy,
			obj.Metadata,
		)
		decl, err := scanObjectTypeDeclaration(row)
		if err != nil {
			return nil, err
		}
		out = append(out, decl)
	}
	return out, nil
}

func insertProviderDeclarations(ctx context.Context, tx *sql.Tx, moduleVersionID string, manifest Manifest) ([]ProviderDeclaration, error) {
	out := make([]ProviderDeclaration, 0, len(manifest.Provides.Providers))
	for _, provider := range manifest.Provides.Providers {
		row := tx.QueryRowContext(ctx, providerDeclarationSelectSQL(`
			INSERT INTO modules.provider_declarations (
				module_declaration_id, module_version_id, provider_key, display_name,
				description, runtime_requirements, health_check_spec, metadata
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`),
			ids.NewModuleDeclarationID(),
			moduleVersionID,
			provider.ProviderKey,
			provider.DisplayName,
			provider.Description,
			provider.RuntimeRequirements,
			provider.HealthCheckSpec,
			provider.Metadata,
		)
		decl, err := scanProviderDeclaration(row)
		if err != nil {
			return nil, err
		}
		out = append(out, decl)
	}
	return out, nil
}

func insertCapabilityDeclarations(ctx context.Context, tx *sql.Tx, moduleVersionID string, manifest Manifest) ([]CapabilityDeclaration, error) {
	out := make([]CapabilityDeclaration, 0, len(manifest.Provides.Capabilities))
	for _, capability := range manifest.Provides.Capabilities {
		row := tx.QueryRowContext(ctx, capabilityDeclarationSelectSQL(`
			INSERT INTO modules.capability_declarations (
				module_declaration_id, module_version_id, provider_key, endpoint_name,
				capability_class_namespace, capability_class_name, display_name, description,
				form, input_schema_json, output_schema_json, execution_authorization_level,
				risk_level, side_effects_json, credential_requirements_json,
				policy_requirements_json, metadata
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		`),
			ids.NewModuleDeclarationID(),
			moduleVersionID,
			capability.ProviderKey,
			capability.EndpointName,
			capability.CapabilityClassNamespace,
			capability.CapabilityClassName,
			capability.DisplayName,
			capability.Description,
			capability.Form,
			capability.InputSchemaJSON,
			capability.OutputSchemaJSON,
			capability.ExecutionAuthorizationLevel,
			capability.RiskLevel,
			capability.SideEffectsJSON,
			capability.CredentialRequirementsJSON,
			capability.PolicyRequirementsJSON,
			capability.Metadata,
		)
		decl, err := scanCapabilityDeclaration(row)
		if err != nil {
			return nil, err
		}
		out = append(out, decl)
	}
	return out, nil
}

func insertUsageDocumentDeclarations(ctx context.Context, tx *sql.Tx, moduleVersionID string, manifest Manifest) ([]UsageDocumentDeclaration, error) {
	out := make([]UsageDocumentDeclaration, 0, len(manifest.Provides.UsageDocuments))
	for _, doc := range manifest.Provides.UsageDocuments {
		row := tx.QueryRowContext(ctx, usageDocumentDeclarationSelectSQL(`
			INSERT INTO modules.usage_document_declarations (
				module_declaration_id, module_version_id, target_kind, target_ref, title,
				path, body_format, section_map_json, visibility_policy_json, review_status,
				content_hash, metadata
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		`),
			ids.NewModuleDeclarationID(),
			moduleVersionID,
			doc.TargetKind,
			doc.TargetRef,
			doc.Title,
			doc.Path,
			doc.BodyFormat,
			doc.SectionMapJSON,
			doc.VisibilityPolicyJSON,
			doc.ReviewStatus,
			doc.ContentHash,
			doc.Metadata,
		)
		decl, err := scanUsageDocumentDeclaration(row)
		if err != nil {
			return nil, err
		}
		out = append(out, decl)
	}
	return out, nil
}

func insertBackupHookDeclarations(ctx context.Context, tx *sql.Tx, moduleVersionID string, manifest Manifest) ([]BackupHookDeclaration, error) {
	out := make([]BackupHookDeclaration, 0, len(manifest.Provides.BackupHooks))
	for _, hook := range manifest.Provides.BackupHooks {
		row := tx.QueryRowContext(ctx, backupHookDeclarationSelectSQL(`
			INSERT INTO modules.backup_hook_declarations (
				module_declaration_id, module_version_id, hook_key, hook_kind,
				trigger_mode, output_schema_json, retention_policy_json, metadata
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`),
			ids.NewModuleDeclarationID(),
			moduleVersionID,
			hook.HookKey,
			hook.HookKind,
			hook.TriggerMode,
			hook.OutputSchemaJSON,
			hook.RetentionPolicyJSON,
			hook.Metadata,
		)
		decl, err := scanBackupHookDeclaration(row)
		if err != nil {
			return nil, err
		}
		out = append(out, decl)
	}
	return out, nil
}
