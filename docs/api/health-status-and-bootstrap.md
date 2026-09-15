---
title: "Health Status And Bootstrap API"
description: "API reference for LOOM health, status, and bootstrap readiness routes."
audience:
  - developer
  - operator
tags:
  - loom
  - api
  - operations
status: draft
verified_at: "2026-07-07"
source_scope:
  - "internal/httpapi/server.go"
  - "internal/localclient/client.go"
  - "go run ./cmd/loom --json health"
  - "go run ./cmd/loom --json status"
related:
  - "[[API Reference]]"
  - "[[Runtime Status And Support]]"
  - "[[Getting Started]]"
  - "[[Diagnostics And Support]]"
aliases:
  - "Health API"
  - "Status API"
---
# Health Status And Bootstrap API

## What This Page Covers

This page covers the read-only daemon routes that answer whether LOOM is alive,
which node/runtime is serving, and whether bootstrap state is ready.

## Routes

| Method | Path | Safety | Purpose |
|---|---|---|---|
| `GET` | `/v1/health` | read-only | Daemon health, node identity, database/storage/migration/bootstrap checks. |
| `GET` | `/v1/status` | read-only | Concise operational status used by `loom status`. |
| `GET` | `/v1/bootstrap/status` | read-only | Bootstrap readiness and missing bootstrap records. |

Slice 4 verified `loom --json health` and `loom --json status`. Slice 10
verified setup/bootstrap-adjacent local checks through CLI and source.

## CLI Equivalents

```bash
loom --json health
loom --json status
loom setup status --json
```

The CLI is the recommended user-facing path. Raw route examples are mainly for
developers extending the local client or HTTP API.

## Healthy State

Healthy health/status responses should identify:

- daemon status;
- node id and role;
- database state;
- storage state;
- migration state;
- bootstrap state;
- relevant job/search/event/runner summary where status includes it.

If health is not OK, avoid mutating operations until the failing check is
understood.

## Bootstrap Status

Bootstrap status is about required seed records and node identity readiness. It
is not the same as setup install state. Setup install state is covered by the
setup commands and operations docs.

Use bootstrap status when a daemon starts but core records appear missing or
runtime identity looks wrong.

## Error Behavior

These routes should return clear JSON errors when config, database, storage,
migration, or bootstrap state prevents a valid report. Generic transport
failure usually means the CLI could not reach the daemon socket or HTTP
endpoint.

## Related Docs

- [[Runtime Status And Support]]
- [[Getting Started]]
- [[Diagnostics And Support]]
- [[Main And Mac Operations]]
- [[API Development]]
