package workers

import (
	"encoding/json"
	"fmt"
	"time"

	"loom.local/loom/internal/ids"
)

const (
	ResourceKnowledgeHeavy = "knowledge_heavy"
	ResourceLeaseActive    = "active"
	ResourceLeaseReleased  = "released"
	ResourceLeaseExpired   = "expired"
	ResourceLeaseCancelled = "cancelled"
)

type ResourceCapacity struct {
	WorkerResourceCapacityID string          `json:"worker_resource_capacity_id"`
	ResourceKey              string          `json:"resource_key"`
	Capacity                 int             `json:"capacity"`
	Enabled                  bool            `json:"enabled"`
	Policy                   json.RawMessage `json:"policy"`
	CreatedAt                time.Time       `json:"created_at"`
	UpdatedAt                time.Time       `json:"updated_at"`
}

type ResourceLease struct {
	WorkerResourceLeaseID       string          `json:"worker_resource_lease_id"`
	ResourceKey                 string          `json:"resource_key"`
	SlotNumber                  int             `json:"slot_number"`
	Status                      string          `json:"status"`
	Generation                  int64           `json:"generation"`
	HolderID                    string          `json:"holder_id"`
	WorkerInstanceID            *string         `json:"worker_instance_id,omitempty"`
	WorkerRunID                 *string         `json:"worker_run_id,omitempty"`
	KnowledgePipelineRunID      *string         `json:"knowledge_pipeline_run_id,omitempty"`
	KnowledgePipelineStageRunID *string         `json:"knowledge_pipeline_stage_run_id,omitempty"`
	AcquiredAt                  time.Time       `json:"acquired_at"`
	RenewedAt                   time.Time       `json:"renewed_at"`
	ExpiresAt                   time.Time       `json:"expires_at"`
	ReleasedAt                  *time.Time      `json:"released_at,omitempty"`
	Metadata                    json.RawMessage `json:"metadata"`
}

func ValidateResourceCapacity(capacity ResourceCapacity) error {
	if err := ids.Validate(ids.WorkerResourceCapacityPrefix, capacity.WorkerResourceCapacityID); err != nil {
		return invalidResource("worker_resource_capacity_id is invalid: %v", err)
	}
	if capacity.ResourceKey == "" {
		return invalidResource("resource_key is required")
	}
	if capacity.Capacity <= 0 {
		return invalidResource("capacity must be greater than zero")
	}
	if len(capacity.Policy) == 0 || !json.Valid(capacity.Policy) {
		return invalidResource("policy must be valid JSON")
	}
	return nil
}

func ValidateResourceLease(lease ResourceLease) error {
	if err := ids.Validate(ids.WorkerResourceLeasePrefix, lease.WorkerResourceLeaseID); err != nil {
		return invalidResource("worker_resource_lease_id is invalid: %v", err)
	}
	if lease.ResourceKey == "" || lease.HolderID == "" {
		return invalidResource("resource_key and holder_id are required")
	}
	if lease.SlotNumber <= 0 || lease.Generation <= 0 {
		return invalidResource("slot_number and generation must be greater than zero")
	}
	if lease.WorkerInstanceID != nil {
		if err := ids.Validate(ids.WorkerInstancePrefix, *lease.WorkerInstanceID); err != nil {
			return invalidResource("worker_instance_id is invalid: %v", err)
		}
	}
	if lease.WorkerRunID != nil {
		if err := ids.Validate(ids.WorkerRunPrefix, *lease.WorkerRunID); err != nil {
			return invalidResource("worker_run_id is invalid: %v", err)
		}
	}
	if lease.KnowledgePipelineRunID != nil {
		if err := ids.Validate(ids.KnowledgePipelineRunPrefix, *lease.KnowledgePipelineRunID); err != nil {
			return invalidResource("knowledge_pipeline_run_id is invalid: %v", err)
		}
	}
	if lease.KnowledgePipelineStageRunID != nil {
		if err := ids.Validate(ids.KnowledgePipelineStageRunPrefix, *lease.KnowledgePipelineStageRunID); err != nil {
			return invalidResource("knowledge_pipeline_stage_run_id is invalid: %v", err)
		}
	}
	switch lease.Status {
	case ResourceLeaseActive, ResourceLeaseReleased, ResourceLeaseExpired, ResourceLeaseCancelled:
	default:
		return invalidResource("unsupported resource lease status %q", lease.Status)
	}
	if lease.ExpiresAt.IsZero() || !lease.ExpiresAt.After(lease.AcquiredAt) {
		return invalidResource("expires_at must be after acquired_at")
	}
	return nil
}

func invalidResource(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}
