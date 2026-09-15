package projectcontracts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflowFacetDiscoversPlaceholderWorkflow(t *testing.T) {
	root := scaffoldWorkflowPlaceholderProject(t)

	analysis := Analyze(root)
	if !analysis.Report.OK {
		t.Fatalf("workflow project should validate: %#v", analysis.Report.Diagnostics)
	}
	if len(analysis.Report.Workflows) != 1 {
		t.Fatalf("workflows discovered = %d, want 1", len(analysis.Report.Workflows))
	}
	workflow := analysis.Report.Workflows[0]
	if workflow.WorkflowID != "example_workflow" || workflow.ImplementationKind != WorkflowImplementationPlaceholder {
		t.Fatalf("unexpected workflow item: %#v", workflow)
	}
	if workflow.ActivationStatus != WorkflowActivationStatusBlocked {
		t.Fatalf("placeholder workflow should be blocked, got %#v", workflow)
	}
	if !hasPlanAction(analysis.Plan.Actions, "would_validate_workflow_contract", "example_workflow") {
		t.Fatalf("missing workflow validation action: %#v", analysis.Plan.Actions)
	}
	if !hasPlanAction(analysis.Plan.Actions, "would_block_first_class_workflow_runtime", "example_workflow") {
		t.Fatalf("missing workflow blocked action: %#v", analysis.Plan.Actions)
	}
}

func TestWorkflowFacetInvalidKindAndSchemaFail(t *testing.T) {
	root := scaffoldWorkflowPlaceholderProject(t)
	replaceProjectFile(t, root, "workflows/example_workflow/loom.workflow.yaml", "kind: loom.workflow", "kind: loom.nope")
	replaceProjectFile(t, root, "workflows/example_workflow/loom.workflow.yaml", "schema_version: workflow.contract.v0.3", "schema_version: workflow.contract.v9")

	analysis := Analyze(root)
	if analysis.Report.OK {
		t.Fatal("invalid workflow kind/schema should fail validation")
	}
	if !hasDiagnostic(analysis.Report.Diagnostics, "workflow.kind_invalid") {
		t.Fatalf("missing workflow kind diagnostic: %#v", analysis.Report.Diagnostics)
	}
	if !hasDiagnostic(analysis.Report.Diagnostics, "workflow.schema_version_invalid") {
		t.Fatalf("missing workflow schema diagnostic: %#v", analysis.Report.Diagnostics)
	}
}

func TestWorkflowFacetRequiresImplementationKind(t *testing.T) {
	root := scaffoldWorkflowPlaceholderProject(t)
	replaceProjectFile(t, root, "workflows/example_workflow/loom.workflow.yaml", "implementation:\n  kind: placeholder", "implementation: {}")

	analysis := Analyze(root)
	if analysis.Report.OK {
		t.Fatal("missing workflow implementation.kind should fail validation")
	}
	if !hasDiagnostic(analysis.Report.Diagnostics, "workflow.implementation_required") {
		t.Fatalf("missing implementation required diagnostic: %#v", analysis.Report.Diagnostics)
	}
}

func TestWorkflowFacetIDAndStatusValidation(t *testing.T) {
	t.Run("id mismatch", func(t *testing.T) {
		root := scaffoldWorkflowPlaceholderProject(t)
		replaceProjectFile(t, root, "workflows/example_workflow/loom.workflow.yaml", "id: example_workflow", "id: other_workflow")

		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatal("workflow id/folder mismatch should fail")
		}
		if !hasDiagnostic(analysis.Report.Diagnostics, "workflow.id_folder_mismatch") {
			t.Fatalf("missing id mismatch diagnostic: %#v", analysis.Report.Diagnostics)
		}
	})

	t.Run("active status", func(t *testing.T) {
		root := scaffoldWorkflowPlaceholderProject(t)
		replaceProjectFile(t, root, "workflows/example_workflow/loom.workflow.yaml", "status: draft", "status: active")

		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatal("active workflow status should fail in v0.3")
		}
		if !hasDiagnostic(analysis.Report.Diagnostics, "workflow.status_invalid") {
			t.Fatalf("missing status diagnostic: %#v", analysis.Report.Diagnostics)
		}
	})
}

