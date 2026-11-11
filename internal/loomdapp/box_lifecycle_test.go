package loomdapp

import (
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/boxregistry"
)

func TestAdditionalBoxArchiveAbsenceDoesNotBindPersonalOrCreateCustody(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "company")
	archive := filepath.Join(root, "missing")
	if err := os.Mkdir(source, 0750); err != nil {
		t.Fatal(err)
	}
	catalog := boxregistry.Catalog{Boxes: []boxregistry.Box{
		{ID: "box_personal", Key: "personal", RootRef: "box", Primary: true, Available: true},
		{ID: "box_company", Key: "company", RootRef: "box:box_company", RootPath: source, ArchiveRoot: archive, Available: true},
		{ID: "box_unavailable", Key: "unavailable", Available: false},
	}}
	held := []string{}
	owners := additionalBoxArchiveRoots(catalog, func(box boxregistry.Box, err error) {
		if err == nil {
			t.Fatal("missing custody has no diagnostic")
		}
		held = append(held, box.ID)
	})
	if len(owners) != 0 || len(held) != 1 || held[0] != "box_company" {
		t.Fatalf("unexpected owners/holds: %v %v", owners, held)
	}
	if _, err := os.Lstat(archive); !os.IsNotExist(err) {
		t.Fatalf("startup must not create custody: %v", err)
	}
	if err := os.Mkdir(archive, 0750); err != nil {
		t.Fatal(err)
	}
	owners = additionalBoxArchiveRoots(catalog, nil)
	owner, ok := owners["box_company"]
	if !ok || len(owners) != 1 || owner.BoxID != "box_company" || owner.Primary || owner.BoxRoot != source || owner.StorageRoot != archive {
		t.Fatalf("available custody not bound exactly: %+v", owners)
	}
	for _, path := range []string{"Projects", "Library", "Records", ".loom/workspace-archives"} {
		if info, err := os.Lstat(filepath.Join(archive, path)); err != nil || !info.IsDir() {
			t.Fatalf("managed archive directory %s missing: %v", path, err)
		}
	}
	kept := filepath.Join(archive, "Projects", "kept.txt")
	if err := os.WriteFile(kept, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	if len(additionalBoxArchiveRoots(catalog, nil)) != 1 {
		t.Fatal("repeated startup lost a valid archive owner")
	}
	if raw, err := os.ReadFile(kept); err != nil || string(raw) != "retained" {
		t.Fatal("repeated preparation changed retained data", err)
	}
	if err := os.Rename(filepath.Join(archive, "Projects"), filepath.Join(archive, "held-projects")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(archive, "held-projects"), filepath.Join(archive, "Projects")); err != nil {
		t.Fatal(err)
	}
	if len(additionalBoxArchiveRoots(catalog, nil)) != 0 {
		t.Fatal("unsafe managed child admitted into lifecycle owner")
	}
}
