package projects

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestProjectArchiveLockKeysAreSortedUniqueAndCrossBound(t *testing.T) {
	got := ProjectArchiveLockKeys(
		"project_test",
		"project-test",
		"workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		[]string{"repo_b", "repo_a", "repo_b"},
		[]string{"watched_roots", "scripts", "scripts"},
	)
	want := []string{
		"loom:project-archive:archive:workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		"loom:project-archive:runtime:project_test:scripts",
		"loom:project-archive:runtime:project_test:watched_roots",
		"loom:project-repository:project:id:project_test",
		"loom:project-repository:project:slug:project-test",
		"loom:project-repository:repository:id:repo_a",
		"loom:project-repository:repository:id:repo_b",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lock keys = %#v, want %#v", got, want)
	}
}

func TestProjectArchiveStateRejectsMissingOrOutOfOrderEvidence(t *testing.T) {
	started := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	state := ProjectPhysicalArchiveState{
		SchemaVersion:   ProjectPhysicalArchiveStateSchemaVersion,
		Status:          ProjectPhysicalArchiveStatusInProgress,
		Phase:           ProjectArchivePhaseDeactivationPending,
		MutationBlocked: true,
		ProjectID:       "project_test", ProjectSlug: "project-test",
		OperationID: "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		PlanDigest:  "sha256:" + strings.Repeat("a", 64), WorkspacePlanDigest: "sha256:" + strings.Repeat("b", 64),
		ActivePath: "/fixture/box/Projects/project-test", ArchivePath: "/fixture/storage/archive/projects/project-test/project",
		ActorID: "actor_test", Reason: "archive project", StartedAt: started,
	}
	if err := ValidateProjectPhysicalArchiveState(state); err != nil {
		t.Fatal(err)
	}
	state.Phase = ProjectArchivePhaseRuntimeDeactivated
	if err := ValidateProjectPhysicalArchiveState(state); err == nil {
		t.Fatal("runtime phase accepted without timestamp")
	}
	state.RuntimeDeactivatedAt = &started
	state.RuntimeQuiescenceDigest = "sha256:" + strings.Repeat("c", 64)
	if err := ValidateProjectPhysicalArchiveState(state); err != nil {
		t.Fatal(err)
	}
	state.Phase = ProjectArchivePhaseComplete
	state.Status = ProjectPhysicalArchiveStatusArchived
	if err := ValidateProjectPhysicalArchiveState(state); err == nil {
		t.Fatal("complete state accepted without workspace and manifest evidence")
	}
}

func TestProjectArchiveStateTransitionPreservesImmutableAndAdjacentEvidence(t *testing.T) {
	started := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	state := ProjectPhysicalArchiveState{
		SchemaVersion: ProjectPhysicalArchiveStateSchemaVersion, Status: ProjectPhysicalArchiveStatusInProgress,
		Phase: ProjectArchivePhaseDeactivationPending, MutationBlocked: true, ProjectID: "project_test", ProjectSlug: "project-test",
		OperationID: "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAV", PlanDigest: "sha256:" + strings.Repeat("a", 64),
		WorkspacePlanDigest: "sha256:" + strings.Repeat("b", 64), ActivePath: "/fixture/active", ArchivePath: "/fixture/archive",
		ActorID: "actor_test", Reason: "archive project", StartedAt: started,
	}
	t.Run("immutable", func(t *testing.T) {
		next := state
		next.ActorID = "actor_substituted"
		if err := validateProjectPhysicalArchiveStateTransition(state, next); err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("immutable transition error = %v", err)
		}
	})
	t.Run("skip", func(t *testing.T) {
		next := state
		runtimeAt := started.Add(time.Second)
		workspaceAt := runtimeAt.Add(time.Second)
		next.Phase = ProjectArchivePhaseProjectStatePending
		next.RuntimeDeactivatedAt = &runtimeAt
		next.RuntimeQuiescenceDigest = "sha256:" + strings.Repeat("c", 64)
		next.WorkspaceCompletedAt = &workspaceAt
		next.ArchiveManifestDigest = "sha256:" + strings.Repeat("d", 64)
		if err := validateProjectPhysicalArchiveStateTransition(state, next); err == nil || !strings.Contains(err.Error(), "adjacent") {
			t.Fatalf("phase skip error = %v", err)
		}
	})
	t.Run("same phase evidence", func(t *testing.T) {
		next := state
		next.StartedAt = next.StartedAt.Add(time.Nanosecond)
		if err := validateProjectPhysicalArchiveStateTransition(state, next); err == nil {
			t.Fatal("same-phase evidence rewrite was accepted")
		}
	})
}
