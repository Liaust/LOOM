-- +goose Up
CREATE TABLE IF NOT EXISTS projects.project_schedule_registrations (
    project_schedule_registration_id text PRIMARY KEY CHECK (project_schedule_registration_id LIKE 'project_schedule_registration_%'),
    project_contract_registration_id text NOT NULL REFERENCES projects.project_contract_registrations(project_contract_registration_id) ON DELETE CASCADE,
    project_id text NOT NULL REFERENCES projects.projects(project_id) ON DELETE CASCADE,
    schedule_key text NOT NULL,
    backend_schedule_key text NOT NULL,
    schedule_folder text NOT NULL,
    schedule_manifest_path text NOT NULL,
    schedule_hash text NOT NULL DEFAULT '',
    input_path text NOT NULL DEFAULT '',
    input_hash text NOT NULL DEFAULT '',
    target_capability text NOT NULL DEFAULT '',
    automation_id text NULL REFERENCES automation.automations(automation_id),
    schedule_id text NULL REFERENCES automation.schedules(schedule_id),
    activation_status text NOT NULL CHECK (
        activation_status IN (
            'registered',
            'paused',
            'active',
            'disabled',
            'blocked',
            'stale'
        )
    ),
    last_activated_by_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    last_activated_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, schedule_key),
    UNIQUE (project_id, backend_schedule_key)
);

CREATE INDEX IF NOT EXISTS projects_schedule_registrations_registration_idx
ON projects.project_schedule_registrations (project_contract_registration_id);

CREATE INDEX IF NOT EXISTS projects_schedule_registrations_project_status_idx
ON projects.project_schedule_registrations (project_id, activation_status);

CREATE INDEX IF NOT EXISTS projects_schedule_registrations_schedule_idx
ON projects.project_schedule_registrations (schedule_id);

CREATE INDEX IF NOT EXISTS projects_schedule_registrations_backend_key_idx
ON projects.project_schedule_registrations (backend_schedule_key);

-- +goose Down
DROP TABLE IF EXISTS projects.project_schedule_registrations;
