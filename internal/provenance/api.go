package provenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	FoundationRequestTimeout       = 5 * time.Second
	MaximumFoundationRequestBytes  = 8 << 20
	MaximumFoundationResponseBytes = 8 << 20
	MaximumFoundationExcerptBytes  = 512
	MaximumFoundationFilterBytes   = 256
	MaximumFoundationReceiptBytes  = 128
)

var ErrFoundationNotFound = errors.New("provenance object not found")

type FoundationNotFoundError struct {
	Kind string
	ID   SemanticID
}

func (err *FoundationNotFoundError) Error() string {
	return fmt.Sprintf("provenance %s %s was not found", err.Kind, err.ID)
}

func (err *FoundationNotFoundError) Unwrap() error { return ErrFoundationNotFound }

type FoundationValidationError struct{ Err error }

func (err *FoundationValidationError) Error() string { return err.Err.Error() }
func (err *FoundationValidationError) Unwrap() error { return err.Err }

type FoundationConflictError struct{ Err error }

func (err *FoundationConflictError) Error() string { return err.Err.Error() }
func (err *FoundationConflictError) Unwrap() error { return err.Err }

// ExecutionAuthority is LOOM authorization evidence produced by the main
// policy/idempotency boundary. It is intentionally separate from semantic
// producers, source actors, artifact authors, and approving actors.
type ExecutionAuthority struct {
	ActorID          string `json:"actor_id"`
	OriginNodeID     string `json:"origin_node_id"`
	PolicyDecisionID string `json:"policy_decision_id"`
	Capability       string `json:"capability"`
}

type AuthorizedCandidateRegistration struct {
	Execution ExecutionAuthority                         `json:"execution_authority"`
	Receipt   CandidateRegistrationTransportBatchReceipt `json:"receipt"`
}

type AuthorizedManualOperations struct {
	Execution ExecutionAuthority          `json:"execution_authority"`
	Receipt   ManualOperationBatchReceipt `json:"receipt"`
}

type TextExcerpt struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

type SourceRegistrationTransportReceipt struct {
	ID                  SemanticID   `json:"source_reference_id"`
	SchemaVersion       string       `json:"schema_version"`
	SourceKind          TextExcerpt  `json:"source_kind"`
	Status              string       `json:"status"`
	VerificationPosture string       `json:"verification_posture"`
	ResolutionAt        time.Time    `json:"resolution_at"`
	GapReason           *TextExcerpt `json:"gap_reason,omitempty"`
}

type CandidateRegistrationTransportReceipt struct {
	SchemaVersion       string                               `json:"schema_version"`
	CandidateID         SemanticID                           `json:"candidate_id"`
	SourceResults       []SourceRegistrationTransportReceipt `json:"source_results"`
	State               string                               `json:"state"`
	EffectiveState      string                               `json:"effective_state"`
	Replayed            bool                                 `json:"replayed"`
	ImmediatelyReadable bool                                 `json:"immediately_readable"`
	RegistrationDigest  string                               `json:"registration_digest"`
}

type CandidateRegistrationTransportBatchReceipt struct {
	SchemaVersion      string                                  `json:"schema_version"`
	RegistrationDigest string                                  `json:"registration_digest"`
	Replayed           bool                                    `json:"replayed"`
	Receipts           []CandidateRegistrationTransportReceipt `json:"receipts"`
}

type CandidateSummary struct {
	ID               SemanticID  `json:"candidate_id"`
	SchemaVersion    string      `json:"schema_version"`
	State            string      `json:"state"`
	Domain           string      `json:"domain"`
	Visibility       string      `json:"visibility"`
	RecordKind       string      `json:"record_kind"`
	Claim            TextExcerpt `json:"claim_excerpt"`
	RecordContext    TextExcerpt `json:"record_context_excerpt"`
	AssertionPosture string      `json:"assertion_posture"`
	ProducerID       string      `json:"producer_id"`
	RegisteredAt     time.Time   `json:"registered_at"`
}

type RecordSummary struct {
	ID               SemanticID  `json:"record_id"`
	SchemaVersion    string      `json:"schema_version"`
	RecordKind       string      `json:"record_kind"`
	Claim            TextExcerpt `json:"claim_excerpt"`
	RecordContext    TextExcerpt `json:"record_context_excerpt"`
	Domain           string      `json:"domain"`
	Visibility       string      `json:"visibility"`
	AssertionPosture string      `json:"assertion_posture"`
	AmbiguityCount   int         `json:"ambiguity_count"`
	CreatedAt        time.Time   `json:"created_at"`
}

