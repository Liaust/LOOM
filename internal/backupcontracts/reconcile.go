package backupcontracts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	agentwatchedroots "loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/requestctx"
)

const ReconcileMessageTTL = 24 * time.Hour

type ReconcileQueueResult struct {
	NodeID          string                `json:"node_id"`
	DesiredRevision int64                 `json:"desired_revision"`
	DesiredHash     string                `json:"desired_hash"`
	Message         communication.Message `json:"message"`
	Reused          bool                  `json:"reused"`
}

type ReconcileService struct {
	DB            *sql.DB
	Communication communication.Service
	Now           func() time.Time
}

func NewReconcileService(db *sql.DB) ReconcileService {
	return ReconcileService{DB: db, Communication: communication.NewService(db), Now: func() time.Time { return time.Now().UTC() }}
}

func (s ReconcileService) QueueNodes(ctx context.Context, req requestctx.Context, nodeRefs []string) ([]ReconcileQueueResult, error) {
	seen := map[string]bool{}
	results := []ReconcileQueueResult{}
	for _, ref := range nodeRefs {
		node, err := nodes.NewService(s.DB).GetNode(ctx, ref)
		if err != nil {
			return results, err
		}
		if seen[node.NodeID] {
			continue
		}
		seen[node.NodeID] = true
		var count int
		if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM box.watch_root_registrations WHERE node_id = $1 AND (source_kind = $2 OR area_key LIKE 'backup\_%' ESCAPE '\')`, node.NodeID, SourceKindBoxBackupContract).Scan(&count); err != nil {
			return results, err
		}
		if count == 0 {
			continue
		}
		result, err := s.queueNode(ctx, req, node, false)
		if err != nil {
			return results, err
		}
		results = append(results, result)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].NodeID < results[j].NodeID })
	return results, nil
}

func (s ReconcileService) RetryNode(ctx context.Context, req requestctx.Context, nodeRef string) (ReconcileQueueResult, error) {
	node, err := nodes.NewService(s.DB).GetNode(ctx, nodeRef)
	if err != nil {
		return ReconcileQueueResult{}, err
	}
	return s.queueNode(ctx, req, node, true)
}

func (s ReconcileService) queueNode(ctx context.Context, req requestctx.Context, node nodes.Node, force bool) (ReconcileQueueResult, error) {
	if node.Status != "active" || node.RetiredAt != nil {
		return ReconcileQueueResult{}, fmt.Errorf("node is not active: %s", node.Status)
	}
	roots, err := s.desiredRoots(ctx, node.NodeID)
	if err != nil {
		return ReconcileQueueResult{}, err
	}
	desiredHash, err := communication.ConfigHash(roots)
	if err != nil {
		return ReconcileQueueResult{}, err
	}
	previous, found, err := s.latestMessage(ctx, node.NodeID)
	if err != nil {
		return ReconcileQueueResult{}, err
	}
	revision := int64(1)
	if found {
		priorPayload, decodeErr := DecodeReconcilePayload(previous.PayloadJSON)
		if decodeErr != nil {
			return ReconcileQueueResult{}, decodeErr
		}
		revision = priorPayload.Evidence.DesiredRevision
		if priorPayload.Evidence.ConfigHash != desiredHash {
			revision++
		}
		if !force && priorPayload.Evidence.ConfigHash == desiredHash && activeOrCompletedMessage(previous.Status) {
			if previous.Status != communication.StatusAcked {
				if err := s.markQueued(ctx, node.NodeID, revision, previous.CommunicationMessageID); err != nil {
					return ReconcileQueueResult{}, err
				}
			}
			result := ReconcileQueueResult{NodeID: node.NodeID, DesiredRevision: revision, DesiredHash: desiredHash, Message: previous, Reused: true}
			if err := s.recordQueueEvent(ctx, req, result, force); err != nil {
				return ReconcileQueueResult{}, err
			}
			return result, nil
		}
	}
	payload := ProtectedFolderReconcilePayload{SchemaVersion: ProtectedFolderControlSchemaVersion, Evidence: communication.NewDesiredStateEvidence(node.NodeID, revision, desiredHash), Roots: roots}
	if err := payload.Validate(); err != nil {
		return ReconcileQueueResult{}, err
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return ReconcileQueueResult{}, err
	}
	now := s.now()
	expiresAt := now.Add(ReconcileMessageTTL)
	idempotencyKey := fmt.Sprintf("backup.reconcile.%s.%d.%s", node.NodeID, revision, strings.TrimPrefix(desiredHash, "sha256:")[:12])
	if force || (found && !activeOrCompletedMessage(previous.Status)) {
		idempotencyKey += ".attempt." + ids.NewCommunicationMessageID()
	}
	message, err := s.Communication.Enqueue(ctx, req, communication.EnqueueInput{NodeRef: node.NodeID, Kind: MessageKindProtectedFolderReconcile, PayloadJSON: payloadJSON, IdempotencyKey: idempotencyKey, ExpiresAt: &expiresAt, Metadata: json.RawMessage(`{"domain":"backup.protected_folder","operation":"reconcile"}`)})
	if err != nil {
		return ReconcileQueueResult{}, err
	}
	if err := s.markQueued(ctx, node.NodeID, revision, message.CommunicationMessageID); err != nil {
		return ReconcileQueueResult{}, err
	}
	result := ReconcileQueueResult{NodeID: node.NodeID, DesiredRevision: revision, DesiredHash: desiredHash, Message: message}
	if err := s.recordQueueEvent(ctx, req, result, force); err != nil {
		return ReconcileQueueResult{}, err
	}
	return result, nil
}

