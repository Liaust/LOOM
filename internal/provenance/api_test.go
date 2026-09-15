package provenance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestFoundationAPIListUsesStableBoundedSummariesPostgres(t *testing.T) {
	_, store, _ := migratedStore(t, "foundation_api")
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	api := &FoundationAPI{store: store, service: service}
	base := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	ids := []SemanticID{
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
	}
	for index, id := range ids {
		claim := strings.Repeat(string(rune('a'+index)), MaximumFoundationExcerptBytes+32)
		candidate := Candidate{
			ID: id, SchemaVersion: SchemaVersion, State: "pending", Domain: "loom-development",
			Visibility: "private", RecordKind: "decision", Claim: claim,
			RecordContext: "bounded list", AssertionPosture: "source_claim",
			ProducerID: "producer-test", RegisteredAt: base.Add(time.Duration(index) * time.Second),
			Submitted: json.RawMessage(`{"semantic":"evidence"}`), Payload: json.RawMessage(`{"semantic":"evidence"}`),
		}
		if err := store.AppendCandidate(context.Background(), candidate); err != nil {
			t.Fatal(err)
		}
	}

	first, err := api.ListCandidates(context.Background(), PageRequest{Limit: 1, Domain: "loom-development", Visibility: "private"})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || !first.Truncated || first.NextTime == nil || first.NextID == nil || *first.NextID != ids[0] {
		t.Fatalf("first page = %#v", first)
	}
	if !first.Items[0].Claim.Truncated || len(first.Items[0].Claim.Text) > MaximumFoundationExcerptBytes {
		t.Fatalf("claim excerpt = %#v", first.Items[0].Claim)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"submitted", "payload", "semantic"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("summary leaked %q: %s", forbidden, encoded)
		}
	}

	second, err := api.ListCandidates(context.Background(), PageRequest{
		Limit: 1, Domain: "loom-development", Visibility: "private",
		AfterTime: first.NextTime, AfterID: first.NextID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].ID != ids[1] {
		t.Fatalf("second page = %#v", second)
	}
}

func TestFoundationAPIExactGetAndValidationErrors(t *testing.T) {
	api := &FoundationAPI{}
	_, err := api.RegisterCandidates(context.Background(), "", CandidateRegistrationRequest{})
	var validation *FoundationValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("registration error = %T %v", err, err)
	}
	if err := ValidateFoundationPageRequest(PageRequest{Limit: MaximumPageLimit + 1}); err == nil {
		t.Fatal("oversized page limit accepted")
	}
	if err := ValidateFoundationPageRequest(PageRequest{AfterTime: new(time.Time)}); err == nil {
		t.Fatal("partial cursor accepted")
	}
}

