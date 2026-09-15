package agents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/realtime"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/routing"
)

func (s Service) CallTool(ctx context.Context, req requestctx.Context, input AgentToolCallInput) (AgentToolCallOutcome, error) {
	input = normalizeAgentToolCallInput(input)
	if input.WorkContextRef == "" {
		return AgentToolCallOutcome{}, fmt.Errorf("work_context_ref is required")
	}
	if input.ToolRef == "" {
		return AgentToolCallOutcome{}, fmt.Errorf("tool_ref is required")
	}
	if err := validateObjectJSON(input.Input, "input"); err != nil {
		return AgentToolCallOutcome{}, err
	}
	if err := validateObjectJSON(input.Metadata, "metadata"); err != nil {
		return AgentToolCallOutcome{}, err
	}

	detail, err := s.activeWorkContextDetail(ctx, input.WorkContextRef)
	if err != nil {
		return AgentToolCallOutcome{}, err
	}
	toolView := detail.ToolView.ToolView
	entry, err := getToolViewEntryForRef(ctx, s.DB, toolView.ToolViewID, input.ToolRef)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AgentToolCallOutcome{}, ErrToolNotAvailable
		}
		return AgentToolCallOutcome{}, err
	}
	if entry.VisibilityState == VisibilityRedacted {
		return AgentToolCallOutcome{}, ErrToolNotAvailable
	}

	if existing, found, err := getAgentToolCallByIdempotency(ctx, s.DB, detail.WorkContext.AgentWorkContextID, entry.ToolName, input.IdempotencyKey); err != nil {
		return AgentToolCallOutcome{}, err
	} else if found {
		return s.agentToolCallOutcomeFromRecord(ctx, existing, entry), nil
	}

	agentReq := agentRequestContext(req, detail.WorkContext)
	if entry.EntryKind == EntryKindOperatingTool {
		return s.callOperatingTool(ctx, agentReq, detail, toolView, entry, input)
	}
	if entry.EntryKind != EntryKindCapability || entry.CapabilityEndpointID == nil || entry.CapabilityAddress == "" {
		return AgentToolCallOutcome{}, ErrToolNotAvailable
	}

	routingOutcome, err := s.Routing.Call(ctx, agentReq, routing.CapabilityCallInput{
		Target:          entry.CapabilityAddress,
		ActorRef:        detail.WorkContext.ActorID,
		OriginNodeRef:   stringValue(detail.WorkContext.RuntimeNodeID),
		ScopeRef:        stringValue(detail.WorkContext.CurrentScopeID),
		Input:           objectOrDefault(input.Input),
		RequestApproval: input.RequestApproval,
		ApprovalReason:  input.ApprovalReason,
		Metadata:        agentToolCallMetadata(input.Metadata, detail.WorkContext, entry),
	}, input.IdempotencyKey)
	if err != nil {
		return AgentToolCallOutcome{}, err
	}

	if routingOutcome.ApprovalID != "" {
		if _, err := s.Realtime.CreateApprovalNotification(ctx, agentReq, realtime.ApprovalNotificationInput{
			ApprovalRef:       routingOutcome.ApprovalID,
			RouteRef:          routingOutcome.Route.RouteID,
			CapabilityCallRef: routingOutcome.CapabilityCall.CapabilityCallID,
			PolicyDecisionRef: routingOutcome.PolicyDecisionID,
			ScopeRef:          stringValue(routingOutcome.CapabilityCall.ScopeID),
		}); err != nil {
			return AgentToolCallOutcome{}, fmt.Errorf("approval notification failed: %w", err)
		}
	}

	status := agentToolCallStatusFromRouting(routingOutcome)
	toolCall, err := s.insertAgentToolCall(ctx, agentReq, agentToolCallInsert{
		WorkContext:       detail.WorkContext,
		AccessSession:     detail.AccessSession,
		ToolView:          toolView,
		Entry:             entry,
		IdempotencyKey:    input.IdempotencyKey,
		Status:            status,
		RouteID:           routingOutcome.Route.RouteID,
		CapabilityCallID:  routingOutcome.CapabilityCall.CapabilityCallID,
		PolicyDecisionID:  routingOutcome.PolicyDecisionID,
		ApprovalID:        routingOutcome.ApprovalID,
		GrantID:           routingOutcome.GrantID,
		Completed:         agentToolCallTerminal(status),
		Metadata:          input.Metadata,
		OperatingResult:   nil,
		CapabilityAddress: entry.CapabilityAddress,
	})
	if err != nil {
		return AgentToolCallOutcome{}, err
	}

	return AgentToolCallOutcome{
		AgentToolCallID:  toolCall.AgentToolCallID,
		Status:           status,
		ReasonCode:       routingOutcome.ErrorCode,
		SafeExplanation:  routingOutcome.ErrorMessage,
		NextActionHint:   nextActionHint(status),
		AgentToolCall:    toolCall,
		ToolViewEntry:    entry,
		RoutingOutcome:   &routingOutcome,
		ApprovalID:       routingOutcome.ApprovalID,
		PolicyDecisionID: routingOutcome.PolicyDecisionID,
		CapabilityCallID: routingOutcome.CapabilityCall.CapabilityCallID,
		RouteID:          routingOutcome.Route.RouteID,
	}, nil
}

