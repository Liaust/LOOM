---
title: "Projects"
description: "Choose an existing project and repository, inspect source versus registered state, and understand safe creation, export and archive boundaries."
audience:
  - user
  - operator
  - agent
tags:
  - loom
  - user-guide
  - projects
status: draft
verified_at: "2026-09-13"
source_scope:
  - "internal/loomcli/project_contracts.go"
  - "internal/loomcli/project_repositories.go"
  - "internal/loomcli/project_physical_archive.go"
  - "internal/loomcli/repostate.go"
  - "internal/repostate"
  - "internal/projectexport"
  - "internal/filepolicy"
  - "internal/loomcli/portal/project_actions.go"
related:
  - "[[Getting Started]]"
  - "[[External Agents And AI LOOM Pack]]"
  - "[[Projects Facets And Contracts]]"
  - "[[Projects And Scopes CLI Reference]]"
  - "[[Projects Portal]]"
  - "[[Canonical Paths]]"
  - "[[Command Safety]]"
aliases:
  - "Project Workflow"
---
# Projects

## Start With Ordinary Source

A normal project starts with one small layout:

```text
project/
  AGENTS.md
  .loom/project.yaml
  .project/
    OVERVIEW.md
    STATE.md
    ROADMAP.md
    MAP.md
    protocols/WORKFLOW.md
  notes/
  repos/
```

`.loom` owns identity and resources you explicitly ask LOOM to manage.
`.project` is editable project-wide context: purpose, progress, priorities and
folder roles. `notes`, `repos` and any folders you add are ordinary folders,
not automatically activated features. No Git repository is created. Existing
custom `.project`, `.repo` and instructions are preserved, not migrated.

Backend creation connects the same identity to the project registry. The
bounded `main.project_context_refresh` worker observes `.project` metadata on
a 60-second interval, at most 20 registered projects per pass. It does not crawl
the project or run an agent. This lets Provenance find a project even when it
has no repositories. File declarations remain distinct from accepted decisions.
This worker must be enabled and healthy in your installation; source files alone
do not prove that a running backend has observed the latest context.

## Find The Existing Project First

A LOOM project is a scope with an owning node, portable contracts and explicit
repository membership. It is not every folder an agent happens to open.
For daily work, find the existing project before scaffolding another:

```bash
loom project list
loom project inspect <project-ref>
loom project status <project-ref>
```

Replace angle-bracket placeholders with identifiers returned by inspection;
they are illustrative syntax, not literal shell arguments. Check the owner,
source root, lifecycle and registered contract before making changes. This
page's examples were checked against source/local help on 2026-09-10; no live
project was created, registered, moved or run for documentation verification.

In ORCA, choose the accepted project/repository folder and follow its own
`AGENTS.md`. The existing Main agent can help locate it, but a conversation
does not itself establish membership or permission. A coding worktree remains
an isolated checkout owned by its harness; completing it does not deploy a
project or make it the canonical runtime root.

## Inspect The Files On Their Owning Node

`status` describes declaration readiness. To inspect current filesystem contracts
on the configured backend:

```bash
loom project validate <project-ref> --backend
loom project plan <project-ref> --backend
loom project doctor <project-ref> --backend
loom project diff <project-ref> --backend
```

For a Main-owned project inspected from Mac, `--backend` asks the configured
backend to inspect its filesystem. Confirm that the backend is Main; the flag
does not download the project or reach an arbitrary owner through SSH.
For a local folder, use its path and omit the flag, for example
`loom project validate /path/to/project`.

Healthy results identify the intended project, resolved contract and layout.
`canonical`, `legacy` and `canonical_with_legacy` describe layout, not runtime
health. Compare validation findings and current-versus-registered differences.
A source change may legitimately require registration; investigate the diff
before updating the registry.

## Choose A Repository And Its Development State

Projects explicitly declare zero, one or many repository members:

```bash
loom project repos list <project-ref>
loom project repos inspect <project-ref> <repository-ref>
loom project repos status <project-ref>
```

Declaration repository paths are relative to the project; legacy member paths
are relative to the `repos/` facet. LOOM does not infer
membership from child Git folders, aliases, archive retention or linked
worktrees. A linked worktree observation is not canonical project state.
Lists/status support bounded `--limit` and `--after` pagination; `--json`
exposes the typed response.

| Observation | Interpretation |
|---|---|
| `observed` | The current observer inspected the member; branch, HEAD, dirty state and optional development state may be available. |
| `not_observed` | A required local path or input was absent; no Git state is invented. |
| `remote_unavailable` | The owner is not observable through this backend; no raw SSH fallback is performed. |
| `.repo` `not_enabled`, `invalid` or `mismatched` | Optional development state is absent or fails its identity/validation contract; inspection does not rewrite it. |

Project `.loom/`, `.project/` and optional repository `.repo/` serve different purposes:

- `.loom/project.yaml` and `.loom/contracts/` define project identity, facets,
  membership and runtime contracts. `.loom/agents/` supplies project guidance.
- Optional Git-tracked `.repo/repo.yaml` and development objects describe the
  repository's identity, purpose, accepted features, decisions and handoffs.
  They do not store live terminals, worktree paths, session IDs or secrets.
- Normal projects use `.project` context without `.repo` enrollment. Legacy
  project scaffolds carry an opt-in development pack under
  `.loom/agent-packs/repo-development/`; they do not initialize `.repo/` in
  every repository. Inspect an opted-in local repository with
  `loom repo-state validate --path /path/to/repository`.
