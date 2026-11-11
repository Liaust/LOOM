package storagearchive

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectstate"
)

// The older archive fake intentionally strips private execution fields. This
// wrapper models the real registry reader's validated relative source binding.
type repositoryContextFixtureReader struct {
	base ProjectRuntimeRepositoryStateService
}

func (r repositoryContextFixtureReader) ReadProjectRepositoryState(ctx context.Context, ref string) (projects.ProjectRepositoryReadModel, error) {
	model, err := r.base.ReadProjectRepositoryState(ctx, ref)
	if err == nil && model.Source != nil {
		model.Source.ProjectContractPath = filepath.Join(model.Source.ProjectRoot, ".loom/project.json")
		model.Source.ReposContractPath = filepath.Join(model.Source.ProjectRoot, "repos/loom.repos.yaml")
	}
	return model, err
}

func TestArchivedRepositoryContextUsesPlanMembershipAndRetainedCustody(t *testing.T) {
	env := newProjectArchivePlanEnvironment(t, projectArchiveAdapterFixture{Key: "01000000000000000000000000", OwnerNode: "main", RootKind: "canonical", RepositoryCount: 2})
	id := env.projects.detail.Project.Project.ProjectID
	for index := range env.repositories.state.Members {
		env.repositories.state.Members[index].RepositoryID = fmt.Sprintf("repo_01ARZ3NDEKTSV4RRFFQ69G5FA%d", index)
		env.repositories.state.Members[index].ObservationRevision = 1
		env.repositories.state.Members[index].StateRoot = ".repo"
	}
	reader := repositoryContextFixtureReader{base: env.repositories}
	env.service.RepositoryState = reader
	for name, body := range map[string]string{
		"repos/loom.repos.yaml":        "retained membership fixture\n",
		"repos/repo-a/.repo/repo.yaml": "repository:\n  id: " + env.repositories.state.Members[0].RepositoryID + "\nowner_project:\n  id: " + id + "\n",
	} {
		path := filepath.Join(env.canonicalRoot, name)
		if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0640); err != nil {
			t.Fatal(err)
		}
	}
	req := projectArchivePlanRequest()
	req.ScopeID = env.projects.detail.Project.Project.ProjectScopeID
	plan, err := env.service.PlanProjectPhysicalArchive(t.Context(), req, id, ProjectPhysicalArchivePlanInput{OperationID: projectArchiveAdapterOperationID, Reason: "retained repository context"})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := projectPlanEvidence(plan)
	if err != nil {
		t.Fatal(err)
	}
	store := &projectSurfaceEvidenceStore{rows: map[string]projects.ProjectPlanEvidence{evidence.OperationID: evidence}}
	env.service.PlanEvidence = store
	if _, err := env.service.ApplyProjectPhysicalArchive(t.Context(), plan, plan.PlanDigest); err != nil {
		t.Fatal(err)
	}
	env.service.WorkspaceMove = env.workspace.planner
	inspector := ProjectDevelopmentInspector{Archive: env.service}
	service := projectstate.Service{Reader: reader, LocalNode: "main", ProjectDevelopment: inspector}
	got, err := service.ObserveProjectForProvenance(t.Context(), id)
	if err != nil || len(got.Repositories) != 2 {
		t.Fatalf("archive resolution: %+v %v", got, err)
	}
	present, missing := got.Repositories[0], got.Repositories[1]
	wantRoot, err := filepath.EvalSymlinks(filepath.Join(plan.Workspace.Destination.Path.AbsolutePath, "repos/repo-a"))
	if err != nil {
		t.Fatal(err)
	}
	if !present.Available || present.RepositoryRoot != wantRoot || present.Projection.Archive == nil || present.Projection.Archive.OperationID != plan.Workspace.OperationID || present.Projection.DevelopmentState.Posture != projectstate.DevelopmentStateEnabled || present.Projection.Git != nil || present.SourceVersion != 1 || present.MembershipSourcePath != "repos/loom.repos.yaml" {
		t.Fatalf("retained source identity/posture: %+v", present)
	}
	if missing.Available || missing.ReasonCode != "member_path_missing" {
		t.Fatalf("missing retained member: %+v", missing)
	}
	// Only verified project development publishes the additive read-only root;
	// repository projections still omit execution locations.
	if got.Projection.Development.Archive == nil || got.Projection.Development.Archive.ReadOnlyRoot != plan.Workspace.Destination.Path.AbsolutePath || present.Projection.Archive.ReadOnlyRoot != "" {
		t.Fatal("archive navigation escaped its development context")
	}
	copy := *got.Projection.Development.Archive
	copy.ReadOnlyRoot = ""
	got.Projection.Development.Archive = &copy
	raw, err := json.Marshal(got)
	if err != nil || strings.Contains(string(raw), env.roots.BoxRoot) || strings.Contains(string(raw), env.roots.StorageRoot) {
		t.Fatalf("private location leaked: %s %v", raw, err)
	}
	if _, err := os.Stat(env.canonicalRoot); !os.IsNotExist(err) {
		t.Fatal("read recreated active project")
	}
	// The location validator remains tied to the original membership snapshot.
	env.repositories.state.Members[0].Path = "replacement"
	if present.ValidateRead(t.Context()) == nil {
		t.Fatal("changed membership remained authorized")
	}
	got, err = service.ObserveProjectForProvenance(t.Context(), id)
	if err != nil || got.Repositories[0].Available || got.Repositories[0].ReasonCode != "archive_source_unverified" {
		t.Fatalf("changed membership read: %+v %v", got, err)
	}
}

func TestArchivedProjectDevelopmentUsesVerifiedCustody(t *testing.T) {
	env := newProjectArchivePlanEnvironment(t, projectArchiveAdapterFixture{Key: "01000000000000000000000000", OwnerNode: "main", RootKind: "canonical"})
	id := env.projects.detail.Project.Project.ProjectID
	if err := os.MkdirAll(filepath.Join(env.canonicalRoot, ".project"), 0750); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		".loom/project.yaml":   "kind: loom.project\nschema_version: project.contract.v0.5\nproject:\n  id: " + id + "\n  owner_node: main\n",
		".project/OVERVIEW.md": "# Archived research\n\n## Purpose\nRetain the measured experiment.\n",
		".project/STATE.md":    "# State\n\n## Current State\nRetained experiment.\n",
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
	if got.Posture != projectstate.ProjectDevelopmentArchived || got.Purpose != "Retain the measured experiment." || got.CapturedBytes == 0 || got.Archive == nil || got.Archive.OperationID != plan.Workspace.OperationID || got.Archive.ManifestDigest == "" || got.Archive.ReadOnlyRoot != plan.Workspace.Destination.Path.AbsolutePath {
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
	if got := inspector.Inspect(t.Context(), input); got.CapturedBytes != 0 || len(got.Documents) != 0 || got.Archive != nil {
		t.Fatal("archive drift was exposed as verified context")
	}
}
