package agents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/events"
	"loom.local/loom/internal/requestctx"
)

var ErrToolNotAvailable = errors.New("agent tool is not available in this work context")

type providerRouteHints struct {
	TargetNodeID       string
	HealthStatus       string
	AvailabilityStatus string
	PresenceState      string
}

type usageMatch struct {
	UsageDocumentID string
	SectionLabel    string
}

func (s Service) SearchTools(ctx context.Context, req requestctx.Context, input ToolSearchInput) (ToolSearchResult, error) {
	input = normalizeToolSearchInput(input)
	if input.WorkContextRef == "" {
		return ToolSearchResult{}, fmt.Errorf("work_context_ref is required")
	}
	if input.Query == "" {
		return ToolSearchResult{}, fmt.Errorf("query is required")
	}
	if err := validateObjectJSON(input.Metadata, "metadata"); err != nil {
		return ToolSearchResult{}, err
	}

	detail, err := s.activeWorkContextDetail(ctx, input.WorkContextRef)
	if err != nil {
		return ToolSearchResult{}, err
	}
	toolView := detail.ToolView.ToolView

	capabilitySvc := capabilities.Service{DB: s.DB}
	searchLimit := input.Limit * 3
	if searchLimit < input.Limit {
		searchLimit = input.Limit
	}
	if searchLimit > 40 {
		searchLimit = 40
	}
	candidates, err := capabilitySvc.SearchCapabilities(ctx, capabilities.CapabilitySearchInput{
		Query:       input.Query,
		Limit:       searchLimit,
		ProviderRef: input.ProviderRef,
		ScopeRef:    input.ScopeRef,
		Form:        input.Form,
		Status:      capabilities.EndpointStatusActive,
	})
	if err != nil {
		return ToolSearchResult{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ToolSearchResult{}, err
	}
	defer tx.Rollback()

	results := []ToolSearchCandidate{}
	for _, candidate := range candidates {
		if len(results) >= input.Limit {
			break
		}

		hints, err := capabilityProviderRouteHints(ctx, tx, candidate.ProviderID)
		if err != nil {
			return ToolSearchResult{}, err
		}
		authLevel, err := actorAuthorizationLevel(ctx, tx, detail.WorkContext.ActorID, hints.TargetNodeID)
		if err != nil {
			return ToolSearchResult{}, err
		}
		if authLevel == nil {
			continue
		}

		visibility := VisibilityRequestable
		reason := "actor_can_request_with_approval"
		approvalHint := "approval_required"
		if *authLevel >= candidate.ExecutionAuthorizationLevel {
			visibility = VisibilityVisible
			reason = "actor_authorized_on_target_node"
			approvalHint = "none"
		}
		if visibility == VisibilityRequestable && !includeRequestable(input.IncludeRequestable) {
			continue
		}

		match, err := findMatchedUsageDocument(ctx, tx, candidate.CapabilityEndpointID, input.Query)
		if err != nil {
			return ToolSearchResult{}, err
		}
		entry, err := getOrInsertSearchCandidateEntryTx(ctx, tx, toolView.ToolViewID, candidate, hints, authLevel, visibility, reason, approvalHint)
		if err != nil {
			return ToolSearchResult{}, err
		}
		results = append(results, agentToolSearchCandidate(entry, candidate, hints, match))
	}

	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       events.TypeAgentToolSearched,
		EventLevel:      "audit",
		Request:         req,
		ScopeID:         stringValue(detail.WorkContext.CurrentScopeID),
		TargetKind:      "agent_work_context",
		TargetID:        detail.WorkContext.AgentWorkContextID,
		Status:          detail.WorkContext.Status,
		Result:          "searched",
		VisibilityClass: "internal",
		Payload: map[string]any{
			"agent_work_context_id": detail.WorkContext.AgentWorkContextID,
			"tool_view_id":          toolView.ToolViewID,
			"query":                 input.Query,
			"candidate_count":       len(results),
		},
	}); err != nil {
		return ToolSearchResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ToolSearchResult{}, err
	}

	message := ""
	if len(results) == 0 {
		message = "No visible or requestable tools matched this query."
	}
	return ToolSearchResult{
		AgentWorkContextID: detail.WorkContext.AgentWorkContextID,
		ToolViewID:         toolView.ToolViewID,
		Query:              input.Query,
		Candidates:         results,
		Message:            message,
	}, nil
}

