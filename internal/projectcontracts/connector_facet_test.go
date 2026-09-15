package projectcontracts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConnectorFacetDiscoversScriptBackedConnector(t *testing.T) {
	root := writeConnectorFacetProject(t)

	analysis := Analyze(root)
	if !analysis.Report.OK {
		t.Fatalf("analysis produced diagnostics: %#v", analysis.Report.Diagnostics)
	}
	if len(analysis.Report.Connectors) != 1 || len(analysis.Plan.Connectors) != 1 {
		t.Fatalf("connectors report=%d plan=%d", len(analysis.Report.Connectors), len(analysis.Plan.Connectors))
	}
	connector := analysis.Plan.Connectors[0]
	if connector.ProviderAddress != "main@example_connector" {
		t.Fatalf("provider address = %q", connector.ProviderAddress)
	}
	if len(connector.Capabilities) != 1 || connector.Capabilities[0].CapabilityAddress != "main@example_connector.ping" {
		t.Fatalf("connector capabilities = %#v", connector.Capabilities)
	}
	if connector.ActivationStatus != ConnectorActivationStatusPending {
		t.Fatalf("activation status = %q", connector.ActivationStatus)
	}
	actionNames := map[string]bool{}
	for _, action := range analysis.Plan.Actions {
		actionNames[action.Action] = true
	}
	for _, want := range []string{
		"would_register_connector_provider",
		"would_register_connector_script",
		"would_register_connector_capability_endpoint",
		"would_register_connector_endpoint_version",
		"would_register_connector_runtime_binding",
		"would_register_connector_usage_document",
	} {
		if !actionNames[want] {
			t.Fatalf("missing connector plan action %q in %#v", want, analysis.Plan.Actions)
		}
	}
}

func TestConnectorFacetMarksKnownUnsupportedRuntimeBlocked(t *testing.T) {
	root := writeConnectorFacetProject(t)
	manifest := filepath.Join(root, "connectors", "example_connector", "loom.connector.yaml")
	replaceConnectorTestFile(t, manifest, "kind: script", "kind: native")

	analysis := Analyze(root)
	if !analysis.Report.OK {
		t.Fatalf("known unsupported runtime should warn, not fail: %#v", analysis.Report.Diagnostics)
	}
	connector := analysis.Plan.Connectors[0]
	if connector.ActivationStatus != ConnectorActivationStatusBlocked {
		t.Fatalf("activation status = %q", connector.ActivationStatus)
	}
	found := false
	for _, action := range analysis.Plan.Actions {
		if action.Action == "would_block_unsupported_connector_runtime" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("missing unsupported runtime action: %#v", analysis.Plan.Actions)
	}
}

func TestConnectorFacetSkipsDisabledConnector(t *testing.T) {
	root := writeConnectorFacetProject(t)
	manifest := filepath.Join(root, "connectors", "example_connector", "loom.connector.yaml")
	replaceConnectorTestFile(t, manifest, "status: draft", "status: disabled")

	analysis := Analyze(root)
	if !analysis.Report.OK {
		t.Fatalf("disabled connector should not fail validation: %#v", analysis.Report.Diagnostics)
	}
	if len(analysis.Plan.Connectors) != 1 {
		t.Fatalf("connectors = %d", len(analysis.Plan.Connectors))
	}
	connector := analysis.Plan.Connectors[0]
	if connector.ActivationStatus != ConnectorActivationStatusDisabled {
		t.Fatalf("activation status = %q", connector.ActivationStatus)
	}
	if !hasConnectorPlanAction(analysis.Plan.Actions, "would_skip_disabled_connector") {
		t.Fatalf("missing disabled connector skip action: %#v", analysis.Plan.Actions)
	}
	if hasConnectorPlanAction(analysis.Plan.Actions, "would_register_connector_provider") {
		t.Fatalf("disabled connector should not plan provider registration: %#v", analysis.Plan.Actions)
	}
}

func TestConnectorFacetRejectsInvalidKindAndSchema(t *testing.T) {
	root := writeConnectorFacetProject(t)
	manifest := filepath.Join(root, "connectors", "example_connector", "loom.connector.yaml")
	replaceConnectorTestFile(t, manifest, "kind: loom.connector", "kind: loom.not_connector")
	replaceConnectorTestFile(t, manifest, "schema_version: connector.contract.v0.3", "schema_version: connector.contract.v9")

	analysis := Analyze(root)
	assertDiagnostic(t, analysis.Report.Diagnostics, "connector.kind_invalid")
	assertDiagnostic(t, analysis.Report.Diagnostics, "connector.schema_version_invalid")
}

func TestConnectorFacetRejectsInvalidProviderKey(t *testing.T) {
	root := writeConnectorFacetProject(t)
	manifest := filepath.Join(root, "connectors", "example_connector", "loom.connector.yaml")
	replaceConnectorTestFile(t, manifest, "key: example_connector", "key: project")

	analysis := Analyze(root)
	assertDiagnostic(t, analysis.Report.Diagnostics, "connector.provider_key_invalid")
}

