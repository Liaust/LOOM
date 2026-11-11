package loomcli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/response"
)

const provenanceCLIViewPrefix = "loom.provenance.cli."

type provenanceSearchView struct {
	SchemaVersion     string                                                   `json:"schema_version"`
	Query             string                                                   `json:"query"`
	Ordering          string                                                   `json:"ordering"`
	AcceptedRecords   provenance.SearchCollectionPage[provenanceSearchViewHit] `json:"accepted_records"`
	PendingCandidates provenance.SearchCollectionPage[provenanceSearchViewHit] `json:"pending_candidates"`
	UnresolvedCases   provenance.SearchCollectionPage[provenanceSearchViewHit] `json:"unresolved_cases"`
	RepositoryState   provenance.SearchCollectionPage[provenanceSearchViewHit] `json:"repo_state"`
	ProjectState      provenance.SearchCollectionPage[provenanceSearchViewHit] `json:"project_state"`
	Returned          int                                                      `json:"returned"`
	NextCursor        string                                                   `json:"next_cursor,omitempty"`
	Truncated         bool                                                     `json:"truncated"`
}

type provenanceSearchViewHit struct {
	RecordID     provenance.SemanticID     `json:"record_id,omitempty"`
	CandidateID  provenance.SemanticID     `json:"candidate_id,omitempty"`
	CaseID       provenance.SemanticID     `json:"case_id,omitempty"`
	RepositoryID string                    `json:"repository_id,omitempty"`
	ProjectID    string                    `json:"project_id,omitempty"`
	SnapshotID   provenance.SemanticID     `json:"snapshot_id,omitempty"`
	Match        provenanceSearchViewMatch `json:"match"`
	ExactGet     provenance.SearchExactGet `json:"exact_get"`
}

type provenanceSearchViewMatch struct {
	Summary          provenance.SearchText          `json:"summary"`
	Source           provenance.SearchSourceSummary `json:"source"`
	Freshness        provenance.SearchFreshness     `json:"freshness"`
	AssertionPosture string                         `json:"assertion_posture"`
	ProjectIDs       []string                       `json:"project_ids"`
	RepositoryIDs    []string                       `json:"repository_ids"`
	CompactTruncated bool                           `json:"compact_truncated"`
}

func newProvenanceSearchView(data provenance.SearchResponse) provenanceSearchView {
	view := provenanceSearchView{SchemaVersion: provenanceCLIViewPrefix + "search.v1", Query: data.Query, Ordering: data.Ordering, Returned: data.Returned, NextCursor: data.NextCursor, Truncated: data.Truncated}
	view.AcceptedRecords = searchViewPage(data.AcceptedRecords, func(item provenance.AcceptedRecordSearchResult) provenanceSearchViewHit {
		return provenanceSearchViewHit{RecordID: item.RecordID, Match: searchViewMatch(item.Match), ExactGet: item.ExactGet}
	})
	view.PendingCandidates = searchViewPage(data.PendingCandidates, func(item provenance.PendingCandidateSearchResult) provenanceSearchViewHit {
		return provenanceSearchViewHit{CandidateID: item.CandidateID, Match: searchViewMatch(item.Match), ExactGet: item.ExactGet}
	})
	view.UnresolvedCases = searchViewPage(data.UnresolvedCases, func(item provenance.UnresolvedCaseSearchResult) provenanceSearchViewHit {
		return provenanceSearchViewHit{CaseID: item.CaseID, Match: searchViewMatch(item.Match), ExactGet: item.ExactGet}
	})
	view.RepositoryState = searchViewPage(data.RepositoryState, func(item provenance.RepositoryStateSearchResult) provenanceSearchViewHit {
		return provenanceSearchViewHit{RepositoryID: item.RepositoryID, Match: searchViewMatch(item.Match), ExactGet: item.ExactGet}
	})
	view.ProjectState = searchViewPage(data.ProjectState, func(item provenance.ProjectStateSearchResult) provenanceSearchViewHit {
		return provenanceSearchViewHit{ProjectID: item.ProjectID, SnapshotID: item.SnapshotID, Match: searchViewMatch(item.Match), ExactGet: item.ExactGet}
	})
	return view
}