func (s Service) InspectTool(ctx context.Context, req requestctx.Context, input ToolInspectInput) (ToolInspection, error) {
	input = normalizeToolInspectInput(input)
	if input.WorkContextRef == "" {
		return ToolInspection{}, fmt.Errorf("work_context_ref is required")
	}
	if input.ToolRef == "" {
		return ToolInspection{}, fmt.Errorf("tool_ref is required")
	}

	detail, err := s.activeWorkContextDetail(ctx, input.WorkContextRef)
	if err != nil {
		return ToolInspection{}, err
	}
	toolView := detail.ToolView.ToolView

	entry, err := getToolViewEntryForRef(ctx, s.DB, toolView.ToolViewID, input.ToolRef)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ToolInspection{}, ErrToolNotAvailable
		}
		return ToolInspection{}, err
	}
	if entry.VisibilityState == VisibilityRedacted {
		return ToolInspection{}, ErrToolNotAvailable
	}

	var inspection ToolInspection
	switch entry.EntryKind {
	case EntryKindOperatingTool:
		toolSchema, err := operatingToolSchema(entry)
		if err != nil {
			return ToolInspection{}, err
		}
		inspection = ToolInspection{
			AgentWorkContextID: detail.WorkContext.AgentWorkContextID,
			ToolViewID:         toolView.ToolViewID,
			Entry:              entry,
			ToolSchema:         toolSchema,
			PolicyHint:         policyHintForEntry(entry),
			AvailabilityHint:   entry.AvailabilityHint,
			PresenceHint:       "local",
			RouteHint:          "internal_operating_tool",
			Metadata:           json.RawMessage(`{"tool_kind":"operating_tool"}`),
		}
	case EntryKindCapability:
		if entry.CapabilityEndpointID == nil || *entry.CapabilityEndpointID == "" {
			return ToolInspection{}, ErrToolNotAvailable
		}
		capabilitySvc := capabilities.Service{DB: s.DB}
		capabilityInspection, err := capabilitySvc.InspectCapability(ctx, *entry.CapabilityEndpointID)
		if err != nil {
			return ToolInspection{}, ErrToolNotAvailable
		}
		if capabilityInspection.Endpoint.Status != capabilities.EndpointStatusActive || capabilityInspection.Provider.Status != capabilities.ProviderStatusActive {
			return ToolInspection{}, ErrToolNotAvailable
		}
		hints, err := capabilityProviderRouteHints(ctx, s.DB, capabilityInspection.Provider.ProviderID)
		if err != nil {
			return ToolInspection{}, err
		}
		sections := extractUsageSections(capabilityInspection.UsageDocuments, input.Query, input.UsageSectionLabel, input.MaxSections, input.MaxCharsPerSection)
		toolSchema := capabilityToolSchema(entry, capabilityInspection)
		inspection = ToolInspection{
			AgentWorkContextID: detail.WorkContext.AgentWorkContextID,
			ToolViewID:         toolView.ToolViewID,
			Entry:              entry,
			ToolSchema:         toolSchema,
			UsageSections:      sections,
			PolicyHint:         policyHintForEntry(entry),
			AvailabilityHint:   nonEmpty(hints.AvailabilityStatus, entry.AvailabilityHint),
			PresenceHint:       nonEmpty(hints.PresenceState, "unknown"),
			RouteHint:          "call_via loom.capability.call",
			Metadata:           json.RawMessage(`{"tool_kind":"capability"}`),
		}
	default:
		return ToolInspection{}, ErrToolNotAvailable
	}

	if _, err := (events.Service{DB: s.DB}).Append(ctx, events.AppendInput{
		EventType:       events.TypeAgentToolInspected,
		EventLevel:      "audit",
		Request:         req,
		ScopeID:         stringValue(detail.WorkContext.CurrentScopeID),
		TargetKind:      "tool_view_entry",
		TargetID:        entry.ToolViewEntryID,
		Status:          entry.VisibilityState,
		Result:          "inspected",
		VisibilityClass: "internal",
		Payload: map[string]any{
			"agent_work_context_id": detail.WorkContext.AgentWorkContextID,
			"tool_view_id":          toolView.ToolViewID,
			"tool_view_entry_id":    entry.ToolViewEntryID,
			"tool_name":             entry.ToolName,
			"entry_kind":            entry.EntryKind,
			"capability_address":    entry.CapabilityAddress,
		},
	}); err != nil {
		return ToolInspection{}, err
	}

	return inspection, nil
}

