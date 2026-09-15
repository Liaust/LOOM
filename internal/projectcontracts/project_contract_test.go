package projectcontracts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnalyzeValidContractDerivesProjectProvider(t *testing.T) {
	root := projectFixture(t, `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: gmail-automation
  name: Gmail Automation
  owner_node: macbook
facets:
  notes: true
  scripts: true
  direct_events: true
`)
	mkdir(t, root, "notes")
	mkdir(t, root, "scripts")
	mkdir(t, root, "direct_events")

	analysis := Analyze(root)
	if !analysis.Report.OK {
		t.Fatalf("expected report ok, diagnostics=%#v", analysis.Report.Diagnostics)
	}
	if analysis.Report.Summary.Errors != 0 || analysis.Report.Summary.Warnings != 3 {
		t.Fatalf("unexpected diagnostics summary: %#v diagnostics=%#v", analysis.Report.Summary, analysis.Report.Diagnostics)
	}
	assertDiagnostic(t, analysis.Report.Diagnostics, "notes.contract_missing")
	assertDiagnostic(t, analysis.Report.Diagnostics, "script.none_found")
	assertDiagnostic(t, analysis.Report.Diagnostics, "direct_event.none_found")
	if got := analysis.Report.DerivedProviders[0].CompactAddress; got != "macbook@gmail-automation" {
		t.Fatalf("derived provider = %q, want macbook@gmail-automation", got)
	}
	if !analysis.Plan.Registerable {
		t.Fatal("plan should be registerable")
	}
	if len(analysis.Plan.Actions) == 0 {
		t.Fatal("plan should include read-only actions")
	}
}

func TestAnalyzeRejectsInvalidSlug(t *testing.T) {
	root := projectFixture(t, `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: Gmail Automation
  name: Gmail Automation
  owner_node: macbook
`)
	analysis := Analyze(root)
	if analysis.Report.OK {
		t.Fatal("expected invalid slug to fail validation")
	}
	assertDiagnostic(t, analysis.Report.Diagnostics, "project.slug_invalid")
}

func TestAnalyzeRejectsInvalidKindAndSchema(t *testing.T) {
	root := projectFixture(t, `
kind: loom.module
schema_version: project.contract.v0.1
project:
  slug: gmail-automation
  name: Gmail Automation
  owner_node: macbook
`)
	analysis := Analyze(root)
	if analysis.Report.OK {
		t.Fatal("expected invalid kind and schema to fail validation")
	}
	assertDiagnostic(t, analysis.Report.Diagnostics, "contract.kind_invalid")
	assertDiagnostic(t, analysis.Report.Diagnostics, "contract.schema_version_invalid")
}

func TestAnalyzeRejectsMissingOwnerNodeAndInvalidStatus(t *testing.T) {
	root := projectFixture(t, `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: gmail-automation
  name: Gmail Automation
  status: ready
`)
	analysis := Analyze(root)
	if analysis.Report.OK {
		t.Fatal("expected missing owner node and invalid status to fail validation")
	}
	assertDiagnostic(t, analysis.Report.Diagnostics, "project.owner_node_required")
	assertDiagnostic(t, analysis.Report.Diagnostics, "project.status_invalid")
}

func TestAnalyzeRejectsUnknownFacet(t *testing.T) {
	root := projectFixture(t, `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: gmail-automation
  name: Gmail Automation
  owner_node: macbook
facets:
  unknown_thing: true
`)
	analysis := Analyze(root)
	if analysis.Report.OK {
		t.Fatal("expected unknown facet to fail validation")
	}
	assertDiagnostic(t, analysis.Report.Diagnostics, "facet.unknown")
}

func TestAnalyzeWarnsForDisabledFacetFolderPresent(t *testing.T) {
	root := projectFixture(t, `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: gmail-automation
  name: Gmail Automation
  owner_node: macbook
facets:
  scripts: false
`)
	mkdir(t, root, "scripts")
	analysis := Analyze(root)
	if !analysis.Report.OK {
		t.Fatalf("disabled present folder should warn, not fail: %#v", analysis.Report.Diagnostics)
	}
	assertDiagnostic(t, analysis.Report.Diagnostics, "facet.folder_present_but_disabled")
}

