package supportbundle

import (
	"context"

	"loom.local/loom/internal/loomcli/portal"
)

type PortalSummary struct {
	ScreenCount int                   `json:"screen_count"`
	Screens     []PortalScreenSummary `json:"screens"`
	Actions     PortalActionSummary   `json:"actions"`
}

type PortalScreenSummary struct {
	ScreenID      string         `json:"screen_id"`
	Title         string         `json:"title"`
	ActionCount   int            `json:"action_count"`
	ActionsByRisk map[string]int `json:"actions_by_risk,omitempty"`
}

type PortalActionSummary struct {
	Total         int            `json:"total"`
	ByRisk        map[string]int `json:"by_risk"`
	ByInteraction map[string]int `json:"by_interaction"`
	ByScreen      map[string]int `json:"by_screen"`
}

func collectPortal(context.Context, CollectionContext) (CollectorOutput, error) {
	summary := PortalSummary{
		Actions: PortalActionSummary{
			ByRisk:        map[string]int{},
			ByInteraction: map[string]int{},
			ByScreen:      map[string]int{},
		},
	}
	for _, screen := range portal.Screens() {
		state := portal.ScreenStateFromSnapshot(screen.ID, portal.Snapshot{})
		inventory := portal.ActionInventoryForScreen(state)
		screenSummary := PortalScreenSummary{
			ScreenID:      screen.ID,
			Title:         screen.Title,
			ActionCount:   len(inventory),
			ActionsByRisk: map[string]int{},
		}
		for _, action := range inventory {
			risk := string(action.Risk)
			interaction := string(action.Interaction)
			screenSummary.ActionsByRisk[risk]++
			summary.Actions.Total++
			summary.Actions.ByRisk[risk]++
			summary.Actions.ByInteraction[interaction]++
			summary.Actions.ByScreen[screen.ID]++
		}
		summary.Screens = append(summary.Screens, screenSummary)
	}
	summary.ScreenCount = len(summary.Screens)
	return jsonSummary("summaries/portal.json", summary, PrivacyDiagnosticSummary)
}
