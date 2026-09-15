package notesprojection

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestProjectionInheritedOwnerReadOnlyACL(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("run as an unprivileged user to exercise inherited owner denial")
	}
	base, err := os.MkdirTemp("/tmp", "lnacl-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(base, func(p string, d os.DirEntry, e error) error {
			if e == nil && d.IsDir() {
				_ = os.Chmod(p, 0o700)
			}
			return nil
		})
		if err := os.RemoveAll(base); err != nil {
			t.Error(err)
		}
	})
	source := filepath.Join(base, "source.md")
	if err := os.WriteFile(source, []byte("ACL source"), 0o600); err != nil {
		t.Fatal(err)
	}
	// user::r-x, a named read-only agent, group::r-x, mask::r-x, other::r-x.
	raw := make([]byte, 4+5*8)
	binary.LittleEndian.PutUint32(raw, 2)
	for i, e := range []struct {
		tag uint16
		uid uint32
	}{{1, ^uint32(0)}, {2, 65533}, {4, ^uint32(0)}, {16, ^uint32(0)}, {32, ^uint32(0)}} {
		binary.LittleEndian.PutUint16(raw[4+i*8:], e.tag)
		binary.LittleEndian.PutUint16(raw[6+i*8:], 5)
		binary.LittleEndian.PutUint32(raw[8+i*8:], e.uid)
	}
	if err := unix.Setxattr(base, "system.posix_acl_default", raw, 0); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(base, "denied")
	if err := os.Mkdir(probe, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(probe, "must-fail"), []byte("x"), 0o600); !os.IsPermission(err) {
		t.Fatalf("fixture does not reproduce owner denial: %v", err)
	}
	root := filepath.Join(base, "generated")
	svc := NewService(fakeProjectionSources{sources: []SourceObject{{RootKind: RootKindBoxNotes, SourceNodeKey: "main", RelativePath: "new/deep/note.md", SourcePath: source}}}, root)
	for range 2 {
		if _, err := svc.Rebuild(t.Context(), RebuildInput{}); err != nil {
			t.Fatal(err)
		}
	}
	assertProjectionFile(t, filepath.Join(root, "nodes/main/Notes/new/deep/note.md"), "ACL source")
	if err := filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		want := os.FileMode(0o444)
		if d.IsDir() {
			want = 0o555
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode %o want %o", p, info.Mode().Perm(), want)
		}
		acl := make([]byte, 256)
		n, e := unix.Getxattr(p, "system.posix_acl_access", acl)
		if e != nil {
			return e
		}
		for off := 4; off+8 <= n; off += 8 {
			if binary.LittleEndian.Uint16(acl[off:]) == 2 && binary.LittleEndian.Uint16(acl[off+2:])&2 != 0 {
				t.Errorf("named agent gained write: %s", p)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(source); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("source mode changed: %v", err)
	}
}
