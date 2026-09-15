package enrollmentflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"loom.local/loom/internal/correlation"
	"loom.local/loom/internal/idempotency"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/response"
)

type HTTPMainRunner struct {
	BaseURL       string
	Client        *http.Client
	CorrelationID string
}

func NewHTTPMainRunner(baseURL string, correlationID string) HTTPMainRunner {
	return HTTPMainRunner{
		BaseURL:       strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		CorrelationID: correlation.Normalize(correlationID),
		Client:        &http.Client{Timeout: 15 * time.Second},
	}
}

func (r HTTPMainRunner) CreateEnrollmentToken(ctx context.Context, input CreateTokenInput) (CreateTokenResult, error) {
	envelope, err := doMainJSON[nodes.CreateEnrollmentTokenInput, nodes.CreateEnrollmentTokenResult](
		ctx,
		r,
		http.MethodPost,
		"/v1/node-enrollment-tokens",
		"setup.enroll.token",
		nodes.CreateEnrollmentTokenInput{TTLSeconds: input.TTLSeconds},
	)
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

func (r HTTPMainRunner) ApproveEnrollment(ctx context.Context, requestID string) (ApprovalResult, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return ApprovalResult{}, errors.New("enrollment request id is required")
	}
	path := "/v1/node-enrollment-requests/" + url.PathEscape(requestID) + "/approve"
	envelope, err := doMainJSON[nodes.ApproveEnrollmentInput, nodes.ApproveEnrollmentResult](
		ctx,
		r,
		http.MethodPost,
		path,
		"setup.enroll.approve."+requestID,
		nodes.ApproveEnrollmentInput{EnrollmentRequestRef: requestID},
	)
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

func (r HTTPMainRunner) GetNodeHealth(ctx context.Context, nodeRef string) (NodeHealthResult, error) {
	nodeRef = strings.TrimSpace(nodeRef)
	if nodeRef == "" {
		return NodeHealthResult{}, errors.New("node ref is required")
	}
	envelope, err := doMainJSON[map[string]any, nodes.NodeHealth](
		ctx,
		r,
		http.MethodGet,
		"/v1/nodes/"+url.PathEscape(nodeRef)+"/health",
		"",
		nil,
	)
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

func doMainJSON[I any, O any](ctx context.Context, runner HTTPMainRunner, method, path, idempotencyKey string, input I) (response.Envelope[O], error) {
	var output response.Envelope[O]
	base := strings.TrimRight(strings.TrimSpace(runner.BaseURL), "/")
	if base == "" {
		return output, errors.New("main URL is required")
	}
	var body io.Reader
	if method != http.MethodGet {
		payload, err := json.Marshal(input)
		if err != nil {
			return output, err
		}
		body = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return output, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set(correlation.Header, runner.correlationID())
	if method != http.MethodGet {
		request.Header.Set("Content-Type", "application/json")
	}
	if strings.TrimSpace(idempotencyKey) != "" {
		request.Header.Set(idempotency.Header, strings.TrimSpace(idempotencyKey))
	}
	client := runner.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(request)
	if err != nil {
		return output, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return output, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var failure response.ErrorEnvelope
		_ = json.Unmarshal(raw, &failure)
		message := strings.TrimSpace(failure.Error.Summary)
		if message == "" {
			message = strings.TrimSpace(string(raw))
		}
		if message == "" {
			message = resp.Status
		}
		return output, fmt.Errorf("%s %s failed with status %d: %s", method, path, resp.StatusCode, message)
	}
	if err := json.Unmarshal(raw, &output); err != nil {
		return output, err
	}
	return output, nil
}

func (r HTTPMainRunner) correlationID() string {
	if strings.TrimSpace(r.CorrelationID) == "" {
		return correlation.Normalize("")
	}
	return correlation.Normalize(r.CorrelationID)
}
