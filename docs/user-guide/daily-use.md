---
title: "Daily Use"
description: "Use LOOM day to day by checking readiness, opening the portal, and moving to the right workflow surface."
audience:
  - user
  - operator
tags:
  - loom
  - user-guide
status: draft
verified_at: "2026-07-07"
source_scope:
  - "go run ./cmd/loom --json status"
  - "go run ./cmd/loom --json health"
  - "go run ./cmd/loom enter --help"
  - "internal/loomcli/portal/screens.go"
  - "internal/loomcli/portal/home.go"
  - "internal/loomcli/portal/doctor.go"
related:
  - "[[Getting Started]]"
  - "[[Portal First Tour]]"
  - "[[Projects]]"
  - "[[Notes And Search]]"
  - "[[Files Storage And Lane]]"
  - "[[Backups Cloud And Restore]]"
  - "[[Automation]]"
aliases:
  - "Daily LOOM Workflow"
---
# Daily Use

## What This Page Covers

This page describes the normal daily loop for using LOOM without going straight
to implementation details.

The loop is:

1. check readiness;
2. open Home;
3. inspect Doctor only if needed;
4. move to the surface for the task;
5. use dry-runs or fixture paths for risky work;
6. collect support evidence if state looks wrong.

## Daily Readiness

Run:

```bash
loom status
loom health
```

`loom status` answers whether LOOM looks ready for work. It summarizes daemon
health, jobs, recent events, search/index attention, runner status, and next
inspection commands.

`loom health` answers whether `loomd` itself is healthy. It is narrower than
status and is useful when you suspect a daemon, database, storage, migration, or
bootstrap problem.

Use JSON when you are comparing results in a test log:

```bash
loom --json status
loom --json health
```

## Open The Daily Surface

Run:

```bash
loom enter
```

The Home surface is the default. Home is intentionally a summary. It should put
the most important operational facts at the top before deeper screens:

- diagnostics: LOOM, daemon, node, capture time;
- network: nodes and events;
- storage: Box, Lane, Dropzone, watched roots;
- jobs: daemon, database, workers, jobs;
- recent activity;
- navigation to the rest of the portal.

If Home says "No failures", you can usually move directly to your task.

## Use Doctor For Attention

Doctor is the prioritized repair surface. It is built from Home attention items
and partial portal load errors.

Use Doctor when:

- Home shows failures or warnings;
- a portal screen partially loaded;
- a job, worker, node, notes, storage, cloud, backup, or project issue needs a
  safe next action;
- you need a human-readable summary before collecting a support bundle.

Doctor classifies issues into areas such as system, database, workers, jobs and
indexing, automation, nodes and sync, Box/Lane, storage, cloud/backup, notes,
projects, portal, and unknown.

## Task Surfaces

After readiness checks, use the surface that matches the task:

| Goal | Start Here |
|---|---|
| Create or inspect a project | [[Projects]] |
| Search notes or check indexing | [[Notes And Search]] |
| Move files or inspect Box/Lane | [[Files Storage And Lane]] |
| Inspect backups or cloud status | [[Backups Cloud And Restore]] |
| Run schedules, scripts, jobs, or workers | [[Automation]] |
| Inspect nodes, watched roots, and capabilities | [[Portal First Tour]] and later node/admin docs |
| Collect evidence for a problem | [[Diagnostics And Support]] |

The linked workflow pages are written in later slices. Until those pages are
complete, treat this table as navigation, not a full procedure.

## Safe Daily Habits

- Prefer read-only commands first.
- Use `--json` when you need exact evidence.
- Use portal Home and Doctor before guessing at repairs.
- Use `.loom-acceptance` for test artifacts.
- Use `--dry-run` for backup, restore, cleanup, retention, cloud, purge,
  update, and maintenance workflows when available.
- Do not write directly into generated backup views.
- Do not bypass LOOM policy with raw shell for normal LOOM actions.

## When Something Looks Wrong

If a command fails or the portal shows attention:

1. Run `loom health`.
2. Run `loom status`.
3. Open Doctor.
4. Follow the safe next inspection.
5. Use [[Diagnostics And Support]] if you need a support bundle.

Do not document or normalize a workaround that changes production-like state
without a reviewed plan.

## Related Docs

- [[Getting Started]]
- [[Portal First Tour]]
- [[Home And Doctor]]
- [[Runtime Status And Support]]
- [[Diagnostics And Support]]
