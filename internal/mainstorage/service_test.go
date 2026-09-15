package mainstorage

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"os"
	"path/filepath"

	"loom.local/loom/internal/filesystemmeta"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/storagecatalog"
)

func TestRunOnceAcceptsStableFileAndRegistersCatalogEntry(t *testing.T) {
	root := t.TempDir()
	pathValue := filepath.Join(root, "Reports", "stable.md")
	content := strings.Repeat("a", 1024*1024)
	writeMainStorageFile(t, pathValue, content)
	now := time.Date(2026, 6, 5, 18, 0, 0, 0, time.UTC)
	old := now.Add(-2 * time.Minute)
	if err := os.Chtimes(pathValue, old, old); err != nil {
		t.Fatalf("set mtime: %v", err)
	}
	catalog := &mainStorageCatalogFake{}
	service := NewService(catalog, Config{
		BackingRoot:  root,
		NodeKey:      "main",
		StableWindow: time.Minute,
		Now:          func() time.Time { return now },
	})

	result, err := service.RunOnce(context.Background(), Config{})
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if result.Status.FilesAccepted != 1 || result.Status.FilesDelayed != 0 || result.Status.BytesHashed == 0 {
		t.Fatalf("unexpected status: %#v", result.Status)
	}
	if len(catalog.inputs) != 1 {
		t.Fatalf("catalog inputs = %d, want 1", len(catalog.inputs))
	}
	input := catalog.inputs[0]
	if input.RelativePath != "Reports/stable.md" || input.NodeKey != "main" || input.ChecksumAlgorithm != "sha256" || input.ChecksumValue == "" {
		t.Fatalf("unexpected catalog input: %#v", input)
	}
	if input.RetentionPath == "" {
		t.Fatalf("catalog input did not include retention path: %#v", input)
	}
	wantRetentionPath := filepath.Join(filepath.Dir(root), "storage-retention", "main-documents", "by-sha256", input.ChecksumValue)
	if input.RetentionPath != wantRetentionPath {
		t.Fatalf("RetentionPath = %q, want %q", input.RetentionPath, wantRetentionPath)
	}
	retained, err := os.ReadFile(input.RetentionPath)
	if err != nil {
		t.Fatalf("read retained payload: %v", err)
	}
	if string(retained) != content {
		t.Fatalf("retained payload content mismatch")
	}
	if err := os.Remove(pathValue); err != nil {
		t.Fatalf("remove source path: %v", err)
	}
	retainedAfterDelete, err := os.ReadFile(input.RetentionPath)
	if err != nil {
		t.Fatalf("read retained payload after source delete: %v", err)
	}
	if string(retainedAfterDelete) != content {
		t.Fatalf("retained payload changed after source delete")
	}
	status, err := service.Status(context.Background())
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if status.FilesAccepted != 1 || len(status.Imports) != 1 || status.Imports[0].State != StateAccepted {
		t.Fatalf("stored status mismatch: %#v", status)
	}
	if status.RetentionRoot == "" || status.Imports[0].RetentionPath == "" {
		t.Fatalf("stored status did not include retention details: %#v", status)
	}
}

func TestRunOnceDelaysFreshChangingFile(t *testing.T) {
	root := t.TempDir()
	pathValue := filepath.Join(root, "fresh.mov")
	writeMainStorageFile(t, pathValue, "still-copying")
	now := time.Date(2026, 6, 5, 18, 30, 0, 0, time.UTC)
	if err := os.Chtimes(pathValue, now.Add(-5*time.Second), now.Add(-5*time.Second)); err != nil {
		t.Fatalf("set mtime: %v", err)
	}
	catalog := &mainStorageCatalogFake{}
	service := NewService(catalog, Config{
		BackingRoot:  root,
		StableWindow: time.Minute,
		Now:          func() time.Time { return now },
	})

	result, err := service.RunOnce(context.Background(), Config{})
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if result.Status.FilesAccepted != 0 || result.Status.FilesDelayed != 1 {
		t.Fatalf("unexpected status: %#v", result.Status)
	}
	if len(catalog.inputs) != 0 {
		t.Fatalf("fresh file should not be registered: %#v", catalog.inputs)
	}
	if got := result.Status.Imports[0]; got.State != StateStabilizing || got.DelayReason == "" {
		t.Fatalf("unexpected import status: %#v", got)
	}
}

func TestRunOnceSkipsLoomInternals(t *testing.T) {
	root := t.TempDir()
	pathValue := filepath.Join(root, ".loom", "state.json")
	writeMainStorageFile(t, pathValue, "{}")
	now := time.Date(2026, 6, 5, 19, 0, 0, 0, time.UTC)
	service := NewService(&mainStorageCatalogFake{}, Config{
		BackingRoot:  root,
		StableWindow: time.Second,
		Now:          func() time.Time { return now },
	})

	result, err := service.RunOnce(context.Background(), Config{})
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if result.Status.FilesDiscovered != 0 {
		t.Fatalf("expected .loom file to be skipped, got %#v", result.Status)
	}
}

func TestRunOnceNormalizesMainDocumentDirectoryModes(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Research")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create directory: %v", err)
	}
	now := time.Date(2026, 6, 6, 19, 30, 0, 0, time.UTC)
	service := NewService(&mainStorageCatalogFake{}, Config{
		BackingRoot:  root,
		StableWindow: time.Second,
		Now:          func() time.Time { return now },
	})

	if _, err := service.RunOnce(context.Background(), Config{}); err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat normalized directory: %v", err)
	}
	if info.Mode().Perm()&0o770 != 0o770 {
		t.Fatalf("directory permissions = %o, want group writable", info.Mode().Perm())
	}
	if info.Mode()&os.ModeSetgid == 0 {
		t.Fatalf("directory mode = %#o, want setgid bit", info.Mode())
	}
}

