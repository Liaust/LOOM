---
title: "Storage And Timeline Portal"
description: "Use Portal to inspect typed physical-root, catalog, transfer, backup, archive, safe-delete, migration, failure, and recent activity state."
audience:
  - user
  - operator
tags:
  - loom
  - portal
  - storage
status: verified
verified_at: "2026-08-27"
source_scope:
  - "internal/loomcli/portal/storage.go"
  - "internal/loomcli/portal/storage_render.go"
  - "internal/loomcli/portal/action_selectors.go"
  - "internal/loomcli/portal/repair_suggestions.go"
related:
  - "[[LOOM Box Portal]]"
  - "[[Files Storage And Lane]]"
  - "[[Storage Retention And Safe Delete]]"
aliases:
  - "Storage Portal"
  - "Timeline Portal"
---
# Storage And Timeline Portal

Open Portal with `loom enter`, then choose Storage or Timeline. Portal uses the
same typed API/client contracts as the CLI; it does not scan or materialize a
generated filesystem tree.

## Storage Surface

The first view summarizes:

- configured physical root health;
- bounded catalog entries and selected-entry details;
- transfer and custody status;
- main Documents, backup, archive, retention, fidelity, and safe-delete state;
- active failures/attention;
- cloud and mount status when configured.

Human view paths remain useful for navigation, while internal absolute paths
and raw IDs are kept secondary unless inspection needs them.

## Actions Preserved

Portal keeps typed actions for:

- inspect/open storage status;
- inspect and safely repair Lane promotion/catalog/cleanup phases;
- acknowledge/archive appropriate transfer attention;
- backup coverage/contracts and archive workflows;
- safe-delete and fidelity inspection;
- canonical filesystem migration inspection;
- distinct Main Box and Storage mount controls.

Portal does not offer storage export refresh, rebuild, repair, create, link, or
delete actions.

## Transfer Status

`cataloged` and `local_cleanup_done` are terminal success. Incomplete phases
such as `accepted_on_main`, typed failures, obsolete migration state, and
cleanup-withheld appear as attention rather than running or neutral success.

`source_cleanup_failed` may mean canonical custody/catalog and local cleanup
already succeeded while only main transport staging remains. The rendered
action describes the exact remaining idempotent work.

## Legacy Export Attention

Historical storage-export stale/failed records remain intelligible. Portal
classifies them as legacy export failures and routes to Storage inspection only
(`storage.open`). It exposes no repair action and never suggests “Storage
Refresh.”

## Timeline

Timeline presents recent durable activity and failures. Treat it as navigation
into typed state, not as proof that bytes are safe to delete. Open the relevant
storage, backup, transfer, or archive detail before acting.

## Offline Behavior

Portal keeps local Box/source information available while marking backend
status unavailable/degraded. It must not convert an offline or malformed
backend response into success.

## Related Docs

- [[LOOM Box Portal]]
- [[Files Storage And Lane]]
- [[Storage Retention And Safe Delete]]

## Complete live reference and preserved operational detail

The canonical guidance above is the current storage contract. The detailed material below preserves the complete live command, route, workflow, safety, interpretation, and troubleshooting coverage that predates the filesystem migration. Retired generated-export behavior is retained only when explicitly labelled as compatibility or historical context.

### What This Page Covers

This page covers two portal surfaces:

- LOOM Main Storage, for current storage safety and protection state;
- Timeline, for recent operations across storage, notes, jobs, automation,
  backup, cloud, and diagnostics.

Open them with:

```bash
loom enter --start storage
loom enter --start timeline
```

Slice 7 verified both one-shot renders.

### LOOM Main Storage

The Storage surface starts with storage safety because users need to know
whether files are accepted, protected, pending, or failing before browsing
details.

Verified top-level sections:

- Storage Safety;
- Available Actions;
- User Storage;
- Backup And Cloud Protection.

### Storage Safety

The historical list below used to describe Storage Safety. Current Portal
Storage instead reports typed canonical filesystem roots, catalog health,
retention, archive, backup, and safe-delete evidence:

- view key;
- total entries;
- files and directories;
- writable count;
- read-only count;
- legacy export state/findings only when old attention records exist;
- retention safe and unsafe candidates;
- tombstoned count;
- backup pending and failed counts;
- skipped-too-large count;
- retained or snapshot count;
- database pressure when relevant.

The former `loom-main` export/refresh indicators are retired. Use the physical
root and catalog status rows to interpret current health.

### Available Actions

At verification time, the surface showed:

| Action | Safety | Meaning |
|---|---|---|
| Storage Filesystem Status | inspect | Inspect canonical physical roots and migration state. |
| Storage Catalog Status | inspect | Inspect bounded catalog health and failures. |
| Retention Status | inspect | Inspect retention and tombstone state. |
| Main Documents Status | inspect | Inspect writable main `Documents` import status. |
| Mount Helper Status | inspect | Inspect Mac local mount helper readiness. |
| Inspect Cloud Cooldown | inspect | Inspect cached cloud remote state. |
| Refresh Main Cloud Status | safe-run | Run live cloud status refresh. |

Cloud-specific behavior belongs to [[Cloud Storage]] and
[[Backups Cloud And Restore]]. The Storage surface includes cloud summary only
because cloud is part of protection state.

### User Storage

User Storage lists top-level view paths. Typical rows include:

```text
main/Documents
main/Archive
macbook/Backups
macbook/Lane
```

Each row shows a short description and status such as writable, archive,
read-only, or transfer.

The important distinction is:

- `main/Documents` is writable main storage;
- `main/Archive` is controlled archive state;
- `macbook/Backups` identifies watched-root backup custody in the catalog;
- `macbook/Lane` identifies Lane imports custody in the catalog.

Do not treat all visible rows as writable folders.

### Backup And Cloud Protection

This section combines protection signals:

- main `Documents` import state;
- file-transfer counts;
- Lane status when available;
- cloud status and backend;
- remote cloud state;
- main backup status and latest successful backup;
- mount helper note.

The MacBook mount note says SMB is used by default through the Mac storage
helper, while physical-root/catalog status is backend state. This distinction
matters when the local mount is unavailable but canonical custody is healthy,
or the reverse.

### Timeline

The Timeline surface is an activity and attention view. It aggregates:

- jobs;
- workers;
- storage;
- notes indexes;
- automation;
- backup and cloud;
- snapshot summaries;
- partial portal data.

The surface has two main sections:

- Recent Failures Or Attention;
- Full History.

When running operations, use Timeline to confirm whether work is active,
succeeded, failed, cancelled, indexed, or waiting for attention.

### Timeline Rows

Rows show:

- time;
- status;
- domain badge;
- title;
- progress or status summary.

Storage rows use the storage domain badge for Lane push, custody/catalog
repair, migration, and related storage events. Notes index rows use the notes badge. Backup and
cloud rows use the backup badge.

Selecting a row exposes an inspect action with event ID, domain, kind, target,
summary, error code, error message, related action, and correlation ID.

### When To Record A Bug

Record a portal bug if:

- Storage Safety counts contradict `loom storage tree`, `list`, or `retention
  status`;
- canonical read-only custody or a generated projection is shown as a writable
  source;
- unsafe retention candidates are hidden from Storage Safety;
- file transfer failures are absent from both Storage and Timeline;
- Timeline marks cancelled or completed work as active;
- a storage event has no useful inspect details or correlation ID.

Historical v0.9.6 findings remain archived. Capture new bugs through the current
`.project/` workflow.
