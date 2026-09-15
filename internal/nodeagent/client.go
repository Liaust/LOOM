package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/correlation"
	"loom.local/loom/internal/filetransfer"
	"loom.local/loom/internal/idempotency"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/routing"
	loomsync "loom.local/loom/internal/sync"
	mainwatchedroots "loom.local/loom/internal/watchedroots"
)

type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

type RemoteRequestError struct {
	StatusCode int
	Method     string
	Path       string
	Envelope   response.ErrorEnvelope
	Body       string
}

func (e RemoteRequestError) Error() string {
	if e.Envelope.Error.Summary != "" {
		return fmt.Sprintf("%s %s failed with status %d: %s", e.Method, e.Path, e.StatusCode, e.Envelope.Error.Summary)
	}
	if e.Body != "" {
		return fmt.Sprintf("%s %s failed with status %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
	}
	return fmt.Sprintf("%s %s failed with status %d", e.Method, e.Path, e.StatusCode)
}

func NewClient(baseURL string) (Client, error) {
	baseURL, err := normalizeMainURL(baseURL)
	if err != nil {
		return Client{}, err
	}
	return Client{
		BaseURL: baseURL,
		HTTPClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}, nil
}

func (c Client) Enroll(ctx context.Context, correlationID, idempotencyKey string, input nodes.CreateEnrollmentRequestInput) (response.Envelope[nodes.EnrollmentRequest], error) {
	return doJSON[nodes.CreateEnrollmentRequestInput, nodes.EnrollmentRequest](ctx, c, http.MethodPost, "/v1/node-agent/enroll", correlationID, idempotencyKey, input)
}

func (c Client) Heartbeat(ctx context.Context, correlationID string, input nodes.HeartbeatInput) (response.Envelope[nodes.Heartbeat], error) {
	return doJSON[nodes.HeartbeatInput, nodes.Heartbeat](ctx, c, http.MethodPost, "/v1/node-agent/heartbeat", correlationID, "", input)
}

func (c Client) Poll(ctx context.Context, correlationID string, input communication.PollInput) (response.Envelope[communication.PollResult], error) {
	return doJSON[communication.PollInput, communication.PollResult](ctx, c, http.MethodPost, "/v1/node-agent/poll", correlationID, "", input)
}

func (c Client) Ack(ctx context.Context, correlationID, idempotencyKey string, input communication.AckInput) (response.Envelope[communication.AckResult], error) {
	return doJSON[communication.AckInput, communication.AckResult](ctx, c, http.MethodPost, "/v1/node-agent/ack", correlationID, idempotencyKey, input)
}

func (c Client) AdvertiseProvider(ctx context.Context, correlationID, idempotencyKey string, input capabilities.ProviderAdvertisementInput) (response.Envelope[capabilities.ProviderAdvertisementInspection], error) {
	return doJSON[capabilities.ProviderAdvertisementInput, capabilities.ProviderAdvertisementInspection](ctx, c, http.MethodPost, "/v1/node-agent/providers/advertise", correlationID, idempotencyKey, input)
}

func (c Client) CapabilityResult(ctx context.Context, correlationID, idempotencyKey string, input routing.RemoteResultInput) (response.Envelope[routing.RemoteResultOutcome], error) {
	return doJSON[routing.RemoteResultInput, routing.RemoteResultOutcome](ctx, c, http.MethodPost, "/v1/node-agent/capability-result", correlationID, idempotencyKey, input)
}

func (c Client) PushSyncBatch(ctx context.Context, correlationID, idempotencyKey string, input loomsync.PushBatchInput) (response.Envelope[loomsync.PushBatchResult], error) {
	return doJSON[loomsync.PushBatchInput, loomsync.PushBatchResult](ctx, c, http.MethodPost, "/v1/node-agent/sync/batches", correlationID, idempotencyKey, input)
}

func (c Client) UploadSyncedObject(ctx context.Context, correlationID, idempotencyKey string, input loomsync.SyncedObjectInput) (response.Envelope[loomsync.SyncedObjectResult], error) {
	return doJSON[loomsync.SyncedObjectInput, loomsync.SyncedObjectResult](ctx, c, http.MethodPost, "/v1/node-agent/sync/object-upload", correlationID, idempotencyKey, input)
}

func (c Client) PushPrivateBackup(ctx context.Context, correlationID, idempotencyKey string, input loomsync.PrivateBackupInput) (response.Envelope[loomsync.PrivateBackupResult], error) {
	return doJSON[loomsync.PrivateBackupInput, loomsync.PrivateBackupResult](ctx, c, http.MethodPost, "/v1/node-agent/sync/private-backup", correlationID, idempotencyKey, input)
}

func (c Client) CreateDeletionRequest(ctx context.Context, correlationID, idempotencyKey string, input loomsync.DeletionRequestInput) (response.Envelope[loomsync.DeletionRequestResult], error) {
	return doJSON[loomsync.DeletionRequestInput, loomsync.DeletionRequestResult](ctx, c, http.MethodPost, "/v1/node-agent/sync/deletion-request", correlationID, idempotencyKey, input)
}

func (c Client) ReportWatchedRoot(ctx context.Context, correlationID, idempotencyKey string, input mainwatchedroots.ReportInput) (response.Envelope[mainwatchedroots.ReportResult], error) {
	return doJSON[mainwatchedroots.ReportInput, mainwatchedroots.ReportResult](ctx, c, http.MethodPost, "/v1/node-agent/watched-roots/report", correlationID, idempotencyKey, input)
}

func (c Client) PushWatchedRootBackupBatch(ctx context.Context, correlationID, idempotencyKey string, input mainwatchedroots.BackupBatchInput) (response.Envelope[mainwatchedroots.BackupBatchResult], error) {
	return doJSON[mainwatchedroots.BackupBatchInput, mainwatchedroots.BackupBatchResult](ctx, c, http.MethodPost, "/v1/node-agent/watched-roots/backup-batches", correlationID, idempotencyKey, input)
}

func (c Client) CreateFileTransfer(ctx context.Context, correlationID, idempotencyKey string, manifest filetransfer.Manifest) (response.Envelope[filetransfer.Status], error) {
	return doJSON[filetransfer.Manifest, filetransfer.Status](ctx, c, http.MethodPost, "/v1/file-transfers", correlationID, idempotencyKey, manifest)
}

func (c Client) GetFileTransfer(ctx context.Context, correlationID, transferID string) (response.Envelope[filetransfer.Status], error) {
	return doJSON[map[string]any, filetransfer.Status](ctx, c, http.MethodGet, "/v1/file-transfers/"+url.PathEscape(transferID), correlationID, "", nil)
}

func (c Client) UploadFileTransferChunk(ctx context.Context, correlationID, idempotencyKey, transferID string, index int64, payload []byte, checksumAlgorithm, checksumHex string) (response.Envelope[filetransfer.UploadChunkResult], error) {
	headers := map[string]string{}
	if strings.TrimSpace(checksumAlgorithm) != "" {
		headers["X-LOOM-Checksum-Algorithm"] = strings.TrimSpace(checksumAlgorithm)
	}
	if strings.TrimSpace(checksumHex) != "" {
		headers["X-LOOM-Checksum-Hex"] = strings.TrimSpace(checksumHex)
	}
	return doRaw[filetransfer.UploadChunkResult](ctx, withFileTransferTimeout(c), http.MethodPut, "/v1/file-transfers/"+url.PathEscape(transferID)+"/chunks/"+url.PathEscape(fmt.Sprintf("%d", index)), correlationID, idempotencyKey, payload, headers)
}

func (c Client) CompleteFileTransfer(ctx context.Context, correlationID, idempotencyKey, transferID string) (response.Envelope[filetransfer.CompleteResult], error) {
	return doJSON[map[string]any, filetransfer.CompleteResult](ctx, withFileTransferTimeout(c), http.MethodPost, "/v1/file-transfers/"+url.PathEscape(transferID)+"/complete", correlationID, idempotencyKey, nil)
}

func (c Client) AbortFileTransfer(ctx context.Context, correlationID, idempotencyKey, transferID, reason string) (response.Envelope[filetransfer.Status], error) {
	return doJSON[filetransfer.AbortInput, filetransfer.Status](ctx, c, http.MethodPost, "/v1/file-transfers/"+url.PathEscape(transferID)+"/abort", correlationID, idempotencyKey, filetransfer.AbortInput{Reason: reason})
}

func withFileTransferTimeout(client Client) Client {
	const minTimeout = 30 * time.Minute
	if client.HTTPClient == nil {
		client.HTTPClient = &http.Client{Timeout: minTimeout}
		return client
	}
	if client.HTTPClient.Timeout == 0 || client.HTTPClient.Timeout >= minTimeout {
		return client
	}
	clone := *client.HTTPClient
	clone.Timeout = minTimeout
	client.HTTPClient = &clone
	return client
}

func doJSON[I any, O any](ctx context.Context, client Client, method, path, correlationID, idempotencyKey string, input I) (response.Envelope[O], error) {
	var output response.Envelope[O]
	body, err := json.Marshal(input)
	if err != nil {
		return output, err
	}
	request, err := http.NewRequestWithContext(ctx, method, client.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return output, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set(correlation.Header, correlation.Normalize(correlationID))
	if strings.TrimSpace(idempotencyKey) != "" {
		request.Header.Set(idempotency.Header, strings.TrimSpace(idempotencyKey))
	}
	httpClient := client.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(request)
	if err != nil {
		return output, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return output, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errEnvelope response.ErrorEnvelope
		_ = json.Unmarshal(raw, &errEnvelope)
		return output, RemoteRequestError{
			StatusCode: resp.StatusCode,
			Method:     method,
			Path:       path,
			Envelope:   errEnvelope,
			Body:       strings.TrimSpace(string(raw)),
		}
	}
	if err := json.Unmarshal(raw, &output); err != nil {
		return output, err
	}
	return output, nil
}

func doRaw[O any](ctx context.Context, client Client, method, path, correlationID, idempotencyKey string, payload []byte, headers map[string]string) (response.Envelope[O], error) {
	var output response.Envelope[O]
	request, err := http.NewRequestWithContext(ctx, method, client.BaseURL+path, bytes.NewReader(payload))
	if err != nil {
		return output, err
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("Accept", "application/json")
	request.Header.Set(correlation.Header, correlation.Normalize(correlationID))
	if strings.TrimSpace(idempotencyKey) != "" {
		request.Header.Set(idempotency.Header, strings.TrimSpace(idempotencyKey))
	}
	for key, value := range headers {
		if strings.TrimSpace(key) != "" && strings.TrimSpace(value) != "" {
			request.Header.Set(key, strings.TrimSpace(value))
		}
	}
	httpClient := client.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(request)
	if err != nil {
		return output, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return output, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errEnvelope response.ErrorEnvelope
		_ = json.Unmarshal(raw, &errEnvelope)
		return output, RemoteRequestError{
			StatusCode: resp.StatusCode,
			Method:     method,
			Path:       path,
			Envelope:   errEnvelope,
			Body:       strings.TrimSpace(string(raw)),
		}
	}
	if err := json.Unmarshal(raw, &output); err != nil {
		return output, err
	}
	return output, nil
}
