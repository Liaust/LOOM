package runtime

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	WorkerKeyPrefix = "node-agent."

	KindSupervisorSelfcheck = "node_supervisor_selfcheck"
	KindHeartbeat           = "node_heartbeat"
	KindPoll                = "node_poll"
	KindOutboxFlusher       = "node_outbox_flusher"
	KindLocalQueueReporter  = "local_queue_reporter"
	KindWatchedRoot         = "watched_root"
	// KindDropzoneTransfer is retained only to decode historical worker
	// instances. No runtime registry accepts this kind.
	KindDropzoneTransfer = "dropzone_transfer"
	KindStorageMount     = "storage_mount"
	KindLaneHousekeeping = "lane_housekeeping"

	WorkerKeySupervisorSelfcheck = WorkerKeyPrefix + KindSupervisorSelfcheck
	WorkerKeyHeartbeat           = WorkerKeyPrefix + KindHeartbeat
	WorkerKeyPoll                = WorkerKeyPrefix + KindPoll
	WorkerKeyOutboxFlusher       = WorkerKeyPrefix + KindOutboxFlusher
	WorkerKeyLocalQueueReporter  = WorkerKeyPrefix + KindLocalQueueReporter
	// WorkerKeyDropzoneTransfer identifies historical worker evidence only.
	WorkerKeyDropzoneTransfer = WorkerKeyPrefix + KindDropzoneTransfer
	WorkerKeyStorageMount     = WorkerKeyPrefix + KindStorageMount
	WorkerKeyLaneHousekeeping = WorkerKeyPrefix + KindLaneHousekeeping

	WorkerStatusHealthy              = "healthy"
	WorkerStatusOfflineQueueing      = "offline_queueing"
	WorkerStatusDegraded             = "degraded"
	WorkerStatusBlocked              = "blocked"
	WorkerStatusRequiresManualAction = "requires_manual_action"
	WorkerStatusPaused               = "paused"
	WorkerStatusShuttingDown         = "shutting_down"

	RunStatusStarted   = "started"
	RunStatusSucceeded = "succeeded"
	RunStatusFailed    = "failed"
	RunStatusSkipped   = "skipped"

	OutboxKindMessageAck                   = "message_ack"
	OutboxKindCapabilityResult             = "capability_result"
	OutboxKindProviderAdvertisement        = "provider_advertisement"
	OutboxKindWorkerSummary                = "worker_summary"
	OutboxStatusPending                    = "pending"
	OutboxStatusInflight                   = "inflight"
	OutboxStatusDone                       = "done"
	OutboxStatusFailed                     = "failed"
	OutboxStatusManualAction               = "manual_action"
	defaultHeartbeatIntervalSeconds        = 30
	defaultPollIntervalSeconds             = 30
	defaultFlusherIntervalSeconds          = 30
	defaultSelfcheckIntervalSeconds        = 300
	defaultReporterIntervalSeconds         = 60
	defaultLaneHousekeepingIntervalSeconds = 60
	defaultLeaseTimeoutSeconds             = 120
)

func WatchedRootWorkerKey(rootKey string) string {
	rootKey = strings.TrimSpace(rootKey)
	if rootKey == "" {
		return WorkerKeyPrefix + KindWatchedRoot
	}
	return WorkerKeyPrefix + KindWatchedRoot + "." + rootKey
}

type DefaultInstanceInput struct {
	HeartbeatIntervalSeconds int
	PollIntervalSeconds      int
}

