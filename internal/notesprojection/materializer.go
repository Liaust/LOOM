package notesprojection

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/storageview"
)

func BuildProjectionEntries(root string, sources []SourceObject, now time.Time) ([]ProjectionEntry, []Finding) {
	var entries []ProjectionEntry
	var findings []Finding
	used := map[string]int{}
	for _, source := range sources {
		projectedPath, err := projectedPathForSource(source)
		if err != nil {
			findings = append(findings, Finding{
				Severity: SeverityWarning,
				Kind:     "source_project_path_invalid",
				Path:     source.RelativePath,
				Summary:  err.Error(),
			})
			continue
		}
		projectedPath = deduplicateProjectedPath(projectedPath, source.KnowledgeObjectID, used)
		entry := ProjectionEntry{
			SourceCategory:    source.SourceCategory,
			SourcePosture:     source.SourcePosture,
			Declaration:       source.Declaration,
			TopicKey:          source.TopicKey,
			CollectionKey:     source.CollectionKey,
			ProjectedPath:     projectedPath,
			FilesystemPath:    filepath.Join(root, filepath.FromSlash(projectedPath)),
			SourcePath:        strings.TrimSpace(source.SourcePath),
			CopyMode:          CopyModeFileCopy,
			Status:            ProjectionStatusPlanned,
			NotesSourceRootID: strings.TrimSpace(source.NotesSourceRootID),
			RootKind:          strings.TrimSpace(source.RootKind),
			SourceNodeKey:     strings.TrimSpace(source.SourceNodeKey),
			ProjectID:         source.ProjectID,
			ProjectSlug:       strings.TrimSpace(source.ProjectSlug),
			RelativePath:      strings.Trim(strings.ReplaceAll(source.RelativePath, "\\", "/"), "/"),
			KnowledgeObjectID: strings.TrimSpace(source.KnowledgeObjectID),
			StorageEntryID:    source.StorageEntryID,
			Title:             strings.TrimSpace(source.Title),
			FileClass:         strings.TrimSpace(source.FileClass),
			MimeType:          strings.TrimSpace(source.MimeType),
			SizeBytes:         source.SizeBytes,
			SourceHash:        strings.TrimSpace(source.SourceHash),
			SourceRevision:    strings.TrimSpace(source.SourceRevision),
			SourceRefKind:     strings.TrimSpace(source.SourceRefKind),
			SourceRefURI:      strings.TrimSpace(source.SourceRefURI),
			SourceRefMember:   strings.TrimSpace(source.SourceRefMember),
			LastSeenAt:        source.LastSeenAt,
		}
		if entry.LastSeenAt.IsZero() {
			entry.LastSeenAt = now
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].ProjectedPath == entries[j].ProjectedPath {
			return entries[i].KnowledgeObjectID < entries[j].KnowledgeObjectID
		}
		return entries[i].ProjectedPath < entries[j].ProjectedPath
	})
	return entries, findings
}

type materializer struct {
	root     string
	now      time.Time
	dryRun   bool
	entries  []ProjectionEntry
	findings []Finding
	reuse    bool
}