func searchViewPage[T any](page provenance.SearchCollectionPage[T], project func(T) provenanceSearchViewHit) provenance.SearchCollectionPage[provenanceSearchViewHit] {
	result := provenance.SearchCollectionPage[provenanceSearchViewHit]{Items: []provenanceSearchViewHit{}, Truncated: page.Truncated}
	for _, item := range page.Items {
		result.Items = append(result.Items, project(item))
	}
	return result
}

func searchViewMatch(match provenance.SearchCompactMatch) provenanceSearchViewMatch {
	return provenanceSearchViewMatch{match.Summary, match.Source, match.Freshness, match.AssertionPosture, match.ProjectIDs, match.RepositoryIDs, match.CompactTruncated}
}

// Upstream lifecycle gets expose one aggregate truncation bit. A false bit
// establishes completeness for loaded collections; true cannot identify which
// collection is partial. Never reinterpret that uncertainty as complete.
type provenanceViewCollection[T any] struct {
	Items        []T    `json:"items"`
	Completeness string `json:"completeness"`
	Truncated    bool   `json:"truncated"`
}

func provenanceViewPage[T any](items []T, upstreamPartial bool, limit int) provenanceViewCollection[T] {
	page := provenanceViewCollection[T]{Items: append([]T{}, items...), Completeness: "complete"}
	if upstreamPartial {
		page.Completeness = "unknown"
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.Truncated, page.Completeness = true, "partial"
	}
	return page
}

type provenanceExactView struct {
	RelationshipID   provenance.SemanticID                               `json:"relationship_id,omitempty"`
	RelationshipType string                                              `json:"relationship_type,omitempty"`
	FromRecordID     provenance.SemanticID                               `json:"from_record_id,omitempty"`
	ToRecordID       provenance.SemanticID                               `json:"to_record_id,omitempty"`
	SchemaVersion    string                                              `json:"schema_version"`
	RecordID         provenance.SemanticID                               `json:"record_id,omitempty"`
	CandidateID      provenance.SemanticID                               `json:"candidate_id,omitempty"`
	CaseID           provenance.SemanticID                               `json:"case_id,omitempty"`
	Claim            string                                              `json:"claim,omitempty"`
	RecordContext    string                                              `json:"record_context,omitempty"`
	Issue            string                                              `json:"issue,omitempty"`
	RecordKind       string                                              `json:"record_kind,omitempty"`
	Domain           string                                              `json:"domain"`
	Visibility       string                                              `json:"visibility"`
	AssertionPosture string                                              `json:"assertion_posture,omitempty"`
	EffectiveState   string                                              `json:"effective_state,omitempty"`
	RecordedAt       time.Time                                           `json:"recorded_at"`
	Temporal         *provenance.TemporalInterpretation                  `json:"temporal_interpretation,omitempty"`
	Modality         *string                                             `json:"modality,omitempty"`
	SourceActorID    *string                                             `json:"source_actor_id,omitempty"`
	ArtifactAuthorID *string                                             `json:"artifact_author_id,omitempty"`
	ApprovingActorID *string                                             `json:"approving_actor_id,omitempty"`
	SourceActor      *provenance.ActorReference                          `json:"source_actor,omitempty"`
	ArtifactAuthor   *provenance.ActorReference                          `json:"artifact_author,omitempty"`
	ApprovingActor   *provenance.ActorReference                          `json:"approving_actor,omitempty"`
	Scope            *provenanceScopeView                                `json:"scope,omitempty"`
	Ambiguity        *provenanceViewCollection[string]                   `json:"ambiguity,omitempty"`
	SourceReferences provenanceViewCollection[provenance.SemanticID]     `json:"source_references"`
	Relationships    provenanceViewCollection[provenance.SemanticID]     `json:"relationships"`
	Lineage          *provenanceViewCollection[provenance.SemanticID]    `json:"lineage,omitempty"`
	Resolution       *provenanceViewCollection[provenanceResolutionView] `json:"resolution,omitempty"`
	Members          *provenanceViewCollection[provenance.CaseMember]    `json:"members,omitempty"`
	Deferral         *provenanceDeferralView                             `json:"latest_deferral,omitempty"`
	SubmissionFields string                                              `json:"submission_fields,omitempty"`
	Sources          *provenanceSourcesView                              `json:"sources,omitempty"`
	Truncated        bool                                                `json:"truncated"`
	TruncatedFields  []string                                            `json:"truncated_fields"`
	DetailCommand    string                                              `json:"detail_command"`
}

