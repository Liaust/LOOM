package agentpack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadRepositoryPack(t *testing.T) *Pack {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "ai-loom-pack"))
	if err != nil {
		t.Fatal(err)
	}
	pack, err := LoadFromPath(root)
	if err != nil {
		t.Fatal(err)
	}
	return pack
}

func TestWorkspaceScaffoldCompleteIdempotentAndPreserving(t *testing.T) {
	pack := loadRepositoryPack(t)
	destination := filepath.Join(t.TempDir(), "morathustra")
	allowWorkspaceCleanup(t, destination)
	plan, err := PlanWorkspace(pack, WorkspaceOptions{Template: "morathustra", Path: destination})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Path != destination || plan.Conflicts != 0 {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("planning created destination: %v", err)
	}
	result, err := ApplyWorkspace(plan, true)
	if err != nil || !result.Applied {
		t.Fatalf("apply result=%#v err=%v", result, err)
	}
	template, err := findWorkspaceTemplate(pack, "morathustra")
	if err != nil {
		t.Fatal(err)
	}
	for _, relative := range template.Files {
		if info, err := os.Stat(filepath.Join(destination, filepath.FromSlash(relative))); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("missing scaffolded file %s: info=%v err=%v", relative, info, err)
		}
	}

	second, err := PlanWorkspace(pack, WorkspaceOptions{Template: "morathustra", Path: destination})
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range second.Actions {
		if action.Kind == WorkspaceCreateDirectory || action.Kind == WorkspaceCreateFile {
			t.Fatalf("second plan is not idempotent: %#v", second.Actions)
		}
	}
	if _, err := ApplyWorkspace(second, true); err != nil {
		t.Fatalf("idempotent apply: %v", err)
	}

	soulPath := filepath.Join(destination, ".hermes/SOUL.md")
	customSoul := "# Morathustra\n\nthe operator-curated identity.\n"
	if err := os.WriteFile(soulPath, []byte(customSoul), 0o644); err != nil {
		t.Fatal(err)
	}
	preservePlan, err := PlanWorkspace(pack, WorkspaceOptions{Template: "morathustra", Path: destination})
	if err != nil {
		t.Fatal(err)
	}
	if !hasWorkspaceAction(preservePlan, ".hermes/SOUL.md", WorkspacePreserveUserFile) {
		t.Fatalf("edited SOUL.md was not preserved: %#v", preservePlan.Actions)
	}
	if _, err := ApplyWorkspace(preservePlan, true); err != nil {
		t.Fatal(err)
	}
	if payload, err := os.ReadFile(soulPath); err != nil || string(payload) != customSoul {
		t.Fatalf("custom SOUL.md changed: payload=%q err=%v", payload, err)
	}

	if err := os.RemoveAll(filepath.Join(destination, "investigations")); err != nil {
		t.Fatal(err)
	}
	repairPlan, err := PlanWorkspace(pack, WorkspaceOptions{Template: "morathustra", Path: destination})
	if err != nil {
		t.Fatal(err)
	}
	if !hasWorkspaceAction(repairPlan, "investigations", WorkspaceCreateDirectory) || !hasWorkspaceAction(repairPlan, "investigations/README.md", WorkspaceCreateFile) {
		t.Fatalf("missing optional directory was not planned: %#v", repairPlan.Actions)
	}
	if _, err := ApplyWorkspace(repairPlan, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, "investigations", "README.md")); err != nil {
		t.Fatalf("missing directory was not repaired: %v", err)
	}
}

