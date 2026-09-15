package projects_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/db"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

type repositoryMemberFixture struct {
	ID        string `json:"id"`
	Key       string `json:"key"`
	Path      string `json:"path"`
	Role      string `json:"role"`
	StateRoot string `json:"state_root,omitempty"`
}

type repositoryRegistrationFixture struct {
	ProjectID     string
	Slug          string
	ProjectRoot   string
	ProjectPath   string
	ReposPath     string
	ProjectSchema string
	ReposSchema   string
	Members       []repositoryMemberFixture
}

func TestProjectRepositoryRegistrationTransactionPostgres(t *testing.T) {
	sqlDB, req := projectRepositoryTransactionDatabase(t)
	service := projects.NewService(sqlDB)
	ctx := context.Background()

	emptyFixture := newRepositoryRegistrationFixture("empty", projects.ProjectRepositoryProjectSchemaV04, projects.ProjectRepositoryReposSchemaV04, nil)
	empty, err := service.RegisterProjectContract(ctx, req, repositoryRegistrationInput(t, emptyFixture))
	if err != nil {
		t.Fatalf("register explicit empty repository set: %v", err)
	}
	if !empty.Created || empty.RepositorySource == nil || empty.RepositorySource.MemberCount != 0 || empty.RepositorySource.SourceRevision != 1 {
		t.Fatalf("empty registration result = %#v", empty)
	}
	assertRowCount(t, sqlDB, `SELECT count(*) FROM projects.project_repository_memberships WHERE project_id = $1`, 0, emptyFixture.ProjectID)

	orderIDs := []string{newRepositoryID(), newRepositoryID()}
	sort.Strings(orderIDs)
	orderFixture := newRepositoryRegistrationFixture("id-order", projects.ProjectRepositoryProjectSchemaV04, projects.ProjectRepositoryReposSchemaV04, []repositoryMemberFixture{
		{ID: orderIDs[1], Key: "alpha", Path: "alpha", Role: string(projects.ProjectRepositoryRolePrimary)},
		{ID: orderIDs[0], Key: "zeta", Path: "zeta", Role: string(projects.ProjectRepositoryRoleComponent)},
	})
	orderInput := repositoryRegistrationInput(t, orderFixture)
	if _, err := service.RegisterProjectContract(ctx, req, orderInput); err != nil {
		t.Fatalf("register members whose key and identity orders differ: %v", err)
	}
	orderReplay, err := service.RegisterProjectContract(ctx, req, orderInput)
	if err != nil || !orderReplay.Unchanged {
		t.Fatalf("replay members whose key and identity orders differ: result=%#v err=%v", orderReplay, err)
	}

	ownedID := newRepositoryID()
	componentID := newRepositoryID()
	mainFixture := newRepositoryRegistrationFixture("main", projects.ProjectRepositoryProjectSchemaV04, projects.ProjectRepositoryReposSchemaV04, []repositoryMemberFixture{
		{ID: ownedID, Key: "backend", Path: "backend", Role: string(projects.ProjectRepositoryRolePrimary), StateRoot: ".repo"},
		{ID: componentID, Key: "portal", Path: "portal", Role: string(projects.ProjectRepositoryRoleComponent)},
	})
	mainInput := repositoryRegistrationInput(t, mainFixture)
	created, err := service.RegisterProjectContract(ctx, req, mainInput)
	if err != nil {
		t.Fatalf("register complete member set: %v", err)
	}
	if !created.Created || created.RepositorySource == nil || created.RepositorySource.Classification != projects.ProjectRepositorySourceClassificationFirstRegistration {
		t.Fatalf("first registration result = %#v", created)
	}
	registrationID := created.Detail.Registration.ProjectContractRegistrationID
	assertRowCount(t, sqlDB, `SELECT count(*) FROM projects.project_repository_memberships WHERE project_id = $1 AND lifecycle_status = 'active'`, 2, mainFixture.ProjectID)
	assertRowCount(t, sqlDB, `SELECT count(*) FROM projects.project_repository_observations WHERE project_id = $1`, 2, mainFixture.ProjectID)
	assertRowCount(t, sqlDB, `SELECT count(*) FROM projects.project_repository_source_history WHERE project_id = $1`, 1, mainFixture.ProjectID)

	historyBefore := rowCount(t, sqlDB, `SELECT count(*) FROM projects.project_repository_source_history WHERE project_id = $1`, mainFixture.ProjectID)
	eventsBefore := rowCount(t, sqlDB, `SELECT count(*) FROM events.events WHERE target_id = $1`, registrationID)
	var sourceRegisteredAt, contractRegisteredAt time.Time
	if err := sqlDB.QueryRow(`SELECT registered_at FROM projects.project_repository_sources WHERE project_id = $1`, mainFixture.ProjectID).Scan(&sourceRegisteredAt); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.QueryRow(`SELECT last_registered_at FROM projects.project_contract_registrations WHERE project_id = $1`, mainFixture.ProjectID).Scan(&contractRegisteredAt); err != nil {
		t.Fatal(err)
	}
	replay, err := service.RegisterProjectContract(ctx, req, mainInput)
	if err != nil {
		t.Fatalf("identical replay: %v", err)
	}
	if !replay.Unchanged || replay.Created || replay.Updated || replay.RepositorySource == nil || replay.RepositorySource.Classification != projects.ProjectRepositorySourceClassificationIdenticalReplay || len(replay.EventIDs) != 0 {
		t.Fatalf("identical replay result = %#v", replay)
	}
	assertRowCount(t, sqlDB, `SELECT count(*) FROM projects.project_repository_source_history WHERE project_id = $1`, historyBefore, mainFixture.ProjectID)
	assertRowCount(t, sqlDB, `SELECT count(*) FROM events.events WHERE target_id = $1`, eventsBefore, registrationID)
	var replaySourceAt, replayContractAt time.Time
	if err := sqlDB.QueryRow(`SELECT registered_at FROM projects.project_repository_sources WHERE project_id = $1`, mainFixture.ProjectID).Scan(&replaySourceAt); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.QueryRow(`SELECT last_registered_at FROM projects.project_contract_registrations WHERE project_id = $1`, mainFixture.ProjectID).Scan(&replayContractAt); err != nil {
		t.Fatal(err)
	}
	if !replaySourceAt.Equal(sourceRegisteredAt) || !replayContractAt.Equal(contractRegisteredAt) {
		t.Fatalf("identical replay advanced timestamps: source %s -> %s contract %s -> %s", sourceRegisteredAt, replaySourceAt, contractRegisteredAt, replayContractAt)
	}

	observedAt := time.Date(2026, 8, 29, 12, 30, 0, 0, time.UTC)
	if _, err := sqlDB.Exec(`
		UPDATE projects.project_repository_observations
		SET observation_posture = 'observed', reason_code = '', observed_at = $3,
		    observation_json = '{"head":"abc123","dirty":false}'::jsonb,
		    observation_revision = 7, updated_at = $3
		WHERE project_id = $1 AND repository_id = $2
	`, mainFixture.ProjectID, ownedID, observedAt); err != nil {
		t.Fatal(err)
	}
	changedFixture := mainFixture
	changedFixture.Members = append([]repositoryMemberFixture(nil), mainFixture.Members...)
	changedFixture.Members[1].Path = "portal-moved"
	changed, err := service.RegisterProjectContract(ctx, req, repositoryRegistrationInput(t, changedFixture))
	if err != nil {
		t.Fatalf("register changed member binding: %v", err)
	}
	if changed.RepositorySource == nil || changed.RepositorySource.Classification != projects.ProjectRepositorySourceClassificationSemanticChange || changed.RepositorySource.SourceRevision != 2 {
		t.Fatalf("changed source result = %#v", changed.RepositorySource)
	}
	assertObservation(t, sqlDB, mainFixture.ProjectID, ownedID, "observed", "", 7, true, `{"dirty": false, "head": "abc123"}`)
	assertObservation(t, sqlDB, mainFixture.ProjectID, componentID, "not_observed", "source_binding_changed", 2, false, `{}`)

	relocatedFixture := changedFixture
	relocatedFixture.ProjectRoot += "-relocated"
	relocatedFixture.ProjectPath = filepath.Join(relocatedFixture.ProjectRoot, ".loom", "project.yaml")
	relocatedFixture.ReposPath = filepath.Join(relocatedFixture.ProjectRoot, "repos", "repos.yaml")
	relocated, err := service.RegisterProjectContract(ctx, req, repositoryRegistrationInput(t, relocatedFixture))
	if err != nil {
		t.Fatalf("register source relocation: %v", err)
	}
	if relocated.RepositorySource == nil || relocated.RepositorySource.Classification != projects.ProjectRepositorySourceClassificationRelocation || relocated.RepositorySource.SourceRevision != 3 {
		t.Fatalf("relocation result = %#v", relocated.RepositorySource)
	}
	assertObservation(t, sqlDB, mainFixture.ProjectID, ownedID, "not_observed", "source_binding_changed", 8, false, `{}`)
	assertObservation(t, sqlDB, mainFixture.ProjectID, componentID, "not_observed", "source_binding_changed", 3, false, `{}`)
	assertLatestSourceChange(t, sqlDB, mainFixture.ProjectID, "source_relocation")

	referenceFixture := newRepositoryRegistrationFixture("reference", projects.ProjectRepositoryProjectSchemaV04, projects.ProjectRepositoryReposSchemaV04, []repositoryMemberFixture{{ID: ownedID, Key: "shared", Path: "shared", Role: string(projects.ProjectRepositoryRoleReference)}})
	if _, err := service.RegisterProjectContract(ctx, req, repositoryRegistrationInput(t, referenceFixture)); err != nil {
		t.Fatalf("register non-owning reference: %v", err)
	}
	assertRepositoryOwner(t, sqlDB, ownedID, mainFixture.ProjectID)
	assertMembershipOwnerAndRole(t, sqlDB, referenceFixture.ProjectID, ownedID, mainFixture.ProjectID, "reference")

	conflictFixture := newRepositoryRegistrationFixture("conflict", projects.ProjectRepositoryProjectSchemaV04, projects.ProjectRepositoryReposSchemaV04, []repositoryMemberFixture{{ID: ownedID, Key: "stolen", Path: "stolen", Role: string(projects.ProjectRepositoryRolePrimary)}})
	if _, err := service.RegisterProjectContract(ctx, req, repositoryRegistrationInput(t, conflictFixture)); !errors.Is(err, projects.ErrProjectRepositoryOwnershipConflict) {
		t.Fatalf("cross-project owning conflict error = %v", err)
	}
	assertRowCount(t, sqlDB, `SELECT count(*) FROM projects.projects WHERE project_id = $1 OR slug = $2`, 0, conflictFixture.ProjectID, conflictFixture.Slug)
	assertRowCount(t, sqlDB, `SELECT count(*) FROM scopes.scopes WHERE scope_key = $1`, 0, "project:"+conflictFixture.Slug)

	testProjectRepositoryVersionTransitions(t, sqlDB, service, req)
	testProjectRepositoryValidationAndLateRollback(t, sqlDB, service, req)

	archiveHistory := rowCount(t, sqlDB, `SELECT count(*) FROM projects.project_repository_source_history WHERE project_id = $1`, mainFixture.ProjectID)
	if _, err := service.UpdateProjectArchiveState(ctx, req, mainFixture.ProjectID, json.RawMessage(`{"reason":"slice-2c-test"}`)); err != nil {
		t.Fatalf("archive repository project: %v", err)
	}
	assertRowCount(t, sqlDB, `SELECT count(*) FROM projects.project_repository_memberships WHERE project_id = $1 AND lifecycle_status = 'archived'`, 2, mainFixture.ProjectID)
	assertRepositoryLifecycle(t, sqlDB, ownedID, "archived")
	assertRepositoryLifecycle(t, sqlDB, componentID, "archived")
	assertMembershipOwnerAndRole(t, sqlDB, referenceFixture.ProjectID, ownedID, mainFixture.ProjectID, "reference")
	assertRowCount(t, sqlDB, `SELECT count(*) FROM projects.project_repository_source_history WHERE project_id = $1`, archiveHistory, mainFixture.ProjectID)
	assertRowCount(t, sqlDB, `SELECT count(*) FROM events.events WHERE event_type = 'project.archived' AND target_id = $1`, 1, mainFixture.ProjectID)
	if _, err := service.RegisterProjectContract(ctx, req, repositoryRegistrationInput(t, relocatedFixture)); err == nil || !(projects.IsProjectRuntimeArchived(err) || projects.IsProjectArchiveInProgress(err) && strings.Contains(err.Error(), "phase=invalid_state")) {
		t.Fatalf("archived repository mutation error = %v", err)
	}
	assertRowCount(t, sqlDB, `SELECT count(*) FROM projects.project_repository_source_history WHERE project_id = $1`, archiveHistory, mainFixture.ProjectID)
}

