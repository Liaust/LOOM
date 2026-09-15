package localclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"loom.local/loom/internal/agents"
	"loom.local/loom/internal/artifacts"
	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/backupcoverage"
	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/box"
	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/cloudstorage"
	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/correlation"
	"loom.local/loom/internal/dropzone"
	"loom.local/loom/internal/events"
	"loom.local/loom/internal/filetransfer"
	"loom.local/loom/internal/health"
	"loom.local/loom/internal/idempotency"
	"loom.local/loom/internal/identity"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/lane"
	"loom.local/loom/internal/mainstorage"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/minidashboard"
	"loom.local/loom/internal/modules"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/objects"
	"loom.local/loom/internal/policy"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectdoctor"
	"loom.local/loom/internal/projectexport"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
	"loom.local/loom/internal/realtime"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/routing"
	"loom.local/loom/internal/scopes"
	"loom.local/loom/internal/scripts"
	"loom.local/loom/internal/search"
	"loom.local/loom/internal/serviceregistry"
	loomstatus "loom.local/loom/internal/status"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/storagedoctor"
	"loom.local/loom/internal/storagefidelity"
	"loom.local/loom/internal/storageretention"
	"loom.local/loom/internal/storageview"
	loomsync "loom.local/loom/internal/sync"
	mainwatchedroots "loom.local/loom/internal/watchedroots"
	"loom.local/loom/internal/workers"
)

type Client struct {
	SocketPath     string
	BaseURL        string
	client         *http.Client
	idempotencyKey string
}

type CloudSnapshotStatusReport struct {
	Cloud     cloudstorage.StatusReport         `json:"cloud"`
	Snapshots *cloudstorage.SnapshotListResult  `json:"snapshots,omitempty"`
	Producer  maintenance.CloudProtectionStatus `json:"producer"`
}

func New(socketPath string) Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
	}

	return Client{
		SocketPath: socketPath,
		BaseURL:    "http://loom",
		client: &http.Client{
			Transport: transport,
		},
	}
}