type WorkerInstance struct {
	WorkerKey           string          `json:"worker_key"`
	Kind                string          `json:"kind"`
	DisplayName         string          `json:"display_name"`
	Enabled             bool            `json:"enabled"`
	IntervalSeconds     int             `json:"interval_seconds"`
	LeaseTimeoutSeconds int             `json:"lease_timeout_seconds"`
	LocalRootKey        string          `json:"local_root_key,omitempty"`
	ConfigHash          string          `json:"config_hash,omitempty"`
	ConfigJSON          json.RawMessage `json:"config_json,omitempty"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

type WorkerCheckpoint struct {
	WorkerKey           string          `json:"worker_key"`
	LastRunID           string          `json:"last_run_id,omitempty"`
	LastStartedAt       *time.Time      `json:"last_started_at,omitempty"`
	LastFinishedAt      *time.Time      `json:"last_finished_at,omitempty"`
	LastSuccessAt       *time.Time      `json:"last_success_at,omitempty"`
	LastFailureAt       *time.Time      `json:"last_failure_at,omitempty"`
	ConsecutiveFailures int             `json:"consecutive_failures"`
	LastErrorCode       string          `json:"last_error_code,omitempty"`
	LastErrorMessage    string          `json:"last_error_message,omitempty"`
	Metadata            json.RawMessage `json:"metadata,omitempty"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

type WorkerRun struct {
	LocalRunID    string          `json:"local_run_id"`
	WorkerKey     string          `json:"worker_key"`
	Kind          string          `json:"kind"`
	Status        string          `json:"status"`
	StartedAt     time.Time       `json:"started_at"`
	FinishedAt    *time.Time      `json:"finished_at,omitempty"`
	DurationMS    int64           `json:"duration_ms"`
	ResultJSON    json.RawMessage `json:"result_json,omitempty"`
	ErrorCode     string          `json:"error_code,omitempty"`
	ErrorMessage  string          `json:"error_message,omitempty"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	HealthStatus  string          `json:"health_status,omitempty"`
	HealthMessage string          `json:"health_message,omitempty"`
}

type WorkerHealth struct {
	WorkerKey           string          `json:"worker_key"`
	Kind                string          `json:"kind"`
	Status              string          `json:"status"`
	LastRunID           string          `json:"last_run_id,omitempty"`
	LastSuccessAt       *time.Time      `json:"last_success_at,omitempty"`
	LastFailureAt       *time.Time      `json:"last_failure_at,omitempty"`
	ConsecutiveFailures int             `json:"consecutive_failures"`
	Message             string          `json:"message,omitempty"`
	QueueSummaryJSON    json.RawMessage `json:"queue_summary_json,omitempty"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

type OutboxItem struct {
	LocalOutboxID    string          `json:"local_outbox_id"`
	Kind             string          `json:"kind"`
	Status           string          `json:"status"`
	IdempotencyKey   string          `json:"idempotency_key,omitempty"`
	CorrelationID    string          `json:"correlation_id,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	NextAttemptAt    *time.Time      `json:"next_attempt_at,omitempty"`
	LastAttemptAt    *time.Time      `json:"last_attempt_at,omitempty"`
	AttemptCount     int             `json:"attempt_count"`
	PayloadRef       string          `json:"payload_ref,omitempty"`
	PayloadJSON      json.RawMessage `json:"payload_json,omitempty"`
	ResultJSON       json.RawMessage `json:"result_json,omitempty"`
	LastErrorCode    string          `json:"last_error_code,omitempty"`
	LastErrorMessage string          `json:"last_error_message,omitempty"`
	SourceWorkerKey  string          `json:"source_worker_key,omitempty"`
	SourceMessageID  string          `json:"source_message_id,omitempty"`
}

type InboxItem struct {
	LocalInboxID  string          `json:"local_inbox_id"`
	MessageID     string          `json:"message_id"`
	Kind          string          `json:"kind"`
	Status        string          `json:"status"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	PayloadJSON   json.RawMessage `json:"payload_json,omitempty"`
	LastErrorCode string          `json:"last_error_code,omitempty"`
	LastError     string          `json:"last_error,omitempty"`
}

type OutboxSummary struct {
	Counts            map[string]int `json:"counts"`
	OldestPendingAt   *time.Time     `json:"oldest_pending_at,omitempty"`
	NextDueAt         *time.Time     `json:"next_due_at,omitempty"`
	TotalPendingBytes int64          `json:"total_pending_bytes"`
}

type InboxSummary struct {
	Counts        map[string]int `json:"counts"`
	LegacyRecords int            `json:"legacy_records"`
}

type WorkerCounts struct {
	Total        int `json:"total"`
	Healthy      int `json:"healthy"`
	Degraded     int `json:"degraded"`
	Blocked      int `json:"blocked"`
	ManualAction int `json:"manual_action"`
	Paused       int `json:"paused"`
}

type QueueCounts struct {
	InboxPending        int   `json:"inbox_pending"`
	InboxDone           int   `json:"inbox_done"`
	InboxFailed         int   `json:"inbox_failed"`
	OutboxPending       int   `json:"outbox_pending"`
	OutboxInflight      int   `json:"outbox_inflight"`
	OutboxDone          int   `json:"outbox_done"`
	OutboxFailed        int   `json:"outbox_failed"`
	OutboxManualAction  int   `json:"outbox_manual_action"`
	LegacyInboxRecords  int   `json:"legacy_inbox_records"`
	LegacyOutboxRecords int   `json:"legacy_outbox_records"`
	SyncPending         int   `json:"sync_pending"`
	SyncFailed          int   `json:"sync_failed"`
	SyncConflicted      int   `json:"sync_conflicted"`
	BackupPending       int   `json:"backup_pending"`
	BackupRetryable     int   `json:"backup_retryable"`
	BackupFailed        int   `json:"backup_failed"`
	BackupManualAction  int   `json:"backup_manual_action"`
	BackupPendingBytes  int64 `json:"backup_pending_bytes"`
	PendingBytes        int64 `json:"pending_bytes"`
}

type OldestPending struct {
	Outbox *time.Time `json:"outbox,omitempty"`
}

type LocalSummary struct {
	GeneratedAt   time.Time     `json:"generated_at"`
	Workers       WorkerCounts  `json:"workers"`
	Queues        QueueCounts   `json:"queues"`
	OldestPending OldestPending `json:"oldest_pending"`
	HighestStatus string        `json:"highest_status"`
}

type WorkerStatus struct {
	Instance   WorkerInstance    `json:"instance"`
	Health     *WorkerHealth     `json:"health,omitempty"`
	Checkpoint *WorkerCheckpoint `json:"checkpoint,omitempty"`
}

type Status struct {
	DataDir string         `json:"data_dir"`
	Workers []WorkerStatus `json:"workers"`
	Summary LocalSummary   `json:"summary"`
	Outbox  OutboxSummary  `json:"outbox"`
	Inbox   InboxSummary   `json:"inbox"`
}

type RunOutput struct {
	Instance   WorkerInstance   `json:"instance"`
	Run        WorkerRun        `json:"run"`
	Health     WorkerHealth     `json:"health"`
	Checkpoint WorkerCheckpoint `json:"checkpoint"`
}
