package projectcontracts

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"loom.local/loom/internal/ids"
)

const repositoryContractFixtureProjectID = "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"

func TestRepositorySourceSnapshotUsesExactValidatedBytesForEmptyAndPopulatedContracts(t *testing.T) {
	tests := []struct {
		name         string
		reposFixture string
		reposRaw     string
		wantVersion  string
	}{
		{
			name: "v0.4 empty members and watch roots",
			reposRaw: `kind: loom.repos
schema_version: repos.contract.v0.4
repos:
  status: active
  watch_roots: []
  members: []
`,
			wantVersion: ReposSchemaV04,
		},
		{name: "v0.4 populated", reposFixture: "repos_v04_custom_members.yaml", wantVersion: ReposSchemaV04},
		{name: "v0.3 watch policy", reposFixture: "repos_v03_custom.yaml", wantVersion: ReposSchemaV03},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectFixture := "project_v04.yaml"
			if test.wantVersion == ReposSchemaV03 {
				projectFixture = "project_v03.yaml"
			}
			root := repositoryContractFixture(t, projectFixture, firstNonEmptyString(test.reposFixture, "repos_v04_default_empty.yaml"))
			if test.reposRaw != "" {
				writeFile(t, root, ".loom/contracts/repos.yaml", test.reposRaw, 0o600)
			}
			raw, err := os.ReadFile(filepath.Join(root, ".loom", "contracts", "repos.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			analysis := Analyze(root)
			assertRepositoryAnalysisOK(t, analysis)
			if analysis.Report.RepositorySource == nil || analysis.Plan.RepositorySource == nil {
				t.Fatalf("repository source snapshot missing: report=%#v plan=%#v", analysis.Report.RepositorySource, analysis.Plan.RepositorySource)
			}
			want := RepositorySourceSnapshot{
				ContractPath:          filepath.ToSlash(filepath.Join(root, ".loom", "contracts", "repos.yaml")),
				ContractHash:          hashBytesURI(raw),
				ContractSchemaVersion: test.wantVersion,
			}
			if *analysis.Report.RepositorySource != want || *analysis.Plan.RepositorySource != want {
				t.Fatalf("repository source snapshots = report %#v plan %#v, want %#v", analysis.Report.RepositorySource, analysis.Plan.RepositorySource, want)
			}
			if analysis.Report.RepositorySource == analysis.Plan.RepositorySource {
				t.Fatal("report and plan must own separate immutable snapshot values")
			}
		})
	}
}

func TestRepositorySourceSnapshotIsNilForV03WithoutRepositoriesFacet(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, CanonicalRootContractPath, `kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: legacy-without-repositories
  name: Legacy Without Repositories
  owner_node: main
`, 0o600)
	analysis := Analyze(root)
	assertRepositoryAnalysisOK(t, analysis)
	if analysis.Report.RepositorySource != nil || analysis.Plan.RepositorySource != nil {
		t.Fatalf("legacy source = report %#v plan %#v, want nil", analysis.Report.RepositorySource, analysis.Plan.RepositorySource)
	}
}

func TestInvariantAV03DefaultRootsRemainWatchPolicyOnly(t *testing.T) {
	v03 := repositoryContractFixture(t, "project_v03.yaml", "repos_v03_default.yaml")
	v04 := repositoryContractFixture(t, "project_v04.yaml", "repos_v04_default_empty.yaml")

	v03Analysis := Analyze(v03)
	v04Analysis := Analyze(v04)
	assertRepositoryAnalysisOK(t, v03Analysis)
	assertRepositoryAnalysisOK(t, v04Analysis)
	if len(v03Analysis.Report.RepositoryMembers) != 0 || len(v04Analysis.Report.RepositoryMembers) != 0 {
		t.Fatalf("watched roots must not imply members: v0.3=%#v v0.4=%#v", v03Analysis.Report.RepositoryMembers, v04Analysis.Report.RepositoryMembers)
	}
	assertEffectiveRepoPolicyEqual(t, v03Analysis, v04Analysis)
}

