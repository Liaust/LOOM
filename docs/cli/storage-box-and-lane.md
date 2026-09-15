---
title: "Storage Box And Lane CLI Reference"
description: "Current Box, Lane, canonical filesystem, catalog, retention, fidelity, cleanup, archive, and mount commands."
audience:
  - user
  - operator
  - developer
tags:
  - loom
  - cli
  - storage
status: verified
verified_at: "2026-08-30"
source_scope:
  - "go run ./cmd/loom box --help"
  - "go run ./cmd/loom lane --help"
  - "go run ./cmd/loom storage --help"
  - "internal/loomcli/storage_filesystem.go"
related:
  - "[[Command Safety]]"
  - "[[Files Storage And Lane]]"
  - "[[Canonical Paths]]"
---
# Storage Box And Lane CLI Reference

## Safety Classes

- Status, list, inspect, verify, doctor, dry-run, and plan operations are
  read-only.
- Lane send, repair, acceptance, archive, retention backfill, cleanup apply,
  and migration apply mutate state.
- Filesystem migration commands never move bytes. The operator moves reviewed
  paths outside LOOM between plan and apply.
- Storage export compatibility commands are read-only and deprecated.
- Retired Main intake cleanup is saved-plan-bound, non-recursive, and separate
  from ordinary setup/repair. Apply requires the exact cleanup digest and
  explicit confirmation.

## Box

```bash
loom box status
loom box path
loom box init --dry-run --path <box> --profile workspace
loom box repair --dry-run
loom box watch-plan
loom box watch-status
```

`watch-apply` changes desired state on main. Box init/repair creates durable
contracts and source folders but not visible runtime state.

## Lane

```bash
loom lane status
loom lane send --dry-run
loom lane repair <batch-id>
loom lane publish <batch-id>       # compatibility alias for repair
loom lane acknowledge-transfer <batch-id>
loom lane archive-transfer <batch-id>
```

Repair resumes promotion, catalog, or cleanup from persisted evidence without
re-upload. Use explicit cross-device authorization only when the initial batch
requires it; authorization is not inferred for legacy records.

Lane commands are workspace actions. Main receives accepted batches in its
configured Storage Imports root and does not expose a Main Lane action.

## Retired Main Intake Cleanup

```bash
loom setup cleanup plan --plan <saved-main-setup-plan.json>
loom setup cleanup apply --plan <saved-main-setup-plan.json> \
  --confirm-digest <cleanup-digest> --dry-run
```

The setup plan inventories only built-in exact obsolete Main names. It does not
walk recursively. Apply rechecks the saved setup-plan hash, cleanup digest,
parent identity, object identity, emptiness, and known-link target. Without
`--dry-run`, `--yes` is also required. Non-empty, unknown, unexpected-link, or
changed targets are skipped and reported. Ordinary setup and repair never run
this cleanup implicitly.

## Canonical Filesystem

```bash
loom storage filesystem status
loom storage filesystem migrate --dry-run \
  --root documents=/old/documents:/srv/loom/box/Documents \
  --manifest ./filesystem-migration
loom storage filesystem migrate --apply \
  --manifest ./filesystem-migration --yes
loom storage filesystem verify
```

The dry-run writes a deterministic bounded manifest set when necessary and
prints a concise summary. Review metadata and the external byte move are
operator responsibilities. Apply preflights the complete reviewed set and
rebinds all catalog paths in one PostgreSQL transaction. Verification returns
nonzero in both human and JSON modes when findings are not OK.

Root mappings must have unique names and non-overlapping old/new roots. Nested,
duplicate, symlink-escaping, changed, or divergent evidence fails closed.

## Catalog And Physical Status

```bash
loom storage list --limit 50
loom storage inspect <path-or-id>
loom storage verify <path-or-id>
loom storage tree --limit 200
loom storage transfers
loom storage failures
loom storage doctor
```

`tree` is a bounded catalog presentation, not a materialized filesystem tree.
Doctor reports typed physical-root, catalog, retention, archive, and mount
health without payload-tree traversal.

## Documents, Retention, Fidelity, And Delete

```bash
loom storage main-documents status
loom storage main-documents reconcile --dry-run
loom storage retention status
loom storage fidelity report --limit 50
loom storage fidelity backfill --dry-run --limit 50
loom storage safe-delete check <path-or-id>
loom storage inventory
loom storage cleanup plan --out <reviewed-plan.json>
loom storage cleanup apply --plan <reviewed-plan.json> --yes
```