func (s Service) GetAgentToolCall(ctx context.Context, ref string) (AgentToolCall, error) {
	return getAgentToolCall(ctx, s.DB, ref)
}

func (s Service) ListAgentToolCalls(ctx context.Context, filter AgentToolCallFilter) ([]AgentToolCall, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	if filter.Status != "" && !ValidAgentToolCallStatus(filter.Status) {
		return nil, fmt.Errorf("unsupported agent tool call status filter: %s", filter.Status)
	}
	query := agentToolCallSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if filter.WorkContextRef != "" {
		workContextID, err := resolveWorkContextID(ctx, s.DB, filter.WorkContextRef)
		if err != nil {
			return nil, err
		}
		add("agent_work_context_id =", workContextID)
	}
	if filter.Status != "" {
		add("status =", filter.Status)
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	calls := []AgentToolCall{}
	for rows.Next() {
		call, err := scanAgentToolCall(rows)
		if err != nil {
			return nil, err
		}
		calls = append(calls, call)
	}
	return calls, rows.Err()
}

func (s Service) WriteWorklog(ctx context.Context, req requestctx.Context, input WriteWorklogInput) (WorklogEntry, error) {
	input = normalizeWriteWorklogInput(input)
	if input.WorkContextRef == "" {
		return WorklogEntry{}, fmt.Errorf("work_context_ref is required")
	}
	if !ValidWorklogEntryKind(input.EntryKind) {
		return WorklogEntry{}, fmt.Errorf("unsupported worklog entry kind: %s", input.EntryKind)
	}
	if input.Summary == "" {
		return WorklogEntry{}, fmt.Errorf("summary is required")
	}
	if err := validateObjectJSON(input.Metadata, "metadata"); err != nil {
		return WorklogEntry{}, err
	}
	detail, err := s.activeWorkContextDetail(ctx, input.WorkContextRef)
	if err != nil {
		return WorklogEntry{}, err
	}
	req = agentRequestContext(req, detail.WorkContext)
	objectID := ""
	if input.ObjectRef != "" {
		objectID, err = resolveObjectID(ctx, s.DB, input.ObjectRef)
		if err != nil {
			return WorklogEntry{}, err
		}
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return WorklogEntry{}, err
	}
	defer tx.Rollback()

	entry, err := scanWorklogEntry(tx.QueryRowContext(ctx, worklogEntrySelectSQL(`
		INSERT INTO agents.worklog_entries (
			worklog_entry_id, agent_access_session_id, agent_work_context_id,
			actor_id, entry_kind, summary, body, object_id, visibility_class, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, nullif($8, ''), $9, $10)
	`),
		ids.NewWorklogEntryID(),
		detail.AccessSession.AgentAccessSessionID,
		detail.WorkContext.AgentWorkContextID,
		detail.WorkContext.ActorID,
		input.EntryKind,
		input.Summary,
		input.Body,
		objectID,
		input.VisibilityClass,
		objectOrDefault(input.Metadata),
	))
	if err != nil {
		return WorklogEntry{}, err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       events.TypeAgentWorklogWritten,
		EventLevel:      "audit",
		Request:         req,
		ScopeID:         stringValue(detail.WorkContext.CurrentScopeID),
		TargetKind:      "worklog_entry",
		TargetID:        entry.WorklogEntryID,
		Status:          input.EntryKind,
		Result:          "written",
		VisibilityClass: "internal",
		Payload: map[string]any{
			"worklog_entry_id":      entry.WorklogEntryID,
			"agent_work_context_id": entry.AgentWorkContextID,
			"entry_kind":            entry.EntryKind,
			"object_id":             stringValue(entry.ObjectID),
		},
	}); err != nil {
		return WorklogEntry{}, err
	}
	if err := tx.Commit(); err != nil {
		return WorklogEntry{}, err
	}
	return entry, nil
}

