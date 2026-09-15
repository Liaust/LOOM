package projectapply

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/backupcoverage"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/maintenance"
	pc "loom.local/loom/internal/projectcontracts"
	sr "loom.local/loom/internal/serviceregistry"
)

type ApplicationBackupStatus interface {
	ApplicationProtection(context.Context, sr.ApplicationRuntimeRequest, sr.ApplicationRuntimeReceipt) (pc.DeclarationFact, error)
}

type ApplicationBackupOperations interface {
	ListBackups(context.Context, maintenance.OperationFilter) ([]maintenance.BackupOperation, error)
}

type CloudApplicationBackupStatus struct {
	Operations ApplicationBackupOperations
	Config     func() (config.Config, error)
	Now        func() time.Time
}

func (s CloudApplicationBackupStatus) ApplicationProtection(ctx context.Context, q sr.ApplicationRuntimeRequest, receipt sr.ApplicationRuntimeReceipt) (pc.DeclarationFact, error) {
	out := pc.DeclarationFact{State: pc.DeclarationPending}
	if s.Operations == nil || s.Config == nil {
		return out, nil
	}
	cfg, err := s.Config()
	if err != nil {
		return out, err
	}
	root := cfg.ApplicationDataBackupRoot
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" || receipt.Owner != q.Owner || receipt.AppliedAt == nil || receipt.AppliedAt.IsZero() || receipt.Current == nil || receipt.Current.State != "observed" || receipt.Current.ErrorCode != "" {
		return out, nil
	}
	requested := false
	for key, data := range q.Data {
		if data.Backup != "cloud_history" {
			continue
		}
		requested = true
		actual := receipt.Data[key]
		if actual.Path != filepath.Join(root, q.Owner.Instance()+"-"+key) || actual.Mount != root || actual.Inode == 0 || actual.PoolInode == 0 {
			return out, nil
		}
	}
	if !requested {
		return pc.DeclarationFact{State: pc.DeclarationUnknown}, nil
	}
	rows, err := s.Operations.ListBackups(ctx, maintenance.OperationFilter{Kind: maintenance.OperationKindCloudSnapshotUpload, SubjectKind: "node", SubjectID: q.Owner.NodeID, Limit: 1})
	if err != nil {
		return out, err
	}
	if len(rows) != 1 {
		return out, nil
	}
	return applicationCloudBackupFact(q, receipt.AppliedAt.UTC(), root, cfg.NodeID, rows[0], s.now()), nil
}

func (s CloudApplicationBackupStatus) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func applicationCloudBackupFact(q sr.ApplicationRuntimeRequest, installed time.Time, root, archiveNode string, row maintenance.BackupOperation, now time.Time) pc.DeclarationFact {
	out := pc.DeclarationFact{State: pc.DeclarationPending}
	op := row.Operation
	if op.OperationKind != maintenance.OperationKindCloudSnapshotUpload || op.SubjectKind != "node" || op.SubjectID != q.Owner.NodeID {
		return out
	}
	if op.Status == maintenance.OperationFailed {
		out.State = pc.DeclarationFailed
		return out
	}
	if op.Status != maintenance.OperationSucceeded || op.FinishedAt == nil || op.FinishedAt.After(now) || !strings.HasPrefix(op.MaintenanceOperationID, "maintenance_operation_") {
		return out
	}
	var result struct {
		Status         string                             `json:"status"`
		Committed      bool                               `json:"committed"`
		Retryable      bool                               `json:"retryable"`
		ManifestSHA256 string                             `json:"manifest_sha256"`
		Roots          []backupcoverage.CloudRootEvidence `json:"root_coverage"`
	}
	if json.Unmarshal(op.ResultJSON, &result) != nil || result.Status != "succeeded" || !result.Committed || result.Retryable || len(result.ManifestSHA256) != 64 {
		return out
	}
	for _, entry := range result.Roots {
		// Archive naming uses the configured node key; the maintenance operation
		// independently binds the registered owner node ID.
		if archiveNode == "" || entry.Name != "application_data" || entry.PathSHA256 != backupcoverage.CloudRootPathSHA256(root) || entry.NodeID != archiveNode || entry.SnapshotAt.IsZero() || entry.SnapshotAt.Before(installed) || entry.SnapshotAt.After(*op.FinishedAt) || now.Sub(entry.SnapshotAt) > 36*time.Hour {
			continue
		}
		for _, artifact := range row.Artifacts {
			var metadata struct {
				Roots []backupcoverage.CloudRootEvidence `json:"root_coverage"`
			}
			if artifact.ArtifactKind != maintenance.ArtifactKindCloudSnapshot || artifact.MaintenanceOperationID != op.MaintenanceOperationID || artifact.SHA256 == nil || *artifact.SHA256 != result.ManifestSHA256 || json.Unmarshal(artifact.Metadata, &metadata) != nil {
				continue
			}
			for _, recorded := range metadata.Roots {
				if recorded == entry {
					return pc.DeclarationFact{State: pc.DeclarationSatisfied, Revision: result.ManifestSHA256, EvidenceRef: op.MaintenanceOperationID}
				}
			}
		}
	}
	return out
}
