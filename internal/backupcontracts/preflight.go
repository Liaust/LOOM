package backupcontracts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/events"
	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/requestctx"
)

const (
	DefaultPreflightTTL     = 15 * time.Minute
	MaxPreflightResultBytes = 256 * 1024
)

type PreflightFilter struct {
	NodeRef string
	Status  string
	Limit   int
}

type PreflightService struct {
	DB            *sql.DB
	Communication communication.Service
	Now           func() time.Time
}

func NewPreflightService(db *sql.DB) PreflightService {
	return PreflightService{DB: db, Communication: communication.NewService(db), Now: func() time.Time { return time.Now().UTC() }}
}

func (s PreflightService) Create(ctx context.Context, req requestctx.Context, input PreflightCreateRequest) (PreflightRecord, error) {
	now := s.now()
	input.NodeRef = strings.TrimSpace(input.NodeRef)
	input.Path = strings.TrimSpace(input.Path)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if input.NodeRef == "" {
		return PreflightRecord{}, errors.New("node_ref is required")
	}
	if input.Path == "" || !filepath.IsAbs(input.Path) {
		return PreflightRecord{}, errors.New("path must be absolute")
	}
	input.Path = filepath.Clean(input.Path)
	if input.Path == string(filepath.Separator) {
		return PreflightRecord{}, errors.New("filesystem root cannot be preflighted")
	}
	ignore, err := normalizePreflightIgnore(input.Ignore)
	if err != nil {
		return PreflightRecord{}, err
	}
	budget, err := normalizePreflightBudget(input.Budget)
	if err != nil {
		return PreflightRecord{}, err
	}
	if input.Recheck != nil {
		if err := input.Recheck.Validate(); err != nil {
			return PreflightRecord{}, err
		}
	}
	node, err := nodes.NewService(s.DB).GetNode(ctx, input.NodeRef)
	if err != nil {
		return PreflightRecord{}, err
	}
	if node.Status != "active" {
		return PreflightRecord{}, fmt.Errorf("node is not active: %s", node.Status)
	}
	if input.IdempotencyKey != "" {
		if existing, found, err := s.findByIdempotency(ctx, node.NodeID, input.IdempotencyKey); err != nil {
			return PreflightRecord{}, err
		} else if found {
			if filepath.Clean(existing.RequestedPath) != input.Path {
				return PreflightRecord{}, errors.New("idempotency conflict for preflight key")
			}
			if existing.CommunicationMessageID == "" {
				return PreflightRecord{}, errors.New("idempotency conflict for incomplete preflight")
			}
			message, err := s.Communication.GetMessage(ctx, existing.CommunicationMessageID)
			if err != nil {
				return PreflightRecord{}, err
			}
			payload, err := DecodePreflightPayload(message.PayloadJSON)
			if err != nil {
				return PreflightRecord{}, err
			}
			if !sameRecheckIdentity(payload.Recheck, input.Recheck) {
				return PreflightRecord{}, errors.New("idempotency conflict for preflight recheck identity")
			}
			return existing, nil
		}
	}
	return s.createAttempt(ctx, req, node.NodeID, input.Path, input.Recheck, ignore, input.Include, input.Exclude, budget, input.IdempotencyKey, "", now)
}

func (s PreflightService) Retry(ctx context.Context, req requestctx.Context, preflightID string) (PreflightRecord, error) {
	previous, err := s.Get(ctx, preflightID)
	if err != nil {
		return PreflightRecord{}, err
	}
	if previous.CommunicationMessageID == "" {
		return PreflightRecord{}, errors.New("preflight has no durable communication message")
	}
	message, err := s.Communication.GetMessage(ctx, previous.CommunicationMessageID)
	if err != nil {
		return PreflightRecord{}, err
	}
	payload, err := DecodePreflightPayload(message.PayloadJSON)
	if err != nil {
		return PreflightRecord{}, fmt.Errorf("decode prior preflight request: %w", err)
	}
	now := s.now()
	return s.createAttempt(ctx, req, previous.TargetNodeID, previous.RequestedPath, payload.Recheck, payload.Ignore, payload.Include, payload.Exclude, payload.Budget, "retry."+preflightID+"."+ids.NewBackupPreflightID(), preflightID, now)
}

