package portal

import (
	"encoding/json"
	"fmt"
	"strings"

	"loom.local/loom/internal/loomcli/actions"
)

type PortalActionRisk string

const (
	ActionRiskInspect   PortalActionRisk = "inspect"
	ActionRiskSafeRun   PortalActionRisk = "safe_run"
	ActionRiskSensitive PortalActionRisk = "sensitive"
	ActionRiskDangerous PortalActionRisk = "dangerous"
	ActionRiskBlocked   PortalActionRisk = "blocked"
)

type PortalActionAvailability string

const (
	ActionAvailable PortalActionAvailability = "available"
	ActionDisabled  PortalActionAvailability = "disabled"
)

type ExecutionDependency string

const (
	ExecutionDependencyNavigation ExecutionDependency = "navigation"
	ExecutionDependencyLocal      ExecutionDependency = "local"
	ExecutionDependencyMain       ExecutionDependency = "main"
)

const mainOfflineExecutionReason = "Main is offline. This action requires the main node."

type PortalActionLifecycle string

const (
	ActionLifecycleIdle              PortalActionLifecycle = "idle"
	ActionLifecyclePreview           PortalActionLifecycle = "preview"
	ActionLifecycleNeedsInput        PortalActionLifecycle = "needs_input"
	ActionLifecyclePreflighting      PortalActionLifecycle = "preflighting"
	ActionLifecycleReview            PortalActionLifecycle = "review"
	ActionLifecycleNeedsConfirmation PortalActionLifecycle = "needs_confirmation"
	ActionLifecycleMutation          PortalActionLifecycle = "mutation"
	ActionLifecycleRunning           PortalActionLifecycle = "running"
	ActionLifecycleWaiting           PortalActionLifecycle = "waiting"
	ActionLifecycleSucceeded         PortalActionLifecycle = "succeeded"
	ActionLifecycleFailed            PortalActionLifecycle = "failed"
	ActionLifecycleCancelled         PortalActionLifecycle = "cancelled"
)

type PortalActionInteraction string

const (
	ActionInteractionDirectInspect PortalActionInteraction = "direct_inspect"
	ActionInteractionDirectRun     PortalActionInteraction = "direct_run"
	ActionInteractionFormRun       PortalActionInteraction = "form_run"
	ActionInteractionWizard        PortalActionInteraction = "wizard"
	ActionInteractionRawDetails    PortalActionInteraction = "raw_details_only"
)

