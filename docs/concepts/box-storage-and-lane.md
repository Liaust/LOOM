---
title: "Box Storage And Lane"
description: "How LOOM separates writable source files, canonical custody, protection copies, generated artifacts, and transfer state."
audience:
  - user
  - operator
  - developer
tags:
  - loom
  - concepts
  - storage
status: verified
verified_at: "2026-08-30"
source_scope:
  - "internal/filesystemlayout"
  - "internal/box"
  - "internal/lane"
  - "internal/storagecatalog"
  - "internal/storagedoctor"
related:
  - "[[Canonical Paths]]"
  - "[[Files Storage And Lane]]"
  - "[[Storage Retention And Safe Delete]]"
aliases:
  - "LOOM Storage Model"
---
# Box Storage And Lane

LOOM does not treat every filesystem path as the same kind of truth. A path is
useful only when its ownership and mutation rules are clear.

## The Four Storage Classes

1. **Live source** is where a person or application intentionally edits a
   file. Box `Documents`, `Notes`, `Projects`, and Lane input are source
   surfaces.
2. **Canonical custody** is the durable physical location accepted by main.
   Lane batches enter `Storage/imports`, watched-root backups enter
   `Storage/backups`, and archives enter `Storage/archive`.
3. **Protection copy** is retained to prevent loss or support recovery. Object
   blobs, retention payloads, operational backups, and cloud snapshots are not
   the editable source and are not substituted for it in fidelity checks.
4. **Generated artifact** is reproducible operational output. The Notes
   projection and indexes are generated. The former generated storage export
   is retired.

The storage catalog connects these classes. It owns identity, lineage,
checksums, availability, retention, and physical references; directory names
alone are not catalog truth.

## Box

A Box is a human-owned workspace. A workspace Box has these visible folders:

```text
Documents/
Notes/
Projects/
loom-lane/
```

Main Box contains `Documents/`, `Notes/`, `Projects/`, `Topics/`, and
`Library/`; it has no Lane input. Workspace Lane sends are accepted into
Main's configured Storage Imports root.

`Box/.loom/` contains durable contracts and policies. Volatile workspace Lane
state belongs under the configured node runtime state root, normally
`/var/lib/loom/box-state`. New installs do not create `Box/.loom/state`.
Legacy state at that path is a migration input only: LOOM selects one complete
state tree, reports migration required, and fails closed if legacy and
canonical trees diverge.

Project-local `.loom/` remains a separate portable project contract and is not
changed by the Box runtime-state rule.

## Lane

Lane transfers deliberate batches from a Box to main. `file_tree` and
`bundle_seed` use the same inventory, policy fingerprint, acceptance identity,
promotion contract, catalog transaction, cleanup intent, and repair lifecycle.

Main stages transport data below its confined runtime staging root, then
promotes accepted content into the configured Imports root. Promotion never
trusts a caller-supplied final path. Cross-device copy/verify/rename requires
explicit authorization that is retained for retries of that same batch.

Important statuses include:

- `promotion_failed`: transport arrived, canonical promotion did not finish;
- `accepted_on_main`: canonical bytes exist but catalog finalization is not
  complete;
- `catalog_failed`: catalog commit needs repair;
- `source_cleanup_failed`: custody/catalog are complete but remaining cleanup
  needs an idempotent retry;
- `local_cleanup_withheld`: LOOM could not prove the local source was unchanged;
- `cataloged`: main custody is complete and the local source was intentionally
  retained;
- `local_cleanup_done`: main custody is complete and the reviewed local source
  cleanup finished.

`loom lane repair` resumes from persisted evidence; it does not re-upload
payload bytes. Live attention can be acknowledged or archived without deleting
the transfer history.

## Retired Intake Compatibility And Watched Roots

Dropzone is retired. It has no current Box folder, policy, worker, Portal
section, search alias, or mutation action. Bounded compatibility readers may
decode historical transfer records without creating paths or changing
evidence. Watched roots create immutable per-batch backup custody under the
configured `Storage/backups` root. Small/direct and large/chunked paths
converge on the same manifest-committed custody contract.

## What Users Mount

Main exposes two private SMB identities:

- `loom-main-box`: active configured main Box, read/write;
- `loom-storage`: canonical physical Storage, read-only.

Storage is inspectable but not an alternate editing surface. Finder metadata
is supported at the protocol layer; importers ignore routine sidecars rather
than presenting them as user documents.

