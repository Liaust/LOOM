package cloudstorage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/backupcoverage"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/maintenance"
)

type LegacyTreeSnapshotBackend struct {
	Driver                    Driver
	beforeMetadataRehydration func(string) error
	afterFinalRemoteCheck     func(string) error
}

const maxLegacyTreeUploadManifestBytes int64 = 4 << 20

func (b LegacyTreeSnapshotBackend) BackendKind() string {
	return SnapshotBackendLegacyTree
}

func (b LegacyTreeSnapshotBackend) Push(ctx context.Context, input SnapshotPushInput) (SnapshotPushResult, error) {
	started := time.Now()
	metrics := SnapshotMetrics{}
	now := normalizeNow(input.Now)
	cfg, err := NormalizeConfig(input.Config)
	if err != nil {
		return SnapshotPushResult{}, err
	}
	if !cfg.Enabled {
		return SnapshotPushResult{}, fmt.Errorf("cloud storage is disabled")
	}
	driver, err := normalizeDriver(cfg, firstNonNilDriver(input.Driver, b.Driver))
	if err != nil {
		return SnapshotPushResult{}, err
	}
	dataDir := firstNonEmpty(input.DataDir, input.CoverageOptions.DataDir, config.DefaultDataDir)
	backupRoot := firstNonEmpty(input.BackupRoot, input.CoverageOptions.BackupRoot, filepath.Join(dataDir, "backups", "main"))
	backupDir, err := ResolveBackupDir(input.BackupRef, backupRoot)
	if err != nil {
		return SnapshotPushResult{}, err
	}
	stageStarted := time.Now()
	verification, err := maintenance.VerifyBackupDirectory(ctx, backupDir)
	metrics.LocalBackupVerifyDurationMS = durationMSSince(stageStarted)
	if err != nil {
		return SnapshotPushResult{}, err
	}
	result := SnapshotPushResult{
		Status:       SnapshotStatusFailed,
		DryRun:       input.DryRun,
		Backend:      SnapshotBackendLegacyTree,
		BackupRef:    firstNonEmpty(input.BackupRef, "latest"),
		BackupDir:    backupDir,
		Verification: verification,
		Checks:       map[string]string{"local_backup_verify": verification.Status},
		Metrics:      metrics,
	}
	if verification.Status != maintenance.VerificationSucceeded {
		result.Error = "local backup verification failed"
		metrics.TotalDurationMS = durationMSSince(started)
		result.Metrics = metrics
		return result, nil
	}
	stageStarted = time.Now()
	coverage, err := backupcoverage.Check(ctx, snapshotCoverageOptions(input, backupRoot, filepath.Join(backupDir, "manifest.json")))
	metrics.CoverageCheckDurationMS = durationMSSince(stageStarted)
	if err != nil {
		return SnapshotPushResult{}, err
	}
	result.Coverage = coverage
	result.Checks["backup_coverage"] = coverage.Status
	if coverage.Status == backupcoverage.OverallCritical {
		result.Error = "backup coverage is critical"
		metrics.TotalDurationMS = durationMSSince(started)
		result.Metrics = metrics
		return result, nil
	}
	stageStarted = time.Now()
	fileCount, totalBytes, err := maintenance.DirectoryInventory(backupDir)
	metrics.InventoryDurationMS = durationMSSince(stageStarted)
	if err != nil {
		return SnapshotPushResult{}, err
	}
	result.FileCount = fileCount
	result.TotalBytes = totalBytes
	sourceManifestPath := filepath.Join(backupDir, "manifest.json")
	sourceManifest, sourceManifestSHA, sourceManifestSize, err := maintenance.ReadBackupManifestWithSHA256(sourceManifestPath)
	if err != nil {
		return SnapshotPushResult{}, err
	}
	snapshotRef := snapshotRefFromBackupDir(backupDir)
	uploadID := newCloudUploadID(now(), snapshotRef)
	nodeID := safeRemoteSegment(firstNonEmpty(input.NodeID, "main"))
	finalPrefix := path.Join(cfg.Roots.MainSnapshots, nodeID, snapshotRef)
	tempPrefix := path.Join("_system", "tmp", "snapshot-uploads", uploadID)
	result.SnapshotRef = snapshotRef
	result.UploadID = uploadID
	result.RemotePrefix = finalPrefix
	result.RemoteURI = cfg.RemoteURI(finalPrefix)
	result.TempRemotePrefix = tempPrefix
	result.TempRemoteURI = cfg.RemoteURI(tempPrefix)
	if input.DryRun {
		result.Status = SnapshotStatusPlanned
		metrics.TotalDurationMS = durationMSSince(started)
		result.Metrics = metrics
		return result, nil
	}
	mover, ok := driver.(RemoteMover)
	if !ok {
		return SnapshotPushResult{}, fmt.Errorf("configured cloud driver does not support remote promotion")
	}
	copyResult, err := driver.CopyToRemote(ctx, backupDir, tempPrefix, CopyOptions{Checksum: true, PreserveLinks: true, PreserveMetadata: true})
	metrics.RemoteCopyDurationMS += copyResult.DurationMS
	if err != nil {
		result.Checks["remote_copy"] = SnapshotStatusFailed
		result.Error = err.Error()
		metrics.TotalDurationMS = durationMSSince(started)
		result.Metrics = metrics
		return result, nil
	}
	_ = copyResult
	result.Checks["remote_copy"] = SnapshotStatusSucceeded
	checkTemp, err := checkLegacyTreeSnapshot(ctx, driver, backupDir, tempPrefix)
	metrics.RemoteCheckDurationMS += checkTemp.DurationMS
	if err != nil || !checkTemp.Matched {
		result.Checks["remote_check_temp"] = SnapshotStatusFailed
		if err != nil {
			result.Error = err.Error()
		} else {
			result.Error = "temporary remote check failed"
		}
		metrics.TotalDurationMS = durationMSSince(started)
		result.Metrics = metrics
		return result, nil
	}
	result.Checks["remote_check_temp"] = SnapshotStatusSucceeded
	moveResult, err := mover.MoveRemote(ctx, tempPrefix, finalPrefix, CopyOptions{})
	metrics.RemotePromoteDurationMS += moveResult.DurationMS
	if err != nil {
		result.Checks["remote_promote"] = SnapshotStatusFailed
		result.Error = err.Error()
		metrics.TotalDurationMS = durationMSSince(started)
		result.Metrics = metrics
		return result, nil
	}
	result.Checks["remote_promote"] = SnapshotStatusSucceeded
	checkFinal, err := checkLegacyTreeSnapshot(ctx, driver, backupDir, finalPrefix)
	metrics.RemoteCheckDurationMS += checkFinal.DurationMS
	if err != nil || !checkFinal.Matched {
		result.Checks["remote_check_final"] = SnapshotStatusFailed
		if err != nil {
			result.Error = err.Error()
		} else {
			result.Error = "final remote check failed"
		}
		metrics.TotalDurationMS = durationMSSince(started)
		result.Metrics = metrics
		return result, nil
	}
	result.Checks["remote_check_final"] = SnapshotStatusSucceeded
	if b.afterFinalRemoteCheck != nil {
		if err := b.afterFinalRemoteCheck(sourceManifestPath); err != nil {
			result.Checks["source_backup_manifest_stable"] = SnapshotStatusFailed
			result.Error = err.Error()
			metrics.TotalDurationMS = durationMSSince(started)
			result.Metrics = metrics
			return result, nil
		}
	}
	completed := now()
	_, finalSourceManifestSHA, finalSourceManifestSize, err := maintenance.ReadBackupManifestWithSHA256(sourceManifestPath)
	if err != nil {
		result.Checks["source_backup_manifest_stable"] = SnapshotStatusFailed
		result.Error = err.Error()
		metrics.TotalDurationMS = durationMSSince(started)
		result.Metrics = metrics
		return result, nil
	}
	if finalSourceManifestSize != sourceManifestSize || finalSourceManifestSHA != sourceManifestSHA {
		result.Checks["source_backup_manifest_stable"] = SnapshotStatusFailed
		result.Error = "source backup manifest changed during cloud upload"
		metrics.TotalDurationMS = durationMSSince(started)
		result.Metrics = metrics
		return result, nil
	}
	result.Checks["source_backup_manifest_stable"] = SnapshotStatusSucceeded
	manifest := SnapshotUploadManifest{
		SchemaVersion:              SnapshotUploadManifestSchema,
		UploadID:                   uploadID,
		Backend:                    SnapshotBackendLegacyTree,
		Provider:                   cfg.Provider,
		BackupOperationID:          verification.BackupOperationID,
		SourceBackupDir:            backupDir,
		SourceBackupManifestPath:   sourceManifestPath,
		SourceBackupManifestSHA256: sourceManifestSHA,
		SourceBackupPaths:          sourceManifest.Paths,
		RemoteURI:                  cfg.RemoteURI(finalPrefix),
		RemotePrefix:               finalPrefix,
		SnapshotRef:                snapshotRef,
		CreatedAt:                  completed,
		CompletedAt:                completed,
		Driver:                     "rclone_sftp",
		VerifyBeforeUpload:         verification.Status,
		VerifyAfterUpload:          SnapshotStatusSucceeded,
		FileCount:                  fileCount,
		TotalBytes:                 totalBytes,
		Checks:                     result.Checks,
	}
	localManifestPath := filepath.Join(cfg.StateDir, "manifests", uploadID+".json")
	stageStarted = time.Now()
	if err := WriteSnapshotUploadManifest(localManifestPath, manifest); err != nil {
		return SnapshotPushResult{}, err
	}
	metrics.ManifestWriteDurationMS = durationMSSince(stageStarted)
	manifestUpload, err := driver.CopyToRemote(ctx, localManifestPath, path.Join(finalPrefix, "cloud-upload.json"), CopyOptions{SingleFile: true})
	metrics.ManifestUploadDurationMS = manifestUpload.DurationMS
	if err != nil {
		result.Checks["cloud_upload_manifest"] = SnapshotStatusFailed
		result.Error = err.Error()
		metrics.TotalDurationMS = durationMSSince(started)
		result.Metrics = metrics
		return result, nil
	}
	result.Checks["cloud_upload_manifest"] = SnapshotStatusSucceeded
	result.LocalManifestPath = localManifestPath
	result.Manifest = &manifest
	result.Status = SnapshotStatusSucceeded
	metrics.TotalDurationMS = durationMSSince(started)
	result.Metrics = metrics
	return result, nil
}