const (
	ActionFieldText         = "text"
	ActionFieldTextareaJSON = "textarea_json"
	ActionFieldSelect       = "select"
	ActionFieldBoolean      = "boolean"
	ActionFieldNumber       = "number"
	ActionFieldPath         = "path"
	ActionFieldRef          = "ref"
	ActionFieldSecret       = "secret"
	ActionFieldReadonly     = "readonly"

	ConfirmationStrengthNormal = "normal"
	ConfirmationStrengthStrong = "strong"

	PortalExecutorNavigate                = "portal.navigate"
	PortalExecutorWorkerInspect           = "worker.inspect"
	PortalExecutorWorkerRunsInspect       = "worker.runs.inspect"
	PortalExecutorWorkerRunOnce           = "worker.run_once"
	PortalExecutorIndexInspect            = "index.inspect"
	PortalExecutorIndexExplainObject      = "index.explain_object"
	PortalExecutorIndexRetry              = "index.retry"
	PortalExecutorIndexRetryFailed        = "index.retry_failed"
	PortalExecutorIndexRebuildObject      = "index.rebuild_object"
	PortalExecutorMaintenanceInspect      = "maintenance.inspect"
	PortalExecutorMaintenanceBackupRun    = "maintenance.backup.run"
	PortalExecutorMaintenanceBackupVerify = "maintenance.backup.verify"
	PortalExecutorMaintenanceObjectScan   = "maintenance.object_store.scan"
	PortalExecutorCloudStatusLive         = "cloud.status.live"
	PortalExecutorCloudCooldownInspect    = "cloud.cooldown.inspect"
	PortalExecutorObjectInspect           = "object.inspect"
	PortalExecutorRecordInspect           = "record.inspect"
	PortalExecutorScheduleInspect         = "automation.schedule.inspect"
	PortalExecutorScheduleFireNow         = "automation.schedule.fire_now"
	PortalExecutorSchedulePause           = "automation.schedule.pause"
	PortalExecutorScheduleResume          = "automation.schedule.resume"
	PortalExecutorScheduleFireInspect     = "automation.schedule_fire.inspect"
	PortalExecutorDirectEndpointInspect   = "automation.direct_event_endpoint.inspect"
	PortalExecutorDirectEventInspect      = "automation.direct_event.inspect"
	PortalExecutorDirectEventRawPayload   = "automation.direct_event.raw_payload"
	PortalExecutorInvocationInspect       = "automation.invocation.inspect"
	PortalExecutorProviderInspect         = "capability.provider.inspect"
	PortalExecutorProviderHealth          = "capability.provider.health"
	PortalExecutorProviderAdInspect       = "capability.provider_advertisement.inspect"
	PortalExecutorCapabilityInspect       = "capability.inspect"
	PortalExecutorCapabilityUsageDocs     = "capability.usage_docs"
	PortalExecutorCapabilityCallInspect   = "capability.call.inspect"
	PortalExecutorCapabilityCall          = "capability.call"
	PortalExecutorRuntimeBindingInspect   = "capability.runtime_binding.inspect"
	PortalExecutorProjectInspect          = "project.inspect"
	PortalExecutorProjectValidateLocal    = "project.validate_local"
	PortalExecutorProjectValidateBackend  = "project.validate_backend"
	PortalExecutorProjectRegisterBackend  = "project.register_backend"
	PortalExecutorProjectRegistrationPlan = "project.registration_plan"
	PortalExecutorProjectDoctor           = "project.doctor"
	PortalExecutorProjectDiff             = "project.diff"
	PortalExecutorProjectActivate         = "project.activate"
	PortalExecutorProjectDeactivate       = "project.deactivate"
	PortalExecutorProjectArchive          = "project.archive"
	PortalExecutorProjectArchiveInspect   = "project.archive.inspect"
	PortalExecutorProjectArchiveRestore   = "project.archive.restore"
	PortalExecutorProjectAutomationHealth = "project.automation_health"
	PortalExecutorProjectScaffoldBackend  = "project.scaffold.backend"
	PortalExecutorProjectScaffoldCleanup  = "project.scaffold.cleanup"
	PortalExecutorProjectAddFacet         = "project.facet.add"
	PortalExecutorProjectMigrateLayout    = "project.layout.migrate"
	PortalExecutorProjectFacetInspect     = "project.facet.inspect"
	PortalExecutorProjectWatchPlan        = "project.watch_plan"
	PortalExecutorProjectSyncStatus       = "project.sync_status"
	PortalExecutorProjectBackupStatus     = "project.backup_status"
	PortalExecutorProjectExportLocal      = "project.export.local"
	PortalExecutorProjectExportBackend    = "project.export.backend"
	PortalExecutorBoxInit                 = "box.init"
	PortalExecutorBoxProjectScaffold      = "box.project.scaffold"
	PortalExecutorBoxWatchApply           = "box.watch.apply"
	PortalExecutorBoxDropzoneRetry        = "box.dropzone.retry"
	PortalExecutorBoxDropzonePause        = "box.dropzone.pause"
	PortalExecutorBoxDropzoneResume       = "box.dropzone.resume"
	PortalExecutorBoxDropzoneClean        = "box.dropzone.clean"
	PortalExecutorBoxLaneSend             = "box.lane.send"
	PortalExecutorBoxLanePlan             = "box.lane.plan"
	PortalExecutorBoxIgnoreInspect        = "box.ignore.inspect"
	PortalExecutorBoxLanePendingAck       = "box.lane.pending.acknowledge"
	PortalExecutorBoxLaneTransferAck      = "box.lane.transfer.acknowledge"
	PortalExecutorBoxLaneTransferArchive  = "box.lane.transfer.archive"
	PortalExecutorStorageInspect          = "storage.inspect"
	PortalExecutorStorageSafeToDelete     = "storage.safe_to_delete"
	PortalExecutorStorageFetch            = "storage.fetch"
	PortalExecutorStorageRestore          = "storage.restore"
	PortalExecutorStorageArchive          = "storage.archive"
	PortalExecutorStorageRetentionStatus  = "storage.retention.status"
	PortalExecutorMainDocumentsStatus     = "storage.main_documents.status"
	PortalExecutorDeletionRequestInspect  = "sync.deletion_request.inspect"
	PortalExecutorDeletionRequestReview   = "sync.deletion_request.review"
	PortalExecutorDeletionRequestApprove  = "sync.deletion_request.approve"
	PortalExecutorDeletionRequestDeny     = "sync.deletion_request.deny"
	PortalExecutorDeletionRequestComplete = "sync.deletion_request.complete"
	PortalExecutorJobInspect              = "job.inspect"
	PortalExecutorJobLogsInspect          = "job.logs.inspect"
	PortalExecutorJobOutputsInspect       = "job.outputs.inspect"
	PortalExecutorJobRetry                = "job.retry"
	PortalExecutorJobCancel               = "job.cancel"
	PortalExecutorJobAttentionAcknowledge = "job.attention.acknowledge"
	PortalExecutorJobAttentionArchive     = "job.attention.archive"
	PortalExecutorNodeInspect             = "node.inspect"
	PortalExecutorNodeHealth              = "node.health"
	PortalExecutorProtectedFolderProtect  = "backup.protected_folder.protect"
	PortalExecutorProtectedFolderInspect  = "backup.protected_folder.inspect"
	PortalExecutorProtectedFolderEnable   = "backup.protected_folder.enable"
	PortalExecutorProtectedFolderDisable  = "backup.protected_folder.disable"
	PortalExecutorProtectedFolderRecheck  = "backup.protected_folder.recheck"
	PortalExecutorProtectedFolderRetry    = "backup.protected_folder.retry"
	PortalExecutorProtectedFolderDelete   = "backup.protected_folder.delete"
	PortalExecutorNotesEmbeddingsToggle   = "notes.embeddings.toggle"
	PortalExecutorUnsupported             = "unsupported"
)

