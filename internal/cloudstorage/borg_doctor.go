package cloudstorage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type SnapshotBackendStatusInput struct {
	Config    Config
	Runner    BorgCommandRunner
	Now       func() time.Time
	Live      bool
	ForceLive bool
	UseCache  bool
}

type SnapshotBackendStatusReport struct {
	Backend         string            `json:"backend"`
	Status          string            `json:"status"`
	CheckedAt       time.Time         `json:"checked_at"`
	Initialized     bool              `json:"initialized"`
	Repository      string            `json:"repository,omitempty"`
	ArchiveCount    int               `json:"archive_count,omitempty"`
	Checks          map[string]string `json:"checks"`
	Error           string            `json:"error,omitempty"`
	RepairHint      string            `json:"repair_hint,omitempty"`
	CommandSummary  string            `json:"command_summary,omitempty"`
	Cached          bool              `json:"cached,omitempty"`
	CachePath       string            `json:"cache_path,omitempty"`
	CacheAgeSeconds int64             `json:"cache_age_seconds,omitempty"`
	RemoteState     *RemoteState      `json:"remote_state,omitempty"`
}

type SnapshotBackendInitInput struct {
	Config  Config
	Runner  BorgCommandRunner
	Confirm bool
	Now     func() time.Time
}

type SnapshotBackendInitResult struct {
	Backend     string            `json:"backend"`
	Status      string            `json:"status"`
	Initialized bool              `json:"initialized"`
	Repository  string            `json:"repository,omitempty"`
	Checks      map[string]string `json:"checks"`
	Error       string            `json:"error,omitempty"`
}

func SnapshotBackendStatus(ctx context.Context, input SnapshotBackendStatusInput) (SnapshotBackendStatusReport, error) {
	cfg, err := NormalizeConfig(input.Config)
	if err != nil {
		return SnapshotBackendStatusReport{}, err
	}
	now := normalizeNow(input.Now)
	report := SnapshotBackendStatusReport{
		Backend:    cfg.Snapshots.Backend,
		Status:     "ok",
		CheckedAt:  now(),
		Repository: cfg.Snapshots.Borg.Repository,
		Checks:     map[string]string{},
	}
	if cfg.Snapshots.Backend != SnapshotBackendBorg {
		report.Initialized = true
		report.CommandSummary = "legacy tree snapshots use rclone paths and do not require repository initialization"
		return report, nil
	}
	state, stateErr := LoadRemoteState(cfg)
	if stateErr == nil {
		report.RemoteState = &state
	}
	if !input.Live || input.UseCache {
		if cached, ok := readSnapshotBackendStatusCache(cfg, report.CheckedAt); ok {
			return cached, nil
		}
		report.Status = RemoteStateUnknown
		report.Checks["borg_backend_cache"] = "missing"
		report.CachePath = SnapshotBackendStatusCachePath(cfg)
		report.CommandSummary = "No cached Borg backend status is available; run `loom cloud snapshot backend status --live` for an explicit probe."
		return report, nil
	}
	if stateErr == nil && !input.ForceLive && !CanLiveProbe(state, report.CheckedAt) {
		if cached, ok := readSnapshotBackendStatusCache(cfg, report.CheckedAt); ok {
			cached.Status = RemoteStateCoolingDown
			cached.RemoteState = &state
			cached.CommandSummary = "Live Borg backend status skipped while cloud remote is cooling down; showing cached status."
			return cached, nil
		}
		report.Status = RemoteStateCoolingDown
		report.Checks["borg_live_probe"] = RemoteStateCoolingDown
		report.CommandSummary = "Live Borg backend status skipped while cloud remote is cooling down."
		return report, nil
	}
	runner := input.Runner
	if runner.Exec == nil && !runner.DisableRemoteLock && runner.RemoteLockWait == nil {
		runner = NewBorgCommandRunner(cfg)
	} else {
		runner.Config = cfg
	}
	statusLockWait := RemoteLockWait(cfg)
	if runner.RemoteLockWait == nil {
		runner.RemoteLockWait = &statusLockWait
	}
	out, err := runner.Run(ctx, BorgCommand{Args: []string{"list", "--json"}})
	if err != nil {
		if errors.Is(err, ErrRemoteLockBusy) {
			report.Status = "lock_busy"
			report.Checks["borg_list"] = "lock_busy"
			report.Error = err.Error()
			report.RepairHint = "Wait for the active cloud operation to finish, then retry the live backend status."
			return report, nil
		}
		_ = RecordRemoteFailureAt(cfg, err, report.CheckedAt, "cloud.borg_backend.status")
		report.Status = "not_ready"
		report.Checks["borg_list"] = SnapshotStatusFailed
		report.Error = err.Error()
		report.RepairHint = "Run `loom cloud snapshot backend init --confirm` after configuring Borg repository credentials."
		_ = writeSnapshotBackendStatusCache(cfg, report)
		return report, nil
	}
	archives := parseBorgArchiveList(out)
	report.Checks["borg_list"] = SnapshotStatusSucceeded
	report.Initialized = true
	report.ArchiveCount = len(archives)
	_ = RecordRemoteTransportSuccessAt(cfg, report.CheckedAt, "cloud.borg_backend.status")
	if refreshed, loadErr := LoadRemoteState(cfg); loadErr == nil {
		report.RemoteState = &refreshed
	}
	_ = writeSnapshotBackendStatusCache(cfg, report)
	return report, nil
}

