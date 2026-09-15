package idempotency

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/ids"
)

const Header = "X-Loom-Idempotency-Key"

const (
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
)

const (
	DecisionNew             = "new"
	DecisionReplayCompleted = "replay_completed"
	DecisionReplayFailed    = "replay_failed"
	DecisionInProgress      = "in_progress"
	DecisionConflict        = "conflict"
	DecisionExpired         = "expired"
)

const defaultTTL = 24 * time.Hour

const (
	recoveryEvidenceSchemaVersion = "idempotency_recovery_evidence.v0.1"
	maxRecoveryEvidenceBytes      = 64 << 10
)

type Service struct {
	DB *sql.DB
}

func NewService(db *sql.DB) Service {
	return Service{DB: db}
}

type Record struct {
	IdempotencyID    string
	ScopeKind        string
	ScopeRef         string
	ActorID          string
	NodeID           string
	Key              string
	RequestHash      string
	Operation        string
	Status           string
	ResultKind       string
	ResultRef        string
	ResponseSnapshot json.RawMessage
	ErrorCode        string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	ExpiresAt        time.Time
}

type BeginInput struct {
	ScopeKind string
	ScopeRef  string
	ActorID   string
	NodeID    string
	Key       string
	Operation string
	Request   any
	TTL       time.Duration
	Metadata  json.RawMessage
}

type BeginResult struct {
	Record   Record
	Decision string
}

// RecoveryEvidenceInput identifies one already-begun request whose durable
// domain transaction can provide enough evidence to recover an interrupted
// HTTP completion. RequestHash must be the hash returned by RequestHash for
// the exact Begin request.
type RecoveryEvidenceInput struct {
	ActorID     string
	NodeID      string
	Key         string
	Operation   string
	RequestHash string
	Kind        string
	Payload     any
}

type recoveryEvidenceEnvelope struct {
	SchemaVersion string          `json:"schema_version"`
	Kind          string          `json:"kind"`
	Payload       json.RawMessage `json:"payload"`
}

func (s Service) Begin(ctx context.Context, input BeginInput) (BeginResult, error) {
	if s.DB == nil {
		return BeginResult{}, errors.New("idempotency database is not configured")
	}
	input = normalizeBeginInput(input)
	if input.Key == "" {
		return BeginResult{}, errors.New("idempotency key is required")
	}
	if input.Operation == "" {
		return BeginResult{}, errors.New("idempotency operation is required")
	}

	requestHash, err := RequestHash(map[string]any{
		"operation": input.Operation,
		"request":   input.Request,
	})
	if err != nil {
		return BeginResult{}, err
	}

	id := ids.NewIdempotencyID()
	expiresAt := time.Now().UTC().Add(input.TTL)
	row := s.DB.QueryRowContext(ctx, `
		INSERT INTO interface.idempotency_keys (
			idempotency_id,
			scope_kind,
			scope_ref,
			actor_id,
			node_id,
			key,
			request_hash,
			operation,
			status,
			expires_at,
			metadata
		)
		VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), $6, $7, $8, $9, $10, $11::jsonb)
		ON CONFLICT (scope_kind, scope_ref, key) DO NOTHING
		RETURNING
			idempotency_id,
			scope_kind,
			scope_ref,
			COALESCE(actor_id, ''),
			COALESCE(node_id, ''),
			key,
			request_hash,
			operation,
			status,
			COALESCE(result_kind, ''),
			COALESCE(result_ref, ''),
			response_snapshot,
			COALESCE(error_code, ''),
			created_at,
			updated_at,
			expires_at
	`, id, input.ScopeKind, input.ScopeRef, input.ActorID, input.NodeID, input.Key, requestHash, input.Operation, StatusInProgress, expiresAt, metadataJSON(input.Metadata))
	record, err := scanRecord(row)
	if err == nil {
		return BeginResult{Record: record, Decision: DecisionNew}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return BeginResult{}, err
	}

	record, err = s.find(ctx, input.ScopeKind, input.ScopeRef, input.Key)
	if err != nil {
		return BeginResult{}, err
	}
	if record.RequestHash != requestHash {
		return BeginResult{Record: record, Decision: DecisionConflict}, nil
	}
	if time.Now().UTC().After(record.ExpiresAt) {
		return BeginResult{Record: record, Decision: DecisionExpired}, nil
	}

	switch record.Status {
	case StatusCompleted:
		return BeginResult{Record: record, Decision: DecisionReplayCompleted}, nil
	case StatusFailed:
		return BeginResult{Record: record, Decision: DecisionReplayFailed}, nil
	default:
		return BeginResult{Record: record, Decision: DecisionInProgress}, nil
	}
}

