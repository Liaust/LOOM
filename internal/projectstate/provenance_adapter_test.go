package projectstate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/projects"
)

func TestObserveProjectForProvenanceKeepsRootsExecutionOnlyAndBounded(t *testing.T) {
	now := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	model := projects.ProjectRepositoryReadModel{
		Project: projects.ProjectRepositoryReadProject{ProjectID: "project_01ARZ3NDEKTSV4RRFFQ69G5FAV", Slug: "atlas", Name: "Atlas", Status: "active"},
		Source: &projects.ProjectRepositoryReadSource{
			ProjectContractSchemaVersion: projects.ProjectRepositoryProjectSchemaV04, ReposContractSchemaVersion: projects.ProjectRepositoryReposSchemaV04,
			ProjectContractPath: "/registered/atlas/.loom/project.yaml", ReposContractPath: "/registered/atlas/.loom/contracts/repos.yaml",
			ProjectRoot: "/registered/atlas", OwnerNode: "main", SemanticDigest: testDigest("a"),
			LocationDigest: testDigest("b"), SourceRevision: 3,
		},
		Members: []projects.ProjectRepositoryReadMember{
			{RepositoryID: "repo_01ARZ3NDEKTSV4RRFFQ69G5FAV", RepositoryOwnerProjectID: "project_01ARZ3NDEKTSV4RRFFQ69G5FAV", Key: "primary", Path: "primary", Role: projects.ProjectRepositoryRolePrimary, SourceBindingDigest: testDigest("c"), ObservationRevision: 7},
			{RepositoryID: "repo_01ARZ3NDEKTSV4RRFFQ69G5FB0", RepositoryOwnerProjectID: "project_01ARZ3NDEKTSV4RRFFQ69G5FAV", Key: "reference", Path: "reference", Role: projects.ProjectRepositoryRoleReference, SourceBindingDigest: testDigest("d"), ObservationRevision: 8},
		},
	}
	paths := &staticPathResolver{byRelative: map[string]ResolvedPath{
		"repos/primary":   {Path: "/private/node/atlas/repos/primary", Exists: true, Directory: true},
		"repos/reference": {Path: "/private/node/atlas/repos/reference", Exists: true, Directory: true},
	}}
	service := Service{
		Reader: staticProjectReader{model: model}, Paths: paths,
		Git: &staticGitObserver{byRoot: map[string]GitProjection{
			"/private/node/atlas/repos/primary":   {RootMatchesMember: true},
			"/private/node/atlas/repos/reference": {RootMatchesMember: true},
		}},
		DevelopmentState: staticDevelopmentStateInspector{}, Clock: fixedClock{value: now}, LocalNode: "main",
	}
	source, err := service.ObserveProjectForProvenance(context.Background(), "atlas")
	if err != nil {
		t.Fatal(err)
	}
	if source.Projection.Source.SourceRevision != 3 || len(source.Repositories) != 2 {
		t.Fatalf("unexpected bounded provenance source: %#v", source)
	}
	if !source.Repositories[0].Owned || !source.Repositories[0].Available || source.Repositories[0].RepositoryRoot != "/private/node/atlas/repos/primary" || source.Repositories[0].SourceVersion != 7 {
		t.Fatalf("primary repository source = %#v", source.Repositories[0])
	}
	if source.Repositories[1].Owned || source.Repositories[1].RepositoryRoot != "" || source.Repositories[1].ReasonCode != "repository_not_owned_by_project" {
		t.Fatalf("reference repository source = %#v", source.Repositories[1])
	}
	payload, err := json.Marshal(source.Projection)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "/private/node") || strings.Contains(string(payload), "/registered/atlas") {
		t.Fatalf("portable projection leaked execution-only roots: %s", payload)
	}
}

