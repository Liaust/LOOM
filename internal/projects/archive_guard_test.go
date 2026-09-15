package projects

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEnsureRuntimeActiveRejectsArchivedProject(t *testing.T) {
	store := newRuntimeGuardFakeStore(t)
	db := sql.OpenDB(runtimeGuardFakeConnector{store: store})
	defer db.Close()

	store.expect("FROM projects.projects p JOIN scopes.scopes", func(args []driver.NamedValue) {
		requireRuntimeGuardArgs(t, args, "project_archived")
	}, []driver.Value{"project_archived", "archived-project", "archived", "scope_archived", "active", []byte(`{}`)})

	err := EnsureRuntimeActive(context.Background(), db, RuntimeRef{
		ProjectID:    "project_archived",
		ResourceKind: "capability_provider",
		ResourceRef:  "scripts@main",
	})
	if !IsProjectRuntimeArchived(err) {
		t.Fatalf("error = %v, want project runtime archived", err)
	}
	if !strings.Contains(err.Error(), "restore or reactivate") || !strings.Contains(err.Error(), "archived-project") {
		t.Fatalf("archived error should be operator-readable, got %q", err.Error())
	}
	store.requireDone()
}

func TestEnsureRuntimeActiveRejectsArchivedProjectScope(t *testing.T) {
	store := newRuntimeGuardFakeStore(t)
	db := sql.OpenDB(runtimeGuardFakeConnector{store: store})
	defer db.Close()

	store.expect("FROM scopes.scopes s LEFT JOIN projects.projects", func(args []driver.NamedValue) {
		requireRuntimeGuardArgs(t, args, "scope_project")
	}, []driver.Value{"project_active", "active-project", "active", "scope_project", "archived", []byte(`{}`)})

	err := EnsureRuntimeActive(context.Background(), db, RuntimeRef{ScopeID: "scope_project"})
	if !errors.Is(err, ErrProjectRuntimeArchived) {
		t.Fatalf("error = %v, want ErrProjectRuntimeArchived", err)
	}
	store.requireDone()
}

func TestEnsureRuntimeActiveIgnoresNonProjectScope(t *testing.T) {
	store := newRuntimeGuardFakeStore(t)
	db := sql.OpenDB(runtimeGuardFakeConnector{store: store})
	defer db.Close()

	store.expect("FROM scopes.scopes s LEFT JOIN projects.projects", func(args []driver.NamedValue) {
		requireRuntimeGuardArgs(t, args, "scope_system")
	}, []driver.Value{nil, nil, "", "scope_system", "archived", []byte(`{}`)})

	if err := EnsureRuntimeActive(context.Background(), db, RuntimeRef{ScopeID: "scope_system"}); err != nil {
		t.Fatalf("non-project scope should not be treated as archived project runtime: %v", err)
	}
	store.requireDone()
}

func TestEnsureRuntimeActiveAllowsActiveProjectScope(t *testing.T) {
	store := newRuntimeGuardFakeStore(t)
	db := sql.OpenDB(runtimeGuardFakeConnector{store: store})
	defer db.Close()

	store.expect("FROM scopes.scopes s LEFT JOIN projects.projects", func(args []driver.NamedValue) {
		requireRuntimeGuardArgs(t, args, "scope_active")
	}, []driver.Value{"project_active", "active-project", "active", "scope_active", "active", []byte(`{}`)})

	if err := EnsureRuntimeActive(context.Background(), db, RuntimeRef{ScopeID: "scope_active"}); err != nil {
		t.Fatalf("active project scope should be allowed: %v", err)
	}
	store.requireDone()
}

func TestEnsureRuntimeActiveSkipsEmptyRefs(t *testing.T) {
	store := newRuntimeGuardFakeStore(t)
	db := sql.OpenDB(runtimeGuardFakeConnector{store: store})
	defer db.Close()

	if err := EnsureRuntimeActive(context.Background(), db, RuntimeRef{}); err != nil {
		t.Fatalf("empty runtime ref should be allowed: %v", err)
	}
	store.requireDone()
}

