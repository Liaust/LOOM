package enrollmentflow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRunFullFlowSucceeds(t *testing.T) {
	t.Parallel()

	main := newFakeMainRunner()
	target := newFakeTargetRunner()
	result, err := Run(context.Background(), testSpec(), main, target)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Status != StateVerifiedOnMain || !result.VerifiedOnMain {
		t.Fatalf("result status unexpected: %#v", result)
	}
	if result.EnrollmentRequestID != "node_enrollment_request_test" || result.NodeID != "node_test" || result.NodeCredentialID != "node_credential_test" {
		t.Fatalf("result identifiers unexpected: %#v", result)
	}
	if result.CredentialHint != "hintcred" || result.HeartbeatID != "node_heartbeat_test" || result.PresenceState != "online" {
		t.Fatalf("result health unexpected: %#v", result)
	}
	if result.Redacted != true {
		t.Fatalf("result should be marked redacted: %#v", result)
	}
	if target.importedToken != "node_cred_secret" {
		t.Fatalf("credential token was not passed to target import")
	}
}

func TestRunFailuresAreCapturedAndRedacted(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name      string
		configure func(*fakeMainRunner, *fakeTargetRunner)
		wantCode  string
	}{
		{name: "token", configure: func(m *fakeMainRunner, _ *fakeTargetRunner) {
			m.createErr = errors.New("token=node_enroll_secret failed")
		}, wantCode: FailureTokenCreate},
		{name: "request", configure: func(_ *fakeMainRunner, t *fakeTargetRunner) {
			t.submitErr = errors.New("submit failed for token=node_enroll_secret")
		}, wantCode: FailureRequestSubmit},
		{name: "approval", configure: func(m *fakeMainRunner, _ *fakeTargetRunner) {
			m.approveErr = errors.New("approval credential=node_cred_secret failed")
		}, wantCode: FailureApproval},
		{name: "credential_import", configure: func(_ *fakeMainRunner, t *fakeTargetRunner) {
			t.importErr = errors.New("import credential_token=node_cred_secret failed")
		}, wantCode: FailureCredentialImport},
		{name: "heartbeat", configure: func(_ *fakeMainRunner, t *fakeTargetRunner) { t.heartbeatErr = errors.New("heartbeat failed") }, wantCode: FailureHeartbeat},
		{name: "main_verify", configure: func(m *fakeMainRunner, _ *fakeTargetRunner) { m.healthErr = errors.New("verify failed") }, wantCode: FailureMainVerification},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			main := newFakeMainRunner()
			target := newFakeTargetRunner()
			tt.configure(main, target)
			result, _ := Run(context.Background(), testSpec(), main, target)
			if result.Status != StateFailed || result.FailureCode != tt.wantCode {
				t.Fatalf("result = %#v, want failure %s", result, tt.wantCode)
			}
			if strings.Contains(result.FailureMessage, "node_enroll_secret") || strings.Contains(result.FailureMessage, "node_cred_secret") {
				t.Fatalf("failure was not redacted: %#v", result)
			}
		})
	}
}

func TestRunResumeFromPendingRequest(t *testing.T) {
	t.Parallel()

	main := newFakeMainRunner()
	target := newFakeTargetRunner()
	spec := testSpec()
	spec.Resume = true
	spec.State = ResumeState{Status: StateRequestSubmitted, EnrollmentRequestID: "node_enrollment_request_test"}
	result, err := Run(context.Background(), spec, main, target)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Status != StateVerifiedOnMain {
		t.Fatalf("status = %q", result.Status)
	}
	if main.createCalls != 0 || target.submitCalls != 0 {
		t.Fatalf("resume should skip token/request: main=%d target=%d", main.createCalls, target.submitCalls)
	}
	if main.approveCalls != 1 {
		t.Fatalf("resume should approve pending request, approve calls=%d", main.approveCalls)
	}
}

func TestRunResumeFromCredentialImported(t *testing.T) {
	t.Parallel()

	main := newFakeMainRunner()
	target := newFakeTargetRunner()
	spec := testSpec()
	spec.Resume = true
	spec.State = ResumeState{
		Status:              StateCredentialImported,
		EnrollmentRequestID: "node_enrollment_request_test",
		NodeID:              "node_test",
		NodeCredentialID:    "node_credential_test",
		CredentialImported:  true,
	}
	result, err := Run(context.Background(), spec, main, target)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Status != StateVerifiedOnMain || target.heartbeatCalls != 1 {
		t.Fatalf("resume from credential imported unexpected: %#v heartbeat_calls=%d", result, target.heartbeatCalls)
	}
	if main.createCalls != 0 || target.submitCalls != 0 || main.approveCalls != 0 || target.importCalls != 0 {
		t.Fatalf("resume should skip early steps main=%#v target=%#v", main, target)
	}
}

