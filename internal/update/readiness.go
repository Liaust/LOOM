package update

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ReadinessTarget is resolved from the reviewed release before activation,
// never from the active symlink or the running unit's configured ExecStart.
type ReadinessTarget struct {
	ReleaseID     string `json:"release_id" yaml:"release_id"`
	ReleasePath   string `json:"release_path" yaml:"release_path"`
	Executable    string `json:"executable" yaml:"executable"`
	MigrationHead int64  `json:"migration_head" yaml:"migration_head"`
}

// ProcessIdentity uses Linux /proc start ticks to distinguish PID reuse. The
// health producer must return the same self-observed identity in data.instance.
type ProcessIdentity struct {
	PID        int    `json:"pid" yaml:"pid"`
	StartTicks string `json:"start_ticks" yaml:"start_ticks"`
	Executable string `json:"executable" yaml:"executable"`
}

type ReadinessEvidence struct {
	Target        ReadinessTarget `json:"target" yaml:"target"`
	Process       ProcessIdentity `json:"process" yaml:"process"`
	HealthStatus  string          `json:"health_status" yaml:"health_status"`
	HealthVersion string          `json:"health_version" yaml:"health_version"`
	MigrationHead int64           `json:"migration_head" yaml:"migration_head"`
	ObservedAt    time.Time       `json:"observed_at" yaml:"observed_at"`
}

var nixStorePackagePattern = regexp.MustCompile(`^/nix/store/[0-9a-z]{32}-[^/[:space:]]+$`)

func resolveReadinessTarget(ctx context.Context, runner CommandRunner, release ReleaseState, flakeOutput string, migrationHead int64) (ReadinessTarget, error) {
	target := ReadinessTarget{ReleaseID: release.ReleaseID, ReleasePath: release.Path, MigrationHead: migrationHead}
	if release.Path == "" || migrationHead < 0 {
		return target, fmt.Errorf("readiness target release or migration head is unknown")
	}
	flake, host, ok := strings.Cut(flakeArgument(release.Path, flakeOutput), "#")
	if !ok || host == "" || strings.ContainsAny(host, "/#\n\r") || filepath.Clean(flake) != filepath.Clean(release.Path) {
		return target, fmt.Errorf("readiness requires a NixOS host in the reviewed release flake")
	}
	// Evaluation only: no build, lockfile rewrite or import-from-derivation.
	output, err := runner(ctx, "nix", "eval", "--raw", "--no-write-lock-file", "--option", "allow-import-from-derivation", "false",
		flake+"#nixosConfigurations."+strconv.Quote(host)+".config.loom.service.package.outPath")
	if err != nil {
		return target, fmt.Errorf("resolve reviewed loomd package: %w", err)
	}
	storePath := strings.TrimSpace(string(output))
	if !nixStorePackagePattern.MatchString(storePath) {
		return target, fmt.Errorf("reviewed loomd package did not resolve to an immutable Nix store path")
	}
	target.Executable = storePath + "/bin/loomd"
	return target, nil
}

func validateReadinessTarget(target ReadinessTarget) error {
	if target.ReleaseID == "" || !filepath.IsAbs(target.ReleasePath) || target.MigrationHead < 0 ||
		filepath.Base(target.Executable) != "loomd" || filepath.Base(filepath.Dir(target.Executable)) != "bin" ||
		!nixStorePackagePattern.MatchString(filepath.Dir(filepath.Dir(target.Executable))) {
		return fmt.Errorf("exact running-target identity is unknown or invalid")
	}
	return nil
}

func inspectServiceProcess(ctx context.Context, runner CommandRunner) (ProcessIdentity, error) {
	output, err := runner(ctx, "systemctl", "show", "loomd.service", "--property=MainPID", "--property=ActiveState", "--property=SubState")
	if err != nil {
		return ProcessIdentity{}, fmt.Errorf("observe loomd service identity: %w", err)
	}
	properties := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			properties[key] = value
		}
	}
	pid, err := strconv.Atoi(properties["MainPID"])
	if err != nil || pid <= 0 || properties["ActiveState"] != "active" || properties["SubState"] != "running" {
		return ProcessIdentity{}, fmt.Errorf("loomd service has no active running process identity")
	}
	stat, err := runner(ctx, "cat", fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ProcessIdentity{}, fmt.Errorf("observe loomd process start: %w", err)
	}
	start, err := processStartTicks(stat, pid)
	if err != nil {
		return ProcessIdentity{}, err
	}
	executable, err := runner(ctx, "readlink", fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		// Cross-user /proc links may require the operator's existing sudo
		// authority. This is one fixed read, never a permission change or restart.
		executable, err = runner(ctx, "sudo", "-n", "readlink", fmt.Sprintf("/proc/%d/exe", pid))
		if err != nil {
			return ProcessIdentity{}, fmt.Errorf("observe running loomd executable: %w", err)
		}
	}
	return ProcessIdentity{PID: pid, StartTicks: start, Executable: strings.TrimSpace(string(executable))}, nil
}

