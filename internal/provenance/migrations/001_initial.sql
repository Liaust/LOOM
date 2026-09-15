CREATE SCHEMA IF NOT EXISTS provenance;

CREATE OR REPLACE FUNCTION provenance.reject_ledger_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'append-only table: %.%', TG_TABLE_SCHEMA, TG_TABLE_NAME
        USING ERRCODE = '55000';
END;
$$;

CREATE TABLE provenance.candidates (
    id UUID PRIMARY KEY,
    schema_version TEXT NOT NULL CHECK (schema_version = '1.0'),
    state TEXT NOT NULL CHECK (state = 'pending'),
    domain TEXT NOT NULL CHECK (btrim(domain) <> ''),
    visibility TEXT NOT NULL CHECK (btrim(visibility) <> ''),
    record_kind TEXT NOT NULL CHECK (btrim(record_kind) <> ''),
    claim TEXT NOT NULL CHECK (btrim(claim) <> ''),
    record_context TEXT NOT NULL CHECK (btrim(record_context) <> ''),
    assertion_posture TEXT NOT NULL CHECK (btrim(assertion_posture) <> ''),
    producer_id TEXT NOT NULL CHECK (btrim(producer_id) <> ''),
    registered_at TIMESTAMPTZ NOT NULL,
    submitted_json JSONB NOT NULL,
    payload_json JSONB NOT NULL
);

CREATE TABLE provenance.source_references (
    id UUID PRIMARY KEY,
    candidate_id UUID REFERENCES provenance.candidates(id),
    schema_version TEXT NOT NULL CHECK (schema_version = '1.0'),
    source_kind TEXT NOT NULL CHECK (btrim(source_kind) <> ''),
    status TEXT NOT NULL CHECK (status IN ('resolved', 'unresolved')),
    verification_posture TEXT NOT NULL CHECK (
        verification_posture IN ('content_verified', 'locator_verified', 'unverified', 'unknown')
    ),
    resolver_name TEXT NOT NULL CHECK (btrim(resolver_name) <> ''),
    resolver_version TEXT NOT NULL CHECK (btrim(resolver_version) <> ''),
    resolution_at TIMESTAMPTZ NOT NULL,
    canonical_locator TEXT,
    version_address TEXT,
    content_digest TEXT,
    source_context TEXT,
    gap_reason TEXT,
    submitted_json JSONB NOT NULL,
    payload_json JSONB NOT NULL,
    CHECK (
        (status = 'resolved' AND canonical_locator IS NOT NULL AND btrim(canonical_locator) <> '' AND gap_reason IS NULL)
        OR
        (status = 'unresolved' AND gap_reason IS NOT NULL AND btrim(gap_reason) <> '')
    ),
    CHECK (
        verification_posture = 'unknown'
        OR (verification_posture = 'content_verified' AND status = 'resolved' AND content_digest IS NOT NULL AND btrim(content_digest) <> '')
        OR (verification_posture = 'locator_verified' AND status = 'resolved')
        OR (verification_posture = 'unverified' AND status = 'unresolved' AND canonical_locator IS NULL AND content_digest IS NULL)
    )
);

CREATE TABLE provenance.records (
    id UUID PRIMARY KEY,
    schema_version TEXT NOT NULL CHECK (schema_version = '1.0'),
    claim TEXT NOT NULL CHECK (btrim(claim) <> ''),
    record_kind TEXT NOT NULL CHECK (btrim(record_kind) <> ''),
    record_context TEXT NOT NULL CHECK (btrim(record_context) <> ''),
    ambiguity_json JSONB NOT NULL,
    modality TEXT,
    domain TEXT NOT NULL CHECK (btrim(domain) <> ''),
    visibility TEXT NOT NULL CHECK (btrim(visibility) <> ''),
    assertion_posture TEXT NOT NULL CHECK (btrim(assertion_posture) <> ''),
    source_actor_id TEXT,
    artifact_author_id TEXT,
    approving_actor_id TEXT,
    observed_at TIMESTAMPTZ,
    valid_from TIMESTAMPTZ,
    valid_until TIMESTAMPTZ,
    anchors_json JSONB,
    temporal_json JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    payload_json JSONB NOT NULL,
    CHECK (valid_from IS NULL OR valid_until IS NULL OR valid_until >= valid_from)
);

