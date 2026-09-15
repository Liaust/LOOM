-- Upstream 003 also contains Archivist checkpoints/review outcomes and
-- clarification delivery orchestration. Those are deferred. Evidence-only
-- registration remains part of the source/evidence store contract.
CREATE TABLE provenance.evidence_registrations (
    id UUID PRIMARY KEY,
    reconciliation_protocol_version TEXT NOT NULL CHECK (
        reconciliation_protocol_version = '2.0'
    ),
    source_reference_id UUID NOT NULL UNIQUE REFERENCES provenance.source_references(id),
    domain TEXT NOT NULL CHECK (btrim(domain) <> ''),
    visibility TEXT NOT NULL CHECK (btrim(visibility) <> ''),
    evidence_context TEXT NOT NULL CHECK (btrim(evidence_context) <> ''),
    producer_json JSONB NOT NULL,
    registered_at TIMESTAMPTZ NOT NULL,
    resolution_case_id UUID REFERENCES provenance.resolution_cases(id),
    clarification_id UUID,
    payload_json JSONB NOT NULL,
    CHECK (clarification_id IS NULL OR resolution_case_id IS NOT NULL)
);

CREATE INDEX evidence_registrations_scope_idx
    ON provenance.evidence_registrations(domain, visibility, registered_at, id);

CREATE TRIGGER reject_ledger_mutation
    BEFORE UPDATE OR DELETE ON provenance.evidence_registrations
    FOR EACH ROW EXECUTE FUNCTION provenance.reject_ledger_mutation();