## Safe Mutation Rule

Do not infer that a file is deletable because another pathname exists. Use
typed status and `loom storage safe-delete check`. A safe result requires
catalog identity plus verified retained evidence; known fidelity differences
may produce `partially_safe` rather than a false guarantee.

## Compatibility Boundary

`loom storage export` remains a read-only deprecated diagnostic for one
release. It cannot build, refresh, repair, link, or delete an export. Legacy
path fields remain readable only for migration and old-manifest evidence.

## Related Docs

- [[Canonical Paths]]
- [[Files Storage And Lane]]
- [[Storage Retention And Safe Delete]]

## Complete live reference and preserved operational detail

The canonical guidance above is the current storage contract. The detailed material below preserves the complete live command, route, workflow, safety, interpretation, and troubleshooting coverage that predates the filesystem migration. Retired generated-export behavior is retained only when explicitly labelled as compatibility or historical context.

### The Short Version

LOOM separates where users work from where LOOM records, protects, and exposes
accepted data.

- Box is the local user workspace.
- Workspace Lane is the current explicit custody transfer mechanism; Main
  receives accepted batches in Storage Imports.
- The storage catalog is main's record of accepted storage entries.
- The storage export is a generated filesystem view of catalog state.
- Retention and physical refs prove whether bytes are protected.
- Safe-delete checks decide whether a source-side file can be removed.

This separation is why LOOM can show a file in `loom-storage` without treating
that generated view as the file's writable source of truth.

### LOOM Box

The Box is a local folder with a contract under `.loom/box.yaml`. The contract
declares user-facing areas and policy files.

Current user-facing areas include:

| Area | Purpose |
|---|---|
| `Documents` | General files that should be backed up and cataloged. |
| `Notes` | Notes input point for [[Notes Knowledge Index]]. |
| `Projects` | Project-owned folders and contracts. |
| `loom-lane` | Workspace-only explicit custody transfer staging. |

The Box status command reads the contract and filesystem state. The path command
only resolves the root path and is best treated as a scripting helper.

Box initialization adds scoped agent guidance to `Notes/AGENTS.md` and
`Documents/AGENTS.md`; it does not add a Box-root identity file. Notes is a
canonical writable source whose content enters backup and indexing pipelines
according to Box policy. Documents is canonical broad user storage, where
metadata and indexing support varies by file type. In both areas, generated
metadata, projections, and `loom-storage` exports are not writable sources.

Existing agent instructions are user content and are preserved. A later Box
initialization may add a missing standard file, but it does not overwrite a
user-edited `AGENTS.md`. Destructive or bulk document changes require explicit
user scope, and exact operational commands should be checked through
`search-loom-docs`.

### Watch Policies

Box policies compile into node-agent watched-root desired state.

Documents and Notes are normal watched roots:

- Documents use incremental raw backup and metadata-oriented indexing.
- Notes use incremental raw backup plus notes-specific indexing.
- Both use the `managed` file-policy profile: mandatory LOOM runtime state,
  reconstructible dependencies, and user `.loomignore` rules are omitted from
  that operation.

Projects are not compiled from the Box watch policy because projects own their
own project-level contracts and facets. Workspace Lane is not a watched mirror
because it is a custody transfer path, not a continuous source mirror. Main has
no Lane watched-root or intake folder.

Watched-root status records included and excluded counts. The Portal presents
these as protected and ignored counts so an omitted path is not mistaken for
protected data.

### File Policy Profiles And `.loomignore`

LOOM resolves one operation-specific policy rather than maintaining unrelated
exclude lists:

| Profile | Normal use | Mandatory safety | Reconstructible defaults | `.loomignore` |
|---|---|---|---|---|
| `managed` | Continuous backup | applied | applied | applied |
| `faithful` | Normal Lane and project export | applied | retained | applied |
| `source_only` | Smaller Lane transfer | applied | applied | applied |
| `exact` | Explicit complete Lane transfer | applied | retained | bypassed |

Mandatory safety covers LOOM-owned transfer/runtime state such as
`.loom/state/` and `.loom/tmp/`; it cannot be negated. Managed reconstructible
defaults include dependency and cache trees such as `node_modules`, `.venv`,
`venv`, `__pycache__`, `.pytest_cache`, `.mypy_cache`, and `.ruff_cache`.
`.git`, `.env`, `.secrets`, `.data`, `dist`, `build`, and `target` are retained
unless a user rule excludes them. In particular, `.git` can contain local-only
history and refs, so it is not safe to classify as universally reproducible.

