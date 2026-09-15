package provenance

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

//go:embed testdata/store_contract.json
var storeContractFixtureJSON []byte

type storeContractFixture struct {
	Candidates            []Candidate           `json:"candidates"`
	Sources               []SourceReference     `json:"sources"`
	Records               []Record              `json:"records"`
	ProcessingRun         ProcessingRun         `json:"processing_run"`
	Operation             Operation             `json:"operation"`
	ResolutionCase        ResolutionCase        `json:"resolution_case"`
	CaseMembers           []CaseMember          `json:"case_members"`
	CaseEvent             CaseEvent             `json:"case_event"`
	Relationship          Relationship          `json:"relationship"`
	EvidenceRegistration  EvidenceRegistration  `json:"evidence_registration"`
	CandidateEvidenceLink CandidateEvidenceLink `json:"candidate_evidence_link"`
	CandidateLineage      CandidateLineage      `json:"candidate_lineage"`
	CandidateEvent        LedgerEvent           `json:"candidate_event"`
	RecordEvent           LedgerEvent           `json:"record_event"`
	RelationshipEvent     LedgerEvent           `json:"relationship_event"`
	RegistrationReplay    RegistrationReplay    `json:"registration_replay"`
}

func loadStoreContractFixture(t *testing.T) storeContractFixture {
	t.Helper()
	var fixture storeContractFixture
	if err := json.Unmarshal(storeContractFixtureJSON, &fixture); err != nil {
		t.Fatalf("decode store contract fixture: %v", err)
	}
	if len(fixture.Candidates) != 2 || len(fixture.Sources) != 3 || len(fixture.Records) != 2 {
		t.Fatalf("incomplete store contract fixture: %#v", fixture)
	}
	return fixture
}

func seedStoreContract(t *testing.T, store *Store, fixture storeContractFixture) {
	t.Helper()
	ctx := context.Background()
	err := store.Transact(ctx, func(tx *Store) error {
		for _, candidate := range fixture.Candidates {
			if err := tx.AppendCandidate(ctx, candidate); err != nil {
				return err
			}
		}
		for _, source := range fixture.Sources {
			if err := tx.AppendSourceReference(ctx, source); err != nil {
				return err
			}
		}
		for _, record := range fixture.Records {
			if err := tx.AppendRecord(ctx, record); err != nil {
				return err
			}
			if err := tx.LinkRecordSource(ctx, RecordSource{
				RecordID: record.ID, SourceReferenceID: fixture.Sources[0].ID,
				LinkedAt: record.CreatedAt,
			}); err != nil {
				return err
			}
			if err := tx.LinkRecordProducer(ctx, RecordProducer{
				RecordID: record.ID, CandidateID: &fixture.Candidates[0].ID,
				Producer: fixture.Relationship.CreatedBy, LinkedAt: record.CreatedAt,
			}); err != nil {
				return err
			}
		}
		if err := tx.AppendProcessingRun(ctx, fixture.ProcessingRun); err != nil {
			return err
		}
		if err := tx.AppendOperation(ctx, fixture.Operation); err != nil {
			return err
		}
		if err := tx.AppendResolutionCase(ctx, fixture.ResolutionCase); err != nil {
			return err
		}
		for _, member := range fixture.CaseMembers {
			if err := tx.AppendCaseMember(ctx, member); err != nil {
				return err
			}
		}
		if err := tx.AppendCaseEvent(ctx, fixture.CaseEvent); err != nil {
			return err
		}
		if err := tx.AppendRelationship(ctx, fixture.Relationship); err != nil {
			return err
		}
		if err := tx.AppendCandidateEvent(ctx, fixture.CandidateEvent); err != nil {
			return err
		}
		if err := tx.AppendRecordEvent(ctx, fixture.RecordEvent); err != nil {
			return err
		}
		if err := tx.AppendRelationshipEvent(ctx, fixture.RelationshipEvent); err != nil {
			return err
		}
		if err := tx.AppendEvidenceRegistration(ctx, fixture.EvidenceRegistration); err != nil {
			return err
		}
		if err := tx.AppendCandidateEvidenceLink(ctx, fixture.CandidateEvidenceLink); err != nil {
			return err
		}
		if err := tx.AppendCandidateLineage(ctx, fixture.CandidateLineage); err != nil {
			return err
		}
		_, _, err := tx.PutRegistrationReplay(ctx, fixture.RegistrationReplay)
		return err
	})
	if err != nil {
		t.Fatalf("seed store contract: %v", err)
	}
}

