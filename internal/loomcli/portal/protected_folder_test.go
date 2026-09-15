package portal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/loomcli/actions"
	"loom.local/loom/internal/nodes"
)

func TestNodesLoaderIsolatesProtectedFolderFailure(t *testing.T) {
	client := newFakePortalClient()
	client.protectedFolderErr = errors.New("protected-folder endpoint unavailable")
	result := LoadScreen(context.Background(), client, "corr_test", ScreenNodes, Snapshot{})
	if len(result.State.Data.Nodes.Nodes) == 0 || len(result.State.Data.Nodes.WatchedRoots) == 0 {
		t.Fatalf("protected-folder failure erased healthy node data: %#v", result.State.Data.Nodes)
	}
	found := false
	for _, partial := range result.PartialErrors {
		if strings.Contains(partial.Source, "protected_folders") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing isolated protected_folders partial: %#v", result.PartialErrors)
	}
}

func TestProtectFolderWizardStagesPreflightReviewConfirmationMutation(t *testing.T) {
	client := newFakePortalClient()
	client.protectedPreflight = completedProtectedFolderPreflight(false)
	action := NewProtectFolderAction([]nodes.Node{{NodeID: "node_workspace", NodeKey: "workspace", DisplayName: "Studio Mac", Status: "active"}})
	action.SetFieldValue("display_name", "Family Photos")
	action.SetFieldValue("owner_node", "workspace")
	action.SetFieldValue("target_path", "/Users/leonardo/Pictures")

	model := Model{client: client, correlationID: "corr_test", keymap: DefaultKeyMap(), actionPanel: ActionPanelState{Lifecycle: ActionLifecyclePreview, Action: action}}
	next, cmd := model.advanceActionPreview()
	preflighting := next.(Model)
	if preflighting.actionPanel.Lifecycle != ActionLifecyclePreflighting || cmd == nil {
		t.Fatalf("preview did not enter preflighting: %s", preflighting.actionPanel.Lifecycle)
	}
	reviewModel, _ := preflighting.handleActionExecuted(cmd().(actionExecutedMsg))
	review := reviewModel.(Model)
	if review.actionPanel.Lifecycle != ActionLifecycleReview || review.actionPanel.Result.Blocking {
		t.Fatalf("preflight did not produce non-blocking review: %#v", review.actionPanel)
	}
	if review.actionPanel.Action.FieldValueByName("preflight_id") != "bpf_complete" {
		t.Fatalf("durable preflight identity was not preserved: %#v", review.actionPanel.Action.InputValues)
	}
	mode := testMode()
	mode.TTY.Width = 48
	renderedReview := RenderPortalActionResult(mode, review.actionPanel)
	for _, want := range []string{"Policy Files", ".loomignore", "Estimate Truncated", "Ignored: 2 files"} {
		if !strings.Contains(renderedReview, want) {
			t.Fatalf("review missing effective policy evidence %q:\n%s", want, renderedReview)
		}
	}
	for _, line := range strings.Split(renderedReview, "\n") {
		if len(line) > mode.TTY.Width {
			t.Fatalf("review line exceeds narrow terminal width (%d): %q", mode.TTY.Width, line)
		}
	}

	confirmationModel, _ := review.updateActionPanel(tea.KeyMsg{Type: tea.KeyEnter})
	confirmation := confirmationModel.(Model)
	if confirmation.actionPanel.Lifecycle != ActionLifecycleNeedsConfirmation {
		t.Fatalf("review did not advance to confirmation: %s", confirmation.actionPanel.Lifecycle)
	}
	mutationModel, mutationCmd := confirmation.updateActionPanel(tea.KeyMsg{Type: tea.KeyEnter})
	mutation := mutationModel.(Model)
	if mutation.actionPanel.Lifecycle != ActionLifecycleMutation || mutationCmd == nil {
		t.Fatalf("confirmation did not advance to mutation: %s", mutation.actionPanel.Lifecycle)
	}
	resultModel, _ := mutation.handleActionExecuted(mutationCmd().(actionExecutedMsg))
	result := resultModel.(Model)
	if result.actionPanel.Lifecycle != ActionLifecycleSucceeded {
		t.Fatalf("mutation result = %#v", result.actionPanel.Result)
	}
	if client.protectedCreateInput.PreflightID != "bpf_complete" || client.protectedCreateInput.OwnerNode != "workspace" || client.protectedCreateInput.TargetScope != backupcontracts.TargetScopeOwnerNodeAbsolute {
		t.Fatalf("create input lost reviewed evidence or owner routing: %#v", client.protectedCreateInput)
	}
	if client.protectedCreateInput.BackupMode != backupcontracts.BackupModeIncrementalRaw {
		t.Fatalf("backup mode = %q", client.protectedCreateInput.BackupMode)
	}
}

