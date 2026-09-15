package enrollmentflow

import (
	"context"
	"errors"
	"strings"
	"time"

	"loom.local/loom/internal/config"
	"loom.local/loom/internal/correlation"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/nodeagent"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/version"
)

type LocalMainRunner struct {
	Client        localclient.Client
	CorrelationID string
}

func NewLocalMainRunner(socketPath string, correlationID string) LocalMainRunner {
	if strings.TrimSpace(socketPath) == "" {
		socketPath = config.DefaultSocketPath
	}
	return LocalMainRunner{
		Client:        localclient.New(socketPath),
		CorrelationID: correlation.Normalize(correlationID),
	}
}

func (r LocalMainRunner) CreateEnrollmentToken(ctx context.Context, input CreateTokenInput) (CreateTokenResult, error) {
	envelope, err := r.Client.CreateEnrollmentToken(ctx, r.correlationID(), nodes.CreateEnrollmentTokenInput{TTLSeconds: input.TTLSeconds})
	if err != nil {
		return CreateTokenResult{}, err
	}
	return CreateTokenResult{
		TokenID:    envelope.Data.Token.NodeEnrollmentTokenID,
		TokenHint:  envelope.Data.Token.TokenHint,
		TokenValue: envelope.Data.TokenValue,
		ExpiresAt:  envelope.Data.Token.ExpiresAt,
	}, nil
}

func (r LocalMainRunner) ApproveEnrollment(ctx context.Context, requestID string) (ApprovalResult, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return ApprovalResult{}, errors.New("enrollment request id is required")
	}
	envelope, err := r.Client.ApproveEnrollment(ctx, r.correlationID(), requestID, nodes.ApproveEnrollmentInput{EnrollmentRequestRef: requestID})
	if err != nil {
		return ApprovalResult{}, err
	}
	return ApprovalResult{
		EnrollmentRequestID: envelope.Data.Request.NodeEnrollmentRequestID,
		NodeID:              envelope.Data.Node.NodeID,
		NodeCredentialID:    envelope.Data.Credential.NodeCredentialID,
		CredentialHint:      envelope.Data.Credential.CredentialHint,
		CredentialToken:     envelope.Data.CredentialToken,
		ApprovedAt:          derefTime(envelope.Data.Request.ApprovedAt),
	}, nil
}

func (r LocalMainRunner) GetNodeHealth(ctx context.Context, nodeRef string) (NodeHealthResult, error) {
	envelope, err := r.Client.GetNodeHealth(ctx, r.correlationID(), nodeRef)
	if err != nil {
		return NodeHealthResult{}, err
	}
	health := envelope.Data
	result := NodeHealthResult{
		NodeID:        health.Node.NodeID,
		NodeKey:       health.Node.NodeKey,
		PresenceState: health.Node.PresenceState,
		LastSeenAt:    health.Node.LastSeenAt,
	}
	if health.LastHeartbeat != nil {
		result.HeartbeatID = health.LastHeartbeat.NodeHeartbeatID
		result.PresenceState = health.LastHeartbeat.PresenceState
	}
	return result, nil
}

func (r LocalMainRunner) correlationID() string {
	if strings.TrimSpace(r.CorrelationID) == "" {
		return correlation.Normalize("")
	}
	return correlation.Normalize(r.CorrelationID)
}

type LocalTargetRunner struct {
	Store         nodeagent.Store
	CorrelationID string
}

func NewLocalTargetRunner(configPath, statePath, dataDir, correlationID string) LocalTargetRunner {
	return LocalTargetRunner{
		Store: nodeagent.Store{
			ConfigPath: configPath,
			StatePath:  statePath,
			DataDir:    dataDir,
		},
		CorrelationID: correlation.Normalize(correlationID),
	}
}

func (r LocalTargetRunner) EnsureNodeAgentInitialized(ctx context.Context, input NodeAgentInitInput) error {
	_ = ctx
	config, err := r.Store.LoadConfig()
	if err != nil {
		return err
	}
	if config.NodeKey != strings.TrimSpace(input.NodeKey) {
		return errors.New("node-agent config node_key does not match enrollment spec")
	}
	if _, err := r.Store.LoadState(); err != nil {
		return err
	}
	return r.Store.EnsureDataDirs()
}

