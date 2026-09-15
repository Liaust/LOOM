package portal

import (
	"context"
	"fmt"
	"strings"

	"loom.local/loom/internal/backupcontracts"
)

func (e ActionExecutor) executeProtectedFolderProtect(ctx context.Context, action PortalAction) PortalActionResult {
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	preflightID := strings.TrimSpace(action.InputValues["preflight_id"])
	if preflightID == "" {
		request := backupcontracts.PreflightCreateRequest{
			NodeRef: action.FieldValueByName("owner_node"),
			Path:    action.FieldValueByName("target_path"),
			IdempotencyKey: "portal.protect." + protectedFolderContractKey(
				action.FieldValueByName("display_name"),
				action.FieldValueByName("target_path"),
			),
		}
		envelope, err := e.Client.CreateBackupContractPreflight(ctx, e.CorrelationID, request)
		if err != nil {
			return failedActionResult(action, "portal.protected_folder_preflight_failed", err.Error())
		}
		return e.projectProtectedFolderPreflight(ctx, action, envelope.Data)
	}
	if action.FieldValueByName("preflight_ready") != "true" {
		envelope, err := e.Client.GetBackupContractPreflight(ctx, e.CorrelationID, preflightID)
		if err != nil {
			return failedActionResult(action, "portal.protected_folder_preflight_failed", err.Error())
		}
		return e.projectProtectedFolderPreflight(ctx, action, envelope.Data)
	}

	key := strings.TrimSpace(action.FieldValueByName("contract_key"))
	if key == "" {
		key = protectedFolderContractKey(action.FieldValueByName("display_name"), action.FieldValueByName("target_path"))
	}
	envelope, err := e.Client.CreateBackupContract(ctx, e.CorrelationID, backupcontracts.CreateRequest{
		Key:         key,
		DisplayName: strings.TrimSpace(action.FieldValueByName("display_name")),
		OwnerNode:   action.FieldValueByName("owner_node"),
		TargetPath:  action.FieldValueByName("target_path"),
		TargetScope: backupcontracts.TargetScopeOwnerNodeAbsolute,
		BackupMode:  protectedFolderBackupMode(action.FieldValueByName("backup_mode")),
		PreflightID: preflightID,
	})
	if err != nil {
		return failedActionResult(action, "portal.protected_folder_create_failed", err.Error())
	}
	result := envelope.Data
	summary := "Protection intent saved."
	if result.NodeApplyQueued && !result.NodeApplied {
		summary = "Protection intent saved; Waiting For Node until the owner applies it."
	}
	return PortalActionResult{
		ActionID: action.ID, Title: action.Label, Status: ActionLifecycleSucceeded, Summary: summary,
		Fields: []ActionResultField{
			{Label: "Contract", Value: result.Contract.Key},
			{Label: "Owner Node", Value: result.Contract.OwnerNode},
			{Label: "Path", Value: result.Contract.Target.Path},
			{Label: "Desired Registered", Value: boolLabel(result.DesiredStateRegistered)},
			{Label: "Node Work Queued", Value: boolLabel(result.NodeApplyQueued)},
			{Label: "Node Applied", Value: boolLabel(result.NodeApplied)},
		},
		RefreshScreen: ScreenNodes,
	}
}

