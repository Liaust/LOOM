package projectcontracts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModuleFacetDiscoversRegisterablePackage(t *testing.T) {
	root := scaffoldModuleTestProject(t)

	analysis := Analyze(root)
	if !analysis.Report.OK {
		t.Fatalf("module project should validate: %#v", analysis.Report.Diagnostics)
	}
	if len(analysis.Report.Modules) != 1 {
		t.Fatalf("modules discovered = %d, want 1", len(analysis.Report.Modules))
	}
	item := analysis.Report.Modules[0]
	if item.ModuleID != "loom.module-facet-smoke" || !item.RegistrationEnabled {
		t.Fatalf("unexpected module item: %#v", item)
	}
	if len(analysis.Plan.Actions) == 0 {
		t.Fatal("expected module plan actions")
	}
	if !hasPlanAction(analysis.Plan.Actions, "would_register_module_package", "loom.module-facet-smoke") {
		t.Fatalf("missing module registration action: %#v", analysis.Plan.Actions)
	}
	if !hasPlanAction(analysis.Plan.Actions, "would_record_project_module_registration", "loom.module-facet-smoke") {
		t.Fatalf("missing project module registration action: %#v", analysis.Plan.Actions)
	}
}

func TestModuleFacetInvalidWrapperKindFails(t *testing.T) {
	root := scaffoldModuleTestProject(t)
	replaceProjectFile(t, root, "modules/example_module/loom.module_project.yaml", "kind: loom.module_project", "kind: loom.nope")

	analysis := Analyze(root)
	if analysis.Report.OK {
		t.Fatal("invalid module wrapper kind should fail validation")
	}
	if !hasDiagnostic(analysis.Report.Diagnostics, "module.project_kind_invalid") {
		t.Fatalf("missing invalid kind diagnostic: %#v", analysis.Report.Diagnostics)
	}
}

func TestModuleFacetUnsafeManifestPathFails(t *testing.T) {
	root := scaffoldModuleTestProject(t)
	replaceProjectFile(t, root, "modules/example_module/loom.module_project.yaml", "manifest: module.json", "manifest: ../module.json")

	analysis := Analyze(root)
	if analysis.Report.OK {
		t.Fatal("unsafe module manifest path should fail validation")
	}
	if !hasDiagnostic(analysis.Report.Diagnostics, "module.manifest_path_invalid") {
		t.Fatalf("missing unsafe manifest diagnostic: %#v", analysis.Report.Diagnostics)
	}
}

func TestModuleFacetStatefulModuleRequiresBackupHooks(t *testing.T) {
	root := scaffoldModuleTestProject(t)
	replaceProjectFile(t, root, "modules/example_module/module.json", `"database": {
      "required": false
    }`, `"database": {
      "required": true
    }`)

	analysis := Analyze(root)
	if analysis.Report.OK {
		t.Fatal("stateful module without backup hooks should fail validation")
	}
	if !hasDiagnostic(analysis.Report.Diagnostics, "module.backup_hooks_required") {
		t.Fatalf("missing backup hook diagnostic: %#v", analysis.Report.Diagnostics)
	}
}

func TestModuleFacetDisabledWrapperIsDiscoveredButNotRegisterable(t *testing.T) {
	root := scaffoldModuleTestProject(t)
	replaceProjectFile(t, root, "modules/example_module/loom.module_project.yaml", "status: draft", "status: disabled")

	analysis := Analyze(root)
	if !analysis.Report.OK {
		t.Fatalf("disabled module wrapper should still validate: %#v", analysis.Report.Diagnostics)
	}
	if len(analysis.Report.Modules) != 1 || analysis.Report.Modules[0].RegistrationEnabled {
		t.Fatalf("disabled module should be discovered but not registrable: %#v", analysis.Report.Modules)
	}
	if !hasPlanAction(analysis.Plan.Actions, "would_skip_disabled_module", "loom.module-facet-smoke") {
		t.Fatalf("missing disabled module plan action: %#v", analysis.Plan.Actions)
	}
}

func scaffoldModuleTestProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	result, err := ScaffoldProject(ScaffoldOptions{
		Name:      "Module Facet Smoke",
		OwnerNode: "main",
		Preset:    PresetModule,
		Directory: dir,
	})
	if err != nil {
		t.Fatalf("scaffold module project: %v", err)
	}
	return result.ProjectRoot
}

func replaceProjectFile(t *testing.T, root, rel, old, next string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	updated := strings.Replace(string(payload), old, next, 1)
	if updated == string(payload) {
		t.Fatalf("expected to replace %q in %s", old, rel)
	}
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func hasDiagnostic(diagnostics []Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func hasPlanAction(actions []PlanAction, action, targetRef string) bool {
	for _, planAction := range actions {
		if planAction.Action == action && planAction.TargetRef == targetRef {
			return true
		}
	}
	return false
}
