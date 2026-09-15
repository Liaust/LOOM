---
title: "Backup Cloud And Maintenance CLI Reference"
description: "Commands for backup coverage, contracts, verification, restore drills, cloud snapshots, database care, and maintenance."
audience:
  - user
  - operator
  - developer
tags:
  - loom
  - cli
  - backup
  - cloud
  - operations
status: verified
verified_at: "2026-08-31"
source_scope:
  - "go run ./cmd/loom backup --help"
  - "go run ./cmd/loom cloud --help"
  - "go run ./cmd/loom maintenance --help"
  - "go run ./cmd/loom database --help"
related:
  - "[[Command Safety]]"
  - "[[Backup Restore And Drills]]"
  - "[[Backup Sync Retention And Cloud]]"
---
# Backup Cloud And Maintenance CLI Reference

## Safety

Use status, coverage, list, verify, plan, doctor, and `--dry-run` first.
Creation, contract changes, snapshot upload/retention, compaction apply, and
maintenance mutations require their documented confirmations. Restore drills
are isolated; a production restore is runbook-driven and is not automated by
these commands.

## Main Backup

```bash
loom backup status
loom backup coverage
loom backup create --production --dry-run
loom backup create --production
loom backup list --limit 10
loom backup verify <backup-ref-or-path>
loom backup restore-drill <backup-ref-or-path> --dry-run
loom backup restore-drill <backup-ref-or-path>
```

Coverage and backup creation use configured canonical roots. The Notes
projection is excluded by default; the retired generated storage export is
always non-canonical. Imports coverage is mandatory in current v0.9 manifests;
the explicit complete-physical or committed-Lane policy does not change merely
because filesystem roots are cut over.

## Backup Contracts

```bash
loom backup contracts plan
loom backup contracts status
loom backup contracts preflight --node <node> --path <absolute-folder>
loom backup contracts create --dry-run ...
loom backup contracts enable <contract>
loom backup contracts disable <contract>
loom backup contracts recheck <contract>
loom backup contracts retry-activation <contract>
loom backup contracts delete <contract>
```

Contracts live in Box and route acceptance through the owner node. Desired,
queued, applied, failed, and offline states remain distinct. Read the command
help for required identities and confirmation flags before mutation.

## Cloud Snapshot

```bash
loom cloud doctor
loom cloud snapshot backend doctor
loom cloud snapshot backend status
loom cloud snapshot status
loom cloud snapshot list
loom cloud snapshot verify latest --profile metadata
loom cloud snapshot verify --profile rolling_repository --max-duration 30m
loom cloud snapshot verify <exact-borg-archive> --profile archive_data
loom cloud snapshot push --backup latest --dry-run
loom cloud snapshot fetch latest --to <empty-staging-directory>
loom cloud snapshot restore-drill latest --dry-run
loom cloud snapshot retention plan --json > /secure/review/loom-retention-plan.json
loom cloud snapshot retention apply --plan /secure/review/loom-retention-plan.json \
  --confirm-digest <exact-plan-digest> --confirm
```

Backend initialization and retention apply are explicit mutations. Snapshot
push refuses incomplete local coverage and uses the same canonical root options
for Borg and legacy-tree backends. Legacy-tree preserves links and metadata and
rehydrates evidence-recorded metadata before strict verification; Borg restore
must satisfy the same evidence without rehydration.

For Borg, `--keep-latest` is compatibility input and does not alter the fixed
14-daily/8-weekly/6-monthly policy. Apply accepts either the exact plan object
or the JSON response envelope written by `plan --json`. `--plan` and
`--confirm-digest` must describe the same reviewed plan. `--compact` requests a
separate compact stage after a verified prune; neither stage reports reclaimed
bytes without independent measurement.

## Database And Maintenance

```bash
loom database status
loom database compact --dry-run
loom maintenance status
loom maintenance findings list --limit 20
```

Database compaction is operational database maintenance, not filesystem
cleanup. Maintenance operations are durable audit records; inspect the exact
operation before retrying or acknowledging it.

## Failure Rules

- Do not upload an unverified local backup.
- Do not enable recurring Imports-inclusive local backup until generation
  retention and inode/byte growth have a reviewed bound.
