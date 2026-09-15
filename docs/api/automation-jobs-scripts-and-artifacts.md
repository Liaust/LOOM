---
title: "Automation Jobs Scripts And Artifacts API"
description: "API reference for LOOM automation, direct events, schedules, integrations, scripts, jobs, runners, workers, artifacts, and invocations."
audience:
  - developer
  - operator
tags:
  - loom
  - api
  - automation
  - jobs
status: draft
verified_at: "2026-07-07"
source_scope:
  - "internal/httpapi/server.go"
  - "internal/httpapi/automation.go"
  - "internal/loomcli/automation.go"
  - "internal/loomcli/domain.go"
  - "internal/loomcli/workers.go"
related:
  - "[[API Reference]]"
  - "[[Automation Jobs And Workers CLI Reference]]"
  - "[[Automation Jobs And Workers]]"
  - "[[Events Communication And Realtime]]"
aliases:
  - "Automation API"
  - "Jobs API"
  - "Workers API"
---
# Automation Jobs Scripts And Artifacts API

## Envelope

Automation and execution endpoints use the normal LOOM envelope:

```json
{
  "ok": true,
  "data": {},
  "meta": {
    "correlation_id": "corr_..."
  }
}
```

Errors include `error.code`, summary text, and correlation metadata. Preserve
the correlation id when reporting a failed automation, job, or worker action.

## Scripts

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/scripts` | GET | read-only | `loom scripts list` |
| `/v1/scripts/register` | POST | project-scoped mutating | `loom script register` |
| `/v1/scripts/{ref}` | GET | read-only | `loom script inspect` |
| `/v1/scripts/{ref}/run` | POST | project-scoped mutating | `loom script run` |

Script run requests create job work. The CLI validates run mode before calling
the backend, so clients should also avoid ambiguous combinations such as wait
and no-wait at the same time.

## Jobs

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/jobs/status` | GET | read-only | `loom jobs status` |
| `/v1/jobs` | GET | read-only | `loom jobs list` |
| `/v1/jobs/queue` | GET | read-only | `loom jobs queue` |
| `/v1/jobs/failures` | GET | read-only | `loom jobs failures` |
| `/v1/jobs/run-next` | POST | operator-mutating | `loom jobs run next --once` |
| `/v1/jobs/{ref}` | GET | read-only | `loom job inspect` |
| `/v1/jobs/{ref}/logs` | GET | read-only | `loom job logs` |
| `/v1/jobs/{ref}/outputs` | GET | read-only | `loom job outputs` |
| `/v1/jobs/{ref}/cancel` | POST | operator-mutating | `loom job cancel` |
| `/v1/jobs/{ref}/retry` | POST | operator-mutating | `loom job retry` |
| `/v1/jobs/{ref}/acknowledge` | POST | operator-mutating | `loom job acknowledge` |
| `/v1/jobs/{ref}/archive` | POST | operator-mutating | `loom job archive` |

Job lists support filters such as status, job type, script, workflow, project,
scope, and limit through the local client. Job failures additionally support
attention-state filters.

At verification time, `jobs status` returned queued `0`, running `0`, failed
`0`, and one runner. `job logs` returned log-record rows, while `job outputs`
returned an empty list for the inspected completed job.