func TestFoundationAPIAppliesDecodedLifecycleValuesAndReplaysPostgres(t *testing.T) {
	_, store, _ := migratedStore(t, "foundation_api_operations")
	service, err := NewService(store, WithClock(fixedClock), WithIDFactory(sequentialIDFactory(20000)))
	if err != nil {
		t.Fatal(err)
	}
	api := &FoundationAPI{store: store, service: service}
	fixture := loadLifecycleParityFixture(t)
	registered, err := api.RegisterCandidates(context.Background(), "foundation-api-operation", CandidateRegistrationRequest{
		Candidates: []CandidateRegistration{fixture.CandidateRegistration},
	})
	if err != nil {
		t.Fatal(err)
	}
	reviewer := reviewerIdentity()
	operation := RejectCandidateOperation{
		OperationMetadata: operationMetadata(20010, reviewer),
		CandidateID:       registered.Receipts[0].CandidateID,
		Reason:            "The bounded manual review rejects this candidate.",
	}
	document, err := json.Marshal(operation)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(document, &fields); err != nil {
		t.Fatal(err)
	}
	fields["operation_type"] = json.RawMessage(`"reject_candidate"`)
	document, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	request := ManualOperationsRequest{
		Scope:    LifecycleScope{Domain: fixture.CandidateRegistration.Domain, Visibility: fixture.CandidateRegistration.Visibility},
		Producer: reviewer, Operations: []json.RawMessage{document},
	}
	receipt, err := api.ApplyManualOperations(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Replayed || len(receipt.Receipts) != 1 || receipt.Receipts[0].OperationID != operation.OperationID {
		t.Fatalf("receipt = %#v", receipt)
	}
	replayed, err := api.ApplyManualOperations(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || len(replayed.Receipts) != 1 || !replayed.Receipts[0].Replayed {
		t.Fatalf("replayed receipt = %#v", replayed)
	}
	projection, err := api.GetCandidate(context.Background(), operation.CandidateID, 10)
	if err != nil || projection.EffectiveState != "rejected" {
		t.Fatalf("projection = %#v, error = %v", projection, err)
	}
}

func TestDecodeLifecycleOperationsIsExactAndHasNoSearchMode(t *testing.T) {
	producer := `{"producer_id":"reviewer","producer_kind":"working_agent"}`
	operationID := "33333333-3333-4333-8333-333333333333"
	candidateID := "44444444-4444-4444-8444-444444444444"
	documents := []json.RawMessage{json.RawMessage(`{
		"operation_type":"reject_candidate",
		"schema_version":"1.0",
		"operation_id":"` + operationID + `",
		"occurred_at":"2026-08-29T12:00:00Z",
		"producer":` + producer + `,
		"candidate_id":"` + candidateID + `",
		"reason":"not supported by evidence"
	}`)}
	operations, err := decodeLifecycleOperations(documents)
	if err != nil {
		t.Fatal(err)
	}
	reject, ok := operations[0].(RejectCandidateOperation)
	if !ok || reject.CandidateID != SemanticID(candidateID) || reject.Producer.ProducerID != "reviewer" {
		t.Fatalf("decoded operation = %#v", operations[0])
	}

	for _, invalid := range []json.RawMessage{
		json.RawMessage(`{"operation_type":"search","query":"anything"}`),
		json.RawMessage(`{"operation_type":"reject_candidate","unknown":true}`),
	} {
		if _, err := decodeLifecycleOperations([]json.RawMessage{invalid}); err == nil {
			t.Fatalf("invalid operation accepted: %s", invalid)
		}
	}
}

func TestBoundedExcerptPreservesUTF8(t *testing.T) {
	value := strings.Repeat("é", MaximumFoundationExcerptBytes)
	excerpt := boundedExcerpt(value)
	if !excerpt.Truncated || !json.Valid([]byte(`"`+excerpt.Text+`"`)) || len(excerpt.Text) > MaximumFoundationExcerptBytes {
		t.Fatalf("excerpt = %#v", excerpt)
	}
}

func TestCandidateRegistrationTransportReceiptOmitsSourceBodiesAndBoundsPosture(t *testing.T) {
	gapReason := strings.Repeat("é", MaximumFoundationReceiptBytes)
	sourceContext := "private semantic source body"
	projected := ProjectCandidateRegistrationReceipt(CandidateRegistrationBatchReceipt{
		SchemaVersion: SchemaVersion,
		Receipts: []CandidateRegistrationReceipt{{
			SchemaVersion: SchemaVersion,
			CandidateID:   "11111111-1111-4111-8111-111111111111",
			SourceResults: []SourceReference{{
				ID: "22222222-2222-4222-8222-222222222222", SchemaVersion: SchemaVersion,
				SourceKind: strings.Repeat("source", MaximumFoundationReceiptBytes),
				Status:     "unresolved", VerificationPosture: "unverified", GapReason: &gapReason,
				SourceContext: &sourceContext, Submitted: json.RawMessage(`{"private":true}`), Payload: json.RawMessage(`{"private":true}`),
			}},
		}},
	})
	source := projected.Receipts[0].SourceResults[0]
	if !source.SourceKind.Truncated || len(source.SourceKind.Text) > MaximumFoundationReceiptBytes || source.GapReason == nil || !source.GapReason.Truncated || len(source.GapReason.Text) > MaximumFoundationReceiptBytes {
		t.Fatalf("source receipt = %#v", source)
	}
	encoded, err := json.Marshal(projected)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"source_context", "submitted", "payload", "private semantic source body"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("transport receipt leaked %q: %s", forbidden, encoded)
		}
	}

	maximum := CandidateRegistrationBatchReceipt{SchemaVersion: SchemaVersion}
	for candidateIndex := 0; candidateIndex < MaximumRegistrationBatch; candidateIndex++ {
		candidate := CandidateRegistrationReceipt{
			SchemaVersion: SchemaVersion, CandidateID: semanticID(21000 + candidateIndex),
			RegistrationDigest: strings.Repeat("d", 64),
		}
		for sourceIndex := 0; sourceIndex < MaximumSourcesPerObject; sourceIndex++ {
			candidate.SourceResults = append(candidate.SourceResults, SourceReference{
				ID:            semanticID(22000 + candidateIndex*MaximumSourcesPerObject + sourceIndex),
				SchemaVersion: SchemaVersion, SourceKind: strings.Repeat("s", MaximumFoundationReceiptBytes+32),
				Status: "unresolved", VerificationPosture: "unverified", GapReason: &gapReason,
			})
		}
		maximum.Receipts = append(maximum.Receipts, candidate)
	}
	encoded, err = json.Marshal(ProjectCandidateRegistrationReceipt(maximum))
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > MaximumFoundationResponseBytes {
		t.Fatalf("maximum transport receipt = %d bytes", len(encoded))
	}
}

func TestFoundationAPIExactSourcesValidationAndUnavailable(t *testing.T) {
	api := &FoundationAPI{}
	for _, kind := range []string{"candidate", "record"} {
		for _, tc := range []struct {
			id         SemanticID
			limit      int
			validation bool
		}{{"bad", 16, true}, {semanticID(1), -1, true}, {semanticID(1), 101, true}, {semanticID(1), 0, false}, {semanticID(1), 100, false}} {
			var err error
			if kind == "candidate" {
				_, err = api.GetCandidateWithSources(context.Background(), tc.id, tc.limit)
			} else {
				_, err = api.GetRecordWithSources(context.Background(), tc.id, tc.limit)
			}
			var validation *FoundationValidationError
			if tc.validation {
				if !errors.As(err, &validation) {
					t.Fatalf("expected validation: %v", err)
				}
			} else if !errors.Is(err, ErrLinkedSourcesRead) {
				t.Fatalf("missing backend became success: %v", err)
			}
		}
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, errors.New("private SQL locator text")} {
		err := &linkedSourcesReadError{cause: cause}
		if !errors.Is(err, ErrLinkedSourcesRead) || !errors.Is(err, cause) || strings.Contains(err.Error(), "private") {
			t.Fatalf("unsafe typed failure: %v", err)
		}
	}
}
