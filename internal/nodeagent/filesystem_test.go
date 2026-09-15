package nodeagent

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestFilesystemRootsAddCommandUpdatesConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := Store{
		ConfigPath: filepath.Join(dir, "config.json"),
		StatePath:  filepath.Join(dir, "state.json"),
		DataDir:    filepath.Join(dir, "data"),
	}
	if err := store.SaveConfig(Config{
		MainURL:     "http://10.44.0.2:8080",
		NodeKey:     "workspace-test",
		DisplayName: "Workspace Test",
	}); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}
	safeRoot := filepath.Join(dir, "safe-root")
	if err := os.MkdirAll(safeRoot, 0o700); err != nil {
		t.Fatalf("create safe root: %v", err)
	}

	opts := rootOptions{
		configPath: store.ConfigPath,
		statePath:  store.StatePath,
		dataDir:    store.DataDir,
		out:        io.Discard,
		errOut:     io.Discard,
	}
	cmd := newFilesystemCommand(&opts)
	cmd.SetArgs([]string{
		"roots", "add",
		"--key", "slice13",
		"--path", safeRoot,
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("filesystem roots add failed: %v", err)
	}

	loaded, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if len(loaded.Filesystem.SafeRoots) != 1 {
		t.Fatalf("expected one filesystem root, got %#v", loaded.Filesystem.SafeRoots)
	}
	root := loaded.Filesystem.SafeRoots[0]
	wantPath, err := canonicalDirectoryPath(safeRoot)
	if err != nil {
		t.Fatalf("canonicalDirectoryPath failed: %v", err)
	}
	if root.RootKey != "slice13" || root.AbsolutePath != wantPath || !root.AllowList || !root.AllowMetadata || !root.AllowIngest {
		t.Fatalf("unexpected filesystem root: %#v", root)
	}
}
