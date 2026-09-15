package lane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"loom.local/loom/internal/filesystemmeta"
	"loom.local/loom/internal/storagecatalog"
)

type StorageCatalog interface {
	RegisterLaneCustody(context.Context, storagecatalog.LaneCustodyInput) (storagecatalog.EntryDetail, error)
}

type filesystemObservationCatalog interface {
	RegisterFilesystemObservation(context.Context, storagecatalog.RegisterFilesystemObservationInput) (storagecatalog.FilesystemObservation, error)
}

const (
	laneCustodyManifestName          = ".loom-lane-custody.json"
	laneCustodyManifestSchemaVersion = "loom.lane.custody_manifest.v1"
)

type AcceptPhaseError struct {
	Phase string
	Err   error
}

func (err *AcceptPhaseError) Error() string { return err.Phase + ": " + err.Err.Error() }
func (err *AcceptPhaseError) Unwrap() error { return err.Err }

type laneCustodyManifest struct {
	SchemaVersion  string             `json:"schema_version"`
	SourceNodeKey  string             `json:"source_node_key"`
	BatchID        string             `json:"batch_id"`
	AcceptedDate   string             `json:"accepted_date"`
	InventoryHash  string             `json:"inventory_hash"`
	FileCount      int                `json:"file_count"`
	DirectoryCount int                `json:"directory_count"`
	SymlinkCount   int                `json:"symlink_count"`
	TotalBytes     int64              `json:"total_bytes"`
	Entries        []laneCustodyEntry `json:"entries"`
}

type laneCustodyEntry struct {
	RelativePath string    `json:"relative_path"`
	Kind         string    `json:"kind"`
	SizeBytes    int64     `json:"size_bytes,omitempty"`
	Mode         uint32    `json:"mode"`
	ModifiedAt   time.Time `json:"modified_at"`
	SHA256       string    `json:"sha256,omitempty"`
	LinkTarget   string    `json:"link_target,omitempty"`
}

type lanePromotion struct {
	path                    string
	method                  string
	sourceFilesystemID      uint64
	destinationFilesystemID uint64
	idempotent              bool
	stagingPath             string
	stagingRetained         bool
	manifest                laneCustodyManifest
}

func Accept(ctx context.Context, catalog StorageCatalog, input AcceptInput) (AcceptResult, error) {
	started := time.Now()
	metrics := Metrics{}
	if catalog == nil {
		return AcceptResult{}, fmt.Errorf("storage catalog is required")
	}
	normalized, err := normalizeAcceptInput(input)
	if err != nil {
		return AcceptResult{}, err
	}
	promotion, err := promoteLaneCustody(ctx, normalized)
	if err != nil {
		return AcceptResult{}, &AcceptPhaseError{Phase: "promotion", Err: err}
	}
	result := AcceptResult{
		SourceNodeKey:           normalized.SourceNodeKey,
		SourceBoxID:             normalized.SourceBoxID,
		BatchID:                 normalized.BatchID,
		AcceptedPath:            promotion.path,
		VisibleStoragePath:      filepath.ToSlash(filepath.Join("imports", normalized.SourceNodeKey, normalized.AcceptedAt.Format("2006-01-02"), normalized.BatchID)),
		AcceptedAt:              normalized.AcceptedAt,
		PromotionMethod:         promotion.method,
		SourceFilesystemID:      promotion.sourceFilesystemID,
		DestinationFilesystemID: promotion.destinationFilesystemID,
		PromotionIdempotent:     promotion.idempotent,
	}
	if promotion.stagingRetained {
		result.StagingCleanupState = "retained_until_catalog_success"
	} else if promotion.method == "atomic_rename" {
		result.StagingCleanupState = "moved_atomically"
	} else {
		result.StagingCleanupState = "not_present"
	}
	custodyRoot, err := os.OpenRoot(promotion.path)
	if err != nil {
		return result, &AcceptPhaseError{Phase: "catalog", Err: fmt.Errorf("open canonical Lane custody root: %w", err)}
	}
	defer custodyRoot.Close()
	walkStarted := time.Now()
	err = forEachAcceptedEntry(promotion.path, normalized.SkipAppleDouble, func(file acceptedFile) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if file.observation.Kind != filesystemmeta.ObjectKindRegularFile {
			result.ObservationsRecorded += registerLaneObservation(ctx, catalog, normalized, file, "", normalized.AcceptedAt)
			if file.observation.Kind == filesystemmeta.ObjectKindDirectory || file.observation.Kind == filesystemmeta.ObjectKindPackage {
				result.DirectoriesObserved++
			}
			result.FilesSkipped++
			return nil
		}
		stageStarted := time.Now()
		checksum, size, err := fileSHA256Root(custodyRoot, filepath.FromSlash(file.relativePath))
		metrics.AcceptedHashDurationMS += durationMSSince(stageStarted)
		metrics.AcceptedHashOperations++
		if err != nil {
			return err
		}
		stageStarted = time.Now()
		detail, err := catalog.RegisterLaneCustody(ctx, storagecatalog.LaneCustodyInput{
			CustodyNodeKey:        normalized.CustodyNodeKey,
			SourceNodeKey:         normalized.SourceNodeKey,
			SourceBoxID:           normalized.SourceBoxID,
			BatchID:               normalized.BatchID,
			RelativeLanePath:      file.relativePath,
			FileSizeBytes:         size,
			ChecksumAlgorithm:     "sha256",
			ChecksumValue:         checksum,
			FinalCustodyPath:      file.absolutePath,
			AcceptedAt:            normalized.AcceptedAt,
			FilesystemObservation: &file.observation,
		})
		metrics.AcceptedRegisterDurationMS += durationMSSince(stageStarted)
		metrics.AcceptedRegisterOperations++
		if err != nil {
			return err
		}
		result.FilesCataloged++
		result.ObservationsRecorded += registerLaneObservation(ctx, catalog, normalized, file, detail.Entry.StorageEntryID, normalized.AcceptedAt)
		result.TotalBytes += size
		if len(result.StorageEntryIDs) < 1000 {
			result.StorageEntryIDs = append(result.StorageEntryIDs, detail.Entry.StorageEntryID)
		} else {
			result.StorageEntryIDsTruncated = true
		}
		return nil
	})
	walkDuration := durationMSSince(walkStarted)
	metrics.AcceptedScanDurationMS = walkDuration - metrics.AcceptedHashDurationMS - metrics.AcceptedRegisterDurationMS
	if metrics.AcceptedScanDurationMS < 0 {
		metrics.AcceptedScanDurationMS = 0
	}
	if err != nil {
		return result, &AcceptPhaseError{Phase: "catalog", Err: err}
	}
	sort.Strings(result.StorageEntryIDs)
	if promotion.stagingRetained {
		result.StagingCleanupState = "retained_for_transport_cleanup"
	}
	metrics.TotalDurationMS = durationMSSince(started)
	result.Metrics = metrics
	return result, nil
}

