---
title: "Backups Cloud And Restore"
description: "Check backup coverage, create and verify main backups, inspect cloud snapshots, and rehearse recovery without touching live data."
audience:
  - user
  - operator
tags:
  - loom
  - user-guide
  - backup
  - cloud
status: verified
verified_at: "2026-08-31"
source_scope:
  - "go run ./cmd/loom backup --help"
  - "go run ./cmd/loom cloud snapshot --help"
  - "internal/backupcoverage"
  - "internal/maintenance"
  - "internal/backupstrategy/archive_manifest_v2.go"
  - "internal/workers/tick_policy.go"
  - "internal/workers/runtimes/cloud_snapshot.go"
  - "tests/smoke/v2_cloud_backup_runtime_optimization_local.sh"
related:
  - "[[Backup Sync Retention And Cloud]]"
  - "[[Backup Restore And Drills]]"
---
# Backups Cloud And Restore

Treat recovery as a chain of evidence, not as the presence of one folder.

## Check Coverage First

```bash
loom backup status
loom backup coverage
loom backup contracts plan
```

Coverage should name configured canonical roots. A legacy generated export is
rebuildable compatibility state and must not appear as a source that makes a
backup complete.

Protected-folder contracts are explicit Box contracts. Plan and preflight
before enabling them; offline owner nodes remain pending rather than falsely
applied.

## Create And Verify A Main Backup

Start with a non-mutating plan:

```bash
loom backup create --production --dry-run
```

The real create operation writes only to the configured operational backup
root:

```bash
loom backup create --production
loom backup list --limit 10
loom backup verify <backup-ref-or-path>
```

A valid result verifies the database dump, manifest, health evidence, and every
declared custody snapshot. In-progress backup/archive batches are skipped until
their `manifest.json` commit marker exists.

Imports is mandatory first-class coverage. Its manifest names the explicit
custody policy and portable evidence. Existing migrated markerless production
custody stays in complete-physical mode after filesystem cutover; that means
all physical bytes and symlink targets are covered without falsely claiming
ordinary Lane commit provenance. A later switch to committed-only mode is a
separate reviewed conversion, not a side effect of changing paths.

## Rehearse Restore

Dry-run selects and validates the backup without creating a drill database:

```bash
loom backup restore-drill <backup-ref-or-path> --dry-run
```

The full drill restores into a temporary database and drops it afterward:

```bash
loom backup restore-drill <backup-ref-or-path>
```

Neither form restores into live PostgreSQL or live filesystem roots.

## Check Cloud Recovery

```bash
loom cloud doctor
loom cloud snapshot backend status
loom cloud snapshot status
loom cloud snapshot list
loom cloud snapshot verify latest --profile metadata
```

The same verify command offers three explicit Borg assurance profiles:

```bash
loom cloud snapshot verify latest --profile metadata
loom cloud snapshot verify --profile rolling_repository --max-duration 30m
loom cloud snapshot verify <exact-borg-archive> --profile archive_data
```

`metadata` verifies one archive's metadata and is the default.
`rolling_repository` accepts no archive argument and spends only its positive
time budget on repository checking; success means bounded progress, not that
one archive's complete data was reread. `archive_data` requires the exact
canonical Borg archive name, rejects `latest`, and performs the complete
archive-data check. The result reports the chosen profile and coverage so these
claims cannot be confused. None of the commands creates an automatic schedule.

Plan before upload:

```bash
loom cloud snapshot push --backup latest --dry-run
```

Both supported backends verify canonical backup coverage before remote
mutation. Fetch also uses an explicit empty staging destination and does not
perform a live restore.

For current direct Borg archives, a restore-drill dry-run authenticates the
remote archive manifest, confines extracted payload to the declared roots, and
verifies the separate operational and provenance recovery packages. V1 retains
complete entry/hash comparison; v2 relies on Borg for payload authentication
and LOOM for root and bounded-package verification. A complete strict restore
must actually recover both packages into disposable targets; an archive check
or extracted files alone do not count.

