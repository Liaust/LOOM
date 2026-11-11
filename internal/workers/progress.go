package workers

import (
	"context"
	"encoding/json"
	"fmt"
)

// RecordRunProgress retains an observation, never a success or accepted checkpoint.
// The existing lease/current-run fence prevents a late producer rewriting history.
func (s Service) RecordRunProgress(ctx context.Context, run RunContext, summary json.RawMessage) error {
	if s.DB == nil || len(summary) > 16<<10 {
		return fmt.Errorf("%w: bounded worker progress and database required", ErrInvalid)
	}
	value, err := normalizeJSONObject(summary, "worker progress")
	if err != nil {
		return err
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE workers.worker_runs wr
		SET result_summary_json=$4,updated_at=now()
		FROM workers.worker_instances wi,workers.worker_leases wl
		WHERE wr.worker_run_id=$1 AND wr.worker_instance_id=wi.worker_instance_id
		AND wi.current_run_id=wr.worker_run_id AND wr.run_status='running'
		AND wr.lease_id=wl.worker_lease_id AND wl.worker_lease_id=$2
		AND wl.generation=$3 AND wl.lease_status='active' AND wl.expires_at>now()`,
		run.Run.WorkerRunID, run.Lease.WorkerLeaseID, run.Lease.Generation, []byte(value))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%w: worker progress lease is no longer current", ErrConflict)
	}
	return nil
}