func normalizeToolSearchInput(input ToolSearchInput) ToolSearchInput {
	input.WorkContextRef = strings.TrimSpace(input.WorkContextRef)
	input.Query = strings.TrimSpace(input.Query)
	if input.Limit <= 0 || input.Limit > 20 {
		input.Limit = 10
	}
	input.Form = strings.TrimSpace(input.Form)
	input.ProviderRef = strings.TrimSpace(input.ProviderRef)
	input.ScopeRef = strings.TrimSpace(input.ScopeRef)
	return input
}

func normalizeToolInspectInput(input ToolInspectInput) ToolInspectInput {
	input.WorkContextRef = strings.TrimSpace(input.WorkContextRef)
	input.ToolRef = strings.TrimSpace(input.ToolRef)
	input.Query = strings.TrimSpace(input.Query)
	input.UsageSectionLabel = strings.TrimSpace(input.UsageSectionLabel)
	if input.MaxSections <= 0 || input.MaxSections > 5 {
		input.MaxSections = 3
	}
	if input.MaxCharsPerSection <= 0 || input.MaxCharsPerSection > 2000 {
		input.MaxCharsPerSection = 800
	}
	return input
}

func includeRequestable(value *bool) bool {
	if value == nil {
		return true
	}
	return *value
}

func (s Service) activeWorkContextDetail(ctx context.Context, ref string) (WorkContextDetail, error) {
	detail, err := s.GetWorkContext(ctx, ref)
	if err != nil {
		return WorkContextDetail{}, err
	}
	if detail.WorkContext.Status != WorkContextStatusActive {
		return WorkContextDetail{}, fmt.Errorf("work context is not active")
	}
	if detail.AccessSession.Status != AccessSessionStatusActive {
		return WorkContextDetail{}, fmt.Errorf("access session is not active")
	}
	if detail.ToolView == nil || detail.WorkContext.ActiveToolViewID == nil || *detail.WorkContext.ActiveToolViewID == "" {
		return WorkContextDetail{}, fmt.Errorf("work context has no active tool view")
	}
	if detail.ToolView.ToolView.Status != ToolViewStatusActive {
		return WorkContextDetail{}, fmt.Errorf("tool view is not active")
	}
	return detail, nil
}

func capabilityProviderRouteHints(ctx context.Context, q queryer, providerID string) (providerRouteHints, error) {
	var hints providerRouteHints
	var availability, health, presence sql.NullString
	err := q.QueryRowContext(ctx, `
		SELECT p.node_id,
		       COALESCE(h.health_status, 'unknown'),
		       COALESCE(h.availability_status, 'unknown'),
		       COALESCE(n.presence_state, 'unknown')
		FROM capabilities.providers p
		LEFT JOIN capabilities.provider_health h ON h.provider_id = p.provider_id
		LEFT JOIN nodes.nodes n ON n.node_id = p.node_id
		WHERE p.provider_id = $1
	`, providerID).Scan(&hints.TargetNodeID, &health, &availability, &presence)
	if err != nil {
		return providerRouteHints{}, err
	}
	hints.HealthStatus = nonEmpty(health.String, "unknown")
	hints.AvailabilityStatus = nonEmpty(availability.String, "unknown")
	hints.PresenceState = nonEmpty(presence.String, "unknown")
	return hints, nil
}

