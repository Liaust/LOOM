package setup

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	LaunchAgentLabel = "local.loom.node-agent"
)

var setupLaunchdRepairDelay = 750 * time.Millisecond

type LaunchdRunner interface {
	Launchctl(ctx context.Context, args ...string) (string, error)
}

type DefaultLaunchdRunner struct{}

func (DefaultLaunchdRunner) Launchctl(ctx context.Context, args ...string) (string, error) {
	if _, err := exec.LookPath("launchctl"); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "launchctl", args...)
	var out bytes.Buffer
	var errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(errOut.String())
		if message == "" {
			message = strings.TrimSpace(out.String())
		}
		if message != "" {
			return out.String(), fmt.Errorf("launchctl %s: %w: %s", strings.Join(args, " "), err, message)
		}
		return out.String(), fmt.Errorf("launchctl %s: %w", strings.Join(args, " "), err)
	}
	return out.String(), nil
}

type LaunchAgentSpec struct {
	Label      string
	PlistPath  string
	Program    string
	ConfigPath string
	StatePath  string
	DataDir    string
	LogDir     string
	StdoutPath string
	StderrPath string
	WorkingDir string
}

func LaunchAgentPlistPath(home string) string {
	return filepath.Join(filepath.Clean(home), "Library", "LaunchAgents", LaunchAgentLabel+".plist")
}

func launchAgentServiceTarget() string {
	return "gui/" + strconv.Itoa(os.Getuid()) + "/" + LaunchAgentLabel
}

func launchAgentDomain() string {
	return "gui/" + strconv.Itoa(os.Getuid())
}

func launchAgentSpecFromPlan(plan SetupPlan) (LaunchAgentSpec, bool, error) {
	if plan.Spec.InstallMode != InstallModeService || plan.Spec.ServiceManager != ServiceManagerLaunchd || !plan.Spec.EnableNodeAgent {
		return LaunchAgentSpec{}, false, nil
	}
	home := strings.TrimSpace(plan.Spec.HomeDir)
	if home == "" {
		return LaunchAgentSpec{}, false, fmt.Errorf("home_dir is required for LaunchAgent service")
	}
	plistPath := plan.Paths.LaunchAgentPlistPath
	if strings.TrimSpace(plistPath) == "" {
		plistPath = LaunchAgentPlistPath(home)
	}
	logDir := firstNonEmpty(plan.Paths.LogDir, filepath.Join(home, ".local", "state", "loom", "logs"))
	return LaunchAgentSpec{
		Label:      LaunchAgentLabel,
		PlistPath:  plistPath,
		Program:    filepath.Join(home, ".local", "bin", "loom-node-agent"),
		ConfigPath: plan.Paths.NodeAgentConfigPath,
		StatePath:  plan.Paths.NodeAgentStatePath,
		DataDir:    plan.Paths.NodeAgentDataDir,
		LogDir:     logDir,
		StdoutPath: filepath.Join(logDir, "loom-node-agent.out.log"),
		StderrPath: filepath.Join(logDir, "loom-node-agent.err.log"),
		WorkingDir: home,
	}, true, nil
}

