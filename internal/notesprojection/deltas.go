package notesprojection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

var (
	ErrDeltaUncertain              = errors.New("projection delta evidence is uncertain")
	ErrProjectionReconcileRequired = errors.New("projection full reconciliation required")
)

// UncertainSourcesError isolates unavailable identities without re-reading every
// member of the same batch recursively. It is never withdrawal evidence.
type UncertainSourcesError struct {
	ObjectIDs []string
}

func (e *UncertainSourcesError) Error() string {
	return "projection delta evidence is uncertain: targeted source evidence is unavailable"
}
func (e *UncertainSourcesError) Unwrap() error { return ErrDeltaUncertain }

func readProjectionDelta(ctx context.Context, provider TargetedSourceProvider, ids []string) (SourceDelta, error) {
	delta, err := provider.ReadProjectionSources(ctx, ids)
	if err != nil {
		return SourceDelta{}, err
	}
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	for _, source := range delta.Sources {
		if !wanted[source.KnowledgeObjectID] {
			return SourceDelta{}, ErrDeltaUncertain
		}
		delete(wanted, source.KnowledgeObjectID)
	}
	for id, reason := range delta.Withdrawn {
		if !wanted[id] || reason == "" {
			return SourceDelta{}, ErrDeltaUncertain
		}
		delete(wanted, id)
	}
	if len(wanted) > 0 {
		return SourceDelta{}, ErrDeltaUncertain
	}
	return delta, nil
}

