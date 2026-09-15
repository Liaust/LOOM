package portal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/routing"
	loomsync "loom.local/loom/internal/sync"
)

func (e ActionExecutor) executeScheduleInspect(ctx context.Context, action PortalAction) PortalActionResult {
	target := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if target == "" {
		return failedActionResult(action, "portal.action_target_missing", "Schedule inspect action is missing a schedule target.")
	}
	envelope, err := e.Client.GetSchedule(ctx, e.CorrelationID, target)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	schedule := envelope.Data.Schedule
	automationRecord := envelope.Data.Automation
	fields := []ActionResultField{
		{Label: "Schedule", Value: firstNonEmpty(schedule.DisplayName, schedule.ScheduleKey, schedule.ScheduleID, target)},
		{Label: "Status", Value: firstNonEmpty(schedule.Status, "-")},
		{Label: "Kind", Value: firstNonEmpty(schedule.ScheduleKind, "-")},
		{Label: "Expression", Value: firstNonEmpty(schedule.ScheduleExpr, "-")},
		{Label: "Timezone", Value: firstNonEmpty(schedule.Timezone, "-")},
		{Label: "Next Fire", Value: timePtrOrDash(schedule.NextFireAt)},
		{Label: "Last Fire", Value: timePtrOrDash(schedule.LastFireAt)},
		{Label: "Last Fire ID", Value: stringPtrOrDash(schedule.LastScheduleFireID)},
		{Label: "Automation", Value: firstNonEmpty(automationRecord.DisplayName, automationRecord.AutomationKey, automationRecord.AutomationID, schedule.AutomationID, "-")},
		{Label: "Run As", Value: firstNonEmpty(schedule.RunAsActorID, "-")},
		{Label: "Scope", Value: stringPtrOrDash(schedule.ScopeID)},
		{Label: "Project", Value: stringPtrOrDash(schedule.ProjectID)},
	}
	appendJSONField := func(label string, raw json.RawMessage) {
		if compact := compactJSON(raw); compact != "-" {
			fields = append(fields, ActionResultField{Label: label, Value: compact})
		}
	}
	appendJSONField("Target Profile", schedule.TargetProfileJSON)
	appendJSONField("Misfire Policy", schedule.MisfireProfileJSON)
	appendJSONField("Concurrency Policy", schedule.ConcurrencyProfileJSON)
	appendJSONField("Approval Policy", schedule.ApprovalProfileJSON)
	appendJSONField("Timeout Policy", schedule.TimeoutProfileJSON)
	appendJSONField("Retry Policy", schedule.RetryProfileJSON)
	return successActionResult(action, envelope.Meta.CorrelationID, "Schedule detail loaded.", fields)
}

func (e ActionExecutor) executeScheduleFireNow(ctx context.Context, action PortalAction) PortalActionResult {
	target := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if target == "" {
		return failedActionResult(action, "portal.action_target_missing", "Schedule fire action is missing a schedule target.")
	}
	envelope, err := e.Client.FireScheduleNow(ctx, e.CorrelationID, target, automation.FireScheduleInput{
		Reason:      "portal manual fire",
		Metadata:    portalActionMetadata(action),
		DispatchNow: true,
	})
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	result := envelope.Data
	fields := []ActionResultField{
		{Label: "Schedule", Value: firstNonEmpty(result.Schedule.DisplayName, result.Schedule.ScheduleKey, result.Schedule.ScheduleID, target)},
		{Label: "Schedule Status", Value: firstNonEmpty(result.Schedule.Status, "-")},
		{Label: "Fire", Value: firstNonEmpty(result.Fire.ScheduleFireID, "-")},
		{Label: "Fire Status", Value: firstNonEmpty(result.Fire.Status, "-")},
		{Label: "Scheduled For", Value: timeOrDash(result.Fire.ScheduledFor)},
		{Label: "Invocation", Value: firstNonEmpty(result.Invocation.InvocationID, stringPtrOrDash(result.Fire.InvocationID))},
		{Label: "Invocation Status", Value: firstNonEmpty(result.Invocation.Status, "-")},
		{Label: "Target Capability", Value: firstNonEmpty(result.Invocation.TargetCapability, "-")},
		{Label: "Route", Value: stringPtrOrDash(result.Fire.RouteID)},
		{Label: "Capability Call", Value: stringPtrOrDash(result.Fire.CapabilityCallID)},
		{Label: "Job", Value: stringPtrOrDash(result.Fire.JobID)},
	}
	if result.Dispatch != nil {
		fields = append(fields, ActionResultField{Label: "Dispatch", Value: fmt.Sprintf("claimed=%d succeeded=%d failed=%d manual_action=%d", result.Dispatch.Claimed, result.Dispatch.Succeeded, result.Dispatch.Failed, result.Dispatch.ManualAction)})
	}
	if result.DispatchError != "" {
		fields = append(fields, ActionResultField{Label: "Dispatch Error", Value: result.DispatchError})
	}
	return successActionResult(action, envelope.Meta.CorrelationID, "Schedule fire was requested.", fields)
}

func (e ActionExecutor) executeSchedulePause(ctx context.Context, action PortalAction) PortalActionResult {
	return e.executeScheduleStatusUpdate(ctx, action, "pause")
}

func (e ActionExecutor) executeScheduleResume(ctx context.Context, action PortalAction) PortalActionResult {
	return e.executeScheduleStatusUpdate(ctx, action, "resume")
}

