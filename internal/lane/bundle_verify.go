package lane

import (
	"archive/tar"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"loom.local/loom/internal/filesystemmeta"
)

func VerifyBundle(manifestPath, archivePath string) (BundleManifest, error) {
	if err := requireRegularBundleArtifact(manifestPath); err != nil {
		return BundleManifest{}, fmt.Errorf("bundle manifest: %w", err)
	}
	if err := requireRegularBundleArtifact(archivePath); err != nil {
		return BundleManifest{}, fmt.Errorf("bundle archive: %w", err)
	}
	manifest, err := ReadBundleManifest(manifestPath)
	if err != nil {
		return BundleManifest{}, err
	}
	part := manifest.ArchiveParts[0]
	if filepath.Base(archivePath) != part.Name {
		return BundleManifest{}, fmt.Errorf("bundle archive name %q does not match manifest part %q", filepath.Base(archivePath), part.Name)
	}
	checksum, size, err := bundleArtifactSHA256(archivePath)
	if err != nil {
		return BundleManifest{}, err
	}
	if size != part.SizeBytes || "sha256:"+checksum != part.SHA256 {
		return BundleManifest{}, fmt.Errorf("bundle archive checksum or size mismatch")
	}
	if err := verifyBundleArchiveInventory(archivePath, manifest.Plan); err != nil {
		return BundleManifest{}, err
	}
	return manifest, nil
}

func verifyBundleArchiveInventory(archivePath string, plan TransferPlan) error {
	archive, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer archive.Close()
	reader := tar.NewReader(archive)
	seen := make(map[string]bool, len(plan.Entries))
	index := 0
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read bundle archive header: %w", err)
		}
		if header.Format != tar.FormatPAX {
			return fmt.Errorf("bundle archive entry %q is not POSIX PAX", header.Name)
		}
		if header.PAXRecords[bundlePAXSchemaKey] != bundlePAXSchemaValue {
			return fmt.Errorf("bundle archive entry %q is missing its LOOM PAX marker", header.Name)
		}
		if index >= len(plan.Entries) {
			return fmt.Errorf("bundle archive contains unplanned entry %q", header.Name)
		}
		entry := plan.Entries[index]
		wantName := entry.RelativePath
		wantType := byte(tar.TypeReg)
		if entry.Kind == filesystemmeta.ObjectKindDirectory {
			wantName += "/"
			wantType = tar.TypeDir
		}
		cleanName := strings.TrimSuffix(header.Name, "/")
		if err := validateBundleRelativePath(cleanName); err != nil {
			return fmt.Errorf("bundle archive header %q: %w", header.Name, err)
		}
		if seen[cleanName] {
			return fmt.Errorf("bundle archive contains duplicate destination %q", cleanName)
		}
		seen[cleanName] = true
		if header.Name != wantName || header.Typeflag != wantType {
			return fmt.Errorf("bundle archive entry %d is %q type %d, want %q type %d", index, header.Name, header.Typeflag, wantName, wantType)
		}
		if header.Linkname != "" {
			return fmt.Errorf("bundle archive entry %q contains a link target", header.Name)
		}
		if header.Size != entry.Bytes || uint32(header.Mode&0o777) != entry.Mode || !header.ModTime.UTC().Equal(entry.ModifiedAt.UTC()) {
			return fmt.Errorf("bundle archive entry %q metadata does not match transfer plan", header.Name)
		}
		copied, err := io.Copy(io.Discard, reader)
		if err != nil {
			return fmt.Errorf("read bundle archive entry %q: %w", header.Name, err)
		}
		if copied != entry.Bytes {
			return fmt.Errorf("bundle archive entry %q yielded %d bytes, want %d", header.Name, copied, entry.Bytes)
		}
		index++
	}
	if index != len(plan.Entries) {
		return fmt.Errorf("bundle archive contains %d entries, want %d", index, len(plan.Entries))
	}
	return nil
}

func requireRegularBundleArtifact(pathValue string) error {
	file, err := openRegularBundleArtifact(pathValue)
	if err != nil {
		return err
	}
	return file.Close()
}

func openRegularBundleArtifact(pathValue string) (*os.File, error) {
	fd, err := unix.Open(pathValue, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), pathValue)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open bundle artifact file descriptor")
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("%s is not a regular no-follow artifact", pathValue)
	}
	return file, nil
}

func bundleArtifactSHA256(pathValue string) (string, int64, error) {
	file, err := openRegularBundleArtifact(pathValue)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return "", 0, err
	}
	return fmt.Sprintf("%x", hasher.Sum(nil)), size, nil
}
