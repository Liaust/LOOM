package nodeagent

import (
	"golang.org/x/sys/unix"
	"loom.local/loom/internal/projects"
	"os"
	"path/filepath"
	"testing"
)

func TestApplicationSourceDirectoryIdentity(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	allocation := filepath.Join(root, "data")
	if err = os.MkdirAll(filepath.Join(allocation, "papers"), 0700); err != nil {
		t.Fatal(err)
	}
	var st unix.Stat_t
	if err = unix.Stat(allocation, &st); err != nil {
		t.Fatal(err)
	}
	b := projects.DeclarationApplicationDataBinding{Path: allocation, Device: uint64(st.Dev), Inode: st.Ino, Subpath: "papers"}
	if err = validateApplicationSourcePath(b); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(filepath.Join(allocation, "papers"), filepath.Join(root, "elsewhere")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(allocation, "papers")); err != nil {
		t.Fatal(err)
	}
	if validateApplicationSourcePath(b) == nil {
		t.Fatal("symlink escaped allocation")
	}
	b.Subpath = ""
	b.Inode++
	if validateApplicationSourcePath(b) == nil {
		t.Fatal("replacement allocation accepted")
	}
	b.Inode--
	b.Subpath = "../elsewhere"
	if validateApplicationSourcePath(b) == nil {
		t.Fatal("traversal accepted")
	}
}
