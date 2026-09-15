---
title: "Command Index"
description: "Map of current LOOM command domains to reference and workflow documentation."
audience:
  - user
  - operator
  - developer
tags:
  - loom
  - cli
  - reference
status: verified
verified_at: "2026-08-31"
source_scope:
  - "go run ./cmd/loom --help"
  - "go run ./cmd/loom storage --help"
  - "go run ./cmd/loom backup --help"
  - "go run ./cmd/loom lane --help"
  - "go run ./cmd/loom provenance --help"
  - "go run ./cmd/loom agent pack --help"
related:
  - "[[Command Safety]]"
  - "[[Storage Box And Lane CLI Reference]]"
  - "[[Backup Cloud And Maintenance CLI Reference]]"
---
# Command Index

Run `loom <domain> --help` for exact flags. This page routes users to the
current conceptual and safety context.

## Storage And Files

| Domain | Use | Documentation |
|---|---|---|
| `loom box` | Inspect/init/repair Box and watch contracts. | [[Storage Box And Lane CLI Reference]] |
| `loom lane` | Plan/send/repair Lane batches. | [[Storage Box And Lane CLI Reference]] |
| `loom ignore` | Explain transfer-policy inclusion. | [[Files Storage And Lane]] |
| `loom storage` | Catalog, filesystem, Documents, archive, retention, fidelity, cleanup, safe-delete, and mounts. | [[Storage Box And Lane CLI Reference]] |
| `loom watched-roots` | Watched-root desired/applied status. | [[Backup Sync Retention And Cloud]] |
| `loom sync` | Node sync/private backup operations. | [[Backup Sync Retention And Cloud]] |

`loom storage export` is a read-only deprecated compatibility diagnostic. No
current command refreshes/rebuilds a generated storage tree.

## Backup, Cloud, And Maintenance

| Domain | Use | Documentation |
|---|---|---|
| `loom backup` | Coverage, contracts, create, verify, restore drill. | [[Backup Cloud And Maintenance CLI Reference]] |
| `loom cloud` | Cloud status, snapshots, retention, offload/fetch. | [[Backup Cloud And Maintenance CLI Reference]] |
| `loom maintenance` | Durable maintenance operations/findings. | [[Backup Cloud And Maintenance CLI Reference]] |
| `loom database` | Database status and compaction. | [[Backup Cloud And Maintenance CLI Reference]] |
| `loom update` | Reviewed update/maintenance-window lifecycle. | [[Main And Mac Operations]] |
| `loom setup` | Install/status/doctor/repair/uninstall planning. | [[Configuration]] |

## Knowledge, Search, And Objects

| Domain | Use |
|---|---|
| `loom notes`, `loom index`, `loom indexes` | Notes pipeline, indexing, search readiness. |
| `loom search` | Query indexed knowledge. |
| `loom object`, `loom objects` | Object inspection/lifecycle. |
| `loom project`, `loom projects` | Project contracts, archive, validation, and lifecycle. |
| `loom provenance` | Qualified semantic search, exact record/candidate/case reads, semantic repository discovery, and manual one-project projection sync. |

Canonical Notes source remains in Box/project roots. The Notes projection is
generated and read-only.

Choose one engine first: Objects for technical state, Notes for source
material, and Provenance for qualified current state. Follow compact search
results with exact get or inspect, then expand sequentially only for a named
gap. Pending candidates are opt-in clues and never accepted records.

Use `loom project` and `loom project repos` for technical registry navigation.
Use `loom provenance repo list` when user context describes a repository by
alias, purpose, topic, role, or current focus.

## Execution And Automation

| Domain | Use |
|---|---|
| `loom job`, `loom jobs` | Jobs and executions. |
| `loom worker`, `loom workers` | Durable worker state and runs. |
| `loom automation`, `loom automations` | Automation definitions. |
| `loom schedule`, `loom schedules` | Scheduled triggers. |
| `loom direct-event`, `loom direct-events` | Direct event receivers. |
| `loom script`, `loom scripts` | Script contracts. |
| `loom runner`, `loom runners` | Execution runners. |

## Nodes, Services, And Security

| Domain | Use |
|---|---|
| `loom node`, `loom nodes` | Node inventory/status. |
| `loom service`, `loom services` | Service registry/operations. |
| `loom provider`, `loom providers` | Provider inventory. |
| `loom capability`, `loom capabilities` | Capability contracts. |
| `loom routes`, `loom grants`, `loom approvals`, `loom policy` | Routing and authorization. |
| `loom communication`, `loom message`, `loom messages` | Durable node messaging. |
| `loom security` | Security inspection. |

## Top-Level Operator Surfaces

