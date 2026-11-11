package provenance

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestReviewValidationAndUnavailableStore(t *testing.T) {
	valid := ReviewCheckpoint{ObjectKind: "case", ObjectID: semanticID(1), ChangeToken: strings.Repeat("a", 32)}
	for _, request := range []ReviewRequest{
		{Limit: 101}, {Limit: -1}, {Cursor: "bad"}, {Domain: strings.Repeat("x", 257)},
		{ExcludeDomains: make([]string, 33)}, {ExcludeDomains: []string{""}},
		{Reviewed: make([]ReviewCheckpoint, 1001)},
		{Reviewed: []ReviewCheckpoint{{ObjectKind: "record", ObjectID: valid.ObjectID, ChangeToken: valid.ChangeToken}}},
		{Reviewed: []ReviewCheckpoint{{ObjectKind: "case", ObjectID: "bad", ChangeToken: valid.ChangeToken}}},
		{Reviewed: []ReviewCheckpoint{{ObjectKind: "case", ObjectID: valid.ObjectID, ChangeToken: "not-returned"}}},
	} {
		if err := ValidateReviewRequest(request); err == nil {
			t.Fatalf("invalid request accepted: %#v", request)
		}
		_, err := (&FoundationAPI{}).ListReview(context.Background(), request)
		var validation *FoundationValidationError
		if !errors.As(err, &validation) {
			t.Fatalf("validation error=%v", err)
		}
	}
	if err := ValidateReviewRequest(ReviewRequest{Reviewed: []ReviewCheckpoint{valid}, ExcludeDomains: []string{"fixture", "fixture-real-project"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&FoundationAPI{}).ListReview(context.Background(), ReviewRequest{Limit: 10}); err == nil {
		t.Fatal("missing store returned empty work")
	}
	for _, raw := range []string{`{}`, `{"as_of":"2026-10-12T00:00:00Z","changed_at":"2026-10-13T00:00:00Z","kind":"case","id":"` + string(valid.ObjectID) + `"}`, `{"unexpected":true}`} {
		if _, err := decodeReviewCursor(base64.RawURLEncoding.EncodeToString([]byte(raw))); err == nil {
			t.Fatalf("bad cursor accepted: %s", raw)
		}
	}
}

type reviewTestRows struct {
	pgx.Rows
	items []ReviewItem
	index int
	err   error
}

func (rows *reviewTestRows) Next() bool { rows.index++; return rows.index <= len(rows.items) }
func (rows *reviewTestRows) Close()     {}
func (rows *reviewTestRows) Err() error { return rows.err }
func (rows *reviewTestRows) Scan(dest ...any) error {
	item := rows.items[rows.index-1]
	*dest[0].(*string) = item.ObjectKind
	*dest[1].(*string) = string(item.ObjectID)
	*dest[2].(*string) = item.EffectiveState
	*dest[3].(*string) = item.ReviewReason
	*dest[4].(*string) = item.ChangeToken
	*dest[5].(*time.Time) = item.ChangedAt
	*dest[6].(*string) = item.Domain
	*dest[7].(*string) = item.Visibility
	return nil
}

func TestReviewBoundedSingleQueryAndCursor(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	items := []ReviewItem{
		{ObjectKind: "candidate", ObjectID: semanticID(1), EffectiveState: "pending", ReviewReason: "deferral_due", ChangeToken: strings.Repeat("a", 32), ChangedAt: now.Add(-time.Hour), Domain: "real", Visibility: "private"},
		{ObjectKind: "case", ObjectID: semanticID(2), EffectiveState: "open", ReviewReason: "changed_evidence", ChangeToken: strings.Repeat("b", 32), ChangedAt: now.Add(-time.Minute), Domain: "real", Visibility: "private"},
	}
	calls := 0
	store := &Store{q: queryAdapter{query: func(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
		calls++
		if sql != reviewSQL || len(args) != 10 || args[8] != 2 {
			t.Fatalf("unbounded query: %v", args)
		}
		if calls == 1 {
			return &reviewTestRows{items: items}, nil
		}
		if args[0] != now || args[6] != "candidate" || args[7] != string(items[0].ObjectID) || *args[5].(*time.Time) != items[0].ChangedAt {
			t.Fatalf("cursor lost effective order/as-of: %v", args)
		}
		return &reviewTestRows{items: items[1:]}, nil
	}}}
	first, err := store.listReview(context.Background(), ReviewRequest{Limit: 1}, now)
	if err != nil || len(first.Items) != 1 || !first.Truncated || first.NextCursor == "" || calls != 1 {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	if err := ValidateReviewRequest(ReviewRequest{Cursor: first.NextCursor}); err != nil {
		t.Fatal(err)
	}
	second, err := store.listReview(context.Background(), ReviewRequest{Limit: 1, Cursor: first.NextCursor}, now.Add(time.Hour))
	if err != nil || len(second.Items) != 1 || second.Truncated || second.NextCursor != "" || calls != 2 || second.AsOf != now {
		t.Fatalf("second=%#v err=%v", second, err)
	}
	encoded, _ := json.Marshal(first)
	for _, forbidden := range []string{"submitted", "payload_json", "claim", "source_context"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("body dump leaked %s", forbidden)
		}
	}
	store.q.query = func(context.Context, string, ...any) (pgx.Rows, error) {
		return &reviewTestRows{err: errors.New("read failed")}, nil
	}
	if _, err := store.listReview(context.Background(), ReviewRequest{Limit: 10}, now); err == nil {
		t.Fatal("failed read became no-work")
	}
}

func TestReviewEffectiveSelectionPostgres(t *testing.T) {
	_, store, _ := migratedStore(t, "effective_review")
	ctx := context.Background()
	fixture := loadStoreContractFixture(t)
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	newCandidate := func(n int, domain string) SemanticID {
		item := fixture.Candidates[0]
		item.ID, item.Domain, item.State, item.RegisteredAt = semanticID(n), domain, "pending", now.Add(-24*time.Hour)
		if err := store.AppendCandidate(ctx, item); err != nil {
			t.Fatal(err)
		}
		return item.ID
	}
	newOperation := func(n int, at time.Time) SemanticID {
		op := fixture.Operation
		op.ID, op.ProcessingRunID, op.PackID, op.OccurredAt, op.AppliedAt = semanticID(n), nil, nil, at, at
		if err := store.AppendOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
		return op.ID
	}
	deferCandidate := func(id SemanticID, n int, due *time.Time) {
		at := now.Add(-time.Hour)
		payload, _ := json.Marshal(map[string]any{"next_review_at": due, "reason_code": "needs_human", "reason": "Awaiting source-backed clarification"})
		if err := store.AppendCandidateEvent(ctx, LedgerEvent{ObjectID: id, EventType: "deferred", OperationID: newOperation(n, at), OccurredAt: at, Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	unreviewed := newCandidate(30001, "real")
	accepted := newCandidate(30002, "real")
	for i, kind := range []string{"accepted", "consolidated", "rejected"} {
		id := accepted
		if i > 0 {
			id = newCandidate(30002+i, "real")
		}
		if err := store.AppendCandidateEvent(ctx, LedgerEvent{ObjectID: id, EventType: kind, OperationID: newOperation(31002+i, now.Add(-time.Hour)), OccurredAt: now.Add(-time.Hour), Payload: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	future := newCandidate(30005, "real")
	futureDue := now.Add(time.Hour)
	deferCandidate(future, 31005, &futureDue)
	due := newCandidate(30006, "real")
	pastDue := now.Add(-time.Minute)
	deferCandidate(due, 31006, &pastDue)
	blocked := newCandidate(30007, "real")
	deferCandidate(blocked, 31007, nil)
	fixtureID := newCandidate(30008, "scheduled-fixture")
	realSimilar := newCandidate(30009, "scheduled-fixture-real")
	newCase := func(n int) SemanticID {
		item := fixture.ResolutionCase
		item.ID, item.Domain, item.InitialStatus, item.CreatedAt, item.CreatedByOperationID = semanticID(n), "real", "open", now.Add(-24*time.Hour), nil
		if err := store.AppendResolutionCase(ctx, item); err != nil {
			t.Fatal(err)
		}
		return item.ID
	}
	caseEvent := func(caseID SemanticID, n int, kind string, outcome *string, at time.Time) {
		e := fixture.CaseEvent
		opID := newOperation(n+1000, at)
		e.ID, e.CaseID, e.EventType, e.Outcome, e.OccurredAt, e.OperationID, e.Payload = semanticID(n), caseID, kind, outcome, at, &opID, json.RawMessage(`{}`)
		if err := store.AppendCaseEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	resolved := newCase(32001)
	closed := "resolved"
	caseEvent(resolved, 33001, "closed", &closed, now.Add(-time.Hour))
	humanBlocked := newCase(32002)
	awaitingHuman := "awaiting_human"
	caseEvent(humanBlocked, 33002, "review_deferred", &awaitingHuman, now.Add(-time.Hour))
	changedCase := newCase(32003)
	caseEvent(changedCase, 33003, "review_deferred", nil, now.Add(-time.Hour))
	caseEvent(changedCase, 33004, "clarification_added", nil, now.Add(-time.Minute))
	futureCase := newCase(32004)
	caseEvent(futureCase, 33005, "review_deferred", nil, now.Add(-time.Hour))
	if err := store.AppendCaseMember(ctx, CaseMember{CaseID: futureCase, MemberType: "candidate", CandidateID: &future, Role: "subject", AttachedAt: now.Add(-2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	request := ReviewRequest{Limit: 100, ExcludeDomains: []string{"scheduled-fixture"}}
	page, err := store.listReview(ctx, request, now)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[SemanticID]ReviewItem)
	for _, item := range page.Items {
		byID[item.ObjectID] = item
	}
	for _, id := range []SemanticID{accepted, semanticID(30003), semanticID(30004), future, blocked, fixtureID, resolved, humanBlocked, futureCase} {
		if _, ok := byID[id]; ok {
			t.Fatalf("terminal/future/unchanged item included: %s %#v", id, byID[id])
		}
	}
	for _, id := range []SemanticID{unreviewed, due, realSimilar, changedCase} {
		if _, ok := byID[id]; !ok {
			t.Fatalf("eligible item lost: %s", id)
		}
	}
	if byID[due].ReviewReason != "deferral_due" || byID[changedCase].ReviewReason != "changed_evidence" {
		t.Fatalf("wrong reasons: %#v", page)
	}
	// Checkpoint filtering occurs before LIMIT; due time does not change the token every sweep.
	for _, item := range page.Items {
		request.Reviewed = append(request.Reviewed, ReviewCheckpoint{ObjectKind: item.ObjectKind, ObjectID: item.ObjectID, ChangeToken: item.ChangeToken})
	}
	empty, err := store.listReview(ctx, request, now.Add(time.Minute))
	if err != nil || len(empty.Items) != 0 || empty.Truncated {
		t.Fatalf("unchanged checkpoints=%#v %v", empty, err)
	}
	// Independent durable evidence registration can unblock a case without a case-event mutation.
	source := fixture.Sources[0]
	source.ID, source.CandidateID, source.ResolutionAt = semanticID(35001), nil, now.Add(time.Minute)
	if err := store.AppendSourceReference(ctx, source); err != nil {
		t.Fatal(err)
	}
	evidence := fixture.EvidenceRegistration
	evidence.ID, evidence.SourceReferenceID, evidence.Domain, evidence.ResolutionCaseID, evidence.RegisteredAt = semanticID(35002), source.ID, "real", &humanBlocked, now.Add(time.Minute)
	if err := store.AppendEvidenceRegistration(ctx, evidence); err != nil {
		t.Fatal(err)
	}
	changed, err := store.listReview(ctx, request, now.Add(2*time.Minute))
	if err != nil || len(changed.Items) != 1 || changed.Items[0].ObjectID != humanBlocked || changed.Items[0].ReviewReason != "changed_evidence" || changed.Items[0].EffectiveState != awaitingHuman {
		t.Fatalf("new registered evidence lost=%#v %v", changed, err)
	}
	request.Reviewed = append(request.Reviewed, ReviewCheckpoint{ObjectKind: "case", ObjectID: humanBlocked, ChangeToken: changed.Items[0].ChangeToken})
	link := fixture.CandidateEvidenceLink
	link.ID, link.CandidateID, link.SourceReferenceID, link.OperationID, link.LinkedAt = semanticID(36001), blocked, source.ID, newOperation(36002, now.Add(3*time.Minute)), now.Add(3*time.Minute)
	if err := store.AppendCandidateEvidenceLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	linked, err := store.listReview(ctx, request, now.Add(4*time.Minute))
	if err != nil || len(linked.Items) != 1 || linked.Items[0].ObjectID != blocked || linked.Items[0].ReviewReason != "changed_evidence" {
		t.Fatalf("new candidate evidence lost=%#v %v", linked, err)
	}
	// Restarting traversal discovers a deferral registered long before the first page.
	request.Limit = 1
	late, err := store.listReview(ctx, request, now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	foundFuture := false
	foundFutureCase := false
	for {
		for _, item := range late.Items {
			if item.ObjectID == future {
				foundFuture = true
			}
			if item.ObjectID == futureCase && item.ReviewReason == "deferral_due" {
				foundFutureCase = true
			}
		}
		if !late.Truncated {
			break
		}
		request.Cursor = late.NextCursor
		late, err = store.listReview(ctx, request, now.Add(2*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
	}
	if !foundFuture || !foundFutureCase {
		t.Fatal("older due deferral stranded behind registration cursor")
	}
}
