---
title: "Backup Sync Retention And Cloud"
description: "How LOOM moves protected data from source through canonical custody, retention, operational backup, and cloud recovery copies."
audience:
  - user
  - operator
  - developer
tags:
  - loom
  - concepts
  - backup
  - cloud
  - storage
status: verified
verified_at: "2026-08-31"
source_scope:
  - "internal/sync"
  - "internal/watchedroots"
  - "internal/filetransfer"
  - "internal/storagearchive"
  - "internal/backupcoverage"
  - "internal/maintenance"
  - "internal/cloudstorage"
  - "internal/backupstrategy/archive_manifest_v2.go"
  - "internal/workers/tick_policy.go"
  - "internal/workers/runtimes/cloud_snapshot.go"
  - "tests/smoke/v2_cloud_backup_runtime_optimization_local.sh"
related:
  - "[[Backups Cloud And Restore]]"
  - "[[Backup Restore And Drills]]"
  - "[[Storage Retention And Safe Delete]]"
---
# Backup Sync Retention And Cloud

LOOM uses several kinds of protection. They solve different failure modes and
must not be collapsed into one generic “backup folder.”

## Custody Flow

```text
editable source
  -> watched-root batch or explicit transfer
  -> manifest-committed canonical custody
  -> catalog and retention evidence
  -> verified operational main backup
  -> optional verified cloud snapshot
```

Watched-root backup custody lives below the configured
`Storage/backups/<node>/<root>/<batch>/`. The batch manifest is the commit
marker. Snapshot code skips in-progress or hidden staging directories and
copies only immutable committed batches.

Archive custody lives below `Storage/archive/<archive-key>/`. User keys cannot
enter the hidden dot-prefixed namespace. An archive publishes verified objects
and its manifest atomically, then commits all catalog rows in one transaction.
Retry can recover from immutable published custody even when the original
source becomes unreadable.

## Direct And Chunked Transfers

Small/direct private backup and large/chunked file-transfer paths carry the
same batch/item identity: node, protected root, local batch, relative path,
content address, transport kind, size, and checksum. Evidence from another
batch or root is rejected before catalog commit.

A chunked file is assembled and checked against the declared whole-file
checksum before it becomes visible in canonical custody. A mismatch removes
only the new temporary assembly and preserves an existing accepted destination.

## Retention And Safe Delete

Retention is a deliberate protection copy under the configured internal
retention root. It is not an editable source and not the same thing as a main
backup. Safe-delete combines source identity, catalog state, retained payload,
checksum evidence, cloud evidence when required, and filesystem-fidelity
findings.

## Operational Main Backup

`loom backup create --production` captures the database and declared durable
roots. Canonical physical roots include Box when policy says it is covered,
Imports custody, user backups, archive custody, internal object/retention
protection, and required manifests. Generated storage exports are never a
backup source.

Imports uses a backup-owned content-addressed store: missing immutable regular
files are copied once, and each dated backup links those objects while copying
directory and symlink metadata independently. Before any object write, LOOM
validates the complete source inventory and checks aggregate missing bytes plus
a fixed safety reserve. The completed snapshot must match that exact preflight
inventory before evidence is published. The reserve is a bounded byte-space
gate; it is not proof of inode capacity, and recurring schedules still require
a reviewed generation-retention policy.

The Imports provenance policy is explicit and independent of filesystem
cutover. New custody can require Lane commit manifests. Migrated production
custody with markerless historical batches uses complete-physical-custody
mode, which covers every file, directory, and symlink without inventing commit
history. Switching layouts alone never upgrades that provenance claim.

The Notes projection is excluded by default because it is generated. An
explicit compatibility-copy policy may include it, but the manifest must then
declare it as included rather than simultaneously excluded.

Backup verification parses current custody manifests, confines all referenced
paths to the snapshot, and checks payload/object existence plus size/checksum
evidence. Explicit older layouts remain supported, but malformed current
manifests do not fall through as legacy.

## Cloud Snapshots

Cloud snapshots are verified recovery copies of verified local main backups.
Both the Borg and legacy-tree backends receive canonical roots from runtime
configuration. Upload refuses incomplete backup coverage before remote
mutation. Legacy-tree upload/check/fetch explicitly preserve links and Unix
metadata; fetch authenticates portable Imports evidence and rehydrates exact
recorded metadata before strict verification where SFTP timestamp precision is
lower. Borg extraction must pass the same strict evidence directly.

