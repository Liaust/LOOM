package bootstrapssh

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"loom.local/loom/internal/enrollmentflow"
	"loom.local/loom/internal/setup"
)

func Run(ctx context.Context, input RunInput) (Result, error) {
	spec, err := normalizeSpec(input.Spec)
	if err != nil {
		return Result{}, err
	}
	if !spec.DryRun && !spec.Yes {
		return Result{}, fmt.Errorf("SSH bootstrap source delivery requires --yes; use --dry-run to preview")
	}
	runner := input.Runner
	if runner == nil {
		runner = NewSystemRunner(spec)
	}
	facts, err := CollectRemoteFacts(ctx, runner)
	if err != nil {
		return Result{}, fmt.Errorf("collect remote facts: %w", err)
	}
	spec.PackageMode = packageModeForRemote(spec, facts)
	source, err := BuildSourcePlan(spec, facts)
	if err != nil {
		return Result{}, err
	}
	setupSpec := BuildSetupSpec(spec, facts, source)
	setupPlan, err := setup.Plan(setup.PlannerInput{
		Spec:  setupSpec,
		Facts: remoteSetupFacts(facts),
	})
	if err != nil {
		return Result{}, err
	}
	remoteSetup := BuildRemoteSetupPlan(setupPlan, source)
	warnings := planWarnings(spec, facts, source)
	now := time.Now
	if input.Now != nil {
		now = input.Now
	}
	plan := Plan{
		CreatedAt:           now().UTC(),
		Spec:                redactedSpec(spec),
		Target:              targetFromSpec(spec),
		Facts:               facts,
		Source:              source,
		SetupSpec:           setupPlan.Spec,
		SetupPlan:           setupPlan,
		RemoteSetup:         remoteSetup,
		Warnings:            warnings,
		WouldRunRemoteSetup: true,
	}
	plan.Target.keyPath = ""
	result := Result{
		Status:          StatusPlanned,
		DryRun:          spec.DryRun,
		Plan:            plan,
		Warnings:        warnings,
		RemoteSetupNote: "remote setup apply runs only when --yes is provided",
	}
	if spec.DryRun {
		result.RemoteSetupNote = "dry-run: remote setup apply was not run"
		return result, nil
	}
	if source.Mode != SourceModeCurrentRsync {
		result.Status = StatusFailed
		return result, fmt.Errorf("source mode %s can be planned but cannot be delivered in this slice", source.Mode)
	}
	if err := runner.CopyTo(ctx, source.LocalPath, source.RemotePath, CopyOptions{Excludes: source.Excludes}); err != nil {
		result.Status = StatusFailed
		return result, err
	}
	result.SourceCopied = true
	if err := validateRemoteExecutionSupport(setupPlan.Spec, facts); err != nil {
		result.Status = StatusFailed
		return result, BootstrapError{Code: "bootstrap.binary.unavailable", Phase: "preflight", Message: err.Error()}
	}
	buildStep, err := runRemoteBuild(ctx, runner, remoteSetup)
	result.Steps = append(result.Steps, buildStep)
	if err != nil {
		result.Status = StatusFailed
		return result, err
	}
	specStep, err := writeRemoteSetupSpec(ctx, runner, remoteSetup, setupPlan.Spec)
	result.Steps = append(result.Steps, specStep)
	if err != nil {
		result.Status = StatusFailed
		return result, err
	}
	result.SetupSpecCopied = true
	applyResult, applyStep, err := runRemoteSetupApply(ctx, runner, remoteSetup)
	result.RemoteApply = &applyResult
	result.Steps = append(result.Steps, applyStep)
	if err != nil {
		result.Status = StatusFailed
		return result, err
	}
	if applyResult.Refused || len(applyResult.Blocked) > 0 {
		result.Status = StatusBlocked
		return result, nil
	}
	if setupPlan.Spec.NodeKind != "main" {
		enrollmentResult, enrollmentStep, err := runSSHEnrollmentFlow(ctx, spec, runner, setupPlan, source, input)
		result.Enrollment = &enrollmentResult
		result.Steps = append(result.Steps, enrollmentStep)
		if err != nil {
			result.Status = StatusFailed
			return result, err
		}
		recordResult, recordStep, err := recordRemoteEnrollmentResult(ctx, runner, remoteSetup, enrollmentResult)
		_ = recordResult
		result.Steps = append(result.Steps, recordStep)
		if err != nil {
			result.Status = StatusPartial
			return result, err
		}
	}
	statusResult, statusStep, err := runRemoteSetupStatus(ctx, runner, remoteSetup)
	result.RemoteStatus = &statusResult
	result.Steps = append(result.Steps, statusStep)
	if err != nil {
		result.Status = StatusPartial
		return result, err
	}
	doctorResult, doctorStep, err := runRemoteSetupDoctor(ctx, runner, remoteSetup)
	result.RemoteDoctor = &doctorResult
	result.Steps = append(result.Steps, doctorStep)
	if err != nil {
		result.Status = StatusPartial
		return result, err
	}
	result.Status = StatusApplied
	return result, nil
}

