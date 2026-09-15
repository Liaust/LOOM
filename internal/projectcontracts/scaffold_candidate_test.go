package projectcontracts

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestProjectScaffoldCandidatePackageSources(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "mixed_packages_and_ancillary", true: "invalid_existing_package"}[invalid], func(t *testing.T) {
			source, err := ScaffoldProject(ScaffoldOptions{Name: "Source Project", OwnerNode: "main", Facets: []string{"scripts"}, Directory: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			original := filepath.Join(source.ProjectRoot, "scripts", "hello_world")
			if err := os.Rename(original, filepath.Join(source.ProjectRoot, "scripts", "user_package")); err != nil {
				t.Fatal(err)
			}
			original = filepath.Join(source.ProjectRoot, "scripts", "user_package")
			manifest := filepath.Join(original, "loom.script.yaml")
			raw, err := os.ReadFile(manifest)
			if err != nil {
				t.Fatal(err)
			}
			raw = []byte(strings.ReplaceAll(string(raw), "hello_world", "user_package"))
			if invalid {
				raw = []byte("kind: invalid\n")
			}
			if err := os.WriteFile(manifest, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(original, "loom.exposure.yaml")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(original, "ancillary.bin"), []byte("user payload\x00\xff"), 0o600); err != nil {
				t.Fatal(err)
			}
			before := facetPreflightTree(t, source.ProjectRoot)
			candidate, err := ScaffoldProject(ScaffoldOptions{Name: "Source Project", OwnerNode: "main", Facets: []string{"scripts"}, Directory: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadProject(candidate.ProjectRoot)
			if err != nil {
				t.Fatal(err)
			}
			loaded.candidateSources = &scaffoldCandidateSources{root: source.ProjectRoot, packages: map[string]bool{"scripts/hello_world": true}}
			report := Validate(loaded)
			if report.OK == invalid || len(report.Scripts) != 2 {
				t.Fatalf("both packages must be analyzed, invalid=%t: %#v", invalid, report)
			}
			if !invalid {
				sourceReport := Analyze(source.ProjectRoot).Report
				for _, item := range report.Scripts {
					if item.Key == "user_package" && (item.PackageHash != sourceReport.Scripts[0].PackageHash || item.Folder != "scripts/user_package") {
						t.Fatalf("original package identity/hash changed: %#v", item)
					}
				}
			}
			if !reflect.DeepEqual(before, facetPreflightTree(t, source.ProjectRoot)) {
				t.Fatal("candidate changed original package")
			}
			if _, err := os.Stat(filepath.Join(candidate.ProjectRoot, "scripts", "user_package")); !os.IsNotExist(err) {
				t.Fatal("existing package should not be copied to candidate")
			}
		})
	}
}

func TestProjectScaffoldCandidateNormalAnalyzeUnchanged(t *testing.T) {
	source, err := ScaffoldProject(ScaffoldOptions{Name: "Normal Analyze", OwnerNode: "main", Preset: PresetAutomation, Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadProject(source.ProjectRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, facet := range []string{"scripts", "workflows", "connectors", "modules", "schedules", "direct_events"} {
		want, wantErr := os.ReadDir(filepath.Join(loaded.RootPath, facet))
		got, gotErr := projectFacetEntries(loaded, facet)
		if (wantErr == nil) != (gotErr == nil) || len(want) != len(got) {
			t.Fatalf("normal discovery changed for %s", facet)
		}
		for i := range want {
			if want[i].Name() != got[i].Name() || want[i].Type() != got[i].Type() {
				t.Fatalf("entry changed for %s", facet)
			}
		}
		packageLoaded, path := projectFacetPackage(loaded, facet, "example")
		if packageLoaded.RootPath != loaded.RootPath || path != filepath.Join(loaded.RootPath, facet, "example") {
			t.Fatal("normal package roots changed")
		}
	}
	if !Analyze(source.ProjectRoot).Report.OK {
		t.Fatal("normal analysis failed")
	}
}

func TestProjectScaffoldCandidateOriginalPackagePathEscape(t *testing.T) {
	source, err := ScaffoldProject(ScaffoldOptions{Name: "Path Escape", OwnerNode: "main", Facets: []string{"scripts"}, Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(source.ProjectRoot, "scripts/hello_world/loom.script.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "./run.sh") {
		t.Fatal("fixture missing entrypoint")
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(raw), "./run.sh", "./../../outside.sh")), 0o600); err != nil {
		t.Fatal(err)
	}
	candidate, err := ScaffoldProject(ScaffoldOptions{Name: "Path Escape", OwnerNode: "main", Facets: []string{"docs"}, Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadProject(candidate.ProjectRoot)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Contract.Facets["scripts"] = true
	loaded.candidateSources = &scaffoldCandidateSources{root: source.ProjectRoot, packages: map[string]bool{}}
	report := Validate(loaded)
	for _, diagnostic := range report.Diagnostics {
		if diagnostic.Code == "script.entrypoint_missing" && strings.Contains(diagnostic.File, source.ProjectRoot) {
			return
		}
	}
	t.Fatalf("source package path escape was hidden: %#v", report)
}

func TestProjectScaffoldCandidateWorkflowReferencesExistingScript(t *testing.T) {
	source, err := ScaffoldProject(ScaffoldOptions{Name: "Cross Root", OwnerNode: "main", Facets: []string{"scripts"}, Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := ScaffoldProject(ScaffoldOptions{Name: "Cross Root", OwnerNode: "main", Facets: []string{"workflows"}, Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(candidate.ProjectRoot, "workflows/example_workflow/loom.workflow.yaml")
	raw, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	old := "kind: workflow"
	if !strings.Contains(string(raw), old) {
		t.Fatal("fixture lacks workflow implementation")
	}
	raw = []byte(strings.ReplaceAll(string(raw), old, "kind: script\n  script_ref: ../../scripts/hello_world/loom.script.yaml"))
	if err := os.WriteFile(manifest, raw, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadProject(candidate.ProjectRoot)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Contract.Facets["scripts"] = true
	loaded.candidateSources = &scaffoldCandidateSources{root: source.ProjectRoot, packages: map[string]bool{"workflows/example_workflow": true}}
	report := Validate(loaded)
	if len(report.Scripts) != 1 {
		t.Fatalf("expected existing declared script: %#v", report.Scripts)
	}
	if !report.OK {
		t.Fatalf("candidate workflow must resolve the existing declared script without copying it: %#v", report.Diagnostics)
	}
}

func TestProjectScaffoldCandidateExistingWorkflowReferencesNewScript(t *testing.T) {
	source, err := ScaffoldProject(ScaffoldOptions{Name: "Reverse Reference", OwnerNode: "main", Facets: []string{"workflows"}, Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(source.ProjectRoot, "workflows/example_workflow/loom.workflow.yaml")
	raw, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.ReplaceAll(string(raw), "kind: workflow", "kind: script\n  script_ref: ../../scripts/hello_world/loom.script.yaml"))
	if err := os.WriteFile(manifest, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	candidate, err := ScaffoldProject(ScaffoldOptions{Name: "Reverse Reference", OwnerNode: "main", Facets: []string{"scripts"}, Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadProject(candidate.ProjectRoot)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Contract.Facets["workflows"] = true
	loaded.candidateSources = &scaffoldCandidateSources{root: source.ProjectRoot, packages: map[string]bool{"scripts/hello_world": true}}
	report := Validate(loaded)
	if !report.OK || len(report.Workflows) != 1 || report.Workflows[0].ScriptManifestPath != filepath.ToSlash(filepath.Join(candidate.ProjectRoot, "scripts/hello_world/loom.script.yaml")) {
		t.Fatalf("original workflow must use exact new script source: %#v", report)
	}
}

func TestProjectScaffoldCandidatePackageCollision(t *testing.T) {
	for _, kind := range []string{"directory", "file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			source, err := ScaffoldProject(ScaffoldOptions{Name: "Collision Source", OwnerNode: "main", Facets: []string{"docs"}, Directory: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(source.ProjectRoot, "scripts"), 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(source.ProjectRoot, "scripts/hello_world")
			switch kind {
			case "directory":
				err = os.Mkdir(path, 0o755)
			case "file":
				err = os.WriteFile(path, []byte("user data"), 0o600)
			case "symlink":
				err = os.Symlink(t.TempDir(), path)
			}
			if err != nil {
				t.Fatal(err)
			}
			candidate, err := ScaffoldProject(ScaffoldOptions{Name: "Collision Source", OwnerNode: "main", Facets: []string{"scripts"}, Directory: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadProject(candidate.ProjectRoot)
			if err != nil {
				t.Fatal(err)
			}
			loaded.candidateSources = &scaffoldCandidateSources{root: source.ProjectRoot, packages: map[string]bool{"scripts/hello_world": true}}
			if report := Validate(loaded); report.OK {
				t.Fatalf("source %s collision was hidden", kind)
			}
		})
	}
}