Cloud status, verification, fetch, retention, and restore drills are separate
operations. Fetching a snapshot does not restore it into a live node.

Current direct Borg archives are explicitly classified as `user_data`,
`acceptance`, or `milestone`. Routine archive writers may create, list, verify,
and fetch through an explicit command allowlist; global-option prefixes cannot
shift command parsing into `delete`, `prune`, or `compact` authority.
A separately configured retention authority plans only authenticated
`user_data` archives for the selected node. The fixed policy keeps the newest
archive in 14 daily, 8 ISO-weekly, and 6 monthly buckets; acceptance,
milestone, foreign-node, pending, legacy-unclassified, and unauthenticated
archives are never prune candidates.

New routine direct archives use the authenticated
`loom.direct_archive.manifest.v2` envelope. The envelope binds the repository,
node, archive name and class, configured roots and exclusions, and the exact
operational and provenance recovery packages. It deliberately does not repeat
a complete per-file user-data hash inventory. Borg performs one namespace walk
with its stable `ctime,size,inode` files cache and reads changed content; LOOM
then performs bounded package-stability, exact archive-metadata, and embedded-
envelope checks before the pending archive is renamed. The routine cost is
therefore proportional to filesystem entries plus changed bytes, not all
unchanged payload bytes.

That routine success is intentionally narrower than a deep scan. It does not
claim that every older data chunk was reread. LOOM exposes three separate
assurance profiles: complete metadata for one archive, time-bounded rolling
repository progress, and complete data verification for one exact archive.
Strict fetch/restore is a fourth claim. Prune, compact, delete, payload export,
archive-wide `{sha256}` listing, and a second LOOM source-hash pass are absent
from the v2 routine writer.

A Borg retention apply is bound to the repository id, complete archive
inventory digest, exact policy and archive glob, plan digest, a fresh
repository-wide check, and an exact operator confirmation. Prune and compact
are separate outcomes. A successful prune with skipped or failed compact is
`partial`, and LOOM does not claim reclaimed bytes merely because Borg
reported that both commands exited successfully. Before the repository check
or prune, every live archive matched by the Borg prune glob must also be an
exact-name, authenticated, same-node `user_data` member of the canonical plan;
otherwise apply refuses without mutation.

Direct-archive fetch authenticates the remote manifest before extraction. V1
retains its complete entry, metadata, and hash comparison. V2 first rejects
undeclared paths and escaping links, then relies on Borg extraction to
authenticate user payload chunks while LOOM re-verifies root confinement and
the exact bounded recovery packages. Strict recovery separately proves the
operational PostgreSQL package and the isolated provenance package.
The operational dump is opened without following links, bound to its verified
inode/type/mode/size/hash, and copied to an unlinked private input while the
source descriptor remains open. Replacement or content drift before or during
`pg_restore` fails the drill and preserves truthful cleanup reporting.
Only those exact identities can produce a read-only migration eligibility
report. That report never grants cleanup authority; local cleanup remains a
separate production migration decision.

## Daily Local Scheduling

The existing worker scheduler supports timezone-aware `daily_local` policies;
there is no second timer or backup daemon. The accepted pair is main backup at
`03:00` and cloud archive at `03:15 Europe/Amsterdam`. Each next occurrence is
stored as a durable UTC timestamp but recalculated from local wall-clock time,
so spring gaps are skipped and the first fall-back occurrence owns that local
calendar day.

The scheduled cloud worker accepts only a recovery package created from 03:00
through that run's start on the same Amsterdam calendar day. A missing, stale,
or overrun package, a stale policy fingerprint, or an overlapping run/remote
lock fails before archive mutation. These policies are a supported software
surface, not an active production schedule: activation remains a separate
reviewed operator checkpoint.

## What Each Layer Protects

| Layer | Protects against | Not a substitute for |
|---|---|---|
| Canonical custody | loss of an editable source or transfer endpoint | operational backup |
| Retention/object copy | accidental deletion and content loss | original metadata fidelity |
| Main backup | main-node database/disk loss | tested off-site recovery |
| Cloud snapshot | site or main-backup loss | local verified restore drill |

## Healthy Evidence

A trustworthy recovery chain has:

- committed custody manifests;
- available catalog entries and matching physical references;
- coherent backup coverage with no critical gap;
- a verified backup manifest;
- a successful non-destructive restore drill;
- a verified cloud snapshot when off-site recovery is required.

## Related Docs

