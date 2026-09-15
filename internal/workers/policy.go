package workers

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
	"loom.local/loom/internal/idempotency"
	"loom.local/loom/internal/requestctx"
)

const workerPolicyMetadataSchema = "worker_policy_control.v0.1"

const (
	workerPolicyIdempotencyOperation = "worker.policy.set"
	workerPolicyRecoveryKind         = "worker_policy_result"
)

const (
	maxWorkerPolicyReasonBytes         = 1024
	maxWorkerPolicyIdempotencyKeyBytes = 256
)

type workerPolicyMutationRecord struct {
	SchemaVersion      string            `json:"schema_version"`
	IdempotencyKeyHash string            `json:"idempotency_key_hash"`
	RequestFingerprint string            `json:"request_fingerprint"`
	ChangedAt          time.Time         `json:"changed_at"`
	ChangedByActorID   string            `json:"changed_by_actor_id"`
	Reason             string            `json:"reason"`
	CorrelationID      string            `json:"correlation_id"`
	Old                WorkerPolicyState `json:"old"`
	New                WorkerPolicyState `json:"new"`
}

func (s Service) InspectWorkerPolicy(ctx context.Context, workerRef string) (WorkerPolicyState, error) {
	if s.DB == nil {
		return WorkerPolicyState{}, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	instance, err := resolveWorkerInstance(ctx, s.DB, workerRef)
	if err != nil {
		return WorkerPolicyState{}, err
	}
	return workerPolicyState(instance, time.Now().UTC())
}

func (s Service) SetWorkerPolicy(ctx context.Context, req requestctx.Context, workerRef string, input SetWorkerPolicyInput) (SetWorkerPolicyResult, error) {
	if s.DB == nil {
		return SetWorkerPolicyResult{}, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	input, desired, desiredJSON, err := normalizeSetWorkerPolicyInput(input)
	if err != nil {
		return SetWorkerPolicyResult{}, err
	}

	resolved, err := resolveWorkerInstance(ctx, s.DB, workerRef)
	if err != nil {
		return SetWorkerPolicyResult{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return SetWorkerPolicyResult{}, err
	}
	defer tx.Rollback()

	instance, err := lockWorkerInstance(ctx, tx, resolved.WorkerInstanceID)
	if err != nil {
		return SetWorkerPolicyResult{}, err
	}
	if instance.Locality != LocalityMainOwned {
		return SetWorkerPolicyResult{}, fmt.Errorf("%w: worker %s locality %s is not supported by policy control", ErrInvalid, instance.WorkerKey, instance.Locality)
	}
	if err := validateManualOnlyWorkerPolicy(instance.WorkerKind, desiredJSON); err != nil {
		return SetWorkerPolicyResult{}, err
	}

	now := time.Now().UTC()
	oldState, err := workerPolicyState(instance, now)
	if err != nil {
		return SetWorkerPolicyResult{}, err
	}
	desiredFingerprint, err := TickPolicyFingerprint(desiredJSON)
	if err != nil {
		return SetWorkerPolicyResult{}, err
	}
	requestFingerprint, err := workerPolicyRequestFingerprint(input, desiredFingerprint)
	if err != nil {
		return SetWorkerPolicyResult{}, err
	}
	recoveryInput, err := workerPolicyRecoveryEvidenceInput(req, workerRef, input)
	if err != nil {
		return SetWorkerPolicyResult{}, err
	}
	if input.Confirm {
		var record workerPolicyMutationRecord
		found, err := idempotency.LoadRecoveryEvidenceTx(ctx, tx, recoveryInput, &record)
		if err != nil {
			return SetWorkerPolicyResult{}, err
		}
		if found {
			replay, err := replayWorkerPolicyRecord(instance, input, requestFingerprint, record)
			if err != nil {
				return SetWorkerPolicyResult{}, err
			}
			if err := tx.Commit(); err != nil {
				return SetWorkerPolicyResult{}, err
			}
			return replay, nil
		}
	}
	if input.ExpectedPolicyFingerprint != oldState.PolicyFingerprint {
		if replay, ok := replayWorkerPolicyMutation(instance, input, requestFingerprint, oldState); ok {
			if err := tx.Commit(); err != nil {
				return SetWorkerPolicyResult{}, err
			}
			return replay, nil
		}
		return SetWorkerPolicyResult{}, fmt.Errorf("%w: worker %s policy fingerprint changed: expected %s, found %s", ErrConflict, instance.WorkerKey, input.ExpectedPolicyFingerprint, oldState.PolicyFingerprint)
	}
	if !instance.Enabled || instance.LifecycleStatus == LifecycleDisabled || instance.LifecycleStatus == LifecycleRetired {
		return SetWorkerPolicyResult{}, fmt.Errorf("%w: worker %s is disabled", ErrConflict, instance.WorkerKey)
	}
	if instance.CurrentRunID != nil {
		return SetWorkerPolicyResult{}, fmt.Errorf("%w: worker %s has active run %s", ErrConflict, instance.WorkerKey, *instance.CurrentRunID)
	}
	activeLease, err := hasActiveWorkerLease(ctx, tx, instance.WorkerInstanceID)
	if err != nil {
		return SetWorkerPolicyResult{}, err
	}
	if activeLease {
		return SetWorkerPolicyResult{}, fmt.Errorf("%w: worker %s has an active lease", ErrConflict, instance.WorkerKey)
	}
	nextRunAfter := oldState.NextRunAfter
	if desiredFingerprint != oldState.PolicyFingerprint {
		nextRunAfter = desired.NextAfter(now)
	} else if desired.Mode == TickModeManual {
		nextRunAfter = nil
	}
	updated := instance
	updated.TickPolicyJSON = desiredJSON
	updated.NextRunAfter = nextRunAfter
	newState, err := workerPolicyState(updated, now)
	if err != nil {
		return SetWorkerPolicyResult{}, err
	}
	changed := oldState.PolicyFingerprint != newState.PolicyFingerprint || !equalOptionalTimes(oldState.NextRunAfter, newState.NextRunAfter)
	result := SetWorkerPolicyResult{
		WorkerInstanceID: instance.WorkerInstanceID,
		WorkerKey:        instance.WorkerKey,
		DryRun:           input.DryRun,
		Applied:          !input.DryRun,
		Changed:          changed,
		Reason:           input.Reason,
		Old:              oldState,
		New:              newState,
		NextRunEvidence: WorkerPolicyNextRunEvidence{
			DerivedAt:       now,
			Previous:        oldState.NextRunAfter,
			Proposed:        newState.NextRunAfter,
			DerivationBasis: policyNextRunBasis(oldState, newState),
		},
	}
	if input.DryRun {
		return result, nil
	}

	record := workerPolicyMutationRecord{
		SchemaVersion:      workerPolicyMetadataSchema,
		IdempotencyKeyHash: workerPolicyIdempotencyKeyHash(input.IdempotencyKey),
		RequestFingerprint: requestFingerprint,
		ChangedAt:          now,
		ChangedByActorID:   req.ActorID,
		Reason:             input.Reason,
		CorrelationID:      req.CorrelationID,
		Old:                oldState,
		New:                newState,
	}
	marker, err := json.Marshal(record)
	if err != nil {
		return SetWorkerPolicyResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE workers.worker_instances
		SET tick_policy_json = $2::jsonb,
		    next_run_after = $3,
		    metadata = CASE
		        WHEN $6 THEN jsonb_set(metadata, '{policy_control}', $4::jsonb, true)
		        WHEN metadata ? 'policy_control' THEN jsonb_set(metadata, '{policy_control_confirmation}', $4::jsonb, true)
		        ELSE jsonb_set(jsonb_set(metadata, '{policy_control}', $4::jsonb, true), '{policy_control_confirmation}', $4::jsonb, true)
		    END,
		    updated_at = $5
		WHERE worker_instance_id = $1
	`, instance.WorkerInstanceID, []byte(desiredJSON), nextRunAfter, marker, now, changed); err != nil {
		return SetWorkerPolicyResult{}, err
	}
	eventStatus := "policy_updated"
	if !changed {
		eventStatus = "policy_confirmed"
	}
	if err := appendWorkerEvent(ctx, tx, req, events.TypeWorkerInstanceUpdated, "worker_instance", instance.WorkerInstanceID, eventStatus, map[string]any{
		"actor_id":               req.ActorID,
		"actor_key":              req.ActorKey,
		"worker_instance_id":     instance.WorkerInstanceID,
		"worker_key":             instance.WorkerKey,
		"old_policy":             oldState.Policy,
		"new_policy":             newState.Policy,
		"old_policy_fingerprint": oldState.PolicyFingerprint,
		"new_policy_fingerprint": newState.PolicyFingerprint,
		"old_next_run_after":     oldState.NextRunAfter,
		"new_next_run_after":     newState.NextRunAfter,
		"reason":                 input.Reason,
		"correlation_id":         req.CorrelationID,
		"changed":                changed,
	}); err != nil {
		return SetWorkerPolicyResult{}, err
	}
	result.EventType = events.TypeWorkerInstanceUpdated
	recoveryInput.Payload = record
	if _, err := idempotency.StoreRecoveryEvidenceTx(ctx, tx, recoveryInput); err != nil {
		return SetWorkerPolicyResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return SetWorkerPolicyResult{}, err
	}
	return result, nil
}

func workerPolicyRecoveryEvidenceInput(req requestctx.Context, workerRef string, input SetWorkerPolicyInput) (idempotency.RecoveryEvidenceInput, error) {
	requestHash, err := idempotency.RequestHash(map[string]any{
		"operation": workerPolicyIdempotencyOperation,
		"request": map[string]any{
			"worker_ref": workerRef,
			"input":      input,
		},
	})
	if err != nil {
		return idempotency.RecoveryEvidenceInput{}, err
	}
	return idempotency.RecoveryEvidenceInput{
		ActorID:     req.ActorID,
		NodeID:      req.OriginNodeID,
		Key:         input.IdempotencyKey,
		Operation:   workerPolicyIdempotencyOperation,
		RequestHash: requestHash,
		Kind:        workerPolicyRecoveryKind,
	}, nil
}

func workerPolicyRequestFingerprint(input SetWorkerPolicyInput, desiredFingerprint string) (string, error) {
	payload, err := json.Marshal(struct {
		Expected string `json:"expected"`
		Desired  string `json:"desired"`
		Reason   string `json:"reason"`
	}{input.ExpectedPolicyFingerprint, desiredFingerprint, input.Reason})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("sha256:%x", sum[:]), nil
}

func workerPolicyIdempotencyKeyHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("sha256:%x", sum[:])
}

func replayWorkerPolicyMutation(instance WorkerInstance, input SetWorkerPolicyInput, requestFingerprint string, current WorkerPolicyState) (SetWorkerPolicyResult, bool) {
	if input.DryRun || input.IdempotencyKey == "" {
		return SetWorkerPolicyResult{}, false
	}
	var metadata struct {
		PolicyControl             workerPolicyMutationRecord `json:"policy_control"`
		PolicyControlConfirmation workerPolicyMutationRecord `json:"policy_control_confirmation"`
	}
	if err := json.Unmarshal(instance.Metadata, &metadata); err != nil {
		return SetWorkerPolicyResult{}, false
	}
	for _, record := range []workerPolicyMutationRecord{metadata.PolicyControl, metadata.PolicyControlConfirmation} {
		if record.SchemaVersion != workerPolicyMetadataSchema || record.IdempotencyKeyHash != workerPolicyIdempotencyKeyHash(input.IdempotencyKey) || record.RequestFingerprint != requestFingerprint || record.New.PolicyFingerprint != current.PolicyFingerprint {
			continue
		}
		replay, err := replayWorkerPolicyRecord(instance, input, requestFingerprint, record)
		return replay, err == nil
	}
	return SetWorkerPolicyResult{}, false
}

func replayWorkerPolicyRecord(instance WorkerInstance, input SetWorkerPolicyInput, requestFingerprint string, record workerPolicyMutationRecord) (SetWorkerPolicyResult, error) {
	if record.SchemaVersion != workerPolicyMetadataSchema ||
		record.IdempotencyKeyHash != workerPolicyIdempotencyKeyHash(input.IdempotencyKey) ||
		record.RequestFingerprint != requestFingerprint ||
		record.Old.WorkerInstanceID != instance.WorkerInstanceID ||
		record.New.WorkerInstanceID != instance.WorkerInstanceID ||
		record.Old.WorkerKey != instance.WorkerKey ||
		record.New.WorkerKey != instance.WorkerKey {
		return SetWorkerPolicyResult{}, fmt.Errorf("%w: worker policy recovery evidence does not match the request", ErrConflict)
	}
	return SetWorkerPolicyResult{
		WorkerInstanceID: instance.WorkerInstanceID,
		WorkerKey:        instance.WorkerKey,
		Applied:          true,
		Changed:          record.Old.PolicyFingerprint != record.New.PolicyFingerprint || !equalOptionalTimes(record.Old.NextRunAfter, record.New.NextRunAfter),
		IdempotentReplay: true,
		Reason:           record.Reason,
		Old:              record.Old,
		New:              record.New,
		NextRunEvidence: WorkerPolicyNextRunEvidence{
			DerivedAt:       record.ChangedAt,
			Previous:        record.Old.NextRunAfter,
			Proposed:        record.New.NextRunAfter,
			DerivationBasis: policyNextRunBasis(record.Old, record.New),
		},
		EventType: events.TypeWorkerInstanceUpdated,
	}, nil
}

func normalizeSetWorkerPolicyInput(input SetWorkerPolicyInput) (SetWorkerPolicyInput, TickPolicy, json.RawMessage, error) {
	input.ExpectedPolicyFingerprint = strings.TrimSpace(input.ExpectedPolicyFingerprint)
	input.Reason = strings.TrimSpace(input.Reason)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if input.ExpectedPolicyFingerprint == "" {
		return SetWorkerPolicyInput{}, TickPolicy{}, nil, fmt.Errorf("%w: expected_policy_fingerprint is required", ErrInvalid)
	}
	if err := validateWorkerPolicyFingerprint(input.ExpectedPolicyFingerprint); err != nil {
		return SetWorkerPolicyInput{}, TickPolicy{}, nil, err
	}
	input.ExpectedPolicyFingerprint = "sha256:" + strings.ToLower(strings.TrimPrefix(input.ExpectedPolicyFingerprint, "sha256:"))
	if len(input.Reason) > maxWorkerPolicyReasonBytes {
		return SetWorkerPolicyInput{}, TickPolicy{}, nil, fmt.Errorf("%w: reason exceeds %d bytes", ErrInvalid, maxWorkerPolicyReasonBytes)
	}
	if len(input.IdempotencyKey) > maxWorkerPolicyIdempotencyKeyBytes {
		return SetWorkerPolicyInput{}, TickPolicy{}, nil, fmt.Errorf("%w: idempotency key exceeds %d bytes", ErrInvalid, maxWorkerPolicyIdempotencyKeyBytes)
	}
	if input.DryRun == input.Confirm {
		return SetWorkerPolicyInput{}, TickPolicy{}, nil, fmt.Errorf("%w: choose exactly one of dry_run or confirm", ErrInvalid)
	}
	if input.Confirm {
		if input.Reason == "" {
			return SetWorkerPolicyInput{}, TickPolicy{}, nil, fmt.Errorf("%w: reason is required when applying worker policy", ErrInvalid)
		}
		if input.IdempotencyKey == "" {
			return SetWorkerPolicyInput{}, TickPolicy{}, nil, fmt.Errorf("%w: idempotency key is required when applying worker policy", ErrInvalid)
		}
	}
	normalized, raw, err := NormalizeTickPolicy(input.Policy)
	if err != nil {
		return SetWorkerPolicyInput{}, TickPolicy{}, nil, err
	}
	input.Policy = normalized
	return input, normalized, raw, nil
}

func ValidateSetWorkerPolicyInput(input SetWorkerPolicyInput) error {
	_, _, _, err := normalizeSetWorkerPolicyInput(input)
	return err
}

func NormalizeSetWorkerPolicyInput(input SetWorkerPolicyInput) (SetWorkerPolicyInput, error) {
	normalized, _, _, err := normalizeSetWorkerPolicyInput(input)
	return normalized, err
}

func validateWorkerPolicyFingerprint(value string) error {
	hexValue := strings.TrimPrefix(value, "sha256:")
	if len(hexValue) != sha256.Size*2 || len(hexValue) == len(value) {
		return fmt.Errorf("%w: expected_policy_fingerprint must be a sha256 URI", ErrInvalid)
	}
	decoded, err := hex.DecodeString(hexValue)
	if err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("%w: expected_policy_fingerprint must be a sha256 URI", ErrInvalid)
	}
	return nil
}

func workerPolicyState(instance WorkerInstance, capturedAt time.Time) (WorkerPolicyState, error) {
	policy, err := ParseTickPolicy(instance.TickPolicyJSON)
	if err != nil {
		return WorkerPolicyState{}, err
	}
	fingerprint, err := TickPolicyFingerprint(instance.TickPolicyJSON)
	if err != nil {
		return WorkerPolicyState{}, err
	}
	return WorkerPolicyState{
		WorkerInstanceID:  instance.WorkerInstanceID,
		WorkerKey:         instance.WorkerKey,
		Locality:          instance.Locality,
		LifecycleStatus:   instance.LifecycleStatus,
		Enabled:           instance.Enabled,
		Paused:            instance.Paused,
		CurrentRunID:      instance.CurrentRunID,
		Policy:            policy,
		PolicyFingerprint: fingerprint,
		NextRunAfter:      instance.NextRunAfter,
		CapturedAt:        capturedAt.UTC(),
	}, nil
}

func lockWorkerInstance(ctx context.Context, tx *sql.Tx, instanceID string) (WorkerInstance, error) {
	instance, err := scanWorkerInstance(tx.QueryRowContext(ctx, workerInstanceSelectSQL()+`
		WHERE worker_instance_id = $1
		FOR UPDATE
	`, instanceID))
	if err == sql.ErrNoRows {
		return WorkerInstance{}, fmt.Errorf("%w: worker %q was not found", ErrNotFound, instanceID)
	}
	return instance, err
}

func hasActiveWorkerLease(ctx context.Context, tx *sql.Tx, instanceID string) (bool, error) {
	var active bool
	err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM workers.worker_leases
			WHERE worker_instance_id = $1
			  AND lease_status = 'active'
			  AND expires_at > now()
		)
	`, instanceID).Scan(&active)
	return active, err
}

func equalOptionalTimes(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func policyNextRunBasis(oldState, newState WorkerPolicyState) string {
	if newState.Policy.Mode == TickModeManual {
		return "manual policy clears next_run_after"
	}
	if oldState.PolicyFingerprint == newState.PolicyFingerprint {
		return "unchanged scheduled policy preserves next_run_after"
	}
	if newState.Policy.Mode == TickModeDailyLocal {
		return fmt.Sprintf("daily_local policy schedules the next %s occurrence in %s", newState.Policy.LocalTime, newState.Policy.Timezone)
	}
	return fmt.Sprintf("interval policy schedules from apply time plus %d seconds", newState.Policy.IntervalSeconds)
}
