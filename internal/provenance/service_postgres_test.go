package provenance

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

//go:embed testdata/lifecycle_parity.json
var lifecycleParityFixtureJSON []byte

type lifecycleParityFixture struct {
	SchemaVersion                  string                `json:"schema_version"`
	UpstreamCommit                 string                `json:"upstream_commit"`
	CandidateRegistration          CandidateRegistration `json:"candidate_registration"`
	DuplicateCandidateRegistration CandidateRegistration `json:"duplicate_candidate_registration"`
	ReconciliationScenarios        []struct {
		Name             string   `json:"name"`
		Operations       []string `json:"operations"`
		RelationshipType string   `json:"relationship_type"`
	} `json:"reconciliation_scenarios"`
}

func loadLifecycleParityFixture(t *testing.T) lifecycleParityFixture {
	t.Helper()
	var fixture lifecycleParityFixture
	if err := json.Unmarshal(lifecycleParityFixtureJSON, &fixture); err != nil {
		t.Fatalf("decode lifecycle parity fixture: %v", err)
	}
	if fixture.UpstreamCommit != "93d3d94a76059802d00dbbe818cb188babcb18f4" || len(fixture.ReconciliationScenarios) != 6 {
		t.Fatalf("lifecycle fixture does not match pinned upstream snapshot: %#v", fixture)
	}
	return fixture
}

func semanticID(number int) SemanticID {
	return SemanticID(fmt.Sprintf("00000000-0000-4000-8000-%012d", number))
}

func fixedClock() time.Time {
	return time.Date(2026, 8, 29, 17, 0, 0, 123456000, time.UTC)
}

func sequentialIDFactory(start int) IDFactory {
	var sequence atomic.Int64
	sequence.Store(int64(start - 1))
	return func() (SemanticID, error) { return semanticID(int(sequence.Add(1))), nil }
}

func newLifecycleService(t *testing.T, purpose string, startID int) (*Store, *Service) {
	t.Helper()
	_, store, _ := migratedStore(t, purpose)
	service, err := NewService(store, WithClock(fixedClock), WithIDFactory(sequentialIDFactory(startID)))
	if err != nil {
		t.Fatal(err)
	}
	return store, service
}

func reviewerIdentity() ProducerIdentity {
	return ProducerIdentity{ProducerID: "invented-manual-reviewer", ProducerKind: "working_agent", RunID: idPointer(semanticID(9001)), TaskID: "invented-review-task"}
}

func idPointer(id SemanticID) *SemanticID { return &id }

func operationMetadata(id int, producer ProducerIdentity, evidence ...SemanticID) OperationMetadata {
	return OperationMetadata{SchemaVersion: SchemaVersion, OperationID: semanticID(id), OccurredAt: fixedClock().Add(time.Duration(id) * time.Microsecond), Producer: producer, EvidenceSourceIDs: evidence}
}

func acceptedRecord(id int, registration CandidateRegistration, reviewer ProducerIdentity, sources ...SemanticID) AcceptedRecord {
	history := []ProducerIdentity{registration.Producer}
	if !sameProducer(registration.Producer, reviewer) {
		history = append(history, reviewer)
	}
	return AcceptedRecord{
		Record: Record{ID: semanticID(id), SchemaVersion: SchemaVersion, Claim: registration.Claim,
			RecordKind: registration.RecordKind, RecordContext: registration.RecordContext,
			Ambiguity: append([]string(nil), registration.Ambiguity...), Domain: registration.Domain,
			Visibility: registration.Visibility, AssertionPosture: registration.AssertionPosture,
			Temporal: registration.Temporal, Anchors: registration.Anchors,
			CreatedAt: fixedClock().Add(time.Duration(id) * time.Microsecond)},
		SourceReferenceIDs: sources, ProducerHistory: history,
	}
}

func registerFixtureBatch(t *testing.T, service *Service, fixture lifecycleParityFixture) CandidateRegistrationBatchReceipt {
	t.Helper()
	receipt, err := service.RegisterCandidateBatch(context.Background(), CandidateRegistrationBatch{
		ReplayKey: "fixture-registration", Candidates: []CandidateRegistration{fixture.CandidateRegistration, fixture.DuplicateCandidateRegistration},
	})
	if err != nil {
		t.Fatalf("register fixture batch: %v", err)
	}
	return receipt
}

func registrationWithSources(registration CandidateRegistration, firstID, count int) CandidateRegistration {
	template := registration.Sources[0]
	registration.Sources = make([]SourceRegistration, count)
	for index := range count {
		registration.Sources[index] = template
		registration.Sources[index].SourceReferenceID = semanticID(firstID + index)
	}
	return registration
}