func TestRunOnceSkipsMacOSPlatformMetadata(t *testing.T) {
	root := t.TempDir()
	writeMainStorageFile(t, filepath.Join(root, "Report.md"), "report")
	writeMainStorageFile(t, filepath.Join(root, "._Report.md"), "appledouble")
	writeMainStorageFile(t, filepath.Join(root, ".DS_Store"), "finder")
	writeMainStorageFile(t, filepath.Join(root, "Folder", "._Nested.txt"), "nested appledouble")
	writeMainStorageFile(t, filepath.Join(root, "Folder", ".DS_Store"), "nested finder")
	writeMainStorageFile(t, filepath.Join(root, ".Spotlight-V100", "store"), "spotlight")
	writeMainStorageFile(t, filepath.Join(root, "Icon\r"), "icon")
	writeMainStorageFile(t, filepath.Join(root, "video.mov.part"), "partial")
	writeMainStorageFile(t, filepath.Join(root, "~$draft.docx"), "lock")
	now := time.Date(2026, 6, 6, 20, 0, 0, 0, time.UTC)
	old := now.Add(-time.Minute)
	if err := filepath.WalkDir(root, func(pathValue string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if pathValue == root || entry.IsDir() {
			return nil
		}
		return os.Chtimes(pathValue, old, old)
	}); err != nil {
		t.Fatalf("set mtimes: %v", err)
	}
	catalog := &mainStorageCatalogFake{}
	service := NewService(catalog, Config{
		BackingRoot:  root,
		StableWindow: time.Second,
		Now:          func() time.Time { return now },
	})

	result, err := service.RunOnce(context.Background(), Config{})
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if result.Status.FilesDiscovered != 1 || result.Status.FilesAccepted != 1 || result.Status.FilesSkipped != 8 {
		t.Fatalf("unexpected status: %#v", result.Status)
	}
	if len(catalog.inputs) != 1 || catalog.inputs[0].RelativePath != "Report.md" {
		t.Fatalf("expected only the real document to be cataloged, got %#v", catalog.inputs)
	}
	ignored := 0
	accepted := 0
	for _, item := range result.Status.Imports {
		switch item.State {
		case StateIgnored:
			ignored++
			if item.IgnoredReason == "" {
				t.Fatalf("ignored item did not explain why: %#v", item)
			}
		case StateAccepted:
			accepted++
			if item.RelativePath != "Report.md" {
				t.Fatalf("unexpected accepted import: %#v", item)
			}
		}
	}
	if accepted != 1 || ignored != 8 {
		t.Fatalf("unexpected import statuses: %#v", result.Status.Imports)
	}
}

func TestRunOnceAcceptsFinderCopiedFolderShapeWithoutCatalogingMetadata(t *testing.T) {
	root := t.TempDir()
	writeMainStorageFile(t, filepath.Join(root, "Morgan", ".DS_Store"), "finder")
	writeMainStorageFile(t, filepath.Join(root, "Morgan", "._Engram_mac_icon-iOS-Default-1024x1024@1x.png"), "appledouble")
	writeMainStorageFile(t, filepath.Join(root, "Morgan", "Engram", "apps", "desktop", ".DS_Store"), "nested finder")
	writeMainStorageFile(t, filepath.Join(root, "Morgan", "Engram", "apps", "desktop", "electron.vite.config.ts"), "export default {}")
	writeMainStorageFile(t, filepath.Join(root, "Morgan", "Engram", "apps", "desktop", "dist", "Engram.app", "Contents", "Info.plist"), "<plist/>")
	linkParent := filepath.Join(root, "Morgan", "Engram", "apps", "desktop", "dist", "Engram.app", "Contents", "Frameworks", "Electron Framework.framework")
	if err := os.MkdirAll(linkParent, 0o755); err != nil {
		t.Fatalf("create link parent: %v", err)
	}
	if err := os.Symlink("Versions/Current/Resources", filepath.Join(linkParent, "Resources")); err != nil {
		t.Fatalf("create framework symlink: %v", err)
	}
	now := time.Date(2026, 6, 17, 8, 0, 0, 0, time.UTC)
	old := now.Add(-time.Hour)
	if err := filepath.WalkDir(root, func(pathValue string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if pathValue == root || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		return os.Chtimes(pathValue, old, old)
	}); err != nil {
		t.Fatalf("set mtimes: %v", err)
	}
	catalog := &mainStorageCatalogFake{}
	service := NewService(catalog, Config{
		BackingRoot:  root,
		StableWindow: time.Second,
		Now:          func() time.Time { return now },
	})

	result, err := service.RunOnce(context.Background(), Config{})
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if result.Status.FilesDiscovered != 1 || result.Status.FilesAccepted != 1 || result.Status.FilesSkipped != 4 {
		t.Fatalf("unexpected status: %#v", result.Status)
	}
	wantAccepted := map[string]bool{
		"Morgan/Engram/apps/desktop/electron.vite.config.ts": false,
	}
	observedPackage := false
	for _, item := range result.Status.Imports {
		if item.RelativePath == "Morgan/Engram/apps/desktop/dist/Engram.app" &&
			item.ObjectKind == filesystemmeta.ObjectKindPackage &&
			item.IgnoredReason == "package directory observed as metadata boundary" {
			observedPackage = true
		}
	}
	if !observedPackage {
		t.Fatalf("expected package boundary observation, imports=%#v", result.Status.Imports)
	}
	for _, input := range catalog.inputs {
		if strings.Contains(input.RelativePath, ".DS_Store") || strings.Contains(input.RelativePath, "/._") || strings.HasPrefix(input.RelativePath, "._") {
			t.Fatalf("cataloged Finder metadata: %#v", input)
		}
		if _, ok := wantAccepted[input.RelativePath]; !ok {
			t.Fatalf("unexpected catalog input: %#v", input)
		}
		wantAccepted[input.RelativePath] = true
	}
	for pathValue, seen := range wantAccepted {
		if !seen {
			t.Fatalf("expected accepted file %q, inputs=%#v", pathValue, catalog.inputs)
		}
	}
}

func TestRunOnceObservesEmptyDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Empty Folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	catalog := &mainStorageCatalogFake{}
	service := NewService(catalog, Config{
		BackingRoot:  root,
		StableWindow: time.Second,
		Now:          func() time.Time { return now },
	})

	result, err := service.RunOnce(context.Background(), Config{})
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if result.Status.FilesAccepted != 0 || result.Status.DirectoriesObserved != 1 || result.Status.ObservationsRecorded != 1 {
		t.Fatalf("unexpected status: %#v", result.Status)
	}
	if len(catalog.observations) != 1 {
		t.Fatalf("observations = %d, want 1", len(catalog.observations))
	}
	observation := catalog.observations[0]
	if observation.LogicalPath != "Empty Folder" || observation.ObjectKind != filesystemmeta.ObjectKindDirectory {
		t.Fatalf("unexpected observation: %#v", observation)
	}
}