func (m materializer) run(ctx context.Context) (_ Manifest, _ []Change, _ []Finding, resultErr error) {
	if err := ctx.Err(); err != nil {
		return Manifest{}, nil, nil, err
	}
	previous, _, err := ReadManifest(m.root)
	var findings []Finding
	if err != nil {
		findings = append(findings, Finding{
			Severity: SeverityWarning,
			Kind:     "previous_manifest_unreadable",
			Path:     manifestPath(m.root),
			Summary:  err.Error(),
		})
	}
	next := map[string]ProjectionEntry{}
	for _, entry := range m.entries {
		next[entry.ProjectedPath] = entry
	}
	var changes []Change
	if !m.dryRun {
		if err := prepareProjectionDir(m.root, m.root); err != nil {
			return Manifest{}, changes, findings, fmt.Errorf("create notes projection root: %w", err)
		}
		defer func() {
			resultErr = errors.Join(resultErr, finalizePermissions(m.root))
		}()
		if err := writeFileAtomic(m.root, filepath.Join(m.root, ".loom", "rebuild-pending"), []byte("incomplete\n"), 0o444); err != nil {
			return Manifest{}, changes, findings, err
		}
		if err := makeExistingDirsWritable(m.root); err != nil {
			return Manifest{}, changes, findings, fmt.Errorf("prepare notes projection permissions: %w", err)
		}
	}
	pruneChanges, pruneFindings, err := pruneStaleEntries(m.root, previous, next, m.dryRun)
	if err != nil {
		return Manifest{}, changes, findings, err
	}
	changes = append(changes, pruneChanges...)
	findings = append(findings, pruneFindings...)

	entries := append([]ProjectionEntry(nil), m.entries...)
	old := make(map[string]ProjectionEntry, len(previous.Entries))
	for _, entry := range previous.Entries {
		old[entry.ProjectedPath] = entry
	}
	for i := range entries {
		if err := ctx.Err(); err != nil {
			return Manifest{}, changes, findings, err
		}
		if m.reuse && reusableProjectionEntry(m.root, old[entries[i].ProjectedPath], entries[i]) {
			entries[i] = old[entries[i].ProjectedPath]
			continue
		}
		entry, change, entryFindings, err := m.materializeEntry(entries[i])
		if err != nil {
			return Manifest{}, changes, findings, err
		}
		entries[i] = entry
		if change.Action != "" {
			changes = append(changes, change)
		}
		findings = append(findings, entryFindings...)
	}
	orphanDirChanges, orphanDirFindings, err := pruneEmptyOrphanDirectories(m.root, entries, m.dryRun)
	if err != nil {
		return Manifest{}, changes, findings, err
	}
	changes = append(changes, orphanDirChanges...)
	findings = append(findings, orphanDirFindings...)
	allFindings := append(append([]Finding{}, m.findings...), findings...)
	manifest := NewManifest(m.root, m.now, entries, allFindings)
	if !m.dryRun {
		if err := writeManifest(m.root, manifest); err != nil {
			return Manifest{}, changes, findings, err
		}
		if err := finalizePermissions(m.root); err != nil {
			return Manifest{}, changes, findings, err
		}
		if err := prepareProjectionDir(m.root, filepath.Join(m.root, ".loom")); err != nil {
			return Manifest{}, changes, findings, err
		}
		if err := os.Remove(filepath.Join(m.root, ".loom", "rebuild-pending")); err != nil {
			return Manifest{}, changes, findings, err
		}
	}
	return manifest, changes, findings, nil
}

func (m materializer) materializeEntry(entry ProjectionEntry) (ProjectionEntry, Change, []Finding, error) {
	change := Change{
		Action: "copy",
		Path:   entry.ProjectedPath,
		Source: entry.SourcePath,
		Status: "planned",
	}
	if m.dryRun {
		return entry, change, nil, nil
	}
	if strings.TrimSpace(entry.SourcePath) == "" {
		return m.materializeEntryFromRetainedSource(entry, Change{Action: "copy", Path: entry.ProjectedPath, Status: "missing_source", Message: "source path is empty"})
	}
	info, err := os.Stat(entry.SourcePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return m.materializeEntryFromRetainedSource(entry, Change{Action: "copy", Path: entry.ProjectedPath, Source: entry.SourcePath, Status: "missing_source"})
		}
		return entry, Change{}, nil, fmt.Errorf("stat notes projection source %s: %w", entry.SourcePath, err)
	}
	if info.IsDir() || entry.FileClass == storagecatalog.FileClassDirectory {
		entry.CopyMode = CopyModeSkipped
		entry.Status = ProjectionStatusSkipped
		return entry, Change{Action: "skip", Path: entry.ProjectedPath, Source: entry.SourcePath, Status: "directory"}, []Finding{{
			Severity: SeverityInfo,
			Kind:     "directory_skipped",
			Path:     entry.ProjectedPath,
			Summary:  "Directory knowledge object is represented by metadata only and was not copied into the projection.",
		}}, nil
	}
	if !info.Mode().IsRegular() {
		entry.CopyMode = CopyModeSkipped
		entry.Status = ProjectionStatusSkipped
		return entry, Change{Action: "skip", Path: entry.ProjectedPath, Source: entry.SourcePath, Status: "not_regular"}, []Finding{{
			Severity: SeverityWarning,
			Kind:     "source_not_regular",
			Path:     entry.ProjectedPath,
			Summary:  "Source path is not a regular file and was not copied into the projection.",
		}}, nil
	}
	if err := copyRegularFile(m.root, entry.SourcePath, entry.FilesystemPath, m.copyLimit(entry)); err != nil {
		return entry, Change{}, nil, err
	}
	entry.Status = ProjectionStatusMaterialized
	entry.CopyMode = CopyModeFileCopy
	materializedAt := m.now
	entry.MaterializedAt = &materializedAt
	return entry, Change{Action: "copy", Path: entry.ProjectedPath, Source: entry.SourcePath, Status: "materialized"}, nil, nil
}

