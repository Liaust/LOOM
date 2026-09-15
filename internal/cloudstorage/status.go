package cloudstorage

import (
	"context"
	"errors"
	"time"
)

type StatusInput struct {
	ConfigPath string
	Driver     Driver
	Now        func() time.Time
	Mode       StatusMode
	ForceLive  bool
}

type StatusMode string

const (
	StatusModeCached StatusMode = "cached"
	StatusModeLive   StatusMode = "live"
	StatusModeAuto   StatusMode = "auto"
)

type StatusReport struct {
	SchemaVersion string                       `json:"schema_version"`
	Status        string                       `json:"status"`
	Mode          string                       `json:"mode"`
	CheckedAt     time.Time                    `json:"checked_at"`
	Config        ConfigSummary                `json:"config"`
	Remote        *RemoteStatus                `json:"remote,omitempty"`
	Roots         []RootStatus                 `json:"roots,omitempty"`
	RemoteState   *RemoteState                 `json:"remote_state,omitempty"`
	LockPath      string                       `json:"lock_path,omitempty"`
	SnapshotStore *SnapshotBackendStatusReport `json:"snapshot_store,omitempty"`
}

type ConfigSummary struct {
	Path                           string `json:"path"`
	Exists                         bool   `json:"exists"`
	Enabled                        bool   `json:"enabled"`
	Provider                       string `json:"provider"`
	Driver                         string `json:"driver"`
	RemoteName                     string `json:"remote_name"`
	RemoteRoot                     string `json:"remote_root"`
	RcloneConfigPath               string `json:"rclone_config_path"`
	StateDir                       string `json:"state_dir"`
	RemoteLockPath                 string `json:"remote_lock_path"`
	RemoteLockWaitSeconds          int    `json:"remote_lock_wait_seconds"`
	RemoteLockWorkerWaitSeconds    int    `json:"remote_lock_worker_wait_seconds"`
	RemoteLockEffectfulWaitSeconds int    `json:"remote_lock_effectful_wait_seconds"`
	SnapshotBackend                string `json:"snapshot_backend"`
	BorgCompression                string `json:"borg_compression,omitempty"`
	BorgCheckMode                  string `json:"borg_check_mode,omitempty"`
	BorgCheckIntervalHours         int    `json:"borg_check_interval_hours,omitempty"`
	BorgInventoryCacheTTLSeconds   int    `json:"borg_inventory_cache_ttl_seconds,omitempty"`
	MainSnapshotsRoot              string `json:"main_snapshots_root"`
	FullOffloadRoot                string `json:"full_offload_root"`
	CloudFolderRoot                string `json:"cloud_folder_root"`
}

func Status(ctx context.Context, input StatusInput) (StatusReport, error) {
	now := time.Now
	if input.Now != nil {
		now = input.Now
	}
	load, err := LoadConfig(input.ConfigPath)
	if err != nil {
		return StatusReport{}, err
	}
	report := StatusReport{
		SchemaVersion: ConfigSchemaVersion,
		Status:        "disabled",
		Mode:          string(statusMode(input.Mode)),
		CheckedAt:     now().UTC(),
		Config:        summarizeConfig(load),
		LockPath:      RemoteLockPath(load.Config),
	}
	if !load.Config.Enabled {
		state := defaultRemoteState(load.Config, report.CheckedAt)
		state.State = RemoteStateDisabled
		report.RemoteState = &state
		return report, nil
	}
	state, stateErr := LoadRemoteState(load.Config)
	if stateErr == nil {
		report.RemoteState = &state
	}
	mode := statusMode(input.Mode)
	if mode == StatusModeAuto {
		if stateErr == nil && CanLiveProbe(state, report.CheckedAt) {
			mode = StatusModeLive
		} else {
			mode = StatusModeCached
		}
		report.Mode = string(mode)
	}
	if mode != StatusModeLive {
		return applyCachedRemoteState(report, load.Config, state, stateErr), nil
	}
	if stateErr == nil && !input.ForceLive && !CanLiveProbe(state, report.CheckedAt) {
		state.LiveProbeAllowed = false
		state.LiveProbeSkipReason = RemoteStateCoolingDown
		report.Status = RemoteStateCoolingDown
		report.RemoteState = &state
		report.Roots = state.LastRoots
		report.Remote = remoteStatusFromState(state)
		return report, nil
	}
	lockErr := WithRemoteLock(ctx, load.Config, RemoteLockOptions{Operation: "cloud.status.live", Wait: RemoteLockWait(load.Config)}, func(ctx context.Context) error {
		driver := input.Driver
		if driver == nil {
			d := NewRcloneDriver(load.Config)
			d.DisableRemoteLock = true
			driver = d
		} else {
			driver = driverWithoutRemoteLock(driver)
		}
		probe, err := ProbeRemote(ctx, driver, "")
		if err != nil {
			_ = RecordRemoteFailureAt(load.Config, err, report.CheckedAt, "cloud.status.live")
			if refreshed, loadErr := LoadRemoteState(load.Config); loadErr == nil {
				report.RemoteState = &refreshed
			}
			report.Status = "unreachable"
			report.Remote = &probe.Status
			report.Roots = rootStatuses(load.Config, nil)
			return nil
		}
		report.Status = "reachable"
		report.Remote = &probe.Status
		report.Roots = rootStatuses(load.Config, probe.Entries)
		_ = RecordRemoteSuccessAt(load.Config, probe, report.CheckedAt, "cloud.status.live")
		if refreshed, loadErr := LoadRemoteState(load.Config); loadErr == nil {
			report.RemoteState = &refreshed
		}
		return nil
	})
	if lockErr != nil {
		if errors.Is(lockErr, ErrRemoteLockBusy) {
			report.Status = "lock_busy"
			if report.RemoteState != nil {
				state := *report.RemoteState
				state.LiveProbeAllowed = false
				state.LiveProbeSkipReason = "remote_lock_busy"
				report.RemoteState = &state
				report.Remote = remoteStatusFromState(state)
			}
			return report, nil
		}
		return StatusReport{}, lockErr
	}
	return report, nil
}

