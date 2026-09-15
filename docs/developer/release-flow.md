---
title: "Release Flow"
description: "Developer and operator guide to preparing LOOM releases, staging artifacts, planning updates, and handing off production apply."
audience:
  - developer
  - operator
tags:
  - loom
  - developer
  - operations
  - configuration
status: draft
verified_at: "2026-08-30"
source_scope:
  - "/tmp/loomdocs-v096 update release --help"
  - "loom update release retention --help"
  - "go run ./cmd/loom update release stage --help"
  - "/tmp/loomdocs-v096 update plan --help"
  - "/tmp/loomdocs-v096 update workspace --help"
  - "/tmp/loomdocs-v096 --json update workspace status"
  - "go run ./cmd/loom --json setup workspace install --release-id <release-id> --yes"
  - "internal/setup/launchd.go"
  - "internal/update/workspace.go"
related:
  - "[[Developer Documentation]]"
  - "[[Updating And Rebuilding]]"
  - "[[Migrations]]"
  - "[[Backup Restore And Drills]]"
aliases:
  - "LOOM Release Flow"
---
# Release Flow

## Purpose

A public GitHub source release and an installed host release are different
artifacts. The first developer preview publishes a clean source baseline with
its license, implementation state and known limitations. It does not imply
that every reader's host is configured or that ready-to-install binaries exist.
Version identifiers in older examples describe historical internal development;
do not present them as previously published public releases.

The rest of this page describes the existing operator update workflow after a
host has been configured. A Git tag or GitHub release does not execute it.

The release flow separates development work from production apply. A developer
prepares tested source and release artifacts. An operator applies them through
backup-gated update commands.

## Source To Release

A release should identify:

- source commit;
- release path;
- migrations directory;
- target host/runtime;
- expected services;
- backup requirement;
- rollback class.

The production release path shape is:

```text
/srv/loom/releases/<release-id>
```

The active production source is:

```text
/srv/loom/current
```

## Stage A Release

The CLI exposes release staging:

```bash
loom update release stage \
  --stage-path <stage-path> \
  --target-path <target-path> \
  --backup-scope operational_only
```

Staging is mutating. It copies release material and writes a target-safe
release manifest. `--backup-scope` accepts only `operational_only` or
`canonical_user_data` and writes the versioned `loom.release.backup_scope.v1`
declaration. Classify the release from its reviewed canonical-data effects;
never infer scope from migration counts or generic update step IDs. Omitting
the flag preserves fail-closed compatibility and requires a complete backup.
The published stage root is always a real, non-symlink directory at exact mode
0755, including when the caller pre-creates an empty mode-0700 staging
directory. Staging preserves payload modes and still refuses a non-empty target
without `--overwrite`. Review the manifest before update apply.

## Plan

Production plan:

```bash
loom update plan --release-path /srv/loom/releases/<release-id>
```

Workspace plan:

```bash
loom update workspace plan --release-path <local-release-path>
```

Planning should be read-only. It is where migration delta, backup requirement,
service impact, and rollback class are surfaced. Planning fails before backup
verification or any apply coordination unless the target release root is a
real, non-symlink directory at exact mode 0755 and the trusted release manifest
points back to that exact root. Apply builds this plan again before any backup,
maintenance, release-switch, or command-runner effect.

## Apply Handoff

Production apply is operator-owned:

```bash
loom update apply \
  --release-path /srv/loom/releases/<release-id> \
  --backup-path <backup-ref-or-path> \
  --yes
```

The handoff should include:

- release id and path;
- source commit;
- test summary;
- migration notes;
- backup/restore-drill expectation;
- known caveats;
- rollback class.

## Workspace Updates

Workspace updates switch local release symlinks and write workspace update
manifests:

```bash
loom update workspace status
loom update workspace apply --release-path <local-release-path> --yes
loom update workspace rollback --yes
```

For a Mac workspace release built from the local checkout, use the setup
installer first:

```bash
loom setup workspace install --release-id <release-id> --yes
```

Then immediately align update state with the active release:

```bash
loom update workspace apply --release-path "$(readlink ~/.local/share/loom/current)" --yes
```

The setup installer is responsible for staging the release and ensuring the
macOS LaunchAgent is loaded. The update apply records the active release in the
workspace update manifest, refreshes binary symlinks, performs node-agent
health checks, and uses the loaded LaunchAgent restart path instead of
unloading/rebootstrapping the job.

Inspect your own workspace update status to establish its active release and
available rollback history.

## Release Retention

Release directories are immutable deployment copies. Git preserves source
history, while update manifests preserve audit history; neither requires every
physical release directory to remain forever. Build a read-only plan before
removing old copies:

```bash
loom update release retention plan
loom update release retention plan --workspace
```

The default contract protects the active symlink target, both sides of the
active update manifest, the newest ten existing successful update targets,
explicit pins, and every release younger than 48 hours. Add a known-good pin
when an older release must remain immediately available:

```bash
loom update release retention plan --pin <release-name>
```

Apply requires the exact hash from the reviewed plan and recomputes the plan
before deleting anything:

```bash
loom update release retention apply --plan-hash <sha256:...> --yes
loom update release retention apply --workspace --plan-hash <sha256:...> --yes
```

A broken active pointer, invalid manifest/history evidence, missing pin, or
unexpected entry in the release root blocks deletion.

## Post-Apply Validation

After production apply:

```bash
loom status
loom health
loom backup status
loom maintenance status
loom node health main
loom node health macbook
```

Run focused acceptance only. Keep any production validation files under
`.loom-acceptance`.

## Related Docs

- [[Updating And Rebuilding]]
- [[Migrations]]
- [[Backup Restore And Drills]]
- [[Testing]]