func newProvenanceRelationshipView(data provenance.RelationshipLifecycleProjection) provenanceExactView {
	v := newProvenanceExactView("relationship", data.Relationship.ID, data.Truncated, nil)
	v.RelationshipID, v.RelationshipType = data.Relationship.ID, data.Relationship.RelationshipType
	v.FromRecordID, v.ToRecordID = data.Relationship.FromRecordID, data.Relationship.ToRecordID
	if data.Relationship.Context != nil {
		v.RecordContext = v.text("context", *data.Relationship.Context, 900)
	}
	v.EffectiveState, v.RecordedAt = data.EffectiveState, data.Relationship.CreatedAt
	v.SourceReferences = provenanceViewPage(data.Relationship.EvidenceSourceIDs, data.Truncated, searchExactProjectionLimitCLI)
	return v.withTruncation()
}

type provenanceScopeView struct {
	Projects provenanceViewCollection[string] `json:"projects"`
	Entities provenanceViewCollection[string] `json:"entities"`
	Topics   provenanceViewCollection[string] `json:"topics"`
}

type provenanceResolutionView struct {
	OperationID  *provenance.SemanticID `json:"operation_id,omitempty"`
	RecordID     *provenance.SemanticID `json:"record_id,omitempty"`
	EventType    string                 `json:"event_type,omitempty"`
	Summary      string                 `json:"summary,omitempty"`
	Outcome      *string                `json:"outcome,omitempty"`
	OccurredAt   *time.Time             `json:"occurred_at,omitempty"`
	Completeness string                 `json:"completeness"`
}

type provenanceDeferralView struct {
	ReasonCode      string     `json:"reason_code"`
	Reason          string     `json:"reason"`
	NextReviewAt    *time.Time `json:"next_review_at,omitempty"`
	NeedsCapability string     `json:"needs_capability,omitempty"`
	Completeness    string     `json:"completeness"`
}

func newProvenanceExactView(kind string, id provenance.SemanticID, partial bool, sources *provenance.LinkedSources) provenanceExactView {
	view := provenanceExactView{SchemaVersion: provenanceCLIViewPrefix + kind + ".v1", Truncated: partial, TruncatedFields: []string{}, DetailCommand: "loom provenance " + kind + " get " + string(id) + " --details --json"}
	view.Relationships = provenanceViewCollection[provenance.SemanticID]{Items: []provenance.SemanticID{}, Completeness: "not_loaded"}
	if sources != nil {
		view.DetailCommand += " --sources"
		view.Sources = newProvenanceSourcesView(*sources)
	}
	return view
}

func (view *provenanceExactView) text(field, value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	view.Truncated = true
	view.TruncatedFields = append(view.TruncatedFields, field)
	return string([]rune(value)[:limit-3]) + "..."
}

func (view *provenanceExactView) temporal(value provenance.TemporalInterpretation) *provenance.TemporalInterpretation {
	value.Interpretation = view.text("temporal_interpretation.interpretation", value.Interpretation, 600)
	value.SourceText = view.text("temporal_interpretation.source_text", value.SourceText, 300)
	value.Uncertainty = view.text("temporal_interpretation.uncertainty", value.Uncertainty, 300)
	return &value
}