func (e ActionExecutor) projectProtectedFolderPreflight(ctx context.Context, action PortalAction, record backupcontracts.PreflightRecord) PortalActionResult {
	// Poll only already-created durable state. No sleep and no unbounded owner-node wait.
	for attempt := 0; record.Status == backupcontracts.PreflightStatusPending && attempt < 2; attempt++ {
		envelope, err := e.Client.GetBackupContractPreflight(ctx, e.CorrelationID, record.PreflightID)
		if err != nil {
			return failedActionResult(action, "portal.protected_folder_preflight_failed", err.Error())
		}
		record = envelope.Data
	}
	next := map[string]string{"preflight_id": record.PreflightID}
	fields := []ActionResultField{
		{Label: "Preflight", Value: record.PreflightID},
		{Label: "Owner Node", Value: firstNonEmpty(action.FieldValueByName("owner_node"), record.TargetNodeID)},
		{Label: "Path", Value: firstNonEmpty(record.CanonicalPath, record.RequestedPath)},
	}
	if record.Status == backupcontracts.PreflightStatusPending {
		next["preflight_ready"] = "false"
		return PortalActionResult{ActionID: action.ID, Title: action.Label, Status: ActionLifecycleWaiting, Summary: "Waiting For Node. Main accepted the durable preflight request; resume by rechecking this record when the owner node returns.", Fields: fields, NextInput: next}
	}
	if record.Result == nil {
		next["preflight_ready"] = "false"
		message := firstNonEmpty(record.ErrorMessage, "The owner node could not produce preflight evidence.")
		fields = append(fields, ActionResultField{Label: "Status", Value: record.Status})
		return PortalActionResult{ActionID: action.ID, Title: action.Label, Status: ActionLifecycleReview, Summary: message, Fields: fields, NextInput: next, Blocking: true, ErrorCode: firstNonEmpty(record.ErrorCode, "portal.protected_folder_preflight_blocked")}
	}
	result := record.Result
	next["preflight_ready"] = "true"
	fields = append(fields,
		ActionResultField{Label: "Canonical Path", Value: firstNonEmpty(result.CanonicalPath, result.RequestedPath)},
		ActionResultField{Label: "Files", Value: fmt.Sprintf("%d", result.FileCount)},
		ActionResultField{Label: "Folders", Value: fmt.Sprintf("%d", result.DirectoryCount)},
		ActionResultField{Label: "Apparent Size", Value: storageFormatBytes(result.ApparentBytes)},
		ActionResultField{Label: "Ignore Profile", Value: firstNonEmpty(result.Policy.Profile, "managed")},
		ActionResultField{Label: "Included", Value: fmt.Sprintf("%d files / %s", result.Policy.IncludedCount, storageFormatBytes(result.Policy.IncludedBytes))},
		ActionResultField{Label: "Ignored", Value: fmt.Sprintf("%d files / %s", result.Policy.IgnoredCount, storageFormatBytes(result.Policy.IgnoredBytes))},
		ActionResultField{Label: "Policy Files", Value: firstNonEmpty(strings.Join(result.Policy.PolicyFiles, ", "), "none discovered")},
		ActionResultField{Label: "Estimate Truncated", Value: boolLabel(result.Truncated)},
	)
	blocking := !result.Exists || !result.Directory || !result.Readable
	for _, finding := range result.Findings {
		fields = append(fields, ActionResultField{Label: "Finding (" + firstNonEmpty(finding.Severity, "info") + ")", Value: firstNonEmpty(finding.Message, finding.Code)})
		blocking = blocking || finding.Blocking
	}
	summary := "Preflight complete. Review the owner-node evidence before confirmation."
	if blocking {
		summary = "Preflight found a blocking condition. Fix it and recheck before creating protection."
	}
	return PortalActionResult{ActionID: action.ID, Title: action.Label, Status: ActionLifecycleReview, Summary: summary, Fields: fields, NextInput: next, Blocking: blocking}
}

func (e ActionExecutor) executeProtectedFolderInspect(ctx context.Context, action PortalAction) PortalActionResult {
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	envelope, err := e.Client.GetProtectedFolder(ctx, e.CorrelationID, action.Executor.Target)
	if err != nil {
		return failedActionResult(action, "portal.protected_folder_inspect_failed", err.Error())
	}
	record := envelope.Data
	return PortalActionResult{ActionID: action.ID, Title: action.Label, Status: ActionLifecycleSucceeded, Summary: "Protected-folder lifecycle evidence loaded.", Fields: protectedFolderResultFields(record)}
}

func (e ActionExecutor) executeProtectedFolderEnable(ctx context.Context, action PortalAction) PortalActionResult {
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	envelope, err := e.Client.EnableBackupContract(ctx, e.CorrelationID, action.Executor.Target, backupcontracts.EnableRequest{})
	if err != nil {
		return failedActionResult(action, "portal.protected_folder_enable_failed", err.Error())
	}
	return protectedFolderLifecycleActionResult(action, envelope.Data, "Protection enabled; owner-node application is tracked separately.")
}

func (e ActionExecutor) executeProtectedFolderDisable(ctx context.Context, action PortalAction) PortalActionResult {
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	envelope, err := e.Client.DisableBackupContract(ctx, e.CorrelationID, action.Executor.Target, backupcontracts.DisableRequest{})
	if err != nil {
		return failedActionResult(action, "portal.protected_folder_disable_failed", err.Error())
	}
	return protectedFolderLifecycleActionResult(action, envelope.Data, "Protection disabled. Source data was retained.")
}