func (m materializer) materializeEntryFromRetainedSource(entry ProjectionEntry, missing Change) (ProjectionEntry, Change, []Finding, error) {
	switch {
	case storagecatalog.IsFilesystemPhysicalRefKind(entry.SourceRefKind) && strings.TrimSpace(entry.SourceRefURI) != "":
		source := strings.TrimSpace(entry.SourceRefURI)
		info, err := os.Stat(source)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return missingProjectionEntry(entry, missing, "source_not_found", fmt.Sprintf("Source file is not available at %s.", firstNonEmpty(entry.SourcePath, source)))
			}
			return entry, Change{}, nil, fmt.Errorf("stat notes projection retained source %s: %w", source, err)
		}
		if info.IsDir() {
			entry.CopyMode = CopyModeSkipped
			entry.Status = ProjectionStatusSkipped
			return entry, Change{Action: "skip", Path: entry.ProjectedPath, Source: source, Status: "directory"}, []Finding{{
				Severity: SeverityInfo,
				Kind:     "directory_skipped",
				Path:     entry.ProjectedPath,
				Summary:  "Directory knowledge object is represented by metadata only and was not copied into the projection.",
			}}, nil
		}
		if !info.Mode().IsRegular() {
			entry.CopyMode = CopyModeSkipped
			entry.Status = ProjectionStatusSkipped
			return entry, Change{Action: "skip", Path: entry.ProjectedPath, Source: source, Status: "not_regular"}, []Finding{{
				Severity: SeverityWarning,
				Kind:     "source_not_regular",
				Path:     entry.ProjectedPath,
				Summary:  "Retained source path is not a regular file and was not copied into the projection.",
			}}, nil
		}
		if err := copyRegularFile(m.root, source, entry.FilesystemPath, m.copyLimit(entry)); err != nil {
			return entry, Change{}, nil, err
		}
		entry.Status = ProjectionStatusMaterialized
		entry.CopyMode = CopyModeFileCopy
		materializedAt := m.now
		entry.MaterializedAt = &materializedAt
		return entry, Change{Action: "copy", Path: entry.ProjectedPath, Source: source, Status: "materialized", Message: "retained source"}, nil, nil
	case entry.SourceRefKind == storagecatalog.PhysicalRefKindBackupArtifact && strings.TrimSpace(entry.SourceRefURI) != "":
		source := strings.TrimSpace(entry.SourceRefURI)
		member := firstNonEmpty(entry.SourceRefMember, "content")
		if err := copyBackupArtifactMember(m.root, source, member, entry.FilesystemPath, m.copyLimit(entry)); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return missingProjectionEntry(entry, missing, "source_not_found", fmt.Sprintf("Source file is not available at %s.", firstNonEmpty(entry.SourcePath, source)))
			}
			return entry, Change{}, nil, err
		}
		entry.Status = ProjectionStatusMaterialized
		entry.CopyMode = CopyModeFileCopy
		materializedAt := m.now
		entry.MaterializedAt = &materializedAt
		return entry, Change{Action: "copy", Path: entry.ProjectedPath, Source: source, Status: "materialized", Message: "backup artifact"}, nil, nil
	default:
		if strings.TrimSpace(entry.SourcePath) == "" {
			return missingProjectionEntry(entry, missing, "source_path_missing", "Knowledge object does not have an available source path for projection.")
		}
		return missingProjectionEntry(entry, missing, "source_not_found", fmt.Sprintf("Source file is not available at %s.", entry.SourcePath))
	}
}