New routine archives use a v2 authenticated envelope and Borg's incremental
files cache. They still inspect the filesystem namespace, but unchanged file
contents are not reread by LOOM for a second source hash or archive-wide hash
list. Routine cost therefore follows entries plus changed bytes. A successful
routine archive proves its exact policy and recovery packages, metadata graph,
changed chunks, and pending-to-canonical commit; it does not prove that every
older payload chunk was reread. Use `archive_data` or a strict restore when you
need those stronger claims.

The supported daily-local production shape is main recovery-package creation
at `03:00` followed by cloud archive at `03:15 Europe/Amsterdam`. These policies
are not activated merely by installing the software or running disposable
acceptance. When an operator activates them in a separate reviewed checkpoint,
the scheduler preserves local wall-clock behavior across DST and the cloud run
refuses yesterday's, missing, pre-window, or post-start package before remote
mutation. The cloud worker keeps its two-hour timeout and single-run/remote-lock
boundaries.

Borg retention uses a fixed 14-daily/8-weekly/6-monthly plan. Save and review
the JSON plan, then apply it only with its exact digest and explicit
confirmation. Acceptance and milestone archives are protected classes. Prune
and compact have separate results, and LOOM does not claim reclaimed space
from command success alone. Even after strict restore, migration eligibility
is only a report: local cleanup remains a later production decision.

Before enabling an unattended Imports-inclusive schedule, operators must have
a reviewed local generation-retention/cleanup policy and measured byte/inode
capacity. The canonical direct Borg path has already committed its first
production archive, while strict recovery has passed disposable software
acceptance. The deferred operator action is a separately reviewed activation
of the `03:00` and `03:15 Europe/Amsterdam` daily-local policies, followed by
one measured package and archive observation that confirms duration, timeout,
cache identity, remote capacity, retention, and storage growth. Until then,
operators can run explicit one-shot backup or cloud commands without claiming
that the daily policies are active. A timed-out run remains failed/unpublished
evidence, not a successful backup.

## Interpreting Problems

- **coverage missing**: a canonical durable root lacks declared protection;
- **custody manifest invalid**: identity, path confinement, object existence,
  size, or checksum evidence failed;
- **cloud snapshot mismatch**: do not overwrite the same-named remote object;
- **restore drill failed**: the backup is not operationally trusted even if
  upload succeeded.

## Related Docs

- [[Backup Sync Retention And Cloud]]
- [[Backup Restore And Drills]]

## Complete live reference and preserved operational detail

The canonical guidance above is the current storage contract. The detailed material below preserves the complete live command, route, workflow, safety, interpretation, and troubleshooting coverage that predates the filesystem migration. Retired generated-export behavior is retained only when explicitly labelled as compatibility or historical context.

### What This Page Covers

This page is the safe user workflow for answering four questions:

- is LOOM creating main-node backups;
- does current data have backup coverage;
- is the cloud snapshot layer reachable;
- is a restore drill possible when an operator needs to prove recovery.

The commands here are read-only or dry-run unless explicitly marked otherwise.
For the deeper operator runbook, use [[Backup Restore And Drills]]. For the
model behind local backups, cloud snapshots, retention, and sync, use
[[Backup Sync Retention And Cloud]].

### Fast Backup Check

Start with backup status:

```bash
loom backup status
loom backup list --limit 5
```

At verification time, `loom backup status --json` returned `status=ok`, zero
open findings, and a latest successful backup operation. `loom backup list
--limit 5 --json` returned five recent records with the newest status
`succeeded`.

If status is not `ok`, go to [[Database Maintenance]] and
[[Backup Restore And Drills]] before running any apply command. A failed or
missing backup is not a reason to delete source files.

### Coverage Check

Coverage asks whether the important LOOM custody roots are represented by the
backup policy:

```bash
loom backup coverage
loom backup contracts plan
```

At verification time, coverage was `ok` in `main_backed` mode, with 22 entries
and zero critical missing paths. The contract plan reported all 22 contracts
satisfied.

Coverage includes more than the PostgreSQL database. The current backup plan
also accounts for object store data, private backups, main Documents, storage
retention, canonical storage archive custody, Box Notes, and the generated
Notes projection. The retired storage export is excluded rather than backed up.

### Protect An Extra Folder

