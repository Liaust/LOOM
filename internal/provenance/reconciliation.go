package provenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const MaximumOperationBatch = 100

var ErrLifecycleConflict = errors.New("semantic lifecycle transition conflicts with durable state")

type CandidateTerminalConflictError struct {
	CandidateID        SemanticID
	EffectiveState     string
	WinningOperationID SemanticID
}

func (err *CandidateTerminalConflictError) Error() string {
	return fmt.Sprintf("candidate %s is already %s by operation %s", err.CandidateID, err.EffectiveState, err.WinningOperationID)
}

func (err *CandidateTerminalConflictError) Unwrap() error { return ErrLifecycleConflict }

type LifecycleScope struct {
	Domain     string `json:"domain"`
	Visibility string `json:"visibility"`
}

// OperationMetadata records the semantic producer of an interpretation. It
// intentionally contains no authenticated LOOM request actor or node.
type OperationMetadata struct {
	SchemaVersion     string           `json:"schema_version"`
	OperationID       SemanticID       `json:"operation_id"`
	OccurredAt        time.Time        `json:"occurred_at"`
	Producer          ProducerIdentity `json:"producer"`
	EvidenceSourceIDs []SemanticID     `json:"evidence_source_reference_ids,omitempty"`
}

type LifecycleOperation interface {
	lifecycleOperation()
	lifecycleKind() string
	lifecycleMetadata() OperationMetadata
	lifecycleLockKeys() []string
}

type AcceptedRecord struct {
	Record               Record             `json:"record"`
	SourceReferenceIDs   []SemanticID       `json:"source_reference_ids"`
	ProducerHistory      []ProducerIdentity `json:"producer_history"`
	SourceActor          *ActorReference    `json:"source_actor,omitempty"`
	ArtifactAuthor       *ActorReference    `json:"artifact_author,omitempty"`
	ApprovingActor       *ActorReference    `json:"approving_actor,omitempty"`
	RepresentationEvents []LedgerEvent      `json:"representation_events,omitempty"`
}

type AcceptCandidateOperation struct {
	OperationMetadata
	CandidateID SemanticID     `json:"candidate_id"`
	Accepted    AcceptedRecord `json:"accepted_record"`
}

func (AcceptCandidateOperation) lifecycleOperation()   {}
func (AcceptCandidateOperation) lifecycleKind() string { return "accept_candidate" }
func (operation AcceptCandidateOperation) lifecycleMetadata() OperationMetadata {
	return operation.OperationMetadata
}
func (operation AcceptCandidateOperation) lifecycleLockKeys() []string {
	return []string{"candidate:" + string(operation.CandidateID), "record:" + string(operation.Accepted.Record.ID)}
}

type ConsolidateCandidateOperation struct {
	OperationMetadata
	CandidateID SemanticID `json:"candidate_id"`
	RecordID    SemanticID `json:"record_id"`
	Rationale   string     `json:"rationale"`
}

func (ConsolidateCandidateOperation) lifecycleOperation()   {}
func (ConsolidateCandidateOperation) lifecycleKind() string { return "consolidate_candidate" }
func (operation ConsolidateCandidateOperation) lifecycleMetadata() OperationMetadata {
	return operation.OperationMetadata
}
func (operation ConsolidateCandidateOperation) lifecycleLockKeys() []string {
	return []string{"candidate:" + string(operation.CandidateID), "record:" + string(operation.RecordID)}
}

type RejectCandidateOperation struct {
	OperationMetadata
	CandidateID SemanticID `json:"candidate_id"`
	Reason      string     `json:"reason"`
}

func (RejectCandidateOperation) lifecycleOperation()   {}
func (RejectCandidateOperation) lifecycleKind() string { return "reject_candidate" }
func (operation RejectCandidateOperation) lifecycleMetadata() OperationMetadata {
	return operation.OperationMetadata
}
func (operation RejectCandidateOperation) lifecycleLockKeys() []string {
	return []string{"candidate:" + string(operation.CandidateID)}
}

type DeferCandidateOperation struct {
	OperationMetadata
	CandidateID     SemanticID `json:"candidate_id"`
	ReasonCode      string     `json:"reason_code"`
	Reason          string     `json:"reason"`
	NextReviewAt    *time.Time `json:"next_review_at,omitempty"`
	NeedsCapability string     `json:"needs_capability,omitempty"`
}

func (DeferCandidateOperation) lifecycleOperation()   {}
func (DeferCandidateOperation) lifecycleKind() string { return "defer_candidate" }
func (operation DeferCandidateOperation) lifecycleMetadata() OperationMetadata {
	return operation.OperationMetadata
}
func (operation DeferCandidateOperation) lifecycleLockKeys() []string {
	return []string{"candidate:" + string(operation.CandidateID)}
}

type LinkCandidateEvidenceOperation struct {
	OperationMetadata
	CandidateID       SemanticID `json:"candidate_id"`
	SourceReferenceID SemanticID `json:"source_reference_id"`
	EvidenceContext   string     `json:"evidence_context"`
}

func (LinkCandidateEvidenceOperation) lifecycleOperation()   {}
func (LinkCandidateEvidenceOperation) lifecycleKind() string { return "link_candidate_evidence" }
func (operation LinkCandidateEvidenceOperation) lifecycleMetadata() OperationMetadata {
	return operation.OperationMetadata
}
func (operation LinkCandidateEvidenceOperation) lifecycleLockKeys() []string {
	return []string{"candidate:" + string(operation.CandidateID), "source:" + string(operation.SourceReferenceID)}
}

type AppendQualificationOperation struct {
	OperationMetadata
	RecordID       SemanticID `json:"record_id"`
	Qualification  string     `json:"qualification"`
	MarksAmbiguity bool       `json:"marks_ambiguity"`
}

func (AppendQualificationOperation) lifecycleOperation()   {}
func (AppendQualificationOperation) lifecycleKind() string { return "append_qualification" }
func (operation AppendQualificationOperation) lifecycleMetadata() OperationMetadata {
	return operation.OperationMetadata
}
func (operation AppendQualificationOperation) lifecycleLockKeys() []string {
	return []string{"record:" + string(operation.RecordID)}
}

type AppendTemporalInterpretationOperation struct {
	OperationMetadata
	RecordID SemanticID             `json:"record_id"`
	Temporal TemporalInterpretation `json:"temporal_interpretation"`
}

func (AppendTemporalInterpretationOperation) lifecycleOperation() {}
func (AppendTemporalInterpretationOperation) lifecycleKind() string {
	return "append_temporal_interpretation"
}
func (operation AppendTemporalInterpretationOperation) lifecycleMetadata() OperationMetadata {
	return operation.OperationMetadata
}
func (operation AppendTemporalInterpretationOperation) lifecycleLockKeys() []string {
	return []string{"record:" + string(operation.RecordID)}
}

type AppendAssertionPostureOperation struct {
	OperationMetadata
	RecordID         SemanticID `json:"record_id"`
	AssertionPosture string     `json:"assertion_posture"`
	Rationale        string     `json:"rationale"`
}

func (AppendAssertionPostureOperation) lifecycleOperation()   {}
func (AppendAssertionPostureOperation) lifecycleKind() string { return "append_assertion_posture" }
func (operation AppendAssertionPostureOperation) lifecycleMetadata() OperationMetadata {
	return operation.OperationMetadata
}
func (operation AppendAssertionPostureOperation) lifecycleLockKeys() []string {
	return []string{"record:" + string(operation.RecordID)}
}

type CaseMemberReference struct {
	MemberType string     `json:"member_type"`
	MemberID   SemanticID `json:"member_id"`
	Role       string     `json:"role"`
}

