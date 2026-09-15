package projectstate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"loom.local/loom/internal/projects"
)

func projectDevelopmentFixture(t *testing.T) ProjectDevelopmentInput {
	t.Helper()
	input := ProjectDevelopmentInput{ProjectRoot: t.TempDir(), ProjectID: "project_01ARZ3NDEKTSV4RRFFQ69G5FAV", OwnerNode: "main", LocalNode: "main", Lifecycle: "draft", SourceRevision: 3}
	writeProjectDevelopmentFile(t, input, projectIdentityPath, "kind: loom.project\nschema_version: project.contract.v0.5\nproject:\n  id: "+input.ProjectID+"\n  name: Research\n  slug: research\n  owner_node: main\nresources: {}\n")
	writeProjectDevelopmentFile(t, input, ".project/OVERVIEW.md", "# Research\n\n## Purpose\nUnderstand the source material.\n")
	return input
}

func writeProjectDevelopmentFile(t *testing.T, input ProjectDevelopmentInput, name, body string) {
	t.Helper()
	file := filepath.Join(input.ProjectRoot, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func projectDevelopmentDocument(t *testing.T, state ProjectDevelopmentState, name string) ProjectDevelopmentDocument {
	t.Helper()
	for _, doc := range state.Documents {
		if doc.Path == name {
			return doc
		}
	}
	t.Fatalf("missing source document %q", name)
	return ProjectDevelopmentDocument{}
}

func TestProjectDevelopmentFileOnlyCapturedContext(t *testing.T) {
	input := projectDevelopmentFixture(t)
	stateText := "# State\n\n## Active Focus\nReview evidence.\n\n## Current State\nSources collected.\n\n## Blockers\nAwaiting a scan.\n\n## Next Action\nCompare the two sources.\n"
	writeProjectDevelopmentFile(t, input, ".project/STATE.md", stateText)
	writeProjectDevelopmentFile(t, input, ".project/ROADMAP.md", "# Roadmap\n\n1. Collect\n2. Review\n")
	writeProjectDevelopmentFile(t, input, ".project/MAP.md", "# Map\n\n## Structure\n- `notes/`: evidence\n")
	writeProjectDevelopmentFile(t, input, ".project/features/review/feature.yaml", "slug: review\ntitle: Review evidence\nstatus: in_progress\nnext_action: Compare sources\nblocked_reason: null\n")
	writeProjectDevelopmentFile(t, input, ".project/decisions/ADR-001.md", "# Keep original evidence\n\n- Decision ID: `ADR-001`\n- Status: `proposed`\n- Date: `2026-09-13`\n\n## Decision\nPreserve the source.\n")
	writeProjectDevelopmentFile(t, input, ".project/decisions/ADR-002.md", "# A file is not approval\n\n## Decision\nThe text says accepted, but there is no declared status.\n")
	writeProjectDevelopmentFile(t, input, ".project/decisions/ADR-003.md", "# Example\n\n```markdown\n- Status: accepted\n```\n\n## Decision\nNo declared status.\n")
	writeProjectDevelopmentFile(t, input, ".project/features/review/internal/feature.yaml", "must not read: [")
	writeProjectDevelopmentFile(t, input, "repos/component/.project/STATE.md", "# State\n## Active Focus\nWrong focus\n")
	writeProjectDevelopmentFile(t, input, "notes/payload.md", "not context")
	before, err := os.ReadFile(filepath.Join(input.ProjectRoot, ".project/STATE.md"))
	if err != nil {
		t.Fatal(err)
	}
	got := ReadProjectDevelopment(t.Context(), input)
	if got.Posture != ProjectDevelopmentReady || !got.Complete || got.OmittedFiles != 0 || got.Git != nil || got.SourceRevision != 3 {
		t.Fatalf("file-only posture: %+v", got)
	}
	if got.Purpose != "Understand the source material." || got.CurrentFocus != "Review evidence." || got.Progress != "Sources collected." || got.Blockers != "Awaiting a scan." || got.NextAction != "Compare the two sources." || !strings.Contains(got.Structure, "notes/") || !strings.Contains(got.Roadmap, "2. Review") {
		t.Fatalf("summary: %+v", got)
	}
	if len(got.Features) != 1 || got.Features[0].Status != "in_progress" || got.Features[0].BlockedReason != "" || len(got.Decisions) != 3 || got.Decisions[0].Status != "proposed" || got.Decisions[1].Status != "" || got.Decisions[2].Status != "" {
		t.Fatalf("source declarations: %+v %+v", got.Features, got.Decisions)
	}
	doc := projectDevelopmentDocument(t, got, ".project/STATE.md")
	if doc.Hash != projectDevelopmentHash(before) || doc.Excerpt != string(before) || doc.Truncated || doc.SizeBytes != int64(len(before)) {
		t.Fatalf("captured document: %+v", doc)
	}
	again := ReadProjectDevelopment(t.Context(), input)
	if !reflect.DeepEqual(got, again) {
		t.Fatal("unchanged source is not deterministic")
	}
	input.SourceRevision++
	if next := ReadProjectDevelopment(t.Context(), input); next.SourceDigest != got.SourceDigest {
		t.Fatal("registry revision changed content identity")
	}
	writeProjectDevelopmentFile(t, input, ".project/STATE.md", strings.ReplaceAll(stateText, "Review evidence.", "Publish findings."))
	next := ReadProjectDevelopment(t.Context(), input)
	if next.SourceDigest == got.SourceDigest || next.CurrentFocus == got.CurrentFocus || doc.Excerpt != stateText {
		t.Fatal("mutable source replaced captured observation")
	}
	writeProjectDevelopmentFile(t, input, ".project/STATE.md", stateText)
	if reverted := ReadProjectDevelopment(t.Context(), input); reverted.SourceDigest != got.SourceDigest {
		t.Fatal("reverted source did not recover content identity")
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), input.ProjectRoot) || strings.Contains(string(encoded), "Wrong focus") || strings.Contains(string(encoded), "must not read") {
		t.Fatal("unselected content or execution root leaked")
	}
}

func TestProjectDevelopmentCompatibilityAndMetadata(t *testing.T) {
	input := projectDevelopmentFixture(t)
	if err := os.Remove(filepath.Join(input.ProjectRoot, ".project/OVERVIEW.md")); err != nil {
		t.Fatal(err)
	}
	writeProjectDevelopmentFile(t, input, ".project/PROJECT.md", "# Existing Project\n\nExisting custom purpose.\n\n## Scope\nA mature source.\n")
	writeProjectDevelopmentFile(t, input, ".project/STATE.md", "---\ncurrent_focus: Existing focus\nprogress: Underway\nblockers: None declared\nnext_action: Review\n---\n# State\n")
	writeProjectDevelopmentFile(t, input, ".project/decisions/source.md", "---\nid: ADR-custom\ntitle: Original title\nstatus: deferred\ndate: 2026-09-13\n---\n# Heading\n")
	got := ReadProjectDevelopment(t.Context(), input)
	if got.Posture != ProjectDevelopmentReady || got.Purpose != "Existing custom purpose." || got.CurrentFocus != "Existing focus" || len(got.Decisions) != 1 || got.Decisions[0].Status != "deferred" {
		t.Fatalf("compatibility: %+v", got)
	}
	writeProjectDevelopmentFile(t, input, ".project/OVERVIEW.md", "---\nmalformed: [\n---\n# Overview\n")
	got = ReadProjectDevelopment(t.Context(), input)
	if got.Posture != ProjectDevelopmentInvalid || got.Purpose != "" {
		t.Fatal("malformed OVERVIEW silently fell back to PROJECT")
	}
}

func TestProjectDevelopmentSourcePostures(t *testing.T) {
	for _, scenario := range []string{"missing_context", "missing_root", "missing_identity", "malformed_identity", "wrong_id", "wrong_owner", "remote", "archived", "source_archived", "malformed_feature", "malformed_decision", "duplicate_yaml", "alias_yaml", "binary", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			input := projectDevelopmentFixture(t)
			ctx := t.Context()
			want := ProjectDevelopmentInvalid
			switch scenario {
			case "missing_context":
				if err := os.RemoveAll(filepath.Join(input.ProjectRoot, ".project")); err != nil {
					t.Fatal(err)
				}
				want = ProjectDevelopmentMissing
			case "missing_root":
				input.ProjectRoot += "/absent"
				want = ProjectDevelopmentUnavailable
			case "missing_identity":
				if err := os.Remove(filepath.Join(input.ProjectRoot, projectIdentityPath)); err != nil {
					t.Fatal(err)
				}
				want = ProjectDevelopmentMissing
			case "malformed_identity":
				writeProjectDevelopmentFile(t, input, projectIdentityPath, "project: [")
			case "wrong_id":
				input.ProjectID = "project_other"
				want = ProjectDevelopmentMismatch
			case "wrong_owner":
				input.OwnerNode, input.LocalNode = "other", "other"
				want = ProjectDevelopmentMismatch
			case "remote":
				input.LocalNode = "remote"
				want = ProjectDevelopmentUnavailable
			case "archived":
				input.Lifecycle = "archived"
				want = ProjectDevelopmentArchived
			case "source_archived":
				payload, err := os.ReadFile(filepath.Join(input.ProjectRoot, projectIdentityPath))
				if err != nil {
					t.Fatal(err)
				}
				writeProjectDevelopmentFile(t, input, projectIdentityPath, strings.Replace(string(payload), "  owner_node: main", "  owner_node: main\n  status: archived", 1))
				want = ProjectDevelopmentArchived
			case "malformed_feature":
				writeProjectDevelopmentFile(t, input, ".project/features/test/feature.yaml", "status: [ready]")
			case "malformed_decision":
				writeProjectDevelopmentFile(t, input, ".project/decisions/test.md", "---\nstatus: [accepted]\n---\n# Decision")
			case "duplicate_yaml":
				writeProjectDevelopmentFile(t, input, ".project/features/test/feature.yaml", "status: ready\nstatus: complete\n")
			case "alias_yaml":
				writeProjectDevelopmentFile(t, input, ".project/features/test/feature.yaml", "status: &x ready\ntitle: *x\n")
			case "binary":
				writeProjectDevelopmentFile(t, input, ".project/STATE.md", "bad\x00text")
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = ProjectDevelopmentUnavailable
			}
			got := ReadProjectDevelopment(ctx, input)
			if got.Posture != want || got.Complete || got.ReasonCode == "" {
				t.Fatalf("%s: %+v", scenario, got)
			}
			if (scenario == "remote" || scenario == "archived" || scenario == "cancelled") && len(got.Documents) != 0 {
				t.Fatal("unavailable/archived source was read")
			}
		})
	}
}