func normalizeSpec(spec Spec) (Spec, error) {
	spec.TargetHost = strings.TrimSpace(spec.TargetHost)
	spec.SSHUser = strings.TrimSpace(spec.SSHUser)
	spec.SSHKeyPath = strings.TrimSpace(spec.SSHKeyPath)
	spec.NodeKey = strings.TrimSpace(spec.NodeKey)
	spec.DisplayName = strings.TrimSpace(spec.DisplayName)
	spec.NodeKind = normalizeToken(spec.NodeKind)
	spec.NodeRole = normalizeToken(spec.NodeRole)
	spec.RuntimeClass = normalizeToken(spec.RuntimeClass)
	spec.MainHost = strings.TrimSpace(spec.MainHost)
	spec.MainURL = strings.TrimSpace(spec.MainURL)
	spec.MainSSHUser = strings.TrimSpace(spec.MainSSHUser)
	spec.MainSSHKeyPath = strings.TrimSpace(spec.MainSSHKeyPath)
	spec.MainSocket = strings.TrimSpace(spec.MainSocket)
	spec.SourceMode = strings.TrimSpace(spec.SourceMode)
	spec.SourcePath = strings.TrimSpace(spec.SourcePath)
	spec.GitURL = strings.TrimSpace(spec.GitURL)
	spec.GitRef = strings.TrimSpace(spec.GitRef)
	spec.RemoteSourceDir = strings.TrimSpace(spec.RemoteSourceDir)
	spec.PackageMode = normalizeToken(spec.PackageMode)
	spec.InstallMode = normalizeToken(spec.InstallMode)
	spec.ServiceManager = normalizeToken(spec.ServiceManager)
	spec.BoxPath = strings.TrimSpace(spec.BoxPath)
	spec.BoxProfile = normalizeToken(spec.BoxProfile)
	spec.HomeDir = strings.TrimSpace(spec.HomeDir)
	spec.UserName = strings.TrimSpace(spec.UserName)
	spec.ConfigDir = strings.TrimSpace(spec.ConfigDir)
	spec.DataDir = strings.TrimSpace(spec.DataDir)
	spec.StateDir = strings.TrimSpace(spec.StateDir)
	spec.LogDir = strings.TrimSpace(spec.LogDir)
	spec.ServiceRoot = strings.TrimSpace(spec.ServiceRoot)
	spec.StorageRoot = strings.TrimSpace(spec.StorageRoot)
	spec.ImportsRoot = strings.TrimSpace(spec.ImportsRoot)
	spec.UserBackupsRoot = strings.TrimSpace(spec.UserBackupsRoot)
	spec.ArchiveRoot = strings.TrimSpace(spec.ArchiveRoot)
	spec.GeneratedRoot = strings.TrimSpace(spec.GeneratedRoot)
	spec.BoxStateRoot = strings.TrimSpace(spec.BoxStateRoot)
	spec.ObjectStorePath = strings.TrimSpace(spec.ObjectStorePath)
	spec.MainDocumentsPath = strings.TrimSpace(spec.MainDocumentsPath)
	spec.StorageExportRoot = strings.TrimSpace(spec.StorageExportRoot)
	spec.SocketPath = strings.TrimSpace(spec.SocketPath)
	spec.HTTPListenAddr = strings.TrimSpace(spec.HTTPListenAddr)
	spec.DBURL = strings.TrimSpace(spec.DBURL)
	spec.MigrationsDir = strings.TrimSpace(spec.MigrationsDir)
	spec.BootstrapMode = normalizeToken(spec.BootstrapMode)
	if spec.TargetHost == "" {
		return Spec{}, fmt.Errorf("SSH target host is required")
	}
	if spec.SSHPort < 0 {
		return Spec{}, fmt.Errorf("ssh port cannot be negative")
	}
	if spec.MainSSHPort < 0 {
		return Spec{}, fmt.Errorf("main ssh port cannot be negative")
	}
	if spec.EnrollmentTTLSeconds < 0 {
		return Spec{}, fmt.Errorf("enrollment ttl seconds cannot be negative")
	}
	if spec.NodeKind == "" {
		spec.NodeKind = "workspace"
	}
	if spec.EnrollmentTTLSeconds == 0 {
		spec.EnrollmentTTLSeconds = 1800
	}
	if spec.MainURL == "" && spec.MainHost != "" && spec.NodeKind != "main" {
		spec.MainURL = mainURLFromHost(spec.MainHost)
	}
	return spec, nil
}

