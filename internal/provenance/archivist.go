package provenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	ArchivistPolicyVersion          = "loom.provenance.archivist.policy.v1"
	ArchivistResultSchemaVersion    = "loom.provenance.archivist.result.v1"
	ArchivistCheckpointSchema       = "loom.provenance.archivist.checkpoint.v1"
	DefaultArchivistCandidateLimit  = 10
	MaximumArchivistCandidateLimit  = 25
	DefaultArchivistEvidenceLimit   = 8
	MaximumArchivistEvidenceLimit   = 16
	MaximumArchivistIdempotencyKey  = 256
	MaximumArchivistDecisionReason  = 1024
	MaximumArchivistOutcomeReason   = 160
	archivistRelationshipSupersedes = "supersedes"
	archivistRelationshipCorrects   = "corrects"
	archivistRelationshipRefines    = "refines"
)

type ArchivistOutcome string

const (
	ArchivistOutcomeAccept      ArchivistOutcome = "accept"
	ArchivistOutcomeConsolidate ArchivistOutcome = "consolidate"
	ArchivistOutcomeSupersede   ArchivistOutcome = "supersede"
	ArchivistOutcomeCorrect     ArchivistOutcome = "correct"
	ArchivistOutcomeRefine      ArchivistOutcome = "refine"
	ArchivistOutcomeReject      ArchivistOutcome = "reject"
	ArchivistOutcomeDefer       ArchivistOutcome = "defer"
)

var archivistAllowedRecordKinds = map[string]struct{}{
	"constraint":          {},
	"correction":          {},
	"decision":            {},
	"objective":           {},
	"outcome":             {},
	"preference":          {},
	"supporting_evidence": {},
	"unresolved_question": {},
}

// ArchivistTransport deliberately composes the existing typed search/exact-get
// surface with the foundation's sole lifecycle mutation surface. It does not
// introduce a second reconciliation service.
type ArchivistTransport interface {
	FoundationTransport
	SearchTransport
}

type ArchivistCursor struct {
	RegisteredAt time.Time  `json:"registered_at"`
	CandidateID  SemanticID `json:"candidate_id"`
}

type ArchivistDecision struct {
	CandidateID    SemanticID       `json:"candidate_id"`
	Outcome        ArchivistOutcome `json:"outcome"`
	TargetRecordID *SemanticID      `json:"target_record_id,omitempty"`
	Reason         string           `json:"reason,omitempty"`
	NextReviewAt   *time.Time       `json:"next_review_at,omitempty"`
}

type ArchivistRunRequest struct {
	CandidateLimit int                         `json:"candidate_limit"`
	EvidenceLimit  int                         `json:"evidence_limit"`
	Domain         string                      `json:"domain,omitempty"`
	Visibility     string                      `json:"visibility,omitempty"`
	Cursor         *ArchivistCursor            `json:"cursor,omitempty"`
	CandidateIDs   []SemanticID                `json:"candidate_ids,omitempty"`
	Decisions      []ArchivistDecision         `json:"decisions,omitempty"`
	IdempotencyKey string                      `json:"-"`
	Producer       ProducerIdentity            `json:"-"`
	LeaseGuard     func(context.Context) error `json:"-"`
}

type ArchivistCandidateOutcome struct {
	CandidateID SemanticID       `json:"candidate_id"`
	Outcome     ArchivistOutcome `json:"outcome"`
	ReasonCode  string           `json:"reason_code,omitempty"`
	RecordID    *SemanticID      `json:"record_id,omitempty"`
	CaseID      *SemanticID      `json:"case_id,omitempty"`
	CaseCreated bool             `json:"case_created,omitempty"`
	Replayed    bool             `json:"replayed"`
	exactReads  int
}

type ArchivistRunResult struct {
	SchemaVersion string                      `json:"schema_version"`
	PolicyVersion string                      `json:"policy_version"`
	Selected      int                         `json:"selected"`
	Examined      int                         `json:"examined"`
	Accepted      int                         `json:"accepted"`
	Consolidated  int                         `json:"consolidated"`
	Related       int                         `json:"related"`
	Rejected      int                         `json:"rejected"`
	Deferred      int                         `json:"deferred"`
	Stale         int                         `json:"stale"`
	CasesCreated  int                         `json:"cases_created"`
	CasesUpdated  int                         `json:"cases_updated"`
	ExactReads    int                         `json:"exact_reads"`
	Replayed      int                         `json:"replayed"`
	CycleComplete bool                        `json:"cycle_complete"`
	NextCursor    *ArchivistCursor            `json:"next_cursor,omitempty"`
	Outcomes      []ArchivistCandidateOutcome `json:"outcomes"`
}

type Archivist struct {
	transport    ArchivistTransport
	beforeCommit func(context.Context, SemanticID) error
	afterCommit  func(context.Context, SemanticID) error
}

func NewArchivist(transport ArchivistTransport) (*Archivist, error) {
	if transport == nil {
		return nil, errors.New("provenance Archivist transport is required")
	}
	return &Archivist{transport: transport}, nil
}

type archivistCandidateEnvelope struct {
	SchemaVersion string                `json:"schema_version"`
	CandidateID   SemanticID            `json:"candidate_id"`
	Registration  CandidateRegistration `json:"registration"`
	SourceResults []SourceReference     `json:"source_results"`
	RegisteredAt  time.Time             `json:"registered_at"`
	State         string                `json:"state"`
}

type archivistRelatedEvidence struct {
	truncated      bool
	accepted       []RecordLifecycleProjection
	pending        []CandidateLifecycleProjection
	cases          []ResolutionCaseLifecycleProjection
	exactReadCount int
}

type archivistAssessment struct {
	eligible   bool
	reasonCode string
	reason     string
}

