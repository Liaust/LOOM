package workers

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/ids"
)

func (s Service) AcquireResourceLease(ctx context.Context, request ResourceLeaseRequest) (ResourceLease, error) {
	if s.DB == nil {
		return ResourceLease{}, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	request.ResourceKey, request.HolderID = strings.TrimSpace(request.ResourceKey), strings.TrimSpace(request.HolderID)
	if request.ResourceKey == "" || request.HolderID == "" {
		return ResourceLease{}, fmt.Errorf("%w: resource_key and holder_id are required", ErrInvalid)
	}
	if request.TTL <= 0 {
		request.TTL = 10 * time.Minute
	}
	now := request.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ResourceLease{}, err
	}
	defer tx.Rollback()
	var capacity int
	var enabled bool
	if err = tx.QueryRowContext(ctx, `SELECT capacity, enabled FROM workers.resource_capacities WHERE resource_key=$1 FOR UPDATE`, request.ResourceKey).Scan(&capacity, &enabled); err != nil {
		if err == sql.ErrNoRows {
			return ResourceLease{}, fmt.Errorf("%w: resource %q was not found", ErrNotFound, request.ResourceKey)
		}
		return ResourceLease{}, err
	}
	if !enabled {
		return ResourceLease{}, fmt.Errorf("%w: resource %q is disabled", ErrConflict, request.ResourceKey)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workers.resource_leases SET status='expired', released_at=$2, renewed_at=$2 WHERE resource_key=$1 AND status='active' AND expires_at<=$2`, request.ResourceKey, now); err != nil {
		return ResourceLease{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT slot_number FROM workers.resource_leases WHERE resource_key=$1 AND status='active' FOR UPDATE`, request.ResourceKey)
	if err != nil {
		return ResourceLease{}, err
	}
	active := map[int]bool{}
	for rows.Next() {
		var slot int
		if err := rows.Scan(&slot); err != nil {
			rows.Close()
			return ResourceLease{}, err
		}
		active[slot] = true
	}
	if err := rows.Close(); err != nil {
		return ResourceLease{}, err
	}
	slot := availableResourceSlot(capacity, active)
	if slot == 0 {
		return ResourceLease{}, fmt.Errorf("%w: resource %q has no available capacity", ErrConflict, request.ResourceKey)
	}
	var generation int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(max(generation),0)+1 FROM workers.resource_leases WHERE resource_key=$1 AND slot_number=$2`, request.ResourceKey, slot).Scan(&generation); err != nil {
		return ResourceLease{}, err
	}
	lease, err := scanResourceLease(tx.QueryRowContext(ctx, `INSERT INTO workers.resource_leases (worker_resource_lease_id,resource_key,slot_number,status,generation,holder_id,worker_instance_id,worker_run_id,knowledge_pipeline_run_id,knowledge_pipeline_stage_run_id,acquired_at,renewed_at,expires_at,metadata) VALUES ($1,$2,$3,'active',$4,$5,$6,$7,$8,$9,$10,$10,$11,'{"schema_version":"worker_resource_lease.metadata.v1"}'::jsonb) RETURNING `+resourceLeaseColumns(), ids.NewWorkerResourceLeaseID(), request.ResourceKey, slot, generation, request.HolderID, request.WorkerInstanceID, request.WorkerRunID, request.KnowledgePipelineRunID, request.KnowledgePipelineStageRunID, now, now.Add(request.TTL)))
	if err != nil {
		return ResourceLease{}, err
	}
	if err = tx.Commit(); err != nil {
		return ResourceLease{}, err
	}
	return lease, nil
}

func (s Service) RenewResourceLease(ctx context.Context, lease ResourceLease, ttl time.Duration) (ResourceLease, error) {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return scanResourceLease(s.DB.QueryRowContext(ctx, `UPDATE workers.resource_leases SET renewed_at=now(),expires_at=now()+$3::interval WHERE worker_resource_lease_id=$1 AND generation=$2 AND status='active' AND expires_at>now() RETURNING `+resourceLeaseColumns(), lease.WorkerResourceLeaseID, lease.Generation, fmt.Sprintf("%f seconds", ttl.Seconds())))
}

func (s Service) ReleaseResourceLease(ctx context.Context, lease ResourceLease) error {
	result, err := s.DB.ExecContext(ctx, `UPDATE workers.resource_leases SET status='released',released_at=now(),renewed_at=now() WHERE worker_resource_lease_id=$1 AND generation=$2 AND status='active'`, lease.WorkerResourceLeaseID, lease.Generation)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("%w: resource lease fence is stale", ErrConflict)
	}
	return nil
}

func (s Service) ExpireResourceLeases(ctx context.Context, now time.Time) (int64, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE workers.resource_leases SET status='expired',released_at=$1,renewed_at=$1 WHERE status='active' AND expires_at<=$1`, now)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func resourceLeaseColumns() string {
	return `worker_resource_lease_id,resource_key,slot_number,status,generation,holder_id,worker_instance_id,worker_run_id,knowledge_pipeline_run_id,knowledge_pipeline_stage_run_id,acquired_at,renewed_at,expires_at,released_at,metadata`
}

func scanResourceLease(scanner scanner) (ResourceLease, error) {
	var lease ResourceLease
	var instanceID, runID, pipelineRunID, stageRunID sql.NullString
	var released sql.NullTime
	var metadata []byte
	err := scanner.Scan(&lease.WorkerResourceLeaseID, &lease.ResourceKey, &lease.SlotNumber, &lease.Status, &lease.Generation, &lease.HolderID, &instanceID, &runID, &pipelineRunID, &stageRunID, &lease.AcquiredAt, &lease.RenewedAt, &lease.ExpiresAt, &released, &metadata)
	if err != nil {
		return ResourceLease{}, err
	}
	lease.WorkerInstanceID, lease.WorkerRunID = stringPtr(instanceID), stringPtr(runID)
	lease.KnowledgePipelineRunID, lease.KnowledgePipelineStageRunID = stringPtr(pipelineRunID), stringPtr(stageRunID)
	lease.ReleasedAt, lease.Metadata = timePtr(released), rawMessage(metadata)
	return lease, nil
}