type CreateResolutionCaseOperation struct {
	OperationMetadata
	Case    ResolutionCase        `json:"case"`
	Members []CaseMemberReference `json:"members"`
	Events  []CaseEvent           `json:"events"`
}

func (CreateResolutionCaseOperation) lifecycleOperation()   {}
func (CreateResolutionCaseOperation) lifecycleKind() string { return "create_resolution_case" }
func (operation CreateResolutionCaseOperation) lifecycleMetadata() OperationMetadata {
	return operation.OperationMetadata
}
func (operation CreateResolutionCaseOperation) lifecycleLockKeys() []string {
	keys := []string{"case:" + string(operation.Case.ID)}
	for _, member := range operation.Members {
		keys = append(keys, member.MemberType+":"+string(member.MemberID))
	}
	return keys
}

type UpdateResolutionCaseOperation struct {
	OperationMetadata
	CaseID SemanticID `json:"case_id"`
	Event  CaseEvent  `json:"event"`
}

func (UpdateResolutionCaseOperation) lifecycleOperation()   {}
func (UpdateResolutionCaseOperation) lifecycleKind() string { return "update_resolution_case" }
func (operation UpdateResolutionCaseOperation) lifecycleMetadata() OperationMetadata {
	return operation.OperationMetadata
}
func (operation UpdateResolutionCaseOperation) lifecycleLockKeys() []string {
	return []string{"case:" + string(operation.CaseID)}
}

type CloseResolutionCaseOperation struct {
	OperationMetadata
	CaseID  SemanticID `json:"case_id"`
	Event   CaseEvent  `json:"event"`
	Outcome string     `json:"outcome"`
}

func (CloseResolutionCaseOperation) lifecycleOperation()   {}
func (CloseResolutionCaseOperation) lifecycleKind() string { return "close_resolution_case" }
func (operation CloseResolutionCaseOperation) lifecycleMetadata() OperationMetadata {
	return operation.OperationMetadata
}
func (operation CloseResolutionCaseOperation) lifecycleLockKeys() []string {
	return []string{"case:" + string(operation.CaseID)}
}

type AttachCaseMemberOperation struct {
	OperationMetadata
	CaseID SemanticID          `json:"case_id"`
	Member CaseMemberReference `json:"member"`
}

func (AttachCaseMemberOperation) lifecycleOperation()   {}
func (AttachCaseMemberOperation) lifecycleKind() string { return "attach_case_member" }
func (operation AttachCaseMemberOperation) lifecycleMetadata() OperationMetadata {
	return operation.OperationMetadata
}
func (operation AttachCaseMemberOperation) lifecycleLockKeys() []string {
	return []string{"case:" + string(operation.CaseID), operation.Member.MemberType + ":" + string(operation.Member.MemberID)}
}

type AddRelationshipOperation struct {
	OperationMetadata
	Relationship Relationship `json:"relationship"`
}

func (AddRelationshipOperation) lifecycleOperation()   {}
func (AddRelationshipOperation) lifecycleKind() string { return "add_relationship" }
func (operation AddRelationshipOperation) lifecycleMetadata() OperationMetadata {
	return operation.OperationMetadata
}
func (operation AddRelationshipOperation) lifecycleLockKeys() []string {
	keys := []string{"relationship:" + string(operation.Relationship.ID), "record:" + string(operation.Relationship.FromRecordID), "record:" + string(operation.Relationship.ToRecordID)}
	if operation.Relationship.ResolutionCaseID != nil {
		keys = append(keys, "case:"+string(*operation.Relationship.ResolutionCaseID))
	}
	return keys
}

type EndRelationshipOperation struct {
	OperationMetadata
	RelationshipID SemanticID `json:"relationship_id"`
	Reason         string     `json:"reason"`
}

func (EndRelationshipOperation) lifecycleOperation()   {}
func (EndRelationshipOperation) lifecycleKind() string { return "end_relationship" }
func (operation EndRelationshipOperation) lifecycleMetadata() OperationMetadata {
	return operation.OperationMetadata
}
func (operation EndRelationshipOperation) lifecycleLockKeys() []string {
	return []string{"relationship:" + string(operation.RelationshipID)}
}

type ManualOperationBatch struct {
	Scope           LifecycleScope       `json:"scope"`
	Producer        ProducerIdentity     `json:"producer"`
	ProcessingRunID *SemanticID          `json:"processing_run_id,omitempty"`
	PackID          *SemanticID          `json:"pack_id,omitempty"`
	Operations      []LifecycleOperation `json:"-"`
}

type OperationReceipt struct {
	SchemaVersion  string     `json:"schema_version"`
	OperationID    SemanticID `json:"operation_id"`
	OperationType  string     `json:"operation_type"`
	Result         string     `json:"result"`
	ObjectID       SemanticID `json:"object_id"`
	EffectiveState string     `json:"effective_state,omitempty"`
	Replayed       bool       `json:"replayed"`
}

type ManualOperationBatchReceipt struct {
	SchemaVersion string             `json:"schema_version"`
	Result        string             `json:"result"`
	Replayed      bool               `json:"replayed"`
	Receipts      []OperationReceipt `json:"receipts"`
}

type preparedOperation struct {
	operation LifecycleOperation
	payload   json.RawMessage
	digest    string
}

func (service *Service) ApplyOperations(ctx context.Context, batch ManualOperationBatch) (ManualOperationBatchReceipt, error) {
	prepared, lockKeys, err := prepareOperationBatch(batch)
	if err != nil {
		return ManualOperationBatchReceipt{}, err
	}
	var result ManualOperationBatchReceipt
	err = service.store.Transact(ctx, func(tx *Store) error {
		if err := lockTransactionKeys(ctx, tx, lockKeys...); err != nil {
			return err
		}
		replayed := make([]OperationReceipt, 0, len(prepared))
		existingCount := 0
		for _, item := range prepared {
			metadata := item.operation.lifecycleMetadata()
			stored, found, err := tx.GetOperation(ctx, metadata.OperationID)
			if err != nil {
				return err
			}
			if !found {
				continue
			}
			existingCount++
			producerJSON, _ := json.Marshal(metadata.Producer)
			if stored.OperationType != item.operation.lifecycleKind() || stored.InputHash != item.digest ||
				!jsonEqual(stored.Producer, producerJSON) || !jsonEqual(stored.Operation, item.payload) {
				return ErrOperationReplayConflict
			}
			var receipt OperationReceipt
			if err := decodeLosslessJSON(stored.ResultPayload, &receipt); err != nil {
				return fmt.Errorf("decode operation replay receipt: %w", err)
			}
			receipt.Replayed = true
			replayed = append(replayed, receipt)
		}
		if existingCount > 0 {
			if existingCount != len(prepared) {
				return ErrPartialOperationReplay
			}
			result = ManualOperationBatchReceipt{SchemaVersion: SchemaVersion, Result: "applied", Replayed: true, Receipts: replayed}
			return nil
		}

		state := newReconciliationState(tx, batch.Scope)
		for _, item := range prepared {
			if err := state.validate(ctx, item.operation); err != nil {
				return err
			}
		}
		receipts := make([]OperationReceipt, 0, len(prepared))
		for _, item := range prepared {
			receipt := receiptForOperation(item.operation)
			receiptJSON, err := json.Marshal(receipt)
			if err != nil {
				return fmt.Errorf("encode operation receipt: %w", err)
			}
			metadata := item.operation.lifecycleMetadata()
			producerJSON, err := json.Marshal(metadata.Producer)
			if err != nil {
				return fmt.Errorf("encode operation producer: %w", err)
			}
			if err := tx.AppendOperation(ctx, Operation{
				ID: metadata.OperationID, ProcessingRunID: batch.ProcessingRunID, PackID: batch.PackID,
				OperationType: item.operation.lifecycleKind(), SchemaVersion: SchemaVersion,
				Producer: producerJSON, OccurredAt: metadata.OccurredAt.UTC(), InputHash: item.digest,
				Result: "applied", Operation: item.payload, ResultPayload: receiptJSON,
				AppliedAt: service.clock().UTC(),
			}); err != nil {
				return err
			}
			if err := applyLifecycleOperation(ctx, tx, item.operation); err != nil {
				return err
			}
			receipts = append(receipts, receipt)
		}
		result = ManualOperationBatchReceipt{SchemaVersion: SchemaVersion, Result: "applied", Receipts: receipts}
		return nil
	})
	return result, err
}