func (archivist *Archivist) Run(ctx context.Context, request ArchivistRunRequest) (ArchivistRunResult, error) {
	request, decisions, err := normalizeArchivistRunRequest(request)
	if err != nil {
		return ArchivistRunResult{}, err
	}
	if readiness := archivist.transport.Readiness(); readiness.State != ReadinessReady {
		return ArchivistRunResult{}, fmt.Errorf("provenance Archivist runtime is not ready: %s", readiness.Code)
	}
	if err := archivistFence(ctx, request.LeaseGuard); err != nil {
		return ArchivistRunResult{}, err
	}

	result := ArchivistRunResult{
		SchemaVersion: ArchivistResultSchemaVersion,
		PolicyVersion: ArchivistPolicyVersion,
		Outcomes:      make([]ArchivistCandidateOutcome, 0, request.CandidateLimit),
	}
	ids, cursor, cycleComplete, err := archivist.selectCandidateIDs(ctx, request, decisions)
	if err != nil {
		return ArchivistRunResult{}, err
	}
	result.Selected, result.NextCursor, result.CycleComplete = len(ids), cursor, cycleComplete

	for _, candidateID := range ids {
		if err := archivistFence(ctx, request.LeaseGuard); err != nil {
			return ArchivistRunResult{}, err
		}
		projection, err := archivist.transport.GetCandidate(ctx, candidateID, MaximumSourcesPerObject)
		result.ExactReads++
		if errors.Is(err, ErrFoundationNotFound) {
			result.Stale++
			result.Outcomes = append(result.Outcomes, ArchivistCandidateOutcome{CandidateID: candidateID, Outcome: ArchivistOutcomeDefer, ReasonCode: "stale_candidate"})
			continue
		}
		if err != nil {
			return ArchivistRunResult{}, fmt.Errorf("load candidate %s exact evidence: %w", candidateID, err)
		}
		if projection.EffectiveState != "pending" {
			result.Stale++
			result.Outcomes = append(result.Outcomes, ArchivistCandidateOutcome{CandidateID: candidateID, Outcome: ArchivistOutcomeDefer, ReasonCode: "stale_candidate"})
			continue
		}
		result.Examined++

		envelope, err := decodeArchivistCandidateEnvelope(projection)
		if err != nil {
			outcome, applyErr := archivist.deferCandidate(ctx, request, projection, archivistCandidateEnvelope{}, "partial_evidence", "Candidate evidence could not be decoded exactly.", nil)
			if applyErr != nil {
				return ArchivistRunResult{}, applyErr
			}
			accumulateArchivistOutcome(&result, outcome)
			continue
		}

		related, err := archivist.relatedEvidence(ctx, projection, envelope, request.EvidenceLimit)
		if err != nil {
			return ArchivistRunResult{}, fmt.Errorf("retrieve related evidence for candidate %s: %w", candidateID, err)
		}
		result.ExactReads += related.exactReadCount
		decision, explicit := decisions[candidateID]
		outcome, err := archivist.reconcileCandidate(ctx, request, projection, envelope, related, decision, explicit)
		if err != nil {
			return ArchivistRunResult{}, fmt.Errorf("reconcile candidate %s: %w", candidateID, err)
		}
		accumulateArchivistOutcome(&result, outcome)
	}
	return result, nil
}

func normalizeArchivistRunRequest(request ArchivistRunRequest) (ArchivistRunRequest, map[SemanticID]ArchivistDecision, error) {
	if request.CandidateLimit == 0 {
		request.CandidateLimit = DefaultArchivistCandidateLimit
	}
	if request.CandidateLimit < 1 || request.CandidateLimit > MaximumArchivistCandidateLimit {
		return ArchivistRunRequest{}, nil, fmt.Errorf("Archivist candidate limit must be between 1 and %d", MaximumArchivistCandidateLimit)
	}
	if request.EvidenceLimit == 0 {
		request.EvidenceLimit = DefaultArchivistEvidenceLimit
	}
	if request.EvidenceLimit < 1 || request.EvidenceLimit > MaximumArchivistEvidenceLimit {
		return ArchivistRunRequest{}, nil, fmt.Errorf("Archivist evidence limit must be between 1 and %d", MaximumArchivistEvidenceLimit)
	}
	request.Domain, request.Visibility = strings.TrimSpace(request.Domain), strings.TrimSpace(request.Visibility)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if request.IdempotencyKey == "" || len(request.IdempotencyKey) > MaximumArchivistIdempotencyKey {
		return ArchivistRunRequest{}, nil, fmt.Errorf("Archivist idempotency key must contain between 1 and %d bytes", MaximumArchivistIdempotencyKey)
	}
	if err := validateProducer(request.Producer); err != nil {
		return ArchivistRunRequest{}, nil, fmt.Errorf("Archivist producer: %w", err)
	}
	if request.Cursor != nil {
		request.Cursor.RegisteredAt = request.Cursor.RegisteredAt.UTC()
		if request.Cursor.RegisteredAt.IsZero() || request.Cursor.CandidateID.Validate() != nil {
			return ArchivistRunRequest{}, nil, errors.New("Archivist cursor requires a timestamp and canonical candidate id")
		}
	}
	ids := map[SemanticID]struct{}{}
	for _, id := range request.CandidateIDs {
		if err := id.Validate(); err != nil {
			return ArchivistRunRequest{}, nil, err
		}
		if _, duplicate := ids[id]; duplicate {
			return ArchivistRunRequest{}, nil, fmt.Errorf("Archivist candidate id %s is duplicated", id)
		}
		ids[id] = struct{}{}
	}
	decisions := make(map[SemanticID]ArchivistDecision, len(request.Decisions))
	for _, decision := range request.Decisions {
		if err := decision.CandidateID.Validate(); err != nil {
			return ArchivistRunRequest{}, nil, err
		}
		if _, duplicate := decisions[decision.CandidateID]; duplicate {
			return ArchivistRunRequest{}, nil, fmt.Errorf("candidate %s has multiple Archivist decisions", decision.CandidateID)
		}
		decision.Reason = strings.TrimSpace(decision.Reason)
		if len(decision.Reason) > MaximumArchivistDecisionReason {
			return ArchivistRunRequest{}, nil, fmt.Errorf("candidate %s decision reason exceeds %d bytes", decision.CandidateID, MaximumArchivistDecisionReason)
		}
		if !validArchivistOutcome(decision.Outcome) {
			return ArchivistRunRequest{}, nil, fmt.Errorf("candidate %s has unsupported Archivist outcome %q", decision.CandidateID, decision.Outcome)
		}
		if requiresArchivistTarget(decision.Outcome) {
			if decision.TargetRecordID == nil || decision.TargetRecordID.Validate() != nil {
				return ArchivistRunRequest{}, nil, fmt.Errorf("candidate %s outcome %s requires a canonical target_record_id", decision.CandidateID, decision.Outcome)
			}
		} else if decision.TargetRecordID != nil {
			return ArchivistRunRequest{}, nil, fmt.Errorf("candidate %s outcome %s does not accept target_record_id", decision.CandidateID, decision.Outcome)
		}
		if decision.Outcome == ArchivistOutcomeReject && decision.Reason == "" {
			return ArchivistRunRequest{}, nil, fmt.Errorf("candidate %s rejection requires a reason", decision.CandidateID)
		}
		if decision.NextReviewAt != nil {
			next := decision.NextReviewAt.UTC()
			decision.NextReviewAt = &next
			if decision.Outcome != ArchivistOutcomeDefer {
				return ArchivistRunRequest{}, nil, fmt.Errorf("candidate %s next_review_at is only valid for defer", decision.CandidateID)
			}
		}
		decisions[decision.CandidateID] = decision
		ids[decision.CandidateID] = struct{}{}
	}
	if len(ids) > request.CandidateLimit {
		return ArchivistRunRequest{}, nil, fmt.Errorf("explicit Archivist selection exceeds candidate limit %d", request.CandidateLimit)
	}
	request.CandidateIDs = request.CandidateIDs[:0]
	for id := range ids {
		request.CandidateIDs = append(request.CandidateIDs, id)
	}
	sort.Slice(request.CandidateIDs, func(i, j int) bool { return request.CandidateIDs[i] < request.CandidateIDs[j] })
	return request, decisions, nil
}

