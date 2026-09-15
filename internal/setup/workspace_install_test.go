package setup

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/nodeagent"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/response"
)

func TestRunWorkspaceInstallStagesReleaseAndEnrolls(t *testing.T) {
	source := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	approvedAt := time.Unix(1234, 0).UTC()
	receivedAt := time.Unix(4321, 0).UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/node-enrollment-tokens":
			response.WriteJSON(w, http.StatusCreated, response.Success("test", nodes.CreateEnrollmentTokenResult{
				Token: nodes.EnrollmentToken{
					NodeEnrollmentTokenID: "node_enrollment_token_test",
					TokenHint:             "hint",
					Status:                nodes.EnrollmentTokenActive,
					ExpiresAt:             time.Unix(3600, 0).UTC(),
				},
				TokenValue: "node_enroll_super_secret",
			}))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/node-agent/enroll":
			response.WriteJSON(w, http.StatusCreated, response.Success("test", nodes.EnrollmentRequest{
				NodeEnrollmentRequestID: "node_enrollment_request_test",
				Status:                  nodes.EnrollmentRequestPending,
			}))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/node-enrollment-requests/node_enrollment_request_test/approve":
			response.WriteJSON(w, http.StatusOK, response.Success("test", nodes.ApproveEnrollmentResult{
				Request: nodes.EnrollmentRequest{
					NodeEnrollmentRequestID: "node_enrollment_request_test",
					Status:                  nodes.EnrollmentRequestApproved,
					ApprovedAt:              &approvedAt,
				},
				Node: nodes.Node{
					NodeID:        "node_test",
					NodeKey:       "macbook",
					PresenceState: nodes.PresenceOnline,
				},
				Credential: nodes.NodeCredential{
					NodeCredentialID: "node_credential_test",
					NodeID:           "node_test",
					CredentialHint:   "cred_hint",
					Status:           nodes.NodeCredentialActive,
				},
				CredentialToken: "node_cred_super_secret",
			}))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/node-agent/heartbeat":
			response.WriteJSON(w, http.StatusOK, response.Success("test", nodes.Heartbeat{
				NodeHeartbeatID: "node_heartbeat_test",
				NodeID:          "node_test",
				PresenceState:   nodes.PresenceOnline,
				ReportedStatus:  "ok",
				ReceivedAt:      receivedAt,
			}))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/nodes/node_test/health":
			response.WriteJSON(w, http.StatusOK, response.Success("test", nodes.NodeHealth{
				Node: nodes.Node{
					NodeID:        "node_test",
					NodeKey:       "macbook",
					PresenceState: nodes.PresenceOnline,
				},
				LastHeartbeat: &nodes.Heartbeat{
					NodeHeartbeatID: "node_heartbeat_test",
					NodeID:          "node_test",
					PresenceState:   nodes.PresenceOnline,
					ReceivedAt:      receivedAt,
				},
			}))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	result, err := RunWorkspaceInstall(context.Background(), WorkspaceInstallInput{
		NodeKey:        "macbook",
		DisplayName:    "MacBook Primary Workspace",
		MainURL:        server.URL,
		HomeDir:        home,
		SourcePath:     source,
		ReleaseID:      "release-test",
		Yes:            true,
		Resume:         true,
		BuildRunner:    fakeWorkspaceBuildRunner{},
		LaunchdRunner:  fakeWorkspaceLaunchdRunner{},
		ServiceManager: ServiceManagerLaunchd,
	})
	if err != nil {
		t.Fatalf("RunWorkspaceInstall returned error: %v", err)
	}
	if result.Status != "applied" {
		t.Fatalf("status = %q, result=%#v", result.Status, result)
	}
	if result.Enrollment == nil || result.Enrollment.Status != "verified_on_main" || !result.Enrollment.VerifiedOnMain {
		t.Fatalf("enrollment result unexpected: %#v", result.Enrollment)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "share", "loom", "releases", "release-test", "bin", "loom")); err != nil {
		t.Fatalf("loom binary was not staged: %v", err)
	}
	if target, err := os.Readlink(filepath.Join(home, ".local", "share", "loom", "current")); err != nil || target != result.Plan.Release.ReleasePath {
		t.Fatalf("current symlink = %q err=%v", target, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "loom", "install.yaml")); err != nil {
		t.Fatalf("install manifest was not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "loom-box", ".loom", "state")); !os.IsNotExist(err) {
		t.Fatalf("workspace Box must not contain volatile runtime state, stat err=%v", err)
	}
	if _, err := os.Stat(result.Plan.Setup.Paths.BoxStateRoot); err != nil {
		t.Fatalf("external Box runtime-state root missing: %v", err)
	}
	store := nodeagent.Store{
		ConfigPath: filepath.Join(home, ".config", "loom-node-agent", "config.json"),
		StatePath:  filepath.Join(home, ".local", "state", "loom-node-agent", "state.json"),
		DataDir:    filepath.Join(home, ".local", "state", "loom-node-agent"),
	}
	state, err := store.LoadState()
	if err != nil {
		t.Fatalf("load node-agent state: %v", err)
	}
	if state.NodeID != "node_test" || state.CredentialToken != "node_cred_super_secret" {
		t.Fatalf("node-agent state credential mismatch: %#v", state)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if strings.Contains(string(payload), "node_enroll_super_secret") || strings.Contains(string(payload), "node_cred_super_secret") {
		t.Fatalf("workspace install result leaked enrollment secrets: %s", string(payload))
	}
}

func TestPlanWorkspaceInstallDerivesMainURLFromMainHost(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	plan, err := PlanWorkspaceInstall(WorkspaceInstallInput{
		MainHost:   "loom-main",
		MainURL:    "",
		HomeDir:    home,
		SourcePath: t.TempDir(),
		ReleaseID:  "release-test",
	})
	if err != nil {
		t.Fatalf("PlanWorkspaceInstall returned error: %v", err)
	}
	if plan.Setup.Spec.MainURL != "http://loom-main:8080" {
		t.Fatalf("main URL = %q", plan.Setup.Spec.MainURL)
	}
}

type fakeWorkspaceBuildRunner struct{}

func (fakeWorkspaceBuildRunner) Run(_ context.Context, _ string, _ string, args ...string) error {
	for i, arg := range args {
		if arg == "-o" && i+1 < len(args) {
			if err := os.MkdirAll(filepath.Dir(args[i+1]), 0o755); err != nil {
				return err
			}
			return os.WriteFile(args[i+1], []byte("#!/bin/sh\n"), 0o755)
		}
	}
	return nil
}

type fakeWorkspaceLaunchdRunner struct{}

func (fakeWorkspaceLaunchdRunner) Launchctl(_ context.Context, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "print" {
		return "pid = 123\n", nil
	}
	return "", nil
}
