package provenance

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	MaximumRegistrationBatch = 100
	MaximumSourcesPerObject  = 50
	MaximumDocumentBytes     = 64 * 1024
)

var ErrOperationReplayConflict = errors.New("operation replay identifier has a different payload")
var ErrPartialOperationReplay = errors.New("operation batch mixes replayed and new operations")

// ActorReference is semantic evidence about a source, artifact, or approving
// actor. It is deliberately not an authenticated LOOM request actor.
type ActorReference struct {
	ActorID           string      `json:"actor_id,omitempty"`
	DisplayName       string      `json:"display_name,omitempty"`
	ActorKind         string      `json:"actor_kind,omitempty"`
	SourceReferenceID *SemanticID `json:"source_reference_id,omitempty"`
}

type RelationshipHint struct {
	RelationshipType string `json:"relationship_type"`
	TargetHint       string `json:"target_hint"`
	Context          string `json:"context,omitempty"`
}

// SourceRegistration carries a resolver result into the ledger. The service
// does not upgrade an unresolved or unverified source posture.
type SourceRegistration struct {
	SourceReferenceID SemanticID      `json:"source_reference_id,omitempty"`
	Kind              string          `json:"kind"`
	Status            string          `json:"status"`
	Verification      string          `json:"verification_posture"`
	ResolverName      string          `json:"resolver_name"`
	ResolverVersion   string          `json:"resolver_version"`
	ResolutionAt      time.Time       `json:"resolution_at,omitempty"`
	CanonicalLocator  *string         `json:"canonical_locator,omitempty"`
	VersionAddress    *string         `json:"version_address,omitempty"`
	ContentDigest     *string         `json:"content_digest,omitempty"`
	SourceContext     *string         `json:"source_context,omitempty"`
	GapReason         *string         `json:"gap_reason,omitempty"`
	Submitted         json.RawMessage `json:"submitted"`
	Details           json.RawMessage `json:"details,omitempty"`
}

type CandidateRegistration struct {
	SchemaVersion           string                 `json:"schema_version"`
	Claim                   string                 `json:"claim"`
	RecordKind              string                 `json:"record_kind"`
	RecordContext           string                 `json:"record_context"`
	Sources                 []SourceRegistration   `json:"sources"`
	Ambiguity               []string               `json:"ambiguity,omitempty"`
	Domain                  string                 `json:"domain"`
	Visibility              string                 `json:"visibility"`
	Temporal                TemporalInterpretation `json:"temporal_interpretation"`
	AssertionPosture        string                 `json:"assertion_posture"`
	Producer                ProducerIdentity       `json:"producer"`
	SourceActor             *ActorReference        `json:"source_actor,omitempty"`
	ArtifactAuthor          *ActorReference        `json:"artifact_author,omitempty"`
	ApprovingActor          *ActorReference        `json:"approving_actor,omitempty"`
	Anchors                 *StructuralAnchors     `json:"anchors,omitempty"`
	RelationshipHints       []RelationshipHint     `json:"relationship_hints,omitempty"`
	DerivedFromCandidateIDs []SemanticID           `json:"derived_from_candidate_ids,omitempty"`
}

type CandidateRegistrationBatch struct {
	ReplayKey  string                  `json:"replay_key"`
	Candidates []CandidateRegistration `json:"candidates"`
}

type CandidateRegistrationReceipt struct {
	SchemaVersion       string            `json:"schema_version"`
	CandidateID         SemanticID        `json:"candidate_id"`
	SourceResults       []SourceReference `json:"source_results"`
	State               string            `json:"state"`
	EffectiveState      string            `json:"effective_state"`
	Replayed            bool              `json:"replayed"`
	ImmediatelyReadable bool              `json:"immediately_readable"`
	RegistrationDigest  string            `json:"registration_digest"`
}

type CandidateRegistrationBatchReceipt struct {
	SchemaVersion      string                         `json:"schema_version"`
	RegistrationDigest string                         `json:"registration_digest"`
	Replayed           bool                           `json:"replayed"`
	Receipts           []CandidateRegistrationReceipt `json:"receipts"`
}