func (e ActionExecutor) executeScheduleStatusUpdate(ctx context.Context, action PortalAction, update string) PortalActionResult {
	target := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if target == "" {
		return failedActionResult(action, "portal.action_target_missing", "Schedule status action is missing a schedule target.")
	}
	input := automation.UpdateScheduleStatusInput{Reason: "portal " + update, Metadata: portalActionMetadata(action)}
	var (
		envelope response.Envelope[automation.ScheduleDetail]
		err      error
	)
	switch update {
	case "pause":
		envelope, err = e.Client.PauseSchedule(ctx, e.CorrelationID, target, input)
	case "resume":
		envelope, err = e.Client.ResumeSchedule(ctx, e.CorrelationID, target, input)
	default:
		return failedActionResult(action, "portal.action_invalid", "Unsupported schedule status update.")
	}
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	schedule := envelope.Data.Schedule
	fields := []ActionResultField{
		{Label: "Schedule", Value: firstNonEmpty(schedule.DisplayName, schedule.ScheduleKey, schedule.ScheduleID, target)},
		{Label: "Status", Value: firstNonEmpty(schedule.Status, "-")},
		{Label: "Next Fire", Value: timePtrOrDash(schedule.NextFireAt)},
		{Label: "Automation", Value: firstNonEmpty(envelope.Data.Automation.DisplayName, envelope.Data.Automation.AutomationKey, envelope.Data.Automation.AutomationID, "-")},
	}
	return successActionResult(action, envelope.Meta.CorrelationID, "Schedule "+update+" completed.", fields)
}

func (e ActionExecutor) executeScheduleFireInspect(ctx context.Context, action PortalAction) PortalActionResult {
	target := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if target == "" {
		return failedActionResult(action, "portal.action_target_missing", "Schedule fire inspect action is missing a fire target.")
	}
	envelope, err := e.Client.GetScheduleFire(ctx, e.CorrelationID, target)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	fire := envelope.Data
	fields := []ActionResultField{
		{Label: "Fire", Value: firstNonEmpty(fire.ScheduleFireID, target)},
		{Label: "Schedule", Value: firstNonEmpty(fire.ScheduleID, "-")},
		{Label: "Automation", Value: firstNonEmpty(fire.AutomationID, "-")},
		{Label: "Status", Value: firstNonEmpty(fire.Status, "-")},
		{Label: "Misfire", Value: firstNonEmpty(fire.MisfireStatus, "-")},
		{Label: "Scheduled For", Value: timeOrDash(fire.ScheduledFor)},
		{Label: "Lateness Seconds", Value: fmt.Sprintf("%d", fire.LatenessSeconds)},
		{Label: "Worker Run", Value: stringPtrOrDash(fire.WorkerRunID)},
		{Label: "Invocation", Value: stringPtrOrDash(fire.InvocationID)},
		{Label: "Route", Value: stringPtrOrDash(fire.RouteID)},
		{Label: "Capability Call", Value: stringPtrOrDash(fire.CapabilityCallID)},
		{Label: "Job", Value: stringPtrOrDash(fire.JobID)},
		{Label: "Failure", Value: firstNonEmpty(stringPtrOrDash(fire.FailureCode), stringPtrOrDash(fire.FailureMessage), "-")},
	}
	return successActionResult(action, envelope.Meta.CorrelationID, "Schedule fire detail loaded.", fields)
}

func (e ActionExecutor) executeDirectEventEndpointInspect(ctx context.Context, action PortalAction) PortalActionResult {
	target := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if target == "" {
		return failedActionResult(action, "portal.action_target_missing", "Endpoint inspect action is missing an endpoint target.")
	}
	envelope, err := e.Client.GetDirectEventEndpoint(ctx, e.CorrelationID, target)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	endpoint := envelope.Data.Endpoint
	fields := []ActionResultField{
		{Label: "Endpoint", Value: firstNonEmpty(endpoint.DisplayName, endpoint.EndpointSlug, endpoint.EndpointID, target)},
		{Label: "Status", Value: firstNonEmpty(endpoint.Status, "-")},
		{Label: "Path", Value: firstNonEmpty(endpoint.EndpointPath, "-")},
		{Label: "Event Type", Value: firstNonEmpty(endpoint.EventType, "-")},
		{Label: "Response Mode", Value: firstNonEmpty(endpoint.ResponseMode, "-")},
		{Label: "Integration", Value: firstNonEmpty(envelope.Data.Integration.DisplayName, envelope.Data.Integration.IntegrationKey, envelope.Data.Integration.IntegrationID, endpoint.IntegrationID, "-")},
		{Label: "Automation", Value: firstNonEmpty(envelope.Data.Automation.DisplayName, envelope.Data.Automation.AutomationKey, envelope.Data.Automation.AutomationID, endpoint.AutomationID, "-")},
	}
	return successActionResult(action, envelope.Meta.CorrelationID, "Direct event endpoint detail loaded.", fields)
}