func TestCandidateRegistrationParityStableReplayConflictAndAtomicityPostgres(t *testing.T) {
	ctx := context.Background()
	store, service := newLifecycleService(t, "lifecycle_register", 100)
	fixture := loadLifecycleParityFixture(t)
	batch := CandidateRegistrationBatch{ReplayKey: "stable-fixture-request", Candidates: []CandidateRegistration{fixture.CandidateRegistration, fixture.DuplicateCandidateRegistration}}
	receipt, err := service.RegisterCandidateBatch(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Replayed || len(receipt.Receipts) != 2 || receipt.RegistrationDigest == "" {
		t.Fatalf("initial receipt = %#v", receipt)
	}
	if receipt.Receipts[0].CandidateID == receipt.Receipts[1].CandidateID || receipt.Receipts[0].SourceResults[0].VerificationPosture != "unverified" || receipt.Receipts[0].SourceResults[0].GapReason == nil {
		t.Fatalf("candidate source posture or ids were not preserved: %#v", receipt)
	}
	replayed, err := service.RegisterCandidateBatch(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.Receipts[0].CandidateID != receipt.Receipts[0].CandidateID || replayed.Receipts[1].SourceResults[0].ID != receipt.Receipts[1].SourceResults[0].ID {
		t.Fatalf("unstable registration replay: first=%#v replay=%#v", receipt, replayed)
	}
	aliasBatch := batch
	aliasBatch.ReplayKey = "stable-fixture-alias"
	alias, err := service.RegisterCandidateBatch(ctx, aliasBatch)
	if err != nil || !alias.Replayed || alias.Receipts[0].CandidateID != receipt.Receipts[0].CandidateID || alias.Receipts[1].CandidateID != receipt.Receipts[1].CandidateID {
		t.Fatalf("registration alias replay=%#v err=%v", alias, err)
	}
	boundAlias, found, err := store.getRegistrationReplayByKey(ctx, aliasBatch.ReplayKey)
	if err != nil || !found || boundAlias.ReplayKey != aliasBatch.ReplayKey || boundAlias.CandidateIDs[0] != receipt.Receipts[0].CandidateID {
		t.Fatalf("durable registration alias=%#v found=%t err=%v", boundAlias, found, err)
	}
	conflictingAlias := aliasBatch
	conflictingAlias.Candidates = append([]CandidateRegistration(nil), aliasBatch.Candidates...)
	conflictingAlias.Candidates[0].Claim = "A changed payload cannot reuse the admitted alias."
	if _, err := service.RegisterCandidateBatch(ctx, conflictingAlias); !errors.Is(err, ErrReplayConflict) {
		t.Fatalf("registration alias payload conflict=%v", err)
	}

	conflict := batch
	conflict.Candidates = append([]CandidateRegistration(nil), batch.Candidates...)
	conflict.Candidates[0].Claim = "A conflicting payload under the same request key."
	if _, err := service.RegisterCandidateBatch(ctx, conflict); !errors.Is(err, ErrReplayConflict) {
		t.Fatalf("payload conflict error = %v", err)
	}

	collisionA := fixture.CandidateRegistration
	collisionB := fixture.DuplicateCandidateRegistration
	collisionA.Sources = append([]SourceRegistration(nil), collisionA.Sources...)
	collisionB.Sources = append([]SourceRegistration(nil), collisionB.Sources...)
	collisionA.Sources[0].SourceReferenceID = semanticID(2999)
	collisionB.Sources[0].SourceReferenceID = semanticID(2999)
	if _, err := service.RegisterCandidateBatch(ctx, CandidateRegistrationBatch{ReplayKey: "atomic-collision", Candidates: []CandidateRegistration{collisionA, collisionB}}); err == nil {
		t.Fatal("source id collision unexpectedly succeeded")
	}
	var candidates, sources, replays int
	if err := store.q.queryRow(ctx, `SELECT (SELECT count(*) FROM provenance.candidates),(SELECT count(*) FROM provenance.source_references),(SELECT count(*) FROM provenance.registration_replays)`).Scan(&candidates, &sources, &replays); err != nil {
		t.Fatal(err)
	}
	if candidates != 2 || sources != 2 || replays != 2 {
		t.Fatalf("failed registration mutated ledger: candidates=%d sources=%d replays=%d", candidates, sources, replays)
	}

	concurrentBatch := batch
	concurrentBatch.ReplayKey = "concurrent-stable-fixture"
	concurrentBatch.Candidates = []CandidateRegistration{fixture.CandidateRegistration}
	concurrentBatch.Candidates[0].Sources = append([]SourceRegistration(nil), concurrentBatch.Candidates[0].Sources...)
	concurrentBatch.Candidates[0].Sources[0].SourceReferenceID = semanticID(2888)
	const contenders = 12
	var wait sync.WaitGroup
	results := make(chan CandidateRegistrationBatchReceipt, contenders)
	errorsFound := make(chan error, contenders)
	wait.Add(contenders)
	for range contenders {
		go func() {
			defer wait.Done()
			value, err := service.RegisterCandidateBatch(ctx, concurrentBatch)
			results <- value
			errorsFound <- err
		}()
	}
	wait.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	var stableID SemanticID
	inserted := 0
	for value := range results {
		if stableID == "" {
			stableID = value.Receipts[0].CandidateID
		}
		if value.Receipts[0].CandidateID != stableID {
			t.Fatalf("concurrent replay returned different candidate ids: %s and %s", stableID, value.Receipts[0].CandidateID)
		}
		if !value.Replayed {
			inserted++
		}
	}
	if inserted != 1 {
		t.Fatalf("concurrent registration insert receipts=%d, want 1", inserted)
	}
	oversized := fixture.CandidateRegistration
	oversized.Ambiguity = make([]string, MaximumSourcesPerObject+1)
	for index := range oversized.Ambiguity {
		oversized.Ambiguity[index] = fmt.Sprintf("bounded ambiguity %d", index)
	}
	if _, err := service.RegisterCandidate(ctx, "oversized-candidate", oversized); err == nil {
		t.Fatal("oversized candidate collection was accepted")
	}
}

func TestEvidenceOnlyRegistrationPreservesGapAndCreatesNoCandidatePostgres(t *testing.T) {
	ctx := context.Background()
	store, service := newLifecycleService(t, "lifecycle_evidence", 300)
	fixture := loadLifecycleParityFixture(t)
	request := EvidenceRegistrationRequest{RegistrationID: semanticID(310), Domain: fixture.CandidateRegistration.Domain, Visibility: "private", EvidenceContext: "Invented evidence-only source gap.", Producer: fixture.CandidateRegistration.Producer, RegisteredAt: fixedClock(), Source: fixture.CandidateRegistration.Sources[0]}
	request.Source.SourceReferenceID = semanticID(311)
	receipt, err := service.RegisterEvidence(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != "registered" || receipt.SourceResult.Status != "unresolved" || receipt.SourceResult.VerificationPosture != "unverified" {
		t.Fatalf("evidence receipt=%#v", receipt)
	}
	var candidateCount, evidenceCount int
	if err := store.q.queryRow(ctx, `SELECT (SELECT count(*) FROM provenance.candidates),(SELECT count(*) FROM provenance.evidence_registrations)`).Scan(&candidateCount, &evidenceCount); err != nil {
		t.Fatal(err)
	}
	if candidateCount != 0 || evidenceCount != 1 {
		t.Fatalf("evidence-only registration candidates=%d evidence=%d", candidateCount, evidenceCount)
	}
	invalid := request
	invalid.RegistrationID = semanticID(312)
	invalid.Source.SourceReferenceID = semanticID(313)
	invalid.Source.Status = "resolved"
	invalid.Source.CanonicalLocator = nil
	if _, err := service.RegisterEvidence(ctx, invalid); err == nil {
		t.Fatal("resolved source without locator was accepted")
	}
}

func TestCandidateEvidenceLinkIsScopedAppendOnlyAndReplaySafePostgres(t *testing.T) {
	ctx := context.Background()
	_, service := newLifecycleService(t, "lifecycle_candidate_evidence", 340)
	fixture := loadLifecycleParityFixture(t)
	registration := fixture.CandidateRegistration
	registration.Sources = append([]SourceRegistration(nil), registration.Sources...)
	registration.Sources[0].SourceReferenceID = semanticID(341)
	candidate, err := service.RegisterCandidate(ctx, "candidate-evidence-base", registration)
	if err != nil {
		t.Fatal(err)
	}
	evidenceSource := fixture.DuplicateCandidateRegistration.Sources[0]
	evidenceSource.SourceReferenceID = semanticID(342)
	if _, err := service.RegisterEvidence(ctx, EvidenceRegistrationRequest{RegistrationID: semanticID(343), Domain: registration.Domain, Visibility: registration.Visibility, EvidenceContext: "Additional invented evidence.", Producer: registration.Producer, RegisteredAt: fixedClock(), Source: evidenceSource}); err != nil {
		t.Fatal(err)
	}
	reviewer := reviewerIdentity()
	scope := LifecycleScope{Domain: registration.Domain, Visibility: registration.Visibility}
	link := LinkCandidateEvidenceOperation{OperationMetadata: operationMetadata(344, reviewer, evidenceSource.SourceReferenceID), CandidateID: candidate.CandidateID, SourceReferenceID: evidenceSource.SourceReferenceID, EvidenceContext: "The source qualifies the same pending candidate."}
	receipt, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{link}})
	if err != nil || receipt.Receipts[0].EffectiveState != "pending" {
		t.Fatalf("candidate evidence link=%#v err=%v", receipt, err)
	}
	replayed, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{link}})
	if err != nil || !replayed.Replayed {
		t.Fatalf("candidate evidence replay=%#v err=%v", replayed, err)
	}
	projection, found, err := service.GetCandidateLifecycle(ctx, candidate.CandidateID, 10)
	if err != nil || !found || len(projection.SourceReferenceIDs) != 2 || projection.EffectiveState != "pending" {
		t.Fatalf("candidate evidence projection=%#v found=%t err=%v", projection, found, err)
	}
	conflict := link
	conflict.EvidenceContext = "A conflicting evidence-link payload."
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{conflict}}); !errors.Is(err, ErrOperationReplayConflict) {
		t.Fatalf("candidate evidence replay conflict=%v", err)
	}
}

func TestCandidateEvidenceAggregateBoundIsAtomicPostgres(t *testing.T) {
	ctx := context.Background()
	store, service := newLifecycleService(t, "lifecycle_candidate_evidence_bound", 10000)
	fixture := loadLifecycleParityFixture(t)
	registration := registrationWithSources(fixture.CandidateRegistration, 11000, MaximumSourcesPerObject-1)
	candidate, err := service.RegisterCandidate(ctx, "evidence-bound-base", registration)
	if err != nil {
		t.Fatal(err)
	}

	evidenceIDs := []SemanticID{semanticID(12000), semanticID(12001), semanticID(12002)}
	for index, sourceID := range evidenceIDs {
		source := fixture.DuplicateCandidateRegistration.Sources[0]
		source.SourceReferenceID = sourceID
		_, err := service.RegisterEvidence(ctx, EvidenceRegistrationRequest{
			RegistrationID: semanticID(12100 + index), Domain: registration.Domain,
			Visibility: registration.Visibility, EvidenceContext: "Invented bounded candidate evidence.",
			Producer: registration.Producer, RegisteredAt: fixedClock(), Source: source,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	reviewer := reviewerIdentity()
	scope := LifecycleScope{Domain: registration.Domain, Visibility: registration.Visibility}
	exactLimit := LinkCandidateEvidenceOperation{
		OperationMetadata: operationMetadata(12200, reviewer, evidenceIDs[0]), CandidateID: candidate.CandidateID,
		SourceReferenceID: evidenceIDs[0], EvidenceContext: "This is the exact aggregate source limit.",
	}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{exactLimit}}); err != nil {
		t.Fatalf("exact-limit evidence link: %v", err)
	}
	projection, found, err := service.GetCandidateLifecycle(ctx, candidate.CandidateID, MaximumSourcesPerObject)
	if err != nil || !found || len(projection.SourceReferenceIDs) != MaximumSourcesPerObject {
		t.Fatalf("exact-limit projection=%#v found=%t err=%v", projection, found, err)
	}
	deferAtLimit := DeferCandidateOperation{OperationMetadata: operationMetadata(12204, reviewer), CandidateID: candidate.CandidateID, ReasonCode: "bounded-review", Reason: "Exact-limit candidates remain operable."}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{deferAtLimit}}); err != nil {
		t.Fatalf("exact-limit candidate was bricked: %v", err)
	}

	oneOver := LinkCandidateEvidenceOperation{
		OperationMetadata: operationMetadata(12201, reviewer, evidenceIDs[1]), CandidateID: candidate.CandidateID,
		SourceReferenceID: evidenceIDs[1], EvidenceContext: "This would exceed the aggregate source limit.",
	}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{oneOver}}); err == nil {
		t.Fatal("one-over-limit evidence link was accepted")
	}

	secondRegistration := registrationWithSources(fixture.DuplicateCandidateRegistration, 13000, MaximumSourcesPerObject-1)
	secondCandidate, err := service.RegisterCandidate(ctx, "evidence-bound-atomic", secondRegistration)
	if err != nil {
		t.Fatal(err)
	}
	firstBatchLink := LinkCandidateEvidenceOperation{
		OperationMetadata: operationMetadata(12202, reviewer, evidenceIDs[1]), CandidateID: secondCandidate.CandidateID,
		SourceReferenceID: evidenceIDs[1], EvidenceContext: "The first link reaches the exact limit.",
	}
	secondBatchLink := LinkCandidateEvidenceOperation{
		OperationMetadata: operationMetadata(12203, reviewer, evidenceIDs[2]), CandidateID: secondCandidate.CandidateID,
		SourceReferenceID: evidenceIDs[2], EvidenceContext: "The second link would exceed the limit.",
	}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{firstBatchLink, secondBatchLink}}); err == nil {
		t.Fatal("multi-link over-limit batch was accepted")
	}

	var firstLinks, firstRejectedOperations, secondLinks, secondOperations int
	if err := store.q.queryRow(ctx, `
		SELECT
			(SELECT count(*) FROM provenance.candidate_evidence_links WHERE candidate_id=$1),
			(SELECT count(*) FROM provenance.operation_history WHERE id=$2),
			(SELECT count(*) FROM provenance.candidate_evidence_links WHERE candidate_id=$3),
			(SELECT count(*) FROM provenance.operation_history WHERE id IN ($4,$5))
	`, string(candidate.CandidateID), string(oneOver.OperationID), string(secondCandidate.CandidateID), string(firstBatchLink.OperationID), string(secondBatchLink.OperationID)).Scan(&firstLinks, &firstRejectedOperations, &secondLinks, &secondOperations); err != nil {
		t.Fatal(err)
	}
	if firstLinks != 1 || firstRejectedOperations != 0 || secondLinks != 0 || secondOperations != 0 {
		t.Fatalf("aggregate-bound rollback first_links=%d rejected_ops=%d second_links=%d second_ops=%d", firstLinks, firstRejectedOperations, secondLinks, secondOperations)
	}
}