func findMatchedUsageDocument(ctx context.Context, q queryer, endpointID, query string) (usageMatch, error) {
	pattern := "%" + query + "%"
	var match usageMatch
	err := q.QueryRowContext(ctx, `
		SELECT capability_usage_document_id
		FROM capabilities.usage_documents
		WHERE target_kind = 'capability_endpoint'
		  AND target_id = $1
		  AND review_status = 'approved'
		  AND (title ILIKE $2 OR body ILIKE $2)
		ORDER BY
			CASE WHEN title ILIKE $2 THEN 0 WHEN body ILIKE $2 THEN 1 ELSE 2 END,
			updated_at DESC,
			title
		LIMIT 1
	`, endpointID, pattern).Scan(&match.UsageDocumentID)
	if err == sql.ErrNoRows {
		return usageMatch{}, nil
	}
	if err != nil {
		return usageMatch{}, err
	}
	match.SectionLabel = "matched_document"
	return match, nil
}

func getOrInsertSearchCandidateEntryTx(ctx context.Context, tx *sql.Tx, toolViewID string, candidate capabilities.CapabilityCandidate, hints providerRouteHints, authLevel *int, visibility, reason, approvalHint string) (ToolViewEntry, error) {
	toolName := capabilityToolName(candidate.CompactAddress)
	entry, err := getToolViewEntryForRef(ctx, tx, toolViewID, candidate.CapabilityEndpointID)
	if err == nil {
		return entry, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ToolViewEntry{}, err
	}
	return insertToolViewEntryTx(ctx, tx, toolViewID, toolViewEntryInsert{
		EntryKind:                   EntryKindCapability,
		ToolName:                    toolName,
		VisibilityState:             visibility,
		SourceLayer:                 SourceLayerSearch,
		ReasonCode:                  reason,
		CapabilityEndpointID:        candidate.CapabilityEndpointID,
		CapabilityAddress:           candidate.CompactAddress,
		ProviderID:                  candidate.ProviderID,
		ProviderAddress:             candidate.ProviderAddress,
		TargetNodeID:                hints.TargetNodeID,
		DisplayName:                 candidate.DisplayName,
		Description:                 candidate.Description,
		RiskLevel:                   candidate.RiskLevel,
		ExecutionAuthorizationLevel: candidate.ExecutionAuthorizationLevel,
		ActorAuthorizationLevel:     authLevel,
		ApprovalHint:                approvalHint,
		AvailabilityHint:            nonEmpty(hints.AvailabilityStatus, candidate.ProviderHealth),
		MatchedUseSummary:           candidate.MatchedUseSummary,
		CompactMetadata:             searchCandidateMetadata(candidate, hints),
		SchemaSummary:               json.RawMessage(`{"input_schema_available":true,"output_schema_available":true}`),
	})
}

func getToolViewEntryForRef(ctx context.Context, q queryer, toolViewID, ref string) (ToolViewEntry, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ToolViewEntry{}, sql.ErrNoRows
	}
	return scanToolViewEntry(q.QueryRowContext(ctx, toolViewEntrySelectSQL()+`
		WHERE tool_view_id = $1
		  AND (
		       tool_view_entry_id = $2
		    OR tool_name = $2
		    OR capability_address = $2
		    OR capability_endpoint_id = $2
		  )
		ORDER BY
			CASE WHEN tool_view_entry_id = $2 THEN 0
			     WHEN capability_address = $2 THEN 1
			     WHEN capability_endpoint_id = $2 THEN 2
			     ELSE 3
			END,
			created_at
		LIMIT 1
	`, toolViewID, ref))
}

