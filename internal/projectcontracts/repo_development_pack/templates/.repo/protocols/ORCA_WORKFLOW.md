# ORCA Workflow

A Codex model inside ORCA remains ORCA-owned. Use the running harness and selected
host for lifecycle; never transfer ownership based on model or vendor name.

## Ordinary edits

Follow the actual repository instructions in the current task and worktree.
Check existing changes and branch where Git exists, make the bounded change, run
appropriate tests and provide a concise handoff. No new feature slices or formal
file inventories are required unless the repository says otherwise. Absent
optional `.repo/` needs no installation or administration.

## Planned features

Use the repository's accepted plan and ORCA-native project, thread and worktree
flow when it requires an isolated worker. Verify scope, branch and accepted base;
checkpoint completed slices with validation and progress, then provide the
committed handoff for the integrator. Workers do not merge stable or integration
branches. Keep handoff, recovery and cleanup within ORCA ownership.

Load the installed `orca-cli` skill for ORCA operations and `orchestration` when
structured multi-worker coordination is needed. Follow the version-matched guidance
served by the selected ORCA CLI; this repository pack intentionally does not
duplicate subcommands or flags. Source-bound Linux proof covers patched non-Git
folder registration, not installed availability. Registration does not create or
validate directories. Use supported native operations without inventing a Git
wrapper or mutating runtime databases.

## Optional repository state

Only an explicit `.repo/` opt-in uses `REPOSITORY_STATE_PROTOCOL.md`. Stay in the
ORCA-owned worktree, review its local plan/digest and unstaged output, and commit
explicitly. Keep runtime identifiers in ORCA; LOOM never stages, commits,
archives a thread or changes ORCA ownership.