func (s PreflightService) createAttempt(ctx context.Context, req requestctx.Context, nodeID, requestedPath string, recheck *ProtectedFolderRecheckIdentity, ignore IgnorePolicy, include, exclude []string, budget PreflightBudget, idempotencyKey, retryOf string, now time.Time) (PreflightRecord, error) {
	preflightID := ids.NewBackupPreflightID()
	expiresAt := now.Add(DefaultPreflightTTL)
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO backup.protected_folder_preflights (
			protected_folder_preflight_id, target_node_id, requested_path, status,
			request_schema_version, expires_at, requested_by_actor_id, correlation_id,
			idempotency_key, retry_of_preflight_id, metadata
		) VALUES ($1, $2, $3, 'pending', $4, $5, nullif($6, ''), $7, $8, nullif($9, ''), '{}'::jsonb)
	`, preflightID, nodeID, requestedPath, ProtectedFolderControlSchemaVersion, expiresAt, req.ActorID, req.CorrelationID, idempotencyKey, retryOf)
	if err != nil {
		return PreflightRecord{}, err
	}
	payload := ProtectedFolderPreflightPayload{
		SchemaVersion: ProtectedFolderControlSchemaVersion,
		PreflightID:   preflightID,
		TargetNode:    nodeID,
		RequestedPath: requestedPath,
		Recheck:       recheck,
		Ignore:        ignore,
		Include:       append([]string{}, include...),
		Exclude:       append([]string{}, exclude...),
		Budget:        budget,
		ExpiresAt:     expiresAt,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return PreflightRecord{}, err
	}
	messageKey := idempotencyKey
	if messageKey == "" {
		messageKey = "backup.preflight." + preflightID
	}
	message, err := s.Communication.Enqueue(ctx, req, communication.EnqueueInput{
		NodeRef:        nodeID,
		Kind:           MessageKindProtectedFolderPreflight,
		PayloadJSON:    payloadJSON,
		IdempotencyKey: messageKey,
		ExpiresAt:      &expiresAt,
		Metadata:       json.RawMessage(`{"domain":"backup.protected_folder","operation":"preflight"}`),
	})
	if err != nil {
		_, _ = s.DB.ExecContext(ctx, `UPDATE backup.protected_folder_preflights SET status = 'failed', error_code = 'preflight.enqueue_failed', error_message = $2, completed_at = now(), updated_at = now() WHERE protected_folder_preflight_id = $1`, preflightID, boundedError(err))
		return PreflightRecord{}, err
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE backup.protected_folder_preflights SET communication_message_id = $2, updated_at = now() WHERE protected_folder_preflight_id = $1`, preflightID, message.CommunicationMessageID); err != nil {
		return PreflightRecord{}, err
	}
	if _, err := events.NewService(s.DB).Append(ctx, events.AppendInput{EventType: events.TypeBackupPreflightRequested, EventLevel: "audit", Request: req, TargetKind: "backup_preflight", TargetID: preflightID, Status: PreflightStatusPending, Payload: map[string]any{"node_id": nodeID, "requested_path": requestedPath, "communication_message_id": message.CommunicationMessageID}}); err != nil {
		return PreflightRecord{}, err
	}
	return s.Get(ctx, preflightID)
}

