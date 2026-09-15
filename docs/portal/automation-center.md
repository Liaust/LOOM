---
title: "Automation Center Portal"
description: "Guide to the portal surface for schedules, direct events, integrations, automations, invocations, and automation failures."
audience:
  - user
  - operator
tags:
  - loom
  - portal
  - automation
status: draft
verified_at: "2026-07-07"
source_scope:
  - "/tmp/loomdocs-v096 --no-animation --no-color enter --exit-after-render --no-boot-animation --start automations"
  - "internal/loomcli/portal/loaders.go"
  - "internal/loomcli/portal/action_selectors.go"
  - "internal/loomcli/portal/render.go"
related:
  - "[[Portal Guide]]"
  - "[[Automation]]"
  - "[[Automation Jobs And Workers]]"
  - "[[Jobs And Background Operations Portal]]"
aliases:
  - "Automation Center"
  - "Automations Portal"
---
# Automation Center Portal

## What This Page Covers

Automation Center is the portal surface for schedule and event-driven
automation. Open it with:

```bash
loom enter --start automations
```

Slice 9 verified a one-shot render against main.

## What The Surface Loads

Automation Center loads:

- automations;
- schedules and schedule status;
- schedule fires;
- integrations;
- direct-event endpoints;
- direct events and direct-event status;
- invocations and invocation failures;
- archived-project context for action suppression.

If one backend call fails, the surface can still render partial data and mark
the failed loader as partial.

## Top-Level Sections

The verified render showed:

- Available Actions;
- Automation Failures;
- Active Automations;
- Automation Health.

The current system had no automation failures and no active automations. That
is a healthy empty state when no schedules or endpoints have been configured.

## Available Actions

Actions are grouped to avoid a long flat list:

| Group | Meaning |
|---|---|
| Run | Run automation workers such as the scheduler or dispatcher. |
| Controls | Fire, pause, resume, or test automation inputs when matching records exist. |
| Check | Inspect schedules, endpoints, integrations, automations, and workers. |
| Logs | Inspect fires, direct events, invocations, raw payloads, and worker run history. |

When there are no records for a group, the portal may only show the groups that
have available actions. In the verified empty state, Run and Check were shown.

## Failure Interpretation

Automation failures combine:

- failed invocations;
- failed direct events;
- missed schedule fires;
- direct-event mapping or authorization problems;
- stale project refs that suppress actions.

Use `tab` for raw/detail mode when you need the rows behind the summary. Use
the CLI for precise follow-up:

```bash
loom invocations failures --limit 20
loom direct-events failures --limit 20
loom direct-events status
loom schedules status
```

## Archived Project Suppression

Automation Center checks project archive state before exposing mutating related
actions. For archived project context, schedule fire/pause/resume and
direct-event endpoint tests should be suppressed.

If the portal exposes a mutating action for an archived project automation,
record a bug before using it.

## Related Docs

- [[Automation]]
- [[Automation Jobs And Workers]]
- [[Automation Jobs And Workers CLI Reference]]
- [[Jobs And Background Operations Portal]]
