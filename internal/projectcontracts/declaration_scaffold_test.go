package projectcontracts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectCreateConflictAndReplay(t *testing.T) {
	opts := ScaffoldOptions{Name: "Create test", Slug: "create-test", OwnerNode: "main", Directory: t.TempDir(), Mode: ScaffoldModeDeclaration}
	result, err := ScaffoldProject(opts)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadProject(result.ProjectRoot)
	if err != nil {
		t.Fatal(err)
	}
	// Ordinary user payload survives a constructor replay.
	payload := filepath.Join(result.ProjectRoot, "notes", "user.md")
	if err := os.WriteFile(payload, []byte("owned"), 0600); err != nil {
		t.Fatal(err)
	}
	repeated, err := ScaffoldProject(opts)
	if err != nil || repeated.ProjectID != result.ProjectID {
		t.Fatalf("replay: %+v %v", repeated, err)
	}
	after, err := LoadProject(result.ProjectRoot)
	if err != nil || string(after.Raw) != string(loaded.Raw) {
		t.Fatal("replay changed source")
	}
	opts.Name = "Different name"
	if _, err := ScaffoldProject(opts); err == nil {
		t.Fatal("conflicting identity accepted")
	}
	after, err = LoadProject(result.ProjectRoot)
	if err != nil || string(after.Raw) != string(loaded.Raw) {
		t.Fatal("conflict changed source")
	}
	if raw, err := os.ReadFile(payload); err != nil || string(raw) != "owned" {
		t.Fatal("user data changed")
	}
}

func TestProjectCreatePreflightNegatives(t *testing.T) {
	for _, conflict := range []string{"notes", "repos", ".loom", "symlink", "legacy", "root_symlink"} {
		t.Run(conflict, func(t *testing.T) {
			opts := ScaffoldOptions{Name: "Create test", Slug: "create-test", OwnerNode: "main", Directory: t.TempDir(), Mode: ScaffoldModeDeclaration}
			root := filepath.Join(opts.Directory, opts.Slug)
			if conflict == "root_symlink" {
				if err := os.Symlink(t.TempDir(), root); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
				switch conflict {
				case "symlink":
					if err := os.Symlink(t.TempDir(), filepath.Join(root, "notes")); err != nil {
						t.Fatal(err)
					}
				case "legacy":
					if err := os.WriteFile(filepath.Join(root, LegacyRootContractPath), []byte("existing"), 0600); err != nil {
						t.Fatal(err)
					}
				default:
					if err := os.WriteFile(filepath.Join(root, conflict), []byte("existing"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, dry := range []bool{true, false} {
				opts.DryRun = dry
				if _, err := ScaffoldProject(opts); err == nil {
					t.Fatal("conflict accepted")
				}
			}
			if raw, err := os.ReadFile(filepath.Join(root, CanonicalRootContractPath)); err == nil && strings.Contains(string(raw), ProjectSchemaV05) {
				t.Fatal("preflight published source")
			}
		})
	}
}

func TestProjectCreateExplainsExistingCanonicalLegacySource(t *testing.T) {
	opts := ScaffoldOptions{Name: "Existing", Slug: "existing", OwnerNode: "main", Directory: t.TempDir(), Mode: ScaffoldModeDeclaration}
	root := filepath.Join(opts.Directory, opts.Slug)
	if err := os.MkdirAll(filepath.Join(root, ".loom"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, CanonicalRootContractPath)
	raw := []byte("kind: loom.project\nschema_version: project.contract.v0.4\nfacets: {notes: true}\n")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, dry := range []bool{true, false} {
		opts.DryRun = dry
		if _, err := ScaffoldProject(opts); err == nil || !strings.Contains(err.Error(), "create conflicts with existing project source; source preserved") {
			t.Fatalf("existing project conflict was not explained: %v", err)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(raw) {
		t.Fatal("existing source changed")
	}
}
