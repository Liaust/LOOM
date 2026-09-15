package httpapi

import (
	"errors"
	"net/http"

	"loom.local/loom/internal/projects"
)

func (s Server) writeProjectRuntimeArchivedError(w http.ResponseWriter, correlationID, domain, target string, err error) bool {
	code, message := projectRuntimeGuardError(err)
	if code == "" {
		return false
	}
	s.writeError(w, correlationID, http.StatusConflict, code, domain, target, message, nil)
	return true
}

func projectRuntimeGuardError(err error) (string, string) {
	switch {
	case errors.Is(err, projects.ErrProjectRestoreBlocked):
		var restore projects.ProjectRestoreBlockedError
		if errors.As(err, &restore) && restore.Phase == projects.ProjectRestorePhaseComplete {
			return "project_runtime.restored_inactive", "Project custody is restored but runtime remains inactive. Activation and fence release require a separate supported operation."
		}
		return "project_runtime.restore_in_progress", "Project restore is incomplete or uncertain. Inspect and recover the exact operation; runtime remains blocked."
	case projects.IsProjectArchiveInProgress(err):
		return "project_runtime.archive_in_progress", "Project archive evidence is incomplete or invalid. Inspect and recover the exact operation before changing project runtime."
	case projects.IsProjectRuntimeArchived(err):
		return "project_runtime.archived", "Project is archived; restore it before reviewing a separate runtime activation."
	default:
		return "", ""
	}
}
