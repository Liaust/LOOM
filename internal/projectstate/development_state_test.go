package projectstate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileDevelopmentStateInspectorReportsAbsentValidMalformedAndMismatch(t *testing.T) {
	memberRoot := t.TempDir()
	inspector := FileDevelopmentStateInspector{}
	input := DevelopmentStateInput{
		MemberRoot:   memberRoot,
		StateRoot:    ".repo",
		ProjectID:    "project_expected",
		RepositoryID: "repo_expected",
	}
	if got := inspector.Inspect(context.Background(), input); got.Posture != DevelopmentStateNotEnabled {
		t.Fatalf("absent state posture = %#v", got)
	}
	if err := os.Mkdir(filepath.Join(memberRoot, ".repo"), 0o755); err != nil {
		t.Fatalf("mkdir .repo: %v", err)
	}
	mustWriteFile(t, filepath.Join(memberRoot, ".repo", "repo.yaml"), "repository:\n  id: repo_expected\n  owning_project:\n    id: project_expected\n")
	valid := inspector.Inspect(context.Background(), input)
	if valid.Posture != DevelopmentStateEnabled || valid.ReasonCode != "" || valid.SourceDigest == "" || valid.RelativePath != ".repo/repo.yaml" {
		t.Fatalf("valid state posture = %#v", valid)
	}

	mustWriteFile(t, filepath.Join(memberRoot, ".repo", "repo.yaml"), "repository:\n  id: repo_other\n  project_id: project_expected\n")
	mismatch := inspector.Inspect(context.Background(), input)
	if mismatch.Posture != DevelopmentStateMismatch || mismatch.ReasonCode != "identity_backlink_mismatch" {
		t.Fatalf("mismatched state posture = %#v", mismatch)
	}

	mustWriteFile(t, filepath.Join(memberRoot, ".repo", "repo.yaml"), "repository: [unterminated\n")
	malformed := inspector.Inspect(context.Background(), input)
	if malformed.Posture != DevelopmentStateInvalid || malformed.ReasonCode != "identity_malformed" {
		t.Fatalf("malformed state posture = %#v", malformed)
	}

	mustWriteFile(t, filepath.Join(memberRoot, ".repo", "repo.yaml"), "repository:\n  id: repo_expected\n  id: repo_other\n  project_id: project_expected\n")
	duplicate := inspector.Inspect(context.Background(), input)
	if duplicate.Posture != DevelopmentStateInvalid || duplicate.ReasonCode != "identity_malformed" {
		t.Fatalf("duplicate-key state posture = %#v", duplicate)
	}
}

func TestOSPathResolverRejectsMemberAndStateSymlinkEscapes(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escaped")); err != nil {
		t.Fatalf("symlink escape fixture: %v", err)
	}
	resolver := OSPathResolver{}
	if _, err := resolver.ResolveWithin(root, "escaped"); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("member escape error = %v", err)
	}
	if _, err := resolver.ResolveWithin(root, "../outside"); !errors.Is(err, ErrPathInvalid) {
		t.Fatalf("parent escape error = %v", err)
	}

	member := filepath.Join(root, "member")
	if err := os.Mkdir(member, 0o755); err != nil {
		t.Fatalf("mkdir member: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(member, ".repo")); err != nil {
		t.Fatalf("symlink state escape fixture: %v", err)
	}
	state := FileDevelopmentStateInspector{Paths: resolver}.Inspect(context.Background(), DevelopmentStateInput{
		MemberRoot: member, StateRoot: ".repo", ProjectID: "project_expected", RepositoryID: "repo_expected",
	})
	if state.Posture != DevelopmentStateInvalid || state.ReasonCode != "path_escape" {
		t.Fatalf("state escape posture = %#v", state)
	}
}