type RelationshipSummary struct {
	ID                  SemanticID  `json:"relationship_id"`
	SchemaVersion       string      `json:"schema_version"`
	RelationshipType    string      `json:"relationship_type"`
	FromRecordID        SemanticID  `json:"from_record_id"`
	ToRecordID          SemanticID  `json:"to_record_id"`
	ResolutionCaseID    *SemanticID `json:"resolution_case_id,omitempty"`
	Context             TextExcerpt `json:"context_excerpt"`
	EvidenceSourceCount int         `json:"evidence_source_count"`
	CreatedAt           time.Time   `json:"created_at"`
}

type ResolutionCaseSummary struct {
	ID            SemanticID  `json:"case_id"`
	SchemaVersion string      `json:"schema_version"`
	Issue         TextExcerpt `json:"issue_excerpt"`
	Domain        string      `json:"domain"`
	Visibility    string      `json:"visibility"`
	InitialStatus string      `json:"initial_status"`
	CreatedAt     time.Time   `json:"created_at"`
}

type CandidateRegistrationRequest struct {
	Candidates []CandidateRegistration `json:"candidates"`
}

type ManualOperationsRequest struct {
	Scope           LifecycleScope    `json:"scope"`
	Producer        ProducerIdentity  `json:"producer"`
	ProcessingRunID *SemanticID       `json:"processing_run_id,omitempty"`
	PackID          *SemanticID       `json:"pack_id,omitempty"`
	Operations      []json.RawMessage `json:"operations"`
}

// FoundationTransport is the exact, bounded transport surface. It contains no
// ranked search, bulk export, automatic reconciliation, project extraction,
// or scheduling method.
type FoundationTransport interface {
	Readiness() RuntimeReadiness
	ListCandidates(context.Context, PageRequest) (Page[CandidateSummary], error)
	GetCandidate(context.Context, SemanticID, int) (CandidateLifecycleProjection, error)
	ListRecords(context.Context, PageRequest) (Page[RecordSummary], error)
	GetRecord(context.Context, SemanticID, int) (RecordLifecycleProjection, error)
	ListRelationships(context.Context, PageRequest) (Page[RelationshipSummary], error)
	GetRelationship(context.Context, SemanticID, int) (RelationshipLifecycleProjection, error)
	ListResolutionCases(context.Context, PageRequest) (Page[ResolutionCaseSummary], error)
	GetResolutionCase(context.Context, SemanticID, int) (ResolutionCaseLifecycleProjection, error)
	RegisterCandidates(context.Context, string, CandidateRegistrationRequest) (CandidateRegistrationBatchReceipt, error)
	ApplyManualOperations(context.Context, ManualOperationsRequest) (ManualOperationBatchReceipt, error)
}

type FoundationAPI struct {
	runtime *Runtime
	store   *Store
	service *Service
}

func NewFoundationAPI(runtime *Runtime) (*FoundationAPI, error) {
	if runtime == nil || runtime.pool == nil {
		return nil, errors.New("provenance runtime is required")
	}
	store, err := NewStore(runtime.pool)
	if err != nil {
		return nil, err
	}
	service, err := NewService(store)
	if err != nil {
		return nil, err
	}
	return &FoundationAPI{runtime: runtime, store: store, service: service}, nil
}

func (api *FoundationAPI) Readiness() RuntimeReadiness {
	if api == nil {
		return (*Runtime)(nil).Readiness()
	}
	return api.runtime.Readiness()
}

func (api *FoundationAPI) ListCandidates(ctx context.Context, request PageRequest) (Page[CandidateSummary], error) {
	page, err := api.store.ListCandidates(ctx, request)
	if err != nil {
		return Page[CandidateSummary]{}, err
	}
	result := Page[CandidateSummary]{NextTime: page.NextTime, NextID: page.NextID, Truncated: page.Truncated}
	for _, item := range page.Items {
		result.Items = append(result.Items, CandidateSummary{
			ID: item.ID, SchemaVersion: item.SchemaVersion, State: item.State,
			Domain: item.Domain, Visibility: item.Visibility, RecordKind: item.RecordKind,
			Claim: boundedExcerpt(item.Claim), RecordContext: boundedExcerpt(item.RecordContext),
			AssertionPosture: item.AssertionPosture, ProducerID: item.ProducerID,
			RegisteredAt: item.RegisteredAt,
		})
	}
	return result, nil
}

func (api *FoundationAPI) GetCandidate(ctx context.Context, id SemanticID, limit int) (CandidateLifecycleProjection, error) {
	item, found, err := api.service.GetCandidateLifecycle(ctx, id, limit)
	if err != nil {
		return CandidateLifecycleProjection{}, err
	}
	if !found {
		return CandidateLifecycleProjection{}, &FoundationNotFoundError{Kind: "candidate", ID: id}
	}
	return item, nil
}

