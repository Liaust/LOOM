package update

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

func MaintenanceStatusFromManifest(manifest UpdateManifest) *MaintenanceWindow {
	return manifest.Maintenance
}

func ResumeMaintenance(ctx context.Context, input ResumeMaintenanceInput) (ResumeMaintenanceResult, error) {
	manifestPath := strings.TrimSpace(input.ManifestPath)
	stateDir := strings.TrimSpace(input.StateDir)
	if stateDir == "" {
		stateDir = DefaultStateDir("")
	}
	if manifestPath == "" {
		manifestPath = ActiveManifestPath(stateDir)
	}
	manifest, err := ReadUpdateManifest(manifestPath)
	if err != nil {
		return ResumeMaintenanceResult{}, err
	}
	result := ResumeMaintenanceResult{
		Status:       firstNonEmpty(maintenanceWindowStatus(manifest.Maintenance), "none"),
		UpdateID:     manifest.UpdateID,
		ManifestPath: manifestPath,
		Manifest:     &manifest,
		Window:       manifest.Maintenance,
		Changed:      []UpdateChange{},
	}
	if !input.Yes {
		result.Refused = true
		result.Refusal = "update maintenance resume requires --yes"
		result.Status = "refused"
		return result, nil
	}
	if !input.AllowNonProduction && !isProductionMain(input.Runtime) {
		result.Refused = true
		result.Refusal = "update maintenance resume requires the production main runtime; pass --allow-non-production only for dev drills"
		result.Status = "refused"
		return result, nil
	}
	if manifest.Maintenance == nil {
		result.Status = MaintenanceWindowStatusSkipped
		result.Changed = append(result.Changed, UpdateChange{Step: "maintenance_resume", Status: MaintenanceWindowStatusSkipped, Message: "No maintenance window is recorded."})
		return result, nil
	}
	if !maintenanceWindowNeedsResume(manifest.Maintenance) {
		result.Status = manifest.Maintenance.Status
		result.Changed = append(result.Changed, UpdateChange{Step: "maintenance_resume", Status: result.Status, Message: "No resources require resume."})
		return result, nil
	}
	coordinator := input.Maintenance
	if coordinator == nil {
		return result, fmt.Errorf("maintenance coordinator is required to resume recorded resources")
	}
	resumed, err := coordinator.Resume(ctx, MaintenanceResumeInput{
		UpdateID: manifest.UpdateID,
		Window:   *manifest.Maintenance,
		Now:      input.Now,
	})
	manifest.Maintenance = &resumed
	result.Status = resumed.Status
	result.Window = &resumed
	result.Manifest = &manifest
	result.Changed = append(result.Changed, UpdateChange{Step: "maintenance_resume", Status: resumed.Status, Message: maintenanceWindowChangeMessage(resumed)})
	if err != nil {
		markMaintenanceResumeRequired(manifest.Maintenance)
		manifest.Diagnostics = append(manifest.Diagnostics, UpdateDiagnostic{
			Severity: DiagnosticWarning,
			Code:     "update.maintenance_resume_required",
			Message:  err.Error(),
		})
		result.Status = MaintenanceWindowStatusResumeRequired
	}
	paths := manifestPaths(stateDirFromMaintenanceInput(stateDir, manifestPath), manifest.UpdateID)
	result.ManifestPath = paths.active
	result.HistoryPath = paths.history
	if writeErr := writeAllUpdateManifests(paths, manifest, true); writeErr != nil {
		return result, writeErr
	}
	return result, err
}

func maintenanceWindowStatus(window *MaintenanceWindow) string {
	if window == nil {
		return ""
	}
	return strings.TrimSpace(window.Status)
}

func stateDirFromMaintenanceInput(stateDir, manifestPath string) string {
	if strings.TrimSpace(stateDir) != "" {
		return strings.TrimSpace(stateDir)
	}
	manifestPath = filepath.Clean(strings.TrimSpace(manifestPath))
	if strings.TrimSpace(manifestPath) == "" {
		return DefaultStateDir("")
	}
	return filepath.Dir(manifestPath)
}
