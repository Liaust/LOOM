package provenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

type archivistFakeTransport struct {
	FoundationTransport
	SearchTransport
	readiness  RuntimeReadiness
	candidates map[SemanticID]CandidateLifecycleProjection
	records    map[SemanticID]RecordLifecycleProjection
	cases      map[SemanticID]ResolutionCaseLifecycleProjection
	list       []CandidateSummary
	search     SearchResponse
	searchErr  error
	applyCalls []ManualOperationsRequest
	seen       map[string]ManualOperationBatchReceipt
}

func newArchivistFakeTransport() *archivistFakeTransport {
	return &archivistFakeTransport{
		readiness:  RuntimeReadiness{State: ReadinessReady, Code: ReadinessCodeReady},
		candidates: map[SemanticID]CandidateLifecycleProjection{}, records: map[SemanticID]RecordLifecycleProjection{},
		cases: map[SemanticID]ResolutionCaseLifecycleProjection{}, seen: map[string]ManualOperationBatchReceipt{},
	}
}

func (fake *archivistFakeTransport) Readiness() RuntimeReadiness { return fake.readiness }

func (fake *archivistFakeTransport) ListCandidates(_ context.Context, request PageRequest) (Page[CandidateSummary], error) {
	items := append([]CandidateSummary(nil), fake.list...)
	if request.AfterTime != nil && request.AfterID != nil {
		filtered := items[:0]
		for _, item := range items {
			if item.RegisteredAt.After(*request.AfterTime) || (item.RegisteredAt.Equal(*request.AfterTime) && item.ID > *request.AfterID) {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	if len(items) <= request.Limit {
		return Page[CandidateSummary]{Items: items}, nil
	}
	selected := items[:request.Limit]
	last := selected[len(selected)-1]
	return Page[CandidateSummary]{Items: selected, NextTime: &last.RegisteredAt, NextID: &last.ID, Truncated: true}, nil
}

func (fake *archivistFakeTransport) GetCandidate(_ context.Context, id SemanticID, _ int) (CandidateLifecycleProjection, error) {
	item, found := fake.candidates[id]
	if !found {
		return CandidateLifecycleProjection{}, &FoundationNotFoundError{Kind: "candidate", ID: id}
	}
	return item, nil
}

func (fake *archivistFakeTransport) GetRecord(_ context.Context, id SemanticID, _ int) (RecordLifecycleProjection, error) {
	item, found := fake.records[id]
	if !found {
		return RecordLifecycleProjection{}, &FoundationNotFoundError{Kind: "record", ID: id}
	}
	return item, nil
}

func (fake *archivistFakeTransport) GetResolutionCase(_ context.Context, id SemanticID, _ int) (ResolutionCaseLifecycleProjection, error) {
	item, found := fake.cases[id]
	if !found {
		return ResolutionCaseLifecycleProjection{}, &FoundationNotFoundError{Kind: "case", ID: id}
	}
	return item, nil
}

func (fake *archivistFakeTransport) Search(context.Context, SearchRequest) (SearchResponse, error) {
	return fake.search, fake.searchErr
}

func (fake *archivistFakeTransport) ApplyManualOperations(_ context.Context, request ManualOperationsRequest) (ManualOperationBatchReceipt, error) {
	fake.applyCalls = append(fake.applyCalls, request)
	keyBytes, _ := json.Marshal(request)
	key := string(keyBytes)
	if receipt, found := fake.seen[key]; found {
		for index := range receipt.Receipts {
			receipt.Receipts[index].Replayed = true
		}
		receipt.Replayed = true
		return receipt, nil
	}
	receipt := ManualOperationBatchReceipt{SchemaVersion: SchemaVersion, Result: "applied"}
	for _, document := range request.Operations {
		var header struct {
			OperationType string     `json:"operation_type"`
			OperationID   SemanticID `json:"operation_id"`
			CandidateID   SemanticID `json:"candidate_id"`
			RecordID      SemanticID `json:"record_id"`
			Case          struct {
				ID SemanticID `json:"case_id"`
			} `json:"case"`
			Events []CaseEvent `json:"events"`
		}
		if err := json.Unmarshal(document, &header); err != nil {
			return ManualOperationBatchReceipt{}, err
		}
		objectID := header.CandidateID
		if objectID == "" {
			objectID = header.RecordID
		}
		if objectID == "" {
			objectID = header.Case.ID
		}
		receipt.Receipts = append(receipt.Receipts, OperationReceipt{
			SchemaVersion: SchemaVersion, OperationID: header.OperationID, OperationType: header.OperationType, Result: "applied", ObjectID: objectID,
		})
		if header.OperationType == "create_resolution_case" {
			events := append([]CaseEvent(nil), header.Events...)
			for index := range events {
				events[index].OperationID = &header.OperationID
			}
			createdBy := header.OperationID
			fake.cases[header.Case.ID] = ResolutionCaseLifecycleProjection{
				SchemaVersion: SchemaVersion, Case: ResolutionCase{ID: header.Case.ID, CreatedByOperationID: &createdBy}, EffectiveState: "open", Events: events,
			}
		}
	}
	fake.seen[key] = receipt
	return receipt, nil
}

func TestArchivistFrozenPolicyAcceptsOnlyExactUserStatement(t *testing.T) {
	fake := newArchivistFakeTransport()
	projection := strictArchivistCandidate(t, "11111111-1111-4111-8111-111111111111", "Keep the Archivist manual-first.")
	fake.candidates[projection.Candidate.ID] = projection
	archivist, err := NewArchivist(fake)
	if err != nil {
		t.Fatal(err)
	}
	result, err := archivist.Run(context.Background(), archivistRequest("accept-exact", projection.Candidate.ID))
	if err != nil {
		t.Fatal(err)
	}
	if result.Accepted != 1 || result.Deferred != 0 || len(fake.applyCalls) != 1 {
		t.Fatalf("result=%#v calls=%d", result, len(fake.applyCalls))
	}
	if got := operationTypes(fake.applyCalls[0]); !reflect.DeepEqual(got, []string{"accept_candidate"}) {
		t.Fatalf("operation types=%#v", got)
	}
	if strings.Contains(string(fake.applyCalls[0].Operations[0]), "source_context") {
		t.Fatal("worker operation unexpectedly copied raw source context")
	}
	if strings.Contains(string(mustTestJSON(t, result)), projection.Candidate.Claim) {
		t.Fatal("bounded worker result unexpectedly emitted the candidate claim")
	}
}

func TestArchivistDefersAgentInterpretationPartialEvidenceAndConflict(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*CandidateLifecycleProjection, *archivistFakeTransport)
		reasonCode string
	}{
		{
			name: "agent interpretation",
			mutate: func(item *CandidateLifecycleProjection, _ *archivistFakeTransport) {
				var envelope archivistCandidateEnvelope
				mustDecodeJSON(t, item.Candidate.Payload, &envelope)
				envelope.Registration.AssertionPosture = "agent_interpretation"
				item.Candidate.AssertionPosture = envelope.Registration.AssertionPosture
				item.Candidate.Payload = mustTestJSON(t, envelope)
				item.Candidate.Submitted = mustTestJSON(t, envelope.Registration)
			},
			reasonCode: "agent_interpretation",
		},
		{
			name: "derived interpretation",
			mutate: func(item *CandidateLifecycleProjection, _ *archivistFakeTransport) {
				var envelope archivistCandidateEnvelope
				mustDecodeJSON(t, item.Candidate.Payload, &envelope)
				envelope.Registration.DerivedFromCandidateIDs = []SemanticID{"44444444-4444-4444-8444-444444444444"}
				item.Candidate.Payload = mustTestJSON(t, envelope)
				item.Candidate.Submitted = mustTestJSON(t, envelope.Registration)
			},
			reasonCode: "agent_interpretation",
		},
		{
			name: "partial evidence",
			mutate: func(item *CandidateLifecycleProjection, _ *archivistFakeTransport) {
				item.SourceReferenceIDs = append(item.SourceReferenceIDs, SemanticID("22222222-2222-4222-8222-222222222222"))
			},
			reasonCode: "partial_evidence",
		},
		{
			name: "related pending conflict",
			mutate: func(item *CandidateLifecycleProjection, fake *archivistFakeTransport) {
				conflict := strictArchivistCandidate(t, "33333333-3333-4333-8333-333333333333", item.Candidate.Claim)
				fake.candidates[conflict.Candidate.ID] = conflict
				fake.search.PendingCandidates.Items = []PendingCandidateSearchResult{{CandidateID: conflict.Candidate.ID}}
			},
			reasonCode: "material_conflict",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := newArchivistFakeTransport()
			projection := strictArchivistCandidate(t, "11111111-1111-4111-8111-111111111111", "Use one exact provenance service.")
			test.mutate(&projection, fake)
			fake.candidates[projection.Candidate.ID] = projection
			archivist, _ := NewArchivist(fake)
			result, err := archivist.Run(context.Background(), archivistRequest("defer-"+test.name, projection.Candidate.ID))
			if err != nil {
				t.Fatal(err)
			}
			if result.Deferred != 1 || len(result.Outcomes) != 1 || result.Outcomes[0].ReasonCode != test.reasonCode {
				t.Fatalf("result=%#v", result)
			}
			if result.ExactReads < 2 {
				t.Fatalf("exact read counter omitted candidate or case evidence: %#v", result)
			}
			if got := operationTypes(fake.applyCalls[0]); !reflect.DeepEqual(got, []string{"defer_candidate", "create_resolution_case"}) {
				t.Fatalf("operation types=%#v", got)
			}
		})
	}
}

