-- +goose Up

CREATE SCHEMA IF NOT EXISTS realtime;

CREATE TABLE IF NOT EXISTS realtime.topics (
    topic_id text PRIMARY KEY CHECK (topic_id LIKE 'topic_%'),
    topic_path text UNIQUE NOT NULL CHECK (length(topic_path) > 0),
    display_name text NOT NULL DEFAULT '',
    scope_id text NULL REFERENCES scopes.scopes(scope_id),
    owner_actor_id text NULL REFERENCES identity.actors(actor_id),
    created_by_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    publisher_policy_id text NULL REFERENCES policy.decisions(policy_decision_id),
    subscriber_policy_id text NULL REFERENCES policy.decisions(policy_decision_id),
    retention_mode text NOT NULL DEFAULT 'retain_bounded' CHECK (
        retention_mode IN ('retain_latest', 'retain_bounded', 'durable_event_only')
    ),
    delivery_class text NOT NULL DEFAULT 'polling' CHECK (
        delivery_class IN ('polling', 'actor_inbox', 'main_outbox')
    ),
    ordering_mode text NOT NULL DEFAULT 'topic_sequence' CHECK (
        ordering_mode IN ('topic_sequence')
    ),
    status text NOT NULL DEFAULT 'active' CHECK (
        status IN ('active', 'closed', 'archived', 'failed')
    ),
    last_sequence bigint NOT NULL DEFAULT 0 CHECK (last_sequence >= 0),
    latest_payload_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(latest_payload_json) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    closed_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS realtime_topics_scope_status_idx ON realtime.topics (scope_id, status);
CREATE INDEX IF NOT EXISTS realtime_topics_created_by_idx ON realtime.topics (created_by_actor_id);
CREATE INDEX IF NOT EXISTS realtime_topics_updated_at_desc_idx ON realtime.topics (updated_at DESC);

CREATE TABLE IF NOT EXISTS realtime.topic_publications (
    topic_publication_id text PRIMARY KEY CHECK (topic_publication_id LIKE 'topic_publication_%'),
    topic_id text NOT NULL REFERENCES realtime.topics(topic_id) ON DELETE CASCADE,
    sequence bigint NOT NULL CHECK (sequence > 0),
    publisher_actor_id text NULL REFERENCES identity.actors(actor_id),
    publisher_node_id text NULL REFERENCES nodes.nodes(node_id),
    publisher_provider_id text NULL REFERENCES capabilities.providers(provider_id),
    message_type text NOT NULL CHECK (length(message_type) > 0),
    payload_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload_json) = 'object'),
    payload_schema_ref text NOT NULL DEFAULT '',
    correlation_id text NOT NULL DEFAULT '',
    causation_ref text NOT NULL DEFAULT '',
    authorization_ref text NOT NULL DEFAULT '',
    policy_decision_id text NULL REFERENCES policy.decisions(policy_decision_id),
    durability_mode text NOT NULL DEFAULT 'retained' CHECK (
        durability_mode IN ('retained', 'latest_only', 'durable_event_only')
    ),
    created_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    UNIQUE (topic_id, sequence)
);

CREATE INDEX IF NOT EXISTS realtime_topic_publications_topic_sequence_idx ON realtime.topic_publications (topic_id, sequence);
CREATE INDEX IF NOT EXISTS realtime_topic_publications_topic_created_idx ON realtime.topic_publications (topic_id, created_at DESC);
CREATE INDEX IF NOT EXISTS realtime_topic_publications_message_type_idx ON realtime.topic_publications (message_type);
CREATE INDEX IF NOT EXISTS realtime_topic_publications_correlation_idx ON realtime.topic_publications (correlation_id);