func (e ActionExecutor) executeDirectEventInspect(ctx context.Context, action PortalAction) PortalActionResult {
	target := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if target == "" {
		return failedActionResult(action, "portal.action_target_missing", "Direct event inspect action is missing an event target.")
	}
	envelope, err := e.Client.GetDirectEvent(ctx, e.CorrelationID, target)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	event := envelope.Data.DirectEvent
	fields := []ActionResultField{
		{Label: "Direct Event", Value: firstNonEmpty(event.DirectEventID, target)},
		{Label: "Status", Value: firstNonEmpty(event.Status, "-")},
		{Label: "External Event", Value: firstNonEmpty(event.ExternalEventID, "-")},
		{Label: "Endpoint", Value: firstNonEmpty(envelope.Data.Endpoint.DisplayName, envelope.Data.Endpoint.EndpointSlug, envelope.Data.Endpoint.EndpointID, event.EndpointID, "-")},
		{Label: "Integration", Value: firstNonEmpty(envelope.Data.Integration.DisplayName, envelope.Data.Integration.IntegrationKey, envelope.Data.Integration.IntegrationID, event.IntegrationID, "-")},
		{Label: "Automation", Value: firstNonEmpty(envelope.Data.Automation.DisplayName, envelope.Data.Automation.AutomationKey, envelope.Data.Automation.AutomationID, event.AutomationID, "-")},
		{Label: "Path", Value: firstNonEmpty(event.RequestPath, "-")},
		{Label: "Payload Hash", Value: firstNonEmpty(event.PayloadHash, "-")},
		{Label: "Attempts", Value: fmt.Sprintf("%d", event.AttemptCount)},
		{Label: "Invocation", Value: stringPtrOrDash(event.InvocationID)},
		{Label: "Route", Value: stringPtrOrDash(event.RouteID)},
		{Label: "Capability Call", Value: stringPtrOrDash(event.CapabilityCallID)},
		{Label: "Job", Value: stringPtrOrDash(event.JobID)},
		{Label: "Failure", Value: firstNonEmpty(stringPtrOrDash(event.FailureCode), stringPtrOrDash(event.FailureMessage), "-")},
	}
	if envelope.Data.Invocation != nil {
		fields = append(fields, ActionResultField{Label: "Invocation Status", Value: firstNonEmpty(envelope.Data.Invocation.Status, "-")})
	}
	return successActionResult(action, envelope.Meta.CorrelationID, "Direct event detail loaded.", fields)
}

func (e ActionExecutor) executeDirectEventRawPayload(ctx context.Context, action PortalAction) PortalActionResult {
	target := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if target == "" {
		return failedActionResult(action, "portal.action_target_missing", "Direct event raw payload action is missing an event target.")
	}
	envelope, err := e.Client.GetDirectEventRawPayload(ctx, e.CorrelationID, target)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	payload := envelope.Data
	fields := []ActionResultField{
		{Label: "Direct Event", Value: firstNonEmpty(payload.DirectEventID, target)},
		{Label: "Payload Hash", Value: firstNonEmpty(payload.PayloadHash, "-")},
		{Label: "Received", Value: timeOrDash(payload.ReceivedAt)},
		{Label: "Headers", Value: compactJSON(payload.HeadersJSON)},
		{Label: "Query", Value: compactJSON(payload.QueryJSON)},
		{Label: "Body", Value: compactJSON(payload.BodyJSON)},
	}
	raw, _ := json.Marshal(payload)
	result := successActionResult(action, envelope.Meta.CorrelationID, "Direct event raw payload loaded.", fields)
	result.RawResponse = string(raw)
	return result
}

func (e ActionExecutor) executeInvocationInspect(ctx context.Context, action PortalAction) PortalActionResult {
	target := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if target == "" {
		return failedActionResult(action, "portal.action_target_missing", "Invocation inspect action is missing an invocation target.")
	}
	envelope, err := e.Client.GetInvocation(ctx, e.CorrelationID, target)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	invocation := envelope.Data
	fields := []ActionResultField{
		{Label: "Invocation", Value: firstNonEmpty(invocation.InvocationID, target)},
		{Label: "Status", Value: firstNonEmpty(invocation.Status, "-")},
		{Label: "Source", Value: strings.TrimSpace(firstNonEmpty(invocation.SourceKind, "-") + " " + firstNonEmpty(invocation.SourceOccurrenceRef, invocation.SourceRef))},
		{Label: "Target Capability", Value: firstNonEmpty(invocation.TargetCapability, "-")},
		{Label: "Attempts", Value: fmt.Sprintf("%d/%d", invocation.AttemptCount, invocation.MaxAttempts)},
		{Label: "Next Attempt", Value: timePtrOrDash(invocation.NextAttemptAt)},
		{Label: "Route", Value: stringPtrOrDash(invocation.RouteID)},
		{Label: "Capability Call", Value: stringPtrOrDash(invocation.CapabilityCallID)},
		{Label: "Job", Value: stringPtrOrDash(invocation.JobID)},
		{Label: "Approval", Value: stringPtrOrDash(invocation.ApprovalID)},
		{Label: "Failure", Value: firstNonEmpty(stringPtrOrDash(invocation.FailureCode), stringPtrOrDash(invocation.FailureMessage), "-")},
	}
	return successActionResult(action, envelope.Meta.CorrelationID, "Invocation detail loaded.", fields)
}

