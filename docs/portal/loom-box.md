---
title: "LOOM Box Portal"
description: "Inspect local Box source folders, durable policies, workspace Lane state, Main Imports locality, watched-root status, and safe actions."
audience:
  - user
  - operator
tags:
  - loom
  - portal
  - storage
status: verified
verified_at: "2026-08-30"
source_scope:
  - "internal/loomcli/portal/box_render.go"
  - "internal/loomcli/portal/box_actions.go"
  - "internal/loomcli/portal/home.go"
  - "internal/box"
related:
  - "[[Storage And Timeline Portal]]"
  - "[[Box Storage And Lane]]"
  - "[[Files Storage And Lane]]"
aliases:
  - "Box Portal"
---
# LOOM Box Portal

Open `loom enter` and choose Box. The screen combines read-only local Box
inspection with explicit actions; it does not treat Box as a generated main
storage view.

## What You See

- resolved Box path/profile/owner;
- initialized, partial, missing, or conflicting contract state;
- `Documents`, `Notes`, `Projects`, and profile-local workspace intake;
- durable `.loom/` policies/contracts;
- workspace Lane runtime status, only on workspace nodes;
- Main Storage Imports locality, without a Main Lane surface;
- watched-root desired/applied state;
- recent/attention transfer summaries.

New installs keep volatile state outside Box. A legacy `.loom/state` tree may
be read during migration, but divergent legacy/canonical trees stop child
status and mutation.

## Safe Actions

Depending on state, Portal can offer:

- inspect/open a folder or policy;
- plan/init/repair an incomplete Box;
- plan/apply watched-root policy;
- inspect workspace Lane state;
- repair, acknowledge, or archive a compatible transfer state;
- navigate to typed Storage status.

Actions that mutate state show their command/effect and confirmation class.
Lane repair never claims to republish an export or re-upload accepted bytes.

## Status Meaning

- `cataloged`: main custody complete, local content intentionally retained;
- `local_cleanup_done`: main custody complete, reviewed local cleanup complete;
- `accepted_on_main`: custody exists but catalog work remains;
- `promotion_failed`, `catalog_failed`, `source_cleanup_failed`: stopped
  attention with phase-specific repair;
- `local_cleanup_withheld`: source changed or deletion could not be proven;
- obsolete migration state: inspect/resolve migration rather than continue
  blindly.

## Main Box Mount

The optional Main Box SMB action targets the distinct read/write
`loom-main-box` identity. Storage actions target read-only `loom-storage`.
Portal/mount helpers refuse foreign sources and collisions with direct cloud.

## Related Docs

- [[Storage And Timeline Portal]]
- [[Box Storage And Lane]]
- [[Files Storage And Lane]]

## Complete live reference and preserved operational detail

The canonical guidance above is the current storage contract. The detailed material below preserves the complete live command, route, workflow, safety, interpretation, and troubleshooting coverage that predates the filesystem migration. Retired generated-export behavior is retained only when explicitly labelled as compatibility or historical context.

### What This Page Covers

The LOOM Box surface is the daily file-workspace surface. It answers:

- is the local Box initialized and valid;
- is Lane empty, pending, or failed;
- is this a workspace Lane source or a Main Imports destination;
- which folders and policies exist;
- are Box watched roots registered and reported;
- which Box actions are available.

Open it with:

```bash
loom enter --start box
```

Verification command:

```bash
loom --no-animation --no-color enter --exit-after-render --no-boot-animation --start box
```

### Top Actions

At verification time, the Box surface exposed these actions:

| Action | Safety | Meaning |
|---|---|---|
| Initialize Box | disabled when complete | Creates the Box scaffold when missing. |
| View Watch Plan | inspect | Shows desired watched-root plan. |
| View Watch Status | inspect | Shows backend registrations and node-agent reports. |
| Create Project In Box | safe-run | Opens project creation under Box Projects. |
| Inspect Box | inspect | Shows raw Box status details. |
| Send Lane To Main | disabled when Lane is empty | Sends pending Lane files when enabled. |
| Apply Box Watch Policy | sensitive | Records Box watch policy on main. |

Disabled actions explain why they are disabled. For example, "Send Lane To
Main" is disabled when Lane has no pending items.

### Box Status

The first section shows the highest-signal status:

- Box status;
- profile;
- contract state;
- workspace Lane state, when the current Box profile is workspace;
- watch policy summary.

In details/raw mode it also shows path, owner, default project path, and
profile-local policy details. Retired intake fields remain omitted.

### Workspace LOOM Lane

The Lane section shows:

- state;
- pending item count;
- active pending item count;
- acknowledged item count;
- pending bytes;
- retained Lane recovery bytes and protected-evidence bytes;
- SSH/rsync preflight;
- pending rows;
- last transfer status when present.

Details mode separates successful-grace, protected, cleanup-quarantine,
transport-safety, and untracked recovery bytes and shows the next successful
quarantine expiry. Rendering and transfer inspection are read-only; the local
node-agent housekeeping worker owns automatic 30-minute successful-quarantine
expiry. Failed and cleanup-withheld evidence is reported but never pruned by
the Portal.

At verification time, Lane was empty, preflight was ready, and the last
transfer row showed `local_cleanup_done`.

Main profile renders no Lane section or Lane action. Its status strip identifies
Storage Imports as the destination for accepted workspace transfers.

### Retired Intake Evidence

Dropzone has no Portal section, action group, search alias, completion, raw
detail, or status field. Historical records remain available only through
bounded read-only compatibility code outside the current Portal surface.

### Folders And Policies

This section lists visible Box folders and policies:

- Documents;
- Lane, on workspace profiles only;
- Notes;
- Projects;
- Documents policy;
- Notes policy.

The portal intentionally translates policy paths into user meaning. For
example, Documents is shown as backing up general documents, while Notes is
shown as syncing and indexing markdown notes.

### Watch Policy

The Watch Policy section shows compiled watched roots:

- backend root key;
- sync mode;
- index mode;
- backup mode.

At verification time, Documents had `sync=none`, `index=metadata_only`, and
`backup=incremental_raw`. Notes had `sync=selected_files`,
`index=markdown_text`, and `backup=incremental_raw`.

### Watch Status

Watch Status compares:

- desired roots;
- registered roots;
- reported roots;
- node-agent report status.

The normal healthy state is desired roots matching registered and reported
roots, with node-agent reports healthy.

### When To Record A Bug

Record a Box portal bug if:

- `loom box status` is healthy but the portal reports an invalid Box;
- Lane has pending items but "Send Lane To Main" remains disabled;
- Main shows a Lane action, Lane section, or retired intake field;
- watch plan and watch status disagree with the CLI;
- a sensitive action runs without confirmation;
- canonical read-only custody or generated projections are presented as
  writable source folders.

Historical v0.9.6 findings remain archived. Capture new bugs through the current
`.project/` workflow.