func prepareOperationBatch(batch ManualOperationBatch) ([]preparedOperation, []string, error) {
	if err := validateRequired(batch.Scope.Domain, "operation scope domain"); err != nil {
		return nil, nil, err
	}
	if err := validateRequired(batch.Scope.Visibility, "operation scope visibility"); err != nil {
		return nil, nil, err
	}
	if err := validateProducer(batch.Producer); err != nil {
		return nil, nil, err
	}
	if len(batch.Operations) == 0 || len(batch.Operations) > MaximumOperationBatch {
		return nil, nil, fmt.Errorf("operation batch must contain between 1 and %d operations", MaximumOperationBatch)
	}
	for _, id := range []*SemanticID{batch.ProcessingRunID, batch.PackID} {
		if id != nil {
			if err := id.Validate(); err != nil {
				return nil, nil, err
			}
		}
	}
	seen := map[SemanticID]struct{}{}
	prepared := make([]preparedOperation, 0, len(batch.Operations))
	lockKeys := make([]string, 0, len(batch.Operations)*3)
	for index, operation := range batch.Operations {
		if operation == nil {
			return nil, nil, fmt.Errorf("operation %d is nil", index)
		}
		metadata := operation.lifecycleMetadata()
		if metadata.SchemaVersion != SchemaVersion {
			return nil, nil, fmt.Errorf("operation %d requires schema_version 1.0", index)
		}
		for _, check := range []error{metadata.OperationID.Validate(), validateTimestamp(metadata.OccurredAt, "operation occurred_at"), validateProducer(metadata.Producer)} {
			if check != nil {
				return nil, nil, fmt.Errorf("operation %d: %w", index, check)
			}
		}
		if !sameProducer(metadata.Producer, batch.Producer) {
			return nil, nil, fmt.Errorf("operation %d producer differs from batch producer", index)
		}
		if _, duplicate := seen[metadata.OperationID]; duplicate {
			return nil, nil, fmt.Errorf("operation id %s is duplicated in the batch", metadata.OperationID)
		}
		seen[metadata.OperationID] = struct{}{}
		if len(metadata.EvidenceSourceIDs) > MaximumSourcesPerObject {
			return nil, nil, fmt.Errorf("operation %s exceeds evidence bound", metadata.OperationID)
		}
		if hasDuplicateIDs(metadata.EvidenceSourceIDs) {
			return nil, nil, fmt.Errorf("operation %s evidence ids must be unique", metadata.OperationID)
		}
		payload, err := marshalLifecycleOperation(batch, operation)
		if err != nil {
			return nil, nil, fmt.Errorf("encode operation %d: %w", index, err)
		}
		if len(payload) > MaximumDocumentBytes {
			return nil, nil, fmt.Errorf("operation %s exceeds payload bound", metadata.OperationID)
		}
		prepared = append(prepared, preparedOperation{operation: operation, payload: payload, digest: digestJSON(payload)})
		lockKeys = append(lockKeys, "operation:"+string(metadata.OperationID))
		lockKeys = append(lockKeys, operation.lifecycleLockKeys()...)
		for _, sourceID := range metadata.EvidenceSourceIDs {
			lockKeys = append(lockKeys, "source:"+string(sourceID))
		}
	}
	return prepared, lockKeys, nil
}

func marshalLifecycleOperation(batch ManualOperationBatch, operation LifecycleOperation) (json.RawMessage, error) {
	raw, err := json.Marshal(operation)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	kind, _ := json.Marshal(operation.lifecycleKind())
	fields["operation_type"] = kind
	scope, _ := json.Marshal(batch.Scope)
	fields["semantic_scope"] = scope
	processingRunID, _ := json.Marshal(batch.ProcessingRunID)
	fields["processing_run_id"] = processingRunID
	packID, _ := json.Marshal(batch.PackID)
	fields["pack_id"] = packID
	return json.Marshal(fields)
}

type candidateValidationState struct {
	candidate      Candidate
	registration   CandidateRegistration
	sourceIDs      []SemanticID
	effectiveState string
	winnerID       SemanticID
	deferred       bool
}

type caseValidationState struct {
	item    ResolutionCase
	closed  bool
	members map[caseMemberKey]struct{}
}

type caseMemberKey struct {
	memberType string
	memberID   SemanticID
	role       string
}

type relationshipValidationState struct {
	item  Relationship
	ended bool
}

type reconciliationState struct {
	store         *Store
	scope         LifecycleScope
	candidates    map[SemanticID]*candidateValidationState
	records       map[SemanticID]Record
	recordSources map[SemanticID][]SemanticID
	cases         map[SemanticID]*caseValidationState
	relationships map[SemanticID]*relationshipValidationState
	linkedSources map[string]struct{}
	caseEventIDs  map[SemanticID]struct{}
}

func newReconciliationState(store *Store, scope LifecycleScope) *reconciliationState {
	return &reconciliationState{store: store, scope: scope, candidates: map[SemanticID]*candidateValidationState{}, records: map[SemanticID]Record{}, recordSources: map[SemanticID][]SemanticID{}, cases: map[SemanticID]*caseValidationState{}, relationships: map[SemanticID]*relationshipValidationState{}, linkedSources: map[string]struct{}{}, caseEventIDs: map[SemanticID]struct{}{}}
}

