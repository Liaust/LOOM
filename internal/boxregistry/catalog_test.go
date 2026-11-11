package boxregistry

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T) (Document, Options) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{PrimaryPath: filepath.Join(base, "box"), PrimaryStateRoot: filepath.Join(base, "state", "box-state"), DataRoot: filepath.Join(base, "state"), StorageRoot: filepath.Join(base, "storage"), Node: "main"}
	doc := Document{SchemaVersion: SchemaVersion, DefaultBox: "personal", Boxes: []Entry{
		{Key: "personal", Label: "Personal", Node: "main", RootPath: opts.PrimaryPath},
		{Key: "company", Label: "Company", Node: "main", RootPath: filepath.Join(base, "boxes", "company")},
	}}
	for _, entry := range doc.Boxes {
		if err := os.MkdirAll(filepath.Join(entry.RootPath, ".loom"), 0700); err != nil {
			t.Fatal(err)
		}
		raw := []byte("schema_version: loom.box.v0.5\nbox_id: box_" + entry.Key + "\nowner_node: main\nroot_path: " + entry.RootPath + "\n")
		if err := os.WriteFile(filepath.Join(entry.RootPath, ".loom", "box.yaml"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return doc, opts
}

func TestCatalogKeepsLegacyPrimaryAndDistinctCompany(t *testing.T) {
	doc, opts := fixture(t)
	c, err := Resolve(doc, opts)
	if err != nil {
		t.Fatal(err)
	}
	personal, err := c.Get("box")
	if err != nil {
		t.Fatal(err)
	}
	company, err := c.Get("company")
	if err != nil {
		t.Fatal(err)
	}
	if personal.ID != "box_personal" || personal.RootRef != "box" || personal.StateRoot != opts.PrimaryStateRoot {
		t.Fatalf("changed primary: %+v", personal)
	}
	if company.RootRef != "box:box_company" || company.StateRoot == personal.StateRoot {
		t.Fatalf("company not independent: %+v", company)
	}
	for _, ref := range []string{company.ID, company.Key, company.RootRef} {
		if got, err := c.Get(ref); err != nil || got.ID != company.ID {
			t.Fatalf("resolve %q: %+v %v", ref, got, err)
		}
	}
	got, err := c.ForPath(filepath.Join(company.RootPath, "Notes", "same.md"))
	if err != nil || got.ID != company.ID {
		t.Fatalf("path ownership: %+v %v", got, err)
	}
	if _, err := c.ForPath(company.RootPath + "-other/note.md"); !errors.Is(err, ErrUnknown) {
		t.Fatal("prefix sibling matched", err)
	}
}

func TestCatalogRejectsAmbiguousAndUnsafeBindings(t *testing.T) {
	for _, name := range []string{"duplicate_key", "nested", "internal", "duplicate_id", "owner", "symlink", "default"} {
		t.Run(name, func(t *testing.T) {
			doc, opts := fixture(t)
			switch name {
			case "duplicate_key":
				doc.Boxes[1].Key = "personal"
			case "nested":
				doc.Boxes[1].RootPath = filepath.Join(opts.PrimaryPath, "Company")
			case "internal":
				doc.Boxes[1].RootPath = filepath.Join(opts.DataRoot, "Company")
			case "owner":
				doc.Boxes[1].Node = "different"
			case "default":
				doc.DefaultBox = "company"
			case "duplicate_id":
				file := filepath.Join(doc.Boxes[1].RootPath, ".loom", "box.yaml")
				raw := []byte("schema_version: loom.box.v0.5\nbox_id: box_personal\nowner_node: main\nroot_path: " + doc.Boxes[1].RootPath + "\n")
				if err := os.WriteFile(file, raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				path := doc.Boxes[1].RootPath
				if err := os.Rename(path, path+"-real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+"-real", path); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Resolve(doc, opts); err == nil {
				t.Fatal("unsafe binding accepted")
			}
		})
	}
}

func TestMissingCompanyIsUnavailableNotPersonal(t *testing.T) {
	doc, opts := fixture(t)
	if err := os.RemoveAll(doc.Boxes[1].RootPath); err != nil {
		t.Fatal(err)
	}
	c, err := Resolve(doc, opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get("company"); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := c.Get("box"); err != nil {
		t.Fatal("company affected primary", err)
	}
	if _, err := c.Select(Selection{BoxIDs: []string{"company"}}, ""); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

func TestSelectionIsExplicitAndDigestStable(t *testing.T) {
	doc, opts := fixture(t)
	c, err := Resolve(doc, opts)
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.Select(Selection{}, "")
	if err != nil || s.Origin != "default" || !s.Contains("box_personal") || s.Contains("box_company") {
		t.Fatalf("default %+v %v", s, err)
	}
	s, err = c.Select(Selection{}, "company")
	if err != nil || !s.Contains("box_company") {
		t.Fatalf("bound default %+v %v", s, err)
	}
	a, err := c.Select(Selection{AllBoxes: true}, "")
	if err != nil || len(a.BoxIDs) != 2 {
		t.Fatalf("all %+v %v", a, err)
	}
	b, err := c.Select(Selection{BoxIDs: []string{"company", "box", "company"}}, "")
	if err != nil || a.Digest != b.Digest {
		t.Fatalf("unstable membership %+v %+v %v", a, b, err)
	}
	if _, err := c.Select(Selection{AllBoxes: true, BoxIDs: []string{"box"}}, ""); !errors.Is(err, ErrSelection) {
		t.Fatal(err)
	}
	if _, err := c.Select(Selection{}, "missing"); !errors.Is(err, ErrUnknown) {
		t.Fatal("bound default fell back", err)
	}
}

func TestLoadStrictCatalogAndLegacyWithoutDescriptor(t *testing.T) {
	doc, opts := fixture(t)
	opts.File = filepath.Join(filepath.Dir(opts.PrimaryPath), "boxes.json")
	raw, _ := json.Marshal(doc)
	if err := os.WriteFile(opts.File, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(opts); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(opts.File, append(raw, []byte(" {}")...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(opts); err == nil {
		t.Fatal("trailing document accepted")
	}
	opts.File = ""
	if err := os.Remove(filepath.Join(opts.PrimaryPath, ".loom", "box.yaml")); err != nil {
		t.Fatal(err)
	}
	c, err := Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := c.Get("box"); err != nil || b.ID != "" {
		t.Fatalf("legacy changed %+v %v", b, err)
	}
}