func TestProjectRepositoryRegistrationConcurrentOwnershipAndReplayPostgres(t *testing.T) {
	sqlDB, req := projectRepositoryTransactionDatabase(t)
	service := projects.NewService(sqlDB)
	sharedRepositoryID := newRepositoryID()
	fixtures := []repositoryRegistrationFixture{
		newRepositoryRegistrationFixture("race-a", projects.ProjectRepositoryProjectSchemaV04, projects.ProjectRepositoryReposSchemaV04, []repositoryMemberFixture{{ID: sharedRepositoryID, Key: "owned", Path: "owned", Role: string(projects.ProjectRepositoryRolePrimary)}}),
		newRepositoryRegistrationFixture("race-b", projects.ProjectRepositoryProjectSchemaV04, projects.ProjectRepositoryReposSchemaV04, []repositoryMemberFixture{{ID: sharedRepositoryID, Key: "owned", Path: "owned", Role: string(projects.ProjectRepositoryRolePrimary)}}),
	}
	type outcome struct {
		index  int
		result projects.RegisterProjectContractResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, len(fixtures))
	var wait sync.WaitGroup
	for index, fixture := range fixtures {
		index, input := index, repositoryRegistrationInput(t, fixture)
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			result, err := service.RegisterProjectContract(context.Background(), req, input)
			outcomes <- outcome{index: index, result: result, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(outcomes)
	successes := 0
	conflicts := 0
	winner := -1
	for outcome := range outcomes {
		if outcome.err == nil {
			successes++
			winner = outcome.index
			continue
		}
		if !errors.Is(outcome.err, projects.ErrProjectRepositoryOwnershipConflict) {
			t.Fatalf("concurrent ownership error = %v", outcome.err)
		}
		conflicts++
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent ownership outcomes = %d successes, %d conflicts", successes, conflicts)
	}
	assertRepositoryOwner(t, sqlDB, sharedRepositoryID, fixtures[winner].ProjectID)
	assertRowCount(t, sqlDB, `SELECT count(*) FROM projects.projects WHERE project_id IN ($1, $2)`, 1, fixtures[0].ProjectID, fixtures[1].ProjectID)

	replayFixture := newRepositoryRegistrationFixture("race-replay", projects.ProjectRepositoryProjectSchemaV04, projects.ProjectRepositoryReposSchemaV04, []repositoryMemberFixture{{ID: newRepositoryID(), Key: "only", Path: "only", Role: string(projects.ProjectRepositoryRolePrimary)}})
	replayInput := repositoryRegistrationInput(t, replayFixture)
	start = make(chan struct{})
	outcomes = make(chan outcome, 2)
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			result, err := service.RegisterProjectContract(context.Background(), req, replayInput)
			outcomes <- outcome{index: index, result: result, err: err}
		}(index)
	}
	close(start)
	wait.Wait()
	close(outcomes)
	created, unchanged := 0, 0
	for outcome := range outcomes {
		if outcome.err != nil {
			t.Fatalf("concurrent identical replay: %v", outcome.err)
		}
		if outcome.result.Created {
			created++
		}
		if outcome.result.Unchanged {
			unchanged++
		}
	}
	if created != 1 || unchanged != 1 {
		t.Fatalf("concurrent replay outcomes = %d created, %d unchanged", created, unchanged)
	}
	assertRowCount(t, sqlDB, `SELECT count(*) FROM projects.project_repository_source_history WHERE project_id = $1`, 1, replayFixture.ProjectID)
}

