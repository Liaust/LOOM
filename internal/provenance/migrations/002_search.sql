-- Upstream 002 adds SQLite FTS. Slice 1 deliberately ports only exact and
-- bounded store access, so PostgreSQL receives stable lookup indexes and no
-- ranked-search projection.
CREATE INDEX candidates_scope_registered_idx
    ON provenance.candidates(domain, visibility, registered_at, id);
CREATE INDEX source_references_candidate_idx
    ON provenance.source_references(candidate_id, resolution_at, id);
CREATE INDEX records_scope_created_idx
    ON provenance.records(domain, visibility, created_at, id);
CREATE INDEX records_kind_idx
    ON provenance.records(record_kind, assertion_posture);
CREATE INDEX record_sources_source_idx
    ON provenance.record_sources(source_reference_id, linked_at, record_id);
CREATE INDEX record_producers_record_idx
    ON provenance.record_producers(record_id, linked_at, id);
CREATE INDEX resolution_cases_scope_created_idx
    ON provenance.resolution_cases(domain, visibility, created_at, id);
CREATE INDEX case_members_case_idx
    ON provenance.case_members(case_id, attached_at, id);
CREATE INDEX case_events_case_time_idx
    ON provenance.case_events(case_id, occurred_at, id);
CREATE INDEX relationships_from_idx
    ON provenance.relationships(from_record_id, created_at, id);
CREATE INDEX relationships_to_idx
    ON provenance.relationships(to_record_id, created_at, id);
CREATE INDEX operation_history_pack_idx
    ON provenance.operation_history(pack_id, occurred_at, id);
CREATE INDEX candidate_events_candidate_idx
    ON provenance.candidate_events(candidate_id, occurred_at, id);
CREATE INDEX record_events_record_idx
    ON provenance.record_events(record_id, occurred_at, id);
CREATE INDEX relationship_events_relationship_idx
    ON provenance.relationship_events(relationship_id, occurred_at, id);
