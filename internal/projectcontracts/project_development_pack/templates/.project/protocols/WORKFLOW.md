# Project Workflow

Read current state and the relevant task, not every project document. Keep
context concise and update it after meaningful changes. Features and decisions
may have their own files when useful; they are not mandatory for ordinary edits.

## Coding And Worktrees

Check the actual branch and existing edits; preserve work by other agents.
Use a native worktree only when independent work benefits from isolation.
Native Codex owns its threads/worktrees, including a CLI opened through ORCA.
Use ORCA controls for ORCA-owned folder, terminal and shared-page surfaces;
opening a worktree elsewhere does not transfer ownership. Load relevant harness
instructions only when needed. LOOM does not manage another set of worktrees.

Git worktrees contain tracked files at the selected revision, not necessarily
uncommitted edits. If this project's context is inside that repository it travels
with the checkout. Otherwise the coordinator supplies relevant parent-project
context and source references in the task handoff, not a second canonical copy.

Workers update assigned source and report results with focused check evidence.
The coordinator integrates shared STATE/ROADMAP changes; branch-local completion
is not automatically integrated project truth. The creating harness owns cleanup.

For a useful saved-conversation handoff, record its exact thread ID, host, actual
cwd and role in `.project/CONVERSATIONS.md`. Use the creating harness's returned
ID or trusted explicit context; unknown stays unknown. Interactive coding chats
and scheduled verification conversations are distinct. This is a handoff index,
not transcript storage, a second session manager or proof of a Desktop project.

## Context And Provenance

Project context describes observed work and declarations. It does not approve
deployment or make prose an accepted semantic decision. Use the installed
Provenance skill and `loom provenance candidate register` to capture durable
source-backed decisions during normal work, not only after a search. Inspect exact
records and resolve contradictions. Keep candidates separate from accepted records.
Keep actual producer/task attribution and replay receipts. Skip routine edits and
debugging; no capture quota. Use Main's ledger rather than another host's ledger.
Use Notes for source material and Objects for technical state, not as substitute
decision ledgers. No CLI enrollment is needed for each ordinary new folder.
