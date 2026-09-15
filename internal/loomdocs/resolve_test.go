package loomdocs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRootPrecedence(t *testing.T) {
	temp := t.TempDir()
	explicit := filepath.Join(temp, "explicit")
	environment := filepath.Join(temp, "environment")
	packaged := filepath.Join(temp, "bin", "..", "share", "loom", "docs")
	repository := filepath.Join(temp, "repo")
	for _, path := range []string{explicit, environment, packaged, filepath.Join(repository, "docs")} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repository, "go.mod"), []byte("module fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(temp, "bin", "loom")

	root, err := ResolveRoot(ResolveOptions{ExplicitPath: explicit, Environment: environment, ExecutablePath: executable, WorkingDir: repository, LoomVersion: "v1"})
	if err != nil || root.Source != SourceExplicit {
		t.Fatalf("explicit root = %#v err=%v", root, err)
	}
	root, err = ResolveRoot(ResolveOptions{Environment: environment, ExecutablePath: executable, WorkingDir: repository})
	if err != nil || root.Source != SourceEnvironment {
		t.Fatalf("environment root = %#v err=%v", root, err)
	}
	root, err = ResolveRoot(ResolveOptions{ExecutablePath: executable, WorkingDir: repository})
	if err != nil || root.Source != SourcePackaged {
		t.Fatalf("packaged root = %#v err=%v", root, err)
	}
	if err := os.RemoveAll(filepath.Clean(packaged)); err != nil {
		t.Fatal(err)
	}
	root, err = ResolveRoot(ResolveOptions{ExecutablePath: executable, WorkingDir: filepath.Join(repository, "nested")})
	if err != nil || root.Source != SourceRepository {
		t.Fatalf("repository root = %#v err=%v", root, err)
	}
}

func TestResolveRootRejectsMissingAndNonDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := ResolveRoot(ResolveOptions{ExplicitPath: missing}); err == nil {
		t.Fatal("expected missing explicit root to fail")
	}
	file := filepath.Join(t.TempDir(), "docs")
	if err := os.WriteFile(file, []byte("no"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveRoot(ResolveOptions{ExplicitPath: file}); err == nil {
		t.Fatal("expected non-directory root to fail")
	}
}
