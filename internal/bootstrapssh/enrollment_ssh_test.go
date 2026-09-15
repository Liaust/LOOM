package bootstrapssh

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/enrollmentflow"
	"loom.local/loom/internal/nodeagent"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/setup"
)

func TestSSHMainRunnerParsesEnrollmentToken(t *testing.T) {
	runner := &enrollmentCommandFakeRunner{}
	main := SSHMainRunner{Runner: runner, SocketPath: "/run/loom/loomd.sock"}
	token, err := main.CreateEnrollmentToken(context.Background(), enrollmentflow.CreateTokenInput{TTLSeconds: 60})
	if err != nil {
		t.Fatalf("CreateEnrollmentToken returned error: %v", err)
	}
	if token.TokenValue != "node_enroll_super_secret" || token.TokenID != "node_enrollment_token_test" {
		t.Fatalf("token mismatch: %#v", token)
	}
	if len(runner.commands) != 1 || !strings.Contains(runner.commands[0], "enrollment-token") || !strings.Contains(runner.commands[0], "/run/loom/loomd.sock") {
		t.Fatalf("main command mismatch: %#v", runner.commands)
	}
}

func TestSSHTargetRunnerPassesEnrollmentTokenThroughStdin(t *testing.T) {
	runner := &enrollmentCommandFakeRunner{}
	target := testSSHTargetRunner(runner)
	result, err := target.SubmitEnrollmentRequest(context.Background(), "node_enroll_super_secret")
	if err != nil {
		t.Fatalf("SubmitEnrollmentRequest returned error: %v", err)
	}
	if result.EnrollmentRequestID != "node_enrollment_request_test" {
		t.Fatalf("request id = %q", result.EnrollmentRequestID)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("commands = %#v", runner.commands)
	}
	if strings.Contains(runner.commands[0], "node_enroll_super_secret") {
		t.Fatalf("enrollment token leaked into command: %s", runner.commands[0])
	}
	if !strings.Contains(runner.commands[0], "--token-file \"$token_file\"") || runner.stdin[0] != "node_enroll_super_secret" {
		t.Fatalf("token file/stdin command mismatch: command=%s stdin=%q", runner.commands[0], runner.stdin[0])
	}
}

func TestSSHTargetRunnerPassesCredentialTokenThroughStdin(t *testing.T) {
	runner := &enrollmentCommandFakeRunner{}
	target := testSSHTargetRunner(runner)
	if err := target.ImportCredential(context.Background(), testCredentialImportInput()); err != nil {
		t.Fatalf("ImportCredential returned error: %v", err)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("commands = %#v", runner.commands)
	}
	if strings.Contains(runner.commands[0], "node_cred_super_secret") {
		t.Fatalf("credential token leaked into command: %s", runner.commands[0])
	}
	if !strings.Contains(runner.commands[0], "--from-file \"$approval_file\"") || !strings.Contains(runner.stdin[0], "node_cred_super_secret") {
		t.Fatalf("approval file/stdin command mismatch: command=%s stdin=%q", runner.commands[0], runner.stdin[0])
	}
}

func testSSHTargetRunner(runner Runner) SSHTargetRunner {
	return SSHTargetRunner{
		Runner: runner,
		SetupPlan: setup.SetupPlan{
			Spec: setup.SetupSpec{
				PackageMode: setup.PackageModeLocalBuild,
				SourcePath:  "/home/loomadmin/.local/share/loom/source/current",
			},
			Paths: setup.PathPlan{
				NodeAgentConfigPath: "/home/loomadmin/.config/loom-node-agent/config.json",
				NodeAgentStatePath:  "/home/loomadmin/.local/state/loom-node-agent/state.json",
				NodeAgentDataDir:    "/home/loomadmin/.local/state/loom-node-agent",
			},
		},
		Source: SourcePlan{RemotePath: "/home/loomadmin/.local/share/loom/source/current"},
	}
}

func testCredentialImportInput() enrollmentflow.CredentialImportInput {
	return enrollmentflow.CredentialImportInput{
		NodeID:           "node_test",
		NodeCredentialID: "node_credential_test",
		CredentialToken:  "node_cred_super_secret",
	}
}

type enrollmentCommandFakeRunner struct {
	commands []string
	stdin    []string
}

func (r *enrollmentCommandFakeRunner) Run(_ context.Context, command RemoteCommand) (RemoteResult, error) {
	r.commands = append(r.commands, command.Command)
	r.stdin = append(r.stdin, command.Stdin)
	switch {
	case strings.Contains(command.Command, "enrollment-token"):
		return RemoteResult{Stdout: mustEnrollmentJSON(response.Success("test", nodes.CreateEnrollmentTokenResult{
			Token: nodes.EnrollmentToken{
				NodeEnrollmentTokenID: "node_enrollment_token_test",
				TokenHint:             "hint",
				Status:                "active",
				ExpiresAt:             time.Unix(3600, 0),
			},
			TokenValue: "node_enroll_super_secret",
		}))}, nil
	case strings.Contains(command.Command, "enroll"):
		return RemoteResult{Stdout: mustEnrollmentJSON(response.Success("test", nodes.EnrollmentRequest{
			NodeEnrollmentRequestID: "node_enrollment_request_test",
			Status:                  "pending",
		}))}, nil
	case strings.Contains(command.Command, "credential"):
		return RemoteResult{Stdout: mustEnrollmentJSON(response.Success("test", nodeagent.Status{
			NodeID:               "node_test",
			NodeCredentialID:     "node_credential_test",
			CredentialConfigured: true,
		}))}, nil
	default:
		return RemoteResult{}, nil
	}
}

func (r *enrollmentCommandFakeRunner) CopyTo(context.Context, string, string, CopyOptions) error {
	return nil
}

func mustEnrollmentJSON(value any) string {
	payload, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(payload)
}
