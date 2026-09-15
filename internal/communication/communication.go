package communication

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/requestctx"
)

const (
	DirectionMainToNode = "main_to_node"
	DirectionNodeToMain = "node_to_main"

	StatusPending         = "pending"
	StatusAvailable       = "available"
	StatusClaimed         = "claimed"
	StatusDelivered       = "delivered"
	StatusAcked           = "acked"
	StatusFailedRetryable = "failed_retryable"
	StatusFailedPermanent = "failed_permanent"
	StatusDeadLetter      = "dead_letter"
	StatusCancelled       = "cancelled"
	StatusExpired         = "expired"

	KindMainPing                         = "main.ping"
	KindMainNoop                         = "main.noop"
	KindProviderAdvertisement            = "provider.advertisement"
	KindProviderAdvertisementPlaceholder = "provider.advertisement.placeholder"
	KindCapabilityDispatch               = "capability.dispatch"
	KindCapabilityResult                 = "capability.result"
	KindProtectedFolderPreflight         = "backup.protected_folder.preflight.v1"
	KindProtectedFolderReconcile         = "backup.protected_folder.reconcile.v1"
	KindProjectWatchReconcile            = "project.watch.reconcile.v1"

	MaxProtectedFolderControlPayloadBytes = 256 * 1024

	AckStatusAccepted        = "accepted"
	AckStatusCompleted       = "completed"
	AckStatusFailedRetryable = "failed_retryable"
	AckStatusFailedPermanent = "failed_permanent"
	AckStatusRejected        = "rejected"
)

type Message struct {
	CommunicationMessageID string          `json:"communication_message_id"`
	NodeID                 string          `json:"node_id"`
	Direction              string          `json:"direction"`
	Kind                   string          `json:"kind"`
	Status                 string          `json:"status"`
	IdempotencyKey         *string         `json:"idempotency_key,omitempty"`
	CorrelationID          *string         `json:"correlation_id,omitempty"`
	RouteID                *string         `json:"route_id,omitempty"`
	CapabilityCallID       *string         `json:"capability_call_id,omitempty"`
	PayloadJSON            json.RawMessage `json:"payload_json"`
	PayloadHash            *string         `json:"payload_hash,omitempty"`
	ResultJSON             json.RawMessage `json:"result_json"`
	ErrorJSON              json.RawMessage `json:"error_json"`
	AttemptCount           int             `json:"attempt_count"`
	AvailableAt            time.Time       `json:"available_at"`
	ClaimedAt              *time.Time      `json:"claimed_at,omitempty"`
	DeliveredAt            *time.Time      `json:"delivered_at,omitempty"`
	AckedAt                *time.Time      `json:"acked_at,omitempty"`
	ExpiresAt              *time.Time      `json:"expires_at,omitempty"`
	CreatedAt              time.Time       `json:"created_at"`
	UpdatedAt              time.Time       `json:"updated_at"`
	Metadata               json.RawMessage `json:"metadata"`
}

type MessageAck struct {
	CommunicationAckID     string          `json:"communication_ack_id"`
	CommunicationMessageID string          `json:"communication_message_id"`
	NodeID                 string          `json:"node_id"`
	AckStatus              string          `json:"ack_status"`
	IdempotencyKey         *string         `json:"idempotency_key,omitempty"`
	ResultJSON             json.RawMessage `json:"result_json"`
	ErrorJSON              json.RawMessage `json:"error_json"`
	ProcessedAt            *time.Time      `json:"processed_at,omitempty"`
	CreatedAt              time.Time       `json:"created_at"`
	Metadata               json.RawMessage `json:"metadata"`
}

type EnqueueInput struct {
	NodeRef          string          `json:"node_ref"`
	Kind             string          `json:"kind"`
	PayloadJSON      json.RawMessage `json:"payload,omitempty"`
	IdempotencyKey   string          `json:"idempotency_key,omitempty"`
	RouteID          string          `json:"route_id,omitempty"`
	CapabilityCallID string          `json:"capability_call_id,omitempty"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
	ExpiresAt        *time.Time      `json:"expires_at,omitempty"`
}

type PollInput struct {
	NodeRef         string          `json:"node_ref"`
	CredentialToken string          `json:"credential_token,omitempty"`
	MaxMessages     int             `json:"max_messages,omitempty"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
}