func validArchivistOutcome(outcome ArchivistOutcome) bool {
	switch outcome {
	case ArchivistOutcomeAccept, ArchivistOutcomeConsolidate, ArchivistOutcomeSupersede,
		ArchivistOutcomeCorrect, ArchivistOutcomeRefine, ArchivistOutcomeReject, ArchivistOutcomeDefer:
		return true
	default:
		return false
	}
}

func requiresArchivistTarget(outcome ArchivistOutcome) bool {
	return outcome == ArchivistOutcomeConsolidate || outcome == ArchivistOutcomeSupersede || outcome == ArchivistOutcomeCorrect || outcome == ArchivistOutcomeRefine
}

func (archivist *Archivist) selectCandidateIDs(ctx context.Context, request ArchivistRunRequest, decisions map[SemanticID]ArchivistDecision) ([]SemanticID, *ArchivistCursor, bool, error) {
	if len(request.CandidateIDs) > 0 || len(decisions) > 0 {
		return append([]SemanticID(nil), request.CandidateIDs...), nil, true, nil
	}
	pageRequest := PageRequest{Limit: request.CandidateLimit, Domain: request.Domain, Visibility: request.Visibility}
	if request.Cursor != nil {
		pageRequest.AfterTime, pageRequest.AfterID = &request.Cursor.RegisteredAt, &request.Cursor.CandidateID
	}
	page, err := archivist.transport.ListCandidates(ctx, pageRequest)
	if err != nil {
		return nil, nil, false, err
	}
	ids := make([]SemanticID, 0, len(page.Items))
	for _, item := range page.Items {
		ids = append(ids, item.ID)
	}
	if !page.Truncated || page.NextTime == nil || page.NextID == nil {
		return ids, nil, true, nil
	}
	return ids, &ArchivistCursor{RegisteredAt: page.NextTime.UTC(), CandidateID: *page.NextID}, false, nil
}

func decodeArchivistCandidateEnvelope(projection CandidateLifecycleProjection) (archivistCandidateEnvelope, error) {
	var envelope archivistCandidateEnvelope
	if err := decodeLosslessJSON(projection.Candidate.Payload, &envelope); err != nil {
		return archivistCandidateEnvelope{}, err
	}
	if envelope.SchemaVersion != SchemaVersion || envelope.CandidateID != projection.Candidate.ID || envelope.State != "pending" ||
		!envelope.RegisteredAt.UTC().Equal(projection.Candidate.RegisteredAt.UTC()) {
		return archivistCandidateEnvelope{}, errors.New("candidate envelope identity differs from exact projection")
	}
	var submitted CandidateRegistration
	if err := decodeLosslessJSON(projection.Candidate.Submitted, &submitted); err != nil {
		return archivistCandidateEnvelope{}, err
	}
	left, _ := json.Marshal(envelope.Registration)
	right, _ := json.Marshal(submitted)
	if !jsonEqual(left, right) {
		return archivistCandidateEnvelope{}, errors.New("candidate submitted registration differs from durable envelope")
	}
	if envelope.Registration.Claim != projection.Candidate.Claim || envelope.Registration.RecordKind != projection.Candidate.RecordKind ||
		envelope.Registration.RecordContext != projection.Candidate.RecordContext || envelope.Registration.Domain != projection.Candidate.Domain ||
		envelope.Registration.Visibility != projection.Candidate.Visibility || envelope.Registration.AssertionPosture != projection.Candidate.AssertionPosture {
		return archivistCandidateEnvelope{}, errors.New("candidate registration differs from indexed projection")
	}
	return envelope, nil
}