type enrollmentResumeLoader interface {
	LoadResumeState(context.Context) (enrollmentflow.ResumeState, error)
}

func runSSHEnrollmentFlow(ctx context.Context, spec Spec, runner Runner, setupPlan setup.SetupPlan, source SourcePlan, input RunInput) (enrollmentflow.Result, StepResult, error) {
	mainRunner := input.EnrollmentMainRunner
	if mainRunner == nil {
		if strings.TrimSpace(spec.MainHost) != "" {
			mainSpec := Spec{
				TargetHost: spec.MainHost,
				SSHUser:    spec.MainSSHUser,
				SSHPort:    spec.MainSSHPort,
				SSHKeyPath: spec.MainSSHKeyPath,
			}
			mainRunner = SSHMainRunner{
				Runner:        NewSystemRunner(mainSpec),
				SocketPath:    spec.MainSocket,
				CorrelationID: "bootstrap.ssh.enroll." + setupPlan.Spec.NodeKey,
			}
		} else {
			mainRunner = enrollmentflow.NewLocalMainRunner(spec.MainSocket, "bootstrap.ssh.enroll."+setupPlan.Spec.NodeKey)
		}
	}
	targetRunner := input.EnrollmentTargetRunner
	if targetRunner == nil {
		targetRunner = SSHTargetRunner{
			Runner:        runner,
			SetupPlan:     setupPlan,
			Source:        source,
			CorrelationID: "bootstrap.ssh.enroll." + setupPlan.Spec.NodeKey,
		}
	}
	flowSpec := enrollmentflow.Spec{
		NodeKey:         setupPlan.Spec.NodeKey,
		DisplayName:     setupPlan.Spec.DisplayName,
		NodeKind:        setupPlan.Spec.NodeKind,
		NodeRole:        setupPlan.Spec.NodeRole,
		RuntimeClass:    setupPlan.Spec.RuntimeClass,
		TokenTTLSeconds: setupPlan.Spec.EnrollmentTTLSeconds,
		Approve:         true,
		VerifyHeartbeat: true,
		VerifyMain:      true,
	}
	if loader, ok := targetRunner.(enrollmentResumeLoader); ok {
		state, err := loader.LoadResumeState(ctx)
		if err != nil && spec.Resume {
			return enrollmentflow.Result{Status: enrollmentflow.StateFailed, Redacted: true}, StepResult{
				ID:      "enroll_node",
				Status:  StatusFailed,
				Message: RedactEnrollmentSecrets(err.Error()),
			}, err
		}
		if err == nil {
			flowSpec.Resume = true
			flowSpec.State = state
		}
	} else if spec.Resume {
		return enrollmentflow.Result{Status: enrollmentflow.StateFailed, Redacted: true}, StepResult{
			ID:      "enroll_node",
			Status:  StatusFailed,
			Message: "target enrollment runner cannot load resume state",
		}, BootstrapError{Code: "bootstrap.enrollment.resume_unavailable", Phase: "enroll_node", Message: "target enrollment runner cannot load resume state"}
	}
	enrollmentResult, err := enrollmentflow.Run(ctx, flowSpec, mainRunner, targetRunner)
	enrollmentResult = redactBootstrapEnrollmentResult(enrollmentResult)
	step := StepResult{ID: "enroll_node", Status: StatusApplied, Message: enrollmentResult.Status}
	if err != nil || enrollmentResult.Status == enrollmentflow.StateFailed || enrollmentResult.Status == enrollmentflow.StateUnrecoverable {
		step.Status = StatusFailed
		step.Message = enrollmentResult.FailureMessage
		if strings.TrimSpace(step.Message) == "" && err != nil {
			step.Message = RedactEnrollmentSecrets(err.Error())
		}
		return enrollmentResult, step, BootstrapError{
			Code:    "bootstrap.enrollment.failed",
			Phase:   "enroll_node",
			Message: firstNonEmpty(enrollmentResult.FailureMessage, "node enrollment failed"),
		}
	}
	return enrollmentResult, step, nil
}

func redactBootstrapEnrollmentResult(result enrollmentflow.Result) enrollmentflow.Result {
	result.FailureMessage = RedactEnrollmentSecrets(result.FailureMessage)
	for i := range result.Steps {
		result.Steps[i].Message = RedactEnrollmentSecrets(result.Steps[i].Message)
	}
	result.Redacted = true
	return result
}

