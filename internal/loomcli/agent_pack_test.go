package loomcli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/agentpack"
)

func agentPackFixturePath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "agentpack", "testdata", "valid-pack"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func executeAgentPackCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := NewRootCommand()
	output := &bytes.Buffer{}
	cmd.SetOut(output)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	err := cmd.Execute()
	return output.String(), err
}

func TestAgentPackStatusAndValidateAreLocal(t *testing.T) {
	output, err := executeAgentPackCommand(t, "agent", "pack", "status", "--pack-dir", agentPackFixturePath(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "fixture-pack 1.0.0") ||
		!strings.Contains(output, "Harnesses: codex, orca") ||
		!strings.Contains(output, "Recommended skill sets: morathustra, project") ||
		!strings.Contains(output, "errors=0 warnings=0") {
		t.Fatalf("unexpected status:\n%s", output)
	}
	output, err = executeAgentPackCommand(t, "--json", "agent", "pack", "validate", "--pack-dir", agentPackFixturePath(t))
	if err != nil {
		t.Fatal(err)
	}
	var report agentpack.ValidationReport
	if err := json.Unmarshal([]byte(output), &report); err != nil || !report.Valid() {
		t.Fatalf("report=%#v decode=%v output=%s", report, err, output)
	}
}

func TestAgentPackScaffoldWorkspaceDryRunAndApply(t *testing.T) {
	packDir, err := filepath.Abs(filepath.Join("..", "..", "ai-loom-pack"))
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "morathustra")
	cleanupAgentWorkspace(t, destination)
	output, err := executeAgentPackCommand(t, "--json", "agent", "pack", "scaffold-workspace", "--pack-dir", packDir, "--template", "morathustra", "--path", destination, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	var plan agentpack.WorkspacePlan
	if err := json.Unmarshal([]byte(output), &plan); err != nil || len(plan.Actions) == 0 || plan.Path != destination {
		t.Fatalf("plan=%#v decode=%v output=%s", plan, err, output)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("dry-run created workspace: %v", err)
	}
	output, err = executeAgentPackCommand(t, "agent", "pack", "scaffold-workspace", "--pack-dir", packDir, "--template", "morathustra", "--path", destination, "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "Scaffolded morathustra workspace") {
		t.Fatalf("unexpected apply output:\n%s", output)
	}
	if _, err := os.Stat(filepath.Join(destination, "handoffs", "HANDOFF-TEMPLATE.md")); err != nil {
		t.Fatal(err)
	}
}

func TestAgentPackScaffoldArchivistNamedTemplateIsPortableAndPreserving(t *testing.T) {
	packDir, err := filepath.Abs(filepath.Join("..", "..", "ai-loom-pack"))
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "archivist")
	output, err := executeAgentPackCommand(t, "--json", "agent", "pack", "scaffold-workspace", "--pack-dir", packDir, "--template", "archivist", "--path", destination, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	var plan agentpack.WorkspacePlan
	if err := json.Unmarshal([]byte(output), &plan); err != nil || plan.Template != "archivist" || plan.Path != destination || plan.Conflicts != 0 {
		t.Fatalf("plan=%#v decode=%v output=%s", plan, err, output)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("archivist dry-run created workspace: %v", err)
	}
	for _, action := range plan.Actions {
		if action.RelativePath == "memory" || strings.HasPrefix(action.RelativePath, "memory/") {
			t.Fatalf("archivist plan contains private memory: %#v", action)
		}
	}

	output, err = executeAgentPackCommand(t, "--json", "agent", "pack", "scaffold-workspace", "--pack-dir", packDir, "--template", "archivist", "--path", destination, "--yes")
	if err != nil {
		t.Fatal(err)
	}
	var result agentpack.WorkspaceResult
	if err := json.Unmarshal([]byte(output), &result); err != nil || !result.Applied || result.Template != "archivist" {
		t.Fatalf("result=%#v decode=%v output=%s", result, err, output)
	}
	if _, err := os.Stat(filepath.Join(destination, "protocols", "CANDIDATE-REVIEW.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, "memory")); !os.IsNotExist(err) {
		t.Fatalf("archivist apply created private memory: %v", err)
	}

	identityPath := filepath.Join(destination, "ARCHIVIST.md")
	custom := "# Archivist\n\nUser-owned extension.\n"
	if err := os.WriteFile(identityPath, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := executeAgentPackCommand(t, "agent", "pack", "scaffold-workspace", "--pack-dir", packDir, "--template", "archivist", "--path", destination, "--yes"); err != nil {
		t.Fatal(err)
	}
	if payload, err := os.ReadFile(identityPath); err != nil || string(payload) != custom {
		t.Fatalf("custom ARCHIVIST.md changed: payload=%q err=%v", payload, err)
	}
}

func TestAgentPackScaffoldWorkspaceNamedTemplateSelectionIsGeneric(t *testing.T) {
	packDir, err := filepath.Abs(filepath.Join("..", "..", "ai-loom-pack"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"archivist", "project", "box-notes"} {
		t.Run(name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), name)
			output, err := executeAgentPackCommand(t, "--json", "agent", "pack", "scaffold-workspace", "--pack-dir", packDir, "--template", name, "--path", destination, "--dry-run")
			if err != nil {
				t.Fatal(err)
			}
			var plan agentpack.WorkspacePlan
			if err := json.Unmarshal([]byte(output), &plan); err != nil || plan.Template != name || len(plan.Actions) == 0 {
				t.Fatalf("plan=%#v decode=%v output=%s", plan, err, output)
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatalf("named-template dry-run created workspace: %v", err)
			}
		})
	}
}

func TestAgentPackScaffoldWorkspaceRequiresModeAndExplicitPath(t *testing.T) {
	packDir, err := filepath.Abs(filepath.Join("..", "..", "ai-loom-pack"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executeAgentPackCommand(t, "agent", "pack", "scaffold-workspace", "--pack-dir", packDir, "--dry-run"); err == nil {
		t.Fatal("expected missing explicit path to fail")
	}
	if _, err := executeAgentPackCommand(t, "agent", "pack", "scaffold-workspace", "--pack-dir", packDir, "--path", filepath.Join(t.TempDir(), "workspace")); err == nil {
		t.Fatal("expected missing execution mode to fail")
	}
}

func TestAgentPackCommandTreeIsRegistered(t *testing.T) {
	cmd := NewRootCommand()
	for _, args := range [][]string{{"agent", "pack", "status"}, {"agent", "pack", "validate"}, {"agent", "pack", "scaffold-workspace"}} {
		found, _, err := cmd.Find(args)
		if err != nil || found == nil {
			t.Fatalf("Find(%v) command=%v err=%v", args, found, err)
		}
	}
}

func TestAgentPackInstallIsNotRegistered(t *testing.T) {
	_, err := executeAgentPackCommand(t, "agent", "pack", "install")
	if err == nil {
		t.Fatalf("expected install command to be absent, got %v", err)
	}
}

func cleanupAgentWorkspace(t *testing.T, path string) {
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

func TestAgentPackMorathustraHermesReplayAndLegacyRefusal(t *testing.T) {
	packDir, err := filepath.Abs(filepath.Join("..", "..", "ai-loom-pack"))
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "morathustra")
	cleanupAgentWorkspace(t, destination)
	args := []string{"--json", "agent", "pack", "scaffold-workspace", "--pack-dir", packDir, "--path", destination, "--yes"}
	if _, err := executeAgentPackCommand(t, args...); err != nil {
		t.Fatal(err)
	}
	soul := filepath.Join(destination, ".hermes/SOUL.md")
	custom := "# Morathustra\n\nOperator-reviewed personality.\n"
	if err := os.WriteFile(soul, []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := executeAgentPackCommand(t, args...)
	if err != nil {
		t.Fatal(err)
	}
	var result agentpack.WorkspaceResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	preserved := false
	for _, action := range result.Actions {
		if action.Kind == agentpack.WorkspaceCreateDirectory || action.Kind == agentpack.WorkspaceCreateFile {
			t.Fatalf("replay created %s", action.RelativePath)
		}
		if action.RelativePath == ".hermes/SOUL.md" && action.Kind == agentpack.WorkspacePreserveUserFile {
			preserved = true
		}
	}
	if !preserved {
		t.Fatal("replay did not preserve SOUL")
	}
	if payload, err := os.ReadFile(soul); err != nil || string(payload) != custom {
		t.Fatal("custom SOUL changed")
	}
	if err := os.WriteFile(filepath.Join(destination, "MORA.md"), []byte("legacy identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := executeAgentPackCommand(t, args...); err == nil || !strings.Contains(err.Error(), "operator review required") {
		t.Fatalf("legacy refusal=%v", err)
	}
	if payload, err := os.ReadFile(filepath.Join(destination, "MORA.md")); err != nil || string(payload) != "legacy identity" {
		t.Fatal("legacy identity changed")
	}
}

func TestAgentPackWorkspaceRefusalHasVisibleDiagnostic(t *testing.T) {
	packDir, err := filepath.Abs(filepath.Join("..", "..", "ai-loom-pack"))
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "legacy")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "MORA.md"), []byte("synthetic private personality"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := NewRootCommand()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"agent", "pack", "scaffold-workspace", "--pack-dir", packDir, "--path", destination, "--yes"})
	if err := command.Execute(); err == nil {
		t.Fatal("expected refusal")
	}
	if !strings.Contains(output.String(), "operator review required") || !strings.Contains(output.String(), "MORA.md") {
		t.Fatalf("missing diagnostic: %s", output.String())
	}
	if strings.Contains(output.String(), "synthetic private personality") {
		t.Fatal("diagnostic leaked file content")
	}
}

func TestAgentPackMorathustraNamedIdentityScaffoldRemainsOffline(t *testing.T) {
	packDir, err := filepath.Abs(filepath.Join("..", "..", "ai-loom-pack"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	trapDir := filepath.Join(root, "tools")
	if err := os.Mkdir(trapDir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "external-call")
	t.Setenv("LOOM_TEST_EXTERNAL_CALL_MARKER", marker)
	for _, name := range []string{"basecamp", "gh", "git", "hermes", "ssh"} {
		if err := os.WriteFile(filepath.Join(trapDir, name), []byte("#!/bin/sh\nprintf 'unexpected external call' > \"$LOOM_TEST_EXTERNAL_CALL_MARKER\"\nexit 99\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", trapDir)
	t.Setenv("HOME", root)
	t.Setenv("BASECAMP_PROFILE", "codex")
	t.Setenv("GH_TOKEN", "synthetic-personal-credential-do-not-copy")
	t.Setenv("GH_CONFIG_DIR", filepath.Join(root, "personal-gh"))
	destination := filepath.Join(root, "workspace")
	cleanupAgentWorkspace(t, destination)
	args := []string{"--json", "agent", "pack", "scaffold-workspace", "--pack-dir", packDir, "--path", destination, "--yes"}
	for replay := 0; replay < 2; replay++ {
		output, err := executeAgentPackCommand(t, args...)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output, "synthetic-personal-credential") {
			t.Fatal("scaffold reported credential values")
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("scaffold invoked external account or runtime tooling")
	}
	for _, relative := range []string{".git", ".hermes/auth.json", ".hermes/config.yaml", ".hermes/state.db", ".config/basecamp", ".config/gh"} {
		if _, err := os.Lstat(filepath.Join(destination, relative)); !os.IsNotExist(err) {
			t.Errorf("scaffold created forbidden state: %s", relative)
		}
	}
	for _, name := range []string{"BASECAMP.md", "GITHUB.md"} {
		want, err := os.ReadFile(filepath.Join(packDir, "templates/morathustra/protocols", name))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(destination, "protocols", name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("named protocol changed in scaffold: %s", name)
		}
	}
}

func TestAgentPackMINAExplicitScaffoldPlanApplyReplayAndDefault(t *testing.T) {
	packDir, err := filepath.Abs(filepath.Join("..", "..", "ai-loom-pack"))
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "mina")
	cleanupAgentWorkspace(t, destination)
	args := []string{"--json", "agent", "pack", "scaffold-workspace", "--pack-dir", packDir, "--path", destination}
	output, err := executeAgentPackCommand(t, append(args, "--dry-run")...)
	if err != nil {
		t.Fatal(err)
	}
	var plan agentpack.WorkspacePlan
	if err := json.Unmarshal([]byte(output), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Template != "morathustra" {
		t.Fatal("CLI default changed")
	}
	args = append(args, "--template", "mina")
	output, err = executeAgentPackCommand(t, append(args, "--dry-run")...)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(output), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Template != "mina" || plan.PackVersion != "0.6.4" || plan.Conflicts != 0 {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("dry-run wrote output")
	}
	output, err = executeAgentPackCommand(t, append(args, "--yes")...)
	if err != nil {
		t.Fatal(err)
	}
	var result agentpack.WorkspaceResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.Template != "mina" {
		t.Fatal("MINA not applied")
	}
	output, err = executeAgentPackCommand(t, append(args, "--yes")...)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	for _, action := range result.Actions {
		if action.Kind != agentpack.WorkspaceUnchangedFile && action.Kind != agentpack.WorkspaceUnchangedDirectory {
			t.Fatalf("replay changed %s", action.RelativePath)
		}
	}
}

func workspaceExportCLIFixture(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	paths := []string{"ai-loom-pack/manifest.yaml", "nix/files/mina/skins/mina-matrix-teal.yaml"}
	for _, relative := range []string{".hermes/SOUL.md", "AGENTS.md", "WORKFLOW.md", "OPERATING-POLICY.md", "WORKSPACE-MAP.md", "TOOLING.md", "protocols/README.md", "protocols/BASECAMP.md", "protocols/GITHUB.md", "protocols/CREDENTIALS.md", "protocols/EXTERNAL-MESSAGING.md", "protocols/LOOM-ROUTING.md", "protocols/PROJECT-DELEGATION.md", "protocols/PROVENANCE-AND-MEMORY.md", "protocols/SESSION-RETRIEVAL.md", "protocols/SKILL-CREATION-AND-PROMOTION.md", "protocols/DEVICE-AND-TOOL-ROUTING.md", "protocols/MAC-COMPUTER-USE.md", "handoffs/HANDOFF-TEMPLATE.md"} {
		paths = append(paths, "ai-loom-pack/templates/mina/"+relative)
	}
	for _, path := range paths {
		payload, err := os.ReadFile(filepath.Join("..", "..", path))
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, payload, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "--quiet"}, append([]string{"add", "--"}, paths...), {"commit", "--quiet", "-m", "CLI fixture"}} {
		cmd := exec.Command("git", append([]string{"-c", "core.hooksPath=/dev/null", "-C", root}, args...)...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid"}
		if data, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture Git: %v %s", err, data)
		}
	}
	return root
}

func TestWorkspaceExportCLIPlanApplyAndRefusal(t *testing.T) {
	source := workspaceExportCLIFixture(t)
	destination := filepath.Join(t.TempDir(), "projection")
	args := []string{"--json", "agent", "pack", "export-workspace", "--template", "mina", "--source-root", source, "--path", destination}
	var id string
	for _, flags := range [][]string{nil, {"--dry-run"}, {"--dry-run", "--yes"}} {
		output, err := executeAgentPackCommand(t, append(append([]string{}, args...), flags...)...)
		if err != nil {
			t.Fatal(err)
		}
		var plan agentpack.WorkspaceExportPlan
		if err := json.Unmarshal([]byte(output), &plan); err != nil {
			t.Fatal(err)
		}
		if len(plan.Manifest.Files) != 22 || plan.Manifest.ExportID == "" || id != "" && id != plan.Manifest.ExportID {
			t.Fatalf("invalid plan: %s", output)
		}
		id = plan.Manifest.ExportID
		if strings.Contains(output, source) || strings.Contains(output, destination) {
			t.Fatal("structured plan leaked host paths")
		}
		if _, err := os.Lstat(destination); !os.IsNotExist(err) {
			t.Fatal("read-only CLI wrote output")
		}
	}
	output, err := executeAgentPackCommand(t, append(args, "--yes")...)
	if err != nil {
		t.Fatal(err)
	}
	var result agentpack.WorkspaceExportResult
	if err := json.Unmarshal([]byte(output), &result); err != nil || !result.Applied || result.Manifest.ExportID != id || len(result.Created) != 28 {
		t.Fatalf("result: %+v %v", result, err)
	}
	info, err := os.Stat(filepath.Join(destination, ".hermes/SOUL.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = executeAgentPackCommand(t, append(args, "--yes")...)
	if err == nil {
		t.Fatal("existing export not refused")
	}
	after, err := os.Stat(filepath.Join(destination, ".hermes/SOUL.md"))
	if err != nil || !os.SameFile(info, after) || !info.ModTime().Equal(after.ModTime()) {
		t.Fatal("refusal changed destination")
	}
	for _, bad := range [][]string{
		{"agent", "pack", "export-workspace"},
		{"agent", "pack", "export-workspace", "--template", "morathustra", "--source-root", source, "--path", destination + "-bad"},
		{"agent", "pack", "export-workspace", "--template", "mina", "--source-root", source, "--path", destination + "-bad", "--pack-dir", source},
	} {
		if _, err := executeAgentPackCommand(t, bad...); err == nil {
			t.Fatal("invalid CLI options accepted")
		}
	}
	help, err := executeAgentPackCommand(t, "agent", "pack", "export-workspace", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, term := range []string{"--template mina", "--source-root", "--path", "--dry-run never writes", "memory or authentication", "Existing destinations are always refused"} {
		if !strings.Contains(help, term) {
			t.Errorf("help missing %q", term)
		}
	}
	// Root silence must not swallow the actionable refusal.
	cmd := NewRootCommand()
	stderr := &bytes.Buffer{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{"agent", "pack", "export-workspace"})
	if err := cmd.Execute(); err == nil || !strings.Contains(stderr.String(), "Workspace export failed:") {
		t.Fatal("silent CLI refusal")
	}
}