func AcceptBundle(ctx context.Context, catalog StorageCatalog, input BundleAcceptInput) (BundleAcceptResult, error) {
	if catalog == nil {
		return BundleAcceptResult{}, fmt.Errorf("storage catalog is required")
	}
	unpack, err := UnpackBundle(input)
	if err != nil {
		return BundleAcceptResult{}, err
	}
	catalogResult, err := Accept(ctx, catalog, catalogAcceptInputForBundle(input, unpack.AcceptedPath))
	if err != nil {
		return BundleAcceptResult{Unpack: unpack}, err
	}
	if catalogResult.FilesCataloged != unpack.FileCount || catalogResult.TotalBytes != unpack.TotalBytes {
		return BundleAcceptResult{Unpack: unpack, Catalog: catalogResult}, fmt.Errorf("cataloged Lane bundle tree does not match manifest totals")
	}
	return BundleAcceptResult{Unpack: unpack, Catalog: catalogResult}, nil
}

func catalogAcceptInputForBundle(input BundleAcceptInput, acceptedPath string) AcceptInput {
	return AcceptInput{
		AcceptedPath: acceptedPath, RemoteRoot: input.RemoteRoot, TrustedRemoteRoot: input.TrustedRemoteRoot,
		ImportsRoot: input.ImportsRoot, SourceNodeKey: input.SourceNodeKey, SourceBoxID: input.SourceBoxID,
		BatchID: input.BatchID, AcceptedDate: input.AcceptedDate, CustodyNodeKey: input.CustodyNodeKey,
		AcceptedAt: input.AcceptedAt, SkipAppleDouble: false, AllowCrossDevicePromotion: input.AllowCrossDevicePromotion,
		DeviceID: input.DeviceID, AvailableBytes: input.AvailableBytes, Rename: input.Rename, BeforeCopyEntry: input.BeforeCopyEntry,
	}
}

