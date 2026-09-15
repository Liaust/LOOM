package loomcli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/repostate"
)

const (
	cliRepositoryID = "repo_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	cliProjectID    = "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"
)

func TestRootIncludesRepositoryStateLocalCommands(t *testing.T) {
	command := NewRootCommand()
	for _, args := range [][]string{{"repo-state", "validate"}, {"repo-state", "init"}, {"repo-state", "migration-plan"}} {
		found, _, err := command.Find(args)
		if err != nil || found == nil {
			t.Fatalf("Find(%v) command=%v err=%v", args, found, err)
		}
	}
}

func TestRepositoryStateInitCLIIsDryRunFirstAndDigestBound(t *testing.T) {
	repository := newCLIRepositoryStateGitRepo(t)
	pack := newCLIRepositoryStatePack(t)
	args := append([]string{"--json", "repo-state", "init"}, cliRepositoryStateFlags(repository, pack)...)
	stdout, stderr, err := executeRootCommand(args...)
	if err != nil {
		t.Fatalf("plan error=%v stderr=%s stdout=%s", err, stderr, stdout)
	}
	var plan repostate.ChangePlan
	if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
		t.Fatalf("decode plan: %v\n%s", err, stdout)
	}
	if plan.Digest == "" || !plan.ApplyAllowed || plan.Kind != repostate.ChangeInitialize {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	if _, err := os.Stat(filepath.Join(repository, repostate.StateRoot)); !os.IsNotExist(err) {
		t.Fatalf("dry-run created .repo: %v", err)
	}

	missingConfirmation := append(append([]string{"repo-state", "init"}, cliRepositoryStateFlags(repository, pack)...), "--apply", "--plan-digest", plan.Digest)
	if _, _, err := executeRootCommand(missingConfirmation...); err == nil || !strings.Contains(err.Error(), "confirmation") {
		t.Fatalf("missing confirmation error=%v", err)
	}
	applyArgs := append(append([]string{"--json", "repo-state", "init"}, cliRepositoryStateFlags(repository, pack)...), "--apply", "--plan-digest", plan.Digest, "--yes")
	stdout, stderr, err = executeRootCommand(applyArgs...)
	if err != nil {
		t.Fatalf("apply error=%v stderr=%s stdout=%s", err, stderr, stdout)
	}
	var result repostate.ApplyResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode apply result: %v\n%s", err, stdout)
	}
	if !result.Applied || result.CommitCreated || result.Staged || len(result.Written) == 0 {
		t.Fatalf("unexpected apply result: %#v", result)
	}
	status := cliRepositoryStateGit(t, repository, "status", "--porcelain=v1", "--untracked-files=all")
	if !strings.Contains(status, "?? .repo/") {
		t.Fatalf("CLI staged or committed initialization: %q", status)
	}
}

func TestRepositoryStateValidateCLIUsesOptionalExplicitMembership(t *testing.T) {
	repository := newCLIRepositoryStateGitRepo(t)
	pack := newCLIRepositoryStatePack(t)
	planOutput, _, err := executeRootCommand(append([]string{"--json", "repo-state", "init"}, cliRepositoryStateFlags(repository, pack)...)...)
	if err != nil {
		t.Fatal(err)
	}
	var plan repostate.ChangePlan
	if err := json.Unmarshal([]byte(planOutput), &plan); err != nil {
		t.Fatal(err)
	}
	if _, _, err := executeRootCommand(append(append([]string{"repo-state", "init"}, cliRepositoryStateFlags(repository, pack)...), "--apply", "--plan-digest", plan.Digest, "--yes")...); err != nil {
		t.Fatal(err)
	}
	cliRepositoryStateGit(t, repository, "add", repostate.StateRoot)
	cliRepositoryStateGit(t, repository, "-c", "user.name=LOOM Test", "-c", "user.email=loom-test@example.invalid", "commit", "-qm", "add repository state")

	stdout, stderr, err := executeRootCommand(
		"--json", "repo-state", "validate", "--path", repository,
		"--repository-id", cliRepositoryID,
		"--owner-project-id", cliProjectID,
		"--owner-project-slug", "example",
		"--role", "primary",
	)
	if err != nil {
		t.Fatalf("validate error=%v stderr=%s stdout=%s", err, stderr, stdout)
	}
	var extraction repostate.Extraction
	if err := json.Unmarshal([]byte(stdout), &extraction); err != nil {
		t.Fatalf("decode validation: %v\n%s", err, stdout)
	}
	if extraction.TrackingStatus != repostate.TrackingValid || len(extraction.Validation.Issues) != 0 {
		t.Fatalf("validated status=%s issues=%#v", extraction.TrackingStatus, extraction.Validation.Issues)
	}
}