func TestCandidateLineageRequiresExactSemanticScopePostgres(t *testing.T) {
	ctx := context.Background()
	store, service := newLifecycleService(t, "lifecycle_lineage_scope", 14000)
	fixture := loadLifecycleParityFixture(t)
	parentRegistration := registrationWithSources(fixture.CandidateRegistration, 14100, 1)
	parent, err := service.RegisterCandidate(ctx, "lineage-parent", parentRegistration)
	if err != nil {
		t.Fatal(err)
	}
	sameScope := registrationWithSources(fixture.DuplicateCandidateRegistration, 14101, 1)
	sameScope.DerivedFromCandidateIDs = []SemanticID{parent.CandidateID}
	child, err := service.RegisterCandidate(ctx, "lineage-same-scope", sameScope)
	if err != nil {
		t.Fatal(err)
	}
	projection, found, err := service.GetCandidateLifecycle(ctx, child.CandidateID, 10)
	if err != nil || !found || len(projection.Lineage) != 1 || projection.Lineage[0] != parent.CandidateID {
		t.Fatalf("same-scope lineage projection=%#v found=%t err=%v", projection, found, err)
	}

	validDomainPeer := registrationWithSources(fixture.CandidateRegistration, 14102, 1)
	crossDomain := registrationWithSources(fixture.DuplicateCandidateRegistration, 14103, 1)
	crossDomain.Domain = "another-semantic-domain"
	crossDomain.DerivedFromCandidateIDs = []SemanticID{parent.CandidateID}
	if _, err := service.RegisterCandidateBatch(ctx, CandidateRegistrationBatch{ReplayKey: "lineage-cross-domain", Candidates: []CandidateRegistration{validDomainPeer, crossDomain}}); err == nil {
		t.Fatal("cross-domain lineage parent was accepted")
	}

	validVisibilityPeer := registrationWithSources(fixture.CandidateRegistration, 14104, 1)
	crossVisibility := registrationWithSources(fixture.DuplicateCandidateRegistration, 14105, 1)
	crossVisibility.Visibility = "shared"
	crossVisibility.DerivedFromCandidateIDs = []SemanticID{parent.CandidateID}
	if _, err := service.RegisterCandidateBatch(ctx, CandidateRegistrationBatch{ReplayKey: "lineage-cross-visibility", Candidates: []CandidateRegistration{validVisibilityPeer, crossVisibility}}); err == nil {
		t.Fatal("cross-visibility lineage parent was accepted")
	}

	var candidates, lineage, leakedSources, refusedReplays int
	if err := store.q.queryRow(ctx, `
		SELECT
			(SELECT count(*) FROM provenance.candidates),
			(SELECT count(*) FROM provenance.candidate_lineage),
			(SELECT count(*) FROM provenance.source_references WHERE id IN ($1,$2,$3,$4)),
			(SELECT count(*) FROM provenance.registration_replays WHERE replay_key IN ('lineage-cross-domain','lineage-cross-visibility'))
	`, string(validDomainPeer.Sources[0].SourceReferenceID), string(crossDomain.Sources[0].SourceReferenceID), string(validVisibilityPeer.Sources[0].SourceReferenceID), string(crossVisibility.Sources[0].SourceReferenceID)).Scan(&candidates, &lineage, &leakedSources, &refusedReplays); err != nil {
		t.Fatal(err)
	}
	if candidates != 2 || lineage != 1 || leakedSources != 0 || refusedReplays != 0 {
		t.Fatalf("lineage scope rollback candidates=%d lineage=%d leaked_sources=%d refused_replays=%d", candidates, lineage, leakedSources, refusedReplays)
	}
}

func TestManualLifecycleAcceptConsolidateDeferRejectAndReplayPostgres(t *testing.T) {
	ctx := context.Background()
	store, service := newLifecycleService(t, "lifecycle_manual", 400)
	fixture := loadLifecycleParityFixture(t)
	registered := registerFixtureBatch(t, service, fixture)
	reviewer := reviewerIdentity()
	scope := LifecycleScope{Domain: fixture.CandidateRegistration.Domain, Visibility: "private"}
	firstCandidate, secondCandidate := registered.Receipts[0].CandidateID, registered.Receipts[1].CandidateID
	firstSource, secondSource := registered.Receipts[0].SourceResults[0].ID, registered.Receipts[1].SourceResults[0].ID
	accept := AcceptCandidateOperation{OperationMetadata: operationMetadata(410, reviewer, firstSource), CandidateID: firstCandidate, Accepted: acceptedRecord(411, fixture.CandidateRegistration, reviewer, firstSource)}
	sourceActorID := "invented-source-author"
	accept.Accepted.SourceActor = &ActorReference{ActorID: sourceActorID, ActorKind: "human", SourceReferenceID: &firstSource}
	consolidate := ConsolidateCandidateOperation{OperationMetadata: operationMetadata(412, reviewer, secondSource), CandidateID: secondCandidate, RecordID: accept.Accepted.Record.ID, Rationale: "The second extraction expresses the same scoped assertion."}
	receipt, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{accept, consolidate}})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Replayed || len(receipt.Receipts) != 2 || receipt.Receipts[0].EffectiveState != "accepted" || receipt.Receipts[1].EffectiveState != "consolidated" {
		t.Fatalf("lifecycle receipt=%#v", receipt)
	}
	replayed, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{accept, consolidate}})
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || !replayed.Receipts[0].Replayed {
		t.Fatalf("operation replay=%#v", replayed)
	}
	partial := AppendQualificationOperation{OperationMetadata: operationMetadata(413, reviewer, firstSource), RecordID: accept.Accepted.Record.ID, Qualification: "A new operation cannot be mixed into an old replay batch."}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{accept, partial}}); !errors.Is(err, ErrPartialOperationReplay) {
		t.Fatalf("partial replay error=%v", err)
	}
	wrongScope := scope
	wrongScope.Domain = "another-domain"
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: wrongScope, Producer: reviewer, Operations: []LifecycleOperation{accept}}); !errors.Is(err, ErrOperationReplayConflict) {
		t.Fatalf("scope-changing replay error=%v", err)
	}
	conflicting := accept
	conflicting.Accepted.Record.Claim = "Conflicting operation payload"
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{conflicting}}); !errors.Is(err, ErrOperationReplayConflict) {
		t.Fatalf("operation conflict error=%v", err)
	}
	firstProjection, found, err := service.GetCandidateLifecycle(ctx, firstCandidate, 10)
	if err != nil || !found || firstProjection.EffectiveState != "accepted" {
		t.Fatalf("accepted candidate projection=%#v found=%t err=%v", firstProjection, found, err)
	}
	secondProjection, found, err := service.GetCandidateLifecycle(ctx, secondCandidate, 10)
	if err != nil || !found || secondProjection.EffectiveState != "consolidated" {
		t.Fatalf("consolidated candidate projection=%#v found=%t err=%v", secondProjection, found, err)
	}
	recordProjection, found, err := service.GetRecordLifecycle(ctx, accept.Accepted.Record.ID, 10)
	if err != nil || !found || len(recordProjection.SourceReferenceIDs) != 2 || len(recordProjection.ProducerHistory) != 3 {
		t.Fatalf("consolidated record projection=%#v found=%t err=%v", recordProjection, found, err)
	}
	if recordProjection.Record.SourceActorID == nil || *recordProjection.Record.SourceActorID != sourceActorID || recordProjection.ProducerHistory[1].Producer.ProducerID != reviewer.ProducerID {
		t.Fatalf("semantic source and reviewer identities were conflated: %#v", recordProjection)
	}

	third := fixture.CandidateRegistration
	third.Sources = append([]SourceRegistration(nil), third.Sources...)
	third.Sources[0].SourceReferenceID = semanticID(420)
	thirdReceipt, err := service.RegisterCandidate(ctx, "defer-then-reject", third)
	if err != nil {
		t.Fatal(err)
	}
	deferOperation := DeferCandidateOperation{OperationMetadata: operationMetadata(421, reviewer), CandidateID: thirdReceipt.CandidateID, ReasonCode: "missing-capability", Reason: "A bounded external capability is unavailable.", NeedsCapability: "invented-research"}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{deferOperation}}); err != nil {
		t.Fatal(err)
	}
	projection, _, err := service.GetCandidateLifecycle(ctx, thirdReceipt.CandidateID, 10)
	if err != nil || projection.EffectiveState != "pending" || len(projection.LatestDeferral) == 0 {
		t.Fatalf("deferred projection=%#v err=%v", projection, err)
	}
	rejectOperation := RejectCandidateOperation{OperationMetadata: operationMetadata(422, reviewer), CandidateID: thirdReceipt.CandidateID, Reason: "The invented assertion is unsupported."}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{rejectOperation}}); err != nil {
		t.Fatal(err)
	}
	projection, _, _ = service.GetCandidateLifecycle(ctx, thirdReceipt.CandidateID, 10)
	if projection.EffectiveState != "rejected" {
		t.Fatalf("rejected projection=%#v", projection)
	}
	var originalRows, terminalEvents, operations int
	if err := store.q.queryRow(ctx, `SELECT (SELECT count(*) FROM provenance.candidates WHERE id IN ($1,$2,$3)),(SELECT count(*) FROM provenance.candidate_events WHERE event_type IN ('accepted','consolidated','rejected')),(SELECT count(*) FROM provenance.operation_history)`, string(firstCandidate), string(secondCandidate), string(thirdReceipt.CandidateID)).Scan(&originalRows, &terminalEvents, &operations); err != nil {
		t.Fatal(err)
	}
	if originalRows != 3 || terminalEvents != 3 || operations != 4 {
		t.Fatalf("append-only lifecycle rows candidates=%d terminal=%d operations=%d", originalRows, terminalEvents, operations)
	}
}

