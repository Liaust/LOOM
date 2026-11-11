package serviceregistry

import (
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
	pc "loom.local/loom/internal/projectcontracts"
)

type applicationBrowseFixture struct {
	runtime    ApplicationRuntime
	request    ApplicationBrowseRequest
	allocation string
	view       string
	private    string
	peer       uint32
}

func applicationTestBrowse(t *testing.T) applicationBrowseFixture {
	t.Helper()
	r, host, runtimeRequest, _ := applicationTestRuntime(t)
	if _, err := r.Execute(t.Context(), 1234, runtimeRequest); err != nil {
		t.Fatal(err)
	}
	policy, err := r.policy()
	if err != nil {
		t.Fatal(err)
	}
	pool := filepath.Join(host.root, "pool")
	allocation, private := filepath.Join(pool, "exports"), filepath.Join(pool, "private")
	view := filepath.Join(allocation, "notebooks")
	for _, directory := range []string{filepath.Join(view, "child"), private} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		filepath.Join(view, "paper.pdf"):             "selected PDF",
		filepath.Join(view, "child", "nested.pdf"):   "nested PDF",
		filepath.Join(allocation, "outside.txt"):     "unapproved sibling",
		filepath.Join(private, "private-secret.txt"): "private allocation",
	} {
		if err := os.WriteFile(name, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var installed ApplicationInstallation
	if err := r.Store.read("installation-"+runtimeRequest.Owner.Instance(), &installed); err != nil {
		t.Fatal(err)
	}
	// Seed real custody separately: the runtime fixture's EnsureData deliberately
	// reports a synthetic service UID/filesystem instead of the temp dirs' owner.
	installed.Data = map[string]ApplicationDataIdentity{}
	for key, directory := range map[string]string{"exports": allocation, "private": private} {
		identity, err := applicationDataIdentityOwner(directory, pool, uint32(os.Geteuid()), uint32(os.Getegid()))
		if err != nil {
			t.Fatal(err)
		}
		installed.Data[key] = identity
		policy.Grants[0].Data[key] = ApplicationDataPolicy{Path: directory, Pool: pool, BindingRef: "binding:" + key, CloudBackup: true}
	}
	applicationPrerequisitesWrite(t, r.PolicyPath, policy)
	if err := r.Store.write("installation-"+installed.Owner.Instance(), installed); err != nil {
		t.Fatal(err)
	}
	before := applicationBrowseRuntimeSnapshot(t, r, host)
	t.Cleanup(func() {
		if after := applicationBrowseRuntimeSnapshot(t, r, host); after != before {
			t.Error("browsing changed installation/publication/policy state, generation, process or restart/reload counters")
		}
	})
	return applicationBrowseFixture{
		runtime: r, peer: policy.InstallerUID, allocation: allocation, view: view, private: private,
		request: ApplicationBrowseRequest{
			Owner: installed.Owner, ProjectName: "Browse fixture", Operation: "apply", OperationToken: "browse-accept",
			ExpectedInstallationRevision: installed.Revision,
			Views: map[string]ApplicationBrowseBinding{"exports": {
				BindingRef: "binding:exports", View: pc.ApplicationDataBrowse{Label: "Exports", Subpath: "notebooks"},
			}},
		},
	}
}

func applicationBrowseRuntimeSnapshot(t *testing.T, r ApplicationRuntime, host *applicationFixtureHost) string {
	t.Helper()
	state := applicationPrerequisitesTree(t, r.Store.Root)
	delete(state, r.Store.Root) // Adding a derived browse record changes its parent's mtime.
	for name := range state {
		if strings.HasPrefix(filepath.Base(name), "browse-") {
			delete(state, name)
		}
	}
	policy, err := os.ReadFile(r.PolicyPath)
	if err != nil {
		t.Fatal(err)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	return applicationSHA(struct {
		State                       map[string]string
		Policy                      []byte
		Generation                  ApplicationGeneration
		Process                     ApplicationProcessObservation
		Loaded                      string
		Restarts, Reloads, Removals int
	}{state, policy, host.generation, host.process, host.loaded, host.restarts, host.reloads, host.removals})
}

func (f applicationBrowseFixture) accept(t *testing.T) ApplicationBrowseReceipt {
	t.Helper()
	receipt, err := f.runtime.BrowseControl(t.Context(), f.peer, f.request)
	if err != nil || receipt.State != "succeeded" || !receipt.Matches || receipt.Revision == "" {
		t.Fatalf("accept: %+v, %v", receipt, err)
	}
	return receipt
}

func (f applicationBrowseFixture) read(t *testing.T, q ApplicationBrowseRead) (ApplicationBrowseResult, *os.File) {
	t.Helper()
	result, file, err := f.runtime.BrowseRead(t.Context(), f.peer, q)
	if file != nil {
		t.Cleanup(func() { _ = file.Close() })
	}
	if err != nil {
		t.Fatalf("%s: %v", q.Operation, err)
	}
	return result, file
}

func applicationBrowseWantError(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil || code != "" && err.Error() != "application."+code {
		t.Fatalf("error = %v; want application.%s", err, code)
	}
}

func (f applicationBrowseFixture) denied(t *testing.T, peer uint32, q ApplicationBrowseRead, code string) {
	t.Helper()
	_, file, err := f.runtime.BrowseRead(t.Context(), peer, q)
	if file != nil {
		_ = file.Close()
		t.Fatal("denied read returned a descriptor")
	}
	applicationBrowseWantError(t, err, code)
}

func TestApplicationBrowseExplicitAcceptanceAndReads(t *testing.T) {
	f := applicationTestBrowse(t)
	ref := applicationBrowseRootRef(f.request.Owner, "exports")
	before, descriptor := f.read(t, ApplicationBrowseRead{Operation: "catalog"})
	if len(before.Roots) != 0 || descriptor != nil {
		t.Fatalf("unaccepted data exposed: %+v", before)
	}
	f.denied(t, f.peer, ApplicationBrowseRead{Operation: "open", Ref: ref, Path: "paper.pdf"}, "browse.private")
	receipt := f.accept(t)
	var record ApplicationBrowseRecord
	key := "browse-" + f.request.Owner.Instance()
	if err := f.runtime.Store.read(key, &record); err != nil {
		t.Fatal(err)
	}
	if record.Revision != receipt.Revision || record.Owner != f.request.Owner || len(record.Views) != 1 || len(record.Data) != 1 || record.ViewInodes["exports"].Inode == 0 {
		t.Fatalf("derived acceptance record: %+v", record)
	}
	if _, exists := record.Data["private"]; exists {
		t.Fatal("private identity copied into browse record")
	}
	var st unix.Stat_t
	if err := unix.Lstat(filepath.Join(f.runtime.Store.Root, key+".json"), &st); err != nil || st.Uid != f.runtime.Store.OwnerUID || uint32(st.Mode)&0777 != 0600 {
		t.Fatalf("derived record custody/mode: %+v, %v", st, err)
	}
	inspect := f.request
	inspect.Operation = "inspect"
	observed, err := f.runtime.BrowseControl(t.Context(), f.peer, inspect)
	if err != nil || !observed.Matches || observed.Revision != receipt.Revision || observed.State != "observed" {
		t.Fatalf("inspect: %+v, %v", observed, err)
	}
	catalog, descriptor := f.read(t, ApplicationBrowseRead{Operation: "catalog", ProjectID: f.request.Owner.ProjectID})
	if descriptor != nil || len(catalog.Roots) != 1 {
		t.Fatalf("catalog: %+v", catalog)
	}
	root := catalog.Roots[0]
	if root.Ref != ref || root.SourcePath != f.view || root.DataKey != "exports" || root.ProjectName != f.request.ProjectName || root.Label != "Exports" || root.Format != "files" || !root.ReadOnly || !root.Available || root.Revision != receipt.Revision {
		t.Fatalf("root: %+v", root)
	}
	other, _ := f.read(t, ApplicationBrowseRead{Operation: "catalog", ProjectID: "project_01ARZ3NDEKTSV4RRFFQ69G5FAW"})
	if len(other.Roots) != 0 {
		t.Fatalf("project filter ignored: %+v", other)
	}
	privateRef := applicationBrowseRootRef(f.request.Owner, "private")
	f.denied(t, f.peer, ApplicationBrowseRead{Operation: "resolve", Ref: privateRef}, "browse.private")
	result, file := f.read(t, ApplicationBrowseRead{Operation: "open", Ref: ref, Path: "paper.pdf", Revision: receipt.Revision})
	if file == nil || result.Root == nil || result.Root.Ref != ref {
		t.Fatalf("open descriptor/root: %+v, %v", result, file)
	}
	body, err := io.ReadAll(file)
	if err != nil || string(body) != "selected PDF" {
		t.Fatalf("selected content: %q, %v", body, err)
	}
	if _, err := file.Write([]byte("forbidden")); err == nil {
		t.Fatal("opened descriptor is writable")
	}
	_, directory := f.read(t, ApplicationBrowseRead{Operation: "open", Ref: ref, Revision: receipt.Revision})
	if directory == nil {
		t.Fatal("directory open returned no descriptor")
	}
	if info, err := directory.Stat(); err != nil || !info.IsDir() {
		t.Fatalf("directory descriptor: %v, %v", info, err)
	}
	children, descriptor := f.read(t, ApplicationBrowseRead{Operation: "children", Ref: ref, Revision: receipt.Revision, Names: []string{"paper.pdf", "child", "missing"}})
	if descriptor != nil || len(children.Entries) != 2 || children.Entries[0].Name != "paper.pdf" || children.Entries[0].Kind != "file" || children.Entries[0].Size != 12 || children.Entries[1].Kind != "directory" {
		t.Fatalf("children: %+v", children)
	}
	nested, _ := f.read(t, ApplicationBrowseRead{Operation: "children", Ref: ref, Path: "child", Revision: receipt.Revision, Names: []string{"nested.pdf"}})
	if len(nested.Entries) != 1 || nested.Entries[0].Path != "child/nested.pdf" {
		t.Fatalf("nested children: %+v", nested)
	}
}

func TestApplicationBrowseRejectsTraversalAndSymlinks(t *testing.T) {
	f := applicationTestBrowse(t)
	receipt := f.accept(t)
	ref := applicationBrowseRootRef(f.request.Owner, "exports")
	for _, name := range []string{"../outside.txt", "/etc/passwd", "child/../../outside.txt", `child\nested.pdf`, "child//nested.pdf", "child/./nested.pdf", "paper\x00.pdf"} {
		t.Run(name, func(t *testing.T) {
			f.denied(t, f.peer, ApplicationBrowseRead{Operation: "open", Ref: ref, Path: name, Revision: receipt.Revision}, "browse.path_invalid")
		})
	}
	for name, target := range map[string]string{"linked.pdf": filepath.Join(f.private, "private-secret.txt"), "linked-dir": f.private} {
		if err := os.Symlink(target, filepath.Join(f.view, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"linked.pdf", "linked-dir/private-secret.txt"} {
		f.denied(t, f.peer, ApplicationBrowseRead{Operation: "open", Ref: ref, Path: name}, "")
	}
	children, _ := f.read(t, ApplicationBrowseRead{Operation: "children", Ref: ref, Names: []string{"linked.pdf", "linked-dir", "paper.pdf"}})
	if len(children.Entries) != 1 || children.Entries[0].Name != "paper.pdf" {
		t.Fatalf("symlink children exposed: %+v", children)
	}
	for _, name := range []string{"..", "child/nested.pdf", `child\nested.pdf`} {
		f.denied(t, f.peer, ApplicationBrowseRead{Operation: "children", Ref: ref, Names: []string{name}}, "browse.path_invalid")
	}
	for _, subpath := range []string{"../private", "/etc", "notebooks/linked-dir"} {
		q := f.request
		q.Views = maps.Clone(q.Views)
		q.OperationToken, q.ExpectedRevision = "browse-invalid-subpath", receipt.Revision
		binding := q.Views["exports"]
		binding.View.Subpath = subpath
		q.Views["exports"] = binding
		_, err := f.runtime.BrowseControl(t.Context(), f.peer, q)
		applicationBrowseWantError(t, err, "")
	}
}

func TestApplicationBrowseWrongPeer(t *testing.T) {
	f := applicationTestBrowse(t)
	for _, peer := range []uint32{0, f.peer + 1} {
		for _, operation := range []string{"inspect", "apply"} {
			q := f.request
			q.Operation = operation
			_, err := f.runtime.BrowseControl(t.Context(), peer, q)
			applicationBrowseWantError(t, err, "peer.denied")
		}
	}
	f.accept(t)
	for _, peer := range []uint32{0, f.peer + 1} {
		for _, operation := range []string{"catalog", "resolve", "children", "open"} {
			f.denied(t, peer, ApplicationBrowseRead{Operation: operation, Ref: applicationBrowseRootRef(f.request.Owner, "exports"), Path: "paper.pdf"}, "peer.denied")
		}
	}
}

func TestApplicationBrowseCASAndReplay(t *testing.T) {
	f := applicationTestBrowse(t)
	q := f.request
	q.ExpectedInstallationRevision = "stale-installation"
	_, err := f.runtime.BrowseControl(t.Context(), f.peer, q)
	applicationBrowseWantError(t, err, "browse.revision_conflict")
	receipt := f.accept(t)
	before := applicationPrerequisitesTree(t, f.runtime.Store.Root)
	again, err := f.runtime.BrowseControl(t.Context(), f.peer, f.request)
	if err != nil || !reflect.DeepEqual(receipt, again) || !reflect.DeepEqual(before, applicationPrerequisitesTree(t, f.runtime.Store.Root)) {
		t.Fatalf("token replay changed receipt or state: %+v, %v", again, err)
	}
	q = f.request
	q.ProjectName = "different input"
	_, err = f.runtime.BrowseControl(t.Context(), f.peer, q)
	applicationBrowseWantError(t, err, "operation.token_conflict")
	for _, stale := range []string{"view", "installation", "runtime"} {
		q = f.request
		q.OperationToken, q.ExpectedRevision = "browse-cas", receipt.Revision
		switch stale {
		case "view":
			q.ExpectedRevision = "stale-view"
		case "installation":
			q.ExpectedInstallationRevision = "stale-installation"
		case "runtime":
			q.RuntimeDesired = "stale-runtime"
		}
		_, err = f.runtime.BrowseControl(t.Context(), f.peer, q)
		applicationBrowseWantError(t, err, "browse.revision_conflict")
	}
	var installed ApplicationInstallation
	if err := f.runtime.Store.read("installation-"+f.request.Owner.Instance(), &installed); err != nil || installed.Request == nil {
		t.Fatalf("installed request: %+v, %v", installed, err)
	}
	q = f.request
	q.Views = maps.Clone(q.Views)
	binding := q.Views["exports"]
	binding.View.Label = "Renamed exports"
	q.Views["exports"] = binding
	q.OperationToken, q.ExpectedRevision = "browse-update", receipt.Revision
	q.ExpectedInstallationRevision, q.RuntimeDesired = "stale-installation", installed.Request.RuntimeDesiredDigest()
	updated, err := f.runtime.BrowseControl(t.Context(), f.peer, q)
	if err != nil || updated.Revision == receipt.Revision || updated.State != "succeeded" {
		t.Fatalf("authorized runtime-matching update: %+v, %v", updated, err)
	}
	_, err = f.runtime.BrowseControl(t.Context(), f.peer, f.request)
	applicationBrowseWantError(t, err, "browse.revision_conflict")
	ref := applicationBrowseRootRef(f.request.Owner, "exports")
	f.denied(t, f.peer, ApplicationBrowseRead{Operation: "open", Ref: ref, Path: "paper.pdf", Revision: receipt.Revision}, "browse.view_changed")
	resolved, _ := f.read(t, ApplicationBrowseRead{Operation: "resolve", Ref: ref, Revision: updated.Revision})
	if resolved.Root == nil || resolved.Root.Label != "Renamed exports" || resolved.Root.Ref != ref {
		t.Fatalf("label update lost root identity: %+v", resolved)
	}
}

func TestApplicationBrowseRevoke(t *testing.T) {
	f := applicationTestBrowse(t)
	receipt := f.accept(t)
	q := f.request
	q.OperationToken, q.ExpectedRevision, q.Views = "browse-revoke", receipt.Revision, map[string]ApplicationBrowseBinding{}
	revoked, err := f.runtime.BrowseControl(t.Context(), f.peer, q)
	if err != nil || revoked.State != "succeeded" || revoked.Revision == receipt.Revision {
		t.Fatalf("revoke: %+v, %v", revoked, err)
	}
	catalog, _ := f.read(t, ApplicationBrowseRead{Operation: "catalog"})
	if len(catalog.Roots) != 0 {
		t.Fatalf("revoked root remains discoverable: %+v", catalog)
	}
	ref := applicationBrowseRootRef(f.request.Owner, "exports")
	for _, operation := range []string{"resolve", "open", "children"} {
		f.denied(t, f.peer, ApplicationBrowseRead{Operation: operation, Ref: ref, Path: "paper.pdf", Revision: receipt.Revision}, "browse.private")
	}
	if body, err := os.ReadFile(filepath.Join(f.view, "paper.pdf")); err != nil || string(body) != "selected PDF" {
		t.Fatalf("revoke changed payload: %q, %v", body, err)
	}
}

func TestApplicationBrowseChangedInodes(t *testing.T) {
	for _, changed := range []string{"allocation", "subpath"} {
		t.Run(changed, func(t *testing.T) {
			f := applicationTestBrowse(t)
			receipt := f.accept(t)
			directory := f.view
			if changed == "allocation" {
				directory = f.allocation
			}
			if err := os.Rename(directory, directory+"-retained"); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(f.view, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.view, "paper.pdf"), []byte("replacement must stay private"), 0600); err != nil {
				t.Fatal(err)
			}
			ref := applicationBrowseRootRef(f.request.Owner, "exports")
			for _, operation := range []string{"resolve", "open", "children"} {
				f.denied(t, f.peer, ApplicationBrowseRead{Operation: operation, Ref: ref, Path: "paper.pdf", Revision: receipt.Revision}, "browse.custody_changed")
			}
			catalog, _ := f.read(t, ApplicationBrowseRead{Operation: "catalog"})
			if len(catalog.Roots) != 1 || catalog.Roots[0].Available || catalog.Roots[0].Reason != "application.browse.custody_changed" {
				t.Fatalf("changed custody availability: %+v", catalog)
			}
			if changed == "allocation" {
				q := f.request
				q.OperationToken, q.ExpectedRevision = "browse-replaced-allocation", receipt.Revision
				_, err := f.runtime.BrowseControl(t.Context(), f.peer, q)
				applicationBrowseWantError(t, err, "browse.custody_changed")
			}
		})
	}
}
