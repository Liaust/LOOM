package bootstrapssh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"loom.local/loom/internal/correlation"
	"loom.local/loom/internal/enrollmentflow"
	"loom.local/loom/internal/nodeagent"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/setup"
)

type SSHMainRunner struct {
	Runner        Runner
	SocketPath    string
	CorrelationID string
}

func (r SSHMainRunner) CreateEnrollmentToken(ctx context.Context, input enrollmentflow.CreateTokenInput) (enrollmentflow.CreateTokenResult, error) {
	var envelope response.Envelope[nodes.CreateEnrollmentTokenResult]
	if err := r.runJSON(ctx, "main_create_token", r.command("node", "enrollment-token", "create", "--ttl-seconds", strconv.Itoa(input.TTLSeconds)), &envelope); err != nil {
		return enrollmentflow.CreateTokenResult{}, err
	}
	return enrollmentflow.CreateTokenResult{
		TokenID:    envelope.Data.Token.NodeEnrollmentTokenID,
		TokenHint:  envelope.Data.Token.TokenHint,
		TokenValue: envelope.Data.TokenValue,
		ExpiresAt:  envelope.Data.Token.ExpiresAt,
	}, nil
}

func (r SSHMainRunner) ApproveEnrollment(ctx context.Context, requestID string) (enrollmentflow.ApprovalResult, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return enrollmentflow.ApprovalResult{}, errors.New("enrollment request id is required")
	}
	var envelope response.Envelope[nodes.ApproveEnrollmentResult]
	if err := r.runJSON(ctx, "main_approve_request", r.command("node", "enrollment-request", "approve", requestID), &envelope); err != nil {
		return enrollmentflow.ApprovalResult{}, err
	}
	return enrollmentflow.ApprovalResult{
		EnrollmentRequestID: envelope.Data.Request.NodeEnrollmentRequestID,
		NodeID:              envelope.Data.Node.NodeID,
		NodeCredentialID:    envelope.Data.Credential.NodeCredentialID,
		CredentialHint:      envelope.Data.Credential.CredentialHint,
		CredentialToken:     envelope.Data.CredentialToken,
		ApprovedAt:          derefTime(envelope.Data.Request.ApprovedAt),
	}, nil
}

func (r SSHMainRunner) GetNodeHealth(ctx context.Context, nodeRef string) (enrollmentflow.NodeHealthResult, error) {
	nodeRef = strings.TrimSpace(nodeRef)
	if nodeRef == "" {
		return enrollmentflow.NodeHealthResult{}, errors.New("node ref is required")
	}
	var envelope response.Envelope[nodes.NodeHealth]
	if err := r.runJSON(ctx, "main_verify_health", r.command("node", "health", nodeRef), &envelope); err != nil {
		return enrollmentflow.NodeHealthResult{}, err
	}
	result := enrollmentflow.NodeHealthResult{
		NodeID:        envelope.Data.Node.NodeID,
		NodeKey:       envelope.Data.Node.NodeKey,
		PresenceState: envelope.Data.Node.PresenceState,
		LastSeenAt:    envelope.Data.Node.LastSeenAt,
	}
	if envelope.Data.LastHeartbeat != nil {
		result.HeartbeatID = envelope.Data.LastHeartbeat.NodeHeartbeatID
		result.PresenceState = envelope.Data.LastHeartbeat.PresenceState
	}
	return result, nil
}

func (r SSHMainRunner) command(args ...string) string {
	parts := []string{"loom", "--json", "--no-interactive"}
	if strings.TrimSpace(r.CorrelationID) != "" {
		parts = append(parts, "--correlation-id", correlation.Normalize(r.CorrelationID))
	}
	if strings.TrimSpace(r.SocketPath) != "" {
		parts = append(parts, "--socket", r.SocketPath)
	}
	parts = append(parts, args...)
	return shellJoin(parts)
}

func (r SSHMainRunner) runJSON(ctx context.Context, phase string, command string, out any) error {
	if r.Runner == nil {
		return errors.New("main SSH runner is required")
	}
	result, err := r.Runner.Run(ctx, RemoteCommand{Command: command})
	if err != nil {
		return sshEnrollmentError(phase, result, err)
	}
	if err := decodeEnvelopeJSON(result.Stdout, out); err != nil {
		return sshEnrollmentJSONError(phase, result, err)
	}
	return nil
}

type SSHTargetRunner struct {
	Runner        Runner
	SetupPlan     setup.SetupPlan
	Source        SourcePlan
	CorrelationID string
}