func missingProjectionEntry(entry ProjectionEntry, change Change, kind, summary string) (ProjectionEntry, Change, []Finding, error) {
	entry.CopyMode = CopyModeMissing
	entry.Status = ProjectionStatusMissing
	if strings.TrimSpace(change.Status) == "" {
		change.Status = "missing_source"
	}
	if strings.TrimSpace(change.Message) == "" {
		change.Message = summary
	}
	return entry, change, []Finding{{
		Severity: SeverityWarning,
		Kind:     kind,
		Path:     entry.ProjectedPath,
		Summary:  summary,
	}}, nil
}

func projectedPathForSource(source SourceObject) (string, error) {
	relativePath, err := storageview.SanitizeRelativePath(source.RelativePath)
	if err != nil {
		return "", fmt.Errorf("invalid relative path: %w", err)
	}
	switch strings.TrimSpace(source.RootKind) {
	case RootKindBoxNotes:
		node := storageview.SanitizeLabel(firstNonEmpty(source.SourceNodeKey, "unknown-node"))
		return path.Join("nodes", node, "Notes", relativePath), nil
	case RootKindProjectNotes:
		project := firstNonEmpty(source.ProjectSlug, ptrValue(source.ProjectID), "unknown-project")
		return path.Join("projects", storageview.SanitizeLabel(project), "notes", relativePath), nil
	case RootKindBoxTopics, RootKindBoxLibrary, RootKindProjectMaterial:
		rootPath, err := storageview.SanitizeRelativePath(source.RootRelativePath)
		if err != nil {
			return "", fmt.Errorf("invalid declared root path: %w", err)
		}
		if source.RootKind == RootKindProjectMaterial {
			if source.Declaration != "notes" && source.Declaration != "docs" && source.Declaration != "research" {
				return "", fmt.Errorf("invalid material declaration")
			}
			if rootPath != source.Declaration && !strings.HasPrefix(rootPath, source.Declaration+"/") {
				return "", fmt.Errorf("material root differs from declaration")
			}
			project := firstNonEmpty(source.ProjectSlug, ptrValue(source.ProjectID))
			if project == "" {
				return "", fmt.Errorf("material project identity is missing")
			}
			return path.Join("projects", storageview.SanitizeLabel(project), rootPath, relativePath), nil
		}
		area := "Topics"
		if source.RootKind == RootKindBoxLibrary {
			area = "Library"
		}
		if rootPath != area && !strings.HasPrefix(rootPath, area+"/") {
			return "", fmt.Errorf("source root differs from declared area")
		}
		node := storageview.SanitizeLabel(firstNonEmpty(source.SourceNodeKey, "unknown-node"))
		return path.Join("nodes", node, rootPath, relativePath), nil
	default:
		return "", fmt.Errorf("unsupported notes source root kind %q", source.RootKind)
	}
}

