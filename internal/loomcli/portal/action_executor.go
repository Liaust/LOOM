package portal

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/cloudstorage"
	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/lane"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/search"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/storageretention"
	"loom.local/loom/internal/workers"
)

type ActionExecutor struct {
	Client           Client
	CorrelationID    string
	MainAvailability MainAvailability
}

func ExecutePortalAction(ctx context.Context, client Client, correlationID string, action PortalAction, confirmed bool, availability ...MainAvailability) (PortalActionResult, error) {
	if action.Disabled() {
		if action.DisabledReason == mainOfflineExecutionReason {
			return mainOfflineActionResult(action), nil
		}
		return PortalActionResult{}, actionDisabledError(action)
	}
	mainAvailability := MainAvailability{}
	if len(availability) > 0 {
		mainAvailability = availability[0]
	}
	if mainAvailability.State == MainAvailabilityOffline && portalActionExecutionDependency(action) == ExecutionDependencyMain {
		return mainOfflineActionResult(action), nil
	}
	if action.RequiresConfirmation() && !confirmed {
		return PortalActionResult{}, ErrActionConfirmationRequired
	}
	if !action.InputValid() {
		return PortalActionResult{}, fmt.Errorf("action %q has invalid input", action.ID)
	}
	return ActionExecutor{Client: client, CorrelationID: correlationID, MainAvailability: mainAvailability}.Execute(ctx, action), nil
}

func (e ActionExecutor) Execute(ctx context.Context, action PortalAction) PortalActionResult {
	if e.MainAvailability.State == MainAvailabilityOffline && portalActionExecutionDependency(action) == ExecutionDependencyMain {
		return mainOfflineActionResult(action)
	}
	switch action.Executor.Kind {
	case PortalExecutorWorkerInspect:
		return e.executeWorkerInspect(ctx, action)
	case PortalExecutorWorkerRunsInspect:
		return e.executeWorkerRunsInspect(ctx, action)
	case PortalExecutorWorkerRunOnce:
		return e.executeWorkerRunOnce(ctx, action)
	case PortalExecutorIndexInspect:
		return e.executeIndexInspect(action)
	case PortalExecutorIndexExplainObject:
		return e.executeIndexExplainObject(ctx, action)
	case PortalExecutorIndexRetry:
		return e.executeIndexRetry(ctx, action)
	case PortalExecutorIndexRetryFailed:
		return e.executeIndexRetryFailed(ctx, action)
	case PortalExecutorIndexRebuildObject:
		return e.executeIndexRebuildObject(ctx, action)
	case PortalExecutorMaintenanceInspect:
		return e.executeMaintenanceInspect(action)
	case PortalExecutorMaintenanceBackupRun:
		return e.executeMaintenanceBackupRun(ctx, action)
	case PortalExecutorMaintenanceBackupVerify:
		return e.executeMaintenanceBackupVerify(ctx, action)
	case PortalExecutorMaintenanceObjectScan:
		return e.executeMaintenanceObjectStoreScan(ctx, action)
	case PortalExecutorCloudStatusLive:
		return e.executeCloudStatusLive(ctx, action)
	case PortalExecutorCloudCooldownInspect:
		return e.executeCloudCooldownInspect(action)
	case PortalExecutorObjectInspect:
		return e.executeObjectInspect(ctx, action)
	case PortalExecutorRecordInspect:
		return e.executeRecordInspect(action)
	case PortalExecutorProtectedFolderProtect:
		return e.executeProtectedFolderProtect(ctx, action)
	case PortalExecutorProtectedFolderInspect:
		return e.executeProtectedFolderInspect(ctx, action)
	case PortalExecutorProtectedFolderEnable:
		return e.executeProtectedFolderEnable(ctx, action)
	case PortalExecutorProtectedFolderDisable:
		return e.executeProtectedFolderDisable(ctx, action)
	case PortalExecutorProtectedFolderRecheck:
		return e.executeProtectedFolderRecheck(ctx, action)
	case PortalExecutorProtectedFolderRetry:
		return e.executeProtectedFolderRetry(ctx, action)
	case PortalExecutorProtectedFolderDelete:
		return e.executeProtectedFolderDelete(ctx, action)
	case PortalExecutorScheduleInspect:
		return e.executeScheduleInspect(ctx, action)
	case PortalExecutorScheduleFireNow:
		return e.executeScheduleFireNow(ctx, action)
	case PortalExecutorSchedulePause:
		return e.executeSchedulePause(ctx, action)
	case PortalExecutorScheduleResume:
		return e.executeScheduleResume(ctx, action)
	case PortalExecutorScheduleFireInspect:
		return e.executeScheduleFireInspect(ctx, action)
	case PortalExecutorDirectEndpointInspect:
		return e.executeDirectEventEndpointInspect(ctx, action)
	case PortalExecutorDirectEventInspect:
		return e.executeDirectEventInspect(ctx, action)
	case PortalExecutorDirectEventRawPayload:
		return e.executeDirectEventRawPayload(ctx, action)
	case PortalExecutorInvocationInspect:
		return e.executeInvocationInspect(ctx, action)
	case PortalExecutorProviderInspect:
		return e.executeProviderInspect(ctx, action)
	case PortalExecutorProviderHealth:
		return e.executeProviderHealth(ctx, action)
	case PortalExecutorProviderAdInspect:
		return e.executeProviderAdvertisementInspect(ctx, action)
	case PortalExecutorCapabilityInspect:
		return e.executeCapabilityInspect(ctx, action)
	case PortalExecutorCapabilityUsageDocs:
		return e.executeCapabilityUsageDocs(ctx, action)
	case PortalExecutorCapabilityCallInspect:
		return e.executeCapabilityCallInspect(ctx, action)
	case PortalExecutorCapabilityCall:
		return e.executeCapabilityCall(ctx, action)
	case PortalExecutorRuntimeBindingInspect:
		return e.executeRuntimeBindingInspect(ctx, action)
	case PortalExecutorProjectInspect:
		return e.executeProjectInspect(ctx, action)
	case PortalExecutorProjectValidateLocal:
		return e.executeProjectValidateLocal(action)
	case PortalExecutorProjectValidateBackend:
		return e.executeProjectValidateBackend(ctx, action)
	case PortalExecutorProjectRegisterBackend:
		return e.executeProjectRegisterBackend(ctx, action)
	case PortalExecutorProjectRegistrationPlan:
		return e.executeProjectRegistrationPlan(ctx, action)
	case PortalExecutorProjectDoctor:
		return e.executeProjectDoctor(ctx, action)
	case PortalExecutorProjectDiff:
		return e.executeProjectDiff(ctx, action)
	case PortalExecutorProjectActivate:
		return e.executeProjectActivate(ctx, action)
	case PortalExecutorProjectDeactivate:
		return e.executeProjectDeactivate(ctx, action)
	case PortalExecutorProjectArchive:
		return e.executeProjectArchive(ctx, action)
	case PortalExecutorProjectArchiveInspect:
		return e.executeProjectArchiveInspect(ctx, action)
	case PortalExecutorProjectArchiveRestore:
		return e.executeProjectArchiveRestore(ctx, action)
	case PortalExecutorProjectAutomationHealth:
		return e.executeProjectAutomationHealth(ctx, action)
	case PortalExecutorProjectScaffoldBackend:
		return e.executeProjectScaffoldBackend(ctx, action)
	case PortalExecutorProjectScaffoldCleanup:
		return e.executeProjectScaffoldCleanup(action)
	case PortalExecutorProjectAddFacet:
		return e.executeProjectAddFacet(ctx, action)
	case PortalExecutorProjectMigrateLayout:
		return e.executeProjectMigrateLayout(ctx, action)
	case PortalExecutorProjectFacetInspect:
		return e.executeProjectFacetInspect(action)
	case PortalExecutorProjectWatchPlan:
		return e.executeProjectWatchPlan(ctx, action)
	case PortalExecutorProjectSyncStatus:
		return e.executeProjectSyncStatus(ctx, action)
	case PortalExecutorProjectBackupStatus:
		return e.executeProjectBackupStatus(ctx, action)
	case PortalExecutorProjectExportLocal:
		return e.executeProjectExport(ctx, action, false)
	case PortalExecutorProjectExportBackend:
		return e.executeProjectExport(ctx, action, true)
	case PortalExecutorBoxInit:
		return e.executeBoxInit(action)
	case PortalExecutorBoxProjectScaffold:
		return e.executeBoxProjectScaffold(action)
	case PortalExecutorBoxWatchApply:
		return e.executeBoxWatchApply(ctx, action)
	case PortalExecutorBoxDropzoneRetry, PortalExecutorBoxDropzonePause, PortalExecutorBoxDropzoneResume, PortalExecutorBoxDropzoneClean:
		return failedActionResult(action, "portal.dropzone_retired", "Dropzone mutations are retired; historical evidence is read-only.")
	case PortalExecutorBoxLaneSend:
		return e.executeBoxLaneSend(ctx, action)
	case PortalExecutorBoxLanePlan:
		return e.executeBoxLanePlan(action)
	case PortalExecutorBoxIgnoreInspect:
		return e.executeBoxIgnoreInspect(action)
	case PortalExecutorBoxLanePendingAck:
		return e.executeBoxLanePendingAcknowledge(action)
	case PortalExecutorBoxLaneTransferAck:
		return e.executeBoxLaneTransferAttention(action, false)
	case PortalExecutorBoxLaneTransferArchive:
		return e.executeBoxLaneTransferAttention(action, true)
	case PortalExecutorStorageInspect:
		return e.executeStorageInspect(ctx, action)
	case PortalExecutorStorageSafeToDelete:
		return e.executeStorageSafeToDelete(ctx, action)
	case PortalExecutorStorageFetch:
		return e.executeStorageFetch(ctx, action)
	case PortalExecutorStorageRestore:
		return e.executeStorageRestore(ctx, action)
	case PortalExecutorStorageArchive:
		return e.executeStorageArchive(ctx, action)
	case PortalExecutorStorageRetentionStatus:
		return e.executeStorageRetentionStatus(ctx, action)
	case PortalExecutorMainDocumentsStatus:
		return e.executeMainDocumentsStatus(ctx, action)
	case PortalExecutorDeletionRequestInspect:
		return e.executeDeletionRequestInspect(ctx, action)
	case PortalExecutorDeletionRequestReview:
		return e.executeDeletionRequestReview(ctx, action)
	case PortalExecutorDeletionRequestApprove:
		return e.executeDeletionRequestApprove(ctx, action)
	case PortalExecutorDeletionRequestDeny:
		return e.executeDeletionRequestDeny(ctx, action)
	case PortalExecutorDeletionRequestComplete:
		return e.executeDeletionRequestComplete(ctx, action)
	case PortalExecutorJobInspect:
		return e.executeJobInspect(ctx, action)
	case PortalExecutorJobLogsInspect:
		return e.executeJobLogsInspect(ctx, action)
	case PortalExecutorJobOutputsInspect:
		return e.executeJobOutputsInspect(ctx, action)
	case PortalExecutorJobRetry:
		return e.executeJobRetry(ctx, action)
	case PortalExecutorJobCancel:
		return e.executeJobCancel(ctx, action)
	case PortalExecutorJobAttentionAcknowledge:
		return e.executeJobAttentionAcknowledge(ctx, action)
	case PortalExecutorJobAttentionArchive:
		return e.executeJobAttentionArchive(ctx, action)
	case PortalExecutorNodeInspect:
		return e.executeNodeInspect(ctx, action)
	case PortalExecutorNodeHealth:
		return e.executeNodeHealth(ctx, action)
	case PortalExecutorNotesEmbeddingsToggle:
		return e.executeNotesEmbeddingsToggle(ctx, action)
	case PortalExecutorNavigate:
		return PortalActionResult{
			ActionID:      action.ID,
			Title:         action.Label,
			Status:        ActionLifecycleSucceeded,
			Summary:       "Opened " + firstNonEmpty(action.TargetLabel, action.TargetRef, action.Label),
			RawCommand:    append([]string{}, action.RawCommand...),
			RefreshScreen: action.RefreshScreen,
		}
	default:
		return PortalActionResult{
			ActionID:      action.ID,
			Title:         action.Label,
			Status:        ActionLifecycleFailed,
			Summary:       "Action is not executable from the portal yet.",
			ErrorCode:     "portal.action_unsupported",
			ErrorMessage:  fmt.Sprintf("Unsupported action executor: %s", firstNonEmpty(action.Executor.Kind, PortalExecutorUnsupported)),
			RawCommand:    append([]string{}, action.RawCommand...),
			RefreshScreen: action.RefreshScreen,
		}
	}
}