type PollResult struct {
	NodeID      string    `json:"node_id"`
	Messages    []Message `json:"messages"`
	MaxMessages int       `json:"max_messages"`
	ClaimedAt   time.Time `json:"claimed_at"`
	HasMore     bool      `json:"has_more"`
}

type AckInput struct {
	NodeRef                 string          `json:"node_ref"`
	CredentialToken         string          `json:"credential_token,omitempty"`
	CommunicationMessageRef string          `json:"communication_message_ref"`
	AckStatus               string          `json:"ack_status"`
	IdempotencyKey          string          `json:"idempotency_key,omitempty"`
	ResultJSON              json.RawMessage `json:"result,omitempty"`
	ErrorJSON               json.RawMessage `json:"error,omitempty"`
	ProcessedAt             *time.Time      `json:"processed_at,omitempty"`
	Metadata                json.RawMessage `json:"metadata,omitempty"`
}

type AckResult struct {
	Message Message    `json:"message"`
	Ack     MessageAck `json:"ack"`
}

type MessageFilter struct {
	Limit          int
	NodeRef        string
	Status         string
	Kind           string
	Direction      string
	CorrelationID  string
	IdempotencyKey string
}

type Health struct {
	TotalNodes         int `json:"total_nodes"`
	OnlineNodes        int `json:"online_nodes"`
	RecentlySeenNodes  int `json:"recently_seen_nodes"`
	OfflineNodes       int `json:"offline_nodes"`
	PendingMessages    int `json:"pending_messages"`
	FailedMessages     int `json:"failed_messages"`
	DeadLetterMessages int `json:"dead_letter_messages"`
}

type Service struct {
	DB *sql.DB
}

func NewService(db *sql.DB) Service {
	return Service{DB: db}
}

func (s Service) Enqueue(ctx context.Context, req requestctx.Context, input EnqueueInput) (Message, error) {
	input = normalizeEnqueueInput(input)
	if input.Kind == KindProjectWatchReconcile {
		return Message{}, fmt.Errorf("project watch controls require their atomic declaration owner")
	}
	if input.NodeRef == "" {
		return Message{}, fmt.Errorf("node_ref is required")
	}
	if input.Kind == "" {
		return Message{}, fmt.Errorf("kind is required")
	}
	if !validMessageKind(input.Kind) {
		return Message{}, fmt.Errorf("unsupported message kind: %s", input.Kind)
	}
	if err := validateJSONObject(input.PayloadJSON, "payload"); err != nil {
		return Message{}, err
	}
	if isProtectedFolderControlKind(input.Kind) && len(input.PayloadJSON) > MaxProtectedFolderControlPayloadBytes {
		return Message{}, fmt.Errorf("protected-folder control payload exceeds %d bytes", MaxProtectedFolderControlPayloadBytes)
	}
	if input.ExpiresAt != nil && !input.ExpiresAt.After(time.Now().UTC()) {
		return Message{}, fmt.Errorf("expires_at must be in the future")
	}
	if err := validateJSONObject(input.Metadata, "metadata"); err != nil {
		return Message{}, err
	}
	node, err := nodes.NewService(s.DB).GetNode(ctx, input.NodeRef)
	if err != nil {
		return Message{}, err
	}
	if node.Status != "active" {
		return Message{}, fmt.Errorf("node is not active: %s", node.Status)
	}
	payloadHash := hashPayload(input.PayloadJSON)
	if input.IdempotencyKey != "" {
		existing, found, err := s.findByIdempotency(ctx, node.NodeID, DirectionMainToNode, input.IdempotencyKey)
		if err != nil {
			return Message{}, err
		}
		if found {
			if existing.Kind != input.Kind || pointerValue(existing.PayloadHash) != payloadHash {
				return Message{}, fmt.Errorf("idempotency conflict for message key")
			}
			return existing, nil
		}
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, err
	}
	defer tx.Rollback()

	message, err := enqueueMessageTx(ctx, tx, req, node.NodeID, input, payloadHash)
	if err != nil {
		return Message{}, err
	}
	if err = tx.Commit(); err != nil {
		return Message{}, err
	}
	return message, nil
}

func enqueueMessageTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, nodeID string, input EnqueueInput, payloadHash string) (Message, error) {
	message, err := scanMessage(tx.QueryRowContext(ctx, `
		INSERT INTO communication.messages (
			communication_message_id, node_id, direction, kind, status,
			idempotency_key, correlation_id, route_id, capability_call_id,
			payload_json, payload_hash, metadata, expires_at
		)
		VALUES ($1, $2, 'main_to_node', $3, 'available',
		        nullif($4, ''), nullif($5, ''), nullif($6, ''), nullif($7, ''),
		        $8, $9, $10, $11)
		RETURNING `+messageColumns()+`
	`, ids.NewCommunicationMessageID(), nodeID, input.Kind, input.IdempotencyKey,
		req.CorrelationID, input.RouteID, input.CapabilityCallID, input.PayloadJSON,
		payloadHash, input.Metadata, input.ExpiresAt))
	if err != nil {
		return Message{}, err
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  events.TypeCommunicationMessageCreated,
		EventLevel: "audit",
		Request:    req,
		ScopeID:    req.ScopeID,
		TargetKind: "communication_message",
		TargetID:   message.CommunicationMessageID,
		Status:     message.Status,
		Result:     "created",
		Payload: map[string]any{
			"communication_message_id": message.CommunicationMessageID,
			"node_id":                  message.NodeID,
			"direction":                message.Direction,
			"kind":                     message.Kind,
			"status":                   message.Status,
		},
		VisibilityClass: "internal",
	}); err != nil {
		return Message{}, err
	}
	return message, nil
}

