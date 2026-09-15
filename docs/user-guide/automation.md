---
title: "Automation"
description: "Inspect LOOM schedules, invocations, jobs and worker policies, and recognize the limits of current agent automation."
audience:
  - user
  - operator
tags:
  - loom
  - user-guide
  - automation
  - jobs
status: draft
verified_at: "2026-09-10"
source_scope:
  - "internal/loomcli/automation.go"
  - "internal/loomcli/domain.go"
  - "internal/loomcli/workers.go"
  - "internal/automation/calendar.go"
  - "internal/loomcli/portal/action_selectors.go"
related:
  - "[[Getting Started]]"
  - "[[Automation Jobs And Workers]]"
  - "[[Automation Jobs And Workers CLI Reference]]"
  - "[[Automation Center Portal]]"
  - "[[Jobs And Background Operations Portal]]"
  - "[[Projects]]"
  - "[[Command Safety]]"
aliases:
  - "LOOM Automation"
  - "Jobs And Automation"
---
# Automation

## Find The Record That Owns The Work

Use this guide when something should run later, has failed, or appears stuck.
Identify whether it is a core schedule, a job, or a background worker before
changing anything. These have separate configuration and execution records.

The examples below inspect a configured LOOM backend. Syntax was checked
against source/local help on 2026-09-10; this documentation pass ran no live
schedule, job or worker command. Historical counts of zero schedules or ten
visible workers are not a current health report.

| Work | Inspect first | Then follow |
|---|---|---|
| A timed capability call | Schedule and its fires | Invocation and dispatched result/job, where applicable |
| An incoming external event | Endpoint/direct-event state | Invocation failure or result |
| A script or workflow execution | Job | Status, logs, outputs and events |
| Recovery, cloud, indexing or maintenance | Worker instance and policy | Worker run and operation-specific evidence |
| A recurring agent conversation | Its explicit owning system | LOOM-owned agent execution remains deferred; do not invent a duplicate schedule |

In the portal, use `loom enter --start automations` for Automation Center,
`loom enter --start jobs` for jobs, and `loom enter --start background` for
workers and operational work. Rendering a page is not execution evidence.

## Inspect Schedules And Invocations

```bash
loom schedules status
loom schedules list --limit 20
loom automations list --limit 20
loom direct-event endpoints list --limit 20
loom direct-events status
loom invocations failures --limit 20
```

An empty core schedule list can be valid even while daily backup workers run:
those workers have their own tick policies. For an existing schedule, use
`loom schedule inspect <schedule-ref>` and
`loom schedule fires <schedule-ref>`; follow an invocation with
`loom invocation inspect <invocation-ref>`. Replace placeholders with
identifiers from the preceding result.

Check the target, project/scope, run-as actor, enabled/paused state, timezone,
next occurrence and recorded failures. A configured schedule is not a fire;
a fire is not a completed invocation; dispatch is not proof that the intended
external effect happened. Preserve the exact IDs when reporting a mismatch.

## Read Jobs And Their Results

```bash
loom jobs status
loom jobs list --limit 20
loom jobs failures --limit 20 --attention-status all
loom runners status
loom job inspect <job-id>
loom job logs <job-id>
loom job outputs <job-id>
loom job events <job-id> --limit 20
loom artifacts list --job <job-id>
```

A script is a reusable definition; a job records an execution. Look at the
job's terminal status, attempts and error before reading outputs. Logs are
operational detail; outputs are structured results; artifacts are retained
generated objects. A successful job may produce no artifact. An acknowledged
or archived failure is not proof that the work succeeded.

## Inspect Daily Recovery And Other Workers

```bash
loom workers list --limit 20
loom worker inspect main.main_backup
loom worker policy inspect main.main_backup
loom worker runs main.main_backup --limit 5
loom worker inspect main.cloud_snapshot_upload
loom worker policy inspect main.cloud_snapshot_upload
loom worker runs main.cloud_snapshot_upload --limit 5
```

The 2026-09-10 operator receipt records unchanged `daily_local` policies:
Main recovery packages at **03:00 Europe/Amsterdam**, then cloud archives at
**03:15 Europe/Amsterdam**. Inspect actual policy mode, local time, timezone,
fingerprint and next occurrence together with enabled/pause state and the
latest run. Do not recreate these policies from an old setup example.

Healthy scheduling means the configured policy matches intent and runs have
truthful terminal evidence. It does not erase earlier failures or prove a
complete restore. The Sept 10 exact raw-root archive and bounded read-back
closed Topics/Library coverage for that archive; older archives keep their
declared scope. Backup success, coverage and recovery assurance are separate
questions. Use the linked CLI/operations guidance to inspect the specific
operation or archive before considering a retry.

Archivist is a different worker: its registered workspace and manual cycle
were accepted on 2026-09-01. It remains manual-only; that receipt did not
enable a recurring model-driven task.

## Calendar Source And Agent Execution Status

Accepted source adds five-field cron with explicit timezone, disabled-by-default
new cron schedules, and `schedules create --dry-run` preview. This source
acceptance is not production activation. The recorded Main operator receipts
remain at migration 68; the calendar migration and actual Nix build receipt
retain their separate gate. No new calendar schedule was activated by this wave.

Preview validates a proposed target, scope, actor, input and next time without
creating a schedule. It is not an execution grant or a test of a future model
run. One-shot and elapsed-interval behavior remain separate from cron, as do
the existing daily worker policies.

Further LOOM-owned Hermes execution/agentic automation implementation is
explicitly deferred. Hermes native schedules belong to the harness; they are
not automatically LOOM schedule records. Do not put the same logical task on
both clocks. A future agent task still needs an accepted owner, scope, budget,
failure visibility and execution contract before activation.

## Run Or Repair Only After Reviewing The Effect

Manual and repair actions change real state. The exact target and task
authorization matter even when the CLI has a confirmation flag:

| Action | Actual boundary |
|---|---|
| `loom worker run <worker-ref> --once` | Executes one worker and may perform backup/cloud or other operational work. Record a reason and preserve request/run identity. |
| `loom jobs run next --once` | Runs the next eligible job, which may not be the job you were inspecting. Review the queue first. |
| Job `retry` / `cancel` | Alters execution; inspect possible prior effects before retrying. |
| Job `acknowledge` / `archive` | Changes attention/history handling, not the underlying outcome. |
| Worker `policy set` | Requires the inspected fingerprint and exactly one of `--dry-run` or `--yes`; apply also requires reason and idempotency key. |
| `loom worker repair-stale --yes` | Marks qualifying stale runs timed out and releases their instances; it is broader than inspecting one failure. |

Do not use these as health probes. On timeout or uncertain delivery, inspect
the recorded operation before issuing another mutation. If the project is
archived or restored inactive, its runtime guards remain authoritative; do
not resume schedules or writers to bypass them. See [[Projects]].

## When To Escalate

Preserve the project/node, schedule/job/worker ID, occurrence or run time,
status and exact error. Escalate missing terminal evidence, repeated failures,
unexpected target/actor, unavailable owner, or a mutating action offered for a
guarded project. A current failure needs scoped diagnosis, not a broad stale-run
repair or a new notification channel.

Use [[Automation Jobs And Workers]] for the model and
[[Automation Jobs And Workers CLI Reference]] for exact commands. Follow
[[Command Safety]] and the relevant operator runbook for recovery, retention
or other production effects.
