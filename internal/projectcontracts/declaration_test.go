package projectcontracts

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func declarationTestRoot(t *testing.T, raw []byte) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".loom"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, CanonicalRootContractPath), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestDeclarationCompilerStrictSources(t *testing.T) {
	raw := fixtureRead(t, "minimal.yaml")
	t.Run("valid", func(t *testing.T) {
		loaded, err := LoadProject(declarationTestRoot(t, raw))
		if err != nil {
			t.Fatal(err)
		}
		if string(loaded.Raw) != string(raw) {
			t.Fatal("source bytes changed")
		}
	})
	for name, source := range map[string]string{
		"duplicate":              string(raw) + "resources: {}\n",
		"duplicate_escaped_json": `{"kind":"loom.project","schema_version":"project.contract.v0.5","project":{"id":"project_01ARZ3NDEKTSV4RRFFQ69G5FAV","slug":"plain-project","name":"Plain project","owner_node":"main"},"resources":{},"resourc\u0065s":{}}`,
		"unknown":                string(raw) + "facets: {}\n",
		"null":                   strings.Replace(string(raw), "resources: {}", "resources: null", 1),
		"trailing":               string(raw) + "---\n{}\n",
		"scalar":                 strings.Replace(string(raw), "name: Plain project", "name: 123", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadProject(declarationTestRoot(t, []byte(source))); err == nil {
				t.Fatal("invalid source accepted")
			}
		})
	}
	var corpus struct {
		Cases []struct {
			Name     string
			Document json.RawMessage
		}
	}
	if err := json.Unmarshal(fixtureRead(t, "invalid.json"), &corpus); err != nil {
		t.Fatal(err)
	}
	for _, c := range corpus.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if _, err := LoadProject(declarationTestRoot(t, c.Document)); err == nil {
				t.Fatal("frozen invalid source accepted")
			}
		})
	}
}

