\set ON_ERROR_STOP on

-- An OS/ICU update can change index ordering without a PostgreSQL major upgrade.
-- Refreshing catalog versions alone would hide potentially stale indexes.
SELECT
  EXISTS (
    SELECT 1 FROM pg_database
    WHERE datname = current_database()
      AND datcollversion IS NOT NULL
      AND datcollversion IS DISTINCT FROM pg_database_collation_actual_version(oid)
  ) OR EXISTS (
    SELECT 1 FROM pg_collation
    WHERE collversion IS NOT NULL
      AND collversion IS DISTINCT FROM pg_collation_actual_version(oid)
  ) AS loom_collation_rebuild_required
\gset

\if :loom_collation_rebuild_required
  DO $check$
  BEGIN
    IF EXISTS (SELECT 1 FROM pg_class WHERE relkind = 'm')
       OR EXISTS (
         SELECT 1 FROM pg_partitioned_table
         WHERE EXISTS (SELECT 1 FROM unnest(partcollation::oid[]) c WHERE c <> 0)
       ) THEN
      RAISE EXCEPTION 'Collation transition needs review of materialized views or collated partition bounds';
    END IF;
  END;
  $check$;
  \echo Rebuilding indexes before refreshing collation versions
  SET lock_timeout = '10s';
  SET statement_timeout = '25min';
  SET maintenance_work_mem = '512MB';
  SET max_parallel_maintenance_workers = 2;
  REINDEX DATABASE;

  SELECT format('ALTER COLLATION %I.%I REFRESH VERSION', n.nspname, c.collname)
  FROM pg_collation c JOIN pg_namespace n ON n.oid = c.collnamespace
  WHERE c.collversion IS NOT NULL
    AND c.collversion IS DISTINCT FROM pg_collation_actual_version(c.oid)
  \gexec

  SELECT format('ALTER DATABASE %I REFRESH COLLATION VERSION', datname)
  FROM pg_database
  WHERE datname = current_database()
    AND datcollversion IS NOT NULL
    AND datcollversion IS DISTINCT FROM pg_database_collation_actual_version(oid)
  \gexec
\endif
