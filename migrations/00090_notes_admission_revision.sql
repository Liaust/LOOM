-- +goose Up
-- Metadata can change without a new content version. An older consumer must
-- not acknowledge a later delivery of that same version, even after reinsert.
CREATE SEQUENCE knowledge.source_admission_revision;
ALTER TABLE knowledge.pending_source_admissions
 ADD COLUMN revision bigint NOT NULL DEFAULT nextval('knowledge.source_admission_revision');

-- +goose Down
ALTER TABLE knowledge.pending_source_admissions DROP COLUMN revision;
DROP SEQUENCE knowledge.source_admission_revision;
