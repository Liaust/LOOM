---
title: "Nodes Communication Sync And Setup CLI Reference"
description: "CLI reference for LOOM nodes, durable communication, sync, watched roots, setup, bootstrap, and update commands."
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
  - "/tmp/loomdocs-v096 node --help"
  - "/tmp/loomdocs-v096 sync --help"
  - "/tmp/loomdocs-v096 watched-roots --help"
  - "/tmp/loomdocs-v096 setup --help"
  - "/tmp/loomdocs-v096 update --help"
  - "/tmp/loomdocs-v096 --json node list --limit 10"
  - "/tmp/loomdocs-v096 --json sync status --node macbook"
  - "/tmp/loomdocs-v096 --json watched-roots status --limit 5"
  - "/tmp/loomdocs-v096 --json setup status"
  - "/tmp/loomdocs-v096 --json update status"
  - "go run ./cmd/loom --json update maintenance status --state-dir <empty-temp-dir>"
  - "internal/setup/launchd.go"
  - "internal/update/workspace.go"
related:
  - "[[CLI Reference]]"
  - "[[Main And Mac Operations]]"
  - "[[Node Agent CLI Reference]]"
  - "[[Daemon CLI Reference]]"
  - "[[Node Enrollment And Watched Roots]]"
aliases:
  - "Nodes CLI"
  - "Setup CLI"
  - "Sync CLI"
---
# Nodes Communication Sync And Setup CLI Reference

## Safety Classes

| Command | Safety Class | Notes |
|---|---|---|
| `loom node list`, `node inspect`, `node health` | read-only | Main-backed node inventory and health. |
| `loom node enrollment-request list/inspect`, `node enrollment-token` inspection | read-only or credential-sensitive | Enrollment state and tokens. |
| `loom node enrollment-request approve/deny`, `node credential issue`, `node enrollment-token create` | operator-mutating | Creates or changes node access. |
| `loom communication health`, `messages list`, `message inspect` | read-only | Durable node message status. |
| `loom message enqueue` | operator-mutating | Enqueues main-to-node work. |
| `loom sync status --node`, `sync batches/conflicts/replicas/private-backups/deletion-requests` | read-only | Main-backed sync state. |
| `loom sync deletion-request review/approve/deny/complete` | operator-mutating | Changes deletion-request review state. |
| `loom watched-roots status/findings/failures/backups` | read-only | Main-backed watched-root reports. |
| `loom setup status/plan/doctor/manifest` | local read-only | Local install and setup diagnostics. |
| `loom setup apply/repair/workspace install/uninstall apply` | operator-mutating | Changes local install, services, or data retention. |
| `loom bootstrap ssh` | operator-mutating | Delivers source to a trusted SSH target. |
| `loom update status/history/manifest/workspace status` | read-only | Local update manifest state. |
| `loom update plan`, `update workspace plan` | dry-run planning | Requires a release path but does not apply. |
| `loom update release stage`, `update apply`, `update rollback`, `update workspace apply/rollback` | operator-mutating | Changes release symlinks, services, or production state. |

## Nodes

Inspect node inventory:

```bash
loom node list --limit 10
loom node inspect main
loom node health main
loom node health macbook
```

At verification time, `node list --json` returned two active online nodes:
`main` and `macbook`. `node health <node-ref>` requires a node ref and returns
heartbeat age plus pending, failed, and dead-letter message counters.

Enrollment and credentials are sensitive:

```bash
loom node enrollment-request list --limit 20
loom node enrollment-request inspect <request-ref>
loom node enrollment-request approve <request-ref>
loom node credential issue <node-ref>
loom node enrollment-token create
```

Approving enrollment, issuing credentials, or creating tokens should be done as
an operator action. Do not paste credential material into docs or bug reports.

## Durable Communication

Communication is the durable message layer between main and nodes:

```bash
loom communication health
loom messages list --limit 20
loom messages list --node macbook --status pending
loom message inspect <message-ref>
```

At verification time, communication health returned total, online, recently
seen, and offline node counts plus pending, failed, and dead-letter message
counts.

Enqueue is effectful:

```bash
loom message enqueue --node macbook --kind main.ping --input '{}'
```

Use an idempotency key for repeated operator attempts:

