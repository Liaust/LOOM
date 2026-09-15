package workers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
)

func (s Service) SeedBuiltins(ctx context.Context, req requestctx.Context, mainNodeRef string) (SeedResult, error) {
	if s.DB == nil {
		return SeedResult{}, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	if s.Registry == nil {
		return SeedResult{}, fmt.Errorf("%w: worker registry is required", ErrInvalid)
	}

	kindDescriptors := s.Registry.Descriptors()
	if len(kindDescriptors) == 0 {
		return SeedResult{}, fmt.Errorf("%w: worker registry has no descriptors", ErrInvalid)
	}
	instanceDescriptors := s.Registry.DefaultInstances()

	mainNodeID, err := resolveSeedNode(ctx, s.DB, req, mainNodeRef)
	if err != nil {
		return SeedResult{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return SeedResult{}, err
	}
	defer tx.Rollback()

	result := SeedResult{}
	kindsByName := map[string]KindDescriptor{}
	for _, descriptor := range kindDescriptors {
		created, err := upsertWorkerKind(ctx, tx, req, descriptor)
		if err != nil {
			return SeedResult{}, err
		}
		kindsByName[descriptor.WorkerKind] = descriptor
		if created {
			result.KindsCreated++
			if err := appendSeedEvent(ctx, tx, req, events.TypeWorkerKindRegistered, "worker_kind", descriptor.WorkerKind, "registered", map[string]any{
				"worker_kind": descriptor.WorkerKind,
			}); err != nil {
				return SeedResult{}, err
			}
		} else {
			result.KindsUpdated++
		}
	}

	for _, instanceDescriptor := range instanceDescriptors {
		kind, ok := kindsByName[strings.TrimSpace(instanceDescriptor.WorkerKind)]
		if !ok {
			return SeedResult{}, fmt.Errorf("%w: worker kind %q is not registered", ErrInvalid, instanceDescriptor.WorkerKind)
		}
		instanceDescriptor, err = normalizeInstanceDescriptor(instanceDescriptor, kind)
		if err != nil {
			return SeedResult{}, err
		}

		instanceID, created, err := upsertWorkerInstance(ctx, tx, instanceDescriptor, mainNodeID)
		if err != nil {
			return SeedResult{}, err
		}
		if created {
			result.InstancesCreated++
			if err := appendSeedEvent(ctx, tx, req, events.TypeWorkerInstanceRegistered, "worker_instance", instanceID, "registered", map[string]any{
				"worker_instance_id": instanceID,
				"worker_key":         instanceDescriptor.WorkerKey,
				"worker_kind":        instanceDescriptor.WorkerKind,
			}); err != nil {
				return SeedResult{}, err
			}
		} else {
			result.InstancesUpdated++
		}

		healthCreated, err := ensureSeedHealth(ctx, tx, instanceID)
		if err != nil {
			return SeedResult{}, err
		}
		if healthCreated {
			result.HealthCreated++
		}
	}

	if err := tx.Commit(); err != nil {
		return SeedResult{}, err
	}
	return result, nil
}

func resolveSeedNode(ctx context.Context, db *sql.DB, req requestctx.Context, mainNodeRef string) (string, error) {
	mainNodeRef = strings.TrimSpace(mainNodeRef)
	if mainNodeRef == "" {
		if strings.TrimSpace(req.OriginNodeID) != "" {
			return req.OriginNodeID, nil
		}
		mainNodeRef = "main"
	}

	var nodeID string
	err := db.QueryRowContext(ctx, `
		SELECT node_id
		FROM nodes.nodes
		WHERE node_id = $1 OR node_key = $1
	`, mainNodeRef).Scan(&nodeID)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("%w: node %q was not found", ErrNotFound, mainNodeRef)
	}
	if err != nil {
		return "", err
	}
	return nodeID, nil
}

func upsertWorkerKind(ctx context.Context, tx *sql.Tx, req requestctx.Context, descriptor KindDescriptor) (bool, error) {
	descriptor, err := normalizeKindDescriptor(descriptor)
	if err != nil {
		return false, err
	}

	var inserted string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO workers.worker_kinds (
			worker_kind, display_name, description, runtime_owner, runtime_package,
			status, supported_localities, may_create_jobs, may_call_capabilities,
			may_touch_filesystem, may_store_raw_payloads, default_tick_policy_json,
			default_concurrency_policy_json, default_retry_policy_json,
			default_timeout_policy_json, default_resource_limits_json,
			config_schema_json, checkpoint_schema_json, result_schema_json,
			registered_by_actor_id, metadata
		)
		VALUES (
			$1, $2, $3, $4, $5,
			$6, $7::text[], $8, $9,
			$10, $11, $12,
			$13, $14,
			$15, $16,
			$17, $18, $19,
			nullif($20, ''), $21
		)
		ON CONFLICT (worker_kind) DO NOTHING
		RETURNING worker_kind
	`,
		descriptor.WorkerKind,
		descriptor.DisplayName,
		descriptor.Description,
		descriptor.RuntimeOwner,
		descriptor.RuntimePackage,
		descriptor.Status,
		textArrayLiteral(descriptor.SupportedLocalities),
		descriptor.MayCreateJobs,
		descriptor.MayCallCapabilities,
		descriptor.MayTouchFilesystem,
		descriptor.MayStoreRawPayloads,
		[]byte(descriptor.DefaultTickPolicyJSON),
		[]byte(descriptor.DefaultConcurrencyPolicyJSON),
		[]byte(descriptor.DefaultRetryPolicyJSON),
		[]byte(descriptor.DefaultTimeoutPolicyJSON),
		[]byte(descriptor.DefaultResourceLimitsJSON),
		[]byte(descriptor.ConfigSchemaJSON),
		[]byte(descriptor.CheckpointSchemaJSON),
		[]byte(descriptor.ResultSchemaJSON),
		req.ActorID,
		[]byte(descriptor.Metadata),
	).Scan(&inserted)
	if err == nil {
		return true, nil
	}
	if err != sql.ErrNoRows {
		return false, err
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE workers.worker_kinds
		SET display_name = $2,
		    description = $3,
		    runtime_owner = $4,
		    runtime_package = $5,
		    status = $6,
		    supported_localities = $7::text[],
		    may_create_jobs = $8,
		    may_call_capabilities = $9,
		    may_touch_filesystem = $10,
		    may_store_raw_payloads = $11,
		    default_tick_policy_json = $12,
		    default_concurrency_policy_json = $13,
		    default_retry_policy_json = $14,
		    default_timeout_policy_json = $15,
		    default_resource_limits_json = $16,
		    config_schema_json = $17,
		    checkpoint_schema_json = $18,
		    result_schema_json = $19,
		    registered_by_actor_id = COALESCE(registered_by_actor_id, nullif($20, '')),
		    metadata = $21,
		    updated_at = now()
		WHERE worker_kind = $1
	`,
		descriptor.WorkerKind,
		descriptor.DisplayName,
		descriptor.Description,
		descriptor.RuntimeOwner,
		descriptor.RuntimePackage,
		descriptor.Status,
		textArrayLiteral(descriptor.SupportedLocalities),
		descriptor.MayCreateJobs,
		descriptor.MayCallCapabilities,
		descriptor.MayTouchFilesystem,
		descriptor.MayStoreRawPayloads,
		[]byte(descriptor.DefaultTickPolicyJSON),
		[]byte(descriptor.DefaultConcurrencyPolicyJSON),
		[]byte(descriptor.DefaultRetryPolicyJSON),
		[]byte(descriptor.DefaultTimeoutPolicyJSON),
		[]byte(descriptor.DefaultResourceLimitsJSON),
		[]byte(descriptor.ConfigSchemaJSON),
		[]byte(descriptor.CheckpointSchemaJSON),
		[]byte(descriptor.ResultSchemaJSON),
		req.ActorID,
		[]byte(descriptor.Metadata),
	)
	return false, err
}