func TestArchivistWorkspaceScaffoldIsCompletePortableIdempotentAndPreserving(t *testing.T) {
	pack := loadRepositoryPack(t)
	destination := filepath.Join(t.TempDir(), "archivist")
	plan, err := PlanWorkspace(pack, WorkspaceOptions{Template: "archivist", Path: destination})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Template != "archivist" || plan.Conflicts != 0 {
		t.Fatalf("unexpected archivist plan: %#v", plan)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("planning created archivist workspace: %v", err)
	}
	result, err := ApplyWorkspace(plan, true)
	if err != nil || !result.Applied {
		t.Fatalf("apply result=%#v err=%v", result, err)
	}

	template, err := findWorkspaceTemplate(pack, "archivist")
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for _, relative := range template.Files {
		declared[relative] = true
		path := filepath.Join(destination, filepath.FromSlash(relative))
		if info, statErr := os.Stat(path); statErr != nil || !info.Mode().IsRegular() {
			t.Fatalf("missing scaffolded file %s: info=%v err=%v", relative, info, statErr)
		}
	}
	if _, err := os.Stat(filepath.Join(destination, "memory")); !os.IsNotExist(err) {
		t.Fatalf("archivist scaffold contains private memory: %v", err)
	}

	forbiddenComponents := map[string]bool{"memory": true, "runtime": true, "state": true, "credentials": true, "database": true}
	forbiddenContent := []string{
		"/srv/", "/Users/", "/home/", "/var/lib/", "postgres://", "postgresql://",
		"LOOM_PROVENANCE_DB_URL", "pass://", "BEGIN PRIVATE KEY", "password=", "token=",
		".loom/state", "checkpoint.json", "loom storage ", "loom node ", "loom backup ",
		"loom cloud ", "loom schedule ", "loom automation ", "pass-cli",
	}
	err = filepath.Walk(destination, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, relErr := filepath.Rel(destination, path)
		if relErr != nil {
			return relErr
		}
		for _, component := range strings.Split(filepath.ToSlash(relative), "/") {
			if forbiddenComponents[strings.ToLower(component)] {
				t.Errorf("forbidden scaffold component %q in %q", component, relative)
			}
		}
		if info.IsDir() {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			t.Errorf("non-regular scaffold output %q", relative)
			return nil
		}
		if !declared[filepath.ToSlash(relative)] {
			t.Errorf("undeclared scaffold file %q", relative)
		}
		payload, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, marker := range forbiddenContent {
			if strings.Contains(string(payload), marker) {
				t.Errorf("scaffold file %q contains host, secret, database, or runtime-state marker %q", relative, marker)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	identityPath := filepath.Join(destination, "ARCHIVIST.md")
	custom := "# Archivist\n\nOperator-reviewed local addition.\n"
	if err := os.WriteFile(identityPath, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	replay, err := PlanWorkspace(pack, WorkspaceOptions{Template: "archivist", Path: destination})
	if err != nil {
		t.Fatal(err)
	}
	if !hasWorkspaceAction(replay, "ARCHIVIST.md", WorkspacePreserveUserFile) {
		t.Fatalf("edited ARCHIVIST.md was not preserved: %#v", replay.Actions)
	}
	for _, action := range replay.Actions {
		if action.Kind == WorkspaceCreateDirectory || action.Kind == WorkspaceCreateFile {
			t.Fatalf("archivist replay is not idempotent: %#v", replay.Actions)
		}
	}
	if _, err := ApplyWorkspace(replay, true); err != nil {
		t.Fatal(err)
	}
	if payload, err := os.ReadFile(identityPath); err != nil || string(payload) != custom {
		t.Fatalf("custom ARCHIVIST.md changed: payload=%q err=%v", payload, err)
	}
}

func TestWorkspaceScaffoldNamedTemplateSelectionRemainsGeneric(t *testing.T) {
	pack := loadRepositoryPack(t)
	for _, name := range []string{"archivist", "morathustra", "project", "box-notes", "box-documents"} {
		t.Run(name, func(t *testing.T) {
			template, err := findWorkspaceTemplate(pack, name)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := PlanWorkspace(pack, WorkspaceOptions{Template: name, Path: filepath.Join(t.TempDir(), name)})
			if err != nil {
				t.Fatal(err)
			}
			if plan.Template != name {
				t.Fatalf("plan selected %q, want %q", plan.Template, name)
			}
			createdFiles := 0
			for _, action := range plan.Actions {
				if action.Kind == WorkspaceCreateFile {
					createdFiles++
				}
			}
			if createdFiles != len(template.Files) {
				t.Fatalf("template %q planned %d files, want %d", name, createdFiles, len(template.Files))
			}
		})
	}
}

func TestWorkspaceScaffoldRequiresExplicitPathAndConfirmation(t *testing.T) {
	pack := loadRepositoryPack(t)
	if _, err := PlanWorkspace(pack, WorkspaceOptions{Template: "morathustra"}); err == nil || !strings.Contains(err.Error(), "explicit --path") {
		t.Fatalf("expected explicit-path error, got %v", err)
	}
	destination := filepath.Join(t.TempDir(), "morathustra")
	allowWorkspaceCleanup(t, destination)
	plan, err := PlanWorkspace(pack, WorkspaceOptions{Template: "morathustra", Path: destination})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyWorkspace(plan, false); err == nil {
		t.Fatal("expected unconfirmed apply to fail")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("unconfirmed apply created destination: %v", err)
	}
}

func TestWorkspaceScaffoldBlocksNonDirectoryCollision(t *testing.T) {
	pack := loadRepositoryPack(t)
	destination := filepath.Join(t.TempDir(), "morathustra")
	allowWorkspaceCleanup(t, destination)
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "protocols"), []byte("blocking file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanWorkspace(pack, WorkspaceOptions{Template: "morathustra", Path: destination})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Conflicts == 0 || !hasWorkspaceAction(plan, "protocols", WorkspaceConflict) {
		t.Fatalf("expected protocols path conflict: %#v", plan)
	}
	if _, err := ApplyWorkspace(plan, true); err == nil {
		t.Fatal("expected conflicting apply to fail")
	}
}

func TestWorkspaceScaffoldRejectsManifestTraversal(t *testing.T) {
	pack := loadRepositoryPack(t)
	mutated := *pack
	mutated.Manifest = pack.Manifest
	mutated.Manifest.Templates = append([]ManifestTemplate(nil), pack.Manifest.Templates...)
	for index := range mutated.Manifest.Templates {
		if mutated.Manifest.Templates[index].Name == "morathustra" {
			mutated.Manifest.Templates[index].Files = append([]string(nil), mutated.Manifest.Templates[index].Files...)
			mutated.Manifest.Templates[index].Files = append(mutated.Manifest.Templates[index].Files, "../escape.md")
		}
	}
	if _, err := PlanWorkspace(&mutated, WorkspaceOptions{Template: "morathustra", Path: filepath.Join(t.TempDir(), "workspace")}); err == nil {
		t.Fatal("expected manifest traversal to fail")
	}
}

func TestMorathustraHandoffTemplateHasRequiredEvidenceFields(t *testing.T) {
	pack := loadRepositoryPack(t)
	path := filepath.Join(pack.Root.Path, "templates", "morathustra", "handoffs", "HANDOFF-TEMPLATE.md")
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(payload)
	for _, field := range []string{
		"Project or system scope", "Observed Behavior", "Affected node, project, and path",
		"timestamps", "Correlation or job IDs", "Commands run", "Reproduction steps",
		"Attempted repairs", "Suspected component", "Constraints and forbidden actions",
		"Current safety and production state", "Required validation",
	} {
		if !strings.Contains(strings.ToLower(content), strings.ToLower(field)) {
			t.Fatalf("handoff template missing %q:\n%s", field, content)
		}
	}
}

func hasWorkspaceAction(plan WorkspacePlan, relative string, kind WorkspaceActionKind) bool {
	for _, action := range plan.Actions {
		if action.RelativePath == relative && action.Kind == kind {
			return true
		}
	}
	return false
}

// Read-only installed placeholders are intentional; relax only this test's tree
// after assertions so TempDir can remove its disposable fixture.
func allowWorkspaceCleanup(t *testing.T, path string) {
	t.Helper()
	t.Cleanup(func() {
		_ = filepath.WalkDir(path, func(path string, entry os.DirEntry, err error) error {
			if err == nil && entry.IsDir() {
				_ = os.Chmod(path, 0o700)
			}
			return nil
		})
	})
}