func (archivist *Archivist) relatedEvidence(ctx context.Context, projection CandidateLifecycleProjection, envelope archivistCandidateEnvelope, limit int) (archivistRelatedEvidence, error) {
	query := boundedArchivistSearchQuery(projection.Candidate.Claim)
	if query == "" {
		return archivistRelatedEvidence{}, nil
	}
	request := SearchRequest{
		Query: query, IncludePending: true, Limit: limit,
		Collections: []SearchCollection{SearchCollectionAcceptedRecords, SearchCollectionPendingCandidates, SearchCollectionUnresolvedCases},
	}
	if envelope.Registration.Anchors != nil {
		if len(envelope.Registration.Anchors.Projects) == 1 {
			request.Project = envelope.Registration.Anchors.Projects[0]
		}
		repositories := anchorRepositories(envelope.Registration.Anchors)
		if len(repositories) == 1 {
			request.Repository = repositories[0]
		}
	}
	response, err := archivist.transport.Search(ctx, request)
	if err != nil {
		return archivistRelatedEvidence{}, err
	}
	result := archivistRelatedEvidence{truncated: response.Truncated}
	for _, item := range response.AcceptedRecords.Items {
		exact, err := archivist.transport.GetRecord(ctx, item.RecordID, MaximumSourcesPerObject)
		result.exactReadCount++
		if err != nil {
			return archivistRelatedEvidence{}, err
		}
		if sameArchivistScope(projection.Candidate.Domain, projection.Candidate.Visibility, exact.Record.Domain, exact.Record.Visibility) &&
			archivistObjectsRelated(envelope.Registration, exact.Record.Claim, exact.Record.RecordKind, exact.Record.Anchors) {
			result.accepted = append(result.accepted, exact)
		}
	}
	for _, item := range response.PendingCandidates.Items {
		if item.CandidateID == projection.Candidate.ID {
			continue
		}
		exact, err := archivist.transport.GetCandidate(ctx, item.CandidateID, MaximumSourcesPerObject)
		result.exactReadCount++
		if err != nil {
			return archivistRelatedEvidence{}, err
		}
		if exact.EffectiveState != "pending" || !sameArchivistScope(projection.Candidate.Domain, projection.Candidate.Visibility, exact.Candidate.Domain, exact.Candidate.Visibility) {
			continue
		}
		var registration CandidateRegistration
		if err := decodeLosslessJSON(exact.Candidate.Submitted, &registration); err != nil {
			return archivistRelatedEvidence{}, err
		}
		if archivistObjectsRelated(envelope.Registration, exact.Candidate.Claim, exact.Candidate.RecordKind, registration.Anchors) {
			result.pending = append(result.pending, exact)
		}
	}
	for _, item := range response.UnresolvedCases.Items {
		exact, err := archivist.transport.GetResolutionCase(ctx, item.CaseID, MaximumSourcesPerObject)
		result.exactReadCount++
		if err != nil {
			return archivistRelatedEvidence{}, err
		}
		if sameArchivistScope(projection.Candidate.Domain, projection.Candidate.Visibility, exact.Case.Domain, exact.Case.Visibility) {
			result.cases = append(result.cases, exact)
		}
	}
	return result, nil
}

func boundedArchivistSearchQuery(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= MaximumSearchQueryBytes {
		return value
	}
	cut := MaximumSearchQueryBytes
	for cut > 0 && !utf8.ValidString(value[:cut]) {
		cut--
	}
	return strings.TrimSpace(value[:cut])
}

func sameArchivistScope(leftDomain, leftVisibility, rightDomain, rightVisibility string) bool {
	return leftDomain == rightDomain && leftVisibility == rightVisibility
}

func archivistObjectsRelated(candidate CandidateRegistration, otherClaim, otherKind string, otherAnchors *StructuralAnchors) bool {
	if canonicalArchivistText(candidate.Claim) == canonicalArchivistText(otherClaim) {
		return true
	}
	if strings.TrimSpace(candidate.RecordKind) != strings.TrimSpace(otherKind) || candidate.Anchors == nil || otherAnchors == nil {
		return false
	}
	return exactSingleArchivistEntity(candidate.Anchors) != "" && exactSingleArchivistEntity(candidate.Anchors) == exactSingleArchivistEntity(otherAnchors)
}

func canonicalArchivistText(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(value)), " ")
}

func exactSingleArchivistEntity(anchors *StructuralAnchors) string {
	if anchors == nil || len(anchors.Entities) != 1 {
		return ""
	}
	return strings.TrimSpace(anchors.Entities[0])
}

