---
title: "Automation Jobs And Workers"
description: "Understand the separate owners of triggers, capability dispatch, jobs, background worker policies and agent schedules."
audience:
  - user
  - operator
  - developer
tags:
  - loom
  - concepts
  - automation
  - jobs
status: draft
verified_at: "2026-09-10"
source_scope:
  - "internal/automation/service.go"
  - "internal/automation/scheduler.go"
  - "internal/automation/calendar.go"
  - "internal/loomcli/automation.go"
  - "internal/loomcli/domain.go"
  - "internal/loomcli/workers.go"
  - "internal/workers/policy.go"
  - "internal/loomcli/portal/action_selectors.go"
related:
  - "[[Automation]]"
  - "[[Events Communication And Realtime]]"
  - "[[Projects Facets And Contracts]]"
  - "[[Automation Jobs And Workers CLI Reference]]"
  - "[[Automation Jobs Scripts And Artifacts API]]"
  - "[[Automation Center Portal]]"
  - "[[Jobs And Background Operations Portal]]"
  - "[[Modules Connectors And Agents]]"
aliases:
  - "Automation Model"
  - "Jobs Workers And Runners"
---
# Automation Jobs And Workers

## Follow A Trigger To Its Outcome

LOOM separates when work is requested, how it is authorized, and what executed.
This lets a user diagnose a failed task without guessing from a timer or a log.
The model below describes current source reviewed on 2026-09-10; live claims
retain their own dated receipts.

| Record or component | Role |
|---|---|
| Integration | External caller identity and allowed scope for direct events |
| Direct-event endpoint | Validates/maps incoming data to an automation target |
| Schedule | Time-based trigger for a target capability |
| Automation | Durable definition behind a schedule or event endpoint |
| Fire/direct event | The recorded triggering occurrence |
| Invocation | Dispatch attempt and result/attention state |
| Script or workflow | Executable work definition |
| Job | Durable queued/running/completed execution record |
| Runner | Claims and executes eligible jobs |
| Worker | Runs bounded LOOM background duties under instance policy |
| Event | Durable history and correlation |
| Log/output/artifact | Operational detail, structured result or retained generated object |

A schedule fire or accepted direct event creates an invocation that dispatches
a permissioned capability. The target may create a job; not every capability
call is a script job. Scripts and workflows execute through jobs and
capabilities rather than becoming free-floating shell schedules.

A definition can exist without any work queued. A dispatch can fail before a
job exists. A job may finish without artifacts. Use the corresponding record
at each step; no single green counter proves the entire chain.

## Core Schedules And Calendar Source

Core schedules target capabilities and carry project/scope, actor, input,
concurrency, timeout and misfire policy. That configuration does not replace
the target's authorization checks. Direct-event callers likewise need their
own identity and allowed endpoint/scope.

The accepted calendar source adds five-field cron with an explicit IANA zone
or UTC. Occurrences persist as UTC instants; spring-forward gaps are skipped
and the repeated fall-back interval is excluded on its second occurrence.
The existing durable scheduler remains the execution owner, rather than a
second cron engine. On downtime, it handles the stored due occurrence under
the lateness/concurrency policy and advances beyond current time; it does not
promise to replay every missed minute.

New cron schedules default disabled. Source preview shares normalization and
returns the proposed next time without durable schedule identity or activation.
Existing one-shot and elapsed-interval defaults are unchanged. These are
source-implemented contracts, not live calendar acceptance: the Sept 10 Main
receipts remain at migration 68, and calendar deployment/build acceptance is
separate. A local help command advertising cron does not prove the connected
daemon supports it.

## Worker Policies Are A Separate Clock

A worker is background execution machinery, not necessarily an independent OS
process or an agent conversation. Its instance has enabled/pause state and a
tick policy. The supported modes are manual, interval and `daily_local`;
inspect the normalized policy and fingerprint to understand scheduling.

The recorded Main policies are 03:00 recovery packages and 03:15 cloud archives
in **Europe/Amsterdam**, unchanged in the 2026-09-10 operator receipt. Their
runs belong to workers, so zero core schedules does not mean backup is off.
Local-time policies must be read with their timezone; do not reinterpret them
as fixed UTC hours throughout the year.

A worker run supplies attempt, status and operation evidence. A current
successful run does not remove historical failure findings, expand old archive
coverage, or prove a full restore. Recovery, coverage and retention have their
own evidence and operator gates.

## Hermes And Archivist Are Not Recurring LOOM Jobs

Hermes native schedules belong to the agent harness and do not automatically
appear as LOOM schedules or invocations. The planned LOOM-owned agent executor
would need a bounded capability/dispatch contract; further implementation is
explicitly deferred. Do not schedule the same logical task in Hermes and LOOM
to bridge that missing contract.

Archivist illustrates another distinction. The Main workspace supplies narrow
reasoning/retrieval instructions, while `main.provenance_archivist` inside
`loomd` supplies deterministic selection, leases, checkpoints and lifecycle
operations. Workspace registration, a bounded read-only Codex task and one
idempotent manual cycle were accepted on 2026-09-01. That is not recurring
model-driven execution, a global scan or a new semantic ledger.

An agent task created by a development tool also belongs to that tool's task
system. It does not activate a Main schedule merely because it wakes later.
See [[Modules Connectors And Agents]] for skills, harnesses and authority.

## Inspect Before Changing Execution

[[Automation]] gives the inspection sequence: schedule/fire to invocation,
job to logs/outputs/events, or worker policy to run. The portal mirrors those
owners through Automation Center, Jobs and Background Operations.

Manual worker/job-runner commands require `--once`; stale-run repair requires
`--yes`. Worker policy apply requires its inspected fingerprint, explicit
confirmation, reason and idempotency key. These are concrete API/CLI guards,
not substitutes for task authorization or guarantees that a retry is harmless.

Project lifecycle further restricts execution. Archived and restored-inactive
projects retain runtime guards; restoring payload bytes does not resume
services, schedules, capabilities or watchers. Historical archive inspection
does not establish current project membership. Report a boundary failure
rather than bypassing it with direct database, shell or duplicate scheduling.

Useful success evidence names the actual occurrence/run, terminal result and
relevant output or operation. Useful failure evidence preserves what was
attempted and whether an effect is known, refused or uncertain. Keep those
distinctions when reporting a task complete.