func (b LegacyTreeSnapshotBackend) List(ctx context.Context, input SnapshotListInput) (SnapshotListResult, error) {
	cfg, err := NormalizeConfig(input.Config)
	if err != nil {
		return SnapshotListResult{}, err
	}
	driver, err := normalizeDriver(cfg, firstNonNilDriver(input.Driver, b.Driver))
	if err != nil {
		return SnapshotListResult{}, err
	}
	nodeID := safeRemoteSegment(firstNonEmpty(input.NodeID, "main"))
	root := path.Join(cfg.Roots.MainSnapshots, nodeID)
	entries, err := driver.List(ctx, root)
	if err != nil {
		return SnapshotListResult{}, err
	}
	items := make([]SnapshotItem, 0, len(entries))
	now := time.Now().UTC()
	for _, entry := range entries {
		if !entry.IsDir {
			continue
		}
		ref := path.Base(entry.Path)
		prefix := path.Join(root, ref)
		items = append(items, SnapshotItem{Ref: ref, Backend: SnapshotBackendLegacyTree, RemotePrefix: prefix, RemoteURI: cfg.RemoteURI(prefix), Status: "discovered", DiscoveredAt: now})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Ref < items[j].Ref })
	result := SnapshotListResult{Status: "empty", NodeID: nodeID, RemoteRoot: cfg.RemoteURI(root), Snapshots: items}
	if len(items) > 0 {
		result.Status = "ok"
		latest := items[len(items)-1]
		result.Latest = &latest
	}
	return result, nil
}