func TestRunApprovedWithoutCredentialTokenIsUnrecoverable(t *testing.T) {
	t.Parallel()

	spec := testSpec()
	spec.Resume = true
	spec.State = ResumeState{
		Status:              StateApproved,
		EnrollmentRequestID: "node_enrollment_request_test",
		NodeID:              "node_test",
		NodeCredentialID:    "node_credential_test",
		CredentialHint:      "hintcred",
	}
	result, err := Run(context.Background(), spec, newFakeMainRunner(), newFakeTargetRunner())
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Status != StateUnrecoverable || result.FailureCode != FailureCredentialTokenUnavailable {
		t.Fatalf("result = %#v", result)
	}
}

func TestRedactStringRedactsKnownSecretsAndSensitiveAssignments(t *testing.T) {
	t.Parallel()

	got := RedactString("token=node_enroll_secret credential_token=node_cred_secret api_key=abc", Secrets{
		EnrollmentToken: "node_enroll_secret",
		CredentialToken: "node_cred_secret",
	})
	for _, forbidden := range []string{"node_enroll_secret", "node_cred_secret", "abc"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("redaction leaked %q in %q", forbidden, got)
		}
	}
}

func testSpec() Spec {
	return Spec{
		NodeKey:         "workspace-test",
		DisplayName:     "Workspace Test",
		NodeKind:        "workspace",
		NodeRole:        "primary_workspace",
		RuntimeClass:    "workspace_full",
		TokenTTLSeconds: 1800,
		Approve:         true,
		VerifyHeartbeat: true,
		VerifyMain:      true,
	}
}

type fakeMainRunner struct {
	createErr    error
	approveErr   error
	healthErr    error
	createCalls  int
	approveCalls int
	healthCalls  int
}

func newFakeMainRunner() *fakeMainRunner {
	return &fakeMainRunner{}
}

func (f *fakeMainRunner) CreateEnrollmentToken(context.Context, CreateTokenInput) (CreateTokenResult, error) {
	f.createCalls++
	if f.createErr != nil {
		return CreateTokenResult{}, f.createErr
	}
	return CreateTokenResult{
		TokenID:    "node_enrollment_token_test",
		TokenHint:  "hintenroll",
		TokenValue: "node_enroll_secret",
		ExpiresAt:  time.Now().UTC().Add(time.Hour),
	}, nil
}

func (f *fakeMainRunner) ApproveEnrollment(_ context.Context, requestID string) (ApprovalResult, error) {
	f.approveCalls++
	if f.approveErr != nil {
		return ApprovalResult{}, f.approveErr
	}
	return ApprovalResult{
		EnrollmentRequestID: requestID,
		NodeID:              "node_test",
		NodeCredentialID:    "node_credential_test",
		CredentialHint:      "hintcred",
		CredentialToken:     "node_cred_secret",
		ApprovedAt:          time.Now().UTC(),
	}, nil
}

func (f *fakeMainRunner) GetNodeHealth(context.Context, string) (NodeHealthResult, error) {
	f.healthCalls++
	if f.healthErr != nil {
		return NodeHealthResult{}, f.healthErr
	}
	now := time.Now().UTC()
	return NodeHealthResult{
		NodeID:        "node_test",
		NodeKey:       "workspace-test",
		PresenceState: "online",
		HeartbeatID:   "node_heartbeat_test",
		LastSeenAt:    &now,
	}, nil
}

type fakeTargetRunner struct {
	ensureErr      error
	submitErr      error
	importErr      error
	heartbeatErr   error
	submitCalls    int
	importCalls    int
	heartbeatCalls int
	importedToken  string
}

func newFakeTargetRunner() *fakeTargetRunner {
	return &fakeTargetRunner{}
}

func (f *fakeTargetRunner) EnsureNodeAgentInitialized(context.Context, NodeAgentInitInput) error {
	return f.ensureErr
}

func (f *fakeTargetRunner) SubmitEnrollmentRequest(context.Context, string) (EnrollmentRequestResult, error) {
	f.submitCalls++
	if f.submitErr != nil {
		return EnrollmentRequestResult{}, f.submitErr
	}
	return EnrollmentRequestResult{EnrollmentRequestID: "node_enrollment_request_test", Status: "pending"}, nil
}

func (f *fakeTargetRunner) ImportCredential(_ context.Context, input CredentialImportInput) error {
	f.importCalls++
	f.importedToken = input.CredentialToken
	return f.importErr
}

func (f *fakeTargetRunner) HeartbeatOnce(context.Context) (HeartbeatResult, error) {
	f.heartbeatCalls++
	if f.heartbeatErr != nil {
		return HeartbeatResult{}, f.heartbeatErr
	}
	return HeartbeatResult{HeartbeatID: "node_heartbeat_test", NodeID: "node_test", PresenceState: "online", ReceivedAt: time.Now().UTC()}, nil
}
