---
title: "Storage Retention And Safe Delete"
description: "Inspect canonical physical roots, catalog and retention evidence, fidelity differences, cleanup plans, and safe-delete decisions."
audience:
  - operator
  - developer
tags:
  - loom
  - operations
  - storage
status: verified
verified_at: "2026-08-27"
source_scope:
  - "internal/storagedoctor"
  - "internal/storageretention"
  - "internal/storagefidelity"
  - "internal/storagecleanup"
  - "internal/storagemigration"
related:
  - "[[Storage Box And Lane CLI Reference]]"
  - "[[Canonical Paths]]"
  - "[[Backup Sync Retention And Cloud]]"
---
# Storage Retention And Safe Delete

## Inspect Before Mutation

```bash
loom storage filesystem status
loom storage doctor
loom storage retention status
loom storage fidelity report --limit 100
loom storage failures
```

Filesystem status is bounded: it checks configured roots, catalog counts, and
typed findings without walking every payload tree. Missing, symlinked, or
inaccessible canonical roots fail closed.

## Safe Delete

Check one exact source:

```bash
loom storage safe-delete check <path-or-entry-id>
loom storage inspect-path <path>
```

Interpret the result:

- `safe`: required retained payload and identity evidence is verified;
- `partially_safe`: bytes are retained but safe-view/fidelity differences are
  known;
- `unsafe`, `unknown`, or failed: do not delete.

Object blobs and retention payloads are intentional protection copies. They
may retain bytes, but their mode/mtime is not inspected as though it were the
original source.

## Fidelity

```bash
loom storage fidelity backfill --dry-run --limit 100
loom storage fidelity report --limit 100
```

Apply only after reviewing the bounded observations. Backfill records evidence;
it does not rewrite payload bytes or rebuild a view.

## Main Documents

The active root is configured canonical Box `Documents`, not the deprecated
`MainDocuments` migration input.

```bash
loom storage main-documents status
loom storage main-documents reconcile --dry-run
loom storage main-documents retention backfill --dry-run --limit 100
```

Any mutating reconciliation with distinct legacy/canonical roots must compare
both roots. Normal importer ticks skip deprecated-root inventory unless an
explicit comparison or mutation requires it.

## Cleanup

```bash
loom storage inventory
loom storage cleanup plan --out <reviewed-plan.json>
loom storage cleanup apply --plan <reviewed-plan.json> --yes
```

Only high-confidence acceptance artifacts are eligible. Apply quarantines by
default. `--delete-now` is a separate destructive decision and requires a
reviewed plan.

## Filesystem Migration

Migration has four separate responsibilities:

1. LOOM inventories and writes a reviewed manifest (`--dry-run --manifest`).
2. The operator stops writers and moves bytes outside LOOM.
3. LOOM verifies evidence and atomically rebinds catalog paths (`--apply --yes`).
4. LOOM verifies the new layout; rollback uses the exact inverse reviewed
   evidence when the cutover plan calls for it.

Never apply a manifest with changed source/destination evidence, overlapping
roots, open conflicts, or a layout fingerprint from another runtime.

## Retired Export

There is no export repair/rebuild step. `loom storage export status` is a
deprecated read-only compatibility diagnostic and must not appear in repair
automation.

## Related Docs

- [[Storage Box And Lane CLI Reference]]
- [[Canonical Paths]]
- [[Backup Sync Retention And Cloud]]

## Complete live reference and preserved operational detail

The canonical guidance above is the current storage contract. The detailed material below preserves the complete live command, route, workflow, safety, interpretation, and troubleshooting coverage that predates the filesystem migration. Retired generated-export behavior is retained only when explicitly labelled as compatibility or historical context.

### Purpose

This runbook keeps storage operations safe.

Use it when:

- a user wants to delete source-side files after LOOM has accepted them;
- catalog or physical-root status shows unsafe candidates;
- main `Documents` needs reconciliation;
- canonical root or catalog evidence needs inspection;
- fidelity findings may affect restore quality;
- `.loom-acceptance` or other dev artifacts need cleanup.

### Start With Read-Only Checks

Run:

```bash
loom storage tree --limit 100
loom storage list --limit 50
loom storage transfers --limit 20
loom storage failures --limit 20
loom storage retention status
loom storage export status
loom storage main-documents status
loom storage doctor --include-fidelity
```

Then inspect the portal:

```bash
loom enter --start storage
loom enter --start timeline
loom enter --start doctor
```

Do not start with cleanup, archive, restore, or apply commands.

