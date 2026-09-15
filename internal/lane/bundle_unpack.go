package lane

import (
	"archive/tar"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"loom.local/loom/internal/filesystemmeta"
)

func UnpackBundle(input BundleAcceptInput) (BundleUnpackResult, error) {
	normalized, manifest, err := normalizeBundleAcceptInput(input)
	if err != nil {
		return BundleUnpackResult{}, err
	}
	checkedAccept, err := normalizeAcceptInput(catalogAcceptInputForBundle(normalized, normalized.AcceptedPath))
	if err != nil {
		return BundleUnpackResult{}, err
	}
	canonicalTarget := filepath.Join(checkedAccept.ImportsRoot, checkedAccept.SourceNodeKey, checkedAccept.AcceptedDate, checkedAccept.BatchID)
	if _, statErr := os.Lstat(canonicalTarget); statErr == nil {
		result := bundleUnpackResult(normalized, manifest)
		result.Idempotent = true
		return result, nil
	} else if !os.IsNotExist(statErr) {
		return BundleUnpackResult{}, statErr
	}
	return unpackVerifiedBundle(normalized, manifest)
}

func unpackVerifiedBundle(normalized BundleAcceptInput, manifest BundleManifest) (BundleUnpackResult, error) {
	started := time.Now()
	result := bundleUnpackResult(normalized, manifest)
	if info, statErr := os.Lstat(normalized.AcceptedPath); statErr == nil {
		if err := requireNoSymlinkDirectoryPath(normalized.RemoteRoot, filepath.Dir(normalized.AcceptedPath)); err != nil {
			return BundleUnpackResult{}, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return BundleUnpackResult{}, fmt.Errorf("accepted Lane bundle path already exists and is not a directory")
		}
		if err := verifyExtractedBundleTree(normalized.AcceptedPath, manifest.Plan); err != nil {
			return BundleUnpackResult{}, fmt.Errorf("accepted Lane bundle conflicts with manifest: %w", err)
		}
		if err := prepareSharedBundleTree(filepath.Dir(normalized.ManifestPath), normalized.AcceptedPath); err != nil {
			return BundleUnpackResult{}, err
		}
		result.Idempotent = true
		result.UnpackDurationMS = durationMSSince(started)
		return result, nil
	} else if !os.IsNotExist(statErr) {
		return BundleUnpackResult{}, statErr
	}

	stagingDir := filepath.Dir(normalized.ManifestPath)
	if err := requireNoSymlinkDirectoryPath(filepath.Join(normalized.RemoteRoot, "staging"), stagingDir); err != nil {
		return BundleUnpackResult{}, err
	}
	available := normalized.AvailableBytes
	if available == nil {
		available = bundleFilesystemFreeBytes
	}
	availableBytes, err := available(stagingDir)
	if err != nil {
		return BundleUnpackResult{}, fmt.Errorf("check Lane bundle staging free space: %w", err)
	}
	requiredBytes, err := lanePromotionRequiredBytes(manifest.Plan.TotalBytes, len(manifest.Plan.Entries))
	if err != nil {
		return BundleUnpackResult{}, err
	}
	if availableBytes < requiredBytes {
		return BundleUnpackResult{}, fmt.Errorf("insufficient Lane staging free space for verified bundle extraction: required %d bytes including safety margin, available %d", requiredBytes, availableBytes)
	}
	acceptedParent := filepath.Dir(normalized.AcceptedPath)
	if acceptedParent != stagingDir {
		return BundleUnpackResult{}, fmt.Errorf("verified Lane bundle tree must remain inside its staging batch")
	}
	temporaryDir, err := os.MkdirTemp(stagingDir, ".unpack-")
	if err != nil {
		return BundleUnpackResult{}, err
	}
	if err := os.Chmod(temporaryDir, 0o700); err != nil {
		_ = os.RemoveAll(temporaryDir)
		return BundleUnpackResult{}, err
	}
	defer os.RemoveAll(temporaryDir)
	treeRoot := filepath.Join(temporaryDir, "tree")
	if err := os.Mkdir(treeRoot, 0o700); err != nil {
		return BundleUnpackResult{}, err
	}
	if err := extractBundleArchive(normalized.ArchivePath, treeRoot, manifest.Plan, manifest.ArchiveParts[0], normalized.BeforeExtractEntry); err != nil {
		return BundleUnpackResult{}, err
	}
	if err := verifyExtractedBundleTree(treeRoot, manifest.Plan); err != nil {
		return BundleUnpackResult{}, fmt.Errorf("verify extracted Lane bundle tree: %w", err)
	}
	if err := prepareSharedBundleTree(stagingDir, treeRoot); err != nil {
		return BundleUnpackResult{}, err
	}
	if err := syncDirectory(treeRoot); err != nil {
		return BundleUnpackResult{}, err
	}
	if err := os.Rename(treeRoot, normalized.AcceptedPath); err != nil {
		if info, statErr := os.Lstat(normalized.AcceptedPath); statErr == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			if verifyErr := verifyExtractedBundleTree(normalized.AcceptedPath, manifest.Plan); verifyErr == nil {
				result.Idempotent = true
				result.UnpackDurationMS = durationMSSince(started)
				return result, nil
			}
		}
		return BundleUnpackResult{}, fmt.Errorf("promote verified Lane bundle tree: %w", err)
	}
	if err := syncDirectory(acceptedParent); err != nil {
		return BundleUnpackResult{}, err
	}
	result.UnpackDurationMS = durationMSSince(started)
	return result, nil
}

