---
title: "Repository Development State"
description: "Default project-wide .project context and compatibility with optional repository-specific .repo state."
audience:
  - developer
  - agent
tags:
  - loom
  - developer
  - projects
  - testing
status: draft
verified_at: "2026-09-13"
source_scope:
  - "internal/repostate"
  - "internal/loomcli/repostate.go"
  - "internal/projectcontracts/repo_development_pack"
  - "tests/smoke/v2_repo_development_state_local.sh"
related:
  - "[[Developer Documentation]]"
  - "[[AI Worktree Development]]"
  - "[[Projects Facets And Contracts]]"
  - "[[Testing]]"
aliases:
  - "Portable Repository State"
  - ".repo Development State"
---
# Repository Development State

## What This Page Covers

Normal `loom project create` now generates a small project-wide `.project/`
beside `.loom/`, plus `AGENTS.md`. Start with STATE and relevant work, use
OVERVIEW for purpose (existing PROJECT.md is supported), ROADMAP for priorities
and MAP for folder roles. Features and decisions are optional; a simple task
does not require a lifecycle object, Git repository or worktree.

`.loom/project.yaml` remains the only project identity/resource declaration.
Backend creation registers that same identity without activating resources.
The 60-second context worker observes registered owner-local metadata in bounded
batches. Provenance exposes it as `project_state`, separate from accepted records.
Use `loom provenance project get <project-id> --snapshot <snapshot-id>` for the
captured Markdown excerpts and hashes behind a search result. No `.repo`
installation or manual projection command is needed for this project context.

The development installation has deployed this project context and exercised
real agent capture/retrieval. That does not automatically migrate another
installation's projects. The remainder of this page documents
the compatible legacy `.repo` pack, not the default onboarding workflow.

This page explains LOOM's optional, Git-tracked `.repo/` development-state
contract. It covers the project/repository boundary, explicit opt-in workflow,
read-only validation, portable content rules, and Codex/ORCA worktree
ownership.

LOOM itself still uses `.project/`. Do not initialize or migrate the LOOM
repository merely to follow this page.

## Who Should Read This

Read this when you lead development in a repository owned by a LOOM project,
review a proposed `.repo/` commit, or need to interpret `loom repo-state`
output. Project operators should first understand [[Projects Facets And Contracts]].

## Mental Model

Three sources have different jobs:

| Source | Owns | Must not own |
|---|---|---|
| Project `.loom/` | Project identity, explicit repository membership, policy, and the optional development pack. | Repository feature state or live harness sessions. |
| Repository `.repo/` | Portable declared repository identity, state, roadmap, lifecycle records, protocols, and handoffs. | Absolute paths, credentials, private memory, caches, session identifiers, or accepted semantic context. |
| Codex or ORCA runtime | The task/thread, worktree, terminal, recovery, and cleanup lifecycle created by that harness. | Canonical repository identity or a competing LOOM worktree record. |

Git commits are the handoff boundary between harnesses. A live worktree is
never canonical repository state, and neither Codex nor ORCA adopts, moves,
repairs, or deletes a worktree created by the other harness.

## Explicit Opt-In

Legacy LOOM project scaffolds include a source pack at:

```text
<project>/.loom/agent-packs/repo-development/
```

The pack is inert. LOOM does not install it into member repositories
automatically.

Before opting in:

1. Resolve the repository's authoritative member in
   `<project>/.loom/project.yaml`, or legacy `.loom/contracts/repos.yaml`.
2. Confirm the repository is the intended Git root and review its current
   status.
3. Run an initialization dry run with the explicit repository and pack paths
   plus the complete repository identity.
4. Review every planned path, conflict, Git posture, and the printed SHA-256
   plan digest.
5. Repeat the same inputs with `--apply`, the exact digest, and `--yes`.
6. Review the unstaged `.repo/` output, validate it against authoritative
   membership, and create the Git commit yourself.

A representative dry run is:

```bash
loom --json repo-state init \
  --path /explicit/disposable/repository \
  --pack /explicit/project/.loom/agent-packs/repo-development \
  --repository-id repo_... \
  --repository-name example \
  --role primary \
  --purpose "Own the example implementation." \
  --owner-project-id project_... \
  --owner-project-slug example \
  --stable-branch main \
  --default-branch main
```

Apply only the reviewed plan:

```bash
loom --json repo-state init \
  <the-same-path-pack-and-identity-flags> \
  --apply \
  --plan-digest sha256:<reviewed-digest> \
  --yes
```

The apply operation creates or replaces only reviewed generated targets. It
does not stage or commit. An unrelated dirty path blocks apply; existing dirt
confined to the exact planned `.repo/` targets remains visible in the plan for
review.