CREATE TABLE IF NOT EXISTS realtime.subscriptions (
    subscription_id text PRIMARY KEY CHECK (subscription_id LIKE 'subscription_%'),
    topic_id text NOT NULL REFERENCES realtime.topics(topic_id) ON DELETE CASCADE,
    source_kind text NOT NULL DEFAULT 'topic' CHECK (source_kind IN ('topic')),
    source_ref text NOT NULL,
    subscriber_actor_id text NULL REFERENCES identity.actors(actor_id),
    subscriber_node_id text NULL REFERENCES nodes.nodes(node_id),
    scope_id text NULL REFERENCES scopes.scopes(scope_id),
    filter_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(filter_json) = 'object'),
    cursor_sequence bigint NOT NULL DEFAULT 0 CHECK (cursor_sequence >= 0),
    last_acknowledged_sequence bigint NOT NULL DEFAULT 0 CHECK (last_acknowledged_sequence >= 0),
    delivery_target_kind text NOT NULL DEFAULT 'polling' CHECK (
        delivery_target_kind IN ('polling', 'actor_inbox', 'main_outbox')
    ),
    delivery_target_ref text NOT NULL DEFAULT '',
    delivery_mode text NOT NULL DEFAULT 'polling' CHECK (
        delivery_mode IN ('polling', 'actor_inbox', 'main_outbox')
    ),
    delivery_class text NOT NULL DEFAULT 'polling' CHECK (
        delivery_class IN ('polling', 'actor_inbox', 'main_outbox')
    ),
    authorization_ref text NOT NULL DEFAULT '',
    policy_decision_id text NULL REFERENCES policy.decisions(policy_decision_id),
    status text NOT NULL DEFAULT 'active' CHECK (
        status IN ('active', 'cancelled', 'expired', 'failed')
    ),
    expires_at timestamptz NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    last_delivered_at timestamptz NULL,
    last_acknowledged_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS realtime_subscriptions_topic_status_idx ON realtime.subscriptions (topic_id, status);
CREATE INDEX IF NOT EXISTS realtime_subscriptions_actor_idx ON realtime.subscriptions (subscriber_actor_id);
CREATE INDEX IF NOT EXISTS realtime_subscriptions_node_idx ON realtime.subscriptions (subscriber_node_id);
CREATE INDEX IF NOT EXISTS realtime_subscriptions_expires_at_idx ON realtime.subscriptions (expires_at);

CREATE TABLE IF NOT EXISTS realtime.presence (
    presence_id text PRIMARY KEY CHECK (presence_id LIKE 'presence_%'),
    subject_kind text NOT NULL CHECK (
        subject_kind IN ('node', 'actor', 'provider', 'module_service', 'job_runner')
    ),
    subject_ref text NOT NULL CHECK (length(subject_ref) > 0),
    node_id text NULL REFERENCES nodes.nodes(node_id),
    state text NOT NULL DEFAULT 'unknown' CHECK (
        state IN ('online', 'recently_seen', 'offline', 'degraded', 'unknown', 'stale', 'quarantined', 'revoked')
    ),
    source_kind text NOT NULL DEFAULT 'heartbeat' CHECK (
        source_kind IN ('heartbeat', 'manual', 'provider_status', 'runner_status', 'expiry_worker')
    ),
    source_ref text NOT NULL DEFAULT '',
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    confidence numeric NULL CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1)),
    status_detail text NOT NULL DEFAULT '',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS realtime_presence_subject_node_idx
    ON realtime.presence (subject_kind, subject_ref, COALESCE(node_id, ''));
CREATE INDEX IF NOT EXISTS realtime_presence_state_idx ON realtime.presence (state);
CREATE INDEX IF NOT EXISTS realtime_presence_expires_at_idx ON realtime.presence (expires_at);
CREATE INDEX IF NOT EXISTS realtime_presence_updated_at_desc_idx ON realtime.presence (updated_at DESC);

CREATE TABLE IF NOT EXISTS realtime.notifications (
    notification_id text PRIMARY KEY CHECK (notification_id LIKE 'notification_%'),
    target_kind text NOT NULL CHECK (
        target_kind IN ('actor', 'node', 'device', 'session', 'module_service', 'approval_queue')
    ),
    target_ref text NOT NULL CHECK (length(target_ref) > 0),
    source_kind text NOT NULL CHECK (
        source_kind IN ('policy', 'route', 'capability_call', 'job', 'sync', 'node', 'provider', 'module', 'system')
    ),
    source_ref text NOT NULL DEFAULT '',
    scope_id text NULL REFERENCES scopes.scopes(scope_id),
    category text NOT NULL CHECK (
        category IN ('approval', 'security', 'job', 'workflow', 'sync', 'node', 'provider', 'module', 'project', 'reminder', 'message')
    ),
    priority text NOT NULL DEFAULT 'normal' CHECK (
        priority IN ('low', 'normal', 'high', 'urgent', 'critical')
    ),
    summary text NOT NULL CHECK (length(summary) > 0),
    payload_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload_json) = 'object'),
    authorization_ref text NOT NULL DEFAULT '',
    policy_decision_id text NULL REFERENCES policy.decisions(policy_decision_id),
    approval_id text NULL REFERENCES policy.approvals(approval_id),
    route_id text NULL REFERENCES routing.routes(route_id),
    capability_call_id text NULL REFERENCES routing.capability_calls(capability_call_id),
    status text NOT NULL DEFAULT 'created' CHECK (
        status IN ('created', 'routed', 'delivered', 'acknowledged', 'dismissed', 'expired', 'failed', 'cancelled')
    ),
    expires_at timestamptz NULL,
    acknowledged_at timestamptz NULL,
    dismissed_at timestamptz NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS realtime_notifications_target_status_idx ON realtime.notifications (target_kind, target_ref, status);
