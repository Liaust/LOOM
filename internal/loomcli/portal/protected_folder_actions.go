package portal

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/nodes"
)

var protectedFolderKeySeparators = regexp.MustCompile(`[^a-z0-9]+`)

func NewProtectFolderAction(nodeRecords []nodes.Node) PortalAction {
	options := make([]string, 0, len(nodeRecords))
	for _, node := range nodeRecords {
		if node.Status != "active" {
			continue
		}
		options = append(options, firstNonEmpty(node.NodeKey, node.NodeID))
	}
	action := PortalAction{
		ID:                  "backup.protected_folder.protect",
		Label:               "Protect Folder",
		Description:         "Check a folder on its owner node, review effective ignore policy and size evidence, then register durable protection intent.",
		Domain:              "backup",
		SourceScreen:        ScreenNodes,
		TargetKind:          "protected_folder",
		TargetLabel:         "New protected folder",
		Risk:                ActionRiskSensitive,
		State:               ActionAvailable,
		InteractionType:     ActionInteractionWizard,
		ExecutionDependency: ExecutionDependencyMain,
		Executor:            PortalActionExecutor{Kind: PortalExecutorProtectedFolderProtect, Payload: map[string]string{}},
		InputValues:         map[string]string{"backup_mode": "Full Backup", "advanced_options": "false"},
		InputFields: []PortalActionField{
			{Name: "display_name", Label: "Name", Kind: ActionFieldText, Placeholder: "Family Photos"},
			{Name: "contract_key", Label: "Contract Key", Kind: ActionFieldText, Placeholder: "generated from name or path", Help: "Optional stable key. Lowercase letters, numbers, and hyphens are used."},
			{Name: "owner_node", Label: "Owner Node", Kind: ActionFieldSelect, Required: true, Options: options, Help: "The node that owns and scans this path."},
			{Name: "target_path", Label: "Folder Path", Kind: ActionFieldPath, Required: true, Placeholder: "/absolute/path"},
			{Name: "backup_mode", Label: "Backup Mode", Kind: ActionFieldSelect, Required: true, Options: []string{"Full Backup", "Metadata Only", "No Backup"}},
			{Name: "advanced_options", Label: "Advanced Options", Kind: ActionFieldBoolean, Help: "Collapsed by default. The managed ignore policy is used unless the contract is edited through an advanced interface."},
		},
		ConfirmationPolicy: ConfirmationPolicy{
			Required: true,
			Strength: ConfirmationStrengthNormal,
			Prompt:   "Create this protected-folder contract? The source folder is retained and is never moved or deleted.",
		},
		RefreshScreen: ScreenNodes,
	}
	if len(options) == 0 {
		action.State = ActionDisabled
		action.DisabledReason = "No active owner nodes are available."
	} else {
		action.InputValues["owner_node"] = options[0]
	}
	return action
}

func protectedFolderActions(record backupcontracts.ProtectedFolderRecord) (PortalAction, []PortalAction) {
	inspect := newProtectedFolderRecordAction(record, PortalExecutorProtectedFolderInspect, "Inspect Protected Folder", ActionRiskInspect)
	related := []PortalAction{}
	switch record.Lifecycle {
	case backupcontracts.ProtectedFolderStatusDisabled:
		related = append(related, newProtectedFolderRecordAction(record, PortalExecutorProtectedFolderEnable, "Enable Protection", ActionRiskSensitive))
	case backupcontracts.ProtectedFolderStatusAttention:
		related = append(related, newProtectedFolderRecordAction(record, PortalExecutorProtectedFolderRecheck, "Recheck Folder", ActionRiskSafeRun))
		related = append(related, newProtectedFolderRecordAction(record, PortalExecutorProtectedFolderRetry, "Retry Activation", ActionRiskSafeRun))
	default:
		related = append(related, newProtectedFolderRecordAction(record, PortalExecutorProtectedFolderDisable, "Disable Protection", ActionRiskSensitive))
		if record.Lifecycle == backupcontracts.ProtectedFolderStatusWaitingForNode || record.Lifecycle == backupcontracts.ProtectedFolderStatusActivating {
			related = append(related, newProtectedFolderRecordAction(record, PortalExecutorProtectedFolderRetry, "Retry Activation", ActionRiskSafeRun))
		}
	}
	deleteAction := newProtectedFolderRecordAction(record, PortalExecutorProtectedFolderDelete, "Delete Protection Contract", ActionRiskSensitive)
	deleteAction.ConfirmationPolicy = ConfirmationPolicy{
		Required: true,
		Strength: ConfirmationStrengthStrong,
		Prompt:   "Delete only the LOOM protection contract? The source folder and its contents are retained.",
	}
	related = append(related, deleteAction)
	return inspect, related
}