func bundleUnpackResult(normalized BundleAcceptInput, manifest BundleManifest) BundleUnpackResult {
	return BundleUnpackResult{
		BatchID:       normalized.BatchID,
		AcceptedPath:  normalized.AcceptedPath,
		ManifestPath:  normalized.ManifestPath,
		ArchivePath:   normalized.ArchivePath,
		ArchiveSHA256: manifest.ArchiveParts[0].SHA256,
		FileCount:     manifest.Plan.FileCount,
		DirCount:      manifest.Plan.DirCount,
		TotalBytes:    manifest.Plan.TotalBytes,
	}
}

func prepareSharedBundleTree(stagingDir, treeRoot string) error {
	stagingFD, err := unix.Open(stagingDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open Lane bundle staging directory: %w", err)
	}
	defer unix.Close(stagingFD)
	var stagingStat unix.Stat_t
	if err := unix.Fstat(stagingFD, &stagingStat); err != nil {
		return fmt.Errorf("inspect Lane bundle staging directory: %w", err)
	}
	treeFD, err := unix.Open(treeRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open verified Lane bundle tree: %w", err)
	}
	defer unix.Close(treeFD)
	if err := unix.Fchown(treeFD, -1, int(stagingStat.Gid)); err != nil {
		return fmt.Errorf("assign verified Lane bundle tree to staging group: %w", err)
	}
	if err := unix.Fchmod(treeFD, 0o2770); err != nil {
		return fmt.Errorf("share verified Lane bundle tree with custody service: %w", err)
	}
	return nil
}

