---
title: "Provenance Runtime Operations"
description: "Operate and diagnose the isolated LOOM provenance foundation without exposing semantic content or activating production."
audience:
  - operator
  - developer
tags:
  - loom
  - operations
  - database
  - backup
  - security
status: verified
created_at: "2026-08-30T06:03:18Z"
updated_at: "2026-08-30T08:02:45Z"
verified_at: "2026-08-30"
source_scope:
  - "internal/provenance/runtime.go"
  - "internal/provenance/health.go"
  - "internal/provenance/recovery_package.go"
  - "internal/maintenance/backup_manifest.go"
  - "internal/backup/restore_drill.go"
  - "internal/workers/runtimes/main_backup.go"
  - "internal/httpapi/backup_coverage.go"
  - "internal/backupstrategy/archive_manifest.go"
  - "internal/cloudstorage/direct_archive.go"
  - "internal/supportbundle/collect_provenance.go"
  - "tests/smoke/v2_provenance_runtime_local.sh"
related:
  - "[[Provenance Foundation Reference]]"
  - "[[Backup Restore And Drills]]"
  - "[[Database Maintenance]]"
  - "[[Support Bundles]]"
aliases:
  - "Semantic Ledger Operations"
---
# Provenance Runtime Operations

## What This Runbook Covers

LOOM's provenance foundation is a durable semantic ledger inside the existing
`loomd` process. It uses its own PostgreSQL database and pool so semantic
lifecycle work cannot join a `loom_main` transaction accidentally.

This runbook covers readiness, isolation, restart, recovery evidence, and safe
diagnostics. It does not activate production. Ranked search, automatic
extraction, Archivist runs, Basecamp delivery, project tracking, and SQLite
import are not part of the foundation.

## Runtime Shape

The expected production-like shape is:

```text
loomd
  main pool        -> loom_main
  provenance pool  -> loom_provenance as role loom_provenance
```

There is no provenance daemon, container, VM, or second service. The daemon
opens the main database first, then the provenance database. Shutdown closes
them in reverse order.

The provenance database URL is configured separately with
`LOOM_PROVENANCE_DB_URL`. Normal config diagnostics expose only whether it is
configured; they do not print the URL or its credential material.

## Readiness

When the service has been activated through a reviewed deployment, the private
readiness route is:

```bash
curl --fail --silent --show-error \
  --unix-socket /run/loom/loomd.sock \
  http://loom/v1/provenance/health
```

A healthy response reports:

- `state: ready` and `code: ready`;
- database `loom_provenance`;
- role `loom_provenance`;
- equal applied and packaged schema heads;
- no pending migration versions.

The route is policy-authorized as `main@provenance.health.read`. It returns
schema and runtime metadata, never candidate claims, source excerpts,
submitted payloads, connection strings, or credentials.

Treat `wrong_database`, `wrong_role`, `schema_behind`, `schema_ahead`,
`history_mismatch`, and `schema_tampered` as fail-closed states. Do not repair
them with manual SQL. Preserve the logs, verify configuration, and use the
reviewed migration or restore workflow.

## Restart Check

A safe restart acceptance proves three things:

1. `loomd` stops cleanly and closes the provenance pool before the main pool.
2. The next process starts with migration writes disabled against an already
   migrated database and still reports `ready`.
3. An exact candidate read returns the same durable lifecycle state recorded
   before shutdown.

The local disposable smoke performs this sequence without touching a running
service:

```bash
bash tests/smoke/v2_provenance_runtime_local.sh
```

Do not use this development smoke against `loom-main`; it deliberately creates
and drops databases and writes synthetic semantic markers inside its own
temporary cluster.

## Backup And Restore State

Backup manifest schema `loom.backup.manifest.v0.10` can describe one complete
custom-format provenance dump. The contract binds:

- the isolated database name and exact schema head;
- all provenance-owned relations;
- per-relation logical counts and a stable graph digest;
- dump size and SHA-256;
- exactly one matching authenticated provenance artifact.

Verification also requires an independently retained SHA-256 for the manifest
itself. A manifest cannot authenticate its own replacement. A disposable
restore must reproduce the schema head, every logical count, and the graph
digest, and it must use a target whose name starts with
`loom_provenance_restore_drill_`.

The accepted backup-transition runtime now creates this recovery package beside
the operational package and records its manifest path and SHA-256 in successful,
committed main-backup operation evidence. General backup coverage reads that
separately retained operation evidence and independently verifies the package;
the direct Borg archive path also binds the verified package and identity into
its archive evidence.

The implementation is present, but production activation and live backup or
restore remain integrator-owned and were not performed by this feature. When no
successful committed operation evidence is available, coverage still reports
the provenance entry as critical/unknown. Do not suppress that finding or
compute the expected identity from the manifest being checked.

## Health And Support Redaction

The recovery health builder has a bounded, read-only inspection path for schema
readiness, lifecycle counts, source/evidence integrity, and verified backup
freshness. It uses aggregate queries rather than serializing semantic rows.

The support collector accepts only the bounded health projection. If no
provenance health provider is injected, the section is marked skipped; it does
not fall back to a raw database dump. Current support output must never include:

- claims or record bodies;
- source context or excerpts;
- submitted or internal payload JSON;
- PostgreSQL URLs, roles containing secrets, passwords, or tokens.

## Common Problems

- **Readiness says `wrong_database` or `wrong_role`:** verify the separate
  provenance URL and Nix role/database provisioning. Never point the
  provenance pool at `loom_main`.
- **Readiness says the schema is behind:** use the reviewed release migration
  path. Do not enable ad-hoc auto-migration on a production host.
- **Backup coverage is critical/unknown:** confirm a successful committed main
  backup recorded the provenance manifest path and SHA-256, then inspect the
  recovery package without replacing that trusted value. Missing operation
  evidence remains a fail-closed result.
- **The support section is skipped:** the runtime has no injected provenance
  health provider. Do not replace the skipped section with semantic rows.
- **A restore target is rejected:** use a new disposable target with the
  required prefix. `loom_main`, `loom_provenance`, and arbitrary database names
  are intentionally refused.

## Related Docs

- [[Provenance Foundation Reference]]
- [[Backup Restore And Drills]]
- [[Database Maintenance]]
- [[Support Bundles]]
- [[Provenance Runtime Development]]
