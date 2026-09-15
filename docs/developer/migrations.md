---
title: "Migrations"
description: "Developer guide to LOOM database migrations, daemon migration commands, and update-flow boundaries."
audience:
  - developer
  - operator
tags:
  - loom
  - developer
  - operations
status: draft
verified_at: "2026-07-07"
source_scope:
  - "go run ./cmd/loomd migrate --help"
  - "internal/loomdapp/root.go"
  - "migrations"
related:
  - "[[Developer Documentation]]"
  - "[[Daemon CLI Reference]]"
  - "[[Database Maintenance]]"
  - "[[Release Flow]]"
aliases:
  - "Database Migrations"
---
# Migrations

## Purpose

LOOM uses PostgreSQL on DB-capable nodes. Schema changes belong in the
migration system, and production migration application belongs inside the
release/update process unless an operator explicitly chooses a maintenance
action.

## Commands

```bash
loomd migrate status
loomd migrate up
```

`migrate status` is read-only. `migrate up` mutates the database.

`loomd serve` can also run migrations before serving when configured with:

```bash
loomd serve --auto-migrate true
```

Production service configuration should come from the host config, not an
ad-hoc terminal invocation.

## Development Workflow

For a schema change:

1. Add a migration under `migrations/`.
2. Update the Go domain/service code that reads or writes the schema.
3. Add focused tests for the migration-dependent behavior.
4. Run migration-aware tests.
5. Include migration impact in the release/update plan.

Do not use raw SQL edits against production as a substitute for a migration.

## Update Boundary

Production update planning should account for migration delta and rollback
class. If an update has migrations, rollback may become restore-required rather
than service-only.

Use:

```bash
loom update plan --release-path /srv/loom/releases/<release-id>
```

Then review migration delta before apply.

## Runtime Startup

`loomd serve` constructs services after config load and optional migrations.
The current daemon source wires object store, search, knowledge, storage,
file-transfer, maintenance, automation, jobs, workers, policy, modules,
realtime, nodes, sync, and watched-root services into the HTTP API.

If migrations fail during startup, `loomd` should fail rather than continuing
against an unexpected schema.

## Related Docs

- [[Daemon CLI Reference]]
- [[Database Maintenance]]
- [[Release Flow]]
- [[API Development]]
