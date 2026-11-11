package backupstrategy

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// A coalesced post-commit wake hint, not authority to delete. The privileged
// owner still resolves fresh plans, live references and exact cloud evidence.
type LocalRetentionRequest struct {
	Schema         string    `json:"schema"`
	WorkerRunID    string    `json:"worker_run_id"`
	Archive        string    `json:"archive"`
	ManifestSHA256 string    `json:"manifest_sha256"`
	RequestedAt    time.Time `json:"requested_at"`
	WakeAt         time.Time `json:"wake_at"`
}

func RequestLocalRecoveryRetention(dataDir, runID, archive, manifestSHA string) error {
	if _, err := exactAbsolutePath(dataDir); err != nil || dataDir == "/" || runID == "" || len(runID) > 160 || validateArchiveIdentity(archive) != nil || normalizeSHA256(manifestSHA) == "" {
		return fmt.Errorf("exact completed cloud identity required for local retention request")
	}
	root := filepath.Join(dataDir, "backups")
	if previous, _, err := readLocalRetentionRequest(dataDir); err == nil && previous.WorkerRunID == runID && previous.Archive == archive && previous.ManifestSHA256 == manifestSHA {
		return nil
	}
	now := time.Now().UTC()
	return writeRecoveryRecord(root, "local-retention-request.json", LocalRetentionRequest{Schema: "loom.local_retention.request.v1", WorkerRunID: runID, Archive: archive, ManifestSHA256: manifestSHA, RequestedAt: now, WakeAt: now}, 0644)
}

func readLocalRetentionRequest(dataDir string) (LocalRetentionRequest, string, error) {
	var request LocalRetentionRequest
	root := filepath.Join(dataDir, "backups")
	if err := checkNotesRecoveryRecordCustody(root, "local-retention-request"); err != nil {
		return request, "", err
	}
	raw, err := notesRecoveryReadJSON(filepath.Join(root, "local-retention-request.json"), 16<<10, &request)
	if err != nil {
		return request, "", err
	}
	if request.Schema != "loom.local_retention.request.v1" || request.WorkerRunID == "" || request.RequestedAt.IsZero() || normalizeSHA256(request.ManifestSHA256) == "" || validateArchiveIdentity(request.Archive) != nil {
		return request, "", fmt.Errorf("invalid local retention request")
	}
	return request, notesRecoveryHash(raw), nil
}

func WakePendingLocalRecoveryRetention(dataDir string) error {
	if _, err := exactAbsolutePath(dataDir); err != nil || dataDir == "/" {
		return fmt.Errorf("configured data directory required")
	}
	request, hash, err := readLocalRetentionRequest(dataDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	root := filepath.Join(dataDir, "backups")
	var ack struct {
		Schema        string `json:"schema"`
		RequestSHA256 string `json:"request_sha256"`
		Status        string `json:"status"`
	}
	if checkNotesRecoveryRecordCustody(root, "local-retention-ack") == nil {
		if _, err := notesRecoveryReadJSON(filepath.Join(root, "local-retention-ack.json"), 4096, &ack); err == nil && ack.Schema == "loom.local_retention.ack.v1" && ack.RequestSHA256 == hash && (ack.Status == "planned" || ack.Status == "applied") {
			return nil
		}
	}
	request.WakeAt = time.Now().UTC()
	return writeRecoveryRecord(root, "local-retention-request.json", request, 0644)
}