type EvidenceRegistrationRequest struct {
	RegistrationID   SemanticID         `json:"registration_id,omitempty"`
	Domain           string             `json:"domain"`
	Visibility       string             `json:"visibility"`
	EvidenceContext  string             `json:"evidence_context"`
	Producer         ProducerIdentity   `json:"producer"`
	RegisteredAt     time.Time          `json:"registered_at,omitempty"`
	ResolutionCaseID *SemanticID        `json:"resolution_case_id,omitempty"`
	ClarificationID  *SemanticID        `json:"clarification_id,omitempty"`
	Source           SourceRegistration `json:"source"`
}

type EvidenceRegistrationReceipt struct {
	SchemaVersion     string          `json:"schema_version"`
	RegistrationID    SemanticID      `json:"registration_id"`
	SourceReferenceID SemanticID      `json:"source_reference_id"`
	SourceResult      SourceReference `json:"source_result"`
	State             string          `json:"state"`
}

type IDFactory func() (SemanticID, error)
type Clock func() time.Time

type Service struct {
	store     *Store
	idFactory IDFactory
	clock     Clock
}

type ServiceOption func(*Service)

func WithIDFactory(factory IDFactory) ServiceOption {
	return func(service *Service) {
		if factory != nil {
			service.idFactory = factory
		}
	}
}

func WithClock(clock Clock) ServiceOption {
	return func(service *Service) {
		if clock != nil {
			service.clock = clock
		}
	}
}

func NewService(store *Store, options ...ServiceOption) (*Service, error) {
	if store == nil {
		return nil, errors.New("provenance service store is required")
	}
	service := &Service{
		store:     store,
		idFactory: newSemanticID,
		clock: func() time.Time {
			return time.Now().UTC()
		},
	}
	for _, option := range options {
		option(service)
	}
	return service, nil
}

func (service *Service) RegisterCandidate(ctx context.Context, replayKey string, registration CandidateRegistration) (CandidateRegistrationReceipt, error) {
	receipt, err := service.RegisterCandidateBatch(ctx, CandidateRegistrationBatch{
		ReplayKey: replayKey, Candidates: []CandidateRegistration{registration},
	})
	if err != nil {
		return CandidateRegistrationReceipt{}, err
	}
	return receipt.Receipts[0], nil
}