func deduplicateProjectedPath(projectedPath, objectID string, used map[string]int) string {
	projectedPath = strings.Trim(projectedPath, "/")
	if used[projectedPath] == 0 {
		used[projectedPath] = 1
		return projectedPath
	}
	used[projectedPath]++
	dir := path.Dir(projectedPath)
	if dir == "." {
		dir = ""
	}
	base := path.Base(projectedPath)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if stem == "" {
		stem = base
	}
	candidateBase := fmt.Sprintf("%s (%s)%s", stem, shortID(objectID), ext)
	candidate := candidateBase
	if dir != "" {
		candidate = path.Join(dir, candidateBase)
	}
	for used[candidate] > 0 {
		used[projectedPath]++
		candidateBase = fmt.Sprintf("%s (%s-%d)%s", stem, shortID(objectID), used[projectedPath], ext)
		candidate = candidateBase
		if dir != "" {
			candidate = path.Join(dir, candidateBase)
		}
	}
	used[candidate] = 1
	return candidate
}

func pruneStaleEntries(root string, previous Manifest, next map[string]ProjectionEntry, dryRun bool) ([]Change, []Finding, error) {
	var changes []Change
	var findings []Finding
	for _, previousEntry := range previous.Entries {
		if _, ok := next[previousEntry.ProjectedPath]; ok {
			continue
		}
		target, err := safeJoin(root, previousEntry.ProjectedPath)
		if err != nil {
			findings = append(findings, Finding{Severity: SeverityWarning, Kind: "stale_path_unsafe", Path: previousEntry.ProjectedPath, Summary: err.Error()})
			continue
		}
		change := Change{Action: "remove_stale", Path: previousEntry.ProjectedPath, Status: "planned"}
		if !dryRun {
			err := os.Remove(target)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return changes, findings, fmt.Errorf("remove stale projection entry %s: %w", target, err)
			}
			change.Status = "removed"
		}
		changes = append(changes, change)
	}
	return changes, findings, nil
}

func pruneEmptyOrphanDirectories(root string, entries []ProjectionEntry, dryRun bool) ([]Change, []Finding, error) {
	var changes []Change
	var findings []Finding
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || root == "." {
		return changes, findings, nil
	}
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return changes, findings, nil
	} else if err != nil {
		return changes, findings, fmt.Errorf("inspect notes projection root for orphan directories: %w", err)
	}

	required := map[string]bool{root: true}
	loomDir := filepath.Join(root, ".loom")
	required[loomDir] = true
	for _, entry := range entries {
		if strings.TrimSpace(entry.ProjectedPath) == "" {
			continue
		}
		target, err := safeJoin(root, entry.ProjectedPath)
		if err != nil {
			findings = append(findings, Finding{Severity: SeverityWarning, Kind: "orphan_directory_path_unsafe", Path: entry.ProjectedPath, Summary: err.Error()})
			continue
		}
		dir := filepath.Dir(target)
		for {
			required[dir] = true
			if dir == root || dir == "." || dir == string(filepath.Separator) {
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	var dirs []string
	if err := filepath.WalkDir(root, func(pathValue string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if pathValue == root {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if pathValue == loomDir || strings.HasPrefix(pathValue, loomDir+string(filepath.Separator)) {
			return filepath.SkipDir
		}
		dirs = append(dirs, pathValue)
		return nil
	}); err != nil {
		return changes, findings, fmt.Errorf("walk notes projection orphan directories: %w", err)
	}
	sortPathsDeepestFirst(dirs)
	for _, dir := range dirs {
		if required[dir] {
			continue
		}
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			findings = append(findings, Finding{Severity: SeverityWarning, Kind: "orphan_directory_path_unsafe", Path: dir, Summary: err.Error()})
			continue
		}
		rel = filepath.ToSlash(rel)
		empty, err := isDirEmpty(dir)
		if err != nil {
			findings = append(findings, Finding{Severity: SeverityWarning, Kind: "orphan_directory_unreadable", Path: rel, Summary: err.Error()})
			continue
		}
		if !empty {
			findings = append(findings, Finding{Severity: SeverityWarning, Kind: "orphan_directory_non_empty", Path: rel, Summary: "Unexpected projection directory is not empty and was preserved."})
			continue
		}
		change := Change{Action: "remove_orphan_dir", Path: rel, Status: "planned", Message: "empty generated projection directory"}
		if !dryRun {
			if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
				return changes, findings, fmt.Errorf("remove empty orphan projection directory %s: %w", dir, err)
			}
			change.Status = "removed"
		}
		changes = append(changes, change)
	}
	return changes, findings, nil
}

func (m materializer) copyLimit(entry ProjectionEntry) int64 {
	if m.reuse && entry.SizeBytes != nil {
		return *entry.SizeBytes
	}
	return -1
}

func copyProjectionBytes(out io.Writer, in io.Reader, expected int64) error {
	if expected < 0 {
		_, err := io.Copy(out, in)
		return err
	}
	n, err := io.Copy(out, io.LimitReader(in, expected+1))
	if err == nil && n != expected {
		return fmt.Errorf("source size changed during automatic projection")
	}
	return err
}

func copyRegularFile(root, source, target string, expected int64) error {
	if err := prepareProjectionDir(root, filepath.Dir(target)); err != nil {
		return fmt.Errorf("create projection parent directory: %w", err)
	}
	if err := os.Chmod(target, 0o644); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("prepare projected file for rewrite %s: %w", target, err)
	}
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open notes projection source %s: %w", source, err)
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(target), ".loom-copy-*")
	if err != nil {
		return fmt.Errorf("open temporary projected file: %w", err)
	}
	tmp := out.Name()
	defer func() { _ = out.Close(); _ = os.Remove(tmp) }()
	if err := copyProjectionBytes(out, in, expected); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy notes projection file %s: %w", source, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close temporary projected file %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, target); err != nil {
		return fmt.Errorf("replace projected file %s: %w", target, err)
	}
	return os.Chmod(target, 0o444)
}

