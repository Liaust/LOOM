package bootstrapssh

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"loom.local/loom/internal/setup"
)

const (
	StatusPlanned = "planned"
	StatusApplied = "applied"
	StatusPartial = "partial"
	StatusFailed  = "failed"
	StatusBlocked = "blocked"
)

func BuildRemoteSetupPlan(setupPlan setup.SetupPlan, source SourcePlan) RemoteSetupPlan {
	specPath := "/tmp/loom-setup-" + setupPlan.PlanID + ".yaml"
	enrollmentResultPath := "/tmp/loom-enrollment-" + setupPlan.PlanID + ".json"
	remote := RemoteSetupPlan{
		SpecPath:             specPath,
		EnrollmentResultPath: enrollmentResultPath,
		PackageMode:          setupPlan.Spec.PackageMode,
		RequiredTools:        requiredTools(setupPlan.Spec),
	}
	remote.BuildCommand = remoteBuildCommand(setupPlan.Spec, source.RemotePath)
	remote.ApplyCommand = remoteLoomCommand(setupPlan.Spec, source.RemotePath) + " --json --no-interactive setup apply --spec " + shellQuote(specPath) + " --yes"
	remote.StatusCommand = remoteLoomCommand(setupPlan.Spec, source.RemotePath) + " --json --no-interactive setup status --spec " + shellQuote(specPath)
	remote.DoctorCommand = remoteLoomCommand(setupPlan.Spec, source.RemotePath) + " --json --no-interactive setup doctor --spec " + shellQuote(specPath)
	remote.RecordEnrollmentCommand = remoteLoomCommand(setupPlan.Spec, source.RemotePath) +
		" --json --no-interactive setup manifest record-enrollment --manifest " +
		shellQuote(setupPlan.Paths.ManifestPath) + " --from-file " + shellQuote(enrollmentResultPath)
	return remote
}

func writeRemoteSetupSpec(ctx context.Context, runner Runner, remote RemoteSetupPlan, spec setup.SetupSpec) (StepResult, error) {
	payload, err := yaml.Marshal(spec)
	if err != nil {
		return StepResult{ID: "copy_setup_spec", Status: StatusFailed, Message: err.Error()}, err
	}
	command := "umask 077 && cat > " + shellQuote(remote.SpecPath) + " && chmod 0600 " + shellQuote(remote.SpecPath)
	result, err := runner.Run(ctx, RemoteCommand{Command: command, Stdin: string(payload)})
	step := StepResult{ID: "copy_setup_spec", Status: StatusApplied, Command: command, ExitCode: result.ExitCode}
	if err != nil {
		step.Status = StatusFailed
		step.Message = err.Error()
		return step, remoteError("bootstrap.setup_spec.copy_failed", "copy_setup_spec", "copy remote setup spec", result, err)
	}
	return step, nil
}

func runRemoteBuild(ctx context.Context, runner Runner, remote RemoteSetupPlan) (StepResult, error) {
	if strings.TrimSpace(remote.BuildCommand) == "" {
		return StepResult{ID: "build_binaries", Status: "skipped", Message: "no build command required"}, nil
	}
	result, err := runner.Run(ctx, RemoteCommand{Command: remote.BuildCommand})
	step := StepResult{ID: "build_binaries", Status: StatusApplied, Command: remote.BuildCommand, ExitCode: result.ExitCode}
	if err != nil {
		step.Status = StatusFailed
		step.Message = err.Error()
		return step, remoteError("bootstrap.binary.unavailable", "build_binaries", "prepare remote LOOM binaries", result, err)
	}
	return step, nil
}

func runRemoteSetupApply(ctx context.Context, runner Runner, remote RemoteSetupPlan) (setup.ApplyResult, StepResult, error) {
	var decoded setup.ApplyResult
	step, err := runRemoteJSON(ctx, runner, "setup_apply", remote.ApplyCommand, &decoded)
	return decoded, step, err
}

func runRemoteSetupStatus(ctx context.Context, runner Runner, remote RemoteSetupPlan) (setup.SetupStatus, StepResult, error) {
	var decoded setup.SetupStatus
	step, err := runRemoteJSON(ctx, runner, "setup_status", remote.StatusCommand, &decoded)
	return decoded, step, err
}

func runRemoteSetupDoctor(ctx context.Context, runner Runner, remote RemoteSetupPlan) (setup.DoctorReport, StepResult, error) {
	var decoded setup.DoctorReport
	step, err := runRemoteJSON(ctx, runner, "setup_doctor", remote.DoctorCommand, &decoded)
	return decoded, step, err
}

func recordRemoteEnrollmentResult(ctx context.Context, runner Runner, remote RemoteSetupPlan, result any) (setup.RecordEnrollmentResult, StepResult, error) {
	var decoded setup.RecordEnrollmentResult
	payload, err := json.Marshal(result)
	if err != nil {
		step := StepResult{ID: "record_enrollment", Status: StatusFailed, Message: err.Error()}
		return decoded, step, err
	}
	writeCommand := "umask 077 && cat > " + shellQuote(remote.EnrollmentResultPath) + " && chmod 0600 " + shellQuote(remote.EnrollmentResultPath)
	writeResult, err := runner.Run(ctx, RemoteCommand{Command: writeCommand, Stdin: string(payload)})
	if err != nil {
		step := StepResult{ID: "record_enrollment", Status: StatusFailed, Command: writeCommand, ExitCode: writeResult.ExitCode, Message: err.Error()}
		return decoded, step, remoteError("bootstrap.enrollment.record_failed", "record_enrollment", "copy remote enrollment result", writeResult, err)
	}
	step, err := runRemoteJSON(ctx, runner, "record_enrollment", remote.RecordEnrollmentCommand, &decoded)
	cleanupCommand := "rm -f " + shellQuote(remote.EnrollmentResultPath)
	_, _ = runner.Run(ctx, RemoteCommand{Command: cleanupCommand})
	if err != nil {
		return decoded, step, err
	}
	return decoded, step, nil
}

