-- Upstream 006 adds candidate lifecycle, replay, search projection, and
-- Archivist lease structures. Slice 1 ports only lifecycle/evidence/replay
-- store primitives; FTS and Archivist leases remain deferred.
CREATE TABLE provenance.candidate_evidence_links (
    id UUID PRIMARY KEY,
    candidate_id UUID NOT NULL REFERENCES provenance.candidates(id),
    source_reference_id UUID NOT NULL REFERENCES provenance.source_references(id),
    operation_id UUID NOT NULL REFERENCES provenance.operation_history(id),
    linked_at TIMESTAMPTZ NOT NULL,
    producer_json JSONB NOT NULL,
    payload_json JSONB NOT NULL,
    UNIQUE (candidate_id, source_reference_id)
);

CREATE TABLE provenance.candidate_lineage (
    id BIGSERIAL PRIMARY KEY,
    candidate_id UUID NOT NULL REFERENCES provenance.candidates(id),
    derived_from_candidate_id UUID NOT NULL REFERENCES provenance.candidates(id),
    registered_at TIMESTAMPTZ NOT NULL,
    UNIQUE (candidate_id, derived_from_candidate_id),
    CHECK (candidate_id <> derived_from_candidate_id)
);

CREATE TABLE provenance.registration_replays (
    replay_key TEXT PRIMARY KEY CHECK (btrim(replay_key) <> ''),
    workflow_key TEXT NOT NULL CHECK (btrim(workflow_key) <> ''),
    registration_digest TEXT NOT NULL CHECK (btrim(registration_digest) <> ''),
    producer_json JSONB NOT NULL,
    candidate_ids_json JSONB NOT NULL,
    receipts_json JSONB NOT NULL,
    registered_at TIMESTAMPTZ NOT NULL,
    payload_json JSONB NOT NULL,
    UNIQUE (workflow_key, registration_digest)
);

CREATE INDEX candidate_evidence_links_candidate_idx
    ON provenance.candidate_evidence_links(candidate_id, linked_at, id);
CREATE INDEX candidate_evidence_links_source_idx
    ON provenance.candidate_evidence_links(source_reference_id, linked_at, id);
CREATE INDEX candidate_lineage_parent_idx
    ON provenance.candidate_lineage(derived_from_candidate_id, registered_at, candidate_id);
CREATE INDEX registration_replays_workflow_idx
    ON provenance.registration_replays(workflow_key, registered_at, replay_key);

DO $$
DECLARE
    table_name TEXT;
BEGIN
    FOREACH table_name IN ARRAY ARRAY[
        'candidate_evidence_links', 'candidate_lineage', 'registration_replays'
    ]
    LOOP
        EXECUTE format(
            'CREATE TRIGGER reject_ledger_mutation BEFORE UPDATE OR DELETE ON provenance.%I FOR EACH ROW EXECUTE FUNCTION provenance.reject_ledger_mutation()',
            table_name
        );
    END LOOP;
END;
$$;
