-- +goose Up
-- Reuse the direct-event queue; only explicit project subscriptions are selected.
-- +goose StatementBegin
CREATE FUNCTION automation.queue_hermes_internal_event() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE
    selected record;
    source_project text;
    body jsonb;
BEGIN
    IF NEW.event_type NOT IN ('job.failed', 'job.timed_out') OR NEW.job_id IS NULL THEN
        RETURN NEW;
    END IF;
    SELECT p.project_id INTO source_project
      FROM jobs.jobs j JOIN projects.projects p ON p.project_scope_id = j.scope_id
     WHERE j.job_id = NEW.job_id;
    IF source_project IS NULL THEN RETURN NEW; END IF;
    body := jsonb_build_object('event', jsonb_build_object(
        'event_id', NEW.event_id, 'event_type', NEW.event_type,
        'project_id', source_project, 'job_id', NEW.job_id,
        'created_at', NEW.created_at));
    FOR selected IN
        SELECT e.* FROM automation.direct_event_endpoints e
        JOIN automation.automations a USING (automation_id)
        JOIN automation.integrations i ON i.integration_id = e.integration_id
        WHERE e.status = 'active' AND a.status = 'active' AND i.status = 'active'
          AND a.project_id = source_project
          AND e.metadata_json->'hermes_internal_events' ? NEW.event_type
          AND a.target_profile_json->>'capability_ref' = 'main@system.hermes.event'
          AND (i.allowed_projects_json = '[]'::jsonb OR i.allowed_projects_json ? source_project)
    LOOP
        INSERT INTO automation.direct_events (
            direct_event_id, endpoint_id, integration_id, automation_id, status,
            external_event_id, idempotency_key, request_method, request_path,
            payload_hash, raw_body_json, metadata_json)
        VALUES ('direct_event_' || md5(NEW.event_id || ':' || selected.endpoint_id),
            selected.endpoint_id, selected.integration_id, selected.automation_id, 'accepted',
            NEW.event_id, 'loom-event:' || NEW.event_id, 'INTERNAL', selected.endpoint_path,
            'sha256:' || encode(sha256(convert_to(body::text, 'UTF8')), 'hex'), body,
            jsonb_build_object('source', 'loom_internal_event', 'event_id', NEW.event_id))
        ON CONFLICT (endpoint_id, idempotency_key) WHERE idempotency_key <> '' DO NOTHING;
        IF FOUND THEN PERFORM pg_notify('loom_worker_wake', 'direct_event_ingest'); END IF;
    END LOOP;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER queue_hermes_internal_event AFTER INSERT ON events.events
FOR EACH ROW EXECUTE FUNCTION automation.queue_hermes_internal_event();

-- +goose Down
DROP TRIGGER IF EXISTS queue_hermes_internal_event ON events.events;
DROP FUNCTION IF EXISTS automation.queue_hermes_internal_event();
