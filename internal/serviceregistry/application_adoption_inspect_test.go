package serviceregistry

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func applicationProjectStateReadOnlyFixture(t *testing.T, f *applicationProjectStateRuntimeFixture) applicationProjectStateRuntime {
	t.Helper()
	r := f.runtime
	r.systemctl = ""
	r.run = func(ctx context.Context, tool string, args ...string) ([]byte, error) {
		t.Helper()
		if tool != r.tools.Getfacl {
			t.Fatalf("inspection reached non-read tool %q", tool)
		}
		return f.runtime.run(ctx, tool, args...)
	}
	return r
}

func TestApplicationProjectStateInspectAbsentStoreReadOnly(t *testing.T) {
	f := newApplicationProjectStateRuntimeFixture(t)
	uid := applicationAccountUID(f.policy.ProjectState.Owner)
	r := applicationProjectStateReadOnlyFixture(t, f)
	before := applicationPrerequisitesTree(t, f.root)
	out, vector := r.inspect(t.Context(), f.policy, uid, ApplicationDataIdentity{})
	again, next := r.inspect(t.Context(), f.policy, uid, ApplicationDataIdentity{})
	if out.Availability != "available" || out.Custody != "unclaimed" || out.Path != "" ||
		out.Identity == nil || out.Identity.UID != f.policy.ProjectState.ProjectUID || out.Identity.UID == uid ||
		!applicationDigestPattern.MatchString(vector) || vector != next || !reflect.DeepEqual(out, again) {
		t.Fatalf("bad unclaimed inspection: %+v", out)
	}
	if _, err := os.Stat(r.store.Root); !os.IsNotExist(err) {
		t.Fatal("inspection created state store")
	}
	if f.showCalls != 0 || f.writes != 0 || len(f.acls) != 0 || !reflect.DeepEqual(before, applicationPrerequisitesTree(t, f.root)) {
		t.Fatal("unclaimed inspection had effects or read ACLs")
	}
	wrong, _ := r.inspect(t.Context(), f.policy, f.policy.ProjectState.ProjectUID, ApplicationDataIdentity{})
	if wrong.Availability != "unavailable" || wrong.Path != "" {
		t.Fatal("state-owner UID was accepted as dedicated application UID")
	}
}

func TestApplicationProjectStateInspectReadyAndPending(t *testing.T) {
	f := newApplicationProjectStateRuntimeFixture(t)
	uid := applicationAccountUID(f.policy.ProjectState.Owner)
	id, err := f.runtime.ensure(t.Context(), f.policy, uid, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := applicationProjectStateReadOnlyFixture(t, f)
	before := applicationPrerequisitesTree(t, f.root)
	writes, shows := f.writes, f.showCalls
	out, vector := r.inspect(t.Context(), f.policy, uid, id)
	again, next := r.inspect(t.Context(), f.policy, uid, id)
	if out.Availability != "available" || out.Custody != "matches" || out.Path != f.policy.Path ||
		out.Identity == nil || out.Identity.Inode != id.Inode || out.Identity.PoolInode != id.PoolInode ||
		vector != next || !reflect.DeepEqual(out, again) {
		t.Fatalf("ready adoption not recognized: %+v", out)
	}
	zero, _ := r.inspect(t.Context(), f.policy, uid, ApplicationDataIdentity{})
	if zero.Custody != "unclaimed" || zero.Path != "" {
		t.Fatal("path disclosed without retained installation identity")
	}
	wrong := id
	wrong.Inode++
	conflict, _ := r.inspect(t.Context(), f.policy, uid, wrong)
	if conflict.Custody != "conflict" || conflict.Path != "" {
		t.Fatal("conflicting prior identity matched")
	}
	if f.writes != writes || f.showCalls != shows || !reflect.DeepEqual(before, applicationPrerequisitesTree(t, f.root)) {
		t.Fatal("ready inspection changed state or queried service status")
	}
	key := applicationProjectStateJournalKey(*f.policy.ProjectState)
	var journal applicationProjectStateJournal
	if err = r.store.read(key, &journal); err != nil {
		t.Fatal(err)
	}
	journal.State = "pending"
	if err = r.store.write(key, journal); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.store.Root, key+".json")
	pending, _ := os.ReadFile(path)
	out, changed := r.inspect(t.Context(), f.policy, uid, id)
	after, _ := os.ReadFile(path)
	if out.Custody != "conflict" || out.Path != "" || changed == vector || !bytes.Equal(pending, after) || f.writes != writes || f.showCalls != shows {
		t.Fatal("inspection resumed or concealed pending adoption")
	}
}