func (s ReconcileService) recordQueueEvent(ctx context.Context, req requestctx.Context, result ReconcileQueueResult, retry bool) error {
	eventType := events.TypeBackupDesiredQueued
	if retry {
		eventType = events.TypeBackupRetryRequested
	}
	_, err := events.NewService(s.DB).Append(ctx, events.AppendInput{EventType: eventType, EventLevel: "audit", Request: req, TargetKind: "node", TargetID: result.NodeID, Status: "queued", Payload: map[string]any{"desired_revision": result.DesiredRevision, "desired_hash": result.DesiredHash, "communication_message_id": result.Message.CommunicationMessageID, "reused": result.Reused}})
	return err
}

func (s ReconcileService) desiredRoots(ctx context.Context, nodeID string) ([]ProtectedFolderDesiredRoot, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT source_contract_key, backend_root_key, safe_root_key, display_name,
		       config_hash, config_json, activation_status, metadata
		FROM box.watch_root_registrations
		WHERE node_id = $1
		  AND (source_kind = $2 OR area_key LIKE 'backup\_%' ESCAPE '\')
		ORDER BY source_contract_key, backend_root_key
	`, nodeID, SourceKindBoxBackupContract)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	roots := []ProtectedFolderDesiredRoot{}
	for rows.Next() {
		var contractKey, rootKey, safeRootKey, displayName, configHash, status string
		var configJSON, metadataJSON []byte
		if err := rows.Scan(&contractKey, &rootKey, &safeRootKey, &displayName, &configHash, &configJSON, &status, &metadataJSON); err != nil {
			return nil, err
		}
		if status == "stale" || status == "disabled" {
			continue
		}
		var config agentwatchedroots.RootConfig
		if err := json.Unmarshal(configJSON, &config); err != nil {
			return nil, err
		}
		config = agentwatchedroots.NormalizeRootConfig(config)
		var metadata map[string]any
		if err := json.Unmarshal(metadataJSON, &metadata); err != nil {
			return nil, err
		}
		targetPath, _ := metadata["target_path"].(string)
		if strings.TrimSpace(contractKey) == "" {
			contractKey = strings.TrimPrefix(rootKey, "loom_box_backup__")
		}
		root := ProtectedFolderDesiredRoot{
			ContractKey: contractKey, RootKey: rootKey, SafeRootKey: safeRootKey,
			DisplayName: displayName, TargetPath: targetPath, Enabled: true,
			ConfigHash: configHash, ConfigJSON: json.RawMessage(configJSON),
			Backup:  BackupPolicy{Mode: config.BackupPolicy.Mode, MaxFileBytes: config.BackupPolicy.MaxFileBytes, MaxBatchBytes: config.BackupPolicy.MaxBatchBytes},
			Ignore:  IgnorePolicy{Profile: config.IgnorePolicy.Profile, DiscoverUserRules: config.IgnorePolicy.DiscoverUserRules},
			Include: append([]string{}, config.Include...), Exclude: append([]string{}, config.Exclude...),
		}
		roots = append(roots, root)
	}
	return roots, rows.Err()
}

func (s ReconcileService) latestMessage(ctx context.Context, nodeID string) (communication.Message, bool, error) {
	messages, err := s.Communication.ListMessages(ctx, communication.MessageFilter{NodeRef: nodeID, Kind: MessageKindProtectedFolderReconcile, Direction: communication.DirectionMainToNode, Limit: 1})
	if err != nil {
		return communication.Message{}, false, err
	}
	if len(messages) == 0 {
		return communication.Message{}, false, nil
	}
	return messages[0], true, nil
}

func (s ReconcileService) markQueued(ctx context.Context, nodeID string, revision int64, messageID string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE box.watch_root_registrations SET desired_revision = $2, reconciliation_message_id = $3, last_apply_error_code = '', last_apply_error_message = '', activation_status = CASE WHEN activation_status IN ('stale', 'disabled') THEN activation_status ELSE 'pending_agent_apply' END, updated_at = now() WHERE node_id = $1 AND (source_kind = $4 OR area_key LIKE 'backup\_%' ESCAPE '\') AND NOT (source_contract_deleted_at IS NOT NULL AND activation_status = 'disabled' AND applied_revision = desired_revision)`, nodeID, revision, messageID, SourceKindBoxBackupContract)
	return err
}