func TestEnsureProjectMutableRejectsArchivedProject(t *testing.T) {
	err := EnsureProjectMutable(Project{
		ProjectID:      "project_archived",
		ProjectScopeID: "scope_archived",
		Slug:           "archived-project",
		Status:         "archived",
	}, "project_activation", "scripts")
	if !IsProjectRuntimeArchived(err) {
		t.Fatalf("error = %v, want archived project mutation error", err)
	}
	if !strings.Contains(err.Error(), "archived-project") || !strings.Contains(err.Error(), "project_activation:scripts") {
		t.Fatalf("archived mutation error should include project and resource, got %q", err.Error())
	}
}

func TestEnsureProjectMutableAllowsActiveProject(t *testing.T) {
	if err := EnsureProjectMutable(Project{ProjectID: "project_active", Slug: "active-project", Status: "active"}, "project_activation", "scripts"); err != nil {
		t.Fatalf("active project mutation should be allowed: %v", err)
	}
}

func TestEnsureProjectMutableRejectsPhysicalArchiveInProgress(t *testing.T) {
	started := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	state := ProjectPhysicalArchiveState{
		SchemaVersion:   ProjectPhysicalArchiveStateSchemaVersion,
		Status:          ProjectPhysicalArchiveStatusInProgress,
		Phase:           ProjectArchivePhaseDeactivationPending,
		MutationBlocked: true,
		ProjectID:       "project_active", ProjectSlug: "active-project",
		OperationID: "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		PlanDigest:  "sha256:" + strings.Repeat("a", 64), WorkspacePlanDigest: "sha256:" + strings.Repeat("b", 64),
		ActivePath: "/fixture/box/Projects/active-project", ArchivePath: "/fixture/storage/archive/projects/active-project/project",
		ActorID: "actor_test", Reason: "archive project", StartedAt: started,
	}
	payload, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	err = EnsureProjectMutable(Project{ProjectID: "project_active", Slug: "active-project", Status: "active", ArchiveState: payload}, "project_activation", "scripts")
	if !IsProjectArchiveInProgress(err) || !strings.Contains(err.Error(), state.OperationID) || !strings.Contains(err.Error(), string(state.Phase)) {
		t.Fatalf("in-progress archive error = %v", err)
	}
}

func TestEnsureProjectMutableFailsClosedOnInvalidArchiveState(t *testing.T) {
	err := EnsureProjectMutable(Project{
		ProjectID: "project_active", Slug: "active-project", Status: "active",
		ArchiveState: json.RawMessage(`{"schema_version":"unknown","status":"in_progress"}`),
	}, "project_activation", "scripts")
	if !IsProjectArchiveInProgress(err) || !strings.Contains(err.Error(), "phase=invalid_state") {
		t.Fatalf("invalid archive state guard error = %v", err)
	}
}

func TestEnsureRuntimeActiveRejectsPhysicalArchiveInProgress(t *testing.T) {
	store := newRuntimeGuardFakeStore(t)
	db := sql.OpenDB(runtimeGuardFakeConnector{store: store})
	defer db.Close()
	started := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	state := ProjectPhysicalArchiveState{
		SchemaVersion:   ProjectPhysicalArchiveStateSchemaVersion,
		Status:          ProjectPhysicalArchiveStatusInProgress,
		Phase:           ProjectArchivePhaseDeactivationPending,
		MutationBlocked: true,
		ProjectID:       "project_active", ProjectSlug: "active-project",
		OperationID: "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		PlanDigest:  "sha256:" + strings.Repeat("a", 64), WorkspacePlanDigest: "sha256:" + strings.Repeat("b", 64),
		ActivePath: "/fixture/box/Projects/active-project", ArchivePath: "/fixture/storage/archive/projects/active-project/project",
		ActorID: "actor_test", Reason: "archive project", StartedAt: started,
	}
	payload, _ := json.Marshal(state)
	store.expect("FROM projects.projects p JOIN scopes.scopes", func(args []driver.NamedValue) {
		requireRuntimeGuardArgs(t, args, "project_active")
	}, []driver.Value{"project_active", "active-project", "active", "scope_active", "active", payload})
	err := EnsureRuntimeActive(context.Background(), db, RuntimeRef{ProjectID: "project_active", ResourceKind: "schedule", ResourceRef: "daily"})
	if !IsProjectArchiveInProgress(err) {
		t.Fatalf("error = %v, want in-progress archive block", err)
	}
	store.requireDone()
}

