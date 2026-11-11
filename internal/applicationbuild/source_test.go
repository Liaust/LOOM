package applicationbuild

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDefaultDiscoveryStaysInsideRuntimePackage(t *testing.T) {
	s, input, runner := fixture(t)
	input.SourceFiles = nil
	writeFile(t, filepath.Join(input.SourceRoot, "app", "helper.py"), "VALUE = 42\n")
	writeFile(t, filepath.Join(input.SourceRoot, "app", "tests", "private.py"), "PRIVATE_SENTINEL")
	writeFile(t, filepath.Join(input.SourceRoot, "app", ".data", "private.py"), "PRIVATE_SENTINEL")
	writeFile(t, filepath.Join(input.SourceRoot, "app", "credentials", "token.py"), "PRIVATE_SENTINEL")
	writeFile(t, filepath.Join(input.SourceRoot, "app", "mail", "raw.py"), "PRIVATE_SENTINEL")
	writeFile(t, filepath.Join(input.SourceRoot, "app", "notes", "private.py"), "PRIVATE_SENTINEL")
	writeFile(t, filepath.Join(input.SourceRoot, "app", "ignored.py"), "PRIVATE_SENTINEL")
	writeFile(t, filepath.Join(input.SourceRoot, "app", ".loomignore"), "ignored.py\n")
	writeFile(t, filepath.Join(input.SourceRoot, "unrelated", "module.py"), "PRIVATE_SENTINEL")
	snapshot, err := s.Snapshot(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, file := range snapshot.Files {
		names = append(names, file.Path)
	}
	if !slices.Equal(names, []string{"app/__init__.py", "app/helper.py", "app/main.py"}) {
		t.Fatalf("runtime scope: %v", names)
	}
	if _, err := os.Stat(s.options.WorkDir); !os.IsNotExist(err) || len(runner.commands) != 0 {
		t.Fatal("default snapshot was not read-only")
	}
	input.ExpectedSourceDigest = snapshot.SourceDigest
	writeFile(t, filepath.Join(input.SourceRoot, "app", "new.py"), "VALUE = 1\n")
	if _, err := s.Build(t.Context(), input); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("new source file did not invalidate plan: %v", err)
	}
}

func TestSingleModuleDiscoveryDoesNotReadSiblingFiles(t *testing.T) {
	s, input, _ := fixture(t)
	input.SourceFiles = nil
	input.PythonModule = "worker"
	writeFile(t, filepath.Join(input.SourceRoot, "worker.py"), "print('worker')\n")
	writeFile(t, filepath.Join(input.SourceRoot, "private.py"), "PRIVATE_SENTINEL")
	snapshot, err := s.Snapshot(t.Context(), input)
	if err != nil || len(snapshot.Files) != 1 || snapshot.Files[0].Path != "worker.py" {
		t.Fatalf("single module discovery: %#v %v", snapshot, err)
	}
}

func TestSourceRejectsUnsafePaths(t *testing.T) {
	for _, name := range []string{
		"../outside.py", "/outside.py", "app/../outside.py", "app//main.py", "app\\main.py",
		".data/token.py", "app/.env", "app/.data/token.py", "app/.cache/module.py",
		"secrets/token.py", "private/app.py", "notes/scratch.py", "mail/raw.py", "raw_mail/mail.py",
		"profiles/auth.py", "archives/source.py", "app/__pycache__/compiled.py", "venv/runtime.py",
		"app/tests/fixtures.py", "app/test_runtime.py", "app/config.json", "app/main.py\n",
	} {
		t.Run(name, func(t *testing.T) {
			s, input, runner := fixture(t)
			input.SourceFiles = append(input.SourceFiles, name)
			if _, err := s.Snapshot(t.Context(), input); !errors.Is(err, ErrUnsafeSource) {
				t.Fatalf("unsafe input accepted: %v", err)
			}
			if len(runner.commands) != 0 {
				t.Fatal("unsafe snapshot ran command")
			}
		})
	}
}