Mutation forms require their explicit confirmations. Main Documents
reconciliation forces legacy/canonical comparison before catalog mutation when
distinct roots are configured; ordinary importer dry-runs do not repeatedly
walk the deprecated root.

## Archive And Restore

```bash
loom storage archive <path-or-id> --dry-run \
  --archive-key <key> --kind manual_archive --to <archive-path>
loom storage fetch <path-or-id> --to <destination>
loom storage restore <path-or-id> --to <destination>
```

Archive keys cannot be hidden/dot-prefixed. Archive publication is
manifest-committed and catalog-atomic.

## Mounts

```bash
loom storage mount-status --protocol smb --doctor
loom storage mount-policy status
```

The Mac helper owns actual mount/unmount identity checks. Storage is read-only;
main Box is a distinct read/write share.

## Deprecated Export Diagnostic

```bash
loom storage export status
```

No export materialize, refresh, rebuild, repair, link, or delete operation is
available. Historical API mutation routes return a retired error.

## Related Docs

- [[Files Storage And Lane]]
- [[Canonical Paths]]
- [[Command Safety]]

## Complete live reference and preserved operational detail

The canonical guidance above is the current storage contract. The detailed material below preserves the complete live command, route, workflow, safety, interpretation, and troubleshooting coverage that predates the filesystem migration. Retired generated-export behavior is retained only when explicitly labelled as compatibility or historical context.

### Safety Classes

| Command | Safety Class | Notes |
|---|---|---|
| `loom box status`, `loom box path` | read-only | Inspect local Box state or print the resolved Box path. |
| `loom box init --dry-run`, `loom box repair --dry-run` | dry-run | Plan Box scaffold writes. |
| `loom box init`, `loom box repair` | local-mutating | Create or repair Box folders and generated contracts. |
| `loom box watch-plan`, `loom box watch-status` | read-only | Inspect desired and reported watched-root state. |
| `loom box watch-apply --dry-run` | dry-run | Computes backend watch registration changes. |
| `loom box watch-apply` | operator-mutating | Records Box watched-root desired state on main. |
| `loom ignore inspect`, `loom ignore explain` | local read-only | Resolve effective backup or Lane policy without changing files. |
| `loom setup cleanup plan --plan <saved-plan>` | read-only | Shows the exact non-recursive obsolete Main intake inventory embedded in a setup plan. |
| `loom setup cleanup apply --plan <saved-plan> --confirm-digest <digest> --dry-run` | dry-run | Revalidates identities and reports what an apply would remove or skip. |
| `loom setup cleanup apply --plan <saved-plan> --confirm-digest <digest> --yes` | operator-mutating | Removes only unchanged exact empty directories or exact known obsolete links. |
| `loom lane status` | read-only | Shows local pending Lane items, policy identity, protected/ignored counts, recovery-storage bytes, and preflight. |
| `loom lane send --dry-run` | dry-run | Shows the faithful plan, automatic transport choice, and temporary-space estimate. |
| `loom lane send` | operator-mutating | Sends pending Lane files to main in faithful mode using automatic file-tree or bundle transport. |
| `loom lane send --bundle`, `--no-bundle` | operator-mutating | Forces bundle or file-tree transport; review the force warning and space estimate. |
| `loom lane send --source-only` | operator-mutating | Also excludes managed reconstructible dependencies. |
| `loom lane send --exact` | operator-mutating | Bypasses `.loomignore`; requires explicit policy review. |
| `loom lane publish/repair --dry-run` | dry-run | Plans imports promotion, catalog repair, or confined transport-staging cleanup for an accepted batch; payload bytes are not re-uploaded. |
| `loom lane acknowledge-*`, `loom lane archive-transfer` | operator-mutating | Records attention handling without deleting history. |
| `loom storage tree`, `loom storage list`, `loom storage inspect` | read-only | Inspect catalog entries and stable logical paths. `tree` is a compatibility surface and may report retirement rather than build a filesystem tree. |
| `loom storage status`, `loom storage verify` | read-only | Explain a path's accepted/pending/failed/safe-delete state. |
| `loom storage safe-delete check` | read-only | Check explicit safe-delete state. |
| `loom storage retention status` | read-only | Inspect retention and tombstone counts. |
| `loom storage fidelity report` | read-only | Report known safe-view fidelity differences. |
| `loom storage fidelity backfill --dry-run` | dry-run | Plan fidelity observations without writing findings. |
| `loom storage export status` | read-only | Inspect the deprecated export compatibility status. |
| `loom storage main-documents status` | read-only | Inspect main `Documents` import scanner status. |
| `loom storage main-documents reconcile --dry-run` | dry-run | Plan main `Documents` tombstones. |
| `loom storage main-documents retention backfill --dry-run` | dry-run | Plan retained-byte backfill. |
| `loom storage archive --dry-run` | dry-run | Plan archive output. |
| `loom storage archive` | operator-mutating | Writes archive catalog/output state. |
| `loom storage benchmark` | local/remote test mutation | Writes bounded `.loom-acceptance` files to explicit local, writable Main Box SMB, and optional rsync targets; avoid the read-only Storage share. |
| `loom storage cloud-offload plan` | read-only/dry-run | Resolve and review an archive-oriented cloud offload without copying bytes. |
| `loom storage cloud-offload apply --confirm` | remote-mutating | Upload, verify, and record one explicitly confirmed offload. |
| `loom storage cloud-status` | read-only | Inspect a recorded cloud-offload manifest. |
| `loom storage cloud-fetch` | local-mutating | Fetch verified offload content to an explicit missing file or empty directory. |
| `loom storage fetch`, `loom storage restore apply` | operator-mutating | Writes a local destination path. |
| `loom storage cleanup plan` | read-only/dry-run | Builds cleanup plan. |
| `loom storage cleanup apply --dry-run` | dry-run | Shows quarantine or deletion changes. |
| `loom storage cleanup apply --yes` | operator-mutating | Quarantines by default; `--delete-now` is stronger and explicit. |
| `loom storage mount-status`, `loom storage mount-policy status --local` | read-only | Inspect local mount readiness and desired state. |
| `loom storage mount-policy enable/disable/repair-once` | operator-mutating | Changes or repairs local mount policy. |

