package projectstate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/projects"
)

func TestServiceBuildsDeterministicBoundedProjectionWithoutInferringWorktrees(t *testing.T) {
	observedAt := time.Date(2026, 8, 29, 20, 30, 0, 0, time.UTC)
	reader := staticProjectReader{model: projects.ProjectRepositoryReadModel{
		Project: projects.ProjectRepositoryReadProject{ProjectID: "project_test", ProjectScopeID: "scope_test", ProjectScopeKey: "project:test", Slug: "test", Name: "Test", Status: "active", UpdatedAt: observedAt.Add(-time.Hour)},
		Facets: []projects.ProjectRepositoryReadFacet{
			{FacetKey: "scripts", Status: "active"},
			{FacetKey: "repos", Folder: "repos", Enabled: true, Present: true, Status: "active"},
		},
		Source: &projects.ProjectRepositoryReadSource{
			ProjectContractSchemaVersion: projects.ProjectRepositoryProjectSchemaV04,
			ReposContractSchemaVersion:   projects.ProjectRepositoryReposSchemaV04,
			ProjectRoot:                  "/registered/project",
			ProjectContractPath:          "/registered/project/.loom/project.yaml",
			ReposContractPath:            "/registered/project/.loom/contracts/repos.yaml",
			OwnerNode:                    "main",
			SemanticDigest:               testDigest("a"),
			LocationDigest:               testDigest("b"),
			SourceRevision:               3,
			RegisteredAt:                 observedAt.Add(-2 * time.Hour),
		},
		Members: []projects.ProjectRepositoryReadMember{
			{RepositoryID: "repo_b", RepositoryOwnerProjectID: "project_test", Key: "missing", Path: "missing", Role: projects.ProjectRepositoryRoleComponent, MembershipLifecycle: projects.RepositoryLifecycleActive, RepositoryLifecycle: projects.RepositoryLifecycleActive, SourceBindingDigest: testDigest("d")},
			{RepositoryID: "repo_a", RepositoryOwnerProjectID: "project_test", Key: "primary", Path: "primary", Role: projects.ProjectRepositoryRolePrimary, StateRoot: ".repo", MembershipLifecycle: projects.RepositoryLifecycleActive, RepositoryLifecycle: projects.RepositoryLifecycleActive, SourceBindingDigest: testDigest("c")},
		},
	}}
	paths := &staticPathResolver{byRelative: map[string]ResolvedPath{
		"repos/primary": {Path: "/resolved/primary", Exists: true, Directory: true},
		"repos/missing": {Path: "/registered/project/repos/missing"},
	}}
	git := &staticGitObserver{byRoot: map[string]GitProjection{
		"/resolved/primary": {Worktree: true, RootMatchesMember: true, Head: "0123456789abcdef0123456789abcdef01234567", HeadPosture: GitHeadObserved},
	}}
	service := Service{
		Reader: reader,
		Git:    git,
		Paths:  paths,
		DevelopmentState: staticDevelopmentStateInspector{projection: DevelopmentStateProjection{
			Posture: DevelopmentStateEnabled, RelativePath: ".repo/repo.yaml", SourceDigest: testDigest("e"),
		}},
		Clock:     fixedClock{value: observedAt},
		LocalNode: "main",
	}

	projection, err := service.ObserveProject(context.Background(), "test")
	if err != nil {
		t.Fatalf("ObserveProject returned error: %v", err)
	}
	if projection.SchemaVersion != SchemaVersion || projection.Source.SourceRevision != 3 || !projection.ObservedAt.Equal(observedAt) {
		t.Fatalf("unexpected project projection: %#v", projection)
	}
	if got := []string{projection.Facets[0].Key, projection.Facets[1].Key}; !reflect.DeepEqual(got, []string{"repos", "scripts"}) {
		t.Fatalf("facet order = %#v", got)
	}
	if got := []string{projection.Members[0].RepositoryID, projection.Members[1].RepositoryID}; !reflect.DeepEqual(got, []string{"repo_a", "repo_b"}) {
		t.Fatalf("member order = %#v", got)
	}
	primary := projection.Members[0]
	if primary.ObservationPosture != projects.ProjectRepositoryObservationObserved || primary.Git == nil || !primary.Git.Worktree || primary.Git.WorktreeCanonicalProjectState {
		t.Fatalf("worktree observation = %#v", primary)
	}
	missing := projection.Members[1]
	if missing.ObservationPosture != projects.ProjectRepositoryObservationNotObserved || missing.ReasonCode != "member_path_missing" || missing.ObservedAt != nil {
		t.Fatalf("missing observation = %#v", missing)
	}
	if projection.Observation.Posture != projects.ProjectRepositoryObservationNotObserved || projection.Observation.Observed != 1 || projection.Observation.NotObserved != 1 {
		t.Fatalf("observation summary = %#v", projection.Observation)
	}
	if !reflect.DeepEqual(paths.roots, []string{"/registered/project", "/registered/project"}) || !reflect.DeepEqual(paths.calls, []string{"repos/primary", "repos/missing"}) {
		t.Fatalf("observer used sources outside the registered member set: %#v", paths.calls)
	}
	if primary.RelativeSource != "primary" || missing.RelativeSource != "missing" {
		t.Fatalf("portable relative member paths changed: primary=%q missing=%q", primary.RelativeSource, missing.RelativeSource)
	}
	repeated, err := service.ObserveProject(context.Background(), "test")
	if err != nil {
		t.Fatalf("repeat ObserveProject returned error: %v", err)
	}
	if !reflect.DeepEqual(projection, repeated) {
		t.Fatalf("fixed input/clock projection was not deterministic:\nfirst=%#v\nsecond=%#v", projection, repeated)
	}
}