func (api *FoundationAPI) ListRecords(ctx context.Context, request PageRequest) (Page[RecordSummary], error) {
	page, err := api.store.ListRecords(ctx, request)
	if err != nil {
		return Page[RecordSummary]{}, err
	}
	result := Page[RecordSummary]{NextTime: page.NextTime, NextID: page.NextID, Truncated: page.Truncated}
	for _, item := range page.Items {
		result.Items = append(result.Items, RecordSummary{
			ID: item.ID, SchemaVersion: item.SchemaVersion, RecordKind: item.RecordKind,
			Claim: boundedExcerpt(item.Claim), RecordContext: boundedExcerpt(item.RecordContext),
			Domain: item.Domain, Visibility: item.Visibility, AssertionPosture: item.AssertionPosture,
			AmbiguityCount: len(item.Ambiguity), CreatedAt: item.CreatedAt,
		})
	}
	return result, nil
}

func (api *FoundationAPI) GetRecord(ctx context.Context, id SemanticID, limit int) (RecordLifecycleProjection, error) {
	item, found, err := api.service.GetRecordLifecycle(ctx, id, limit)
	if err != nil {
		return RecordLifecycleProjection{}, err
	}
	if !found {
		return RecordLifecycleProjection{}, &FoundationNotFoundError{Kind: "record", ID: id}
	}
	return item, nil
}

func (api *FoundationAPI) ListRelationships(ctx context.Context, request PageRequest) (Page[RelationshipSummary], error) {
	page, err := api.store.ListRelationships(ctx, request)
	if err != nil {
		return Page[RelationshipSummary]{}, err
	}
	result := Page[RelationshipSummary]{NextTime: page.NextTime, NextID: page.NextID, Truncated: page.Truncated}
	for _, item := range page.Items {
		contextValue := ""
		if item.Context != nil {
			contextValue = *item.Context
		}
		result.Items = append(result.Items, RelationshipSummary{
			ID: item.ID, SchemaVersion: item.SchemaVersion, RelationshipType: item.RelationshipType,
			FromRecordID: item.FromRecordID, ToRecordID: item.ToRecordID,
			ResolutionCaseID: item.ResolutionCaseID, Context: boundedExcerpt(contextValue),
			EvidenceSourceCount: len(item.EvidenceSourceIDs), CreatedAt: item.CreatedAt,
		})
	}
	return result, nil
}

func (api *FoundationAPI) GetRelationship(ctx context.Context, id SemanticID, limit int) (RelationshipLifecycleProjection, error) {
	item, found, err := api.service.GetRelationshipLifecycle(ctx, id, limit)
	if err != nil {
		return RelationshipLifecycleProjection{}, err
	}
	if !found {
		return RelationshipLifecycleProjection{}, &FoundationNotFoundError{Kind: "relationship", ID: id}
	}
	return item, nil
}

func (api *FoundationAPI) ListResolutionCases(ctx context.Context, request PageRequest) (Page[ResolutionCaseSummary], error) {
	page, err := api.store.ListResolutionCases(ctx, request)
	if err != nil {
		return Page[ResolutionCaseSummary]{}, err
	}
	result := Page[ResolutionCaseSummary]{NextTime: page.NextTime, NextID: page.NextID, Truncated: page.Truncated}
	for _, item := range page.Items {
		result.Items = append(result.Items, ResolutionCaseSummary{
			ID: item.ID, SchemaVersion: item.SchemaVersion, Issue: boundedExcerpt(item.Issue),
			Domain: item.Domain, Visibility: item.Visibility, InitialStatus: item.InitialStatus,
			CreatedAt: item.CreatedAt,
		})
	}
	return result, nil
}

func (api *FoundationAPI) GetResolutionCase(ctx context.Context, id SemanticID, limit int) (ResolutionCaseLifecycleProjection, error) {
	item, found, err := api.service.GetResolutionCaseLifecycle(ctx, id, limit)
	if err != nil {
		return ResolutionCaseLifecycleProjection{}, err
	}
	if !found {
		return ResolutionCaseLifecycleProjection{}, &FoundationNotFoundError{Kind: "case", ID: id}
	}
	return item, nil
}

