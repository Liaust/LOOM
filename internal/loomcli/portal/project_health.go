package portal

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectdoctor"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
	"loom.local/loom/internal/routing"
)

const projectHealthDispatcherLagAfter = 30 * time.Second

type projectAutomationHealth struct {
	Ref                 string
	Project             projects.Project
	Detail              projects.ProjectRegistrationDetail
	BackendAnalysis     *projectdoctor.BackendAnalysisResult
	Plan                *projectcontracts.ProjectPlan
	Report              *projectcontracts.ValidationReport
	Providers           []capabilities.ProviderListItem
	Capabilities        []capabilities.CapabilityListItem
	RuntimeBindings     []capabilities.RuntimeBindingInspection
	Schedules           []automation.Schedule
	DirectEndpoints     []automation.DirectEventEndpoint
	DirectEvents        []automation.DirectEvent
	Invocations         []automation.Invocation
	InvocationFailures  []automation.Invocation
	Jobs                []jobs.Job
	FailedJobs          []jobs.Job
	CapabilityCalls     []routing.CapabilityCall
	SyncStatus          *projectwatch.ProjectSyncStatus
	BackupStatus        *projectwatch.ProjectBackupStatus
	PartialErrors       []string
	RequiredCredentials []string
	CredentialPolicy    string
}

func (e ActionExecutor) executeProjectAutomationHealth(ctx context.Context, action PortalAction) PortalActionResult {
	ref := firstNonEmpty(action.Executor.Target, action.TargetRef, action.Executor.Payload["project_ref"])
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if ref == "" {
		return failedActionResult(action, "portal.action_target_missing", "Project automation health action is missing a project ref.")
	}
	health := e.collectProjectAutomationHealth(ctx, ref)
	fields := projectAutomationHealthFields(health)
	summary := "Project automation health loaded."
	overall := projectAutomationHealthOverall(health)
	if overall != "ok" {
		summary = "Project automation health needs attention."
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       summary,
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: e.CorrelationID,
		RefreshScreen: action.RefreshScreen,
	}
}