func testProjectRepositoryVersionTransitions(t *testing.T, sqlDB *sql.DB, service projects.Service, req requestctx.Context) {
	t.Helper()
	legacy := newRepositoryRegistrationFixture("upgrade", projects.ProjectRepositoryProjectSchemaV03, projects.ProjectRepositoryReposSchemaV03, nil)
	legacy.ProjectID = ""
	legacyInput := repositoryRegistrationInput(t, legacy)
	registered, err := service.RegisterProjectContract(context.Background(), req, legacyInput)
	if err != nil {
		t.Fatalf("register v0.3 compatibility source: %v", err)
	}
	projectID := registered.Detail.Project.Project.ProjectID
	if !strings.HasPrefix(projectID, ids.ProjectPrefix+"_") || registered.RepositorySource == nil || registered.RepositorySource.SourceRevision != 1 {
		t.Fatalf("v0.3 registration = %#v", registered)
	}
	assertRowCount(t, sqlDB, `SELECT count(*) FROM projects.project_repository_memberships WHERE project_id = $1`, 0, projectID)

	upgrade := legacy
	upgrade.ProjectID = projectID
	upgrade.ProjectSchema = projects.ProjectRepositoryProjectSchemaV04
	upgrade.ReposSchema = projects.ProjectRepositoryReposSchemaV04
	upgrade.Members = []repositoryMemberFixture{{ID: newRepositoryID(), Key: "upgraded", Path: "upgraded", Role: string(projects.ProjectRepositoryRolePrimary)}}
	upgraded, err := service.RegisterProjectContract(context.Background(), req, repositoryRegistrationInput(t, upgrade))
	if err != nil {
		t.Fatalf("register v0.3 to v0.4 upgrade: %v", err)
	}
	if upgraded.RepositorySource == nil || upgraded.RepositorySource.SourceRevision != 2 || upgraded.RepositorySource.Classification != projects.ProjectRepositorySourceClassificationSemanticChange {
		t.Fatalf("upgrade result = %#v", upgraded.RepositorySource)
	}

	downgrade := upgrade
	downgrade.ProjectID = ""
	downgrade.ProjectSchema = projects.ProjectRepositoryProjectSchemaV03
	downgrade.ReposSchema = projects.ProjectRepositoryReposSchemaV03
	downgrade.Members = nil
	if _, err := service.RegisterProjectContract(context.Background(), req, repositoryRegistrationInput(t, downgrade)); !errors.Is(err, projects.ErrUnsupportedProjectRepositorySourceVersionTransition) {
		t.Fatalf("downgrade error = %v", err)
	}
	assertRowCount(t, sqlDB, `SELECT count(*) FROM projects.project_repository_source_history WHERE project_id = $1`, 2, projectID)
	assertRowCount(t, sqlDB, `SELECT source_revision FROM projects.project_repository_sources WHERE project_id = $1`, 2, projectID)
}