func normalizeInstanceDescriptor(instance InstanceDescriptor, kind KindDescriptor) (InstanceDescriptor, error) {
	instance.WorkerKey = strings.TrimSpace(instance.WorkerKey)
	if err := ValidateWorkerKey(instance.WorkerKey); err != nil {
		return InstanceDescriptor{}, err
	}
	instance.WorkerKind = strings.TrimSpace(instance.WorkerKind)
	if err := ValidateWorkerKind(instance.WorkerKind); err != nil {
		return InstanceDescriptor{}, err
	}
	if instance.WorkerKind != kind.WorkerKind {
		return InstanceDescriptor{}, fmt.Errorf("%w: instance %q kind %q does not match descriptor kind %q", ErrInvalid, instance.WorkerKey, instance.WorkerKind, kind.WorkerKind)
	}
	instance.DisplayName = strings.TrimSpace(instance.DisplayName)
	if instance.DisplayName == "" {
		instance.DisplayName = kind.DisplayName
	}
	instance.Description = strings.TrimSpace(instance.Description)
	if instance.Description == "" {
		instance.Description = kind.Description
	}
	instance.Locality = strings.TrimSpace(instance.Locality)
	if instance.Locality == "" {
		instance.Locality = LocalityMainOwned
	}
	if !validLocality(instance.Locality) {
		return InstanceDescriptor{}, fmt.Errorf("%w: instance %q locality %q is invalid", ErrInvalid, instance.WorkerKey, instance.Locality)
	}

	var err error
	if instance.ConfigJSON, err = normalizeJSONObject(instance.ConfigJSON, "config_json"); err != nil {
		return InstanceDescriptor{}, err
	}
	if len(instance.TickPolicyJSON) == 0 {
		instance.TickPolicyJSON = kind.DefaultTickPolicyJSON
	}
	if instance.TickPolicyJSON, err = normalizeJSONObject(instance.TickPolicyJSON, "tick_policy_json"); err != nil {
		return InstanceDescriptor{}, err
	}
	if err := validateManualOnlyWorkerPolicy(instance.WorkerKind, instance.TickPolicyJSON); err != nil {
		return InstanceDescriptor{}, err
	}
	if len(instance.ConcurrencyJSON) == 0 {
		instance.ConcurrencyJSON = kind.DefaultConcurrencyPolicyJSON
	}
	if instance.ConcurrencyJSON, err = normalizeJSONObject(instance.ConcurrencyJSON, "concurrency_policy_json"); err != nil {
		return InstanceDescriptor{}, err
	}
	if len(instance.RetryPolicyJSON) == 0 {
		instance.RetryPolicyJSON = kind.DefaultRetryPolicyJSON
	}
	if instance.RetryPolicyJSON, err = normalizeJSONObject(instance.RetryPolicyJSON, "retry_policy_json"); err != nil {
		return InstanceDescriptor{}, err
	}
	if len(instance.TimeoutPolicyJSON) == 0 {
		instance.TimeoutPolicyJSON = kind.DefaultTimeoutPolicyJSON
	}
	if instance.TimeoutPolicyJSON, err = normalizeJSONObject(instance.TimeoutPolicyJSON, "timeout_policy_json"); err != nil {
		return InstanceDescriptor{}, err
	}
	if len(instance.ResourceLimitsJSON) == 0 {
		instance.ResourceLimitsJSON = kind.DefaultResourceLimitsJSON
	}
	if instance.ResourceLimitsJSON, err = normalizeJSONObject(instance.ResourceLimitsJSON, "resource_limits_json"); err != nil {
		return InstanceDescriptor{}, err
	}
	if instance.VisibilityJSON, err = normalizeJSONObject(instance.VisibilityJSON, "visibility_json"); err != nil {
		return InstanceDescriptor{}, err
	}
	if instance.Metadata, err = normalizeJSONObject(instance.Metadata, "metadata"); err != nil {
		return InstanceDescriptor{}, err
	}
	return instance, nil
}