CREATE TABLE provenance.record_sources (
    record_id UUID NOT NULL REFERENCES provenance.records(id),
    source_reference_id UUID NOT NULL REFERENCES provenance.source_references(id),
    linked_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (record_id, source_reference_id)
);

CREATE TABLE provenance.record_producers (
    id BIGSERIAL PRIMARY KEY,
    record_id UUID NOT NULL REFERENCES provenance.records(id),
    candidate_id UUID REFERENCES provenance.candidates(id),
    producer_id TEXT NOT NULL CHECK (btrim(producer_id) <> ''),
    producer_json JSONB NOT NULL,
    linked_at TIMESTAMPTZ NOT NULL,
    UNIQUE NULLS NOT DISTINCT (record_id, candidate_id, producer_id)
);

CREATE TABLE provenance.resolution_cases (
    id UUID PRIMARY KEY,
    schema_version TEXT NOT NULL CHECK (schema_version = '1.0'),
    issue TEXT NOT NULL CHECK (btrim(issue) <> ''),
    domain TEXT NOT NULL CHECK (btrim(domain) <> ''),
    visibility TEXT NOT NULL CHECK (btrim(visibility) <> ''),
    initial_status TEXT NOT NULL CHECK (btrim(initial_status) <> ''),
    created_at TIMESTAMPTZ NOT NULL,
    created_by_operation_id UUID,
    payload_json JSONB NOT NULL
);

CREATE TABLE provenance.case_members (
    id BIGSERIAL PRIMARY KEY,
    case_id UUID NOT NULL REFERENCES provenance.resolution_cases(id),
    member_type TEXT NOT NULL CHECK (member_type IN ('candidate', 'record')),
    candidate_id UUID REFERENCES provenance.candidates(id),
    record_id UUID REFERENCES provenance.records(id),
    role TEXT NOT NULL CHECK (btrim(role) <> ''),
    attached_at TIMESTAMPTZ NOT NULL,
    operation_id UUID,
    CHECK (
        (member_type = 'candidate' AND candidate_id IS NOT NULL AND record_id IS NULL)
        OR
        (member_type = 'record' AND record_id IS NOT NULL AND candidate_id IS NULL)
    ),
    UNIQUE NULLS NOT DISTINCT (case_id, member_type, candidate_id, record_id, role)
);

CREATE TABLE provenance.case_events (
    id UUID PRIMARY KEY,
    case_id UUID NOT NULL REFERENCES provenance.resolution_cases(id),
    schema_version TEXT NOT NULL CHECK (schema_version = '1.0'),
    event_type TEXT NOT NULL CHECK (btrim(event_type) <> ''),
    occurred_at TIMESTAMPTZ NOT NULL,
    producer_json JSONB NOT NULL,
    summary TEXT NOT NULL CHECK (btrim(summary) <> ''),
    outcome TEXT,
    evidence_json JSONB NOT NULL,
    operation_id UUID,
    payload_json JSONB NOT NULL
);

CREATE TABLE provenance.relationships (
    id UUID PRIMARY KEY,
    schema_version TEXT NOT NULL CHECK (schema_version = '1.0'),
    relationship_type TEXT NOT NULL CHECK (btrim(relationship_type) <> ''),
    from_record_id UUID NOT NULL REFERENCES provenance.records(id),
    to_record_id UUID NOT NULL REFERENCES provenance.records(id),
    resolution_case_id UUID REFERENCES provenance.resolution_cases(id),
    context TEXT,
    coverage_json JSONB,
    evidence_json JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    created_by_json JSONB NOT NULL,
    created_by_operation_id UUID,
    payload_json JSONB NOT NULL,
    CHECK (from_record_id <> to_record_id)
);