func testProjectRepositoryValidationAndLateRollback(t *testing.T, sqlDB *sql.DB, service projects.Service, req requestctx.Context) {
	t.Helper()
	partialFixture := newRepositoryRegistrationFixture("partial", projects.ProjectRepositoryProjectSchemaV04, projects.ProjectRepositoryReposSchemaV04, []repositoryMemberFixture{
		{ID: newRepositoryID(), Key: "one", Path: "one", Role: string(projects.ProjectRepositoryRolePrimary)},
		{ID: newRepositoryID(), Key: "two", Path: "two", Role: string(projects.ProjectRepositoryRoleComponent)},
	})
	partialInput := repositoryRegistrationInput(t, partialFixture)
	var partialPlan map[string]any
	if err := json.Unmarshal(partialInput.RegistrationPlan, &partialPlan); err != nil {
		t.Fatal(err)
	}
	partialPlan["repository_members"] = partialPlan["repository_members"].([]any)[:1]
	partialInput.RegistrationPlan = marshalJSON(t, partialPlan)
	if _, err := service.RegisterProjectContract(context.Background(), req, partialInput); err == nil || !strings.Contains(err.Error(), "complete repository member set") {
		t.Fatalf("partial member validation error = %v", err)
	}
	assertRowCount(t, sqlDB, `SELECT count(*) FROM projects.projects WHERE project_id = $1`, 0, partialFixture.ProjectID)

	rollbackFixture := newRepositoryRegistrationFixture("late-rollback", projects.ProjectRepositoryProjectSchemaV04, projects.ProjectRepositoryReposSchemaV04, []repositoryMemberFixture{{ID: newRepositoryID(), Key: "only", Path: "only", Role: string(projects.ProjectRepositoryRolePrimary)}})
	withoutSource := repositoryRegistrationInput(t, rollbackFixture)
	withoutSource.RepositorySource = nil
	withoutSource.Facets = nil
	for _, target := range []*json.RawMessage{&withoutSource.ValidationReport, &withoutSource.RegistrationPlan} {
		var document map[string]any
		if err := json.Unmarshal(*target, &document); err != nil {
			t.Fatal(err)
		}
		delete(document, "repos")
		delete(document, "repository_members")
		delete(document, "repository_source")
		delete(document, "facets")
		*target = marshalJSON(t, document)
	}
	base, err := service.RegisterProjectContract(context.Background(), req, withoutSource)
	if err != nil {
		t.Fatalf("create rollback fixture project contract: %v", err)
	}
	registrationID := base.Detail.Registration.ProjectContractRegistrationID
	eventsBefore := rowCount(t, sqlDB, `SELECT count(*) FROM events.events WHERE target_id = $1`, registrationID)
	invalidRequest := req
	invalidRequest.OriginNodeID = "node_missing_repository_transaction"
	if _, err := service.RegisterProjectContract(context.Background(), invalidRequest, repositoryRegistrationInput(t, rollbackFixture)); err == nil {
		t.Fatal("late event foreign-key failure unexpectedly succeeded")
	}
	assertRowCount(t, sqlDB, `SELECT count(*) FROM projects.project_repository_sources WHERE project_id = $1`, 0, rollbackFixture.ProjectID)
	assertRowCount(t, sqlDB, `SELECT count(*) FROM projects.project_repository_memberships WHERE project_id = $1`, 0, rollbackFixture.ProjectID)
	assertRowCount(t, sqlDB, `SELECT count(*) FROM projects.project_repository_source_history WHERE project_id = $1`, 0, rollbackFixture.ProjectID)
	assertRowCount(t, sqlDB, `SELECT count(*) FROM events.events WHERE target_id = $1`, eventsBefore, registrationID)
	assertRowCount(t, sqlDB, `SELECT registration_revision FROM projects.project_contract_registrations WHERE project_id = $1`, 1, rollbackFixture.ProjectID)
}

