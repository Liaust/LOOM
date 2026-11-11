package serviceregistry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplicationCredentialPrivateRootBelowSharedAncestors(t *testing.T) {
	shared, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0770); err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(shared, "protected")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(private, "oauth.json")
	if err := os.WriteFile(file, []byte("private-test-value"), 0600); err != nil {
		t.Fatal(err)
	}
	f, _, err := applicationRuntimeCredentialOpen(file, uint32(os.Getuid()), private)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := os.Symlink(private, filepath.Join(shared, "alias")); err != nil {
		t.Fatal(err)
	}
	if f, _, err := applicationRuntimeCredentialOpen(filepath.Join(shared, "alias", "oauth.json"), uint32(os.Getuid()), filepath.Join(shared, "alias")); err == nil {
		f.Close()
		t.Fatal("symlink root accepted")
	}
	if err := os.Chmod(private, 0750); err != nil {
		t.Fatal(err)
	}
	if f, _, err := applicationRuntimeCredentialOpen(file, uint32(os.Getuid()), private); err == nil {
		f.Close()
		t.Fatal("nonprivate import root accepted")
	}
}
