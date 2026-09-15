package nodes

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
)

const (
	PresenceUnknown      = "unknown"
	PresenceOnline       = "online"
	PresenceRecentlySeen = "recently_seen"
	PresenceOffline      = "offline"
	PresenceDegraded     = "degraded"
	PresenceQuarantined  = "quarantined"
	PresenceRevoked      = "revoked"
)

type HeartbeatInput struct {
	NodeRef           string          `json:"node_ref,omitempty"`
	CredentialToken   string          `json:"credential_token,omitempty"`
	RuntimeVersion    string          `json:"runtime_version,omitempty"`
	ReportedStatus    string          `json:"reported_status,omitempty"`
	InboxBacklog      int             `json:"inbox_backlog,omitempty"`
	OutboxBacklog     int             `json:"outbox_backlog,omitempty"`
	StorageStatusJSON json.RawMessage `json:"storage_status,omitempty"`
	ErrorSummaryJSON  json.RawMessage `json:"error_summary,omitempty"`
	ReportedAt        *time.Time      `json:"reported_at,omitempty"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
}

type Heartbeat struct {
	NodeHeartbeatID   string          `json:"node_heartbeat_id"`
	NodeID            string          `json:"node_id"`
	RuntimeVersion    *string         `json:"runtime_version,omitempty"`
	ReportedStatus    string          `json:"reported_status"`
	PresenceState     string          `json:"presence_state"`
	InboxBacklog      int             `json:"inbox_backlog"`
	OutboxBacklog     int             `json:"outbox_backlog"`
	StorageStatusJSON json.RawMessage `json:"storage_status_json"`
	ErrorSummaryJSON  json.RawMessage `json:"error_summary_json"`
	PayloadJSON       json.RawMessage `json:"payload_json"`
	ReportedAt        *time.Time      `json:"reported_at,omitempty"`
	ReceivedAt        time.Time       `json:"received_at"`
	Metadata          json.RawMessage `json:"metadata"`
}

type NodeHealth struct {
	Node                Node       `json:"node"`
	LastHeartbeat       *Heartbeat `json:"last_heartbeat,omitempty"`
	HeartbeatAgeSeconds *int64     `json:"heartbeat_age_seconds,omitempty"`
	PendingMessages     int        `json:"pending_messages"`
	FailedMessages      int        `json:"failed_messages"`
	DeadLetterMessages  int        `json:"dead_letter_messages"`
}

func (s Service) IngestHeartbeat(ctx context.Context, req requestctx.Context, input HeartbeatInput) (Heartbeat, error) {
	input = normalizeHeartbeatInput(input)
	if input.NodeRef == "" {
		return Heartbeat{}, fmt.Errorf("node_ref is required")
	}
	if input.CredentialToken == "" {
		return Heartbeat{}, fmt.Errorf("credential_token is required")
	}
	if input.ReportedStatus == "" {
		input.ReportedStatus = "ok"
	}
	if input.InboxBacklog < 0 || input.OutboxBacklog < 0 {
		return Heartbeat{}, fmt.Errorf("backlog values must be non-negative")
	}
	if err := validateObjectJSON(input.StorageStatusJSON, "storage_status"); err != nil {
		return Heartbeat{}, err
	}
	if err := validateObjectJSON(input.ErrorSummaryJSON, "error_summary"); err != nil {
		return Heartbeat{}, err
	}
	node, err := s.GetNode(ctx, input.NodeRef)
	if err != nil {
		return Heartbeat{}, err
	}
	if node.Status == "disabled" || node.Status == "retired" || node.Status == "quarantined" {
		return Heartbeat{}, fmt.Errorf("node cannot heartbeat while status is %s", node.Status)
	}
	_, credentialNode, err := s.AuthenticateCredential(ctx, input.CredentialToken)
	if err != nil {
		return Heartbeat{}, err
	}
	if credentialNode.NodeID != node.NodeID {
		return Heartbeat{}, fmt.Errorf("credential does not belong to node %s", node.NodeID)
	}
	presence := heartbeatPresence(node, input.ReportedStatus)
	payload := mustJSON(map[string]any{
		"node_id":         node.NodeID,
		"runtime_version": input.RuntimeVersion,
		"reported_status": input.ReportedStatus,
		"inbox_backlog":   input.InboxBacklog,
		"outbox_backlog":  input.OutboxBacklog,
	})

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Heartbeat{}, err
	}
	defer tx.Rollback()

	heartbeat, err := scanHeartbeat(tx.QueryRowContext(ctx, `
		INSERT INTO nodes.heartbeats (
			node_heartbeat_id, node_id, runtime_version, reported_status,
			presence_state, inbox_backlog, outbox_backlog, storage_status_json,
			error_summary_json, payload_json, reported_at, metadata
		)
		VALUES ($1, $2, nullif($3, ''), $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING `+heartbeatColumns()+`
	`, ids.NewNodeHeartbeatID(), node.NodeID, input.RuntimeVersion, input.ReportedStatus,
		presence, input.InboxBacklog, input.OutboxBacklog, objectOrDefault(input.StorageStatusJSON),
		objectOrDefault(input.ErrorSummaryJSON), payload, input.ReportedAt, objectOrDefault(input.Metadata)))
	if err != nil {
		return Heartbeat{}, err
	}
	previousPresence := node.PresenceState
	if _, err := tx.ExecContext(ctx, `
		UPDATE nodes.nodes
		SET presence_state = $2,
		    last_heartbeat_at = $3,
		    last_seen_at = $3,
		    runtime_version = nullif($4, ''),
		    updated_at = now()
		WHERE node_id = $1
	`, node.NodeID, presence, heartbeat.ReceivedAt, input.RuntimeVersion); err != nil {
		return Heartbeat{}, err
	}
	if previousPresence != presence {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO nodes.node_status_history (
				node_status_history_id, node_id, previous_presence_state,
				next_presence_state, reason_code, heartbeat_id, metadata
			)
			VALUES ($1, $2, nullif($3, ''), $4, $5, $6, '{"slice":"10"}'::jsonb)
		`, "node_status_history_"+ids.NewEventID(), node.NodeID, previousPresence, presence, "heartbeat", heartbeat.NodeHeartbeatID); err != nil {
			return Heartbeat{}, err
		}
		if _, err := events.AppendTx(ctx, tx, events.AppendInput{
			EventType:  events.TypeNodePresenceChanged,
			EventLevel: "audit",
			Request:    req,
			ScopeID:    req.ScopeID,
			TargetKind: "node",
			TargetID:   node.NodeID,
			Status:     presence,
			Result:     "presence_changed",
			Payload: map[string]any{
				"node_id":                 node.NodeID,
				"previous_presence_state": previousPresence,
				"next_presence_state":     presence,
				"heartbeat_id":            heartbeat.NodeHeartbeatID,
			},
			VisibilityClass: "internal",
		}); err != nil {
			return Heartbeat{}, err
		}
	}
	if previousPresence != presence || input.ReportedStatus != "ok" {
		if _, err := events.AppendTx(ctx, tx, events.AppendInput{
			EventType:  events.TypeNodeHeartbeatReceived,
			EventLevel: "node_activity",
			Request:    req,
			ScopeID:    req.ScopeID,
			TargetKind: "node",
			TargetID:   node.NodeID,
			Status:     input.ReportedStatus,
			Result:     "heartbeat_received",
			Payload: map[string]any{
				"node_id":           node.NodeID,
				"node_heartbeat_id": heartbeat.NodeHeartbeatID,
				"presence_state":    presence,
				"inbox_backlog":     input.InboxBacklog,
				"outbox_backlog":    input.OutboxBacklog,
			},
			VisibilityClass: "internal",
		}); err != nil {
			return Heartbeat{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Heartbeat{}, err
	}
	return heartbeat, nil
}

func (s Service) AuthenticateCredential(ctx context.Context, token string) (NodeCredential, Node, error) {
	hint := tokenHint(token)
	rows, err := s.DB.QueryContext(ctx, `SELECT `+nodeCredentialColumns()+`, credential_hash
		FROM security.node_auth_credentials
		WHERE status = 'active' AND credential_hint = $1
		ORDER BY created_at DESC
	`, hint)
	if err != nil {
		return NodeCredential{}, Node{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var hash string
		credential, err := scanNodeCredentialWithHash(rows, &hash)
		if err != nil {
			return NodeCredential{}, Node{}, err
		}
		if verifySecretToken(token, hash) {
			node, err := s.GetNode(ctx, credential.NodeID)
			if err != nil {
				return NodeCredential{}, Node{}, err
			}
			if node.Status != "active" {
				return NodeCredential{}, Node{}, fmt.Errorf("node is not active: %s", node.Status)
			}
			if _, err := s.DB.ExecContext(ctx, `
				UPDATE security.node_auth_credentials
				SET last_used_at = now()
				WHERE node_credential_id = $1
			`, credential.NodeCredentialID); err != nil {
				return NodeCredential{}, Node{}, err
			}
			return credential, node, nil
		}
	}
	if err := rows.Err(); err != nil {
		return NodeCredential{}, Node{}, err
	}
	return NodeCredential{}, Node{}, fmt.Errorf("node credential is invalid")
}

func (s Service) GetNodeHealth(ctx context.Context, ref string) (NodeHealth, error) {
	node, err := s.GetNode(ctx, ref)
	if err != nil {
		return NodeHealth{}, err
	}
	health := NodeHealth{Node: node}
	heartbeat, err := scanHeartbeat(s.DB.QueryRowContext(ctx, `SELECT `+heartbeatColumns()+`
		FROM nodes.heartbeats
		WHERE node_id = $1
		ORDER BY received_at DESC
		LIMIT 1
	`, node.NodeID))
	if err == nil {
		health.LastHeartbeat = &heartbeat
		age := int64(time.Since(heartbeat.ReceivedAt).Seconds())
		health.HeartbeatAgeSeconds = &age
	} else if !errorsIsNoRows(err) {
		return NodeHealth{}, err
	}
	if err := s.DB.QueryRowContext(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE status IN ('pending', 'available', 'claimed', 'delivered')),
			COUNT(*) FILTER (WHERE status IN ('failed_retryable', 'failed_permanent')),
			COUNT(*) FILTER (WHERE status = 'dead_letter')
		FROM communication.messages
		WHERE node_id = $1
	`, node.NodeID).Scan(&health.PendingMessages, &health.FailedMessages, &health.DeadLetterMessages); err != nil {
		return NodeHealth{}, err
	}
	return health, nil
}

func normalizeHeartbeatInput(input HeartbeatInput) HeartbeatInput {
	input.NodeRef = strings.TrimSpace(input.NodeRef)
	input.CredentialToken = strings.TrimSpace(input.CredentialToken)
	input.RuntimeVersion = strings.TrimSpace(input.RuntimeVersion)
	input.ReportedStatus = defaultString(input.ReportedStatus, "ok")
	input.StorageStatusJSON = objectOrDefault(input.StorageStatusJSON)
	input.ErrorSummaryJSON = objectOrDefault(input.ErrorSummaryJSON)
	input.Metadata = objectOrDefault(input.Metadata)
	return input
}

func heartbeatPresence(node Node, reportedStatus string) string {
	switch node.Status {
	case "quarantined":
		return PresenceQuarantined
	case "retired":
		return PresenceRevoked
	}
	if reportedStatus == "degraded" || reportedStatus == "error" {
		return PresenceDegraded
	}
	if reportedStatus == "offline" {
		return PresenceOffline
	}
	return PresenceOnline
}

func heartbeatColumns() string {
	return `
		node_heartbeat_id, node_id, runtime_version, reported_status,
		presence_state, inbox_backlog, outbox_backlog, storage_status_json,
		error_summary_json, payload_json, reported_at, received_at, metadata`
}

func scanHeartbeat(scanner nodeScanner) (Heartbeat, error) {
	var heartbeat Heartbeat
	var runtimeVersion sql.NullString
	var reportedAt sql.NullTime
	var storageStatus, errorSummary, payload, metadata []byte
	if err := scanner.Scan(
		&heartbeat.NodeHeartbeatID,
		&heartbeat.NodeID,
		&runtimeVersion,
		&heartbeat.ReportedStatus,
		&heartbeat.PresenceState,
		&heartbeat.InboxBacklog,
		&heartbeat.OutboxBacklog,
		&storageStatus,
		&errorSummary,
		&payload,
		&reportedAt,
		&heartbeat.ReceivedAt,
		&metadata,
	); err != nil {
		return Heartbeat{}, err
	}
	heartbeat.RuntimeVersion = stringPtr(runtimeVersion)
	heartbeat.ReportedAt = timePtr(reportedAt)
	heartbeat.StorageStatusJSON = jsonOrEmpty(storageStatus)
	heartbeat.ErrorSummaryJSON = jsonOrEmpty(errorSummary)
	heartbeat.PayloadJSON = jsonOrEmpty(payload)
	heartbeat.Metadata = jsonOrEmpty(metadata)
	return heartbeat, nil
}

func scanNodeCredentialWithHash(scanner nodeScanner, hash *string) (NodeCredential, error) {
	var credential NodeCredential
	var issuedBy, revokedReason sql.NullString
	var expiresAt, lastUsedAt, revokedAt sql.NullTime
	var metadata []byte
	dest := []any{
		&credential.NodeCredentialID,
		&credential.NodeID,
		&credential.CredentialKind,
		&credential.CredentialHint,
		&credential.Status,
		&issuedBy,
		&credential.CreatedAt,
		&expiresAt,
		&lastUsedAt,
		&revokedAt,
		&revokedReason,
		&metadata,
	}
	if hash != nil {
		dest = append(dest, hash)
	}
	if err := scanner.Scan(dest...); err != nil {
		return NodeCredential{}, err
	}
	credential.IssuedByActorID = stringPtr(issuedBy)
	credential.ExpiresAt = timePtr(expiresAt)
	credential.LastUsedAt = timePtr(lastUsedAt)
	credential.RevokedAt = timePtr(revokedAt)
	credential.RevokedReason = stringPtr(revokedReason)
	credential.Metadata = jsonOrEmpty(metadata)
	return credential, nil
}

func errorsIsNoRows(err error) bool {
	return err == sql.ErrNoRows
}