func TestArchivistExplicitOutcomesUseOnlyFoundationOperations(t *testing.T) {
	tests := []struct {
		outcome ArchivistOutcome
		want    []string
	}{
		{ArchivistOutcomeAccept, []string{"accept_candidate"}},
		{ArchivistOutcomeConsolidate, []string{"consolidate_candidate"}},
		{ArchivistOutcomeSupersede, []string{"accept_candidate", "add_relationship"}},
		{ArchivistOutcomeCorrect, []string{"accept_candidate", "add_relationship"}},
		{ArchivistOutcomeRefine, []string{"accept_candidate", "add_relationship"}},
		{ArchivistOutcomeReject, []string{"reject_candidate"}},
		{ArchivistOutcomeDefer, []string{"defer_candidate", "create_resolution_case"}},
	}
	for _, test := range tests {
		t.Run(string(test.outcome), func(t *testing.T) {
			fake := newArchivistFakeTransport()
			candidate := strictArchivistCandidate(t, "11111111-1111-4111-8111-111111111111", "Keep the worker manual-first.")
			fake.candidates[candidate.Candidate.ID] = candidate
			decision := ArchivistDecision{CandidateID: candidate.Candidate.ID, Outcome: test.outcome, Reason: "explicit bounded review"}
			if requiresArchivistTarget(test.outcome) {
				targetID := SemanticID("22222222-2222-4222-8222-222222222222")
				decision.TargetRecordID = &targetID
				target := archivistTargetRecord(targetID, candidate)
				fake.records[targetID] = RecordLifecycleProjection{SchemaVersion: SchemaVersion, Record: target}
				fake.search.AcceptedRecords.Items = []AcceptedRecordSearchResult{{RecordID: targetID}}
			}
			request := archivistRequest("explicit-"+string(test.outcome), candidate.Candidate.ID)
			request.Decisions = []ArchivistDecision{decision}
			archivist, _ := NewArchivist(fake)
			if _, err := archivist.Run(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			if got := operationTypes(fake.applyCalls[0]); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("operation types=%#v want=%#v", got, test.want)
			}
		})
	}
}