func TestStoreContractPreservesExactObjectsBoundedNavigationAndAppendHistoryPostgres(t *testing.T) {
	ctx := context.Background()
	pool, store, _ := migratedStore(t, "store_contract")
	fixture := loadStoreContractFixture(t)
	seedStoreContract(t, store, fixture)

	candidate, found, err := store.GetCandidate(ctx, fixture.Candidates[0].ID)
	if err != nil || !found || candidate.ID != fixture.Candidates[0].ID || !jsonEqual(candidate.Payload, fixture.Candidates[0].Payload) {
		t.Fatalf("candidate = %#v, found=%t, err=%v", candidate, found, err)
	}
	source, found, err := store.GetSourceReference(ctx, fixture.Sources[1].ID)
	if err != nil || !found || source.Status != "unresolved" || source.VerificationPosture != "unverified" || source.GapReason == nil {
		t.Fatalf("unresolved source posture = %#v, found=%t, err=%v", source, found, err)
	}
	record, found, err := store.GetRecord(ctx, fixture.Records[0].ID)
	if err != nil || !found || record.ID != fixture.Records[0].ID || record.Temporal.Interpretation != fixture.Records[0].Temporal.Interpretation {
		t.Fatalf("record = %#v, found=%t, err=%v", record, found, err)
	}
	if sourceIDs, err := store.ListRecordSourceIDs(ctx, record.ID, 10); err != nil || len(sourceIDs) != 1 || sourceIDs[0] != fixture.Sources[0].ID {
		t.Fatalf("record sources = %v, err=%v", sourceIDs, err)
	}
	if producers, err := store.ListRecordProducers(ctx, record.ID, 10); err != nil || len(producers) != 1 || producers[0].Producer.ProducerID != "invented-store-worker" {
		t.Fatalf("record producers = %#v, err=%v", producers, err)
	}

	resolutionCase, found, err := store.GetResolutionCase(ctx, fixture.ResolutionCase.ID)
	if err != nil || !found || resolutionCase.ID != fixture.ResolutionCase.ID {
		t.Fatalf("case = %#v, found=%t, err=%v", resolutionCase, found, err)
	}
	if members, err := store.ListCaseMembers(ctx, resolutionCase.ID, 10); err != nil || len(members) != 2 {
		t.Fatalf("case members = %#v, err=%v", members, err)
	}
	if events, err := store.ListCaseEvents(ctx, resolutionCase.ID, 10); err != nil || len(events) != 1 || events[0].ID != fixture.CaseEvent.ID {
		t.Fatalf("case events = %#v, err=%v", events, err)
	}
	relationship, found, err := store.GetRelationship(ctx, fixture.Relationship.ID)
	if err != nil || !found || relationship.FromRecordID != fixture.Relationship.FromRecordID {
		t.Fatalf("relationship = %#v, found=%t, err=%v", relationship, found, err)
	}
	operation, found, err := store.GetOperation(ctx, fixture.Operation.ID)
	if err != nil || !found || operation.InputHash != fixture.Operation.InputHash {
		t.Fatalf("operation = %#v, found=%t, err=%v", operation, found, err)
	}
	processingRun, found, err := store.GetProcessingRun(ctx, fixture.ProcessingRun.ID)
	if err != nil || !found || processingRun.InputHash != fixture.ProcessingRun.InputHash {
		t.Fatalf("processing run = %#v, found=%t, err=%v", processingRun, found, err)
	}
	evidence, found, err := store.GetEvidenceRegistration(ctx, fixture.Sources[1].ID)
	if err != nil || !found || evidence.ID != fixture.EvidenceRegistration.ID {
		t.Fatalf("evidence registration = %#v, found=%t, err=%v", evidence, found, err)
	}
	if ids, err := store.ListCandidateEvidenceSourceIDs(ctx, fixture.Candidates[0].ID, 10); err != nil || len(ids) != 1 || ids[0] != fixture.Sources[1].ID {
		t.Fatalf("candidate evidence = %v, err=%v", ids, err)
	}
	if ids, err := store.ListCandidateLineage(ctx, fixture.Candidates[0].ID, 10); err != nil || len(ids) != 1 || ids[0] != fixture.Candidates[1].ID {
		t.Fatalf("candidate lineage = %v, err=%v", ids, err)
	}
	candidateEvents, candidateEventErr := store.ListCandidateEvents(ctx, fixture.Candidates[0].ID, 10)
	recordEvents, recordEventErr := store.ListRecordEvents(ctx, fixture.Records[0].ID, 10)
	relationshipEvents, relationshipEventErr := store.ListRelationshipEvents(ctx, fixture.Relationship.ID, 10)
	if candidateEventErr != nil || recordEventErr != nil || relationshipEventErr != nil {
		t.Fatalf("list ledger events: candidate=%v record=%v relationship=%v", candidateEventErr, recordEventErr, relationshipEventErr)
	}
	for name, events := range map[string][]LedgerEvent{
		"candidate": candidateEvents, "record": recordEvents, "relationship": relationshipEvents,
	} {
		if len(events) != 1 {
			t.Fatalf("%s events = %#v", name, events)
		}
	}

	first, err := store.ListCandidates(ctx, PageRequest{Limit: 1, Domain: "invented-slice-one", Visibility: "private"})
	if err != nil || !first.Truncated || len(first.Items) != 1 || first.Items[0].ID != fixture.Candidates[1].ID {
		t.Fatalf("first candidate page = %#v, err=%v", first, err)
	}
	second, err := store.ListCandidates(ctx, PageRequest{
		Limit: 1, Domain: "invented-slice-one", Visibility: "private",
		AfterTime: first.NextTime, AfterID: first.NextID,
	})
	if err != nil || second.Truncated || len(second.Items) != 1 || second.Items[0].ID != fixture.Candidates[0].ID {
		t.Fatalf("second candidate page = %#v, err=%v", second, err)
	}
	if _, err := store.ListRecords(ctx, PageRequest{Limit: MaximumPageLimit + 1}); err == nil {
		t.Fatal("oversized record page was accepted")
	}
	if page, err := store.ListRelationships(ctx, PageRequest{Limit: 10, Domain: "invented-slice-one", Visibility: "private"}); err != nil || len(page.Items) != 1 {
		t.Fatalf("relationship page = %#v, err=%v", page, err)
	}
	if page, err := store.ListResolutionCases(ctx, PageRequest{Limit: 10, Domain: "invented-slice-one", Visibility: "private"}); err != nil || len(page.Items) != 1 {
		t.Fatalf("case page = %#v, err=%v", page, err)
	}

	replayed, replay, err := store.PutRegistrationReplay(ctx, fixture.RegistrationReplay)
	if err != nil || !replay || replayed.ReplayKey != fixture.RegistrationReplay.ReplayKey {
		t.Fatalf("exact registration replay = %#v, replayed=%t, err=%v", replayed, replay, err)
	}
	conflicting := fixture.RegistrationReplay
	conflicting.Payload = json.RawMessage(`{"schema_version":"1.0","changed":true}`)
	if _, _, err := store.PutRegistrationReplay(ctx, conflicting); !errors.Is(err, ErrReplayConflict) {
		t.Fatalf("conflicting replay error = %v", err)
	}
	workflowConflict := conflicting
	workflowConflict.ReplayKey = "different-key-for-same-workflow-and-digest"
	if _, _, err := store.PutRegistrationReplay(ctx, workflowConflict); !errors.Is(err, ErrReplayConflict) {
		t.Fatalf("workflow/digest payload conflict error = %v", err)
	}
	var replayCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM provenance.registration_replays`).Scan(&replayCount); err != nil || replayCount != 1 {
		t.Fatalf("replay count = %d, err=%v", replayCount, err)
	}

	if _, err := pool.Exec(ctx, `UPDATE provenance.records SET claim = 'rewritten' WHERE id = $1`, string(record.ID)); !isAppendOnlyError(err) {
		t.Fatalf("record update error = %v, want append-only SQLSTATE", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM provenance.record_sources WHERE record_id = $1`, string(record.ID)); !isAppendOnlyError(err) {
		t.Fatalf("record source delete error = %v, want append-only SQLSTATE", err)
	}
	if err := store.AppendCaseMember(ctx, fixture.CaseMembers[0]); err == nil {
		t.Fatal("duplicate candidate case membership was accepted")
	}
}

