---
title: "Command Safety"
description: "Reference for LOOM command safety classes, dry-run expectations, fixture boundaries, and production-like operations."
audience:
  - user
  - operator
  - developer
  - agent
tags:
  - loom
  - cli
  - operations
  - security
status: draft
verified_at: "2026-07-07"
source_scope:
  - "AGENTS.md"
related:
  - "[[CLI Reference]]"
  - "[[Command Index]]"
  - "[[Diagnostics And Support]]"
  - "[[Storage Retention And Safe Delete]]"
aliases:
  - "CLI Safety"
---
# Command Safety

## What This Page Covers

This page defines the safety language used throughout the LOOM CLI docs. LOOM
is connected to production-like main and workspace nodes, so examples must make
clear whether a command only reads state, plans a change, mutates a fixture, or
can affect real user data and runtime state.

## Safety Classes

| Class | Meaning | Example Shape |
|---|---|---|
| `read-only` | Reads current state and should not change records or files. | `loom status`, `loom backup status`, `loom projects list` |
| `dry-run` | Plans or validates a mutation without applying it. | `loom project archive --dry-run`, `loom storage filesystem migrate --dry-run --manifest <path>` |
| `bounded artifact creation` | Creates a bounded support or acceptance artifact. | `loom support bundle create --dry-run` before real creation |
| `fixture-mutating` | Mutates an accepted `.loom-acceptance` test fixture. | Project scaffold/facet/archive tests in a fixture project |
| `operator-mutating` | Changes real LOOM state and requires an operator decision. | `loom update apply`, `loom grant revoke`, real backup/cloud actions |
| `dangerous` | Can destroy, purge, overwrite, expose credentials, or break runtime state. | Purge, restore apply, destructive cleanup, raw credential handling |

The same command group can contain multiple classes. Read the specific command
section before running an example.

## Default Safe Order

When investigating a problem:

1. Run status/list/inspect commands.
2. Run doctor or explain commands when available.
3. Run dry-run planning.
4. Use `.loom-acceptance` fixtures for workflow tests.
5. Run mutating commands only when the operation is intentional and scoped.

Do not start with repair, cleanup, update, restore, purge, grant, approval, or
capability execution commands.

## Dry-Run Is Not Always Zero-Record

`--dry-run` means the target action should not be applied. It can still create
or read audit/control records in some subsystems so the plan is traceable.

When that distinction matters, the command docs call it out. Treat dry-run as
safe to use for planning, not as a guarantee that no diagnostic record exists.

## Fixture Boundary

Manual smoke, slice, latency, and acceptance artifacts should live under
`.loom-acceptance`:

```text
~/loom-box/Documents/.loom-acceptance/<scenario>/
```

or a feature-specific accepted fixture folder. Do not place loose test files in
user storage roots.

## Production-Like Operations

Commands in these domains often require extra care:

- backup and restore;
- cloud snapshots and retention;
- storage cleanup, restore, safe-delete, and purge-like operations;
- database compaction and maintenance apply;
- update apply and rollback;
- approval decisions and grant revocation;
- real capability calls;
- project archive or activation changes on non-fixture projects.

Use the matching operations runbook and dry-run/plan command first.

## JSON Safety

For automation, use `--json` and check:

- `ok`;
- `error.code` and `error.target` on failure;
- `meta.correlation_id`;
- whether the response says a command is a dry-run, planned action, or applied
  action.

If a command returns a generic error for a known precondition, record that as a
bug in the active development notes instead of normalizing the behavior in
docs.

## Related Docs

- [[CLI Reference]]
- [[Command Index]]
- [[Diagnostics And Support]]
- [[Storage Retention And Safe Delete]]
- [[Security Authorization And Policy]]