Use `loom repo-state migration-plan` instead of `init` only for a separately
reviewed `.project/` transformation. Migration is also dry-run-first and never
authorizes a real repository campaign by itself.

## Read-Only Validation

Validation always requires an explicit repository path. Supply all four
membership flags together when authoritative project membership is available:

```bash
loom --json repo-state validate \
  --path /explicit/disposable/repository \
  --repository-id repo_... \
  --owner-project-id project_... \
  --owner-project-slug example \
  --role primary
```

Important `tracking_status` values are:

| Status | Meaning |
|---|---|
| `not_enabled` | `.repo/` is absent. This is a supported state, not an instruction to install it. |
| `stale_version` | The manifest envelope names an unsupported exact schema version. Its body is not interpreted. |
| `malformed` | The supported source is invalid, unsafe, non-regular, untracked, or incomplete. |
| `membership_unresolved` | Portable state parsed, but authoritative owning membership was not supplied. |
| `mismatched_repository` | The declared repository ID differs from membership. |
| `mismatched_owner` | The project ID or slug backlink differs from membership. |
| `mismatched_membership` | The owning role, state root, or portable membership source facts differ. |
| `valid` | The source, Git observation, and authoritative membership agree. |

`freshness.tracked_state` is a Git observation. A dirty repository can still
have structurally valid state; the output reports `dirty` and
`repo_tree_dirty` rather than pretending the working tree is clean. Declared
fields and Git observations do not become accepted semantic truth. The
extractor therefore emits `accepted_context` as an empty collection.

## Portable Content Review

Every file under `.repo/` must be a regular Git-tracked file with a
repository-relative path. Review the complete staged diff before commit and
reject:

- host, project, or worktree absolute paths;
- credentials, token values, environment files, or authentication exports;
- Codex, ORCA, terminal, thread, conversation, or session identifiers;
- caches, locks, sockets, logs, generated indexes, or mutable runtime state;
- private agent memory, personality, hidden prompts, or harness state; and
- claims that declarations or Git observations are accepted semantic context.

Portable progress and handoffs may record branch names, full Git commits,
validation commands, review findings, and integration notes. Runtime paths and
handles stay in the owning harness.

## Harness Ownership

A Codex worker uses a Codex-managed worktree and stays on its worker branch. An
ORCA worker loads the installed ORCA guidance, creates an ORCA-managed
worktree, and stays on the branch ORCA and Git both report. In both cases:

1. verify the Git root, branch, base, and status before editing;
2. keep runtime-only identifiers out of `.repo/`;
3. commit reviewable checkpoints and portable handoff evidence;
4. stop at integrator review; and
5. let the creating harness own archive, recovery, and cleanup.

Closing or archiving a harness thread, restarting ORCA, or handing a Codex task
between Codex-owned contexts must not change the committed `.repo/` projection.

## Acceptance And Healthy Output

Run the disposable local matrix with:

```bash
bash tests/smoke/v2_repo_development_state_local.sh
```

It builds a temporary `loom` binary and owns every repository, branch,
worktree, runtime sentinel, and generated `.repo/` tree it exercises. It covers
absent, opt-in, current, stale, malformed, dirty, owner-mismatch,
multi-branch/worktree, and archived-runtime cases without contacting a node or
changing a real repository.

Healthy current state has:

- `tracking_status: valid`;
- authoritative repository and project IDs matching `repo.yaml`;
- `freshness.tracked_state: clean` for a committed unchanged tree;
- `freshness.head_commit` equal to `freshness.source_commit` when the `.repo/`
  commit is at `HEAD`;
- `accepted_context: []`; and
- no portable-content violation in the reviewed source tree.

Native ORCA restart evidence is kept in the owning feature's acceptance note,
not in `.repo/`. The local smoke deliberately does not register a real project,
restart ORCA, or retain an external harness session.

## Common Problems

- `not_enabled` when `.repo/` was expected:
  Confirm the explicit path. Do not auto-install the pack.
- `malformed` immediately after apply:
  Untracked `.repo/` output is intentionally not accepted as current portable
  state. Review it, then stage and commit explicitly if approved.
- `mismatched_owner`:
  Reconcile the project membership and repository backlink. Do not silently
  rewrite either source.
- apply blocked by Git posture:
  Review `git.blocking_paths`. Unrelated dirt must be handled by its owner
  before apply.
- harness reports a different branch or root than Git:
  Stop. Do not adopt or relocate the worktree through another harness.

## Related Docs

- [[AI Worktree Development]]
- [[Projects Facets And Contracts]]
- [[Testing]]
- [[Repository Map]]
