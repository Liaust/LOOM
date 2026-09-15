package projectcontracts

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validLayoutContract = `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: layout-smoke
  name: Layout Smoke
  owner_node: main
`

func TestLoadProjectCanonicalLayout(t *testing.T) {
	root := t.TempDir()
	writeLayoutContract(t, root, CanonicalRootContractPath, validLayoutContract)

	loaded, err := LoadProject(root)
	if err != nil {
		t.Fatalf("LoadProject returned error: %v", err)
	}
	if loaded.Layout != ProjectLayoutCanonical {
		t.Fatalf("layout = %q, want %q", loaded.Layout, ProjectLayoutCanonical)
	}
	wantPath := filepath.Join(root, filepath.FromSlash(CanonicalRootContractPath))
	if loaded.ContractPath != wantPath || !loaded.Discovery.CanonicalPresent || loaded.Discovery.LegacyPresent {
		t.Fatalf("unexpected discovery: loaded=%#v", loaded)
	}
}

func TestLoadProjectLegacyLayoutWarns(t *testing.T) {
	root := t.TempDir()
	writeLayoutContract(t, root, LegacyRootContractPath, validLayoutContract)

	analysis := Analyze(root)
	if analysis.Loaded == nil || analysis.Loaded.Layout != ProjectLayoutLegacy {
		t.Fatalf("unexpected loaded layout: %#v", analysis.Loaded)
	}
	if !analysis.Report.OK {
		t.Fatalf("legacy layout warning should not block validation: %#v", analysis.Report.Diagnostics)
	}
	assertDiagnostic(t, analysis.Report.Diagnostics, "contract.layout_legacy")
}

func TestLoadProjectEquivalentDuplicatePrefersCanonical(t *testing.T) {
	root := t.TempDir()
	writeLayoutContract(t, root, CanonicalRootContractPath, validLayoutContract)
	writeLayoutContract(t, root, LegacyRootContractPath, `
# Same contract with a different field order and an explicit normalized status.
schema_version: project.contract.v0.3
kind: loom.project
project:
  owner_node: main
  status: draft
  name: Layout Smoke
  slug: layout-smoke
`)

	analysis := Analyze(root)
	if analysis.Loaded == nil || analysis.Loaded.Layout != ProjectLayoutCanonicalWithLegacy {
		t.Fatalf("unexpected loaded layout: %#v diagnostics=%#v", analysis.Loaded, analysis.Report.Diagnostics)
	}
	wantPath := filepath.Join(root, filepath.FromSlash(CanonicalRootContractPath))
	if analysis.Loaded.ContractPath != wantPath {
		t.Fatalf("contract path = %q, want %q", analysis.Loaded.ContractPath, wantPath)
	}
	assertDiagnostic(t, analysis.Report.Diagnostics, "contract.layout_duplicate")
}

func TestLoadProjectDivergentDuplicateFailsClosed(t *testing.T) {
	root := t.TempDir()
	writeLayoutContract(t, root, CanonicalRootContractPath, validLayoutContract)
	writeLayoutContract(t, root, LegacyRootContractPath, strings.Replace(validLayoutContract, "Layout Smoke", "Different Project", 1))

	_, err := LoadProject(root)
	if err == nil {
		t.Fatal("expected layout conflict")
	}
	var loadErr LoadError
	if !errors.As(err, &loadErr) || loadErr.Code != "contract.layout_conflict" {
		t.Fatalf("error = %#v, want contract.layout_conflict", err)
	}
	analysis := Analyze(root)
	assertDiagnostic(t, analysis.Report.Diagnostics, "contract.layout_conflict")
}

func TestLoadProjectMissingNamesBothSupportedPaths(t *testing.T) {
	analysis := Analyze(t.TempDir())
	assertDiagnostic(t, analysis.Report.Diagnostics, "contract.read_failed")
	message := analysis.Report.Diagnostics[0].Message
	if !strings.Contains(message, CanonicalRootContractPath) || !strings.Contains(message, LegacyRootContractPath) {
		t.Fatalf("missing-contract diagnostic does not name both paths: %q", message)
	}
}

func writeLayoutContract(t *testing.T, root, relativePath, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create contract directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", relativePath, err)
	}
}
