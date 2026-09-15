package projectcontracts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/scripts"
)

func TestScriptFacetDiscoversDisabledExposure(t *testing.T) {
	root := scriptProjectFixture(t)
	writeScriptPackage(t, root, "hello_world", true, disabledScriptExposure())

	analysis := Analyze(root)
	if !analysis.Report.OK {
		t.Fatalf("expected report ok, diagnostics=%#v", analysis.Report.Diagnostics)
	}
	if analysis.Report.Summary.Errors != 0 || analysis.Report.Summary.Warnings != 0 {
		t.Fatalf("unexpected diagnostics summary: %#v diagnostics=%#v", analysis.Report.Summary, analysis.Report.Diagnostics)
	}
	if len(analysis.Report.Scripts) != 1 {
		t.Fatalf("scripts = %#v", analysis.Report.Scripts)
	}
	script := analysis.Report.Scripts[0]
	if script.Key != "hello_world" || script.Exposed || script.ActivationStatus != ScriptActivationStatusDisabled {
		t.Fatalf("unexpected script item: %#v", script)
	}
	if script.Form != capabilities.CapabilityFormJob {
		t.Fatalf("form = %q", script.Form)
	}
	assertPlanAction(t, analysis.Plan.Actions, "would_register_script", "hello_world")
	assertPlanAction(t, analysis.Plan.Actions, "would_skip_disabled_script_exposure", "hello_world")
}

func TestScriptFacetEnabledExposureProducesCapabilityAddress(t *testing.T) {
	root := scriptProjectFixture(t)
	writeScriptPackage(t, root, "hello_world", true, enabledScriptExposure())

	analysis := Analyze(root)
	if !analysis.Report.OK {
		t.Fatalf("expected report ok, diagnostics=%#v", analysis.Report.Diagnostics)
	}
	if len(analysis.Report.Scripts) != 1 {
		t.Fatalf("scripts = %#v", analysis.Report.Scripts)
	}
	script := analysis.Report.Scripts[0]
	if !script.Exposed {
		t.Fatalf("script should be exposed: %#v", script)
	}
	if script.ProviderAddress != "main@script-smoke" {
		t.Fatalf("provider address = %q", script.ProviderAddress)
	}
	if script.CapabilityAddress != "main@script-smoke.hello_world" {
		t.Fatalf("capability address = %q", script.CapabilityAddress)
	}
	if script.ActivationStatus != ScriptActivationStatusPending {
		t.Fatalf("activation status = %q", script.ActivationStatus)
	}
	assertPlanAction(t, analysis.Plan.Actions, "would_register_capability_endpoint", "main@script-smoke.hello_world")
	assertPlanAction(t, analysis.Plan.Actions, "would_register_script_runtime_binding", "main@script-smoke.hello_world")
}

func TestScriptFacetParsesCredentialRequirements(t *testing.T) {
	root := scriptProjectFixture(t)
	exposure := strings.Replace(enabledScriptExposure(), "execution:\n  default_mode:", "credentials:\n  required:\n    - ref: telegram.bot_token\n      expose_as: TELEGRAM_BOT_TOKEN\n      kind: env\n\nexecution:\n  default_mode:", 1)
	writeScriptPackage(t, root, "hello_world", true, exposure)

	analysis := Analyze(root)
	if !analysis.Report.OK {
		t.Fatalf("expected report ok, diagnostics=%#v", analysis.Report.Diagnostics)
	}
	script := analysis.Report.Scripts[0]
	if len(script.Credentials.Required) != 1 || script.Credentials.Required[0].Ref != "telegram.bot_token" || script.Credentials.Required[0].ExposeAs != "TELEGRAM_BOT_TOKEN" {
		t.Fatalf("credential requirements not parsed: %#v", script.Credentials)
	}
	if string(script.CredentialJSON) == "" || string(script.CredentialJSON) == "{}" {
		t.Fatalf("credential requirements JSON should not be empty: %s", script.CredentialJSON)
	}
	assertPlanAction(t, analysis.Plan.Actions, "would_require_runtime_credential", "telegram.bot_token")
}

