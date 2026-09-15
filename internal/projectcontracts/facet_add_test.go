package projectcontracts

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func facetPreflightTree(t *testing.T, root string) map[string]string {
	t.Helper()
	entries := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			entries[relative] = info.Mode().String()
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unexpected fixture type: %s", path)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		entries[relative] = fmt.Sprintf("%s:%x", info.Mode(), sha256.Sum256(content))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestProjectFacetPreflightMinimalReposComplete(t *testing.T) {
	scaffold, err := ScaffoldProject(ScaffoldOptions{Name: "Facet Preflight", Slug: "facet-preflight", OwnerNode: "main", Preset: PresetMinimal, Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	before := facetPreflightTree(t, scaffold.ProjectRoot)
	preview, err := AddProjectFacets(AddProjectFacetsOptions{ProjectRoot: scaffold.ProjectRoot, Facets: []string{"repos"}, DryRun: true})
	if err != nil {
		t.Fatalf("generated candidate should validate: %v", err)
	}
	if !reflect.DeepEqual(before, facetPreflightTree(t, scaffold.ProjectRoot)) {
		t.Fatal("dry-run changed source")
	}
	if !preview.OK || !preview.Validation.OK || preview.Validation.State != ScaffoldValidationPassed {
		t.Errorf("dry-run must validate the complete candidate, got %#v", preview.Validation)
	}
	result, err := AddProjectFacets(AddProjectFacetsOptions{ProjectRoot: scaffold.ProjectRoot, Facets: []string{"repos"}})
	if err != nil {
		t.Fatalf("adding repos to an untouched generated project must not fail after publication: %v", err)
	}
	if !result.OK || !result.Validation.OK || !Analyze(scaffold.ProjectRoot).Report.OK {
		t.Fatal("published candidate is invalid")
	}
}

func TestProjectFacetPreflightCustomizedPolicyRefusesBeforeWrites(t *testing.T) {
	for _, dryRun := range []bool{true, false} {
		t.Run(fmt.Sprintf("dry_run_%t", dryRun), func(t *testing.T) {
			scaffold, err := ScaffoldProject(ScaffoldOptions{Name: "Custom Policy", Slug: "custom-policy", OwnerNode: "main", Preset: PresetMinimal, Directory: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(scaffold.ProjectRoot, ".loom/contracts/backup.yaml")
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			content = []byte(strings.ReplaceAll(string(content), ": 1048576\n", ": 8388608\n"))
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatal(err)
			}
			if !Analyze(scaffold.ProjectRoot).Report.OK {
				t.Fatal("custom fixture must be valid before request")
			}
			before := facetPreflightTree(t, scaffold.ProjectRoot)
			_, err = AddProjectFacets(AddProjectFacetsOptions{ProjectRoot: scaffold.ProjectRoot, Facets: []string{"repos"}, DryRun: dryRun})
			if err == nil {
				t.Error("customized policy needs explicit resolution, not an unvalidated success")
			}
			if !reflect.DeepEqual(before, facetPreflightTree(t, scaffold.ProjectRoot)) {
				t.Fatal("rejected candidate changed source tree")
			}
		})
	}
}

// An existing package in a previously disabled facet must still be examined
// when the candidate also introduces that facet's generated example package.
func TestProjectFacetPreflightExistingPackageRefusesBeforeWrites(t *testing.T) {
	for _, dryRun := range []bool{true, false} {
		t.Run(fmt.Sprintf("dry_run_%t", dryRun), func(t *testing.T) {
			scaffold, err := ScaffoldProject(ScaffoldOptions{Name: "Existing Package", OwnerNode: "main", Facets: []string{"docs"}, Directory: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			packageRoot := filepath.Join(scaffold.ProjectRoot, "scripts", "user_tool")
			if err := os.MkdirAll(packageRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(packageRoot, "loom.script.yaml"), []byte("kind: user.invalid\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			before := facetPreflightTree(t, scaffold.ProjectRoot)
			result, err := AddProjectFacets(AddProjectFacetsOptions{ProjectRoot: scaffold.ProjectRoot, Facets: []string{"scripts"}, DryRun: dryRun})
			if err == nil || result.OK {
				t.Errorf("existing invalid package must refuse the complete candidate: result=%#v err=%v", result.Validation, err)
			}
			if !reflect.DeepEqual(before, facetPreflightTree(t, scaffold.ProjectRoot)) {
				t.Fatal("existing package refusal changed source")
			}
		})
	}
}

func TestProjectFacetPreflightCustomPackagePreserved(t *testing.T) {
	scaffold, err := ScaffoldProject(ScaffoldOptions{Name: "User Package", OwnerNode: "main", Facets: []string{"docs"}, Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	packageRoot := filepath.Join(scaffold.ProjectRoot, "scripts", "user_tool")
	if err := os.MkdirAll(packageRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"loom.script.yaml": "kind: loom.script\nid: user_tool\nname: User Tool\nversion: 1.0.0\nentrypoint:\n  command: [bash, ./run.sh]\nexecution:\n  timeout_seconds: 10\n  network: false\n",
		"run.sh":           "#!/usr/bin/env bash\nprintf 'user tool'\n",
		"user-data.bin":    "user-owned tiny fixture\x00\xff",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(packageRoot, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	before := facetPreflightTree(t, scaffold.ProjectRoot)
	preview, err := AddProjectFacets(AddProjectFacetsOptions{ProjectRoot: scaffold.ProjectRoot, Facets: []string{"scripts"}, DryRun: true})
	if err != nil || !preview.OK || !preview.Validation.OK {
		t.Fatalf("candidate must include user and generated packages: result=%#v err=%v", preview.Validation, err)
	}
	if !reflect.DeepEqual(before, facetPreflightTree(t, scaffold.ProjectRoot)) {
		t.Fatal("preview changed source")
	}
	result, err := AddProjectFacets(AddProjectFacetsOptions{ProjectRoot: scaffold.ProjectRoot, Facets: []string{"scripts"}})
	if err != nil || !result.OK {
		t.Fatalf("apply: result=%#v err=%v", result.Validation, err)
	}
	for name, content := range files {
		actual, err := os.ReadFile(filepath.Join(packageRoot, name))
		if err != nil || string(actual) != content {
			t.Fatalf("custom package file %s changed: %v", name, err)
		}
	}
	analysis := Analyze(scaffold.ProjectRoot)
	if !analysis.Report.OK || len(analysis.Report.Scripts) != 2 {
		t.Fatalf("must validate both user and generated packages: %#v", analysis.Report)
	}
}

func TestProjectFacetPreflightPublicationFailureAndRetry(t *testing.T) {
	for _, failurePath := range []string{".loom/agents/project.md", ".loom/contracts/repos.yaml", ".loom/project.yaml"} {
		t.Run(strings.ReplaceAll(failurePath, "/", "_"), func(t *testing.T) {
			scaffold, err := ScaffoldProject(ScaffoldOptions{Name: "Publication Retry", OwnerNode: "main", Preset: PresetMinimal, Directory: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			before := facetPreflightTree(t, scaffold.ProjectRoot)
			result, err := addProjectFacets(AddProjectFacetsOptions{ProjectRoot: scaffold.ProjectRoot, Facets: []string{"repos"}}, scaffoldCandidateHooks{beforeWrite: func(path string) error {
				if path == failurePath {
					return fmt.Errorf("injected publication failure")
				}
				return nil
			}})
			if err == nil || result.OK {
				t.Fatalf("failure not returned: %#v %v", result, err)
			}
			if actionForFile(result.Files, failurePath) != "failed" {
				t.Fatalf("failed path missing from result: %#v", result.Files)
			}
			after := facetPreflightTree(t, scaffold.ProjectRoot)
			for _, file := range result.Files {
				switch file.Action {
				case "created", "updated":
					if before[file.Path] == after[file.Path] {
						t.Fatalf("reported effect did not occur: %#v", file)
					}
				case "failed", "planned", "planned_update", "skipped":
					if before[file.Path] != after[file.Path] {
						t.Fatalf("unreported write: %#v", file)
					}
				default:
					t.Fatalf("unexpected action %#v", file)
				}
			}
			if failurePath == ".loom/agents/project.md" && !reflect.DeepEqual(before, after) {
				t.Fatal("first-write failure changed source")
			}
			retry, err := AddProjectFacets(AddProjectFacetsOptions{ProjectRoot: scaffold.ProjectRoot, Facets: []string{"repos"}})
			if err != nil || !retry.OK || !Analyze(scaffold.ProjectRoot).Report.OK {
				t.Fatalf("retry must replan partial state: %#v %v", retry.Validation, err)
			}
			stable := facetPreflightTree(t, scaffold.ProjectRoot)
			replay, err := AddProjectFacets(AddProjectFacetsOptions{ProjectRoot: scaffold.ProjectRoot, Facets: []string{"repos"}})
			if err != nil || !replay.OK || !reflect.DeepEqual(stable, facetPreflightTree(t, scaffold.ProjectRoot)) {
				t.Fatalf("replay changed source: %v", err)
			}
		})
	}
}

func TestProjectFacetPreflightValidationFailureWritesNothing(t *testing.T) {
	for _, dryRun := range []bool{true, false} {
		t.Run(fmt.Sprintf("dry_run_%t", dryRun), func(t *testing.T) {
			scaffold, err := ScaffoldProject(ScaffoldOptions{Name: "Validation Injection", OwnerNode: "main", Preset: PresetMinimal, Directory: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			before := facetPreflightTree(t, scaffold.ProjectRoot)
			result, err := addProjectFacets(AddProjectFacetsOptions{ProjectRoot: scaffold.ProjectRoot, Facets: []string{"repos"}, DryRun: dryRun}, scaffoldCandidateHooks{beforeValidation: func(root string) error {
				return os.WriteFile(filepath.Join(root, ".loom/contracts/repos.yaml"), []byte("kind: invalid\n"), 0o600)
			}})
			if err == nil || result.OK || result.Validation.OK || !reflect.DeepEqual(before, facetPreflightTree(t, scaffold.ProjectRoot)) {
				t.Fatalf("invalid candidate must fail without source writes: %#v %v", result.Validation, err)
			}
		})
	}
}

func TestProjectFacetPreflightConcurrentSourceEdit(t *testing.T) {
	for _, atWrite := range []bool{false, true} {
		t.Run(fmt.Sprintf("during_publication_%t", atWrite), func(t *testing.T) {
			scaffold, err := ScaffoldProject(ScaffoldOptions{Name: "Concurrent Edit", OwnerNode: "main", Preset: PresetMinimal, Directory: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			edit := func() error {
				return os.WriteFile(filepath.Join(scaffold.ProjectRoot, ".loom/contracts/backup.yaml"), []byte("kind: user.edited\n"), 0o600)
			}
			hooks := scaffoldCandidateHooks{beforePublication: edit}
			if atWrite {
				hooks = scaffoldCandidateHooks{beforeWrite: func(path string) error {
					if path == ".loom/contracts/repos.yaml" {
						return edit()
					}
					return nil
				}}
			}
			result, err := addProjectFacets(AddProjectFacetsOptions{ProjectRoot: scaffold.ProjectRoot, Facets: []string{"repos"}}, hooks)
			if err == nil || result.OK || !strings.Contains(err.Error(), "source changed") {
				t.Fatalf("source edit must refuse: %#v %v", result.Validation, err)
			}
			if got := readFile(t, scaffold.ProjectRoot, ".loom/contracts/backup.yaml"); got != "kind: user.edited\n" {
				t.Fatalf("concurrent edit overwritten: %s", got)
			}
			beforeRetry := facetPreflightTree(t, scaffold.ProjectRoot)
			_, err = AddProjectFacets(AddProjectFacetsOptions{ProjectRoot: scaffold.ProjectRoot, Facets: []string{"repos"}, Force: true})
			if err == nil || !reflect.DeepEqual(beforeRetry, facetPreflightTree(t, scaffold.ProjectRoot)) {
				t.Fatal("retry must preserve/refuse conflicting user policy even with force")
			}
		})
	}
}

func TestProjectFacetPreflightGeneratedPackagePublicationRetry(t *testing.T) {
	scaffold, err := ScaffoldProject(ScaffoldOptions{Name: "Package Retry", OwnerNode: "main", Facets: []string{"docs"}, Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	result, err := addProjectFacets(AddProjectFacetsOptions{ProjectRoot: scaffold.ProjectRoot, Facets: []string{"scripts"}}, scaffoldCandidateHooks{beforeWrite: func(path string) error {
		if path == ".loom/project.yaml" {
			return fmt.Errorf("late failure")
		}
		return nil
	}})
	if err == nil || result.OK {
		t.Fatal("expected late failure")
	}
	for _, file := range result.Files {
		if strings.HasPrefix(file.Path, "scripts/hello_world/") && file.Action != "created" {
			t.Fatalf("generated package not published completely: %#v", file)
		}
	}
	if err := os.WriteFile(filepath.Join(scaffold.ProjectRoot, "scripts/hello_world/ancillary.txt"), []byte("user addition"), 0o600); err != nil {
		t.Fatal(err)
	}
	retry, err := AddProjectFacets(AddProjectFacetsOptions{ProjectRoot: scaffold.ProjectRoot, Facets: []string{"scripts"}})
	if err != nil || !retry.OK {
		t.Fatalf("retry must preserve complete package and ancillary data: %v", err)
	}
	if got := readFile(t, scaffold.ProjectRoot, "scripts/hello_world/ancillary.txt"); got != "user addition" {
		t.Fatal("ancillary data changed")
	}
}

func TestProjectFacetPreflightMetadataOnlyAndGuidance(t *testing.T) {
	scaffold, err := ScaffoldProject(ScaffoldOptions{Name: "Metadata Only", OwnerNode: "main", Preset: PresetResearch, Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(scaffold.ProjectRoot, "notes/payload/deep"), 0o700); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(scaffold.ProjectRoot, "notes/payload/deep/user.bin")
	if err := os.WriteFile(payload, []byte("never copied to candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scaffold.ProjectRoot, "AGENTS.md"), []byte("User instructions\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(scaffold.ProjectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(scaffold.ProjectRoot, ".loom"), 0o700); err != nil {
		t.Fatal(err)
	}
	before := facetPreflightTree(t, scaffold.ProjectRoot)
	var temporary string
	preview, err := addProjectFacets(AddProjectFacetsOptions{ProjectRoot: scaffold.ProjectRoot, Facets: []string{"repos"}, DryRun: true, Force: true}, scaffoldCandidateHooks{beforeValidation: func(root string) error {
		temporary = root
		if _, err := os.Lstat(filepath.Join(root, "notes/payload")); !os.IsNotExist(err) {
			t.Fatal("payload tree entered candidate")
		}
		return nil
	}})
	if err != nil || !preview.Validation.OK || actionForFile(preview.Files, ".loom/contracts/backup.yaml") != "planned_update" {
		t.Fatalf("complete dependency plan: %#v %v", preview, err)
	}
	if !reflect.DeepEqual(before, facetPreflightTree(t, scaffold.ProjectRoot)) {
		t.Fatal("preview changed source")
	}
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatal("owned temporary candidate was not removed")
	}
	result, err := AddProjectFacets(AddProjectFacetsOptions{ProjectRoot: scaffold.ProjectRoot, Facets: []string{"repos"}, Force: true})
	if err != nil || !result.OK {
		t.Fatalf("apply: %v", err)
	}
	if readFile(t, scaffold.ProjectRoot, "AGENTS.md") != "User instructions\n" {
		t.Fatal("custom guidance overwritten by force")
	}
	if !strings.Contains(readFile(t, scaffold.ProjectRoot, "README.md"), "Preset: `research`") || !strings.Contains(readFile(t, scaffold.ProjectRoot, ".loom/agents/project.md"), "notes, repos, docs") {
		t.Fatal("generated guidance does not match new facets or preserved preset")
	}
	for _, relative := range []string{".", ".loom", "notes", "notes/payload", "notes/payload/deep"} {
		info, err := os.Stat(filepath.Join(scaffold.ProjectRoot, relative))
		if err != nil || before[relative] != info.Mode().String() {
			t.Fatalf("existing directory mode changed: %s %v", relative, err)
		}
	}
	if got := readFile(t, scaffold.ProjectRoot, "notes/payload/deep/user.bin"); got != "never copied to candidate" {
		t.Fatal("payload changed")
	}
}

func TestProjectFacetPreflightPathEscapeRefusesBeforeWrites(t *testing.T) {
	for _, target := range []string{"repos", ".loom/contracts/repos.yaml"} {
		t.Run(strings.ReplaceAll(target, "/", "_"), func(t *testing.T) {
			scaffold, err := ScaffoldProject(ScaffoldOptions{Name: "Source Escape", OwnerNode: "main", Preset: PresetMinimal, Directory: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			if err := os.Symlink(outside, filepath.Join(scaffold.ProjectRoot, target)); err != nil {
				t.Fatal(err)
			}
			before := readFile(t, scaffold.ProjectRoot, CanonicalRootContractPath)
			result, err := AddProjectFacets(AddProjectFacetsOptions{ProjectRoot: scaffold.ProjectRoot, Facets: []string{"repos"}, Force: true})
			if err == nil || result.OK || readFile(t, scaffold.ProjectRoot, CanonicalRootContractPath) != before {
				t.Fatalf("path escape did not refuse before writes: %v", err)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatal("outside path changed")
			}
		})
	}
}

func TestProjectFacetPreflightBootstrapCompleteAndRetry(t *testing.T) {
	root := filepath.Join(t.TempDir(), "fresh-project")
	options := AddProjectFacetsOptions{ProjectRoot: root, Facets: []string{"scripts"}, BootstrapMissing: true, DryRun: true}
	preview, err := AddProjectFacets(options)
	if err != nil || !preview.Validation.OK || preview.Validation.State != ScaffoldValidationPassed {
		t.Fatalf("bootstrap preview not validated: %#v %v", preview.Validation, err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("bootstrap preview wrote source")
	}
	options.DryRun = false
	failed, err := addProjectFacets(options, scaffoldCandidateHooks{beforeWrite: func(path string) error {
		if path == CanonicalRootContractPath {
			return fmt.Errorf("late bootstrap failure")
		}
		return nil
	}})
	if err == nil || failed.OK || actionForFile(failed.Files, CanonicalRootContractPath) != "failed" {
		t.Fatalf("expected truthful bootstrap failure: %v", err)
	}
	result, err := AddProjectFacets(options)
	if err != nil || !result.OK || !Analyze(root).Report.OK {
		t.Fatalf("bootstrap retry should replan complete packages: %#v %v", result.Validation, err)
	}
}

func TestProjectFacetPreflightConcurrentPackageEdit(t *testing.T) {
	scaffold, err := ScaffoldProject(ScaffoldOptions{Name: "Package Edit", OwnerNode: "main", Facets: []string{"scripts"}, Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(scaffold.ProjectRoot, "scripts/hello_world/run.sh")
	_, err = addProjectFacets(AddProjectFacetsOptions{ProjectRoot: scaffold.ProjectRoot, Facets: []string{"docs"}}, scaffoldCandidateHooks{beforePublication: func() error { return os.WriteFile(path, []byte("#!/bin/sh\nprintf user-edit\n"), 0o755) }})
	if err == nil || !strings.Contains(err.Error(), "package or declaration") {
		t.Fatalf("package hash change should invalidate candidate: %v", err)
	}
	loaded, err := LoadProject(scaffold.ProjectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Contract.Facets["docs"] {
		t.Fatal("published root despite source package edit")
	}
}

func TestProjectFacetPreflightUnselectedOrdinaryFiles(t *testing.T) {
	scaffold, err := ScaffoldProject(ScaffoldOptions{Name: "Ordinary Files", OwnerNode: "main", Preset: PresetMinimal, Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"scripts", "docs", "workflows"} {
		if err := os.WriteFile(filepath.Join(scaffold.ProjectRoot, name), []byte("ordinary user file"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := AddProjectFacets(AddProjectFacetsOptions{ProjectRoot: scaffold.ProjectRoot, Facets: []string{"repos"}})
	if err != nil || !result.OK {
		t.Fatalf("unselected ordinary paths should not be interpreted as facets: %v", err)
	}
	for _, name := range []string{"scripts", "docs", "workflows"} {
		if got := readFile(t, scaffold.ProjectRoot, name); got != "ordinary user file" {
			t.Fatalf("ordinary file changed: %s", name)
		}
	}
}

func TestProjectFacetPreflightResolvedCustomPolicyPath(t *testing.T) {
	scaffold, err := ScaffoldProject(ScaffoldOptions{Name: "Explicit Policy", OwnerNode: "main", Preset: PresetMinimal, Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	customPath := "settings/protection.yaml"
	if err := os.Mkdir(filepath.Join(scaffold.ProjectRoot, "settings"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(scaffold.ProjectRoot, ".loom/contracts/backup.yaml"), filepath.Join(scaffold.ProjectRoot, customPath)); err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(scaffold.ProjectRoot, CanonicalRootContractPath)
	raw, err := os.ReadFile(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), ".loom/contracts/backup.yaml") {
		t.Fatal("fixture lacks backup reference")
	}
	if err := os.WriteFile(rootPath, []byte(strings.ReplaceAll(string(raw), ".loom/contracts/backup.yaml", customPath)), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := AddProjectFacets(AddProjectFacetsOptions{ProjectRoot: scaffold.ProjectRoot, Facets: []string{"repos"}})
	if err != nil || !result.OK || actionForFile(result.Files, customPath) != "updated" {
		t.Fatalf("generated policy should keep its explicit path: %#v %v", result.Files, err)
	}
	if _, err := os.Stat(filepath.Join(scaffold.ProjectRoot, ".loom/contracts/backup.yaml")); !os.IsNotExist(err) {
		t.Fatal("created an unreferenced canonical policy")
	}
	if !strings.Contains(readFile(t, scaffold.ProjectRoot, CanonicalRootContractPath), customPath) {
		t.Fatal("custom policy reference changed")
	}
}

func TestProjectFacetPreflightRepairsEarlierGeneratedPartialState(t *testing.T) {
	scaffold, err := ScaffoldProject(ScaffoldOptions{Name: "Earlier Partial", OwnerNode: "main", Preset: PresetMinimal, Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadProject(scaffold.ProjectRoot)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Contract.Facets["repos"] = true
	root, _, err := projectContractScaffoldFile(loaded.Contract, loaded.Raw, CanonicalRootContractPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(loaded.ContractPath, root.Content, 0o600); err != nil {
		t.Fatal(err)
	}
	// Before this repair, the root was published with repos enabled while the
	// still-untouched generated backup policy retained only Notes coverage.
	result, err := AddProjectFacets(AddProjectFacetsOptions{ProjectRoot: scaffold.ProjectRoot, Facets: []string{"repos"}})
	if err != nil || !result.OK || actionForFile(result.Files, ".loom/contracts/backup.yaml") != "updated" || !Analyze(scaffold.ProjectRoot).Report.OK {
		t.Fatalf("recognizable generated partial state should be recoverable: %#v %v", result.Files, err)
	}
}