func InitializeSnapshotBackend(ctx context.Context, input SnapshotBackendInitInput) (SnapshotBackendInitResult, error) {
	cfg, err := NormalizeConfig(input.Config)
	if err != nil {
		return SnapshotBackendInitResult{}, err
	}
	result := SnapshotBackendInitResult{
		Backend:    cfg.Snapshots.Backend,
		Repository: cfg.Snapshots.Borg.Repository,
		Checks:     map[string]string{},
	}
	if cfg.Snapshots.Backend != SnapshotBackendBorg {
		result.Status = "noop"
		result.Initialized = true
		result.Checks["backend_init"] = "not_required"
		return result, nil
	}
	if !input.Confirm {
		return SnapshotBackendInitResult{}, fmt.Errorf("pass --confirm to initialize the Borg cloud snapshot repository")
	}
	runner := input.Runner
	if runner.Exec == nil && !runner.DisableRemoteLock && runner.RemoteLockWait == nil {
		runner = NewBorgCommandRunner(cfg)
	} else {
		runner.Config = cfg
	}
	status, err := SnapshotBackendStatus(ctx, SnapshotBackendStatusInput{Config: cfg, Runner: runner, Now: input.Now, Live: true, ForceLive: true})
	if err != nil {
		return SnapshotBackendInitResult{}, err
	}
	if status.Initialized {
		result.Status = "already_initialized"
		result.Initialized = true
		result.Checks["borg_list"] = SnapshotStatusSucceeded
		return result, nil
	}
	if _, err := runner.Run(ctx, BorgCommand{Args: []string{"init", "--encryption", cfg.Snapshots.Borg.Encryption}}); err != nil {
		result.Status = SnapshotStatusFailed
		result.Checks["borg_init"] = SnapshotStatusFailed
		result.Error = err.Error()
		return result, nil
	}
	result.Status = SnapshotStatusSucceeded
	result.Initialized = true
	result.Checks["borg_init"] = SnapshotStatusSucceeded
	_ = writeSnapshotBackendStatusCache(cfg, SnapshotBackendStatusReport{
		Backend:     cfg.Snapshots.Backend,
		Status:      "ok",
		CheckedAt:   normalizeNow(input.Now)(),
		Initialized: true,
		Repository:  cfg.Snapshots.Borg.Repository,
		Checks:      map[string]string{"borg_init": SnapshotStatusSucceeded},
	})
	return result, nil
}