Project-owned folders should normally use the project's generated
`.loom/contracts/backup.yaml`. For an extra node-local folder that is not owned
by a project, use **Protect Folder** on the Portal's **Nodes And Watched Roots**
screen:

1. Choose the node that owns the folder and enter the absolute path on that
   node.
2. Start the preflight. LOOM asks the owner node to inspect the folder without
   following symlinks or returning file contents.
3. Review the canonical path, protected and ignored counts, apparent bytes,
   effective `.loomignore` policy files, and any findings.
4. Confirm only when the preflight is complete and has no blocking finding.
5. Wait for the owner node to apply the current desired configuration, then for
   an accepted backup before treating the folder as protected.

The workflow is durable. You can leave the Portal while preflight or activation
is pending and resume from the protected-folder details later. If main is
offline, Protect Folder and its lifecycle actions are disabled. If main accepts
the request but the owner node is offline, the folder remains **Waiting For
Node**; queued work is not proof of protection.

#### Understand The Lifecycle

| Status | Meaning |
|---|---|
| Waiting For Node | Main accepted pending work, but the owner node is unavailable. |
| Ready | Preflight completed and the reviewed contract can be created. |
| Activating | Desired state is registered or queued, but current owner-node application/report evidence is incomplete. |
| Active | The current configuration is applied and reported, but no accepted backup after that acknowledgement proves custody yet. |
| Protected | The current configuration is applied and reported, and a later backup was accepted. |
| Attention | A blocking preflight finding, apply failure, configuration mismatch, failed latest backup, or missing canonical YAML needs review. |
| Disabled | The contract remains recorded but no longer requests an active watched root. |

#### Advanced CLI Workflow

The CLI exposes the same control-plane contract for operators and automation.
Preflight first and pass the returned id to create:

```bash
loom backup contracts preflight \
  --node main \
  --path /srv/field-data

loom backup contracts create field-data \
  --node main \
  --path /srv/field-data \
  --display-name "Field Data" \
  --preflight <completed-preflight-id>
```

Check the lifecycle rather than treating a successful mutation response as an
applied backup:

```bash
loom backup contracts status --node main
loom backup contracts inspect field-data
```

The main daemon stores canonical desired contract YAML in its configured Box:

```text
<box-root>/.loom/contracts/backup/field-data.yaml
```

The selected owner node owns and scans `/srv/field-data`; it does not store the
canonical contract merely because it owns the source. Recheck a changed folder
or retry a failed activation without creating a second contract:

```bash
loom backup contracts recheck field-data
loom backup contracts retry-activation field-data --reason "owner node restored"
```

`recheck` identifies the existing managed contract, safe root, and watched-root
worker. That lets the folder overlap its own active registration without
weakening first-time protection checks; overlap with another contract or any
other configured/generated/runtime root still blocks.

To stop a standalone policy without losing the contract history, disable it:

```bash
loom backup contracts disable field-data
```

To remove the contract file, use the Portal's strong confirmation or the CLI's
explicit confirmation:

```bash
loom backup contracts delete field-data --yes
```

Neither disabling nor deleting a contract deletes the source folder or retained
backup data. Deletion only removes the canonical desired contract and asks the
owner node to remove the managed watched root. While that tombstone is queued,
status remains `waiting_for_node` or `activating`. After the matching owner-node
acknowledgement, the deleted contract leaves the normal list while audit and
registration history remain. A YAML file that disappears without an
intentional delete remains visible as `attention`.

### Check Effective Ignore Policy

A watched root uses the `managed` backup profile. Mandatory LOOM runtime state,
known reconstructible dependency/cache trees, and matching `.loomignore` paths
are omitted. `.git`, `.env`, `.secrets`, and `.data` are retained unless a user
rule excludes them; `.git` may contain local-only history and refs. LOOM does
not import `.gitignore`, because source-control inclusion and recoverability are
different decisions.

Review the policy and protected/ignored counts before source cleanup:

```bash
loom ignore inspect <root> --operation backup
loom ignore explain <path> --root <root> --operation backup
```

An ignored path is not protected by that backup and cannot be recovered from
it unless another custody path retained it. A backed-up but unindexed file is
different: search may not expose its content, but the backup remains recovery
evidence.