func renderLaunchAgentPlist(spec LaunchAgentSpec) []byte {
	args := []string{
		spec.Program,
		"--config", spec.ConfigPath,
		"--state", spec.StatePath,
		"--data-dir", spec.DataDir,
		"serve",
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n")
	b.WriteString("<dict>\n")
	plistString(&b, "Label", spec.Label)
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, arg := range args {
		b.WriteString("\t\t<string>")
		b.WriteString(xmlEscape(arg))
		b.WriteString("</string>\n")
	}
	b.WriteString("\t</array>\n")
	plistBool(&b, "RunAtLoad", true)
	plistBool(&b, "KeepAlive", true)
	plistString(&b, "WorkingDirectory", spec.WorkingDir)
	plistString(&b, "StandardOutPath", spec.StdoutPath)
	plistString(&b, "StandardErrorPath", spec.StderrPath)
	b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
	plistString(&b, "PATH", filepath.Join(spec.WorkingDir, ".local", "bin")+":/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin")
	plistString(&b, "LOOM_NODE_AGENT_CONFIG", spec.ConfigPath)
	plistString(&b, "LOOM_NODE_AGENT_STATE", spec.StatePath)
	plistString(&b, "LOOM_NODE_AGENT_DATA_DIR", spec.DataDir)
	b.WriteString("\t</dict>\n")
	b.WriteString("</dict>\n</plist>\n")
	return []byte(b.String())
}

func plistString(b *strings.Builder, key, value string) {
	b.WriteString("\t<key>")
	b.WriteString(xmlEscape(key))
	b.WriteString("</key>\n\t<string>")
	b.WriteString(xmlEscape(value))
	b.WriteString("</string>\n")
}

func plistBool(b *strings.Builder, key string, value bool) {
	b.WriteString("\t<key>")
	b.WriteString(xmlEscape(key))
	b.WriteString("</key>\n")
	if value {
		b.WriteString("\t<true/>\n")
	} else {
		b.WriteString("\t<false/>\n")
	}
}

func xmlEscape(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	value = strings.ReplaceAll(value, `"`, "&quot;")
	value = strings.ReplaceAll(value, "'", "&apos;")
	return value
}

func applyLaunchAgent(plan SetupPlan, dryRun bool, runner LaunchdRunner) (applyChanges, error) {
	out := applyChanges{}
	spec, enabled, err := launchAgentSpecFromPlan(plan)
	if err != nil {
		out.blocked = append(out.blocked, ApplyChange{ID: "install_launch_agent", Category: "service", Status: ApplyStatusBlocked, Message: err.Error()})
		return out, nil
	}
	if !enabled {
		out.skipped = append(out.skipped, ApplyChange{ID: "install_launch_agent", Category: "service", Status: ApplyStatusSkipped, Message: "LaunchAgent is not configured"})
		return out, nil
	}

	for _, dir := range []applyDir{
		{id: "ensure_launch_agent_dir", category: "service", path: filepath.Dir(spec.PlistPath), mode: 0o700},
		{id: "ensure_node_agent_log_dir", category: "service", path: spec.LogDir, mode: 0o700},
	} {
		change, dirErr := ensureDirChange(dir.id, dir.category, dir.path, dir.mode, dryRun)
		if dirErr != nil {
			return out, dirErr
		}
		appendToApplyChanges(&out, change)
	}

	plistChange, err := writeFileChange("write_launch_agent_plist", "service", spec.PlistPath, renderLaunchAgentPlist(spec), 0o644, dryRun)
	if err != nil {
		return out, err
	}
	appendToApplyChanges(&out, plistChange)

	if dryRun {
		out.changed = append(out.changed, ApplyChange{
			ID:       "load_launch_agent",
			Category: "service",
			Status:   ApplyStatusWouldChange,
			Path:     spec.PlistPath,
			Message:  "would enable and kickstart " + spec.Label + ", bootstrapping only if missing",
			Metadata: map[string]any{
				"label":  spec.Label,
				"target": launchAgentServiceTarget(),
			},
		})
		return out, nil
	}

	if runner == nil {
		runner = DefaultLaunchdRunner{}
	}
	ctx := context.Background()
	target := launchAgentServiceTarget()
	bootstrapMessage := "LaunchAgent was already loaded; enabled and kickstarted " + spec.Label
	if !launchAgentLoaded(ctx, runner, target) {
		bootstrapMessage = "bootstrapped and kickstarted " + spec.Label
		if err := bootstrapLaunchAgent(ctx, runner, spec.PlistPath, target); err != nil {
			out.blocked = append(out.blocked, ApplyChange{ID: "load_launch_agent", Category: "service", Status: ApplyStatusBlocked, Path: spec.PlistPath, Message: err.Error()})
			return out, nil
		}
	}
	if _, err := runner.Launchctl(ctx, "enable", target); err != nil {
		out.blocked = append(out.blocked, ApplyChange{ID: "enable_launch_agent", Category: "service", Status: ApplyStatusBlocked, Path: spec.PlistPath, Message: err.Error()})
		return out, nil
	}
	if warning, err := kickstartLaunchAgent(ctx, runner, target); err != nil {
		out.blocked = append(out.blocked, ApplyChange{ID: "kickstart_launch_agent", Category: "service", Status: ApplyStatusBlocked, Path: spec.PlistPath, Message: err.Error()})
		return out, nil
	} else if warning != "" {
		bootstrapMessage += "; " + warning
	}
	out.changed = append(out.changed, ApplyChange{
		ID:       "load_launch_agent",
		Category: "service",
		Status:   ApplyStatusChanged,
		Path:     spec.PlistPath,
		Message:  bootstrapMessage,
		Metadata: map[string]any{
			"label":  spec.Label,
			"target": launchAgentServiceTarget(),
		},
	})
	return out, nil
}

func launchAgentLoaded(ctx context.Context, runner LaunchdRunner, target string) bool {
	_, err := runner.Launchctl(ctx, "print", target)
	return err == nil
}

func bootstrapLaunchAgent(ctx context.Context, runner LaunchdRunner, plistPath string, target string) error {
	const attempts = 4
	var firstErr error
	var firstOutput string
	var lastErr error
	var lastOutput string
	for attempt := 0; attempt < attempts; attempt++ {
		output, err := runner.Launchctl(ctx, "bootstrap", launchAgentDomain(), plistPath)
		if err == nil || launchdAlreadyLoadedError(output, err) || launchAgentLoaded(ctx, runner, target) {
			return nil
		}
		if firstErr == nil {
			firstErr = err
			firstOutput = output
		}
		lastErr = err
		lastOutput = output
		if !recoverableLaunchdBootstrapError(output, err) {
			return err
		}
		if attempt == attempts-1 {
			break
		}
		if err := waitSetupLaunchdRepairDelay(ctx, attempt); err != nil {
			return fmt.Errorf("wait before retrying LaunchAgent bootstrap: %w", err)
		}
	}
	return fmt.Errorf("LaunchAgent bootstrap failed after retries: original=%v original_output=%s retry=%v output=%s", firstErr, strings.TrimSpace(firstOutput), lastErr, strings.TrimSpace(lastOutput))
}

func kickstartLaunchAgent(ctx context.Context, runner LaunchdRunner, target string) (string, error) {
	const attempts = 4
	var firstErr error
	var firstOutput string
	var lastErr error
	var lastOutput string
	for attempt := 0; attempt < attempts; attempt++ {
		output, err := runner.Launchctl(ctx, "kickstart", "-k", target)
		if err == nil {
			return "", nil
		}
		if running, _ := launchAgentRunning(ctx, runner, target); running {
			return "kickstart reported an error, but LaunchAgent is running", nil
		}
		if firstErr == nil {
			firstErr = err
			firstOutput = output
		}
		lastErr = err
		lastOutput = output
		if !recoverableLaunchdKickstartError(output, err) {
			return "", err
		}
		if attempt == attempts-1 {
			break
		}
		if err := waitSetupLaunchdRepairDelay(ctx, attempt); err != nil {
			return "", fmt.Errorf("wait before retrying LaunchAgent kickstart: %w", err)
		}
	}
	if running, _ := launchAgentRunning(ctx, runner, target); running {
		return "kickstart reported an error, but LaunchAgent is running", nil
	}
	return "", fmt.Errorf("LaunchAgent kickstart failed after retries: original=%v original_output=%s retry=%v output=%s", firstErr, strings.TrimSpace(firstOutput), lastErr, strings.TrimSpace(lastOutput))
}

func launchAgentRunning(ctx context.Context, runner LaunchdRunner, target string) (bool, string) {
	output, err := runner.Launchctl(ctx, "print", target)
	if err != nil {
		return false, output
	}
	return parseLaunchdPID(output) > 0, output
}

func recoverableLaunchdBootstrapError(output string, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(output + " " + err.Error()))
	return strings.Contains(text, "bootstrap failed: 5") || strings.Contains(text, "input/output error")
}

