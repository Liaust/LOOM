package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"

	"loom.local/loom/internal/backupcoverage"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/response"
)

func (s Server) handleBackupCoverage(w http.ResponseWriter, r *http.Request) {
	correlationID, ctx := requestMeta(r)
	if r.Method != http.MethodGet {
		s.writeError(w, correlationID, http.StatusMethodNotAllowed, "method.not_allowed", "backup", r.URL.Path, "Method is not allowed.", nil)
		return
	}
	opts := backupcoverage.OptionsFromConfig(s.services.RuntimeConfig)
	opts.Mode = backupcoverage.ModeMainBacked
	manifestPath, manifestSHA256, err := s.latestTrustedProvenanceBackupEvidence(ctx)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "backup.coverage_failed", "backup", "coverage", "Could not read backup coverage evidence.", err)
		return
	}
	opts.ProvenanceManifestPath = manifestPath
	opts.TrustedProvenanceManifestSHA256 = manifestSHA256
	report, err := backupcoverage.Check(ctx, opts)
	if err != nil {
		s.writeError(w, correlationID, http.StatusInternalServerError, "backup.coverage_failed", "backup", "coverage", "Could not read backup coverage.", err)
		return
	}
	response.WriteJSON(w, http.StatusOK, response.Success(correlationID, report))
}

func (s Server) latestTrustedProvenanceBackupEvidence(ctx context.Context) (string, string, error) {
	if s.services.Maintenance.DB == nil {
		return "", "", nil
	}
	backups, err := s.services.Maintenance.ListBackups(ctx, maintenance.OperationFilter{Kind: maintenance.OperationKindMainBackup, Status: maintenance.OperationSucceeded, Limit: 100})
	if err != nil {
		return "", "", err
	}
	for _, backup := range backups {
		var result struct {
			SchemaVersion            string `json:"schema_version"`
			Phase                    string `json:"phase"`
			Status                   string `json:"status"`
			Committed                bool   `json:"committed"`
			ProvenanceManifestPath   string `json:"provenance_manifest_path"`
			ProvenanceManifestSHA256 string `json:"provenance_manifest_sha256"`
		}
		if json.Unmarshal(backup.Operation.ResultJSON, &result) != nil || result.SchemaVersion != "main_backup.result.v1" || result.Phase != "recovery_packages" || result.Status != maintenance.OperationSucceeded || !result.Committed {
			continue
		}
		manifestPath := strings.TrimSpace(result.ProvenanceManifestPath)
		if manifestPath == "" || filepath.Clean(manifestPath) != manifestPath || filepath.Base(manifestPath) != provenance.RecoveryManifestFile {
			return "", "", maintenance.ErrInvalid
		}
		verification, verifyErr := maintenance.VerifyProvenanceBackupPackage(ctx, filepath.Dir(manifestPath), result.ProvenanceManifestSHA256)
		if verifyErr != nil {
			return "", "", verifyErr
		}
		if verification.ManifestSHA256 != result.ProvenanceManifestSHA256 {
			return "", "", maintenance.ErrInvalid
		}
		return result.ProvenanceManifestPath, result.ProvenanceManifestSHA256, nil
	}
	return "", "", nil
}