func (view provenanceExactView) withTruncation() provenanceExactView {
	view.Truncated = view.Truncated || view.SourceReferences.Truncated
	if view.Scope != nil {
		view.Truncated = view.Truncated || view.Scope.Projects.Truncated || view.Scope.Entities.Truncated || view.Scope.Topics.Truncated
	}
	if view.Ambiguity != nil {
		view.Truncated = view.Truncated || view.Ambiguity.Truncated
	}
	if view.Lineage != nil {
		view.Truncated = view.Truncated || view.Lineage.Truncated
	}
	if view.Members != nil {
		view.Truncated = view.Truncated || view.Members.Truncated
	}
	if view.Resolution != nil {
		view.Truncated = view.Truncated || view.Resolution.Truncated
	}
	if view.Sources != nil {
		view.Truncated = view.Truncated || view.Sources.SourcesTruncated
		for _, item := range view.Sources.Items {
			view.Truncated = view.Truncated || len(item.TruncatedFields) > 0
		}
	}
	return view
}

func scopeView(anchors *provenance.StructuralAnchors) *provenanceScopeView {
	if anchors == nil {
		return nil
	}
	return &provenanceScopeView{provenanceViewPage(anchors.Projects, false, 8), provenanceViewPage(anchors.Entities, false, 8), provenanceViewPage(anchors.Topics, false, 8)}
}

func newProvenanceRecordView(data provenance.RecordLifecycleProjection, sources *provenance.LinkedSources) provenanceExactView {
	record := data.Record
	view := newProvenanceExactView("record", record.ID, data.Truncated, sources)
	view.RecordID, view.RecordKind, view.Domain, view.Visibility = record.ID, record.RecordKind, record.Domain, record.Visibility
	view.Claim, view.RecordContext = view.text("claim", record.Claim, 800), view.text("record_context", record.RecordContext, 600)
	view.AssertionPosture, view.RecordedAt = record.AssertionPosture, record.CreatedAt
	view.Temporal, view.Scope = view.temporal(record.Temporal), scopeView(record.Anchors)
	view.Modality, view.SourceActorID, view.ArtifactAuthorID, view.ApprovingActorID = record.Modality, record.SourceActorID, record.ArtifactAuthorID, record.ApprovingActorID
	ambiguity := provenanceViewPage(record.Ambiguity, data.Truncated, 4)
	for i := range ambiguity.Items {
		ambiguity.Items[i] = view.text(fmt.Sprintf("ambiguity.items.%d", i), ambiguity.Items[i], 300)
	}
	view.Ambiguity = &ambiguity
	view.SourceReferences = provenanceViewPage(data.SourceReferenceIDs, data.Truncated, 8)
	return view.withTruncation()
}