func processStartTicks(stat []byte, pid int) (string, error) {
	// comm can contain spaces and parentheses. Fields after its last ')' start
	// at field 3; starttime is field 22, index 19 in that suffix.
	s := strings.TrimSpace(string(stat))
	open, close := strings.Index(s, "("), strings.LastIndex(s, ")")
	if open < 1 || close <= open || strings.TrimSpace(s[:open]) != strconv.Itoa(pid) {
		return "", fmt.Errorf("loomd process stat identity is unknown")
	}
	fields := strings.Fields(s[close+1:])
	if len(fields) <= 19 {
		return "", fmt.Errorf("loomd process start identity is missing")
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || ticks == 0 {
		return "", fmt.Errorf("loomd process start identity is invalid")
	}
	return strconv.FormatUint(ticks, 10), nil
}

type readinessHealth struct {
	OK   bool `json:"ok"`
	Data struct {
		Service  string `json:"service"`
		Status   string `json:"status"`
		Version  string `json:"version"`
		Instance struct {
			PID        int    `json:"pid"`
			StartTicks string `json:"start_ticks"`
		} `json:"instance"`
		Checks struct {
			Migrations struct {
				Status         string `json:"status"`
				CurrentVersion *int64 `json:"current_version"`
				LatestVersion  *int64 `json:"latest_version"`
				Pending        int64  `json:"pending"`
				Error          string `json:"error"`
			} `json:"migrations"`
		} `json:"checks"`
	} `json:"data"`
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

func runTargetReadiness(ctx context.Context, runner CommandRunner, target ReadinessTarget) (ReadinessEvidence, error) {
	return runTargetReadinessUntilReady(ctx, runner, target, 3*time.Minute, time.Second)
}

func runTargetReadinessUntilReady(ctx context.Context, runner CommandRunner, target ReadinessTarget, timeout, interval time.Duration) (ReadinessEvidence, error) {
	if err := validateReadinessTarget(target); err != nil {
		return ReadinessEvidence{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Package projections may symlink bin/loomd. Resolve the reviewed executable
	// after the build, without accepting unit configuration as target evidence.
	output, err := runner(ctx, "readlink", "-e", target.Executable)
	if err != nil {
		return ReadinessEvidence{}, fmt.Errorf("resolve reviewed executable: %w", err)
	}
	expectedExecutable := strings.TrimSpace(string(output))
	canonicalTarget := target
	canonicalTarget.Executable = expectedExecutable
	if err := validateReadinessTarget(canonicalTarget); err != nil {
		return ReadinessEvidence{}, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return ReadinessEvidence{}, fmt.Errorf("wait for loomd readiness: %w", err)
		}
		// Probe once before inspection so typed startup unavailability retains its
		// existing wait even while systemd has no active PID during migration.
		probe, probeErr := runner(ctx, "loom", "health", "--json")
		var health readinessHealth
		if err := json.Unmarshal(probe, &health); err != nil {
			return ReadinessEvidence{}, fmt.Errorf("parse loom health output: %w", err)
		}
		if health.Error.Code == "transport.unavailable" {
			if err := waitReadiness(ctx, interval); err != nil {
				return ReadinessEvidence{}, err
			}
			continue
		}
		if probeErr != nil {
			return ReadinessEvidence{}, probeErr
		}
		if err := validateTargetHealth(health, target); err != nil {
			return ReadinessEvidence{}, err
		}
		before, err := inspectServiceProcess(ctx, runner)
		if err != nil {
			return ReadinessEvidence{}, err
		}
		// Bracket the authoritative health response with host observations.
		output, checkErr := runner(ctx, "loom", "health", "--json")
		health = readinessHealth{}
		if err := json.Unmarshal(output, &health); err != nil {
			return ReadinessEvidence{}, fmt.Errorf("parse loom health output: %w", err)
		}
		if health.Error.Code == "transport.unavailable" {
			if err := waitReadiness(ctx, interval); err != nil {
				return ReadinessEvidence{}, err
			}
			continue
		}
		if checkErr != nil {
			return ReadinessEvidence{}, checkErr
		}
		if err := validateTargetHealth(health, target); err != nil {
			return ReadinessEvidence{}, err
		}
		after, err := inspectServiceProcess(ctx, runner)
		if err != nil {
			return ReadinessEvidence{}, err
		}
		if before != after {
			if err := waitReadiness(ctx, interval); err != nil {
				return ReadinessEvidence{}, err
			}
			continue
		}
		if health.Data.Instance.PID != before.PID || health.Data.Instance.StartTicks != before.StartTicks {
			return ReadinessEvidence{}, fmt.Errorf("health response is from a different loomd process")
		}
		if before.Executable != expectedExecutable {
			return ReadinessEvidence{}, fmt.Errorf("running loomd executable %q differs from reviewed target %q", before.Executable, expectedExecutable)
		}
		return ReadinessEvidence{Target: target, Process: before, HealthStatus: health.Data.Status, HealthVersion: health.Data.Version, MigrationHead: target.MigrationHead, ObservedAt: time.Now().UTC()}, nil
	}
}

func validateTargetHealth(health readinessHealth, target ReadinessTarget) error {
	if !health.OK || health.Error.Code != "" || health.Data.Status != "ok" || health.Data.Service != "loomd" {
		return fmt.Errorf("loom health is not ok")
	}
	m := health.Data.Checks.Migrations
	if m.Status != "ok" || m.Error != "" || m.CurrentVersion == nil || m.LatestVersion == nil ||
		*m.CurrentVersion != target.MigrationHead || *m.LatestVersion != target.MigrationHead || m.Pending != 0 {
		return fmt.Errorf("loomd migration state does not match reviewed target head %d", target.MigrationHead)
	}
	ticks, err := strconv.ParseUint(health.Data.Instance.StartTicks, 10, 64)
	if health.Data.Instance.PID <= 0 || err != nil || ticks == 0 || strconv.FormatUint(ticks, 10) != health.Data.Instance.StartTicks {
		return fmt.Errorf("loomd health instance identity is unknown; data.instance.pid and start_ticks are required")
	}
	return nil
}

func waitReadiness(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("wait for loomd readiness: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}