func (state *reconciliationState) validate(ctx context.Context, operation LifecycleOperation) error {
	metadata := operation.lifecycleMetadata()
	for _, sourceID := range metadata.EvidenceSourceIDs {
		if err := sourceID.Validate(); err != nil {
			return err
		}
		inScope, err := state.sourceInScope(ctx, sourceID)
		if err != nil {
			return err
		}
		if !inScope {
			return fmt.Errorf("operation %s cites source %s outside its scope", metadata.OperationID, sourceID)
		}
	}
	requireEvidence := func() error {
		if len(metadata.EvidenceSourceIDs) == 0 {
			return fmt.Errorf("%s requires source evidence", operation.lifecycleKind())
		}
		return nil
	}
	switch op := operation.(type) {
	case AcceptCandidateOperation:
		if err := requireEvidence(); err != nil {
			return err
		}
		candidate, err := state.candidate(ctx, op.CandidateID)
		if err != nil {
			return err
		}
		if err := requirePendingCandidate(candidate); err != nil {
			return err
		}
		if _, err := state.record(ctx, op.Accepted.Record.ID); err == nil {
			return errors.New("accepted record id already exists")
		} else if !errors.Is(err, errObjectNotFound) {
			return err
		}
		if err := validateAcceptedRecord(op.Accepted, candidate, metadata); err != nil {
			return err
		}
		state.records[op.Accepted.Record.ID] = op.Accepted.Record
		state.recordSources[op.Accepted.Record.ID] = append([]SemanticID(nil), op.Accepted.SourceReferenceIDs...)
		candidate.effectiveState, candidate.winnerID = "accepted", metadata.OperationID
	case ConsolidateCandidateOperation:
		if err := requireEvidence(); err != nil {
			return err
		}
		if err := validateRequired(op.Rationale, "consolidation rationale"); err != nil {
			return err
		}
		candidate, err := state.candidate(ctx, op.CandidateID)
		if err != nil {
			return err
		}
		if err := requirePendingCandidate(candidate); err != nil {
			return err
		}
		record, err := state.record(ctx, op.RecordID)
		if err != nil {
			return errors.New("consolidation target is outside the operation scope")
		}
		if record.Domain != candidate.candidate.Domain || record.Visibility != candidate.candidate.Visibility {
			return errors.New("consolidation crosses domain or visibility")
		}
		if !idsContainAll(metadata.EvidenceSourceIDs, candidate.sourceIDs) {
			return errors.New("consolidation omits candidate source evidence")
		}
		recordSources, err := state.sourcesForRecord(ctx, record.ID)
		if err != nil {
			return err
		}
		combinedSources := uniqueIDs(recordSources, candidate.sourceIDs)
		if len(combinedSources) > MaximumSourcesPerObject {
			return fmt.Errorf("consolidation would exceed the record's %d-source lifecycle bound", MaximumSourcesPerObject)
		}
		state.recordSources[record.ID] = combinedSources
		candidate.effectiveState, candidate.winnerID = "consolidated", metadata.OperationID
	case RejectCandidateOperation:
		if err := validateRequired(op.Reason, "rejection reason"); err != nil {
			return err
		}
		candidate, err := state.candidate(ctx, op.CandidateID)
		if err != nil {
			return err
		}
		if err := requirePendingCandidate(candidate); err != nil {
			return err
		}
		candidate.effectiveState, candidate.winnerID = "rejected", metadata.OperationID
	case DeferCandidateOperation:
		for _, check := range []error{validateRequired(op.ReasonCode, "deferral reason code"), validateRequired(op.Reason, "deferral reason")} {
			if check != nil {
				return check
			}
		}
		candidate, err := state.candidate(ctx, op.CandidateID)
		if err != nil {
			return err
		}
		if err := requirePendingCandidate(candidate); err != nil {
			return err
		}
		if candidate.deferred {
			return errors.New("candidate has multiple deferral operations in one batch")
		}
		candidate.deferred = true
	case LinkCandidateEvidenceOperation:
		if err := validateRequired(op.EvidenceContext, "candidate evidence context"); err != nil {
			return err
		}
		candidate, err := state.candidate(ctx, op.CandidateID)
		if err != nil {
			return err
		}
		if err := requirePendingCandidate(candidate); err != nil {
			return err
		}
		if !slices.Contains(metadata.EvidenceSourceIDs, op.SourceReferenceID) {
			return errors.New("linked candidate evidence must be cited by the operation")
		}
		key := string(op.CandidateID) + ":" + string(op.SourceReferenceID)
		if _, exists := state.linkedSources[key]; exists || slices.Contains(candidate.sourceIDs, op.SourceReferenceID) {
			return errors.New("candidate evidence source is already linked")
		}
		var exists bool
		if err := state.store.q.queryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provenance.candidate_evidence_links WHERE candidate_id=$1 AND source_reference_id=$2)`, string(op.CandidateID), string(op.SourceReferenceID)).Scan(&exists); err != nil {
			return fmt.Errorf("check candidate evidence link: %w", err)
		}
		if exists {
			return errors.New("candidate evidence source is already linked")
		}
		if len(candidate.sourceIDs) >= MaximumSourcesPerObject {
			return fmt.Errorf("candidate evidence would exceed the %d-source lifecycle bound", MaximumSourcesPerObject)
		}
		state.linkedSources[key] = struct{}{}
		candidate.sourceIDs = append(candidate.sourceIDs, op.SourceReferenceID)
	case AppendQualificationOperation:
		if err := requireEvidence(); err != nil {
			return err
		}
		if err := validateRequired(op.Qualification, "qualification"); err != nil {
			return err
		}
		if _, err := state.record(ctx, op.RecordID); err != nil {
			return err
		}
	case AppendTemporalInterpretationOperation:
		if err := requireEvidence(); err != nil {
			return err
		}
		if err := validateRequired(op.Temporal.Interpretation, "temporal interpretation"); err != nil {
			return err
		}
		if op.Temporal.ValidFrom != nil && op.Temporal.ValidUntil != nil && op.Temporal.ValidUntil.Before(*op.Temporal.ValidFrom) {
			return errors.New("temporal valid_until cannot precede valid_from")
		}
		if _, err := state.record(ctx, op.RecordID); err != nil {
			return err
		}
	case AppendAssertionPostureOperation:
		if err := requireEvidence(); err != nil {
			return err
		}
		for _, check := range []error{validateRequired(op.AssertionPosture, "assertion posture"), validateRequired(op.Rationale, "assertion posture rationale")} {
			if check != nil {
				return check
			}
		}
		if _, err := state.record(ctx, op.RecordID); err != nil {
			return err
		}
	case CreateResolutionCaseOperation:
		if err := requireEvidence(); err != nil {
			return err
		}
		if op.Case.CreatedByOperationID != nil {
			return errors.New("new resolution case cannot predeclare operation audit identity")
		}
		if op.Case.Domain != state.scope.Domain || op.Case.Visibility != state.scope.Visibility {
			return errors.New("resolution case crosses domain or visibility")
		}
		if len(op.Members) == 0 || len(op.Members) > MaximumSourcesPerObject {
			return errors.New("resolution case requires a bounded non-empty member set")
		}
		if len(op.Events) == 0 || len(op.Events) > MaximumSourcesPerObject {
			return errors.New("resolution case requires a bounded non-empty initial event set")
		}
		if _, err := state.resolutionCase(ctx, op.Case.ID); err == nil {
			return errors.New("resolution case id already exists")
		} else if !errors.Is(err, errObjectNotFound) {
			return err
		}
		if err := validateResolutionCaseForService(op.Case); err != nil {
			return err
		}
		closed := isClosedStatus(op.Case.InitialStatus)
		if closed {
			return errors.New("resolution case cannot append members or events after a terminal initial status")
		}
		caseState := &caseValidationState{item: op.Case, closed: closed, members: map[caseMemberKey]struct{}{}}
		for _, member := range op.Members {
			if err := state.addCaseMember(ctx, caseState, member); err != nil {
				return err
			}
		}
		for _, event := range op.Events {
			if closed {
				return errors.New("resolution case event follows a terminal outcome")
			}
			if err := validateCaseEventForOperation(event, op.Case.ID, metadata); err != nil {
				return err
			}
			if err := state.validateCaseEventID(ctx, event.ID); err != nil {
				return err
			}
			if isClosedOutcome(event.Outcome) {
				closed = true
			}
		}
		caseState.closed = closed
		state.cases[op.Case.ID] = caseState
	case UpdateResolutionCaseOperation:
		if err := requireEvidence(); err != nil {
			return err
		}
		resolutionCase, err := state.resolutionCase(ctx, op.CaseID)
		if err != nil {
			return err
		}
		if resolutionCase.closed {
			return errors.New("resolution case is closed")
		}
		if err := validateCaseEventForOperation(op.Event, op.CaseID, metadata); err != nil {
			return err
		}
		if err := state.validateCaseEventID(ctx, op.Event.ID); err != nil {
			return err
		}
		if isClosedOutcome(op.Event.Outcome) {
			resolutionCase.closed = true
		}
	case CloseResolutionCaseOperation:
		if err := requireEvidence(); err != nil {
			return err
		}
		if err := validateRequired(op.Outcome, "resolution case outcome"); err != nil {
			return err
		}
		if !isClosedStatus(op.Outcome) {
			return errors.New("close operation requires a terminal outcome")
		}
		resolutionCase, err := state.resolutionCase(ctx, op.CaseID)
		if err != nil {
			return err
		}
		if resolutionCase.closed {
			return errors.New("resolution case is closed")
		}
		if err := validateCaseEventForOperation(op.Event, op.CaseID, metadata); err != nil {
			return err
		}
		if err := state.validateCaseEventID(ctx, op.Event.ID); err != nil {
			return err
		}
		if op.Event.Outcome == nil || *op.Event.Outcome != op.Outcome {
			return errors.New("close operation outcome differs from its case event")
		}
		kind := strings.ToLower(strings.TrimSpace(op.Event.EventType))
		if kind != "close" && kind != "closed" {
			return errors.New("close operation requires a closing case event")
		}
		resolutionCase.closed = true
	case AttachCaseMemberOperation:
		resolutionCase, err := state.resolutionCase(ctx, op.CaseID)
		if err != nil {
			return err
		}
		if resolutionCase.closed {
			return errors.New("cannot attach a member to a closed resolution case")
		}
		if err := state.addCaseMember(ctx, resolutionCase, op.Member); err != nil {
			return err
		}
	case AddRelationshipOperation:
		if err := requireEvidence(); err != nil {
			return err
		}
		relationship := op.Relationship
		copy := relationship
		copy.Payload = json.RawMessage(`{}`)
		if err := validateRelationship(copy); err != nil {
			return err
		}
		if relationship.SchemaVersion != SchemaVersion {
			return errors.New("relationship requires schema_version 1.0")
		}
		if len(relationship.EvidenceSourceIDs) > MaximumSourcesPerObject || hasDuplicateIDs(relationship.EvidenceSourceIDs) {
			return errors.New("relationship evidence must be bounded and unique")
		}
		if relationship.CreatedByOperationID != nil {
			return errors.New("new relationship cannot predeclare operation audit identity")
		}
		if !sameProducer(relationship.CreatedBy, metadata.Producer) {
			return errors.New("relationship creator differs from operation producer")
		}
		if !idsContainAll(metadata.EvidenceSourceIDs, relationship.EvidenceSourceIDs) {
			return errors.New("relationship cites evidence absent from its operation")
		}
		if _, err := state.relationship(ctx, relationship.ID); err == nil {
			return errors.New("relationship id already exists")
		} else if !errors.Is(err, errObjectNotFound) {
			return err
		}
		fromRecord, err := state.record(ctx, relationship.FromRecordID)
		if err != nil {
			return errors.New("relationship endpoint is outside the operation scope")
		}
		toRecord, err := state.record(ctx, relationship.ToRecordID)
		if err != nil {
			return errors.New("relationship endpoint is outside the operation scope")
		}
		if fromRecord.ID == toRecord.ID {
			return errors.New("relationship endpoints must differ")
		}
		var relationshipCase *caseValidationState
		if relationship.ResolutionCaseID != nil {
			relationshipCase, err = state.resolutionCase(ctx, *relationship.ResolutionCaseID)
			if err != nil {
				return errors.New("relationship resolution case is outside the operation scope")
			}
		}
		if strings.Contains(strings.ToLower(relationship.RelationshipType), "partial") {
			coverage := strings.TrimSpace(string(relationship.Coverage))
			if relationshipCase == nil || coverage == "" || coverage == "null" {
				return errors.New("partial relationship requires coverage and a resolution case")
			}
			if !relationshipCase.hasRecord(relationship.FromRecordID) || !relationshipCase.hasRecord(relationship.ToRecordID) {
				return errors.New("partial relationship requires both record endpoints as resolution case members")
			}
		}
		state.relationships[relationship.ID] = &relationshipValidationState{item: relationship}
	case EndRelationshipOperation:
		if err := requireEvidence(); err != nil {
			return err
		}
		if err := validateRequired(op.Reason, "relationship ending reason"); err != nil {
			return err
		}
		relationship, err := state.relationship(ctx, op.RelationshipID)
		if err != nil {
			return err
		}
		if relationship.ended {
			return errors.New("relationship is already ended")
		}
		relationship.ended = true
	default:
		return fmt.Errorf("unsupported lifecycle operation %T", operation)
	}
	return nil
}

var errObjectNotFound = errors.New("semantic object not found")

func (state *reconciliationState) candidate(ctx context.Context, id SemanticID) (*candidateValidationState, error) {
	if item, ok := state.candidates[id]; ok {
		return item, nil
	}
	candidate, found, err := state.store.GetCandidate(ctx, id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errObjectNotFound
	}
	if candidate.Domain != state.scope.Domain || candidate.Visibility != state.scope.Visibility {
		return nil, errors.New("candidate is outside the operation scope")
	}
	var registration CandidateRegistration
	if err := decodeLosslessJSON(candidate.Submitted, &registration); err != nil {
		return nil, fmt.Errorf("decode candidate registration: %w", err)
	}
	rows, err := state.store.q.query(ctx, `
		SELECT source_reference_id::text FROM (
			SELECT id AS source_reference_id, resolution_at AS linked_at FROM provenance.source_references WHERE candidate_id=$1
			UNION ALL
			SELECT source_reference_id, linked_at FROM provenance.candidate_evidence_links WHERE candidate_id=$1
		) AS sources ORDER BY linked_at, source_reference_id LIMIT $2
	`, string(id), MaximumSourcesPerObject+1)
	if err != nil {
		return nil, fmt.Errorf("load candidate sources: %w", err)
	}
	sourceIDs, err := scanSemanticIDs(rows, "candidate lifecycle sources")
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(sourceIDs) > MaximumSourcesPerObject {
		return nil, errors.New("candidate source set exceeds lifecycle bound")
	}
	item := &candidateValidationState{candidate: candidate, registration: registration, sourceIDs: sourceIDs, effectiveState: "pending"}
	var eventType, winnerID string
	err = state.store.q.queryRow(ctx, `SELECT event_type, operation_id::text FROM provenance.candidate_events WHERE candidate_id=$1 AND event_type IN ('accepted','consolidated','rejected') ORDER BY id LIMIT 1`, string(id)).Scan(&eventType, &winnerID)
	if err == nil {
		item.effectiveState, item.winnerID = eventType, SemanticID(winnerID)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("load candidate terminal state: %w", err)
	}
	state.candidates[id] = item
	return item, nil
}

func requirePendingCandidate(candidate *candidateValidationState) error {
	if candidate.effectiveState == "pending" {
		return nil
	}
	return &CandidateTerminalConflictError{CandidateID: candidate.candidate.ID, EffectiveState: candidate.effectiveState, WinningOperationID: candidate.winnerID}
}

func (state *reconciliationState) record(ctx context.Context, id SemanticID) (Record, error) {
	if item, ok := state.records[id]; ok {
		return item, nil
	}
	record, found, err := state.store.GetRecord(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if !found {
		return Record{}, errObjectNotFound
	}
	if record.Domain != state.scope.Domain || record.Visibility != state.scope.Visibility {
		return Record{}, errors.New("record is outside the operation scope")
	}
	state.records[id] = record
	return record, nil
}

func (state *reconciliationState) sourcesForRecord(ctx context.Context, id SemanticID) ([]SemanticID, error) {
	if sourceIDs, ok := state.recordSources[id]; ok {
		return sourceIDs, nil
	}
	rows, err := state.store.q.query(ctx, `
		SELECT source_reference_id::text
		FROM provenance.record_sources
		WHERE record_id=$1
		ORDER BY source_reference_id
		LIMIT $2
	`, string(id), MaximumSourcesPerObject+1)
	if err != nil {
		return nil, fmt.Errorf("load record sources: %w", err)
	}
	sourceIDs, err := scanSemanticIDs(rows, "record sources")
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(sourceIDs) > MaximumSourcesPerObject {
		return nil, errors.New("record source set exceeds lifecycle bound")
	}
	state.recordSources[id] = sourceIDs
	return sourceIDs, nil
}

func (state *reconciliationState) resolutionCase(ctx context.Context, id SemanticID) (*caseValidationState, error) {
	if item, ok := state.cases[id]; ok {
		return item, nil
	}
	resolutionCase, found, err := state.store.GetResolutionCase(ctx, id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errObjectNotFound
	}
	if resolutionCase.Domain != state.scope.Domain || resolutionCase.Visibility != state.scope.Visibility {
		return nil, errors.New("resolution case is outside the operation scope")
	}
	rows, err := state.store.q.query(ctx, `
		SELECT member_type,candidate_id::text,record_id::text,role
		FROM provenance.case_members
		WHERE case_id=$1
		ORDER BY attached_at,id
		LIMIT $2
	`, string(id), MaximumSourcesPerObject+1)
	if err != nil {
		return nil, fmt.Errorf("load resolution case members: %w", err)
	}
	members := map[caseMemberKey]struct{}{}
	for rows.Next() {
		var memberType, role string
		var candidateID, recordID *string
		if err := rows.Scan(&memberType, &candidateID, &recordID, &role); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan resolution case member: %w", err)
		}
		memberID := optionalID(candidateID)
		if memberID == nil {
			memberID = optionalID(recordID)
		}
		if memberID == nil {
			rows.Close()
			return nil, errors.New("resolution case contains a member without an object id")
		}
		members[caseMemberKey{memberType: memberType, memberID: *memberID, role: role}] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate resolution case members: %w", err)
	}
	rows.Close()
	if len(members) > MaximumSourcesPerObject {
		return nil, errors.New("resolution case member set exceeds lifecycle bound")
	}
	var durableTerminal bool
	err = state.store.q.queryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM provenance.case_events
			WHERE case_id=$1 AND lower(btrim(outcome)) IN ('closed','resolved','dismissed')
		)
	`, string(id)).Scan(&durableTerminal)
	if err != nil {
		return nil, fmt.Errorf("load resolution case state: %w", err)
	}
	closed := durableTerminal || isClosedStatus(resolutionCase.InitialStatus)
	item := &caseValidationState{item: resolutionCase, closed: closed, members: members}
	state.cases[id] = item
	return item, nil
}

