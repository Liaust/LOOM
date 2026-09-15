package cloudstorage

import (
	"context"
	"path/filepath"
	"strings"

	"loom.local/loom/internal/storagecatalog"
)

func LatestVerifiedRetentionCoverage(ctx context.Context, cfg Config) (storagecatalog.MainDocumentCloudBackup, error) {
	cfg, err := NormalizeConfig(cfg)
	if err != nil {
		return storagecatalog.MainDocumentCloudBackup{}, err
	}
	if !cfg.Enabled {
		return storagecatalog.MainDocumentCloudBackup{}, nil
	}
	matches, err := filepath.Glob(filepath.Join(cfg.StateDir, "manifests", "*.json"))
	if err != nil {
		return storagecatalog.MainDocumentCloudBackup{}, err
	}
	best := storagecatalog.MainDocumentCloudBackup{}
	for _, path := range matches {
		if err := ctx.Err(); err != nil {
			return storagecatalog.MainDocumentCloudBackup{}, err
		}
		manifest, err := ReadSnapshotUploadManifest(path)
		if err != nil {
			continue
		}
		if strings.TrimSpace(manifest.SourceBackupPaths.StorageRetention) == "" {
			continue
		}
		if manifest.VerifyAfterUpload != SnapshotStatusSucceeded {
			continue
		}
		if manifest.CompletedAt.IsZero() {
			continue
		}
		if best.VerifiedAt != nil && !manifest.CompletedAt.After(*best.VerifiedAt) {
			continue
		}
		ref := firstNonEmpty(manifest.SnapshotRef, manifest.Archive, manifest.UploadID)
		completed := manifest.CompletedAt.UTC()
		best = storagecatalog.MainDocumentCloudBackup{
			Confirmed:              true,
			Ref:                    ref,
			VerifiedAt:             &completed,
			CoversStorageRetention: true,
		}
	}
	return best, nil
}
