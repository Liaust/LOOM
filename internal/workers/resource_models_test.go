package workers

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
)

func TestValidateResourceModels(t *testing.T) {
	capacity := ResourceCapacity{WorkerResourceCapacityID: ids.NewWorkerResourceCapacityID(), ResourceKey: ResourceKnowledgeHeavy, Capacity: 1, Enabled: true, Policy: json.RawMessage(`{"enforced":true}`)}
	if err := ValidateResourceCapacity(capacity); err != nil {
		t.Fatalf("ValidateResourceCapacity returned error: %v", err)
	}
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	lease := ResourceLease{WorkerResourceLeaseID: ids.NewWorkerResourceLeaseID(), ResourceKey: ResourceKnowledgeHeavy, SlotNumber: 1, Status: ResourceLeaseActive, Generation: 1, HolderID: "worker:one", AcquiredAt: now, RenewedAt: now, ExpiresAt: now.Add(time.Minute)}
	if err := ValidateResourceLease(lease); err != nil {
		t.Fatalf("ValidateResourceLease returned error: %v", err)
	}
	lease.Status = "invented"
	if err := ValidateResourceLease(lease); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid lease error = %v, want ErrInvalid", err)
	}
}