LOOM discovers `.loomignore` at the Box or project root, at independently
watched-root boundaries, and in nested directories. Rules use Gitignore-style
comments, blank lines, anchored and unanchored paths, directory patterns,
`*`, `?`, `**`, escaped leading `#` or `!`, and `!` negation. Parent rules are
resolved before child rules and the last matching rule wins, except that no
rule can re-include mandatory safety state. The `.loomignore` file itself stays
included so an export or transfer carries its policy intent.

LOOM deliberately does not import `.gitignore`: source-control inclusion and
recoverability are different decisions. A build output may be intentionally
version-ignored but expensive or impossible to recreate, while a tracked path
may still be excluded from one backup by explicit LOOM policy.

Ignored also does not mean "backed up but not indexed." An ignored path is
omitted from that operation and is not protected by it. A backed-up but
unindexed file remains recoverable even though search cannot find its content.
Inspect or explain policy before cleanup:

```bash
loom ignore inspect <root> --operation backup
loom ignore inspect <root> --operation lane
loom ignore explain <path> --root <root> --operation backup
```

### LOOM Lane

Lane is a visible staging area under the Box. It is for intentional transfer to
main.

The normal flow is:

1. a user places files under `~/loom-box/loom-lane`;
2. `loom lane status` reports the faithful canonical plan, policy fingerprint,
   protected/ignored counts, automatic transport recommendation, temporary
   space estimate, and SSH/rsync readiness;
3. `loom lane send --dry-run` reviews the same faithful plan;
4. `loom lane send` creates a batch and uses either the file-tree path or a
   verified seed bundle;
5. main safely accepts the file tree, or verifies and unpacks the seed bundle,
   then catalogs the extracted files through the normal custody path;
6. local Lane cleanup atomically renames accepted source files into a hidden,
   batch-scoped cleanup quarantine when the command is allowed to clean up;
7. after the completed batch record is durable, LOOM removes the now-redundant
   file-tree safety tree or bundle artifact and gives the current cleanup
   quarantine a 30-minute recovery grace period.

Faithful mode retains dependency trees and Git history unless `.loomignore`
excludes them. `--source-only` additionally omits managed reconstructible
defaults. `--exact` bypasses `.loomignore` but still applies mandatory safety;
it therefore requires an explicit warning and review. A reviewed dry-run can
be pinned with `--expected-policy-fingerprint` so a policy change forces a new
plan.

The default transport request is `auto`. LOOM recommends `bundle_seed` when a
batch has more than 10,000 regular files, or more than 2,000 regular files with
an average size below 256 KiB. Otherwise it selects `file_tree`. Empty batches
also remain file-tree no-ops. The recommendation is derived from the same
canonical `TransferPlan` used by file-tree transfer; transport selection does
not change profile semantics, ignore decisions, inventory hashes, or traversal.

A seed bundle is an uncompressed POSIX PAX tar plus a manifest. It is a batch
safety artifact under hidden Lane state, not a second retained source tree and
not a storage-catalog entry. LOOM verifies the source before and after bundle
creation, records archive and manifest checksums, transfers only those two
artifacts, and keeps them while retry evidence is needed. Main verifies the
batch-bound paths, checksum, manifest limits, entry types, counts, sizes, modes,
and paths before atomically promoting the extracted tree. Tar and manifest
files never enter the storage catalog; only extracted Lane files do.

`--bundle` forces `bundle_seed`; `--no-bundle` forces `file_tree`. Both expose a
force warning when they override the automatic recommendation, and they are
mutually exclusive. Use them only for a reviewed reason. A failed bundle
transfer retains both visible source files and the verified bundle artifact;
`--resume` re-verifies and reuses that artifact rather than rebuilding it.

Successful and non-successful recovery evidence have different lifecycles. A
successful batch retains only its rename-based cleanup quarantine for 30
minutes; a newer successful batch retires the previous successful quarantine
immediately. The completed batch record audits the expiry and removal. The
successful transport safety artifact is removed only after main custody is
accepted, cataloged, published, the cleanup quarantine is durable, and the
completed record is persisted. Failed, interrupted, active, cleanup-withheld,
acknowledged cleanup-withheld, and archived cleanup-withheld evidence is never
removed by automatic housekeeping.

