package portal

import (
	"fmt"
	"path/filepath"
	"time"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/lane"
)

func NewBoxInspectAction(status box.Status) PortalAction {
	payload := boxStatusPayload(status)
	action := NewPortalRecordInspectAction("box", ScreenBox, "box", status.RootPath, "LOOM Box", payload)
	action.ID = "box.status.inspect"
	action.Label = "Inspect Box"
	action.Description = "Inspect the resolved local LOOM Box path, profile, contract, folders, policies, and profile-local intake model."
	action.TargetKind = "box"
	action.TargetRef = status.RootPath
	action.TargetLabel = firstNonEmpty(status.RootPath, "LOOM Box")
	action.RawCommand = []string{"loom", "box", "status", "--path", status.RootPath, "--profile", status.Profile}
	action.RefreshScreen = ScreenBox
	return action
}

func NewBoxLanePendingInspectAction(item lane.PendingItem, status box.Status) PortalAction {
	payload := map[string]string{
		"schema_version":   status.SchemaVersion,
		"box_root":         status.RootPath,
		"profile":          status.Profile,
		"relative_path":    item.RelativePath,
		"kind":             item.Kind,
		"bytes":            fmt.Sprintf("%d", item.Bytes),
		"file_count":       fmt.Sprintf("%d", item.FileCount),
		"dir_count":        fmt.Sprintf("%d", item.DirCount),
		"modified_at":      item.ModifiedAt.Format(time.RFC3339),
		"age_seconds":      fmt.Sprintf("%d", item.AgeSeconds),
		"attention":        item.AttentionStatus,
		"attention_reason": item.AttentionReason,
	}
	if portalHasWorkspaceLane(status) {
		payload["lane_path"] = status.Lane.LanePath
		payload["lane_state"] = status.Lane.State
		payload["lane_preflight"] = status.Lane.Preflight.Status
	}
	label := firstNonEmpty(item.RelativePath, "LOOM Lane item")
	action := NewPortalRecordInspectAction("box", ScreenBox, "box_lane_pending", item.RelativePath, label, payload)
	action.ID = "box.lane." + safeActionID(item.RelativePath) + ".inspect"
	action.Label = "Inspect Lane Item"
	action.Description = "Inspect a pending LOOM Lane item before transfer."
	action.TargetKind = "box_lane_pending"
	action.TargetRef = item.RelativePath
	action.TargetLabel = label
	action.RawCommand = []string{"loom", "lane", "status", "--path", status.RootPath, "--profile", status.Profile}
	action.RefreshScreen = ScreenBox
	return action
}