func TestProtectFolderBlockingReviewCannotConfirmAndCanCancel(t *testing.T) {
	client := newFakePortalClient()
	client.protectedPreflight = completedProtectedFolderPreflight(true)
	action := NewProtectFolderAction([]nodes.Node{{NodeKey: "workspace", Status: "active"}})
	action.SetFieldValue("target_path", "/Volumes/Unavailable")
	result := ActionExecutor{Client: client, CorrelationID: "corr_test"}.executeProtectedFolderProtect(context.Background(), action)
	if result.Status != ActionLifecycleReview || !result.Blocking {
		t.Fatalf("blocking preflight projected as %#v", result)
	}
	model := Model{keymap: DefaultKeyMap(), actionPanel: ActionPanelState{Lifecycle: ActionLifecycleReview, Action: action, Result: result}}
	next, _ := model.updateActionPanel(tea.KeyMsg{Type: tea.KeyEnter})
	if got := next.(Model).actionPanel.Lifecycle; got != ActionLifecycleReview {
		t.Fatalf("blocking review advanced to %s", got)
	}
	next, _ = next.(Model).updateActionPanel(tea.KeyMsg{Type: tea.KeyEsc})
	if got := next.(Model).actionPanel.Lifecycle; got != ActionLifecycleCancelled {
		t.Fatalf("review cancellation = %s", got)
	}
}

func TestProtectFolderOfflineSemantics(t *testing.T) {
	client := newFakePortalClient()
	action := NewProtectFolderAction([]nodes.Node{{NodeKey: "workspace", Status: "active"}})
	action.SetFieldValue("target_path", "/Users/leonardo/Documents")
	result, err := ExecutePortalAction(context.Background(), client, "corr_test", action, true, MainAvailability{State: MainAvailabilityOffline})
	if err != nil || result.Status != ActionLifecycleFailed || !strings.Contains(result.ErrorMessage, "Main is offline") {
		t.Fatalf("main-offline result = %#v err=%v", result, err)
	}
	if client.protectedPreflightInput.Path != "" {
		t.Fatal("main-offline action reached backend")
	}

	client.protectedPreflight = backupcontracts.PreflightRecord{PreflightID: "bpf_wait", Status: backupcontracts.PreflightStatusPending, TargetNodeID: "node_workspace", RequestedPath: "/Users/leonardo/Documents"}
	result = ActionExecutor{Client: client, CorrelationID: "corr_test"}.executeProtectedFolderProtect(context.Background(), action)
	if result.Status != ActionLifecycleWaiting || !strings.Contains(result.Summary, "Waiting For Node") {
		t.Fatalf("owner-offline pending request was not durable waiting state: %#v", result)
	}
	action.InputValues["preflight_id"] = result.NextInput["preflight_id"]
	action.InputValues["preflight_ready"] = result.NextInput["preflight_ready"]
	model := Model{client: client, correlationID: "corr_test", keymap: DefaultKeyMap(), actionPanel: ActionPanelState{Lifecycle: ActionLifecycleWaiting, Action: action, Result: result}}
	client.protectedPreflight = completedProtectedFolderPreflight(false)
	next, cmd := model.updateActionPanel(tea.KeyMsg{Type: tea.KeyEnter})
	if got := next.(Model).actionPanel.Lifecycle; got != ActionLifecyclePreflighting || cmd == nil {
		t.Fatalf("waiting record did not resume preflight: %s", got)
	}
	reviewModel, _ := next.(Model).handleActionExecuted(cmd().(actionExecutedMsg))
	if got := reviewModel.(Model).actionPanel.Lifecycle; got != ActionLifecycleReview {
		t.Fatalf("resumed preflight did not return to review: %s", got)
	}
}

func TestNodesProtectedFoldersRenderLifecycleGroupsAndActions(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	records := []backupcontracts.ProtectedFolderRecord{
		protectedFolderFixture("photos", "Photos", backupcontracts.ProtectedFolderStatusProtected, "/Users/leonardo/Pictures", now),
		protectedFolderFixture("drafts", "Drafts", backupcontracts.ProtectedFolderStatusWaitingForNode, "/Users/leonardo/Drafts", time.Time{}),
		protectedFolderFixture("archive", "Archive", backupcontracts.ProtectedFolderStatusAttention, "/Volumes/Archive", time.Time{}),
		protectedFolderFixture("old", "Old Folder", backupcontracts.ProtectedFolderStatusDisabled, "/Users/leonardo/Old", time.Time{}),
	}
	state := ScreenState{Screen: ScreenNodes, Status: ScreenLoadLoaded, Data: ScreenData{Nodes: NodesData{
		Nodes:            []nodes.Node{{NodeID: "node_workspace", NodeKey: "workspace", DisplayName: "Studio Mac", Status: "active", PresenceState: "online"}},
		ProtectedFolders: records,
	}}}
	for _, width := range []int{48, 120} {
		mode := testMode()
		mode.TTY.Width = width
		output := RenderScreenWithState(RenderInput{Mode: mode, Registry: actions.DefaultRegistry(), Screen: ScreenNodes, State: state, Width: width})
		for _, want := range []string{"Protect Folder", "Protected Folders", "Active / Protected", "Waiting", "Attention", "Disabled", "Studio Mac", "/Users/leonardo/Pictures", "last_scan=", "last_backup=", "Active Watched Roots"} {
			if !strings.Contains(output, want) {
				t.Fatalf("width %d missing %q:\n%s", width, want, output)
			}
		}
		if strings.Index(output, "Protected Folders") > strings.Index(output, "Active Watched Roots") {
			t.Fatalf("protected folders rendered after watched roots:\n%s", output)
		}
	}

	items := nodesDefaultSelectableItems(state)
	var disabled SelectableItem
	for _, item := range items {
		if item.RecordKind == "protected_folder" && item.RecordRef == "old" {
			disabled = item
		}
	}
	if disabled.PrimaryAction == nil || findRelatedAction(disabled, PortalExecutorProtectedFolderEnable).ID == "" {
		t.Fatalf("disabled folder actions missing: %#v", disabled)
	}
	deleteAction := findRelatedAction(disabled, PortalExecutorProtectedFolderDelete)
	if deleteAction.ConfirmationPolicy.Strength != ConfirmationStrengthStrong || !strings.Contains(deleteAction.ConfirmationPolicy.Prompt, "source folder") {
		t.Fatalf("delete confirmation does not preserve source-data contract: %#v", deleteAction.ConfirmationPolicy)
	}
}