func (service *Service) RegisterCandidateBatch(ctx context.Context, batch CandidateRegistrationBatch) (CandidateRegistrationBatchReceipt, error) {
	for index := range batch.Candidates {
		if batch.Candidates[index].SchemaVersion == "" {
			batch.Candidates[index].SchemaVersion = SchemaVersion
		}
		if strings.TrimSpace(batch.Candidates[index].Visibility) == "" {
			batch.Candidates[index].Visibility = "private"
		}
	}
	producer, payload, workflowKey, digest, err := validateRegistrationBatch(batch)
	if err != nil {
		return CandidateRegistrationBatchReceipt{}, err
	}
	lockKeys := []string{"registration-key:" + batch.ReplayKey, "registration-workflow:" + workflowKey + ":" + digest}
	for _, registration := range batch.Candidates {
		for _, parentID := range registration.DerivedFromCandidateIDs {
			lockKeys = append(lockKeys, "candidate:"+string(parentID))
		}
	}
	var result CandidateRegistrationBatchReceipt
	err = service.store.Transact(ctx, func(tx *Store) error {
		if err := lockTransactionKeys(ctx, tx, lockKeys...); err != nil {
			return err
		}
		if stored, found, err := tx.getRegistrationReplayByKey(ctx, batch.ReplayKey); err != nil {
			return err
		} else if found {
			if stored.RegistrationDigest != digest ||
				!sameProducer(stored.Producer, producer) || !jsonEqual(stored.Payload, payload) {
				return ErrReplayConflict
			}
			result, err = decodeCandidateReplay(stored, true)
			return err
		}
		if stored, found, err := tx.GetRegistrationReplay(ctx, workflowKey, digest); err != nil {
			return err
		} else if found {
			if !sameProducer(stored.Producer, producer) || !jsonEqual(stored.Payload, payload) {
				return ErrReplayConflict
			}
			alias := stored
			alias.ReplayKey = batch.ReplayKey
			alias.WorkflowKey = registrationAliasWorkflowKey(workflowKey, batch.ReplayKey)
			bound, _, err := tx.PutRegistrationReplay(ctx, alias)
			if err != nil {
				return err
			}
			result, err = decodeCandidateReplay(bound, true)
			return err
		}
		for candidateIndex, registration := range batch.Candidates {
			for _, parentID := range registration.DerivedFromCandidateIDs {
				parent, found, err := tx.GetCandidate(ctx, parentID)
				if err != nil {
					return fmt.Errorf("load candidate %d lineage parent %s: %w", candidateIndex, parentID, err)
				}
				if !found || parent.Domain != registration.Domain || parent.Visibility != registration.Visibility {
					return fmt.Errorf("candidate %d lineage parent %s is outside its semantic scope", candidateIndex, parentID)
				}
			}
		}

		registeredAt := service.clock().UTC()
		receipts := make([]CandidateRegistrationReceipt, 0, len(batch.Candidates))
		candidateIDs := make([]SemanticID, 0, len(batch.Candidates))
		for _, registration := range batch.Candidates {
			candidateID, err := service.idFactory()
			if err != nil {
				return fmt.Errorf("allocate candidate id: %w", err)
			}
			sources := make([]SourceReference, 0, len(registration.Sources))
			for _, sourceInput := range registration.Sources {
				source, err := service.materializeSource(sourceInput, &candidateID, registeredAt)
				if err != nil {
					return err
				}
				sources = append(sources, source)
			}
			pendingPayload, err := json.Marshal(struct {
				SchemaVersion string                `json:"schema_version"`
				CandidateID   SemanticID            `json:"candidate_id"`
				Registration  CandidateRegistration `json:"registration"`
				SourceResults []SourceReference     `json:"source_results"`
				RegisteredAt  time.Time             `json:"registered_at"`
				State         string                `json:"state"`
			}{SchemaVersion, candidateID, registration, sources, registeredAt, "pending"})
			if err != nil {
				return fmt.Errorf("encode pending candidate: %w", err)
			}
			submitted, err := json.Marshal(registration)
			if err != nil {
				return fmt.Errorf("encode candidate registration: %w", err)
			}
			candidate := Candidate{
				ID: candidateID, SchemaVersion: SchemaVersion, State: "pending",
				Domain: registration.Domain, Visibility: registration.Visibility,
				RecordKind: registration.RecordKind, Claim: registration.Claim,
				RecordContext:    registration.RecordContext,
				AssertionPosture: registration.AssertionPosture,
				ProducerID:       registration.Producer.ProducerID, RegisteredAt: registeredAt,
				Submitted: submitted, Payload: pendingPayload,
			}
			if err := tx.AppendCandidate(ctx, candidate); err != nil {
				return err
			}
			for _, source := range sources {
				if err := tx.AppendSourceReference(ctx, source); err != nil {
					return err
				}
			}
			for _, parentID := range registration.DerivedFromCandidateIDs {
				if err := tx.AppendCandidateLineage(ctx, CandidateLineage{
					CandidateID: candidateID, DerivedFromCandidateID: parentID, RegisteredAt: registeredAt,
				}); err != nil {
					return err
				}
			}
			candidateIDs = append(candidateIDs, candidateID)
			receipts = append(receipts, CandidateRegistrationReceipt{
				SchemaVersion: SchemaVersion, CandidateID: candidateID, SourceResults: sources,
				State: "pending", EffectiveState: "pending", ImmediatelyReadable: true,
				RegistrationDigest: digest,
			})
		}
		receiptJSON, err := json.Marshal(receipts)
		if err != nil {
			return fmt.Errorf("encode registration receipts: %w", err)
		}
		replay := RegistrationReplay{
			ReplayKey: batch.ReplayKey, WorkflowKey: workflowKey, RegistrationDigest: digest,
			Producer: producer, CandidateIDs: candidateIDs, Receipts: receiptJSON,
			RegisteredAt: registeredAt, Payload: payload,
		}
		stored, replayed, err := tx.PutRegistrationReplay(ctx, replay)
		if err != nil {
			return err
		}
		result, err = decodeCandidateReplay(stored, replayed)
		return err
	})
	return result, err
}

