package projectstate

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOSGitCommandRunnerLocksDownObservationProcess(t *testing.T) {
	binDirectory := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(binDirectory, 0o700); err != nil {
		t.Fatalf("mkdir fake git bin: %v", err)
	}
	fakeGit := filepath.Join(binDirectory, "git")
	mustWriteExecutable(t, fakeGit, `#!/bin/sh
printf '%s\n' "$GIT_OPTIONAL_LOCKS" "$GIT_TERMINAL_PROMPT" "$LC_ALL" "$LANG" "$@"
`)
	t.Setenv("PATH", binDirectory)
	t.Setenv("GIT_OPTIONAL_LOCKS", "1")
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	t.Setenv("LC_ALL", "inherited-locale")
	t.Setenv("LANG", "inherited-language")

	output, err := (OSGitCommandRunner{}).Run(context.Background(), "/fixture/repository", "status", "--porcelain=v2")
	if err != nil {
		t.Fatalf("run locked observation command: %v", err)
	}
	want := strings.Join([]string{
		"0",
		"0",
		"C",
		"C",
		"-c",
		"core.fsmonitor=false",
		"-C",
		"/fixture/repository",
		"status",
		"--porcelain=v2",
		"",
	}, "\n")
	if string(output) != want {
		t.Fatalf("locked process boundary = %q, want %q", output, want)
	}
}

func TestParseGitStatusPorcelainV2CoversAheadBehindDirtyAndSubmodules(t *testing.T) {
	dirty := readGitStatusFixture(t, "ahead-behind-dirty.txt")
	if dirty.CurrentBranch != "feature/observation" || dirty.HeadPosture != GitHeadObserved || dirty.Ahead != 2 || dirty.Behind != 3 || !dirty.AheadBehindObserved {
		t.Fatalf("unexpected branch projection: %#v", dirty)
	}
	if !dirty.Dirty.Dirty || !dirty.Dirty.TrackedChanges || !dirty.Dirty.UntrackedChanges || dirty.Dirty.SubmoduleChanges {
		t.Fatalf("unexpected dirty projection: %#v", dirty.Dirty)
	}

	submodule := readGitStatusFixture(t, "submodule-conflict.txt")
	if !submodule.Dirty.SubmoduleChanges || !submodule.Dirty.Conflicts || !submodule.Dirty.TrackedChanges {
		t.Fatalf("unexpected submodule/conflict projection: %#v", submodule.Dirty)
	}
}

func TestGitAdapterObservesRepositoryAndLinkedWorktreeWithoutCanonicalizingIt(t *testing.T) {
	repository := filepath.Join(t.TempDir(), "repository")
	mustRunGit(t, "", "init", repository)
	mustRunGit(t, repository, "config", "user.email", "test@example.invalid")
	mustRunGit(t, repository, "config", "user.name", "LOOM Test")
	mustWriteFile(t, filepath.Join(repository, "tracked.txt"), "initial\n")
	mustRunGit(t, repository, "add", "tracked.txt")
	mustRunGit(t, repository, "commit", "-m", "initial")
	mustRunGit(t, repository, "branch", "-M", "main")
	remote := filepath.Join(filepath.Dir(repository), "remote.git")
	mustRunGit(t, "", "init", "--bare", remote)
	mustRunGit(t, repository, "remote", "add", "origin", remote)
	mustRunGit(t, repository, "push", "-u", "origin", "main")
	mustRunGit(t, repository, "remote", "set-head", "origin", "main")

	adapter := GitAdapter{}
	clean, err := adapter.Observe(context.Background(), repository)
	if err != nil {
		t.Fatalf("observe clean repository: %v", err)
	}
	if clean.Worktree || clean.WorktreeCanonicalProjectState || clean.CurrentBranch != "main" || clean.DefaultBranch != "origin/main" || clean.DefaultBranchPosture != GitDefaultBranchObserved || !clean.AheadBehindObserved || clean.Ahead != 0 || clean.Behind != 0 || clean.HeadPosture != GitHeadObserved || clean.Dirty.Dirty || !clean.RootMatchesMember {
		t.Fatalf("unexpected clean observation: %#v", clean)
	}
	if !strings.HasPrefix(clean.LocalIdentityDigest, "sha256:") || len(clean.LocalIdentityDigest) != len("sha256:")+64 {
		t.Fatalf("local identity digest = %q", clean.LocalIdentityDigest)
	}

	mustWriteFile(t, filepath.Join(repository, "tracked.txt"), "changed\n")
	mustWriteFile(t, filepath.Join(repository, "untracked.txt"), "new\n")
	dirty, err := adapter.Observe(context.Background(), repository)
	if err != nil {
		t.Fatalf("observe dirty repository: %v", err)
	}
	if !dirty.Dirty.Dirty || !dirty.Dirty.TrackedChanges || !dirty.Dirty.UntrackedChanges {
		t.Fatalf("dirty posture = %#v", dirty.Dirty)
	}
	mustRunGit(t, repository, "checkout", "--", "tracked.txt")
	if err := os.Remove(filepath.Join(repository, "untracked.txt")); err != nil {
		t.Fatalf("remove untracked fixture: %v", err)
	}

	linked := filepath.Join(filepath.Dir(repository), "linked")
	mustRunGit(t, repository, "worktree", "add", "-b", "linked-observation", linked)
	worktree, err := adapter.Observe(context.Background(), linked)
	if err != nil {
		t.Fatalf("observe linked worktree: %v", err)
	}
	if !worktree.Worktree || worktree.WorktreeCanonicalProjectState || !worktree.RootMatchesMember {
		t.Fatalf("linked worktree posture = %#v", worktree)
	}

	subdirectory := filepath.Join(repository, "nested")
	if err := os.Mkdir(subdirectory, 0o755); err != nil {
		t.Fatalf("mkdir nested fixture: %v", err)
	}
	_, err = adapter.Observe(context.Background(), subdirectory)
	var observationError *GitObservationError
	if !errors.As(err, &observationError) || observationError.Code != "git_root_mismatch" {
		t.Fatalf("nested root error = %v", err)
	}
}