### Box Commands

Inspect:

```bash
loom box status
loom --json box status
loom box path
```

Use `box status` for contract-backed truth. Use `box path` when a script needs
only the resolved root path.

Initialize or repair:

```bash
loom box init --dry-run
loom box repair --dry-run
loom box init
loom box repair
```

Watch policy:

```bash
loom box watch-plan
loom box watch-status
loom box watch-apply --dry-run
loom box watch-apply --emit-shell
loom box watch-apply --idempotency-key <key>
```

The watch plan currently compiles Box Documents and Box Notes. Projects own
their own policies, and workspace Lane is a custody path rather than a watched
root. Main has no Lane watched root.

### Effective Ignore Commands

Inspect a whole root with the continuous-backup (`managed`) or normal-Lane
(`faithful`) profile:

```bash
loom ignore inspect <root> --operation backup
loom ignore inspect <root> --operation lane
```

Explain the ordered decisions for one path:

```bash
loom ignore explain <path> --root <root> --operation backup
loom ignore explain <path> --root <root> --operation lane
```

`inspect` reports policy version and fingerprint, discovered `.loomignore`
files, included/ignored file and byte counts, ignored counts by rule category,
and bounded ignored-path samples. `explain` shows the matching decision trace.

`.loomignore` can live at a Box root, project root, independently watched-root
boundary, or nested directory. It supports Gitignore-style comments, blank
lines, anchoring, directory rules, `*`, `?`, `**`, escaped leading `#`/`!`, and
`!` negation. Parent files are applied before child files and the last matching
rule wins. Mandatory LOOM runtime-state exclusions cannot be negated, and the
`.loomignore` file itself remains included.

LOOM does not import `.gitignore`. `.git`, `.env`, `.secrets`, `.data`, `dist`,
`build`, and `target` remain eligible unless `.loomignore` excludes them;
`.git` may contain local-only history and refs that must remain recoverable.

### Retired Intake Compatibility

Dropzone is not an active command family and is absent from help and
completion. Bounded historical readers remain decode-only compatibility code;
they do not create paths or permit mutations. Use the reviewed setup cleanup
commands above for exact obsolete Main names, never a recursive filesystem
command.

### Lane Commands

Read-only:

```bash
loom lane status
loom lane status --include-fidelity
```

Transfer:

```bash
loom lane send --dry-run
loom lane send --source-only --dry-run
loom lane send --exact --dry-run
loom lane send --bundle --dry-run
loom lane send --no-bundle --dry-run
loom lane send --resume
loom lane send --keep-local
```