func (s Service) ListWorklog(ctx context.Context, filter WorklogFilter) ([]WorklogEntry, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	if filter.WorkContextRef == "" {
		return nil, fmt.Errorf("work_context_ref is required")
	}
	workContextID, err := resolveWorkContextID(ctx, s.DB, filter.WorkContextRef)
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, worklogEntrySelectSQL()+`
		WHERE agent_work_context_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`, workContextID, filter.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []WorklogEntry{}
	for rows.Next() {
		entry, err := scanWorklogEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

type agentToolCallInsert struct {
	WorkContext       WorkContext
	AccessSession     AccessSession
	ToolView          ToolView
	Entry             ToolViewEntry
	IdempotencyKey    string
	Status            string
	RouteID           string
	CapabilityCallID  string
	PolicyDecisionID  string
	ApprovalID        string
	GrantID           string
	Completed         bool
	Metadata          json.RawMessage
	OperatingResult   json.RawMessage
	CapabilityAddress string
}

func (s Service) insertAgentToolCall(ctx context.Context, req requestctx.Context, input agentToolCallInsert) (AgentToolCall, error) {
	var completedAt any
	if input.Completed {
		completedAt = time.Now().UTC()
	}
	metadata := objectOrDefault(input.Metadata)
	if len(strings.TrimSpace(string(input.OperatingResult))) > 0 {
		metadata = mergeObjectMetadata(metadata, map[string]any{"operating_result": json.RawMessage(input.OperatingResult)})
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return AgentToolCall{}, err
	}
	defer tx.Rollback()
	call, err := scanAgentToolCall(tx.QueryRowContext(ctx, agentToolCallSelectSQL(`
		INSERT INTO agents.tool_calls (
			agent_tool_call_id, agent_access_session_id, agent_work_context_id,
			tool_view_id, tool_view_entry_id, actor_id, tool_name,
			capability_endpoint_id, capability_address, route_id, capability_call_id,
			policy_decision_id, approval_id, grant_id, idempotency_key, status,
			completed_at, metadata
		)
		VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			nullif($8, ''), $9, nullif($10, ''), nullif($11, ''),
			nullif($12, ''), nullif($13, ''), nullif($14, ''), $15, $16,
			$17, $18
		)
	`),
		ids.NewAgentToolCallID(),
		input.AccessSession.AgentAccessSessionID,
		input.WorkContext.AgentWorkContextID,
		input.ToolView.ToolViewID,
		input.Entry.ToolViewEntryID,
		input.WorkContext.ActorID,
		input.Entry.ToolName,
		stringValue(input.Entry.CapabilityEndpointID),
		input.CapabilityAddress,
		input.RouteID,
		input.CapabilityCallID,
		input.PolicyDecisionID,
		input.ApprovalID,
		input.GrantID,
		input.IdempotencyKey,
		input.Status,
		completedAt,
		metadata,
	))
	if err != nil {
		return AgentToolCall{}, err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       events.TypeAgentToolCalled,
		EventLevel:      "audit",
		Request:         req,
		ScopeID:         stringValue(input.WorkContext.CurrentScopeID),
		TargetKind:      "agent_tool_call",
		TargetID:        call.AgentToolCallID,
		Status:          call.Status,
		Result:          "called",
		VisibilityClass: "internal",
		Payload: map[string]any{
			"agent_tool_call_id":    call.AgentToolCallID,
			"agent_work_context_id": stringValue(call.AgentWorkContextID),
			"tool_name":             call.ToolName,
			"capability_address":    call.CapabilityAddress,
			"route_id":              stringValue(call.RouteID),
			"capability_call_id":    stringValue(call.CapabilityCallID),
			"policy_decision_id":    stringValue(call.PolicyDecisionID),
			"approval_id":           stringValue(call.ApprovalID),
		},
	}); err != nil {
		return AgentToolCall{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentToolCall{}, err
	}
	return call, nil
}

func (s Service) callOperatingTool(ctx context.Context, req requestctx.Context, detail WorkContextDetail, toolView ToolView, entry ToolViewEntry, input AgentToolCallInput) (AgentToolCallOutcome, error) {
	result, worklogEntryID, err := s.executeOperatingTool(ctx, req, detail, entry, input.Input)
	status := AgentToolCallStatusCompleted
	reason := ""
	explanation := ""
	if err != nil {
		status = AgentToolCallStatusFailed
		reason = "operating_tool.failed"
		explanation = err.Error()
		result = json.RawMessage(fmt.Sprintf(`{"error":%q}`, err.Error()))
	}
	toolCall, insertErr := s.insertAgentToolCall(ctx, req, agentToolCallInsert{
		WorkContext:       detail.WorkContext,
		AccessSession:     detail.AccessSession,
		ToolView:          toolView,
		Entry:             entry,
		IdempotencyKey:    input.IdempotencyKey,
		Status:            status,
		Completed:         true,
		Metadata:          input.Metadata,
		OperatingResult:   result,
		CapabilityAddress: entry.CapabilityAddress,
	})
	if insertErr != nil {
		return AgentToolCallOutcome{}, insertErr
	}
	if err != nil {
		return AgentToolCallOutcome{
			AgentToolCallID: toolCall.AgentToolCallID,
			Status:          status,
			ReasonCode:      reason,
			SafeExplanation: explanation,
			NextActionHint:  nextActionHint(status),
			AgentToolCall:   toolCall,
			ToolViewEntry:   entry,
			OperatingResult: result,
			WorklogEntryID:  worklogEntryID,
		}, nil
	}
	return AgentToolCallOutcome{
		AgentToolCallID: toolCall.AgentToolCallID,
		Status:          status,
		NextActionHint:  nextActionHint(status),
		AgentToolCall:   toolCall,
		ToolViewEntry:   entry,
		OperatingResult: result,
		WorklogEntryID:  worklogEntryID,
	}, nil
}

func (s Service) executeOperatingTool(ctx context.Context, req requestctx.Context, detail WorkContextDetail, entry ToolViewEntry, rawInput json.RawMessage) (json.RawMessage, string, error) {
	switch entry.ToolName {
	case "loom.tool_view.inspect":
		raw, err := json.Marshal(detail.ToolView)
		return raw, "", err
	case "loom.capability.search":
		var input ToolSearchInput
		if err := json.Unmarshal(objectOrDefault(rawInput), &input); err != nil {
			return nil, "", err
		}
		input.WorkContextRef = detail.WorkContext.AgentWorkContextID
		result, err := s.SearchTools(ctx, req, input)
		raw, marshalErr := json.Marshal(result)
		if err != nil {
			return raw, "", err
		}
		return raw, "", marshalErr
	case "loom.capability.inspect":
		var input struct {
			CapabilityRef      string `json:"capability_ref"`
			ToolRef            string `json:"tool_ref"`
			Query              string `json:"query"`
			UsageSectionLabel  string `json:"usage_section_label"`
			MaxSections        int    `json:"max_sections"`
			MaxCharsPerSection int    `json:"max_chars_per_section"`
		}
		if err := json.Unmarshal(objectOrDefault(rawInput), &input); err != nil {
			return nil, "", err
		}
		toolRef := strings.TrimSpace(input.ToolRef)
		if toolRef == "" {
			toolRef = strings.TrimSpace(input.CapabilityRef)
		}
		result, err := s.InspectTool(ctx, req, ToolInspectInput{
			WorkContextRef:     detail.WorkContext.AgentWorkContextID,
			ToolRef:            toolRef,
			Query:              input.Query,
			UsageSectionLabel:  input.UsageSectionLabel,
			MaxSections:        input.MaxSections,
			MaxCharsPerSection: input.MaxCharsPerSection,
		})
		raw, marshalErr := json.Marshal(result)
		if err != nil {
			return raw, "", err
		}
		return raw, "", marshalErr
	case "loom.capability.call":
		var input struct {
			CapabilityRef   string          `json:"capability_ref"`
			ToolRef         string          `json:"tool_ref"`
			Input           json.RawMessage `json:"input"`
			RequestApproval bool            `json:"request_approval"`
			ApprovalReason  string          `json:"approval_reason"`
			IdempotencyKey  string          `json:"idempotency_key"`
			Metadata        json.RawMessage `json:"metadata"`
		}
		if err := json.Unmarshal(objectOrDefault(rawInput), &input); err != nil {
			return nil, "", err
		}
		toolRef := strings.TrimSpace(input.ToolRef)
		if toolRef == "" {
			toolRef = strings.TrimSpace(input.CapabilityRef)
		}
		if toolRef == "" {
			return nil, "", fmt.Errorf("capability_ref is required")
		}
		selected, err := getToolViewEntryForRef(ctx, s.DB, detail.ToolView.ToolView.ToolViewID, toolRef)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, "", ErrToolNotAvailable
			}
			return nil, "", err
		}
		if selected.EntryKind != EntryKindCapability || selected.VisibilityState == VisibilityRedacted {
			return nil, "", ErrToolNotAvailable
		}
		result, err := s.CallTool(ctx, req, AgentToolCallInput{
			WorkContextRef:  detail.WorkContext.AgentWorkContextID,
			ToolRef:         selected.CapabilityAddress,
			Input:           input.Input,
			RequestApproval: input.RequestApproval,
			ApprovalReason:  input.ApprovalReason,
			IdempotencyKey:  input.IdempotencyKey,
			Metadata:        input.Metadata,
		})
		raw, marshalErr := json.Marshal(result)
		if err != nil {
			return raw, "", err
		}
		return raw, "", marshalErr
	case "loom.progress.inspect":
		var input struct {
			Source     string `json:"source"`
			SourceKind string `json:"source_kind"`
			SourceRef  string `json:"source_ref"`
			Limit      int    `json:"limit"`
		}
		if err := json.Unmarshal(objectOrDefault(rawInput), &input); err != nil {
			return nil, "", err
		}
		input.Source = strings.TrimSpace(input.Source)
		if input.Source == "" && strings.TrimSpace(input.SourceKind) != "" && strings.TrimSpace(input.SourceRef) != "" {
			input.Source = strings.TrimSpace(input.SourceKind) + ":" + strings.TrimSpace(input.SourceRef)
		}
		result, err := s.Realtime.GetProgress(ctx, input.Source, input.Limit)
		raw, marshalErr := json.Marshal(result)
		if err != nil {
			return raw, "", err
		}
		return raw, "", marshalErr
	case "loom.worklog.write":
		var worklogInputRaw struct {
			EntryKind       string          `json:"entry_kind"`
			Kind            string          `json:"kind"`
			Summary         string          `json:"summary"`
			Body            string          `json:"body"`
			ObjectRef       string          `json:"object_ref"`
			VisibilityClass string          `json:"visibility_class"`
			Metadata        json.RawMessage `json:"metadata"`
		}
		if err := json.Unmarshal(objectOrDefault(rawInput), &worklogInputRaw); err != nil {
			return nil, "", err
		}
		input := WriteWorklogInput{
			WorkContextRef:  detail.WorkContext.AgentWorkContextID,
			EntryKind:       nonEmpty(worklogInputRaw.EntryKind, worklogInputRaw.Kind),
			Summary:         worklogInputRaw.Summary,
			Body:            worklogInputRaw.Body,
			ObjectRef:       worklogInputRaw.ObjectRef,
			VisibilityClass: worklogInputRaw.VisibilityClass,
			Metadata:        worklogInputRaw.Metadata,
		}
		result, err := s.WriteWorklog(ctx, req, input)
		raw, marshalErr := json.Marshal(result)
		if err != nil {
			return raw, "", err
		}
		return raw, result.WorklogEntryID, marshalErr
	default:
		return nil, "", fmt.Errorf("operating tool handler is not implemented: %s", entry.ToolName)
	}
}