func statusMode(mode StatusMode) StatusMode {
	switch mode {
	case StatusModeLive, StatusModeAuto:
		return mode
	default:
		return StatusModeCached
	}
}

func applyCachedRemoteState(report StatusReport, cfg Config, state RemoteState, stateErr error) StatusReport {
	if stateErr != nil {
		report.Status = RemoteStateUnknown
		report.Remote = &RemoteStatus{Reachable: false, RemoteURI: cfg.RemoteURI(""), CheckedAt: report.CheckedAt}
		return report
	}
	report.Status = state.State
	report.Roots = state.LastRoots
	report.Remote = remoteStatusFromState(state)
	return report
}

func remoteStatusFromState(state RemoteState) *RemoteStatus {
	checkedAt := state.UpdatedAt
	if state.LastLiveCheckAt != nil {
		checkedAt = *state.LastLiveCheckAt
	}
	return &RemoteStatus{
		Reachable: state.State == RemoteStateHealthy,
		RemoteURI: state.RemoteURI,
		CheckedAt: checkedAt,
		Entries:   state.LastRemoteEntries,
	}
}

func summarizeConfig(load LoadResult) ConfigSummary {
	cfg := load.Config
	return ConfigSummary{
		Path:                           load.Path,
		Exists:                         load.Exists,
		Enabled:                        cfg.Enabled,
		Provider:                       cfg.Provider,
		Driver:                         cfg.Driver,
		RemoteName:                     cfg.RemoteName,
		RemoteRoot:                     cfg.RemoteRoot,
		RcloneConfigPath:               cfg.RcloneConfigPath,
		StateDir:                       cfg.StateDir,
		RemoteLockPath:                 RemoteLockPath(cfg),
		RemoteLockWaitSeconds:          cfg.RemoteLockWaitSeconds,
		RemoteLockWorkerWaitSeconds:    cfg.RemoteLockWorkerWaitSeconds,
		RemoteLockEffectfulWaitSeconds: cfg.RemoteLockEffectfulWaitSeconds,
		SnapshotBackend:                cfg.Snapshots.Backend,
		BorgCompression:                cfg.Snapshots.Borg.Compression,
		BorgCheckMode:                  cfg.Snapshots.Borg.CheckMode,
		BorgCheckIntervalHours:         cfg.Snapshots.Borg.CheckIntervalHours,
		BorgInventoryCacheTTLSeconds:   cfg.Snapshots.Borg.InventoryCacheTTLSeconds,
		MainSnapshotsRoot:              cfg.Roots.MainSnapshots,
		FullOffloadRoot:                cfg.Roots.FullOffload,
		CloudFolderRoot:                cfg.Roots.CloudFolder,
	}
}

func rootStatuses(cfg Config, entries []RemoteEntry) []RootStatus {
	present := map[string]bool{}
	for _, entry := range entries {
		present[entry.Path] = true
	}
	out := []RootStatus{}
	for _, root := range cfg.Roots.Required() {
		status := RootStatus{
			Name:      root.Name,
			Prefix:    root.Prefix,
			RemoteURI: cfg.RemoteURI(root.Prefix),
			Exists:    present[root.Prefix],
			Status:    "missing",
		}
		if status.Exists {
			status.Status = "present"
		}
		out = append(out, status)
	}
	return out
}