The default is `faithful`: mandatory transfer state and `.loomignore` apply,
while dependency trees and Git history remain eligible. `--source-only` also
applies managed reconstructible defaults. `--exact` bypasses `.loomignore` but
still applies mandatory safety rules. `--source-only` and `--exact` are
mutually exclusive.

Transport is independent of those content profiles. The default request is
`auto`: LOOM selects `bundle_seed` above 10,000 regular files, or above 2,000
regular files when their average size is below 256 KiB; otherwise it selects
`file_tree`. `--bundle` and `--no-bundle` force those modes and are mutually
exclusive. Status exposes the automatic recommendation and reason. Send
dry-run and results expose requested, recommended, and selected mode in both
human and JSON output, with average file size, estimated archive and temporary
bytes, and any force warning.

After reviewing a dry-run, pin its fingerprint when sending so policy drift
forces a new review:

```bash
loom lane send --expected-policy-fingerprint <sha256-fingerprint>
```

In `bundle_seed` mode, LOOM creates one uncompressed PAX tar and one manifest
under hidden Lane state. The bundle is the local retry/safety artifact; LOOM
does not also create the file-tree safety copy. It verifies the archive and
source inventory before transfer, reports archive and manifest SHA-256 values,
then sends only those artifacts. Main safely verifies and unpacks the batch and
catalogs the extracted files through normal Lane custody. The tar and manifest
are not cataloged as user storage.

If creation or transfer fails, visible Lane source and the verified artifact
remain. `loom lane send --resume` binds to the latest eligible failed bundle,
re-verifies it, and reuses its original batch rather than creating a replacement.
After a successful send, the completed record becomes durable before LOOM
removes the redundant transport artifact: the batch's `sent/<batch-id>` tree
for `file_tree`, or its tar-and-manifest directory for `bundle_seed`. The
rename-based cleanup quarantine remains for a 30-minute grace period. A newer
success retires the previous successful quarantine immediately, so successful
steady state is one temporary payload-equivalent and then zero after expiry.

The node-agent's `node-agent.lane_housekeeping` worker checks expiry every
minute and records removal state, time, and reason in the batch record. It is
restart-safe and idempotent. Failed, interrupted, active, cleanup-withheld,
acknowledged cleanup-withheld, and archived cleanup-withheld artifacts and
quarantines are protected from automatic removal regardless of age.

Human and JSON status expose retained recovery bytes, the successful-grace
portion, protected evidence, cleanup quarantine bytes, transport-safety bytes,
untracked bytes, and the next successful-quarantine expiry. Material protected
evidence warns without imposing an automatic byte cap. Status, inspect,
dry-run, and Portal rendering do not run housekeeping or mutate storage. Local
tests verify this lifecycle, but production-node throughput and hardware-main
acceptance remain deferred operator checks.

`loom lane status` and Portal's Inspect Transfer Plan compile the local
filesystem-policy inventory even when rsync, SSH, or main is unavailable. The
Portal plan performs no remote preparation and writes no transfer state.
`loom lane send --dry-run` follows the actual send path and therefore keeps the
rsync/SSH preflight; use status or the Portal plan when only a local read-only
inventory is required.

Publish or repair an accepted batch without re-uploading:

```bash
loom lane publish <batch-id> --dry-run
loom lane repair <batch-id> --dry-run
```

Attention handling:

```bash
loom lane acknowledge-pending <relative-path> --note "reviewed"
loom lane acknowledge-transfer <batch-id> --note "reviewed"
loom lane archive-transfer <batch-id> --note "reviewed"
```

At verification time, `loom lane send --dry-run` returned a `noop` result
because Lane was empty.

### Storage Browse Commands

```bash
loom storage tree --limit 100
loom storage list --limit 50
loom storage list --node macbook
loom storage list --source-area notes
loom storage list --file-class text
loom storage inspect <view-path-or-storage-entry-id>
loom storage inspect-path <view-path-or-main-documents-path>
```

Common filters for `tree`, `list`, and `fidelity report`:

- `--node`;
- `--source-area`;
- `--storage-class`;
- `--file-class`;
- `--processing-state`;
- `--availability-state`;
- `--include-deleted`;
- `--limit`.

### Status, Verify, And Safe Delete

