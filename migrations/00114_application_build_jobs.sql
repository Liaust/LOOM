-- +goose Up
ALTER TABLE jobs.jobs DROP CONSTRAINT jobs_jobs_job_type_v034_check;
ALTER TABLE jobs.jobs ADD CONSTRAINT jobs_jobs_job_type_v114_check
    CHECK (job_type IN ('script_run','workflow_run','application_build'));
CREATE UNIQUE INDEX jobs_application_build_identity ON jobs.jobs(target_id)
    WHERE job_type='application_build';
CREATE INDEX jobs_application_build_ready ON jobs.jobs(execution_node_id,status,next_attempt_at,lease_expires_at)
    WHERE job_type='application_build' AND NOT manual_action_required;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM jobs.jobs WHERE job_type='application_build') THEN
        RAISE EXCEPTION 'retained application build history requires compatible readers; down migration refused';
    END IF;
END $$;
-- +goose StatementEnd
DROP INDEX jobs.jobs_application_build_identity;
DROP INDEX jobs.jobs_application_build_ready;
ALTER TABLE jobs.jobs DROP CONSTRAINT jobs_jobs_job_type_v114_check;
ALTER TABLE jobs.jobs ADD CONSTRAINT jobs_jobs_job_type_v034_check
    CHECK (job_type IN ('script_run','workflow_run'));