func ProjectCandidateRegistrationReceipt(receipt CandidateRegistrationBatchReceipt) CandidateRegistrationTransportBatchReceipt {
	projected := CandidateRegistrationTransportBatchReceipt{
		SchemaVersion: receipt.SchemaVersion, RegistrationDigest: receipt.RegistrationDigest,
		Replayed: receipt.Replayed, Receipts: make([]CandidateRegistrationTransportReceipt, 0, len(receipt.Receipts)),
	}
	for _, item := range receipt.Receipts {
		candidate := CandidateRegistrationTransportReceipt{
			SchemaVersion: item.SchemaVersion, CandidateID: item.CandidateID,
			State: item.State, EffectiveState: item.EffectiveState, Replayed: item.Replayed,
			ImmediatelyReadable: item.ImmediatelyReadable, RegistrationDigest: item.RegistrationDigest,
			SourceResults: make([]SourceRegistrationTransportReceipt, 0, len(item.SourceResults)),
		}
		for _, source := range item.SourceResults {
			var gapReason *TextExcerpt
			if source.GapReason != nil {
				excerpt := boundedText(*source.GapReason, MaximumFoundationReceiptBytes)
				gapReason = &excerpt
			}
			candidate.SourceResults = append(candidate.SourceResults, SourceRegistrationTransportReceipt{
				ID: source.ID, SchemaVersion: source.SchemaVersion,
				SourceKind: boundedText(source.SourceKind, MaximumFoundationReceiptBytes),
				Status:     source.Status, VerificationPosture: source.VerificationPosture,
				ResolutionAt: source.ResolutionAt, GapReason: gapReason,
			})
		}
		projected.Receipts = append(projected.Receipts, candidate)
	}
	return projected
}

func (api *FoundationAPI) RegisterCandidates(ctx context.Context, replayKey string, request CandidateRegistrationRequest) (CandidateRegistrationBatchReceipt, error) {
	batch := CandidateRegistrationBatch{ReplayKey: strings.TrimSpace(replayKey), Candidates: request.Candidates}
	for index := range batch.Candidates {
		if batch.Candidates[index].SchemaVersion == "" {
			batch.Candidates[index].SchemaVersion = SchemaVersion
		}
		if strings.TrimSpace(batch.Candidates[index].Visibility) == "" {
			batch.Candidates[index].Visibility = "private"
		}
	}
	if _, _, _, _, err := validateRegistrationBatch(batch); err != nil {
		return CandidateRegistrationBatchReceipt{}, &FoundationValidationError{Err: err}
	}
	receipt, err := api.service.RegisterCandidateBatch(ctx, batch)
	if err == nil {
		return receipt, nil
	}
	if errors.Is(err, ErrReplayConflict) {
		return CandidateRegistrationBatchReceipt{}, &FoundationConflictError{Err: err}
	}
	if isFoundationDatabaseError(err) {
		return CandidateRegistrationBatchReceipt{}, err
	}
	return CandidateRegistrationBatchReceipt{}, &FoundationConflictError{Err: err}
}

func (api *FoundationAPI) ApplyManualOperations(ctx context.Context, request ManualOperationsRequest) (ManualOperationBatchReceipt, error) {
	operations, err := decodeLifecycleOperations(request.Operations)
	if err != nil {
		return ManualOperationBatchReceipt{}, &FoundationValidationError{Err: err}
	}
	batch := ManualOperationBatch{
		Scope: request.Scope, Producer: request.Producer,
		ProcessingRunID: request.ProcessingRunID, PackID: request.PackID,
		Operations: operations,
	}
	if _, _, err := prepareOperationBatch(batch); err != nil {
		return ManualOperationBatchReceipt{}, &FoundationValidationError{Err: err}
	}
	receipt, err := api.service.ApplyOperations(ctx, batch)
	if err == nil {
		return receipt, nil
	}
	if errors.Is(err, ErrOperationReplayConflict) || errors.Is(err, ErrPartialOperationReplay) || errors.Is(err, ErrLifecycleConflict) {
		return ManualOperationBatchReceipt{}, &FoundationConflictError{Err: err}
	}
	if isFoundationDatabaseError(err) {
		return ManualOperationBatchReceipt{}, err
	}
	return ManualOperationBatchReceipt{}, &FoundationConflictError{Err: err}
}

func ValidateFoundationPageRequest(request PageRequest) error {
	if _, err := request.normalized(); err != nil {
		return err
	}
	if len(request.Domain) > MaximumFoundationFilterBytes || len(request.Visibility) > MaximumFoundationFilterBytes {
		return fmt.Errorf("domain and visibility filters must be at most %d bytes", MaximumFoundationFilterBytes)
	}
	return nil
}

