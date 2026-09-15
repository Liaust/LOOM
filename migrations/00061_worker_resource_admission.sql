-- +goose Up

CREATE TABLE IF NOT EXISTS workers.resource_capacities (
    worker_resource_capacity_id text PRIMARY KEY CHECK (worker_resource_capacity_id LIKE 'worker_resource_capacity_%'),
    resource_key text NOT NULL UNIQUE CHECK (resource_key ~ '^[a-z][a-z0-9_:-]{0,127}$'),
    capacity integer NOT NULL CHECK (capacity > 0),
    enabled boolean NOT NULL DEFAULT true,
    policy_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(policy_json) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO workers.resource_capacities (
    worker_resource_capacity_id, resource_key, capacity, enabled, policy_json
)
VALUES (
    'worker_resource_capacity_knowledge_heavy',
    'knowledge_heavy',
    1,
    true,
    '{"schema_version":"worker_resource_capacity.policy.v1","enforcement":"durable_slot_lease"}'::jsonb
)
ON CONFLICT (resource_key) DO UPDATE
SET capacity = EXCLUDED.capacity,
    enabled = EXCLUDED.enabled,
    policy_json = EXCLUDED.policy_json,
    updated_at = now();

CREATE TABLE IF NOT EXISTS workers.resource_leases (
    worker_resource_lease_id text PRIMARY KEY CHECK (worker_resource_lease_id LIKE 'worker_resource_lease_%'),
    resource_key text NOT NULL REFERENCES workers.resource_capacities(resource_key) ON DELETE RESTRICT,
    slot_number integer NOT NULL CHECK (slot_number > 0),
    status text NOT NULL CHECK (status IN ('active', 'released', 'expired', 'cancelled')),
    generation bigint NOT NULL CHECK (generation > 0),
    holder_id text NOT NULL CHECK (holder_id <> ''),
    worker_instance_id text NULL REFERENCES workers.worker_instances(worker_instance_id) ON DELETE SET NULL,
    worker_run_id text NULL REFERENCES workers.worker_runs(worker_run_id) ON DELETE SET NULL,
    knowledge_pipeline_run_id text NULL REFERENCES knowledge.pipeline_runs(knowledge_pipeline_run_id) ON DELETE SET NULL,
    knowledge_pipeline_stage_run_id text NULL REFERENCES knowledge.pipeline_stage_runs(knowledge_pipeline_stage_run_id) ON DELETE SET NULL,
    acquired_at timestamptz NOT NULL,
    renewed_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    released_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    CHECK (expires_at > acquired_at)
);

CREATE UNIQUE INDEX IF NOT EXISTS resource_leases_active_slot_idx
ON workers.resource_leases (resource_key, slot_number)
WHERE status = 'active';

CREATE INDEX IF NOT EXISTS resource_leases_expiry_idx
ON workers.resource_leases (expires_at)
WHERE status = 'active';

CREATE INDEX IF NOT EXISTS resource_leases_holder_idx
ON workers.resource_leases (holder_id, status);

CREATE INDEX IF NOT EXISTS resource_leases_pipeline_idx
ON workers.resource_leases (knowledge_pipeline_run_id, knowledge_pipeline_stage_run_id)
WHERE knowledge_pipeline_run_id IS NOT NULL;

-- +goose Down

DROP TABLE IF EXISTS workers.resource_leases;
DELETE FROM workers.resource_capacities WHERE resource_key = 'knowledge_heavy';
DROP TABLE IF EXISTS workers.resource_capacities;