func normalizeAgentToolCallInput(input AgentToolCallInput) AgentToolCallInput {
	input.WorkContextRef = strings.TrimSpace(input.WorkContextRef)
	input.ToolRef = strings.TrimSpace(input.ToolRef)
	input.ApprovalReason = strings.TrimSpace(input.ApprovalReason)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if input.IdempotencyKey == "" {
		input.IdempotencyKey = ids.NewAgentToolCallID()
	}
	input.Input = objectOrDefault(input.Input)
	input.Metadata = objectOrDefault(input.Metadata)
	return input
}

func normalizeWriteWorklogInput(input WriteWorklogInput) WriteWorklogInput {
	input.WorkContextRef = strings.TrimSpace(input.WorkContextRef)
	input.EntryKind = defaultString(strings.TrimSpace(input.EntryKind), WorklogEntryKindNote)
	input.Summary = strings.TrimSpace(input.Summary)
	input.ObjectRef = strings.TrimSpace(input.ObjectRef)
	input.VisibilityClass = defaultString(strings.TrimSpace(input.VisibilityClass), "internal")
	input.Metadata = objectOrDefault(input.Metadata)
	return input
}

func agentRequestContext(req requestctx.Context, workContext WorkContext) requestctx.Context {
	req.ActorID = workContext.ActorID
	if runtimeNodeID := stringValue(workContext.RuntimeNodeID); runtimeNodeID != "" {
		req.OriginNodeID = runtimeNodeID
	}
	if scopeID := stringValue(workContext.CurrentScopeID); scopeID != "" {
		req.ScopeID = scopeID
	}
	req.Source = "agent-access-context"
	return req
}

