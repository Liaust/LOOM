package storagedoctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/config"
	"loom.local/loom/internal/storagecatalog"
)

func TestBuildFilesystemStatusInspectsOnlyConfiguredRootEntries(t *testing.T) {
	base := t.TempDir()
	roots := []string{"box", "storage", "imports", "backups", "archive", "generated"}
	for _, name := range roots {
		root := filepath.Join(base, name)
		if err := os.MkdirAll(filepath.Join(root, "payload", "deep"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(base, "missing-external-payload"), filepath.Join(root, "payload", "deep", "dangling")); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Config{
		BoxPath: base + "/box", StorageRoot: base + "/storage", ImportsRoot: base + "/imports",
		UserBackupsRoot: base + "/backups", ArchiveRoot: base + "/archive", GeneratedRoot: base + "/generated",
		StorageExport: base + "/legacy-export",
	}
	entries := []storagecatalog.Entry{{AvailabilityState: storagecatalog.AvailabilityStateAvailable}, {AvailabilityState: storagecatalog.AvailabilityStatePending}}
	status := BuildFilesystemStatus(cfg, entries, len(entries), nil)
	if len(status.Roots) != len(roots) || status.Status != StatusWarning {
		t.Fatalf("status = %#v", status)
	}
	for _, root := range status.Roots {
		if !root.Exists || !root.Directory || root.Symlink || root.Status != StatusOK {
			t.Fatalf("nested payload state affected bounded root inspection: %#v", root)
		}
	}
	if !status.Catalog.Truncated || status.Catalog.Returned != 2 || status.Catalog.Available != 1 || status.Catalog.Pending != 1 {
		t.Fatalf("catalog status = %#v", status.Catalog)
	}
	if status.Export.Active || !status.Export.Deprecated || status.Export.LegacyPath != cfg.StorageExport {
		t.Fatalf("export compatibility = %#v", status.Export)
	}
}

func TestBuildFilesystemStatusRejectsSymlinkRoot(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "box-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	status := BuildFilesystemStatus(config.Config{BoxPath: link}, nil, 10, nil)
	if status.Status != StatusError || len(status.Roots) == 0 || !status.Roots[0].Symlink || status.Roots[0].Status != StatusError {
		t.Fatalf("symlink root did not fail closed: %#v", status)
	}
}

func TestBuildFilesystemStatusNamesPreCutoverRootsWithoutBroadRuntimeStorage(t *testing.T) {
	base := t.TempDir()
	dataRoot := filepath.Join(base, "var-lib-loom")
	storageRoot := filepath.Join(base, "srv-loom", "storage")
	for _, root := range []string{
		filepath.Join(base, "box"), storageRoot, filepath.Join(dataRoot, "lane", "accepted"),
		filepath.Join(dataRoot, "private-backups"), filepath.Join(dataRoot, "storage-archive"),
		filepath.Join(dataRoot, "generated"),
	} {
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dataRoot, "unrelated-runtime-secret"), []byte("not storage payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		BoxPath:                  filepath.Join(base, "box"),
		DataDir:                  dataRoot,
		StorageRoot:              storageRoot,
		CanonicalUserBackupsRoot: filepath.Join(storageRoot, "backups"),
		ImportsRoot:              filepath.Join(dataRoot, "lane", "accepted"),
		UserBackupsRoot:          filepath.Join(dataRoot, "private-backups"),
		ArchiveRoot:              filepath.Join(dataRoot, "storage-archive"),
		GeneratedRoot:            filepath.Join(dataRoot, "generated"),
		StorageExport:            filepath.Join(dataRoot, "storage-views", "main-export"),
		LegacySplitRoots:         true,
	}
	status := BuildFilesystemStatus(cfg, nil, 10, nil)
	if status.LayoutMode != "legacy_split_transition" || !status.Export.Active {
		t.Fatalf("pre-cutover status = %#v", status)
	}
	for _, root := range status.Roots {
		if root.Key == "storage" {
			if root.Path != storageRoot || !strings.Contains(root.Role, "planned canonical") {
				t.Fatalf("storage transition root = %#v", root)
			}
			return
		}
	}
	t.Fatal("storage root status missing")
}