func TestRunOnceAcceptsZeroByteMainDocument(t *testing.T) {
	root := t.TempDir()
	pathValue := filepath.Join(root, "empty.txt")
	writeMainStorageFile(t, pathValue, "")
	now := time.Date(2026, 6, 19, 10, 15, 0, 0, time.UTC)
	old := now.Add(-time.Hour)
	if err := os.Chtimes(pathValue, old, old); err != nil {
		t.Fatal(err)
	}
	catalog := &mainStorageCatalogFake{}
	service := NewService(catalog, Config{BackingRoot: root, StableWindow: time.Second, Now: func() time.Time { return now }})

	result, err := service.RunOnce(context.Background(), Config{})
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if result.Status.FilesAccepted != 1 || len(catalog.inputs) != 1 {
		t.Fatalf("unexpected result: status=%#v inputs=%#v", result.Status, catalog.inputs)
	}
	input := catalog.inputs[0]
	if input.FileSizeBytes != 0 || input.ChecksumValue != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("zero-byte file was not accepted with empty sha256: %#v", input)
	}
}

func TestRunOnceObservesExecutableBitBeforePermissionNormalization(t *testing.T) {
	root := t.TempDir()
	pathValue := filepath.Join(root, "script.sh")
	writeMainStorageFile(t, pathValue, "#!/bin/sh\n")
	if err := os.Chmod(pathValue, 0o500); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 19, 10, 30, 0, 0, time.UTC)
	old := now.Add(-time.Hour)
	if err := os.Chtimes(pathValue, old, old); err != nil {
		t.Fatal(err)
	}
	catalog := &mainStorageCatalogFake{}
	service := NewService(catalog, Config{BackingRoot: root, StableWindow: time.Second, Now: func() time.Time { return now }})

	result, err := service.RunOnce(context.Background(), Config{})
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if result.Status.FilesAccepted != 1 || len(catalog.inputs) != 1 {
		t.Fatalf("unexpected result: status=%#v inputs=%#v", result.Status, catalog.inputs)
	}
	observation := catalog.inputs[0].FilesystemObservation
	if observation == nil || !observation.Executable || observation.SourceMode != 0o500 {
		t.Fatalf("executable/source mode not observed before normalization: %#v", observation)
	}
	info, err := os.Stat(pathValue)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o660 != 0o660 {
		t.Fatalf("main document file was not normalized writable: mode=%#o", info.Mode().Perm())
	}
}

func TestRunOncePrioritizesUncatalogedFilesBeforePerRunCap(t *testing.T) {
	root := t.TempDir()
	writeMainStorageFile(t, filepath.Join(root, "A-existing.md"), "existing")
	writeMainStorageFile(t, filepath.Join(root, "Z-new.md"), "new")
	now := time.Date(2026, 6, 13, 20, 0, 0, 0, time.UTC)
	old := now.Add(-time.Minute)
	if err := filepath.WalkDir(root, func(pathValue string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if pathValue == root || entry.IsDir() {
			return nil
		}
		return os.Chtimes(pathValue, old, old)
	}); err != nil {
		t.Fatalf("set mtimes: %v", err)
	}
	catalog := &mainStorageCatalogFake{
		active: []storagecatalog.Entry{mainStorageEntryFixture("A-existing.md")},
	}
	service := NewService(catalog, Config{
		BackingRoot:    root,
		StableWindow:   time.Second,
		MaxFilesPerRun: 1,
		Now:            func() time.Time { return now },
	})

	result, err := service.RunOnce(context.Background(), Config{})
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if result.Status.FilesAccepted != 1 || len(catalog.inputs) != 1 {
		t.Fatalf("unexpected status/input count: status=%#v inputs=%#v", result.Status, catalog.inputs)
	}
	if got := catalog.inputs[0].RelativePath; got != "Z-new.md" {
		t.Fatalf("RunOnce processed %q first, want uncataloged Z-new.md", got)
	}
}

func TestRunOnceStopsCleanlyAtByteBudget(t *testing.T) {
	root := t.TempDir()
	writeMainStorageFile(t, filepath.Join(root, "first.bin"), strings.Repeat("a", 8))
	writeMainStorageFile(t, filepath.Join(root, "second.bin"), strings.Repeat("b", 8))
	now := time.Date(2026, 6, 17, 9, 0, 0, 0, time.UTC)
	old := now.Add(-time.Minute)
	for _, pathValue := range []string{filepath.Join(root, "first.bin"), filepath.Join(root, "second.bin")} {
		if err := os.Chtimes(pathValue, old, old); err != nil {
			t.Fatalf("set mtime: %v", err)
		}
	}
	catalog := &mainStorageCatalogFake{}
	service := NewService(catalog, Config{
		BackingRoot:          root,
		StableWindow:         time.Second,
		MaxBytesHashedPerRun: 8,
		Now:                  func() time.Time { return now },
	})

	result, err := service.RunOnce(context.Background(), Config{})
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if result.Status.FilesAccepted != 1 || !result.Status.ByteBudgetExhausted || result.Status.FilesRemaining != 1 {
		t.Fatalf("unexpected budget status: %#v", result.Status)
	}
	if result.Status.Metrics.BudgetStopCount != 1 {
		t.Fatalf("BudgetStopCount = %d, want 1", result.Status.Metrics.BudgetStopCount)
	}
	if got := result.Status.Imports[len(result.Status.Imports)-1]; got.State != StateBudgetDeferred || got.DelayReason == "" {
		t.Fatalf("unexpected deferred item: %#v", got)
	}
}