func checkBorgSnapshotBackend(ctx context.Context, cfg Config, lookPath func(string) (string, error), live bool) []DoctorFinding {
	if cfg.Snapshots.Backend != SnapshotBackendBorg {
		return []DoctorFinding{okFinding("cloud.snapshot.backend", "Cloud snapshots use the legacy tree backend.", map[string]any{"backend": cfg.Snapshots.Backend})}
	}
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	findings := []DoctorFinding{
		okFinding("cloud.snapshot.backend", "Cloud snapshots use the Borg backend.", map[string]any{"backend": cfg.Snapshots.Backend}),
	}
	if resolved, err := lookPath(cfg.Snapshots.Borg.Binary); err != nil {
		findings = append(findings, DoctorFinding{
			Code:       "cloud.borg.binary",
			Status:     FindingFailed,
			Severity:   SeverityCritical,
			Message:    "Borg binary is not available.",
			Evidence:   map[string]any{"binary": cfg.Snapshots.Borg.Binary},
			RepairHint: "Install BorgBackup on the main node or configure snapshots.borg.binary.",
		})
	} else {
		findings = append(findings, okFinding("cloud.borg.binary", "Borg binary is available.", map[string]any{"path": resolved}))
	}
	if strings.TrimSpace(cfg.Snapshots.Borg.Repository) == "" {
		findings = append(findings, DoctorFinding{
			Code:       "cloud.borg.repository",
			Status:     FindingFailed,
			Severity:   SeverityCritical,
			Message:    "Borg repository is not configured.",
			RepairHint: "Set snapshots.borg.repository to the Hetzner Storage Box Borg repository path.",
		})
	} else {
		findings = append(findings, okFinding("cloud.borg.repository", "Borg repository is configured.", map[string]any{"repository": cfg.Snapshots.Borg.Repository}))
	}
	if strings.TrimSpace(cfg.Snapshots.Borg.PassphraseFile) == "" {
		findings = append(findings, DoctorFinding{
			Code:       "cloud.borg.passphrase_file",
			Status:     FindingFailed,
			Severity:   SeverityCritical,
			Message:    "Borg passphrase file is not configured.",
			RepairHint: "Store the Borg passphrase in a root/service-readable file and set snapshots.borg.passphrase_file.",
		})
	} else if info, err := os.Stat(cfg.Snapshots.Borg.PassphraseFile); err != nil {
		findings = append(findings, DoctorFinding{
			Code:       "cloud.borg.passphrase_file",
			Status:     FindingFailed,
			Severity:   SeverityCritical,
			Message:    "Borg passphrase file is not readable.",
			Evidence:   map[string]any{"path": cfg.Snapshots.Borg.PassphraseFile, "error": err.Error()},
			RepairHint: "Create the passphrase file and make it readable by the LOOM service user.",
		})
	} else if info.IsDir() {
		findings = append(findings, DoctorFinding{
			Code:     "cloud.borg.passphrase_file",
			Status:   FindingFailed,
			Severity: SeverityCritical,
			Message:  "Borg passphrase file path is a directory.",
			Evidence: map[string]any{"path": cfg.Snapshots.Borg.PassphraseFile},
		})
	} else {
		findings = append(findings, okFinding("cloud.borg.passphrase_file", "Borg passphrase file is present.", map[string]any{"path": cfg.Snapshots.Borg.PassphraseFile, "mode": info.Mode().Perm().String()}))
	}
	if !live {
		return findings
	}
	return append(findings, checkBorgSnapshotBackendLive(ctx, cfg)...)
}

func checkBorgSnapshotBackendLive(ctx context.Context, cfg Config) []DoctorFinding {
	if cfg.Snapshots.Backend != SnapshotBackendBorg {
		return nil
	}
	runner := NewBorgCommandRunner(cfg)
	runner.DisableRemoteLock = true
	status, err := SnapshotBackendStatus(ctx, SnapshotBackendStatusInput{Config: cfg, Runner: runner, Live: true})
	if err != nil {
		return []DoctorFinding{{
			Code:       "cloud.borg.status",
			Status:     FindingFailed,
			Severity:   SeverityCritical,
			Message:    "Borg backend status check failed.",
			Evidence:   map[string]any{"error": err.Error()},
			RepairHint: "Check Borg config and credentials.",
		}}
	}
	if status.Initialized {
		return []DoctorFinding{okFinding("cloud.borg.initialized", "Borg repository is initialized and listable.", map[string]any{"archives": status.ArchiveCount})}
	}
	return []DoctorFinding{{
		Code:       "cloud.borg.initialized",
		Status:     FindingFailed,
		Severity:   SeverityCritical,
		Message:    "Borg repository is not initialized or not reachable.",
		Evidence:   map[string]any{"error": status.Error},
		RepairHint: status.RepairHint,
	}}
}

func SnapshotBackendStatusCachePath(cfg Config) string {
	stateDir := strings.TrimSpace(cfg.StateDir)
	if stateDir == "" {
		stateDir = DefaultStateDir
	}
	return filepath.Join(stateDir, "status", "borg_backend.json")
}

func readSnapshotBackendStatusCache(cfg Config, now time.Time) (SnapshotBackendStatusReport, bool) {
	path := SnapshotBackendStatusCachePath(cfg)
	payload, err := os.ReadFile(path)
	if err != nil {
		return SnapshotBackendStatusReport{}, false
	}
	var report SnapshotBackendStatusReport
	if err := json.Unmarshal(payload, &report); err != nil {
		return SnapshotBackendStatusReport{}, false
	}
	report.Cached = true
	report.CachePath = path
	if !report.CheckedAt.IsZero() {
		report.CacheAgeSeconds = int64(now.UTC().Sub(report.CheckedAt.UTC()).Seconds())
		if report.CacheAgeSeconds < 0 {
			report.CacheAgeSeconds = 0
		}
	}
	if state, err := LoadRemoteState(cfg); err == nil {
		report.RemoteState = &state
	}
	return report, true
}

func writeSnapshotBackendStatusCache(cfg Config, report SnapshotBackendStatusReport) error {
	path := SnapshotBackendStatusCachePath(cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	report.Cached = false
	report.CachePath = ""
	report.CacheAgeSeconds = 0
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(payload, '\n'), 0o640)
}
