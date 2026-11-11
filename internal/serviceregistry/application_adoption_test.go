package serviceregistry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// This runner models POSIX ACLs and their mode-bit mask on owned temporary
// files only. It never invokes ACL tools, root commands or a real service.
type applicationAdoptionFixture struct {
	t        *testing.T
	root     string
	q        ApplicationStateAdoptionRequest
	adopter  *ApplicationStateAdopter
	acls     map[ApplicationStatePathIdentity]string
	writes   int
	failAt   int
	onRead   func()
	onWrite  func()
	journal  []ApplicationStateAdoptionRecord
	fenceErr error
}

func applicationAdoptionTestIdentity(t *testing.T, path string) ApplicationStatePathIdentity {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		t.Fatal(err)
	}
	return (applicationAdoptionHandle{stat: st}).identity()
}

func newApplicationAdoptionFixture(t *testing.T) *applicationAdoptionFixture {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("non-root fixture; no root filesystem effects")
	}
	// Avoid macOS's user-private temp ancestors: all mutation stays in this
	// owned fixture and /tmp's normal traverse permission needs no grant.
	root, err := os.MkdirTemp("/tmp", "loom-adoption-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(root, "state"), 0700); err != nil {
		t.Fatal(err)
	}
	payload := []byte("existing database\x00record-id=42\nreceipt=keep\xff")
	if err = os.WriteFile(filepath.Join(root, "state", "app.db"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	f := &applicationAdoptionFixture{t: t, root: root, acls: map[ApplicationStatePathIdentity]string{}}
	dir := applicationAdoptionTestIdentity(t, filepath.Join(root, "state"))
	file := applicationAdoptionTestIdentity(t, filepath.Join(root, "state", "app.db"))
	f.q = ApplicationStateAdoptionRequest{
		ProjectRoot: root, Directory: "state", ProjectUID: uint32(os.Geteuid()), ProjectGID: dir.GID,
		ApplicationUID: uint32(os.Geteuid()) + 100000, Mode: ApplicationStateAdopt, Expected: &dir,
		Files:        []ApplicationStateAdoptionFile{{Name: "app.db", Expected: file}},
		RootIdentity: applicationAdoptionTestIdentity(t, root),
	}
	f.adopter, err = NewApplicationStateAdopter(ApplicationStateACLTools{"/trusted/getfacl", "/trusted/setfacl"}, f.run, func(context.Context) error { return f.fenceErr })
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *applicationAdoptionFixture) persist(r ApplicationStateAdoptionRecord) error {
	// Model durable serialization rather than retaining aliased slices.
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	var copy ApplicationStateAdoptionRecord
	if err = json.Unmarshal(raw, &copy); err == nil {
		f.journal = append(f.journal, copy)
	}
	return err
}

func (f *applicationAdoptionFixture) run(_ context.Context, command string, args ...string) ([]byte, error) {
	f.t.Helper()
	if len(args) < 3 || args[len(args)-2] != "--" {
		f.t.Fatalf("missing option boundary: %q", args)
	}
	path := args[len(args)-1]
	prefix := fmt.Sprintf("/proc/%d/fd/", os.Getpid())
	if !strings.HasPrefix(path, prefix) {
		f.t.Fatalf("non-pinned ACL target %q", path)
	}
	fd, err := strconv.Atoi(strings.TrimPrefix(path, prefix))
	if err != nil {
		f.t.Fatal(err)
	}
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		f.t.Fatal(err)
	}
	id := (applicationAdoptionHandle{stat: st}).identity()
	acl, ok := f.acls[id]
	if !ok {
		mode := uint32(st.Mode)
		acl = fmt.Sprintf("user::%s\ngroup::%s\nother::%s\n", applicationAdoptionPerm(uint8(mode>>6)&7), applicationAdoptionPerm(uint8(mode>>3)&7), applicationAdoptionPerm(uint8(mode)&7))
		parsed, e := applicationAdoptionParseACL(acl, id.Directory)
		if e != nil {
			f.t.Fatal(e)
		}
		acl = parsed.text()
		f.acls[id] = acl
	}
	switch command {
	case "/trusted/getfacl":
		if !reflect.DeepEqual(args[:len(args)-1], []string{"--omit-header", "--numeric", "--no-effective", "--absolute-names", "--"}) {
			f.t.Fatalf("unexpected getfacl arguments: %q", args)
		}
		if f.onRead != nil {
			hook := f.onRead
			f.onRead = nil
			hook()
		}
		return []byte(acl), nil
	case "/trusted/setfacl":
		if len(f.journal) == 0 {
			f.t.Fatal("ACL effect before journal intent")
		}
		i := 1
		if len(args) < 4 || args[0] != "--no-mask" {
			f.t.Fatalf("unexpected setfacl arguments: %q", args)
		}
		if id.Directory {
			if args[i] != "--remove-default" {
				f.t.Fatal("default ACL not explicitly replaced")
			}
			i++
		}
		if len(args) != i+3 || !strings.HasPrefix(args[i], "--set=") {
			f.t.Fatalf("unexpected setfacl arguments: %q", args)
		}
		f.writes++
		if f.writes == f.failAt {
			return nil, errors.New("unsupported ACL backend")
		}
		next, e := applicationAdoptionParseACL(strings.ReplaceAll(strings.TrimPrefix(args[i], "--set="), ",", "\n"), id.Directory)
		if e != nil {
			f.t.Fatal(e)
		}
		// Refuse even a simulated mutation outside this fixture's subtree.
		contained := false
		filepath.WalkDir(f.root, func(p string, d os.DirEntry, e error) error {
			if e == nil && applicationAdoptionTestIdentity(f.t, p) == id {
				contained = true
			}
			return nil
		})
		if !contained {
			f.t.Fatal("attempted mutation outside owned fixture")
		}
		if e = unix.Fchmod(fd, next.mode()|uint32(st.Mode)&07000); e != nil {
			f.t.Fatal(e)
		}
		f.acls[id] = next.text()
		if f.onWrite != nil {
			hook := f.onWrite
			f.onWrite = nil
			hook()
		}
		return nil, nil
	default:
		f.t.Fatalf("unexpected executable %q", command)
	}
	return nil, nil
}

