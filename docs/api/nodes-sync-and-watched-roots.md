---
title: "Nodes Sync And Watched Roots API"
description: "API reference for LOOM node registry, protected-folder control, node-agent reports, sync state, and watched-root status."
audience:
  - developer
  - operator
tags:
  - loom
  - api
  - operations
  - configuration
status: draft
verified_at: "2026-08-17"
source_scope:
  - "internal/httpapi/server.go"
  - "internal/loomcli/domain.go"
  - "internal/loomcli/sync.go"
  - "internal/loomcli/watched_roots.go"
  - "internal/loomcli/communication.go"
  - "internal/nodeagent"
  - "internal/backupcontracts/control.go"
  - "internal/backupcontracts/status.go"
related:
  - "[[Backup Cloud And Maintenance API]]"
  - "[[API Reference]]"
  - "[[Nodes Communication Sync And Setup CLI Reference]]"
  - "[[Node Agent CLI Reference]]"
  - "[[Node Enrollment And Watched Roots]]"
aliases:
  - "Nodes API"
  - "Sync API"
  - "Watched Roots API"
---
# Nodes Sync And Watched Roots API

## Envelope

Main-backed node, communication, sync, and watched-root routes return the
normal LOOM envelope:

```json
{
  "ok": true,
  "data": {},
  "meta": {
    "correlation_id": "corr_..."
  }
}
```

Local setup/update and `loom-node-agent` commands can use different local JSON
shapes; they are CLI-local operational surfaces, not the same HTTP envelope.

## Bootstrap And Nodes

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/bootstrap/status` | GET | read-only | `loom status`, setup/bootstrap checks |
| `/v1/nodes` | GET | read-only | `loom node list` |
| `/v1/nodes/{ref}` | GET | read-only | `loom node inspect`, `loom node health` |

At verification time, `loom node list --json` returned two active nodes:
`main` and `macbook`. `loom node health main --json` returned heartbeat and
message counters for a specific node.

## Node-Agent Reports

| Endpoint | Method | Safety | CLI/Agent Path |
|---|---|---|---|
| `/v1/node-agent/enroll` | POST | enrollment mutating | `loom-node-agent enroll` |
| `/v1/node-agent/heartbeat` | POST | node-agent mutating | `loom-node-agent heartbeat` |
| `/v1/node-agent/poll` | POST | node-agent mutating | `loom-node-agent poll` |
| `/v1/node-agent/ack` | POST | node-agent mutating | node-agent inbox/outbox runtime |
| `/v1/node-agent/providers/advertise` | POST | provider advertisement mutating | `loom-node-agent advertise` |
| `/v1/node-agent/capability-result` | POST | execution result mutating | node-agent runtime dispatch |
| `/v1/node-agent/sync/batches` | POST | sync report mutating | `loom-node-agent sync push` |
| `/v1/node-agent/sync/object-upload` | POST | upload mutating | node-agent sync worker |
| `/v1/node-agent/sync/private-backup` | POST | private backup mutating | `loom-node-agent private-backup` |
| `/v1/node-agent/sync/deletion-request` | POST | deletion request mutating | `loom-node-agent sync request-delete` |
| `/v1/node-agent/watched-roots/report` | POST | watched-root report mutating | watched-root worker |
| `/v1/node-agent/watched-roots/backup-batches` | POST | backup report mutating | watched-root backup worker |

These endpoints are for node-agent runtime calls. Manual operators should use
`loom-node-agent` or main `loom` commands instead of constructing raw requests.

## Protected-Folder Control Plane

User-facing Protect Folder requests enter through the backup-contract API in
[[Backup Cloud And Maintenance API]]. Main stores canonical desired YAML and
routes preflight or reconciliation to the selected owner node through the
existing durable communication and node-agent poll/ack substrate.

This relationship does not make communication payloads, message kinds,
credentials, or agent polling internals part of the public Protect Folder API.
Clients should use backup-contract lifecycle endpoints and their projected
status rather than enqueueing raw messages. Node agents authenticate with their
existing node credentials; requests never carry credential values.

Preflight acknowledgement is bounded evidence, not folder contents.
Reconciliation acknowledgement identifies the desired revision and
configuration hash the node applied. Main combines that evidence with the
matching watched-root report and a later accepted backup before projecting
`protected`.

## Durable Communication

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/communication/health` | GET | read-only | `loom communication health` |
| `/v1/communication/messages` | GET/POST | read-only or operator-mutating | `loom messages list`, `loom message enqueue` |
| `/v1/communication/messages/{ref}` | GET | read-only | `loom message inspect` |

At verification time, communication health returned node presence counters and
message backlog counters. `messages list --limit 5 --json` returned five
durable communication records.

## Sync

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/sync/status` | GET | read-only | `loom sync status --node <node>` |
| `/v1/sync/batches` | GET | read-only | `loom sync batches` |
| `/v1/sync/conflicts` | GET | read-only | `loom sync conflicts` |
| `/v1/sync/replicas` | GET | read-only | `loom sync replicas` |
| `/v1/sync/private-backups` | GET | read-only | `loom sync private-backups` |
| `/v1/sync/deletion-requests` | GET | read-only | `loom sync deletion-requests` |
| `/v1/sync/deletion-requests/{ref}` | GET/POST | read-only or operator-mutating | `loom sync deletion-request inspect/review/approve/deny/complete` |

`sync status` requires a node filter. Without `--node`, the CLI returns
`sync.node_required`.

## Watched Roots

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/watched-roots/status` | GET | read-only | `loom watched-roots status` |
| `/v1/watched-roots/findings` | GET | read-only | `loom watched-roots findings`, `loom watched-roots failures` |
| `/v1/watched-roots/backups/status` | GET | read-only | `loom watched-roots backups status` |
| `/v1/watched-roots/backups/batches` | GET | read-only | `loom watched-roots backups batches` |
| `/v1/watched-roots/backups/items` | GET | read-only | `loom watched-roots backups items` |

Status rows wrap the watched root under a `root` field and can include latest
findings. Backup status returns root, latest batch, item counts, accepted,
skipped, failed, duplicate, deletion marker, artifact, and byte counters.

Protected folders are a user-facing policy projection, while watched roots are
the complete technical inventory. A protected-folder contract compiles to a
backup-only managed watched root on its owner node, but system, Box, project,
notes, and other roots continue to appear only in the broader watched-root
surfaces. Do not infer a protected-folder contract from an arbitrary watched
root.

## Related Docs

- [[Backup Cloud And Maintenance API]]
- [[Nodes Communication Sync And Setup CLI Reference]]
- [[Node Agent CLI Reference]]
- [[Node Enrollment And Watched Roots]]
- [[API Route Index]]