func TestGitAdapterObservationDoesNotChangeIndex(t *testing.T) {
	repository := filepath.Join(t.TempDir(), "repository")
	mustRunGit(t, "", "init", repository)
	mustRunGit(t, repository, "config", "user.email", "test@example.invalid")
	mustRunGit(t, repository, "config", "user.name", "LOOM Test")
	tracked := filepath.Join(repository, "tracked.txt")
	mustWriteFile(t, tracked, "stable content\n")
	mustRunGit(t, repository, "add", "tracked.txt")
	mustRunGit(t, repository, "commit", "-m", "initial")

	changedTime := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(tracked, changedTime, changedTime); err != nil {
		t.Fatalf("change tracked-file times: %v", err)
	}
	indexPath := filepath.Join(repository, ".git", "index")
	beforeContent, beforeInfo := readFileState(t, indexPath)

	if _, err := (GitAdapter{}).Observe(context.Background(), repository); err != nil {
		t.Fatalf("observe repository: %v", err)
	}
	afterContent, afterInfo := readFileState(t, indexPath)
	if !bytes.Equal(afterContent, beforeContent) {
		t.Fatal("Git observation changed index content")
	}
	if !afterInfo.ModTime().Equal(beforeInfo.ModTime()) {
		t.Fatalf("Git observation changed index modification time: before=%s after=%s", beforeInfo.ModTime(), afterInfo.ModTime())
	}
	if _, err := os.Stat(indexPath + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("index lock remains after observation: %v", err)
	}
}

func TestGitAdapterObservationDoesNotInvokeConfiguredFSMonitor(t *testing.T) {
	repository := filepath.Join(t.TempDir(), "repository")
	mustRunGit(t, "", "init", repository)
	mustRunGit(t, repository, "config", "user.email", "test@example.invalid")
	mustRunGit(t, repository, "config", "user.name", "LOOM Test")
	mustWriteFile(t, filepath.Join(repository, "tracked.txt"), "initial\n")
	mustRunGit(t, repository, "add", "tracked.txt")
	mustRunGit(t, repository, "commit", "-m", "initial")

	helper := filepath.Join(filepath.Dir(repository), "fsmonitor-helper")
	marker := helper + ".invoked"
	mustWriteExecutable(t, helper, `#!/bin/sh
printf invoked > "$0.invoked"
printf 'token\000'
`)
	mustRunGit(t, repository, "config", "core.fsmonitor", helper)
	mustRunGit(t, repository, "status", "--porcelain=v2")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("configured fsmonitor control was not invoked: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatalf("remove fsmonitor marker: %v", err)
	}

	if _, err := (GitAdapter{}).Observe(context.Background(), repository); err != nil {
		t.Fatalf("observe repository with configured fsmonitor: %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Git observation invoked repository-configured fsmonitor: %v", err)
	}
}

func readGitStatusFixture(t *testing.T, name string) GitProjection {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("testdata", "status", name))
	if err != nil {
		t.Fatalf("read status fixture: %v", err)
	}
	payload = []byte(strings.ReplaceAll(strings.TrimSpace(string(payload)), "\n", "\x00") + "\x00")
	projection, err := parseGitStatusPorcelainV2(payload)
	if err != nil {
		t.Fatalf("parse status fixture %s: %v", name, err)
	}
	return projection
}

func mustRunGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	commandArgs := args
	if directory != "" {
		commandArgs = append([]string{"-C", directory}, args...)
	}
	command := exec.Command("git", commandArgs...)
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(commandArgs, " "), err, output)
	}
}

func mustWriteFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatalf("write fixture %s: %v", path, err)
	}
}

func mustWriteExecutable(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o700); err != nil {
		t.Fatalf("write executable fixture %s: %v", path, err)
	}
}

func readFileState(t *testing.T, path string) ([]byte, os.FileInfo) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture state %s: %v", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat fixture state %s: %v", path, err)
	}
	return content, info
}
