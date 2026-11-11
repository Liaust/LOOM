package serviceregistry

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func applicationAdoptionMaskedFixtureACL(owner, foreign uint32, directory bool) string {
	ownerPerm := "rw-"
	if directory {
		ownerPerm = "rwx"
	}
	raw := fmt.Sprintf("user::%s\nuser:%d:rwx\nuser:%d:rwx\ngroup::r-x\ngroup:986:rwx\nmask::---\nother::---\n", ownerPerm, owner, foreign)
	if directory {
		raw += fmt.Sprintf("default:user::rwx\ndefault:user:%d:rwx\ndefault:user:%d:rwx\ndefault:group::r-x\ndefault:group:986:rwx\ndefault:mask::rwx\ndefault:other::r-x\n", owner, foreign)
	}
	return raw
}

func TestApplicationStateAdoptionNormalizePrivateDefaults(t *testing.T) {
	for _, drift := range []bool{false, true} {
		t.Run(fmt.Sprint(drift), func(t *testing.T) {
			f := newApplicationAdoptionFixture(t)
			f.q.NormalizePrivateDefaults = true
			foreign := f.q.ProjectUID + 41
			f.acls[*f.q.Expected] = applicationAdoptionMaskedFixtureACL(f.q.ProjectUID, foreign, true)
			fileID := f.q.Files[0].Expected
			f.acls[fileID] = applicationAdoptionMaskedFixtureACL(f.q.ProjectUID, foreign, false)
			path := filepath.Join(f.root, "state", "app.db")
			payload, _ := os.ReadFile(path)
			record, err := f.adopter.Adopt(context.Background(), f.q, f.persist)
			if err != nil {
				t.Fatal(err)
			}
			for _, change := range record.Changes {
				if change.Role == "parent" {
					continue
				}
				before, _ := applicationAdoptionParseACL(change.Before, change.Identity.Directory)
				after, _ := applicationAdoptionParseACL(change.After, change.Identity.Directory)
				for _, uid := range []uint32{foreign, foreign + 1, f.q.ProjectUID} {
					for _, groups := range [][]uint32{nil, {986}, {change.Identity.GID}} {
						if before.access(uid, change.Identity.UID, groups, change.Identity.GID) != after.access(uid, change.Identity.UID, groups, change.Identity.GID) {
							t.Fatalf("effective access changed for non-app uid %d at %s", uid, change.Path)
						}
					}
				}
				if after[fmt.Sprintf("user:%d", foreign)] != 0 || after["group:986"] != 0 || after["group:"] != 0 {
					t.Fatal("dormant foreign permissions became effective")
				}
				want := uint8(6)
				if change.Identity.Directory {
					want = 7
				}
				if after.access(f.q.ApplicationUID, change.Identity.UID, nil, change.Identity.GID) != want {
					t.Fatal("application did not receive exact necessary access")
				}
				if change.Role == "state" {
					future := applicationAdoptionACL{}
					for key, perm := range after {
						if strings.HasPrefix(key, "default:") {
							future[strings.TrimPrefix(key, "default:")] = perm
						}
					}
					// Model a future app-owned regular file created mode 0660.
					// ACL inheritance also respects the application's create mode.
					future["user:"] &= 6
					future["mask:"] &= 6
					future["other:"] = 0
					if future.access(f.q.ProjectUID, f.q.ApplicationUID, nil, f.q.ProjectGID) != 6 ||
						future.access(f.q.ApplicationUID, f.q.ApplicationUID, nil, f.q.ProjectGID) != 6 ||
						future.access(foreign, f.q.ApplicationUID, []uint32{986}, f.q.ProjectGID) != 0 ||
						future["group:"] != 0 || future["other:"] != 0 {
						t.Fatal("future defaults are not private owner/application access")
					}
				}
			}
			if err = f.adopter.Check(context.Background(), record); err != nil {
				t.Fatal(err)
			}
			if drift {
				acl, _ := applicationAdoptionParseACL(f.acls[fileID], false)
				acl[fmt.Sprintf("user:%d", foreign+99)] = 4
				f.acls[fileID] = acl.text()
			}
			writes := f.writes
			err = f.adopter.Rollback(context.Background(), record)
			if drift {
				if err == nil || f.writes != writes {
					t.Fatal("rollback overwrote post-transition ACL drift")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				for _, change := range record.Changes {
					if f.acls[change.Identity] != change.Before {
						t.Fatal("exact original access/default ACL was not restored")
					}
				}
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(payload, after) || applicationAdoptionTestIdentity(t, path) != fileID {
				t.Fatal("transition changed data bytes or file ownership/identity")
			}
		})
	}
}

func TestApplicationStateAdoptionNormalizationRefusesLiveForeignWriter(t *testing.T) {
	f := newApplicationAdoptionFixture(t)
	f.q.NormalizePrivateDefaults = true
	foreign := f.q.ProjectUID + 41
	if err := os.Chmod(filepath.Join(f.root, "state", "app.db"), 0660); err != nil {
		t.Fatal(err)
	}
	f.acls[f.q.Files[0].Expected] = fmt.Sprintf("user::rw-\nuser:%d:rw-\ngroup::---\nmask::rw-\nother::---\n", foreign)
	if _, err := f.adopter.Adopt(context.Background(), f.q, f.persist); err == nil || !strings.Contains(err.Error(), "acl_foreign_writer") || f.writes != 0 {
		t.Fatalf("normalization accepted a currently effective foreign writer: %v", err)
	}
}

func TestApplicationStateAdoptionPreservesRegisteredRootOwnerACL(t *testing.T) {
	q := ApplicationStateAdoptionRequest{ProjectRoot: "/registered/project", Directory: ".data", ProjectUID: 1002, ProjectGID: 986, ApplicationUID: 400001,
		RootIdentity: ApplicationStatePathIdentity{UID: 990}, NormalizePrivateDefaults: true}
	id := ApplicationStatePathIdentity{Device: 1, Inode: 3, UID: 1002, GID: 985}
	h := applicationAdoptionHandle{path: "/registered/project/.data/db", stat: unix.Stat_t{Uid: id.UID, Gid: id.GID, Mode: unix.S_IFREG | 0660}}
	before := "user::rw-\nuser:990:rw-\ngroup::---\nmask::rw-\nother::---\n"
	if err := applicationAdoptionACLCustody(before, h, q); err != nil {
		t.Fatal(err)
	}
	after, err := applicationAdoptionDesiredACL(before, "file", id, q)
	if err != nil {
		t.Fatal(err)
	}
	acl, _ := applicationAdoptionParseACL(after, false)
	if acl.access(990, id.UID, nil, id.GID) != 6 {
		t.Fatal("existing authorized project root owner access was stripped")
	}
}

func TestApplicationProjectStateOpaqueBackupDirectory(t *testing.T) {
	f := newApplicationAdoptionFixture(t)
	backup := filepath.Join(f.root, "state", "schema-backups")
	if err := os.Mkdir(backup, 0700); err != nil {
		t.Fatal(err)
	}
	history := filepath.Join(backup, "original.sqlite3")
	if err := os.WriteFile(history, []byte("existing schema history"), 0600); err != nil {
		t.Fatal(err)
	}
	// Opaque contents are not walked or interpreted, even when they include a
	// link that would be forbidden as a directly managed state entry.
	if err := os.Symlink("/outside", filepath.Join(backup, "opaque-link")); err != nil {
		t.Fatal(err)
	}
	dirID, fileID := applicationAdoptionTestIdentity(t, backup), applicationAdoptionTestIdentity(t, history)
	p, err := ObserveApplicationProjectState(f.root, "state", applicationProjectStateTestOwner())
	if err != nil || len(p.Files) != 1 || p.NormalizePrivateDefaults {
		t.Fatalf("opaque history rejected, granted or opted into transition: %v", err)
	}
	record, err := f.adopter.Adopt(context.Background(), f.q, f.persist)
	if err != nil {
		t.Fatal(err)
	}
	if _, read := f.acls[dirID]; read {
		t.Fatal("opaque directory ACL was inspected")
	}
	if _, read := f.acls[fileID]; read {
		t.Fatal("opaque history ACL was inspected")
	}
	if err = f.adopter.Rollback(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(history)
	if string(raw) != "existing schema history" || applicationAdoptionTestIdentity(t, backup) != dirID || applicationAdoptionTestIdentity(t, history) != fileID {
		t.Fatal("opaque history changed")
	}
	for i := 0; i < 63; i++ {
		if err = os.WriteFile(filepath.Join(f.root, "state", fmt.Sprintf("file-%d", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if p, err = ObserveApplicationProjectState(f.root, "state", p.Owner); err != nil || len(p.Files) != 64 {
		t.Fatalf("private directory consumed the 64-regular-file allowance: %v", err)
	}
}

func TestApplicationProjectStateRuntimeUsesHostParentGroups(t *testing.T) {
	for _, approved := range []bool{false, true} {
		t.Run(fmt.Sprint(approved), func(t *testing.T) {
			f := newApplicationProjectStateRuntimeFixture(t)
			project := filepath.Join(f.root, "project")
			if err := os.Mkdir(project, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(filepath.Join(f.root, "state"), filepath.Join(project, "state")); err != nil {
				t.Fatal(err)
			}
			if err := os.Chown(f.root, -1, os.Getegid()); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(f.root, 0770); err != nil {
				t.Fatal(err)
			}
			p, err := ObserveApplicationProjectState(project, "state", applicationProjectStateTestOwner())
			if err != nil {
				t.Fatal(err)
			}
			p.NormalizePrivateDefaults = true
			f.policy.ProjectState, f.policy.Pool, f.policy.Path = &p, project, filepath.Join(project, "state")
			parentID := applicationAdoptionTestIdentity(t, f.root)
			// Box can expose project editors through a named group ACL while
			// its actual owning group differs from the registered root group.
			f.acls[parentID] = fmt.Sprintf("user::rwx\ngroup::r-x\ngroup:%d:rwx\nmask::rwx\nother::---\n", p.RootIdentity.GID)
			f.runtime.trustedParentOwners = map[string]uint32{f.root: parentID.UID}
			if approved {
				f.runtime.trustedParentGroups = map[string]uint32{f.root: parentID.GID}
			}
			q, err := f.runtime.request(p, f.q.ApplicationUID)
			if err != nil {
				t.Fatal(err)
			}
			if q.AllowedParentGroups[project] != p.RootIdentity.GID || !q.NormalizePrivateDefaults {
				t.Fatal("root group or explicit transition flag not threaded")
			}
			_, present := q.AllowedParentGroups[f.root]
			if present != approved || approved && q.AllowedParentGroups[f.root] != parentID.GID {
				t.Fatal("ancestor group was inferred from state rather than host policy")
			}
			_, err = f.runtime.ensure(context.Background(), f.policy, f.q.ApplicationUID, nil)
			if approved && err != nil {
				t.Fatal(err)
			}
			if !approved && (err == nil || f.writes != 0) {
				t.Fatal("unknown writable parent was accepted")
			}
		})
	}
}
