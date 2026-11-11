package projects_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/projects"
)

func TestProjectLocationTransactionPostgres(t *testing.T) {
	database, req := projectRepositoryTransactionDatabase(t)
	ctx := context.Background()
	service := projects.NewService(database)
	fixture := newRepositoryRegistrationFixture("location", projects.ProjectRepositoryProjectSchemaV04, projects.ProjectRepositoryReposSchemaV04,
		[]repositoryMemberFixture{{ID: newRepositoryID(), Key: "app", Path: "app", Role: "primary"}})
	if _, err := service.RegisterProjectContract(ctx, req, repositoryRegistrationInput(t, fixture)); err != nil {
		t.Fatal(err)
	}
	// These rows must not be reactivated/recreated by the location-only writer.
	for _, query := range []string{
		`UPDATE projects.repositories SET lifecycle_status='archived', metadata='{"retained":true}' WHERE owning_project_id=$1`,
		`UPDATE projects.project_repository_memberships SET lifecycle_status='archived', metadata='{"preserve":true}' WHERE project_id=$1`,
		`UPDATE projects.project_repository_observations SET observation_posture='observed', observed_at=now(), observation_json='{"head":"abc"}', observation_revision=4 WHERE project_id=$1`,
		`UPDATE projects.projects SET metadata=metadata || '{"large":9007199254740993,"unrelated":{"keep":true}}' WHERE project_id=$1`,
	} {
		if _, err := database.ExecContext(ctx, query, fixture.ProjectID); err != nil {
			t.Fatal(err)
		}
	}
	before, err := service.CurrentProjectLocationSnapshot(ctx, fixture.Slug)
	if err != nil {
		t.Fatal(err)
	}
	var saved projects.ProjectLocationSnapshot
	if err := json.Unmarshal(marshalJSON(t, before), &saved); err != nil {
		t.Fatal(err)
	}
	historyBefore := locationHistoryJSON(t, database, fixture.ProjectID)
	facetsBefore := locationFacetsJSON(t, database, fixture.ProjectID)
	newRoot, op := fixture.ProjectRoot+"-moved", "location-op-"+ids.NewProjectID()
	marker := marshalJSON(t, map[string]string{"id": op, "kind": "relocate_project", "source": fixture.ProjectRoot, "destination": newRoot})
	if _, err := database.ExecContext(ctx, `UPDATE projects.projects SET metadata=jsonb_set(metadata,'{workspace_filesystem_operation}',$2::jsonb) WHERE project_id=$1`, fixture.ProjectID, marker); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CurrentProjectLocationSnapshot(ctx, fixture.ProjectID); !errors.Is(err, projects.ErrProjectLocationChanged) {
		t.Fatalf("fresh review accepted pending operation: %v", err)
	}
	withFence, err := service.CurrentProjectLocationSnapshot(ctx, fixture.ProjectID, op)
	if err != nil || withFence.ExpectedDigest != saved.ExpectedDigest {
		t.Fatalf("own fence invalidated review: %v", err)
	}
	// The caller can roll back both the successor and its eventual journal row.
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CommitProjectLocationTx(ctx, tx, saved, newRoot, op); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	afterRollback, err := service.CurrentProjectLocationSnapshot(ctx, fixture.ProjectID, op)
	if err != nil || afterRollback.ExpectedDigest != saved.ExpectedDigest || !bytes.Equal(historyBefore, locationHistoryJSON(t, database, fixture.ProjectID)) {
		t.Fatalf("rollback changed predecessor/history: %v", err)
	}
	// A foreign marker is never removed or ignored.
	tx, err = database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CommitProjectLocationTx(ctx, tx, saved, newRoot, "other"); !errors.Is(err, projects.ErrProjectLocationChanged) {
		_ = tx.Rollback()
		t.Fatalf("accepted foreign marker: %v", err)
	}
	_ = tx.Rollback()
	tx, err = database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	evidence, err := service.CommitProjectLocationTx(ctx, tx, saved, newRoot, op)
	if err != nil {
		t.Fatal(err)
	}
	// Commit deliberately leaves the exact marker installed for its owner.
	var pending []byte
	if err := tx.QueryRowContext(ctx, `SELECT metadata->'workspace_filesystem_operation' FROM projects.projects WHERE project_id=$1`, fixture.ProjectID).Scan(&pending); err != nil || !jsonEquivalent(pending, marker) {
		t.Fatalf("location writer changed pending fence: %s %v", pending, err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE projects.projects SET metadata=metadata-'workspace_filesystem_operation',updated_at=now() WHERE project_id=$1 AND metadata->'workspace_filesystem_operation'->>'id'=$2`, fixture.ProjectID, op); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	after, err := service.CurrentProjectLocationSnapshot(ctx, fixture.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.OperationID != op || evidence.Prior.ExpectedDigest != saved.ExpectedDigest || evidence.Next.ExpectedDigest != after.ExpectedDigest || after.Root != newRoot {
		t.Fatal("journal evidence does not bind actual predecessor/successor")
	}
	if after.RepositorySource.SemanticDigest != before.RepositorySource.SemanticDigest || after.RepositorySource.LocationDigest == before.RepositorySource.LocationDigest || after.RepositorySource.SourceRevision != before.RepositorySource.SourceRevision+1 || after.Registration.RegistrationRevision != before.Registration.RegistrationRevision+1 {
		t.Fatal("source digests/revisions are not a location successor")
	}
	if !reflect.DeepEqual(before.Repositories, after.Repositories) || !bytes.Equal(facetsBefore, locationFacetsJSON(t, database, fixture.ProjectID)) {
		t.Fatal("location successor replaced repository/membership/facet state")
	}
	for _, raw := range [][2]json.RawMessage{{before.Registration.Contract, after.Registration.Contract}, {before.Registration.ValidationReport, after.Registration.ValidationReport}, {before.Registration.RegistrationPlan, after.Registration.RegistrationPlan}, {before.Project.ArchiveState, after.Project.ArchiveState}} {
		if !bytes.Equal(raw[0], raw[1]) {
			t.Fatal("historical request/contract/archive evidence changed")
		}
	}
	if after.Registration.ContractHash != before.Registration.ContractHash || after.Registration.ContractSchemaVersion != before.Registration.ContractSchemaVersion || after.Registration.ActivationStatus != before.Registration.ActivationStatus || after.Project.Status != before.Project.Status || after.Project.ProjectScopeID != before.Project.ProjectScopeID || after.Project.Slug != before.Project.Slug || !bytes.Contains(after.Project.Metadata, []byte("9007199254740993")) {
		t.Fatal("lost identity, activation, contract bytes or unrelated metadata precision")
	}
	for _, observation := range after.Observations {
		if observation.ObservationPosture != projects.ProjectRepositoryObservationNotObserved || observation.ObservedAt != nil || observation.ObservationRevision != 5 || observation.ReasonCode != "source_binding_changed" || string(observation.Observation) != "{}" {
			t.Fatal("old location observation remains current")
		}
	}
	if !bytes.Equal(historyBefore, locationHistoryJSONThroughRevision(t, database, fixture.ProjectID, before.RepositorySource.SourceRevision)) {
		t.Fatal("old history snapshot was rewritten")
	}
	assertRowCount(t, database, `SELECT count(*) FROM projects.project_repository_source_history WHERE project_id=$1 AND source_change_kind='source_relocation'`, 1, fixture.ProjectID)
	// A stale predecessor cannot append another successor, even under a new ID.
	tx, err = database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CommitProjectLocationTx(ctx, tx, saved, fixture.ProjectRoot+"-again", "next"); !errors.Is(err, projects.ErrProjectLocationChanged) {
		_ = tx.Rollback()
		t.Fatalf("accepted stale predecessor: %v", err)
	}
	_ = tx.Rollback()
}

func TestProjectLocationRepositoryLessDeclarationPostgres(t *testing.T) {
	database, req := projectRepositoryTransactionDatabase(t)
	service := projects.NewService(database)
	for _, version := range []string{projects.ProjectRepositoryProjectSchemaV05, projects.ProjectRepositoryProjectSchemaV06} {
		t.Run(version, func(t *testing.T) {
			root, id := t.TempDir(), ids.NewProjectID()
			if _, err := service.RegisterProjectContract(t.Context(), req, declarationRegistrationInputVersion(t, root, id, 0, version)); err != nil {
				t.Fatal(err)
			}
			// Model an older source-less declaration in this owned fixture. The
			// existing history remains evidence and must not be rewritten.
			if _, err := database.Exec(`DELETE FROM projects.project_repository_sources WHERE project_id=$1`, id); err != nil {
				t.Fatal(err)
			}
			historyBefore := locationHistoryJSON(t, database, id)
			before, err := service.CurrentProjectLocationSnapshot(t.Context(), id)
			if err != nil || before.RepositorySource != nil {
				t.Fatalf("repository-less declaration fabricated a source: %#v %v", before.RepositorySource, err)
			}
			tx, err := database.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			evidence, err := service.CommitProjectLocationTx(t.Context(), tx, before, root+"-new", "no-repositories")
			if err != nil {
				t.Fatal(err)
			}
			if evidence.Next.RepositorySource != nil || evidence.Next.Root != root+"-new" || !bytes.Equal(before.Registration.Contract, evidence.Next.Registration.Contract) {
				t.Fatal("source-less declaration lost its actual contract/location")
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			assertRowCount(t, database, `SELECT count(*) FROM projects.project_repository_sources WHERE project_id=$1`, 0, id)
			if !bytes.Equal(historyBefore, locationHistoryJSON(t, database, id)) {
				t.Fatal("source-less declaration rewrote repository history")
			}
		})
	}
}

func locationHistoryJSON(t *testing.T, database *sql.DB, id string) []byte {
	return locationHistoryThrough(t, database, id, ``, nil)
}

func locationHistoryJSONThroughRevision(t *testing.T, database *sql.DB, id string, revision int64) []byte {
	return locationHistoryThrough(t, database, id, ` AND h.source_revision <= $2`, revision)
}

func locationHistoryThrough(t *testing.T, database *sql.DB, id, predicate string, revision any) []byte {
	t.Helper()
	args := []any{id}
	if revision != nil {
		args = append(args, revision)
	}
	var raw []byte
	if err := database.QueryRow(`SELECT coalesce(jsonb_agg(to_jsonb(h) ORDER BY source_revision),'[]'::jsonb) FROM projects.project_repository_source_history h WHERE h.project_id=$1`+predicate, args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func locationFacetsJSON(t *testing.T, database *sql.DB, id string) []byte {
	t.Helper()
	var raw []byte
	if err := database.QueryRow(`SELECT coalesce(jsonb_agg(to_jsonb(f) ORDER BY facet_key),'[]'::jsonb) FROM projects.project_contract_facets f WHERE f.project_id=$1`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func jsonEquivalent(left, right []byte) bool {
	var a, b map[string]string
	return json.Unmarshal(left, &a) == nil && json.Unmarshal(right, &b) == nil && reflect.DeepEqual(a, b)
}

func TestProjectLocationMetadataDriftPostgres(t *testing.T) {
	database, req := projectRepositoryTransactionDatabase(t)
	service := projects.NewService(database)
	fixture := newRepositoryRegistrationFixture("location-drift", projects.ProjectRepositoryProjectSchemaV04, projects.ProjectRepositoryReposSchemaV04, nil)
	if _, err := service.RegisterProjectContract(t.Context(), req, repositoryRegistrationInput(t, fixture)); err != nil {
		t.Fatal(err)
	}
	before, err := service.CurrentProjectLocationSnapshot(t.Context(), fixture.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE projects.projects SET metadata=metadata || '{"unrelated_change":true}' WHERE project_id=$1`, fixture.ProjectID); err != nil {
		t.Fatal(err)
	}
	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := service.CommitProjectLocationTx(t.Context(), tx, before, strings.TrimSuffix(fixture.ProjectRoot, "/")+"-new", "drift"); !errors.Is(err, projects.ErrProjectLocationChanged) {
		t.Fatalf("accepted unrelated metadata drift: %v", err)
	}
}

func TestProjectLocationAlreadyLockedPostgres(t *testing.T) {
	database, req := projectRepositoryTransactionDatabase(t)
	service := projects.NewService(database)
	repoID := newRepositoryID()
	fixture := newRepositoryRegistrationFixture("location-locked", projects.ProjectRepositoryProjectSchemaV04, projects.ProjectRepositoryReposSchemaV04,
		[]repositoryMemberFixture{{ID: repoID, Key: "app", Path: "app", Role: "primary"}})
	if _, err := service.RegisterProjectContract(t.Context(), req, repositoryRegistrationInput(t, fixture)); err != nil {
		t.Fatal(err)
	}
	release, err := service.AcquireProjectArchiveLocks(t.Context(), projects.ProjectArchiveLockKeys(fixture.ProjectID, fixture.Slug, "location-locked", []string{repoID}, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	// Both calls use connections other than the session holding the fence. A
	// project OR repository advisory-lock reentry must fail this bounded check.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	snapshot, err := service.CurrentProjectLocationSnapshotLocked(ctx, fixture.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := service.CommitProjectLocationTx(ctx, tx, snapshot, fixture.ProjectRoot+"-new", "location-locked"); err != nil {
		t.Fatalf("already-locked commit reentered the fence: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	ordinary, cancelOrdinary := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancelOrdinary()
	if _, err := service.CurrentProjectLocationSnapshot(ordinary, fixture.ProjectID); err == nil {
		t.Fatal("ordinary reader stopped acquiring its project advisory fence")
	}
}