func TestApplicationStateAdoptionRetentionAndRollback(t *testing.T) {
	f := newApplicationAdoptionFixture(t)
	path := filepath.Join(f.root, "state", "app.db")
	before, _ := os.ReadFile(path)
	statBefore, _ := os.Stat(path)
	r, err := f.adopter.Adopt(context.Background(), f.q, f.persist)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.journal) != 1 || r.Created || r.StorageClass != "project_state" || r.Identity.UID != f.q.ProjectUID || r.Identity.GID != f.q.ProjectGID || r.Identity.Inode != f.q.Expected.Inode || r.Identity.Device != f.q.Expected.Device || r.Identity.PoolInode != applicationAdoptionTestIdentity(t, f.root).Inode || r.Identity.Mount != f.root {
		t.Fatalf("bad composed identity/journal: %+v", r)
	}
	if err = f.adopter.Check(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	statAfter, _ := os.Stat(path)
	if !bytes.Equal(before, data) || !statBefore.ModTime().Equal(statAfter.ModTime()) || applicationAdoptionTestIdentity(t, path) != f.q.Files[0].Expected {
		t.Fatal("existing data, mtime or ownership changed")
	}
	for _, c := range r.Changes {
		acl, e := applicationAdoptionParseACL(c.After, c.Identity.Directory)
		if e != nil {
			t.Fatal(e)
		}
		if c.Role == "state" {
			if acl[fmt.Sprintf("default:user:%d", f.q.ProjectUID)] != 7 || acl[fmt.Sprintf("default:user:%d", f.q.ApplicationUID)] != 7 || acl["default:group:"] != 0 || acl["default:other:"] != 0 {
				t.Fatal("default ACL does not retain private owner/app access")
			}
		} else if c.Role == "parent" && c.Before != c.After {
			if acl[fmt.Sprintf("user:%d", f.q.ApplicationUID)] != 1 || c.Path != f.root {
				t.Fatal("parent grant exceeded necessary traversal")
			}
		}
	}
	if err = f.adopter.Rollback(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	writes := f.writes
	if err = f.adopter.Rollback(context.Background(), r); err != nil || f.writes != writes {
		t.Fatalf("rollback is not retry-safe: %v", err)
	}
	for _, c := range r.Changes {
		if f.acls[c.Identity] != c.Before {
			t.Fatalf("ACL not restored at %s", c.Path)
		}
	}
	data, _ = os.ReadFile(path)
	if !bytes.Equal(before, data) {
		t.Fatal("rollback changed database bytes")
	}
}

func TestApplicationStateAdoptionRefusesPaths(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../state", "/state", "state/../state", "state//other", "state/", ".git", ".loom", ".project", ".codex", "data/.git", ".data/.project", ".data/repo", ".data/.agents", ".data/tools", ".data/.Git", "src", "source/db", "worktrees/db", "state\\other"} {
		t.Run("relative="+name, func(t *testing.T) {
			f := newApplicationAdoptionFixture(t)
			f.q.Directory = name
			if _, err := f.adopter.Adopt(context.Background(), f.q, f.persist); err == nil || f.writes != 0 {
				t.Fatalf("unsafe path accepted: %q (%v)", name, err)
			}
		})
	}
	for _, test := range []struct {
		name   string
		change func(*testing.T, *applicationAdoptionFixture)
	}{
		{"root-symlink", func(t *testing.T, f *applicationAdoptionFixture) {
			alias := f.root + "-alias"
			if err := os.Symlink(f.root, alias); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Remove(alias) })
			f.q.ProjectRoot = alias
		}},
		{"state-symlink", func(t *testing.T, f *applicationAdoptionFixture) {
			if err := os.Rename(filepath.Join(f.root, "state"), filepath.Join(f.root, "original")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("original", filepath.Join(f.root, "state")); err != nil {
				t.Fatal(err)
			}
		}},
		{"ancestor-symlink", func(t *testing.T, f *applicationAdoptionFixture) {
			if err := os.Symlink(".", filepath.Join(f.root, "alias")); err != nil {
				t.Fatal(err)
			}
			f.q.Directory = "alias/state"
		}},
		{"file-symlink", func(t *testing.T, f *applicationAdoptionFixture) {
			if err := os.Symlink("app.db", filepath.Join(f.root, "state", "link")); err != nil {
				t.Fatal(err)
			}
			f.q.Files[0].Name = "link"
		}},
		{"file-hardlink", func(t *testing.T, f *applicationAdoptionFixture) {
			if err := os.Link(filepath.Join(f.root, "state", "app.db"), filepath.Join(f.root, "copy")); err != nil {
				t.Fatal(err)
			}
		}},
		{"non-directory", func(t *testing.T, f *applicationAdoptionFixture) { f.q.Directory = "state/app.db" }},
		{"foreign-owner", func(t *testing.T, f *applicationAdoptionFixture) {
			f.q.ProjectUID++
			id := *f.q.Expected
			id.UID = f.q.ProjectUID
			f.q.Expected = &id
			f.q.Files = nil
		}},
		{"wrong-inode", func(t *testing.T, f *applicationAdoptionFixture) { f.q.Expected.Inode++ }},
		{"wrong-device", func(t *testing.T, f *applicationAdoptionFixture) { f.q.Expected.Device++ }},
		{"nested-project", func(t *testing.T, f *applicationAdoptionFixture) {
			if err := os.Mkdir(filepath.Join(f.root, "state", ".loom"), 0700); err != nil {
				t.Fatal(err)
			}
		}},
		{"world-writable", func(t *testing.T, f *applicationAdoptionFixture) {
			if err := os.Chmod(filepath.Join(f.root, "state"), 0702); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing-state", func(t *testing.T, f *applicationAdoptionFixture) { f.q.Directory = "absent" }},
		{"missing-file", func(t *testing.T, f *applicationAdoptionFixture) { f.q.Files[0].Name = "absent.db" }},
		{"file-escape", func(t *testing.T, f *applicationAdoptionFixture) { f.q.Files[0].Name = "../copy" }},
		{"no-mode", func(t *testing.T, f *applicationAdoptionFixture) { f.q.Mode = "" }},
		{"no-identity", func(t *testing.T, f *applicationAdoptionFixture) { f.q.Expected = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newApplicationAdoptionFixture(t)
			test.change(t, f)
			if _, err := f.adopter.Adopt(context.Background(), f.q, f.persist); err == nil || f.writes != 0 {
				t.Fatalf("unsafe adoption succeeded/effected: %v, writes=%d", err, f.writes)
			}
		})
	}
}

func TestApplicationStateAdoptionHiddenState(t *testing.T) {
	f := newApplicationAdoptionFixture(t)
	if err := os.Rename(filepath.Join(f.root, "state"), filepath.Join(f.root, ".data")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(f.root, ".data", "app.db"), filepath.Join(f.root, ".data", ".app.db")); err != nil {
		t.Fatal(err)
	}
	f.q.Directory, f.q.Files[0].Name = ".data", ".app.db"
	r, err := f.adopter.Adopt(context.Background(), f.q, f.persist)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.adopter.Check(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if err = f.adopter.Rollback(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(f.root, ".data", ".app.db")); err != nil {
		t.Fatal(err)
	}
}

func TestApplicationStateAdoptionCheckFencesReadReplacement(t *testing.T) {
	f := newApplicationAdoptionFixture(t)
	r, err := f.adopter.Adopt(context.Background(), f.q, f.persist)
	if err != nil {
		t.Fatal(err)
	}
	f.onRead = func() {
		if err := os.Rename(filepath.Join(f.root, "state"), filepath.Join(f.root, "retained")); err != nil {
			t.Fatal(err)
		}
	}
	if err = f.adopter.Check(context.Background(), r); err == nil {
		t.Fatal("check accepted replacement during ACL observation")
	}
}

func TestApplicationStateAdoptionInitializeIsExplicit(t *testing.T) {
	f := newApplicationAdoptionFixture(t)
	f.q.Directory, f.q.Mode, f.q.Expected, f.q.Files = "fresh", ApplicationStateInitialize, nil, nil
	r, err := f.adopter.Adopt(context.Background(), f.q, f.persist)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Created || r.Request.Mode != ApplicationStateAdopt || r.Request.Expected == nil || len(f.journal) != 3 {
		t.Fatalf("initialization not journalled: %+v", r)
	}
	if err = f.adopter.Rollback(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(f.root, "fresh"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("initialize created a DB or rollback removed directory: %v", err)
	}
	f.q.Directory = "state"
	writes := f.writes
	if _, err = f.adopter.Adopt(context.Background(), f.q, f.persist); err == nil || f.writes != writes {
		t.Fatal("initialize accepted existing state")
	}
}

func TestApplicationStateAdoptionRollbackCAS(t *testing.T) {
	for _, kind := range []string{"acl-drift", "path-replaced", "parent-replaced", "journal-tampered"} {
		t.Run(kind, func(t *testing.T) {
			f := newApplicationAdoptionFixture(t)
			r, err := f.adopter.Adopt(context.Background(), f.q, f.persist)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "acl-drift":
				for _, c := range r.Changes {
					if c.Role == "state" {
						a, _ := applicationAdoptionParseACL(c.After, true)
						a["user:2147483646"] = 4
						f.acls[c.Identity] = a.text()
					}
				}
			case "path-replaced":
				if err = os.Rename(filepath.Join(f.root, "state"), filepath.Join(f.root, "retained")); err != nil {
					t.Fatal(err)
				}
				if err = os.Mkdir(filepath.Join(f.root, "state"), 0700); err != nil {
					t.Fatal(err)
				}
			case "parent-replaced":
				old := f.root + "-old"
				if err = os.Rename(f.root, old); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.RemoveAll(old) })
				if err = os.Mkdir(f.root, 0700); err != nil {
					t.Fatal(err)
				}
			case "journal-tampered":
				r.Changes[len(r.Changes)-1].Before = "user::rwx\ngroup::rwx\nother::rwx\n"
			}
			writes := f.writes
			if err = f.adopter.Check(context.Background(), r); err == nil {
				t.Fatal("check accepted drift")
			}
			if err = f.adopter.Rollback(context.Background(), r); err == nil || f.writes != writes {
				t.Fatalf("rollback overwrote drift: %v, %d->%d", err, writes, f.writes)
			}
		})
	}
}