- Do not resume scheduled cloud upload until remote capacity/retention and the
  chosen backend have passed snapshot/fetch/restore acceptance.
- Measure the first Imports-inclusive local/cloud runs and explicitly approve
  timeouts large enough for the selected backend; timeout means failed and
  unpublished, never accepted.
- Do not overwrite a same-named cloud snapshot that fails verification.
- Do not treat a successful upload as a successful restore drill.
- Do not purge production main without the production-main confirmation,
  exact node identity, and a verified backup reference.
- The built-in `main.main_backup` and `main.cloud_snapshot_upload` instances
  are seeded with manual tick policies for the filesystem transition. Manual
  RunOnce/CLI use remains available. Slice 10 may reinstate reviewed intervals
  only after duration/timeout, local retention, remote capacity/retention, and
  strict fetch/restore evidence are accepted.
- Canonical Box and physical Storage remain protected from automatic uninstall
  deletion; explicitly approved runtime database removal is a separate plan.

## Related Docs

- [[Backup Restore And Drills]]
- [[Backup Sync Retention And Cloud]]
- [[Command Safety]]

## Complete live reference and preserved operational detail

The canonical guidance above is the current storage contract. The detailed material below preserves the complete live command, route, workflow, safety, interpretation, and troubleshooting coverage that predates the filesystem migration. Retired generated-export behavior is retained only when explicitly labelled as compatibility or historical context.

### Safety Classes

| Command | Safety Class | Notes |
|---|---|---|
| `loom backup status`, `loom backup list` | read-only | Main-backed backup operation status and history. |
| `loom backup coverage`, `loom backup contracts plan` | read-only | Backup coverage and contract planning. |
| `loom backup contracts list`, `status`, `inspect` | read-only | Inspect canonical protected-folder intent and projected lifecycle evidence. |
| `loom backup contracts preflight` | queued read-only | Asks the owner node for a bounded, no-follow scan and effective-policy evidence; it does not return file contents. |
| `loom backup contracts create --dry-run`, `enable --dry-run`, `disable --dry-run`, `delete --dry-run` | dry-run | Shows a planned contract or lifecycle change without writing. |
| `loom backup contracts create`, `enable` | operator-mutating | Writes canonical desired state through main and queues owner-node reconciliation. |
| `loom backup contracts recheck`, `retry-activation` | queued operator control | Queues a fresh owner-node preflight or reconciliation attempt. |
| `loom backup contracts disable` | operator-mutating | Keeps canonical YAML but removes the managed root from desired active state. |
| `loom backup contracts delete --yes` | operator-mutating | Removes the standalone contract YAML file. It does not delete the target files. |
| `loom backup contract migrate-ignore-policy --dry-run` | dry-run | Plans migration from copied legacy exclusions to the managed profile. |
| `loom backup contract migrate-ignore-policy --apply --yes` | operator-mutating | Applies the reviewed migration; it does not rewrite retained payload bytes. |
| `loom backup create --dry-run --production` | dry-run | Shows capture/exclusion plan without running a backup. |
| `loom backup create --production` | operator-mutating | Runs main backup through the maintenance backend. |
| `loom backup verify <ref>` | read-only | Verifies a registered backup or local backup directory. |
| `loom backup restore-drill <ref> --dry-run` | dry-run | Requires a locally readable backup directory. |
| `loom backup restore-drill <ref>` | operator-mutating | Restores into a temporary drill database. |
| `loom cloud status --cached` | read-only | Main-backed cached cloud readiness. |
| `loom cloud doctor` | live read-only | Runs live diagnostics against configured cloud state. |
| `loom cloud snapshot status/list` | live read-only | Checks snapshot state, may use the remote lock. |
| `loom cloud snapshot push --dry-run` | dry-run, main-local | Verifies and plans upload from a locally readable main backup. |
| `loom cloud snapshot push` | operator-mutating, main-local | Uploads a verified backup to cloud. |
| `loom cloud snapshot fetch` | operator-mutating | Downloads a cloud snapshot into an empty staging directory. |
| `loom cloud snapshot verify` | live read-only | Verifies snapshot acceptance metadata or Borg archive state through the main service context by default. |
| `loom cloud snapshot restore-drill --dry-run` | dry-run | Fetches/verifies enough state to plan a drill. |
| `loom cloud snapshot retention plan/status` | live read-only | Plans cloud snapshot retention. |
| `loom cloud snapshot retention apply --plan <file> --confirm-digest <digest> --confirm` | operator-mutating | Applies an exact reviewed plan; Borg prunes authenticated in-class archives and reports compact separately. |
| `loom cloud snapshot backend status --cached` | read-only | Shows cached backend status. |
| `loom cloud snapshot backend init --confirm` | operator-mutating | Initializes configured snapshot repository. |
| `loom database status`, `loom database doctor` | read-only | Database status, warnings, migration state, and retention advice. |
| `loom database compact --dry-run` | dry-run | Default compaction mode; writes nothing. |
| `loom database compact --plan <file> --confirm` | operator-mutating | Preferred reviewed-plan apply path. |
| `loom database compact --dry-run=false --confirm` | operator-mutating | Compatibility apply path; still uses guarded limits. |
| `loom maintenance status`, `findings list`, `db status`, `backup status/list`, `object-store status` | read-only | Maintenance workers, findings, backup, DB, and object-store status. |
| `loom maintenance backup run --once` | operator-mutating | Runs main backup once. |
| `loom maintenance backup verify <ref>` | read-only | Verifies a registered backup. |
| `loom maintenance object-store scan --sample` | operator-mutating | Runs an integrity scan worker. |
| `loom maintenance retention dry-run` | dry-run | Database retention plan. |
| `loom maintenance retention apply --plan <file> --yes` | operator-mutating | Preferred maintenance retention apply path. |
| `loom maintenance retention apply --yes` | operator-mutating | Compatibility apply path; still requires explicit confirmation. |

