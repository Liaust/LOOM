package projectapply

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/backupcoverage"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/maintenance"
	pc "loom.local/loom/internal/projectcontracts"
	sr "loom.local/loom/internal/serviceregistry"
)

type applicationBackupFixture struct {
	rows   []maintenance.BackupOperation
	filter maintenance.OperationFilter
}

func (f *applicationBackupFixture) ListBackups(_ context.Context, filter maintenance.OperationFilter) ([]maintenance.BackupOperation, error) {
	f.filter = filter
	return f.rows, nil
}

func TestApplicationCloudBackupStatusUsesActualFreshArchive(t *testing.T) {
	for _, name := range []string{"success", "none", "failed", "running", "uncommitted", "retryable", "old_installation", "expired", "future", "wrong_node", "wrong_root", "excluded_root", "missing_artifact", "wrong_hash", "wrong_artifact_root", "data_changed", "custody_unknown", "no_installation_time"} {
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
			installed := now.Add(-2 * time.Hour)
			root := "/srv/loom/application-data"
			call, _, _ := applicationFixture(t)
			var payload applicationPayload
			_ = json.Unmarshal(call.Payload, &payload)
			q := payload.Request
			q.Data = map[string]sr.ApplicationDataRequest{"files": {Backup: "cloud_history"}}
			entry := backupcoverage.CloudRootEvidence{Name: "application_data", PathSHA256: backupcoverage.CloudRootPathSHA256(root), NodeID: "main", SnapshotAt: now.Add(-time.Hour)}
			digest := strings.Repeat("a", 64)
			operation := maintenance.Operation{MaintenanceOperationID: "maintenance_operation_fixture", OperationKind: maintenance.OperationKindCloudSnapshotUpload, SubjectKind: "node", SubjectID: q.Owner.NodeID, Status: maintenance.OperationSucceeded, FinishedAt: &now}
			result := map[string]any{"status": "succeeded", "committed": true, "retryable": false, "manifest_sha256": digest}
			receipt := sr.ApplicationRuntimeReceipt{Owner: q.Owner, AppliedAt: &installed, Current: &sr.ApplicationCurrentObservation{State: "observed"}, Data: map[string]sr.ApplicationDataIdentity{"files": {Path: filepath.Join(root, q.Owner.Instance()+"-files"), Mount: root, Inode: 1, PoolInode: 2}}}
			switch name {
			case "failed":
				operation.Status = maintenance.OperationFailed
			case "running":
				operation.Status = maintenance.OperationRunning
			case "uncommitted":
				result["committed"] = false
			case "retryable":
				result["retryable"] = true
			case "old_installation":
				installed = now
			case "expired":
				entry.SnapshotAt = now.Add(-37 * time.Hour)
				installed = now.Add(-40 * time.Hour)
			case "future":
				entry.SnapshotAt = now.Add(time.Hour)
			case "wrong_node":
				entry.NodeID = "other"
			case "wrong_root":
				entry.PathSHA256 = strings.Repeat("b", 64)
			case "excluded_root":
				entry.Name = "box_projects"
			case "data_changed":
				delete(receipt.Data, "files")
			case "custody_unknown":
				receipt.Current.ErrorCode = "application.data.changed"
			case "no_installation_time":
				receipt.AppliedAt = nil
			}
			result["root_coverage"] = []backupcoverage.CloudRootEvidence{entry}
			operation.ResultJSON, _ = json.Marshal(result)
			if name == "wrong_artifact_root" {
				entry.PathSHA256 = strings.Repeat("c", 64)
			}
			metadata, _ := json.Marshal(map[string]any{"root_coverage": []backupcoverage.CloudRootEvidence{entry}})
			if name == "wrong_hash" {
				digest = strings.Repeat("b", 64)
			}
			row := maintenance.BackupOperation{Operation: operation, Artifacts: []maintenance.Artifact{{MaintenanceOperationID: operation.MaintenanceOperationID, ArtifactKind: maintenance.ArtifactKindCloudSnapshot, SHA256: &digest, Metadata: metadata}}}
			if name == "missing_artifact" {
				row.Artifacts = nil
			}
			f := &applicationBackupFixture{rows: []maintenance.BackupOperation{row}}
			if name == "none" {
				f.rows = nil
			}
			s := CloudApplicationBackupStatus{Operations: f, Config: func() (config.Config, error) {
				return config.Config{ApplicationDataBackupRoot: root, NodeID: "main"}, nil
			}, Now: func() time.Time { return now }}
			got, err := s.ApplicationProtection(t.Context(), q, receipt)
			want := pc.DeclarationPending
			if name == "success" {
				want = pc.DeclarationSatisfied
			}
			if name == "failed" {
				want = pc.DeclarationFailed
			}
			if err != nil || got.State != want {
				t.Fatalf("%s: %+v %v", name, got, err)
			}
			if got.State == pc.DeclarationSatisfied && (got.EvidenceRef != operation.MaintenanceOperationID || got.Revision != digest) {
				t.Fatal("unqualified evidence")
			}
			if f.filter.Kind != "" && (f.filter.SubjectID != q.Owner.NodeID || f.filter.Kind != maintenance.OperationKindCloudSnapshotUpload || f.filter.Limit != 1 || f.filter.Status != "") {
				t.Fatal("backup query hides latest failure or crosses node", f.filter)
			}
		})
	}
}
