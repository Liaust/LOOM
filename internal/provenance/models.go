package provenance

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	SchemaVersion                 = "1.0"
	ReconciliationProtocolVersion = "2.0"
	DefaultPageLimit              = 50
	MaximumPageLimit              = 100
)

// SemanticID preserves the upstream UUID-shaped identity contract. The store
// never invents a replacement identifier for an object supplied by a caller.
type SemanticID string

func ParseSemanticID(value string) (SemanticID, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return "", fmt.Errorf("semantic id %q is not a canonical UUID", value)
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return "", fmt.Errorf("semantic id %q is not a canonical UUID", value)
		}
	}
	return SemanticID(value), nil
}

func (id SemanticID) Validate() error {
	_, err := ParseSemanticID(string(id))
	return err
}

type ProducerIdentity struct {
	ProducerID   string         `json:"producer_id"`
	ProducerKind string         `json:"producer_kind"`
	DisplayName  string         `json:"display_name,omitempty"`
	RunID        *SemanticID    `json:"run_id,omitempty"`
	TaskID       string         `json:"task_id,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

type StructuralAnchors struct {
	Entities []string `json:"entities,omitempty"`
	Topics   []string `json:"topics,omitempty"`
	Projects []string `json:"projects,omitempty"`
}

type TemporalInterpretation struct {
	Interpretation string     `json:"interpretation"`
	SourceText     string     `json:"source_text,omitempty"`
	ObservedAt     *time.Time `json:"observed_at,omitempty"`
	ValidFrom      *time.Time `json:"valid_from,omitempty"`
	ValidUntil     *time.Time `json:"valid_until,omitempty"`
	Uncertainty    string     `json:"uncertainty,omitempty"`
}

type Candidate struct {
	ID               SemanticID      `json:"candidate_id"`
	SchemaVersion    string          `json:"schema_version"`
	State            string          `json:"state"`
	Domain           string          `json:"domain"`
	Visibility       string          `json:"visibility"`
	RecordKind       string          `json:"record_kind"`
	Claim            string          `json:"claim"`
	RecordContext    string          `json:"record_context"`
	AssertionPosture string          `json:"assertion_posture"`
	ProducerID       string          `json:"producer_id"`
	RegisteredAt     time.Time       `json:"registered_at"`
	Submitted        json.RawMessage `json:"submitted"`
	Payload          json.RawMessage `json:"payload"`
}

type SourceReference struct {
	ID                  SemanticID      `json:"source_reference_id"`
	CandidateID         *SemanticID     `json:"candidate_id,omitempty"`
	SchemaVersion       string          `json:"schema_version"`
	SourceKind          string          `json:"source_kind"`
	Status              string          `json:"status"`
	VerificationPosture string          `json:"verification_posture"`
	ResolverName        string          `json:"resolver_name"`
	ResolverVersion     string          `json:"resolver_version"`
	ResolutionAt        time.Time       `json:"resolution_at"`
	CanonicalLocator    *string         `json:"canonical_locator,omitempty"`
	VersionAddress      *string         `json:"version_address,omitempty"`
	ContentDigest       *string         `json:"content_digest,omitempty"`
	SourceContext       *string         `json:"source_context,omitempty"`
	GapReason           *string         `json:"gap_reason,omitempty"`
	Submitted           json.RawMessage `json:"submitted"`
	Payload             json.RawMessage `json:"payload"`
}

type Record struct {
	ID               SemanticID             `json:"record_id"`
	SchemaVersion    string                 `json:"schema_version"`
	Claim            string                 `json:"claim"`
	RecordKind       string                 `json:"record_kind"`
	RecordContext    string                 `json:"record_context"`
	Ambiguity        []string               `json:"ambiguity,omitempty"`
	Modality         *string                `json:"modality,omitempty"`
	Domain           string                 `json:"domain"`
	Visibility       string                 `json:"visibility"`
	AssertionPosture string                 `json:"assertion_posture"`
	SourceActorID    *string                `json:"source_actor_id,omitempty"`
	ArtifactAuthorID *string                `json:"artifact_author_id,omitempty"`
	ApprovingActorID *string                `json:"approving_actor_id,omitempty"`
	Temporal         TemporalInterpretation `json:"temporal_interpretation"`
	Anchors          *StructuralAnchors     `json:"anchors,omitempty"`
	CreatedAt        time.Time              `json:"created_at"`
	Payload          json.RawMessage        `json:"payload"`
}

type RecordSource struct {
	RecordID          SemanticID `json:"record_id"`
	SourceReferenceID SemanticID `json:"source_reference_id"`
	LinkedAt          time.Time  `json:"linked_at"`
}

type RecordProducer struct {
	RecordID    SemanticID       `json:"record_id"`
	CandidateID *SemanticID      `json:"candidate_id,omitempty"`
	Producer    ProducerIdentity `json:"producer"`
	LinkedAt    time.Time        `json:"linked_at"`
}

type ResolutionCase struct {
	ID                   SemanticID      `json:"case_id"`
	SchemaVersion        string          `json:"schema_version"`
	Issue                string          `json:"issue"`
	Domain               string          `json:"domain"`
	Visibility           string          `json:"visibility"`
	InitialStatus        string          `json:"initial_status"`
	CreatedAt            time.Time       `json:"created_at"`
	CreatedByOperationID *SemanticID     `json:"created_by_operation_id,omitempty"`
	Payload              json.RawMessage `json:"payload"`
}

type CaseMember struct {
	CaseID      SemanticID  `json:"case_id"`
	MemberType  string      `json:"member_type"`
	CandidateID *SemanticID `json:"candidate_id,omitempty"`
	RecordID    *SemanticID `json:"record_id,omitempty"`
	Role        string      `json:"role"`
	AttachedAt  time.Time   `json:"attached_at"`
	OperationID *SemanticID `json:"operation_id,omitempty"`
}

type CaseEvent struct {
	ID                SemanticID       `json:"event_id"`
	CaseID            SemanticID       `json:"case_id"`
	SchemaVersion     string           `json:"schema_version"`
	EventType         string           `json:"event_type"`
	OccurredAt        time.Time        `json:"occurred_at"`
	Producer          ProducerIdentity `json:"producer"`
	Summary           string           `json:"summary"`
	Outcome           *string          `json:"outcome,omitempty"`
	EvidenceSourceIDs []SemanticID     `json:"evidence_source_reference_ids,omitempty"`
	OperationID       *SemanticID      `json:"operation_id,omitempty"`
	Payload           json.RawMessage  `json:"payload"`
}

type Relationship struct {
	ID                   SemanticID       `json:"relationship_id"`
	SchemaVersion        string           `json:"schema_version"`
	RelationshipType     string           `json:"relationship_type"`
	FromRecordID         SemanticID       `json:"from_record_id"`
	ToRecordID           SemanticID       `json:"to_record_id"`
	ResolutionCaseID     *SemanticID      `json:"resolution_case_id,omitempty"`
	Context              *string          `json:"context,omitempty"`
	Coverage             json.RawMessage  `json:"coverage,omitempty"`
	EvidenceSourceIDs    []SemanticID     `json:"evidence_source_reference_ids,omitempty"`
	CreatedAt            time.Time        `json:"created_at"`
	CreatedBy            ProducerIdentity `json:"created_by"`
	CreatedByOperationID *SemanticID      `json:"created_by_operation_id,omitempty"`
	Payload              json.RawMessage  `json:"payload"`
}

type ProcessingRun struct {
	ID            SemanticID      `json:"processing_run_id"`
	RunKind       string          `json:"run_kind"`
	SchemaVersion string          `json:"schema_version"`
	InputHash     string          `json:"input_hash"`
	Status        string          `json:"status"`
	Actor         json.RawMessage `json:"actor"`
	Bounds        json.RawMessage `json:"bounds,omitempty"`
	StartedAt     time.Time       `json:"started_at"`
	CompletedAt   *time.Time      `json:"completed_at,omitempty"`
	Payload       json.RawMessage `json:"payload"`
}

type Operation struct {
	ID              SemanticID      `json:"operation_id"`
	ProcessingRunID *SemanticID     `json:"processing_run_id,omitempty"`
	PackID          *SemanticID     `json:"pack_id,omitempty"`
	OperationType   string          `json:"operation_type"`
	SchemaVersion   string          `json:"schema_version"`
	Producer        json.RawMessage `json:"producer"`
	OccurredAt      time.Time       `json:"occurred_at"`
	InputHash       string          `json:"input_hash"`
	Result          string          `json:"result"`
	Operation       json.RawMessage `json:"operation"`
	ResultPayload   json.RawMessage `json:"result_payload"`
	AppliedAt       time.Time       `json:"applied_at"`
}

type LedgerEvent struct {
	ObjectID    SemanticID      `json:"object_id"`
	EventType   string          `json:"event_type"`
	OperationID SemanticID      `json:"operation_id"`
	OccurredAt  time.Time       `json:"occurred_at"`
	Payload     json.RawMessage `json:"payload"`
}

type EvidenceRegistration struct {
	ID                            SemanticID       `json:"registration_id"`
	ReconciliationProtocolVersion string           `json:"reconciliation_protocol_version"`
	SourceReferenceID             SemanticID       `json:"source_reference_id"`
	Domain                        string           `json:"domain"`
	Visibility                    string           `json:"visibility"`
	EvidenceContext               string           `json:"evidence_context"`
	Producer                      ProducerIdentity `json:"producer"`
	RegisteredAt                  time.Time        `json:"registered_at"`
	ResolutionCaseID              *SemanticID      `json:"resolution_case_id,omitempty"`
	ClarificationID               *SemanticID      `json:"clarification_id,omitempty"`
	Payload                       json.RawMessage  `json:"payload"`
}

type CandidateEvidenceLink struct {
	ID                SemanticID       `json:"link_id"`
	CandidateID       SemanticID       `json:"candidate_id"`
	SourceReferenceID SemanticID       `json:"source_reference_id"`
	OperationID       SemanticID       `json:"operation_id"`
	LinkedAt          time.Time        `json:"linked_at"`
	Producer          ProducerIdentity `json:"producer"`
	Payload           json.RawMessage  `json:"payload"`
}

type CandidateLineage struct {
	CandidateID            SemanticID `json:"candidate_id"`
	DerivedFromCandidateID SemanticID `json:"derived_from_candidate_id"`
	RegisteredAt           time.Time  `json:"registered_at"`
}

type RegistrationReplay struct {
	ReplayKey          string           `json:"replay_key"`
	WorkflowKey        string           `json:"workflow_key"`
	RegistrationDigest string           `json:"registration_digest"`
	Producer           ProducerIdentity `json:"producer"`
	CandidateIDs       []SemanticID     `json:"candidate_ids"`
	Receipts           json.RawMessage  `json:"receipts"`
	RegisteredAt       time.Time        `json:"registered_at"`
	Payload            json.RawMessage  `json:"payload"`
}

type PageRequest struct {
	Limit      int
	AfterTime  *time.Time
	AfterID    *SemanticID
	Domain     string
	Visibility string
}

type Page[T any] struct {
	Items     []T         `json:"items"`
	NextTime  *time.Time  `json:"next_time,omitempty"`
	NextID    *SemanticID `json:"next_id,omitempty"`
	Truncated bool        `json:"truncated"`
}

func (request PageRequest) normalized() (PageRequest, error) {
	if request.Limit == 0 {
		request.Limit = DefaultPageLimit
	}
	if request.Limit < 1 || request.Limit > MaximumPageLimit {
		return PageRequest{}, fmt.Errorf("page limit must be between 1 and %d", MaximumPageLimit)
	}
	if (request.AfterTime == nil) != (request.AfterID == nil) {
		return PageRequest{}, errors.New("pagination cursor requires both time and id")
	}
	if request.AfterID != nil {
		if err := request.AfterID.Validate(); err != nil {
			return PageRequest{}, err
		}
	}
	return request, nil
}

func validateRequired(value, field string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	return nil
}

func validateDocument(value json.RawMessage, field string, nullable bool) error {
	trimmed := strings.TrimSpace(string(value))
	if trimmed == "" {
		if nullable {
			return nil
		}
		return fmt.Errorf("%s is required", field)
	}
	if !json.Valid(value) {
		return fmt.Errorf("%s must contain valid JSON", field)
	}
	if !nullable && trimmed == "null" {
		return fmt.Errorf("%s cannot be null", field)
	}
	return nil
}

func validateTimestamp(value time.Time, field string) error {
	if value.IsZero() {
		return fmt.Errorf("%s is required", field)
	}
	return nil
}