func agentToolCallStatusFromRouting(outcome routing.CapabilityCallOutcome) string {
	switch outcome.Status {
	case routing.CapabilityCallStatusApprovalRequired:
		return AgentToolCallStatusApprovalRequired
	case routing.CapabilityCallStatusCompleted:
		return AgentToolCallStatusCompleted
	case routing.CapabilityCallStatusFailed, routing.CapabilityCallStatusCancelled:
		return AgentToolCallStatusFailed
	case routing.CapabilityCallStatusPlanned:
		return AgentToolCallStatusPlanned
	default:
		return AgentToolCallStatusDispatched
	}
}

func agentToolCallTerminal(status string) bool {
	switch status {
	case AgentToolCallStatusApprovalRequired, AgentToolCallStatusCompleted, AgentToolCallStatusFailed, AgentToolCallStatusDenied:
		return true
	default:
		return false
	}
}

func nextActionHint(status string) string {
	switch status {
	case AgentToolCallStatusApprovalRequired:
		return "wait_for_approval_or_owner_decision"
	case AgentToolCallStatusCompleted:
		return "inspect_result_or_continue"
	case AgentToolCallStatusFailed:
		return "inspect_error_or_choose_another_tool"
	default:
		return "inspect_tool_call_for_updates"
	}
}