func TestWorkflowFacetFirstClassRuntimeIsReservedAndBlocked(t *testing.T) {
	root := scaffoldWorkflowPlaceholderProject(t)
	replaceProjectFile(t, root, "workflows/example_workflow/loom.workflow.yaml", "kind: placeholder", "kind: workflow")

	analysis := Analyze(root)
	if !analysis.Report.OK {
		t.Fatalf("reserved workflow runtime should validate with warning, got diagnostics: %#v", analysis.Report.Diagnostics)
	}
	if !hasDiagnostic(analysis.Report.Diagnostics, "workflow.runtime_unsupported") {
		t.Fatalf("missing runtime unsupported diagnostic: %#v", analysis.Report.Diagnostics)
	}
	if !hasPlanAction(analysis.Plan.Actions, "would_block_first_class_workflow_runtime", "example_workflow") {
		t.Fatalf("missing workflow runtime blocked action: %#v", analysis.Plan.Actions)
	}
}

func TestWorkflowFacetScriptBackedWorkflowResolvesScriptCapability(t *testing.T) {
	root := scaffoldWorkflowScriptProject(t)

	analysis := Analyze(root)
	if !analysis.Report.OK {
		t.Fatalf("script-backed workflow should validate: %#v", analysis.Report.Diagnostics)
	}
	if len(analysis.Report.Workflows) != 1 {
		t.Fatalf("workflows discovered = %d", len(analysis.Report.Workflows))
	}
	workflow := analysis.Report.Workflows[0]
	if workflow.ImplementationKind != WorkflowImplementationScript {
		t.Fatalf("implementation kind = %q", workflow.ImplementationKind)
	}
	if workflow.ScriptKey != "hello_world" || workflow.ScriptCapabilityAddress != "main@workflow-smoke.hello_world" {
		t.Fatalf("workflow script link not resolved: %#v", workflow)
	}
	if !hasPlanAction(analysis.Plan.Actions, "would_link_script_backed_workflow", "example_workflow") {
		t.Fatalf("missing script-backed workflow action: %#v", analysis.Plan.Actions)
	}
}

func TestWorkflowFacetScriptRefAndStepValidation(t *testing.T) {
	t.Run("unsafe script ref", func(t *testing.T) {
		root := scaffoldWorkflowScriptProject(t)
		replaceProjectFile(t, root, "workflows/example_workflow/loom.workflow.yaml", "../../scripts/hello_world/loom.script.yaml", "../../../outside/loom.script.yaml")

		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatal("unsafe script ref should fail")
		}
		if !hasDiagnostic(analysis.Report.Diagnostics, "workflow.script_ref_unsafe") {
			t.Fatalf("missing unsafe script ref diagnostic: %#v", analysis.Report.Diagnostics)
		}
	})

	t.Run("invalid step target", func(t *testing.T) {
		root := scaffoldWorkflowPlaceholderProject(t)
		appendWorkflowSteps(t, root, `
steps:
  - id: parse
    kind: capability_call
    target: not-a-capability
`)

		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatal("invalid step target should fail")
		}
		if !hasDiagnostic(analysis.Report.Diagnostics, "workflow.step_target_invalid") {
			t.Fatalf("missing step target diagnostic: %#v", analysis.Report.Diagnostics)
		}
	})
}

func TestWorkflowFacetDisabledWorkflowIsSkipped(t *testing.T) {
	root := scaffoldWorkflowPlaceholderProject(t)
	replaceProjectFile(t, root, "workflows/example_workflow/loom.workflow.yaml", "status: draft", "status: disabled")

	analysis := Analyze(root)
	if !analysis.Report.OK {
		t.Fatalf("disabled workflow should validate: %#v", analysis.Report.Diagnostics)
	}
	if len(analysis.Report.Workflows) != 1 || analysis.Report.Workflows[0].ActivationStatus != WorkflowActivationStatusDisabled {
		t.Fatalf("disabled workflow not discovered correctly: %#v", analysis.Report.Workflows)
	}
	if !hasPlanAction(analysis.Plan.Actions, "would_skip_disabled_workflow", "example_workflow") {
		t.Fatalf("missing disabled workflow action: %#v", analysis.Plan.Actions)
	}
}