func (state *reconciliationState) relationship(ctx context.Context, id SemanticID) (*relationshipValidationState, error) {
	if item, ok := state.relationships[id]; ok {
		return item, nil
	}
	relationship, found, err := state.store.GetRelationship(ctx, id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errObjectNotFound
	}
	fromRecord, err := state.record(ctx, relationship.FromRecordID)
	if err != nil {
		return nil, errors.New("relationship is outside the operation scope")
	}
	toRecord, err := state.record(ctx, relationship.ToRecordID)
	if err != nil {
		return nil, errors.New("relationship is outside the operation scope")
	}
	_ = fromRecord
	_ = toRecord
	var ended bool
	if err := state.store.q.queryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provenance.relationship_events WHERE relationship_id=$1 AND event_type='ended')`, string(id)).Scan(&ended); err != nil {
		return nil, fmt.Errorf("load relationship state: %w", err)
	}
	item := &relationshipValidationState{item: relationship, ended: ended}
	state.relationships[id] = item
	return item, nil
}

func (state *reconciliationState) sourceInScope(ctx context.Context, id SemanticID) (bool, error) {
	var exists bool
	err := state.store.q.queryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM provenance.source_references source
			LEFT JOIN provenance.candidates candidate ON candidate.id=source.candidate_id
			LEFT JOIN provenance.evidence_registrations evidence ON evidence.source_reference_id=source.id
			WHERE source.id=$1 AND (
				(candidate.domain=$2 AND candidate.visibility=$3) OR
				(evidence.domain=$2 AND evidence.visibility=$3) OR
				EXISTS(SELECT 1 FROM provenance.record_sources link JOIN provenance.records record ON record.id=link.record_id WHERE link.source_reference_id=source.id AND record.domain=$2 AND record.visibility=$3)
			)
		)
	`, string(id), state.scope.Domain, state.scope.Visibility).Scan(&exists)
	return exists, err
}