// The bridge no longer rereads after observation; the regression matrix below
// proves that none of a later snapshot's source/member facts can leak into it.
func TestObserveProjectForProvenanceDoesNotMixChangedRegistrySnapshot(t *testing.T) {
	changes := map[string]func(*projects.ProjectRepositoryReadModel){
		"project name":           func(m *projects.ProjectRepositoryReadModel) { m.Project.Name = "Changed" },
		"project lifecycle":      func(m *projects.ProjectRepositoryReadModel) { m.Project.Status = "archived" },
		"source revision":        func(m *projects.ProjectRepositoryReadModel) { m.Source.SourceRevision++ },
		"registration":           func(m *projects.ProjectRepositoryReadModel) { m.Source.ProjectContractRegistrationID = "other" },
		"schema":                 func(m *projects.ProjectRepositoryReadModel) { m.Source.ProjectContractSchemaVersion = "unknown" },
		"source root":            func(m *projects.ProjectRepositoryReadModel) { m.Source.ProjectRoot = "/other" },
		"project attribution":    func(m *projects.ProjectRepositoryReadModel) { m.Source.ProjectContractPath = "/other/project.yaml" },
		"membership attribution": func(m *projects.ProjectRepositoryReadModel) { m.Source.ReposContractPath = "/other/repos.yaml" },
		"location digest":        func(m *projects.ProjectRepositoryReadModel) { m.Source.LocationDigest = testDigest("f") },
		"semantic digest":        func(m *projects.ProjectRepositoryReadModel) { m.Source.SemanticDigest = testDigest("f") },
		"owner node":             func(m *projects.ProjectRepositoryReadModel) { m.Source.OwnerNode = "remote" },
		"member path":            func(m *projects.ProjectRepositoryReadModel) { m.Members[0].Path = "other" },
		"member role": func(m *projects.ProjectRepositoryReadModel) {
			m.Members[0].Role = projects.ProjectRepositoryRoleReference
		},
		"member owner": func(m *projects.ProjectRepositoryReadModel) { m.Members[0].RepositoryOwnerProjectID = "other" },
		"member lifecycle": func(m *projects.ProjectRepositoryReadModel) {
			m.Members[0].MembershipLifecycle = projects.RepositoryLifecycleArchived
		},
		"repository lifecycle": func(m *projects.ProjectRepositoryReadModel) {
			m.Members[0].RepositoryLifecycle = projects.RepositoryLifecycleArchived
		},
		"member digest":   func(m *projects.ProjectRepositoryReadModel) { m.Members[0].SourceBindingDigest = testDigest("f") },
		"member revision": func(m *projects.ProjectRepositoryReadModel) { m.Members[0].ObservationRevision++ },
		"state root":      func(m *projects.ProjectRepositoryReadModel) { m.Members[0].StateRoot = ".other" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			member := localReadMember("repo_test", "api", "api", "")
			member.ObservationRevision = 7
			model := localReadModel("/registered/project", "project_test", []projects.ProjectRepositoryReadMember{member})
			model.Source.SourceRevision = 3
			reader := &driftingProjectionReader{model: model, change: change}
			paths := &staticPathResolver{byRelative: map[string]ResolvedPath{"repos/api": {Path: "/registered/project/repos/api", Exists: true, Directory: true}}}
			source, err := (Service{Reader: reader, LocalNode: "main", Paths: paths, Git: &staticGitObserver{}, DevelopmentState: staticDevelopmentStateInspector{}}).ObserveProjectForProvenance(context.Background(), "project_test")
			if err != nil || reader.calls != 1 || len(source.Repositories) != 1 {
				t.Fatalf("inconsistent source snapshot: reads=%d error=%v", reader.calls, err)
			}
			got := source.Repositories[0]
			if got.SourceVersion != 7 || source.Projection.Source.SourceRevision != 3 || got.RepositoryRoot != "/registered/project/repos/api" || got.NavigationPath != "repos/api" || got.MembershipSourcePath != ".loom/contracts/repos.yaml" || got.Projection.SourceBindingDigest != member.SourceBindingDigest || got.Projection.MembershipLifecycle != member.MembershipLifecycle {
				t.Fatalf("mixed snapshots: %#v", source)
			}
		})
	}
}

type driftingProjectionReader struct {
	model  projects.ProjectRepositoryReadModel
	change func(*projects.ProjectRepositoryReadModel)
	calls  int
}

