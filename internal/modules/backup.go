package modules

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
)

func (s Service) ExportModuleBackup(ctx context.Context, req requestctx.Context, input ExportModuleBackupInput) (ModuleBackupExportDetail, error) {
	input.InstallationRef = strings.TrimSpace(input.InstallationRef)
	input.Kind = strings.TrimSpace(input.Kind)
	if input.InstallationRef == "" {
		return ModuleBackupExportDetail{}, fmt.Errorf("installation_ref is required")
	}
	if input.Kind == "" {
		input.Kind = BackupExportKindManifest
	}
	if input.Kind != BackupExportKindManifest {
		return ModuleBackupExportDetail{}, fmt.Errorf("unsupported module backup export kind: %s", input.Kind)
	}
	if err := requireJSONObject(input.Metadata, "metadata"); err != nil {
		return ModuleBackupExportDetail{}, err
	}

	installationID, err := resolveInstallationID(ctx, s.DB, input.InstallationRef)
	if err != nil {
		return ModuleBackupExportDetail{}, err
	}
	installation, err := getInstallation(ctx, s.DB, installationID)
	if err != nil {
		return ModuleBackupExportDetail{}, err
	}
	if installation.Status == InstallationStatusFailed || installation.Status == InstallationStatusRemoved {
		return ModuleBackupExportDetail{}, fmt.Errorf("cannot export backup for module installation with status %s", installation.Status)
	}
	if err := requireActorAdminOnNode(ctx, s.DB, req.ActorID, installation.TargetNodeID); err != nil {
		return ModuleBackupExportDetail{}, err
	}

	detail, err := s.InspectInstallation(ctx, installationID)
	if err != nil {
		return ModuleBackupExportDetail{}, err
	}
	versionDetail, err := s.InspectModuleVersion(ctx, detail.Version.ModuleVersionID)
	if err != nil {
		return ModuleBackupExportDetail{}, err
	}
	hook, hookFound, err := findBackupHookByKind(ctx, s.DB, detail.Version.ModuleVersionID, input.Kind)
	if err != nil {
		return ModuleBackupExportDetail{}, err
	}

	payload := map[string]any{
		"export_kind": input.Kind,
		"exported_at": time.Now().UTC().Format(time.RFC3339Nano),
		"module": map[string]any{
			"module_id":         detail.Version.ModuleID,
			"module_name":       detail.Version.ModuleName,
			"version":           detail.Version.Version,
			"module_version_id": detail.Version.ModuleVersionID,
			"manifest_hash":     detail.Version.ManifestHash,
			"module_kind":       detail.Version.ModuleKind,
			"source_ref":        detail.Version.SourceRef,
		},
		"installation": detail.Installation,
		"runtime": map[string]any{
			"namespaces":    detail.Namespaces,
			"providers":     detail.Providers,
			"capabilities":  detail.Capabilities,
			"health":        detail.Health,
			"compatibility": detail.Compatibility,
		},
		"declarations": map[string]any{
			"requirements":    versionDetail.Requirements,
			"object_types":    versionDetail.ObjectTypes,
			"providers":       versionDetail.Providers,
			"capabilities":    versionDetail.Capabilities,
			"usage_documents": versionDetail.UsageDocuments,
			"backup_hooks":    versionDetail.BackupHooks,
		},
		"manifest_json": detail.Version.ManifestJSON,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return ModuleBackupExportDetail{}, err
	}
	payloadHash := hashBytes(payloadJSON)
	storageURI := detail.Installation.ObjectStorePrefix + "backups/" + input.Kind + "/" + payloadHash + ".json"
	var hookID *string
	if hookFound {
		value := hook.ModuleDeclarationID
		hookID = &value
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ModuleBackupExportDetail{}, err
	}
	defer tx.Rollback()

	row := tx.QueryRowContext(ctx, moduleBackupExportSelectSQL(`
		INSERT INTO modules.backup_exports (
			module_backup_export_id, module_installation_id, module_version_id,
			backup_hook_declaration_id, export_kind, export_status, payload_json,
			payload_hash, storage_uri, created_by_actor_id, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`),
		ids.NewModuleBackupExportID(),
		detail.Installation.ModuleInstallationID,
		detail.Version.ModuleVersionID,
		hookID,
		input.Kind,
		BackupExportStatusCompleted,
		payloadJSON,
		payloadHash,
		storageURI,
		req.ActorID,
		objectOrDefault(input.Metadata),
	)
	export, err := scanModuleBackupExport(row)
	if err != nil {
		return ModuleBackupExportDetail{}, err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       events.TypeModuleBackupExported,
		EventLevel:      "audit",
		Request:         req,
		ScopeID:         detail.Installation.InstallScopeID,
		TargetKind:      "module_installation",
		TargetID:        detail.Installation.ModuleInstallationID,
		Status:          BackupExportStatusCompleted,
		Result:          "exported",
		VisibilityClass: "internal",
		Payload: map[string]any{
			"module_backup_export_id": export.ModuleBackupExportID,
			"module_installation_id":  detail.Installation.ModuleInstallationID,
			"module_version_id":       detail.Version.ModuleVersionID,
			"export_kind":             export.ExportKind,
			"payload_hash":            export.PayloadHash,
			"storage_uri":             export.StorageURI,
		},
	}); err != nil {
		return ModuleBackupExportDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return ModuleBackupExportDetail{}, err
	}
	return ModuleBackupExportDetail{
		Export:       export,
		Installation: detail.Installation,
		Version:      detail.Version,
		Hook:         optionalHook(hook, hookFound),
	}, nil
}

