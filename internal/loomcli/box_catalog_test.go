package loomcli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/response"
)

type cliBoxCatalogFixture struct {
	root, personal, company, data, personalState, offlineSocket string
	catalog                                                     boxregistry.Catalog
}

func newCLIBoxCatalogFixture(t *testing.T, companyState string) cliBoxCatalogFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := cliBoxCatalogFixture{root: root, personal: filepath.Join(root, "personal"), company: filepath.Join(root, "companies", "company"), data: filepath.Join(root, "runtime"), personalState: filepath.Join(root, "runtime", "personal-state"), offlineSocket: filepath.Join(root, "absent.sock")}
	configPath, catalogPath := filepath.Join(root, "loom.env"), filepath.Join(root, "boxes.json")
	if err := os.WriteFile(configPath, []byte("LOOM_ENV=development\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"LOOM_CONFIG_FILE": configPath, "LOOM_BOXES_FILE": catalogPath,
		"LOOM_NODE_ID": "main", "LOOM_NODE_KIND": "main", "LOOM_NODE_ROLE": "main",
		"LOOM_DATA_DIR": f.data, "LOOM_SERVICE_ROOT": filepath.Join(root, "service"), "LOOM_STORAGE_ROOT": filepath.Join(root, "storage"),
		"LOOM_BOX_PATH": f.personal, "LOOM_BOX_STATE_ROOT": f.personalState, "LOOM_BOX_PROFILE": "main",
		"LOOM_SOCKET_PATH": f.offlineSocket, "LOOM_MAIN_URL": "", "LOOM_BOX_ID": "",
	} {
		t.Setenv(name, value)
	}
	doc := boxregistry.Document{SchemaVersion: boxregistry.SchemaVersion, DefaultBox: "personal", Boxes: []boxregistry.Entry{
		{Key: "personal", Label: "Personal", Node: "main", RootPath: f.personal},
		{Key: "company", Label: "Company", Node: "main", RootPath: f.company},
	}}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalogPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	initialize := func(root, id, ref, state string) {
		t.Helper()
		resolved, err := box.Resolve(box.ResolveInput{BoxID: id, RootRef: ref, ConfiguredPath: root, RuntimeStateRoot: state, NodeID: "main", NodeRole: "main"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := box.Init(box.InitInput{Resolved: resolved, NewBoxID: func() string { return id }}); err != nil {
			t.Fatal(err)
		}
	}
	initialize(f.personal, "box_personal", "box", f.personalState)
	switch companyState {
	case "available":
		initialize(f.company, "box_company", "box:box_company", filepath.Join(f.data, "boxes", "box_company"))
	case "descriptor_missing":
		if err := os.MkdirAll(f.company, 0700); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := resolveCLIConfig(&options{})
	if err != nil {
		t.Fatal(err)
	}
	f.catalog, err = cfg.BoxCatalog()
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f cliBoxCatalogFixture) serve(t *testing.T) string {
	t.Helper()
	socket, stop := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/boxes" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		response.WriteJSON(w, http.StatusOK, response.Success("box-catalog-test", f.catalog))
	})
	t.Cleanup(stop)
	return socket
}

func TestBoxCLISelectedCatalogFieldsAndPathJSON(t *testing.T) {
	f := newCLIBoxCatalogFixture(t, "available")
	socket := f.serve(t)
	t.Setenv("LOOM_BOX_ID", "box_personal")
	resolved, err := resolveBoxForCLI(&options{boxRef: "company", socketPath: socket}, boxCommandFlags{})
	if err != nil || resolved.BoxID != "box_company" || resolved.RootRef != "box:box_company" || resolved.RootPath != f.company || resolved.RuntimeStateRoot != filepath.Join(f.data, "boxes", "box_company") || resolved.OwnerNode != "main" {
		t.Fatalf("selected identity/root/state lost: %+v err=%v", resolved, err)
	}
	stdout, stderr, err := executeRootCommand("--socket", socket, "--box", "company", "--json", "box", "path", "--path", f.company)
	if err != nil {
		t.Fatalf("box path: %v %s", err, stderr)
	}
	var got box.Resolved
	if err := json.Unmarshal([]byte(stdout), &got); err != nil || got.BoxID != resolved.BoxID || got.RootRef != resolved.RootRef || got.RootPath != resolved.RootPath || got.RuntimeStateRoot != resolved.RuntimeStateRoot {
		t.Fatalf("path JSON lost selected fields: %s err=%v", stdout, err)
	}
	if personal, err := resolveBoxForCLI(&options{boxRef: "personal", socketPath: socket}, boxCommandFlags{}); err != nil || personal.BoxID != "box_personal" || personal.RootRef != "box" || personal.RuntimeStateRoot != f.personalState {
		t.Fatalf("Personal identity/state changed: %+v err=%v", personal, err)
	}
	t.Setenv("LOOM_BOX_ID", "box_company")
	if got, err := resolveBoxForCLI(&options{socketPath: socket}, boxCommandFlags{}); err != nil || got.BoxID != "box_company" {
		t.Fatalf("profile-bound Box ignored: %+v err=%v", got, err)
	}
}

func TestBoxCLIInitUnavailableCompanyUsesOneIdentityAndLiveCatalog(t *testing.T) {
	for _, state := range []string{"root_missing", "descriptor_missing"} {
		t.Run(state, func(t *testing.T) {
			f := newCLIBoxCatalogFixture(t, state)
			socket := f.serve(t) // Deliberately stays unavailable after initialization.
			personalBefore, err := os.ReadFile(box.ContractPath(f.personal))
			if err != nil {
				t.Fatal(err)
			}
			var id string
			for attempt := 0; attempt < 2; attempt++ {
				stdout, stderr, err := executeRootCommand("--socket", socket, "--box", "company", "--json", "box", "init")
				if err != nil {
					t.Fatalf("init attempt %d: %v stderr=%s stdout=%s", attempt, err, stderr, stdout)
				}
				var result box.InitResult
				if err := json.Unmarshal([]byte(stdout), &result); err != nil || result.StatusAfter.Contract == nil {
					t.Fatalf("invalid init result: %s err=%v", stdout, err)
				}
				actual := result.StatusAfter.Contract.BoxID
				if attempt == 0 {
					id = actual
				}
				if !strings.HasPrefix(actual, "box_") || actual != id || actual == "box_personal" || result.RootPath != f.company || result.OwnerNode != "main" || result.StatusAfter.RuntimeStateRoot != filepath.Join(f.data, "boxes", id) {
					t.Fatalf("descriptor identity and state diverged: %+v id=%s", result, id)
				}
			}
			cfg, err := resolveCLIConfig(&options{})
			if err != nil {
				t.Fatal(err)
			}
			catalog, err := cfg.BoxCatalog()
			if err != nil {
				t.Fatal(err)
			}
			binding, err := catalog.Get("company")
			if err != nil || binding.ID != id || binding.RootRef != "box:"+id || binding.StateRoot != filepath.Join(f.data, "boxes", id) {
				t.Fatalf("live catalog differs from initialized identity: %+v %v", binding, err)
			}
			personalAfter, err := os.ReadFile(box.ContractPath(f.personal))
			if err != nil || string(personalAfter) != string(personalBefore) {
				t.Fatal("Personal descriptor changed")
			}
			if _, err := os.Stat(f.personalState); !os.IsNotExist(err) {
				t.Fatalf("Company init touched Personal runtime state: %v", err)
			}
		})
	}
}

func TestBoxCLIInitOfflineLocalCatalogAndDryRun(t *testing.T) {
	f := newCLIBoxCatalogFixture(t, "root_missing")
	for _, dryRun := range []bool{true, false} {
		args := []string{"--socket", f.offlineSocket, "--box", "company", "--json", "box", "init"}
		if dryRun {
			args = append(args, "--dry-run")
		}
		stdout, stderr, err := executeRootCommand(args...)
		if err != nil {
			t.Fatalf("owning-node offline init: %v %s %s", err, stderr, stdout)
		}
		if dryRun {
			if _, err := os.Stat(f.company); !os.IsNotExist(err) {
				t.Fatalf("dry run created source root: %v", err)
			}
			if _, err := os.Stat(filepath.Join(f.data, "boxes")); !os.IsNotExist(err) {
				t.Fatalf("dry run created state: %v", err)
			}
		}
	}
}

func TestBoxCLIRejectsForeignDaemonBeforeInitMutation(t *testing.T) {
	f := newCLIBoxCatalogFixture(t, "root_missing")
	for i := range f.catalog.Boxes {
		f.catalog.Boxes[i].Node = "foreign-main"
	}
	socket := f.serve(t)
	for _, extra := range [][]string{nil, {"--path", f.company}} {
		args := append([]string{"--socket", socket, "--box", "company", "box", "init"}, extra...)
		if _, _, err := executeRootCommand(args...); err == nil || !strings.Contains(err.Error(), "not local node") {
			t.Fatalf("foreign daemon accepted: %v", err)
		}
	}
	if _, err := os.Stat(f.company); !os.IsNotExist(err) {
		t.Fatalf("foreign daemon caused local source mutation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.data, "boxes")); !os.IsNotExist(err) {
		t.Fatalf("foreign daemon caused local state mutation: %v", err)
	}
}

func TestBoxCLIRejectsBoundPathMismatchAndUnavailableReads(t *testing.T) {
	f := newCLIBoxCatalogFixture(t, "root_missing")
	socket := f.serve(t)
	for _, test := range []struct{ ref, path string }{{"company", f.personal}, {"personal", f.company}} {
		_, _, err := executeRootCommand("--socket", socket, "--box", test.ref, "box", "init", "--path", test.path)
		if err == nil || !strings.Contains(err.Error(), "explicit path disagrees") {
			t.Fatalf("bound path mismatch accepted: %v", err)
		}
	}
	if _, err := resolveBoxForCLI(&options{boxRef: "company", socketPath: socket}, boxCommandFlags{}); !errors.Is(err, boxregistry.ErrUnavailable) {
		t.Fatalf("unavailable read allocated or fell back: %v", err)
	}
	if _, err := resolveBoxForCLI(&options{boxRef: "unknown", socketPath: socket}, boxCommandFlags{initialize: true}); !errors.Is(err, boxregistry.ErrUnknown) {
		t.Fatalf("unknown Box accepted: %v", err)
	}
	if _, err := os.Stat(f.company); !os.IsNotExist(err) {
		t.Fatalf("rejected selection wrote Company: %v", err)
	}
}

func TestBoxCLIKeepsUnboundOfflineResolution(t *testing.T) {
	f := newCLIBoxCatalogFixture(t, "root_missing")
	for _, path := range []string{"", filepath.Join(f.root, "offline-workspace")} {
		resolved, err := resolveBoxForCLI(&options{socketPath: f.offlineSocket}, boxCommandFlags{path: path})
		want := path
		if want == "" {
			want = f.personal
		}
		if err != nil || resolved.RootPath != want || resolved.RuntimeStateRoot != f.personalState || resolved.BoxID != "" || resolved.RootRef != "" {
			t.Fatalf("legacy offline resolution changed: %+v %v", resolved, err)
		}
	}
	if _, err := resolveBoxForCLI(&options{allBoxes: true}, boxCommandFlags{path: f.company}); err == nil {
		t.Fatal("single-root command accepted all Boxes")
	}
}

func TestBoxCLIRefusesUnavailableWithoutLocalCatalog(t *testing.T) {
	f := newCLIBoxCatalogFixture(t, "root_missing")
	socket := f.serve(t)
	t.Setenv("LOOM_BOXES_FILE", "")
	if _, err := resolveBoxForCLI(&options{boxRef: "company", socketPath: socket}, boxCommandFlags{initialize: true}); !errors.Is(err, boxregistry.ErrUnavailable) {
		t.Fatalf("unavailable remote snapshot used as initialization authority: %v", err)
	}
	if _, err := os.Stat(f.company); !os.IsNotExist(err) {
		t.Fatalf("unavailable remote snapshot caused local write: %v", err)
	}
}

func TestBoxCLIRejectsAdditionalBoxWithoutState(t *testing.T) {
	f := newCLIBoxCatalogFixture(t, "available")
	f.catalog.Boxes[1].StateRoot = ""
	socket := f.serve(t)
	if _, err := resolveBoxForCLI(&options{boxRef: "company", socketPath: socket}, boxCommandFlags{}); err == nil {
		t.Fatal("additional Box fell back to Personal runtime state")
	}
}

func TestResolveCLIClientBoxContextFromWorkingDirectory(t *testing.T) {
	f := newCLIBoxCatalogFixture(t, "available")
	nested := filepath.Join(f.company, "Projects", "example")
	prefix := f.company + "-other"
	archive := f.catalog.Boxes[1].ArchiveRoot
	archivedProject := filepath.Join(archive, "Projects", "archived-example")
	archivePrefix := archive + "-other"
	for _, path := range []string{nested, prefix, archivedProject, archivePrefix} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(f.root, "company-alias")
	cross := filepath.Join(f.company, "personal-alias")
	escape := filepath.Join(f.company, "outside-alias")
	archiveAlias := filepath.Join(f.root, "archive-alias")
	for link, target := range map[string]string{alias: nested, cross: f.personal, escape: prefix, archiveAlias: archivedProject} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name, cwd, envRef, ref, pwd string
		opts                        *options
		all, noCatalog              bool
	}{
		{name: "company-root", cwd: f.company, ref: "box_company"},
		{name: "company-nested", cwd: nested, ref: "box_company"},
		{name: "personal", cwd: f.personal, ref: "box_personal"},
		{name: "outside", cwd: f.root},
		{name: "filesystem-root", cwd: "/"},
		{name: "not-a-prefix-match", cwd: prefix},
		{name: "symlink-into-company", cwd: alias, ref: "box_company"},
		{name: "symlink-across-boxes", cwd: cross, ref: "box_personal"},
		{name: "symlink-outside", cwd: escape},
		{name: "spoofed-pwd", cwd: f.root, pwd: f.company},
		{name: "company-archive-root", cwd: archive, ref: "box_company"},
		{name: "company-archived-project", cwd: archivedProject, ref: "box_company"},
		{name: "company-archive-alias", cwd: archiveAlias, ref: "box_company"},
		{name: "archive-component-boundary", cwd: archivePrefix},
		{name: "personal-storage-stays-legacy", cwd: filepath.Join(f.root, "storage")},
		{name: "flag-over-archive", cwd: archivedProject, opts: &options{boxRef: "personal"}, ref: "personal"},
		{name: "flag-over-env-and-cwd", cwd: f.company, envRef: "box_company", opts: &options{boxRef: "personal"}, ref: "personal"},
		{name: "env-over-cwd", cwd: f.company, envRef: " box_personal ", ref: "box_personal"},
		{name: "all-over-cwd", cwd: f.company, opts: &options{allBoxes: true}, all: true},
		{name: "all-preserves-env", cwd: f.company, envRef: "box_personal", opts: &options{allBoxes: true}, ref: "box_personal", all: true},
		{name: "legacy-without-catalog", cwd: f.company, noCatalog: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(test.cwd)
			t.Setenv("PWD", firstNonEmpty(test.pwd, test.cwd))
			t.Setenv("LOOM_BOX_ID", test.envRef)
			if test.noCatalog {
				t.Setenv("LOOM_BOXES_FILE", "")
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/v1/health" {
					t.Errorf("unexpected context lookup: %s %s", r.Method, r.URL)
				}
				if got := r.Header.Get("X-Loom-Box"); got != test.ref {
					t.Errorf("Box header = %q, want %q", got, test.ref)
				}
				if got := r.URL.Query().Get("all_boxes"); (got == "true") != test.all {
					t.Errorf("all_boxes = %q, want %v", got, test.all)
				}
				response.WriteJSON(w, http.StatusOK, response.Success("cwd-test", struct{}{}))
			}))
			defer server.Close()
			t.Setenv("LOOM_MAIN_URL", server.URL)
			_, client, err := commandClient(test.opts)
			if err != nil {
				t.Fatal(err)
			}
			if got := requests.Load(); got != 0 {
				t.Fatalf("context selection made %d remote calls", got)
			}
			if _, err := client.Health(context.Background(), "cwd-test"); err != nil {
				t.Fatal(err)
			}
			if got := requests.Load(); got != 1 {
				t.Fatalf("requests = %d, want only the command request", got)
			}
		})
	}
}