func TestConnectorFacetRejectsDuplicateProviderKey(t *testing.T) {
	root := writeConnectorFacetProject(t)
	first := filepath.Join(root, "connectors", "example_connector")
	second := filepath.Join(root, "connectors", "second_connector")
	copyConnectorTestDir(t, first, second)

	analysis := Analyze(root)
	assertDiagnostic(t, analysis.Report.Diagnostics, "connector.provider_duplicate")
}

func TestConnectorFacetRejectsDuplicateEndpoint(t *testing.T) {
	root := writeConnectorFacetProject(t)
	manifest := filepath.Join(root, "connectors", "example_connector", "loom.connector.yaml")
	replaceConnectorTestFile(t, manifest, "usage_documents:", `  - endpoint: ping
    display_name: Ping Again
    form: job
    runtime:
      kind: script
      script: scripts/ping
    input_schema:
      type: object
    output_schema:
      type: object
usage_documents:`)

	analysis := Analyze(root)
	assertDiagnostic(t, analysis.Report.Diagnostics, "connector.endpoint_duplicate")
}

func TestConnectorFacetRejectsMissingScriptEntrypoint(t *testing.T) {
	root := writeConnectorFacetProject(t)
	runPath := filepath.Join(root, "connectors", "example_connector", "scripts", "ping", "run.sh")
	if err := os.Remove(runPath); err != nil {
		t.Fatalf("remove connector run script: %v", err)
	}

	analysis := Analyze(root)
	assertDiagnostic(t, analysis.Report.Diagnostics, "script.entrypoint_missing")
}

func TestConnectorFacetRejectsEscapingScriptPath(t *testing.T) {
	root := writeConnectorFacetProject(t)
	manifest := filepath.Join(root, "connectors", "example_connector", "loom.connector.yaml")
	replaceConnectorTestFile(t, manifest, "script: scripts/ping", "script: ../outside")

	analysis := Analyze(root)
	assertDiagnostic(t, analysis.Report.Diagnostics, "connector.runtime_script_unsafe")
}

func TestConnectorFacetWarnsOnMissingUsageDocument(t *testing.T) {
	root := writeConnectorFacetProject(t)
	readmePath := filepath.Join(root, "connectors", "example_connector", "README.md")
	if err := os.Remove(readmePath); err != nil {
		t.Fatalf("remove connector usage document: %v", err)
	}

	analysis := Analyze(root)
	if !analysis.Report.OK {
		t.Fatalf("missing usage document should warn, not fail: %#v", analysis.Report.Diagnostics)
	}
	assertDiagnostic(t, analysis.Report.Diagnostics, "connector.usage_document_missing")
}

func writeConnectorFacetProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeConnectorTestFile(t, root, "loom.project.yaml", `kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: connector-smoke
  name: Connector Smoke
  owner_node: main
facets:
  connectors: true
`, 0o600)
	connectorRoot := filepath.Join(root, "connectors", "example_connector")
	scriptRoot := filepath.Join(connectorRoot, "scripts", "ping")
	if err := os.MkdirAll(scriptRoot, 0o755); err != nil {
		t.Fatalf("mkdir connector script package: %v", err)
	}
	writeConnectorTestFile(t, connectorRoot, "loom.connector.yaml", `kind: loom.connector
schema_version: connector.contract.v0.3
provider:
  key: example_connector
  display_name: Example Connector
  type: connector
  version: 0.1.0
  status: draft
runtime:
  kind: script
  base_dir: scripts
capabilities:
  - endpoint: ping
    display_name: Ping
    form: job
    risk_level: low
    runtime:
      kind: script
      script: scripts/ping
      default_mode: wait_for_completion
      wait_timeout_seconds: 30
    input_schema:
      type: object
    output_schema:
      type: object
usage_documents:
  - path: README.md
    target: provider
`, 0o600)
	writeConnectorTestFile(t, connectorRoot, "README.md", "connector usage\n", 0o600)
	writeConnectorTestFile(t, scriptRoot, "loom.script.yaml", `kind: loom.script
id: connector_ping
name: Connector Ping
version: 0.1.0
entrypoint:
  command:
    - ./run.sh
execution:
  timeout_seconds: 30
  network: false
`, 0o600)
	writeConnectorTestFile(t, scriptRoot, "run.sh", "#!/usr/bin/env bash\nprintf '{\"ok\":true}\\n'\n", 0o700)
	return root
}

func writeConnectorTestFile(t *testing.T, root, rel, body string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func copyConnectorTestDir(t *testing.T, src, dst string) {
	t.Helper()
	if err := filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, payload, info.Mode())
	}); err != nil {
		t.Fatalf("copy connector test dir: %v", err)
	}
}

func hasConnectorPlanAction(actions []PlanAction, action string) bool {
	for _, candidate := range actions {
		if candidate.Action == action {
			return true
		}
	}
	return false
}

func replaceConnectorTestFile(t *testing.T, path, old, new string) {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	next := strings.Replace(string(payload), old, new, 1)
	if next == string(payload) {
		t.Fatalf("expected to replace %q in %s", old, path)
	}
	if err := os.WriteFile(path, []byte(next), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