func (r LocalTargetRunner) SubmitEnrollmentRequest(ctx context.Context, token string) (EnrollmentRequestResult, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return EnrollmentRequestResult{}, errors.New("enrollment token is required")
	}
	config, err := r.Store.LoadConfig()
	if err != nil {
		return EnrollmentRequestResult{}, err
	}
	state, err := r.Store.LoadState()
	if err != nil {
		return EnrollmentRequestResult{}, err
	}
	client, err := nodeagent.NewClient(config.MainURL)
	if err != nil {
		return EnrollmentRequestResult{}, err
	}
	envelope, err := client.Enroll(ctx, r.correlationID(), "setup.enroll."+config.NodeKey, nodes.CreateEnrollmentRequestInput{
		EnrollmentToken:       token,
		RequestedNodeKey:      config.NodeKey,
		RequestedDisplayName:  config.DisplayName,
		RequestedNodeKind:     config.NodeKind,
		RequestedNodeRole:     config.NodeRole,
		RequestedRuntimeClass: config.RuntimeClass,
	})
	if err != nil {
		return EnrollmentRequestResult{}, err
	}
	state.EnrollmentRequestID = envelope.Data.NodeEnrollmentRequestID
	if err := r.Store.SaveState(state); err != nil {
		return EnrollmentRequestResult{}, err
	}
	return EnrollmentRequestResult{EnrollmentRequestID: envelope.Data.NodeEnrollmentRequestID, Status: envelope.Data.Status}, nil
}

func (r LocalTargetRunner) ImportCredential(ctx context.Context, input CredentialImportInput) error {
	_ = ctx
	state, err := r.Store.LoadState()
	if err != nil {
		return err
	}
	if strings.TrimSpace(input.NodeID) == "" || strings.TrimSpace(input.NodeCredentialID) == "" || strings.TrimSpace(input.CredentialToken) == "" {
		return errors.New("node id, credential id, and credential token are required")
	}
	state.NodeID = strings.TrimSpace(input.NodeID)
	state.NodeCredentialID = strings.TrimSpace(input.NodeCredentialID)
	state.CredentialToken = strings.TrimSpace(input.CredentialToken)
	return r.Store.SaveState(state)
}

func (r LocalTargetRunner) HeartbeatOnce(ctx context.Context) (HeartbeatResult, error) {
	config, err := r.Store.LoadConfig()
	if err != nil {
		return HeartbeatResult{}, err
	}
	state, err := r.Store.LoadState()
	if err != nil {
		return HeartbeatResult{}, err
	}
	if strings.TrimSpace(state.NodeID) == "" || strings.TrimSpace(state.CredentialToken) == "" {
		return HeartbeatResult{}, errors.New("node credential is not imported")
	}
	client, err := nodeagent.NewClient(config.MainURL)
	if err != nil {
		return HeartbeatResult{}, err
	}
	envelope, err := client.Heartbeat(ctx, r.correlationID(), nodes.HeartbeatInput{
		NodeRef:         state.NodeID,
		CredentialToken: state.CredentialToken,
		RuntimeVersion:  version.Current().Version,
		ReportedStatus:  "ok",
	})
	if err != nil {
		return HeartbeatResult{}, err
	}
	heartbeat := envelope.Data
	state.LastHeartbeat = &nodeagent.HeartbeatState{
		NodeHeartbeatID: heartbeat.NodeHeartbeatID,
		PresenceState:   heartbeat.PresenceState,
		ReportedStatus:  heartbeat.ReportedStatus,
		ReceivedAt:      heartbeat.ReceivedAt,
	}
	if err := r.Store.SaveState(state); err != nil {
		return HeartbeatResult{}, err
	}
	return HeartbeatResult{
		HeartbeatID:   heartbeat.NodeHeartbeatID,
		NodeID:        heartbeat.NodeID,
		PresenceState: heartbeat.PresenceState,
		ReceivedAt:    heartbeat.ReceivedAt,
	}, nil
}

func (r LocalTargetRunner) correlationID() string {
	if strings.TrimSpace(r.CorrelationID) == "" {
		return correlation.Normalize("")
	}
	return correlation.Normalize(r.CorrelationID)
}

func derefTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}
