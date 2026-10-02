-- +goose Up

-- These authority tables are small but gate reads of the entire Notes corpus.
-- The default 50-change threshold can leave them unanalyzed indefinitely.
ALTER TABLE knowledge.notes_current_custody SET (autovacuum_analyze_threshold = 0, autovacuum_analyze_scale_factor = 0.05);
ALTER TABLE knowledge.notes_custody_transitions SET (autovacuum_analyze_threshold = 0, autovacuum_analyze_scale_factor = 0.05);
ALTER TABLE knowledge.notes_custody_projection_receipts SET (autovacuum_analyze_threshold = 0, autovacuum_analyze_scale_factor = 0.05);
ALTER TABLE storage.workspace_lifecycle_events SET (autovacuum_analyze_threshold = 0, autovacuum_analyze_scale_factor = 0.05);
ALTER TABLE storage.workspace_archive_operations SET (autovacuum_analyze_threshold = 0, autovacuum_analyze_scale_factor = 0.05);

ANALYZE knowledge.notes_current_custody;
ANALYZE knowledge.notes_custody_transitions;
ANALYZE knowledge.notes_custody_projection_receipts;
ANALYZE storage.workspace_lifecycle_events;
ANALYZE storage.workspace_archive_operations;

-- +goose Down

ALTER TABLE knowledge.notes_current_custody RESET (autovacuum_analyze_threshold, autovacuum_analyze_scale_factor);
ALTER TABLE knowledge.notes_custody_transitions RESET (autovacuum_analyze_threshold, autovacuum_analyze_scale_factor);
ALTER TABLE knowledge.notes_custody_projection_receipts RESET (autovacuum_analyze_threshold, autovacuum_analyze_scale_factor);
ALTER TABLE storage.workspace_lifecycle_events RESET (autovacuum_analyze_threshold, autovacuum_analyze_scale_factor);
ALTER TABLE storage.workspace_archive_operations RESET (autovacuum_analyze_threshold, autovacuum_analyze_scale_factor);