func TestEnsureRuntimeActiveFailsClosedOnInvalidArchiveState(t *testing.T) {
	store := newRuntimeGuardFakeStore(t)
	db := sql.OpenDB(runtimeGuardFakeConnector{store: store})
	defer db.Close()

	store.expect("FROM projects.projects p JOIN scopes.scopes", func(args []driver.NamedValue) {
		requireRuntimeGuardArgs(t, args, "project_active")
	}, []driver.Value{"project_active", "active-project", "active", "scope_active", "active", []byte(`{"schema_version":"unknown","status":"in_progress"}`)})

	err := EnsureRuntimeActive(context.Background(), db, RuntimeRef{
		ProjectRef: "project_active", ResourceKind: "script", ResourceRef: "hello_world",
	})
	if !IsProjectArchiveInProgress(err) || !strings.Contains(err.Error(), "phase=invalid_state") {
		t.Fatalf("invalid archive state runtime guard error = %v", err)
	}
	store.requireDone()
}

func TestBuildProjectArchiveCustodyBindingCanonicalRepositoryMatrix(t *testing.T) {
	for _, members := range []int{0, 1, 3} {
		t.Run(fmt.Sprintf("members-%d", members), func(t *testing.T) {
			detail, repository, canonicalRoot := projectArchiveCustodyFixture(t, "main", false, members)
			binding, err := BuildProjectArchiveCustodyBinding(detail, repository, canonicalRoot)
			if err != nil {
				t.Fatal(err)
			}
			if binding.CustodyKind != ProjectArchiveCanonicalCustody || binding.ProjectID != detail.Project.Project.ProjectID || binding.RegistrationRevision != 7 || binding.RepositoryMemberCount != members || binding.RepositorySourceRevision != 4 {
				t.Fatalf("binding = %#v", binding)
			}
		})
	}
}

func TestBuildProjectArchiveCustodyBindingUsesRegisteredContractLifecycle(t *testing.T) {
	for _, status := range []string{"draft", "", "active", "paused", "archived"} {
		t.Run(status, func(t *testing.T) {
			detail, repository, root := projectArchiveCustodyFixture(t, "main", false, 0)
			raw := []byte(strings.Replace(string(detail.Registration.Contract), `"status":"active"`, `"status":"`+status+`"`, 1))
			detail.Registration.Contract = raw
			detail.Registration.ContractHash = projectArchiveGuardTestDigest(raw)
			if err := os.WriteFile(detail.Registration.ContractPath, raw, 0640); err != nil {
				t.Fatal(err)
			}
			_, err := BuildProjectArchiveCustodyBinding(detail, repository, root)
			if projectStatusFromContract(status) == "active" {
				if err != nil {
					t.Fatalf("registered %q contract should remain archivable: %v", status, err)
				}
			} else if !errors.Is(err, ErrProjectArchiveInvalidBinding) {
				t.Fatalf("contradictory lifecycle accepted: %v", err)
			}
			after, readErr := os.ReadFile(detail.Registration.ContractPath)
			if readErr != nil || string(after) != string(raw) {
				t.Fatal("custody validation changed the contract")
			}
		})
	}
}