func TestInvariantAV03CustomPolicyMapsOnlyToV04WatchRoots(t *testing.T) {
	v03 := repositoryContractFixture(t, "project_v03.yaml", "repos_v03_custom.yaml")
	v04 := repositoryContractFixture(t, "project_v04.yaml", "repos_v04_custom_members.yaml")

	v03Analysis := Analyze(v03)
	v04Analysis := Analyze(v04)
	assertRepositoryAnalysisOK(t, v03Analysis)
	assertRepositoryAnalysisOK(t, v04Analysis)
	assertEffectiveRepoPolicyEqual(t, v03Analysis, v04Analysis)
	if len(v03Analysis.Report.RepositoryMembers) != 0 {
		t.Fatalf("v0.3 roots inferred members: %#v", v03Analysis.Report.RepositoryMembers)
	}
	if len(v04Analysis.Report.RepositoryMembers) != 2 {
		t.Fatalf("explicit v0.4 members = %#v, want two", v04Analysis.Report.RepositoryMembers)
	}
}

func TestInvariantAV03ValidationAndMigrationPlanAreReadOnly(t *testing.T) {
	root := repositoryContractFixture(t, "project_v03.yaml", "repos_v03_custom.yaml")
	projectBefore := readFile(t, root, CanonicalRootContractPath)
	reposBefore := readFile(t, root, ".loom/contracts/repos.yaml")

	for range 2 {
		analysis := Analyze(root)
		assertRepositoryAnalysisOK(t, analysis)
	}
	plan, err := PlanProjectContractV04Migration(ProjectContractV04MigrationOptions{
		ProjectRoot: root,
		ProjectID:   repositoryContractFixtureProjectID,
	})
	if err != nil {
		t.Fatalf("PlanProjectContractV04Migration returned error: %v", err)
	}
	if plan.Project.Project.ID != repositoryContractFixtureProjectID {
		t.Fatalf("planned project ID = %q", plan.Project.Project.ID)
	}
	if len(plan.Repos.Repos.Members) != 0 {
		t.Fatalf("migration plan inferred members: %#v", plan.Repos.Repos.Members)
	}
	if strings.Contains(reposBefore, "repo_") {
		t.Fatal("v0.3 fixture unexpectedly contains a repository ID")
	}
	if got := readFile(t, root, CanonicalRootContractPath); got != projectBefore {
		t.Fatal("validation or migration planning rewrote the project contract")
	}
	if got := readFile(t, root, ".loom/contracts/repos.yaml"); got != reposBefore {
		t.Fatal("validation or migration planning rewrote the repos contract")
	}
}

func TestInvariantAV03RemainsReadableAndRegisterable(t *testing.T) {
	root := repositoryContractFixture(t, "project_v03.yaml", "repos_v03_default.yaml")
	analysis := Analyze(root)
	assertRepositoryAnalysisOK(t, analysis)
	if !analysis.Report.Registerable || !analysis.Plan.Registerable {
		t.Fatalf("v0.3 compatibility project is not registerable: report=%#v plan=%#v", analysis.Report, analysis.Plan)
	}
}

func TestInvariantAV04PolicyAndMembersValidateIndependently(t *testing.T) {
	root := repositoryContractFixture(t, "project_v04.yaml", "repos_v04_custom_members.yaml")
	valid := Analyze(root)
	assertRepositoryAnalysisOK(t, valid)
	wantPolicy := effectiveRepoPolicy(valid)

	invalidMember := strings.Replace(readFile(t, root, ".loom/contracts/repos.yaml"), "path: source/backend", "path: ../outside", 1)
	writeFile(t, root, ".loom/contracts/repos.yaml", invalidMember, 0o600)
	memberAnalysis := Analyze(root)
	assertDiagnostic(t, memberAnalysis.Report.Diagnostics, "repos.member_path_unsafe")
	if got := effectiveRepoPolicy(memberAnalysis); !reflect.DeepEqual(got, wantPolicy) {
		t.Fatalf("member validation changed watch policy:\n got %#v\nwant %#v", got, wantPolicy)
	}

	invalidPolicy := strings.Replace(readFixture(t, "repos_v04_custom_members.yaml"), "path: source\n", "path: ../outside\n", 1)
	writeFile(t, root, ".loom/contracts/repos.yaml", invalidPolicy, 0o600)
	policyAnalysis := Analyze(root)
	assertDiagnostic(t, policyAnalysis.Report.Diagnostics, "repos.path_unsafe")
	if len(policyAnalysis.Report.RepositoryMembers) != 2 {
		t.Fatalf("watch policy validation discarded explicit members: %#v", policyAnalysis.Report.RepositoryMembers)
	}
}

