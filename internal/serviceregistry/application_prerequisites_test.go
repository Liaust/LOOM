package serviceregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Fixture setup writes synthetic owner state directly. No effect adapter is
// installed: an accidental query-to-execution edge panics instead of succeeding.
func applicationPrerequisitesFixture(t *testing.T) (ApplicationRuntime, ApplicationPrerequisiteQuery, ApplicationHostPolicy) {
	t.Helper()
	root := applicationFixtureDir(t)
	d := applicationTestDescriptor()
	owner := ApplicationOwner{ProjectID: "project_01ARZ3NDEKTSV4RRFFQ69G5FAV", NodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV", Resource: "prerequisites"}
	p := ApplicationHostPolicy{SchemaVersion: "application.policy.v1", NodeID: owner.NodeID, Platform: d.Artifact.Platform, InstallerUID: 1234, PublisherUID: 0, Grants: []ApplicationGrant{{Owner: owner, RepositoryID: d.RepositoryID, PolicyRevision: "policy-a", LocationRevision: "location-a", MaxMemoryBytes: 9007199254740993, Data: map[string]ApplicationDataPolicy{}, Credentials: map[string]ApplicationCredentialPolicy{}}}}
	r := ApplicationRuntime{Store: ApplicationStateStore{Root: filepath.Join(root, "state"), OwnerUID: uint32(os.Geteuid())}, PolicyPath: filepath.Join(root, "policy.json")}
	applicationPrerequisitesWrite(t, r.PolicyPath, p)
	return r, ApplicationPrerequisiteQuery{SchemaVersion: ApplicationPrerequisiteSchema, Owner: owner}, p
}
func applicationPrerequisitesWrite(t *testing.T, path string, v any) {
	t.Helper()
	raw, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	if e = applicationAtomic(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
}
func applicationPrerequisitesTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	e := filepath.Walk(root, func(path string, info os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		contents := ""
		if info.Mode().IsRegular() {
			b, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			contents = applicationSHA(b)
		}
		out[path] = fmt.Sprintf("%v:%v:%s", info.Mode(), info.ModTime().UnixNano(), contents)
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func TestApplicationPrerequisitesAbsentAndReadOnly(t *testing.T) {
	r, q, _ := applicationPrerequisitesFixture(t)
	before := applicationPrerequisitesTree(t, filepath.Dir(r.PolicyPath))
	a, e := r.QueryPrerequisites(t.Context(), 1234, q)
	if e != nil {
		t.Fatal(e)
	}
	if a.Owner != q.Owner || a.Publication.Present || a.Installation.Present || len(a.Data) != 0 || len(a.Credentials) != 0 || a.Installation.Applied != nil || a.Observation != nil || a.CollectedAt.IsZero() || a.IdentityRevision == "" {
		t.Fatalf("missing facts misrepresented: %+v", a)
	}
	b, e := r.QueryPrerequisites(t.Context(), 1234, q)
	if e != nil || a.IdentityRevision != b.IdentityRevision {
		t.Fatalf("unstable identity: %v", e)
	}
	if !reflect.DeepEqual(before, applicationPrerequisitesTree(t, filepath.Dir(r.PolicyPath))) {
		t.Fatal("query mutated fixture")
	}
	if _, e = os.Lstat(r.Store.Root); !os.IsNotExist(e) {
		t.Fatal("query created state directory")
	}
}
func TestApplicationPrerequisitesScopedAdmission(t *testing.T) {
	for _, name := range []string{"publisher", "other_peer", "wrong_node", "invalid_schema", "invalid_owner"} {
		t.Run(name, func(t *testing.T) {
			r, q, _ := applicationPrerequisitesFixture(t)
			uid := uint32(1234)
			switch name {
			case "publisher":
				uid = 0
			case "other_peer":
				uid = 1235
			case "wrong_node":
				q.Owner.NodeID = "node_01ARZ3NDEKTSV4RRFFQ69G5FAW"
			case "invalid_schema":
				q.SchemaVersion = "other"
			case "invalid_owner":
				q.Owner.ProjectID = "bad"
			}
			a, e := r.QueryPrerequisites(t.Context(), uid, q)
			if e == nil || a.IdentityRevision != "" {
				t.Fatalf("accepted: %+v %v", a, e)
			}
		})
	}
}
func TestApplicationPrerequisitesBindingsAndPrivateProjection(t *testing.T) {
	r, q, p := applicationPrerequisitesFixture(t)
	root := filepath.Dir(r.PolicyPath)
	pool := filepath.Join(root, "private-pool")
	data := filepath.Join(pool, "private-data")
	if e := os.MkdirAll(data, 0700); e != nil {
		t.Fatal(e)
	}
	credential := filepath.Join(root, "private-credential-filename")
	if e := os.WriteFile(credential, []byte("SECRET-SENTINEL"), 0600); e != nil {
		t.Fatal(e)
	}
	p.Grants[0].Data = map[string]ApplicationDataPolicy{"first": {Path: data, Pool: pool, BindingRef: "binding:first"}, "second": {Path: filepath.Join(pool, "missing"), Pool: pool, BindingRef: "binding:second"}}
	p.Grants[0].Credentials = map[string]ApplicationCredentialPolicy{"credential.available": {Path: credential, Revision: "credential-a"}, "credential.missing": {Path: filepath.Join(root, "no-credential"), Revision: "credential-b"}}
	p.Grants[0].Edge = &ApplicationEdgePolicy{Owner: q.Owner, Hostname: "example.invalid", Revision: "edge-a"}
	applicationPrerequisitesWrite(t, r.PolicyPath, p)
	identity, e := applicationDataIdentityOwner(data, pool, uint32(os.Geteuid()), uint32(os.Getegid()))
	if e != nil {
		t.Fatal(e)
	}
	d := applicationTestDescriptor()
	pub := ApplicationPublication{Owner: q.Owner, Revision: "pub-a", PolicyRevision: "policy-a", LocationRevision: "location-a", GrantDigest: applicationSHA(p.Grants[0]), Descriptor: d}
	applicationPrerequisitesWrite(t, filepath.Join(r.Store.Root, "publication-"+q.Owner.Instance()+".json"), pub)
	inst := ApplicationInstallation{Owner: q.Owner, Revision: "install-a", Generation: "generation-a", Committed: true, Applied: true, UID: uint32(os.Geteuid()), Descriptor: d, Data: map[string]ApplicationDataIdentity{"first": identity}}
	applicationPrerequisitesWrite(t, filepath.Join(r.Store.Root, "installation-"+q.Owner.Instance()+".json"), inst)
	observed := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	startup := ApplicationStartupObservation{Owner: q.Owner, State: "observed", ObservedAt: observed, Current: &ApplicationCurrentObservation{ObservedAt: observed, State: "observed", Process: ApplicationProcessObservation{State: "active", Generation: "generation-a", Executable: "/private-executable"}, Readiness: applicationUnknownReadiness(), ManagementPresent: true}}
	applicationPrerequisitesWrite(t, filepath.Join(r.Store.Root, "startup-"+q.Owner.Instance()+".json"), startup)
	before := applicationPrerequisitesTree(t, root)
	a, e := r.QueryPrerequisites(t.Context(), 1234, q)
	if e != nil {
		t.Fatal(e)
	}
	if !a.Publication.Present || a.Publication.GrantMatches == nil || !*a.Publication.GrantMatches || !a.Installation.Present || a.Data["first"].Custody != "matches" || a.Data["first"].Availability != "available" || a.Data["second"].Availability != "missing" || a.Credentials["credential.available"].Availability != "available" || a.Credentials["credential.missing"].Availability != "missing" || a.ApprovedEdgeRef == nil {
		t.Fatalf("scoped facts: %+v", a)
	}
	if a.Observation == nil || !a.Observation.ObservedAt.Equal(observed) || !a.CollectedAt.After(observed) {
		t.Fatal("observation time fabricated")
	}
	raw, _ := json.Marshal(a)
	for _, s := range []string{root, "private-pool", "private-data", "private-credential-filename", "SECRET-SENTINEL", "/private-executable", "store_root", "launcher", "expected_revision", "max_memory_bytes"} {
		if strings.Contains(string(raw), s) {
			t.Fatalf("private data leaked: %s", s)
		}
	}
	if !reflect.DeepEqual(before, applicationPrerequisitesTree(t, root)) {
		t.Fatal("query mutated files/modes/times")
	}
	startup.Current.ObservedAt = observed.Add(time.Hour)
	applicationPrerequisitesWrite(t, filepath.Join(r.Store.Root, "startup-"+q.Owner.Instance()+".json"), startup)
	b, e := r.QueryPrerequisites(t.Context(), 1234, q)
	if e != nil || b.IdentityRevision != a.IdentityRevision || !b.Observation.ObservedAt.Equal(startup.Current.ObservedAt) {
		t.Fatal("observation refresh changed prerequisite identity")
	}
	inst.Data["first"] = ApplicationDataIdentity{Inode: 1}
	applicationPrerequisitesWrite(t, filepath.Join(r.Store.Root, "installation-"+q.Owner.Instance()+".json"), inst)
	b, e = r.QueryPrerequisites(t.Context(), 1234, q)
	if e != nil || b.Data["first"].Custody != "conflict" {
		t.Fatalf("custody substitution accepted: %+v %v", b, e)
	}
}
func TestApplicationPrerequisitesUnsafeMetadata(t *testing.T) {
	for _, kind := range []string{"policy_symlink", "state_symlink", "publication_symlink", "publication_mode", "credential_symlink", "credential_fifo", "credential_mode", "data_symlink"} {
		t.Run(kind, func(t *testing.T) {
			r, q, p := applicationPrerequisitesFixture(t)
			root := filepath.Dir(r.PolicyPath)
			target := filepath.Join(root, "target")
			if e := os.WriteFile(target, []byte("SECRET"), 0600); e != nil {
				t.Fatal(e)
			}
			path := filepath.Join(root, "unsafe")
			switch kind {
			case "policy_symlink":
				if e := os.Rename(r.PolicyPath, path); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(path, r.PolicyPath); e != nil {
					t.Fatal(e)
				}
			case "state_symlink":
				if e := os.Symlink(root, r.Store.Root); e != nil {
					t.Fatal(e)
				}
			case "publication_symlink", "publication_mode":
				if e := os.Mkdir(r.Store.Root, 0700); e != nil {
					t.Fatal(e)
				}
				path = filepath.Join(r.Store.Root, "publication-"+q.Owner.Instance()+".json")
				if kind == "publication_symlink" {
					if e := os.Symlink(target, path); e != nil {
						t.Fatal(e)
					}
				} else {
					if e := os.WriteFile(path, []byte(`{}`), 0666); e != nil {
						t.Fatal(e)
					}
					if e := os.Chmod(path, 0666); e != nil {
						t.Fatal(e)
					}
				}
			case "credential_symlink", "credential_fifo", "credential_mode":
				p.Grants[0].Credentials["credential.test"] = ApplicationCredentialPolicy{Path: path, Revision: "a"}
				if kind == "credential_symlink" {
					if e := os.Symlink(target, path); e != nil {
						t.Fatal(e)
					}
				} else if kind == "credential_fifo" {
					if e := applicationPrerequisitesFIFO(path); e != nil {
						t.Fatal(e)
					}
				} else {
					if e := os.WriteFile(path, []byte("SECRET"), 0644); e != nil {
						t.Fatal(e)
					}
				}
				applicationPrerequisitesWrite(t, r.PolicyPath, p)
			case "data_symlink":
				if e := os.Symlink(root, path); e != nil {
					t.Fatal(e)
				}
				p.Grants[0].Data["data"] = ApplicationDataPolicy{Path: path, Pool: root}
				applicationPrerequisitesWrite(t, r.PolicyPath, p)
			}
			a, e := r.QueryPrerequisites(t.Context(), 1234, q)
			if strings.HasPrefix(kind, "credential_") {
				if e != nil || a.Credentials["credential.test"].Availability != "unavailable" {
					t.Fatalf("unsafe credential. %+v %v", a, e)
				}
			} else if kind == "data_symlink" {
				if e != nil || a.Data["data"].Availability != "unavailable" {
					t.Fatalf("unsafe data: %+v %v", a, e)
				}
			} else if e == nil {
				t.Fatalf("unsafe record accepted: %+v", a)
			}
		})
	}
}
func TestApplicationPrerequisitesExactInteger(t *testing.T) {
	a := ApplicationPrerequisiteDataIdentity{Inode: 9007199254740993, PoolInode: 18446744073709551615}
	raw, e := json.Marshal(a)
	if e != nil {
		t.Fatal(e)
	}
	var b ApplicationPrerequisiteDataIdentity
	if e = applicationDecodeJSON(raw, &b); e != nil || a != b || !strings.Contains(string(raw), "18446744073709551615") {
		t.Fatalf("uint64 rounded: %s %v", raw, e)
	}
}
func TestApplicationPrerequisitesBoundedDrift(t *testing.T) {
	r, q, p := applicationPrerequisitesFixture(t)
	n := 0
	a, e := r.queryPrerequisites(t.Context(), 1234, q, func() {
		n++
		p.Grants[0].PolicyRevision = fmt.Sprintf("policy-%d", n)
		applicationPrerequisitesWrite(t, r.PolicyPath, p)
	})
	var uncertain *ApplicationPrerequisiteUncertainty
	if !errors.As(e, &uncertain) || a.IdentityRevision != "" || n != 3 {
		t.Fatalf("mixed/unbounded query: %+v %v attempts=%d", a, e, n)
	}
	n = 0
	a, e = r.queryPrerequisites(t.Context(), 1234, q, func() {
		n++
		if n == 1 {
			p.Grants[0].PolicyRevision = "stable"
			applicationPrerequisitesWrite(t, r.PolicyPath, p)
		}
	})
	if e != nil || a.PolicyRevision != "stable" || n != 2 {
		t.Fatalf("retry did not converge: %+v %v", a, e)
	}
	c, cancel := context.WithCancel(t.Context())
	cancel()
	if _, e = r.QueryPrerequisites(c, 1234, q); !errors.Is(e, context.Canceled) {
		t.Fatal("cancellation ignored")
	}
}

func applicationPrerequisitesFIFO(path string) error { return unix.Mkfifo(path, 0600) }

func TestApplicationPrerequisitesConcurrentPublication(t *testing.T) {
	r, q, p := applicationPrerequisitesFixture(t)
	pub := ApplicationPublication{Owner: q.Owner, Revision: "pub-0", PolicyRevision: p.Grants[0].PolicyRevision, LocationRevision: p.Grants[0].LocationRevision, GrantDigest: applicationSHA(p.Grants[0]), Descriptor: applicationTestDescriptor()}
	path := filepath.Join(r.Store.Root, "publication-"+q.Owner.Instance()+".json")
	applicationPrerequisitesWrite(t, path, pub)
	change := make(chan int)
	done := make(chan error)
	defer close(change)
	go func() {
		for n := range change {
			next := pub
			next.Revision = fmt.Sprintf("pub-%d", n)
			raw, e := json.Marshal(next)
			if e == nil {
				e = applicationAtomic(path, raw, 0600)
			}
			done <- e
		}
	}()
	attempts := 0
	out, e := r.queryPrerequisites(t.Context(), 1234, q, func() {
		attempts++
		change <- attempts
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	})
	var uncertain *ApplicationPrerequisiteUncertainty
	if !errors.As(e, &uncertain) || out.IdentityRevision != "" || attempts != 3 {
		t.Fatalf("mixed publication: %+v %v", out, e)
	}
	stable, e := r.QueryPrerequisites(t.Context(), 1234, q)
	if e != nil || stable.Publication.Revision != "pub-3" {
		t.Fatalf("coherent publication failed: %+v %v", stable, e)
	}
}
func TestApplicationPrerequisitesRevokedDuringCollection(t *testing.T) {
	for _, change := range []string{"peer", "owner"} {
		t.Run(change, func(t *testing.T) {
			r, q, p := applicationPrerequisitesFixture(t)
			a, e := r.queryPrerequisites(t.Context(), 1234, q, func() {
				if change == "peer" {
					p.InstallerUID = 1235
				} else {
					p.Grants = nil
				}
				applicationPrerequisitesWrite(t, r.PolicyPath, p)
			})
			if e == nil || a.IdentityRevision != "" {
				t.Fatalf("stale admission returned: %+v %v", a, e)
			}
		})
	}
}
func TestApplicationPrerequisitesCredentialIdentity(t *testing.T) {
	r, q, p := applicationPrerequisitesFixture(t)
	path := filepath.Join(filepath.Dir(r.PolicyPath), "credential")
	if e := os.WriteFile(path, []byte("private-one"), 0600); e != nil {
		t.Fatal(e)
	}
	p.Grants[0].Credentials["credential.test"] = ApplicationCredentialPolicy{Path: path, Revision: "a"}
	applicationPrerequisitesWrite(t, r.PolicyPath, p)
	a, e := r.QueryPrerequisites(t.Context(), 1234, q)
	if e != nil {
		t.Fatal(e)
	}
	// A consumer's access timestamp is not a new credential identity.
	var st unix.Stat_t
	if e = unix.Lstat(path, &st); e != nil {
		t.Fatal(e)
	}
	if e = unix.UtimesNano(path, []unix.Timespec{{Sec: st.Atim.Sec + 1, Nsec: st.Atim.Nsec}, st.Mtim}); e != nil {
		t.Fatal(e)
	}
	// utimes changes ctime too, so compare pure metadata projection after a read
	// instead: actual content access may update atime but never the queried bytes.
	if _, e = os.ReadFile(path); e != nil {
		t.Fatal(e)
	}
	b, e := r.QueryPrerequisites(t.Context(), 1234, q)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = os.ReadFile(path); e != nil {
		t.Fatal(e)
	}
	c, e := r.QueryPrerequisites(t.Context(), 1234, q)
	if e != nil || b.IdentityRevision != c.IdentityRevision {
		t.Fatal("access time churned stable identity")
	}
	if e = os.WriteFile(path, []byte("private-two"), 0600); e != nil {
		t.Fatal(e)
	}
	d, e := r.QueryPrerequisites(t.Context(), 1234, q)
	if e != nil || d.IdentityRevision == a.IdentityRevision {
		t.Fatal("same-revision credential rotation was missed")
	}
}

func TestApplicationPrerequisitesUnknownObservationProjection(t *testing.T) {
	r, q, _ := applicationPrerequisitesFixture(t)
	inst := ApplicationInstallation{Owner: q.Owner, Revision: "install-a", Generation: "generation-a", Descriptor: applicationTestDescriptor()}
	applicationPrerequisitesWrite(t, filepath.Join(r.Store.Root, "installation-"+q.Owner.Instance()+".json"), inst)
	startup := ApplicationStartupObservation{Owner: q.Owner, Current: &ApplicationCurrentObservation{ObservedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), State: "/private/state", Process: ApplicationProcessObservation{Generation: inst.Generation, State: "/private/process"}, Readiness: ApplicationReadiness{Protection: "/private/protection"}}}
	applicationPrerequisitesWrite(t, filepath.Join(r.Store.Root, "startup-"+q.Owner.Instance()+".json"), startup)
	a, e := r.QueryPrerequisites(t.Context(), 1234, q)
	if e != nil {
		t.Fatal(e)
	}
	if a.Observation == nil || a.Observation.State != "unknown" || a.Observation.ProcessState != "unknown" || a.Observation.Readiness.Process != "unknown" || a.Observation.Readiness.Protection != "unknown" {
		t.Fatalf("unknown/private observation not safely projected: %+v", a.Observation)
	}
}
