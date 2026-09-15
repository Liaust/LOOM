package portal

import (
	"fmt"

	"loom.local/loom/internal/cloudstorage"
)

func NewCloudStatusLiveAction(status cloudstorage.StatusReport, sourceScreen string) PortalAction {
	action := PortalAction{
		ID:           "cloud.status.live",
		Label:        "Refresh Main Cloud Status",
		Description:  "Ask the main daemon for cloud status. This can open a Storage Box connection.",
		Domain:       "maintenance",
		SourceScreen: NormalizeScreen(sourceScreen),
		TargetKind:   "cloud_status",
		TargetRef:    "cloud.status",
		TargetLabel:  "Cloud status",
		Risk:         ActionRiskSafeRun,
		State:        ActionAvailable,
		InputValues:  map[string]string{},
		Executor: PortalActionExecutor{
			Kind:   PortalExecutorCloudStatusLive,
			Target: "cloud.status",
			Payload: map[string]string{
				"config_path": cloudstorage.DefaultConfigPath,
			},
		},
		RawCommand:    []string{"loom", "cloud", "status", "--live"},
		RawDetails:    cloudStatusPayload(status),
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func NewCloudCooldownInspectAction(status cloudstorage.StatusReport, sourceScreen string) PortalAction {
	action := PortalAction{
		ID:            "cloud.cooldown.inspect",
		Label:         "Inspect Cloud Cooldown",
		Description:   "Inspect cached cloud state, last failure, and next allowed live probe.",
		Domain:        "maintenance",
		SourceScreen:  NormalizeScreen(sourceScreen),
		TargetKind:    "cloud_remote_state",
		TargetRef:     "cloud.remote_state",
		TargetLabel:   "Cloud remote state",
		Risk:          ActionRiskInspect,
		State:         ActionAvailable,
		InputValues:   map[string]string{},
		Executor:      PortalActionExecutor{Kind: PortalExecutorCloudCooldownInspect, Target: "cloud.remote_state", Payload: cloudStatusPayload(status)},
		RawCommand:    []string{"loom", "cloud", "status", "--cached"},
		RawDetails:    cloudStatusPayload(status),
		RefreshScreen: NormalizeScreen(sourceScreen),
	}
	action.ConfirmationPolicy = confirmationPolicyForRisk(action.Risk)
	return action
}

func cloudStatusPayload(status cloudstorage.StatusReport) map[string]string {
	payload := map[string]string{
		"status":                 firstNonEmpty(status.Status, "-"),
		"mode":                   firstNonEmpty(status.Mode, "cached"),
		"enabled":                fmt.Sprintf("%t", status.Config.Enabled),
		"provider":               firstNonEmpty(status.Config.Provider, "-"),
		"driver":                 firstNonEmpty(status.Config.Driver, "-"),
		"remote":                 firstNonEmpty(status.Config.RemoteName, "-") + ":" + firstNonEmpty(status.Config.RemoteRoot, "-"),
		"snapshot_backend":       firstNonEmpty(status.Config.SnapshotBackend, "-"),
		"config_path":            firstNonEmpty(status.Config.Path, cloudstorage.DefaultConfigPath),
		"remote_lock_path":       firstNonEmpty(status.LockPath, status.Config.RemoteLockPath, "-"),
		"checked_at":             timeOrDash(status.CheckedAt),
		"main_snapshots_root":    firstNonEmpty(status.Config.MainSnapshotsRoot, "-"),
		"full_offload_root":      firstNonEmpty(status.Config.FullOffloadRoot, "-"),
		"cloud_folder_root":      firstNonEmpty(status.Config.CloudFolderRoot, "-"),
		"live_probe_instruction": "Run `loom cloud status --live` only when an operator explicitly wants a live Storage Box check.",
	}
	if status.RemoteState != nil {
		payload["remote_state"] = firstNonEmpty(status.RemoteState.State, "-")
		payload["last_success"] = timePtrOrDash(status.RemoteState.LastSuccessAt)
		payload["last_failure"] = timePtrOrDash(status.RemoteState.LastFailureAt)
		payload["next_live_check"] = timePtrOrDash(status.RemoteState.NextLiveCheckAfter)
		payload["last_error_class"] = firstNonEmpty(status.RemoteState.LastErrorClass, "-")
		payload["live_probe_allowed"] = fmt.Sprintf("%t", status.RemoteState.LiveProbeAllowed)
		payload["live_probe_skip_reason"] = firstNonEmpty(status.RemoteState.LiveProbeSkipReason, "-")
	}
	if status.SnapshotStore != nil {
		payload["snapshot_store_status"] = firstNonEmpty(status.SnapshotStore.Status, "-")
		payload["snapshot_store_initialized"] = fmt.Sprintf("%t", status.SnapshotStore.Initialized)
		payload["snapshot_store_repository"] = firstNonEmpty(status.SnapshotStore.Repository, "-")
	}
	return payload
}
