---
title: "Runtime Status And Support"
description: "CLI reference for read-only runtime status, health, portal entry, version, and support evidence commands."
audience:
  - user
  - operator
  - developer
tags:
  - loom
  - cli
  - troubleshooting
status: draft
verified_at: "2026-08-16"
source_scope:
  - "go run ./cmd/loom status --help"
  - "go run ./cmd/loom health --help"
  - "go run ./cmd/loom version --help"
  - "go run ./cmd/loom enter --help"
  - "go run ./cmd/loom support --help"
  - "go run ./cmd/loom support bundle create --help"
  - "go run ./cmd/loom support acceptance cleanup --help"
  - "go run ./cmd/loom --json status"
  - "go run ./cmd/loom --json health"
  - "go run ./cmd/loom --json support bundle create --dry-run --profile minimal --max-items 5 --max-bytes 4096"
  - "internal/loomcli/enter.go"
  - "internal/loomcli/portal/availability.go"
related:
  - "[[CLI Reference]]"
  - "[[Getting Started]]"
  - "[[Daily Use]]"
  - "[[Diagnostics And Support]]"
  - "[[Home And Doctor]]"
aliases:
  - "Status CLI"
  - "Support CLI"
---
# Runtime Status And Support

## What This Page Covers

This page documents the first CLI commands for checking LOOM and collecting
diagnostic evidence:

- `loom status`;
- `loom health`;
- `loom version`;
- `loom enter`;
- `loom support`.

These commands are the normal starting point before project, notes, storage,
backup, automation, node, capability, or maintenance workflows.

## Safety Class

| Command | Safety Class | Notes |
|---|---|---|
| `loom status` | read-only | Summarizes system state and next inspections. |
| `loom health` | read-only | Checks daemon health. |
| `loom version` | read-only | Prints CLI version. |
| `loom enter` | read-only display | Opens interactive portal unless one-shot flags are used. |
| `loom support bundle create --dry-run` | read-only planning | Shows bundle plan without writing archive. |
| `loom support bundle create` | bounded artifact creation | Writes a redacted archive. Review profile and output. |
| `loom support acceptance cleanup` without `--yes` | dry-run | Scans `.loom-acceptance` cleanup plan. |
| `loom support acceptance cleanup --yes` | mutating | Archives or deletes eligible acceptance fixture children. |

## `loom status`

Purpose:

```bash
loom status
loom --json status
```

`loom status` shows concise LOOM system status. Slice 4 verified the command
help and a live JSON status check.

The JSON response can include:

- top-level status;
- daemon health;
- node identity;
- job counts;
- recent event count;
- search/indexing attention;
- runner state;
- next inspection suggestions;
- response metadata such as correlation ID, source, freshness, and generated
  time.

Use `loom --json status` in test logs because it preserves exact fields.

## `loom health`

Purpose:

```bash
loom health
loom --json health
```

`loom health` shows daemon health. Slice 4 verified the command help and a live
JSON health check.

Healthy output should include checks for:

- config;
- database;
- storage;
- migrations;
- bootstrap.

If health is not ok, inspect health before running broader workflows.

## `loom version`

Purpose:

```bash
loom version
```

This prints the CLI version. Use it in bug reports when command behavior does
not match docs.

## `loom enter`

Purpose:

```bash
loom enter
```

`loom enter` opens the terminal portal. Useful flags:

```bash
loom enter --start home
loom enter --start doctor
loom enter --compact
loom enter --exit-after-render --no-boot-animation --start home
```

`--exit-after-render` renders one portal view and exits. This is the preferred
portal smoke-test path.

If main cannot be reached, `loom enter` starts in offline mode instead of
returning a startup error. The render names the selected HTTP main URL or Unix
socket path. Home, Doctor, and Box keep useful local state; main-owned surfaces
render an unavailable explanation. Press `r` in an interactive portal to retry.

Use these read-only offline smoke checks with a test configuration or an
intentionally unreachable localhost target:

```bash
loom --no-animation --no-color enter --exit-after-render --no-boot-animation --start home
loom --no-animation --no-color enter --exit-after-render --no-boot-animation --start doctor
loom --no-animation --no-color enter --exit-after-render --no-boot-animation --start projects
```

`portal.start_failed` is reserved for a terminal/application startup failure.
An offline main is normal portal state, not that error. Errors from one-shot
actions or commands use `portal.operation_failed` unless they have a more
specific confirmation code.

The command help says JSON, plain, and noninteractive paths fail cleanly for the
interactive portal instead of printing prompts or terminal control sequences.

## `loom support`

Purpose:

```bash
loom support --help
```

The support command currently has:

- `loom support bundle`;
- `loom support acceptance`.

### Bundle Dry-Run

Use dry-run first:

```bash
loom --json support bundle create --dry-run --profile minimal --max-items 5 --max-bytes 4096
```

Slice 4 verified this command. It returned a planned support bundle without
writing the archive.

### Bundle Create

Create an archive when you need evidence:

```bash
loom support bundle create --profile minimal
```

Important flags:

- `--dry-run`;
- `--profile minimal|default|full`;
- `--output`;
- `--max-items`;
- `--max-bytes`;
- `--timeout`;
- `--include-logs`;
- `--include-live`;
- `--include-projects`;
- `--project`;
- `--include-absolute-paths`.

Use the smallest profile that captures the problem. Add logs, live probes, or
absolute paths only when needed.

### Acceptance Cleanup

Acceptance cleanup is for children under `.loom-acceptance` folders:

```bash
loom support acceptance cleanup --root <path>
```

Without `--yes`, the command is a dry-run. With `--yes`, it applies cleanup.
With `--delete-now`, it deletes eligible acceptance fixtures instead of
archiving them.

Do not use acceptance cleanup as a general storage cleanup command.

## Output Flags

These commands inherit common global flags:

- `--json`;
- `--plain`;
- `--verbose`;
- `--no-color`;
- `--no-animation`;
- `--no-interactive`;
- `--config`;
- `--socket`;
- `--correlation-id`.

Prefer `--json` for evidence and `--correlation-id` when tracing one workflow.
Normal human errors stay concise. Add `--verbose` when you need one bounded,
sanitized underlying cause line; credentials and URL query or fragment values
are not printed.

## Related Docs

- [[Getting Started]]
- [[Daily Use]]
- [[Diagnostics And Support]]
- [[Home And Doctor]]
- [[Command Index]]