func TestPortalDeleteRefreshOmitsConvergedContractAndKeepsRetentionPromise(t *testing.T) {
	client := newFakePortalClient()
	record := protectedFolderFixture("old", "Old Folder", backupcontracts.ProtectedFolderStatusDisabled, "/Users/leonardo/Old", time.Time{})
	client.protectedFolders = backupcontracts.ProtectedFolderListResult{
		Folders: []backupcontracts.ProtectedFolderRecord{record},
		Counts:  map[string]int{backupcontracts.ProtectedFolderStatusDisabled: 1},
	}
	state := ScreenState{Screen: ScreenNodes, Status: ScreenLoadLoaded, Data: ScreenData{Nodes: NodesData{ProtectedFolders: client.protectedFolders.Folders}}}
	items := nodesDefaultSelectableItems(state)
	var selected SelectableItem
	for _, item := range items {
		if item.RecordKind == "protected_folder" && item.RecordRef == "old" {
			selected = item
			break
		}
	}
	if selected.RecordRef == "" {
		t.Fatalf("protected-folder selection fixture is empty: %#v", items)
	}
	deleteAction := findRelatedAction(selected, PortalExecutorProtectedFolderDelete)
	result := ActionExecutor{Client: client, CorrelationID: "corr_test"}.executeProtectedFolderDelete(context.Background(), deleteAction)
	if result.Status != ActionLifecycleSucceeded || result.RefreshScreen != ScreenNodes || !strings.Contains(result.Summary, "Source data was retained") {
		t.Fatalf("delete action result = %#v", result)
	}
	loaded := LoadScreen(context.Background(), client, "corr_test", ScreenNodes, Snapshot{})
	for _, visible := range loaded.State.Data.Nodes.ProtectedFolders {
		if visible.Key == "old" {
			t.Fatalf("converged deleted contract remained in Portal refresh: %#v", loaded.State.Data.Nodes.ProtectedFolders)
		}
	}
}

func completedProtectedFolderPreflight(blocking bool) backupcontracts.PreflightRecord {
	record := backupcontracts.PreflightRecord{
		PreflightID: "bpf_complete", TargetNodeID: "node_workspace", RequestedPath: "/Users/leonardo/Pictures", CanonicalPath: "/Users/leonardo/Pictures", Status: backupcontracts.PreflightStatusCompleted,
		Result: &backupcontracts.PreflightResult{RequestedPath: "/Users/leonardo/Pictures", CanonicalPath: "/Users/leonardo/Pictures", Exists: true, Directory: true, Readable: true, FileCount: 42, DirectoryCount: 3, ApparentBytes: 1024, Policy: backupcontracts.PreflightPolicyEvidence{Profile: "managed", PolicyFiles: []string{".loomignore", "nested/.loomignore"}, IncludedCount: 40, IncludedBytes: 900, IgnoredCount: 2, IgnoredBytes: 124}},
	}
	if blocking {
		record.Result.Findings = []backupcontracts.PreflightFinding{{Code: "path.unreadable", Severity: "error", Message: "Folder is not readable.", Blocking: true}}
	}
	return record
}

func protectedFolderFixture(key, name, lifecycle, path string, at time.Time) backupcontracts.ProtectedFolderRecord {
	record := backupcontracts.ProtectedFolderRecord{
		Key: key, Lifecycle: lifecycle, Status: lifecycle, OwnerNodeID: "node_workspace", OwnerNodeKey: "workspace",
		Contract: backupcontracts.Contract{Key: key, DisplayName: name, OwnerNode: "workspace", Target: backupcontracts.TargetSpec{Scope: backupcontracts.TargetScopeOwnerNodeAbsolute, Path: path}},
	}
	if !at.IsZero() {
		record.LastBackupAcceptedAt = &at
		record.Details, _ = json.Marshal(map[string]any{"root_reported_at": at, "latest_backup_at": at, "latest_backup_status": "accepted"})
	}
	if lifecycle == backupcontracts.ProtectedFolderStatusAttention {
		record.AttentionReason = "owner-node report does not match the current desired configuration"
	}
	return record
}