func TestApplicationProjectStateInspectRefusesDrift(t *testing.T) {
	for _, kind := range []string{"acl", "path", "missing-journal", "corrupt-journal", "store-symlink"} {
		t.Run(kind, func(t *testing.T) {
			f := newApplicationProjectStateRuntimeFixture(t)
			uid := applicationAccountUID(f.policy.ProjectState.Owner)
			id, err := f.runtime.ensure(t.Context(), f.policy, uid, nil)
			if err != nil {
				t.Fatal(err)
			}
			key := applicationProjectStateJournalKey(*f.policy.ProjectState)
			journalPath := filepath.Join(f.runtime.store.Root, key+".json")
			switch kind {
			case "acl":
				acl, _ := applicationAdoptionParseACL(f.acls[*f.q.Expected], true)
				acl["user:1234567"] = 4
				f.acls[*f.q.Expected] = acl.text()
			case "path":
				if err = os.Rename(f.policy.Path, f.policy.Path+"-retained"); err != nil {
					t.Fatal(err)
				}
			case "missing-journal":
				if err = os.Remove(journalPath); err != nil {
					t.Fatal(err)
				}
			case "corrupt-journal":
				if err = os.WriteFile(journalPath, []byte("invalid json"), 0600); err != nil {
					t.Fatal(err)
				}
			case "store-symlink":
				original := f.runtime.store.Root + "-retained"
				if err = os.Rename(f.runtime.store.Root, original); err != nil {
					t.Fatal(err)
				}
				if err = os.Symlink(original, f.runtime.store.Root); err != nil {
					t.Fatal(err)
				}
			}
			r := applicationProjectStateReadOnlyFixture(t, f)
			writes, shows := f.writes, f.showCalls
			out, vector := r.inspect(t.Context(), f.policy, uid, id)
			if out.Custody == "matches" || out.Path != "" || !applicationDigestPattern.MatchString(vector) || f.writes != writes || f.showCalls != shows {
				t.Fatalf("unsafe/mutating inspection: %+v", out)
			}
		})
	}
}

// All effect methods are deliberately nil; any accidental collector execution
// panics. Only the optional read-only inspector is installed.
type applicationProjectStateInspectionSystem struct {
	ApplicationSystem
	runtime applicationProjectStateRuntime
	uid     uint32
}

func (s *applicationProjectStateInspectionSystem) inspectProjectState(ctx context.Context, p ApplicationDataPolicy, uid uint32, prior ApplicationDataIdentity) (ApplicationPrerequisiteData, string) {
	s.uid = uid
	return s.runtime.inspect(ctx, p, uid, prior)
}

func TestApplicationProjectStateInspectCollector(t *testing.T) {
	f := newApplicationProjectStateRuntimeFixture(t)
	r, query, policy := applicationPrerequisitesFixture(t)
	f.policy.ProjectState.Owner = query.Owner
	uid := applicationAccountUID(query.Owner)
	id, err := f.runtime.ensure(t.Context(), f.policy, uid, nil)
	if err != nil {
		t.Fatal(err)
	}
	policy.Grants[0].Data = map[string]ApplicationDataPolicy{"database": f.policy}
	applicationPrerequisitesWrite(t, r.PolicyPath, policy)
	applicationPrerequisitesWrite(t, filepath.Join(r.Store.Root, "installation-"+query.Owner.Instance()+".json"), ApplicationInstallation{
		Owner: query.Owner, Revision: "install-a", UID: uid, Data: map[string]ApplicationDataIdentity{"database": id},
	})
	system := &applicationProjectStateInspectionSystem{runtime: applicationProjectStateReadOnlyFixture(t, f)}
	r.System = system
	first, err := r.QueryPrerequisites(t.Context(), 1234, query)
	if err != nil || first.Data["database"].Custody != "matches" || first.Data["database"].Path != "" || first.Data["database"].Identity.Device != 0 || system.uid != uid {
		t.Fatalf("collector project-state branch failed: %+v %v", first.Data, err)
	}
	query.SourceData = "database"
	source, err := r.QueryPrerequisites(t.Context(), 1234, query)
	if err != nil || source.Data["database"].Path != f.policy.Path || source.Data["database"].Identity.UID != id.UID {
		t.Fatalf("matching source-data identity not exposed: %+v %v", source.Data, err)
	}
	// New runtime files are not rescanned into state policy or revision.
	if err = os.WriteFile(filepath.Join(f.policy.Path, "app.db-wal"), []byte("new sidecar"), 0600); err != nil {
		t.Fatal(err)
	}
	again, err := r.QueryPrerequisites(t.Context(), 1234, query)
	if err != nil || again.IdentityRevision != source.IdentityRevision {
		t.Fatalf("new WAL changed metadata revision: %v", err)
	}
}
