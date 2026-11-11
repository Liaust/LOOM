-- +goose Up
-- Canonicalize only complete, proven-equivalent legacy consumer configurations.
-- No registration, pending edit, source or retained payload is rewritten.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION watched_roots.content_config(config jsonb) RETURNS jsonb
LANGUAGE sql IMMUTABLE STRICT AS $$
 WITH normalized AS (
 SELECT CASE WHEN config->>'schema_version'='watched_root.config.v0.3'
 AND NOT config ? 'files' AND (
 (NOT config ? 'consumer_selections' AND NOT (
 config#>>'{index_policy,mode}'='markdown_text'
 AND config#>>'{sync_policy,mode}'='selected_files'
 AND config#>>'{backup_policy,mode}' <> 'none'))
 OR (config#>>'{index_policy,mode}'='markdown_text'
 AND config#>>'{sync_policy,mode}'='selected_files'
 AND config#>>'{backup_policy,mode}' <> 'none'
 AND config->'consumer_selections'=jsonb_build_object('sync',jsonb_build_object(
 'include','["**/*.[aA][dD][oO][cC]","**/*.[mM][aA][rR][kK][dD][oO][wW][nN]","**/*.[mM][dD]","**/*.[mM][dD][oO][wW][nN]","**/*.[rR][sS][tT]","**/*.[tT][eE][xX][tT]","**/*.[tT][xX][tT]"]'::jsonb,'exclude','[]'::jsonb))))
 THEN jsonb_set(config-'consumer_selections','{schema_version}','"watched_root.config.v0.2"'::jsonb)
 ELSE config END AS value)
 SELECT value-'display_name' #- '{watch,debounce_window}'
 #- '{scan,full_rescan_interval}' #- '{scan,max_runtime}'
 #- '{scan,max_files_per_run}' #- '{scan,max_dirs_per_run}'
 #- '{scan,max_hash_bytes_per_run}' #- '{scan,stability_window}' FROM normalized;
$$;
-- +goose StatementEnd

-- +goose Down
-- Retain compatible source readers and pending edit authority.