func (s Service) ListMessages(ctx context.Context, filter MessageFilter) ([]Message, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	query := `SELECT ` + messageColumns() + ` FROM communication.messages WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if strings.TrimSpace(filter.NodeRef) != "" {
		node, err := nodes.NewService(s.DB).GetNode(ctx, filter.NodeRef)
		if err != nil {
			return nil, err
		}
		add("node_id =", node.NodeID)
	}
	if strings.TrimSpace(filter.Status) != "" {
		add("status =", strings.TrimSpace(filter.Status))
	}
	if strings.TrimSpace(filter.Kind) != "" {
		add("kind =", strings.TrimSpace(filter.Kind))
	}
	if strings.TrimSpace(filter.Direction) != "" {
		add("direction =", strings.TrimSpace(filter.Direction))
	}
	if strings.TrimSpace(filter.CorrelationID) != "" {
		add("correlation_id =", strings.TrimSpace(filter.CorrelationID))
	}
	if strings.TrimSpace(filter.IdempotencyKey) != "" {
		add("idempotency_key =", strings.TrimSpace(filter.IdempotencyKey))
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		message, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, message)
	}
	return out, rows.Err()
}

func (s Service) GetMessage(ctx context.Context, ref string) (Message, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Message{}, fmt.Errorf("message ref is required")
	}
	return scanMessage(s.DB.QueryRowContext(ctx, `SELECT `+messageColumns()+`
		FROM communication.messages
		WHERE communication_message_id = $1
	`, ref))
}

func (s Service) Poll(ctx context.Context, req requestctx.Context, input PollInput) (PollResult, error) {
	input = normalizePollInput(input)
	if input.NodeRef == "" {
		return PollResult{}, fmt.Errorf("node_ref is required")
	}
	if input.CredentialToken == "" {
		return PollResult{}, fmt.Errorf("credential_token is required")
	}
	node, err := s.authenticateNode(ctx, input.NodeRef, input.CredentialToken)
	if err != nil {
		return PollResult{}, err
	}
	if input.MaxMessages <= 0 {
		input.MaxMessages = 10
	}
	if input.MaxMessages > 50 {
		input.MaxMessages = 50
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return PollResult{}, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		WITH candidate AS (
			SELECT communication_message_id
			FROM communication.messages
			WHERE node_id = $1
			  AND direction = 'main_to_node'
			  AND status = 'available'
			  AND available_at <= now()
			  AND (expires_at IS NULL OR expires_at > now())
			ORDER BY available_at ASC, created_at ASC
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE communication.messages AS m
		SET status = 'delivered',
		    claimed_at = COALESCE(m.claimed_at, now()),
		    delivered_at = now(),
		    attempt_count = m.attempt_count + 1,
		    updated_at = now()
		FROM candidate
		WHERE m.communication_message_id = candidate.communication_message_id
		RETURNING `+messageColumnsFor("m")+`
	`, node.NodeID, input.MaxMessages)
	if err != nil {
		return PollResult{}, err
	}
	defer rows.Close()

	messages := []Message{}
	for rows.Next() {
		message, err := scanMessage(rows)
		if err != nil {
			return PollResult{}, err
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return PollResult{}, err
	}
	for _, message := range messages {
		if _, err := events.AppendTx(ctx, tx, events.AppendInput{
			EventType:  events.TypeCommunicationMessageClaimed,
			EventLevel: "node_activity",
			Request:    req,
			ScopeID:    req.ScopeID,
			TargetKind: "communication_message",
			TargetID:   message.CommunicationMessageID,
			Status:     message.Status,
			Result:     "delivered",
			Payload: map[string]any{
				"communication_message_id": message.CommunicationMessageID,
				"node_id":                  message.NodeID,
				"kind":                     message.Kind,
				"attempt_count":            message.AttemptCount,
			},
			VisibilityClass: "internal",
		}); err != nil {
			return PollResult{}, err
		}
	}
	var remaining int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM communication.messages
		WHERE node_id = $1
		  AND direction = 'main_to_node'
		  AND status = 'available'
		  AND available_at <= now()
		  AND (expires_at IS NULL OR expires_at > now())
	`, node.NodeID).Scan(&remaining); err != nil {
		return PollResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return PollResult{}, err
	}
	return PollResult{
		NodeID:      node.NodeID,
		Messages:    messages,
		MaxMessages: input.MaxMessages,
		ClaimedAt:   time.Now().UTC(),
		HasMore:     remaining > 0,
	}, nil
}

func (s Service) Ack(ctx context.Context, req requestctx.Context, input AckInput) (AckResult, error) {
	input = normalizeAckInput(input)
	if input.NodeRef == "" {
		return AckResult{}, fmt.Errorf("node_ref is required")
	}
	if input.CredentialToken == "" {
		return AckResult{}, fmt.Errorf("credential_token is required")
	}
	if input.CommunicationMessageRef == "" {
		return AckResult{}, fmt.Errorf("communication_message_ref is required")
	}
	if !validAckStatus(input.AckStatus) {
		return AckResult{}, fmt.Errorf("unsupported ack status: %s", input.AckStatus)
	}
	if err := validateJSONObject(input.ResultJSON, "result"); err != nil {
		return AckResult{}, err
	}
	if err := validateJSONObject(input.ErrorJSON, "error"); err != nil {
		return AckResult{}, err
	}
	node, err := s.authenticateNode(ctx, input.NodeRef, input.CredentialToken)
	if err != nil {
		return AckResult{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return AckResult{}, err
	}
	defer tx.Rollback()

	message, err := scanMessage(tx.QueryRowContext(ctx, `SELECT `+messageColumns()+`
		FROM communication.messages
		WHERE communication_message_id = $1
		FOR UPDATE
	`, input.CommunicationMessageRef))
	if err != nil {
		return AckResult{}, err
	}
	if message.NodeID != node.NodeID {
		return AckResult{}, fmt.Errorf("message does not belong to node %s", node.NodeID)
	}
	if message.Direction != DirectionMainToNode {
		return AckResult{}, fmt.Errorf("message is not main-to-node")
	}

	if existing, found, err := s.findAckByMessageTx(ctx, tx, message.CommunicationMessageID); err != nil {
		return AckResult{}, err
	} else if found {
		if !sameAck(existing, input) {
			return AckResult{}, fmt.Errorf("message already has a conflicting acknowledgement")
		}
		return AckResult{Message: message, Ack: existing}, nil
	}
	if input.IdempotencyKey != "" {
		if existing, found, err := s.findAckByIdempotencyTx(ctx, tx, node.NodeID, input.IdempotencyKey); err != nil {
			return AckResult{}, err
		} else if found {
			if existing.CommunicationMessageID != message.CommunicationMessageID || !sameAck(existing, input) {
				return AckResult{}, fmt.Errorf("idempotency conflict for acknowledgement key")
			}
			return AckResult{Message: message, Ack: existing}, nil
		}
	}

	ack, err := scanAck(tx.QueryRowContext(ctx, `
		INSERT INTO communication.message_acks (
			communication_ack_id, communication_message_id, node_id, ack_status,
			idempotency_key, result_json, error_json, processed_at, metadata
		)
		VALUES ($1, $2, $3, $4, nullif($5, ''), $6, $7, $8, $9)
		RETURNING `+ackColumns()+`
	`, ids.NewCommunicationAckID(), message.CommunicationMessageID, node.NodeID, input.AckStatus,
		input.IdempotencyKey, input.ResultJSON, input.ErrorJSON, input.ProcessedAt, input.Metadata))
	if err != nil {
		return AckResult{}, err
	}
	nextStatus := messageStatusForAck(input.AckStatus)
	message, err = scanMessage(tx.QueryRowContext(ctx, `
		UPDATE communication.messages
		SET status = $2,
		    result_json = $3,
		    error_json = $4,
		    acked_at = CASE WHEN $2 = 'acked' THEN now() ELSE acked_at END,
		    updated_at = now()
		WHERE communication_message_id = $1
		RETURNING `+messageColumns()+`
	`, message.CommunicationMessageID, nextStatus, input.ResultJSON, input.ErrorJSON))
	if err != nil {
		return AckResult{}, err
	}
	eventType := events.TypeCommunicationMessageAcked
	eventResult := "acked"
	if nextStatus != StatusAcked {
		eventType = events.TypeCommunicationMessageFailed
		eventResult = nextStatus
	}
	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:  eventType,
		EventLevel: "node_activity",
		Request:    req,
		ScopeID:    req.ScopeID,
		TargetKind: "communication_message",
		TargetID:   message.CommunicationMessageID,
		Status:     message.Status,
		Result:     eventResult,
		Payload: map[string]any{
			"communication_message_id": message.CommunicationMessageID,
			"communication_ack_id":     ack.CommunicationAckID,
			"node_id":                  message.NodeID,
			"ack_status":               ack.AckStatus,
		},
		VisibilityClass: "internal",
	}); err != nil {
		return AckResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return AckResult{}, err
	}
	return AckResult{Message: message, Ack: ack}, nil
}

func (s Service) Health(ctx context.Context) (Health, error) {
	var health Health
	if err := s.DB.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COUNT(*) FILTER (WHERE presence_state = 'online'),
			COUNT(*) FILTER (WHERE presence_state = 'recently_seen'),
			COUNT(*) FILTER (WHERE presence_state = 'offline')
		FROM nodes.nodes
	`).Scan(&health.TotalNodes, &health.OnlineNodes, &health.RecentlySeenNodes, &health.OfflineNodes); err != nil {
		return Health{}, err
	}
	if err := s.DB.QueryRowContext(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE status IN ('pending', 'available', 'claimed', 'delivered')),
			COUNT(*) FILTER (WHERE status IN ('failed_retryable', 'failed_permanent')),
			COUNT(*) FILTER (WHERE status = 'dead_letter')
		FROM communication.messages
	`).Scan(&health.PendingMessages, &health.FailedMessages, &health.DeadLetterMessages); err != nil {
		return Health{}, err
	}
	return health, nil
}

func (s Service) authenticateNode(ctx context.Context, nodeRef, credentialToken string) (nodes.Node, error) {
	nodeService := nodes.NewService(s.DB)
	node, err := nodeService.GetNode(ctx, nodeRef)
	if err != nil {
		return nodes.Node{}, err
	}
	_, credentialNode, err := nodeService.AuthenticateCredential(ctx, credentialToken)
	if err != nil {
		return nodes.Node{}, err
	}
	if credentialNode.NodeID != node.NodeID {
		return nodes.Node{}, fmt.Errorf("credential does not belong to node %s", node.NodeID)
	}
	return node, nil
}

func (s Service) findByIdempotency(ctx context.Context, nodeID, direction, key string) (Message, bool, error) {
	message, err := scanMessage(s.DB.QueryRowContext(ctx, `SELECT `+messageColumns()+`
		FROM communication.messages
		WHERE node_id = $1 AND direction = $2 AND idempotency_key = $3
	`, nodeID, direction, key))
	if err == sql.ErrNoRows {
		return Message{}, false, nil
	}
	if err != nil {
		return Message{}, false, err
	}
	return message, true, nil
}

func normalizeEnqueueInput(input EnqueueInput) EnqueueInput {
	input.NodeRef = strings.TrimSpace(input.NodeRef)
	input.Kind = strings.TrimSpace(input.Kind)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.RouteID = strings.TrimSpace(input.RouteID)
	input.CapabilityCallID = strings.TrimSpace(input.CapabilityCallID)
	input.PayloadJSON = objectOrDefault(input.PayloadJSON)
	input.Metadata = objectOrDefault(input.Metadata)
	return input
}

func normalizePollInput(input PollInput) PollInput {
	input.NodeRef = strings.TrimSpace(input.NodeRef)
	input.CredentialToken = strings.TrimSpace(input.CredentialToken)
	input.Metadata = objectOrDefault(input.Metadata)
	return input
}

func normalizeAckInput(input AckInput) AckInput {
	input.NodeRef = strings.TrimSpace(input.NodeRef)
	input.CredentialToken = strings.TrimSpace(input.CredentialToken)
	input.CommunicationMessageRef = strings.TrimSpace(input.CommunicationMessageRef)
	input.AckStatus = strings.TrimSpace(input.AckStatus)
	if input.AckStatus == "" {
		input.AckStatus = AckStatusCompleted
	}
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.ResultJSON = objectOrDefault(input.ResultJSON)
	input.ErrorJSON = objectOrDefault(input.ErrorJSON)
	input.Metadata = objectOrDefault(input.Metadata)
	return input
}

func validMessageKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case KindMainPing, KindMainNoop, KindProviderAdvertisement, KindProviderAdvertisementPlaceholder, KindCapabilityDispatch, KindCapabilityResult,
		KindProtectedFolderPreflight, KindProtectedFolderReconcile, KindProjectWatchReconcile:
		return true
	default:
		return false
	}
}

func isProtectedFolderControlKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case KindProtectedFolderPreflight, KindProtectedFolderReconcile:
		return true
	default:
		return false
	}
}

func validAckStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case AckStatusAccepted, AckStatusCompleted, AckStatusFailedRetryable, AckStatusFailedPermanent, AckStatusRejected:
		return true
	default:
		return false
	}
}

func messageStatusForAck(status string) string {
	switch status {
	case AckStatusAccepted, AckStatusCompleted:
		return StatusAcked
	case AckStatusFailedRetryable:
		return StatusFailedRetryable
	default:
		return StatusFailedPermanent
	}
}

func sameAck(ack MessageAck, input AckInput) bool {
	return ack.AckStatus == input.AckStatus &&
		pointerValue(ack.IdempotencyKey) == input.IdempotencyKey &&
		sameJSONObject(ack.ResultJSON, input.ResultJSON) &&
		sameJSONObject(ack.ErrorJSON, input.ErrorJSON)
}

func sameJSONObject(left, right json.RawMessage) bool {
	var leftObject map[string]any
	var rightObject map[string]any
	if err := json.Unmarshal(objectOrDefault(left), &leftObject); err != nil {
		return false
	}
	if err := json.Unmarshal(objectOrDefault(right), &rightObject); err != nil {
		return false
	}
	leftCanonical, err := json.Marshal(leftObject)
	if err != nil {
		return false
	}
	rightCanonical, err := json.Marshal(rightObject)
	if err != nil {
		return false
	}
	return string(leftCanonical) == string(rightCanonical)
}

func hashPayload(payload json.RawMessage) string {
	sum := sha256.Sum256(objectOrDefault(payload))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func messageColumns() string {
	return `
		communication_message_id, node_id, direction, kind, status,
		idempotency_key, correlation_id, route_id, capability_call_id,
		payload_json, payload_hash, result_json, error_json, attempt_count,
		available_at, claimed_at, delivered_at, acked_at, expires_at,
		created_at, updated_at, metadata`
}

func messageColumnsFor(alias string) string {
	prefix := strings.TrimSpace(alias) + "."
	return `
		` + prefix + `communication_message_id, ` + prefix + `node_id, ` + prefix + `direction, ` + prefix + `kind, ` + prefix + `status,
		` + prefix + `idempotency_key, ` + prefix + `correlation_id, ` + prefix + `route_id, ` + prefix + `capability_call_id,
		` + prefix + `payload_json, ` + prefix + `payload_hash, ` + prefix + `result_json, ` + prefix + `error_json, ` + prefix + `attempt_count,
		` + prefix + `available_at, ` + prefix + `claimed_at, ` + prefix + `delivered_at, ` + prefix + `acked_at, ` + prefix + `expires_at,
		` + prefix + `created_at, ` + prefix + `updated_at, ` + prefix + `metadata`
}

func ackColumns() string {
	return `
		communication_ack_id, communication_message_id, node_id, ack_status,
		idempotency_key, result_json, error_json, processed_at, created_at, metadata`
}

func scanMessage(scanner rowScanner) (Message, error) {
	var message Message
	var idempotencyKey, correlationID, routeID, callID, payloadHash sql.NullString
	var claimedAt, deliveredAt, ackedAt, expiresAt sql.NullTime
	var payload, result, errJSON, metadata []byte
	if err := scanner.Scan(
		&message.CommunicationMessageID,
		&message.NodeID,
		&message.Direction,
		&message.Kind,
		&message.Status,
		&idempotencyKey,
		&correlationID,
		&routeID,
		&callID,
		&payload,
		&payloadHash,
		&result,
		&errJSON,
		&message.AttemptCount,
		&message.AvailableAt,
		&claimedAt,
		&deliveredAt,
		&ackedAt,
		&expiresAt,
		&message.CreatedAt,
		&message.UpdatedAt,
		&metadata,
	); err != nil {
		return Message{}, err
	}
	message.IdempotencyKey = stringPtr(idempotencyKey)
	message.CorrelationID = stringPtr(correlationID)
	message.RouteID = stringPtr(routeID)
	message.CapabilityCallID = stringPtr(callID)
	message.PayloadHash = stringPtr(payloadHash)
	message.ClaimedAt = timePtr(claimedAt)
	message.DeliveredAt = timePtr(deliveredAt)
	message.AckedAt = timePtr(ackedAt)
	message.ExpiresAt = timePtr(expiresAt)
	message.PayloadJSON = jsonOrEmpty(payload)
	message.ResultJSON = jsonOrEmpty(result)
	message.ErrorJSON = jsonOrEmpty(errJSON)
	message.Metadata = jsonOrEmpty(metadata)
	return message, nil
}

func scanAck(scanner rowScanner) (MessageAck, error) {
	var ack MessageAck
	var idempotencyKey sql.NullString
	var processedAt sql.NullTime
	var result, errJSON, metadata []byte
	if err := scanner.Scan(
		&ack.CommunicationAckID,
		&ack.CommunicationMessageID,
		&ack.NodeID,
		&ack.AckStatus,
		&idempotencyKey,
		&result,
		&errJSON,
		&processedAt,
		&ack.CreatedAt,
		&metadata,
	); err != nil {
		return MessageAck{}, err
	}
	ack.IdempotencyKey = stringPtr(idempotencyKey)
	ack.ResultJSON = jsonOrEmpty(result)
	ack.ErrorJSON = jsonOrEmpty(errJSON)
	ack.ProcessedAt = timePtr(processedAt)
	ack.Metadata = jsonOrEmpty(metadata)
	return ack, nil
}

func (s Service) findAckByMessageTx(ctx context.Context, tx *sql.Tx, messageID string) (MessageAck, bool, error) {
	ack, err := scanAck(tx.QueryRowContext(ctx, `SELECT `+ackColumns()+`
		FROM communication.message_acks
		WHERE communication_message_id = $1
	`, messageID))
	if err == sql.ErrNoRows {
		return MessageAck{}, false, nil
	}
	if err != nil {
		return MessageAck{}, false, err
	}
	return ack, true, nil
}

func (s Service) findAckByIdempotencyTx(ctx context.Context, tx *sql.Tx, nodeID, key string) (MessageAck, bool, error) {
	ack, err := scanAck(tx.QueryRowContext(ctx, `SELECT `+ackColumns()+`
		FROM communication.message_acks
		WHERE node_id = $1 AND idempotency_key = $2
	`, nodeID, key))
	if err == sql.ErrNoRows {
		return MessageAck{}, false, nil
	}
	if err != nil {
		return MessageAck{}, false, err
	}
	return ack, true, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func objectOrDefault(raw json.RawMessage) json.RawMessage {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{}`)
	}
	return raw
}