- Initialization/migration is a separate reviewed, digest-bound apply. It does
  not stage or commit Git changes, and preserves historical `.project/`
  planning. Follow the repository's existing instructions instead of migrating
  it just because this feature exists.

The repository contains checks for these contracts. Verify your own harness's
instruction discovery; repository membership does not prove that an agent has
loaded project context into its current session.

## Create Or Extend Only The Intended Project

For an authorized new Main project, preview the target and generated files:

```bash
loom project create <project-name> --backend --owner-node main --dry-run
```

Removing `--dry-run` creates the default source tree and registers its same
identity on the configured backend. It activates no application or resource.
Without `--backend`, creation is local-only and source-only. If registration
fails after creation, the result retains the files and ID with
`source_created/context_pending`; retry the same command after resolving the
prerequisite. It will not reset edited context. Credential values never belong
in either context or contracts.

For normal projects, edit resource declarations in `.loom/project.yaml` only
when LOOM should manage a folder or service, then review the plan/apply result.
Use the planner's returned next command rather than inventing prerequisite
operations. A normal declaration flow is:

```bash
loom project context <project-ref>
loom project plan <project-ref>
loom project apply <project-ref> --plan-id <reviewed-plan-id> --idempotency-key <request-key>
loom project status <project-ref>
```

The plan reports required grants, credentials, application prerequisites and
optional publication. Apply runs only the declared operations. Keep the original
request key when recovering a lost response; use the returned operation and
resume instructions for a partial operation. Context retrieval remains useful
even when deployment prerequisites are incomplete. Ordinary code edits require
neither an apply nor a new registration.

## Legacy Layout Compatibility

The following facet/layout commands remain legacy compatibility operations,
not the new project setup workflow.

To extend an existing project, preview
`loom project facet add <project-ref> notes --backend --dry-run`.
Removing `--dry-run` writes the facet and normally re-registers the backend
contract. For a legacy layout, preview
`loom project migrate-layout <project-ref> --backend --dry-run`; mutation
requires `--apply --yes` after reviewing collisions and preserved files.
Layout migration does not implicitly register the result. Revalidate and
inspect its diff before an authorized
`loom project register <project-ref> --backend`.

Do not use overwrite or cleanup options to bypass a collision. Archived and
restored-inactive guards may correctly refuse mutation.

## Export A Copy Without Changing Lifecycle

An export writes a caller-local tar. It is useful for a reviewed handoff or
portable copy; it is not project archival or permission to publish its contents:

```bash
loom project export <project-path> --mode portable --out /path/to/.loom-acceptance/project.tar
```

Choose an existing approved output parent. `--backend` selects backend source
and downloads the archive bytes to the caller's `--out` path. Do not count a
remote pathname alone as successful delivery. Review size, checksum, included
and ignored counts. Existing output replacement requires explicit
`--overwrite`.

`human` omits `.loom/`, `.loom-acceptance` and only the managed root
`AGENTS.md`; custom root guidance is preserved. `portable` carries portable
contracts/guidance but excludes runtime state/temp paths. `archival` adds a
bounded registration-reference manifest without credential values. None
deactivates the source project.

Root/nested `.loomignore` rules use Gitignore-style ordering and negation.
Mandatory `.loom/state/` and `.loom/tmp/` exclusions cannot be negated;
`.loomignore` itself is retained. LOOM does not import `.gitignore`, because
version-control exclusions do not establish recovery coverage.

## Archive And Restore Require A Lifecycle Review

Physical project archive deactivates project-owned runtime and moves custody
through a reviewed operation. The current source exposes these review surfaces:

```bash
loom project archive plan <project-ref> --reason "finished"
loom project archive inspect <project-ref>
loom project archive restore plan <project-ref> --reason "return files for review"
```

A review identifies source/destination, inventory, runtime deactivations,
operation and exact project digest. Apply is a separate operator action:
`archive apply` or `archive restore apply` takes the project reference,
reviewed compact JSON file, matching `--plan-digest` and `--yes`. Recovery
binds the original operation/digest. Never replace this with an unreviewed
move, copy, broad retry or a bare legacy archive command.

Historical snapshot archive readers remain for older evidence; their flags
are not interchangeable with physical plan/apply. Do not infer an archive's
readability or recovery status from a test performed on another installation.

**Restoring files leaves runtime inactive.** Services, schedules, capabilities
and watched-root writers remain fenced. Reactivation requires its own supported
workflow; an inactive restore is not proof that application service has resumed.
Archived history remains inspectable, but retention does not establish active
repository membership or current Provenance. Check the source/currentness fields
in retrieval results rather than treating historical records as live state.

## Portal And Troubleshooting

Open `loom enter --start projects`, select the project, and use Structure for
contract/layout findings or the repository details for observed state. Use the
project's review/confirmation flow for supported lifecycle changes. Export
Project offers Local or From Main; both target a caller-local output file.
Local export can use readable local source offline, while From Main requires
its backend. No new portal acceptance ran for this page.

If identity, ownership, source path or runtime state disagrees, preserve the
finding and inspect the original contract before changing anything. A missing
remote repository, stale registered snapshot, historical archive and
restored-inactive guard are different conditions. Use [[Projects Portal]],
[[Projects And Scopes CLI Reference]] and [[Command Safety]] for the next
scoped inspection.
