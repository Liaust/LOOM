---
name: manage-loom-projects
description: Work in a LOOM project, manage its declarations and lifecycle, or deploy declared applications through project plan/apply/status, including approved public HTTPS endpoints.
---

# Manage LOOM Projects

## Ordinary edits

Work in known source under actual repository instructions (including `AGENTS.md`).
Start with project `.project/STATE.md` and relevant files; use OVERVIEW (or an
existing PROJECT.md), ROADMAP and MAP only when that context helps. These are
editable project-wide Markdown, not another registration manifest. Edit relevant
files, run appropriate tests and give a concise handoff.
No LOOM registration, activation, projection refresh or semantic acceptance
is needed merely to read or edit source. Do not install optional `.repo/` or
initialize Git as enrollment ceremony. Use the actual harness; Git remains optional.

Use known commands; consult [project commands](references/project-commands.md)
when needed. Optional `loom project context <ref> --node <node>` reads bounded
development context and technical status; `--verbose` adds source pointers. Neither
is a prerequisite for editing or a request to apply or refresh anything.

## When asking LOOM to manage a resource

Resolve the canonical source and requested effect. `.loom/project.yaml` owns
intent; edit its resource declarations to request management. Saving source does
not enroll it. Generated `loom-storage` views are never write targets.

Creation is caller-local unless `--backend` selects the configured backend
filesystem. An `owner_node` field does not route a local command to Main.
Normal backend creation registers the same generated identity and source, with
no resource activation. Its context is automatically observed by the bounded
project-context worker. Local-only creation stays source-only.
Plan/apply resolves on the selected owner node; verify the returned target/root.

Use reviewed plan/apply/status for management. Keep target, plan and stable request key. Historical
operation inspection does not resume it. Registration and activation are separate;
valid source is not runtime health. A deterministic projection is not accepted
semantic context. Reading or refreshing source never accepts candidates or
launches Archivist.

## Authority and credentials

LOOM host updates, NixOS rebuilds and backup/restore belong to the operator unless
delegated. Authorized application rollouts through project plan/apply belong to
the project agent. Archive/migration retains its explicit review; archived projects
are not normal mutation targets.

Proton Pass owns credential values. Current `.loom/contracts/credentials.yaml`
bindings support environment variables or absolute files. Managed application
`credential_sources` uses exact `pass://SHARE/ITEM/FIELD` references. For now,
the operator creates/registers credentials manually in Proton. When an item is missing,
specify the required account/item and fields, then ask for its reference, never
the secret in chat. Automatic generation is deferred; do not request an Editor
token upgrade or choose `pass+generate://` for current rollouts. Keep only
references in source, logs and handoffs.

## Harness and availability

Choose lifecycle by running harness, not model vendor. A Codex model inside ORCA
remains ORCA-owned; Codex-owned worktrees use native Codex controls. Use the
installed native skills; never adopt the other's worktree.

ORCA folder registration does not create its directory.

## Source custody and command scope

Project export and `.loomignore` are current. In Portal's **Export Project...**,
**Local** reads caller-visible source even when main is offline. **From Main**
requires backend access and returns the archive to caller-local output.
Every mode excludes `.loom/state/` and `.loom/tmp/`. Review overwrite/ignore rules
in the reference.

Declaration reconciliation and prepared application installation use the same
plan/apply/status workflow; node policy supplies allocation and credential access
without per-app operator grants. For deployment inputs see
[managed applications](references/managed-applications.md).
Public HTTPS beneath `apps.example.com` is part of that same workflow: declare
the hostname. No separate per-application DNS, TLS or operator grant setup is needed.
The declaration `--refresh-projections` effect remains separate. Project development
context has its own automatic metadata refresh; a manual diagnostic is
`loom provenance project sync <project-ref>`.
Source support, installed availability and live health remain distinct.
