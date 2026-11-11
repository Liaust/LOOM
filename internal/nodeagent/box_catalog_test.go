package nodeagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/filesystemconnector"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/projectcontracts"
)

func multiBoxNodeConfig(t *testing.T) (Store, Config, State, string) {
	t.Helper()
	store, cfg, state := projectWatchNodeFixture(t)
	base, err := filepath.EvalSymlinks(filepath.Dir(cfg.BoxRootPath))
	if err != nil {
		t.Fatal(err)
	}
	cfg.BoxRootPath = filepath.Join(base, "box")
	cfg.BoxesFile = filepath.Join(base, "boxes.json")
	cfg.BoxStateRoot, cfg.BoxCatalogDataRoot, cfg.BoxCatalogStorageRoot = filepath.Join(base, "data", "box-state"), filepath.Join(base, "data"), filepath.Join(base, "storage")
	company := filepath.Join(base, "boxes", "company")
	for _, binding := range []struct{ root, id string }{{cfg.BoxRootPath, "box_personal"}, {company, "box_company"}} {
		if err := os.MkdirAll(filepath.Join(binding.root, ".loom"), 0700); err != nil {
			t.Fatal(err)
		}
		raw := projectWatchJSON(map[string]string{"schema_version": "loom.box.v0.5", "box_id": binding.id, "root_path": binding.root, "owner_node": cfg.NodeKey})
		if err := os.WriteFile(filepath.Join(binding.root, ".loom", "box.yaml"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	doc := boxregistry.Document{SchemaVersion: boxregistry.SchemaVersion, DefaultBox: "personal", Boxes: []boxregistry.Entry{{Key: "personal", Label: "Personal", Node: cfg.NodeKey, RootPath: cfg.BoxRootPath}, {Key: "company", Label: "Company", Node: cfg.NodeKey, RootPath: company}}}
	if err := os.WriteFile(cfg.BoxesFile, projectWatchJSON(doc), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	return store, cfg, state, company
}

func TestMultiBoxNodeProjectAdmissionAndWatchIsolation(t *testing.T) {
	store, cfg, state, company := multiBoxNodeConfig(t)
	personal, _ := projectWatchFixturePayload(t, cfg, state, "same", 1)
	companyCfg := cfg
	companyCfg.BoxRootPath = company
	selected, _ := projectWatchFixturePayload(t, companyCfg, state, "company-same", 1)
	assertProjectWatchAck(t, BuildProjectWatchReconcileAck(t.Context(), store, state, projectWatchFixtureMessage(personal)))
	personalWorker, err := noderuntime.NewStore(store.DataDir).LoadInstance(personal.Group.Roots[0].WorkerKey)
	if err != nil {
		t.Fatal(err)
	}
	assertProjectWatchAck(t, BuildProjectWatchReconcileAck(t.Context(), store, state, projectWatchFixtureMessage(selected)))
	after, err := noderuntime.NewStore(store.DataDir).LoadInstance(personalWorker.WorkerKey)
	if err != nil || !legacyWorkerSame(after, personalWorker) {
		t.Fatal("company apply changed Personal worker", err)
	}
	cfg, err = store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Filesystem = filesystemconnector.UpsertSafeRoot(cfg.Filesystem, filesystemconnector.DefaultSafeRoot("loom_box", cfg.BoxRootPath))
	if err := os.MkdirAll(filepath.Join(cfg.BoxRootPath, "Notes"), 0700); err != nil {
		t.Fatal(err)
	}
	personalConfig, err := watchedroots.ValidateRootConfig(watchedroots.RootConfig{RootKey: "loom_box__notes", SafeRootKey: "loom_box", RootRelativePath: "Notes", Watch: watchedroots.WatchConfig{Enabled: true}}, cfg.Filesystem)
	if err != nil {
		t.Fatal(err)
	}
	personalWatch, err := applyWatchedRootValidated(store, cfg, personalConfig, "notes")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := nodeBoxForPath(cfg, selected.Group.ProjectRoot)
	if err != nil || binding.ID != "box_company" {
		t.Fatal(binding, err)
	}
	key := box.WatchSafeRootKey(binding.ID, binding.RootRef)
	rootConfig, err := watchedroots.ValidatePortableRootConfig(watchedroots.RootConfig{RootKey: key + "__notes", SafeRootKey: key, RootRelativePath: "Notes", Watch: watchedroots.WatchConfig{Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(company, "Notes"), 0700); err != nil {
		t.Fatal(err)
	}
	item := projectcontracts.ProjectWatchedRootItem{Metadata: map[string]any{"box_id": binding.ID}}
	updated, err := ensureWatchedRootPlanSafeRoot(store, cfg, rootConfig, item, company, false)
	if err != nil {
		t.Fatal(err)
	}
	safe, ok := filesystemconnector.FindSafeRoot(updated.Filesystem, key)
	if !ok || safe.AbsolutePath != company {
		t.Fatal("exact Box safe root missing")
	}
	companyValidated, err := watchedroots.ValidateRootConfig(rootConfig, updated.Filesystem)
	if err != nil {
		t.Fatal(err)
	}
	companyWatch, err := applyWatchedRootValidated(store, updated, companyValidated, "notes")
	if err != nil || companyWatch.Worker.IntervalSeconds != personalWatch.Worker.IntervalSeconds {
		t.Fatal("company watch did not retain native Box scheduling", err)
	}
	preserved, err := noderuntime.NewStore(store.DataDir).LoadInstance(personalWatch.Worker.WorkerKey)
	if err != nil || !legacyWorkerSame(preserved, personalWatch.Worker) {
		t.Fatal("company watch changed Personal watch", err)
	}
	if _, err := ensureWatchedRootPlanSafeRoot(store, updated, rootConfig, item, cfg.BoxRootPath, false); err == nil {
		t.Fatal("foreign Box retargeted safe root")
	}
	data, _ := json.Marshal(updated)
	var decoded map[string]any
	_ = json.Unmarshal(data, &decoded)
	if decoded["boxes_file"] != cfg.BoxesFile {
		t.Fatal("boxes_file was not persisted")
	}
}

func TestMultiBoxNodeCatalogFailsClosed(t *testing.T) {
	store, cfg, _, company := multiBoxNodeConfig(t)
	if _, err := nodeBoxForPath(cfg, filepath.Dir(cfg.BoxRootPath)); err == nil {
		t.Fatal("catalog admission widened to Box parent")
	}
	incomplete := cfg
	incomplete.BoxCatalogDataRoot = ""
	if err := store.SaveConfig(incomplete); err == nil {
		t.Fatal("incomplete catalog config admitted")
	}
	if err := os.Remove(filepath.Join(company, ".loom", "box.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := nodeBoxForPath(cfg, company); err == nil {
		t.Fatal("missing descriptor fell back to Personal")
	}
}