func TestRepresentationRelationshipAndResolutionCaseLifecyclePostgres(t *testing.T) {
	ctx := context.Background()
	_, service := newLifecycleService(t, "lifecycle_graph", 500)
	fixture := loadLifecycleParityFixture(t)
	registered := registerFixtureBatch(t, service, fixture)
	reviewer := reviewerIdentity()
	scope := LifecycleScope{Domain: fixture.CandidateRegistration.Domain, Visibility: "private"}
	sourceA, sourceB := registered.Receipts[0].SourceResults[0].ID, registered.Receipts[1].SourceResults[0].ID
	acceptA := AcceptCandidateOperation{OperationMetadata: operationMetadata(510, reviewer, sourceA), CandidateID: registered.Receipts[0].CandidateID, Accepted: acceptedRecord(511, fixture.CandidateRegistration, reviewer, sourceA)}
	acceptB := AcceptCandidateOperation{OperationMetadata: operationMetadata(512, reviewer, sourceB), CandidateID: registered.Receipts[1].CandidateID, Accepted: acceptedRecord(513, fixture.DuplicateCandidateRegistration, reviewer, sourceB)}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{acceptA, acceptB}}); err != nil {
		t.Fatal(err)
	}
	caseID := semanticID(520)
	openingOutcome := "open"
	openingEvent := CaseEvent{ID: semanticID(521), CaseID: caseID, SchemaVersion: SchemaVersion, EventType: "opened", OccurredAt: fixedClock().Add(521 * time.Microsecond), Producer: reviewer, Summary: "The invented decisions need coverage-qualified reconciliation.", Outcome: &openingOutcome, EvidenceSourceIDs: []SemanticID{sourceA, sourceB}}
	createCase := CreateResolutionCaseOperation{OperationMetadata: operationMetadata(522, reviewer, sourceA, sourceB), Case: ResolutionCase{ID: caseID, SchemaVersion: SchemaVersion, Issue: "Coverage differs between two invented decisions.", Domain: scope.Domain, Visibility: scope.Visibility, InitialStatus: "open", CreatedAt: fixedClock().Add(520 * time.Microsecond)}, Members: []CaseMemberReference{{MemberType: "record", MemberID: acceptA.Accepted.Record.ID, Role: "earlier"}, {MemberType: "record", MemberID: acceptB.Accepted.Record.ID, Role: "later"}}, Events: []CaseEvent{openingEvent}}
	coverage := json.RawMessage(`{"description":"Hosted deployments only","dimensions":{"deployment":"hosted"}}`)
	relationshipID := semanticID(523)
	addRelationship := AddRelationshipOperation{OperationMetadata: operationMetadata(524, reviewer, sourceA, sourceB), Relationship: Relationship{ID: relationshipID, SchemaVersion: SchemaVersion, RelationshipType: "partially_supersedes", FromRecordID: acceptB.Accepted.Record.ID, ToRecordID: acceptA.Accepted.Record.ID, ResolutionCaseID: &caseID, Coverage: coverage, EvidenceSourceIDs: []SemanticID{sourceA, sourceB}, CreatedAt: fixedClock().Add(523 * time.Microsecond), CreatedBy: reviewer}}
	qualification := AppendQualificationOperation{OperationMetadata: operationMetadata(525, reviewer, sourceA), RecordID: acceptA.Accepted.Record.ID, Qualification: "Hosted deployments require a separate decision.", MarksAmbiguity: true}
	newTemporal := TemporalInterpretation{Interpretation: "Applies only after the invented review."}
	temporal := AppendTemporalInterpretationOperation{OperationMetadata: operationMetadata(532, reviewer, sourceA), RecordID: acceptA.Accepted.Record.ID, Temporal: newTemporal}
	posture := AppendAssertionPostureOperation{OperationMetadata: operationMetadata(533, reviewer, sourceA), RecordID: acceptA.Accepted.Record.ID, AssertionPosture: "qualified", Rationale: "The bounded evidence narrows the assertion."}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{createCase, addRelationship, qualification, temporal, posture}}); err != nil {
		t.Fatal(err)
	}
	recordProjection, _, err := service.GetRecordLifecycle(ctx, acceptA.Accepted.Record.ID, 1)
	if err != nil || !recordProjection.Truncated || recordProjection.Record.Temporal.Interpretation != newTemporal.Interpretation || recordProjection.Record.AssertionPosture != "qualified" {
		t.Fatalf("record representation=%#v err=%v", recordProjection, err)
	}
	fullRecordProjection, _, err := service.GetRecordLifecycle(ctx, acceptA.Accepted.Record.ID, 10)
	if err != nil || !slicesContains(fullRecordProjection.Record.Ambiguity, qualification.Qualification) {
		t.Fatalf("record ambiguity history=%#v err=%v", fullRecordProjection, err)
	}
	relationshipProjection, _, err := service.GetRelationshipLifecycle(ctx, relationshipID, 10)
	if err != nil || relationshipProjection.EffectiveState != "active" {
		t.Fatalf("relationship projection=%#v err=%v", relationshipProjection, err)
	}
	invalidPartial := addRelationship
	invalidPartial.OperationMetadata = operationMetadata(534, reviewer, sourceA)
	invalidPartial.Relationship.ID = semanticID(535)
	invalidPartial.Relationship.ResolutionCaseID = nil
	invalidPartial.Relationship.Coverage = nil
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{invalidPartial}}); err == nil {
		t.Fatal("partial relationship without case and coverage was accepted")
	}
	caseProjection, _, err := service.GetResolutionCaseLifecycle(ctx, caseID, 10)
	if err != nil || caseProjection.EffectiveState != "open" || len(caseProjection.Members) != 2 {
		t.Fatalf("case projection=%#v err=%v", caseProjection, err)
	}

	investigating := "investigating"
	update := UpdateResolutionCaseOperation{OperationMetadata: operationMetadata(526, reviewer, sourceA), CaseID: caseID, Event: CaseEvent{ID: semanticID(527), CaseID: caseID, SchemaVersion: SchemaVersion, EventType: "reviewed", OccurredAt: fixedClock().Add(527 * time.Microsecond), Producer: reviewer, Summary: "The bounded coverage was reviewed.", Outcome: &investigating, EvidenceSourceIDs: []SemanticID{sourceA}}}
	end := EndRelationshipOperation{OperationMetadata: operationMetadata(528, reviewer, sourceA), RelationshipID: relationshipID, Reason: "The invented relationship is superseded by the closed case."}
	closed := "resolved"
	closeOperation := CloseResolutionCaseOperation{OperationMetadata: operationMetadata(529, reviewer, sourceA), CaseID: caseID, Outcome: closed, Event: CaseEvent{ID: semanticID(530), CaseID: caseID, SchemaVersion: SchemaVersion, EventType: "closed", OccurredAt: fixedClock().Add(530 * time.Microsecond), Producer: reviewer, Summary: "The invented case is resolved.", Outcome: &closed, EvidenceSourceIDs: []SemanticID{sourceA}}}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{update, end, closeOperation}}); err != nil {
		t.Fatal(err)
	}
	relationshipProjection, _, _ = service.GetRelationshipLifecycle(ctx, relationshipID, 10)
	caseProjection, _, _ = service.GetResolutionCaseLifecycle(ctx, caseID, 10)
	if relationshipProjection.EffectiveState != "ended" || caseProjection.EffectiveState != "resolved" {
		t.Fatalf("terminal graph projections relationship=%#v case=%#v", relationshipProjection, caseProjection)
	}
	attach := AttachCaseMemberOperation{OperationMetadata: operationMetadata(531, reviewer), CaseID: caseID, Member: CaseMemberReference{MemberType: "candidate", MemberID: registered.Receipts[0].CandidateID, Role: "late"}}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{attach}}); err == nil {
		t.Fatal("closed case accepted a new member")
	}
}