func (b LegacyTreeSnapshotBackend) Verify(ctx context.Context, input SnapshotVerifyInput) (SnapshotVerifyResult, error) {
	now := normalizeNow(input.Now)
	cfg, err := NormalizeConfig(input.Config)
	if err != nil {
		return SnapshotVerifyResult{}, err
	}
	driver, err := normalizeDriver(cfg, firstNonNilDriver(input.Driver, b.Driver))
	if err != nil {
		return SnapshotVerifyResult{}, err
	}
	item, err := resolveLegacyTreeSnapshotRef(ctx, cfg, driver, input.NodeID, input.Ref)
	if err != nil {
		return SnapshotVerifyResult{}, err
	}
	stateDir := firstNonEmpty(input.StateDir, cfg.StateDir)
	localManifest := filepath.Join(stateDir, "temp", "snapshot-verify", item.Ref, "cloud-upload.json")
	_ = os.Remove(localManifest)
	if err := os.MkdirAll(filepath.Dir(localManifest), 0o750); err != nil {
		return SnapshotVerifyResult{}, err
	}
	copyResult, err := driver.CopyFromRemote(ctx, path.Join(item.RemotePrefix, "cloud-upload.json"), localManifest, CopyOptions{SingleFile: true})
	result := SnapshotVerifyResult{
		Status:       SnapshotStatusFailed,
		Ref:          item.Ref,
		Backend:      SnapshotBackendLegacyTree,
		RemotePrefix: item.RemotePrefix,
		RemoteURI:    item.RemoteURI,
		ManifestPath: localManifest,
		CheckedAt:    now(),
		Checks:       map[string]string{},
	}
	if err != nil {
		result.Checks["cloud_upload_manifest"] = SnapshotStatusFailed
		result.Errors = append(result.Errors, err.Error())
		_ = copyResult
		return result, nil
	}
	manifest, err := readLegacyTreeUploadManifestBounded(localManifest)
	if err != nil {
		result.Checks["cloud_upload_manifest"] = SnapshotStatusFailed
		result.Errors = append(result.Errors, err.Error())
		return result, nil
	}
	result.Manifest = manifest
	result.Checks["cloud_upload_manifest"] = SnapshotStatusSucceeded
	if err := validateLegacyTreeUploadIdentity(manifest, item); err != nil {
		result.Checks["remote_identity"] = SnapshotStatusFailed
		result.Errors = append(result.Errors, err.Error())
		return result, nil
	}
	result.Checks["remote_identity"] = SnapshotStatusSucceeded
	remoteBackupManifest := filepath.Join(filepath.Dir(localManifest), "manifest.json")
	_ = os.Remove(remoteBackupManifest)
	if _, err := driver.CopyFromRemote(ctx, path.Join(item.RemotePrefix, "manifest.json"), remoteBackupManifest, CopyOptions{SingleFile: true}); err != nil {
		result.Checks["source_backup_manifest"] = SnapshotStatusFailed
		result.Errors = append(result.Errors, err.Error())
		return result, nil
	}
	if err := verifyLegacyTreeSourceManifest(manifest, remoteBackupManifest); err != nil {
		result.Checks["source_backup_manifest"] = SnapshotStatusFailed
		result.Errors = append(result.Errors, err.Error())
		return result, nil
	}
	result.Checks["source_backup_manifest"] = SnapshotStatusSucceeded
	if _, err := driver.List(ctx, item.RemotePrefix); err != nil {
		result.Checks["remote_list"] = SnapshotStatusFailed
		result.Errors = append(result.Errors, err.Error())
		return result, nil
	}
	result.Checks["remote_list"] = SnapshotStatusSucceeded
	result.Status = SnapshotStatusSucceeded
	return result, nil
}