func portalActionExecutionDependency(action PortalAction) ExecutionDependency {
	if action.ExecutionDependency != "" {
		return action.ExecutionDependency
	}
	return portalExecutorDependency(action.Executor.Kind)
}

func mainOfflineActionResult(action PortalAction) PortalActionResult {
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleFailed,
		Summary:       "Action is unavailable while main is offline.",
		ErrorCode:     "portal.main_offline",
		ErrorMessage:  mainOfflineExecutionReason,
		RawCommand:    append([]string{}, action.RawCommand...),
		RefreshScreen: action.RefreshScreen,
	}
}

func (e ActionExecutor) executeWorkerInspect(ctx context.Context, action PortalAction) PortalActionResult {
	target := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if target == "" {
		return failedActionResult(action, "portal.action_target_missing", "Worker inspect action is missing a worker target.")
	}
	envelope, err := e.Client.InspectWorker(ctx, e.CorrelationID, target)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	detail := envelope.Data
	instance := detail.Instance
	fields := []ActionResultField{
		{Label: "Worker", Value: firstNonEmpty(instance.WorkerKey, target)},
		{Label: "Kind", Value: firstNonEmpty(instance.WorkerKind, "-")},
		{Label: "Owner Node", Value: firstNonEmpty(instance.OwnerNodeID, "-")},
		{Label: "Host Node", Value: firstNonEmpty(instance.HostNodeID, "-")},
		{Label: "Lifecycle", Value: firstNonEmpty(instance.LifecycleStatus, "-")},
		{Label: "Enabled", Value: fmt.Sprintf("%t", instance.Enabled)},
		{Label: "Paused", Value: fmt.Sprintf("%t", instance.Paused)},
		{Label: "Locality", Value: firstNonEmpty(instance.Locality, "-")},
		{Label: "Current Run", Value: stringPtrOrDash(instance.CurrentRunID)},
		{Label: "Last Run", Value: stringPtrOrDash(instance.LastRunID)},
		{Label: "Last Success", Value: timePtrOrDash(instance.LastSuccessAt)},
		{Label: "Last Failure", Value: timePtrOrDash(instance.LastFailureAt)},
		{Label: "Heartbeat", Value: timePtrOrDash(instance.LastHeartbeatAt)},
		{Label: "Next Run", Value: timePtrOrDash(instance.NextRunAfter)},
		{Label: "Backoff", Value: timePtrOrDash(instance.BackoffUntil)},
		{Label: "Consecutive Failures", Value: fmt.Sprintf("%d", instance.ConsecutiveFails)},
	}
	if detail.CurrentRun != nil {
		fields = append(fields, ActionResultField{Label: "Current Run Status", Value: firstNonEmpty(detail.CurrentRun.RunStatus, "-")})
	}
	if detail.LastRun != nil {
		fields = append(fields,
			ActionResultField{Label: "Last Run Status", Value: firstNonEmpty(detail.LastRun.RunStatus, "-")},
			ActionResultField{Label: "Last Run Retryable", Value: fmt.Sprintf("%t", detail.LastRun.Retryable)},
		)
	}
	if detail.LatestCheckpoint != nil {
		fields = append(fields,
			ActionResultField{Label: "Checkpoint", Value: firstNonEmpty(detail.LatestCheckpoint.CheckpointKey, detail.LatestCheckpoint.WorkerCheckpointID, "-")},
			ActionResultField{Label: "Checkpoint Updated", Value: timeOrDash(detail.LatestCheckpoint.UpdatedAt)},
		)
	}
	if detail.Health != nil {
		fields = append(fields,
			ActionResultField{Label: "Health", Value: firstNonEmpty(detail.Health.HealthStatus, "-")},
			ActionResultField{Label: "Severity", Value: firstNonEmpty(detail.Health.Severity, "-")},
			ActionResultField{Label: "Health Summary", Value: firstNonEmpty(detail.Health.Summary, "-")},
		)
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "Worker detail loaded.",
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		RefreshScreen: action.RefreshScreen,
	}
}

func (e ActionExecutor) executeWorkerRunsInspect(ctx context.Context, action PortalAction) PortalActionResult {
	target := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if target == "" {
		return failedActionResult(action, "portal.action_target_missing", "Worker runs action is missing a worker target.")
	}
	envelope, err := e.Client.ListWorkerRuns(ctx, e.CorrelationID, target, workers.RunFilter{Limit: 10})
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	fields := []ActionResultField{{Label: "Worker", Value: target}, {Label: "Runs", Value: fmt.Sprintf("%d", len(envelope.Data))}}
	for idx, run := range envelope.Data {
		if idx >= 5 {
			break
		}
		fields = append(fields, ActionResultField{
			Label: fmt.Sprintf("Run %d", idx+1),
			Value: fmt.Sprintf("%s %s trigger=%s started=%s finished=%s retryable=%t", firstNonEmpty(run.WorkerRunID, "-"), firstNonEmpty(run.RunStatus, "-"), firstNonEmpty(run.TriggerKind, "-"), timeOrDash(run.StartedAt), timePtrOrDash(run.FinishedAt), run.Retryable),
		})
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "Worker runs loaded.",
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		RefreshScreen: action.RefreshScreen,
	}
}

func (e ActionExecutor) executeWorkerRunOnce(ctx context.Context, action PortalAction) PortalActionResult {
	target := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if target == "" {
		return failedActionResult(action, "portal.action_target_missing", "Worker run action is missing a worker target.")
	}

	idempotencyKey := "portal_" + strings.ReplaceAll(action.ID, ".", "_") + "_" + ids.NewIdempotencyID()
	envelope, err := e.Client.RunWorkerOnce(ctx, e.CorrelationID, target, workers.RunOnceInput{
		Reason:         "portal action " + action.ID,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}

	workerKey := envelope.Data.Worker.Instance.WorkerKey
	runID := envelope.Data.Run.WorkerRunID
	runStatus := envelope.Data.Run.RunStatus
	resultIDempotency := envelope.Meta.IdempotencyKey
	if resultIDempotency == "" {
		resultIDempotency = idempotencyKey
	}
	return PortalActionResult{
		ActionID:       action.ID,
		Title:          action.Label,
		Status:         ActionLifecycleSucceeded,
		Summary:        "Worker run was requested.",
		RawCommand:     append([]string{}, action.RawCommand...),
		CorrelationID:  firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		IdempotencyKey: resultIDempotency,
		RefreshScreen:  action.RefreshScreen,
		Fields: []ActionResultField{
			{Label: "Worker", Value: firstNonEmpty(workerKey, target)},
			{Label: "Run", Value: firstNonEmpty(runID, "-")},
			{Label: "Run Status", Value: firstNonEmpty(runStatus, "-")},
			{Label: "Idempotency", Value: firstNonEmpty(resultIDempotency, "-")},
		},
	}
}

func (e ActionExecutor) executeIndexInspect(action PortalAction) PortalActionResult {
	payload := action.Executor.Payload
	fields := []ActionResultField{
		{Label: "Index Status", Value: firstNonEmpty(payload["index_status_id"], action.TargetRef, "-")},
		{Label: "Status", Value: firstNonEmpty(payload["status"], "-")},
		{Label: "Index Type", Value: firstNonEmpty(payload["index_type"], "-")},
		{Label: "Object", Value: firstNonEmpty(payload["object_id"], "-")},
		{Label: "Object Version", Value: firstNonEmpty(payload["object_version_id"], "-")},
		{Label: "Source", Value: firstNonEmpty(payload["source_kind"], "-") + ":" + firstNonEmpty(payload["source_id"], "-")},
		{Label: "Attempts", Value: firstNonEmpty(payload["attempts"], "0")},
		{Label: "Next Attempt", Value: firstNonEmpty(payload["next_attempt_at"], "-")},
		{Label: "Last Worker Run", Value: firstNonEmpty(payload["last_worker_run"], "-")},
		{Label: "Error", Value: firstNonEmpty(payload["last_error_code"], payload["last_error"], "-")},
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "Index work detail loaded.",
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		RefreshScreen: action.RefreshScreen,
	}
}

func (e ActionExecutor) executeIndexExplainObject(ctx context.Context, action PortalAction) PortalActionResult {
	objectRef := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if objectRef == "" {
		return failedActionResult(action, "portal.action_target_missing", "Index explain action is missing an object ref.")
	}
	envelope, err := e.Client.ExplainIndexObject(ctx, e.CorrelationID, search.IndexExplainInput{ObjectRef: objectRef})
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	result := envelope.Data
	fields := []ActionResultField{
		{Label: "Object", Value: firstNonEmpty(result.ObjectID, result.ObjectRef, objectRef)},
		{Label: "Version", Value: firstNonEmpty(result.ObjectVersionID, "-")},
		{Label: "Queue Status", Value: firstNonEmpty(result.QueueStatus, "-")},
		{Label: "Statuses", Value: fmt.Sprintf("%d", len(result.Statuses))},
	}
	for idx, status := range result.Statuses {
		if idx >= 5 {
			break
		}
		fields = append(fields, ActionResultField{
			Label: fmt.Sprintf("Status %d", idx+1),
			Value: fmt.Sprintf("%s %s %s", firstNonEmpty(status.IndexStatusID, "-"), firstNonEmpty(status.IndexType, "-"), firstNonEmpty(status.Status, "-")),
		})
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "Object index state loaded.",
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		RefreshScreen: action.RefreshScreen,
	}
}

func (e ActionExecutor) executeIndexRetry(ctx context.Context, action PortalAction) PortalActionResult {
	indexStatusID := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef, action.Executor.Payload["index_status_id"]))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if indexStatusID == "" {
		return failedActionResult(action, "portal.action_target_missing", "Index retry action is missing an index status ID.")
	}
	envelope, err := e.Client.RetryIndexWork(ctx, e.CorrelationID, search.IndexStatusRefInput{IndexStatusID: indexStatusID})
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	status := envelope.Data
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "Index work was queued for retry.",
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		RefreshScreen: action.RefreshScreen,
		Fields: []ActionResultField{
			{Label: "Index Status", Value: firstNonEmpty(status.IndexStatusID, indexStatusID)},
			{Label: "Status", Value: firstNonEmpty(status.Status, "-")},
			{Label: "Object", Value: firstNonEmpty(status.ObjectID, "-")},
			{Label: "Attempts", Value: fmt.Sprintf("%d", status.AttemptCount)},
		},
	}
}

func (e ActionExecutor) executeIndexRetryFailed(ctx context.Context, action PortalAction) PortalActionResult {
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	input := search.IndexRetryFailedInput{
		Limit:      intPayload(action, "limit", 50),
		ObjectRef:  action.Executor.Payload["object_ref"],
		ProjectRef: action.Executor.Payload["project_ref"],
		ScopeRef:   action.Executor.Payload["scope_ref"],
		IndexType:  action.Executor.Payload["index_type"],
	}
	envelope, err := e.Client.RetryFailedIndexWork(ctx, e.CorrelationID, input)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	summary := envelope.Data
	fields := []ActionResultField{
		{Label: "Retried", Value: fmt.Sprintf("%d", summary.Retried)},
		{Label: "Skipped", Value: fmt.Sprintf("%d", summary.Skipped)},
		{Label: "Items", Value: fmt.Sprintf("%d", len(summary.Items))},
	}
	for idx, item := range summary.Items {
		if idx >= 5 {
			break
		}
		fields = append(fields, ActionResultField{
			Label: fmt.Sprintf("Item %d", idx+1),
			Value: fmt.Sprintf("%s %s", firstNonEmpty(item.IndexStatusID, "-"), firstNonEmpty(item.Status, "-")),
		})
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "Failed index work retry was requested.",
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		RefreshScreen: action.RefreshScreen,
	}
}

