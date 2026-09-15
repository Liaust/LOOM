package modules

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/events"
	"loom.local/loom/internal/requestctx"
)

type installedCapabilityTarget struct {
	Provider   InstalledProvider
	Capability InstalledCapability
}

func (s Service) ExposeModuleCapability(ctx context.Context, req requestctx.Context, input ExposeModuleCapabilityInput) (ModuleCapabilityExposureResult, error) {
	input.InstallationRef = strings.TrimSpace(input.InstallationRef)
	input.CapabilityRef = strings.TrimSpace(input.CapabilityRef)
	if input.InstallationRef == "" {
		return ModuleCapabilityExposureResult{}, fmt.Errorf("installation_ref is required")
	}
	if input.CapabilityRef == "" {
		return ModuleCapabilityExposureResult{}, fmt.Errorf("capability_ref is required")
	}
	if err := requireJSONObject(input.Metadata, "metadata"); err != nil {
		return ModuleCapabilityExposureResult{}, err
	}

	installationID, err := resolveInstallationID(ctx, s.DB, input.InstallationRef)
	if err != nil {
		return ModuleCapabilityExposureResult{}, err
	}
	installation, err := getInstallation(ctx, s.DB, installationID)
	if err != nil {
		return ModuleCapabilityExposureResult{}, err
	}
	if installation.Status != InstallationStatusEnabled {
		return ModuleCapabilityExposureResult{}, fmt.Errorf("module installation must be enabled before capability exposure; current status is %s", installation.Status)
	}
	if err := requireActorAdminOnNode(ctx, s.DB, req.ActorID, installation.TargetNodeID); err != nil {
		return ModuleCapabilityExposureResult{}, err
	}
	target, err := getInstalledCapabilityTarget(ctx, s.DB, installationID, input.CapabilityRef)
	if err != nil {
		return ModuleCapabilityExposureResult{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ModuleCapabilityExposureResult{}, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		UPDATE capabilities.providers
		SET status = 'active',
		    updated_at = now()
		WHERE provider_id = $1
	`, target.Provider.ProviderID); err != nil {
		return ModuleCapabilityExposureResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO capabilities.provider_health (
			provider_id, health_status, availability_status, last_checked_at,
			last_ok_at, message, details_json
		)
		VALUES ($1, $2, $3, now(), now(), $4, $5)
		ON CONFLICT (provider_id)
		DO UPDATE SET
			health_status = EXCLUDED.health_status,
			availability_status = EXCLUDED.availability_status,
			last_checked_at = EXCLUDED.last_checked_at,
			last_ok_at = EXCLUDED.last_ok_at,
			message = EXCLUDED.message,
			details_json = EXCLUDED.details_json
	`, target.Provider.ProviderID, capabilities.HealthStatusOK, capabilities.AvailabilityStatusAvailable, "module capability exposed", objectOrDefault(input.Metadata)); err != nil {
		return ModuleCapabilityExposureResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE capabilities.endpoint_versions
		SET status = 'active',
		    approved_by_actor_id = COALESCE(approved_by_actor_id, $2),
		    approved_at = COALESCE(approved_at, now())
		WHERE capability_endpoint_version_id = $1
	`, target.Capability.CapabilityEndpointVersionID, req.ActorID); err != nil {
		return ModuleCapabilityExposureResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE capabilities.capability_endpoints
		SET status = 'active',
		    active_endpoint_version_id = $2,
		    updated_at = now()
		WHERE capability_endpoint_id = $1
	`, target.Capability.CapabilityEndpointID, target.Capability.CapabilityEndpointVersionID); err != nil {
		return ModuleCapabilityExposureResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE capabilities.usage_documents
		SET review_status = 'approved',
		    target_address = COALESCE(target_address, $2),
		    approved_by_actor_id = COALESCE(approved_by_actor_id, $3),
		    approved_at = COALESCE(approved_at, now()),
		    updated_at = now()
		WHERE target_kind = 'capability_endpoint'
		  AND target_id = $1
	`, target.Capability.CapabilityEndpointID, target.Capability.CompactAddress, req.ActorID); err != nil {
		return ModuleCapabilityExposureResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE modules.installation_providers
		SET exposure_status = $1,
		    updated_at = now()
		WHERE module_installation_id = $2
		  AND provider_id = $3
	`, ExposureStatusExposed, installationID, target.Provider.ProviderID); err != nil {
		return ModuleCapabilityExposureResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE modules.installation_capabilities
		SET exposure_status = $1,
		    metadata = metadata || $4::jsonb,
		    updated_at = now()
		WHERE module_installation_id = $2
		  AND capability_endpoint_id = $3
	`, ExposureStatusExposed, installationID, target.Capability.CapabilityEndpointID, objectOrDefault(input.Metadata)); err != nil {
		return ModuleCapabilityExposureResult{}, err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       events.TypeModuleCapabilityExposed,
		EventLevel:      "audit",
		Request:         req,
		ScopeID:         installation.InstallScopeID,
		TargetKind:      "module_installation",
		TargetID:        installationID,
		Status:          ExposureStatusExposed,
		Result:          "exposed",
		VisibilityClass: "internal",
		Payload: map[string]any{
			"module_installation_id":         installationID,
			"module_version_id":              installation.ModuleVersionID,
			"provider_id":                    target.Provider.ProviderID,
			"provider_address":               target.Provider.CompactAddress,
			"capability_endpoint_id":         target.Capability.CapabilityEndpointID,
			"capability_address":             target.Capability.CompactAddress,
			"capability_endpoint_version_id": target.Capability.CapabilityEndpointVersionID,
		},
	}); err != nil {
		return ModuleCapabilityExposureResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ModuleCapabilityExposureResult{}, err
	}
	if _, err := s.InspectHealth(ctx, installationID); err != nil {
		return ModuleCapabilityExposureResult{}, err
	}
	return s.moduleCapabilityExposureResult(ctx, installationID, target.Capability.CapabilityEndpointID, ExposureStatusExposed, "module capability exposed")
}

func (s Service) DisableModuleCapability(ctx context.Context, req requestctx.Context, input DisableModuleCapabilityInput) (ModuleCapabilityExposureResult, error) {
	input.InstallationRef = strings.TrimSpace(input.InstallationRef)
	input.CapabilityRef = strings.TrimSpace(input.CapabilityRef)
	if input.InstallationRef == "" {
		return ModuleCapabilityExposureResult{}, fmt.Errorf("installation_ref is required")
	}
	if input.CapabilityRef == "" {
		return ModuleCapabilityExposureResult{}, fmt.Errorf("capability_ref is required")
	}
	if err := requireJSONObject(input.Metadata, "metadata"); err != nil {
		return ModuleCapabilityExposureResult{}, err
	}

	installationID, err := resolveInstallationID(ctx, s.DB, input.InstallationRef)
	if err != nil {
		return ModuleCapabilityExposureResult{}, err
	}
	installation, err := getInstallation(ctx, s.DB, installationID)
	if err != nil {
		return ModuleCapabilityExposureResult{}, err
	}
	if err := requireActorAdminOnNode(ctx, s.DB, req.ActorID, installation.TargetNodeID); err != nil {
		return ModuleCapabilityExposureResult{}, err
	}
	target, err := getInstalledCapabilityTarget(ctx, s.DB, installationID, input.CapabilityRef)
	if err != nil {
		return ModuleCapabilityExposureResult{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ModuleCapabilityExposureResult{}, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		UPDATE capabilities.capability_endpoints
		SET status = 'disabled',
		    active_endpoint_version_id = NULL,
		    updated_at = now()
		WHERE capability_endpoint_id = $1
	`, target.Capability.CapabilityEndpointID); err != nil {
		return ModuleCapabilityExposureResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE capabilities.endpoint_versions
		SET status = 'disabled'
		WHERE capability_endpoint_version_id = $1
	`, target.Capability.CapabilityEndpointVersionID); err != nil {
		return ModuleCapabilityExposureResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE capabilities.usage_documents
		SET review_status = 'pending_review',
		    approved_by_actor_id = NULL,
		    approved_at = NULL,
		    updated_at = now()
		WHERE target_kind = 'capability_endpoint'
		  AND target_id = $1
	`, target.Capability.CapabilityEndpointID); err != nil {
		return ModuleCapabilityExposureResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE modules.installation_capabilities
		SET exposure_status = $1,
		    metadata = metadata || $4::jsonb,
		    updated_at = now()
		WHERE module_installation_id = $2
		  AND capability_endpoint_id = $3
	`, ExposureStatusDisabled, installationID, target.Capability.CapabilityEndpointID, objectOrDefault(input.Metadata)); err != nil {
		return ModuleCapabilityExposureResult{}, err
	}
	if err := updateProviderExposureAfterCapabilityDisable(ctx, tx, installationID, target.Provider.ProviderID); err != nil {
		return ModuleCapabilityExposureResult{}, err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       events.TypeModuleCapabilityDisabled,
		EventLevel:      "audit",
		Request:         req,
		ScopeID:         installation.InstallScopeID,
		TargetKind:      "module_installation",
		TargetID:        installationID,
		Status:          ExposureStatusDisabled,
		Result:          "disabled",
		VisibilityClass: "internal",
		Payload: map[string]any{
			"module_installation_id":         installationID,
			"module_version_id":              installation.ModuleVersionID,
			"provider_id":                    target.Provider.ProviderID,
			"provider_address":               target.Provider.CompactAddress,
			"capability_endpoint_id":         target.Capability.CapabilityEndpointID,
			"capability_address":             target.Capability.CompactAddress,
			"capability_endpoint_version_id": target.Capability.CapabilityEndpointVersionID,
		},
	}); err != nil {
		return ModuleCapabilityExposureResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ModuleCapabilityExposureResult{}, err
	}
	if _, err := s.InspectHealth(ctx, installationID); err != nil {
		return ModuleCapabilityExposureResult{}, err
	}
	return s.moduleCapabilityExposureResult(ctx, installationID, target.Capability.CapabilityEndpointID, ExposureStatusDisabled, "module capability disabled")
}

func getInstalledCapabilityTarget(ctx context.Context, q queryer, installationID, capabilityRef string) (installedCapabilityTarget, error) {
	capabilityRef = strings.TrimSpace(capabilityRef)
	var out installedCapabilityTarget
	var providerMetadata, usageDocumentIDs, capabilityMetadata []byte
	err := q.QueryRowContext(ctx, `
		SELECT
			ip.module_installation_id, ip.module_declaration_id, p.provider_id,
			p.provider_key, p.compact_address, p.display_name, p.description,
			p.provider_type, p.status, ip.exposure_status,
			coalesce(ph.health_status, 'unknown'), coalesce(ph.availability_status, 'unknown'),
			p.metadata, p.created_at, p.updated_at,
			ic.module_installation_id, ic.module_declaration_id, c.capability_class_id,
			e.capability_endpoint_id, ev.capability_endpoint_version_id, e.endpoint_name,
			e.compact_address, c.namespace || '.' || c.name AS class_name,
			c.display_name, c.description, e.form, e.risk_level,
			e.execution_authorization_level, e.status, ev.status,
			ic.exposure_status, ic.usage_document_ids, e.metadata,
			e.created_at, e.updated_at
		FROM modules.installation_capabilities ic
		JOIN capabilities.capability_classes c ON c.capability_class_id = ic.capability_class_id
		JOIN capabilities.capability_endpoints e ON e.capability_endpoint_id = ic.capability_endpoint_id
		JOIN capabilities.endpoint_versions ev ON ev.capability_endpoint_version_id = ic.capability_endpoint_version_id
		JOIN capabilities.providers p ON p.provider_id = e.provider_id
		JOIN modules.installation_providers ip ON ip.module_installation_id = ic.module_installation_id
			AND ip.provider_id = p.provider_id
		LEFT JOIN capabilities.provider_health ph ON ph.provider_id = p.provider_id
		WHERE ic.module_installation_id = $1
		  AND (
		    ic.module_declaration_id = $2
		    OR e.capability_endpoint_id = $2
		    OR e.compact_address = $2
		    OR e.endpoint_name = $2
		    OR p.provider_key || '.' || e.endpoint_name = $2
		  )
		ORDER BY e.compact_address
		LIMIT 1
	`, installationID, capabilityRef).Scan(
		&out.Provider.ModuleInstallationID,
		&out.Provider.ModuleDeclarationID,
		&out.Provider.ProviderID,
		&out.Provider.ProviderKey,
		&out.Provider.CompactAddress,
		&out.Provider.DisplayName,
		&out.Provider.Description,
		&out.Provider.ProviderType,
		&out.Provider.Status,
		&out.Provider.ExposureStatus,
		&out.Provider.HealthStatus,
		&out.Provider.AvailabilityStatus,
		&providerMetadata,
		&out.Provider.CreatedAt,
		&out.Provider.UpdatedAt,
		&out.Capability.ModuleInstallationID,
		&out.Capability.ModuleDeclarationID,
		&out.Capability.CapabilityClassID,
		&out.Capability.CapabilityEndpointID,
		&out.Capability.CapabilityEndpointVersionID,
		&out.Capability.EndpointName,
		&out.Capability.CompactAddress,
		&out.Capability.ClassName,
		&out.Capability.DisplayName,
		&out.Capability.Description,
		&out.Capability.Form,
		&out.Capability.RiskLevel,
		&out.Capability.ExecutionAuthorizationLevel,
		&out.Capability.Status,
		&out.Capability.VersionStatus,
		&out.Capability.ExposureStatus,
		&usageDocumentIDs,
		&capabilityMetadata,
		&out.Capability.CreatedAt,
		&out.Capability.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return installedCapabilityTarget{}, fmt.Errorf("module capability was not found for installation %s: %s", installationID, capabilityRef)
	}
	if err != nil {
		return installedCapabilityTarget{}, err
	}
	out.Provider.Metadata = jsonRaw(providerMetadata)
	out.Capability.UsageDocumentIDs = jsonRaw(usageDocumentIDs)
	out.Capability.Metadata = jsonRaw(capabilityMetadata)
	return out, nil
}

func updateProviderExposureAfterCapabilityDisable(ctx context.Context, tx *sql.Tx, installationID, providerID string) error {
	var exposedCount int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*)
		FROM modules.installation_capabilities ic
		JOIN capabilities.capability_endpoints e ON e.capability_endpoint_id = ic.capability_endpoint_id
		WHERE ic.module_installation_id = $1
		  AND e.provider_id = $2
		  AND ic.exposure_status = $3
		  AND e.status = 'active'
	`, installationID, providerID, ExposureStatusExposed).Scan(&exposedCount); err != nil {
		return err
	}
	if exposedCount > 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE modules.installation_providers
		SET exposure_status = $1,
		    updated_at = now()
		WHERE module_installation_id = $2
		  AND provider_id = $3
	`, ExposureStatusInstalledDisabled, installationID, providerID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE capabilities.provider_health
		SET health_status = $2,
		    availability_status = $3,
		    last_checked_at = now(),
		    message = 'module provider has no exposed capabilities',
		    details_json = '{}'::jsonb
		WHERE provider_id = $1
	`, providerID, capabilities.HealthStatusUnknown, capabilities.AvailabilityStatusUnavailable); err != nil {
		return err
	}
	return nil
}

func (s Service) moduleCapabilityExposureResult(ctx context.Context, installationID, capabilityEndpointID, status, message string) (ModuleCapabilityExposureResult, error) {
	detail, err := s.InspectInstallation(ctx, installationID)
	if err != nil {
		return ModuleCapabilityExposureResult{}, err
	}
	var capability InstalledCapability
	for _, item := range detail.Capabilities {
		if item.CapabilityEndpointID == capabilityEndpointID {
			capability = item
			break
		}
	}
	if capability.CapabilityEndpointID == "" {
		return ModuleCapabilityExposureResult{}, fmt.Errorf("module capability was not found after update: %s", capabilityEndpointID)
	}
	target, err := getInstalledCapabilityTarget(ctx, s.DB, installationID, capabilityEndpointID)
	if err != nil {
		return ModuleCapabilityExposureResult{}, err
	}
	return ModuleCapabilityExposureResult{
		Installation: detail.Installation,
		Version:      detail.Version,
		Provider:     target.Provider,
		Capability:   capability,
		Health:       detail.Health,
		Status:       status,
		Message:      message,
	}, nil
}