func (s Service) ListModuleBackupExports(ctx context.Context, filter ModuleBackupExportFilter) ([]ModuleBackupExport, error) {
	if filter.Limit <= 0 || filter.Limit > MaxModuleLimit {
		filter.Limit = DefaultModuleLimit
	}
	filter.InstallationRef = strings.TrimSpace(filter.InstallationRef)
	if filter.InstallationRef == "" {
		return nil, fmt.Errorf("installation_ref is required")
	}
	installationID, err := resolveInstallationID(ctx, s.DB, filter.InstallationRef)
	if err != nil {
		return nil, err
	}
	query := moduleBackupExportSelectSQL() + ` WHERE module_installation_id = $1`
	args := []any{installationID}
	if kind := strings.TrimSpace(filter.Kind); kind != "" {
		args = append(args, kind)
		query += fmt.Sprintf(" AND export_kind = $%d", len(args))
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY created_at DESC, module_backup_export_id DESC LIMIT $%d", len(args))
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ModuleBackupExport{}
	for rows.Next() {
		item, err := scanModuleBackupExport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s Service) InspectModuleBackupExport(ctx context.Context, ref string) (ModuleBackupExportDetail, error) {
	export, err := getModuleBackupExport(ctx, s.DB, ref)
	if err != nil {
		return ModuleBackupExportDetail{}, err
	}
	installation, err := getInstallation(ctx, s.DB, export.ModuleInstallationID)
	if err != nil {
		return ModuleBackupExportDetail{}, err
	}
	version, err := getModuleVersion(ctx, s.DB, export.ModuleVersionID)
	if err != nil {
		return ModuleBackupExportDetail{}, err
	}
	var hook *BackupHookDeclaration
	if export.BackupHookDeclarationID != nil {
		decl, err := getBackupHookDeclaration(ctx, s.DB, *export.BackupHookDeclarationID)
		if err != nil {
			return ModuleBackupExportDetail{}, err
		}
		hook = &decl
	}
	return ModuleBackupExportDetail{
		Export:       export,
		Installation: installation,
		Version:      version,
		Hook:         hook,
	}, nil
}

func findBackupHookByKind(ctx context.Context, q queryer, moduleVersionID, kind string) (BackupHookDeclaration, bool, error) {
	rows, err := q.QueryContext(ctx, backupHookDeclarationSelectSQL()+`
		WHERE module_version_id = $1
		  AND hook_kind = $2
		ORDER BY hook_key
		LIMIT 1
	`, moduleVersionID, kind)
	if err != nil {
		return BackupHookDeclaration{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return BackupHookDeclaration{}, false, err
		}
		return BackupHookDeclaration{}, false, nil
	}
	item, err := scanBackupHookDeclaration(rows)
	if err != nil {
		return BackupHookDeclaration{}, false, err
	}
	return item, true, rows.Err()
}

func getBackupHookDeclaration(ctx context.Context, q queryer, ref string) (BackupHookDeclaration, error) {
	row := q.QueryRowContext(ctx, backupHookDeclarationSelectSQL()+`
		WHERE module_declaration_id = $1
	`, ref)
	return scanBackupHookDeclaration(row)
}

func getModuleBackupExport(ctx context.Context, q queryer, ref string) (ModuleBackupExport, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ModuleBackupExport{}, sql.ErrNoRows
	}
	row := q.QueryRowContext(ctx, moduleBackupExportSelectSQL()+`
		WHERE module_backup_export_id = $1
	`, ref)
	return scanModuleBackupExport(row)
}

func moduleBackupExportSelectSQL(prefix ...string) string {
	columns := `module_backup_export_id, module_installation_id, module_version_id,
	        backup_hook_declaration_id, export_kind, export_status, payload_json,
	        payload_hash, storage_uri, created_by_actor_id, created_at, metadata
	`
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + ` RETURNING ` + columns
	}
	return `SELECT ` + columns + ` FROM modules.backup_exports`
}

func scanModuleBackupExport(s scanner) (ModuleBackupExport, error) {
	var item ModuleBackupExport
	var hookID sql.NullString
	var payloadJSON, metadata []byte
	if err := s.Scan(
		&item.ModuleBackupExportID,
		&item.ModuleInstallationID,
		&item.ModuleVersionID,
		&hookID,
		&item.ExportKind,
		&item.ExportStatus,
		&payloadJSON,
		&item.PayloadHash,
		&item.StorageURI,
		&item.CreatedByActorID,
		&item.CreatedAt,
		&metadata,
	); err != nil {
		return ModuleBackupExport{}, err
	}
	if hookID.Valid {
		item.BackupHookDeclarationID = &hookID.String
	}
	item.PayloadJSON = jsonRaw(payloadJSON)
	item.Metadata = jsonRaw(metadata)
	return item, nil
}

func optionalHook(hook BackupHookDeclaration, ok bool) *BackupHookDeclaration {
	if !ok {
		return nil
	}
	return &hook
}
