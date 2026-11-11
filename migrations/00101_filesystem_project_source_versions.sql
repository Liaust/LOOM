-- +goose Up
-- Keep legacy pairs and require one exact source/version for declarations.
-- +goose StatementBegin
DO $$
DECLARE tab text;
BEGIN
 FOREACH tab IN ARRAY ARRAY['project_repository_sources','project_repository_source_history'] LOOP
  EXECUTE format('ALTER TABLE projects.%I DROP CONSTRAINT declaration_source_versions',tab);
  EXECUTE format($q$ALTER TABLE projects.%I ADD CONSTRAINT declaration_source_versions CHECK (
   (project_contract_schema_version IN ('project.contract.v0.3','project.contract.v0.4') AND repos_contract_schema_version IN ('repos.contract.v0.3','repos.contract.v0.4')) OR
   (project_contract_schema_version IN ('project.contract.v0.5','project.contract.v0.6') AND repos_contract_schema_version=project_contract_schema_version
    AND project_contract_path=project_root||'/.loom/project.yaml' AND repos_contract_path=project_contract_path AND repos_contract_digest=project_contract_digest))$q$,tab);
 END LOOP;
END; $$;
-- +goose StatementEnd

-- +goose Down
-- Retain v0.6 sources and history; never rewrite accepted declarations on rollback.