func copyBackupArtifactMember(root, source, member, target string, expected int64) error {
	if err := prepareProjectionDir(root, filepath.Dir(target)); err != nil {
		return fmt.Errorf("create projection parent directory: %w", err)
	}
	if err := os.Chmod(target, 0o644); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("prepare projected file for rewrite %s: %w", target, err)
	}
	file, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open notes projection backup artifact %s: %w", source, err)
	}
	defer file.Close()
	out, err := os.CreateTemp(filepath.Dir(target), ".loom-copy-*")
	if err != nil {
		return fmt.Errorf("open temporary projected file: %w", err)
	}
	tmp := out.Name()
	defer func() {
		_ = out.Close()
		_ = os.Remove(tmp)
	}()
	reader := tar.NewReader(file)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("backup artifact member %q was not found", member)
		}
		if err != nil {
			return fmt.Errorf("read notes projection backup artifact %s: %w", source, err)
		}
		if header == nil || header.Name != member {
			continue
		}
		if header.FileInfo().IsDir() {
			return fmt.Errorf("backup artifact member %q is a directory", member)
		}
		if err := copyProjectionBytes(out, reader, expected); err != nil {
			return fmt.Errorf("copy notes projection backup artifact %s: %w", source, err)
		}
		if err := out.Close(); err != nil {
			return fmt.Errorf("close temporary projected file %s: %w", tmp, err)
		}
		if err := os.Rename(tmp, target); err != nil {
			return fmt.Errorf("replace projected file %s: %w", target, err)
		}
		return os.Chmod(target, 0o444)
	}
}

func writeFileAtomic(root, target string, payload []byte, mode os.FileMode) error {
	if err := prepareProjectionDir(root, filepath.Dir(target)); err != nil {
		return err
	}
	if err := os.Chmod(target, 0o644); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(target), ".loom-metadata-*")
	if err != nil {
		return err
	}
	tmp := out.Name()
	defer func() { _ = out.Close(); _ = os.Remove(tmp) }()
	if _, err := out.Write(payload); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		return err
	}
	return os.Chmod(target, mode)
}

