---
title: "API Route Index"
description: "Map of LOOM HTTP API route groups to API documentation pages."
audience:
  - developer
  - operator
tags:
  - loom
  - api
  - reference
status: draft
verified_at: "2026-07-07"
source_scope:
  - "internal/httpapi/server.go"
related:
  - "[[API Reference]]"
  - "[[Authentication Envelope And Errors]]"
  - "[[Developer Documentation]]"
aliases:
  - "HTTP Route Index"
---
# API Route Index

This page maps current route groups to the API pages that will explain them.
Slice 1 found 199 registered route handlers in `internal/httpapi/server.go`.

## Route Groups

| Route Group | API Page | Notes |
|---|---|---|
| `/v1/health`, `/v1/status`, `/v1/bootstrap/status` | [[Health Status And Bootstrap API]] | Readiness and bootstrap status. |
| `/v1/box/*`, `/v1/file-transfers*`, storage file-transfer helpers | [[Storage Box And File Transfers API]] | Box watch state, Dropzone upload sessions, and file transfers. |
| `/v1/scopes*`, `/v1/projects*`, `/v1/project-*` | [[Projects Scopes And Contracts API]] | Project lifecycle, facets, contracts, and registrations. |
| `/v1/knowledge/notes/*`, `/v1/objects*`, `/v1/search`, `/v1/index*` | [[Notes Knowledge And Search API]] | Notes roots, objects, extraction, search, indexes, embeddings, projection, and workers. |
| `/v1/storage/*` | [[Storage Box And File Transfers API]] | Storage tree, entries, retention, safe-delete, lane, export, archive, fetch, restore, tombstones, fidelity, and path resolution. |
| `/v1/scripts*`, `/v1/jobs*`, `/v1/runners*`, `/v1/workers*`, `/v1/artifacts*` | [[Automation Jobs Scripts And Artifacts API]] | Execution and background work. |
| `/v1/automations*`, `/v1/integrations*`, `/v1/direct-events*`, `/v1/schedules*`, `/v1/invocations*` | [[Automation Jobs Scripts And Artifacts API]] | Automation, direct events, schedules, and invocations. |
| `/v1/nodes*`, `/v1/node-agent/*`, `/v1/sync/*`, `/v1/watched-roots/*`, `/v1/communication/*` | [[Nodes Sync And Watched Roots API]] | Nodes, enrollment, communication, sync, watched roots, and private backups. |
| `/v1/providers*`, `/v1/capabilities*`, `/v1/capability-*`, `/v1/routes*`, `/v1/policy/*`, `/v1/approvals*`, `/v1/grants*` | [[Capabilities Policy Security And Routes API]] | Capability routing, provider registry, policy, approvals, grants, and call audit. |
| `/v1/modules*`, `/v1/module-*`, `/v1/agents/*`, `/v1/realtime/*` | [[Modules Agents And Realtime API]] | Modules, agent-facing access, and realtime primitives. |
| `/v1/maintenance/*`, `/v1/backup/coverage`, `/v1/cloud/*` | [[Backup Cloud And Maintenance API]] | Maintenance, backup coverage, cloud status, snapshots, retention, and backend state. |

## Verification Rule

Route presence is source-verified here. Request, response, and error examples
must be verified in the domain API pages before those pages mark examples as
working.

## Related Docs

- [[API Reference]]
- [[Developer Documentation]]
- [[Command Index]]