func (e ActionExecutor) executeProviderInspect(ctx context.Context, action PortalAction) PortalActionResult {
	target := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if target == "" {
		return failedActionResult(action, "portal.action_target_missing", "Provider inspect action is missing a provider target.")
	}
	envelope, err := e.Client.GetProvider(ctx, e.CorrelationID, target)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	provider := envelope.Data.Provider
	fields := []ActionResultField{
		{Label: "Provider", Value: firstNonEmpty(provider.DisplayName, provider.CompactAddress, provider.ProviderKey, provider.ProviderID, target)},
		{Label: "Status", Value: firstNonEmpty(provider.Status, "-")},
		{Label: "Type", Value: firstNonEmpty(provider.ProviderType, "-")},
		{Label: "Node", Value: firstNonEmpty(provider.NodeID, "-")},
		{Label: "Scope", Value: firstNonEmpty(provider.ScopeID, "-")},
		{Label: "Version", Value: firstNonEmpty(provider.Version, "-")},
		{Label: "Endpoints", Value: fmt.Sprintf("%d", len(envelope.Data.Endpoints))},
		{Label: "Usage Docs", Value: fmt.Sprintf("%d", len(envelope.Data.UsageDocuments))},
	}
	if envelope.Data.Health != nil {
		fields = append(fields,
			ActionResultField{Label: "Health", Value: firstNonEmpty(envelope.Data.Health.HealthStatus, "-")},
			ActionResultField{Label: "Availability", Value: firstNonEmpty(envelope.Data.Health.AvailabilityStatus, "-")},
			ActionResultField{Label: "Health Message", Value: firstNonEmpty(envelope.Data.Health.Message, "-")},
		)
	}
	return successActionResult(action, envelope.Meta.CorrelationID, "Provider detail loaded.", fields)
}

func (e ActionExecutor) executeProviderHealth(ctx context.Context, action PortalAction) PortalActionResult {
	target := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if target == "" {
		return failedActionResult(action, "portal.action_target_missing", "Provider health action is missing a provider target.")
	}
	envelope, err := e.Client.GetProviderHealth(ctx, e.CorrelationID, target)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	health := envelope.Data
	fields := []ActionResultField{
		{Label: "Provider", Value: firstNonEmpty(health.ProviderID, target)},
		{Label: "Health", Value: firstNonEmpty(health.HealthStatus, "-")},
		{Label: "Availability", Value: firstNonEmpty(health.AvailabilityStatus, "-")},
		{Label: "Checked", Value: timePtrOrDash(health.LastCheckedAt)},
		{Label: "Last OK", Value: timePtrOrDash(health.LastOKAt)},
		{Label: "Message", Value: firstNonEmpty(health.Message, "-")},
	}
	return successActionResult(action, envelope.Meta.CorrelationID, "Provider health loaded.", fields)
}

func (e ActionExecutor) executeProviderAdvertisementInspect(ctx context.Context, action PortalAction) PortalActionResult {
	target := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if target == "" {
		return failedActionResult(action, "portal.action_target_missing", "Provider advertisement inspect action is missing a target.")
	}
	envelope, err := e.Client.GetProviderAdvertisement(ctx, e.CorrelationID, target)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	ad := envelope.Data.Advertisement
	fields := []ActionResultField{
		{Label: "Advertisement", Value: firstNonEmpty(ad.ProviderAdvertisementID, target)},
		{Label: "Status", Value: firstNonEmpty(ad.Status, "-")},
		{Label: "Origin Node", Value: firstNonEmpty(ad.OriginNodeID, "-")},
		{Label: "Provider", Value: stringPtrOrDash(ad.ProviderID)},
		{Label: "Endpoints", Value: fmt.Sprintf("%d", len(envelope.Data.Endpoints))},
		{Label: "Usage Docs", Value: fmt.Sprintf("%d", len(envelope.Data.UsageDocuments))},
		{Label: "Received", Value: timeOrDash(ad.ReceivedAt)},
		{Label: "Validated", Value: timePtrOrDash(ad.ValidatedAt)},
		{Label: "Reviewed", Value: timePtrOrDash(ad.ReviewedAt)},
		{Label: "Rejection Reason", Value: firstNonEmpty(ad.RejectionReason, "-")},
	}
	return successActionResult(action, envelope.Meta.CorrelationID, "Provider advertisement detail loaded.", fields)
}

func (e ActionExecutor) executeCapabilityInspect(ctx context.Context, action PortalAction) PortalActionResult {
	target := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if target == "" {
		return failedActionResult(action, "portal.action_target_missing", "Capability inspect action is missing a capability target.")
	}
	envelope, err := e.Client.GetCapability(ctx, e.CorrelationID, target)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	detail := envelope.Data
	endpoint := detail.Endpoint
	fields := []ActionResultField{
		{Label: "Capability", Value: firstNonEmpty(endpoint.CompactAddress, endpoint.EndpointName, endpoint.CapabilityEndpointID, target)},
		{Label: "Status", Value: firstNonEmpty(endpoint.Status, "-")},
		{Label: "Form", Value: firstNonEmpty(endpoint.Form, "-")},
		{Label: "Risk", Value: firstNonEmpty(endpoint.RiskLevel, "-")},
		{Label: "Auth Level", Value: fmt.Sprintf("%d", endpoint.ExecutionAuthorizationLevel)},
		{Label: "Class", Value: firstNonEmpty(detail.Class.DisplayName, detail.Class.Name, detail.Class.CapabilityClassID, "-")},
		{Label: "Provider", Value: firstNonEmpty(detail.Provider.DisplayName, detail.Provider.CompactAddress, detail.Provider.ProviderKey, detail.Provider.ProviderID, "-")},
		{Label: "Active Version", Value: activeVersionLabel(detail.ActiveVersion)},
		{Label: "Usage Docs", Value: fmt.Sprintf("%d", len(detail.UsageDocuments))},
		{Label: "Input Schema", Value: schemaSummary(endpoint.InputSchemaJSON)},
		{Label: "Output Schema", Value: schemaSummary(endpoint.OutputSchemaJSON)},
	}
	if detail.ProviderHealth != nil {
		fields = append(fields, ActionResultField{Label: "Provider Health", Value: firstNonEmpty(detail.ProviderHealth.HealthStatus, "-")})
	}
	return successActionResult(action, envelope.Meta.CorrelationID, "Capability detail loaded.", fields)
}

