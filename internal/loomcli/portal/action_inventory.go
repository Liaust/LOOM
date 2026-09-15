package portal

type PortalActionInventoryItem struct {
	Surface              string
	ActionID             string
	Label                string
	Domain               string
	TargetKind           string
	TargetRef            string
	Interaction          PortalActionInteraction
	Risk                 PortalActionRisk
	ExecutorKind         string
	ExecutionDependency  ExecutionDependency
	RequiresInput        bool
	RequiresConfirmation bool
	Available            bool
	DisabledReason       string
}

func ActionInventoryForScreen(state ScreenState) []PortalActionInventoryItem {
	state.Screen = NormalizeScreen(state.Screen)
	actions := ScreenActions(state)
	items := make([]PortalActionInventoryItem, 0, len(actions))
	for _, action := range actions {
		items = append(items, PortalActionInventoryItem{
			Surface:              state.Screen,
			ActionID:             action.ID,
			Label:                action.Label,
			Domain:               action.Domain,
			TargetKind:           action.TargetKind,
			TargetRef:            action.TargetRef,
			Interaction:          action.Interaction(),
			Risk:                 action.Risk,
			ExecutorKind:         action.Executor.Kind,
			ExecutionDependency:  action.ExecutionDependency,
			RequiresInput:        len(action.InputFields) > 0,
			RequiresConfirmation: action.RequiresConfirmation(),
			Available:            !action.Disabled(),
			DisabledReason:       action.DisabledReason,
		})
	}
	return items
}
