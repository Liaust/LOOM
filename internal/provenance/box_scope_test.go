package provenance

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"loom.local/loom/internal/boxregistry"
)

func boxContext(ids ...string) context.Context {
	return boxregistry.WithSelection(context.Background(), boxregistry.ResolvedSelection{BoxIDs: ids})
}

func TestBoxAttributionDoesNotInferMembershipFromMentions(t *testing.T) {
	store := &Store{}
	ctx := boxContext("box_company")
	for _, anchors := range []*StructuralAnchors{nil, {}, {Entities: []string{"box_company"}}, {Topics: []string{"box_company"}}, {Boxes: []string{"box_personal"}}, {Boxes: []string{"box_company", "box_personal"}}} {
		if err := store.checkAnchors(ctx, anchors); !errors.Is(err, ErrBoxSelectionMismatch) {
			t.Fatalf("attribution %#v: %v", anchors, err)
		}
	}
	if err := store.checkAnchors(ctx, &StructuralAnchors{Boxes: []string{"box_company"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.checkAnchors(boxContext(), &StructuralAnchors{Boxes: []string{"box_company"}}); !errors.Is(err, ErrBoxSelectionMismatch) {
		t.Fatal(err)
	}
	for _, ctx := range []context.Context{context.Background(), boxregistry.WithSelection(context.Background(), boxregistry.ResolvedSelection{AllBoxes: true})} {
		if selectedBoxes(ctx) != nil || store.checkAnchors(ctx, nil) != nil {
			t.Fatal("global history lost qualification")
		}
	}
	if selectedBoxes(boxContext()) == nil {
		t.Fatal("empty selection became global")
	}
}

func TestBoxProjectAttributionUsesLatestVerifiedProjection(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		store := &Store{q: queryAdapter{queryRow: func(_ context.Context, query string, args ...any) pgx.Row {
			if !strings.Contains(query, "home_box_id") || !strings.Contains(query, "ORDER BY p.projection_revision DESC") || !reflect.DeepEqual(args[1], []string{"box_company"}) {
				t.Fatal(query, args)
			}
			return projectProjectionValues(allowed)
		}}}
		err := store.checkAnchors(boxContext("box_company"), &StructuralAnchors{Projects: []string{projectionTestProjectID}})
		if allowed && err != nil || !allowed && !errors.Is(err, ErrBoxSelectionMismatch) {
			t.Fatal(allowed, err)
		}
	}
}

func TestBoxExactReadsRejectForeignAndUnattributedHistory(t *testing.T) {
	for _, anchors := range []*StructuralAnchors{nil, {Boxes: []string{"box_personal"}}, {Boxes: []string{"box_company"}}} {
		submitted, _ := json.Marshal(struct {
			Anchors *StructuralAnchors `json:"anchors"`
		}{anchors})
		rawAnchors, _ := json.Marshal(anchors)
		store := &Store{q: queryAdapter{queryRow: func(_ context.Context, query string, _ ...any) pgx.Row {
			return projectProjectionTestRow(func(out ...any) error {
				*out[0].(*string) = string(semanticID(1))
				if strings.Contains(query, "FROM provenance.candidates") {
					*out[11].(*json.RawMessage) = submitted
				} else {
					*out[5].(*[]byte) = []byte(`[]`)
					*out[16].(*[]byte) = rawAnchors
					*out[17].(*[]byte) = []byte(`{}`)
				}
				return nil
			})
		}}}
		_, candidateFound, candidateErr := store.GetCandidate(boxContext("box_company"), semanticID(1))
		_, recordFound, recordErr := store.GetRecord(boxContext("box_company"), semanticID(1))
		allowed := anchors != nil && anchors.Boxes[0] == "box_company"
		if candidateFound != allowed || recordFound != allowed || allowed && (candidateErr != nil || recordErr != nil) || !allowed && (!errors.Is(candidateErr, ErrBoxSelectionMismatch) || !errors.Is(recordErr, ErrBoxSelectionMismatch)) {
			t.Fatal(anchors, candidateFound, candidateErr, recordFound, recordErr)
		}
		replayErr := store.checkReplaySelection(boxContext("box_company"), RejectCandidateOperation{CandidateID: semanticID(1)})
		if allowed && replayErr != nil || !allowed && !errors.Is(replayErr, ErrBoxSelectionMismatch) {
			t.Fatal("replay bypassed Box context", replayErr)
		}
	}
}

func TestBoxReconciliationEvidenceUsesMembership(t *testing.T) {
	ctx := boxContext("box_company")
	store := &Store{q: queryAdapter{queryRow: func(_ context.Context, query string, args ...any) pgx.Row {
		if !strings.Contains(query, "candidate.submitted_json->'anchors'") || !strings.Contains(query, "record.anchors_json") || !strings.Contains(query, "evidence.payload_json->'anchors'") || !reflect.DeepEqual(args[len(args)-1], []string{"box_company"}) {
			t.Fatal(query, args)
		}
		return projectProjectionValues(false)
	}}}
	state := newReconciliationState(store, LifecycleScope{Domain: "loom", Visibility: "private"})
	if found, err := state.sourceInScope(ctx, semanticID(1)); err != nil || found {
		t.Fatal(found, err)
	}
}

func TestBoxSearchAndReviewCursorsBindSelection(t *testing.T) {
	ctx := boxContext("box_company")
	request := SearchRequest{Query: "same decision"}
	normalized, err := normalizeSearchRequestContext(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	request.Cursor, err = encodeSearchCursor(normalized.Digest, searchQueryRow{ID: string(semanticID(1)), Score: 1, SortTime: time.Now(), CollectionPriority: 0})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = normalizeSearchRequestContext(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, err = normalizeSearchRequestContext(boxContext("box_personal"), request); err == nil {
		t.Fatal("cursor crossed Boxes")
	}
	if _, err = normalizeSearchRequest(request); err == nil {
		t.Fatal("scoped cursor became global")
	}
	store := &Store{q: queryAdapter{queryRow: func(context.Context, string, ...any) pgx.Row { return projectProjectionValues("membership") }, query: func(context.Context, string, ...any) (pgx.Rows, error) {
		t.Fatal("mismatched review reached SQL")
		return nil, nil
	}}}
	now := time.Now().UTC()
	raw, _ := json.Marshal(reviewCursor{AsOf: now, ChangedAt: now, Kind: "candidate", ID: semanticID(1), BoxDigest: selectionDigest(ctx)})
	// Use the normal encoder format rather than a second cursor implementation.
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	if _, err := store.listReview(boxContext("box_personal"), ReviewRequest{Cursor: encoded}, now); err == nil {
		t.Fatal("review cursor crossed Boxes")
	}
}

func TestBoxFiltersPrecedeRankingAndPagination(t *testing.T) {
	ctx := boxContext("box_company")
	queries := 0
	store := &Store{q: queryAdapter{queryRow: func(context.Context, string, ...any) pgx.Row { return projectProjectionValues("membership") }, query: func(_ context.Context, query string, args ...any) (pgx.Rows, error) {
		queries++
		if !reflect.DeepEqual(args[len(args)-1], []string{"box_company"}) {
			t.Fatal(args)
		}
		predicate := strings.Index(query, "::text[] IS NULL")
		boundary := strings.Index(query, "), ranked AS")
		if boundary < 0 {
			boundary = strings.LastIndex(query, "LIMIT")
		}
		if predicate < 0 || predicate > boundary {
			t.Fatal("late Box filter", query)
		}
		return &reviewTestRows{}, nil
	}}}
	for _, query := range []func(context.Context, []string, string, string, searchQueryCursor, int) ([]searchQueryRow, bool, error){store.searchAcceptedRecords, store.searchPendingCandidates, store.searchUnresolvedCases, store.searchProjectState, store.searchRepositoryState} {
		if _, _, err := query(ctx, []string{"same"}, "", "", searchQueryCursor{}, 1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.ListCandidates(ctx, PageRequest{Limit: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListRecords(ctx, PageRequest{Limit: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListResolutionCases(ctx, PageRequest{Limit: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListRelationships(ctx, PageRequest{Limit: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.listReview(ctx, ReviewRequest{Limit: 1}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if queries != 10 {
		t.Fatal(queries)
	}
}

func TestBoxMembershipReassociationInvalidatesCursor(t *testing.T) {
	membership := "initial"
	store := &Store{q: queryAdapter{queryRow: func(_ context.Context, query string, args ...any) pgx.Row {
		if query != boxMembershipSQL || !reflect.DeepEqual(args[0], []string{"box_company"}) {
			t.Fatal(query, args)
		}
		return projectProjectionValues(membership)
	}}}
	request := SearchRequest{Query: "same decision"}
	ctx, err := store.boxSelectionContext(boxContext("box_company"))
	if err != nil {
		t.Fatal(err)
	}
	initial, err := normalizeSearchRequestContext(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	request.Cursor, err = encodeSearchCursor(initial.Digest, searchQueryRow{ID: string(semanticID(1)), Score: 1, SortTime: time.Now(), CollectionPriority: 0})
	if err != nil {
		t.Fatal(err)
	}
	membership = "reassigned"
	ctx, err = store.boxSelectionContext(boxContext("box_company"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := normalizeSearchRequestContext(ctx, request); err == nil {
		t.Fatal("old membership cursor survived reassociation")
	}
}

func TestBoxProjectProjectionPreservesVerifiedMembership(t *testing.T) {
	input := projectionTestSyncInput()
	input.HomeBoxID = "box_company"
	snapshot, repositories, err := prepareProjectProjectionSync(input, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	project, err := decodeProjectContext(snapshot.ProjectionJSON, snapshot.ProjectID)
	if err != nil || project.HomeBoxID != "box_company" {
		t.Fatal(project, err)
	}
	for _, repository := range repositories {
		var card RepositoryCard
		if err := json.Unmarshal(repository.CardJSON, &card); err != nil || card.OwningProject.HomeBoxID != "box_company" {
			t.Fatal(card, err)
		}
	}
	input.HomeBoxID = ""
	legacy, _, err := prepareProjectProjectionSync(input, time.Now())
	if err != nil || legacy.SnapshotDigest == snapshot.SnapshotDigest {
		t.Fatal("membership not in projection digest", err)
	}
	cloned := cloneArchivistAnchors(&StructuralAnchors{Boxes: []string{"box_company"}})
	if !reflect.DeepEqual(cloned.Boxes, []string{"box_company"}) {
		t.Fatal(cloned)
	}
}