func (e ActionExecutor) executeCapabilityUsageDocs(ctx context.Context, action PortalAction) PortalActionResult {
	target := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if target == "" {
		return failedActionResult(action, "portal.action_target_missing", "Usage docs action is missing a capability target.")
	}
	envelope, err := e.Client.GetCapabilityUsageDocs(ctx, e.CorrelationID, target)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	fields := []ActionResultField{{Label: "Documents", Value: fmt.Sprintf("%d", len(envelope.Data))}}
	for idx, doc := range envelope.Data {
		if idx >= 5 {
			break
		}
		fields = append(fields,
			ActionResultField{Label: fmt.Sprintf("Doc %d", idx+1), Value: firstNonEmpty(doc.Title, doc.CapabilityUsageDocumentID)},
			ActionResultField{Label: fmt.Sprintf("Doc %d Format", idx+1), Value: firstNonEmpty(doc.BodyFormat, "-")},
			ActionResultField{Label: fmt.Sprintf("Doc %d Status", idx+1), Value: firstNonEmpty(doc.ReviewStatus, "-")},
		)
		if strings.TrimSpace(doc.Body) != "" {
			fields = append(fields, ActionResultField{Label: fmt.Sprintf("Doc %d Preview", idx+1), Value: trimForWidth(doc.Body, 140)})
		}
	}
	return successActionResult(action, envelope.Meta.CorrelationID, "Capability usage docs loaded.", fields)
}

func (e ActionExecutor) executeCapabilityCallInspect(ctx context.Context, action PortalAction) PortalActionResult {
	target := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if target == "" {
		return failedActionResult(action, "portal.action_target_missing", "Capability call inspect action is missing a call target.")
	}
	envelope, err := e.Client.GetCapabilityCall(ctx, e.CorrelationID, target)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	return successActionResult(action, envelope.Meta.CorrelationID, "Capability call detail loaded.", capabilityCallResultFields(envelope.Data))
}

func (e ActionExecutor) executeCapabilityCall(ctx context.Context, action PortalAction) PortalActionResult {
	target := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if target == "" {
		return failedActionResult(action, "portal.action_target_missing", "Capability call action is missing a target.")
	}
	inputJSON := strings.TrimSpace(firstNonEmpty(action.InputValues["input_json"], "{}"))
	if inputJSON == "" {
		inputJSON = "{}"
	}
	envelope, err := e.Client.CallCapability(ctx, e.CorrelationID, routing.CapabilityCallInput{
		Target:   target,
		Input:    json.RawMessage(inputJSON),
		Metadata: portalActionMetadata(action),
	})
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	outcome := envelope.Data
	fields := capabilityCallResultFields(outcome.CapabilityCall)
	fields = append(fields,
		ActionResultField{Label: "Outcome Status", Value: firstNonEmpty(outcome.Status, "-")},
		ActionResultField{Label: "Route", Value: firstNonEmpty(outcome.Route.RouteID, outcome.CapabilityCall.RouteID, "-")},
		ActionResultField{Label: "Job", Value: firstNonEmpty(outcome.JobID, stringPtrOrDash(outcome.CapabilityCall.JobID))},
		ActionResultField{Label: "Policy Decision", Value: firstNonEmpty(outcome.PolicyDecisionID, stringPtrOrDash(outcome.CapabilityCall.PolicyDecisionID))},
		ActionResultField{Label: "Approval", Value: firstNonEmpty(outcome.ApprovalID, stringPtrOrDash(outcome.CapabilityCall.ApprovalID))},
	)
	if outcome.ErrorCode != "" || outcome.ErrorMessage != "" {
		fields = append(fields, ActionResultField{Label: "Error", Value: strings.TrimSpace(outcome.ErrorCode + " " + outcome.ErrorMessage)})
	}
	result := successActionResult(action, envelope.Meta.CorrelationID, "Capability call was requested.", fields)
	raw, _ := json.Marshal(outcome)
	result.RawResponse = string(raw)
	return result
}

func (e ActionExecutor) executeDeletionRequestInspect(ctx context.Context, action PortalAction) PortalActionResult {
	target := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if target == "" {
		return failedActionResult(action, "portal.action_target_missing", "Deletion request inspect action is missing a request target.")
	}
	envelope, err := e.Client.GetDeletionRequest(ctx, e.CorrelationID, target)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	return successActionResult(action, envelope.Meta.CorrelationID, "Deletion request loaded.", deletionRequestResultFields(envelope.Data))
}

func (e ActionExecutor) executeDeletionRequestReview(ctx context.Context, action PortalAction) PortalActionResult {
	return e.executeDeletionRequestLifecycle(ctx, action, "review", func(input loomsync.DeletionRequestUpdateInput) (response.Envelope[loomsync.DeletionRequest], error) {
		return e.Client.ReviewDeletionRequest(ctx, e.CorrelationID, input)
	})
}

