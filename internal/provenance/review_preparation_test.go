package provenance

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func preparationFixture(t *testing.T) CandidateWithSources {
	t.Helper()
	registration := loadLifecycleParityFixture(t).CandidateRegistration
	registration.Producer = ProducerIdentity{ProducerID: "mina", ProducerKind: "working_agent", TaskID: "capture-task"}
	raw, err := json.Marshal(registration)
	if err != nil {
		t.Fatal(err)
	}
	id, source := semanticID(44001), semanticID(44002)
	return CandidateWithSources{CandidateLifecycleProjection: CandidateLifecycleProjection{SchemaVersion: SchemaVersion,
		Candidate:      Candidate{ID: id, Domain: registration.Domain, Visibility: registration.Visibility, Claim: registration.Claim, Submitted: raw},
		EffectiveState: "pending", SourceReferenceIDs: []SemanticID{source}},
		Sources: LinkedSources{SchemaVersion: LinkedSourcesSchemaVersion, ParentKind: "candidate", ParentID: id, Posture: "stored_resolution", Returned: 1,
			Items: []LinkedSourceItem{{SourceReferenceID: source, StoredSourceReceipt: &StoredSourceReceipt{SchemaVersion: SchemaVersion, Status: "resolved", VerificationPosture: "unknown"}}}}}
}

func TestPrepareCandidateReviewPreservesContextsAndEvidence(t *testing.T) {
	candidate := preparationFixture(t)
	reviewer := ProducerIdentity{ProducerID: "mina", ProducerKind: "working_agent", TaskID: "real-review-task"}
	prepared, err := PrepareCandidateReview(candidate, reviewer, nil, "", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	operations, err := decodeLifecycleOperations(prepared.Request.Operations)
	if err != nil || len(operations) != 1 {
		t.Fatal(err)
	}
	accept := operations[0].(AcceptCandidateOperation)
	var original CandidateRegistration
	json.Unmarshal(candidate.Candidate.Submitted, &original)
	if !sameProducer(accept.Accepted.ProducerHistory[0], original.Producer) || !sameProducer(accept.Accepted.ProducerHistory[1], reviewer) || accept.Accepted.Record.Claim != original.Claim || !reflect.DeepEqual(accept.Accepted.Record.Temporal, original.Temporal) || accept.Accepted.SourceReferenceIDs[0] != candidate.SourceReferenceIDs[0] {
		t.Fatalf("preparation altered capture evidence: %+v", accept)
	}
	if err := ValidateManualOperationsRequest(prepared.Request); err != nil {
		t.Fatal(err)
	}
	// A saved request retains its UUIDs and exact replay identity after parsing.
	raw, _ := json.Marshal(prepared.Request)
	var replay ManualOperationsRequest
	json.Unmarshal(raw, &replay)
	if err := ValidateManualOperationsRequest(replay); err != nil {
		t.Fatal(err)
	}
	if string(replay.Operations[0]) != string(prepared.Request.Operations[0]) {
		t.Fatal("saved replay changed")
	}
}

func TestPrepareCandidateReviewSupersessionPreservesBothSources(t *testing.T) {
	candidate := preparationFixture(t)
	id, source := semanticID(44004), semanticID(44005)
	target := RecordWithSources{RecordLifecycleProjection: RecordLifecycleProjection{SchemaVersion: SchemaVersion,
		Record: Record{ID: id, Domain: candidate.Candidate.Domain, Visibility: candidate.Candidate.Visibility}, SourceReferenceIDs: []SemanticID{source}},
		Sources: LinkedSources{SchemaVersion: LinkedSourcesSchemaVersion, ParentKind: "record", ParentID: id, Posture: "stored_resolution", Returned: 1,
			Items: []LinkedSourceItem{{SourceReferenceID: source, StoredSourceReceipt: &StoredSourceReceipt{SchemaVersion: SchemaVersion, Status: "resolved"}}}}}
	prepared, err := PrepareCandidateReview(candidate, ProducerIdentity{ProducerID: "mina", ProducerKind: "working_agent", TaskID: "review"}, &target, "Explicit scoped revision", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	operations, err := decodeLifecycleOperations(prepared.Request.Operations)
	if err != nil || len(operations) != 2 {
		t.Fatal("supersession batch missing relationship")
	}
	relation := operations[1].(AddRelationshipOperation).Relationship
	if relation.FromRecordID != prepared.RecordID || relation.ToRecordID != id || relation.RelationshipType != "supersedes" || len(relation.EvidenceSourceIDs) != 2 || relation.EvidenceSourceIDs[0] != candidate.SourceReferenceIDs[0] || relation.EvidenceSourceIDs[1] != source {
		t.Fatal("direction or evidence altered")
	}
}

func TestPrepareCandidateReviewRefusesBadSourcesAndIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*CandidateWithSources, *ProducerIdentity)
	}{
		{"terminal", func(c *CandidateWithSources, _ *ProducerIdentity) { c.EffectiveState = "accepted" }},
		{"truncated evidence", func(c *CandidateWithSources, _ *ProducerIdentity) { c.Sources.SourcesTruncated = true }},
		{"missing receipt", func(c *CandidateWithSources, _ *ProducerIdentity) { c.Sources.Items = nil }},
		{"unresolved", func(c *CandidateWithSources, _ *ProducerIdentity) { c.Sources.Items[0].Status = "unresolved" }},
		{"wrong parent", func(c *CandidateWithSources, _ *ProducerIdentity) { c.Sources.ParentID = semanticID(44003) }},
		{"task absent", func(_ *CandidateWithSources, p *ProducerIdentity) { p.TaskID = "" }},
		{"kind substitution", func(_ *CandidateWithSources, p *ProducerIdentity) { p.ProducerKind = "different" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := preparationFixture(t)
			reviewer := ProducerIdentity{ProducerID: "mina", ProducerKind: "working_agent", TaskID: "review-task"}
			tc.change(&candidate, &reviewer)
			if _, err := PrepareCandidateReview(candidate, reviewer, nil, "", time.Now().UTC()); err == nil {
				t.Fatal("invalid review prepared")
			}
		})
	}
	candidate := preparationFixture(t)
	target := RecordWithSources{RecordLifecycleProjection: RecordLifecycleProjection{SchemaVersion: SchemaVersion, Record: Record{Domain: "other", Visibility: candidate.Candidate.Visibility}}}
	if _, err := PrepareCandidateReview(candidate, ProducerIdentity{ProducerID: "mina", ProducerKind: "working_agent", TaskID: "review"}, &target, "explicit change", time.Now().UTC()); err == nil || !strings.Contains(err.Error(), "same-scope") {
		t.Fatal("cross-scope supersession prepared")
	}
}
