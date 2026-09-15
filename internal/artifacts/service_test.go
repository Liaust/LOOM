package artifacts

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveArtifactPathAcceptsRelativePathInsideArtifactDir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "artifacts"), 0o700); err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(root, "artifacts", "report.md")
	if err := os.WriteFile(expected, []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected, err := filepath.EvalSymlinks(expected)
	if err != nil {
		t.Fatal(err)
	}

	resolved, err := resolveArtifactPath(root, "artifacts/report.md")
	if err != nil {
		t.Fatalf("resolveArtifactPath returned error: %v", err)
	}
	if resolved != expected {
		t.Fatalf("expected %q, got %q", expected, resolved)
	}
}

func TestResolveArtifactPathRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if _, err := resolveArtifactPath(root, "../outside.md"); err == nil {
		t.Fatal("expected traversal path to be rejected")
	}
}

func TestResolveArtifactPathRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	if _, err := resolveArtifactPath(root, "link/secret.md"); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
}