func TestBuildProjectArchiveCustodyBindingRejectsUnsupportedCustodyTyped(t *testing.T) {
	tests := []struct {
		name      string
		ownerNode string
		external  bool
	}{
		{name: "Mac-owned", ownerNode: "macbook"},
		{name: "external Main", ownerNode: "main", external: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detail, repository, canonicalRoot := projectArchiveCustodyFixture(t, test.ownerNode, test.external, 1)
			_, err := BuildProjectArchiveCustodyBinding(detail, repository, canonicalRoot)
			if !IsProjectArchiveUnsupportedCustody(err) {
				t.Fatalf("error = %v, want unsupported_custody", err)
			}
			var custodyErr ProjectArchiveUnsupportedCustodyError
			if !errors.As(err, &custodyErr) || custodyErr.ProjectSlug != "archive-fixture" {
				t.Fatalf("typed custody error = %#v", err)
			}
		})
	}
}

func TestBuildProjectArchiveCustodyBindingRejectsContractAndRepositorySubstitution(t *testing.T) {
	detail, repository, canonicalRoot := projectArchiveCustodyFixture(t, "main", false, 1)
	detail.Registration.Contract = json.RawMessage(strings.Replace(string(detail.Registration.Contract), `"id":"project_archive_fixture"`, `"id":"project_substituted"`, 1))
	if _, err := BuildProjectArchiveCustodyBinding(detail, repository, canonicalRoot); !errors.Is(err, ErrProjectArchiveInvalidBinding) {
		t.Fatalf("contract substitution error = %v", err)
	}

	detail, repository, canonicalRoot = projectArchiveCustodyFixture(t, "main", false, 1)
	repository.Source.ProjectContractRegistrationID = "project_contract_registration_substituted"
	if _, err := BuildProjectArchiveCustodyBinding(detail, repository, canonicalRoot); !errors.Is(err, ErrProjectArchiveInvalidBinding) {
		t.Fatalf("repository substitution error = %v", err)
	}
}