func TestResolutionCaseTerminalStateCannotReopenPostgres(t *testing.T) {
	ctx := context.Background()
	store, service := newLifecycleService(t, "lifecycle_case_terminal", 15000)
	fixture := loadLifecycleParityFixture(t)
	registered := registerFixtureBatch(t, service, fixture)
	reviewer := reviewerIdentity()
	scope := LifecycleScope{Domain: fixture.CandidateRegistration.Domain, Visibility: "private"}
	member := CaseMemberReference{MemberType: "candidate", MemberID: registered.Receipts[0].CandidateID, Role: "subject"}
	source := registered.Receipts[0].SourceResults[0].ID
	resolved := "resolved"
	investigating := "investigating"
	open := "open"

	terminalInitialID := semanticID(15010)
	terminalInitial := CreateResolutionCaseOperation{
		OperationMetadata: operationMetadata(15013, reviewer, source),
		Case:              ResolutionCase{ID: terminalInitialID, SchemaVersion: SchemaVersion, Issue: "A terminal initial case cannot append state.", Domain: scope.Domain, Visibility: scope.Visibility, InitialStatus: resolved, CreatedAt: fixedClock().Add(15010 * time.Microsecond)},
		Members:           []CaseMemberReference{member},
		Events:            []CaseEvent{{ID: semanticID(15011), CaseID: terminalInitialID, SchemaVersion: SchemaVersion, EventType: "closed", OccurredAt: fixedClock().Add(15011 * time.Microsecond), Producer: reviewer, Summary: "Already terminal.", Outcome: &resolved, EvidenceSourceIDs: []SemanticID{source}}},
	}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{terminalInitial}}); err == nil {
		t.Fatal("terminal initial status accepted later members and events")
	}

	reopenedCreationID := semanticID(15020)
	reopenedCreation := CreateResolutionCaseOperation{
		OperationMetadata: operationMetadata(15023, reviewer, source),
		Case:              ResolutionCase{ID: reopenedCreationID, SchemaVersion: SchemaVersion, Issue: "A terminal event cannot be followed by an open event.", Domain: scope.Domain, Visibility: scope.Visibility, InitialStatus: open, CreatedAt: fixedClock().Add(15020 * time.Microsecond)},
		Members:           []CaseMemberReference{member},
		Events: []CaseEvent{
			{ID: semanticID(15021), CaseID: reopenedCreationID, SchemaVersion: SchemaVersion, EventType: "closed", OccurredAt: fixedClock().Add(15021 * time.Microsecond), Producer: reviewer, Summary: "The case resolved.", Outcome: &resolved, EvidenceSourceIDs: []SemanticID{source}},
			{ID: semanticID(15022), CaseID: reopenedCreationID, SchemaVersion: SchemaVersion, EventType: "reviewed", OccurredAt: fixedClock().Add(15022 * time.Microsecond), Producer: reviewer, Summary: "This event would reopen it.", Outcome: &investigating, EvidenceSourceIDs: []SemanticID{source}},
		},
	}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{reopenedCreation}}); err == nil {
		t.Fatal("terminal-then-open creation sequence was accepted")
	}

	closedCaseID := semanticID(15030)
	closedCreation := CreateResolutionCaseOperation{
		OperationMetadata: operationMetadata(15033, reviewer, source),
		Case:              ResolutionCase{ID: closedCaseID, SchemaVersion: SchemaVersion, Issue: "A valid case closes in append order.", Domain: scope.Domain, Visibility: scope.Visibility, InitialStatus: open, CreatedAt: fixedClock().Add(15030 * time.Microsecond)},
		Members:           []CaseMemberReference{member},
		Events: []CaseEvent{
			{ID: semanticID(15031), CaseID: closedCaseID, SchemaVersion: SchemaVersion, EventType: "opened", OccurredAt: fixedClock().Add(15031 * time.Microsecond), Producer: reviewer, Summary: "The case opened.", Outcome: &open, EvidenceSourceIDs: []SemanticID{source}},
			{ID: semanticID(15032), CaseID: closedCaseID, SchemaVersion: SchemaVersion, EventType: "closed", OccurredAt: fixedClock().Add(15032 * time.Microsecond), Producer: reviewer, Summary: "The case resolved.", Outcome: &resolved, EvidenceSourceIDs: []SemanticID{source}},
		},
	}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{closedCreation}}); err != nil {
		t.Fatalf("valid terminal creation: %v", err)
	}

	durableReopen := CaseEvent{ID: semanticID(15034), CaseID: closedCaseID, SchemaVersion: SchemaVersion, EventType: "reviewed", OccurredAt: fixedClock().Add(15034 * time.Microsecond), Producer: reviewer, Summary: "A durable later nonterminal outcome must not reopen the case.", Outcome: &investigating, EvidenceSourceIDs: []SemanticID{source}, Payload: json.RawMessage(`{"outcome":"investigating"}`)}
	if err := store.AppendCaseEvent(ctx, durableReopen); err != nil {
		t.Fatal(err)
	}
	update := UpdateResolutionCaseOperation{OperationMetadata: operationMetadata(15035, reviewer, source), CaseID: closedCaseID, Event: CaseEvent{ID: semanticID(15036), CaseID: closedCaseID, SchemaVersion: SchemaVersion, EventType: "reviewed", OccurredAt: fixedClock().Add(15036 * time.Microsecond), Producer: reviewer, Summary: "A post-commit update must be refused.", Outcome: &investigating, EvidenceSourceIDs: []SemanticID{source}}}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{update}}); err == nil {
		t.Fatal("durably closed case accepted an update")
	}
	attach := AttachCaseMemberOperation{OperationMetadata: operationMetadata(15037, reviewer), CaseID: closedCaseID, Member: CaseMemberReference{MemberType: "candidate", MemberID: registered.Receipts[1].CandidateID, Role: "late"}}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{attach}}); err == nil {
		t.Fatal("durably closed case accepted a member")
	}
	projection, found, err := service.GetResolutionCaseLifecycle(ctx, closedCaseID, 10)
	if err != nil || !found || projection.EffectiveState != resolved {
		t.Fatalf("monotonic case projection=%#v found=%t err=%v", projection, found, err)
	}

	var cases, events, members, rejectedOperations int
	if err := store.q.queryRow(ctx, `
		SELECT
			(SELECT count(*) FROM provenance.resolution_cases WHERE id IN ($1,$2,$3)),
			(SELECT count(*) FROM provenance.case_events WHERE case_id=$3),
			(SELECT count(*) FROM provenance.case_members WHERE case_id=$3),
			(SELECT count(*) FROM provenance.operation_history WHERE id IN ($4,$5,$6,$7))
	`, string(terminalInitialID), string(reopenedCreationID), string(closedCaseID), string(terminalInitial.OperationID), string(reopenedCreation.OperationID), string(update.OperationID), string(attach.OperationID)).Scan(&cases, &events, &members, &rejectedOperations); err != nil {
		t.Fatal(err)
	}
	if cases != 1 || events != 3 || members != 1 || rejectedOperations != 0 {
		t.Fatalf("case monotonicity ledger cases=%d events=%d members=%d rejected_ops=%d", cases, events, members, rejectedOperations)
	}
}

