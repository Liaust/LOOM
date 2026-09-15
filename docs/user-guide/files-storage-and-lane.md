---
title: "Files Storage And Lane"
description: "Use Box, Lane, canonical main custody, and direct Box/Storage shares without confusing editable files with protection copies."
audience:
  - user
  - operator
tags:
  - loom
  - user-guide
  - storage
status: verified
verified_at: "2026-08-30"
source_scope:
  - "go run ./cmd/loom box --help"
  - "go run ./cmd/loom lane --help"
  - "go run ./cmd/loom storage --help"
  - "tests/smoke/canonical_filesystem_local.sh"
related:
  - "[[Box Storage And Lane]]"
  - "[[Canonical Paths]]"
  - "[[Storage And Timeline Portal]]"
---
# Files Storage And Lane

Use Box for work, Lane for deliberate transfer, and main Storage for read-only
inspection of accepted custody.

## Start With Box

Inspect or create a Box locally:

```bash
loom box status
loom box init --dry-run --path "$HOME/loom-box" --profile workspace
loom box init --path "$HOME/loom-box" --profile workspace
```

`Documents`, `Notes`, and `Projects` are editable source folders. On workspace
nodes, `loom-lane` is the transfer input governed by policy. Main has no Lane
input; it receives workspace transfers in Storage Imports. Do not write
volatile worker state into `.loom/state`; current runtime state is node-owned
and hidden outside Box.

Use the project or Box ignore explanation before assuming a file will move:

```bash
loom ignore inspect "$HOME/loom-box"
loom ignore explain "$HOME/loom-box/Projects/example/cache.bin" \
  --root "$HOME/loom-box"
```

## Send With Lane

Review pending content and transport choice first:

```bash
loom lane status
loom lane send --dry-run
```

Then send according to the reviewed plan. LOOM may select a file tree or a
bundle, but both preserve the same inventory and cleanup policy. Keep-local
leaves the reviewed source in place; normal cleanup quarantines only after main
promotion and catalog completion.

If a transfer stops:

```bash
loom lane status
loom lane repair <batch-id>
```

Repair uses accepted staging/custody evidence and does not re-upload payload
bytes. If local content changed, LOOM withholds cleanup rather than deleting an
unproven source.

## Inspect Main Custody

Use typed catalog/status commands:

```bash
loom storage filesystem status
loom storage list --limit 50
loom storage inspect <view-path-or-entry-id>
loom storage transfers
loom storage failures
```

The former generated filesystem tree is not operational truth. `loom storage
export status` is only a deprecated read-only compatibility diagnostic.

## Main Box And Storage On Mac

The private SMB identities are distinct:

- `/Volumes/loom-main-box`: main Box, read/write;
- `/Volumes/loom-storage`: canonical Storage, read-only.

Use the repository mount helpers or Portal actions so host/share/path identity
is checked. Do not manually repurpose one mountpoint for the other, and do not
mount either over the direct-cloud path.

## Delete Safely

Check the exact source before deletion:

```bash
loom storage safe-delete check <path-or-entry>
loom storage fidelity report --limit 50
```

`safe` means required retention evidence is verified. `partially_safe` means
the payload is protected but metadata differences are known. Any failed,
unknown, or incomplete result is a stop condition.

Use cleanup planning for known acceptance artifacts:

```bash
loom storage inventory
loom storage cleanup plan --out <reviewed-plan.json>
loom storage cleanup apply --plan <reviewed-plan.json> --yes
```

Cleanup quarantines by default. Immediate deletion requires the separate
`--delete-now` decision.

## Related Docs

- [[Box Storage And Lane]]
- [[Canonical Paths]]
- [[Storage Retention And Safe Delete]]

## Complete live reference and preserved operational detail

The canonical guidance above is the current storage contract. The detailed material below preserves the complete live command, route, workflow, safety, interpretation, and troubleshooting coverage that predates the filesystem migration. Retired generated-export behavior is retained only when explicitly labelled as compatibility or historical context.

### What This Page Covers

This page explains the normal file workflow in LOOM:

1. use the LOOM Box as the local user-facing workspace;
2. use workspace LOOM Lane for explicit custody transfers;
3. inspect accepted custody through catalog paths and the read-only direct
   Storage share;
4. check retention and safe-delete state before removing source-side data.

For the implementation model, read [[Box Storage And Lane]]. For exact command
syntax, read [[Storage Box And Lane CLI Reference]].

### The Rule That Prevents Confusion

Writable source folders and canonical custody are different things.

Use source folders for work:

- `~/loom-box/Documents`;
- `~/loom-box/Notes`;
- `~/loom-box/Projects`;
- `~/loom-box/loom-lane`;
- main-owned writable sources such as main `Documents` when operating on main.

Use canonical read-only custody and catalog paths for inspection:

- `~/loom-storage`, the direct read-only canonical Storage share;
- catalog paths such as `macbook/Backups/...`;
- canonical backup and archive custody plus explicitly generated projections.

