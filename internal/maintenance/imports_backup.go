package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	ImportsBackupEvidenceSchema = "loom.imports.backup_evidence.v1"

	ImportsBackupPolicyCanonical = "committed_lane_custody_only"
	ImportsBackupPolicyLegacy    = "legacy_complete_physical_custody"
	ImportsSnapshotSharedStore   = "shared_content_addressed_store"

	laneCustodyManifestName   = ".loom-lane-custody.json"
	laneCustodyManifestSchema = "loom.lane.custody_manifest.v1"

	maxImportsEvidenceEntries = 1_000_000
	maxImportsEvidenceBytes   = 256 << 20
	maxImportsRelativePath    = 4096
)

type ImportsBackupEvidence struct {
	Schema                string                       `json:"schema"`
	Policy                string                       `json:"policy"`
	InventoryHash         string                       `json:"inventory_hash"`
	FileCount             int64                        `json:"file_count"`
	DirectoryCount        int64                        `json:"directory_count"`
	SymlinkCount          int64                        `json:"symlink_count"`
	TotalBytes            int64                        `json:"total_bytes"`
	SnapshotMethod        string                       `json:"snapshot_method"`
	SharedObjectLinkCount int64                        `json:"shared_object_link_count,omitempty"`
	Entries               []ImportsBackupEvidenceEntry `json:"entries"`
}

type ImportsBackupEvidenceEntry struct {
	RelativePath string    `json:"relative_path"`
	Kind         string    `json:"kind"`
	Mode         uint32    `json:"mode"`
	ModifiedAt   time.Time `json:"modified_at"`
	SizeBytes    int64     `json:"size_bytes,omitempty"`
	SHA256       string    `json:"sha256,omitempty"`
	LinkTarget   string    `json:"link_target,omitempty"`
}

type laneCustodyManifestEvidence struct {
	SchemaVersion  string                     `json:"schema_version"`
	SourceNodeKey  string                     `json:"source_node_key"`
	BatchID        string                     `json:"batch_id"`
	AcceptedDate   string                     `json:"accepted_date"`
	InventoryHash  string                     `json:"inventory_hash"`
	FileCount      int                        `json:"file_count"`
	DirectoryCount int                        `json:"directory_count"`
	SymlinkCount   int                        `json:"symlink_count"`
	TotalBytes     int64                      `json:"total_bytes"`
	Entries        []laneCustodyManifestEntry `json:"entries"`
}

type laneCustodyManifestEntry struct {
	RelativePath string    `json:"relative_path"`
	Kind         string    `json:"kind"`
	SizeBytes    int64     `json:"size_bytes,omitempty"`
	Mode         uint32    `json:"mode"`
	ModifiedAt   time.Time `json:"modified_at"`
	SHA256       string    `json:"sha256,omitempty"`
	LinkTarget   string    `json:"link_target,omitempty"`
}

// WriteImportsBackupEvidence inventories the already-copied snapshot, checks
// its custody policy, and atomically publishes portable verification evidence.
func WriteImportsBackupEvidence(ctx context.Context, importsRoot, evidencePath, policy, snapshotMethod string, sharedObjectLinkCount int64) (ImportsBackupEvidence, error) {
	return WriteImportsBackupEvidenceMatching(ctx, importsRoot, evidencePath, policy, snapshotMethod, sharedObjectLinkCount, nil)
}

// WriteImportsBackupEvidenceMatching publishes evidence only when the completed
// snapshot exactly matches the immutable source inventory captured before any
// shared-object or destination mutation.
func WriteImportsBackupEvidenceMatching(ctx context.Context, importsRoot, evidencePath, policy, snapshotMethod string, sharedObjectLinkCount int64, expected *ImportsBackupEvidence) (ImportsBackupEvidence, error) {
	evidence, err := InspectImportsCustody(ctx, importsRoot, policy)
	if err != nil {
		return ImportsBackupEvidence{}, err
	}
	if expected != nil {
		want := *expected
		want.SnapshotMethod = ""
		want.SharedObjectLinkCount = 0
		if !reflect.DeepEqual(evidence, want) {
			return ImportsBackupEvidence{}, fmt.Errorf("completed Imports snapshot does not match preflight custody inventory")
		}
	}
	if snapshotMethod != ImportsSnapshotSharedStore {
		return ImportsBackupEvidence{}, fmt.Errorf("unsupported Imports snapshot method %q", snapshotMethod)
	}
	evidence.SnapshotMethod = snapshotMethod
	evidence.SharedObjectLinkCount = sharedObjectLinkCount
	if snapshotMethod == ImportsSnapshotSharedStore && sharedObjectLinkCount != evidence.FileCount {
		return ImportsBackupEvidence{}, fmt.Errorf("Imports shared-store snapshot linked %d of %d regular files", sharedObjectLinkCount, evidence.FileCount)
	}
	if err := writeImportsEvidenceAtomic(evidencePath, evidence); err != nil {
		return ImportsBackupEvidence{}, err
	}
	return evidence, nil
}