func TestObserveProjectLocalRepositoryMatrixBelowReposFacet(t *testing.T) {
	projectRoot := t.TempDir()
	repositoriesRoot := filepath.Join(projectRoot, "repos")
	if err := os.Mkdir(repositoriesRoot, 0o755); err != nil {
		t.Fatalf("mkdir repositories facet: %v", err)
	}

	presentRoot := filepath.Join(repositoriesRoot, "present")
	initializeCommittedRepository(t, presentRoot)

	dirtyRoot := filepath.Join(repositoriesRoot, "dirty")
	initializeCommittedRepository(t, dirtyRoot)
	mustWriteFile(t, filepath.Join(dirtyRoot, "tracked.txt"), "changed\n")

	nestedRoot := filepath.Join(repositoriesRoot, "groups", "nested")
	initializeCommittedRepository(t, nestedRoot)

	linkedSource := filepath.Join(t.TempDir(), "linked-source")
	initializeCommittedRepository(t, linkedSource)
	linkedRoot := filepath.Join(repositoriesRoot, "linked")
	mustRunGit(t, linkedSource, "worktree", "add", "-b", "linked-observation-service", linkedRoot)

	validStateRoot := filepath.Join(repositoriesRoot, "state-valid")
	initializeCommittedRepository(t, validStateRoot)
	writeDevelopmentStateFixture(t, validStateRoot, "repository:\n  id: repo_state_valid\n  owning_project:\n    id: project_matrix\n")

	missingStateRoot := filepath.Join(repositoriesRoot, "state-missing")
	initializeCommittedRepository(t, missingStateRoot)

	malformedStateRoot := filepath.Join(repositoriesRoot, "state-malformed")
	initializeCommittedRepository(t, malformedStateRoot)
	writeDevelopmentStateFixture(t, malformedStateRoot, "repository: [unterminated\n")

	mismatchedStateRoot := filepath.Join(repositoriesRoot, "state-mismatched")
	initializeCommittedRepository(t, mismatchedStateRoot)
	writeDevelopmentStateFixture(t, mismatchedStateRoot, "repository:\n  id: repo_other\n  project_id: project_matrix\n")

	members := []projects.ProjectRepositoryReadMember{
		localReadMember("repo_present", "present", "present", ""),
		localReadMember("repo_absent", "absent", "absent", ""),
		localReadMember("repo_dirty", "dirty", "dirty", ""),
		localReadMember("repo_nested", "nested", "groups/nested", ""),
		localReadMember("repo_linked", "linked", "linked", ""),
		localReadMember("repo_state_valid", "state-valid", "state-valid", ".repo"),
		localReadMember("repo_state_missing", "state-missing", "state-missing", ".repo"),
		localReadMember("repo_state_malformed", "state-malformed", "state-malformed", ".repo"),
		localReadMember("repo_state_mismatched", "state-mismatched", "state-mismatched", ".repo"),
	}
	observedAt := time.Date(2026, 8, 30, 8, 0, 0, 0, time.UTC)
	service := Service{
		Reader: staticProjectReader{model: localReadModel(projectRoot, "project_matrix", members)},
		Clock:  fixedClock{value: observedAt}, LocalNode: "main",
	}
	projection, err := service.ObserveProject(context.Background(), "project_matrix")
	if err != nil {
		t.Fatalf("ObserveProject returned error: %v", err)
	}
	byKey := make(map[string]RepositoryProjection, len(projection.Members))
	for _, member := range projection.Members {
		byKey[member.Key] = member
		if filepath.IsAbs(member.RelativeSource) || member.RelativeSource == "repos" || strings.HasPrefix(member.RelativeSource, "repos/") {
			t.Fatalf("surface path gained physical repos prefix: key=%s path=%q", member.Key, member.RelativeSource)
		}
	}

	assertObservedRepository(t, byKey["present"], "present")
	if byKey["present"].DevelopmentState.Posture != DevelopmentStateNotEnabled {
		t.Fatalf("present missing state posture = %#v", byKey["present"].DevelopmentState)
	}
	absent := byKey["absent"]
	if absent.ObservationPosture != projects.ProjectRepositoryObservationNotObserved || absent.ReasonCode != "member_path_missing" || absent.Git != nil {
		t.Fatalf("absent member posture = %#v", absent)
	}
	dirty := byKey["dirty"]
	assertObservedRepository(t, dirty, "dirty")
	if !dirty.Git.Dirty.Dirty || !dirty.Git.Dirty.TrackedChanges {
		t.Fatalf("dirty member posture = %#v", dirty.Git.Dirty)
	}
	nested := byKey["nested"]
	assertObservedRepository(t, nested, "groups/nested")
	if !nested.Git.RootMatchesMember {
		t.Fatalf("nested checkout root posture = %#v", nested.Git)
	}
	linked := byKey["linked"]
	assertObservedRepository(t, linked, "linked")
	if !linked.Git.Worktree || linked.Git.WorktreeCanonicalProjectState {
		t.Fatalf("linked worktree posture = %#v", linked.Git)
	}
	validState := byKey["state-valid"]
	assertObservedRepository(t, validState, "state-valid")
	if validState.DevelopmentState.Posture != DevelopmentStateEnabled || validState.DevelopmentState.RelativePath != ".repo/repo.yaml" {
		t.Fatalf("valid development state = %#v", validState.DevelopmentState)
	}
	missingState := byKey["state-missing"]
	assertObservedRepository(t, missingState, "state-missing")
	if missingState.DevelopmentState.Posture != DevelopmentStateNotEnabled || missingState.DevelopmentState.RelativePath != ".repo" {
		t.Fatalf("missing development state = %#v", missingState.DevelopmentState)
	}
	malformedState := byKey["state-malformed"]
	assertObservedRepository(t, malformedState, "state-malformed")
	if malformedState.DevelopmentState.Posture != DevelopmentStateInvalid || malformedState.DevelopmentState.ReasonCode != "identity_malformed" {
		t.Fatalf("malformed development state = %#v", malformedState.DevelopmentState)
	}
	mismatchedState := byKey["state-mismatched"]
	assertObservedRepository(t, mismatchedState, "state-mismatched")
	if mismatchedState.DevelopmentState.Posture != DevelopmentStateMismatch || mismatchedState.DevelopmentState.ReasonCode != "identity_backlink_mismatch" {
		t.Fatalf("mismatched development state = %#v", mismatchedState.DevelopmentState)
	}
}