### Backup Commands

Inspect:

```bash
loom backup status
loom backup list --limit 5
loom backup list --status succeeded --limit 20
```

Coverage:

```bash
loom backup coverage
loom backup contracts plan
```

Local coverage overrides are available for fixture or main-local inspection:

```bash
loom backup coverage --local --data-dir /var/lib/loom
loom backup coverage --manifest /var/lib/loom/backups/main/<backup>/manifest.json
```

Standalone backup contracts protect extra folders that are not naturally owned
by a project policy. The lifecycle commands go through the configured main
daemon. Main stores canonical YAML in its configured Box; the selected owner
node owns and scans the source path.

Start with a bounded owner-node preflight:

```bash
loom backup contracts preflight \
  --node main \
  --path /srv/field-data
```

The response is often queued. Retain the returned `preflight_id`, inspect or
resume it through the Portal when needed, and create only after it is completed
without blocking findings:

```bash
loom backup contracts create field-data \
  --node main \
  --path /srv/field-data \
  --display-name "Field Data" \
  --preflight <completed-preflight-id>
```

`preflight` reports the canonical path, filesystem/mount evidence, bounded
file/directory/byte counts, whether the scan truncated, managed-policy files,
protected/ignored counts, and findings. It does not follow symlinks or include
file contents.

Use the lifecycle projection for status:

```bash
loom backup contracts status
loom backup contracts status --node main --status attention
loom backup contracts inspect field-data
```

`waiting_for_node` means main accepted pending work but the owner node is
unavailable. `ready` means preflight completed. `activating` means current
apply/report/config evidence is incomplete. `active` means the current desired
configuration is applied and reported. Only `protected` also has an accepted
backup after the current acknowledgement. `attention` and `disabled` require
the reason or operator intent to be read explicitly.

The lifecycle controls are:

```bash
loom backup contracts recheck field-data
loom backup contracts retry-activation field-data --reason "owner node restored"
loom backup contracts enable field-data --dry-run
loom backup contracts enable field-data
loom backup contracts disable field-data --dry-run
loom backup contracts disable field-data
loom backup contracts delete field-data --dry-run
loom backup contracts delete field-data --yes
```

The canonical contract YAML lives in main's configured hidden Box metadata:

```text
<box-root>/.loom/contracts/backup/<contract-key>.yaml
```

The Box contract points to that directory through:

```yaml
policies:
  backup_contracts: .loom/contracts/backup
```

`disable` keeps the YAML file and marks the contract inactive. `delete --yes`
removes the YAML contract and reconciles away its managed watched root. Neither
action deletes source files or retained backup data. For project-owned folders,
prefer the project `.loom/contracts/backup.yaml` workflow created by project
scaffolding.

`recheck` is bound to the existing contract's managed safe root and runtime
worker, so its own active folder can be checked again while every cross-contract
or parent/child overlap remains blocked. A queued delete remains visible as
waiting/activating until the owner node acknowledges the tombstone. It then
leaves the normal contract list; an unintentional missing YAML file instead
remains an Attention item.

HTTP `202 Accepted` and CLI wording such as queued mean durable desired work was
accepted by main. They do not mean the owner node applied it, a watched-root
report matches the current configuration, or backup custody exists.

Continuous backup uses the `managed` policy: mandatory LOOM state, known
reconstructible dependency/cache trees, and `.loomignore` rules are omitted.
Inspect a root before accepting ignored content as intentionally unprotected:

```bash
loom ignore inspect <root> --operation backup
loom ignore explain <path> --root <root> --operation backup
```

`.gitignore` is not imported. `.git`, `.env`, `.secrets`, and `.data` are
eligible unless `.loomignore` excludes them; Git history can include local-only
refs. An ignored path is absent from that backup and cannot be restored from
it. This differs from a protected file whose content was not indexed for
search.

Migrate contracts that contain the old expanded default exclusion list only
after reviewing the dry-run:

```bash
loom backup contract migrate-ignore-policy --dry-run
loom backup contract migrate-ignore-policy --apply --yes
```

Apply requires both flags and is idempotent. The migration updates contract
policy; it does not add protection for paths that an effective `.loomignore`
still excludes.

Create plan:

```bash
loom backup create --dry-run --production
loom backup create --dry-run --allow-non-production
```

Real create:

```bash
loom backup create --production --reason "operator reviewed"
```

Verify:

```bash
loom backup verify <backup-ref-or-path>
```

Restore drill:

```bash
loom backup restore-drill <backup-ref-or-path> \
  --dry-run \
  --target-database loom_restore_drill_manual
```

The drill process must be able to read the backup directory. Verification by
registered ref can work from Mac; restore drilling a main-local backup ref from
Mac can fail with `backup.restore_drill_requires_local_directory`.

### Cloud Commands

Status:

```bash
loom cloud status --cached
loom cloud status --live
loom cloud doctor
```

Use `--local` only when you intentionally want the CLI process to read its own
local cloud config. Without `--local`, `cloud status` and `cloud doctor` use
the main service context.

Snapshot inventory:

```bash
loom cloud snapshot status
loom cloud snapshot list
loom cloud snapshot verify latest --profile metadata
loom cloud snapshot retention status
loom cloud snapshot retention plan --json > /secure/review/loom-retention-plan.json
```

At verification time, snapshot list returned `status=empty`, backend `borg`,
and zero snapshots. Retention status returned `empty`, with no removal
candidates.

`loom cloud snapshot verify <ref>` delegates to `loomd` by default so Mac and
other operator shells do not need direct access to `/etc/loom/cloud`,
`/var/lib/loom/cloud`, Borg passphrases, or cloud SSH credentials. Use
`--local` only for explicit node-local diagnostics. Local verify requires the
CLI process to read the cloud config, remote lock, and Borg credentials; missing
local config returns `cloud.snapshot_verify_local_config_missing`, while local
credential or lock permission failures return
`cloud.snapshot_verify_service_context_required`.

The verify command keeps deep assurance in this same command family. Choose
exactly one profile with `--profile`:

```bash
# Complete metadata verification for one resolved archive (the default).
loom cloud snapshot verify latest --profile metadata

# Time-bounded repository progress. This accepts no archive argument.
loom cloud snapshot verify \
  --profile rolling_repository \
  --max-duration 30m

# Complete data verification for one exact canonical Borg archive name.
loom cloud snapshot verify main-20260831T031500Z-example --profile archive_data
```