CREATE INDEX IF NOT EXISTS realtime_notifications_category_status_idx ON realtime.notifications (category, status);
CREATE INDEX IF NOT EXISTS realtime_notifications_priority_idx ON realtime.notifications (priority);
CREATE INDEX IF NOT EXISTS realtime_notifications_approval_idx ON realtime.notifications (approval_id);
CREATE INDEX IF NOT EXISTS realtime_notifications_route_idx ON realtime.notifications (route_id);
CREATE INDEX IF NOT EXISTS realtime_notifications_call_idx ON realtime.notifications (capability_call_id);
CREATE INDEX IF NOT EXISTS realtime_notifications_expires_at_idx ON realtime.notifications (expires_at);
CREATE INDEX IF NOT EXISTS realtime_notifications_created_at_desc_idx ON realtime.notifications (created_at DESC);

CREATE TABLE IF NOT EXISTS realtime.notification_deliveries (
    notification_delivery_id text PRIMARY KEY CHECK (notification_delivery_id LIKE 'notification_delivery_%'),
    notification_id text NOT NULL REFERENCES realtime.notifications(notification_id) ON DELETE CASCADE,
    target_kind text NOT NULL CHECK (
        target_kind IN ('actor_inbox', 'main_outbox', 'polling', 'node_channel')
    ),
    target_ref text NOT NULL DEFAULT '',
    delivery_mode text NOT NULL DEFAULT 'polling' CHECK (
        delivery_mode IN ('actor_inbox', 'main_outbox', 'polling')
    ),
    status text NOT NULL DEFAULT 'pending' CHECK (
        status IN ('pending', 'delivered', 'acknowledged', 'failed', 'cancelled', 'expired')
    ),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at timestamptz NULL,
    last_attempt_at timestamptz NULL,
    delivered_at timestamptz NULL,
    acknowledged_at timestamptz NULL,
    error_code text NOT NULL DEFAULT '',
    error_message text NOT NULL DEFAULT '',
    communication_message_id text NULL REFERENCES communication.messages(communication_message_id),
    created_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS realtime_notification_deliveries_notification_idx ON realtime.notification_deliveries (notification_id);
CREATE INDEX IF NOT EXISTS realtime_notification_deliveries_target_status_idx ON realtime.notification_deliveries (target_kind, target_ref, status);
CREATE INDEX IF NOT EXISTS realtime_notification_deliveries_next_attempt_idx ON realtime.notification_deliveries (next_attempt_at);

CREATE TABLE IF NOT EXISTS realtime.progress_feeds (
    progress_feed_id text PRIMARY KEY CHECK (progress_feed_id LIKE 'progress_feed_%'),
    source_kind text NOT NULL CHECK (
        source_kind IN ('job', 'sync', 'capability_call', 'route', 'private_backup', 'workflow_run', 'script_run', 'transfer', 'index_worker', 'backup_worker', 'connector_provider')
    ),
    source_ref text NOT NULL CHECK (length(source_ref) > 0),
    scope_id text NULL REFERENCES scopes.scopes(scope_id),
    topic_id text NULL REFERENCES realtime.topics(topic_id),
    current_status text NOT NULL DEFAULT 'pending' CHECK (
        current_status IN ('pending', 'running', 'waiting', 'blocked', 'succeeded', 'failed', 'cancelled', 'warning', 'closed')
    ),
    current_stage text NOT NULL DEFAULT '',
    current_message text NOT NULL DEFAULT '',
    current_value numeric NULL,
    total_value numeric NULL,
    last_sequence bigint NOT NULL DEFAULT 0 CHECK (last_sequence >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    closed_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    UNIQUE (source_kind, source_ref)
);

CREATE INDEX IF NOT EXISTS realtime_progress_feeds_source_idx ON realtime.progress_feeds (source_kind, source_ref);
CREATE INDEX IF NOT EXISTS realtime_progress_feeds_status_idx ON realtime.progress_feeds (current_status);
CREATE INDEX IF NOT EXISTS realtime_progress_feeds_updated_at_desc_idx ON realtime.progress_feeds (updated_at DESC);

CREATE TABLE IF NOT EXISTS realtime.progress_updates (
    progress_update_id text PRIMARY KEY CHECK (progress_update_id LIKE 'progress_update_%'),
    progress_feed_id text NOT NULL REFERENCES realtime.progress_feeds(progress_feed_id) ON DELETE CASCADE,
    source_kind text NOT NULL,
    source_ref text NOT NULL,
    sequence bigint NOT NULL CHECK (sequence > 0),
    stage text NOT NULL DEFAULT '',
    status text NOT NULL CHECK (
        status IN ('pending', 'running', 'waiting', 'blocked', 'succeeded', 'failed', 'cancelled', 'warning', 'closed')
    ),
    message text NOT NULL DEFAULT '',
    payload_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload_json) = 'object'),
    progress_value numeric NULL,
    total_value numeric NULL,
    severity text NOT NULL DEFAULT 'normal' CHECK (
        severity IN ('debug', 'info', 'normal', 'warning', 'error')
    ),
    correlation_id text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    UNIQUE (progress_feed_id, sequence)
);

