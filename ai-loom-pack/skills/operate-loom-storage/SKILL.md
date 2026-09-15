---
name: operate-loom-storage
description: Inspect or operate LOOM Box, Lane, Dropzone, main storage, backup, retention, fidelity, or cloud workflows when real user files, custody, protection, or safe deletion are involved.
---

# Operate LOOM Storage

Treat every non-fixture storage path as real user data. Determine ownership and
custody before proposing a write, transfer, cleanup, restore, or deletion.

## Source-Of-Truth Order

1. The explicit user scope and instructions at the source path.
2. Box `.loom/box.yaml` and area policies for local source folders.
3. Project contracts for project-owned roots.
4. Main storage catalog, physical refs, retention, transfer, and fidelity
   evidence.
5. Release-matched docs, current CLI help, and the relevant operations runbook.
6. Generated exports only as read-only representations of accepted state.

Never write directly into generated `loom-storage` backup, Lane, archive, or
projection views. Change canonical source or use the controlling LOOM command.

## Classify The Surface

- **Box Notes/Documents:** writable source controlled by Box policy.
- **Project root:** writable project source controlled by project contracts.
- **Lane:** explicit send-to-main custody staging.
- **Dropzone:** transfer-record-managed custody surface.
- **Main Documents:** writable main source with import/catalog behavior.
- **Generated storage export:** read-only or command-controlled view.
- **Backup/cloud artifact:** protection evidence, not an ordinary source tree.

If classification is unclear, inspect paths and status without changing them.

## Inspect, Plan, Apply, Verify

1. Inspect Box, transfer, catalog, retention, backup, and cloud status relevant
   to the exact path.
2. Plan with dry-run, inventory, coverage, fidelity, or safe-delete checks. For
   Lane, record the automatic transport recommendation, reason, and estimated
   temporary space before deciding whether an override is justified.
3. Classify the action and state the custody transition.
4. Apply only the reviewed command to explicit paths.
5. Verify catalog state, physical protection, destination, checksum when
   supported, and source-side safe-to-delete state.
6. Escalate ambiguity or failed convergence before retrying broadly.

Lane content policy and transport are separate decisions. Preserve the
reviewed `faithful`, `source_only`, or `exact` profile, and normally accept the
automatic `file_tree` or `bundle_seed` choice. Do not force `--bundle` or
`--no-bundle` without evidence from status or dry-run. A bundle tar and manifest
are retry/safety artifacts under hidden Lane state, not retained user storage;
verify main's extracted-file custody and catalog evidence before source-side
cleanup.

On success, expect only the current rename-based cleanup quarantine during its
30-minute grace period. The redundant file-tree safety tree or bundle artifact
is removed only after the completed record is durable, and the local node-agent
expires the successful quarantine automatically. A newer success retires the
prior successful quarantine immediately. Never interpret age,
acknowledgement, or archival as authority to remove failed, interrupted,
active, or cleanup-withheld recovery evidence. Inspect retained/protected byte
diagnostics; large protected evidence requires retry, repair, or explicit
operator cleanup, not automatic pruning.

Read [storage commands and safety gates](references/storage-commands.md) before
any mutation.

## Protect A Folder

Use the Portal's **Protect Folder** workflow for an arbitrary node-local folder;
use the CLI when an operator or automation needs the typed control surface.

1. Inspect the intended owner node and confirm the source path is local to that
   node.
2. Request preflight and wait for completion. Review the canonical path,
   truncation marker, protected/ignored counts, effective managed policy, and
   every blocking finding.
3. Create only from matching completed preflight evidence. Let main store
   canonical YAML in its configured hidden Box and route desired state to the
   selected owner node.
4. Follow lifecycle status. Treat **Waiting For Node** as durable pending work,
   **Active** as current configuration applied/reported, and **Protected** as an
   accepted backup after the current acknowledgement.
5. Recheck after path/policy changes; retry activation only after diagnosing the
   apply or connectivity cause.

Never treat an accepted or queued request as custody proof. Never bypass main
with SSH, copy canonical contract YAML to the owner, or use local
`watched-roots apply-plan` as the user-facing protected-folder path. If main is
offline, stop main-dependent actions. If the owner is offline after acceptance,
preserve **Waiting For Node** and resume when it returns.

Disable or delete only after confirming the intended contract. Deleting a
contract removes desired policy and its managed watched root; it does not
delete source files or retained backup data.

## Safety Classification

- **Inspect:** Box/path/status, Lane status, storage list/tree/inspect/doctor,
  transfer status, backup status/list/coverage, and cloud status/doctor.
- **Plan:** Lane send dry-run, cleanup inventory, fidelity backfill dry-run,
  backup/restore drill dry-runs, cloud offload plans, and safe-delete checks.
- **Safe run:** bounded transfers or repairs whose source, destination, and
  rollback/retention behavior are reviewed.
- **Sensitive:** deletion, archive, restore over existing data, production
  backup changes, cloud credentials, bulk operations, and retention changes.

`partially_safe` means payload retention exists but safe-view fidelity differs;
it is not blanket permission to delete. Quarantine is preferred to immediate
deletion. Never use destructive flags without an explicit operator decision.
Lane status, dry-runs, Portal rendering, and inspect actions are read-only and
must never be used as a housekeeping trigger.

## Production And Rebuild Boundary

Production storage validation uses focused commands and acceptance artifacts
under `.loom-acceptance`, never loose scratch files. NixOS rebuilds, production
deploys, backup/restore execution, and cloud credential changes require their
runbooks and operator authority. This skill does not grant either.

## Current Versus Future And Escalation

Verify the installed CLI and public docs before describing a transfer, cloud,
or fidelity workflow as ready. Escalate with origin node, canonical source,
view path, storage entry or transfer ID, timestamps, checksums, retention and
fidelity state, commands, and current custody. Do not guess when main is
unreachable or evidence disagrees.
