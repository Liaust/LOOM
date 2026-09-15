package projects_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

type restoreTransactionFixture struct {
	db                    *sql.DB
	service               projects.Service
	req                   requestctx.Context
	archive               projects.ProjectPhysicalArchiveTransitionInput
	states                []projects.ProjectPhysicalRestoreState
	sharedID, sharedOwner string
	history               int
}

func newRestoreTransactionFixture(t *testing.T) restoreTransactionFixture {
	t.Helper()
	database, req := projectRepositoryTransactionDatabase(t)
	service, ctx := projects.NewService(database), context.Background()
	sharedID := newRepositoryID()
	owner := newRepositoryRegistrationFixture("restore-owner", projects.ProjectRepositoryProjectSchemaV04, projects.ProjectRepositoryReposSchemaV04, []repositoryMemberFixture{{ID: sharedID, Key: "shared", Path: "shared", Role: "primary"}})
	if _, err := service.RegisterProjectContract(ctx, req, restoreRegistrationInput(t, owner)); err != nil {
		t.Fatal(err)
	}
	fixture := newRepositoryRegistrationFixture("restore-project", projects.ProjectRepositoryProjectSchemaV04, projects.ProjectRepositoryReposSchemaV04, []repositoryMemberFixture{
		{ID: newRepositoryID(), Key: "primary", Path: "primary", Role: "primary"},
		{ID: newRepositoryID(), Key: "component", Path: "component", Role: "component"},
		{ID: sharedID, Key: "reference", Path: "reference", Role: "reference"},
	})
	registered, err := service.RegisterProjectContract(ctx, req, restoreRegistrationInput(t, fixture))
	if err != nil {
		t.Fatal(err)
	}
	repository, err := service.ReadProjectRepositoryState(ctx, fixture.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	at := time.Now().UTC().Truncate(time.Microsecond)
	state := projects.ProjectPhysicalArchiveState{
		SchemaVersion: projects.ProjectPhysicalArchiveStateSchemaVersion, Status: "in_progress", Phase: projects.ProjectArchivePhaseDeactivationPending, MutationBlocked: true,
		ProjectID: fixture.ProjectID, ProjectSlug: fixture.Slug, OperationID: "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		PlanDigest: digest, WorkspacePlanDigest: digest, ActivePath: fixture.ProjectRoot, ArchivePath: fixture.ProjectRoot + "-archive", ActorID: req.ActorID, Reason: "disposable restore transaction fixture", StartedAt: at,
	}
	input := projects.ProjectPhysicalArchiveTransitionInput{State: state, ExpectedScopeID: registered.Detail.Project.Project.ProjectScopeID,
		ExpectedRegistrationID: registered.Detail.Registration.ProjectContractRegistrationID, ExpectedRegistrationRevision: registered.Detail.Registration.RegistrationRevision, ExpectedRepositorySourceRevision: repository.Source.SourceRevision}
	for _, member := range repository.Members {
		input.ExpectedMembers = append(input.ExpectedMembers, projects.ProjectArchiveRepositoryMemberBinding{RepositoryID: member.RepositoryID, RepositoryOwnerProjectID: member.RepositoryOwnerProjectID, MemberKey: member.Key, Role: member.Role})
	}
	for i, phase := range []projects.ProjectPhysicalArchivePhase{projects.ProjectArchivePhaseDeactivationPending, projects.ProjectArchivePhaseRuntimeDeactivated, projects.ProjectArchivePhaseProjectStatePending, projects.ProjectArchivePhaseComplete} {
		stamp := at.Add(time.Duration(i) * time.Second)
		input.State.Phase = phase
		switch phase {
		case projects.ProjectArchivePhaseRuntimeDeactivated:
			input.State.RuntimeDeactivatedAt = &stamp
			input.State.RuntimeQuiescenceDigest = digest
		case projects.ProjectArchivePhaseProjectStatePending:
			input.State.WorkspaceCompletedAt = &stamp
			input.State.ArchiveManifestDigest = digest
		case projects.ProjectArchivePhaseComplete:
			input.State.Status = "archived"
			input.State.ArchivedAt = &stamp
		}
		if _, err := service.TransitionProjectPhysicalArchive(ctx, req, input); err != nil {
			t.Fatal(err)
		}
	}
	start, moved, completed := at.Add(4*time.Second), at.Add(5*time.Second), at.Add(6*time.Second)
	initial := projects.ProjectPhysicalRestoreState{SchemaVersion: projects.ProjectPhysicalRestoreStateSchemaVersion, Request: req,
		OperationID: "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAZ", PlanDigest: digest, WorkspacePlanDigest: digest,
		Phase: projects.ProjectRestorePhasePending, Status: "in_progress", MutationBlocked: true, ActivationState: "inactive", StartedAt: start}
	pending := initial
	pending.Phase = projects.ProjectRestorePhaseProjectStatePending
	pending.WorkspaceCompletedAt = &moved
	pending.ActiveManifestDigest = digest
	terminal := pending
	terminal.Phase = projects.ProjectRestorePhaseComplete
	terminal.Status = "restored"
	terminal.RestoredAt = &completed
	return restoreTransactionFixture{db: database, service: service, req: req, archive: input, states: []projects.ProjectPhysicalRestoreState{initial, pending, terminal}, sharedID: sharedID, sharedOwner: owner.ProjectID,
		history: rowCount(t, database, `SELECT count(*) FROM projects.project_repository_source_history WHERE project_id=$1`, fixture.ProjectID)}
}

func (f restoreTransactionFixture) transition(state projects.ProjectPhysicalRestoreState) (projects.ProjectPhysicalRestoreTransitionResult, error) {
	return f.service.TransitionProjectPhysicalRestore(context.Background(), f.req, projects.ProjectPhysicalRestoreTransitionInput{Archive: f.archive, State: state})
}

func restoreRegistrationInput(t *testing.T, fixture repositoryRegistrationFixture) projects.RegisterProjectContractInput {
	t.Helper()
	input := repositoryRegistrationInput(t, fixture)
	// The older fixture predates the required report/plan source binding.
	for _, raw := range []*json.RawMessage{&input.ValidationReport, &input.RegistrationPlan} {
		var document map[string]json.RawMessage
		if err := json.Unmarshal(*raw, &document); err != nil {
			t.Fatal(err)
		}
		document["repository_source"] = marshalJSON(t, map[string]string{
			"contract_path":           input.RepositorySource.ContractPath,
			"contract_hash":           input.RepositorySource.ContractHash,
			"contract_schema_version": input.RepositorySource.ContractSchemaVersion,
		})
		*raw = marshalJSON(t, document)
	}
	return input
}

func (f restoreTransactionFixture) assertLifecycle(t *testing.T, lifecycle string, events int) {
	t.Helper()
	registration := "archived"
	if lifecycle == "active" {
		registration = "registered"
	}
	assertRowCount(t, f.db, `SELECT count(*) FROM projects.projects WHERE project_id=$1 AND status=$2`, 1, f.archive.State.ProjectID, lifecycle)
	assertRowCount(t, f.db, `SELECT count(*) FROM scopes.scopes WHERE scope_id=$1 AND status=$2`, 1, f.archive.ExpectedScopeID, lifecycle)
	assertRowCount(t, f.db, `SELECT count(*) FROM projects.project_contract_registrations WHERE project_id=$1 AND registration_status=$2 AND activation_status='inactive'`, 1, f.archive.State.ProjectID, registration)
	assertRowCount(t, f.db, `SELECT count(*) FROM projects.project_repository_memberships WHERE project_id=$1 AND lifecycle_status=$2`, 3, f.archive.State.ProjectID, lifecycle)
	assertRowCount(t, f.db, `SELECT count(*) FROM projects.repositories WHERE owning_project_id=$1 AND lifecycle_status=$2`, 2, f.archive.State.ProjectID, lifecycle)
	assertRowCount(t, f.db, `SELECT count(*) FROM projects.repositories WHERE repository_id=$1 AND owning_project_id=$2 AND lifecycle_status='active'`, 1, f.sharedID, f.sharedOwner)
	assertRowCount(t, f.db, `SELECT count(*) FROM projects.project_repository_source_history WHERE project_id=$1`, f.history, f.archive.State.ProjectID)
	assertRowCount(t, f.db, `SELECT count(*) FROM projects.project_repository_sources WHERE project_id=$1 AND source_revision=$2`, 1, f.archive.State.ProjectID, f.archive.ExpectedRepositorySourceRevision)
	assertRowCount(t, f.db, `SELECT count(*) FROM events.events WHERE target_id=$1 AND event_type='project.archived'`, 1, f.archive.State.ProjectID)
	assertRowCount(t, f.db, `SELECT count(*) FROM events.events WHERE target_id=$1 AND event_type='project.restored'`, events, f.archive.State.ProjectID)
}

func TestProjectPhysicalRestoreTransactionPostgres(t *testing.T) {
	f := newRestoreTransactionFixture(t)
	ctx := context.Background()
	if _, err := f.transition(f.states[2]); err == nil {
		t.Fatal("completion bypassed intent")
	}
	for _, state := range f.states[:2] {
		if _, err := f.transition(state); err != nil {
			t.Fatal(err)
		}
		f.assertLifecycle(t, "archived", 0)
	}
	// Fail the final project row after registration/member/scope/event changes.
	// The database transaction must undo every earlier write, not just the last.
	if _, err := f.db.ExecContext(ctx, `CREATE FUNCTION projects.restore_test_abort() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.status='active' AND NEW.project_id='`+f.archive.State.ProjectID+`' THEN RAISE EXCEPTION 'disposable restore completion fault'; END IF; RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecContext(ctx, `CREATE TRIGGER restore_test_abort BEFORE UPDATE ON projects.projects FOR EACH ROW EXECUTE FUNCTION projects.restore_test_abort()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.db.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS restore_test_abort ON projects.projects`)
		_, _ = f.db.ExecContext(context.Background(), `DROP FUNCTION IF EXISTS projects.restore_test_abort()`)
	})
	if _, err := f.transition(f.states[2]); err == nil || !strings.Contains(err.Error(), "disposable restore completion fault") {
		t.Fatalf("injected rollback: %v", err)
	}
	f.assertLifecycle(t, "archived", 0)
	if _, err := f.db.ExecContext(ctx, `DROP TRIGGER restore_test_abort ON projects.projects`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecContext(ctx, `DROP FUNCTION projects.restore_test_abort()`); err != nil {
		t.Fatal(err)
	}
	completed, err := f.transition(f.states[2])
	if err != nil {
		t.Fatal(err)
	}
	if completed.Replay || completed.State.EventID == "" {
		t.Fatal("missing actual restore event")
	}
	f.assertLifecycle(t, "active", 1)
	replayed, err := f.transition(completed.State)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replay || !reflect.DeepEqual(replayed.State, completed.State) {
		t.Fatal("replay changed state/event/times")
	}
	f.assertLifecycle(t, "active", 1)
	archive, ok := projects.ParseProjectPhysicalArchiveState(replayed.Project.ArchiveState)
	if !ok {
		t.Fatal("unparseable restore history")
	}
	archive.Restore = nil
	if !reflect.DeepEqual(archive, f.archive.State) {
		t.Fatal("original archive history changed")
	}
	if err := projects.EnsureRuntimeActive(ctx, f.db, projects.RuntimeRef{ProjectID: f.archive.State.ProjectID}); !errors.Is(err, projects.ErrProjectRestoreBlocked) {
		t.Fatalf("runtime guard: %v", err)
	}
	if err := projects.EnsureRuntimeActive(ctx, f.db, projects.RuntimeRef{ScopeID: f.archive.ExpectedScopeID}); !errors.Is(err, projects.ErrProjectRestoreBlocked) {
		t.Fatalf("scope guard: %v", err)
	}
	var payload []byte
	if err := f.db.QueryRowContext(ctx, `SELECT payload FROM events.events WHERE event_id=$1`, completed.State.EventID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatal(err)
	}
	if event["restore_operation_id"] != completed.State.OperationID || event["archive_operation_id"] != archive.OperationID || event["activation_state"] != "inactive" || event["restored_at"] != completed.State.RestoredAt.Format(time.RFC3339Nano) {
		t.Fatalf("event binding: %s", payload)
	}
	if _, err := f.db.ExecContext(ctx, `UPDATE events.events SET payload='{}'::jsonb WHERE event_id=$1`, completed.State.EventID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.transition(completed.State); err == nil {
		t.Fatal("terminal replay accepted changed event")
	}
	if _, err := f.db.ExecContext(ctx, `UPDATE events.events SET payload=$2::jsonb WHERE event_id=$1`, completed.State.EventID, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := f.transition(completed.State); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.TransitionProjectPhysicalArchive(ctx, f.req, f.archive); err == nil {
		t.Fatal("old archive replay accepted")
	}
}

func TestProjectPhysicalRestoreDriftPostgres(t *testing.T) {
	f := newRestoreTransactionFixture(t)
	ctx := context.Background()
	if _, err := f.transition(f.states[0]); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"archive", "actor", "scope", "revision", "source", "member", "owner", "phase", "event"} {
		t.Run(name, func(t *testing.T) {
			input := projects.ProjectPhysicalRestoreTransitionInput{Archive: f.archive, State: f.states[1]}
			input.Archive.ExpectedMembers = append([]projects.ProjectArchiveRepositoryMemberBinding(nil), f.archive.ExpectedMembers...)
			req := f.req
			switch name {
			case "archive":
				input.Archive.State.Reason += "changed"
			case "actor":
				req.ActorID += "changed"
			case "scope":
				input.Archive.ExpectedScopeID += "changed"
			case "revision":
				input.Archive.ExpectedRegistrationRevision++
			case "source":
				input.Archive.ExpectedRepositorySourceRevision++
			case "member":
				input.Archive.ExpectedMembers[0].MemberKey += "changed"
			case "owner":
				input.Archive.ExpectedMembers[0].RepositoryOwnerProjectID += "changed"
			case "phase":
				input.State = f.states[2]
			case "event":
				input.State.EventID = "event_01ARZ3NDEKTSV4RRFFQ69G5FAV"
			}
			if _, err := f.service.TransitionProjectPhysicalRestore(ctx, req, input); err == nil {
				t.Fatal("changed binding accepted")
			}
			f.assertLifecycle(t, "archived", 0)
		})
	}
	// Observe a real current-row drift, not just a changed caller expectation.
	if _, err := f.db.ExecContext(ctx, `UPDATE projects.project_repository_sources SET source_revision=source_revision+1 WHERE project_id=$1`, f.archive.State.ProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.transition(f.states[1]); err == nil {
		t.Fatal("live source drift accepted")
	}
	if _, err := f.db.ExecContext(ctx, `UPDATE projects.project_repository_sources SET source_revision=source_revision-1 WHERE project_id=$1`, f.archive.State.ProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.transition(f.states[1]); err != nil {
		t.Fatal(err)
	}
	f.assertLifecycle(t, "archived", 0)
	extraID := newRepositoryID()
	if _, err := f.db.ExecContext(ctx, `INSERT INTO projects.repositories(repository_id,owning_project_id,lifecycle_status) VALUES($1,$2,'archived')`, extraID, f.archive.State.ProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.transition(f.states[2]); err == nil {
		t.Fatal("unreviewed owned repository accepted")
	}
	assertRowCount(t, f.db, `SELECT count(*) FROM events.events WHERE target_id=$1 AND event_type='project.restored'`, 0, f.archive.State.ProjectID)
}

func TestProjectPhysicalRestoreSilentUpdateRefusalPostgres(t *testing.T) {
	f := newRestoreTransactionFixture(t)
	ctx := context.Background()
	for _, state := range f.states[:2] {
		if _, err := f.transition(state); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.db.ExecContext(ctx, `CREATE FUNCTION projects.restore_test_skip() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.registration_status='registered' AND NEW.project_id='`+f.archive.State.ProjectID+`' THEN RETURN NULL; END IF; RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.db.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS restore_test_skip ON projects.project_contract_registrations`)
		_, _ = f.db.ExecContext(context.Background(), `DROP FUNCTION IF EXISTS projects.restore_test_skip()`)
	})
	if _, err := f.db.ExecContext(ctx, `CREATE TRIGGER restore_test_skip BEFORE UPDATE ON projects.project_contract_registrations FOR EACH ROW EXECUTE FUNCTION projects.restore_test_skip()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.transition(f.states[2]); err == nil || !strings.Contains(err.Error(), "affected 0 rows") {
		t.Fatalf("silent update refusal: %v", err)
	}
	f.assertLifecycle(t, "archived", 0)
}

func TestProjectPhysicalRestoreConcurrentIntentPostgres(t *testing.T) {
	f := newRestoreTransactionFixture(t)
	var wg sync.WaitGroup
	errorsCh := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := f.transition(f.states[0]); errorsCh <- err }()
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	f.assertLifecycle(t, "archived", 0)
	if _, err := f.transition(f.states[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.transition(f.states[2]); err != nil {
		t.Fatal(err)
	}
	f.assertLifecycle(t, "active", 1)
}

func TestProjectPhysicalRestoreDatabaseNotConfigured(t *testing.T) {
	_, err := (projects.Service{}).TransitionProjectPhysicalRestore(context.Background(), requestctx.Context{}, projects.ProjectPhysicalRestoreTransitionInput{})
	if err == nil {
		t.Fatal("missing database accepted")
	}
}