Do not edit canonical backup/archive custody or generated projections. If an
accepted copy is wrong, fix the writable source, inspect the transfer/catalog
evidence, and use the relevant bounded retry or repair action.

### Open The Portal

The Box surface is the best place to inspect local file readiness:

```bash
loom enter --start box
```

The Storage surface is the best place to inspect accepted custody, catalog
paths, physical roots, retention, and protection state:

```bash
loom enter --start storage
```

Slice 7 verified both one-shot renders:

```bash
loom --no-animation --no-color enter --exit-after-render --no-boot-animation --start box
loom --no-animation --no-color enter --exit-after-render --no-boot-animation --start storage
```

At verification time, the Box surface showed a valid Box contract, an empty
Lane, reported Documents and Notes watched roots, and healthy Box watch state.
The Storage surface showed the `loom-main` view, storage safety counts, user
storage roots, main `Documents` status, file-transfer counts, cloud protection
summary, and main backup summary.

### Check Local Box State

Start with:

```bash
loom box status
```

Machine-readable form:

```bash
loom --json box status
```

The status output is the contract-backed operational view. It tells you:

- Box root path;
- profile;
- owner node;
- contract state;
- enabled areas;
- workspace Lane state, or Main Storage Imports locality;
- watched-root policy files.

Use `box path` only when a script needs the resolved root path:

```bash
loom box path
```

For profile and owner truth, prefer `loom box status` because it reads the Box
contract and current local state.

### Initialize Or Repair The Box

Preview before writing:

```bash
loom box init --dry-run
loom box repair --dry-run
```

Apply only when the planned folders and policy files are correct:

```bash
loom box init
loom box repair
```

Normal users usually should not need these commands after setup. Use them when
the Box folder is missing, partially initialized, or repaired after a local
filesystem issue.

### Check Watch Policy

The Box watch policy describes which Box areas the node-agent should watch and
how each area should be treated.

Preview the desired state:

```bash
loom box watch-plan
```

Check backend registration and node-agent reports:

```bash
loom box watch-status
```

Dry-run backend application:

```bash
loom box watch-apply --dry-run
```

Apply only when you intend to record the desired watch policy on main:

```bash
loom box watch-apply
```

At verification time, the plan included Documents and Notes. Projects own
their own project-level watch policies, while workspace Lane is an explicit
custody transfer path. Main has no Lane watched root.

### Review What Is Protected

Continuous Box backup uses the `managed` policy: mandatory LOOM state,
reconstructible dependency/cache trees, and matching `.loomignore` paths are
omitted. The Portal's watched-root rows and backup status show protected and
ignored counts. Ignored means unprotected by that operation, not merely hidden
from search.

Inspect the effective policy without changing files:

```bash
loom ignore inspect ~/loom-box/Documents --operation backup
loom ignore explain path/to/file --root ~/loom-box/Documents --operation backup
```

Place `.loomignore` at the Box root, a project root, an independently watched
root, or a nested directory. It supports Gitignore-style comments, blank lines,
anchored/unanchored paths, directory rules, `*`, `?`, `**`, escaped leading
`#`/`!`, and `!` negation. A later matching rule wins, but mandatory LOOM state
cannot be re-included. The `.loomignore` file stays protected so the policy is
carried with the source.

LOOM deliberately does not copy `.gitignore` rules into backup policy.
Version-control cleanliness is not recovery policy. `.git`, `.env`, `.secrets`,
`.data`, `dist`, `build`, and `target` are retained unless `.loomignore`
excludes them; `.git` may contain local-only refs and history.

### Use LOOM Lane

LOOM Lane is for explicit local-to-main custody transfer. Put files under:

```text
~/loom-box/loom-lane
```

Then inspect:

```bash
loom lane status --include-fidelity
```

The normal profile is `faithful`: it applies mandatory safety and
`.loomignore`, while retaining dependency trees and Git history. Preview the
canonical transfer plan and automatic transport choice:

```bash
loom lane send --dry-run
```

Use deeper modes only when their tradeoff is intentional:

```bash
loom lane send --source-only --dry-run
loom lane send --exact --dry-run
```

`--source-only` also omits managed reconstructible dependencies. `--exact`
bypasses `.loomignore` while preserving mandatory safety; review its warning
because previously ignored data will enter the transfer. If a dry-run is the
approved plan, copy its fingerprint into
`--expected-policy-fingerprint <fingerprint>` when sending.

Transport choice does not change those content rules. By default, Lane uses
`auto`: it selects a verified `bundle_seed` for more than 10,000 regular files,
or for more than 2,000 regular files averaging below 256 KiB. Other batches use
the existing `file_tree` path. Status and dry-run show the recommendation,
reason, average size, and estimated temporary space before anything is sent.

Force a mode only for a reviewed reason:

```bash
loom lane send --bundle --dry-run
loom lane send --no-bundle --dry-run
```

