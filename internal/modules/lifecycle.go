package modules

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
	coreversion "loom.local/loom/internal/version"
)

type nodeRecord struct {
	ID     string
	Key    string
	Role   string
	Status string
}

type scopeRecord struct {
	ID   string
	Key  string
	Slug string
}

func (s Service) CheckCompatibility(ctx context.Context, input CompatibilityInput) (CompatibilityReport, error) {
	input.ModuleVersionRef = strings.TrimSpace(input.ModuleVersionRef)
	if input.ModuleVersionRef == "" {
		return CompatibilityReport{}, fmt.Errorf("module_version_ref is required")
	}
	targetNodeRef := defaultString(strings.TrimSpace(input.TargetNodeRef), "main")
	installScopeRef := defaultString(strings.TrimSpace(input.InstallScopeRef), "system")

	versionID, err := resolveModuleVersionID(ctx, s.DB, input.ModuleVersionRef)
	if err != nil {
		return CompatibilityReport{}, err
	}
	version, err := getModuleVersion(ctx, s.DB, versionID)
	if err != nil {
		return CompatibilityReport{}, err
	}
	pkg, err := getModulePackage(ctx, s.DB, version.ModulePackageID)
	if err != nil {
		return CompatibilityReport{}, err
	}
	node, err := resolveNode(ctx, s.DB, targetNodeRef)
	if err != nil {
		return CompatibilityReport{}, fmt.Errorf("resolve target node: %w", err)
	}
	scope, err := resolveScope(ctx, s.DB, installScopeRef)
	if err != nil {
		return CompatibilityReport{}, fmt.Errorf("resolve install scope: %w", err)
	}

	report := CompatibilityReport{
		CanInstall:      true,
		Status:          CompatibilityStatusOK,
		ModuleVersionID: version.ModuleVersionID,
		ModuleID:        version.ModuleID,
		TargetNodeID:    node.ID,
		InstallScopeID:  scope.ID,
	}
	block := func(reason string) {
		report.MissingRequirements = append(report.MissingRequirements, reason)
		report.CanInstall = false
		report.Status = CompatibilityStatusBlocked
	}
	warn := func(reason string) {
		report.Warnings = append(report.Warnings, reason)
		if report.Status == CompatibilityStatusOK {
			report.Status = CompatibilityStatusDegraded
		}
	}

	if node.Key != "main" && node.Role != "main" {
		block("v0.1 module installation only supports the main node")
	}
	if node.Status != "active" {
		block("target node is not active")
	}
	if pkg.PackageKind != PackageKindNativeModule {
		block("module package is not a native_module package")
	}
	if version.Status != ModuleVersionStatusValid {
		block("module version is not valid")
	}
	if version.CompatibleMin != "" && !compatibleWithCurrent(version.CompatibleMin) {
		block("module requires a newer LOOM core version: " + version.CompatibleMin)
	}
	if version.CompatibleMax != "" {
		warn("compatible_core_max is recorded but not enforced in v0.1")
	}

	requirements, err := listRequirements(ctx, s.DB, version.ModuleVersionID)
	if err != nil {
		return CompatibilityReport{}, err
	}
	supportedRuntimeFeatures := map[string]struct{}{
		"postgres":            {},
		"object_store":        {},
		"capability_registry": {},
		"event_log":           {},
		"realtime":            {},
		"sync":                {},
	}
	for _, requirement := range requirements {
		switch requirement.RequirementKind {
		case RequirementKindRuntimeFeature:
			if _, ok := supportedRuntimeFeatures[requirement.RequirementKey]; !ok {
				block("missing runtime feature: " + requirement.RequirementKey)
			}
		case RequirementKindConnector:
			block("connector dependency deferred in v0.1: " + requirement.RequirementKey)
		case RequirementKindCredential:
			warn("credential requirement must be checked by the installed module later: " + requirement.RequirementKey)
		case RequirementKindModule:
			if !moduleExists(ctx, s.DB, requirement.RequirementKey) {
				block("required module is not registered: " + requirement.RequirementKey)
			}
		default:
			warn("requirement kind is recorded but not actively enforced in v0.1: " + requirement.RequirementKind)
		}
	}
	sort.Strings(report.MissingRequirements)
	sort.Strings(report.Warnings)
	return report, nil
}