func TestInvariantBExplicitScaffoldGeneratesStableIdentityOnlyOnApply(t *testing.T) {
	dryParent := t.TempDir()
	dry, err := ScaffoldProject(ScaffoldOptions{
		Name: "Dry Repository Contract", Slug: "dry-repository-contract", OwnerNode: "main", Preset: PresetMinimal,
		Facets: []string{"repos"}, Directory: dryParent, DryRun: true,
		RepositoryMembers: []RepoMemberSpec{{Key: "backend", Path: "backend", Role: RepositoryRolePrimary}},
	})
	if err != nil {
		t.Fatalf("dry-run scaffold returned error: %v", err)
	}
	if dry.ProjectID != "" || dry.RepositoryMembers[0].ID != "" {
		t.Fatalf("dry-run generated identity: project=%q members=%#v", dry.ProjectID, dry.RepositoryMembers)
	}
	assertNoFile(t, dryParent, "dry-repository-contract")

	result, err := ScaffoldProject(ScaffoldOptions{
		Name: "Repository Contract", Slug: "repository-contract", OwnerNode: "main", Preset: PresetMinimal,
		Facets: []string{"repos"}, Directory: t.TempDir(),
		RepositoryMembers: []RepoMemberSpec{
			{Key: "api", Path: "api", Role: RepositoryRolePrimary, StateRoot: ".repo"},
			{Key: "portal", Path: "portal", Role: RepositoryRolePrimary},
		},
	})
	if err != nil {
		t.Fatalf("ScaffoldProject returned error: %v", err)
	}
	if err := ids.Validate(ids.ProjectPrefix, result.ProjectID); err != nil {
		t.Fatalf("generated project ID is invalid: %v", err)
	}
	if len(result.RepositoryMembers) != 2 {
		t.Fatalf("scaffold members = %#v", result.RepositoryMembers)
	}
	for _, member := range result.RepositoryMembers {
		if err := ids.Validate(RepositoryIDPrefix, member.ID); err != nil {
			t.Fatalf("generated repository ID is invalid: %v", err)
		}
	}

	projectBefore := readFile(t, result.ProjectRoot, CanonicalRootContractPath)
	reposBefore := readFile(t, result.ProjectRoot, ".loom/contracts/repos.yaml")
	first := Analyze(result.ProjectRoot)
	second := Analyze(result.ProjectRoot)
	assertRepositoryAnalysisOK(t, first)
	assertRepositoryAnalysisOK(t, second)
	if first.Report.Project.ID != result.ProjectID || second.Plan.Project.ID != result.ProjectID {
		t.Fatalf("project identity was not stable: result=%q first=%q second=%q", result.ProjectID, first.Report.Project.ID, second.Plan.Project.ID)
	}
	if !reflect.DeepEqual(first.Report.RepositoryMembers, second.Report.RepositoryMembers) {
		t.Fatalf("repository identities changed across validation: first=%#v second=%#v", first.Report.RepositoryMembers, second.Report.RepositoryMembers)
	}
	if readFile(t, result.ProjectRoot, CanonicalRootContractPath) != projectBefore || readFile(t, result.ProjectRoot, ".loom/contracts/repos.yaml") != reposBefore {
		t.Fatal("validation rewrote scaffolded identities")
	}
}

