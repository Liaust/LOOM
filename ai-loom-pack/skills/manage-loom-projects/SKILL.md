---
name: manage-loom-projects
description: Plan LOOM integration, manage projects, deploy applications, and report friction.
---

# Manage LOOM Projects

For new repos/infrastructure or friction, see [integration planning](references/integration-planning.md).
Use the existing plan/report, not another approval document.

## Ordinary edits

Follow `AGENTS.md`, read files directly, test and hand off. Source work needs no
enrollment, activation or semantic acceptance. Git and `.repo/` are optional.

Implement substantial features and fixes directly when authorized, including
complex/unfamiliar work. Delegation is optional. Complete authorized application
rollouts within node policy; involve another owner only for conflicting work or
missing effect/access scope.

Use known commands or [project commands](references/project-commands.md).
`loom project context <ref> --node <node>` optionally reads context/status.

## When asking LOOM to manage a resource

Resolve the canonical source and requested effect. `.loom/project.yaml` owns
intent; edit its resource declarations to request management. Saving source does
not enroll it. Generated `loom-storage` views are never write targets.

Separate Notes indexing, client selection and placement; see
[Notes pipeline](references/notes-pipeline.md). Existing collections can retain
older folder names; applying source does not verify client layout.
Use one project-root `files` resource with inherited `files.rules`, not descendant
redeclarations. Preserve compatibility selections.
Original `loom files ls/stat/read/explain` needs no indexing.

Creation is caller-local; `--backend` selects backend creation/registration without
activation. `owner_node` does not route commands. Verify the plan's target/root.

Review plan/apply/status; retain target, plan and stable request key. Inspection
never resumes operations. Registration isn't activation/health; refreshes never
accept semantic context or launch Archivist.

## Work state and Notes views

`project.work_state` classifies work; passive must not pause services, schedules,
intake or editable Notes. For classification-only registration and archive/view
rules, read [project commands](references/project-commands.md).

## Authority and credentials

LOOM host updates, NixOS rebuilds and backup/restore belong to the operator unless
delegated. Authorized application rollouts through project deploy/plan/apply belong to
the project agent. Archive/migration retains its explicit review; archived projects
are not normal mutation targets.

Proton Pass owns reusable credential values. `.loom/contracts/credentials.yaml` supports
environment-variable or absolute-file references; managed `credential_sources`
uses exact `pass://SHARE/ITEM/FIELD` references. the operator registers missing items
manually: specify account/item/fields and ask for its reference, never a secret.
Automatic generation, Editor token upgrades and `pass+generate://` are deferred.
For already-authorized Google OAuth, the supported protected import can return
an opaque owner-bound `runtime://...` reference without repeating consent.
Keep only references in source, logs and handoffs.

## Harness and availability

Use each harness's native controls. Opening elsewhere never transfers
ownership. ORCA registration is retired; it is not part of LOOM registration.

## Source custody and command scope

Export respects `.loomignore`. Portal **Export Project...** Local reads
caller-visible source offline; From Main needs connectivity. Output is caller-local,
excluding `.loom/state/` and `.loom/tmp/`. Keep credential values out of exports.

For private Python, Cargo/Rust or locked Nix-native workers, declare source/run and genuine
settings, then `project deploy --check` / `project deploy --yes`. LOOM packages,
binds and supervises them; do not manufacture deployment sidecars or a repository.
Existing prepared applications retain plan/apply/status. Node policy supplies
allocation and credentials without manual per-app grants. See
[managed applications](references/managed-applications.md).
Public HTTPS beneath `apps.example.com` is part of that same workflow: declare
the hostname. No separate per-application DNS, TLS or operator grant setup is needed.
Project metadata refreshes automatically; provenance sync is diagnostic.

Use [schedules](references/project-schedules.md) for native Hermes agent/profile
tasks, standalone Main Codex dispatch and ordinary LOOM capability schedules.
Personal routines need no dummy project; declared agents share plan/apply/status.
Coding workers need no persona.