func NewBoxLanePendingAcknowledgeAction(item lane.PendingItem, status box.Status) PortalAction {
	payload := boxStatusPayload(status)
	payload["relative_path"] = item.RelativePath
	payload["attention"] = item.AttentionStatus
	payload["bytes"] = fmt.Sprintf("%d", item.Bytes)
	payload["file_count"] = fmt.Sprintf("%d", item.FileCount)
	payload["dir_count"] = fmt.Sprintf("%d", item.DirCount)
	action := PortalAction{
		ID:            "box.lane." + safeActionID(item.RelativePath) + ".acknowledge",
		Label:         "Acknowledge Lane Item",
		Description:   "Clear active attention for this current Lane item state without deleting, moving, or sending the file.",
		Domain:        "box",
		SourceScreen:  ScreenBox,
		TargetKind:    "box_lane_pending",
		TargetRef:     item.RelativePath,
		TargetLabel:   firstNonEmpty(item.RelativePath, "LOOM Lane item"),
		Risk:          ActionRiskSafeRun,
		State:         ActionAvailable,
		Executor:      PortalActionExecutor{Kind: PortalExecutorBoxLanePendingAck, Target: status.RootPath, Payload: payload},
		RawCommand:    []string{"loom", "lane", "acknowledge-pending", item.RelativePath, "--path", status.RootPath, "--profile", status.Profile},
		RawDetails:    payload,
		RefreshScreen: ScreenBox,
	}
	if item.AttentionStatus == lane.AttentionStatusAcknowledged {
		action.State = ActionDisabled
		action.DisabledReason = "This current Lane item state is already acknowledged."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewBoxLaneTransferInspectAction(transfer lane.TransferSummary, status box.Status) PortalAction {
	payload := map[string]string{
		"schema_version":            status.SchemaVersion,
		"box_root":                  status.RootPath,
		"profile":                   status.Profile,
		"batch_id":                  transfer.BatchID,
		"status":                    transfer.Status,
		"attention":                 transfer.AttentionStatus,
		"file_count":                fmt.Sprintf("%d", transfer.FileCount),
		"bytes":                     fmt.Sprintf("%d", transfer.TotalBytes),
		"visible_path":              transfer.VisibleStoragePath,
		"error":                     transfer.ErrorMessage,
		"cleanup_quarantine":        transfer.LocalCleanupQuarantinePath,
		"cleanup_quarantine_state":  transfer.LocalCleanupQuarantineState,
		"cleanup_quarantine_bytes":  fmt.Sprintf("%d", transfer.LocalCleanupQuarantineBytes),
		"cleanup_quarantine_reason": transfer.LocalCleanupQuarantineRemovalReason,
		"safety_cleanup_state":      transfer.LocalSafetyCleanupState,
		"safety_cleanup_reason":     transfer.LocalSafetyRemovalReason,
		"quarantined_items":         fmt.Sprintf("%d", len(transfer.QuarantinedLocalItems)),
		"restored_items":            fmt.Sprintf("%d", len(transfer.RestoredLocalItems)),
		"removed_items":             fmt.Sprintf("%d", len(transfer.RemovedLocalItems)),
	}
	if transfer.StartedAt != nil {
		payload["started_at"] = transfer.StartedAt.Format(time.RFC3339)
	}
	if transfer.CompletedAt != nil {
		payload["completed_at"] = transfer.CompletedAt.Format(time.RFC3339)
	}
	if transfer.LocalCleanupQuarantineExpiresAt != nil {
		payload["cleanup_quarantine_expires_at"] = transfer.LocalCleanupQuarantineExpiresAt.Format(time.RFC3339)
	}
	if transfer.LocalCleanupQuarantineRemovedAt != nil {
		payload["cleanup_quarantine_removed_at"] = transfer.LocalCleanupQuarantineRemovedAt.Format(time.RFC3339)
	}
	if transfer.LocalSafetyRemovedAt != nil {
		payload["safety_removed_at"] = transfer.LocalSafetyRemovedAt.Format(time.RFC3339)
	}
	action := NewPortalRecordInspectAction("box", ScreenBox, "box_lane_transfer", transfer.BatchID, firstNonEmpty(transfer.BatchID, "Lane transfer"), payload)
	action.ID = "box.lane.transfer." + safeActionID(transfer.BatchID) + ".inspect"
	action.Label = "Inspect Lane Transfer"
	action.Description = "Inspect the latest LOOM Lane transfer record and safe follow-up options."
	action.TargetKind = "box_lane_transfer"
	action.TargetRef = transfer.BatchID
	action.TargetLabel = firstNonEmpty(transfer.BatchID, "Lane transfer")
	action.RawCommand = []string{"loom", "lane", "status", "--path", status.RootPath, "--profile", status.Profile, "--include-fidelity"}
	action.RefreshScreen = ScreenBox
	return action
}

func NewBoxLaneTransferAttentionAction(transfer lane.TransferSummary, status box.Status, archive bool) PortalAction {
	payload := boxStatusPayload(status)
	payload["batch_id"] = transfer.BatchID
	payload["transfer_status"] = transfer.Status
	payload["attention"] = transfer.AttentionStatus
	payload["error"] = transfer.ErrorMessage
	label := "Acknowledge Lane Transfer"
	description := "Clear active attention for this failed Lane transfer while preserving the failed batch record."
	if transfer.Status == lane.BatchStatusLocalCleanupWithheld {
		description = "Clear active attention for this cleanup-withheld Lane transfer while preserving promoted and cataloged main custody and the audit record."
	} else if transfer.Status == lane.BatchStatusAcceptedOnMain {
		description = "Clear active attention for this incomplete Lane transfer while preserving promoted main custody and the audit record."
	}
	executor := PortalExecutorBoxLaneTransferAck
	command := "acknowledge-transfer"
	if archive {
		label = "Archive Lane Transfer Attention"
		description = "Move this failed Lane transfer out of active attention while keeping the audit record."
		if transfer.Status == lane.BatchStatusLocalCleanupWithheld {
			description = "Move this cleanup-withheld Lane transfer out of active attention while preserving promoted and cataloged main custody and the audit record."
		} else if transfer.Status == lane.BatchStatusAcceptedOnMain {
			description = "Move this incomplete Lane transfer out of active attention while preserving promoted main custody and the audit record."
		}
		executor = PortalExecutorBoxLaneTransferArchive
		command = "archive-transfer"
	}
	action := PortalAction{
		ID:            "box.lane.transfer." + safeActionID(transfer.BatchID) + "." + command,
		Label:         label,
		Description:   description,
		Domain:        "box",
		SourceScreen:  ScreenBox,
		TargetKind:    "box_lane_transfer",
		TargetRef:     transfer.BatchID,
		TargetLabel:   firstNonEmpty(transfer.BatchID, "Lane transfer"),
		Risk:          ActionRiskSafeRun,
		State:         ActionAvailable,
		Executor:      PortalActionExecutor{Kind: executor, Target: status.RootPath, Payload: payload},
		RawCommand:    []string{"loom", "lane", command, transfer.BatchID, "--path", status.RootPath, "--profile", status.Profile},
		RawDetails:    payload,
		RefreshScreen: ScreenBox,
	}
	if !boxLaneTransferAttentionStatus(transfer.Status) {
		action.State = ActionDisabled
		action.DisabledReason = "Only incomplete, failed, obsolete-migration, or cleanup-withheld Lane transfer attention can be acknowledged or archived."
	} else if transfer.AttentionStatus == lane.AttentionStatusAcknowledged || transfer.AttentionStatus == lane.AttentionStatusArchived {
		action.State = ActionDisabled
		action.DisabledReason = "This Lane transfer attention is already resolved."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewBoxLaneTransferRepairAction(transfer lane.TransferSummary, status box.Status) PortalAction {
	payload := boxStatusPayload(status)
	payload["batch_id"] = transfer.BatchID
	payload["transfer_status"] = transfer.Status
	payload["lane_repair_batch_id"] = transfer.BatchID
	description := "Retry idempotent imports promotion, catalog registration, and transport-staging cleanup for this exact Lane batch."
	if transfer.Status == lane.BatchStatusSourceCleanupFailed {
		description = "Retry idempotent transport-staging cleanup, then honor the original keep-local or safe local-quarantine policy without re-uploading."
	} else if transfer.Status == lane.BatchStatusLocalCleanupWithheld {
		description = "Retry only idempotent transport-staging cleanup while preserving canonical custody and local-cleanup evidence."
	}
	action := PortalAction{
		ID:            "box.lane.transfer." + safeActionID(transfer.BatchID) + ".repair",
		Label:         "Repair Lane Transfer",
		Description:   description,
		Domain:        "box",
		SourceScreen:  ScreenBox,
		TargetKind:    "box_lane_transfer",
		TargetRef:     transfer.BatchID,
		TargetLabel:   firstNonEmpty(transfer.BatchID, "Lane transfer"),
		Risk:          ActionRiskSensitive,
		State:         ActionAvailable,
		Executor:      PortalActionExecutor{Kind: PortalExecutorBoxLaneSend, Target: status.RootPath, Payload: payload},
		RawCommand:    []string{"loom", "lane", "repair", transfer.BatchID, "--path", status.RootPath, "--profile", status.Profile},
		RawDetails:    payload,
		RefreshScreen: ScreenBox,
	}
	if !boxLaneTransferRepairStatus(transfer) {
		action.State = ActionDisabled
		action.DisabledReason = "This Lane transfer phase has no safe idempotent main-custody repair action."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func boxLaneTransferAttentionStatus(status string) bool {
	switch status {
	case lane.BatchStatusFailed, lane.BatchStatusPromotionFailed, lane.BatchStatusAcceptedOnMain,
		lane.BatchStatusCatalogFailed, lane.BatchStatusSourceCleanupFailed,
		lane.BatchStatusObsoleteAcceptedMigrationRequired, lane.BatchStatusLocalCleanupWithheld:
		return true
	default:
		return false
	}
}

func boxLaneTransferRepairStatus(transfer lane.TransferSummary) bool {
	switch transfer.Status {
	case lane.BatchStatusPromotionFailed, lane.BatchStatusAcceptedOnMain, lane.BatchStatusCatalogFailed, lane.BatchStatusSourceCleanupFailed:
		return true
	case lane.BatchStatusLocalCleanupWithheld:
		return transfer.SelectedTransport == lane.TransportModeBundleSeed
	default:
		return false
	}
}

func NewBoxLaneSendAction(status box.Status) PortalAction {
	payload := boxStatusPayload(status)
	payload["lane_profile"] = string(filepolicy.ProfileFaithful)
	payload["lane_transport"] = string(lane.TransportModeAuto)
	action := PortalAction{
		ID:            "box.lane.send",
		Label:         "Send Lane",
		Description:   "Send the faithful Lane plan to main using the automatic file-tree or verified bundle choice, honoring .loomignore while retaining dependencies and Git history unless a user rule excludes them.",
		Domain:        "box",
		SourceScreen:  ScreenBox,
		TargetKind:    "box_lane",
		TargetRef:     status.RootPath,
		TargetLabel:   "LOOM Lane",
		Risk:          ActionRiskSensitive,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorBoxLaneSend, Target: status.RootPath, Payload: payload},
		RawCommand:    []string{"loom", "lane", "send", "--path", status.RootPath, "--profile", status.Profile},
		RawDetails:    payload,
		RefreshScreen: ScreenBox,
	}
	applyBoxLaneSendAvailability(&action, status)
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewBoxLanePlanAction(status box.Status) PortalAction {
	payload := boxStatusPayload(status)
	payload["lane_profile"] = string(filepolicy.ProfileFaithful)
	payload["lane_transport"] = string(lane.TransportModeAuto)
	payload["dry_run"] = "true"
	action := PortalAction{
		ID:                  "box.lane.plan",
		Label:               "Inspect Transfer Plan",
		Description:         "Build the faithful policy plan and automatic transport recommendation locally without rsync, SSH, main, remote preparation, filesystem writes, or cleanup.",
		Domain:              "box",
		SourceScreen:        ScreenBox,
		TargetKind:          "box_lane",
		TargetRef:           status.RootPath,
		TargetLabel:         "LOOM Lane",
		Risk:                ActionRiskInspect,
		State:               ActionAvailable,
		InputValues:         map[string]string{},
		Executor:            PortalActionExecutor{Kind: PortalExecutorBoxLanePlan, Target: status.RootPath, Payload: payload},
		ExecutionDependency: ExecutionDependencyLocal,
		RawCommand:          []string{"loom", "lane", "status", "--path", status.RootPath, "--profile", status.Profile},
		RawDetails:          payload,
		RefreshScreen:       ScreenBox,
	}
	applyBoxLanePlanAvailability(&action, status)
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewBoxLaneProfileSendAction(status box.Status, profile filepolicy.Profile) PortalAction {
	payload := boxStatusPayload(status)
	payload["lane_profile"] = string(profile)
	payload["lane_transport"] = string(lane.TransportModeAuto)
	idSuffix := string(profile)
	label := "Send Source-Only Lane"
	description := "Send a source-only Lane plan to main, excluding managed reconstructible dependencies while honoring .loomignore."
	flag := "--source-only"
	if profile == filepolicy.ProfileExact {
		label = "Send Exact Lane"
		description = "Send an exact Lane plan to main, bypassing .loomignore while still excluding mandatory LOOM transfer state."
		flag = "--exact"
	}
	action := PortalAction{
		ID:            "box.lane.send." + idSuffix,
		Label:         label,
		Description:   description,
		Domain:        "box",
		SourceScreen:  ScreenBox,
		TargetKind:    "box_lane",
		TargetRef:     status.RootPath,
		TargetLabel:   "LOOM Lane",
		Risk:          ActionRiskSensitive,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorBoxLaneSend, Target: status.RootPath, Payload: payload},
		RawCommand:    []string{"loom", "lane", "send", flag, "--path", status.RootPath, "--profile", status.Profile},
		RawDetails:    payload,
		RefreshScreen: ScreenBox,
	}
	applyBoxLaneSendAvailability(&action, status)
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	if profile == filepolicy.ProfileExact {
		action.ConfirmationPolicy.Prompt = "Exact mode bypasses every .loomignore rule. Confirm that the additional data belongs in this transfer."
	}
	return action
}

func NewBoxIgnoreInspectAction(status box.Status) PortalAction {
	payload := boxStatusPayload(status)
	payload["operation"] = "backup"
	action := PortalAction{
		ID:           "box.ignore.inspect",
		Label:        "Inspect Effective Ignore Rules",
		Description:  "Inspect the effective managed backup or faithful Lane policy, including discovered .loomignore files and bounded ignored-path samples.",
		Domain:       "box",
		SourceScreen: ScreenBox,
		TargetKind:   "box_ignore_policy",
		TargetRef:    status.RootPath,
		TargetLabel:  firstNonEmpty(status.RootPath, "LOOM Box"),
		Risk:         ActionRiskInspect,
		State:        ActionAvailable,
		InputFields: []PortalActionField{{
			Name:    "operation",
			Label:   "Operation",
			Kind:    ActionFieldSelect,
			Value:   "backup",
			Options: []string{"backup", "lane"},
			Help:    "Backup uses managed policy; Lane uses faithful policy.",
		}},
		InputValues:         map[string]string{"operation": "backup"},
		Executor:            PortalActionExecutor{Kind: PortalExecutorBoxIgnoreInspect, Target: status.RootPath, Payload: payload},
		ExecutionDependency: ExecutionDependencyLocal,
		RawCommand:          []string{"loom", "ignore", "inspect", status.RootPath, "--operation", "backup"},
		RawDetails:          payload,
		RefreshScreen:       ScreenBox,
	}
	if status.State != "ok" {
		action.State = ActionDisabled
		action.DisabledReason = "Initialize or repair the Box before inspecting effective ignore rules."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func applyBoxLanePlanAvailability(action *PortalAction, status box.Status) {
	switch {
	case status.Profile != box.ProfileWorkspace:
		action.State = ActionDisabled
		action.DisabledReason = "LOOM Lane user actions are available only on workspace nodes; main receives transfers in Storage Imports."
	case status.State != "ok":
		action.State = ActionDisabled
		action.DisabledReason = "Initialize or repair the Box before planning LOOM Lane."
	case status.Lane == nil:
		action.State = ActionDisabled
		action.DisabledReason = "LOOM Lane status is unavailable."
	case status.Lane.PendingItems == 0:
		action.State = ActionDisabled
		action.DisabledReason = "LOOM Lane is empty."
	}
}

func applyBoxLaneSendAvailability(action *PortalAction, status box.Status) {
	applyBoxLanePlanAvailability(action, status)
	if action.State == ActionDisabled {
		return
	}
	if status.Lane.Preflight.Status == lane.PreflightMissingTools {
		action.State = ActionDisabled
		action.DisabledReason = "Install rsync and ssh before sending LOOM Lane."
	}
}

func NewBoxInitAction(status box.Status) PortalAction {
	payload := boxStatusPayload(status)
	action := PortalAction{
		ID:            "box.init",
		Label:         "Initialize Box",
		Description:   "Create or repair the local LOOM Box folder scaffold and generated contracts.",
		Domain:        "box",
		SourceScreen:  ScreenBox,
		TargetKind:    "box",
		TargetRef:     status.RootPath,
		TargetLabel:   firstNonEmpty(status.RootPath, "LOOM Box"),
		Risk:          ActionRiskSensitive,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorBoxInit, Target: status.RootPath, Payload: payload},
		RawCommand:    []string{"loom", "box", "init", "--path", status.RootPath, "--profile", status.Profile},
		RawDetails:    payload,
		RefreshScreen: ScreenBox,
	}
	if status.State == "ok" {
		action.State = ActionDisabled
		action.DisabledReason = "This Box is already initialized and complete."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewBoxWatchPlanAction(status box.Status) PortalAction {
	payload := boxStatusPayload(status)
	action := NewPortalRecordInspectAction("box", ScreenBox, "box_watch_plan", status.RootPath+".watch_plan", "Box Watch Plan", payload)
	action.ID = "box.watch_plan.inspect"
	action.Label = "View Watch Plan"
	action.Description = "Inspect the Box Notes/Documents watched-root plan."
	action.TargetKind = "box_watch_plan"
	action.RawCommand = []string{"loom", "box", "watch-plan", "--path", status.RootPath, "--profile", status.Profile}
	action.RefreshScreen = ScreenBox
	if status.State != "ok" {
		action.State = ActionDisabled
		action.DisabledReason = "Initialize or repair the Box before compiling the watch plan."
	}
	return action
}

func NewBoxWatchStatusAction(status box.Status) PortalAction {
	payload := boxStatusPayload(status)
	action := NewPortalRecordInspectAction("box", ScreenBox, "box_watch_status", status.RootPath+".watch_status", "Box Watch Status", payload)
	action.ID = "box.watch_status.inspect"
	action.Label = "View Watch Status"
	action.Description = "Inspect Box watched-root registrations and node-agent report state."
	action.TargetKind = "box_watch_status"
	action.RawCommand = []string{"loom", "box", "watch-status", "--path", status.RootPath, "--profile", status.Profile}
	action.RefreshScreen = ScreenBox
	if status.State != "ok" {
		action.State = ActionDisabled
		action.DisabledReason = "Initialize or repair the Box before checking watch status."
	}
	return action
}

func NewBoxWatchApplyAction(status box.Status) PortalAction {
	payload := boxStatusPayload(status)
	action := PortalAction{
		ID:           "box.watch_policy.apply",
		Label:        "Apply Box Watch Policy",
		Description:  "Register the Box Notes/Documents policies as watched roots on main.",
		Domain:       "box",
		SourceScreen: ScreenBox,
		TargetKind:   "box_watch_policy",
		TargetRef:    status.RootPath,
		TargetLabel:  "Box Watch Policy",
		Risk:         ActionRiskSensitive,
		State:        ActionAvailable,
		InputFields: []PortalActionField{{
			Name:  "dry_run",
			Label: "Dry Run",
			Kind:  ActionFieldBoolean,
			Value: "false",
			Help:  "Set true to validate the watch plan without writing watched-root registrations.",
		}},
		InputValues:   map[string]string{"dry_run": "false"},
		Executor:      PortalActionExecutor{Kind: PortalExecutorBoxWatchApply, Target: status.RootPath, Payload: payload},
		RawCommand:    []string{"loom", "box", "watch-apply", "--path", status.RootPath, "--profile", status.Profile},
		RawDetails:    payload,
		RefreshScreen: ScreenBox,
	}
	if status.State != "ok" {
		action.State = ActionDisabled
		action.DisabledReason = "Initialize or repair the Box before applying watch policy."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewBoxProjectScaffoldAction(status box.Status) PortalAction {
	payload := boxStatusPayload(status)
	action := PortalAction{
		ID:           "box.project.scaffold",
		Label:        "Create Project In Box",
		Description:  "Create a project scaffold under the Box Projects folder.",
		Domain:       "box",
		SourceScreen: ScreenBox,
		TargetKind:   "box_project_default",
		TargetRef:    status.DefaultProjectPath,
		TargetLabel:  firstNonEmpty(status.DefaultProjectPath, filepath.Join(status.RootPath, "Projects")),
		Risk:         ActionRiskSafeRun,
		State:        ActionAvailable,
		InputFields: []PortalActionField{{
			Name:        "project_name",
			Label:       "Project Name",
			Kind:        ActionFieldText,
			Required:    true,
			Placeholder: "New Project",
			Help:        "The project will be scaffolded inside the Box Projects folder.",
		}},
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorBoxProjectScaffold, Target: status.DefaultProjectPath, Payload: payload},
		RawCommand:    []string{"loom", "project", "scaffold", "<project-name>"},
		RawDetails:    payload,
		RefreshScreen: ScreenProjects,
	}
	if status.State != "ok" {
		action.State = ActionDisabled
		action.DisabledReason = "Initialize or repair the Box before creating a Box project."
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewBoxPathInspectAction(kind string, row box.PathStatus, status box.Status) PortalAction {
	payload := map[string]string{
		"kind":          row.Kind,
		"key":           row.Key,
		"path":          row.Path,
		"relative_path": row.RelativePath,
		"status":        row.Status,
		"enabled":       fmt.Sprintf("%t", row.Enabled),
		"box_root":      status.RootPath,
		"profile":       status.Profile,
	}
	recordKind := "box_area"
	if kind == "policy" {
		recordKind = "box_policy"
	}
	action := NewPortalRecordInspectAction("box", ScreenBox, recordKind, row.Key, row.Key, payload)
	action.ID = fmt.Sprintf("box.%s.%s.inspect", recordKind, safeActionID(row.Key))
	action.Label = "Inspect " + titleFromToken(row.Key)
	action.Description = "Inspect this Box " + kind + " path."
	action.TargetKind = recordKind
	action.TargetRef = row.Key
	action.TargetLabel = firstNonEmpty(row.Path, row.RelativePath, row.Key)
	action.RawCommand = []string{"loom", "box", "status", "--path", status.RootPath, "--profile", status.Profile}
	action.RefreshScreen = ScreenBox
	return action
}

func boxStatusPayload(status box.Status) map[string]string {
	payload := map[string]string{
		"schema_version":           status.SchemaVersion,
		"root_path":                status.RootPath,
		"path_source":              status.PathSource,
		"profile":                  status.Profile,
		"profile_source":           status.ProfileSource,
		"owner_node":               status.OwnerNode,
		"node_role":                status.NodeRole,
		"state":                    status.State,
		"initialized":              fmt.Sprintf("%t", status.Initialized),
		"contract_path":            status.ContractPath,
		"contract_state":           status.ContractState,
		"default_project_path":     status.DefaultProjectPath,
		"runtime_state_root":       status.RuntimeStateRoot,
		"runtime_state_read_root":  status.RuntimeStateReadRoot,
		"runtime_state_write_root": status.RuntimeStateWriteRoot,
	}
	if portalHasWorkspaceLane(status) {
		payload["lane_state"] = status.Lane.State
		payload["lane_path"] = status.Lane.LanePath
		payload["lane_status_path"] = status.Lane.StatePath
		payload["lane_pending_items"] = fmt.Sprintf("%d", status.Lane.PendingItems)
		payload["lane_pending_files"] = fmt.Sprintf("%d", status.Lane.PendingFiles)
		payload["lane_pending_dirs"] = fmt.Sprintf("%d", status.Lane.PendingDirs)
		payload["lane_pending_bytes"] = fmt.Sprintf("%d", status.Lane.PendingBytes)
		payload["lane_preflight"] = status.Lane.Preflight.Status
		payload["lane_profile"] = string(status.Lane.Profile)
		payload["lane_policy_version"] = status.Lane.PolicyVersion
		payload["lane_policy_fingerprint"] = status.Lane.PolicyFingerprint
		payload["lane_ignored_files"] = fmt.Sprintf("%d", status.Lane.IgnoredFileCount)
		payload["lane_ignored_bytes"] = fmt.Sprintf("%d", status.Lane.IgnoredBytes)
		payload["lane_recovery_retained_bytes"] = fmt.Sprintf("%d", status.Lane.RecoveryStorage.RetainedBytes)
		payload["lane_recovery_grace_bytes"] = fmt.Sprintf("%d", status.Lane.RecoveryStorage.SuccessfulGraceBytes)
		payload["lane_recovery_protected_bytes"] = fmt.Sprintf("%d", status.Lane.RecoveryStorage.ProtectedEvidenceBytes)
		payload["lane_recovery_cleanup_bytes"] = fmt.Sprintf("%d", status.Lane.RecoveryStorage.CleanupQuarantineBytes)
		payload["lane_recovery_safety_bytes"] = fmt.Sprintf("%d", status.Lane.RecoveryStorage.TransportSafetyBytes)
		payload["lane_recovery_untracked_bytes"] = fmt.Sprintf("%d", status.Lane.RecoveryStorage.UntrackedBytes)
	}
	return payload
}
