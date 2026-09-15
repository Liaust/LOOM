---
title: "Testing"
description: "How to choose and run LOOM unit, package, CLI, portal, smoke, and production-safe validation."
audience:
  - developer
  - operator
  - agent
tags:
  - loom
  - developer
  - testing
status: draft
verified_at: "2026-07-07"
source_scope:
  - "find internal -name '*_test.go'"
  - "tests/smoke"
  - "tests/lib/production_guard.sh"
related:
  - "[[Developer Documentation]]"
  - "[[Local Development]]"
  - "[[Diagnostics And Support]]"
  - "[[Release Flow]]"
aliases:
  - "LOOM Testing"
---
# Testing

## What This Page Covers

This page explains how to validate LOOM changes without running more than the
change needs. LOOM is connected to production-like main and workspace nodes, so
test selection matters.

## Test Layers

| Layer | Command Shape | Use For |
|---|---|---|
| Package tests | `go test ./internal/<package>` | Domain logic, services, parsing, rendering, local validation. |
| Focused package groups | `go test ./internal/a ./internal/b` | Feature slices touching several packages. |
| CLI help checks | `go run ./cmd/loom <group> --help` | Command availability and flag surface. |
| CLI JSON checks | `go run ./cmd/loom --json <command>` | User-facing output and local-client behavior. |
| Portal one-shot render | `loom enter --exit-after-render --start <screen>` | Portal screen rendering without manual interaction. |
| Smoke scripts | `tests/smoke/<file>.sh` | Larger workflows and hardware/runtime behavior. |
| Production update validation | Update runbook commands | Rebuild/apply flows after planned release staging. |

## Default Development Checks

For a narrow Go package edit:

```bash
go test ./internal/<package>
git diff --check
```

For a CLI command edit:

```bash
go test ./internal/loomcli ./internal/localclient
go run ./cmd/loom <command> --help
go run ./cmd/loom --json <safe-command>
git diff --check
```

For an API edit:

```bash
go test ./internal/httpapi ./internal/localclient ./internal/<domain>
git diff --check
```

For a portal edit:

```bash
go test ./internal/loomcli/portal ./internal/loomcli
go run ./cmd/loom --no-animation --no-color enter --exit-after-render --start home
git diff --check
```

Change `home` to the specific screen touched.

## Package Test Coverage

Some integration tests skip unless their external prerequisites are explicitly
configured. An ordinary `go test ./...` pass is not proof of database recovery,
live Mac pairing, or a particular NixOS deployment. Use `go test -v` when you need
to review skipped checks. Native Hermes recovery acceptance requires both
`LOOM_TEST_HERMES_PYTHON` and `LOOM_TEST_HERMES_BINARY`; do not infer these from a
maintainer's old Nix store paths. The Python native compatibility tests likewise
require the explicitly selected `HERMES_NATIVE_SOURCE` or `HERMES_NATIVE_ENV`.

Current tests exist across many package areas, including:

- `internal/loomcli`, `internal/loomcli/portal`, and `internal/loomcli/ui`;
- `internal/httpapi` and `internal/localclient`;
- project contracts, scaffolding, watched roots, and activation;
- storage catalog, export, retention, fidelity, archive, and doctor logic;
- knowledge, search, notes projection, and file extraction;
- backup, cloud, maintenance, update, setup, and support bundle logic;
- capabilities, routing, policy, realtime, modules, and agents;
- node agent, bootstrap SSH, communication, sync, and watched roots.

Run the package group that matches the implementation boundary. Avoid
defaulting to broad hardware smoke tests for a small parser or renderer change.

## Smoke Tests

Smoke tests live in `tests/smoke/`. Many are historical slice tests and some
target hardware or production-like state. Before running a smoke test:

1. Read the script.
2. Check whether it targets `loom-main`, `loom-dev`, Mac, or a local fixture.
3. Check whether it writes user-visible files, runs workers, modifies backup
   state, or needs SSH/sudo.
4. Prefer dry-run modes where the script supports them.
5. Keep acceptance artifacts under `.loom-acceptance`.

Production guard helpers live in `tests/lib/production_guard.sh`.

## Portal Render Checks

Portal screen smoke checks should use non-interactive one-shot rendering:

```bash
go run ./cmd/loom --no-animation --no-color enter \
  --exit-after-render \
  --no-boot-animation \
  --start projects
```

This validates screen loading and rendering without relying on manual terminal
navigation. It does not prove every interactive action works; pair it with
action source inspection and CLI checks for the actions that matter.

## JSON And Error Checks

When docs or scripts rely on machine-readable behavior, verify `--json`.
Useful properties:

- command exits successfully for success examples;
- JSON uses `ok`, `data`, `error`, and `meta` envelope conventions where
  applicable;
- errors are specific enough for a later operator or developer to act on;
- no secrets are printed in normal output.

If a command returns a generic error where a specific precondition is known,
record that as a bug instead of hiding it in docs.

## Production-Like Validation

Use the smallest validation that proves the change. For production-like main
and Mac checks:

- prefer read-only status/list/inspect commands;
- use `--dry-run` before apply;
- require explicit operator decisions for backup, restore, purge, update,
  cloud retention, and destructive cleanup;
- capture evidence in notes or handoff files;
- do not leave loose test files outside `.loom-acceptance`.

## Related Docs

- [[Local Development]]
- [[Diagnostics And Support]]
- [[Release Flow]]
- [[Updating And Rebuilding]]
- [[AI Worktree Development]]