func projectRepositoryTransactionDatabase(t *testing.T) (*sql.DB, requestctx.Context) {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("LOOM_TEST_DB_URL"))
	if databaseURL == "" {
		t.Skip("LOOM_TEST_DB_URL is not set; repository transaction tests require a dedicated disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := migrations.Up(ctx, databaseURL, projectRepositoryTransactionMigrationsDir(t)); err != nil {
		t.Fatalf("migrate disposable repository transaction database: %v", err)
	}
	sqlDB, err := db.OpenSQL(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	var databaseName string
	if err := sqlDB.QueryRowContext(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatal(err)
	}
	if databaseName == "postgres" || databaseName == "template0" || databaseName == "template1" {
		t.Fatalf("LOOM_TEST_DB_URL points to reserved database %q", databaseName)
	}
	if _, err := bootstrap.NewService(sqlDB).EnsureDevBootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	req, err := requestctx.ResolveBootstrap(ctx, sqlDB, "corr_project_repository_transaction")
	if err != nil {
		t.Fatal(err)
	}
	return sqlDB, req
}

func projectRepositoryTransactionMigrationsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "migrations")
}

func newRepositoryRegistrationFixture(prefix, projectSchema, reposSchema string, members []repositoryMemberFixture) repositoryRegistrationFixture {
	slug := repositoryTransactionSlug(prefix)
	root := filepath.Join(os.TempDir(), "loom-repository-transaction", slug)
	return repositoryRegistrationFixture{
		ProjectID:     ids.NewProjectID(),
		Slug:          slug,
		ProjectRoot:   root,
		ProjectPath:   filepath.Join(root, ".loom", "project.yaml"),
		ReposPath:     filepath.Join(root, "repos", "repos.yaml"),
		ProjectSchema: projectSchema,
		ReposSchema:   reposSchema,
		Members:       append([]repositoryMemberFixture(nil), members...),
	}
}