func BuildSetupSpec(spec Spec, facts RemoteFacts, source SourcePlan) setup.SetupSpec {
	return setup.SetupSpec{
		NodeKey:              spec.NodeKey,
		DisplayName:          spec.DisplayName,
		NodeKind:             spec.NodeKind,
		NodeRole:             spec.NodeRole,
		RuntimeClass:         spec.RuntimeClass,
		MainURL:              spec.MainURL,
		InstallMode:          spec.InstallMode,
		ServiceManager:       spec.ServiceManager,
		PackageMode:          spec.PackageMode,
		UserName:             firstNonEmpty(spec.UserName, facts.User),
		HomeDir:              firstNonEmpty(spec.HomeDir, facts.HomeDir),
		SourcePath:           source.RemotePath,
		SourceCommit:         spec.GitRef,
		BoxPath:              spec.BoxPath,
		BoxProfile:           spec.BoxProfile,
		ConfigDir:            spec.ConfigDir,
		DataDir:              spec.DataDir,
		StateDir:             spec.StateDir,
		LogDir:               spec.LogDir,
		ServiceRoot:          spec.ServiceRoot,
		StorageRoot:          spec.StorageRoot,
		ImportsRoot:          spec.ImportsRoot,
		UserBackupsRoot:      spec.UserBackupsRoot,
		ArchiveRoot:          spec.ArchiveRoot,
		GeneratedRoot:        spec.GeneratedRoot,
		BoxStateRoot:         spec.BoxStateRoot,
		ObjectStorePath:      spec.ObjectStorePath,
		MainDocumentsPath:    spec.MainDocumentsPath,
		SocketPath:           spec.SocketPath,
		HTTPListenAddr:       spec.HTTPListenAddr,
		DBURL:                spec.DBURL,
		MigrationsDir:        spec.MigrationsDir,
		BootstrapMode:        spec.BootstrapMode,
		EnrollmentTTLSeconds: spec.EnrollmentTTLSeconds,
	}
}

func remoteSetupFacts(facts RemoteFacts) setup.TargetFacts {
	return setup.TargetFacts{
		OS:                   facts.OS,
		Arch:                 facts.Arch,
		Hostname:             facts.Hostname,
		UserName:             facts.User,
		HomeDir:              facts.HomeDir,
		HasSudo:              facts.HasSudo,
		HasSystemd:           facts.HasSystemd,
		HasLaunchd:           facts.HasLaunchd,
		HasNix:               facts.HasNix,
		HasGit:               facts.HasGit,
		HasGo:                facts.HasGo,
		ExistingLoom:         binaryFact("loom", facts.ExistingLoom),
		ExistingLoomd:        binaryFact("loomd", facts.ExistingLoomd),
		ExistingNodeAgent:    binaryFact("loom-node-agent", facts.ExistingNodeAgent),
		ExistingManifestPath: facts.ExistingManifestPath,
	}
}

func binaryFact(name string, path string) setup.BinaryFact {
	path = strings.TrimSpace(path)
	return setup.BinaryFact{Name: name, Path: path, Found: path != ""}
}

func planWarnings(spec Spec, facts RemoteFacts, source SourcePlan) []Warning {
	warnings := []Warning{}
	if source.Mode == SourceModeCurrentRsync && !facts.HasRsync {
		warnings = append(warnings, Warning{
			Code:    "bootstrapssh.remote_rsync_missing",
			Message: "remote target does not report rsync; current-rsync delivery may fail until rsync is installed",
		})
	}
	if source.Mode == SourceModeGitClone && !facts.HasGit {
		warnings = append(warnings, Warning{
			Code:    "bootstrapssh.remote_git_missing",
			Message: "remote target does not report git; git-clone source mode cannot run until git is installed",
		})
	}
	if strings.TrimSpace(facts.ExistingManifestPath) != "" {
		warnings = append(warnings, Warning{
			Code:    "bootstrapssh.existing_manifest",
			Message: "remote target already has a LOOM install manifest",
			Field:   facts.ExistingManifestPath,
		})
	}
	if spec.NodeKind != "main" && strings.TrimSpace(spec.MainURL) == "" {
		warnings = append(warnings, Warning{
			Code:    "bootstrapssh.main_url_missing",
			Message: "non-main setup plan will include a main_url diagnostic until --main-url or --main-host is provided",
			Field:   "main_url",
		})
	}
	return warnings
}

func mainURLFromHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if parsed, err := url.Parse(host); err == nil && parsed.Scheme != "" {
		return host
	}
	if strings.Contains(host, ":") {
		return "http://" + host
	}
	return "http://" + host + ":8080"
}

func normalizeToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