### Safe Delete Procedure

Use this procedure before deleting any source-side file.

1. Identify the source file and any catalog/custody path.
2. Inspect the path:

```bash
loom storage status <path>
loom storage verify <path>
```

3. Run the explicit safe-delete check:

```bash
loom storage safe-delete check <source-path-or-view-path>
```

4. Confirm the result says safe and understand why.
5. Delete only the intended source-side file, never canonical backup/archive
   custody or generated projections.

The older equivalent command is:

```bash
loom storage safe-to-delete <source-path-or-view-path>
```

Prefer `safe-delete check` for new procedures.

### Interpreting Safe Delete

Safe delete means LOOM believes source-side deletion is acceptable because a
retained copy, accepted backup artifact, archive, or other protection state is
available.

It does not mean:

- every generated view can be edited;
- every fidelity attribute is perfectly restorable;
- deleting a whole folder is safe because one child file is safe;
- user intent has been confirmed.

If fidelity findings matter, run the fidelity checks before deletion.

### Fidelity Checks

Report known differences:

```bash
loom storage fidelity report --limit 50
loom storage fidelity report <path-or-prefix>
```

Plan backfill:

```bash
loom storage fidelity backfill --dry-run --limit 100
```

Apply only after reviewing the dry-run:

```bash
loom storage fidelity backfill --apply --yes --limit 100
```

Fidelity backfill records observations and findings. It must not be treated as
a payload rewrite or a storage export refresh.

### Main Documents Protection

Main `Documents` is a writable main source area. Check it separately:

```bash
loom storage main-documents status
```

Inspect one relative path:

```bash
loom storage main-documents protection <relative-path>
loom storage main-documents safe-delete <relative-path>
```

Reconcile missing catalog rows with a dry-run first:

```bash
loom storage main-documents reconcile --dry-run
```

Apply tombstones only after review:

```bash
loom storage main-documents reconcile --yes --reason "operator reviewed missing files"
```

Backfill missing retained payloads with a dry-run first:

```bash
loom storage main-documents retention backfill --dry-run --limit 100
```

Apply:

```bash
loom storage main-documents retention backfill --yes --limit 100
```

### Retired Export Compatibility

The storage export is retired. The one-release compatibility diagnostic is
read-only:

```bash
loom storage export status
```

There is no rebuild, refresh, repair, or apply path. Use `loom storage doctor`,
`loom storage filesystem status`, and `loom storage filesystem verify` for
canonical physical-root and catalog evidence.

If backend state is healthy but the local Mac mount looks wrong, check mount
state separately:

```bash
loom storage mount-status
loom storage mount-policy status --local
```

### Archive And Restore

Archive dry-run:

```bash
loom storage archive <view-path-or-storage-entry-id> --dry-run --kind manual_archive --to main/Archive/<key>
```

Restore planning:

```bash
loom storage restore plan <view-path-or-storage-entry-id> --mode safe --target <destination> --out <plan-file>
```

Apply restore only after reviewing the plan:

```bash
loom storage restore apply <plan-id-or-plan-file> --yes
```

Fetch and restore commands write destination files. Keep destinations explicit.

### Cleanup Dev Artifacts

Inventory first:

```bash
loom storage inventory --root <canonical-box-root>
```

Plan:

```bash
loom storage cleanup plan --root <canonical-box-root> --out <plan.json>
```

Dry-run apply:

```bash
loom storage cleanup apply --plan <plan.json> --dry-run
```

Apply:

```bash
loom storage cleanup apply --plan <plan.json> --yes
```

Cleanup quarantines by default. Use `--delete-now` only after an explicit
operator decision and reviewed plan. Production cleanup outside
`.loom-acceptance` paths requires the production flags documented by the CLI.

### Repair Planning

Use dry-run repair commands before apply modes:

```bash
loom storage repair catalog --source main-documents --dry-run
loom storage repair retention --dry-run
```

`repair catalog --yes` can tombstone or repair catalog rows where supported.
Keep `--reason` and `--created-by` explicit when applying catalog repairs.

### Stop Conditions

Stop and escalate before applying changes when:

- safe-delete is false or partially safe;
- a catalog path is the only evidence and no exact source/custody physical ref
  can be identified;
- fidelity findings include metadata loss that matters to the user;
- retention has failed rows;
- canonical filesystem verification reports divergent roots or unsafe path
  evidence;
- cleanup wants to touch paths outside `.loom-acceptance` without production
  review;
- a command requires cloud, backup, or database actions not covered by this
  runbook.
