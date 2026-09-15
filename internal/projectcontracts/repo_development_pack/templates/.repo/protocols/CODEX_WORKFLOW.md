# Codex Workflow

This route is for the Codex running harness. A Codex model inside ORCA follows
`ORCA_WORKFLOW.md`; the model vendor does not select worktree ownership.

## Ordinary edits

Follow the actual repository instructions in the current task and worktree.
Check existing changes and branch before editing, make the bounded change, run
appropriate tests and provide a concise handoff. No new feature slices or formal
file inventories are required unless the repository says otherwise. Absent
optional `.repo/` needs no installation or administration.

## Planned features

Use the repository's accepted plan and native Codex task/worktree flow when it
requires an isolated worker. Verify scope, branch and accepted base; checkpoint
completed slices with validation and progress, then provide the committed handoff
for the integrator. Workers do not merge stable or integration branches.

Use Codex-native task, worktree, handoff and review controls. Load the installed
`openai-docs` skill for current mechanics; this pack does not duplicate product commands.
Codex Handoff moves Codex-owned task context; Git merge is separate. Keep native
ownership through recovery and cleanup. If a required native flow is unavailable,
report the limitation; use only an explicitly accepted fallback.

## Optional repository state

Only an explicit `.repo/` opt-in uses `REPOSITORY_STATE_PROTOCOL.md`. Stay in the
Codex-managed worktree, review its local plan/digest and unstaged output, and
commit explicitly. LOOM never stages, commits or changes Codex ownership.
