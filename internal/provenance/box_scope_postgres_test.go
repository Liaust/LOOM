package provenance

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestBoxScopedProvenancePostgres(t *testing.T) {
	pool, store, _ := migratedStore(t, "box_scope")
	global := context.Background()
	now := time.Now().UTC().Add(-time.Minute)
	for i, anchors := range []*StructuralAnchors{{Boxes: []string{"box_personal"}}, {Boxes: []string{"box_company"}}, nil, {Boxes: []string{"box_company", "box_personal"}}} {
		id := semanticID(100 + i)
		if err := store.AppendRecord(global, Record{ID: id, SchemaVersion: SchemaVersion, Claim: "same decision", RecordKind: "decision", RecordContext: "same title", Domain: "loom", Visibility: "private", AssertionPosture: "qualified", Temporal: TemporalInterpretation{Interpretation: "current"}, Anchors: anchors, CreatedAt: now, Payload: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
		submitted, _ := json.Marshal(CandidateRegistration{Anchors: anchors})
		if err := store.AppendCandidate(global, Candidate{ID: id, SchemaVersion: SchemaVersion, State: "pending", Claim: "same decision", RecordKind: "decision", RecordContext: "same title", Domain: "loom", Visibility: "private", AssertionPosture: "qualified", ProducerID: "fixture", RegisteredAt: now, Submitted: submitted, Payload: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
		caseID := semanticID(200 + i)
		if err := store.AppendResolutionCase(global, ResolutionCase{ID: caseID, SchemaVersion: SchemaVersion, Issue: "same decision", Domain: "loom", Visibility: "private", InitialStatus: "open", CreatedAt: now, Payload: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
		if err := store.AppendCaseMember(global, CaseMember{CaseID: caseID, MemberType: "candidate", CandidateID: &id, Role: "context", AttachedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	ctx := boxContext("box_company")
	records, err := store.ListRecords(ctx, PageRequest{Limit: 1})
	if err != nil || len(records.Items) != 1 || records.Items[0].ID != semanticID(101) || records.Truncated {
		t.Fatal(records, err)
	}
	candidates, err := store.ListCandidates(ctx, PageRequest{Limit: 1})
	if err != nil || len(candidates.Items) != 1 || candidates.Items[0].ID != semanticID(101) || candidates.Truncated {
		t.Fatal(candidates, err)
	}
	cases, err := store.ListResolutionCases(ctx, PageRequest{Limit: 1})
	if err != nil || len(cases.Items) != 1 || cases.Items[0].ID != semanticID(201) || cases.Truncated {
		t.Fatal(cases, err)
	}
	for _, id := range []SemanticID{semanticID(100), semanticID(102), semanticID(103)} {
		if _, _, err := store.GetRecord(ctx, id); !errors.Is(err, ErrBoxSelectionMismatch) {
			t.Fatal(id, err)
		}
		if _, _, err := store.GetCandidate(ctx, id); !errors.Is(err, ErrBoxSelectionMismatch) {
			t.Fatal(id, err)
		}
	}
	if _, _, err := store.GetResolutionCase(ctx, semanticID(200)); !errors.Is(err, ErrBoxSelectionMismatch) {
		t.Fatal(err)
	}
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	api := &FoundationAPI{store: store, service: service}
	if _, err := api.GetCandidateWithSources(ctx, semanticID(100), 10); !errors.Is(err, ErrBoxSelectionMismatch) {
		t.Fatal("candidate source expansion lost mismatch", err)
	}
	if _, err := api.GetRecordWithSources(ctx, semanticID(100), 10); !errors.Is(err, ErrBoxSelectionMismatch) {
		t.Fatal("record source expansion lost mismatch", err)
	}
	review, err := store.listReview(ctx, ReviewRequest{Limit: 10}, now.Add(time.Second))
	if err != nil || len(review.Items) != 2 {
		t.Fatal(review, err)
	}
	var representativeSQL string
	var representativeArgs []any
	query := store.q.query
	store.q.query = func(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
		representativeSQL, representativeArgs = sql, args
		return query(ctx, sql, args...)
	}
	for _, search := range []func(context.Context, []string, string, string, searchQueryCursor, int) ([]searchQueryRow, bool, error){store.searchAcceptedRecords, store.searchPendingCandidates, store.searchUnresolvedCases} {
		rows, truncated, err := search(ctx, []string{"same"}, "", "", searchQueryCursor{}, 1)
		if err != nil || len(rows) != 1 || truncated {
			t.Fatal(rows, truncated, err)
		}
	}
	var plan json.RawMessage
	if err := pool.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+representativeSQL, representativeArgs...).Scan(&plan); err != nil || !json.Valid(plan) {
		t.Fatal("representative scoped query plan", string(plan), err)
	}
	for _, selected := range []context.Context{boxContext(), boxContext("box_empty")} {
		page, err := store.ListRecords(selected, PageRequest{})
		if err != nil || len(page.Items) != 0 {
			t.Fatal("empty selection broadened", page, err)
		}
	}
	// Inheritance is evidence from the latest project projection, not a slug or
	// a textual mention, and does not rewrite the historical ledger payload.
	input := projectionTestSyncInput()
	input.HomeBoxID = "box_company"
	snapshot, _, err := prepareProjectProjectionSync(input, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.appendProjectProjection(global, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := store.checkAnchors(ctx, &StructuralAnchors{Projects: []string{snapshot.ProjectID}}); err != nil {
		t.Fatal(err)
	}
	if err := store.checkAnchors(boxContext("box_personal"), &StructuralAnchors{Projects: []string{snapshot.ProjectID}}); !errors.Is(err, ErrBoxSelectionMismatch) {
		t.Fatal(err)
	}
}
