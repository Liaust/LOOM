---
title: "Home And Doctor"
description: "Understand the portal Home summary and Doctor repair-priority surface."
audience:
  - user
  - operator
tags:
  - loom
  - portal
  - troubleshooting
status: draft
verified_at: "2026-07-07"
source_scope:
  - "go run ./cmd/loom --no-animation --no-color enter --exit-after-render --no-boot-animation --start home"
  - "go run ./cmd/loom --no-animation --no-color enter --exit-after-render --no-boot-animation --start doctor"
  - "internal/loomcli/portal/home.go"
  - "internal/loomcli/portal/doctor.go"
  - "internal/loomcli/portal/doctor_render.go"
related:
  - "[[Portal Guide]]"
  - "[[Portal First Tour]]"
  - "[[Diagnostics And Support]]"
  - "[[Runtime Status And Support]]"
aliases:
  - "Portal Home"
  - "Portal Doctor"
---
# Home And Doctor

## What This Page Covers

Home and Doctor are the first two portal surfaces.

Home answers: "What is the current state of LOOM?"

Doctor answers: "What needs attention, and what is the safest next inspection?"

## Home

Home is the daily dashboard. It summarizes state from the current portal
snapshot and makes the rest of the portal navigable.

Slice 4 verified that Home rendered in one-shot mode:

```bash
loom --no-animation --no-color enter --exit-after-render --no-boot-animation --start home
```

The verified render showed these sections:

- Overview;
- Recent Activity;
- Navigation;
- key hints.

The exact values are live state and will change.

## Home Overview

Home groups summary items into areas:

| Area | What It Means |
|---|---|
| Diagnostics | LOOM status, daemon identity, node identity, capture time. |
| Network | Node status and direct-event endpoint attention. |
| Storage | Box, Lane, Dropzone, and watched roots. |
| Jobs | Daemon, database, workers, and job queue/failure state. |

The source for this summary is `internal/loomcli/portal/home.go`.

Home also builds attention items. Attention is ranked so severe and recent
items appear before lower-priority information.

## Recent Activity

Recent Activity gives a short timeline preview. It is not the full event log.
Use Timeline or domain-specific pages when you need deeper history.

## Navigation

Home lists the portal groups:

- Daily;
- Data;
- Automation;
- Network And Admin.

Use Enter to open a highlighted screen. Use search or command mode when it is
faster than moving through the list.

## Doctor

Doctor turns Home attention and portal partial-load errors into findings.

Slice 4 verified that Doctor rendered in one-shot mode:

```bash
loom --no-animation --no-color enter --exit-after-render --no-boot-animation --start doctor
```

At verification time, Doctor showed `status=ok`, `critical=0`, `warning=0`,
and no active issues. That was a point-in-time result, not a permanent state.

## Doctor Status

Doctor status is derived from finding totals:

- `critical`: at least one critical finding;
- `warning`: no critical findings, but at least one warning;
- `ok`: no critical or warning findings.

Doctor normalizes severities. Failures, errors, and unhealthy states become
critical. Warning, degraded, stale, partial, and manual-action states become
warning. Healthy or informational states are informational.

## Doctor Areas

Doctor can classify issues into areas:

- system;
- database;
- workers;
- jobs and indexing;
- automation;
- nodes and sync;
- Box and Lane;
- storage;
- cloud and backup;
- notes;
- projects;
- portal;
- unknown.

This makes Doctor the right first place for triage before jumping into the
deeper screen.

## Safe Next Actions

Doctor findings can include:

- explanation;
- impact;
- safe next action;
- target screen or record;
- inspect action;
- safe repair action;
- dangerous repair action.

Prefer inspect actions first. Dangerous repair actions require operator review
and should not be run casually from docs or acceptance tests.

## When Home And Doctor Disagree

If Home says the system is healthy but Doctor reports active findings, or if
Doctor says there are no findings but another surface clearly fails, record the
exact render and command output. That is a portal consistency bug.

Historical v0.9.6 findings remain archived. Capture new bugs through the current
`.project/` workflow.

## Related Docs

- [[Navigation And Command Mode]]
- [[Portal First Tour]]
- [[Diagnostics And Support]]
- [[Runtime Status And Support]]