func agentToolCallMetadata(metadata json.RawMessage, workContext WorkContext, entry ToolViewEntry) json.RawMessage {
	return mergeObjectMetadata(metadata, map[string]any{
		"agent_work_context_id": workContext.AgentWorkContextID,
		"tool_view_entry_id":    entry.ToolViewEntryID,
		"tool_name":             entry.ToolName,
	})
}

func mergeObjectMetadata(raw json.RawMessage, additions map[string]any) json.RawMessage {
	base := map[string]any{}
	_ = json.Unmarshal(objectOrDefault(raw), &base)
	for key, value := range additions {
		base[key] = value
	}
	out, err := json.Marshal(base)
	if err != nil {
		return objectOrDefault(raw)
	}
	return out
}

func (s Service) agentToolCallOutcomeFromRecord(ctx context.Context, call AgentToolCall, entry ToolViewEntry) AgentToolCallOutcome {
	out := AgentToolCallOutcome{
		AgentToolCallID:  call.AgentToolCallID,
		Status:           call.Status,
		NextActionHint:   nextActionHint(call.Status),
		AgentToolCall:    call,
		ToolViewEntry:    entry,
		ApprovalID:       stringValue(call.ApprovalID),
		PolicyDecisionID: stringValue(call.PolicyDecisionID),
		CapabilityCallID: stringValue(call.CapabilityCallID),
		RouteID:          stringValue(call.RouteID),
	}
	if call.CapabilityCallID != nil && *call.CapabilityCallID != "" {
		if routingCall, err := s.Routing.GetCapabilityCall(ctx, *call.CapabilityCallID); err == nil {
			out.ReasonCode = stringValue(routingCall.ErrorCode)
			out.SafeExplanation = stringValue(routingCall.ErrorMessage)
		}
	}
	return out
}

func getAgentToolCallByIdempotency(ctx context.Context, q queryer, workContextID, toolName, idempotencyKey string) (AgentToolCall, bool, error) {
	call, err := scanAgentToolCall(q.QueryRowContext(ctx, agentToolCallSelectSQL()+`
		WHERE agent_work_context_id = $1
		  AND tool_name = $2
		  AND idempotency_key = $3
	`, workContextID, toolName, idempotencyKey))
	if err == sql.ErrNoRows {
		return AgentToolCall{}, false, nil
	}
	if err != nil {
		return AgentToolCall{}, false, err
	}
	return call, true, nil
}

