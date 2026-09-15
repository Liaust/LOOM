---
title: "Architecture"
description: "Developer map of LOOM runtime composition, canonical storage ownership, catalog transactions, API/client/CLI/Portal layers, and workers."
audience:
  - developer
  - agent
tags:
  - loom
  - developer
  - architecture
status: verified
verified_at: "2026-08-27"
source_scope:
  - "AGENTS.md"
  - "internal/loomdapp/root.go"
  - "internal/filesystemlayout"
  - "internal/storagecatalog"
  - "internal/httpapi/server.go"
  - "internal/localclient/client.go"
related:
  - "[[Repository Map]]"
  - "[[Box Storage And Lane]]"
  - "[[Canonical Paths]]"
---
# Architecture

## Runtime Shape

`loomd` composes trusted runtime configuration into services and workers.
`internal/httpapi` exposes typed private routes, `internal/localclient` provides
the shared client contract, `internal/loomcli` provides commands, and
`internal/loomcli/portal` renders the interactive surface. CLI/Portal callers
do not invent custody roots.

Main remains the coordination point for catalog, archive, policy, discovery,
knowledge, and inter-node transfer. Nodes own live local state and call main
through typed capability/API/message paths.

## Filesystem Ownership

`internal/filesystemlayout` validates the configured ownership graph:

```text
Service
  Box                 editable source
  agents/             agent workspaces
  Storage/
    imports/           Lane custody
    backups/           user backup custody
    archive/           archive custody

Data
  box-state/           volatile Lane/Dropzone state
  generated/notes/     generated projection
  object-store/        protection copy
  storage-retention/   protection copy
  backups/main/        operational backups
```

Compatibility roots are typed migration inputs only. Production composition
passes custom configured roots through every service constructor.

## Catalog And Custody

`internal/storagecatalog` is authoritative for entries, physical references,
versions, availability, retention, tombstones, fidelity, and archive manifests.
Source resolution is operation-specific: readable source selection differs
from protection checks and fidelity observation.

Lane promotion and archive/catalog finalization use complete transactional
contracts. Large migration manifests are split into bounded artifacts but
preflight and apply the full reviewed set in one PostgreSQL transaction.

Filesystem commit markers precede visibility to backup traversal. File transfer
checks whole-file evidence before final rename. Archive recovery persists enough
immutable evidence to retry catalog commit without reopening an unreadable
source.

## Generated Data

The Notes projection is generated/read-only and built from canonical source or
approved backup evidence. Search/index state lives in PostgreSQL. The former
generated storage export has no active builder/materializer/worker/setup/share
path. Bounded catalog queries replace tree construction.

## Transfer Boundaries

Lane remote staging is confined by a hidden main-side command using trusted
runtime roots and no-follow component validation. Structured responses attest
operation, source, batch, runtime root, exact staging path, and terminal status.
Subprocess output capture is bounded streaming head/tail with exact byte counts.

Watched-root direct/chunked transfers bind every artifact to node/root/batch/
item/path/content identity. Both converge on the same manifest-committed backup
custody contract.

## Presentation And Operations

Doctor, support, status, API, CLI, and Portal use bounded typed summaries.
Storage SMB exports only active Box (read/write) and canonical Storage
(read-only) over private authenticated SMB3. Setup/bootstrap no longer creates
human links to a generated export.

## Testing Boundary

Use package tests for invariants and `tests/smoke/canonical_filesystem_local.sh`
for version-neutral local composition. Production cutover, rebuild, mounts,
backup/restore, and hardware acceptance are separate integrator operations.

## Related Docs

- [[Repository Map]]
- [[Box Storage And Lane]]
- [[Canonical Paths]]

## Complete live reference and preserved operational detail

The canonical guidance above is the current storage contract. The detailed material below preserves the complete live command, route, workflow, safety, interpretation, and troubleshooting coverage that predates the filesystem migration. Retired generated-export behavior is retained only when explicitly labelled as compatibility or historical context.

### What This Page Covers

This page explains the implemented software shape of LOOM for developers. For
the product mental model, start with [[LOOM Architecture]]. This page is about
where code runs, which packages own which boundary, and how a change normally
moves through the daemon, API, CLI, portal, workers, and database.

### Runtime Shape

LOOM currently has three Go entry points:

| Binary | Entry Point | Purpose |
|---|---|---|
| `loom` | `cmd/loom/main.go` | User/operator CLI and terminal portal. |
| `loomd` | `cmd/loomd/main.go` | Main daemon, service wiring, HTTP API, database access, workers. |
| `loom-node-agent` | `cmd/loom-node-agent/main.go` | Local node agent runtime for workspace-side status, providers, sync, watched roots, and storage mount tasks. |