type PortalAction struct {
	ID                  string
	Label               string
	Description         string
	Domain              string
	SourceScreen        string
	TargetKind          string
	TargetRef           string
	TargetLabel         string
	Risk                PortalActionRisk
	State               PortalActionAvailability
	DisabledReason      string
	InputFields         []PortalActionField
	InputValues         map[string]string
	InteractionType     PortalActionInteraction
	ConfirmationPolicy  ConfirmationPolicy
	Executor            PortalActionExecutor
	ExecutionDependency ExecutionDependency
	RawCommand          []string
	RawDetails          map[string]string
	RefreshScreen       string
}

type PortalActionField struct {
	Name        string
	Label       string
	Kind        string
	Required    bool
	Value       string
	Options     []string
	Placeholder string
	Help        string
	Error       string
}

type ConfirmationPolicy struct {
	Required bool
	Strength string
	Prompt   string
}

type PortalActionExecutor struct {
	Kind    string
	Target  string
	Payload map[string]string
}

type ActionPanelState struct {
	Lifecycle      PortalActionLifecycle
	Action         PortalAction
	Result         PortalActionResult
	Error          string
	SelectedField  int
	SelectedButton int
	RawDetails     bool
}

type PortalActionResult struct {
	ActionID       string
	Title          string
	Status         PortalActionLifecycle
	Summary        string
	Fields         []ActionResultField
	RawCommand     []string
	RawResponse    string
	ErrorCode      string
	ErrorMessage   string
	CorrelationID  string
	IdempotencyKey string
	RefreshScreen  string
	NextInput      map[string]string
	Blocking       bool
}

type ActionResultField struct {
	Label string
	Value string
}

func PortalActionFromRegistry(action actions.Action) PortalAction {
	risk := portalRiskFromRegistry(action.Risk)
	executor := PortalActionExecutor{
		Kind:   action.ExecutionKind,
		Target: action.ExecutionTarget,
	}
	if executor.Kind == "" && action.StartScreen != "" {
		executor.Kind = PortalExecutorNavigate
		executor.Target = NormalizeScreen(action.StartScreen)
	}
	if executor.Kind == "" {
		executor.Kind = PortalExecutorUnsupported
	}

	state := ActionAvailable
	disabledReason := ""
	if !action.Enabled {
		state = ActionDisabled
		disabledReason = "This action is disabled."
	}
	if risk == ActionRiskDangerous {
		state = ActionDisabled
		disabledReason = firstNonEmpty(disabledReason, "Dangerous actions are blocked in this portal slice.")
	}

	targetRef := action.ExecutionTarget
	if targetRef == "" {
		targetRef = action.StartScreen
	}
	targetKind := ""
	switch executor.Kind {
	case PortalExecutorNavigate:
		targetKind = "screen"
	case PortalExecutorWorkerRunOnce:
		targetKind = "worker"
	default:
		targetKind = "action"
	}

	portalAction := PortalAction{
		ID:                  action.ID,
		Label:               action.Title,
		Description:         action.Description,
		Domain:              action.Domain,
		SourceScreen:        NormalizeScreen(action.StartScreen),
		TargetKind:          targetKind,
		TargetRef:           targetRef,
		TargetLabel:         firstNonEmpty(action.ExecutionTarget, action.StartScreen, action.Title),
		Risk:                risk,
		State:               state,
		DisabledReason:      disabledReason,
		InputValues:         map[string]string{},
		Executor:            executor,
		ExecutionDependency: portalExecutorDependency(executor.Kind),
		RawCommand:          append([]string{}, action.RawCommand...),
		RawDetails: map[string]string{
			"registry_action_id": action.ID,
			"execution_kind":     action.ExecutionKind,
			"execution_target":   action.ExecutionTarget,
		},
		RefreshScreen: NormalizeScreen(action.StartScreen),
	}
	portalAction.ConfirmationPolicy = confirmationPolicyForRisk(portalAction.Risk)
	return portalAction
}