func normalizeAcceptInput(input AcceptInput) (AcceptInput, error) {
	input.AcceptedPath = strings.TrimSpace(input.AcceptedPath)
	if input.AcceptedPath == "" {
		return AcceptInput{}, fmt.Errorf("accepted_path is required")
	}
	absolute, err := filepath.Abs(input.AcceptedPath)
	if err != nil {
		return AcceptInput{}, fmt.Errorf("resolve accepted_path: %w", err)
	}
	input.AcceptedPath = filepath.Clean(absolute)
	if strings.TrimSpace(input.RemoteRoot) == "" {
		input.RemoteRoot = DefaultRemoteRoot
	}
	var remoteRoot string
	if input.TrustedRemoteRoot {
		remoteRoot, err = normalizeTrustedLaneRuntimeRoot(input.RemoteRoot)
	} else {
		remoteRoot, err = normalizeRemoteRoot(input.RemoteRoot)
	}
	if err != nil {
		return AcceptInput{}, err
	}
	input.RemoteRoot = remoteRoot
	rawSourceNode := strings.TrimSpace(input.SourceNodeKey)
	input.SourceNodeKey = safeToken(rawSourceNode, "source-node")
	if input.SourceNodeKey == "" || input.SourceNodeKey != rawSourceNode {
		return AcceptInput{}, fmt.Errorf("source_node_key is required")
	}
	input.SourceBoxID = strings.TrimSpace(input.SourceBoxID)
	rawBatchID := strings.TrimSpace(input.BatchID)
	input.BatchID = safeToken(rawBatchID, "batch")
	if input.BatchID == "" || input.BatchID != rawBatchID {
		return AcceptInput{}, fmt.Errorf("batch_id is required")
	}
	input.CustodyNodeKey = strings.TrimSpace(input.CustodyNodeKey)
	if input.CustodyNodeKey == "" {
		input.CustodyNodeKey = "main"
	}
	requestedDate := strings.TrimSpace(input.AcceptedDate)
	if input.AcceptedAt.IsZero() {
		if requestedDate != "" {
			parsed, err := time.Parse("2006-01-02", requestedDate)
			if err != nil {
				return AcceptInput{}, fmt.Errorf("accepted_date must use YYYY-MM-DD: %w", err)
			}
			input.AcceptedAt = parsed.UTC()
		} else {
			input.AcceptedAt = time.Now().UTC()
		}
	} else {
		input.AcceptedAt = input.AcceptedAt.UTC()
		if requestedDate != "" && requestedDate != input.AcceptedAt.Format("2006-01-02") {
			return AcceptInput{}, fmt.Errorf("accepted_at and accepted_date must identify the same day")
		}
	}
	input.AcceptedDate = input.AcceptedAt.Format("2006-01-02")
	stagingBatch := filepath.Join(input.RemoteRoot, "staging", input.SourceNodeKey, input.BatchID)
	stagingTree := filepath.Join(stagingBatch, "tree")
	if input.AcceptedPath != stagingBatch && input.AcceptedPath != stagingTree {
		legacyAccepted := filepath.Join(input.RemoteRoot, "accepted", input.SourceNodeKey, input.AcceptedDate, input.BatchID)
		if input.AcceptedPath == legacyAccepted {
			return AcceptInput{}, &AcceptPhaseError{Phase: "obsolete_accepted_migration", Err: fmt.Errorf("obsolete accepted Lane custody at %s requires explicit historical migration", legacyAccepted)}
		}
		return AcceptInput{}, fmt.Errorf("accepted_path must be the current Lane staging batch %s or verified bundle tree %s", stagingBatch, stagingTree)
	}
	importsRoot := strings.TrimSpace(input.ImportsRoot)
	if importsRoot == "" {
		return AcceptInput{}, fmt.Errorf("imports_root is required from trusted runtime configuration")
	}
	importsRoot, err = filepath.Abs(importsRoot)
	if err != nil {
		return AcceptInput{}, fmt.Errorf("resolve imports_root: %w", err)
	}
	importsRoot, err = resolveExistingPathPrefix(importsRoot)
	if err != nil {
		return AcceptInput{}, fmt.Errorf("resolve imports_root existing prefix: %w", err)
	}
	input.ImportsRoot = filepath.Clean(importsRoot)
	if pathsOverlap(input.RemoteRoot, input.ImportsRoot) {
		return AcceptInput{}, fmt.Errorf("Lane runtime root and imports root must not overlap")
	}
	return input, nil
}