func decodeLifecycleOperations(documents []json.RawMessage) ([]LifecycleOperation, error) {
	if len(documents) == 0 || len(documents) > MaximumOperationBatch {
		return nil, fmt.Errorf("operation batch must contain between 1 and %d operations", MaximumOperationBatch)
	}
	operations := make([]LifecycleOperation, 0, len(documents))
	for index, document := range documents {
		if len(document) == 0 || len(document) > MaximumDocumentBytes {
			return nil, fmt.Errorf("operation %d exceeds its bounded payload", index)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(document, &fields); err != nil || fields == nil {
			return nil, fmt.Errorf("operation %d must be one JSON object", index)
		}
		var operationType string
		if raw, ok := fields["operation_type"]; !ok || json.Unmarshal(raw, &operationType) != nil || strings.TrimSpace(operationType) == "" {
			return nil, fmt.Errorf("operation %d requires operation_type", index)
		}
		delete(fields, "operation_type")
		payload, err := json.Marshal(fields)
		if err != nil {
			return nil, fmt.Errorf("encode operation %d: %w", index, err)
		}
		var operation LifecycleOperation
		switch operationType {
		case "accept_candidate":
			operation, err = decodeLifecycleOperation[AcceptCandidateOperation](payload)
		case "consolidate_candidate":
			operation, err = decodeLifecycleOperation[ConsolidateCandidateOperation](payload)
		case "reject_candidate":
			operation, err = decodeLifecycleOperation[RejectCandidateOperation](payload)
		case "defer_candidate":
			operation, err = decodeLifecycleOperation[DeferCandidateOperation](payload)
		case "link_candidate_evidence":
			operation, err = decodeLifecycleOperation[LinkCandidateEvidenceOperation](payload)
		case "append_qualification":
			operation, err = decodeLifecycleOperation[AppendQualificationOperation](payload)
		case "append_temporal_interpretation":
			operation, err = decodeLifecycleOperation[AppendTemporalInterpretationOperation](payload)
		case "append_assertion_posture":
			operation, err = decodeLifecycleOperation[AppendAssertionPostureOperation](payload)
		case "create_resolution_case":
			operation, err = decodeLifecycleOperation[CreateResolutionCaseOperation](payload)
		case "update_resolution_case":
			operation, err = decodeLifecycleOperation[UpdateResolutionCaseOperation](payload)
		case "close_resolution_case":
			operation, err = decodeLifecycleOperation[CloseResolutionCaseOperation](payload)
		case "attach_case_member":
			operation, err = decodeLifecycleOperation[AttachCaseMemberOperation](payload)
		case "add_relationship":
			operation, err = decodeLifecycleOperation[AddRelationshipOperation](payload)
		case "end_relationship":
			operation, err = decodeLifecycleOperation[EndRelationshipOperation](payload)
		default:
			return nil, fmt.Errorf("operation %d has unsupported operation_type %q", index, operationType)
		}
		if err != nil {
			return nil, fmt.Errorf("operation %d is invalid: %w", index, err)
		}
		operations = append(operations, operation)
	}
	return operations, nil
}

func decodeLifecycleOperation[T LifecycleOperation](payload []byte) (LifecycleOperation, error) {
	var operation T
	if err := decodeStrictJSON(payload, &operation); err != nil {
		return nil, err
	}
	return operation, nil
}

func decodeStrictJSON(payload []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("request must contain one JSON object")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func boundedExcerpt(value string) TextExcerpt {
	return boundedText(value, MaximumFoundationExcerptBytes)
}

func boundedText(value string, limit int) TextExcerpt {
	if len(value) <= limit {
		return TextExcerpt{Text: value}
	}
	cut := limit
	for cut > 0 && !utf8.ValidString(value[:cut]) {
		cut--
	}
	return TextExcerpt{Text: value[:cut], Truncated: true}
}

func isFoundationDatabaseError(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (api *FoundationAPI) GetCandidateWithSources(ctx context.Context, id SemanticID, limit int) (CandidateWithSources, error) {
	parent, sources, err := api.readExactSources(ctx, "candidate", id, limit)
	if err != nil {
		return CandidateWithSources{}, err
	}
	return CandidateWithSources{CandidateLifecycleProjection: parent.(CandidateLifecycleProjection), Sources: sources}, nil
}

func (api *FoundationAPI) GetRecordWithSources(ctx context.Context, id SemanticID, limit int) (RecordWithSources, error) {
	parent, sources, err := api.readExactSources(ctx, "record", id, limit)
	if err != nil {
		return RecordWithSources{}, err
	}
	return RecordWithSources{RecordLifecycleProjection: parent.(RecordLifecycleProjection), Sources: sources}, nil
}

var _ LinkedSourcesTransport = (*FoundationAPI)(nil)