// InspectImportsCustody builds portable evidence for a source or snapshot and
// fully validates canonical Lane commit manifests before a caller mutates its
// backup destination. Legacy mode intentionally inventories every physical
// object because pre-cutover custody has no commit markers.
func InspectImportsCustody(ctx context.Context, importsRoot, policy string) (ImportsBackupEvidence, error) {
	evidence, err := buildImportsBackupEvidence(ctx, importsRoot, policy)
	if err != nil {
		return ImportsBackupEvidence{}, err
	}
	if policy == ImportsBackupPolicyCanonical {
		if err := validateCanonicalImportsCustody(ctx, importsRoot, evidence); err != nil {
			return ImportsBackupEvidence{}, err
		}
	}
	return evidence, nil
}

// ValidateImportsBackup proves the snapshot still matches its bounded portable
// evidence. Canonical mode additionally proves every copied batch has a valid
// Lane custody commit manifest and contains no uncommitted payload.
func ValidateImportsBackup(ctx context.Context, importsRoot, evidencePath, policy, snapshotMethod string) error {
	want, err := readImportsBackupEvidence(evidencePath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(policy) == "" {
		policy = want.Policy
	}
	if want.Policy != policy {
		return fmt.Errorf("imports backup policy mismatch: evidence=%q manifest=%q", want.Policy, policy)
	}
	if strings.TrimSpace(snapshotMethod) == "" {
		snapshotMethod = want.SnapshotMethod
	}
	if want.SnapshotMethod != snapshotMethod {
		return fmt.Errorf("imports snapshot method mismatch: evidence=%q manifest=%q", want.SnapshotMethod, snapshotMethod)
	}
	got, err := buildImportsBackupEvidence(ctx, importsRoot, policy)
	if err != nil {
		return err
	}
	got.SnapshotMethod = want.SnapshotMethod
	got.SharedObjectLinkCount = want.SharedObjectLinkCount
	if want.SnapshotMethod != ImportsSnapshotSharedStore {
		return fmt.Errorf("unsupported Imports snapshot method %q", want.SnapshotMethod)
	}
	if want.SnapshotMethod == ImportsSnapshotSharedStore && want.SharedObjectLinkCount != want.FileCount {
		return fmt.Errorf("Imports shared-store snapshot evidence is incomplete")
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("imports backup inventory does not match portable evidence")
	}
	if policy == ImportsBackupPolicyCanonical {
		return validateCanonicalImportsCustody(ctx, importsRoot, got)
	}
	return nil
}

// RehydrateLegacyTreeImportsMetadata restores exact portable Imports metadata
// after a legacy-tree transport whose SFTP backend preserves only whole-second
// mtimes. Evidence is authenticated against the backup manifest before any
// metadata mutation, and every payload/link is content-validated no-follow.
func RehydrateLegacyTreeImportsMetadata(ctx context.Context, backupDir string) error {
	return rehydrateLegacyTreeImportsMetadata(ctx, backupDir, nil)
}

func rehydrateLegacyTreeImportsMetadata(ctx context.Context, backupDir string, beforeApply func(ImportsBackupEvidenceEntry) error) error {
	manifest, err := ReadBackupManifest(filepath.Join(backupDir, "manifest.json"))
	if err != nil {
		return err
	}
	if manifest.Schema != BackupManifestSchemaV09 {
		return nil
	}
	evidenceRelative := strings.TrimSpace(manifest.Paths.ImportsEvidence)
	importsRelative := strings.TrimSpace(manifest.Paths.Imports)
	if evidenceRelative == "" || importsRelative == "" {
		return fmt.Errorf("v0.9 backup is missing Imports paths")
	}
	var evidenceArtifact *BackupManifestArtifact
	for index := range manifest.Artifacts {
		artifact := &manifest.Artifacts[index]
		if artifact.Kind != ArtifactKindImportsEvidence {
			continue
		}
		if evidenceArtifact != nil {
			return fmt.Errorf("backup manifest has multiple Imports evidence artifacts")
		}
		evidenceArtifact = artifact
	}
	if evidenceArtifact == nil || strings.TrimSpace(evidenceArtifact.Path) != evidenceRelative || evidenceArtifact.SizeBytes == nil || *evidenceArtifact.SizeBytes < 0 || *evidenceArtifact.SizeBytes > maxImportsEvidenceBytes || strings.TrimSpace(evidenceArtifact.SHA256) == "" {
		return fmt.Errorf("backup manifest has no authenticated Imports evidence artifact")
	}
	evidencePath, err := ResolveBackupPath(backupDir, evidenceRelative)
	if err != nil {
		return err
	}
	actualHash, actualSize, err := hashImportsNoFollowRegular(ctx, evidencePath)
	if err != nil {
		return err
	}
	if actualSize != *evidenceArtifact.SizeBytes || actualHash != evidenceArtifact.SHA256 {
		return fmt.Errorf("Imports evidence artifact size or checksum does not match backup manifest")
	}
	evidence, err := readImportsBackupEvidence(evidencePath)
	if err != nil {
		return err
	}
	if evidence.Policy != manifest.Policies.Imports || evidence.SnapshotMethod != manifest.Policies.ImportsSnapshot {
		return fmt.Errorf("Imports evidence policy does not match backup manifest")
	}
	importsRoot, err := ResolveBackupPath(backupDir, importsRelative)
	if err != nil {
		return err
	}
	if err := validateImportsContentForMetadataRehydration(ctx, importsRoot, evidence); err != nil {
		return err
	}
	rootHandle, err := os.OpenRoot(importsRoot)
	if err != nil {
		return err
	}
	defer rootHandle.Close()
	entries := append([]ImportsBackupEvidenceEntry(nil), evidence.Entries...)
	sort.SliceStable(entries, func(i, j int) bool {
		leftDirectory := entries[i].Kind == "directory"
		rightDirectory := entries[j].Kind == "directory"
		if leftDirectory != rightDirectory {
			return !leftDirectory
		}
		if leftDirectory {
			return strings.Count(entries[i].RelativePath, "/") > strings.Count(entries[j].RelativePath, "/")
		}
		return entries[i].RelativePath < entries[j].RelativePath
	})
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if beforeApply != nil {
			if err := beforeApply(entry); err != nil {
				return err
			}
		}
		if err := applyImportsMetadataConfined(rootHandle, entry); err != nil {
			return err
		}
	}
	return nil
}