func (s Service) InstallModule(ctx context.Context, req requestctx.Context, input InstallModuleInput) (ModuleInstallationDetail, error) {
	input.ModuleVersionRef = strings.TrimSpace(input.ModuleVersionRef)
	if input.ModuleVersionRef == "" {
		return ModuleInstallationDetail{}, fmt.Errorf("module_version_ref is required")
	}
	input.TargetNodeRef = defaultString(strings.TrimSpace(input.TargetNodeRef), "main")
	input.InstallScopeRef = defaultString(strings.TrimSpace(input.InstallScopeRef), "system")
	if err := requireJSONObject(input.Metadata, "metadata"); err != nil {
		return ModuleInstallationDetail{}, err
	}

	report, err := s.CheckCompatibility(ctx, CompatibilityInput{
		ModuleVersionRef: input.ModuleVersionRef,
		TargetNodeRef:    input.TargetNodeRef,
		InstallScopeRef:  input.InstallScopeRef,
	})
	if err != nil {
		return ModuleInstallationDetail{}, err
	}
	if !report.CanInstall {
		return ModuleInstallationDetail{}, fmt.Errorf("module compatibility blocked install: %s", strings.Join(report.MissingRequirements, "; "))
	}
	if err := requireActorAdminOnNode(ctx, s.DB, req.ActorID, report.TargetNodeID); err != nil {
		return ModuleInstallationDetail{}, err
	}

	if existing, err := getInstallationByUniqueTarget(ctx, s.DB, report.ModuleVersionID, report.TargetNodeID, report.InstallScopeID); err == nil {
		detail, err := s.InspectInstallation(ctx, existing.ModuleInstallationID)
		if err != nil {
			return ModuleInstallationDetail{}, err
		}
		detail.Idempotent = true
		detail.Message = "module is already installed for this module version, node, and scope"
		return detail, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ModuleInstallationDetail{}, err
	}

	versionDetail, err := inspectModuleVersion(ctx, s.DB, report.ModuleVersionID)
	if err != nil {
		return ModuleInstallationDetail{}, err
	}
	manifest, err := manifestFromVersion(versionDetail.Version)
	if err != nil {
		return ModuleInstallationDetail{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ModuleInstallationDetail{}, err
	}
	defer tx.Rollback()

	installationID := ids.NewModuleInstallationID()
	namespaceRoot := namespaceRoot(versionDetail.Version.ModuleID, installationID)
	paths := installationPaths(s.DataDir, versionDetail.Version.ModuleID, installationID, manifest.Storage.Database.Required)
	reportJSON, err := json.Marshal(report)
	if err != nil {
		return ModuleInstallationDetail{}, err
	}
	installation, err := insertInstallationTx(ctx, tx, req, installationID, versionDetail.Version, report, namespaceRoot, paths, reportJSON, input.Metadata)
	if err != nil {
		return ModuleInstallationDetail{}, err
	}
	failInstall := func(cause error) (ModuleInstallationDetail, error) {
		_, _ = tx.ExecContext(ctx, `
			UPDATE modules.installations
			SET status = $1, failure_reason = $2, updated_at = now()
			WHERE module_installation_id = $3
		`, InstallationStatusFailed, cause.Error(), installation.ModuleInstallationID)
		_, _ = events.AppendTx(ctx, tx, events.AppendInput{
			EventType:       events.TypeModuleInstallFailed,
			EventLevel:      "audit",
			Request:         req,
			ScopeID:         installation.InstallScopeID,
			TargetKind:      "module_installation",
			TargetID:        installation.ModuleInstallationID,
			Status:          InstallationStatusFailed,
			Result:          "failed",
			VisibilityClass: "internal",
			Payload: map[string]any{
				"module_installation_id": installation.ModuleInstallationID,
				"module_version_id":      installation.ModuleVersionID,
				"error":                  cause.Error(),
			},
		})
		_ = tx.Commit()
		return ModuleInstallationDetail{}, cause
	}

	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       events.TypeModuleInstallStarted,
		EventLevel:      "audit",
		Request:         req,
		ScopeID:         installation.InstallScopeID,
		TargetKind:      "module_installation",
		TargetID:        installation.ModuleInstallationID,
		Status:          InstallationStatusInstalling,
		Result:          "started",
		VisibilityClass: "internal",
		Payload: map[string]any{
			"module_installation_id": installation.ModuleInstallationID,
			"module_version_id":      installation.ModuleVersionID,
			"target_node_id":         installation.TargetNodeID,
			"install_scope_id":       installation.InstallScopeID,
		},
	}); err != nil {
		return failInstall(err)
	}

	if err := reserveNamespacesTx(ctx, tx, installation, manifest, paths); err != nil {
		return failInstall(err)
	}
	providerIDs, err := installDeclaredProvidersTx(ctx, tx, req, installation, versionDetail)
	if err != nil {
		return failInstall(err)
	}
	capabilityIDs, err := installDeclaredCapabilitiesTx(ctx, tx, req, installation, versionDetail, providerIDs)
	if err != nil {
		return failInstall(err)
	}
	if err := installUsageDocumentsTx(ctx, tx, req, installation, versionDetail, capabilityIDs); err != nil {
		return failInstall(err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE modules.installations
		SET status = $1, installed_at = now(), updated_at = now()
		WHERE module_installation_id = $2
	`, InstallationStatusInstalled, installation.ModuleInstallationID); err != nil {
		return failInstall(err)
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       events.TypeModuleInstalled,
		EventLevel:      "audit",
		Request:         req,
		ScopeID:         installation.InstallScopeID,
		TargetKind:      "module_installation",
		TargetID:        installation.ModuleInstallationID,
		Status:          InstallationStatusInstalled,
		Result:          "installed",
		VisibilityClass: "internal",
		Payload: map[string]any{
			"module_installation_id": installation.ModuleInstallationID,
			"module_version_id":      installation.ModuleVersionID,
			"provider_count":         len(providerIDs),
			"capability_count":       len(capabilityIDs),
		},
	}); err != nil {
		return failInstall(err)
	}
	if err := tx.Commit(); err != nil {
		return ModuleInstallationDetail{}, err
	}
	if _, err := s.InspectHealth(ctx, installation.ModuleInstallationID); err != nil {
		return ModuleInstallationDetail{}, err
	}
	return s.InspectInstallation(ctx, installation.ModuleInstallationID)
}

func (s Service) EnableInstallation(ctx context.Context, req requestctx.Context, ref string) (ModuleInstallationDetail, error) {
	installationID, err := resolveInstallationID(ctx, s.DB, ref)
	if err != nil {
		return ModuleInstallationDetail{}, err
	}
	installation, err := getInstallation(ctx, s.DB, installationID)
	if err != nil {
		return ModuleInstallationDetail{}, err
	}
	if err := requireActorAdminOnNode(ctx, s.DB, req.ActorID, installation.TargetNodeID); err != nil {
		return ModuleInstallationDetail{}, err
	}
	if installation.Status == InstallationStatusFailed || installation.Status == InstallationStatusRemoved {
		return ModuleInstallationDetail{}, fmt.Errorf("cannot enable module installation with status %s", installation.Status)
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ModuleInstallationDetail{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		UPDATE modules.installations
		SET status = $1, enabled_at = now(), updated_at = now()
		WHERE module_installation_id = $2
	`, InstallationStatusEnabled, installationID); err != nil {
		return ModuleInstallationDetail{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE modules.namespaces
		SET status = $1, updated_at = now()
		WHERE module_installation_id = $2
		  AND status IN ('reserved', 'disabled')
	`, NamespaceStatusActive, installationID); err != nil {
		return ModuleInstallationDetail{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE capabilities.providers p
		SET status = 'active', updated_at = now()
		FROM modules.installation_providers ip
		WHERE ip.provider_id = p.provider_id
		  AND ip.module_installation_id = $1
	`, installationID); err != nil {
		return ModuleInstallationDetail{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE modules.installation_providers
		SET exposure_status = $1, updated_at = now()
		WHERE module_installation_id = $2
	`, ExposureStatusInstalledDisabled, installationID); err != nil {
		return ModuleInstallationDetail{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE capabilities.capability_endpoints e
		SET status = 'disabled',
		    active_endpoint_version_id = NULL,
		    updated_at = now()
		FROM modules.installation_capabilities ic
		WHERE ic.capability_endpoint_id = e.capability_endpoint_id
		  AND ic.module_installation_id = $1
	`, installationID); err != nil {
		return ModuleInstallationDetail{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE modules.installation_capabilities
		SET exposure_status = $1, updated_at = now()
		WHERE module_installation_id = $2
	`, ExposureStatusInstalledDisabled, installationID); err != nil {
		return ModuleInstallationDetail{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE capabilities.usage_documents ud
		SET review_status = 'pending_review',
		    approved_by_actor_id = NULL,
		    approved_at = NULL,
		    updated_at = now()
		FROM modules.installation_capabilities ic
		WHERE ud.target_kind = 'capability_endpoint'
		  AND ud.target_id = ic.capability_endpoint_id
		  AND ic.module_installation_id = $1
	`, installationID); err != nil {
		return ModuleInstallationDetail{}, err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       events.TypeModuleEnabled,
		EventLevel:      "audit",
		Request:         req,
		ScopeID:         installation.InstallScopeID,
		TargetKind:      "module_installation",
		TargetID:        installationID,
		Status:          InstallationStatusEnabled,
		Result:          "enabled",
		VisibilityClass: "internal",
		Payload: map[string]any{
			"module_installation_id": installationID,
		},
	}); err != nil {
		return ModuleInstallationDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return ModuleInstallationDetail{}, err
	}
	if _, err := s.InspectHealth(ctx, installationID); err != nil {
		return ModuleInstallationDetail{}, err
	}
	return s.InspectInstallation(ctx, installationID)
}

func (s Service) DisableInstallation(ctx context.Context, req requestctx.Context, ref string) (ModuleInstallationDetail, error) {
	installationID, err := resolveInstallationID(ctx, s.DB, ref)
	if err != nil {
		return ModuleInstallationDetail{}, err
	}
	installation, err := getInstallation(ctx, s.DB, installationID)
	if err != nil {
		return ModuleInstallationDetail{}, err
	}
	if err := requireActorAdminOnNode(ctx, s.DB, req.ActorID, installation.TargetNodeID); err != nil {
		return ModuleInstallationDetail{}, err
	}
	if installation.Status == InstallationStatusFailed || installation.Status == InstallationStatusRemoved {
		return ModuleInstallationDetail{}, fmt.Errorf("cannot disable module installation with status %s", installation.Status)
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ModuleInstallationDetail{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		UPDATE modules.installations
		SET status = $1, disabled_at = now(), updated_at = now()
		WHERE module_installation_id = $2
	`, InstallationStatusDisabled, installationID); err != nil {
		return ModuleInstallationDetail{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE modules.namespaces
		SET status = $1, updated_at = now()
		WHERE module_installation_id = $2
		  AND status IN ('reserved', 'active')
	`, NamespaceStatusDisabled, installationID); err != nil {
		return ModuleInstallationDetail{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE capabilities.providers p
		SET status = 'disabled', updated_at = now()
		FROM modules.installation_providers ip
		WHERE ip.provider_id = p.provider_id
		  AND ip.module_installation_id = $1
	`, installationID); err != nil {
		return ModuleInstallationDetail{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE capabilities.provider_health ph
		SET health_status = $2,
		    availability_status = $3,
		    last_checked_at = now(),
		    message = 'module installation disabled',
		    details_json = '{}'::jsonb
		FROM modules.installation_providers ip
		WHERE ip.provider_id = ph.provider_id
		  AND ip.module_installation_id = $1
	`, installationID, capabilities.HealthStatusUnknown, capabilities.AvailabilityStatusUnavailable); err != nil {
		return ModuleInstallationDetail{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE capabilities.capability_endpoints e
		SET status = 'disabled',
		    active_endpoint_version_id = NULL,
		    updated_at = now()
		FROM modules.installation_capabilities ic
		WHERE ic.capability_endpoint_id = e.capability_endpoint_id
		  AND ic.module_installation_id = $1
	`, installationID); err != nil {
		return ModuleInstallationDetail{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE capabilities.endpoint_versions ev
		SET status = 'disabled'
		FROM modules.installation_capabilities ic
		WHERE ic.capability_endpoint_version_id = ev.capability_endpoint_version_id
		  AND ic.module_installation_id = $1
	`, installationID); err != nil {
		return ModuleInstallationDetail{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE modules.installation_providers
		SET exposure_status = $1, updated_at = now()
		WHERE module_installation_id = $2
	`, ExposureStatusDisabled, installationID); err != nil {
		return ModuleInstallationDetail{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE modules.installation_capabilities
		SET exposure_status = $1, updated_at = now()
		WHERE module_installation_id = $2
	`, ExposureStatusDisabled, installationID); err != nil {
		return ModuleInstallationDetail{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE capabilities.usage_documents ud
		SET review_status = 'pending_review',
		    approved_by_actor_id = NULL,
		    approved_at = NULL,
		    updated_at = now()
		FROM modules.installation_capabilities ic
		WHERE ud.target_kind = 'capability_endpoint'
		  AND ud.target_id = ic.capability_endpoint_id
		  AND ic.module_installation_id = $1
	`, installationID); err != nil {
		return ModuleInstallationDetail{}, err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       events.TypeModuleDisabled,
		EventLevel:      "audit",
		Request:         req,
		ScopeID:         installation.InstallScopeID,
		TargetKind:      "module_installation",
		TargetID:        installationID,
		Status:          InstallationStatusDisabled,
		Result:          "disabled",
		VisibilityClass: "internal",
		Payload: map[string]any{
			"module_installation_id": installationID,
		},
	}); err != nil {
		return ModuleInstallationDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return ModuleInstallationDetail{}, err
	}
	if _, err := s.InspectHealth(ctx, installationID); err != nil {
		return ModuleInstallationDetail{}, err
	}
	return s.InspectInstallation(ctx, installationID)
}

func (s Service) InspectInstallation(ctx context.Context, ref string) (ModuleInstallationDetail, error) {
	installationID, err := resolveInstallationID(ctx, s.DB, ref)
	if err != nil {
		return ModuleInstallationDetail{}, err
	}
	installation, err := getInstallation(ctx, s.DB, installationID)
	if err != nil {
		return ModuleInstallationDetail{}, err
	}
	version, err := getModuleVersion(ctx, s.DB, installation.ModuleVersionID)
	if err != nil {
		return ModuleInstallationDetail{}, err
	}
	namespaces, err := listNamespaces(ctx, s.DB, installationID)
	if err != nil {
		return ModuleInstallationDetail{}, err
	}
	providers, err := listInstalledProviders(ctx, s.DB, installationID)
	if err != nil {
		return ModuleInstallationDetail{}, err
	}
	capabilityList, err := listInstalledCapabilities(ctx, s.DB, installationID)
	if err != nil {
		return ModuleInstallationDetail{}, err
	}
	health, found, err := getLatestHealth(ctx, s.DB, installationID)
	if err != nil {
		return ModuleInstallationDetail{}, err
	}
	compatibility := CompatibilityReport{}
	_ = json.Unmarshal(installation.CompatibilityReport, &compatibility)
	var healthPtr *ModuleHealthSnapshot
	if found {
		healthPtr = &health
	}
	return ModuleInstallationDetail{
		Installation:  installation,
		Version:       version,
		Namespaces:    namespaces,
		Providers:     providers,
		Capabilities:  capabilityList,
		Health:        healthPtr,
		Compatibility: compatibility,
	}, nil
}

func (s Service) InspectHealth(ctx context.Context, ref string) (ModuleHealthDetail, error) {
	installationID, err := resolveInstallationID(ctx, s.DB, ref)
	if err != nil {
		return ModuleHealthDetail{}, err
	}
	detail, err := s.InspectInstallation(ctx, installationID)
	if err != nil {
		return ModuleHealthDetail{}, err
	}
	health, err := s.deriveHealth(ctx, detail)
	if err != nil {
		return ModuleHealthDetail{}, err
	}
	return ModuleHealthDetail{
		Installation:  detail.Installation,
		Health:        health,
		Namespaces:    detail.Namespaces,
		Providers:     detail.Providers,
		Capabilities:  detail.Capabilities,
		Compatibility: detail.Compatibility,
	}, nil
}

func (s Service) ListInstallationProviders(ctx context.Context, ref string) ([]InstalledProvider, error) {
	installationID, err := resolveInstallationID(ctx, s.DB, ref)
	if err != nil {
		return nil, err
	}
	return listInstalledProviders(ctx, s.DB, installationID)
}

func (s Service) ListInstallationCapabilities(ctx context.Context, ref string) ([]InstalledCapability, error) {
	installationID, err := resolveInstallationID(ctx, s.DB, ref)
	if err != nil {
		return nil, err
	}
	return listInstalledCapabilities(ctx, s.DB, installationID)
}

func (s Service) deriveHealth(ctx context.Context, detail ModuleInstallationDetail) (ModuleHealthSnapshot, error) {
	warnings := []string{}
	missing := []string{}
	status := ModuleHealthStatusOK
	nodeStatus := ""
	if err := s.DB.QueryRowContext(ctx, `SELECT status FROM nodes.nodes WHERE node_id = $1`, detail.Installation.TargetNodeID).Scan(&nodeStatus); err != nil {
		return ModuleHealthSnapshot{}, err
	}
	if nodeStatus != "active" {
		status = ModuleHealthStatusBlocked
		missing = append(missing, "target node is not active")
	}
	if detail.Installation.Status == InstallationStatusFailed || detail.Installation.Status == InstallationStatusRemoved {
		status = ModuleHealthStatusBlocked
		missing = append(missing, "installation status is "+detail.Installation.Status)
	}
	if len(detail.Providers) == 0 {
		status = ModuleHealthStatusDegraded
		warnings = append(warnings, "no provider rows linked")
	}
	if len(detail.Capabilities) == 0 {
		status = ModuleHealthStatusDegraded
		warnings = append(warnings, "no capability endpoint rows linked")
	}
	if len(detail.Namespaces) == 0 {
		status = ModuleHealthStatusDegraded
		warnings = append(warnings, "no namespaces reserved")
	}
	for _, provider := range detail.Providers {
		if provider.HealthStatus == capabilities.HealthStatusUnknown {
			warnings = append(warnings, "provider health is unknown: "+provider.CompactAddress)
			if status == ModuleHealthStatusOK {
				status = ModuleHealthStatusDegraded
			}
		}
	}
	if len(detail.Compatibility.MissingRequirements) > 0 {
		status = ModuleHealthStatusBlocked
		missing = append(missing, detail.Compatibility.MissingRequirements...)
	}
	if len(detail.Compatibility.Warnings) > 0 {
		warnings = append(warnings, detail.Compatibility.Warnings...)
		if status == ModuleHealthStatusOK {
			status = ModuleHealthStatusDegraded
		}
	}
	sort.Strings(missing)
	sort.Strings(warnings)
	missingJSON, _ := json.Marshal(missing)
	warningsJSON, _ := json.Marshal(warnings)
	detailsJSON, _ := json.Marshal(map[string]any{
		"provider_addresses":   providerAddresses(detail.Providers),
		"capability_addresses": capabilityAddresses(detail.Capabilities),
		"namespace_values":     namespaceValues(detail.Namespaces),
	})

	var health ModuleHealthSnapshot
	err := s.DB.QueryRowContext(ctx, `
		INSERT INTO modules.health_snapshots (
			module_health_id, module_installation_id, health_status, installation_status,
			target_node_status, provider_count, capability_count, namespace_count,
			missing_requirements, warnings, details_json, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, '{}'::jsonb)
		RETURNING module_health_id, module_installation_id, health_status, installation_status,
		          target_node_status, provider_count, capability_count, namespace_count,
		          missing_requirements, warnings, details_json, checked_at, metadata
	`,
		ids.NewModuleHealthID(),
		detail.Installation.ModuleInstallationID,
		status,
		detail.Installation.Status,
		nodeStatus,
		len(detail.Providers),
		len(detail.Capabilities),
		len(detail.Namespaces),
		missingJSON,
		warningsJSON,
		detailsJSON,
	).Scan(
		&health.ModuleHealthID,
		&health.ModuleInstallationID,
		&health.HealthStatus,
		&health.InstallationStatus,
		&health.TargetNodeStatus,
		&health.ProviderCount,
		&health.CapabilityCount,
		&health.NamespaceCount,
		&health.MissingRequirements,
		&health.Warnings,
		&health.DetailsJSON,
		&health.CheckedAt,
		&health.Metadata,
	)
	if err != nil {
		return ModuleHealthSnapshot{}, err
	}
	health.MissingRequirements = jsonRaw(health.MissingRequirements)
	health.Warnings = jsonRaw(health.Warnings)
	health.DetailsJSON = jsonRaw(health.DetailsJSON)
	health.Metadata = jsonRaw(health.Metadata)
	return health, nil
}

func installationColumns() string {
	return `module_installation_id, module_version_id, target_node_id, install_scope_id,
	        installed_by_actor_id, status, namespace_root, filesystem_path,
	        object_store_prefix, database_name, compatibility_report, failure_reason,
	        installed_at, enabled_at, disabled_at, created_at, updated_at, metadata`
}

func namespaceColumns() string {
	return `module_namespace_id, module_installation_id, module_version_id,
	        namespace_kind, namespace_key, namespace_value, status, metadata,
	        created_at, updated_at`
}

func healthSnapshotColumns() string {
	return `module_health_id, module_installation_id, health_status, installation_status,
	        target_node_status, provider_count, capability_count, namespace_count,
	        missing_requirements, warnings, details_json, checked_at, metadata`
}

func scanInstallation(s scanner) (ModuleInstallation, error) {
	var item ModuleInstallation
	var compatibilityReport, metadata []byte
	var installedAt, enabledAt, disabledAt sql.NullTime
	if err := s.Scan(
		&item.ModuleInstallationID,
		&item.ModuleVersionID,
		&item.TargetNodeID,
		&item.InstallScopeID,
		&item.InstalledByActorID,
		&item.Status,
		&item.NamespaceRoot,
		&item.FilesystemPath,
		&item.ObjectStorePrefix,
		&item.DatabaseName,
		&compatibilityReport,
		&item.FailureReason,
		&installedAt,
		&enabledAt,
		&disabledAt,
		&item.CreatedAt,
		&item.UpdatedAt,
		&metadata,
	); err != nil {
		return ModuleInstallation{}, err
	}
	item.CompatibilityReport = jsonRaw(compatibilityReport)
	item.Metadata = jsonRaw(metadata)
	item.InstalledAt = nullTimePtr(installedAt)
	item.EnabledAt = nullTimePtr(enabledAt)
	item.DisabledAt = nullTimePtr(disabledAt)
	return item, nil
}

func scanNamespace(s scanner) (ModuleNamespace, error) {
	var item ModuleNamespace
	var metadata []byte
	if err := s.Scan(
		&item.ModuleNamespaceID,
		&item.ModuleInstallationID,
		&item.ModuleVersionID,
		&item.NamespaceKind,
		&item.NamespaceKey,
		&item.NamespaceValue,
		&item.Status,
		&metadata,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return ModuleNamespace{}, err
	}
	item.Metadata = jsonRaw(metadata)
	return item, nil
}

func scanInstalledProvider(s scanner) (InstalledProvider, error) {
	var item InstalledProvider
	var metadata []byte
	if err := s.Scan(
		&item.ModuleInstallationID,
		&item.ModuleDeclarationID,
		&item.ProviderID,
		&item.ProviderKey,
		&item.CompactAddress,
		&item.DisplayName,
		&item.Description,
		&item.ProviderType,
		&item.Status,
		&item.ExposureStatus,
		&item.HealthStatus,
		&item.AvailabilityStatus,
		&metadata,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return InstalledProvider{}, err
	}
	item.Metadata = jsonRaw(metadata)
	return item, nil
}

func scanInstalledCapability(s scanner) (InstalledCapability, error) {
	var item InstalledCapability
	var usageDocumentIDs, metadata []byte
	if err := s.Scan(
		&item.ModuleInstallationID,
		&item.ModuleDeclarationID,
		&item.CapabilityClassID,
		&item.CapabilityEndpointID,
		&item.CapabilityEndpointVersionID,
		&item.EndpointName,
		&item.CompactAddress,
		&item.ClassName,
		&item.DisplayName,
		&item.Description,
		&item.Form,
		&item.RiskLevel,
		&item.ExecutionAuthorizationLevel,
		&item.Status,
		&item.VersionStatus,
		&item.ExposureStatus,
		&usageDocumentIDs,
		&metadata,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return InstalledCapability{}, err
	}
	item.UsageDocumentIDs = jsonRaw(usageDocumentIDs)
	item.Metadata = jsonRaw(metadata)
	return item, nil
}

func scanHealthSnapshot(s scanner) (ModuleHealthSnapshot, error) {
	var item ModuleHealthSnapshot
	var missingRequirements, warnings, details, metadata []byte
	if err := s.Scan(
		&item.ModuleHealthID,
		&item.ModuleInstallationID,
		&item.HealthStatus,
		&item.InstallationStatus,
		&item.TargetNodeStatus,
		&item.ProviderCount,
		&item.CapabilityCount,
		&item.NamespaceCount,
		&missingRequirements,
		&warnings,
		&details,
		&item.CheckedAt,
		&metadata,
	); err != nil {
		return ModuleHealthSnapshot{}, err
	}
	item.MissingRequirements = jsonRaw(missingRequirements)
	item.Warnings = jsonRaw(warnings)
	item.DetailsJSON = jsonRaw(details)
	item.Metadata = jsonRaw(metadata)
	return item, nil
}

func getInstallation(ctx context.Context, q queryer, ref string) (ModuleInstallation, error) {
	row := q.QueryRowContext(ctx, `SELECT `+installationColumns()+` FROM modules.installations WHERE module_installation_id = $1`, ref)
	return scanInstallation(row)
}

func getInstallationByUniqueTarget(ctx context.Context, q queryer, moduleVersionID, targetNodeID, installScopeID string) (ModuleInstallation, error) {
	row := q.QueryRowContext(ctx, `SELECT `+installationColumns()+`
		FROM modules.installations
		WHERE module_version_id = $1
		  AND target_node_id = $2
		  AND install_scope_id = $3
	`, moduleVersionID, targetNodeID, installScopeID)
	return scanInstallation(row)
}

func resolveInstallationID(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", sql.ErrNoRows
	}
	var installationID string
	err := q.QueryRowContext(ctx, `
		SELECT i.module_installation_id
		FROM modules.installations i
		JOIN modules.module_versions mv ON mv.module_version_id = i.module_version_id
		WHERE i.module_installation_id = $1
		   OR i.module_version_id = $1
		   OR mv.module_id = $1
		ORDER BY i.created_at DESC, i.module_installation_id DESC
		LIMIT 1
	`, ref).Scan(&installationID)
	return installationID, err
}

func listNamespaces(ctx context.Context, q queryer, installationID string) ([]ModuleNamespace, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+namespaceColumns()+`
		FROM modules.namespaces
		WHERE module_installation_id = $1
		ORDER BY namespace_kind, namespace_key
	`, installationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ModuleNamespace{}
	for rows.Next() {
		item, err := scanNamespace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func listInstalledProviders(ctx context.Context, q queryer, installationID string) ([]InstalledProvider, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT ip.module_installation_id, ip.module_declaration_id, p.provider_id,
		       p.provider_key, p.compact_address, p.display_name, p.description,
		       p.provider_type, p.status, ip.exposure_status,
		       coalesce(ph.health_status, 'unknown'), coalesce(ph.availability_status, 'unknown'),
		       p.metadata, p.created_at, p.updated_at
		FROM modules.installation_providers ip
		JOIN capabilities.providers p ON p.provider_id = ip.provider_id
		LEFT JOIN capabilities.provider_health ph ON ph.provider_id = p.provider_id
		WHERE ip.module_installation_id = $1
		ORDER BY p.provider_key
	`, installationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InstalledProvider{}
	for rows.Next() {
		item, err := scanInstalledProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func listInstalledCapabilities(ctx context.Context, q queryer, installationID string) ([]InstalledCapability, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT ic.module_installation_id, ic.module_declaration_id, c.capability_class_id,
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
		WHERE ic.module_installation_id = $1
		ORDER BY e.compact_address
	`, installationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InstalledCapability{}
	for rows.Next() {
		item, err := scanInstalledCapability(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func getLatestHealth(ctx context.Context, q queryer, installationID string) (ModuleHealthSnapshot, bool, error) {
	row := q.QueryRowContext(ctx, `SELECT `+healthSnapshotColumns()+`
		FROM modules.health_snapshots
		WHERE module_installation_id = $1
		ORDER BY checked_at DESC, module_health_id DESC
		LIMIT 1
	`, installationID)
	item, err := scanHealthSnapshot(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ModuleHealthSnapshot{}, false, nil
	}
	if err != nil {
		return ModuleHealthSnapshot{}, false, err
	}
	return item, true, nil
}

func nullTimePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}

func insertInstallationTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, installationID string, version ModuleVersion, report CompatibilityReport, namespaceRoot string, paths modulePaths, reportJSON, metadata json.RawMessage) (ModuleInstallation, error) {
	row := tx.QueryRowContext(ctx, `
		INSERT INTO modules.installations (
			module_installation_id, module_version_id, target_node_id, install_scope_id,
			installed_by_actor_id, status, namespace_root, filesystem_path,
			object_store_prefix, database_name, compatibility_report, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING `+installationColumns(),
		installationID,
		version.ModuleVersionID,
		report.TargetNodeID,
		report.InstallScopeID,
		req.ActorID,
		InstallationStatusInstalling,
		namespaceRoot,
		paths.FilesystemPath,
		paths.ObjectStorePrefix,
		paths.DatabaseName,
		reportJSON,
		objectOrDefault(metadata),
	)
	return scanInstallation(row)
}

func reserveNamespacesTx(ctx context.Context, tx *sql.Tx, installation ModuleInstallation, manifest Manifest, paths modulePaths) error {
	items := []struct {
		kind  string
		key   string
		value string
	}{
		{NamespaceKindModule, "module", installation.NamespaceRoot},
		{NamespaceKindFilesystem, "filesystem", paths.FilesystemPath},
		{NamespaceKindObjectStore, "object_store", paths.ObjectStorePrefix},
		{NamespaceKindEventPrefix, "event_prefix", manifest.Module.ID + ".*"},
	}
	if paths.DatabaseName != "" {
		items = append(items, struct {
			kind  string
			key   string
			value string
		}{NamespaceKindDatabase, "database", paths.DatabaseName})
	}
	for _, provider := range manifest.Provides.Providers {
		items = append(items, struct {
			kind  string
			key   string
			value string
		}{NamespaceKindProvider, provider.ProviderKey, "main@" + provider.ProviderKey})
	}
	for _, capability := range manifest.Provides.Capabilities {
		items = append(items, struct {
			kind  string
			key   string
			value string
		}{NamespaceKindCapability, capability.ProviderKey + "." + capability.EndpointName, "main@" + capability.ProviderKey + "." + capability.EndpointName})
	}
	for _, obj := range manifest.Provides.ObjectTypes {
		items = append(items, struct {
			kind  string
			key   string
			value string
		}{NamespaceKindObjectType, obj.ObjectType, manifest.Module.ID + "." + obj.ObjectType})
	}
	for _, item := range items {
		if strings.TrimSpace(item.value) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO modules.namespaces (
				module_namespace_id, module_installation_id, module_version_id,
				namespace_kind, namespace_key, namespace_value, status, metadata
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, '{}'::jsonb)
		`,
			ids.NewModuleNamespaceID(),
			installation.ModuleInstallationID,
			installation.ModuleVersionID,
			item.kind,
			item.key,
			item.value,
			NamespaceStatusReserved,
		); err != nil {
			return err
		}
	}
	return nil
}

func installDeclaredProvidersTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, installation ModuleInstallation, detail ModuleVersionInspection) (map[string]string, error) {
	providerIDs := map[string]string{}
	for _, decl := range detail.Providers {
		providerID := ids.NewProviderID()
		compactAddress := "main@" + decl.ProviderKey
		metadata, _ := json.Marshal(map[string]any{
			"module_installation_id": installation.ModuleInstallationID,
			"module_version_id":      installation.ModuleVersionID,
			"module_declaration_id":  decl.ModuleDeclarationID,
			"module_owned":           true,
			"exposure":               "installed_disabled",
		})
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO capabilities.providers (
				provider_id, provider_key, compact_address, display_name, description,
				provider_type, node_id, scope_id, version, status, runtime_profile_json,
				documentation_refs_json, package_ref, created_by_actor_id, metadata
			)
			VALUES ($1, $2, $3, $4, $5, 'module', $6, $7, $8, 'disabled', $9, '[]'::jsonb, $10, $11, $12)
		`,
			providerID,
			decl.ProviderKey,
			compactAddress,
			decl.DisplayName,
			decl.Description,
			installation.TargetNodeID,
			installation.InstallScopeID,
			detail.Version.Version,
			decl.RuntimeRequirements,
			installation.ModuleInstallationID,
			req.ActorID,
			metadata,
		); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO capabilities.provider_health (
				provider_id, health_status, availability_status, last_checked_at, message, details_json
			)
			VALUES ($1, 'unknown', 'unavailable', now(), 'module installed but not enabled/exposed', $2)
		`, providerID, decl.HealthCheckSpec); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO modules.installation_providers (
				module_installation_id, module_declaration_id, provider_id, exposure_status, metadata
			)
			VALUES ($1, $2, $3, $4, '{}'::jsonb)
		`, installation.ModuleInstallationID, decl.ModuleDeclarationID, providerID, ExposureStatusInstalledDisabled); err != nil {
			return nil, err
		}
		providerIDs[decl.ProviderKey] = providerID
	}
	return providerIDs, nil
}

func installDeclaredCapabilitiesTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, installation ModuleInstallation, detail ModuleVersionInspection, providerIDs map[string]string) (map[string]string, error) {
	capabilityIDs := map[string]string{}
	for _, decl := range detail.Capabilities {
		providerID := providerIDs[decl.ProviderKey]
		if providerID == "" {
			return nil, fmt.Errorf("capability declaration provider is not installed: %s", decl.ProviderKey)
		}
		classID := ids.NewCapabilityClassID()
		endpointID := ids.NewCapabilityEndpointID()
		endpointVersionID := ids.NewCapabilityEndpointVersionID()
		classMetadata, _ := json.Marshal(map[string]any{
			"module_installation_id": installation.ModuleInstallationID,
			"module_declaration_id":  decl.ModuleDeclarationID,
			"module_owned":           true,
		})
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO capabilities.capability_classes (
				capability_class_id, namespace, name, version, display_name,
				description, form, input_schema_json, output_schema_json,
				default_risk_level, default_policy_requirements_json, status, metadata
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'active', $12)
		`,
			classID,
			decl.CapabilityClassNamespace,
			decl.CapabilityClassName,
			detail.Version.Version,
			decl.DisplayName,
			decl.Description,
			decl.Form,
			decl.InputSchemaJSON,
			decl.OutputSchemaJSON,
			decl.RiskLevel,
			decl.PolicyRequirementsJSON,
			classMetadata,
		); err != nil {
			return nil, err
		}
		endpointMetadata, _ := json.Marshal(map[string]any{
			"module_installation_id": installation.ModuleInstallationID,
			"module_declaration_id":  decl.ModuleDeclarationID,
			"module_owned":           true,
			"exposure":               "installed_disabled",
		})
		compactAddress := "main@" + decl.ProviderKey + "." + decl.EndpointName
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO capabilities.capability_endpoints (
				capability_endpoint_id, provider_id, capability_class_id, endpoint_name,
				compact_address, form, input_schema_json, output_schema_json, risk_level,
				execution_authorization_level, side_effects_json, policy_requirements_json,
				credential_requirements_json, approval_requirements_json, job_behavior_json,
				session_behavior_json, stream_behavior_json, lease_behavior_json, status, metadata
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, '{}'::jsonb, '{}'::jsonb, '{}'::jsonb, '{}'::jsonb, '{}'::jsonb, 'disabled', $14)
		`,
			endpointID,
			providerID,
			classID,
			decl.EndpointName,
			compactAddress,
			decl.Form,
			decl.InputSchemaJSON,
			decl.OutputSchemaJSON,
			decl.RiskLevel,
			decl.ExecutionAuthorizationLevel,
			decl.SideEffectsJSON,
			decl.PolicyRequirementsJSON,
			decl.CredentialRequirementsJSON,
			endpointMetadata,
		); err != nil {
			return nil, err
		}
		versionManifest, _ := json.Marshal(map[string]any{
			"module_id":              detail.Version.ModuleID,
			"module_version_id":      detail.Version.ModuleVersionID,
			"module_installation_id": installation.ModuleInstallationID,
			"module_declaration_id":  decl.ModuleDeclarationID,
			"manifest_hash":          detail.Version.ManifestHash,
		})
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO capabilities.endpoint_versions (
				capability_endpoint_version_id, capability_endpoint_id, version_label,
				implementation_hash, manifest_json, input_schema_json, output_schema_json,
				risk_level, execution_authorization_level, policy_requirements_json,
				credential_requirements_json, approval_requirements_json, status, metadata
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, '{}'::jsonb, 'disabled', $12)
		`,
			endpointVersionID,
			endpointID,
			detail.Version.Version,
			detail.Version.ManifestHash,
			versionManifest,
			decl.InputSchemaJSON,
			decl.OutputSchemaJSON,
			decl.RiskLevel,
			decl.ExecutionAuthorizationLevel,
			decl.PolicyRequirementsJSON,
			decl.CredentialRequirementsJSON,
			endpointMetadata,
		); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO modules.installation_capabilities (
				module_installation_id, module_declaration_id, capability_class_id,
				capability_endpoint_id, capability_endpoint_version_id, exposure_status,
				usage_document_ids, metadata
			)
			VALUES ($1, $2, $3, $4, $5, $6, '[]'::jsonb, '{}'::jsonb)
		`, installation.ModuleInstallationID, decl.ModuleDeclarationID, classID, endpointID, endpointVersionID, ExposureStatusInstalledDisabled); err != nil {
			return nil, err
		}
		capabilityIDs[decl.ProviderKey+"."+decl.EndpointName] = endpointID
		capabilityIDs[decl.EndpointName] = endpointID
		_ = req
	}
	return capabilityIDs, nil
}

func installUsageDocumentsTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, installation ModuleInstallation, detail ModuleVersionInspection, capabilityIDs map[string]string) error {
	for _, doc := range detail.UsageDocuments {
		if doc.TargetKind != DeclarationTargetCapability {
			continue
		}
		endpointID := capabilityIDs[doc.TargetRef]
		if endpointID == "" {
			return fmt.Errorf("usage document target was not installed: %s", doc.TargetRef)
		}
		pkg, err := getModulePackage(ctx, tx, detail.Version.ModulePackageID)
		if err != nil {
			return err
		}
		body, err := readFileWithinRoot(pkg.SourceURI, doc.Path)
		if err != nil {
			return err
		}
		bodyFormat := doc.BodyFormat
		if bodyFormat != capabilities.UsageDocumentFormatMarkdown {
			bodyFormat = capabilities.UsageDocumentFormatMarkdown
		}
		usageDocID := ids.NewCapabilityUsageDocumentID()
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO capabilities.usage_documents (
				capability_usage_document_id, target_kind, target_id, target_address,
				title, version_label, body_format, body, section_map_json,
				visibility_policy_json, review_status, content_hash, source_kind,
				source_ref, created_by_actor_id, metadata
			)
			VALUES ($1, 'capability_endpoint', $2, null, $3, $4, $5, $6, $7, $8, $9, $10, 'manifest', $11, $12, $13)
		`,
			usageDocID,
			endpointID,
			doc.Title,
			detail.Version.Version,
			bodyFormat,
			string(body),
			doc.SectionMapJSON,
			doc.VisibilityPolicyJSON,
			capabilities.UsageReviewStatusPendingReview,
			doc.ContentHash,
			doc.ModuleDeclarationID,
			req.ActorID,
			doc.Metadata,
		); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE modules.installation_capabilities
			SET usage_document_ids = usage_document_ids || to_jsonb($1::text),
			    updated_at = now()
			WHERE module_installation_id = $2
			  AND capability_endpoint_id = $3
		`, usageDocID, installation.ModuleInstallationID, endpointID); err != nil {
			return err
		}
	}
	return nil
}

func manifestFromVersion(version ModuleVersion) (Manifest, error) {
	var manifest Manifest
	if err := json.Unmarshal(version.ManifestJSON, &manifest); err != nil {
		return Manifest{}, err
	}
	return normalizeManifest(manifest), nil
}

type modulePaths struct {
	FilesystemPath    string
	ObjectStorePrefix string
	DatabaseName      string
}

func installationPaths(dataDir, moduleID, installationID string, needsDatabase bool) modulePaths {
	safeModuleID := strings.ReplaceAll(moduleID, ".", "/")
	short := shortInstallationID(installationID)
	paths := modulePaths{
		FilesystemPath:    filepath.Join(defaultString(dataDir, "/var/lib/loom"), "modules", safeModuleID, installationID),
		ObjectStorePrefix: "module/" + moduleID + "/" + installationID + "/",
	}
	if needsDatabase {
		paths.DatabaseName = "loom_mod_" + strings.ReplaceAll(strings.TrimPrefix(moduleID, "loom."), "-", "_") + "_" + short
		paths.DatabaseName = strings.ReplaceAll(paths.DatabaseName, ".", "_")
	}
	return paths
}

func namespaceRoot(moduleID, installationID string) string {
	return moduleID + ".i" + shortInstallationID(installationID)
}

func shortInstallationID(id string) string {
	parts := strings.Split(id, "_")
	last := parts[len(parts)-1]
	if len(last) > 10 {
		last = last[len(last)-10:]
	}
	return strings.ToLower(last)
}

func compatibleWithCurrent(min string) bool {
	current := coreversion.Current().Version
	return min == "" || min == current || min == "0.0.0-dev"
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func moduleExists(ctx context.Context, q queryer, moduleID string) bool {
	var exists bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM modules.module_versions WHERE module_id = $1 AND status = 'valid')`, moduleID).Scan(&exists)
	return err == nil && exists
}

func requireActorAdminOnNode(ctx context.Context, q queryer, actorID, nodeID string) error {
	var allowed bool
	if err := q.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM identity.actor_node_authorizations
			WHERE actor_id = $1
			  AND node_id = $2
			  AND status = 'active'
			  AND authorization_level >= 5
		)
	`, actorID, nodeID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return fmt.Errorf("actor is not authorized to install modules on target node")
	}
	return nil
}

func resolveNode(ctx context.Context, q queryer, ref string) (nodeRecord, error) {
	var node nodeRecord
	err := q.QueryRowContext(ctx, `
		SELECT node_id, node_key, node_role, status
		FROM nodes.nodes
		WHERE node_id = $1 OR node_key = $1
	`, strings.TrimSpace(ref)).Scan(&node.ID, &node.Key, &node.Role, &node.Status)
	return node, err
}

func resolveScope(ctx context.Context, q queryer, ref string) (scopeRecord, error) {
	var scope scopeRecord
	err := q.QueryRowContext(ctx, `
		SELECT scope_id, scope_key, slug
		FROM scopes.scopes
		WHERE scope_id = $1 OR scope_key = $1 OR slug = $1
	`, strings.TrimSpace(ref)).Scan(&scope.ID, &scope.Key, &scope.Slug)
	return scope, err
}

func providerAddresses(providers []InstalledProvider) []string {
	out := make([]string, 0, len(providers))
	for _, provider := range providers {
		out = append(out, provider.CompactAddress)
	}
	sort.Strings(out)
	return out
}

func capabilityAddresses(capabilityList []InstalledCapability) []string {
	out := make([]string, 0, len(capabilityList))
	for _, capability := range capabilityList {
		out = append(out, capability.CompactAddress)
	}
	sort.Strings(out)
	return out
}

func namespaceValues(namespaces []ModuleNamespace) []string {
	out := make([]string, 0, len(namespaces))
	for _, namespace := range namespaces {
		out = append(out, namespace.NamespaceValue)
	}
	sort.Strings(out)
	return out
}
