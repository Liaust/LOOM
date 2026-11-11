package projectapply

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/config"
)

func TestMultiBoxProjectSourceSelectionAndMembership(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{NodeID: "main", BoxPath: filepath.Join(base, "personal"), BoxStateRoot: filepath.Join(base, "data", "box-state"), DataDir: filepath.Join(base, "data"), StorageRoot: filepath.Join(base, "storage"), BoxesFile: filepath.Join(base, "boxes.json")}
	company := filepath.Join(base, "company")
	for _, b := range []struct{ root, id string }{{cfg.BoxPath, "box_personal"}, {company, "box_company"}} {
		if err := os.MkdirAll(filepath.Join(b.root, ".loom"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(b.root, "Projects", "same"), 0700); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]string{"schema_version": "loom.box.v0.5", "box_id": b.id, "root_path": b.root, "owner_node": cfg.NodeID})
		if err := os.WriteFile(filepath.Join(b.root, ".loom", "box.yaml"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(boxregistry.Document{SchemaVersion: boxregistry.SchemaVersion, DefaultBox: "personal", Boxes: []boxregistry.Entry{{Key: "personal", Label: "Personal", Node: cfg.NodeID, RootPath: cfg.BoxPath}, {Key: "company", Label: "Company", Node: cfg.NodeID, RootPath: company}}})
	if err := os.WriteFile(cfg.BoxesFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx := boxregistry.WithSelection(t.Context(), boxregistry.ResolvedSelection{BoxIDs: []string{"box_company"}})
	binding, physical, err := selectedProjectBox(ctx, cfg, filepath.Join(company, "Projects", "same"))
	if err != nil || binding.ID != "box_company" || physical != filepath.Join(company, "Projects", "same") {
		t.Fatal(binding, physical, err)
	}
	if _, _, err := selectedProjectBox(ctx, cfg, filepath.Join(cfg.BoxPath, "Projects", "same")); err == nil {
		t.Fatal("foreign project admitted")
	}
	if err := validateProjectHomeBox(json.RawMessage(`{"home_box_id":"box_personal"}`), binding); err == nil {
		t.Fatal("stale home membership admitted")
	}
	if err := validateProjectHomeBox(json.RawMessage(`{"home_box_id":"box_company"}`), binding); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(cfg.BoxPath, "Projects", "same"), filepath.Join(company, "Projects", "foreign")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := selectedProjectBox(ctx, cfg, filepath.Join(company, "Projects", "foreign")); err == nil {
		t.Fatal("symlink crossed Box boundary")
	}
}

func TestPrimaryProjectLocationRevisionRetainsLegacyHash(t *testing.T) {
	const legacy = `{"box":"/srv/loom/box","node_id":"node_main","physical_root":"/srv/loom/box/Projects/same","project_root":"/srv/loom/box/Projects/same"}`
	want := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(legacy)))
	for _, id := range []string{"", "box_personal"} {
		binding := boxregistry.Box{ID: id, RootRef: "box", RootPath: "/srv/loom/box", Primary: true}
		if got := projectLocationRevision("node_main", binding, "/srv/loom/box/Projects/same", "/srv/loom/box/Projects/same"); got != want {
			t.Fatalf("primary %q changed legacy location revision: got %s want %s", id, got, want)
		}
	}
}

func TestUncataloguedProjectLocationRevisionRetainsLegacyHash(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "Projects", "same")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	binding, physical, err := selectedProjectBox(t.Context(), config.Config{BoxPath: base}, root)
	if err != nil || !binding.Primary || binding.ID != "" {
		t.Fatalf("legacy binding: %+v %v", binding, err)
	}
	legacy, err := canonicalValue(map[string]string{"node_id": "node_main", "box": base, "project_root": root, "physical_root": physical})
	if err != nil {
		t.Fatal(err)
	}
	if got := projectLocationRevision("node_main", binding, root, physical); got != hashBytes(legacy) {
		t.Fatal("absent BoxesFile invalidated legacy location revision")
	}
}

func TestAdditionalProjectLocationRevisionIncludesBoxIdentity(t *testing.T) {
	binding := boxregistry.Box{ID: "box_company", RootRef: "box:box_company", RootPath: "/srv/loom/boxes/company"}
	root := binding.RootPath + "/Projects/same"
	got := projectLocationRevision("node_main", binding, root, root)
	current, err := canonicalValue(map[string]string{"node_id": "node_main", "box": binding.RootPath, "box_id": binding.ID, "root_ref": binding.RootRef, "project_root": root, "physical_root": root})
	if err != nil || got != hashBytes(current) {
		t.Fatal("additional Box lost its identity-bound location revision")
	}
	for _, change := range []func(*boxregistry.Box){
		func(b *boxregistry.Box) { b.ID = "box_replaced" },
		func(b *boxregistry.Box) { b.RootRef = "box:box_replaced" },
	} {
		next := binding
		change(&next)
		if projectLocationRevision("node_main", next, root, root) == got {
			t.Fatal("additional Box identity change retained old location revision")
		}
	}
}