func upsertWorkerInstance(ctx context.Context, tx *sql.Tx, descriptor InstanceDescriptor, mainNodeID string) (string, bool, error) {
	instanceID := ids.NewWorkerInstanceID()

	var inserted string
	err := tx.QueryRowContext(ctx, `
		INSERT INTO workers.worker_instances (
			worker_instance_id, worker_key, worker_kind, display_name, description,
			owner_node_id, host_node_id, locality, lifecycle_status, enabled, paused,
			config_json, tick_policy_json, concurrency_policy_json, retry_policy_json,
			timeout_policy_json, resource_limits_json, visibility_json, metadata
		)
		VALUES (
			$1, $2, $3, $4, $5,
			$6, $6, $7, 'active', true, false,
			$8, $9, $10, $11,
			$12, $13, $14, $15
		)
		ON CONFLICT (worker_key) DO NOTHING
		RETURNING worker_instance_id
	`,
		instanceID,
		descriptor.WorkerKey,
		descriptor.WorkerKind,
		descriptor.DisplayName,
		descriptor.Description,
		mainNodeID,
		descriptor.Locality,
		[]byte(descriptor.ConfigJSON),
		[]byte(descriptor.TickPolicyJSON),
		[]byte(descriptor.ConcurrencyJSON),
		[]byte(descriptor.RetryPolicyJSON),
		[]byte(descriptor.TimeoutPolicyJSON),
		[]byte(descriptor.ResourceLimitsJSON),
		[]byte(descriptor.VisibilityJSON),
		[]byte(descriptor.Metadata),
	).Scan(&inserted)
	if err == nil {
		return inserted, true, nil
	}
	if err != sql.ErrNoRows {
		return "", false, err
	}

	err = tx.QueryRowContext(ctx, `
		UPDATE workers.worker_instances
		SET worker_kind = $1,
		    display_name = $2,
		    description = $3,
		    owner_node_id = $4,
		    host_node_id = $4,
		    locality = $5,
		    lifecycle_status = CASE
		        WHEN lifecycle_status IN ('disabled', 'retired') THEN lifecycle_status
		        ELSE 'active'
		    END,
		    enabled = CASE WHEN lifecycle_status IN ('disabled', 'retired') THEN enabled ELSE true END,
		    config_json = $6,
		    tick_policy_json = CASE
		        WHEN metadata ? 'policy_control' THEN tick_policy_json
		        ELSE $7::jsonb
		    END,
		    concurrency_policy_json = $8,
		    retry_policy_json = $9,
		    timeout_policy_json = $10,
		    resource_limits_json = $11,
		    visibility_json = $12,
		    metadata = $13::jsonb || CASE
		        WHEN metadata ? 'policy_control'
		        THEN jsonb_build_object('policy_control', metadata->'policy_control')
		        ELSE '{}'::jsonb
		    END,
		    updated_at = now()
		WHERE worker_key = $14
		RETURNING worker_instance_id
	`,
		descriptor.WorkerKind,
		descriptor.DisplayName,
		descriptor.Description,
		mainNodeID,
		descriptor.Locality,
		[]byte(descriptor.ConfigJSON),
		[]byte(descriptor.TickPolicyJSON),
		[]byte(descriptor.ConcurrencyJSON),
		[]byte(descriptor.RetryPolicyJSON),
		[]byte(descriptor.TimeoutPolicyJSON),
		[]byte(descriptor.ResourceLimitsJSON),
		[]byte(descriptor.VisibilityJSON),
		[]byte(descriptor.Metadata),
		descriptor.WorkerKey,
	).Scan(&instanceID)
	return instanceID, false, err
}

