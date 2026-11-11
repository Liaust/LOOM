package serviceregistry

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func applicationProjectStateTestOwner() ApplicationOwner {
	return ApplicationOwner{ProjectID: "project_01ARZ3NDEKTSV4RRFFQ69G5FAV", NodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV", Resource: "state"}
}

func TestObserveApplicationProjectState(t *testing.T) {
	f := newApplicationAdoptionFixture(t)
	if err := os.Rename(filepath.Join(f.root, "state"), filepath.Join(f.root, ".data")); err != nil {
		t.Fatal(err)
	}
	p, err := ObserveApplicationProjectState(f.root, ".data", applicationProjectStateTestOwner())
	if err != nil {
		t.Fatal(err)
	}
	if p.Owner != applicationProjectStateTestOwner() || p.ProjectUID != f.q.ProjectUID || p.ProjectGID != f.q.ProjectGID ||
		p.RootIdentity != f.q.RootIdentity || p.DirectoryIdentity != *f.q.Expected ||
		len(p.Files) != 1 || p.Files[0] != f.q.Files[0] || f.writes != 0 || len(f.journal) != 0 {
		t.Fatalf("unexpected observation: %+v", p)
	}
	// A private file may retain a different group from its state directory.
	other := *f.q.Expected
	q := f.q
	q.RootIdentity.UID++
	q.AllowedParentOwners = map[string]uint32{f.root: q.RootIdentity.UID}
	q.Files = append([]ApplicationStateAdoptionFile(nil), q.Files...)
	q.Files[0].Expected.GID = other.GID + 1
	if err = applicationAdoptionValidate(q); err != nil {
		t.Fatalf("different root owner/file group refused by request shape: %v", err)
	}
	if err = os.Mkdir(filepath.Join(f.root, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	if empty, err := ObserveApplicationProjectState(f.root, "empty", p.Owner); err != nil || len(empty.Files) != 0 {
		t.Fatalf("existing empty directory not observed: %v", err)
	}
}

func TestObserveApplicationProjectStateRefusals(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "directory", "metadata", "limit", "root-symlink", "missing"} {
		t.Run(kind, func(t *testing.T) {
			f := newApplicationAdoptionFixture(t)
			root, relative := f.root, "state"
			switch kind {
			case "symlink":
				if err := os.Symlink("app.db", filepath.Join(root, "state", ".link")); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(filepath.Join(root, "state", "app.db"), filepath.Join(root, "state", "alias.db")); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(filepath.Join(root, "state", "nested"), 0755); err != nil {
					t.Fatal(err)
				}
			case "metadata":
				if err := os.WriteFile(filepath.Join(root, "state", ".git"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "limit":
				for i := 0; i < 64; i++ {
					if err := os.WriteFile(filepath.Join(root, "state", fmt.Sprintf("part-%d", i)), nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "root-symlink":
				root += "-alias"
				if err := os.Symlink(f.root, root); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Remove(root) })
			case "missing":
				relative = "missing"
			}
			if _, err := ObserveApplicationProjectState(root, relative, applicationProjectStateTestOwner()); err == nil || f.writes != 0 {
				t.Fatal("unsafe observation accepted")
			}
		})
	}
}

func TestApplicationProjectStateTrustedSharedParent(t *testing.T) {
	for _, approved := range []bool{false, true} {
		t.Run(fmt.Sprint(approved), func(t *testing.T) {
			f := newApplicationAdoptionFixture(t)
			// Use a group this non-root fixture belongs to so the kernel does
			// not clear setgid during the ACL model's mode-bit update.
			if err := os.Chown(f.root, -1, os.Getegid()); err != nil {
				t.Fatal(err)
			}
			f.q.RootIdentity = applicationAdoptionTestIdentity(t, f.root)
			if err := os.Chmod(f.root, 0770|os.ModeSetgid); err != nil {
				t.Fatal(err)
			}
			f.q.AllowedParentOwners = map[string]uint32{f.root: f.q.RootIdentity.UID}
			if approved {
				f.q.AllowedParentGroups = map[string]uint32{f.root: f.q.RootIdentity.GID}
			}
			r, err := f.adopter.Adopt(context.Background(), f.q, f.persist)
			if !approved {
				if err == nil || f.writes != 0 {
					t.Fatal("unapproved shared/setgid parent accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range r.Changes {
				if c.Path != f.root {
					continue
				}
				acl, e := applicationAdoptionParseACL(c.After, true)
				if e != nil || acl[fmt.Sprintf("user:%d", f.q.ApplicationUID)] != 1 || acl["group:"] != 7 {
					t.Fatal("shared parent rights changed or application gained listing/write")
				}
			}
			if err = f.adopter.Rollback(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(f.root)
			if err != nil || info.Mode().Perm() != 0770 || info.Mode()&os.ModeSetgid == 0 {
				t.Fatal("parent setgid/mode not preserved")
			}
		})
	}
}

func TestApplicationProjectStateFileGroupRetention(t *testing.T) {
	f := newApplicationAdoptionFixture(t)
	if f.q.ProjectGID == uint32(os.Getegid()) {
		t.Skip("fixture has no different permitted primary group")
	}
	path := filepath.Join(f.root, "state", "app.db")
	if err := os.Chown(path, -1, os.Getegid()); err != nil {
		t.Fatal(err)
	}
	f.q.Files[0].Expected = applicationAdoptionTestIdentity(t, path)
	p, err := ObserveApplicationProjectState(f.root, "state", applicationProjectStateTestOwner())
	if err != nil || p.Files[0].Expected.GID == p.ProjectGID {
		t.Fatalf("different file group not retained in observation: %v", err)
	}
	before := f.q.Files[0].Expected
	record, err := f.adopter.Adopt(context.Background(), f.q, f.persist)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.adopter.Rollback(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if applicationAdoptionTestIdentity(t, path) != before {
		t.Fatal("adoption/rollback changed existing file ownership")
	}
}

const applicationProjectStateStoppedFixture = "LoadState=loaded\nActiveState=inactive\nSubState=dead\nJob=\nMainPID=0\nControlPID=0\n"

func TestApplicationProjectStateQuiescence(t *testing.T) {
	if !applicationProjectStateStopped([]byte(applicationProjectStateStoppedFixture)) ||
		!applicationProjectStateStopped([]byte(strings.Replace(applicationProjectStateStoppedFixture, "LoadState=loaded", "LoadState=not-found", 1))) {
		t.Fatal("stopped/absent service refused")
	}
	for _, raw := range []string{
		"", strings.Replace(applicationProjectStateStoppedFixture, "inactive", "active", 1),
		strings.Replace(applicationProjectStateStoppedFixture, "MainPID=0", "MainPID=1", 1),
		strings.Replace(applicationProjectStateStoppedFixture, "ControlPID=0", "ControlPID=8", 1),
		strings.Replace(applicationProjectStateStoppedFixture, "Job=", "Job=42", 1),
		strings.Replace(applicationProjectStateStoppedFixture, "SubState=dead\n", "", 1),
		applicationProjectStateStoppedFixture + "MainPID=0\n",
	} {
		if applicationProjectStateStopped([]byte(raw)) {
			t.Fatalf("unquiesced/unknown service accepted: %q", raw)
		}
	}
}

func TestApplicationProjectStatePrivateStateRejectsInheritedWriters(t *testing.T) {
	f := newApplicationAdoptionFixture(t)
	// Main's mode-0700 state retains dormant inherited access entries and
	// live writable defaults. Neither may become a broad application grant.
	f.acls[*f.q.Expected] = "user::rwx\nuser:1001:rwx\ngroup::r-x\ngroup:986:rwx\nmask::---\nother::---\n" +
		"default:user::rwx\ndefault:user:1001:rwx\ndefault:group::r-x\ndefault:group:986:rwx\ndefault:mask::rwx\ndefault:other::r-x\n"
	if _, err := f.adopter.Adopt(context.Background(), f.q, f.persist); err == nil || !strings.Contains(err.Error(), "acl_foreign_writer") || f.writes != 0 {
		t.Fatalf("inherited state writers were broadened or silently scrubbed: %v", err)
	}
}

type applicationProjectStateRuntimeFixture struct {
	*applicationAdoptionFixture
	runtime   applicationProjectStateRuntime
	policy    ApplicationDataPolicy
	status    string
	showCalls int
}

func newApplicationProjectStateRuntimeFixture(t *testing.T) *applicationProjectStateRuntimeFixture {
	t.Helper()
	base := newApplicationAdoptionFixture(t)
	p, err := ObserveApplicationProjectState(base.root, "state", applicationProjectStateTestOwner())
	if err != nil {
		t.Fatal(err)
	}
	f := &applicationProjectStateRuntimeFixture{applicationAdoptionFixture: base, status: applicationProjectStateStoppedFixture}
	f.policy = ApplicationDataPolicy{StorageClass: ApplicationProjectStateStorageClass, ProjectState: &p, Pool: p.ProjectRoot, Path: filepath.Join(p.ProjectRoot, p.RelativePath)}
	f.runtime = applicationProjectStateRuntime{
		store: ApplicationStateStore{Root: filepath.Join(base.root, ".host-state"), OwnerUID: uint32(os.Geteuid())},
		tools: ApplicationStateACLTools{"/trusted/getfacl", "/trusted/setfacl"}, systemctl: "/trusted/systemctl",
	}
	f.runtime.run = func(ctx context.Context, command string, args ...string) ([]byte, error) {
		current := *f.policy.ProjectState
		if command == "/trusted/systemctl" {
			want := []string{"show", current.Owner.Unit(), "--property=LoadState,ActiveState,SubState,MainPID,ControlPID,Job"}
			if fmt.Sprint(args) != fmt.Sprint(want) {
				t.Fatalf("unexpected status command: %q", args)
			}
			f.showCalls++
			return []byte(f.status), nil
		}
		if command == "/trusted/setfacl" {
			var j applicationProjectStateJournal
			if err := f.runtime.store.read(applicationProjectStateJournalKey(current), &j); err != nil || j.State != "pending" || len(j.Adoption.Changes) == 0 {
				t.Fatalf("ACL effect without durable original intent: %v", err)
			}
			base.journal = append(base.journal, j.Adoption)
		}
		return base.run(ctx, command, args...)
	}
	return f
}

func TestApplicationProjectStateRuntimeFrozenJournal(t *testing.T) {
	f := newApplicationProjectStateRuntimeFixture(t)
	ctx := context.Background()
	id, err := f.runtime.ensure(ctx, f.policy, f.q.ApplicationUID, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := applicationProjectStateJournalKey(*f.policy.ProjectState)
	journalPath := filepath.Join(f.runtime.store.Root, key+".json")
	before, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	var j applicationProjectStateJournal
	if err = f.runtime.store.read(key, &j); err != nil || j.State != "ready" {
		t.Fatalf("journal not ready: %v", err)
	}
	frozen, writes, shows := applicationSHA(f.policy), f.writes, f.showCalls
	if err = os.WriteFile(filepath.Join(f.root, "state", "app.db-wal"), []byte("new runtime sidecar"), 0600); err != nil {
		t.Fatal(err)
	}
	// Existing successful adoption is read-only even while its service runs.
	f.status = strings.Replace(f.status, "inactive", "active", 1)
	for i := 0; i < 2; i++ {
		now, e := f.runtime.ensure(ctx, f.policy, f.q.ApplicationUID, &id)
		if e != nil || !applicationSameDataIdentity(id, now) {
			t.Fatalf("revalidation failed: %v", e)
		}
	}
	after, err := os.ReadFile(journalPath)
	if err != nil || !bytes.Equal(before, after) || f.writes != writes || f.showCalls != shows || applicationSHA(f.policy) != frozen {
		t.Fatal("runtime WAL changed frozen policy, journal or ACLs")
	}
	if _, err = os.Stat(filepath.Join(f.runtime.store.Root, "adoptions")); !os.IsNotExist(err) {
		t.Fatal("unexpected adoptions subdirectory")
	}
}

func TestApplicationProjectStateRuntimeFailureFences(t *testing.T) {
	for _, kind := range []string{"live-service", "prior-conflict", "path-conflict", "missing-journal", "partial-adoption", "completed-pending", "policy-conflict"} {
		t.Run(kind, func(t *testing.T) {
			f := newApplicationProjectStateRuntimeFixture(t)
			ctx := context.Background()
			var prior *ApplicationDataIdentity
			if kind == "missing-journal" || kind == "completed-pending" || kind == "policy-conflict" {
				id, err := f.runtime.ensure(ctx, f.policy, f.q.ApplicationUID, nil)
				if err != nil {
					t.Fatal(err)
				}
				prior = &id
				key := applicationProjectStateJournalKey(*f.policy.ProjectState)
				switch kind {
				case "missing-journal":
					if err = os.Remove(filepath.Join(f.runtime.store.Root, key+".json")); err != nil {
						t.Fatal(err)
					}
				case "completed-pending":
					var j applicationProjectStateJournal
					if err = f.runtime.store.read(key, &j); err != nil {
						t.Fatal(err)
					}
					j.State = "pending"
					if err = f.runtime.store.write(key, j); err != nil {
						t.Fatal(err)
					}
				case "policy-conflict":
					f.policy.ProjectState.Owner.Resource = "other"
				}
			}
			switch kind {
			case "live-service":
				f.status = strings.Replace(f.status, "inactive", "active", 1)
			case "prior-conflict":
				prior = &ApplicationDataIdentity{Inode: 42}
			case "path-conflict":
				f.policy.Path += "-other"
			case "partial-adoption":
				f.failAt = 2
			}
			writes := f.writes
			_, err := f.runtime.ensure(ctx, f.policy, f.q.ApplicationUID, prior)
			if kind == "completed-pending" {
				if err != nil || f.writes != writes {
					t.Fatalf("completed intent did not reconcile read-only: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("unsafe runtime ensure accepted")
			}
			if kind != "partial-adoption" && f.writes != writes {
				t.Fatal("refusal performed ACL effect")
			}
			if kind == "partial-adoption" {
				key := applicationProjectStateJournalKey(*f.policy.ProjectState)
				before, e := os.ReadFile(filepath.Join(f.runtime.store.Root, key+".json"))
				if e != nil {
					t.Fatal(e)
				}
				f.failAt, writes = 0, f.writes
				if _, e = f.runtime.ensure(ctx, f.policy, f.q.ApplicationUID, nil); e == nil || !strings.Contains(e.Error(), "adoption_recovery_required") {
					t.Fatalf("partial adoption was rebaselined: %v", e)
				}
				after, _ := os.ReadFile(filepath.Join(f.runtime.store.Root, key+".json"))
				if f.writes != writes || !bytes.Equal(before, after) {
					t.Fatal("recovery overwrote original before ACL")
				}
			}
		})
	}
}