func TestWorkflowFacetExecutableWorkflowPackage(t *testing.T) {
	root := scaffoldWorkflowTestProject(t)

	analysis := Analyze(root)
	if !analysis.Report.OK {
		t.Fatalf("executable workflow should validate: %#v", analysis.Report.Diagnostics)
	}
	if len(analysis.Report.Workflows) != 1 {
		t.Fatalf("workflows discovered = %d", len(analysis.Report.Workflows))
	}
	workflow := analysis.Report.Workflows[0]
	if !workflow.Executable || workflow.SchemaVersion != WorkflowSchemaV031 || workflow.ImplementationKind != WorkflowImplementationWorkflow {
		t.Fatalf("workflow should be executable v0.3.1 package: %#v", workflow)
	}
	if workflow.ActivationStatus != WorkflowActivationStatusExecutable {
		t.Fatalf("activation status = %q", workflow.ActivationStatus)
	}
	if workflow.PackageHash == "" || workflow.PackageRoot != "workflows/example_workflow" {
		t.Fatalf("workflow package fields not populated: %#v", workflow)
	}
	if workflow.CapabilityAddress != "main@workflow-smoke.example_workflow" {
		t.Fatalf("capability address = %q", workflow.CapabilityAddress)
	}
	for _, action := range []string{
		"would_register_workflow_package",
		"would_register_workflow_capability_endpoint",
		"would_register_workflow_runtime_binding",
	} {
		if !hasPlanAction(analysis.Plan.Actions, action, workflow.WorkflowID) && !hasPlanAction(analysis.Plan.Actions, action, workflow.CapabilityAddress) {
			t.Fatalf("missing %s action: %#v", action, analysis.Plan.Actions)
		}
	}
}

func TestWorkflowFacetParsesCredentialRequirements(t *testing.T) {
	root := scaffoldWorkflowTestProject(t)
	replaceProjectFile(t, root, "workflows/example_workflow/loom.workflow.yaml", "credentials:\n  required: []", "credentials:\n  required:\n    - ref: telegram.default_chat_id\n      expose_as: TELEGRAM_CHAT_ID\n      kind: env")

	analysis := Analyze(root)
	if !analysis.Report.OK {
		t.Fatalf("workflow with credentials should validate: %#v", analysis.Report.Diagnostics)
	}
	workflow := analysis.Report.Workflows[0]
	if len(workflow.Credentials.Required) != 1 || workflow.Credentials.Required[0].Ref != "telegram.default_chat_id" {
		t.Fatalf("credential requirements not parsed: %#v", workflow.Credentials)
	}
	if string(workflow.CredentialJSON) == "" || string(workflow.CredentialJSON) == "{}" {
		t.Fatalf("credential requirements JSON should not be empty: %s", workflow.CredentialJSON)
	}
	if !hasPlanAction(analysis.Plan.Actions, "would_require_runtime_credential", "telegram.default_chat_id") {
		t.Fatalf("missing credential plan action: %#v", analysis.Plan.Actions)
	}
}

func TestWorkflowFacetExecutableWorkflowDefaultsVersion(t *testing.T) {
	root := scaffoldWorkflowTestProject(t)
	replaceProjectFile(t, root, "workflows/example_workflow/loom.workflow.yaml", "  version: 0.1.0\n", "")

	analysis := Analyze(root)
	if !analysis.Report.OK {
		t.Fatalf("executable workflow without explicit version should validate: %#v", analysis.Report.Diagnostics)
	}
	workflow := analysis.Report.Workflows[0]
	if workflow.Version != "0.1.0" {
		t.Fatalf("workflow version = %q, want default 0.1.0", workflow.Version)
	}
}