Older contracts may contain a copied expanded list of legacy exclusions.
Migrate them through the guarded main-backed command:

```bash
loom backup contract migrate-ignore-policy --dry-run
loom backup contract migrate-ignore-policy --apply --yes
```

The apply updates contract policy after review; it does not rewrite retained
payloads or make `.loomignore` exclusions recoverable.

### Backup Create Dry-Run

Use a dry-run before any manual backup:

```bash
loom backup create --dry-run --production
```

The command requires either `--production` or `--allow-non-production`. That is
intentional: the operator must say whether this is a production main-node
backup or a non-production test.

At verification time, dry-run returned `status=planned`, backend
`maintenance.main_backup`, nine capture groups, and six excluded secret classes.
The exclusions include raw credential material such as private keys, node
tokens, enrollment tokens, environment secret files, and raw database URLs.

### Verify A Backup

Verify a registered backup by operation id:

```bash
loom backup verify <backup-ref>
```

At verification time, the latest registered backup verified successfully from
Mac against main. Verification can prove that the registered backup artifact is
known and valid without running a database restore.

### Restore Drill Boundary

Restore drills are stronger than verification:

```bash
loom backup restore-drill <backup-ref> --dry-run --target-database loom_restore_drill_example
```

The drill process must be able to read the backup directory locally. From Mac,
the latest registered main backup verified correctly, but restore-drill dry-run
returned:

```text
backup.restore_drill_requires_local_directory
```

That is a placement requirement. Run local backup restore drills on the node
that can read `/var/lib/loom/backups/main/...`, or use a fetched cloud snapshot
staging directory as the local input. Do not point restore drills at the active
LOOM database. Drill database names must start with `loom_restore_drill_`.

### Cloud Snapshot Check

Start with cached cloud status when you only need a quick answer:

```bash
loom cloud status --cached
loom cloud snapshot backend status --cached
```

At verification time, cached cloud status was healthy, cloud was enabled, the
snapshot backend was `borg`, and the main-backed source was
`main-authoritative`.

Run live cloud diagnostics only when you are deliberately checking the remote:

```bash
loom cloud doctor
loom cloud snapshot status
loom cloud snapshot list
```

At verification time, `loom cloud doctor --json` returned `status=ok`.
`loom cloud snapshot list --json` returned a clean empty Borg inventory.

Cloud commands can temporarily return `lock_busy` when another LOOM cloud probe
or worker holds the Storage Box lock. Treat that as a cooldown/serialization
state, not immediately as cloud data loss. Retry after the other operation
finishes.

Direct cloud snapshot push is an operator path, not a normal user check:

```bash
loom cloud snapshot push --backup latest --dry-run
```

It reads backups from the machine running the command. If you run it on a Mac
that cannot read main's backup root, it returns
`cloud.snapshot_push_backup_missing`. Use the main worker path or run the direct
push on the node that owns the backup.

### Cloud Retention Check

Plan retention before applying it:

```bash
loom cloud snapshot retention status --keep-latest 14
loom cloud snapshot retention plan --keep-latest 14
```

At verification time, retention status was `empty`, with zero kept snapshots
and zero removal candidates. Apply is an operator action:

```bash
loom cloud snapshot retention apply --keep-latest 14 --confirm
```

On the Borg backend, retention deletes old matching Borg archives after the
plan validates the LOOM archive naming pattern. On the legacy tree backend,
retention moves old valid snapshots into retention trash instead of deleting
them directly.

### What To Do When A Check Fails

Use this order:

1. Check `loom backup status` and `loom maintenance status`.
2. Check `loom backup coverage` and `loom backup contracts plan`.
3. Check `loom cloud status --cached`.
4. Run `loom cloud doctor` only when a live cloud probe is appropriate.
5. Run dry-runs before backup creation, database compaction, cloud retention, or
   restore drills.
6. Preserve the error code and correlation id when reporting a failure.

Do not start with cleanup, retention apply, database compaction apply, cloud
retention apply, or restore drill apply.

### Related Docs

- [[Backup Sync Retention And Cloud]]
- [[Backup Cloud And Maintenance CLI Reference]]
- [[Backup Restore And Drills]]
- [[Cloud Storage]]
- [[Database Maintenance]]
