package cloudstorage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"loom.local/loom/internal/backupcoverage"
	"loom.local/loom/internal/backupstrategy"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/maintenance"
)

type BorgSnapshotBackend struct {
	Runner BorgCommandRunner
}

func NewBorgSnapshotBackend(cfg Config) BorgSnapshotBackend {
	return BorgSnapshotBackend{Runner: NewBorgCommandRunner(cfg)}
}

func (b BorgSnapshotBackend) BackendKind() string {
	return SnapshotBackendBorg
}

func (b BorgSnapshotBackend) Push(ctx context.Context, input SnapshotPushInput) (SnapshotPushResult, error) {
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
	runner := b.runner(cfg)
	if err := validateBorgCommandConfig(cfg); err != nil {
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
		Backend:      SnapshotBackendBorg,
		BackupRef:    firstNonEmpty(input.BackupRef, "latest"),
		BackupDir:    backupDir,
		Repository:   cfg.Snapshots.Borg.Repository,
		Verification: verification,
		Compression:  cfg.Snapshots.Borg.Compression,
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
	snapshotRef := snapshotRefFromBackupDir(backupDir)
	nodeID := safeRemoteSegment(firstNonEmpty(input.NodeID, "main"))
	archive := borgArchiveName(nodeID, snapshotRef)
	uploadID := newCloudUploadID(now(), snapshotRef)
	result.FileCount = fileCount
	result.TotalBytes = totalBytes
	result.SnapshotRef = snapshotRef
	result.UploadID = uploadID
	result.Archive = archive
	result.RemotePrefix = "::" + archive
	result.RemoteURI = borgRemoteURI(cfg, archive)
	if input.DryRun {
		result.Status = SnapshotStatusPlanned
		metrics.TotalDurationMS = durationMSSince(started)
		result.Metrics = metrics
		return result, nil
	}
	createArgs := []string{"create", "--json", "--stats"}
	if cfg.Snapshots.Borg.Compression != "" {
		createArgs = append(createArgs, "--compression", cfg.Snapshots.Borg.Compression)
	}
	createArgs = append(createArgs, "::"+archive, ".")
	stageStarted = time.Now()
	createOut, err := runner.Run(ctx, BorgCommand{Args: createArgs, Dir: backupDir})
	metrics.BorgCreateDurationMS = durationMSSince(stageStarted)
	if err != nil {
		result.Checks["borg_create"] = SnapshotStatusFailed
		result.Error = err.Error()
		metrics.TotalDurationMS = durationMSSince(started)
		result.Metrics = metrics
		return result, nil
	}
	result.Checks["borg_create"] = SnapshotStatusSucceeded
	packedBytes, deduplicatedBytes, metadata := parseBorgMetadata(createOut)
	result.PackedBytes = packedBytes
	result.DeduplicatedBytes = deduplicatedBytes
	stageStarted = time.Now()
	if _, err := runner.Run(ctx, BorgCommand{Args: []string{"info", "--json", "::" + archive}}); err != nil {
		result.Checks["borg_info"] = SnapshotStatusFailed
		result.Error = err.Error()
		metrics.BorgInfoDurationMS = durationMSSince(stageStarted)
		metrics.TotalDurationMS = durationMSSince(started)
		result.Metrics = metrics
		return result, nil
	}
	metrics.BorgInfoDurationMS = durationMSSince(stageStarted)
	result.Checks["borg_info"] = SnapshotStatusSucceeded
	stageStarted = time.Now()
	if ran, skippedReason, err := runBorgCheckWithCadence(ctx, runner, cfg, archive, now()); err != nil {
		result.Checks["borg_check"] = SnapshotStatusFailed
		result.Error = err.Error()
		metrics.BorgCheckDurationMS = durationMSSince(stageStarted)
		metrics.TotalDurationMS = durationMSSince(started)
		result.Metrics = metrics
		return result, nil
	} else if ran {
		metrics.BorgCheckDurationMS = durationMSSince(stageStarted)
		result.Checks["borg_check"] = SnapshotStatusSucceeded
	} else {
		metrics.BorgCheckDurationMS = durationMSSince(stageStarted)
		result.Checks["borg_check"] = firstNonEmpty(skippedReason, "skipped")
		metrics.BorgCheckSkippedReason = firstNonEmpty(skippedReason, "skipped")
	}
	sourceManifestPath := filepath.Join(backupDir, "manifest.json")
	sourceManifestSHA, err := fileSHA256(sourceManifestPath)
	if err != nil {
		return SnapshotPushResult{}, err
	}
	sourceManifest, err := maintenance.ReadBackupManifest(sourceManifestPath)
	if err != nil {
		return SnapshotPushResult{}, err
	}
	completed := now()
	manifest := SnapshotUploadManifest{
		SchemaVersion:              SnapshotUploadManifestSchema,
		UploadID:                   uploadID,
		Backend:                    SnapshotBackendBorg,
		Provider:                   cfg.Provider,
		BackupOperationID:          verification.BackupOperationID,
		SourceBackupDir:            backupDir,
		SourceBackupManifestPath:   sourceManifestPath,
		SourceBackupManifestSHA256: sourceManifestSHA,
		SourceBackupPaths:          sourceManifest.Paths,
		RemoteURI:                  result.RemoteURI,
		RemotePrefix:               result.RemotePrefix,
		SnapshotRef:                snapshotRef,
		Repository:                 cfg.Snapshots.Borg.Repository,
		Archive:                    archive,
		CreatedAt:                  completed,
		CompletedAt:                completed,
		Driver:                     "borg",
		VerifyBeforeUpload:         verification.Status,
		VerifyAfterUpload:          SnapshotStatusSucceeded,
		FileCount:                  fileCount,
		TotalBytes:                 totalBytes,
		PackedBytes:                packedBytes,
		DeduplicatedBytes:          deduplicatedBytes,
		Compression:                cfg.Snapshots.Borg.Compression,
		Encryption:                 cfg.Snapshots.Borg.Encryption,
		Checks:                     result.Checks,
		BackendMetadata:            metadata,
	}
	localManifestPath := filepath.Join(cfg.StateDir, "manifests", uploadID+".json")
	stageStarted = time.Now()
	if err := WriteSnapshotUploadManifest(localManifestPath, manifest); err != nil {
		return SnapshotPushResult{}, err
	}
	metrics.ManifestWriteDurationMS = durationMSSince(stageStarted)
	result.LocalManifestPath = localManifestPath
	result.Manifest = &manifest
	result.Status = SnapshotStatusSucceeded
	metrics.TotalDurationMS = durationMSSince(started)
	result.Metrics = metrics
	_ = removeBorgInventoryCache(cfg, nodeID)
	return result, nil
}

func (b BorgSnapshotBackend) List(ctx context.Context, input SnapshotListInput) (SnapshotListResult, error) {
	cfg, err := NormalizeConfig(input.Config)
	if err != nil {
		return SnapshotListResult{}, err
	}
	runner := b.runner(cfg)
	nodeID := safeRemoteSegment(firstNonEmpty(input.NodeID, "main"))
	if cached, ok := readBorgInventoryCache(cfg, nodeID, time.Now().UTC()); ok {
		return cached, nil
	}
	archives, out, err := listBorgArchivesWithOutput(ctx, runner)
	if err != nil {
		if result, ok := borgUninitializedSnapshotListResult(cfg, nodeID, out, err); ok {
			return result, nil
		}
		return SnapshotListResult{}, err
	}
	items := make([]SnapshotItem, 0, len(archives))
	for _, archive := range archives {
		name := archive.Name
		if name == "" {
			continue
		}
		if isDirectPendingBorgArchive(name) {
			continue
		}
		if isDirectCanonicalBorgArchive(name) {
			item, ok := directArchiveSnapshotItem(cfg, nodeID, archive)
			if ok {
				items = append(items, item)
			}
			continue
		}
		if !strings.HasPrefix(name, nodeID+"-") {
			continue
		}
		ref := strings.TrimPrefix(name, nodeID+"-")
		items = append(items, SnapshotItem{
			Ref:          ref,
			Backend:      SnapshotBackendBorg,
			Repository:   cfg.Snapshots.Borg.Repository,
			Archive:      name,
			RemotePrefix: "::" + name,
			RemoteURI:    borgRemoteURI(cfg, name),
			Status:       "discovered",
			DiscoveredAt: archive.Time,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].DiscoveredAt.Equal(items[j].DiscoveredAt) {
			return items[i].Archive < items[j].Archive
		}
		return items[i].DiscoveredAt.Before(items[j].DiscoveredAt)
	})
	result := SnapshotListResult{
		Status:     SnapshotListStatusEmpty,
		NodeID:     nodeID,
		Backend:    SnapshotBackendBorg,
		Repository: cfg.Snapshots.Borg.Repository,
		RemoteRoot: borgRemoteURI(cfg, ""),
		Snapshots:  items,
	}
	if len(items) > 0 {
		result.Status = SnapshotListStatusOK
		latest := items[len(items)-1]
		result.Latest = &latest
	}
	_ = writeBorgInventoryCache(cfg, nodeID, result, time.Now().UTC())
	return result, nil
}

func listBorgArchives(ctx context.Context, runner BorgCommandRunner) ([]borgArchiveItem, error) {
	archives, _, err := listBorgArchivesWithOutput(ctx, runner)
	return archives, err
}

func listBorgArchivesWithOutput(ctx context.Context, runner BorgCommandRunner) ([]borgArchiveItem, []byte, error) {
	out, err := runner.Run(ctx, BorgCommand{Args: []string{"list", "--json"}})
	if err != nil {
		return nil, out, err
	}
	return parseBorgArchiveList(out), out, nil
}

func borgUninitializedSnapshotListResult(cfg Config, nodeID string, output []byte, err error) (SnapshotListResult, bool) {
	if !isBorgRepositoryUninitializedError(output, err) {
		return SnapshotListResult{}, false
	}
	message := strings.TrimSpace(string(output))
	if message == "" && err != nil {
		message = err.Error()
	}
	return SnapshotListResult{
		Status:     SnapshotListStatusUninitialized,
		NodeID:     nodeID,
		Backend:    SnapshotBackendBorg,
		Repository: cfg.Snapshots.Borg.Repository,
		RemoteRoot: borgRemoteURI(cfg, ""),
		Snapshots:  []SnapshotItem{},
		Code:       SnapshotBackendUninitializedCode,
		Error:      message,
		RepairHint: "Initialize the configured Borg repository with `loom cloud snapshot backend init --confirm` before pushing or listing snapshots.",
	}, true
}

func isBorgRepositoryUninitializedError(output []byte, err error) bool {
	if err == nil && len(output) == 0 {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(string(output)) + " " + errString(err))
	if !strings.Contains(text, "repository") {
		return false
	}
	for _, marker := range []string{
		"does not exist",
		"not found",
		"not a valid repository",
		"is not valid",
		"is not initialized",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (b BorgSnapshotBackend) Verify(ctx context.Context, input SnapshotVerifyInput) (SnapshotVerifyResult, error) {
	now := normalizeNow(input.Now)
	cfg, err := NormalizeConfig(input.Config)
	if err != nil {
		return SnapshotVerifyResult{}, err
	}
	options, err := NormalizeSnapshotVerifyOptions(input.Ref, input.SnapshotVerifyOptions)
	if err != nil {
		return SnapshotVerifyResult{}, err
	}
	runner := b.runner(cfg)
	if options.Profile == SnapshotVerifyProfileRollingRepository {
		result := SnapshotVerifyResult{
			Status:             SnapshotStatusFailed,
			Profile:            options.Profile,
			MaxDurationSeconds: options.MaxDurationSeconds,
			Backend:            SnapshotBackendBorg,
			Repository:         cfg.Snapshots.Borg.Repository,
			RemoteURI:          borgRemoteURI(cfg, ""),
			CheckedAt:          now(),
			Checks:             map[string]string{},
		}
		_, err := runner.Run(ctx, BorgCommand{Args: []string{"check", "--repository-only", "--max-duration", strconv.FormatInt(options.MaxDurationSeconds, 10)}})
		if err != nil {
			result.Checks["borg_repository_check"] = SnapshotStatusFailed
			result.Errors = append(result.Errors, err.Error())
			return result, nil
		}
		result.Checks["borg_repository_check"] = SnapshotStatusSucceeded
		result.Coverage = SnapshotVerifyCoverageRepositoryTimeBounded
		result.Status = SnapshotStatusSucceeded
		return result, nil
	}
	var item SnapshotItem
	if options.Profile == SnapshotVerifyProfileArchiveData {
		archive := strings.TrimSpace(input.Ref)
		item = SnapshotItem{
			Ref:          archive,
			Backend:      SnapshotBackendBorg,
			Repository:   cfg.Snapshots.Borg.Repository,
			Archive:      archive,
			RemotePrefix: "::" + archive,
			RemoteURI:    borgRemoteURI(cfg, archive),
			Status:       "requested",
			DiscoveredAt: now(),
		}
	} else {
		item, err = b.resolveSnapshotRef(ctx, cfg, input.NodeID, input.Ref)
		if err != nil {
			return SnapshotVerifyResult{}, err
		}
	}
	result := SnapshotVerifyResult{
		Status:       SnapshotStatusFailed,
		Profile:      options.Profile,
		Ref:          item.Ref,
		Backend:      SnapshotBackendBorg,
		Repository:   cfg.Snapshots.Borg.Repository,
		Archive:      item.Archive,
		RemotePrefix: item.RemotePrefix,
		RemoteURI:    item.RemoteURI,
		CheckedAt:    now(),
		Checks:       map[string]string{},
	}
	checkArgs := []string{"check", "--archives-only", "::" + item.Archive}
	coverage := SnapshotVerifyCoverageArchiveMetadata
	switch options.Profile {
	case SnapshotVerifyProfileArchiveData:
		coverage = SnapshotVerifyCoverageArchiveData
		checkArgs = []string{"check", "--archives-only", "--verify-data", "::" + item.Archive}
	}
	if _, err := runner.Run(ctx, BorgCommand{Args: []string{"info", "--json", "::" + item.Archive}}); err != nil {
		result.Checks["borg_info"] = SnapshotStatusFailed
		result.Errors = append(result.Errors, err.Error())
		return result, nil
	}
	result.Checks["borg_info"] = SnapshotStatusSucceeded
	if _, err := runner.Run(ctx, BorgCommand{Args: checkArgs}); err != nil {
		result.Checks["borg_check"] = SnapshotStatusFailed
		result.Errors = append(result.Errors, err.Error())
		return result, nil
	}
	result.Checks["borg_check"] = SnapshotStatusSucceeded
	result.Coverage = coverage
	result.Status = SnapshotStatusSucceeded
	return result, nil
}

func (b BorgSnapshotBackend) Fetch(ctx context.Context, input SnapshotFetchInput) (result SnapshotFetchResult, err error) {
	custody, err := beginRestoreFetchDestination(&input)
	if err != nil {
		return SnapshotFetchResult{}, err
	}
	defer custody.close()
	stage := "fetch_resolution"
	identity := SnapshotFetchResult{Status: SnapshotStatusFailed, Backend: SnapshotBackendBorg}
	defer func() {
		if err != nil {
			err = &snapshotFetchFailure{Stage: stage, Identity: identity, Cause: err}
		}
	}()
	now := normalizeNow(input.Now)
	cfg, err := NormalizeConfig(input.Config)
	if err != nil {
		return SnapshotFetchResult{}, err
	}
	runner := b.runner(cfg)
	item, err := b.resolveSnapshotRef(ctx, cfg, input.NodeID, input.Ref)
	if err != nil {
		return SnapshotFetchResult{}, err
	}
	identity.Ref, identity.Archive = item.Ref, item.Archive
	stage = "fetch_destination"
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
	var directPrepared *backupstrategy.PreparedDirectArchiveManifest
	if isDirectCanonicalBorgArchive(item.Archive) {
		stage = "fetch_authentication"
		prepared, verifyErr := readAuthenticatedDirectArchiveManifest(ctx, runner, cfg, item.Archive)
		if verifyErr != nil {
			return SnapshotFetchResult{}, fmt.Errorf("authenticate direct archive before fetch: %w", verifyErr)
		}
		identity.DirectArchiveManifestSHA256 = prepared.ManifestSHA256
		stage = "fetch_archive_verification"
		if prepared.ManifestV2 != nil {
			prepared.V2UserSymlinkTargets, verifyErr = verifyDirectArchiveV2BeforeFetch(ctx, runner, item.Archive, prepared)
		} else {
			// Keep the established v1 info/check/authenticated-list reader intact.
			prepared, verifyErr = readAndVerifyDirectArchive(ctx, runner, cfg, item.Archive)
		}
		if verifyErr != nil {
			return SnapshotFetchResult{}, fmt.Errorf("verify direct archive before fetch: %w", verifyErr)
		}
		directPrepared = &prepared
	}
	extractArgs := []string{"extract", "::" + item.Archive}
	if directPrepared != nil && directPrepared.ManifestV2 != nil {
		extractArgs = append(extractArgs, directArchiveV2FetchPaths(*directPrepared)...)
	}
	stage = "fetch_extraction"
	out, err := runner.Run(ctx, BorgCommand{Args: extractArgs, Dir: target})
	if err != nil {
		return SnapshotFetchResult{}, err
	}
	stage = "fetch_extracted_verification"
	result = SnapshotFetchResult{
		Status:       SnapshotStatusFailed,
		Ref:          item.Ref,
		Backend:      SnapshotBackendBorg,
		Repository:   cfg.Snapshots.Borg.Repository,
		Archive:      item.Archive,
		RemotePrefix: item.RemotePrefix,
		RemoteURI:    item.RemoteURI,
		TargetDir:    target,
		Copy: CopyResult{
			Command:    "borg extract",
			Source:     item.RemoteURI,
			Dest:       target,
			FinishedAt: now(),
			Output:     strings.TrimSpace(string(out)),
		},
		Checks: map[string]string{},
	}
	if directPrepared != nil {
		if directPrepared.ManifestV2 != nil {
			result.V2UserSymlinkTargets = cloneV2UserSymlinkTargets(directPrepared.V2UserSymlinkTargets)
		}
		extraction, verifyErr := backupstrategy.VerifyExtractedDirectArchive(ctx, target, *directPrepared)
		if verifyErr != nil {
			return SnapshotFetchResult{}, verifyErr
		}
		if directPrepared.ManifestV2 == nil {
			result.DirectArchiveManifest = &directPrepared.Manifest
		}
		result.DirectArchiveManifestSHA256 = directPrepared.ManifestSHA256
		result.ExtractionVerification = &extraction
		result.Checks["remote_direct_archive"] = SnapshotStatusSucceeded
		result.Checks["borg_extraction"] = SnapshotStatusSucceeded
		result.Checks["extracted_direct_archive"] = extraction.Status
		if extraction.Status == SnapshotStatusSucceeded && directPrepared.ManifestV2 != nil {
			provenanceDir := filepath.Join(target, filepath.FromSlash(directPrepared.ManifestV2.ProvenancePackage.ArchivePath))
			provenance, provenanceErr := maintenance.VerifyProvenanceBackupPackage(ctx, provenanceDir, directPrepared.ManifestV2.ProvenancePackage.ManifestSHA256)
			if provenanceErr != nil {
				return SnapshotFetchResult{}, fmt.Errorf("reverify extracted v2 provenance package: %w", provenanceErr)
			}
			expected := directPrepared.ManifestV2.ProvenancePackage
			if provenance.Status != maintenance.VerificationSucceeded || provenance.ManifestSHA256 != expected.ManifestSHA256 || provenance.SchemaHead != expected.SchemaHead || provenance.GraphDigest != expected.GraphDigest || provenance.DumpSizeBytes != expected.DumpSizeBytes || !provenance.CompletedAt.Equal(expected.CompletedAt) {
				extraction.Status = SnapshotStatusFailed
				extraction.Errors = append(extraction.Errors, "extracted v2 provenance package does not match authenticated package identity")
				result.ExtractionVerification = &extraction
				result.Checks["extracted_direct_archive"] = SnapshotStatusFailed
				result.Checks["provenance_package"] = SnapshotStatusFailed
				return result, nil
			}
			extraction.Checks["provenance_package"] = SnapshotStatusSucceeded
			result.ExtractionVerification = &extraction
			result.Checks["provenance_package"] = SnapshotStatusSucceeded
		}
		if extraction.Status == SnapshotStatusSucceeded {
			result.Status = SnapshotStatusSucceeded
		}
		return result, nil
	}
	verification, verifyErr := maintenance.VerifyBackupDirectory(ctx, target)
	if verifyErr != nil {
		return SnapshotFetchResult{}, verifyErr
	}
	result.Verification = verification
	result.Checks["legacy_backup"] = verification.Status
	if verification.Status == maintenance.VerificationSucceeded {
		result.Status = SnapshotStatusSucceeded
	}
	return result, nil
}

func verifyDirectArchiveV2BeforeFetch(ctx context.Context, runner BorgCommandRunner, archive string, prepared backupstrategy.PreparedDirectArchiveManifest) (map[string]string, error) {
	manifest := prepared.ManifestV2
	if manifest == nil {
		return nil, fmt.Errorf("v2 direct archive manifest is required")
	}
	if manifest.ArchiveName != archive {
		return nil, fmt.Errorf("v2 direct archive name does not match authenticated manifest")
	}
	if _, err := runner.Run(ctx, BorgCommand{Args: []string{"info", "--json", "::" + archive}}); err != nil {
		return nil, err
	}
	if _, err := runner.Run(ctx, BorgCommand{Args: []string{"check", "--archives-only", "::" + archive}}); err != nil {
		return nil, err
	}
	return verifyDirectArchiveV2ArchivePaths(ctx, runner, archive, prepared)
}

func verifyDirectArchiveV2ArchivePaths(ctx context.Context, runner BorgCommandRunner, archive string, prepared backupstrategy.PreparedDirectArchiveManifest) (map[string]string, error) {
	manifest := prepared.ManifestV2
	if manifest == nil {
		return nil, fmt.Errorf("v2 direct archive manifest is required")
	}
	type boundary struct {
		path     string
		kind     string
		userRoot bool
	}
	boundaries := make([]boundary, 0, len(manifest.Roots)+2)
	for _, root := range manifest.Roots {
		boundaries = append(boundaries, boundary{path: root.ArchivePath, kind: "directory", userRoot: true})
	}
	boundaries = append(boundaries,
		boundary{path: manifest.OperationalPackage.ArchivePath, kind: "directory"},
		boundary{path: manifest.ProvenancePackage.ArchivePath, kind: "directory"},
	)
	exact := map[string]string{
		backupstrategy.DirectArchiveEvidenceDir:                                                          "directory",
		path.Join(backupstrategy.DirectArchiveEvidenceDir, backupstrategy.DirectArchiveManifestFile):     "file",
		path.Join(backupstrategy.DirectArchiveEvidenceDir, backupstrategy.DirectArchiveManifestHashFile): "file",
	}
	seenBoundaries := make(map[string]struct{}, len(boundaries))
	seenPaths := make(map[string]struct{})
	symlinkTargets := make(map[string]string)
	command := BorgCommand{Args: []string{"list", "--json-lines", "--format", "{path}{type}{mode}{linktarget}{health}", "::" + archive}}
	err := consumeBorgStream(ctx, runner, command, func(reader io.Reader) error {
		decoder := json.NewDecoder(reader)
		for {
			var item struct {
				Path       string `json:"path"`
				Type       string `json:"type"`
				Mode       string `json:"mode"`
				LinkTarget string `json:"linktarget"`
				Healthy    bool   `json:"healthy"`
			}
			if err := decoder.Decode(&item); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				return err
			}
			if !utf8.ValidString(item.Path) || !utf8.ValidString(item.LinkTarget) || item.Path == "" || path.IsAbs(item.Path) || item.Path != path.Clean(item.Path) || item.Path == ".." || strings.HasPrefix(item.Path, "../") {
				return fmt.Errorf("v2 direct archive contains unsafe path %q", item.Path)
			}
			_, kind, modeErr := parseDirectArchiveBorgMode(item.Mode)
			if modeErr != nil {
				return fmt.Errorf("v2 direct archive path %q: %w", item.Path, modeErr)
			}
			if !item.Healthy {
				return fmt.Errorf("v2 direct archive path %q is not healthy", item.Path)
			}
			if _, duplicate := seenPaths[item.Path]; duplicate {
				return fmt.Errorf("v2 direct archive contains duplicate path %q", item.Path)
			}
			seenPaths[item.Path] = struct{}{}
			if want, ok := exact[item.Path]; ok {
				if kind != want {
					return fmt.Errorf("v2 direct archive evidence path %q has unexpected type %q", item.Path, kind)
				}
				continue
			}
			matched := false
			for _, declared := range boundaries {
				if item.Path == declared.path || strings.HasPrefix(item.Path, declared.path+"/") {
					matched = true
					if item.Path == declared.path {
						if kind != declared.kind {
							return fmt.Errorf("v2 direct archive declared path %q has unexpected type %q", item.Path, kind)
						}
						seenBoundaries[declared.path] = struct{}{}
					}
					if kind == "symlink" {
						if !declared.userRoot {
							return fmt.Errorf("v2 direct archive bounded package path %q is a symlink", item.Path)
						}
						if item.LinkTarget == "" || strings.IndexByte(item.LinkTarget, 0) >= 0 {
							return fmt.Errorf("v2 direct archive symlink %q has an invalid target", item.Path)
						}
						// User-data symlinks are restored as inert leaf entries. Their
						// target text is Borg-authenticated and may be absolute or resolve
						// outside the declared root, but no archive entry may appear below
						// the symlink path. The archive-wide check below enforces that leaf
						// boundary before extraction begins.
						symlinkTargets[item.Path] = item.LinkTarget
					}
					break
				}
			}
			if !matched {
				return fmt.Errorf("v2 direct archive contains unexpected payload root or path %q", item.Path)
			}
			if kind == "hardlink" {
				target := filepath.ToSlash(item.LinkTarget)
				if target == "" || path.IsAbs(target) || target != path.Clean(target) || target == ".." || strings.HasPrefix(target, "../") {
					return fmt.Errorf("v2 direct archive hardlink %q has unsafe target %q", item.Path, item.LinkTarget)
				}
				targetAllowed := false
				for _, declared := range boundaries {
					if target == declared.path || strings.HasPrefix(target, declared.path+"/") {
						targetAllowed = true
						break
					}
				}
				if !targetAllowed {
					return fmt.Errorf("v2 direct archive hardlink %q escapes declared payload roots", item.Path)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("verify v2 direct archive path confinement: %w", err)
	}
	for symlinkPath := range symlinkTargets {
		for candidate := range seenPaths {
			if strings.HasPrefix(candidate, symlinkPath+"/") {
				return nil, fmt.Errorf("verify v2 direct archive path confinement: symlink %q is an archive ancestor of %q", symlinkPath, candidate)
			}
		}
	}
	for _, declared := range boundaries {
		if _, ok := seenBoundaries[declared.path]; !ok {
			return nil, fmt.Errorf("v2 direct archive is missing declared path %q", declared.path)
		}
	}
	for evidencePath := range exact {
		if _, ok := seenPaths[evidencePath]; !ok {
			return nil, fmt.Errorf("v2 direct archive is missing evidence path %q", evidencePath)
		}
	}
	return symlinkTargets, nil
}

func directArchiveV2FetchPaths(prepared backupstrategy.PreparedDirectArchiveManifest) []string {
	manifest := prepared.ManifestV2
	if manifest == nil {
		return nil
	}
	paths := make([]string, 0, len(manifest.Roots)+3)
	paths = append(paths, backupstrategy.DirectArchiveEvidenceDir)
	for _, root := range manifest.Roots {
		paths = append(paths, root.ArchivePath)
	}
	paths = append(paths, manifest.OperationalPackage.ArchivePath, manifest.ProvenancePackage.ArchivePath)
	return paths
}

func (b BorgSnapshotBackend) PlanRetention(ctx context.Context, input SnapshotRetentionInput) (SnapshotRetentionPlan, error) {
	return planBorgSnapshotRetention(ctx, b, input)
}

func (b BorgSnapshotBackend) ApplyRetention(ctx context.Context, input SnapshotRetentionApplyInput) (SnapshotRetentionApplyResult, error) {
	return applyBorgSnapshotRetention(ctx, b, input)
}

func (b BorgSnapshotBackend) runner(cfg Config) BorgCommandRunner {
	if b.Runner.Exec != nil || b.Runner.StreamExec != nil {
		b.Runner.Config = cfg
		return b.Runner
	}
	return NewBorgCommandRunner(cfg)
}

func (b BorgSnapshotBackend) resolveSnapshotRef(ctx context.Context, cfg Config, nodeID, ref string) (SnapshotItem, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = "latest"
	}
	if ref == "latest" {
		list, err := b.List(ctx, SnapshotListInput{Config: cfg, NodeID: nodeID})
		if err != nil {
			return SnapshotItem{}, err
		}
		if list.Latest == nil {
			return SnapshotItem{}, fmt.Errorf("no borg cloud snapshots found for node %s", list.NodeID)
		}
		return *list.Latest, nil
	}
	list, err := b.List(ctx, SnapshotListInput{Config: cfg, NodeID: nodeID})
	if err == nil {
		for _, item := range list.Snapshots {
			if item.Ref == ref || item.Archive == ref {
				return item, nil
			}
		}
	}
	node := safeRemoteSegment(firstNonEmpty(nodeID, "main"))
	archive := borgArchiveName(node, safeRemoteSegment(ref))
	return SnapshotItem{
		Ref:          safeRemoteSegment(ref),
		Backend:      SnapshotBackendBorg,
		Repository:   cfg.Snapshots.Borg.Repository,
		Archive:      archive,
		RemotePrefix: "::" + archive,
		RemoteURI:    borgRemoteURI(cfg, archive),
		Status:       "requested",
		DiscoveredAt: time.Now().UTC(),
	}, nil
}

func borgArchiveName(nodeID, snapshotRef string) string {
	return safeRemoteSegment(nodeID) + "-" + safeRemoteSegment(snapshotRef)
}

func ensureBorgRetentionArchive(nodeID, ref, archive string) error {
	expected := borgArchiveName(nodeID, ref)
	if archive != expected {
		return fmt.Errorf("refusing to delete Borg archive %q; expected %q", archive, expected)
	}
	return nil
}

func borgRemoteURI(cfg Config, archive string) string {
	if strings.TrimSpace(archive) == "" {
		return "borg:" + cfg.Snapshots.Borg.Repository
	}
	return "borg:" + cfg.Snapshots.Borg.Repository + "::" + archive
}

type borgCheckRecord struct {
	CheckedAt  time.Time `json:"checked_at"`
	Repository string    `json:"repository"`
	Mode       string    `json:"mode"`
	Archive    string    `json:"archive,omitempty"`
}

func runBorgCheckWithCadence(ctx context.Context, runner BorgCommandRunner, cfg Config, archive string, now time.Time) (bool, string, error) {
	mode := normalizeToken(cfg.Snapshots.Borg.CheckMode)
	switch mode {
	case "disabled", "none", "skip", "skipped":
		return false, "skipped_disabled", nil
	}
	interval := time.Duration(cfg.Snapshots.Borg.CheckIntervalHours) * time.Hour
	recordPath := borgCheckRecordPath(cfg)
	if interval > 0 {
		if record, ok := readBorgCheckRecord(recordPath); ok &&
			record.Repository == cfg.Snapshots.Borg.Repository &&
			normalizeToken(record.Mode) == mode &&
			now.Sub(record.CheckedAt) < interval {
			return false, "skipped_recent", nil
		}
	}
	ran, err := runBorgCheck(ctx, runner, mode, archive)
	if err != nil || !ran {
		return ran, "", err
	}
	_ = writeBorgCheckRecord(recordPath, borgCheckRecord{
		CheckedAt:  now.UTC(),
		Repository: cfg.Snapshots.Borg.Repository,
		Mode:       mode,
		Archive:    archive,
	})
	return true, "", nil
}

func borgCheckRecordPath(cfg Config) string {
	return filepath.Join(cfg.StateDir, "borg", "last-check.json")
}

func readBorgCheckRecord(path string) (borgCheckRecord, bool) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return borgCheckRecord{}, false
	}
	var record borgCheckRecord
	if err := json.Unmarshal(payload, &record); err != nil {
		return borgCheckRecord{}, false
	}
	if record.CheckedAt.IsZero() {
		return borgCheckRecord{}, false
	}
	return record, true
}

func writeBorgCheckRecord(path string, record borgCheckRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(payload, '\n'), 0o600)
}

type borgInventoryCacheFile struct {
	GeneratedAt time.Time          `json:"generated_at"`
	Result      SnapshotListResult `json:"result"`
}

func readBorgInventoryCache(cfg Config, nodeID string, now time.Time) (SnapshotListResult, bool) {
	ttl := time.Duration(cfg.Snapshots.Borg.InventoryCacheTTLSeconds) * time.Second
	if ttl <= 0 {
		return SnapshotListResult{}, false
	}
	path := borgInventoryCachePath(cfg, nodeID)
	payload, err := os.ReadFile(path)
	if err != nil {
		return SnapshotListResult{}, false
	}
	var cached borgInventoryCacheFile
	if err := json.Unmarshal(payload, &cached); err != nil {
		return SnapshotListResult{}, false
	}
	age := now.Sub(cached.GeneratedAt)
	if cached.GeneratedAt.IsZero() || age < 0 || age > ttl {
		return SnapshotListResult{}, false
	}
	result := cached.Result
	result.Cached = true
	result.CachePath = path
	result.CacheAgeSeconds = int64(age.Seconds())
	return result, true
}

func writeBorgInventoryCache(cfg Config, nodeID string, result SnapshotListResult, now time.Time) error {
	if cfg.Snapshots.Borg.InventoryCacheTTLSeconds <= 0 {
		return nil
	}
	path := borgInventoryCachePath(cfg, nodeID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	result.Cached = false
	result.CachePath = ""
	result.CacheAgeSeconds = 0
	payload, err := json.MarshalIndent(borgInventoryCacheFile{GeneratedAt: now.UTC(), Result: result}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(payload, '\n'), 0o600)
}

func removeBorgInventoryCache(cfg Config, nodeID string) error {
	err := os.Remove(borgInventoryCachePath(cfg, nodeID))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func borgInventoryCachePath(cfg Config, nodeID string) string {
	return filepath.Join(cfg.StateDir, "borg", "inventory", safeRemoteSegment(nodeID)+".json")
}

func runBorgCheck(ctx context.Context, runner BorgCommandRunner, mode, archive string) (bool, error) {
	switch normalizeToken(mode) {
	case "", "repository", "repo", "repository-only":
		_, err := runner.Run(ctx, BorgCommand{Args: []string{"check", "--repository-only"}})
		return true, err
	case "archive":
		_, err := runner.Run(ctx, BorgCommand{Args: []string{"check", "::" + archive}})
		return true, err
	case "full":
		_, err := runner.Run(ctx, BorgCommand{Args: []string{"check"}})
		return true, err
	case "disabled", "none", "skip", "skipped":
		return false, nil
	default:
		return false, fmt.Errorf("unsupported borg check mode %q", mode)
	}
}

type borgArchiveItem struct {
	Name string
	Time time.Time
}

func parseBorgArchiveList(payload []byte) []borgArchiveItem {
	var doc struct {
		Archives []struct {
			Name    string `json:"name"`
			Archive string `json:"archive"`
			Time    string `json:"time"`
			Start   string `json:"start"`
		} `json:"archives"`
	}
	if err := json.Unmarshal(payload, &doc); err != nil {
		return nil
	}
	out := make([]borgArchiveItem, 0, len(doc.Archives))
	for _, archive := range doc.Archives {
		name := firstNonEmpty(archive.Name, archive.Archive)
		if name == "" {
			continue
		}
		at := parseBorgTime(firstNonEmpty(archive.Time, archive.Start))
		out = append(out, borgArchiveItem{Name: name, Time: at})
	}
	return out
}

func parseBorgTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000000"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func parseBorgMetadata(payload []byte) (int64, int64, map[string]any) {
	var doc map[string]any
	if err := json.Unmarshal(payload, &doc); err != nil {
		return 0, 0, nil
	}
	metadata := map[string]any{}
	if archive, ok := doc["archive"].(map[string]any); ok {
		metadata["archive"] = archive
	}
	if stats, ok := doc["cache"].(map[string]any); ok {
		metadata["cache"] = stats
	}
	packed := extractNestedInt64(doc, "archive", "stats", "compressed_size")
	if packed == 0 {
		packed = extractNestedInt64(doc, "archive", "stats", "compressed_csize")
	}
	dedup := extractNestedInt64(doc, "archive", "stats", "deduplicated_size")
	if dedup == 0 {
		dedup = extractNestedInt64(doc, "archive", "stats", "deduplicated_csize")
	}
	if archives, ok := doc["archives"].([]any); ok && len(archives) == 1 {
		if archive, ok := archives[0].(map[string]any); ok {
			if packed == 0 {
				packed = extractNestedInt64(archive, "stats", "compressed_size")
			}
			if packed == 0 {
				packed = extractNestedInt64(archive, "stats", "compressed_csize")
			}
			if dedup == 0 {
				dedup = extractNestedInt64(archive, "stats", "deduplicated_size")
			}
			if dedup == 0 {
				dedup = extractNestedInt64(archive, "stats", "deduplicated_csize")
			}
		}
	}
	if len(metadata) == 0 {
		return packed, dedup, nil
	}
	return packed, dedup, metadata
}

func extractNestedInt64(doc map[string]any, path ...string) int64 {
	var current any = doc
	for _, segment := range path {
		obj, ok := current.(map[string]any)
		if !ok {
			return 0
		}
		current = obj[segment]
	}
	switch value := current.(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	case json.Number:
		n, _ := value.Int64()
		return n
	default:
		return 0
	}
}

func fileSHA256(path string) (string, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}