func (r SSHTargetRunner) EnsureNodeAgentInitialized(ctx context.Context, input enrollmentflow.NodeAgentInitInput) error {
	status, err := r.status(ctx)
	if err != nil {
		return err
	}
	if status.Config.NodeKey != strings.TrimSpace(input.NodeKey) {
		return fmt.Errorf("remote node-agent config node_key=%q does not match expected %q", status.Config.NodeKey, input.NodeKey)
	}
	if strings.TrimSpace(status.Config.MainURL) == "" {
		return errors.New("remote node-agent config is missing main_url")
	}
	return nil
}

func (r SSHTargetRunner) SubmitEnrollmentRequest(ctx context.Context, token string) (enrollmentflow.EnrollmentRequestResult, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return enrollmentflow.EnrollmentRequestResult{}, errors.New("enrollment token is required")
	}
	var envelope response.Envelope[nodes.EnrollmentRequest]
	command := oneLineTempFileCommand("token_file", "/tmp/loom-enroll-token.XXXXXX", r.agentCommand("enroll")+" --token-file \"$token_file\"")
	if err := r.runJSONWithStdin(ctx, "target_submit_request", command, token, &envelope); err != nil {
		return enrollmentflow.EnrollmentRequestResult{}, err
	}
	return enrollmentflow.EnrollmentRequestResult{
		EnrollmentRequestID: envelope.Data.NodeEnrollmentRequestID,
		Status:              envelope.Data.Status,
	}, nil
}

func (r SSHTargetRunner) ImportCredential(ctx context.Context, input enrollmentflow.CredentialImportInput) error {
	if strings.TrimSpace(input.NodeID) == "" || strings.TrimSpace(input.NodeCredentialID) == "" || strings.TrimSpace(input.CredentialToken) == "" {
		return errors.New("node id, credential id, and credential token are required")
	}
	approval := response.Envelope[nodes.ApproveEnrollmentResult]{
		OK: true,
		Data: nodes.ApproveEnrollmentResult{
			Node:            nodes.Node{NodeID: strings.TrimSpace(input.NodeID)},
			Credential:      nodes.NodeCredential{NodeCredentialID: strings.TrimSpace(input.NodeCredentialID)},
			CredentialToken: strings.TrimSpace(input.CredentialToken),
		},
		Meta: response.NewMeta(correlation.Normalize(r.CorrelationID)),
	}
	payload, err := json.Marshal(approval)
	if err != nil {
		return err
	}
	var envelope response.Envelope[nodeagent.Status]
	command := oneLineTempFileCommand("approval_file", "/tmp/loom-approval.XXXXXX", r.agentCommand("credential", "import")+" --from-file \"$approval_file\"")
	return r.runJSONWithStdin(ctx, "target_import_credential", command, string(payload), &envelope)
}

func (r SSHTargetRunner) HeartbeatOnce(ctx context.Context) (enrollmentflow.HeartbeatResult, error) {
	var envelope response.Envelope[nodes.Heartbeat]
	if err := r.runJSON(ctx, "target_heartbeat_once", r.agentCommand("heartbeat", "--once"), &envelope); err != nil {
		return enrollmentflow.HeartbeatResult{}, err
	}
	return enrollmentflow.HeartbeatResult{
		HeartbeatID:   envelope.Data.NodeHeartbeatID,
		NodeID:        envelope.Data.NodeID,
		PresenceState: envelope.Data.PresenceState,
		ReceivedAt:    envelope.Data.ReceivedAt,
	}, nil
}

func (r SSHTargetRunner) LoadResumeState(ctx context.Context) (enrollmentflow.ResumeState, error) {
	status, err := r.status(ctx)
	if err != nil {
		return enrollmentflow.ResumeState{}, err
	}
	resume := enrollmentflow.ResumeState{
		Status:              enrollmentflow.StateNotStarted,
		EnrollmentRequestID: status.EnrollmentRequestID,
		NodeID:              status.NodeID,
		NodeCredentialID:    status.NodeCredentialID,
		CredentialImported:  status.CredentialConfigured && strings.TrimSpace(status.NodeID) != "" && strings.TrimSpace(status.NodeCredentialID) != "",
		CredentialTokenHeld: status.CredentialConfigured,
	}
	switch {
	case status.LastHeartbeat != nil:
		resume.Status = enrollmentflow.StateHeartbeatVerified
		resume.HeartbeatID = status.LastHeartbeat.NodeHeartbeatID
	case resume.CredentialImported:
		resume.Status = enrollmentflow.StateCredentialImported
	case strings.TrimSpace(resume.EnrollmentRequestID) != "":
		resume.Status = enrollmentflow.StateRequestSubmitted
	}
	return resume, nil
}