func TestProjectDevelopmentRejectsMetadataPathEscapesAndSpecialFiles(t *testing.T) {
	for _, name := range []string{".project", ".project/STATE.md", ".project/features", ".project/features/escape", ".project/decisions/escape.md", ".loom", "fifo"} {
		t.Run(name, func(t *testing.T) {
			input := projectDevelopmentFixture(t)
			outside := t.TempDir()
			if err := os.WriteFile(filepath.Join(outside, "feature.yaml"), []byte("title: DO NOT CAPTURE"), 0600); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(input.ProjectRoot, name)
			if name == "fifo" {
				target = filepath.Join(input.ProjectRoot, ".project/STATE.md")
			}
			if err := os.RemoveAll(target); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				t.Fatal(err)
			}
			if name == "fifo" {
				if err := syscall.Mkfifo(target, 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Symlink(outside, target); err != nil {
				t.Fatal(err)
			}
			got := ReadProjectDevelopment(t.Context(), input)
			if got.Posture != ProjectDevelopmentInvalid || got.Complete {
				t.Fatalf("unsafe source: %+v", got)
			}
			for _, doc := range got.Documents {
				if strings.Contains(doc.Excerpt, "DO NOT CAPTURE") {
					t.Fatal("path escape captured outside bytes")
				}
			}
		})
	}
}

func TestProjectDevelopmentLimits(t *testing.T) {
	t.Run("file_and_excerpt", func(t *testing.T) {
		input := projectDevelopmentFixture(t)
		writeProjectDevelopmentFile(t, input, ".project/STATE.md", strings.Repeat("a", ProjectDevelopmentFileLimit))
		got := ReadProjectDevelopment(t.Context(), input)
		doc := projectDevelopmentDocument(t, got, ".project/STATE.md")
		if got.Posture != ProjectDevelopmentReady || !doc.Truncated || len(doc.Excerpt) != ProjectDevelopmentExcerptLimit || doc.Hash == "" {
			t.Fatalf("bounded excerpt: %+v", doc)
		}
		writeProjectDevelopmentFile(t, input, ".project/STATE.md", strings.Repeat("a", ProjectDevelopmentFileLimit+1))
		got = ReadProjectDevelopment(t.Context(), input)
		doc = projectDevelopmentDocument(t, got, ".project/STATE.md")
		if got.Posture != ProjectDevelopmentPartial || got.Complete || got.OmittedFiles != 1 || doc.Posture != "omitted" || doc.Hash != "" || doc.Excerpt != "" {
			t.Fatalf("oversize: %+v", got)
		}
	})
	t.Run("files", func(t *testing.T) {
		input := projectDevelopmentFixture(t)
		for i := 0; i < 130; i++ {
			writeProjectDevelopmentFile(t, input, fmt.Sprintf(".project/decisions/%03d.md", i), "# Decision\n")
		}
		got := ReadProjectDevelopment(t.Context(), input)
		if len(got.Documents) != ProjectDevelopmentFilesLimit || got.OmittedFiles != 9 || got.Posture != ProjectDevelopmentPartial || got.Complete {
			t.Fatalf("file-count limit: files=%d omissions=%d posture=%s", len(got.Documents), got.OmittedFiles, got.Posture)
		}
		again := ReadProjectDevelopment(t.Context(), input)
		if !reflect.DeepEqual(got, again) {
			t.Fatal("bounded selection is nondeterministic")
		}
	})
	t.Run("total_bytes", func(t *testing.T) {
		input := projectDevelopmentFixture(t)
		for i := 0; i < 33; i++ {
			writeProjectDevelopmentFile(t, input, fmt.Sprintf(".project/features/%03d/feature.yaml", i), "title: "+strings.Repeat("a", ProjectDevelopmentFileLimit-len("title: ")))
		}
		got := ReadProjectDevelopment(t.Context(), input)
		if got.CapturedBytes > ProjectDevelopmentBytesLimit || got.OmittedFiles != 2 || len(got.Features) != 31 || got.Posture != ProjectDevelopmentPartial {
			t.Fatalf("byte limit: bytes=%d omissions=%d features=%d posture=%s", got.CapturedBytes, got.OmittedFiles, len(got.Features), got.Posture)
		}
	})
}

func TestProjectDevelopmentGitMatchesCapturedSourcesOnly(t *testing.T) {
	input := projectDevelopmentFixture(t)
	writeProjectDevelopmentFile(t, input, ".project/STATE.md", "# State\n## Current Focus\nRead sources.\n")
	initializeCommittedRepository(t, input.ProjectRoot)
	mustRunGit(t, input.ProjectRoot, "add", ".loom", ".project")
	mustRunGit(t, input.ProjectRoot, "commit", "-m", "project context")
	if got := ReadProjectDevelopment(t.Context(), input); got.Git != nil {
		t.Fatal("Git was not opt-in")
	}
	input.ObserveGit = true
	got := ReadProjectDevelopment(t.Context(), input)
	if got.Git == nil || got.Git.Posture != "committed" || got.Git.Commit == "" {
		t.Fatalf("captured commit: %+v", got.Git)
	}
	writeProjectDevelopmentFile(t, input, "tracked.txt", "unrelated working change")
	if next := ReadProjectDevelopment(t.Context(), input); !reflect.DeepEqual(next.Git, got.Git) || next.SourceDigest != got.SourceDigest {
		t.Fatal("unrelated repository work changed context evidence")
	}
	writeProjectDevelopmentFile(t, input, ".project/OVERVIEW.md", "# Research\n## Purpose\nUncommitted purpose.\n")
	got = ReadProjectDevelopment(t.Context(), input)
	if got.Git == nil || got.Git.Posture != "uncommitted" || got.Git.Commit != "" || got.Git.Head == "" {
		t.Fatalf("uncommitted content mislabeled: %+v", got.Git)
	}
	writeProjectDevelopmentFile(t, input, ".project/OVERVIEW.md", "# Research\n\n## Purpose\nUnderstand the source material.\n")
	if err := os.Remove(filepath.Join(input.ProjectRoot, ".project/STATE.md")); err != nil {
		t.Fatal(err)
	}
	got = ReadProjectDevelopment(t.Context(), input)
	if got.Git == nil || got.Git.Posture != "uncommitted" || got.Git.Commit != "" {
		t.Fatalf("deleted selected source mislabeled: %+v", got.Git)
	}
}

func TestProjectDevelopmentZeroRepositoryServiceProjection(t *testing.T) {
	input := projectDevelopmentFixture(t)
	model := localReadModel(input.ProjectRoot, input.ProjectID, nil)
	model.Source.SourceRevision = input.SourceRevision
	model.Source.ProjectContractSchemaVersion = projects.ProjectRepositoryProjectSchemaV05
	model.Source.ReposContractSchemaVersion = projects.ProjectRepositoryProjectSchemaV05
	model.Source.ReposContractPath = model.Source.ProjectContractPath
	service := Service{Reader: staticProjectReader{model: model}, LocalNode: "main"}
	got, err := service.ObserveProject(t.Context(), input.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Development.Posture != ProjectDevelopmentReady || len(got.Members) != 0 || got.Development.SourceRevision != input.SourceRevision {
		t.Fatalf("zero-repo projection: %+v", got)
	}
	source, err := service.ObserveProjectContextForProvenance(t.Context(), input.ProjectID)
	if err != nil || !reflect.DeepEqual(source.Projection.Development, got.Development) {
		t.Fatalf("project bridge: %+v %v", source, err)
	}
}

func TestProjectDevelopmentCanonicalRepositoryOwnerBacklink(t *testing.T) {
	root := t.TempDir()
	writeDevelopmentStateFixture(t, root, "repository:\n  id: repo_expected\nowner_project:\n  id: project_expected\n  slug: expected\n")
	input := DevelopmentStateInput{MemberRoot: root, StateRoot: ".repo", ProjectID: "project_expected", RepositoryID: "repo_expected"}
	got := (FileDevelopmentStateInspector{}).Inspect(t.Context(), input)
	if got.Posture != DevelopmentStateEnabled {
		t.Fatalf("canonical backlink: %+v", got)
	}
	input.ProjectID = "project_other"
	if got := (FileDevelopmentStateInspector{}).Inspect(t.Context(), input); got.Posture != DevelopmentStateMismatch {
		t.Fatalf("canonical mismatch: %+v", got)
	}
}