func (s Service) Complete(ctx context.Context, id, resultKind, resultRef string, responseSnapshot any) error {
	if strings.TrimSpace(id) == "" || s.DB == nil {
		return nil
	}
	snapshot, err := json.Marshal(responseSnapshot)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `
		UPDATE interface.idempotency_keys
		SET
			status = $2,
			result_kind = NULLIF($3, ''),
			result_ref = NULLIF($4, ''),
			response_snapshot = $5::jsonb,
			error_code = NULL,
			updated_at = now()
		WHERE idempotency_id = $1
		  AND status IN ('in_progress', 'failed')
	`, id, StatusCompleted, resultKind, resultRef, string(snapshot))
	return err
}

func (s Service) Fail(ctx context.Context, id, errorCode string, responseSnapshot any) error {
	if strings.TrimSpace(id) == "" || s.DB == nil {
		return nil
	}
	var snapshot any = nil
	if responseSnapshot != nil {
		payload, err := json.Marshal(responseSnapshot)
		if err != nil {
			return err
		}
		snapshot = string(payload)
	}
	_, err := s.DB.ExecContext(ctx, `
		UPDATE interface.idempotency_keys
		SET
			status = $2,
			error_code = NULLIF($3, ''),
			response_snapshot = $4::jsonb,
			updated_at = now()
		WHERE idempotency_id = $1
		  AND status = 'in_progress'
		  AND NOT (metadata ? 'recovery_evidence')
	`, id, StatusFailed, errorCode, snapshot)
	return err
}

