package projectcontracts

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveSingletonContractCanonicalAndLegacy(t *testing.T) {
	t.Run("canonical preferred for notes", func(t *testing.T) {
		root := projectFixture(t, `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: contract-paths
  name: Contract Paths
  owner_node: main
facets:
  notes: true
`)
		writeFile(t, root, ".loom/contracts/notes.yaml", validNotesContract(), 0o600)
		loaded, err := LoadProject(root)
		if err != nil {
			t.Fatalf("LoadProject returned error: %v", err)
		}
		resolution, err := ResolveSingletonContract(loaded, ProjectContractNotes, "")
		if err != nil {
			t.Fatalf("ResolveSingletonContract returned error: %v", err)
		}
		if resolution.RelativePath != ".loom/contracts/notes.yaml" || resolution.Layout != ProjectLayoutCanonical || !resolution.Present {
			t.Fatalf("unexpected resolution: %#v", resolution)
		}
	})

	t.Run("canonical project falls back to legacy notes", func(t *testing.T) {
		root := projectFixture(t, `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: mixed-paths
  name: Mixed Paths
  owner_node: main
facets:
  notes: true
`)
		writeFile(t, root, "notes/loom.notes.yaml", validNotesContract(), 0o600)
		analysis := Analyze(root)
		if !analysis.Report.OK || len(analysis.Report.Notes) != 1 {
			t.Fatalf("mixed layout should validate: %#v", analysis.Report.Diagnostics)
		}
		if got := analysis.Report.Notes[0].ContractPath; got != filepath.ToSlash(filepath.Join(root, "notes/loom.notes.yaml")) {
			t.Fatalf("contract path = %q", got)
		}
		assertDiagnostic(t, analysis.Report.Diagnostics, "contract.path_legacy")
	})

	t.Run("legacy project defaults policies to legacy path", func(t *testing.T) {
		root := t.TempDir()
		writeLayoutContract(t, root, LegacyRootContractPath, validLayoutContract)
		writeFile(t, root, "policies/backup.yaml", validBackupPolicy("project", "."), 0o600)
		loaded, err := LoadProject(root)
		if err != nil {
			t.Fatalf("LoadProject returned error: %v", err)
		}
		resolution, err := ResolveSingletonContract(loaded, ProjectContractBackup, "")
		if err != nil {
			t.Fatalf("ResolveSingletonContract returned error: %v", err)
		}
		if resolution.RelativePath != "policies/backup.yaml" || resolution.Layout != ProjectLayoutLegacy {
			t.Fatalf("unexpected legacy resolution: %#v", resolution)
		}
	})
}

func TestResolveSingletonContractExplicitOverride(t *testing.T) {
	root := projectFixture(t, validLayoutContract)
	writeFile(t, root, "config/project-sync.yaml", validSyncPolicy("notes", "notes"), 0o600)
	loaded, err := LoadProject(root)
	if err != nil {
		t.Fatalf("LoadProject returned error: %v", err)
	}
	resolution, err := ResolveSingletonContract(loaded, ProjectContractSync, "config/project-sync.yaml")
	if err != nil {
		t.Fatalf("ResolveSingletonContract returned error: %v", err)
	}
	if !resolution.Explicit || !resolution.Present || resolution.RelativePath != "config/project-sync.yaml" {
		t.Fatalf("unexpected explicit resolution: %#v", resolution)
	}
	if _, err := ResolveSingletonContract(loaded, ProjectContractSync, "../outside.yaml"); err == nil {
		t.Fatal("expected unsafe override rejection")
	}
}

func TestResolveSingletonContractDuplicateHandling(t *testing.T) {
	root := projectFixture(t, `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: duplicate-paths
  name: Duplicate Paths
  owner_node: main
facets:
  notes: true
`)
	writeFile(t, root, ".loom/contracts/notes.yaml", validNotesContract(), 0o600)
	writeFile(t, root, "notes/loom.notes.yaml", strings.Replace(validNotesContract(), "kind: loom.notes\nschema_version: notes.contract.v0.3", "# reordered\nschema_version: notes.contract.v0.3\nkind: loom.notes", 1), 0o600)
	analysis := Analyze(root)
	if !analysis.Report.OK || len(analysis.Report.Notes) != 1 {
		t.Fatalf("equivalent duplicate should validate: %#v", analysis.Report.Diagnostics)
	}
	assertDiagnostic(t, analysis.Report.Diagnostics, "contract.singleton_layout_duplicate")

	writeFile(t, root, "notes/loom.notes.yaml", strings.Replace(validNotesContract(), "backup: false", "backup: true", 1), 0o600)
	loaded, err := LoadProject(root)
	if err != nil {
		t.Fatalf("LoadProject returned error: %v", err)
	}
	_, err = ResolveSingletonContract(loaded, ProjectContractNotes, "")
	var pathErr ContractPathError
	if !errors.As(err, &pathErr) || pathErr.Code != "contract.singleton_layout_conflict" {
		t.Fatalf("error = %#v, want singleton layout conflict", err)
	}
	analysis = Analyze(root)
	if analysis.Report.OK {
		t.Fatal("divergent duplicate should fail validation")
	}
	assertDiagnostic(t, analysis.Report.Diagnostics, "contract.singleton_layout_conflict")
}

func TestKeyedProjectContractPath(t *testing.T) {
	path, err := KeyedProjectContractPath("services", "web-api")
	if err != nil || path != ".loom/contracts/services/web-api.yaml" {
		t.Fatalf("path = %q err=%v", path, err)
	}
	for _, input := range [][2]string{{"../services", "web"}, {"services", "../web"}, {"Services", "web"}} {
		if _, err := KeyedProjectContractPath(input[0], input[1]); err == nil {
			t.Fatalf("expected invalid keyed path rejection for %#v", input)
		}
	}
}

func TestFacetAdditionUsesResolvedSingletonLayout(t *testing.T) {
	t.Run("canonical project", func(t *testing.T) {
		scaffold, err := ScaffoldProject(ScaffoldOptions{
			Name:      "Canonical Facet",
			OwnerNode: "main",
			Facets:    []string{"docs"},
			Directory: t.TempDir(),
		})
		if err != nil {
			t.Fatalf("ScaffoldProject returned error: %v", err)
		}
		if _, err := AddProjectFacets(AddProjectFacetsOptions{ProjectRoot: scaffold.ProjectRoot, Facets: []string{"notes"}}); err != nil {
			t.Fatalf("AddProjectFacets returned error: %v", err)
		}
		assertFile(t, scaffold.ProjectRoot, ".loom/contracts/notes.yaml")
		assertNoFile(t, scaffold.ProjectRoot, "notes/loom.notes.yaml")
	})

	t.Run("legacy project", func(t *testing.T) {
		root := t.TempDir()
		writeLayoutContract(t, root, LegacyRootContractPath, `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: legacy-facet
  name: Legacy Facet
  owner_node: main
facets:
  notes: true
`)
		writeFile(t, root, "notes/loom.notes.yaml", validNotesContract(), 0o600)
		if _, err := AddProjectFacets(AddProjectFacetsOptions{ProjectRoot: root, Facets: []string{"notes"}}); err != nil {
			t.Fatalf("AddProjectFacets returned error: %v", err)
		}
		assertFile(t, root, "notes/loom.notes.yaml")
		assertNoFile(t, root, ".loom/contracts/notes.yaml")
	})
}