func TestApplicationStateAdoptionPartialFailureRecovery(t *testing.T) {
	f := newApplicationAdoptionFixture(t)
	f.failAt = 2
	r, err := f.adopter.Adopt(context.Background(), f.q, f.persist)
	if err == nil || len(r.Changes) == 0 || len(f.journal) != 1 {
		t.Fatalf("partial failure lost journal: %v", err)
	}
	f.failAt = 0
	if err = f.adopter.Rollback(context.Background(), r); err != nil {
		t.Fatal(err)
	}
}

func TestApplicationStateAdoptionJournalAndFence(t *testing.T) {
	for _, kind := range []string{"journal-failure", "no-journal", "live-writer", "path-race", "acl-race"} {
		t.Run(kind, func(t *testing.T) {
			f := newApplicationAdoptionFixture(t)
			persist := ApplicationStateAdoptionJournal(f.persist)
			switch kind {
			case "journal-failure":
				persist = func(ApplicationStateAdoptionRecord) error { return errors.New("journal unavailable") }
			case "no-journal":
				persist = nil
			case "live-writer":
				f.fenceErr = errors.New("active writer")
			case "path-race":
				persist = func(r ApplicationStateAdoptionRecord) error {
					if err := f.persist(r); err != nil {
						return err
					}
					return os.Rename(filepath.Join(f.root, "state"), filepath.Join(f.root, "moved"))
				}
			case "acl-race":
				persist = func(r ApplicationStateAdoptionRecord) error {
					if err := f.persist(r); err != nil {
						return err
					}
					id := applicationAdoptionTestIdentity(t, f.root)
					a, _ := applicationAdoptionParseACL(f.acls[id], true)
					a["user:123456"] = 0
					a["mask:"] = 0
					f.acls[id] = a.text()
					return nil
				}
			}
			if _, err := f.adopter.Adopt(context.Background(), f.q, persist); err == nil || f.writes != 0 {
				t.Fatalf("unfenced effect: %v writes=%d", err, f.writes)
			}
		})
	}
}

