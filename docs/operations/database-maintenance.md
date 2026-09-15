---
title: "Database Maintenance"
description: "Operator runbook for LOOM database status, doctor checks, guarded retention compaction, and maintenance findings."
audience:
  - operator
  - developer
tags:
  - loom
  - operations
  - troubleshooting
status: draft
verified_at: "2026-07-07"
source_scope:
  - "/tmp/loomdocs-v096 --json database status"
  - "/tmp/loomdocs-v096 --json database doctor"
  - "/tmp/loomdocs-v096 --json database compact --dry-run"
  - "/tmp/loomdocs-v096 --json maintenance retention dry-run"
  - "internal/loomcli/database.go"
  - "internal/loomcli/maintenance.go"
  - "internal/maintenance"
related:
  - "[[Operations]]"
  - "[[Backup Sync Retention And Cloud]]"
  - "[[Backup Cloud And Maintenance CLI Reference]]"
  - "[[Backup Restore And Drills]]"
  - "[[Configuration]]"
aliases:
  - "Database Compaction"
  - "Maintenance Retention"
---
# Database Maintenance

## Purpose

This runbook keeps LOOM database maintenance explicit and reversible. It covers
status checks, doctor output, guarded compaction, and maintenance findings.

The database is durable operational truth. Compaction should never be used as a
casual cleanup command.

## Read-Only Checks

```bash
loom database status
loom database doctor
loom maintenance db status
loom maintenance status
loom maintenance findings list --limit 20
```

Healthy state means:

- database status is `ok`;
- migration status is `ok`;
- warnings are empty or understood;
- maintenance workers are healthy;
- open findings are zero or triaged.

At verification time, database status was `ok`, migration status was `ok`, and
database doctor reported no warnings.

## Compaction Dry-Run

Dry-run is the default:

```bash
loom database compact --dry-run
loom maintenance retention dry-run
```

For a reviewed apply, write the plan to a file:

```bash
loom database compact --dry-run --out /tmp/loom-db-compaction-plan.json
```

The maintenance equivalent is:

```bash
loom maintenance retention dry-run --out /tmp/loom-db-compaction-plan.json
```

At verification time, dry-run returned:

- `status=dry_run`;
- `dry_run=true`;
- `mutates_database=false`;
- six retention plan groups;
- explanatory warnings that no rows were deleted.

Those warnings are guardrails. They explain that apply writes aggregate rollups
and deletes only old routine success events plus settled worker leases, while
preserving audit-critical evidence.

## What The Plan Reports

The current plan groups include:

- old routine success events from `events.events`;
- old non-manual succeeded worker runs;
- settled worker controls;
- settled worker leases.

The plan explicitly preserves:

- audit, security, error, failed, manual, and recent events;
- failed, manual, running, recent, and reference-critical worker runs;
- pending and failed worker controls;
- active leases;
- maintenance operation evidence;
- maintenance findings.

## What Apply Can Remove

The v0.9.9 apply path deletes only:

- old routine success events in `events.events`;
- old settled worker leases in `workers.worker_leases`.

The deletion is bounded by:

- the retention cutoff;
- an allowed table list;
- the candidate class rules;
- `--max-rows-per-batch`;
- `--max-total-rows`.

Worker runs and worker controls may appear in the dry-run as rollup candidates,
but detailed deletion for those tables remains disabled. They are retained
because they can explain why work ran, paused, retried, or changed state.

## Apply Compaction

Apply only after reviewing the exported plan and confirming a fresh backup
exists:

```bash
loom database compact --plan /tmp/loom-db-compaction-plan.json --confirm
```

Equivalent maintenance command:

```bash
loom maintenance retention apply --plan /tmp/loom-db-compaction-plan.json --yes
```

The compatibility commands still exist:

```bash
loom database compact --dry-run=false --confirm
```

```bash
loom maintenance retention apply --yes
```

Use `--reason` when a human operator is applying a plan:

```bash
loom database compact \
  --plan /tmp/loom-db-compaction-plan.json \
  --confirm \
  --reason "v0.9.9 acceptance after fresh backup"
```

Record the plan hash and preserve dry-run evidence in the operations note or
release test log before applying. Apply records maintenance operation evidence
when the DB maintenance worker instance is registered; if that evidence cannot
be recorded, the result should explain why with `evidence_status=unavailable`.

Compaction deletes rows logically. PostgreSQL table files and database files may
not shrink immediately after the command returns. Autovacuum can reuse freed
space. `VACUUM FULL`, `CLUSTER`, and table rewrites are not part of this
workflow.

## Findings

List findings:

```bash
loom maintenance findings list --status open --limit 50
loom maintenance findings list --severity warning --limit 50
loom maintenance findings list --worker main.db_maintenance
```

Findings are operator attention records. Do not delete them by manipulating the
database directly. Use maintenance surfaces and future planned finding lifecycle
commands when available.

## When Events Are Large

If `events.events` grows large during development:

1. Run `loom database status`.
2. Run `loom database compact --dry-run`.
3. Review retention candidates and warnings.
4. Confirm backups are healthy with `loom backup status` and
   `loom backup coverage`.
5. Apply only if the dry-run plan is understood.

Large size alone is not enough reason to run SQL deletes manually.

## Related Docs

- [[Backup Sync Retention And Cloud]]
- [[Backup Cloud And Maintenance CLI Reference]]
- [[Backup Restore And Drills]]
- [[Configuration]]