func newProvenanceCandidateView(data provenance.CandidateLifecycleProjection, sources *provenance.LinkedSources) provenanceExactView {
	candidate := data.Candidate
	view := newProvenanceExactView("candidate", candidate.ID, data.Truncated, sources)
	view.CandidateID, view.RecordKind, view.Domain, view.Visibility = candidate.ID, candidate.RecordKind, candidate.Domain, candidate.Visibility
	view.Claim, view.RecordContext = view.text("claim", candidate.Claim, 800), view.text("record_context", candidate.RecordContext, 600)
	view.AssertionPosture, view.EffectiveState, view.RecordedAt = candidate.AssertionPosture, data.EffectiveState, candidate.RegisteredAt
	view.SourceReferences = provenanceViewPage(data.SourceReferenceIDs, data.Truncated, 8)
	lineage := provenanceViewPage(data.Lineage, data.Truncated, 8)
	view.Lineage = &lineage
	var registration *provenance.CandidateRegistration
	view.SubmissionFields = "unavailable"
	if len(candidate.Submitted) > 0 && json.Unmarshal(candidate.Submitted, &registration) == nil && registration != nil {
		view.SubmissionFields = "loaded"
		view.Temporal, view.Scope = view.temporal(registration.Temporal), scopeView(registration.Anchors)
		view.SourceActor, view.ArtifactAuthor, view.ApprovingActor = registration.SourceActor, registration.ArtifactAuthor, registration.ApprovingActor
		ambiguity := provenanceViewPage(registration.Ambiguity, false, 4)
		for i := range ambiguity.Items {
			ambiguity.Items[i] = view.text(fmt.Sprintf("ambiguity.items.%d", i), ambiguity.Items[i], 300)
		}
		view.Ambiguity = &ambiguity
	}
	var resolutions []provenanceResolutionView
	if data.WinningOperationID != nil {
		resolutions = append(resolutions, provenanceResolutionView{OperationID: data.WinningOperationID, Completeness: "unknown"})
		for _, event := range data.Events {
			if event.OperationID != *data.WinningOperationID {
				continue
			}
			var payload struct {
				RecordID  *provenance.SemanticID `json:"record_id"`
				Reason    string                 `json:"reason"`
				Rationale string                 `json:"rationale"`
			}
			if json.Unmarshal(event.Payload, &payload) == nil {
				resolutions[0].RecordID = payload.RecordID
				resolutions[0].Summary = view.text("resolution.items.0.summary", strings.TrimSpace(payload.Reason+" "+payload.Rationale), 300)
				resolutions[0].Completeness = "loaded"
			}
			resolutions[0].EventType = event.EventType
			break
		}
	}
	resolution := provenanceViewPage(resolutions, data.Truncated, 4)
	view.Resolution = &resolution
	if len(data.LatestDeferral) > 0 {
		deferral := provenanceDeferralView{Completeness: "unavailable"}
		if json.Unmarshal(data.LatestDeferral, &deferral) == nil {
			deferral.Completeness = "loaded"
			deferral.Reason = view.text("latest_deferral.reason", deferral.Reason, 300)
			deferral.NeedsCapability = view.text("latest_deferral.needs_capability", deferral.NeedsCapability, 300)
		}
		view.Deferral = &deferral
	}
	return view.withTruncation()
}

func newProvenanceCaseView(data provenance.ResolutionCaseLifecycleProjection) provenanceExactView {
	view := newProvenanceExactView("case", data.Case.ID, data.Truncated, nil)
	view.CaseID, view.Domain, view.Visibility, view.RecordedAt = data.Case.ID, data.Case.Domain, data.Case.Visibility, data.Case.CreatedAt
	view.Issue, view.EffectiveState = view.text("issue", data.Case.Issue, 800), data.EffectiveState
	members := provenanceViewPage(data.Members, data.Truncated, 8)
	view.Members = &members
	var summaries []provenanceResolutionView
	var sourceIDs []provenance.SemanticID
	for _, event := range data.Events {
		summaries = append(summaries, provenanceResolutionView{OperationID: event.OperationID, EventType: event.EventType, Summary: event.Summary, Outcome: event.Outcome, OccurredAt: &event.OccurredAt, Completeness: "loaded"})
		for _, id := range event.EvidenceSourceIDs {
			found := false
			for _, old := range sourceIDs {
				if id == old {
					found = true
					break
				}
			}
			if !found {
				sourceIDs = append(sourceIDs, id)
			}
		}
	}
	resolution := provenanceViewPage(summaries, data.Truncated, 4)
	for i := range resolution.Items {
		resolution.Items[i].Summary = view.text(fmt.Sprintf("resolution.items.%d.summary", i), resolution.Items[i].Summary, 300)
		if resolution.Items[i].Outcome != nil {
			outcome := view.text(fmt.Sprintf("resolution.items.%d.outcome", i), *resolution.Items[i].Outcome, 300)
			resolution.Items[i].Outcome = &outcome
		}
	}
	view.Resolution = &resolution
	view.SourceReferences = provenanceViewPage(sourceIDs, data.Truncated, 8)
	return view.withTruncation()
}

type provenanceSourcesView struct {
	SchemaVersion    string                 `json:"schema_version"`
	ParentKind       string                 `json:"parent_kind"`
	ParentID         provenance.SemanticID  `json:"parent_id"`
	Posture          string                 `json:"posture"`
	Items            []provenanceSourceView `json:"items"`
	Returned         int                    `json:"returned"`
	SourcesTruncated bool                   `json:"sources_truncated"`
	IncompleteItems  int                    `json:"incomplete_items"`
}