```bash
loom storage status <path>
loom storage verify <path>
loom storage verify-path <main-documents-view-path>
loom storage safe-delete check <source-path-or-view-path>
loom storage safe-to-delete <source-path-or-view-path>
```

Prefer `storage safe-delete check` in new docs and scripts. The
`safe-to-delete` command remains available.

Slice 7 verified both safe-delete forms against an existing `.loom-acceptance`
view path. Both reported the fixture file safe because main had an accepted
retained copy.

### Retention And Fidelity

```bash
loom storage retention status
loom storage fidelity report --limit 50
loom storage fidelity backfill --dry-run --limit 100
loom storage fidelity backfill --apply --yes --limit 100
```

`fidelity backfill --dry-run` is the default planning posture. Apply mode writes
filesystem observation and fidelity finding rows.

### Main Documents

```bash
loom storage main-documents status
loom storage main-documents reconcile --dry-run
loom storage main-documents retention backfill --dry-run --limit 100
loom storage main-documents protection <relative-path>
loom storage main-documents safe-delete <relative-path>
```

`main-documents reconcile --yes` tombstones missing catalog rows, so keep the
dry-run in normal examples.

### Export Compatibility, Archive, Fetch, And Restore

The one-release export surface is diagnostic only:

```bash
loom storage export status
```

There is no export refresh, rebuild, repair, materialize, create, link, or
delete command. Inspect canonical roots and catalog evidence instead:

```bash
loom storage filesystem status
loom storage filesystem verify
loom storage doctor
```

Archive:

```bash
loom storage archive <view-path-or-storage-entry-id> --dry-run --kind manual_archive --to main/Archive/<key>
```

Fetch and restore write local files:

```bash
loom storage fetch <view-path-or-storage-entry-id> --to <destination>
loom storage restore plan <view-path-or-storage-entry-id> --mode safe --target <destination> --out <plan-file>
loom storage restore apply <plan-id-or-plan-file> --yes
```

Use restore planning before apply. Use `safe` mode unless a faithful or raw
restore is explicitly required.

### Failures, Doctor, Cleanup, And Repair

```bash
loom storage transfers --limit 20
loom storage failures --limit 20
loom storage doctor --include-fidelity
loom storage inventory --root <canonical-box-root>
loom storage cleanup plan --root <canonical-box-root> --out <plan.json>
loom storage cleanup apply --plan <plan.json> --dry-run
loom storage repair catalog --source main-documents --dry-run
loom storage repair retention --dry-run
```

Cleanup quarantines by default when applied. `--delete-now` is a stronger
operator decision and should not be used as a casual example.

### Mount Commands

```bash
loom storage mount-status
loom storage mount-status --doctor
loom storage mount-policy status --local
loom storage mount-policy enable --local
loom storage mount-policy disable --local --keep-mounted
loom storage mount-policy repair-once --local
```

At verification time, mount status reported SMB readiness with mount path
`~/loom-storage`, and local mount policy reported desired and actual state as
mounted.

### Benchmark And Cloud Offload

The benchmark command is an acceptance tool, not a read-only diagnostic. Pass
explicit scenario paths and let it remove fixtures by default:

```bash
loom storage benchmark \
  --local-path "$HOME/loom-box/Documents/.loom-acceptance/storage-benchmark/local" \
  --smb-path "/Volumes/loom-main-box/Documents/.loom-acceptance/storage-benchmark/smb"
```

Do not point `--smb-path` at read-only `/Volumes/loom-storage`, and do not use an
rsync target without separate host/write authorization. `--keep` deliberately
retains test artifacts and therefore needs a cleanup decision.

Cloud offload is separate from main backup snapshots. Plan first, then apply
only with explicit remote authority:

```bash
loom storage cloud-offload plan <archive-ref>
loom storage cloud-offload apply <archive-ref> --confirm
loom storage cloud-status <cloud-offload-ref>
loom storage cloud-fetch <cloud-offload-ref> --to <missing-file-or-empty-directory>
```

`--allow-non-archive` broadens accepted sources and must be an explicit reviewed
decision. Fetch never restores directly into canonical source or custody.

### JSON Shape Note

Most backend-backed commands return the normal LOOM response envelope with
`ok`, `data`, and `meta`. Some aggregate local diagnostics, such as workspace
Lane, storage transfers, failures, doctor, repair retention, and mount policy
status, return direct reports. Scripts should inspect the specific command
output instead of assuming one JSON shape for every storage command.