func TestCloseResolutionCaseRequiresTerminalOutcomeAfterReloadPostgres(t *testing.T) {
	ctx := context.Background()
	store, service := newLifecycleService(t, "lifecycle_case_close_contract", 17000)
	fixture := loadLifecycleParityFixture(t)
	registered := registerFixtureBatch(t, service, fixture)
	reviewer := reviewerIdentity()
	scope := LifecycleScope{Domain: fixture.CandidateRegistration.Domain, Visibility: "private"}
	source := registered.Receipts[0].SourceResults[0].ID
	open := "open"
	caseID := semanticID(17100)
	create := CreateResolutionCaseOperation{
		OperationMetadata: operationMetadata(17102, reviewer, source),
		Case:              ResolutionCase{ID: caseID, SchemaVersion: SchemaVersion, Issue: "Only terminal outcomes may close this case.", Domain: scope.Domain, Visibility: scope.Visibility, InitialStatus: open, CreatedAt: fixedClock().Add(17100 * time.Microsecond)},
		Members:           []CaseMemberReference{{MemberType: "candidate", MemberID: registered.Receipts[0].CandidateID, Role: "subject"}},
		Events:            []CaseEvent{{ID: semanticID(17101), CaseID: caseID, SchemaVersion: SchemaVersion, EventType: "opened", OccurredAt: fixedClock().Add(17101 * time.Microsecond), Producer: reviewer, Summary: "The case opened.", Outcome: &open, EvidenceSourceIDs: []SemanticID{source}}},
	}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{create}}); err != nil {
		t.Fatal(err)
	}
	nonterminal := "investigating"
	invalidClose := CloseResolutionCaseOperation{OperationMetadata: operationMetadata(17103, reviewer, source), CaseID: caseID, Outcome: nonterminal, Event: CaseEvent{ID: semanticID(17104), CaseID: caseID, SchemaVersion: SchemaVersion, EventType: "closed", OccurredAt: fixedClock().Add(17104 * time.Microsecond), Producer: reviewer, Summary: "This outcome is not terminal.", Outcome: &nonterminal, EvidenceSourceIDs: []SemanticID{source}}}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{invalidClose}}); err == nil {
		t.Fatal("close operation accepted a nonterminal outcome")
	}

	dismissed := "dismissed"
	validClose := CloseResolutionCaseOperation{OperationMetadata: operationMetadata(17105, reviewer, source), CaseID: caseID, Outcome: dismissed, Event: CaseEvent{ID: semanticID(17106), CaseID: caseID, SchemaVersion: SchemaVersion, EventType: "closed", OccurredAt: fixedClock().Add(17106 * time.Microsecond), Producer: reviewer, Summary: "The case is durably dismissed.", Outcome: &dismissed, EvidenceSourceIDs: []SemanticID{source}}}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{validClose}}); err != nil {
		t.Fatal(err)
	}
	update := UpdateResolutionCaseOperation{OperationMetadata: operationMetadata(17107, reviewer, source), CaseID: caseID, Event: CaseEvent{ID: semanticID(17108), CaseID: caseID, SchemaVersion: SchemaVersion, EventType: "reviewed", OccurredAt: fixedClock().Add(17108 * time.Microsecond), Producer: reviewer, Summary: "A reload must preserve terminality.", Outcome: &nonterminal, EvidenceSourceIDs: []SemanticID{source}}}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{update}}); err == nil {
		t.Fatal("accepted close was nonterminal after reload")
	}
	attach := AttachCaseMemberOperation{OperationMetadata: operationMetadata(17109, reviewer), CaseID: caseID, Member: CaseMemberReference{MemberType: "candidate", MemberID: registered.Receipts[1].CandidateID, Role: "late"}}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{attach}}); err == nil {
		t.Fatal("accepted close allowed a member after reload")
	}
	projection, found, err := service.GetResolutionCaseLifecycle(ctx, caseID, 10)
	if err != nil || !found || projection.EffectiveState != dismissed || len(projection.Events) != 2 {
		t.Fatalf("terminal close projection=%#v found=%t err=%v", projection, found, err)
	}
	var rejectedOperations int
	if err := store.q.queryRow(ctx, `SELECT count(*) FROM provenance.operation_history WHERE id IN ($1,$2,$3)`, string(invalidClose.OperationID), string(update.OperationID), string(attach.OperationID)).Scan(&rejectedOperations); err != nil {
		t.Fatal(err)
	}
	if rejectedOperations != 0 {
		t.Fatalf("rejected close mutations appended %d operations", rejectedOperations)
	}
}

func TestResolutionCaseLifecycleOrdersByOccurredAtThenAppendOrderPostgres(t *testing.T) {
	ctx := context.Background()
	_, service := newLifecycleService(t, "lifecycle_case_chronology", 17200)
	fixture := loadLifecycleParityFixture(t)
	registered := registerFixtureBatch(t, service, fixture)
	reviewer := reviewerIdentity()
	scope := LifecycleScope{Domain: fixture.CandidateRegistration.Domain, Visibility: "private"}
	source := registered.Receipts[0].SourceResults[0].ID
	caseID := semanticID(17300)
	oldOutcome, lowOutcome, middleOutcome, highOutcome := "open", "queued", "investigating", "reviewing"
	oldTime := fixedClock().Add(time.Minute)
	latestTime := oldTime.Add(time.Minute)
	events := []CaseEvent{
		{ID: semanticID(17999), CaseID: caseID, SchemaVersion: SchemaVersion, EventType: "opened", OccurredAt: oldTime, Producer: reviewer, Summary: "An older event has the highest UUID.", Outcome: &oldOutcome, EvidenceSourceIDs: []SemanticID{source}},
		{ID: semanticID(17312), CaseID: caseID, SchemaVersion: SchemaVersion, EventType: "reviewed", OccurredAt: latestTime, Producer: reviewer, Summary: "The highest tied UUID is committed first.", Outcome: &highOutcome, EvidenceSourceIDs: []SemanticID{source}},
		{ID: semanticID(17310), CaseID: caseID, SchemaVersion: SchemaVersion, EventType: "reviewed", OccurredAt: latestTime, Producer: reviewer, Summary: "The lowest tied UUID is committed second.", Outcome: &lowOutcome, EvidenceSourceIDs: []SemanticID{source}},
		{ID: semanticID(17311), CaseID: caseID, SchemaVersion: SchemaVersion, EventType: "reviewed", OccurredAt: latestTime, Producer: reviewer, Summary: "The middle tied UUID is committed last.", Outcome: &middleOutcome, EvidenceSourceIDs: []SemanticID{source}},
	}
	create := CreateResolutionCaseOperation{
		OperationMetadata: operationMetadata(17301, reviewer, source),
		Case:              ResolutionCase{ID: caseID, SchemaVersion: SchemaVersion, Issue: "Case chronology must not follow UUID order alone.", Domain: scope.Domain, Visibility: scope.Visibility, InitialStatus: "open", CreatedAt: fixedClock()},
		Members:           []CaseMemberReference{{MemberType: "candidate", MemberID: registered.Receipts[0].CandidateID, Role: "subject"}},
		Events:            events,
	}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{create}}); err != nil {
		t.Fatal(err)
	}
	projection, found, err := service.GetResolutionCaseLifecycle(ctx, caseID, 10)
	wantOrder := []SemanticID{semanticID(17311), semanticID(17310), semanticID(17312), semanticID(17999)}
	if err != nil || !found || projection.EffectiveState != middleOutcome || len(projection.Events) != len(wantOrder) {
		t.Fatalf("chronological case projection=%#v found=%t err=%v", projection, found, err)
	}
	for index, eventID := range wantOrder {
		if projection.Events[index].ID != eventID {
			t.Fatalf("case event order[%d]=%s want %s", index, projection.Events[index].ID, eventID)
		}
	}
}

