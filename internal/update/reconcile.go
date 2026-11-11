package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

const ReconciliationSchemaVersion = "loom.update.reconciliation.v1"

type ReconcileInput struct {
	StateDir     string
	ManifestPath string
	Runner       CommandRunner
}

type ReconciliationEvidence struct {
	SchemaVersion         string            `json:"schema_version"`
	EvidenceID            string            `json:"evidence_id"`
	UpdateID              string            `json:"update_id"`
	PlanHash              string            `json:"plan_hash"`
	InitialStatus         string            `json:"initial_status"`
	InitialManifestSHA256 string            `json:"initial_manifest_sha256"`
	Status                string            `json:"status"`
	Readiness             ReadinessEvidence `json:"readiness"`
}

type ReconcileResult struct {
	Status       string                  `json:"status"`
	ManifestPath string                  `json:"manifest_path"`
	EvidencePath string                  `json:"evidence_path,omitempty"`
	Appended     bool                    `json:"appended"`
	Evidence     *ReconciliationEvidence `json:"evidence,omitempty"`
}

var operationIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

// Reconcile observes only. There is deliberately no build, maintenance,
// backup, activation or restart callback in this API.
func Reconcile(ctx context.Context, input ReconcileInput) (ReconcileResult, error) {
	stateDir := firstNonEmpty(input.StateDir, DefaultStateDir(""))
	path := firstNonEmpty(input.ManifestPath, ActiveManifestPath(stateDir))
	result := ReconcileResult{Status: "unverified", ManifestPath: path}
	initial, err := os.ReadFile(path)
	if err != nil {
		return result, err
	}
	manifest, err := ReadUpdateManifest(path)
	if err != nil {
		return result, err
	}
	if !operationIDPattern.MatchString(manifest.UpdateID) || manifest.PlanHash == "" {
		return result, fmt.Errorf("existing update operation identity is invalid")
	}
	if manifest.Status != UpdateStatusFailed && manifest.Status != UpdateStatusSucceeded && manifest.Status != UpdateStatusRolledBack {
		return result, fmt.Errorf("only a finished update operation may be reconciled")
	}
	if manifest.ReadinessTarget == nil {
		return result, fmt.Errorf("existing operation has no reviewed executable identity; readiness remains unverified")
	}
	target := *manifest.ReadinessTarget
	if target.ReleaseID != manifest.Target.ReleaseID || target.ReleasePath != manifest.Target.Path {
		return result, fmt.Errorf("reviewed executable identity does not belong to this update target")
	}
	if target.MigrationHead != manifest.Migrations.TargetLatestVersion {
		return result, fmt.Errorf("reviewed migration identity differs from this update target")
	}
	evidence, err := runTargetReadiness(ctx, firstRunner(input.Runner), target)
	if err != nil {
		return result, err
	}
	status := "observed_ready"
	if manifest.Status == UpdateStatusFailed {
		status = "failed_then_observed_ready"
	}
	record := ReconciliationEvidence{SchemaVersion: ReconciliationSchemaVersion, UpdateID: manifest.UpdateID, PlanHash: manifest.PlanHash,
		InitialStatus: manifest.Status, InitialManifestSHA256: bytesSHA256(initial), Status: status, Readiness: evidence}
	record.EvidenceID = reconciliationID(record)
	// Do not attach evidence to a receipt replaced while observation ran.
	current, err := os.ReadFile(path)
	if err != nil {
		return result, err
	}
	if bytesSHA256(current) != record.InitialManifestSHA256 {
		return result, fmt.Errorf("update receipt changed during observation; inspect again")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	dir := filepath.Join(stateDir, "reconciliations", manifest.UpdateID)
	result.EvidencePath = filepath.Join(dir, record.EvidenceID+".json")
	stored, appended, err := appendReconciliation(result.EvidencePath, record)
	if err != nil {
		return result, err
	}
	result.Status, result.Evidence, result.Appended = stored.Status, &stored, appended
	return result, nil
}

func bytesSHA256(raw []byte) string {
	hash := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(hash[:])
}

func reconciliationID(record ReconciliationEvidence) string {
	// Equivalent observations of the same instance are idempotent, even if
	// inspected later. Preserve the first observation's time in its sidecar.
	record.EvidenceID = ""
	record.Readiness.ObservedAt = time.Time{}
	raw, _ := json.Marshal(record)
	return "ready_" + bytesSHA256(raw)[7:]
}

func appendReconciliation(path string, record ReconciliationEvidence) (ReconciliationEvidence, bool, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o770); err != nil {
		return record, false, err
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return record, false, err
	}
	tmp, err := os.CreateTemp(dir, ".reconcile-*")
	if err != nil {
		return record, false, err
	}
	defer os.Remove(tmp.Name())
	alignUpdateManifestPermissions(tmp.Name(), dir)
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		_ = tmp.Close()
		return record, false, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return record, false, err
	}
	if err := tmp.Close(); err != nil {
		return record, false, err
	}
	// Link publishes a complete file atomically and never overwrites a prior
	// observation, including when two inspectors observe the same instance.
	if err := os.Link(tmp.Name(), path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return record, false, err
		}
		stored, err := readReconciliation(path)
		if err != nil {
			return record, false, err
		}
		if stored.EvidenceID != record.EvidenceID || reconciliationID(stored) != record.EvidenceID {
			return record, false, fmt.Errorf("existing reconciliation evidence differs")
		}
		return stored, false, nil
	}
	directory, err := os.Open(dir)
	if err != nil {
		return record, true, err
	}
	defer directory.Close()
	return record, true, directory.Sync()
}

func readReconciliation(path string) (ReconciliationEvidence, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ReconciliationEvidence{}, err
	}
	var record ReconciliationEvidence
	if err := json.Unmarshal(raw, &record); err != nil {
		return record, err
	}
	if record.SchemaVersion != ReconciliationSchemaVersion || record.EvidenceID != reconciliationID(record) {
		return record, fmt.Errorf("invalid reconciliation evidence")
	}
	return record, nil
}
