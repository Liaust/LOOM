---
title: "Navigation And Command Mode"
description: "Navigate the LOOM terminal portal, use one-shot rendering, and understand command mode boundaries."
audience:
  - user
  - operator
tags:
  - loom
  - portal
status: draft
verified_at: "2026-08-16"
source_scope:
  - "go run ./cmd/loom enter --help"
  - "go run ./cmd/loom --no-animation --no-color enter --exit-after-render --no-boot-animation --start home"
  - "internal/loomcli/portal/screens.go"
  - "internal/loomcli/portal/command_parser.go"
  - "internal/loomcli/portal/command_executor.go"
  - "internal/loomcli/portal/action_inventory.go"
  - "internal/loomcli/portal/availability.go"
  - "internal/loomcli/portal/action_model.go"
related:
  - "[[Portal Guide]]"
  - "[[Portal First Tour]]"
  - "[[Home And Doctor]]"
  - "[[Command Safety]]"
aliases:
  - "Portal Navigation"
  - "Portal Command Mode"
---
# Navigation And Command Mode

## What This Page Covers

This page explains how to move through the LOOM terminal portal and how to use
noninteractive rendering for smoke checks.

It does not document every screen action. Surface-specific action docs are
written in the portal pages for Projects, Notes, Box, Storage, Automation, Jobs,
Nodes, Capabilities, Background Operations, and Object Store Diagnostics.

## Open The Portal

Run:

```bash
loom enter
```

Useful flags:

```bash
loom enter --start home
loom enter --start doctor
loom enter --compact
loom enter --exit-after-render --no-boot-animation --start home
```

`--exit-after-render` is for smoke tests and diagnostics. It renders one portal
view and exits.

The portal intentionally rejects raw JSON, plain, and noninteractive output
modes for the interactive path. Use `--exit-after-render` for a deterministic
noninteractive render.

## Screen Groups

The current screen groups are:

| Group | Screens |
|---|---|
| Daily | Home, Doctor, Projects, LOOM Notes, LOOM Box |
| Data | LOOM Main Storage, Timeline |
| Automation | Automation Center, Jobs |
| Network And Admin | Nodes And Watched Roots, Capabilities And Providers, Background Operations, Object Store Diagnostics |

These groups are source-verified in `internal/loomcli/portal/screens.go`.

## Key Hints

The portal renders key hints at the bottom of the screen. Slice 4 verified the
Home render includes:

```text
/ search
# scoped
$ command
j/k move
trackpad/pgup/pgdn scroll
enter open
space actions
esc back
r refresh
tab details
? help
q quit
```

Use search to find surfaces or content. Use actions only after you understand
the current context and safety level.

## Command Mode

Command mode is entered with `$`. It is for portal-recognized commands and
actions, not a general shell.

Use command mode when you want a portal action without navigating manually. Use
the CLI when you need a precise scriptable command. Use raw shell only for
developer or operations tasks that are outside normal LOOM behavior.

If command mode routes to a wrong surface or wrong action, treat that as a
portal bug. Do not document the wrong route as expected behavior.

Command risk and connection dependency are separate decisions. A read-only
command can still require main. The preview shows whether execution depends on
`local` state or `main`; preview and completion remain available while main is
offline. Execution fails with `portal.main_offline` before confirmation and
before the command runner when the command needs main. Unknown commands default
to main-dependent. The current deliberately small local command allowlist
contains `loom version`.

## Actions

Press `space` to open actions for the current surface. Actions should be
contextual and grouped by purpose. Prefer:

- inspect;
- details;
- status;
- dry-run;
- copy/show command;
- safe repair.

Be careful with:

- apply;
- archive;
- cleanup;
- delete;
- update;
- restore;
- purge;
- production-like maintenance.

The portal action inventory is source-backed by
`internal/loomcli/portal/action_inventory.go` and related action selector files.
Surface-specific docs must verify important actions before presenting them as
working.

## Automatic Offline Mode

The portal enters offline mode automatically when its bounded main health probe
cannot connect. Startup still succeeds; no separate offline flag is required.

Home shows the selected main target and the time of the last check before any
system summary. Doctor reports one `Main node is unreachable` finding. LOOM Box
keeps its locally inspected Box, Lane, Dropzone, and update state, while clearly
marking backend watch and registration state unavailable.

Projects, Jobs, Storage, Timeline, Automation, Notes, Nodes, Capabilities,
Background Operations, and Object Store Diagnostics remain navigable but show
one consistent unavailable view. Their loaders and actions do not fan out to
main after the failed health probe.

Navigation remains enabled. Main-dependent actions are disabled and are also
rejected at the executor boundary with `portal.main_offline`. The explicitly
audited local action classes are Box initialization and local project-contract
validation; their normal lifecycle guards and confirmation rules still apply.
Unknown action executors default to main-dependent.

Press `r` to run a new health probe. Recovery replaces the local-only snapshot,
reloads the current surface, and re-evaluates actions and command dependencies.
If a previously reachable main disconnects, cached main-owned rows are removed
instead of being presented as current.

## One-Shot Rendering For Tests

Use one-shot rendering in docs and tests when you need proof that the portal can
load a screen:

```bash
loom --no-animation --no-color enter --exit-after-render --no-boot-animation --start home
loom --no-animation --no-color enter --exit-after-render --no-boot-animation --start doctor
loom --no-animation --no-color enter --exit-after-render --no-boot-animation --start projects
```

These renders exit successfully when main is offline. Home and Doctor show the
connection state; a main-owned surface such as Projects shows the unavailable
view. The commands are read-only, but they still use the current LOOM service
context, so an online render may reflect live production-like data.

## Related Docs

- [[Portal First Tour]]
- [[Home And Doctor]]
- [[Portal Guide]]
- [[Command Safety]]