func TestBuildProjectArchiveCustodyBindingAuthenticatesLiveContractNoFollow(t *testing.T) {
	t.Run("stale bytes", func(t *testing.T) {
		detail, repository, canonicalRoot := projectArchiveCustodyFixture(t, "main", false, 1)
		if err := os.WriteFile(detail.Registration.ContractPath, []byte(`{"stale":true}`), 0o640); err != nil {
			t.Fatal(err)
		}
		if _, err := BuildProjectArchiveCustodyBinding(detail, repository, canonicalRoot); !errors.Is(err, ErrProjectArchiveInvalidBinding) || !strings.Contains(err.Error(), "does not match registered contract_hash") {
			t.Fatalf("stale contract error = %v", err)
		}
	})

	t.Run("terminal symlink", func(t *testing.T) {
		detail, repository, canonicalRoot := projectArchiveCustodyFixture(t, "main", false, 1)
		target := filepath.Join(filepath.Dir(detail.Registration.ContractPath), "replacement.json")
		if err := os.WriteFile(target, detail.Registration.Contract, 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(detail.Registration.ContractPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Base(target), detail.Registration.ContractPath); err != nil {
			t.Fatal(err)
		}
		if _, err := BuildProjectArchiveCustodyBinding(detail, repository, canonicalRoot); !errors.Is(err, ErrProjectArchiveInvalidBinding) || !strings.Contains(err.Error(), "no-follow regular file") {
			t.Fatalf("symlink contract error = %v", err)
		}
	})

	t.Run("missing", func(t *testing.T) {
		detail, repository, canonicalRoot := projectArchiveCustodyFixture(t, "main", false, 1)
		if err := os.Remove(detail.Registration.ContractPath); err != nil {
			t.Fatal(err)
		}
		if _, err := BuildProjectArchiveCustodyBinding(detail, repository, canonicalRoot); !errors.Is(err, ErrProjectArchiveInvalidBinding) {
			t.Fatalf("missing contract error = %v", err)
		}
	})
}

func TestBuildProjectArchiveCustodyBindingAllowsAcceptedRootSymlinkAlias(t *testing.T) {
	detail, repository, canonicalRoot := projectArchiveCustodyFixture(t, "main", false, 1)
	alias := filepath.Join(t.TempDir(), "project-alias")
	if err := os.Symlink(canonicalRoot, alias); err != nil {
		t.Fatal(err)
	}
	detail.Registration.ProjectRoot = alias
	detail.Registration.ContractPath = filepath.Join(alias, ".loom", "project.json")
	repository.Source.ProjectRoot = alias
	binding, err := BuildProjectArchiveCustodyBinding(detail, repository, canonicalRoot)
	if err != nil {
		t.Fatal(err)
	}
	if binding.RegisteredRoot != alias || binding.ResolvedRoot == alias || binding.ContractRelativePath != ".loom/project.json" {
		t.Fatalf("root alias binding = %#v", binding)
	}
}

func projectArchiveCustodyFixture(t *testing.T, ownerNode string, external bool, members int) (ProjectRegistrationDetail, ProjectRepositoryReadModel, string) {
	t.Helper()
	base := t.TempDir()
	canonicalRoot := filepath.Join(base, "box", "Projects", "archive-fixture")
	registeredRoot := canonicalRoot
	if external {
		registeredRoot = filepath.Join(base, "external", "archive-fixture")
	}
	if err := os.MkdirAll(filepath.Join(canonicalRoot, ".loom"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(registeredRoot, ".loom"), 0o750); err != nil {
		t.Fatal(err)
	}
	projectID := "project_archive_fixture"
	registrationID := "project_contract_registration_archive_fixture"
	contract := json.RawMessage(`{"kind":"loom.project","schema_version":"project.contract.v0.4","project":{"id":"project_archive_fixture","slug":"archive-fixture","owner_node":"` + ownerNode + `","status":"active"}}`)
	contractPath := filepath.Join(registeredRoot, ".loom", "project.json")
	if err := os.WriteFile(contractPath, contract, 0o640); err != nil {
		t.Fatal(err)
	}
	detail := ProjectRegistrationDetail{
		Project: ProjectDetail{Project: Project{
			ProjectID: projectID, ProjectScopeID: "scope_archive_fixture", ProjectScopeKey: "project:archive-fixture",
			Slug: "archive-fixture", Name: "Archive Fixture", Status: "active", UpdatedAt: time.Date(2026, 9, 4, 9, 0, 0, 0, time.UTC),
		}},
		Registration: &ProjectContractRegistration{
			ProjectContractRegistrationID: registrationID, ProjectID: projectID, ProjectRoot: registeredRoot,
			ContractPath: contractPath, ContractHash: projectArchiveGuardTestDigest(contract), ContractSchemaVersion: "project.contract.v0.4", Contract: contract,
			RegistrationStatus: ProjectRegistrationStatusRegistered, ActivationStatus: ProjectActivationStatusBaseActive, RegistrationRevision: 7,
		},
	}
	repository := ProjectRepositoryReadModel{
		Project: ProjectRepositoryReadProject{
			ProjectID: projectID, ProjectScopeID: detail.Project.Project.ProjectScopeID, ProjectScopeKey: detail.Project.Project.ProjectScopeKey,
			Slug: detail.Project.Project.Slug, Name: detail.Project.Project.Name, Status: "active", UpdatedAt: detail.Project.Project.UpdatedAt,
		},
		Source: &ProjectRepositoryReadSource{
			ProjectContractRegistrationID: registrationID, ProjectContractSchemaVersion: "project.contract.v0.4", ReposContractSchemaVersion: "repos.contract.v0.4",
			ProjectRoot: registeredRoot, OwnerNode: ownerNode, SemanticDigest: "sha256:" + strings.Repeat("b", 64), LocationDigest: "sha256:" + strings.Repeat("c", 64), SourceRevision: 4,
		},
	}
	for index := 0; index < members; index++ {
		repository.Members = append(repository.Members, ProjectRepositoryReadMember{
			RepositoryID: fmt.Sprintf("repository_%d", index), RepositoryOwnerProjectID: projectID, Key: fmt.Sprintf("repo-%d", index),
			Role: ProjectRepositoryRoleComponent, MembershipLifecycle: RepositoryLifecycleActive, RepositoryLifecycle: RepositoryLifecycleActive,
			SourceBindingDigest: "sha256:" + strings.Repeat("d", 64), ObservationRevision: int64(index + 1),
		})
	}
	return detail, repository, canonicalRoot
}

func projectArchiveGuardTestDigest(payload []byte) string {
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:])
}

