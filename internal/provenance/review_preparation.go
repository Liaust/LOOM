package provenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type PreparedCandidateReview struct {
	Request            ManualOperationsRequest `json:"request"`
	RecordID           SemanticID              `json:"record_id"`
	RelationshipID     *SemanticID             `json:"relationship_id,omitempty"`
	LifecycleTruncated bool                    `json:"lifecycle_truncated"`
}

// Selected preparation uses the same exact reads as search. The pure helper
// below remains available for intentional internal/global callers.
func (api *FoundationAPI) PrepareCandidateReview(ctx context.Context, candidateID SemanticID, reviewer ProducerIdentity, targetID SemanticID, relationshipContext string, at time.Time) (PreparedCandidateReview, error) {
	candidate, err := api.GetCandidateWithSources(ctx, candidateID, MaximumSourcesPerObject)
	if err != nil {
		return PreparedCandidateReview{}, err
	}
	var target *RecordWithSources
	if targetID != "" {
		exact, err := api.GetRecordWithSources(ctx, targetID, MaximumSourcesPerObject)
		if err != nil {
			return PreparedCandidateReview{}, err
		}
		target = &exact
	}
	return PrepareCandidateReview(candidate, reviewer, target, relationshipContext, at)
}

// Preparation copies observations into a reviewable request. It performs no
// reconciliation, source fetch or policy decision; apply revalidates live state.
func PrepareCandidateReview(candidate CandidateWithSources, reviewer ProducerIdentity, target *RecordWithSources, relationshipContext string, at time.Time) (PreparedCandidateReview, error) {
	var out PreparedCandidateReview
	if candidate.SchemaVersion != SchemaVersion || candidate.EffectiveState != "pending" {
		return out, errors.New("review preparation requires a currently pending candidate")
	}
	if err := completeReviewSources("candidate", candidate.Candidate.ID, candidate.SourceReferenceIDs, candidate.Sources); err != nil {
		return out, err
	}
	if err := validateProducer(reviewer); err != nil || strings.TrimSpace(reviewer.TaskID) == "" {
		return out, errors.New("supply the actual reviewer producer ID/kind and task context")
	}
	var registration CandidateRegistration
	decoder := json.NewDecoder(bytes.NewReader(candidate.Candidate.Submitted))
	decoder.UseNumber()
	if err := decoder.Decode(&registration); err != nil || registration.Domain != candidate.Candidate.Domain || registration.Visibility != candidate.Candidate.Visibility || registration.Claim != candidate.Candidate.Claim {
		return out, errors.New("candidate registration is incomplete or inconsistent with its lifecycle identity")
	}
	recordID, err := newSemanticID()
	if err != nil {
		return out, err
	}
	operationID, err := newSemanticID()
	if err != nil {
		return out, err
	}
	metadata := OperationMetadata{SchemaVersion: SchemaVersion, OperationID: operationID, OccurredAt: at.UTC(), Producer: reviewer, EvidenceSourceIDs: append([]SemanticID(nil), candidate.SourceReferenceIDs...)}
	history := []ProducerIdentity{registration.Producer}
	if !sameProducer(registration.Producer, reviewer) {
		history = append(history, reviewer)
	}
	accepted := AcceptedRecord{Record: Record{ID: recordID, SchemaVersion: SchemaVersion, Claim: registration.Claim,
		RecordKind: registration.RecordKind, RecordContext: registration.RecordContext, Ambiguity: append([]string(nil), registration.Ambiguity...),
		Domain: registration.Domain, Visibility: registration.Visibility, AssertionPosture: registration.AssertionPosture,
		Temporal: registration.Temporal, Anchors: cloneArchivistAnchors(registration.Anchors), CreatedAt: at.UTC(), Payload: json.RawMessage(`{}`)},
		SourceReferenceIDs: append([]SemanticID(nil), candidate.SourceReferenceIDs...), ProducerHistory: history,
		SourceActor: registration.SourceActor, ArtifactAuthor: registration.ArtifactAuthor, ApprovingActor: registration.ApprovingActor}
	state := &candidateValidationState{candidate: candidate.Candidate, registration: registration, sourceIDs: candidate.SourceReferenceIDs}
	if err := validateAcceptedRecord(accepted, state, metadata); err != nil {
		return out, err
	}
	operations := []LifecycleOperation{AcceptCandidateOperation{OperationMetadata: metadata, CandidateID: candidate.Candidate.ID, Accepted: accepted}}
	if target != nil {
		if target.SchemaVersion != SchemaVersion || target.Record.Domain != registration.Domain || target.Record.Visibility != registration.Visibility || strings.TrimSpace(relationshipContext) == "" {
			return out, errors.New("supersession needs an exact same-scope accepted record and an explicit relationship context")
		}
		if err := completeReviewSources("record", target.Record.ID, target.SourceReferenceIDs, target.Sources); err != nil {
			return out, err
		}
		relationID, err := newSemanticID()
		if err != nil {
			return out, err
		}
		relationOperationID, err := newSemanticID()
		if err != nil {
			return out, err
		}
		evidence := append([]SemanticID(nil), candidate.SourceReferenceIDs...)
		for _, id := range target.SourceReferenceIDs {
			if !idsContainAll(evidence, []SemanticID{id}) {
				evidence = append(evidence, id)
			}
		}
		operations = append(operations, AddRelationshipOperation{OperationMetadata: OperationMetadata{SchemaVersion: SchemaVersion,
			OperationID: relationOperationID, OccurredAt: at.UTC(), Producer: reviewer, EvidenceSourceIDs: evidence},
			Relationship: Relationship{ID: relationID, SchemaVersion: SchemaVersion, RelationshipType: "supersedes",
				FromRecordID: recordID, ToRecordID: target.Record.ID, Context: &relationshipContext, EvidenceSourceIDs: evidence,
				CreatedAt: at.UTC(), CreatedBy: reviewer, Payload: json.RawMessage(`{}`)}})
		out.RelationshipID = &relationID
	} else if relationshipContext != "" {
		return out, errors.New("relationship context requires a supersedes target")
	}
	out.Request = ManualOperationsRequest{Scope: LifecycleScope{Domain: registration.Domain, Visibility: registration.Visibility}, Producer: reviewer, Operations: []json.RawMessage{}}
	for _, operation := range operations {
		raw, err := archivistOperationDocument(operation)
		if err != nil {
			return out, err
		}
		out.Request.Operations = append(out.Request.Operations, raw)
	}
	if err := ValidateManualOperationsRequest(out.Request); err != nil {
		return out, err
	}
	out.RecordID, out.LifecycleTruncated = recordID, candidate.Truncated || target != nil && target.Truncated
	return out, nil
}

func completeReviewSources(kind string, id SemanticID, ids []SemanticID, sources LinkedSources) error {
	if id.Validate() != nil || sources.SchemaVersion != LinkedSourcesSchemaVersion || sources.ParentKind != kind || sources.ParentID != id || sources.Posture != "stored_resolution" || sources.SourcesTruncated || sources.IncompleteItems != 0 || len(ids) == 0 || len(ids) > MaximumSourcesPerObject || len(sources.Items) != len(ids) || sources.Returned != len(ids) || hasDuplicateIDs(ids) {
		return fmt.Errorf("%s source evidence is incomplete; obtain its complete exact source receipts", kind)
	}
	seen := map[SemanticID]bool{}
	for _, item := range sources.Items {
		if item.SourceReferenceID.Validate() != nil || item.StoredSourceReceipt == nil || item.ReceiptUnavailable != "" || item.Status != "resolved" || !idsContainAll(ids, []SemanticID{item.SourceReferenceID}) || seen[item.SourceReferenceID] {
			return fmt.Errorf("%s source evidence is unavailable, unresolved or inconsistent", kind)
		}
		seen[item.SourceReferenceID] = true
	}
	return nil
}