func TestResolveCLIClientRejectsUnavailableWorkingDirectoryBox(t *testing.T) {
	f := newCLIBoxCatalogFixture(t, "descriptor_missing")
	t.Chdir(f.company)
	if _, _, err := commandClient(nil); !errors.Is(err, boxregistry.ErrUnavailable) {
		t.Fatalf("unavailable Company fell back to Personal: %v", err)
	}
	for _, test := range []struct {
		name, envRef string
		opts         *options
	}{
		{"flag", "", &options{boxRef: "personal"}},
		{"env", "box_personal", nil},
		{"all", "", &options{allBoxes: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("LOOM_BOX_ID", test.envRef)
			if _, _, err := commandClient(test.opts); err != nil {
				t.Fatalf("explicit selection lost precedence over unavailable cwd: %v", err)
			}
		})
	}
}

func TestResolveCLIClientRejectsUnavailableCompanyArchive(t *testing.T) {
	for _, state := range []string{"root_missing", "descriptor_missing"} {
		t.Run(state, func(t *testing.T) {
			f := newCLIBoxCatalogFixture(t, state)
			archive := filepath.Join(f.root, "storage", "Boxes", "company", "Projects", "archived-example")
			if err := os.MkdirAll(archive, 0700); err != nil {
				t.Fatal(err)
			}
			t.Chdir(archive)
			if _, _, err := commandClient(nil); !errors.Is(err, boxregistry.ErrUnavailable) {
				t.Fatalf("unavailable Company archive fell back to Personal: %v", err)
			}
		})
	}
}