type provenanceSourceView struct {
	provenance.LinkedSourceItem
	TruncatedFields     []string `json:"truncated_fields"`
	ExcerptAvailability string   `json:"excerpt_availability"`
}

func newProvenanceSourcesView(data provenance.LinkedSources) *provenanceSourcesView {
	view := &provenanceSourcesView{SchemaVersion: data.SchemaVersion, ParentKind: data.ParentKind, ParentID: data.ParentID, Posture: data.Posture, Items: []provenanceSourceView{}, SourcesTruncated: data.SourcesTruncated, IncompleteItems: data.IncompleteItems}
	page := provenanceViewPage(data.Items, false, 4)
	view.SourcesTruncated = view.SourcesTruncated || page.Truncated
	for _, source := range page.Items {
		item := provenanceSourceView{LinkedSourceItem: source, TruncatedFields: []string{}, ExcerptAvailability: "not_available_in_stored_receipt"}
		if source.StoredSourceReceipt != nil {
			receipt := *source.StoredSourceReceipt
			item.StoredSourceReceipt = &receipt
			for _, field := range []struct {
				name  string
				value **string
			}{{"canonical_locator", &receipt.CanonicalLocator}, {"version_address", &receipt.VersionAddress}, {"gap_reason", &receipt.GapReason}} {
				if *field.value != nil && utf8.RuneCountInString(**field.value) > 300 {
					value := string([]rune(**field.value)[:297]) + "..."
					*field.value = &value
					item.TruncatedFields = append(item.TruncatedFields, field.name)
				}
			}
		}
		view.Items = append(view.Items, item)
	}
	view.Returned = len(view.Items)
	return view
}

func renderProvenanceView[T, V any](cmd *cobra.Command, opts *options, envelope response.Envelope[T], view V, details bool) error {
	if details {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		if !opts.jsonOutput {
			encoder.SetIndent("", "  ")
		}
		return encoder.Encode(envelope)
	}
	projected := response.Envelope[V]{OK: envelope.OK, Data: view, Meta: envelope.Meta}
	if opts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(projected)
	}
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(view); err != nil {
		return err
	}
	renderResponseMeta(cmd, opts, envelope.Meta)
	return nil
}

func renderProvenanceSearchView(cmd *cobra.Command, view provenanceSearchView) {
	fmt.Fprintf(cmd.OutOrStdout(), "Provenance search: %s\n", strconv.Quote(view.Query))
	for _, section := range []struct {
		title string
		page  provenance.SearchCollectionPage[provenanceSearchViewHit]
	}{
		{"Project state (source declarations)", view.ProjectState}, {"Accepted records", view.AcceptedRecords}, {"Unresolved cases", view.UnresolvedCases}, {"Pending candidates (unaccepted)", view.PendingCandidates}, {"Repository state", view.RepositoryState},
	} {
		fmt.Fprintf(cmd.OutOrStdout(), "%s: %d; truncated: %t\n", section.title, len(section.page.Items), section.page.Truncated)
		for _, item := range section.page.Items {
			match := item.Match
			fmt.Fprintf(cmd.OutOrStdout(), "- %s\n  %s [%s; %s]\n", strconv.Quote(match.Summary.Text), item.ExactGet.ID, strconv.Quote(match.AssertionPosture), strconv.Quote(match.Freshness.Currentness))
			if item.SnapshotID != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "  Snapshot: %s\n", item.SnapshotID)
			}
			// Render the same qualified fields as JSON, including temporal and
			// source partialness, without repeating the displayed summary.
			match.Summary.Text = ""
			qualified, _ := json.Marshal(match)
			fmt.Fprintf(cmd.OutOrStdout(), "  Qualifications: %s\n  Exact: %s\n", qualified, declarationDisplay(item.ExactGet.Path))
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Returned: %d; truncated: %t\n", view.Returned, view.Truncated)
	if strings.TrimSpace(view.NextCursor) != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Next cursor: %s\n", view.NextCursor)
	}
}
