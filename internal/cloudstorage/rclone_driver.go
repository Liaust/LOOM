package cloudstorage

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type ExecFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

type RcloneDriver struct {
	Config            Config
	Exec              ExecFunc
	Now               func() time.Time
	DisableRemoteLock bool
}

type rcloneOperationClass string

const (
	rcloneOperationStatusList rcloneOperationClass = "status_list"
	rcloneOperationCopyCheck  rcloneOperationClass = "copy_check"
)

func NewRcloneDriver(cfg Config) RcloneDriver {
	normalized, err := NormalizeConfig(cfg)
	if err != nil {
		normalized = cfg
	}
	return RcloneDriver{Config: normalized, Exec: defaultExec, Now: time.Now}
}

func (d RcloneDriver) Status(ctx context.Context) (RemoteStatus, error) {
	probe, err := d.Probe(ctx, "")
	return probe.Status, err
}

func (d RcloneDriver) Probe(ctx context.Context, prefix string) (RemoteProbeResult, error) {
	started := time.Now()
	output, err := d.run(ctx, rcloneOperationStatusList, "lsf", d.Config.RemoteURI(prefix))
	if err != nil {
		status := RemoteStatus{Reachable: false, RemoteURI: d.Config.RemoteURI(prefix), CheckedAt: d.now(), DurationMS: durationMSSince(started)}
		return RemoteProbeResult{Status: status, ErrorClass: ClassifyRemoteError(err)}, err
	}
	entries := parseRcloneLSF(output)
	status := RemoteStatus{
		Reachable:  true,
		RemoteURI:  d.Config.RemoteURI(prefix),
		CheckedAt:  d.now(),
		Entries:    len(entries),
		DurationMS: durationMSSince(started),
	}
	return RemoteProbeResult{Status: status, Entries: entries}, nil
}

func (d RcloneDriver) List(ctx context.Context, prefix string) ([]RemoteEntry, error) {
	output, err := d.run(ctx, rcloneOperationStatusList, "lsf", d.Config.RemoteURI(prefix))
	if err != nil {
		return nil, err
	}
	return parseRcloneLSF(output), nil
}

func (d RcloneDriver) CopyToRemote(ctx context.Context, localPath, remotePath string, opts CopyOptions) (CopyResult, error) {
	started := time.Now()
	command := "copy"
	if opts.SingleFile {
		command = "copyto"
	}
	args := []string{}
	if opts.DryRun {
		args = append(args, "--dry-run")
	}
	if opts.Checksum {
		args = append(args, "--checksum")
	}
	if opts.PreserveLinks {
		args = append(args, "--links")
	}
	if opts.PreserveMetadata {
		args = append(args, "--metadata")
	}
	if !opts.SingleFile {
		args = append(args, "--create-empty-src-dirs")
	}
	dest := d.Config.RemoteURI(remotePath)
	output, err := d.run(ctx, rcloneOperationCopyCheck, command, append(args, localPath, dest)...)
	result := CopyResult{Command: command, Source: localPath, Dest: dest, DryRun: opts.DryRun, DurationMS: durationMSSince(started), FinishedAt: d.now(), Output: string(bytes.TrimSpace(output))}
	return result, err
}

func (d RcloneDriver) CopyFromRemote(ctx context.Context, remotePath, localPath string, opts CopyOptions) (CopyResult, error) {
	started := time.Now()
	command := "copy"
	if opts.SingleFile {
		command = "copyto"
	}
	args := []string{}
	if opts.DryRun {
		args = append(args, "--dry-run")
	}
	if opts.Checksum {
		args = append(args, "--checksum")
	}
	if opts.PreserveLinks {
		args = append(args, "--links")
	}
	if opts.PreserveMetadata {
		args = append(args, "--metadata")
	}
	if !opts.SingleFile {
		args = append(args, "--create-empty-src-dirs")
	}
	source := d.Config.RemoteURI(remotePath)
	output, err := d.run(ctx, rcloneOperationCopyCheck, command, append(args, source, localPath)...)
	result := CopyResult{Command: command, Source: source, Dest: localPath, DryRun: opts.DryRun, DurationMS: durationMSSince(started), FinishedAt: d.now(), Output: string(bytes.TrimSpace(output))}
	return result, err
}

func (d RcloneDriver) Check(ctx context.Context, localPath, remotePath string) (CheckResult, error) {
	return d.CheckWithOptions(ctx, localPath, remotePath, CheckOptions{})
}