func (e ActionExecutor) collectProjectAutomationHealth(ctx context.Context, ref string) projectAutomationHealth {
	health := projectAutomationHealth{Ref: ref}
	addPartial := func(label string, err error) {
		if err != nil {
			health.PartialErrors = append(health.PartialErrors, label+": "+err.Error())
		}
	}

	if envelope, err := e.Client.GetProjectRegistrationStatus(ctx, e.CorrelationID, ref); err != nil {
		addPartial("registration", err)
	} else {
		health.Detail = envelope.Data
		health.Project = envelope.Data.Project.Project
		if health.Ref == "" {
			health.Ref = projectRef(health.Project)
		}
		health.Plan = parseStoredProjectPlan(envelope.Data)
		health.Report = parseStoredValidationReport(envelope.Data)
		health.RequiredCredentials = requiredProjectCredentialRefs(health.Plan)
		health.CredentialPolicy = projectCredentialPolicyStatus(health.Plan, health.RequiredCredentials)
	}
	if health.Project.ProjectID == "" {
		if envelope, err := e.Client.GetProject(ctx, e.CorrelationID, ref); err != nil {
			addPartial("project", err)
		} else {
			health.Project = envelope.Data.Project
		}
	}

	projectRef := firstNonEmpty(health.Ref, ref)
	if projectRef == "" {
		projectRef = ref
	}
	if envelope, err := e.Client.AnalyzeProjectContractBackend(ctx, e.CorrelationID, projectcontracts.BackendAnalysisInput{ProjectRef: projectRef}); err != nil {
		addPartial("backend_analysis", err)
	} else {
		analysis := envelope.Data
		health.BackendAnalysis = &analysis
	}
	if envelope, err := e.Client.ListProviders(ctx, e.CorrelationID, capabilities.ProviderFilter{ProjectRef: projectRef, Limit: 100}); err != nil {
		addPartial("providers", err)
	} else {
		health.Providers = envelope.Data
	}
	if envelope, err := e.Client.ListCapabilities(ctx, e.CorrelationID, capabilities.CapabilityFilter{ProjectRef: projectRef, Limit: 200}); err != nil {
		addPartial("capabilities", err)
	} else {
		health.Capabilities = envelope.Data
	}
	health.RuntimeBindings = collectProjectRuntimeBindingsForHealth(ctx, e.Client, e.CorrelationID, health.Providers, addPartial)
	if envelope, err := e.Client.ListSchedules(ctx, e.CorrelationID, automation.ScheduleFilter{ProjectRef: projectRef, Limit: 50}); err != nil {
		addPartial("schedules", err)
	} else {
		health.Schedules = envelope.Data
	}
	if envelope, err := e.Client.ListDirectEventEndpoints(ctx, e.CorrelationID, automation.DirectEventEndpointFilter{ProjectRef: projectRef, Limit: 50}); err != nil {
		addPartial("direct_event_endpoints", err)
	} else {
		health.DirectEndpoints = envelope.Data
	}
	if envelope, err := e.Client.ListDirectEvents(ctx, e.CorrelationID, automation.DirectEventFilter{ProjectRef: projectRef, Limit: 20}); err != nil {
		addPartial("direct_events", err)
	} else {
		health.DirectEvents = envelope.Data
	}
	if envelope, err := e.Client.ListInvocations(ctx, e.CorrelationID, automation.InvocationFilter{ProjectRef: projectRef, Limit: 50}); err != nil {
		addPartial("invocations", err)
	} else {
		health.Invocations = envelope.Data
	}
	if envelope, err := e.Client.ListInvocationFailures(ctx, e.CorrelationID, automation.InvocationFilter{ProjectRef: projectRef, Limit: 50}); err != nil {
		addPartial("invocation_failures", err)
	} else {
		health.InvocationFailures = envelope.Data
	}
	if envelope, err := e.Client.ListJobs(ctx, e.CorrelationID, jobs.ListFilter{ProjectRef: projectRef, Limit: 50}); err != nil {
		addPartial("jobs", err)
	} else {
		health.Jobs = envelope.Data
	}
	if envelope, err := e.Client.ListFailedJobs(ctx, e.CorrelationID, jobs.ListFilter{ProjectRef: projectRef, Limit: 50}); err != nil {
		addPartial("failed_jobs", err)
	} else {
		health.FailedJobs = envelope.Data
	}
	scopeRef := firstNonEmpty(health.Project.ProjectScopeKey, health.Detail.Project.Project.ProjectScopeKey)
	if scopeRef != "" {
		if envelope, err := e.Client.ListCapabilityCalls(ctx, e.CorrelationID, routing.CapabilityCallFilter{ScopeRef: scopeRef, Limit: 50}); err != nil {
			addPartial("capability_calls", err)
		} else {
			health.CapabilityCalls = envelope.Data
		}
	}
	if envelope, err := e.Client.GetProjectSyncStatus(ctx, e.CorrelationID, projectRef); err != nil {
		addPartial("sync_status", err)
	} else {
		status := envelope.Data
		health.SyncStatus = &status
	}
	if envelope, err := e.Client.GetProjectBackupStatus(ctx, e.CorrelationID, projectRef); err != nil {
		addPartial("backup_status", err)
	} else {
		status := envelope.Data
		health.BackupStatus = &status
	}
	return health
}

func collectProjectRuntimeBindingsForHealth(ctx context.Context, client Client, correlationID string, providers []capabilities.ProviderListItem, addPartial func(string, error)) []capabilities.RuntimeBindingInspection {
	bindings := []capabilities.RuntimeBindingInspection{}
	seen := map[string]bool{}
	for _, provider := range providers {
		ref := firstNonEmpty(provider.Provider.CompactAddress, provider.Provider.ProviderID, provider.Provider.ProviderKey)
		if ref == "" {
			continue
		}
		envelope, err := client.ListRuntimeBindings(ctx, correlationID, capabilities.RuntimeBindingFilter{ProviderRef: ref, Limit: 50})
		if err != nil {
			addPartial("runtime_bindings", err)
			continue
		}
		for _, binding := range envelope.Data {
			id := firstNonEmpty(binding.Binding.RuntimeBindingID, binding.Endpoint.CompactAddress+":"+binding.Binding.RuntimeKind)
			if seen[id] {
				continue
			}
			seen[id] = true
			bindings = append(bindings, binding)
		}
	}
	return bindings
}