func TestScriptFacetReportsManifestAndEntrypointDiagnostics(t *testing.T) {
	t.Run("bad manifest", func(t *testing.T) {
		root := scriptProjectFixture(t)
		packageRoot := filepath.Join(root, "scripts", "hello_world")
		mkdir(t, root, "scripts/hello_world")
		writeFile(t, packageRoot, "loom.script.yaml", `
kind: loom.script
id: Bad Script
name: Bad Script
version: 0.1.0
entrypoint:
  command:
    - ./run.sh
execution:
  timeout_seconds: 30
`, 0o600)

		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatalf("expected invalid manifest to fail")
		}
		assertDiagnostic(t, analysis.Report.Diagnostics, "script.manifest_invalid")
	})

	t.Run("missing interpreter file", func(t *testing.T) {
		root := scriptProjectFixture(t)
		packageRoot := filepath.Join(root, "scripts", "hello_world")
		mkdir(t, root, "scripts/hello_world")
		writeFile(t, packageRoot, "loom.script.yaml", scriptManifest("bash", "run.sh"), 0o600)
		writeFile(t, packageRoot, "README.md", "usage\n", 0o600)

		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatalf("expected missing interpreter file to fail")
		}
		assertDiagnostic(t, analysis.Report.Diagnostics, "script.entrypoint_missing")
	})

	t.Run("direct file not executable", func(t *testing.T) {
		root := scriptProjectFixture(t)
		packageRoot := filepath.Join(root, "scripts", "hello_world")
		mkdir(t, root, "scripts/hello_world")
		writeFile(t, packageRoot, "loom.script.yaml", scriptManifest("./run.sh"), 0o600)
		writeFile(t, packageRoot, "run.sh", "#!/usr/bin/env bash\n", 0o600)
		writeFile(t, packageRoot, "README.md", "usage\n", 0o600)

		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatalf("expected non-executable entrypoint to fail")
		}
		assertDiagnostic(t, analysis.Report.Diagnostics, "script.entrypoint_not_executable")
	})
}

func TestValidateScriptExposureDiagnostics(t *testing.T) {
	project := NormalizeContract(ProjectContract{
		Project: ProjectSpec{
			Slug:      "script-smoke",
			Name:      "Script Smoke",
			OwnerNode: "main",
		},
	})
	exposure := ScriptExposure{
		Kind:          ScriptExposureKind,
		SchemaVersion: ScriptExposureSchemaV03,
		Expose: ScriptExposeSpec{
			Enabled:  true,
			Provider: "project",
			Endpoint: "Bad-Endpoint",
		},
		Capability: ScriptCapabilitySpec{
			Form:        "action",
			RiskLevel:   capabilities.RiskLevelLow,
			InputSchema: map[string]any{"bad": func() {}},
		},
		Execution: ScriptExposureExecution{
			DefaultMode:        "wait_until_started",
			WaitTimeoutSeconds: 10,
		},
	}

	diagnostics := ValidateScriptExposure(project, scripts.Manifest{ID: "hello_world"}, exposure)
	assertDiagnostic(t, diagnostics, "script_exposure.form_alias_action")
	assertDiagnostic(t, diagnostics, "script_exposure.endpoint_invalid")
	assertDiagnostic(t, diagnostics, "script_exposure.address_invalid")
	assertDiagnostic(t, diagnostics, "script_exposure.input_schema_invalid")
}

