---
title: "CLI Development"
description: "How to add, change, test, and document LOOM CLI commands."
audience:
  - developer
  - agent
tags:
  - loom
  - developer
  - cli
status: draft
verified_at: "2026-07-07"
source_scope:
  - "internal/loomcli/root.go"
  - "internal/loomcli/*.go"
  - "internal/localclient/client.go"
  - "docs/cli"
related:
  - "[[CLI Reference]]"
  - "[[API Development]]"
  - "[[Testing]]"
  - "[[Command Index]]"
aliases:
  - "Developing CLI Commands"
---
# CLI Development

## What This Page Covers

This page explains the conventions for changing `loom`, the user/operator CLI.
The CLI is a user surface, an automation surface, and the backend for many
portal actions, so command shape and error quality matter.

## Command Ownership

`internal/loomcli/root.go` creates the root command and attaches every command
group. Domain files under `internal/loomcli/` define the groups:

- `projects`, `notes`, `storage`, `box`, `lane`;
- `backup`, `cloud`, `database`, `maintenance`, `support`;
- `automation`, `jobs`, `workers`, `scripts`, `artifacts`;
- `node`, `sync`, `watched-roots`, `communication`, `setup`, `update`;
- `providers`, `capabilities`, `policy`, `approvals`, `grants`, `modules`,
  `agents`, `realtime`.

Keep command parsing and rendering in `internal/loomcli`. Keep durable
business logic in domain packages.

## Command Shape

Prefer explicit noun/verb shape:

```text
loom <domain> <verb>
loom <domain> <resource> <verb>
```

Use singular/plural intentionally. If the root help lists `loom project`, do
not document `loom projects` unless the alias exists and is tested.

## Output Modes

Most commands should support:

- human-readable default output;
- `--json` for automation;
- `--plain` only when a compact script-friendly scalar makes sense;
- `--verbose` when extra human detail is useful.

For JSON output, return current envelope shapes when talking to `loomd`. Do not
invent a one-off JSON structure in a command if a domain response already
exists.

## Local Client Boundary

Commands that talk to the daemon should call `internal/localclient`. If a new
endpoint is needed, add:

1. domain input/result types;
2. HTTP handler;
3. local-client method;
4. CLI command;
5. focused tests.

Do not build URLs or socket transports ad hoc inside command functions.

## Safety And Confirmation

Mutating commands should make safety visible:

- add `--dry-run` when planning is possible;
- require `--yes`, `--confirm`, or a strong confirmation for destructive or
  production-like operations;
- prefer fixture examples in docs;
- return specific precondition errors instead of generic runtime failures;
- preserve idempotency keys for retryable operations.

Examples of commands that need extra care are backup restore, storage cleanup,
cloud retention apply, update apply, project archive, grant revoke, approval
decide, and capability call without `--dry-run`.

## Error Handling

Use existing LOOM error helpers and domain errors. A good CLI error tells the
operator:

- what failed;
- which target failed;
- whether retrying is safe;
- whether the fix is config, auth, missing state, or a product bug.

Do not collapse known validation states into `runtime.error`.

## Tests

For a command-only change:

```bash
go test ./internal/loomcli
go run ./cmd/loom <command> --help
git diff --check
```

For command changes that use the daemon:

```bash
go test ./internal/loomcli ./internal/localclient ./internal/httpapi ./internal/<domain>
go run ./cmd/loom --json <safe-command>
```

Use `--dry-run` or `.loom-acceptance` fixtures for mutating workflows.

## Documentation

When command behavior changes, update:

- the relevant page under `docs/cli/`;
- `docs/reference/command-index.md` if command availability changed;
- any user-guide, operations, or portal page that shows the workflow;
- the active feature audit file when the docs-writing plan requires it.

## Related Docs

- [[CLI Reference]]
- [[Command Index]]
- [[API Development]]
- [[Portal Development]]
- [[Testing]]