func TestRunOnceStopsCleanlyAtRuntimeBudget(t *testing.T) {
	root := t.TempDir()
	writeMainStorageFile(t, filepath.Join(root, "first.bin"), "a")
	now := time.Date(2026, 6, 17, 9, 30, 0, 0, time.UTC)
	old := now.Add(-time.Minute)
	if err := os.Chtimes(filepath.Join(root, "first.bin"), old, old); err != nil {
		t.Fatalf("set mtime: %v", err)
	}
	service := NewService(&mainStorageCatalogFake{}, Config{
		BackingRoot:  root,
		StableWindow: time.Second,
		MaxRuntime:   time.Nanosecond,
		Now:          func() time.Time { return now },
	})

	result, err := service.RunOnce(context.Background(), Config{})
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if !result.Status.RuntimeBudgetExhausted || result.Status.FilesRemaining != 1 || result.Status.FilesAccepted != 0 {
		t.Fatalf("unexpected runtime budget status: %#v", result.Status)
	}
	if got := result.Status.Imports[len(result.Status.Imports)-1]; got.State != StateBudgetDeferred || got.DelayReason == "" {
		t.Fatalf("unexpected deferred item: %#v", got)
	}
}

func TestRunOnceSkipsAlreadyCatalogedUnchangedFilesBeforePerRunCap(t *testing.T) {
	root := t.TempDir()
	writeMainStorageFile(t, filepath.Join(root, "A-existing.md"), "existing-a")
	writeMainStorageFile(t, filepath.Join(root, "B-existing.md"), "existing-b")
	now := time.Date(2026, 6, 14, 10, 0, 0, 0, time.UTC)
	old := now.Add(-time.Minute)
	for _, pathValue := range []string{
		filepath.Join(root, "A-existing.md"),
		filepath.Join(root, "B-existing.md"),
	} {
		if err := os.Chtimes(pathValue, old, old); err != nil {
			t.Fatalf("set mtime: %v", err)
		}
	}
	catalog := &mainStorageCatalogFake{
		active: []storagecatalog.Entry{
			mainStorageEntryFixtureForFile(t, root, "A-existing.md"),
			mainStorageEntryFixtureForFile(t, root, "B-existing.md"),
		},
	}
	service := NewService(catalog, Config{
		BackingRoot:    root,
		StableWindow:   time.Second,
		MaxFilesPerRun: 1,
		Now:            func() time.Time { return now },
	})

	result, err := service.RunOnce(context.Background(), Config{})
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if result.Status.FilesAccepted != 0 || result.Status.FilesAlreadyCataloged != 2 || result.Status.FilesRemaining != 0 || result.Status.BytesHashed != 0 {
		t.Fatalf("unexpected status: %#v", result.Status)
	}
	if len(catalog.inputs) != 0 {
		t.Fatalf("unchanged files should not be registered again: %#v", catalog.inputs)
	}
}

func TestRunOnceProcessesCatalogedFileWhenSourceChanged(t *testing.T) {
	root := t.TempDir()
	pathValue := filepath.Join(root, "A-existing.md")
	writeMainStorageFile(t, pathValue, "old")
	now := time.Date(2026, 6, 14, 10, 30, 0, 0, time.UTC)
	old := now.Add(-time.Hour)
	if err := os.Chtimes(pathValue, old, old); err != nil {
		t.Fatalf("set old mtime: %v", err)
	}
	active := mainStorageEntryFixtureForFile(t, root, "A-existing.md")
	writeMainStorageFile(t, pathValue, "new content with different size")
	changed := now.Add(-time.Minute)
	if err := os.Chtimes(pathValue, changed, changed); err != nil {
		t.Fatalf("set changed mtime: %v", err)
	}
	catalog := &mainStorageCatalogFake{active: []storagecatalog.Entry{active}}
	service := NewService(catalog, Config{
		BackingRoot:  root,
		StableWindow: time.Second,
		Now:          func() time.Time { return now },
	})

	result, err := service.RunOnce(context.Background(), Config{})
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if result.Status.FilesAccepted != 1 || result.Status.FilesAlreadyCataloged != 0 || len(catalog.inputs) != 1 {
		t.Fatalf("unexpected status/input count: status=%#v inputs=%#v", result.Status, catalog.inputs)
	}
	if got := catalog.inputs[0].RelativePath; got != "A-existing.md" {
		t.Fatalf("RunOnce processed %q, want changed file", got)
	}
}

func TestRunOncePrioritizesNewestUncatalogedFilesBeforeOldBacklog(t *testing.T) {
	root := t.TempDir()
	writeMainStorageFile(t, filepath.Join(root, "Documents sent from Mac", "old-backlog.md"), "old")
	writeMainStorageFile(t, filepath.Join(root, ".loom-acceptance", "fresh.md"), "fresh")
	now := time.Date(2026, 6, 13, 21, 0, 0, 0, time.UTC)
	old := now.Add(-24 * time.Hour)
	fresh := now.Add(-2 * time.Minute)
	if err := os.Chtimes(filepath.Join(root, "Documents sent from Mac", "old-backlog.md"), old, old); err != nil {
		t.Fatalf("set old mtime: %v", err)
	}
	if err := os.Chtimes(filepath.Join(root, ".loom-acceptance", "fresh.md"), fresh, fresh); err != nil {
		t.Fatalf("set fresh mtime: %v", err)
	}
	catalog := &mainStorageCatalogFake{}
	service := NewService(catalog, Config{
		BackingRoot:    root,
		StableWindow:   time.Minute,
		MaxFilesPerRun: 1,
		Now:            func() time.Time { return now },
	})

	result, err := service.RunOnce(context.Background(), Config{})
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if result.Status.FilesAccepted != 1 || len(catalog.inputs) != 1 {
		t.Fatalf("unexpected status/input count: status=%#v inputs=%#v", result.Status, catalog.inputs)
	}
	if got := catalog.inputs[0].RelativePath; got != ".loom-acceptance/fresh.md" {
		t.Fatalf("RunOnce processed %q first, want newest uncataloged .loom-acceptance file", got)
	}
}

