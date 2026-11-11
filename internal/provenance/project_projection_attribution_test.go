package provenance

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"loom.local/loom/internal/projectstate"
)

func TestProjectionAttributionPreservesRetainedRepositoryAndUnknownFields(t *testing.T) {
	now := time.Now().UTC()
	previous, repositories, err := prepareProjectProjectionSync(projectionTestSyncInput(), now)
	if err != nil {
		t.Fatal(err)
	}
	previous.ID = semanticID(1)
	next := previous
	next.ID = semanticID(2)
	repository := repositories[0]
	repository.ID, repository.ProjectSnapshotID = semanticID(3), previous.ID
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(repository.CardJSON, &fields); err != nil {
		t.Fatal(err)
	}
	fields["retained_extension"] = json.RawMessage(`{"number":9007199254740993,"qualification":"historical"}`)
	repository.CardJSON, _ = json.Marshal(fields)
	got, changed, err := attributeRetainedRepository(repository, previous, next, "box_company", now.Add(time.Hour))
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	var after map[string]json.RawMessage
	if err := json.Unmarshal(got.CardJSON, &after); err != nil {
		t.Fatal(err)
	}
	var owner map[string]json.RawMessage
	if err := json.Unmarshal(after["owning_project"], &owner); err != nil {
		t.Fatal(err)
	}
	if string(owner["home_box_id"]) != `"box_company"` {
		t.Fatal(string(after["owning_project"]))
	}
	delete(owner, "home_box_id")
	after["owning_project"], _ = json.Marshal(owner)
	var oldOwner map[string]json.RawMessage
	_ = json.Unmarshal(fields["owning_project"], &oldOwner)
	fields["owning_project"], _ = json.Marshal(oldOwner)
	if !reflect.DeepEqual(after, fields) {
		t.Fatal("membership repair changed retained card fields")
	}
	if got.ProjectSnapshotID != next.ID || !reflect.DeepEqual(got.ObservedAt, repository.ObservedAt) || !reflect.DeepEqual(got.ObservedCommit, repository.ObservedCommit) || !reflect.DeepEqual(got.SourceVersion, repository.SourceVersion) || !reflect.DeepEqual(got.SourceDigest, repository.SourceDigest) || got.SearchableSummary != repository.SearchableSummary {
		t.Fatal("membership repair fabricated an observation")
	}
	got.ProjectSnapshotID = next.ID
	if _, changed, err := attributeRetainedRepository(got, next, next, "box_company", now); err != nil || changed {
		t.Fatal("already attributed replay changed", changed, err)
	}
	if _, _, err := attributeRetainedRepository(got, next, next, "box_personal", now); !errors.Is(err, ErrBoxSelectionMismatch) {
		t.Fatal("repair silently transferred membership", err)
	}
}

func TestProjectionAttributionValidationRequiresVerifiedMembership(t *testing.T) {
	api := &FoundationAPI{store: &Store{}, service: &Service{}}
	for _, input := range []ProjectProjectionMembershipInput{
		{ProjectID: projectionTestProjectID},
		{ProjectID: projectionTestProjectID, HomeBoxID: "Personal"},
		{ProjectID: "bad", HomeBoxID: "box_company"},
		{ProjectID: projectionTestProjectID, HomeBoxID: "box_personal"},
	} {
		if _, err := api.SyncProjectProjectionMembership(boxContext("box_company"), input); err == nil {
			t.Fatal("invalid input reached transaction", input)
		}
	}
}

func TestProjectionAttributionReplayAndMissingSnapshotDoNotWrite(t *testing.T) {
	now := time.Now().UTC()
	for _, missing := range []bool{false, true} {
		payload := json.RawMessage(`{"project_id":"` + projectionTestProjectID + `","home_box_id":"box_company","retained_extension":9007199254740993}`)
		writes := 0
		store := &Store{q: queryAdapter{
			exec: func(_ context.Context, query string, _ ...any) (int64, error) {
				if !strings.Contains(query, "pg_advisory_xact_lock") {
					writes++
				}
				return 1, nil
			},
			queryRow: func(context.Context, string, ...any) pgx.Row {
				if missing {
					return projectProjectionTestRow(func(...any) error { return pgx.ErrNoRows })
				}
				return projectProjectionValues(string(semanticID(1)), projectionTestProjectID, int64(2), int64(1), "identity", "digest", "schema", "snapshot", now, "captured summary", payload, now)
			},
			query: func(_ context.Context, query string, _ ...any) (pgx.Rows, error) {
				if !strings.Contains(query, "project_snapshot_id=$2::uuid") {
					t.Fatal(query)
				}
				return &reviewTestRows{}, nil
			},
		}}
		result, err := store.syncProjectProjectionMembership(boxContext("box_company"), ProjectProjectionMembershipInput{ProjectID: projectionTestProjectID, HomeBoxID: "box_company"}, now)
		if missing {
			if !errors.Is(err, ErrProjectProjectionNotFound) {
				t.Fatal(err)
			}
		} else if err != nil || !result.Replayed || result.ProjectSnapshotID != semanticID(1) {
			t.Fatal(result, err)
		}
		if writes != 0 {
			t.Fatal("no-op attribution wrote snapshots", writes)
		}
	}
}