func sameRecheckIdentity(left, right *ProtectedFolderRecheckIdentity) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (s PreflightService) Get(ctx context.Context, preflightID string) (PreflightRecord, error) {
	preflightID = strings.TrimSpace(preflightID)
	if preflightID == "" {
		return PreflightRecord{}, errors.New("preflight id is required")
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE backup.protected_folder_preflights SET status = 'expired', error_code = 'preflight.expired', error_message = 'The owner node did not complete preflight before expiry.', completed_at = COALESCE(completed_at, now()), updated_at = now() WHERE protected_folder_preflight_id = $1 AND status = 'pending' AND expires_at <= now()`, preflightID); err != nil {
		return PreflightRecord{}, err
	}
	return scanPreflight(s.DB.QueryRowContext(ctx, `SELECT `+preflightColumns()+` FROM backup.protected_folder_preflights WHERE protected_folder_preflight_id = $1`, preflightID))
}

func (s PreflightService) List(ctx context.Context, filter PreflightFilter) ([]PreflightRecord, error) {
	if _, err := s.DB.ExecContext(ctx, `UPDATE backup.protected_folder_preflights SET status = 'expired', error_code = 'preflight.expired', error_message = 'The owner node did not complete preflight before expiry.', completed_at = COALESCE(completed_at, now()), updated_at = now() WHERE status = 'pending' AND expires_at <= now()`); err != nil {
		return nil, err
	}
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	query := `SELECT ` + preflightColumns() + ` FROM backup.protected_folder_preflights WHERE true`
	args := []any{}
	if strings.TrimSpace(filter.NodeRef) != "" {
		node, err := nodes.NewService(s.DB).GetNode(ctx, filter.NodeRef)
		if err != nil {
			return nil, err
		}
		args = append(args, node.NodeID)
		query += fmt.Sprintf(" AND target_node_id = $%d", len(args))
	}
	if strings.TrimSpace(filter.Status) != "" {
		args = append(args, strings.TrimSpace(filter.Status))
		query += fmt.Sprintf(" AND status = $%d", len(args))
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []PreflightRecord{}
	for rows.Next() {
		record, err := scanPreflight(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

// ProjectAcknowledgement materializes the domain view after the generic
// communication acknowledgement has committed. It is intentionally safe to
// retry and does not mutate acknowledgement truth.
func (s PreflightService) ProjectAcknowledgement(ctx context.Context, ack communication.AckResult) error {
	if ack.Message.Kind != MessageKindProtectedFolderPreflight {
		return nil
	}
	payload, err := DecodePreflightPayload(ack.Message.PayloadJSON)
	if err != nil {
		return err
	}
	if ack.Message.CommunicationMessageID == "" || payload.PreflightID == "" {
		return errors.New("preflight acknowledgement is missing durable identity")
	}
	now := s.now()
	if ack.Ack.AckStatus == communication.AckStatusCompleted {
		if len(ack.Ack.ResultJSON) > MaxPreflightResultBytes {
			return errors.New("preflight acknowledgement result exceeds bounded size")
		}
		var result PreflightResult
		if err := communication.DecodeStrictJSONObject(ack.Ack.ResultJSON, &result); err != nil {
			return err
		}
		if err := validatePreflightResult(result); err != nil {
			return err
		}
		resultJSON, _ := json.Marshal(result)
		completedAt := now
		if ack.Ack.ProcessedAt != nil {
			completedAt = ack.Ack.ProcessedAt.UTC()
		}
		outcome, err := s.DB.ExecContext(ctx, `UPDATE backup.protected_folder_preflights SET status = 'completed', canonical_path = $3, response_schema_version = $4, result_json = $5, error_code = '', error_message = '', completed_at = $6, updated_at = now() WHERE protected_folder_preflight_id = $1 AND communication_message_id = $2 AND status = 'pending'`, payload.PreflightID, ack.Message.CommunicationMessageID, result.CanonicalPath, result.SchemaVersion, resultJSON, completedAt)
		if err != nil {
			return err
		}
		changed, _ := outcome.RowsAffected()
		if err := s.requireProjectionTarget(ctx, outcome, payload.PreflightID, PreflightStatusCompleted); err != nil {
			return err
		}
		if changed > 0 {
			return s.recordCompletionEvent(ctx, ack, payload.PreflightID, PreflightStatusCompleted)
		}
		return nil
	}
	code, message := stableAckError(ack.Ack.ErrorJSON, ack.Ack.AckStatus)
	outcome, err := s.DB.ExecContext(ctx, `UPDATE backup.protected_folder_preflights SET status = 'failed', error_code = $3, error_message = $4, completed_at = $5, updated_at = now() WHERE protected_folder_preflight_id = $1 AND communication_message_id = $2 AND status = 'pending'`, payload.PreflightID, ack.Message.CommunicationMessageID, code, message, now)
	if err != nil {
		return err
	}
	changed, _ := outcome.RowsAffected()
	if err := s.requireProjectionTarget(ctx, outcome, payload.PreflightID, PreflightStatusFailed); err != nil {
		return err
	}
	if changed > 0 {
		return s.recordCompletionEvent(ctx, ack, payload.PreflightID, PreflightStatusFailed)
	}
	return nil
}

func (s PreflightService) recordCompletionEvent(ctx context.Context, ack communication.AckResult, preflightID, status string) error {
	correlationID := ""
	if ack.Message.CorrelationID != nil {
		correlationID = *ack.Message.CorrelationID
	}
	req, err := requestctx.ResolveBootstrap(ctx, s.DB, correlationID)
	if err != nil {
		return err
	}
	_, err = events.NewService(s.DB).Append(ctx, events.AppendInput{EventType: events.TypeBackupPreflightCompleted, EventLevel: "audit", Request: req, TargetKind: "backup_preflight", TargetID: preflightID, Status: status, Payload: map[string]any{"node_id": ack.Message.NodeID, "communication_message_id": ack.Message.CommunicationMessageID, "ack_status": ack.Ack.AckStatus}})
	return err
}

func (s PreflightService) ValidateForContract(ctx context.Context, preflightID string, contract Contract) error {
	record, err := s.Get(ctx, preflightID)
	if err != nil {
		return err
	}
	if record.Status != PreflightStatusCompleted || record.Result == nil {
		return fmt.Errorf("preflight %s is not completed", preflightID)
	}
	node, err := nodes.NewService(s.DB).GetNode(ctx, contract.OwnerNode)
	if err != nil {
		return err
	}
	if record.TargetNodeID != node.NodeID || filepath.Clean(record.RequestedPath) != filepath.Clean(contract.Target.Path) {
		return errors.New("preflight does not match the contract owner node and target path")
	}
	for _, finding := range record.Result.Findings {
		if finding.Blocking {
			return fmt.Errorf("preflight has blocking finding %s", finding.Code)
		}
	}
	return nil
}

func normalizePreflightIgnore(input *IgnorePolicy) (IgnorePolicy, error) {
	result := IgnorePolicy{Profile: string(filepolicy.ProfileManaged), DiscoverUserRules: true}
	if input != nil {
		result = *input
		if strings.TrimSpace(result.Profile) == "" {
			result.Profile = string(filepolicy.ProfileManaged)
		}
	}
	profile, err := filepolicy.ParseProfile(result.Profile)
	if err != nil {
		return IgnorePolicy{}, err
	}
	result.Profile = string(profile)
	return result, nil
}

func normalizePreflightBudget(input PreflightBudget) (PreflightBudget, error) {
	defaults := DefaultPreflightBudget()
	if input.MaxEntries == 0 {
		input.MaxEntries = defaults.MaxEntries
	}
	if input.MaxApparentBytes == 0 {
		input.MaxApparentBytes = defaults.MaxApparentBytes
	}
	if input.MaxDurationMillis == 0 {
		input.MaxDurationMillis = defaults.MaxDurationMillis
	}
	test := ProtectedFolderPreflightPayload{SchemaVersion: ProtectedFolderControlSchemaVersion, PreflightID: "validation", TargetNode: "validation", RequestedPath: "/validation", Budget: input, ExpiresAt: time.Now().Add(time.Minute)}
	if err := test.Validate(); err != nil {
		return PreflightBudget{}, err
	}
	return input, nil
}

func validatePreflightResult(result PreflightResult) error {
	if result.SchemaVersion != ProtectedFolderPreflightResultVersion {
		return fmt.Errorf("unsupported preflight result schema %q", result.SchemaVersion)
	}
	if result.RequestedPath == "" || result.Budget.MaxEntries <= 0 {
		return errors.New("preflight result is missing required bounded evidence")
	}
	if len(result.Findings) > 200 {
		return errors.New("preflight result contains too many findings")
	}
	return nil
}

func (s PreflightService) findByIdempotency(ctx context.Context, nodeID, key string) (PreflightRecord, bool, error) {
	record, err := scanPreflight(s.DB.QueryRowContext(ctx, `SELECT `+preflightColumns()+` FROM backup.protected_folder_preflights WHERE target_node_id = $1 AND idempotency_key = $2`, nodeID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return PreflightRecord{}, false, nil
	}
	return record, err == nil, err
}

type preflightScanner interface{ Scan(...any) error }

func scanPreflight(scanner preflightScanner) (PreflightRecord, error) {
	var result PreflightRecord
	var resultJSON []byte
	var messageID, canonicalPath, responseVersion, errorCode, errorMessage, retryOf sql.NullString
	var completedAt sql.NullTime
	if err := scanner.Scan(&result.PreflightID, &result.TargetNodeID, &result.RequestedPath, &canonicalPath, &result.Status, &result.RequestSchemaVersion, &responseVersion, &messageID, &resultJSON, &errorCode, &errorMessage, &result.ExpiresAt, &retryOf, &result.CreatedAt, &completedAt); err != nil {
		return PreflightRecord{}, err
	}
	result.CanonicalPath = nullableString(canonicalPath)
	result.ResponseSchemaVersion = nullableString(responseVersion)
	result.CommunicationMessageID = nullableString(messageID)
	result.ErrorCode = nullableString(errorCode)
	result.ErrorMessage = nullableString(errorMessage)
	result.RetryOfPreflightID = nullableString(retryOf)
	if completedAt.Valid {
		result.CompletedAt = &completedAt.Time
	}
	if len(resultJSON) > 0 && string(resultJSON) != "{}" {
		var value PreflightResult
		if err := json.Unmarshal(resultJSON, &value); err != nil {
			return PreflightRecord{}, err
		}
		result.Result = &value
	}
	return result, nil
}

func preflightColumns() string {
	return `protected_folder_preflight_id, target_node_id, requested_path, nullif(canonical_path, ''), status, request_schema_version, nullif(response_schema_version, ''), communication_message_id, result_json, nullif(error_code, ''), nullif(error_message, ''), expires_at, retry_of_preflight_id, created_at, completed_at`
}

func nullableString(value sql.NullString) string {
	if value.Valid {
		return value.String
	}
	return ""
}
func (s PreflightService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func boundedError(err error) string {
	value := strings.TrimSpace(err.Error())
	if len(value) > 2048 {
		return value[:2048]
	}
	return value
}

func stableAckError(raw json.RawMessage, status string) (string, string) {
	var value struct {
		Code    string `json:"code"`
		Summary string `json:"summary"`
	}
	_ = json.Unmarshal(raw, &value)
	if strings.TrimSpace(value.Code) == "" {
		value.Code = "preflight." + strings.TrimSpace(status)
	}
	if strings.TrimSpace(value.Summary) == "" {
		value.Summary = "The owner node could not complete folder preflight."
	}
	return value.Code, boundedError(errors.New(value.Summary))
}

func (s PreflightService) requireProjectionTarget(ctx context.Context, result sql.Result, preflightID, expectedStatus string) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		record, getErr := s.Get(ctx, preflightID)
		if getErr == nil && record.Status == expectedStatus {
			return nil
		}
		return errors.New("preflight acknowledgement has no pending projection target")
	}
	return nil
}
