---
title: "Node Agent CLI Reference"
description: "CLI reference for the local loom-node-agent runtime, enrollment, provider advertisements, sync, watched roots, Dropzone, and storage mount workers."
audience:
  - operator
  - developer
tags:
  - loom
  - cli
  - operations
  - configuration
status: draft
verified_at: "2026-07-07"
source_scope:
  - "go run ./cmd/loom-node-agent --help"
  - "go run ./cmd/loom-node-agent --json status"
  - "go run ./cmd/loom-node-agent --json providers local"
  - "go run ./cmd/loom-node-agent --json workers status"
  - "go run ./cmd/loom-node-agent --json sync status"
  - "go run ./cmd/loom-node-agent --json watched-roots status"
  - "go run ./cmd/loom-node-agent --json storage-mount status"
related:
  - "[[Nodes Communication Sync And Setup CLI Reference]]"
  - "[[Main And Mac Operations]]"
  - "[[Node Enrollment And Watched Roots]]"
  - "[[Files Storage And Lane]]"
aliases:
  - "loom-node-agent"
  - "Node Agent CLI"
---
# Node Agent CLI Reference

## Purpose

`loom-node-agent` is the local runtime for a non-main node. On the MacBook it
owns local status, provider advertisement payloads, local runtime workers,
workspace sync queues, watched-root scanning, Dropzone transfer support, and
the desired state for the LOOM Main storage mount.

The main `loom` CLI usually asks main about node state. `loom-node-agent` asks
or changes local node-agent state.

## Safe Inspection

```bash
loom-node-agent status
loom-node-agent providers local
loom-node-agent workers status
loom-node-agent workers list
loom-node-agent inbox status
loom-node-agent outbox status
loom-node-agent outbox list
```

At verification time:

- `status --json` returned `ok=true` with config path, data dir, credential,
  node id, runtime, state path, version, and last heartbeat/poll fields;
- `providers local --json` returned a provider advertisement payload;
- `workers list --json` returned nine local runtime workers;
- `outbox list --json` returned local outbox records.

## Enrollment And Credentials

Enrollment and credentials are local-to-main identity workflows:

```bash
loom-node-agent init
loom-node-agent enroll
loom-node-agent credential status
loom-node-agent heartbeat
loom-node-agent poll
```

These commands can create or use credential material. Do not expose raw tokens,
private keys, or credential payloads in documentation or bug reports.

## Local Runtime Workers

Worker commands:

```bash
loom-node-agent workers status
loom-node-agent workers list
loom-node-agent workers run <worker-key> --once
```

Run commands are local operational actions. Use status and list first, then run
one worker only when an operator intentionally wants a single local cycle.

## Sync Queue

Local sync commands:

```bash
loom-node-agent sync status
loom-node-agent sync init-local
loom-node-agent sync push
loom-node-agent sync request-delete <path>
```

Smoke-only helpers exist:

```bash
loom-node-agent sync append-test-event
loom-node-agent sync create-test-object
```

Use smoke helpers only in accepted `.loom-acceptance` scenarios. They are not a
normal user workflow.

## Watched Roots

Local watched-root commands:

```bash
loom-node-agent watched-roots list
loom-node-agent watched-roots status
loom-node-agent watched-roots explain <path>
loom-node-agent watched-roots backups status
```

Mutating local configuration and worker actions:

```bash
loom-node-agent watched-roots add <root-key> <path>
loom-node-agent watched-roots apply-plan <watch-plan.json>
loom-node-agent watched-roots run --once
loom-node-agent watched-roots disable <root-key> --reason "project archived" --yes
```

`disable` stops future supervisor runs without deleting the watched-root
configuration, checkpoints, run history, findings, outbox, or backup evidence.
It is the supported owner-node action after a project archive or another
reviewed deactivation. Re-applying or adding the root explicitly enables its
worker again; a disabled root rejects direct `watched-roots run` requests.

At verification time, local watched-root list/status returned two roots and
backup status returned counts, paths, node id, and node key.

## Filesystem, Dropzone, Lane Housekeeping, And Storage Mount

Filesystem safe-root configuration:

```bash
loom-node-agent filesystem roots list
loom-node-agent filesystem roots add <name> <path> --mode read_only
```

Dropzone:

```bash
loom-node-agent dropzone status
loom-node-agent dropzone configure
loom-node-agent dropzone run --once
```

The default `node-agent.lane_housekeeping` runtime worker checks hidden local
Lane recovery state every minute. It expires the current successful cleanup
quarantine after its 30-minute grace period and converges audited pending
removals across restart. It does not remove failed, interrupted, active, or
cleanup-withheld evidence. `loom-node-agent workers status` reports its latest
health and run evidence; ordinary Lane status and Portal views do not invoke
it.

Storage mount desired state:

```bash
loom-node-agent storage-mount status
loom-node-agent storage-mount enable --repair
loom-node-agent storage-mount repair-once --force
loom-node-agent storage-mount disable
```

At verification time, storage-mount status returned actual state, desired
state, host, protocol, mount path, policy path, status path, and cloud storage
fields. Background mount repair must not prompt for credentials; missing SMB
credentials should be handled by an explicit user mount and Keychain save.

## Private Backup

Private backup commands push raw private payloads without indexing. Treat them
as operator workflows:

```bash
loom-node-agent private-backup --help
```

Use [[Backups Cloud And Restore]] and [[Storage Retention And Safe Delete]]
when deciding whether a private backup is operationally needed.

## Related Docs

- [[Nodes Communication Sync And Setup CLI Reference]]
- [[Node Enrollment And Watched Roots]]
- [[Main And Mac Operations]]
- [[Files Storage And Lane]]