// StoreRecoveryEvidenceTx binds a bounded domain result to its own
// idempotency row in the same transaction as the domain mutation. A committed
// receipt reopens an earlier competing failure to in_progress so the ordinary
// HTTP completion path can finish it, and prevents a later failure from
// replacing the recoverable success evidence.
func StoreRecoveryEvidenceTx(ctx context.Context, tx *sql.Tx, input RecoveryEvidenceInput) (bool, error) {
	if tx == nil {
		return false, errors.New("idempotency recovery transaction is required")
	}
	input = normalizeRecoveryEvidenceInput(input)
	if err := validateRecoveryEvidenceInput(input); err != nil {
		return false, err
	}
	payload, err := json.Marshal(input.Payload)
	if err != nil {
		return false, fmt.Errorf("marshal idempotency recovery payload: %w", err)
	}
	envelope, err := json.Marshal(recoveryEvidenceEnvelope{
		SchemaVersion: recoveryEvidenceSchemaVersion,
		Kind:          input.Kind,
		Payload:       payload,
	})
	if err != nil {
		return false, fmt.Errorf("marshal idempotency recovery evidence: %w", err)
	}
	if len(envelope) > maxRecoveryEvidenceBytes {
		return false, fmt.Errorf("idempotency recovery evidence exceeds %d bytes", maxRecoveryEvidenceBytes)
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE interface.idempotency_keys
		SET metadata = jsonb_set(metadata, '{recovery_evidence}', $6::jsonb, true),
		    status = 'in_progress',
		    result_kind = NULL,
		    result_ref = NULL,
		    response_snapshot = NULL,
		    error_code = NULL,
		    updated_at = now()
		WHERE scope_kind = $1
		  AND scope_ref = $2
		  AND key = $3
		  AND operation = $4
		  AND request_hash = $5
		  AND status IN ('in_progress', 'failed')
	`, input.scopeKind(), input.scopeRef(), input.Key, input.Operation, input.RequestHash, string(envelope))
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows == 1, nil
}

// LoadRecoveryEvidenceTx returns the receipt for one exact request while
// locking its idempotency row in the caller's transaction. Missing rows or
// rows without evidence are not errors; malformed or mismatched evidence is.
func LoadRecoveryEvidenceTx(ctx context.Context, tx *sql.Tx, input RecoveryEvidenceInput, target any) (bool, error) {
	if tx == nil {
		return false, errors.New("idempotency recovery transaction is required")
	}
	input = normalizeRecoveryEvidenceInput(input)
	if err := validateRecoveryEvidenceInput(input); err != nil {
		return false, err
	}
	var raw []byte
	err := tx.QueryRowContext(ctx, `
		SELECT metadata->'recovery_evidence'
		FROM interface.idempotency_keys
		WHERE scope_kind = $1
		  AND scope_ref = $2
		  AND key = $3
		  AND operation = $4
		  AND request_hash = $5
		FOR UPDATE
	`, input.scopeKind(), input.scopeRef(), input.Key, input.Operation, input.RequestHash).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && len(raw) == 0) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if len(raw) > maxRecoveryEvidenceBytes {
		return false, fmt.Errorf("idempotency recovery evidence exceeds %d bytes", maxRecoveryEvidenceBytes)
	}
	var envelope recoveryEvidenceEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return false, fmt.Errorf("decode idempotency recovery evidence: %w", err)
	}
	if envelope.SchemaVersion != recoveryEvidenceSchemaVersion || envelope.Kind != input.Kind || len(envelope.Payload) == 0 {
		return false, errors.New("idempotency recovery evidence identity is invalid")
	}
	if target == nil {
		return false, errors.New("idempotency recovery target is required")
	}
	if err := json.Unmarshal(envelope.Payload, target); err != nil {
		return false, fmt.Errorf("decode idempotency recovery payload: %w", err)
	}
	return true, nil
}

func normalizeRecoveryEvidenceInput(input RecoveryEvidenceInput) RecoveryEvidenceInput {
	input.ActorID = strings.TrimSpace(input.ActorID)
	input.NodeID = strings.TrimSpace(input.NodeID)
	input.Key = strings.TrimSpace(input.Key)
	input.Operation = strings.TrimSpace(input.Operation)
	input.RequestHash = strings.TrimSpace(input.RequestHash)
	input.Kind = strings.TrimSpace(input.Kind)
	return input
}

func validateRecoveryEvidenceInput(input RecoveryEvidenceInput) error {
	if input.Key == "" || input.Operation == "" || input.RequestHash == "" || input.Kind == "" {
		return errors.New("idempotency recovery identity is incomplete")
	}
	if !strings.HasPrefix(input.RequestHash, "sha256:") || len(input.RequestHash) != len("sha256:")+sha256.Size*2 {
		return errors.New("idempotency recovery request hash is invalid")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(input.RequestHash, "sha256:")); err != nil {
		return errors.New("idempotency recovery request hash is invalid")
	}
	return nil
}

func (input RecoveryEvidenceInput) scopeKind() string {
	return normalizeBeginInput(BeginInput{ActorID: input.ActorID, NodeID: input.NodeID}).ScopeKind
}

func (input RecoveryEvidenceInput) scopeRef() string {
	return normalizeBeginInput(BeginInput{ActorID: input.ActorID, NodeID: input.NodeID}).ScopeRef
}

func (s Service) find(ctx context.Context, scopeKind, scopeRef, key string) (Record, error) {
	row := s.DB.QueryRowContext(ctx, `
		SELECT
			idempotency_id,
			scope_kind,
			scope_ref,
			COALESCE(actor_id, ''),
			COALESCE(node_id, ''),
			key,
			request_hash,
			operation,
			status,
			COALESCE(result_kind, ''),
			COALESCE(result_ref, ''),
			response_snapshot,
			COALESCE(error_code, ''),
			created_at,
			updated_at,
			expires_at
		FROM interface.idempotency_keys
		WHERE scope_kind = $1 AND scope_ref = $2 AND key = $3
	`, scopeKind, scopeRef, key)
	return scanRecord(row)
}

func RequestHash(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("canonicalize idempotency request: %w", err)
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func normalizeBeginInput(input BeginInput) BeginInput {
	input.ScopeKind = strings.TrimSpace(input.ScopeKind)
	input.ScopeRef = strings.TrimSpace(input.ScopeRef)
	input.ActorID = strings.TrimSpace(input.ActorID)
	input.NodeID = strings.TrimSpace(input.NodeID)
	input.Key = strings.TrimSpace(input.Key)
	input.Operation = strings.TrimSpace(input.Operation)
	if input.ScopeKind == "" {
		input.ScopeKind = "actor_node"
	}
	if input.ScopeRef == "" {
		switch {
		case input.ActorID != "" && input.NodeID != "":
			input.ScopeRef = input.ActorID + "@" + input.NodeID
		case input.ActorID != "":
			input.ScopeRef = input.ActorID
		case input.NodeID != "":
			input.ScopeRef = input.NodeID
		default:
			input.ScopeRef = "global"
		}
	}
	if input.TTL <= 0 {
		input.TTL = defaultTTL
	}
	return input
}

func metadataJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRecord(row rowScanner) (Record, error) {
	var record Record
	var snapshot []byte
	if err := row.Scan(
		&record.IdempotencyID,
		&record.ScopeKind,
		&record.ScopeRef,
		&record.ActorID,
		&record.NodeID,
		&record.Key,
		&record.RequestHash,
		&record.Operation,
		&record.Status,
		&record.ResultKind,
		&record.ResultRef,
		&snapshot,
		&record.ErrorCode,
		&record.CreatedAt,
		&record.UpdatedAt,
		&record.ExpiresAt,
	); err != nil {
		return Record{}, err
	}
	if len(snapshot) > 0 {
		record.ResponseSnapshot = append(json.RawMessage(nil), snapshot...)
	}
	return record, nil
}