func TestInvariantBForcedScaffoldPreservesExistingExplicitIdentity(t *testing.T) {
	options := ScaffoldOptions{
		Name: "Stable Scaffold", Slug: "stable-scaffold", OwnerNode: "main", Preset: PresetMinimal,
		Facets: []string{"repos"}, Directory: t.TempDir(),
		RepositoryMembers: []RepoMemberSpec{{Key: "backend", Path: "backend", Role: RepositoryRolePrimary}},
	}
	first, err := ScaffoldProject(options)
	if err != nil {
		t.Fatalf("initial scaffold returned error: %v", err)
	}
	options.Force = true
	second, err := ScaffoldProject(options)
	if err != nil {
		t.Fatalf("forced scaffold returned error: %v", err)
	}
	if second.ProjectID != first.ProjectID || second.RepositoryMembers[0].ID != first.RepositoryMembers[0].ID {
		t.Fatalf("forced scaffold replaced stable identity: first=%#v second=%#v", first, second)
	}
}

func TestInvariantBForcedScaffoldCannotReinterpretV03WatchPolicy(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repository-contract-smoke")
	writeFile(t, root, CanonicalRootContractPath, readFixture(t, "project_v03.yaml"), 0o600)
	writeFile(t, root, ".loom/contracts/repos.yaml", readFixture(t, "repos_v03_custom.yaml"), 0o600)
	mkdir(t, root, "repos")
	before := readFile(t, root, ".loom/contracts/repos.yaml")
	_, err := ScaffoldProject(ScaffoldOptions{
		Name: "Repository Contract Smoke", Slug: "repository-contract-smoke", OwnerNode: "main", Preset: PresetMinimal,
		Facets: []string{"repos"}, Directory: parent, Force: true, ProjectID: repositoryContractFixtureProjectID,
	})
	if err == nil || !strings.Contains(err.Error(), "cannot reinterpret") {
		t.Fatalf("forced v0.3 scaffold was not rejected: %v", err)
	}
	if got := readFile(t, root, ".loom/contracts/repos.yaml"); got != before {
		t.Fatal("rejected forced scaffold changed v0.3 watched-root policy")
	}
}

func TestInvariantBMigrationBindsExistingProjectIdentityAndGeneratesMembersOnlyOnApply(t *testing.T) {
	root := repositoryContractFixture(t, "project_v03.yaml", "repos_v03_default.yaml")
	plan, err := PlanProjectContractV04Migration(ProjectContractV04MigrationOptions{
		ProjectRoot:       root,
		ProjectID:         repositoryContractFixtureProjectID,
		RepositoryMembers: []RepoMemberSpec{{Key: "backend", Path: "backend", Role: RepositoryRoleComponent}},
	})
	if err != nil {
		t.Fatalf("plan returned error: %v", err)
	}
	if plan.Project.Project.ID != repositoryContractFixtureProjectID || plan.Repos.Repos.Members[0].ID != "" {
		t.Fatalf("dry-run plan changed or generated identity: %#v", plan)
	}
	notApplied, err := ApplyProjectContractV04Migration(plan, false)
	if err == nil || notApplied.Repos.Repos.Members[0].ID != "" {
		t.Fatalf("unconfirmed apply generated identity or succeeded: result=%#v err=%v", notApplied, err)
	}
	result, err := ApplyProjectContractV04Migration(plan, true)
	if err != nil {
		t.Fatalf("apply returned error: %v", err)
	}
	if result.Project.Project.ID != repositoryContractFixtureProjectID {
		t.Fatalf("migration created a second project identity: %#v", result.Project.Project)
	}
	if err := ids.Validate(RepositoryIDPrefix, result.Repos.Repos.Members[0].ID); err != nil {
		t.Fatalf("apply did not generate a valid member ID: %v", err)
	}
	analysis := Analyze(root)
	assertRepositoryAnalysisOK(t, analysis)
	if analysis.Report.Project.ID != repositoryContractFixtureProjectID || analysis.Report.RepositoryMembers[0].ID != result.Repos.Repos.Members[0].ID {
		t.Fatalf("applied identities were not preserved: %#v", analysis.Report)
	}
	migratedRepos := readFile(t, root, ".loom/contracts/repos.yaml")
	if !strings.Contains(migratedRepos, "watch_roots:") || strings.Contains(migratedRepos, "\n  roots:") {
		t.Fatalf("migration did not separate watch_roots from v0.3 roots:\n%s", migratedRepos)
	}
}