func portalExecutorDependency(kind string) ExecutionDependency {
	dependency, _ := portalExecutorDependencyDecision(kind)
	return dependency
}

func portalExecutorDependencyDecision(kind string) (ExecutionDependency, bool) {
	switch kind {
	case PortalExecutorNavigate:
		return ExecutionDependencyNavigation, true
	case PortalExecutorBoxInit,
		PortalExecutorProjectValidateLocal,
		PortalExecutorProjectExportLocal,
		PortalExecutorBoxLanePlan,
		PortalExecutorBoxIgnoreInspect:
		return ExecutionDependencyLocal, true
	case PortalExecutorWorkerInspect,
		PortalExecutorWorkerRunsInspect,
		PortalExecutorWorkerRunOnce,
		PortalExecutorIndexInspect,
		PortalExecutorIndexExplainObject,
		PortalExecutorIndexRetry,
		PortalExecutorIndexRetryFailed,
		PortalExecutorIndexRebuildObject,
		PortalExecutorMaintenanceInspect,
		PortalExecutorMaintenanceBackupRun,
		PortalExecutorMaintenanceBackupVerify,
		PortalExecutorMaintenanceObjectScan,
		PortalExecutorCloudStatusLive,
		PortalExecutorCloudCooldownInspect,
		PortalExecutorObjectInspect,
		PortalExecutorRecordInspect,
		PortalExecutorScheduleInspect,
		PortalExecutorScheduleFireNow,
		PortalExecutorSchedulePause,
		PortalExecutorScheduleResume,
		PortalExecutorScheduleFireInspect,
		PortalExecutorDirectEndpointInspect,
		PortalExecutorDirectEventInspect,
		PortalExecutorDirectEventRawPayload,
		PortalExecutorInvocationInspect,
		PortalExecutorProviderInspect,
		PortalExecutorProviderHealth,
		PortalExecutorProviderAdInspect,
		PortalExecutorCapabilityInspect,
		PortalExecutorCapabilityUsageDocs,
		PortalExecutorCapabilityCallInspect,
		PortalExecutorCapabilityCall,
		PortalExecutorRuntimeBindingInspect,
		PortalExecutorProjectInspect,
		PortalExecutorProjectValidateBackend,
		PortalExecutorProjectRegisterBackend,
		PortalExecutorProjectRegistrationPlan,
		PortalExecutorProjectDoctor,
		PortalExecutorProjectDiff,
		PortalExecutorProjectActivate,
		PortalExecutorProjectDeactivate,
		PortalExecutorProjectArchive,
		PortalExecutorProjectArchiveInspect,
		PortalExecutorProjectArchiveRestore,
		PortalExecutorProjectAutomationHealth,
		PortalExecutorProjectScaffoldBackend,
		PortalExecutorProjectScaffoldCleanup,
		PortalExecutorProjectAddFacet,
		PortalExecutorProjectMigrateLayout,
		PortalExecutorProjectFacetInspect,
		PortalExecutorProjectWatchPlan,
		PortalExecutorProjectSyncStatus,
		PortalExecutorProjectBackupStatus,
		PortalExecutorProjectExportBackend,
		PortalExecutorBoxProjectScaffold,
		PortalExecutorBoxWatchApply,
		PortalExecutorBoxDropzoneRetry,
		PortalExecutorBoxDropzonePause,
		PortalExecutorBoxDropzoneResume,
		PortalExecutorBoxDropzoneClean,
		PortalExecutorBoxLaneSend,
		PortalExecutorBoxLanePendingAck,
		PortalExecutorBoxLaneTransferAck,
		PortalExecutorBoxLaneTransferArchive,
		PortalExecutorStorageInspect,
		PortalExecutorStorageSafeToDelete,
		PortalExecutorStorageFetch,
		PortalExecutorStorageRestore,
		PortalExecutorStorageArchive,
		PortalExecutorStorageRetentionStatus,
		PortalExecutorMainDocumentsStatus,
		PortalExecutorDeletionRequestInspect,
		PortalExecutorDeletionRequestReview,
		PortalExecutorDeletionRequestApprove,
		PortalExecutorDeletionRequestDeny,
		PortalExecutorDeletionRequestComplete,
		PortalExecutorJobInspect,
		PortalExecutorJobLogsInspect,
		PortalExecutorJobOutputsInspect,
		PortalExecutorJobRetry,
		PortalExecutorJobCancel,
		PortalExecutorJobAttentionAcknowledge,
		PortalExecutorJobAttentionArchive,
		PortalExecutorNodeInspect,
		PortalExecutorNodeHealth,
		PortalExecutorProtectedFolderProtect,
		PortalExecutorProtectedFolderInspect,
		PortalExecutorProtectedFolderEnable,
		PortalExecutorProtectedFolderDisable,
		PortalExecutorProtectedFolderRecheck,
		PortalExecutorProtectedFolderRetry,
		PortalExecutorProtectedFolderDelete,
		PortalExecutorNotesEmbeddingsToggle,
		PortalExecutorUnsupported:
		return ExecutionDependencyMain, true
	default:
		return ExecutionDependencyMain, false
	}
}

