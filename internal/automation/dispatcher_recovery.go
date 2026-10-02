package automation

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/routing"
)

// Recover receipts, never replay calls. An expired invocation lease alone does
// not prove that its owner stopped: wait for a terminal worker or linked job.
func (s Service) recoverExpiredInvocations(ctx context.Context, req requestctx.Context, input DispatcherRunInput) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT invocation_id FROM automation.invocations i
		WHERE status IN ('leased','calling') AND lease_expires_at <= $1
		AND ($2 = '' OR invocation_id = $2)
		AND NOT EXISTS (SELECT 1 FROM workers.worker_runs w WHERE w.worker_run_id=i.leased_by_worker_run_id AND w.run_status IN ('starting','running'))
		AND (EXISTS (SELECT 1 FROM workers.worker_runs w WHERE w.worker_run_id=i.leased_by_worker_run_id AND w.run_status IN ('succeeded','failed','cancelled','timed_out'))
		OR EXISTS (SELECT 1 FROM jobs.jobs j WHERE j.status IN ('completed','failed','cancelled','timed_out')
			AND (j.job_id=i.job_id OR EXISTS (SELECT 1 FROM routing.capability_calls c
				WHERE c.metadata->>'invocation_id'=i.invocation_id
				AND (c.job_id=j.job_id OR j.metadata #>> '{routing,capability_call_id}'=c.capability_call_id)))))
		ORDER BY lease_expires_at LIMIT $3`, input.Now, input.InvocationRef, input.BatchSize)
	if err != nil {
		return err
	}
	var refs []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			rows.Close()
			return err
		}
		refs = append(refs, ref)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if err := s.recoverExpiredInvocation(ctx, req, ref, input.Now); err != nil {
			return err
		}
	}
	return nil
}

func (s Service) recoverExpiredInvocation(ctx context.Context, req requestctx.Context, ref string, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	i, err := scanInvocation(tx.QueryRowContext(ctx, invocationSelectSQL()+`
		WHERE invocation_id=$1 AND status IN ('leased','calling') AND lease_expires_at <= $2
		FOR UPDATE SKIP LOCKED`, ref, now))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var workerActive, workerStopped bool
	if err := tx.QueryRowContext(ctx, `SELECT
		EXISTS (SELECT 1 FROM workers.worker_runs WHERE worker_run_id=$1 AND run_status IN ('starting','running')),
		EXISTS (SELECT 1 FROM workers.worker_runs WHERE worker_run_id=$1 AND run_status IN ('succeeded','failed','cancelled','timed_out'))`, i.LeasedByWorkerRunID).Scan(&workerActive, &workerStopped); err != nil {
		return err
	}
	if workerActive {
		return nil
	}
	outcome := routing.CapabilityCallOutcome{Result: i.ResultJSON, ResultRefs: i.ResultRefsJSON}
	var callStatus string
	err = tx.QueryRowContext(ctx, `SELECT capability_call_id, route_id, status, coalesce(job_id,''), result_json, result_refs_json
		FROM routing.capability_calls
		WHERE metadata->>'invocation_id'=$1
		ORDER BY created_at DESC, capability_call_id DESC LIMIT 1`, ref).Scan(
		&outcome.CapabilityCall.CapabilityCallID, &outcome.Route.RouteID, &callStatus, &outcome.JobID, &outcome.Result, &outcome.ResultRefs)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var jobStatus string
	err = tx.QueryRowContext(ctx, `SELECT job_id, status FROM jobs.jobs
		WHERE job_id=$1 OR job_id=$2 OR ($3 <> '' AND metadata #>> '{routing,capability_call_id}'=$3)
		ORDER BY created_at DESC, job_id DESC LIMIT 1`, i.JobID, outcome.JobID, outcome.CapabilityCall.CapabilityCallID).Scan(&outcome.JobID, &jobStatus)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if jobStatus == "created" || jobStatus == "queued" || jobStatus == "running" {
		return nil
	}
	// Manual dispatches have no worker row; recover them only with durable
	// terminal job evidence. Unknown external side effects need human inspection.
	if !workerStopped && jobStatus == "" {
		return nil
	}
	status, fireStatus, event, fireEvent := InvocationStatusRequiresManualAction, FireStatusRequiresManualAction, events.TypeInvocationFailed, events.TypeScheduleFireFailed
	code, message := "automation.dispatch_interrupted", "Dispatcher stopped before its receipt; inspect before retrying"
	if jobStatus == "completed" || (jobStatus == "" && callStatus == routing.CapabilityCallStatusCompleted) {
		status, fireStatus, event, fireEvent = InvocationStatusSucceeded, FireStatusCompleted, events.TypeInvocationCompleted, events.TypeScheduleFireCompleted
		code, message = "", ""
	} else if jobStatus != "" {
		status, fireStatus = InvocationStatusFailed, FireStatusFailed
		code, message = "automation.linked_job_"+jobStatus, "Recovered expired dispatch from terminal linked job"
	}
	if _, err := markInvocationOutcomeTx(ctx, tx, req, i, now, outcome, status, fireStatus, event, fireEvent, code, message); err != nil {
		return err
	}
	return tx.Commit()
}