func (d RcloneDriver) CheckWithOptions(ctx context.Context, localPath, remotePath string, opts CheckOptions) (CheckResult, error) {
	started := time.Now()
	remote := d.Config.RemoteURI(remotePath)
	args := []string{}
	if opts.PreserveLinks {
		args = append(args, "--links")
	}
	if opts.PreserveMetadata {
		args = append(args, "--metadata")
	}
	args = append(args, localPath, remote)
	output, err := d.run(ctx, rcloneOperationCopyCheck, "check", args...)
	result := CheckResult{
		Matched:    err == nil,
		LocalPath:  localPath,
		RemotePath: remote,
		DurationMS: durationMSSince(started),
		CheckedAt:  d.now(),
		Output:     string(bytes.TrimSpace(output)),
	}
	return result, err
}

func (d RcloneDriver) MoveRemote(ctx context.Context, fromRemotePath, toRemotePath string, opts CopyOptions) (CopyResult, error) {
	started := time.Now()
	args := []string{}
	if opts.DryRun {
		args = append(args, "--dry-run")
	}
	source := d.Config.RemoteURI(fromRemotePath)
	dest := d.Config.RemoteURI(toRemotePath)
	output, err := d.run(ctx, rcloneOperationCopyCheck, "moveto", append(args, source, dest)...)
	result := CopyResult{Command: "moveto", Source: source, Dest: dest, DryRun: opts.DryRun, DurationMS: durationMSSince(started), FinishedAt: d.now(), Output: string(bytes.TrimSpace(output))}
	return result, err
}

func (d RcloneDriver) run(ctx context.Context, class rcloneOperationClass, command string, args ...string) ([]byte, error) {
	execFunc := d.Exec
	if execFunc == nil {
		execFunc = defaultExec
	}
	rcloneArgs := []string{}
	if strings.TrimSpace(d.Config.RcloneConfigPath) != "" {
		rcloneArgs = append(rcloneArgs, "--config", d.Config.RcloneConfigPath)
	}
	rcloneArgs = append(rcloneArgs, rcloneBudgetFlags(class)...)
	rcloneArgs = append(rcloneArgs, command)
	rcloneArgs = append(rcloneArgs, args...)
	var output []byte
	run := func(ctx context.Context) error {
		var err error
		output, err = execFunc(ctx, d.Config.RcloneBinary, rcloneArgs...)
		return err
	}
	if d.DisableRemoteLock {
		return output, run(ctx)
	}
	lockErr := WithRemoteLock(ctx, d.Config, RemoteLockOptions{Operation: "rclone." + command, Wait: rcloneLockWait(d.Config, class)}, run)
	return output, lockErr
}

func rcloneBudgetFlags(class rcloneOperationClass) []string {
	switch class {
	case rcloneOperationCopyCheck:
		return []string{
			"--retries", "2",
			"--low-level-retries", "2",
			"--retries-sleep", "60s",
			"--contimeout", "10s",
			"--timeout", "5m",
			"--checkers", "1",
			"--transfers", "1",
			"--sftp-connections", "3",
			"--sftp-concurrency", "4",
			"--stats", "30s",
		}
	default:
		return []string{
			"--retries", "1",
			"--low-level-retries", "1",
			"--contimeout", "5s",
			"--timeout", "15s",
			"--checkers", "1",
			"--transfers", "1",
			"--sftp-connections", "2",
			"--sftp-concurrency", "4",
			"--stats", "0",
		}
	}
}

func rcloneLockWait(cfg Config, class rcloneOperationClass) time.Duration {
	if class == rcloneOperationCopyCheck {
		return RemoteLockEffectfulWait(cfg)
	}
	return RemoteLockWait(cfg)
}

func (d RcloneDriver) now() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}

func parseRcloneLSF(output []byte) []RemoteEntry {
	lines := strings.Split(string(output), "\n")
	entries := []RemoteEntry{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		entry := RemoteEntry{Path: strings.TrimSuffix(line, "/"), IsDir: strings.HasSuffix(line, "/")}
		entries = append(entries, entry)
	}
	return entries
}

func defaultExec(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func durationMSSince(start time.Time) int64 {
	if start.IsZero() {
		return 0
	}
	ms := time.Since(start).Milliseconds()
	if ms < 0 {
		return 0
	}
	return ms
}