func agentToolSearchCandidate(entry ToolViewEntry, candidate capabilities.CapabilityCandidate, hints providerRouteHints, match usageMatch) ToolSearchCandidate {
	return ToolSearchCandidate{
		ToolViewEntryID:             entry.ToolViewEntryID,
		ToolName:                    entry.ToolName,
		CapabilityAddress:           entry.CapabilityAddress,
		ProviderAddress:             entry.ProviderAddress,
		TargetNodeID:                entry.TargetNodeID,
		DisplayName:                 entry.DisplayName,
		Description:                 entry.Description,
		MatchedUseSummary:           compactWhitespace(nonEmpty(candidate.MatchedUseSummary, entry.MatchedUseSummary), 280),
		MatchedUsageDocumentID:      match.UsageDocumentID,
		MatchedUsageSectionLabel:    match.SectionLabel,
		Tags:                        candidate.Tags,
		RiskLevel:                   entry.RiskLevel,
		ExecutionAuthorizationLevel: entry.ExecutionAuthorizationLevel,
		ActorAuthorizationLevel:     entry.ActorAuthorizationLevel,
		VisibilityState:             entry.VisibilityState,
		ApprovalHint:                entry.ApprovalHint,
		AvailabilityHint:            nonEmpty(hints.AvailabilityStatus, entry.AvailabilityHint),
		PresenceHint:                nonEmpty(hints.PresenceState, "unknown"),
		Score:                       candidate.Score,
		MatchReasons:                candidate.MatchReasons,
		Metadata:                    jsonOrDefault(entry.CompactMetadata, `{}`),
	}
}