// mkdir's mode is filtered by inherited default ACLs. Repair owner access on
// each held directory before descending; do not make group/agents writable or
// follow a linked parent into another tree.
func prepareProjectionDir(root, dir string) error {
	root, dir = filepath.Clean(root), filepath.Clean(dir)
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("projection parent escapes root")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.Fchmod(fd, 0o755); err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	for _, name := range strings.Split(rel, string(filepath.Separator)) {
		if err := unix.Mkdirat(fd, name, 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
			return err
		}
		next, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		_ = unix.Close(fd)
		fd = next
		if err := unix.Fchmod(fd, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func projectionCurrent(root string, previous Manifest, entries []ProjectionEntry) bool {
	if previous.SchemaVersion != ManifestSchemaVersion || previous.ProjectionRoot != root || !previous.ReadOnly || len(previous.Entries) != len(entries) {
		return false
	}
	if _, err := os.Lstat(filepath.Join(root, ".loom", "rebuild-pending")); !errors.Is(err, os.ErrNotExist) {
		return false
	}
	if info, err := os.Lstat(root); err != nil || !info.IsDir() || info.Mode().Perm() != 0o555 {
		return false
	}
	for i := range entries {
		if !reusableProjectionEntry(root, previous.Entries[i], entries[i]) {
			return false
		}
	}
	return true
}

func reusableProjectionEntry(root string, previous, next ProjectionEntry) bool {
	if previous.Status != ProjectionStatusMaterialized || previous.CopyMode != CopyModeFileCopy || previous.SourceHash == "" || previous.SourceRevision == "" {
		return false
	}
	identity := func(entry ProjectionEntry) ProjectionEntry {
		entry.Status, entry.CopyMode = "", ""
		entry.MaterializedAt = nil
		entry.LastSeenAt = time.Time{}
		return entry
	}
	if !reflect.DeepEqual(identity(previous), identity(next)) {
		return false
	}
	target, err := safeJoin(root, next.ProjectedPath)
	if err != nil {
		return false
	}
	info, err := os.Lstat(target)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm() == 0o444 && next.SizeBytes != nil && info.Size() == *next.SizeBytes
}

func makeExistingDirsWritable(root string) error {
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return filepath.WalkDir(root, func(pathValue string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		return os.Chmod(pathValue, 0o755)
	})
}

func finalizePermissions(root string) error {
	var dirs []string
	if err := filepath.WalkDir(root, func(pathValue string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			dirs = append(dirs, pathValue)
			return nil
		}
		if err := os.Chmod(pathValue, 0o444); err != nil {
			return fmt.Errorf("set projected file read-only %s: %w", pathValue, err)
		}
		return nil
	}); err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i], 0o555); err != nil {
			return fmt.Errorf("set projection directory read-only %s: %w", dirs[i], err)
		}
	}
	return nil
}

func sortPathsDeepestFirst(paths []string) {
	sort.Slice(paths, func(i, j int) bool {
		depthI := strings.Count(paths[i], string(filepath.Separator))
		depthJ := strings.Count(paths[j], string(filepath.Separator))
		if depthI == depthJ {
			return paths[i] > paths[j]
		}
		return depthI > depthJ
	})
}

func isDirEmpty(pathValue string) (bool, error) {
	dir, err := os.Open(pathValue)
	if err != nil {
		return false, err
	}
	defer dir.Close()
	_, err = dir.Readdirnames(1)
	if errors.Is(err, io.EOF) {
		return true, nil
	}
	return false, err
}

func safeJoin(root, relativePath string) (string, error) {
	relativePath, err := storageview.SanitizeRelativePath(relativePath)
	if err != nil {
		return "", err
	}
	target := filepath.Join(root, filepath.FromSlash(relativePath))
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", err
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", fmt.Errorf("path escapes projection root")
	}
	return target, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func ptrValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func shortID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "object"
	}
	if len(id) <= 10 {
		return id
	}
	return id[len(id)-10:]
}
