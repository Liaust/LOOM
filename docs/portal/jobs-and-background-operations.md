---
title: "Jobs And Background Operations Portal"
description: "Guide to the portal Jobs and Background Operations surfaces for jobs, runners, workers, maintenance, backup, cloud, and operational attention."
audience:
  - user
  - operator
tags:
  - loom
  - portal
  - jobs
  - automation
status: draft
verified_at: "2026-07-07"
source_scope:
  - "/tmp/loomdocs-v096 --no-animation --no-color enter --exit-after-render --no-boot-animation --start jobs"
  - "/tmp/loomdocs-v096 --no-animation --no-color enter --exit-after-render --no-boot-animation --start background"
  - "internal/loomcli/portal/loaders.go"
  - "internal/loomcli/portal/action_selectors.go"
  - "internal/loomcli/portal/render.go"
related:
  - "[[Portal Guide]]"
  - "[[Automation]]"
  - "[[Automation Jobs And Workers]]"
  - "[[Backup Cloud And Maintenance CLI Reference]]"
  - "[[Database Maintenance]]"
aliases:
  - "Jobs Portal"
  - "Background Operations Portal"
---
# Jobs And Background Operations Portal

## What This Page Covers

This page covers two portal surfaces:

- Jobs, for jobs, runners, job logs, job outputs, index status, and
  search/index repair;
- Background Operations, for workers, maintenance, backups, cloud snapshots,
  database maintenance, object-store health, and production update state.

Open them with:

```bash
loom enter --start jobs
loom enter --start background
```

Slice 9 verified one-shot renders for both surfaces.

## Jobs

Jobs answers: is queued execution healthy, and does any job or index
work need attention?

The verified render showed:

- Available Actions;
- Job Queue;
- Jobs Attention;
- Running Jobs;
- Queued Jobs;
- Runner Health;
- Indexing.

At verification time, queue counts were queued `0`, running `0`, failed `0`,
timed out `0`, and cancelled `1`. Runner Health showed one idle runner.

## Jobs Actions

Actions are grouped:

| Group | Meaning |
|---|---|
| Run | Run job and index workers. |
| Repair | Retry, cancel, acknowledge, or archive job and index work. |
| Check | Inspect jobs, runners, workers, and index state. |
| Logs | Inspect job logs, job outputs, and worker run history. |

This grouping is important. Retry/cancel/archive operations are state-changing
and should not appear as casual top-level buttons.

## Jobs Attention

The attention section is owned by Doctor. It appears when jobs/indexing has
failed jobs, timed-out jobs, cancelled jobs, or index failures that need
inspection.

Use the portal first to see the summary, then inspect with the CLI:

```bash
loom jobs failures --limit 20 --attention-status all
loom job inspect <job-id>
loom job logs <job-id>
loom indexes failures --limit 20
```

Press `tab` in the portal to expose failed job rows, index failures, queue
entries, and recent index statuses.

## Background Operations

Background Operations answers: are LOOM's platform workers, backups, database
maintenance, cloud snapshot state, and object store healthy?

The verified render showed:

- worker count and maintenance summary;
- Available Actions;
- Backup And Cloud Protection;
- Cloud Snapshots;
- Production Updates;
- Worker Attention;
- Healthy Worker Groups;
- Database Maintenance;
- Object Store.

At verification time, workers were healthy or unknown-without-attention, main
backup status was `ok`, database migration was `ok`, and object-store status
was `ok`.

## Background Actions

Actions are grouped:

| Group | Meaning |
|---|---|
| Run | Run maintenance jobs or workers. |
| Check | Verify backups and refresh live cloud status. |
| Logs | Inspect worker run history. |
| Inspect | Inspect operational records and status. |

Run and Check actions can touch live workers or live cloud diagnostics. Use
them deliberately, and prefer the CLI dry-run/status command first when
planning an operation.

## Worker Groups

Background Operations groups workers by domain:

- main maintenance;
- automation;
- search and indexing;
- jobs and scripts;
- platform.

Workers with attention should appear before healthy worker groups. A worker can
be enabled and have unknown health when it has not produced a recent successful
run; that is not automatically a failure. Look for `attention_required`,
consecutive failures, stale current run, and recent error summaries.

## Archived Project Suppression

Jobs checks archived project state before offering job repair
actions tied to archived projects. Background Operations checks archived
project context before offering worker run actions that are associated with
archived project work.

If an archived project exposes mutating actions through these surfaces, record
it as a portal bug.

## Related Docs

- [[Automation]]
- [[Automation Jobs And Workers]]
- [[Automation Jobs And Workers CLI Reference]]
- [[Automation Center Portal]]
- [[Storage And Timeline Portal]]