func (b LegacyTreeSnapshotBackend) Fetch(ctx context.Context, input SnapshotFetchInput) (SnapshotFetchResult, error) {
	custody, err := beginRestoreFetchDestination(&input)
	if err != nil {
		return SnapshotFetchResult{}, err
	}
	defer custody.close()
	cfg, err := NormalizeConfig(input.Config)
	if err != nil {
		return SnapshotFetchResult{}, err
	}
	driver, err := normalizeDriver(cfg, firstNonNilDriver(input.Driver, b.Driver))
	if err != nil {
		return SnapshotFetchResult{}, err
	}
	item, err := resolveLegacyTreeSnapshotRef(ctx, cfg, driver, input.NodeID, input.Ref)
	if err != nil {
		return SnapshotFetchResult{}, err
	}
	target := filepath.Clean(strings.TrimSpace(input.To))
	if target == "." || target == "" {
		return SnapshotFetchResult{}, fmt.Errorf("target directory is required")
	}
	if err := custody.revalidate(); err != nil {
		return SnapshotFetchResult{}, err
	}
	if err := requireEmptyOrMissingDir(target); err != nil {
		return SnapshotFetchResult{}, err
	}
	copyResult, err := driver.CopyFromRemote(ctx, item.RemotePrefix, target, CopyOptions{PreserveLinks: true, PreserveMetadata: true})
	if err != nil {
		return SnapshotFetchResult{}, err
	}
	if _, err := validateDownloadedLegacyTreeSnapshot(target, item); err != nil {
		return SnapshotFetchResult{}, fmt.Errorf("authenticate legacy-tree snapshot: %w", err)
	}
	if b.beforeMetadataRehydration != nil {
		if err := b.beforeMetadataRehydration(target); err != nil {
			return SnapshotFetchResult{}, err
		}
	}
	if err := maintenance.RehydrateLegacyTreeImportsMetadata(ctx, target); err != nil {
		return SnapshotFetchResult{}, fmt.Errorf("rehydrate legacy-tree Imports metadata: %w", err)
	}
	verification, err := maintenance.VerifyBackupDirectory(ctx, target)
	if err != nil {
		return SnapshotFetchResult{}, err
	}
	status := SnapshotStatusSucceeded
	if verification.Status != maintenance.VerificationSucceeded {
		status = SnapshotStatusFailed
	}
	return SnapshotFetchResult{
		Status:       status,
		Ref:          item.Ref,
		Backend:      SnapshotBackendLegacyTree,
		RemotePrefix: item.RemotePrefix,
		RemoteURI:    item.RemoteURI,
		TargetDir:    target,
		Copy:         copyResult,
		Verification: verification,
	}, nil
}