func (archivist *Archivist) reconcileCandidate(ctx context.Context, request ArchivistRunRequest, projection CandidateLifecycleProjection, envelope archivistCandidateEnvelope, related archivistRelatedEvidence, decision ArchivistDecision, explicit bool) (ArchivistCandidateOutcome, error) {
	assessment := assessArchivistAutoAcceptance(projection, envelope, related)
	if !explicit {
		if assessment.eligible {
			return archivist.acceptCandidate(ctx, request, projection, envelope, ArchivistOutcomeAccept, nil)
		}
		return archivist.deferCandidate(ctx, request, projection, envelope, assessment.reasonCode, assessment.reason, nil)
	}

	switch decision.Outcome {
	case ArchivistOutcomeAccept:
		if !assessment.eligible {
			return archivist.deferCandidate(ctx, request, projection, envelope, assessment.reasonCode, assessment.reason, decision.NextReviewAt)
		}
		return archivist.acceptCandidate(ctx, request, projection, envelope, decision.Outcome, nil)
	case ArchivistOutcomeConsolidate:
		if source := assessArchivistSourcePolicy(projection, envelope); !source.eligible {
			return archivist.deferCandidate(ctx, request, projection, envelope, source.reasonCode, source.reason, decision.NextReviewAt)
		}
		target, err := archivist.transport.GetRecord(ctx, *decision.TargetRecordID, MaximumSourcesPerObject)
		if err != nil {
			outcome, deferErr := archivist.deferCandidate(ctx, request, projection, envelope, "incompatible_relationship", "Consolidation target is unavailable or incompatible.", decision.NextReviewAt)
			return addArchivistExactRead(outcome, deferErr)
		}
		if related.hasConflictOtherThan(target.Record.ID) {
			outcome, deferErr := archivist.deferCandidate(ctx, request, projection, envelope, "material_conflict", "Related evidence beyond the consolidation target requires review.", decision.NextReviewAt)
			return addArchivistExactRead(outcome, deferErr)
		}
		if !compatibleArchivistConsolidation(envelope.Registration, target.Record) {
			outcome, deferErr := archivist.deferCandidate(ctx, request, projection, envelope, "incompatible_relationship", "Consolidation requires an exact same-scope claim and entity match.", decision.NextReviewAt)
			return addArchivistExactRead(outcome, deferErr)
		}
		outcome, applyErr := archivist.consolidateCandidate(ctx, request, projection, envelope, target.Record.ID, decision.Reason)
		return addArchivistExactRead(outcome, applyErr)
	case ArchivistOutcomeSupersede, ArchivistOutcomeCorrect, ArchivistOutcomeRefine:
		if source := assessArchivistSourcePolicy(projection, envelope); !source.eligible {
			return archivist.deferCandidate(ctx, request, projection, envelope, source.reasonCode, source.reason, decision.NextReviewAt)
		}
		target, err := archivist.transport.GetRecord(ctx, *decision.TargetRecordID, MaximumSourcesPerObject)
		if err != nil || !compatibleArchivistRelationship(envelope.Registration, target.Record) {
			outcome, deferErr := archivist.deferCandidate(ctx, request, projection, envelope, "incompatible_relationship", "Relationship target is unavailable or incompatible.", decision.NextReviewAt)
			return addArchivistExactRead(outcome, deferErr)
		}
		if related.hasConflictOtherThan(target.Record.ID) {
			outcome, deferErr := archivist.deferCandidate(ctx, request, projection, envelope, "material_conflict", "Related evidence beyond the relationship target requires review.", decision.NextReviewAt)
			return addArchivistExactRead(outcome, deferErr)
		}
		outcome, applyErr := archivist.acceptCandidate(ctx, request, projection, envelope, decision.Outcome, &target.Record)
		return addArchivistExactRead(outcome, applyErr)
	case ArchivistOutcomeReject:
		return archivist.rejectCandidate(ctx, request, projection, envelope, decision.Reason)
	case ArchivistOutcomeDefer:
		reason := decision.Reason
		if reason == "" {
			reason = "Manual review deferred this candidate."
		}
		return archivist.deferCandidate(ctx, request, projection, envelope, "manual_defer", reason, decision.NextReviewAt)
	default:
		return ArchivistCandidateOutcome{}, fmt.Errorf("unsupported outcome %q", decision.Outcome)
	}
}

func (related archivistRelatedEvidence) hasConflictOtherThan(targetID SemanticID) bool {
	if related.truncated || len(related.pending) > 0 || len(related.cases) > 0 {
		return true
	}
	for _, record := range related.accepted {
		if record.Record.ID != targetID {
			return true
		}
	}
	return false
}

func assessArchivistAutoAcceptance(projection CandidateLifecycleProjection, envelope archivistCandidateEnvelope, related archivistRelatedEvidence) archivistAssessment {
	if assessment := assessArchivistSourcePolicy(projection, envelope); !assessment.eligible {
		return assessment
	}
	if related.truncated {
		return archivistAssessment{reasonCode: "related_evidence_truncated", reason: "Bounded related evidence is incomplete."}
	}
	if len(related.accepted) > 0 || len(related.pending) > 0 || len(related.cases) > 0 {
		return archivistAssessment{reasonCode: "material_conflict", reason: "Related accepted, pending, or unresolved evidence requires review."}
	}
	return archivistAssessment{eligible: true}
}

func assessArchivistSourcePolicy(projection CandidateLifecycleProjection, envelope archivistCandidateEnvelope) archivistAssessment {
	registration := envelope.Registration
	if _, allowed := archivistAllowedRecordKinds[strings.TrimSpace(registration.RecordKind)]; !allowed {
		return archivistAssessment{reasonCode: "record_kind_not_allowed", reason: "Record kind is outside the frozen automatic policy."}
	}
	if strings.TrimSpace(registration.AssertionPosture) != "explicit_user_statement" || len(registration.DerivedFromCandidateIDs) > 0 {
		return archivistAssessment{reasonCode: "agent_interpretation", reason: "Candidate is not a direct explicit user statement."}
	}
	if len(registration.Ambiguity) > 0 || strings.TrimSpace(registration.Temporal.Uncertainty) != "" || exactSingleArchivistEntity(registration.Anchors) == "" || len(registration.RelationshipHints) > 0 {
		return archivistAssessment{reasonCode: "ambiguous_candidate", reason: "Entity, time, intended claim, or relationship is ambiguous."}
	}
	if registration.SourceActor == nil || strings.ToLower(strings.TrimSpace(registration.SourceActor.ActorKind)) != "user" ||
		strings.TrimSpace(registration.SourceActor.ActorID) == "" || registration.SourceActor.SourceReferenceID == nil {
		return archivistAssessment{reasonCode: "not_explicit_user_statement", reason: "Candidate lacks an exact user actor bound to source evidence."}
	}
	if len(envelope.SourceResults) == 0 || len(envelope.SourceResults) != len(projection.SourceReferenceIDs) {
		return archivistAssessment{reasonCode: "partial_evidence", reason: "Candidate source evidence is incomplete."}
	}
	sourceIDs := map[SemanticID]SourceReference{}
	for _, source := range envelope.SourceResults {
		if source.Status != "resolved" || source.VerificationPosture != "content_verified" || source.GapReason != nil {
			return archivistAssessment{reasonCode: "source_gap", reason: "Candidate has unresolved or non-content-verified source evidence."}
		}
		sourceIDs[source.ID] = source
	}
	for _, id := range projection.SourceReferenceIDs {
		if _, found := sourceIDs[id]; !found {
			return archivistAssessment{reasonCode: "partial_evidence", reason: "Candidate exact source identifiers are incomplete."}
		}
	}
	actorSource, found := sourceIDs[*registration.SourceActor.SourceReferenceID]
	if !found || actorSource.SourceKind != "codex_current_thread" || actorSource.ContentDigest == nil || strings.TrimSpace(*actorSource.ContentDigest) == "" ||
		actorSource.SourceContext == nil || strings.TrimSpace(*actorSource.SourceContext) == "" || !sourceContainsExplicitExcerpt(actorSource.Submitted) {
		return archivistAssessment{reasonCode: "not_explicit_user_statement", reason: "User source is not a content-verified current-task statement."}
	}
	return archivistAssessment{eligible: true}
}