func TestPartialRelationshipRequiresBothCaseEndpointsAtomicallyPostgres(t *testing.T) {
	ctx := context.Background()
	store, service := newLifecycleService(t, "lifecycle_partial_membership", 18500)
	fixture := loadLifecycleParityFixture(t)
	registered := registerFixtureBatch(t, service, fixture)
	reviewer := reviewerIdentity()
	scope := LifecycleScope{Domain: fixture.CandidateRegistration.Domain, Visibility: "private"}
	sourceA, sourceB := registered.Receipts[0].SourceResults[0].ID, registered.Receipts[1].SourceResults[0].ID
	acceptA := AcceptCandidateOperation{OperationMetadata: operationMetadata(18510, reviewer, sourceA), CandidateID: registered.Receipts[0].CandidateID, Accepted: acceptedRecord(18511, fixture.CandidateRegistration, reviewer, sourceA)}
	acceptB := AcceptCandidateOperation{OperationMetadata: operationMetadata(18512, reviewer, sourceB), CandidateID: registered.Receipts[1].CandidateID, Accepted: acceptedRecord(18513, fixture.DuplicateCandidateRegistration, reviewer, sourceB)}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{acceptA, acceptB}}); err != nil {
		t.Fatal(err)
	}
	open := "open"
	caseID := semanticID(18520)
	createCase := CreateResolutionCaseOperation{
		OperationMetadata: operationMetadata(18522, reviewer, sourceA),
		Case:              ResolutionCase{ID: caseID, SchemaVersion: SchemaVersion, Issue: "Only one relationship endpoint is a member.", Domain: scope.Domain, Visibility: scope.Visibility, InitialStatus: open, CreatedAt: fixedClock()},
		Members:           []CaseMemberReference{{MemberType: "record", MemberID: acceptA.Accepted.Record.ID, Role: "included"}},
		Events:            []CaseEvent{{ID: semanticID(18521), CaseID: caseID, SchemaVersion: SchemaVersion, EventType: "opened", OccurredAt: fixedClock(), Producer: reviewer, Summary: "The one-sided case opened.", Outcome: &open, EvidenceSourceIDs: []SemanticID{sourceA}}},
	}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{createCase}}); err != nil {
		t.Fatal(err)
	}
	qualification := AppendQualificationOperation{OperationMetadata: operationMetadata(18523, reviewer, sourceA), RecordID: acceptA.Accepted.Record.ID, Qualification: "This event must roll back with the invalid relationship."}
	coverage := json.RawMessage(`{"description":"Invented partial coverage"}`)
	partial := AddRelationshipOperation{OperationMetadata: operationMetadata(18524, reviewer, sourceA, sourceB), Relationship: Relationship{ID: semanticID(18525), SchemaVersion: SchemaVersion, RelationshipType: "partially_supersedes", FromRecordID: acceptB.Accepted.Record.ID, ToRecordID: acceptA.Accepted.Record.ID, ResolutionCaseID: &caseID, Coverage: coverage, EvidenceSourceIDs: []SemanticID{sourceA, sourceB}, CreatedAt: fixedClock(), CreatedBy: reviewer}}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{qualification, partial}}); err == nil {
		t.Fatal("partial relationship accepted a case missing one endpoint")
	}
	var relationships, representationEvents, operations int
	if err := store.q.queryRow(ctx, `SELECT (SELECT count(*) FROM provenance.relationships WHERE id=$1),(SELECT count(*) FROM provenance.record_events WHERE record_id=$2),(SELECT count(*) FROM provenance.operation_history WHERE id IN ($3,$4))`, string(partial.Relationship.ID), string(acceptA.Accepted.Record.ID), string(qualification.OperationID), string(partial.OperationID)).Scan(&relationships, &representationEvents, &operations); err != nil {
		t.Fatal(err)
	}
	if relationships != 0 || representationEvents != 0 || operations != 0 {
		t.Fatalf("partial relationship rollback relationships=%d events=%d operations=%d", relationships, representationEvents, operations)
	}
}

func TestConsolidationDeduplicatesAndBoundsRecordSourcesAtomicallyPostgres(t *testing.T) {
	ctx := context.Background()
	store, service := newLifecycleService(t, "lifecycle_consolidation_sources", 19000)
	fixture := loadLifecycleParityFixture(t)
	reviewer := reviewerIdentity()
	scope := LifecycleScope{Domain: fixture.CandidateRegistration.Domain, Visibility: "private"}
	targetRegistration := registrationWithSources(fixture.CandidateRegistration, 19100, MaximumSourcesPerObject-1)
	target, err := service.RegisterCandidate(ctx, "consolidation-target", targetRegistration)
	if err != nil {
		t.Fatal(err)
	}
	targetSources := make([]SemanticID, len(target.SourceResults))
	for index, source := range target.SourceResults {
		targetSources[index] = source.ID
	}
	accept := AcceptCandidateOperation{OperationMetadata: operationMetadata(19200, reviewer, targetSources...), CandidateID: target.CandidateID, Accepted: acceptedRecord(19201, targetRegistration, reviewer, targetSources...)}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{accept}}); err != nil {
		t.Fatal(err)
	}

	overlapRegistration := registrationWithSources(fixture.DuplicateCandidateRegistration, 19300, 1)
	overlapCandidate, err := service.RegisterCandidate(ctx, "consolidation-overlap", overlapRegistration)
	if err != nil {
		t.Fatal(err)
	}
	overlapDirect := overlapCandidate.SourceResults[0].ID
	linkOverlap := LinkCandidateEvidenceOperation{OperationMetadata: operationMetadata(19400, reviewer, targetSources[0]), CandidateID: overlapCandidate.CandidateID, SourceReferenceID: targetSources[0], EvidenceContext: "The candidate shares evidence already linked to the target record."}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{linkOverlap}}); err != nil {
		t.Fatal(err)
	}
	consolidateExact := ConsolidateCandidateOperation{OperationMetadata: operationMetadata(19401, reviewer, overlapDirect, targetSources[0]), CandidateID: overlapCandidate.CandidateID, RecordID: accept.Accepted.Record.ID, Rationale: "Overlap is deduplicated and one new source reaches the exact bound."}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{consolidateExact}}); err != nil {
		t.Fatalf("exact-bound overlapping consolidation: %v", err)
	}

	overRegistration := registrationWithSources(fixture.CandidateRegistration, 19301, 1)
	overCandidate, err := service.RegisterCandidate(ctx, "consolidation-one-over", overRegistration)
	if err != nil {
		t.Fatal(err)
	}
	deferOperation := DeferCandidateOperation{OperationMetadata: operationMetadata(19402, reviewer), CandidateID: overCandidate.CandidateID, ReasonCode: "atomicity", Reason: "This must roll back with the over-limit consolidation."}
	consolidateOver := ConsolidateCandidateOperation{OperationMetadata: operationMetadata(19403, reviewer, overCandidate.SourceResults[0].ID), CandidateID: overCandidate.CandidateID, RecordID: accept.Accepted.Record.ID, Rationale: "This unique source would exceed the aggregate bound."}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{deferOperation, consolidateOver}}); err == nil {
		t.Fatal("one-over-limit consolidation was accepted")
	}
	projection, found, err := service.GetCandidateLifecycle(ctx, overCandidate.CandidateID, 10)
	if err != nil || !found || projection.EffectiveState != "pending" || len(projection.LatestDeferral) != 0 {
		t.Fatalf("over-limit candidate projection=%#v found=%t err=%v", projection, found, err)
	}
	var recordSources, overEvents, rejectedOperations int
	if err := store.q.queryRow(ctx, `SELECT (SELECT count(*) FROM provenance.record_sources WHERE record_id=$1),(SELECT count(*) FROM provenance.candidate_events WHERE candidate_id=$2),(SELECT count(*) FROM provenance.operation_history WHERE id IN ($3,$4))`, string(accept.Accepted.Record.ID), string(overCandidate.CandidateID), string(deferOperation.OperationID), string(consolidateOver.OperationID)).Scan(&recordSources, &overEvents, &rejectedOperations); err != nil {
		t.Fatal(err)
	}
	if recordSources != MaximumSourcesPerObject || overEvents != 0 || rejectedOperations != 0 {
		t.Fatalf("consolidation source rollback record_sources=%d events=%d operations=%d", recordSources, overEvents, rejectedOperations)
	}
}