func TestObserveProjectRejectsReposFacetPathEscapeBeforeGitObservation(t *testing.T) {
	projectRoot := t.TempDir()
	repositoriesRoot := filepath.Join(projectRoot, "repos")
	if err := os.Mkdir(repositoriesRoot, 0o755); err != nil {
		t.Fatalf("mkdir repositories facet: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	initializeCommittedRepository(t, outside)
	if err := os.Symlink(outside, filepath.Join(repositoriesRoot, "escaped")); err != nil {
		t.Fatalf("create escaping member symlink: %v", err)
	}
	git := &staticGitObserver{}
	service := Service{
		Reader: staticProjectReader{model: localReadModel(projectRoot, "project_escape", []projects.ProjectRepositoryReadMember{
			localReadMember("repo_escape", "escaped", "escaped", ""),
		})},
		Git: git, Clock: fixedClock{value: time.Date(2026, 8, 30, 8, 30, 0, 0, time.UTC)}, LocalNode: "main",
	}
	projection, err := service.ObserveProject(context.Background(), "project_escape")
	if err != nil {
		t.Fatalf("ObserveProject returned error: %v", err)
	}
	member := projection.Members[0]
	if member.RelativeSource != "escaped" || member.ObservationPosture != projects.ProjectRepositoryObservationNotObserved || member.ReasonCode != "path_escape" || member.Git != nil {
		t.Fatalf("escaping member posture = %#v", member)
	}
	if len(git.calls) != 0 {
		t.Fatalf("escaping member reached Git observation: %#v", git.calls)
	}
}

func TestObserveProjectRejectsReplacedReposFacetRootBeforeDevelopmentStateOrGit(t *testing.T) {
	projectRoot := t.TempDir()
	repositoriesRoot := filepath.Join(projectRoot, "repos")
	if err := os.Mkdir(repositoriesRoot, 0o755); err != nil {
		t.Fatalf("mkdir original repositories facet: %v", err)
	}
	model := localReadModel(projectRoot, "project_replaced_facet", []projects.ProjectRepositoryReadMember{
		localReadMember("repo_outside", "outside", "backend", ".repo"),
	})

	outsideFacet := filepath.Join(t.TempDir(), "outside-repos")
	outsideMember := filepath.Join(outsideFacet, "backend")
	initializeCommittedRepository(t, outsideMember)
	writeDevelopmentStateFixture(t, outsideMember, "repository:\n  id: repo_outside\n  project_id: project_replaced_facet\n")
	if err := os.Remove(repositoriesRoot); err != nil {
		t.Fatalf("remove original repositories facet: %v", err)
	}
	if err := os.Symlink(outsideFacet, repositoriesRoot); err != nil {
		t.Fatalf("replace repositories facet with symlink: %v", err)
	}

	git := &staticGitObserver{}
	developmentState := &recordingDevelopmentStateInspector{}
	service := Service{
		Reader: staticProjectReader{model: model}, Git: git, DevelopmentState: developmentState,
		Clock: fixedClock{value: time.Date(2026, 8, 30, 8, 45, 0, 0, time.UTC)}, LocalNode: "main",
	}
	projection, err := service.ObserveProject(context.Background(), "project_replaced_facet")
	if err != nil {
		t.Fatalf("ObserveProject returned error: %v", err)
	}
	member := projection.Members[0]
	if member.RepositoryID != "repo_outside" || member.RelativeSource != "backend" || member.ObservationPosture != projects.ProjectRepositoryObservationNotObserved || member.ReasonCode != "path_escape" || member.Git != nil {
		t.Fatalf("replaced repositories facet posture = %#v", member)
	}
	if len(developmentState.calls) != 0 || len(git.calls) != 0 {
		t.Fatalf("replaced repositories facet reached local inspectors: development=%#v git=%#v", developmentState.calls, git.calls)
	}
}

func TestServiceReportsRemoteUnavailableWithoutFilesystemOrGitInference(t *testing.T) {
	now := time.Date(2026, 8, 29, 21, 0, 0, 0, time.UTC)
	paths := &staticPathResolver{}
	git := &staticGitObserver{}
	developmentState := &recordingDevelopmentStateInspector{}
	service := Service{
		Reader: staticProjectReader{model: projects.ProjectRepositoryReadModel{
			Project: projects.ProjectRepositoryReadProject{ProjectID: "project_remote", Slug: "remote", Status: "active"},
			Source:  &projects.ProjectRepositoryReadSource{ProjectContractSchemaVersion: projects.ProjectRepositoryProjectSchemaV04, ReposContractSchemaVersion: projects.ProjectRepositoryReposSchemaV04, ProjectContractPath: "/remote/project/.loom/project.yaml", ReposContractPath: "/remote/project/.loom/contracts/repos.yaml", ProjectRoot: "/remote/project", OwnerNode: "workspace", SemanticDigest: testDigest("a"), LocationDigest: testDigest("b")},
			Members: []projects.ProjectRepositoryReadMember{{RepositoryID: "repo_remote", RepositoryOwnerProjectID: "project_remote", Path: "repository", Role: projects.ProjectRepositoryRolePrimary, SourceBindingDigest: testDigest("c")}},
		}},
		Paths: paths, Git: git, DevelopmentState: developmentState, Clock: fixedClock{value: now}, LocalNode: "main",
	}
	projection, err := service.ObserveProject(context.Background(), "remote")
	if err != nil {
		t.Fatalf("ObserveProject returned error: %v", err)
	}
	member := projection.Members[0]
	if member.ObservationPosture != projects.ProjectRepositoryObservationRemoteUnavailable || member.ReasonCode != "owner_node_not_local" || member.ObservedAt == nil || member.DevelopmentState.Posture != DevelopmentStateNotObserved {
		t.Fatalf("remote member = %#v", member)
	}
	if len(paths.calls) != 0 || len(git.calls) != 0 || len(developmentState.calls) != 0 {
		t.Fatalf("remote observation touched local state: paths=%#v git=%#v development=%#v", paths.calls, git.calls, developmentState.calls)
	}
}

func TestServiceKeepsV03ProjectWithoutRepositorySourceExplicitlyEmpty(t *testing.T) {
	service := Service{
		Reader: staticProjectReader{model: projects.ProjectRepositoryReadModel{Project: projects.ProjectRepositoryReadProject{ProjectID: "project_v03", Slug: "legacy", Status: "active"}, Facets: []projects.ProjectRepositoryReadFacet{}}},
		Clock:  fixedClock{value: time.Date(2026, 8, 29, 22, 0, 0, 0, time.UTC)}, LocalNode: "main",
	}
	projection, err := service.ObserveProject(context.Background(), "legacy")
	if err != nil {
		t.Fatalf("ObserveProject returned error: %v", err)
	}
	if projection.Source.Posture != SourcePostureNotRegistered || len(projection.Members) != 0 || projection.Observation.MemberCount != 0 {
		t.Fatalf("legacy projection invented repository state: %#v", projection)
	}
}

func TestServiceReturnsTypedUnavailableProjectError(t *testing.T) {
	service := Service{Reader: staticProjectReader{err: errors.New("backend offline")}, LocalNode: "main"}
	_, err := service.ObserveProject(context.Background(), "missing")
	if !errors.Is(err, ErrProjectStateUnavailable) {
		t.Fatalf("unavailable error = %v", err)
	}
}

type staticProjectReader struct {
	model projects.ProjectRepositoryReadModel
	err   error
}

func (r staticProjectReader) ReadProjectRepositoryState(context.Context, string) (projects.ProjectRepositoryReadModel, error) {
	return r.model, r.err
}

type staticPathResolver struct {
	byRelative map[string]ResolvedPath
	errors     map[string]error
	roots      []string
	calls      []string
}

func (r *staticPathResolver) ResolveWithin(root string, relative string) (ResolvedPath, error) {
	r.roots = append(r.roots, root)
	r.calls = append(r.calls, relative)
	if err := r.errors[relative]; err != nil {
		return ResolvedPath{}, err
	}
	return r.byRelative[relative], nil
}

type staticGitObserver struct {
	byRoot map[string]GitProjection
	errors map[string]error
	calls  []string
}

func (o *staticGitObserver) Observe(_ context.Context, root string) (GitProjection, error) {
	o.calls = append(o.calls, root)
	if err := o.errors[root]; err != nil {
		return GitProjection{}, err
	}
	return o.byRoot[root], nil
}

type staticDevelopmentStateInspector struct{ projection DevelopmentStateProjection }

func (i staticDevelopmentStateInspector) Inspect(context.Context, DevelopmentStateInput) DevelopmentStateProjection {
	return i.projection
}

type recordingDevelopmentStateInspector struct {
	calls []DevelopmentStateInput
}

func (i *recordingDevelopmentStateInspector) Inspect(_ context.Context, input DevelopmentStateInput) DevelopmentStateProjection {
	i.calls = append(i.calls, input)
	return DevelopmentStateProjection{Posture: DevelopmentStateNotObserved}
}

type fixedClock struct{ value time.Time }

func (c fixedClock) Now() time.Time { return c.value }

func testDigest(character string) string {
	return "sha256:" + character + "000000000000000000000000000000000000000000000000000000000000000"
}

func initializeCommittedRepository(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		t.Fatalf("mkdir repository parent: %v", err)
	}
	mustRunGit(t, "", "init", root)
	mustRunGit(t, root, "config", "user.email", "test@example.invalid")
	mustRunGit(t, root, "config", "user.name", "LOOM Test")
	mustWriteFile(t, filepath.Join(root, "tracked.txt"), "initial\n")
	mustRunGit(t, root, "add", "tracked.txt")
	mustRunGit(t, root, "commit", "-m", "initial")
}

func writeDevelopmentStateFixture(t *testing.T, memberRoot, contents string) {
	t.Helper()
	stateRoot := filepath.Join(memberRoot, ".repo")
	if err := os.Mkdir(stateRoot, 0o755); err != nil {
		t.Fatalf("mkdir development state root: %v", err)
	}
	mustWriteFile(t, filepath.Join(stateRoot, "repo.yaml"), contents)
}

func localReadMember(repositoryID, key, path, stateRoot string) projects.ProjectRepositoryReadMember {
	return projects.ProjectRepositoryReadMember{
		RepositoryID: repositoryID, RepositoryOwnerProjectID: "project_matrix", Key: key, Path: path,
		Role: projects.ProjectRepositoryRoleComponent, StateRoot: stateRoot,
		MembershipLifecycle: projects.RepositoryLifecycleActive, RepositoryLifecycle: projects.RepositoryLifecycleActive,
		SourceBindingDigest: testDigest("c"),
	}
}

func localReadModel(projectRoot, projectID string, members []projects.ProjectRepositoryReadMember) projects.ProjectRepositoryReadModel {
	for index := range members {
		members[index].RepositoryOwnerProjectID = projectID
	}
	return projects.ProjectRepositoryReadModel{
		Project: projects.ProjectRepositoryReadProject{ProjectID: projectID, Slug: projectID, Status: "active"},
		Source: &projects.ProjectRepositoryReadSource{
			ProjectContractSchemaVersion: projects.ProjectRepositoryProjectSchemaV04,
			ReposContractSchemaVersion:   projects.ProjectRepositoryReposSchemaV04,
			ProjectContractPath:          filepath.Join(projectRoot, ".loom/project.yaml"),
			ReposContractPath:            filepath.Join(projectRoot, ".loom/contracts/repos.yaml"),
			ProjectRoot:                  projectRoot, OwnerNode: "main", SemanticDigest: testDigest("a"), LocationDigest: testDigest("b"),
		},
		Members: members,
	}
}

func assertObservedRepository(t *testing.T, member RepositoryProjection, relativeSource string) {
	t.Helper()
	if member.RelativeSource != relativeSource || member.ObservationPosture != projects.ProjectRepositoryObservationObserved || member.Git == nil {
		t.Fatalf("observed member %q = %#v", relativeSource, member)
	}
}