- [[Backups Cloud And Restore]]
- [[Backup Restore And Drills]]
- [[Storage Retention And Safe Delete]]

## Complete live reference and preserved operational detail

The canonical guidance above is the current storage contract. The detailed material below preserves the complete live command, route, workflow, safety, interpretation, and troubleshooting coverage that predates the filesystem migration. Retired generated-export behavior is retained only when explicitly labelled as compatibility or historical context.

### The Mental Model

LOOM separates live state, retained local custody, and cloud disaster recovery.

Live source state is where users work: Box folders, project folders, main
Documents, notes roots, and runtime databases. Retained state is LOOM-managed
evidence that accepted data still exists somewhere under LOOM custody. Cloud
snapshots are disaster-recovery copies of verified main backups.

Those layers are connected, but they are not interchangeable:

- sync moves or reconciles live data between local node surfaces and main;
- backup captures main-node LOOM state into local backup artifacts;
- cloud snapshot uploads a verified local main backup to the configured remote;
- retention reduces old routine history or old cloud snapshots only after a
  plan;
- restore drills prove that a backup or cloud snapshot can be restored into a
  temporary database.

### Why Backup Is Not Just Sync

Sync and indexing make data visible and searchable. Backup proves recoverable
custody.

For example, a note can be indexed and searchable before it is covered by a
verified backup. That is useful for daily work, but it is not enough to call the
data recoverable. The backup coverage command exists to make this boundary
explicit:

```bash
loom backup coverage
loom backup contracts plan
```

At verification time, coverage had 22 satisfied entries and zero critical
missing paths.

Indexing and sync are not backup contracts. Indexing creates searchable
knowledge objects. Sync/watch roots describe how LOOM observes or moves live
state. Backup coverage asks whether a source is protected by a backup policy
and represented in backup manifests.

Ignore policy is another separate boundary. A backed-up but unindexed file is
still recoverable; an ignored file was omitted from that backup operation and
is not protected by it. Search state must never be used as proof of backup, and
an ignored count must never be presented as successful protection.

### Backup Contracts

LOOM has two backup policy paths:

- project backup policy, stored with the project and preferred for
  project-owned folders;
- standalone backup contracts, stored under main's configured hidden Box
  metadata for extra folders that are not naturally project-owned.

Standalone contracts are canonical desired state stored in main's configured
hidden Box:

```text
<box-root>/.loom/contracts/backup/<contract-key>.yaml
```

The Box contract points to that hidden directory with
`policies.backup_contracts`, normally `.loom/contracts/backup`. The selected
owner node can be different from main: it owns and scans the source path, then
applies a revisioned backup-only managed watched root. Disabled contracts remain
inspectable but do not request active backup coverage. Deleting a contract
removes desired YAML and the managed watched root, not source files or retained
backup data.

The normal user-facing lifecycle is the Portal's **Protect Folder** workflow:
select an owner node and path, wait for bounded preflight, review effective
policy and findings, confirm the contract, and then follow the lifecycle to
**Protected**. The CLI exposes the same advanced workflow:

```bash
loom backup contracts preflight --node <node> --path <folder>
loom backup contracts create <key> --node <node> --path <folder> \
  --preflight <completed-preflight-id>
loom backup contracts status
loom backup contracts inspect <key>
loom backup contracts disable <key>
loom backup contracts delete <key> --yes
```

From any operator shell, these commands go through main. They do not SSH to an
owner node or write operator-local Box metadata.

Lifecycle projection deliberately separates intent from custody:

- **Waiting For Node** is durable pending work with an unavailable owner;
- **Ready** is completed preflight before desired registration;
- **Activating** is incomplete current application/report/config evidence;
- **Active** is current desired state applied and reported;
- **Protected** adds an accepted backup after the current acknowledgement;
- **Attention** is blocking or inconsistent evidence;
- **Disabled** is retained canonical intent without an active managed root.

This is a backup-only contract. It does not opt the folder into bidirectional
sync, search indexing, notes projection, file sharing, or a project. Those
remain independent explicit policies. Conversely, a folder being synced or
indexed is not evidence that Protect Folder reached **Protected**.

Continuous backup resolves the `managed` profile. It always excludes mandatory
LOOM runtime state, excludes known reconstructible dependency/cache trees, and
then applies `.loomignore` rules discovered from the watched-root boundary
through nested directories. `.git`, `.env`, `.secrets`, and `.data` are not
universal exclusions; retaining `.git` matters because local refs and history
may exist nowhere else. LOOM does not import `.gitignore`, because version
control and recoverability have different purposes.

