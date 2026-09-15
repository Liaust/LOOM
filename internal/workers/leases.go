package workers

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"loom.local/loom/internal/ids"
)

func acquireLease(ctx context.Context, tx *sql.Tx, instance WorkerInstance, holderID, holderKind string, ttl time.Duration) (WorkerLease, error) {
	if ttl <= 0 {
		ttl = time.Minute
	}
	leaseKey := "worker:" + instance.WorkerInstanceID
	now := time.Now().UTC()

	if err := expireLeaseIfNeeded(ctx, tx, leaseKey, now); err != nil {
		return WorkerLease{}, err
	}

	var active string
	err := tx.QueryRowContext(ctx, `
		SELECT worker_lease_id
		FROM workers.worker_leases
		WHERE lease_key = $1 AND lease_status = 'active'
		LIMIT 1
	`, leaseKey).Scan(&active)
	if err == nil {
		return WorkerLease{}, fmt.Errorf("%w: worker %s already has active lease %s", ErrConflict, instance.WorkerKey, active)
	}
	if err != sql.ErrNoRows {
		return WorkerLease{}, err
	}

	var generation int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(max(generation), 0) + 1
		FROM workers.worker_leases
		WHERE lease_key = $1
	`, leaseKey).Scan(&generation); err != nil {
		return WorkerLease{}, err
	}

	return scanWorkerLease(tx.QueryRowContext(ctx, `
		INSERT INTO workers.worker_leases (
			worker_lease_id, worker_instance_id, lease_key, lease_status,
			generation, holder_id, holder_kind, acquired_at, renewed_at,
			expires_at, metadata
		)
		VALUES (
			$1, $2, $3, 'active',
			$4, $5, $6, $7, $7,
			$8, '{"schema_version":"worker_lease.metadata.v0.2"}'::jsonb
		)
		RETURNING worker_lease_id, worker_instance_id, lease_key, lease_status,
		          generation, holder_id, holder_kind, run_id, acquired_at,
		          renewed_at, expires_at, released_at, metadata
	`, ids.NewWorkerLeaseID(), instance.WorkerInstanceID, leaseKey, generation, holderID, holderKind, now, now.Add(ttl)))
}

func releaseLease(ctx context.Context, tx *sql.Tx, lease WorkerLease) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE workers.worker_leases
		SET lease_status = 'released',
		    released_at = now(),
		    renewed_at = now()
		WHERE worker_lease_id = $1
		  AND generation = $2
		  AND lease_status = 'active'
	`, lease.WorkerLeaseID, lease.Generation)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("%w: worker lease %s generation %d is not active", ErrConflict, lease.WorkerLeaseID, lease.Generation)
	}
	return nil
}

func expireLeaseIfNeeded(ctx context.Context, tx *sql.Tx, leaseKey string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE workers.worker_leases
		SET lease_status = 'expired',
		    released_at = $2,
		    renewed_at = $2
		WHERE lease_key = $1
		  AND lease_status = 'active'
		  AND expires_at <= $2
	`, leaseKey, now)
	return err
}

func attachLeaseRun(ctx context.Context, tx *sql.Tx, lease WorkerLease, runID string) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE workers.worker_leases
		SET run_id = $3,
		    renewed_at = now()
		WHERE worker_lease_id = $1
		  AND generation = $2
		  AND lease_status = 'active'
	`, lease.WorkerLeaseID, lease.Generation, runID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("%w: worker lease %s generation %d is not active", ErrConflict, lease.WorkerLeaseID, lease.Generation)
	}
	return nil
}

func ensureLeaseFence(ctx context.Context, tx *sql.Tx, lease WorkerLease) error {
	var exists bool
	err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM workers.worker_leases
			WHERE worker_lease_id = $1
			  AND generation = $2
			  AND lease_status = 'active'
			  AND expires_at > now()
		)
	`, lease.WorkerLeaseID, lease.Generation).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: worker lease %s generation %d is no longer active", ErrConflict, lease.WorkerLeaseID, lease.Generation)
	}
	return nil
}

func workerLeaseSelectSQL() string {
	return `
		SELECT worker_lease_id, worker_instance_id, lease_key, lease_status,
		       generation, holder_id, holder_kind, run_id, acquired_at,
		       renewed_at, expires_at, released_at, metadata
		FROM workers.worker_leases
	`
}

func scanWorkerLease(scanner scanner) (WorkerLease, error) {
	var lease WorkerLease
	var runID sql.NullString
	var releasedAt sql.NullTime
	var metadata []byte
	if err := scanner.Scan(
		&lease.WorkerLeaseID,
		&lease.WorkerInstanceID,
		&lease.LeaseKey,
		&lease.LeaseStatus,
		&lease.Generation,
		&lease.HolderID,
		&lease.HolderKind,
		&runID,
		&lease.AcquiredAt,
		&lease.RenewedAt,
		&lease.ExpiresAt,
		&releasedAt,
		&metadata,
	); err != nil {
		return WorkerLease{}, err
	}
	lease.RunID = stringPtr(runID)
	lease.ReleasedAt = timePtr(releasedAt)
	lease.Metadata = rawMessage(metadata)
	return lease, nil
}