`--bundle` and `--no-bundle` are mutually exclusive and report a warning when
they override the recommendation. A bundle is one uncompressed PAX tar plus a
manifest stored under hidden Lane state. It is the retry safety artifact, not a
second file-tree copy and not a storage item. Main verifies and safely unpacks
it, then catalogs only the extracted files through the normal Lane path.

Send pending files only after the dry-run is acceptable:

```bash
loom lane send
```

In the Box Portal, `Send Lane` runs faithful mode. `Send Lane...` contains
Inspect Transfer Plan, source-only, and exact choices. Inspect Effective Ignore
Rules and transfer planning are local and remain usable when main is offline;
send actions follow the Portal's main availability gate.

Inspect Transfer Plan reads the Lane filesystem and policy directly. It needs
neither rsync nor SSH, performs no remote preparation, and writes no transfer
state. The closest CLI equivalent is `loom lane status`. In contrast,
`loom lane send --dry-run` deliberately keeps the real-send rsync/SSH preflight.

Useful recovery commands:

```bash
loom lane send --resume
loom lane publish <batch-id> --dry-run
loom lane repair <batch-id> --dry-run
loom lane acknowledge-pending <relative-path> --note "reviewed"
loom lane acknowledge-transfer <batch-id> --note "reviewed"
loom lane archive-transfer <batch-id> --note "reviewed"
```

If a bundle build or transfer fails, both the visible Lane source and verified
artifact remain. Resume binds to the latest eligible failed bundle, re-verifies
it, and reuses its original batch.

After a successful transfer, LOOM keeps the renamed cleanup quarantine for 30
minutes so late writes through a previously opened file remain recoverable.
Once the completed record is durable, it removes the redundant file-tree safety
tree or bundle artifact. A second success removes the older successful
quarantine immediately and gives only the new batch its grace period. The
node-agent expires that current quarantine automatically after the deadline.

`loom lane status` and the Box Portal show how many bytes are retained for the
successful grace period and how many belong to protected failed or
cleanup-withheld evidence. They warn when protected evidence is material, but
never delete it. Acknowledging or archiving cleanup-withheld attention does not
authorize automatic deletion. Status, dry-run, Inspect Transfer Plan, and
Portal rendering are read-only and never trigger housekeeping.

Repository tests verify the local lifecycle and safe unpacking. Hardware-main
acceptance and production throughput are still separate operator validation
and are not claimed by this page.

At verification time, `loom lane status --include-fidelity` reported no
pending files and a ready SSH/rsync preflight.

### Retired Intake Evidence

Dropzone is retired and is not a current user workflow. Old transfer records
may remain available to bounded read-only compatibility readers, but current
Box status, Portal, help, completion, and search do not expose it as an intake
surface. Do not recreate its folder, policy, worker, or upload route.

Main operators can inventory exact obsolete Main names through a saved setup
plan:

```bash
loom setup cleanup plan --plan <saved-main-setup-plan.json>
```

The inventory is non-recursive. A separately reviewed apply requires the exact
cleanup digest and confirmation, and skips any non-empty, unknown, linked to an
unexpected target, or changed entry. This guide does not authorize running it
on production.

### Inspect Main Storage

List stable catalog paths:

```bash
loom storage tree
```

List catalog entries:

```bash
loom storage list --limit 20
```

Inspect one path:

```bash
loom storage inspect <view-path-or-storage-entry-id>
loom storage inspect-path <view-path-or-main-documents-path>
```

Status and verification commands explain accepted, pending, failed, ignored,
retained, and safe-delete states:

```bash
loom storage status <path>
loom storage verify <path>
loom storage verify-path <main-documents-view-path>
```

At verification time, `loom storage tree --limit 5` returned the `loom-main`
catalog view with read-only backup/archive custody paths and the separately
writable Main Box source area.

### Check Safe Delete

Before deleting source-side data, run:

```bash
loom storage safe-delete check <source-path-or-view-path>
```

The older equivalent command is:

```bash
loom storage safe-to-delete <source-path-or-view-path>
```

Prefer `storage safe-delete check` in new docs and scripts because its purpose
is explicit.

Only delete source-side files when the result says the item is safe and the path
you are deleting is actually the source path you intend to remove. A catalog or
custody path can prove that LOOM has retained data, but it is not itself the
writable source.

### Work With The Local Storage Link

On Mac, the local storage link is:

```text
~/loom-storage
```

Check mount readiness:

```bash
loom storage mount-status
loom storage mount-policy status --local
```

Treat this as an inspection surface. If you need to change a file, go back to
the source folder in `~/loom-box`, a project folder, or main `Documents`.

### When Something Looks Wrong

Use this sequence:

```bash
loom box status
loom lane status --include-fidelity
loom storage failures --limit 20
loom storage doctor --include-fidelity
loom enter --start doctor
```

Then use the more specific operations page:

- [[Storage Retention And Safe Delete]] for retention, fidelity, cleanup, and
  safe-delete checks;
- [[Notes Index Maintenance]] when a notes file exists but search is stale;
- [[Backups Cloud And Restore]] when the issue is backup or cloud protection.
