package portal

import (
	"encoding/json"
	"fmt"
	"strings"

	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/routing"
	loomsync "loom.local/loom/internal/sync"
	mainwatchedroots "loom.local/loom/internal/watchedroots"
)

func NewPortalRecordInspectAction(domain, sourceScreen, kind, ref, label string, payload map[string]string) PortalAction {
	action := PortalAction{
		ID:            fmt.Sprintf("%s.%s.%s.inspect", safeActionID(domain), safeActionID(kind), safeActionID(firstNonEmpty(ref, label))),
		Label:         "Inspect " + titleFromToken(kind),
		Description:   "Inspect this " + titleFromToken(kind) + " record.",
		Domain:        domain,
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    kind,
		TargetRef:     ref,
		TargetLabel:   firstNonEmpty(label, ref),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorRecordInspect, Target: ref, Payload: payload},
		RawDetails:    payload,
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewAutomationInspectAction(record automation.Automation, sourceScreen string) PortalAction {
	return NewPortalRecordInspectAction("automation", sourceScreen, "automation", record.AutomationID, automationLabel(record), automationPayload(record))
}

func NewIntegrationInspectAction(record automation.Integration, sourceScreen string) PortalAction {
	return NewPortalRecordInspectAction("automation", sourceScreen, "integration", record.IntegrationID, integrationLabel(record), integrationPayload(record))
}

func NewScheduleInspectAction(schedule automation.Schedule, sourceScreen string) PortalAction {
	ref := firstNonEmpty(schedule.ScheduleID, schedule.ScheduleKey)
	label := scheduleLabel(schedule)
	action := PortalAction{
		ID:            fmt.Sprintf("automation.schedule.%s.inspect", safeActionID(firstNonEmpty(ref, label))),
		Label:         "Inspect Schedule",
		Description:   "Inspect schedule status, timing, policies, and target capability.",
		Domain:        "automation",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "schedule",
		TargetRef:     ref,
		TargetLabel:   label,
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorScheduleInspect, Target: ref, Payload: schedulePayload(schedule)},
		RawCommand:    []string{"loom", "schedule", "inspect", ref},
		RawDetails:    schedulePayload(schedule),
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This schedule row does not include a schedule ref."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewScheduleFireNowAction(schedule automation.Schedule, sourceScreen string) PortalAction {
	ref := firstNonEmpty(schedule.ScheduleID, schedule.ScheduleKey)
	action := PortalAction{
		ID:            fmt.Sprintf("automation.schedule.%s.fire_now", safeActionID(firstNonEmpty(ref, scheduleLabel(schedule)))),
		Label:         "Fire Schedule Now",
		Description:   "Create one manual schedule fire and run the dispatcher immediately.",
		Domain:        "automation",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "schedule",
		TargetRef:     ref,
		TargetLabel:   scheduleLabel(schedule),
		Risk:          ActionRiskSafeRun,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorScheduleFireNow, Target: ref, Payload: schedulePayload(schedule)},
		RawCommand:    []string{"loom", "schedule", "fire", ref, "--reason", "portal manual fire"},
		RawDetails:    schedulePayload(schedule),
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This schedule row does not include a schedule ref."
	} else if schedule.Status == automation.ScheduleStatusDisabled || schedule.Status == automation.ScheduleStatusCompleted {
		action.State = ActionDisabled
		action.DisabledReason = "This schedule is not active enough to fire manually."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewSchedulePauseAction(schedule automation.Schedule, sourceScreen string) PortalAction {
	return scheduleStatusAction(schedule, sourceScreen, "pause", PortalExecutorSchedulePause, "Pause Schedule", "Pause this active schedule.", automation.ScheduleStatusActive)
}

func NewScheduleResumeAction(schedule automation.Schedule, sourceScreen string) PortalAction {
	return scheduleStatusAction(schedule, sourceScreen, "resume", PortalExecutorScheduleResume, "Resume Schedule", "Resume this paused schedule.", automation.ScheduleStatusPaused)
}

func scheduleStatusAction(schedule automation.Schedule, sourceScreen, actionName, executorKind, label, description, requiredStatus string) PortalAction {
	ref := firstNonEmpty(schedule.ScheduleID, schedule.ScheduleKey)
	action := PortalAction{
		ID:            fmt.Sprintf("automation.schedule.%s.%s", safeActionID(firstNonEmpty(ref, scheduleLabel(schedule))), actionName),
		Label:         label,
		Description:   description,
		Domain:        "automation",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "schedule",
		TargetRef:     ref,
		TargetLabel:   scheduleLabel(schedule),
		Risk:          ActionRiskSensitive,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: executorKind, Target: ref, Payload: schedulePayload(schedule)},
		RawCommand:    []string{"loom", "schedule", actionName, ref, "--reason", "portal action"},
		RawDetails:    schedulePayload(schedule),
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This schedule row does not include a schedule ref."
	} else if schedule.Status != requiredStatus {
		action.State = ActionDisabled
		action.DisabledReason = fmt.Sprintf("This action is available only when the schedule is %s.", requiredStatus)
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewScheduleFireInspectAction(fire automation.ScheduleFire, sourceScreen string) PortalAction {
	ref := fire.ScheduleFireID
	action := PortalAction{
		ID:            fmt.Sprintf("automation.schedule_fire.%s.inspect", safeActionID(ref)),
		Label:         "Inspect Schedule Fire",
		Description:   "Inspect this schedule fire and linked invocation/job/capability call.",
		Domain:        "automation",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "schedule_fire",
		TargetRef:     ref,
		TargetLabel:   firstNonEmpty(ref, fire.ScheduleID),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorScheduleFireInspect, Target: ref, Payload: scheduleFirePayload(fire)},
		RawCommand:    []string{"loom", "schedule", "fires", fire.ScheduleID},
		RawDetails:    scheduleFirePayload(fire),
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This schedule fire row does not include a fire ref."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewDirectEventEndpointInspectAction(endpoint automation.DirectEventEndpoint, sourceScreen string) PortalAction {
	ref := firstNonEmpty(endpoint.EndpointID, endpoint.EndpointSlug)
	action := PortalAction{
		ID:            fmt.Sprintf("automation.direct_endpoint.%s.inspect", safeActionID(firstNonEmpty(ref, endpointLabel(endpoint)))),
		Label:         "Inspect Direct Event Endpoint",
		Description:   "Inspect endpoint routing, response mode, integration, and automation links.",
		Domain:        "automation",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "direct_event_endpoint",
		TargetRef:     ref,
		TargetLabel:   endpointLabel(endpoint),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorDirectEndpointInspect, Target: ref, Payload: endpointPayload(endpoint)},
		RawCommand:    []string{"loom", "endpoint", "inspect", ref},
		RawDetails:    endpointPayload(endpoint),
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This endpoint row does not include an endpoint ref."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewDirectEventEndpointTestAction(endpoint automation.DirectEventEndpoint, sourceScreen string) PortalAction {
	ref := firstNonEmpty(endpoint.EndpointID, endpoint.EndpointSlug)
	action := PortalAction{
		ID:             fmt.Sprintf("automation.direct_endpoint.%s.test", safeActionID(firstNonEmpty(ref, endpointLabel(endpoint)))),
		Label:          "Test Direct Event Endpoint",
		Description:    "Submit a local endpoint test payload when a dedicated backend test API exists.",
		Domain:         "automation",
		SourceScreen:   NormalizeScreen(sourceScreen),
		TargetKind:     "direct_event_endpoint",
		TargetRef:      ref,
		TargetLabel:    endpointLabel(endpoint),
		Risk:           ActionRiskSensitive,
		State:          ActionDisabled,
		DisabledReason: "Direct event endpoint test requires a dedicated backend test API.",
		InputValues:    map[string]string{},
		Executor:       PortalActionExecutor{Kind: PortalExecutorUnsupported, Target: ref, Payload: endpointPayload(endpoint)},
		RawDetails:     endpointPayload(endpoint),
		RefreshScreen:  NormalizeScreen(sourceScreen),
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewDirectEventInspectAction(event automation.DirectEvent, sourceScreen string) PortalAction {
	ref := event.DirectEventID
	action := PortalAction{
		ID:            fmt.Sprintf("automation.direct_event.%s.inspect", safeActionID(ref)),
		Label:         "Inspect Direct Event",
		Description:   "Inspect event ingest state, mapping, invocation, and result links.",
		Domain:        "automation",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "direct_event",
		TargetRef:     ref,
		TargetLabel:   firstNonEmpty(event.ExternalEventID, ref),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorDirectEventInspect, Target: ref, Payload: directEventPayload(event)},
		RawCommand:    []string{"loom", "direct-event", "inspect", ref},
		RawDetails:    directEventPayload(event),
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This direct event row does not include an event ref."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewDirectEventRawPayloadAction(event automation.DirectEvent, sourceScreen string) PortalAction {
	ref := event.DirectEventID
	action := PortalAction{
		ID:            fmt.Sprintf("automation.direct_event.%s.raw_payload", safeActionID(ref)),
		Label:         "Inspect Raw Payload",
		Description:   "Inspect the stored raw direct-event payload.",
		Domain:        "automation",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "direct_event",
		TargetRef:     ref,
		TargetLabel:   firstNonEmpty(event.ExternalEventID, ref),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorDirectEventRawPayload, Target: ref, Payload: directEventPayload(event)},
		RawCommand:    []string{"loom", "direct-event", "raw", ref},
		RawDetails:    directEventPayload(event),
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This direct event row does not include an event ref."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewInvocationInspectAction(invocation automation.Invocation, sourceScreen string) PortalAction {
	ref := invocation.InvocationID
	action := PortalAction{
		ID:            fmt.Sprintf("automation.invocation.%s.inspect", safeActionID(ref)),
		Label:         "Inspect Invocation",
		Description:   "Inspect automation invocation routing, call, job, and failure state.",
		Domain:        "automation",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "invocation",
		TargetRef:     ref,
		TargetLabel:   firstNonEmpty(ref, invocation.TargetCapability),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorInvocationInspect, Target: ref, Payload: invocationPayload(invocation)},
		RawCommand:    []string{"loom", "invocation", "inspect", ref},
		RawDetails:    invocationPayload(invocation),
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This invocation row does not include an invocation ref."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewProviderInspectAction(provider capabilities.ProviderListItem, sourceScreen string) PortalAction {
	ref := firstNonEmpty(provider.Provider.ProviderID, provider.Provider.ProviderKey, provider.Provider.CompactAddress)
	action := PortalAction{
		ID:            fmt.Sprintf("capability.provider.%s.inspect", safeActionID(firstNonEmpty(ref, providerLabel(provider)))),
		Label:         "Inspect Provider",
		Description:   "Inspect provider registration, health, endpoints, and usage docs.",
		Domain:        "capabilities",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "provider",
		TargetRef:     ref,
		TargetLabel:   providerLabel(provider),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorProviderInspect, Target: ref, Payload: providerPayload(provider)},
		RawCommand:    []string{"loom", "provider", "inspect", ref},
		RawDetails:    providerPayload(provider),
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This provider row does not include a provider ref."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewProviderHealthAction(provider capabilities.ProviderListItem, sourceScreen string) PortalAction {
	ref := firstNonEmpty(provider.Provider.ProviderID, provider.Provider.ProviderKey, provider.Provider.CompactAddress)
	action := PortalAction{
		ID:            fmt.Sprintf("capability.provider.%s.health", safeActionID(firstNonEmpty(ref, providerLabel(provider)))),
		Label:         "Provider Health",
		Description:   "Inspect the provider health and availability state.",
		Domain:        "capabilities",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "provider",
		TargetRef:     ref,
		TargetLabel:   providerLabel(provider),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorProviderHealth, Target: ref, Payload: providerPayload(provider)},
		RawCommand:    []string{"loom", "provider", "health", ref},
		RawDetails:    providerPayload(provider),
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This provider row does not include a provider ref."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewProviderAdvertisementInspectAction(ad capabilities.ProviderAdvertisement, sourceScreen string) PortalAction {
	ref := ad.ProviderAdvertisementID
	action := PortalAction{
		ID:            fmt.Sprintf("capability.provider_advertisement.%s.inspect", safeActionID(ref)),
		Label:         "Inspect Provider Advertisement",
		Description:   "Inspect provider advertisement validation, review, and upsert state.",
		Domain:        "capabilities",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "provider_advertisement",
		TargetRef:     ref,
		TargetLabel:   ref,
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorProviderAdInspect, Target: ref, Payload: providerAdvertisementPayload(ad)},
		RawCommand:    []string{"loom", "provider-advertisement", "inspect", ref},
		RawDetails:    providerAdvertisementPayload(ad),
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This advertisement row does not include an advertisement ref."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewCapabilityInspectAction(capability capabilities.CapabilityListItem, sourceScreen string) PortalAction {
	ref := capability.CapabilityEndpoint.CapabilityEndpointID
	label := capabilityLabel(capability)
	action := PortalAction{
		ID:            fmt.Sprintf("capability.endpoint.%s.inspect", safeActionID(firstNonEmpty(ref, label))),
		Label:         "Inspect Capability",
		Description:   "Inspect capability schema, risk, provider, endpoint, and docs.",
		Domain:        "capabilities",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "capability",
		TargetRef:     ref,
		TargetLabel:   label,
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorCapabilityInspect, Target: firstNonEmpty(ref, capability.CapabilityEndpoint.CompactAddress), Payload: capabilityPayload(capability)},
		RawCommand:    []string{"loom", "capability", "inspect", firstNonEmpty(ref, capability.CapabilityEndpoint.CompactAddress)},
		RawDetails:    capabilityPayload(capability),
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" && capability.CapabilityEndpoint.CompactAddress == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This capability row does not include a capability ref."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewCapabilityUsageDocsAction(capability capabilities.CapabilityListItem, sourceScreen string) PortalAction {
	ref := firstNonEmpty(capability.CapabilityEndpoint.CapabilityEndpointID, capability.CapabilityEndpoint.CompactAddress)
	action := PortalAction{
		ID:            fmt.Sprintf("capability.endpoint.%s.usage_docs", safeActionID(firstNonEmpty(ref, capabilityLabel(capability)))),
		Label:         "View Usage Docs",
		Description:   "View backend usage documents for this capability.",
		Domain:        "capabilities",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "capability",
		TargetRef:     ref,
		TargetLabel:   capabilityLabel(capability),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorCapabilityUsageDocs, Target: ref, Payload: capabilityPayload(capability)},
		RawCommand:    []string{"loom", "capability", "usage-docs", ref},
		RawDetails:    capabilityPayload(capability),
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This capability row does not include a capability ref."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewCapabilityCallAction(capability capabilities.CapabilityListItem, sourceScreen string) PortalAction {
	ref := firstNonEmpty(capability.CapabilityEndpoint.CompactAddress, capability.CapabilityEndpoint.CapabilityEndpointID)
	action := PortalAction{
		ID:           fmt.Sprintf("capability.endpoint.%s.call", safeActionID(firstNonEmpty(ref, capabilityLabel(capability)))),
		Label:        "Invoke Capability",
		Description:  "Call this capability through the router with a minimal JSON payload.",
		Domain:       "capabilities",
		SourceScreen: NormalizeScreen(sourceScreen),
		TargetKind:   "capability",
		TargetRef:    ref,
		TargetLabel:  capabilityLabel(capability),
		Risk:         ActionRiskSensitive,
		State:        ActionAvailable,
		InputFields: []PortalActionField{{
			Name:        "input_json",
			Label:       "Input JSON",
			Kind:        ActionFieldTextareaJSON,
			Required:    false,
			Value:       "{}",
			Placeholder: "{}",
			Help:        "Minimal JSON input sent to the capability.",
		}},
		InputValues:   map[string]string{"input_json": "{}"},
		Executor:      PortalActionExecutor{Kind: PortalExecutorCapabilityCall, Target: ref, Payload: capabilityPayload(capability)},
		RawCommand:    []string{"loom", "capability", "call", ref, "--input", "{}"},
		RawDetails:    capabilityPayload(capability),
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if reason := capabilityCallDisabledReason(capability); reason != "" {
		action.State = ActionDisabled
		action.DisabledReason = reason
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewCapabilityCallInspectAction(call routing.CapabilityCall, sourceScreen string) PortalAction {
	ref := call.CapabilityCallID
	action := PortalAction{
		ID:            fmt.Sprintf("capability.call.%s.inspect", safeActionID(ref)),
		Label:         "Inspect Capability Call",
		Description:   "Inspect routing and result state for this capability call.",
		Domain:        "capabilities",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "capability_call",
		TargetRef:     ref,
		TargetLabel:   firstNonEmpty(ref, call.Operation),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorCapabilityCallInspect, Target: ref, Payload: capabilityCallPayload(call)},
		RawCommand:    []string{"loom", "capability-call", "inspect", ref},
		RawDetails:    capabilityCallPayload(call),
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This capability call row does not include a call ref."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewJobInspectAction(job jobs.Job, sourceScreen string) PortalAction {
	ref := job.JobID
	action := PortalAction{
		ID:            fmt.Sprintf("job.%s.inspect", safeActionID(firstNonEmpty(ref, job.JobType))),
		Label:         "Inspect Job",
		Description:   "Inspect job detail, attempts, outputs, logs, artifacts, and failure state.",
		Domain:        "jobs",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "job",
		TargetRef:     ref,
		TargetLabel:   jobLabel(job),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorJobInspect, Target: ref, Payload: jobPayload(job)},
		RawCommand:    []string{"loom", "job", "inspect", ref},
		RawDetails:    jobPayload(job),
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This job row does not include a job ID."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewJobLogsInspectAction(job jobs.Job, sourceScreen string) PortalAction {
	ref := job.JobID
	action := PortalAction{
		ID:            fmt.Sprintf("job.%s.logs", safeActionID(firstNonEmpty(ref, job.JobType))),
		Label:         "Inspect Job Logs",
		Description:   "Inspect captured log streams for this job.",
		Domain:        "jobs",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "job",
		TargetRef:     ref,
		TargetLabel:   jobLabel(job),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorJobLogsInspect, Target: ref, Payload: jobPayload(job)},
		RawCommand:    []string{"loom", "job", "logs", ref},
		RawDetails:    jobPayload(job),
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This job row does not include a job ID."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewJobOutputsInspectAction(job jobs.Job, sourceScreen string) PortalAction {
	ref := job.JobID
	action := PortalAction{
		ID:            fmt.Sprintf("job.%s.outputs", safeActionID(firstNonEmpty(ref, job.JobType))),
		Label:         "Inspect Job Outputs",
		Description:   "Inspect structured outputs produced by this job.",
		Domain:        "jobs",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "job",
		TargetRef:     ref,
		TargetLabel:   jobLabel(job),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorJobOutputsInspect, Target: ref, Payload: jobPayload(job)},
		RawCommand:    []string{"loom", "job", "outputs", ref},
		RawDetails:    jobPayload(job),
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This job row does not include a job ID."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewJobRetryAction(job jobs.Job, sourceScreen string) PortalAction {
	ref := job.JobID
	action := PortalAction{
		ID:            fmt.Sprintf("job.%s.retry", safeActionID(firstNonEmpty(ref, job.JobType))),
		Label:         "Retry Job",
		Description:   "Requeue this failed or timed-out job when attempts remain.",
		Domain:        "jobs",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "job",
		TargetRef:     ref,
		TargetLabel:   jobLabel(job),
		Risk:          ActionRiskSensitive,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorJobRetry, Target: ref, Payload: jobPayload(job)},
		RawCommand:    []string{"loom", "job", "retry", ref},
		RawDetails:    jobPayload(job),
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This job row does not include a job ID."
	} else if reason := jobRetryDisabledReason(job); reason != "" {
		action.State = ActionDisabled
		action.DisabledReason = reason
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewJobCancelAction(job jobs.Job, sourceScreen string) PortalAction {
	ref := job.JobID
	action := PortalAction{
		ID:            fmt.Sprintf("job.%s.cancel", safeActionID(firstNonEmpty(ref, job.JobType))),
		Label:         "Cancel Job",
		Description:   "Request cancellation for this queued or running job.",
		Domain:        "jobs",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "job",
		TargetRef:     ref,
		TargetLabel:   jobLabel(job),
		Risk:          ActionRiskSensitive,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorJobCancel, Target: ref, Payload: jobPayload(job)},
		RawCommand:    []string{"loom", "job", "cancel", ref},
		RawDetails:    jobPayload(job),
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This job row does not include a job ID."
	} else if !jobCancellable(job) {
		action.State = ActionDisabled
		action.DisabledReason = "This job is not queued or running."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewJobAcknowledgeAttentionAction(job jobs.Job, sourceScreen string) PortalAction {
	ref := job.JobID
	action := PortalAction{
		ID:            fmt.Sprintf("job.%s.attention.acknowledge", safeActionID(firstNonEmpty(ref, job.JobType))),
		Label:         "Acknowledge Job Attention",
		Description:   "Remove this failed job from active attention without changing its terminal status.",
		Domain:        "jobs",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "job",
		TargetRef:     ref,
		TargetLabel:   jobLabel(job),
		Risk:          ActionRiskSensitive,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorJobAttentionAcknowledge, Target: ref, Payload: jobPayload(job)},
		RawCommand:    []string{"loom", "job", "acknowledge", ref},
		RawDetails:    jobPayload(job),
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This job row does not include a job ID."
	} else if reason := jobAttentionAcknowledgeDisabledReason(job); reason != "" {
		action.State = ActionDisabled
		action.DisabledReason = reason
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewJobArchiveAttentionAction(job jobs.Job, sourceScreen string) PortalAction {
	ref := job.JobID
	action := PortalAction{
		ID:            fmt.Sprintf("job.%s.attention.archive", safeActionID(firstNonEmpty(ref, job.JobType))),
		Label:         "Archive Job Attention",
		Description:   "Retire this failed job from active attention while keeping it inspectable.",
		Domain:        "jobs",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "job",
		TargetRef:     ref,
		TargetLabel:   jobLabel(job),
		Risk:          ActionRiskSensitive,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorJobAttentionArchive, Target: ref, Payload: jobPayload(job)},
		RawCommand:    []string{"loom", "job", "archive", ref},
		RawDetails:    jobPayload(job),
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This job row does not include a job ID."
	} else if reason := jobAttentionArchiveDisabledReason(job); reason != "" {
		action.State = ActionDisabled
		action.DisabledReason = reason
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewRunnerInspectAction(runner jobs.Runner, sourceScreen string) PortalAction {
	return NewPortalRecordInspectAction("jobs", sourceScreen, "runner", runner.RunnerID, firstNonEmpty(runner.RunnerKey, runner.RunnerID), runnerPayload(runner))
}

func NewNodeInspectAction(node nodes.Node, sourceScreen string) PortalAction {
	ref := firstNonEmpty(node.NodeID, node.NodeKey)
	action := PortalAction{
		ID:            fmt.Sprintf("node.%s.inspect", safeActionID(firstNonEmpty(ref, nodeLabel(node)))),
		Label:         "Inspect Node",
		Description:   "Inspect node registration, runtime, presence, and credential state.",
		Domain:        "nodes",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "node",
		TargetRef:     ref,
		TargetLabel:   nodeLabel(node),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorNodeInspect, Target: ref, Payload: nodePayload(node)},
		RawCommand:    []string{"loom", "node", "inspect", ref},
		RawDetails:    nodePayload(node),
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This node row does not include a node ref."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewNodeHealthAction(node nodes.Node, sourceScreen string) PortalAction {
	ref := firstNonEmpty(node.NodeID, node.NodeKey)
	action := PortalAction{
		ID:            fmt.Sprintf("node.%s.health", safeActionID(firstNonEmpty(ref, nodeLabel(node)))),
		Label:         "Node Health",
		Description:   "Inspect node heartbeat and communication health.",
		Domain:        "nodes",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "node",
		TargetRef:     ref,
		TargetLabel:   nodeLabel(node),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorNodeHealth, Target: ref, Payload: nodePayload(node)},
		RawCommand:    []string{"loom", "node", "health", ref},
		RawDetails:    nodePayload(node),
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This node row does not include a node ref."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewWatchedRootInspectAction(status mainwatchedroots.RootStatus, sourceScreen string) PortalAction {
	root := status.Root
	ref := root.NodeID + "/" + root.RootKey
	return NewPortalRecordInspectAction("nodes", sourceScreen, "watched_root", ref, firstNonEmpty(root.DisplayName, ref), watchedRootPayload(root, status))
}

func NewWatchedRootFindingInspectAction(finding mainwatchedroots.Finding, sourceScreen string) PortalAction {
	ref := firstNonEmpty(finding.WatchedRootFindingID, finding.NodeID+"/"+finding.RootKey+"/"+finding.FindingKey)
	return NewPortalRecordInspectAction("nodes", sourceScreen, "watched_root_finding", ref, firstNonEmpty(finding.Summary, ref), watchedRootFindingPayload(finding))
}

func NewWatchedRootBackupStatusInspectAction(status mainwatchedroots.BackupStatus, sourceScreen string) PortalAction {
	ref := status.Root.NodeID + "/" + status.Root.RootKey
	return NewPortalRecordInspectAction("nodes", sourceScreen, "watched_root_backup_status", ref, ref, watchedRootBackupStatusPayload(status))
}

func NewWatchedRootBackupBatchInspectAction(batch mainwatchedroots.BackupBatch, sourceScreen string) PortalAction {
	ref := batch.WatchedRootBackupBatchID
	return NewPortalRecordInspectAction("nodes", sourceScreen, "watched_root_backup_batch", ref, ref, watchedRootBackupBatchPayload(batch))
}

func NewSyncBatchInspectAction(batch loomsync.SyncBatch, sourceScreen string) PortalAction {
	return NewPortalRecordInspectAction("nodes", sourceScreen, "sync_batch", batch.SyncBatchID, batch.SyncBatchID, syncBatchPayload(batch))
}

func NewSyncConflictInspectAction(conflict loomsync.SyncConflict, sourceScreen string) PortalAction {
	return NewPortalRecordInspectAction("nodes", sourceScreen, "sync_conflict", conflict.SyncConflictID, firstNonEmpty(conflict.Summary, conflict.SyncConflictID), syncConflictPayload(conflict))
}

func NewSyncReplicaInspectAction(replica loomsync.SyncReplica, sourceScreen string) PortalAction {
	return NewPortalRecordInspectAction("nodes", sourceScreen, "sync_replica", replica.ReplicaID, replica.ReplicaID, syncReplicaPayload(replica))
}

func NewPrivateBackupInspectAction(backup loomsync.PrivateBackupOperation, sourceScreen string) PortalAction {
	return NewPortalRecordInspectAction("nodes", sourceScreen, "private_backup", backup.PrivateBackupOperationID, backup.PrivateBackupOperationID, privateBackupPayload(backup))
}

func NewDeletionRequestInspectAction(request loomsync.DeletionRequest, sourceScreen string) PortalAction {
	ref := request.DeletionRequestID
	action := PortalAction{
		ID:            fmt.Sprintf("deletion_request.%s.inspect", safeActionID(firstNonEmpty(ref, request.TargetRef))),
		Label:         "Inspect Deletion Request",
		Description:   "Inspect deletion request status, target, reason, and review state.",
		Domain:        "sync",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "deletion_request",
		TargetRef:     ref,
		TargetLabel:   firstNonEmpty(request.TargetRef, ref),
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorDeletionRequestInspect, Target: ref, Payload: deletionRequestPayload(request)},
		RawCommand:    []string{"loom", "sync", "deletion-request", "inspect", ref},
		RawDetails:    deletionRequestPayload(request),
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This deletion request row does not include a request ID."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewDeletionRequestReviewAction(request loomsync.DeletionRequest, sourceScreen string) PortalAction {
	return newDeletionRequestLifecycleAction(request, sourceScreen, "review", "Review Deletion Request", "Mark this deletion request as pending review.", PortalExecutorDeletionRequestReview)
}

func NewDeletionRequestApproveAction(request loomsync.DeletionRequest, sourceScreen string) PortalAction {
	return newDeletionRequestLifecycleAction(request, sourceScreen, "approve", "Approve Deletion Request", "Approve the request semantics without deleting files.", PortalExecutorDeletionRequestApprove)
}

func NewDeletionRequestDenyAction(request loomsync.DeletionRequest, sourceScreen string) PortalAction {
	return newDeletionRequestLifecycleAction(request, sourceScreen, "deny", "Deny Deletion Request", "Deny this deletion request without deleting files.", PortalExecutorDeletionRequestDeny)
}

func NewDeletionRequestCompleteAction(request loomsync.DeletionRequest, sourceScreen string) PortalAction {
	return newDeletionRequestLifecycleAction(request, sourceScreen, "complete", "Complete Deletion Request", "Mark this request completed after safe action happened elsewhere.", PortalExecutorDeletionRequestComplete)
}

func newDeletionRequestLifecycleAction(request loomsync.DeletionRequest, sourceScreen string, verb string, label string, description string, executor string) PortalAction {
	ref := request.DeletionRequestID
	action := PortalAction{
		ID:            fmt.Sprintf("deletion_request.%s.%s", safeActionID(firstNonEmpty(ref, request.TargetRef)), verb),
		Label:         label,
		Description:   description,
		Domain:        "sync",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "deletion_request",
		TargetRef:     ref,
		TargetLabel:   firstNonEmpty(request.TargetRef, ref),
		Risk:          ActionRiskSensitive,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: executor, Target: ref, Payload: deletionRequestPayload(request)},
		RawCommand:    []string{"loom", "sync", "deletion-request", verb, ref},
		RawDetails:    deletionRequestPayload(request),
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	if ref == "" {
		action.State = ActionDisabled
		action.DisabledReason = "This deletion request row does not include a request ID."
	} else if reason := deletionRequestLifecycleDisabledReason(request, verb); reason != "" {
		action.State = ActionDisabled
		action.DisabledReason = reason
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func automationLabel(record automation.Automation) string {
	return firstNonEmpty(record.DisplayName, record.AutomationKey, record.AutomationID)
}

func integrationLabel(record automation.Integration) string {
	return firstNonEmpty(record.DisplayName, record.IntegrationKey, record.IntegrationID)
}

func scheduleLabel(schedule automation.Schedule) string {
	return firstNonEmpty(schedule.DisplayName, schedule.ScheduleKey, schedule.ScheduleID)
}

func endpointLabel(endpoint automation.DirectEventEndpoint) string {
	return firstNonEmpty(endpoint.DisplayName, endpoint.EndpointSlug, endpoint.EndpointID)
}

func providerLabel(provider capabilities.ProviderListItem) string {
	return firstNonEmpty(provider.Provider.DisplayName, provider.Provider.CompactAddress, provider.Provider.ProviderKey, provider.Provider.ProviderID)
}

func capabilityLabel(capability capabilities.CapabilityListItem) string {
	return firstNonEmpty(capability.DisplayName, capability.CapabilityEndpoint.CompactAddress, capability.ClassName, capability.CapabilityEndpoint.CapabilityEndpointID)
}

func jobLabel(job jobs.Job) string {
	return firstNonEmpty(job.JobID, job.JobType)
}

func nodeLabel(node nodes.Node) string {
	return firstNonEmpty(node.DisplayName, node.NodeKey, node.NodeID)
}

func automationPayload(record automation.Automation) map[string]string {
	return map[string]string{
		"automation_id": record.AutomationID,
		"key":           record.AutomationKey,
		"display_name":  record.DisplayName,
		"status":        record.Status,
		"source_kind":   record.SourceKind,
		"run_as":        record.RunAsActorID,
		"scope":         stringPtrOrDash(record.ScopeID),
		"project":       stringPtrOrDash(record.ProjectID),
		"updated_at":    timeOrDash(record.UpdatedAt),
	}
}

func integrationPayload(record automation.Integration) map[string]string {
	return map[string]string{
		"integration_id": record.IntegrationID,
		"key":            record.IntegrationKey,
		"display_name":   record.DisplayName,
		"status":         record.Status,
		"actor":          record.ActorID,
		"updated_at":     timeOrDash(record.UpdatedAt),
	}
}

func schedulePayload(schedule automation.Schedule) map[string]string {
	return map[string]string{
		"schedule_id":    schedule.ScheduleID,
		"automation_id":  schedule.AutomationID,
		"key":            schedule.ScheduleKey,
		"display_name":   schedule.DisplayName,
		"status":         schedule.Status,
		"kind":           schedule.ScheduleKind,
		"expression":     schedule.ScheduleExpr,
		"timezone":       schedule.Timezone,
		"next_fire":      timePtrOrDash(schedule.NextFireAt),
		"last_fire":      timePtrOrDash(schedule.LastFireAt),
		"last_fire_id":   stringPtrOrDash(schedule.LastScheduleFireID),
		"run_as":         schedule.RunAsActorID,
		"scope":          stringPtrOrDash(schedule.ScopeID),
		"project":        stringPtrOrDash(schedule.ProjectID),
		"target_profile": compactJSON(schedule.TargetProfileJSON),
		"misfire_policy": compactJSON(schedule.MisfireProfileJSON),
		"concurrency":    compactJSON(schedule.ConcurrencyProfileJSON),
		"approval":       compactJSON(schedule.ApprovalProfileJSON),
		"timeout":        compactJSON(schedule.TimeoutProfileJSON),
		"retry":          compactJSON(schedule.RetryProfileJSON),
	}
}

func scheduleFirePayload(fire automation.ScheduleFire) map[string]string {
	return map[string]string{
		"schedule_fire_id": fire.ScheduleFireID,
		"schedule_id":      fire.ScheduleID,
		"automation_id":    fire.AutomationID,
		"status":           fire.Status,
		"misfire_status":   fire.MisfireStatus,
		"scheduled_for":    timeOrDash(fire.ScheduledFor),
		"lateness_seconds": fmt.Sprintf("%d", fire.LatenessSeconds),
		"worker_run":       stringPtrOrDash(fire.WorkerRunID),
		"invocation":       stringPtrOrDash(fire.InvocationID),
		"route":            stringPtrOrDash(fire.RouteID),
		"capability_call":  stringPtrOrDash(fire.CapabilityCallID),
		"job":              stringPtrOrDash(fire.JobID),
		"failure_code":     stringPtrOrDash(fire.FailureCode),
		"failure_message":  stringPtrOrDash(fire.FailureMessage),
	}
}

func endpointPayload(endpoint automation.DirectEventEndpoint) map[string]string {
	return map[string]string{
		"endpoint_id":     endpoint.EndpointID,
		"slug":            endpoint.EndpointSlug,
		"display_name":    endpoint.DisplayName,
		"status":          endpoint.Status,
		"integration":     endpoint.IntegrationID,
		"automation":      endpoint.AutomationID,
		"event_type":      endpoint.EventType,
		"path":            endpoint.EndpointPath,
		"response_mode":   endpoint.ResponseMode,
		"mapping_profile": compactJSON(endpoint.MappingProfileJSON),
	}
}

func directEventPayload(event automation.DirectEvent) map[string]string {
	return map[string]string{
		"direct_event_id": event.DirectEventID,
		"endpoint_id":     event.EndpointID,
		"integration":     event.IntegrationID,
		"automation":      event.AutomationID,
		"status":          event.Status,
		"external_id":     event.ExternalEventID,
		"idempotency":     event.IdempotencyKey,
		"method":          event.RequestMethod,
		"path":            event.RequestPath,
		"payload_hash":    event.PayloadHash,
		"attempts":        fmt.Sprintf("%d", event.AttemptCount),
		"invocation":      stringPtrOrDash(event.InvocationID),
		"route":           stringPtrOrDash(event.RouteID),
		"capability_call": stringPtrOrDash(event.CapabilityCallID),
		"job":             stringPtrOrDash(event.JobID),
		"failure_code":    stringPtrOrDash(event.FailureCode),
		"failure_message": stringPtrOrDash(event.FailureMessage),
		"received_at":     timeOrDash(event.ReceivedAt),
	}
}

func invocationPayload(invocation automation.Invocation) map[string]string {
	return map[string]string{
		"invocation_id":     invocation.InvocationID,
		"automation_id":     invocation.AutomationID,
		"source":            invocation.SourceKind,
		"source_ref":        invocation.SourceRef,
		"source_occurrence": invocation.SourceOccurrenceRef,
		"actor":             invocation.ActorID,
		"origin_node":       invocation.OriginNodeID,
		"target_capability": invocation.TargetCapability,
		"idempotency":       invocation.IdempotencyKey,
		"status":            invocation.Status,
		"attempts":          fmt.Sprintf("%d/%d", invocation.AttemptCount, invocation.MaxAttempts),
		"next_attempt":      timePtrOrDash(invocation.NextAttemptAt),
		"route":             stringPtrOrDash(invocation.RouteID),
		"capability_call":   stringPtrOrDash(invocation.CapabilityCallID),
		"job":               stringPtrOrDash(invocation.JobID),
		"approval":          stringPtrOrDash(invocation.ApprovalID),
		"failure_code":      stringPtrOrDash(invocation.FailureCode),
		"failure_message":   stringPtrOrDash(invocation.FailureMessage),
	}
}

func providerPayload(provider capabilities.ProviderListItem) map[string]string {
	return map[string]string{
		"provider_id":  provider.Provider.ProviderID,
		"provider_key": provider.Provider.ProviderKey,
		"address":      provider.Provider.CompactAddress,
		"display_name": provider.Provider.DisplayName,
		"type":         provider.Provider.ProviderType,
		"node":         provider.Provider.NodeID,
		"scope":        provider.Provider.ScopeID,
		"status":       provider.Provider.Status,
		"health":       provider.HealthStatus,
		"availability": provider.AvailabilityStatus,
		"version":      provider.Provider.Version,
	}
}

func providerAdvertisementPayload(ad capabilities.ProviderAdvertisement) map[string]string {
	return map[string]string{
		"advertisement_id": ad.ProviderAdvertisementID,
		"origin_node":      ad.OriginNodeID,
		"provider_id":      stringPtrOrDash(ad.ProviderID),
		"status":           ad.Status,
		"hash":             ad.AdvertisementHash,
		"idempotency":      stringPtrOrDash(ad.IdempotencyKey),
		"rejection_reason": ad.RejectionReason,
		"received_at":      timeOrDash(ad.ReceivedAt),
		"validated_at":     timePtrOrDash(ad.ValidatedAt),
		"reviewed_at":      timePtrOrDash(ad.ReviewedAt),
	}
}

func capabilityPayload(capability capabilities.CapabilityListItem) map[string]string {
	return map[string]string{
		"capability_id":    capability.CapabilityEndpoint.CapabilityEndpointID,
		"address":          capability.CapabilityEndpoint.CompactAddress,
		"endpoint":         capability.CapabilityEndpoint.EndpointName,
		"display_name":     capability.DisplayName,
		"class":            capability.ClassName,
		"provider_id":      capability.CapabilityEndpoint.ProviderID,
		"provider_address": capability.ProviderAddress,
		"provider_key":     capability.ProviderKey,
		"provider_health":  capability.ProviderHealth,
		"form":             capability.CapabilityEndpoint.Form,
		"risk":             capability.CapabilityEndpoint.RiskLevel,
		"auth_level":       fmt.Sprintf("%d", capability.CapabilityEndpoint.ExecutionAuthorizationLevel),
		"status":           capability.CapabilityEndpoint.Status,
		"description":      capability.Description,
	}
}

func capabilityCallPayload(call routing.CapabilityCall) map[string]string {
	return map[string]string{
		"capability_call_id": call.CapabilityCallID,
		"key":                call.CapabilityCallKey,
		"route":              call.RouteID,
		"correlation":        call.CorrelationID,
		"idempotency":        stringPtrOrDash(call.IdempotencyKey),
		"actor":              call.ActorID,
		"origin_node":        call.OriginNodeID,
		"target_node":        call.TargetNodeID,
		"provider":           call.ProviderID,
		"capability":         call.CapabilityEndpointID,
		"operation":          call.Operation,
		"execution_mode":     call.ExecutionMode,
		"job":                stringPtrOrDash(call.JobID),
		"status":             call.Status,
		"error_code":         stringPtrOrDash(call.ErrorCode),
		"error_message":      stringPtrOrDash(call.ErrorMessage),
	}
}

func jobPayload(job jobs.Job) map[string]string {
	return map[string]string{
		"job_id":            job.JobID,
		"type":              job.JobType,
		"status":            job.Status,
		"origin_actor":      job.OriginActorID,
		"origin_node":       job.OriginNodeID,
		"execution_node":    job.ExecutionNodeID,
		"target_kind":       stringPtrOrDash(job.TargetKind),
		"target":            stringPtrOrDash(job.TargetID),
		"script":            stringPtrOrDash(job.ScriptID),
		"source_object":     stringPtrOrDash(job.SourceObjectID),
		"attempts":          fmt.Sprintf("%d/%d", job.AttemptCount, job.MaxAttempts),
		"priority":          fmt.Sprintf("%d", job.Priority),
		"next_attempt":      timePtrOrDash(job.NextAttemptAt),
		"manual_action":     fmt.Sprintf("%t", job.ManualAction),
		"worker_run":        stringPtrOrDash(job.LastWorkerRunID),
		"heartbeat":         timePtrOrDash(job.LastHeartbeatAt),
		"cancel_requested":  timePtrOrDash(job.CancelRequestedAt),
		"failure_code":      stringPtrOrDash(job.FailureCode),
		"failure_message":   stringPtrOrDash(job.FailureMessage),
		"attention_status":  jobs.EffectiveFailureAttentionStatus(job.FailureAttentionStatus),
		"attention_updated": timePtrOrDash(job.FailureAttentionUpdatedAt),
		"attention_actor":   stringPtrOrDash(job.FailureAttentionUpdatedByActorID),
		"attention_note":    firstNonEmpty(job.FailureAttentionNote, "-"),
		"created_at":        timeOrDash(job.CreatedAt),
	}
}

func runnerPayload(runner jobs.Runner) map[string]string {
	return map[string]string{
		"runner_id":    runner.RunnerID,
		"runner_key":   runner.RunnerKey,
		"node":         runner.NodeID,
		"type":         runner.RunnerType,
		"status":       runner.Status,
		"current_job":  stringPtrOrDash(runner.CurrentJobID),
		"heartbeat":    timePtrOrDash(runner.LastHeartbeatAt),
		"version":      runner.Version,
		"job_types":    compactJSON(runner.SupportedJobTypes),
		"runtimes":     compactJSON(runner.SupportedRuntimes),
		"last_updated": timeOrDash(runner.UpdatedAt),
	}
}

func nodePayload(node nodes.Node) map[string]string {
	return map[string]string{
		"node_id":           node.NodeID,
		"node_key":          node.NodeKey,
		"display_name":      node.DisplayName,
		"kind":              node.NodeKind,
		"role":              node.NodeRole,
		"runtime_class":     node.RuntimeClass,
		"status":            node.Status,
		"presence":          node.PresenceState,
		"last_heartbeat":    timePtrOrDash(node.LastHeartbeatAt),
		"last_seen":         timePtrOrDash(node.LastSeenAt),
		"runtime_version":   stringPtrOrDash(node.RuntimeVersion),
		"enrollment_status": node.EnrollmentStatus,
		"credential_status": node.CredentialStatus,
		"owner_actor":       stringPtrOrDash(node.OwnerActorID),
		"home_scope":        stringPtrOrDash(node.HomeScopeID),
	}
}

func watchedRootPayload(root mainwatchedroots.WatchedRoot, status mainwatchedroots.RootStatus) map[string]string {
	return map[string]string{
		"watched_root_id": root.WatchedRootID,
		"node":            root.NodeID,
		"root_key":        root.RootKey,
		"worker":          root.WorkerKey,
		"display_name":    root.DisplayName,
		"safe_root_key":   root.SafeRootKey,
		"status":          root.Status,
		"config_hash":     root.ConfigHash,
		"last_reported":   timeOrDash(root.LastReportedAt),
		"findings":        fmt.Sprintf("%d", len(status.LatestFindings)),
	}
}

func watchedRootFindingPayload(finding mainwatchedroots.Finding) map[string]string {
	return map[string]string{
		"finding_id":    finding.WatchedRootFindingID,
		"watched_root":  finding.WatchedRootID,
		"node":          finding.NodeID,
		"root_key":      finding.RootKey,
		"finding_key":   finding.FindingKey,
		"severity":      finding.Severity,
		"status":        finding.Status,
		"kind":          finding.Kind,
		"relative_path": finding.RelativePath,
		"summary":       finding.Summary,
		"first_seen":    timeOrDash(finding.FirstSeenAt),
		"last_seen":     timeOrDash(finding.LastSeenAt),
		"resolved_at":   timePtrOrDash(finding.ResolvedAt),
	}
}

func watchedRootBackupStatusPayload(status mainwatchedroots.BackupStatus) map[string]string {
	return map[string]string{
		"node":             status.Root.NodeID,
		"root_key":         status.Root.RootKey,
		"status":           status.Status,
		"latest_batch":     backupBatchPtrID(status.LatestBatch),
		"batches":          fmt.Sprintf("%d", status.BatchCount),
		"items":            fmt.Sprintf("%d", status.ItemCount),
		"accepted":         fmt.Sprintf("%d", status.AcceptedCount),
		"duplicate":        fmt.Sprintf("%d", status.DuplicateCount),
		"skipped":          fmt.Sprintf("%d", status.SkippedCount),
		"failed":           fmt.Sprintf("%d", status.FailedCount),
		"artifacts":        fmt.Sprintf("%d", status.ArtifactCount),
		"deletion_markers": fmt.Sprintf("%d", status.DeletionMarkerCount),
		"total_bytes":      fmt.Sprintf("%d", status.TotalBytes),
	}
}

func watchedRootBackupBatchPayload(batch mainwatchedroots.BackupBatch) map[string]string {
	return map[string]string{
		"batch_id":         batch.WatchedRootBackupBatchID,
		"watched_root":     batch.WatchedRootID,
		"node":             batch.NodeID,
		"root_key":         batch.RootKey,
		"worker":           batch.WorkerKey,
		"idempotency":      stringPtrOrDash(batch.IdempotencyKey),
		"kind":             batch.BatchKind,
		"mode":             batch.BackupMode,
		"status":           batch.Status,
		"items":            fmt.Sprintf("%d", batch.ItemCount),
		"accepted":         fmt.Sprintf("%d", batch.AcceptedCount),
		"duplicate":        fmt.Sprintf("%d", batch.DuplicateCount),
		"skipped":          fmt.Sprintf("%d", batch.SkippedCount),
		"failed":           fmt.Sprintf("%d", batch.FailedCount),
		"artifacts":        fmt.Sprintf("%d", batch.ArtifactCount),
		"deletion_markers": fmt.Sprintf("%d", batch.DeletionMarkerCount),
		"total_bytes":      fmt.Sprintf("%d", batch.TotalBytes),
		"received_at":      timeOrDash(batch.ReceivedAt),
		"completed_at":     timePtrOrDash(batch.CompletedAt),
	}
}

func backupBatchPtrID(batch *mainwatchedroots.BackupBatch) string {
	if batch == nil {
		return "-"
	}
	return firstNonEmpty(batch.WatchedRootBackupBatchID, "-")
}

func jobRetryDisabledReason(job jobs.Job) string {
	switch job.Status {
	case jobs.StatusFailed, jobs.StatusTimedOut:
	default:
		return "This job is not in a retryable state."
	}
	if job.MaxAttempts > 0 && job.AttemptCount >= job.MaxAttempts {
		return "This job has exhausted its configured attempts; force retry is not exposed in the portal yet."
	}
	return ""
}

func jobCancellable(job jobs.Job) bool {
	switch job.Status {
	case jobs.StatusCreated, jobs.StatusQueued, jobs.StatusRunning:
		return true
	default:
		return false
	}
}

func jobAttentionAcknowledgeDisabledReason(job jobs.Job) string {
	if !jobs.JobNeedsFailureAttention(job) {
		return "This job does not require failed-job attention."
	}
	switch jobs.EffectiveFailureAttentionStatus(job.FailureAttentionStatus) {
	case jobs.FailureAttentionStatusAcknowledged:
		return "This job attention is already acknowledged."
	case jobs.FailureAttentionStatusArchived:
		return "This job attention is already archived."
	default:
		return ""
	}
}

func jobAttentionArchiveDisabledReason(job jobs.Job) string {
	if !jobs.JobNeedsFailureAttention(job) {
		return "This job does not require failed-job attention."
	}
	if jobs.EffectiveFailureAttentionStatus(job.FailureAttentionStatus) == jobs.FailureAttentionStatusArchived {
		return "This job attention is already archived."
	}
	return ""
}

func deletionRequestLifecycleDisabledReason(request loomsync.DeletionRequest, verb string) string {
	status := loomsync.NormalizeDeletionRequestStatus(request.Status)
	if status == "" {
		status = loomsync.DeletionRequestStatusPendingReview
	}
	switch verb {
	case "review":
		if status == loomsync.DeletionRequestStatusPendingReview {
			return "This deletion request is already pending review."
		}
		if !loomsync.DeletionRequestStatusIsActive(status) {
			return "This deletion request is already resolved."
		}
	case "approve":
		if status == loomsync.DeletionRequestStatusApproved {
			return "This deletion request is already approved."
		}
		if status == loomsync.DeletionRequestStatusDenied || status == loomsync.DeletionRequestStatusCompleted {
			return "This deletion request is already resolved."
		}
	case "deny":
		if status == loomsync.DeletionRequestStatusDenied {
			return "This deletion request is already denied."
		}
		if status == loomsync.DeletionRequestStatusCompleted {
			return "This deletion request is already completed."
		}
	case "complete":
		if status == loomsync.DeletionRequestStatusCompleted {
			return "This deletion request is already completed."
		}
		if status == loomsync.DeletionRequestStatusDenied {
			return "This deletion request is denied."
		}
	}
	return ""
}

func capabilityCallDisabledReason(capability capabilities.CapabilityListItem) string {
	endpoint := capability.CapabilityEndpoint
	ref := firstNonEmpty(endpoint.CompactAddress, endpoint.CapabilityEndpointID)
	if ref == "" {
		return "This capability row does not include a callable target."
	}
	if endpoint.Status != "" && !strings.EqualFold(endpoint.Status, "active") {
		return "This capability endpoint is not active."
	}
	form := strings.ToLower(strings.TrimSpace(endpoint.Form))
	switch form {
	case "", "query", "read", "inspect":
	default:
		return "This capability form is not supported by the minimal portal input renderer."
	}
	risk := strings.ToLower(strings.TrimSpace(endpoint.RiskLevel))
	switch risk {
	case "", "low", "read", "readonly", "read_only", "inspect", "safe":
	default:
		return "This capability risk level requires a stronger portal policy."
	}
	if endpoint.ExecutionAuthorizationLevel > 1 {
		return "This capability requires a higher authorization level than the portal invokes directly in this slice."
	}
	return ""
}

func compactJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "-"
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	compact, err := json.Marshal(value)
	if err != nil {
		return string(raw)
	}
	if len(compact) == 0 || string(compact) == "null" {
		return "-"
	}
	return string(compact)
}
