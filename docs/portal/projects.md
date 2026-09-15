---
title: "Projects Portal"
description: "Use the Portal Projects surface to inspect layout, explicit repository state, health, source migration, exports, and archive quarantine."
audience:
  - user
  - operator
tags:
  - loom
  - portal
  - projects
status: draft
verified_at: "2026-09-15"
source_scope:
  - "internal/loomcli/portal/projects_render.go"
  - "internal/loomcli/portal/project_actions.go"
  - "internal/loomcli/portal/project_action_executor.go"
  - "internal/loomcli/portal/project_actions_test.go"
  - "tests/smoke/v2_project_repository_state_local.sh"
  - "internal/projectexport/"
  - "loom project export --help"
  - "internal/loomcli/portal/app_test.go"
related:
  - "[[Portal Guide]]"
  - "[[Projects]]"
  - "[[Projects Facets And Contracts]]"
  - "[[Projects And Scopes CLI Reference]]"
aliases:
  - "Project Surface"
  - "Portal Projects"
---
# Projects Portal

## Current Scope

The Portal still exposes legacy facet/preset and registration-oriented forms.
It is not yet a complete visual equivalent of the newer file-first declaration
workflow. For normal new-project creation and declaration plan/apply/status,
use the [current project CLI](../cli/projects-and-scopes.md). The forms below
describe compatibility surfaces, not extra steps required for a new project.
Source inspection supports this distinction; this page is not a fresh interactive
acceptance of every Portal action.

## Open Projects

```bash
loom enter --start projects
```

The Projects home groups active, paused, and archived projects. Select a
project to open the project explorer, then choose Structure, Runtime, Storage,
or Timeline.

## Project Detail

Project detail summarizes:

- lifecycle, registration, activation, runtime, and archive health;
- backend owner and project root;
- current contract drift;
- resolved source layout and contract path;
- enabled facets;
- explicit repository membership and bounded observation posture;
- the next useful action.

Layout is derived from live backend analysis when available. This avoids
presenting a stale registered path as current filesystem truth after migration.

## Structure Actions

Structure contains contract and facet work so the top-level project view stays
compact. Actions can include:

- Validate Contract;
- Re-register Contract;
- View Registration Plan;
- Add Facet;
- Run Project Doctor;
- Migrate Project Layout, only for active legacy-compatible source.

Canonical projects do not expose the migration form. Archived projects do not
expose normal Structure mutations.

## Repository State

When a registered project has repository source state, Project detail renders
the source versions and revision, member counts, observation summary, and each
explicit member's stable ID, role, relative path, Git posture, and optional
`.repo` posture. The status and inspect actions are read-only.

A missing local member is shown as `not_observed`; an unavailable remote-owned
member is `remote_unavailable`. A linked worktree may be visible as an
observation, but the Portal never presents it as canonical project state or
infers it as a member. Archived projects retain repository status and inspect
actions while every repository and project mutation remains suppressed.

## Migrate Project Layout

For a `legacy` or `canonical_with_legacy` project, open Structure and choose
Migrate Project Layout. The form defaults Dry Run to true.

Dry-run reports:

- before layout;
- ordered file actions;
- collisions;
- preserved custom files;
- follow-up commands.

After reviewing the plan, set Dry Run to false and confirm the sensitive
action. Portal sends explicit apply and confirmation to the backend. A
successful apply reports `legacy -> canonical` and the bounded migration
record.

Portal does not re-register automatically. Use Validate Contract, inspect
drift, then Re-register Contract so the backend snapshot stores
`.loom/project.yaml` and its current hash.

## Add Facet

Add Facet also defaults to dry-run. It updates canonical project control files
on the backend and can re-register after apply. It does not create a legacy
root contract beside an existing canonical project.

## Create Project

The Projects home Create Project action targets a selected node's Box Projects
area. Its form includes project name, target node, preset, facets, dry-run, and
register-after-create. New project source uses the canonical `.loom/` layout.

## Archived Project Quarantine

Archived projects remain browseable, but normal project and runtime actions are
suppressed. The project explorer exposes only archive inspection and restore
planning. Layout migration, facet addition, activation, capability calls,
schedule controls, job repair, and worker actions must not leak through other
Portal surfaces for an archived project.

If an archived project exposes a normal mutation, treat that as a Portal bug.

## CLI Equivalents

```bash
loom project validate <project-ref> --backend
loom project migrate-layout <project-ref> --backend --dry-run
loom project migrate-layout <project-ref> --backend --apply --yes
loom project register <project-ref> --backend
loom project status <project-ref>
```

See [[Projects And Scopes CLI Reference]] for safety and output details.

## Grouped Project Export

Project detail exposes one compact **Export Project...** group. It contains
human, portable, and archival variants for each source location:

| Action | Source and result |
| --- | --- |
| Export Human (Local) | Read the locally registered project root and create a person-oriented tar. |
| Export Portable (Local) | Read the local root and retain portable LOOM control material. |
| Export Archival (Local) | Read the local root and add bounded registration references to the portable source. |
| Export Human (From Main) | Build the human archive on main and download its bytes. |
| Export Portable (From Main) | Build the portable archive on main and download its bytes. |
| Export Archival (From Main) | Build the archival archive on main and download its bytes. |

A Local action is available only when the registered project root is a readable
directory on the machine running Portal. It is a local dependency and remains
available when main is offline. A From Main action is main-dependent: Portal's
normal online/degraded/offline availability gate applies, and offline mode
disables execution.

Every variant writes to the caller-local **Output Tar** path. From Main streams
the generated bytes back to that path; a pathname that exists only on main is
not a successful export result. Existing output is preserved unless the
operator deliberately enables **Overwrite** for the reviewed path.

All three modes use the shared file-policy resolver and deterministic tar
writer:

- `human` omits `.loom/`, `.loom-acceptance`, and only a root `AGENTS.md` with
  LOOM's managed-entry marker; a user-authored root `AGENTS.md` survives;
- `portable` includes project identity, contracts, agent guidance, templates,
  tools, `.loom/.gitignore`, and `.loomignore`, while mandatory
  `.loom/state/` and `.loom/tmp/` remain excluded;
- `archival` adds a bounded manifest of backend registration references but
  never resolves or copies credential values.

Project and nested `.loomignore` files use Gitignore-style matching, `!`
negation, parent-before-child ordering, and last-match-wins behavior. The
`.loomignore` file itself remains portable. LOOM does not import `.gitignore`,
because source-control exclusions do not establish recoverability; `.git`
therefore remains eligible unless an explicit `.loomignore` rule excludes it.
Mandatory runtime exclusions cannot be negated, and traversal or escaping
symlinks fail safely rather than broadening an archive.

## Related Docs

- [[Projects]]
- [[Projects Facets And Contracts]]
- [[Projects And Scopes CLI Reference]]
- [[Portal First Tour]]