func TestProjectionAttributionDoesNotResurrectReassignedRepository(t *testing.T) {
	store := &Store{q: queryAdapter{
		exec:     func(context.Context, string, ...any) (int64, error) { return 1, nil },
		queryRow: func(context.Context, string, ...any) pgx.Row { return projectProjectionValues(string(semanticID(2))) },
	}}
	err := store.requireCurrentAttributionRepository(context.Background(), repositorySnapshot{ID: semanticID(1), RepositoryID: "repo_changed"})
	var conflict *FoundationConflictError
	if !errors.As(err, &conflict) {
		t.Fatal(err)
	}
}

func TestProjectionAttributionAppendsOnlyMembershipAndRetainsObservation(t *testing.T) {
	now := time.Now().UTC()
	previous, _, err := prepareProjectProjectionSync(projectDevelopmentTestInput("Retained captured purpose"), now)
	if err != nil {
		t.Fatal(err)
	}
	previous.ID, previous.ProjectionRevision = semanticID(1), 3
	var original map[string]json.RawMessage
	if err := json.Unmarshal(previous.ProjectionJSON, &original); err != nil {
		t.Fatal(err)
	}
	original["retained_extension"] = json.RawMessage(`{"exact_number":9007199254740993}`)
	previous.ProjectionJSON, _ = json.Marshal(original)
	previous.SnapshotDigest = digestProjection(previous.ProjectionJSON)
	previous.SourceIdentityDigest, _ = projectProjectionSourceIdentity(previous)
	inserts := 0
	store := &Store{q: queryAdapter{
		queryRow: func(_ context.Context, query string, args ...any) pgx.Row {
			if args[0] != previous.ProjectID {
				t.Fatal(args)
			}
			if strings.Contains(query, "observed_at,searchable_summary,projection_json,created_at") {
				return projectProjectionValues(string(previous.ID), previous.ProjectID, previous.ProjectionRevision, previous.SourceRevision, previous.SourceIdentityDigest, previous.SourceDigest, previous.SourceSchemaVersion, previous.SnapshotDigest, previous.ObservedAt, previous.SearchableSummary, previous.ProjectionJSON, previous.CreatedAt)
			}
			if !strings.Contains(query, "source_identity_digest, source_revision") {
				t.Fatal(query)
			}
			return projectProjectionValues(string(previous.ID), previous.ProjectionRevision, previous.SourceIdentityDigest, previous.SourceRevision, previous.SourceSchemaVersion, previous.SnapshotDigest, previous.SourceDigest)
		},
		query: func(_ context.Context, query string, args ...any) (pgx.Rows, error) {
			if !strings.Contains(query, "project_snapshot_id=$2::uuid") || args[1] != string(previous.ID) {
				t.Fatal(query, args)
			}
			return &reviewTestRows{}, nil
		},
		exec: func(_ context.Context, query string, args ...any) (int64, error) {
			if strings.Contains(query, "pg_advisory_xact_lock") {
				return 1, nil
			}
			if !strings.Contains(query, "INSERT INTO provenance.project_projection_snapshots") {
				t.Fatal("unexpected mutation", query)
			}
			inserts++
			if args[2] != int64(4) || args[3] != previous.SourceRevision || args[5] != previous.SourceDigest || args[8] != previous.ObservedAt || args[9] != previous.SearchableSummary {
				t.Fatal("repair changed source evidence", args)
			}
			var after map[string]json.RawMessage
			if err := json.Unmarshal(args[10].([]byte), &after); err != nil {
				t.Fatal(err)
			}
			if string(after["home_box_id"]) != `"box_company"` {
				t.Fatal(after)
			}
			delete(after, "home_box_id")
			if !reflect.DeepEqual(after, original) {
				t.Fatal("repair rewrote captured project content")
			}
			return 1, nil
		},
	}}
	result, err := store.syncProjectProjectionMembership(boxContext("box_company"), ProjectProjectionMembershipInput{ProjectID: previous.ProjectID, HomeBoxID: "box_company"}, now.Add(time.Hour))
	if err != nil || inserts != 1 || result.Replayed || result.ProjectReplayed || result.ProjectSnapshotID == previous.ID {
		t.Fatal(result, inserts, err)
	}
}

