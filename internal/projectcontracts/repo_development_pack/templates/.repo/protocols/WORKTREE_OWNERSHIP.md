# Worktree Ownership

One harness owns each worktree from creation through cleanup. Choose by the
running harness, not the model vendor: a Codex model inside ORCA remains ORCA-owned.

- Codex-native tasks retain Codex-managed worktree ownership.
- ORCA-native tasks retain ORCA project/thread/worktree ownership.
- Neither harness adopts, relocates, repairs or deletes the other's worktree.
- LOOM supplies no competing worktree manager.
- A live worktree is never canonical repository state.

The portable boundary is the Git branch, committed checkpoints, validation and
handoff evidence. Keep unique dirty work in its owning harness until committed,
deliberately discarded by an authorized operator, or recovered natively.

Reading repository context needs no worktree creation or `.repo/` opt-in.
Repository-state commands target only the explicit current harness-owned Git
worktree and never claim its lifecycle. Explicit opt-in and apply rules live in
`REPOSITORY_STATE_PROTOCOL.md`; the owning harness reviews output and commits.

Store portable branch and commit facts in feature progress when used. Keep
absolute worktree paths, session/terminal IDs and cleanup state in the harness,
never in portable `.repo/` files.
