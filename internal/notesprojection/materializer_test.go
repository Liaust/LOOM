package notesprojection

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"loom.local/loom/internal/storagecatalog"
)

func TestBoxSourceProjectionPathsAndContext(t *testing.T) {
	for _, test := range []struct{ kind, root, decl, want string }{
		{RootKindBoxNotes, "", "", "nodes/main/Notes/a.md"},
		{RootKindProjectNotes, "", "", "projects/atlas/notes/a.md"},
		{RootKindBoxTopics, "Topics/cadence", "", "nodes/main/Topics/cadence/a.md"},
		{RootKindBoxLibrary, "Library", "", "nodes/main/Library/a.md"},
		{RootKindProjectMaterial, "docs/review", "docs", "projects/atlas/docs/review/a.md"},
		{RootKindProjectMaterial, "research", "research", "projects/atlas/research/a.md"},
	} {
		source := SourceObject{RootKind: test.kind, RootRelativePath: test.root, Declaration: test.decl, RelativePath: "a.md", SourceNodeKey: "main", ProjectSlug: "atlas"}
		got, err := projectedPathForSource(source)
		if err != nil || got != test.want {
			t.Fatalf("projection %s: %s %v", test.kind, got, err)
		}
		if test.root != "" {
			source.RootRelativePath = "../escape"
			if _, err := projectedPathForSource(source); err == nil {
				t.Fatal("accepted unbound projection root")
			}
		}
	}
}

func TestProjectionRefreshReusesUnchangedFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "generated")
	defer makeProjectionWritableForCleanup(t, root)
	source := writeProjectionSource(t, "note.md", "original")
	size := int64(len("original"))
	provider := fakeProjectionSources{sources: []SourceObject{{
		KnowledgeObjectID: "knowledge_object_refresh", NotesSourceRootID: "notes_source_root_refresh",
		RootKind: RootKindBoxNotes, SourceNodeKey: "main", SourcePath: source, RelativePath: "nested/note.md",
		SourceHash: "sha256:original", SourceRevision: "original", SizeBytes: &size,
	}}}
	svc := NewService(provider, root)
	if changed, err := svc.Refresh(t.Context()); err != nil || !changed {
		t.Fatalf("first refresh: %t %v", changed, err)
	}
	target := filepath.Join(root, "nodes/main/Notes/nested/note.md")
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := svc.Refresh(t.Context()); err != nil || changed {
		t.Fatalf("unchanged refresh: %t %v", changed, err)
	}
	after, err := os.Stat(target)
	if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("unchanged file recopied: %v", err)
	}
	provider.sources[0].SourceRevision = "changed"
	provider.sources[0].SourceHash = "sha256:changed"
	if err := os.WriteFile(source, []byte("modified"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc.Sources = provider
	if changed, err := svc.Refresh(t.Context()); err != nil || !changed {
		t.Fatalf("changed refresh: %t %v", changed, err)
	}
	assertProjectionFile(t, target, "modified")
}

func TestProjectionRefreshRefusesTruncatedInventory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "generated")
	defer makeProjectionWritableForCleanup(t, root)
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	retained := filepath.Join(root, "retained.txt")
	if err := os.WriteFile(retained, []byte("retain"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := NewService(fakeProjectionSources{sources: make([]SourceObject, 5001)}, root)
	if _, err := svc.Refresh(t.Context()); err == nil {
		t.Fatal("accepted incomplete inventory")
	}
	assertProjectionFile(t, retained, "retain")
}

func TestProjectionFailureResealsAndRecordsIncomplete(t *testing.T) {
	root := filepath.Join(t.TempDir(), "generated")
	defer makeProjectionWritableForCleanup(t, root)
	source := writeProjectionSource(t, "note.md", "body")
	if err := os.MkdirAll(filepath.Join(root, "nodes"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "nodes/main")); err != nil {
		t.Fatal(err)
	}
	svc := NewService(fakeProjectionSources{sources: []SourceObject{{RootKind: RootKindBoxNotes, SourceNodeKey: "main", RelativePath: "nested/note.md", SourcePath: source}}}, root)
	if _, err := svc.Rebuild(t.Context(), RebuildInput{}); err == nil {
		t.Fatal("linked parent accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "Notes")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("external tree touched: %v", err)
	}
	if info, err := os.Stat(root); err != nil || info.Mode().Perm() != 0o555 {
		t.Fatalf("failure did not reseal root: %v", err)
	}
	status, err := svc.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range status.Findings {
		found = found || f.Kind == "rebuild_incomplete"
	}
	if !found || status.LastRebuildAt != nil {
		t.Fatalf("failed rebuild looks complete: %+v", status)
	}
}

func TestProjectionRefreshRefusesConcurrentRebuild(t *testing.T) {
	root := t.TempDir()
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer unix.Flock(fd, unix.LOCK_UN)
	svc := NewService(fakeProjectionSources{}, root)
	if _, err := svc.Refresh(t.Context()); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("concurrent refresh: %v", err)
	}
}

func TestProjectionRefreshCopyBudgetAndSizeDrift(t *testing.T) {
	root := filepath.Join(t.TempDir(), "generated")
	defer makeProjectionWritableForCleanup(t, root)
	source := writeProjectionSource(t, "note.md", "abc")
	size := int64(64*1024*1024 + 1)
	provider := fakeProjectionSources{sources: []SourceObject{{RootKind: RootKindBoxNotes, SourceNodeKey: "main", RelativePath: "note.md", SourcePath: source, SizeBytes: &size}}}
	svc := NewService(provider, root)
	if _, err := svc.Refresh(t.Context()); err == nil || !strings.Contains(err.Error(), "copy budget") {
		t.Fatalf("oversized copy: %v", err)
	}
	if _, _, err := ReadManifest(root); err != nil {
		t.Fatal(err)
	}
	size = 1
	if _, err := svc.Refresh(t.Context()); err == nil || !strings.Contains(err.Error(), "size changed") {
		t.Fatalf("source growth not refused: %v", err)
	}
	size = 3
	if _, err := svc.Refresh(t.Context()); err != nil {
		t.Fatalf("failed copy could not retry: %v", err)
	}
	status, err := svc.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range status.Findings {
		if finding.Kind == "rebuild_incomplete" {
			t.Fatal("successful retry retained incomplete marker")
		}
	}
	assertProjectionFile(t, filepath.Join(root, "nodes/main/Notes/note.md"), "abc")
}

func TestRebuildMaterializesReadOnlyProjectionAndManifest(t *testing.T) {
	root := filepath.Join(t.TempDir(), DefaultDirectoryName)
	defer makeProjectionWritableForCleanup(t, root)
	boxSource := writeProjectionSource(t, "daily.md", "box note")
	projectSource := writeProjectionSource(t, "project.md", "project note")
	projectID := "project_test"
	provider := fakeProjectionSources{sources: []SourceObject{
		{
			KnowledgeObjectID: "knowledge_object_box",
			NotesSourceRootID: "notes_source_root_box",
			RootKind:          RootKindBoxNotes,
			SourceNodeKey:     "main",
			SourcePath:        boxSource,
			RelativePath:      "Daily/daily.md",
			FileClass:         storagecatalog.FileClassMarkdown,
			SourceHash:        "sha256:box",
		},
		{
			KnowledgeObjectID: "knowledge_object_project",
			NotesSourceRootID: "notes_source_root_project",
			RootKind:          RootKindProjectNotes,
			SourceNodeKey:     "main",
			ProjectID:         &projectID,
			ProjectSlug:       "osint-tools",
			SourcePath:        projectSource,
			RelativePath:      "runbook.md",
			FileClass:         storagecatalog.FileClassMarkdown,
			SourceHash:        "sha256:project",
		},
	}}
	svc := NewService(provider, root)
	svc.Now = fixedProjectionNow

	result, err := svc.Rebuild(context.Background(), RebuildInput{})
	if err != nil {
		t.Fatalf("Rebuild returned error: %v", err)
	}
	for _, want := range []string{
		filepath.Join(root, "nodes", "main", "Notes", "Daily", "daily.md"),
		filepath.Join(root, "projects", "osint-tools", "notes", "runbook.md"),
		filepath.Join(root, ".loom", "manifest.json"),
		filepath.Join(root, ".loom", "README.txt"),
	} {
		if _, err := os.Stat(want); err != nil {
			t.Fatalf("expected projected path %s: %v", want, err)
		}
	}
	assertProjectionFile(t, filepath.Join(root, "nodes", "main", "Notes", "Daily", "daily.md"), "box note")
	assertProjectionFile(t, filepath.Join(root, "projects", "osint-tools", "notes", "runbook.md"), "project note")
	if result.Manifest.Counts.Materialized != 2 || result.Status.Counts.Materialized != 2 {
		t.Fatalf("materialized counts = manifest %#v status %#v", result.Manifest.Counts, result.Status.Counts)
	}
	manifestPayload, err := os.ReadFile(filepath.Join(root, ".loom", "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestPayload, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if !manifest.ReadOnly || len(manifest.Entries) != 2 {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}
	entry := findProjectionEntry(manifest.Entries, "projects/osint-tools/notes/runbook.md")
	if entry == nil || entry.KnowledgeObjectID != "knowledge_object_project" || entry.SourcePath != projectSource || ptrValue(entry.ProjectID) != projectID {
		t.Fatalf("project entry did not preserve source mapping: %#v", entry)
	}
	fileInfo, err := os.Stat(filepath.Join(root, "nodes", "main", "Notes", "Daily", "daily.md"))
	if err != nil {
		t.Fatalf("stat projected file: %v", err)
	}
	if fileInfo.Mode().Perm()&0o222 != 0 {
		t.Fatalf("projected file mode = %o, want read-only", fileInfo.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Join(root, "nodes", "main", "Notes"))
	if err != nil {
		t.Fatalf("stat projected directory: %v", err)
	}
	if dirInfo.Mode().Perm()&0o222 != 0 {
		t.Fatalf("projected directory mode = %o, want read-only", dirInfo.Mode().Perm())
	}
}

func TestRebuildDryRunDoesNotCreateProjectionRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), DefaultDirectoryName)
	source := writeProjectionSource(t, "daily.md", "box note")
	svc := NewService(fakeProjectionSources{sources: []SourceObject{{
		KnowledgeObjectID: "knowledge_object_box",
		NotesSourceRootID: "notes_source_root_box",
		RootKind:          RootKindBoxNotes,
		SourceNodeKey:     "main",
		SourcePath:        source,
		RelativePath:      "daily.md",
		FileClass:         storagecatalog.FileClassMarkdown,
	}}}, root)
	svc.Now = fixedProjectionNow

	result, err := svc.Rebuild(context.Background(), RebuildInput{DryRun: true})
	if err != nil {
		t.Fatalf("Rebuild returned error: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("projection root stat error = %v, want not exist", err)
	}
	if len(result.Changes) != 1 || result.Changes[0].Status != "planned" {
		t.Fatalf("dry-run changes = %#v", result.Changes)
	}
	if result.Manifest.Counts.Entries != 1 || result.Manifest.Counts.Materialized != 0 {
		t.Fatalf("dry-run manifest counts = %#v", result.Manifest.Counts)
	}
}

func TestRebuildPrunesStaleManifestEntries(t *testing.T) {
	root := filepath.Join(t.TempDir(), DefaultDirectoryName)
	defer makeProjectionWritableForCleanup(t, root)
	oldSource := writeProjectionSource(t, "old.md", "old")
	newSource := writeProjectionSource(t, "new.md", "new")
	svc := NewService(fakeProjectionSources{sources: []SourceObject{{
		KnowledgeObjectID: "knowledge_object_old",
		NotesSourceRootID: "notes_source_root_box",
		RootKind:          RootKindBoxNotes,
		SourceNodeKey:     "main",
		SourcePath:        oldSource,
		RelativePath:      "old.md",
		FileClass:         storagecatalog.FileClassMarkdown,
	}}}, root)
	svc.Now = fixedProjectionNow
	if _, err := svc.Rebuild(context.Background(), RebuildInput{}); err != nil {
		t.Fatalf("initial Rebuild returned error: %v", err)
	}

	makeProjectionWritableForCleanup(t, root)
	svc.Sources = fakeProjectionSources{sources: []SourceObject{{
		KnowledgeObjectID: "knowledge_object_new",
		NotesSourceRootID: "notes_source_root_box",
		RootKind:          RootKindBoxNotes,
		SourceNodeKey:     "main",
		SourcePath:        newSource,
		RelativePath:      "new.md",
		FileClass:         storagecatalog.FileClassMarkdown,
	}}}
	result, err := svc.Rebuild(context.Background(), RebuildInput{})
	if err != nil {
		t.Fatalf("second Rebuild returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "nodes", "main", "Notes", "old.md")); !os.IsNotExist(err) {
		t.Fatalf("old projection stat error = %v, want not exist", err)
	}
	if _, err := os.Stat(filepath.Join(root, "nodes", "main", "Notes", "new.md")); err != nil {
		t.Fatalf("new projection missing: %v", err)
	}
	if !hasProjectionChange(result.Changes, "remove_stale") {
		t.Fatalf("expected remove_stale change: %#v", result.Changes)
	}
}

func TestRebuildPrunesEmptyOrphanProjectionDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), DefaultDirectoryName)
	defer makeProjectionWritableForCleanup(t, root)
	source := writeProjectionSource(t, "daily.md", "box note")
	emptyOrphan := filepath.Join(root, "projects", "v095-cleanup", "notes")
	nonEmptyOrphan := filepath.Join(root, "projects", "v097-cleanup", "notes")
	if err := os.MkdirAll(emptyOrphan, 0o755); err != nil {
		t.Fatalf("create empty orphan: %v", err)
	}
	if err := os.MkdirAll(nonEmptyOrphan, 0o755); err != nil {
		t.Fatalf("create non-empty orphan: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nonEmptyOrphan, "keep.md"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("write non-empty orphan file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".loom"), 0o755); err != nil {
		t.Fatalf("create internal metadata dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".loom", "keep.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write internal metadata file: %v", err)
	}
	svc := NewService(fakeProjectionSources{sources: []SourceObject{{
		KnowledgeObjectID: "knowledge_object_daily",
		NotesSourceRootID: "notes_source_root_box",
		RootKind:          RootKindBoxNotes,
		SourceNodeKey:     "main",
		SourcePath:        source,
		RelativePath:      "daily.md",
		FileClass:         storagecatalog.FileClassMarkdown,
	}}}, root)
	svc.Now = fixedProjectionNow

	result, err := svc.Rebuild(context.Background(), RebuildInput{})
	if err != nil {
		t.Fatalf("Rebuild returned error: %v", err)
	}
	if _, err := os.Stat(emptyOrphan); !os.IsNotExist(err) {
		t.Fatalf("empty orphan notes directory should be removed, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "projects", "v095-cleanup")); !os.IsNotExist(err) {
		t.Fatalf("empty orphan project directory should be removed, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(nonEmptyOrphan, "keep.md")); err != nil {
		t.Fatalf("non-empty orphan should be preserved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".loom", "keep.json")); err != nil {
		t.Fatalf("internal metadata directory should be preserved: %v", err)
	}
	if !hasProjectionChange(result.Changes, "remove_orphan_dir") {
		t.Fatalf("expected remove_orphan_dir change: %#v", result.Changes)
	}
	if !hasProjectionFinding(result.Findings, "orphan_directory_non_empty") {
		t.Fatalf("expected non-empty orphan finding: %#v", result.Findings)
	}
}

func TestRebuildMarksMissingSourcesInManifest(t *testing.T) {
	root := filepath.Join(t.TempDir(), DefaultDirectoryName)
	defer makeProjectionWritableForCleanup(t, root)
	svc := NewService(fakeProjectionSources{sources: []SourceObject{{
		KnowledgeObjectID: "knowledge_object_missing",
		NotesSourceRootID: "notes_source_root_box",
		RootKind:          RootKindBoxNotes,
		SourceNodeKey:     "main",
		SourcePath:        filepath.Join(t.TempDir(), "missing.md"),
		RelativePath:      "missing.md",
		FileClass:         storagecatalog.FileClassMarkdown,
	}}}, root)
	svc.Now = fixedProjectionNow

	result, err := svc.Rebuild(context.Background(), RebuildInput{})
	if err != nil {
		t.Fatalf("Rebuild returned error: %v", err)
	}
	if result.Manifest.Counts.Missing != 1 || result.Status.Counts.Missing != 1 {
		t.Fatalf("missing counts = manifest %#v status %#v", result.Manifest.Counts, result.Status.Counts)
	}
	entry := findProjectionEntry(result.Manifest.Entries, "nodes/main/Notes/missing.md")
	if entry == nil || entry.Status != ProjectionStatusMissing {
		t.Fatalf("missing manifest entry = %#v", entry)
	}
	if len(result.Findings) == 0 || !strings.Contains(result.Findings[0].Kind, "source") {
		t.Fatalf("expected source finding: %#v", result.Findings)
	}
}

func TestRebuildMaterializesFromRetainedLocalPathWhenSourceMissing(t *testing.T) {
	root := filepath.Join(t.TempDir(), DefaultDirectoryName)
	defer makeProjectionWritableForCleanup(t, root)
	missingSource := filepath.Join(t.TempDir(), "mac-note.md")
	retainedSource := writeProjectionSource(t, "retained-mac-note.md", "retained box note")
	svc := NewService(fakeProjectionSources{sources: []SourceObject{{
		KnowledgeObjectID: "knowledge_object_retained_local",
		NotesSourceRootID: "notes_source_root_box",
		RootKind:          RootKindBoxNotes,
		SourceNodeKey:     "macbook",
		SourcePath:        missingSource,
		SourceRefKind:     storagecatalog.PhysicalRefKindLocalPath,
		SourceRefURI:      retainedSource,
		RelativePath:      "mac-note.md",
		FileClass:         storagecatalog.FileClassMarkdown,
	}}}, root)
	svc.Now = fixedProjectionNow

	result, err := svc.Rebuild(context.Background(), RebuildInput{})
	if err != nil {
		t.Fatalf("Rebuild returned error: %v", err)
	}
	if result.Manifest.Counts.Materialized != 1 || result.Manifest.Counts.Missing != 0 {
		t.Fatalf("counts = %#v, want one materialized retained source", result.Manifest.Counts)
	}
	assertProjectionFile(t, filepath.Join(root, "nodes", "macbook", "Notes", "mac-note.md"), "retained box note")
	entry := findProjectionEntry(result.Manifest.Entries, "nodes/macbook/Notes/mac-note.md")
	if entry == nil || entry.SourcePath != missingSource || entry.SourceRefKind != storagecatalog.PhysicalRefKindLocalPath || entry.SourceRefURI != retainedSource {
		t.Fatalf("retained entry did not preserve source/ref mapping: %#v", entry)
	}
}

func TestRebuildMaterializesFromBackupArtifactWhenSourceMissing(t *testing.T) {
	root := filepath.Join(t.TempDir(), DefaultDirectoryName)
	defer makeProjectionWritableForCleanup(t, root)
	missingSource := filepath.Join(t.TempDir(), "mac-report.docx")
	artifact := writeProjectionBackupArtifact(t, "content", []byte("docx bytes"))
	svc := NewService(fakeProjectionSources{sources: []SourceObject{{
		KnowledgeObjectID: "knowledge_object_retained_artifact",
		NotesSourceRootID: "notes_source_root_box",
		RootKind:          RootKindBoxNotes,
		SourceNodeKey:     "macbook",
		SourcePath:        missingSource,
		SourceRefKind:     storagecatalog.PhysicalRefKindBackupArtifact,
		SourceRefURI:      artifact,
		SourceRefMember:   "content",
		RelativePath:      "mac-report.docx",
		FileClass:         storagecatalog.FileClassOfficeDocument,
	}}}, root)
	svc.Now = fixedProjectionNow

	result, err := svc.Rebuild(context.Background(), RebuildInput{})
	if err != nil {
		t.Fatalf("Rebuild returned error: %v", err)
	}
	if result.Manifest.Counts.Materialized != 1 || result.Manifest.Counts.Missing != 0 {
		t.Fatalf("counts = %#v, want one materialized backup artifact", result.Manifest.Counts)
	}
	assertProjectionFile(t, filepath.Join(root, "nodes", "macbook", "Notes", "mac-report.docx"), "docx bytes")
	entry := findProjectionEntry(result.Manifest.Entries, "nodes/macbook/Notes/mac-report.docx")
	if entry == nil || entry.SourcePath != missingSource || entry.SourceRefKind != storagecatalog.PhysicalRefKindBackupArtifact || entry.SourceRefURI != artifact {
		t.Fatalf("backup artifact entry did not preserve source/ref mapping: %#v", entry)
	}
}

func TestBuildProjectionEntriesDeduplicatesCollisions(t *testing.T) {
	root := filepath.Join(t.TempDir(), DefaultDirectoryName)
	entries, findings := BuildProjectionEntries(root, []SourceObject{
		{KnowledgeObjectID: "knowledge_object_first", NotesSourceRootID: "notes_source_root_box", RootKind: RootKindBoxNotes, SourceNodeKey: "main", RelativePath: "daily.md"},
		{KnowledgeObjectID: "knowledge_object_second", NotesSourceRootID: "notes_source_root_box", RootKind: RootKindBoxNotes, SourceNodeKey: "main", RelativePath: "daily.md"},
	}, fixedProjectionNow())
	if len(findings) != 0 || len(entries) != 2 {
		t.Fatalf("entries=%#v findings=%#v", entries, findings)
	}
	if entries[0].ProjectedPath == entries[1].ProjectedPath {
		t.Fatalf("duplicate projected paths were not disambiguated: %#v", entries)
	}
}

type fakeProjectionSources struct {
	sources []SourceObject
}

func (f fakeProjectionSources) ListProjectionSources(context.Context, SourceListInput) ([]SourceObject, error) {
	return append([]SourceObject(nil), f.sources...), nil
}

func fixedProjectionNow() time.Time {
	return time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
}

func writeProjectionSource(t *testing.T, name, payload string) string {
	t.Helper()
	pathValue := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Dir(pathValue), 0o755); err != nil {
		t.Fatalf("create source parent: %v", err)
	}
	if err := os.WriteFile(pathValue, []byte(payload), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	return pathValue
}

func writeProjectionBackupArtifact(t *testing.T, member string, payload []byte) string {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o600, Size: int64(len(`{}`))}); err != nil {
		t.Fatalf("write manifest header: %v", err)
	}
	if _, err := writer.Write([]byte(`{}`)); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := writer.WriteHeader(&tar.Header{Name: member, Mode: 0o600, Size: int64(len(payload))}); err != nil {
		t.Fatalf("write content header: %v", err)
	}
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("write content: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	pathValue := filepath.Join(t.TempDir(), "payload.tar")
	if err := os.WriteFile(pathValue, buffer.Bytes(), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	return pathValue
}

func assertProjectionFile(t *testing.T, pathValue, want string) {
	t.Helper()
	payload, err := os.ReadFile(pathValue)
	if err != nil {
		t.Fatalf("read %s: %v", pathValue, err)
	}
	if string(payload) != want {
		t.Fatalf("%s payload = %q, want %q", pathValue, string(payload), want)
	}
}

func findProjectionEntry(entries []ProjectionEntry, projectedPath string) *ProjectionEntry {
	for i := range entries {
		if entries[i].ProjectedPath == projectedPath {
			return &entries[i]
		}
	}
	return nil
}

func hasProjectionChange(changes []Change, action string) bool {
	for _, change := range changes {
		if change.Action == action {
			return true
		}
	}
	return false
}

func hasProjectionFinding(findings []Finding, kind string) bool {
	for _, finding := range findings {
		if finding.Kind == kind {
			return true
		}
	}
	return false
}

func makeProjectionWritableForCleanup(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return
	}
	_ = filepath.WalkDir(root, func(pathValue string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			_ = os.Chmod(pathValue, 0o755)
			return nil
		}
		_ = os.Chmod(pathValue, 0o644)
		return nil
	})
}
