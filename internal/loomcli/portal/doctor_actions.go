package portal

import (
	"strings"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/cloudstorage"
	"loom.local/loom/internal/loomcli/actions"
	"loom.local/loom/internal/search"
	"loom.local/loom/internal/storagecatalog"
)

func NewDoctorFindingInspectAction(finding DoctorFinding) PortalAction {
	payload := map[string]string{
		"finding_id":       finding.ID,
		"area":             finding.Area,
		"severity":         finding.Severity,
		"title":            finding.Title,
		"explanation":      finding.Explanation,
		"impact":           finding.Impact,
		"safe_next_action": finding.SafeNextAction,
		"target_kind":      finding.TargetKind,
		"target_ref":       finding.TargetRef,
		"source_kind":      finding.SourceKind,
		"source_id":        finding.SourceID,
		"observed_at":      timeOrDash(finding.ObservedAt),
	}
	action := NewPortalRecordInspectAction("doctor", ScreenDoctor, "doctor_finding", finding.ID, firstNonEmpty(finding.Title, finding.ID), payload)
	action.Label = "Inspect Finding"
	action.Description = "Inspect why this issue matters and what safe next action Doctor recommends."
	action.TargetKind = "doctor_finding"
	action.TargetRef = finding.ID
	action.TargetLabel = firstNonEmpty(finding.Title, finding.ID)
	action.RefreshScreen = ScreenDoctor
	return action
}

func doctorSelectableItems(state ScreenState) []SelectableItem {
	data := state.Data.Doctor
	items := make([]SelectableItem, 0, len(data.Findings))
	for idx, finding := range data.Findings {
		action := NewDoctorFindingInspectAction(finding)
		related := doctorRelatedActionsForFinding(finding, state.RawDetails)
		items = append(items, SelectableItem{
			Kind:           SelectableKindRecord,
			Label:          firstNonEmpty(finding.Title, finding.ID),
			Description:    firstNonEmpty(finding.SafeNextAction, finding.Explanation),
			Screen:         ScreenDoctor,
			RowIndex:       idx,
			RecordKind:     "doctor_finding",
			RecordRef:      finding.ID,
			RecordLabel:    firstNonEmpty(finding.Title, finding.ID),
			PrimaryAction:  actionPtr(action),
			RelatedActions: related,
		})
	}
	return items
}

func doctorInspectActionID(item AttentionItem, area string) string {
	if strings.TrimSpace(item.InspectActionID) != "" {
		return item.InspectActionID
	}
	if item.TargetKind == DoctorTargetKindScreen && strings.TrimSpace(item.TargetRef) != "" {
		if action, ok := doctorActionForID(NormalizeScreen(item.TargetRef) + ".open"); ok {
			return action.ID
		}
	}
	if action, ok := doctorActionForID(doctorAreaTargetScreen(area) + ".open"); ok {
		return action.ID
	}
	return ""
}

func doctorRepairActionIDs(repairID string) (safeRepairID string, dangerousRepairID string) {
	action, ok := doctorActionForID(repairID)
	if !ok {
		return "", ""
	}
	if doctorActionAllowedInDefault(action) {
		return action.ID, ""
	}
	return "", action.ID
}

func doctorRelatedActionsForFinding(finding DoctorFinding, includeDangerous bool) []PortalAction {
	actions := []PortalAction{}
	add := func(actionID string) {
		action, ok := doctorActionForID(actionID)
		if !ok {
			return
		}
		if !includeDangerous && !doctorActionAllowedInDefault(action) {
			return
		}
		actions = append(actions, action)
	}
	add(finding.InspectActionID)
	add(finding.SafeRepairActionID)
	if includeDangerous {
		add(finding.DangerousRepairActionID)
	}
	return actions
}

func doctorActionAllowedInDefault(action PortalAction) bool {
	if action.Disabled() {
		return false
	}
	switch action.Risk {
	case ActionRiskInspect, ActionRiskSafeRun, ActionRiskSensitive:
		return true
	default:
		return false
	}
}

func doctorActionForID(actionID string) (PortalAction, bool) {
	actionID = strings.TrimSpace(actionID)
	if actionID == "" {
		return PortalAction{}, false
	}
	if action, ok := actions.DefaultRegistry().Get(actionID); ok {
		return PortalActionFromRegistry(action), true
	}
	switch actionID {
	case "cloud.status.live":
		return NewCloudStatusLiveAction(cloudstorage.StatusReport{}, ScreenDoctor), true
	case "storage.mount.helper.inspect":
		return NewStorageMountHelperAction(), true
	case "storage.retention.status":
		return NewStorageRetentionStatusAction(storagecatalog.RetentionStatus{}), true
	case "index.retry_failed":
		return NewIndexRetryFailedAction(search.IndexRetryFailedInput{Limit: 50}), true
	case "box.watch_policy.apply":
		return NewBoxWatchApplyAction(box.Status{State: "ok"}), true
	default:
		return PortalAction{}, false
	}
}