func (s ReconcileService) ProjectAcknowledgement(ctx context.Context, ack communication.AckResult) error {
	if ack.Message.Kind != MessageKindProtectedFolderReconcile {
		return nil
	}
	desired, err := DecodeReconcilePayload(ack.Message.PayloadJSON)
	if err != nil {
		return err
	}
	if ack.Message.NodeID != desired.Evidence.TargetNode {
		return errors.New("reconciliation acknowledgement target mismatch")
	}
	now := s.now()
	if ack.Ack.ProcessedAt != nil {
		now = ack.Ack.ProcessedAt.UTC()
	}
	if ack.Ack.AckStatus != communication.AckStatusCompleted {
		code, message := stableAckError(ack.Ack.ErrorJSON, ack.Ack.AckStatus)
		_, err := s.DB.ExecContext(ctx, `UPDATE box.watch_root_registrations SET last_node_ack_status = $3, last_node_acknowledged_at = $4, last_apply_error_code = $5, last_apply_error_message = $6, activation_status = CASE WHEN activation_status IN ('stale', 'disabled') THEN activation_status ELSE 'blocked' END, updated_at = now() WHERE node_id = $1 AND desired_revision = $2 AND reconciliation_message_id = $7 AND (source_kind = $8 OR area_key LIKE 'backup\_%' ESCAPE '\')`, ack.Message.NodeID, desired.Evidence.DesiredRevision, ack.Ack.AckStatus, now, code, message, ack.Message.CommunicationMessageID, SourceKindBoxBackupContract)
		return err
	}
	result, err := DecodeProtectedFolderAck(ack.Ack.ResultJSON)
	if err != nil {
		return err
	}
	rootAcks := map[string]ProtectedFolderRootAck{}
	for _, root := range result.Roots {
		rootAcks[root.ContractKey] = root
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT box_watch_root_registration_id, source_contract_key, activation_status, desired_config_hash FROM box.watch_root_registrations WHERE node_id = $1 AND desired_revision = $2 AND reconciliation_message_id = $3 AND (source_kind = $4 OR area_key LIKE 'backup\_%' ESCAPE '\')`, ack.Message.NodeID, desired.Evidence.DesiredRevision, ack.Message.CommunicationMessageID, SourceKindBoxBackupContract)
	if err != nil {
		return err
	}
	type registration struct{ id, key, status, hash string }
	registrations := []registration{}
	for rows.Next() {
		var item registration
		if err := rows.Scan(&item.id, &item.key, &item.status, &item.hash); err != nil {
			rows.Close()
			return err
		}
		registrations = append(registrations, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range registrations {
		rootAck, present := rootAcks[item.key]
		status := "blocked"
		appliedHash := ""
		errorCode, errorMessage := result.ErrorCode, result.ErrorMessage
		if item.status == "stale" || item.status == "disabled" {
			status = "disabled"
			if result.ErrorCode == "" {
				errorCode, errorMessage = "", ""
			}
		} else if present && rootAck.Applied && rootAck.ConfigHash == item.hash {
			status, appliedHash, errorCode, errorMessage = "applied", rootAck.ConfigHash, "", ""
		} else if present && rootAck.ErrorCode != "" {
			errorCode = rootAck.ErrorCode
		}
		if _, err := s.DB.ExecContext(ctx, `UPDATE box.watch_root_registrations SET applied_revision = $2, applied_config_hash = $3, activation_status = $4, last_node_ack_status = $5, last_node_acknowledged_at = $6, last_applied_at = CASE WHEN applied_revision = $2 AND applied_config_hash = $3 THEN last_applied_at ELSE $6 END, last_apply_error_code = $7, last_apply_error_message = $8, updated_at = now() WHERE box_watch_root_registration_id = $1`, item.id, result.Evidence.AppliedRevision, appliedHash, status, ack.Ack.AckStatus, now, errorCode, boundedError(errors.New(errorMessage))); err != nil {
			return err
		}
	}
	if len(registrations) > 0 {
		correlationID := ""
		if ack.Message.CorrelationID != nil {
			correlationID = *ack.Message.CorrelationID
		}
		req, err := requestctx.ResolveBootstrap(ctx, s.DB, correlationID)
		if err != nil {
			return err
		}
		if _, err := events.NewService(s.DB).Append(ctx, events.AppendInput{EventType: events.TypeBackupNodeApplied, EventLevel: "audit", Request: req, TargetKind: "node", TargetID: ack.Message.NodeID, Status: ack.Ack.AckStatus, Payload: map[string]any{"desired_revision": desired.Evidence.DesiredRevision, "applied_revision": result.Evidence.AppliedRevision, "communication_message_id": ack.Message.CommunicationMessageID}}); err != nil {
			return err
		}
	}
	return nil
}

func activeOrCompletedMessage(status string) bool {
	switch status {
	case communication.StatusPending, communication.StatusAvailable, communication.StatusClaimed, communication.StatusDelivered, communication.StatusAcked:
		return true
	default:
		return false
	}
}

func (s ReconcileService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