func newProtectedFolderRecordAction(record backupcontracts.ProtectedFolderRecord, kind, label string, risk PortalActionRisk) PortalAction {
	description := "Inspect protected-folder lifecycle and generation evidence."
	switch kind {
	case PortalExecutorProtectedFolderEnable:
		description = "Enable the contract and queue desired state for its owner node."
	case PortalExecutorProtectedFolderDisable:
		description = "Disable the contract and reconcile the owner node; source data is retained."
	case PortalExecutorProtectedFolderRecheck:
		description = "Run a fresh bounded owner-node preflight for this folder."
	case PortalExecutorProtectedFolderRetry:
		description = "Retry desired-state delivery to the contract's owner node."
	case PortalExecutorProtectedFolderDelete:
		description = "Delete the contract and managed watch intent; source data is retained."
	}
	action := PortalAction{
		ID:                  fmt.Sprintf("backup.protected_folder.%s.%s", strings.TrimPrefix(kind, "backup.protected_folder."), record.Key),
		Label:               label,
		Description:         description,
		Domain:              "backup",
		SourceScreen:        ScreenNodes,
		TargetKind:          "protected_folder",
		TargetRef:           record.Key,
		TargetLabel:         firstNonEmpty(record.Contract.DisplayName, filepath.Base(record.Contract.Target.Path), record.Key),
		Risk:                risk,
		State:               ActionAvailable,
		InteractionType:     ActionInteractionDirectRun,
		ExecutionDependency: ExecutionDependencyMain,
		Executor: PortalActionExecutor{Kind: kind, Target: record.Key, Payload: map[string]string{
			"key":  record.Key,
			"node": firstNonEmpty(record.OwnerNodeKey, record.Contract.OwnerNode, record.OwnerNodeID),
			"path": record.Contract.Target.Path,
		}},
		InputValues:   map[string]string{},
		RefreshScreen: ScreenNodes,
	}
	if risk == ActionRiskInspect {
		action.InteractionType = ActionInteractionDirectInspect
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(risk)
	return action
}

func protectedFolderContractKey(displayName, targetPath string) string {
	base := strings.TrimSpace(displayName)
	if base == "" {
		base = filepath.Base(filepath.Clean(targetPath))
	}
	base = protectedFolderKeySeparators.ReplaceAllString(strings.ToLower(base), "-")
	base = strings.Trim(base, "-")
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "protected-folder"
	}
	return base
}

func orderedProtectedFolders(records []backupcontracts.ProtectedFolderRecord) []backupcontracts.ProtectedFolderRecord {
	ordered := append([]backupcontracts.ProtectedFolderRecord(nil), records...)
	rank := map[string]int{
		backupcontracts.ProtectedFolderStatusProtected:      0,
		backupcontracts.ProtectedFolderStatusActive:         0,
		backupcontracts.ProtectedFolderStatusWaitingForNode: 1,
		backupcontracts.ProtectedFolderStatusReady:          1,
		backupcontracts.ProtectedFolderStatusActivating:     1,
		backupcontracts.ProtectedFolderStatusAttention:      2,
		backupcontracts.ProtectedFolderStatusDisabled:       3,
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := rank[ordered[i].Lifecycle], rank[ordered[j].Lifecycle]
		if left != right {
			return left < right
		}
		return ordered[i].Key < ordered[j].Key
	})
	return ordered
}

func protectedFolderBackupMode(label string) string {
	switch strings.TrimSpace(label) {
	case "Metadata Only":
		return backupcontracts.BackupModeMetadataOnly
	case "No Backup":
		return backupcontracts.BackupModeNone
	default:
		return backupcontracts.BackupModeIncrementalRaw
	}
}
