-- +goose Up

CREATE INDEX IF NOT EXISTS workers_worker_controls_applied_run_idx
    ON workers.worker_controls (applied_by_run_id)
    WHERE applied_by_run_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS workers_worker_leases_run_idx
    ON workers.worker_leases (run_id)
    WHERE run_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS workers_worker_health_current_run_idx
    ON workers.worker_health (current_run_id)
    WHERE current_run_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS workers_worker_heartbeats_current_run_idx
    ON workers.worker_heartbeats (current_run_id)
    WHERE current_run_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS automation_schedule_fires_worker_run_idx
    ON automation.schedule_fires (worker_run_id)
    WHERE worker_run_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS automation_invocations_leased_worker_run_idx
    ON automation.invocations (leased_by_worker_run_id)
    WHERE leased_by_worker_run_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS automation_direct_events_ingest_worker_run_idx
    ON automation.direct_events (ingest_worker_run_id)
    WHERE ingest_worker_run_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS search_index_status_last_worker_run_idx
    ON search.index_status (last_worker_run_id)
    WHERE last_worker_run_id IS NOT NULL;

-- +goose Down

DROP INDEX IF EXISTS search.search_index_status_last_worker_run_idx;
DROP INDEX IF EXISTS automation.automation_direct_events_ingest_worker_run_idx;
DROP INDEX IF EXISTS automation.automation_invocations_leased_worker_run_idx;
DROP INDEX IF EXISTS automation.automation_schedule_fires_worker_run_idx;
DROP INDEX IF EXISTS workers.workers_worker_heartbeats_current_run_idx;
DROP INDEX IF EXISTS workers.workers_worker_health_current_run_idx;
DROP INDEX IF EXISTS workers.workers_worker_leases_run_idx;
DROP INDEX IF EXISTS workers.workers_worker_controls_applied_run_idx;