func TestApplicationStateAdoptionACLValidation(t *testing.T) {
	for _, raw := range []string{
		"", "user::rwx\ngroup::---\n", "# file: /outside\nuser::rwx\ngroup::---\nother::---\n",
		"user::rwx\nuser::rwx\ngroup::---\nother::---\n", "user::rwx\ngroup::---\nother::z--\n",
		"user::rwx\nuser:alice:rwx\ngroup::---\nother::---\n", "user::rwx\nuser:1:rwx\ngroup::---\nother::---\n",
		"user::rwx\ngroup::---\nother::---\ndefault:user::rwx\n", strings.Repeat("x", 16385),
	} {
		if _, err := applicationAdoptionParseACL(raw, true); err == nil {
			t.Fatalf("invalid ACL accepted: %.100q", raw)
		}
	}
	for _, kind := range []string{"masked-rights", "foreign-writer", "conflicting-app", "unsupported"} {
		t.Run(kind, func(t *testing.T) {
			f := newApplicationAdoptionFixture(t)
			id := *f.q.Expected
			raw := "user::rwx\ngroup::---\nother::---\n"
			switch kind {
			case "masked-rights":
				raw += "user:123456:rw-\nmask::---\n"
			case "foreign-writer":
				raw += "user:123456:rw-\nmask::rw-\n"
				if err := os.Chmod(filepath.Join(f.root, "state"), 0760); err != nil {
					t.Fatal(err)
				}
			case "conflicting-app":
				raw += fmt.Sprintf("user:%d:r--\nmask::---\n", f.q.ApplicationUID)
			case "unsupported":
				raw = "not a POSIX ACL"
			}
			f.acls[id] = raw
			if _, err := f.adopter.Adopt(context.Background(), f.q, f.persist); err == nil || f.writes != 0 {
				t.Fatalf("unsafe ACL accepted: %v", err)
			}
		})
	}
	for _, tools := range []ApplicationStateACLTools{{"getfacl", "/trusted/setfacl"}, {"/trusted/getfacl", "sh"}, {"/trusted/getfacl;rm", "/trusted/setfacl"}, {"/trusted/getfacl", "/trusted/../setfacl"}} {
		if _, err := NewApplicationStateAdopter(tools, func(context.Context, string, ...string) ([]byte, error) { return nil, nil }, func(context.Context) error { return nil }); err == nil {
			t.Fatalf("unfixed tools accepted: %+v", tools)
		}
	}
}
