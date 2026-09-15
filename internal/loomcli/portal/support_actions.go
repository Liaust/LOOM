package portal

const supportBundleDefaultOutputPath = ".loom-acceptance/support/loom-support.tar.gz"

func NewSupportBundleCommandHintAction(sourceScreen string) PortalAction {
	sourceScreen = NormalizeScreen(sourceScreen)
	command := []string{"loom", "support", "bundle", "create", "--output", supportBundleDefaultOutputPath}
	action := PortalAction{
		ID:              "support.bundle.create_hint",
		Label:           "Create Support Bundle",
		Description:     "Show the CLI command for a redacted diagnostic bundle for handoff.",
		Domain:          "support",
		SourceScreen:    sourceScreen,
		TargetKind:      "support_bundle",
		TargetRef:       "default",
		TargetLabel:     "Default support bundle",
		Risk:            ActionRiskInspect,
		State:           ActionAvailable,
		InteractionType: ActionInteractionDirectInspect,
		Executor: PortalActionExecutor{
			Kind:   PortalExecutorRecordInspect,
			Target: "support.bundle.default",
			Payload: map[string]string{
				"command":     "loom support bundle create --output " + supportBundleDefaultOutputPath,
				"output":      supportBundleDefaultOutputPath,
				"profile":     "default",
				"logs":        "not included by default",
				"live_probes": "not included by default",
				"handoff":     "review the archive before sharing it",
			},
		},
		RawCommand:    command,
		RefreshScreen: sourceScreen,
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}
