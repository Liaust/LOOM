//go:build linux

package hermesprofile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenCustodyTraversesExecuteOnlyAncestor(t *testing.T) {
	root := filepath.Join(t.TempDir(), "search-only")
	leaf := filepath.Join(root, "recovery")
	if err := os.MkdirAll(leaf, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0700) })

	custody, err := openCustody(leaf)
	if err != nil {
		t.Fatalf("execute-only ancestor should remain traversable: %v", err)
	}
	defer custody.close()
	if err := custody.revalidate(); err != nil {
		t.Fatalf("execute-only ancestor revalidation failed: %v", err)
	}
}