func projectAutomationHealthFields(health projectAutomationHealth) []ActionResultField {
	contractStatus, contractDetail := projectContractHealth(health)
	capStatus, capDetail := projectCapabilityHealth(health.Capabilities)
	runtimeStatus, runtimeDetail := projectRuntimeHealth(health.Capabilities, health.RuntimeBindings)
	credentialStatus, credentialDetail := projectCredentialHealth(health)
	scheduleStatus, scheduleDetail := projectScheduleHealth(health.Schedules)
	endpointStatus, endpointDetail := projectDirectEndpointHealth(health.DirectEndpoints)
	invocationStatus, invocationDetail := projectInvocationHealth(health.Invocations, health.InvocationFailures)
	jobStatus, jobDetail := projectJobHealth(health.Jobs, health.FailedJobs)
	callStatus, callDetail := projectCapabilityCallHealth(health.CapabilityCalls)
	dispatcherStatus, dispatcherDetail := projectDispatcherLagHealth(health.Invocations)
	syncStatus, syncDetail := projectSyncHealth(health.SyncStatus)
	backupStatus, backupDetail := projectBackupHealth(health.BackupStatus)
	overall := projectAutomationHealthOverall(health)

	fields := []ActionResultField{
		{Label: "Project", Value: firstNonEmpty(projectLabel(health.Project), health.Ref, "-")},
		{Label: "Owner Node", Value: firstNonEmpty(projectHealthOwnerNode(health), "-")},
		{Label: "Facets", Value: projectHealthFacetsSummary(health.Detail)},
		{Label: "Backend Drift", Value: healthValue(projectBackendDriftHealth(health))},
		{Label: "Overall", Value: healthValue(overall, projectAutomationHealthSummary(health))},
		{Label: "Contract", Value: healthValue(contractStatus, contractDetail)},
		{Label: "Activation", Value: healthValue(projectActivationHealthStatus(health.Detail), projectActivationHealthDetail(health.Detail))},
		{Label: "Target Capabilities", Value: healthValue(capStatus, capDetail)},
		{Label: "Runtime Bindings", Value: healthValue(runtimeStatus, runtimeDetail)},
		{Label: "Credentials", Value: healthValue(credentialStatus, credentialDetail)},
		{Label: "Schedules", Value: healthValue(scheduleStatus, scheduleDetail)},
		{Label: "Direct Events", Value: healthValue(endpointStatus, endpointDetail)},
		{Label: "Invocations", Value: healthValue(invocationStatus, invocationDetail)},
		{Label: "Jobs", Value: healthValue(jobStatus, jobDetail)},
		{Label: "Capability Calls", Value: healthValue(callStatus, callDetail)},
		{Label: "Dispatcher", Value: healthValue(dispatcherStatus, dispatcherDetail)},
		{Label: "Sync", Value: healthValue(syncStatus, syncDetail)},
		{Label: "Backup", Value: healthValue(backupStatus, backupDetail)},
	}
	if len(health.PartialErrors) > 0 {
		fields = append(fields, ActionResultField{Label: "Partial Data", Value: healthValue("warning", strings.Join(health.PartialErrors, "; "))})
	}
	return fields
}

func projectHealthOwnerNode(health projectAutomationHealth) string {
	owner := ptrOrDash(health.Project.HomeNodeID)
	if health.Plan != nil {
		owner = firstNonEmpty(health.Plan.Project.OwnerNode, owner)
	}
	if health.Report != nil {
		owner = firstNonEmpty(health.Report.Project.OwnerNode, owner)
	}
	return owner
}

func projectHealthFacetsSummary(detail projects.ProjectRegistrationDetail) string {
	if len(detail.Facets) == 0 {
		return "none"
	}
	values := make([]string, 0, len(detail.Facets))
	for _, facet := range detail.Facets {
		key := strings.TrimSpace(facet.FacetKey)
		if key == "" {
			continue
		}
		status := firstNonEmpty(facet.FacetStatus, "registered")
		values = append(values, key+"="+status)
	}
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ", ")
}

