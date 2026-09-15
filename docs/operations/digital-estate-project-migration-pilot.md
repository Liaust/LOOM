---
title: "Digital Estate Project Migration Pilot"
description: "Run the disposable LOOM-to-ORCA project migration acceptance lifecycle without touching real projects or weakening custody checks."
audience:
  - operator
  - developer
  - agent
tags:
  - loom
  - operations
  - projects
  - backup
status: verified
verified_at: "2026-09-05"
source_scope:
  - "tests/smoke/v2_digital_estate_project_pilot.sh"
  - "internal/estatemigration/project_pilot_test.go"
  - "internal/cloudstorage/digital_estate_project_pilot_test.go"
  - "ORCA 1.4.192 public project and worktree CLI"
related:
  - "[[ORCA Main Operations]]"
  - "[[Backup Restore And Drills]]"
  - "[[Provenance Runtime Operations]]"
aliases:
  - "Project Migration Pilot"
---
# Digital Estate Project Migration Pilot

## What This Page Covers

This runbook exercises one disposable single-repository project and one
disposable multi-repository project through the complete LOOM custody and ORCA
registration lifecycle. It proves inventory, Git fidelity, reviewed
development-state migration, LOOM registration, provenance, backup, isolated
restore, ORCA folder-project behavior, and final cleanup.

The workflow is acceptance evidence for the digital-estate migration feature.
It is not authority to inventory, move, register, or delete a real project.
Real migration remains a separately reviewed production campaign.

## Custody Order

The ordering is deliberate:

1. LOOM inventories, stages, verifies, publishes, registers, backs up, fetches,
   and restores the disposable projects.
2. The harness signs an acceptance receipt that binds the exact retained roots
   and the repository inputs used to verify them.
3. Only then does an operator register each accepted root in ORCA as a folder
   project.
4. The harness proves ORCA's inherent project surface and a separate disposable
   workspace lifecycle.
5. The operator removes only the two smoke-owned ORCA projects.
6. The harness authenticates both receipts, proves exact ORCA absence,
   revalidates the retained estate, and removes its own state directory.

ORCA does not move or reinterpret the project data. LOOM acceptance remains the
custody boundary; ORCA registration follows it.

## Prepare The Disposable Estate

Run the smoke from a clean reviewed LOOM checkout. Create one unique temporary
directory and keep its value for every phase:

```bash
state_dir="$(mktemp -d /private/tmp/loom-digital-estate-project-pilot.XXXXXXXX)"
bash tests/smoke/v2_digital_estate_project_pilot.sh prepare \
  --state-dir "$state_dir"
```

`prepare` builds only disposable fixtures beneath `state_dir`. It must finish
with all thirteen acceptance checks passing and print the exact single-project
path, multi-project path, and `acceptance-state.json` receipt. It also stops its
disposable LOOM and PostgreSQL services before returning control.

Do not edit either accepted project root, the receipt, or any harness-bound
repository file between phases. A changed byte invalidates the signed handoff.

## Register The Exact Folders In ORCA

ORCA 1.4.192 supports durable non-Git folder projects through the normal
application UI. Its public `orca repo add` command does not expose folder-kind
project creation, so this narrow lifecycle has two explicit UI actions.

For each exact path printed by `prepare`:

1. Open **Add a project** in ORCA.
2. Choose **Browse folder**.
3. Select the exact accepted parent project folder.

Do not select a nested Git repository, add a wrapper `.git` directory, move the
folder, or reuse an existing ORCA project. The exact parent folder is the
accepted project identity.

The supported read-only inventory is:

```bash
orca status --json
orca project list --json
orca project setups --json
orca worktree list --json
```

Each accepted folder must appear as one local folder project, one local folder
host setup, and one inherent non-bare, non-archived main workspace at the exact
same path. Extra or substituted rows fail the next phase.

## Verify The ORCA Lifecycle

After both folder projects are visible, run:

```bash
bash tests/smoke/v2_digital_estate_project_pilot.sh verify-orca \
  --state-dir "$state_dir"
```

The verifier authenticates the acceptance receipt and retained trees before
using public ORCA commands. For a folder project, ORCA creates a disposable
non-main workspace as a distinct metadata overlay at the accepted folder path,
not as another filesystem checkout. Its identity adds a unique
`::workspace:<instance>` suffix.

The verifier requires that exact shape, removes only the disposable non-main
workspace through `orca worktree rm`, proves the two inherent main surfaces are
unchanged, rechecks both project trees, and writes a signed
`orca-verification-state.json` receipt. Do not manually create or remove a
workspace to help the smoke pass.

## Remove The Smoke-Owned ORCA Projects

Review `orca-verification-state.json`, then use ORCA's normal UI to remove only
the two exact folder projects named by that receipt. Removing the project also
removes its inherent main workspace surface; do not remove that workspace row
independently.

Before finalization, the public inventory must show that the recorded project,
setup, inherent-workspace, disposable-workspace, instance, name, and path
identities are absent. Unrelated ORCA projects or transient sidebar entries are
outside the smoke's authority and must not be changed.

## Finalize And Interpret Success

Run the last phase with the unchanged state-directory value:

```bash
bash tests/smoke/v2_digital_estate_project_pilot.sh finalize \
  --state-dir "$state_dir"
```

Healthy completion prints:

```text
[ok] ORCA project, setup, and workspace identities are absent and retained estate is unchanged
FINALIZE_COMPLETE=true
STATE_DIRECTORY_REMOVED=<the supplied state directory>
```

`finalize` authenticates both receipts, checks every recorded ORCA identity is
absent, and reruns the retained-tree verification before cleanup. It then
removes only the authenticated state directory supplied to the smoke. A failed
phase preserves the fixtures and receipts for investigation instead of guessing
that cleanup is safe.

## Fail-Closed Boundaries

Stop and inspect the preserved state when any phase reports:

- a missing, changed, or unauthenticated receipt;
- source, accepted-target, Git, or harness drift;
- a project registered before LOOM acceptance;
- the wrong ORCA path, host, kind, setup, or workspace identity;
- extra or missing inherent folder-workspace rows;
- an unproven disposable workspace create/remove lifecycle;
- incomplete ORCA removal; or
- uncertain cleanup.

Do not repair a failure by substituting a nested repository, editing a receipt,
using private ORCA runtime state, or deleting unrelated metadata. Preserve the
state directory and resolve the exact failed boundary.

## Production Boundary

This runbook validates disposable project shapes only. It does not authorize
Main access, a real digital-estate inventory, production transfer, source
cleanup, retention changes, deployment, credentials, cloud mutation, or later
migration slices. A real project remains source-owned until its own byte, Git,
LOOM registration, provenance, backup, fetch, restore, ORCA, and retained-source
evidence passes under an accepted campaign.

## Related Docs

- [[ORCA Main Operations]]
- [[Backup Restore And Drills]]
- [[Provenance Runtime Operations]]
