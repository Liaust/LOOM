package update

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type StatusInput struct {
	StateDir string
	Limit    int
	Now      func() time.Time
}

func Status(input StatusInput) UpdateStatus {
	stateDir := strings.TrimSpace(input.StateDir)
	if stateDir == "" {
		stateDir = DefaultStateDir("")
	}
	stateDir = filepath.Clean(stateDir)
	activePath := ActiveManifestPath(stateDir)
	now := currentTime(input.Now)
	status := UpdateStatus{
		CheckedAt:          now,
		StateDir:           stateDir,
		ActiveManifestPath: activePath,
		History:            []UpdateManifest{},
		Diagnostics:        []UpdateDiagnostic{},
	}

	active, err := ReadUpdateManifest(activePath)
	if err == nil {
		status.ActiveExists = true
		status.Active = &active
		raw, readErr := os.ReadFile(activePath)
		if readErr == nil && operationIDPattern.MatchString(active.UpdateID) {
			entries, _ := os.ReadDir(filepath.Join(stateDir, "reconciliations", active.UpdateID))
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasPrefix(entry.Name(), "ready_") || !strings.HasSuffix(entry.Name(), ".json") {
					continue
				}
				record, readErr := readReconciliation(filepath.Join(stateDir, "reconciliations", active.UpdateID, entry.Name()))
				if readErr != nil {
					status.Diagnostics = append(status.Diagnostics, UpdateDiagnostic{Severity: DiagnosticWarning, Code: "update.reconciliation_invalid", Message: readErr.Error()})
					continue
				}
				if record.UpdateID == active.UpdateID && record.InitialManifestSHA256 == bytesSHA256(raw) {
					status.Reconciliations = append(status.Reconciliations, record)
				}
			}
		}
	} else if errors.Is(err, fs.ErrNotExist) || !pathExists(activePath) {
		status.Diagnostics = append(status.Diagnostics, UpdateDiagnostic{
			Severity: DiagnosticInfo,
			Code:     "update.active_manifest_missing",
			Message:  "No active update manifest exists yet.",
			Path:     activePath,
		})
	} else {
		status.ActiveExists = true
		status.Diagnostics = append(status.Diagnostics, UpdateDiagnostic{
			Severity: DiagnosticWarning,
			Code:     "update.active_manifest_invalid",
			Message:  err.Error(),
			Path:     activePath,
		})
	}

	history, diagnostics := ListHistory(stateDir, input.Limit)
	status.History = history
	status.Diagnostics = append(status.Diagnostics, diagnostics...)
	return status
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
