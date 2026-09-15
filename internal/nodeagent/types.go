package nodeagent

import (
	"encoding/json"
	"time"

	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/filesystemconnector"
	"loom.local/loom/internal/filetransfer"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/routing"
	loomsync "loom.local/loom/internal/sync"
	"loom.local/loom/internal/version"
)

const (
	defaultHeartbeatIntervalSeconds = 30
	defaultPollIntervalSeconds      = 30
	defaultNodeKind                 = "workspace"
	defaultNodeRole                 = "workspace"
	defaultRuntimeClass             = "workspace"
)

type Config struct {
	MainURL                  string                     `json:"main_url"`
	NodeKey                  string                     `json:"node_key"`
	DisplayName              string                     `json:"display_name"`
	NodeKind                 string                     `json:"node_kind"`
	NodeRole                 string                     `json:"node_role"`
	RuntimeClass             string                     `json:"runtime_class"`
	BoxRootPath              string                     `json:"box_root_path,omitempty"`
	BoxStateRoot             string                     `json:"box_state_root,omitempty"`
	HeartbeatIntervalSeconds int                        `json:"heartbeat_interval_seconds"`
	PollIntervalSeconds      int                        `json:"poll_interval_seconds"`
	BackupTransport          BackupTransportConfig      `json:"backup_transport,omitempty"`
	Filesystem               filesystemconnector.Config `json:"filesystem,omitempty"`
	ServiceManager           ServiceManagerConfig       `json:"service_manager,omitempty"`
	CreatedAt                time.Time                  `json:"created_at"`
	UpdatedAt                time.Time                  `json:"updated_at"`
}

type ServiceManagerConfig struct {
	ApplicationSocketPath string `json:"application_socket_path,omitempty"`
	AllowlistPath         string `json:"allowlist_path,omitempty"`
	HelperPath            string `json:"helper_path,omitempty"`
}

type BackupTransportConfig struct {
	DirectMaxBytes  int64  `json:"direct_max_bytes,omitempty"`
	ForceChunked    bool   `json:"force_chunked,omitempty"`
	ChunkSizeBytes  int64  `json:"chunk_size_bytes,omitempty"`
	StabilityWindow string `json:"stability_window,omitempty"`
}

func normalizeBackupTransportConfig(config BackupTransportConfig) BackupTransportConfig {
	if config.DirectMaxBytes <= 0 {
		config.DirectMaxBytes = loomsync.MaxPrivateBackupBytes
	}
	if config.ChunkSizeBytes <= 0 {
		config.ChunkSizeBytes = filetransfer.DefaultChunkSizeBytes
	}
	if config.ChunkSizeBytes > filetransfer.MaxChunkSizeBytes {
		config.ChunkSizeBytes = filetransfer.MaxChunkSizeBytes
	}
	if config.StabilityWindow == "" {
		config.StabilityWindow = "1s"
	}
	return config
}

type State struct {
	EnrollmentRequestID string              `json:"enrollment_request_id,omitempty"`
	NodeID              string              `json:"node_id,omitempty"`
	NodeCredentialID    string              `json:"node_credential_id,omitempty"`
	CredentialToken     string              `json:"credential_token,omitempty"`
	LastHeartbeat       *HeartbeatState     `json:"last_heartbeat,omitempty"`
	LastPoll            *PollState          `json:"last_poll,omitempty"`
	LastAdvertisement   *AdvertisementState `json:"last_advertisement,omitempty"`
	UpdatedAt           time.Time           `json:"updated_at"`
}

type HeartbeatState struct {
	NodeHeartbeatID string    `json:"node_heartbeat_id"`
	PresenceState   string    `json:"presence_state"`
	ReportedStatus  string    `json:"reported_status"`
	ReceivedAt      time.Time `json:"received_at"`
}

type PollState struct {
	PollCompletedAt time.Time `json:"poll_completed_at"`
	MessagesClaimed int       `json:"messages_claimed"`
	MessagesAcked   int       `json:"messages_acked"`
	LastMessageID   string    `json:"last_message_id,omitempty"`
}

type AdvertisementState struct {
	ProviderAdvertisementID string    `json:"provider_advertisement_id"`
	Status                  string    `json:"status"`
	ProviderID              string    `json:"provider_id,omitempty"`
	ProviderAddress         string    `json:"provider_address,omitempty"`
	AdvertisedAt            time.Time `json:"advertised_at"`
}

type Status struct {
	ConfigPath           string              `json:"config_path"`
	StatePath            string              `json:"state_path"`
	DataDir              string              `json:"data_dir"`
	Version              version.Info        `json:"version"`
	Config               Config              `json:"config"`
	EnrollmentRequestID  string              `json:"enrollment_request_id,omitempty"`
	NodeID               string              `json:"node_id,omitempty"`
	NodeCredentialID     string              `json:"node_credential_id,omitempty"`
	CredentialConfigured bool                `json:"credential_configured"`
	LastHeartbeat        *HeartbeatState     `json:"last_heartbeat,omitempty"`
	LastPoll             *PollState          `json:"last_poll,omitempty"`
	LastAdvertisement    *AdvertisementState `json:"last_advertisement,omitempty"`
	Runtime              *noderuntime.Status `json:"runtime,omitempty"`
}

type PollRunResult struct {
	Poll      communication.PollResult `json:"poll"`
	Processed []ProcessedMessage       `json:"processed"`
}

type ProcessedMessage struct {
	CommunicationMessageID              string                       `json:"communication_message_id"`
	Kind                                string                       `json:"kind"`
	AckStatus                           string                       `json:"ack_status"`
	InboxPath                           string                       `json:"inbox_path"`
	OutboxPath                          string                       `json:"outbox_path"`
	CapabilityResultOutboxPath          string                       `json:"capability_result_outbox_path,omitempty"`
	RuntimeAckOutboxID                  string                       `json:"runtime_ack_outbox_id,omitempty"`
	RuntimeAckOutboxStatus              string                       `json:"runtime_ack_outbox_status,omitempty"`
	RuntimeCapabilityResultOutboxID     string                       `json:"runtime_capability_result_outbox_id,omitempty"`
	RuntimeCapabilityResultOutboxStatus string                       `json:"runtime_capability_result_outbox_status,omitempty"`
	RuntimeFlushError                   string                       `json:"runtime_flush_error,omitempty"`
	Ack                                 communication.AckResult      `json:"ack"`
	CapabilityResult                    *routing.RemoteResultOutcome `json:"capability_result,omitempty"`
}

func objectJSON(values map[string]any) json.RawMessage {
	data, err := json.Marshal(values)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return data
}