func (e ActionExecutor) executeIndexRebuildObject(ctx context.Context, action PortalAction) PortalActionResult {
	objectRef := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef, action.Executor.Payload["object_id"]))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if objectRef == "" {
		return failedActionResult(action, "portal.action_target_missing", "Index rebuild action is missing an object ref.")
	}
	envelope, err := e.Client.RebuildIndexObject(ctx, e.CorrelationID, search.RebuildInput{ObjectRef: objectRef, Force: true})
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	result := envelope.Data
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "Object index rebuild was queued.",
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		RefreshScreen: action.RefreshScreen,
		Fields: []ActionResultField{
			{Label: "Object", Value: firstNonEmpty(result.ObjectID, objectRef)},
			{Label: "Version", Value: firstNonEmpty(result.ObjectVersionID, "-")},
			{Label: "Status", Value: firstNonEmpty(result.Status, "-")},
			{Label: "Index Statuses", Value: fmt.Sprintf("%d", len(result.Statuses))},
		},
	}
}

func (e ActionExecutor) executeMaintenanceInspect(action PortalAction) PortalActionResult {
	fields := make([]ActionResultField, 0, len(action.Executor.Payload))
	keys := make([]string, 0, len(action.Executor.Payload))
	for key := range action.Executor.Payload {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := action.Executor.Payload[key]
		if strings.TrimSpace(value) == "" {
			continue
		}
		fields = append(fields, ActionResultField{Label: titleFromToken(key), Value: value})
	}
	if len(fields) == 0 {
		fields = append(fields, ActionResultField{Label: "Target", Value: firstNonEmpty(action.TargetLabel, action.TargetRef, "-")})
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "Maintenance detail loaded.",
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		RefreshScreen: action.RefreshScreen,
	}
}

func (e ActionExecutor) executeMaintenanceBackupRun(ctx context.Context, action PortalAction) PortalActionResult {
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	idempotencyKey := "portal_" + strings.ReplaceAll(action.ID, ".", "_") + "_" + ids.NewIdempotencyID()
	envelope, err := e.Client.RunMaintenanceBackup(ctx, e.CorrelationID, maintenance.BackupRunInput{
		Reason:         "portal action " + action.ID,
		IdempotencyKey: idempotencyKey,
		Metadata:       json.RawMessage(`{"source":"loom_portal"}`),
	})
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	resultIDempotency := envelope.Meta.IdempotencyKey
	if resultIDempotency == "" {
		resultIDempotency = idempotencyKey
	}
	fields := workerRunFields(envelope.Data, resultIDempotency)
	fields = append(fields, backupRunSummaryFields(envelope.Data.Run.ResultSummaryJSON)...)
	return PortalActionResult{
		ActionID:       action.ID,
		Title:          action.Label,
		Status:         ActionLifecycleSucceeded,
		Summary:        "Main backup run was requested.",
		Fields:         fields,
		RawCommand:     append([]string{}, action.RawCommand...),
		CorrelationID:  firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		IdempotencyKey: resultIDempotency,
		RefreshScreen:  action.RefreshScreen,
	}
}

func (e ActionExecutor) executeMaintenanceBackupVerify(ctx context.Context, action PortalAction) PortalActionResult {
	backupRef := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef, action.Executor.Payload["operation_id"]))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if backupRef == "" {
		return failedActionResult(action, "portal.action_target_missing", "Backup verify action is missing a backup ref.")
	}
	envelope, err := e.Client.VerifyMaintenanceBackup(ctx, e.CorrelationID, maintenance.BackupVerifyInput{BackupRef: backupRef})
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	verification := envelope.Data
	fields := []ActionResultField{
		{Label: "Verification", Value: firstNonEmpty(verification.Status, "-")},
		{Label: "Backup", Value: firstNonEmpty(verification.BackupOperationID, backupRef)},
		{Label: "Directory", Value: firstNonEmpty(verification.BackupDir, "-")},
		{Label: "Manifest", Value: firstNonEmpty(verification.ManifestPath, "-")},
		{Label: "Checked", Value: timeOrDash(verification.CheckedAt)},
		{Label: "Artifacts", Value: fmt.Sprintf("%d", verification.VerifiedArtifactNum)},
	}
	checks := make([]string, 0, len(verification.Checks))
	for check := range verification.Checks {
		checks = append(checks, check)
	}
	sort.Strings(checks)
	for _, check := range checks {
		status := verification.Checks[check]
		fields = append(fields, ActionResultField{Label: "Check " + check, Value: status})
	}
	for idx, message := range verification.Errors {
		fields = append(fields, ActionResultField{Label: fmt.Sprintf("Error %d", idx+1), Value: message})
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "Backup verification completed.",
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		RefreshScreen: action.RefreshScreen,
	}
}

func (e ActionExecutor) executeMaintenanceObjectStoreScan(ctx context.Context, action PortalAction) PortalActionResult {
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	idempotencyKey := "portal_" + strings.ReplaceAll(action.ID, ".", "_") + "_" + ids.NewIdempotencyID()
	mode := firstNonEmpty(action.Executor.Payload["mode"], "sample")
	envelope, err := e.Client.RunMaintenanceObjectStoreScan(ctx, e.CorrelationID, maintenance.ObjectStoreScanInput{
		Mode:           mode,
		Reason:         "portal action " + action.ID,
		IdempotencyKey: idempotencyKey,
		Metadata:       json.RawMessage(`{"source":"loom_portal"}`),
	})
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	resultIDempotency := envelope.Meta.IdempotencyKey
	if resultIDempotency == "" {
		resultIDempotency = idempotencyKey
	}
	fields := workerRunFields(envelope.Data, resultIDempotency)
	fields = append(fields, objectStoreScanSummaryFields(envelope.Data.Run.ResultSummaryJSON)...)
	return PortalActionResult{
		ActionID:       action.ID,
		Title:          action.Label,
		Status:         ActionLifecycleSucceeded,
		Summary:        "Object-store scan was requested.",
		Fields:         fields,
		RawCommand:     append([]string{}, action.RawCommand...),
		CorrelationID:  firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		IdempotencyKey: resultIDempotency,
		RefreshScreen:  action.RefreshScreen,
	}
}

func (e ActionExecutor) executeCloudStatusLive(ctx context.Context, action PortalAction) PortalActionResult {
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	configPath := firstNonEmpty(action.Executor.Payload["config_path"], cloudstorage.DefaultConfigPath)
	envelope, err := e.Client.CloudStatusLive(ctx, e.CorrelationID, cloudstorage.CloudStatusLiveInput{
		ConfigPath: configPath,
	})
	if err != nil {
		return failedActionResult(action, "portal.cloud_status_failed", cloudServiceContextHint(err))
	}
	report := envelope.Data
	fields := cloudStatusActionFields(report)
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "Live cloud status probe completed.",
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		RefreshScreen: action.RefreshScreen,
	}
}

func (e ActionExecutor) executeCloudCooldownInspect(action PortalAction) PortalActionResult {
	fields := sortedPayloadFields(action.Executor.Payload)
	if len(fields) == 0 {
		fields = append(fields, ActionResultField{Label: "State", Value: "unknown"})
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "Cached cloud remote state loaded.",
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		RefreshScreen: action.RefreshScreen,
	}
}

func cloudStatusActionFields(report cloudstorage.StatusReport) []ActionResultField {
	fields := []ActionResultField{
		{Label: "Status", Value: firstNonEmpty(report.Status, "-")},
		{Label: "Mode", Value: firstNonEmpty(report.Mode, "-")},
		{Label: "Provider", Value: firstNonEmpty(report.Config.Provider, "-")},
		{Label: "Remote", Value: firstNonEmpty(report.Config.RemoteName, "-") + ":" + firstNonEmpty(report.Config.RemoteRoot, "-")},
		{Label: "Snapshot Backend", Value: firstNonEmpty(report.Config.SnapshotBackend, "-")},
		{Label: "Remote Lock", Value: firstNonEmpty(report.LockPath, report.Config.RemoteLockPath, "-")},
		{Label: "Checked", Value: timeOrDash(report.CheckedAt)},
	}
	if report.RemoteState != nil {
		fields = append(fields,
			ActionResultField{Label: "Remote State", Value: firstNonEmpty(report.RemoteState.State, "-")},
			ActionResultField{Label: "Last Success", Value: timePtrOrDash(report.RemoteState.LastSuccessAt)},
			ActionResultField{Label: "Last Failure", Value: timePtrOrDash(report.RemoteState.LastFailureAt)},
			ActionResultField{Label: "Next Live Check", Value: timePtrOrDash(report.RemoteState.NextLiveCheckAfter)},
			ActionResultField{Label: "Last Error Class", Value: firstNonEmpty(report.RemoteState.LastErrorClass, "-")},
		)
	}
	if report.Remote != nil {
		fields = append(fields, ActionResultField{Label: "Entries", Value: fmt.Sprintf("%d", report.Remote.Entries)})
	}
	return fields
}

func sortedPayloadFields(payload map[string]string) []ActionResultField {
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	fields := make([]ActionResultField, 0, len(keys))
	for _, key := range keys {
		value := strings.TrimSpace(payload[key])
		if value == "" {
			continue
		}
		fields = append(fields, ActionResultField{Label: titleFromToken(key), Value: value})
	}
	return fields
}

func cloudServiceContextHint(err error) string {
	if hint := cloudstorage.ServiceContextHint(err); hint != "" {
		return hint + " Original error: " + err.Error()
	}
	return err.Error()
}