func validateDownloadedLegacyTreeSnapshot(target string, item SnapshotItem) (SnapshotUploadManifest, error) {
	manifest, err := readLegacyTreeUploadManifestBounded(filepath.Join(target, "cloud-upload.json"))
	if err != nil {
		return SnapshotUploadManifest{}, err
	}
	if err := validateLegacyTreeUploadIdentity(manifest, item); err != nil {
		return SnapshotUploadManifest{}, err
	}
	if err := verifyLegacyTreeSourceManifest(manifest, filepath.Join(target, "manifest.json")); err != nil {
		return SnapshotUploadManifest{}, err
	}
	return manifest, nil
}

func validateLegacyTreeUploadIdentity(manifest SnapshotUploadManifest, item SnapshotItem) error {
	if manifest.Backend != SnapshotBackendLegacyTree {
		return fmt.Errorf("cloud upload manifest backend %q is not %q", manifest.Backend, SnapshotBackendLegacyTree)
	}
	if strings.TrimSpace(manifest.RemotePrefix) != strings.TrimSpace(item.RemotePrefix) {
		return fmt.Errorf("cloud upload manifest remote_prefix does not match requested snapshot")
	}
	switch manifest.SchemaVersion {
	case SnapshotUploadManifestSchema:
		if strings.TrimSpace(manifest.SnapshotRef) == "" || manifest.SnapshotRef != item.Ref {
			return fmt.Errorf("cloud upload manifest snapshot_ref does not match requested snapshot")
		}
		if strings.TrimSpace(manifest.RemoteURI) == "" || manifest.RemoteURI != item.RemoteURI {
			return fmt.Errorf("cloud upload manifest remote_uri does not match requested snapshot")
		}
		if strings.TrimSpace(manifest.SourceBackupManifestSHA256) == "" {
			return fmt.Errorf("current cloud upload manifest requires source_backup_manifest_sha256")
		}
	case SnapshotUploadManifestSchemaV063:
		// v0.6.3 manifests remain readable for pre-v0.9 backups. They may lack
		// the source manifest digest; current-schema snapshots may not.
		if manifest.SnapshotRef != "" && manifest.SnapshotRef != item.Ref {
			return fmt.Errorf("legacy cloud upload manifest snapshot_ref does not match requested snapshot")
		}
	default:
		return fmt.Errorf("unsupported snapshot upload manifest schema %q", manifest.SchemaVersion)
	}
	if value := strings.TrimSpace(manifest.SourceBackupManifestSHA256); value != "" {
		decoded, err := hex.DecodeString(value)
		if err != nil || len(decoded) != sha256.Size {
			return fmt.Errorf("cloud upload manifest has invalid source_backup_manifest_sha256")
		}
	}
	return nil
}

func verifyLegacyTreeSourceManifest(upload SnapshotUploadManifest, manifestPath string) error {
	want := strings.TrimSpace(upload.SourceBackupManifestSHA256)
	if want == "" {
		if upload.SchemaVersion == SnapshotUploadManifestSchemaV063 {
			return nil
		}
		return fmt.Errorf("source backup manifest hash is required")
	}
	got, err := hashLegacyTreeRegularNoFollow(manifestPath, maxLegacyTreeUploadManifestBytes)
	if err != nil {
		return fmt.Errorf("hash downloaded source backup manifest: %w", err)
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("downloaded source backup manifest hash does not match cloud upload manifest")
	}
	return nil
}