func searchCandidateMetadata(candidate capabilities.CapabilityCandidate, hints providerRouteHints) json.RawMessage {
	raw, err := json.Marshal(map[string]any{
		"class_name":          candidate.ClassName,
		"form":                candidate.Form,
		"provider_health":     candidate.ProviderHealth,
		"availability_status": hints.AvailabilityStatus,
		"presence_state":      hints.PresenceState,
	})
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func operatingToolSchema(entry ToolViewEntry) (ToolSchema, error) {
	for _, spec := range operatingToolSpecs() {
		if spec.Name == entry.ToolName {
			return ToolSchema{
				Name:         spec.Name,
				Description:  spec.Description,
				InputSchema:  jsonOrDefault(spec.InputSchema, `{}`),
				OutputSchema: jsonOrDefault(spec.OutputSchema, `{}`),
				Loom: ToolSchemaLOOM{
					EntryKind:       entry.EntryKind,
					ToolViewEntryID: entry.ToolViewEntryID,
					ToolViewID:      entry.ToolViewID,
					VisibilityState: entry.VisibilityState,
					CallVia:         spec.Name,
				},
			}, nil
		}
	}
	return ToolSchema{}, ErrToolNotAvailable
}

func capabilityToolSchema(entry ToolViewEntry, inspection capabilities.CapabilityInspection) ToolSchema {
	inputSchema := inspection.Endpoint.InputSchemaJSON
	outputSchema := inspection.Endpoint.OutputSchemaJSON
	if inspection.ActiveVersion != nil {
		inputSchema = inspection.ActiveVersion.InputSchemaJSON
		outputSchema = inspection.ActiveVersion.OutputSchemaJSON
	}
	return ToolSchema{
		Name:         entry.ToolName,
		Description:  nonEmpty(inspection.Endpoint.EndpointName+": "+inspection.Class.Description, entry.Description),
		InputSchema:  jsonOrDefault(inputSchema, `{}`),
		OutputSchema: jsonOrDefault(outputSchema, `{}`),
		Loom: ToolSchemaLOOM{
			CapabilityEndpointID:        inspection.Endpoint.CapabilityEndpointID,
			CapabilityAddress:           inspection.Endpoint.CompactAddress,
			ProviderID:                  inspection.Provider.ProviderID,
			ProviderAddress:             inspection.Provider.CompactAddress,
			TargetNodeID:                entry.TargetNodeID,
			EntryKind:                   entry.EntryKind,
			ToolViewEntryID:             entry.ToolViewEntryID,
			ToolViewID:                  entry.ToolViewID,
			ExecutionAuthorizationLevel: entry.ExecutionAuthorizationLevel,
			ActorAuthorizationLevel:     entry.ActorAuthorizationLevel,
			RiskLevel:                   entry.RiskLevel,
			VisibilityState:             entry.VisibilityState,
			CallVia:                     "loom.capability.call",
		},
	}
}

func policyHintForEntry(entry ToolViewEntry) ToolPolicyHint {
	return ToolPolicyHint{
		VisibilityState: entry.VisibilityState,
		ApprovalHint:    entry.ApprovalHint,
		ReasonCode:      entry.ReasonCode,
	}
}

type markdownSection struct {
	Label   string
	Heading string
	Body    string
}

func extractUsageSections(docs []capabilities.UsageDocument, query, label string, maxSections, maxChars int) []UsageSection {
	queryTerms := normalizedTerms(query)
	matches := []UsageSection{}
	for _, doc := range docs {
		if doc.ReviewStatus != capabilities.UsageReviewStatusApproved {
			continue
		}
		sections := splitMarkdownSections(doc.Body)
		for _, section := range sections {
			if label != "" && section.Label != label {
				continue
			}
			reason := usageSectionMatchReason(section, queryTerms)
			if label == "" && len(queryTerms) > 0 && reason == "" {
				continue
			}
			if reason == "" {
				reason = "default"
			}
			matches = append(matches, UsageSection{
				UsageDocumentID: doc.CapabilityUsageDocumentID,
				Title:           doc.Title,
				SectionLabel:    section.Label,
				SectionHeading:  section.Heading,
				Excerpt:         compactWhitespace(section.Body, maxChars),
				MatchReason:     reason,
			})
			if len(matches) >= maxSections {
				return matches
			}
		}
	}
	if len(matches) > 0 || len(queryTerms) == 0 {
		return matches
	}
	for _, doc := range docs {
		if doc.ReviewStatus != capabilities.UsageReviewStatusApproved {
			continue
		}
		sections := splitMarkdownSections(doc.Body)
		for _, section := range sections {
			matches = append(matches, UsageSection{
				UsageDocumentID: doc.CapabilityUsageDocumentID,
				Title:           doc.Title,
				SectionLabel:    section.Label,
				SectionHeading:  section.Heading,
				Excerpt:         compactWhitespace(section.Body, maxChars),
				MatchReason:     "fallback",
			})
			if len(matches) >= maxSections {
				return matches
			}
		}
	}
	return matches
}

func splitMarkdownSections(body string) []markdownSection {
	lines := strings.Split(body, "\n")
	sections := []markdownSection{}
	current := markdownSection{Label: "section-1", Heading: "Overview"}
	bodyLines := []string{}
	sectionNumber := 1

	flush := func() {
		text := strings.TrimSpace(strings.Join(bodyLines, "\n"))
		if text == "" && len(sections) > 0 {
			return
		}
		current.Body = text
		sections = append(sections, current)
		bodyLines = []string{}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			heading := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
			if heading != "" {
				if len(bodyLines) > 0 || len(sections) == 0 && current.Heading != "Overview" {
					flush()
					sectionNumber = len(sections) + 1
				}
				current = markdownSection{Label: fmt.Sprintf("section-%d", sectionNumber), Heading: heading}
				sectionNumber++
				continue
			}
		}
		bodyLines = append(bodyLines, line)
	}
	flush()

	if len(sections) == 0 {
		return []markdownSection{{Label: "section-1", Heading: "Overview", Body: strings.TrimSpace(body)}}
	}
	for i := range sections {
		if sections[i].Body == "" {
			sections[i].Body = sections[i].Heading
		}
	}
	return sections
}

func usageSectionMatchReason(section markdownSection, terms []string) string {
	if len(terms) == 0 {
		return ""
	}
	heading := strings.ToLower(section.Heading)
	body := strings.ToLower(section.Body)
	for _, term := range terms {
		if strings.Contains(heading, term) {
			return "heading"
		}
	}
	for _, term := range terms {
		if strings.Contains(body, term) {
			return "body"
		}
	}
	return ""
}

func normalizedTerms(query string) []string {
	fields := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	seen := map[string]struct{}{}
	out := []string{}
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if len(field) < 3 {
			continue
		}
		if _, exists := seen[field]; exists {
			continue
		}
		seen[field] = struct{}{}
		out = append(out, field)
	}
	sort.Strings(out)
	return out
}

func compactWhitespace(value string, maxChars int) string {
	value = strings.Join(strings.Fields(value), " ")
	if maxChars <= 0 || len(value) <= maxChars {
		return value
	}
	if maxChars <= 3 {
		return value[:maxChars]
	}
	return strings.TrimSpace(value[:maxChars-3]) + "..."
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