func promoteLaneCustody(ctx context.Context, input AcceptInput) (lanePromotion, error) {
	target := filepath.Join(input.ImportsRoot, input.SourceNodeKey, input.AcceptedDate, input.BatchID)
	anchor := string(filepath.Separator)
	if volume := filepath.VolumeName(input.ImportsRoot); volume != "" {
		anchor = volume + string(filepath.Separator)
	}
	if err := mkdirAllNoSymlink(anchor, filepath.Dir(target), 0o755); err != nil {
		return lanePromotion{}, fmt.Errorf("prepare imports custody parent: %w", err)
	}
	if err := requireNoSymlinkDirectoryPath(anchor, filepath.Dir(target)); err != nil {
		return lanePromotion{}, fmt.Errorf("validate imports custody parent: %w", err)
	}

	targetInfo, targetErr := os.Lstat(target)
	sourceInfo, sourceErr := os.Lstat(input.AcceptedPath)
	if targetErr == nil {
		if !targetInfo.IsDir() || targetInfo.Mode()&os.ModeSymlink != 0 {
			return lanePromotion{}, fmt.Errorf("canonical imports destination exists and is not a no-follow directory: %s", target)
		}
		committed, err := readAndVerifyLaneCustodyManifest(ctx, target, input)
		if err != nil {
			return lanePromotion{}, fmt.Errorf("canonical imports destination conflicts with committed batch: %w", err)
		}
		promotion := lanePromotion{path: target, method: "existing_committed", idempotent: true, manifest: committed}
		promotion.destinationFilesystemID, err = acceptFilesystemID(input, input.ImportsRoot)
		if err != nil {
			return lanePromotion{}, err
		}
		if sourceErr == nil {
			if !sourceInfo.IsDir() || sourceInfo.Mode()&os.ModeSymlink != 0 {
				return lanePromotion{}, fmt.Errorf("Lane staging source is not a no-follow directory")
			}
			if err := requireNoSymlinkDirectoryPath(filepath.Join(input.RemoteRoot, "staging"), input.AcceptedPath); err != nil {
				return lanePromotion{}, err
			}
			staged, err := buildLaneCustodyManifest(ctx, input.AcceptedPath, input)
			if err != nil {
				return lanePromotion{}, err
			}
			if staged.InventoryHash != committed.InventoryHash {
				return lanePromotion{}, fmt.Errorf("Lane staging source conflicts with already committed imports batch")
			}
			promotion.sourceFilesystemID, err = acceptFilesystemID(input, input.AcceptedPath)
			if err != nil {
				return lanePromotion{}, err
			}
			promotion.stagingPath = input.AcceptedPath
			promotion.stagingRetained = true
		} else if !os.IsNotExist(sourceErr) {
			return lanePromotion{}, sourceErr
		}
		return promotion, nil
	}
	if !os.IsNotExist(targetErr) {
		return lanePromotion{}, targetErr
	}
	if sourceErr != nil {
		return lanePromotion{}, fmt.Errorf("Lane staging source unavailable and no committed imports batch exists: %w", sourceErr)
	}
	if !sourceInfo.IsDir() || sourceInfo.Mode()&os.ModeSymlink != 0 {
		return lanePromotion{}, fmt.Errorf("Lane staging source must be a no-follow directory")
	}
	stagingRoot := filepath.Join(input.RemoteRoot, "staging")
	if err := requireNoSymlinkDirectoryPath(stagingRoot, input.AcceptedPath); err != nil {
		return lanePromotion{}, err
	}
	manifest, err := buildLaneCustodyManifest(ctx, input.AcceptedPath, input)
	if err != nil {
		return lanePromotion{}, err
	}
	lockParent := filepath.Join(input.RemoteRoot, "promotion-locks", input.SourceNodeKey, input.AcceptedDate)
	if err := mkdirAllNoSymlink(input.RemoteRoot, lockParent, 0o700); err != nil {
		return lanePromotion{}, fmt.Errorf("prepare Lane promotion lock root: %w", err)
	}
	lockPath := filepath.Join(lockParent, input.BatchID+".lock")
	lock, err := acquireLanePromotionLock(lockPath, manifest.InventoryHash)
	if err != nil {
		return lanePromotion{}, fmt.Errorf("acquire exclusive Lane promotion lock: %w", err)
	}
	defer lock.close()
	if err := cleanupAbandonedLanePromotions(filepath.Dir(target), input.BatchID); err != nil {
		return lanePromotion{}, fmt.Errorf("clean abandoned Lane promotion: %w", err)
	}
	if err := cleanupLaneCustodyManifestTemps(input.AcceptedPath); err != nil {
		return lanePromotion{}, fmt.Errorf("clean abandoned Lane custody manifest: %w", err)
	}
	if err := ensureLaneCustodyManifest(input.AcceptedPath, manifest); err != nil {
		return lanePromotion{}, err
	}
	if _, err := os.Lstat(target); err == nil {
		return lanePromotion{}, fmt.Errorf("canonical imports destination appeared while promotion was being prepared; retry after inspecting its committed manifest")
	} else if !os.IsNotExist(err) {
		return lanePromotion{}, err
	}
	sourceFilesystemID, err := acceptFilesystemID(input, input.AcceptedPath)
	if err != nil {
		return lanePromotion{}, fmt.Errorf("inspect Lane staging filesystem: %w", err)
	}
	destinationFilesystemID, err := acceptFilesystemID(input, input.ImportsRoot)
	if err != nil {
		return lanePromotion{}, fmt.Errorf("inspect imports filesystem: %w", err)
	}
	promotion := lanePromotion{
		path: target, sourceFilesystemID: sourceFilesystemID, destinationFilesystemID: destinationFilesystemID,
		stagingPath: input.AcceptedPath, manifest: manifest,
	}
	rename := input.Rename
	if rename == nil {
		rename = os.Rename
	}
	if sourceFilesystemID == destinationFilesystemID {
		if err := rename(input.AcceptedPath, target); err == nil {
			promotion.method = "atomic_rename"
			if _, err := readAndVerifyLaneCustodyManifest(ctx, target, input); err != nil {
				return lanePromotion{}, fmt.Errorf("verify atomically promoted imports batch: %w", err)
			}
			_ = syncDirectory(filepath.Dir(target))
			return promotion, nil
		} else if !errors.Is(err, syscall.EXDEV) || !input.AllowCrossDevicePromotion {
			return lanePromotion{}, fmt.Errorf("atomic Lane promotion failed: %w", err)
		}
	}
	if !input.AllowCrossDevicePromotion {
		return lanePromotion{}, fmt.Errorf("Lane staging and imports are on different filesystems; retry with explicit cross-device promotion after reviewing copy amplification")
	}
	if err := copyVerifyRenameLaneCustody(ctx, input, manifest, target); err != nil {
		return lanePromotion{}, err
	}
	promotion.method = "copy_verify_rename"
	promotion.stagingRetained = true
	return promotion, nil
}

type lanePromotionLock struct {
	file *os.File
}