func (service *Service) RegisterEvidence(ctx context.Context, request EvidenceRegistrationRequest) (EvidenceRegistrationReceipt, error) {
	if err := validateEvidenceRequest(request); err != nil {
		return EvidenceRegistrationReceipt{}, err
	}
	registeredAt := request.RegisteredAt.UTC()
	if registeredAt.IsZero() {
		registeredAt = service.clock().UTC()
	}
	registrationID := request.RegistrationID
	var err error
	if registrationID == "" {
		registrationID, err = service.idFactory()
		if err != nil {
			return EvidenceRegistrationReceipt{}, fmt.Errorf("allocate evidence registration id: %w", err)
		}
	}
	source, err := service.materializeSource(request.Source, nil, registeredAt)
	if err != nil {
		return EvidenceRegistrationReceipt{}, err
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return EvidenceRegistrationReceipt{}, fmt.Errorf("encode evidence registration: %w", err)
	}
	if len(payload) > MaximumDocumentBytes {
		return EvidenceRegistrationReceipt{}, errors.New("evidence registration exceeds its bounded payload")
	}
	registration := EvidenceRegistration{
		ID: registrationID, ReconciliationProtocolVersion: ReconciliationProtocolVersion,
		SourceReferenceID: source.ID, Domain: request.Domain, Visibility: request.Visibility,
		EvidenceContext: request.EvidenceContext, Producer: request.Producer,
		RegisteredAt: registeredAt, ResolutionCaseID: request.ResolutionCaseID,
		ClarificationID: request.ClarificationID, Payload: payload,
	}
	err = service.store.Transact(ctx, func(tx *Store) error {
		if err := lockTransactionKeys(ctx, tx, "evidence-registration:"+string(registrationID), "source:"+string(source.ID)); err != nil {
			return err
		}
		if request.ResolutionCaseID != nil {
			item, found, err := tx.GetResolutionCase(ctx, *request.ResolutionCaseID)
			if err != nil {
				return err
			}
			if !found || item.Domain != request.Domain || item.Visibility != request.Visibility {
				return errors.New("evidence registration resolution case is outside its scope")
			}
		}
		if err := tx.AppendSourceReference(ctx, source); err != nil {
			return err
		}
		return tx.AppendEvidenceRegistration(ctx, registration)
	})
	if err != nil {
		return EvidenceRegistrationReceipt{}, err
	}
	return EvidenceRegistrationReceipt{
		SchemaVersion: SchemaVersion, RegistrationID: registrationID,
		SourceReferenceID: source.ID, SourceResult: source, State: "registered",
	}, nil
}