CREATE INDEX IF NOT EXISTS realtime_progress_updates_feed_sequence_idx ON realtime.progress_updates (progress_feed_id, sequence);
CREATE INDEX IF NOT EXISTS realtime_progress_updates_source_created_idx ON realtime.progress_updates (source_kind, source_ref, created_at DESC);
CREATE INDEX IF NOT EXISTS realtime_progress_updates_correlation_idx ON realtime.progress_updates (correlation_id);

CREATE TABLE IF NOT EXISTS realtime.leases (
    lease_id text PRIMARY KEY CHECK (lease_id LIKE 'lease_%'),
    resource_kind text NOT NULL CHECK (length(resource_kind) > 0),
    resource_ref text NOT NULL CHECK (length(resource_ref) > 0),
    holder_kind text NOT NULL CHECK (
        holder_kind IN ('actor', 'node', 'job', 'route', 'capability_call', 'provider', 'system')
    ),
    holder_ref text NOT NULL CHECK (length(holder_ref) > 0),
    holder_actor_id text NULL REFERENCES identity.actors(actor_id),
    node_id text NULL REFERENCES nodes.nodes(node_id),
    provider_id text NULL REFERENCES capabilities.providers(provider_id),
    scope_id text NULL REFERENCES scopes.scopes(scope_id),
    route_id text NULL REFERENCES routing.routes(route_id),
    capability_call_id text NULL REFERENCES routing.capability_calls(capability_call_id),
    authorization_ref text NOT NULL DEFAULT '',
    policy_decision_id text NULL REFERENCES policy.decisions(policy_decision_id),
    approval_id text NULL REFERENCES policy.approvals(approval_id),
    grant_id text NULL REFERENCES policy.grants(grant_id),
    lease_mode text NOT NULL CHECK (
        lease_mode IN ('read', 'write', 'control', 'exclusive', 'shared')
    ),
    status text NOT NULL DEFAULT 'granted' CHECK (
        status IN ('granted', 'released', 'expired', 'revoked', 'failed')
    ),
    starts_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    released_at timestamptz NULL,
    release_reason text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    CHECK (expires_at > starts_at)
);

CREATE INDEX IF NOT EXISTS realtime_leases_resource_status_idx ON realtime.leases (resource_kind, resource_ref, status);
CREATE INDEX IF NOT EXISTS realtime_leases_holder_idx ON realtime.leases (holder_kind, holder_ref);
CREATE INDEX IF NOT EXISTS realtime_leases_expires_at_idx ON realtime.leases (expires_at);
CREATE INDEX IF NOT EXISTS realtime_leases_actor_idx ON realtime.leases (holder_actor_id);
CREATE INDEX IF NOT EXISTS realtime_leases_node_idx ON realtime.leases (node_id);
CREATE INDEX IF NOT EXISTS realtime_leases_provider_idx ON realtime.leases (provider_id);

-- +goose Down

DROP TABLE IF EXISTS realtime.leases;
DROP TABLE IF EXISTS realtime.progress_updates;
DROP TABLE IF EXISTS realtime.progress_feeds;
DROP TABLE IF EXISTS realtime.notification_deliveries;
DROP TABLE IF EXISTS realtime.notifications;
DROP TABLE IF EXISTS realtime.presence;
DROP TABLE IF EXISTS realtime.subscriptions;
DROP TABLE IF EXISTS realtime.topic_publications;
DROP TABLE IF EXISTS realtime.topics;

DROP SCHEMA IF EXISTS realtime;
