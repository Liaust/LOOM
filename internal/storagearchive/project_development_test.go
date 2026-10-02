package storagearchive

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"loom.local/loom/internal/projectstate"
)

func TestArchivedProjectDevelopmentUsesVerifiedCustody(t *testing.T) {
	env := newProjectArchivePlanEnvironment(t, projectArchiveAdapterFixture{Key: "01000000000000000000000000", OwnerNode: "main", RootKind: "canonical"})
	id := env.projects.detail.Project.Project.ProjectID
	if err := os.MkdirAll(filepath.Join(env.canonicalRoot, ".project"), 0750); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		".loom/project.yaml":   "kind: loom.project\nschema_version: project.contract.v0.5\nproject:\n  id: " + id + "\n  owner_node: main\n",
		".project/OVERVIEW.md": "# Archived research\n\n## Purpose\nRetain the measured experiment.\n",
	} {
		if err := os.WriteFile(filepath.Join(env.canonicalRoot, name), []byte(body), 0640); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := env.service.PlanProjectPhysicalArchive(t.Context(), projectArchivePlanRequest(), id, ProjectPhysicalArchivePlanInput{OperationID: projectArchiveAdapterOperationID, Reason: "archive context fixture", PlannedAt: time.Date(2026, 9, 4, 10, 30, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.service.ApplyProjectPhysicalArchive(t.Context(), plan, plan.PlanDigest); err != nil {
		t.Fatal(err)
	}
	// The standard workspace adapter has no inspect method; use its real kernel.
	env.service.WorkspaceMove = env.workspace.planner
	model, err := env.repositories.ReadProjectRepositoryState(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	input := projectstate.ProjectDevelopmentInput{ProjectID: id, ProjectRoot: env.registeredRoot, Lifecycle: "archived", OwnerNode: "main", LocalNode: "main", SourceRevision: model.Source.SourceRevision}
	inspector := ProjectDevelopmentInspector{Archive: env.service}
	got := inspector.Inspect(t.Context(), input)
	if got.Posture != projectstate.ProjectDevelopmentArchived || got.Purpose != "Retain the measured experiment." || got.CapturedBytes == 0 {
		t.Fatalf("archived context missing: %+v", got)
	}
	if _, err := os.Stat(env.canonicalRoot); !os.IsNotExist(err) {
		t.Fatal("inspection recreated active source")
	}
	wrong := input
	wrong.SourceRevision++
	if got := inspector.Inspect(t.Context(), wrong); got.CapturedBytes != 0 {
		t.Fatal("stale registration was accepted")
	}
	if err := os.WriteFile(filepath.Join(plan.Workspace.Destination.Path.AbsolutePath, ".project/OVERVIEW.md"), []byte("tampered"), 0640); err != nil {
		t.Fatal(err)
	}
	if got := inspector.Inspect(t.Context(), input); got.CapturedBytes != 0 || len(got.Documents) != 0 {
		t.Fatal("archive drift was exposed as verified context")
	}
}