func projectAutomationHealthOverall(health projectAutomationHealth) string {
	statuses := []string{}
	add := func(status string, _ string) {
		statuses = append(statuses, status)
	}
	add(projectContractHealth(health))
	add(projectCapabilityHealth(health.Capabilities))
	add(projectRuntimeHealth(health.Capabilities, health.RuntimeBindings))
	add(projectCredentialHealth(health))
	add(projectScheduleHealth(health.Schedules))
	add(projectDirectEndpointHealth(health.DirectEndpoints))
	add(projectInvocationHealth(health.Invocations, health.InvocationFailures))
	add(projectJobHealth(health.Jobs, health.FailedJobs))
	add(projectCapabilityCallHealth(health.CapabilityCalls))
	add(projectDispatcherLagHealth(health.Invocations))
	add(projectSyncHealth(health.SyncStatus))
	add(projectBackupHealth(health.BackupStatus))
	if len(health.PartialErrors) > 0 {
		statuses = append(statuses, "warning")
	}
	return worstHealthStatus(statuses...)
}

func projectAutomationHealthSummary(health projectAutomationHealth) string {
	return fmt.Sprintf("%d capabilities, %d schedules, %d endpoints, %d jobs", len(health.Capabilities), len(health.Schedules), len(health.DirectEndpoints), len(health.Jobs))
}

func projectContractHealth(health projectAutomationHealth) (string, string) {
	reg := health.Detail.Registration
	if reg == nil {
		return "blocked", "no registered project contract"
	}
	if health.BackendAnalysis != nil {
		switch health.BackendAnalysis.DriftStatus {
		case projectdoctor.BackendDriftStale:
			return "warning", firstNonEmpty(health.BackendAnalysis.NextAction, "backend contract differs from registered snapshot")
		case projectdoctor.BackendDriftUnregistered:
			return "blocked", firstNonEmpty(health.BackendAnalysis.NextAction, "backend contract is not registered")
		case projectdoctor.BackendDriftInvalid:
			return "blocked", firstNonEmpty(health.BackendAnalysis.NextAction, "backend project contract is invalid")
		case projectdoctor.BackendDriftUnavailable:
			return "warning", firstNonEmpty(health.BackendAnalysis.NextAction, "backend contract drift is unavailable")
		}
	}
	errorsCount := 0
	warningsCount := 0
	if health.Report != nil {
		errorsCount += health.Report.Summary.Errors
		warningsCount += health.Report.Summary.Warnings
	}
	if health.Plan != nil {
		errorsCount += health.Plan.Summary.Errors
		warningsCount += health.Plan.Summary.Warnings
	}
	if errorsCount > 0 {
		return "blocked", fmt.Sprintf("%d validation/plan errors", errorsCount)
	}
	if warningsCount > 0 {
		return "warning", fmt.Sprintf("%d validation/plan warnings", warningsCount)
	}
	return "ok", firstNonEmpty(reg.RegistrationStatus, "registered")
}

func projectBackendDriftHealth(health projectAutomationHealth) (string, string) {
	if health.BackendAnalysis == nil {
		return "warning", "backend contract drift was not checked"
	}
	switch health.BackendAnalysis.DriftStatus {
	case projectdoctor.BackendDriftCurrent:
		return "ok", "backend contract matches registered snapshot"
	case projectdoctor.BackendDriftStale:
		return "warning", firstNonEmpty(health.BackendAnalysis.NextAction, "re-register contract from backend")
	case projectdoctor.BackendDriftUnregistered:
		return "blocked", firstNonEmpty(health.BackendAnalysis.NextAction, "register contract from backend")
	case projectdoctor.BackendDriftInvalid:
		return "blocked", firstNonEmpty(health.BackendAnalysis.NextAction, "fix backend project contract")
	case projectdoctor.BackendDriftUnavailable:
		return "warning", firstNonEmpty(health.BackendAnalysis.NextAction, "inspect registration and backend contract")
	default:
		return "warning", string(health.BackendAnalysis.DriftStatus)
	}
}

func projectActivationHealthStatus(detail projects.ProjectRegistrationDetail) string {
	if detail.Registration == nil {
		return "blocked"
	}
	status := strings.TrimSpace(strings.ToLower(detail.Registration.ActivationStatus))
	switch status {
	case "", "inactive", "registered":
		return "warning"
	case projects.ProjectActivationStatusBaseActive, "active", "activated":
		return "ok"
	case "failed", "error", "blocked":
		return "blocked"
	default:
		if strings.Contains(status, "active") {
			return "ok"
		}
		return "warning"
	}
}

