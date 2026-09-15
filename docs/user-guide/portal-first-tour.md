---
title: "Portal First Tour"
description: "Tour the LOOM terminal portal from Home through the major user, data, automation, and admin surfaces."
audience:
  - user
  - operator
tags:
  - loom
  - user-guide
  - portal
status: draft
verified_at: "2026-08-17"
source_scope:
  - "go run ./cmd/loom enter --help"
  - "go run ./cmd/loom --no-animation --no-color enter --exit-after-render --no-boot-animation --start home"
  - "go run ./cmd/loom --no-animation --no-color enter --exit-after-render --no-boot-animation --start doctor"
  - "internal/loomcli/portal/screens.go"
  - "internal/loomcli/portal/snapshot.go"
  - "internal/loomcli/portal/loaders.go"
  - "internal/loomcli/portal/protected_folder_actions.go"
  - "internal/loomcli/portal/protected_folder_action_executor.go"
  - "internal/loomcli/portal/nodes_render.go"
related:
  - "[[Nodes And Capabilities Portal]]"
  - "[[Backups Cloud And Restore]]"
  - "[[Navigation And Command Mode]]"
  - "[[Home And Doctor]]"
  - "[[Daily Use]]"
  - "[[Portal Guide]]"
aliases:
  - "LOOM Portal Tour"
---
# Portal First Tour

## What This Page Covers

This page gives a first pass through the terminal portal. It focuses on where
to look, not on every action inside every screen.

For exact keys, command mode, and one-shot render options, see
[[Navigation And Command Mode]].

## Open The Portal

Run:

```bash
loom enter
```

The default start screen is Home. You can start at a specific screen:

```bash
loom enter --start doctor
loom enter --start projects
loom enter --start notes
```

Slice 4 verified that `loom enter --help` lists these start screen aliases:

```text
home, doctor, timeline, box, notes, storage, projects, database, background,
automations, jobs, nodes, capabilities
```

## One-Shot Smoke Render

When you need to verify that a portal screen renders without opening an
interactive session, use:

```bash
loom --no-animation --no-color enter --exit-after-render --no-boot-animation --start home
```

One-shot renders are verified for Home, Doctor, and a representative main-owned
surface. They also work when the selected main target is deliberately
unreachable.

## If Main Is Offline

You do not need to restart the portal or pass a special flag. The first failed
main health probe switches the portal into an explicit offline state.

Start with these surfaces:

1. Home shows the selected main URL or Unix socket, the last check time, and
   local Box, Lane, Dropzone, and update information. It does not turn missing
   global data into healthy zero counts.
2. Doctor shows one `Main node is unreachable` finding alongside independent
   local findings.
3. LOOM Box remains locally inspectable and marks main-backed watch and
   registration status unavailable.

Main-owned surfaces still open, but they show `Main is offline` rather than
stale or empty records. Navigation, command previews, and command completion
continue to work. Main-dependent actions and commands, including Protect
Folder, are unavailable.

After restoring main or the network route, press `r` on the current screen. The
portal probes main again and reloads that same screen without a restart.

## Portal Groups

The current portal groups are source-verified in
`internal/loomcli/portal/screens.go`.

Daily:

- Home;
- Doctor;
- Projects;
- LOOM Notes;
- LOOM Box.

Data:

- LOOM Main Storage;
- Timeline.

Automation:

- Automation Center;
- Jobs.

Network And Admin:

- Nodes And Watched Roots;
- Capabilities And Providers;
- Background Operations;
- Object Store Diagnostics.

## Tour Order

Use this first-tour order:

1. Home: confirm main availability first, then the system summary and recent
   activity when online.
2. Doctor: check whether any local or main issue needs attention.
3. Projects: inspect active project state.
4. LOOM Notes: inspect notes roots, indexed objects, and search status.
5. LOOM Box: inspect local Box, Lane, Dropzone, and watched roots.
6. LOOM Main Storage: inspect generated storage and retained data views.
7. Timeline: inspect recent operational activity.
8. Automation Center: inspect schedules, direct events, and automation state.
9. Jobs: inspect jobs, indexing, and related background work.
10. Nodes And Watched Roots: inspect nodes, protect an extra folder, and review
    active watched source roots.
11. Capabilities And Providers: inspect capability/provider state.
12. Background Operations: inspect workers and long-running background state.
13. Object Store Diagnostics: inspect object-store and index diagnostics.

Not every surface should be used every day. Home, Doctor, Projects, Notes, and
Box are the primary daily surfaces.

## Try Protect Folder

On **Nodes And Watched Roots**, **Protected Folders** is the user-facing view;
**Active Watched Roots** remains the full technical inventory. Choose **Protect
Folder** for an extra node-local folder that is not already project-owned.

Select the owner node, enter its absolute folder path, and start preflight. The
Portal saves pending work so you can leave and return. Review the effective
managed policy and protected/ignored counts before confirming. Blocking
findings keep confirmation disabled.

After creation, follow the lifecycle instead of reading “queued” as success:

- **Waiting For Node** means the owner is unavailable for pending work;
- **Activating** means current apply/report evidence is incomplete;
- **Active** means current configuration is applied and reported;
- **Protected** adds an accepted backup after that current acknowledgement;
- **Attention** identifies evidence that needs review.

Use the details actions to recheck, retry activation, disable, enable, or delete
the contract. Delete requires strong confirmation and does not remove the
source folder or retained backups. See [[Backups Cloud And Restore]] for the
complete workflow.

## What To Ignore At First

Avoid running actions from deeper admin surfaces until you understand the
workflow. Some actions are read-only, some are dry-run friendly, and some can
change production-like state.

When the portal offers an action, read its label and prefer inspect, status,
dry-run, or details actions before repair, cleanup, archive, update, or apply
actions.

## If Navigation Looks Wrong

If cursor navigation opens the wrong surface, command mode routes to the wrong
action, or a screen exposes actions that do not match the current context, that
is a portal bug. Record it in the active bug report during docs acceptance work
instead of teaching users to work around it.

## Related Docs

- [[Navigation And Command Mode]]
- [[Home And Doctor]]
- [[Daily Use]]
- [[Portal Guide]]
- [[Nodes And Capabilities Portal]]
- [[Backups Cloud And Restore]]