func (service *Service) materializeSource(input SourceRegistration, candidateID *SemanticID, fallbackTime time.Time) (SourceReference, error) {
	id := input.SourceReferenceID
	var err error
	if id == "" {
		id, err = service.idFactory()
		if err != nil {
			return SourceReference{}, fmt.Errorf("allocate source reference id: %w", err)
		}
	}
	resolutionAt := input.ResolutionAt.UTC()
	if resolutionAt.IsZero() {
		resolutionAt = fallbackTime
	}
	payload, err := json.Marshal(struct {
		SchemaVersion     string          `json:"schema_version"`
		SourceReferenceID SemanticID      `json:"source_reference_id"`
		Submitted         json.RawMessage `json:"submitted"`
		Status            string          `json:"status"`
		ResolverName      string          `json:"resolver_name"`
		ResolverVersion   string          `json:"resolver_version"`
		ResolutionAt      time.Time       `json:"resolution_at"`
		Verification      string          `json:"verification_posture"`
		CanonicalLocator  *string         `json:"canonical_locator,omitempty"`
		VersionAddress    *string         `json:"version_address,omitempty"`
		ContentDigest     *string         `json:"content_digest,omitempty"`
		SourceContext     *string         `json:"source_context,omitempty"`
		GapReason         *string         `json:"gap_reason,omitempty"`
		Details           json.RawMessage `json:"details,omitempty"`
	}{SchemaVersion, id, input.Submitted, input.Status, input.ResolverName,
		input.ResolverVersion, resolutionAt, input.Verification, input.CanonicalLocator,
		input.VersionAddress, input.ContentDigest, input.SourceContext, input.GapReason,
		input.Details})
	if err != nil {
		return SourceReference{}, fmt.Errorf("encode source result: %w", err)
	}
	source := SourceReference{
		ID: id, CandidateID: candidateID, SchemaVersion: SchemaVersion, SourceKind: input.Kind,
		Status: input.Status, VerificationPosture: input.Verification,
		ResolverName: input.ResolverName, ResolverVersion: input.ResolverVersion,
		ResolutionAt: resolutionAt, CanonicalLocator: input.CanonicalLocator,
		VersionAddress: input.VersionAddress, ContentDigest: input.ContentDigest,
		SourceContext: input.SourceContext, GapReason: input.GapReason,
		Submitted: input.Submitted, Payload: payload,
	}
	if err := validateSource(source); err != nil {
		return SourceReference{}, err
	}
	return source, nil
}

func validateRegistrationBatch(batch CandidateRegistrationBatch) (ProducerIdentity, json.RawMessage, string, string, error) {
	if err := validateRequired(batch.ReplayKey, "registration replay key"); err != nil {
		return ProducerIdentity{}, nil, "", "", err
	}
	if len(batch.Candidates) == 0 || len(batch.Candidates) > MaximumRegistrationBatch {
		return ProducerIdentity{}, nil, "", "", fmt.Errorf("candidate registration batch must contain between 1 and %d candidates", MaximumRegistrationBatch)
	}
	producer := batch.Candidates[0].Producer
	if err := validateProducer(producer); err != nil {
		return ProducerIdentity{}, nil, "", "", err
	}
	for index, registration := range batch.Candidates {
		if err := validateCandidateRegistration(registration); err != nil {
			return ProducerIdentity{}, nil, "", "", fmt.Errorf("candidate %d: %w", index, err)
		}
		if !sameProducer(registration.Producer, producer) {
			return ProducerIdentity{}, nil, "", "", errors.New("candidate registration batch requires one exact producer identity")
		}
	}
	payload, err := json.Marshal(struct {
		SchemaVersion string                  `json:"schema_version"`
		Candidates    []CandidateRegistration `json:"candidates"`
	}{SchemaVersion, batch.Candidates})
	if err != nil {
		return ProducerIdentity{}, nil, "", "", fmt.Errorf("encode candidate batch: %w", err)
	}
	if len(payload) > MaximumDocumentBytes*len(batch.Candidates) {
		return ProducerIdentity{}, nil, "", "", errors.New("candidate registration batch exceeds its bounded payload")
	}
	producerJSON, _ := json.Marshal(producer)
	workflowKey := digestJSON(producerJSON)
	digest := digestJSON(payload)
	return producer, payload, workflowKey, digest, nil
}