func TestInvariantBZeroMemberMigrationWritesExplicitEmptySet(t *testing.T) {
	root := repositoryContractFixture(t, "project_v03.yaml", "repos_v03_default.yaml")
	plan, err := PlanProjectContractV04Migration(ProjectContractV04MigrationOptions{ProjectRoot: root, ProjectID: repositoryContractFixtureProjectID})
	if err != nil {
		t.Fatalf("plan returned error: %v", err)
	}
	if _, err := ApplyProjectContractV04Migration(plan, true); err != nil {
		t.Fatalf("apply returned error: %v", err)
	}
	if migrated := readFile(t, root, ".loom/contracts/repos.yaml"); !strings.Contains(migrated, "members: []") {
		t.Fatalf("zero-member migration did not write an explicit empty set:\n%s", migrated)
	}
}

func TestInvariantBMigrationRejectsMutatedPlanTarget(t *testing.T) {
	root := repositoryContractFixture(t, "project_v03.yaml", "repos_v03_default.yaml")
	plan, err := PlanProjectContractV04Migration(ProjectContractV04MigrationOptions{ProjectRoot: root, ProjectID: repositoryContractFixtureProjectID})
	if err != nil {
		t.Fatalf("plan returned error: %v", err)
	}
	before := readFile(t, root, CanonicalRootContractPath)
	plan.Project.Project.ID = "project_01ARZ3NDEKTSV4RRFFQ69G5FAA"
	if _, err := ApplyProjectContractV04Migration(plan, true); err == nil || !strings.Contains(err.Error(), "target changed") {
		t.Fatalf("mutated migration plan was not rejected: %v", err)
	}
	if got := readFile(t, root, CanonicalRootContractPath); got != before {
		t.Fatal("rejected mutated migration plan changed source")
	}
}

func TestInvariantBUnlistedGitChildrenNeverBecomeMembers(t *testing.T) {
	root := repositoryContractFixture(t, "project_v04.yaml", "repos_v04_default_empty.yaml")
	mkdir(t, root, "repos/unlisted/.git")
	analysis := Analyze(root)
	assertRepositoryAnalysisOK(t, analysis)
	if len(analysis.Report.RepositoryMembers) != 0 || len(analysis.Plan.RepositoryMembers) != 0 {
		t.Fatalf("unlisted Git child was inferred as membership: report=%#v plan=%#v", analysis.Report.RepositoryMembers, analysis.Plan.RepositoryMembers)
	}
}

func TestInvariantBRepositoryIdentitySurvivesMoveAndPathRename(t *testing.T) {
	root := repositoryContractFixture(t, "project_v04.yaml", "repos_v04_custom_members.yaml")
	before := Analyze(root)
	assertRepositoryAnalysisOK(t, before)
	contract := strings.Replace(readFile(t, root, ".loom/contracts/repos.yaml"), "path: source/backend", "path: source/backend-renamed", 1)
	writeFile(t, root, ".loom/contracts/repos.yaml", contract, 0o600)
	afterRename := Analyze(root)
	assertRepositoryAnalysisOK(t, afterRename)
	if before.Report.RepositoryMembers[0].ID != afterRename.Report.RepositoryMembers[0].ID || before.Report.RepositoryMembers[0].Path == afterRename.Report.RepositoryMembers[0].Path {
		t.Fatalf("path rename did not preserve identity and expose path change: before=%#v after=%#v", before.Report.RepositoryMembers[0], afterRename.Report.RepositoryMembers[0])
	}

	moved := filepath.Join(t.TempDir(), "moved-project")
	if err := os.Rename(root, moved); err != nil {
		t.Fatalf("move project: %v", err)
	}
	afterMove := Analyze(moved)
	assertRepositoryAnalysisOK(t, afterMove)
	if afterMove.Report.Project.ID != before.Report.Project.ID || afterMove.Report.RepositoryMembers[0].ID != before.Report.RepositoryMembers[0].ID {
		t.Fatalf("move changed stable identities: before=%#v after=%#v", before.Report, afterMove.Report)
	}
}