```bash
loom message enqueue --node macbook --kind main.ping --input '{}' --idempotency-key manual-ping-001
```

## Sync

Main-backed sync inspection:

```bash
loom sync status --node macbook
loom sync batches --node macbook --limit 20
loom sync conflicts --node macbook --limit 20
loom sync replicas --node macbook --limit 20
loom sync private-backups --node macbook --limit 20
loom sync deletion-requests --node macbook --limit 20
```

`sync status` requires `--node`. Without it, the CLI returns
`sync.node_required` with a clear error.

Deletion requests are review records, not direct deletes:

```bash
loom sync deletion-request inspect <deletion-request-id>
loom sync deletion-request review <deletion-request-id>
loom sync deletion-request approve <deletion-request-id>
loom sync deletion-request deny <deletion-request-id> --reason "not safe"
loom sync deletion-request complete <deletion-request-id>
```

Use [[Storage Retention And Safe Delete]] before approving or completing a
deletion request.

## Watched Roots

Main-backed watched-root reports:

```bash
loom watched-roots status --limit 20
loom watched-roots status --node macbook --include-fidelity --limit 20
loom watched-roots findings --limit 20
loom watched-roots failures --limit 20
loom watched-roots backups status
loom watched-roots backups batches --limit 20
loom watched-roots backups items --limit 20
```

At verification time, status returned reports for watched roots, findings and
failures returned zero rows, and backup status returned accepted, artifact,
batch, deletion marker, duplicate, failed, item, skipped, and byte counters.

## Setup And Bootstrap

Local setup inspection:

```bash
loom setup status
loom setup plan
loom setup doctor
loom setup manifest path
loom setup manifest inspect
```

These commands inspect the local machine and do not use the normal service
envelope. At verification time, Mac setup status reported `configured`, the
install manifest was loaded, Box was configured, the node-agent LaunchAgent was
running, and enrollment had a configured credential.

Apply, repair, workspace install, and uninstall apply are operator actions:

```bash
loom setup apply
loom setup repair --yes
loom setup workspace install
loom setup uninstall plan
loom setup uninstall apply --mode preserve-data --yes
```

On macOS workspace installs, setup manages `local.loom.node-agent` as a
user-level LaunchAgent. The setup path is idempotent: it checks whether the
LaunchAgent is already loaded, avoids a blind `bootout`, bootstraps only when
the job is missing, enables the job, and then kickstarts it. Transient launchd
bootstrap and kickstart errors are retried or verified against the running
LaunchAgent before setup reports a blocking service failure.

Production main uninstall has extra guardrails. Do not run purge without a
reviewed backup ref and explicit node confirmation.

SSH bootstrap is also an operator action:

```bash
loom bootstrap ssh loom-main
```

Use it only for trusted targets.

## Updates

Read-only update inspection:

```bash
loom update status
loom update history
loom update workspace status
loom update workspace history
```

At verification time, local production update status had no active manifest
under `/var/lib/loom/update`, while workspace update status had an active
workspace manifest and 20 history entries.

Planning:

```bash
loom update plan --release-path /srv/loom/releases/<release-id>
loom update workspace plan --release-path <local-release-path>
```

Apply and rollback:

```bash
loom update apply --release-path /srv/loom/releases/<release-id> --backup-path <backup-ref-or-path> --yes
loom update rollback --service-only --yes
loom update workspace apply --release-path <local-release-path> --yes
loom update workspace rollback --yes
```

For Mac workspace releases installed through `loom setup workspace install`,
run a workspace update apply against the active release afterward when you need
operator status to match the actual symlink:

```bash
loom update workspace apply --release-path "$(readlink ~/.local/share/loom/current)" --yes
```

The install command stages and switches the local release. The workspace update
apply records that same release in `~/.local/state/loom/update`, refreshes the
binary symlinks, and restarts the LaunchAgent through the safer loaded-job
kickstart path.

`loom update maintenance status` is also read-only. When there is no active
update manifest, it returns a normal `none` state instead of an error. That
means no update maintenance window is currently recorded.

## Related Docs

- [[Node Agent CLI Reference]]
- [[Daemon CLI Reference]]
- [[Main And Mac Operations]]
- [[Updating And Rebuilding]]
- [[Node Enrollment And Watched Roots]]