func TestShouldSkipRelativePathOnlySkipsKnownPlatformMetadata(t *testing.T) {
	for _, pathValue := range []string{
		"._Report.md",
		"Folder/._Report.md",
		".DS_Store",
		"Folder/.DS_Store",
		".Spotlight-V100/store",
		".TemporaryItems/item",
		".Trashes/item",
		".AppleDouble/item",
		".LSOverride",
		".metadata_never_index",
		".com.apple.timemachine.donotpresent",
		"Network Trash Folder/item",
		"Temporary Items/item",
		"Icon\r",
		".loom/state.json",
		"video.mov.part",
		"download.tmp",
		"download.crdownload",
		"~$draft.docx",
		".~lock.report#",
		"file.icloud",
		"file.loom-meta.json",
		"folder/file.loom-meta.json",
	} {
		if shouldSkip, reason := shouldSkipRelativePath(pathValue); !shouldSkip || reason == "" {
			t.Fatalf("shouldSkipRelativePath(%q) = false, want true", pathValue)
		}
	}
	for _, pathValue := range []string{
		"Report.md",
		".env",
		"Folder/.keep",
		"Folder/Report.md",
	} {
		if shouldSkip, _ := shouldSkipRelativePath(pathValue); shouldSkip {
			t.Fatalf("shouldSkipRelativePath(%q) = true, want false", pathValue)
		}
	}
}

func TestFileStillMatchesSnapshotDetectsPartialCopyMovement(t *testing.T) {
	root := t.TempDir()
	pathValue := filepath.Join(root, "large.mov")
	writeMainStorageFile(t, pathValue, "initial")
	info, err := os.Stat(pathValue)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	file := discoveredFile{
		RelativePath: "large.mov",
		PhysicalPath: pathValue,
		SizeBytes:    info.Size(),
		ModifiedAt:   info.ModTime().UTC(),
	}
	writeMainStorageFile(t, pathValue, "initial plus more bytes")

	if stable, reason := fileStillMatchesSnapshot(file); stable || reason == "" {
		t.Fatalf("fileStillMatchesSnapshot = %t/%q, want unstable with reason", stable, reason)
	}
}

func TestReconcileDryRunReportsMissingCatalogedMainDocument(t *testing.T) {
	root := t.TempDir()
	writeMainStorageFile(t, filepath.Join(root, "Present.md"), "present")
	catalog := &mainStorageCatalogFake{
		active: []storagecatalog.Entry{
			mainStorageEntryFixture("Present.md"),
			mainStorageEntryFixture("Missing.md"),
		},
	}
	service := NewService(catalog, Config{
		BackingRoot: root,
		NodeKey:     "main",
		Now:         func() time.Time { return time.Date(2026, 6, 13, 18, 0, 0, 0, time.UTC) },
	})

	result, err := service.Reconcile(context.Background(), ReconcileInput{DryRun: true})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if !result.DryRun || result.FilesPresent != 1 || result.ActiveCataloged != 2 || result.MissingCataloged != 1 || result.Tombstoned != 0 {
		t.Fatalf("unexpected reconcile result: %#v", result)
	}
	if len(result.Items) != 1 || result.Items[0].RelativePath != "Missing.md" || result.Items[0].State != StateMissingDeferred {
		t.Fatalf("unexpected reconcile items: %#v", result.Items)
	}
	if len(catalog.tombstones) != 0 {
		t.Fatalf("dry-run should not tombstone: %#v", catalog.tombstones)
	}
	if len(catalog.activeLimits) != 1 || catalog.activeLimits[0] != 0 {
		t.Fatalf("reconcile should request all active entries, limits=%#v", catalog.activeLimits)
	}
}

func TestRunOnceDefersMissingCatalogedMainDocumentInsteadOfTombstoning(t *testing.T) {
	root := t.TempDir()
	catalog := &mainStorageCatalogFake{
		active: []storagecatalog.Entry{
			mainStorageEntryFixture("Missing.md"),
		},
	}
	now := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	service := NewService(catalog, Config{
		BackingRoot:  root,
		NodeKey:      "main",
		StableWindow: time.Second,
		Now:          func() time.Time { return now },
	})

	result, err := service.RunOnce(context.Background(), Config{})
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if result.Status.FilesMissingCataloged != 1 || result.Status.FilesTombstoned != 0 {
		t.Fatalf("unexpected missing/tombstone counts: %#v", result.Status)
	}
	if len(catalog.tombstones) != 0 {
		t.Fatalf("RunOnce should not tombstone missing source rows: %#v", catalog.tombstones)
	}
	if len(result.Status.Imports) != 1 || result.Status.Imports[0].State != StateMissingDeferred || result.Status.Imports[0].DelayReason == "" {
		t.Fatalf("expected missing deferred import item, got %#v", result.Status.Imports)
	}
}

func TestReconcileYesTombstonesMissingCatalogedMainDocument(t *testing.T) {
	root := t.TempDir()
	catalog := &mainStorageCatalogFake{
		active: []storagecatalog.Entry{
			mainStorageEntryFixture("Missing.md"),
		},
	}
	now := time.Date(2026, 6, 13, 18, 30, 0, 0, time.UTC)
	service := NewService(catalog, Config{
		BackingRoot: root,
		NodeKey:     "main",
		Now:         func() time.Time { return now },
	})

	result, err := service.Reconcile(context.Background(), ReconcileInput{Yes: true, Reason: "unit test", CreatedBy: "test"})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.DryRun || result.MissingCataloged != 1 || result.Tombstoned != 1 {
		t.Fatalf("unexpected reconcile result: %#v", result)
	}
	if len(result.Items) != 1 || result.Items[0].State != StateTombstoned {
		t.Fatalf("unexpected reconcile items: %#v", result.Items)
	}
	if len(catalog.tombstones) != 1 {
		t.Fatalf("tombstones = %d, want 1", len(catalog.tombstones))
	}
	if got := catalog.tombstones[0]; got.RelativePath != "Missing.md" || got.Reason != "unit test" || got.CreatedBy != "test" {
		t.Fatalf("unexpected tombstone input: %#v", got)
	}
}

