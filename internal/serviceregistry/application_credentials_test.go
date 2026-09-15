package serviceregistry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

type applicationCredentialFixture struct {
	calls int
	fail  bool
	err   error
}

func (f *applicationCredentialFixture) ResolveApplicationCredential(context.Context, ApplicationOwner, string) ([]byte, error) {
	f.calls++
	if f.err != nil {
		return []byte("private-fixture-value"), f.err
	}
	if f.fail {
		return []byte("private-fixture-value"), errors.New("secret in upstream stderr")
	}
	return []byte("private-fixture-value\n"), nil
}

func applicationProtonFixture(t *testing.T) (ApplicationRuntime, ApplicationHostPolicy, ApplicationProvisionRequest, *applicationCredentialFixture) {
	t.Helper()
	r, p, q := applicationProvisionFixture(t)
	root := filepath.Join(filepath.Dir(r.PolicyPath), "runtime-credentials")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	p.Provisioning.Proton = &ApplicationProtonPolicy{ShareIDs: []string{"share-id"}, CredentialRoot: root}
	q.CredentialSources = map[string]string{"fixture.auth": "pass://share-id/item-id/password"}
	f := &applicationCredentialFixture{}
	r.Credentials = f
	applicationPrerequisitesWrite(t, r.PolicyPath, p)
	return r, p, q, f
}

func TestApplicationProtonPlanMaterializeReplay(t *testing.T) {
	r, p, q, f := applicationProtonFixture(t)
	before := applicationPrerequisitesTree(t, filepath.Dir(r.PolicyPath))
	plan, err := r.PlanProvision(t.Context(), p.InstallerUID, q)
	if err != nil {
		t.Fatal(err)
	}
	if f.calls != 0 || !reflect.DeepEqual(before, applicationPrerequisitesTree(t, filepath.Dir(r.PolicyPath))) {
		t.Fatal("planning resolved a value or changed files")
	}
	if _, err = r.Provision(t.Context(), p.InstallerUID, plan); err != nil {
		t.Fatal(err)
	}
	g, err := r.resolveGrant(p, q.Owner)
	if err != nil {
		t.Fatal(err)
	}
	credential := g.Credentials["fixture.auth"]
	value, err := os.ReadFile(credential.Path)
	if err != nil || string(value) != "private-fixture-value\n" {
		t.Fatal("credential bytes changed")
	}
	info, _ := os.Lstat(credential.Path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("unsafe materialization")
	}
	identities, err := applicationCredentialIdentities(g, r.Store.OwnerUID)
	if err != nil {
		t.Fatal(err)
	}
	f.fail = true
	if _, err = r.Provision(t.Context(), p.InstallerUID, plan); err != nil || f.calls != 1 {
		t.Fatal("replay regenerated credential", err, f.calls)
	}
	after, err := applicationCredentialIdentities(g, r.Store.OwnerUID)
	if err != nil || !reflect.DeepEqual(identities, after) {
		t.Fatal("replay changed deployed inode")
	}
	facts, err := r.QueryPrerequisites(t.Context(), p.InstallerUID, ApplicationPrerequisiteQuery{SchemaVersion: ApplicationPrerequisiteSchema, Owner: q.Owner})
	if err != nil || facts.Credentials["fixture.auth"].Availability != "available" {
		t.Fatal("credential readiness", err)
	}
	for _, v := range []any{plan, facts, g, identities} {
		raw, _ := json.Marshal(v)
		if strings.Contains(string(raw), "private-fixture-value") {
			t.Fatal("secret in public or durable metadata")
		}
	}
	for _, next := range []map[string]string{nil, {"fixture.auth": "pass://share-id/other/password"}} {
		changed := q
		changed.CredentialSources = next
		if _, err = r.PlanProvision(t.Context(), p.InstallerUID, changed); err == nil || err.Error() != "application.credential.rotation_required" {
			t.Fatal("implicit credential rotation accepted", err)
		}
	}
	q.Data["extra"] = "fixture.extra"
	newPlan, err := r.PlanProvision(t.Context(), p.InstallerUID, q)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Provision(t.Context(), p.InstallerUID, newPlan); err != nil || f.calls != 1 {
		t.Fatal("data expansion changed credentials", err)
	}
}