func validateJSONObject(raw json.RawMessage, field string) error {
	raw = objectOrDefault(raw)
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return fmt.Errorf("%s must be a valid JSON object: %w", field, err)
	}
	if object == nil {
		return fmt.Errorf("%s must be a JSON object", field)
	}
	return nil
}

func jsonOrEmpty(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(raw)
}

func stringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func timePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// EnqueueProjectWatchTx couples one bounded project desired-state control to its
// owner transaction. It cannot enqueue another kind or commit the caller's tx.
func EnqueueProjectWatchTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, nodeID, key string, payload json.RawMessage) (Message, error) {
	if tx == nil || ids.Validate(ids.NodePrefix, nodeID) != nil || strings.TrimSpace(key) != key || key == "" || len(key) > 256 || len(payload) > MaxProtectedFolderControlPayloadBytes {
		return Message{}, fmt.Errorf("invalid project watch control coordinates")
	}
	var object map[string]json.RawMessage
	if err := DecodeStrictJSONObject(payload, &object); err != nil {
		return Message{}, err
	}
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM nodes.nodes WHERE node_id=$1 FOR SHARE`, nodeID).Scan(&status); err != nil {
		return Message{}, err
	}
	if status != "active" {
		return Message{}, fmt.Errorf("project watch node is not active")
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "loom:project-watch-message:"+nodeID+":"+key); err != nil {
		return Message{}, err
	}
	hash := hashPayload(payload)
	existing, err := scanMessage(tx.QueryRowContext(ctx, `SELECT `+messageColumns()+` FROM communication.messages WHERE node_id=$1 AND direction='main_to_node' AND idempotency_key=$2 FOR UPDATE`, nodeID, key))
	if err == nil {
		if existing.Kind != KindProjectWatchReconcile || pointerValue(existing.PayloadHash) != hash {
			return Message{}, fmt.Errorf("project watch message idempotency conflict")
		}
		return existing, nil
	}
	if err != sql.ErrNoRows {
		return Message{}, err
	}
	return enqueueMessageTx(ctx, tx, req, nodeID, EnqueueInput{NodeRef: nodeID, Kind: KindProjectWatchReconcile, IdempotencyKey: key, PayloadJSON: payload, Metadata: json.RawMessage(`{}`)}, hash)
}