func (e ActionExecutor) executeDeletionRequestApprove(ctx context.Context, action PortalAction) PortalActionResult {
	return e.executeDeletionRequestLifecycle(ctx, action, "approve", func(input loomsync.DeletionRequestUpdateInput) (response.Envelope[loomsync.DeletionRequest], error) {
		return e.Client.ApproveDeletionRequest(ctx, e.CorrelationID, input)
	})
}

func (e ActionExecutor) executeDeletionRequestDeny(ctx context.Context, action PortalAction) PortalActionResult {
	return e.executeDeletionRequestLifecycle(ctx, action, "deny", func(input loomsync.DeletionRequestUpdateInput) (response.Envelope[loomsync.DeletionRequest], error) {
		return e.Client.DenyDeletionRequest(ctx, e.CorrelationID, input)
	})
}

func (e ActionExecutor) executeDeletionRequestComplete(ctx context.Context, action PortalAction) PortalActionResult {
	return e.executeDeletionRequestLifecycle(ctx, action, "complete", func(input loomsync.DeletionRequestUpdateInput) (response.Envelope[loomsync.DeletionRequest], error) {
		return e.Client.CompleteDeletionRequest(ctx, e.CorrelationID, input)
	})
}

func (e ActionExecutor) executeDeletionRequestLifecycle(ctx context.Context, action PortalAction, verb string, call func(loomsync.DeletionRequestUpdateInput) (response.Envelope[loomsync.DeletionRequest], error)) PortalActionResult {
	target := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if target == "" {
		return failedActionResult(action, "portal.action_target_missing", "Deletion request action is missing a request target.")
	}
	input := loomsync.DeletionRequestUpdateInput{RequestRef: target}
	if verb == "deny" {
		input.Reason = "Denied from LOOM Portal."
	}
	envelope, err := call(input)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	return successActionResult(action, envelope.Meta.CorrelationID, "Deletion request "+verb+" completed.", deletionRequestResultFields(envelope.Data))
}

func (e ActionExecutor) executeJobInspect(ctx context.Context, action PortalAction) PortalActionResult {
	target := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if target == "" {
		return failedActionResult(action, "portal.action_target_missing", "Job inspect action is missing a job target.")
	}
	envelope, err := e.Client.GetJob(ctx, e.CorrelationID, target)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	detail := envelope.Data
	fields := jobResultFields(detail.Job)
	fields = append(fields,
		ActionResultField{Label: "Attempts", Value: fmt.Sprintf("%d", len(detail.Attempts))},
		ActionResultField{Label: "Logs", Value: fmt.Sprintf("%d", len(detail.Logs))},
		ActionResultField{Label: "Outputs", Value: fmt.Sprintf("%d", len(detail.Outputs))},
		ActionResultField{Label: "Artifacts", Value: fmt.Sprintf("%d", len(detail.Artifacts))},
	)
	for idx, attempt := range detail.Attempts {
		if idx >= 3 {
			break
		}
		fields = append(fields, ActionResultField{Label: fmt.Sprintf("Attempt %d", attempt.AttemptNumber), Value: firstNonEmpty(attempt.Status, "-") + " " + firstNonEmpty(attempt.WorkdirPath, "")})
	}
	return successActionResult(action, envelope.Meta.CorrelationID, "Job detail loaded.", fields)
}

func (e ActionExecutor) executeJobLogsInspect(ctx context.Context, action PortalAction) PortalActionResult {
	target := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if target == "" {
		return failedActionResult(action, "portal.action_target_missing", "Job logs action is missing a job target.")
	}
	envelope, err := e.Client.GetJobLogs(ctx, e.CorrelationID, target)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	fields := []ActionResultField{{Label: "Job", Value: target}, {Label: "Logs", Value: fmt.Sprintf("%d", len(envelope.Data))}}
	for idx, log := range envelope.Data {
		if idx >= 5 {
			break
		}
		value := fmt.Sprintf("%s %s bytes=%d path=%s", firstNonEmpty(log.Stream, "-"), firstNonEmpty(log.StorageKind, "-"), log.ByteCount, firstNonEmpty(log.Path, "-"))
		if strings.TrimSpace(log.TailText) != "" {
			value += " tail=" + trimForWidth(log.TailText, 100)
		}
		fields = append(fields, ActionResultField{Label: fmt.Sprintf("Log %d", idx+1), Value: value})
	}
	return successActionResult(action, envelope.Meta.CorrelationID, "Job logs loaded.", fields)
}

func (e ActionExecutor) executeJobOutputsInspect(ctx context.Context, action PortalAction) PortalActionResult {
	target := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if target == "" {
		return failedActionResult(action, "portal.action_target_missing", "Job outputs action is missing a job target.")
	}
	envelope, err := e.Client.GetJobOutputs(ctx, e.CorrelationID, target)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	fields := []ActionResultField{{Label: "Job", Value: target}, {Label: "Outputs", Value: fmt.Sprintf("%d", len(envelope.Data))}}
	for idx, output := range envelope.Data {
		if idx >= 5 {
			break
		}
		fields = append(fields, ActionResultField{
			Label: fmt.Sprintf("Output %d", idx+1),
			Value: fmt.Sprintf("%s %s status=%s artifact=%s value=%s", firstNonEmpty(output.OutputKey, output.JobOutputID), firstNonEmpty(output.OutputType, "-"), firstNonEmpty(output.Status, "-"), stringPtrOrDash(output.ArtifactID), compactJSON(output.ValueJSON)),
		})
	}
	return successActionResult(action, envelope.Meta.CorrelationID, "Job outputs loaded.", fields)
}