func TestSourceRejectsLinksAndSpecialFiles(t *testing.T) {
	for _, kind := range []string{"file-link", "directory-link", "fifo", "policy-link", "root-link"} {
		t.Run(kind, func(t *testing.T) {
			s, input, _ := fixture(t)
			switch kind {
			case "file-link":
				outside := filepath.Join(t.TempDir(), "outside.py")
				writeFile(t, outside, "PRIVATE_SENTINEL")
				if err := os.Remove(filepath.Join(input.SourceRoot, "app", "main.py")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(input.SourceRoot, "app", "main.py")); err != nil {
					t.Fatal(err)
				}
			case "directory-link":
				outside := t.TempDir()
				writeFile(t, filepath.Join(outside, "main.py"), "PRIVATE_SENTINEL")
				if err := os.Symlink(outside, filepath.Join(input.SourceRoot, "linked")); err != nil {
					t.Fatal(err)
				}
				input.SourceFiles = append(input.SourceFiles, "linked/main.py")
			case "fifo":
				if err := unix.Mkfifo(filepath.Join(input.SourceRoot, "app", "fifo.py"), 0600); err != nil {
					t.Fatal(err)
				}
				input.SourceFiles = append(input.SourceFiles, "app/fifo.py")
			case "policy-link":
				outside := filepath.Join(t.TempDir(), "ignore")
				writeFile(t, outside, "")
				if err := os.Symlink(outside, filepath.Join(input.SourceRoot, ".loomignore")); err != nil {
					t.Fatal(err)
				}
			case "root-link":
				alias := filepath.Join(t.TempDir(), "root")
				if err := os.Symlink(input.SourceRoot, alias); err != nil {
					t.Fatal(err)
				}
				input.SourceRoot = alias
			}
			if _, err := s.Snapshot(t.Context(), input); !errors.Is(err, ErrUnsafeSource) {
				t.Fatalf("link/special input accepted: %v", err)
			}
		})
	}
}

func TestSourcePoliciesAreBoundedAndApplicable(t *testing.T) {
	s, input, _ := fixture(t)
	writeFile(t, filepath.Join(input.SourceRoot, ".loomignore"), "app/\n")
	if _, err := s.Snapshot(t.Context(), input); !errors.Is(err, ErrUnsafeSource) {
		t.Fatalf("ignored input accepted: %v", err)
	}
	writeFile(t, filepath.Join(input.SourceRoot, "app", ".loomignore"), "!*.py\n")
	first, err := s.Snapshot(t.Context(), input)
	if err != nil {
		t.Fatalf("closer negation not honored: %v", err)
	}
	writeFile(t, filepath.Join(input.SourceRoot, "app", ".loomignore"), "# policy revision\n!*.py\n")
	second, err := s.Snapshot(t.Context(), input)
	if err != nil || first.SourceDigest == second.SourceDigest || first.PolicyDigest == second.PolicyDigest {
		t.Fatalf("policy capture not bound: %v", err)
	}
	writeFile(t, filepath.Join(input.SourceRoot, "app", ".loomignore"), "[malformed\n")
	if _, err := s.Snapshot(t.Context(), input); !errors.Is(err, ErrUnsafeSource) {
		t.Fatalf("malformed policy accepted: %v", err)
	}
	writeFile(t, filepath.Join(input.SourceRoot, "app", ".loomignore"), strings.Repeat("#", 1024*1024+1))
	if _, err := s.Snapshot(t.Context(), input); !errors.Is(err, ErrUnsafeSource) {
		t.Fatalf("policy budget ignored: %v", err)
	}
}

func TestSourceByteAndFileBudgets(t *testing.T) {
	s, input, _ := fixture(t)
	s.options.MaxSourceBytes = 1
	if _, err := s.Snapshot(t.Context(), input); !errors.Is(err, ErrUnsafeSource) {
		t.Fatalf("byte limit ignored: %v", err)
	}
	s.options.MaxSourceBytes = 1024
	s.options.MaxSourceFiles = 1
	if _, err := s.Snapshot(t.Context(), input); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("file limit ignored: %v", err)
	}
}

func TestSnapshotIncludesRunnablePackageModule(t *testing.T) {
	s, input, _ := fixture(t)
	writeFile(t, filepath.Join(input.SourceRoot, "app", "__main__.py"), "print('entry')\n")
	input.PythonModule = "app"
	input.SourceFiles = []string{"app/__init__.py", "app/__main__.py"}
	if _, err := s.Snapshot(t.Context(), input); err != nil {
		t.Fatal(err)
	}
}