func normalizeBundleAcceptInput(input BundleAcceptInput) (BundleAcceptInput, BundleManifest, error) {
	var remoteRoot string
	var err error
	if input.TrustedRemoteRoot {
		remoteRoot, err = normalizeTrustedLaneRuntimeRoot(input.RemoteRoot)
	} else {
		remoteRoot, err = normalizeRemoteRoot(input.RemoteRoot)
	}
	if err != nil {
		return BundleAcceptInput{}, BundleManifest{}, err
	}
	input.RemoteRoot = filepath.Clean(filepath.FromSlash(remoteRoot))
	rawSourceNode := strings.TrimSpace(input.SourceNodeKey)
	rawBatchID := strings.TrimSpace(input.BatchID)
	input.SourceNodeKey = safeToken(rawSourceNode, "source-node")
	input.BatchID = safeToken(rawBatchID, "batch")
	input.SourceBoxID = strings.TrimSpace(input.SourceBoxID)
	if input.SourceNodeKey == "" || input.BatchID == "" || input.SourceNodeKey != rawSourceNode || input.BatchID != rawBatchID {
		return BundleAcceptInput{}, BundleManifest{}, fmt.Errorf("source node key and batch ID must be valid Lane identity tokens")
	}
	acceptedDate, err := time.Parse("2006-01-02", strings.TrimSpace(input.AcceptedDate))
	if err != nil {
		return BundleAcceptInput{}, BundleManifest{}, fmt.Errorf("accepted_date must use YYYY-MM-DD: %w", err)
	}
	input.AcceptedDate = acceptedDate.UTC().Format("2006-01-02")
	if input.AcceptedAt.IsZero() {
		input.AcceptedAt = acceptedDate.UTC()
	} else {
		input.AcceptedAt = input.AcceptedAt.UTC()
		if input.AcceptedAt.Format("2006-01-02") != input.AcceptedDate {
			return BundleAcceptInput{}, BundleManifest{}, fmt.Errorf("accepted_at and accepted_date must identify the same day")
		}
	}
	for label, pathValue := range map[string]*string{
		"manifest_path": &input.ManifestPath,
		"archive_path":  &input.ArchivePath,
		"accepted_path": &input.AcceptedPath,
	} {
		trimmed := strings.TrimSpace(*pathValue)
		if trimmed == "" {
			return BundleAcceptInput{}, BundleManifest{}, fmt.Errorf("%s is required", label)
		}
		absolute, err := filepath.Abs(trimmed)
		if err != nil {
			return BundleAcceptInput{}, BundleManifest{}, err
		}
		*pathValue = filepath.Clean(absolute)
	}
	expectedStaging := filepath.Join(input.RemoteRoot, "staging", input.SourceNodeKey, input.BatchID)
	if filepath.Dir(input.ManifestPath) != expectedStaging || filepath.Base(input.ManifestPath) != bundleManifestFileName || filepath.Dir(input.ArchivePath) != expectedStaging || filepath.Base(input.ArchivePath) != bundleArchiveFileName {
		return BundleAcceptInput{}, BundleManifest{}, fmt.Errorf("bundle manifest and archive must be direct children of the current Lane staging batch")
	}
	expectedAccepted := filepath.Join(expectedStaging, "tree")
	if input.AcceptedPath != expectedAccepted {
		return BundleAcceptInput{}, BundleManifest{}, fmt.Errorf("accepted_path must be the verified staging tree %s", expectedAccepted)
	}
	if err := requireNoSymlinkDirectoryPath(filepath.Join(input.RemoteRoot, "staging"), expectedStaging); err != nil {
		return BundleAcceptInput{}, BundleManifest{}, err
	}
	manifest, err := VerifyBundle(input.ManifestPath, input.ArchivePath)
	if err != nil {
		return BundleAcceptInput{}, BundleManifest{}, err
	}
	if manifest.BatchID != input.BatchID || manifest.SourceNodeKey != input.SourceNodeKey || manifest.SourceBoxID != input.SourceBoxID || manifest.TransferMode != TransportModeBundleSeed {
		return BundleAcceptInput{}, BundleManifest{}, fmt.Errorf("bundle manifest identity does not match the requested Lane batch")
	}
	return input, manifest, nil
}