func ensureSeedHealth(ctx context.Context, tx *sql.Tx, instanceID string) (bool, error) {
	healthID := ids.NewWorkerHealthID()
	detailsJSON := json.RawMessage(`{"schema_version":"worker_health.v0.2","seeded":true}`)
	metadataJSON := json.RawMessage(`{"schema_version":"worker_health.metadata.v0.2","seeded_by":"workers.SeedBuiltins"}`)

	var inserted string
	err := tx.QueryRowContext(ctx, `
		INSERT INTO workers.worker_health (
			worker_health_id, worker_instance_id, health_status, severity,
			summary, attention_required, details_json, metadata
		)
		VALUES (
			$1, $2, 'unknown', 'info',
			'Worker has not run yet.', false, $3, $4
		)
		ON CONFLICT (worker_instance_id) DO NOTHING
		RETURNING worker_health_id
	`, healthID, instanceID, []byte(detailsJSON), []byte(metadataJSON)).Scan(&inserted)
	if err == nil {
		return true, nil
	}
	if err == sql.ErrNoRows {
		return false, nil
	}
	return false, err
}

func appendSeedEvent(ctx context.Context, tx *sql.Tx, req requestctx.Context, eventType, targetKind, targetID, status string, payload map[string]any) error {
	_, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       eventType,
		EventLevel:      "audit",
		Request:         req,
		ScopeID:         req.ScopeID,
		TargetKind:      targetKind,
		TargetID:        targetID,
		Status:          status,
		Result:          "ok",
		Payload:         payload,
		VisibilityClass: "internal",
	})
	return err
}

func textArrayLiteral(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ReplaceAll(value, `\`, `\\`)
		value = strings.ReplaceAll(value, `"`, `\"`)
		quoted = append(quoted, `"`+value+`"`)
	}
	return "{" + strings.Join(quoted, ",") + "}"
}