func applyImportsMetadataConfined(root *os.Root, entry ImportsBackupEvidenceEntry) error {
	relative := filepath.FromSlash(entry.RelativePath)
	var opened *os.File
	var openedStat unix.Stat_t
	if entry.Kind != "symlink" {
		flags := os.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		if entry.Kind == "directory" {
			flags |= unix.O_DIRECTORY
		}
		file, err := root.OpenFile(relative, flags, 0)
		if err != nil {
			return fmt.Errorf("open Imports object %q for metadata restore: %w", entry.RelativePath, err)
		}
		opened = file
		defer opened.Close()
		info, statErr := file.Stat()
		if statErr != nil || (entry.Kind == "regular_file" && !info.Mode().IsRegular()) || (entry.Kind == "directory" && !info.IsDir()) {
			return fmt.Errorf("Imports object %q changed before metadata restore", entry.RelativePath)
		}
		if err := unix.Fstat(int(file.Fd()), &openedStat); err != nil {
			return fmt.Errorf("stat opened Imports object %q: %w", entry.RelativePath, err)
		}
		if err := file.Chmod(os.FileMode(entry.Mode)); err != nil {
			return fmt.Errorf("restore Imports mode for %q: %w", entry.RelativePath, err)
		}
	}
	parentRelative := filepath.Dir(relative)
	parent, err := root.OpenFile(parentRelative, os.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open confined Imports parent for %q: %w", entry.RelativePath, err)
	}
	parentInfo, statErr := parent.Stat()
	if statErr != nil || !parentInfo.IsDir() {
		_ = parent.Close()
		return fmt.Errorf("Imports parent for %q changed before metadata restore", entry.RelativePath)
	}
	base := filepath.Base(relative)
	var beforePath unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), base, &beforePath, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		_ = parent.Close()
		return fmt.Errorf("stat confined Imports object %q: %w", entry.RelativePath, err)
	}
	if entry.Kind == "symlink" {
		if beforePath.Mode&unix.S_IFMT != unix.S_IFLNK {
			_ = parent.Close()
			return fmt.Errorf("Imports symlink %q changed before metadata restore", entry.RelativePath)
		}
		target, err := readImportsLinkAt(parent, base)
		if err != nil || target != entry.LinkTarget {
			_ = parent.Close()
			return fmt.Errorf("Imports symlink %q changed before metadata restore", entry.RelativePath)
		}
	} else if beforePath.Dev != openedStat.Dev || beforePath.Ino != openedStat.Ino {
		_ = parent.Close()
		return fmt.Errorf("Imports object %q changed before metadata restore", entry.RelativePath)
	}
	times := []unix.Timespec{unix.NsecToTimespec(entry.ModifiedAt.UnixNano()), unix.NsecToTimespec(entry.ModifiedAt.UnixNano())}
	timeErr := unix.UtimesNanoAt(int(parent.Fd()), base, times, unix.AT_SYMLINK_NOFOLLOW)
	if timeErr != nil {
		_ = parent.Close()
		return fmt.Errorf("restore Imports mtime for %q: %w", entry.RelativePath, timeErr)
	}
	var afterPath unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), base, &afterPath, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		_ = parent.Close()
		return fmt.Errorf("re-stat confined Imports object %q: %w", entry.RelativePath, err)
	}
	if beforePath.Dev != afterPath.Dev || beforePath.Ino != afterPath.Ino {
		_ = parent.Close()
		return fmt.Errorf("Imports object %q changed during metadata restore", entry.RelativePath)
	}
	if entry.Kind == "symlink" {
		target, err := readImportsLinkAt(parent, base)
		if err != nil || target != entry.LinkTarget {
			_ = parent.Close()
			return fmt.Errorf("Imports symlink %q changed during metadata restore", entry.RelativePath)
		}
	}
	if err := parent.Close(); err != nil {
		return err
	}
	got, err := root.Lstat(relative)
	if err != nil {
		return err
	}
	if uint32(got.Mode().Perm()) != entry.Mode || !got.ModTime().UTC().Equal(entry.ModifiedAt) || (entry.Kind == "regular_file" && !got.Mode().IsRegular()) || (entry.Kind == "directory" && !got.IsDir()) || (entry.Kind == "symlink" && got.Mode()&os.ModeSymlink == 0) {
		return fmt.Errorf("Imports object %q changed during metadata restore", entry.RelativePath)
	}
	return nil
}