func acquireLanePromotionLock(pathValue, inventoryHash string) (*lanePromotionLock, error) {
	fd, err := unix.Open(pathValue, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), pathValue)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open Lane promotion lock")
	}
	closeOnError := func(err error) (*lanePromotionLock, error) {
		_ = file.Close()
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return closeOnError(err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		return closeOnError(fmt.Errorf("Lane promotion lock must be a single-link regular file"))
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return closeOnError(fmt.Errorf("another live process owns this batch promotion"))
		}
		return closeOnError(err)
	}
	if err := file.Chmod(0o600); err != nil {
		return closeOnError(err)
	}
	if err := file.Truncate(0); err != nil {
		return closeOnError(err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return closeOnError(err)
	}
	if _, err := fmt.Fprintf(file, "%s\n", inventoryHash); err != nil {
		return closeOnError(err)
	}
	if err := file.Sync(); err != nil {
		return closeOnError(err)
	}
	return &lanePromotionLock{file: file}, nil
}

func (lock *lanePromotionLock) close() {
	if lock == nil || lock.file == nil {
		return
	}
	_ = unix.Flock(int(lock.file.Fd()), unix.LOCK_UN)
	_ = lock.file.Close()
}

func cleanupAbandonedLanePromotions(parent, batchID string) error {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return err
	}
	defer root.Close()
	prefix := "." + batchID + ".promoting-"
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("abandoned promotion candidate %q is not a no-follow directory", entry.Name())
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() && strings.Contains(entry.Name(), "-"+laneCustodyManifestName+".tmp-") {
			if err := root.Remove(entry.Name()); err != nil {
				return err
			}
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("abandoned promotion candidate %q is not a no-follow directory", entry.Name())
		}
		if err := root.RemoveAll(entry.Name()); err != nil {
			return err
		}
	}
	return nil
}

func cleanupLaneCustodyManifestTemps(rootPath string) error {
	parent := filepath.Dir(filepath.Clean(rootPath))
	entries, err := os.ReadDir(parent)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return err
	}
	defer root.Close()
	prefix := laneCustodyManifestTempPrefix(rootPath)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("abandoned custody manifest candidate %q is not a regular file", entry.Name())
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("abandoned custody manifest candidate %q is not a regular file", entry.Name())
		}
		if err := root.Remove(entry.Name()); err != nil {
			return err
		}
	}
	return nil
}

func laneCustodyManifestTempPrefix(rootPath string) string {
	return filepath.Base(filepath.Clean(rootPath)) + "-" + laneCustodyManifestName + ".tmp-"
}