func projectActivationHealthDetail(detail projects.ProjectRegistrationDetail) string {
	if detail.Registration == nil {
		return "not registered"
	}
	return firstNonEmpty(detail.Registration.ActivationStatus, detail.Registration.RegistrationStatus, "-")
}

func projectCapabilityHealth(items []capabilities.CapabilityListItem) (string, string) {
	if len(items) == 0 {
		return "blocked", "no project capabilities returned"
	}
	active, disabled, failed := countCapabilityStatuses(items)
	switch {
	case active == 0:
		return "blocked", fmt.Sprintf("0 active of %d capabilities", len(items))
	case disabled+failed > 0:
		return "warning", fmt.Sprintf("%d active, %d disabled/failed", active, disabled+failed)
	default:
		return "ok", fmt.Sprintf("%d active", active)
	}
}

func projectRuntimeHealth(capabilityItems []capabilities.CapabilityListItem, bindings []capabilities.RuntimeBindingInspection) (string, string) {
	if len(capabilityItems) == 0 {
		return "blocked", "no target capabilities"
	}
	if len(bindings) == 0 {
		return "blocked", "no runtime bindings returned"
	}
	active := 0
	disabled := 0
	for _, binding := range bindings {
		switch strings.ToLower(binding.Binding.Status) {
		case capabilities.RuntimeBindingStatusActive:
			active++
		case capabilities.RuntimeBindingStatusDisabled, capabilities.RuntimeBindingStatusRevoked:
			disabled++
		}
	}
	if active == 0 {
		return "blocked", fmt.Sprintf("0 active of %d runtime bindings", len(bindings))
	}
	if disabled > 0 {
		return "warning", fmt.Sprintf("%d active, %d disabled", active, disabled)
	}
	return "ok", fmt.Sprintf("%d active", active)
}

func projectCredentialHealth(health projectAutomationHealth) (string, string) {
	if len(health.RequiredCredentials) == 0 {
		return "ok", "no required credential refs in scripts/workflows"
	}
	detail := fmt.Sprintf("%d required: %s", len(health.RequiredCredentials), strings.Join(health.RequiredCredentials, ", "))
	if health.CredentialPolicy == "missing" {
		return "blocked", detail + "; credential policy missing"
	}
	if health.CredentialPolicy == "partial" {
		return "warning", detail + "; credential policy incomplete"
	}
	return "ok", detail + "; availability checked during activation"
}

func projectScheduleHealth(items []automation.Schedule) (string, string) {
	if len(items) == 0 {
		return "ok", "no schedules registered"
	}
	active := countByStatus(items, func(item automation.Schedule) string { return item.Status }, automation.ScheduleStatusActive)
	paused := countByStatus(items, func(item automation.Schedule) string { return item.Status }, automation.ScheduleStatusPaused)
	disabled := countByStatus(items, func(item automation.Schedule) string { return item.Status }, automation.ScheduleStatusDisabled)
	if active == 0 {
		return "warning", fmt.Sprintf("0 active, %d paused, %d disabled", paused, disabled)
	}
	if paused+disabled > 0 {
		return "warning", fmt.Sprintf("%d active, %d paused, %d disabled", active, paused, disabled)
	}
	return "ok", fmt.Sprintf("%d active", active)
}

func projectDirectEndpointHealth(items []automation.DirectEventEndpoint) (string, string) {
	if len(items) == 0 {
		return "ok", "no direct-event endpoints registered"
	}
	active := countByStatus(items, func(item automation.DirectEventEndpoint) string { return item.Status }, automation.DirectEventEndpointStatusActive)
	paused := countByStatus(items, func(item automation.DirectEventEndpoint) string { return item.Status }, automation.DirectEventEndpointStatusPaused)
	disabled := countByStatus(items, func(item automation.DirectEventEndpoint) string { return item.Status }, automation.DirectEventEndpointStatusDisabled)
	if active == 0 {
		return "warning", fmt.Sprintf("0 active, %d paused, %d disabled", paused, disabled)
	}
	if paused+disabled > 0 {
		return "warning", fmt.Sprintf("%d active, %d paused, %d disabled", active, paused, disabled)
	}
	return "ok", fmt.Sprintf("%d active", active)
}