func sourceContainsExplicitExcerpt(raw json.RawMessage) bool {
	var document struct {
		Evidence struct {
			Excerpt string `json:"excerpt"`
		} `json:"evidence"`
	}
	return json.Unmarshal(raw, &document) == nil && strings.TrimSpace(document.Evidence.Excerpt) != ""
}

func compatibleArchivistConsolidation(candidate CandidateRegistration, target Record) bool {
	return sameArchivistScope(candidate.Domain, candidate.Visibility, target.Domain, target.Visibility) &&
		strings.TrimSpace(candidate.RecordKind) == strings.TrimSpace(target.RecordKind) &&
		canonicalArchivistText(candidate.Claim) == canonicalArchivistText(target.Claim) &&
		exactSingleArchivistEntity(candidate.Anchors) != "" && exactSingleArchivistEntity(candidate.Anchors) == exactSingleArchivistEntity(target.Anchors)
}

func compatibleArchivistRelationship(candidate CandidateRegistration, target Record) bool {
	return sameArchivistScope(candidate.Domain, candidate.Visibility, target.Domain, target.Visibility) &&
		strings.TrimSpace(candidate.RecordKind) == strings.TrimSpace(target.RecordKind) &&
		exactSingleArchivistEntity(candidate.Anchors) != "" && exactSingleArchivistEntity(candidate.Anchors) == exactSingleArchivistEntity(target.Anchors)
}

func (archivist *Archivist) acceptCandidate(ctx context.Context, request ArchivistRunRequest, projection CandidateLifecycleProjection, envelope archivistCandidateEnvelope, outcome ArchivistOutcome, target *Record) (ArchivistCandidateOutcome, error) {
	registration := envelope.Registration
	recordID := deterministicArchivistID("record", string(projection.Candidate.ID), ArchivistPolicyVersion)
	metadata := archivistOperationMetadata(request, projection, outcome, targetID(target), "")
	producerHistory := []ProducerIdentity{registration.Producer}
	if !sameProducer(registration.Producer, request.Producer) {
		producerHistory = append(producerHistory, request.Producer)
	}
	accepted := AcceptedRecord{
		Record: Record{
			ID: recordID, SchemaVersion: SchemaVersion, Claim: registration.Claim,
			RecordKind: registration.RecordKind, RecordContext: registration.RecordContext,
			Ambiguity: append([]string(nil), registration.Ambiguity...), Domain: registration.Domain,
			Visibility: registration.Visibility, AssertionPosture: registration.AssertionPosture,
			Temporal: registration.Temporal, Anchors: cloneArchivistAnchors(registration.Anchors),
			CreatedAt: projection.Candidate.RegisteredAt.UTC(), Payload: json.RawMessage(`{}`),
		},
		SourceReferenceIDs: append([]SemanticID(nil), projection.SourceReferenceIDs...),
		ProducerHistory:    producerHistory, SourceActor: registration.SourceActor,
		ArtifactAuthor: registration.ArtifactAuthor, ApprovingActor: registration.ApprovingActor,
	}
	operations := []LifecycleOperation{AcceptCandidateOperation{OperationMetadata: metadata, CandidateID: projection.Candidate.ID, Accepted: accepted}}
	var relationshipID *SemanticID
	if target != nil {
		relationType := archivistRelationshipType(outcome)
		id := deterministicArchivistID("relationship", string(projection.Candidate.ID), string(outcome), string(target.ID), ArchivistPolicyVersion)
		relationshipID = &id
		relationMetadata := archivistOperationMetadata(request, projection, outcome, target.ID, "relationship")
		operations = append(operations, AddRelationshipOperation{OperationMetadata: relationMetadata, Relationship: Relationship{
			ID: id, SchemaVersion: SchemaVersion, RelationshipType: relationType,
			FromRecordID: recordID, ToRecordID: target.ID,
			EvidenceSourceIDs: append([]SemanticID(nil), projection.SourceReferenceIDs...),
			CreatedAt:         projection.Candidate.RegisteredAt.UTC(), CreatedBy: request.Producer,
			Payload: json.RawMessage(`{}`),
		}})
	}
	receipt, err := archivist.apply(ctx, request, projection.Candidate.ID, registration, operations)
	if err != nil {
		return ArchivistCandidateOutcome{}, err
	}
	result := ArchivistCandidateOutcome{CandidateID: projection.Candidate.ID, Outcome: outcome, RecordID: &recordID, Replayed: receipt.Replayed}
	_ = relationshipID // relationship identity remains durable in the foundation receipt, not worker output.
	return result, nil
}

