package bootstrapssh

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type SystemRunner struct {
	Target SSHTarget
}

func NewSystemRunner(spec Spec) SystemRunner {
	return SystemRunner{Target: targetFromSpec(spec)}
}

func targetFromSpec(spec Spec) SSHTarget {
	host := strings.TrimSpace(spec.TargetHost)
	user := strings.TrimSpace(spec.SSHUser)
	if strings.Contains(host, "@") && user == "" {
		parts := strings.SplitN(host, "@", 2)
		user = strings.TrimSpace(parts[0])
		host = strings.TrimSpace(parts[1])
	}
	keyPath := strings.TrimSpace(spec.SSHKeyPath)
	return SSHTarget{
		Host:              host,
		User:              user,
		Port:              spec.SSHPort,
		KeyPathConfigured: keyPath != "",
		keyPath:           keyPath,
	}
}

func (r SystemRunner) Run(ctx context.Context, command RemoteCommand) (RemoteResult, error) {
	args := r.sshArgs()
	args = append(args, r.Target.address(), "sh", "-s")
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = strings.NewReader(command.Command + "\n" + command.Stdin)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := RemoteResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: 0}
	if err != nil {
		result.ExitCode = exitCode(err)
		return result, fmt.Errorf("ssh command failed: %s", r.redact(strings.TrimSpace(stderr.String())))
	}
	return result, nil
}

func (r SystemRunner) CopyTo(ctx context.Context, localPath string, remotePath string, opts CopyOptions) error {
	if strings.TrimSpace(localPath) == "" {
		return fmt.Errorf("local source path is required")
	}
	if strings.TrimSpace(remotePath) == "" {
		return fmt.Errorf("remote source path is required")
	}
	if _, err := r.Run(ctx, RemoteCommand{Command: "mkdir -p " + shellQuote(remotePath)}); err != nil {
		return err
	}
	args := []string{"-az"}
	for _, exclude := range opts.Excludes {
		exclude = strings.TrimSpace(exclude)
		if exclude != "" {
			args = append(args, "--exclude", exclude)
		}
	}
	sshArgs := r.sshArgs()
	args = append(args, "-e", "ssh "+shellJoin(sshArgs))
	source := filepath.Clean(localPath)
	if !strings.HasSuffix(source, string(filepath.Separator)) {
		source += string(filepath.Separator)
	}
	args = append(args, source, r.Target.address()+":"+remotePath+"/")
	cmd := exec.CommandContext(ctx, "rsync", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("rsync source delivery failed: %s", r.redact(strings.TrimSpace(stderr.String())))
	}
	return nil
}

func (r SystemRunner) sshArgs() []string {
	args := []string{"-o", "BatchMode=yes"}
	if r.Target.Port > 0 {
		args = append(args, "-p", strconv.Itoa(r.Target.Port))
	}
	if strings.TrimSpace(r.Target.keyPath) != "" {
		args = append(args, "-i", r.Target.keyPath)
	}
	return args
}

func (r SystemRunner) redact(value string) string {
	keyPath := strings.TrimSpace(r.Target.keyPath)
	if keyPath == "" {
		return value
	}
	return strings.ReplaceAll(value, keyPath, "[redacted-ssh-key-path]")
}

func (t SSHTarget) address() string {
	if strings.TrimSpace(t.User) == "" {
		return strings.TrimSpace(t.Host)
	}
	return strings.TrimSpace(t.User) + "@" + strings.TrimSpace(t.Host)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return 1
}

func shellJoin(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
