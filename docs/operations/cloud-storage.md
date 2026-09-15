---
title: "Cloud Storage"
description: "Operator runbook for LOOM cloud status, Borg snapshot backend, remote locks, cloud snapshot upload, verification, retention, and recovery."
audience:
  - operator
  - developer
tags:
  - loom
  - operations
  - cloud
  - backup
status: draft
verified_at: "2026-07-07"
source_scope:
  - "/tmp/loomdocs-v096 --json cloud status --cached"
  - "/tmp/loomdocs-v096 --json cloud doctor"
  - "/tmp/loomdocs-v096 --json cloud snapshot list"
  - "/tmp/loomdocs-v096 --json cloud snapshot retention status --keep-latest 14"
  - "/tmp/loomdocs-v096 --json cloud snapshot backend status --cached"
  - "go run ./cmd/loom --json cloud snapshot push --dry-run --backup latest --backup-root <empty-temp-dir>"
  - "internal/cloudstorage/config.go"
  - "internal/workers/runtimes/cloud_snapshot.go"
related:
  - "[[Operations]]"
  - "[[Backups Cloud And Restore]]"
  - "[[Backup Sync Retention And Cloud]]"
  - "[[Backup Restore And Drills]]"
  - "[[Configuration]]"
aliases:
  - "Cloud Snapshot Operations"
  - "Hetzner Storage Box"
---
# Cloud Storage

## Purpose

This runbook covers the LOOM cloud layer used for disaster recovery snapshots
and cloud readiness checks. The configured production-like cloud provider is
Hetzner Storage Box through rclone and Borg.

LOOM cloud storage is not a general writable sync folder. LOOM should operate
inside its configured remote root, normally `loom`.

## Configuration Model

The default cloud config path is:

```text
/etc/loom/cloud/config.json
```

Important defaults from `internal/cloudstorage/config.go`:

| Field | Default |
|---|---|
| provider | `hetzner_storage_box` |
| driver | `rclone` |
| remote name | `loom-cloud` |
| remote root | `loom` |
| state dir | `/var/lib/loom/cloud` |
| rclone config | `/etc/loom/cloud/rclone.conf` |
| snapshot backend default | `legacy_tree` |
| Borg binary | `borg` |
| Borg compression | `zstd,6` |
| Borg check mode | `repository` |
| remote lock worker wait | `30` seconds |
| remote lock effectful wait | `300` seconds |

Current main-backed status verified a Borg snapshot backend. Missing local
cloud config on Mac can still make local-only cloud commands report disabled.
Use main-backed commands for main cloud state unless intentionally inspecting
local config.

## Safe Status Checks

Cached:

```bash
loom cloud status --cached
loom cloud snapshot backend status --cached
```

Live:

```bash
loom cloud doctor
loom cloud snapshot status
loom cloud snapshot list
```

At verification time, cached cloud status was healthy and enabled, backend
status was `ok`, cloud doctor was `ok`, and snapshot list returned an empty
Borg inventory.

## Remote Lock Behavior

LOOM serializes cloud operations because the Storage Box connection budget is
small. A live cloud command can return:

```text
lock_busy
```

That usually means another LOOM cloud probe or worker is currently using the
remote lock. Wait and retry. Do not immediately clean, reinitialize, or delete
remote content.

## Backend Doctor And Init

Inspect backend state from main:

```bash
loom cloud snapshot backend status --cached
loom cloud snapshot backend status --live
```

Initialize only after config, remote SSH/rclone access, and Borg passphrase
handling are correct:

```bash
loom cloud snapshot backend init --confirm
```

Without `--confirm`, init returns
`cloud.snapshot_backend_init_confirm_required`.

`loom cloud snapshot backend doctor` currently runs local CLI diagnostics
against the config available to the command process. From Mac, that can report
local cloud disabled even while main-backed status is healthy.

## Snapshot Upload

Preferred autonomous path:

```bash
loom worker run main.cloud_snapshot_upload --once
```

The worker:

- skips if cloud is disabled;
- skips if another operation holds the remote lock;
- skips cleanly if no local backup manifest exists;
- dry-runs the latest local backup before upload;
- verifies an existing matching snapshot before skipping upload;
- records maintenance operation evidence for real uploads.

Direct operator path, run where main backups and cloud config are readable:

```bash
loom cloud snapshot push --backup latest --dry-run
loom cloud snapshot push --backup latest
```

Direct push is local to the CLI process. It does not delegate to main loomd.
When the latest backup is not readable under the local backup root, the command
returns `cloud.snapshot_push_backup_missing` instead of a generic runtime
error. The repair path is to run the direct push on the node that owns the
backup, pass `--backup-root` to a readable local main backup root, or use the
main cloud snapshot upload worker.

## Snapshot Inventory And Verification

```bash
loom cloud snapshot list
loom cloud snapshot verify latest
```

When the inventory is empty, `list` can still be healthy. Empty means there are
currently no valid snapshots for the node in the configured backend. It does
not mean cloud is disabled.

Normal snapshot verification delegates to the LOOM service context. This is the
operator-safe path from Mac and from non-service shells on main because Borg
credentials, the cloud config, and the remote lock live under service-owned
paths. Use `loom cloud snapshot verify latest --local` only when intentionally
testing the current process context. A local missing config returns
`cloud.snapshot_verify_local_config_missing`; a local credential or lock
permission failure returns `cloud.snapshot_verify_service_context_required`.

## Retention

Plan first:

```bash
loom cloud snapshot retention status --keep-latest 14
loom cloud snapshot retention plan --keep-latest 14
```

Apply only after review:

```bash
loom cloud snapshot retention apply --keep-latest 14 --confirm
```

For Borg, apply deletes old matching LOOM archives. For legacy tree snapshots,
apply moves old valid snapshots into retention trash.

## Fetch And Restore Drill

Fetch to an empty staging directory:

```bash
loom cloud snapshot fetch latest --to /var/lib/loom/cloud/restore-drills/manual/backup
```

Plan a drill:

```bash
loom cloud snapshot restore-drill latest \
  --dry-run \
  --target-database loom_restore_drill_cloud
```

Use [[Backup Restore And Drills]] for the full drill procedure.

## Related Docs

- [[Backups Cloud And Restore]]
- [[Backup Restore And Drills]]
- [[Backup Cloud And Maintenance CLI Reference]]
- [[Configuration]]
- [[Environment Variables]]