func ensureLaneCustodyManifest(root string, manifest laneCustodyManifest) error {
	marker := filepath.Join(root, laneCustodyManifestName)
	if _, err := os.Lstat(marker); err == nil {
		existing, err := readLaneCustodyManifest(marker)
		if err != nil || existing.InventoryHash != manifest.InventoryHash || existing.SourceNodeKey != manifest.SourceNodeKey || existing.BatchID != manifest.BatchID || existing.AcceptedDate != manifest.AcceptedDate {
			return fmt.Errorf("reserved Lane custody manifest path conflicts with batch payload")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := writeLaneCustodyManifest(marker, manifest); err != nil {
		if os.IsExist(err) {
			existing, readErr := readLaneCustodyManifest(marker)
			if readErr == nil && existing.InventoryHash == manifest.InventoryHash && existing.SourceNodeKey == manifest.SourceNodeKey && existing.BatchID == manifest.BatchID && existing.AcceptedDate == manifest.AcceptedDate {
				return nil
			}
		}
		return fmt.Errorf("write Lane custody manifest: %w", err)
	}
	if err := syncDirectory(root); err != nil {
		return err
	}
	return nil
}

func buildLaneCustodyManifest(ctx context.Context, root string, input AcceptInput) (laneCustodyManifest, error) {
	manifest := laneCustodyManifest{
		SchemaVersion: laneCustodyManifestSchemaVersion,
		SourceNodeKey: input.SourceNodeKey,
		BatchID:       input.BatchID,
		AcceptedDate:  input.AcceptedDate,
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return laneCustodyManifest{}, err
	}
	defer rootHandle.Close()
	err = fs.WalkDir(rootHandle.FS(), ".", func(pathValue string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if pathValue == "." {
			return nil
		}
		rel := filepath.ToSlash(pathValue)
		if rel == laneCustodyManifestName {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if err := validateBundleRelativePath(rel); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		item := laneCustodyEntry{RelativePath: rel, Mode: uint32(info.Mode().Perm()), ModifiedAt: info.ModTime().UTC()}
		switch {
		case info.IsDir():
			item.Kind = filesystemmeta.ObjectKindDirectory
			manifest.DirectoryCount++
		case info.Mode().IsRegular():
			item.Kind = filesystemmeta.ObjectKindRegularFile
			item.SHA256, item.SizeBytes, err = fileSHA256Root(rootHandle, filepath.FromSlash(rel))
			if err != nil {
				return err
			}
			manifest.FileCount++
			if item.SizeBytes > math.MaxInt64-manifest.TotalBytes {
				return fmt.Errorf("Lane custody byte total overflows int64")
			}
			manifest.TotalBytes += item.SizeBytes
		case info.Mode()&os.ModeSymlink != 0:
			item.Kind = filesystemmeta.ObjectKindSymlink
			item.LinkTarget, err = rootHandle.Readlink(filepath.FromSlash(rel))
			if err != nil {
				return err
			}
			manifest.SymlinkCount++
		default:
			return fmt.Errorf("Lane custody entry %q has unsupported type %s", rel, info.Mode().Type())
		}
		manifest.Entries = append(manifest.Entries, item)
		return nil
	})
	if err != nil {
		return laneCustodyManifest{}, err
	}
	sort.Slice(manifest.Entries, func(i, j int) bool { return manifest.Entries[i].RelativePath < manifest.Entries[j].RelativePath })
	manifest.InventoryHash, err = hashLaneCustodyEntries(manifest.Entries)
	return manifest, err
}

func hashLaneCustodyEntries(entries []laneCustodyEntry) (string, error) {
	digest := sha256.New()
	if err := json.NewEncoder(digest).Encode(entries); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

func writeLaneCustodyManifest(pathValue string, manifest laneCustodyManifest) (err error) {
	parent := filepath.Dir(pathValue)
	tempParent := filepath.Dir(parent)
	file, err := os.CreateTemp(tempParent, laneCustodyManifestTempPrefix(parent))
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer func() {
		_ = os.Remove(tempPath)
	}()
	if err = file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(manifest); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Link(tempPath, pathValue); err != nil {
		return err
	}
	return syncDirectory(parent)
}

func readLaneCustodyManifest(pathValue string) (laneCustodyManifest, error) {
	fd, err := unix.Open(pathValue, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return laneCustodyManifest{}, err
	}
	file := os.NewFile(uintptr(fd), pathValue)
	if file == nil {
		_ = unix.Close(fd)
		return laneCustodyManifest{}, fmt.Errorf("open Lane custody manifest")
	}
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return laneCustodyManifest{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		return laneCustodyManifest{}, fmt.Errorf("Lane custody manifest must be a no-follow single-link regular file")
	}
	var manifest laneCustodyManifest
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return laneCustodyManifest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return laneCustodyManifest{}, fmt.Errorf("Lane custody manifest has trailing JSON content")
	}
	if manifest.SchemaVersion != laneCustodyManifestSchemaVersion {
		return laneCustodyManifest{}, fmt.Errorf("unsupported Lane custody manifest schema %q", manifest.SchemaVersion)
	}
	want, err := hashLaneCustodyEntries(manifest.Entries)
	if err != nil || want != manifest.InventoryHash {
		return laneCustodyManifest{}, fmt.Errorf("Lane custody manifest inventory hash mismatch")
	}
	return manifest, nil
}

func readAndVerifyLaneCustodyManifest(ctx context.Context, root string, input AcceptInput) (laneCustodyManifest, error) {
	manifest, err := readLaneCustodyManifest(filepath.Join(root, laneCustodyManifestName))
	if err != nil {
		return laneCustodyManifest{}, err
	}
	if manifest.SourceNodeKey != input.SourceNodeKey || manifest.BatchID != input.BatchID || manifest.AcceptedDate != input.AcceptedDate {
		return laneCustodyManifest{}, fmt.Errorf("Lane custody manifest identity mismatch")
	}
	observed, err := buildLaneCustodyManifest(ctx, root, input)
	if err != nil {
		return laneCustodyManifest{}, err
	}
	if observed.InventoryHash != manifest.InventoryHash || observed.FileCount != manifest.FileCount || observed.DirectoryCount != manifest.DirectoryCount || observed.SymlinkCount != manifest.SymlinkCount || observed.TotalBytes != manifest.TotalBytes {
		return laneCustodyManifest{}, fmt.Errorf("Lane custody tree no longer matches committed manifest")
	}
	return manifest, nil
}

func copyVerifyRenameLaneCustody(ctx context.Context, input AcceptInput, manifest laneCustodyManifest, target string) error {
	available := input.AvailableBytes
	if available == nil {
		available = bundleFilesystemFreeBytes
	}
	availableBytes, err := available(input.ImportsRoot)
	if err != nil {
		return fmt.Errorf("check imports free space: %w", err)
	}
	required, err := lanePromotionRequiredBytes(manifest.TotalBytes, len(manifest.Entries))
	if err != nil {
		return err
	}
	if availableBytes < required {
		return fmt.Errorf("insufficient imports free space for explicit Lane cross-device promotion: required %d bytes including safety margin, available %d", required, availableBytes)
	}
	parent := filepath.Dir(target)
	temporary, err := os.MkdirTemp(parent, "."+input.BatchID+".promoting-")
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(temporary)
		}
	}()
	if err := copyLaneCustodyEntries(ctx, input, manifest, temporary); err != nil {
		return err
	}
	if err := writeLaneCustodyManifest(filepath.Join(temporary, laneCustodyManifestName), manifest); err != nil {
		return err
	}
	if _, err := readAndVerifyLaneCustodyManifest(ctx, temporary, input); err != nil {
		return fmt.Errorf("verify copied Lane custody tree: %w", err)
	}
	if err := syncDirectory(temporary); err != nil {
		return err
	}
	rename := input.Rename
	if rename == nil {
		rename = os.Rename
	}
	if err := rename(temporary, target); err != nil {
		if _, statErr := os.Lstat(target); statErr == nil {
			return fmt.Errorf("imports destination appeared during Lane promotion; no merge was attempted: %w", err)
		}
		return fmt.Errorf("commit verified Lane custody tree: %w", err)
	}
	cleanup = false
	_ = syncDirectory(parent)
	return nil
}

func lanePromotionRequiredBytes(payloadBytes int64, entryCount int) (int64, error) {
	if payloadBytes < 0 || entryCount < 0 || uint64(entryCount)+1 > uint64(math.MaxInt64/4096) {
		return 0, fmt.Errorf("Lane promotion size proof overflows int64")
	}
	metadataBytes := int64(entryCount+1) * 4096
	if metadataBytes > math.MaxInt64-payloadBytes-64*1024*1024 {
		return 0, fmt.Errorf("Lane promotion size proof overflows int64")
	}
	return payloadBytes + metadataBytes + 64*1024*1024, nil
}

func copyLaneCustodyEntries(ctx context.Context, input AcceptInput, manifest laneCustodyManifest, destination string) error {
	sourceRoot, err := os.OpenRoot(input.AcceptedPath)
	if err != nil {
		return err
	}
	defer sourceRoot.Close()
	destinationRoot, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer destinationRoot.Close()
	for index, entry := range manifest.Entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if input.BeforeCopyEntry != nil {
			if err := input.BeforeCopyEntry(index, entry.RelativePath); err != nil {
				return err
			}
		}
		relative := filepath.FromSlash(entry.RelativePath)
		switch entry.Kind {
		case filesystemmeta.ObjectKindDirectory:
			if err := destinationRoot.Mkdir(relative, os.FileMode(entry.Mode)); err != nil {
				return err
			}
			if err := destinationRoot.Chmod(relative, os.FileMode(entry.Mode)); err != nil {
				return err
			}
		case filesystemmeta.ObjectKindRegularFile:
			if err := copyLaneCustodyFile(sourceRoot, destinationRoot, relative, os.FileMode(entry.Mode)); err != nil {
				return err
			}
			if err := destinationRoot.Chtimes(relative, entry.ModifiedAt, entry.ModifiedAt); err != nil {
				return err
			}
		case filesystemmeta.ObjectKindSymlink:
			if err := destinationRoot.Symlink(entry.LinkTarget, relative); err != nil {
				return err
			}
			if err := setLaneCustodySymlinkMtime(destinationRoot, relative, entry.LinkTarget, entry.ModifiedAt); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported Lane custody entry kind %q", entry.Kind)
		}
	}
	for index := len(manifest.Entries) - 1; index >= 0; index-- {
		entry := manifest.Entries[index]
		if entry.Kind != filesystemmeta.ObjectKindDirectory {
			continue
		}
		if err := destinationRoot.Chtimes(filepath.FromSlash(entry.RelativePath), entry.ModifiedAt, entry.ModifiedAt); err != nil {
			return err
		}
	}
	return nil
}