func TestDeclarationCompilerDualRootConflict(t *testing.T) {
	raw := fixtureRead(t, "minimal.yaml")
	root := declarationTestRoot(t, raw)
	other := strings.Replace(string(raw), "resources: {}", "resources:\n  reading:\n    kind: knowledge\n    knowledge: {path: material, category: notes}", 1)
	if err := os.WriteFile(filepath.Join(root, LegacyRootContractPath), []byte(other), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProject(root); err == nil || !strings.Contains(err.Error(), "layout conflict") {
		t.Fatalf("complete declaration comparison: %v", err)
	}
}

func TestDeclarationCompilerCapturesPreparedArtifact(t *testing.T) {
	root := declarationWebDAVRoot(t)
	file := filepath.Join(root, CanonicalRootContractPath)
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	source := strings.Replace(string(raw), "manifest: ", "artifact_descriptor: .loom/applications/artifact.json\n      manifest: ", 1)
	if err := os.WriteFile(file, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".loom/applications/artifact.json")
	if err := os.WriteFile(path, []byte(`{"prepared":"one"}`), 0600); err != nil {
		t.Fatal(err)
	}
	first := Analyze(root)
	if !first.Report.OK || first.Plan.Declaration == nil {
		t.Fatalf("compile: %+v", first.Report.Diagnostics)
	}
	captured := func(a Analysis) string {
		for _, s := range a.Plan.Declaration.Sources {
			if s.Ref == ".loom/applications/artifact.json" {
				if s.SchemaVersion != "application.artifact.v1" {
					t.Fatal(s)
				}
				return s.Hash
			}
		}
		t.Fatal("descriptor not captured")
		return ""
	}
	before := captured(first)
	if err := os.WriteFile(path, []byte(`{"prepared":"two"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if captured(Analyze(root)) == before {
		t.Fatal("changed descriptor retained source digest")
	}
	for _, ref := range []string{"../artifact.json", "/tmp/artifact.json", ".loom/applications/webdav.yaml"} {
		changed := strings.Replace(source, "artifact_descriptor: .loom/applications/artifact.json", "artifact_descriptor: "+ref, 1)
		if _, err := LoadProject(declarationTestRoot(t, []byte(changed))); err == nil {
			t.Fatal("unsafe/aliased descriptor reference accepted", ref)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if a := Analyze(root); a.Report.OK {
		t.Fatal("missing descriptor accepted")
	}
}

func declarationWebDAVRoot(t *testing.T) string {
	t.Helper()
	root := declarationTestRoot(t, fixtureRead(t, "webdav.yaml"))
	for _, p := range []string{"repos/webdav", "material", "data/library", ".loom/policies", ".loom/applications"} {
		if err := os.MkdirAll(filepath.Join(root, p), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for p, name := range map[string]string{".loom/policies/backup.yaml": "backup.yaml", ".loom/applications/webdav.yaml": "webdav-application.fixture.txt"} {
		if err := os.WriteFile(filepath.Join(root, p), declarationDependency(t, name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestDeclarationCompilerPreservesIntentAndSource(t *testing.T) {
	root := declarationWebDAVRoot(t)
	a := Analyze(root)
	if !a.Report.OK || !a.Report.Registerable {
		t.Fatalf("compile: %+v", a.Report.Diagnostics)
	}
	c := a.Plan.Declaration
	if c == nil || len(c.Document.Resources) != 4 || len(c.Sources) != 3 || len(c.Repositories) != 1 || c.Repositories[0].ID != "" || c.Repositories[0].Path != "repos/webdav" {
		t.Fatalf("lost intent: %+v", c)
	}
	if len(a.Report.RepositoryMembers) != 0 || len(a.Report.Facets) != 0 {
		t.Fatal("declaration became legacy enrollment")
	}
	if len(a.Report.WatchedRoots) != 2 || a.Report.WatchedRoots[0].Key != "reading" || a.Report.WatchedRoots[0].RootRelativePath != "material" || a.Report.WatchedRoots[0].BackupMode != "none" || a.Report.WatchedRoots[1].Key != "webdav_library" || a.Report.WatchedRoots[1].RootRelativePath != "data/library" {
		t.Fatalf("explicit enrollment: %+v", a.Report.WatchedRoots)
	}
	if len(c.Errors) != 0 {
		t.Fatalf("source compilation: %+v", c.Errors)
	}
	if len(a.Plan.UnsupportedFeatures) != 0 {
		t.Fatal("application source incorrectly rejected before owner resolution")
	}
	for _, action := range a.Plan.Actions {
		if action.TargetKind == "application" && action.Status == "applied" {
			t.Fatal("application claimed applied")
		}
	}
	for _, s := range c.Sources {
		if s.Hash != declarationHash(s.Raw) || s.Revision != s.Hash {
			t.Fatal("unbound source")
		}
	}
	if len(c.Protection) != 1 || len(c.Protection[0].WatchedRoots) != 1 || c.Protection[0].Roots[0].Key != "webdav_library" {
		t.Fatalf("selection: %+v", c.Protection)
	}
	// Mutating a plan's typed payload must not alter the validation report.
	r := c.Document.Resources["code"]
	r.Repository.Path = "other"
	if a.Report.Declaration.Document.Resources["code"].Repository.Path != "repos/webdav" {
		t.Fatal("plan aliases report")
	}
}

func TestDeclarationCompilerPolicySelection(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprint("disabled_", disabled), func(t *testing.T) {
			root := declarationWebDAVRoot(t)
			raw := string(fixtureRead(t, "backup.yaml")) + "    - key: dormant\n      path: should-not-enroll\n"
			raw = strings.Replace(raw, "  roots:", "  defaults:\n    max_batch_bytes: 33554432\n    max_pending_items: 80\n    max_pending_bytes: 67108864\n    include_deletion_markers: false\n    exclude: ['**/*.cache']\n  roots:", 1)
			if disabled {
				raw = strings.Replace(raw, "enabled: true", "enabled: false", 1)
			}
			if err := os.WriteFile(filepath.Join(root, ".loom/policies/backup.yaml"), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			a := Analyze(root)
			if !a.Report.OK {
				t.Fatalf("compile: %+v", a.Report.Diagnostics)
			}
			s := a.Plan.Declaration.Protection[0]
			if s.Enabled == disabled || len(s.Roots) != 1 || s.Roots[0].Path != "data/library" || s.Roots[0].MaxBatchBytes != 33554432 || s.Roots[0].MaxPendingItems != 80 || s.Roots[0].MaxPendingBytes != 67108864 || *s.Roots[0].IncludeDeletionMarkers {
				t.Fatalf("limits/disabled: %+v", s)
			}
			if disabled && len(s.WatchedRoots) != 0 {
				t.Fatal("disabled source generated enabled config")
			}
			for _, snapshot := range a.Plan.Declaration.Sources {
				if snapshot.Ref == s.PolicyRef && (string(snapshot.Raw) != raw || snapshot.SchemaVersion != BackupPolicySchemaV03) {
					t.Fatal("policy bytes lost")
				}
			}
		})
	}
}

func TestDeclarationCompilerPolicyNegatives(t *testing.T) {
	for name, transform := range map[string]func(string) string{
		"no_exact_match":  func(s string) string { return strings.Replace(s, "path: data/library", "path: data", 1) },
		"duplicate_match": func(s string) string { return s + "    - key: other\n      path: data/library\n" },
		"wrong_schema":    func(s string) string { return strings.Replace(s, BackupPolicySchemaV03, "backup.policy.v0.9", 1) },
		"unknown":         func(s string) string { return s + "not_a_policy: true\n" },
		"trailing":        func(s string) string { return s + "---\n{}\n" },
		"duplicate":       func(s string) string { return s + "backup: {}\n" },
		"bad_pattern":     func(s string) string { return s + "      include: ['../escape']\n" },
		"negative_limit":  func(s string) string { return strings.Replace(s, "16777216", "-1", 1) },
	} {
		t.Run(name, func(t *testing.T) {
			root := declarationWebDAVRoot(t)
			raw := transform(string(declarationDependency(t, "backup.yaml")))
			if err := os.WriteFile(filepath.Join(root, ".loom/policies/backup.yaml"), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if a := Analyze(root); a.Report.OK {
				t.Fatal("invalid policy accepted")
			}
		})
	}
}

func TestDeclarationCompilerPhysicalCustody(t *testing.T) {
	for _, relative := range []string{"material", ".loom/policies/backup.yaml", ".loom/applications/webdav.yaml"} {
		t.Run(relative, func(t *testing.T) {
			root := declarationWebDAVRoot(t)
			target := filepath.Join(root, relative)
			if err := os.Remove(target); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			if strings.HasSuffix(relative, ".yaml") {
				outside = filepath.Join(outside, "source")
				if err := os.WriteFile(outside, fixtureRead(t, "backup.yaml"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(outside, target); err != nil {
				t.Fatal(err)
			}
			if a := Analyze(root); a.Report.OK {
				t.Fatal("symlink source/target accepted")
			}
		})
	}
	root := declarationWebDAVRoot(t)
	loaded, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(loaded.ContractPath, append(loaded.Raw, []byte("# changed\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if Validate(loaded).OK {
		t.Fatal("changed root source accepted")
	}
}

func TestDeclarationCompilerInertFoldersAndExplicitPaths(t *testing.T) {
	raw := strings.Replace(string(fixtureRead(t, "minimal.yaml")), "resources: {}", "resources:\n  reading:\n    kind: knowledge\n    knowledge: {path: unusual/narratives, category: research}\n  code:\n    kind: repository\n    repository: {path: source-code, role: primary}", 1)
	root := declarationTestRoot(t, []byte(raw))
	for _, p := range []string{"unusual/narratives", "source-code", "services", "scripts", "repos", "notes", "topics"} {
		if err := os.MkdirAll(filepath.Join(root, p), 0700); err != nil {
			t.Fatal(err)
		}
	}
	a := Analyze(root)
	if !a.Report.OK || a.Report.Summary.Warnings != 0 {
		t.Fatalf("explicit paths or ordinary folders: %+v", a.Report.Diagnostics)
	}
	if len(a.Plan.Declaration.Document.Resources) != 2 || len(a.Plan.Declaration.Repositories) != 1 || len(a.Report.Repos) != 0 || len(a.Report.Scripts) != 0 || len(a.Report.Services) != 0 {
		t.Fatal("implicit facet scan")
	}
}

func declarationDependency(t *testing.T, name string) []byte {
	t.Helper()
	if name != "backup.yaml" {
		return fixtureRead(t, name)
	}
	raw, err := os.ReadFile("testdata/declaration_compiler/backup.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDeclarationCompilerSelectedSingletonConflict(t *testing.T) {
	root := declarationWebDAVRoot(t)
	sourcePath := filepath.Join(root, CanonicalRootContractPath)
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), ".loom/policies/backup.yaml", ".loom/contracts/backup.yaml", 1))
	if err := os.WriteFile(sourcePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".loom/contracts", "policies"} {
		if err := os.MkdirAll(filepath.Join(root, p), 0700); err != nil {
			t.Fatal(err)
		}
	}
	policy := declarationDependency(t, "backup.yaml")
	if err := os.WriteFile(filepath.Join(root, ".loom/contracts/backup.yaml"), policy, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "policies/backup.yaml"), []byte(strings.Replace(string(policy), "enabled: true", "enabled: false", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if Analyze(root).Report.OK {
		t.Fatal("conflicting singleton layout accepted")
	}
}

func TestDeclarationCompilerNoUnselectedPolicyReads(t *testing.T) {
	root := declarationWebDAVRoot(t)
	if err := os.MkdirAll(filepath.Join(root, ".loom/contracts"), 0700); err != nil {
		t.Fatal(err)
	}
	// A directory at the unrelated conventional address cannot affect this
	// explicitly selected custom policy. Resolve only the declaration's source.
	if err := os.Mkdir(filepath.Join(root, ".loom/contracts/backup.yaml"), 0700); err != nil {
		t.Fatal(err)
	}
	if a := Analyze(root); !a.Report.OK {
		t.Fatalf("unselected policy was read: %+v", a.Report.Diagnostics)
	}
}

func TestDeclarationCompilerDuplicateProtectionCompilesOnce(t *testing.T) {
	root := declarationWebDAVRoot(t)
	sourcePath := filepath.Join(root, CanonicalRootContractPath)
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte("  duplicate:\n    kind: protection\n    protection: {policy_ref: .loom/policies/backup.yaml, path: data/library}\n")...)
	if err := os.WriteFile(sourcePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	a := Analyze(root)
	if !a.Report.OK {
		t.Fatalf("compile: %+v", a.Report.Diagnostics)
	}
	roots := 0
	for _, selection := range a.Plan.Declaration.Protection {
		roots += len(selection.WatchedRoots)
	}
	if roots != 1 {
		t.Fatalf("duplicate effective coverage compiled %d times", roots)
	}
}

func TestDeclarationCompilerRejectsLateAlternateSource(t *testing.T) {
	root := declarationTestRoot(t, fixtureRead(t, "minimal.yaml"))
	loaded, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, LegacyRootContractPath), loaded.Raw, 0600); err != nil {
		t.Fatal(err)
	}
	if Validate(loaded).OK {
		t.Fatal("late root source was not bound")
	}
}

func TestDeclarationCompilerRejectsCrossPolicyOwnerKeyConflict(t *testing.T) {
	root := declarationTestRoot(t, []byte(strings.Replace(string(fixtureRead(t, "minimal.yaml")), "resources: {}", "resources:\n  first:\n    kind: protection\n    protection: {path: data/first, policy_ref: .loom/first.yaml}\n  second:\n    kind: protection\n    protection: {path: data/second, policy_ref: .loom/second.yaml}", 1)))
	for _, name := range []string{"first", "second"} {
		if err := os.MkdirAll(filepath.Join(root, "data", name), 0700); err != nil {
			t.Fatal(err)
		}
		raw := strings.Replace(string(declarationDependency(t, "backup.yaml")), "data/library", "data/"+name, 1)
		if err := os.WriteFile(filepath.Join(root, ".loom", name+".yaml"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if Analyze(root).Report.OK {
		t.Fatal("two different paths compiled to the same existing owner key")
	}
}
