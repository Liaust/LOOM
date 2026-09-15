---
title: "Portal Guide"
description: "Guide to the LOOM terminal portal surfaces, status indicators, and actions."
audience:
  - user
  - operator
tags:
  - loom
  - portal
status: draft
verified_at: "2026-07-07"
source_scope:
  - "internal/loomcli/portal/screens.go"
  - "internal/loomcli/portal/action_inventory.go"
related:
  - "[[User Guide]]"
  - "[[CLI Reference]]"
  - "[[Operations]]"
aliases:
  - "LOOM Portal"
---
# Portal Guide

The portal is LOOM's human-facing terminal interface. It groups daily status,
projects, notes, files, automation, background work, nodes, capabilities, and
diagnostics into navigable surfaces.

## Start Here

- [[Navigation And Command Mode]] explains screen groups, cursor navigation,
  command mode, raw/detail toggles, and action selection.
- [[Home And Doctor]] explains the daily readiness surface and system health
  interpretation.
- [[Portal First Tour]] gives a user-oriented guided path.

## Surface Guides

- [[Projects Portal]]
- [[LOOM Notes Portal]]
- [[LOOM Box Portal]]
- [[Storage And Timeline Portal]]
- [[Automation Center Portal]]
- [[Jobs And Background Operations Portal]]
- [[Nodes And Capabilities Portal]]
- [[Object Store Diagnostics Portal]]

## Verification Rule

The archived v0.9.6 surface audit remains the original verification evidence.
If a Portal action is stale, fails, or appears in an archived context where it
should not, capture a bounded bugfix through `.project/` and mark the docs
section blocked or deferred.

## Related Docs

- [[User Guide]]
- [[Command Safety]]
- [[Operations]]
- [[Developer Documentation]]