func TestRepositoryStateMigrationPlanCLIReportsWithoutMutation(t *testing.T) {
	repository := newCLIRepositoryStateGitRepo(t)
	pack := newCLIRepositoryStatePack(t)
	files := map[string]string{
		"project.yaml": "schema_version: 1\nproject:\n  name: Example\n",
		"PROJECT.md":   "# Project Definition\n",
		"README.md":    "# Project Control Plane\n",
		"STATE.md":     "# Repository State\n",
		"ROADMAP.md":   "# Repository Roadmap\n",
	}
	for path, content := range files {
		writeCLIRepositoryStateFile(t, filepath.Join(repository, ".project", path), content)
	}
	cliRepositoryStateGit(t, repository, "add", ".project")
	cliRepositoryStateGit(t, repository, "-c", "user.name=LOOM Test", "-c", "user.email=loom-test@example.invalid", "commit", "-qm", "add project state")
	args := append([]string{"--json", "repo-state", "migration-plan"}, cliRepositoryStateFlags(repository, pack)...)
	stdout, stderr, err := executeRootCommand(args...)
	if err != nil {
		t.Fatalf("migration plan error=%v stderr=%s stdout=%s", err, stderr, stdout)
	}
	var plan repostate.ChangePlan
	if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
		t.Fatalf("decode migration plan: %v\n%s", err, stdout)
	}
	if plan.Kind != repostate.ChangeMigrate || plan.Digest == "" || !plan.ApplyAllowed || plan.Counts.Split == 0 || plan.Counts.Renamed == 0 {
		t.Fatalf("unexpected migration plan: %#v", plan)
	}
	if _, err := os.Stat(filepath.Join(repository, repostate.StateRoot)); !os.IsNotExist(err) {
		t.Fatalf("migration dry-run created .repo: %v", err)
	}
	found := false
	for _, file := range plan.Files {
		if file.Source == ".project/project.yaml" && file.Disposition == repostate.MigrationSplit {
			found = true
		}
	}
	if !found {
		t.Fatal("migration report omitted .project/project.yaml")
	}
}

func TestRepositoryStateCommandsRequireExplicitPathAndCompleteApplyReview(t *testing.T) {
	if _, _, err := executeRootCommand("repo-state", "validate"); err == nil || !strings.Contains(err.Error(), "--path is required") {
		t.Fatalf("validate missing path error=%v", err)
	}
	repository := newCLIRepositoryStateGitRepo(t)
	pack := newCLIRepositoryStatePack(t)
	args := append([]string{"repo-state", "migration-plan"}, cliRepositoryStateFlags(repository, pack)...)
	args = append(args, "--apply", "--yes")
	if _, _, err := executeRootCommand(args...); err == nil || !strings.Contains(err.Error(), "reviewed plan digest") {
		t.Fatalf("apply missing digest error=%v", err)
	}
}

func cliRepositoryStateFlags(repository, pack string) []string {
	return []string{
		"--path", repository,
		"--pack", pack,
		"--repository-id", cliRepositoryID,
		"--repository-name", "Example Repository",
		"--alias", "example",
		"--role", "primary",
		"--purpose", "Exercise the explicit repository state command.",
		"--topic", "example",
		"--owner-project-id", cliProjectID,
		"--owner-project-slug", "example",
		"--stable-branch", "main",
		"--default-branch", "main",
	}
}

func newCLIRepositoryStateGitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeCLIRepositoryStateFile(t, filepath.Join(root, "README.md"), "# Synthetic repository\n")
	cliRepositoryStateGit(t, root, "init", "-q")
	cliRepositoryStateGit(t, root, "add", "README.md")
	cliRepositoryStateGit(t, root, "-c", "user.name=LOOM Test", "-c", "user.email=loom-test@example.invalid", "commit", "-qm", "fixture")
	return root
}

func newCLIRepositoryStatePack(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"repo.yaml":     "kind: loom.repository_state\nschema_version: repo.state.v1\n",
		"README.md":     "# Repository Development State\n",
		"REPOSITORY.md": "# Repository Definition\n",
		"STATE.md":      "# Repository State\n\n## Current State\n\nNot initialized.\n\n## Active Focus\n\n- Review.\n\n## Recent Outcomes\n\n- None.\n\n## Blockers\n\n- None.\n",
		"ROADMAP.md":    "# Repository Roadmap\n\n## Next Priorities\n\n1. Review initialization.\n",
	}
	for path, content := range files {
		writeCLIRepositoryStateFile(t, filepath.Join(root, "templates", repostate.StateRoot, filepath.FromSlash(path)), content)
	}
	return root
}

func writeCLIRepositoryStateFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func cliRepositoryStateGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "LANG=C")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