func (r SSHTargetRunner) status(ctx context.Context) (nodeagent.Status, error) {
	var envelope response.Envelope[nodeagent.Status]
	if err := r.runJSON(ctx, "target_status", r.agentCommand("status"), &envelope); err != nil {
		return nodeagent.Status{}, err
	}
	return envelope.Data, nil
}

func (r SSHTargetRunner) agentCommand(args ...string) string {
	spec := r.SetupPlan.Spec
	sourcePath := firstNonEmpty(r.Source.RemotePath, spec.SourcePath)
	command := remoteNodeAgentCommand(spec, sourcePath)
	parts := []string{"--json"}
	if strings.TrimSpace(r.CorrelationID) != "" {
		parts = append(parts, "--correlation-id", correlation.Normalize(r.CorrelationID))
	}
	if strings.TrimSpace(r.SetupPlan.Paths.NodeAgentConfigPath) != "" {
		parts = append(parts, "--config", r.SetupPlan.Paths.NodeAgentConfigPath)
	}
	if strings.TrimSpace(r.SetupPlan.Paths.NodeAgentStatePath) != "" {
		parts = append(parts, "--state", r.SetupPlan.Paths.NodeAgentStatePath)
	}
	if strings.TrimSpace(r.SetupPlan.Paths.NodeAgentDataDir) != "" {
		parts = append(parts, "--data-dir", r.SetupPlan.Paths.NodeAgentDataDir)
	}
	parts = append(parts, args...)
	return command + " " + shellJoin(parts)
}

func (r SSHTargetRunner) runJSON(ctx context.Context, phase string, command string, out any) error {
	return r.runJSONWithStdin(ctx, phase, command, "", out)
}

func (r SSHTargetRunner) runJSONWithStdin(ctx context.Context, phase string, command string, stdin string, out any) error {
	if r.Runner == nil {
		return errors.New("target SSH runner is required")
	}
	result, err := r.Runner.Run(ctx, RemoteCommand{Command: command, Stdin: stdin})
	if err != nil {
		return sshEnrollmentError(phase, result, err)
	}
	if err := decodeEnvelopeJSON(result.Stdout, out); err != nil {
		return sshEnrollmentJSONError(phase, result, err)
	}
	return nil
}

func oneLineTempFileCommand(varName string, pattern string, bodyCommand string) string {
	quotedPattern := shellQuote(pattern)
	quotedVar := "\"$" + varName + "\""
	return varName + "=\"$(mktemp " + quotedPattern + ")\" && chmod 0600 " + quotedVar + " && cat > " + quotedVar + " && " + bodyCommand + "; status=$?; rm -f " + quotedVar + "; exit \"$status\""
}

func decodeEnvelopeJSON(stdout string, out any) error {
	if strings.TrimSpace(stdout) == "" {
		return errors.New("remote command returned empty JSON")
	}
	if err := json.Unmarshal([]byte(stdout), out); err != nil {
		return err
	}
	value := reflect.ValueOf(out)
	if value.Kind() == reflect.Pointer && !value.IsNil() {
		elem := value.Elem()
		if elem.Kind() == reflect.Struct {
			ok := elem.FieldByName("OK")
			if ok.IsValid() && ok.Kind() == reflect.Bool && !ok.Bool() {
				return errors.New("remote command returned ok=false")
			}
		}
	}
	return nil
}

func sshEnrollmentError(phase string, result RemoteResult, err error) error {
	message := strings.TrimSpace(fmt.Sprint(err))
	if message == "" {
		message = "remote enrollment command failed"
	}
	return BootstrapError{
		Code:          "bootstrap.enrollment.remote_failed",
		Phase:         phase,
		Message:       RedactEnrollmentSecrets(message),
		ExitCode:      result.ExitCode,
		StdoutExcerpt: safeExcerpt(result.Stdout),
		StderrExcerpt: safeExcerpt(result.Stderr),
	}
}

func sshEnrollmentJSONError(phase string, result RemoteResult, err error) error {
	return BootstrapError{
		Code:          "bootstrap.enrollment.remote_invalid_json",
		Phase:         phase,
		Message:       RedactEnrollmentSecrets(err.Error()),
		ExitCode:      result.ExitCode,
		StdoutExcerpt: safeExcerpt(result.Stdout),
		StderrExcerpt: safeExcerpt(result.Stderr),
	}
}

func derefTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}