func TestReconcilePlansLegacyOnlyDocumentsWithoutMutation(t *testing.T) {
	root := t.TempDir()
	legacyRoot := filepath.Join(root, "legacy")
	canonicalRoot := filepath.Join(root, "box", "Documents")
	writeMainStorageFile(t, filepath.Join(legacyRoot, "Reports", "legacy.md"), "legacy payload")
	catalog := &mainStorageCatalogFake{active: []storagecatalog.Entry{mainStorageEntryFixture("Reports/legacy.md")}}
	service := NewService(catalog, Config{BackingRoot: canonicalRoot, LegacyRoot: legacyRoot})

	result, err := service.Reconcile(context.Background(), ReconcileInput{DryRun: true, CompareLegacy: true})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.Migration.Status != DocumentsMigrationReady || result.Migration.LegacyOnly != 2 || result.Migration.Conflicts != 0 {
		t.Fatalf("unexpected migration plan: %#v", result.Migration)
	}
	if !result.CatalogMutationBlocked || result.CatalogMutationBlockReason == "" {
		t.Fatalf("legacy-only plan did not block catalog mutation: %#v", result)
	}
	if _, err := os.Lstat(canonicalRoot); !os.IsNotExist(err) {
		t.Fatalf("dry-run created canonical root, stat err=%v", err)
	}
	if len(catalog.tombstones) != 0 {
		t.Fatalf("dry-run mutated catalog: %#v", catalog.tombstones)
	}
}

func TestReconcileFailsClosedOnDifferingLegacyCollision(t *testing.T) {
	root := t.TempDir()
	legacyRoot := filepath.Join(root, "legacy")
	canonicalRoot := filepath.Join(root, "box", "Documents")
	writeMainStorageFile(t, filepath.Join(legacyRoot, "same.md"), "same")
	writeMainStorageFile(t, filepath.Join(canonicalRoot, "same.md"), "same")
	writeMainStorageFile(t, filepath.Join(legacyRoot, "conflict.md"), "legacy")
	writeMainStorageFile(t, filepath.Join(canonicalRoot, "conflict.md"), "modern")
	catalog := &mainStorageCatalogFake{active: []storagecatalog.Entry{mainStorageEntryFixture("Missing.md")}}
	service := NewService(catalog, Config{BackingRoot: canonicalRoot, LegacyRoot: legacyRoot})

	result, err := service.Reconcile(context.Background(), ReconcileInput{Yes: true, CompareLegacy: true})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.Migration.Status != DocumentsMigrationBlocked || result.Migration.Conflicts != 1 || result.Migration.Equivalent != 1 {
		t.Fatalf("unexpected migration plan: %#v", result.Migration)
	}
	if !result.CatalogMutationBlocked || result.Tombstoned != 0 || len(catalog.tombstones) != 0 {
		t.Fatalf("collision did not fail closed before catalog mutation: result=%#v tombstones=%#v", result, catalog.tombstones)
	}
	conflict := findDocumentsMigrationItem(result.Migration.Items, "conflict.md")
	if conflict.State != DocumentsMigrationConflict || conflict.LegacySHA256 == "" || conflict.CanonicalSHA256 == "" || conflict.LegacySHA256 == conflict.CanonicalSHA256 {
		t.Fatalf("conflict evidence is incomplete: %#v", conflict)
	}
}

func TestReconcileForcesDistinctLegacyComparisonWhenCallerOmitsFlag(t *testing.T) {
	root := t.TempDir()
	legacyRoot := filepath.Join(root, "legacy")
	canonicalRoot := filepath.Join(root, "box", "Documents")
	writeMainStorageFile(t, filepath.Join(legacyRoot, "legacy.md"), "legacy payload")
	catalog := &mainStorageCatalogFake{active: []storagecatalog.Entry{mainStorageEntryFixture("Missing.md")}}
	service := NewService(catalog, Config{BackingRoot: canonicalRoot, LegacyRoot: legacyRoot})

	result, err := service.Reconcile(context.Background(), ReconcileInput{Yes: true})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.Migration.Status != DocumentsMigrationReady || result.Migration.LegacyOnly == 0 {
		t.Fatalf("distinct configured legacy root was not compared: %#v", result.Migration)
	}
	if !result.CatalogMutationBlocked || result.Tombstoned != 0 || len(catalog.tombstones) != 0 {
		t.Fatalf("service boundary failed open: result=%#v tombstones=%#v", result, catalog.tombstones)
	}
}

func TestReconcileDryRunSkipsImplicitLegacyComparison(t *testing.T) {
	root := t.TempDir()
	canonicalRoot := filepath.Join(root, "box", "Documents")
	legacyRoot := filepath.Join(root, "legacy-is-not-a-directory")
	writeMainStorageFile(t, filepath.Join(canonicalRoot, "Present.md"), "present")
	writeMainStorageFile(t, legacyRoot, "legacy sentinel")
	service := NewService(&mainStorageCatalogFake{}, Config{
		BackingRoot: canonicalRoot,
		LegacyRoot:  legacyRoot,
	})

	result, err := service.Reconcile(context.Background(), ReconcileInput{DryRun: true})
	if err != nil {
		t.Fatalf("Reconcile returned error after touching an implicit legacy root: %v", err)
	}
	if result.Migration.Status != DocumentsMigrationNotRequested || result.FilesPresent != 1 {
		t.Fatalf("ordinary dry-run unexpectedly planned migration: %#v", result)
	}
}

func TestRunOnceSkipsImplicitLegacyComparison(t *testing.T) {
	root := t.TempDir()
	canonicalRoot := filepath.Join(root, "box", "Documents")
	legacyRoot := filepath.Join(root, "legacy-is-not-a-directory")
	writeMainStorageFile(t, filepath.Join(canonicalRoot, "Present.md"), "present")
	writeMainStorageFile(t, legacyRoot, "legacy sentinel")
	service := NewService(&mainStorageCatalogFake{}, Config{
		BackingRoot:  canonicalRoot,
		LegacyRoot:   legacyRoot,
		StableWindow: time.Hour,
	})

	result, err := service.RunOnce(context.Background(), Config{})
	if err != nil {
		t.Fatalf("RunOnce returned error after touching an implicit legacy root: %v", err)
	}
	if result.Status.FilesDiscovered != 1 || result.Status.FilesFailed != 0 {
		t.Fatalf("unexpected RunOnce status: %#v", result.Status)
	}
}

