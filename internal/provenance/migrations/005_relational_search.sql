-- Upstream 005 is a SQLite relational FTS projection. Ranked search is
-- explicitly deferred; these exact relationship/case indexes are sufficient
-- for bounded store navigation.
CREATE INDEX relationships_case_idx
    ON provenance.relationships(resolution_case_id, created_at, id)
    WHERE resolution_case_id IS NOT NULL;
CREATE INDEX case_members_candidate_idx
    ON provenance.case_members(candidate_id, attached_at, id)
    WHERE candidate_id IS NOT NULL;
CREATE INDEX case_members_record_idx
    ON provenance.case_members(record_id, attached_at, id)
    WHERE record_id IS NOT NULL;