func (archivist *Archivist) consolidateCandidate(ctx context.Context, request ArchivistRunRequest, projection CandidateLifecycleProjection, envelope archivistCandidateEnvelope, targetID SemanticID, rationale string) (ArchivistCandidateOutcome, error) {
	if strings.TrimSpace(rationale) == "" {
		rationale = "Explicit bounded review found an exact compatible record."
	}
	metadata := archivistOperationMetadata(request, projection, ArchivistOutcomeConsolidate, targetID, rationale)
	receipt, err := archivist.apply(ctx, request, projection.Candidate.ID, envelope.Registration, []LifecycleOperation{ConsolidateCandidateOperation{
		OperationMetadata: metadata, CandidateID: projection.Candidate.ID, RecordID: targetID, Rationale: rationale,
	}})
	if err != nil {
		return ArchivistCandidateOutcome{}, err
	}
	return ArchivistCandidateOutcome{CandidateID: projection.Candidate.ID, Outcome: ArchivistOutcomeConsolidate, RecordID: &targetID, Replayed: receipt.Replayed}, nil
}

func (archivist *Archivist) rejectCandidate(ctx context.Context, request ArchivistRunRequest, projection CandidateLifecycleProjection, envelope archivistCandidateEnvelope, reason string) (ArchivistCandidateOutcome, error) {
	metadata := archivistOperationMetadata(request, projection, ArchivistOutcomeReject, "", reason)
	receipt, err := archivist.apply(ctx, request, projection.Candidate.ID, envelope.Registration, []LifecycleOperation{RejectCandidateOperation{
		OperationMetadata: metadata, CandidateID: projection.Candidate.ID, Reason: reason,
	}})
	if err != nil {
		return ArchivistCandidateOutcome{}, err
	}
	return ArchivistCandidateOutcome{CandidateID: projection.Candidate.ID, Outcome: ArchivistOutcomeReject, Replayed: receipt.Replayed}, nil
}

func (archivist *Archivist) deferCandidate(ctx context.Context, request ArchivistRunRequest, projection CandidateLifecycleProjection, envelope archivistCandidateEnvelope, reasonCode, reason string, nextReviewAt *time.Time) (ArchivistCandidateOutcome, error) {
	if reasonCode == "" {
		reasonCode = "review_required"
	}
	if reason == "" {
		reason = "Bounded Archivist policy requires review."
	}
	registration := envelope.Registration
	if registration.Domain == "" {
		var err error
		registration, err = registrationFromProjection(projection)
		if err != nil {
			return ArchivistCandidateOutcome{}, err
		}
	}
	deferMetadata := archivistOperationMetadata(request, projection, ArchivistOutcomeDefer, "", reasonCode+":"+reason)
	caseID := deterministicArchivistID("case", string(projection.Candidate.ID), reasonCode, ArchivistPolicyVersion)
	caseOperationID := deterministicArchivistID("operation", request.IdempotencyKey, string(projection.Candidate.ID), "case", reasonCode, ArchivistPolicyVersion)
	caseMetadata := OperationMetadata{
		SchemaVersion: SchemaVersion, OperationID: caseOperationID,
		OccurredAt: projection.Candidate.RegisteredAt.UTC(), Producer: request.Producer,
		EvidenceSourceIDs: append([]SemanticID(nil), projection.SourceReferenceIDs...),
	}
	eventID := deterministicArchivistID("case-event", request.IdempotencyKey, string(projection.Candidate.ID), reasonCode, ArchivistPolicyVersion)
	event := CaseEvent{
		ID: eventID, CaseID: caseID, SchemaVersion: SchemaVersion, EventType: "review_deferred",
		OccurredAt: projection.Candidate.RegisteredAt.UTC(), Producer: request.Producer,
		Summary: boundedArchivistReason(reason), EvidenceSourceIDs: append([]SemanticID(nil), projection.SourceReferenceIDs...),
		Payload: json.RawMessage(`{}`),
	}
	operations := []LifecycleOperation{DeferCandidateOperation{
		OperationMetadata: deferMetadata, CandidateID: projection.Candidate.ID,
		ReasonCode: reasonCode, Reason: reason, NextReviewAt: nextReviewAt,
	}}
	created := false
	existing, err := archivist.transport.GetResolutionCase(ctx, caseID, MaximumSourcesPerObject)
	if errors.Is(err, ErrFoundationNotFound) {
		created = true
		operations = append(operations, CreateResolutionCaseOperation{OperationMetadata: caseMetadata, Case: ResolutionCase{
			ID: caseID, SchemaVersion: SchemaVersion, Issue: boundedArchivistReason(reason),
			Domain: registration.Domain, Visibility: registration.Visibility, InitialStatus: "open",
			CreatedAt: projection.Candidate.RegisteredAt.UTC(), Payload: json.RawMessage(`{}`),
		}, Members: []CaseMemberReference{{MemberType: "candidate", MemberID: projection.Candidate.ID, Role: "subject"}}, Events: []CaseEvent{event}})
	} else if err != nil {
		return ArchivistCandidateOutcome{}, err
	} else if existing.Case.CreatedByOperationID != nil && *existing.Case.CreatedByOperationID == caseOperationID {
		// Re-submit the original atomic batch so crash-after-commit retries use
		// the foundation's exact operation replay path.
		created = true
		operations = append(operations, CreateResolutionCaseOperation{OperationMetadata: caseMetadata, Case: ResolutionCase{
			ID: caseID, SchemaVersion: SchemaVersion, Issue: boundedArchivistReason(reason),
			Domain: registration.Domain, Visibility: registration.Visibility, InitialStatus: "open",
			CreatedAt: projection.Candidate.RegisteredAt.UTC(), Payload: json.RawMessage(`{}`),
		}, Members: []CaseMemberReference{{MemberType: "candidate", MemberID: projection.Candidate.ID, Role: "subject"}}, Events: []CaseEvent{event}})
	} else {
		operations = append(operations, UpdateResolutionCaseOperation{OperationMetadata: caseMetadata, CaseID: caseID, Event: event})
	}
	receipt, err := archivist.apply(ctx, request, projection.Candidate.ID, registration, operations)
	if err != nil {
		return ArchivistCandidateOutcome{}, err
	}
	return ArchivistCandidateOutcome{CandidateID: projection.Candidate.ID, Outcome: ArchivistOutcomeDefer, ReasonCode: reasonCode, CaseID: &caseID, CaseCreated: created, Replayed: receipt.Replayed || allArchivistReceiptsReplayed(receipt), exactReads: 1}, nil
}