`rolling_repository` requires a positive whole-second duration and runs only
the Borg repository check with that bound. It does not claim complete
archive-data coverage. `archive_data` rejects `latest`, requires the exact Borg
archive name, and does not accept `--max-duration`. Human and JSON output name
the selected `profile` and its `coverage`; rolling output also repeats the
bounded duration. Running one of these commands does not schedule another run
or change worker policy.

Backend:

```bash
loom cloud snapshot backend status --cached
loom cloud snapshot backend status --live
loom cloud snapshot backend init --confirm
```

`cloud snapshot backend doctor` is a local CLI diagnostics command: it reads the
cloud config available to the CLI process. On Mac, it can report local cloud
disabled even when main-backed cached cloud status is healthy.

Direct snapshot push:

```bash
loom cloud snapshot push --backup latest --dry-run
```

Direct snapshot push is local to the CLI process. It reads the backup from
`--backup-root`, defaulting to `<data-dir>/backups/main`, and it reads the cloud
config available to that process. Run it on the main node or in a process that
can read the main backup root and cloud config.

If the requested backup cannot be resolved locally, the command fails with:

```text
cloud.snapshot_push_backup_missing
```

That means the command is running in the wrong local context or needs an
explicit `--backup` or `--backup-root` pointing at a readable verified backup.

Fetch and restore drill:

```bash
loom cloud snapshot fetch latest --to /var/lib/loom/cloud/restore-drills/manual/backup
loom cloud snapshot restore-drill latest --dry-run --target-database loom_restore_drill_cloud
```

These are operator workflows. The fetch target must be an empty local staging
directory.

### Database Commands

```bash
loom database status
loom database doctor
loom database compact --dry-run
```

At verification time, database status was `ok`, migrations were `ok`, and
doctor had no warnings. `database compact --dry-run` returned `dry_run=true`,
`mutates_database=false`, six retention plan groups, and explanatory warnings
that no rows were deleted.

Export the reviewed plan when preparing an apply:

```bash
loom database compact --dry-run --out /tmp/loom-db-compaction-plan.json
```

Apply only after reviewing that plan and confirming a fresh backup exists:

```bash
loom database compact --plan /tmp/loom-db-compaction-plan.json --confirm
```

The equivalent maintenance commands are:

```bash
loom maintenance retention dry-run --out /tmp/loom-db-compaction-plan.json
loom maintenance retention apply --plan /tmp/loom-db-compaction-plan.json --yes
```

The compatibility apply path remains available:

```bash
loom database compact --dry-run=false --confirm
```

Compaction deletes only old routine success events and old settled worker
leases, bounded by cutoff, table allow-list, batch size, and total row limits.
It preserves worker runs, worker controls, maintenance operations, findings,
audit/security/failure/manual events, and recent rows.

Logical deletion does not guarantee immediate PostgreSQL file-size shrink.
Autovacuum can reuse freed space; `VACUUM FULL`, `CLUSTER`, and table rewrites
are intentionally outside this workflow.

### Maintenance Commands

Overview:

```bash
loom maintenance status
loom maintenance findings list --limit 20
```

Database and backup:

```bash
loom maintenance db status
loom maintenance backup status
loom maintenance backup list --limit 5
loom maintenance backup verify <backup-ref>
```

Run a main backup once:

```bash
loom maintenance backup run --once --reason "operator reviewed"
```

Object-store:

```bash
loom maintenance object-store status
loom maintenance object-store scan --sample --reason "operator reviewed"
loom maintenance object-store scan --blob <blob-ref> --reason "operator reviewed"
```

Exactly one of `--sample` or `--blob` is required for a scan.

Retention:

```bash
loom maintenance retention dry-run
loom maintenance retention apply --yes
```

`database compact` and `maintenance retention` use the same guarded compaction
backend.

### Related Docs

- [[Backups Cloud And Restore]]
- [[Backup Restore And Drills]]
- [[Cloud Storage]]
- [[Database Maintenance]]
- [[Environment Variables]]
