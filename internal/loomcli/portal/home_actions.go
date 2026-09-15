package portal

func NewAttentionInspectAction(item AttentionItem) PortalAction {
	item = applyAttentionRepairSuggestion(item)
	payload := map[string]string{
		"attention_id":        item.ID,
		"domain":              item.Domain,
		"severity":            item.Severity,
		"title":               item.Title,
		"reason":              item.Reason,
		"suggested_action":    item.Suggestion,
		"failure_class":       item.FailureClass,
		"target_kind":         item.TargetKind,
		"target_ref":          item.TargetRef,
		"inspect_action_id":   item.InspectActionID,
		"repair_action_id":    item.RepairActionID,
		"related_action_id":   item.RelatedActionID,
		"created_at":          timeOrDash(item.CreatedAt),
		"dangerous_repairs":   "not run automatically",
		"confirmation_policy": "repair actions still use their own risk and confirmation rules",
	}
	action := NewPortalRecordInspectAction("home", ScreenHome, "attention", item.ID, firstNonEmpty(item.Title, item.ID), payload)
	action.Label = "Inspect Attention"
	action.Description = "Inspect why this failure needs attention and what to do next."
	action.TargetKind = "attention"
	action.TargetRef = item.ID
	action.TargetLabel = firstNonEmpty(item.Title, item.ID)
	action.RefreshScreen = ScreenHome
	return action
}

func attentionSelectableItems(state ScreenState) []SelectableItem {
	data := state.Data.Home
	items := make([]SelectableItem, 0, len(data.Attention))
	for idx, item := range data.Attention {
		action := NewAttentionInspectAction(item)
		items = append(items, SelectableItem{
			Kind:          SelectableKindRecord,
			Label:         firstNonEmpty(item.Title, item.ID),
			Description:   firstNonEmpty(item.Suggestion, item.Reason),
			Screen:        ScreenHome,
			RowIndex:      idx,
			RecordKind:    "attention",
			RecordRef:     item.ID,
			RecordLabel:   firstNonEmpty(item.Title, item.ID),
			PrimaryAction: actionPtr(action),
		})
	}
	return items
}