func addArchivistExactRead(outcome ArchivistCandidateOutcome, err error) (ArchivistCandidateOutcome, error) {
	outcome.exactReads++
	return outcome, err
}

func registrationFromProjection(projection CandidateLifecycleProjection) (CandidateRegistration, error) {
	var registration CandidateRegistration
	if err := decodeLosslessJSON(projection.Candidate.Submitted, &registration); err != nil {
		return CandidateRegistration{}, err
	}
	return registration, nil
}

func (archivist *Archivist) apply(ctx context.Context, request ArchivistRunRequest, candidateID SemanticID, registration CandidateRegistration, operations []LifecycleOperation) (ManualOperationBatchReceipt, error) {
	if err := archivistFence(ctx, request.LeaseGuard); err != nil {
		return ManualOperationBatchReceipt{}, err
	}
	if archivist.beforeCommit != nil {
		if err := archivist.beforeCommit(ctx, candidateID); err != nil {
			return ManualOperationBatchReceipt{}, err
		}
	}
	documents := make([]json.RawMessage, 0, len(operations))
	for _, operation := range operations {
		document, err := archivistOperationDocument(operation)
		if err != nil {
			return ManualOperationBatchReceipt{}, err
		}
		documents = append(documents, document)
	}
	receipt, err := archivist.transport.ApplyManualOperations(ctx, ManualOperationsRequest{
		Scope:    LifecycleScope{Domain: registration.Domain, Visibility: registration.Visibility},
		Producer: request.Producer, Operations: documents,
	})
	if err != nil {
		return ManualOperationBatchReceipt{}, err
	}
	if archivist.afterCommit != nil {
		if err := archivist.afterCommit(ctx, candidateID); err != nil {
			return ManualOperationBatchReceipt{}, err
		}
	}
	if err := archivistFence(ctx, request.LeaseGuard); err != nil {
		return ManualOperationBatchReceipt{}, err
	}
	return receipt, nil
}

func archivistOperationMetadata(request ArchivistRunRequest, projection CandidateLifecycleProjection, outcome ArchivistOutcome, target any, rationale string) OperationMetadata {
	return OperationMetadata{
		SchemaVersion: SchemaVersion,
		OperationID:   deterministicArchivistID("operation", request.IdempotencyKey, string(projection.Candidate.ID), string(outcome), fmt.Sprint(target), rationale, ArchivistPolicyVersion),
		OccurredAt:    projection.Candidate.RegisteredAt.UTC(), Producer: request.Producer,
		EvidenceSourceIDs: append([]SemanticID(nil), projection.SourceReferenceIDs...),
	}
}

func archivistOperationDocument(operation LifecycleOperation) (json.RawMessage, error) {
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
	return json.Marshal(fields)
}

func archivistFence(ctx context.Context, guard func(context.Context) error) error {
	if guard == nil {
		return errors.New("provenance Archivist lease guard is required")
	}
	if err := guard(ctx); err != nil {
		return fmt.Errorf("provenance Archivist lease lost: %w", err)
	}
	return nil
}

func deterministicArchivistID(parts ...string) SemanticID {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(part))
	}
	value := hash.Sum(nil)[:16]
	value[6] = (value[6] & 0x0f) | 0x50
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return SemanticID(encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32])
}

func cloneArchivistAnchors(value *StructuralAnchors) *StructuralAnchors {
	if value == nil {
		return nil
	}
	return &StructuralAnchors{
		Entities: append([]string(nil), value.Entities...), Topics: append([]string(nil), value.Topics...), Projects: append([]string(nil), value.Projects...),
	}
}

func archivistRelationshipType(outcome ArchivistOutcome) string {
	switch outcome {
	case ArchivistOutcomeSupersede:
		return archivistRelationshipSupersedes
	case ArchivistOutcomeCorrect:
		return archivistRelationshipCorrects
	case ArchivistOutcomeRefine:
		return archivistRelationshipRefines
	default:
		return ""
	}
}

func targetID(target *Record) any {
	if target == nil {
		return ""
	}
	return target.ID
}

func boundedArchivistReason(value string) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= MaximumArchivistOutcomeReason {
		return value
	}
	runes := []rune(value)
	return string(runes[:MaximumArchivistOutcomeReason])
}

func allArchivistReceiptsReplayed(receipt ManualOperationBatchReceipt) bool {
	if len(receipt.Receipts) == 0 {
		return false
	}
	for _, item := range receipt.Receipts {
		if !item.Replayed {
			return false
		}
	}
	return true
}

func accumulateArchivistOutcome(result *ArchivistRunResult, outcome ArchivistCandidateOutcome) {
	result.Outcomes = append(result.Outcomes, outcome)
	result.ExactReads += outcome.exactReads
	if outcome.Replayed {
		result.Replayed++
	}
	switch outcome.Outcome {
	case ArchivistOutcomeAccept:
		result.Accepted++
	case ArchivistOutcomeConsolidate:
		result.Consolidated++
	case ArchivistOutcomeSupersede, ArchivistOutcomeCorrect, ArchivistOutcomeRefine:
		result.Accepted++
		result.Related++
	case ArchivistOutcomeReject:
		result.Rejected++
	case ArchivistOutcomeDefer:
		result.Deferred++
		if outcome.CaseID != nil {
			if outcome.CaseCreated {
				result.CasesCreated++
			} else {
				result.CasesUpdated++
			}
		}
	}
}