func repositoryRegistrationInput(t *testing.T, fixture repositoryRegistrationFixture) projects.RegisterProjectContractInput {
	t.Helper()
	project := projects.ProjectContractProjectInput{
		ID: fixture.ProjectID, Slug: fixture.Slug, Name: "Repository " + fixture.Slug, OwnerNode: "main", Status: "active",
	}
	if fixture.ProjectSchema == projects.ProjectRepositoryProjectSchemaV03 {
		project.ID = ""
	}
	contract := marshalJSON(t, map[string]any{"kind": "loom.project", "schema_version": fixture.ProjectSchema, "project": project})
	projectDigest := digestJSON(contract)
	reposContract := marshalJSON(t, map[string]any{"kind": "loom.repos", "schema_version": fixture.ReposSchema, "repos": map[string]any{"members": fixture.Members}})
	reposDigest := digestJSON(reposContract)
	repos := []map[string]any{{"contract_path": fixture.ReposPath, "contract_hash": reposDigest}}
	report := marshalJSON(t, map[string]any{
		"schema_version": "project.validation_report.v0.3", "project_root": fixture.ProjectRoot,
		"contract_path": fixture.ProjectPath, "ok": true, "registerable": true,
		"project": project, "repos": repos, "repository_members": fixture.Members,
		"repository_source": map[string]string{"contract_path": fixture.ReposPath, "contract_hash": reposDigest, "contract_schema_version": fixture.ReposSchema},
	})
	plan := marshalJSON(t, map[string]any{
		"schema_version": "project.plan.v0.3", "generated_at": "2026-08-29T12:00:00Z",
		"project_root": fixture.ProjectRoot, "contract_path": fixture.ProjectPath,
		"registerable": true, "project": project, "repos": repos, "repository_members": fixture.Members,
		"repository_source": map[string]string{"contract_path": fixture.ReposPath, "contract_hash": reposDigest, "contract_schema_version": fixture.ReposSchema},
	})
	return projects.RegisterProjectContractInput{
		ProjectRoot: fixture.ProjectRoot, ContractPath: fixture.ProjectPath,
		ContractHash: projectDigest, ContractSchemaVersion: fixture.ProjectSchema,
		Contract: contract, ValidationReport: report, RegistrationPlan: plan, Project: project,
		RepositorySource: &projects.RegisterProjectRepositorySourceInput{ContractPath: fixture.ReposPath, ContractHash: reposDigest, ContractSchemaVersion: fixture.ReposSchema},
		DerivedProviders: json.RawMessage(`[]`),
		Facets:           []projects.ProjectContractFacetInput{{Key: "repos", Folder: "repos", Enabled: true, Present: true}},
		PolicyRefs:       json.RawMessage(`[]`),
		Metadata:         json.RawMessage(`{"source":"slice-2c-postgres"}`),
	}
}

