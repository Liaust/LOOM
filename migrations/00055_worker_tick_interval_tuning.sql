-- +goose Up
-- Reduce idle worker bookkeeping churn. The previous 5-15 second policies
-- were useful during early development, but they generated hundreds of
-- thousands of routine worker/event rows per day on an otherwise idle node.

WITH policy(worker_kind, interval_seconds) AS (
    VALUES
        ('automation_dispatcher', 60),
        ('automation_scheduler', 60),
        ('direct_event_ingest', 60),
        ('indexer_text', 60),
        ('job_runner', 60),
        ('knowledge_indexer', 60),
        ('main_documents_import', 300),
        ('policy_expiry', 300),
        ('realtime_expiry', 60)
)
UPDATE workers.worker_kinds AS kind
SET default_tick_policy_json = jsonb_set(
        kind.default_tick_policy_json::jsonb,
        '{interval_seconds}',
        to_jsonb(policy.interval_seconds),
        true
    ),
    updated_at = now()
FROM policy
WHERE kind.worker_kind = policy.worker_kind
  AND kind.default_tick_policy_json::jsonb ->> 'mode' = 'interval';

WITH policy(worker_key, interval_seconds) AS (
    VALUES
        ('main.automation_dispatcher', 60),
        ('main.automation_scheduler', 60),
        ('main.direct_event_ingest', 60),
        ('main.indexer_text', 60),
        ('main.job_runner', 60),
        ('main.knowledge_indexer', 60),
        ('main.main_documents_import', 300),
        ('main.policy_expiry', 300),
        ('main.realtime_expiry', 60)
)
UPDATE workers.worker_instances AS instance
SET tick_policy_json = jsonb_set(
        instance.tick_policy_json::jsonb,
        '{interval_seconds}',
        to_jsonb(policy.interval_seconds),
        true
    ),
    updated_at = now()
FROM policy
WHERE instance.worker_key = policy.worker_key
  AND instance.tick_policy_json::jsonb ->> 'mode' = 'interval';

WITH config(worker_key, idle_seconds, active_seconds) AS (
    VALUES
        ('main.automation_dispatcher', 60, 1),
        ('main.automation_scheduler', 60, 2),
        ('main.direct_event_ingest', 60, 1),
        ('main.job_runner', 60, 2),
        ('main.main_documents_import', 300, 5)
)
UPDATE workers.worker_instances AS instance
SET config_json = jsonb_set(
        jsonb_set(
            instance.config_json::jsonb,
            '{idle_tick_interval_seconds}',
            to_jsonb(config.idle_seconds),
            true
        ),
        '{active_tick_interval_seconds}',
        to_jsonb(config.active_seconds),
        true
    ),
    updated_at = now()
FROM config
WHERE instance.worker_key = config.worker_key;

-- +goose Down
WITH policy(worker_kind, interval_seconds) AS (
    VALUES
        ('automation_dispatcher', 5),
        ('automation_scheduler', 30),
        ('direct_event_ingest', 5),
        ('indexer_text', 10),
        ('job_runner', 15),
        ('knowledge_indexer', 15),
        ('main_documents_import', 15),
        ('policy_expiry', 60),
        ('realtime_expiry', 5)
)
UPDATE workers.worker_kinds AS kind
SET default_tick_policy_json = jsonb_set(
        kind.default_tick_policy_json::jsonb,
        '{interval_seconds}',
        to_jsonb(policy.interval_seconds),
        true
    ),
    updated_at = now()
FROM policy
WHERE kind.worker_kind = policy.worker_kind
  AND kind.default_tick_policy_json::jsonb ->> 'mode' = 'interval';

WITH policy(worker_key, interval_seconds) AS (
    VALUES
        ('main.automation_dispatcher', 5),
        ('main.automation_scheduler', 30),
        ('main.direct_event_ingest', 5),
        ('main.indexer_text', 10),
        ('main.job_runner', 15),
        ('main.knowledge_indexer', 15),
        ('main.main_documents_import', 15),
        ('main.policy_expiry', 60),
        ('main.realtime_expiry', 5)
)
UPDATE workers.worker_instances AS instance
SET tick_policy_json = jsonb_set(
        instance.tick_policy_json::jsonb,
        '{interval_seconds}',
        to_jsonb(policy.interval_seconds),
        true
    ),
    updated_at = now()
FROM policy
WHERE instance.worker_key = policy.worker_key
  AND instance.tick_policy_json::jsonb ->> 'mode' = 'interval';

WITH config(worker_key, idle_seconds, active_seconds) AS (
    VALUES
        ('main.automation_dispatcher', 5, 1),
        ('main.automation_scheduler', 30, 2),
        ('main.direct_event_ingest', 5, 1),
        ('main.job_runner', 15, 2),
        ('main.main_documents_import', 15, 5)
)
UPDATE workers.worker_instances AS instance
SET config_json = jsonb_set(
        jsonb_set(
            instance.config_json::jsonb,
            '{idle_tick_interval_seconds}',
            to_jsonb(config.idle_seconds),
            true
        ),
        '{active_tick_interval_seconds}',
        to_jsonb(config.active_seconds),
        true
    ),
    updated_at = now()
FROM config
WHERE instance.worker_key = config.worker_key;