func (state *reconciliationState) addCaseMember(ctx context.Context, resolutionCase *caseValidationState, member CaseMemberReference) error {
	if err := validateRequired(member.Role, "case member role"); err != nil {
		return err
	}
	if err := member.MemberID.Validate(); err != nil {
		return err
	}
	switch member.MemberType {
	case "candidate":
		if _, err := state.candidate(ctx, member.MemberID); err != nil {
			return err
		}
	case "record":
		if _, err := state.record(ctx, member.MemberID); err != nil {
			return err
		}
	default:
		return errors.New("case member type must be candidate or record")
	}
	key := caseMemberKey{memberType: member.MemberType, memberID: member.MemberID, role: member.Role}
	if _, duplicate := resolutionCase.members[key]; duplicate {
		return errors.New("resolution case member is already attached")
	}
	if len(resolutionCase.members) >= MaximumSourcesPerObject {
		return fmt.Errorf("resolution case would exceed the %d-member lifecycle bound", MaximumSourcesPerObject)
	}
	resolutionCase.members[key] = struct{}{}
	return nil
}

func (state *caseValidationState) hasRecord(id SemanticID) bool {
	for member := range state.members {
		if member.memberType == "record" && member.memberID == id {
			return true
		}
	}
	return false
}

func (state *reconciliationState) validateCaseEventID(ctx context.Context, id SemanticID) error {
	if _, duplicate := state.caseEventIDs[id]; duplicate {
		return errors.New("resolution case event ids must be unique")
	}
	var exists bool
	if err := state.store.q.queryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provenance.case_events WHERE id=$1)`, string(id)).Scan(&exists); err != nil {
		return fmt.Errorf("check resolution case event id: %w", err)
	}
	if exists {
		return errors.New("resolution case event id already exists")
	}
	state.caseEventIDs[id] = struct{}{}
	return nil
}

func validateAcceptedRecord(accepted AcceptedRecord, candidate *candidateValidationState, metadata OperationMetadata) error {
	record := accepted.Record
	if record.Domain != candidate.candidate.Domain || record.Visibility != candidate.candidate.Visibility {
		return errors.New("accepted record crosses domain or visibility")
	}
	if len(accepted.SourceReferenceIDs) == 0 || len(accepted.SourceReferenceIDs) > MaximumSourcesPerObject || hasDuplicateIDs(accepted.SourceReferenceIDs) {
		return errors.New("accepted record requires a bounded unique source set")
	}
	if len(record.Ambiguity) > MaximumSourcesPerObject || (record.Anchors != nil &&
		(len(record.Anchors.Entities) > MaximumSourcesPerObject || len(record.Anchors.Topics) > MaximumSourcesPerObject || len(record.Anchors.Projects) > MaximumSourcesPerObject)) {
		return errors.New("accepted record repeated fields exceed their bounded collection limit")
	}
	if !idsContainAll(accepted.SourceReferenceIDs, candidate.sourceIDs) {
		return errors.New("accepted record omits candidate source evidence")
	}
	if !idsContainAll(metadata.EvidenceSourceIDs, accepted.SourceReferenceIDs) {
		return errors.New("accept operation omits record source evidence")
	}
	if len(accepted.RepresentationEvents) != 0 {
		return errors.New("accepted record cannot predeclare append-only representation history")
	}
	candidateProducer := candidate.registration.Producer
	if candidateProducer.ProducerID == metadata.Producer.ProducerID && !sameProducer(candidateProducer, metadata.Producer) {
		return errors.New("accepted record producer history reuses one producer id with conflicting grounded identities")
	}
	expected := []ProducerIdentity{candidateProducer}
	if !sameProducer(candidateProducer, metadata.Producer) {
		expected = append(expected, metadata.Producer)
	}
	if len(expected) != len(accepted.ProducerHistory) {
		return errors.New("accepted record producer history must exactly preserve candidate then reviewer")
	}
	for index := range expected {
		if !sameProducer(expected[index], accepted.ProducerHistory[index]) {
			return errors.New("accepted record producer history must exactly preserve candidate then reviewer")
		}
	}
	allowed := map[SemanticID]struct{}{}
	for _, id := range accepted.SourceReferenceIDs {
		allowed[id] = struct{}{}
	}
	for role, actor := range map[string]*ActorReference{"source_actor": accepted.SourceActor, "artifact_author": accepted.ArtifactAuthor, "approving_actor": accepted.ApprovingActor} {
		if err := validateActorReference(actor, allowed, role); err != nil {
			return err
		}
	}
	record.Payload = json.RawMessage(`{}`)
	return validateRecord(record)
}

func validateResolutionCaseForService(item ResolutionCase) error {
	if item.SchemaVersion != SchemaVersion {
		return errors.New("resolution case requires schema_version 1.0")
	}
	copy := item
	copy.Payload = json.RawMessage(`{}`)
	return validateResolutionCase(copy)
}

func validateCaseEventForOperation(event CaseEvent, caseID SemanticID, metadata OperationMetadata) error {
	if event.SchemaVersion != SchemaVersion {
		return errors.New("case event requires schema_version 1.0")
	}
	if event.CaseID != caseID {
		return errors.New("case event case id differs from its operation")
	}
	if event.OperationID != nil {
		return errors.New("case event cannot predeclare operation audit identity")
	}
	if !sameProducer(event.Producer, metadata.Producer) {
		return errors.New("case event producer differs from operation producer")
	}
	if !idsContainAll(metadata.EvidenceSourceIDs, event.EvidenceSourceIDs) {
		return errors.New("case event cites evidence absent from its operation")
	}
	if len(event.EvidenceSourceIDs) > MaximumSourcesPerObject || hasDuplicateIDs(event.EvidenceSourceIDs) {
		return errors.New("case event evidence must be bounded and unique")
	}
	copy := event
	copy.OperationID = nil
	copy.Payload = json.RawMessage(`{}`)
	return validateCaseEvent(copy)
}

func applyLifecycleOperation(ctx context.Context, store *Store, operation LifecycleOperation) error {
	metadata := operation.lifecycleMetadata()
	review := map[string]any{"producer": metadata.Producer, "semantic_identity_only": true}
	switch op := operation.(type) {
	case AcceptCandidateOperation:
		accepted := op.Accepted
		record := accepted.Record
		if accepted.SourceActor != nil {
			record.SourceActorID = optionalActorID(accepted.SourceActor)
		}
		if accepted.ArtifactAuthor != nil {
			record.ArtifactAuthorID = optionalActorID(accepted.ArtifactAuthor)
		}
		if accepted.ApprovingActor != nil {
			record.ApprovingActorID = optionalActorID(accepted.ApprovingActor)
		}
		payload, err := json.Marshal(accepted)
		if err != nil {
			return err
		}
		record.Payload = payload
		if err := store.AppendRecord(ctx, record); err != nil {
			return err
		}
		for _, sourceID := range accepted.SourceReferenceIDs {
			if err := store.LinkRecordSource(ctx, RecordSource{RecordID: record.ID, SourceReferenceID: sourceID, LinkedAt: metadata.OccurredAt.UTC()}); err != nil {
				return err
			}
		}
		for _, producer := range accepted.ProducerHistory {
			if err := store.LinkRecordProducer(ctx, RecordProducer{RecordID: record.ID, CandidateID: &op.CandidateID, Producer: producer, LinkedAt: metadata.OccurredAt.UTC()}); err != nil {
				return err
			}
		}
		payload, _ = json.Marshal(map[string]any{"record_id": record.ID, "review": review})
		return store.AppendCandidateEvent(ctx, LedgerEvent{ObjectID: op.CandidateID, EventType: "accepted", OperationID: metadata.OperationID, OccurredAt: metadata.OccurredAt.UTC(), Payload: payload})
	case ConsolidateCandidateOperation:
		rows, err := store.q.query(ctx, `
			SELECT candidate_sources.source_reference_id::text
			FROM (
				SELECT id AS source_reference_id FROM provenance.source_references WHERE candidate_id=$1
				UNION
				SELECT source_reference_id FROM provenance.candidate_evidence_links WHERE candidate_id=$1
			) AS candidate_sources
			WHERE NOT EXISTS (
				SELECT 1 FROM provenance.record_sources existing
				WHERE existing.record_id=$2 AND existing.source_reference_id=candidate_sources.source_reference_id
			)
			ORDER BY candidate_sources.source_reference_id
		`, string(op.CandidateID), string(op.RecordID))
		if err != nil {
			return err
		}
		sources, err := scanSemanticIDs(rows, "candidate consolidation sources")
		rows.Close()
		if err != nil {
			return err
		}
		for _, sourceID := range sources {
			if err := store.LinkRecordSource(ctx, RecordSource{RecordID: op.RecordID, SourceReferenceID: sourceID, LinkedAt: metadata.OccurredAt.UTC()}); err != nil {
				return err
			}
		}
		candidate, _, err := store.GetCandidate(ctx, op.CandidateID)
		if err != nil {
			return err
		}
		var registration CandidateRegistration
		if err := decodeLosslessJSON(candidate.Submitted, &registration); err != nil {
			return err
		}
		if err := store.LinkRecordProducer(ctx, RecordProducer{RecordID: op.RecordID, CandidateID: &op.CandidateID, Producer: registration.Producer, LinkedAt: metadata.OccurredAt.UTC()}); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"record_id": op.RecordID, "rationale": op.Rationale, "review": review})
		return store.AppendCandidateEvent(ctx, LedgerEvent{ObjectID: op.CandidateID, EventType: "consolidated", OperationID: metadata.OperationID, OccurredAt: metadata.OccurredAt.UTC(), Payload: payload})
	case RejectCandidateOperation:
		payload, _ := json.Marshal(map[string]any{"reason": op.Reason, "review": review})
		return store.AppendCandidateEvent(ctx, LedgerEvent{ObjectID: op.CandidateID, EventType: "rejected", OperationID: metadata.OperationID, OccurredAt: metadata.OccurredAt.UTC(), Payload: payload})
	case DeferCandidateOperation:
		payload, _ := json.Marshal(map[string]any{"reason_code": op.ReasonCode, "reason": op.Reason, "next_review_at": op.NextReviewAt, "needs_capability": op.NeedsCapability, "review": review})
		return store.AppendCandidateEvent(ctx, LedgerEvent{ObjectID: op.CandidateID, EventType: "deferred", OperationID: metadata.OperationID, OccurredAt: metadata.OccurredAt.UTC(), Payload: payload})
	case LinkCandidateEvidenceOperation:
		payload, _ := json.Marshal(map[string]any{"evidence_context": op.EvidenceContext})
		return store.AppendCandidateEvidenceLink(ctx, CandidateEvidenceLink{ID: metadata.OperationID, CandidateID: op.CandidateID, SourceReferenceID: op.SourceReferenceID, OperationID: metadata.OperationID, LinkedAt: metadata.OccurredAt.UTC(), Producer: metadata.Producer, Payload: payload})
	case AppendQualificationOperation:
		eventType := "qualification_appended"
		if op.MarksAmbiguity {
			eventType = "ambiguity_appended"
		}
		payload, _ := json.Marshal(map[string]any{"qualification": op.Qualification, "marks_ambiguity": op.MarksAmbiguity})
		return store.AppendRecordEvent(ctx, LedgerEvent{ObjectID: op.RecordID, EventType: eventType, OperationID: metadata.OperationID, OccurredAt: metadata.OccurredAt.UTC(), Payload: payload})
	case AppendTemporalInterpretationOperation:
		payload, _ := json.Marshal(map[string]any{"temporal_interpretation": op.Temporal})
		return store.AppendRecordEvent(ctx, LedgerEvent{ObjectID: op.RecordID, EventType: "temporal_interpretation_appended", OperationID: metadata.OperationID, OccurredAt: metadata.OccurredAt.UTC(), Payload: payload})
	case AppendAssertionPostureOperation:
		payload, _ := json.Marshal(map[string]any{"assertion_posture": op.AssertionPosture, "rationale": op.Rationale})
		return store.AppendRecordEvent(ctx, LedgerEvent{ObjectID: op.RecordID, EventType: "assertion_posture_appended", OperationID: metadata.OperationID, OccurredAt: metadata.OccurredAt.UTC(), Payload: payload})
	case CreateResolutionCaseOperation:
		item := op.Case
		item.CreatedByOperationID = &metadata.OperationID
		payload, err := json.Marshal(struct {
			Case    ResolutionCase        `json:"case"`
			Members []CaseMemberReference `json:"members"`
			Events  []CaseEvent           `json:"events"`
		}{op.Case, op.Members, op.Events})
		if err != nil {
			return err
		}
		item.Payload = payload
		if err := store.AppendResolutionCase(ctx, item); err != nil {
			return err
		}
		for _, member := range op.Members {
			if err := appendCaseMemberReference(ctx, store, item.ID, member, metadata); err != nil {
				return err
			}
		}
		for _, event := range op.Events {
			event.OperationID = &metadata.OperationID
			if err := appendCaseEventInOrder(ctx, store, event); err != nil {
				return err
			}
		}
		return nil
	case UpdateResolutionCaseOperation:
		event := op.Event
		event.OperationID = &metadata.OperationID
		return appendCaseEventInOrder(ctx, store, event)
	case CloseResolutionCaseOperation:
		event := op.Event
		event.OperationID = &metadata.OperationID
		return appendCaseEventInOrder(ctx, store, event)
	case AttachCaseMemberOperation:
		return appendCaseMemberReference(ctx, store, op.CaseID, op.Member, metadata)
	case AddRelationshipOperation:
		item := op.Relationship
		item.CreatedByOperationID = &metadata.OperationID
		item.Payload = mustJSON(op.Relationship)
		return store.AppendRelationship(ctx, item)
	case EndRelationshipOperation:
		payload, _ := json.Marshal(map[string]any{"reason": op.Reason})
		return store.AppendRelationshipEvent(ctx, LedgerEvent{ObjectID: op.RelationshipID, EventType: "ended", OperationID: metadata.OperationID, OccurredAt: metadata.OccurredAt.UTC(), Payload: payload})
	default:
		return fmt.Errorf("unsupported lifecycle operation %T", operation)
	}
}

func appendCaseMemberReference(ctx context.Context, store *Store, caseID SemanticID, reference CaseMemberReference, metadata OperationMetadata) error {
	member := CaseMember{CaseID: caseID, MemberType: reference.MemberType, Role: reference.Role, AttachedAt: metadata.OccurredAt.UTC(), OperationID: &metadata.OperationID}
	if reference.MemberType == "candidate" {
		member.CandidateID = &reference.MemberID
	} else {
		member.RecordID = &reference.MemberID
	}
	return store.AppendCaseMember(ctx, member)
}

func appendCaseEventInOrder(ctx context.Context, store *Store, event CaseEvent) error {
	var appendOrder int64
	if err := store.q.queryRow(ctx, `SELECT count(*) + 1 FROM provenance.case_events WHERE case_id=$1`, string(event.CaseID)).Scan(&appendOrder); err != nil {
		return fmt.Errorf("allocate resolution case event append order: %w", err)
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode resolution case event: %w", err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(payload, &document); err != nil {
		return fmt.Errorf("prepare resolution case event append order: %w", err)
	}
	encodedOrder, _ := json.Marshal(appendOrder)
	document["append_order"] = encodedOrder
	event.Payload, err = json.Marshal(document)
	if err != nil {
		return fmt.Errorf("encode resolution case event append order: %w", err)
	}
	return store.AppendCaseEvent(ctx, event)
}

func receiptForOperation(operation LifecycleOperation) OperationReceipt {
	receipt := OperationReceipt{SchemaVersion: SchemaVersion, OperationID: operation.lifecycleMetadata().OperationID, OperationType: operation.lifecycleKind(), Result: "applied"}
	switch op := operation.(type) {
	case AcceptCandidateOperation:
		receipt.ObjectID, receipt.EffectiveState = op.CandidateID, "accepted"
	case ConsolidateCandidateOperation:
		receipt.ObjectID, receipt.EffectiveState = op.CandidateID, "consolidated"
	case RejectCandidateOperation:
		receipt.ObjectID, receipt.EffectiveState = op.CandidateID, "rejected"
	case DeferCandidateOperation:
		receipt.ObjectID, receipt.EffectiveState = op.CandidateID, "pending"
	case LinkCandidateEvidenceOperation:
		receipt.ObjectID, receipt.EffectiveState = op.CandidateID, "pending"
	case AppendQualificationOperation:
		receipt.ObjectID = op.RecordID
	case AppendTemporalInterpretationOperation:
		receipt.ObjectID = op.RecordID
	case AppendAssertionPostureOperation:
		receipt.ObjectID = op.RecordID
	case CreateResolutionCaseOperation:
		receipt.ObjectID, receipt.EffectiveState = op.Case.ID, op.Case.InitialStatus
	case UpdateResolutionCaseOperation:
		receipt.ObjectID = op.CaseID
		if op.Event.Outcome != nil {
			receipt.EffectiveState = *op.Event.Outcome
		}
	case CloseResolutionCaseOperation:
		receipt.ObjectID, receipt.EffectiveState = op.CaseID, op.Outcome
	case AttachCaseMemberOperation:
		receipt.ObjectID = op.CaseID
	case AddRelationshipOperation:
		receipt.ObjectID, receipt.EffectiveState = op.Relationship.ID, "active"
	case EndRelationshipOperation:
		receipt.ObjectID, receipt.EffectiveState = op.RelationshipID, "ended"
	}
	return receipt
}

func optionalActorID(actor *ActorReference) *string {
	if actor == nil || strings.TrimSpace(actor.ActorID) == "" {
		return nil
	}
	value := strings.TrimSpace(actor.ActorID)
	return &value
}
func hasDuplicateIDs(ids []SemanticID) bool {
	seen := map[SemanticID]struct{}{}
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			return true
		}
		seen[id] = struct{}{}
	}
	return false
}

func uniqueIDs(groups ...[]SemanticID) []SemanticID {
	seen := map[SemanticID]struct{}{}
	result := make([]SemanticID, 0)
	for _, ids := range groups {
		for _, id := range ids {
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			result = append(result, id)
		}
	}
	return result
}

func idsContainAll(haystack, needles []SemanticID) bool {
	values := map[SemanticID]struct{}{}
	for _, id := range haystack {
		values[id] = struct{}{}
	}
	for _, id := range needles {
		if _, ok := values[id]; !ok {
			return false
		}
	}
	return true
}
func mustJSON(value any) json.RawMessage { encoded, _ := json.Marshal(value); return encoded }
func isClosedStatus(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "closed" || value == "resolved" || value == "dismissed"
}
func isClosedOutcome(value *string) bool { return value != nil && isClosedStatus(*value) }