func launchdAlreadyLoadedError(output string, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(output + " " + err.Error()))
	return strings.Contains(text, "already loaded") || strings.Contains(text, "already bootstrapped")
}

func recoverableLaunchdKickstartError(output string, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(output + " " + err.Error()))
	return strings.Contains(text, "exit status 37") ||
		strings.Contains(text, "service is not loaded") ||
		strings.Contains(text, "could not find service")
}

func waitSetupLaunchdRepairDelay(ctx context.Context, attempt int) error {
	if setupLaunchdRepairDelay <= 0 {
		return nil
	}
	delay := setupLaunchdRepairDelay
	for i := 0; i < attempt; i++ {
		delay *= 2
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func launchdServiceStatus(spec SetupSpec, paths PathPlan, runner LaunchdRunner) ServiceStatus {
	home := strings.TrimSpace(spec.HomeDir)
	plistPath := paths.LaunchAgentPlistPath
	if plistPath == "" && home != "" {
		plistPath = LaunchAgentPlistPath(home)
	}
	status := ServiceStatus{
		Name:     "loom-node-agent",
		Manager:  ServiceManagerLaunchd,
		Label:    LaunchAgentLabel,
		Path:     plistPath,
		Expected: true,
		Status:   "missing",
	}
	if strings.TrimSpace(plistPath) == "" {
		status.Status = "not_configured"
		status.Message = "LaunchAgent plist path is not configured"
		return status
	}
	status.Exists = pathExists(plistPath)
	if !status.Exists {
		return status
	}
	if runner == nil {
		runner = DefaultLaunchdRunner{}
	}
	output, err := runner.Launchctl(context.Background(), "print", launchAgentServiceTarget())
	if err != nil {
		status.Status = "not_loaded"
		status.Message = err.Error()
		return status
	}
	status.Loaded = true
	status.PID = parseLaunchdPID(output)
	if status.PID > 0 {
		status.Status = "running"
	} else {
		status.Status = "loaded"
	}
	return status
}

func parseLaunchdPID(output string) int {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "pid = ") {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, "pid = "))
		pid, _ := strconv.Atoi(raw)
		return pid
	}
	return 0
}

func installedServicesFromPlan(plan SetupPlan) []InstalledService {
	if plan.Spec.InstallMode != InstallModeService || plan.Spec.ServiceManager == ServiceManagerNone {
		return nil
	}
	if plan.Spec.EnableNodeAgent && !plan.Spec.EnableLoomd {
		service := InstalledService{Name: "loom-node-agent", Manager: plan.Spec.ServiceManager}
		if plan.Spec.ServiceManager == ServiceManagerLaunchd {
			service.Label = LaunchAgentLabel
			service.Path = plan.Paths.LaunchAgentPlistPath
		}
		return []InstalledService{service}
	}
	if plan.Spec.EnableLoomd {
		return []InstalledService{{Name: "loomd", Manager: plan.Spec.ServiceManager}}
	}
	return nil
}