func (e ActionExecutor) executeJobRetry(ctx context.Context, action PortalAction) PortalActionResult {
	target := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if target == "" {
		return failedActionResult(action, "portal.action_target_missing", "Job retry action is missing a job target.")
	}
	envelope, err := e.Client.RetryJob(ctx, e.CorrelationID, jobs.RetryJobInput{JobRef: target})
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	return successActionResult(action, envelope.Meta.CorrelationID, "Job retry was requested.", jobResultFields(envelope.Data))
}

func (e ActionExecutor) executeJobCancel(ctx context.Context, action PortalAction) PortalActionResult {
	target := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if target == "" {
		return failedActionResult(action, "portal.action_target_missing", "Job cancel action is missing a job target.")
	}
	envelope, err := e.Client.CancelJob(ctx, e.CorrelationID, jobs.CancelJobInput{JobRef: target})
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	return successActionResult(action, envelope.Meta.CorrelationID, "Job cancel was requested.", jobResultFields(envelope.Data))
}

func (e ActionExecutor) executeJobAttentionAcknowledge(ctx context.Context, action PortalAction) PortalActionResult {
	target := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if target == "" {
		return failedActionResult(action, "portal.action_target_missing", "Job attention acknowledgement action is missing a job target.")
	}
	envelope, err := e.Client.AcknowledgeJobAttention(ctx, e.CorrelationID, jobs.JobAttentionInput{JobRef: target})
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	return successActionResult(action, envelope.Meta.CorrelationID, "Job attention was acknowledged.", jobResultFields(envelope.Data))
}

func (e ActionExecutor) executeJobAttentionArchive(ctx context.Context, action PortalAction) PortalActionResult {
	target := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if target == "" {
		return failedActionResult(action, "portal.action_target_missing", "Job attention archive action is missing a job target.")
	}
	envelope, err := e.Client.ArchiveJobAttention(ctx, e.CorrelationID, jobs.JobAttentionInput{JobRef: target})
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	return successActionResult(action, envelope.Meta.CorrelationID, "Job attention was archived.", jobResultFields(envelope.Data))
}

func (e ActionExecutor) executeNodeInspect(ctx context.Context, action PortalAction) PortalActionResult {
	target := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if target == "" {
		return failedActionResult(action, "portal.action_target_missing", "Node inspect action is missing a node target.")
	}
	envelope, err := e.Client.GetNode(ctx, e.CorrelationID, target)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	node := envelope.Data
	fields := []ActionResultField{
		{Label: "Node", Value: firstNonEmpty(node.DisplayName, node.NodeKey, node.NodeID, target)},
		{Label: "Status", Value: firstNonEmpty(node.Status, "-")},
		{Label: "Kind", Value: firstNonEmpty(node.NodeKind, "-")},
		{Label: "Role", Value: firstNonEmpty(node.NodeRole, "-")},
		{Label: "Runtime", Value: firstNonEmpty(node.RuntimeClass, "-")},
		{Label: "Presence", Value: firstNonEmpty(node.PresenceState, "-")},
		{Label: "Last Heartbeat", Value: timePtrOrDash(node.LastHeartbeatAt)},
		{Label: "Last Seen", Value: timePtrOrDash(node.LastSeenAt)},
		{Label: "Runtime Version", Value: stringPtrOrDash(node.RuntimeVersion)},
		{Label: "Enrollment", Value: firstNonEmpty(node.EnrollmentStatus, "-")},
		{Label: "Credential", Value: firstNonEmpty(node.CredentialStatus, "-")},
		{Label: "Home Scope", Value: stringPtrOrDash(node.HomeScopeID)},
	}
	return successActionResult(action, envelope.Meta.CorrelationID, "Node detail loaded.", fields)
}

func (e ActionExecutor) executeNodeHealth(ctx context.Context, action PortalAction) PortalActionResult {
	target := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if target == "" {
		return failedActionResult(action, "portal.action_target_missing", "Node health action is missing a node target.")
	}
	envelope, err := e.Client.GetNodeHealth(ctx, e.CorrelationID, target)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	health := envelope.Data
	fields := []ActionResultField{
		{Label: "Node", Value: firstNonEmpty(health.Node.DisplayName, health.Node.NodeKey, health.Node.NodeID, target)},
		{Label: "Status", Value: firstNonEmpty(health.Node.Status, "-")},
		{Label: "Presence", Value: firstNonEmpty(health.Node.PresenceState, "-")},
		{Label: "Heartbeat Age Seconds", Value: int64PtrOrDash(health.HeartbeatAgeSeconds)},
		{Label: "Pending Messages", Value: fmt.Sprintf("%d", health.PendingMessages)},
		{Label: "Failed Messages", Value: fmt.Sprintf("%d", health.FailedMessages)},
		{Label: "Dead Letters", Value: fmt.Sprintf("%d", health.DeadLetterMessages)},
	}
	if health.LastHeartbeat != nil {
		fields = append(fields,
			ActionResultField{Label: "Last Heartbeat", Value: timeOrDash(health.LastHeartbeat.ReceivedAt)},
			ActionResultField{Label: "Reported Status", Value: firstNonEmpty(health.LastHeartbeat.ReportedStatus, "-")},
		)
	}
	return successActionResult(action, envelope.Meta.CorrelationID, "Node health loaded.", fields)
}

