---
title: "Troubleshooting"
description: "First-pass troubleshooting order for LOOM status, health, portal, jobs, storage, notes, backup, cloud, and node issues."
audience:
  - user
  - operator
  - developer
tags:
  - loom
  - operations
  - troubleshooting
status: draft
verified_at: "2026-08-16"
source_scope:
  - "docs/user-guide/diagnostics-and-support.md"
  - "internal/loomcli/portal/availability.go"
  - "internal/loomcli/enter.go"
related:
  - "[[Diagnostics And Support]]"
  - "[[Runtime Status And Support]]"
  - "[[Support Bundles]]"
  - "[[Command Safety]]"
aliases:
  - "Troubleshooting LOOM"
---
# Troubleshooting

## What This Page Covers

This runbook gives a safe first-pass order for diagnosing LOOM issues. It
starts with read-only commands and only escalates to repair or apply operations
after the failing area is known.

## First Commands

Run:

```bash
loom status
loom health
loom maintenance status
```

If you need machine-readable evidence:

```bash
loom --json status
loom --json health
loom --json maintenance status
```

## Portal Checks

Open Home and Doctor:

```bash
loom enter --start home
loom enter --start doctor
```

For noninteractive proof that rendering works:

```bash
loom --no-animation --no-color enter --exit-after-render --no-boot-animation --start doctor
```

If a portal surface opens the wrong target or exposes an action in a context
where it should be suppressed, record that as a portal bug. Do not treat wrong
navigation as expected workflow.

### Main Offline Or Portal Startup Failure?

These are different conditions:

- `Main is offline` means the terminal portal started correctly but its bounded
  health probe could not reach the selected main URL or Unix socket. Home,
  Doctor, and Box remain useful; main-owned screens show unavailable. Restore
  the route or main service and press `r`.
- `portal.start_failed` means the terminal application itself could not start.
  Check terminal mode, input/output streams, and the concise error. Add
  `--verbose` for one bounded cause line.
- `portal.operation_failed` means a requested one-shot action, preview, or
  command could not complete. Its target identifies the selected main
  transport when relevant.

For a safe connection check, use an isolated config and an intentionally
unreachable localhost target with the three one-shot commands above. A valid
offline render exits zero. Invalid configuration or a true application startup
failure remains non-zero.

## Narrow The Domain

Use the domain status page or command that matches the symptom:

| Symptom | First Checks |
|---|---|
| CLI cannot reach LOOM | `loom health`, setup status, socket/config checks |
| Jobs stuck or failing | `loom jobs status`, `loom jobs failures`, `loom workers list` |
| Notes/search stale | `loom notes overview`, `loom indexes status`, `loom indexes failures` |
| Storage safety unclear | `loom storage status`, `loom storage safe-delete check <path>` |
| Backup uncertainty | `loom backup status`, `loom backup coverage`, `loom backup list` |
| Cloud uncertainty | `loom cloud status --cached`, `loom cloud doctor` |
| Node/sync issues | `loom node list`, `loom communication health`, `loom sync status --node <node>` |
| Policy/capability issue | `loom policy decisions list`, `loom capabilities search <term>` |

## Escalation Rules

Before repair/apply:

1. Capture read-only status.
2. Run a dry-run or plan command if available.
3. Confirm the target is a fixture or intended real target.
4. Check whether the operation is local, main-owned, or cross-node.
5. Check whether a backup or rollback path is required.

Do not run broad cleanup, restore, purge, update, grant, approval, or cloud
retention commands as first-pass troubleshooting.

## Evidence Collection

Use support bundles when you need a bounded diagnostic artifact:

```bash
loom support bundle create --dry-run --profile minimal
```

Then create the real bundle only when the dry-run scope is acceptable.

## Known Bug Tracking

Use the public repository's Issues for reproducible bugs. Include the source
revision, platform, failing command, redacted error and expected behavior.
Do not upload credentials, raw private support bundles or database dumps.
See [Contributing](../../CONTRIBUTING.md) and [Security](../../SECURITY.md).

## Related Docs

- [[Diagnostics And Support]]
- [[Runtime Status And Support]]
- [[Support Bundles]]
- [[Command Safety]]
- [[Database Maintenance]]