func TestAnalyzeWarnsForEnabledFacetsWithMissingFolders(t *testing.T) {
	root := projectFixture(t, `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: gmail-automation
  name: Gmail Automation
  owner_node: macbook
facets:
  scripts: true
  workflows: true
`)
	analysis := Analyze(root)
	if !analysis.Report.OK {
		t.Fatalf("warnings should not block validation: %#v", analysis.Report.Diagnostics)
	}
	assertDiagnostic(t, analysis.Report.Diagnostics, "facet.folder_missing")
	if analysis.Report.Summary.Warnings != 2 {
		t.Fatalf("expected two missing-folder warnings, got %#v diagnostics=%#v", analysis.Report.Summary, analysis.Report.Diagnostics)
	}
}

func TestAnalyzeRejectsUnsafePolicyPath(t *testing.T) {
	root := projectFixture(t, `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: gmail-automation
  name: Gmail Automation
  owner_node: macbook
policies:
  sync: ../outside.yaml
`)
	analysis := Analyze(root)
	if analysis.Report.OK {
		t.Fatal("expected unsafe policy path to fail validation")
	}
	assertDiagnostic(t, analysis.Report.Diagnostics, "policy.path_unsafe")
}

func TestAnalyzeWarnsForMissingPolicyPath(t *testing.T) {
	root := projectFixture(t, `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: gmail-automation
  name: Gmail Automation
  owner_node: macbook
policies:
  sync: policies/sync.yaml
`)
	analysis := Analyze(root)
	if !analysis.Report.OK {
		t.Fatalf("missing policy file should warn, not fail: %#v", analysis.Report.Diagnostics)
	}
	assertDiagnostic(t, analysis.Report.Diagnostics, "policy.path_missing")
	if len(analysis.Report.PolicyRefs) != 1 || analysis.Report.PolicyRefs[0].Path != "policies/sync.yaml" {
		t.Fatalf("unexpected policy refs: %#v", analysis.Report.PolicyRefs)
	}
}

func TestAnalyzeRejectsInvalidProviderDefault(t *testing.T) {
	root := projectFixture(t, `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: gmail-automation
  name: Gmail Automation
  owner_node: macbook
provider_defaults:
  scripts_provider: bad.provider
`)
	analysis := Analyze(root)
	if analysis.Report.OK {
		t.Fatal("expected invalid provider default to fail validation")
	}
	assertDiagnostic(t, analysis.Report.Diagnostics, "provider.default_invalid")
}

func TestAnalyzeReportsMissingContract(t *testing.T) {
	root := t.TempDir()
	analysis := Analyze(root)
	if analysis.Report.OK {
		t.Fatal("expected missing contract to fail")
	}
	assertDiagnostic(t, analysis.Report.Diagnostics, "contract.read_failed")
}

func TestAnalyzeReportsParseFailure(t *testing.T) {
	root := projectFixture(t, `kind: [`)
	analysis := Analyze(root)
	if analysis.Report.OK {
		t.Fatal("expected malformed yaml to fail")
	}
	assertDiagnostic(t, analysis.Report.Diagnostics, "contract.parse_failed")
}

func TestPlanJSONIsStableAndSerializable(t *testing.T) {
	root := projectFixture(t, `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: gmail-automation
  name: Gmail Automation
  owner_node: macbook
facets:
  scripts: true
`)
	mkdir(t, root, "scripts")
	analysis := Analyze(root)
	payload, err := json.Marshal(analysis.Plan)
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	if !strings.Contains(string(payload), `"compact_address":"macbook@gmail-automation"`) {
		t.Fatalf("plan json missing provider: %s", payload)
	}
}

func TestInvalidPlanIsNotRegisterable(t *testing.T) {
	root := projectFixture(t, `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: Invalid Slug
  name: Gmail Automation
  owner_node: macbook
`)
	analysis := Analyze(root)
	if analysis.Plan.Registerable {
		t.Fatal("invalid plan should not be registerable")
	}
	if analysis.Plan.Summary.Errors == 0 {
		t.Fatalf("invalid plan should carry validation errors: %#v", analysis.Plan)
	}
}

func projectFixture(t *testing.T, contract string) string {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, filepath.FromSlash(CanonicalRootContractPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create canonical contract directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(contract)+"\n"), 0o600); err != nil {
		t.Fatalf("write contract: %v", err)
	}
	return root
}

func mkdir(t *testing.T, root, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(path)), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func assertDiagnostic(t *testing.T, diagnostics []Diagnostic, code string) {
	t.Helper()
	for _, diag := range diagnostics {
		if diag.Code == code {
			return
		}
	}
	t.Fatalf("missing diagnostic %q in %#v", code, diagnostics)
}
