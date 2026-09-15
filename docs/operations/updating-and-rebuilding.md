---
title: "Updating And Rebuilding"
description: "Operator runbook for LOOM update planning, release staging, rebuilds, rollback boundaries, and safe validation."
audience:
  - operator
  - developer
tags:
  - loom
  - operations
  - configuration
status: draft
verified_at: "2026-07-07"
source_scope:
  - "/tmp/loomdocs-v096 update --help"
  - "/tmp/loomdocs-v096 --json update status"
  - "/tmp/loomdocs-v096 --json update workspace status"
  - "go run ./cmd/loom --json update maintenance status --state-dir <empty-temp-dir>"
  - "go run ./cmd/loom --json setup workspace install --release-id <release-id> --yes"
  - "internal/setup/launchd.go"
  - "internal/update/workspace.go"
related:
  - "[[Operations]]"
  - "[[Backup Restore And Drills]]"
  - "[[Database Maintenance]]"
  - "[[Release Flow]]"
aliases:
  - "Production Updates"
  - "Rebuild Runbook"
---
# Updating And Rebuilding

## Operating Model

Production-like updates are not development smokes. Use read-only checks,
update plans, backup gates, focused acceptance, and explicit rollback
boundaries.

Main production identity:

```text
host: loom-main
LOOM node: main
active source: /srv/loom/current
update state: /var/lib/loom/update
```

Workspace update identity on Mac uses local state under
`~/.local/state/loom/update`.

Physical release copies are bounded separately from update history. Main uses
`/srv/loom/releases`; Mac uses `~/.local/share/loom/releases`. Retention never
deletes update manifests or Git history.

## Safe Inspection

Start with:

```bash
loom update status
loom update history
loom update workspace status
loom update workspace history
loom backup status
loom backup coverage
loom health
```

At verification time, `loom update status --json` reported no active local
production update manifest under `/var/lib/loom/update`, and `loom update
workspace status --json` reported an active workspace manifest with 20 history
entries.

## Plan Before Apply

Production plan:

```bash
loom update plan --release-path /srv/loom/releases/<release-id>
```

Workspace plan:

```bash
loom update workspace plan --release-path <local-release-path>
```

Review release path, migration delta, backup requirement, service impact, and
rollback class. If the plan is blocked, stop.

## Backup Gate

Before production apply:

```bash
loom backup create --dry-run --production
loom backup create --production --reason "operator reviewed"
loom backup verify <backup-ref-or-path>
loom backup coverage
```

For stronger confidence, run a restore drill on a node that can read the backup
directory locally:

```bash
loom backup restore-drill <backup-ref-or-path> --dry-run --target-database loom_restore_drill_update
```

Do not use update apply to skip backup review.

## Apply And Rollback

Production apply from the trusted Mac operator wrapper:

```bash
scripts/loom-main update apply \
  --release-path /srv/loom/releases/<release-id> \
  --backup-path /var/lib/loom/backups/main/<backup-directory> \
  --backup-ref <maintenance-operation-id> \
  --yes
```

The wrapper performs planning, optional backup creation, and the update
transaction as `loomadmin`. It passes the immutable directory and its registered
maintenance operation ID separately. During apply, the configured loomd Unix
socket verifies that operation and directory as the `loom` service identity;
the coordinating process cannot substitute a path or use its own inaccessible
filesystem view. Only the fixed `nixos-rebuild` step uses the existing
Keychain/TTY sudo mechanism. Direct production `loom update apply` is supported
only when both `--backup-path` and its corresponding `--backup-ref` are supplied
and loomd is available. The wrapper rejects unsafe whitespace or shell
metacharacters in remote arguments before SSH.

Service-only rollback:

```bash
loom update rollback --service-only --yes
```

Restore-required rollback:

```bash
loom update rollback --restore-required --yes
```

`--restore-required` is a boundary marker. It does not casually perform a
destructive PostgreSQL restore.

Mac workspace releases use a local flow. The setup installer builds and stages
the release under `~/.local/share/loom/releases`, switches
`~/.local/share/loom/current`, writes setup state, and ensures the
`local.loom.node-agent` LaunchAgent is loaded:

```bash
loom setup workspace install --release-id <release-id> --yes
```

After a direct workspace install, align the workspace update manifest with the
active release:

```bash
loom update workspace apply --release-path "$(readlink ~/.local/share/loom/current)" --yes
```

This second step makes `loom update workspace status` report the same commit
and release path that the active symlink uses. It also refreshes
`~/.local/bin/loom`, `~/.local/bin/loom-node-agent`, and restarts the
LaunchAgent without unloading it first.

## Maintenance Window

Update apply can pause active schedules and direct-event endpoints, record what
was paused, switch the release, rebuild, check health, and resume only the
resources it paused.

Expected commands:

```bash
loom update maintenance status
loom update maintenance resume --yes
```

`loom update maintenance status` returns `Status: none` when no active update
manifest exists. That is a healthy empty state: no update maintenance window is
currently recorded. If an active manifest exists and records paused schedules or
direct-event endpoints, inspect the status before running resume.

## Prune Old Release Copies

Inspect current release retention without mutation:

```bash
loom update release retention plan
loom update release retention plan --workspace
```

Review every deletion candidate, the active and rollback paths, explicit pins,
and the plan hash. Then apply the unchanged plan:

```bash
loom update release retention apply --plan-hash <sha256:...> --yes
loom update release retention apply --workspace --plan-hash <sha256:...> --yes
```

The defaults retain ten successful deployment targets and all releases younger
than 48 hours. Use repeatable `--pin <release-name>` flags for known-good
releases that must survive beyond those windows. Apply refuses if release
state changes after review or if evidence is incomplete.

## Hardware Rebuilds

NixOS rebuilds on main are operator work. Use the main-node runbook, confirm
network/auth first, and avoid combining rebuilds with unrelated data-changing
operations.

Safe prechecks:

```bash
ssh loom-main 'loom status'
ssh loom-main 'systemctl is-active loomd postgresql wireguard-wg0 sshd'
ssh loom-main 'ip -br addr show wg0'
```

After rebuild, run focused checks:

```bash
loom status
loom health
loom backup status
loom maintenance status
loom node health main
```

## Acceptance Hygiene

When production-only validation needs files, write them under
`.loom-acceptance` scenario folders. Do not run generic development smoke
suites on production, and do not leave loose files in user storage roots.

## Related Docs

- [[Backup Restore And Drills]]
- [[Database Maintenance]]
- [[Main And Mac Operations]]
- [[Release Flow]]