Before relying on a contract, inspect its effective policy and the reported
protected/ignored counts:

```bash
loom ignore inspect <root> --operation backup
loom ignore explain <path> --root <root> --operation backup
```

Existing contracts that copied the legacy expanded default list require the
guarded migration. Review its dry-run before applying:

```bash
loom backup contract migrate-ignore-policy --dry-run
loom backup contract migrate-ignore-policy --apply --yes
```

The migration changes contract policy, not retained payload bytes. Ignored
paths will not be recoverable from future backups unless another protection
path retains them, so policy review belongs before source cleanup or migration
apply.

### Local Main Backups

Main backups are maintenance operations. They capture multiple classes of
state:

- PostgreSQL dump;
- object store snapshot;
- private backup snapshot;
- main Documents snapshot;
- storage retention snapshot;
- storage archive snapshot;
- `loom-notes` projection snapshot;
- install and service environment evidence with secrets redacted;
- a backup manifest and health report.

Manual creation is guarded:

```bash
loom backup create --dry-run --production
loom backup create --production --reason "operator reviewed"
```

The dry-run describes planned capture groups and excluded secret classes. The
real create path is an operator action.

### Cloud Snapshots

Cloud snapshots are copies of verified local main backups. Borg is the packed,
deduplicated target backend. Production has already committed a canonical
direct Borg archive through the reviewed backup path. The remaining production
boundary is separate activation of the accepted daily-local policies followed
by one measured package/archive observation; documentation and software
integration do not activate that schedule.

For Borg, LOOM stores backup snapshots as deduplicated archives in one
configured repository. The public cloud status layer still uses rclone for
remote readiness, roots, and lock state. The default remote root is `loom`, so
LOOM should only manage configured prefixes under that root. Folders outside
that LOOM remote root are not canonical LOOM storage.

The main worker is the supported upload path. Until the separately reviewed
production schedule activation, operators may run it explicitly without
changing its tick policy:

```bash
loom worker run main.cloud_snapshot_upload --once
```

The worker dry-runs the latest local backup, checks whether an equivalent cloud
snapshot already exists, verifies existing snapshots when found, and otherwise
uploads. It skips when cloud is disabled, a remote lock is busy, or no local
backup manifest exists.

### Remote Locks And Cooldowns

Hetzner Storage Box has tight connection limits. LOOM therefore serializes
cloud operations through remote lock and cooldown state. A `lock_busy` status
means another LOOM cloud operation currently owns the lock. It is not by itself
evidence that the remote is corrupt.

Use cached status for ordinary dashboards:

```bash
loom cloud status --cached
loom cloud snapshot backend status --cached
```

Use live status or doctor when the operator intentionally wants a remote probe.

### Retention

Retention has two different meanings:

- database retention compacts routine operational history while preserving
  audit-critical evidence;
- cloud snapshot retention keeps the latest valid snapshots and removes or
  trashes old valid snapshots.

Database compaction defaults to dry-run:

```bash
loom database compact --dry-run
loom maintenance retention dry-run
```

The reviewed apply path exports the plan first:

```bash
loom database compact --dry-run --out /tmp/loom-db-compaction-plan.json
loom database compact --plan /tmp/loom-db-compaction-plan.json --confirm
```

The first destructive policy is deliberately narrow. It deletes only old
routine success events and old settled worker leases, in bounded batches.
Worker runs, worker controls, maintenance operations, findings,
audit/security/failure/manual events, and recent rows are retained. Logical row
deletion may not immediately reduce PostgreSQL file size; freed space can be
reused by PostgreSQL, while table rewrites are outside this workflow.

Cloud retention also has a plan step:

```bash
loom cloud snapshot retention plan --keep-latest 14
```

Apply commands require explicit confirmation.

### Restore Drills

A restore drill is the proof step. It should not target the active LOOM
database, and drill database names must start with `loom_restore_drill_`.

Registered backup verification can be performed from Mac through the main
daemon. A backup restore drill requires the process running the drill to read
the backup directory. In practice, run local backup restore drills on main or
against a fetched cloud snapshot staging directory.

### Related Docs

- [[Backups Cloud And Restore]]
- [[Backup Cloud And Maintenance CLI Reference]]
- [[Backup Restore And Drills]]
- [[Cloud Storage]]
- [[Database Maintenance]]