func setLaneCustodySymlinkMtime(root *os.Root, relative, expectedTarget string, modifiedAt time.Time) error {
	parentRelative := filepath.Dir(relative)
	parent, err := root.OpenFile(parentRelative, os.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open confined Lane symlink parent %q: %w", filepath.ToSlash(relative), err)
	}
	defer parent.Close()
	base := filepath.Base(relative)
	var before unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), base, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("stat copied Lane symlink %q: %w", filepath.ToSlash(relative), err)
	}
	if before.Mode&unix.S_IFMT != unix.S_IFLNK {
		return fmt.Errorf("copied Lane symlink %q changed before timestamp restore", filepath.ToSlash(relative))
	}
	target, err := root.Readlink(relative)
	if err != nil || target != expectedTarget {
		return fmt.Errorf("copied Lane symlink %q changed before timestamp restore", filepath.ToSlash(relative))
	}
	timestamp := unix.NsecToTimespec(modifiedAt.UnixNano())
	if err := unix.UtimesNanoAt(int(parent.Fd()), base, []unix.Timespec{timestamp, timestamp}, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("restore copied Lane symlink timestamp %q: %w", filepath.ToSlash(relative), err)
	}
	var after unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), base, &after, unix.AT_SYMLINK_NOFOLLOW); err != nil || before.Dev != after.Dev || before.Ino != after.Ino {
		return fmt.Errorf("copied Lane symlink %q changed during timestamp restore", filepath.ToSlash(relative))
	}
	target, err = root.Readlink(relative)
	if err != nil || target != expectedTarget {
		return fmt.Errorf("copied Lane symlink %q changed during timestamp restore", filepath.ToSlash(relative))
	}
	return nil
}