func projectInvocationHealth(invocations []automation.Invocation, failures []automation.Invocation) (string, string) {
	if len(invocations) == 0 && len(failures) == 0 {
		return "ok", "no recent invocations"
	}
	failed := len(failures)
	pending := 0
	running := 0
	succeeded := 0
	for _, invocation := range invocations {
		switch invocation.Status {
		case automation.InvocationStatusSucceeded:
			succeeded++
		case automation.InvocationStatusPending:
			pending++
		case automation.InvocationStatusLeased, automation.InvocationStatusCalling:
			running++
		case automation.InvocationStatusFailed, automation.InvocationStatusTimedOut, automation.InvocationStatusRequiresManualAction:
			failed++
		}
	}
	if failed > 0 {
		return "blocked", fmt.Sprintf("%d failed/manual, %d pending, %d running, %d succeeded", failed, pending, running, succeeded)
	}
	if pending+running > 0 {
		return "warning", fmt.Sprintf("%d pending, %d running, %d succeeded", pending, running, succeeded)
	}
	return "ok", fmt.Sprintf("%d succeeded", succeeded)
}

func projectJobHealth(recent []jobs.Job, failedJobs []jobs.Job) (string, string) {
	if len(recent) == 0 && len(failedJobs) == 0 {
		return "ok", "no recent jobs"
	}
	failed := len(failedJobs)
	queued := 0
	running := 0
	completed := 0
	for _, job := range recent {
		switch job.Status {
		case jobs.StatusQueued, jobs.StatusCreated:
			queued++
		case jobs.StatusRunning:
			running++
		case jobs.StatusCompleted:
			completed++
		case jobs.StatusFailed, jobs.StatusTimedOut, jobs.StatusCancelled:
			failed++
		}
	}
	if failed > 0 {
		return "blocked", fmt.Sprintf("%d failed, %d running, %d queued, %d completed", failed, running, queued, completed)
	}
	if running+queued > 0 {
		return "warning", fmt.Sprintf("%d running, %d queued, %d completed", running, queued, completed)
	}
	return "ok", fmt.Sprintf("%d completed", completed)
}

func projectCapabilityCallHealth(calls []routing.CapabilityCall) (string, string) {
	if len(calls) == 0 {
		return "ok", "no recent capability calls"
	}
	failed := 0
	active := 0
	completed := 0
	for _, call := range calls {
		switch call.Status {
		case routing.CapabilityCallStatusCompleted:
			completed++
		case routing.CapabilityCallStatusFailed, routing.CapabilityCallStatusCancelled:
			failed++
		case routing.CapabilityCallStatusPlanned, routing.CapabilityCallStatusAuthorized, routing.CapabilityCallStatusDispatched, routing.CapabilityCallStatusExecuting, routing.CapabilityCallStatusApprovalRequired:
			active++
		}
	}
	if failed > 0 {
		return "blocked", fmt.Sprintf("%d failed, %d active, %d completed", failed, active, completed)
	}
	if active > 0 {
		return "warning", fmt.Sprintf("%d active, %d completed", active, completed)
	}
	return "ok", fmt.Sprintf("%d completed", completed)
}

func projectDispatcherLagHealth(invocations []automation.Invocation) (string, string) {
	now := time.Now().UTC()
	var oldest *time.Time
	count := 0
	for _, invocation := range invocations {
		switch invocation.Status {
		case automation.InvocationStatusPending, automation.InvocationStatusLeased, automation.InvocationStatusCalling:
			count++
			created := invocation.CreatedAt
			if created.IsZero() {
				created = invocation.UpdatedAt
			}
			if created.IsZero() {
				continue
			}
			if oldest == nil || created.Before(*oldest) {
				oldest = &created
			}
		}
	}
	if count == 0 {
		return "ok", "no pending dispatcher work"
	}
	if oldest == nil {
		return "warning", fmt.Sprintf("%d pending invocation(s)", count)
	}
	lag := now.Sub(oldest.UTC())
	if lag < 0 {
		lag = 0
	}
	if lag >= projectHealthDispatcherLagAfter {
		return "warning", fmt.Sprintf("%d pending invocation(s), oldest waiting %s", count, lag.Round(time.Second))
	}
	return "ok", fmt.Sprintf("%d pending invocation(s), oldest waiting %s", count, lag.Round(time.Second))
}