func TestInvariantBMemberValidationFailsClosed(t *testing.T) {
	tests := []struct {
		name       string
		replaceOld string
		replaceNew string
		diagnostic string
	}{
		{name: "invalid member ID", replaceOld: "repo_01ARZ3NDEKTSV4RRFFQ69G5FAA", replaceNew: "repo_invalid", diagnostic: "repos.member_id_invalid"},
		{name: "unsafe path", replaceOld: "path: source/backend", replaceNew: "path: ../outside", diagnostic: "repos.member_path_unsafe"},
		{name: "reserved path", replaceOld: "path: source/backend", replaceNew: "path: .loom", diagnostic: "repos.member_path_reserved"},
		{name: "invalid role", replaceOld: "role: component", replaceNew: "role: owner", diagnostic: "repos.member_role_invalid"},
		{name: "unsafe state root", replaceOld: "state_root: .repo", replaceNew: "state_root: ../state", diagnostic: "repos.member_state_root_unsafe"},
		{name: "reserved state root", replaceOld: "state_root: .repo", replaceNew: "state_root: .git", diagnostic: "repos.member_state_root_reserved"},
		{name: "duplicate ID", replaceOld: "repo_01ARZ3NDEKTSV4RRFFQ69G5FAB", replaceNew: "repo_01ARZ3NDEKTSV4RRFFQ69G5FAA", diagnostic: "repos.member_id_duplicate"},
		{name: "duplicate key", replaceOld: "key: shared-library", replaceNew: "key: backend", diagnostic: "repos.member_key_duplicate"},
		{name: "duplicate path", replaceOld: "path: source/shared-library", replaceNew: "path: source/backend", diagnostic: "repos.member_path_duplicate"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := repositoryContractFixture(t, "project_v04.yaml", "repos_v04_custom_members.yaml")
			contract := strings.Replace(readFile(t, root, ".loom/contracts/repos.yaml"), test.replaceOld, test.replaceNew, 1)
			writeFile(t, root, ".loom/contracts/repos.yaml", contract, 0o600)
			analysis := Analyze(root)
			if analysis.Report.OK {
				t.Fatalf("invalid member contract unexpectedly validated: %#v", analysis.Report)
			}
			assertDiagnostic(t, analysis.Report.Diagnostics, test.diagnostic)
		})
	}
}

func TestInvariantBInvalidProjectIdentityFailsClosed(t *testing.T) {
	root := repositoryContractFixture(t, "project_v04.yaml", "repos_v04_default_empty.yaml")
	contract := strings.Replace(readFile(t, root, CanonicalRootContractPath), repositoryContractFixtureProjectID, "project_invalid", 1)
	writeFile(t, root, CanonicalRootContractPath, contract, 0o600)
	analysis := Analyze(root)
	if analysis.Report.OK {
		t.Fatal("invalid project identity unexpectedly validated")
	}
	assertDiagnostic(t, analysis.Report.Diagnostics, "project.id_invalid")
}

func TestInvariantBMemberSymlinkEscapeFailsClosed(t *testing.T) {
	root := repositoryContractFixture(t, "project_v04.yaml", "repos_v04_custom_members.yaml")
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "repos", "source")); err != nil {
		t.Fatalf("create escaping symlink: %v", err)
	}
	analysis := Analyze(root)
	if analysis.Report.OK {
		t.Fatal("symlink escape unexpectedly validated")
	}
	assertDiagnostic(t, analysis.Report.Diagnostics, "repos.member_path_symlink_escape")
}