func TestProjectionMembershipOnlySyncPostgres(t *testing.T) {
	pool, store, _ := migratedStore(t, "projection_attribution")
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	api := &FoundationAPI{store: store, service: service}
	input := projectionTestSyncInput()
	// Legacy parents can have retained child cards without a memberships array.
	input.Project.Members = nil
	input.Project.Development = projectstate.ProjectDevelopmentState{}
	global := context.Background()
	before, err := api.SyncProjectProjection(global, input)
	if err != nil {
		t.Fatal(err)
	}
	original, err := api.GetProjectProjection(global, before.ProjectID, before.ProjectSnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	unknownID, projectRecordID := semanticID(100), semanticID(101)
	for _, record := range []Record{
		{ID: unknownID},
		{ID: projectRecordID, Anchors: &StructuralAnchors{Projects: []string{before.ProjectID}}},
	} {
		record.SchemaVersion, record.Claim, record.RecordKind, record.RecordContext = "1.0", "same retained decision", "decision", "retained context"
		record.Domain, record.Visibility, record.AssertionPosture = "loom", "private", "qualified"
		record.Temporal, record.CreatedAt, record.Payload = TemporalInterpretation{Interpretation: "historical"}, time.Now().UTC(), json.RawMessage(`{}`)
		if err := store.AppendRecord(global, record); err != nil {
			t.Fatal(err)
		}
	}
	ctx := boxContext("box_company")
	if _, _, err := store.GetRecord(ctx, projectRecordID); !errors.Is(err, ErrBoxSelectionMismatch) {
		t.Fatal("legacy attribution was fabricated", err)
	}
	repair := ProjectProjectionMembershipInput{ProjectID: before.ProjectID, HomeBoxID: "box_company"}
	result, err := api.SyncProjectProjectionMembership(ctx, repair)
	if err != nil || result.Replayed || result.ProjectSnapshotID == before.ProjectSnapshotID || len(result.Repositories) != len(input.Repositories) {
		t.Fatal(result, err)
	}
	current, err := api.GetProjectProjection(ctx, before.ProjectID, result.ProjectSnapshotID)
	if err != nil || current.Context.HomeBoxID != "box_company" || !current.ObservedAt.Equal(original.ObservedAt) || current.SourceDigest != original.SourceDigest || current.SourceRevision != original.SourceRevision {
		t.Fatal(current, err)
	}
	current.Context.HomeBoxID = ""
	if !reflect.DeepEqual(current.Context, original.Context) {
		t.Fatal("repair reread or changed captured project context")
	}
	old, err := api.GetProjectProjection(global, before.ProjectID, before.ProjectSnapshotID)
	if err != nil || old.Context.HomeBoxID != "" {
		t.Fatal("historical snapshot was rewritten", old, err)
	}
	if _, found, err := store.GetRecord(ctx, projectRecordID); err != nil || !found {
		t.Fatal("project attribution not usable", found, err)
	}
	if _, _, err := store.GetRecord(ctx, unknownID); !errors.Is(err, ErrBoxSelectionMismatch) {
		t.Fatal("nonproject history was attributed", err)
	}
	if record, found, err := store.GetRecord(global, unknownID); err != nil || !found || record.Anchors != nil {
		t.Fatal(record, err)
	}
	for _, child := range result.Repositories {
		var oldJSON, newJSON json.RawMessage
		var oldID SemanticID
		for _, oldChild := range before.Repositories {
			if oldChild.RepositoryID == child.RepositoryID {
				oldID = oldChild.SnapshotID
				break
			}
		}
		if oldID == "" {
			t.Fatal("missing original repository capture", child.RepositoryID)
		}
		if err := pool.QueryRow(global, `SELECT card_json FROM provenance.repository_projection_snapshots WHERE id=$1`, string(oldID)).Scan(&oldJSON); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(global, `SELECT card_json FROM provenance.repository_projection_snapshots WHERE id=$1`, string(child.SnapshotID)).Scan(&newJSON); err != nil {
			t.Fatal(err)
		}
		var oldCard, newCard RepositoryCard
		_ = json.Unmarshal(oldJSON, &oldCard)
		_ = json.Unmarshal(newJSON, &newCard)
		if newCard.OwningProject.HomeBoxID != "box_company" {
			t.Fatal(newCard)
		}
		newCard.OwningProject.HomeBoxID = ""
		if !reflect.DeepEqual(oldCard, newCard) {
			t.Fatal("repair refreshed repository observations")
		}
	}
	again, err := api.SyncProjectProjectionMembership(ctx, repair)
	if err != nil || !again.Replayed || again.ProjectSnapshotID != result.ProjectSnapshotID {
		t.Fatal(again, err)
	}
	if _, err := api.SyncProjectProjectionMembership(global, ProjectProjectionMembershipInput{ProjectID: before.ProjectID, HomeBoxID: "box_personal"}); !errors.Is(err, ErrBoxSelectionMismatch) {
		t.Fatal("repair transferred an attributed project", err)
	}
	var count int
	if err := pool.QueryRow(global, `SELECT count(*) FROM provenance.project_projection_snapshots WHERE project_id=$1`, before.ProjectID).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
}