func TestReconcileIdenticalLegacyRootDoesNotRequireComparison(t *testing.T) {
	root := t.TempDir()
	catalog := &mainStorageCatalogFake{active: []storagecatalog.Entry{mainStorageEntryFixture("Missing.md")}}
	service := NewService(catalog, Config{BackingRoot: root, LegacyRoot: root})

	result, err := service.Reconcile(context.Background(), ReconcileInput{Yes: true})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.Migration.Status != DocumentsMigrationNotRequested || result.CatalogMutationBlocked || result.Tombstoned != 1 {
		t.Fatalf("identical roots changed ordinary reconcile behavior: %#v", result)
	}
}

func TestPlanDocumentsMigrationIdenticalRootsShortCircuitsBeforeInventory(t *testing.T) {
	pathValue := filepath.Join(t.TempDir(), "not-a-directory")
	writeMainStorageFile(t, pathValue, "sentinel")

	plan, err := planDocumentsMigration(context.Background(), pathValue, pathValue)
	if err != nil {
		t.Fatalf("planDocumentsMigration returned error: %v", err)
	}
	if plan.Status != DocumentsMigrationNotNeeded || plan.LegacyExists || plan.CanonicalExists || len(plan.Items) != 0 {
		t.Fatalf("identical roots were inventoried: %#v", plan)
	}
}

func TestPlanDocumentsMigrationRejectsTypeAndSymlinkCollisions(t *testing.T) {
	root := t.TempDir()
	legacyRoot := filepath.Join(root, "legacy")
	canonicalRoot := filepath.Join(root, "canonical")
	writeMainStorageFile(t, filepath.Join(legacyRoot, "typed"), "file")
	if err := os.MkdirAll(filepath.Join(canonicalRoot, "typed"), 0o755); err != nil {
		t.Fatalf("mkdir type collision: %v", err)
	}
	writeMainStorageFile(t, filepath.Join(legacyRoot, "target"), "target")
	writeMainStorageFile(t, filepath.Join(canonicalRoot, "target"), "target")
	if err := os.Symlink("target", filepath.Join(legacyRoot, "linked")); err != nil {
		t.Fatalf("create legacy symlink: %v", err)
	}
	if err := os.Symlink("target", filepath.Join(canonicalRoot, "linked")); err != nil {
		t.Fatalf("create canonical symlink: %v", err)
	}

	plan, err := planDocumentsMigration(context.Background(), legacyRoot, canonicalRoot)
	if err != nil {
		t.Fatalf("planDocumentsMigration returned error: %v", err)
	}
	if plan.Status != DocumentsMigrationBlocked || plan.Conflicts != 2 {
		t.Fatalf("unexpected collision plan: %#v", plan)
	}
}

func findDocumentsMigrationItem(items []DocumentsMigrationItem, relativePath string) DocumentsMigrationItem {
	for _, item := range items {
		if item.RelativePath == relativePath {
			return item
		}
	}
	return DocumentsMigrationItem{}
}

func TestBackfillRetentionCreatesMissingPayloadAndRegistersRef(t *testing.T) {
	root := t.TempDir()
	pathValue := filepath.Join(root, "Research", "dataset.bin")
	content := "retention payload"
	writeMainStorageFile(t, pathValue, content)
	now := time.Date(2026, 6, 14, 15, 0, 0, 0, time.UTC)
	old := now.Add(-time.Hour)
	if err := os.Chtimes(pathValue, old, old); err != nil {
		t.Fatalf("set mtime: %v", err)
	}
	checksum, err := hashFile(pathValue)
	if err != nil {
		t.Fatalf("hash source: %v", err)
	}
	size := int64(len(content))
	entry := storagecatalog.Entry{
		StorageEntryID:    ids.NewStorageEntryID(),
		StorageClass:      storagecatalog.StorageClassMainDocument,
		SourceArea:        storagecatalog.SourceAreaMainDocuments,
		OriginNodeKey:     "main",
		LogicalPath:       "Research/dataset.bin",
		CurrentViewPath:   "main/Documents/Research/dataset.bin",
		SizeBytes:         &size,
		ChecksumAlgorithm: "sha256",
		ChecksumHex:       checksum,
		FileClass:         storagecatalog.FileClassBinary,
		ProcessingState:   storagecatalog.ProcessingStateMetadataOnly,
		AvailabilityState: storagecatalog.AvailabilityStateAvailable,
		RetentionState:    storagecatalog.RetentionStatePending,
	}
	ref := storagecatalog.PhysicalRef{
		StoragePhysicalRefID: ids.NewStoragePhysicalRefID(),
		StorageEntryID:       entry.StorageEntryID,
		RefKind:              storagecatalog.PhysicalRefKindLocalPath,
		URI:                  pathValue,
		NodeKey:              "main",
		Status:               storagecatalog.PhysicalRefStatusAvailable,
	}
	retentionRoot := filepath.Join(t.TempDir(), "storage-retention")
	catalog := &mainStorageCatalogFake{
		active: []storagecatalog.Entry{entry},
		details: map[string]storagecatalog.EntryDetail{
			entry.StorageEntryID: {Entry: entry, PhysicalRefs: []storagecatalog.PhysicalRef{ref}},
		},
	}
	service := NewService(catalog, Config{
		BackingRoot:   root,
		RetentionRoot: retentionRoot,
		NodeKey:       "main",
		Now:           func() time.Time { return now },
	})

	result, err := service.BackfillRetention(context.Background(), RetentionBackfillInput{Yes: true})
	if err != nil {
		t.Fatalf("BackfillRetention returned error: %v", err)
	}
	if result.DryRun || result.Created != 1 || result.Scanned != 1 || result.Failed != 0 {
		t.Fatalf("unexpected backfill result: %#v", result)
	}
	if len(catalog.inputs) != 1 {
		t.Fatalf("catalog inputs = %d, want 1", len(catalog.inputs))
	}
	input := catalog.inputs[0]
	if input.StorageEntryID != entry.StorageEntryID || input.RetentionPath == "" {
		t.Fatalf("unexpected backfill catalog input: %#v", input)
	}
	retained, err := os.ReadFile(input.RetentionPath)
	if err != nil {
		t.Fatalf("read retained payload: %v", err)
	}
	if string(retained) != content {
		t.Fatalf("retained payload = %q, want %q", string(retained), content)
	}
}

