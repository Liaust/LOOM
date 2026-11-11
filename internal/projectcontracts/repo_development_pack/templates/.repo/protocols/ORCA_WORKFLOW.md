# ORCA Workflow (Legacy)

ORCA is retired. Do not launch it, load legacy skills or use it as a fallback.
Use native Codex/Hermes controls for current work. Historically, a Codex model
inside ORCA remains ORCA-owned; a model name never transfers lifecycle ownership.

## Ordinary edits

Follow the actual repository instructions in the current task and worktree.
Check existing changes and branch where Git exists, make the bounded change, run
appropriate tests and provide a concise handoff. No new feature slices or formal
file inventories are required unless the repository says otherwise. Absent
optional `.repo/` needs no installation or administration.

## Planned features

Use the accepted plan and current native harness. Verify scope and base;
checkpoint slices with validation and progress for the integrator. Workers do
not merge integration branches. Preserve legacy pending edits, conversations,
non-Git workspaces and recovery history; do not adopt or delete them automatically.

Do not load `orca-cli` or `orchestration`. Historical version-matched guidance
describes the retired client only. This compatibility page does not
duplicate subcommands or flags or establish installed availability. Never mutate
runtime databases or fabricate a Git wrapper to recover legacy work.

## Optional repository state

Only explicit `.repo/` opt-in uses `REPOSITORY_STATE_PROTOCOL.md`. Review the
current worktree's plan and output; commit explicitly. LOOM never stages,
commits or transfers legacy ownership.