// ApplyDelta changes only explicitly selected identities and collision peers.
// The existing schema-v1 manifest remains the reader/repair contract. Metadata
// allocation may traverse that manifest, but unrelated files are never statted.
func (s Service) ApplyDelta(ctx context.Context, objectIDs []string) (changed bool, resultErr error) {
	provider, ok := s.Sources.(TargetedSourceProvider)
	if !ok {
		return false, fmt.Errorf("targeted projection source provider is required")
	}
	root, err := normalizeProjectionRoot(s.ProjectionRoot)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return false, err
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, err
	}
	defer unix.Close(fd)
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return false, fmt.Errorf("Notes projection is busy: %w", err)
	}
	defer unix.Flock(fd, unix.LOCK_UN)
	selected := map[string]bool{}
	for _, id := range objectIDs {
		if strings.TrimSpace(id) == "" {
			return false, fmt.Errorf("empty delta identity")
		}
		selected[id] = true
	}
	// Crash recovery records the exact affected set before any payload mutation.
	// Replay merges it with new dirty work; legacy full-rebuild markers require
	// the existing complete repair route rather than guessing their scope.
	marker := filepath.Join(root, ".loom", "rebuild-pending")
	payload, err := os.ReadFile(marker)
	if err == nil {
		var replay []string
		if json.Unmarshal(payload, &replay) != nil || len(replay) == 0 {
			return false, ErrProjectionReconcileRequired
		}
		for _, id := range replay {
			if id == "" {
				return false, ErrProjectionReconcileRequired
			}
			selected[id] = true
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if len(selected) == 0 {
		return false, nil
	}
	if len(selected) > MaxDeltaIdentities {
		return false, ErrProjectionReconcileRequired
	}
	// Refused identities must not decode the entire generated view on every
	// retry. Read their bounded authority evidence first, under the same lock;
	// successful evidence is reused by the first planning pass below.
	admissionCtx, cancelAdmission := context.WithTimeout(ctx, 2*time.Second)
	defer cancelAdmission()
	initialIDs := make([]string, 0, len(selected))
	for id := range selected {
		initialIDs = append(initialIDs, id)
	}
	sort.Strings(initialIDs)
	initialDelta, err := readProjectionDelta(admissionCtx, provider, initialIDs)
	if err != nil {
		return false, err
	}
	cachedDelta := &initialDelta
	previous, found, err := ReadManifest(root)
	if err != nil || !found || previous.SchemaVersion != ManifestSchemaVersion || previous.ProjectionRoot != root || !previous.ReadOnly {
		return false, ErrProjectionReconcileRequired
	}
	old := map[string]ProjectionEntry{}
	sources := map[string]SourceObject{}
	paths := map[string]bool{}
	for _, entry := range previous.Entries {
		if entry.KnowledgeObjectID == "" || paths[entry.ProjectedPath] {
			return false, ErrProjectionReconcileRequired
		}
		if _, duplicate := old[entry.KnowledgeObjectID]; duplicate {
			return false, ErrProjectionReconcileRequired
		}
		target, err := safeJoin(root, entry.ProjectedPath)
		if err != nil || target != entry.FilesystemPath {
			return false, ErrProjectionReconcileRequired
		}
		paths[entry.ProjectedPath] = true
		old[entry.KnowledgeObjectID] = entry
		sources[entry.KnowledgeObjectID] = sourceFromProjectionEntry(entry)
	}
	loaded := map[string]bool{}
	var entries []ProjectionEntry
	// Once payload work starts, retain the caller's action deadline rather than
	// the short metadata/planning admission budget.
	for {
		if err := admissionCtx.Err(); err != nil {
			return false, err
		}
		var ids []string
		for id := range selected {
			if !loaded[id] {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		if len(ids) > 0 {
			var delta SourceDelta
			if cachedDelta != nil {
				delta, cachedDelta = *cachedDelta, nil
			} else {
				delta, err = readProjectionDelta(admissionCtx, provider, ids)
				if err != nil {
					return false, err
				}
			}
			for _, source := range delta.Sources {
				id := source.KnowledgeObjectID
				sources[id] = source
			}
			for id := range delta.Withdrawn {
				delete(sources, id)
			}
			for _, id := range ids {
				loaded[id] = true
			}
		}
		ordered := make([]SourceObject, 0, len(sources))
		for _, source := range sources {
			ordered = append(ordered, source)
		}
		sort.Slice(ordered, func(i, j int) bool { return projectionSourceOrder(ordered[i]) < projectionSourceOrder(ordered[j]) })
		var findings []Finding
		entries, findings = BuildProjectionEntries(root, ordered, s.now())
		if len(findings) > 0 {
			return false, ErrProjectionReconcileRequired
		}
		expanded := false
		for _, entry := range entries {
			prior, exists := old[entry.KnowledgeObjectID]
			if exists && prior.ProjectedPath != entry.ProjectedPath && !selected[entry.KnowledgeObjectID] {
				selected[entry.KnowledgeObjectID] = true
				expanded = true
			}
		}
		if len(selected) > MaxDeltaIdentities {
			return false, ErrProjectionReconcileRequired
		}
		if !expanded {
			break
		}
	}
	if err := admissionCtx.Err(); err != nil {
		return false, err
	}
	cancelAdmission()
	touched := map[string]bool{root: true, filepath.Join(root, ".loom"): true}
	touchedFiles := map[string]bool{}
	touchParents := func(relative string) error {
		target, err := safeJoin(root, relative)
		if err != nil {
			return err
		}
		if info, err := os.Lstat(target); err == nil && !info.Mode().IsRegular() {
			return ErrDeltaUncertain
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		touchedFiles[target] = true
		for dir := filepath.Dir(target); ; dir = filepath.Dir(dir) {
			touched[dir] = true
			if dir == root {
				break
			}
		}
		return nil
	}
	for id := range selected {
		if prior, ok := old[id]; ok {
			if err := touchParents(prior.ProjectedPath); err != nil {
				return false, err
			}
		}
	}
	for _, entry := range entries {
		if selected[entry.KnowledgeObjectID] {
			if err := touchParents(entry.ProjectedPath); err != nil {
				return false, err
			}
		}
	}
	// Restore permissions only on owned, affected ancestors, including failures.
	defer func() {
		resultErr = errors.Join(resultErr, finalizeDeltaFiles(root, touchedFiles), finalizeDeltaDirectories(root, touched))
	}()
	replay := make([]string, 0, len(selected))
	for id := range selected {
		replay = append(replay, id)
	}
	sort.Strings(replay)
	payload, err = json.Marshal(replay)
	if err != nil {
		return false, err
	}
	if err := writeFileAtomic(root, marker, payload, 0o444); err != nil {
		return false, err
	}
	nextPaths := map[string]ProjectionEntry{}
	for _, entry := range entries {
		nextPaths[entry.ProjectedPath] = entry
	}
	var findings []Finding
	// Preserve existing findings for unaffected entries; a current affected entry
	// gets its freshly observed findings rather than stale errors.
	affectedPaths := map[string]bool{}
	for id := range selected {
		if prior, ok := old[id]; ok {
			affectedPaths[prior.ProjectedPath] = true
		}
	}
	for _, entry := range entries {
		if selected[entry.KnowledgeObjectID] {
			affectedPaths[entry.ProjectedPath] = true
		}
	}
	for _, finding := range previous.Findings {
		if !affectedPaths[finding.Path] {
			findings = append(findings, finding)
		}
	}
	m := materializer{root: root, now: s.now(), reuse: true}
	for i, entry := range entries {
		if !selected[entry.KnowledgeObjectID] {
			entries[i] = old[entry.KnowledgeObjectID]
			continue
		}
		prior := old[entry.KnowledgeObjectID]
		if reusableProjectionEntry(root, prior, entry) {
			entries[i] = prior
			continue
		}
		if entry.SizeBytes == nil || *entry.SizeBytes < 0 {
			return false, ErrDeltaUncertain
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		current, change, entryFindings, err := m.materializeEntry(entry)
		if err != nil {
			return false, err
		}
		// Unavailable bytes do not prove withdrawal or a successful replacement.
		if current.Status != ProjectionStatusMaterialized {
			return false, ErrDeltaUncertain
		}
		entries[i] = current
		findings = append(findings, entryFindings...)
		changed = changed || change.Action != ""
	}
	// Remove only old paths of selected identities after replacement copies.
	// Omission from a partial/failed read never reaches this point.
	for id := range selected {
		prior, ok := old[id]
		if !ok {
			continue
		}
		if _, retained := nextPaths[prior.ProjectedPath]; retained {
			continue
		}
		target, err := safeJoin(root, prior.ProjectedPath)
		if err != nil {
			return false, err
		}
		if err := prepareProjectionDir(root, filepath.Dir(target)); err != nil {
			return false, err
		}
		if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		changed = true
	}
	// Only inspect affected ancestors for empty generated directories.
	dirs := make([]string, 0, len(touched))
	for dir := range touched {
		dirs = append(dirs, dir)
	}
	sortPathsDeepestFirst(dirs)
	for _, dir := range dirs {
		if dir == root || dir == filepath.Join(root, ".loom") {
			continue
		}
		empty, err := isDirEmpty(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if empty {
			if err := prepareProjectionDir(root, filepath.Dir(dir)); err != nil {
				return false, err
			}
			if err := os.Remove(dir); err != nil {
				return false, err
			}
		}
	}
	manifest := NewManifest(root, s.now(), entries, findings)
	if err := writeManifest(root, manifest); err != nil {
		return false, err
	}
	if err := os.Remove(marker); err != nil {
		return false, err
	}
	return changed, nil
}

func finalizeDeltaDirectories(root string, touched map[string]bool) error {
	dirs := make([]string, 0, len(touched))
	for dir := range touched {
		dirs = append(dirs, dir)
	}
	sortPathsDeepestFirst(dirs)
	var result error
	for _, dir := range dirs {
		fd, err := openDeltaDescendant(root, dir, true)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err == nil {
			err = unix.Fchmod(fd, 0o555)
			unix.Close(fd)
		}
		result = errors.Join(result, err)
	}
	return result
}

func finalizeDeltaFiles(root string, files map[string]bool) error {
	var result error
	for file := range files {
		fd, err := openDeltaDescendant(root, file, false)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err == nil {
			err = unix.Fchmod(fd, 0o444)
			unix.Close(fd)
		}
		result = errors.Join(result, err)
	}
	return result
}

// Hold and traverse each parent without following links. Failure cleanup must
// have the same custody boundary as prepareProjectionDir, including ancestors.
func openDeltaDescendant(root, target string, directory bool) (int, error) {
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return -1, fmt.Errorf("projection descendant escapes root")
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil || rel == "." {
		return fd, err
	}
	names := strings.Split(rel, string(filepath.Separator))
	for i, name := range names {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
		if directory || i < len(names)-1 {
			flags |= unix.O_DIRECTORY
		}
		next, err := unix.Openat(fd, name, flags, 0)
		unix.Close(fd)
		if err != nil {
			return -1, err
		}
		fd = next
	}
	if !directory {
		var info unix.Stat_t
		if err := unix.Fstat(fd, &info); err != nil {
			unix.Close(fd)
			return -1, err
		}
		if info.Mode&unix.S_IFMT != unix.S_IFREG {
			unix.Close(fd)
			return -1, ErrDeltaUncertain
		}
	}
	return fd, nil
}

func projectionSourceOrder(s SourceObject) string {
	return strings.Join([]string{s.RootKind, s.SourceNodeKey, firstNonEmpty(s.ProjectSlug, ptrValue(s.ProjectID)), s.RelativePath, s.KnowledgeObjectID}, "\x00")
}

func sourceFromProjectionEntry(e ProjectionEntry) SourceObject {
	rootPath := e.RootRelativePath
	// Schema-v1 readers predate root_relative_path. Recover it from the declared
	// generated path for old Topics/Library/material entries without a DB scan.
	if rootPath == "" && (e.RootKind == RootKindBoxTopics || e.RootKind == RootKindBoxLibrary || e.RootKind == RootKindProjectMaterial) {
		prefix := "nodes/" + strings.SplitN(strings.TrimPrefix(e.ProjectedPath, "nodes/"), "/", 2)[0] + "/"
		if e.RootKind == RootKindProjectMaterial {
			prefix = "projects/" + strings.SplitN(strings.TrimPrefix(e.ProjectedPath, "projects/"), "/", 2)[0] + "/"
		}
		base := strings.TrimPrefix(e.ProjectedPath, prefix)
		// Deduplicated basenames can differ; directory suffix is stable.
		parent := path.Dir(e.RelativePath)
		dir := path.Dir(base)
		if parent != "." {
			rootPath = strings.TrimSuffix(dir, "/"+parent)
		} else {
			rootPath = dir
		}
	}
	return SourceObject{RootRelativePath: rootPath, SourceCategory: e.SourceCategory, SourcePosture: e.SourcePosture, Declaration: e.Declaration, TopicKey: e.TopicKey, CollectionKey: e.CollectionKey, KnowledgeObjectID: e.KnowledgeObjectID, NotesSourceRootID: e.NotesSourceRootID, RootKind: e.RootKind, SourceNodeKey: e.SourceNodeKey, ProjectID: e.ProjectID, ProjectSlug: e.ProjectSlug, StorageEntryID: e.StorageEntryID, SourcePath: e.SourcePath, SourceRefKind: e.SourceRefKind, SourceRefURI: e.SourceRefURI, SourceRefMember: e.SourceRefMember, RelativePath: e.RelativePath, Title: e.Title, FileClass: e.FileClass, MimeType: e.MimeType, SizeBytes: e.SizeBytes, SourceHash: e.SourceHash, SourceRevision: e.SourceRevision, LastSeenAt: e.LastSeenAt}
}