## Runners, Workers, And Artifacts

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/runners/status` | GET | read-only | `loom runners status` |
| `/v1/runners` | GET | read-only | `loom runners list` |
| `/v1/runners/{ref}` | GET | read-only | `loom runner inspect` |
| `/v1/workers` | GET | read-only | `loom workers list` |
| `/v1/workers/{ref}` | GET | read-only | `loom worker inspect` |
| `/v1/workers/{ref}/runs` | GET | read-only | `loom worker runs` |
| `/v1/workers/{ref}/run-once` | POST | operator-mutating | `loom worker run <ref> --once` |
| `/v1/workers/{ref}/policy` | GET/POST | read-only or operator-mutating | `loom worker policy inspect/set` |
| `/v1/workers/repair-stale` | POST | operator-mutating | `loom worker repair-stale --yes` |
| `/v1/artifacts` | GET | read-only | `loom artifacts list` |
| `/v1/artifacts/{ref}` | GET | read-only | `loom artifact inspect` |

Worker run and stale-repair endpoints should be reached through CLI or portal
actions that enforce explicit run mode and confirmation. Slice 9 verified that
the CLI rejects missing `--once` and missing `--yes`.

Worker policy `GET` returns the normalized tick policy, stable fingerprint,
and derived next-run evidence. `POST` accepts exactly one typed policy object.
A dry-run sets `dry_run:true`; apply sets `confirm:true` and requires a reason,
the fingerprint captured by inspection, and `X-Loom-Idempotency-Key`. The
service locks the main-owned worker instance, rejects disabled or actively
running workers, compares the fingerprint, and updates `tick_policy_json` and
`next_run_after` in the same transaction as the
`worker.instance.updated` audit event. It cannot edit worker config,
locality, lifecycle, concurrency, retry, timeout, or resource policy. The
reason is audit-visible and must not contain credentials or other secrets.
Each confirmed request records bounded recovery evidence in its own
idempotency row within the worker transaction. That receipt makes a committed
changed or unchanged result replayable after interrupted HTTP completion and
after later policy mutations. Completed success is terminal and cannot be
overwritten by a competing failed attempt.

## Automations And Schedules

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/automations` | GET | read-only | `loom automations list` |
| `/v1/automations/{ref}` | GET | read-only | `loom automation inspect` |
| `/v1/schedules/status` | GET | read-only | `loom schedules status` |
| `/v1/schedules` | GET/POST | read-only or operator-mutating | `loom schedules list/create` |
| `/v1/schedules/{ref}` | GET | read-only | `loom schedule inspect` |
| `/v1/schedules/{ref}/pause` | POST | operator-mutating | `loom schedule pause` |
| `/v1/schedules/{ref}/resume` | POST | operator-mutating | `loom schedule resume` |
| `/v1/schedules/{ref}/disable` | POST | operator-mutating | `loom schedule disable` |
| `/v1/schedules/{ref}/fire` | POST | operator-mutating | `loom schedule fire` |
| `/v1/schedules/{ref}/fires` | GET | read-only | `loom schedule fires` |

Schedule creation requires target metadata and timing configuration. Schedule
fire can dispatch now or enqueue depending on request input.

## Integrations, Direct Events, And Invocations

| Endpoint | Method | Safety | CLI Path |
|---|---|---|---|
| `/v1/integrations` | GET/POST | read-only or operator-mutating | `loom integrations list/create` |
| `/v1/integrations/{ref}` | GET | read-only | `loom integration inspect` |
| `/v1/integrations/{ref}/disable` | POST | operator-mutating | `loom integration disable` |
| `/v1/integrations/{ref}/revoke` | POST | operator-mutating | `loom integration revoke` |
| `/v1/integrations/{ref}/auth-profiles` | GET/POST | read-only or operator-mutating | `loom integration auth list/create` |
| `/v1/integrations/{ref}/auth-profiles/{auth-ref}/revoke` | POST | operator-mutating | `loom integration auth revoke` |
| `/v1/direct-events/status` | GET | read-only | `loom direct-events status` |
| `/v1/direct-events/failures` | GET | read-only | `loom direct-events failures` |
| `/v1/direct-events` | GET | read-only | `loom direct-events list` |
| `/v1/direct-events/{ref}` | GET | read-only | `loom direct-event inspect` |
| `/v1/direct-events/{ref}/raw` | GET | sensitive read-only | `loom direct-event raw` |
| `/v1/direct-events/endpoints` | GET/POST | read-only or operator-mutating | `loom direct-event endpoints list/create` |
| `/v1/direct-events/endpoints/{ref}` | GET | read-only | `loom direct-event endpoint inspect` |
| `/v1/direct-events/endpoints/{ref}/pause` | POST | operator-mutating | `loom direct-event endpoint pause` |
| `/v1/direct-events/endpoints/{ref}/resume` | POST | operator-mutating | `loom direct-event endpoint resume` |
| `/v1/direct-events/endpoints/{ref}/disable` | POST | operator-mutating | `loom direct-event endpoint disable` |
| `/v1/direct-events/endpoints/{ref}/preview` | POST | test-mutating | `loom direct-event endpoint preview` |
| `/v1/direct-events/ingest/{slug}` | POST | event-ingest mutating | `loom direct-event ingest` |
| `/v1/invocations` | GET | read-only | `loom invocations list` |
| `/v1/invocations/failures` | GET | read-only | `loom invocations failures` |
| `/v1/invocations/{ref}` | GET | read-only | `loom invocation inspect` |

Direct-event ingest reads bounded request bodies and can return unauthorized,
mapping, idempotency, timeout, or accepted/completed states depending on
endpoint response mode.

## Related Docs

- [[Automation Jobs And Workers CLI Reference]]
- [[Automation Jobs And Workers]]
- [[Automation]]
- [[API Route Index]]