type mainStorageCatalogFake struct {
	inputs       []storagecatalog.MainDocumentInput
	observations []storagecatalog.RegisterFilesystemObservationInput
	active       []storagecatalog.Entry
	details      map[string]storagecatalog.EntryDetail
	activeLimits []int
	tombstones   []storagecatalog.MainDocumentTombstoneInput
}

func (f *mainStorageCatalogFake) RegisterFilesystemObservation(_ context.Context, input storagecatalog.RegisterFilesystemObservationInput) (storagecatalog.FilesystemObservation, error) {
	f.observations = append(f.observations, input)
	return storagecatalog.FilesystemObservation{
		StorageFilesystemObservationID: ids.NewStorageFilesystemObservationID(),
		StorageEntryID:                 stringPtr(input.StorageEntryID),
		SourceArea:                     input.SourceArea,
		SourceNodeKey:                  input.SourceNodeKey,
		SourceRef:                      input.SourceRef,
		LogicalPath:                    input.LogicalPath,
		ObjectKind:                     input.ObjectKind,
	}, nil
}

func (f *mainStorageCatalogFake) RegisterMainDocument(_ context.Context, input storagecatalog.MainDocumentInput) (storagecatalog.EntryDetail, error) {
	f.inputs = append(f.inputs, input)
	size := input.FileSizeBytes
	entry := storagecatalog.Entry{
		StorageEntryID:    ids.NewStorageEntryID(),
		StorageClass:      storagecatalog.StorageClassMainDocument,
		SourceArea:        storagecatalog.SourceAreaMainDocuments,
		OriginNodeKey:     input.NodeKey,
		LogicalPath:       input.RelativePath,
		CurrentViewPath:   "main/Documents/" + input.RelativePath,
		SizeBytes:         &size,
		ChecksumAlgorithm: input.ChecksumAlgorithm,
		ChecksumHex:       input.ChecksumValue,
		FileClass:         storagecatalog.ClassifyPath(input.RelativePath, ""),
		ProcessingState:   storagecatalog.ProcessingStateMetadataOnly,
		AvailabilityState: storagecatalog.AvailabilityStateAvailable,
		RetentionState:    storagecatalog.RetentionStatePending,
	}
	return storagecatalog.EntryDetail{Entry: entry}, nil
}

func (f *mainStorageCatalogFake) ListActiveMainDocuments(_ context.Context, limit int) ([]storagecatalog.Entry, error) {
	f.activeLimits = append(f.activeLimits, limit)
	return append([]storagecatalog.Entry(nil), f.active...), nil
}

func (f *mainStorageCatalogFake) InspectEntry(_ context.Context, ref string) (storagecatalog.EntryDetail, error) {
	if f.details != nil {
		if detail, ok := f.details[ref]; ok {
			return detail, nil
		}
	}
	for _, entry := range f.active {
		if ref == entry.StorageEntryID || ref == entry.LogicalPath || ref == entry.CurrentViewPath {
			return storagecatalog.EntryDetail{Entry: entry}, nil
		}
	}
	return storagecatalog.EntryDetail{}, os.ErrNotExist
}

func (f *mainStorageCatalogFake) TombstoneMainDocument(_ context.Context, input storagecatalog.MainDocumentTombstoneInput) (storagecatalog.Tombstone, error) {
	f.tombstones = append(f.tombstones, input)
	return storagecatalog.Tombstone{
		StorageTombstoneID: ids.NewStorageTombstoneID(),
		StorageEntryID:     input.StorageEntryID,
		TombstoneKind:      input.TombstoneKind,
		Reason:             input.Reason,
		CreatedBy:          input.CreatedBy,
		CreatedAt:          input.TombstonedAt,
	}, nil
}

func mainStorageEntryFixture(relativePath string) storagecatalog.Entry {
	size := int64(len(relativePath))
	return storagecatalog.Entry{
		StorageEntryID:    ids.NewStorageEntryID(),
		StorageClass:      storagecatalog.StorageClassMainDocument,
		SourceArea:        storagecatalog.SourceAreaMainDocuments,
		OriginNodeKey:     "main",
		LogicalPath:       relativePath,
		CurrentViewPath:   "main/Documents/" + relativePath,
		SizeBytes:         &size,
		FileClass:         storagecatalog.ClassifyPath(relativePath, ""),
		ProcessingState:   storagecatalog.ProcessingStateMetadataOnly,
		AvailabilityState: storagecatalog.AvailabilityStateAvailable,
		RetentionState:    storagecatalog.RetentionStateSnapshot,
	}
}

func mainStorageEntryFixtureForFile(t *testing.T, root, relativePath string) storagecatalog.Entry {
	t.Helper()
	pathValue := filepath.Join(root, filepath.FromSlash(relativePath))
	info, err := os.Stat(pathValue)
	if err != nil {
		t.Fatalf("stat %s: %v", relativePath, err)
	}
	size := info.Size()
	metadata, err := json.Marshal(map[string]any{
		"schema_version": "storage.main_document.metadata.v0.6",
		"source":         "mainstorage.importer",
		"relative_path":  relativePath,
		"modified_at":    info.ModTime().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	return storagecatalog.Entry{
		StorageEntryID:    ids.NewStorageEntryID(),
		StorageClass:      storagecatalog.StorageClassMainDocument,
		SourceArea:        storagecatalog.SourceAreaMainDocuments,
		OriginNodeKey:     "main",
		LogicalPath:       relativePath,
		CurrentViewPath:   "main/Documents/" + relativePath,
		SizeBytes:         &size,
		ChecksumAlgorithm: "sha256",
		ChecksumHex:       strings.Repeat("a", 64),
		FileClass:         storagecatalog.ClassifyPath(relativePath, ""),
		ProcessingState:   storagecatalog.ProcessingStateMetadataOnly,
		AvailabilityState: storagecatalog.AvailabilityStateAvailable,
		RetentionState:    storagecatalog.RetentionStateSnapshot,
		Metadata:          metadata,
	}
}

func writeMainStorageFile(t *testing.T, pathValue, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(pathValue), 0o755); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if err := os.WriteFile(pathValue, []byte(content), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