func runRemoteJSON(ctx context.Context, runner Runner, id string, command string, out any) (StepResult, error) {
	result, err := runner.Run(ctx, RemoteCommand{Command: command})
	step := StepResult{ID: id, Status: StatusApplied, Command: command, ExitCode: result.ExitCode}
	if strings.TrimSpace(result.Stdout) != "" {
		if jsonErr := json.Unmarshal([]byte(result.Stdout), out); jsonErr != nil {
			step.Status = StatusFailed
			step.Message = jsonErr.Error()
			return step, BootstrapError{
				Code:          "bootstrap.setup.remote_invalid_json",
				Phase:         id,
				Message:       jsonErr.Error(),
				ExitCode:      result.ExitCode,
				StdoutExcerpt: safeExcerpt(result.Stdout),
				StderrExcerpt: safeExcerpt(result.Stderr),
			}
		}
	}
	if err != nil {
		step.Status = StatusFailed
		step.Message = err.Error()
		return step, remoteError(remoteFailureCode(id), id, "run remote "+id, result, err)
	}
	return step, nil
}

func remoteFailureCode(id string) string {
	switch id {
	case "setup_status":
		return "bootstrap.status.remote_failed"
	case "setup_doctor":
		return "bootstrap.doctor.remote_failed"
	default:
		return "bootstrap.setup.remote_failed"
	}
}

func remoteError(code, phase, message string, result RemoteResult, err error) BootstrapError {
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		message = err.Error()
	}
	return BootstrapError{
		Code:          code,
		Phase:         phase,
		Message:       message,
		ExitCode:      result.ExitCode,
		StdoutExcerpt: safeExcerpt(result.Stdout),
		StderrExcerpt: safeExcerpt(result.Stderr),
	}
}

func safeExcerpt(value string) string {
	value = RedactEnrollmentSecrets(strings.TrimSpace(value))
	if len(value) <= 600 {
		return value
	}
	return value[:600] + "...[truncated]"
}

func requiredTools(spec setup.SetupSpec) []string {
	tools := []string{"loom"}
	switch spec.RuntimeClass {
	case "main_full":
		return appendMissingTool(tools, "loomd")
	case "workspace_full", "workspace_light":
		return appendMissingTool(tools, "loom-node-agent")
	default:
		if spec.EnableNodeAgent {
			return appendMissingTool(tools, "loom-node-agent")
		}
		return tools
	}
}

func appendMissingTool(tools []string, tool string) []string {
	for _, existing := range tools {
		if existing == tool {
			return tools
		}
	}
	return append(tools, tool)
}

func remoteBuildCommand(spec setup.SetupSpec, sourcePath string) string {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return ""
	}
	switch spec.PackageMode {
	case setup.PackageModeNix:
		packages := make([]string, 0, len(requiredTools(spec)))
		for _, tool := range requiredTools(spec) {
			packages = append(packages, ".#"+tool)
		}
		return "cd " + shellQuote(sourcePath) + " && nix --extra-experimental-features " + shellQuote("nix-command flakes") + " build " + strings.Join(packages, " ")
	case setup.PackageModeLocalBuild:
		commands := []string{"cd " + shellQuote(sourcePath), "mkdir -p .loom/bin"}
		for _, tool := range requiredTools(spec) {
			subPackage := "./cmd/" + tool
			commands = append(commands, "go build -o "+shellQuote(".loom/bin/"+tool)+" "+shellQuote(subPackage))
		}
		return strings.Join(commands, " && ")
	default:
		return ""
	}
}

func remoteLoomCommand(spec setup.SetupSpec, sourcePath string) string {
	switch spec.PackageMode {
	case setup.PackageModeNix:
		return "cd " + shellQuote(sourcePath) + " && nix --extra-experimental-features " + shellQuote("nix-command flakes") + " run .#loom --"
	case setup.PackageModeLocalBuild:
		return "cd " + shellQuote(sourcePath) + " && ./.loom/bin/loom"
	default:
		return "loom"
	}
}

func remoteNodeAgentCommand(spec setup.SetupSpec, sourcePath string) string {
	switch spec.PackageMode {
	case setup.PackageModeNix:
		return "cd " + shellQuote(sourcePath) + " && nix --extra-experimental-features " + shellQuote("nix-command flakes") + " run .#loom-node-agent --"
	case setup.PackageModeLocalBuild:
		return "cd " + shellQuote(sourcePath) + " && ./.loom/bin/loom-node-agent"
	default:
		return "loom-node-agent"
	}
}

func packageModeForRemote(spec Spec, facts RemoteFacts) string {
	if strings.TrimSpace(spec.PackageMode) != "" {
		return spec.PackageMode
	}
	if facts.HasNix {
		return setup.PackageModeNix
	}
	if facts.HasGo {
		return setup.PackageModeLocalBuild
	}
	return setup.PackageModeUnknown
}

func validateRemoteExecutionSupport(spec setup.SetupSpec, facts RemoteFacts) error {
	switch spec.PackageMode {
	case setup.PackageModeNix:
		if !facts.HasNix {
			return fmt.Errorf("remote package mode nix requires nix on target")
		}
	case setup.PackageModeLocalBuild:
		if !facts.HasGo {
			return fmt.Errorf("remote package mode local-build requires go on target")
		}
	case setup.PackageModeUnknown:
		return fmt.Errorf("remote target has neither nix nor go available for building LOOM")
	}
	return nil
}
