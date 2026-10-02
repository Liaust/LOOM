-- +goose Up
-- Hermes owns its native receipt; the existing declaration journal records
-- the adapter action just like the other typed declaration owners.
ALTER TABLE projects.declaration_action_receipts DROP CONSTRAINT declaration_action_receipts_owner_check;
ALTER TABLE projects.declaration_action_receipts ADD CONSTRAINT declaration_action_receipts_owner_check
 CHECK (owner IN ('projects','knowledge','backupcontracts','serviceregistry','notesprojection','provenance','automation','hermesschedules'));

-- +goose Down
-- Existing Hermes receipts prevent downgrade rather than losing history.
ALTER TABLE projects.declaration_action_receipts DROP CONSTRAINT declaration_action_receipts_owner_check;
ALTER TABLE projects.declaration_action_receipts ADD CONSTRAINT declaration_action_receipts_owner_check
 CHECK (owner IN ('projects','knowledge','backupcontracts','serviceregistry','notesprojection','provenance','automation'));