func (e ActionExecutor) executeProtectedFolderDelete(ctx context.Context, action PortalAction) PortalActionResult {
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	envelope, err := e.Client.DeleteBackupContract(ctx, e.CorrelationID, action.Executor.Target, backupcontracts.DeleteRequest{})
	if err != nil {
		return failedActionResult(action, "portal.protected_folder_delete_failed", err.Error())
	}
	return protectedFolderLifecycleActionResult(action, envelope.Data, "Protection contract deleted. Source data was retained.")
}

func (e ActionExecutor) executeProtectedFolderRetry(ctx context.Context, action PortalAction) PortalActionResult {
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	envelope, err := e.Client.RetryBackupContractActivation(ctx, e.CorrelationID, action.Executor.Target, backupcontracts.RetryActivationRequest{Reason: "portal operator retry"})
	if err != nil {
		return failedActionResult(action, "portal.protected_folder_retry_failed", err.Error())
	}
	queue := envelope.Data
	return PortalActionResult{ActionID: action.ID, Title: action.Label, Status: ActionLifecycleSucceeded, Summary: "Desired state requeued; Waiting For Node until acknowledgement and matching root evidence arrive.", Fields: []ActionResultField{{Label: "Owner Node", Value: queue.NodeID}, {Label: "Desired Revision", Value: fmt.Sprintf("%d", queue.DesiredRevision)}, {Label: "Desired Hash", Value: queue.DesiredHash}}, RefreshScreen: ScreenNodes}
}

func (e ActionExecutor) executeProtectedFolderRecheck(ctx context.Context, action PortalAction) PortalActionResult {
	if e.Client == nil {
		return failedActionResult(action, "portal.client_missing", ErrMissingClient.Error())
	}
	envelope, err := e.Client.RecheckBackupContract(ctx, e.CorrelationID, action.Executor.Target)
	if err != nil {
		return failedActionResult(action, "portal.protected_folder_recheck_failed", err.Error())
	}
	record := envelope.Data
	status := ActionLifecycleSucceeded
	summary := "Fresh owner-node preflight completed."
	if record.Status == backupcontracts.PreflightStatusPending {
		status = ActionLifecycleWaiting
		summary = "Waiting For Node. Main accepted the recheck request."
	}
	return PortalActionResult{ActionID: action.ID, Title: action.Label, Status: status, Summary: summary, Fields: []ActionResultField{{Label: "Preflight", Value: record.PreflightID}, {Label: "Status", Value: record.Status}}, RefreshScreen: ScreenNodes}
}

func protectedFolderLifecycleActionResult(action PortalAction, lifecycle backupcontracts.LifecycleResult, summary string) PortalActionResult {
	return PortalActionResult{ActionID: action.ID, Title: action.Label, Status: ActionLifecycleSucceeded, Summary: summary, Fields: []ActionResultField{{Label: "Contract", Value: lifecycle.Contract.Key}, {Label: "Desired Registered", Value: boolLabel(lifecycle.DesiredStateRegistered)}, {Label: "Node Work Queued", Value: boolLabel(lifecycle.NodeApplyQueued)}, {Label: "Node Applied", Value: boolLabel(lifecycle.NodeApplied)}}, RefreshScreen: ScreenNodes}
}

func protectedFolderResultFields(record backupcontracts.ProtectedFolderRecord) []ActionResultField {
	lastBackup := "not accepted"
	if record.LastBackupAcceptedAt != nil {
		lastBackup = timeOrDash(*record.LastBackupAcceptedAt)
	}
	return []ActionResultField{
		{Label: "Contract", Value: record.Key},
		{Label: "Lifecycle", Value: record.Lifecycle},
		{Label: "Owner Node", Value: firstNonEmpty(record.OwnerNodeKey, record.Contract.OwnerNode, record.OwnerNodeID)},
		{Label: "Path", Value: record.Contract.Target.Path},
		{Label: "Desired Registered", Value: boolLabel(record.DesiredRegistered)},
		{Label: "Node Applied", Value: boolLabel(record.NodeApplied)},
		{Label: "Root Reported", Value: boolLabel(record.RootReported)},
		{Label: "Config Matches", Value: boolLabel(record.ConfigHashMatches)},
		{Label: "Last Accepted Backup", Value: lastBackup},
		{Label: "Attention", Value: firstNonEmpty(record.AttentionReason, "none")},
	}
}

func boolLabel(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