func TestValidateScriptExposureCredentialDiagnostics(t *testing.T) {
	project := NormalizeContract(ProjectContract{
		Project: ProjectSpec{
			Slug:      "script-smoke",
			Name:      "Script Smoke",
			OwnerNode: "main",
		},
	})
	exposure := ScriptExposure{
		Kind:          ScriptExposureKind,
		SchemaVersion: ScriptExposureSchemaV03,
		Expose: ScriptExposeSpec{
			Enabled: true,
		},
		Capability: ScriptCapabilitySpec{
			Form:      capabilities.CapabilityFormJob,
			RiskLevel: capabilities.RiskLevelLow,
		},
		Credentials: CredentialSpec{Required: []CredentialRequirement{{
			Ref:      "telegram.bot_token",
			ExposeAs: "telegram_token",
			Kind:     "env",
		}}},
	}

	diagnostics := ValidateScriptExposure(project, scripts.Manifest{ID: "hello_world"}, exposure)
	assertDiagnostic(t, diagnostics, "credential.expose_as_invalid")
}

func TestValidateScriptExposureDefaultsWaitTimeoutForJobRunnerTick(t *testing.T) {
	root := scriptProjectFixture(t)
	exposure := strings.Replace(enabledScriptExposure(), "  wait_timeout_seconds: 10\n", "", 1)
	writeScriptPackage(t, root, "hello_world", true, exposure)

	analysis := Analyze(root)
	if !analysis.Report.OK {
		t.Fatalf("expected report ok, diagnostics=%#v", analysis.Report.Diagnostics)
	}
	if len(analysis.Report.Scripts) != 1 {
		t.Fatalf("scripts = %#v", analysis.Report.Scripts)
	}
	if analysis.Report.Scripts[0].WaitTimeoutSeconds != 120 {
		t.Fatalf("wait_timeout_seconds = %d, want 120", analysis.Report.Scripts[0].WaitTimeoutSeconds)
	}
}

func scriptProjectFixture(t *testing.T) string {
	t.Helper()
	root := projectFixture(t, `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: script-smoke
  name: Script Smoke
  owner_node: main
facets:
  scripts: true
`)
	mkdir(t, root, "scripts")
	return root
}

func writeScriptPackage(t *testing.T, root, key string, executable bool, exposure string) {
	t.Helper()
	packageRoot := filepath.Join(root, "scripts", key)
	mkdir(t, root, filepath.ToSlash(filepath.Join("scripts", key)))
	mode := os.FileMode(0o600)
	if executable {
		mode = 0o700
	}
	writeFile(t, packageRoot, "loom.script.yaml", scriptManifest("./run.sh"), 0o600)
	writeFile(t, packageRoot, "run.sh", "#!/usr/bin/env bash\nprintf 'hello\\n'\n", mode)
	writeFile(t, packageRoot, "README.md", "usage\n", 0o600)
	writeFile(t, packageRoot, "loom.exposure.yaml", exposure, 0o600)
}

func scriptManifest(command ...string) string {
	commandYAML := ""
	for _, part := range command {
		commandYAML += "    - " + part + "\n"
	}
	return `
kind: loom.script
id: hello_world
name: Hello World
version: 0.1.0
description: Test script.
entrypoint:
  command:
` + commandYAML + `
execution:
  timeout_seconds: 30
  network: false
  filesystem:
    mode: read_only
usage_documents:
  - path: README.md
`
}

func disabledScriptExposure() string {
	return scriptExposure(false)
}

func enabledScriptExposure() string {
	return scriptExposure(true)
}

func scriptExposure(enabled bool) string {
	enabledText := "false"
	if enabled {
		enabledText = "true"
	}
	return `
kind: loom.script_exposure
schema_version: script.exposure.v0.3
expose:
  enabled: ` + enabledText + `
  provider: project
  endpoint: hello_world
  display_name: Hello World
  description: Test script capability.
capability:
  class_namespace: project
  class_name: script_smoke
  form: job
  risk_level: low
  execution_authorization_level: 1
  input_schema:
    type: object
  output_schema:
    type: object
execution:
  default_mode: wait_until_started
  wait_timeout_seconds: 10
`
}

func writeFile(t *testing.T, root, rel, body string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertPlanAction(t *testing.T, actions []PlanAction, action, target string) {
	t.Helper()
	for _, item := range actions {
		if item.Action == action && item.TargetRef == target {
			return
		}
	}
	t.Fatalf("missing plan action %s for %s in %#v", action, target, actions)
}
