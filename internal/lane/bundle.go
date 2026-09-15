package lane

import (
	"archive/tar"
	"crypto/sha256"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"loom.local/loom/internal/filesystemmeta"
)

const (
	bundlePAXSchemaKey   = "LOOM.bundle.entry"
	bundlePAXSchemaValue = "v1"
)

func CreateBundle(input CreateBundleInput) (artifact BundleArtifact, err error) {
	input.LanePath = filepath.Clean(strings.TrimSpace(input.LanePath))
	input.PolicyRoot = filepath.Clean(strings.TrimSpace(input.PolicyRoot))
	input.ArtifactRoot = filepath.Clean(strings.TrimSpace(input.ArtifactRoot))
	input.BatchID = strings.TrimSpace(input.BatchID)
	input.SourceNodeKey = strings.TrimSpace(input.SourceNodeKey)
	if input.LanePath == "." || input.ArtifactRoot == "." {
		return BundleArtifact{}, fmt.Errorf("Lane path and bundle artifact root are required")
	}
	if input.PolicyRoot == "." {
		input.PolicyRoot = input.LanePath
	}
	if input.BatchID == "" || safeToken(input.BatchID, "batch") != input.BatchID {
		return BundleArtifact{}, fmt.Errorf("bundle batch ID is invalid")
	}
	if input.SourceNodeKey == "" || safeToken(input.SourceNodeKey, "workspace") != input.SourceNodeKey {
		return BundleArtifact{}, fmt.Errorf("bundle source node key is invalid")
	}
	if input.Plan.Transport.SelectedMode != TransportModeBundleSeed {
		return BundleArtifact{}, fmt.Errorf("bundle creation requires a transfer plan selecting bundle_seed")
	}
	if err := validateTransferPlanEntries(input.Plan); err != nil {
		return BundleArtifact{}, err
	}
	if got := transferInventoryHash(input.Plan); got != input.Plan.InventoryHash {
		return BundleArtifact{}, fmt.Errorf("bundle transfer plan inventory hash mismatch")
	}

	before, err := BuildTransferPlan(input.LanePath, input.PolicyRoot, input.Plan.Profile)
	if err != nil {
		return BundleArtifact{}, fmt.Errorf("re-plan Lane source before bundle creation: %w", err)
	}
	if !samePlannedInventory(input.Plan, before) {
		return BundleArtifact{}, fmt.Errorf("Lane source changed before bundle creation; rebuild the transfer plan")
	}

	if err := os.MkdirAll(input.ArtifactRoot, 0o700); err != nil {
		return BundleArtifact{}, err
	}
	available := input.AvailableBytes
	if available == nil {
		available = bundleFilesystemFreeBytes
	}
	availableBytes, err := available(input.ArtifactRoot)
	if err != nil {
		return BundleArtifact{}, fmt.Errorf("check bundle temporary storage: %w", err)
	}
	if required := input.Plan.Transport.EstimatedTemporaryBytes; required > 0 && availableBytes < required {
		return BundleArtifact{}, fmt.Errorf("insufficient free disk space for Lane bundle: required %d bytes including safety margin, available %d bytes at %s", required, availableBytes, input.ArtifactRoot)
	}

	finalDir := filepath.Join(input.ArtifactRoot, input.BatchID)
	if _, statErr := os.Lstat(finalDir); statErr == nil {
		return BundleArtifact{}, fmt.Errorf("bundle artifact already exists for batch %s", input.BatchID)
	} else if !os.IsNotExist(statErr) {
		return BundleArtifact{}, statErr
	}
	temporaryDir, err := os.MkdirTemp(input.ArtifactRoot, "."+input.BatchID+".tmp-")
	if err != nil {
		return BundleArtifact{}, err
	}
	if err := os.Chmod(temporaryDir, 0o700); err != nil {
		_ = os.RemoveAll(temporaryDir)
		return BundleArtifact{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(temporaryDir)
		}
	}()

	archivePath := filepath.Join(temporaryDir, bundleArchiveFileName)
	archiveBytes, archiveSHA, err := writeBundleArchive(archivePath, input)
	if err != nil {
		return BundleArtifact{}, err
	}
	after, err := BuildTransferPlan(input.LanePath, input.PolicyRoot, input.Plan.Profile)
	if err != nil {
		return BundleArtifact{}, fmt.Errorf("re-plan Lane source after bundle creation: %w", err)
	}
	if !samePlannedInventory(input.Plan, after) {
		return BundleArtifact{}, fmt.Errorf("Lane source changed during bundle creation; incomplete bundle was discarded")
	}

	createdAt := input.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	manifest := BundleManifest{
		SchemaVersion:      BundleManifestSchemaVersion,
		BatchID:            input.BatchID,
		SourcePath:         input.LanePath,
		SourceNodeKey:      input.SourceNodeKey,
		SourceBoxID:        strings.TrimSpace(input.SourceBoxID),
		CreatedAt:          createdAt,
		ArchiveFormat:      BundleArchiveFormatPAXTar,
		Compression:        BundleCompressionNone,
		TriggerKind:        BundleTriggerManualLane,
		ReadinessBasis:     BundleReadinessOperatorTrigger,
		TransferMode:       TransportModeBundleSeed,
		SelectionReason:    input.Plan.Transport.Reason,
		PolicyFingerprint:  input.Plan.PolicyFingerprint,
		PolicyHashes:       input.Plan.PolicyHashes,
		InventoryHash:      input.Plan.InventoryHash,
		TotalSourceFiles:   input.Plan.FileCount,
		TotalSourceBytes:   input.Plan.TotalBytes,
		ExcludedEntryCount: len(input.Plan.Ignored),
		ExcludedFileCount:  input.Plan.IgnoredFileCount,
		ExcludedBytes:      input.Plan.IgnoredBytes,
		ArchiveParts: []BundleArchivePart{{
			Name:      bundleArchiveFileName,
			SizeBytes: archiveBytes,
			SHA256:    archiveSHA,
		}},
		Plan:                    input.Plan,
		SourceFingerprintBefore: input.Plan.InventoryHash,
		SourceFingerprintAfter:  after.InventoryHash,
		CustodyAction:           BundleCustodyUnpack,
		CleanupState:            BundleCleanupRetainedForRetry,
		RestoreInstructions:     "verify this manifest and archive, then unpack only through LOOM Lane bundle acceptance",
	}
	if err := validateBundleManifest(manifest); err != nil {
		return BundleArtifact{}, err
	}
	manifestPayload, manifestSHA, err := marshalBundleManifest(manifest)
	if err != nil {
		return BundleArtifact{}, err
	}
	manifestPath := filepath.Join(temporaryDir, bundleManifestFileName)
	if err := writeSyncedFile(manifestPath, manifestPayload, 0o600); err != nil {
		return BundleArtifact{}, err
	}
	if _, err := VerifyBundle(manifestPath, archivePath); err != nil {
		return BundleArtifact{}, fmt.Errorf("verify completed local bundle: %w", err)
	}
	if err := syncDirectory(temporaryDir); err != nil {
		return BundleArtifact{}, err
	}
	if err := os.Rename(temporaryDir, finalDir); err != nil {
		return BundleArtifact{}, err
	}
	committed = true
	if err := syncDirectory(input.ArtifactRoot); err != nil {
		return BundleArtifact{}, err
	}
	return BundleArtifact{
		BatchID:        input.BatchID,
		ArtifactPath:   finalDir,
		ArchivePath:    filepath.Join(finalDir, bundleArchiveFileName),
		ManifestPath:   filepath.Join(finalDir, bundleManifestFileName),
		ArchiveBytes:   archiveBytes,
		ArchiveSHA256:  archiveSHA,
		ManifestSHA256: manifestSHA,
		CreatedAt:      createdAt,
		CleanupState:   BundleCleanupRetainedForRetry,
	}, nil
}