The local node-agent runs Lane recovery housekeeping on a one-minute interval,
so expiry converges across restart. `loom lane status`, dry-runs, Portal
rendering, and Inspect Transfer Plan only report retained, grace, protected,
cleanup, safety, and untracked bytes; those read-only paths never trigger
cleanup. Material protected evidence produces a warning and still requires
retry, repair, or explicit operator cleanup.

In the Portal, `Send Lane` is the faithful primary action. `Send Lane...`
groups Inspect Transfer Plan, source-only send, and exact send, while Inspect
Effective Ignore Rules remains a local action. When main is offline, local
inspection stays available and transfer actions remain disabled by the normal
Portal dependency gate.

Portal's Inspect Transfer Plan compiles the filesystem-policy inventory
directly. It does not require rsync, SSH, main availability, or remote
preparation and does not write Lane state. `loom lane status` is its closest
read-only CLI equivalent. `loom lane send --dry-run` intentionally retains the
real-send transfer-tool preflight so that CLI send semantics do not weaken.

The automatic choice, local bundle creation/verification, safe-unpack behavior,
and local lifecycle are covered by repository tests. Production-node transfer
throughput and hardware-main acceptance remain separate operator validation;
the local evidence does not claim those results.

Lane has acknowledgement commands because not every pending or failed transfer
should be retried immediately. Acknowledgement records operator intent without
deleting history.

### Retired Dropzone Evidence

Dropzone transfer records can remain as historical evidence. They are not a
current intake mechanism and are omitted from Box status, Portal, help,
completion, and search. Compatibility inspection is read-only and must not
recreate a visible folder, policy, worker state, or transfer route.

### Storage Catalog

The storage catalog is main's database-backed record of storage entries.

A storage entry records:

- storage entry ID;
- storage class;
- source area;
- origin node;
- logical path;
- current view path;
- checksum and size;
- file class;
- processing state;
- availability state;
- retention state;
- metadata.

Physical refs connect a storage entry to retained bytes, backup artifacts,
archive objects, local paths, cloud refs, or other storage backends.

### Catalog Paths And Physical Custody

LOOM exposes stable human catalog paths backed by exact canonical physical
custody; it no longer materializes a generated filesystem tree.

Examples:

```text
main/Documents
main/Archive
macbook/Backups
macbook/Lane
```

The underlying surfaces have explicit permission semantics:

- writable areas are canonical source surfaces, such as main `Documents`;
- backup, Lane-import, and archive custody is read-only to users;
- generated projections such as Notes are read-only and rebuildable;
- controlled areas require LOOM commands rather than manual editing.

A logical path can be visible in the catalog while its physical custody remains
read-only. The direct `loom-storage` SMB share exposes canonical custody, not a
writable mirror or generated catalog tree.

### Main Documents

Main `Documents` is a writable source area on main. It has its own import
scanner, catalog rows, retention payloads, protection status, reconcile command,
and safe-delete command.

This is different from `macbook/Backups/...`, which represents watched-root
backup entries in canonical custody from another node's source folders.

### Retention And Tombstones

Retention answers whether LOOM still has bytes and metadata for a file.

Tombstones represent cataloged deletion or archival state. A tombstone is not
the same thing as deleting retained bytes immediately. It is durable metadata
that tells LOOM a source-side item was removed, archived, superseded, or made
unavailable.

Safe-delete commands depend on retention and physical refs. They should be used
before removing a source-side file.

### Filesystem Fidelity

Filesystems carry metadata beyond file bytes:

- executable bits;
- symlinks;
- package boundaries;
- xattrs;
- ACL presence;
- Apple metadata;
- empty directories;
- case and Unicode behavior.

LOOM records fidelity observations and findings so operators know when a
generated safe view differs from source filesystem metadata. A file can be
payload-safe but still have fidelity findings that matter for faithful restore.

### How This Connects To Other LOOM Features

- [[Notes Knowledge Index]] uses Box Notes and project notes roots as inputs.
- [[Backup Sync Retention And Cloud]] extends storage protection into backup
  and cloud snapshots.
- [[Projects Facets And Contracts]] uses project folders under Box Projects and
  project-owned facets.
- [[Object Store Indexes And Search]] consumes accepted objects and extracted
  text, but storage catalog state remains the file protection source.