func successActionResult(action PortalAction, correlationID, summary string, fields []ActionResultField) PortalActionResult {
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       summary,
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(correlationID),
		RefreshScreen: action.RefreshScreen,
	}
}

func portalActionMetadata(action PortalAction) json.RawMessage {
	raw, err := json.Marshal(map[string]string{"source": "loom_portal", "action_id": action.ID})
	if err != nil {
		return json.RawMessage(`{"source":"loom_portal"}`)
	}
	return raw
}

func activeVersionLabel(version any) string {
	if version == nil {
		return "-"
	}
	raw, _ := json.Marshal(version)
	var shaped struct {
		CapabilityEndpointVersionID string `json:"capability_endpoint_version_id"`
		VersionLabel                string `json:"version_label"`
		Status                      string `json:"status"`
	}
	if json.Unmarshal(raw, &shaped) == nil {
		return firstNonEmpty(shaped.VersionLabel, shaped.CapabilityEndpointVersionID, shaped.Status, "-")
	}
	return "-"
}

func schemaSummary(raw json.RawMessage) string {
	compact := compactJSON(raw)
	if compact == "-" {
		return "-"
	}
	return trimForWidth(compact, 120)
}

func capabilityCallResultFields(call routing.CapabilityCall) []ActionResultField {
	return []ActionResultField{
		{Label: "Capability Call", Value: firstNonEmpty(call.CapabilityCallID, "-")},
		{Label: "Status", Value: firstNonEmpty(call.Status, "-")},
		{Label: "Route", Value: firstNonEmpty(call.RouteID, "-")},
		{Label: "Provider", Value: firstNonEmpty(call.ProviderID, "-")},
		{Label: "Capability", Value: firstNonEmpty(call.CapabilityEndpointID, "-")},
		{Label: "Operation", Value: firstNonEmpty(call.Operation, "-")},
		{Label: "Execution Mode", Value: firstNonEmpty(call.ExecutionMode, "-")},
		{Label: "Job", Value: stringPtrOrDash(call.JobID)},
		{Label: "Error", Value: firstNonEmpty(stringPtrOrDash(call.ErrorCode), stringPtrOrDash(call.ErrorMessage), "-")},
	}
}

func deletionRequestResultFields(request loomsync.DeletionRequest) []ActionResultField {
	return []ActionResultField{
		{Label: "Deletion Request", Value: firstNonEmpty(request.DeletionRequestID, "-")},
		{Label: "Status", Value: firstNonEmpty(request.Status, "-")},
		{Label: "Origin Node", Value: firstNonEmpty(request.OriginNodeID, "-")},
		{Label: "Target", Value: firstNonEmpty(request.TargetKind, "-") + ":" + firstNonEmpty(request.TargetRef, "-")},
		{Label: "Requested Action", Value: firstNonEmpty(request.RequestedAction, "-")},
		{Label: "Reason", Value: firstNonEmpty(request.Reason, "-")},
		{Label: "Requested", Value: timeOrDash(request.RequestedAt)},
		{Label: "Reviewed", Value: timePtrOrDash(request.ReviewedAt)},
		{Label: "Reviewed By", Value: stringPtrOrDash(request.ReviewedByActorID)},
	}
}

func jobResultFields(job jobs.Job) []ActionResultField {
	return []ActionResultField{
		{Label: "Job", Value: firstNonEmpty(job.JobID, "-")},
		{Label: "Status", Value: firstNonEmpty(job.Status, "-")},
		{Label: "Type", Value: firstNonEmpty(job.JobType, "-")},
		{Label: "Origin Node", Value: firstNonEmpty(job.OriginNodeID, "-")},
		{Label: "Execution Node", Value: firstNonEmpty(job.ExecutionNodeID, "-")},
		{Label: "Attempts", Value: fmt.Sprintf("%d/%d", job.AttemptCount, job.MaxAttempts)},
		{Label: "Target", Value: firstNonEmpty(stringPtrOrDash(job.TargetKind), "-") + ":" + firstNonEmpty(stringPtrOrDash(job.TargetID), "-")},
		{Label: "Script", Value: stringPtrOrDash(job.ScriptID)},
		{Label: "Source Object", Value: stringPtrOrDash(job.SourceObjectID)},
		{Label: "Worker Run", Value: stringPtrOrDash(job.LastWorkerRunID)},
		{Label: "Manual Action", Value: fmt.Sprintf("%t", job.ManualAction)},
		{Label: "Attention", Value: jobs.EffectiveFailureAttentionStatus(job.FailureAttentionStatus)},
		{Label: "Attention Updated", Value: timePtrOrDash(job.FailureAttentionUpdatedAt)},
		{Label: "Next Attempt", Value: timePtrOrDash(job.NextAttemptAt)},
		{Label: "Cancel Requested", Value: timePtrOrDash(job.CancelRequestedAt)},
		{Label: "Failure", Value: firstNonEmpty(stringPtrOrDash(job.FailureCode), stringPtrOrDash(job.FailureMessage), "-")},
	}
}