func TestRegistrationReplayUsesLosslessJSONNumbersPostgres(t *testing.T) {
	ctx := context.Background()
	pool, store, _ := migratedStore(t, "replay_json_numbers")
	fixture := loadStoreContractFixture(t)
	replay := fixture.RegistrationReplay
	replay.ReplayKey = "lossless-large-integer-replay"
	replay.WorkflowKey = "lossless-large-integer-workflow"
	replay.RegistrationDigest = "sha256:lossless-large-integer"
	replay.Receipts = json.RawMessage(`{"n":9007199254740992,"nested":{"a":1,"b":2}}`)
	replay.Payload = json.RawMessage(`{"n":9007199254740992,"nested":{"a":1,"b":2}}`)
	replay.Producer.Metadata = map[string]any{
		"n":      json.Number("9007199254740992"),
		"nested": map[string]any{"a": json.Number("1"), "b": json.Number("2")},
	}

	if _, replayed, err := store.PutRegistrationReplay(ctx, replay); err != nil || replayed {
		t.Fatalf("initial lossless replay insert replayed=%t err=%v", replayed, err)
	}
	loaded, found, err := store.GetRegistrationReplay(ctx, replay.WorkflowKey, replay.RegistrationDigest)
	if err != nil || !found {
		t.Fatalf("load lossless replay found=%t err=%v", found, err)
	}
	if number, ok := loaded.Producer.Metadata["n"].(json.Number); !ok || number.String() != "9007199254740992" {
		t.Fatalf("loaded producer number = %#v (%T), want lossless json.Number", loaded.Producer.Metadata["n"], loaded.Producer.Metadata["n"])
	}

	semanticallySame := replay
	semanticallySame.Receipts = json.RawMessage(` { "nested": { "b": 2, "a": 1 }, "n": 9007199254740992 } `)
	semanticallySame.Payload = json.RawMessage("{\n  \"nested\": {\"b\": 2, \"a\": 1},\n  \"n\": 9007199254740992\n}")
	semanticallySame.Producer.Metadata = map[string]any{
		"nested": map[string]any{"b": json.Number("2"), "a": json.Number("1")},
		"n":      json.Number("9007199254740992"),
	}
	if _, replayed, err := store.PutRegistrationReplay(ctx, semanticallySame); err != nil || !replayed {
		t.Fatalf("key-order/whitespace replay replayed=%t err=%v", replayed, err)
	}

	receiptsConflict := semanticallySame
	receiptsConflict.Receipts = json.RawMessage(`{"nested":{"a":1,"b":2},"n":9007199254740993}`)
	if _, _, err := store.PutRegistrationReplay(ctx, receiptsConflict); !errors.Is(err, ErrReplayConflict) {
		t.Fatalf("large-integer receipts conflict error = %v", err)
	}

	payloadConflict := semanticallySame
	payloadConflict.Payload = json.RawMessage(`{"nested":{"a":1,"b":2},"n":9007199254740993}`)
	if _, _, err := store.PutRegistrationReplay(ctx, payloadConflict); !errors.Is(err, ErrReplayConflict) {
		t.Fatalf("large-integer payload conflict error = %v", err)
	}

	producerConflict := semanticallySame
	producerConflict.Producer.Metadata = map[string]any{
		"nested": map[string]any{"a": json.Number("1"), "b": json.Number("2")},
		"n":      json.Number("9007199254740993"),
	}
	if _, _, err := store.PutRegistrationReplay(ctx, producerConflict); !errors.Is(err, ErrReplayConflict) {
		t.Fatalf("large-integer producer metadata conflict error = %v", err)
	}

	var replayCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM provenance.registration_replays`).Scan(&replayCount); err != nil || replayCount != 1 {
		t.Fatalf("lossless replay count = %d, err=%v", replayCount, err)
	}
}

func TestStoreTransactionAndConstraintsFailWithoutPartialMutationPostgres(t *testing.T) {
	ctx := context.Background()
	pool, store, _ := migratedStore(t, "store_rollback")
	fixture := loadStoreContractFixture(t)
	candidate := fixture.Candidates[0]
	source := fixture.Sources[0]
	err := store.Transact(ctx, func(tx *Store) error {
		if err := tx.AppendCandidate(ctx, candidate); err != nil {
			return err
		}
		if err := tx.AppendSourceReference(ctx, source); err != nil {
			return err
		}
		return tx.AppendCandidate(ctx, candidate)
	})
	if err == nil {
		t.Fatal("duplicate candidate transaction unexpectedly succeeded")
	}
	var candidates, sources int
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM provenance.candidates),
		       (SELECT count(*) FROM provenance.source_references)
	`).Scan(&candidates, &sources); err != nil {
		t.Fatal(err)
	}
	if candidates != 0 || sources != 0 {
		t.Fatalf("failed transaction retained candidates=%d sources=%d", candidates, sources)
	}

	invalid := fixture.Sources[0]
	invalid.CandidateID = nil
	invalid.VerificationPosture = "unverified"
	if err := store.AppendSourceReference(ctx, invalid); err == nil {
		t.Fatal("resolved source with unverified posture was accepted")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM provenance.source_references`).Scan(&sources); err != nil || sources != 0 {
		t.Fatalf("invalid source mutated storage: sources=%d err=%v", sources, err)
	}
}

func TestConcurrentStorePrimitivesAndStablePaginationPostgres(t *testing.T) {
	ctx := context.Background()
	pool, store, _ := migratedStore(t, "store_race")
	fixture := loadStoreContractFixture(t)
	const workers = 24
	var wait sync.WaitGroup
	errorsFound := make(chan error, workers)
	wait.Add(workers)
	for index := range workers {
		go func(index int) {
			defer wait.Done()
			candidate := fixture.Candidates[0]
			candidate.ID = SemanticID(fmt.Sprintf("00000000-0000-4000-8000-%012d", 1000+index))
			candidate.RegisteredAt = fixture.Candidates[0].RegisteredAt.Add(time.Duration(index) * time.Millisecond)
			source := fixture.Sources[0]
			source.ID = SemanticID(fmt.Sprintf("00000000-0000-4000-8000-%012d", 2000+index))
			source.CandidateID = &candidate.ID
			source.ResolutionAt = candidate.RegisteredAt
			errorsFound <- store.Transact(ctx, func(tx *Store) error {
				if err := tx.AppendCandidate(ctx, candidate); err != nil {
					return err
				}
				return tx.AppendSourceReference(ctx, source)
			})
		}(index)
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}

	seen := map[SemanticID]bool{}
	request := PageRequest{Limit: 7, Domain: "invented-slice-one", Visibility: "private"}
	for {
		page, err := store.ListCandidates(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		for _, candidate := range page.Items {
			if seen[candidate.ID] {
				t.Fatalf("candidate %s repeated across pages", candidate.ID)
			}
			seen[candidate.ID] = true
		}
		if !page.Truncated {
			break
		}
		request.AfterTime, request.AfterID = page.NextTime, page.NextID
	}
	if len(seen) != workers {
		t.Fatalf("stable pagination returned %d candidates, want %d", len(seen), workers)
	}

	duplicateCandidate := fixture.Candidates[0]
	duplicateCandidate.ID = SemanticID("00000000-0000-4000-8000-000000009999")
	duplicateSource := fixture.Sources[0]
	duplicateSource.ID = SemanticID("00000000-0000-4000-8000-000000008999")
	duplicateSource.CandidateID = &duplicateCandidate.ID
	const contenders = 8
	successes := atomicCounter{}
	wait = sync.WaitGroup{}
	wait.Add(contenders)
	for range contenders {
		go func() {
			defer wait.Done()
			if err := store.Transact(ctx, func(tx *Store) error {
				if err := tx.AppendCandidate(ctx, duplicateCandidate); err != nil {
					return err
				}
				return tx.AppendSourceReference(ctx, duplicateSource)
			}); err == nil {
				successes.Add()
			}
		}()
	}
	wait.Wait()
	if successes.Load() != 1 {
		t.Fatalf("concurrent duplicate successes = %d, want 1", successes.Load())
	}
	var candidateCount, sourceCount int
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM provenance.candidates WHERE id = $1),
		       (SELECT count(*) FROM provenance.source_references WHERE id = $2)
	`, string(duplicateCandidate.ID), string(duplicateSource.ID)).Scan(&candidateCount, &sourceCount); err != nil {
		t.Fatal(err)
	}
	if candidateCount != 1 || sourceCount != 1 {
		t.Fatalf("concurrent duplicate rows candidates=%d sources=%d", candidateCount, sourceCount)
	}

	replay := fixture.RegistrationReplay
	replay.ReplayKey = "concurrent-replay-key"
	replay.WorkflowKey = "concurrent-replay-workflow"
	replay.RegistrationDigest = "sha256:concurrent-replay"
	replay.RegisteredAt = replay.RegisteredAt.Add(123 * time.Nanosecond)
	replaySuccesses := atomicCounter{}
	replayReturns := atomicCounter{}
	replayErrors := make(chan error, contenders)
	wait = sync.WaitGroup{}
	wait.Add(contenders)
	for range contenders {
		go func() {
			defer wait.Done()
			_, replayed, err := store.PutRegistrationReplay(ctx, replay)
			if err == nil {
				if replayed {
					replayReturns.Add()
				} else {
					replaySuccesses.Add()
				}
			}
			replayErrors <- err
		}()
	}
	wait.Wait()
	close(replayErrors)
	for err := range replayErrors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if replaySuccesses.Load() != 1 || replayReturns.Load() != contenders-1 {
		t.Fatalf("concurrent replay inserted=%d replayed=%d", replaySuccesses.Load(), replayReturns.Load())
	}
}

type atomicCounter struct {
	mu    sync.Mutex
	value int
}

func (counter *atomicCounter) Add() {
	counter.mu.Lock()
	counter.value++
	counter.mu.Unlock()
}

func (counter *atomicCounter) Load() int {
	counter.mu.Lock()
	defer counter.mu.Unlock()
	return counter.value
}

func isAppendOnlyError(err error) bool {
	var pgError *pgconn.PgError
	return errors.As(err, &pgError) && pgError.Code == "55000"
}