func TestArchivistIncompatibleRelationshipDefersAndOpensCase(t *testing.T) {
	fake := newArchivistFakeTransport()
	candidate := strictArchivistCandidate(t, "11111111-1111-4111-8111-111111111111", "Correct the retained policy.")
	fake.candidates[candidate.Candidate.ID] = candidate
	targetID := SemanticID("22222222-2222-4222-8222-222222222222")
	target := archivistTargetRecord(targetID, candidate)
	target.Anchors.Entities = []string{"different-entity"}
	fake.records[targetID] = RecordLifecycleProjection{SchemaVersion: SchemaVersion, Record: target}
	request := archivistRequest("incompatible", candidate.Candidate.ID)
	request.Decisions = []ArchivistDecision{{CandidateID: candidate.Candidate.ID, Outcome: ArchivistOutcomeCorrect, TargetRecordID: &targetID}}
	archivist, _ := NewArchivist(fake)
	result, err := archivist.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deferred != 1 || result.Outcomes[0].ReasonCode != "incompatible_relationship" {
		t.Fatalf("result=%#v", result)
	}
	if got := operationTypes(fake.applyCalls[0]); !reflect.DeepEqual(got, []string{"defer_candidate", "create_resolution_case"}) {
		t.Fatalf("operation types=%#v", got)
	}
}

