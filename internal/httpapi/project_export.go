package httpapi

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/storagearchive"
)

// The caller holds the custody-reader lease through archive creation and delivery.
// Historical registration paths never authorize an arbitrary Storage export.
func (s Server) archivedProjectExportRoot(ctx context.Context, detail projects.ProjectRegistrationDetail, inspection storagearchive.ProjectArchiveInspectResult, requested string) (string, error) {
	project := detail.Project.Project
	state, valid := projects.ParseProjectPhysicalArchiveState(project.ArchiveState)
	physical := inspection.Physical
	if !valid || project.Status != "archived" || state.ProjectID != project.ProjectID ||
		state.Phase != projects.ProjectArchivePhaseComplete || state.Status != "archived" ||
		inspection.Project.Project.Project.ProjectID != project.ProjectID || inspection.Project.Project.Project.Status != "archived" ||
		physical == nil || physical.ProjectID != project.ProjectID || physical.ArchiveOperationID != state.OperationID ||
		physical.OperationID != state.OperationID || physical.PlanDigest != state.PlanDigest ||
		physical.Status != "archived" || physical.EvidenceStatus != "verified" || physical.CurrentPayload == nil ||
		physical.CurrentPayload.Root != storagearchive.WorkspaceRootStorage || s.services.Files == nil {
		return "", fmt.Errorf("verified current archived project payload required")
	}
	payload := physical.CurrentPayload
	root, entry, err := s.services.Files.SourcePath(ctx, "storage", payload.RelativePath)
	if err != nil || entry.Kind != "directory" || root != payload.AbsolutePath {
		return "", fmt.Errorf("current archive payload does not match the configured Files owner")
	}
	if requested != "" && (strings.TrimSpace(requested) != root || filepath.Clean(requested) != root) {
		return "", fmt.Errorf("requested export path differs from the verified current archive payload")
	}
	return root, nil
}