func validateCandidateRegistration(registration CandidateRegistration) error {
	if registration.SchemaVersion == "" {
		registration.SchemaVersion = SchemaVersion
	}
	if registration.SchemaVersion != SchemaVersion {
		return errors.New("candidate registration requires schema_version 1.0")
	}
	for _, check := range []error{
		validateRequired(registration.Claim, "candidate claim"),
		validateRequired(registration.RecordKind, "candidate record kind"),
		validateRequired(registration.RecordContext, "candidate record context"),
		validateRequired(registration.Domain, "candidate domain"),
		validateRequired(registration.Visibility, "candidate visibility"),
		validateRequired(registration.AssertionPosture, "candidate assertion posture"),
		validateRequired(registration.Temporal.Interpretation, "candidate temporal interpretation"),
		validateProducer(registration.Producer),
	} {
		if check != nil {
			return check
		}
	}
	if len(registration.Sources) == 0 || len(registration.Sources) > MaximumSourcesPerObject {
		return fmt.Errorf("candidate sources must contain between 1 and %d entries", MaximumSourcesPerObject)
	}
	if registration.Temporal.ValidFrom != nil && registration.Temporal.ValidUntil != nil && registration.Temporal.ValidUntil.Before(*registration.Temporal.ValidFrom) {
		return errors.New("candidate valid_until cannot precede valid_from")
	}
	encoded, err := json.Marshal(registration)
	if err != nil {
		return fmt.Errorf("encode candidate registration: %w", err)
	}
	if len(encoded) > MaximumDocumentBytes {
		return errors.New("candidate registration exceeds its bounded payload")
	}
	if len(registration.Ambiguity) > MaximumSourcesPerObject ||
		len(registration.RelationshipHints) > MaximumSourcesPerObject ||
		len(registration.DerivedFromCandidateIDs) > MaximumSourcesPerObject {
		return errors.New("candidate repeated fields exceed their bounded collection limit")
	}
	if registration.Anchors != nil && (len(registration.Anchors.Entities) > MaximumSourcesPerObject ||
		len(registration.Anchors.Topics) > MaximumSourcesPerObject || len(registration.Anchors.Projects) > MaximumSourcesPerObject) {
		return errors.New("candidate anchors exceed their bounded collection limit")
	}
	lineage := map[SemanticID]struct{}{}
	for _, id := range registration.DerivedFromCandidateIDs {
		if err := id.Validate(); err != nil {
			return err
		}
		if _, duplicate := lineage[id]; duplicate {
			return errors.New("derived_from_candidate_ids must be unique")
		}
		lineage[id] = struct{}{}
	}
	sourceIDs := map[SemanticID]struct{}{}
	for _, source := range registration.Sources {
		if err := validateSourceRegistration(source); err != nil {
			return err
		}
		if source.SourceReferenceID != "" {
			if _, duplicate := sourceIDs[source.SourceReferenceID]; duplicate {
				return errors.New("candidate source_reference_ids must be unique")
			}
			sourceIDs[source.SourceReferenceID] = struct{}{}
		}
	}
	for role, actor := range map[string]*ActorReference{
		"source_actor":    registration.SourceActor,
		"artifact_author": registration.ArtifactAuthor,
		"approving_actor": registration.ApprovingActor,
	} {
		if err := validateActorReference(actor, sourceIDs, role); err != nil {
			return err
		}
	}
	return nil
}

func validateSourceRegistration(source SourceRegistration) error {
	for _, check := range []error{
		validateRequired(source.Kind, "source kind"), validateRequired(source.Status, "source status"),
		validateRequired(source.Verification, "source verification posture"),
		validateRequired(source.ResolverName, "source resolver name"),
		validateRequired(source.ResolverVersion, "source resolver version"),
		validateDocument(source.Submitted, "source submitted", false),
		validateDocument(source.Details, "source details", true),
	} {
		if check != nil {
			return check
		}
	}
	if source.SourceReferenceID != "" {
		if err := source.SourceReferenceID.Validate(); err != nil {
			return err
		}
	}
	if source.Status == "resolved" {
		if source.CanonicalLocator == nil || strings.TrimSpace(*source.CanonicalLocator) == "" || source.GapReason != nil {
			return errors.New("resolved source requires a canonical locator and no gap reason")
		}
	} else if source.Status == "unresolved" {
		if source.GapReason == nil || strings.TrimSpace(*source.GapReason) == "" {
			return errors.New("unresolved source requires a gap reason")
		}
	} else {
		return errors.New("source status must be resolved or unresolved")
	}
	switch source.Verification {
	case "unknown":
	case "content_verified":
		if source.Status != "resolved" || source.ContentDigest == nil || strings.TrimSpace(*source.ContentDigest) == "" {
			return errors.New("content_verified source must be resolved with a content digest")
		}
	case "locator_verified":
		if source.Status != "resolved" {
			return errors.New("locator_verified source must be resolved")
		}
	case "unverified":
		if source.Status != "unresolved" || source.CanonicalLocator != nil || source.ContentDigest != nil {
			return errors.New("unverified source must remain unresolved without canonical locator or content digest")
		}
	default:
		return errors.New("unsupported source verification posture")
	}
	return nil
}

