package provenance

import (
	"context"
	"errors"
	"testing"
)

func TestAcceptedRecordProducerContextAcrossTasks(t *testing.T) {
	registration := loadLifecycleParityFixture(t).CandidateRegistration
	registration.Producer = ProducerIdentity{ProducerID: "mina", ProducerKind: "working_agent", TaskID: "capture-task", RunID: idPointer(semanticID(801))}
	source := semanticID(802)
	candidate := &candidateValidationState{
		candidate:    Candidate{Domain: registration.Domain, Visibility: registration.Visibility},
		registration: registration, sourceIDs: []SemanticID{source},
	}
	reviewer := registration.Producer
	reviewer.TaskID = "review-task"
	reviewer.RunID = idPointer(semanticID(803))
	metadata := operationMetadata(804, reviewer, source)
	accepted := acceptedRecord(805, registration, reviewer, source)
	if err := validateAcceptedRecord(accepted, candidate, metadata); err != nil {
		t.Fatalf("same agent in another task/run must retain both contexts: %v", err)
	}
	if len(accepted.ProducerHistory) != 2 || accepted.ProducerHistory[0].TaskID != "capture-task" || accepted.ProducerHistory[1].TaskID != "review-task" {
		t.Fatalf("execution context history was collapsed: %#v", accepted.ProducerHistory)
	}
	for _, scenario := range []struct {
		name string
		edit func(*AcceptedRecord, *OperationMetadata)
	}{
		{"producer kind reuse", func(a *AcceptedRecord, m *OperationMetadata) {
			m.Producer.ProducerKind = "different_kind"
			a.ProducerHistory[1] = m.Producer
		}},
		{"missing original context", func(a *AcceptedRecord, _ *OperationMetadata) { a.ProducerHistory = a.ProducerHistory[1:] }},
		{"rewritten original task", func(a *AcceptedRecord, _ *OperationMetadata) { a.ProducerHistory[0].TaskID = "rewritten-task" }},
		{"reversed history", func(a *AcceptedRecord, _ *OperationMetadata) {
			a.ProducerHistory[0], a.ProducerHistory[1] = a.ProducerHistory[1], a.ProducerHistory[0]
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			a := acceptedRecord(805, registration, reviewer, source)
			m := metadata
			scenario.edit(&a, &m)
			if err := validateAcceptedRecord(a, candidate, m); !errors.Is(err, ErrAcceptedProducerHistory) {
				t.Fatalf("invalid attribution accepted or misclassified: %v", err)
			}
		})
	}
}

func TestAcceptedRecordAcrossTasksPreservesDatabaseHistoryPostgres(t *testing.T) {
	ctx := context.Background()
	store, service := newLifecycleService(t, "same_agent_review_context", 820)
	registration := loadLifecycleParityFixture(t).CandidateRegistration
	registration.Producer = ProducerIdentity{ProducerID: "mina", ProducerKind: "working_agent", TaskID: "capture-task"}
	registered, err := service.RegisterCandidateBatch(ctx, CandidateRegistrationBatch{ReplayKey: "same-agent-capture", Candidates: []CandidateRegistration{registration}})
	if err != nil {
		t.Fatal(err)
	}
	receipt := registered.Receipts[0]
	sources := make([]SemanticID, len(receipt.SourceResults))
	for index, source := range receipt.SourceResults {
		sources[index] = source.ID
	}
	reviewer := registration.Producer
	reviewer.TaskID = "review-task"
	accepted := acceptedRecord(825, registration, reviewer, sources...)
	operation := AcceptCandidateOperation{OperationMetadata: operationMetadata(826, reviewer, sources...), CandidateID: receipt.CandidateID, Accepted: accepted}
	batch := ManualOperationBatch{Scope: LifecycleScope{Domain: registration.Domain, Visibility: registration.Visibility}, Producer: reviewer, Operations: []LifecycleOperation{operation}}
	if _, err := service.ApplyOperations(ctx, batch); err != nil {
		t.Fatalf("same producer's distinct capture/review contexts must both persist: %v", err)
	}
	history, err := store.ListRecordProducers(ctx, accepted.Record.ID, 10)
	if err != nil || len(history) != 2 {
		t.Fatalf("producer history = %#v, err = %v", history, err)
	}
	if history[0].CandidateID == nil || *history[0].CandidateID != receipt.CandidateID || history[1].CandidateID != nil ||
		history[0].Producer.TaskID != "capture-task" || history[1].Producer.TaskID != "review-task" {
		t.Fatalf("capture/review linkage or task context lost: %#v", history)
	}
	if replay, err := service.ApplyOperations(ctx, batch); err != nil || !replay.Replayed {
		t.Fatalf("exact review replay = %#v, err = %v", replay, err)
	}
}
