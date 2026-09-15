package repostate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOSGitObserverReadsBoundedTrackedSourceWithoutMutatingIndex(t *testing.T) {
	root := t.TempDir()
	runFixtureGit(t, root, "init", "-q")
	runFixtureGit(t, root, "config", "user.name", "LOOM Test")
	runFixtureGit(t, root, "config", "user.email", "loom-test@example.invalid")
	mustWriteRepositoryFixtureFile(t, root, ".repo/repo.yaml", validManifestFixture())
	mustWriteRepositoryFixtureFile(t, root, ".repo/STATE.md", "# State\n")
	runFixtureGit(t, root, "add", ".repo")
	runFixtureGit(t, root, "commit", "-q", "-m", "add repository state")

	observedAt := time.Date(2026, 8, 30, 15, 0, 0, 0, time.UTC)
	observer := OSGitObserver{Now: func() time.Time { return observedAt }}
	clean, err := observer.Observe(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if clean.ObservedAt != observedAt || clean.TrackedState != GitTrackedClean || clean.HeadCommit == "" || clean.SourceCommit != clean.HeadCommit || clean.LatestRepoCommitAt.IsZero() {
		t.Fatalf("clean observation = %#v", clean)
	}
	if len(clean.Entries) != 2 || clean.Entries[0].Path != ".repo/STATE.md" || clean.Entries[1].Path != ".repo/repo.yaml" {
		t.Fatalf("tracked entries = %#v", clean.Entries)
	}

	mustWriteRepositoryFixtureFile(t, root, ".repo/STATE.md", "# Changed State\n")
	mustWriteRepositoryFixtureFile(t, root, ".repo/untracked.md", "# Untracked\n")
	dirty, err := observer.Observe(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if dirty.TrackedState != GitTrackedDirty || len(dirty.UntrackedPaths) != 1 || dirty.UntrackedPaths[0] != ".repo/untracked.md" {
		t.Fatalf("dirty observation = %#v", dirty)
	}
	if staged := strings.TrimSpace(runFixtureGit(t, root, "diff", "--cached", "--name-only")); staged != "" {
		t.Fatalf("observer mutated the index; staged=%q", staged)
	}
}

func TestOSGitObserverRejectsNonRegularTrackedEntry(t *testing.T) {
	root := t.TempDir()
	runFixtureGit(t, root, "init", "-q")
	runFixtureGit(t, root, "config", "user.name", "LOOM Test")
	runFixtureGit(t, root, "config", "user.email", "loom-test@example.invalid")
	mustWriteRepositoryFixtureFile(t, root, ".repo/repo.yaml", validManifestFixture())
	runFixtureGit(t, root, "add", ".repo/repo.yaml")
	runFixtureGit(t, root, "commit", "-q", "-m", "add repository manifest")
	if err := os.Symlink("repo.yaml", filepath.Join(root, ".repo", "link")); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, root, "add", ".repo/link")

	_, err := (OSGitObserver{}).Observe(context.Background(), root)
	var observationError *GitObservationError
	if !errors.As(err, &observationError) || observationError.Code != "non_regular_entry" {
		t.Fatalf("non-regular entry error = %v", err)
	}
}

func TestOSGitObserverRejectsStateRootAndTrackedFileReplacement(t *testing.T) {
	newRepository := func(t *testing.T) string {
		t.Helper()
		root := t.TempDir()
		runFixtureGit(t, root, "init", "-q")
		runFixtureGit(t, root, "config", "user.name", "LOOM Test")
		runFixtureGit(t, root, "config", "user.email", "loom-test@example.invalid")
		mustWriteRepositoryFixtureFile(t, root, ".repo/repo.yaml", validManifestFixture())
		mustWriteRepositoryFixtureFile(t, root, ".repo/STATE.md", "# State\n")
		runFixtureGit(t, root, "add", ".repo")
		runFixtureGit(t, root, "commit", "-q", "-m", "add repository state")
		return root
	}

	t.Run("state root swap", func(t *testing.T) {
		root := newRepository(t)
		outside := t.TempDir()
		mustWriteRepositoryFixtureFile(t, outside, "STATE.md", "outside root sentinel\n")
		var swapErr error
		observer := OSGitObserver{afterStateRootOpen: func() {
			if err := os.Rename(filepath.Join(root, StateRoot), filepath.Join(root, StateRoot+"-held")); err != nil {
				swapErr = err
				return
			}
			swapErr = os.Symlink(outside, filepath.Join(root, StateRoot))
		}}
		_, err := observer.Observe(context.Background(), root)
		if swapErr != nil {
			t.Fatal(swapErr)
		}
		var observationError *GitObservationError
		if !errors.As(err, &observationError) || observationError.Code != "tracked_file_unreadable" {
			t.Fatalf("root-swap observation error = %v", err)
		}
	})

	t.Run("tracked file swap", func(t *testing.T) {
		root := newRepository(t)
		outside := filepath.Join(t.TempDir(), "outside.md")
		if err := os.WriteFile(outside, []byte("outside file sentinel\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		swapped := false
		var swapErr error
		observer := OSGitObserver{beforeTrackedOpen: func(path string) {
			if path != ".repo/STATE.md" || swapped {
				return
			}
			swapped = true
			statePath := filepath.Join(root, StateRoot, "STATE.md")
			if err := os.Rename(statePath, statePath+".held"); err != nil {
				swapErr = err
				return
			}
			swapErr = os.Symlink(outside, statePath)
		}}
		_, err := observer.Observe(context.Background(), root)
		if swapErr != nil {
			t.Fatal(swapErr)
		}
		var observationError *GitObservationError
		if !swapped || !errors.As(err, &observationError) || observationError.Code != "tracked_file_unreadable" {
			t.Fatalf("file-swap observation error = %v", err)
		}
	})
}

func TestDefaultExtractorIsReadOnlyInSyntheticGitRepository(t *testing.T) {
	root := t.TempDir()
	runFixtureGit(t, root, "init", "-q")
	runFixtureGit(t, root, "config", "user.name", "LOOM Test")
	runFixtureGit(t, root, "config", "user.email", "loom-test@example.invalid")
	writeValidRepositoryStateFixture(t, root)
	runFixtureGit(t, root, "add", ".repo")
	runFixtureGit(t, root, "commit", "-q", "-m", "add valid repository state")
	membership := fixtureMembership()

	extraction := (Extractor{}).Extract(context.Background(), ExtractInput{RepositoryRoot: root, Membership: &membership})
	if extraction.TrackingStatus != TrackingValid || extraction.AcceptedContext == nil || len(extraction.AcceptedContext) != 0 {
		t.Fatalf("default extraction = status %q issues=%#v accepted=%#v", extraction.TrackingStatus, extraction.Validation.Issues, extraction.AcceptedContext)
	}
	if status := strings.TrimSpace(runFixtureGit(t, root, "status", "--porcelain")); status != "" {
		t.Fatalf("default extractor changed synthetic repository state: %q", status)
	}
}

func runFixtureGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", directory}, args...)
	command := exec.Command("git", commandArgs...)
	command.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE=2026-08-29T12:00:00Z",
		"GIT_COMMITTER_DATE=2026-08-29T12:00:00Z",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