type runtimeGuardFakeQuery struct {
	snippet string
	check   func([]driver.NamedValue)
	row     []driver.Value
}

type runtimeGuardFakeStore struct {
	t       *testing.T
	mu      sync.Mutex
	queries []runtimeGuardFakeQuery
}

func newRuntimeGuardFakeStore(t *testing.T) *runtimeGuardFakeStore {
	t.Helper()
	return &runtimeGuardFakeStore{t: t}
}

func (s *runtimeGuardFakeStore) expect(snippet string, check func([]driver.NamedValue), row []driver.Value) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, runtimeGuardFakeQuery{snippet: compactRuntimeGuardSQL(snippet), check: check, row: row})
}

func (s *runtimeGuardFakeStore) query(query string, args []driver.NamedValue) (driver.Rows, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queries) == 0 {
		s.t.Fatalf("unexpected query: %s", query)
	}
	expected := s.queries[0]
	s.queries = s.queries[1:]
	if !strings.Contains(compactRuntimeGuardSQL(query), expected.snippet) {
		s.t.Fatalf("query = %q, want to contain %q", compactRuntimeGuardSQL(query), expected.snippet)
	}
	if expected.check != nil {
		expected.check(args)
	}
	return &runtimeGuardFakeRows{columns: runtimeGuardFakeColumns(len(expected.row)), row: expected.row}, nil
}

func (s *runtimeGuardFakeStore) requireDone() {
	s.t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queries) != 0 {
		s.t.Fatalf("unconsumed queries: %d", len(s.queries))
	}
}

type runtimeGuardFakeConnector struct {
	store *runtimeGuardFakeStore
}

func (c runtimeGuardFakeConnector) Connect(context.Context) (driver.Conn, error) {
	return runtimeGuardFakeConn{store: c.store}, nil
}

func (c runtimeGuardFakeConnector) Driver() driver.Driver {
	return runtimeGuardFakeDriver{}
}

type runtimeGuardFakeDriver struct{}

func (runtimeGuardFakeDriver) Open(string) (driver.Conn, error) {
	return nil, fmt.Errorf("use sql.OpenDB with runtimeGuardFakeConnector")
}

type runtimeGuardFakeConn struct {
	store *runtimeGuardFakeStore
}

func (c runtimeGuardFakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("prepared statements are not supported by runtimeGuardFakeConn")
}

func (c runtimeGuardFakeConn) Close() error {
	return nil
}

func (c runtimeGuardFakeConn) Begin() (driver.Tx, error) {
	return nil, fmt.Errorf("transactions are not supported by runtimeGuardFakeConn")
}

func (c runtimeGuardFakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.store.query(query, args)
}

type runtimeGuardFakeRows struct {
	columns []string
	row     []driver.Value
	sent    bool
}

func (r *runtimeGuardFakeRows) Columns() []string {
	return r.columns
}

func (r *runtimeGuardFakeRows) Close() error {
	return nil
}

func (r *runtimeGuardFakeRows) Next(dest []driver.Value) error {
	if r.sent || r.row == nil {
		return io.EOF
	}
	copy(dest, r.row)
	r.sent = true
	return nil
}

func runtimeGuardFakeColumns(n int) []string {
	columns := make([]string, n)
	for i := range columns {
		columns[i] = fmt.Sprintf("c%d", i)
	}
	return columns
}

func compactRuntimeGuardSQL(query string) string {
	return strings.Join(strings.Fields(query), " ")
}

