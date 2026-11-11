# Worktree Ownership

One harness owns each worktree through cleanup. Choose by the running harness,
not the model vendor. Historically, a Codex model inside ORCA remains ORCA-owned.
ORCA is retired; do not launch it as a fallback or erase legacy work.

- Codex-native tasks retain Codex-managed worktree ownership.
- Hermes owns its native sessions and delegation.
- Retained ORCA work is legacy, not a current lifecycle default.
- Neither harness adopts, relocates, repairs or deletes the other's worktree.
- LOOM supplies no competing worktree manager.
- A live worktree is never canonical repository state.

The portable boundary is the Git branch, committed checkpoints, validation and
handoff evidence. Keep unique dirty work in its owning harness until committed,
deliberately discarded by an authorized operator, or recovered natively.

Reading context needs no worktree creation or `.repo/` opt-in. Repository-state
commands target the explicit current Git worktree, never its lifecycle.
`REPOSITORY_STATE_PROTOCOL.md` owns opt-in/apply rules; the harness reviews output.

Store portable branch and commit facts in feature progress when used. Keep
absolute worktree paths, session/terminal IDs and cleanup state in the harness,
never in portable `.repo/` files.
