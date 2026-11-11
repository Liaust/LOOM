package projectexport

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestDirectorySnapshotPreservesGitAndEmptyDirectories(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"empty", ".git/objects", "node_modules/bulk"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, text := range map[string]string{"note.md": "original", ".git/HEAD": "ref: refs/heads/main\n", "run.sh": "#!/bin/sh\n", "node_modules/bulk/file": "rebuildable"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0700); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := PlanDirectory(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	var output bytes.Buffer
	summary, err := WriteArchive(context.Background(), &output, plan)
	if err != nil {
		t.Fatal(err)
	}
	if summary.SchemaVersion != DirectorySchemaVersion || summary.Ignored.Count == 0 {
		t.Fatalf("%+v", summary)
	}
	seen := map[string]*tar.Header{}
	reader := tar.NewReader(&output)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		seen[header.Name] = header
	}
	if seen["empty/"] == nil || seen[".git/HEAD"] == nil || seen["run.sh"] == nil || seen["run.sh"].Mode != 0700 || seen["node_modules/bulk/file"] != nil {
		t.Fatalf("members: %+v", seen)
	}
	if err := plan.CheckDirectory(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := plan.CheckDirectory(context.Background()); err == nil {
		t.Fatal("changed source accepted")
	}
}

func TestDirectorySnapshotRejectsLinksAndExternalGit(t *testing.T) {
	for _, kind := range []string{"link", "external-git"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			if kind == "link" {
				if err := os.Symlink(t.TempDir(), filepath.Join(root, "linked")); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /outside"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			p, err := PlanDirectory(context.Background(), root)
			if err == nil {
				p.Close()
				t.Fatal("unsafe snapshot accepted")
			}
		})
	}
}