func newRepositoryID() string {
	return "repo_" + strings.TrimPrefix(ids.NewProjectID(), ids.ProjectPrefix+"_")
}

func repositoryTransactionSlug(prefix string) string {
	return prefix + "-" + strings.ToLower(strings.TrimPrefix(ids.NewProjectID(), ids.ProjectPrefix+"_"))
}

func digestJSON(raw json.RawMessage) string {
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func marshalJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func rowCount(t *testing.T, sqlDB *sql.DB, query string, args ...any) int {
	t.Helper()
	var value int
	if err := sqlDB.QueryRowContext(context.Background(), query, args...).Scan(&value); err != nil {
		t.Fatalf("query scalar %q: %v", query, err)
	}
	return value
}

func assertRowCount(t *testing.T, sqlDB *sql.DB, query string, want int, args ...any) {
	t.Helper()
	if got := rowCount(t, sqlDB, query, args...); got != want {
		t.Fatalf("query scalar %q = %d, want %d", query, got, want)
	}
}

func assertRepositoryOwner(t *testing.T, sqlDB *sql.DB, repositoryID, wantProjectID string) {
	t.Helper()
	var owner string
	if err := sqlDB.QueryRowContext(context.Background(), `SELECT owning_project_id FROM projects.repositories WHERE repository_id = $1`, repositoryID).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != wantProjectID {
		t.Fatalf("repository %s owner = %s, want %s", repositoryID, owner, wantProjectID)
	}
}

func assertRepositoryLifecycle(t *testing.T, sqlDB *sql.DB, repositoryID, want string) {
	t.Helper()
	var lifecycle string
	if err := sqlDB.QueryRowContext(context.Background(), `SELECT lifecycle_status FROM projects.repositories WHERE repository_id = $1`, repositoryID).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if lifecycle != want {
		t.Fatalf("repository %s lifecycle = %s, want %s", repositoryID, lifecycle, want)
	}
}

func assertMembershipOwnerAndRole(t *testing.T, sqlDB *sql.DB, projectID, repositoryID, wantOwner, wantRole string) {
	t.Helper()
	var owner, role string
	if err := sqlDB.QueryRowContext(context.Background(), `
		SELECT repository_owner_project_id, role
		FROM projects.project_repository_memberships
		WHERE project_id = $1 AND repository_id = $2
	`, projectID, repositoryID).Scan(&owner, &role); err != nil {
		t.Fatal(err)
	}
	if owner != wantOwner || role != wantRole {
		t.Fatalf("membership %s/%s owner/role = %s/%s, want %s/%s", projectID, repositoryID, owner, role, wantOwner, wantRole)
	}
}

func assertObservation(t *testing.T, sqlDB *sql.DB, projectID, repositoryID, wantPosture, wantReason string, wantRevision int64, wantObservedAt bool, wantPayload string) {
	t.Helper()
	var posture, reason string
	var revision int64
	var observedAt sql.NullTime
	var payload []byte
	if err := sqlDB.QueryRowContext(context.Background(), `
		SELECT observation_posture, reason_code, observation_revision, observed_at, observation_json
		FROM projects.project_repository_observations
		WHERE project_id = $1 AND repository_id = $2
	`, projectID, repositoryID).Scan(&posture, &reason, &revision, &observedAt, &payload); err != nil {
		t.Fatal(err)
	}
	if posture != wantPosture || reason != wantReason || revision != wantRevision || observedAt.Valid != wantObservedAt || !jsonEqual(payload, []byte(wantPayload)) {
		t.Fatalf("observation %s/%s = posture %s reason %s revision %d observed_at %t payload %s", projectID, repositoryID, posture, reason, revision, observedAt.Valid, payload)
	}
}

func assertLatestSourceChange(t *testing.T, sqlDB *sql.DB, projectID, want string) {
	t.Helper()
	var change string
	if err := sqlDB.QueryRowContext(context.Background(), `
		SELECT source_change_kind
		FROM projects.project_repository_source_history
		WHERE project_id = $1
		ORDER BY source_revision DESC
		LIMIT 1
	`, projectID).Scan(&change); err != nil {
		t.Fatal(err)
	}
	if change != want {
		t.Fatalf("latest source change = %s, want %s", change, want)
	}
}

func jsonEqual(left, right []byte) bool {
	var leftValue, rightValue any
	return json.Unmarshal(left, &leftValue) == nil && json.Unmarshal(right, &rightValue) == nil && reflect.DeepEqual(leftValue, rightValue)
}