func requireRuntimeGuardArgs(t *testing.T, args []driver.NamedValue, want ...any) {
	t.Helper()
	if len(args) != len(want) {
		t.Fatalf("arg count = %d, want %d", len(args), len(want))
	}
	for i := range want {
		if fmt.Sprint(args[i].Value) != fmt.Sprint(want[i]) {
			t.Fatalf("arg %d = %v, want %v", i+1, args[i].Value, want[i])
		}
	}
}

func TestDeclarationArchiveRepositoryBindingVersions(t *testing.T) {
	for _, projectVersion := range []string{ProjectRepositoryProjectSchemaV03, ProjectRepositoryProjectSchemaV04, ProjectRepositoryProjectSchemaV05} {
		for _, repoVersion := range []string{ProjectRepositoryReposSchemaV03, ProjectRepositoryReposSchemaV04, ProjectRepositoryProjectSchemaV05, "repos.contract.v0.5", "unknown"} {
			t.Run(projectVersion+"_"+repoVersion, func(t *testing.T) {
				detail, repository, root := projectArchiveCustodyFixture(t, "main", false, 0)
				detail.Registration.ContractSchemaVersion = projectVersion
				repository.Source.ProjectContractSchemaVersion = projectVersion
				repository.Source.ReposContractSchemaVersion = repoVersion
				if projectVersion == ProjectRepositoryProjectSchemaV05 {
					detail.Registration.ContractPath = filepath.Join(root, ".loom", "project.yaml")
				}
				want := isSupportedProjectRepositorySourceVersions(ProjectRepositorySourceVersions{ProjectContract: projectVersion, ReposContract: repoVersion})
				canonical, resolveErr := filepath.EvalSymlinks(root)
				if resolveErr != nil {
					t.Fatal(resolveErr)
				}
				err := validateProjectArchiveRepositoryBinding(detail.Project.Project, *detail.Registration, repository, canonical)
				if (err == nil) != want {
					t.Fatalf("version pair valid=%v error=%v", want, err)
				}
			})
		}
	}
}
func TestDeclarationArchiveRepositorySingleSource(t *testing.T) {
	for _, count := range []int{0, 1, 3} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			detail, repository, root := projectArchiveCustodyFixture(t, "main", false, count)
			raw := json.RawMessage(strings.Replace(string(detail.Registration.Contract), ProjectRepositoryProjectSchemaV04, ProjectRepositoryProjectSchemaV05, 1))
			detail.Registration.ContractSchemaVersion = ProjectRepositoryProjectSchemaV05
			detail.Registration.ContractPath = filepath.Join(root, ".loom", "project.yaml")
			detail.Registration.Contract = raw
			detail.Registration.ContractHash = projectArchiveGuardTestDigest(raw)
			if err := os.WriteFile(detail.Registration.ContractPath, raw, 0600); err != nil {
				t.Fatal(err)
			}
			source := repository.Source
			source.ProjectContractSchemaVersion = ProjectRepositoryProjectSchemaV05
			source.ReposContractSchemaVersion = ProjectRepositoryProjectSchemaV05
			if _, err := BuildProjectArchiveCustodyBinding(detail, repository, root); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"missing", "owner", "member"} {
				t.Run(name, func(t *testing.T) {
					changed := repository
					copied := *source
					changed.Source = &copied
					changed.Members = append([]ProjectRepositoryReadMember(nil), repository.Members...)
					switch name {
					case "missing":
						changed.Source = nil
					case "owner":
						changed.Source.OwnerNode = "workspace"
					case "member":
						changed.Members = append(changed.Members, ProjectRepositoryReadMember{})
					}
					if _, err := BuildProjectArchiveCustodyBinding(detail, changed, root); err == nil {
						t.Fatal("substituted archive source accepted")
					}
				})
			}
			encoded, err := json.Marshal(source)
			if err != nil || strings.Contains(string(encoded), detail.Registration.ContractPath) {
				t.Fatal("internal source path escaped into read DTO")
			}
		})
	}
}
