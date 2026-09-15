package storagedoctor

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/fsaccess"
)

func TestCheckCanonicalDirectoryUsesEffectiveWriteAndExecuteAccess(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o777); err != nil {
		t.Fatalf("chmod fixture: %v", err)
	}

	tests := []struct {
		name    string
		missing fsaccess.Requirement
	}{
		{name: "write denied", missing: fsaccess.Write},
		{name: "execute denied", missing: fsaccess.Execute},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			result := checkCanonicalDirectoryWithAccess("canonical_dir", directory, func(path string, requirements ...fsaccess.Requirement) fsaccess.Result {
				called = true
				if path != directory {
					t.Fatalf("access path = %q, want %q", path, directory)
				}
				if len(requirements) != 2 || requirements[0] != fsaccess.Write || requirements[1] != fsaccess.Execute {
					t.Fatalf("access requirements = %#v, want write and execute", requirements)
				}
				return fsaccess.Result{Path: path, MissingModes: []fsaccess.Requirement{test.missing}, Error: "effective access denied"}
			})
			if !called {
				t.Fatal("effective access checker was not called")
			}
			if result.Status != StatusError || !strings.Contains(result.Summary, "service account") || !strings.Contains(result.Detail, "effective access denied") {
				t.Fatalf("result = %#v, want effective-access error", result)
			}
		})
	}
}

func TestCheckCanonicalDirectoryRejectsSymlinkBeforeAccessCheck(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("create target: %v", err)
	}
	link := filepath.Join(root, "documents")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	result := checkCanonicalDirectoryWithAccess("canonical_dir", link, func(string, ...fsaccess.Requirement) fsaccess.Result {
		t.Fatal("access checker must not run for a symlink")
		return fsaccess.Result{}
	})
	if result.Status != StatusError || !strings.Contains(result.Summary, "must not be a symlink") {
		t.Fatalf("result = %#v, want symlink error", result)
	}
}

func TestCheckNotSeparateMountClassifiesFindmntOutcomes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		run        func(string) ([]byte, error)
		wantStatus string
		wantText   string
	}{
		{
			name:       "mountpoint",
			run:        func(string) ([]byte, error) { return []byte("/srv/loom/box/Documents\n"), nil },
			wantStatus: StatusError,
			wantText:   "must not be a separate or bind mount",
		},
		{
			name:       "not mountpoint",
			run:        func(string) ([]byte, error) { return exec.Command("sh", "-c", "exit 1").CombinedOutput() },
			wantStatus: StatusOK,
			wantText:   "not a separate mountpoint",
		},
		{
			name:       "findmnt execution failed",
			run:        func(string) ([]byte, error) { return nil, errors.New("findmnt unavailable") },
			wantStatus: StatusError,
			wantText:   "could not verify",
		},
		{
			name:       "findmnt abnormal exit",
			run:        func(string) ([]byte, error) { return exec.Command("sh", "-c", "exit 2").CombinedOutput() },
			wantStatus: StatusError,
			wantText:   "could not verify",
		},
		{
			name: "findmnt permission failure",
			run: func(string) ([]byte, error) {
				return exec.Command("sh", "-c", "echo permission denied >&2; exit 1").CombinedOutput()
			},
			wantStatus: StatusError,
			wantText:   "could not verify",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result := checkNotSeparateMountWithRunner("/srv/loom/box/Documents", test.run)
			if result.Status != test.wantStatus || !strings.Contains(result.Summary, test.wantText) {
				t.Fatalf("result = %#v, want status=%q text=%q", result, test.wantStatus, test.wantText)
			}
		})
	}
}