func TestResolveCLIClientRejectsRedirectedCompanyArchive(t *testing.T) {
	f := newCLIBoxCatalogFixture(t, "available")
	archive := filepath.Join(f.root, "storage", "Boxes", "company")
	other := filepath.Join(f.root, "unbound")
	if err := os.MkdirAll(filepath.Dir(archive), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, archive); err != nil {
		t.Fatal(err)
	}
	t.Chdir(other)
	if _, _, err := commandClient(nil); !errors.Is(err, boxregistry.ErrUnavailable) {
		t.Fatalf("redirected Company archive accepted: %v", err)
	}
}

func TestResolveCLIClientWorkingDirectoryRequiresValidatedCatalog(t *testing.T) {
	for _, test := range []string{"missing-catalog", "invalid-catalog", "descriptor-mismatch", "symlink-root"} {
		t.Run(test, func(t *testing.T) {
			f := newCLIBoxCatalogFixture(t, "available")
			t.Chdir(f.company)
			switch test {
			case "missing-catalog":
				t.Setenv("LOOM_BOXES_FILE", filepath.Join(f.root, "absent.json"))
			case "invalid-catalog":
				if err := os.WriteFile(os.Getenv("LOOM_BOXES_FILE"), []byte(`{"schema_version":"untrusted","boxes":[]}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "descriptor-mismatch":
				if err := os.WriteFile(box.ContractPath(f.company), []byte("schema_version: loom.box.v0.5\nbox_id: box_company\nowner_node: foreign\nroot_path: "+f.company+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink-root":
				moved := f.company + "-moved"
				if err := os.Rename(f.company, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, f.company); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := commandClient(nil); err == nil {
				t.Fatal("unvalidated catalog silently used or ignored")
			}
			if _, _, err := commandClient(&options{boxRef: "personal"}); err != nil {
				t.Fatalf("explicit selection unexpectedly depends on local catalog: %v", err)
			}
		})
	}
	if _, err := resolveCLIClient(config.Config{}, &options{boxRef: "personal", allBoxes: true}); err == nil {
		t.Fatal("--box and --all-boxes conflict accepted")
	}
}