func writeBundleArchive(pathValue string, input CreateBundleInput) (int64, string, error) {
	file, err := os.OpenFile(pathValue, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, "", err
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()
	hasher := sha256.New()
	counter := &countingWriter{writer: io.MultiWriter(file, hasher)}
	writer := tar.NewWriter(counter)
	for index, entry := range input.Plan.Entries {
		if input.BeforeEntry != nil {
			if err := input.BeforeEntry(index, entry); err != nil {
				_ = writer.Close()
				return 0, "", err
			}
		}
		source, info, err := openPlannedBundleSource(input.LanePath, entry)
		if err != nil {
			_ = writer.Close()
			return 0, "", fmt.Errorf("open planned bundle entry %q: %w", entry.RelativePath, err)
		}
		if err := verifyBundleSourceInfo(entry, info); err != nil {
			_ = source.Close()
			_ = writer.Close()
			return 0, "", err
		}
		headerName := entry.RelativePath
		typeFlag := byte(tar.TypeReg)
		if entry.Kind == filesystemmeta.ObjectKindDirectory {
			headerName += "/"
			typeFlag = tar.TypeDir
		}
		header := &tar.Header{
			Name:       headerName,
			Mode:       int64(entry.Mode & 0o777),
			Size:       entry.Bytes,
			ModTime:    entry.ModifiedAt.UTC(),
			Typeflag:   typeFlag,
			Format:     tar.FormatPAX,
			PAXRecords: map[string]string{bundlePAXSchemaKey: bundlePAXSchemaValue},
		}
		if err := writer.WriteHeader(header); err != nil {
			_ = source.Close()
			_ = writer.Close()
			return 0, "", err
		}
		if entry.Kind == filesystemmeta.ObjectKindRegularFile {
			written, err := io.CopyN(writer, source, entry.Bytes)
			if err != nil || written != entry.Bytes {
				_ = source.Close()
				_ = writer.Close()
				return 0, "", fmt.Errorf("read planned bundle entry %q: copied %d of %d bytes: %w", entry.RelativePath, written, entry.Bytes, err)
			}
			var extra [1]byte
			if count, readErr := source.Read(extra[:]); count != 0 || (readErr != nil && readErr != io.EOF) {
				_ = source.Close()
				_ = writer.Close()
				return 0, "", fmt.Errorf("planned bundle entry %q grew during archive creation", entry.RelativePath)
			}
		}
		afterInfo, statErr := source.Stat()
		closeErr := source.Close()
		if statErr != nil {
			_ = writer.Close()
			return 0, "", statErr
		}
		if err := verifyBundleSourceInfo(entry, afterInfo); err != nil {
			_ = writer.Close()
			return 0, "", err
		}
		if closeErr != nil {
			_ = writer.Close()
			return 0, "", closeErr
		}
	}
	if err := writer.Close(); err != nil {
		return 0, "", err
	}
	if err := file.Sync(); err != nil {
		return 0, "", err
	}
	if err := file.Close(); err != nil {
		return 0, "", err
	}
	closed = true
	return counter.count, fmt.Sprintf("sha256:%x", hasher.Sum(nil)), nil
}

func openPlannedBundleSource(root string, entry TransferEntry) (*os.File, os.FileInfo, error) {
	if err := validateBundleRelativePath(entry.RelativePath); err != nil {
		return nil, nil, err
	}
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	currentFD := rootFD
	components := strings.Split(entry.RelativePath, "/")
	for index, component := range components {
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		if index < len(components)-1 || entry.Kind == filesystemmeta.ObjectKindDirectory {
			flags |= unix.O_DIRECTORY
		}
		nextFD, openErr := unix.Openat(currentFD, component, flags, 0)
		if currentFD != rootFD {
			_ = unix.Close(currentFD)
		}
		if openErr != nil {
			_ = unix.Close(rootFD)
			return nil, nil, openErr
		}
		currentFD = nextFD
	}
	_ = unix.Close(rootFD)
	file := os.NewFile(uintptr(currentFD), filepath.Join(root, filepath.FromSlash(entry.RelativePath)))
	if file == nil {
		_ = unix.Close(currentFD)
		return nil, nil, fmt.Errorf("open planned source file descriptor")
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return file, info, nil
}

func verifyBundleSourceInfo(entry TransferEntry, info os.FileInfo) error {
	kind := filesystemmeta.ObjectKindRegularFile
	if info.IsDir() {
		kind = filesystemmeta.ObjectKindDirectory
	} else if !info.Mode().IsRegular() {
		return fmt.Errorf("planned bundle entry %q changed to unsupported type %s", entry.RelativePath, info.Mode().Type())
	}
	if kind != entry.Kind || regularFileSize(info) != entry.Bytes || uint32(info.Mode().Perm()) != entry.Mode || !info.ModTime().UTC().Equal(entry.ModifiedAt.UTC()) {
		return fmt.Errorf("planned bundle entry %q metadata changed", entry.RelativePath)
	}
	return nil
}

func validateBundleRelativePath(value string) error {
	if strings.TrimSpace(value) == "" || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("bundle path is empty or contains NUL")
	}
	if strings.HasPrefix(value, "/") || path.IsAbs(value) {
		return fmt.Errorf("bundle path %q is absolute", value)
	}
	cleaned := path.Clean(value)
	if cleaned != value || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("bundle path %q is not a clean relative path", value)
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." {
			return fmt.Errorf("bundle path %q contains an unsafe component", value)
		}
	}
	return nil
}

func samePlannedInventory(left, right TransferPlan) bool {
	return left.Profile == right.Profile && left.PolicyVersion == right.PolicyVersion && left.PolicyFingerprint == right.PolicyFingerprint && left.InventoryHash == right.InventoryHash
}

func bundleFilesystemFreeBytes(pathValue string) (int64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(pathValue, &stat); err != nil {
		return 0, err
	}
	blocks := uint64(stat.Bavail)
	blockSize := uint64(stat.Bsize)
	if blockSize > 0 && blocks > math.MaxInt64/blockSize {
		return math.MaxInt64, nil
	}
	return int64(blocks * blockSize), nil
}

func writeSyncedFile(pathValue string, payload []byte, mode os.FileMode) error {
	file, err := os.OpenFile(pathValue, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func syncDirectory(pathValue string) error {
	directory, err := os.Open(pathValue)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

type countingWriter struct {
	writer io.Writer
	count  int64
}

func (writer *countingWriter) Write(payload []byte) (int, error) {
	count, err := writer.writer.Write(payload)
	writer.count = saturatingAdd(writer.count, int64(count))
	return count, err
}