func applyMainAvailabilityToAction(action PortalAction, availability MainAvailability) PortalAction {
	if action.ExecutionDependency == "" {
		action.ExecutionDependency = portalExecutorDependency(action.Executor.Kind)
	}
	if availability.State != MainAvailabilityOffline && action.DisabledReason == mainOfflineExecutionReason {
		action.State = ActionAvailable
		action.DisabledReason = ""
	}
	if availability.State == MainAvailabilityOffline && action.ExecutionDependency == ExecutionDependencyMain && !action.Disabled() {
		action.State = ActionDisabled
		action.DisabledReason = mainOfflineExecutionReason
	}
	return action
}

func portalRiskFromRegistry(risk actions.RiskLevel) PortalActionRisk {
	switch risk {
	case actions.RiskReadOnly:
		return ActionRiskInspect
	case actions.RiskSafeRun:
		return ActionRiskSafeRun
	case actions.RiskStateChange:
		return ActionRiskSensitive
	case actions.RiskDestructive:
		return ActionRiskDangerous
	default:
		return ActionRiskBlocked
	}
}

func confirmationPolicyForRisk(risk PortalActionRisk) ConfirmationPolicy {
	switch risk {
	case ActionRiskInspect:
		return ConfirmationPolicy{Required: false, Strength: ConfirmationStrengthNormal}
	case ActionRiskSafeRun:
		return ConfirmationPolicy{
			Required: true,
			Strength: ConfirmationStrengthNormal,
			Prompt:   "Confirm this bounded action before it runs.",
		}
	case ActionRiskSensitive:
		return ConfirmationPolicy{
			Required: true,
			Strength: ConfirmationStrengthNormal,
			Prompt:   "Confirm this sensitive action before it runs.",
		}
	case ActionRiskDangerous:
		return ConfirmationPolicy{
			Required: true,
			Strength: ConfirmationStrengthStrong,
			Prompt:   "Dangerous actions are blocked until a stronger policy is implemented.",
		}
	default:
		return ConfirmationPolicy{
			Required: true,
			Strength: ConfirmationStrengthStrong,
			Prompt:   "This action is blocked.",
		}
	}
}

func (a PortalAction) ValidateInput() []PortalActionField {
	fields := append([]PortalActionField{}, a.InputFields...)
	values := a.InputValues
	for idx := range fields {
		field := &fields[idx]
		field.Error = ""
		value := field.Value
		if values != nil {
			value = firstNonEmpty(values[field.Name], value)
		}
		if field.Required && strings.TrimSpace(value) == "" {
			field.Error = "Required."
			continue
		}
		if field.Kind == ActionFieldSelect && len(field.Options) > 0 && strings.TrimSpace(value) != "" && !stringInSlice(strings.TrimSpace(value), field.Options) {
			field.Error = "Choose one of: " + strings.Join(field.Options, ", ")
			continue
		}
		if field.Kind == ActionFieldTextareaJSON && strings.TrimSpace(value) != "" && !json.Valid([]byte(value)) {
			field.Error = "Invalid JSON."
		}
	}
	return fields
}

