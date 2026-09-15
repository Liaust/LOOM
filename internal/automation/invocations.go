package automation

import (
	"context"
	"database/sql"
	"encoding/json"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
)

type CreateInvocationTxInput struct {
	AutomationID        string
	SourceKind          string
	SourceRef           string
	SourceOccurrenceRef string
	ActorID             string
	OriginNodeID        string
	ScopeID             *string
	ProjectID           *string
	TargetCapability    string
	InputJSON           json.RawMessage
	IdempotencyKey      string
	MaxAttempts         int
	Metadata            json.RawMessage
	EventPayload        map[string]any
}

func (s Service) createInvocationTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, input CreateInvocationTxInput) (Invocation, error) {
	inputJSON, err := normalizeJSONObject(input.InputJSON, "input_json")
	if err != nil {
		return Invocation{}, err
	}
	metadata, err := normalizeJSONObject(input.Metadata, "metadata")
	if err != nil {
		return Invocation{}, err
	}
	inputHash, err := objectHash(inputJSON)
	if err != nil {
		return Invocation{}, err
	}
	if input.MaxAttempts <= 0 {
		input.MaxAttempts = defaultMaxAttempts
	}

	invocation, err := scanInvocation(tx.QueryRowContext(ctx, `
		INSERT INTO automation.invocations (
			invocation_id, automation_id, source_kind, source_ref,
			source_occurrence_ref, actor_id, origin_node_id, scope_id, project_id,
			target_capability, input_json, input_hash, idempotency_key, status,
			attempt_count, max_attempts, metadata_json
		)
		VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9,
			$10, $11::jsonb, $12, $13, $14, 0, $15, $16::jsonb
		)
		RETURNING invocation_id, automation_id, source_kind, source_ref,
		          source_occurrence_ref, actor_id, origin_node_id, scope_id,
		          project_id, target_capability, input_json, input_hash,
		          idempotency_key, status, attempt_count, max_attempts,
		          next_attempt_at, leased_by_worker_run_id, leased_at,
		          lease_expires_at, route_id, capability_call_id, job_id,
		          policy_decision_id, approval_id, grant_id, result_json,
		          result_refs_json, failure_code, failure_message, created_at,
		          updated_at, started_at, completed_at, failed_at, metadata_json
	`, ids.NewInvocationID(),
		input.AutomationID,
		input.SourceKind,
		input.SourceRef,
		input.SourceOccurrenceRef,
		input.ActorID,
		input.OriginNodeID,
		input.ScopeID,
		input.ProjectID,
		input.TargetCapability,
		inputJSON,
		inputHash,
		input.IdempotencyKey,
		InvocationStatusPending,
		input.MaxAttempts,
		metadata,
	))
	if err != nil {
		return Invocation{}, err
	}

	payload := input.EventPayload
	if payload == nil {
		payload = map[string]any{}
	}
	payload["automation_id"] = invocation.AutomationID
	payload["source_kind"] = invocation.SourceKind
	payload["source_ref"] = invocation.SourceRef
	payload["source_occurrence_ref"] = invocation.SourceOccurrenceRef
	payload["target_capability"] = invocation.TargetCapability
	payload["input_hash"] = invocation.InputHash
	payload["invocation_idempotency"] = invocation.IdempotencyKey
	payload["dispatcher_idempotency"] = "automation-dispatch:" + invocation.InvocationID + ":attempt:1"
	if err := appendLifecycleEventTx(ctx, tx, req, events.TypeInvocationCreated, "invocation", invocation.InvocationID, invocation.Status, payload); err != nil {
		return Invocation{}, err
	}
	return invocation, nil
}