func TestAttachCaseMemberAggregateBoundAndReplayPostgres(t *testing.T) {
	ctx := context.Background()
	store, service := newLifecycleService(t, "lifecycle_case_member_bound", 21000)
	fixture := loadLifecycleParityFixture(t)
	registrations := make([]CandidateRegistration, MaximumSourcesPerObject+1)
	for index := range registrations {
		registrations[index] = registrationWithSources(fixture.CandidateRegistration, 21100+index, 1)
	}
	registered, err := service.RegisterCandidateBatch(ctx, CandidateRegistrationBatch{ReplayKey: "case-member-candidates", Candidates: registrations})
	if err != nil {
		t.Fatal(err)
	}
	reviewer := reviewerIdentity()
	scope := LifecycleScope{Domain: fixture.CandidateRegistration.Domain, Visibility: "private"}
	members := make([]CaseMemberReference, MaximumSourcesPerObject-1)
	for index := range members {
		members[index] = CaseMemberReference{MemberType: "candidate", MemberID: registered.Receipts[index].CandidateID, Role: fmt.Sprintf("member-%02d", index)}
	}
	open := "open"
	caseID := semanticID(21200)
	create := CreateResolutionCaseOperation{
		OperationMetadata: operationMetadata(21202, reviewer, registered.Receipts[0].SourceResults[0].ID),
		Case:              ResolutionCase{ID: caseID, SchemaVersion: SchemaVersion, Issue: "Case membership has an aggregate bound.", Domain: scope.Domain, Visibility: scope.Visibility, InitialStatus: open, CreatedAt: fixedClock()},
		Members:           members,
		Events:            []CaseEvent{{ID: semanticID(21201), CaseID: caseID, SchemaVersion: SchemaVersion, EventType: "opened", OccurredAt: fixedClock(), Producer: reviewer, Summary: "The bounded case opened.", Outcome: &open, EvidenceSourceIDs: []SemanticID{registered.Receipts[0].SourceResults[0].ID}}},
	}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{create}}); err != nil {
		t.Fatal(err)
	}
	exactMember := CaseMemberReference{MemberType: "candidate", MemberID: registered.Receipts[MaximumSourcesPerObject-1].CandidateID, Role: "member-49"}
	exactAttach := AttachCaseMemberOperation{OperationMetadata: operationMetadata(21203, reviewer), CaseID: caseID, Member: exactMember}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{exactAttach}}); err != nil {
		t.Fatalf("exact-limit case member: %v", err)
	}
	replayed, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{exactAttach}})
	if err != nil || !replayed.Replayed || !replayed.Receipts[0].Replayed {
		t.Fatalf("case member replay=%#v err=%v", replayed, err)
	}
	duplicateAttach := AttachCaseMemberOperation{OperationMetadata: operationMetadata(21204, reviewer), CaseID: caseID, Member: exactMember}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{duplicateAttach}}); err == nil {
		t.Fatal("duplicate case member under a new operation was accepted")
	}
	overMember := CaseMemberReference{MemberType: "candidate", MemberID: registered.Receipts[MaximumSourcesPerObject].CandidateID, Role: "member-50"}
	deferOperation := DeferCandidateOperation{OperationMetadata: operationMetadata(21205, reviewer), CandidateID: overMember.MemberID, ReasonCode: "atomicity", Reason: "This must roll back with the over-limit attachment."}
	overAttach := AttachCaseMemberOperation{OperationMetadata: operationMetadata(21206, reviewer), CaseID: caseID, Member: overMember}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{deferOperation, overAttach}}); err == nil {
		t.Fatal("one-over-limit case member was accepted")
	}
	projection, found, err := service.GetResolutionCaseLifecycle(ctx, caseID, MaximumSourcesPerObject)
	if err != nil || !found || len(projection.Members) != MaximumSourcesPerObject || projection.Truncated {
		t.Fatalf("bounded case members projection=%#v found=%t err=%v", projection, found, err)
	}
	candidateProjection, _, err := service.GetCandidateLifecycle(ctx, overMember.MemberID, 10)
	if err != nil || candidateProjection.EffectiveState != "pending" || len(candidateProjection.LatestDeferral) != 0 {
		t.Fatalf("case-member rollback candidate projection=%#v err=%v", candidateProjection, err)
	}
	var membersCount, rejectedOperations int
	if err := store.q.queryRow(ctx, `SELECT (SELECT count(*) FROM provenance.case_members WHERE case_id=$1),(SELECT count(*) FROM provenance.operation_history WHERE id IN ($2,$3,$4))`, string(caseID), string(duplicateAttach.OperationID), string(deferOperation.OperationID), string(overAttach.OperationID)).Scan(&membersCount, &rejectedOperations); err != nil {
		t.Fatal(err)
	}
	if membersCount != MaximumSourcesPerObject || rejectedOperations != 0 {
		t.Fatalf("case member rollback members=%d operations=%d", membersCount, rejectedOperations)
	}
}

func TestCandidateLifecycleDeferralQueryFailsClosedPostgres(t *testing.T) {
	ctx := context.Background()
	store, service := newLifecycleService(t, "lifecycle_deferral_query_error", 16000)
	fixture := loadLifecycleParityFixture(t)
	registration := registrationWithSources(fixture.CandidateRegistration, 16100, 1)
	candidate, err := service.RegisterCandidate(ctx, "deferral-query-error", registration)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.q.exec(ctx, `ALTER TABLE provenance.candidate_events DROP COLUMN payload_json`); err != nil {
		t.Fatal(err)
	}
	projection, found, err := service.GetCandidateLifecycle(ctx, candidate.CandidateID, 10)
	if err == nil || found || projection.Candidate.ID != "" {
		t.Fatalf("candidate lifecycle swallowed deferral query failure: projection=%#v found=%t err=%v", projection, found, err)
	}
}

func TestLifecycleBatchPrevalidationLeavesNoPartialMutationPostgres(t *testing.T) {
	ctx := context.Background()
	store, service := newLifecycleService(t, "lifecycle_atomic", 600)
	fixture := loadLifecycleParityFixture(t)
	registered := registerFixtureBatch(t, service, fixture)
	reviewer := reviewerIdentity()
	scope := LifecycleScope{Domain: fixture.CandidateRegistration.Domain, Visibility: "private"}
	source := registered.Receipts[0].SourceResults[0].ID
	accept := AcceptCandidateOperation{OperationMetadata: operationMetadata(610, reviewer, source), CandidateID: registered.Receipts[0].CandidateID, Accepted: acceptedRecord(611, fixture.CandidateRegistration, reviewer, source)}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{accept}}); err != nil {
		t.Fatal(err)
	}
	qualification := AppendQualificationOperation{OperationMetadata: operationMetadata(612, reviewer, source), RecordID: accept.Accepted.Record.ID, Qualification: "This must roll back with the invalid later operation."}
	invalidEnd := EndRelationshipOperation{OperationMetadata: operationMetadata(613, reviewer, source), RelationshipID: semanticID(699), Reason: "No such relationship exists."}
	if _, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{qualification, invalidEnd}}); err == nil {
		t.Fatal("invalid later operation unexpectedly succeeded")
	}
	var events, operations int
	if err := store.q.queryRow(ctx, `SELECT (SELECT count(*) FROM provenance.record_events WHERE record_id=$1),(SELECT count(*) FROM provenance.operation_history WHERE id IN ($2,$3))`, string(accept.Accepted.Record.ID), string(qualification.OperationID), string(invalidEnd.OperationID)).Scan(&events, &operations); err != nil {
		t.Fatal(err)
	}
	if events != 0 || operations != 0 {
		t.Fatalf("failed operation batch retained events=%d operations=%d", events, operations)
	}
}

func TestConcurrentTerminalActionsChooseOneMonotonicWinnerPostgres(t *testing.T) {
	ctx := context.Background()
	store, service := newLifecycleService(t, "lifecycle_terminal_race", 700)
	fixture := loadLifecycleParityFixture(t)
	registration := fixture.CandidateRegistration
	registration.Sources = append([]SourceRegistration(nil), registration.Sources...)
	registration.Sources[0].SourceReferenceID = semanticID(701)
	registered, err := service.RegisterCandidate(ctx, "terminal-race", registration)
	if err != nil {
		t.Fatal(err)
	}
	reviewer := reviewerIdentity()
	scope := LifecycleScope{Domain: registration.Domain, Visibility: registration.Visibility}
	const contenders = 16
	type attempt struct {
		operation RejectCandidateOperation
		receipt   ManualOperationBatchReceipt
		err       error
	}
	results := make(chan attempt, contenders)
	var wait sync.WaitGroup
	wait.Add(contenders)
	for index := range contenders {
		go func(index int) {
			defer wait.Done()
			operation := RejectCandidateOperation{OperationMetadata: operationMetadata(710+index, reviewer), CandidateID: registered.CandidateID, Reason: fmt.Sprintf("terminal contender %d", index)}
			receipt, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{operation}})
			results <- attempt{operation, receipt, err}
		}(index)
	}
	wait.Wait()
	close(results)
	successes := 0
	conflicts := 0
	var winner RejectCandidateOperation
	for result := range results {
		if result.err == nil {
			successes++
			winner = result.operation
			continue
		}
		var terminal *CandidateTerminalConflictError
		if errors.As(result.err, &terminal) {
			conflicts++
			continue
		}
		t.Fatalf("unexpected terminal race error: %v", result.err)
	}
	if successes != 1 || conflicts != contenders-1 {
		t.Fatalf("terminal race successes=%d conflicts=%d", successes, conflicts)
	}
	replay, err := service.ApplyOperations(ctx, ManualOperationBatch{Scope: scope, Producer: reviewer, Operations: []LifecycleOperation{winner}})
	if err != nil || !replay.Replayed {
		t.Fatalf("winner replay=%#v err=%v", replay, err)
	}
	projection, found, err := service.GetCandidateLifecycle(ctx, registered.CandidateID, 10)
	if err != nil || !found || projection.EffectiveState != "rejected" || projection.WinningOperationID == nil || *projection.WinningOperationID != winner.OperationID {
		t.Fatalf("terminal projection=%#v found=%t err=%v", projection, found, err)
	}
	var candidates, events, operations int
	if err := store.q.queryRow(ctx, `SELECT (SELECT count(*) FROM provenance.candidates WHERE id=$1),(SELECT count(*) FROM provenance.candidate_events WHERE candidate_id=$1 AND event_type IN ('accepted','consolidated','rejected')),(SELECT count(*) FROM provenance.operation_history)`, string(registered.CandidateID)).Scan(&candidates, &events, &operations); err != nil {
		t.Fatal(err)
	}
	if candidates != 1 || events != 1 || operations != 1 {
		t.Fatalf("terminal race ledger candidates=%d events=%d operations=%d", candidates, events, operations)
	}
}

func slicesContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
