package filesystemlayout

import (
	"path/filepath"
	"testing"
)

func TestDefaultLayoutUsesCanonicalPhysicalRoots(t *testing.T) {
	layout := Default()
	wants := map[string]string{
		"service":      "/srv/loom",
		"box":          "/srv/loom/box",
		"agents":       "/srv/loom/agents",
		"storage":      "/srv/loom/storage",
		"imports":      "/srv/loom/storage/imports",
		"user_backups": "/srv/loom/storage/backups",
		"archive":      "/srv/loom/storage/archive",
		"box_state":    "/var/lib/loom/box-state",
		"generated":    "/var/lib/loom/generated",
		"notes":        "/var/lib/loom/generated/notes",
		"retention":    "/var/lib/loom/storage-retention",
		"operations":   "/var/lib/loom/backups/main",
	}
	got := map[string]string{
		"service":      layout.ServiceRoot.String(),
		"box":          layout.BoxRoot.String(),
		"agents":       layout.AgentsRoot.String(),
		"storage":      layout.StorageRoot.String(),
		"imports":      layout.ImportsRoot.String(),
		"user_backups": layout.UserBackupsRoot.String(),
		"archive":      layout.ArchiveRoot.String(),
		"box_state":    layout.BoxStateRoot.String(),
		"generated":    layout.GeneratedRoot.String(),
		"notes":        layout.NotesProjectionRoot.String(),
		"retention":    layout.StorageRetentionRoot.String(),
		"operations":   layout.OperationalBackupsRoot.String(),
	}
	for key, want := range wants {
		if got[key] != want {
			t.Fatalf("%s root = %q, want %q", key, got[key], want)
		}
	}
}

func TestLayoutDerivesChildrenFromConfiguredRoots(t *testing.T) {
	root := t.TempDir()
	layout, err := New(Options{
		ServiceRoot: filepath.Join(root, "service"),
		DataRoot:    filepath.Join(root, "runtime"),
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if got, want := layout.ImportsRoot.String(), filepath.Join(root, "service", "storage", "imports"); got != want {
		t.Fatalf("ImportsRoot = %q, want %q", got, want)
	}
	if got, want := layout.NotesProjectionRoot.String(), filepath.Join(root, "runtime", "generated", "notes"); got != want {
		t.Fatalf("NotesProjectionRoot = %q, want %q", got, want)
	}
	if got, want := layout.BoxStateRoot.String(), filepath.Join(root, "runtime", "box-state"); got != want {
		t.Fatalf("BoxStateRoot = %q, want %q", got, want)
	}
}

func TestLayoutRejectsRelativeDirtyAndOverlappingRoots(t *testing.T) {
	tests := []struct {
		name string
		opts Options
	}{
		{name: "relative", opts: Options{ServiceRoot: "srv/loom"}},
		{name: "dirty", opts: Options{ServiceRoot: "/srv/loom/../loom"}},
		{name: "configuration newline", opts: Options{ServiceRoot: "/srv/loom\nLOOM_DB_URL=unsafe"}},
		{name: "ownership overlap", opts: Options{AgentsRoot: "/srv/loom/box/agents"}},
		{name: "storage child escape", opts: Options{ImportsRoot: "/srv/other/imports"}},
		{name: "internal overlap", opts: Options{GeneratedRoot: "/var/lib/loom/storage-retention/generated"}},
		{name: "arbitrary external box state", opts: Options{BoxStateRoot: "/home/operator/unrelated-state"}},
		{
			name: "inside deprecated export",
			opts: Options{
				BoxRoot:                     "/var/lib/loom/storage-views/main-export/main/Documents",
				DeprecatedStorageExportRoot: "/var/lib/loom/storage-views/main-export",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.opts); err == nil {
				t.Fatal("New returned nil error")
			}
		})
	}
}

func TestLayoutAllowsOnlyExactLegacyBoxStateCompatibilityRoot(t *testing.T) {
	layout, err := New(Options{
		BoxRoot:          "/home/loomadmin/loom-box",
		BoxStateRoot:     "/home/loomadmin/loom-box/.loom/state",
		LegacySplitRoots: true,
	})
	if err != nil {
		t.Fatalf("legacy compatibility layout: %v", err)
	}
	if got := layout.SafeFields()["legacy_box_state_compatibility_active"]; got != "true" {
		t.Fatalf("legacy compatibility field = %q", got)
	}
	if _, err := New(Options{BoxRoot: "/home/loomadmin/loom-box", BoxStateRoot: "/home/loomadmin/loom-box/.loom/other-state", LegacySplitRoots: true}); err == nil {
		t.Fatal("layout accepted a non-contract state root inside Box")
	}
	if _, err := New(Options{BoxRoot: "/srv/loom/box", BoxStateRoot: "/srv/loom/box/.loom/state", LegacySplitRoots: false}); err == nil {
		t.Fatal("post-cutover layout accepted mutable runtime state inside canonical Box")
	}
}

func TestLayoutAllowsExplicitLegacySplitRootsWithoutWeakeningCanonicalDefault(t *testing.T) {
	layout, err := New(Options{
		ServiceRoot:                 "/srv/loom",
		BoxRoot:                     "/home/loomadmin/loom-box",
		StorageRoot:                 "/srv/loom/storage",
		ImportsRoot:                 "/var/lib/loom/lane/accepted",
		UserBackupsRoot:             "/var/lib/loom/private-backups",
		ArchiveRoot:                 "/var/lib/loom/storage-archive",
		DataRoot:                    "/var/lib/loom",
		BoxStateRoot:                "/home/loomadmin/loom-box/.loom/state",
		GeneratedRoot:               "/var/lib/loom/generated",
		NotesProjectionRoot:         "/var/lib/loom/loom-notes",
		DeprecatedStorageExportRoot: "/var/lib/loom/storage-views/main-export",
		LegacySplitRoots:            true,
	})
	if err != nil {
		t.Fatalf("legacy split layout: %v", err)
	}
	if got := layout.SafeFields()["legacy_split_roots"]; got != "true" {
		t.Fatalf("legacy split diagnostic = %q", got)
	}
	if _, err := New(Options{
		StorageRoot:                 "/srv/loom/storage",
		ImportsRoot:                 "/var/lib/loom/lane/accepted",
		DeprecatedStorageExportRoot: "/var/lib/loom/storage-views/main-export",
	}); err == nil {
		t.Fatal("canonical validation accepted split roots without the transition contract")
	}
}

func TestRootJoinRejectsEscape(t *testing.T) {
	root := Root("/srv/loom/storage/imports")
	if _, err := root.Join("..", "archive"); err == nil {
		t.Fatal("Join accepted an escaping path")
	}
	if _, err := root.Join("/absolute"); err == nil {
		t.Fatal("Join accepted an absolute component")
	}
	got, err := root.Join("macbook", "2026-08-27", "batch-1")
	if err != nil {
		t.Fatalf("Join returned error: %v", err)
	}
	if got != "/srv/loom/storage/imports/macbook/2026-08-27/batch-1" {
		t.Fatalf("Join = %q", got)
	}
}