func TestArchivistCrashLeaseLossDuplicateAndDeterministicRetry(t *testing.T) {
	newFixture := func(t *testing.T) (*Archivist, *archivistFakeTransport, CandidateLifecycleProjection, ArchivistRunRequest) {
		fake := newArchivistFakeTransport()
		candidate := strictArchivistCandidate(t, "11111111-1111-4111-8111-111111111111", "Use deterministic retries.")
		fake.candidates[candidate.Candidate.ID] = candidate
		archivist, _ := NewArchivist(fake)
		return archivist, fake, candidate, archivistRequest("deterministic-retry", candidate.Candidate.ID)
	}

	t.Run("crash before commit", func(t *testing.T) {
		archivist, fake, _, request := newFixture(t)
		archivist.beforeCommit = func(context.Context, SemanticID) error { return errors.New("crash before commit") }
		if _, err := archivist.Run(context.Background(), request); err == nil || len(fake.applyCalls) != 0 {
			t.Fatalf("error=%v calls=%d", err, len(fake.applyCalls))
		}
	})

	t.Run("crash after commit and duplicate retry", func(t *testing.T) {
		archivist, fake, candidate, request := newFixture(t)
		archivist.afterCommit = func(context.Context, SemanticID) error { return errors.New("crash after commit") }
		if _, err := archivist.Run(context.Background(), request); err == nil || len(fake.applyCalls) != 1 {
			t.Fatalf("error=%v calls=%d", err, len(fake.applyCalls))
		}
		first := operationIDs(fake.applyCalls[0])
		archivist.afterCommit = nil
		result, err := archivist.Run(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if result.Replayed != 1 || len(fake.applyCalls) != 2 || !reflect.DeepEqual(first, operationIDs(fake.applyCalls[1])) {
			t.Fatalf("result=%#v ids=%#v/%#v", result, first, operationIDs(fake.applyCalls[1]))
		}
		if fake.candidates[candidate.Candidate.ID].EffectiveState != "pending" {
			t.Fatal("fake fixture unexpectedly hid deterministic operation replay")
		}
	})

	t.Run("lease lost before commit", func(t *testing.T) {
		archivist, fake, _, request := newFixture(t)
		request.LeaseGuard = func(context.Context) error { return errors.New("lost") }
		if _, err := archivist.Run(context.Background(), request); err == nil || len(fake.applyCalls) != 0 {
			t.Fatalf("error=%v calls=%d", err, len(fake.applyCalls))
		}
	})

	t.Run("lease lost after commit", func(t *testing.T) {
		archivist, fake, _, request := newFixture(t)
		checks := 0
		request.LeaseGuard = func(context.Context) error {
			checks++
			if checks >= 4 {
				return errors.New("lost after commit")
			}
			return nil
		}
		if _, err := archivist.Run(context.Background(), request); err == nil || len(fake.applyCalls) != 1 {
			t.Fatalf("error=%v checks=%d calls=%d", err, checks, len(fake.applyCalls))
		}
	})
}

func TestArchivistStaleCandidateAndBoundedCursor(t *testing.T) {
	fake := newArchivistFakeTransport()
	base := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	for index := 0; index < 3; index++ {
		id := SemanticID(fmt.Sprintf("11111111-1111-4111-8111-%012d", index+1))
		item := strictArchivistCandidate(t, id, fmt.Sprintf("Bounded candidate %d", index+1))
		item.Candidate.RegisteredAt = base.Add(time.Duration(index) * time.Second)
		var envelope archivistCandidateEnvelope
		mustDecodeJSON(t, item.Candidate.Payload, &envelope)
		envelope.RegisteredAt = item.Candidate.RegisteredAt
		item.Candidate.Payload = mustTestJSON(t, envelope)
		fake.candidates[id] = item
		fake.list = append(fake.list, CandidateSummary{ID: id, RegisteredAt: item.Candidate.RegisteredAt})
	}
	stale := fake.candidates["11111111-1111-4111-8111-000000000001"]
	stale.EffectiveState = "accepted"
	fake.candidates[stale.Candidate.ID] = stale
	archivist, _ := NewArchivist(fake)
	request := archivistRequest("bounded-page", "")
	request.CandidateIDs = nil
	request.CandidateLimit = 2
	result, err := archivist.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Selected != 2 || result.Stale != 1 || result.NextCursor == nil || result.CycleComplete {
		t.Fatalf("result=%#v", result)
	}
}

func TestArchivistCrashAfterCommitDeterministicReplayPostgres(t *testing.T) {
	_, store, _ := migratedStore(t, "archivist_replay")
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	api := &FoundationAPI{store: store, service: service, runtime: &Runtime{readiness: RuntimeReadiness{State: ReadinessReady, Code: ReadinessCodeReady}}}
	registration := strictArchivistRegistration("A source gap must defer.")
	registration.SourceActor.ActorKind = "agent"
	receipt, err := service.RegisterCandidate(context.Background(), "archivist-pg-registration", registration)
	if err != nil {
		t.Fatal(err)
	}
	archivist, _ := NewArchivist(api)
	archivist.afterCommit = func(context.Context, SemanticID) error { return errors.New("simulated crash after commit") }
	request := ArchivistRunRequest{
		CandidateLimit: 1, EvidenceLimit: 4, CandidateIDs: []SemanticID{receipt.CandidateID},
		IdempotencyKey: "archivist-pg-retry", Producer: archivistProducer(), LeaseGuard: allowArchivistLease,
	}
	if _, err := archivist.Run(context.Background(), request); err == nil {
		t.Fatal("crash after commit did not surface")
	}
	archivist.afterCommit = nil
	result, err := archivist.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Deferred != 1 || result.Replayed != 1 {
		t.Fatalf("replay result=%#v", result)
	}
	var operations, cases, events int
	if err := store.q.queryRow(context.Background(), `SELECT count(*) FROM provenance.operation_history`).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if err := store.q.queryRow(context.Background(), `SELECT count(*) FROM provenance.resolution_cases`).Scan(&cases); err != nil {
		t.Fatal(err)
	}
	if err := store.q.queryRow(context.Background(), `SELECT count(*) FROM provenance.case_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if operations != 2 || cases != 1 || events != 1 {
		t.Fatalf("operations=%d cases=%d events=%d", operations, cases, events)
	}
}

func strictArchivistCandidate(t *testing.T, id SemanticID, claim string) CandidateLifecycleProjection {
	t.Helper()
	registration := strictArchivistRegistration(claim)
	sourceID := registration.Sources[0].SourceReferenceID
	registeredAt := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	locator := "codex-current-thread:verified-user-message"
	source := SourceReference{
		ID: sourceID, CandidateID: &id, SchemaVersion: SchemaVersion, SourceKind: "codex_current_thread",
		Status: "resolved", VerificationPosture: "content_verified", ResolverName: "codex-current-thread", ResolverVersion: "1.0",
		ResolutionAt: registeredAt, CanonicalLocator: &locator, ContentDigest: stringPointer("sha256:exact"), SourceContext: stringPointer(claim),
		Submitted: registration.Sources[0].Submitted, Payload: json.RawMessage(`{}`),
	}
	envelope := archivistCandidateEnvelope{
		SchemaVersion: SchemaVersion, CandidateID: id, Registration: registration,
		SourceResults: []SourceReference{source}, RegisteredAt: registeredAt, State: "pending",
	}
	return CandidateLifecycleProjection{
		SchemaVersion: SchemaVersion, Candidate: Candidate{
			ID: id, SchemaVersion: SchemaVersion, State: "pending", Domain: registration.Domain, Visibility: registration.Visibility,
			RecordKind: registration.RecordKind, Claim: registration.Claim, RecordContext: registration.RecordContext,
			AssertionPosture: registration.AssertionPosture, ProducerID: registration.Producer.ProducerID,
			RegisteredAt: registeredAt, Submitted: mustTestJSON(t, registration), Payload: mustTestJSON(t, envelope),
		}, EffectiveState: "pending", SourceReferenceIDs: []SemanticID{sourceID},
	}
}

func strictArchivistRegistration(claim string) CandidateRegistration {
	sourceID := SemanticID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	observed := time.Date(2026, 8, 31, 9, 59, 0, 0, time.UTC)
	return CandidateRegistration{
		SchemaVersion: SchemaVersion, Claim: claim, RecordKind: "decision",
		RecordContext: "Explicit user decision in the current task.", Domain: "loom-development", Visibility: "private",
		Sources: []SourceRegistration{{
			SourceReferenceID: sourceID, Kind: "codex_current_thread", Status: "resolved", Verification: "content_verified",
			ResolverName: "codex-current-thread", ResolverVersion: "1.0", ResolutionAt: observed,
			CanonicalLocator: stringPointer("codex-current-thread:verified-user-message"),
			ContentDigest:    stringPointer("sha256:exact"), SourceContext: stringPointer(claim),
			Submitted: json.RawMessage(`{"kind":"codex_current_thread","evidence":{"excerpt":"explicit user statement"}}`),
		}},
		Temporal:         TemporalInterpretation{Interpretation: "Current explicit decision.", ObservedAt: &observed},
		AssertionPosture: "explicit_user_statement", Producer: ProducerIdentity{ProducerID: "registrar", ProducerKind: "working_agent"},
		SourceActor: &ActorReference{ActorID: "user-leonardo", ActorKind: "user", SourceReferenceID: &sourceID},
		Anchors:     &StructuralAnchors{Entities: []string{"loom.provenance.archivist"}, Projects: []string{"LOOM"}},
	}
}

func archivistTargetRecord(id SemanticID, candidate CandidateLifecycleProjection) Record {
	var registration CandidateRegistration
	_ = json.Unmarshal(candidate.Candidate.Submitted, &registration)
	return Record{
		ID: id, SchemaVersion: SchemaVersion, Claim: registration.Claim, RecordKind: registration.RecordKind,
		RecordContext: "Existing exact accepted record.", Domain: registration.Domain, Visibility: registration.Visibility,
		AssertionPosture: "accepted", Temporal: registration.Temporal, Anchors: cloneArchivistAnchors(registration.Anchors),
		CreatedAt: candidate.Candidate.RegisteredAt.Add(-time.Hour), Payload: json.RawMessage(`{}`),
	}
}

func archivistRequest(key string, candidateID SemanticID) ArchivistRunRequest {
	request := ArchivistRunRequest{
		CandidateLimit: 4, EvidenceLimit: 4, IdempotencyKey: key,
		Producer: archivistProducer(), LeaseGuard: allowArchivistLease,
	}
	if candidateID != "" {
		request.CandidateIDs = []SemanticID{candidateID}
	}
	return request
}

func archivistProducer() ProducerIdentity {
	return ProducerIdentity{ProducerID: "loom.provenance_archivist", ProducerKind: "loom_worker", TaskID: "main.provenance_archivist", Metadata: map[string]any{"policy_version": ArchivistPolicyVersion}}
}

func allowArchivistLease(context.Context) error { return nil }

func operationTypes(request ManualOperationsRequest) []string {
	result := make([]string, 0, len(request.Operations))
	for _, document := range request.Operations {
		var header struct {
			OperationType string `json:"operation_type"`
		}
		_ = json.Unmarshal(document, &header)
		result = append(result, header.OperationType)
	}
	return result
}

func operationIDs(request ManualOperationsRequest) []SemanticID {
	result := make([]SemanticID, 0, len(request.Operations))
	for _, document := range request.Operations {
		var header struct {
			OperationID SemanticID `json:"operation_id"`
		}
		_ = json.Unmarshal(document, &header)
		result = append(result, header.OperationID)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func mustTestJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustDecodeJSON(t *testing.T, raw json.RawMessage, destination any) {
	t.Helper()
	if err := json.Unmarshal(raw, destination); err != nil {
		t.Fatal(err)
	}
}

func stringPointer(value string) *string { return &value }
