package cloudstorage

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	loomconfig "loom.local/loom/internal/config"
)

const (
	FindingOK      = "ok"
	FindingWarning = "warning"
	FindingFailed  = "failed"

	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

type DoctorInput struct {
	ConfigPath    string
	RuntimeConfig loomconfig.Config
	Driver        Driver
	LookPath      func(string) (string, error)
	Now           func() time.Time
	Live          bool
	ForceLive     bool
	CheckBorg     bool
	CheckRclone   bool
}

type DoctorReport struct {
	SchemaVersion string          `json:"schema_version"`
	Status        string          `json:"status"`
	CheckedAt     time.Time       `json:"checked_at"`
	Config        ConfigSummary   `json:"config"`
	Remote        *RemoteStatus   `json:"remote,omitempty"`
	Roots         []RootStatus    `json:"roots,omitempty"`
	RemoteState   *RemoteState    `json:"remote_state,omitempty"`
	Findings      []DoctorFinding `json:"findings"`
}

type DoctorFinding struct {
	Code       string         `json:"code"`
	Status     string         `json:"status"`
	Severity   string         `json:"severity"`
	Message    string         `json:"message"`
	Evidence   map[string]any `json:"evidence,omitempty"`
	RepairHint string         `json:"repair_hint,omitempty"`
}

func Doctor(ctx context.Context, input DoctorInput) (DoctorReport, error) {
	now := time.Now
	if input.Now != nil {
		now = input.Now
	}
	load, err := LoadConfig(input.ConfigPath)
	if err != nil {
		return DoctorReport{}, err
	}
	report := DoctorReport{
		SchemaVersion: ConfigSchemaVersion,
		Status:        "disabled",
		CheckedAt:     now().UTC(),
		Config:        summarizeConfig(load),
	}
	if !load.Exists {
		report.Findings = append(report.Findings, infoFinding("cloud.config_missing", "Cloud config file is not present; cloud storage is disabled until configured.", map[string]any{"path": load.Path}))
	}
	if !load.Config.Enabled {
		report.Findings = append(report.Findings, infoFinding("cloud.disabled", "Cloud storage is disabled."))
		report.Status = summarizeFindings(report.Findings, "disabled")
		return report, nil
	}

	cfg := load.Config
	report.Findings = append(report.Findings, checkMainNode(input.RuntimeConfig))
	report.Findings = append(report.Findings, checkCloudPathIsolation(cfg, input.RuntimeConfig)...)
	report.Findings = append(report.Findings, checkStateDir(cfg.StateDir))
	report.Findings = append(report.Findings, checkRcloneBinary(cfg, input.LookPath))
	report.Findings = append(report.Findings, checkRcloneConfig(cfg.RcloneConfigPath))
	shouldCheckBorg, shouldCheckRclone := doctorLiveChecks(input)
	report.Findings = append(report.Findings, checkBorgSnapshotBackend(ctx, cfg, input.LookPath, false)...)

	state, stateErr := LoadRemoteState(cfg)
	if stateErr == nil {
		report.RemoteState = &state
	}
	if !input.Live && !input.CheckBorg && !input.CheckRclone {
		report.Status = summarizeFindings(report.Findings, "ok")
		return report, nil
	}
	if stateErr == nil && !input.ForceLive && !CanLiveProbe(state, report.CheckedAt) {
		report.Remote = remoteStatusFromState(state)
		report.Roots = state.LastRoots
		report.Findings = append(report.Findings, DoctorFinding{
			Code:       "cloud.remote.cooldown",
			Status:     FindingWarning,
			Severity:   SeverityWarning,
			Message:    "Cloud remote live checks are cooling down after a transport failure.",
			Evidence:   remoteCooldownEvidence(state),
			RepairHint: "Wait until the next live check time or use --force-live for one explicit operator probe.",
		})
		report.Status = summarizeFindings(report.Findings, "ok")
		return report, nil
	}
	if !shouldCheckRclone && !shouldCheckBorg {
		report.Status = summarizeFindings(report.Findings, "ok")
		return report, nil
	}
	var roots []RootStatus
	lockErr := WithRemoteLock(ctx, cfg, RemoteLockOptions{Operation: "cloud.doctor.live", Wait: RemoteLockWait(cfg)}, func(ctx context.Context) error {
		if shouldCheckBorg {
			report.Findings = append(report.Findings, checkBorgSnapshotBackendLive(ctx, cfg)...)
		}
		if !shouldCheckRclone {
			return nil
		}
		driver := input.Driver
		if driver == nil {
			d := NewRcloneDriver(cfg)
			d.DisableRemoteLock = true
			driver = d
		} else {
			driver = driverWithoutRemoteLock(driver)
		}
		remote, checkedRoots, remoteFinding := checkRemote(ctx, cfg, driver)
		roots = checkedRoots
		report.Remote = &remote
		report.Roots = roots
		report.Findings = append(report.Findings, remoteFinding)
		if remoteFinding.Status == FindingOK {
			entries := remoteEntriesFromRoots(roots)
			_ = RecordRemoteSuccessAt(cfg, RemoteProbeResult{Status: remote, Entries: entries}, report.CheckedAt, "cloud.doctor.live")
		} else if errValue, ok := remoteFinding.Evidence["error"].(string); ok && errValue != "" {
			_ = RecordRemoteFailureAt(cfg, errors.New(errValue), report.CheckedAt, "cloud.doctor.live")
		}
		return nil
	})
	if lockErr != nil {
		if errors.Is(lockErr, ErrRemoteLockBusy) {
			report.Findings = append(report.Findings, remoteLockBusyFinding(cfg, lockErr))
			report.Status = summarizeFindings(report.Findings, "ok")
			return report, nil
		}
		return DoctorReport{}, lockErr
	}
	for _, root := range roots {
		if root.Exists {
			report.Findings = append(report.Findings, okFinding("cloud.root."+root.Name, "Remote cloud root is present.", map[string]any{"remote_uri": root.RemoteURI}))
		} else {
			report.Findings = append(report.Findings, DoctorFinding{
				Code:       "cloud.root." + root.Name,
				Status:     FindingWarning,
				Severity:   SeverityWarning,
				Message:    "Remote cloud root is missing; this doctor command does not create remote directories.",
				Evidence:   map[string]any{"remote_uri": root.RemoteURI},
				RepairHint: "Create the configured cloud roots during cloud provisioning before enabling uploads.",
			})
		}
	}
	report.Status = summarizeFindings(report.Findings, "ok")
	return report, nil
}

func remoteLockBusyFinding(cfg Config, err error) DoctorFinding {
	return DoctorFinding{
		Code:       "cloud.remote.lock_busy",
		Status:     FindingWarning,
		Severity:   SeverityWarning,
		Message:    "Cloud remote live checks were skipped because another LOOM cloud operation holds the Storage Box lock.",
		Evidence:   map[string]any{"lock_path": RemoteLockPath(cfg), "error": err.Error()},
		RepairHint: "Wait for the active cloud operation to finish, then retry the live check.",
	}
}

func doctorLiveChecks(input DoctorInput) (checkBorg bool, checkRclone bool) {
	if input.CheckBorg || input.CheckRclone {
		return input.CheckBorg, input.CheckRclone
	}
	if input.Live {
		return true, true
	}
	return false, false
}

func remoteCooldownEvidence(state RemoteState) map[string]any {
	evidence := map[string]any{
		"state":            state.State,
		"failure_count":    state.FailureCount,
		"last_error_class": state.LastErrorClass,
	}
	if state.LastFailureAt != nil {
		evidence["last_failure_at"] = state.LastFailureAt.Format(time.RFC3339)
	}
	if state.NextLiveCheckAfter != nil {
		evidence["next_live_check_after"] = state.NextLiveCheckAfter.Format(time.RFC3339)
	}
	return evidence
}

func remoteEntriesFromRoots(roots []RootStatus) []RemoteEntry {
	entries := make([]RemoteEntry, 0, len(roots))
	for _, root := range roots {
		if !root.Exists {
			continue
		}
		entries = append(entries, RemoteEntry{Path: root.Prefix, IsDir: true})
	}
	return entries
}

func checkMainNode(cfg loomconfig.Config) DoctorFinding {
	if cfg.NodeKind == "main" && cfg.NodeRole == "main" {
		return okFinding("cloud.node.main", "Cloud managed operations are running on a main node.", map[string]any{"node_id": cfg.NodeID})
	}
	return DoctorFinding{
		Code:       "cloud.node.main",
		Status:     FindingFailed,
		Severity:   SeverityCritical,
		Message:    "Managed cloud operations must run from the main node.",
		Evidence:   map[string]any{"node_id": cfg.NodeID, "node_kind": cfg.NodeKind, "node_role": cfg.NodeRole},
		RepairHint: "Run cloud snapshot/offload commands on loom-main, not on a workspace node.",
	}
}

func checkCloudPathIsolation(cfg Config, runtime loomconfig.Config) []DoctorFinding {
	findings := []DoctorFinding{}
	for field, value := range map[string]string{
		"remote_root":    cfg.RemoteRoot,
		"main_snapshots": cfg.Roots.MainSnapshots,
		"full_offload":   cfg.Roots.FullOffload,
		"cloud_folder":   cfg.Roots.CloudFolder,
	} {
		if err := ValidateRemotePrefix(field, value); err != nil {
			findings = append(findings, DoctorFinding{
				Code:       "cloud.path." + field,
				Status:     FindingFailed,
				Severity:   SeverityCritical,
				Message:    err.Error(),
				Evidence:   map[string]any{"value": value},
				RepairHint: "Use a relative remote prefix such as loom/main-snapshots, never a local path.",
			})
			continue
		}
		if looksLikeRuntimePath(value, runtime) {
			findings = append(findings, DoctorFinding{
				Code:       "cloud.path." + field,
				Status:     FindingFailed,
				Severity:   SeverityCritical,
				Message:    "Cloud remote prefix appears to point into a local LOOM runtime path.",
				Evidence:   map[string]any{"value": value},
				RepairHint: "Remote prefixes must be cloud-relative names, not /var/lib/loom paths.",
			})
			continue
		}
		findings = append(findings, okFinding("cloud.path."+field, "Cloud remote prefix is cloud-relative.", map[string]any{"value": value}))
	}
	return findings
}

func looksLikeRuntimePath(value string, runtime loomconfig.Config) bool {
	if strings.HasPrefix(value, "/") {
		return true
	}
	for _, runtimePath := range []string{runtime.DataDir, runtime.ObjectStore, runtime.StorageExport, runtime.MainDocuments, runtime.BoxPath} {
		runtimePath = strings.TrimSpace(runtimePath)
		if runtimePath == "" {
			continue
		}
		if strings.HasPrefix(value, runtimePath) {
			return true
		}
	}
	return false
}

func checkStateDir(path string) DoctorFinding {
	info, err := os.Stat(path)
	if err != nil {
		status := FindingFailed
		severity := SeverityCritical
		if errors.Is(err, os.ErrNotExist) {
			status = FindingWarning
			severity = SeverityWarning
		}
		return DoctorFinding{
			Code:       "cloud.state_dir",
			Status:     status,
			Severity:   severity,
			Message:    "Cloud state directory is not ready.",
			Evidence:   map[string]any{"path": path, "error": err.Error()},
			RepairHint: "Create the configured cloud state directory and make it writable by the LOOM service user.",
		}
	}
	if !info.IsDir() {
		return DoctorFinding{
			Code:       "cloud.state_dir",
			Status:     FindingFailed,
			Severity:   SeverityCritical,
			Message:    "Cloud state path exists but is not a directory.",
			Evidence:   map[string]any{"path": path},
			RepairHint: "Move the file and create a directory at the configured cloud state path.",
		}
	}
	if info.Mode().Perm()&0o200 == 0 {
		return DoctorFinding{
			Code:       "cloud.state_dir",
			Status:     FindingFailed,
			Severity:   SeverityCritical,
			Message:    "Cloud state directory is not owner-writable.",
			Evidence:   map[string]any{"path": path, "mode": info.Mode().Perm().String()},
			RepairHint: "Grant the LOOM service user write access to the cloud state directory.",
		}
	}
	return okFinding("cloud.state_dir", "Cloud state directory is present and owner-writable.", map[string]any{"path": path})
}

func checkRcloneBinary(cfg Config, lookPath func(string) (string, error)) DoctorFinding {
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	resolved, err := lookPath(cfg.RcloneBinary)
	if err != nil {
		return DoctorFinding{
			Code:       "cloud.rclone.binary",
			Status:     FindingFailed,
			Severity:   SeverityCritical,
			Message:    "rclone is required for cloud storage but was not found.",
			Evidence:   map[string]any{"binary": cfg.RcloneBinary},
			RepairHint: "Install rclone on the main node or configure loom.cloud.installRclone in Nix.",
		}
	}
	return okFinding("cloud.rclone.binary", "rclone binary is available.", map[string]any{"path": resolved})
}

func checkRcloneConfig(path string) DoctorFinding {
	info, err := os.Stat(path)
	if err != nil {
		return DoctorFinding{
			Code:       "cloud.rclone.config",
			Status:     FindingFailed,
			Severity:   SeverityCritical,
			Message:    "rclone config is missing or unreadable.",
			Evidence:   map[string]any{"path": path, "error": err.Error()},
			RepairHint: "Create the rclone config with Hetzner Storage Box credentials and mode 0600 or 0640.",
		}
	}
	if info.IsDir() {
		return DoctorFinding{
			Code:       "cloud.rclone.config",
			Status:     FindingFailed,
			Severity:   SeverityCritical,
			Message:    "rclone config path is a directory.",
			Evidence:   map[string]any{"path": path},
			RepairHint: "Write the rclone config as a file at the configured path.",
		}
	}
	mode := info.Mode().Perm()
	if mode&0o007 != 0 {
		return DoctorFinding{
			Code:       "cloud.rclone.config",
			Status:     FindingFailed,
			Severity:   SeverityCritical,
			Message:    "rclone config is world-accessible.",
			Evidence:   map[string]any{"path": path, "mode": mode.String()},
			RepairHint: "Run chmod 0600 or chmod 0640 on the rclone config.",
		}
	}
	return okFinding("cloud.rclone.config", "rclone config is present and not world-readable.", map[string]any{"path": path, "mode": mode.String()})
}

func checkRemote(ctx context.Context, cfg Config, driver Driver) (RemoteStatus, []RootStatus, DoctorFinding) {
	probe, err := ProbeRemote(ctx, driver, "")
	remote := probe.Status
	if err != nil {
		return remote, rootStatuses(cfg, nil), DoctorFinding{
			Code:       "cloud.remote.reachable",
			Status:     FindingFailed,
			Severity:   SeverityCritical,
			Message:    "Cloud remote is not reachable through rclone.",
			Evidence:   map[string]any{"remote_uri": cfg.RemoteURI(""), "error": err.Error()},
			RepairHint: "Check the rclone remote name, Hetzner credentials, SSH key, and network route.",
		}
	}
	roots := rootStatuses(cfg, probe.Entries)
	return remote, roots, okFinding("cloud.remote.reachable", "Cloud remote is reachable through rclone.", map[string]any{"remote_uri": cfg.RemoteURI("")})
}

func okFinding(code, message string, evidence ...map[string]any) DoctorFinding {
	finding := DoctorFinding{Code: code, Status: FindingOK, Severity: SeverityInfo, Message: message}
	if len(evidence) > 0 {
		finding.Evidence = evidence[0]
	}
	return finding
}

func infoFinding(code, message string, evidence ...map[string]any) DoctorFinding {
	return okFinding(code, message, evidence...)
}

func summarizeFindings(findings []DoctorFinding, defaultStatus string) string {
	status := defaultStatus
	for _, finding := range findings {
		if finding.Status == FindingFailed && finding.Severity == SeverityCritical {
			return "failed"
		}
		if finding.Status == FindingWarning && status != "failed" {
			status = "warning"
		}
	}
	return status
}

func ConfigDir(configPath string) string {
	if strings.TrimSpace(configPath) == "" {
		configPath = DefaultConfigPath
	}
	return filepath.Dir(configPath)
}