func copyLaneCustodyFile(sourceRoot, targetRoot *os.Root, relative string, mode os.FileMode) error {
	info, err := sourceRoot.Lstat(relative)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("Lane custody source %s changed before copy", relative)
	}
	in, err := sourceRoot.Open(relative)
	if err != nil {
		return err
	}
	defer in.Close()
	openedInfo, err := in.Stat()
	if err != nil || !os.SameFile(info, openedInfo) || !openedInfo.Mode().IsRegular() {
		return fmt.Errorf("Lane custody source %s changed while opening", relative)
	}
	out, err := targetRoot.OpenFile(relative, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	if err := out.Chmod(mode.Perm()); err != nil {
		_ = out.Close()
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func acceptFilesystemID(input AcceptInput, pathValue string) (uint64, error) {
	if input.DeviceID != nil {
		return input.DeviceID(pathValue)
	}
	var stat unix.Stat_t
	if err := unix.Stat(pathValue, &stat); err != nil {
		return 0, err
	}
	return uint64(stat.Dev), nil
}

func pathsOverlap(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	return left == right || localPathWithin(left, right) || localPathWithin(right, left)
}

func resolveExistingPathPrefix(pathValue string) (string, error) {
	pathValue = filepath.Clean(pathValue)
	current := pathValue
	var missing []string
	for {
		if _, err := os.Lstat(current); err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return resolved, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no existing ancestor for %s", pathValue)
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func removeLaneStagingTree(remoteRoot, stagingPath string) error {
	stagingRoot := filepath.Join(remoteRoot, "staging")
	relative, err := filepath.Rel(stagingRoot, stagingPath)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("Lane staging cleanup path escapes runtime staging")
	}
	root, err := os.OpenRoot(stagingRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.RemoveAll(relative)
}

func localPathWithin(root, candidate string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	if err != nil {
		return false
	}
	return rel != "." && rel != "" && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}

type acceptedFile struct {
	absolutePath string
	relativePath string
	observation  filesystemmeta.Observation
}

func forEachAcceptedEntry(root string, skipAppleDouble bool, visit func(acceptedFile) error) error {
	if visit == nil {
		return fmt.Errorf("accepted entry visitor is required")
	}
	return filepath.WalkDir(root, func(pathValue string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if pathValue == root {
			return nil
		}
		rel, err := filepath.Rel(root, pathValue)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		name := entry.Name()
		if ignoredAcceptedEntry(rel, name, skipAppleDouble) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if _, err := storagecatalog.NormalizeLogicalPath(rel); err != nil {
			return fmt.Errorf("accepted path %q is unsafe: %w", rel, err)
		}
		observation, err := filesystemmeta.DetectPath(pathValue, filesystemmeta.DetectOptions{
			RootPath:          root,
			IncludeXattrNames: true,
		})
		if err != nil {
			return err
		}
		observation = sanitizeAcceptedLaneObservation(observation)
		file := acceptedFile{
			absolutePath: filepath.Clean(pathValue),
			relativePath: rel,
			observation:  observation,
		}
		if err := visit(file); err != nil {
			return err
		}
		if entry.IsDir() && observation.IsPackage {
			return filepath.SkipDir
		}
		return nil
	})
}

// sanitizeAcceptedLaneObservation prevents the destination filesystem's birth
// time from being relabelled as source creation time. Ordinary Lane acceptance
// and the current bundle manifest both preserve source mtime, but neither
// carries verified source-side birth-time evidence. If such custody evidence is
// added later it must be overlaid explicitly after this destination scan.
func sanitizeAcceptedLaneObservation(observation filesystemmeta.Observation) filesystemmeta.Observation {
	observation.SourceCreatedAt = nil
	observation.SourceCreatedBasis = ""
	return observation
}

func registerLaneObservation(ctx context.Context, catalog StorageCatalog, input AcceptInput, file acceptedFile, storageEntryID string, observedAt time.Time) int {
	observationCatalog, ok := catalog.(filesystemObservationCatalog)
	if !ok {
		return 0
	}
	_, err := observationCatalog.RegisterFilesystemObservation(ctx, storagecatalog.FilesystemObservationInputFromMeta(storagecatalog.FilesystemObservationInputOptions{
		StorageEntryID: storageEntryID,
		SourceArea:     storagecatalog.SourceAreaLane,
		SourceNodeKey:  input.SourceNodeKey,
		SourceRef:      "lane/" + input.AcceptedAt.Format("2006-01-02") + "/" + input.BatchID,
		LogicalPath:    file.relativePath,
		ObservedAt:     observedAt,
	}, file.observation))
	if err != nil {
		return 0
	}
	return 1
}

func ignoredAcceptedEntry(relativePath, name string, skipAppleDouble bool) bool {
	switch strings.TrimSpace(filepath.ToSlash(relativePath)) {
	case "", ".", "..", laneReadmeControlPath, laneCustodyManifestName:
		return true
	}
	if skipAppleDouble && strings.HasPrefix(name, "._") {
		return true
	}
	return false
}

func fileSHA256(pathValue string) (string, int64, error) {
	file, err := os.Open(pathValue)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func fileSHA256Root(root *os.Root, relative string) (string, int64, error) {
	file, err := root.Open(relative)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", 0, fmt.Errorf("Lane custody source %s is no longer a regular file", relative)
	}
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func safeToken(value, fallback string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "-"))
	value = strings.ReplaceAll(value, "/", "-")
	value = strings.ReplaceAll(value, " ", "-")
	value = strings.Trim(value, "-.")
	if value == "" {
		return ""
	}
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			builder.WriteRune(r)
		}
	}
	out := strings.Trim(builder.String(), "-.")
	if out == "" {
		return fallback
	}
	return out
}