func readImportsLinkAt(parent *os.File, name string) (string, error) {
	buffer := make([]byte, 256)
	for len(buffer) <= maxImportsRelativePath*4 {
		count, err := unix.Readlinkat(int(parent.Fd()), name, buffer)
		if err != nil {
			return "", err
		}
		if count < len(buffer) {
			return string(buffer[:count]), nil
		}
		buffer = make([]byte, len(buffer)*2)
	}
	return "", fmt.Errorf("Imports symlink target is too long")
}

func validateImportsContentForMetadataRehydration(ctx context.Context, root string, evidence ImportsBackupEvidence) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("Imports snapshot root must be a real directory")
	}
	want := make(map[string]ImportsBackupEvidenceEntry, len(evidence.Entries))
	for _, entry := range evidence.Entries {
		if _, duplicate := want[entry.RelativePath]; duplicate {
			return fmt.Errorf("Imports evidence duplicates object %q", entry.RelativePath)
		}
		want[entry.RelativePath] = entry
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer rootHandle.Close()
	seen := make(map[string]struct{}, len(want))
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
		relative, err := normalizeImportsEvidencePath(pathValue)
		if err != nil {
			return err
		}
		expected, ok := want[relative]
		if !ok {
			return fmt.Errorf("Imports snapshot contains unaccounted object %q", relative)
		}
		if _, duplicate := seen[relative]; duplicate {
			return fmt.Errorf("Imports snapshot duplicates object %q", relative)
		}
		seen[relative] = struct{}{}
		objectInfo, err := entry.Info()
		if err != nil {
			return err
		}
		switch expected.Kind {
		case "directory":
			if !objectInfo.IsDir() {
				return fmt.Errorf("Imports object %q is not the expected directory", relative)
			}
		case "regular_file":
			if !objectInfo.Mode().IsRegular() || objectInfo.Size() != expected.SizeBytes {
				return fmt.Errorf("Imports object %q size or kind mismatch", relative)
			}
			hash, err := hashImportsRootFileContent(ctx, rootHandle, filepath.FromSlash(relative), expected.SizeBytes)
			if err != nil || hash != expected.SHA256 {
				return fmt.Errorf("Imports object %q checksum mismatch", relative)
			}
		case "symlink":
			if objectInfo.Mode()&os.ModeSymlink == 0 {
				return fmt.Errorf("Imports object %q is not the expected symlink", relative)
			}
			target, err := rootHandle.Readlink(filepath.FromSlash(relative))
			if err != nil || target != expected.LinkTarget {
				return fmt.Errorf("Imports symlink %q target mismatch", relative)
			}
		default:
			return fmt.Errorf("Imports evidence has unsupported kind %q", expected.Kind)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(want) {
		return fmt.Errorf("Imports snapshot is missing %d evidence objects", len(want)-len(seen))
	}
	return nil
}

func hashImportsRootFileContent(ctx context.Context, root *os.Root, relative string, expectedSize int64) (string, error) {
	file, err := root.Open(relative)
	if err != nil {
		return "", err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() != expectedSize {
		return "", fmt.Errorf("Imports object changed before content validation")
	}
	digest := sha256.New()
	written, err := io.Copy(digest, &importsContextReader{ctx: ctx, reader: file})
	if err != nil {
		return "", err
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || written != expectedSize {
		return "", fmt.Errorf("Imports object changed during content validation")
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func hashImportsNoFollowRegular(ctx context.Context, pathValue string) (string, int64, error) {
	before, err := os.Lstat(pathValue)
	if err != nil {
		return "", 0, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return "", 0, fmt.Errorf("Imports evidence artifact must be a no-follow regular file")
	}
	if before.Size() > maxImportsEvidenceBytes {
		return "", 0, fmt.Errorf("Imports evidence artifact exceeds %d-byte symmetric limit", maxImportsEvidenceBytes)
	}
	file, err := os.Open(pathValue)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return "", 0, fmt.Errorf("Imports evidence artifact changed before authentication")
	}
	digest := sha256.New()
	written, err := io.Copy(digest, &importsContextReader{ctx: ctx, reader: file})
	if err != nil {
		return "", 0, err
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(opened, after) || written != opened.Size() {
		return "", 0, fmt.Errorf("Imports evidence artifact changed during authentication")
	}
	return hex.EncodeToString(digest.Sum(nil)), written, nil
}

type importsContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *importsContextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func buildImportsBackupEvidence(ctx context.Context, root, policy string) (ImportsBackupEvidence, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "." || root == "" || !filepath.IsAbs(root) {
		return ImportsBackupEvidence{}, fmt.Errorf("imports backup root must be absolute")
	}
	if policy != ImportsBackupPolicyCanonical && policy != ImportsBackupPolicyLegacy {
		return ImportsBackupEvidence{}, fmt.Errorf("unsupported imports backup policy %q", policy)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return ImportsBackupEvidence{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return ImportsBackupEvidence{}, fmt.Errorf("imports backup root must be a real directory")
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return ImportsBackupEvidence{}, err
	}
	defer rootHandle.Close()
	evidence := ImportsBackupEvidence{Schema: ImportsBackupEvidenceSchema, Policy: policy, Entries: []ImportsBackupEvidenceEntry{}}
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
		if len(evidence.Entries) >= maxImportsEvidenceEntries {
			return fmt.Errorf("imports backup exceeds %d evidence entries", maxImportsEvidenceEntries)
		}
		rel, err := normalizeImportsEvidencePath(pathValue)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		item := ImportsBackupEvidenceEntry{RelativePath: rel, Mode: uint32(info.Mode().Perm()), ModifiedAt: info.ModTime().UTC()}
		switch {
		case info.IsDir():
			item.Kind = "directory"
			evidence.DirectoryCount++
		case info.Mode().IsRegular():
			item.Kind = "regular_file"
			item.SHA256, item.SizeBytes, err = hashImportsRootFile(ctx, rootHandle, filepath.FromSlash(rel), info)
			if err != nil {
				return err
			}
			evidence.FileCount++
			evidence.TotalBytes += item.SizeBytes
		case info.Mode()&os.ModeSymlink != 0:
			item.Kind = "symlink"
			item.LinkTarget, err = rootHandle.Readlink(filepath.FromSlash(rel))
			if err != nil {
				return err
			}
			evidence.SymlinkCount++
		default:
			return fmt.Errorf("imports backup entry %q has unsupported type %s", rel, info.Mode().Type())
		}
		evidence.Entries = append(evidence.Entries, item)
		return nil
	})
	if err != nil {
		return ImportsBackupEvidence{}, err
	}
	sort.Slice(evidence.Entries, func(i, j int) bool { return evidence.Entries[i].RelativePath < evidence.Entries[j].RelativePath })
	evidence.InventoryHash, err = hashImportsEvidenceEntries(evidence.Entries)
	return evidence, err
}

func validateCanonicalImportsCustody(ctx context.Context, root string, evidence ImportsBackupEvidence) error {
	entryByPath := make(map[string]ImportsBackupEvidenceEntry, len(evidence.Entries))
	markerIdentities := map[string][]string{}
	for _, entry := range evidence.Entries {
		entryByPath[entry.RelativePath] = entry
		if filepath.Base(filepath.FromSlash(entry.RelativePath)) != laneCustodyManifestName || entry.Kind != "regular_file" {
			continue
		}
		parts := strings.Split(entry.RelativePath, "/")
		if len(parts) != 4 {
			return fmt.Errorf("Lane custody commit manifest %q is outside source/date/batch layout", entry.RelativePath)
		}
		batchPrefix := strings.Join(parts[:3], "/")
		markerIdentities[batchPrefix] = parts[:3]
	}
	batchPrefixes := make(map[string]struct{}, len(markerIdentities))
	prefixes := make([]string, 0, len(markerIdentities))
	for prefix := range markerIdentities {
		prefixes = append(prefixes, prefix)
	}
	sort.Strings(prefixes)
	for _, batchPrefix := range prefixes {
		if err := validateLaneCustodyManifestBackup(ctx, root, batchPrefix, markerIdentities[batchPrefix], entryByPath); err != nil {
			return err
		}
		batchPrefixes[batchPrefix] = struct{}{}
	}
	for _, entry := range evidence.Entries {
		parts := strings.Split(entry.RelativePath, "/")
		if len(parts) < 3 {
			if entry.Kind != "directory" {
				return fmt.Errorf("canonical imports entry %q is outside a committed batch", entry.RelativePath)
			}
			continue
		}
		if _, ok := batchPrefixes[strings.Join(parts[:3], "/")]; !ok {
			return fmt.Errorf("canonical imports entry %q belongs to an uncommitted batch", entry.RelativePath)
		}
	}
	return nil
}

func validateLaneCustodyManifestBackup(ctx context.Context, root, batchPrefix string, identity []string, observed map[string]ImportsBackupEvidenceEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	markerPath := filepath.Join(root, filepath.FromSlash(batchPrefix), laneCustodyManifestName)
	file, err := os.Open(markerPath)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxImportsEvidenceBytes+1))
	decoder.DisallowUnknownFields()
	var manifest laneCustodyManifestEvidence
	if err := decoder.Decode(&manifest); err != nil {
		return fmt.Errorf("read Lane custody manifest %q: %w", batchPrefix, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("Lane custody manifest %q has trailing or oversized content", batchPrefix)
	}
	if manifest.SchemaVersion != laneCustodyManifestSchema || manifest.SourceNodeKey != identity[0] || manifest.AcceptedDate != identity[1] || manifest.BatchID != identity[2] {
		return fmt.Errorf("Lane custody manifest %q identity mismatch", batchPrefix)
	}
	wantHash, err := hashLaneCustodyManifestEntries(manifest.Entries)
	if err != nil || manifest.InventoryHash != wantHash {
		return fmt.Errorf("Lane custody manifest %q inventory hash mismatch", batchPrefix)
	}
	seen := make(map[string]struct{}, len(manifest.Entries))
	var files, directories, symlinks int
	var totalBytes int64
	for _, item := range manifest.Entries {
		rel, err := normalizeImportsEvidencePath(item.RelativePath)
		if err != nil {
			return err
		}
		full := batchPrefix + "/" + rel
		if _, duplicate := seen[full]; duplicate {
			return fmt.Errorf("Lane custody manifest %q duplicates %q", batchPrefix, rel)
		}
		seen[full] = struct{}{}
		got, ok := observed[full]
		if !ok || got.Kind != item.Kind || got.Mode != item.Mode || !got.ModifiedAt.Equal(item.ModifiedAt) || got.SizeBytes != item.SizeBytes || got.SHA256 != item.SHA256 || got.LinkTarget != item.LinkTarget {
			return fmt.Errorf("Lane custody manifest %q does not match archived object %q", batchPrefix, rel)
		}
		switch item.Kind {
		case "regular_file":
			files++
			totalBytes += item.SizeBytes
		case "directory":
			directories++
		case "symlink":
			symlinks++
		default:
			return fmt.Errorf("Lane custody manifest %q has unsupported kind %q", batchPrefix, item.Kind)
		}
	}
	for pathValue := range observed {
		if !strings.HasPrefix(pathValue, batchPrefix+"/") || pathValue == batchPrefix+"/"+laneCustodyManifestName {
			continue
		}
		if _, ok := seen[pathValue]; !ok {
			return fmt.Errorf("committed Lane batch %q contains unmanifested object %q", batchPrefix, strings.TrimPrefix(pathValue, batchPrefix+"/"))
		}
	}
	if files != manifest.FileCount || directories != manifest.DirectoryCount || symlinks != manifest.SymlinkCount || totalBytes != manifest.TotalBytes {
		return fmt.Errorf("Lane custody manifest %q summary mismatch", batchPrefix)
	}
	return nil
}

func normalizeImportsEvidencePath(value string) (string, error) {
	value = filepath.ToSlash(value)
	if value == "" || value == "." || strings.ContainsRune(value, '\x00') || strings.HasPrefix(value, "/") || len(value) > maxImportsRelativePath || pathClean(value) != value || value == ".." || strings.HasPrefix(value, "../") {
		return "", fmt.Errorf("invalid imports evidence path %q", value)
	}
	return value, nil
}

func pathClean(value string) string {
	return filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
}

func hashImportsRootFile(ctx context.Context, root *os.Root, relative string, expected os.FileInfo) (string, int64, error) {
	file, err := root.Open(relative)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	if !sameImportsEvidenceSource(before, expected) {
		return "", 0, fmt.Errorf("imports source changed before evidence hashing: %s", relative)
	}
	digest := sha256.New()
	buffer := make([]byte, 128<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		read, readErr := file.Read(buffer)
		if read > 0 {
			_, _ = digest.Write(buffer[:read])
			total += int64(read)
		}
		if readErr == io.EOF {
			after, statErr := file.Stat()
			if statErr != nil {
				return "", 0, statErr
			}
			if !sameImportsEvidenceSource(before, after) || total != before.Size() {
				return "", 0, fmt.Errorf("imports source changed while evidence was hashed: %s", relative)
			}
			return hex.EncodeToString(digest.Sum(nil)), total, nil
		}
		if readErr != nil {
			return "", 0, readErr
		}
	}
}

func sameImportsEvidenceSource(left, right os.FileInfo) bool {
	return left != nil && right != nil && left.Mode().IsRegular() && right.Mode().IsRegular() && os.SameFile(left, right) && left.Size() == right.Size() && left.Mode().Perm() == right.Mode().Perm() && left.ModTime().Equal(right.ModTime())
}

func hashImportsEvidenceEntries(entries []ImportsBackupEvidenceEntry) (string, error) {
	digest := sha256.New()
	if err := json.NewEncoder(digest).Encode(entries); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

func hashLaneCustodyManifestEntries(entries []laneCustodyManifestEntry) (string, error) {
	digest := sha256.New()
	if err := json.NewEncoder(digest).Encode(entries); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

func readImportsBackupEvidence(pathValue string) (ImportsBackupEvidence, error) {
	return readImportsBackupEvidenceWithLimit(pathValue, maxImportsEvidenceBytes)
}

func readImportsBackupEvidenceWithLimit(pathValue string, maxBytes int64) (ImportsBackupEvidence, error) {
	pathValue = filepath.Clean(strings.TrimSpace(pathValue))
	if pathValue == "." || pathValue == "" || maxBytes <= 0 {
		return ImportsBackupEvidence{}, fmt.Errorf("imports backup evidence path and positive byte limit are required")
	}
	noFollowInfo, err := os.Lstat(pathValue)
	if err != nil {
		return ImportsBackupEvidence{}, err
	}
	if noFollowInfo.Mode()&os.ModeSymlink != 0 || !noFollowInfo.Mode().IsRegular() {
		return ImportsBackupEvidence{}, fmt.Errorf("imports backup evidence must be a no-follow regular file")
	}
	if noFollowInfo.Size() > maxBytes {
		return ImportsBackupEvidence{}, fmt.Errorf("imports backup evidence exceeds %d-byte symmetric limit", maxBytes)
	}
	file, err := os.Open(pathValue)
	if err != nil {
		return ImportsBackupEvidence{}, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return ImportsBackupEvidence{}, err
	}
	if !openedInfo.Mode().IsRegular() || openedInfo.Size() > maxBytes || !os.SameFile(noFollowInfo, openedInfo) {
		return ImportsBackupEvidence{}, fmt.Errorf("imports backup evidence changed before bounded decode")
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxBytes+1))
	decoder.DisallowUnknownFields()
	var evidence ImportsBackupEvidence
	if err := decoder.Decode(&evidence); err != nil {
		return ImportsBackupEvidence{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ImportsBackupEvidence{}, fmt.Errorf("imports backup evidence has trailing or oversized content")
	}
	if evidence.Schema != ImportsBackupEvidenceSchema || len(evidence.Entries) > maxImportsEvidenceEntries {
		return ImportsBackupEvidence{}, fmt.Errorf("unsupported or oversized imports backup evidence")
	}
	if err := validateImportsEvidenceEntries(evidence); err != nil {
		return ImportsBackupEvidence{}, err
	}
	wantHash, err := hashImportsEvidenceEntries(evidence.Entries)
	if err != nil || wantHash != evidence.InventoryHash {
		return ImportsBackupEvidence{}, fmt.Errorf("imports backup evidence inventory hash mismatch")
	}
	return evidence, nil
}

func validateImportsEvidenceEntries(evidence ImportsBackupEvidence) error {
	seen := make(map[string]struct{}, len(evidence.Entries))
	var files, directories, symlinks, totalBytes int64
	for _, entry := range evidence.Entries {
		relative, err := normalizeImportsEvidencePath(entry.RelativePath)
		if err != nil || relative != entry.RelativePath {
			return fmt.Errorf("invalid Imports evidence entry path %q", entry.RelativePath)
		}
		if _, duplicate := seen[relative]; duplicate {
			return fmt.Errorf("Imports evidence duplicates relative path %q", relative)
		}
		seen[relative] = struct{}{}
		if entry.Mode&^uint32(0o777) != 0 || entry.ModifiedAt.IsZero() {
			return fmt.Errorf("Imports evidence entry %q has invalid mode or mtime", relative)
		}
		switch entry.Kind {
		case "regular_file":
			if entry.SizeBytes < 0 || len(entry.SHA256) != sha256.Size*2 || strings.ToLower(entry.SHA256) != entry.SHA256 || entry.LinkTarget != "" {
				return fmt.Errorf("Imports regular-file evidence %q is incomplete", relative)
			}
			if _, err := hex.DecodeString(entry.SHA256); err != nil {
				return fmt.Errorf("Imports regular-file evidence %q has invalid checksum", relative)
			}
			if totalBytes > int64(^uint64(0)>>1)-entry.SizeBytes {
				return fmt.Errorf("Imports evidence total bytes overflow")
			}
			files++
			totalBytes += entry.SizeBytes
		case "directory":
			if entry.SizeBytes != 0 || entry.SHA256 != "" || entry.LinkTarget != "" {
				return fmt.Errorf("Imports directory evidence %q has payload fields", relative)
			}
			directories++
		case "symlink":
			if entry.SizeBytes != 0 || entry.SHA256 != "" || entry.LinkTarget == "" {
				return fmt.Errorf("Imports symlink evidence %q is incomplete", relative)
			}
			symlinks++
		default:
			return fmt.Errorf("Imports evidence entry %q has unsupported kind %q", relative, entry.Kind)
		}
	}
	if evidence.FileCount != files || evidence.DirectoryCount != directories || evidence.SymlinkCount != symlinks || evidence.TotalBytes != totalBytes {
		return fmt.Errorf("Imports evidence summary does not match entries")
	}
	return nil
}

func writeImportsEvidenceAtomic(pathValue string, evidence ImportsBackupEvidence) error {
	return writeImportsEvidenceAtomicWithLimit(pathValue, evidence, maxImportsEvidenceBytes)
}

func writeImportsEvidenceAtomicWithLimit(pathValue string, evidence ImportsBackupEvidence, maxBytes int64) error {
	pathValue = filepath.Clean(strings.TrimSpace(pathValue))
	if pathValue == "." || pathValue == "" {
		return fmt.Errorf("imports backup evidence path is required")
	}
	if maxBytes <= 0 {
		return fmt.Errorf("imports backup evidence byte limit must be positive")
	}
	if err := os.MkdirAll(filepath.Dir(pathValue), 0o750); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(pathValue), ".imports-evidence-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(0o640); err != nil {
		_ = file.Close()
		return err
	}
	bounded := &importsEvidenceLimitWriter{destination: file, remaining: maxBytes, limit: maxBytes}
	encoder := json.NewEncoder(bounded)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(evidence); err != nil {
		_ = file.Close()
		return fmt.Errorf("write imports backup evidence: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, pathValue); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(pathValue))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

type importsEvidenceLimitWriter struct {
	destination io.Writer
	remaining   int64
	limit       int64
}

func (w *importsEvidenceLimitWriter) Write(payload []byte) (int, error) {
	if int64(len(payload)) > w.remaining {
		return 0, fmt.Errorf("imports backup evidence exceeds %d-byte symmetric limit", w.limit)
	}
	written, err := w.destination.Write(payload)
	w.remaining -= int64(written)
	return written, err
}