func extractBundleArchive(archivePath, treeRoot string, plan TransferPlan, part BundleArchivePart, beforeEntry func(int, TransferEntry) error) error {
	if err := requireRegularBundleArtifact(archivePath); err != nil {
		return err
	}
	archive, err := openRegularBundleArtifact(archivePath)
	if err != nil {
		return err
	}
	defer archive.Close()
	hasher := sha256.New()
	archiveBytes, err := io.Copy(hasher, archive)
	if err != nil {
		return err
	}
	if archiveBytes != part.SizeBytes || fmt.Sprintf("sha256:%x", hasher.Sum(nil)) != part.SHA256 {
		return fmt.Errorf("Lane bundle archive changed after verification")
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return err
	}
	reader := tar.NewReader(archive)
	directories := make([]TransferEntry, 0, plan.DirCount)
	seen := make(map[string]bool, len(plan.Entries))
	index := 0
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read Lane bundle archive: %w", err)
		}
		if index >= len(plan.Entries) {
			return fmt.Errorf("Lane bundle archive exceeds planned entry limit")
		}
		entry := plan.Entries[index]
		if beforeEntry != nil {
			if err := beforeEntry(index, entry); err != nil {
				return err
			}
		}
		cleanName := strings.TrimSuffix(header.Name, "/")
		if err := validateBundleRelativePath(cleanName); err != nil {
			return err
		}
		if seen[cleanName] {
			return fmt.Errorf("Lane bundle archive contains duplicate destination %q", cleanName)
		}
		seen[cleanName] = true
		wantName := entry.RelativePath
		wantType := byte(tar.TypeReg)
		if entry.Kind == filesystemmeta.ObjectKindDirectory {
			wantName += "/"
			wantType = tar.TypeDir
		}
		if header.Format != tar.FormatPAX || header.PAXRecords[bundlePAXSchemaKey] != bundlePAXSchemaValue || header.Name != wantName || header.Typeflag != wantType || header.Linkname != "" || header.Size != entry.Bytes || uint32(header.Mode&0o777) != entry.Mode || !header.ModTime.UTC().Equal(entry.ModifiedAt.UTC()) {
			return fmt.Errorf("Lane bundle archive entry %q does not match planned entry %q", header.Name, entry.RelativePath)
		}
		target, err := containedBundleTarget(treeRoot, entry.RelativePath)
		if err != nil {
			return err
		}
		if entry.Kind == filesystemmeta.ObjectKindDirectory {
			if err := os.Mkdir(target, 0o700); err != nil {
				return err
			}
			directories = append(directories, entry)
		} else {
			file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			copied, copyErr := io.Copy(file, reader)
			closeErr := file.Close()
			if copyErr != nil || copied != entry.Bytes {
				return fmt.Errorf("extract Lane bundle file %q: copied %d of %d bytes: %w", entry.RelativePath, copied, entry.Bytes, copyErr)
			}
			if closeErr != nil {
				return closeErr
			}
			if err := os.Chmod(target, os.FileMode(entry.Mode&0o777)); err != nil {
				return err
			}
			if err := os.Chtimes(target, entry.ModifiedAt, entry.ModifiedAt); err != nil {
				return err
			}
		}
		index++
	}
	if index != len(plan.Entries) {
		return fmt.Errorf("Lane bundle archive contains %d entries, want %d", index, len(plan.Entries))
	}
	for index := len(directories) - 1; index >= 0; index-- {
		entry := directories[index]
		target, err := containedBundleTarget(treeRoot, entry.RelativePath)
		if err != nil {
			return err
		}
		if err := os.Chtimes(target, entry.ModifiedAt, entry.ModifiedAt); err != nil {
			return err
		}
		if err := os.Chmod(target, os.FileMode(entry.Mode&0o777)); err != nil {
			return err
		}
	}
	return nil
}

func verifyExtractedBundleTree(root string, plan TransferPlan) error {
	expected := make(map[string]TransferEntry, len(plan.Entries))
	for _, entry := range plan.Entries {
		expected[entry.RelativePath] = entry
	}
	seen := make(map[string]bool, len(plan.Entries))
	err := filepath.WalkDir(root, func(pathValue string, dirEntry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if pathValue == root {
			return nil
		}
		relative, err := filepath.Rel(root, pathValue)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		entry, ok := expected[relative]
		if !ok {
			return fmt.Errorf("extracted tree contains unplanned path %q", relative)
		}
		if seen[relative] {
			return fmt.Errorf("extracted tree contains duplicate path %q", relative)
		}
		seen[relative] = true
		info, err := os.Lstat(pathValue)
		if err != nil {
			return err
		}
		if err := verifyBundleSourceInfo(entry, info); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("extracted tree contains %d planned entries, want %d", len(seen), len(expected))
	}
	return nil
}

func containedBundleTarget(root, relative string) (string, error) {
	if err := validateBundleRelativePath(relative); err != nil {
		return "", err
	}
	target := filepath.Join(root, filepath.FromSlash(relative))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("bundle target %q escapes extraction root", relative)
	}
	return target, nil
}

func requireNoSymlinkDirectoryPath(root, candidate string) error {
	root = filepath.Clean(root)
	candidate = filepath.Clean(candidate)
	if candidate != root && !localPathWithin(root, candidate) {
		return fmt.Errorf("directory %s is outside %s", candidate, root)
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return err
	}
	current := root
	paths := []string{root}
	if relative != "." {
		for _, component := range strings.Split(relative, string(filepath.Separator)) {
			current = filepath.Join(current, component)
			paths = append(paths, current)
		}
	}
	for _, pathValue := range paths {
		info, err := os.Lstat(pathValue)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("directory component %s is not a no-follow directory", pathValue)
		}
	}
	return nil
}

func mkdirAllNoSymlink(root, candidate string, mode os.FileMode) error {
	root = filepath.Clean(root)
	candidate = filepath.Clean(candidate)
	if candidate != root && !localPathWithin(root, candidate) {
		return fmt.Errorf("directory %s is outside %s", candidate, root)
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("directory root %s is not a no-follow directory", root)
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return err
	}
	current := root
	if relative == "." {
		return nil
	}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, mode); err != nil {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("directory component %s is not a no-follow directory", current)
		}
	}
	return nil
}