func TestApplicationProtonFailureIsPrivateAndResumable(t *testing.T) {
	r, p, q, f := applicationProtonFixture(t)
	plan, err := r.PlanProvision(t.Context(), p.InstallerUID, q)
	if err != nil {
		t.Fatal(err)
	}
	f.fail = true
	if _, err = r.Provision(t.Context(), p.InstallerUID, plan); err == nil || err.Error() != "application.credential.resolve_failed" {
		t.Fatal("unredacted resolution error", err)
	}
	entries, _ := os.ReadDir(p.Provisioning.Proton.CredentialRoot)
	if len(entries) != 0 {
		t.Fatal("failed resolution published bytes")
	}
	f.fail = false
	if _, err = r.Provision(t.Context(), p.InstallerUID, plan); err != nil {
		t.Fatal("retry failed", err)
	}
	g, _ := r.resolveGrant(p, q.Owner)
	path := g.Credentials["fixture.auth"].Path
	if err := r.Store.write("installation-"+q.Owner.Instance(), ApplicationInstallation{Owner: q.Owner, Request: &ApplicationRuntimeRequest{CredentialRevisions: map[string]string{"fixture.auth": g.Credentials["fixture.auth"].Revision}}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	calls := f.calls
	if _, err = r.Provision(t.Context(), p.InstallerUID, plan); err == nil || err.Error() != "application.credential.recovery_required" || f.calls != calls {
		t.Fatal("lost deployed credential silently replaced", err)
	}
}

func TestApplicationProtonFiniteFailureCauses(t *testing.T) {
	for _, cause := range []string{"resolver_unavailable", "account_unavailable", "session_unavailable", "proton_failed", "source_invalid", "value_invalid", "proton_failed: private-fixture-value"} {
		t.Run(cause, func(t *testing.T) {
			r, p, q, f := applicationProtonFixture(t)
			plan, err := r.PlanProvision(t.Context(), p.InstallerUID, q)
			if err != nil {
				t.Fatal(err)
			}
			f.err = applicationError("credential." + cause)
			want := f.err.Error()
			if strings.Contains(cause, ":") {
				want = "application.credential.resolve_failed"
			}
			if _, err = r.Provision(t.Context(), p.InstallerUID, plan); err == nil || err.Error() != want {
				t.Fatalf("got %v, want %s", err, want)
			}
			entries, err := os.ReadDir(p.Provisioning.Proton.CredentialRoot)
			if err != nil || len(entries) != 0 {
				t.Fatal("failure published credential bytes", err)
			}
			f.err = nil
			if _, err = r.Provision(t.Context(), p.InstallerUID, plan); err != nil {
				t.Fatal("same-plan retry failed", err)
			}
		})
	}
}

func TestApplicationProtonAdmissionAndCustody(t *testing.T) {
	for _, name := range []string{"disabled", "share", "source", "resolver", "symlink", "hardlink", "mode", "directory"} {
		t.Run(name, func(t *testing.T) {
			r, p, q, f := applicationProtonFixture(t)
			switch name {
			case "disabled":
				p.Provisioning.Proton = nil
			case "share":
				q.CredentialSources["fixture.auth"] = "pass://other/item/password"
			case "source":
				q.CredentialSources["fixture.auth"] = "pass://share-id/item-id"
			case "resolver":
				r.Credentials = nil
			}
			applicationPrerequisitesWrite(t, r.PolicyPath, p)
			plan, err := r.PlanProvision(t.Context(), p.InstallerUID, q)
			if name == "disabled" || name == "share" || name == "source" || name == "resolver" {
				if err == nil || f.calls != 0 {
					t.Fatal("invalid source policy admitted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			g, _ := p.deriveGrant(q)
			path := g.Grant.Credentials["fixture.auth"].Path
			other := filepath.Join(filepath.Dir(path), "other")
			if err := os.WriteFile(other, []byte("do-not-replace"), 0600); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "symlink":
				err = os.Symlink(other, path)
			case "hardlink":
				err = os.Link(other, path)
			case "mode":
				err = os.WriteFile(path, []byte("unsafe"), 0644)
			case "directory":
				err = os.Mkdir(path, 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.Provision(t.Context(), p.InstallerUID, plan); err == nil || f.calls != 0 {
				t.Fatal("invalid existing target replaced", err)
			}
			bytes, _ := os.ReadFile(other)
			if string(bytes) != "do-not-replace" {
				t.Fatal("unrelated target changed")
			}
		})
	}
}

func TestApplicationCredentialPublicationNoOverwriteAndBound(t *testing.T) {
	r, p, _, _ := applicationProtonFixture(t)
	path := filepath.Join(p.Provisioning.Proton.CredentialRoot, "value")
	if err := applicationPublishCredential(t.Context(), path, []byte("old"), r.Store.OwnerUID); err != nil {
		t.Fatal(err)
	}
	if err := applicationPublishCredential(t.Context(), path, []byte("new"), r.Store.OwnerUID); err == nil {
		t.Fatal("existing credential overwritten")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "old" {
		t.Fatal("old credential lost")
	}
	var output applicationCredentialOutput
	if _, err := output.Write(make([]byte, 64*1024+2)); err == nil || len(output.data) != 0 {
		t.Fatal("unbounded secret output")
	}
}

func TestApplicationProtonEnvironmentDoesNotDependOnInteractiveShell(t *testing.T) {
	t.Setenv("PROTON_PASS_PERSONAL_ACCESS_TOKEN", "never-inherit")
	t.Setenv("PROTON_PASS_KEY_PROVIDER", "keyring")
	t.Setenv("PROTON_PASS_SESSION_DIR", "/wrong-session")
	_, _, q, _ := applicationProtonFixture(t)
	env := applicationProtonEnvironment(q.Owner)
	for _, required := range []string{"HOME=/home/agents", "PROTON_PASS_KEY_PROVIDER=fs", "PASS_LOG_LEVEL=warn", "XDG_DATA_HOME=/home/agents/.local/share"} {
		if !slices.Contains(env, required) {
			t.Fatal("missing noninteractive Proton setting", required)
		}
	}
	for _, entry := range env {
		if strings.Contains(entry, "never-inherit") || strings.HasPrefix(entry, "PROTON_PASS_SESSION_DIR=") || strings.Contains(entry, "keyring") {
			t.Fatal("inherited unrelated credential/session environment")
		}
	}
}