func (e ActionExecutor) executeObjectInspect(ctx context.Context, action PortalAction) PortalActionResult {
	objectRef := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef, action.Executor.Payload["object_id"]))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if objectRef == "" {
		return failedActionResult(action, "portal.action_target_missing", "Object inspect action is missing an object ref.")
	}

	envelope, err := e.Client.GetObject(ctx, e.CorrelationID, objectRef)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	detail := envelope.Data
	object := detail.Object
	fields := []ActionResultField{
		{Label: "Object", Value: firstNonEmpty(object.ObjectID, objectRef)},
		{Label: "Name", Value: firstNonEmpty(object.Name, "-")},
		{Label: "Type", Value: firstNonEmpty(object.ObjectType, "-")},
		{Label: "Status", Value: firstNonEmpty(object.Status, "-")},
		{Label: "Home Scope", Value: stringPtrOrDash(object.HomeScopeID)},
		{Label: "State", Value: firstNonEmpty(object.StateClass, "-")},
		{Label: "Created", Value: timeOrDash(object.CreatedAt)},
		{Label: "Updated", Value: timeOrDash(object.UpdatedAt)},
	}
	if detail.LatestVersion != nil {
		version := detail.LatestVersion
		fields = append(fields,
			ActionResultField{Label: "Latest Version", Value: fmt.Sprintf("%s (%d)", firstNonEmpty(version.ObjectVersionID, "-"), version.VersionNumber)},
			ActionResultField{Label: "Content Hash", Value: stringPtrOrDash(version.ContentHash)},
			ActionResultField{Label: "Source Node", Value: stringPtrOrDash(version.SourceNodeID)},
			ActionResultField{Label: "Source Path", Value: stringPtrOrDash(version.SourcePath)},
			ActionResultField{Label: "Size", Value: int64PtrOrDash(version.SizeBytes)},
			ActionResultField{Label: "MIME", Value: stringPtrOrDash(version.MimeType)},
		)
	}
	if detail.File != nil {
		fields = append(fields,
			ActionResultField{Label: "File", Value: firstNonEmpty(detail.File.LogicalName, "-")},
			ActionResultField{Label: "Text Extractable", Value: fmt.Sprintf("%t", detail.File.TextExtractable)},
			ActionResultField{Label: "Index Policy", Value: firstNonEmpty(detail.File.IndexPolicy, "-")},
			ActionResultField{Label: "Backup Policy", Value: firstNonEmpty(detail.File.RawBackupPolicy, "-")},
		)
	}
	if detail.Blob != nil {
		fields = append(fields,
			ActionResultField{Label: "Blob", Value: firstNonEmpty(detail.Blob.BlobID, "-")},
			ActionResultField{Label: "Blob Hash", Value: firstNonEmpty(detail.Blob.HashURI, "-")},
			ActionResultField{Label: "Blob Status", Value: firstNonEmpty(detail.Blob.Status, "-")},
		)
	}
	fields = append(fields,
		ActionResultField{Label: "Scope Links", Value: fmt.Sprintf("%d", len(detail.ScopeLinks))},
		ActionResultField{Label: "Locations", Value: fmt.Sprintf("%d", len(detail.Locations))},
	)
	for idx, location := range detail.Locations {
		if idx >= 3 {
			break
		}
		fields = append(fields, ActionResultField{
			Label: fmt.Sprintf("Location %d", idx+1),
			Value: fmt.Sprintf("%s %s canonical=%t freshness=%s", firstNonEmpty(location.NodeID, "-"), firstNonEmpty(location.PathOrURI, "-"), location.IsCanonicalLocation, firstNonEmpty(location.FreshnessState, "-")),
		})
	}

	versionErr := ""
	if versionsEnvelope, err := e.Client.ListObjectVersions(ctx, e.CorrelationID, objectRef); err != nil {
		versionErr = err.Error()
		fields = append(fields, ActionResultField{Label: "Versions", Value: "unavailable: " + err.Error()})
	} else {
		fields = append(fields, ActionResultField{Label: "Version Count", Value: fmt.Sprintf("%d", len(versionsEnvelope.Data))})
		for idx, version := range versionsEnvelope.Data {
			if idx >= 5 {
				break
			}
			fields = append(fields, ActionResultField{
				Label: fmt.Sprintf("Version %d", version.VersionNumber),
				Value: fmt.Sprintf("%s status=%s hash=%s", firstNonEmpty(version.ObjectVersionID, "-"), firstNonEmpty(version.Status, "-"), stringPtrOrDash(version.ContentHash)),
			})
		}
	}

	indexErr := ""
	if indexEnvelope, err := e.Client.ExplainIndexObject(ctx, e.CorrelationID, search.IndexExplainInput{ObjectRef: objectRef}); err != nil {
		indexErr = err.Error()
		fields = append(fields, ActionResultField{Label: "Index State", Value: "unavailable: " + err.Error()})
	} else {
		explain := indexEnvelope.Data
		fields = append(fields,
			ActionResultField{Label: "Queue Status", Value: firstNonEmpty(explain.QueueStatus, "-")},
			ActionResultField{Label: "Index Statuses", Value: fmt.Sprintf("%d", len(explain.Statuses))},
		)
		for idx, status := range explain.Statuses {
			if idx >= 5 {
				break
			}
			fields = append(fields, ActionResultField{
				Label: fmt.Sprintf("Index %d", idx+1),
				Value: fmt.Sprintf("%s %s %s attempts=%d", firstNonEmpty(status.IndexStatusID, "-"), firstNonEmpty(status.IndexType, "-"), firstNonEmpty(status.Status, "-"), status.AttemptCount),
			})
		}
	}

	rawResponse := ""
	if versionErr != "" || indexErr != "" {
		raw, _ := json.Marshal(map[string]string{"version_error": versionErr, "index_error": indexErr})
		rawResponse = string(raw)
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "Object detail loaded.",
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		RawResponse:   rawResponse,
		CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		RefreshScreen: action.RefreshScreen,
	}
}

func (e ActionExecutor) executeRecordInspect(action PortalAction) PortalActionResult {
	fields := make([]ActionResultField, 0, len(action.Executor.Payload)+1)
	keys := make([]string, 0, len(action.Executor.Payload))
	for key := range action.Executor.Payload {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := action.Executor.Payload[key]
		if strings.TrimSpace(value) == "" {
			continue
		}
		fields = append(fields, ActionResultField{Label: titleFromToken(key), Value: value})
	}
	if len(fields) == 0 {
		fields = append(fields, ActionResultField{Label: "Record", Value: firstNonEmpty(action.TargetLabel, action.TargetRef, "-")})
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "Database record detail loaded.",
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		RefreshScreen: action.RefreshScreen,
	}
}

func (e ActionExecutor) executeStorageInspect(ctx context.Context, action PortalAction) PortalActionResult {
	ref := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef, action.Executor.Payload["storage_entry_id"], action.Executor.Payload["view_path"]))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if ref == "" {
		return failedActionResult(action, "portal.action_target_missing", "Storage inspect action is missing a storage entry or view path.")
	}
	entryID := strings.TrimSpace(firstNonEmpty(action.Executor.Payload["storage_entry_id"], ref))
	if strings.TrimSpace(action.Executor.Payload["storage_entry_id"]) != "" {
		envelope, err := e.Client.InspectStorageEntry(ctx, e.CorrelationID, entryID)
		if err != nil {
			return failedActionResult(action, "portal.action_failed", err.Error())
		}
		return storageEntryDetailResult(action, envelope.Data, firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID))
	}

	resolveEnvelope, resolveErr := e.Client.ResolveStoragePath(ctx, e.CorrelationID, ref)
	if resolveErr == nil {
		result := resolveEnvelope.Data
		fields := []ActionResultField{
			{Label: "View Path", Value: firstNonEmpty(result.Path, result.ViewEntry.ViewPath, ref)},
			{Label: "Kind", Value: firstNonEmpty(result.ViewEntry.EntryKind, "-")},
			{Label: "Class", Value: firstNonEmpty(result.ViewEntry.StorageClass, "-")},
			{Label: "Source Area", Value: firstNonEmpty(result.ViewEntry.SourceArea, "-")},
			{Label: "Origin Node", Value: firstNonEmpty(result.ViewEntry.OriginNodeKey, "-")},
			{Label: "Storage Entry", Value: firstNonEmpty(result.ViewEntry.StorageEntryID, "-")},
			{Label: "Permissions", Value: firstNonEmpty(result.ViewEntry.Permissions, "-")},
		}
		if result.ViewEntry.SizeBytes != nil {
			fields = append(fields, ActionResultField{Label: "Size", Value: storageFormatBytes(*result.ViewEntry.SizeBytes)})
		}
		if result.EntryDetail != nil {
			fields = append(fields, storageEntryDetailFields(*result.EntryDetail)...)
		}
		return PortalActionResult{
			ActionID:      action.ID,
			Title:         action.Label,
			Status:        ActionLifecycleSucceeded,
			Summary:       "Storage path resolved.",
			Fields:        fields,
			RawCommand:    append([]string{}, action.RawCommand...),
			CorrelationID: firstNonEmpty(resolveEnvelope.Meta.CorrelationID, e.CorrelationID),
			RefreshScreen: action.RefreshScreen,
		}
	}

	envelope, err := e.Client.InspectStorageEntry(ctx, e.CorrelationID, ref)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", firstNonEmpty(resolveErr.Error(), err.Error()))
	}
	return storageEntryDetailResult(action, envelope.Data, firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID))
}

func (e ActionExecutor) executeStorageSafeToDelete(ctx context.Context, action PortalAction) PortalActionResult {
	ref := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef, action.Executor.Payload["storage_entry_id"], action.Executor.Payload["view_path"]))
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if ref == "" {
		return failedActionResult(action, "portal.action_target_missing", "Safe-to-delete action is missing a storage ref.")
	}
	envelope, err := e.Client.CheckStorageSafeToDelete(ctx, e.CorrelationID, storageretention.SafeToDeleteInput{Ref: ref})
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	result := envelope.Data
	fields := []ActionResultField{
		{Label: "Ref", Value: firstNonEmpty(result.Ref, ref)},
		{Label: "Decision", Value: firstNonEmpty(result.Decision, "-")},
		{Label: "Safe", Value: fmt.Sprintf("%t", result.Safe)},
		{Label: "Physical Refs", Value: fmt.Sprintf("%d", len(result.PhysicalRefs))},
		{Label: "Checked", Value: timeOrDash(result.CheckedAt)},
	}
	for idx, reason := range result.Reasons {
		if idx >= 4 {
			break
		}
		fields = append(fields, ActionResultField{Label: fmt.Sprintf("Reason %d", idx+1), Value: reason})
	}
	for idx, blocker := range result.Blockers {
		if idx >= 4 {
			break
		}
		fields = append(fields, ActionResultField{Label: fmt.Sprintf("Blocker %d", idx+1), Value: blocker})
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "Safe-to-delete check completed.",
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		RefreshScreen: action.RefreshScreen,
	}
}

func (e ActionExecutor) executeStorageFetch(ctx context.Context, action PortalAction) PortalActionResult {
	ref := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef, action.Executor.Payload["storage_entry_id"], action.Executor.Payload["view_path"]))
	destination := strings.TrimSpace(action.InputValues["destination_path"])
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if ref == "" {
		return failedActionResult(action, "portal.action_target_missing", "Storage fetch action is missing a storage ref.")
	}
	if destination == "" {
		return failedActionResult(action, "portal.action_input_missing", "Destination path is required.")
	}
	envelope, err := e.Client.FetchStorage(ctx, e.CorrelationID, storageretention.FetchInput{
		Ref:             ref,
		DestinationPath: destination,
		Overwrite:       boolActionInput(action, "overwrite", false),
	})
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	result := envelope.Data
	fields := []ActionResultField{
		{Label: "Storage Entry", Value: firstNonEmpty(result.Entry.StorageEntryID, ref)},
		{Label: "Destination", Value: firstNonEmpty(result.DestinationPath, destination)},
		{Label: "Bytes Written", Value: storageFormatBytes(result.BytesWritten)},
		{Label: "Created", Value: timeOrDash(result.CreatedAt)},
	}
	if result.SourceRef != nil {
		fields = append(fields,
			ActionResultField{Label: "Source Kind", Value: firstNonEmpty(result.SourceRef.RefKind, "-")},
			ActionResultField{Label: "Source Status", Value: firstNonEmpty(result.SourceRef.Status, "-")},
		)
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "Storage entry fetched.",
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		RefreshScreen: action.RefreshScreen,
	}
}

func (e ActionExecutor) executeStorageRestore(ctx context.Context, action PortalAction) PortalActionResult {
	ref := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef, action.Executor.Payload["storage_entry_id"], action.Executor.Payload["view_path"]))
	destination := strings.TrimSpace(action.InputValues["destination_path"])
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if ref == "" {
		return failedActionResult(action, "portal.action_target_missing", "Storage restore action is missing a storage ref.")
	}
	if destination == "" {
		return failedActionResult(action, "portal.action_input_missing", "Destination path is required.")
	}
	envelope, err := e.Client.RestoreStorage(ctx, e.CorrelationID, storageretention.RestoreInput{
		Ref:             ref,
		DestinationPath: destination,
		Overwrite:       boolActionInput(action, "overwrite", false),
		Reason:          firstNonEmpty(action.InputValues["reason"], "portal restore"),
		CreatedBy:       "loom_portal",
	})
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	result := envelope.Data
	fields := []ActionResultField{
		{Label: "Storage Entry", Value: firstNonEmpty(result.FetchResult.Entry.StorageEntryID, ref)},
		{Label: "Destination", Value: firstNonEmpty(result.FetchResult.DestinationPath, destination)},
		{Label: "Bytes Written", Value: storageFormatBytes(result.FetchResult.BytesWritten)},
		{Label: "Restored", Value: timeOrDash(result.RestoredAt)},
	}
	if result.Tombstone != nil {
		fields = append(fields, ActionResultField{Label: "Tombstone", Value: firstNonEmpty(result.Tombstone.StorageTombstoneID, "-")})
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "Storage entry restored.",
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		RefreshScreen: action.RefreshScreen,
	}
}

