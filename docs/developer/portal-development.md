---
title: "Portal Development"
description: "How to change LOOM's terminal portal screens, loaders, action model, command mode, and render tests."
audience:
  - developer
  - agent
tags:
  - loom
  - developer
  - portal
status: draft
verified_at: "2026-07-07"
source_scope:
  - "internal/loomcli/portal/screens.go"
  - "internal/loomcli/portal/action_model.go"
  - "internal/loomcli/portal/action_selectors.go"
  - "internal/loomcli/portal/*_render.go"
related:
  - "[[Portal Guide]]"
  - "[[Navigation And Command Mode]]"
  - "[[CLI Development]]"
  - "[[Testing]]"
aliases:
  - "Developing Portal Screens"
---
# Portal Development

## What This Page Covers

The LOOM portal is the terminal UI launched by `loom enter`. It is implemented
inside the CLI, not as a web frontend. This page explains how to add or change
portal surfaces without breaking navigation, action mapping, or readability.

## Source Layout

| Path | Role |
|---|---|
| `screens.go` | Screen ids, titles, groups, and home navigation ordering. |
| `model.go`, `state.go`, `app.go` | Bubble Tea model and app flow. |
| `loaders.go`, `*_loader.go` | Screen data loading. |
| `*_render.go` | Screen rendering. |
| `action_model.go` | Shared portal action schema, risk, lifecycle, fields, confirmation policy. |
| `action_selectors.go` | Record/action selection and grouped action behavior. |
| `action_executor.go`, `*_action_executor.go` | Action execution. |
| `command_*.go` | Command mode parsing, completion, execution, and rendering. |
| `palette.go`, `styles.go`, `viewport.go` | Visual style and layout helpers. |

## Screen Model

Screens are grouped into:

- Daily;
- Data;
- Automation;
- Network And Admin.

When adding a screen, update `Screens()`, `ScreenGroups()`, screen
normalization, loader, renderer, selectable records, available actions, portal
docs, and tests.

## Action Model

Portal actions should describe:

- target kind and ref;
- risk level;
- availability or disabled reason;
- interaction type;
- required input fields;
- confirmation policy;
- raw command when a CLI equivalent exists;
- executor target and payload.

Actions should not appear for archived or disabled contexts unless they are
safe inspection actions. If a portal action can mutate state, the UI must make
that risk visible.

## Action Grouping

Operationally dense screens such as Background Operations, Automation Center,
and Jobs support grouped actions. This keeps high-value status
visible and moves lower-frequency run/check/log actions behind action groups.

When adding many actions to a screen, decide whether the action should be:

- primary user action;
- contextual maintenance action;
- operator/debug action;
- raw-details-only action.

Avoid flattening all actions into the top of a screen.

## Rendering Rules

Portal screens should prioritize:

- current status and attention first;
- clear grouping by user goal;
- dense but readable rows;
- no text overlap at normal terminal widths;
- grouped actions where a screen would otherwise become noisy;
- safe commands and inspect actions before mutating actions.

Do not add in-app instructional prose explaining the whole feature. The portal
is an operational surface; docs explain the workflow.

## Tests

Run portal package tests:

```bash
go test ./internal/loomcli/portal ./internal/loomcli
```

Run one-shot render checks for touched screens:

```bash
go run ./cmd/loom --no-animation --no-color enter \
  --exit-after-render \
  --no-boot-animation \
  --start jobs
```

Use the screen id that changed. One-shot render validates load/render, not full
interactive action execution.

## Documentation And Audit

When portal behavior changes, update:

- `docs/portal/<surface>.md`;
- the matching CLI/API docs for actions;
- `.project/features/<feature>/portal_surface_audit.md` when a docs/testing
  plan requires it;
- user guide pages when the workflow changes.

## Related Docs

- [[Portal Guide]]
- [[Navigation And Command Mode]]
- [[CLI Development]]
- [[Testing]]
