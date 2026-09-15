package config

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sourceNix(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("nix"); err == nil {
		return p
	}
	p := "/nix/var/nix/profiles/default/bin/nix"
	if _, err := os.Stat(p); err != nil {
		t.Skip("source boundary runtime proof requires Nix")
	}
	return p
}

func sourceCommand(t *testing.T, dir, name string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	// Fixture Git state must not inherit the caller's worktree or hooks.
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	var diagnostic strings.Builder
	cmd.Stderr = &diagnostic
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, diagnostic.String())
	}
	return b
}

func sourceWrite(t *testing.T, root, name, value string) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

func sourceSnapshot(t *testing.T, nix, root, helper string) string {
	t.Helper()
	b := sourceCommand(t, root, nix, "--extra-experimental-features", "nix-command flakes",
		"eval", "--offline", "--no-write-lock-file", "--impure", "--json", "--expr",
		"toString (import "+helper+" { root = ./.; }).outPath")
	var p string
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p, "/nix/store/") {
		t.Fatalf("not a store snapshot: %q", p)
	}
	return p
}

func assertSourceBudget(t *testing.T, root string, limit int64) {
	t.Helper()
	for _, name := range []string{".git", ".loom-acceptance"} {
		if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("snapshot contains forbidden %s: %v", name, err)
		}
	}
	var bytes int64
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			bytes += info.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if bytes > limit {
		t.Fatalf("source snapshot is %d bytes, above %d-byte budget", bytes, limit)
	}
	t.Logf("source snapshot: %d logical bytes", bytes)
}

func TestNixSourceBoundary(t *testing.T) {
	nix := sourceNix(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal("Git required for source boundary proof")
	}
	for _, mode := range []string{"checkout", "linked_worktree", "exported_source"} {
		t.Run(mode, func(t *testing.T) {
			parent, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(parent, "source tree")
			sourceWrite(t, root, "flake.nix", "{ outputs = { self }: {}; }\n")
			sourceWrite(t, root, "source-flake.nix", readRepoFile(t, "tests/nix/source-flake.nix"))
			sourceWrite(t, root, ".gitignore", ".loom-acceptance/\n")
			sourceWrite(t, root, "tracked.txt", "original\n")
			if mode != "exported_source" {
				sourceCommand(t, root, "git", "init", "--quiet")
				sourceCommand(t, root, "git", "config", "core.hooksPath", filepath.Join(parent, "no-hooks"))
				sourceCommand(t, root, "git", "add", ".")
				sourceCommand(t, root, "git", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--quiet", "-m", "fixture")
				if mode == "linked_worktree" {
					linked := filepath.Join(parent, "linked tree")
					sourceCommand(t, root, "git", "worktree", "add", "--quiet", "--detach", linked, "HEAD")
					root = linked
				}
			}
			sourceWrite(t, root, "tracked.txt", "dirty tracked value\n")
			sourceWrite(t, root, "new-source.txt", "new source\n")
			if mode != "exported_source" {
				sourceCommand(t, root, "git", "add", "new-source.txt")
				sourceWrite(t, root, "untracked.txt", "must not enter a Git snapshot\n")
			}
			sourceWrite(t, root, ".loom-acceptance/large.log", strings.Repeat("ignored", 65536))
			before := sourceSnapshot(t, nix, root, "./source-flake.nix")
			assertSourceBudget(t, before, 16*1024)
			for name, want := range map[string]string{"tracked.txt": "dirty tracked value\n", "new-source.txt": "new source\n"} {
				b, err := os.ReadFile(filepath.Join(before, name))
				if err != nil || string(b) != want {
					t.Fatalf("working-tree input %s lost: %q %v", name, b, err)
				}
			}
			if mode != "exported_source" {
				if _, err := os.Lstat(filepath.Join(before, "untracked.txt")); !os.IsNotExist(err) {
					t.Fatal("untracked input entered Git snapshot")
				}
				sourceCommand(t, root, "git", "update-ref", "refs/source-boundary/metadata-only", "HEAD")
			}
			sourceWrite(t, root, ".loom-acceptance/large.log", strings.Repeat("changed", 65536))
			if after := sourceSnapshot(t, nix, root, "./source-flake.nix"); after != before {
				t.Fatalf("metadata/scratch changes created a new source copy: %s -> %s", before, after)
			}
			sourceWrite(t, root, "tracked.txt", "actual source correction\n")
			after := sourceSnapshot(t, nix, root, "./source-flake.nix")
			if after == before {
				t.Fatal("real source change was hidden")
			}
			b, err := os.ReadFile(filepath.Join(after, "tracked.txt"))
			if err != nil || string(b) != "actual source correction\n" {
				t.Fatalf("changed source not visible: %q %v", b, err)
			}
		})
	}
}

func TestNixSourceImportsUseBoundedHelper(t *testing.T) {
	root := repoRoot(t)
	for _, dir := range []string{"internal", "nix", "scripts", "tests"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			if filepath.ToSlash(rel) == "tests/nix/source-flake.nix" {
				return nil
			}
			switch filepath.Ext(p) {
			case ".go", ".nix", ".sh", ".py":
			default:
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if strings.Contains(string(b), "builtins."+"getFlake") {
				t.Errorf("%s bypasses the bounded source helper", rel)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestNixSourceCheckoutBudget(t *testing.T) {
	nix := sourceNix(t)
	root := sourceSnapshot(t, nix, repoRoot(t), "./tests/nix/source-flake.nix")
	assertSourceBudget(t, root, 64*1024*1024)
}