| Command | Use |
|---|---|
| `loom health` | Daemon health. |
| `loom status` | Concise runtime status. |
| `loom enter` | Interactive Portal. |
| `loom support` | Bounded redacted support collection. |
| `loom version` | Version/build identity. |
| `loom docs` | Documentation discovery. |
| `loom agent pack` | Validate the repository-owned AI pack and dry-run or apply a named workspace template to an explicit path. |

## Safety

Read [[Command Safety]] before mutation. Prefer status, inspect, plan,
`--dry-run`, and JSON output for automation. Production deploy, filesystem
cutover, restore, and host mount operations always require their runbooks and
explicit authority.

## Shell Completion

`loom completion`, `loom-node-agent completion`, and `loomd completion` emit
shell-completion scripts for the selected shell. They do not contact the daemon
or mutate LOOM state; install the generated script according to the shell's own
completion directory and trust policy.

## Related Docs

- [[Command Safety]]
- [[Storage Box And Lane CLI Reference]]
- [[Backup Cloud And Maintenance CLI Reference]]

## Complete Binary And Alias Command Maps

The domain-oriented index above is the current navigation layer. The preserved maps below retain explicit first-party coverage for every `loom` alias group plus the separate `loom-node-agent` and `loomd` binaries.

This page maps current command domains to the CLI pages that will explain them.
Slice 1 verified that all current top-level help pages render for `loom`,
`loom-node-agent`, and `loomd`.

### `loom`

| Command Domain | CLI Page | Notes |
|---|---|---|
| `status`, `health`, `version`, `enter`, `support` | [[Runtime Status And Support]] | Daily readiness, portal entry, and support evidence. |
| `docs` | [[Documentation CLI]] | Local release documentation status, deterministic search, bounded inspection, and related-page lookup. |
| `project`, `projects`, `scope` | [[Projects And Scopes CLI Reference]] | Project lifecycle and scope inspection. `project` is canonical; `projects` is a compatibility alias. |
| `notes`, `search`, `index`, `indexes`, `object`, `objects`, `provenance` | [[Notes Search And Indexes CLI Reference]] and [[Provenance Foundation Reference]] | Notes source material, objects and extraction, indexed search, plus qualified provenance search, exact get, and semantic repository discovery. `object` is canonical; `objects` is a compatibility alias for the top-level object-store command. |
| `box`, `lane`, `storage` | [[Storage Box And Lane CLI Reference]] | Box status, Lane transfers, canonical custody/catalog paths, retention, and safe-delete. |
| `backup`, `cloud`, `database`, `maintenance` | [[Backup Cloud And Maintenance CLI Reference]] | Backup coverage, standalone backup contracts, cloud checks, reviewed database compaction, and maintenance. |
| `automation`, `automations`, `integration`, `integrations`, `direct-event`, `direct-events`, `schedule`, `schedules`, `script`, `scripts`, `job`, `jobs`, `runner`, `runners`, `worker`, `workers`, `artifact`, `artifacts`, `invocation`, `invocations` | [[Automation Jobs And Workers CLI Reference]] | Automation, execution, job lifecycle, workers, and artifacts. |
| `capabilities`, `capability`, `capability-call`, `capability-calls`, `provider`, `providers`, `provider-advertisement`, `provider-advertisements`, `route`, `routes`, `approval`, `approvals`, `grant`, `grants`, `policy`, `security`, `actor`, `agent` | [[Capabilities Security And Policy CLI Reference]] and [[Modules Agents And Realtime CLI Reference]] | Capability registry, security, policy, grants, runtime-agent access, and local external-agent pack validation/scaffolding. |
| `node`, `bootstrap`, `setup`, `update`, `communication`, `message`, `messages`, `sync`, `watched-roots` | [[Nodes Communication Sync And Setup CLI Reference]] | Node management, communication, sync, setup, and update flow. |
| `module`, `modules`, `realtime` | [[Modules Agents And Realtime CLI Reference]] | Modules and realtime primitives. |

### `loom-node-agent`

| Command Domain | CLI Page | Notes |
|---|---|---|
| `status`, `serve`, `version`, `workers`, `outbox`, `inbox` | [[Node Agent CLI Reference]] | Local node-agent runtime inspection and operation. |
| `init`, `enroll`, `credential`, `heartbeat`, `poll` | [[Node Agent CLI Reference]] | Enrollment and main communication. |
| `providers`, `advertise`, `filesystem`, `watched-roots` | [[Node Agent CLI Reference]] | Local provider and filesystem exposure. |
| `dropzone`, `sync`, `private-backup`, `storage-mount` | [[Node Agent CLI Reference]] | Transfer, sync, private backup, and local mount desired state. |

### `loomd`

| Command Domain | CLI Page | Notes |
|---|---|---|
| `serve`, `migrate`, `version` | [[Daemon CLI Reference]] | Daemon runtime, migrations, and version checks. |

### Related Docs

- [[CLI Reference]]
- [[Command Safety]]
- [[Operations]]