func validateEvidenceRequest(request EvidenceRegistrationRequest) error {
	for _, check := range []error{
		validateRequired(request.Domain, "evidence registration domain"),
		validateRequired(request.Visibility, "evidence registration visibility"),
		validateRequired(request.EvidenceContext, "evidence registration context"),
		validateProducer(request.Producer), validateSourceRegistration(request.Source),
	} {
		if check != nil {
			return check
		}
	}
	if request.RegistrationID != "" {
		if err := request.RegistrationID.Validate(); err != nil {
			return err
		}
	}
	if request.ClarificationID != nil && request.ResolutionCaseID == nil {
		return errors.New("clarification evidence requires a resolution case")
	}
	for _, id := range []*SemanticID{request.ResolutionCaseID, request.ClarificationID} {
		if id != nil {
			if err := id.Validate(); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateActorReference(actor *ActorReference, allowedSources map[SemanticID]struct{}, role string) error {
	if actor == nil {
		return nil
	}
	if strings.TrimSpace(actor.ActorID) == "" && strings.TrimSpace(actor.DisplayName) == "" {
		return fmt.Errorf("%s requires an actor id or display name", role)
	}
	if actor.SourceReferenceID == nil {
		return fmt.Errorf("%s requires an explicit source reference", role)
	}
	if _, ok := allowedSources[*actor.SourceReferenceID]; !ok {
		return fmt.Errorf("%s source reference is outside the candidate sources", role)
	}
	return nil
}

func decodeCandidateReplay(replay RegistrationReplay, replayed bool) (CandidateRegistrationBatchReceipt, error) {
	var receipts []CandidateRegistrationReceipt
	if err := decodeLosslessJSON(replay.Receipts, &receipts); err != nil {
		return CandidateRegistrationBatchReceipt{}, fmt.Errorf("decode registration replay receipt: %w", err)
	}
	for index := range receipts {
		receipts[index].Replayed = replayed
	}
	return CandidateRegistrationBatchReceipt{
		SchemaVersion: SchemaVersion, RegistrationDigest: replay.RegistrationDigest,
		Replayed: replayed, Receipts: receipts,
	}, nil
}

func sameProducer(left, right ProducerIdentity) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && jsonEqual(leftJSON, rightJSON)
}

func digestJSON(payload []byte) string {
	canonical, ok := canonicalJSON(payload)
	if !ok {
		canonical = payload
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func registrationAliasWorkflowKey(workflowKey, replayKey string) string {
	payload, _ := json.Marshal(struct {
		WorkflowKey string `json:"workflow_key"`
		ReplayKey   string `json:"replay_key"`
	}{workflowKey, replayKey})
	return "alias:" + digestJSON(payload)
}

func lockTransactionKeys(ctx context.Context, store *Store, keys ...string) error {
	unique := map[string]struct{}{}
	for _, key := range keys {
		if key != "" {
			unique[key] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(unique))
	for key := range unique {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	for _, key := range ordered {
		if _, err := store.q.exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key); err != nil {
			return fmt.Errorf("lock provenance object %q: %w", key, err)
		}
	}
	return nil
}

func newSemanticID() (SemanticID, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(raw[:])
	return SemanticID(encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]), nil
}
