---
title: "Automation Jobs And Workers CLI Reference"
description: "CLI reference for LOOM automation, integrations, direct events, schedules, scripts, jobs, runners, workers, artifacts, and invocations."
audience:
  - user
  - operator
  - developer
tags:
  - loom
  - cli
  - automation
  - jobs
status: draft
verified_at: "2026-07-07"
source_scope:
  - "/tmp/loomdocs-v096 automation --help"
  - "/tmp/loomdocs-v096 automations --help"
  - "/tmp/loomdocs-v096 script --help"
  - "/tmp/loomdocs-v096 job --help"
  - "/tmp/loomdocs-v096 worker --help"
  - "/tmp/loomdocs-v096 --json jobs status"
  - "/tmp/loomdocs-v096 --json workers list --limit 10"
  - "/tmp/loomdocs-v096 --json job inspect job_01KWWHDA9ZHT1SM625RPVZPSW6"
related:
  - "[[CLI Reference]]"
  - "[[Automation]]"
  - "[[Automation Jobs And Workers]]"
  - "[[Automation Center Portal]]"
  - "[[Jobs And Background Operations Portal]]"
aliases:
  - "Automation CLI"
  - "Jobs CLI"
  - "Workers CLI"
---
# Automation Jobs And Workers CLI Reference

## Safety Classes

| Command | Safety Class | Notes |
|---|---|---|
| `loom automations list`, `loom automation inspect` | read-only | Automation records. |
| `loom schedules status/list`, `loom schedule inspect/fires` | read-only | Schedule health and fire history. |
| `loom schedules create`, `loom schedule pause/resume/disable/fire` | operator-mutating | Creates or changes time-based automation. |
| `loom integrations list`, `loom integration inspect`, `loom integration auth list` | read-only | Integration and auth profile inspection. |
| `loom integrations create`, `loom integration disable/revoke`, `loom integration auth create/revoke` | operator-mutating | External caller configuration. Auth create prints a token. |
| `loom direct-event endpoints list`, `loom direct-event endpoint inspect`, `loom direct-events status/list/failures`, `loom invocation inspect/list/failures` | read-only | Direct event and invocation inspection. |
| `loom direct-event endpoints create`, `endpoint pause/resume/disable/test`, `direct-event ingest` | operator-mutating | Creates endpoints, tests mappings, or ingests events. |
| `loom scripts list`, `loom script inspect` | read-only | Script registry. |
| `loom script register`, `loom script run` | project-scoped mutating | Registers scripts or creates jobs. |
| `loom jobs status/list/queue/failures`, `loom job inspect/logs/outputs/events` | read-only | Job status and evidence. |
| `loom job retry/cancel/acknowledge/archive` | operator-mutating | Changes job execution or attention state. |
| `loom jobs run next --once` | operator-mutating | Runs the job runner once. |
| `loom runners status/list`, `loom runner inspect` | read-only | Job runner health and inventory. |
| `loom workers list`, `loom worker inspect/runs`, `loom worker policy inspect` | read-only | Background worker inventory, run history, and normalized tick-policy evidence. |
| `loom worker run <worker> --once`, `loom worker policy set`, `loom worker repair-stale --yes` | operator-mutating | Manual execution, fingerprint-guarded tick-policy control, or stale-run repair. |
| `loom artifacts list`, `loom artifact inspect` | read-only | Job artifacts. |

## Automation Commands

Read-only overview:

```bash
loom automations list --limit 20
loom automation inspect <automation-ref>
loom schedules status
loom schedules list --limit 20
loom schedule inspect <schedule-ref>
loom schedule fires <schedule-ref> --limit 20
```

Schedule creation requires a target and exactly one timing mode:

```bash
loom schedules create \
  --key nightly-example \
  --name "Nightly Example" \
  --target <target> \
  --one-shot 2026-07-08T08:00:00Z
```

Operator controls:

```bash
loom schedule fire <schedule-ref> --reason "operator reviewed"
loom schedule pause <schedule-ref>
loom schedule resume <schedule-ref>
loom schedule disable <schedule-ref>
```

## Integrations And Direct Events

Inspect integrations:

```bash
loom integrations list --limit 20
loom integration inspect <integration-ref>
loom integration auth list <integration-ref> --limit 20
```

Create and revoke integration auth only in a controlled terminal. Auth profile
creation returns a token to the caller.

Direct-event inspection:

