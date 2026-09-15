package modules

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/routing"
)

const projectCockpitStatusCapability = "main@loom-project-cockpit.status.read"

type ProjectCockpitAdapter struct {
	DB *sql.DB
}

func NewProjectCockpitAdapter(db *sql.DB) ProjectCockpitAdapter {
	return ProjectCockpitAdapter{DB: db}
}

func (a ProjectCockpitAdapter) Execute(ctx context.Context, execCtx routing.ExecutionContext, input json.RawMessage) (routing.ExecutionResult, error) {
	operation := strings.TrimPrefix(strings.TrimSpace(execCtx.Operation), "capability:")
	if operation != projectCockpitStatusCapability {
		return routing.ExecutionResult{}, fmt.Errorf("unsupported project cockpit operation: %s", execCtx.Operation)
	}
	if a.DB == nil {
		return routing.ExecutionResult{}, fmt.Errorf("project cockpit adapter has no database")
	}

	var request struct {
		ProjectRef string `json:"project_ref,omitempty"`
	}
	if len(strings.TrimSpace(string(input))) > 0 && string(input) != "null" {
		if err := json.Unmarshal(input, &request); err != nil {
			return routing.ExecutionResult{}, fmt.Errorf("project cockpit input must be JSON object: %w", err)
		}
	}

	var installationID, moduleID, version, installationStatus, providerStatus, endpointStatus, providerExposure, capabilityExposure string
	err := a.DB.QueryRowContext(ctx, `
		SELECT i.module_installation_id, mv.module_id, mv.version, i.status,
		       p.status, e.status, ip.exposure_status, ic.exposure_status
		FROM modules.installation_capabilities ic
		JOIN modules.installations i ON i.module_installation_id = ic.module_installation_id
		JOIN modules.module_versions mv ON mv.module_version_id = i.module_version_id
		JOIN capabilities.capability_endpoints e ON e.capability_endpoint_id = ic.capability_endpoint_id
		JOIN capabilities.providers p ON p.provider_id = e.provider_id
		JOIN modules.installation_providers ip ON ip.module_installation_id = i.module_installation_id
			AND ip.provider_id = p.provider_id
		WHERE e.capability_endpoint_id = $1
		  AND e.compact_address = $2
		  AND ic.exposure_status = $3
	`, execCtx.CapabilityEndpointID, projectCockpitStatusCapability, ExposureStatusExposed).Scan(
		&installationID,
		&moduleID,
		&version,
		&installationStatus,
		&providerStatus,
		&endpointStatus,
		&providerExposure,
		&capabilityExposure,
	)
	if err != nil {
		return routing.ExecutionResult{}, fmt.Errorf("project cockpit installation is not exposed: %w", err)
	}

	result, err := json.Marshal(map[string]any{
		"module_id":                  moduleID,
		"installation_id":            installationID,
		"version":                    version,
		"status":                     installationStatus,
		"provider_status":            providerStatus,
		"endpoint_status":            endpointStatus,
		"provider_exposure_status":   providerExposure,
		"capability_exposure_status": capabilityExposure,
		"requested_project_ref":      strings.TrimSpace(request.ProjectRef),
		"checked_at":                 time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return routing.ExecutionResult{}, err
	}
	return routing.ExecutionResult{
		Status:     routing.CapabilityCallStatusCompleted,
		Result:     result,
		ResultRefs: json.RawMessage(`{}`),
	}, nil
}