func (e ActionExecutor) executeStorageArchive(ctx context.Context, action PortalAction) PortalActionResult {
	ref := strings.TrimSpace(firstNonEmpty(action.Executor.Target, action.TargetRef, action.Executor.Payload["storage_entry_id"], action.Executor.Payload["view_path"]))
	targetPath := strings.TrimSpace(action.InputValues["target_path"])
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	if ref == "" {
		return failedActionResult(action, "portal.action_target_missing", "Storage archive action is missing a storage ref.")
	}
	if targetPath == "" {
		return failedActionResult(action, "portal.action_input_missing", "Archive path is required.")
	}
	envelope, err := e.Client.ArchiveStorage(ctx, e.CorrelationID, storagearchive.ArchiveInput{
		SourceRef:          ref,
		TargetPath:         targetPath,
		ArchiveKind:        firstNonEmpty(action.InputValues["archive_kind"], "manual_archive"),
		MarkSourceArchived: boolActionInput(action, "mark_source_archived", false),
		DryRun:             boolActionInput(action, "dry_run", true),
	})
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	result := envelope.Data
	fields := []ActionResultField{
		{Label: "Archive", Value: firstNonEmpty(result.ArchiveManifest.ArchiveKey, result.ArchiveManifest.ArchiveManifestID, "-")},
		{Label: "Kind", Value: firstNonEmpty(result.ArchiveManifest.ArchiveKind, "-")},
		{Label: "Dry Run", Value: fmt.Sprintf("%t", result.DryRun)},
		{Label: "Entries", Value: fmt.Sprintf("%d", len(result.Entries))},
		{Label: "Safe Checks", Value: fmt.Sprintf("%d", len(result.SafeToDelete))},
		{Label: "Created", Value: timeOrDash(result.CreatedAt)},
	}
	for idx, entry := range result.Entries {
		if idx >= 4 {
			break
		}
		fields = append(fields, ActionResultField{
			Label: fmt.Sprintf("Entry %d", idx+1),
			Value: fmt.Sprintf("%s -> %s", firstNonEmpty(entry.SourceViewPath, "-"), firstNonEmpty(entry.ArchiveViewPath, "-")),
		})
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "Storage archive request completed.",
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		RefreshScreen: action.RefreshScreen,
	}
}

func (e ActionExecutor) executeStorageRetentionStatus(ctx context.Context, action PortalAction) PortalActionResult {
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	envelope, err := e.Client.GetStorageRetentionStatus(ctx, e.CorrelationID)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	status := envelope.Data
	fields := []ActionResultField{
		{Label: "Entries", Value: fmt.Sprintf("%d", status.Entries)},
		{Label: "Retained", Value: fmt.Sprintf("%d", status.Retained)},
		{Label: "Snapshots", Value: fmt.Sprintf("%d", status.Snapshots)},
		{Label: "Pending", Value: fmt.Sprintf("%d", status.Pending)},
		{Label: "Expired", Value: fmt.Sprintf("%d", status.Expired)},
		{Label: "Tombstoned", Value: fmt.Sprintf("%d", status.Tombstoned)},
		{Label: "Safe Candidates", Value: fmt.Sprintf("%d", status.SafeCandidates)},
		{Label: "Unsafe Candidates", Value: fmt.Sprintf("%d", status.UnsafeCandidates)},
		{Label: "Generated", Value: timeOrDash(status.GeneratedAt)},
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "Storage retention status loaded.",
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		RefreshScreen: action.RefreshScreen,
	}
}

func (e ActionExecutor) executeMainDocumentsStatus(ctx context.Context, action PortalAction) PortalActionResult {
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	envelope, err := e.Client.GetMainDocumentsStatus(ctx, e.CorrelationID)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	status := envelope.Data
	fields := []ActionResultField{
		{Label: "Exists", Value: fmt.Sprintf("%t", status.Exists)},
		{Label: "Stable Window", Value: fmt.Sprintf("%ds", status.StableWindowSeconds)},
		{Label: "Max Files Per Run", Value: fmt.Sprintf("%d", status.MaxFilesPerRun)},
		{Label: "Discovered", Value: fmt.Sprintf("%d", status.FilesDiscovered)},
		{Label: "Accepted", Value: fmt.Sprintf("%d", status.FilesAccepted)},
		{Label: "Delayed", Value: fmt.Sprintf("%d", status.FilesDelayed)},
		{Label: "Skipped", Value: fmt.Sprintf("%d", status.FilesSkipped)},
		{Label: "Failed", Value: fmt.Sprintf("%d", status.FilesFailed)},
		{Label: "Bytes Hashed", Value: storageFormatBytes(status.BytesHashed)},
		{Label: "Latest Accepted", Value: timePtrOrDash(status.LatestAcceptedAt)},
	}
	if mainDocumentsPortalMetricsAny(status.Metrics) {
		fields = append(fields,
			ActionResultField{Label: "Timing Total", Value: portalDurationMS(status.Metrics.TotalDurationMS)},
			ActionResultField{Label: "Timing Discover", Value: portalDurationMS(status.Metrics.DiscoverDurationMS)},
			ActionResultField{Label: "Timing Hash", Value: portalDurationMS(status.Metrics.HashDurationMS)},
			ActionResultField{Label: "Timing Retention Copy", Value: portalDurationMS(status.Metrics.RetentionCopyDurationMS)},
			ActionResultField{Label: "Timing Register", Value: portalDurationMS(status.Metrics.RegisterDurationMS)},
			ActionResultField{Label: "Timing Reconcile", Value: portalDurationMS(status.Metrics.ReconcileDurationMS)},
			ActionResultField{Label: "Hash Operations", Value: fmt.Sprintf("%d", status.Metrics.HashOperations)},
			ActionResultField{Label: "Register Operations", Value: fmt.Sprintf("%d", status.Metrics.RegisterOperations)},
		)
	}
	for idx, item := range status.Imports {
		if idx >= 4 {
			break
		}
		fields = append(fields, ActionResultField{Label: fmt.Sprintf("Import %d", idx+1), Value: fmt.Sprintf("%s %s", firstNonEmpty(item.State, "-"), firstNonEmpty(item.RelativePath, "-"))})
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "Main documents status loaded.",
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		RefreshScreen: action.RefreshScreen,
	}
}

func storageEntryDetailResult(action PortalAction, detail storagecatalog.EntryDetail, correlationID string) PortalActionResult {
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "Storage entry detail loaded.",
		Fields:        storageEntryDetailFields(detail),
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: correlationID,
		RefreshScreen: action.RefreshScreen,
	}
}

func storageEntryDetailFields(detail storagecatalog.EntryDetail) []ActionResultField {
	entry := detail.Entry
	fields := []ActionResultField{
		{Label: "Storage Entry", Value: firstNonEmpty(entry.StorageEntryID, "-")},
		{Label: "View Path", Value: firstNonEmpty(entry.CurrentViewPath, "-")},
		{Label: "Class", Value: firstNonEmpty(entry.StorageClass, "-")},
		{Label: "Source Area", Value: firstNonEmpty(entry.SourceArea, "-")},
		{Label: "Origin Node", Value: firstNonEmpty(entry.OriginNodeKey, "-")},
		{Label: "Logical Path", Value: firstNonEmpty(entry.LogicalPath, "-")},
		{Label: "File Class", Value: firstNonEmpty(entry.FileClass, "-")},
		{Label: "Processing", Value: firstNonEmpty(entry.ProcessingState, "-")},
		{Label: "Availability", Value: firstNonEmpty(entry.AvailabilityState, "-")},
		{Label: "Retention", Value: firstNonEmpty(entry.RetentionState, "-")},
		{Label: "Physical Refs", Value: fmt.Sprintf("%d", len(detail.PhysicalRefs))},
	}
	if entry.SizeBytes != nil {
		fields = append(fields, ActionResultField{Label: "Size", Value: storageFormatBytes(*entry.SizeBytes)})
	}
	for idx, ref := range detail.PhysicalRefs {
		if idx >= 3 {
			break
		}
		fields = append(fields, ActionResultField{
			Label: fmt.Sprintf("Physical Ref %d", idx+1),
			Value: fmt.Sprintf("%s %s node=%s", firstNonEmpty(ref.RefKind, "-"), firstNonEmpty(ref.Status, "-"), firstNonEmpty(ref.NodeKey, "-")),
		})
	}
	return fields
}

func (e ActionExecutor) executeBoxInit(action PortalAction) PortalActionResult {
	resolved := boxResolvedFromAction(action)
	if strings.TrimSpace(resolved.RootPath) == "" {
		return failedActionResult(action, "portal.action_target_missing", "Box init action is missing a Box path.")
	}
	result, err := box.Init(box.InitInput{Resolved: resolved})
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	fields := []ActionResultField{
		{Label: "Path", Value: result.RootPath},
		{Label: "Profile", Value: result.Profile},
		{Label: "Owner Node", Value: firstNonEmpty(result.OwnerNode, "-")},
		{Label: "State", Value: result.StatusAfter.State},
		{Label: "Created Directories", Value: strconv.Itoa(len(result.CreatedDirs))},
		{Label: "Created Files", Value: strconv.Itoa(len(result.CreatedFiles))},
		{Label: "Existing Directories", Value: strconv.Itoa(len(result.SkippedDirs))},
		{Label: "Existing Files", Value: strconv.Itoa(len(result.SkippedFiles))},
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "Box scaffold initialized.",
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		RefreshScreen: ScreenBox,
	}
}

func (e ActionExecutor) executeBoxProjectScaffold(action PortalAction) PortalActionResult {
	name := strings.TrimSpace(action.InputValues["project_name"])
	if name == "" {
		return failedActionResult(action, "portal.action_input_missing", "Project name is required.")
	}
	resolved := boxResolvedFromAction(action)
	if strings.TrimSpace(resolved.RootPath) == "" {
		return failedActionResult(action, "portal.action_target_missing", "Box project scaffold action is missing a Box path.")
	}
	status := box.Inspect(resolved)
	if status.State != "ok" {
		return failedActionResult(action, "portal.box_not_ready", "Box must be initialized before creating a Box project.")
	}
	result, err := projectcontracts.ScaffoldProject(projectcontracts.ScaffoldOptions{
		Name:            name,
		OwnerNode:       status.OwnerNode,
		Preset:          "minimal",
		Directory:       status.DefaultProjectPath,
		DirectorySource: projectcontracts.ScaffoldDirectoryBox,
		BoxDefaultUsed:  true,
		BoxRoot:         status.RootPath,
		BoxProfile:      status.Profile,
		BoxContractPath: status.ContractPath,
	})
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "Project scaffold created in Box.",
		RawCommand:    append([]string{}, action.RawCommand...),
		RefreshScreen: ScreenProjects,
		Fields: []ActionResultField{
			{Label: "Project", Value: result.Name},
			{Label: "Slug", Value: result.Slug},
			{Label: "Root", Value: result.ProjectRoot},
			{Label: "Parent", Value: result.ParentDir},
			{Label: "Files", Value: strconv.Itoa(len(result.Files))},
		},
	}
}