func NewHTTP(baseURL string) (Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return Client{}, fmt.Errorf("base URL is required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return Client{}, fmt.Errorf("parse base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return Client{}, fmt.Errorf("unsupported base URL scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return Client{}, fmt.Errorf("base URL host is required")
	}
	return Client{
		BaseURL: baseURL,
		client:  http.DefaultClient,
	}, nil
}

func (c Client) WithIdempotencyKey(key string) Client {
	c.idempotencyKey = strings.TrimSpace(key)
	return c
}

func (c Client) Health(ctx context.Context, correlationID string) (response.Envelope[health.Report], error) {
	return doJSON[health.Report](c, ctx, http.MethodGet, "/v1/health", correlationID, nil)
}

func (c Client) Status(ctx context.Context, correlationID string) (response.Envelope[loomstatus.Report], error) {
	return doJSON[loomstatus.Report](c, ctx, http.MethodGet, "/v1/status", correlationID, nil)
}

func (c Client) MiniDashboardStatus(ctx context.Context, correlationID string) (response.Envelope[minidashboard.DomainSnapshot], error) {
	return doJSON[minidashboard.DomainSnapshot](c, ctx, http.MethodGet, "/v1/mini-dashboard/status", correlationID, nil)
}

func (c Client) CloudStatusLive(ctx context.Context, correlationID string, input cloudstorage.CloudStatusLiveInput) (response.Envelope[cloudstorage.StatusReport], error) {
	return doJSON[cloudstorage.StatusReport](c, ctx, http.MethodPost, "/v1/cloud/status/live", correlationID, input)
}

func (c Client) CloudDoctorLive(ctx context.Context, correlationID string, input cloudstorage.CloudDoctorLiveInput) (response.Envelope[cloudstorage.DoctorReport], error) {
	return doJSON[cloudstorage.DoctorReport](c, ctx, http.MethodPost, "/v1/cloud/doctor/live", correlationID, input)
}

func (c Client) CloudSnapshotStatusLive(ctx context.Context, correlationID string, input cloudstorage.CloudSnapshotStatusLiveInput) (response.Envelope[cloudstorage.CloudSnapshotStatusReport], error) {
	return doJSON[cloudstorage.CloudSnapshotStatusReport](c, ctx, http.MethodPost, "/v1/cloud/snapshot/status/live", correlationID, input)
}

func (c Client) CloudSnapshotStatusWithProducerLive(ctx context.Context, correlationID string, input cloudstorage.CloudSnapshotStatusLiveInput) (response.Envelope[CloudSnapshotStatusReport], error) {
	return doJSON[CloudSnapshotStatusReport](c, ctx, http.MethodPost, "/v1/cloud/snapshot/status/live", correlationID, input)
}

func (c Client) RunCloudSnapshot(ctx context.Context, correlationID string, input workers.RunOnceInput) (response.Envelope[workers.RunOnceResult], error) {
	return doJSON[workers.RunOnceResult](c, ctx, http.MethodPost, "/v1/cloud/snapshot/run", correlationID, input)
}

func (c Client) CloudSnapshotListLive(ctx context.Context, correlationID string, input cloudstorage.CloudSnapshotListLiveInput) (response.Envelope[cloudstorage.SnapshotListResult], error) {
	return doJSON[cloudstorage.SnapshotListResult](c, ctx, http.MethodPost, "/v1/cloud/snapshot/list/live", correlationID, input)
}

func (c Client) CloudSnapshotVerifyLive(ctx context.Context, correlationID string, input cloudstorage.SnapshotVerifyLiveInput) (response.Envelope[cloudstorage.SnapshotVerifyResult], error) {
	return doJSON[cloudstorage.SnapshotVerifyResult](c, ctx, http.MethodPost, "/v1/cloud/snapshot/verify/live", correlationID, input)
}

// CloudSnapshotRestoreDrillInput carries selectors only. The daemon owns cloud
// credentials, staging paths and the reviewed database/socket authority.
type CloudSnapshotRestoreDrillInput struct {
	ConfigPath               string `json:"config_path,omitempty"`
	NodeID                   string `json:"node_id,omitempty"`
	Ref                      string `json:"ref"`
	TargetDatabase           string `json:"target_database,omitempty"`
	ProvenanceTargetDatabase string `json:"provenance_target_database,omitempty"`
	DryRun                   bool   `json:"dry_run"`
}

func (c Client) CloudSnapshotRestoreDrillLive(ctx context.Context, correlationID string, input CloudSnapshotRestoreDrillInput) (response.Envelope[cloudstorage.CloudRestoreDrillResult], error) {
	return doJSON[cloudstorage.CloudRestoreDrillResult](c, ctx, http.MethodPost, "/v1/cloud/snapshot/restore-drill/live", correlationID, input)
}

func (c Client) CloudRestoreCleanupPlan(ctx context.Context, correlationID string, input cloudstorage.RestoreCleanupPlanInput) (response.Envelope[cloudstorage.RestoreCleanupResult], error) {
	return doJSON[cloudstorage.RestoreCleanupResult](c, ctx, http.MethodPost, "/v1/cloud/snapshot/restore-cleanup/plan", correlationID, input)
}

func (c Client) CloudRestoreCleanupApply(ctx context.Context, correlationID string, input cloudstorage.RestoreCleanupApplyInput) (response.Envelope[cloudstorage.RestoreCleanupResult], error) {
	return doJSON[cloudstorage.RestoreCleanupResult](c, ctx, http.MethodPost, "/v1/cloud/snapshot/restore-cleanup/apply", correlationID, input)
}

func (c Client) CloudSnapshotRetentionPlanLive(ctx context.Context, correlationID string, input cloudstorage.CloudSnapshotRetentionPlanLiveInput) (response.Envelope[cloudstorage.SnapshotRetentionPlan], error) {
	return doJSON[cloudstorage.SnapshotRetentionPlan](c, ctx, http.MethodPost, "/v1/cloud/snapshot/retention/plan/live", correlationID, input)
}

func (c Client) CloudSnapshotRetentionApplyLive(ctx context.Context, correlationID string, input cloudstorage.CloudSnapshotRetentionApplyLiveInput) (response.Envelope[cloudstorage.SnapshotRetentionApplyResult], error) {
	return doJSON[cloudstorage.SnapshotRetentionApplyResult](c, ctx, http.MethodPost, "/v1/cloud/snapshot/retention/apply/live", correlationID, input)
}

func (c Client) CloudSnapshotBackendStatusLive(ctx context.Context, correlationID string, input cloudstorage.CloudSnapshotBackendStatusLiveInput) (response.Envelope[cloudstorage.SnapshotBackendStatusReport], error) {
	return doJSON[cloudstorage.SnapshotBackendStatusReport](c, ctx, http.MethodPost, "/v1/cloud/snapshot/backend/status/live", correlationID, input)
}

func (c Client) CloudSnapshotBackendInitLive(ctx context.Context, correlationID string, input cloudstorage.CloudSnapshotBackendInitLiveInput) (response.Envelope[cloudstorage.SnapshotBackendInitResult], error) {
	return doJSON[cloudstorage.SnapshotBackendInitResult](c, ctx, http.MethodPost, "/v1/cloud/snapshot/backend/init/live", correlationID, input)
}

func (c Client) BackupCoverage(ctx context.Context, correlationID string) (response.Envelope[backupcoverage.Report], error) {
	return doJSON[backupcoverage.Report](c, ctx, http.MethodGet, "/v1/backup/coverage", correlationID, nil)
}

func (c Client) ListBackupContracts(ctx context.Context, correlationID string) (response.Envelope[backupcontracts.ListResult], error) {
	return doJSON[backupcontracts.ListResult](c, ctx, http.MethodGet, "/v1/backup/contracts", correlationID, nil)
}

func (c Client) GetBackupContract(ctx context.Context, correlationID string, key string) (response.Envelope[backupcontracts.ContractRecord], error) {
	return doJSON[backupcontracts.ContractRecord](c, ctx, http.MethodGet, "/v1/backup/contracts/"+url.PathEscape(key), correlationID, nil)
}

func (c Client) ListProtectedFolders(ctx context.Context, correlationID string, filter backupcontracts.ProtectedFolderFilter) (response.Envelope[backupcontracts.ProtectedFolderListResult], error) {
	values := url.Values{}
	if filter.Lifecycle != "" {
		values.Set("status", filter.Lifecycle)
	}
	if filter.NodeRef != "" {
		values.Set("node", filter.NodeRef)
	}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	path := "/v1/backup/contracts"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[backupcontracts.ProtectedFolderListResult](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetProtectedFolder(ctx context.Context, correlationID, key string) (response.Envelope[backupcontracts.ProtectedFolderRecord], error) {
	return doJSON[backupcontracts.ProtectedFolderRecord](c, ctx, http.MethodGet, "/v1/backup/contracts/"+url.PathEscape(key), correlationID, nil)
}

func (c Client) CreateBackupContract(ctx context.Context, correlationID string, input backupcontracts.CreateRequest) (response.Envelope[backupcontracts.LifecycleResult], error) {
	return doJSON[backupcontracts.LifecycleResult](c, ctx, http.MethodPost, "/v1/backup/contracts", correlationID, input)
}

func (c Client) DisableBackupContract(ctx context.Context, correlationID string, key string, input backupcontracts.DisableRequest) (response.Envelope[backupcontracts.LifecycleResult], error) {
	return doJSON[backupcontracts.LifecycleResult](c, ctx, http.MethodPost, "/v1/backup/contracts/"+url.PathEscape(key)+"/disable", correlationID, input)
}

func (c Client) EnableBackupContract(ctx context.Context, correlationID, key string, input backupcontracts.EnableRequest) (response.Envelope[backupcontracts.LifecycleResult], error) {
	return doJSON[backupcontracts.LifecycleResult](c, ctx, http.MethodPost, "/v1/backup/contracts/"+url.PathEscape(key)+"/enable", correlationID, input)
}

func (c Client) RetryBackupContractActivation(ctx context.Context, correlationID, key string, input backupcontracts.RetryActivationRequest) (response.Envelope[backupcontracts.ReconcileQueueResult], error) {
	return doJSON[backupcontracts.ReconcileQueueResult](c, ctx, http.MethodPost, "/v1/backup/contracts/"+url.PathEscape(key)+"/retry-activation", correlationID, input)
}

func (c Client) RecheckBackupContract(ctx context.Context, correlationID, key string) (response.Envelope[backupcontracts.PreflightRecord], error) {
	return doJSON[backupcontracts.PreflightRecord](c, ctx, http.MethodPost, "/v1/backup/contracts/"+url.PathEscape(key)+"/recheck", correlationID, map[string]any{})
}

func (c Client) DeleteBackupContract(ctx context.Context, correlationID string, key string, input backupcontracts.DeleteRequest) (response.Envelope[backupcontracts.LifecycleResult], error) {
	return doJSON[backupcontracts.LifecycleResult](c, ctx, http.MethodDelete, "/v1/backup/contracts/"+url.PathEscape(key), correlationID, input)
}

func (c Client) MigrateBackupContractIgnorePolicy(ctx context.Context, correlationID string, input backupcontracts.MigrateIgnorePolicyRequest) (response.Envelope[backupcontracts.MigrateIgnorePolicyResponse], error) {
	return doJSON[backupcontracts.MigrateIgnorePolicyResponse](c, ctx, http.MethodPost, "/v1/backup/contracts/migrate-ignore-policy", correlationID, input)
}

func (c Client) CreateBackupContractPreflight(ctx context.Context, correlationID string, input backupcontracts.PreflightCreateRequest) (response.Envelope[backupcontracts.PreflightRecord], error) {
	return doJSON[backupcontracts.PreflightRecord](c, ctx, http.MethodPost, "/v1/backup/contracts/preflights", correlationID, input)
}

func (c Client) ListBackupContractPreflights(ctx context.Context, correlationID string, filter backupcontracts.PreflightFilter) (response.Envelope[[]backupcontracts.PreflightRecord], error) {
	values := url.Values{}
	if filter.NodeRef != "" {
		values.Set("node", filter.NodeRef)
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	path := "/v1/backup/contracts/preflights"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]backupcontracts.PreflightRecord](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetBackupContractPreflight(ctx context.Context, correlationID, preflightID string) (response.Envelope[backupcontracts.PreflightRecord], error) {
	return doJSON[backupcontracts.PreflightRecord](c, ctx, http.MethodGet, "/v1/backup/contracts/preflights/"+url.PathEscape(preflightID), correlationID, nil)
}

func (c Client) RetryBackupContractPreflight(ctx context.Context, correlationID, preflightID string) (response.Envelope[backupcontracts.PreflightRecord], error) {
	return doJSON[backupcontracts.PreflightRecord](c, ctx, http.MethodPost, "/v1/backup/contracts/preflights/"+url.PathEscape(preflightID)+"/retry", correlationID, map[string]any{})
}

func (c Client) CreateAgentAccessSession(ctx context.Context, correlationID string, input agents.CreateAccessSessionInput) (response.Envelope[agents.AccessSession], error) {
	return doJSON[agents.AccessSession](c, ctx, http.MethodPost, "/v1/agents/access-sessions", correlationID, input)
}

func (c Client) ListAgentAccessSessions(ctx context.Context, correlationID string, filter agents.AccessSessionFilter) (response.Envelope[[]agents.AccessSession], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.ActorRef != "" {
		values.Set("actor", filter.ActorRef)
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	path := "/v1/agents/access-sessions"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]agents.AccessSession](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetAgentAccessSession(ctx context.Context, correlationID, ref string) (response.Envelope[agents.AccessSession], error) {
	return doJSON[agents.AccessSession](c, ctx, http.MethodGet, "/v1/agents/access-sessions/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) CreateAgentWorkContext(ctx context.Context, correlationID string, input agents.CreateWorkContextInput) (response.Envelope[agents.WorkContextDetail], error) {
	return doJSON[agents.WorkContextDetail](c, ctx, http.MethodPost, "/v1/agents/work-contexts", correlationID, input)
}

func (c Client) ListAgentWorkContexts(ctx context.Context, correlationID string, filter agents.WorkContextFilter) (response.Envelope[[]agents.WorkContext], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.ActorRef != "" {
		values.Set("actor", filter.ActorRef)
	}
	if filter.AccessSessionRef != "" {
		values.Set("access_session", filter.AccessSessionRef)
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	path := "/v1/agents/work-contexts"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]agents.WorkContext](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetAgentWorkContext(ctx context.Context, correlationID, ref string) (response.Envelope[agents.WorkContextDetail], error) {
	return doJSON[agents.WorkContextDetail](c, ctx, http.MethodGet, "/v1/agents/work-contexts/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) GetAgentToolView(ctx context.Context, correlationID, ref string) (response.Envelope[agents.ToolViewDetail], error) {
	return doJSON[agents.ToolViewDetail](c, ctx, http.MethodGet, "/v1/agents/tool-views/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) SearchAgentTools(ctx context.Context, correlationID string, input agents.ToolSearchInput) (response.Envelope[agents.ToolSearchResult], error) {
	return doJSON[agents.ToolSearchResult](c, ctx, http.MethodPost, "/v1/agents/work-contexts/"+url.PathEscape(input.WorkContextRef)+"/tools/search", correlationID, input)
}

func (c Client) InspectAgentTool(ctx context.Context, correlationID string, input agents.ToolInspectInput) (response.Envelope[agents.ToolInspection], error) {
	values := url.Values{}
	if input.Query != "" {
		values.Set("query", input.Query)
	}
	if input.UsageSectionLabel != "" {
		values.Set("usage_section_label", input.UsageSectionLabel)
	}
	if input.MaxSections > 0 {
		values.Set("max_sections", strconv.Itoa(input.MaxSections))
	}
	if input.MaxCharsPerSection > 0 {
		values.Set("max_chars_per_section", strconv.Itoa(input.MaxCharsPerSection))
	}
	path := "/v1/agents/work-contexts/" + url.PathEscape(input.WorkContextRef) + "/tools/" + url.PathEscape(input.ToolRef) + "/inspect"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[agents.ToolInspection](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) CallAgentTool(ctx context.Context, correlationID string, input agents.AgentToolCallInput) (response.Envelope[agents.AgentToolCallOutcome], error) {
	path := "/v1/agents/work-contexts/" + url.PathEscape(input.WorkContextRef) + "/tools/" + url.PathEscape(input.ToolRef) + "/call"
	return doJSON[agents.AgentToolCallOutcome](c, ctx, http.MethodPost, path, correlationID, input)
}

func (c Client) GetAgentToolCall(ctx context.Context, correlationID, ref string) (response.Envelope[agents.AgentToolCall], error) {
	return doJSON[agents.AgentToolCall](c, ctx, http.MethodGet, "/v1/agents/tool-calls/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) WriteAgentWorklog(ctx context.Context, correlationID string, input agents.WriteWorklogInput) (response.Envelope[agents.WorklogEntry], error) {
	path := "/v1/agents/work-contexts/" + url.PathEscape(input.WorkContextRef) + "/worklog"
	return doJSON[agents.WorklogEntry](c, ctx, http.MethodPost, path, correlationID, input)
}

func (c Client) ListAgentWorklog(ctx context.Context, correlationID string, filter agents.WorklogFilter) (response.Envelope[[]agents.WorklogEntry], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	path := "/v1/agents/work-contexts/" + url.PathEscape(filter.WorkContextRef) + "/worklog"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]agents.WorklogEntry](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) BootstrapStatus(ctx context.Context, correlationID string) (response.Envelope[bootstrap.Summary], error) {
	return doJSON[bootstrap.Summary](c, ctx, http.MethodGet, "/v1/bootstrap/status", correlationID, nil)
}

func (c Client) ApplyBoxWatchPolicy(ctx context.Context, correlationID string, input box.WatchApplyInput) (response.Envelope[box.WatchApplyResult], error) {
	return doJSON[box.WatchApplyResult](c, ctx, http.MethodPost, "/v1/box/watch-policy/apply", correlationID, input)
}

func (c Client) GetBoxWatchStatus(ctx context.Context, correlationID string, input box.WatchStatusInput) (response.Envelope[box.WatchStatusResult], error) {
	return doJSON[box.WatchStatusResult](c, ctx, http.MethodPost, "/v1/box/watch-status", correlationID, input)
}

func (c Client) ListDropzoneUploadSessions(ctx context.Context, correlationID string, limit int) (response.Envelope[[]dropzone.UploadSession], error) {
	path := "/v1/box/dropzone/upload-sessions"
	if limit > 0 {
		values := url.Values{}
		values.Set("limit", strconv.Itoa(limit))
		path += "?" + values.Encode()
	}
	return doJSON[[]dropzone.UploadSession](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetDropzoneUploadSession(ctx context.Context, correlationID, sessionID string) (response.Envelope[dropzone.UploadSession], error) {
	return doJSON[dropzone.UploadSession](c, ctx, http.MethodGet, "/v1/box/dropzone/upload-sessions/"+url.PathEscape(sessionID), correlationID, nil)
}

func (c Client) GetActor(ctx context.Context, correlationID, ref string) (response.Envelope[identity.Actor], error) {
	return doJSON[identity.Actor](c, ctx, http.MethodGet, "/v1/actors/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) GetNode(ctx context.Context, correlationID, ref string) (response.Envelope[nodes.Node], error) {
	return doJSON[nodes.Node](c, ctx, http.MethodGet, "/v1/nodes/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) ListNodes(ctx context.Context, correlationID string, limit int) (response.Envelope[[]nodes.Node], error) {
	path := "/v1/nodes"
	if limit > 0 {
		values := url.Values{}
		values.Set("limit", strconv.Itoa(limit))
		path += "?" + values.Encode()
	}
	return doJSON[[]nodes.Node](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetNodeHealth(ctx context.Context, correlationID, ref string) (response.Envelope[nodes.NodeHealth], error) {
	return doJSON[nodes.NodeHealth](c, ctx, http.MethodGet, "/v1/nodes/"+url.PathEscape(ref)+"/health", correlationID, nil)
}

func (c Client) DecommissionNode(ctx context.Context, correlationID, ref string, input nodes.DecommissionNodeInput) (response.Envelope[nodes.DecommissionNodeResult], error) {
	return doJSON[nodes.DecommissionNodeResult](c, ctx, http.MethodPost, "/v1/nodes/"+url.PathEscape(ref)+"/decommission", correlationID, input)
}

func (c Client) IssueNodeCredential(ctx context.Context, correlationID, ref string, input nodes.IssueNodeCredentialInput) (response.Envelope[nodes.IssueNodeCredentialResult], error) {
	return doJSON[nodes.IssueNodeCredentialResult](c, ctx, http.MethodPost, "/v1/nodes/"+url.PathEscape(ref)+"/credentials/issue", correlationID, input)
}

func (c Client) CreateEnrollmentToken(ctx context.Context, correlationID string, input nodes.CreateEnrollmentTokenInput) (response.Envelope[nodes.CreateEnrollmentTokenResult], error) {
	return doJSON[nodes.CreateEnrollmentTokenResult](c, ctx, http.MethodPost, "/v1/node-enrollment-tokens", correlationID, input)
}

func (c Client) CreateEnrollmentRequest(ctx context.Context, correlationID string, input nodes.CreateEnrollmentRequestInput) (response.Envelope[nodes.EnrollmentRequest], error) {
	return doJSON[nodes.EnrollmentRequest](c, ctx, http.MethodPost, "/v1/node-enrollment-requests", correlationID, input)
}

func (c Client) ListEnrollmentRequests(ctx context.Context, correlationID string, filter nodes.ListEnrollmentRequestsFilter) (response.Envelope[[]nodes.EnrollmentRequest], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	path := "/v1/node-enrollment-requests"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]nodes.EnrollmentRequest](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetEnrollmentRequest(ctx context.Context, correlationID, ref string) (response.Envelope[nodes.EnrollmentRequest], error) {
	return doJSON[nodes.EnrollmentRequest](c, ctx, http.MethodGet, "/v1/node-enrollment-requests/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) ApproveEnrollment(ctx context.Context, correlationID, ref string, input nodes.ApproveEnrollmentInput) (response.Envelope[nodes.ApproveEnrollmentResult], error) {
	return doJSON[nodes.ApproveEnrollmentResult](c, ctx, http.MethodPost, "/v1/node-enrollment-requests/"+url.PathEscape(ref)+"/approve", correlationID, input)
}

func (c Client) DenyEnrollment(ctx context.Context, correlationID, ref string, input nodes.DenyEnrollmentInput) (response.Envelope[nodes.EnrollmentRequest], error) {
	return doJSON[nodes.EnrollmentRequest](c, ctx, http.MethodPost, "/v1/node-enrollment-requests/"+url.PathEscape(ref)+"/deny", correlationID, input)
}

func (c Client) ListScopes(ctx context.Context, correlationID string, limit int) (response.Envelope[[]scopes.Scope], error) {
	path := "/v1/scopes"
	if limit > 0 {
		values := url.Values{}
		values.Set("limit", strconv.Itoa(limit))
		path += "?" + values.Encode()
	}
	return doJSON[[]scopes.Scope](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetScope(ctx context.Context, correlationID, ref string) (response.Envelope[scopes.Scope], error) {
	return doJSON[scopes.Scope](c, ctx, http.MethodGet, "/v1/scopes/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) CreateScope(ctx context.Context, correlationID string, input scopes.CreateInput) (response.Envelope[scopes.CreateResult], error) {
	return doJSON[scopes.CreateResult](c, ctx, http.MethodPost, "/v1/scopes", correlationID, input)
}

func (c Client) ListEvents(ctx context.Context, correlationID string, filter events.ListFilter) (response.Envelope[[]events.Event], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.EventType != "" {
		values.Set("type", filter.EventType)
	}
	if filter.ActorRef != "" {
		values.Set("actor", filter.ActorRef)
	}
	if filter.NodeRef != "" {
		values.Set("node", filter.NodeRef)
	}
	if filter.ScopeRef != "" {
		values.Set("scope", filter.ScopeRef)
	}
	if filter.CorrelationID != "" {
		values.Set("correlation", filter.CorrelationID)
	}
	if filter.JobRef != "" {
		values.Set("job", filter.JobRef)
	}
	if filter.TargetKind != "" {
		values.Set("target_kind", filter.TargetKind)
	}
	if filter.TargetID != "" {
		values.Set("target_id", filter.TargetID)
	}

	path := "/v1/events"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]events.Event](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetEvent(ctx context.Context, correlationID, ref string) (response.Envelope[events.Event], error) {
	return doJSON[events.Event](c, ctx, http.MethodGet, "/v1/events/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) ListProjects(ctx context.Context, correlationID string, limit int) (response.Envelope[[]projects.Project], error) {
	path := "/v1/projects"
	if limit > 0 {
		values := url.Values{}
		values.Set("limit", strconv.Itoa(limit))
		path += "?" + values.Encode()
	}
	return doJSON[[]projects.Project](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetProject(ctx context.Context, correlationID, ref string) (response.Envelope[projects.ProjectDetail], error) {
	return doJSON[projects.ProjectDetail](c, ctx, http.MethodGet, "/v1/projects/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) CreateProject(ctx context.Context, correlationID string, input projects.CreateInput) (response.Envelope[projects.CreateResult], error) {
	return doJSON[projects.CreateResult](c, ctx, http.MethodPost, "/v1/projects", correlationID, input)
}

func (c Client) ScaffoldProject(ctx context.Context, correlationID string, input projectcontracts.ScaffoldOptions) (response.Envelope[projectcontracts.ScaffoldResult], error) {
	return scaffoldJSON(c, ctx, correlationID, input)
}

func (c Client) AddProjectFacets(ctx context.Context, correlationID string, input projectcontracts.AddProjectFacetsOptions) (response.Envelope[projectcontracts.AddProjectFacetsResult], error) {
	return doJSON[projectcontracts.AddProjectFacetsResult](c, ctx, http.MethodPost, "/v1/project-facet-additions", correlationID, input)
}

func (c Client) MigrateProjectLayout(ctx context.Context, correlationID string, input projectcontracts.LayoutMigrationOptions) (response.Envelope[projectcontracts.LayoutMigrationResult], error) {
	return doJSON[projectcontracts.LayoutMigrationResult](c, ctx, http.MethodPost, "/v1/project-layout-migrations", correlationID, input)
}

func (c Client) AnalyzeProjectContractBackend(ctx context.Context, correlationID string, input projectcontracts.BackendAnalysisInput) (response.Envelope[projectdoctor.BackendAnalysisResult], error) {
	return doJSON[projectdoctor.BackendAnalysisResult](c, ctx, http.MethodPost, "/v1/project-contract-analyses", correlationID, input)
}

// ExportProject downloads backend-generated archive bytes to the caller's
// filesystem. It never treats a path on the backend as a successful result.
func (c Client) ExportProject(ctx context.Context, correlationID string, input projectexport.Request, outputPath string, overwrite bool) (projectexport.Summary, error) {
	outputPath = filepath.Clean(strings.TrimSpace(outputPath))
	if outputPath == "." || outputPath == "" {
		return projectexport.Summary{}, fmt.Errorf("project export output path is required")
	}
	if _, err := os.Lstat(outputPath); err == nil && !overwrite {
		return projectexport.Summary{}, fmt.Errorf("project export output %q already exists; review it and pass --overwrite to replace it", outputPath)
	} else if err != nil && !os.IsNotExist(err) {
		return projectexport.Summary{}, fmt.Errorf("inspect project export output %q: %w", outputPath, err)
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return projectexport.Summary{}, err
	}
	const requestPath = "/v1/project-exports"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.requestURL(requestPath), bytes.NewReader(payload))
	if err != nil {
		return projectexport.Summary{}, err
	}
	req.Header.Set(correlation.Header, correlation.Normalize(correlationID))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return projectexport.Summary{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		failure, _ := io.ReadAll(resp.Body)
		var errorEnvelope response.ErrorEnvelope
		if err := json.Unmarshal(failure, &errorEnvelope); err == nil && !errorEnvelope.OK && errorEnvelope.Error.Code != "" {
			return projectexport.Summary{}, &RequestError{Method: http.MethodPost, Path: requestPath, StatusCode: resp.StatusCode, Envelope: errorEnvelope}
		}
		return projectexport.Summary{}, fmt.Errorf("loomd request %s %s failed with status %d: %s", http.MethodPost, requestPath, resp.StatusCode, string(failure))
	}
	summaryPayload, err := base64.RawURLEncoding.DecodeString(resp.Header.Get(projectexport.SummaryHeader))
	if err != nil || len(summaryPayload) == 0 {
		return projectexport.Summary{}, fmt.Errorf("backend project export response is missing a valid summary")
	}
	var result projectexport.Summary
	if err := json.Unmarshal(summaryPayload, &result); err != nil {
		return projectexport.Summary{}, fmt.Errorf("decode backend project export summary: %w", err)
	}
	if result.OutputPath != "" {
		return projectexport.Summary{}, fmt.Errorf("backend project export returned an unusable server-local output path")
	}
	if result.ArchiveBytes <= 0 || result.ArchiveBytes > projectexport.DefaultMaxBytes {
		return projectexport.Summary{}, fmt.Errorf("backend project export size %d is outside the client bound", result.ArchiveBytes)
	}
	partial, err := os.CreateTemp(filepath.Dir(outputPath), "."+filepath.Base(outputPath)+".partial-*")
	if err != nil {
		return projectexport.Summary{}, fmt.Errorf("create partial project export: %w", err)
	}
	partialPath := partial.Name()
	keep := false
	defer func() {
		_ = partial.Close()
		if !keep {
			_ = os.Remove(partialPath)
		}
	}()
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(partial, hash), io.LimitReader(resp.Body, result.ArchiveBytes+1))
	if copyErr != nil {
		return projectexport.Summary{}, fmt.Errorf("download backend project export: %w", copyErr)
	}
	if written != result.ArchiveBytes {
		return projectexport.Summary{}, fmt.Errorf("backend project export byte count mismatch: received %d, expected %d", written, result.ArchiveBytes)
	}
	checksum := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if checksum != result.ArchiveChecksum {
		return projectexport.Summary{}, fmt.Errorf("backend project export checksum mismatch: received %s, expected %s", checksum, result.ArchiveChecksum)
	}
	if err := partial.Sync(); err != nil {
		return projectexport.Summary{}, err
	}
	if err := partial.Close(); err != nil {
		return projectexport.Summary{}, err
	}
	if err := os.Rename(partialPath, outputPath); err != nil {
		return projectexport.Summary{}, fmt.Errorf("install backend project export %q: %w", outputPath, err)
	}
	keep = true
	absolute, _ := filepath.Abs(outputPath)
	result.OutputPath = absolute
	return result, nil
}

func (c Client) RegisterProjectContract(ctx context.Context, correlationID string, input projects.RegisterProjectContractInput) (response.Envelope[projects.RegisterProjectContractResult], error) {
	return doJSON[projects.RegisterProjectContractResult](c, ctx, http.MethodPost, "/v1/project-contract-registrations", correlationID, input)
}

func (c Client) RegisterProjectContractFromBackend(ctx context.Context, correlationID string, input projects.RegisterProjectContractFromBackendInput) (response.Envelope[projects.RegisterProjectContractResult], error) {
	return doJSON[projects.RegisterProjectContractResult](c, ctx, http.MethodPost, "/v1/project-contract-registrations/from-backend", correlationID, input)
}

func (c Client) GetProjectRegistrationStatus(ctx context.Context, correlationID, ref string) (response.Envelope[projects.ProjectRegistrationDetail], error) {
	return doJSON[projects.ProjectRegistrationDetail](c, ctx, http.MethodGet, "/v1/project-contract-registrations/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) ActivateProject(ctx context.Context, correlationID, ref string, input projects.ActivateProjectInput) (response.Envelope[projects.ProjectRegistrationDetail], error) {
	return doJSON[projects.ProjectRegistrationDetail](c, ctx, http.MethodPost, "/v1/projects/"+url.PathEscape(ref)+"/activate", correlationID, input)
}

func (c Client) DeactivateProject(ctx context.Context, correlationID, ref string, input projects.DeactivateProjectInput) (response.Envelope[projects.ProjectDeactivationResult], error) {
	return doJSON[projects.ProjectDeactivationResult](c, ctx, http.MethodPost, "/v1/projects/"+url.PathEscape(ref)+"/deactivate", correlationID, input)
}

func (c Client) ArchiveProject(ctx context.Context, correlationID, ref string, input storagearchive.ProjectArchiveInput) (response.Envelope[storagearchive.ProjectArchiveResult], error) {
	return doJSON[storagearchive.ProjectArchiveResult](c, ctx, http.MethodPost, "/v1/projects/"+url.PathEscape(ref)+"/archive", correlationID, input)
}

func (c Client) InspectProjectArchive(ctx context.Context, correlationID, ref string) (response.Envelope[storagearchive.ProjectArchiveInspectResult], error) {
	return doJSON[storagearchive.ProjectArchiveInspectResult](c, ctx, http.MethodGet, "/v1/projects/"+url.PathEscape(ref)+"/archive/inspect", correlationID, nil)
}

func (c Client) PlanProjectArchiveRestore(ctx context.Context, correlationID, ref string, input storagearchive.ProjectArchiveRestoreInput) (response.Envelope[storagearchive.ProjectArchiveRestorePlan], error) {
	return doJSON[storagearchive.ProjectArchiveRestorePlan](c, ctx, http.MethodPost, "/v1/projects/"+url.PathEscape(ref)+"/archive/restore", correlationID, input)
}

func (c Client) PlanProjectRuntimeMigration(ctx context.Context, correlationID, ref string, input storagearchive.ProjectRuntimeMigrationInput) (response.Envelope[storagearchive.ProjectRuntimeMigrationPlan], error) {
	return doJSON[storagearchive.ProjectRuntimeMigrationPlan](c, ctx, http.MethodPost, "/v1/projects/"+url.PathEscape(ref)+"/archive/migrate-runtime", correlationID, input)
}

func (c Client) BuildProjectWatchPlan(ctx context.Context, correlationID, ref string, input projectwatch.BuildPlanInput) (response.Envelope[projectwatch.ProjectWatchPlan], error) {
	values := url.Values{}
	if input.ProjectRoot != "" {
		values.Set("project_root", input.ProjectRoot)
	}
	if input.UseRegisteredSnapshot {
		values.Set("use_registered_snapshot", "true")
	}
	path := "/v1/projects/" + url.PathEscape(ref) + "/watch-plan"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[projectwatch.ProjectWatchPlan](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) ApplyProjectWatchPolicy(ctx context.Context, correlationID, ref string, input projects.ApplyProjectWatchPolicyInput) (response.Envelope[projects.ApplyProjectWatchPolicyResult], error) {
	return doJSON[projects.ApplyProjectWatchPolicyResult](c, ctx, http.MethodPost, "/v1/projects/"+url.PathEscape(ref)+"/watch-policy/apply", correlationID, input)
}

func (c Client) GetProjectSyncStatus(ctx context.Context, correlationID, ref string) (response.Envelope[projectwatch.ProjectSyncStatus], error) {
	return doJSON[projectwatch.ProjectSyncStatus](c, ctx, http.MethodGet, "/v1/projects/"+url.PathEscape(ref)+"/sync-status", correlationID, nil)
}

func (c Client) GetProjectBackupStatus(ctx context.Context, correlationID, ref string) (response.Envelope[projectwatch.ProjectBackupStatus], error) {
	return doJSON[projectwatch.ProjectBackupStatus](c, ctx, http.MethodGet, "/v1/projects/"+url.PathEscape(ref)+"/backup-status", correlationID, nil)
}

func (c Client) ListObjects(ctx context.Context, correlationID string, filter objects.ListFilter) (response.Envelope[[]objects.Object], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.ProjectRef != "" {
		values.Set("project", filter.ProjectRef)
	}
	if filter.ScopeRef != "" {
		values.Set("scope", filter.ScopeRef)
	}
	if filter.ObjectType != "" {
		values.Set("type", filter.ObjectType)
	}

	path := "/v1/objects"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]objects.Object](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetObject(ctx context.Context, correlationID, ref string) (response.Envelope[objects.ObjectDetail], error) {
	return doJSON[objects.ObjectDetail](c, ctx, http.MethodGet, "/v1/objects/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) ListObjectVersions(ctx context.Context, correlationID, ref string) (response.Envelope[[]objects.ObjectVersion], error) {
	return doJSON[[]objects.ObjectVersion](c, ctx, http.MethodGet, "/v1/objects/"+url.PathEscape(ref)+"/versions", correlationID, nil)
}

func (c Client) IngestObject(ctx context.Context, correlationID string, input objects.IngestFileInput) (response.Envelope[objects.IngestFileResult], error) {
	return doJSON[objects.IngestFileResult](c, ctx, http.MethodPost, "/v1/objects/ingest", correlationID, input)
}

func (c Client) Search(ctx context.Context, correlationID string, input search.SearchInput) (response.Envelope[search.SearchResultSet], error) {
	return doJSON[search.SearchResultSet](c, ctx, http.MethodPost, "/v1/search", correlationID, input)
}

func (c Client) ListFileTransfers(ctx context.Context, correlationID string, filter filetransfer.ListFilter) (response.Envelope[[]filetransfer.Status], error) {
	values := url.Values{}
	if strings.TrimSpace(filter.Status) != "" {
		values.Set("status", strings.TrimSpace(filter.Status))
	}
	if strings.TrimSpace(filter.TransferKind) != "" {
		values.Set("transfer_kind", strings.TrimSpace(filter.TransferKind))
	}
	if strings.TrimSpace(filter.SourceNodeKey) != "" {
		values.Set("source_node_key", strings.TrimSpace(filter.SourceNodeKey))
	}
	if strings.TrimSpace(filter.SourceRootKey) != "" {
		values.Set("source_root_key", strings.TrimSpace(filter.SourceRootKey))
	}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	path := "/v1/file-transfers"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]filetransfer.Status](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) ListStorageEntries(ctx context.Context, correlationID string, filter storagecatalog.ListFilter) (response.Envelope[[]storagecatalog.Entry], error) {
	values := storageListValues(filter)
	path := "/v1/storage/entries"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]storagecatalog.Entry](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) InspectStorageEntry(ctx context.Context, correlationID, ref string) (response.Envelope[storagecatalog.EntryDetail], error) {
	return doJSON[storagecatalog.EntryDetail](c, ctx, http.MethodGet, "/v1/storage/entries/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) RegisterStoragePhysicalRef(ctx context.Context, correlationID, storageEntryID string, input storagecatalog.RegisterPhysicalRefInput) (response.Envelope[storagecatalog.PhysicalRef], error) {
	return doJSON[storagecatalog.PhysicalRef](c, ctx, http.MethodPost, "/v1/storage/entries/"+url.PathEscape(storageEntryID)+"/physical-refs", correlationID, input)
}

func (c Client) ResolveStoragePath(ctx context.Context, correlationID, pathValue string) (response.Envelope[storageview.ResolveResult], error) {
	values := url.Values{}
	values.Set("path", pathValue)
	return doJSON[storageview.ResolveResult](c, ctx, http.MethodGet, "/v1/storage/resolve?"+values.Encode(), correlationID, nil)
}

func (c Client) InspectStoragePath(ctx context.Context, correlationID, pathValue string) (response.Envelope[storagecatalog.MainDocumentProtectionStatus], error) {
	values := url.Values{}
	values.Set("path", pathValue)
	return doJSON[storagecatalog.MainDocumentProtectionStatus](c, ctx, http.MethodGet, "/v1/storage/inspect-path?"+values.Encode(), correlationID, nil)
}

func (c Client) GetMainDocumentsStatus(ctx context.Context, correlationID string) (response.Envelope[mainstorage.Status], error) {
	return doJSON[mainstorage.Status](c, ctx, http.MethodGet, "/v1/storage/main-documents/status", correlationID, nil)
}

func (c Client) ReconcileMainDocuments(ctx context.Context, correlationID string, input mainstorage.ReconcileInput) (response.Envelope[mainstorage.ReconcileResult], error) {
	return doJSON[mainstorage.ReconcileResult](c, ctx, http.MethodPost, "/v1/storage/main-documents/reconcile", correlationID, input)
}

func (c Client) BackfillMainDocumentsRetention(ctx context.Context, correlationID string, input mainstorage.RetentionBackfillInput) (response.Envelope[mainstorage.RetentionBackfillResult], error) {
	return doJSON[mainstorage.RetentionBackfillResult](c, ctx, http.MethodPost, "/v1/storage/main-documents/retention/backfill", correlationID, input)
}

func (c Client) BackfillStorageFidelity(ctx context.Context, correlationID string, input storagefidelity.BackfillInput) (response.Envelope[storagefidelity.BackfillResult], error) {
	return doJSON[storagefidelity.BackfillResult](c, ctx, http.MethodPost, "/v1/storage/fidelity/backfill", correlationID, input)
}

func (c Client) GetMainDocumentProtection(ctx context.Context, correlationID string, input storagecatalog.MainDocumentProtectionInput) (response.Envelope[storagecatalog.MainDocumentProtectionStatus], error) {
	return doJSON[storagecatalog.MainDocumentProtectionStatus](c, ctx, http.MethodPost, "/v1/storage/main-documents/protection", correlationID, input)
}

func (c Client) CheckMainDocumentSafeDelete(ctx context.Context, correlationID string, input storagecatalog.MainDocumentProtectionInput) (response.Envelope[storagecatalog.MainDocumentProtectionStatus], error) {
	return doJSON[storagecatalog.MainDocumentProtectionStatus](c, ctx, http.MethodPost, "/v1/storage/main-documents/safe-delete", correlationID, input)
}

func (c Client) GetStorageFilesystemStatus(ctx context.Context, correlationID string) (response.Envelope[storagedoctor.FilesystemStatus], error) {
	return doJSON[storagedoctor.FilesystemStatus](c, ctx, http.MethodGet, "/v1/storage/filesystem/status", correlationID, nil)
}

func (c Client) GetStorageExportStatus(ctx context.Context, correlationID string) (response.Envelope[storagedoctor.ExportCompatibilityInfo], error) {
	return doJSON[storagedoctor.ExportCompatibilityInfo](c, ctx, http.MethodGet, "/v1/storage/export/status", correlationID, nil)
}

func (c Client) AcceptLaneCustody(ctx context.Context, correlationID string, input lane.AcceptInput) (response.Envelope[lane.AcceptResult], error) {
	return doJSON[lane.AcceptResult](c, ctx, http.MethodPost, "/v1/storage/lane/accept", correlationID, input)
}

func (c Client) GetStorageRetentionStatus(ctx context.Context, correlationID string) (response.Envelope[storagecatalog.RetentionStatus], error) {
	return doJSON[storagecatalog.RetentionStatus](c, ctx, http.MethodGet, "/v1/storage/retention/status", correlationID, nil)
}

func (c Client) ArchiveStorage(ctx context.Context, correlationID string, input storagearchive.ArchiveInput) (response.Envelope[storagearchive.ArchiveResult], error) {
	return doJSON[storagearchive.ArchiveResult](c, ctx, http.MethodPost, "/v1/storage/archive", correlationID, input)
}

func (c Client) CheckStorageSafeToDelete(ctx context.Context, correlationID string, input storageretention.SafeToDeleteInput) (response.Envelope[storageretention.SafeToDeleteResult], error) {
	return doJSON[storageretention.SafeToDeleteResult](c, ctx, http.MethodPost, "/v1/storage/safe-to-delete", correlationID, input)
}

func (c Client) FetchStorage(ctx context.Context, correlationID string, input storageretention.FetchInput) (response.Envelope[storageretention.FetchResult], error) {
	return doJSON[storageretention.FetchResult](c, ctx, http.MethodPost, "/v1/storage/fetch", correlationID, input)
}

func (c Client) RestoreStorage(ctx context.Context, correlationID string, input storageretention.RestoreInput) (response.Envelope[storageretention.RestoreResult], error) {
	return doJSON[storageretention.RestoreResult](c, ctx, http.MethodPost, "/v1/storage/restore", correlationID, input)
}

func (c Client) RecordStorageTombstone(ctx context.Context, correlationID string, input storageretention.RecordTombstoneInput) (response.Envelope[storageretention.RecordTombstoneResult], error) {
	return doJSON[storageretention.RecordTombstoneResult](c, ctx, http.MethodPost, "/v1/storage/tombstones", correlationID, input)
}

func (c Client) PushSyncBatch(ctx context.Context, correlationID string, input loomsync.PushBatchInput) (response.Envelope[loomsync.PushBatchResult], error) {
	return doJSON[loomsync.PushBatchResult](c, ctx, http.MethodPost, "/v1/node-agent/sync/batches", correlationID, input)
}

func (c Client) UploadSyncedObject(ctx context.Context, correlationID string, input loomsync.SyncedObjectInput) (response.Envelope[loomsync.SyncedObjectResult], error) {
	return doJSON[loomsync.SyncedObjectResult](c, ctx, http.MethodPost, "/v1/node-agent/sync/object-upload", correlationID, input)
}

func (c Client) PushPrivateBackup(ctx context.Context, correlationID string, input loomsync.PrivateBackupInput) (response.Envelope[loomsync.PrivateBackupResult], error) {
	return doJSON[loomsync.PrivateBackupResult](c, ctx, http.MethodPost, "/v1/node-agent/sync/private-backup", correlationID, input)
}

func (c Client) CreateDeletionRequest(ctx context.Context, correlationID string, input loomsync.DeletionRequestInput) (response.Envelope[loomsync.DeletionRequestResult], error) {
	return doJSON[loomsync.DeletionRequestResult](c, ctx, http.MethodPost, "/v1/node-agent/sync/deletion-request", correlationID, input)
}

func (c Client) GetSyncStatus(ctx context.Context, correlationID string, filter loomsync.ListFilter) (response.Envelope[loomsync.SyncStatus], error) {
	values := url.Values{}
	if filter.NodeRef != "" {
		values.Set("node", filter.NodeRef)
	}
	path := "/v1/sync/status"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[loomsync.SyncStatus](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) ListSyncBatches(ctx context.Context, correlationID string, filter loomsync.ListFilter) (response.Envelope[[]loomsync.SyncBatch], error) {
	values := syncListValues(filter)
	path := "/v1/sync/batches"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]loomsync.SyncBatch](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) ListSyncConflicts(ctx context.Context, correlationID string, filter loomsync.ListFilter) (response.Envelope[[]loomsync.SyncConflict], error) {
	values := syncListValues(filter)
	path := "/v1/sync/conflicts"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]loomsync.SyncConflict](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) ListSyncReplicas(ctx context.Context, correlationID string, filter loomsync.ListFilter) (response.Envelope[[]loomsync.SyncReplica], error) {
	values := syncListValues(filter)
	path := "/v1/sync/replicas"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]loomsync.SyncReplica](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) ListPrivateBackups(ctx context.Context, correlationID string, filter loomsync.ListFilter) (response.Envelope[[]loomsync.PrivateBackupOperation], error) {
	values := syncListValues(filter)
	path := "/v1/sync/private-backups"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]loomsync.PrivateBackupOperation](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) ListDeletionRequests(ctx context.Context, correlationID string, filter loomsync.ListFilter) (response.Envelope[[]loomsync.DeletionRequest], error) {
	values := syncListValues(filter)
	path := "/v1/sync/deletion-requests"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]loomsync.DeletionRequest](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetDeletionRequest(ctx context.Context, correlationID, ref string) (response.Envelope[loomsync.DeletionRequest], error) {
	return doJSON[loomsync.DeletionRequest](c, ctx, http.MethodGet, "/v1/sync/deletion-requests/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) ReviewDeletionRequest(ctx context.Context, correlationID string, input loomsync.DeletionRequestUpdateInput) (response.Envelope[loomsync.DeletionRequest], error) {
	return doJSON[loomsync.DeletionRequest](c, ctx, http.MethodPost, "/v1/sync/deletion-requests/"+url.PathEscape(input.RequestRef)+"/review", correlationID, input)
}

func (c Client) ApproveDeletionRequest(ctx context.Context, correlationID string, input loomsync.DeletionRequestUpdateInput) (response.Envelope[loomsync.DeletionRequest], error) {
	return doJSON[loomsync.DeletionRequest](c, ctx, http.MethodPost, "/v1/sync/deletion-requests/"+url.PathEscape(input.RequestRef)+"/approve", correlationID, input)
}

func (c Client) DenyDeletionRequest(ctx context.Context, correlationID string, input loomsync.DeletionRequestUpdateInput) (response.Envelope[loomsync.DeletionRequest], error) {
	return doJSON[loomsync.DeletionRequest](c, ctx, http.MethodPost, "/v1/sync/deletion-requests/"+url.PathEscape(input.RequestRef)+"/deny", correlationID, input)
}

func (c Client) CompleteDeletionRequest(ctx context.Context, correlationID string, input loomsync.DeletionRequestUpdateInput) (response.Envelope[loomsync.DeletionRequest], error) {
	return doJSON[loomsync.DeletionRequest](c, ctx, http.MethodPost, "/v1/sync/deletion-requests/"+url.PathEscape(input.RequestRef)+"/complete", correlationID, input)
}

func (c Client) ListWatchedRootStatus(ctx context.Context, correlationID string, filter mainwatchedroots.StatusFilter) (response.Envelope[[]mainwatchedroots.RootStatus], error) {
	values := watchedRootStatusValues(filter)
	path := "/v1/watched-roots/status"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]mainwatchedroots.RootStatus](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) ListWatchedRootFindings(ctx context.Context, correlationID string, filter mainwatchedroots.FindingFilter) (response.Envelope[[]mainwatchedroots.Finding], error) {
	values := watchedRootFindingValues(filter)
	path := "/v1/watched-roots/findings"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]mainwatchedroots.Finding](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetWatchedRootBackupStatus(ctx context.Context, correlationID string, filter mainwatchedroots.BackupFilter) (response.Envelope[mainwatchedroots.BackupStatus], error) {
	values := watchedRootBackupValues(filter)
	path := "/v1/watched-roots/backups/status"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[mainwatchedroots.BackupStatus](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) ListWatchedRootBackupBatches(ctx context.Context, correlationID string, filter mainwatchedroots.BackupFilter) (response.Envelope[[]mainwatchedroots.BackupBatch], error) {
	values := watchedRootBackupValues(filter)
	path := "/v1/watched-roots/backups/batches"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]mainwatchedroots.BackupBatch](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) ListWatchedRootBackupItems(ctx context.Context, correlationID string, filter mainwatchedroots.BackupItemFilter) (response.Envelope[[]mainwatchedroots.BackupItem], error) {
	values := watchedRootBackupItemValues(filter)
	path := "/v1/watched-roots/backups/items"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]mainwatchedroots.BackupItem](c, ctx, http.MethodGet, path, correlationID, nil)
}

func watchedRootStatusValues(filter mainwatchedroots.StatusFilter) url.Values {
	values := url.Values{}
	if filter.NodeRef != "" {
		values.Set("node", filter.NodeRef)
	}
	if filter.RootKey != "" {
		values.Set("root", filter.RootKey)
	}
	if filter.ProjectRef != "" {
		values.Set("project", filter.ProjectRef)
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	return values
}

func watchedRootFindingValues(filter mainwatchedroots.FindingFilter) url.Values {
	values := watchedRootStatusValues(mainwatchedroots.StatusFilter{
		NodeRef:    filter.NodeRef,
		RootKey:    filter.RootKey,
		ProjectRef: filter.ProjectRef,
		Status:     filter.Status,
		Limit:      filter.Limit,
	})
	if filter.Severity != "" {
		values.Set("severity", filter.Severity)
	}
	return values
}

func watchedRootBackupValues(filter mainwatchedroots.BackupFilter) url.Values {
	values := watchedRootStatusValues(mainwatchedroots.StatusFilter{
		NodeRef:    filter.NodeRef,
		RootKey:    filter.RootKey,
		ProjectRef: filter.ProjectRef,
		Status:     filter.Status,
		Limit:      filter.Limit,
	})
	return values
}

func watchedRootBackupItemValues(filter mainwatchedroots.BackupItemFilter) url.Values {
	values := watchedRootBackupValues(mainwatchedroots.BackupFilter{
		NodeRef:    filter.NodeRef,
		RootKey:    filter.RootKey,
		ProjectRef: filter.ProjectRef,
		Status:     filter.Status,
		Limit:      filter.Limit,
	})
	if filter.BatchRef != "" {
		values.Set("batch", filter.BatchRef)
	}
	if filter.Path != "" {
		values.Set("path", filter.Path)
	}
	return values
}

func syncListValues(filter loomsync.ListFilter) url.Values {
	values := url.Values{}
	if filter.NodeRef != "" {
		values.Set("node", filter.NodeRef)
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.ProjectRef != "" {
		values.Set("project", filter.ProjectRef)
	}
	if filter.ActiveOnly {
		values.Set("active_only", "true")
	}
	if filter.IncludeResolved {
		values.Set("include_resolved", "true")
	}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	return values
}

func (c Client) ListIndexStatus(ctx context.Context, correlationID string, filter search.StatusFilter) (response.Envelope[[]search.IndexStatus], error) {
	values := indexStatusValues(filter)
	path := "/v1/indexes/status"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]search.IndexStatus](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) ListIndexQueue(ctx context.Context, correlationID string, filter search.IndexQueueFilter) (response.Envelope[[]search.IndexStatus], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.ObjectRef != "" {
		values.Set("object", filter.ObjectRef)
	}
	if filter.ProjectRef != "" {
		values.Set("project", filter.ProjectRef)
	}
	if filter.ScopeRef != "" {
		values.Set("scope", filter.ScopeRef)
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.IncludeActive {
		values.Set("include_active", "true")
	}

	path := "/v1/indexes/queue"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]search.IndexStatus](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetIndexQueueItem(ctx context.Context, correlationID, ref string) (response.Envelope[search.IndexStatus], error) {
	return doJSON[search.IndexStatus](c, ctx, http.MethodGet, "/v1/indexes/queue/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) ListIndexFailures(ctx context.Context, correlationID string, filter search.StatusFilter) (response.Envelope[[]search.IndexStatus], error) {
	values := indexStatusValues(filter)
	path := "/v1/indexes/failures"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]search.IndexStatus](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) ExplainIndexObject(ctx context.Context, correlationID string, input search.IndexExplainInput) (response.Envelope[search.IndexExplainResult], error) {
	values := url.Values{}
	values.Set("object", input.ObjectRef)
	path := "/v1/indexes/explain?" + values.Encode()
	return doJSON[search.IndexExplainResult](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) RetryIndexWork(ctx context.Context, correlationID string, input search.IndexStatusRefInput) (response.Envelope[search.IndexStatus], error) {
	return doJSON[search.IndexStatus](c, ctx, http.MethodPost, "/v1/indexes/retry", correlationID, input)
}

func (c Client) RetryFailedIndexWork(ctx context.Context, correlationID string, input search.IndexRetryFailedInput) (response.Envelope[search.IndexRetrySummary], error) {
	return doJSON[search.IndexRetrySummary](c, ctx, http.MethodPost, "/v1/indexes/retry-failed", correlationID, input)
}

func (c Client) RebuildIndexObject(ctx context.Context, correlationID string, input search.RebuildInput) (response.Envelope[search.IndexResult], error) {
	return doJSON[search.IndexResult](c, ctx, http.MethodPost, "/v1/indexes/rebuild/object", correlationID, input)
}

func (c Client) RebuildIndex(ctx context.Context, correlationID string, input search.RebuildInput) (response.Envelope[search.IndexResult], error) {
	return doJSON[search.IndexResult](c, ctx, http.MethodPost, "/v1/index/rebuild", correlationID, input)
}

func (c Client) CreateSchedule(ctx context.Context, correlationID string, input automation.CreateScheduleInput) (response.Envelope[automation.ScheduleDetail], error) {
	return doJSON[automation.ScheduleDetail](c, ctx, http.MethodPost, "/v1/schedules", correlationID, input)
}

func (c Client) ListSchedules(ctx context.Context, correlationID string, filter automation.ScheduleFilter) (response.Envelope[[]automation.Schedule], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.AutomationRef != "" {
		values.Set("automation", filter.AutomationRef)
	}
	if filter.ProjectRef != "" {
		values.Set("project", filter.ProjectRef)
	}
	path := "/v1/schedules"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]automation.Schedule](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) ScheduleStatus(ctx context.Context, correlationID string) (response.Envelope[automation.ScheduleStatus], error) {
	return doJSON[automation.ScheduleStatus](c, ctx, http.MethodGet, "/v1/schedules/status", correlationID, nil)
}

func (c Client) GetSchedule(ctx context.Context, correlationID, ref string) (response.Envelope[automation.ScheduleDetail], error) {
	return doJSON[automation.ScheduleDetail](c, ctx, http.MethodGet, "/v1/schedules/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) FireScheduleNow(ctx context.Context, correlationID, ref string, input automation.FireScheduleInput) (response.Envelope[automation.FireScheduleResult], error) {
	return doJSON[automation.FireScheduleResult](c, ctx, http.MethodPost, "/v1/schedules/"+url.PathEscape(ref)+"/fire", correlationID, input)
}

func (c Client) PauseSchedule(ctx context.Context, correlationID, ref string, input automation.UpdateScheduleStatusInput) (response.Envelope[automation.ScheduleDetail], error) {
	return doJSON[automation.ScheduleDetail](c, ctx, http.MethodPost, "/v1/schedules/"+url.PathEscape(ref)+"/pause", correlationID, input)
}

func (c Client) ResumeSchedule(ctx context.Context, correlationID, ref string, input automation.UpdateScheduleStatusInput) (response.Envelope[automation.ScheduleDetail], error) {
	return doJSON[automation.ScheduleDetail](c, ctx, http.MethodPost, "/v1/schedules/"+url.PathEscape(ref)+"/resume", correlationID, input)
}

func (c Client) DisableSchedule(ctx context.Context, correlationID, ref string, input automation.UpdateScheduleStatusInput) (response.Envelope[automation.ScheduleDetail], error) {
	return doJSON[automation.ScheduleDetail](c, ctx, http.MethodPost, "/v1/schedules/"+url.PathEscape(ref)+"/disable", correlationID, input)
}

func (c Client) ListAutomations(ctx context.Context, correlationID string, filter automation.AutomationFilter) (response.Envelope[[]automation.Automation], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.SourceKind != "" {
		values.Set("source_kind", filter.SourceKind)
	}
	path := "/v1/automations"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]automation.Automation](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetAutomation(ctx context.Context, correlationID, ref string) (response.Envelope[automation.AutomationDetail], error) {
	return doJSON[automation.AutomationDetail](c, ctx, http.MethodGet, "/v1/automations/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) CreateIntegration(ctx context.Context, correlationID string, input automation.CreateIntegrationInput) (response.Envelope[automation.IntegrationDetail], error) {
	return doJSON[automation.IntegrationDetail](c, ctx, http.MethodPost, "/v1/integrations", correlationID, input)
}

func (c Client) ListIntegrations(ctx context.Context, correlationID string, filter automation.IntegrationFilter) (response.Envelope[[]automation.Integration], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	path := "/v1/integrations"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]automation.Integration](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetIntegration(ctx context.Context, correlationID, ref string) (response.Envelope[automation.IntegrationDetail], error) {
	return doJSON[automation.IntegrationDetail](c, ctx, http.MethodGet, "/v1/integrations/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) DisableIntegration(ctx context.Context, correlationID, ref string, input automation.UpdateIntegrationStatusInput) (response.Envelope[automation.IntegrationDetail], error) {
	return doJSON[automation.IntegrationDetail](c, ctx, http.MethodPost, "/v1/integrations/"+url.PathEscape(ref)+"/disable", correlationID, input)
}

func (c Client) RevokeIntegration(ctx context.Context, correlationID, ref string, input automation.UpdateIntegrationStatusInput) (response.Envelope[automation.IntegrationDetail], error) {
	return doJSON[automation.IntegrationDetail](c, ctx, http.MethodPost, "/v1/integrations/"+url.PathEscape(ref)+"/revoke", correlationID, input)
}

func (c Client) CreateIntegrationAuthProfile(ctx context.Context, correlationID, integrationRef string, input automation.CreateIntegrationAuthProfileInput) (response.Envelope[automation.IntegrationAuthProfileCreateResult], error) {
	return doJSON[automation.IntegrationAuthProfileCreateResult](c, ctx, http.MethodPost, "/v1/integrations/"+url.PathEscape(integrationRef)+"/auth-profiles", correlationID, input)
}

func (c Client) ListIntegrationAuthProfiles(ctx context.Context, correlationID string, filter automation.IntegrationAuthProfileFilter) (response.Envelope[[]automation.IntegrationAuthProfile], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	path := "/v1/integrations/" + url.PathEscape(filter.IntegrationRef) + "/auth-profiles"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]automation.IntegrationAuthProfile](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) RevokeIntegrationAuthProfile(ctx context.Context, correlationID, ref string, input automation.UpdateIntegrationAuthProfileStatusInput) (response.Envelope[automation.IntegrationAuthProfile], error) {
	return doJSON[automation.IntegrationAuthProfile](c, ctx, http.MethodPost, "/v1/integration-auth-profiles/"+url.PathEscape(ref)+"/revoke", correlationID, input)
}

func (c Client) CreateDirectEventEndpoint(ctx context.Context, correlationID string, input automation.CreateDirectEventEndpointInput) (response.Envelope[automation.DirectEventEndpointDetail], error) {
	return doJSON[automation.DirectEventEndpointDetail](c, ctx, http.MethodPost, "/v1/direct-event-endpoints", correlationID, input)
}

func (c Client) ListDirectEventEndpoints(ctx context.Context, correlationID string, filter automation.DirectEventEndpointFilter) (response.Envelope[[]automation.DirectEventEndpoint], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.IntegrationRef != "" {
		values.Set("integration", filter.IntegrationRef)
	}
	if filter.AutomationRef != "" {
		values.Set("automation", filter.AutomationRef)
	}
	if filter.ProjectRef != "" {
		values.Set("project", filter.ProjectRef)
	}
	path := "/v1/direct-event-endpoints"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]automation.DirectEventEndpoint](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetDirectEventEndpoint(ctx context.Context, correlationID, ref string) (response.Envelope[automation.DirectEventEndpointDetail], error) {
	return doJSON[automation.DirectEventEndpointDetail](c, ctx, http.MethodGet, "/v1/direct-event-endpoints/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) PauseDirectEventEndpoint(ctx context.Context, correlationID, ref string, input automation.UpdateDirectEventEndpointStatusInput) (response.Envelope[automation.DirectEventEndpointDetail], error) {
	return doJSON[automation.DirectEventEndpointDetail](c, ctx, http.MethodPost, "/v1/direct-event-endpoints/"+url.PathEscape(ref)+"/pause", correlationID, input)
}

func (c Client) ResumeDirectEventEndpoint(ctx context.Context, correlationID, ref string, input automation.UpdateDirectEventEndpointStatusInput) (response.Envelope[automation.DirectEventEndpointDetail], error) {
	return doJSON[automation.DirectEventEndpointDetail](c, ctx, http.MethodPost, "/v1/direct-event-endpoints/"+url.PathEscape(ref)+"/resume", correlationID, input)
}

func (c Client) DisableDirectEventEndpoint(ctx context.Context, correlationID, ref string, input automation.UpdateDirectEventEndpointStatusInput) (response.Envelope[automation.DirectEventEndpointDetail], error) {
	return doJSON[automation.DirectEventEndpointDetail](c, ctx, http.MethodPost, "/v1/direct-event-endpoints/"+url.PathEscape(ref)+"/disable", correlationID, input)
}

func (c Client) PreviewDirectEventEndpointMapping(ctx context.Context, correlationID, ref string, input automation.MappingPreviewInput) (response.Envelope[automation.MappingPreviewResult], error) {
	return doJSON[automation.MappingPreviewResult](c, ctx, http.MethodPost, "/v1/direct-event-endpoints/"+url.PathEscape(ref)+"/preview", correlationID, input)
}

func (c Client) IngestDirectEvent(ctx context.Context, correlationID, slug string, input automation.DirectEventLocalIngestInput) (response.Envelope[automation.DirectEventIngestResult], error) {
	values := url.Values{}
	if input.QueryToken != "" {
		values.Set("loom_token", input.QueryToken)
	}
	path := "/v1/direct-events/ingest/" + url.PathEscape(slug)
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	body := input.BodyJSON
	if len(body) == 0 {
		body = json.RawMessage(`{}`)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.requestURL(path), bytes.NewReader(body))
	if err != nil {
		return response.Envelope[automation.DirectEventIngestResult]{}, err
	}
	req.Header.Set(correlation.Header, correlation.Normalize(correlationID))
	req.Header.Set("Content-Type", "application/json")
	if input.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+input.BearerToken)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return response.Envelope[automation.DirectEventIngestResult]{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(resp.Body)
		var errorEnvelope response.ErrorEnvelope
		if err := json.Unmarshal(payload, &errorEnvelope); err == nil && !errorEnvelope.OK && errorEnvelope.Error.Code != "" {
			return response.Envelope[automation.DirectEventIngestResult]{}, &RequestError{
				Method:     http.MethodPost,
				Path:       path,
				StatusCode: resp.StatusCode,
				Envelope:   errorEnvelope,
			}
		}
		if len(payload) > 0 {
			return response.Envelope[automation.DirectEventIngestResult]{}, fmt.Errorf("loomd request %s %s failed with status %d: %s", http.MethodPost, path, resp.StatusCode, string(payload))
		}
		return response.Envelope[automation.DirectEventIngestResult]{}, fmt.Errorf("loomd request %s %s failed with status %d", http.MethodPost, path, resp.StatusCode)
	}
	var envelope response.Envelope[automation.DirectEventIngestResult]
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return response.Envelope[automation.DirectEventIngestResult]{}, err
	}
	return envelope, nil
}

func (c Client) ListDirectEvents(ctx context.Context, correlationID string, filter automation.DirectEventFilter) (response.Envelope[[]automation.DirectEvent], error) {
	values := directEventFilterValues(filter)
	path := "/v1/direct-events"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]automation.DirectEvent](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) ListDirectEventFailures(ctx context.Context, correlationID string, filter automation.DirectEventFilter) (response.Envelope[[]automation.DirectEvent], error) {
	values := directEventFilterValues(filter)
	path := "/v1/direct-events/failures"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]automation.DirectEvent](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) DirectEventStatus(ctx context.Context, correlationID string) (response.Envelope[automation.DirectEventStatus], error) {
	return doJSON[automation.DirectEventStatus](c, ctx, http.MethodGet, "/v1/direct-events/status", correlationID, nil)
}

func (c Client) GetDirectEvent(ctx context.Context, correlationID, ref string) (response.Envelope[automation.DirectEventDetail], error) {
	return doJSON[automation.DirectEventDetail](c, ctx, http.MethodGet, "/v1/direct-events/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) GetDirectEventRawPayload(ctx context.Context, correlationID, ref string) (response.Envelope[automation.DirectEventRawPayload], error) {
	return doJSON[automation.DirectEventRawPayload](c, ctx, http.MethodGet, "/v1/direct-events/"+url.PathEscape(ref)+"/raw", correlationID, nil)
}

func (c Client) ListScheduleFires(ctx context.Context, correlationID string, filter automation.ScheduleFireFilter) (response.Envelope[[]automation.ScheduleFire], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.ScheduleRef != "" {
		values.Set("schedule", filter.ScheduleRef)
	}
	if filter.AutomationRef != "" {
		values.Set("automation", filter.AutomationRef)
	}
	path := "/v1/schedule-fires"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]automation.ScheduleFire](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetScheduleFire(ctx context.Context, correlationID, ref string) (response.Envelope[automation.ScheduleFire], error) {
	return doJSON[automation.ScheduleFire](c, ctx, http.MethodGet, "/v1/schedule-fires/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) ListInvocations(ctx context.Context, correlationID string, filter automation.InvocationFilter) (response.Envelope[[]automation.Invocation], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.AutomationRef != "" {
		values.Set("automation", filter.AutomationRef)
	}
	if filter.ProjectRef != "" {
		values.Set("project", filter.ProjectRef)
	}
	if filter.SourceKind != "" {
		values.Set("source_kind", filter.SourceKind)
	}
	path := "/v1/invocations"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]automation.Invocation](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) ListInvocationFailures(ctx context.Context, correlationID string, filter automation.InvocationFilter) (response.Envelope[[]automation.Invocation], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.AutomationRef != "" {
		values.Set("automation", filter.AutomationRef)
	}
	if filter.ProjectRef != "" {
		values.Set("project", filter.ProjectRef)
	}
	if filter.SourceKind != "" {
		values.Set("source_kind", filter.SourceKind)
	}
	path := "/v1/invocations/failures"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]automation.Invocation](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetInvocation(ctx context.Context, correlationID, ref string) (response.Envelope[automation.Invocation], error) {
	return doJSON[automation.Invocation](c, ctx, http.MethodGet, "/v1/invocations/"+url.PathEscape(ref), correlationID, nil)
}

func indexStatusValues(filter search.StatusFilter) url.Values {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.ObjectRef != "" {
		values.Set("object", filter.ObjectRef)
	}
	if filter.ProjectRef != "" {
		values.Set("project", filter.ProjectRef)
	}
	if filter.ScopeRef != "" {
		values.Set("scope", filter.ScopeRef)
	}
	if filter.IndexType != "" {
		values.Set("index_type", filter.IndexType)
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.FailedOnly {
		values.Set("failed", "true")
	}
	return values
}

func directEventFilterValues(filter automation.DirectEventFilter) url.Values {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.EndpointRef != "" {
		values.Set("endpoint", filter.EndpointRef)
	}
	if filter.IntegrationRef != "" {
		values.Set("integration", filter.IntegrationRef)
	}
	if filter.AutomationRef != "" {
		values.Set("automation", filter.AutomationRef)
	}
	if filter.ProjectRef != "" {
		values.Set("project", filter.ProjectRef)
	}
	return values
}

func (c Client) RegisterScript(ctx context.Context, correlationID string, input scripts.RegisterInput) (response.Envelope[scripts.RegisterResult], error) {
	return doJSON[scripts.RegisterResult](c, ctx, http.MethodPost, "/v1/scripts/register", correlationID, input)
}

func (c Client) ListScripts(ctx context.Context, correlationID string, filter scripts.ListFilter) (response.Envelope[[]scripts.Script], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.ProjectRef != "" {
		values.Set("project", filter.ProjectRef)
	}
	if filter.ScopeRef != "" {
		values.Set("scope", filter.ScopeRef)
	}
	path := "/v1/scripts"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]scripts.Script](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetScript(ctx context.Context, correlationID, ref string) (response.Envelope[scripts.ScriptDetail], error) {
	return doJSON[scripts.ScriptDetail](c, ctx, http.MethodGet, "/v1/scripts/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) RunScript(ctx context.Context, correlationID, ref string, input jobs.CreateScriptRunInput) (response.Envelope[jobs.RunResult], error) {
	return doJSON[jobs.RunResult](c, ctx, http.MethodPost, "/v1/scripts/"+url.PathEscape(ref)+"/run", correlationID, input)
}

func (c Client) ListJobs(ctx context.Context, correlationID string, filter jobs.ListFilter) (response.Envelope[[]jobs.Job], error) {
	path := "/v1/jobs"
	if encoded := jobListValues(filter).Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]jobs.Job](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) ListQueuedJobs(ctx context.Context, correlationID string, filter jobs.ListFilter) (response.Envelope[[]jobs.Job], error) {
	path := "/v1/jobs/queue"
	if encoded := jobListValues(filter).Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]jobs.Job](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) ListFailedJobs(ctx context.Context, correlationID string, filter jobs.ListFilter) (response.Envelope[[]jobs.Job], error) {
	path := "/v1/jobs/failures"
	if encoded := jobListValues(filter).Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]jobs.Job](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) JobStatus(ctx context.Context, correlationID string) (response.Envelope[jobs.QueueSummary], error) {
	return doJSON[jobs.QueueSummary](c, ctx, http.MethodGet, "/v1/jobs/status", correlationID, nil)
}

func (c Client) RunNextJob(ctx context.Context, correlationID string, input workers.RunOnceInput) (response.Envelope[workers.RunOnceResult], error) {
	return doJSON[workers.RunOnceResult](c, ctx, http.MethodPost, "/v1/jobs/run-next", correlationID, input)
}

func jobListValues(filter jobs.ListFilter) url.Values {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.JobType != "" {
		values.Set("type", filter.JobType)
	}
	if filter.ScriptRef != "" {
		values.Set("script", filter.ScriptRef)
	}
	if filter.WorkflowRef != "" {
		values.Set("workflow", filter.WorkflowRef)
	}
	if filter.ProjectRef != "" {
		values.Set("project", filter.ProjectRef)
	}
	if filter.ScopeRef != "" {
		values.Set("scope", filter.ScopeRef)
	}
	if filter.ManualOnly {
		values.Set("manual", "true")
	}
	if filter.AttentionStatus != "" {
		values.Set("attention_status", filter.AttentionStatus)
	}
	if filter.IncludeAcknowledged {
		values.Set("include_acknowledged", "true")
	}
	if filter.IncludeArchived {
		values.Set("include_archived", "true")
	}
	return values
}

func (c Client) GetJob(ctx context.Context, correlationID, ref string) (response.Envelope[jobs.JobDetail], error) {
	return doJSON[jobs.JobDetail](c, ctx, http.MethodGet, "/v1/jobs/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) GetJobLogs(ctx context.Context, correlationID, ref string) (response.Envelope[[]jobs.JobLog], error) {
	return doJSON[[]jobs.JobLog](c, ctx, http.MethodGet, "/v1/jobs/"+url.PathEscape(ref)+"/logs", correlationID, nil)
}

func (c Client) GetJobOutputs(ctx context.Context, correlationID, ref string) (response.Envelope[[]jobs.JobOutput], error) {
	return doJSON[[]jobs.JobOutput](c, ctx, http.MethodGet, "/v1/jobs/"+url.PathEscape(ref)+"/outputs", correlationID, nil)
}

func (c Client) CancelJob(ctx context.Context, correlationID string, input jobs.CancelJobInput) (response.Envelope[jobs.Job], error) {
	return doJSON[jobs.Job](c, ctx, http.MethodPost, "/v1/jobs/"+url.PathEscape(input.JobRef)+"/cancel", correlationID, input)
}

func (c Client) RetryJob(ctx context.Context, correlationID string, input jobs.RetryJobInput) (response.Envelope[jobs.Job], error) {
	return doJSON[jobs.Job](c, ctx, http.MethodPost, "/v1/jobs/"+url.PathEscape(input.JobRef)+"/retry", correlationID, input)
}

func (c Client) AcknowledgeJobAttention(ctx context.Context, correlationID string, input jobs.JobAttentionInput) (response.Envelope[jobs.Job], error) {
	return doJSON[jobs.Job](c, ctx, http.MethodPost, "/v1/jobs/"+url.PathEscape(input.JobRef)+"/acknowledge", correlationID, input)
}

func (c Client) ArchiveJobAttention(ctx context.Context, correlationID string, input jobs.JobAttentionInput) (response.Envelope[jobs.Job], error) {
	return doJSON[jobs.Job](c, ctx, http.MethodPost, "/v1/jobs/"+url.PathEscape(input.JobRef)+"/archive", correlationID, input)
}

func (c Client) ListRunners(ctx context.Context, correlationID string, filter jobs.RunnerFilter) (response.Envelope[[]jobs.Runner], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.NodeID != "" {
		values.Set("node", filter.NodeID)
	}
	path := "/v1/runners"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]jobs.Runner](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) RunnerStatus(ctx context.Context, correlationID string) (response.Envelope[jobs.QueueSummary], error) {
	return doJSON[jobs.QueueSummary](c, ctx, http.MethodGet, "/v1/runners/status", correlationID, nil)
}

func (c Client) GetRunner(ctx context.Context, correlationID, ref string) (response.Envelope[jobs.Runner], error) {
	return doJSON[jobs.Runner](c, ctx, http.MethodGet, "/v1/runners/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) ListArtifacts(ctx context.Context, correlationID string, filter artifacts.ListFilter) (response.Envelope[[]artifacts.Artifact], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.JobRef != "" {
		values.Set("job", filter.JobRef)
	}
	if filter.ScopeRef != "" {
		values.Set("scope", filter.ScopeRef)
	}
	if filter.ObjectRef != "" {
		values.Set("object", filter.ObjectRef)
	}
	path := "/v1/artifacts"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]artifacts.Artifact](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetArtifact(ctx context.Context, correlationID, ref string) (response.Envelope[artifacts.ArtifactDetail], error) {
	return doJSON[artifacts.ArtifactDetail](c, ctx, http.MethodGet, "/v1/artifacts/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) ListProviderAdvertisements(ctx context.Context, correlationID string, filter capabilities.ProviderAdvertisementFilter) (response.Envelope[[]capabilities.ProviderAdvertisement], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.NodeRef != "" {
		values.Set("node", filter.NodeRef)
	}
	if filter.ProviderRef != "" {
		values.Set("provider", filter.ProviderRef)
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	path := "/v1/provider-advertisements"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]capabilities.ProviderAdvertisement](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetProviderAdvertisement(ctx context.Context, correlationID, ref string) (response.Envelope[capabilities.ProviderAdvertisementInspection], error) {
	return doJSON[capabilities.ProviderAdvertisementInspection](c, ctx, http.MethodGet, "/v1/provider-advertisements/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) ApproveProviderAdvertisement(ctx context.Context, correlationID, ref string, input capabilities.ApproveProviderAdvertisementInput) (response.Envelope[capabilities.ProviderAdvertisementInspection], error) {
	return doJSON[capabilities.ProviderAdvertisementInspection](c, ctx, http.MethodPost, "/v1/provider-advertisements/"+url.PathEscape(ref)+"/approve", correlationID, input)
}

func (c Client) RejectProviderAdvertisement(ctx context.Context, correlationID, ref string, input capabilities.RejectProviderAdvertisementInput) (response.Envelope[capabilities.ProviderAdvertisementInspection], error) {
	return doJSON[capabilities.ProviderAdvertisementInspection](c, ctx, http.MethodPost, "/v1/provider-advertisements/"+url.PathEscape(ref)+"/reject", correlationID, input)
}

func (c Client) ListProviders(ctx context.Context, correlationID string, filter capabilities.ProviderFilter) (response.Envelope[[]capabilities.ProviderListItem], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.NodeRef != "" {
		values.Set("node", filter.NodeRef)
	}
	if filter.ScopeRef != "" {
		values.Set("scope", filter.ScopeRef)
	}
	if filter.ProjectRef != "" {
		values.Set("project", filter.ProjectRef)
	}
	if filter.ProviderType != "" {
		values.Set("type", filter.ProviderType)
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.Health != "" {
		values.Set("health", filter.Health)
	}
	if filter.RequireActiveEndpoint {
		values.Set("require_active_endpoint", "true")
	}
	path := "/v1/providers"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]capabilities.ProviderListItem](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetProvider(ctx context.Context, correlationID, ref string) (response.Envelope[capabilities.ProviderInspection], error) {
	return doJSON[capabilities.ProviderInspection](c, ctx, http.MethodGet, "/v1/providers/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) GetProviderHealth(ctx context.Context, correlationID, ref string) (response.Envelope[capabilities.ProviderHealth], error) {
	return doJSON[capabilities.ProviderHealth](c, ctx, http.MethodGet, "/v1/providers/"+url.PathEscape(ref)+"/health", correlationID, nil)
}

func (c Client) ListServices(ctx context.Context, correlationID string, filter serviceregistry.ServiceFilter) (response.Envelope[[]serviceregistry.ServiceListItem], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.NodeRef != "" {
		values.Set("node", filter.NodeRef)
	}
	if filter.ScopeRef != "" {
		values.Set("scope", filter.ScopeRef)
	}
	if filter.ProjectRef != "" {
		values.Set("project", filter.ProjectRef)
	}
	if filter.RegistryState != "" {
		values.Set("status", string(filter.RegistryState))
	}
	if filter.Health != "" {
		values.Set("health", filter.Health)
	}
	path := "/v1/services"
	if query := values.Encode(); query != "" {
		path += "?" + query
	}
	return doJSON[[]serviceregistry.ServiceListItem](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetService(ctx context.Context, correlationID, ref string) (response.Envelope[serviceregistry.ServiceInspection], error) {
	return doJSON[serviceregistry.ServiceInspection](c, ctx, http.MethodGet, "/v1/services/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) PlanServiceRegistration(ctx context.Context, correlationID string, input serviceregistry.ProjectRegistrationInput) (response.Envelope[serviceregistry.RegistrationPlan], error) {
	return doJSON[serviceregistry.RegistrationPlan](c, ctx, http.MethodPost, "/v1/service-registrations/plan", correlationID, map[string]any{"registration": input})
}

func (c Client) ApplyServiceRegistration(ctx context.Context, correlationID string, input serviceregistry.ProjectRegistrationInput) (response.Envelope[serviceregistry.RegistrationResult], error) {
	return doJSON[serviceregistry.RegistrationResult](c, ctx, http.MethodPost, "/v1/service-registrations/apply", correlationID, map[string]any{"registration": input})
}

func (c Client) OperateService(ctx context.Context, correlationID, ref string, operation serviceregistry.Operation, input map[string]any) (response.Envelope[routing.CapabilityCallOutcome], error) {
	return doJSON[routing.CapabilityCallOutcome](c, ctx, http.MethodPost, "/v1/services/"+url.PathEscape(ref)+"/"+url.PathEscape(string(operation)), correlationID, input)
}

func (c Client) ListCapabilities(ctx context.Context, correlationID string, filter capabilities.CapabilityFilter) (response.Envelope[[]capabilities.CapabilityListItem], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.ProviderRef != "" {
		values.Set("provider", filter.ProviderRef)
	}
	if filter.ClassRef != "" {
		values.Set("class", filter.ClassRef)
	}
	if filter.NodeRef != "" {
		values.Set("node", filter.NodeRef)
	}
	if filter.ScopeRef != "" {
		values.Set("scope", filter.ScopeRef)
	}
	if filter.ProjectRef != "" {
		values.Set("project", filter.ProjectRef)
	}
	if filter.Form != "" {
		values.Set("form", filter.Form)
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.Risk != "" {
		values.Set("risk", filter.Risk)
	}
	if filter.AuthorizationLevel > 0 {
		values.Set("authorization_level", strconv.Itoa(filter.AuthorizationLevel))
	}
	path := "/v1/capabilities"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]capabilities.CapabilityListItem](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetCapability(ctx context.Context, correlationID, ref string) (response.Envelope[capabilities.CapabilityInspection], error) {
	return doJSON[capabilities.CapabilityInspection](c, ctx, http.MethodGet, "/v1/capabilities/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) GetCapabilityUsageDocs(ctx context.Context, correlationID, ref string) (response.Envelope[[]capabilities.UsageDocument], error) {
	return doJSON[[]capabilities.UsageDocument](c, ctx, http.MethodGet, "/v1/capabilities/"+url.PathEscape(ref)+"/usage-docs", correlationID, nil)
}

func (c Client) SearchCapabilities(ctx context.Context, correlationID string, input capabilities.CapabilitySearchInput) (response.Envelope[[]capabilities.CapabilityCandidate], error) {
	return doJSON[[]capabilities.CapabilityCandidate](c, ctx, http.MethodPost, "/v1/capabilities/search", correlationID, input)
}

func (c Client) ListRuntimeBindings(ctx context.Context, correlationID string, filter capabilities.RuntimeBindingFilter) (response.Envelope[[]capabilities.RuntimeBindingInspection], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.CapabilityRef != "" {
		values.Set("capability", filter.CapabilityRef)
	}
	if filter.EndpointVersionRef != "" {
		values.Set("endpoint_version", filter.EndpointVersionRef)
	}
	if filter.ProviderRef != "" {
		values.Set("provider", filter.ProviderRef)
	}
	if filter.RuntimeKind != "" {
		values.Set("runtime_kind", filter.RuntimeKind)
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	path := "/v1/capability-runtime-bindings"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]capabilities.RuntimeBindingInspection](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) RegisterRuntimeBinding(ctx context.Context, correlationID string, input capabilities.RegisterRuntimeBindingInput) (response.Envelope[capabilities.RuntimeBindingInspection], error) {
	return doJSON[capabilities.RuntimeBindingInspection](c, ctx, http.MethodPost, "/v1/capability-runtime-bindings", correlationID, input)
}

func (c Client) GetRuntimeBinding(ctx context.Context, correlationID, ref string) (response.Envelope[capabilities.RuntimeBindingInspection], error) {
	return doJSON[capabilities.RuntimeBindingInspection](c, ctx, http.MethodGet, "/v1/capability-runtime-bindings/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) ValidateRuntimeBinding(ctx context.Context, correlationID, ref string) (response.Envelope[routing.RuntimeBindingValidation], error) {
	return doJSON[routing.RuntimeBindingValidation](c, ctx, http.MethodGet, "/v1/capability-runtime-bindings/"+url.PathEscape(ref)+"/validate", correlationID, nil)
}

func (c Client) TestRuntimeBinding(ctx context.Context, correlationID, ref string, input routing.RuntimeBindingTestInput) (response.Envelope[routing.RuntimeBindingTestResult], error) {
	return doJSON[routing.RuntimeBindingTestResult](c, ctx, http.MethodPost, "/v1/capability-runtime-bindings/"+url.PathEscape(ref)+"/test", correlationID, input)
}

func (c Client) RegisterModulePackage(ctx context.Context, correlationID string, input modules.RegisterPackageInput) (response.Envelope[modules.ModuleRegistration], error) {
	return doJSON[modules.ModuleRegistration](c, ctx, http.MethodPost, "/v1/modules/register", correlationID, input)
}

func (c Client) InstallModule(ctx context.Context, correlationID string, input modules.InstallModuleInput) (response.Envelope[modules.ModuleInstallationDetail], error) {
	ref := strings.TrimSpace(input.ModuleVersionRef)
	if ref == "" {
		ref = "module"
	}
	return doJSON[modules.ModuleInstallationDetail](c, ctx, http.MethodPost, "/v1/modules/"+url.PathEscape(ref)+"/install", correlationID, input)
}

func (c Client) ListModules(ctx context.Context, correlationID string, filter modules.ModuleFilter) (response.Envelope[[]modules.ModuleListItem], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.ModuleID != "" {
		values.Set("module_id", filter.ModuleID)
	}
	if filter.ProjectRef != "" {
		values.Set("project", filter.ProjectRef)
	}
	path := "/v1/modules"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]modules.ModuleListItem](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) InspectModule(ctx context.Context, correlationID, ref string) (response.Envelope[modules.ModuleInspection], error) {
	return doJSON[modules.ModuleInspection](c, ctx, http.MethodGet, "/v1/modules/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) InspectModuleVersion(ctx context.Context, correlationID, ref string) (response.Envelope[modules.ModuleVersionInspection], error) {
	return doJSON[modules.ModuleVersionInspection](c, ctx, http.MethodGet, "/v1/module-versions/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) InspectModuleInstallation(ctx context.Context, correlationID, ref string) (response.Envelope[modules.ModuleInstallationDetail], error) {
	return doJSON[modules.ModuleInstallationDetail](c, ctx, http.MethodGet, "/v1/module-installations/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) EnableModuleInstallation(ctx context.Context, correlationID, ref string) (response.Envelope[modules.ModuleInstallationDetail], error) {
	return doJSON[modules.ModuleInstallationDetail](c, ctx, http.MethodPost, "/v1/module-installations/"+url.PathEscape(ref)+"/enable", correlationID, nil)
}

func (c Client) DisableModuleInstallation(ctx context.Context, correlationID, ref string) (response.Envelope[modules.ModuleInstallationDetail], error) {
	return doJSON[modules.ModuleInstallationDetail](c, ctx, http.MethodPost, "/v1/module-installations/"+url.PathEscape(ref)+"/disable", correlationID, nil)
}

func (c Client) InspectModuleInstallationHealth(ctx context.Context, correlationID, ref string) (response.Envelope[modules.ModuleHealthDetail], error) {
	return doJSON[modules.ModuleHealthDetail](c, ctx, http.MethodGet, "/v1/module-installations/"+url.PathEscape(ref)+"/health", correlationID, nil)
}

func (c Client) ListModuleInstallationProviders(ctx context.Context, correlationID, ref string) (response.Envelope[[]modules.InstalledProvider], error) {
	return doJSON[[]modules.InstalledProvider](c, ctx, http.MethodGet, "/v1/module-installations/"+url.PathEscape(ref)+"/providers", correlationID, nil)
}

func (c Client) ListModuleInstallationCapabilities(ctx context.Context, correlationID, ref string) (response.Envelope[[]modules.InstalledCapability], error) {
	return doJSON[[]modules.InstalledCapability](c, ctx, http.MethodGet, "/v1/module-installations/"+url.PathEscape(ref)+"/capabilities", correlationID, nil)
}

func (c Client) ExposeModuleCapability(ctx context.Context, correlationID string, input modules.ExposeModuleCapabilityInput) (response.Envelope[modules.ModuleCapabilityExposureResult], error) {
	return doJSON[modules.ModuleCapabilityExposureResult](
		c,
		ctx,
		http.MethodPost,
		"/v1/module-installations/"+url.PathEscape(input.InstallationRef)+"/capabilities/"+url.PathEscape(input.CapabilityRef)+"/expose",
		correlationID,
		input,
	)
}

func (c Client) DisableModuleCapability(ctx context.Context, correlationID string, input modules.DisableModuleCapabilityInput) (response.Envelope[modules.ModuleCapabilityExposureResult], error) {
	return doJSON[modules.ModuleCapabilityExposureResult](
		c,
		ctx,
		http.MethodPost,
		"/v1/module-installations/"+url.PathEscape(input.InstallationRef)+"/capabilities/"+url.PathEscape(input.CapabilityRef)+"/disable",
		correlationID,
		input,
	)
}

func (c Client) ExportModuleBackup(ctx context.Context, correlationID string, input modules.ExportModuleBackupInput) (response.Envelope[modules.ModuleBackupExportDetail], error) {
	return doJSON[modules.ModuleBackupExportDetail](
		c,
		ctx,
		http.MethodPost,
		"/v1/module-installations/"+url.PathEscape(input.InstallationRef)+"/backup-exports",
		correlationID,
		input,
	)
}

func (c Client) ListModuleBackupExports(ctx context.Context, correlationID string, filter modules.ModuleBackupExportFilter) (response.Envelope[[]modules.ModuleBackupExport], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Kind != "" {
		values.Set("kind", filter.Kind)
	}
	path := "/v1/module-installations/" + url.PathEscape(filter.InstallationRef) + "/backup-exports"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]modules.ModuleBackupExport](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) InspectModuleBackupExport(ctx context.Context, correlationID, ref string) (response.Envelope[modules.ModuleBackupExportDetail], error) {
	return doJSON[modules.ModuleBackupExportDetail](c, ctx, http.MethodGet, "/v1/module-backup-exports/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) ListWorkers(ctx context.Context, correlationID string, filter workers.WorkerFilter) (response.Envelope[[]workers.WorkerListItem], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Kind != "" {
		values.Set("kind", filter.Kind)
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.Health != "" {
		values.Set("health", filter.Health)
	}
	if filter.NodeRef != "" {
		values.Set("node", filter.NodeRef)
	}
	path := "/v1/workers"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]workers.WorkerListItem](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) InspectWorker(ctx context.Context, correlationID, ref string) (response.Envelope[workers.WorkerDetail], error) {
	return doJSON[workers.WorkerDetail](c, ctx, http.MethodGet, "/v1/workers/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) InspectWorkerPolicy(ctx context.Context, correlationID, ref string) (response.Envelope[workers.WorkerPolicyState], error) {
	return doJSON[workers.WorkerPolicyState](c, ctx, http.MethodGet, "/v1/workers/"+url.PathEscape(ref)+"/policy", correlationID, nil)
}

func (c Client) SetWorkerPolicy(ctx context.Context, correlationID, ref string, input workers.SetWorkerPolicyInput) (response.Envelope[workers.SetWorkerPolicyResult], error) {
	return doJSON[workers.SetWorkerPolicyResult](c, ctx, http.MethodPost, "/v1/workers/"+url.PathEscape(ref)+"/policy", correlationID, input)
}

func (c Client) ListWorkerRuns(ctx context.Context, correlationID, ref string, filter workers.RunFilter) (response.Envelope[[]workers.WorkerRun], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.Trigger != "" {
		values.Set("trigger", filter.Trigger)
	}
	path := "/v1/workers/" + url.PathEscape(ref) + "/runs"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]workers.WorkerRun](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) RunWorkerOnce(ctx context.Context, correlationID, ref string, input workers.RunOnceInput) (response.Envelope[workers.RunOnceResult], error) {
	return doJSON[workers.RunOnceResult](c, ctx, http.MethodPost, "/v1/workers/"+url.PathEscape(ref)+"/run-once", correlationID, input)
}

func (c Client) RepairStaleWorkerRuns(ctx context.Context, correlationID string) (response.Envelope[workers.StaleRunRepairResult], error) {
	return doJSON[workers.StaleRunRepairResult](c, ctx, http.MethodPost, "/v1/workers/repair-stale", correlationID, nil)
}

func (c Client) MaintenanceStatus(ctx context.Context, correlationID string) (response.Envelope[maintenance.Status], error) {
	return doJSON[maintenance.Status](c, ctx, http.MethodGet, "/v1/maintenance/status", correlationID, nil)
}

func (c Client) ListMaintenanceFindings(ctx context.Context, correlationID string, filter maintenance.FindingFilter) (response.Envelope[[]maintenance.Finding], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.Severity != "" {
		values.Set("severity", filter.Severity)
	}
	if filter.WorkerRef != "" {
		values.Set("worker", filter.WorkerRef)
	}
	path := "/v1/maintenance/findings"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]maintenance.Finding](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) MaintenanceDBStatus(ctx context.Context, correlationID string) (response.Envelope[maintenance.DBStatus], error) {
	return doJSON[maintenance.DBStatus](c, ctx, http.MethodGet, "/v1/maintenance/db/status", correlationID, nil)
}

func (c Client) CompactMaintenanceDatabase(ctx context.Context, correlationID string, input maintenance.DatabaseCompactInput) (response.Envelope[maintenance.DatabaseCompactResult], error) {
	return doJSON[maintenance.DatabaseCompactResult](c, ctx, http.MethodPost, "/v1/maintenance/db/compact", correlationID, input)
}

func (c Client) MaintenanceBackupStatus(ctx context.Context, correlationID string) (response.Envelope[maintenance.BackupStatus], error) {
	return doJSON[maintenance.BackupStatus](c, ctx, http.MethodGet, "/v1/maintenance/backup/status", correlationID, nil)
}

func (c Client) ListMaintenanceBackups(ctx context.Context, correlationID string, filter maintenance.OperationFilter) (response.Envelope[[]maintenance.BackupOperation], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Kind != "" {
		values.Set("kind", filter.Kind)
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.SubjectKind != "" {
		values.Set("subject_kind", filter.SubjectKind)
	}
	if filter.SubjectID != "" {
		values.Set("subject_id", filter.SubjectID)
	}
	path := "/v1/maintenance/backup/list"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]maintenance.BackupOperation](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) RunMaintenanceBackup(ctx context.Context, correlationID string, input maintenance.BackupRunInput) (response.Envelope[workers.RunOnceResult], error) {
	return doJSON[workers.RunOnceResult](c, ctx, http.MethodPost, "/v1/maintenance/backup/run", correlationID, input)
}

func (c Client) VerifyMaintenanceBackup(ctx context.Context, correlationID string, input maintenance.BackupVerifyInput) (response.Envelope[maintenance.BackupVerification], error) {
	return doJSON[maintenance.BackupVerification](c, ctx, http.MethodPost, "/v1/maintenance/backup/verify", correlationID, input)
}

func (c Client) MaintenanceObjectStoreStatus(ctx context.Context, correlationID string) (response.Envelope[maintenance.ObjectStoreStatus], error) {
	return doJSON[maintenance.ObjectStoreStatus](c, ctx, http.MethodGet, "/v1/maintenance/object-store/status", correlationID, nil)
}

func (c Client) RunMaintenanceObjectStoreScan(ctx context.Context, correlationID string, input maintenance.ObjectStoreScanInput) (response.Envelope[workers.RunOnceResult], error) {
	return doJSON[workers.RunOnceResult](c, ctx, http.MethodPost, "/v1/maintenance/object-store/scan", correlationID, input)
}

func (c Client) ExplainPolicy(ctx context.Context, correlationID string, input policy.DecisionInput) (response.Envelope[policy.PolicyExplanation], error) {
	return doJSON[policy.PolicyExplanation](c, ctx, http.MethodPost, "/v1/policy/explain", correlationID, input)
}

func (c Client) ListPolicyDecisions(ctx context.Context, correlationID string, filter policy.DecisionFilter) (response.Envelope[[]policy.Decision], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.ActorRef != "" {
		values.Set("actor", filter.ActorRef)
	}
	if filter.OriginNodeRef != "" {
		values.Set("origin_node", filter.OriginNodeRef)
	}
	if filter.TargetNodeRef != "" {
		values.Set("target_node", filter.TargetNodeRef)
	}
	if filter.Operation != "" {
		values.Set("operation", filter.Operation)
	}
	if filter.Decision != "" {
		values.Set("decision", filter.Decision)
	}
	if filter.CapabilityEndpointRef != "" {
		values.Set("capability", filter.CapabilityEndpointRef)
	}
	if filter.ApprovalRef != "" {
		values.Set("approval", filter.ApprovalRef)
	}
	if filter.GrantRef != "" {
		values.Set("grant", filter.GrantRef)
	}
	path := "/v1/policy/decisions"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]policy.Decision](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetPolicyDecision(ctx context.Context, correlationID, ref string) (response.Envelope[policy.Decision], error) {
	return doJSON[policy.Decision](c, ctx, http.MethodGet, "/v1/policy/decisions/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) ListApprovals(ctx context.Context, correlationID string, filter policy.ApprovalFilter) (response.Envelope[[]policy.Approval], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.ActorRef != "" {
		values.Set("actor", filter.ActorRef)
	}
	if filter.ApprovingActorRef != "" {
		values.Set("approving_actor", filter.ApprovingActorRef)
	}
	if filter.TargetNodeRef != "" {
		values.Set("target_node", filter.TargetNodeRef)
	}
	if filter.CapabilityEndpointRef != "" {
		values.Set("capability", filter.CapabilityEndpointRef)
	}
	path := "/v1/approvals"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]policy.Approval](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetApproval(ctx context.Context, correlationID, ref string) (response.Envelope[policy.Approval], error) {
	return doJSON[policy.Approval](c, ctx, http.MethodGet, "/v1/approvals/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) DecideApproval(ctx context.Context, correlationID, ref string, input policy.ApprovalDecisionInput) (response.Envelope[policy.ApprovalDecisionResult], error) {
	return doJSON[policy.ApprovalDecisionResult](c, ctx, http.MethodPost, "/v1/approvals/"+url.PathEscape(ref)+"/decide", correlationID, input)
}

func (c Client) ListGrants(ctx context.Context, correlationID string, filter policy.GrantFilter) (response.Envelope[[]policy.Grant], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.GrantType != "" {
		values.Set("type", filter.GrantType)
	}
	if filter.GrantedToActorRef != "" {
		values.Set("actor", filter.GrantedToActorRef)
	}
	if filter.GrantedByActorRef != "" {
		values.Set("granted_by", filter.GrantedByActorRef)
	}
	if filter.ApprovalRef != "" {
		values.Set("approval", filter.ApprovalRef)
	}
	if filter.TargetNodeRef != "" {
		values.Set("target_node", filter.TargetNodeRef)
	}
	if filter.CapabilityEndpointRef != "" {
		values.Set("capability", filter.CapabilityEndpointRef)
	}
	path := "/v1/grants"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]policy.Grant](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetGrant(ctx context.Context, correlationID, ref string) (response.Envelope[policy.Grant], error) {
	return doJSON[policy.Grant](c, ctx, http.MethodGet, "/v1/grants/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) RevokeGrant(ctx context.Context, correlationID, ref string, input policy.GrantRevokeInput) (response.Envelope[policy.Grant], error) {
	return doJSON[policy.Grant](c, ctx, http.MethodPost, "/v1/grants/"+url.PathEscape(ref)+"/revoke", correlationID, input)
}

func (c Client) CallCapability(ctx context.Context, correlationID string, input routing.CapabilityCallInput) (response.Envelope[routing.CapabilityCallOutcome], error) {
	return doJSON[routing.CapabilityCallOutcome](c, ctx, http.MethodPost, "/v1/capability-calls", correlationID, input)
}

func (c Client) ListRoutes(ctx context.Context, correlationID string, filter routing.RouteFilter) (response.Envelope[[]routing.Route], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.ActorRef != "" {
		values.Set("actor", filter.ActorRef)
	}
	if filter.OriginNodeRef != "" {
		values.Set("origin_node", filter.OriginNodeRef)
	}
	if filter.OriginScopeRef != "" {
		values.Set("scope", filter.OriginScopeRef)
	}
	if filter.RuntimeNodeRef != "" {
		values.Set("runtime_node", filter.RuntimeNodeRef)
	}
	if filter.TargetNodeRef != "" {
		values.Set("target_node", filter.TargetNodeRef)
	}
	if filter.ProviderRef != "" {
		values.Set("provider", filter.ProviderRef)
	}
	if filter.CapabilityEndpointRef != "" {
		values.Set("capability", filter.CapabilityEndpointRef)
	}
	if filter.PolicyDecisionRef != "" {
		values.Set("policy_decision", filter.PolicyDecisionRef)
	}
	if filter.ApprovalRef != "" {
		values.Set("approval", filter.ApprovalRef)
	}
	if filter.GrantRef != "" {
		values.Set("grant", filter.GrantRef)
	}
	if filter.JobRef != "" {
		values.Set("job", filter.JobRef)
	}
	if filter.CorrelationID != "" {
		values.Set("correlation", filter.CorrelationID)
	}
	path := "/v1/routes"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]routing.Route](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetRoute(ctx context.Context, correlationID, ref string) (response.Envelope[routing.Route], error) {
	return doJSON[routing.Route](c, ctx, http.MethodGet, "/v1/routes/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) ListCapabilityCalls(ctx context.Context, correlationID string, filter routing.CapabilityCallFilter) (response.Envelope[[]routing.CapabilityCall], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.ActorRef != "" {
		values.Set("actor", filter.ActorRef)
	}
	if filter.OriginNodeRef != "" {
		values.Set("origin_node", filter.OriginNodeRef)
	}
	if filter.ScopeRef != "" {
		values.Set("scope", filter.ScopeRef)
	}
	if filter.TargetNodeRef != "" {
		values.Set("target_node", filter.TargetNodeRef)
	}
	if filter.ProviderRef != "" {
		values.Set("provider", filter.ProviderRef)
	}
	if filter.CapabilityEndpointRef != "" {
		values.Set("capability", filter.CapabilityEndpointRef)
	}
	if filter.PolicyDecisionRef != "" {
		values.Set("policy_decision", filter.PolicyDecisionRef)
	}
	if filter.ApprovalRef != "" {
		values.Set("approval", filter.ApprovalRef)
	}
	if filter.GrantRef != "" {
		values.Set("grant", filter.GrantRef)
	}
	if filter.JobRef != "" {
		values.Set("job", filter.JobRef)
	}
	if filter.CorrelationID != "" {
		values.Set("correlation", filter.CorrelationID)
	}
	if filter.IdempotencyKey != "" {
		values.Set("idempotency_key", filter.IdempotencyKey)
	}
	path := "/v1/capability-calls"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]routing.CapabilityCall](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetCapabilityCall(ctx context.Context, correlationID, ref string) (response.Envelope[routing.CapabilityCall], error) {
	return doJSON[routing.CapabilityCall](c, ctx, http.MethodGet, "/v1/capability-calls/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) CommunicationHealth(ctx context.Context, correlationID string) (response.Envelope[communication.Health], error) {
	return doJSON[communication.Health](c, ctx, http.MethodGet, "/v1/communication/health", correlationID, nil)
}

func (c Client) EnqueueMessage(ctx context.Context, correlationID string, input communication.EnqueueInput) (response.Envelope[communication.Message], error) {
	return doJSON[communication.Message](c, ctx, http.MethodPost, "/v1/communication/messages", correlationID, input)
}

func (c Client) ListMessages(ctx context.Context, correlationID string, filter communication.MessageFilter) (response.Envelope[[]communication.Message], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.NodeRef != "" {
		values.Set("node", filter.NodeRef)
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.Kind != "" {
		values.Set("kind", filter.Kind)
	}
	if filter.Direction != "" {
		values.Set("direction", filter.Direction)
	}
	if filter.CorrelationID != "" {
		values.Set("correlation", filter.CorrelationID)
	}
	if filter.IdempotencyKey != "" {
		values.Set("idempotency_key", filter.IdempotencyKey)
	}
	path := "/v1/communication/messages"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]communication.Message](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetMessage(ctx context.Context, correlationID, ref string) (response.Envelope[communication.Message], error) {
	return doJSON[communication.Message](c, ctx, http.MethodGet, "/v1/communication/messages/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) CreateRealtimeTopic(ctx context.Context, correlationID string, input realtime.CreateTopicInput) (response.Envelope[realtime.Topic], error) {
	return doJSON[realtime.Topic](c, ctx, http.MethodPost, "/v1/realtime/topics", correlationID, input)
}

func (c Client) ListRealtimeTopics(ctx context.Context, correlationID string, filter realtime.TopicFilter) (response.Envelope[[]realtime.Topic], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.ScopeRef != "" {
		values.Set("scope", filter.ScopeRef)
	}
	path := "/v1/realtime/topics"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]realtime.Topic](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetRealtimeTopic(ctx context.Context, correlationID, ref string) (response.Envelope[realtime.Topic], error) {
	return doJSON[realtime.Topic](c, ctx, http.MethodGet, "/v1/realtime/topics/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) PublishRealtimeTopic(ctx context.Context, correlationID, ref string, input realtime.PublishInput) (response.Envelope[realtime.TopicPublication], error) {
	return doJSON[realtime.TopicPublication](c, ctx, http.MethodPost, "/v1/realtime/topics/"+url.PathEscape(ref)+"/publish", correlationID, input)
}

func (c Client) CreateRealtimeSubscription(ctx context.Context, correlationID string, input realtime.CreateSubscriptionInput) (response.Envelope[realtime.Subscription], error) {
	return doJSON[realtime.Subscription](c, ctx, http.MethodPost, "/v1/realtime/subscriptions", correlationID, input)
}

func (c Client) ListRealtimeSubscriptions(ctx context.Context, correlationID string, filter realtime.SubscriptionFilter) (response.Envelope[[]realtime.Subscription], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.TopicRef != "" {
		values.Set("topic", filter.TopicRef)
	}
	path := "/v1/realtime/subscriptions"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]realtime.Subscription](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetRealtimeSubscription(ctx context.Context, correlationID, ref string) (response.Envelope[realtime.Subscription], error) {
	return doJSON[realtime.Subscription](c, ctx, http.MethodGet, "/v1/realtime/subscriptions/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) PollRealtimeSubscription(ctx context.Context, correlationID, ref string, input realtime.PollSubscriptionInput) (response.Envelope[realtime.SubscriptionPollResult], error) {
	return doJSON[realtime.SubscriptionPollResult](c, ctx, http.MethodPost, "/v1/realtime/subscriptions/"+url.PathEscape(ref)+"/poll", correlationID, input)
}

func (c Client) AcknowledgeRealtimeSubscription(ctx context.Context, correlationID, ref string, input realtime.AcknowledgeSubscriptionInput) (response.Envelope[realtime.Subscription], error) {
	return doJSON[realtime.Subscription](c, ctx, http.MethodPost, "/v1/realtime/subscriptions/"+url.PathEscape(ref)+"/ack", correlationID, input)
}

func (c Client) CancelRealtimeSubscription(ctx context.Context, correlationID, ref string) (response.Envelope[realtime.Subscription], error) {
	return doJSON[realtime.Subscription](c, ctx, http.MethodPost, "/v1/realtime/subscriptions/"+url.PathEscape(ref)+"/cancel", correlationID, map[string]any{})
}

func (c Client) ListRealtimePresence(ctx context.Context, correlationID string, filter realtime.PresenceFilter) (response.Envelope[[]realtime.Presence], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.SubjectKind != "" {
		values.Set("subject_kind", filter.SubjectKind)
	}
	if filter.State != "" {
		values.Set("state", filter.State)
	}
	path := "/v1/realtime/presence"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]realtime.Presence](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetRealtimePresence(ctx context.Context, correlationID, ref string) (response.Envelope[realtime.Presence], error) {
	return doJSON[realtime.Presence](c, ctx, http.MethodGet, "/v1/realtime/presence/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) ListRealtimeNotifications(ctx context.Context, correlationID string, filter realtime.NotificationFilter) (response.Envelope[[]realtime.Notification], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.Category != "" {
		values.Set("category", filter.Category)
	}
	if filter.TargetKind != "" {
		values.Set("target_kind", filter.TargetKind)
	}
	if filter.TargetRef != "" {
		values.Set("target_ref", filter.TargetRef)
	}
	if filter.ApprovalRef != "" {
		values.Set("approval", filter.ApprovalRef)
	}
	path := "/v1/realtime/notifications"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]realtime.Notification](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) GetRealtimeNotification(ctx context.Context, correlationID, ref string) (response.Envelope[realtime.Notification], error) {
	return doJSON[realtime.Notification](c, ctx, http.MethodGet, "/v1/realtime/notifications/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) AcknowledgeRealtimeNotification(ctx context.Context, correlationID, ref string) (response.Envelope[realtime.Notification], error) {
	return doJSON[realtime.Notification](c, ctx, http.MethodPost, "/v1/realtime/notifications/"+url.PathEscape(ref)+"/ack", correlationID, map[string]any{})
}

func (c Client) DismissRealtimeNotification(ctx context.Context, correlationID, ref string) (response.Envelope[realtime.Notification], error) {
	return doJSON[realtime.Notification](c, ctx, http.MethodPost, "/v1/realtime/notifications/"+url.PathEscape(ref)+"/dismiss", correlationID, map[string]any{})
}

func (c Client) GetRealtimeProgress(ctx context.Context, correlationID, source string, limit int) (response.Envelope[realtime.ProgressDetail], error) {
	values := url.Values{}
	if limit > 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
	path := "/v1/realtime/progress/" + url.PathEscape(source)
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[realtime.ProgressDetail](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) ListRealtimeLeases(ctx context.Context, correlationID string, filter realtime.LeaseFilter) (response.Envelope[[]realtime.Lease], error) {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.Status != "" {
		values.Set("status", filter.Status)
	}
	if filter.Resource != "" {
		values.Set("resource", filter.Resource)
	}
	if filter.HolderKind != "" {
		values.Set("holder_kind", filter.HolderKind)
	}
	if filter.HolderRef != "" {
		values.Set("holder_ref", filter.HolderRef)
	}
	path := "/v1/realtime/leases"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	return doJSON[[]realtime.Lease](c, ctx, http.MethodGet, path, correlationID, nil)
}

func (c Client) RequestRealtimeLease(ctx context.Context, correlationID string, input realtime.RequestLeaseInput) (response.Envelope[realtime.Lease], error) {
	return doJSON[realtime.Lease](c, ctx, http.MethodPost, "/v1/realtime/leases", correlationID, input)
}

func (c Client) GetRealtimeLease(ctx context.Context, correlationID, ref string) (response.Envelope[realtime.Lease], error) {
	return doJSON[realtime.Lease](c, ctx, http.MethodGet, "/v1/realtime/leases/"+url.PathEscape(ref), correlationID, nil)
}

func (c Client) ReleaseRealtimeLease(ctx context.Context, correlationID, ref string, input realtime.ReleaseLeaseInput) (response.Envelope[realtime.Lease], error) {
	return doJSON[realtime.Lease](c, ctx, http.MethodPost, "/v1/realtime/leases/"+url.PathEscape(ref)+"/release", correlationID, input)
}

func storageListValues(filter storagecatalog.ListFilter) url.Values {
	values := url.Values{}
	if filter.Limit > 0 {
		values.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.StorageClass != "" {
		values.Set("storage_class", filter.StorageClass)
	}
	if filter.SourceArea != "" {
		values.Set("source_area", filter.SourceArea)
	}
	if filter.OriginNodeKey != "" {
		values.Set("node", filter.OriginNodeKey)
	}
	if filter.FileClass != "" {
		values.Set("file_class", filter.FileClass)
	}
	if filter.ProcessingState != "" {
		values.Set("processing_state", filter.ProcessingState)
	}
	if filter.AvailabilityState != "" {
		values.Set("availability_state", filter.AvailabilityState)
	}
	if filter.IncludeDeleted {
		values.Set("include_deleted", "true")
	}
	return values
}

func doJSON[T any](c Client, ctx context.Context, method, path, correlationID string, body any) (response.Envelope[T], error) {
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		payload, err := json.Marshal(body)
		if err != nil {
			return response.Envelope[T]{}, err
		}
		reader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.requestURL(path), reader)
	if err != nil {
		return response.Envelope[T]{}, err
	}
	req.Header.Set(correlation.Header, correlation.Normalize(correlationID))
	if c.idempotencyKey != "" {
		req.Header.Set(idempotency.Header, c.idempotencyKey)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return response.Envelope[T]{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(resp.Body)
		var errorEnvelope response.ErrorEnvelope
		if err := json.Unmarshal(payload, &errorEnvelope); err == nil && !errorEnvelope.OK && errorEnvelope.Error.Code != "" {
			return response.Envelope[T]{}, &RequestError{
				Method:     method,
				Path:       path,
				StatusCode: resp.StatusCode,
				Envelope:   errorEnvelope,
			}
		}
		if len(payload) > 0 {
			return response.Envelope[T]{}, fmt.Errorf("loomd request %s %s failed with status %d: %s", method, path, resp.StatusCode, string(payload))
		}
		return response.Envelope[T]{}, fmt.Errorf("loomd request %s %s failed with status %d", method, path, resp.StatusCode)
	}

	var envelope response.Envelope[T]
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return response.Envelope[T]{}, err
	}
	return envelope, nil
}

func (c Client) requestURL(path string) string {
	baseURL := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if baseURL == "" {
		baseURL = "http://loom"
	}
	return baseURL + path
}