func TestInvariantBRolesAreDescriptiveWithoutPrimaryCardinality(t *testing.T) {
	root := repositoryContractFixture(t, "project_v04.yaml", "repos_v04_multiple_primary.yaml")
	analysis := Analyze(root)
	assertRepositoryAnalysisOK(t, analysis)
	if len(analysis.Report.RepositoryMembers) != 2 {
		t.Fatalf("multiple primary members were not accepted: %#v", analysis.Report.RepositoryMembers)
	}
	if !RepositoryRoleOwns(RepositoryRolePrimary) || !RepositoryRoleOwns(RepositoryRoleComponent) || RepositoryRoleOwns(RepositoryRoleReference) {
		t.Fatal("owner/reference role semantics are incorrect")
	}

	empty := `kind: loom.repos
schema_version: repos.contract.v0.4
repos:
  status: active
  watch_roots: []
  members: []
`
	writeFile(t, root, ".loom/contracts/repos.yaml", empty, 0o600)
	emptyAnalysis := Analyze(root)
	assertRepositoryAnalysisOK(t, emptyAnalysis)
	if len(emptyAnalysis.Report.RepositoryMembers) != 0 {
		t.Fatalf("zero-member project gained members: %#v", emptyAnalysis.Report.RepositoryMembers)
	}
}

func TestInvariantBReferenceCannotGenerateOwningIdentity(t *testing.T) {
	parent := t.TempDir()
	_, err := ScaffoldProject(ScaffoldOptions{
		Name: "Reference Contract", Slug: "reference-contract", OwnerNode: "main", Preset: PresetMinimal,
		Facets: []string{"repos"}, Directory: parent,
		RepositoryMembers: []RepoMemberSpec{{Key: "shared", Path: "shared", Role: RepositoryRoleReference}},
	})
	if err == nil || !strings.Contains(err.Error(), "must supply an existing repository ID") {
		t.Fatalf("reference without an existing ID was not rejected: %v", err)
	}
	assertNoFile(t, parent, "reference-contract")
}

type repoPolicyProjection struct {
	Key, Path, ProjectPath, DisplayName, Status string
	Sync, Backup, Index                         bool
	Include, Exclude                            []string
}

type watchedPolicyProjection struct {
	Key, SafeRoot, RelativePath, Sync, Backup, Index string
	Include, Exclude                                 []string
}

type effectiveRepoPolicyProjection struct {
	Repos []repoPolicyProjection
	Roots []watchedPolicyProjection
}

func effectiveRepoPolicy(analysis Analysis) effectiveRepoPolicyProjection {
	out := effectiveRepoPolicyProjection{}
	for _, item := range analysis.Report.Repos {
		out.Repos = append(out.Repos, repoPolicyProjection{
			Key: item.Key, Path: item.Path, ProjectPath: item.ProjectPath, DisplayName: item.DisplayName,
			Status: item.Status, Sync: item.Sync, Backup: item.Backup, Index: item.Index,
			Include: append([]string{}, item.Include...), Exclude: append([]string{}, item.Exclude...),
		})
	}
	for _, item := range analysis.Report.WatchedRoots {
		if !containsString(item.SourceKinds, "repos_contract") {
			continue
		}
		out.Roots = append(out.Roots, watchedPolicyProjection{
			Key: item.BackendRootKey, SafeRoot: item.SafeRootKey, RelativePath: item.RootRelativePath,
			Sync: item.SyncMode, Backup: item.BackupMode, Index: item.IndexMode,
			Include: append([]string{}, item.Include...), Exclude: append([]string{}, item.Exclude...),
		})
	}
	return out
}

func assertEffectiveRepoPolicyEqual(t *testing.T, left, right Analysis) {
	t.Helper()
	leftPolicy, rightPolicy := effectiveRepoPolicy(left), effectiveRepoPolicy(right)
	if !reflect.DeepEqual(leftPolicy, rightPolicy) {
		t.Fatalf("effective watched-root policy changed:\n v0.3 %#v\n v0.4 %#v", leftPolicy, rightPolicy)
	}
}

func assertRepositoryAnalysisOK(t *testing.T, analysis Analysis) {
	t.Helper()
	if !analysis.Report.OK {
		t.Fatalf("repository contract analysis failed: %#v", analysis.Report.Diagnostics)
	}
}

func repositoryContractFixture(t *testing.T, projectFixtureName, reposFixtureName string) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, CanonicalRootContractPath, readFixture(t, projectFixtureName), 0o600)
	writeFile(t, root, ".loom/contracts/repos.yaml", readFixture(t, reposFixtureName), 0o600)
	mkdir(t, root, "repos")
	return root
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "repository_contracts", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(raw)
}