func (e ActionExecutor) executeProjectScaffoldBackend(ctx context.Context, action PortalAction) PortalActionResult {
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	name := strings.TrimSpace(action.InputValues["project_name"])
	if name == "" {
		return failedActionResult(action, "portal.action_input_missing", "Project name is required.")
	}
	targetNode := strings.TrimSpace(firstNonEmpty(action.InputValues["target_node"], action.InputValues["owner_node"], action.Executor.Payload["default_target_node"], action.Executor.Payload["owner_node"], "main"))
	preset := strings.TrimSpace(firstNonEmpty(action.InputValues["preset"], projectcontracts.PresetMinimal))
	facets := splitCommaField(action.InputValues["facets"])
	dryRun := false
	if raw := strings.TrimSpace(action.InputValues["dry_run"]); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return failedActionResult(action, "portal.action_input_invalid", "Dry Run must be true or false.")
		}
		dryRun = parsed
	}
	registerAfterCreate := true
	if raw := strings.TrimSpace(action.InputValues["register_after_create"]); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return failedActionResult(action, "portal.action_input_invalid", "Register After Create must be true or false.")
		}
		registerAfterCreate = parsed
	}
	if localResult, ok := e.executeProjectScaffoldLocal(action, name, targetNode, preset, facets, dryRun); ok {
		return localResult
	}
	if targetNode != "main" {
		return failedActionResult(action, "portal.project_target_unsupported", "Project creation is currently available for main and the current node's local LOOM Box.")
	}
	input := projectcontracts.ScaffoldOptions{
		Name:      name,
		OwnerNode: targetNode,
		Preset:    preset,
		Facets:    facets,
		DryRun:    dryRun,
	}
	envelope, err := e.Client.ScaffoldProject(ctx, e.CorrelationID, input)
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error())
	}
	result := envelope.Data
	result.Name = firstNonEmpty(result.Name, name)
	result.Slug = firstNonEmpty(result.Slug, projectcontracts.DeriveProjectSlug(name))
	result.OwnerNode = firstNonEmpty(result.OwnerNode, targetNode, "main")
	result.Preset = firstNonEmpty(result.Preset, preset, projectcontracts.PresetMinimal)
	if len(result.Facets) == 0 {
		result.Facets = append([]string{}, facets...)
	}
	facetSummary := strings.Join(result.Facets, ", ")
	if facetSummary == "" {
		facetSummary = "preset defaults"
	}
	projectPath := firstNonEmpty(result.ProjectRoot, result.ParentDir)
	capabilityPrefix := result.OwnerNode + "@" + result.Slug
	status := ActionLifecycleSucceeded
	summary := fmt.Sprintf("Project scaffold created on %s under loom-box/Projects.", firstNonEmpty(result.OwnerNode, targetNode, "main"))
	if result.Validation.State == projectcontracts.ScaffoldValidationPlannedOnly || dryRun {
		summary = fmt.Sprintf("Project scaffold planned on %s under loom-box/Projects.", firstNonEmpty(result.OwnerNode, targetNode, "main"))
	}
	fields := []ActionResultField{
		{Label: "Project", Value: result.Name},
		{Label: "Slug", Value: result.Slug},
		{Label: "Target Node", Value: firstNonEmpty(result.OwnerNode, targetNode)},
		{Label: "Capability Prefix", Value: capabilityPrefix},
		{Label: "Project Path", Value: projectPath},
		{Label: "Parent", Value: result.ParentDir},
		{Label: "Preset", Value: result.Preset},
		{Label: "Facets", Value: facetSummary},
		{Label: "Validation", Value: firstNonEmpty(result.Validation.State, "-")},
		{Label: "Files", Value: strconv.Itoa(len(result.Files))},
	}
	if !dryRun && registerAfterCreate {
		registerEnvelope, err := e.Client.RegisterProjectContractFromBackend(ctx, e.CorrelationID, projects.RegisterProjectContractFromBackendInput{
			ProjectRef:  result.Slug,
			ProjectRoot: result.ProjectRoot,
		})
		if err != nil {
			fields = append(fields,
				ActionResultField{Label: "Registration", Value: "failed"},
				ActionResultField{Label: "Registration Error", Value: err.Error()},
				ActionResultField{Label: "Next Steps", Value: "Run Validate Contract, then Re-register Contract from Projects."},
			)
			return PortalActionResult{
				ActionID:      action.ID,
				Title:         action.Label,
				Status:        ActionLifecycleFailed,
				Summary:       "Project scaffold was created, but backend registration failed.",
				Fields:        fields,
				RawCommand:    append([]string{}, action.RawCommand...),
				CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
				RefreshScreen: ScreenProjects,
			}
		}
		registration := registerEnvelope.Data
		registrationStatus := "-"
		registrationRevision := "-"
		registrationRoot := result.ProjectRoot
		if registration.Detail.Registration != nil {
			registrationStatus = firstNonEmpty(registration.Detail.Registration.RegistrationStatus, "-")
			registrationRevision = strconv.Itoa(registration.Detail.Registration.RegistrationRevision)
			registrationRoot = firstNonEmpty(registration.Detail.Registration.ProjectRoot, registrationRoot)
		}
		summary = fmt.Sprintf("Project created and registered on %s under loom-box/Projects.", firstNonEmpty(result.OwnerNode, targetNode, "main"))
		fields = append(fields,
			ActionResultField{Label: "Registration", Value: registrationStatus},
			ActionResultField{Label: "Revision", Value: registrationRevision},
			ActionResultField{Label: "Registered Root", Value: firstNonEmpty(registrationRoot, "-")},
			ActionResultField{Label: "Created", Value: fmt.Sprintf("%t", registration.Created)},
			ActionResultField{Label: "Updated", Value: fmt.Sprintf("%t", registration.Updated)},
			ActionResultField{Label: "Unchanged", Value: fmt.Sprintf("%t", registration.Unchanged)},
			ActionResultField{Label: "Next Steps", Value: "Refresh Projects, open the new project, then activate the needed facets."},
		)
	} else if dryRun {
		fields = append(fields,
			ActionResultField{Label: "Registration", Value: "skipped_dry_run"},
			ActionResultField{Label: "Next Steps", Value: "Turn off Dry Run to create and register the project."},
		)
	} else {
		fields = append(fields,
			ActionResultField{Label: "Registration", Value: "not_requested"},
			ActionResultField{Label: "Next Steps", Value: "Refresh Projects, open the new project, then validate and register its contract."},
		)
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        status,
		Summary:       summary,
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		RefreshScreen: ScreenProjects,
	}
}

func (e ActionExecutor) executeProjectScaffoldLocal(action PortalAction, name, targetNode, preset string, facets []string, dryRun bool) (PortalActionResult, bool) {
	localOwner := strings.TrimSpace(action.Executor.Payload["local_owner_node"])
	if localOwner == "" || targetNode != localOwner {
		return PortalActionResult{}, false
	}
	resolved := box.Resolved{
		RootPath:         strings.TrimSpace(action.Executor.Payload["local_box_root"]),
		Profile:          strings.TrimSpace(action.Executor.Payload["local_box_profile"]),
		OwnerNode:        localOwner,
		RuntimeStateRoot: strings.TrimSpace(action.Executor.Payload["local_box_state_root"]),
	}
	if strings.TrimSpace(resolved.RootPath) == "" {
		return failedActionResult(action, "portal.action_target_missing", "Local Box path is missing."), true
	}
	status := box.Inspect(resolved)
	if status.State != "ok" {
		return failedActionResult(action, "portal.box_not_ready", "Local Box must be initialized before creating a project on this node."), true
	}
	result, err := projectcontracts.ScaffoldProject(projectcontracts.ScaffoldOptions{
		Name:            name,
		OwnerNode:       targetNode,
		Preset:          preset,
		Facets:          facets,
		Directory:       status.DefaultProjectPath,
		DirectorySource: projectcontracts.ScaffoldDirectoryBox,
		BoxDefaultUsed:  true,
		BoxRoot:         status.RootPath,
		BoxProfile:      status.Profile,
		BoxContractPath: status.ContractPath,
		DryRun:          dryRun,
	})
	if err != nil {
		return failedActionResult(action, "portal.action_failed", err.Error()), true
	}
	if len(result.Facets) == 0 {
		result.Facets = append([]string{}, facets...)
	}
	facetSummary := strings.Join(result.Facets, ", ")
	if facetSummary == "" {
		facetSummary = "preset defaults"
	}
	summary := fmt.Sprintf("Project scaffold created on %s under loom-box/Projects.", targetNode)
	if result.Validation.State == projectcontracts.ScaffoldValidationPlannedOnly || dryRun {
		summary = fmt.Sprintf("Project scaffold planned on %s under loom-box/Projects.", targetNode)
	}
	return PortalActionResult{
		ActionID: action.ID,
		Title:    action.Label,
		Status:   ActionLifecycleSucceeded,
		Summary:  summary,
		Fields: []ActionResultField{
			{Label: "Project", Value: result.Name},
			{Label: "Slug", Value: result.Slug},
			{Label: "Target Node", Value: targetNode},
			{Label: "Capability Prefix", Value: targetNode + "@" + result.Slug},
			{Label: "Project Path", Value: result.ProjectRoot},
			{Label: "Parent", Value: result.ParentDir},
			{Label: "Preset", Value: result.Preset},
			{Label: "Facets", Value: facetSummary},
			{Label: "Validation", Value: firstNonEmpty(result.Validation.State, "-")},
			{Label: "Files", Value: strconv.Itoa(len(result.Files))},
			{Label: "Next Steps", Value: "Refresh Projects after node sync/registration, then fill and activate project contracts."},
		},
		RawCommand:    append([]string{}, action.RawCommand...),
		RefreshScreen: ScreenProjects,
	}, true
}

