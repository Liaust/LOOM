-- +goose Up
-- Reuse the declaration journal and native automation schedules, not a new ledger.
ALTER TABLE projects.declaration_action_receipts DROP CONSTRAINT declaration_action_receipts_owner_check;
ALTER TABLE projects.declaration_action_receipts ADD CONSTRAINT declaration_action_receipts_owner_check
 CHECK (owner IN ('projects','knowledge','backupcontracts','serviceregistry','notesprojection','provenance','automation'));
ALTER TABLE projects.declaration_owner_receipts DROP CONSTRAINT declaration_owner_receipts_owner_check;
ALTER TABLE projects.declaration_owner_receipts ADD CONSTRAINT declaration_owner_receipts_owner_check
 CHECK (owner IN ('projects','knowledge','backupcontracts','automation'));

-- +goose Down
-- Existing receipts deliberately prevent rollback instead of dropping history.
ALTER TABLE projects.declaration_owner_receipts DROP CONSTRAINT declaration_owner_receipts_owner_check;
ALTER TABLE projects.declaration_owner_receipts ADD CONSTRAINT declaration_owner_receipts_owner_check
 CHECK (owner IN ('projects','knowledge','backupcontracts'));
ALTER TABLE projects.declaration_action_receipts DROP CONSTRAINT declaration_action_receipts_owner_check;
ALTER TABLE projects.declaration_action_receipts ADD CONSTRAINT declaration_action_receipts_owner_check
 CHECK (owner IN ('projects','knowledge','backupcontracts','serviceregistry','notesprojection','provenance'));