func TestWorkflowFacetExecutableWorkflowValidation(t *testing.T) {
	t.Run("missing entrypoint", func(t *testing.T) {
		root := scaffoldWorkflowTestProject(t)
		replaceProjectFile(t, root, "workflows/example_workflow/loom.workflow.yaml", "entrypoint:\n  command:\n    - ./run.sh", "entrypoint:\n  command: []")

		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatal("missing workflow entrypoint should fail")
		}
		if !hasDiagnostic(analysis.Report.Diagnostics, "workflow.entrypoint_required") {
			t.Fatalf("missing entrypoint diagnostic: %#v", analysis.Report.Diagnostics)
		}
	})

	t.Run("invalid capability risk", func(t *testing.T) {
		root := scaffoldWorkflowTestProject(t)
		replaceProjectFile(t, root, "workflows/example_workflow/loom.workflow.yaml", "risk_level: medium", "risk_level: severe")

		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatal("invalid workflow capability risk should fail")
		}
		if !hasDiagnostic(analysis.Report.Diagnostics, "workflow.capability_risk_invalid") {
			t.Fatalf("missing capability risk diagnostic: %#v", analysis.Report.Diagnostics)
		}
	})

	t.Run("invalid endpoint", func(t *testing.T) {
		root := scaffoldWorkflowTestProject(t)
		replaceProjectFile(t, root, "workflows/example_workflow/loom.workflow.yaml", "endpoint: example_workflow", "endpoint: Bad Endpoint")

		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatal("invalid workflow endpoint should fail")
		}
		if !hasDiagnostic(analysis.Report.Diagnostics, "workflow.expose_endpoint_invalid") {
			t.Fatalf("missing endpoint diagnostic: %#v", analysis.Report.Diagnostics)
		}
	})

	t.Run("unsafe artifact path", func(t *testing.T) {
		root := scaffoldWorkflowTestProject(t)
		replaceProjectFile(t, root, "workflows/example_workflow/loom.workflow.yaml", "artifacts: []", "artifacts:\n  - key: leak\n    path: ../outside.txt\n    type: file")

		analysis := Analyze(root)
		if analysis.Report.OK {
			t.Fatal("unsafe workflow artifact path should fail")
		}
		if !hasDiagnostic(analysis.Report.Diagnostics, "workflow.package_invalid") {
			t.Fatalf("missing package invalid diagnostic: %#v", analysis.Report.Diagnostics)
		}
	})
}

func scaffoldWorkflowTestProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	result, err := ScaffoldProject(ScaffoldOptions{
		Name:      "Workflow Smoke",
		Slug:      "workflow-smoke",
		OwnerNode: "main",
		Facets:    []string{"workflows"},
		Directory: dir,
	})
	if err != nil {
		t.Fatalf("scaffold workflow project: %v", err)
	}
	return result.ProjectRoot
}

func scaffoldWorkflowPlaceholderProject(t *testing.T) string {
	t.Helper()
	root := scaffoldWorkflowTestProject(t)
	writeWorkflowContract(t, root, `kind: loom.workflow
schema_version: workflow.contract.v0.3

workflow:
  id: example_workflow
  name: Example Workflow
  description: Placeholder workflow intent.
  status: draft

implementation:
  kind: placeholder

expose:
  enabled: false
  provider: project
  endpoint: example_workflow

inputs:
  schema:
    type: object
outputs:
  schema:
    type: object
steps: []
`)
	return root
}

func scaffoldWorkflowScriptProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	result, err := ScaffoldProject(ScaffoldOptions{
		Name:      "Workflow Smoke",
		Slug:      "workflow-smoke",
		OwnerNode: "main",
		Facets:    []string{"scripts", "workflows"},
		Directory: dir,
	})
	if err != nil {
		t.Fatalf("scaffold workflow script project: %v", err)
	}
	replaceProjectFile(t, result.ProjectRoot, "workflows/example_workflow/loom.workflow.yaml", "kind: workflow", "kind: script\n  script_ref: ../../scripts/hello_world/loom.script.yaml")
	return result.ProjectRoot
}

func writeWorkflowContract(t *testing.T, root, payload string) {
	t.Helper()
	path := filepath.Join(root, "workflows", "example_workflow", "loom.workflow.yaml")
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("write workflow contract: %v", err)
	}
}

func appendWorkflowSteps(t *testing.T, root, steps string) {
	t.Helper()
	path := filepath.Join(root, "workflows", "example_workflow", "loom.workflow.yaml")
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read workflow contract: %v", err)
	}
	updated := strings.Replace(string(payload), "steps: []", strings.TrimSpace(steps), 1)
	if updated == string(payload) {
		t.Fatalf("expected to replace workflow steps")
	}
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		t.Fatalf("write workflow contract: %v", err)
	}
}