func getAgentToolCall(ctx context.Context, q queryer, ref string) (AgentToolCall, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return AgentToolCall{}, fmt.Errorf("agent tool call ref is required")
	}
	return scanAgentToolCall(q.QueryRowContext(ctx, agentToolCallSelectSQL()+`
		WHERE agent_tool_call_id = $1
	`, ref))
}

func resolveWorkContextID(ctx context.Context, q queryer, ref string) (string, error) {
	return resolveSingleID(ctx, q, `
		SELECT agent_work_context_id
		FROM agents.agent_work_contexts
		WHERE agent_work_context_id = $1 OR work_context_key = $1
		ORDER BY agent_work_context_id
		LIMIT 2
	`, "agent work context", ref)
}

func resolveObjectID(ctx context.Context, q queryer, ref string) (string, error) {
	return resolveSingleID(ctx, q, `
		SELECT object_id
		FROM objects.objects
		WHERE object_id = $1 OR slug = $1
		ORDER BY object_id
		LIMIT 2
	`, "object", ref)
}

func agentToolCallSelectSQL(prefix ...string) string {
	columns := `
		agent_tool_call_id, agent_access_session_id, agent_work_context_id,
		tool_view_id, tool_view_entry_id, actor_id, tool_name,
		capability_endpoint_id, capability_address, route_id, capability_call_id,
		policy_decision_id, approval_id, grant_id, idempotency_key, status,
		created_at, completed_at, metadata
	`
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + ` RETURNING ` + columns
	}
	return `SELECT ` + columns + ` FROM agents.tool_calls`
}

func worklogEntrySelectSQL(prefix ...string) string {
	columns := `
		worklog_entry_id, agent_access_session_id, agent_work_context_id,
		actor_id, entry_kind, summary, body, object_id, visibility_class,
		created_at, metadata
	`
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + ` RETURNING ` + columns
	}
	return `SELECT ` + columns + ` FROM agents.worklog_entries`
}

func scanAgentToolCall(scanner rowScanner) (AgentToolCall, error) {
	var call AgentToolCall
	var sessionID, workContextID, toolViewID, entryID, endpointID sql.NullString
	var routeID, capabilityCallID, policyDecisionID, approvalID, grantID sql.NullString
	var completedAt sql.NullTime
	var metadata []byte
	if err := scanner.Scan(
		&call.AgentToolCallID,
		&sessionID,
		&workContextID,
		&toolViewID,
		&entryID,
		&call.ActorID,
		&call.ToolName,
		&endpointID,
		&call.CapabilityAddress,
		&routeID,
		&capabilityCallID,
		&policyDecisionID,
		&approvalID,
		&grantID,
		&call.IdempotencyKey,
		&call.Status,
		&call.CreatedAt,
		&completedAt,
		&metadata,
	); err != nil {
		return AgentToolCall{}, err
	}
	call.AgentAccessSessionID = nullableString(sessionID)
	call.AgentWorkContextID = nullableString(workContextID)
	call.ToolViewID = nullableString(toolViewID)
	call.ToolViewEntryID = nullableString(entryID)
	call.CapabilityEndpointID = nullableString(endpointID)
	call.RouteID = nullableString(routeID)
	call.CapabilityCallID = nullableString(capabilityCallID)
	call.PolicyDecisionID = nullableString(policyDecisionID)
	call.ApprovalID = nullableString(approvalID)
	call.GrantID = nullableString(grantID)
	call.CompletedAt = nullableTime(completedAt)
	call.Metadata = jsonOrDefault(metadata, `{}`)
	return call, nil
}

func scanWorklogEntry(scanner rowScanner) (WorklogEntry, error) {
	var entry WorklogEntry
	var sessionID, objectID sql.NullString
	var metadata []byte
	if err := scanner.Scan(
		&entry.WorklogEntryID,
		&sessionID,
		&entry.AgentWorkContextID,
		&entry.ActorID,
		&entry.EntryKind,
		&entry.Summary,
		&entry.Body,
		&objectID,
		&entry.VisibilityClass,
		&entry.CreatedAt,
		&metadata,
	); err != nil {
		return WorklogEntry{}, err
	}
	entry.AgentAccessSessionID = nullableString(sessionID)
	entry.ObjectID = nullableString(objectID)
	entry.Metadata = jsonOrDefault(metadata, `{}`)
	return entry, nil
}
