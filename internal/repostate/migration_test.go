package repostate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testRepositoryID = "repo_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	testProjectID    = "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"
)

func TestInitializationIsDryRunFirstDigestBoundAndNeverCommits(t *testing.T) {
	repository := newMigrationGitRepository(t)
	pack := newRepositoryDevelopmentPack(t)
	options := testChangePlanOptions(repository, pack)
	headBefore := migrationGit(t, repository, "rev-parse", "HEAD")

	first, err := PlanInitialization(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PlanInitialization(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest == "" || first.Digest != second.Digest {
		t.Fatalf("plan digest is not deterministic: %q != %q", first.Digest, second.Digest)
	}
	if !first.ApplyAllowed || !first.Git.Clean || !first.Git.Allowed {
		t.Fatalf("clean initialization plan was blocked: %#v", first)
	}
	if _, err := os.Stat(filepath.Join(repository, StateRoot)); !os.IsNotExist(err) {
		t.Fatalf("dry-run created .repo: %v", err)
	}

	if _, err := ApplyRepositoryStateChange(context.Background(), ChangeInitialize, options, first.Digest, false); err == nil || !strings.Contains(err.Error(), "confirmation") {
		t.Fatalf("apply without confirmation error = %v", err)
	}
	if _, err := ApplyRepositoryStateChange(context.Background(), ChangeInitialize, options, "sha256:"+strings.Repeat("0", 64), true); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("apply with wrong digest error = %v", err)
	}
	result, err := ApplyRepositoryStateChange(context.Background(), ChangeInitialize, options, first.Digest, true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.CommitCreated || result.Staged || len(result.Written) == 0 || result.Plan.Counts.Pending != 0 {
		t.Fatalf("unexpected apply result: %#v", result)
	}
	if got := migrationGit(t, repository, "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("apply created a commit: head %s -> %s", headBefore, got)
	}
	status := migrationGit(t, repository, "status", "--porcelain=v1", "--untracked-files=all")
	if !strings.Contains(status, "?? .repo/") {
		t.Fatalf("created .repo was staged or hidden from review: %q", status)
	}
	manifest, err := os.ReadFile(filepath.Join(repository, RepositoryManifestPath))
	if err != nil {
		t.Fatal(err)
	}
	if !generatedPayloadValid(RepositoryManifestPath, manifest) || !strings.Contains(string(manifest), testRepositoryID) || strings.Contains(string(manifest), "<repository-id>") {
		t.Fatalf("rendered manifest is not a digest-owned explicit initialization: %s", manifest)
	}
}

func TestInitializationOverwriteRequiresUnmodifiedGeneratedOwnership(t *testing.T) {
	repository := newMigrationGitRepository(t)
	pack := newRepositoryDevelopmentPack(t)
	options := testChangePlanOptions(repository, pack)
	plan, err := PlanInitialization(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyRepositoryStateChange(context.Background(), ChangeInitialize, options, plan.Digest, true); err != nil {
		t.Fatal(err)
	}

	packState := filepath.Join(pack, "templates", StateRoot, "STATE.md")
	writeMigrationFixture(t, packState, "# Repository State\n\n## Current State\n\nUpdated pack.\n", 0o644)
	upgrade, err := PlanInitialization(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	target := findMigrationTarget(t, upgrade, ".repo/STATE.md")
	if target.Status != MigrationPending || !target.Overwrite {
		t.Fatalf("unmodified generated target was not eligible for digest-proven overwrite: %#v", target)
	}
	if _, err := ApplyRepositoryStateChange(context.Background(), ChangeInitialize, options, upgrade.Digest, true); err != nil {
		t.Fatal(err)
	}

	repositoryState := filepath.Join(repository, StateRoot, "STATE.md")
	payload, err := os.ReadFile(repositoryState)
	if err != nil {
		t.Fatal(err)
	}
	payload = append(payload, []byte("manual edit\n")...)
	writeMigrationFixture(t, repositoryState, string(payload), 0o644)
	writeMigrationFixture(t, packState, "# Repository State\n\n## Current State\n\nAnother pack version.\n", 0o644)
	conflict, err := PlanInitialization(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	target = findMigrationTarget(t, conflict, ".repo/STATE.md")
	if target.Status != MigrationConflict || target.Overwrite {
		t.Fatalf("manually edited generated target was not protected: %#v", target)
	}
	before := string(payload)
	if _, err := ApplyRepositoryStateChange(context.Background(), ChangeInitialize, options, conflict.Digest, true); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflicting apply error = %v", err)
	}
	after, err := os.ReadFile(repositoryState)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != before {
		t.Fatal("conflicting target was overwritten")
	}
}

func TestApplyRejectsRepositoryAncestorReplacementBeforeFirstWrite(t *testing.T) {
	workspace := t.TempDir()
	holder := filepath.Join(workspace, "holder")
	repository := filepath.Join(holder, "repository")
	newMigrationGitRepositoryAt(t, repository)
	pack := newRepositoryDevelopmentPack(t)
	options := testChangePlanOptions(repository, pack)
	initial, err := PlanInitialization(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyRepositoryStateChange(context.Background(), ChangeInitialize, options, initial.Digest, true); err != nil {
		t.Fatal(err)
	}
	originalState := mustReadMigrationFile(t, filepath.Join(repository, StateRoot, "STATE.md"))

	replacementHolder := filepath.Join(workspace, "replacement-holder")
	replacementRepository := filepath.Join(replacementHolder, "repository")
	replacementStatePath := filepath.Join(replacementRepository, StateRoot, "STATE.md")
	writeMigrationFixture(t, replacementStatePath, "replacement repository state\n", 0o644)
	replacementState := mustReadMigrationFile(t, replacementStatePath)

	writeMigrationFixture(t, filepath.Join(pack, "templates", StateRoot, "STATE.md"), "# Repository State\n\n## Current State\n\nReviewed update.\n", 0o644)
	upgrade, err := PlanInitialization(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	originalHolder := filepath.Join(workspace, "original-holder")
	options.applyHooks = &repositoryStateApplyHooks{afterReplan: func() {
		if err := os.Rename(holder, originalHolder); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacementHolder, holder); err != nil {
			t.Fatal(err)
		}
	}}
	result, err := ApplyRepositoryStateChange(context.Background(), ChangeInitialize, options, upgrade.Digest, true)
	if err == nil || !strings.Contains(err.Error(), "root pathname or ancestor binding changed") {
		t.Fatalf("ancestor replacement apply error = %v", err)
	}
	if len(result.Written) != 0 {
		t.Fatalf("ancestor replacement wrote targets: %#v", result.Written)
	}
	if got := mustReadMigrationFile(t, filepath.Join(originalHolder, "repository", StateRoot, "STATE.md")); string(got) != string(originalState) {
		t.Fatal("renamed original repository target changed before the first write")
	}
	if got := mustReadMigrationFile(t, filepath.Join(holder, "repository", StateRoot, "STATE.md")); string(got) != string(replacementState) {
		t.Fatal("replacement repository target changed before the first write")
	}
}

func TestApplyRejectsRepositoryReplacementBetweenWritesAndReportsEarlierWrite(t *testing.T) {
	workspace := t.TempDir()
	repository := filepath.Join(workspace, "repository")
	newMigrationGitRepositoryAt(t, repository)
	pack := newRepositoryDevelopmentPack(t)
	options := testChangePlanOptions(repository, pack)
	initial, err := PlanInitialization(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyRepositoryStateChange(context.Background(), ChangeInitialize, options, initial.Digest, true); err != nil {
		t.Fatal(err)
	}
	originalRepositoryDefinition := mustReadMigrationFile(t, filepath.Join(repository, StateRoot, "REPOSITORY.md"))

	replacementRepository := filepath.Join(workspace, "replacement")
	replacementReadmePath := filepath.Join(replacementRepository, StateRoot, "README.md")
	replacementDefinitionPath := filepath.Join(replacementRepository, StateRoot, "REPOSITORY.md")
	writeMigrationFixture(t, replacementReadmePath, "replacement readme\n", 0o644)
	writeMigrationFixture(t, replacementDefinitionPath, "replacement definition\n", 0o644)
	replacementReadme := mustReadMigrationFile(t, replacementReadmePath)
	replacementDefinition := mustReadMigrationFile(t, replacementDefinitionPath)

	writeMigrationFixture(t, filepath.Join(pack, "templates", StateRoot, "README.md"), "# Updated Repository Development State\n", 0o644)
	writeMigrationFixture(t, filepath.Join(pack, "templates", StateRoot, "REPOSITORY.md"), "# Updated Repository Definition\n", 0o644)
	upgrade, err := PlanInitialization(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	originalRepository := filepath.Join(workspace, "original")
	options.applyHooks = &repositoryStateApplyHooks{afterWrite: func(completed int, path string) {
		if completed != 1 {
			return
		}
		if path != ".repo/README.md" {
			t.Fatalf("first deterministic write = %s", path)
		}
		if err := os.Rename(repository, originalRepository); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacementRepository, repository); err != nil {
			t.Fatal(err)
		}
	}}
	result, err := ApplyRepositoryStateChange(context.Background(), ChangeInitialize, options, upgrade.Digest, true)
	if err == nil || !strings.Contains(err.Error(), "root pathname or ancestor binding changed") {
		t.Fatalf("between-write replacement error = %v", err)
	}
	if len(result.Written) != 1 || result.Written[0] != ".repo/README.md" {
		t.Fatalf("partial apply truth = %#v", result.Written)
	}
	if got := mustReadMigrationFile(t, filepath.Join(originalRepository, StateRoot, "README.md")); !strings.Contains(string(got), "Updated Repository Development State") {
		t.Fatal("renamed original repository did not retain the earlier completed write")
	}
	if got := mustReadMigrationFile(t, filepath.Join(originalRepository, StateRoot, "REPOSITORY.md")); string(got) != string(originalRepositoryDefinition) {
		t.Fatal("renamed original repository later target changed")
	}
	if got := mustReadMigrationFile(t, filepath.Join(repository, StateRoot, "README.md")); string(got) != string(replacementReadme) {
		t.Fatal("replacement repository first target changed")
	}
	if got := mustReadMigrationFile(t, filepath.Join(repository, StateRoot, "REPOSITORY.md")); string(got) != string(replacementDefinition) {
		t.Fatal("replacement repository later target changed")
	}
}

func TestGeneratedReplacementWriteFailurePreservesPreviousFile(t *testing.T) {
	repository := newMigrationGitRepository(t)
	pack := newRepositoryDevelopmentPack(t)
	options := testChangePlanOptions(repository, pack)
	initial, err := PlanInitialization(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyRepositoryStateChange(context.Background(), ChangeInitialize, options, initial.Digest, true); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(repository, StateRoot, "STATE.md")
	previous := mustReadMigrationFile(t, targetPath)
	writeMigrationFixture(t, filepath.Join(pack, "templates", StateRoot, "STATE.md"), "# Repository State\n\n## Current State\n\nAtomic update.\n", 0o644)
	upgrade, err := PlanInitialization(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	options.applyHooks = &repositoryStateApplyHooks{writeReplacementTemp: func(file *os.File, payload []byte) error {
		if _, err := file.Write(payload[:min(13, len(payload))]); err != nil {
			return err
		}
		return errors.New("injected replacement short write")
	}}
	result, err := ApplyRepositoryStateChange(context.Background(), ChangeInitialize, options, upgrade.Digest, true)
	if err == nil || !strings.Contains(err.Error(), "injected replacement short write") {
		t.Fatalf("replacement write failure error = %v", err)
	}
	if len(result.Written) != 0 {
		t.Fatalf("failed replacement reported writes: %#v", result.Written)
	}
	if got := mustReadMigrationFile(t, targetPath); string(got) != string(previous) {
		t.Fatal("failed atomic replacement damaged the previous generated file")
	}
	assertNoMigrationTemporaryFiles(t, filepath.Dir(targetPath))
}

func TestGeneratedReplacementTargetSwapPreservesReplacementFile(t *testing.T) {
	repository := newMigrationGitRepository(t)
	pack := newRepositoryDevelopmentPack(t)
	options := testChangePlanOptions(repository, pack)
	initial, err := PlanInitialization(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyRepositoryStateChange(context.Background(), ChangeInitialize, options, initial.Digest, true); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(repository, StateRoot, "STATE.md")
	previous := mustReadMigrationFile(t, targetPath)
	writeMigrationFixture(t, filepath.Join(pack, "templates", StateRoot, "STATE.md"), "# Repository State\n\n## Current State\n\nAtomic update.\n", 0o644)
	upgrade, err := PlanInitialization(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(repository, StateRoot, "STATE.previous.md")
	options.applyHooks = &repositoryStateApplyHooks{beforeReplacementVerify: func(path string) {
		if path != ".repo/STATE.md" {
			t.Fatalf("replacement verification hook path = %s", path)
		}
		if err := os.Rename(targetPath, backupPath); err != nil {
			t.Fatal(err)
		}
		writeMigrationFixture(t, targetPath, string(previous), 0o644)
	}}
	result, err := ApplyRepositoryStateChange(context.Background(), ChangeInitialize, options, upgrade.Digest, true)
	if err == nil || !strings.Contains(err.Error(), "identity changed after planning") {
		t.Fatalf("target swap error = %v", err)
	}
	if len(result.Written) != 0 {
		t.Fatalf("refused target swap reported writes: %#v", result.Written)
	}
	if got := mustReadMigrationFile(t, backupPath); string(got) != string(previous) {
		t.Fatal("accepted generated target changed during refused swap")
	}
	if got := mustReadMigrationFile(t, targetPath); string(got) != string(previous) {
		t.Fatal("concurrent replacement file changed during refused swap")
	}
	assertNoMigrationTemporaryFiles(t, filepath.Dir(targetPath))
}

func TestInitializationPlanDigestBindsTargetMode(t *testing.T) {
	repository := newMigrationGitRepository(t)
	pack := newRepositoryDevelopmentPack(t)
	options := testChangePlanOptions(repository, pack)
	first, err := PlanInitialization(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(pack, "templates", StateRoot, "README.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	second, err := PlanInitialization(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest == second.Digest {
		t.Fatal("plan digest did not change with the reviewed target mode")
	}
	target := findMigrationTarget(t, second, ".repo/README.md")
	if target.Mode != "0755" {
		t.Fatalf("reviewed target mode = %q, want 0755", target.Mode)
	}
}

func TestInitializationGeneratedOwnershipIsTargetBound(t *testing.T) {
	repository := newMigrationGitRepository(t)
	pack := newRepositoryDevelopmentPack(t)
	options := testChangePlanOptions(repository, pack)
	plan, err := PlanInitialization(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyRepositoryStateChange(context.Background(), ChangeInitialize, options, plan.Digest, true); err != nil {
		t.Fatal(err)
	}
	statePayload, err := os.ReadFile(filepath.Join(repository, StateRoot, "STATE.md"))
	if err != nil {
		t.Fatal(err)
	}
	writeMigrationFixture(t, filepath.Join(repository, StateRoot, "README.md"), string(statePayload), 0o644)
	conflict, err := PlanInitialization(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	target := findMigrationTarget(t, conflict, ".repo/README.md")
	if target.Status != MigrationConflict || target.Overwrite {
		t.Fatalf("marker copied from another target proved ownership: %#v", target)
	}
}

func TestInitializationApplyRejectsUnrelatedDirtyGitState(t *testing.T) {
	repository := newMigrationGitRepository(t)
	pack := newRepositoryDevelopmentPack(t)
	options := testChangePlanOptions(repository, pack)
	writeMigrationFixture(t, filepath.Join(repository, "unrelated.txt"), "dirty\n", 0o644)
	plan, err := PlanInitialization(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Git.Allowed || plan.ApplyAllowed || len(plan.Git.BlockingPaths) != 1 || plan.Git.BlockingPaths[0] != "unrelated.txt" {
		t.Fatalf("unrelated dirty path was not blocked: %#v", plan.Git)
	}
	if _, err := ApplyRepositoryStateChange(context.Background(), ChangeInitialize, options, plan.Digest, true); err == nil || !strings.Contains(err.Error(), "Git posture") {
		t.Fatalf("dirty apply error = %v", err)
	}
}

func TestInitializationApplyRejectsUnplannedRepoStateChanges(t *testing.T) {
	repository := newMigrationGitRepository(t)
	pack := newRepositoryDevelopmentPack(t)
	options := testChangePlanOptions(repository, pack)
	initial, err := PlanInitialization(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyRepositoryStateChange(context.Background(), ChangeInitialize, options, initial.Digest, true); err != nil {
		t.Fatal(err)
	}
	writeMigrationFixture(t, filepath.Join(repository, StateRoot, "foreign.md"), "repository-owned\n", 0o644)
	plan, err := PlanInitialization(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Git.Allowed || plan.ApplyAllowed || len(plan.Git.BlockingPaths) != 1 || plan.Git.BlockingPaths[0] != ".repo/foreign.md" {
		t.Fatalf("unplanned .repo path was not blocked: %#v", plan.Git)
	}
}

func TestInitializationRejectsSymlinkTargetsWithoutTouchingReferent(t *testing.T) {
	repository := newMigrationGitRepository(t)
	pack := newRepositoryDevelopmentPack(t)
	outside := filepath.Join(t.TempDir(), "outside.md")
	writeMigrationFixture(t, outside, "outside stays unchanged\n", 0o644)
	if err := os.Mkdir(filepath.Join(repository, StateRoot), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repository, StateRoot, "STATE.md")); err != nil {
		t.Fatal(err)
	}
	options := testChangePlanOptions(repository, pack)
	plan, err := PlanInitialization(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	target := findMigrationTarget(t, plan, ".repo/STATE.md")
	if target.Status != MigrationConflict || plan.ApplyAllowed {
		t.Fatalf("symlink target was not reported as a conflict: %#v", target)
	}
	if _, err := ApplyRepositoryStateChange(context.Background(), ChangeInitialize, options, plan.Digest, true); err == nil {
		t.Fatal("apply accepted a symlink target conflict")
	}
	payload, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "outside stays unchanged\n" {
		t.Fatalf("symlink referent changed: %q", payload)
	}
}

func TestInitializationRejectsSymlinkedPackSources(t *testing.T) {
	repository := newMigrationGitRepository(t)
	pack := newRepositoryDevelopmentPack(t)
	target := filepath.Join(pack, "outside.md")
	writeMigrationFixture(t, target, "outside\n", 0o644)
	packState := filepath.Join(pack, "templates", StateRoot, "STATE.md")
	if err := os.Remove(packState); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, packState); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanInitialization(context.Background(), testChangePlanOptions(repository, pack)); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("symlinked pack source error = %v", err)
	}
}

func TestInitializationRejectsSymlinkedPackTreeRoot(t *testing.T) {
	repository := newMigrationGitRepository(t)
	pack := newRepositoryDevelopmentPack(t)
	packTree := filepath.Join(pack, "templates", StateRoot)
	outside := filepath.Join(t.TempDir(), StateRoot)
	if err := os.Rename(packTree, outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, packTree); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanInitialization(context.Background(), testChangePlanOptions(repository, pack)); err == nil || !strings.Contains(err.Error(), "symlinked components") {
		t.Fatalf("symlinked pack tree error = %v", err)
	}
}

func TestProjectMigrationRejectsSymlinkedProjectTreeRoot(t *testing.T) {
	repository := newMigrationGitRepository(t)
	outside := filepath.Join(t.TempDir(), ".project")
	writeMigrationFixture(t, filepath.Join(outside, "STATE.md"), "# Outside\n", 0o644)
	if err := os.Symlink(outside, filepath.Join(repository, ".project")); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanProjectStateMigration(context.Background(), testChangePlanOptions(repository, newRepositoryDevelopmentPack(t))); err == nil || !strings.Contains(err.Error(), "symlinked components") {
		t.Fatalf("symlinked .project tree error = %v", err)
	}
}

func TestProjectMigrationReportsEverySourceAndDispositionWithoutMutation(t *testing.T) {
	repository := t.TempDir()
	pack := newRepositoryDevelopmentPack(t)
	fixtures := map[string]string{
		".project/project.yaml":                          "schema_version: 1\nproject:\n  name: Example\n",
		".project/PROJECT.md":                            "# Project Definition\n\nRepository-local guidance.\n",
		".project/README.md":                             "# Project Control Plane\n",
		".project/STATE.md":                              "# Repository State\n",
		".project/ROADMAP.md":                            "# Repository Roadmap\n",
		".project/protocols/AI_WORKTREE_WORKFLOW.md":     "# Legacy Harness Workflow\n",
		".project/archive/history.md":                    "# History\n",
		".project/features/example/worktree_progress.md": "# Progress\n\nBranch: `codex/example`\n",
		".project/future/idea/outline.md":                "# Idea\n",
		".project/templates/future/outline.md":           "# Future template\n",
		".project/unknown.txt":                           "unmapped\n",
	}
	for path, content := range fixtures {
		writeMigrationFixture(t, filepath.Join(repository, filepath.FromSlash(path)), content, 0o644)
	}
	writeMigrationFixture(t, filepath.Join(repository, StateRoot, "STATE.md"), fixtures[".project/STATE.md"], 0o644)
	initMigrationGitRepository(t, repository)
	options := testChangePlanOptions(repository, pack)
	before := migrationGit(t, repository, "status", "--porcelain=v1", "--untracked-files=all")
	plan, err := PlanProjectStateMigration(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	after := migrationGit(t, repository, "status", "--porcelain=v1", "--untracked-files=all")
	if before != after {
		t.Fatalf("migration planning mutated Git state: before=%q after=%q", before, after)
	}
	if plan.Counts.Preserved == 0 || plan.Counts.Renamed == 0 || plan.Counts.Split == 0 || plan.Counts.Archived == 0 || plan.Counts.Skipped == 0 || plan.Counts.Conflicting == 0 {
		t.Fatalf("migration report omitted a required class: %#v", plan.Counts)
	}
	for _, file := range plan.Files {
		if file.Source == ".project/future/idea/outline.md" && (file.Disposition != MigrationSplit || file.Status != MigrationConflict) {
			t.Fatalf("split source lost its disposition while reporting a conflict: %#v", file)
		}
		if (file.Source == ".project/features/example/worktree_progress.md" || file.Source == ".project/templates/future/outline.md") && file.Disposition != MigrationSplit {
			t.Fatalf("compatibility-map split source was misclassified: %#v", file)
		}
	}
	reported := map[string]int{}
	for _, file := range plan.Files {
		if strings.HasPrefix(file.Source, ".project/") {
			reported[file.Source]++
		}
	}
	for path := range fixtures {
		if reported[path] != 1 {
			t.Fatalf("source %s reported %d times, want exactly once", path, reported[path])
		}
	}
	if plan.ApplyAllowed {
		t.Fatal("migration with unresolved lifecycle and unknown source conflicts allowed apply")
	}
}

func TestProjectMigrationRejectsDuplicateTargetClaims(t *testing.T) {
	repository := t.TempDir()
	pack := newRepositoryDevelopmentPack(t)
	fixtures := map[string]string{
		".project/protocols/AI_WORKTREE_WORKFLOW.md": "# Legacy shared workflow\n",
		".project/protocols/CODEX_WORKFLOW.md":       "# Legacy Codex workflow\n",
	}
	for path, content := range fixtures {
		writeMigrationFixture(t, filepath.Join(repository, filepath.FromSlash(path)), content, 0o644)
	}
	initMigrationGitRepository(t, repository)
	plan, err := PlanProjectStateMigration(context.Background(), testChangePlanOptions(repository, pack))
	if err != nil {
		t.Fatal(err)
	}
	if plan.ApplyAllowed || plan.Counts.Conflicting != 2 {
		t.Fatalf("duplicate target claims were not blocked: %#v", plan.Counts)
	}
	conflictingSources := map[string]bool{}
	for _, file := range plan.Files {
		for _, target := range file.Targets {
			if target.Path == ".repo/protocols/CODEX_WORKFLOW.md" && target.Status == MigrationConflict && strings.Contains(target.Reason, "multiple migration sources") {
				conflictingSources[file.Source] = true
			}
		}
	}
	for source := range fixtures {
		if !conflictingSources[source] {
			t.Fatalf("duplicate target claim from %s was not reported", source)
		}
	}
}

func TestProjectMigrationRejectsHostPathsInLifecycleManifests(t *testing.T) {
	repository := t.TempDir()
	pack := newRepositoryDevelopmentPack(t)
	writeMigrationFixture(t, filepath.Join(repository, ".project", "features", "unsafe", "feature.yaml"), `title: Unsafe
status: planned
priority: medium
dependencies: []
created_at: 2026-08-31
updated_at: 2026-08-31
runtime_path: /Users/example/worktree
`, 0o644)
	initMigrationGitRepository(t, repository)
	plan, err := PlanProjectStateMigration(context.Background(), testChangePlanOptions(repository, pack))
	if err != nil {
		t.Fatal(err)
	}
	if plan.ApplyAllowed || plan.Counts.Conflicting != 1 {
		t.Fatalf("unsafe lifecycle manifest was not blocked: %#v", plan.Counts)
	}
	for _, file := range plan.Files {
		if file.Source == ".project/features/unsafe/feature.yaml" && (file.Disposition != MigrationPreserved || file.Status != MigrationConflict) {
			t.Fatalf("unsafe lifecycle manifest report = %#v", file)
		}
	}
}

func TestProjectMigrationApplyCreatesOnlyRepoAndPreservesProjectSource(t *testing.T) {
	repository := t.TempDir()
	pack := newRepositoryDevelopmentPack(t)
	fixtures := map[string]string{
		".project/project.yaml": "schema_version: 1\nproject:\n  name: Example\n",
		".project/PROJECT.md":   "# Project Definition\n\nPortable guidance.\n",
		".project/README.md":    "# Project Control Plane\n",
		".project/STATE.md":     "# Repository State\n\n## Current State\n\nReady.\n",
		".project/ROADMAP.md":   "# Repository Roadmap\n\n## Next Priorities\n\n1. Review migration.\n",
	}
	for path, content := range fixtures {
		writeMigrationFixture(t, filepath.Join(repository, filepath.FromSlash(path)), content, 0o644)
	}
	initMigrationGitRepository(t, repository)
	options := testChangePlanOptions(repository, pack)
	plan, err := PlanProjectStateMigration(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.ApplyAllowed || plan.Counts.Conflicting != 0 {
		t.Fatalf("bounded migration plan was blocked: %#v", plan.Counts)
	}
	projectBefore, err := os.ReadFile(filepath.Join(repository, ".project", "STATE.md"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := ApplyRepositoryStateChange(context.Background(), ChangeMigrate, options, plan.Digest, true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.CommitCreated || result.Staged {
		t.Fatalf("unexpected migration apply result: %#v", result)
	}
	projectAfter, err := os.ReadFile(filepath.Join(repository, ".project", "STATE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(projectAfter) != string(projectBefore) {
		t.Fatal("migration apply changed .project source")
	}
	if _, err := os.Stat(filepath.Join(repository, RepositoryManifestPath)); err != nil {
		t.Fatalf("migration did not create .repo manifest: %v", err)
	}
	repositoryReadme, err := os.ReadFile(filepath.Join(repository, StateRoot, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(repositoryReadme), "Repository Control Plane") || strings.Contains(string(repositoryReadme), "Project Control Plane") {
		t.Fatalf("migration did not rewrite repository index terminology: %s", repositoryReadme)
	}
	status := migrationGit(t, repository, "status", "--porcelain=v1", "--untracked-files=all")
	if !strings.Contains(status, "?? .repo/") {
		t.Fatalf("migration output was not left reviewable and uncommitted: %q", status)
	}
}

func testChangePlanOptions(repository, pack string) ChangePlanOptions {
	return ChangePlanOptions{
		RepositoryRoot: repository,
		PackRoot:       pack,
		Manifest: RepositoryManifest{
			Kind:          RepositoryManifestKind,
			SchemaVersion: RepositorySchemaVersion,
			Repository: RepositoryIdentity{
				ID: testRepositoryID, Name: "Example Repository", Aliases: []string{"example"},
				Role: RepositoryRolePrimary, Purpose: "Exercise explicit repository state planning.", Topics: []string{"example", "go"},
			},
			OwnerProject: OwnerProjectBacklink{ID: testProjectID, Slug: "example"},
			Branches:     RepositoryBranches{Stable: "main", Default: "main"},
			Source:       RepositorySourcePolicy{Tracking: "git", StateRoot: StateRoot},
		},
	}
}

func newRepositoryDevelopmentPack(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"repo.yaml":                              "kind: loom.repository_state\nschema_version: repo.state.v1\n",
		"README.md":                              "# Repository Development State\n",
		"REPOSITORY.md":                          "# Repository Definition\n",
		"STATE.md":                               "# Repository State\n\n## Current State\n\nNot initialized.\n",
		"ROADMAP.md":                             "# Repository Roadmap\n\n## Next Priorities\n\n1. Review initialization.\n",
		"protocols/WORKTREE_OWNERSHIP.md":        "# Worktree Ownership\n",
		"protocols/CODEX_WORKFLOW.md":            "# Codex Workflow\n",
		"protocols/ORCA_WORKFLOW.md":             "# ORCA Workflow\n",
		"protocols/REPOSITORY_STATE_PROTOCOL.md": "# Repository State Protocol\n",
	}
	for path, content := range files {
		writeMigrationFixture(t, filepath.Join(root, "templates", StateRoot, filepath.FromSlash(path)), content, 0o644)
	}
	return root
}

func newMigrationGitRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	newMigrationGitRepositoryAt(t, root)
	return root
}

func newMigrationGitRepositoryAt(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMigrationFixture(t, filepath.Join(root, "README.md"), "# Synthetic repository\n", 0o644)
	initMigrationGitRepository(t, root)
}

func initMigrationGitRepository(t *testing.T, root string) {
	t.Helper()
	migrationGit(t, root, "init", "-q")
	migrationGit(t, root, "add", ".")
	migrationGit(t, root, "-c", "user.name=LOOM Test", "-c", "user.email=loom-test@example.invalid", "commit", "-qm", "fixture")
}

func migrationGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", root}, args...)
	command := exec.Command("git", commandArgs...)
	command.Env = repositoryGitEnvironment(os.Environ())
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeMigrationFixture(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func mustReadMigrationFile(t *testing.T, path string) []byte {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func assertNoMigrationTemporaryFiles(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".loom-repostate-") {
			t.Fatalf("temporary migration target remains after failure: %s", entry.Name())
		}
	}
}

func findMigrationTarget(t *testing.T, plan ChangePlan, path string) MigrationTarget {
	t.Helper()
	for _, file := range plan.Files {
		for _, target := range file.Targets {
			if target.Path == path {
				return target
			}
		}
	}
	t.Fatalf("target %s not found in plan", path)
	return MigrationTarget{}
}