```bash
loom direct-event endpoints list --limit 20
loom direct-event endpoint inspect <endpoint-ref>
loom direct-events status
loom direct-events list --limit 20
loom direct-events failures --limit 20
loom invocations failures --limit 20
```

Endpoint preview and ingest are mutating or test-oriented workflows:

```bash
loom direct-event endpoint preview <endpoint-ref> --body-json '{"example":true}'
loom direct-event ingest <endpoint-slug> --body-json '{"example":true}'
```

Use them only against accepted endpoints and project scopes.

## Scripts

Inspect scripts:

```bash
loom scripts list --limit 20
loom script inspect <script-ref>
```

Register a script manifest:

```bash
loom script register ./loom.script.yaml --project <project-ref> --activate
```

Run a script:

```bash
loom script run <script-ref> --project <project-ref> --wait
loom script run <script-ref> --project <project-ref> --no-wait
loom script run <script-ref> --project <project-ref> --wait-until-started
```

Choose exactly one wait mode. Slice 9 verification checked that incompatible
wait flags return `request.invalid` before the backend runs anything.

## Jobs, Runners, Logs, Outputs, And Artifacts

Status and lists:

```bash
loom jobs status
loom jobs list --limit 20
loom jobs queue --limit 20
loom jobs failures --limit 20 --attention-status all
loom runners status
loom runners list --limit 20
loom artifacts list --limit 20
```

Inspect a job:

```bash
loom job inspect <job-id>
loom job logs <job-id>
loom job outputs <job-id>
loom job events <job-id> --limit 20
```

Repair and attention actions:

```bash
loom job retry <job-id>
loom job cancel <job-id>
loom job acknowledge <job-id> --note "reviewed"
loom job archive <job-id> --note "resolved"
```

Run the job runner once:

```bash
loom jobs run next --once --reason "operator reviewed"
```

Without `--once`, this command returns `jobs.run_mode_required`.

## Workers

Inspect workers:

```bash
loom workers list --limit 20
loom worker inspect main.job_runner
loom worker runs main.job_runner --limit 10
loom worker policy inspect main.job_runner
```

`policy inspect` returns the normalized `manual` or `interval` policy, its
stable SHA-256 fingerprint, and the current derived `next_run_after` evidence.
Capture that output before a temporary acceptance change so the prior policy
can be restored exactly.

Validate a proposed interval without changing the worker:

```bash
loom worker policy set main.main_backup \
  --dry-run \
  --mode interval \
  --every 5m \
  --run-on-startup=false \
  --expected-policy-fingerprint <captured-fingerprint>
```

Apply requires an explicit confirmation, reason, retry key, and the same
captured fingerprint:

```bash
loom worker policy set main.main_backup \
  --yes \
  --mode interval \
  --every 5m \
  --run-on-startup=false \
  --expected-policy-fingerprint <captured-fingerprint> \
  --reason "reviewed automatic acceptance cycle" \
  --idempotency-key acceptance-main-backup-policy-01
```

Only main-owned workers are mutable through this command. Disabled workers,
active runs, stale fingerprints, unsupported modes, and intervals shorter
than five seconds fail closed. Identical retries with the same key are
idempotent even if HTTP completion was interrupted or a later reviewed policy
change has occurred; a competing failure cannot replace a committed success.
A different concurrent change conflicts. `--every` and `--run-on-startup` are
interval-only flags and are rejected when `--mode manual` is explicit. Restore
the captured policy with a new reviewed request using `--mode manual` and the
fingerprint returned by the applied change. This surface changes tick policy
and derived next-run state only; it is not a generic worker editor. Reasons are
recorded in audit events, so never put credentials or other secrets in them.

Run one worker once:

```bash
loom worker run main.job_runner --once --reason "operator reviewed"
```

Without `--once`, this command returns `worker.run_mode_required`.
The command waits under the selected worker's declared server-side timeout; it
does not impose a shorter generic client deadline. Canceling the command still
cancels runtime work, while LOOM records bounded terminal run evidence.

Repair stale worker runs only after inspection:

```bash
loom worker repair-stale --yes
```

Without `--yes`, this command returns `worker.repair_confirmation_required`.

## Related Docs

- [[Automation]]
- [[Automation Jobs And Workers]]
- [[Automation Jobs Scripts And Artifacts API]]
- [[Automation Center Portal]]
- [[Jobs And Background Operations Portal]]