func (a PortalAction) Interaction() PortalActionInteraction {
	if a.InteractionType != "" {
		return a.InteractionType
	}
	if len(a.InputFields) > 0 {
		return ActionInteractionFormRun
	}
	if a.Executor.Kind == PortalExecutorUnsupported || a.Risk == ActionRiskBlocked {
		return ActionInteractionRawDetails
	}
	if a.Executor.Kind == PortalExecutorNavigate || a.Risk == ActionRiskInspect {
		return ActionInteractionDirectInspect
	}
	if a.IsEffectful() {
		return ActionInteractionDirectRun
	}
	return ActionInteractionDirectInspect
}

func (a PortalAction) InteractionLabel() string {
	switch a.Interaction() {
	case ActionInteractionDirectInspect:
		return "direct inspect"
	case ActionInteractionDirectRun:
		return "direct run"
	case ActionInteractionFormRun:
		return "form run"
	case ActionInteractionWizard:
		return "wizard"
	case ActionInteractionRawDetails:
		return "raw/details only"
	default:
		return string(a.Interaction())
	}
}

func (a PortalAction) FieldValue(field PortalActionField) string {
	if a.InputValues != nil {
		if value, ok := a.InputValues[field.Name]; ok {
			return value
		}
	}
	return field.Value
}

func (a PortalAction) FieldValueByName(name string) string {
	if a.InputValues != nil {
		if value, ok := a.InputValues[name]; ok {
			return value
		}
	}
	for _, field := range a.InputFields {
		if field.Name == name {
			return field.Value
		}
	}
	return ""
}

func (a *PortalAction) EnsureInputValues() {
	if a.InputValues == nil {
		a.InputValues = map[string]string{}
	}
	for _, field := range a.InputFields {
		if _, ok := a.InputValues[field.Name]; !ok && field.Value != "" {
			a.InputValues[field.Name] = field.Value
		}
	}
}

func (a *PortalAction) SetFieldValue(fieldName string, value string) {
	a.EnsureInputValues()
	a.InputValues[fieldName] = value
}

func (a *PortalAction) ToggleBooleanField(fieldName string) {
	a.EnsureInputValues()
	current := strings.ToLower(strings.TrimSpace(a.InputValues[fieldName]))
	if current == "true" || current == "1" || current == "yes" || current == "on" {
		a.InputValues[fieldName] = "false"
		return
	}
	a.InputValues[fieldName] = "true"
}

func (a PortalAction) InputValid() bool {
	for _, field := range a.ValidateInput() {
		if field.Error != "" {
			return false
		}
	}
	return true
}

func stringInSlice(value string, values []string) bool {
	for _, candidate := range values {
		if strings.TrimSpace(candidate) == value {
			return true
		}
	}
	return false
}

func (a PortalAction) RequiresConfirmation() bool {
	if (a.Executor.Kind == PortalExecutorProjectArchive || a.Executor.Kind == PortalExecutorProjectArchiveRestore) && a.Executor.Payload["physical_archive"] == "true" {
		return a.InputValues["physical_review"] != ""
	}
	if a.Executor.Kind == PortalExecutorProjectArchiveInspect && a.InputValues["physical_recovery"] != "" {
		return true
	}
	return a.ConfirmationPolicy.Required
}

func (a PortalAction) IsEffectful() bool {
	switch a.Risk {
	case ActionRiskSafeRun, ActionRiskSensitive, ActionRiskDangerous:
		return true
	default:
		return false
	}
}

func (a PortalAction) Disabled() bool {
	return a.State == ActionDisabled || a.Risk == ActionRiskBlocked
}

func (a PortalAction) RawCommandString() string {
	if len(a.RawCommand) == 0 {
		return ""
	}
	return strings.Join(a.RawCommand, " ")
}

func actionDisabledError(action PortalAction) error {
	reason := strings.TrimSpace(action.DisabledReason)
	if reason == "" {
		reason = "This action is disabled."
	}
	return fmt.Errorf("action %q is disabled: %s", action.ID, reason)
}