func TestObserveProjectContextForProvenanceNeverSamplesRepositoriesOrGit(t *testing.T) {
	input := projectDevelopmentFixture(t)
	if err := os.Mkdir(filepath.Join(input.ProjectRoot, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	gitPath := filepath.Join(bin, "git")
	if err := os.WriteFile(gitPath, []byte("#!/bin/sh\nprintf called > \"$0.called\"\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	member := localReadMember("repo_context", "component", "component", ".repo")
	member.ObservationRevision = 7
	model := localReadModel(input.ProjectRoot, input.ProjectID, []projects.ProjectRepositoryReadMember{member})
	model.Source.SourceRevision = 3
	reader := &driftingProjectionReader{model: model, change: func(m *projects.ProjectRepositoryReadModel) {
		m.Members[0].SourceBindingDigest = "changed"
	}}
	paths, git, development := &staticPathResolver{}, &staticGitObserver{}, &recordingDevelopmentStateInspector{}
	service := Service{Reader: reader, LocalNode: "main", Paths: paths, Git: git, DevelopmentState: development}
	var observer ProvenanceProjectContextObserver = service
	got, err := observer.ObserveProjectContextForProvenance(t.Context(), input.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if reader.calls != 1 || len(paths.calls) != 0 || len(git.calls) != 0 || len(development.calls) != 0 {
		t.Fatalf("context sampled members: registry=%d paths=%v git=%v repo=%v", reader.calls, paths.calls, git.calls, development.calls)
	}
	if _, err := os.Stat(gitPath + ".called"); !os.IsNotExist(err) {
		t.Fatal("context reader invoked project Git")
	}
	if got.Projection.Development.Posture != ProjectDevelopmentReady || got.Projection.Development.Git != nil || len(got.Repositories) != 1 {
		t.Fatalf("context: %+v", got)
	}
	repository := got.Repositories[0]
	if !repository.Owned || repository.Available || repository.RepositoryRoot != "" || repository.SourceVersion != 7 || repository.ReasonCode != "repository_not_sampled" || repository.Projection.Git != nil {
		t.Fatalf("invented repository observation: %+v", repository)
	}
	if repository.Projection.SourceBindingDigest != member.SourceBindingDigest || repository.Projection.MembershipLifecycle != member.MembershipLifecycle || repository.Projection.RepositoryLifecycle != member.RepositoryLifecycle || repository.Projection.RepositoryID != member.RepositoryID || repository.Projection.Role != member.Role || repository.Projection.RepositoryOwnerProjectID != input.ProjectID || repository.Projection.RelativeSource != member.Path || repository.Projection.StateRoot != member.StateRoot {
		t.Fatalf("lost authoritative membership: %+v", repository.Projection)
	}
	if !reflect.DeepEqual(got.Projection.Members[0], repository.Projection) {
		t.Fatal("bridge changed membership snapshot")
	}
}

func TestObserveProjectContextForProvenanceQualifiesArchivedAndRemote(t *testing.T) {
	for _, scenario := range []string{"archived", "remote", "unregistered"} {
		t.Run(scenario, func(t *testing.T) {
			input := projectDevelopmentFixture(t)
			model := localReadModel(input.ProjectRoot, input.ProjectID, nil)
			want := ProjectDevelopmentArchived
			switch scenario {
			case "archived":
				model.Project.Status = "archived"
			case "remote":
				model.Source.OwnerNode = "remote"
				want = ProjectDevelopmentUnavailable
			case "unregistered":
				model.Source = nil
				want = ProjectDevelopmentNotRegistered
			}
			got, err := (Service{Reader: staticProjectReader{model: model}, LocalNode: "main"}).ObserveProjectContextForProvenance(t.Context(), input.ProjectID)
			if err != nil || got.Projection.Development.Posture != want || len(got.Projection.Development.Documents) != 0 {
				t.Fatalf("unavailable project metadata was sampled: %+v %v", got, err)
			}
		})
	}
}

func (r *driftingProjectionReader) ReadProjectRepositoryState(context.Context, string) (projects.ProjectRepositoryReadModel, error) {
	r.calls++
	model := r.model
	source := *model.Source
	model.Source = &source
	model.Members = append([]projects.ProjectRepositoryReadMember(nil), model.Members...)
	if r.calls > 1 {
		r.change(&model)
	}
	return model, nil
}