CREATE TABLE provenance.processing_runs (
    id UUID PRIMARY KEY,
    run_kind TEXT NOT NULL CHECK (btrim(run_kind) <> ''),
    schema_version TEXT NOT NULL CHECK (schema_version = '1.0'),
    input_hash TEXT NOT NULL CHECK (btrim(input_hash) <> ''),
    status TEXT NOT NULL CHECK (btrim(status) <> ''),
    actor_json JSONB NOT NULL,
    bounds_json JSONB,
    started_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    payload_json JSONB NOT NULL,
    CHECK (completed_at IS NULL OR completed_at >= started_at)
);

CREATE TABLE provenance.operation_history (
    id UUID PRIMARY KEY,
    processing_run_id UUID REFERENCES provenance.processing_runs(id),
    pack_id UUID,
    operation_type TEXT NOT NULL CHECK (btrim(operation_type) <> ''),
    schema_version TEXT NOT NULL CHECK (schema_version = '1.0'),
    producer_json JSONB NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    input_hash TEXT NOT NULL CHECK (btrim(input_hash) <> ''),
    result TEXT NOT NULL CHECK (btrim(result) <> ''),
    operation_json JSONB NOT NULL,
    result_json JSONB NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL
);

ALTER TABLE provenance.resolution_cases
    ADD CONSTRAINT resolution_cases_created_operation_fk
    FOREIGN KEY (created_by_operation_id) REFERENCES provenance.operation_history(id);
ALTER TABLE provenance.case_members
    ADD CONSTRAINT case_members_operation_fk
    FOREIGN KEY (operation_id) REFERENCES provenance.operation_history(id);
ALTER TABLE provenance.case_events
    ADD CONSTRAINT case_events_operation_fk
    FOREIGN KEY (operation_id) REFERENCES provenance.operation_history(id);
ALTER TABLE provenance.relationships
    ADD CONSTRAINT relationships_created_operation_fk
    FOREIGN KEY (created_by_operation_id) REFERENCES provenance.operation_history(id);

CREATE TABLE provenance.candidate_events (
    id BIGSERIAL PRIMARY KEY,
    candidate_id UUID NOT NULL REFERENCES provenance.candidates(id),
    event_type TEXT NOT NULL CHECK (btrim(event_type) <> ''),
    operation_id UUID NOT NULL REFERENCES provenance.operation_history(id),
    occurred_at TIMESTAMPTZ NOT NULL,
    payload_json JSONB NOT NULL,
    UNIQUE (candidate_id, operation_id)
);

CREATE TABLE provenance.record_events (
    id BIGSERIAL PRIMARY KEY,
    record_id UUID NOT NULL REFERENCES provenance.records(id),
    event_type TEXT NOT NULL CHECK (btrim(event_type) <> ''),
    operation_id UUID NOT NULL REFERENCES provenance.operation_history(id),
    occurred_at TIMESTAMPTZ NOT NULL,
    payload_json JSONB NOT NULL,
    UNIQUE (record_id, operation_id)
);

CREATE TABLE provenance.relationship_events (
    id BIGSERIAL PRIMARY KEY,
    relationship_id UUID NOT NULL REFERENCES provenance.relationships(id),
    event_type TEXT NOT NULL CHECK (btrim(event_type) <> ''),
    operation_id UUID NOT NULL REFERENCES provenance.operation_history(id),
    occurred_at TIMESTAMPTZ NOT NULL,
    payload_json JSONB NOT NULL,
    UNIQUE (relationship_id, operation_id)
);

DO $$
DECLARE
    table_name TEXT;
BEGIN
    FOREACH table_name IN ARRAY ARRAY[
        'candidates', 'source_references', 'records', 'record_sources',
        'record_producers', 'resolution_cases', 'case_members', 'case_events',
        'relationships', 'operation_history', 'candidate_events',
        'record_events', 'relationship_events'
    ]
    LOOP
        EXECUTE format(
            'CREATE TRIGGER reject_ledger_mutation BEFORE UPDATE OR DELETE ON provenance.%I FOR EACH ROW EXECUTE FUNCTION provenance.reject_ledger_mutation()',
            table_name
        );
    END LOOP;
END;
$$;
