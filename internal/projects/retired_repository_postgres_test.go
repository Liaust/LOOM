package projects_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectregistration"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

// Use the standard dedicated-DB helper, including its normal unset-URL skip.
// Fixtures use unique project IDs and project-scoped assertions in the shared DB.
func rrxDatabase(t *testing.T) (*sql.DB, requestctx.Context) {
	t.Helper()
	return projectRepositoryTransactionDatabase(t)
}

type rrxFixture struct {
	db                           *sql.DB
	req                          requestctx.Context
	service                      projects.Service
	archive                      projects.ProjectPhysicalArchiveTransitionInput
	retiredID, foreignID         string
	foreignProject, subjectRoot  string
	currentMembers, currentOwned int
	protected                    string
}

func rrxResource(path, role, id string) any {
	repo := map[string]string{"path": path, "role": role}
	if id != "" {
		repo["id"] = id
	}
	return map[string]any{"kind": "repository", "repository": repo}
}

func rrxRegister(t *testing.T, f rrxFixture, root, id string, resources map[string]any) projects.RegisterProjectContractResult {
	t.Helper()
	for key := range resources {
		if err := os.MkdirAll(filepath.Join(root, key), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, ".loom"), 0700); err != nil {
		t.Fatal(err)
	}
	raw := marshalJSON(t, map[string]any{
		"kind": "loom.project", "schema_version": pc.ProjectSchemaV05,
		"project":   map[string]string{"id": id, "slug": "retired-" + strings.ToLower(strings.TrimPrefix(id, "project_")), "name": "Retirement review", "owner_node": "main", "status": "active"},
		"resources": resources,
	})
	if err := os.WriteFile(filepath.Join(root, ".loom", "project.yaml"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	input, err := projectregistration.BuildInput(pc.Analyze(root), "retired-repository-review")
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.service.RegisterProjectContract(t.Context(), f.req, input)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func rrxNew(t *testing.T, mixed bool) rrxFixture {
	t.Helper()
	db, req := rrxDatabase(t)
	f := rrxFixture{db: db, req: req, service: projects.NewService(db), subjectRoot: t.TempDir(), foreignProject: ids.NewProjectID()}
	rrxRegister(t, f, t.TempDir(), f.foreignProject, map[string]any{"shared": rrxResource("shared", "component", "")})
	foreign, err := f.service.ReadProjectRepositoryState(t.Context(), f.foreignProject)
	if err != nil || len(foreign.Members) != 1 {
		t.Fatalf("foreign fixture: %+v %v", foreign, err)
	}
	f.foreignID = foreign.Members[0].RepositoryID
	id := ids.NewProjectID()
	resources := map[string]any{"retired": rrxResource("retired", "component", "")}
	if mixed {
		resources["current"] = rrxResource("current", "component", "")
		resources["reference"] = rrxResource("reference", "reference", f.foreignID)
		f.currentMembers, f.currentOwned = 2, 1
	}
	rrxRegister(t, f, f.subjectRoot, id, resources)
	initial, err := f.service.ReadProjectRepositoryState(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range initial.Members {
		if member.Key == "retired" {
			f.retiredID = member.RepositoryID
		}
	}
	if ids.Validate("repo", f.retiredID) != nil {
		t.Fatal("registration did not generate a repository identity")
	}
	delete(resources, "retired")
	registered := rrxRegister(t, f, f.subjectRoot, id, resources)
	current, err := f.service.ReadProjectRepositoryState(t.Context(), id)
	if err != nil || current.Source == nil || current.Source.SourceRevision != 2 || len(current.Members) != f.currentMembers {
		t.Fatalf("retirement current read: %+v %v", current, err)
	}
	assertRepositoryOwner(t, db, f.retiredID, id)
	assertRepositoryLifecycle(t, db, f.retiredID, "archived")
	assertRowCount(t, db, `SELECT count(*) FROM projects.project_repository_memberships WHERE project_id=$1 AND repository_id=$2`, 0, id, f.retiredID)
	assertRowCount(t, db, `SELECT count(*) FROM projects.project_repository_source_history WHERE project_id=$1`, 2, id)
	at := time.Date(2026, 9, 12, 20, 0, 0, 0, time.UTC)
	digest := "sha256:" + strings.Repeat("a", 64)
	f.archive = projects.ProjectPhysicalArchiveTransitionInput{
		State: projects.ProjectPhysicalArchiveState{SchemaVersion: projects.ProjectPhysicalArchiveStateSchemaVersion, Status: "in_progress", Phase: projects.ProjectArchivePhaseDeactivationPending,
			MutationBlocked: true, ProjectID: id, ProjectSlug: current.Project.Slug, OperationID: "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAV", PlanDigest: digest, WorkspacePlanDigest: digest,
			ActivePath: f.subjectRoot, ArchivePath: f.subjectRoot + "-archive", ActorID: req.ActorID, Reason: "owned retirement regression", StartedAt: at},
		ExpectedScopeID: current.Project.ProjectScopeID, ExpectedRegistrationID: registered.Detail.Registration.ProjectContractRegistrationID,
		ExpectedRegistrationRevision: registered.Detail.Registration.RegistrationRevision, ExpectedRepositorySourceRevision: current.Source.SourceRevision,
	}
	for _, m := range current.Members {
		f.archive.ExpectedMembers = append(f.archive.ExpectedMembers, projects.ProjectArchiveRepositoryMemberBinding{RepositoryID: m.RepositoryID, RepositoryOwnerProjectID: m.RepositoryOwnerProjectID, MemberKey: m.Key, Role: m.Role})
	}
	f.protected = f.protectedSnapshot(t)
	return f
}

// Include timestamps and complete JSON, not only counts: no touching retained
// repositories, foreign ownership/memberships, or immutable source evidence.
func (f rrxFixture) protectedSnapshot(t *testing.T) string {
	t.Helper()
	var result string
	err := f.db.QueryRowContext(t.Context(), `SELECT jsonb_build_object(
		'sources',(SELECT jsonb_agg(to_jsonb(s) ORDER BY project_id) FROM projects.project_repository_sources s WHERE project_id IN ($3,$4)),
		'history',(SELECT jsonb_agg(to_jsonb(h) ORDER BY project_id,source_revision) FROM projects.project_repository_source_history h WHERE project_id IN ($3,$4)),
		'retired',(SELECT to_jsonb(r) FROM projects.repositories r WHERE repository_id=$1),
		'foreign_repo',(SELECT to_jsonb(r) FROM projects.repositories r WHERE repository_id=$2),
		'foreign_members',(SELECT jsonb_agg(to_jsonb(m) ORDER BY repository_id) FROM projects.project_repository_memberships m WHERE project_id=$3)
	)::text`, f.retiredID, f.foreignID, f.foreignProject, f.archive.State.ProjectID).Scan(&result)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func (f rrxFixture) assertProjection(t *testing.T, lifecycle string) {
	t.Helper()
	if f.protectedSnapshot(t) != f.protected {
		t.Fatal("retired repository, foreign ownership/membership, or source/history changed")
	}
	id := f.archive.State.ProjectID
	model, err := f.service.ReadProjectRepositoryState(t.Context(), id)
	if err != nil || model.Project.Status != lifecycle || len(model.Members) != f.currentMembers {
		t.Fatalf("current read projection: %+v %v", model, err)
	}
	for _, m := range model.Members {
		if m.RepositoryID == f.retiredID || string(m.MembershipLifecycle) != lifecycle {
			t.Fatal("retired membership resurrected or current membership lifecycle changed")
		}
	}
	state, err := f.service.GetDeclarationProjectState(t.Context(), id)
	if err != nil || len(state.Repositories) != f.currentMembers || state.Repositories["retired"] != "" {
		t.Fatalf("declaration read resurrected retired key: %+v %v", state, err)
	}
	assertRowCount(t, f.db, `SELECT count(*) FROM projects.repositories r JOIN projects.project_repository_memberships m USING(repository_id) WHERE m.project_id=$1 AND r.owning_project_id=$1 AND r.lifecycle_status=$2`, f.currentOwned, id, lifecycle)
	assertRowCount(t, f.db, `SELECT count(*) FROM scopes.scopes WHERE scope_id=$1 AND status=$2`, 1, f.archive.ExpectedScopeID, lifecycle)
	registration := "registered"
	if lifecycle == "archived" {
		registration = "archived"
	}
	assertRowCount(t, f.db, `SELECT count(*) FROM projects.project_contract_registrations WHERE project_id=$1 AND registration_status=$2 AND activation_status='inactive'`, 1, id, registration)
}

func rrxArchiveStates(initial projects.ProjectPhysicalArchiveState) []projects.ProjectPhysicalArchiveState {
	states := []projects.ProjectPhysicalArchiveState{initial}
	runtimeAt, workspaceAt, archivedAt := initial.StartedAt.Add(time.Second), initial.StartedAt.Add(2*time.Second), initial.StartedAt.Add(3*time.Second)
	initial.Phase, initial.RuntimeDeactivatedAt, initial.RuntimeQuiescenceDigest = projects.ProjectArchivePhaseRuntimeDeactivated, &runtimeAt, initial.PlanDigest
	states = append(states, initial)
	initial.Phase, initial.WorkspaceCompletedAt, initial.ArchiveManifestDigest = projects.ProjectArchivePhaseProjectStatePending, &workspaceAt, initial.PlanDigest
	states = append(states, initial)
	initial.Phase, initial.Status, initial.ArchivedAt = projects.ProjectArchivePhaseComplete, "archived", &archivedAt
	return append(states, initial)
}

func TestRetiredRepositoryArchiveRegressionPostgres(t *testing.T) {
	for _, mode := range []string{"empty", "mixed_owned_and_foreign_reference"} {
		t.Run(mode, func(t *testing.T) {
			f := rrxNew(t, mode != "empty")
			for _, state := range rrxArchiveStates(f.archive.State) {
				f.archive.State = state
				before := f.fullSnapshot(t)
				result, err := f.service.TransitionProjectPhysicalArchive(t.Context(), f.req, f.archive)
				if err != nil {
					if before != f.fullSnapshot(t) {
						t.Fatal("failed archive changed durable rows")
					}
					f.assertProjection(t, "active")
					assertRowCount(t, f.db, `SELECT count(*) FROM events.events WHERE target_id=$1 AND event_type='project.archived'`, 0, state.ProjectID)
					t.Fatalf("archive phase %s after actual declaration retirement: %v", state.Phase, err)
				}
				beforeReplay := f.fullSnapshot(t)
				replay, err := projects.NewService(f.db).TransitionProjectPhysicalArchive(t.Context(), f.req, f.archive)
				if err != nil || !replay.Replay || replay.EventID != "" || !reflect.DeepEqual(replay.State, result.State) || beforeReplay != f.fullSnapshot(t) {
					t.Fatalf("archive replay changed durable state: %+v %v", replay, err)
				}
			}
			f.assertProjection(t, "archived")
			assertRowCount(t, f.db, `SELECT count(*) FROM events.events WHERE target_id=$1 AND event_type='project.archived'`, 1, f.archive.State.ProjectID)
			f.restoreAndReplay(t)
		})
	}
}

// Isolate restore from the known archive failure. This SQL seeds a terminal
// projection after real registration/retirement; it is NOT physical archive
// acceptance, a product workaround, or evidence that archive completion passed.
func (f *rrxFixture) seedArchivedProjection(t *testing.T) {
	t.Helper()
	f.archive.State = rrxArchiveStates(f.archive.State)[3]
	payload := marshalJSON(t, f.archive.State)
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE projects.projects SET status='archived',archive_state=$2 WHERE project_id=$1`, []any{f.archive.State.ProjectID, payload}},
		{`UPDATE projects.project_contract_registrations SET registration_status='archived',activation_status='inactive' WHERE project_id=$1`, []any{f.archive.State.ProjectID}},
		{`UPDATE projects.project_repository_memberships SET lifecycle_status='archived' WHERE project_id=$1`, []any{f.archive.State.ProjectID}},
		{`UPDATE projects.repositories r SET lifecycle_status='archived' WHERE r.owning_project_id=$1 AND EXISTS(SELECT 1 FROM projects.project_repository_memberships m WHERE m.project_id=$1 AND m.repository_id=r.repository_id)`, []any{f.archive.State.ProjectID}},
		{`UPDATE scopes.scopes SET status='archived' WHERE scope_id=$1`, []any{f.archive.ExpectedScopeID}},
	} {
		if _, err := tx.ExecContext(t.Context(), statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	f.assertProjection(t, "archived")
}

func TestRetiredRepositoryRestoreRegressionPostgres(t *testing.T) {
	for _, mode := range []string{"empty", "mixed_owned_and_foreign_reference"} {
		t.Run(mode, func(t *testing.T) {
			f := rrxNew(t, mode != "empty")
			f.seedArchivedProjection(t)
			f.restoreAndReplay(t)
		})
	}
}

func (f rrxFixture) restoreAndReplay(t *testing.T) {
	t.Helper()
	at := f.archive.State.ArchivedAt.Add(time.Second)
	moved, restored := at.Add(time.Second), at.Add(2*time.Second)
	initial := projects.ProjectPhysicalRestoreState{SchemaVersion: projects.ProjectPhysicalRestoreStateSchemaVersion, Request: f.req,
		OperationID: "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAZ", PlanDigest: f.archive.State.PlanDigest, WorkspacePlanDigest: f.archive.State.WorkspacePlanDigest,
		Phase: projects.ProjectRestorePhasePending, Status: "in_progress", MutationBlocked: true, ActivationState: "inactive", StartedAt: at}
	pending := initial
	pending.Phase, pending.WorkspaceCompletedAt, pending.ActiveManifestDigest = projects.ProjectRestorePhaseProjectStatePending, &moved, initial.PlanDigest
	terminal := pending
	terminal.Phase, terminal.Status, terminal.RestoredAt = projects.ProjectRestorePhaseComplete, "restored", &restored
	for _, state := range []projects.ProjectPhysicalRestoreState{initial, pending, terminal} {
		input := projects.ProjectPhysicalRestoreTransitionInput{Archive: f.archive, State: state}
		before := f.fullSnapshot(t)
		result, err := projects.NewService(f.db).TransitionProjectPhysicalRestore(t.Context(), f.req, input)
		if err != nil {
			if before != f.fullSnapshot(t) {
				t.Fatal("failed restore changed durable rows")
			}
			t.Fatalf("restore phase %s with proven historical retired repository: %v", state.Phase, err)
		}
		lifecycle := "archived"
		if state.Phase == projects.ProjectRestorePhaseComplete {
			lifecycle = "active"
			if result.State.EventID == "" {
				t.Fatal("restore completion missing event")
			}
		}
		f.assertProjection(t, lifecycle)
		input.State = result.State
		before = f.fullSnapshot(t)
		replay, err := projects.NewService(f.db).TransitionProjectPhysicalRestore(t.Context(), f.req, input)
		if err != nil || !replay.Replay || !reflect.DeepEqual(result.State, replay.State) || before != f.fullSnapshot(t) {
			t.Fatalf("restore replay changed durable state: %+v %v", replay, err)
		}
		if !errors.Is(projects.EnsureProjectMutable(result.Project, "repository", "review"), projects.ErrProjectRestoreBlocked) {
			t.Fatal("restore released mutation fence")
		}
	}
	assertRowCount(t, f.db, `SELECT count(*) FROM events.events WHERE target_id=$1 AND event_type='project.restored'`, 1, f.archive.State.ProjectID)
	before := f.fullSnapshot(t)
	if _, err := f.service.TransitionProjectPhysicalArchive(t.Context(), f.req, f.archive); err == nil || before != f.fullSnapshot(t) {
		t.Fatal("old archive replay accepted or changed restored state")
	}
}

func (f rrxFixture) fullSnapshot(t *testing.T) string {
	t.Helper()
	var rows []string
	for _, table := range []string{"projects.projects", "scopes.scopes", "projects.project_contract_registrations", "projects.repositories", "projects.project_repository_memberships", "projects.project_repository_observations", "projects.project_repository_sources", "projects.project_repository_source_history", "events.events"} {
		var snapshot string
		clause := "project_id IN ($1,$2)"
		switch table {
		case "scopes.scopes":
			clause = "scope_id IN (SELECT project_scope_id FROM projects.projects WHERE project_id IN ($1,$2))"
		case "projects.repositories":
			clause = "owning_project_id IN ($1,$2)"
		case "events.events":
			clause = "target_id IN ($1,$2) OR target_id IN (SELECT project_contract_registration_id FROM projects.project_contract_registrations WHERE project_id IN ($1,$2))"
		}
		if err := f.db.QueryRowContext(t.Context(), `SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM `+table+` t WHERE `+clause, f.archive.State.ProjectID, f.foreignProject).Scan(&snapshot); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, snapshot)
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// Preserve the existing unexplained-owned-row refusal: merely narrowing the
// owned-set query to memberships (or deleting the guard) is not sufficient.
func TestRetiredRepositoryUnexplainedOwnedRowRefusedPostgres(t *testing.T) {
	f := rrxNew(t, false)
	f.seedArchivedProjection(t)
	if _, err := f.db.ExecContext(t.Context(), `INSERT INTO projects.repositories(repository_id,owning_project_id,lifecycle_status) VALUES($1,$2,'archived')`, newRepositoryID(), f.archive.State.ProjectID); err != nil {
		t.Fatal(err)
	}
	state := projects.ProjectPhysicalRestoreState{SchemaVersion: projects.ProjectPhysicalRestoreStateSchemaVersion, Request: f.req,
		OperationID: "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAZ", PlanDigest: f.archive.State.PlanDigest, WorkspacePlanDigest: f.archive.State.WorkspacePlanDigest,
		Phase: projects.ProjectRestorePhasePending, Status: "in_progress", MutationBlocked: true, ActivationState: "inactive", StartedAt: f.archive.State.ArchivedAt.Add(time.Second)}
	before := f.fullSnapshot(t)
	if _, err := f.service.TransitionProjectPhysicalRestore(t.Context(), f.req, projects.ProjectPhysicalRestoreTransitionInput{Archive: f.archive, State: state}); err == nil {
		t.Fatal("unexplained owned repository accepted as retired history")
	}
	if before != f.fullSnapshot(t) {
		t.Fatal("refused owned-set drift changed durable state")
	}
}

// Rebuild internally valid digests for semantic controls, so reference-only and
// wrong-project cases cannot pass merely because the test broke a checksum.
func rrxRebuildSource(t *testing.T, f rrxFixture, table string, revision int64, mutate func(*projects.ProjectRepositoryValidatedSourceInput)) {
	t.Helper()
	if table != "project_repository_source_history" && table != "project_repository_sources" {
		t.Fatal("invalid fixture table")
	}
	var raw []byte
	if err := f.db.QueryRowContext(t.Context(), `SELECT to_jsonb(s)-'source_snapshot_json'||jsonb_build_object('source_snapshot',source_snapshot_json) FROM projects.`+table+` s WHERE project_id=$1 AND source_revision=$2`, f.archive.State.ProjectID, revision).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var source projects.ProjectRepositorySource
	if err := json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	canonical, err := projects.DecodeProjectRepositoryCanonicalSource(source.SourceSnapshot, source.SemanticDigest, source.LocationDigest)
	if err != nil {
		t.Fatal(err)
	}
	s := canonical.Snapshot
	input := projects.ProjectRepositoryValidatedSourceInput{ProjectID: s.ProjectID, ProjectRoot: s.Location.ProjectRoot,
		ProjectContractPath: s.Location.ProjectContractPath, ReposContractPath: s.Location.ReposContractPath, OwnerNode: s.Location.OwnerNode,
		Versions: s.SourceVersions, ProjectContractDigest: s.ProjectContractDigest, ReposContractDigest: s.ReposContractDigest, RegistrationPlan: s.StableRegistrationPlan}
	for _, member := range s.Members {
		input.Members = append(input.Members, projects.ProjectRepositoryValidatedMember{RepositoryID: member.RepositoryID, Key: member.Key, Path: member.Path, Role: member.Role, StateRoot: member.StateRoot})
	}
	mutate(&input)
	rebuilt, err := projects.BuildProjectRepositoryCanonicalSource(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projects.DecodeProjectRepositoryCanonicalSource(rebuilt.SnapshotJSON, rebuilt.SemanticDigest, rebuilt.LocationDigest); err != nil {
		t.Fatalf("semantic control did not have valid canonical digests: %v", err)
	}
	if _, err := f.db.ExecContext(t.Context(), `UPDATE projects.`+table+` SET source_snapshot_json=$3,semantic_digest=$4,location_digest=$5 WHERE project_id=$1 AND source_revision=$2`, f.archive.State.ProjectID, revision, rebuilt.SnapshotJSON, rebuilt.SemanticDigest, rebuilt.LocationDigest); err != nil {
		t.Fatal(err)
	}
}

func rrxCorruptRetirement(t *testing.T, f rrxFixture, mode string) {
	t.Helper()
	var err error
	switch mode {
	case "active_retained_row":
		_, err = f.db.ExecContext(t.Context(), `UPDATE projects.repositories SET lifecycle_status='active' WHERE repository_id=$1`, f.retiredID)
	case "corrupt_history_digest":
		_, err = f.db.ExecContext(t.Context(), `UPDATE projects.project_repository_source_history SET semantic_digest=$2 WHERE project_id=$1 AND source_revision=1`, f.archive.State.ProjectID, "sha256:"+strings.Repeat("0", 64))
	case "corrupt_history_columns":
		_, err = f.db.ExecContext(t.Context(), `UPDATE projects.project_repository_source_history SET project_root=project_root||'-forged',project_contract_path=project_root||'-forged/.loom/project.yaml',repos_contract_path=project_root||'-forged/.loom/project.yaml' WHERE project_id=$1 AND source_revision=1`, f.archive.State.ProjectID)
	case "reference_only_valid_digest":
		rrxRebuildSource(t, f, "project_repository_source_history", 1, func(input *projects.ProjectRepositoryValidatedSourceInput) {
			input.Members[0].Role = projects.ProjectRepositoryRoleReference
		})
	case "wrong_project_valid_digest":
		rrxRebuildSource(t, f, "project_repository_source_history", 1, func(input *projects.ProjectRepositoryValidatedSourceInput) {
			input.ProjectID = f.foreignProject
		})
	case "not_prior_history":
		_, err = f.db.ExecContext(t.Context(), `UPDATE projects.project_repository_source_history SET source_revision=3 WHERE project_id=$1 AND source_revision=1`, f.archive.State.ProjectID)
	case "still_in_current_source":
		rrxRebuildSource(t, f, "project_repository_sources", 2, func(input *projects.ProjectRepositoryValidatedSourceInput) {
			input.Members = []projects.ProjectRepositoryValidatedMember{{RepositoryID: f.retiredID, Key: "retired", Path: "retired", Role: projects.ProjectRepositoryRoleComponent}}
		})
	default:
		t.Fatal("unknown retirement corruption")
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestRetiredRepositoryHistoricalExemptionRefusalsPostgres(t *testing.T) {
	for _, phase := range []string{"archive_intent", "restore_intent", "restore_replay"} {
		for _, mode := range []string{"active_retained_row", "corrupt_history_digest", "corrupt_history_columns", "reference_only_valid_digest", "wrong_project_valid_digest", "not_prior_history", "still_in_current_source"} {
			t.Run(phase+"/"+mode, func(t *testing.T) {
				f := rrxNew(t, false)
				if phase != "archive_intent" {
					f.seedArchivedProjection(t)
				}
				var restore projects.ProjectPhysicalRestoreState
				if phase == "restore_replay" {
					f.restoreAndReplay(t)
					var raw []byte
					if err := f.db.QueryRowContext(t.Context(), `SELECT archive_state->'restore' FROM projects.projects WHERE project_id=$1`, f.archive.State.ProjectID).Scan(&raw); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(raw, &restore); err != nil {
						t.Fatal(err)
					}
				} else if phase == "restore_intent" {
					restore = projects.ProjectPhysicalRestoreState{SchemaVersion: projects.ProjectPhysicalRestoreStateSchemaVersion, Request: f.req,
						OperationID: "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAZ", PlanDigest: f.archive.State.PlanDigest, WorkspacePlanDigest: f.archive.State.WorkspacePlanDigest,
						Phase: projects.ProjectRestorePhasePending, Status: "in_progress", MutationBlocked: true, ActivationState: "inactive", StartedAt: f.archive.State.ArchivedAt.Add(time.Second)}
				}
				rrxCorruptRetirement(t, f, mode)
				before := f.fullSnapshot(t)
				var err error
				if phase == "archive_intent" {
					_, err = f.service.TransitionProjectPhysicalArchive(t.Context(), f.req, f.archive)
				} else {
					_, err = f.service.TransitionProjectPhysicalRestore(t.Context(), f.req, projects.ProjectPhysicalRestoreTransitionInput{Archive: f.archive, State: restore})
				}
				if err == nil || before != f.fullSnapshot(t) {
					t.Fatalf("invalid historical exemption accepted or mutated rows: %v", err)
				}
				t.Logf("refused %s: %v", mode, err)
			})
		}
	}
}