func projectSyncHealth(status *projectwatch.ProjectSyncStatus) (string, string) {
	if status == nil {
		return "warning", "sync status unavailable"
	}
	if status.Blocked > 0 {
		return "blocked", fmt.Sprintf("%d blocked, %d stale, %d reported", status.Blocked, status.Stale, status.Reported)
	}
	if status.Stale > 0 || status.PendingAgentApply > 0 {
		return "warning", fmt.Sprintf("%d stale, %d pending apply, %d roots", status.Stale, status.PendingAgentApply, status.SyncRoots)
	}
	return "ok", fmt.Sprintf("%d roots, %d reported", status.SyncRoots, status.Reported)
}

func projectBackupHealth(status *projectwatch.ProjectBackupStatus) (string, string) {
	if status == nil {
		return "warning", "backup status unavailable"
	}
	if status.Blocked > 0 {
		return "blocked", fmt.Sprintf("%d blocked, %d stale, %d reported", status.Blocked, status.Stale, status.Reported)
	}
	if status.Stale > 0 || status.PendingAgentApply > 0 {
		return "warning", fmt.Sprintf("%d stale, %d pending apply, %d roots", status.Stale, status.PendingAgentApply, status.BackupRoots)
	}
	return "ok", fmt.Sprintf("%d roots, %d reported", status.BackupRoots, status.Reported)
}

func parseStoredProjectPlan(detail projects.ProjectRegistrationDetail) *projectcontracts.ProjectPlan {
	if detail.Registration == nil || len(detail.Registration.RegistrationPlan) == 0 {
		return nil
	}
	var plan projectcontracts.ProjectPlan
	if err := json.Unmarshal(detail.Registration.RegistrationPlan, &plan); err != nil {
		return nil
	}
	return &plan
}

func parseStoredValidationReport(detail projects.ProjectRegistrationDetail) *projectcontracts.ValidationReport {
	if detail.Registration == nil || len(detail.Registration.ValidationReport) == 0 {
		return nil
	}
	var report projectcontracts.ValidationReport
	if err := json.Unmarshal(detail.Registration.ValidationReport, &report); err != nil {
		return nil
	}
	return &report
}

func requiredProjectCredentialRefs(plan *projectcontracts.ProjectPlan) []string {
	if plan == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, script := range plan.Scripts {
		for _, requirement := range script.Credentials.Required {
			ref := strings.TrimSpace(requirement.Ref)
			if ref != "" {
				seen[ref] = true
			}
		}
	}
	for _, workflow := range plan.Workflows {
		for _, requirement := range workflow.Credentials.Required {
			ref := strings.TrimSpace(requirement.Ref)
			if ref != "" {
				seen[ref] = true
			}
		}
	}
	refs := make([]string, 0, len(seen))
	for ref := range seen {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs
}

func projectCredentialPolicyStatus(plan *projectcontracts.ProjectPlan, required []string) string {
	if len(required) == 0 {
		return "none"
	}
	if plan == nil {
		return "missing"
	}
	for _, ref := range plan.PolicyRefs {
		if ref.Key == "credentials" {
			if ref.Present {
				return "present"
			}
			return "partial"
		}
	}
	return "missing"
}

func countCapabilityStatuses(items []capabilities.CapabilityListItem) (active int, disabled int, failed int) {
	for _, item := range items {
		switch strings.ToLower(item.Status) {
		case capabilities.EndpointStatusActive:
			active++
		case capabilities.EndpointStatusDisabled, capabilities.EndpointStatusRevoked:
			disabled++
		case "failed", "error":
			failed++
		}
	}
	return active, disabled, failed
}

func countByStatus[T any](items []T, statusFn func(T) string, status string) int {
	count := 0
	for _, item := range items {
		if strings.EqualFold(statusFn(item), status) {
			count++
		}
	}
	return count
}

func healthValue(status string, detail string) string {
	return firstNonEmpty(status, "unknown") + " - " + firstNonEmpty(detail, "-")
}

func worstHealthStatus(statuses ...string) string {
	worst := "ok"
	rank := func(status string) int {
		switch strings.ToLower(strings.TrimSpace(status)) {
		case "blocked", "failed", "error", "critical":
			return 3
		case "warning", "degraded", "partial":
			return 2
		case "unknown":
			return 1
		default:
			return 0
		}
	}
	for _, status := range statuses {
		if rank(status) > rank(worst) {
			worst = status
		}
	}
	return worst
}