func readLegacyTreeUploadManifestBounded(pathValue string) (SnapshotUploadManifest, error) {
	payload, err := readLegacyTreeRegularNoFollow(pathValue, maxLegacyTreeUploadManifestBytes)
	if err != nil {
		return SnapshotUploadManifest{}, err
	}
	var manifest SnapshotUploadManifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		return SnapshotUploadManifest{}, err
	}
	switch strings.TrimSpace(manifest.SchemaVersion) {
	case SnapshotUploadManifestSchema, SnapshotUploadManifestSchemaV063:
	default:
		return SnapshotUploadManifest{}, fmt.Errorf("unsupported snapshot upload manifest schema %q", manifest.SchemaVersion)
	}
	if strings.TrimSpace(manifest.Backend) == "" {
		manifest.Backend = SnapshotBackendLegacyTree
	}
	return manifest, nil
}

func hashLegacyTreeRegularNoFollow(pathValue string, maxBytes int64) (string, error) {
	payload, err := readLegacyTreeRegularNoFollow(pathValue, maxBytes)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func readLegacyTreeRegularNoFollow(pathValue string, maxBytes int64) ([]byte, error) {
	info, err := os.Lstat(pathValue)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a no-follow regular file", pathValue)
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", pathValue, maxBytes)
	}
	file, err := os.Open(pathValue)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("%s changed while opening", pathValue)
	}
	payload, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > maxBytes || int64(len(payload)) != info.Size() {
		return nil, fmt.Errorf("%s changed size or exceeds %d bytes", pathValue, maxBytes)
	}
	var extra [1]byte
	if count, err := file.Read(extra[:]); err != nil && err != io.EOF {
		return nil, err
	} else if count != 0 {
		return nil, fmt.Errorf("%s has trailing bytes beyond its observed size", pathValue)
	}
	return bytes.Clone(payload), nil
}

func checkLegacyTreeSnapshot(ctx context.Context, driver Driver, localPath, remotePath string) (CheckResult, error) {
	checker, ok := driver.(OptionedChecker)
	if !ok {
		return CheckResult{}, fmt.Errorf("legacy-tree snapshot driver does not support preserve-links verification")
	}
	return checker.CheckWithOptions(ctx, localPath, remotePath, CheckOptions{PreserveLinks: true, PreserveMetadata: true})
}

func (b LegacyTreeSnapshotBackend) PlanRetention(ctx context.Context, input SnapshotRetentionInput) (SnapshotRetentionPlan, error) {
	input.Driver = firstNonNilDriver(input.Driver, b.Driver)
	return planLegacyTreeSnapshotRetention(ctx, input)
}

func (b LegacyTreeSnapshotBackend) ApplyRetention(ctx context.Context, input SnapshotRetentionApplyInput) (SnapshotRetentionApplyResult, error) {
	input.Driver = firstNonNilDriver(input.Driver, b.Driver)
	input.SnapshotRetentionInput.Driver = firstNonNilDriver(input.SnapshotRetentionInput.Driver, input.Driver, b.Driver)
	return applyLegacyTreeSnapshotRetention(ctx, input)
}

func resolveLegacyTreeSnapshotRef(ctx context.Context, cfg Config, driver Driver, nodeID, ref string) (SnapshotItem, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = "latest"
	}
	if ref == "latest" {
		list, err := LegacyTreeSnapshotBackend{Driver: driver}.List(ctx, SnapshotListInput{Config: cfg, Driver: driver, NodeID: nodeID})
		if err != nil {
			return SnapshotItem{}, err
		}
		if list.Latest == nil {
			return SnapshotItem{}, fmt.Errorf("no cloud snapshots found for node %s", list.NodeID)
		}
		return *list.Latest, nil
	}
	node := safeRemoteSegment(firstNonEmpty(nodeID, "main"))
	prefix := path.Join(cfg.Roots.MainSnapshots, node, safeRemoteSegment(ref))
	return SnapshotItem{Ref: safeRemoteSegment(ref), Backend: SnapshotBackendLegacyTree, RemotePrefix: prefix, RemoteURI: cfg.RemoteURI(prefix), Status: "requested", DiscoveredAt: time.Now().UTC()}, nil
}

func firstNonNilDriver(values ...Driver) Driver {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}
