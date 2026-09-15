package bootstrapssh

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/enrollmentflow"
	"loom.local/loom/internal/setup"
)

func TestDefaultRemoteSourceDir(t *testing.T) {
	facts := RemoteFacts{HomeDir: "/home/loomadmin"}
	tests := []struct {
		name string
		spec Spec
		want string
	}{
		{name: "main", spec: Spec{NodeKind: "main", InstallMode: "service"}, want: "/srv/loom/current"},
		{name: "workspace", spec: Spec{NodeKind: "workspace", InstallMode: "user"}, want: "/home/loomadmin/.local/share/loom/source/current"},
		{name: "hardware", spec: Spec{NodeKind: "hardware", InstallMode: "user"}, want: "/home/loomadmin/.local/share/loom/source/current"},
		{name: "developer", spec: Spec{NodeKind: "workspace", InstallMode: "developer"}, want: "/home/loomadmin/loom/current"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DefaultRemoteSourceDir(tc.spec, facts); got != tc.want {
				t.Fatalf("default remote source = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTargetFromSpecParsesUserAtHost(t *testing.T) {
	target := targetFromSpec(Spec{TargetHost: "loomadmin@loom-workspace", SSHPort: 2222, SSHKeyPath: "/tmp/key"})
	if target.User != "loomadmin" || target.Host != "loom-workspace" || target.Port != 2222 || !target.KeyPathConfigured {
		t.Fatalf("target parse mismatch: %#v", target)
	}
}

func TestBuildSetupSpecForWorkspace(t *testing.T) {
	source := SourcePlan{RemotePath: "/home/loomadmin/.local/share/loom/source/current"}
	spec := Spec{
		NodeKind:     "workspace",
		NodeRole:     "primary_workspace",
		RuntimeClass: "workspace_full",
		NodeKey:      "loom-workspace",
		DisplayName:  "LOOM Workspace",
		MainURL:      "http://10.44.0.2:8080",
		InstallMode:  "user",
		PackageMode:  "local_build",
	}
	setupSpec := BuildSetupSpec(spec, defaultFacts(), source)
	if setupSpec.NodeKind != "workspace" || setupSpec.NodeRole != "primary_workspace" || setupSpec.RuntimeClass != "workspace_full" {
		t.Fatalf("setup spec node mismatch: %#v", setupSpec)
	}
	if setupSpec.HomeDir != "/home/loomadmin" || setupSpec.SourcePath != source.RemotePath || setupSpec.MainURL == "" {
		t.Fatalf("setup spec path/main mismatch: %#v", setupSpec)
	}
}

func TestBuildSetupSpecUsesExplicitInstallerPaths(t *testing.T) {
	source := SourcePlan{RemotePath: "/tmp/loom-src"}
	spec := Spec{
		NodeKind:          "main",
		NodeRole:          "main",
		RuntimeClass:      "main_full",
		NodeKey:           "main",
		DisplayName:       "Main",
		InstallMode:       "user",
		ServiceManager:    "none",
		BoxPath:           "/tmp/loom-box",
		BoxProfile:        "main",
		HomeDir:           "/tmp/home",
		UserName:          "loomadmin",
		ConfigDir:         "/tmp/config",
		DataDir:           "/tmp/data",
		StateDir:          "/tmp/state",
		LogDir:            "/tmp/logs",
		ServiceRoot:       "/srv/loom",
		StorageRoot:       "/srv/loom/storage",
		ImportsRoot:       "/srv/loom/storage/imports",
		UserBackupsRoot:   "/srv/loom/storage/backups",
		ArchiveRoot:       "/srv/loom/storage/archive",
		GeneratedRoot:     "/tmp/data/generated",
		BoxStateRoot:      "/tmp/data/box-state",
		ObjectStorePath:   "/tmp/object-store",
		MainDocumentsPath: "/tmp/legacy-main-documents",
		StorageExportRoot: "/tmp/storage-export",
		SocketPath:        "/tmp/run/loomd.sock",
		HTTPListenAddr:    "127.0.0.1:8080",
		DBURL:             "user=loom dbname=loom host=/run/postgresql sslmode=disable",
		MigrationsDir:     "/tmp/migrations",
		BootstrapMode:     "production",
	}
	setupSpec := BuildSetupSpec(spec, defaultFacts(), source)
	if setupSpec.HomeDir != "/tmp/home" || setupSpec.UserName != "loomadmin" || setupSpec.ConfigDir != "/tmp/config" || setupSpec.DataDir != "/tmp/data" {
		t.Fatalf("explicit path fields were not propagated: %#v", setupSpec)
	}
	if setupSpec.ObjectStorePath != "/tmp/object-store" || setupSpec.StorageExportRoot != "" || setupSpec.SocketPath != "/tmp/run/loomd.sock" || setupSpec.DBURL == "" || setupSpec.BootstrapMode != "production" {
		t.Fatalf("explicit main runtime fields were not propagated: %#v", setupSpec)
	}
	if setupSpec.ServiceRoot != "/srv/loom" || setupSpec.StorageRoot != "/srv/loom/storage" || setupSpec.ImportsRoot != "/srv/loom/storage/imports" || setupSpec.UserBackupsRoot != "/srv/loom/storage/backups" || setupSpec.ArchiveRoot != "/srv/loom/storage/archive" || setupSpec.GeneratedRoot != "/tmp/data/generated" || setupSpec.BoxStateRoot != "/tmp/data/box-state" || setupSpec.MainDocumentsPath != "/tmp/legacy-main-documents" {
		t.Fatalf("canonical filesystem roots were not propagated: %#v", setupSpec)
	}
}

func TestRunDryRunBuildsMainSetupPlan(t *testing.T) {
	runner := &bootstrapSSHFakeRunner{facts: defaultPreflightOutput()}
	result, err := Run(context.Background(), RunInput{
		Spec: Spec{
			TargetHost:   "loom-main",
			NodeKind:     "main",
			NodeRole:     "main",
			RuntimeClass: "main_full",
			NodeKey:      "main",
			DryRun:       true,
		},
		Runner: runner,
	})
	if err != nil {
		t.Fatalf("Run main dry-run returned error: %v", err)
	}
	spec := result.Plan.SetupPlan.Spec
	if spec.NodeKind != "main" || spec.EnableLoomd != true || spec.EnableNodeAgent {
		t.Fatalf("main setup plan mismatch: %#v", spec)
	}
	if result.Plan.Source.RemotePath != "/srv/loom/current" {
		t.Fatalf("main source path = %q", result.Plan.Source.RemotePath)
	}
}

func TestRunDryRunBuildsHardwareSetupPlan(t *testing.T) {
	runner := &bootstrapSSHFakeRunner{facts: defaultPreflightOutput()}
	result, err := Run(context.Background(), RunInput{
		Spec: Spec{
			TargetHost:   "gpu-box",
			NodeKind:     "hardware",
			NodeRole:     "capability_node",
			RuntimeClass: "hardware_agent",
			NodeKey:      "gpu-box",
			MainHost:     "loom-main",
			DryRun:       true,
		},
		Runner: runner,
	})
	if err != nil {
		t.Fatalf("Run hardware dry-run returned error: %v", err)
	}
	spec := result.Plan.SetupPlan.Spec
	if spec.NodeKind != "hardware" || spec.EnableBox || !spec.EnableNodeAgent || spec.MainURL != "http://loom-main:8080" {
		t.Fatalf("hardware setup plan mismatch: %#v", spec)
	}
}

func TestRunDryRunDoesNotCopy(t *testing.T) {
	runner := &bootstrapSSHFakeRunner{facts: defaultPreflightOutput()}
	result, err := Run(context.Background(), RunInput{
		Spec: Spec{
			TargetHost:   "loom-workspace",
			NodeKind:     "workspace",
			NodeRole:     "primary_workspace",
			RuntimeClass: "workspace_full",
			NodeKey:      "loom-workspace",
			MainURL:      "http://10.44.0.2:8080",
			SourcePath:   ".",
			DryRun:       true,
		},
		Runner: runner,
		Now:    func() time.Time { return time.Unix(100, 0) },
	})
	if err != nil {
		t.Fatalf("Run dry-run returned error: %v", err)
	}
	if runner.copyCalls != 0 || result.SourceCopied {
		t.Fatalf("dry-run copied source: calls=%d result=%#v", runner.copyCalls, result)
	}
	if result.Plan.SetupPlan.Spec.NodeKind != "workspace" || !result.Plan.WouldRunRemoteSetup {
		t.Fatalf("unexpected dry-run plan: %#v", result.Plan)
	}
}

func TestRunApplyCopiesCurrentSource(t *testing.T) {
	root := t.TempDir()
	runner := &bootstrapSSHFakeRunner{facts: defaultPreflightOutput()}
	result, err := Run(context.Background(), RunInput{
		Spec: Spec{
			TargetHost: "loom-workspace",
			NodeKind:   "workspace",
			MainURL:    "http://10.44.0.2:8080",
			SourcePath: root,
			Yes:        true,
		},
		Runner:                 runner,
		EnrollmentMainRunner:   &bootstrapEnrollmentMainFake{},
		EnrollmentTargetRunner: &bootstrapEnrollmentTargetFake{},
	})
	if err != nil {
		t.Fatalf("Run apply returned error: %v", err)
	}
	if runner.copyCalls != 1 || !result.SourceCopied {
		t.Fatalf("apply did not copy source: calls=%d result=%#v", runner.copyCalls, result)
	}
	if !result.SetupSpecCopied || result.RemoteApply == nil || result.RemoteStatus == nil || result.RemoteDoctor == nil || result.Status != StatusApplied {
		t.Fatalf("remote setup did not complete: %#v", result)
	}
	if result.Enrollment == nil || result.Enrollment.Status != enrollmentflow.StateVerifiedOnMain {
		t.Fatalf("non-main bootstrap did not enroll node: %#v", result.Enrollment)
	}
	if runner.copiedLocal != filepath.Clean(root) {
		t.Fatalf("copied local = %q, want %q", runner.copiedLocal, filepath.Clean(root))
	}
}

func TestRunApplyRequiresYes(t *testing.T) {
	_, err := Run(context.Background(), RunInput{Spec: Spec{TargetHost: "loom-workspace"}})
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("expected --yes refusal, got %v", err)
	}
}

func TestRunReportsExistingManifestWarning(t *testing.T) {
	runner := &bootstrapSSHFakeRunner{facts: defaultPreflightOutput() + "existing_manifest_path=/home/loomadmin/.config/loom/install.yaml\n"}
	result, err := Run(context.Background(), RunInput{
		Spec:   Spec{TargetHost: "loom-workspace", NodeKind: "workspace", MainURL: "http://main:8080", DryRun: true},
		Runner: runner,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !hasWarning(result.Warnings, "bootstrapssh.existing_manifest") {
		t.Fatalf("missing existing manifest warning: %#v", result.Warnings)
	}
}

func TestRunRedactsSSHKeyPathFromResult(t *testing.T) {
	runner := &bootstrapSSHFakeRunner{facts: defaultPreflightOutput()}
	result, err := Run(context.Background(), RunInput{
		Spec:   Spec{TargetHost: "loom-workspace", SSHKeyPath: "/Users/me/.ssh/loom", NodeKind: "workspace", MainURL: "http://main:8080", DryRun: true},
		Runner: runner,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Plan.Spec.SSHKeyPath != "" || !result.Plan.Target.KeyPathConfigured {
		t.Fatalf("SSH key path was not redacted/preserved correctly: %#v", result.Plan)
	}
	if RedactString("using /Users/me/.ssh/loom", Spec{SSHKeyPath: "/Users/me/.ssh/loom"}) != "using [redacted-ssh-key-path]" {
		t.Fatalf("RedactString did not hide SSH key path")
	}
}

func TestRunCopiesRemoteSetupSpec(t *testing.T) {
	runner := &bootstrapSSHFakeRunner{facts: defaultPreflightOutput()}
	result, err := Run(context.Background(), RunInput{
		Spec:   Spec{TargetHost: "loom-main", NodeKind: "main", NodeRole: "main", RuntimeClass: "main_full", NodeKey: "main", SourcePath: t.TempDir(), Yes: true},
		Runner: runner,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !strings.HasPrefix(result.Plan.RemoteSetup.SpecPath, "/tmp/loom-setup-") || !strings.HasSuffix(result.Plan.RemoteSetup.SpecPath, ".yaml") {
		t.Fatalf("unexpected remote setup spec path: %q", result.Plan.RemoteSetup.SpecPath)
	}
	if !strings.Contains(runner.specPayload, "node_kind: main") || strings.Contains(runner.specPayload, "credential_token") {
		t.Fatalf("unexpected remote spec payload:\n%s", runner.specPayload)
	}
}

func TestRemoteCommandsUseNixRunAndBuildRequiredPackages(t *testing.T) {
	runner := &bootstrapSSHFakeRunner{facts: defaultPreflightOutput()}
	result, err := Run(context.Background(), RunInput{
		Spec:   Spec{TargetHost: "loom-main", NodeKind: "main", NodeRole: "main", RuntimeClass: "main_full", NodeKey: "main", SourcePath: t.TempDir(), Yes: true},
		Runner: runner,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	build := result.Plan.RemoteSetup.BuildCommand
	apply := result.Plan.RemoteSetup.ApplyCommand
	if !strings.Contains(build, "nix") || !strings.Contains(build, ".#loom") || !strings.Contains(build, ".#loomd") {
		t.Fatalf("nix build command missing required packages: %s", build)
	}
	if !strings.Contains(apply, "nix") || !strings.Contains(apply, "run .#loom --") || !strings.Contains(apply, "setup apply") || !strings.Contains(apply, "--yes") {
		t.Fatalf("remote setup apply command unexpected: %s", apply)
	}
}

func TestHardwareRemoteBuildStillIncludesLoomForSetupApply(t *testing.T) {
	runner := &bootstrapSSHFakeRunner{facts: defaultPreflightOutput()}
	result, err := Run(context.Background(), RunInput{
		Spec:                   Spec{TargetHost: "gpu-box", NodeKind: "hardware", NodeRole: "capability_node", RuntimeClass: "hardware_agent", NodeKey: "gpu-box", MainURL: "http://main:8080", SourcePath: t.TempDir(), Yes: true},
		Runner:                 runner,
		EnrollmentMainRunner:   &bootstrapEnrollmentMainFake{},
		EnrollmentTargetRunner: &bootstrapEnrollmentTargetFake{},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	build := result.Plan.RemoteSetup.BuildCommand
	if !strings.Contains(build, ".#loom") || !strings.Contains(build, ".#loom-node-agent") {
		t.Fatalf("hardware build should include loom for setup apply and node-agent for runtime: %s", build)
	}
}

func TestRunEnrollmentFailureReturnsStructuredError(t *testing.T) {
	runner := &bootstrapSSHFakeRunner{facts: defaultPreflightOutput()}
	_, err := Run(context.Background(), RunInput{
		Spec:                   Spec{TargetHost: "loom-workspace", NodeKind: "workspace", MainURL: "http://main:8080", SourcePath: t.TempDir(), Yes: true},
		Runner:                 runner,
		EnrollmentMainRunner:   &bootstrapEnrollmentMainFake{approveErr: errors.New("approval failed node_cred_secret")},
		EnrollmentTargetRunner: &bootstrapEnrollmentTargetFake{},
	})
	var bootstrapErr BootstrapError
	if !errors.As(err, &bootstrapErr) {
		t.Fatalf("expected bootstrap error, got %#v", err)
	}
	if strings.Contains(err.Error(), "node_cred_secret") {
		t.Fatalf("enrollment error leaked credential secret: %v", err)
	}
}

func TestRunEnrollmentResumesExistingCredentialByDefault(t *testing.T) {
	runner := &bootstrapSSHFakeRunner{facts: defaultPreflightOutput()}
	mainRunner := &bootstrapEnrollmentMainFake{}
	targetRunner := &bootstrapEnrollmentTargetFake{
		resume: enrollmentflow.ResumeState{
			Status:              enrollmentflow.StateHeartbeatVerified,
			EnrollmentRequestID: "node_enrollment_request_test",
			NodeID:              "node_test",
			NodeCredentialID:    "node_credential_test",
			CredentialImported:  true,
			CredentialTokenHeld: true,
			HeartbeatID:         "node_heartbeat_test",
		},
	}
	result, err := Run(context.Background(), RunInput{
		Spec:                   Spec{TargetHost: "loom-workspace", NodeKind: "workspace", MainURL: "http://main:8080", SourcePath: t.TempDir(), Yes: true},
		Runner:                 runner,
		EnrollmentMainRunner:   mainRunner,
		EnrollmentTargetRunner: targetRunner,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Enrollment == nil || result.Enrollment.Status != enrollmentflow.StateVerifiedOnMain {
		t.Fatalf("expected verified enrollment result, got %#v", result.Enrollment)
	}
	if mainRunner.createCalls != 0 || mainRunner.approveCalls != 0 || targetRunner.requestCalls != 0 || targetRunner.importCalls != 0 || targetRunner.heartbeatCalls != 0 {
		t.Fatalf("resume should not redo token/request/import/heartbeat: main=%#v target=%#v", mainRunner, targetRunner)
	}
	if mainRunner.healthCalls != 1 {
		t.Fatalf("resume should still verify main health, calls=%d", mainRunner.healthCalls)
	}
}

func TestRunInvalidRemoteJSONReturnsStructuredError(t *testing.T) {
	runner := &bootstrapSSHFakeRunner{facts: defaultPreflightOutput(), invalidApplyJSON: true}
	_, err := Run(context.Background(), RunInput{
		Spec:   Spec{TargetHost: "loom-main", NodeKind: "main", NodeRole: "main", RuntimeClass: "main_full", NodeKey: "main", SourcePath: t.TempDir(), Yes: true},
		Runner: runner,
	})
	var bootstrapErr BootstrapError
	if !errors.As(err, &bootstrapErr) || bootstrapErr.Code != "bootstrap.setup.remote_invalid_json" {
		t.Fatalf("expected invalid json bootstrap error, got %#v", err)
	}
}

func TestRunRemoteFailureReturnsStructuredError(t *testing.T) {
	runner := &bootstrapSSHFakeRunner{facts: defaultPreflightOutput(), applyErr: errors.New("remote apply failed")}
	_, err := Run(context.Background(), RunInput{
		Spec:   Spec{TargetHost: "loom-main", NodeKind: "main", NodeRole: "main", RuntimeClass: "main_full", NodeKey: "main", SourcePath: t.TempDir(), Yes: true},
		Runner: runner,
	})
	var bootstrapErr BootstrapError
	if !errors.As(err, &bootstrapErr) || bootstrapErr.Code != "bootstrap.setup.remote_failed" {
		t.Fatalf("expected remote failed bootstrap error, got %#v", err)
	}
}

func TestRunPropagatesCopyFailure(t *testing.T) {
	runner := &bootstrapSSHFakeRunner{facts: defaultPreflightOutput(), copyErr: errors.New("copy failed")}
	_, err := Run(context.Background(), RunInput{
		Spec:   Spec{TargetHost: "loom-workspace", NodeKind: "workspace", MainURL: "http://main:8080", SourcePath: t.TempDir(), Yes: true},
		Runner: runner,
	})
	if err == nil || !strings.Contains(err.Error(), "copy failed") {
		t.Fatalf("expected copy failure, got %v", err)
	}
}

func defaultFacts() RemoteFacts {
	return RemoteFacts{
		Hostname:   "loom-workspace",
		OS:         "linux",
		Arch:       "amd64",
		User:       "loomadmin",
		UID:        "1000",
		HomeDir:    "/home/loomadmin",
		HasSudo:    true,
		HasSystemd: true,
		HasNix:     true,
		HasGit:     true,
		HasRsync:   true,
		HasTar:     true,
	}
}

func defaultPreflightOutput() string {
	return strings.Join([]string{
		"hostname=loom-workspace",
		"os=linux",
		"arch=amd64",
		"user=loomadmin",
		"uid=1000",
		"home_dir=/home/loomadmin",
		"has_sudo=true",
		"has_systemd=true",
		"has_launchd=false",
		"has_nix=true",
		"has_git=true",
		"has_go=true",
		"has_rsync=true",
		"has_tar=true",
		"existing_loom=",
		"existing_loomd=",
		"existing_node_agent=",
		"existing_manifest_path=",
		"",
	}, "\n")
}

type bootstrapSSHFakeRunner struct {
	facts            string
	copyErr          error
	applyErr         error
	invalidApplyJSON bool
	copyCalls        int
	copiedLocal      string
	copiedRemote     string
	specPayload      string
	commands         []string
}

func (r *bootstrapSSHFakeRunner) Run(_ context.Context, command RemoteCommand) (RemoteResult, error) {
	r.commands = append(r.commands, command.Command)
	switch {
	case strings.Contains(command.Command, "kv hostname"):
		return RemoteResult{Stdout: r.facts}, nil
	case strings.Contains(command.Command, "cat >"):
		r.specPayload = command.Stdin
		return RemoteResult{}, nil
	case strings.Contains(command.Command, "nix") && strings.Contains(command.Command, " build "):
		return RemoteResult{Stdout: "built\n"}, nil
	case strings.Contains(command.Command, "go build"):
		return RemoteResult{Stdout: "built\n"}, nil
	case strings.Contains(command.Command, "setup apply"):
		if r.invalidApplyJSON {
			return RemoteResult{Stdout: "{not-json"}, nil
		}
		if r.applyErr != nil {
			return RemoteResult{Stdout: mustJSONForBootstrapSSHTest(setup.ApplyResult{PlanID: "setup_plan_test"}), Stderr: "apply stderr", ExitCode: 1}, r.applyErr
		}
		return RemoteResult{Stdout: mustJSONForBootstrapSSHTest(setup.ApplyResult{PlanID: "setup_plan_test"})}, nil
	case strings.Contains(command.Command, "record-enrollment"):
		return RemoteResult{Stdout: mustJSONForBootstrapSSHTest(setup.RecordEnrollmentResult{Manifest: setup.InstallManifest{Enrollment: setup.EnrollmentManifest{Status: "approved"}}})}, nil
	case strings.Contains(command.Command, "setup status"):
		return RemoteResult{Stdout: mustJSONForBootstrapSSHTest(setup.SetupStatus{Summary: setup.SetupStatusSummary{Status: setup.SummaryConfigured}})}, nil
	case strings.Contains(command.Command, "setup doctor"):
		return RemoteResult{Stdout: mustJSONForBootstrapSSHTest(setup.DoctorReport{Summary: "ok"})}, nil
	default:
		return RemoteResult{}, nil
	}
}

func (r *bootstrapSSHFakeRunner) CopyTo(_ context.Context, localPath string, remotePath string, _ CopyOptions) error {
	r.copyCalls++
	r.copiedLocal = filepath.Clean(localPath)
	r.copiedRemote = remotePath
	return r.copyErr
}

func hasWarning(warnings []Warning, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}

func mustJSONForBootstrapSSHTest(value any) string {
	payload, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(payload)
}

type bootstrapEnrollmentMainFake struct {
	createErr    error
	approveErr   error
	healthErr    error
	createCalls  int
	approveCalls int
	healthCalls  int
}

func (f *bootstrapEnrollmentMainFake) CreateEnrollmentToken(context.Context, enrollmentflow.CreateTokenInput) (enrollmentflow.CreateTokenResult, error) {
	f.createCalls++
	if f.createErr != nil {
		return enrollmentflow.CreateTokenResult{}, f.createErr
	}
	return enrollmentflow.CreateTokenResult{TokenID: "node_enrollment_token_test", TokenHint: "hint", TokenValue: "node_enroll_secret", ExpiresAt: time.Unix(3600, 0)}, nil
}

func (f *bootstrapEnrollmentMainFake) ApproveEnrollment(context.Context, string) (enrollmentflow.ApprovalResult, error) {
	f.approveCalls++
	if f.approveErr != nil {
		return enrollmentflow.ApprovalResult{}, f.approveErr
	}
	return enrollmentflow.ApprovalResult{
		EnrollmentRequestID: "node_enrollment_request_test",
		NodeID:              "node_test",
		NodeCredentialID:    "node_credential_test",
		CredentialHint:      "hint",
		CredentialToken:     "node_cred_secret",
		ApprovedAt:          time.Unix(3700, 0),
	}, nil
}

func (f *bootstrapEnrollmentMainFake) GetNodeHealth(context.Context, string) (enrollmentflow.NodeHealthResult, error) {
	f.healthCalls++
	if f.healthErr != nil {
		return enrollmentflow.NodeHealthResult{}, f.healthErr
	}
	seenAt := time.Unix(3800, 0)
	return enrollmentflow.NodeHealthResult{NodeID: "node_test", NodeKey: "loom-workspace", PresenceState: "online", HeartbeatID: "node_heartbeat_test", LastSeenAt: &seenAt}, nil
}

type bootstrapEnrollmentTargetFake struct {
	requestErr     error
	importErr      error
	heartbeatErr   error
	resume         enrollmentflow.ResumeState
	requestCalls   int
	importCalls    int
	heartbeatCalls int
}

func (f *bootstrapEnrollmentTargetFake) EnsureNodeAgentInitialized(context.Context, enrollmentflow.NodeAgentInitInput) error {
	return nil
}

func (f *bootstrapEnrollmentTargetFake) SubmitEnrollmentRequest(context.Context, string) (enrollmentflow.EnrollmentRequestResult, error) {
	f.requestCalls++
	if f.requestErr != nil {
		return enrollmentflow.EnrollmentRequestResult{}, f.requestErr
	}
	return enrollmentflow.EnrollmentRequestResult{EnrollmentRequestID: "node_enrollment_request_test", Status: "pending"}, nil
}

func (f *bootstrapEnrollmentTargetFake) ImportCredential(context.Context, enrollmentflow.CredentialImportInput) error {
	f.importCalls++
	return f.importErr
}

func (f *bootstrapEnrollmentTargetFake) HeartbeatOnce(context.Context) (enrollmentflow.HeartbeatResult, error) {
	f.heartbeatCalls++
	if f.heartbeatErr != nil {
		return enrollmentflow.HeartbeatResult{}, f.heartbeatErr
	}
	return enrollmentflow.HeartbeatResult{HeartbeatID: "node_heartbeat_test", NodeID: "node_test", PresenceState: "online", ReceivedAt: time.Unix(3800, 0)}, nil
}

func (f *bootstrapEnrollmentTargetFake) LoadResumeState(context.Context) (enrollmentflow.ResumeState, error) {
	if f.resume.Status != "" {
		return f.resume, nil
	}
	return enrollmentflow.ResumeState{Status: enrollmentflow.StateNotStarted}, nil
}