func splitCommaField(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func (e ActionExecutor) executeBoxWatchApply(ctx context.Context, action PortalAction) PortalActionResult {
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	resolved := boxResolvedFromAction(action)
	if strings.TrimSpace(resolved.RootPath) == "" {
		return failedActionResult(action, "portal.action_target_missing", "Box watch apply action is missing a Box path.")
	}
	plan, err := box.BuildWatchPlan(box.WatchStatusInput{Resolved: resolved})
	if err != nil {
		return failedActionResult(action, "portal.box_watch_plan_failed", err.Error())
	}
	dryRun := boolActionInput(action, "dry_run", false)
	envelope, err := e.Client.ApplyBoxWatchPolicy(ctx, e.CorrelationID, box.WatchApplyInput{Resolved: resolved, Plan: &plan, DryRun: dryRun})
	if err != nil {
		return failedActionResult(action, "portal.box_watch_apply_failed", err.Error())
	}
	result := envelope.Data
	fields := []ActionResultField{
		{Label: "Dry Run", Value: fmt.Sprintf("%t", result.DryRun)},
		{Label: "Box", Value: firstNonEmpty(result.Plan.RootPath, resolved.RootPath)},
		{Label: "Owner Node", Value: firstNonEmpty(result.Plan.OwnerNode, resolved.OwnerNode, "-")},
		{Label: "Planned Roots", Value: strconv.Itoa(len(result.Plan.WatchedRoots))},
		{Label: "Registrations", Value: strconv.Itoa(len(result.Registrations))},
		{Label: "Scopes", Value: strconv.Itoa(len(result.Scopes))},
	}
	for idx, registration := range result.Registrations {
		if idx >= 3 {
			break
		}
		fields = append(fields, ActionResultField{
			Label: fmt.Sprintf("Root %d", idx+1),
			Value: fmt.Sprintf("%s %s", firstNonEmpty(registration.AreaKey, registration.BackendRootKey, "-"), firstNonEmpty(registration.ActivationStatus, "-")),
		})
	}
	summary := "Box watch policy applied."
	if result.DryRun {
		summary = "Box watch policy validated."
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       summary,
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		CorrelationID: firstNonEmpty(envelope.Meta.CorrelationID, e.CorrelationID),
		RefreshScreen: ScreenBox,
	}
}

func (e ActionExecutor) executeBoxLaneSend(ctx context.Context, action PortalAction) PortalActionResult {
	resolved := boxResolvedFromAction(action)
	if strings.TrimSpace(resolved.RootPath) == "" {
		return failedActionResult(action, "portal.action_target_missing", "Lane send action is missing a Box path.")
	}
	status := box.Inspect(resolved)
	if status.State != "ok" {
		return failedActionResult(action, "portal.box_not_ready", "Box must be initialized before sending LOOM Lane.")
	}
	if batchID := strings.TrimSpace(action.Executor.Payload["lane_repair_batch_id"]); batchID != "" {
		return e.executeBoxLaneRepair(ctx, action, status, batchID)
	}
	sourceNode := firstNonEmpty(action.Executor.Payload["owner_node"], status.OwnerNode)
	profile, err := filepolicy.ParseProfile(firstNonEmpty(action.Executor.Payload["lane_profile"], string(filepolicy.ProfileFaithful)))
	if err != nil {
		return failedActionResult(action, "portal.lane_profile_invalid", err.Error())
	}
	dryRun := boolActionInput(action, "dry_run", false)
	laneRelPath := laneRelativePathFromBoxStatus(status)
	result, err := lane.Send(ctx, lane.SendInput{
		RootPath:           resolved.RootPath,
		LaneRelPath:        laneRelPath,
		StatePath:          filepath.Join(status.RuntimeStateWriteRoot, "lane"),
		SourceNodeKey:      sourceNode,
		SourceBoxID:        firstNonEmpty(action.Executor.Payload["box_id"], boxIDFromStatus(status)),
		MainHost:           lane.DefaultMainHost,
		RemoteRoot:         lane.DefaultRemoteRoot,
		DryRun:             dryRun,
		Profile:            profile,
		RequestedTransport: lane.TransportMode(firstNonEmpty(action.Executor.Payload["lane_transport"], string(lane.TransportModeAuto))),
	})
	if err != nil {
		return failedActionResult(action, "portal.lane_send_failed", err.Error())
	}
	fields := []ActionResultField{
		{Label: "Requested Transport", Value: string(result.RequestedTransport)},
		{Label: "Recommended Transport", Value: string(result.Transport.RecommendedMode)},
		{Label: "Selected Transport", Value: string(result.SelectedTransport)},
		{Label: "Transport Reason", Value: firstNonEmpty(result.TransportReason, "-")},
		{Label: "Estimated Temporary Space", Value: storageFormatBytes(result.Transport.EstimatedTemporaryBytes)},
		{Label: "Policy Profile", Value: string(result.Profile)},
		{Label: "Policy Version", Value: firstNonEmpty(result.PolicyVersion, "-")},
		{Label: "Policy Files", Value: strconv.Itoa(len(result.PolicyHashes))},
		{Label: "Protected Files", Value: strconv.Itoa(result.FileCount)},
		{Label: "Ignored Files", Value: strconv.Itoa(result.IgnoredFileCount)},
		{Label: "Ignored Bytes", Value: storageFormatBytes(result.IgnoredBytes)},
		{Label: "Batch", Value: firstNonEmpty(result.BatchID, "-")},
		{Label: "Status", Value: firstNonEmpty(result.Status, "-")},
		{Label: "Source Node", Value: firstNonEmpty(result.SourceNodeKey, "-")},
		{Label: "Files", Value: strconv.Itoa(result.FileCount)},
		{Label: "Bytes", Value: strconv.FormatInt(result.TotalBytes, 10)},
		{Label: "Canonical On Main", Value: firstNonEmpty(result.VisibleStoragePath, "-")},
		{Label: "Safety Copy", Value: firstNonEmpty(result.LocalSafetyPath, "-")},
		{Label: "Bundle Artifact", Value: firstNonEmpty(result.BundleArtifactPath, "-")},
		{Label: "Bundle Archive SHA-256", Value: firstNonEmpty(result.BundleArchiveSHA256, "-")},
		{Label: "Bundle Manifest SHA-256", Value: firstNonEmpty(result.BundleManifestSHA256, "-")},
		{Label: "Bundle Cleanup", Value: firstNonEmpty(result.BundleCleanupState, "-")},
		{Label: "Safety Cleanup", Value: firstNonEmpty(result.LocalSafetyCleanupState, "-")},
		{Label: "Safety Cleanup Reason", Value: firstNonEmpty(result.LocalSafetyRemovalReason, "-")},
		{Label: "Remote Accepted", Value: firstNonEmpty(result.RemoteAcceptedPath, "-")},
		{Label: "Cleanup Quarantine", Value: firstNonEmpty(result.LocalCleanupQuarantinePath, "-")},
		{Label: "Cleanup Quarantine State", Value: firstNonEmpty(result.LocalCleanupQuarantineState, "-")},
		{Label: "Cleanup Quarantine Bytes", Value: strconv.FormatInt(result.LocalCleanupQuarantineBytes, 10)},
		{Label: "Cleanup Quarantine Expires", Value: portalTimePtr(result.LocalCleanupQuarantineExpiresAt)},
		{Label: "Recovery Storage Retained", Value: storageFormatBytes(result.RecoveryStorage.RetainedBytes)},
		{Label: "Recovery Storage Protected", Value: storageFormatBytes(result.RecoveryStorage.ProtectedEvidenceBytes)},
		{Label: "Quarantined Local Items", Value: strconv.Itoa(len(result.QuarantinedLocalItems))},
		{Label: "Restored Local Items", Value: strconv.Itoa(len(result.RestoredLocalItems))},
		{Label: "Removed Local Items", Value: strconv.Itoa(len(result.RemovedLocalItems))},
	}
	lifecycle, summary, errorCode := portalLaneSendPresentation(result.Status, result.DryRun)
	if result.ErrorMessage != "" {
		fields = append(fields, ActionResultField{Label: "Follow-up", Value: result.ErrorMessage})
	}
	for index, warning := range result.Warnings {
		fields = append(fields, ActionResultField{Label: fmt.Sprintf("Warning %d", index+1), Value: warning})
	}
	errorMessage := ""
	if lifecycle == ActionLifecycleFailed {
		errorMessage = firstNonEmpty(result.ErrorMessage, summary)
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        lifecycle,
		Summary:       summary,
		ErrorCode:     errorCode,
		ErrorMessage:  errorMessage,
		RawCommand:    append([]string{}, action.RawCommand...),
		RefreshScreen: ScreenBox,
		Fields:        fields,
	}
}

func (e ActionExecutor) executeBoxLaneRepair(ctx context.Context, action PortalAction, status box.Status, batchID string) PortalActionResult {
	result, err := lane.Publish(ctx, lane.PublishInput{
		RootPath:    status.RootPath,
		LaneRelPath: laneRelativePathFromBoxStatus(status),
		StatePath:   filepath.Join(status.RuntimeStateWriteRoot, "lane"),
		BatchID:     batchID,
		MainHost:    lane.DefaultMainHost,
		RemoteRoot:  lane.DefaultRemoteRoot,
	})
	if err != nil {
		return failedActionResult(action, "portal.lane_repair_failed", err.Error())
	}
	lifecycle, summary, errorCode := portalLaneSendPresentation(result.Status, result.DryRun)
	errorMessage := ""
	if lifecycle == ActionLifecycleFailed {
		errorMessage = firstNonEmpty(result.ErrorMessage, summary)
	}
	fields := []ActionResultField{
		{Label: "Batch", Value: result.BatchID},
		{Label: "Status", Value: result.Status},
		{Label: "Canonical On Main", Value: firstNonEmpty(result.VisibleStoragePath, "-")},
		{Label: "Remote Custody Reference", Value: firstNonEmpty(result.RemoteAcceptedPath, "-")},
	}
	for index, warning := range result.Warnings {
		fields = append(fields, ActionResultField{Label: fmt.Sprintf("Warning %d", index+1), Value: warning})
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        lifecycle,
		Summary:       summary,
		ErrorCode:     errorCode,
		ErrorMessage:  errorMessage,
		RawCommand:    append([]string{}, action.RawCommand...),
		RefreshScreen: ScreenBox,
		Fields:        fields,
	}
}

func portalLaneSendPresentation(status string, dryRun bool) (lifecycle PortalActionLifecycle, summary, errorCode string) {
	if dryRun {
		return ActionLifecycleSucceeded, "LOOM Lane transfer plan and transport choice are ready; no files were sent or deleted.", ""
	}
	switch status {
	case lane.BatchStatusPromotionFailed:
		return ActionLifecycleFailed, "LOOM Lane staging is retained; canonical imports promotion needs repair.", "portal.lane_promotion_failed"
	case lane.BatchStatusAcceptedOnMain:
		return ActionLifecycleFailed, "LOOM Lane payload is promoted on main; catalog registration still needs repair.", "portal.lane_catalog_incomplete"
	case lane.BatchStatusCatalogFailed:
		return ActionLifecycleFailed, "LOOM Lane payload is promoted on main, but catalog registration needs repair.", "portal.lane_catalog_failed"
	case lane.BatchStatusSourceCleanupFailed:
		return ActionLifecycleFailed, "LOOM Lane payload is promoted and cataloged; transport staging cleanup and any remaining reviewed local cleanup need repair.", "portal.lane_source_cleanup_failed"
	case lane.BatchStatusCataloged:
		return ActionLifecycleSucceeded, "LOOM Lane payload is promoted and cataloged on main; local content was intentionally retained.", ""
	case lane.BatchStatusPublishedStorageView:
		return ActionLifecycleSucceeded, "LOOM Lane payload has a legacy storage-view publication record.", ""
	case lane.BatchStatusLocalCleanupWithheld:
		return ActionLifecycleFailed, "LOOM Lane payload is promoted and cataloged on main; the changed local source was preserved and cleanup needs attention.", "portal.lane_local_cleanup_withheld"
	case lane.BatchStatusLocalCleanupDone:
		return ActionLifecycleSucceeded, "LOOM Lane transfer completed.", ""
	default:
		return ActionLifecycleSucceeded, "LOOM Lane transfer completed.", ""
	}
}

func (e ActionExecutor) executeBoxLanePlan(action PortalAction) PortalActionResult {
	resolved := boxResolvedFromAction(action)
	if strings.TrimSpace(resolved.RootPath) == "" {
		return failedActionResult(action, "portal.action_target_missing", "Lane plan action is missing a Box path.")
	}
	status := box.Inspect(resolved)
	if status.State != "ok" {
		return failedActionResult(action, "portal.box_not_ready", "Box must be initialized before planning LOOM Lane.")
	}
	profile, err := filepolicy.ParseProfile(firstNonEmpty(action.Executor.Payload["lane_profile"], string(filepolicy.ProfileFaithful)))
	if err != nil {
		return failedActionResult(action, "portal.lane_profile_invalid", err.Error())
	}
	laneRelPath := laneRelativePathFromBoxStatus(status)
	plan, err := lane.BuildTransferPlan(filepath.Join(resolved.RootPath, filepath.FromSlash(laneRelPath)), resolved.RootPath, profile)
	if err != nil {
		return failedActionResult(action, "portal.lane_plan_failed", err.Error())
	}
	fields := []ActionResultField{
		{Label: "Requested Transport", Value: string(plan.Transport.RequestedMode)},
		{Label: "Recommended Transport", Value: string(plan.Transport.RecommendedMode)},
		{Label: "Selected Transport", Value: string(plan.Transport.SelectedMode)},
		{Label: "Transport Reason", Value: firstNonEmpty(plan.Transport.Reason, "-")},
		{Label: "Estimated Archive", Value: storageFormatBytes(plan.Transport.EstimatedArchiveBytes)},
		{Label: "Estimated Temporary Space", Value: storageFormatBytes(plan.Transport.EstimatedTemporaryBytes)},
		{Label: "Policy Profile", Value: string(plan.Profile)},
		{Label: "Policy Version", Value: firstNonEmpty(plan.PolicyVersion, "-")},
		{Label: "Policy Fingerprint", Value: firstNonEmpty(plan.PolicyFingerprint, "-")},
		{Label: "Policy Files", Value: strconv.Itoa(len(plan.PolicyHashes))},
		{Label: "Protected Files", Value: strconv.Itoa(plan.FileCount)},
		{Label: "Protected Directories", Value: strconv.Itoa(plan.DirCount)},
		{Label: "Protected Bytes", Value: storageFormatBytes(plan.TotalBytes)},
		{Label: "Ignored Files", Value: strconv.Itoa(plan.IgnoredFileCount)},
		{Label: "Ignored Directories", Value: strconv.Itoa(plan.IgnoredDirCount)},
		{Label: "Ignored Bytes", Value: storageFormatBytes(plan.IgnoredBytes)},
		{Label: "Pending Items", Value: strconv.Itoa(len(plan.Items))},
		{Label: "Inventory", Value: firstNonEmpty(plan.InventoryHash, "-")},
	}
	for index, warning := range plan.Warnings {
		fields = append(fields, ActionResultField{Label: fmt.Sprintf("Warning %d", index+1), Value: warning})
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "LOOM Lane policy plan and automatic transport recommendation are ready; no transfer tools or main connection were used, and no files or state were written, sent, or removed.",
		RawCommand:    append([]string{}, action.RawCommand...),
		RefreshScreen: ScreenBox,
		Fields:        fields,
	}
}

func laneRelativePathFromBoxStatus(status box.Status) string {
	laneRelPath := lane.DefaultLaneRelPath
	if status.Lane != nil && strings.TrimSpace(status.Lane.LaneRelativePath) != "" {
		return status.Lane.LaneRelativePath
	}
	if status.Contract != nil {
		if area, ok := status.Contract.Areas[box.AreaLane]; ok && strings.TrimSpace(area.Path) != "" {
			laneRelPath = area.Path
		}
	}
	return laneRelPath
}

func (e ActionExecutor) executeBoxIgnoreInspect(action PortalAction) PortalActionResult {
	resolved := boxResolvedFromAction(action)
	if strings.TrimSpace(resolved.RootPath) == "" {
		return failedActionResult(action, "portal.action_target_missing", "Ignore inspection is missing a Box path.")
	}
	operation := strings.ToLower(strings.TrimSpace(firstNonEmpty(action.InputValues["operation"], action.Executor.Payload["operation"], "backup")))
	profile := filepolicy.ProfileManaged
	switch operation {
	case "backup":
	case "lane":
		profile = filepolicy.ProfileFaithful
	default:
		return failedActionResult(action, "portal.ignore_operation_invalid", "Ignore operation must be backup or lane.")
	}
	report, err := filepolicy.Inspect(resolved.RootPath, profile, filepolicy.ResolverOptions{DiscoverUserRules: true}, filepolicy.ScanOptions{MaxEntries: 100000, SampleLimit: 10})
	if err != nil {
		return failedActionResult(action, "portal.ignore_inspect_failed", err.Error())
	}
	fields := []ActionResultField{
		{Label: "Root", Value: report.Root},
		{Label: "Operation", Value: operation},
		{Label: "Policy Profile", Value: string(report.Profile)},
		{Label: "Policy Version", Value: report.PolicyVersion},
		{Label: "Policy Fingerprint", Value: report.PolicyFingerprint},
		{Label: "Policy Files", Value: strconv.Itoa(len(report.PolicyFiles))},
		{Label: "Protected Files", Value: strconv.Itoa(report.Included.Count)},
		{Label: "Protected Bytes", Value: storageFormatBytes(report.Included.Bytes)},
		{Label: "Ignored Files", Value: strconv.Itoa(report.Ignored.Count)},
		{Label: "Ignored Bytes", Value: storageFormatBytes(report.Ignored.Bytes)},
		{Label: "Truncated", Value: fmt.Sprintf("%t", report.Truncated)},
	}
	for _, category := range []filepolicy.RuleCategory{filepolicy.RuleCategoryMandatorySafety, filepolicy.RuleCategoryReconstructible, filepolicy.RuleCategoryUser, filepolicy.RuleCategoryContract} {
		if count := report.IgnoredBySource[category]; count > 0 {
			fields = append(fields, ActionResultField{Label: "Ignored By " + titleFromToken(string(category)), Value: strconv.Itoa(count)})
		}
	}
	for index, sample := range report.IgnoredSamples {
		fields = append(fields, ActionResultField{Label: fmt.Sprintf("Ignored Sample %d", index+1), Value: fmt.Sprintf("%s (%s %s)", sample.Path, sample.RuleCategory, sample.Pattern)})
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "Effective LOOM ignore policy inspected without changing files.",
		Fields:        fields,
		RawCommand:    append([]string{}, action.RawCommand...),
		RefreshScreen: action.RefreshScreen,
	}
}

func (e ActionExecutor) executeBoxLanePendingAcknowledge(action PortalAction) PortalActionResult {
	resolved := boxResolvedFromAction(action)
	if strings.TrimSpace(resolved.RootPath) == "" {
		return failedActionResult(action, "portal.action_target_missing", "Lane pending acknowledgement is missing a Box path.")
	}
	status := box.Inspect(resolved)
	if status.State != "ok" {
		return failedActionResult(action, "portal.box_not_ready", "Box must be initialized before acknowledging LOOM Lane attention.")
	}
	relativePath := firstNonEmpty(action.Executor.Payload["relative_path"], action.TargetRef)
	result, err := lane.AcknowledgePendingItem(lane.AcknowledgePendingInput{
		RootPath:     resolved.RootPath,
		LaneRelPath:  portalLaneRelPathFromStatus(status),
		StatePath:    filepath.Join(status.RuntimeStateWriteRoot, "lane"),
		RelativePath: relativePath,
	})
	if err != nil {
		return failedActionResult(action, "portal.lane_pending_acknowledge_failed", err.Error())
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       "Lane item attention acknowledged without deleting or sending the file.",
		RawCommand:    append([]string{}, action.RawCommand...),
		RefreshScreen: ScreenBox,
		Fields: []ActionResultField{
			{Label: "Path", Value: result.TargetRef},
			{Label: "Attention", Value: result.AttentionStatus},
			{Label: "Files", Value: strconv.Itoa(result.FileCount)},
			{Label: "Dirs", Value: strconv.Itoa(result.DirCount)},
			{Label: "Bytes", Value: strconv.FormatInt(result.Bytes, 10)},
		},
	}
}

func (e ActionExecutor) executeBoxLaneTransferAttention(action PortalAction, archive bool) PortalActionResult {
	resolved := boxResolvedFromAction(action)
	if strings.TrimSpace(resolved.RootPath) == "" {
		return failedActionResult(action, "portal.action_target_missing", "Lane transfer attention action is missing a Box path.")
	}
	status := box.Inspect(resolved)
	if status.State == "invalid" || strings.TrimSpace(status.RuntimeStateWriteRoot) == "" {
		return failedActionResult(action, "portal.box_runtime_state_unavailable", "Box runtime state must be reconciled before changing Lane transfer attention.")
	}
	batchID := firstNonEmpty(action.Executor.Payload["batch_id"], action.TargetRef)
	input := lane.AcknowledgeTransferInput{
		RootPath:  resolved.RootPath,
		StatePath: filepath.Join(status.RuntimeStateWriteRoot, "lane"),
		BatchID:   batchID,
	}
	var (
		result lane.AttentionResult
		err    error
	)
	if archive {
		result, err = lane.ArchiveTransferAttention(input)
	} else {
		result, err = lane.AcknowledgeTransferAttention(input)
	}
	if err != nil {
		return failedActionResult(action, "portal.lane_transfer_attention_failed", err.Error())
	}
	summary := "Failed Lane transfer attention acknowledged without deleting history."
	if archive {
		summary = "Failed Lane transfer attention archived without deleting history."
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleSucceeded,
		Summary:       summary,
		RawCommand:    append([]string{}, action.RawCommand...),
		RefreshScreen: ScreenBox,
		Fields: []ActionResultField{
			{Label: "Batch", Value: result.TargetRef},
			{Label: "Transfer Status", Value: result.Status},
			{Label: "Attention", Value: result.AttentionStatus},
			{Label: "Files", Value: strconv.Itoa(result.FileCount)},
			{Label: "Bytes", Value: strconv.FormatInt(result.Bytes, 10)},
		},
	}
}

func portalLaneRelPathFromStatus(status box.Status) string {
	if status.Lane != nil && strings.TrimSpace(status.Lane.LaneRelativePath) != "" {
		return status.Lane.LaneRelativePath
	}
	if status.Contract != nil {
		if area, ok := status.Contract.Areas[box.AreaLane]; ok && strings.TrimSpace(area.Path) != "" {
			return area.Path
		}
	}
	return lane.DefaultLaneRelPath
}

func boxIDFromStatus(status box.Status) string {
	if status.Contract == nil {
		return ""
	}
	return strings.TrimSpace(status.Contract.BoxID)
}

func boxResolvedFromAction(action PortalAction) box.Resolved {
	payload := action.Executor.Payload
	root := strings.TrimSpace(firstNonEmpty(payload["root_path"], action.TargetRef, action.Executor.Target))
	return box.Resolved{
		RootPath:         root,
		PathSource:       firstNonEmpty(payload["path_source"], "portal_action"),
		Profile:          firstNonEmpty(payload["profile"], box.ProfileWorkspace),
		ProfileSource:    firstNonEmpty(payload["profile_source"], "portal_action"),
		OwnerNode:        firstNonEmpty(payload["owner_node"], "unknown"),
		NodeRole:         payload["node_role"],
		RuntimeStateRoot: payload["runtime_state_root"],
	}
}

func failedActionResult(action PortalAction, code, message string) PortalActionResult {
	if code == "portal.client_missing" && strings.TrimSpace(message) == ErrMissingClient.Error() {
		message = ErrMissingClientFor("execute " + firstNonEmpty(action.Executor.Kind, action.ID, "portal action")).Error()
	}
	return PortalActionResult{
		ActionID:      action.ID,
		Title:         action.Label,
		Status:        ActionLifecycleFailed,
		Summary:       "Action failed.",
		ErrorCode:     code,
		ErrorMessage:  message,
		RawCommand:    append([]string{}, action.RawCommand...),
		RefreshScreen: action.RefreshScreen,
	}
}

func intPayload(action PortalAction, key string, fallback int) int {
	if action.Executor.Payload == nil {
		return fallback
	}
	value, err := strconv.Atoi(strings.TrimSpace(action.Executor.Payload[key]))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func boolActionInput(action PortalAction, key string, fallback bool) bool {
	raw := strings.TrimSpace(firstNonEmpty(action.InputValues[key], action.Executor.Payload[key]))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return value
}

func workerRunFields(result workers.RunOnceResult, idempotencyKey string) []ActionResultField {
	return []ActionResultField{
		{Label: "Worker", Value: firstNonEmpty(result.Worker.Instance.WorkerKey, result.Run.WorkerKind, "-")},
		{Label: "Run", Value: firstNonEmpty(result.Run.WorkerRunID, "-")},
		{Label: "Run Status", Value: firstNonEmpty(result.Run.RunStatus, "-")},
		{Label: "Idempotency", Value: firstNonEmpty(idempotencyKey, result.Run.IdempotencyKey, "-")},
	}
}

func backupRunSummaryFields(raw json.RawMessage) []ActionResultField {
	var summary struct {
		BackupOperationID string `json:"backup_operation_id"`
		BackupDir         string `json:"backup_dir"`
		ArtifactCount     int    `json:"artifact_count"`
		TotalBytes        int64  `json:"total_bytes"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &summary) != nil {
		return nil
	}
	fields := []ActionResultField{}
	if summary.BackupOperationID != "" {
		fields = append(fields, ActionResultField{Label: "Backup", Value: summary.BackupOperationID})
	}
	if summary.BackupDir != "" {
		fields = append(fields, ActionResultField{Label: "Directory", Value: summary.BackupDir})
	}
	if summary.ArtifactCount > 0 {
		fields = append(fields, ActionResultField{Label: "Artifacts", Value: fmt.Sprintf("%d", summary.ArtifactCount)})
		fields = append(fields, ActionResultField{Label: "Total Bytes", Value: fmt.Sprintf("%d", summary.TotalBytes)})
	}
	return fields
}

func objectStoreScanSummaryFields(raw json.RawMessage) []ActionResultField {
	var summary struct {
		Status           string `json:"status"`
		Mode             string `json:"mode"`
		Checked          int64  `json:"checked"`
		Verified         int64  `json:"verified"`
		Missing          int64  `json:"missing"`
		Corrupt          int64  `json:"corrupt"`
		FindingsOpened   int64  `json:"findings_opened"`
		FindingsResolved int64  `json:"findings_resolved"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &summary) != nil {
		return nil
	}
	return []ActionResultField{
		{Label: "Scan Status", Value: firstNonEmpty(summary.Status, "-")},
		{Label: "Mode", Value: firstNonEmpty(summary.Mode, "-")},
		{Label: "Checked", Value: fmt.Sprintf("%d", summary.Checked)},
		{Label: "Verified", Value: fmt.Sprintf("%d", summary.Verified)},
		{Label: "Missing", Value: fmt.Sprintf("%d", summary.Missing)},
		{Label: "Corrupt", Value: fmt.Sprintf("%d", summary.Corrupt)},
		{Label: "Findings Opened", Value: fmt.Sprintf("%d", summary.FindingsOpened)},
		{Label: "Findings Resolved", Value: fmt.Sprintf("%d", summary.FindingsResolved)},
	}
}