`loomd` is the main service boundary. It loads config, optionally applies
migrations, opens PostgreSQL, constructs domain services, then exposes them
through `internal/httpapi.Server`.

### Service Wiring

The central daemon composition point is `internal/loomdapp/root.go`. It wires
the main service objects:

- identity, scopes, nodes, events, and bootstrap;
- projects, project contracts, project activation, and project watched roots;
- objects, search, knowledge, notes projection, and object-store diagnostics;
- storage catalog, storage export, retention, fidelity, archive, file
  transfers, Box, Dropzone, and Lane;
- scripts, jobs, artifacts, automation, schedules, direct events, workers, and
  maintenance;
- providers, capabilities, routing, policy, approvals, grants, modules,
  realtime, agents, communication, sync, and watched roots.

When adding a daemon-backed feature, expect to touch a domain package, service
wiring, HTTP route registration, local client wrapper, CLI command, tests, and
possibly the portal.

### Domain Packages

Most business logic lives under `internal/<domain>/`. The package name usually
matches the schema, command group, or feature surface:

| Domain Area | Packages |
|---|---|
| Core identity and topology | `identity`, `scopes`, `nodes`, `nodeprofiles`, `events` |
| Capability plane | `capabilities`, `routing`, `capabilityruntime`, `policy`, `agents`, `modules`, `realtime` |
| Projects and contracts | `projects`, `projectcontracts`, `projectactivation`, `projectwatch`, `projectdoctor`, `projectaccess` |
| Object and knowledge systems | `objects`, `objectstore`, `knowledge`, `search`, `notesprojection` |
| Files and storage | `box`, `dropzone`, `lane`, `filetransfer`, `storagecatalog`, `storageexport`, `storageretention`, `storagefidelity`, `storagearchive`, `storageview`, `mainstorage` |
| Automation and execution | `automation`, `scripts`, `jobs`, `artifacts`, `workers`, `workers/runtimes`, `workflows` |
| Operations | `backup`, `backupcoverage`, `cloudstorage`, `maintenance`, `supportbundle`, `update`, `setup`, `bootstrapssh` |

Keep domain behavior in domain packages. The CLI and portal should mostly
translate user intent into domain or API calls; they should not become the
source of truth for business rules.

### API And Local Client

`internal/httpapi/server.go` registers the daemon API under `/v1/...`.
Handlers should use `response.Envelope` shapes and domain errors where
possible, not ad-hoc text responses.

The CLI talks to the daemon through `internal/localclient`. The default client
uses the Unix socket path from config and a synthetic `http://loom` base URL.
`localclient.NewHTTP` exists for HTTP-backed paths when a caller explicitly
needs an HTTP base URL.

The normal implementation chain is:

```text
domain service -> HTTP handler -> localclient method -> CLI command/portal action
```

For details, see [[API Development]].

### CLI And Portal

`internal/loomcli/root.go` owns the root Cobra command and attaches the command
groups. Each domain command file should keep parsing, output formatting, and
local-client calls close together.

The portal is part of the CLI, not a separate web app. It lives under
`internal/loomcli/portal/` and is built with Bubble Tea/lipgloss. It uses the
same local client and action model as CLI commands where possible.

For details, see [[CLI Development]] and [[Portal Development]].

### Database And Migrations

Database shape is migration-owned. SQL migrations live under `migrations/`,
and the Go migration wrapper lives under `internal/migrations`.

Do not patch production data or schema by hand as a development shortcut. Add a
new migration, update service code, update tests, and let release/update flows
surface migration delta and rollback class.

For schema overview, see [[Database Schemas]] and [[Migrations]].

### Workers And Events

Events are durable truth. Workers process specific operational loops and leave
records under worker, job, maintenance, knowledge, storage, backup, cloud, and
sync tables.

When adding a worker:

- define the durable state it owns;
- give it an idempotent run-once path;
- record worker runs, controls, health, and failures;
- expose safe status/inspect commands before mutating run/repair commands;
- keep production validation focused and artifact paths under
  `.loom-acceptance`.

### Invariants

Developer changes should preserve these invariants:

- capabilities are typed, permissioned, routed, logged, and revocable;
- projects are scopes, not universal object parents;
- main owns global coordination, policy, archive, discovery, and knowledge;
- worker and capability operations should be idempotent when retried;
- credentials are brokered and redacted;
- CLI JSON output should remain envelope-shaped and scriptable;
- production-like operations need dry-run, confirmation, or explicit operator
  ownership.

### Related Docs

- [[LOOM Architecture]]
- [[Repository Map]]
- [[API Development]]
- [[CLI Development]]
- [[Portal Development]]
- [[Testing]]
