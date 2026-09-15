---
title: "Diagnostics And Support"
description: "Collect safe LOOM diagnostic evidence with health, status, Doctor, and redacted support bundles."
audience:
  - user
  - operator
tags:
  - loom
  - user-guide
  - troubleshooting
status: draft
verified_at: "2026-07-07"
source_scope:
  - "go run ./cmd/loom status --help"
  - "go run ./cmd/loom health --help"
  - "go run ./cmd/loom support --help"
  - "go run ./cmd/loom support bundle create --help"
  - "go run ./cmd/loom --json support bundle create --dry-run --profile minimal --max-items 5 --max-bytes 4096"
  - "go run ./cmd/loom --no-animation --no-color enter --exit-after-render --no-boot-animation --start doctor"
related:
  - "[[Getting Started]]"
  - "[[Daily Use]]"
  - "[[Runtime Status And Support]]"
  - "[[Home And Doctor]]"
  - "[[Operations]]"
aliases:
  - "Support Evidence"
  - "LOOM Diagnostics"
---
# Diagnostics And Support

## What This Page Covers

This page explains the safe first steps when LOOM looks wrong. It focuses on
evidence collection, not repair.

Use it when:

- `loom status` reports warnings or failures;
- Home shows attention;
- Doctor shows findings;
- a workflow fails and you need a support bundle;
- you are writing a reproducible bug report.

## First Checks

Run read-only checks first:

```bash
loom --json health
loom --json status
```

`health` gives daemon-level checks. `status` gives the broader daily summary and
next inspection hints.

If a command fails, keep the exact command, timestamp, and JSON error. Do not
hide the failure by switching to raw shell unless a later operations runbook
explicitly tells you to inspect the host.

## Check Doctor

Open Doctor in the portal:

```bash
loom enter --start doctor
```

For a noninteractive render:

```bash
loom --no-animation --no-color enter --exit-after-render --no-boot-animation --start doctor
```

Doctor groups attention into areas and gives a safe next action. It is the
right place to check whether an issue belongs to system, database, workers,
jobs/indexing, automation, nodes/sync, Box/Lane, storage, cloud/backup, notes,
projects, portal, or an unknown area.

## Create A Support Bundle Plan

Use a dry-run first:

```bash
loom --json support bundle create --dry-run --profile minimal --max-items 5 --max-bytes 4096
```

Slice 4 verified this command. It returned a planned support bundle with:

- a bundle ID;
- output path;
- selected profile;
- `dry_run: true`;
- privacy settings;
- collectors that would run;
- skipped collectors and reasons.

The minimal dry-run does not write the archive. It tells you what evidence
would be collected.

## Create A Support Bundle

When you actually need the archive, remove `--dry-run` and choose a profile:

```bash
loom support bundle create --profile minimal
```

Profiles are:

- `minimal`;
- `default`;
- `full`.

The support command supports bounded collection options:

- `--max-items`;
- `--max-bytes`;
- `--timeout`;
- `--include-logs`;
- `--include-live`;
- `--include-projects`;
- repeated `--project`;
- `--output`.

Use the smallest profile that captures the issue. Add logs or live probes only
when needed.

## Privacy Rules

Support bundles are intended to be redacted, bounded artifacts, but you should
still treat them as operational evidence.

Do not include:

- passwords;
- tokens;
- private keys;
- raw credentials;
- unrelated user files;
- broad absolute paths unless the recipient needs host-specific path evidence.

Use `--include-absolute-paths` only when a path bug cannot be diagnosed through
root aliases.

## Acceptance Artifacts

Acceptance-test artifacts should live under `.loom-acceptance` folders. The
support command exposes a cleanup helper:

```bash
loom support acceptance cleanup --root <path>
```

Without `--yes`, cleanup is a dry-run. With `--yes`, it applies cleanup. With
`--delete-now`, it deletes eligible acceptance fixtures instead of archiving
them. Treat `--delete-now` as a deliberate operator action.

## A Good Bug Report

For a reproducible LOOM bug, capture:

- exact command or portal action;
- node and machine context;
- time of failure;
- JSON output when available;
- Doctor findings;
- support bundle ID or dry-run plan;
- whether the action was read-only, dry-run, fixture-mutating, or
  production-like.

Historical v0.9.6 findings remain archived. New product or runtime bugs belong
in the current `.project/` workflow.

## Related Docs

- [[Runtime Status And Support]]
- [[Home And Doctor]]
- [[Daily Use]]
- [[Operations]]
- [[Command Safety]]
