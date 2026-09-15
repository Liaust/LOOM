---
title: "AI Worktree Development"
description: "Use agents and optional parallel worktrees without adding unnecessary ceremony or mixing runtime ownership."
audience: [developer, agent]
tags: [loom, developer, ai-workflow]
status: draft
verified_at: "2026-09-15"
source_scope: ["AGENTS.md", "CONTRIBUTING.md", "internal/projectcontracts/project_development_pack"]
related: ["[[Developer Documentation]]", "[[Local Development]]", "[[Repository Development State]]", "[[Testing]]"]
aliases: ["AI Development Workflow", "Worktree Development"]
---

# AI Worktree Development

## Start With The Work, Not A New Control System

Use the current checkout or an ordinary branch for a focused change.
Worktrees are useful when independent changes genuinely run in parallel;
they are not required for every edit or every agent conversation.

Public contributions use GitHub issues and PRs. Read the root instructions and
the relevant source, agree the scope, implement, and report focused checks.
A small fix does not require a feature folder, multi-stage dispatch plan,
custom tester or a second agent to review it.

## Context In User Projects

New LOOM projects include a short `AGENTS.md` and project-wide `.project/`
context beside their `.loom/` declarations. Read current state and the files
relevant to the task. Ordinary edits do not require a registration command.

Git worktrees contain tracked content from their selected repository revision.
They do not automatically include an enclosing project's untracked context or
parent directories. If a component repository excludes the parent project's
`.project/`, pass the relevant context and source references in the handoff.
Do not create a second canonical project-state tree to compensate.

## Parallel Work

Give each independent task a branch and clear ownership. Check Git root,
branch, HEAD and uncommitted changes before editing. The harness that creates a
worktree owns its runtime lifecycle: use ORCA for ORCA-managed worktrees and
Codex for Codex-managed worktrees. A plain Git checkout can use ordinary Git.

Do not adopt, move or delete another harness's worktree as an incidental cleanup.
Closing a conversation does not delete the underlying code or establish that
its branch was integrated.

Keep shared files coordinated. Commit reviewable changes and provide the
resulting commit, changed behavior, validation and remaining issues.
Integrate branches deliberately, then check affected behavior once on the
combined result. Updating a shared roadmap is the coordinator's responsibility,
not proof that a feature has been deployed.

## Authority And Validation

A coding task does not inherit a named agent's identity, private memory,
credentials or broad machine access. Repository access is not authorization for
a host deployment, destructive migration, public exposure or a credential change.

Use focused unit checks and direct integration checks on a machine/data set you
are authorized to use. Do not construct a replica environment merely to test a
patch. Report unavailable dependencies and skipped tests accurately. A passing
mock or schema check is not real service acceptance.

For larger work, a short plan and progress/handoff file can help. Treat slices
as engineering checkpoints, not automatic requests for human approval after
every step. Ask when a genuine product decision, missing authority or unresolved
failure requires it.
